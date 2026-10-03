package plugins_test

// The negative controls for every §10.2 and §10.6 audit in `route_a11y_test.go`.
//
// # Why these are separate files
//
// Because an audit that has never rejected anything is not an audit. AGENTS.md states it
// three times in three different places, and each time for a reason that applies here:
//
//   - "**A gate test that cannot fail is not a gate**" — three of phase 5's first-draft gate
//     tests could not fail, and an audit that reports nothing is the same failure with a
//     green light on top.
//   - "**Renaming a test to match the pattern is not the fix**" — it makes the guard quiet
//     without making the gate stronger.
//   - "**An audit nobody can fail is not an audit, and neither is one no fixture reaches**" —
//     the second half is the one that bites here: a fixture that violates nothing makes the
//     "did it report" check pass while the rule never spoke.
//
// So this file has **three** controls, and the third is the one most audits skip:
//
//  1. `TestEveryRouteAuditRejectsTheViolationItClaimsTo` — each audit is run over a minimal
//     fixture built to violate exactly that rule, and each must produce at least one finding
//     **that mentions the thing it is about**. Minimal fixtures matter: a fixture that
//     violates three rules makes the count say nothing.
//  2. `TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch` — each audit is run over a
//     document that is *correct* on the other rules, and must be silent. This is the control
//     that catches an audit which fires for an unrelated reason: without it, control 1 would
//     pass for a rule that is really reporting something else.
//  3. `TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe` — each audit must pass over
//     the route's own real documents. This catches an audit whose fixtures it satisfies and
//     whose product it rejects, which is the "an audit nobody can fail" failure one level
//     further out.
//
// # `silentFailer`, and why the messages are kept
//
// The rules take an `auditFailer` rather than a `*testing.T` precisely so they can be
// *tested* — a test of a `*testing.T`-typed function is a test that fails the suite. The
// messages are recorded as well as counted, because a rule that fires for the wrong reason
// is still a rule that appears to work: a landmark audit that also reported duplicate test
// hooks would pass a fixture built to trip the landmark rule, and the gate would look covered
// while the thing it names was not being checked.

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// silentFailer is a `*testing.T` whose `Errorf` is intercepted, so a rule can be *tested*
// rather than merely run.
type silentFailer struct {
	*testing.T

	failures int
	messages []string
}

// Errorf records the failure and carries on, so one document produces a count and a list
// rather than stopping at the first violation.
func (f *silentFailer) Errorf(format string, args ...any) {
	f.failures++
	f.messages = append(f.messages, fmt.Sprintf(format, args...))
	f.Logf(format, args...)
}

// Helper satisfies the rules' `t.Helper()` calls without recording anything.
func (f *silentFailer) Helper() {}

// mentions reports whether any recorded message contains a fragment.
func (f *silentFailer) mentions(fragment string) bool {
	for _, message := range f.messages {
		if strings.Contains(message, fragment) {
			return true
		}
	}

	return false
}

// violations are the fixtures, one per rule, each violating exactly that rule.
//
// **Each body is otherwise a document that satisfies every other rule**, which is what makes
// `wantOne` a real assertion rather than a formality: an audit that fired for an unrelated
// reason would report a fragment the case does not name and fail the check.
var violations = []struct {
	// name is the case name and describes the violation.
	name string

	// why says what a correct audit would miss if it did not check this. It is read by the
	// reader of a failure here, which is the only reader there is.
	why string

	// body is the minimal fixture.
	body string

	// check is the audit under test.
	check func(auditFailer, *docAudit)

	// wantOne is a fragment only that audit's messages can contain. Pinned per case for the
	// reason the messages are kept: "did it report anything" is the weak check, and it is
	// the one that lets a rule look covered while the thing it names is unchecked.
	wantOne string
}{
	{
		name: "a second h1",
		why:  "§10.2's first rule, and a plugin that rendered its own heading would do exactly this.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1><h1>Also roller</h1></main>`,
		),
		check:   assertExactlyOneH1,
		wantOne: "<h1>",
	},
	{
		name: "no h1 at all",
		why:  "One direction only is half the rule: none of the h1 rule looks like looking complete.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><p>Roller.</p></main>`,
		),
		check:   assertExactlyOneH1,
		wantOne: "<h1>",
	},
	{
		name: "a heading that skips a level",
		why:  "Fails where the h1 count passes: a document starting at h3 has one h1-shaped hole and no outline.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1><h3>Notation</h3></main>`,
		),
		check:   assertHeadingLevelsNeverSkip,
		wantOne: "jumps from h1 to h3",
	},
	{
		name: "no main landmark",
		why:  "A missing landmark is a region a landmark-navigation reader cannot jump to, and this document has no other landmark.",
		body: `<html><head><title>t</title></head><body><a class="skip-link target" href="#main">` +
			`Skip to content</a><h1 data-testid="page-title">Roller</h1></body></html>`,
		check:   assertLandmarksArePresentAndDistinguishing,
		wantOne: "no main landmark",
	},
	{
		name: "two main landmarks",
		why:  "Two articles and no way to choose between them; the h1 rule would pass on this document.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></main>` +
				`<main id="second" role="main" tabindex="-1" class="target"><p>Also roller.</p></main>`,
		),
		check:   assertLandmarksArePresentAndDistinguishing,
		wantOne: "main landmarks, want 1",
	},
	{
		name: "a navigation landmark mislabelled",
		why:  "§7.2's fixed names, and a plugin adding a nav would get this wrong.",
		body: validBody(
			`<nav id="nav" role="navigation" aria-label="Extras" tabindex="-1" class="target"></nav>` +
				`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></main>`,
		),
		check:   assertLandmarksArePresentAndDistinguishing,
		wantOne: "navigation landmark is labelled",
	},
	{
		name: "a complementary landmark mislabelled",
		why:  "§7.2 fixes this name too, and the same plugin that adds a rail would name it.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></main>` +
				`<aside id="rail" role="complementary" aria-label="Extras" tabindex="-1" class="target"></aside>`,
		),
		check:   assertLandmarksArePresentAndDistinguishing,
		wantOne: "complementary landmark is labelled",
	},
	{
		name: "two landmarks with the same name",
		why:  "Distinguishing labels are §10.2's requirement, and this is the only rule that catches a repeated one.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></main>` +
				`<aside id="rail" role="complementary" aria-label="Utilities" tabindex="-1" class="target"></aside>` +
				`<aside id="rail2" role="complementary" aria-label="Utilities" tabindex="-1" class="target"></aside>`,
		),
		check:   assertLandmarksArePresentAndDistinguishing,
		wantOne: "both labelled",
	},
	{
		name: "a landmark the record does not place on a plugin page",
		why:  "A region a reader can jump to and nobody knows what it is for — a plugin could add one and nothing else would notice.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<section role="region" aria-label="Extras"></section></main>`,
		),
		check:   assertLandmarksArePresentAndDistinguishing,
		wantOne: "is not one UI §7.2's plugin",
	},
	{
		name: "a positive tabindex",
		why:  "§7.4's gate failure, and the sign case is the one a string comparison gets wrong.",
		body: validBody(
			`<main id="main" role="main" tabindex=" +3 " class="target"><h1 data-testid="page-title">Roller</h1></main>`,
		),
		check:   assertNoPositiveTabindex,
		wantOne: "tabindex",
	},
	{
		name: "a zero tabindex",
		why:  "Falsy in a language that lets you write it; a check for a non-zero value passes this.",
		body: validBody(
			`<main id="main" role="main" tabindex="0" class="target"><h1 data-testid="page-title">Roller</h1></main>`,
		),
		check:   assertNoPositiveTabindex,
		wantOne: "only -1 is permitted",
	},
	{
		name: "an inline outline suppression",
		why:  "The only half of §10.2's focus rule a document can carry, and only a plugin could introduce one.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title" style="outline: none">Roller</h1></main>`,
		),
		check:   assertNoInlineOutlineSuppression,
		wantOne: "outline: none",
	},
	{
		name: "a focusable element that is aria-hidden",
		why:  "A control a screen reader cannot see is one that reader cannot reach.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<a class="target" href="/x" aria-hidden="true">X</a></main>`,
		),
		check:   assertNoAriaHiddenOnAFocusStop,
		wantOne: "focusable and aria-hidden",
	},
	{
		name: "a focusable element inside an aria-hidden subtree",
		why:  "The attribute is on an ancestor several levels up, so an audit walking only the element fails this.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<div aria-hidden="true"><a class="target" href="/x">X</a></div></main>`,
		),
		check:   assertNoAriaHiddenOnAFocusStop,
		wantOne: "contains focusable elements",
	},
	{
		name: "a skip link after the landmark",
		why:  "A skip link after the banner is not a skip link; it is a link named skip.",
		body: `<html><head><title>t</title></head><body>` +
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></main>` +
			`<a class="skip-link target" href="#main">Skip to content</a></body></html>`,
		check:   assertSkipLinksComeFirstAndResolve,
		wantOne: "skip links first in tab order",
	},
	{
		name: "a skip link to an absent landmark",
		why:  "Focus moves nowhere, which costs a keypress and teaches the reader the skip links are unreliable.",
		body: `<html><head><title>t</title></head><body>` +
			`<a class="skip-link target" href="#nowhere">Skip to content</a>` +
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></main>` +
			`</body></html>`,
		check:   assertSkipLinksComeFirstAndResolve,
		wantOne: "not in the document",
	},
	{
		name: "a skip link to a landmark with no tabindex",
		why:  "Focus lands at the top of a scroll container rather than on an announced element.",
		body: `<html><head><title>t</title></head><body>` +
			`<a class="skip-link target" href="#main">Skip to content</a>` +
			`<main id="main" role="main" class="target"><h1 data-testid="page-title">Roller</h1></main>` +
			`</body></html>`,
		check:   assertSkipLinksComeFirstAndResolve,
		wantOne: "has no tabindex",
	},
	{
		name: "a skip link to an element that is not a landmark",
		why:  "A skip link's target is the region it names, so a link to a div names nothing.",
		body: `<html><head><title>t</title></head><body>` +
			`<a class="skip-link target" href="#main">Skip to content</a>` +
			`<div id="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></div>` +
			`</body></html>`,
		check:   assertSkipLinksComeFirstAndResolve,
		wantOne: "not a landmark",
	},
	{
		// The direction a rendering cannot show: a landmark with no link to it.
		name: "a navigation landmark with no skip link",
		why:  "§7.2 lists the link where the nav exists, and a landmark with no link is a region nobody can jump to.",
		body: `<html><head><title>t</title></head><body>` +
			`<a class="skip-link target" href="#main">Skip to content</a>` +
			`<nav id="nav" role="navigation" aria-label="Campaign" tabindex="-1" class="target"></nav>` +
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></main>` +
			`</body></html>`,
		check:   assertSkipLinksComeFirstAndResolve,
		wantOne: "no skip link to",
	},
	{
		// And the other direction, which is what a future change adding the nav to this
		// document without the link would produce.
		name: "a skip link to a navigation landmark that is not there",
		why:  "§4.6 removes the nav before a campaign exists; this route's document has none, so neither does its link.",
		body: `<html><head><title>t</title></head><body>` +
			`<a class="skip-link target" href="#main">Skip to content</a>` +
			`<a class="skip-link target" href="#nav">Skip to campaign navigation</a>` +
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></main>` +
			`</body></html>`,
		check:   assertSkipLinksComeFirstAndResolve,
		wantOne: "no navigation landmark",
	},
	{
		name: "a repeated data-testid",
		why:  "A hook that appears twice selects nothing, and every assertion built on it selects the first.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<p data-testid="page-title">x</p></main>`,
		),
		check:   assertEveryTestIDIsPresent,
		wantOne: "more than once",
	},
	{
		name: "an empty data-testid",
		why:  "A hook with no value selects nothing, and it is not an absent hook — counting the empty string as one is how this first reported fifteen duplicates on a correct shell.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<p data-testid="">x</p></main>`,
		),
		check:   assertEveryTestIDIsPresent,
		wantOne: "empty data-testid",
	},
	{
		// The coverage half, on a hook the shell owns. **A fixture that dropped several
		// hooks at once would report several and the count would say nothing** — which is
		// exactly what the first version of this case did, and it failed for the right
		// reason: the audit named the *other* three missing hooks rather than the one this
		// row is about, so the case was passing on the wrong message.
		name: "a promise the document shell made and did not keep",
		why:  "A route that stopped rendering its own title hook would satisfy every-hook-present by rendering none.",
		body: `<html><head><title>t</title></head><body>` +
			`<a class="skip-link target" href="#main" data-testid="skip-to-main">Skip to content</a>` +
			`<main id="main" role="main" tabindex="-1" class="target" data-testid="shell-main">` +
			`<h2>Roller</h2>` +
			`<form class="form" method="post" data-testid="roll-form">` +
			`<button class="target" type="submit" data-testid="roll-submit">Roll</button>` +
			`</form></main></body></html>`,
		check:   assertEveryTestIDIsPresent,
		wantOne: `no "page-title" hook`,
	},
	{
		name: "both an applied and a refused outcome",
		why:  "A roll is applied, refused, or has not happened; two outcome blocks render two different things at once.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<div data-testid="roll-result"></div><div data-testid="roll-refused"></div></main>`,
		),
		check:   assertEveryTestIDIsPresent,
		wantOne: "outcome hooks",
	},
	{
		name: "the word in a text node",
		why:  "§1.2's rule in its plainest form, and the only one a substring check would find.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<p>the World of this campaign</p></main>`,
		),
		check:   assertNoRetiredEntityIsNamed,
		wantOne: "a text node",
	},
	{
		// The case a rendered-document reader skips: nobody sees a comment, which is
		// precisely why it must be included rather than skipped.
		name: "the word in an HTML comment",
		why:  "A comment is the one place a renderer is tempted to leave a retired entity, because nobody sees it.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<!-- the world of this campaign --></main>`,
		),
		check:   assertNoRetiredEntityIsNamed,
		wantOne: "an HTML comment",
	},
	{
		name: "the word in an aria-label",
		why:  "An aria-label is announced to a reader, so a retired entity named there is named to the person the rule protects.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<nav aria-label="game sessions"></nav></main>`,
		),
		check:   assertNoRetiredEntityIsNamed,
		wantOne: "the attribute aria-label",
	},
	{
		name: "the word in a title attribute",
		why:  "A tooltip is read aloud; the attribute value is part of what a reader is told.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<span title="Your session">x</span></main>`,
		),
		check:   assertNoRetiredEntityIsNamed,
		wantOne: "the attribute title",
	},
	{
		// The nastiest one: a scan over attribute *values* would miss it and a scan over the
		// raw markup would find it.
		name: "the word in an attribute name",
		why:  "Invisible to a reader and obvious to a developer grepping for the feature it implies, which makes it as retired as a label.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<div data-world="true"></div></main>`,
		),
		check:   assertNoRetiredEntityIsNamed,
		wantOne: "an attribute name",
	},
	{
		name: "a focus stop with no target class",
		why:  "§10.6's rule, and §10.6 says explicitly that it covers plugin output — which nothing else about this route would catch.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<a class="link" href="/x">X</a></main>`,
		),
		check:   assertEveryFocusStopCarriesTarget,
		wantOne: ".target",
	},
	{
		// A `<button>` rather than an anchor, so the element type is the variable and an
		// audit that only walked `a[href]` would pass this row.
		name: "a button with no target class",
		why:  "Seven element types are focus stops and an audit that only looks for anchors catches one of them.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<button type="button">Roll</button></main>`,
		),
		check:   assertEveryFocusStopCarriesTarget,
		wantOne: ".target",
	},
	{
		// The tabindex branch, which is the one an `a[href]`-shaped audit never reaches.
		name: "an element with a tabindex and no target class",
		why:  "§10.6's list names [tabindex] as a focus stop, so an anchor-only audit is a rule with a hole in it.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<div class="region" tabindex="0">R</div></main>`,
		),
		check:   assertEveryFocusStopCarriesTarget,
		wantOne: ".target",
	},
	{
		name: "a reference that resolves to nothing",
		why:  "A control claiming to control something invisible is a control that lies.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<button class="target" aria-controls="nowhere">Roll</button></main>`,
		),
		check:   assertEveryReferenceResolves,
		wantOne: "resolves to no element",
	},
	{
		// The branch this route actually exercises: the roller's form has three labelled
		// fields, and an id renamed in one place only is a field with no name.
		name: "a label for nothing",
		why:  "§7.7's rule, and a widget's form is where it is most likely to be broken.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<label for="nowhere">Expression</label></main>`,
		),
		check:   assertEveryReferenceResolves,
		wantOne: "no accessible name",
	},
	{
		name: "a duplicated id",
		why:  "Two elements sharing an id make every reference ambiguous, and a screen reader picks the first.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<span id="dup"></span><span id="dup"></span></main>`,
		),
		check:   assertEveryReferenceResolves,
		wantOne: "appears 2 times",
	},
	{
		// The half of §10.6's rule that a `for`-attribute audit cannot reach, and it is
		// reachable on this route: the roller's submit button is the only control with no
		// label, because its own text is its name and a widget that added a `<label for>`
		// alongside it would be inventing a name.
		name: "a button whose accessible name is empty",
		why:  "A control the roller's outcome block could add with no text at all is announced as nothing.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<button class="target" type="submit"><img src="/x.png" alt=""></button></main>`,
		),
		check:   assertEveryReferenceResolves,
		wantOne: "no accessible name",
	},
	{
		// A focus stop whose class *contains* the token rather than being it — the case a
		// `strings.Contains(attr, "target")` check passes. Mutation-verified: the check was
		// `strings.Contains` until the card's own audit was tightened to the token list, and
		// this row is the fixture that made the difference visible.
		name: "a class that merely contains the target token as a substring",
		why:  "A styling rename that produced `untargeted` would satisfy a substring check and stop the --target-min minimum applying.",
		body: validBody(
			`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
				`<a class="untargeted" href="/x">X</a></main>`,
		),
		check:   assertEveryFocusStopCarriesTarget,
		wantOne: ".target",
	},
}

// validBody is a fixture shell that satisfies every rule the cases above do not target.
//
// **A helper rather than a repeated literal, because the alternative is a fixture whose only
// difference from a valid document is the violation — and getting that right by hand for
// twenty-six cases is how one of them quietly violates a second rule.** The shell is the
// smallest document that passes every audit: one skip link first, one `main` landmark
// carrying `tabindex` and `role` and the target class, and one `h1` carrying the route's
// promised hook.
func validBody(main string) string {
	return `<html><head><title>t</title></head><body>` +
		`<a class="skip-link target" href="#main" data-testid="skip-to-main">Skip to content</a>` +
		main +
		`</body></html>`
}

// TestEveryRouteAuditRejectsTheViolationItClaimsTo is the negative control: each §10.2 rule
// is run over a minimal document built to violate exactly that rule, and each must produce
// at least one finding **that mentions the thing it is about**.
func TestEveryRouteAuditRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	for _, testCase := range violations {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			counter := &silentFailer{T: t}
			root := parseFixture(t, testCase.body)

			testCase.check(counter, &docAudit{
				t:     counter,
				where: testCase.name,
				root:  root,
			})

			if counter.failures == 0 {
				t.Errorf("the audit reported nothing for a document that violates it. %s. "+
					"An audit that reports nothing is the same failure as no audit, with a "+
					"green light on top", testCase.why)
			}

			if !counter.mentions(testCase.wantOne) {
				t.Errorf("the audit fired %d time(s) but none of its messages mentions %q, "+
					"which is the thing this rule is about; a rule that reports something "+
					"else has not been shown to catch this. Messages: %v",
					counter.failures, testCase.wantOne, counter.messages)
			}
		})
	}
}

// TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch is the negative control's other
// half, and it is the one that catches an audit firing for the wrong reason.
//
// Each audit is run over a document that **violates nothing this route's rules care about**
// — with the rule under test deliberately not applied — and must be silent. Without this,
// control 1 would pass for a landmark audit that is really reporting duplicate test hooks:
// the fixture would produce a finding, the count would be non-zero, and the gate would look
// covered while the landmark rule was never exercised.
func TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch(t *testing.T) {
	t.Parallel()

	// One document per rule, each **correct for that rule**. They are not one shared
	// document, because a shared one cannot be simultaneously correct for "exactly one h1"
	// and for "no heading skips a level" while violating nothing — and a document that
	// violated something would make the silence requirement wrong rather than strict.
	for _, testCase := range []struct {
		name  string
		body  string
		check func(auditFailer, *docAudit)
	}{
		{
			name: "one h1, nothing else wrong",
			body: validBody(
				`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></main>`,
			),
			check: assertExactlyOneH1,
		},
		{
			name: "headings in order",
			body: validBody(
				`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
					`<h2>Notation</h2><h3>Detail</h3></main>`,
			),
			check: assertHeadingLevelsNeverSkip,
		},
		{
			name: "one main landmark, well named",
			body: validBody(
				`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></main>`,
			),
			check: assertLandmarksArePresentAndDistinguishing,
		},
		{
			name: "only a minus-one tabindex",
			body: validBody(
				`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></main>`,
			),
			check: assertNoPositiveTabindex,
		},
		{
			name: "a style attribute that suppresses nothing",
			body: validBody(
				`<main id="main" role="main" tabindex="-1" class="target">` +
					`<h1 data-testid="page-title" style="outline-offset: 2px">Roller</h1></main>`,
			),
			check: assertNoInlineOutlineSuppression,
		},
		{
			name:  "nothing aria-hidden at all",
			body:  validBody(`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></main>`),
			check: assertNoAriaHiddenOnAFocusStop,
		},
		{
			name: "a skip link that resolves",
			body: `<html><head><title>t</title></head><body>` +
				`<a class="skip-link target" href="#main" data-testid="skip-to-main">Skip to content</a>` +
				`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1></main>` +
				`</body></html>`,
			check: assertSkipLinksComeFirstAndResolve,
		},
		{
			name: "unique hooks, all promised ones present",
			body: `<html><head><title>t</title></head><body>` +
				`<a class="skip-link target" href="#main" data-testid="skip-to-main">Skip to content</a>` +
				`<main id="main" role="main" tabindex="-1" class="target" data-testid="shell-main">` +
				`<h1 data-testid="page-title">Roller</h1>` +
				`<form class="form" method="post" data-testid="roll-form">` +
				`<button class="target" type="submit" data-testid="roll-submit">Roll</button>` +
				`</form></main></body></html>`,
			check: assertEveryTestIDIsPresent,
		},
		{
			name: "no retired entity anywhere",
			body: validBody(
				`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
					`<p>The Table remembers.</p></main>`,
			),
			check: assertNoRetiredEntityIsNamed,
		},
		{
			name: "every focus stop carries the class",
			body: validBody(
				`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
					`<a class="target" href="/x">X</a></main>`,
			),
			check: assertEveryFocusStopCarriesTarget,
		},
		{
			// The class as a **token among others**, which is what every real focus stop
			// carries — `class="button button--primary target"`. A name check that demanded
			// the class attribute be exactly `target` would be silent here while being
			// useless on the product.
			name: "the class as one token among several",
			body: validBody(
				`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
					`<button class="button button--primary target" type="submit">Roll</button></main>`,
			),
			check: assertEveryFocusStopCarriesTarget,
		},
		{
			// Names from every source the accname order allows, because the name rule fires
			// when **all** of them are absent — and a document that satisfied three of the four
			// would be a silent pass if the check read only one.
			name: "focus stops named by every source the rule knows",
			body: validBody(
				`<main id="main" role="main" tabindex="-1" class="target">` +
					`<h1 data-testid="page-title">Roller</h1>` +
					`<span id="by">Named</span>` +
					`<button class="target" aria-labelledby="by" type="button">X</button>` +
					`<button class="target" aria-label="Roll" type="button">X</button>` +
					`<label for="labelled">Expression</label>` +
					`<input class="target" id="labelled" type="text">` +
					`<label>Reason <input class="target" type="text"></label>` +
					`</main>`,
			),
			check: assertEveryReferenceResolves,
		},
		{
			// A decorative image inside a named control. The `alt=""` is the case the accname
			// algorithm treats as *contributing nothing*, and a name computation that appended
			// it would still be right here — but the row exists so a future change that
			// returned the `alt` unconditionally is caught by the violation table's row, not
			// only by this one.
			name: "a control with a decorative image and real text",
			body: validBody(
				`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
					`<button class="target" type="button"><img src="/d.png" alt="">Roll</button></main>`,
			),
			check: assertEveryReferenceResolves,
		},
		{
			name: "references that all resolve",
			body: validBody(
				`<main id="main" role="main" tabindex="-1" class="target"><h1 data-testid="page-title">Roller</h1>` +
					`<p id="hint">A hint.</p><button class="target" aria-describedby="hint">Roll</button></main>`,
			),
			check: assertEveryReferenceResolves,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			counter := &silentFailer{T: t}

			testCase.check(counter, &docAudit{
				t:     counter,
				where: testCase.name,
				root:  parseFixture(t, testCase.body),
			})

			if counter.failures != 0 {
				t.Errorf("the audit reported %d finding(s) about a document it is satisfied "+
					"with: %v. An audit that fires where it has nothing to say is as useless "+
					"as one that never fires, because the failures it reports are not the "+
					"failures a reader has",
					counter.failures, counter.messages)
			}
		})
	}
}

// TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe is the third control, and it is
// the one that catches the failure neither of the others can.
//
// A rule whose fixtures it satisfies and whose **product** it rejects is worse than no rule,
// because it is green on its own fixtures and red on the thing it was written for — and the
// first person to see that is whoever next touches the markup. So each audit is run over the
// route's own real documents: the widget, an applied roll and a refused roll.
//
// **All three states, and that is not belt and braces.** The outcome blocks are markup nothing
// else on this route produces, so a rule about headings or focus stops can pass on the widget
// and fail on a roll — which is the whole reason `auditedDocuments` lists three.
func TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe(t *testing.T) {
	t.Parallel()

	checks := []struct {
		name  string
		check func(auditFailer, *docAudit)
	}{
		{name: "ExactlyOneH1", check: assertExactlyOneH1},
		{name: "HeadingLevelsNeverSkip", check: assertHeadingLevelsNeverSkip},
		{
			name:  "LandmarksArePresentAndDistinguishing",
			check: assertLandmarksArePresentAndDistinguishing,
		},
		{name: "NoPositiveTabindex", check: assertNoPositiveTabindex},
		{name: "NoInlineOutlineSuppression", check: assertNoInlineOutlineSuppression},
		{name: "NoAriaHiddenOnAFocusStop", check: assertNoAriaHiddenOnAFocusStop},
		{name: "SkipLinksComeFirstAndResolve", check: assertSkipLinksComeFirstAndResolve},
		{name: "EveryTestIDIsPresent", check: assertEveryTestIDIsPresent},
		{name: "Vocabulary", check: assertNoRetiredEntityIsNamed},
		{name: "EveryReferenceResolves", check: assertEveryReferenceResolves},
		{name: "TargetClass", check: assertEveryFocusStopCarriesTarget},
	}

	for _, doc := range auditedDocuments(t) {
		for _, audited := range checks {
			t.Run(doc.where+" — "+audited.name, func(t *testing.T) {
				t.Parallel()

				counter := &silentFailer{T: t}

				audited.check(counter, parseDocument(t, doc))

				if counter.failures != 0 {
					t.Errorf("the %s audit reported %d finding(s) about a document this route "+
						"really serves: %v. An audit whose product it rejects is worse than no "+
						"audit: it is green on its own fixtures and red on the thing it was "+
						"written for",
						audited.name, counter.failures, counter.messages)
				}
			})
		}
	}
}

// TestTheOutcomeStatesAreDistinctDocuments is what makes the third control meaningful: the
// widget, an applied roll and a refused roll must actually differ in their markup, or the
// control is running one document three times.
//
// **Without this, the third control could pass while only one state was ever audited.** A
// future change that rendered the same document for all three outcomes — plausible, and
// tempting, because "a refusal is the widget with a message" is a natural simplification —
// would leave every audit above satisfied and the refusal state unaudited. The observable is
// the outcome hook, which the template renders for exactly one state at a time.
func TestTheOutcomeStatesAreDistinctDocuments(t *testing.T) {
	t.Parallel()

	widget := auditedDocument("widget", widgetPage(t))
	applied := auditedDocument("applied", appliedRoll(t))
	refused := auditedDocument("refused", refusedRoll(t))

	states := map[string]renderedDocument{
		"widget":  widget,
		"applied": applied,
		"refused": refused,
	}

	hooks := map[string]string{
		"widget":  "",
		"applied": `data-testid="roll-result"`,
		"refused": `data-testid="roll-refused"`,
	}

	for name, doc := range states {
		body := string(doc.body)

		if hook := hooks[name]; hook != "" && !strings.Contains(body, hook) {
			t.Errorf("the %s document carries no %q; the three states must be distinct "+
				"documents or the a11y audits above run one document three times", name, hook)
		}

		for other, otherHook := range hooks {
			if other == name || otherHook == "" {
				continue
			}

			if strings.Contains(body, otherHook) {
				t.Errorf("the %s document also carries %q, which belongs to the %s state; "+
					"one document rendering two outcomes would let every audit pass while "+
					"only one state was checked", name, otherHook, other)
			}
		}
	}
}

// auditedDocument wraps a recorder as an audited document, so the distinctness check reads
// the same bytes the audits do.
//
// A plain `*httptest.ResponseRecorder` rather than an interface: `Result()` returns
// `*http.Response` and asserting a type on it to reach the body would be a cast in a test
// helper for no benefit, when the recorder itself *is* what the caller has.
func auditedDocument(where string, recorder *httptest.ResponseRecorder) renderedDocument {
	return fromRecorder(where, recorder)
}
