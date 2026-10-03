package events_test

// The negative controls for every §10.2 and §10.6 audit in `route_a11y_test.go`.
//
// # Why these are separate files
//
// Because an audit that has never rejected anything is not an audit. AGENTS.md states
// it three times in three different places, and each time for a reason that applies
// here:
//
//   - "**A gate test that cannot fail is not a gate**" — three of phase 5's first-draft
//     gate tests could not fail, and an audit that reports nothing is the same failure
//     with a green light on top.
//   - "**Renaming a test to match the pattern is not the fix**" — it makes the guard
//     quiet without making the gate stronger.
//   - "**An audit nobody can fail is not an audit, and neither is one no fixture
//     reaches**" — the second half bites here: a fixture that violates nothing makes
//     the "did it report" check pass while the rule never spoke.
//
// So this file has two controls, and both are needed:
//
//  1. `TestEveryRouteAuditRejectsTheViolationItClaimsTo` — each audit is run over a
//     minimal fixture built to violate exactly that rule, and each must produce at least
//     one finding **that mentions the thing it is about**. Minimal fixtures matter: a
//     fixture that violates three rules makes the count say nothing.
//  2. `TestEveryRouteAuditPassesOnAFragmentThisRouteCouldServe` — each audit must be
//     **silent** over the route's own real fragments, and must fire over the matching
//     fixture. Together these two catch an audit that fires for an unrelated reason,
//     which control 1 alone would not.
//
// # `silentFailer`, and why the messages are kept
//
// The rules take an `auditFailer` rather than a `*testing.T` precisely so they can be
// *tested*. The count is kept, and the fragment each case must mention is pinned per
// row, because a rule that fires for the wrong reason is still a rule that appears to
// work.

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// silentFailer is a `*testing.T` whose `Errorf` is intercepted.
type silentFailer struct {
	*testing.T

	failures int
	messages []string
}

// Errorf records the failure and carries on, so one fixture produces a count and a list
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

// auditViolations is the fixture table: one minimal document per rule, each violating
// exactly that rule.
//
// **Each body is otherwise a fragment this route would plausibly emit**, which is what
// makes the row a real assertion rather than a formality: an audit that fired for an
// unrelated reason would report a fragment the case does not name and fail the check.
var auditViolations = []struct {
	// name is the case name and describes the violation.
	name string

	// why says what a correct audit would miss if it did not check this. It is read by
	// the reader of a failure here, which is the only reader there is.
	why string

	// body is the minimal fixture.
	body string

	// check is the audit under test.
	check func(auditFailer, *docAudit)

	// wantOne is a fragment only that audit's messages can contain. Pinned per case
	// because the messages are kept: "did it report anything" is the weak check, and it
	// is the one that lets a rule look covered while the thing it names is unchecked.
	wantOne string
}{
	{
		name: "a repeated hook",
		why: "A `data-chrome` on every row of a fragment appended into a list appears " +
			"N times and selects nothing, and §10.2's hook rule is what catches it.",
		body:    `<ol><li data-chrome="chat-log">a</li><li data-chrome="chat-log">b</li></ol>`,
		check:   assertHooksAreUnique,
		wantOne: "more than once",
	},
	{
		name:    "an empty hook",
		why:     "A hook with no value selects nothing, and it is not an absent hook.",
		body:    `<p data-testid="">x</p>`,
		check:   assertHooksAreUnique,
		wantOne: "empty data-testid",
	},
	{
		name: "the word in a text node",
		why: "§1.2's rule in its plainest form, and the only one a substring check " +
			"over raw markup would find for the wrong reason.",
		body:    `<p>the World of this campaign</p>`,
		check:   assertNoRetiredEntityIsNamed,
		wantOne: "a text node",
	},
	{
		// The case a rendered-document reader skips: nobody sees a comment, which is
		// precisely why it must be included rather than skipped.
		name: "the word in an HTML comment",
		why: "A comment is the one place a renderer is tempted to leave a retired " +
			"entity, because nobody sees it.",
		body:    `<p>x</p><!-- the world of this campaign -->`,
		check:   assertNoRetiredEntityIsNamed,
		wantOne: "an HTML comment",
	},
	{
		name:    "the word in an aria-label",
		why:     "An `aria-label` is announced to a reader, so a retired entity named there is named to the person the rule protects.",
		body:    `<p aria-label="the session log">x</p>`,
		check:   assertNoRetiredEntityIsNamed,
		wantOne: "the attribute aria-label",
	},
	{
		// The nastiest one: a scan over attribute *values* would miss it and a scan over
		// the raw markup would find it.
		name:    "the word in an attribute name",
		why:     "Invisible to a reader and obvious to a developer grepping for the feature it implies, which makes it as retired as a label.",
		body:    `<div data-world="true"></div>`,
		check:   assertNoRetiredEntityIsNamed,
		wantOne: "an attribute name",
	},
	{
		name:    "a positive tabindex",
		why:     "§7.4's gate failure, and the sign case is the one a string comparison gets wrong.",
		body:    `<li tabindex=" +3 ">x</li>`,
		check:   assertNoPositiveTabindex,
		wantOne: "tabindex",
	},
	{
		name:    "a zero tabindex",
		why:     "Falsy in a language that lets you write it; a check for a non-zero value passes this.",
		body:    `<li tabindex="0">x</li>`,
		check:   assertNoPositiveTabindex,
		wantOne: "only -1 is permitted",
	},
	{
		name:    "an unparseable tabindex",
		why:     "Three browsers disagreeing about the tab order is the failure, and `+3` sorted before `0` in a string comparison.",
		body:    `<li tabindex="first">x</li>`,
		check:   assertNoPositiveTabindex,
		wantOne: "not an integer",
	},
	{
		name:    "an inline outline suppression",
		why:     "§10.2's focus rule has a stylesheet half this file cannot see, and the inline half is the half a fragment can carry.",
		body:    `<p style="outline: none">x</p>`,
		check:   assertNoInlineOutlineSuppression,
		wantOne: "outline: none",
	},
	{
		name:    "a focusable element that is aria-hidden",
		why:     "A control a screen reader cannot see is one that reader cannot reach.",
		body:    `<a href="/x" aria-hidden="true">X</a>`,
		check:   assertNoAriaHiddenOnAFocusStop,
		wantOne: "focusable and aria-hidden",
	},
	{
		name:    "a focusable element inside an aria-hidden subtree",
		why:     "The attribute is on an ancestor several levels up, so an audit walking only the element fails this.",
		body:    `<div aria-hidden="true"><a href="/x">X</a></div>`,
		check:   assertNoAriaHiddenOnAFocusStop,
		wantOne: "contains focusable elements",
	},
	{
		name:    "a duplicated id",
		why:     "A fragment is appended into a list, so a repeated id arrives twice and every reference to it is ambiguous.",
		body:    `<section id="dup"><p id="dup">x</p></section>`,
		check:   assertReferencesResolveWithinTheFragment,
		wantOne: "appears 2 times",
	},
	{
		name:    "a script element",
		why:     "§7.5 requires rendered fragments the client *places*; a script in one is the one piece of this surface whose code a reader could author.",
		body:    `<p>a</p><script>alert(1)</script>`,
		check:   assertNoRawScriptOrEventHandler,
		wantOne: "<script> element",
	},
	{
		// **A real handler attribute, not an escaped one.** The fixture that looks more
		// dangerous — a payload carrying `&quot; onfocus=&quot;` inside an attribute
		// value — is *not* a violation of this rule: the parser decodes it into the
		// value, there is no `onfocus` attribute, and the audit is right to be silent.
		// Escaping is a different claim, and it is held as a positive assertion over the
		// product in `TestAHostileChatBodyArrivesEscaped`; a fixture here that relied on
		// it would be testing the parser rather than the audit.
		name:    "an event handler attribute",
		why:     "A chat message body is the one thing on this surface a reader cannot be trusted to have authored.",
		body:    `<p onfocus="alert(1)">x</p>`,
		check:   assertNoRawScriptOrEventHandler,
		wantOne: "onfocus",
	},
}

// TestEveryRouteAuditRejectsTheViolationItClaimsTo is the negative control: each §10.2
// rule is run over a minimal document built to violate exactly that rule, and each must
// produce at least one finding **that mentions the thing it is about**.
func TestEveryRouteAuditRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	for _, testCase := range auditViolations {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			counter := &silentFailer{T: t}

			testCase.check(counter, &docAudit{
				t:     counter,
				where: testCase.name,
				root:  parseFixture(t, testCase.body),
			})

			if counter.failures == 0 {
				t.Errorf("the audit reported nothing for a document that violates it. %s. "+
					"An audit that reports nothing is the same failure as no audit, with "+
					"a green light on top", testCase.why)
			}

			if !counter.mentions(testCase.wantOne) {
				t.Errorf("the audit fired %d time(s) but none of its messages mentions "+
					"%q, which is the thing this rule is about; a rule that reports "+
					"something else has not been shown to catch this. Messages: %v",
					counter.failures, testCase.wantOne, counter.messages)
			}
		})
	}
}

// TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch is the control that catches an
// audit firing for an unrelated reason.
//
// Each audit is run over a fragment that **violates nothing this route's rules care
// about**, and must be silent. Without this, control 1 would pass for a landmark audit
// that is really reporting duplicate hooks: the fixture would produce a finding, the
// count would be non-zero, and the gate would look covered while the rule under test
// was never exercised.
func TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch(t *testing.T) {
	t.Parallel()

	// One fixture per rule, each **correct for that rule**. They are not one shared
	// fixture, because a shared one cannot be simultaneously correct for "no positive
	// tabindex" and for "no retired entity" while violating nothing — and a fixture
	// that violated something would make the silence requirement wrong rather than
	// strict.
	for _, testCase := range []struct {
		name  string
		body  string
		check func(auditFailer, *docAudit)
	}{
		{
			name:  "unique, non-empty hooks",
			body:  `<li class="card chat-message" data-seq="7"><p>It opens.</p></li>`,
			check: assertHooksAreUnique,
		},
		{
			name:  "no retired entity anywhere",
			body:  `<p aria-label="The Table remembers">A roll of 2d6+3.</p>`,
			check: assertNoRetiredEntityIsNamed,
		},
		{
			name:  "only a minus-one tabindex",
			body:  `<div tabindex="-1">x</div>`,
			check: assertNoPositiveTabindex,
		},
		{
			name: "a style attribute that suppresses nothing",
			//nolint:misspell // a CSS declaration's own spelling; this row is about
			// `outline-offset` and `outline-colour` suppressing nothing
			body:  `<p style="outline-offset: 2px; outline-color: red">x</p>`,
			check: assertNoInlineOutlineSuppression,
		},
		{
			name:  "nothing aria-hidden at all",
			body:  `<p aria-hidden="false">x</p><div aria-hidden="true"><span>x</span></div>`,
			check: assertNoAriaHiddenOnAFocusStop,
		},
		{
			name:  "one id, referenced from nowhere",
			body:  `<section id="once"><p>x</p></section>`,
			check: assertReferencesResolveWithinTheFragment,
		},
		{
			// The control's own half: a fragment carrying **no** script and no handler is
			// the shape every one of this route's fragments has, so an audit that fired
			// here would be firing on every frame the route writes.
			name:  "plain markup",
			body:  `<section class="live-notice-item"><h4>Watch</h4><p>Pages may lag.</p></section>`,
			check: assertNoRawScriptOrEventHandler,
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
				t.Errorf("the audit reported %d finding(s) about a fragment it is "+
					"satisfied with: %v. An audit that fires where it has nothing to say "+
					"is as useless as one that never fires, because the failures it "+
					"reports are not the failures a reader has",
					counter.failures, counter.messages)
			}
		})
	}
}

// TestEveryRouteAuditPassesOnAFragmentThisRouteCouldServe is the third control, and
// it catches the failure neither of the others can.
//
// A rule whose fixtures it satisfies and whose **product** it rejects is worse than no
// rule: it is green on its own fixtures and red on the thing it was written for, and
// the first person to see that is whoever next touches the markup.
//
// Every kind is covered, and that is not belt and braces: the editor's notice and the
// sidebar's fragments are markup from two different component packages with two
// different escaping paths, so a rule that passes on one has not been shown anything
// about the other.
func TestEveryRouteAuditPassesOnAFragmentThisRouteCouldServe(t *testing.T) {
	t.Parallel()

	checks := []struct {
		name  string
		check func(auditFailer, *docAudit)
	}{
		{name: "Hooks", check: assertHooksAreUnique},
		{name: "Vocabulary", check: assertNoRetiredEntityIsNamed},
		{name: "NoPositiveTabindex", check: assertNoPositiveTabindex},
		{name: "NoInlineOutlineSuppression", check: assertNoInlineOutlineSuppression},
		{name: "NoAriaHiddenOnAFocusStop", check: assertNoAriaHiddenOnAFocusStop},
		{name: "EveryReferenceResolves", check: assertReferencesResolveWithinTheFragment},
		{name: "EveryElementIsOpaque", check: assertNoRawScriptOrEventHandler},
	}

	for _, doc := range auditedFragments(t) {
		for _, audited := range checks {
			t.Run(doc.where+" — "+audited.name, func(t *testing.T) {
				t.Parallel()

				counter := &silentFailer{T: t}
				audited.check(counter, parseDocument(t, doc))

				if counter.failures != 0 {
					t.Errorf("the %s audit reported %d finding(s) about a fragment this "+
						"route really writes: %v. An audit whose product it rejects is "+
						"worse than no audit: it is green on its own fixtures and red on "+
						"the thing it was written for",
						audited.name, counter.failures, counter.messages)
				}
			})
		}
	}
}

// TestTheFragmentsAreDistinctDocuments is what makes the third control meaningful: the
// route's fragments must actually differ, or the control is running one fragment eight
// times.
//
// **Without this, the control could pass while one state was audited.** A future
// change that rendered the same markup for the chat line and the dice line — plausible,
// and tempting, because "a log row is a log row" is a natural simplification — would
// leave every audit satisfied and the second fragment's escaping unchecked.
func TestTheFragmentsAreDistinctDocuments(t *testing.T) {
	t.Parallel()

	fragments := auditedFragments(t)

	seen := map[string]string{}

	for _, doc := range fragments {
		body := string(doc.body)

		if previous, duplicate := seen[body]; duplicate {
			t.Errorf("%s and %s are byte-identical fragments. Every kind must render its "+
				"own markup: a shared one is a second renderer for a second thing, and "+
				"the audits above would run one document eight times",
				previous, doc.where)
		}

		seen[body] = doc.where
	}
}

// parseFixture parses a minimal document written to trip one rule.
func parseFixture(t *testing.T, body string) *html.Node {
	t.Helper()

	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	return root
}
