package web_test

import (
	"strings"
	"testing"
)

// The sweep-conversion audits: UI §10.9's "every finding becomes a committed
// test" for the findings the agent-assisted sweeps (§10.3–§10.7) produced that
// no committed test held.
//
// Each audit below reads the *built* stylesheet, for the reason
// `TestTheBuiltStylesheetCarriesTheTokensAndTheGrid` gives: the markup is
// correct and only the bytes can be missing, which is the one thing a DOM walk
// cannot see. And each audit is a function over a CSS string rather than inline
// assertions over `built(t)`, so its own meta-test can feed it a violating
// fixture and require rejection — an audit nobody can fail is not an audit.
//
// WHAT IS ALREADY HELD ELSEWHERE, AND THEREFORE NOT HERE
// ------------------------------------------------------
// The §2 viewport matrix and its boundaries (including 200% zoom) are
// `TestTierBoundariesFollowTheViewportMatrix`; the breakpoint constants are
// `TestTheLayoutConstantsAreTheOnesTheSpecGives`; every media query matching a
// declared constant is `TestEveryMediaQueryMatchesADeclaredConstant`; the
// scroll model, the centre's wrapping, the 40px sheet context and the
// never-scaled rule are the `grid_test.go` audits; the TV rail widening and the
// inherited type size are the two viewport-sweep findings `a11y_test.go`
// already holds; the stacked table form is
// `TestTheTableStackedFormCarriesTheSameDataAsTheTabularOne`; the D-pad's
// hover finding is `TestNoHoverRuleIsTheOnlyCarrierOfAnAffordance`; and the
// §10.7 preference blocks are the `tokens_contrast_test.go` media audits. What
// follows is the residue: five sweep requirements whose behaviour shipped with
// no test holding it.

// mediaBodies returns the bodies of every top-level `@media` rule whose prelude
// names the given condition.
//
// A dedicated brace-stack walk rather than `eachRule`: a media block holding
// several inner rules defeats a last-`{` body cut, and the short-landscape
// condition owns two blocks in the built file — an audit that read only part
// of one would pass while the rest was deleted.
func mediaBodies(css, condition string) []string {
	var bodies []string

	preludeStart := 0
	depth := 0
	mediaDepth := -1
	var prelude string

	for index := 0; index < len(css); index++ {
		switch css[index] {
		case '{':
			if depth == 0 {
				prelude = css[preludeStart:index]

				if strings.HasPrefix(strings.TrimSpace(prelude), "@media") &&
					strings.Contains(compact(prelude), compact(condition)) {
					mediaDepth = depth + 1
				}
			}

			depth++
		case '}':
			if depth == 0 {
				continue
			}

			depth--

			if mediaDepth >= 0 && depth == mediaDepth-1 {
				open := strings.Index(css[preludeStart:], "{")
				if open >= 0 {
					bodies = append(bodies, css[preludeStart+open+1:index])
				}

				mediaDepth = -1
			}

			if depth == 0 {
				preludeStart = index + 1
			}
		}
	}

	return bodies
}

// ruleDeclares reports whether any rule in bodies whose selector list names
// selector declares declaration.
//
// Selectors are compared per comma-separated member after compacting, because
// the minifier drops attribute-value quotes and the built selector lists are
// long: a substring match for ".shell" would also match ".shell-sheets", and a
// match for ":root" would match every tiered `:root[data-ui=…]` rule.
func ruleDeclares(bodies []string, selector, declaration string) bool {
	wantSelector := compact(selector)
	wantDeclaration := compact(declaration)

	for _, body := range bodies {
		for _, rule := range eachRule(body) {
			for member := range strings.SplitSeq(rule.selector, ",") {
				if compact(member) != wantSelector {
					continue
				}

				if strings.Contains(compact(rule.body), wantDeclaration) {
					return true
				}
			}
		}
	}

	return false
}

// --- §3.5 short landscape ----------------------------------------------------

// auditShortLandscape is §3.5's row — "a 390px-tall viewport cannot afford
// 56 + 56 of chrome" — as four declarations inside the short-landscape block.
//
// The constants were already pinned (`--header-h-short: 2.5rem`,
// `--breakpoint-short: 480px`) and every media query already matches a declared
// constant, but neither says what the block *does*: deleting the block's body
// keeps both green while the short viewport gets the full chrome back. The
// viewport sweep covered this by looking; this is the committed version.
func auditShortLandscape(css string) []string {
	bodies := mediaBodies(css, "max-height:480px")
	if len(bodies) == 0 {
		return []string{
			"no @media (max-height: 480px) block; §3.5's short-landscape row has no rule at all",
		}
	}

	var violations []string

	if !ruleDeclares(bodies, ":root", "--header-h:var(--header-h-short)") {
		violations = append(violations,
			"the short-landscape block does not collapse the header to --header-h-short; "+
				"§3.5 fixes it at 2.5rem because a 390px viewport cannot afford 56px of header")
	}

	if !ruleDeclares(bodies, ".shell", "padding-block-end:0") {
		violations = append(violations,
			"the short-landscape block does not return the bottom-bar reservation to the page; "+
				"without it the document keeps 56px of padding for a bar that is gone (§3.5)")
	}

	if !ruleDeclares(bodies, ".shell-footer", "display:none") {
		violations = append(
			violations,
			"the short-landscape block does not remove the footer; §3.5 removes it entirely rather "+
				"than zero-sizing it, because a 56px row of sub-minimum targets is worse than no row",
		)
	}

	if !ruleDeclares(bodies, ":root[data-ui=compact].shell-sheets", "padding-block-end:0") &&
		!ruleDeclares(bodies, ":root[data-ui=compact-short].shell-sheets", "padding-block-end:0") {
		violations = append(violations,
			"the short-landscape block does not zero the sheets' bottom offset; the sheets would "+
				"float above a bar that is not there (§3.1's †, §3.5)")
	}

	return violations
}

// TestTheBuiltStylesheetCollapsesTheChromeInShortLandscape is §3.5 held on the
// built file.
func TestTheBuiltStylesheetCollapsesTheChromeInShortLandscape(t *testing.T) {
	t.Parallel()

	for _, violation := range auditShortLandscape(built(t)) {
		t.Error(violation)
	}
}

// TestTheBuiltStylesheetShortLandscapeAuditRejectsTheViolationItClaimsTo is the
// meta-proof: each mutation below removes one load-bearing declaration, and the
// audit must object to each.
//
// The near-miss matters more than the deletions: the last fixture keeps every
// declaration but moves the footer's removal out of the media block, and the
// audit must still fail — a check that read the whole file rather than the block
// would pass on it, which is the vacuous-pass shape this file exists to prevent.
func TestTheBuiltStylesheetShortLandscapeAuditRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	fixtures := map[string]string{
		"no short block at all": `.shell{padding-block-end:calc(var(--bottom-bar-h))}`,
		"header not collapsed": `@media (max-height:480px){` +
			`.shell{padding-block-end:0}` +
			`.shell-footer{display:none}` +
			`:root[data-ui=compact] .shell-sheets{padding-block-end:0}}`,
		"footer merely emptied": `@media (max-height:480px){` +
			`:root{--header-h:var(--header-h-short)}` +
			`.shell{padding-block-end:0}` +
			`.shell-footer{visibility:hidden}` +
			`:root[data-ui=compact] .shell-sheets{padding-block-end:0}}`,
		"declaration outside the block": `@media (max-height:480px){` +
			`:root{--header-h:var(--header-h-short)}` +
			`.shell{padding-block-end:0}` +
			`:root[data-ui=compact] .shell-sheets{padding-block-end:0}}` +
			`.shell-footer{display:none}`,
	}

	for name, css := range fixtures {
		if violations := auditShortLandscape(css); len(violations) == 0 {
			t.Errorf("the audit accepts %q; it holds nothing", name)
		}
	}

	healthy := `@media (max-height:480px){` +
		`:root{--header-h:var(--header-h-short)}` +
		`.shell{padding-block-end:0}` +
		`.shell-footer{display:none}` +
		`:root[data-ui=compact] .shell-sheets,:root[data-ui=compact-short] .shell-sheets{padding-block-end:0}}`

	if violations := auditShortLandscape(healthy); len(violations) != 0 {
		t.Errorf("the audit rejects a healthy block: %v", violations)
	}
}

// --- §3.6 bottom-bar reservation ---------------------------------------------

// auditBottomBarReservation is §10.3's "the bottom bar never covering content"
// reduced to the reservation that prevents it.
//
// The compact shell is document-scrolled with a *fixed* bottom bar, so the
// document reserves the bar's height plus the home indicator up front; without
// the reservation the last paragraph of every page sits under the bar. The
// reservation references the token rather than repeating 3.5rem, so a retune
// moves both together.
func auditBottomBarReservation(css string) []string {
	var violations []string

	var shell strings.Builder
	for _, rule := range eachRule(css) {
		if compact(rule.selector) != ".shell" {
			continue
		}

		shell.WriteString(compact(rule.body))
	}

	if !strings.Contains(
		shell.String(),
		"padding-block-end:calc(var(--bottom-bar-h)+env(safe-area-inset-bottom))",
	) {
		violations = append(violations,
			"the base .shell rule does not reserve the bottom bar and the home indicator; "+
				"at compact the bar is fixed, so unreserved content scrolls under it (UI §3.6, §10.3)")
	}

	var sheets strings.Builder
	for _, rule := range eachRule(css) {
		for member := range strings.SplitSeq(rule.selector, ",") {
			if strings.Contains(compact(member), ".shell-sheets") {
				sheets.WriteString(compact(rule.body))
			}
		}
	}

	if !strings.Contains(sheets.String(), "var(--bottom-bar-h)") {
		violations = append(violations,
			"no .shell-sheets rule reads --bottom-bar-h; the rail sheet must anchor above the bar "+
				"rather than over it (§3.1's †, §10.3)")
	}

	return violations
}

// TestTheBuiltStylesheetReservesRoomForTheBottomBar is §3.6's reservation held
// on the built file.
func TestTheBuiltStylesheetReservesRoomForTheBottomBar(t *testing.T) {
	t.Parallel()

	for _, violation := range auditBottomBarReservation(built(t)) {
		t.Error(violation)
	}
}

// TestTheBuiltStylesheetBottomBarAuditRejectsTheViolationItClaimsTo is the
// meta-proof, including the near-miss the token reference exists to catch: a
// reservation that repeats the number instead of reading the token.
func TestTheBuiltStylesheetBottomBarAuditRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	fixtures := map[string]string{
		"no reservation": `.shell{display:grid}` +
			`:root[data-ui=compact] .shell-sheets{padding-block-end:calc(var(--bottom-bar-h))}`,
		"hard-coded height": `.shell{padding-block-end:calc(3.5rem + env(safe-area-inset-bottom))}` +
			`:root[data-ui=compact] .shell-sheets{padding-block-end:calc(var(--bottom-bar-h))}`,
		"sheets ignore the bar": `.shell{padding-block-end:calc(var(--bottom-bar-h) + env(safe-area-inset-bottom))}` +
			`:root[data-ui=compact] .shell-sheets{padding-block-end:0}`,
	}

	for name, css := range fixtures {
		if violations := auditBottomBarReservation(css); len(violations) == 0 {
			t.Errorf("the audit accepts %q; it holds nothing", name)
		}
	}

	healthy := `.shell{padding-block-end:calc(var(--bottom-bar-h) + env(safe-area-inset-bottom))}` +
		`:root[data-ui=compact] .shell-sheets{padding-block-end:calc(var(--bottom-bar-h) + env(safe-area-inset-bottom))}`

	if violations := auditBottomBarReservation(healthy); len(violations) != 0 {
		t.Errorf("the audit rejects a healthy reservation: %v", violations)
	}
}

// --- §4.9 compact map readout ------------------------------------------------

// auditCompactMapReadout is the phone-play sweep's "compact canvas takes no
// gesture" held on the stylesheet half.
//
// The markup half is `TestTheMapSurfaceSatisfiesTheClientContract` — aria-hidden,
// no focus stops, no inline handlers — and `play.css` names this audit as its
// other half: `pointer-events: none` under the compact tiers, without which the
// canvas swallows taps meant for the action bar. The comment cited
// `TestTheCompactMapTakesNoGesture`; no such test existed. This is it, under
// the name the gate pattern runs.
func auditCompactMapReadout(css string) []string {
	var violations []string

	for _, tier := range []string{"compact", "compact-short"} {
		found := false

		for _, rule := range eachRule(css) {
			scoped := false

			for member := range strings.SplitSeq(rule.selector, ",") {
				text := compact(member)
				if strings.Contains(text, ".play-map") && strings.Contains(text, "data-ui="+tier) {
					scoped = true

					break
				}
			}

			if scoped && strings.Contains(compact(rule.body), "pointer-events:none") {
				found = true

				break
			}
		}

		if !found {
			violations = append(violations,
				"no rule takes gestures away from .play-map at "+tier+"; §4.9 makes the compact "+
					"map a read-only readout, and a canvas that takes taps swallows the action bar's")
		}
	}

	return violations
}

// TestTheBuiltStylesheetKeepsTheCompactMapFromTakingGestures is §4.9's readout
// held on the built file.
func TestTheBuiltStylesheetKeepsTheCompactMapFromTakingGestures(t *testing.T) {
	t.Parallel()

	for _, violation := range auditCompactMapReadout(built(t)) {
		t.Error(violation)
	}
}

// TestTheBuiltStylesheetCompactMapAuditRejectsTheViolationItClaimsTo is the
// meta-proof. The near-miss is a `pointer-events: none` on an *unscoped*
// `.play-map` rule: global gesture removal would also silence the laptop map,
// so the audit must demand the tier scope, not just the declaration.
func TestTheBuiltStylesheetCompactMapAuditRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	fixtures := map[string]string{
		"no gesture removal": `:root[data-ui=compact] .play-map{max-block-size:60dvb}`,
		"one tier only":      `:root[data-ui=compact] .play-map{pointer-events:none}`,
		"unscoped removal":   `.play-map{pointer-events:none}`,
	}

	for name, css := range fixtures {
		if violations := auditCompactMapReadout(css); len(violations) == 0 {
			t.Errorf("the audit accepts %q; it holds nothing", name)
		}
	}

	healthy := `:root[data-ui=compact] .play-map,:root[data-ui=compact-short] .play-map{pointer-events:none}`

	if violations := auditCompactMapReadout(healthy); len(violations) != 0 {
		t.Errorf("the audit rejects a healthy readout: %v", violations)
	}
}

// --- §4.9 rail sheet above the action bar -------------------------------------

// auditPlaySheetAnchor is §4.9's layer-collision rule: the compact rail sheet is
// anchored *above* the action bar, never over it, and the anchor reads the
// bar's own token so the two cannot disagree.
//
// `TestTheActionBarSitsBetweenTheRailAndTheFooter` holds the DOM order; this
// holds the offset. A sheet anchored at `inset-block-end: 0` renders in DOM
// order and still covers the bar, which is why order alone is not the claim.
func auditPlaySheetAnchor(css string) []string {
	var violations []string

	anchored := false

	for _, rule := range eachRule(css) {
		if !strings.Contains(compact(rule.selector), ".shell--play.shell-rail") &&
			!strings.Contains(compact(rule.selector), ".shell--play .shell-rail") {
			continue
		}

		if strings.Contains(compact(rule.body), "inset-block-end:var(--action-bar-h)") {
			anchored = true

			break
		}
	}

	if !anchored {
		violations = append(violations,
			"no .shell--play .shell-rail rule anchors above the action bar; without "+
				"inset-block-end: var(--action-bar-h) the rail sheet covers the bar it should sit above (§4.9, §10.3)")
	}

	declared := false

	for _, rule := range eachRule(css) {
		if strings.Contains(compact(rule.body), "--action-bar-h:") {
			declared = true

			break
		}
	}

	if !declared {
		violations = append(violations,
			"--action-bar-h is never declared; the anchor reads a token nothing sets (§4.9)")
	}

	return violations
}

// TestTheBuiltStylesheetAnchorsThePlaySheetAboveTheActionBar is §4.9's
// layer-collision rule held on the built file.
func TestTheBuiltStylesheetAnchorsThePlaySheetAboveTheActionBar(t *testing.T) {
	t.Parallel()

	for _, violation := range auditPlaySheetAnchor(built(t)) {
		t.Error(violation)
	}
}

// TestTheBuiltStylesheetPlaySheetAuditRejectsTheViolationItClaimsTo is the
// meta-proof, including the zero-anchored sheet that DOM order alone would pass.
func TestTheBuiltStylesheetPlaySheetAuditRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	fixtures := map[string]string{
		"no anchor":            `:root{--action-bar-h:4.5rem}.shell--play .shell-rail{position:fixed}`,
		"anchored at zero":     `:root{--action-bar-h:4.5rem}.shell--play .shell-rail{position:fixed;inset-block-end:0}`,
		"anchor with no token": `.shell--play .shell-rail{position:fixed;inset-block-end:var(--action-bar-h)}`,
	}

	for name, css := range fixtures {
		if violations := auditPlaySheetAnchor(css); len(violations) == 0 {
			t.Errorf("the audit accepts %q; it holds nothing", name)
		}
	}

	healthy := `:root{--action-bar-h:4.5rem}` +
		`.shell--play .shell-rail{position:fixed;inset-block-end:var(--action-bar-h)}`

	if violations := auditPlaySheetAnchor(healthy); len(violations) != 0 {
		t.Errorf("the audit rejects a healthy anchor: %v", violations)
	}
}

// --- §10.5 TV strip spacing ---------------------------------------------------

// auditTvStripSpacing is the D-pad walkthrough's "single most important
// declaration": the TV navigation strip separates its destinations by the target
// gap, so a mis-aimed remote press lands on empty space rather than a neighbour.
//
// `--target-gap`'s 16px floor is already held (`tvSeparation` in
// `TestTheTargetMinimumsMeetTheSpecifiedFloors`); what nothing held is that the
// strip *uses* it. A strip with no gap meets the floor vacuously while remote
// presses land on neighbours.
func auditTvStripSpacing(css string) []string {
	var violations []string

	var strip strings.Builder

	for _, rule := range eachRule(css) {
		applies := false

		for member := range strings.SplitSeq(rule.selector, ",") {
			text := compact(member)
			if strings.Contains(text, ".shell-nav") && strings.Contains(text, "data-ui=tv") {
				applies = true
			}
		}

		if applies {
			strip.WriteString(compact(rule.body))
		}
	}

	if strip.Len() == 0 {
		return []string{
			"no TV navigation-strip rule; the D-pad strip has no layout at all (§3.4, §10.5)",
		}
	}

	stripBody := strip.String()

	if !strings.Contains(stripBody, "gap:var(--target-gap)") {
		violations = append(violations,
			"the TV navigation strip does not space itself by --target-gap; §7.3's 16px gutter "+
				"is what keeps a mis-aimed remote press off the neighbour (§3.4, §10.5)")
	}

	if !strings.Contains(stripBody, "block-size:var(--header-h-tv)") {
		violations = append(violations,
			"the TV navigation strip is not the TV header's height; §3.4 fixes the band at 5rem "+
				"because 56px targets in an 80px band is the arithmetic, not a guess")
	}

	return violations
}

// TestTheBuiltStylesheetSpacesTheTvStripByTheTargetGap is §10.5's spacing held
// on the built file.
func TestTheBuiltStylesheetSpacesTheTvStripByTheTargetGap(t *testing.T) {
	t.Parallel()

	for _, violation := range auditTvStripSpacing(built(t)) {
		t.Error(violation)
	}
}

// TestTheBuiltStylesheetTvStripAuditRejectsTheViolationItClaimsTo is the
// meta-proof: a fixed-pixel gap defeats the token the TV target audit floors,
// so the audit must demand the reference, not a number.
func TestTheBuiltStylesheetTvStripAuditRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	fixtures := map[string]string{
		"no strip":     `:root[data-ui=tv] .shell{position:relative}`,
		"fixed gap":    `:root[data-ui=tv] .shell-nav{gap:16px;block-size:var(--header-h-tv)}`,
		"no TV height": `:root[data-ui=tv] .shell-nav{gap:var(--target-gap);block-size:3.5rem}`,
	}

	for name, css := range fixtures {
		if violations := auditTvStripSpacing(css); len(violations) == 0 {
			t.Errorf("the audit accepts %q; it holds nothing", name)
		}
	}

	healthy := `:root[data-ui=tv] .shell-nav,:root[data-ui=tv-wide] .shell-nav{` +
		`block-size:var(--header-h-tv);gap:var(--target-gap)}`

	if violations := auditTvStripSpacing(healthy); len(violations) != 0 {
		t.Errorf("the audit rejects a healthy strip: %v", violations)
	}
}
