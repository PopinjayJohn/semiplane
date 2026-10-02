// The tests that hold the two view-model layers in step.
//
// `components` declares the models a *route* builds and `components/chrome`
// declares the models a *landmark* reads, and three concepts appear in both: the
// signed-in identity, the instance's name, and an unhealthy subsystem. The package
// doc in `viewmodels.go` explains why the reconciliation is a conversion rather
// than a set of aliases; these tests are what stop the two copies from drifting
// once the conversion exists.
//
// This is an **internal** test — `package components`, not `components_test` — and it
// opts out of the `testpackage` linter with the reason inline. `testpackage` is
// right almost everywhere: it forces a test through the package's exported
// surface, which is what catches a type that should not have been exported at
// all. It is wrong here, because three of these claims are about declarations
// that are unexported **on purpose**:
//
//   - the conversion functions are unexported because a route has no business
//     calling them, and
//   - `chrome.CampaignRef.label()` is unexported because both the label and the
//     address are rendering decisions a caller overrides by supplying its own
//     values.
//
// Exporting either to test it would change the design to suit the test, which is
// the one thing a gate must not do. Where a claim needs a declaration's *source*
// rather than its value it parses the file instead — which is also why no test
// seam was added to either package.

//nolint:testpackage // The claims are about deliberately unexported declarations; see above.
package components

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/web/components/chrome"
	"github.com/semiplane/semiplane/internal/web/components/ui"
)

// asState returns its argument typed as the states package's `LoadFailure`.
//
// **The two signatures are the assertion.** A value of this package's type is
// assignable to a variable of that one, and vice versa, only when they are the same
// type — an alias is identity, and a defined type needs a conversion and would not
// compile here.
//
// Written as a function rather than as `var x ui.LoadFailure = y` because the
// explicit type in a variable declaration is redundant *as far as the compiler is
// concerned*, and both the linter and a tidy-minded reader will remove it — at which
// point the test asserts nothing, because `:=` infers `LoadFailure` from the right
// hand side and proves nothing at all. In a signature the type is not redundant: it
// is the declaration the compiler checks against.
func asState(failure LoadFailure) ui.LoadFailure {
	return failure
}

// asRoute is `asState` in the other direction.
func asRoute(failure ui.LoadFailure) LoadFailure {
	return failure
}

// TestLoadFailureIsTheStatePackageType holds the one alias in the package.
//
// `components.LoadFailure` is `ui.LoadFailure`, and the alias is the whole point:
// two structs would be two answers to "what does a failed load look like", and the
// second would not be the one `ui.LoadError` renders — so a caller that got it
// wrong would compile, ship, and show nothing.
//
// Both fields a route sets cross intact. `Heading` is the one that arrived with the
// state, so it is the one a conversion would silently drop.
func TestLoadFailureIsTheStatePackageType(t *testing.T) {
	t.Parallel()

	// Both carry the fields a route sets, so the crossing is not vacuous: a pair
	// of empty structs would cross just as successfully.
	route := LoadFailure{Reference: "req-1", Heading: 1}

	inStates := asState(route)
	if inStates.Reference != "req-1" {
		t.Errorf("the alias lost the reference: %q", inStates.Reference)
	}

	back := asRoute(inStates)
	if back.Heading != 1 {
		t.Errorf("Heading reads as %d through components.LoadFailure after being set "+
			"through ui.LoadFailure; the alias is not identity, so the two names are "+
			"two values and a conversion is needed", back.Heading)
	}

	if back.Reference != "req-1" {
		t.Errorf("the reference reads as %q after crossing twice", back.Reference)
	}
}

// TestTheViewModelLayersAgreeWhereTheyOverlap is the anti-drift test for the
// conversion.
//
// The conversion is total and mechanical, so the failure it cannot catch is a
// field added to one side and forgotten on the other. A forgotten field does not
// compile and does not crash: it renders its zero value. A campaign's name
// silently becoming empty is exactly that bug, and it is invisible in review
// because the conversion still compiles and still looks complete.
//
// Both directions are checked: every field the routes build exists on the chrome
// side (so there is something to copy), and the conversion assigns every one of
// them (so it actually does). The second half is the one that catches the field
// which exists on both sides and is still never read.
func TestTheViewModelLayersAgreeWhereTheyOverlap(t *testing.T) {
	t.Parallel()

	pairs := []struct {
		concept    string
		route      string
		chrome     string
		conversion string
	}{
		{"the signed-in identity", "AccountView", "AccountView", "chromeAccount"},
		{"an unhealthy subsystem", "DegradedView", "DegradedView", "chromeDegraded"},
	}

	for _, pair := range pairs {
		t.Run(pair.concept, func(t *testing.T) {
			t.Parallel()

			routeFields := structFields(t, "viewmodels.go", pair.route)
			if len(routeFields) == 0 {
				t.Fatalf("%s is not declared in viewmodels.go", pair.route)
			}

			chromeFields := structFields(t, filepath.Join("chrome", "chrome.go"), pair.chrome)
			if len(chromeFields) == 0 {
				t.Fatalf("%s is not declared in chrome/chrome.go", pair.chrome)
			}

			for _, field := range sortedKeys(routeFields) {
				if !chromeFields[field] {
					t.Errorf("%s.%s has no counterpart on chrome.%s; the conversion "+
						"cannot copy a field that does not exist, so it renders its "+
						"zero value — an empty name, not a compile error",
						pair.route, field, pair.chrome)
				}
			}

			// And the chrome side must not have grown a field the conversion cannot
			// reach, because that field would also render its zero value — from the
			// other direction, and just as silently.
			//
			// `SignOutHref` is the one permitted asymmetry, and it is documented at
			// the exception below and held by its own test. Everything else must
			// match, because "only one side declares it" is exactly how a field ends
			// up permanently empty.
			for _, field := range sortedKeys(chromeFields) {
				if field == "SignOutHref" {
					continue
				}

				if !routeFields[field] {
					t.Errorf("chrome.%s.%s has no counterpart on %s; a field only one "+
						"side declares cannot be populated by a caller",
						pair.chrome, field, pair.route)
				}
			}

			body := functionBody(t, "viewmodels.go", "", pair.conversion)

			for _, field := range sortedKeys(routeFields) {
				if field == "SignOutHref" {
					// The one field that legitimately moves. `components.AccountView`
					// holds it on `ShellView` — it was added there before the chrome
					// existed, and four routes set it there — while the chrome model
					// holds it on the account, because the account zone is what posts
					// to it. It is therefore a *parameter* of the conversion rather
					// than a field, so it cannot be checked as one, and it is
					// asserted on its own in
					// TestTheSignOutTargetCrossesTheLayerBoundary.
					continue
				}

				if !strings.Contains(body, field+":") {
					t.Errorf("the conversion to chrome.%s does not assign %s; a field "+
						"the conversion forgets renders its zero value",
						pair.chrome, field)
				}
			}
		})
	}
}

// TestTheSignOutTargetCrossesTheLayerBoundary holds the one field that moves
// between layers rather than being copied.
//
// It is a parameter of `chromeAccount` and not a field of either `AccountView`,
// because the two disagree about where it lives — and a test that only compared
// field sets would see two correct structs and no sign that the sign-out target
// could be silently dropped. It is the single control on the account zone that
// works without a client, so dropping it removes the reader's only way out.
func TestTheSignOutTargetCrossesTheLayerBoundary(t *testing.T) {
	t.Parallel()

	// It reaches the chrome side.
	converted := chromeAccount(AccountView{Username: "ada"}, "/logout")
	if converted.SignOutHref != "/logout" {
		t.Errorf("the sign-out target is %q after conversion, want %q",
			converted.SignOutHref, "/logout")
	}

	// And it reaches it through the header, which is the composition that
	// actually renders it.
	header := chromeHeader(ShellView{
		Account:     AccountView{Username: "ada"},
		SignOutHref: "/logout",
	})

	if header.Account.SignOutHref != "/logout" {
		t.Errorf("the banner's account zone carries the sign-out target %q, want %q; "+
			"§4.1's account zone is the one control that works without a client, and "+
			"a form that posts nowhere is a focus stop that does nothing",
			header.Account.SignOutHref, "/logout")
	}

	// And an empty target renders no form at all, rather than a form posting to
	// "" — which would post to the current URL and log the reader out of whatever
	// they were doing.
	anonymous := chromeHeader(ShellView{})
	if anonymous.Account.SignOutHref != "" {
		t.Errorf("an unsigned-in route produced the sign-out target %q",
			anonymous.Account.SignOutHref)
	}
}

// TestTheProductNameIsTheSameInBothPackages holds the duplicated fallback.
//
// Both packages declare `productName`, unexported, with the same reasoning. The
// chrome package cannot import this one — the dependency runs the other way, and a
// package cannot import its caller — so the copy stays.
//
// The comparison reads the *other* package's source rather than against a third
// constant: a constant here would be a third copy, and it would agree with both
// until one changed, at which point it would be the one somebody had to find.
func TestTheProductNameIsTheSameInBothPackages(t *testing.T) {
	t.Parallel()

	mine := constantValue(t, "viewmodels.go", "productName")
	if mine == "" {
		t.Fatal("this package declares no productName")
	}

	theirs := constantValue(t, filepath.Join("chrome", "chrome.go"), "productName")
	if theirs == "" {
		t.Fatal("the chrome package declares no productName")
	}

	if mine != theirs {
		t.Errorf("the product name is %q in components and %q in chrome; a banner "+
			"saying one thing and a document title saying another is a rename that "+
			"reached only one package", mine, theirs)
	}
}

// TestCampaignFallsBackToItsSlugInBothPlaces holds the one duplicated *rule*.
//
// `chrome.CampaignRef.label()` is unexported, so `documentTitleForCampaign`
// writes the same fallback out longhand. Two hand-written copies of one rule is
// exactly the situation a test is for, and the rule has real content: a campaign
// registered without a name still has a slug, and a slug is worse than nothing
// only if it is hidden — a document title reading " — — " tells a reader nothing.
//
// Read as source text on both sides rather than through the API, because the
// chrome method cannot be called from here without exporting it, and exporting a
// rendering decision to serve a test is the wrong trade.
func TestCampaignFallsBackToItsSlugInBothPlaces(t *testing.T) {
	t.Parallel()

	// This side, through the function itself.
	instance := InstanceView{Name: "Greyhaven"}

	if got := documentTitleForCampaign("Page", instance, chrome.CampaignRef{
		Name: "Greyhaven", Slug: "greyhaven",
	}); got != "Page — Greyhaven — Greyhaven" {
		t.Errorf(
			"a named campaign's document title is %q, want %q",
			got,
			"Page — Greyhaven — Greyhaven",
		)
	}

	if got := documentTitleForCampaign("Page", instance, chrome.CampaignRef{
		Slug: "greyhaven",
	}); got != "Page — greyhaven — Greyhaven" {
		t.Errorf("an unnamed campaign's document title is %q, want its slug in the "+
			"campaign position", got)
	}

	// The degenerate case: a reference with nothing in it still gets §7.2's *form*,
	// with the campaign part omitted rather than rendered as a stray separator pair.
	if got := documentTitleForCampaign(
		"Page",
		instance,
		chrome.CampaignRef{},
	); got != "Page — Greyhaven" {
		t.Errorf("an empty campaign reference gives the document title %q; the "+
			"campaign part must be omitted rather than rendered as a separator pair", got)
	}

	// And the chrome side, from its source, so the two are compared rather than
	// one asserted and the other assumed.
	chromeLabel := functionBody(t, filepath.Join("chrome", "chrome.go"), "CampaignRef", "label")

	for _, fragment := range []string{"campaign.Name", "campaign.Slug"} {
		if !strings.Contains(chromeLabel, fragment) {
			t.Errorf("chrome.CampaignRef.label does not mention %s; this package's "+
				"document title falls back from Name to Slug and the two rules have "+
				"to be the same rule", fragment)
		}
	}

	mine := functionBody(t, "viewmodels.go", "", "documentTitleForCampaign")
	for _, fragment := range []string{"campaign.Name", "campaign.Slug"} {
		if !strings.Contains(mine, fragment) {
			t.Errorf("documentTitleForCampaign does not mention %s", fragment)
		}
	}
}

// TestTheServerIsNotEntitledToAssertAThemeOrAConnection is a policy test, and it
// is one because the two values are `int`s with meaningful constants.
//
// §3.7 resolves `data-theme` and `data-ui` client-side before the first paint,
// and §6.6's conclusion is that the document does not vary by them. So the header
// the server renders is always `ThemeAuto` and, on a non-live route, carries no
// live region at all.
//
// The temptation is concrete: a handler knows a reader's preference and a route
// knows whether it is live, and threading either through is a one-line change
// that looks like a feature. It is not — it makes the document vary by a cookie,
// which is what §6.6 rules out and what `Vary: Cookie` would then be needed for.
func TestTheServerIsNotEntitledToAssertAThemeOrAConnection(t *testing.T) {
	t.Parallel()

	header := chromeHeader(ShellView{Instance: InstanceView{Name: "Greyhaven"}})

	if header.Theme != chrome.ThemeAuto {
		t.Errorf("the server-rendered banner asserts theme %v; §3.7 resolves "+
			"data-theme client-side before the first paint and §6.6 requires the "+
			"document not to vary by it, so only ThemeAuto is entitled",
			header.Theme)
	}

	if header.Connection != chrome.ConnectionNone {
		t.Errorf("the server-rendered banner asserts connection state %v; §7.5 gives "+
			"a route's liveness to the client, and ConnectionNone is what renders no "+
			"live region at all", header.Connection)
	}
}

// TestThePreCampaignShellDoesNotAlsoRenderTheDegradedNotice is S3's integration
// rule 10, as a test.
//
// The footer's degraded block and the rail's `DegradedNotice` both carry an
// `<h2>Not working</h2>`. A pre-campaign route has the rail, so passing the
// subsystems to the footer as well would meet the reader with the same heading
// twice in one document — and §7.2's "exactly one h1, heading levels never skip"
// is about validity, not about a heading being *repeated*, so nothing else in the
// phase would see it.
//
// A campaign route is the other case: the rail there is the campaign's own panels
// and renders none of these, so the footer is the only place the warning can go.
func TestThePreCampaignShellDoesNotAlsoRenderTheDegradedNotice(t *testing.T) {
	t.Parallel()

	instance := InstanceView{
		Name:     "Greyhaven",
		Degraded: []DegradedView{{Name: "content watcher", Detail: "stopped"}},
	}

	// The pre-campaign shell passes nil. The rail's `InstanceRail` renders
	// `DegradedNotice` for the same list, so the footer must not.
	preCampaign := chromeFooter(ShellView{Instance: instance}, nil)
	if len(preCampaign.Degraded) != 0 {
		t.Errorf("the pre-campaign footer carries %d degraded subsystems; the rail "+
			"already renders the notice for the same list and both carry an "+
			"`<h2>Not working</h2>`, which is a repeated heading in one document",
			len(preCampaign.Degraded))
	}

	// The campaign shell passes them, because there the rail is the campaign's own
	// panels and renders none of these.
	inCampaign := chromeFooter(ShellView{Instance: instance}, instance.Degraded)
	if len(inCampaign.Degraded) != 1 {
		t.Fatalf("the campaign footer carries %d degraded subsystems, want 1; on a "+
			"campaign route the rail describes the campaign and the footer is the "+
			"only place §4.2's persistent warning can go",
			len(inCampaign.Degraded))
	}

	if inCampaign.Degraded[0].Name != "content watcher" ||
		inCampaign.Degraded[0].Detail != "stopped" {
		t.Errorf("the converted subsystem is %+v; both fields must survive the "+
			"conversion or the operator loses the clause saying what it means",
			inCampaign.Degraded[0])
	}

	// And an empty list converts to nil rather than an empty allocation, so
	// "nothing is wrong" is the zero value and not a manufactured slice.
	if converted := chromeDegraded(nil); converted != nil {
		t.Errorf("chromeDegraded(nil) returned %v, want nil", converted)
	}
}

// --- Source-reading helpers -------------------------------------------------
//
// These parse Go rather than reflect on it, because some of the declarations
// under comparison are unexported and one is a function body rather than a type —
// reflection reaches neither.
//
// The alternative, an exported test seam per declaration, would add API surface
// to a package whose design rests on those declarations staying private. Parsing
// the source is the cheap option that adds nothing.

func parseFile(t *testing.T, name string) *ast.File {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}

	return file
}

// structFields returns the exported field names of a struct type.
//
// Collected in a closure rather than through a package-level destination, because
// `ast.Inspect` cannot return a value and a shared variable between two
// `t.Parallel()` subtests is a data race waiting for a `-race` run to find it.
func structFields(t *testing.T, filename, typeName string) map[string]bool {
	t.Helper()

	fields := map[string]bool{}

	ast.Inspect(parseFile(t, filename), func(node ast.Node) bool {
		decl, ok := node.(*ast.TypeSpec)
		if !ok || decl.Name.Name != typeName {
			return true
		}

		structType, ok := decl.Type.(*ast.StructType)
		if !ok {
			return false
		}

		for _, field := range structType.Fields.List {
			for _, name := range field.Names {
				if name.IsExported() {
					fields[name.Name] = true
				}
			}
		}

		return false
	})

	return fields
}

// constantValue returns an untyped string constant's value from a file.
func constantValue(t *testing.T, filename, name string) string {
	t.Helper()

	var found string

	ast.Inspect(parseFile(t, filename), func(node ast.Node) bool {
		decl, ok := node.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			return true
		}

		for _, spec := range decl.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != name ||
				len(value.Values) != 1 {
				continue
			}

			literal, ok := value.Values[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}

			// strconv.Unquote rather than trimming quotes by hand: the value is a
			// Go string literal and may hold escapes, and a hand-rolled trim would
			// silently mis-read one.
			unquoted, err := strconv.Unquote(literal.Value)
			if err != nil {
				continue
			}

			found = unquoted
		}

		return true
	})

	return found
}

// functionBody returns a function's source text, braces included.
//
// Matches a method as well as a plain function: `func label(` does not appear in
// `func (campaign CampaignRef) label(`, and the receiver is exactly what makes the
// declarations under comparison unexported in the first place.
// The chrome package declares three methods called `label` -- one per receiver --
// so a name-only match finds whichever the parser visited last, which is
// `Theme.label`. The receiver type is therefore part of a method's identity, and
// passing an empty receiver matches only a plain function.
//
// The receiver is compared against the *type expression*, which is what the
// declaration writes: `(campaign CampaignRef)`.
func functionBody(t *testing.T, filename, receiver, name string) string {
	t.Helper()

	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}

	source := string(raw)

	files := token.NewFileSet()

	file, err := parser.ParseFile(files, filename, source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}

	at := -1

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != name {
			continue
		}

		if !hasReceiver(fn, receiver) {
			continue
		}

		// The body, not the declaration: a signature is one line, and the claims
		// this test makes are about what the function does.
		at = files.Position(fn.Body.Lbrace).Offset
	}

	if at < 0 {
		t.Fatalf("%s declares no %s; this test holds a declaration that has gone, "+
			"and a gate that watches nothing is worse than no gate", filename, name)
	}

	depth := 0

	for offset, char := range source[at:] {
		switch char {
		case '{':
			depth++
		case '}':
			depth--

			if depth == 0 {
				return source[at : at+offset+1]
			}
		}
	}

	t.Fatalf("%s has an unterminated declaration block", name)

	return ""
}

// hasReceiver reports whether a function's receiver is the named type.
func hasReceiver(fn *ast.FuncDecl, receiver string) bool {
	if receiver == "" {
		return fn.Recv == nil
	}

	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return false
	}

	identifier, ok := fn.Recv.List[0].Type.(*ast.Ident)
	if !ok {
		return false
	}

	return identifier.Name == receiver
}

// sortedKeys gives a deterministic iteration order, so a failure message lists
// the same fields on every run.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}

	sortStrings(keys)

	return keys
}

// sortStrings is a tiny insertion sort, so this file does not import `sort` for
// four strings.
func sortStrings(values []string) {
	for outer := 1; outer < len(values); outer++ {
		for inner := outer; inner > 0 && values[inner] < values[inner-1]; inner-- {
			values[inner], values[inner-1] = values[inner-1], values[inner]
		}
	}
}
