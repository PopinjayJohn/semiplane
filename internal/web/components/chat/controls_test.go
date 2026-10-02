package chat_test

// The negative controls: proof that each audit in `chat_test.go` can fail, and proof
// that the one carve-out in it is exactly as narrow as it claims.
//
// # Why this file exists
//
// Phase 5 found three gate tests that **could not fail**. They passed on documents
// that violated the rule they claimed to check, which is worse than having no gate: a
// green light wired to nothing reads as a covered requirement in a spec and in a
// review. The mechanism is always the same — the assertion watches a *neighbour* of
// the thing it names.
//
// So every rule below gets a fixture built to violate **exactly that rule**, and the
// test requires a finding whose message mentions the rule. Only one rule runs per
// row, and each fixture is minimal, because a fixture that trips four rules and
// asserts one of them has an ambiguous failure the moment a rule starts reporting
// twice.
//
// # The three rows that are the most important
//
// They are the ones where a naive implementation would pass:
//
//   - **`tabindex=" +1 "`** — signed and padded. `strings.Compare(value, "0") < 0`
//     reads that as negative and passes; a browser reads it as one. The row exists so
//     the numeric parse is load-bearing rather than a preference.
//   - **the word inside a message body** — the one place the vocabulary audit is
//     *supposed* to say nothing, and the row that holds the carve-out to the history
//     list and no wider.
//   - **the word in an attribute name** — the case a scan over attribute *values*
//     misses and a scan over raw markup would find.

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/web/components/chat"
)

// silentFailer records findings and lets the test decide whether there were any.
//
// The count and the messages are both kept, because a count alone cannot tell which
// rule spoke and an audit that fires for an unrelated reason looks exactly like one
// that works.
//
// It **is** an `auditFailer` rather than one wrapped in a `talker`: `Helper` is a
// no-op here, which is correct — there is no `*testing.T` for a failure to be reported
// against, because reporting one is the test's decision.
type silentFailer struct {
	// failures is how many findings the audit reported.
	failures int
	// messages is what it said.
	messages []string
}

// Helper is a no-op.
func (f *silentFailer) Helper() {}

// Errorf records one finding.
func (f *silentFailer) Errorf(format string, args ...any) {
	f.failures++
	f.messages = append(f.messages, fmt.Sprintf(format, args...))
}

// mentions reports whether any finding's text contains a substring.
func (f *silentFailer) mentions(needle string) bool {
	for _, message := range f.messages {
		if strings.Contains(message, needle) {
			return true
		}
	}

	return false
}

// fixture parses a minimal document built to violate exactly one rule.
//
// A **hand-written literal** rather than a mutation of a real rendering, and the
// reason is the opposite of the one that made the search route's fixtures mutations:
// a literal for a hook rule is missing every other hook by construction, so it fires
// for a dozen reasons and the one under test is indistinguishable from the rest. For
// the attribute rules below there is no hook list, so the literal is both minimal and
// unambiguous.
func fixture(t *testing.T, body string) *html.Node {
	t.Helper()

	document, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse the fixture: %v", err)
	}

	return document
}

// TestEveryAuditBelowRejectsTheViolationItClaimsTo runs each rule over a fixture that
// violates it and requires a finding that **names the rule**.
func TestEveryAuditBelowRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	for _, row := range []struct {
		name    string
		body    string
		check   func(auditFailer, *html.Node)
		mention string
	}{
		{
			// `wrap` already contributes one `<h1>`, so this fragment contributes the
			// second — which is why the landmark rule counts rather than checking for
			// presence.
			name:    "a second h1",
			body:    wrap(`<h1>Also chat</h1>`),
			check:   assertLandmarksRule,
			mention: "want exactly 1",
		},
		{
			name:    "a second main landmark",
			body:    wrap(`<main id="second">Also the centre</main>`),
			check:   assertLandmarksRule,
			mention: "<main>",
		},
		{
			// The panel's own obligation: a `<h3>` under the shell's `<h2>` is correct,
			// and a `<h4>` is a skip a reader's outline shows.
			name:    "a heading that skips a level",
			body:    wrap(`<h1>Chat</h1><h4>What has been said</h4>`),
			check:   headingRule,
			mention: "jumps from h",
		},
		{
			name:    "a reference that resolves to nothing",
			body:    wrap(`<section aria-labelledby="nowhere"><h1>Chat</h1></section>`),
			check:   referenceRule,
			mention: "resolves to no element",
		},
		{
			name:    "a label for nothing",
			body:    wrap(`<h1>Chat</h1><label for="nowhere">Message</label>`),
			check:   referenceRule,
			mention: "no accessible name",
		},
		{
			name: "an id used twice",
			body: wrap(`<h1>Chat</h1><label for="dup">Message</label><input id="dup"/>` +
				`<textarea id="dup"></textarea>`),
			check:   referenceRule,
			mention: "appears 2 times",
		},
		{
			name:    "a positive tabindex",
			body:    wrap(`<h1>Chat</h1><button tabindex="1">Send</button>`),
			check:   tabindexRule,
			mention: "§7.4 permits only -1",
		},
		{
			// The signed and padded spelling. `strings.Compare(raw, "0") < 0` passes
			// this and a browser does not, which is the whole disagreement this rule
			// exists to catch.
			name:    "a tabindex with a sign and padding",
			body:    wrap(`<h1>Chat</h1><button tabindex=" +1 ">Send</button>`),
			check:   tabindexRule,
			mention: "tabindex=1",
		},
		{
			// And the other direction: a string comparison against "0" gets this wrong.
			name:    "a zero tabindex",
			body:    wrap(`<h1>Chat</h1><button tabindex="0">Send</button>`),
			check:   tabindexRule,
			mention: "natural document order",
		},
		{
			name:    "a tabindex that is not an integer",
			body:    wrap(`<h1>Chat</h1><button tabindex="first">Send</button>`),
			check:   tabindexRule,
			mention: "which is not an integer",
		},
		{
			name:    "an inline outline suppression",
			body:    wrap(`<h1 style="outline: none">Chat</h1>`),
			check:   outlineRule,
			mention: "outline: none",
		},
		{
			// The normalised form in the message, because the *raw* attribute is
			// `outline : 0` and a control that mentioned the raw spelling would pass
			// against a rule matching a different one.
			name:    "an inline outline of zero, in another spelling",
			body:    wrap(`<h1 style="outline : 0">Chat</h1>`),
			check:   outlineRule,
			mention: `normalised "outline:0"`,
		},
		{
			// The attribute is on a **wrapper**, which is the case a rule reading only
			// the element's own attribute would pass.
			name:    "a focusable element inside an aria-hidden subtree",
			body:    wrap(`<div aria-hidden="true"><a href="/c/greyhaven/chat">Send</a></div>`),
			check:   ariaHiddenRule,
			mention: "contains focusable elements",
		},
		{
			// A whole document, because `wrap` supplies a skip link and the violation is
			// its absence — which a fragment cannot express.
			name: "no skip link at all",
			body: `<!doctype html><html lang="en"><head><title>Chat</title></head><body>` +
				`<main id="main"><h1>Chat</h1></main></body></html>`,
			check:   skipLinkRule,
			mention: "has no skip link",
		},
		{
			// `wrap` supplies one valid skip link, so this fragment supplies a second
			// with a non-fragment href.
			name:    "a skip link that is not a fragment",
			body:    wrap(`<a class="skip-link" href="/c/greyhaven/chat">Skip</a>`),
			check:   skipLinkRule,
			mention: "§7.2's links are fragments",
		},
		{
			// The order half, which the fragment alone cannot express: the focusable
			// thing has to come first in the *document*, and `wrap` puts its skip link
			// first — so the fixture is a whole document rather than a fragment.
			name: "a focus stop before the first skip link",
			body: `<!doctype html><html lang="en"><head><title>Chat</title></head><body>` +
				`<a class="target" href="/c/greyhaven">Greyhaven</a>` +
				`<a class="skip-link" href="#main">Skip to content</a>` +
				`<main id="main"><h1>Chat</h1></main></body></html>`,
			check:   skipLinkRule,
			mention: "precedes the first skip link",
		},
		{
			name:    "a focus stop with no target class",
			body:    wrap(`<h1>Chat</h1><button type="submit">Send</button>`),
			check:   targetRule,
			mention: "does not carry the .target class",
		},
		{
			// The tabindex branch, which an `a[href]`-shaped audit never reaches and
			// which is the branch every landmark in the shell sits on.
			name:    "a landmark with a tabindex and no target class",
			body:    wrap(`<h1>Chat</h1><section tabindex="-1">Log</section>`),
			check:   targetRule,
			mention: "does not carry the .target class",
		},
		{
			// The class-token branch: `notarget` contains `target` as a substring, so a
			// rule that used `strings.Contains` would pass this.
			name:    "a class that merely contains the token",
			body:    wrap(`<h1>Chat</h1><button class="notarget">Send</button>`),
			check:   targetRule,
			mention: "does not carry the .target class",
		},
		{
			name:    "the word in a text node",
			body:    wrap(`<h1>Chat</h1><p>the World Map</p>`),
			check:   vocabularyRule,
			mention: "a text node",
		},
		{
			// A reader never sees a comment, which is exactly why it must be scanned.
			name:    "the word in an HTML comment",
			body:    wrap(`<h1>Chat</h1><!-- the world of this campaign -->`),
			check:   vocabularyRule,
			mention: "an HTML comment",
		},
		{
			name:    "the word in an aria-label",
			body:    wrap(`<h1>Chat</h1><section aria-label="game sessions">Log</section>`),
			check:   vocabularyRule,
			mention: "the attribute aria-label",
		},
		{
			name:    "the word in an attribute name",
			body:    wrap(`<h1>Chat</h1><div data-world="true"></div>`),
			check:   vocabularyRule,
			mention: "an attribute name",
		},
		{
			// The live-region detector's half that a `role` check alone would miss: a
			// region written with only `aria-live`, which is the commonest way one
			// arrives by accident.
			name:    "a region written only with aria-live",
			body:    wrap(`<h1>Chat</h1><section aria-live="polite">Log</section>`),
			check:   liveRegionRule,
			mention: "aria-live",
		},
		{
			// And the other half: a `role="status"` with no `aria-live` at all.
			name:    "a region written only with a role",
			body:    wrap(`<h1>Chat</h1><section role="status">Saved</section>`),
			check:   liveRegionRule,
			mention: "role",
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			counter := &silentFailer{}
			row.check(counter, fixture(t, row.body))

			if counter.failures == 0 {
				t.Fatalf("the rule reported nothing for %s; a rule that cannot fail is a "+
					"gate wired to nothing (UI §10.2)", row.name)
			}

			// And it fired for the *right* reason. A count alone cannot tell which rule
			// spoke, and an audit that fires for an unrelated reason looks exactly like
			// one that works.
			if !counter.mentions(row.mention) {
				t.Errorf("the rule reported %d findings for %s but none of them mentions "+
					"%q, so the rule that fired was not the rule under test; messages:\n  %s",
					counter.failures, row.name, row.mention, strings.Join(counter.messages, "\n  "))
			}
		})
	}
}

// TestTheVocabularyCarveOutIsExactlyTheHistoryList holds the one narrowing in this
// package's audits to the width it claims.
//
// Two halves, and the second is the one that usually goes missing:
//
//   - **The word inside a message body is not a finding.** A GM whose campaign has a
//     fantasy world in it says so in chat, and failing that document would be failing
//     a reader for their own words.
//   - **The word one element outside the history list *is* a finding.** A `<h2>` that
//     happens to sit next to a message is this package's copy, and a carve-out scoped
//     by "is there a message nearby" rather than by "is this inside the history list"
//     would silently skip it.
//
// The second half is why the carve-out is written as a **subtree test on an element**
// rather than as a substring exclusion on the rendered text, and this is the test that
// says so.
func TestTheVocabularyCarveOutIsExactlyTheHistoryList(t *testing.T) {
	t.Parallel()

	t.Run("a message body may say either word", func(t *testing.T) {
		t.Parallel()

		counter := &silentFailer{}

		document := fixture(t, wrap(
			`<ol data-testid="`+chat.HistoryTestID+`">`+
				`<li data-seq="1"><p>we are mapping the world tonight</p></li>`+
				`<li data-seq="2"><p>sessions start at seven</p></li>`+
				`</ol>`,
		))

		assertNoRetiredVocabulary(counter, document)

		if counter.failures != 0 {
			t.Errorf("the vocabulary audit reported %d findings for campaign content the "+
				"reader wrote themselves: %s; the carve-out is wider than the history list",
				counter.failures, strings.Join(counter.messages, "; "))
		}
	})

	t.Run("this package's own copy may not", func(t *testing.T) {
		t.Parallel()

		// Every placement is one step outside the history list: a sibling, a parent's
		// own attribute, a node after the list. A carve-out that stopped walking early
		// rather than stopping at the list's boundary would pass all of them.
		for _, body := range []string{
			`<h2>The world of Greyhaven</h2><ol data-testid="` + chat.HistoryTestID + `">` +
				`<li data-seq="1"><p>hello</p></li></ol>`,
			`<ol data-testid="` + chat.HistoryTestID + `" title="our sessions"></ol>`,
			`<ol data-testid="` + chat.HistoryTestID + `"><li data-seq="1"><p>hello</p></li></ol>` +
				`<p data-note="session 7 ended">after the list</p>`,
		} {
			counter := &silentFailer{}

			assertNoRetiredVocabulary(counter, fixture(t, wrap(body)))

			if counter.failures == 0 {
				t.Errorf("the vocabulary audit reported nothing for %q; the carve-out stops "+
					"somewhere other than the history list's boundary", body)
			}
		}
	})
}

// TestEveryAuditReportsNothingOnItsOwnFixture is the third control: an audit that
// fails on everything is a gate wired to the wrong end, and the way that is found out
// is a red suite in the first week, after which the audit gets switched off rather
// than fixed.
func TestEveryAuditReportsNothingOnItsOwnFixture(t *testing.T) {
	t.Parallel()

	for _, row := range []struct {
		name  string
		check func(auditFailer, *html.Node)
	}{
		{name: "landmarks", check: assertLandmarksRule},
		{name: "heading levels", check: headingRule},
		{name: "references", check: referenceRule},
		{name: "tabindex", check: tabindexRule},
		{name: "inline outline", check: outlineRule},
		{name: "aria-hidden", check: ariaHiddenRule},
		{name: "skip links", check: skipLinkRule},
		{name: "target class", check: targetRule},
		{name: "vocabulary", check: vocabularyRule},
		// One, not zero: this control runs over the **chat** document, whose one live
		// region is the point of the file. The absence half of the contrast is asserted
		// against the search document in `TestTheChatSurfaceCarriesALiveRegion…`.
		{name: "live regions", check: func(failer auditFailer, document *html.Node) {
			assertLiveRegionCount(failer, document, 1)
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			counter := &silentFailer{}
			row.check(counter, renderPage(t, "populated"))

			if counter.failures != 0 {
				t.Errorf("%s reported %d findings on the document this package actually "+
					"serves, so it is too eager and will be switched off rather than "+
					"fixed: %s", row.name, counter.failures, strings.Join(counter.messages, "; "))
			}
		})
	}
}

// wrap puts a fragment in the smallest document the structural rules can be run
// against: one `<main>` landmark, one `<h1>`, and nothing else.
//
// **Minimal rather than complete**, and the reason is that a complete fixture would
// be a second copy of this package's own markup, which can drift from it. A row that
// fires for a reason other than the one under test would then be ambiguous — the
// mistake `internal/httpapi/search`'s own mutation log records twice. The fragment
// carries the violation and nothing else, so the only rule it can trip is its own.
func wrap(fragment string) string {
	return `<!doctype html><html lang="en"><head><title>Chat</title></head><body>` +
		`<a class="skip-link" href="#main">Skip to content</a>` +
		`<main id="main"><h1>Chat</h1>` + fragment + `</main></body></html>`
}

// --- The audit wrappers the controls run ----------------------------------------
//
// Each is one call into `chat_test.go`'s audit with the name the finding needs. They
// are here rather than in that file because they exist only for these controls: the
// audits themselves take a `*testing.T` because that is what a gate wants, and a
// `*testing.T` cannot be used to assert that a finding was reported.

func assertLandmarksRule(failer auditFailer, document *html.Node) {
	assertLandmarksArePresentAndDistinguishing(failer, "control", document)
}

func headingRule(failer auditFailer, document *html.Node) {
	assertHeadingLevelsNeverSkip(failer, "control", document)
}

func referenceRule(failer auditFailer, document *html.Node) {
	assertEveryReferenceResolves(failer, "control", document)
}

func tabindexRule(failer auditFailer, document *html.Node) {
	assertNoPositiveTabindex(failer, "control", document)
}

func outlineRule(failer auditFailer, document *html.Node) {
	assertNoInlineOutlineSuppression(failer, "control", document)
}

func ariaHiddenRule(failer auditFailer, document *html.Node) {
	assertNoAriaHiddenOnAFocusStop(failer, "control", document)
}

func skipLinkRule(failer auditFailer, document *html.Node) {
	assertSkipLinksComeFirstAndResolve(failer, "control", document)
}

func targetRule(failer auditFailer, document *html.Node) {
	assertEveryFocusStopCarriesTheTargetClass(failer, document)
}

func vocabularyRule(failer auditFailer, document *html.Node) {
	assertNoRetiredVocabulary(failer, document)
}

// liveRegionRule asserts the *absence* of a live region, which is the form both of
// the detector's halves need: a fixture built to show the detector working has a
// region in it, and the rule under test is the one that says "this document must have
// none".
func liveRegionRule(failer auditFailer, document *html.Node) {
	assertLiveRegionCount(failer, document, 0)
}

// --- The mutation log -------------------------------------------------------------

// TestEveryAuditHereHasAMutationThatBreaksIt is the mechanical version of the claim
// this file makes about itself: each audit names the **exact mutation** that was
// applied to `chat.templ` and observed to break it, and this test asserts the source
// text that mutation changed is still there.
//
// A claim table is not self-verifying — nothing here re-applies a mutation — so what
// this holds is that a mutation was recorded and that the code it names survives. The
// evidence for each claim is the run that produced it, and this test is what stops a
// reverted mutation from leaving a decoration behind.
//
// **Every mutation was applied to the rendered markup, not to a comment.** The first
// attempt at this log replaced the first textual occurrence of `role="log"`, which was
// a sentence in `chat.templ`'s own header comment explaining why `role="log"` — and
// the test passed. A mutation log that records "replaced `role="log"`" without saying
// *where* is therefore not evidence of anything, and every row below names the
// attribute and the element it is on.
func TestEveryAuditHereHasAMutationThatBreaksIt(t *testing.T) {
	t.Parallel()

	for _, claim := range []struct {
		test   string
		change string
		// stillThere is the source the mutation changed. If it stops being there, the
		// mutation was reverted without revisiting the test — or the claim is stale,
		// and one of the two has to go.
		stillThere string
	}{
		{
			test:       "TestTheChatSurfaceCarriesALiveRegionAndTheSearchSurfaceDoesNot",
			change:     `role="log" on the live region -> role="status"`,
			stillThere: `role="log"`,
		},
		{
			test:       "TestTheChatSurfaceCarriesALiveRegionAndTheSearchSurfaceDoesNot",
			change:     `aria-live="polite" on the live region -> aria-live="off"`,
			stillThere: "\t\t\taria-live=\"polite\"\n",
		},
		{
			test:       "TestTheChatSurfaceCarriesALiveRegionAndTheSearchSurfaceDoesNot",
			change:     "aria-live removed from the live region",
			stillThere: "aria-live=\"polite\"\n\t\t\taria-atomic=\"false\"",
		},
		{
			test:       "TestTheLiveLogIsEmptyAtLoadAndTheHistoryIsNot",
			change:     `aria-live="off" on the history list -> polite`,
			stillThere: `aria-live="off" data-testid={ HistoryTestID }`,
		},
		{
			test:       "TestTheLiveLogIsEmptyAtLoadAndTheHistoryIsNot",
			change:     `aria-live="off" removed from the history list`,
			stillThere: `aria-live="off" data-testid={ HistoryTestID }`,
		},
		{
			test:       "TestTheLiveLogIsEmptyAtLoadAndTheHistoryIsNot",
			change:     `aria-atomic="false" -> "true"`,
			stillThere: `aria-atomic="false"`,
		},
		{
			test: "TestTheLiveLogIsEmptyAtLoadAndTheHistoryIsNot",
			change: "a second, populated live region rendered next to the log — which is " +
				"what the assertion checked one element's children and missed",
			stillThere: "@ChatHistory(view)\n\t\t@Compose",
		},
		{
			test:       "TestEveryFocusStopCarriesTheTargetClass",
			change:     "class=\"target\" removed from the compose textarea",
			stillThere: "class=\"target\"\n\t\t\t\t\tname=\"body\"",
		},
		{
			test:       "TestEveryFocusStopCarriesTheTargetClass",
			change:     `class="target button" removed from the send button`,
			stillThere: `class="target button" data-testid={ ComposeSubmitTestID }`,
		},
		{
			test:       "TestEveryFocusStopCarriesTheTargetClass",
			change:     "class=\"target\" removed from the export button",
			stillThere: `class="target button chat-export"`,
		},
		{
			test:       "TestTheChatSurfaceHasExactlyOneOfEachHook",
			change:     "the signed-out branch renders the form anyway",
			stillThere: "if !view.SignedIn {",
		},
		{
			test:       "TestTheExportControlIsAbsentForAReaderWhoMayNotExport",
			change:     "the export control rendered for a reader who may not export",
			stillThere: "if view.Allowed {",
		},
	} {
		if claim.test == "" {
			t.Fatal("a mutation claim has no test name")
		}

		if claim.stillThere == "" {
			t.Fatalf("the claim for %s names no source that must still be present",
				claim.test)
		}

		if !strings.Contains(chatSource, claim.stillThere) {
			t.Errorf("the mutation recorded for %s (%s) names source text that is no "+
				"longer there:\n  %q\nEither the mutation was reverted without revisiting "+
				"the test, or the claim is stale and one of the two has to go",
				claim.test, claim.change, claim.stillThere)
		}
	}
}

// chatSource is `chat.templ` as it stands, for the mutation log's staleness check.
//
// Read at test time rather than embedded, so the check compares the claim against the
// code that is actually there; a claim table holding a copy of the file would pass
// forever.
var chatSource = func() string {
	raw, err := os.ReadFile("chat.templ")
	if err != nil {
		return ""
	}

	return string(raw)
}()
