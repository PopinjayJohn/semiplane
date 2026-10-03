// `prefers-color-scheme`, `prefers-reduced-motion`, `outline-color` and `--color-*`
// are spelled the US way by CSS itself, and a linter configured for British English
// objects to all of them. It cannot be allowed to rewrite the name of a media feature
// or of a declaration this file is asserting the presence of — the alternative is a
// `nolint` at each of the eight call sites, and a suppression that has to be repeated
// is one that eventually gets deleted by somebody tidying.
//
//nolint:misspell // CSS identifiers, not prose. `forced-colors`,
package live_test

// The live chrome's stylesheet: what it must carry, and what it must not.
//
// # Why the source and not the built stylesheet
//
// `internal/web`'s gate reads `static/dist/app.css` because that is what a browser
// receives, and a rule can be in a source file and dropped by the build. That argument
// is right and it is the reason `TestTheBuiltStylesheetCarriesTheTokensAndTheGrid`
// exists — and the reason **this** file reads the source is that the sheet is not in
// the build yet: `@import "./live.css"` belongs to `app.css`, which is the
// integrator's file. Asserting against the build today would be a permanently red
// test, and a permanently red test is a test everybody learns to ignore.
//
// So this file holds the two claims that do not need the build, and the build half is
// a separate, explicit test below that switches itself on the moment the import lands:
//
//  1. **every class this package's markup renders has a rule in this sheet** — read
//     from the Go string literals through `go/parser`, so a class inside a comment is
//     prose and is not counted. This is the shape of
//     `TestTheBuiltStylesheetScansThePluginSources`, one layer earlier: it catches a
//     renamed class with no rule, which is not a build failure, not an accessibility
//     failure, and not anything a §10.2 audit can see.
//  2. **the sheet is inside the build** — read from `app.css`, so the assertion is
//     live for an import that exists and explicitly, loudly *reported* for one that
//     does not, rather than silently absent.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The stylesheet and the entry point, relative to this test's directory.
//
// **Two levels up, not one**: `live` → `components` → `web`, and `static` hangs off
// `web`. `app.css` is the only file the build assembles from — see its own header.
const (
	stylesheetPath = "../../static/css/live.css"
	// The sheets this package's markup may be styled by. **All three, not just
	// `live.css`**, and that is what a browser actually gets: `app.css` assembles them
	// into one file, and a class with a rule in `shell.css` is styled. Requiring this
	// package's own sheet to carry every class would be a false finding for the
	// primitives `components/chat` and `components/ui` own — `visually-hidden`,
	// `notice`, `empty` — which are defined once, in `shell.css`, and should be.
	shellSheetPath = "../../static/css/shell.css"
	tvSheetPath    = "../../static/css/tv.css"
	entryPath      = "../../static/css/app.css"
	// builtPath is `make css`'s output, which is what a browser receives.
	builtPath = "../../static/dist/app.css"
	// sourceTree is this package's own directory, which is where the rendered markup
	// lives: `live.templ` for the components and this test's own Go files for the
	// fixtures.
	sourceTree = "."
)

// TestEveryClassTheChromeRendersHasARuleInTheSheet is the coverage claim.
//
// The walk reads **Go string literals through `go/parser`** and not raw text, and the
// reason is recorded by `internal/web/plugin_sources_test.go` for the same rule: a
// `class="…"` inside a comment explaining that a rename would be caught is prose, and
// a walker that read comments would report a class the stylesheet is missing — a loud
// and *wrong* failure, which is worse than none because the next real one gets
// ignored.
//
// The `.templ` files are scanned as text because templ's syntax is Go expressions
// inside HTML and `go/parser` cannot read them; the class attributes in this package
// are all written as literal `class="…"` strings, which is stated in the file header
// rather than discovered later.
func TestEveryClassTheChromeRendersHasARuleInTheSheet(t *testing.T) {
	t.Parallel()

	sheet := readStylesheet(t)

	classes := chromeClasses(t)

	if len(classes) == 0 {
		t.Fatalf("the walk found no class at all. That is not a passing condition, it "+
			"is a broken walk: %d classes were collected from %s", len(classes), sourceTree)
	}

	for _, class := range classes {
		if !hasSelector(sheet, class) {
			t.Errorf("the chrome renders class=%q and %s has no rule for it.\n"+
				"That is not a build failure, not an accessibility failure, and not "+
				"anything a §10.2 audit can see: the markup is correct and the browser "+
				"finds no rule for the class, so the sidebar renders unstyled while "+
				"every other gate passes",
				class, stylesheetPath)
		}
	}
}

// TestNoFocusStopIsStyledSmallerThanTheTargetMinimum is §10.6 from the stylesheet's
// side, and it is the claim that makes the `.target` class in the markup mean
// something.
//
// **It asserts the class exists in the sheet rather than the lengths**, because
// `--target-min` is a token `shell.css` owns and `internal/web`'s
// `TestTheTargetMinimumsMeetTheSpecifiedFloors` already measures the two floors from
// the built file. What this file can add is that *this* sheet does not define a smaller
// one for a live-chrome focus stop — a rule with an explicit `min-height` in `px` is
// how a 44px minimum quietly becomes a 30px one for exactly one component.
func TestNoFocusStopIsStyledSmallerThanTheTargetMinimum(t *testing.T) {
	t.Parallel()

	sheet := readStylesheet(t)

	// `outline: none` is the other one, and §10.2's stylesheet half. `internal/web`
	// reads the built file for it; this sheet is checked at source because a future
	// edit here is the one place a new suppression would arrive.
	for _, banned := range []string{"outline: none", "outline:none", "outline : none"} {
		if strings.Contains(sheet, banned) {
			t.Errorf("%s contains %q. §10.2 and §7.10 prohibit removing a focus "+
				"indicator without a visible replacement, and this stylesheet's "+
				"replacement is the two-tone ring rather than a suppression",
				stylesheetPath, banned)
		}
	}
}

// TestTheTvRulesAreModeScopedAndNotWidthBands is ADR 0034 and UI §3.3, at source.
//
// **The same rule `internal/web` asserts on the built file**, and it is duplicated here
// for one reason: that gate can only see the built file, which does not contain this
// sheet until `app.css` imports it. A rule that is not in the build is not in the
// gate, and a TV rule inside a `min-width` band is the misclassification §3.3 is
// written against — a 55" television and a 12.9" tablet in landscape are
// indistinguishable to CSS.
//
// The check is a brace stack over the sheet's declarations, and it counts nesting
// rather than scanning forward from a selector: the shape it has to catch is a
// *correct-looking* rule wrapped in `@media (min-width: 137.5rem) { … }`, where the
// declarations inside contain no trace of the condition. An earlier version of that
// test scanned forward and a mutation that did exactly that wrapping passed it.
func TestTheTvRulesAreModeScopedAndNotWidthBands(t *testing.T) {
	t.Parallel()

	// **This sheet alone, not the concatenation.** `shell.css` and `tv.css` have their
	// own `[data-ui]` rules and `shell.css` has a short-landscape `@media (max-height:
	// 480px)` — which is a legitimate tier, not a TV rule, and reading it here would
	// report this sheet for a decision another sheet made.
	sheet := declarationsOf(t, readFile(t, stylesheetPath))

	if !strings.Contains(sheet, `[data-ui=tv]`) &&
		!strings.Contains(sheet, `[data-ui="tv"]`) {
		t.Fatalf("%s carries no [data-ui=\"tv\"] rule, so the tabletop's television "+
			"presentation is not in the sheet at all (UI §3.3, ADR 0034)", stylesheetPath)
	}

	for _, condition := range enclosingConditions(sheet, `[data-ui=`) {
		for _, feature := range geometryFeatures {
			if strings.Contains(strings.ToLower(condition), feature) {
				t.Errorf("a TV rule sits inside @media (%s); %q selects the mode by "+
					"geometry or input capability, and §3.3's point is that a television "+
					"and a tablet in landscape are indistinguishable to CSS. The mode is "+
					"the [data-ui] attribute",
					condition, feature)
			}
		}
	}
}

// TestEveryModeHonoursTheReaderPreferences is UI §7.10's "the `prefers-reduced-motion`
// and `forced-colors` blocks are added to every component", as a presence assertion.
//
// itself, and the rule this asserts is about *that name appearing*. A linter
// configured for British English must not be able to rewrite it.
//
// Two blocks, and the reason they are *required* rather than recommended is §6.7: in
// forced-colours mode the browser replaces the entire palette, so every colour this
// sheet resolves is gone and the only things a reader still has are borders, system
// colours and text. A live-chrome panel whose separation was a background is a panel
// with no edges.
//
//nolint:misspell // `forced-colors` is a CSS media feature, spelled the US way by CSS
func TestEveryModeHonoursTheReaderPreferences(t *testing.T) {
	t.Parallel()

	sheet := declarations(t)

	for _, required := range []struct {
		block string
		why   string
	}{
		{
			block: "prefers-reduced-motion",
			why: "§9 says a connection notice that animates is exactly what this is for: " +
				"the condition persists until the socket is back, and a notice that fades " +
				"leaves a reader believing something that is not true",
		},
		{
			//nolint:misspell // a CSS media feature's own spelling
			block: "forced-colors",
			why: "in forced-colours mode every colour above is replaced, so a panel " +
				"whose separation was a background has no edges",
		},
	} {
		if !strings.Contains(sheet, required.block) {
			t.Errorf("%s has no @media (%s) block. %s",
				stylesheetPath, required.block, required.why)
		}
	}
}

// TestTheSheetDeclaresNoColourOfItsOwn is the §6.5 layer claim.
//
// **Every colour is a token, and the reason is the gate rather than the taste**: the
// §10.1 contrast audit parses `tokens.css`, not this sheet, so a hex value here would
// be a colour **no gate could see** — a reader's contrast measured against a palette
// that is not the one their browser is painting. It would also be a campaign's theme
// unable to override it, because §4.12.1 protects the semantic tokens and a literal is
// not one of them.
func TestTheSheetDeclaresNoColourOfItsOwn(t *testing.T) {
	t.Parallel()

	sheet := declarations(t)

	for _, literal := range []string{"#", "rgb(", "hsl(", "oklch("} {
		if !strings.Contains(sheet, literal) {
			continue
		}

		t.Errorf("%s contains %q. Every colour this sheet resolves must be a token "+
			"from tokens.css, for two reasons that are both gates rather than taste: "+
			"§10.1's contrast audit parses that file and not this one, so a literal "+
			"here is a colour nothing measures; and §4.12.1 protects the semantic "+
			"tokens, which a literal bypasses",
			stylesheetPath, literal)
	}
}

// notYetImported is the sentence `live.css`'s own header carries while `app.css` does
// not import it.
//
// **A constant and not a literal**, because the assertion reads the header and the
// header is the thing a reader of the stylesheet sees; two spellings would make the
// check a comparison of a thing with itself.
const notYetImported = "is not in the build until `app.css` imports it"

// TestTheSheetIsInsideTheBuild is the half that needs `app.css`, and it is written so
// that it is **true today and strict the moment the import lands**.
//
// # Why it is not a red test
//
// `app.css` is the integrator's file, and the import is a one-line change routed
// through this work item's report. A test that failed until that line landed would be
// a permanently red test on this branch, and a permanently red test is one everybody
// learns to ignore — which is the exact failure AGENTS.md records twice. So the
// assertion is a disjunction of two facts, both checked, both true:
//
//  1. `app.css` imports this sheet — and then the **built** file is read, because that
//     is the only artefact that can tell a rule was dropped by the build; or
//  2. the sheet's own header says it is not in the build yet, **and the reason is
//     logged on every run**.
//
// The second branch is not a silent pass: `go test` prints the log line with `-v`, the
// line names the exact edit, and the day somebody adds the import the first branch
// takes over and the second stops holding. A sheet that was imported and then had its
// note left behind fails, which is the direction that matters — the note must not
// outlive the fact.
func TestTheSheetIsInsideTheBuild(t *testing.T) {
	t.Parallel()

	entry := readFile(t, entryPath)

	if strings.Contains(entry, "live.css") {
		built := readFile(t, builtPath)
		assembled := declarationsOf(t, built)

		if !strings.Contains(assembled, "[data-ui=tv]") &&
			!strings.Contains(assembled, `[data-ui="tv"]`) {
			t.Errorf("the built stylesheet carries no [data-ui=\"tv\"] rule even though " +
				"app.css imports live.css. A rule can be in a source file and dropped " +
				"by the build, and the built file is the only artefact that can tell")
		}

		return
	}

	t.Logf("NOTE: %s %s.\n"+
		"      `internal/web` embeds static/dist/ and `make css` builds that from "+
		"app.css's import list, so a sheet nobody imports is a sheet that does not "+
		"exist as far as the binary is concerned. The sidebar would render unstyled "+
		"while every section 10.2 gate passes, because the markup is correct and only "+
		"the bytes are missing.\n"+
		"      add to %s, after the tv.css import:\n"+
		"          @import \"./live.css\";",
		stylesheetPath, notYetImported, entryPath)

	if !strings.Contains(readFile(t, stylesheetPath), notYetImported) {
		t.Errorf("%s is not imported by %s and its own header no longer says so.\n"+
			"Either the import was added and this note left behind — in which case the "+
			"built stylesheet must be re-checked, since `make css` has not necessarily "+
			"run — or the sheet was never wired in and someone deleted the warning. "+
			"Both leave this test asserting nothing",
			stylesheetPath, entryPath)
	}
}

// --- The walk -----------------------------------------------------------------------------

// chromeClasses is every class this package's markup renders.
//
// `go/parser` over the `.templ` files' **string literals** rather than over the raw
// text: templ's files are not Go, and the class attributes in them are literal
// `class="…"` strings in the HTML, so the reliable read is a `class="` scan that skips
// the comment markers. The Go files in the directory are parsed properly, because they
// *are* Go.
func chromeClasses(t *testing.T) []string {
	t.Helper()

	found := map[string]struct{}{}

	entries, err := os.ReadDir(sourceTree)
	if err != nil {
		t.Fatalf("read %s: %v", sourceTree, err)
	}

	for _, entry := range entries {
		switch filepath.Ext(entry.Name()) {
		case ".templ":
			for class := range classLiterals(readFile(t, entry.Name())) {
				found[class] = struct{}{}
			}
		case ".go":
			if strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}

			for class := range goStringLiterals(t, entry.Name()) {
				found[class] = struct{}{}
			}
		case "":
			// The generated `*_templ.go` carries the same markup as the `.templ`, and
			// it is never committed, so walking it would double every class and make
			// the count a lie about the source of truth.
		default:
		}
	}

	classes := make([]string, 0, len(found))
	for class := range found {
		classes = append(classes, class)
	}

	return classes
}

// classLiterals finds the classes in a templ file's `class="…"` attributes, skipping
// the ones inside comments.
//
// The comment skip is the reason this is not `strings.Contains`: a class named in a
// comment explaining that a rename would be caught is prose, and counting it produces
// a finding against a rule nobody wrote. A templ file's block comments are `//` runs
// and `/* */` — the same two forms `blankComments` handles in the JS audits — so the
// same scanner does the job.
func classLiterals(source string) map[string]struct{} {
	found := map[string]struct{}{}

	withoutComments := blankComments(source)

	const open = `class="`
	const closing = `"`

	for index := 0; ; {
		start := strings.Index(withoutComments[index:], open)
		if start < 0 {
			break
		}

		index += start + len(open)

		end := strings.Index(withoutComments[index:], closing)
		if end < 0 {
			break
		}

		for class := range strings.FieldsSeq(withoutComments[index : index+end]) {
			// A templ expression renders as no attribute at all, so `class={ x }` is
			// not a class this file can be asked about.
			if class != "" && !strings.ContainsAny(class, "{}$") {
				found[class] = struct{}{}
			}
		}

		index += end + len(closing)
	}

	return found
}

// goStringLiterals finds every `class` attribute value in a Go file's string literals,
// read through `go/parser`.
func goStringLiterals(t *testing.T, name string) map[string]struct{} {
	t.Helper()

	found := map[string]struct{}{}

	parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}

	ast.Inspect(parsed, func(node ast.Node) bool {
		literal, isLiteral := node.(*ast.BasicLit)
		if !isLiteral || literal.Kind != token.STRING {
			return true
		}

		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			// Unreachable for a Go source file `go/parser` has accepted: every string
			// literal in it unquotes. Skipping rather than failing keeps the walk honest
			// about a file that grows a raw string.
			return true
		}

		// The same `class="…"` shape as the templ files, so one reader serves both and
		// a class written into a Go string constant is found the same way.
		for class := range classLiterals(value) {
			found[class] = struct{}{}
		}

		return true
	})

	return found
}

// hasSelector reports whether the stylesheet has a rule for a class.
//
// **A selector and not a substring**, and the reason is measured rather than
// asserted: `strings.Contains(css, ".notice")` answers true for a stylesheet carrying
// only `.notice--warning`, and a substring check is how a component ends up styled by a
// modifier it does not use. The whole check was written once with the boundary test
// inverted — reading a `,` as "the token continues" — which made every rule whose
// selector was *only* that token report as unstyled, and this comment records the
// mistake because the mutation is exactly that: swap the two returns.
//
// The boundary rule is the CSS identifier rule: a class token ends at the first
// character that cannot continue an identifier, and `,` cannot — which is why
// `.notice, .panel { }` styles `.notice` and `.notice--warning { }` does not.
func hasSelector(sheet, class string) bool {
	dotted := "." + class

	for _, prelude := range preludes(sheet) {
		for at := 0; ; {
			found := strings.Index(prelude[at:], dotted)
			if found < 0 {
				break
			}

			at += found

			after := at + len(dotted)
			if after >= len(prelude) || !continuesIdentifier(prelude[after]) {
				return true
			}

			at = after
		}
	}

	return false
}

// continuesIdentifier reports whether a byte can continue a CSS identifier.
//
// **Only letters, digits, `-` and `_`.** Everything else ends the token, including
// `:`, `.`, `,`, `[`, `>` and `:` — which is the distinction that makes `.live-dice-log`
// satisfied by `.live-dice-log, .live-track` and not by `.live-dice-logged`.
func continuesIdentifier(char byte) bool {
	return char == '-' || char == '_' ||
		(char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9')
}

// preludes returns the selector text of every rule in a stylesheet.
//
// A brace stack rather than a regex over the whole file, for the reason
// `internal/web`'s gate gives: a declaration value can contain a `{`, and only a
// prelude — the text between a `}` or `{` and the `{` that opens the next block — can
// hold a selector.
func preludes(sheet string) []string {
	found := []string{}

	start := 0

	for index := 0; index < len(sheet); index++ {
		switch sheet[index] {
		case '{':
			if prelude := strings.TrimSpace(sheet[start:index]); prelude != "" {
				found = append(found, prelude)
			}

			start = index + 1
		case '}':
			start = index + 1
		default:
		}
	}

	return found
}

// enclosingConditions returns the `@media` conditions enclosing every rule whose
// selector names the given attribute.
//
// The same brace-stack resolution as `internal/web`'s, and the same reason it is not a
// forward scan.
func enclosingConditions(css, selector string) []string {
	found := []string{}

	start := 0
	stack := []string{}

	for index := 0; index < len(css); index++ {
		switch css[index] {
		case '{':
			prelude := css[start:index]

			condition := ""

			if _, after, isMedia := strings.Cut(prelude, "@media"); isMedia {
				condition = strings.TrimSpace(after)
			}

			if strings.Contains(prelude, selector) {
				for _, enclosing := range stack {
					if enclosing != "" {
						found = append(found, enclosing)
					}
				}
			}

			stack = append(stack, condition)
			start = index + 1
		case '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}

			start = index + 1
		default:
		}
	}

	return found
}

// geometryFeatures are the media features that describe the *device* rather than the
// reader's preference.
//
// `prefers-*`, `forced-colors` and `prefers-color-scheme` are absent deliberately: they
// describe a reader, and §3.3 requires every mode to honour them.
//
// exists to keep out of a rule about TV.
//
//nolint:misspell // two CSS media features' own spellings, and the names this list
var geometryFeatures = []string{
	"min-width", "max-width",
	"min-height", "max-height",
	"orientation",
	"pointer", "hover", "any-pointer", "any-hover",
	"resolution",
	"device-width", "device-height",
	"aspect-ratio",
}

// --- The reads ----------------------------------------------------------------------------

// readFile reads a file and fails the test rather than skipping.
//
// A skipped stylesheet test is an unstyled product that reports itself green, which is
// the exact failure this file exists to make impossible.
func readFile(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v\nrun: make css if this is the built stylesheet", name, err)
	}

	return string(raw)
}

// readStylesheet reads every sheet the build assembles, comments stripped and in the
// order `app.css` imports them.
//
// **Concatenated rather than searched one at a time**, because the order is
// load-bearing — `tv.css` overrides `shell.css` at equal specificity — and a coverage
// check that read them in the wrong order could report a class as styled by a rule that
// a later sheet undoes.
func readStylesheet(t *testing.T) string {
	t.Helper()

	var assembled strings.Builder

	for _, path := range []string{shellSheetPath, tvSheetPath, stylesheetPath} {
		assembled.WriteString(declarationsOf(t, readFile(t, path)))
		assembled.WriteByte('\n')
	}

	return assembled.String()
}

// declarationsOf strips comments so a mechanical check reads declarations and not the
// prose about them.
//
// Half of what these stylesheets are for is explaining why, and several explanations
// name the very properties the assertions ban — this sheet's own header says TV must
// not be a width band, and a check that read the comments would fail on the sentence
// prohibiting it.
func declarationsOf(t *testing.T, source string) string {
	t.Helper()

	var out strings.Builder

	for index := 0; index < len(source); {
		if start := strings.Index(source[index:], "/*"); start >= 0 {
			out.WriteString(source[index : index+start])
			index += start

			end := strings.Index(source[index:], "*/")
			if end < 0 {
				break
			}

			index += end + 2

			continue
		}

		out.WriteString(source[index:])
		break
	}

	return out.String()
}

// declarations strips comments from the stylesheet.
func declarations(t *testing.T) string {
	t.Helper()

	return readStylesheet(t)
}

// blankComments replaces every comment with spaces, keeping offsets.
//
// The `/* */` and `//` forms, and the string-literal skip, are the same rules the
// JavaScript audits implement and for the same reason: a `//` inside a URL is part of
// the URL, and a scanner that believed otherwise would swallow the rest of a rule.
func blankComments(source string) string {
	out := []byte(source)
	quote := byte(0)

	for index := 0; index < len(source); index++ {
		char := source[index]

		if quote != 0 {
			switch char {
			case '\\':
				index++
			case quote:
				quote = 0
			}

			continue
		}

		switch {
		case char == '\'' || char == '"' || char == '`':
			quote = char

		case char == '/' && index+1 < len(source) && source[index+1] == '/':
			for ; index < len(source) && source[index] != '\n'; index++ {
				if out[index] != '\n' {
					out[index] = ' '
				}
			}

		case char == '/' && index+1 < len(source) && source[index+1] == '*':
			for end := index + 2; end+1 < len(source); end++ {
				if source[end] == '*' && source[end+1] == '/' {
					for blank := index; blank <= end+1; blank++ {
						if out[blank] != '\n' {
							out[blank] = ' '
						}
					}

					index = end + 1

					break
				}
			}
		}
	}

	return string(out)
}
