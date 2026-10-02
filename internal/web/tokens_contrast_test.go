// The §10.1 contrast gate: parse internal/web/static/css/tokens.css and assert
// every colour pair the design record claims, against the floor it claims, in
// both themes, and again with a campaign's brand override applied.
//
// # WHY THIS FILE PARSES THE STYLESHEET RATHER THAN CARRYING A PALETTE
//
// The failure this gate exists to prevent is a typo in a hex value shipping an
// AA failure. A test that held its own copy of the palette could not detect that
// typo — it would compare the file against itself, from two places that agree,
// and pass. So every value asserted here is read out of tokens.css at test time,
// and the only colour literals in this file are the one fixture brand pair,
// which is the campaign-supplied value under test in its own right. The floors
// come from WCAG, not from the palette, and are the one thing here that is
// correct to hard-code.
//
// WHAT "MEASURING WHAT THE STYLESHEET SAYS" MEANS HERE
//
// The primitive layer is oklch and the semantic layer is hex, so both are
// converted to 8-bit sRGB and measured with one formula: WCAG 2.2's
// relative-luminance definition. The conversion is not a convenience. The gate
// has to measure the colour a browser rasterises, and a browser clips an
// out-of-gamut oklch to sRGB before anything is painted — so measuring the
// unclipped value would let a ramp step that no user ever sees satisfy a floor
// it only satisfies on paper. TestPrimitiveRampsAreInGamut fails the build
// instead.
//
// The record's own ratios are deliberately NOT asserted. §6.2 and §6.3 round
// three of theirs in the second decimal: light --callout-border claims 3.24
// against a computed 3.21, --callout-secret 8.30 against 8.22, and --warning
// 5.49 against 5.48. All three clear their floors, and pinning the rounded
// figure would fail on correct values while training the next reader to edit
// the test rather than the stylesheet. The floors are the contract; the ratios
// are a record of how the values were arrived at.
//
//nolint:misspell // CSS identifiers, not prose; the rationale follows the package clause
package web_test

// This file's prose uses the UK spelling throughout. The misspell suppression on
// the package clause above is for CSS identifiers only: `forced-colors`,
// `forced-color-adjust`, the `color()` function and the `--color-*` theme
// namespace are the spellings the CSS specifications define, and rewriting any of
// them would break the thing they name — a media query written `forced-colours`
// matches nothing, and a theme namespace spelled any other way generates no
// utilities at all.

import (
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// tokenFile is the stylesheet under test, relative to this package. Read from
// disk rather than through the embedded dist tree: the built stylesheet is
// minified and Tailwind prunes what no utility references, so the tokens that
// reach no utility are absent from it, and a gate that could only see the built
// output would be blind to exactly the layer §6.1 calls primitive.
const tokenFile = "static/css/tokens.css"

// maxVarDepth bounds var() chasing. A cycle in the token graph would otherwise
// be a stack overflow inside the test rather than a diagnosable failure; §6.1's
// tokens nest two deep at most (--surface-border -> --border-subtle).
const maxVarDepth = 16

// ratioEpsilon absorbs the last bit of float error so a value sitting exactly on
// its floor passes. It is 1e-6, six orders of magnitude below the second decimal
// the record reports, so it cannot mask a real shortfall.
const ratioEpsilon = 1e-6

// oklchHueUnit is the number of degrees in one full turn of a hue angle.
const oklchHueUnit = 360

// themeName is a §6.1 theme block: one of the two values [data-theme] can take.
type themeName string

// The two theme blocks §6.2 and §6.3 define. themeNames returns them rather than
// being a package-level slice, so nothing in this file holds state between
// tests.
const (
	themeLight themeName = "light"
	themeDark  themeName = "dark"
)

// themeNames is the fixed set of theme blocks, in the order §6.2 and §6.3
// present them. Every assertion iterates it, so a third theme is a change to
// this function and the gate follows.
func themeNames() []themeName {
	return []themeName{themeLight, themeDark}
}

// tokenClass is what §6.1 and §6.5 make a token, and it decides which assertion
// the token is subject to. The classification is the part of this gate that no
// ratio can express: a colour can clear every floor and still be the wrong kind
// of token, and §6.5's "decorative only" is exactly that case.
type tokenClass int

const (
	// classText is a token that may carry text. It may only be a contrast
	// pair's foreground, and it must appear as one.
	classText tokenClass = iota
	// classBoundary draws an edge or a shape: a border, a focus ring, a fill's
	// own outline. §6.5's 1.4.11 territory.
	classBoundary
	// classSurface fills area. It may only be a pair's background, and it must
	// appear as one.
	classSurface
	// classDecorative is §6.2's "decorative only" floor and §6.5's tokens that
	// may not be relied on. It is never in a pair: relying on it as a
	// control's only boundary is a 1.4.11 failure that no ratio can catch, so
	// the classification is asserted instead.
	classDecorative
	// classTranslucent carries alpha below 1. What it measures is a function of
	// whatever is behind it, so the token layer forbids it in any
	// contrast-bearing slot.
	classTranslucent
	// classNonColour is a length, a duration or a keyword, and the contrast
	// arithmetic does not apply to it.
	classNonColour
)

// String names a class for a failure message. "token --border-subtle" and
// "decorative token --border-subtle" point at different fixes.
func (c tokenClass) String() string {
	switch c {
	case classText:
		return "text"
	case classBoundary:
		return "boundary"
	case classSurface:
		return "surface"
	case classDecorative:
		return "decorative"
	case classTranslucent:
		return "translucent"
	case classNonColour:
		return "non-colour"
	default:
		return "unclassified"
	}
}

// tokenClasses is the complete expected set of layer-2 and layer-3 tokens, with
// the class each one carries. It is the expected value of the stylesheet's own
// key set, and it exists so the gate fails in both directions: a token added to
// tokens.css with no row here fails as unclassified, and a row here with no
// declaration in a theme block fails as missing. A palette that can grow
// without a classification is a palette whose floor nobody chose.
//
// Every name is either transcribed from §6.2/§6.3 or required by another clause.
// There are six additions to the record's two tables and each cites the clause
// that requires it; §6.1's own example names two of them.
func tokenClasses() map[string]tokenClass {
	return map[string]tokenClass{
		// §6.2 and §6.3, transcribed.
		"--bg":               classSurface,
		"--surface":          classSurface,
		"--surface-sunken":   classSurface,
		"--text":             classText,
		"--text-muted":       classText,
		"--text-subtle":      classText,
		"--border-subtle":    classDecorative,
		"--border":           classBoundary,
		"--accent":           classText,
		"--accent-solid":     classBoundary,
		"--on-accent":        classText,
		"--accent-surface":   classSurface,
		"--callout-surface":  classSurface,
		"--callout-border":   classBoundary,
		"--callout-secret":   classText,
		"--callout-revealed": classText,
		"--success":          classText,
		"--warning":          classText,
		"--danger":           classText,
		"--focus-ring":       classBoundary,
		"--brand-accent":     classBoundary,
		"--brand-accent-ink": classText,
		"--map-dim":          classTranslucent,
		"--btn-primary-bg":   classSurface,
		"--btn-primary-fg":   classText,
		"--tab-selected-bg":  classSurface,
		"--tab-selected-fg":  classText,

		// --focus-ring-offset is §6.5's second half of the two-tone ring. The
		// record names the token in prose and gives it no row, and it is a
		// surface: it is what the ring is measured against, never what is
		// measured.
		"--focus-ring-offset": classSurface,

		// §6.7 makes --border thicken, so the width needs a token. 1.4.11 is
		// about the width of a boundary as well as its colour, and a media
		// query cannot thicken a border that was written as a literal.
		"--border-w": classNonColour,

		// --map-outline is what §6.2 and §6.3's --map-dim row delegates its 3:1
		// to, and what §7.1 names as carrying 1.1.1 for the map. The record
		// names it twice and defines it nowhere.
		"--map-outline": classBoundary,

		// These two make §6.7's first rule a token. forced-colors: active drops
		// shadows, so every elevation has to keep its separation as a border;
		// naming the hairline means a component gets the 1px ButtonBorder
		// fallback by using the token it already needed, instead of writing a
		// second rule in each of the places that would need it.
		"--surface-border":        classDecorative,
		"--surface-sunken-border": classDecorative,
	}
}

// floor is a minimum contrast ratio and the success criterion that demands it.
// Carrying the clause is what makes a failure actionable: "4.42 < 4.5" does not
// say which requirement is broken, and the difference between 1.4.3 and 1.4.11
// is the difference between changing a text colour and changing a border.
type floor struct {
	// ratio is the minimum, from WCAG 2.2.
	ratio float64
	// clause is the success criterion, as AGENTS.md and the record cite it.
	clause string
	// label names the kind of content, for the failure message.
	label string
}

// textFloor is 1.4.3: body and control text.
func textFloor() floor {
	return floor{ratio: 4.5, clause: "1.4.3", label: "text"}
}

// nonTextFloor is 1.4.11: component boundaries, focus rings, selected states —
// and, per §6.4, the shape of a solid fill against its own page.
func nonTextFloor() floor {
	return floor{ratio: 3.0, clause: "1.4.11", label: "non-text"}
}

// graphicFloor is 1.1.1: graphics and meaningful images, which is what carries
// the map outline.
func graphicFloor() floor {
	return floor{ratio: 3.0, clause: "1.1.1", label: "graphic"}
}

// contrastPair is one foreground-on-background combination with the floor it must
// clear. It is a foreground and a background rather than two colours because the
// direction is the contract: §6.5's warning is about a token's polarity, and a
// table of unordered colour pairs cannot express one.
type contrastPair struct {
	fg    string
	bg    string
	floor floor
	// why cites the clause the row comes from, so a failure names the
	// requirement it breaks and not only a pair of numbers.
	why string
}

// textOn returns a 4.5:1 row for text on a surface.
func textOn(fg, bg, why string) contrastPair {
	return contrastPair{fg: fg, bg: bg, floor: textFloor(), why: why}
}

// boundaryOn returns a 3:1 row for a boundary against what it sits on.
func boundaryOn(fg, bg, why string) contrastPair {
	return contrastPair{fg: fg, bg: bg, floor: nonTextFloor(), why: why}
}

// raisedSurfaces are the surfaces other than the page that a control, a panel or
// a callout can sit on. §6.2 and §6.3 quote their ratios against --bg and only
// against --bg, but a text token used on any of these is a real rendering, and
// in this palette the ratio on a raised surface is *lower* than on the page — a
// raised surface is closer to the text, not further from it. The record's own
// numbers are not a substitute for measuring them.
func raisedSurfaces() []string {
	return []string{"--surface", "--surface-sunken", "--accent-surface"}
}

// textAndBoundaryOnSurfaces is the cross-product of the tokens that carry text
// and draw boundaries against every surface in the file. It is generated rather
// than written out because the property that matters is that no combination is
// missing, and a hand-written list of thirty rows is a list where a row gets
// deleted and nobody notices.
func textAndBoundaryOnSurfaces() []contrastPair {
	texts := []string{"--text", "--text-muted", "--text-subtle", "--accent"}

	surfaces := raisedSurfaces()
	pairs := make([]contrastPair, 0, len(surfaces)*(len(texts)+2))

	for _, surface := range surfaces {
		for _, text := range texts {
			pairs = append(
				pairs,
				textOn(text, surface, "text on a raised surface, not only on the page"),
			)
		}

		pairs = append(pairs,
			boundaryOn("--border", surface, "a control outline on a raised surface"),
			boundaryOn("--focus-ring", surface, "a focus ring on a raised surface"),
		)
	}

	return pairs
}

// recordPairs are the rows §6.2 and §6.3 state directly, plus the pairs the
// clauses those sections belong to require. Each cites its source; the citation
// is the difference between a table a reader can check against the record and a
// table they have to take on trust.
func recordPairs() []contrastPair {
	// Preallocated because the cross-product below appends, and because a slice
	// literal here would be reallocated twice on every call — which is every
	// pair assertion, in every theme, twice over.
	pairs := make([]contrastPair, 0, 32)

	pairs = append(pairs,
		// The rows §6.2 and §6.3 quote as "Ratio on --bg".
		textOn("--text", "--bg", "§6.2/§6.3 body text"),
		textOn("--text-muted", "--bg", "§6.2/§6.3 muted text"),
		textOn("--text-subtle", "--bg", "§6.2/§6.3 subtle text"),
		boundaryOn("--border", "--bg", "§6.2/§6.3 the boundary that carries 1.4.11"),
		textOn("--accent", "--bg", "§6.2/§6.3 accent as a link or a mark"),
		boundaryOn("--accent-solid", "--bg", "§6.4 a fill's own shape against the page"),
		textOn("--success", "--bg", "§6.2/§6.3 status colour as text"),
		textOn("--warning", "--bg", "§6.2/§6.3 warning as text and icon, never a fill"),
		textOn("--danger", "--bg", "§6.2/§6.3 danger as text and icon"),
		boundaryOn("--focus-ring", "--bg", "§6.2/§6.3 the focus ring on the page"),

		// The row §6.2 and §6.3 quote as "on --accent-solid": a fill's label.
		textOn("--on-accent", "--accent-solid", "§6.2/§6.3 the label on a solid fill"),

		// §6.5's two-tone ring, against the offset it is drawn on. With no
		// component having set --local-surface this resolves to the page, so
		// the row is the mechanism rather than a new number — and the mechanism
		// is what has to be right.
		boundaryOn("--focus-ring", "--focus-ring-offset",
			"§6.5 the ring against its offset, so it survives landing on a same-hue control"),

		// §6.2 and §6.3's callout rows, against the surface the callout sits on
		// rather than the page.
		boundaryOn("--callout-border", "--callout-surface", "§6.2/§6.3 the callout's own edge"),
		textOn(
			"--callout-secret",
			"--callout-surface",
			"§6.2/§6.3 secret text on the callout surface",
		),
		textOn("--callout-revealed", "--accent-surface", "§6.2/§6.3 the revealed-secret marker"),

		// §4.12.3's two floors on the one pair a campaign may set. With the
		// default --brand-accent: var(--accent) these are the accent rows
		// above; with the fixture applied they are the fixture's.
		textOn("--brand-accent-ink", "--brand-accent", "§4.12.3 the brand pair, at 4.5:1"),
		boundaryOn("--brand-accent", "--bg", "§4.12.3 the brand accent against the page, at 3:1"),

		// §6.2 and §6.3 delegate the map's 3:1 to --map-outline, and §7.1
		// names it as what carries 1.1.1.
		contrastPair{
			fg:    "--map-outline",
			bg:    "--bg",
			floor: graphicFloor(),
			why:   "§6.2/§6.3 and §7.1 the map outline, which is what carries 1.1.1",
		},

		// Layer 3's own pair. Numerically identical to the fill row above,
		// because the tokens are aliases, and kept anyway: a layer-3 token that
		// no pair used would be a layer-3 token with no floor.
		textOn(
			"--btn-primary-fg",
			"--btn-primary-bg",
			"§6.1 layer 3: a primary button's label on its fill",
		),
		textOn(
			"--tab-selected-fg",
			"--tab-selected-bg",
			"§6.1 layer 3: the selected tab's label on its surface",
		),
	)

	return append(pairs, textAndBoundaryOnSurfaces()...)
}

// fixtureCampaignBrand is the §4.12.1 override the second pass applies. It
// stands in for a campaign's theme manifest, so the only thing assertable about
// it is what §4.12.3 makes assertable: the ink clears 4.5:1 on the accent, and
// the accent clears 3:1 on the page.
//
// Teal is the hardest realistic brand, not an easy one. It clears the page floor
// by 0.55 in the dark theme — 3.55:1 against #14181D — so a brand colour even
// slightly lighter fails, which is what makes this fixture evidence that the
// re-run measures rather than a formality. It is one pair applied to both themes
// on purpose: a campaign theme is not per-theme (§4.12.4), so a brand that
// works in one theme and not the other is a real shape of bug, and the fixture
// should be shaped like one.
func fixtureCampaignBrand() map[string]string {
	return map[string]string{
		"--brand-accent":     "#0e7c7b",
		"--brand-accent-ink": "#ffffff",
	}
}

// TestContrastPairsMeetTheirFloors is §10.1's first assertion: every pair in
// §6.2 and §6.3, against the floor that pair claims, in both themes.
//
// The failure message names the token, the computed ratio, the floor, the
// success criterion and the clause the row comes from, because a gate that
// reports "contrast too low" has told the reader nothing they did not already
// know from looking at the page.
// TestOKLCHConvertsToTheReferenceSRGB pins the oklch conversion against values
// the CSS Color 4 specification derives from sRGB, because the conversion is
// otherwise only reachable through the primitive layer — and the primitive layer
// is referenced by no contrast pair.
//
// That gap is not theoretical. The gamma encode between oklch's linear-light
// result and the 8-bit value the WCAG formula consumes was dropped from this
// file at one point, every test still passed, and `unused` caught it only because
// it left a helper behind. A conversion nothing measures is a conversion that can
// be wrong.
//
// The three cases are the ends and the middle, because the two failure modes
// differ: a wrong matrix shows up as a hue error, which red and mid grey both
// expose, and a missing gamma encode shows up as a magnitude error, which only
// the mid grey exposes — red's luminance is 1.0 either way and white is 1.0
// either way, so a set of saturated extremes would pass with the encode removed.
func TestOKLCHConvertsToTheReferenceSRGB(t *testing.T) {
	t.Parallel()

	// The reference values are Oklab's L, a and b for these sRGB triples, as
	// published in CSS Color 4's sample code. #ff0000 is L 0.62796,
	// a 0.22486, b 0.12585, which is chroma 0.25768 at hue 29.234 degrees.
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "pure red, the specification's own reverse conversion",
			value: "oklch(0.62796 0.25768 29.234)",
			want:  "#ff0000",
		},
		{
			name:  "mid grey, where a missing gamma encode shows up",
			value: "oklch(0.5998 0 0)",
			want:  "#808080",
		},
		{name: "white, the top of the range", value: "oklch(1 0 0)", want: "#ffffff"},
		{name: "black, the bottom of the range", value: "oklch(0 0 0)", want: "#000000"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sheet, err := parseStylesheet(`[data-theme="light"] { --probe: ` + tc.value + `; }`)
			if err != nil {
				t.Fatalf("parse the probe stylesheet: %v", err)
			}

			got, err := newResolver(sheet, themeLight, nil).colour("--probe")
			if err != nil {
				t.Fatalf("resolve --probe: %v", err)
			}

			if got.String() != tc.want {
				t.Errorf("%s converts to %s, want %s", tc.value, got, tc.want)
			}
		})
	}
}

func TestContrastPairsMeetTheirFloors(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	for _, theme := range themeNames() {
		t.Run(string(theme), func(t *testing.T) {
			t.Parallel()

			assertPairs(t, sheet, theme, nil, "core theme")
		})
	}
}

// TestContrastPairsMeetTheirFloorsWithFixtureCampaignTheme is §10.1's second
// assertion: the whole set again, with a campaign's theme applied.
//
// A brand override is the only user-supplied value in the product that reaches a
// colour pair, so it is the only place a contrast regression can enter from
// outside this repository. §4.12.2's real enforcement — the generator never
// emitting a protected name — is the theme generator's to prove. What this
// proves is that a brand pair which passed validation still leaves every row in
// the file above its floor, in both themes, on every surface.
func TestContrastPairsMeetTheirFloorsWithFixtureCampaignTheme(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)
	brand := fixtureCampaignBrand()

	for _, theme := range themeNames() {
		t.Run(string(theme), func(t *testing.T) {
			t.Parallel()

			assertPairs(t, sheet, theme, brand, "fixture campaign theme")
		})
	}
}

// TestFixtureCampaignBrandActuallyOverrides guards the re-run against becoming a
// no-op. If the fixture were ever replaced with the file's own default value —
// the obvious "simplification" once someone notices the two look alike — the
// second pass would still pass while proving nothing, because nothing would have
// been applied.
//
// The comparison is on the *declaration*, not on the resolved colour, and that
// distinction is load-bearing. A mid-tone brand colour has room for a 4.5:1 label
// on only one side, so a correct fixture's ink is white — which is also the light
// theme's default --on-accent. Comparing colours would report the override as a
// no-op in light while it plainly is not, and the honest fix for that would be to
// weaken the check. "Applied" means the declaration was replaced; that is the
// property worth asserting.
func TestFixtureCampaignBrandActuallyOverrides(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)
	brand := fixtureCampaignBrand()

	for _, theme := range themeNames() {
		t.Run(string(theme), func(t *testing.T) {
			t.Parallel()

			for _, name := range slices.Sorted(maps.Keys(brand)) {
				declared, ok := sheet.themes[theme][name]
				if !ok {
					t.Errorf("%s is overridden by the fixture but is not declared in the %s theme",
						name, theme)

					continue
				}

				if declared == brand[name] {
					t.Errorf(
						"the fixture campaign theme does not change %s: the %s theme already "+
							"declares it as %q, so the second pass asserts nothing about an override",
						name, theme, declared)
				}
			}
		})
	}
}

// TestNoTokenIsMissingFromEitherTheme is §6.1's rule, enforced in both
// directions. "A token present in only one theme is a bug" cuts either way: a
// dark-only token inherits light's value and renders a light-theme control in the
// wrong colour, and a light-only token renders a dark-theme control in light ink
// on a dark page. Both are the same bug, and the record does not distinguish
// them, so neither does this.
func TestNoTokenIsMissingFromEitherTheme(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)
	classes := tokenClasses()

	// A parser that matched nothing would make the comparisons below trivially
	// equal, so the size is asserted first. 24 is §6.2's and §6.3's own row
	// count; the file carries six more because §6.5, §6.7 and §4.12 require
	// tokens the record names without tabulating.
	const minTokens = 24

	for _, theme := range themeNames() {
		if got := len(sheet.themes[theme]); got < minTokens {
			t.Fatalf("the %s theme block yielded %d tokens, want at least %d; the parser found "+
				"nothing to compare", theme, got, minTokens)
		}
	}

	for _, name := range slices.Sorted(maps.Keys(classes)) {
		for _, theme := range themeNames() {
			if _, ok := sheet.themes[theme][name]; !ok {
				t.Errorf("%s is classified in this test but is not declared in the %s theme",
					name, theme)
			}
		}
	}

	assertNoAsymmetry(t, "light", sheet.themes[themeLight], sheet.themes[themeDark])
	assertNoAsymmetry(t, "dark", sheet.themes[themeDark], sheet.themes[themeLight])
}

// assertNoAsymmetry reports every name present in have but absent from want.
// Reporting all of them rather than the first is deliberate: a merge that drops a
// theme block wholesale produces a dozen of these, and a gate that stops at the
// first makes the reader re-run it a dozen times.
func assertNoAsymmetry(t *testing.T, haveLabel string, have, want map[string]string) {
	t.Helper()

	missing := make([]string, 0, len(have))

	for name := range have {
		if _, ok := want[name]; !ok {
			missing = append(missing, name)
		}
	}

	slices.Sort(missing)

	for _, name := range missing {
		t.Errorf(
			"%s is declared in the %s theme but not in the %s theme; §6.1: a token present in "+
				"only one theme is a bug, and the symptom is that it renders as the other theme's "+
				"value rather than failing",
			name, haveLabel, otherTheme(haveLabel),
		)
	}
}

// otherTheme names the theme that is not the given one, so a failure can say
// which block a token is missing from.
func otherTheme(name string) string {
	if name == string(themeLight) {
		return string(themeDark)
	}

	return string(themeLight)
}

// TestEveryThemeTokenIsClassified is the closure property that makes the class
// table load-bearing. A token added to tokens.css with no row in tokenClasses
// has no floor, no polarity, and no statement about whether it may carry text —
// and the gate would otherwise pass, having never looked at it. This is what
// turns the table from documentation into the expected value of a comparison.
func TestEveryThemeTokenIsClassified(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)
	classes := tokenClasses()

	for _, theme := range themeNames() {
		t.Run(string(theme), func(t *testing.T) {
			t.Parallel()

			for _, name := range slices.Sorted(maps.Keys(sheet.themes[theme])) {
				if _, ok := classes[name]; !ok {
					t.Errorf(
						"%s is declared in the %s theme but has no class in this test; add it to "+
							"tokenClasses with the class §6.1 and §6.5 give it, so the gate has a "+
							"floor to hold it to", name, theme)
				}
			}
		})
	}
}

// TestEveryClassifiedTokenIsUsedInAPair is the other half of the closure: a token
// with a class but no contrast pair is a token whose floor nobody checked. §6.1's
// "the contrast test fails on it" is only true if the test knows the token
// exists, and this is where it finds out.
func TestEveryClassifiedTokenIsUsedInAPair(t *testing.T) {
	t.Parallel()

	pairs := recordPairs()

	// A pair is directional, and the direction is the contract, so this tracks
	// which position each token is allowed to occupy.
	asForeground := make(map[string]struct{}, len(pairs))
	asBackground := make(map[string]struct{}, len(pairs))

	for _, pair := range pairs {
		asForeground[pair.fg] = struct{}{}
		asBackground[pair.bg] = struct{}{}
	}

	for _, name := range slices.Sorted(maps.Keys(tokenClasses())) {
		switch tokenClasses()[name] {
		case classText:
			// A text token must be measured as text. One that only ever appears
			// as a background is a token being used to fill area, which is the
			// wrong class and would otherwise be invisible here.
			if _, ok := asForeground[name]; !ok {
				t.Errorf("%s is classified as text but is never the foreground of a contrast "+
					"pair; its floor against every surface is unmeasured", name)
			}
		case classSurface, classBoundary:
			// Both may be measured in either position — a fill is a boundary
			// against the page and a background for its own label — so appearing
			// at all is enough.
			_, foreground := asForeground[name]
			_, background := asBackground[name]

			if !foreground && !background {
				t.Errorf("%s is classified as %s but appears in no contrast pair; its floor is "+
					"unmeasured", name, tokenClasses()[name])
			}
		case classDecorative, classTranslucent, classNonColour:
			// The absence of a pair is the assertion for these three: relying on
			// a decorative token is not checkable from a ratio, so it is checked
			// by the classification and by
			// TestNoUnmeasurableTokenIsInAPair.
		}
	}
}

// TestNoUnmeasurableTokenIsInAPair is §6.5's "decorative only" made enforceable,
// and the rule that keeps a translucent colour out of a contrast-bearing slot.
//
// Both classes exist because a ratio cannot catch them. A decorative token
// clears no floor, so putting one in a pair would assert a guarantee the record
// explicitly declines to make. A translucent token measures as a function of
// whatever is behind it, so a ratio against a named background is a number about
// a stacking context this file does not control — and a brand override or a
// plugin can change what is behind it.
func TestNoUnmeasurableTokenIsInAPair(t *testing.T) {
	t.Parallel()

	classes := tokenClasses()

	for _, pair := range recordPairs() {
		for _, name := range []string{pair.fg, pair.bg} {
			class, known := classes[name]
			if !known {
				t.Errorf("pair %s on %s names %s, which is not declared in either theme; "+
					"this gate measures only what the stylesheet declares", pair.fg, pair.bg, name)

				continue
			}

			switch class {
			case classDecorative:
				t.Errorf(
					"pair %s on %s puts the decorative token %s in a contrast-bearing slot; "+
						"§6.2 and §6.5 decorate it and decline a floor for it, so a ratio here "+
						"would assert a guarantee that does not exist",
					pair.fg, pair.bg, name)
			case classTranslucent:
				t.Errorf(
					"pair %s on %s puts the translucent token %s in a contrast-bearing slot; what "+
						"it measures depends on what is behind it, which nothing here controls",
					pair.fg, pair.bg, name)
			case classNonColour:
				t.Errorf(
					"pair %s on %s puts the non-colour token %s in a contrast-bearing slot; the "+
						"contrast arithmetic does not apply to it", pair.fg, pair.bg, name)
			case classText, classBoundary, classSurface:
				// In a contrast-bearing slot, as intended.
			}
		}
	}
}

// TestSolidFillsMeetTheNonTextFloorOnTheirOwnPage is §6.4 as a rule rather than
// as a value.
//
// §6.4's requirement is not "use #5B9BE8"; it is that a solid fill's own shape
// clears 3:1 against the page it sits on, because 1.4.11 is about the shape and
// not only the label. Asserting the requirement rather than the hex means the
// gate survives a future value that is correct for a different reason, and it is
// the assertion that fails if someone substitutes a fill that does not hold up.
func TestSolidFillsMeetTheNonTextFloorOnTheirOwnPage(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	for _, theme := range themeNames() {
		t.Run(string(theme), func(t *testing.T) {
			t.Parallel()

			resolver := newResolver(sheet, theme, nil)

			page, err := resolver.colour("--bg")
			if err != nil {
				t.Fatalf("resolve --bg: %v", err)
			}

			for _, name := range solidFillTokens() {
				fill, err := resolver.colour(name)
				if err != nil {
					t.Errorf("resolve %s: %v", name, err)

					continue
				}

				if fill.alpha < 1 {
					// Reported precisely by TestNoUnmeasurableTokenIsInAPair.
					continue
				}

				assertRatio(t, name, "--bg", contrastRatio(fill, page), nonTextFloor(),
					"§6.4 a solid fill's shape against the page it sits on")
			}
		})
	}
}

// TestWarningIsNeverAFill is §6.5's second rule, made mechanical. The rule is
// "--warning is a text/icon colour, never a fill", and a rule stated as a
// sentence is a rule the next author re-derives from the sentence. This asserts
// the consequence instead: no fill token in the file resolves to the warning
// colour, in either theme.
//
// --danger and --success are deliberately not covered. §6.4 says the same
// inversion applies to "--danger and every other filled control", so a
// danger-filled control is legitimate, provided its own shape clears 3:1, which
// TestSolidFillsMeetTheNonTextFloorOnTheirOwnPage checks. Amber is the one status
// colour the record rules out as a fill, in both themes and for the same reason:
// dark amber on white is illegible, and a fill is the one place where the
// foreground is not chosen.
func TestWarningIsNeverAFill(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	for _, theme := range themeNames() {
		t.Run(string(theme), func(t *testing.T) {
			t.Parallel()

			resolver := newResolver(sheet, theme, nil)

			warning, err := resolver.colour("--warning")
			if err != nil {
				t.Fatalf("resolve --warning: %v", err)
			}

			for _, name := range solidFillTokens() {
				fill, err := resolver.colour(name)
				if err != nil {
					t.Errorf("resolve %s: %v", name, err)

					continue
				}

				if fill == warning {
					t.Errorf(
						"%s resolves to the warning colour %s; §6.5: --warning is a text and icon "+
							"colour and is never a fill, because a fill is the one place where the "+
							"foreground is not chosen", name, warning)
				}
			}
		})
	}
}

// solidFillTokens are the tokens that paint a solid area and therefore have a
// boundary the user has to see, which is §6.4's subject.
//
// Declared rather than derived, and that is a correction rather than a shortcut.
// Deriving the set from a name suffix gets it wrong twice over: `--bg` ends in
// "-bg" and is the page, and `--tab-selected-bg` ends in "-bg" and is a tint whose
// boundary requirement is carried by the label sitting on it. Deriving it from
// the current colour is worse, because it is circular — repointing a fill at
// `--warning` makes it stop looking like a fill, which is precisely the mistake
// the rule exists to catch, so the mistake would silence the rule. That hole was
// found by mutating `--btn-primary-bg` to `var(--warning)` and watching the gate
// pass.
//
// A *new* solid fill is not in this list, and nothing here would notice until it
// was added. That is covered one step along: a new token has to be classified (or
// the gate fails), and a classified token has to appear in a contrast pair (or the
// gate fails), so its label against its own fill is always checked. Only the shape
// against the page would go unchecked until the list is updated.
func solidFillTokens() []string {
	return []string{"--accent-solid", "--btn-primary-bg"}
}

// TestOnlyDecorativeSurfacesUseBorderSubtle is §6.5's third rule, as far as one
// file can carry it: "--border-subtle may not be the only boundary of an
// interactive element", which in practice means nothing may be wired to it by
// accident.
//
// Which elements are interactive is markup, and markup belongs to the component
// and chrome work. What this file owns is the wiring, so this asserts that the
// decorative token is referenced by exactly two declarations and that both are
// the decorative surface hairlines §6.7 needs for its forced-colors fallback. A
// third reference — a control outline, a card edge, an input border — is the
// accident, caught here rather than by a reviewer reading a colour name.
func TestOnlyDecorativeSurfacesUseBorderSubtle(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	allowed := map[string]struct{}{
		"--surface-border":        {},
		"--surface-sunken-border": {},
	}

	found := make(map[string]struct{}, len(allowed))

	for _, block := range sheet.blocks {
		if block.isBridge {
			// The bridge's whole job is naming every token, so it references
			// --border-subtle by construction. A colour literal there is still
			// caught, by TestNoColourLiteralOutsideThePrimitiveAndThemeBlocks.
			continue
		}

		for _, decl := range block.decls {
			if !strings.Contains(decl.value, "var(--border-subtle)") {
				continue
			}

			found[decl.name] = struct{}{}

			if _, ok := allowed[decl.name]; !ok {
				t.Errorf(
					"%s:%d: %s is wired to the decorative token --border-subtle; §6.5 reserves it "+
						"for decoration, and only the two surface hairlines §6.7 needs for its "+
						"forced-colors fallback may point at it",
					block.prelude, decl.line, decl.name)
			}
		}
	}

	for _, name := range slices.Sorted(maps.Keys(allowed)) {
		if _, ok := found[name]; !ok {
			t.Errorf("%s never references --border-subtle; §6.7's forced-colors fallback reaches "+
				"it through that reference, so without it the separation is lost when shadows are "+
				"dropped", name)
		}
	}
}

// TestNoColourLiteralOutsideThePrimitiveAndThemeBlocks is the rule that keeps the
// two theme blocks authoritative.
//
// A hex or an oklch anywhere else — a media query, a component rule that ended up
// in this file, a "just for forced-colors" exception — is a colour that bypasses
// §6.1's layering and the gate above. In forced-colors: active the correct move
// is a system keyword, and the system palette is the user's, so this is also the
// assertion that the §6.7 block cannot smuggle one of our colours back in. A
// var() reference is fine: a media query that overrides one token from another
// is changing a value, not introducing a colour.
func TestNoColourLiteralOutsideThePrimitiveAndThemeBlocks(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	colourPrefixes := []string{
		"#", "rgb(", "rgba(", "hsl(", "hsla(", "oklch(", "oklab(", "lab(", "lch(", "color(",
	}

	for _, block := range sheet.blocks {
		if block.isPrimitive || block.isTheme {
			continue
		}

		for _, decl := range block.all() {
			value := strings.ToLower(strings.TrimSpace(decl.value))

			for _, prefix := range colourPrefixes {
				if !strings.HasPrefix(value, prefix) {
					continue
				}

				t.Errorf(
					"%s:%d: %s is declared %q, a colour literal outside the primitive block and "+
						"the two theme blocks; a colour's value belongs to exactly one of them, or "+
						"the gate above is not looking at it",
					block.prelude, decl.line, decl.name, decl.value)

				break
			}
		}
	}
}

// TestNoForcedColorAdjustNone guards §6.7's first clause against the failure mode
// nothing else can catch.
//
// forced-color-adjust: none opts *out* of forced colours, and the failure is
// asymmetric: one declaration anywhere in the cascade disables the whole mode
// for that subtree, the user agent stops substituting, and a palette that was
// never going to survive forced colours on its own now also does not. Nothing in
// this repository can contrast-check that, because the browser decides it and the
// stylesheet still looks correct to every tool that reads it.
func TestNoForcedColorAdjustNone(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	for _, block := range sheet.blocks {
		for _, decl := range block.all() {
			if !strings.HasPrefix(decl.name, "forced-color-adjust") {
				continue
			}

			if strings.ToLower(strings.ReplaceAll(decl.value, " ", "")) == "none" {
				t.Errorf(
					"%s:%d: %s: none; §6.7 requires forced-color-adjust: auto throughout, and a "+
						"single `none` disables the mode for a whole subtree with no other symptom",
					block.prelude, decl.line, decl.name)
			}
		}
	}
}

// TestTokenFileDeclaresNoAnimation is §6.7's "nothing may depend on an animation
// to become visible", enforced structurally.
//
// The rule is not "animations must be subtle". It is that no content state may be
// reachable only through motion — a reveal that waits for a transition, a panel
// that fades in, a value that appears at the end of a keyframe. The mechanical
// consequence is that this file must contain no keyframes and no animation token
// at all, so there is nothing here for anything to wait on. The elevation
// shadows are the only depth the system has and they are decorative; --dur-*
// governs a transition a component may run, never a state a component must
// reach.
func TestTokenFileDeclaresNoAnimation(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	// The comment-stripped source, not the file: a keyframe named in a comment is
	// a note about one, not a declaration of one.
	if strings.Contains(sheet.src, "@keyframes") {
		t.Error("tokens.css declares @keyframes; §6.7 requires that nothing may depend on an " +
			"animation to become visible, and a keyframe is how something becomes visible only " +
			"through motion")
	}

	for _, block := range sheet.blocks {
		for _, decl := range block.all() {
			if strings.HasPrefix(decl.name, "--animate-") {
				t.Errorf(
					"%s:%d: %s is an animation token; §6.7 requires that nothing may depend on an "+
						"animation to become visible, so a duration may not be bound to a named "+
						"animation here", block.prelude, decl.line, decl.name)
			}
		}
	}
}

// TestReducedMotionCollapsesEveryDuration is §6.7's motion row, checked rather
// than assumed. "All --dur-* → 1ms" is a rule about a *set*, and a set is exactly
// the thing that drifts: a fifth duration added later and not listed in the media
// block is the one that keeps animating, and it is invisible until someone with
// the setting enabled reports it.
//
// 1ms and not 0ms is deliberate. A zero-duration transition still dispatches
// transitionend, and phase 9's live plane and anything keying off a transition
// would wait for an event that never fires.
func TestReducedMotionCollapsesEveryDuration(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	durations := make([]string, 0, len(sheet.primitives))

	for _, decl := range sheet.primitiveDecls() {
		if strings.HasPrefix(decl.name, "--dur-") {
			durations = append(durations, decl.name)
		}
	}

	if len(durations) == 0 {
		t.Fatal("no --dur-* token in the primitive block; §6.7's reduced-motion rule has nothing " +
			"to collapse, so the assertion below would pass vacuously")
	}

	reduced := sheet.media["(prefers-reduced-motion: reduce)"]

	if len(reduced) == 0 {
		t.Fatal("no @media (prefers-reduced-motion: reduce) block in tokens.css; §6.7 requires " +
			"every --dur-* to become 1ms")
	}

	for _, name := range durations {
		value, ok := reduced[name]
		if !ok {
			t.Errorf(
				"@media (prefers-reduced-motion: reduce) does not set %s; §6.7 requires every "+
					"--dur-* to become 1ms, and a duration left out is the one that keeps animating "+
					"for the user who asked it not to", name)

			continue
		}

		if !strings.EqualFold(strings.TrimSpace(value), "1ms") {
			t.Errorf("@media (prefers-reduced-motion: reduce) sets %s: %s, want 1ms", name, value)
		}
	}
}

// TestPreferredContrastPromotesTheWeakTextTokens is §6.7's contrast row. The two
// weak text tokens must stop existing as separate values, so a muted label a
// low-vision user can no longer read is promoted rather than merely
// contrasted-in-place.
func TestPreferredContrastPromotesTheWeakTextTokens(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	more := sheet.media["(prefers-contrast: more)"]

	if len(more) == 0 {
		t.Fatal("no @media (prefers-contrast: more) block in tokens.css; §6.7 requires " +
			"--text-muted and --text-subtle to promote to --text and --border to thicken")
	}

	for _, name := range []string{"--text-muted", "--text-subtle"} {
		want := "var(--text)"
		got := strings.ReplaceAll(more[name], " ", "")

		if got != want {
			t.Errorf("@media (prefers-contrast: more) sets %s: %q, want %q; §6.7 promotes both "+
				"weak text tokens to --text rather than only adjusting them in place",
				name, more[name], want)
		}
	}

	// 1.4.11 is about the width of a boundary as well as its colour, so the
	// thicker border is a doubling of the width token rather than a second
	// border declaration in every component.
	if got := strings.TrimSpace(more["--border-w"]); got != "2px" {
		t.Errorf("@media (prefers-contrast: more) sets --border-w: %q, want 2px; §6.7 requires "+
			"--border to thicken", got)
	}
}

// TestForcedColorsFallsBackToSystemColours is §6.7's first row, checked for the
// three things that make it work rather than for the keywords themselves.
//
// Our palette does not survive forced colours, and the point of the mode is that
// the user's does. So: the shadows are nulled, because a dropped shadow leaves
// an elevation with no separation at all; the borders become ButtonBorder, which
// is what puts the 1px hairline back for every surface whose separation came
// from a shadow; and the focus ring switches to Highlight, because a ring drawn
// in one of our colours is a ring in a colour the user asked the system to
// remove.
func TestForcedColorsFallsBackToSystemColours(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	forced := sheet.media["(forced-colors: active)"]

	if len(forced) == 0 {
		t.Fatal("no @media (forced-colors: active) block in tokens.css; §6.7 requires " +
			"forced-color-adjust: auto throughout and a 1px border fallback for every " +
			"shadow-based separation")
	}

	// The shadows are named tokens precisely so this is one assertion: an
	// elevation with no token is an elevation whose separation cannot be
	// restored when the mode drops it.
	shadows := 0

	for _, decl := range sheet.primitiveDecls() {
		if !strings.HasPrefix(decl.name, "--shadow-") {
			continue
		}

		shadows++

		if got := strings.TrimSpace(forced[decl.name]); got != "none" {
			t.Errorf(
				"@media (forced-colors: active) does not set %s to none (got %q); §6.7: shadows "+
					"are dropped in this mode, so an elevation that keeps its shadow token keeps a "+
					"separation that no longer renders", decl.name, got)
		}
	}

	if shadows == 0 {
		t.Error("no --shadow-* token in the primitive block; §6.7's first rule is about shadow-" +
			"based separation, and with no shadow token there is nothing for the mode to drop")
	}

	// These two are the fallback, so they must become the system hairline rather
	// than one of ours. TestNoColourLiteralOutsideThePrimitiveAndThemeBlocks
	// already rules out our own colours here; this pins the system keyword so
	// the fallback is a *border* and not merely some other value.
	for _, name := range []string{"--border", "--border-subtle"} {
		if got := strings.TrimSpace(forced[name]); got != "ButtonBorder" {
			t.Errorf("@media (forced-colors: active) sets %s: %q, want ButtonBorder; §6.7 "+
				"requires a 1px system border for every shadow-based separation", name, got)
		}
	}

	if got := strings.TrimSpace(forced["--focus-ring"]); got != "Highlight" {
		t.Errorf("@media (forced-colors: active) sets --focus-ring: %q, want Highlight; §6.7 "+
			"switches focus indicators to Highlight", got)
	}

	// The offset is named rather than left alone for a reason: a two-tone ring
	// whose offset is one of our colours is a ring drawn in a colour this mode
	// exists to remove.
	if got := strings.TrimSpace(forced["--focus-ring-offset"]); got != "Canvas" {
		t.Errorf("@media (forced-colors: active) sets --focus-ring-offset: %q, want Canvas; "+
			"§6.5's two-tone ring needs an offset the system palette owns, or the ring is drawn in "+
			"a colour the mode removes", got)
	}
}

// TestMediaBlocksFollowTheThemeBlocks is a cascade assertion, and it is here
// because the alternative is a silent failure.
//
// :root and [data-theme="light"] have identical specificity, so a value
// re-declared in a media query placed above a theme block loses on source order
// and nothing reports it. The symptom is a user who has asked for reduced motion,
// more contrast or forced colours and gets the default instead — no error, no
// console warning, just a setting that quietly does not apply. Reordering this
// file to put the media blocks first would break all three at once and pass
// every other test here.
func TestMediaBlocksFollowTheThemeBlocks(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	if len(sheet.media) == 0 {
		t.Fatal("no @media block found in tokens.css; §6.7's forced-colors, contrast and motion " +
			"rows are all implemented as media queries and none of them are present")
	}

	latest := 0

	for _, theme := range themeNames() {
		if line, ok := sheet.themeLines[theme]; ok && line > latest {
			latest = line
		}
	}

	for _, query := range slices.Sorted(maps.Keys(sheet.mediaLines)) {
		if sheet.mediaLines[query] < latest {
			t.Errorf(
				"@media %s begins on line %d, above the last theme block on line %d; it has the "+
					"same specificity, so it loses on source order and the setting silently does "+
					"not apply", query, sheet.mediaLines[query], latest)
		}

		// The other half of the same argument. A selector with *higher*
		// specificity than :root would win on specificity and the order above
		// would stop mattering, which is fine; but a selector with *lower*
		// specificity than [data-theme] would lose regardless of order, and
		// :root is the one value on either side of that boundary that a reader
		// has to reason about. Asserting it means the two properties are checked
		// together rather than one of them being assumed.
		for _, selector := range sheet.mediaSelectors[query] {
			if selector != ":root" {
				t.Errorf("@media %s targets %q rather than :root; :root has the same specificity "+
					"as [data-theme], so the override depends on source order, and any other "+
					"selector changes which of the two rules that depends on",
					query, selector)
			}
		}
	}
}

// TestSpacingScaleIsTheSpacingMultiplier ties the two spacing names together.
//
// --space-N is the scale a component reads; --spacing is Tailwind's own root
// multiplier, from which p-4, gap-2 and every other numeric utility is computed.
// They are one scale declared twice, and a scale declared twice is how p-4 and
// --space-4 drift apart: a component mixing a utility and a token then has two
// lengths where it meant one, and neither is wrong on its own.
func TestSpacingScaleIsTheSpacingMultiplier(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	spacing, ok := sheet.primitives["--spacing"]
	if !ok {
		t.Fatal("no --spacing in the primitive block; Tailwind's numeric spacing utilities are " +
			"derived from it, so the scale below has nothing to be a multiple of")
	}

	unit, err := parseRem(spacing)
	if err != nil {
		t.Fatalf("parse --spacing: %v", err)
	}

	steps := make([]string, 0, len(sheet.primitives))

	for _, decl := range sheet.primitiveDecls() {
		if strings.HasPrefix(decl.name, "--space-") {
			steps = append(steps, decl.name)
		}
	}

	if len(steps) == 0 {
		t.Fatal("no --space-* token in the primitive block")
	}

	slices.Sort(steps)

	for _, name := range steps {
		multiplier, err := strconv.Atoi(strings.TrimPrefix(name, "--space-"))
		if err != nil {
			t.Errorf("%s is not a whole-number step, so it cannot be --spacing x N; the name and "+
				"the value have drifted apart", name)

			continue
		}

		got, err := parseRem(sheet.primitives[name])
		if err != nil {
			t.Errorf("parse %s: %v", name, err)

			continue
		}

		if want := unit * float64(multiplier); math.Abs(got-want) > 1e-9 {
			t.Errorf("%s: %s, want %g (%g x %d); a component mixing p-%d with %s would then have "+
				"two different lengths for one step of the scale",
				name, sheet.primitives[name], want, unit, multiplier, multiplier, name)
		}
	}
}

// TestPrimitiveRampsAreInGamut is why the gate converts oklch at all.
//
// An out-of-gamut oklch is not an error in a browser: it is clipped to sRGB
// before anything is painted. The colour that ships is therefore not the colour
// written here, and a contrast figure computed from the unclipped value would be
// a figure about a colour no user ever sees — which is the one failure mode a
// contrast gate exists to prevent, arriving through the gate itself.
func TestPrimitiveRampsAreInGamut(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	colours := 0

	for _, decl := range sheet.primitiveDecls() {
		if !strings.HasPrefix(decl.name, "--color-") {
			continue
		}

		colours++

		parsed, err := parseOKLCH(decl.value)
		if err != nil {
			t.Errorf("parse %s: %s: %v", decl.name, decl.value, err)

			continue
		}

		if !parsed.inGamut {
			t.Errorf(
				"%s: %s is outside the sRGB gamut; a browser clips it, so the colour that renders "+
					"is not the colour written here and any ratio computed from it describes "+
					"something no user sees", decl.name, decl.value)
		}
	}

	if colours == 0 {
		t.Error("no --color-* token in the primitive block; §6.1's first layer is oklch ramps, " +
			"and with none declared the gamut and monotonicity checks have nothing to check")
	}
}

// TestPrimitiveRampsDarkenWithTheirStepNumber is the property that makes a ramp
// a scale, and the direction is the part worth asserting.
//
// Nine steps of one hue are a ramp; nine colours of one hue are a palette. The
// numbering follows the convention every utility framework uses — 50 is the
// lightest step and 900 the darkest — so a step number is a statement about
// lightness and not an arbitrary index. If that ordering breaks, "use 600 for
// emphasis" and "use 300 for emphasis" stop meaning anything relative to each
// other, and the mistake is invisible in review because every individual value is
// a perfectly good colour. No component chooses a step yet, so nothing would
// catch it.
func TestPrimitiveRampsDarkenWithTheirStepNumber(t *testing.T) {
	t.Parallel()

	sheet := loadTokens(t)

	// ramp -> its steps, ordered by the number in the name rather than by
	// position in the file, because a scale whose order is not its order is the
	// thing being tested for.
	byRamp := make(map[string][]rampStep)

	for _, decl := range sheet.primitiveDecls() {
		if !strings.HasPrefix(decl.name, "--color-") {
			continue
		}

		ramp, step, ok := splitRampStep(decl.name)
		if !ok {
			t.Errorf("%s is not a stepped scale; §6.1's primitive layer is ramps, and a ramp with "+
				"no step has no order to check", decl.name)

			continue
		}

		parsed, err := parseOKLCH(decl.value)
		if err != nil {
			t.Errorf("parse %s: %s: %v", decl.name, decl.value, err)

			continue
		}

		byRamp[ramp] = append(
			byRamp[ramp],
			rampStep{number: step, decl: decl, lightness: parsed.lightness},
		)
	}

	for _, ramp := range slices.Sorted(maps.Keys(byRamp)) {
		steps := byRamp[ramp]
		slices.SortFunc(steps, func(a, b rampStep) int { return a.number - b.number })

		for idx := 1; idx < len(steps); idx++ {
			if steps[idx].lightness >= steps[idx-1].lightness {
				t.Errorf(
					"%s is not ordered: step %d is L %.3f and step %d is L %.3f, so the higher "+
						"number is not the darker step; a scale whose order is not its order is a "+
						"set of unrelated colours", ramp, steps[idx-1].number, steps[idx-1].lightness,
					steps[idx].number, steps[idx].lightness)
			}
		}
	}
}

// rampStep is one step of a primitive ramp, with its declared lightness kept
// alongside the declaration. Converting to 8-bit sRGB first would lose the
// precision the ordering is a statement about.
type rampStep struct {
	number    int
	decl      declaration
	lightness float64
}

// splitRampStep divides --color-<ramp>-<step> into its ramp name and step
// number, and reports false for a name that is not a stepped scale. The step is
// parsed as a number rather than compared as text, because "50" and "050" sort
// differently and a ramp that only happens to be padded still has to be checked
// for order.
func splitRampStep(name string) (ramp string, step int, ok bool) {
	trimmed := strings.TrimPrefix(name, "--color-")

	ramp, digits, found := strings.CutLast(trimmed, "-")
	if !found {
		return "", 0, false
	}

	step, err := strconv.Atoi(digits)
	if err != nil {
		return "", 0, false
	}

	return ramp, step, true
}

// assertPairs runs every row of the table against one theme, with one set of
// overrides applied or not.
//
// A row that cannot be resolved is reported as a failure rather than skipped. A
// gate that skips what it cannot read is a gate that reports green on a
// stylesheet it does not understand, and the two most likely reasons to be unable
// to read one — a token renamed, a var() chain that does not terminate — are
// exactly the two most likely ways for this file to break.
func assertPairs(
	t *testing.T,
	sheet tokenSheet,
	theme themeName,
	overrides map[string]string,
	label string,
) {
	t.Helper()

	resolver := newResolver(sheet, theme, overrides)

	for _, pair := range recordPairs() {
		fg, err := resolver.colour(pair.fg)
		if err != nil {
			t.Errorf("%s: %s on %s: resolve the foreground: %v", theme, pair.fg, pair.bg, err)

			continue
		}

		bg, err := resolver.colour(pair.bg)
		if err != nil {
			t.Errorf("%s: %s on %s: resolve the background: %v", theme, pair.fg, pair.bg, err)

			continue
		}

		assertRatio(t, pair.fg, pair.bg, contrastRatio(fg, bg), pair.floor, label+": "+pair.why)
	}
}

// assertRatio reports one measurement against one floor. It is the only place in
// this file that decides pass or fail for contrast, so the message it writes is
// the only thing a reader of a failed build gets: the token, the pair, the
// computed ratio, the floor, the success criterion, and how far short it fell.
func assertRatio(t *testing.T, fg, bg string, got float64, want floor, why string) {
	t.Helper()

	if got+ratioEpsilon < want.ratio {
		t.Errorf(
			"%s on %s: %.2f:1, floor %.1f:1 (WCAG %s, %s) — short by %.2f:1. %s",
			fg, bg, got, want.ratio, want.clause, want.label, want.ratio-got, why)
	}
}

// newResolver builds the var() lookup for one theme and one set of overrides.
func newResolver(sheet tokenSheet, theme themeName, overrides map[string]string) resolver {
	return resolver{scope: sheet.themes[theme], overrides: overrides}
}

// resolver answers "what is this token's value in this theme", which is what
// var(--name) means in CSS. Overrides come first because that is precisely what
// an override is: a value taking precedence for the scope it is declared in.
type resolver struct {
	scope     map[string]string
	overrides map[string]string
}

// colour resolves a token name to a colour, following var() references.
func (r resolver) colour(name string) (colour, error) {
	if r.scope == nil {
		return colour{}, fmt.Errorf("no theme scope to resolve %s in", name)
	}

	raw, ok := r.raw(name)
	if !ok {
		return colour{}, fmt.Errorf("%s is not declared in this theme", name)
	}

	return r.value(raw, 0)
}

// raw returns a token's declared value, preferring an override.
func (r resolver) raw(name string) (string, bool) {
	if value, ok := r.overrides[name]; ok {
		return value, true
	}

	value, ok := r.scope[name]

	return value, ok
}

// value parses one declaration value into a colour.
//
// It understands exactly the syntaxes this file is allowed to use and rejects
// everything else. A value it cannot parse is a value it cannot measure, and
// silently measuring a subset is how a contrast gate comes to certify a
// stylesheet it does not understand.
func (r resolver) value(raw string, depth int) (colour, error) {
	if depth > maxVarDepth {
		return colour{}, fmt.Errorf(
			"var() nested more than %d deep; is there a cycle?",
			maxVarDepth,
		)
	}

	trimmed := strings.TrimSpace(raw)

	switch {
	case strings.HasPrefix(trimmed, "var("):
		return r.varRef(trimmed, depth)
	case strings.HasPrefix(trimmed, "#"):
		return parseHex(trimmed)
	case strings.HasPrefix(trimmed, "rgb("), strings.HasPrefix(trimmed, "rgba("):
		return parseRGBFunction(trimmed)
	case strings.HasPrefix(trimmed, "oklch("):
		parsed, err := parseOKLCH(trimmed)
		if err != nil {
			return colour{}, err
		}

		return parsed.sRGB(), nil
	default:
		return colour{}, fmt.Errorf(
			"%q is not a colour: only #hex, rgb() and oklch() are measurable here, and a keyword, "+
				"a length or `none` in a colour slot is a value no contrast figure can be computed "+
				"from", trimmed)
	}
}

// varRef implements var(--name) and var(--name, <fallback>).
//
// The fallback is used when the name is not declared, which is not a
// convenience: it is what lets --focus-ring-offset name a surface a component
// may or may not have set, and the gate has to resolve it the way a browser
// would or it would measure a token the stylesheet never uses.
func (r resolver) varRef(raw string, depth int) (colour, error) {
	name, fallback, hasFallback, err := splitVarCall(raw)
	if err != nil {
		return colour{}, err
	}

	target, declared := r.raw(name)
	if !declared {
		if !hasFallback {
			return colour{}, fmt.Errorf("var(%s) names an undeclared token", name)
		}

		return r.value(fallback, depth+1)
	}

	return r.value(target, depth+1)
}

// splitVarCall pulls apart `var(--name)` or `var(--name, <fallback>)`. The
// fallback may contain a var() and commas of its own, so the split is by
// parenthesis depth rather than by the first comma.
func splitVarCall(raw string) (name, fallback string, hasFallback bool, err error) {
	if !strings.HasSuffix(raw, ")") {
		return "", "", false, fmt.Errorf("%q: var() is not closed", raw)
	}

	inner := strings.TrimSpace(raw[len("var(") : len(raw)-1])
	depth := 0

	for idx := 0; idx < len(inner); idx++ {
		switch inner[idx] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(inner[:idx]), strings.TrimSpace(inner[idx+1:]), true, nil
			}
		}
	}

	if !strings.HasPrefix(inner, "--") {
		return "", "", false, fmt.Errorf("%q: var() must name a custom property", raw)
	}

	return inner, "", false, nil
}

// colour is one resolved colour: gamma-encoded sRGB in 0..1, with the alpha it
// was declared with.
//
// Eight-bit, not float. The WCAG formula is defined over sRGB values, and
// quantising to 8 bits before measuring is what makes a hex literal and an oklch
// literal comparable — a browser rasterises to 8 bits per channel, so anything
// finer measures a value no display will show.
type colour struct {
	r, g, b float64
	alpha   float64
}

// relativeLuminance is WCAG 2.2's definition: each sRGB channel is linearised,
// then weighted 0.2126 / 0.7152 / 0.0722.
//
// The linearisation threshold is 0.04045, the sRGB specification's value. The
// older 0.03928 that WCAG 2.0 quoted gives an identical result at 8-bit
// precision — the two differ only for channel values between them, and no 8-bit
// step falls in that gap — so the choice is not a variable this gate has to be
// defended on.
func (c colour) relativeLuminance() float64 {
	return 0.2126*linearise(c.r) + 0.7152*linearise(c.g) + 0.0722*linearise(c.b)
}

// linearise converts one gamma-encoded sRGB channel to linear light.
func linearise(channel float64) float64 {
	if channel <= 0.04045 {
		return channel / 12.92
	}

	return math.Pow((channel+0.055)/1.055, 2.4)
}

// contrastRatio is WCAG 2.2's (L_lighter + 0.05) / (L_darker + 0.05).
func contrastRatio(a, b colour) float64 {
	lighter, darker := a.relativeLuminance(), b.relativeLuminance()
	if darker > lighter {
		lighter, darker = darker, lighter
	}

	return (lighter + 0.05) / (darker + 0.05)
}

// String renders a colour as a hex triple, so a failure message names a colour
// the way the stylesheet does.
func (c colour) String() string {
	return fmt.Sprintf("#%02x%02x%02x", channel8(c.r), channel8(c.g), channel8(c.b))
}

// channel8 quantises one channel to 8 bits, which is also the gamut clamp: a
// browser cannot display a value outside 0..1, so the clamp is what the browser
// does rather than a choice made here.
func channel8(channel float64) int {
	return int(math.Round(quantise(channel) * 255))
}

// quantise is the gamut clip and the 8-bit step a browser's rasteriser performs,
// expressed in 0..1. Every colour this file measures has passed through it, so
// the WCAG formula is applied to the same value a display would receive.
func quantise(channel float64) float64 {
	return math.Round(clamp01(channel)*255) / 255
}

// clamp01 bounds a channel to 0..1.
func clamp01(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}

// parseHex parses #rgb, #rrggbb and #rrggbbaa.
func parseHex(value string) (colour, error) {
	digits := strings.TrimPrefix(value, "#")

	switch len(digits) {
	case 3, 4:
		expanded := make([]string, 0, len(digits))

		for _, digit := range digits {
			expanded = append(expanded, string(digit)+string(digit))
		}

		digits = strings.Join(expanded, "")
	case 6, 8:
	default:
		return colour{}, fmt.Errorf("%q is not a 3, 4, 6 or 8 digit hex colour", value)
	}

	channels := make([]float64, 0, 4)

	for idx := 0; idx < len(digits); idx += 2 {
		parsed, err := strconv.ParseUint(digits[idx:idx+2], 16, 8)
		if err != nil {
			return colour{}, fmt.Errorf("%q: %w", value, err)
		}

		channels = append(channels, float64(parsed)/255)
	}

	result := colour{r: channels[0], g: channels[1], b: channels[2], alpha: 1}

	if len(channels) == 4 {
		result.alpha = channels[3]
	}

	return result, nil
}

// parseRGBFunction parses the modern `rgb(r g b / a)` and the legacy
// `rgb(r, g, b)` forms, with channels as 0..255 numbers or percentages. The
// legacy comma form is accepted because it is still what a hand-edited value
// tends to become, and a syntax the parser rejects is a syntax someone works
// around by not using the token layer at all.
func parseRGBFunction(value string) (colour, error) {
	open := strings.Index(value, "(")
	if open < 0 || !strings.HasSuffix(value, ")") {
		return colour{}, fmt.Errorf("%q: rgb() is not closed", value)
	}

	body := value[open+1 : len(value)-1]

	components, alpha, hasAlpha := strings.Cut(body, "/")
	if !hasAlpha {
		components, alpha = body, ""
	}

	fields := strings.FieldsFunc(components, func(r rune) bool { return r == ',' || r == ' ' })

	if len(fields) != 3 {
		return colour{}, fmt.Errorf("%q: rgb() needs three channels, got %d", value, len(fields))
	}

	parsed, err := parseRGBChannels(fields)
	if err != nil {
		return colour{}, fmt.Errorf("%q: %w", value, err)
	}

	result := colour{r: parsed[0], g: parsed[1], b: parsed[2], alpha: 1}

	if alpha = strings.TrimSpace(alpha); alpha != "" {
		parsedAlpha, err := parseAlpha(alpha)
		if err != nil {
			return colour{}, fmt.Errorf("%q: %w", value, err)
		}

		result.alpha = parsedAlpha
	}

	return result, nil
}

// parseRGBChannels converts three rgb() channel components to 0..1.
func parseRGBChannels(fields []string) ([3]float64, error) {
	var out [3]float64

	for idx, field := range fields {
		field = strings.TrimSpace(field)

		if digits, isPercent := strings.CutSuffix(field, "%"); isPercent {
			percent, err := strconv.ParseFloat(digits, 64)
			if err != nil {
				return out, fmt.Errorf("channel %q: %w", field, err)
			}

			out[idx] = clamp01(percent / 100)

			continue
		}

		value, err := strconv.ParseFloat(field, 64)
		if err != nil {
			return out, fmt.Errorf("channel %q: %w", field, err)
		}

		out[idx] = clamp01(value / 255)
	}

	return out, nil
}

// parseAlpha converts an alpha component, as a number or a percentage.
func parseAlpha(value string) (float64, error) {
	if digits, isPercent := strings.CutSuffix(value, "%"); isPercent {
		percent, err := strconv.ParseFloat(digits, 64)
		if err != nil {
			return 0, fmt.Errorf("alpha %q: %w", value, err)
		}

		return clamp01(percent / 100), nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("alpha %q: %w", value, err)
	}

	return clamp01(parsed), nil
}

// oklch is a parsed oklch() colour, kept separate from colour because the
// monotonicity test needs the declared lightness and the gamut test needs to
// know whether clipping would occur. Converting to 8-bit first would lose both.
type oklch struct {
	lightness float64
	chroma    float64
	hue       float64
	alpha     float64
	// inGamut records whether the value is representable in sRGB without
	// clipping. Clipping is silent in a browser, so this is the only place the
	// information exists.
	inGamut bool
}

// sRGB converts a parsed oklch to the quantised colour the contrast formula
// consumes. The gamma encode between the linear result and the 8-bit quantise is
// not optional: oklch goes through linear light, and skipping it measures a colour
// several stops darker than the one a browser paints.
func (o oklch) sRGB() colour {
	linear := oklchToLinearSRGB(o.lightness, o.chroma, o.hue)

	return colour{
		r:     quantise(gammaEncode(linear[0])),
		g:     quantise(gammaEncode(linear[1])),
		b:     quantise(gammaEncode(linear[2])),
		alpha: o.alpha,
	}
}

// oklabToLinearSRGB is Björn Ottosson's matrix from Oklab to linear sRGB.
var oklabToLinearSRGB = [3][3]float64{
	{4.0767416621, -3.3077115913, 0.2309699292},
	{-1.2684380046, 2.6097574011, -0.3413193965},
	{-0.0041960863, -0.7034186147, 1.7076147010},
}

// oklabToLMS is the Oklab to LMS matrix, applied before cubing.
var oklabToLMS = [3][3]float64{
	{1, 0.3963377774, 0.2158037573},
	{1, -0.1055613458, -0.0638541728},
	{1, -0.0894841775, -1.2914855480},
}

// oklchMaxChroma is the reference implementation's maximum chroma, which is what
// `chroma: 50%` means.
const oklchMaxChroma = 0.4

// parseOKLCH parses oklch(L C H) and oklch(L C H / A).
//
// The conversion is why this file can hold an oklch primitive layer at all. Oklch
// is not a space a browser measures contrast in — sRGB is — so a gate that
// skipped the conversion would have no way to check a ramp, and a gate that
// guessed it would be worse than none. The matrices are Ottosson's, the reference
// implementation's.
func parseOKLCH(value string) (oklch, error) {
	var parsed oklch

	open := strings.Index(value, "(")
	if open < 0 || !strings.HasSuffix(value, ")") {
		return parsed, fmt.Errorf("%q: oklch() is not closed", value)
	}

	body := strings.TrimSpace(value[open+1 : len(value)-1])

	components, alpha, hasAlpha := strings.Cut(body, "/")
	if !hasAlpha {
		components, alpha = body, ""
	}

	fields := strings.Fields(components)
	if len(fields) != 3 {
		return parsed, fmt.Errorf("%q: oklch() needs lightness, chroma and hue, got %d",
			value, len(fields))
	}

	lightness, err := parseOKLCHLightness(fields[0])
	if err != nil {
		return parsed, fmt.Errorf("%q: %w", value, err)
	}

	chroma, err := parseOKLCHChroma(fields[1])
	if err != nil {
		return parsed, fmt.Errorf("%q: %w", value, err)
	}

	hue, err := parseOKLCHHue(fields[2])
	if err != nil {
		return parsed, fmt.Errorf("%q: %w", value, err)
	}

	parsed = oklch{
		lightness: lightness,
		chroma:    chroma,
		hue:       hue,
		alpha:     1,
		inGamut:   oklchInGamut(lightness, chroma, hue),
	}

	if alpha = strings.TrimSpace(alpha); alpha != "" {
		parsedAlpha, err := parseAlpha(alpha)
		if err != nil {
			return parsed, fmt.Errorf("%q: %w", value, err)
		}

		parsed.alpha = parsedAlpha
	}

	return parsed, nil
}

// parseOKLCHLightness accepts a 0..1 number or a percentage.
func parseOKLCHLightness(field string) (float64, error) {
	if digits, isPercent := strings.CutSuffix(field, "%"); isPercent {
		percent, err := strconv.ParseFloat(digits, 64)
		if err != nil {
			return 0, fmt.Errorf("lightness %q: %w", field, err)
		}

		return percent / 100, nil
	}

	parsed, err := strconv.ParseFloat(field, 64)
	if err != nil {
		return 0, fmt.Errorf("lightness %q: %w", field, err)
	}

	return parsed, nil
}

// parseOKLCHChroma accepts a number or a percentage, where 100% is the
// implementation's maximum rather than an unbounded scale.
func parseOKLCHChroma(field string) (float64, error) {
	if digits, isPercent := strings.CutSuffix(field, "%"); isPercent {
		percent, err := strconv.ParseFloat(digits, 64)
		if err != nil {
			return 0, fmt.Errorf("chroma %q: %w", field, err)
		}

		return percent / 100 * oklchMaxChroma, nil
	}

	parsed, err := strconv.ParseFloat(field, 64)
	if err != nil {
		return 0, fmt.Errorf("chroma %q: %w", field, err)
	}

	return parsed, nil
}

// parseOKLCHHue accepts a bare number of degrees, or one with deg, grad, rad or
// turn. The units are handled because a hue is the one component where a bare
// number is ambiguous in a way a reader cannot check: 0.5 is half a degree
// unadorned and a quarter turn written as a fraction, and both parse.
func parseOKLCHHue(field string) (float64, error) {
	units := []struct {
		suffix string
		scale  float64
	}{
		{"deg", 1},
		{"grad", 0.9},
		{"rad", 180 / math.Pi},
		{"turn", oklchHueUnit},
	}

	for _, unit := range units {
		if !strings.HasSuffix(field, unit.suffix) {
			continue
		}

		parsed, err := strconv.ParseFloat(strings.TrimSuffix(field, unit.suffix), 64)
		if err != nil {
			return 0, fmt.Errorf("hue %q: %w", field, err)
		}

		return math.Mod(parsed*unit.scale, oklchHueUnit), nil
	}

	parsed, err := strconv.ParseFloat(field, 64)
	if err != nil {
		return 0, fmt.Errorf("hue %q: %w", field, err)
	}

	return math.Mod(parsed, oklchHueUnit), nil
}

// oklchToLinearSRGB converts Oklch to linear-light sRGB. The LMS cone responses
// are cubed, which is where Oklab's nonlinearity lives.
func oklchToLinearSRGB(lightness, chroma, hue float64) [3]float64 {
	radians := hue * math.Pi / 180
	lab := [3]float64{lightness, chroma * math.Cos(radians), chroma * math.Sin(radians)}

	var lms [3]float64

	for row := range oklabToLMS {
		for column := range lab {
			lms[row] += oklabToLMS[row][column] * lab[column]
		}
	}

	var out [3]float64

	for row := range oklabToLinearSRGB {
		for column := range lms {
			out[row] += oklabToLinearSRGB[row][column] * lms[column] * lms[column] * lms[column]
		}
	}

	return out
}

// oklchInGamut reports whether an Oklch colour is representable in sRGB without
// clipping. It is checked on the linear values, before the gamma encode and the
// quantise, because clipping is what makes an out-of-gamut value
// indistinguishable from a slightly wrong in-gamut one.
func oklchInGamut(lightness, chroma, hue float64) bool {
	const tolerance = 1e-4

	for _, channel := range oklchToLinearSRGB(lightness, chroma, hue) {
		if channel < -tolerance || channel > 1+tolerance {
			return false
		}
	}

	return true
}

// gammaEncode converts one linear-light sRGB channel to its gamma-encoded form.
func gammaEncode(channel float64) float64 {
	channel = clamp01(channel)
	if channel <= 0.0031308 {
		return 12.92 * channel
	}

	return 1.055*math.Pow(channel, 1/2.4) - 0.055
}

// parseRem parses a rem length, which is what the spacing scale and the radii are
// written in. A unitless zero is accepted, because `0` is what step 0 of a scale
// is and requiring `0rem` for it would be a rule with no purpose. Percentages and
// other units are rejected rather than coerced: the spacing invariant is a
// statement about rem multiples, and a value in another unit would make it a
// statement about nothing.
func parseRem(value string) (float64, error) {
	trimmed := strings.TrimSpace(value)

	if trimmed == "0" {
		return 0, nil
	}

	if !strings.HasSuffix(trimmed, "rem") {
		return 0, fmt.Errorf("%q is not a rem length", value)
	}

	parsed, err := strconv.ParseFloat(strings.TrimSuffix(trimmed, "rem"), 64)
	if err != nil {
		return 0, fmt.Errorf("%q: %w", value, err)
	}

	return parsed, nil
}

// tokenSheet is a parsed tokens.css: the primitive block, one scope per theme
// block, each media block's declarations, and every block with its line, for the
// whole-file rules to walk.
type tokenSheet struct {
	// src is the comment-stripped source. Comments are blanked rather than
	// removed so line numbers stay correct, and so a keyframe named in a
	// comment is a note about one rather than a declaration of one.
	src        string
	primitives map[string]string
	themes     map[themeName]map[string]string
	media      map[string]map[string]string
	mediaLines map[string]int
	// mediaSelectors records the selector each media query wraps, because the
	// selector is half of why the override applies: :root and [data-theme] have
	// the same specificity, so source order decides, and a selector with higher
	// specificity would decide differently.
	mediaSelectors map[string][]string
	themeLines     map[themeName]int
	blocks         []block
}

// primitiveDecls returns the primitive block's declarations in file order.
func (s *tokenSheet) primitiveDecls() []declaration {
	for _, blk := range s.blocks {
		if blk.isPrimitive {
			return blk.decls
		}
	}

	return nil
}

// block is one top-level `{ … }` in the stylesheet, with the prelude that
// selected it and the line it opened on.
//
// decls and properties are kept apart because the distinction is a contract
// rather than a detail. A theme block holds only custom properties, so every
// token in it is something the gate can resolve and measure; a media block may
// also carry a real property, because §6.7's forced-colors rule is about
// forced-color-adjust and there is no custom property for it. Collapsing the two
// would mean either forbidding the one property the record requires, or losing
// the ability to tell a token from a property at all.
type block struct {
	prelude string
	// decls are the custom properties: all of a theme or primitive block's
	// declarations, and the ones a media block carries inside the selector it
	// wraps.
	decls []declaration
	// properties are real CSS properties, which only a media block may declare.
	properties []declaration
	line       int
	// isPrimitive marks the @theme static block, and isTheme marks a
	// [data-theme] block. The two together are the only places a colour literal
	// may appear.
	isPrimitive bool
	isTheme     bool
	// isBridge marks the @theme inline block, which maps every layer-2 and
	// layer-3 name into Tailwind's own namespaces. It is exempt from the
	// decorative-token rule because naming every token is precisely its job.
	isBridge bool
}

// all returns every declaration in the block, custom properties first. The
// whole-file rules care about both and not about the difference between them.
func (b block) all() []declaration {
	return append(
		append(make([]declaration, 0, len(b.decls)+len(b.properties)), b.decls...),
		b.properties...)
}

// declaration is one property or custom property, with the value it was given
// and the line it ended on.
type declaration struct {
	name  string
	value string
	line  int
}

// isCustom reports whether this is a custom property. A theme or primitive block
// may hold nothing else, because a real property there is not a token and the
// gate can neither resolve it nor hold it to a floor.
func (d declaration) isCustom() bool {
	return strings.HasPrefix(d.name, "--")
}

// loadTokens reads and parses the stylesheet under test.
func loadTokens(t *testing.T) tokenSheet {
	t.Helper()

	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("read %s: %v", tokenFile, err)
	}

	sheet, err := parseStylesheet(string(raw))
	if err != nil {
		t.Fatalf("parse %s: %v", tokenFile, err)
	}

	return sheet
}

// frame is one open `{ … }` while the file is being walked.
type frame struct {
	prelude string
	line    int
	decls   []declaration
	// children are the rules nested inside this one. Only an @media block may
	// have them, because only an @media block has to wrap a selector.
	children []*frame
}

// maxBlockDepth is two: an @media block and the selector inside it. Nothing in
// this file nests further, and a third level is a construct the gate has no rule
// about.
const maxBlockDepth = 2

// parseStylesheet walks the whole file. It is a parser rather than a set of
// regexes because every rule below is a rule about the *structure* of the file —
// two theme blocks, one primitive block, flat declarations, one block per media
// query — and a regex cannot tell a declaration inside a comment from one that is
// live, or a declaration inside a nested rule from one at the top level.
//
// It refuses anything it does not recognise rather than skipping it. A stylesheet
// with a construct this parser cannot see is a stylesheet the gate has not
// verified, and the failure mode of a permissive parser here is a green build on
// an unverified file.
func parseStylesheet(src string) (tokenSheet, error) {
	stripped, err := blankComments(src)
	if err != nil {
		return tokenSheet{}, err
	}

	sheet := tokenSheet{
		src:            stripped,
		primitives:     map[string]string{},
		themes:         map[themeName]map[string]string{},
		media:          map[string]map[string]string{},
		mediaLines:     map[string]int{},
		mediaSelectors: map[string][]string{},
		themeLines:     map[themeName]int{},
	}

	var stack []*frame

	var pending strings.Builder

	line := 1

	for idx := 0; idx < len(stripped); idx++ {
		char := stripped[idx]

		switch char {
		case '\n':
			line++
			pending.WriteByte(' ')

		case '"', '\'':
			// A string literal is copied through whole, so a brace, a semicolon
			// or a quote inside one cannot end a block early. A font stack is
			// the reason this is here.
			quote := char

			pending.WriteByte(char)
			idx++

			for ; idx < len(stripped) && stripped[idx] != quote; idx++ {
				if stripped[idx] == '\\' {
					idx++

					if idx >= len(stripped) {
						break
					}
				}

				if stripped[idx] == '\n' {
					line++
				}

				pending.WriteByte(stripped[idx])
			}

			if idx < len(stripped) {
				pending.WriteByte(quote)
			}

		case '{':
			if len(stack) >= maxBlockDepth {
				return tokenSheet{}, fmt.Errorf(
					"line %d: %q nests more than %d levels deep; the deepest construct this "+
						"parser understands is an @media block wrapping a selector",
					line, pending.String(), maxBlockDepth)
			}

			opened := &frame{prelude: strings.TrimSpace(pending.String()), line: line}
			pending.Reset()

			if len(stack) > 0 {
				open := stack[len(stack)-1]
				open.children = append(open.children, opened)
			}

			stack = append(stack, opened)

		case '}':
			if len(stack) == 0 {
				return tokenSheet{}, fmt.Errorf("line %d: unmatched }", line)
			}

			closed := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			pending.Reset()

			if len(stack) == 0 {
				if err := sheet.add(closed); err != nil {
					return tokenSheet{}, err
				}
			}

		case ';':
			text := strings.TrimSpace(pending.String())
			pending.Reset()

			if len(stack) == 0 {
				// A top-level at-rule, such as an @import in the entry point.
				continue
			}

			if text == "" {
				continue
			}

			decl, err := parseDeclaration(text, line)
			if err != nil {
				return tokenSheet{}, fmt.Errorf("line %d: %w", line, err)
			}

			open := stack[len(stack)-1]
			open.decls = append(open.decls, decl)

		default:
			pending.WriteByte(char)
		}
	}

	if len(stack) != 0 {
		return tokenSheet{}, fmt.Errorf("%q is never closed", stack[len(stack)-1].prelude)
	}

	return sheet, nil
}

// add files one top-level block under the category its prelude names.
//
// Flatness is a contract for the two blocks that hold tokens, and it is checked
// rather than assumed: a token inside a nested rule in a theme block would be
// invisible to every other assertion here, so the whole file would appear to
// pass while a token went unmeasured.
func (s *tokenSheet) add(open *frame) error {
	prelude := open.prelude
	kind := strings.ToLower(prelude)

	if len(open.children) > 0 && !strings.HasPrefix(kind, "@media") {
		return fmt.Errorf(
			"line %d: %q contains a nested rule; every token must be a flat declaration at the top "+
				"level of its block, or a token in a nested rule would be skipped and the gate "+
				"would not see it",
			open.line,
			prelude,
		)
	}

	switch {
	case kind == `[data-theme="light"]`, kind == `[data-theme="dark"]`:
		name := themeName(strings.Trim(strings.TrimPrefix(kind, "[data-theme="), `"]`))

		if _, duplicate := s.themes[name]; duplicate {
			return fmt.Errorf(
				"line %d: a second %q block; §6.1 defines each theme once, and with two blocks the "+
					"merge order decides the values",
				open.line,
				prelude,
			)
		}

		scope, err := scopeOf(open.decls, prelude)
		if err != nil {
			return err
		}

		s.themes[name] = scope
		s.themeLines[name] = open.line
		s.blocks = append(
			s.blocks,
			block{prelude: prelude, decls: open.decls, line: open.line, isTheme: true},
		)

		return nil

	case strings.HasPrefix(kind, "@theme"):
		// `@theme static` is the primitive block. `@theme inline` is the bridge
		// into Tailwind's namespaces, and its values are all var() references
		// rather than a palette, so it is walked for structure and not read as
		// a set of tokens.
		if !strings.Contains(kind, "static") {
			s.blocks = append(s.blocks, block{
				prelude: prelude, decls: open.decls, line: open.line, isBridge: true,
			})

			return nil
		}

		if len(s.primitives) > 0 {
			return fmt.Errorf("line %d: a second @theme static block; §6.1's primitive layer is "+
				"one block, and splitting it makes --spacing and the ramps two declarations of the "+
				"same scales", open.line)
		}

		scope, err := scopeOf(open.decls, prelude)
		if err != nil {
			return err
		}

		s.primitives = scope
		s.blocks = append(s.blocks, block{
			prelude: prelude, decls: open.decls, line: open.line, isPrimitive: true,
		})

		return nil

	case strings.HasPrefix(kind, "@media"):
		return s.addMedia(open, prelude, kind)
	}

	return fmt.Errorf(
		"line %d: %q is neither a theme block, the primitive @theme block nor an @media query; "+
			"this parser reads the whole file and will not guess what an unrecognised block is for",
		open.line, prelude)
}

// addMedia files one media block. Its declarations live in the selector nested
// inside it, because that is what a media query has to wrap, and the selector is
// recorded rather than discarded: §6.7's overrides only win because they share
// specificity with the theme blocks and come later in the file, so the selector
// is part of what the gate has to check.
func (s *tokenSheet) addMedia(open *frame, prelude, kind string) error {
	query := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(kind, "@media"), ""))

	if _, duplicate := s.media[query]; duplicate {
		return fmt.Errorf("line %d: a second @media %s block; a duplicated media query means one "+
			"of the two is dead, and which one is not visible from the file", open.line, query)
	}

	if len(open.children) == 0 {
		return fmt.Errorf("line %d: @media %s contains no rule; a media query has to wrap a "+
			"selector, so this block overrides nothing", open.line, query)
	}

	if len(open.decls) > 0 {
		return fmt.Errorf("line %d: @media %s declares %s at its own level; the declarations have "+
			"to sit inside the selector the query wraps, or the browser ignores them and the "+
			"§6.7 override silently does not apply",
			open.line, query, open.decls[0].name)
	}

	scope := make(map[string]string)

	var decls, properties []declaration

	for _, child := range open.children {
		for _, decl := range child.decls {
			if !decl.isCustom() {
				// A media block is the one place a real property belongs, and
				// §6.7 needs one: forced-color-adjust has no custom property
				// equivalent. It is kept apart from the tokens so the palette
				// stays a palette.
				properties = append(properties, decl)

				continue
			}

			if _, duplicate := scope[decl.name]; duplicate {
				return fmt.Errorf("line %d: %s is declared twice inside @media %s",
					decl.line, decl.name, query)
			}

			scope[decl.name] = decl.value
			decls = append(decls, decl)
		}

		s.mediaSelectors[query] = append(s.mediaSelectors[query], child.prelude)
	}

	s.media[query] = scope
	s.mediaLines[query] = open.line
	s.blocks = append(s.blocks, block{
		prelude: prelude, decls: decls, properties: properties, line: open.line,
	})

	return nil
}

// scopeOf turns a palette block's declarations into a name/value map, refusing
// both a duplicate and a real property.
//
// A duplicate is refused rather than resolved: the second declaration silently
// wins in a browser, and which one is the theme is not something a reader can see
// from the file. Silently keeping the last one here would reproduce exactly that
// invisibility. A real property is refused because a theme block is a palette, and
// every declaration in a palette has to be a token the gate can resolve.
func scopeOf(decls []declaration, prelude string) (map[string]string, error) {
	scope := make(map[string]string, len(decls))

	for _, decl := range decls {
		if !decl.isCustom() {
			return nil, fmt.Errorf("line %d: %q is a property, not a custom property; %q holds "+
				"only tokens, so a property there is something the gate can neither resolve nor "+
				"hold to a floor", decl.line, decl.name, prelude)
		}

		if _, duplicate := scope[decl.name]; duplicate {
			return nil, fmt.Errorf("line %d: %s is declared twice in %q; the second declaration "+
				"silently wins, and which one is the theme is not something a reader can see",
				decl.line, decl.name, prelude)
		}

		scope[decl.name] = decl.value
	}

	return scope, nil
}

// parseDeclaration parses one `name: value` pair. Whether the name is a custom
// property is not decided here: a property is a legitimate declaration inside an
// @media block (forced-color-adjust: auto is one, and §6.7 needs it) and not in
// a theme block, so the callers apply that rule with more context than this
// function has.
func parseDeclaration(text string, line int) (declaration, error) {
	name, value, found := strings.Cut(text, ":")
	if !found {
		return declaration{}, fmt.Errorf("%q is not a `name: value` declaration", text)
	}

	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)

	if name == "" {
		return declaration{}, fmt.Errorf("%q has no property name", text)
	}

	if value == "" {
		return declaration{}, fmt.Errorf("%s has no value", name)
	}

	return declaration{name: name, value: value, line: line}, nil
}

// blankComments replaces every comment with spaces, preserving newlines so that
// line numbers, and therefore every failure message, stay correct.
func blankComments(src string) (string, error) {
	var out strings.Builder

	out.Grow(len(src))

	for idx := 0; idx < len(src); idx++ {
		if src[idx] != '/' || idx+1 >= len(src) || src[idx+1] != '*' {
			out.WriteByte(src[idx])

			continue
		}

		end := strings.Index(src[idx+2:], "*/")
		if end < 0 {
			return "", fmt.Errorf("unterminated comment opening at line %d", lineOf(src, idx))
		}

		for _, char := range src[idx : idx+2+end+2] {
			if char == '\n' {
				out.WriteByte('\n')
			} else {
				out.WriteByte(' ')
			}
		}

		idx += 2 + end + 1
	}

	return out.String(), nil
}

// lineOf counts the newlines before an offset, so a byte position in the
// original source can be reported as a line.
func lineOf(src string, offset int) int {
	return 1 + strings.Count(src[:offset], "\n")
}
