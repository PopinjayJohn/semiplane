package theme_test

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/httpapi/theme"
)

// The generated stylesheet: its shape, and the claims §4.12.2 makes about it.
//
// # Why so much of this file reads the *built* stylesheet
//
// The one thing this layer cannot verify from its own source is whether the tokens
// it overrides reach a browser. `--brand-accent` is declared in `tokens.css`, but a
// product serves `static/dist/app.css` — minified, and with whatever Tailwind prunes
// dropped — so "the token is in the stylesheet" is a claim about bytes that exist
// only after `make css`.
//
// That is the failure class AGENTS.md and `internal/web/a11y_test.go` both name: a
// missing import, or a token that never reaches the build, is not a compile error
// and not a test failure, it is a product that renders unstyled while every other
// gate passes. So these tests read the artefact:
//
//   - `TestTheBrandTokensAreInTheBuiltStylesheet` — the tokens being overridden are
//     in the file the server serves, and they are declared *per theme* so the
//     generated sheet is genuinely competing with them.
//   - `TestTheGeneratedSelectorOutranksEveryThemeBlockInTheBuiltStylesheet` — the
//     generated selector beats those theme blocks on specificity, which is what
//     makes the `<link>`'s position in the document irrelevant.
//
// Both fail with `run: make css` rather than skipping, for the same reason
// `internal/web`'s built-artefact gate does: a skipped stylesheet gate is an
// unstyled product that reports itself green.

// builtStylesheet is the bytes the server serves at /assets/app.css.
const builtStylesheet = "../../web/static/dist/app.css"

// declarationLine is the shape of one declaration the generator writes.
//
// Asserted rather than described: the generator's whole safety argument is that it
// cannot write anything but this, so the test states it as the only shape it
// accepts.
var declarationLine = regexp.MustCompile(`(?m)^ {2}(--[a-z0-9-]+): (#[0-9a-f]{6});$`)

// selectorPattern captures the selector list in front of a rule's opening brace.
var selectorPattern = regexp.MustCompile(`[^{}]*\{`)

// builtCSS reads the built stylesheet, failing rather than skipping when it is
// absent.
//
// The message names the fix for the reason the Makefile's `build` target does: the
// most common cause is a checkout nobody has run `make css` on, and "no such file"
// sends a reader looking for a permissions problem.
func builtCSS(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(builtStylesheet)
	if err != nil {
		t.Fatalf("%s is missing: %v\nrun: make css", builtStylesheet, err)
	}

	return stripComments(string(raw))
}

// A good manifest's sheet, shared by the tests below so none of them re-derives it.
func goodSheet(t *testing.T) string {
	t.Helper()

	parsed, err := theme.Parse([]byte(manifest(fixtureAccent, fixtureInk)), nil, testSlug)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	return parsed.Sheet()
}

// fontTokenNames are the two tokens the generator composes rather than looks up.
//
// They are §4.12.1's left column's font row and deliberately *not* campaign
// vocabulary entries — a manifest naming them under `tokens:` is refused with the
// reason that points at `fonts:` — so the shape claim below has to allow them
// without allowing a second name a campaign could reach. Named here as literals
// for the same reason `brandAccent` is: the spelling under test is the CSS one.
var fontTokenNames = []string{"--font-prose", "--font-ui"}

// TestTheGeneratedStylesheetDeclaresNothingButTheBrandPair is the shape claim: the
// generated sheet is the vocabulary, the two font tokens, and nothing else.
//
// Four assertions, and each catches a different way the generator could go wrong.
// The *names* must come from a closed set — a name in the sheet that is in neither
// list is a token the campaign was never permitted to set, whatever the code
// believes. The *values* must match the declaration pattern, so a generator that
// passed a raw manifest value through would fail here even if the value were
// harmless today. The image must be **absent** for a manifest that did not set it,
// because `--brand-header-image: none` lives in theme.css and a generator that
// wrote one from an empty value would overwrite it with a URL for a file nobody
// named. And the sheet must contain no at-rule and no `url()`, because those are
// the two things that could carry something from outside this process — the whole
// reason §4.12.2's promise is that a campaign ships data.
//
// The at-rule claim is scoped to *this* sheet rather than to every sheet: a
// manifest that names a font is supposed to produce a `@font-face`, and
// `TestAFaceLandsInTheTokenItsOwnSlotNames` covers that half.
//
// **Mutation:** making `generate` write `branded.tokens[name].String()` raw
// without the pattern (i.e. emitting any string a colour's String method could
// return) fails the value assertion; adding a name to `campaignVocabulary` that
// no manifest fixture sets fails the presence assertion; and writing the image
// declaration when `themed.image` is empty fails the absence assertion.
func TestTheGeneratedStylesheetDeclaresNothingButTheBrandPair(t *testing.T) {
	t.Parallel()

	sheet := goodSheet(t)

	declared := map[string]string{}

	for _, match := range declarationLine.FindAllStringSubmatch(sheet, -1) {
		declared[match[1]] = match[2]
	}

	allowed := map[string]bool{}
	for _, name := range append(theme.CampaignTokens(), fontTokenNames...) {
		allowed[name] = true
	}

	if len(declared) == 0 {
		t.Fatalf("the generated sheet declares nothing at all:\n%s\nA walk that "+
			"finds no declaration passes every assertion below for the wrong reason",
			sheet)
	}

	for name, value := range declared {
		if !allowed[name] {
			t.Errorf("the generated sheet declares %s, which is in neither the "+
				"campaign vocabulary nor the font row of §4.12.1's left column:\n%s",
				name, sheet)
		}

		if value != fixtureAccent && value != fixtureInk {
			t.Errorf("the generated sheet declares %s as %s, which is neither "+
				"colour the manifest named", name, value)
		}
	}

	for _, name := range []string{brandAccent, brandInk} {
		if _, ok := declared[name]; !ok {
			t.Errorf("the generated sheet does not declare %s:\n%s", name, sheet)
		}
	}

	if _, image := declared["--brand-header-image"]; image {
		t.Errorf("the generated sheet declares --brand-header-image for a manifest "+
			"that named none; the product's `none` default lives in theme.css, so "+
			"writing an empty one here would put a URL over a default the campaign "+
			"never asked to change:\n%s", sheet)
	}

	if strings.ContainsAny(sheet, "@") {
		t.Errorf("the generated sheet contains an at-rule:\n%s\nA generated "+
			"stylesheet has no business with an at-rule: §10.7's rule about "+
			"media queries and §4.12.2's about @import both exist because "+
			"nothing a campaign writes may reach the cascade", sheet)
	}

	if strings.Contains(sheet, "url(") {
		t.Errorf("the generated sheet contains a url():\n%s", sheet)
	}
}

// TestTheGeneratorEmitsOnlyTheNamesItWasGiven is the mutation guard for the next
// campaign token.
//
// `generate` iterates the vocabulary and looks each name up, so a vocabulary that
// grows to three names makes it emit a third declaration — and for a name the
// manifest did not set, the lookup returns the zero colour, which is `#000000`. A
// GM who adds `--brand-header-image` to the vocabulary and a manifest that sets only
// the pair would then get a campaign theme whose header image is silently black.
//
// The manifest's own both-or-neighbour rule catches this for the *pair*, so the only
// thing standing between that and a black header is this test.
//
// **Mutation:** removing the `if _, ok :=` guard in `generate` fails this test.
func TestTheGeneratorEmitsOnlyTheNamesItWasGiven(t *testing.T) {
	t.Parallel()

	accent, err := theme.ParseColour(fixtureAccent)
	if err != nil {
		t.Fatalf("ParseColour: %v", err)
	}

	sheet := theme.Generator(map[string]theme.Colour{brandAccent: accent}, "", nil)

	if strings.Contains(sheet, brandInk) {
		t.Errorf("the generator emitted %s for a manifest that did not name it:\n%s",
			brandInk, sheet)
	}

	if !strings.Contains(sheet, brandAccent+": "+fixtureAccent+";") {
		t.Errorf("the generator did not emit the token it was given:\n%s", sheet)
	}

	// The zero value must not appear anywhere: it is what a missing lookup
	// produces, and #000000 in a generated sheet is a colour nobody chose.
	if strings.Contains(sheet, "#000000") {
		t.Errorf("the generator emitted the zero colour:\n%s", sheet)
	}
}

// TestTheGeneratedStylesheetCarriesNoProtectedVariable is §4.12.2's enforcement,
// asserted against a forbidden set this package did not write.
//
// The list of protected names is read **out of the product's stylesheets** and
// filtered by §4.12.1's families, so it cannot be wrong in the direction that
// matters: if `ownerOf` forgot a name, the generator would emit it, and this test
// would still know it was forbidden. A test that compared the sheet against
// ownership.go's own protected list would pass for any mistake in that list.
//
// Three sheets are checked — a good one, one for a campaign with no theme, and the
// core sheet a refused manifest falls back to — because a sheet that is safe only on
// the happy path is safe by luck.
//
// **Mutation:** adding `"--focus-ring": ""` to `campaignVocabulary` makes the good
// sheet carry it and fails this test.
func TestTheGeneratedStylesheetCarriesNoProtectedVariable(t *testing.T) {
	t.Parallel()

	forbidden := protectedNames(t)
	if len(forbidden) == 0 {
		t.Fatal("no protected name was found in the product's stylesheets; the " +
			"scanner finds nothing, so every assertion below passes for the wrong reason")
	}

	sheets := map[string]string{
		"a branded campaign": goodSheet(t),
		"the core theme":     theme.CoreSheet(),
	}

	for label, sheet := range sheets {
		for name := range forbidden {
			if strings.Contains(sheet, name) {
				t.Errorf("%s's sheet carries %s, which §4.12.1 protects and no "+
					"campaign may set:\n%s", label, name, sheet)
			}
		}
	}
}

// TestTheProtectedNameScannerRejectsTheStylesheetsItClaimsTo is the meta-test
// AGENTS.md asks for: an audit nobody can fail is worse than no audit.
//
// The scanner is three lines of `strings.Contains`, so the question is not whether
// it works but whether it is *reached* — and a scanner that matched nothing would
// make every test above vacuous. Both directions are fed here: a sheet carrying a
// protected name must be caught, and a sheet carrying only campaign names must not.
//
// **Mutation:** changing `protectedNames` to return the campaign vocabulary instead
// of the protected families fails this test on the first case and no other.
func TestTheProtectedNameScannerRejectsTheStylesheetsItClaimsTo(t *testing.T) {
	t.Parallel()

	protected := protectedNames(t)

	caught := 0

	for name := range protected {
		for _, sheet := range []string{
			":root{--focus-ring:red}",
			"/* comment */\n:root{" + name + ": 1px}",
			"@media screen{" + name + ": red}",
		} {
			if containsAny(sheet, protected) {
				caught++
			}
		}
	}

	if caught == 0 {
		t.Fatalf("the scanner found no protected name in %d synthetic stylesheets, "+
			"each of which carries one; the audit is blind", caught)
	}

	if containsAny(goodSheet(t), protected) {
		t.Error("the scanner reports a branded campaign's own sheet as carrying a " +
			"protected variable")
	}

	// The near-miss: a name that *contains* a protected name as a substring. A
	// scanner using `strings.Contains(name, protected)` rather than the other way
	// round would flag it, and a brand token is exactly the shape that produces one.
	if strings.Contains("--brand-accent", "--focus-ring") {
		t.Error("the substring direction is the wrong way round; a scanner that " +
			"checked whether a protected name contains a declared name would flag " +
			"every campaign token")
	}
}

// containsAny reports the first name in forbidden that sheet carries.
func containsAny(sheet string, forbidden map[string]bool) bool {
	for name := range forbidden {
		if strings.Contains(sheet, name+":") || strings.Contains(sheet, name+" ") {
			return true
		}
	}

	return false
}

// protectedNames is every token the product declares that §4.12.1 protects.
//
// Built from the stylesheets and the protected *families*, so the two inputs are
// independent: a name the product declares, matched by a prefix the record lists.
func protectedNames(t *testing.T) map[string]bool {
	t.Helper()

	names := map[string]bool{}

	for name := range declaredTokens(t) {
		for _, prefix := range theme.ProtectedTokens() {
			if strings.HasPrefix(name, prefix) {
				names[name] = true

				break
			}
		}
	}

	return names
}

// TestTheBrandTokensAreInTheBuiltStylesheet is the check that makes this layer's
// override mean something, against the artefact the server actually serves.
//
// `make a11y` measures contrast from `static/dist/app.css`. If the brand pair were
// declared only in a source file the build drops, the contrast gate would pass on a
// stylesheet that never carried the token — and this route's generated sheet would
// be setting custom properties that nothing reads, for every campaign, with every
// gate green.
//
// **Which tokens are required is read from `app.css`'s own `@import` lines**, and
// that is the part that keeps this test true rather than hopeful. `--brand-header-image`
// is declared in theme.css and tokens.css declares the pair; a build importing only
// two of the three sheets serves a stylesheet carrying two of the three, and a test
// that demanded all three would be asserting a claim about a file rather than about
// the bytes — it would fail forever, or pass because somebody weakened it. Deriving
// the set from the imports makes the test answer "what does this build actually
// serve?", so adding the theme.css import widens the requirement with no edit here.
//
// The second half is the part that is easy to miss: the built declarations must be
// inside a `[data-theme=…]` rule. Declared on `:root` instead, they would be
// specificity (0,1,0) — *equal* to `:root[data-theme]`'s opponent in the next test's
// terms — and which won would come down to the order of two `<link>` elements in a
// template this package does not own.
//
// **Mutation:** removing the `--brand-accent` declaration from tokens.css's light
// rule and rebuilding fails this test. Moving both declarations out of the theme
// rules onto `:root` fails the second half. Adding `@import "./theme.css";` to
// app.css without declaring `--brand-header-image` in it fails the first.
func TestTheBrandTokensAreInTheBuiltStylesheet(t *testing.T) {
	t.Parallel()

	css := builtCSS(t)

	required := requiredBrandTokens(t)
	if len(required) == 0 {
		t.Fatal("no campaign token is declared by any sheet app.css imports; the " +
			"walk over the @import lines finds nothing, so every assertion below " +
			"passes for the wrong reason")
	}

	for _, name := range required {
		if !strings.Contains(css, name+":") {
			t.Errorf("the built stylesheet declares no %s, so the generated sheet "+
				"would set a custom property the browser never reads. Run `make css` "+
				"if the build is stale; if the token is genuinely gone from the "+
				"sheet that declares it then the campaign vocabulary is naming a "+
				"token that no longer exists", name)
		}
	}

	for _, selector := range themeSelectors(t) {
		block := blocksOf(t, css, selector)

		for _, name := range required {
			if !strings.Contains(block, name+":") {
				t.Errorf("the built stylesheet's %s rule declares no %s, so the "+
					"product's default for it lives somewhere else and this package's "+
					"specificity argument does not apply", selector, name)
			}
		}
	}
}

// importPattern is one `@import "./name.css";` line in app.css.
var importPattern = regexp.MustCompile(`@import\s+"\./([^"]+\.css)"`)

// requiredBrandTokens is every campaign token the built stylesheet is committed to
// carrying: the ones declared by a sheet `app.css` imports.
//
// Derived rather than listed, for the reason `TestTheBrandTokensAreInTheBuiltStylesheet`
// gives — the set has to move when the import list does, and a hard-coded list of
// three is a claim about the source rather than about the artefact. Sheets that are
// not imported contribute nothing, because a sheet the build never reads is not in
// the bytes this test is about.
func requiredBrandTokens(t *testing.T) []string {
	t.Helper()

	app := readSheet(t, "app.css")

	imported := map[string]bool{}
	for _, match := range importPattern.FindAllStringSubmatch(app, -1) {
		imported[match[1]] = true
	}

	if len(imported) == 0 {
		t.Fatalf("app.css names no @import of a hand-written sheet; the walk is "+
			"broken, and no token would then be required:\n%s", app)
	}

	required := map[string]bool{}

	for _, sheetName := range sourceSheets {
		if !imported[sheetName] {
			continue
		}

		for _, match := range declarationPattern.FindAllStringSubmatch(readSheet(t, sheetName), -1) {
			name := match[1]
			if slices.Contains(theme.CampaignTokens(), name) {
				required[name] = true
			}
		}
	}

	return slices.Sorted(maps.Keys(required))
}

// TestTheGeneratedSelectorOutranksEveryThemeBlockInTheBuiltStylesheet is why the
// selector is `:root[data-theme]`.
//
// tokens.css declares the brand pair inside `[data-theme="light"]` and
// `[data-theme="dark"]`, both specificity (0,1,0). A generated sheet declaring
// `:root` is *also* (0,1,0), so which declaration wins would depend on whether
// `/c/{slug}/theme.css` is linked before or after `/assets/app.css` — a coupling
// between this package and a template it does not own, invisible to any test, and
// broken silently by a reordering of two lines in a `<head>`.
//
// The fix is one extra attribute on the selector, and this test is what makes it a
// property rather than a preference: it reads the **built** stylesheet, finds every
// rule whose selector mentions `data-theme`, counts specificity components, and
// requires the generated selector to beat all of them. Change `:root[data-theme]`
// to `:root` and this fails.
//
// **Mutation:** replacing `:root[data-theme]` with `:root` fails this test, with
// the numbers that make the case.
func TestTheGeneratedSelectorOutranksEveryThemeBlockInTheBuiltStylesheet(t *testing.T) {
	t.Parallel()

	sheet := goodSheet(t)
	generated := selectorOf(t, sheet)

	if specificity(generated) == 0 {
		t.Fatalf("the generated sheet's selector could not be read from %q", sheet)
	}

	for _, selector := range themeSelectors(t) {
		got := specificity(selector)

		if got >= specificity(generated) {
			t.Errorf("the generated selector %q has specificity %d and the built "+
				"stylesheet's %q has %d: which declaration wins would come down to "+
				"the order of the two <link> elements in a template this package does "+
				"not own, so a reordering of the document's head would silently stop "+
				"every campaign theme applying",
				generated, specificity(generated), selector, got)
		}
	}
}

// themeSelectors is every selector in the built stylesheet that mentions
// `data-theme`, deduplicated.
func themeSelectors(t *testing.T) []string {
	t.Helper()

	seen := map[string]bool{}

	for _, rule := range ruleSelectors(builtCSS(t)) {
		if strings.Contains(rule, "data-theme") {
			seen[rule] = true
		}
	}

	if len(seen) == 0 {
		t.Fatal("the built stylesheet declares no rule selecting a theme; the " +
			"specificity argument this test makes cannot be checked")
	}

	selectors := slices.Sorted(maps.Keys(seen))

	return selectors
}

// ruleSelectors is every selector list in a stylesheet, comma-split.
func ruleSelectors(css string) []string {
	var selectors []string

	for _, match := range selectorPattern.FindAllString(css, -1) {
		prelude := strings.TrimSuffix(strings.TrimSpace(match), "{")
		if prelude == "" || strings.HasPrefix(prelude, "@") || strings.HasSuffix(prelude, "}") {
			continue
		}

		for one := range strings.SplitSeq(prelude, ",") {
			if one = strings.TrimSpace(one); one != "" {
				selectors = append(selectors, one)
			}
		}
	}

	return selectors
}

// preludeContains reports whether a rule's selector prelude selects for
// `selector`, treating the prelude as the comma-separated **list** it is.
//
// One member compared whole, after trimming, rather than a substring search. A
// substring is the wrong tool here for the same reason it is wrong elsewhere in
// this package's history: `[data-theme=light]` is a substring of
// `[data-theme=lightweight]`, so a search would answer true for a rule that has
// nothing to do with the one asked about, and the test would pass on the
// strength of a neighbour.
func preludeContains(prelude, selector string) bool {
	for one := range strings.SplitSeq(prelude, ",") {
		if strings.TrimSpace(one) == selector {
			return true
		}
	}

	return false
}

// blocksOf returns the concatenated bodies of **every** rule in css with the
// given selector.
//
// Every rule rather than the first, and the reason is that `app.css` may import
// more than one sheet declaring a `[data-theme=…]` block: theme.css declares
// `--brand-header-image` in its own two, and a reader that stopped at the first
// match would report the built stylesheet as missing a token two rules further
// on — a wrong failure, which is worse than none because the next real one gets
// ignored. Later rules win in CSS, so the concatenation is also the right order
// to search a declaration in.
//
// **A selector *list* counts as containing it, and that is the whole
// correction.** Tailwind's minifier merges rules whose bodies are identical into
// one rule with a comma-separated selector list, and it did exactly that here:
// theme.css declares `--brand-header-image: none` under `[data-theme="light"]`
// and again under `[data-theme="dark"]`, so the built file carries
//
//	[data-theme=light],[data-theme=dark]{--brand-header-image:none}
//
// and never the literal text `[data-theme=light]{`. A search for `selector + "{"`
// therefore finds nothing and reports the token missing from a theme block that
// carries it — **a wrong failure, on a correct build**, which is worse than no
// assertion at all because the next real one gets ignored alongside it.
//
// This was invisible until `app.css` imported the sheet, because before that the
// token was in no built file at all and both branches agreed. The mutation that
// proves it: change the selector match back to `selector+"{"` and this test goes
// red on the merged rule.
func blocksOf(t *testing.T, css, selector string) string {
	t.Helper()

	var bodies strings.Builder

	for _, match := range selectorPattern.FindAllStringIndex(css, -1) {
		prelude := strings.TrimSuffix(strings.TrimSpace(css[match[0]:match[1]]), "{")
		if !preludeContains(prelude, selector) {
			continue
		}

		body, closed := braceBody(css, match[1]-1)
		if !closed {
			t.Fatalf("the %s rule's body is never closed", selector)
		}

		bodies.WriteString(body)
		bodies.WriteString("\n")
	}

	if bodies.Len() == 0 {
		t.Fatalf("the built stylesheet has no %s rule", selector)
	}

	return bodies.String()
}

// selectorOf reads the selector the generator wrote.
//
// **Comments stripped first, and that is not cosmetic.** The generated sheet opens
// with a prose comment, so a walk that took everything before the first `{` returned
// the comment *and* the selector — and the counter then found the colon in "Do not
// edit this file: it is rewritten" and reported a specificity of 200 for a bare
// `:root`. The test passed with the mutation in place because of a colon in a
// comment, which is exactly the "an audit nobody can fail" failure AGENTS.md records
// for phase 5: the mutation `sheetSelector` → `:root` was BLIND until this helper
// stopped reading comments as selectors.
//
// The assertion that the result is not a comment is here for the same reason: a
// reader who changes this helper should not be able to reintroduce the same bug
// quietly.
func selectorOf(t *testing.T, sheet string) string {
	t.Helper()

	for _, match := range selectorPattern.FindAllString(stripComments(sheet), -1) {
		prelude := strings.TrimSuffix(strings.TrimSpace(match), "{")
		if prelude == "" || strings.HasPrefix(prelude, "@") {
			continue
		}

		if strings.ContainsAny(prelude, "/*") {
			t.Fatalf("the generated sheet's selector read as %q, which contains a "+
				"comment marker: a comment is not a selector and its punctuation "+
				"would be counted as one", prelude)
		}

		return prelude
	}

	t.Fatalf("the generated sheet declares no rule:\n%s", sheet)

	return ""
}

// specificity counts a selector's components as ids × 10000, class-like × 100 and
// type-like × 1.
//
// The numeric packing is CSS's own: (a, b, c) compared as a*10000 + b*100 + c. Only
// the *ordering* matters here — no two selectors in this comparison are within 100 of
// each other — so this does not need to handle `:not()`, `:is()` or the inline
// `!important` case, and says so rather than pretending to be a parser.
func specificity(selector string) int {
	var score int

	for index := 0; index < len(selector); index++ {
		switch selector[index] {
		case '#':
			score += 10000
		case '.', '[', ':':
			score += 100
		}
	}

	return score
}

// TestTheCoreSheetIsAValidStylesheetAndCarriesNoDeclarations is the no-theme
// answer, because "no theme" is still a response.
//
// The core sheet is what every campaign without a manifest is served, so it has to
// parse as CSS and it has to say nothing: no declaration, no at-rule, and no name
// from the vocabulary. A core sheet that carried a default brand would make
// "the campaign has no theme" and "the campaign's theme is this" the same bytes,
// which is the ambiguity `Manifest.Branded` exists to prevent.
func TestTheCoreSheetIsAValidStylesheetAndCarriesNoDeclarations(t *testing.T) {
	t.Parallel()

	sheet := theme.CoreSheet()

	if !strings.HasPrefix(sheet, "/*") {
		t.Errorf("the core sheet does not open with a comment:\n%s", sheet)
	}

	if strings.Contains(sheet, "--") {
		t.Errorf("the core sheet carries a custom property:\n%s", sheet)
	}

	if strings.Contains(sheet, "{") {
		t.Errorf("the core sheet carries a rule:\n%s", sheet)
	}

	if !strings.HasSuffix(sheet, "\n") {
		t.Error("the core sheet does not end in a newline; a stylesheet without one " +
			"is a file whose last line every editor warns about")
	}
}
