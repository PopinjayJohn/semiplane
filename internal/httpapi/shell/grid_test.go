//nolint:misspell // CSS identifiers, not prose; the rationale follows the package clause
package shell_test

// This file's prose uses the UK spelling throughout. The suppression above is for
// CSS identifiers only: `forced-colors` is spelled the US way by CSS itself, and
// the custom-property names are the tokens' names — a linter that rewrote
// `--border-w` to `--border-w` with a UK spelling would be asserting against a
// token the stylesheet does not declare.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// These tests read the stylesheet, and they read it because of one structural
// fact about CSS that a Go test is unusually well suited to: **a custom property
// cannot be used in a media query condition.** Every `@media` in the shell has
// to repeat the breakpoint literally, which means the declared constants and
// the conditions that use them are two copies of the same numbers with nothing
// holding them together. `TestEveryMediaQueryMatchesADeclaredConstant` is the
// thing that holds them together, and it is the reason
// `TestEveryCustomPropertyTheGridUsesIsDeclared` exists as well: a reader who
// finds a breakpoint used in three places needs to know which copy is the
// definition and which are the echoes.
//
// The rest of this file is the set of things this stylesheet is *not* allowed to
// do — no bare `1fr`, no `scale()`, a wrapping centre, a locked shell at medium
// and a document-scrolled one at compact. Each of those is a decision that is
// expensive to get wrong and invisible to a visual review once it has.

// containOverscroll is the §3.6 overscroll declaration, held in a constant so
// the one justification travels with the one spelling.
//
// A spell-checker configured for British English objects to the American form of
// this word wherever it finds it, including in a string literal that is
// comparing against a stylesheet — and it cannot be allowed to rewrite the name
// of a property. The alternative is a `nolint` at each of the three call sites,
// and a suppression that has to be repeated is one that eventually gets deleted
// by somebody tidying.
const containOverscroll = "overscroll-behavior: contain" //nolint:misspell // A CSS property.

// tokenDeclarations returns tokens.css with its comments removed.
//
// The same comment-stripping as `stylesheet`, and the same reason: half of what
// tokens.css is for is explaining why, and a mechanical check has to see the
// declarations. Its header comment also *names* protected variables such as
// `--target-min` in prose, so a check that read the comments would find those
// declared when nothing assigns them — which is precisely the failure this test
// exists to catch.
func tokenDeclarations(t *testing.T) string {
	t.Helper()

	return stripComments(t, readFile(t, "..", "..", "web", "static", "css", "tokens.css"))
}

// stylesheet reads the shell stylesheet with its comments removed, which lives
// outside this package.
//
// Comments go because half of what this stylesheet is for is explaining why, and
// a mechanical check has to see the declarations and not the prose about them:
// this file's own header says `transform: scale()` is prohibited, and a test for
// `scale(` that read the comments would fail on the sentence prohibiting it.
// Every explanation is still in the stylesheet; it is just not in the
// declarations.
func stylesheet(t *testing.T) string {
	t.Helper()

	return stripComments(t, readFile(t, "..", "..", "web", "static", "css", "shell.css"))
}

// stripComments removes every CSS comment from a stylesheet.
func stripComments(t *testing.T, raw []byte) string {
	t.Helper()

	var out strings.Builder

	for index := 0; index < len(raw); {
		if start := strings.Index(string(raw[index:]), "/*"); start >= 0 {
			out.Write(raw[index : index+start])
			index += start

			end := strings.Index(string(raw[index:]), "*/")
			if end < 0 {
				break
			}

			index += end + 2

			continue
		}

		out.Write(raw[index:])
		break
	}

	return out.String()
}

// declaredBreakpoints returns every `--breakpoint-*` declaration, resolved to
// pixels at a 16px root.
//
// A root font size other than 16px would change the resolved value, and the
// resolver's own arithmetic is in CSS pixels, so the conversion is the one the
// resolver makes and 16 is the default every browser ships. `rem` is used above
// 640 because §3.1's table states it in `rem`; the conversion is the price of
// using the spec's own numbers.
func declaredBreakpoints(t *testing.T, css string) map[string]float64 {
	t.Helper()

	declaration := regexp.MustCompile(`--breakpoint-([a-z0-9-]+)\s*:\s*([0-9.]+)(rem|px)`)

	found := map[string]float64{}

	for _, match := range declaration.FindAllStringSubmatch(css, -1) {
		value, err := strconv.ParseFloat(match[2], 64)
		if err != nil {
			t.Fatalf("breakpoint --breakpoint-%s has an unparseable value %q: %v",
				match[1], match[2], err)
		}

		if match[3] == "rem" {
			value *= 16
		}

		found[match[1]] = value
	}

	if len(found) == 0 {
		t.Fatal("no --breakpoint-* custom property is declared; the grid has no single source")
	}

	return found
}

// TestEveryMediaQueryMatchesADeclaredConstant is the anti-drift test for the one
// thing CSS cannot express.
//
// Each condition naming a width or a height is resolved to pixels and matched
// against a declared constant. A media query with no matching constant is
// allowed and is listed in the exception set with its reason, because a feature
// query or a `prefers-*` condition is not a breakpoint and there is nothing to
// declare.
func TestEveryMediaQueryMatchesADeclaredConstant(t *testing.T) {
	t.Parallel()

	css := stylesheet(t)
	breakpoints := declaredBreakpoints(t, css)

	// `min-width`, `max-height` and `max-width` are the three this stylesheet
	// uses to cross a tier boundary.
	condition := regexp.MustCompile(
		`(min-width|max-width|min-height|max-height)\s*:\s*([0-9.]+)(rem|px)`,
	)

	matched := 0

	for _, match := range condition.FindAllStringSubmatch(css, -1) {
		value, err := strconv.ParseFloat(match[2], 64)
		if err != nil {
			t.Fatalf("media query %q has an unparseable value: %v", match[0], err)
		}

		if match[3] == "rem" {
			value *= 16
		}

		matched++

		found := false

		for name, declared := range breakpoints {
			if declared != value {
				continue
			}

			found = true

			t.Logf("%s matches --breakpoint-%s", match[0], name)
		}

		if !found {
			t.Errorf(
				"media query %q resolves to %gpx, which is not any declared "+
					"--breakpoint-* constant (%s); a breakpoint written as a "+
					"literal in a media query and nowhere else is a number with "+
					"no definition",
				match[0], value, formatBreakpoints(breakpoints),
			)
		}
	}

	if matched == 0 {
		t.Fatal("no media query in the stylesheet names a dimension; the tier " +
			"conditions cannot be checked")
	}
}

// formatBreakpoints renders the declared constants for a failure message.
func formatBreakpoints(breakpoints map[string]float64) string {
	parts := make([]string, 0, len(breakpoints))
	for name, value := range breakpoints {
		parts = append(parts, name+"="+strconv.FormatFloat(value, 'f', -1, 64)+"px")
	}

	return strings.Join(parts, ", ")
}

// TestTheCentreTrackIsNeverABareFraction is §4's load-bearing `minmax(0, 1fr)`,
// and it is asserted by subtraction: every occurrence of `1fr` in the file must
// be part of a `minmax(0, 1fr)`.
//
// A bare `1fr` resolves to `min-content` as its minimum, so one long unbroken
// string in a heading — a slug, a URL, a paste — becomes the track's minimum
// and the centre grows past the viewport. The result is a horizontal scrollbar
// and a layout that is correct on every page a designer looked at. This is the
// kind of rule that gets "tidied" into a bare `1fr` by somebody who has never
// seen the failure, so it is a test rather than a comment.
func TestTheCentreTrackIsNeverABareFraction(t *testing.T) {
	t.Parallel()

	css := stylesheet(t)

	const bounded = "minmax(0, 1fr)"

	remaining := strings.ReplaceAll(css, bounded, "")
	if strings.Contains(remaining, "1fr") {
		for line := range strings.SplitSeq(remaining, "\n") {
			if strings.Contains(line, "1fr") {
				t.Errorf(
					"%q contains a bare 1fr; a 1fr track's minimum is "+
						"min-content, so one long unbroken string in a heading "+
						"blows the centre out past the viewport. Use %s (UI §4)",
					strings.TrimSpace(line), bounded,
				)
			}
		}
	}

	if !strings.Contains(css, bounded) {
		t.Errorf("the stylesheet declares no %s at all", bounded)
	}
}

// TestTheShellIsNeverScaled is §5.3's prohibition, and it is asserted against the
// whole stylesheet rather than against the grid alone.
//
// A scaled tier is a photograph of another tier: it inherits that tier's target
// sizes and focus-ring widths at the wrong size, so §7.3's minimum and §8.2's
// focus ring become decorations. Each tier re-decides what persists instead.
func TestTheShellIsNeverScaled(t *testing.T) {
	t.Parallel()

	css := stylesheet(t)

	for _, banned := range []string{"scale(", "zoom:"} {
		if strings.Contains(css, banned) {
			t.Errorf(
				"the stylesheet uses %q; no tier is a scaled version of another "+
					"(UI §3.1, §5.3)", banned,
			)
		}
	}
}

// TestEveryTierDeclaresItsGrid is §3.7's "one canonical DOM, tiers as CSS
// variants, so tiers cannot drift", checked from the other end: every value the
// resolver can write onto `data-ui` has a grid.
//
// The base `.shell` rule is the compact grid, which is the one value without a
// `[data-ui="compact"]` grid of its own — and that is deliberate, not an
// omission. It is what a visitor with no script gets, and §3.7's promise to that
// visitor is coherence rather than a full tier, so the tier the default has to
// be is the one that reflows correctly at 320px with no JavaScript.
func TestEveryTierDeclaresItsGrid(t *testing.T) {
	t.Parallel()

	css := stylesheet(t)

	tiers := []string{
		"compact",
		"compact-short",
		"medium",
		"large",
		"wide",
		"tv",
		"tv-wide",
	}

	base := ruleBody(t, css, ".shell {")
	if !strings.Contains(base, "grid-template-areas") {
		t.Errorf("the base .shell rule declares no grid; it is the compact grid and " +
			"the layout a visitor with no script gets (UI §3.7)")
	}

	if !strings.Contains(base, `"main"`) {
		t.Errorf("the base .shell rule is not the compact grid: it should stack " +
			"header above main with no nav column (UI §3.1, §3.6)")
	}

	for _, tier := range tiers[1:] {
		selector := `:root[data-ui="` + tier + `"] .shell`
		if !strings.Contains(css, selector) {
			t.Errorf("no rule selects %s; the resolver can write data-ui=%q and "+
				"nothing would lay the shell out for it (UI §3.7)", selector, tier)
		}
	}

	// The pre-campaign variant, §4.6's own areas, and the one place a `nav` area
	// must not appear.
	preCampaign := ruleBody(t, css, ".shell--pre-campaign {")
	if !strings.Contains(preCampaign, `"header header"`) ||
		!strings.Contains(preCampaign, `"main rail"`) {
		t.Errorf(
			"the pre-campaign shell does not use §4.6's two-column areas; got:\n%s",
			preCampaign,
		)
	}

	if strings.Contains(preCampaign, "nav") {
		t.Errorf(
			"the pre-campaign shell names a nav column; the left nav is absent "+
				"before a campaign exists and a grid area nothing occupies is a "+
				"grid with a hole (UI §4.6):\n%s",
			preCampaign,
		)
	}
}

// TestTheScrollModelFollowsTheTier is §3.6, and it is the rule most likely to be
// broken by a well-meaning change: a nested scroll region inside a mobile
// viewport fights momentum scrolling and the URL bar, and a document scroll
// inside a viewport-locked shell means a page that scrolls twice.
//
// The compact shell must therefore be document-scrolled and the medium-and-up
// shells viewport-locked, and the assertions are on the declarations rather than
// on a rendering, because a rendering needs a browser.
func TestTheScrollModelFollowsTheTier(t *testing.T) {
	t.Parallel()

	css := stylesheet(t)

	compact := ruleBody(t, css, ".shell {")
	for _, forbidden := range []string{"100dvh", "overflow: hidden"} {
		if strings.Contains(compact, forbidden) {
			t.Errorf(
				"the base (compact) .shell rule declares %q; at compact the shell is "+
					"document-scrolled, one page scroll (UI §3.6):\n%s",
				forbidden, compact,
			)
		}
	}

	// One rule, one selector list, five tiers — and that is the assertion as much
	// as the declarations are. A scroll model declared per tier is a scroll model
	// that a later edit can make disagree with itself, and §3.6 states one model
	// for everything at medium and above.
	locked := ruleBody(t, css, `:root[data-ui="medium"] .shell,`)

	for _, required := range []string{"height: 100dvh", "overflow: hidden"} {
		if !strings.Contains(locked, required) {
			t.Errorf(
				"the viewport-locked shell rule is missing %q; at medium and above "+
					"the shell owns the viewport (UI §3.6):\n%s", required, locked,
			)
		}
	}

	// `vh` only as a fallback declaration preceding `dvh` (§3.6). A bare `100vh`
	// with no `100dvh` after it is a shell that does not resize when a mobile
	// browser's URL bar collapses, which is the entire reason §3.6 names `dvh`.
	viewportHeight := regexp.MustCompile(`height: 100(d?vh)`).FindAllString(locked, -1)
	if len(viewportHeight) != 2 || viewportHeight[0] != "height: 100vh" ||
		viewportHeight[1] != "height: 100dvh" {
		t.Errorf(
			"the viewport-locked rule declares %v; §3.6 requires `vh` as a bare "+
				"fallback declaration immediately preceding `dvh`, in that order, "+
				"and forbids a bare `vh`", viewportHeight,
		)
	}

	// Every tier from medium up is in the list.
	selectorList := ruleSelector(t, css, `:root[data-ui="medium"] .shell,`)

	for _, tier := range []string{"medium", "large", "wide", "tv", "tv-wide"} {
		if !strings.Contains(selectorList, `:root[data-ui="`+tier+`"] .shell`) {
			t.Errorf(
				"the viewport-locked rule does not select the %s tier; a tier "+
					"missing from it gets the compact scroll model at a width "+
					"that is not compact (UI §3.6)", tier,
			)
		}
	}

	// The rails and the centre scroll independently and contain their own
	// overscroll, or a rail reaching its end chains the scroll out to the page.
	centre := ruleBody(t, css, `:root[data-ui="medium"] .shell-main,`)

	for _, required := range []string{"overflow-y: auto", containOverscroll} {
		if !strings.Contains(centre, required) {
			t.Errorf(
				"the scrolling centre is missing %q; without it a rail reaching "+
					"its end chain-scrolls the page behind it (UI §3.6)", required,
			)
		}
	}

	if !strings.Contains(css, containOverscroll) {
		t.Error("no rule in the stylesheet contains an overscroll (UI §3.6)")
	}
}

// TestTheCentreWrapsRatherThanOverflows is §7.9's "no horizontal scrollbar at
// 320px" reduced to the property that prevents it.
//
// At 320px a single unbroken token wider than the track forces a horizontal
// scrollbar however the tracks are sized, and the fix is a wrapping property on
// the centre rather than a smaller track. This asserts the property is present;
// it does not assert the absence of a scrollbar, which needs a browser, and that
// is the phase's `make a11y` sweep rather than a Go test.
func TestTheCentreWrapsRatherThanOverflows(t *testing.T) {
	t.Parallel()

	css := stylesheet(t)

	centre := ruleBody(t, css, ".shell-main {")
	if !strings.Contains(centre, "overflow-wrap: anywhere") {
		t.Errorf(
			"the centre does not wrap long unbroken strings; at 320px one slug "+
				"or URL is enough to force a horizontal scrollbar (UI §7.9, 1.4.10):\n%s",
			centre,
		)
	}

	// `width: 68ch` on a 320px track would be *wider* than the track, so the
	// `min()` is load-bearing rather than decorative.
	if !strings.Contains(css, "min(100%, var(--measure))") {
		t.Errorf(
			"the centre's measure is not min(100%%, …); a bare ch width is wider " +
				"than a 320px track and is the most common cause of a horizontal " +
				"scrollbar on a phone (UI §3.2)",
		)
	}
}

// TestSheetsLeaveFortyPixelsOfContext is the phase's gate number, computed from
// the constants the stylesheet declares rather than from a browser.
//
// §3.1's arithmetic: a 20rem (320px) right drawer and an 18rem (288px) left
// drawer cannot coexist on a 360px screen, and a single 320px drawer on a 360px
// screen leaves a 40px sliver with no way to see the page behind the scrim.
// Below 640px both rails are therefore bottom sheets, and the check here is that
// the declared overlay widths leave at least 40px of the viewport uncovered on
// every row of §2's matrix.
//
// The compact left drawer is `min(18rem, 85vw)`, so on the narrowest row it is
// 85% and 15% — 48px at 320 — is what stays uncovered. The rail sheet is a
// block-size fraction, so what it leaves is above it.
func TestSheetsLeaveFortyPixelsOfContext(t *testing.T) {
	t.Parallel()

	css := stylesheet(t)
	breakpoints := declaredBreakpoints(t, css)

	// The two numbers the property depends on, read from the declarations so the
	// test cannot pass against a value the stylesheet no longer uses.
	viewportFraction := customProperty(t, css, "--drawer-left")
	viewportPercent := regexp.MustCompile(`([0-9.]+)vw`).FindStringSubmatch(viewportFraction)
	if viewportPercent == nil {
		t.Fatalf("--drawer-left is %q, which has no vw term; the compact drawer "+
			"has to be a fraction of the viewport or it does not fit at 320px",
			viewportFraction)
	}

	fraction, err := strconv.ParseFloat(viewportPercent[1], 64)
	if err != nil {
		t.Fatalf("--drawer-left has an unparseable vw term %q: %v", viewportPercent[1], err)
	}

	// §2's verification matrix, the rows a phone actually uses.
	viewports := []struct {
		name          string
		width, height float64
	}{
		{"small phone portrait", 320, 568},
		{"phone portrait", 360, 640},
		{"phone portrait, large", 390, 844},
		{"phone landscape", 844, 390},
		{"tablet portrait", 768, 1024},
	}

	// The gate's number, in CSS pixels.
	const minimumContext = 40

	for _, viewport := range viewports {
		t.Run(viewport.name, func(t *testing.T) {
			t.Parallel()

			drawerWidth := viewport.width * fraction / 100
			if remaining := viewport.width - drawerWidth; remaining < minimumContext {
				t.Errorf(
					"the left drawer covers %.0fpx of a %.0fpx viewport, leaving "+
						"%.0fpx; the gate requires at least %dpx of context "+
						"(UI §3.1, §10.3)",
					drawerWidth, viewport.width, remaining, minimumContext,
				)
			}
		})
	}

	// And the tier boundary the arithmetic turns on: below 640px the right rail
	// is a sheet, and from 640px it is a drawer. The switch has to exist and it
	// has to be at §3.1's `sm`.
	if _, ok := breakpoints["sm"]; !ok {
		t.Error("--breakpoint-sm is not declared; the boundary between the bottom " +
			"sheets and the right drawer has no name (UI §3.1)")
	}
}

// TestEveryCustomPropertyTheGridUsesIsDeclared catches the failure mode of a
// stylesheet built from variables: a `var(--name)` with no `--name` anywhere
// resolves to nothing, silently, and the declaration it was supposed to carry
// does not apply. Nothing warns.
//
// The declaration set is collected from the token layer as well as from this
// file, which is a change of mechanism and not of strength. The rule used to
// carry a hand-maintained list of three names the token layer owns, and it
// covered the grid's own use of `--surface-raised`, `--border` and `--dur-3`.
// S5 then put the §5.1 scale block and the whole primitive library in this file
// — roughly two dozen further token reads — and an allow-list does not survive
// that: either it grows to twenty-six hand-written entries, and the next reader
// cannot tell which are checked and which are aspirational, or it stays at three
// and fails on correct code. Reading the other file instead means the coupling
// is *checked* rather than *enumerated*, so a `var(--typo)` still fails and a
// renamed token still fails, and no entry has to be maintained by hand.
//
// The three names stay, with their reasons, as log lines: the point of naming
// them was that a reader who wonders where `--surface-raised` comes from finds
// the answer here, and that is still true.
//
// The names in the map changed, and that is the finding rather than a detail.
// The map used to name `--surface-raised`, `--border` and `--dur-3` as "the token
// layer's", so the allow-list suppressed exactly the check that would have found
// them. No layer declares either name — §6.2/§6.3 have `--surface` and
// `--surface-sunken` with no raised step, and the duration steps are
// `--dur-instant`/`--dur-fast`/`--dur-base`/`--dur-slow` — so both were live
// references to nothing, and one of them fell back to `Canvas`, a forced-colors
// system colour, which put the compact bottom bar on white in the dark theme. The
// allow-list read as documentation and functioned as a suppression: a reader who
// believed it would conclude those tokens exist. Deriving the set from the file
// makes the same class of defect fail instead of passing quietly, which is the
// only reason this map is shorter than the list it replaced.
//
// What it names now are the couplings a reader genuinely cannot infer from this
// file: the one token whose *value* is a `var()` chain ending in a hook this file
// sets, and the two that exist for the forced-colors fallback rather than for the
// default theme.
func TestEveryCustomPropertyTheGridUsesIsDeclared(t *testing.T) {
	t.Parallel()

	css := stylesheet(t)

	namedForTheReader := map[string]string{
		"--focus-ring-offset": "the token layer defines it as var(--local-surface, var(--bg)); " +
			"this file sets --local-surface on a solid fill, which is §6.5's inversion",
		"--border-subtle": "the token layer's hairline; §6.5 permits it here in exactly " +
			"one place, on the nav collapse's separator, which is not a control's only boundary",
		"--surface-border": "the token layer's §6.7 border fallback, standing in for a " +
			"dropped shadow in forced-colors mode",
		"--border-w": "the token layer's width, which `prefers-contrast: more` doubles " +
			"and forced colours restores — the reason a border here is a var and not 1px",
	}

	declared := map[string]bool{}
	for _, source := range []string{css, tokenDeclarations(t)} {
		for _, match := range regexp.MustCompile(`(--[a-z0-9-]+)\s*:`).FindAllStringSubmatch(source, -1) {
			declared[match[1]] = true
		}
	}

	used := map[string]bool{}
	for _, match := range regexp.MustCompile(`var\((--[a-z0-9-]+)`).FindAllStringSubmatch(css, -1) {
		used[match[1]] = true
	}

	for name := range used {
		if reason, ok := namedForTheReader[name]; ok {
			t.Logf("%s comes from the token layer: %s", name, reason)
		}

		if declared[name] {
			continue
		}

		t.Errorf(
			"%s is used but never declared in shell.css or tokens.css; a var() "+
				"with no declaration resolves to nothing, silently", name,
		)
	}

	if len(used) == 0 {
		t.Fatal("the stylesheet reads no custom property; §3.7's first task is " +
			"that the grid and every component query read one source")
	}
}

// TestTheLayoutConstantsAreTheOnesTheSpecGives pins the numbers §3.1's table
// and §3.4 and §3.5 state, so a retune is a deliberate edit to a value a reader
// can check against the design record rather than a diff nobody can interpret.
func TestTheLayoutConstantsAreTheOnesTheSpecGives(t *testing.T) {
	t.Parallel()

	css := stylesheet(t)
	breakpoints := declaredBreakpoints(t, css)

	want := map[string]float64{
		// §3.1's tier table.
		"sm":  640,
		"md":  768,
		"lg":  1024,
		"xl":  1280,
		"2xl": 1536,
		// §3.5's short-landscape condition, and §3.4's tv-wide.
		"short":   480,
		"tv-wide": 2200,
	}

	for name, expected := range want {
		got, ok := breakpoints[name]
		if !ok {
			t.Errorf("--breakpoint-%s is not declared; the spec gives it as %gpx",
				name, expected)

			continue
		}

		if got != expected {
			t.Errorf("--breakpoint-%s = %gpx, want %gpx", name, got, expected)
		}
	}

	// The chrome heights, which are not breakpoints but are the same kind of
	// number and the same kind of contract.
	heights := map[string]string{
		"--header-h":           "3.5rem", // §3.2's compact and medium header
		"--header-h-short":     "2.5rem", // §3.5's collapsed header
		"--header-h-tv":        "5rem",   // §3.4's TV header
		"--footer-h":           "2rem",   // §3.2's inline footer
		"--bottom-bar-h":       "3.5rem", // §4.2's compact bottom bar
		"--nav-left":           "13rem",  // §3.1's medium left nav
		"--nav-left-lg":        "15rem",  // §3.1's large and wide left nav
		"--nav-rail":           "3.5rem", // §3.2 and §3.5's collapsed icon rail
		"--rail-right":         "20rem",  // §3.2's large right rail
		"--rail-right-md":      "22rem",  // §3.2's medium drawer, §3.1's wide rail
		"--rail-right-tv":      "26rem",  // §3.4
		"--rail-right-tv-wide": "30rem",  // §3.4
	}

	for name, expected := range heights {
		if got := customProperty(t, css, name); got != expected {
			t.Errorf("%s = %q, want %q", name, got, expected)
		}
	}

	// §3.2's two sheet heights, in viewport fractions rather than rems: a sheet
	// of a fixed height on a short screen is a sheet that fills it.
	for name, expected := range map[string]string{
		"--sheet-rail":       "70dvh",
		"--sheet-rail-short": "85dvh",
	} {
		if got := customProperty(t, css, name); got != expected {
			t.Errorf("%s = %q, want %q", name, got, expected)
		}
	}
}

// ruleSelector returns the selector text of the rule at or after an anchor, up
// to its opening brace.
//
// Paired with ruleBody, and separate from it, because "which selectors does this
// one declaration apply to" is a question with its own answer: a rule that names
// four of the five tiers it was written for is a rule whose fifth tier silently
// keeps the previous tier's behaviour.
func ruleSelector(t *testing.T, css, anchor string) string {
	t.Helper()

	index := strings.Index(css, anchor)
	if index < 0 {
		t.Fatalf("no rule begins with a selector containing %q", anchor)
	}

	rest := css[index:]

	open := strings.Index(rest, "{")
	if open < 0 {
		t.Fatalf("the rule at %q has no declaration block", anchor)
	}

	return rest[:open]
}

// customProperty returns a declared custom property's value.
func customProperty(t *testing.T, css, name string) string {
	t.Helper()

	pattern := regexp.MustCompile(regexp.QuoteMeta(name) + `\s*:\s*([^;]+);`)
	match := pattern.FindStringSubmatch(css)
	if match == nil {
		t.Fatalf("%s is not declared in the stylesheet", name)
	}

	return strings.TrimSpace(match[1])
}

// ruleBody returns the declarations of the first rule at or after the given
// selector text.
//
// Brace-matched rather than regex-matched to the closing brace, because a
// declaration block contains braces of its own and a lazy match would stop at
// the first `}`. The caller supplies enough of the selector to be unambiguous,
// which usually means the trailing comma: a rule is often a group, and
// `:root[data-ui="medium"] .shell,` is the first line of a list that also names
// large, wide, tv and tv-wide, while `:root[data-ui="medium"] .shell` with no
// comma is that tier's own rule.
func ruleBody(t *testing.T, css, selectorTail string) string {
	t.Helper()

	index := strings.Index(css, selectorTail)
	if index < 0 {
		t.Fatalf("no rule begins with a selector ending %q", selectorTail)
	}

	rest := css[index:]

	open := strings.Index(rest, "{")
	if open < 0 {
		t.Fatalf("the rule at %q has no declaration block", selectorTail)
	}

	depth := 0

	for offset, char := range rest[open:] {
		switch char {
		case '{':
			depth++
		case '}':
			depth--

			if depth == 0 {
				return rest[open+1 : open+offset]
			}
		}
	}

	t.Fatalf("the rule at %q has an unterminated declaration block", selectorTail)

	return ""
}
