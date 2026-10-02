package httpapi_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoRouteBuildsAnActorFromRequestData is the call-site half of ADR 0050.
//
// `realtime.Actor` cannot be decoded — its `UnmarshalJSON` refuses, and that is asserted in
// `internal/realtime`. This asserts the other half: that no handler in this tree builds one
// out of something the client chose. A type-level guard only covers `encoding/json`, and
// `encoding/json` is not the only way a request reaches a value: a query parameter, a form
// field, a header and a cookie are all client-chosen and all trivially available to a handler.
//
// ## Why this is a variable-resolution audit and not a grep
//
// The first version of this test looked for `realtime.Actor` inside the *arguments* of a
// reader call, and it passed on the tree. The mutation that proved it useless was a handler
// doing exactly what the rule forbids:
//
//	forged := realtime.Actor{}
//	json.NewDecoder(r.Body).Decode(&forged)
//
// Nothing in that snippet mentions the type at the call, because Go passes **variables**. A
// syntactic audit of call sites would have shipped a green light wired to nothing, which this
// repository has now paid for twice — once in the a11y gate, once in the settle filter.
//
// So the audit resolves one hop: for every `realtime.NewActor` call it walks the arguments,
// and where an argument is an identifier it follows that identifier's initialiser in the same
// function. One hop is the honest limit and it is stated here rather than implied: two hops of
// aliasing would defeat it. The alternative — full type checking with `go/types` — is the right
// tool if this ever has to survive an adversary rather than a refactor, and it is recorded as
// the gap rather than quietly left.
func TestNoRouteBuildsAnActorFromRequestData(t *testing.T) {
	t.Parallel()

	readers := clientChosenReaders()

	// The calls this audit *watches* are the constructors, and the calls it looks for
	// inside their arguments are the readers. Passing the readers as the watch set
	// instead is a mistake this test's first version made, and it made it silently: the
	// walk visited reader calls and asked each one for its own initialiser, so it
	// examined `r.URL.Query().Get("x")` and never `NewActor`, and it reported nothing
	// on a tree where a handler built an actor straight from a query parameter. Two
	// sets, named separately, because "what am I looking at" and "what am I looking
	// for" are different questions.
	found, err := walkHandlers(map[string]bool{"NewActor": true}, func(
		fn *ast.FuncDecl,
		call *ast.CallExpr,
	) []string {
		return readerArguments(call, fn, readers)
	})
	if err != nil {
		t.Fatalf("walk the handlers: %v", err)
	}

	for _, hit := range found {
		t.Errorf(
			"%s: %s reads %s into the identity a dispatch runs as; an actor's fields "+
				"are unexported and its UnmarshalJSON refuses precisely so that the "+
				"campaign and the user cannot come from a request (ADR 0050)",
			hit.file, hit.where, hit.reader,
		)
	}
}

// TestAnActorIsOnlyBuiltWhereAnAccessGateRan is the other half of the same record.
//
// A dispatch takes an `Actor`, a route builds the `Actor`, and so the property that makes the
// seam safe is [0024]({{ "decisions/0024-authorisation-gates-mount-not-per-handler/" | relURL }})'s:
// authorisation is a gate the route mounts, not a check inside a handler. This does not
// re-audit the gates — `internal/httpapi/campaigns` owns that — it asserts that the gates are
// still reachable from this tree, because the failure mode of moving a route is that it moves
// *out of the audited directory* and every audit over that directory goes quiet at once.
//
// A test that finds nothing here has stopped seeing anything, so the empty case is a failure
// rather than a pass. That is the [a11y gate's]({{ "contributing/spec/" | relURL }}) lesson
// applied: `go test -run` exits 0 when the pattern matches nothing, so a guard with no positive
// case is indistinguishable from a guard that passed.
func TestAnActorIsOnlyBuiltWhereAnAccessGateRan(t *testing.T) {
	t.Parallel()

	gates := map[string]bool{
		"RequireRead":  true,
		"RequirePlay":  true,
		"RequireEdit":  true,
		"RequireAdmin": true,
	}

	mounted, err := walkHandlers(gates, func(_ *ast.FuncDecl, call *ast.CallExpr) []string {
		return []string{selectorName(call)}
	})
	if err != nil {
		t.Fatalf("walk the handlers: %v", err)
	}

	if len(mounted) == 0 {
		t.Fatal(
			"no handler under internal/httpapi mounts an access gate; either the route " +
				"table moved out of this tree or the audit has stopped seeing it, and " +
				"ADR 0050's dispatch rests on the gates being here",
		)
	}
}

// clientChosenReaders are the calls that return something the client chose **and that
// the identity must not be built from**.
//
// `PathValue` is deliberately absent, and its absence is the rule rather than an
// oversight: ADR 0050 says a route builds an actor from "the session the identity
// middleware resolved and **the campaign the URL path named**", so reading the campaign
// out of `/c/{campaign}/…` with `r.PathValue` is the sanctioned construction and a
// half-written first version of this set that included it reported `edit/edit.go`.
//
// `Cookie` is here for the same reason in reverse: the session cookie *is* how the
// user is found, but it is found by the identity middleware, which resolves it to a
// `domain.Requestor` on the context. A handler reading a cookie itself to learn who
// someone is has stepped around the middleware that validates it.
func clientChosenReaders() map[string]bool {
	return map[string]bool{
		// Body and JSON.
		"Decode": true, "Unmarshal": true, "UnmarshalJSON": true,
		// Query and form — a client may name the campaign in the path or not at all,
		// but never in a parameter that disagrees with the path.
		"Get": true, "PostFormValue": true, "FormValue": true,
		// Headers and cookies, for the middleware reason above.
		"Cookie": true,
	}
}

// readerArguments returns the names of any client-chosen readers reachable from call's
// arguments, following one hop of local variable initialisers.
//
// The hop is a `map[string]ast.Expr` of `x := …` and `var x = …` in the enclosing function,
// built fresh per function so an identifier in one handler cannot be resolved against a
// declaration in another — which is what a package-level index would do, and it would resolve
// most calls to nothing.
func readerArguments(
	call *ast.CallExpr,
	fn *ast.FuncDecl,
	readers map[string]bool,
) []string {
	if fn == nil || fn.Body == nil {
		return nil
	}

	locals := localInitialisers(fn)

	var found []string

	// `seen` is the loop guard. An initialiser that refers to its own variable, or a
	// cycle the one-hop rule would otherwise walk forever, shows up as a test that never
	// finishes — and a hung audit reports nothing at all, which is the one outcome this
	// file cannot afford.
	seen := map[string]bool{}

	var scan func(node ast.Node)

	scan = func(node ast.Node) {
		ast.Inspect(node, func(child ast.Node) bool {
			switch typed := child.(type) {
			case *ast.Ident:
				init, ok := locals[typed.Name]
				if !ok || seen[typed.Name] {
					return true
				}

				seen[typed.Name] = true
				scan(init)

				return true

			case *ast.CallExpr:
				selector, ok := typed.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}

				if readers[selector.Sel.Name] {
					found = append(found, selectorName(typed))
				}

				return true

			default:
				return true
			}
		})
	}

	for _, arg := range call.Args {
		scan(arg)
	}

	return found
}

// localInitialisers maps each locally declared name to its initialiser, for one function.
//
// The arities are deliberately *not* required to match, because the commonest way a handler
// turns a request into a number is `id, _ := strconv.ParseInt(v, 10, 64)` — two names, one
// value. Requiring a match skipped exactly that, and the mutation that proved it useless was
// an actor built from `id`. The mapping is positional where it can be and falls back to the
// first value otherwise, which is the only reading available when one call feeds several
// names.
func localInitialisers(fn *ast.FuncDecl) map[string]ast.Expr {
	initialisers := map[string]ast.Expr{}

	ast.Inspect(fn.Body, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) == 0 {
			return true
		}

		// `=` rather than `:=` is excluded by the token, not by the arity: a plain
		// `x = …` says nothing about where `x` came from, and treating it as a
		// declaration is how an audit ends up resolving a name to its last assignment
		// rather than its first.
		if assign.Tok != token.DEFINE {
			return true
		}

		for i, lhs := range assign.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok {
				continue
			}

			value := assign.Rhs[0]
			if i < len(assign.Rhs) {
				value = assign.Rhs[i]
			}

			initialisers[ident.Name] = value
		}

		return true
	})

	return initialisers
}

// walkHandlers parses every non-test Go file under this package's directory and calls
// `inspect` on each call expression, collecting one hit per name `inspect` returns.
func walkHandlers(
	interesting map[string]bool,
	inspect func(fn *ast.FuncDecl, call *ast.CallExpr) []string,
) ([]handlerHit, error) {
	fileSet := token.NewFileSet()

	// The package directory, which is also the test's working directory and therefore
	// every route handler in this tree. Named so a failure can say what it walked.
	const root = "."

	var hits []handlerHit

	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			// `testdata` holds fixtures that are *meant* to be malformed, and
			// walking into it would make this audit report on its own test data.
			if entry.Name() == "testdata" {
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		parsed, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}

		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}

			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}

				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !interesting[selector.Sel.Name] {
					return true
				}

				for _, what := range inspect(fn, call) {
					hits = append(hits, handlerHit{
						file:   path,
						where:  fn.Name.Name + " → " + selectorName(call),
						reader: what,
					})
				}

				return true
			})
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}

	return hits, nil
}

// selectorName renders a call as `receiver.Method`, falling back to `?.Method` when the
// receiver is itself a call — which it usually is, and which is why a failure message
// otherwise reads `Get` with no indication of what it was called on.
func selectorName(call *ast.CallExpr) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "call"
	}

	receiver := "?"
	if ident, ok := selector.X.(*ast.Ident); ok {
		receiver = ident.Name
	}

	return receiver + "." + selector.Sel.Name
}

// handlerHit is one thing an audit wants reported.
type handlerHit struct {
	// file is the path, relative to this package's directory.
	file string
	// where names the function and the call, so a failure points at a line.
	where string
	// reader is the client-chosen call that was found.
	reader string
}
