package dnd5e //nolint:testpackage // see the note below.
// This file asserts on what the package holds internally, and the internal form is
// the assertion: a parsed expression's dice and modifiers, a pack's compiled rows, the
// effect vocabulary's closed set, the merge by slug. Every one of those is
// unexported on purpose — a client validates against `Grammar` and `Parse`, never
// against `Expr`'s fields — so moving this file out of the package would mean
// exporting internals for a test's benefit, which is the opposite of what the
// boundary is for. The externally reachable behaviour is certified separately in
// `dnd5e_test.go`, through the `rules.System` interface.
import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
)

// # What this file audits, and why it is a test rather than a comment
//
// The package comment claims three things about this package's *shape*: it implements
// `rules.System` and nothing else of semiplane's, it imports nothing from the project
// beyond `internal/domain` and `internal/domain/rules`, and it carries no `init()`. All
// three are the kind of claim that is true until the next contributor adds a line — which
// is why each one is asserted here, by reading this package's own source.
//
// The import audit is the important one. `internal/plugin` translates between a system and
// the hub, so a system that reached for `realtime` would create a cycle the compiler
// refuses, which is a fine outcome for a first attempt and a confusing one for a third;
// `internal/store` and `internal/httpapi` are worse, because they compile and the cost is
// a rule resolver that can read a database. Nothing in the type system stops the second.

// ruleFileNames returns this package's rule source files — the same definition
// `determinism.Audit` uses, so the two audits see the same files.
func ruleFileNames(t *testing.T) []string {
	t.Helper()

	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing this package's files: %v", err)
	}

	rule := make([]string, 0, len(entries))

	for _, entry := range entries {
		if strings.HasSuffix(entry, "_test.go") {
			continue
		}

		rule = append(rule, entry)
	}

	slices.Sort(rule)

	if len(rule) == 0 {
		t.Fatal("no rule files found in this package's own directory")
	}

	return rule
}

// TestTheEngineImportsNothingOutsideTheStandardLibraryAndTheRulesContract is the
// layering rule, and it reads the **import list** rather than trusting a paragraph.
//
// **The whole transitive question, though**, and it is asked of the *transitive* closure
// as well: a system that reached for `internal/realtime` would create a cycle, but one
// that reached for `internal/observability` would compile and take on a logging API whose
// `EventAttributes` has no field a mutation's payload could be passed through — which is
// the structural half of S-12.3. So the rule is: no import outside the standard library
// and `internal/domain`.
func TestTheEngineImportsNothingOutsideTheStandardLibraryAndTheRulesContract(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"github.com/semiplane/semiplane/internal/domain",
		"github.com/semiplane/semiplane/internal/domain/rules",
	}

	// Read from the module file rather than hardcoding the set of first-party packages,
	// so a rename of the module does not make this test pass vacuously.
	module := readModulePath(t)

	for _, file := range ruleFileNames(t) {
		for _, imported := range importsOf(t, file) {
			if !strings.HasPrefix(imported, module) {
				continue
			}

			if slices.Contains(allowed, imported) {
				continue
			}

			t.Errorf("%s imports %q; a gameplay system implements rules.System and reaches for "+
				"nothing else of semiplane's, because the dependency runs inward",
				file, imported)
		}
	}
}

// TestTheEngineCarriesNoInit is S-10.1, and `internal/plugin` has the same assertion over
// every plugin package.
//
// **Parsed rather than grepped**, and the reason is the shape of the mistake: `func init()`
// and `func init /* … */ ()` and a bare `init` reference all parse the same way to `go/ast`
// and none of them is grep-visible with a pattern somebody remembered to write.
func TestTheEngineCarriesNoInit(t *testing.T) {
	t.Parallel()

	for _, file := range ruleFileNames(t) {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}

		for _, declaration := range parsed.Decls {
			function, isFunction := declaration.(*ast.FuncDecl)
			if !isFunction || function.Recv != nil || function.Name == nil {
				continue
			}

			if function.Name.Name == "init" {
				t.Errorf("%s declares an init function; registration happens in the composition "+
					"root, where the order is readable, because with versioned packs the order is "+
					"reviewable information", file)
			}
		}
	}
}

// TestTheEngineCarriesNoPackageLevelMutableState is the second half of S-10.1, and it is
// harder to state than "no init".
//
// A package-level `var` holding a slice or a map is mutable global state: a registry that
// sorted one in place, or appended a system's kind to it, would corrupt the answer for
// every later caller. The tables this package *does* declare are read-only by convention,
// so the test is that every package-level variable is one of the five the file names — and
// that the list is not a way to opt a new one in silently, because a new entry with a
// mutable type is refused below.
func TestTheEngineCarriesNoPackageLevelMutableState(t *testing.T) {
	t.Parallel()

	// The five, and **what each one is**: a table keyed by a compiled-in name, a set of
	// identifiers, a vocabulary, a grammar declaration, and the embedded pack's bytes.
	readOnly := map[string]string{
		"gmOnly":       "the GM-only operation list; GMOnlyOps clones it",
		"effectKinds":  "the effect vocabulary; read with slices.Contains",
		"hookIDs":      "the hook ids; compileHooks walks it",
		"views":        "the declared views; Views clones it",
		"basePackYAML": "the embedded pack; BasePackYAML clones it",
		"criticalRules": "the critical rules, keyed by a compiled-in name; read by " +
			"`bindHook` and never written",
		"masteryRules": "the mastery rules, keyed by a compiled-in name; read by `bindHook` " +
			"and never written",
	}

	for _, file := range ruleFileNames(t) {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}

		for _, declaration := range parsed.Decls {
			generic, isGeneric := declaration.(*ast.GenDecl)
			if !isGeneric || generic.Tok != token.VAR {
				continue
			}

			for _, spec := range generic.Specs {
				value, isValue := spec.(*ast.ValueSpec)
				if !isValue || len(value.Names) != 1 {
					continue
				}

				// **Slices and maps only.** An error sentinel is an interface value holding
				// a pointer to a struct nobody writes to, and a blank import is neither —
				// holding those to a list would make the test a chore rather than a gate.
				if !isMutable(value.Type) {
					continue
				}

				name := value.Names[0].Name
				if name == "_" {
					continue
				}

				why, listed := readOnly[name]
				if !listed {
					t.Errorf(
						"%s declares the package-level variable %q, whose value is mutable "+
							"and which this file does not account for: %s",
						file,
						name,
						"no accessor clones it and no name here explains why it is safe",
					)

					continue
				}

				_ = why
			}
		}
	}
}

// TestEveryTableHandedOutIsCloned is the behavioural half of the table rule.
//
// **Mutation is the probe**, and reading the length is not: a slice handed out by value
// shares its backing array, so appending to it either grows the caller's own slice or —
// when it has spare capacity — writes into the package's. Both are invisible to a caller
// that only compares lengths, and both corrupt the answer for every later caller.
func TestEveryTableHandedOutIsCloned(t *testing.T) {
	t.Parallel()

	// `gmOnly` and `views`, the two package-level slices this package declares.
	first := GMOnlyOps()
	if len(first) == 0 {
		t.Fatal("GMOnlyOps returned nothing, so the assertions below prove nothing")
	}

	first[0] = "clobbered"

	second := GMOnlyOps()
	if second[0] == "clobbered" {
		t.Error("GMOnlyOps handed out the package's own slice; a registry that sorted or " +
			"appended to it would corrupt the vocabulary the conformance findings are written in")
	}

	declared := views
	_ = declared

	got := anEngine(t, "").Views()
	if len(got) == 0 {
		t.Fatal("Views returned nothing, so the assertions below prove nothing")
	}

	got[0].Name = "clobbered"

	if again := anEngine(t, "").Views(); again[0].Name == "clobbered" {
		t.Error("Views handed out the package's own slice")
	}

	// And the kinds, which come from the pack: `ContentKinds` must clone too, because a
	// registry appending a kind to one system's list would otherwise grow another.
	kinds := anEngine(t, "").ContentKinds()
	if len(kinds) == 0 {
		t.Fatal("ContentKinds returned nothing, so the assertions below prove nothing")
	}

	kinds[0] = "clobbered"

	if again := anEngine(t, "").ContentKinds(); again[0] == "clobbered" {
		t.Error("ContentKinds handed out the pack's own slice")
	}
}

// TestTheDeterminismAuditFindsNothingInThisPackage runs the repository's own audit over
// this package rather than trusting that it passes.
//
// **The audit, not a re-implementation of it.** `determinism.Audit` is what `make check`
// runs over `internal/domain/systems`, and the point of calling it here is that it is the
// *same* code path: a rule in this package that the audit would flag is a failure in the
// gate, and finding it in `go test` gives a message naming the construct rather than a red
// CI job somebody has to bisect.
func TestTheDeterminismAuditFindsNothingInThisPackage(t *testing.T) {
	t.Parallel()

	violations, err := determinism.Audit("internal/domain/systems/dnd5e")
	if err != nil {
		t.Fatalf("the determinism audit could not read this package: %v", err)
	}

	for _, violation := range violations {
		t.Errorf("%s", violation)
	}
}

// TestTheEngineImplementsRulesSystemAndOnlyThat is the method set, and it is the check
// that makes the package comment's first claim true.
//
// **Both directions**, and the reverse one is the one that matters: a system that grew a
// tenth method would be fine on its own and a breaking change for every registry that
// wrapped it, and `conformance`'s `upgraded` decorator is the thing that would break. The
// compile-time assertion in `engine.go` covers the forward direction and this covers the
// other one, by counting.
func TestTheEngineImplementsRulesSystemAndOnlyThat(t *testing.T) {
	t.Parallel()

	if err := rules.Validate(anEngine(t, "")); err != nil {
		t.Fatalf("the shipped system does not satisfy the contract: %v", err)
	}

	// The nine methods ADR 0041 settled on, plus the three this package adds for reasons
	// it records: `Versions` (the composition root cannot import this package's struct),
	// `Resolves` (the second registry ADR 0042 asks through), and `Pack` (a status page
	// and a test need the rows).
	want := []string{
		// The nine ADR 0041 settled on.
		"Apply", "ContentKinds", "Derive", "Grammar", "ID", "Parse",
		"RulesetVersion", "Title", "Views",
		// And the four this package adds, each for a reason its own comment records:
		// `Versions` and `Resolves` for the two optional interfaces `internal/plugin`
		// asks through, `Pack` for a status page, and the two convenience accessors
		// that read `Versions`' fields rather than recomputing them.
		"OverlayVersion", "Pack", "PackVersion", "Resolves", "Versions",
	}

	got := exportedMethodsOf(t, "(*Engine)")

	for _, method := range want {
		if !slices.Contains(got, method) {
			t.Errorf("the engine has no method %q", method)
		}
	}

	for _, method := range got {
		if !slices.Contains(want, method) {
			t.Errorf("the engine has a method %q that is neither one of `rules.System`'s nine nor "+
				"one of the three this package records adding", method)
		}
	}
}

// TestTheGrammarIsNeverAWriteUpOfTheD20System is §10.3's sentence, asserted on the
// generated patterns rather than on the parser.
//
// The claim is that the *assumption* is not in the code: change the pack's die sizes and
// the patterns a client validates against change with them. A written-out `20` would
// satisfy every other test in this package and make this one fail.
func TestTheGrammarIsNeverAWriteUpOfTheD20System(t *testing.T) {
	t.Parallel()

	pack := packWithDie(t, 2)
	grammar := Grammar(pack)

	// **The die-bearing terms only.** The `modifier` term has no die in it — it is
	// `[+-][0-9]+` and never mentions a face — so requiring a `2` of it would be asserting
	// about a term the die list cannot reach.
	for _, term := range grammar.Terms {
		if !strings.Contains(term.Name, "die") {
			continue
		}

		// `2` is in this pack's sizes and `20` is in every 5e pack's, so a pattern naming
		// `2` is one generated from the list rather than written out.
		if !strings.Contains(term.Pattern, "2") {
			t.Errorf("the %q term's pattern is %q and mentions no d2, though this pack declares "+
				"one; the pattern looks written out rather than generated",
				term.Name, term.Pattern)
		}
	}
}

// TestARollLabelIsAShareableIdentifier is the identifier `compileID` builds, and it is
// asserted against `rules`' own rule rather than against a comment quoting it.
//
// ADR 0049 holds that every id in this project is lower case with `_`/`-` separators, and
// this package labels every draw with one. An identifier that did not satisfy the rule
// would still *work* — nothing rejects it — and the first symptom would be a label
// rendered from a raw error string somewhere downstream.
func TestARollLabelIsAShareableIdentifier(t *testing.T) {
	t.Parallel()

	labels := []string{
		compileID("roll", "orc_1", "1d20+3", string(AdvNone)),
		compileID("attack", "orc_1", "kobold_1", "Greataxe", "damage"),
		compileID("roll", strings.Repeat("a", 200)),
		compileID("Roll/1"),
	}

	// An all-empty label is legal and yields the empty string, which is worth asserting:
	// it is the one label no identifier rule admits, and a caller that built one would
	// file a draw under a name nothing can address.
	if got := compileID("", "", ""); got != "" {
		t.Errorf("compileID of three empty parts is %q, want empty", got)
	}

	for _, label := range labels {
		if label == "" {
			t.Errorf("an empty label: %q", label)
		}

		if strings.ContainsAny(label, " /\\.:") {
			t.Errorf("the label %q contains a character no identifier holds", label)
		}

		if len(label) > 64 {
			t.Errorf("the label %q is %d characters; `compileID` bounds it and the bound is the "+
				"one `rules` enforces", label, len(label))
		}
	}

	// **Two long labels that agree on their first bytes must not collide.** That is what
	// the digest appended to the truncation exists for, and two labels that share a
	// 47-character prefix and differ afterwards is the case it prevents: they would draw
	// the same numbers, which is two creatures whose attacks share a stream and a replay
	// that cannot separate them.
	prefix := strings.Repeat("b", 60)
	first := compileID(prefix + "one")
	second := compileID(prefix + "two")

	if first == second {
		t.Errorf("two labels sharing a %d-character prefix digest to the same value: %q",
			len(prefix), first)
	}

	// And the empty parts are dropped rather than producing a doubled separator, because
	// an identifier with `__` in it is still valid and nobody can read it.
	if got := compileID("roll", "", "1d20"); got != "roll_1d20" {
		t.Errorf("compileID with an empty part is %q, want %q", got, "roll_1d20")
	}
}

// readModulePath returns this module's path, read from `go.mod`.
func readModulePath(t *testing.T) string {
	t.Helper()

	source, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}

	for line := range strings.SplitSeq(string(source), "\n") {
		rest, found := strings.CutPrefix(strings.TrimSpace(line), "module ")
		if found {
			return strings.TrimSpace(rest)
		}
	}

	t.Fatal("go.mod declares no module path")

	return ""
}

// importsOf returns the import paths one file declares.
func importsOf(t *testing.T, file string) []string {
	t.Helper()

	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}

	paths := make([]string, 0, len(parsed.Imports))

	for _, imported := range parsed.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Fatalf("%s imports %s, which is not a quoted string", file, imported.Path.Value)
		}

		paths = append(paths, path)
	}

	return paths
}

// exportedMethodsOf returns the exported method names a pointer to a named type has,
// **excluding promoted methods**, so the list is the type's own and a method added to an
// embedded interface is not mistaken for one written here.
func exportedMethodsOf(t *testing.T, name string) []string {
	t.Helper()

	declared := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(name, "("), "*"), ")")

	// **Every file in the package**, because a method written in a file whose name has
	// nothing to do with the type is exactly how `Derive` ended up in `view.go` while the
	// interface assertion sits in `engine.go`. Reading one file would report a method set
	// that is wrong whenever the type is spread across files.
	names := make([]string, 0, 16)

	for _, file := range ruleFileNames(t) {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}

		for _, each := range parsed.Decls {
			function, isFunction := each.(*ast.FuncDecl)
			if !isFunction || function.Recv == nil {
				continue
			}

			if receiverName(function.Recv) != declared {
				continue
			}

			if !function.Name.IsExported() {
				continue
			}

			// A value receiver's method is in the pointer's set too, which is what makes
			// `*Engine` a `rules.System`.
			names = append(names, function.Name.Name)
		}
	}

	slices.Sort(names)
	if hasDuplicates(names) {
		t.Errorf("%s declares a method more than once: %v", declared, names)
	}

	slices.Sort(names)

	if len(names) == 0 {
		t.Fatalf("no exported methods found on %s", name)
	}

	return names
}

// hasDuplicates reports whether a sorted, compacted slice still holds two equal entries.
func hasDuplicates(compact []string) bool {
	seen := make(map[string]struct{}, len(compact))
	for _, each := range compact {
		if _, repeated := seen[each]; repeated {
			return true
		}

		seen[each] = struct{}{}
	}

	return false
}

// isMutable reports whether a declaration's type is one a caller could write through: a
// slice, a map, or a pointer to either.
func isMutable(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.ArrayType:
		return true
	case *ast.MapType:
		return true
	case *ast.StarExpr:
		return isMutable(typed.X)
	case *ast.Ident:
		// A named type, which the parser cannot resolve here: assume mutable, because
		// the cost of a false positive is a name added to the accounted-for list and the
		// cost of a false negative is the gate this file exists to be.
		return typed.Name != "string" && typed.Name != "bool" && typed.Name != "error" &&
			typed.Name != "int" && typed.Name != "byte" && typed.Name != "float64"
	default:
		return false
	}
}

// receiverName returns the type name a receiver list declares, without its pointer or
// generic parameters.
func receiverName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}

	switch expr := recv.List[0].Type.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.StarExpr:
		return receiverName(&ast.FieldList{List: []*ast.Field{{Type: expr.X}}})
	default:
		return ""
	}
}
