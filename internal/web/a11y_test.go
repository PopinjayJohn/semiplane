//nolint:misspell // CSS identifiers, not prose; the rationale follows the package clause
package web_test

// The stylesheet-side half of UI §10.2 and §10.6.
//
// The document-side half is `internal/httpapi/shell_render_test.go`, and the two
// are separate files because they are separate claims. §10.2's list has seven
// rules and six of them are properties of a rendered document; the seventh — "no
// `outline: none` without a replacement" — is a property of the stylesheet, and a
// per-route walk of the markup can never see it, because the offending rule is
// in a file the browser reads and the DOM does not contain. §10.6's target-size
// *minimums* are the same kind of claim: the markup carries the `.target` class
// and the stylesheet decides what that class means.
//
// # Why it reads the built stylesheet and not the source
//
// The built file. `make css` minifies and concatenates, so the source of one
// stylesheet and the bytes the browser receives are different artefacts — and a
// claim about "what the shell enforces" is a claim about the bytes. A rule can be
// in a source file and dropped by the build (a selector the scanner cannot parse,
// a rule inside a layer that loses the cascade), and the only way to catch that is
// to read what ships. That is why `grep -c data-theme internal/web/static/dist/
// app.css` is a meaningful check and the same grep against `css/tokens.css` is
// not.
//
// The gate therefore fails when the stylesheet has not been built, rather than
// skipping. A skipped stylesheet gate is an unstyled product that reports itself
// green, which is the exact failure this file exists to make impossible.
//

// This file's prose uses the UK spelling throughout. The misspell suppression on
// the package clause above is for CSS identifiers only: `forced-colors`,
// `prefers-color-scheme`, `color-scheme` and the `--color-*` theme names are
// spelled the US way by CSS itself, and a linter configured for British English
// objects to all of them. It cannot be allowed to rewrite the name of a media
// feature or of a token this file is asserting the presence of — the alternative is
// a `nolint` at each of the eight call sites, and a suppression that has to be
// repeated is one that eventually gets deleted by somebody tidying.

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// builtStylesheet is the bytes the server serves at /assets/app.css.
const builtStylesheet = "static/dist/app.css"

// declarations strips comments, so a mechanical check reads declarations and not
// the prose about them.
//
// Half of what these stylesheets are for is explaining why, and several of the
// explanations name the very properties the assertions ban — shell.css's own
// header says `transform: scale()` is prohibited, and a check for `scale(` that
// read the comments would fail on the sentence prohibiting it.
func declarations(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

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

// built returns the built stylesheet, failing rather than skipping when it is
// absent.
//
// The message names the fix, for the same reason the Makefile's `build` target
// does: the most common cause is a shell that has not run `make css`, and
// "file not found" sends a reader looking for a permissions problem.
func built(t *testing.T) string {
	t.Helper()

	if _, err := os.Stat(builtStylesheet); err != nil {
		t.Fatalf("%s is missing: %v\nrun: make css", builtStylesheet, err)
	}

	return declarations(t, builtStylesheet)
}

// TestTheBuiltStylesheetCarriesTheTokensAndTheGrid is the assertion the whole
// phase rested on and could not make.
//
// Until `app.css` imported `tokens.css`, `shell.css` and `tv.css`, the build
// emitted only Tailwind's base layer: no grid, no token, no `.target`, no
// `.skip-link`. Every §10.2 and §10.6 gate in the repository passed — correctly,
// vacuously — against markup that rendered unstyled. "The shell renders" and "the
// shell is unstyled" are the same observation from every angle a test can take,
// unless the test asks whether the bytes exist.
//
// So this asks. The counts are floors rather than exact numbers: they say "the
// layers are present and not truncated", and a future revision that adds a token
// or a tier does not have to edit this.
func TestTheBuiltStylesheetCarriesTheTokensAndTheGrid(t *testing.T) {
	t.Parallel()

	css := built(t)

	t.Run("TokenLayer", func(t *testing.T) {
		t.Parallel()

		// The two theme blocks, one per §6.2 and §6.3. The minifier strips
		// attribute-value quotes, so the selectors are matched unquoted — a
		// quoted grep reads zero against a correct build, which is how this class
		// of check comes to be ignored.
		for _, theme := range []string{"light", "dark"} {
			if !strings.Contains(css, "[data-theme="+theme+"]") {
				t.Errorf("the built stylesheet carries no [data-theme=%s] block; the "+
					"token layer is not in the build (UI §6.2, §6.3)", theme)
			}
		}

		// A representative token from each of §6.1's three layers. The primitive
		// one is the plugin contract (§4.11.1: a *not yet used* colour, which
		// `@theme static` exists to emit), so its absence is the specific thing
		// that goes wrong when `static` is dropped.
		for _, token := range []string{
			"--color-accent-600:", // layer 1, the primitive ramp
			"--surface:",          // layer 2, a semantic token
			"--btn-primary-bg:",   // layer 3, a component token
		} {
			if !strings.Contains(css, token) {
				t.Errorf("the built stylesheet does not declare %s; a token layer "+
					"is missing from the build (UI §6.1)", token)
			}
		}
	})

	t.Run("Grid", func(t *testing.T) {
		t.Parallel()

		// One `grid-template-areas` per tier that declares one: the compact base
		// rule, the pre-campaign form, medium, large/wide, and TV. Five is the
		// count §3.1 through §3.4 produce.
		if got := strings.Count(css, "grid-template-areas"); got < 5 {
			t.Errorf("the built stylesheet declares %d grid-template-areas rules, "+
				"want at least 5; the shell's per-tier grids are not all in the "+
				"build (UI §3.1, §3.4)", got)
		}

		// Every tier's selector. §3.7's whole responsive system hangs off one
		// root attribute, so a tier with no rule is a tier that renders at
		// whatever the previous tier did.
		for _, tier := range []string{"medium", "large", "wide", "tv", "tv-wide", "compact", "compact-short"} {
			if !strings.Contains(css, "data-ui="+tier) {
				t.Errorf("the built stylesheet carries no rule for [data-ui=%s]; "+
					"the resolver can write it and nothing would lay the shell out "+
					"for it (UI §3.7)", tier)
			}
		}
	})

	t.Run("TargetContract", func(t *testing.T) {
		t.Parallel()

		// §7.3's two minimums, in the exact form the record specifies. Asserted
		// as the *declaration* rather than as a number, because the number is
		// per tier and the tier is data.
		if !strings.Contains(
			css,
			".target{min-inline-size:var(--target-min);min-block-size:var(--target-min)}",
		) {
			if !strings.Contains(css, "min-inline-size:var(--target-min)") ||
				!strings.Contains(css, "min-block-size:var(--target-min)") {
				t.Error("the built stylesheet's .target rule does not set both " +
					"min-inline-size and min-block-size from --target-min; §7.3 " +
					"enforces the minimum by construction and a rule that sets only " +
					"one axis leaves the other at 0")
			}
		}
	})

	t.Run("FocusRing", func(t *testing.T) {
		t.Parallel()

		// §6.5's two tones. One alone disappears when the focused control shares
		// a hue with the ring.
		if !strings.Contains(css, "--focus-ring-offset") {
			t.Error("the built stylesheet does not reference --focus-ring-offset; " +
				"§6.5's focus ring is two-tone, and a ring with one tone vanishes " +
				"on a same-hue control")
		}
	})
}

// TestTheTargetMinimumsMeetTheSpecifiedFloors is §10.6's numeric half, and §7.3's
// table, read out of the built stylesheet.
//
// §7.3 has three rows and three input methods:
//
//	pointer   24 x 24 CSS px — WCAG 2.2 SC 2.5.8 (AA), "a floor, not the goal"
//	touch     44 x 44 CSS px — WCAG 2.5.5 (AAA), Apple HIG 44pt, Material 48dp
//	TV        56px along the movement axis, 16px gutter — no WCAG criterion
//
// `--target-min` is resolved at a 16px root, which is the default every browser
// ships and the assumption `TestEveryMediaQueryMatchesADeclaredConstant` makes
// too. A root font size other than 16px would change the arithmetic; §5.1 pins
// `html { font-size: 100% }`, which is what keeps the two in step.
func TestTheTargetMinimumsMeetTheSpecifiedFloors(t *testing.T) {
	t.Parallel()

	css := built(t)

	// §5.1's table, verbatim. The key is the tier selector the value is declared
	// under, and the ordering matters: the first block whose selector matches
	// `:root` alone is the compact default, because a visitor with no script has
	// no `data-ui` at all and §3.7 promises them coherence rather than a tier.
	rows := []struct {
		name     string
		selector string
		min      float64
		why      string
	}{
		{
			"compact",
			":root",
			44,
			"§5.1 / §7.3's touch minimum, and the tier a no-script visitor gets",
		},
		{
			"medium, large, wide",
			`[data-ui=medium]`,
			36,
			"§5.1, still 1.5x WCAG 2.2 SC 2.5.8's 24px floor",
		},
		{
			"tv, tv-wide",
			`[data-ui=tv]`,
			56,
			"§7.3's movement axis; no WCAG criterion covers 10-foot",
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			got, ok := declaredTargetMin(t, css, row.selector)
			if !ok {
				t.Fatalf("the built stylesheet declares no --target-min under %s; "+
					"§5.1 gives every tier its own value", row.selector)
			}

			if got < row.min {
				t.Errorf("--target-min under %s is %gpx, want at least %gpx: %s "+
					"(UI §7.3, §5.1)", row.selector, got, row.min, row.why)
			}
		})
	}

	// §7.3's TV separation, which is the number the D-pad walkthrough measures:
	// a mis-aimed remote press must not land on a neighbour.
	t.Run("tvSeparation", func(t *testing.T) {
		t.Parallel()

		gap, ok := declaredTargetGap(t, css)
		if !ok {
			t.Fatal("the built stylesheet declares no --target-gap")
		}

		if gap < 16 {
			t.Errorf("--target-gap is %gpx, want at least 16px on TV; a mis-aimed "+
				"remote press must not land on a neighbour (UI §7.3, §10.5)", gap)
		}
	})
}

// declaredTargetMin reads `--target-min` as declared under a selector prefix.
//
// The prefix rather than the exact selector, because the built stylesheet merges
// selectors and drops attribute-value quotes, so `[data-ui=tv]` matches the
// `tv-wide` rule too — which is correct, since both declare the same value.
func declaredTargetMin(t *testing.T, css, selectorPrefix string) (float64, bool) {
	t.Helper()

	return declaredLength(t, css, selectorPrefix, "--target-min")
}

// declaredTargetGap reads `--target-gap` from the tv block.
func declaredTargetGap(t *testing.T, css string) (float64, bool) {
	t.Helper()

	return declaredLength(t, css, `[data-ui=tv]`, "--target-gap")
}

// lengthPattern matches a length-valued declaration's numeric part.
//
// The leading-zero-optional group is not cosmetic: the minifier writes `.5rem`,
// not `0.5rem`, so `[0-9.]+` alone never matches half the values in the built
// stylesheet. A test that silently stops finding declarations is worse than one
// that fails, because it reports a floor as satisfied.
var lengthPattern = regexp.MustCompile(`((?:\d*\.)?\d+)(rem|px)\b`)

// declaredLength resolves a length-valued custom property declared under a
// selector prefix, to pixels at a 16px root.
//
// The search runs **from the declaration back to its selector**, not from the
// selector forward. Forward is the obvious direction and it is wrong: `:root`
// appears many times in the built stylesheet — tokens.css alone declares five —
// so `strings.Index(css, ":root")` finds a `prefers-reduced-motion` block that
// declares nothing this test is looking for. Going backwards from the
// declaration means the selector is the one that actually governs the value, which
// is the only selector the assertion can honestly be about.
//
// The per-name regex is **anchored** at the offset being examined. That detail is
// load-bearing: `--rail-right` is a prefix of `--rail-right-md`,
// `--rail-right-tv` and `--rail-right-tv-wide`, so an unanchored search starting
// at one of those can slide forward to a *different* property's declaration and
// then read that declaration's selector — which is how this helper first reported
// a 26rem rail as 30rem, by starting at `--rail-right-tv` and matching the
// `--rail-right:` several declarations later.
//
// A `calc()` value is not resolvable here without a full cascade, so this
// reports the declared rem or px and declines on anything else rather than
// guessing.
func declaredLength(t *testing.T, css, selectorPrefix, name string) (float64, bool) {
	t.Helper()

	declaration := regexp.MustCompile(`^` + regexp.QuoteMeta(name) + `\s*:`)

	for _, at := range occurrences(css, name) {
		match := declaration.FindStringIndex(css[at:])
		if match == nil {
			continue
		}

		value := lengthPattern.FindStringSubmatch(css[at+match[1]:])
		if value == nil {
			// Declared as something this helper cannot resolve — a `calc()`, a
			// `clamp()`, or a reference to another custom property. Not a failure:
			// §5.1's table is stated in rem and a future revision is free to
			// express a step differently. The rule that catches an unusable value
			// is `.target`'s, asserted above.
			continue
		}

		selector, ok := governingSelector(css, at)
		if !ok || !strings.Contains(selector, selectorPrefix) {
			continue
		}

		pixels, err := strconv.ParseFloat(value[1], 64)
		if err != nil {
			t.Fatalf("%s has an unparseable value %q: %v", name, value[1], err)
		}

		if value[2] == "rem" {
			pixels *= 16
		}

		return pixels, true
	}

	return 0, false
}

// governingSelector returns the selector list that governs a declaration at an
// offset, by walking back to the nearest brace.
//
// The nearest brace, not the nearest `}`: a declaration inside an `@media` block
// is governed by the inner rule's selector, and the media condition is not part
// of it. Walking back past the `{` of the `@media` is exactly what this does, and
// it is why the tv-mode test's "is there a `@media` in this block" check can be
// written against the declarations rather than against the condition.
func governingSelector(css string, at int) (string, bool) {
	before := css[:at]

	open := strings.LastIndex(before, "{")
	if open < 0 {
		return "", false
	}

	cut := strings.LastIndexAny(before[:open], "{}")

	return strings.TrimSpace(before[cut+1 : open]), true
}

// TestNoRuleRemovesAFocusIndicatorWithoutAReplacement is §10.2's fourth rule and
// §7.10's "outline: none without a visible replacement is prohibited".
//
// The check is deliberately narrow and deliberately not a ban on the property.
// `outline: none` is legitimate in exactly one case: an element that draws its
// own indicator, and `TestTheFocusRingExistsToReplaceIt` is what confirms one
// does. What is prohibited is removing the indicator and nothing putting it
// back, and that is a property of the *pair* rather than of either rule alone — so
// the assertion is that `outline:none` does not appear, and separately that the
// two-tone ring it would have replaced is present.
//
// This is also the rule that cannot be checked from the DOM. The markup says
// nothing about outlines; the browser reads a file the DOM does not contain.
func TestNoRuleRemovesAFocusIndicatorWithoutAReplacement(t *testing.T) {
	t.Parallel()

	css := built(t)

	removes := regexp.MustCompile(`outline\s*:\s*(none|0)`)
	if match := removes.FindString(css); match != "" {
		t.Errorf("the built stylesheet sets %q; §7.10 prohibits removing a focus "+
			"indicator without a visible replacement, and the replacement this "+
			"stylesheet uses is the two-tone ring rather than a suppression",
			strings.TrimSpace(match))
	}

	if !strings.Contains(css, ":focus-visible") {
		t.Error("the built stylesheet has no :focus-visible rule at all; " +
			"§7.4 requires :focus-visible for pointer tiers, so a document with " +
			"no visible replacement has no way to gain one")
	}
}

// TestTheTvModeIsAModeAndNotAWidthBand is UI §3.3, asserted against the built
// stylesheet rather than against `tv.css`.
//
// The distinction is the whole reason tv.css is a separate file, and it is a
// structural property of the CSS rather than a comment: **every rule in tv.css is
// scoped by `[data-ui="tv"]` or `[data-ui="tv-wide"]`, and not one of them is
// inside a `@media` width condition.** A width band would make a 12.9" tablet in
// landscape a television, which is the misclassification §3.3 refuses to accept in
// exchange for not shipping a stale user-agent list.
//
// Reading the built file rather than the source also covers the failure this could
// not otherwise catch: if `@media` wrapped the mode's rules in the build, the
// source would look correct and the shipped CSS would not.
func TestTheTvModeIsAModeAndNotAWidthBand(t *testing.T) {
	t.Parallel()

	css := built(t)

	// §7.4's TV exception to `:focus-visible` has to be there: it is the rule
	// that makes the mode usable, and it is invisible in a rendered diff.
	if !strings.Contains(css, "[data-ui=tv]") && !strings.Contains(css, "[data-ui=tv-wide]") {
		t.Fatal("the built stylesheet carries no [data-ui=tv] rule; the mode is not " +
			"in the build (UI §3.3)")
	}

	// The structural half: **no TV rule is nested inside a media condition that
	// describes geometry or input capability.** A mode selected by `min-width`,
	// `orientation`, `pointer` or `hover` is a width band wearing a mode's name,
	// and §3.3's whole argument is that those signals cannot tell a 55" television
	// from a 12.9" tablet in landscape.
	//
	// The nesting is resolved by a brace stack rather than by looking forward from
	// the selector, because looking forward is the wrong direction: the shape this
	// has to catch is a *correct-looking* rule wrapped in
	// `@media (min-width: 137.5rem) { … }`, and the declarations inside it contain
	// no trace of the condition. An earlier version of this test scanned forward
	// and a mutation that did exactly that wrapping passed it.
	//
	// `prefers-*` and `forced-colors` are deliberately *not* in the banned set.
	// §3.3 requires them to be honoured in every mode, and tv.css has three rules
	// inside them — they change an indicator and a border, never the layout, and
	// that is the distinction the next assertion makes.
	for _, selector := range []string{"[data-ui=tv]", "[data-ui=tv-wide]"} {
		for _, condition := range enclosingConditions(css, selector) {
			if banned := geometryFeature(condition); banned != "" {
				t.Errorf("a %s rule sits inside @media (%s); %q selects the mode "+
					"by geometry or input capability, and §3.3's point is that a "+
					"television and a tablet in landscape are indistinguishable to "+
					"CSS. The mode is the [data-ui] attribute",
					selector, condition, banned)
			}
		}
	}
}

// geometryFeatures are the media features that describe the *device* rather than
// the reader's preference.
//
// A rule conditioned on any of them is choosing a layout from what the hardware
// looks like, which is the inference §3.3 forbids. `prefers-reduced-motion`,
// `prefers-contrast`, `forced-colors` and `prefers-color-scheme` are absent
// deliberately: they describe a reader, and §3.3 requires every mode to honour
// them.
var geometryFeatures = []string{
	"min-width", "max-width",
	"min-height", "max-height",
	"orientation",
	"pointer", "hover", "any-pointer", "any-hover",
	"resolution",
	"device-width", "device-height",
	"aspect-ratio",
}

// geometryFeature returns the first geometry feature named in a condition, or "".
func geometryFeature(condition string) string {
	lowered := strings.ToLower(condition)

	for _, feature := range geometryFeatures {
		if strings.Contains(lowered, feature) {
			return feature
		}
	}

	return ""
}

// enclosingConditions returns the `@media` conditions enclosing every rule whose
// selector names the given attribute, outermost first.
//
// A brace stack, because nesting is the thing being asked about. Each `{` records
// whether the prelude that opened it names an `@media`, and a rule is inside every
// entry currently on the stack.
//
// The selector is searched for **in the prelude only** — the text between a `}` or
// `{` and the `{` that opens the next block — and never in the whole stylesheet.
// That is the structural reason this works, and it is why an earlier version that
// scanned every `[` in the document did not: an attribute selector can follow a
// type selector with no delimiter, so `:root[data-ui="tv"]` puts `[` immediately
// after the letters `t`, and any rule for "is this `[` in a selector position?"
// that works on adjacent characters rejects it. The prelude has no such problem —
// a selector can only ever appear there, and a declaration value can only ever
// appear in a body — so the question does not have to be answered at all.
func enclosingConditions(css, selector string) []string {
	var conditions []string

	// preludeStart is the index just after the last `}` or `{`, which is where the
	// text that will open the next block begins.
	preludeStart := 0

	stack := []string{}

	for index := 0; index < len(css); index++ {
		switch css[index] {
		case '{':
			prelude := css[preludeStart:index]

			condition := ""

			if _, after, isMedia := strings.Cut(prelude, "@media"); isMedia {
				condition = strings.TrimSpace(after)
			}

			// The block being opened is `prelude`. It sits inside whatever is
			// already on the stack, so its enclosing conditions are the stack's
			// non-empty entries — and its own condition applies to its body, not
			// to its selector.
			if strings.Contains(prelude, selector) {
				for _, enclosing := range stack {
					if enclosing != "" {
						conditions = append(conditions, enclosing)
					}
				}
			}

			stack = append(stack, condition)
			preludeStart = index + 1
		case '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}

			preludeStart = index + 1
		}
	}

	return conditions
}

// TestTheTvRailWidensAtTvWide is §3.4's last bullet, asserted on the built
// stylesheet.
//
// "At 3840 CSS px (`--breakpoint-tv-wide: 137.5rem`), `--type-scale` rises to 1.75
// and `--rail-right` to 30rem."
//
// The finding this test exists for was a **real defect**, found by the viewport
// sweep rather than by reading: at 3840 × 2160 the rail measured 416px, which is
// 26rem — `--rail-right-tv` — and not the 30rem the record specifies. The TV grid
// read `--rail-right-tv` as a literal in `grid-template-columns`, so the
// `:root[data-ui="tv-wide"] { --rail-right: … }` declaration the record's sentence
// is written in terms of never reached the grid.
//
// Nothing caught it, and the reason is worth recording. `--rail-right` was both
// declared *and* read somewhere — `tv.css`'s navigation strip reads it for its
// inline edge — so the coupling test passed over a property that was dead in the
// one place the record's sentence is about. A property that is used is not
// necessarily used *where it matters*, and only a test that reads the value a
// browser would compute can tell those apart.
//
// So this asserts the resolved length per tier, not the presence of a
// declaration. That is the difference between a gate and an inventory.
func TestTheTvRailWidensAtTvWide(t *testing.T) {
	t.Parallel()

	css := built(t)

	// §3.4's sentence is about a *chain* — a base constant, a per-tier
	// assignment, and a grid that reads the per-tier name — so the chain is
	// asserted link by link rather than as one resolved number. Resolving it would
	// mean emulating the cascade, and an emulation is a second implementation of
	// CSS that can be wrong in its own way; three literal assertions cannot.
	links := []struct {
		what string
		// selector is the rule the declaration must appear under, as a substring.
		selector string
		declare  string
	}{
		{
			what:     "§3.4's base rail",
			selector: ":root",
			declare:  "--rail-right-tv:26rem",
		},
		{
			what:     "§3.4's tv-wide rail",
			selector: ":root",
			declare:  "--rail-right-tv-wide:30rem",
		},
		{
			what:     "the tv tier's assignment",
			selector: ":root[data-ui=tv]",
			declare:  "--rail-right:var(--rail-right-tv)",
		},
		{
			what:     "§3.4's last bullet: the tv-wide assignment",
			selector: ":root[data-ui=tv-wide]",
			declare:  "--rail-right:var(--rail-right-tv-wide)",
		},
		{
			what:     "§3.4's tv type scale",
			selector: ":root[data-ui=tv]",
			declare:  "--type-scale:1.5",
		},
		{
			what:     "§3.4's tv-wide type scale",
			selector: ":root[data-ui=tv-wide]",
			declare:  "--type-scale:1.75",
		},
	}

	for _, link := range links {
		t.Run(link.what, func(t *testing.T) {
			t.Parallel()

			if !declarationUnder(css, link.selector, link.declare) {
				t.Errorf("the built stylesheet has no rule under %q declaring %q; "+
					"§3.4 states the TV rail and type scale as a chain from a base "+
					"constant to a per-tier assignment, and a break anywhere in it "+
					"leaves the tier on the laptop's values",
					link.selector, link.declare)
			}
		})
	}

	// And the link that was actually broken: the grid must read the *assigned*
	// name. Every assertion above would still pass if the grid read
	// `--rail-right-tv` directly while `--rail-right` held the right value, and
	// that is exactly the state the sweep found — 26rem at 3840px.
	tvGrid, ok := gridRuleFor(css, "[data-ui=tv]")
	if !ok {
		t.Fatal("the built stylesheet has no grid rule selecting the tv tier")
	}

	if !strings.Contains(tvGrid, "var(--rail-right)") {
		t.Errorf("the TV grid does not read --rail-right: %s. §3.4 states the "+
			"rail's width as a change to that property, so a grid reading "+
			"--rail-right-tv makes tv-wide unreachable",
			collapse(tvGrid))
	}

	if strings.Contains(tvGrid, "var(--rail-right-tv)") {
		t.Errorf("the TV grid reads --rail-right-tv directly: %s; that is the "+
			"literal the tv-wide override cannot reach", collapse(tvGrid))
	}
}

// declarationUnder reports whether a rule whose selector contains
// `selectorFragment` declares `declaration`.
//
// Compared against the minified text, where the minifier writes `--name:value`
// with no space and drops attribute-value quotes. Whitespace is therefore
// normalised away rather than spelled in the pattern, and the expected string is
// written the way the output writes it — which is checked against a real build
// rather than reasoned about.
func declarationUnder(css, selectorFragment, declaration string) bool {
	want := compact(declaration)

	for _, rule := range eachRule(css) {
		if !strings.Contains(rule.selector, selectorFragment) {
			continue
		}

		if strings.Contains(compact(rule.body), want) {
			return true
		}
	}

	return false
}

// compact removes whitespace, which the minifier does anyway.
func compact(text string) string {
	return strings.Join(strings.Fields(text), "")
}

// cssRule is one rule: its selector list and its declarations.
type cssRule struct {
	selector string
	body     string
}

// eachRule walks the stylesheet's top-level and nested rules, in order.
//
// A brace-stack walk rather than a regex, for the reason §3.7's no-JS resolver
// work found: a `}` inside a declaration value or a comment defeats a regex, and a
// stylesheet that happens not to contain one today is a stylesheet whose checks
// stop working the day somebody writes a content: "}" declaration.
func eachRule(css string) []cssRule {
	var rules []cssRule

	preludeStart := 0
	depth := 0

	for index := 0; index < len(css); index++ {
		switch css[index] {
		case '{':
			if depth == 0 {
				rules = append(rules, cssRule{selector: css[preludeStart:index]})
			}

			depth++
		case '}':
			if depth == 0 {
				continue
			}

			depth--

			if depth != 0 {
				continue
			}

			open := strings.LastIndex(css[preludeStart:index+1], "{")
			if open < 0 {
				continue
			}

			body := css[preludeStart+open+1 : index]

			// Attach the body to the innermost pending rule.
			for i := len(rules) - 1; i >= 0; i-- {
				if rules[i].body == "" {
					rules[i].body = body

					break
				}
			}

			preludeStart = index + 1
		}
	}

	return rules
}

// gridRuleFor returns the declaration block of the rule that lays out a tier's
// grid — the one whose selector mentions the tier *and* whose body declares
// `grid-template-areas`.
//
// Selecting on the body rather than taking the first matching selector is
// deliberate, and it is the second version of this helper. The first matched
// `:root[data-ui=tv] .shell` by substring and found the *shared viewport-locked*
// rule — the one that sets `height: 100dvh` for every tier from medium up — which
// mentions the same selector and declares no grid at all. A substring match for "a
// rule about this tier" returns the rule that happens to be written first, and
// that is regularly a rule about several tiers at once.
func gridRuleFor(css, selectorFragment string) (string, bool) {
	for _, rule := range eachRule(css) {
		if strings.Contains(rule.selector, selectorFragment) &&
			strings.Contains(rule.body, "grid-template-areas") {
			return rule.body, true
		}
	}

	return "", false
}

// collapse shortens a declaration block for a failure message.
func collapse(block string) string {
	joined := strings.Join(strings.Fields(block), " ")
	if len(joined) > 140 {
		return joined[:140] + "…"
	}

	return joined
}

// TestTheInheritedTypeSizeIsTheDesignBase is §5.1 and §5.3's first ban, read as a
// property of the built stylesheet.
//
// §5.1 fixes `html { font-size: 100% }` — which is not negotiable, because it is
// what makes the reader's own browser zoom the multiplier — and then says every
// size is a `rem` derived from a token. For an element with no explicit
// `font-size`, the size it derives from is whatever it *inherits*, and if nothing
// sets one the answer is the user agent's 16px rather than §5.1's
// `--text-base`.
//
// The viewport sweep is what found this: a skip link and the shell's own text
// measured 16px while every sized control measured 17px at compact and 25.5px at
// TV. Small, and structurally worse than it looks — an element added later without
// a type token would render at the browser's default and nothing would say so.
//
// So the rule is: the root is unscaled (`100%`, so zoom works) and the base is
// applied to the body through the token (so every tier's scale reaches everything
// that inherits). Both halves are asserted, because the second alone would be
// satisfied by a rule that scales the root — which is the failure 1.4.4 cares
// about.
func TestTheInheritedTypeSizeIsTheDesignBase(t *testing.T) {
	t.Parallel()

	css := built(t)

	// §5.1: the root stays at 100%. A `font-size` on `:root` in `rem` would scale
	// the reader's browser zoom a second time.
	rootFont := ruleDeclaring(css, ":root", "font-size")
	if rootFont != "100%" {
		t.Errorf(":root declares font-size: %s, want 100%%; §5.1 fixes it there so "+
			"the reader's browser zoom is the only multiplier (1.4.4)", rootFont)
	}

	// The base, through the token, on the element that inherits it.
	bodyFont := ruleDeclaring(css, "body", "font-size")
	if bodyFont != "var(--text-base)" {
		t.Errorf("body declares font-size: %q, want var(--text-base). §5.1's base "+
			"is 17px at --type-scale 1 and reaches every tier through that token; "+
			"without it an element with no explicit font-size inherits the user "+
			"agent's 16px instead", bodyFont)
	}

	// And the token carries the scale, so the base is not a fixed 17px at TV.
	base := declarationUnder(css, ":root", "--text-base:calc(1.0625rem * var(--type-scale))")
	if !base {
		t.Error(`--text-base is not calc(1.0625rem * var(--type-scale)); §5.1 states ` +
			`the base as 1.0625rem *at --type-scale 1*, and applying the multiplier ` +
			`is what lets TV mode raise it to 25.5px without a second declaration`)
	}

	// No `font-size` anywhere is declared in `px`. §5.3's ban, and it is checkable
	// from the built file because the minifier preserves the unit.
	for _, match := range regexp.MustCompile(`font-size:\s*[0-9.]+px`).FindAllString(css, -1) {
		t.Errorf("the built stylesheet declares %q; §5.3 prohibits a px font-size "+
			"because it fights the reader's browser zoom (1.4.4)", match)
	}
}

// ruleDeclaring returns the value a rule assigns to a property, or "".
func ruleDeclaring(css, selectorFragment, property string) string {
	pattern := regexp.MustCompile(regexp.QuoteMeta(property) + `:\s*([^;}]+)`)

	for _, rule := range eachRule(css) {
		if !strings.Contains(rule.selector, selectorFragment) {
			continue
		}

		if match := pattern.FindStringSubmatch(rule.body); match != nil {
			return strings.TrimSpace(match[1])
		}
	}

	return ""
}

// TestNoHoverRuleIsTheOnlyCarrierOfAnAffordance is §10.5's D-pad finding, turned
// into a gate.
//
// The D-pad walkthrough found that a `:hover` rule with no `:focus-visible`
// counterpart is an affordance a remote cannot reach. That is not a hypothetical
// concern here: a touch device emulates `:hover` on touch-down, so the row
// highlights, and by the time a reader has decided whether to press, the highlight
// is gone — an affordance that appears and withdraws itself.
//
// The D-pad pass is agent-assisted and does not run in CI, so a property it
// observed has to become a committed test or it comes back. **There are therefore no
// `:hover` rules in the shell stylesheets at all**, and this asserts their absence
// rather than auditing them for a companion — which is the stronger form, because
// "every hover rule has a focus companion" is a rule that can be satisfied by
// adding a hover rule and a near-identical focus rule, and "there are no hover
// rules" cannot be satisfied by anything.
//
// What is given up: a hover rule that merely *restates* something focus already
// shows is harmless. There are none, because none are needed — the two-tone ring
// covers every focusable element in the shell, and `:focus-visible` covers the
// pointer tiers.
//
// Scanned over the built stylesheet rather than the sources, because the minifier
// rewrites selector lists and a source-level scan would report a rule the build
// dropped as one that shipped.
func TestNoHoverRuleIsTheOnlyCarrierOfAnAffordance(t *testing.T) {
	t.Parallel()

	css := built(t)

	for _, rule := range eachRule(css) {
		if !strings.Contains(rule.selector, ":hover") {
			continue
		}

		t.Errorf("the built stylesheet has a :hover rule: %s { %s }. §10.5's D-pad "+
			"pass found that a hover affordance with no focus companion is one a "+
			"remote cannot reach, and a touch device emulates hover on touch-down so "+
			"the highlight withdraws before the reader has decided. Restate it on "+
			":focus-visible instead — every focusable element in this shell already "+
			"carries the two-tone ring",
			collapse(rule.selector), collapse(rule.body))
	}
}

// TestThePreferencesAreNeverUsedToInferAFormFactor is §3.3's closing sentence.
//
// "`prefers-reduced-motion`, `prefers-contrast` and `forced-colors` are honoured
// in every mode and never used to infer a form factor."
//
// The first half is the requirement and the second half is what makes it a
// requirement rather than an aspiration. A `prefers-contrast: more` rule that
// widened a rail would be using an accessibility preference to guess at the
// display — and a reader who has asked for more contrast because of their eyes
// would get a *different layout* for it, which is the opposite of what they
// asked for.
//
// So: those three conditions may appear, and they may change colours, borders and
// durations. They may not change a `data-ui` value, and nothing in this repository
// writes `data-ui` from a media query at all — the resolver writes it from the
// cookie, `?ui=`, the UA hint and the viewport, which `resolver_test.go` in
// `internal/httpapi/shell` asserts rule by rule.
func TestThePreferencesAreNeverUsedToInferAFormFactor(t *testing.T) {
	t.Parallel()

	css := built(t)

	// No rule combines a preference condition with a `data-ui` write, in either
	// direction. Writing is impossible in plain CSS — `data-ui` is an attribute,
	// not a property — so this asserts the reachable half: no preference block
	// selects on a form factor and no form-factor selector sits inside one.
	for _, condition := range []string{
		"prefers-reduced-motion",
		"prefers-contrast",
		"forced-colors",
	} {
		for _, start := range occurrences(css, condition) {
			// The enclosing rule's selector is the text from the previous `}` or
			// `{` to the `@media`'s `{`, which is enough to see a `data-ui`
			// selector sitting next to the condition.
			before := css[:start]
			cut := strings.LastIndexAny(before, "{}")
			if cut < 0 {
				continue
			}

			selector := before[cut+1:]
			if strings.Contains(selector, "data-ui") {
				t.Errorf("a @media (%s) rule selects on a form factor: %s. §3.3 "+
					"requires the three preference conditions to be honoured in "+
					"every mode and never used to infer one",
					condition, strings.TrimSpace(selector))
			}
		}
	}
}

// occurrences returns every index of a substring.
func occurrences(haystack, needle string) []int {
	var found []int

	for index := 0; ; {
		at := strings.Index(haystack[index:], needle)
		if at < 0 {
			return found
		}

		found = append(found, index+at)
		index += at + len(needle)
	}
}
