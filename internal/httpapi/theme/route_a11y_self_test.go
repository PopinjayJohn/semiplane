package theme_test

// The negative controls for every §10.2 and §10.6 audit in `route_a11y_test.go`.
//
// # Why these are separate files
//
// Because an audit that has never rejected anything is not an audit. AGENTS.md
// states it three times in three different places, and each time for a reason
// that applies here:
//
//   - "**A gate test that cannot fail is not a gate**" — an audit that reports
//     nothing is that failure with a green light on top.
//   - "**Renaming a test to match the pattern is not the fix**" — it makes the
//     guard quiet without making the gate stronger.
//   - "**An audit nobody can fail is not an audit, and neither is one no fixture
//     reaches**" — the second half is the one that bites on a route whose rules
//     are all *negative*. "The response contains no heading" is satisfied by
//     every response this route has ever served, so a fixture that violates
//     nothing makes the "did it report" check pass while the rule never spoke.
//
// So this file has three controls, and the third is the one most audits skip:
//
//  1. `TestEveryRouteAuditRejectsTheViolationItClaimsTo` — each audit is run over
//     a minimal fixture built to violate exactly that rule, and each must produce
//     at least one finding **that mentions the thing it is about**. Minimal
//     fixtures matter: a fixture that violates three rules makes the count say
//     nothing.
//  2. `TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch` — each audit is run
//     over a response that is correct on that rule, and must be silent. Without
//     it, control 1 would pass for a rule that is really reporting something
//     else.
//  3. `TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe` — each audit must
//     pass over the route's own four responses. This catches an audit whose
//     fixtures it satisfies and whose product it rejects.
//
// # Both controls were shown failing
//
// Control 1 and the byte-vs-tree claim behind the vocabulary rule, by mutation,
// each restored afterwards:
//
//   - `assertNoHeading` given an immediate `return` fails control 1 — the h1
//     fixture produces no finding, which is exactly the "an audit nobody can
//     fail" case. Control 2 stays green under that mutation, which is why the
//     two are separate files' worth of controls: silence on a clean response is
//     also what a vacuous rule produces.
//   - `assertNoRetiredEntityIsNamed` rewritten to read the parsed tree's visible
//     text instead of `audit.raw` fails control 1 on three fixtures at once —
//     the word in the bytes, the word in a comment and an `aria-label`, and the
//     word in a different case. That is the AGENTS.md rule stated from the other
//     side: a scan that cannot see those places is the weaker check, and here
//     the fixtures are what say so rather than an argument in a comment.
//
// # `silentFailer`, and why the messages are kept
//
// The rules take an `auditFailer` rather than a `*testing.T` precisely so they
// can be *tested* — a test of a `*testing.T`-typed function is a test that fails
// the suite. The messages are recorded as well as counted, because a rule that
// fires for the wrong reason is still a rule that appears to work: a landmark
// audit that also reported a retired word would pass a fixture built to trip the
// landmark rule, and the gate would look covered while the thing it names was
// not being checked.

import (
	"fmt"
	"strings"
	"testing"
)

// silentFailer is a `*testing.T` whose `Errorf` is intercepted, so a rule can be
// *tested* rather than merely run.
type silentFailer struct {
	*testing.T

	failures int
	messages []string
}

// Errorf records the failure and carries on, so one fixture produces a count and
// a list rather than stopping at the first violation.
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
// **Each body is otherwise a response that satisfies every other rule**, which is
// what makes `wantOne` a real assertion rather than a formality: an audit that
// fired for an unrelated reason would report a fragment the case does not name
// and fail the check.
var violations = []struct {
	// name is the case name and describes the violation.
	name string

	// why says what a correct audit would miss if it did not check this. It is read
	// by the reader of a failure here, which is the only reader there is.
	why string

	// body is the minimal fixture, and it is the bytes the vocabulary rule reads.
	body string

	// contentType is what the response claims to be; empty means `text/css`, which
	// is what this route serves.
	contentType string

	// check is the audit under test.
	check func(auditFailer, *docAudit)

	// wantOne is a fragment only that audit's messages can contain. Pinned per case
	// for the reason the messages are kept: "did it report anything" is the weak
	// check, and it is the one that lets a rule look covered while the thing it
	// names is unchecked.
	wantOne string
}{
	{
		name: "a response that claims to be HTML",
		why: "The first and loudest of the four: a subresource answering text/html is a " +
			"document the browser parses and discards, with an error nobody sees.",
		body:        `<p>root { background: red; }</p>`,
		contentType: "text/html; charset=utf-8",
		check:       assertNotHTMLContentType,
		wantOne:     "text/html",
	},
	{
		name:    "a heading in a stylesheet",
		why:     "A heading means the route has started rendering documents and owes readers the full §10.2 audit.",
		body:    validBody(`<h1>Theme</h1>`),
		check:   assertNoHeading,
		wantOne: "<h1>",
	},
	{
		name:    "a landmark by tag",
		why:     "A region a landmark-navigation reader can jump to, and no role attribute needed to make it one.",
		body:    validBody(`<main id="main"></main>`),
		check:   assertNoLandmark,
		wantOne: "<main>",
	},
	{
		name:    "a landmark by role",
		why:     "An audit that read only tags would pass a `<div role=\"navigation\">`, which is the same region by a different route.",
		body:    validBody(`<div role="navigation"></div>`),
		check:   assertNoLandmark,
		wantOne: `role="navigation"`,
	},
	{
		name:    "an anchor in a stylesheet",
		why:     "§10.6's element, and the one a stylesheet is most likely to grow through an error body that rendered a link.",
		body:    validBody(`<a href="/c/greyhaven/wiki/Theme">Theme</a>`),
		check:   assertNoFocusStop,
		wantOne: "focus stop",
	},
	{
		name:    "a tabindex in a stylesheet",
		why:     "Seven element types are focus stops, and `[tabindex]` is the one no tag-based audit would find.",
		body:    validBody(`<div tabindex="0">x</div>`),
		check:   assertNoFocusStop,
		wantOne: "focus stop",
	},
	{
		name: "a focus stop with no target class",
		why: "§10.6's rule on the one response where it could fire: a control that appeared here " +
			"would still owe readers the --target-min minimum.",
		body:    validBody(`<a href="/x">X</a>`),
		check:   assertEveryFocusStopCarriesTarget,
		wantOne: ".target",
	},
	{
		name:    "the word in the response's own bytes",
		why:     "§1.2's rule in its plainest form, and this is the whole of this route's vocabulary rule.",
		body:    validBody(`<p>the World of this campaign</p>`),
		check:   assertNoRetiredEntityIsNamed,
		wantOne: `"world"`,
	},
	{
		name: "the word in a place a visible-text scan cannot see",
		why: "The case AGENTS.md names: an HTML comment, an aria-label and a data- attribute are " +
			"all invisible to a scan of rendered text and all present in the bytes.",
		body: validBody(
			`<p>x</p><!-- the session of play --><span aria-label="your session"></span>` +
				`<div data-session="true"></div>`,
		),
		check:   assertNoRetiredEntityIsNamed,
		wantOne: `"session"`,
	},
	{
		name: "the word spelled differently in the same response",
		why: "Case-insensitivity, because the failure being guarded against is a noun phrase in a " +
			"lower-case sentence rather than a capitalised title.",
		body:    validBody(`<p>Your SESSION has expired.</p>`),
		check:   assertNoRetiredEntityIsNamed,
		wantOne: `"session"`,
	},
}

// validBody is a fixture shell that satisfies every rule the cases above do not
// target.
//
// **A helper rather than a repeated literal, because the alternative is a fixture
// whose only difference from a correct response is the violation — and getting
// that right by hand for ten cases is how one of them quietly violates a second
// rule.** The shell is the smallest body that passes every audit: a paragraph,
// and nothing a document would carry.
func validBody(inner string) string {
	return `<!doctype html><html><head><title>t</title></head><body><p>Theme.</p>` +
		inner +
		`</body></html>`
}

// TestEveryRouteAuditRejectsTheViolationItClaimsTo is the negative control: each
// rule is run over a minimal fixture built to violate exactly that rule, and each
// must produce at least one finding **that mentions the thing it is about**.
func TestEveryRouteAuditRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	for _, testCase := range violations {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			counter := &silentFailer{T: t}

			testCase.check(counter, fixtureAudit(t, testCase.name, testCase.body,
				testCase.contentType))

			if counter.failures == 0 {
				t.Errorf("the audit reported nothing for a response that violates it. %s. "+
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

// fixtureAudit builds a `docAudit` over a written fixture.
//
// The bytes are kept as well as the tree, because the vocabulary rule reads the
// bytes and a `docAudit` built without them would be a fixture the rule under
// test could not possibly fail — which is the vacuous pass this file exists to
// prevent.
func fixtureAudit(t *testing.T, where, body, contentType string) *docAudit {
	t.Helper()

	if contentType == "" {
		contentType = "text/css"
	}

	return &docAudit{
		t:           t,
		where:       where,
		status:      200,
		contentType: contentType,
		raw:         []byte(body),
		root:        parseFixture(t, body),
	}
}

// TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch is the negative
// control's other half, and it is the one that catches an audit firing for the
// wrong reason.
//
// Each audit is run over a response that **violates nothing this route's rules
// care about** — with the rule under test deliberately satisfied — and must be
// silent. Without this, control 1 would pass for a landmark audit that is really
// reporting retired words: the fixture would produce a finding, the count would
// be non-zero, and the gate would look covered while the landmark rule was never
// exercised.
func TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name        string
		body        string
		contentType string
		check       func(auditFailer, *docAudit)
	}{
		{
			name:        "a stylesheet-shaped content type",
			body:        `:root { --bg: #f7f6f3; }`,
			contentType: "text/css; charset=utf-8",
			check:       assertNotHTMLContentType,
		},
		{
			name:  "paragraphs and nothing a document would carry",
			body:  validBody(`<p>The Table remembers.</p>`),
			check: assertNoHeading,
		},
		{
			name:  "no region at all",
			body:  validBody(`<p>The Table remembers.</p>`),
			check: assertNoLandmark,
		},
		{
			name:  "nothing a keyboard could reach",
			body:  validBody(`<p>The Table remembers.</p>`),
			check: assertNoFocusStop,
		},
		{
			// The class as **the only thing that matters here**: §10.6's rule fires on
			// an element that is focusable and unclassed, so a fixture carrying a
			// focusable element with the class is the shape that must stay silent.
			name:  "a focus stop that carries the class",
			body:  validBody(`<a class="target" href="/x">X</a>`),
			check: assertEveryFocusStopCarriesTarget,
		},
		{
			name:  "no retired entity anywhere",
			body:  validBody(`<p>The Table remembers.</p>`),
			check: assertNoRetiredEntityIsNamed,
		},
		{
			// The stylesheet itself: selectors, custom properties and an at-rule, none
			// of which is a document and none of which names a retired entity.
			name:  "a real generated sheet",
			body:  string(themeSheetFixture(t)),
			check: assertNoRetiredEntityIsNamed,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			counter := &silentFailer{T: t}

			testCase.check(counter, fixtureAudit(t, testCase.name, testCase.body,
				testCase.contentType))

			if counter.failures != 0 {
				t.Errorf("the audit reported %d finding(s) about a response it is satisfied "+
					"with: %v. An audit that fires where it has nothing to say is as useless "+
					"as one that never fires, because the failures it reports are not the "+
					"failures a reader has",
					counter.failures, counter.messages)
			}
		})
	}
}

// themeSheetFixture is a real sheet from the generator, for the control that
// needs bytes this route would actually serve.
//
// A fresh harness rather than a shared one: the control is about what the
// generator emits, and a shared harness would mean a manifest written by another
// test's ordering.
func themeSheetFixture(t *testing.T) []byte {
	t.Helper()

	served := newHarness(t)
	served.write(manifest(fixtureAccent, fixtureInk))

	recorder := served.get(themePath, gmRequestor())
	if recorder.Code != 200 {
		t.Fatalf("the sheet answered %d, want 200", recorder.Code)
	}

	return recorder.Body.Bytes()
}

// TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe is the third control,
// and it is the one that catches the failure neither of the others can.
//
// A rule whose fixtures it satisfies and whose **product** it rejects is worse
// than no rule, because it is green on its own fixtures and red on the thing it
// was written for — and the first person to see that is whoever next touches the
// route. So each audit is run over the route's own four responses: the core
// sheet, the generated sheet, the gate's refusal and the 500.
//
// **All four, and that is not belt and braces.** Three of them are `text/css` and
// one is `text/plain` and one is JSON, and the content-type rule is the one that
// can only be checked across that spread — a control run over the two 200s alone
// would never see the refusal that decides what a failure looks like.
func TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe(t *testing.T) {
	t.Parallel()

	checks := []struct {
		name  string
		check func(auditFailer, *docAudit)
	}{
		{name: "ContentTypeIsNotHTML", check: assertNotHTMLContentType},
		{name: "NoHeading", check: assertNoHeading},
		{name: "NoLandmark", check: assertNoLandmark},
		{name: "NoFocusStop", check: assertNoFocusStop},
		{name: "TargetClass", check: assertEveryFocusStopCarriesTarget},
		{name: "Vocabulary", check: assertNoRetiredEntityIsNamed},
	}

	for _, response := range auditedResponses(t) {
		for _, audited := range checks {
			t.Run(response.where+" — "+audited.name, func(t *testing.T) {
				t.Parallel()

				counter := &silentFailer{T: t}

				audited.check(counter, parseResponse(t, response))

				if counter.failures != 0 {
					t.Errorf("the %s audit reported %d finding(s) about a response this route "+
						"really serves: %v. An audit whose product it rejects is worse than no "+
						"audit: it is green on its own fixtures and red on the thing it was "+
						"written for",
						audited.name, counter.failures, counter.messages)
				}
			})
		}
	}
}

// TestTheRouteServesFourKindsofResponse is what makes the third control
// meaningful: the four audited responses must actually differ in their *kind*, or
// the control is running the same answer four times.
//
// **Without this, the control could pass while only one branch was ever
// audited.** A future change that answered `text/css` for every failure —
// plausible, and tempting, because a stylesheet route that always serves a
// stylesheet reads as consistency — would leave every audit above satisfied and
// the failure branch unaudited, since the content-type rule would be checked
// against one value four times.
func TestTheRouteServesFourKindsOfResponse(t *testing.T) {
	t.Parallel()

	responses := auditedResponses(t)
	if len(responses) != 4 {
		t.Fatalf("the route audits %d responses, want 4; a subject list that quietly "+
			"omits a state is a subject list that can drift", len(responses))
	}

	seenStatus := map[int]int{}
	seenType := map[string]int{}

	for _, response := range responses {
		seenStatus[response.status]++
		seenType[strings.SplitN(response.contentType, ";", 2)[0]]++
	}

	for status, count := range seenStatus {
		if status != 200 && count != 1 {
			t.Errorf("status %d is audited %d times, want exactly 1; a state counted "+
				"twice is a state counted once with a duplicate beside it", status, count)
		}
	}

	if seenType["text/html"] > 0 {
		t.Errorf("one of the audited responses claims text/html (%d of them); this route "+
			"serves a subresource and §10.2's rules are about documents it does not render",
			seenType["text/html"])
	}

	if seenType["text/plain"] == 0 {
		t.Error("no audited response claims text/plain, so this route's failure branch is " +
			"not in the subject list — and the failure branch is the one a reader meets " +
			"when the instance is broken")
	}

	if seenType["text/css"] == 0 {
		t.Error("no audited response claims text/css, so the route's own success is not in " +
			"the subject list")
	}
}
