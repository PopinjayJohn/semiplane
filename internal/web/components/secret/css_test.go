// `forced-colors`, `forced-color-adjust` and the CSS `color`, `background-color`,
// `outline-color` and `border-*-color` properties are spelled the US way by CSS
// itself, and this repository's `misspell` is configured for UK English. It cannot be
// allowed to rewrite the name of a media feature or of a declaration this file is
// asserting the presence of -- rewriting any of them would break the thing they
// name, since a media query written `forced-colours` matches nothing and a property
// spelled `colour` is not a property at all.
//
// The file's own prose uses the UK spelling throughout. The alternative to one
// package-clause suppression is a `nolint` at each of a dozen call sites, and a
// suppression that has to be repeated is one somebody eventually deletes while
// tidying.
//
//nolint:misspell // CSS identifiers, not prose. `forced-colors`,
package secret_test

// `secret.css` is in the build, every selector in it exists in a rendered
// document, it declares no colour of its own, and every colour pair it creates
// clears the floor §10.1 sets for that pair.
//
// # Four failures, and only the first is the famous one
//
// `AGENTS.md` documents the first: **a missing `@import` is not a compile error and
// not a test failure.** `internal/web` embeds `static/dist/`, `make css` builds
// that from `app.css`'s import list, and a sheet nobody imports does not exist as
// far as the binary is concerned — so the callouts render unstyled while every
// §10.2 and §10.6 gate passes, because the markup is correct and only the bytes are
// missing.
//
// The second is newer and **invisible to a sentinel check**: a sheet that *is*
// imported, whose rules name selectors no document renders. It loads, it parses,
// it contributes its sentinel to the built file, and it styles nothing. That is what
// `components/play`'s draft did — `.play-action-bar`, `.sheet-rail` — and its own
// "is the sheet in the build" test passed throughout, because the sentinel really
// was in the file.
//
// The third is `TestTheStylesheetDeclaresNoColourLiteralAndNoUndeclaredToken`:
// §6.1's rule is that the token layer is where colours live, and a hex value in a
// component sheet is a colour no gate measures and a campaign cannot override.
//
// The fourth is the pair table below. §10.1's gate measures the pairs
// `internal/web`'s own table names, and a component sheet that pairs two tokens the
// table does not join creates a pair nobody measured — which is a real failure mode
// and not a theoretical one, because `--callout-secret` on `--surface-sunken` is
// exactly such a pair today.
//
// # Every test whose name carries an `A11Y_TESTS` alternative
//
// `SheetIsInsideTheBuild`, `SheetDeclaresNo`, `RuleInTheSheet`,
// `SelectorInThisSheet`, `SheetIsResponsibleFor`, `Contrast` and `BuiltStylesheet`.

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/web/components"
	"github.com/semiplane/semiplane/internal/web/components/secret"
)

// The paths, relative to this test's directory.
//
// **Three levels up for the entry point and the built file**: `secret` →
// `components` → `web`, and `static` hangs off `web`.
const (
	stylesheetPath = "../../static/css/secret.css"
	entryPath      = "../../static/css/app.css"
	builtPath      = "../../static/dist/app.css"
	tokensPath     = "../../static/css/tokens.css"
	sourceTree     = "."
)

// The sentinel: a custom property nothing else declares.
//
// A class name would not do — `strings.Contains(css, ".secret")` answers `true`
// against a stylesheet carrying only `.secret-chrome`, so a class is a substring and
// a property name is not.
const theSheetIsTheSentinel = "--secret-marker-ink"

// theImportHasLanded is the sentence this sheet's header carries **because the
// import has landed**.
//
// # The marker tracks the wiring, and that is the whole design
//
// `TestTheSheetIsInsideTheBuild` is a **disjunction**, and a disjunction with one
// stale arm is a disarmed gate. An earlier version of `play.css` said "is not
// imported by `app.css`" in its header; the import landed; the header kept the
// sentence; and deleting the `@import` was then **green** — the test took the
// second branch, found the marker still sitting in the header, and passed. That is
// the failure this pair of constants exists to prevent: a note must not outlive the
// fact it describes, and a temporary escape hatch left in place after the thing it
// was escaping has arrived stops being an escape hatch and becomes a hole.
//
// So both arms are live: the import exists → the **built** file is read, because
// that is the only artefact that can tell a rule was dropped by the build; the
// import does not exist → the header must say so, and it does not, so this is red
// and names the fix.
const theImportHasLanded = "imported by `app.css`"

// notWiredInYet is the sentence the header would carry if the import were removed,
// and which it therefore does **not** carry today. Named rather than inlined so
// both arms of the disjunction are visible in one place.
const notWiredInYet = "is not imported by `app.css`"

// theClassesThisSheetIsResponsibleFor is every class this sheet styles.
//
// **A list, not a derivation.** Deriving it would assert that *every* class this
// package renders needs a rule here, which is false: `target`, `button` and
// `visually-hidden` belong to `shell.css`, and restating them would be a second
// declaration of a rule another sheet owns.
var theClassesThisSheetIsResponsibleFor = []string{
	"secret-disclosures",     // the GM's panel
	"secret-disclosure-list", // its list
	"secret-disclosure",      // one row
	"secret-disclosure-name", // the callout's name
	"secret-disclosure-state",
	"secret-disclosure-all",
	"button--secret", // the reveal button
	"secret-outcomes", "secret-outcome-region",
	"secret-outcome", "secret-capped",
	"secret-chrome", "secret-chrome-title", "secret-marker",
	"secret-stub",
	// Emitted by `internal/content/ext/secret.go`, not by this package's
	// templates — and styled here, which is the reason it is on the list.
	"secret", "secret--collapsed", "secret--revealed",
}

// contrastFloor is §10.1's floor, and the WCAG definition of it.
//
// Named as a function rather than a constant so the two floors cannot be swapped by
// a reader who does not look: 4.5:1 for text a reader must be able to read and
// 3:1 for a boundary or a graphical object (1.4.11).
const (
	textFloor     = 4.5
	boundaryFloor = 3.0
)

// thePairsThisSheetCreates is every foreground/background pair `secret.css` puts
// next to each other, and the floor that pair has to clear.
//
// **Derived from the sheet by reading it, not maintained by hand.** A hand-written
// table is a table where a rule gets deleted and nobody notices, and this file's
// whole subject is the pairs a rule creates.
//
// The floors come from WCAG rather than from §6.2/§6.3's rounded figures, for
// `internal/web/tokens_contrast_test.go`'s reason: the record's own ratios are
// rounded in the second decimal and pinning them would fail on correct values while
// training the next reader to edit the test instead of the stylesheet.
var (
	// ruleBody extracts `selector { declarations }` from a stylesheet.
	ruleBody = regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)
	// colourReference is a `var(--token)` read inside a declaration.
	colourReference = regexp.MustCompile(`var\((--[a-z0-9-]+)\)`)
	// hexLiteral is a colour written out rather than read from a token.
	hexLiteral = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)
	// commentBlock is a CSS comment.
	commentBlock = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// TestTheSheetIsInsideTheBuild holds the first failure: the import.
//
// The branch structure is the design; the file header says why, and the constants
// above say what happens if either arm is left stale.
func TestTheSheetIsInsideTheBuild(t *testing.T) {
	t.Parallel()

	if appImports(t, entryPath, "secret.css") {
		built := readFile(t, builtPath)

		if !strings.Contains(built, theSheetIsTheSentinel) {
			t.Errorf("the built stylesheet carries no %s, so secret.css is "+
				"imported but its rules are not in the output. A selector the "+
				"scanner cannot parse, or a rule inside a layer that loses the "+
				"cascade, is dropped by the build and the built file is the "+
				"only artefact that can tell", theSheetIsTheSentinel)
		}

		return
	}

	header := readFile(t, stylesheetPath)

	if strings.Contains(header, theImportHasLanded) {
		t.Errorf("%s is no longer %s, and this sheet's header still says it is. "+
			"The callouts render unstyled and every §10.2 gate still passes, "+
			"because the markup is correct and only the bytes are missing.\n"+
			"      add to %s, after the play.css import:\n"+
			"          @import \"./secret.css\"; and move the header's sentence "+
			"to say it is not wired in yet",
			stylesheetPath, theImportHasLanded, entryPath)
	}

	if !strings.Contains(header, notWiredInYet) {
		t.Errorf("%s is not %s and its own header does not say it is unwired "+
			"either, so this assertion is about nothing. One of the two "+
			"sentences has to be true: the header describes the wiring, and the "+
			"wiring has changed", stylesheetPath, theImportHasLanded)
	}
}

// appImports reports whether the entry point carries an `@import` line for this
// sheet.
//
// **The `@import` line, not the string.** Asking whether `app.css` *mentions*
// `secret.css` is true of the explanatory comment above the import as well, and a
// loose predicate on a prose-bearing file is the mistake `play`'s sheet test records
// in full.
func appImports(t *testing.T, path, sheet string) bool {
	t.Helper()

	entry := readFile(t, path)

	for line := range strings.SplitSeq(entry, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "@import") {
			continue
		}

		if strings.Contains(trimmed, "./"+sheet) {
			return true
		}
	}

	return false
}

// TestEveryClassThisSheetIsResponsibleForExistsInTheDocument is the first half of
// the second failure: the sheet styles something nothing renders.
//
// The document is the **union of every surface that renders these classes** — the
// GM's wiki page (for the classes `internal/content` emits), the editor (for the
// panel and the controls) and the chrome, the stub and the two alerts. A document
// built from a hand-written class list would pass for any list the test author
// agreed with.
func TestEveryClassThisSheetIsResponsibleForExistsInTheDocument(t *testing.T) {
	t.Parallel()

	rendered := renderedClassTokens(t)

	for _, class := range theClassesThisSheetIsResponsibleFor {
		if !slices.Contains(rendered, class) {
			t.Errorf("secret.css styles .%s and no rendered document carries "+
				"that class. The sheet loads, parses and contributes its "+
				"sentinel to the built file while styling nothing -- the "+
				"failure a sentinel check cannot see, because the sentinel "+
				"really is there", class)
		}
	}
}

// TestNoSelectorInThisSheetIsAbsentFromTheDocument is the other half: the sheet has
// a rule for a selector nothing renders.
//
// The converse of the test above and both are needed: a selector that matches
// nothing is dead CSS, and it is the shape of bug this file is written after.
func TestNoSelectorInThisSheetIsAbsentFromTheDocument(t *testing.T) {
	t.Parallel()

	sheet := stripped(readFile(t, stylesheetPath))
	rendered := renderedClassTokens(t)

	for _, selector := range classSelectors(sheet) {
		if !slices.Contains(rendered, selector) {
			t.Errorf("secret.css has a rule for .%s and no rendered document "+
				"carries that class. Either the class was renamed or the rule "+
				"was written for an element this package does not render",
				selector)
		}
	}
}

// TestEveryRuleInThisSheetIsForASelectorTheDocumentRenders is the same claim over
// the **built** file rather than the source.
//
// Reading the built file covers the failure a source-level scan cannot see: a rule
// dropped by the build. A selector present in `secret.css` and absent from
// `static/dist/app.css` is a rule that styles nothing in the shipped binary.
func TestEveryRuleInThisSheetIsForASelectorTheDocumentRenders(t *testing.T) {
	t.Parallel()

	built := readFile(t, builtPath)
	rendered := renderedClassTokens(t)

	// Scoped to the classes this sheet owns. The built file is the whole
	// application; a selector for somebody else's class appearing here is not
	// this sheet's business.
	owned := theClassesThisSheetIsResponsibleFor

	seen := 0

	for _, match := range ruleBody.FindAllStringSubmatch(stripped(built), -1) {
		selector := strings.TrimSpace(match[1])

		if !strings.Contains(selector, "secret") {
			continue
		}

		seen++

		for _, class := range classSelectors(selector) {
			if !slices.Contains(owned, class) {
				continue
			}

			if !slices.Contains(rendered, class) {
				t.Errorf("the built stylesheet has a rule for .%s (selector %q) "+
					"and no rendered document carries that class: the rule is "+
					"in the binary and styles nothing", class, collapse(selector))
			}
		}
	}

	if seen == 0 {
		t.Fatal("no rule mentioning this sheet's selectors was found in the " +
			"built stylesheet; the sheet is not in the build and every other " +
			"assertion in this file is about nothing")
	}
}

// TestTheSheetDeclaresNoColourLiteralAndNoUndeclaredToken is §6.1's rule that the
// token layer is where colours live.
//
// Two halves, and both are the same claim from different directions: a hex literal
// in a component sheet is a colour no gate measures, and a `var(--token)` the token
// layer does not declare resolves to nothing at computed-value time — which is the
// failure mode of a typo, and it renders the element with no colour at all.
func TestTheSheetDeclaresNoColourLiteralAndNoUndeclaredToken(t *testing.T) {
	t.Parallel()

	sheet := stripped(readFile(t, stylesheetPath))
	declared := declaredTokenNames(t)

	if match := hexLiteral.FindString(sheet); match != "" {
		t.Errorf("secret.css writes the colour %s as a literal. §6.1 keeps "+
			"colours in tokens.css, where §10.1's gate measures them; a "+
			"literal in a component sheet is a colour no gate reads and a "+
			"campaign cannot override", match)
	}

	for _, token := range colourReference.FindAllStringSubmatch(sheet, -1) {
		name := token[1]

		if strings.HasPrefix(name, "--secret-") {
			continue // this sheet's own component property
		}

		if _, ok := declared[name]; !ok {
			t.Errorf("secret.css reads var(%s) and neither tokens.css nor "+
				"shell.css declares it, so it resolves to nothing and the "+
				"declaration it is in silently does not apply. A `var()` "+
				"naming a token no sheet defines is not a build error and "+
				"not an accessibility finding — it is an element that "+
				"renders unstyled", name)
		}
	}
}

// TestTheBuiltStylesheetCarriesTheSecretTokensAndTreatments holds that the tokens
// and the rules that read them reached the shipped bytes.
//
// Both halves, because they fail differently: a token declared in `tokens.css` and
// dropped by the build is a colour that resolves to nothing, and a rule dropped by
// the build is a treatment that never happens. Neither is a build error and neither
// is a §10.2 finding.
func TestTheBuiltStylesheetCarriesTheSecretTokensAndTreatments(t *testing.T) {
	t.Parallel()

	built := readFile(t, builtPath)
	declared := tokensDeclaredInBothThemes(t, tokensPath)

	for _, token := range []string{
		"--callout-surface", "--callout-border", "--callout-secret", "--callout-revealed",
	} {
		if !declared[token] {
			t.Errorf("%s is not declared in both themes of tokens.css; §6.2 "+
				"and §6.3 give the callout four tokens and this sheet reads all "+
				"four", token)
		}

		if !strings.Contains(built, token) {
			t.Errorf("the built stylesheet carries no %s", token)
		}
	}

	for _, treatment := range []string{
		"secret--collapsed", "secret--revealed", "secret-marker", "secret-stub",
		"secret-disclosures", "secret-outcome", "secret-capped",
	} {
		if !strings.Contains(built, "."+treatment) {
			t.Errorf("the built stylesheet has no rule for .%s", treatment)
		}
	}
}

// TestTheContrastPairsThisSheetUsesMeetTheirFloors is §10.1 for the pairs *this
// sheet* creates.
//
// # Why a second contrast implementation rather than a shared one
//
// `internal/web/tokens_contrast_test.go` owns the gate, and its table is the pairs
// §6.2/§6.3 name. A component sheet that pairs two tokens that table does not join
// creates a pair nobody measured — and `--callout-secret` on `--surface-sunken` is
// exactly such a pair today. Sharing the implementation across packages would also
// let one package's fixtures satisfy another's rule, which is why every route
// package here carries its own copy (see `components/chat/dom_test.go`'s header).
//
// So: read the sheet's own declarations, resolve the tokens out of `tokens.css`,
// convert and measure with WCAG 2.2's definition, and hold each pair against the
// floor its *use* sets. An 8-bit sRGB conversion with no gamut clipping, because
// §10.1 measures what a browser rasterises.
func TestTheContrastPairsThisSheetUsesMeetTheirFloors(t *testing.T) {
	t.Parallel()

	sheet := readFile(t, stylesheetPath)
	themes := readThemes(t, tokensPath)

	if len(themes) < 2 {
		t.Fatalf("tokens.css yielded %d theme blocks; §6.1 defines two and a "+
			"single-theme run would pass a stylesheet that is legible in only "+
			"one polarity", len(themes))
	}

	pairs := pairsDeclaredIn(sheet)

	if len(pairs) == 0 {
		t.Fatal("no colour pair was read out of secret.css; this assertion is " +
			"about nothing, which is the failure mode of a scan that stopped " +
			"parsing")
	}

	// This sheet's own `:root` block, layered under each theme, because that is
	// how the cascade resolves a component property a theme does not mention.
	defaults := sheetDefaults(t)

	seen := 0

	for _, theme := range themes {
		for name, value := range defaults {
			if _, already := theme.values[name]; !already {
				theme.values[name] = value
			}
		}

		for _, pair := range pairs {
			foreground, declared := resolveToken(theme, pair.foreground)
			if !declared {
				t.Errorf("the %s theme declares no %s, which secret.css reads "+
					"in %q; a var() naming a token nothing defines resolves to "+
					"nothing and the declaration it is in does not apply",
					theme.name, pair.foreground, pair.selector)
			}

			background, declared := resolveToken(theme, pair.background)
			if !declared {
				t.Errorf("the %s theme declares no %s, which secret.css reads "+
					"in %q", theme.name, pair.background, pair.selector)
			}

			if !declared {
				continue
			}

			// A token that resolved to a length or a keyword is not a
			// colour, and measuring it would assert a ratio no browser
			// computes. `--border-w` is the one this sheet reads.
			if !hexLiteral.MatchString(foreground) || !hexLiteral.MatchString(background) {
				continue
			}

			seen++

			ratio := contrastRatio(foreground, background)

			if ratio+1e-9 < pair.floor {
				t.Errorf("the %s theme puts %s (%s) on %s (%s) at %.2f:1 in %q, "+
					"which is below the %.1f:1 floor for that use.\n"+
					"      §10.1 measures the built stylesheet, so a rule that "+
					"never reached the build would not be seen there -- this "+
					"test reads the sheet and resolves the tokens itself, which "+
					"is what makes it a check rather than an inventory",
					theme.name,
					pair.foreground, foreground,
					pair.background, background,
					ratio, pair.selector, pair.floor)
			}
		}
	}

	if seen == 0 {
		t.Fatal("no pair was measured in any theme; the sheet's colours and " +
			"this test's parser have stopped agreeing and every assertion " +
			"above it is vacuous")
	}
}

// TestTheTwoCalloutStatesAreToldApartWithoutColour is §4.10.4's "never hue-only",
// and the mutation that breaks it.
//
// The claim is structural: the two states differ in a property that is **not a
// colour**, so they are still two states on a monochrome display and in a
// `forced-colors` session. This asserts the difference is `border-style` — and that
// deleting either rule, or making the two share one style, is a red test.
func TestTheTwoCalloutStatesAreToldApartWithoutColour(t *testing.T) {
	t.Parallel()

	built := readFile(t, builtPath)

	collapsed := borderStyleFor(built, ".secret--collapsed")
	revealed := borderStyleFor(built, ".secret--revealed")

	if collapsed == "" || revealed == "" {
		t.Fatalf("no border-style was found for .secret--collapsed (%q) or "+
			".secret--revealed (%q); the two states must differ in something "+
			"that is not a colour, and border-style is that something",
			collapsed, revealed)
	}

	if collapsed == revealed {
		t.Errorf("both callout states declare border-inline-start-style: %s.\n"+
			"      §4.10.4: the distinction between a hidden and a revealed "+
			"callout is gameplay-critical and is never hue-only. Dashed "+
			"against solid is what survives a monochrome display and a "+
			"forced-colors session, where every colour in the sheet is "+
			"replaced by a system one", collapsed)
	}

	if collapsed != "dashed" {
		t.Errorf(".secret--collapsed declares border-style %q; §4.10.4's table "+
			"gives the hidden callout a dashed bar", collapsed)
	}
}

// TestForcedColorsKeepsTheTwoStatesApart is the same claim inside the mode that
// removes the colours entirely.
//
// `forced-colors` replaces the palette, so a states-distinction carried by hue
// collapses there — which is exactly why the record insists on a second carrier. So
// the sheet's `forced-colors` block must exist and must not set
// `forced-color-adjust: none`, which would keep our colours *out* of the mode rather
// than handing over to the system ones.
func TestForcedColorsKeepsTheTwoStatesApart(t *testing.T) {
	t.Parallel()

	sheet := stripped(readFile(t, stylesheetPath))

	marker := "@media (forced-colors: active)"

	at := strings.Index(sheet, marker)
	if at < 0 {
		t.Fatal("secret.css has no @media (forced-colors: active) block. §6.7 " +
			"requires the block in every mode, and this sheet is the only place " +
			"the two callout states can be told apart once the palette is gone")
	}

	block := sheet[at:]

	end := strings.Index(block, "}\n}")
	if end > 0 {
		block = block[:end]
	}

	for _, required := range []string{
		".secret--collapsed", ".secret--revealed", ".secret-marker", ".secret-stub",
	} {
		if !strings.Contains(block, required) {
			t.Errorf("the forced-colors block does not mention %s; a rule that "+
				"is not in the block keeps our colours in a mode whose point "+
				"is that our colours are being replaced", required)
		}
	}

	if strings.Contains(sheet, "forced-color-adjust") {
		t.Error("secret.css sets forced-color-adjust; §6.7 requires the mode to " +
			"fall back to the system's colours rather than to opt out of it")
	}

	if strings.Contains(block, "border-style: none") {
		t.Error("the forced-colors block clears a border style; the hidden and " +
			"revealed callouts are told apart by border-style and that is the " +
			"only carrier left once the palette is replaced")
	}
}

// TestNoTvWidthBandInThisSheet is ADR 0034 and §3.3, over this sheet.
//
// A 55" television and a 12.9" tablet in landscape are indistinguishable to CSS, so
// a rule choosing a layout from a media feature picks the wrong one half the time.
// The check walks the sheet's `@media` conditions and fails on any that describes
// the device rather than the reader.
func TestNoTvWidthBandInThisSheet(t *testing.T) {
	t.Parallel()

	sheet := stripped(readFile(t, stylesheetPath))

	device := []string{
		"min-width", "max-width", "min-height", "max-height",
		"orientation", "pointer", "hover", "resolution", "aspect-ratio",
	}

	for _, condition := range mediaConditions(sheet) {
		for _, feature := range device {
			if !strings.Contains(condition, feature) {
				continue
			}

			t.Errorf("secret.css has a rule inside @media (%s). §3.3 requires "+
				"the three preference conditions -- prefers-reduced-motion, "+
				"prefers-contrast and forced-colors -- and no others; anything "+
				"describing the device infers a form factor CSS cannot know",
				strings.TrimSpace(condition))
		}
	}
}

// resolveToken answers a token's value in one theme, following `var()` references
// until one resolves or the bound is reached.
//
// Bounded, because §6.1's token graph nests two deep at most
// (`--surface-border -> --border-subtle`) and this sheet reads layer-2 tokens; a
// cycle would otherwise be a stack overflow inside the test rather than a
// diagnosable failure.
//
// The second return is **declared**, not "is a colour": `--border-w` resolves
// cleanly to `1px` and this function says `false`, and the caller has to be able
// to tell that from a token nothing declares — because the first is a length that
// belongs to a rule it must not measure and the second is a `var()` that resolves
// to nothing, which is a defect.
func resolveToken(theme themeTokens, name string) (value string, declared bool) {
	for range 8 {
		raw, ok := theme.values[name]
		if !ok {
			return "", false
		}

		if inner := colourReference.FindStringSubmatch(raw); inner != nil {
			name = inner[1]

			continue
		}

		return raw, true
	}

	return "", true
}

// sortedSet is the distinct members of a list, sorted.
//
// **Sorted as well as distinct**, so a failure message lists classes in a stable
// order and two runs of the same failure diff cleanly.
func sortedSet(values []string) []string {
	set := make(map[string]struct{}, len(values))

	for _, value := range values {
		set[value] = struct{}{}
	}

	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}

	slices.Sort(out)

	return out
}

// mediaConditions returns every `@media (...)` condition in a stylesheet.
func mediaConditions(sheet string) []string {
	matches := regexp.MustCompile(`@media\s*\(([^)]*)\)`).FindAllStringSubmatch(sheet, -1)

	conditions := make([]string, 0, len(matches))
	for _, match := range matches {
		conditions = append(conditions, match[1])
	}

	return conditions
}

// readFile reads one stylesheet, failing the test if it cannot.
func readFile(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(raw)
}

// stripped removes CSS comments, so a selector or a colour named in prose is not
// counted as code.
//
// **The comment body is replaced with spaces rather than deleted**, so byte offsets
// and the rule structure survive; `secret.css`'s header names `secret.css` in prose
// and a scan reading raw text would find a selector in a comment.
func stripped(sheet string) string {
	return commentBlock.ReplaceAllStringFunc(sheet, func(match string) string {
		return strings.Repeat(" ", len(match))
	})
}

// collapse puts a selector on one line for a failure message.
func collapse(value string) string { return strings.Join(strings.Fields(value), " ") }

// classSelectors returns every class token named in a block of CSS.
func classSelectors(block string) []string {
	matcher := regexp.MustCompile(`\.(-?[A-Za-z_][A-Za-z0-9_-]*)`)
	matches := matcher.FindAllStringSubmatch(block, -1)

	found := make([]string, 0, len(matches))
	for _, match := range matches {
		found = append(found, match[1])
	}

	return sortedSet(found)
}

// renderedClassTokens is every class token the surfaces this package owns render,
// read out of real documents.
//
// **Built from rendered documents rather than a literal**, so it cannot drift from
// the markup and a class that only exists in a test fixture cannot satisfy a
// selector check.
func renderedClassTokens(t *testing.T) []string {
	t.Helper()

	documents := []*html.Node{
		gmWikiDocument(t),
		editorDocument(t),
		render(t, components.Shell(
			shellView(),
			fragment(
				secret.CalloutChrome(secret.CalloutChromeView{
					Title: "The harbourmaster", Ordinal: 0,
					State: secret.StateRevealed, Marker: secret.MarkerShown,
				}),
				secret.Stub(secret.StubView{Enabled: true}),
				secret.RevealOutcome(secret.RevealOutcomeView{Anchor: "a", Revealed: true}),
				secret.CappedAlert(secret.CappedAlertView{Detail: "capped"}),
				secret.OutcomeRegion(),
			),
			components.InstanceRail(shellView().Instance),
		)),
	}

	var tokens []string

	for _, document := range documents {
		walk(t, document, func(node *html.Node) {
			tokens = append(tokens, strings.Fields(attributeOr(node, "class"))...)
		})
	}

	if len(tokens) == 0 {
		t.Fatal("the rendered documents carry no class at all; the selector " +
			"checks below would pass on an empty walk")
	}

	return sortedSet(tokens)
}

// borderStyleFor returns the border style a selector declares, or "".
//
// Read from the **built** stylesheet: a rule in the source and dropped by the build
// is a treatment that never happens, and the shipped bytes are the only artefact
// that can tell.
func borderStyleFor(built, selector string) string {
	for _, match := range ruleBody.FindAllStringSubmatch(stripped(built), -1) {
		if !strings.Contains(match[1], selector) {
			continue
		}

		declarations := match[2]
		at := strings.Index(declarations, "border-inline-start-style")

		if at < 0 {
			continue
		}

		return strings.TrimSpace(strings.TrimPrefix(
			strings.TrimSpace(declarations[at:]), "border-inline-start-style:"))
	}

	return ""
}

// --- The contrast machinery ------------------------------------------------------

// themeTokens is one `[data-theme]` block's declarations.
type themeTokens struct {
	name   string
	values map[string]string
}

// readThemes parses the theme blocks out of `tokens.css`.
//
// `tokPath` is the sheet; the parser is deliberately small and reads
// `[data-theme="…"] { … }` blocks and the declarations inside them. `tokens.css`
// declares nothing else that resolves at computed-value time.
func readThemes(t *testing.T, path string) []themeTokens {
	t.Helper()

	sheet := stripped(readFile(t, path))

	block := regexp.MustCompile(`\[data-theme="([a-z]+)"\]\s*\{`)

	var themes []themeTokens

	for _, at := range block.FindAllStringSubmatchIndex(sheet, -1) {
		name := sheet[at[2]:at[3]]

		open := at[1] - 1
		end := strings.Index(sheet[open:], "}")
		if end < 0 {
			continue
		}

		themes = append(themes, themeTokens{
			name:   name,
			values: parseDeclarations(sheet[open+1 : open+end]),
		})
	}

	return themes
}

// sheetDefaults is this sheet's own `:root` block, layered under every theme.
//
// **Because that is how the cascade resolves it.** `secret.css` declares one
// component property on `:root`, and a theme that does not mention it inherits it —
// so a contrast run looking only at the theme blocks would report the property as
// undeclared in both, which is a false finding rather than a careful one.
func sheetDefaults(t *testing.T) map[string]string {
	t.Helper()

	sheet := stripped(readFile(t, stylesheetPath))

	match := regexp.MustCompile(`:root\s*\{`).FindStringIndex(sheet)
	if match == nil {
		return map[string]string{}
	}

	open := match[1] - 1

	end := strings.Index(sheet[open:], "}")
	if end < 0 {
		return map[string]string{}
	}

	return parseDeclarations(sheet[open+1 : open+end])
}

// parseDeclarations reads `property: value;` pairs out of a block.
//
// **Both custom properties and ordinary declarations**, because the colour pairs this
// file measures are written the second way — `color: var(--text)` is a pair, and a
// parser that only understood `--name: value` would read this sheet as having no
// pairs in it at all, which is the failure mode of a scan that stopped parsing.
func parseDeclarations(block string) map[string]string {
	declaration := regexp.MustCompile(`([-a-zA-Z][-a-zA-Z0-9]*)\s*:\s*([^;]+);`)

	values := map[string]string{}

	for _, match := range declaration.FindAllStringSubmatch(block, -1) {
		values[match[1]] = strings.TrimSpace(match[2])
	}

	return values
}

// declaredTokenNames is every custom property any sheet in the token layer declares.
//
// **Both `tokens.css` and `shell.css`, and the reason is §6.1's own header**: the
// type steps, the space scale's multiplier, the radii and the font families are
// declared by the shell stylesheet, and tokens.css says so in a table at the top.
// A check against tokens.css alone would report every `--space-3` and `--radius-sm`
// this sheet reads as undeclared, which is a false finding rather than a careful one.
//
// A token declared in **only one** of them is still declared — the check here is
// "does it resolve", and `internal/web`'s `TestNoTokenIsMissingFromEitherTheme`
// owns "is it in both themes of tokens.css".
func declaredTokenNames(t *testing.T) map[string]struct{} {
	t.Helper()

	name := regexp.MustCompile(`^\s*(--[a-z0-9-]+)\s*:`)

	declared := map[string]struct{}{}

	for _, path := range []string{tokensPath, "../../static/css/shell.css", stylesheetPath} {
		for line := range strings.SplitSeq(stripped(readFile(t, path)), "\n") {
			if match := name.FindStringSubmatch(line); match != nil {
				declared[match[1]] = struct{}{}
			}
		}
	}

	if len(declared) == 0 {
		t.Fatal("no custom property was read out of the token layer; every " +
			"assertion below would be about nothing")
	}

	return declared
}

// tokensDeclaredInBothThemes returns the names every `[data-theme]` block declares.
//
// Both, not one: §6.1's rule is that a token present in only one theme is a bug,
// and the symptom is that it renders as the other theme's value rather than failing.
func tokensDeclaredInBothThemes(t *testing.T, path string) map[string]bool {
	t.Helper()

	themes := readThemes(t, path)
	if len(themes) == 0 {
		t.Fatal("tokens.css yielded no theme blocks; this assertion is about " +
			"nothing")
	}

	declared := map[string]bool{}

	for name := range themes[0].values {
		declared[name] = true
	}

	for _, theme := range themes[1:] {
		for name := range declared {
			if _, ok := theme.values[name]; !ok {
				declared[name] = false
			}
		}
	}

	return declared
}

// declaredPair is one foreground/background pair a rule creates.
type declaredPair struct {
	selector   string
	foreground string
	background string
	floor      float64
}

// pairsDeclaredIn reads every colour pair a stylesheet creates.
//
// **Read off the sheet**, for the reason the pair table's doc comment gives: a
// hand-written list is a list where a rule gets deleted and nobody notices.
//
// The background for a rule is the nearest preceding declaration of `background` or
// `background-color` in the same rule, falling back to the nearest preceding
// `background` in the sheet — which is how the cascade actually resolves a
// shorthand. A rule with no background at all is measured against `--bg`, which is
// what a browser resolves it to.
func pairsDeclaredIn(sheet string) []declaredPair {
	sheet = stripped(sheet)

	var pairs []declaredPair

	for _, match := range ruleBody.FindAllStringSubmatch(sheet, -1) {
		selector := collapse(match[1])
		declarations := parseDeclarations(match[2])

		background := ""

		for _, property := range []string{"background", "background-color"} {
			value, ok := declarations[property]
			if !ok {
				continue
			}

			if inner := colourReference.FindStringSubmatch(value); inner != nil {
				background = inner[1]
			}
		}

		if background == "" {
			background = "--bg"
		}

		for property, value := range declarations {
			// A `background` is the *ground*, so it is never the
			// foreground of the pair this rule declares. Measuring a
			// colour against itself reads 1.00:1 and reports a floor
			// every stylesheet fails, which is a finding with no content.
			if property == "background" || property == "background-color" {
				continue
			}

			floor, isColour := colourProperties[property]
			if !isColour {
				continue
			}

			inner := colourReference.FindStringSubmatch(value)
			if inner == nil {
				continue
			}

			pairs = append(pairs, declaredPair{
				selector:   selector,
				foreground: inner[1],
				background: background,
				floor:      floor,
			})
		}
	}

	// `@media (forced-colors: active)` is not a pair: it names system colours
	// that no token declares, and measuring them against a theme's palette
	// would assert a ratio the mode never computes.
	if at := strings.Index(sheet, "@media (forced-colors: active)"); at >= 0 {
		filtered := pairs[:0]

		for _, pair := range pairs {
			if !strings.Contains(pair.selector, "forced-colors") {
				filtered = append(filtered, pair)
			}
		}

		pairs = filtered
	}

	return pairs
}

// colourProperties are the declarations that can carry a colour.
//
// **An explicit list rather than a prefix test**, and the reason is
// `border-radius`. A prefix test for `border` reads `border-radius: var(--radius-sm)`
// as a colour pair, resolves `--radius-sm` to a length, and reports a contrast
// ratio between a colour and a length — which is not a finding, it is a parser that
// stopped discriminating. The list is the set of properties whose value the cascade
// paints with.
var colourProperties = map[string]float64{
	"color":                   textFloor,
	"background":              boundaryFloor,
	"background-color":        boundaryFloor,
	"border":                  boundaryFloor,
	"border-color":            boundaryFloor,
	"border-inline":           boundaryFloor,
	"border-inline-start":     boundaryFloor,
	"border-inline-end":       boundaryFloor,
	"border-block":            boundaryFloor,
	"border-block-start":      boundaryFloor,
	"border-block-end":        boundaryFloor,
	"border-top":              boundaryFloor,
	"border-right":            boundaryFloor,
	"border-bottom":           boundaryFloor,
	"border-left":             boundaryFloor,
	"outline":                 boundaryFloor,
	"outline-color":           boundaryFloor,
	"fill":                    boundaryFloor,
	"stroke":                  boundaryFloor,
	"box-shadow":              boundaryFloor,
	"border-inline-end-color": boundaryFloor,
	"border-block-end-color":  boundaryFloor,
}

// contrastRatio is WCAG 2.2's relative-luminance ratio.
//
// The 8-bit sRGB conversion with **no gamut clipping**, which is what
// `internal/web`'s gate does and for the same reason: the gate has to measure what
// a browser rasterises, and a clipped value would let a step no user sees satisfy a
// floor it satisfies on paper.
func contrastRatio(foreground, background string) float64 {
	first := relativeLuminance(foreground)
	second := relativeLuminance(background)

	lighter := math.Max(first, second)
	darker := math.Min(first, second)

	return (lighter + 0.05) / (darker + 0.05)
}

// relativeLuminance is WCAG 2.2's definition for one colour.
func relativeLuminance(colour string) float64 {
	digits := strings.TrimPrefix(colour, "#")

	if len(digits) == 3 {
		digits = strings.Repeat(strings.Repeat(string(digits[0]), 1), 1) +
			string(digits[0]) + string(digits[1]) + string(digits[1]) +
			string(digits[2]) + string(digits[2])
	}

	channels := make([]float64, 0, 3)

	for at := 0; at+1 < len(digits); at += 2 {
		value, err := strconv.ParseInt(digits[at:at+2], 16, 64)
		if err != nil {
			return 0
		}

		channels = append(channels, linearise(float64(value)/255))
	}

	if len(channels) != 3 {
		return 0
	}

	return 0.2126*channels[0] + 0.7152*channels[1] + 0.0722*channels[2]
}

// linearise is the sRGB electro-optical transfer function's inverse.
func linearise(value float64) float64 {
	if value <= 0.03928 {
		return value / 12.92
	}

	return math.Pow((value+0.055)/1.055, 2.4)
}

// TestTheWalkOverThisPackageFoundMarkup asserts the walk above found markup to
// walk, so a renamed file cannot make the selector checks pass on nothing.
func TestTheWalkOverThisPackageFoundMarkup(t *testing.T) {
	t.Parallel()

	matches, err := filepath.Glob(sourceTree + "/*.templ")
	if err != nil {
		t.Fatalf("glob this package's templates: %v", err)
	}

	if len(matches) == 0 {
		t.Fatal("this package renders no .templ file, so the selector checks " +
			"above are being asked about a component that does not exist")
	}
}
