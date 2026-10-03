package theme_test

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/httpapi/theme"
)

// The product's side of the theme layer: `internal/web/static/css/theme.css`.
//
// This file reads the **source** sheet rather than the built one, for the reason
// `ownership_test.go` gives — the build minifies and Tailwind prunes whatever no
// utility references, so a declaration only a rule no template uses would be absent
// from the output, and a check that read it would report the stylesheet as missing
// a token it has. The one test that must read the artefact has its own helper in
// `sheet_test.go`.
//
// The three claims are the three things in that file which a later edit could break
// silently, because none of them is a compile error and none of them is visible
// from a browser without measuring: the scrim's opacity against every floor it
// protects, the motion against §6.7, and the file's own declarations against
// §4.12.1's right-hand column.

// washRulePattern is a rule that paints the header image.
var washRulePattern = regexp.MustCompile(`[^{}]*\.shell-header::before[^{}]*\{`)

// motionPattern is a transition or animation declaration.
var motionPattern = regexp.MustCompile(`(transition|animation)\s*:\s*([^;]+);`)

// mediaBlockPattern is an at-rule with a body, capturing the body's offsets
// through the sheet.
var mediaBlockPattern = regexp.MustCompile(`@media[^{]*\{`)

// headerFloors are the tokens a reader meets inside the banner, and the floor each
// one has to hold *through* the scrim.
//
// `--text-subtle` is in here although the banner does not use it today, and that is
// deliberate: the floor set is what the wash must not take anything under, and a
// breadcrumb row added to the header next quarter must not be the thing that
// discovers a wash at 0.3. A test that measured only the tokens in use today would
// have to be rewritten by whoever changes the markup, which is the test that does
// not get rewritten.
var headerFloors = map[string]float64{
	"--text":        4.5,
	"--text-muted":  4.5,
	"--text-subtle": 4.5,
	"--border":      3.0,
	"--focus-ring":  3.0,
	"--accent":      3.0,
}

// worstImage is the opposite polarity to each theme's page: pure black on the light
// page and pure white on the dark one.
//
// The worst case for *any* alpha, so it does not have to be re-derived when the
// opacity changes — and a partially transparent PNG sitting between the two is
// strictly safer than either, since the composite is a weighted mean that cannot
// leave the segment between the page and the image.
var worstImage = map[string]string{
	"light": "#000000",
	"dark":  "#ffffff",
}

// TestTheHeaderWashKeepsEveryHeaderTokenAboveItsFloor is the arithmetic theme.css
// writes in a comment, checked.
//
// The comment is the argument for `opacity: 0.05`, and a comment is exactly the
// kind of claim nobody re-checks after the first edit: raising the wash to make a
// banner "pop" is a one-character change, every other gate stays green, and the
// first thing to break is a `--border` on a search field that is 3:1 and not 3.1:1.
// So the numbers are recomputed here from the **declared opacity** and the
// **declared palette** — neither is a copy in this test — against the worst image
// either theme can be handed.
//
// Two opacities, because the file has two: the base and the TV rule, where ten
// feet of distance makes the image give way further. Both are held to every floor,
// so lowering the TV number is safe and raising the base one is not.
//
// **Mutation:** changing `opacity: 0.05` in theme.css to `0.3` fails this test
// with four ratios under their floors; deleting the `--border` row from
// `headerFloors` passes it again, which is why the floor set is a table in the
// test rather than a number in the message.
func TestTheHeaderWashKeepsEveryHeaderTokenAboveItsFloor(t *testing.T) {
	t.Parallel()

	opacities := washOpacities(t)
	if len(opacities) == 0 {
		t.Fatal("theme.css declares no .shell-header::before rule outside a media " +
			"block; the walk finds nothing, so every assertion below passes for " +
			"the wrong reason")
	}

	for name, alpha := range opacities {
		if alpha <= 0 || alpha > 1 {
			t.Errorf("the %s wash is %v, which is outside (0, 1]", name, alpha)
		}
	}

	// The image gives way at TV; a rule that made it stronger there would be the
	// comment and the code disagreeing, and only this comparison sees it.
	if base, tv := opacities["the base rule"], opacities["the TV rule"]; tv > base {
		t.Errorf("the TV wash is %v and the base wash is %v: ten feet further from "+
			"the reader is when the image is supposed to give way, not to assert "+
			"itself (theme.css's TV rule)", tv, base)
	}

	for themeName, image := range worstImage {
		blocks := tokensInThemeBlock(t, themeName)

		page, isHex := blocks["--bg"]
		if !isHex || !strings.HasPrefix(page, "#") {
			t.Fatalf("the %s theme's --bg is %q, want a #rrggbb literal: the wash "+
				"composites over the page, and a var() here would make the arithmetic "+
				"a guess", themeName, page)
		}

		for label, alpha := range opacities {
			backdrop := composite(page, image, alpha)

			for token, floor := range headerFloors {
				declared, ok := blocks[token]
				if !ok {
					t.Errorf("the %s theme declares no %s, so the floor cannot be "+
						"measured: the token moved and this test did not", themeName,
						token)

					continue
				}

				ratio, err := ratioOf(declared, backdrop)
				if err != nil {
					t.Fatalf("%s on the %s page: %v", token, themeName, err)
				}

				if ratio < floor {
					t.Errorf("%s measures %.2f:1 over the %s page under the %s wash "+
						"(backdrop %s from %s at %v), want at least %.1f:1 — the "+
						"banner is where a reader meets the search field's boundary "+
						"and the focus ring", token, ratio, themeName, label,
						backdrop, image, alpha, floor)
				}
			}
		}
	}
}

// washOpacities reads the two wash opacities out of theme.css: the base rule and
// the TV one.
//
// Media blocks are excluded by offset rather than by pattern, because the
// forced-colours block contains the *same selector* with `opacity: 1` — that
// declaration is the scrim being withdrawn, and counting it would make the "worst
// case" this test measures a no-image header with no wash at all.
func washOpacities(t *testing.T) map[string]float64 {
	t.Helper()

	sheet := stripComments(readSheet(t, "theme.css"))
	inside := mediaBodies(sheet)

	found := map[string]float64{}

	for _, match := range washRulePattern.FindAllStringSubmatchIndex(sheet, -1) {
		if insideMedia(inside, match[0]) {
			continue
		}

		prelude := sheet[match[0] : strings.Index(sheet[match[0]:], "{")+match[0]]

		body, closed := braceBody(sheet, strings.Index(sheet[match[1]-1:], "{")+match[1]-1)
		if !closed {
			t.Fatalf("the rule %q is never closed", prelude)
		}

		opacity := declarationValue(body, "opacity")
		if opacity == "" {
			t.Fatalf("the rule %q declares no opacity", prelude)
		}

		label := "the base rule"
		if strings.Contains(prelude, "data-ui") {
			label = "the TV rule"
		}

		if _, seen := found[label]; seen {
			t.Fatalf("theme.css has two %s rules for .shell-header::before: %q",
				label, prelude)
		}

		var value float64
		if parsed, err := strconv.ParseFloat(opacity, 64); err != nil {
			t.Fatalf("the rule %q declares opacity %q, which is not a number",
				prelude, opacity)
		} else {
			value = parsed
		}

		found[label] = value
	}

	return found
}

// mediaBodies is every offset range a @media body occupies.
func mediaBodies(sheet string) [][2]int {
	var ranges [][2]int

	for _, match := range mediaBlockPattern.FindAllStringIndex(sheet, -1) {
		open := match[1] - 1
		body, closed := braceBody(sheet, open)
		if !closed {
			continue
		}

		ranges = append(ranges, [2]int{open + 1, open + 1 + len(body)})
	}

	return ranges
}

// insideMedia reports whether offset falls inside one of the recorded bodies.
func insideMedia(ranges [][2]int, offset int) bool {
	for _, span := range ranges {
		if offset >= span[0] && offset < span[1] {
			return true
		}
	}

	return false
}

// declarationValue reads one declaration's value out of a rule body.
func declarationValue(body, name string) string {
	for _, match := range valuePattern.FindAllStringSubmatch(body, -1) {
		if match[1] == "--"+name {
			return strings.TrimSpace(match[2])
		}
	}

	// A non-custom-property declaration: the same grammar, one shape wider.
	pattern := regexp.MustCompile(`(?:^|[;{\s])` + regexp.QuoteMeta(name) + `\s*:\s*([^;}]*)`)

	if match := pattern.FindStringSubmatch(body); match != nil {
		return strings.TrimSpace(match[1])
	}

	return ""
}

// ratioOf is the WCAG contrast between two `#rrggbb` literals, through this
// package's own arithmetic — which `TestTheContrastFormulaAgreesWithTheRatiosTheRecordQuotes`
// holds to the ratios §6.2 and §6.3 quote, so measuring here is measuring with
// the implementation the product measures with.
func ratioOf(first, second string) (float64, error) {
	a, err := theme.ParseColour(first)
	if err != nil {
		return 0, fmt.Errorf("parse the first colour %q: %w", first, err)
	}

	b, err := theme.ParseColour(second)
	if err != nil {
		return 0, fmt.Errorf("parse the second colour %q: %w", second, err)
	}

	return theme.ContrastRatio(a, b), nil
}

// composite blends image over page at alpha, in sRGB — which is where a browser
// composites a `background-image` under an `opacity`.
//
// Rounded to the nearest byte, as an 8-bit surface has to be. The rounding costs
// less than 0.01:1 on either side of a floor this test measures in hundredths, and
// computing in floats would be a claim about a rendering path neither this test nor
// any browser reproduces exactly.
func composite(page, image string, alpha float64) string {
	p := mustHex(page)
	w := mustHex(image)

	var out strings.Builder
	out.WriteString("#")

	for index := range 3 {
		mixed := alpha*float64(w[index]) + (1-alpha)*float64(p[index])
		fmt.Fprintf(&out, "%02x", int(mixed+0.5))
	}

	return out.String()
}

// mustHex reads a `#rrggbb` literal, failing the test if it is not one.
func mustHex(value string) [3]byte {
	var parsed [3]byte

	if len(value) != 7 || value[0] != '#' {
		panic("not a #rrggbb literal: " + value)
	}

	for index := range 3 {
		channel, err := strconv.ParseUint(value[1+2*index:3+2*index], 16, 8)
		if err != nil {
			panic("not a #rrggbb literal: " + value)
		}

		parsed[index] = byte(channel)
	}

	return parsed
}

// TestTheThemeStylesheetAddsNoMotionOutsideThePreferenceBlock holds §6.7 for the
// file this package owns.
//
// theme.css declares exactly one transition — the scrim's opacity, which changes
// when a reader cycles the mode — and this is the rule that keeps it honest. The
// pairing is what makes it a check rather than a comment: every `transition` and
// `animation` declaration in the file is either `none` or **withdrawn** by a
// `none` on the same selector inside the reduced-motion block, so a later rule
// animating a brand property cannot land without the reader's preference being
// honoured.
//
// "Withdrawn rather than absent" is the claim, and it is the claim the file
// actually makes: the transition has to exist outside the block, because that is
// the state a reader who did not ask for reduced motion is in. A rule that
// simply banned motion outside a preference block would fail the product's own
// stylesheet; a rule that only checked the block's contents would pass with the
// block deleted. Both directions are asserted, and the counts are asserted too,
// because a file that declared no motion at all would pass vacuously and the
// rule would then be a green light wired to nothing — the failure AGENTS.md
// records for every audit in this repository.
//
// **Mutation:** deleting `transition: none` from the reduced-motion block fails
// this test on the unwithdrawn base transition; adding a second
// `transition: opacity 2s linear` to `.shell-header` fails it the same way;
// changing the block's `transition: none` to `transition: opacity 1s linear`
// fails it as a motion declared *inside* the preference block; and deleting both
// motion declarations fails it on the empty-count guard, which is the vacuous
// pass this test refuses to make.
func TestTheThemeStylesheetAddsNoMotionOutsideThePreferenceBlock(t *testing.T) {
	t.Parallel()

	sheet := stripComments(readSheet(t, "theme.css"))

	var rules []cssRule
	walkRules(sheet, "", &rules)

	if len(rules) == 0 {
		t.Fatal("theme.css contains no rule at all; the walk finds nothing, so " +
			"every assertion below passes for the wrong reason")
	}

	const reducedMotion = "@media (prefers-reduced-motion: reduce)"

	type motion struct {
		rule     cssRule
		property string
		value    string
	}

	declared := []motion{}
	withdrawn := map[string]bool{}
	hasReducedBlock := false

	// First pass: what the file declares, and what it withdraws. Two passes
	// because theme.css declares its transition *before* the block that withdraws
	// it, and a single pass would judge the declaration against a withdrawal it
	// has not reached yet — a wrong failure, which is worse than none.
	for _, rule := range rules {
		if strings.Contains(rule.selector, "prefers-reduced-motion") {
			hasReducedBlock = true
		}

		for _, match := range motionPattern.FindAllStringSubmatch(declArea(rule.body), -1) {
			declaration := motion{
				rule:     rule,
				property: match[1],
				value:    strings.TrimSpace(match[2]),
			}

			declared = append(declared, declaration)

			if declaration.rule.context == reducedMotion && declaration.value == "none" {
				withdrawn[motionKey(rule, declaration.property)] = true
			}
		}
	}

	for _, declaration := range declared {
		rule, property, value := declaration.rule, declaration.property, declaration.value

		switch {
		case rule.context == reducedMotion:
			if value != "none" {
				t.Errorf("theme.css declares %s: %s inside the reduced-motion block: "+
					"that block withdraws motion, so a duration in it is a reader's "+
					"preference being answered with a rule that animates (§6.7)",
					property, value)
			}

		case value == "none":
			// Withheld outright; nothing to withdraw.

		default:
			if !withdrawn[motionKey(rule, property)] {
				t.Errorf("theme.css declares %s: %s on %q outside a preference block, "+
					"and no `none` withdraws it in %s: a reader who asked for no motion "+
					"gets it anyway, and §6.7 is about the reader's preference rather "+
					"than about the file's intentions",
					property, value, rule.selector, reducedMotion)
			}
		}
	}

	if !hasReducedBlock {
		t.Error("theme.css has no prefers-reduced-motion block, so nothing withdraws " +
			"the transition it declares (§6.7)")
	}

	if len(declared) == 0 {
		t.Error("theme.css declares no transition or animation at all, so this test " +
			"passed on a file with nothing to check; the rule is about the one " +
			"transition the file does declare, and that declaration has to exist " +
			"for the rule to mean anything")
	}

	if len(withdrawn) == 0 {
		t.Error("no motion in theme.css is withdrawn inside the preference block, so " +
			"the pairing this test claims to hold has one side missing")
	}
}

// cssRule is one rule: the at-rule it sits under, its own prelude, and its body.
type cssRule struct {
	context  string // the enclosing at-rule's prelude, or "" at the top level
	selector string
	body     string
}

// walkRules finds every rule in a sheet, carrying the at-rule it is nested under.
//
// Brace-matched rather than pattern-matched, because a `@media` body holds rules
// and a rule body holds declarations, and a pattern over the whole sheet cannot
// tell which of the two a `{` opened — which is how a test ends up measuring a
// transition against the wrong selector.
func walkRules(sheet, context string, found *[]cssRule) {
	for from := 0; from < len(sheet); {
		open := strings.IndexByte(sheet[from:], '{')
		if open < 0 {
			return
		}

		open += from

		body, closed := braceBody(sheet, open)
		if !closed {
			return
		}

		selector := strings.TrimSpace(sheet[from:open])

		*found = append(*found, cssRule{
			context:  context,
			selector: selector,
			body:     body,
		})

		nested := context
		if strings.HasPrefix(selector, "@") {
			nested = selector
		}

		walkRules(body, nested, found)

		from = open + 1 + len(body) + 1
	}
}

// declArea is the part of a rule body holding *this* rule's declarations rather
// than a nested rule's.
//
// The text up to the first nested `{`, which for a rule with no nesting is the
// whole body. theme.css uses no nesting today, and taking the prefix rather than
// the whole body is what stops a `@media` rule from claiming the transitions of
// the rules inside it — a claim that would name the at-rule as the selector and
// make every withdrawal comparison below miss.
func declArea(body string) string {
	prefix, _, found := strings.Cut(body, "{")
	if !found {
		return body
	}

	return prefix
}

// motionKey identifies one rule's declaration of one property.
//
// The selector and the property, and deliberately *not* the at-rule the rule
// sits under: the claim this test makes is that the transition on
// `.shell-header::before` is withdrawn by a `none` **on the same selector inside
// the reduced-motion block**, and a key carrying the context would make those two
// declarations differ by construction — every withdrawal would miss, and the test
// would fail on the product's own correct stylesheet.
func motionKey(rule cssRule, property string) string {
	return rule.selector + "\x00" + property
}

// TestTheThemeStylesheetDeclaresOnlyTheProductDefaults is §4.12.1's right-hand
// column, held against the file that has to obey it.
//
// theme.css is the sheet the generated one competes with, so it sits on the
// *product's* side of the table: a protected token declared here would be the
// product writing a value the accessibility contract owns, in the one file whose
// whole job is to be the safe thing a campaign's declarations lose to. The claim is
// narrow on purpose — the file declares exactly one custom property, the image's
// default — and narrow claims are the ones a test can hold completely.
//
// **Mutation:** adding `--text-muted: #123456;` to theme.css's light block fails
// this test, and no other test in the repository notices: ownership.go's tables
// read *vocabulary* names, not declarations, so this is the only gate on what the
// product's own theme file may write.
func TestTheThemeStylesheetDeclaresOnlyTheProductDefaults(t *testing.T) {
	t.Parallel()

	sheet := stripComments(readSheet(t, "theme.css"))

	declared := map[string]string{}
	for _, match := range valuePattern.FindAllStringSubmatch(sheet, -1) {
		declared[match[1]] = strings.TrimSpace(match[2])
	}

	if len(declared) == 0 {
		t.Fatal("theme.css declares no custom property; the walk finds nothing, so " +
			"every assertion below passes for the wrong reason")
	}

	for name, value := range declared {
		if name != brandImage {
			t.Errorf("theme.css declares %s, and the only custom property this file "+
				"owns is %s: every other name belongs to a token layer, and a "+
				"declaration here would be a second answer to the same question "+
				"(UI §4.12.1)", name, brandImage)

			continue
		}

		if value != "none" {
			t.Errorf("theme.css declares %s as %q, want %q: the product has no image, "+
				"and anything else would be a campaign's default with no campaign "+
				"behind it", name, value, "none")
		}
	}
}
