package chat_test

// UI §10.2's audits for the chat surface, and §7.5's live-region contrast with the
// search route.
//
// Four conventions govern every assertion here, and each of them was learned from a
// gate that passed for the wrong reason:
//
//   - **Parse the DOM; never substring-match the markup.** It is the only way "no
//     `world`" passes while the word sits in an HTML comment, an `aria-label` or an
//     attribute *name*. The vocabulary audit walks every text node, comment,
//     attribute value and attribute name, and its negative controls feed the word in
//     six ways and require the audit to object to each.
//   - **Every audit must be able to fail.** `controls_test.go` is the negative
//     control: each rule is run over a document built to violate exactly that rule,
//     and each must produce a finding that *mentions the thing it is about*. A rule
//     that fires for an unrelated reason looks exactly like one that works.
//   - **An audit must also be capable of reporting nothing.** A red suite in the
//     first week is how an audit gets switched off rather than fixed.
//   - **The contrast is the point.** §7.5 requires a live region on the chat surface
//     and forbids one on the search surface, so one predicate runs over two documents
//     rendered through **the same shell**. A rule only one route could violate is not
//     a rule, and this is what makes the "search has none" half something this work
//     item actually tests rather than something it asserts about somebody else's
//     route.
//
// Every test whose name carries "Structural", "Target", "Vocabulary" or "EveryRoute"
// is matched by the gate's `A11Y_TESTS`, which is how a new audit becomes part of
// `make a11y` rather than a test somebody runs by hand.
//
// # The audits take an interface, not a `*testing.T`
//
// So `controls_test.go` can run **the same audit bodies** over a fixture and assert
// that they objected. A copy of an audit for the controls is the failure this
// package's structure exists to prevent: a copy is a second answer to the same
// question, and it drifts.

import (
	"context"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/httpapi/search"
	"github.com/semiplane/semiplane/internal/web/components"
	"github.com/semiplane/semiplane/internal/web/components/chat"
	"github.com/semiplane/semiplane/internal/web/components/chrome"
)

// retiredEntities is UI §1.2's two words, read from one place.
//
// A package-level list rather than a literal inside the audit, because the rule is
// UI §1.2's and a second copy of the list is a second answer to it.
var retiredEntities = []string{"world", "session"}

// fixedClock is the moment every fixture's messages were sent, so a rendering that
// formats a time differently is caught by a value rather than by a shape.
var fixedClock = time.Date(2026, time.October, 2, 14, 3, 11, 0, time.UTC)

// --- Fixtures --------------------------------------------------------------------

// shellView is the chrome both documents are rendered with.
//
// **One fixture for both, deliberately.** The live-region contrast is only a
// contrast if everything except the route's own content is held constant: the shell
// is where a `role="status"` connection indicator would come from, so rendering two
// different shells would test the shell and not the surfaces.
func shellView() components.ShellView {
	return components.ShellView{
		Title:       "Greyhaven",
		Instance:    components.InstanceView{Name: "Greyhaven", Version: "0.3.0"},
		Account:     components.AccountView{Username: "mira"},
		SignOutHref: "/logout",
		Campaign:    chrome.CampaignRef{Slug: "greyhaven", Name: "Greyhaven"},
	}
}

// populatedPanel is a chat panel with two messages, a GM's export control and a
// refusal on the compose form — every branch this package renders at once, which is
// what makes it the fixture the audits run against.
func populatedPanel() chat.PanelView {
	return chat.PanelView{
		Messages: []chat.Message{
			{Seq: 41, Author: "mira", Body: "the tavern is on fire", At: fixedClock},
			{Seq: 42, Author: "dorn", Body: "I have the rope", At: fixedClock.Add(time.Minute)},
		},
		Truncated: true,
		Compose: chat.ComposeView{
			Action:      "/c/greyhaven/chat",
			Placeholder: "Say something",
			Max:         4096,
			Error:       "That message was too long.",
			SignedIn:    true,
		},
		Export: chat.ExportView{
			Allowed:  true,
			Withheld: 1,
			LastPath: "Journal/chat-20261002-140505.md",
		},
	}
}

// populatedPage is the whole document with the populated panel.
func populatedPage() chat.PageView {
	return chat.PageView{
		Shell:   shellView(),
		Heading: "Chat",
		Panel:   populatedPanel(),
	}
}

// emptyPage is the document with nothing said yet and a signed-out reader: the two
// branches whose *absences* the populated fixture cannot exercise.
//
// Both together rather than separately, because the two absences are the same absence
// seen from two sides — a panel with no messages and a panel nobody may post to are
// each a shape the populated one never reaches.
func emptyPage() chat.PageView {
	return chat.PageView{
		Shell:   shellView(),
		Heading: "Chat",
		Panel: chat.PanelView{
			Compose: chat.ComposeView{Action: "/c/greyhaven/chat"},
			Export:  chat.ExportView{Allowed: false},
		},
	}
}

// searchPage is the search route's own document, rendered through the same shell.
//
// A **real** document from a real component rather than a fixture: the point is that
// `search.ResultsPage` carries no live region, and a hand-written stand-in for it
// would be an assertion about a document this work item wrote.
func searchPage() search.ResultsView {
	return search.ResultsView{
		Shell:  shellView(),
		Action: "/c/greyhaven/search",
		Query:  "goblin",
		Hits: []search.ResultView{{
			Href:    "/c/greyhaven/wiki/Adventures/Goblin_Cave",
			Title:   "Goblin Cave",
			Path:    "Adventures/Goblin Cave",
			Snippet: "Three goblins by the road.",
		}},
	}
}

// render parses one component's output into a document node.
//
// Parsing rather than asserting on the string, for the reason in the file header: the
// audits are about what a browser's accessibility tree sees, and a substring search
// over markup cannot see a word in a comment or in an attribute name.
func render(t *testing.T, component interface {
	Render(ctx context.Context, out io.Writer) error
},
) *html.Node {
	t.Helper()

	var out strings.Builder

	if err := component.Render(context.Background(), &out); err != nil {
		t.Fatalf("render: %v", err)
	}

	document, err := html.Parse(strings.NewReader(out.String()))
	if err != nil {
		t.Fatalf("parse the rendered document: %v", err)
	}

	return document
}

// bothPages is the two documents every cross-cutting audit runs over: the chat surface
// and the search surface, rendered through the same shell.
//
// Two documents for the rules that are about the **shell**, and one of them is the
// search route's. That is not thoroughness for its own sake: a chat component that
// broke the shell's landmarks would break them on search too, and the search document
// has no chat markup in it at all — so it is the control that says the rules are the
// shell's and not this component's.
func bothPages(t *testing.T) map[string]*html.Node {
	t.Helper()

	return map[string]*html.Node{
		"chat":   renderPage(t, "populated"),
		"search": render(t, search.ResultsPage(searchPage())),
	}
}

// renderPage renders the chat document for one of the named states.
func renderPage(t *testing.T, state string) *html.Node {
	t.Helper()

	switch state {
	case "populated":
		return render(t, chat.Page(populatedPage()))
	case "empty":
		return render(t, chat.Page(emptyPage()))
	case "gm-with-no-messages":
		view := emptyPage()
		view.Panel.Export = chat.ExportView{
			Allowed:  true,
			LastPath: "Journal/chat-20261002-140505.md",
		}

		return render(t, chat.Page(view))
	default:
		t.Fatalf("unknown fixture state %q", state)
	}

	return nil
}

// --- The structural contract -------------------------------------------------------

// TestTheChatSurfaceSatisfiesTheStructuralContract is UI §10.2, as one test with a
// subtest per rule so a failure names the rule rather than "the a11y test failed".
//
// The rules are §10.2's own list plus the two it implies and does not spell out —
// heading levels and reference integrity — because a document can have a correct
// landmark and a broken outline, and an `aria-labelledby` that resolves to nothing is
// invisible in a rendering.
func TestTheChatSurfaceSatisfiesTheStructuralContract(t *testing.T) {
	t.Parallel()

	t.Run("landmarks are present and distinguishing", func(t *testing.T) {
		t.Parallel()

		for name, document := range bothPages(t) {
			assertLandmarksArePresentAndDistinguishing(t, name, document)
		}
	})

	t.Run("heading levels never skip", func(t *testing.T) {
		t.Parallel()

		for name, document := range bothPages(t) {
			assertHeadingLevelsNeverSkip(t, name, document)
		}
	})

	t.Run("every reference resolves", func(t *testing.T) {
		t.Parallel()

		for name, document := range bothPages(t) {
			assertEveryReferenceResolves(t, name, document)
		}
	})

	t.Run("no inline outline suppression", func(t *testing.T) {
		t.Parallel()

		for name, document := range bothPages(t) {
			assertNoInlineOutlineSuppression(t, name, document)
		}
	})

	t.Run("no aria-hidden on a focus stop", func(t *testing.T) {
		t.Parallel()

		for name, document := range bothPages(t) {
			assertNoAriaHiddenOnAFocusStop(t, name, document)
		}
	})

	t.Run("skip links come first and resolve", func(t *testing.T) {
		t.Parallel()

		for name, document := range bothPages(t) {
			assertSkipLinksComeFirstAndResolve(t, name, document)
		}
	})
}

// TestEveryFocusStopCarriesTheTargetClass is UI §10.6, and it is the reason the
// markup carries the class rather than the script.
//
// §10.6 makes the target size "enforced by construction", and §7.3's construction is
// a shared utility plus a walk over every focus stop in the **served** document. A
// class applied from the script that arrives with the behaviour would be invisible to
// this walk, which is the same failure `internal/content/target.go` documents for
// rendered page bodies.
func TestEveryFocusStopCarriesTheTargetClass(t *testing.T) {
	t.Parallel()

	for name, document := range bothPages(t) {
		assertEveryFocusStopCarriesTheTargetClass(t, document)
		_ = name
	}
}

// TestTheChatSurfaceHasNoPositiveTabindex is split out from the structural contract
// so `make a11y` names it, and because a chat surface is exactly where a
// roving-tabindex implementation would want to be wrong.
//
// §7.4 permits `tabindex="-1"` and nothing else — including `0`, which is an author
// asserting an order rather than deferring to the document's. The rule is a **numeric**
// comparison for the reason its own control documents: `+1` and ` 1 ` sort before `"0"`
// as strings and would pass a comparison while a browser reads them as one.
func TestTheChatSurfaceHasNoPositiveTabindex(t *testing.T) {
	t.Parallel()

	for name, document := range bothPages(t) {
		assertNoPositiveTabindex(t, name, document)
	}
}

// TestTheVocabularyAuditFindsTheWordWhereverItIs is UI §1.2 and §10.2's last clause.
//
// The scan covers **every text node, every comment, every attribute value and every
// attribute name**, and each inclusion has a reason:
//
//   - **Comments**: a word in a comment is invisible to a reader, which is exactly why
//     an audit reading text nodes would pass on a document whose markup still names a
//     retired entity — and a comment is the one place a renderer is tempted to leave
//     one, because nobody sees it.
//   - **Attribute values**: an `aria-label` or a `title` is announced to a reader, so a
//     retired entity named there is named to the person the rule protects.
//   - **Attribute names**: a `data-world` is invisible to a reader and obvious to a
//     developer grepping for the feature it implies, which makes it exactly as retired
//     as a label.
//
// # The one carve-out, and why it is this narrow
//
// **The history list's message bodies are excluded, and nothing else on the surface
// is.** A chat message is campaign content — a GM whose campaign is about a fantasy
// world will say "world" in chat, and a rule that failed that document would be
// failing a reader for their own words, which is the mistake
// `internal/httpapi/search`'s own copy of this rule documents ("a GM whose page is
// called 'The World Map' will see their own title on their own screen and no audit
// changes that").
//
// The carve-out is a **subtree test on the element carrying `chat-history`**, and
// `TestTheVocabularyCarveOutIsExactlyTheHistoryList` is what holds it to that width: it
// asserts the word is fine inside the list and a finding one element outside it.
func TestTheVocabularyAuditFindsTheWordWhereverItIs(t *testing.T) {
	t.Parallel()

	for name, document := range bothPages(t) {
		assertNoRetiredVocabulary(t, document)
		_ = name
	}
}

// TestTheChatSurfaceHasExactlyOneOfEachHook is §10.2's hook rule, over every state
// this package renders.
//
// Three documents rather than one, and the reason is the rule's own shape: a hook that
// appears more than once in a document selects nothing, and a hook absent from a state
// the view model promises is a state rendered half-way. Both are invisible in the one
// document where neither happens.
func TestTheChatSurfaceHasExactlyOneOfEachHook(t *testing.T) {
	t.Parallel()

	for state, want := range hookExpectations(t) {
		document := renderPage(t, state)
		counts := make(map[string]int)

		walk(t, document, func(node *html.Node) {
			if node.Type != html.ElementNode {
				return
			}

			value, present := attribute(node, "data-testid")
			if !present {
				return
			}

			if value == "" {
				t.Errorf("%s: an element carries an empty data-testid; a hook that selects "+
					"nothing is worse than no hook", state)
			}

			counts[value]++
		})

		for hook, expected := range want {
			if counts[hook] != expected {
				t.Errorf("%s: the %q hook appears %d time(s), want %d",
					state, hook, counts[hook], expected)
			}
		}
	}
}

// hookExpectations is each state's promised set of hooks, and how many of each it
// renders.
//
// **Counts, not presence.** §10.2's rule is that a hook appears exactly once, and a
// hook on every message row appears fifty times — which is why `MessageRow` carries
// `data-seq` and not a `data-testid`.
func hookExpectations(_ *testing.T) map[string]map[string]int {
	return map[string]map[string]int{
		"populated": {
			chat.PanelTestID:         1,
			chat.MessageCountTestID:  1,
			chat.HistoryTestID:       1,
			chat.ComposeTestID:       1,
			chat.ComposeFieldTestID:  1,
			chat.ComposeSubmitTestID: 1,
			chat.ComposeErrorTestID:  1,
			chat.ExportTestID:        1,
			"chat-history-truncated": 1,
			"chat-export-withheld":   1,
			"chat-export-path":       1,
			chat.LogTestID:           1,
			"chat-page-title":        1,
		},
		"empty": {
			chat.PanelTestID:          1,
			chat.MessageCountTestID:   1,
			chat.EmptyTestID:          1,
			"chat-compose-signed-out": 1,
			chat.LogTestID:            1,
			"chat-page-title":         1,
		},
		"gm-with-no-messages": {
			chat.PanelTestID:          1,
			chat.MessageCountTestID:   1,
			chat.EmptyTestID:          1,
			"chat-compose-signed-out": 1,
			chat.ExportTestID:         1,
			"chat-export-path":        1,
			chat.LogTestID:            1,
			"chat-page-title":         1,
		},
	}
}

// --- The live region ----------------------------------------------------------------

// TestTheChatSurfaceCarriesALiveRegionAndTheSearchSurfaceDoesNot is §7.5's contrast,
// and it is one predicate over two documents.
//
// The rule it holds: **chat qualifies for a live region and search must not have one**,
// because a message arrives at a reader who did nothing to ask for it while a search
// result list is reached by navigation and would fire on every load of its own URL.
//
// **Both halves are asserted from this file**, and that is the point: a rule only one
// route could violate is not a rule. `controls_test.go` feeds the same predicate a
// document with `aria-live` and one with `role="status"` and requires a finding from
// each, so "no live region on search" is not passing because the predicate finds
// nothing anywhere.
func TestTheChatSurfaceCarriesALiveRegionAndTheSearchSurfaceDoesNot(t *testing.T) {
	t.Parallel()

	chatRegions := announcingRegions(renderPage(t, "populated"))
	if len(chatRegions) != 1 {
		t.Errorf("the chat document has %d live regions, want exactly 1: %v",
			len(chatRegions), chatRegions)
	}

	if len(chatRegions) == 1 {
		region := chatRegions[0]

		if region.role != "log" {
			t.Errorf("the chat document's live region has role=%q, want %q", region.role, "log")
		}

		if region.politeness != "polite" {
			t.Errorf("the chat document's live region is aria-live=%q, want polite",
				region.politeness)
		}

		if region.testID != chat.LogTestID {
			t.Errorf("the chat document's live region is the %q element, want %q; a live "+
				"region somewhere else would be announcing something that is not chat",
				region.testID, chat.LogTestID)
		}
	}

	// The other half, and the reason this test lives in the *chat* package: §7.5 gives
	// the search route no live region at all, and asserting that from a chat test is
	// the only way this work item can be sure of it without owning that route.
	assertLiveRegionCount(t, render(t, search.ResultsPage(searchPage())), 0)
}

// TestTheLiveLogIsEmptyAtLoadAndTheHistoryIsNot is §7.5's replay rule, which is the
// sharpest thing in the section and the one a rendering cannot show.
//
// "The chat and dice logs must not replay history into a live region on connect — a
// user joining mid-table would otherwise hear the whole table read aloud."
//
// Both halves are asserted, and the second is what makes the first meaningful:
//
//   - the live region has **no element children** — everything that arrived with the
//     document is somewhere else;
//   - the list the messages *are* in carries `aria-live="off"` — so they are neither
//     announced directly nor by inheriting a politeness from an ancestor.
func TestTheLiveLogIsEmptyAtLoadAndTheHistoryIsNot(t *testing.T) {
	t.Parallel()

	document := renderPage(t, "populated")

	// **Exactly one** live region, before its emptiness is checked. Checking one
	// element's children and nothing else would pass a document carrying a *second*
	// populated region somewhere the test did not look — which is the mutation a row
	// of this test's own evidence was caught by, and the assertion that catches it is
	// the count rather than the child walk.
	assertLiveRegionCount(t, document, 1)

	region := mustFind(t, document, `ol[data-testid="`+chat.LogTestID+`"]`)

	if role := attributeOr(region, "role"); role != "log" {
		t.Errorf("the live region has role=%q, want %q; §7.5's own table names "+
			"role=status, and role=log is the container that form cannot be",
			role, "log")
	}

	for child := region.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode {
			t.Errorf("the live region has an element child <%s> at %s at load; §7.5 "+
				"forbids replaying history into a live region on connect",
				child.Data, where(child))
		}
	}

	if politeness, _ := attribute(region, "aria-live"); politeness != "polite" {
		t.Errorf("the live region has aria-live=%q, want polite written out explicitly; "+
			"an implicit politeness is invisible to the gate and to the next reader",
			politeness)
	}

	if atomic, _ := attribute(region, "aria-atomic"); atomic != "false" {
		t.Errorf("the live region has aria-atomic=%q, want false; left at its default the "+
			"whole log is read out again from the top on every append", atomic)
	}

	history := mustFind(t, document, `ol[data-testid="`+chat.HistoryTestID+`"]`)
	if politeness, _ := attribute(history, "aria-live"); politeness != "off" {
		t.Errorf("the history list has aria-live=%q, want off; the messages that arrived "+
			"with the document must not be announced", politeness)
	}

	if got := countElements(history, "li"); got != 2 {
		t.Errorf("the history list holds %d rows, want 2; the fixture is not exercising "+
			"what it says it is", got)
	}
}

// --- The absences that are not structural --------------------------------------------

// TestTheExportControlIsAbsentForAReaderWhoMayNotExport is S-8's line drawn in the
// interface, and it is asserted as an **absence of the control**, not as a disabled one.
//
// A player who can see that an action exists and cannot use it has been told the
// campaign has an export; a disabled button is a focus stop that does nothing, which is
// the same objection §4.7 answers for an empty section.
func TestTheExportControlIsAbsentForAReaderWhoMayNotExport(t *testing.T) {
	t.Parallel()

	document := renderPage(t, "empty")

	if find(document, `button[data-testid="`+chat.ExportTestID+`"]`) != nil {
		t.Error("the export control is rendered for a reader who may not export; content " +
			"writing is GM-only (S-8) and the control should be absent")
	}

	// And the same package *does* render it for a GM, so the absence above is the gate
	// and not a component that never renders it.
	asGM := renderPage(t, "gm-with-no-messages")

	if find(asGM, `button[data-testid="`+chat.ExportTestID+`"]`) == nil {
		t.Error("the export control is not rendered for a GM who may export; the absence " +
			"assertion above would be satisfied by a component that never renders it")
	}

	// The compose form's own absence, same shape: a signed-out reader gets a paragraph
	// rather than a textarea they cannot use.
	if find(document, `textarea[data-testid="`+chat.ComposeFieldTestID+`"]`) != nil {
		t.Error("the compose textarea is rendered for a signed-out reader; a field nobody " +
			"may fill is a focus stop that does nothing")
	}
}

// TestAMessageBodyIsEscaped is the one assertion about campaign content, and it is here
// rather than in a sanitiser test because a chat message is the one piece of this
// surface a reader cannot be trusted to have written.
//
// No `templ.Raw` anywhere in this package is the structural version of the same claim;
// this is the behavioural one, over a body containing markup, a quote and a character
// that would end an attribute.
func TestAMessageBodyIsEscaped(t *testing.T) {
	t.Parallel()

	view := populatedPage()
	view.Panel.Messages = []chat.Message{{
		Seq:    1,
		Author: `dorn" onmouseover="alert(1)`,
		Body:   `<script>alert("pwned")</script> & "quoted" 'single'`,
		At:     fixedClock,
	}}

	document := render(t, chat.Page(view))

	// Scoped to the message list, and the scope is not tidiness: the shell renders
	// `shell.Resolver`'s two `<script>` elements into `<head>` (§3.7's one blocking
	// resolver), so a document-wide check would fire on every rendering and pass for
	// the wrong reason — which is the same shape as a gate that cannot fail.
	messages := mustFind(t, document, `ol[data-testid="`+chat.HistoryTestID+`"]`)

	for _, node := range collect(messages) {
		if node.Type == html.ElementNode && node.Data == "script" {
			t.Error("a message body produced a <script> element; a chat message is " +
				"attacker-reachable and there is no unescaped interpolation in this package")
		}
	}

	// The text is present — escaped — rather than absent, so this is not passing because
	// the message was dropped.
	var found bool

	// `walkAll` rather than `walk`: the evidence is a **text node**, which is not an
	// element, and a walk that visited elements only would report nothing here and the
	// assertion would pass for the wrong reason.
	walkAll(t, document, func(node *html.Node) {
		if node.Type == html.TextNode && strings.Contains(node.Data, "<script>") {
			found = true
		}
	})

	if !found {
		t.Error("the message body does not appear as text; the escaping removed it rather " +
			"than rendering it, so the reader sees nothing at all")
	}

	// And the author's injected attribute never became one: the name is text, and a
	// `<strong>` holding a quote is a `<strong>` holding a quote.
	if find(document, `strong[data-testid="chat-message-author"]`) != nil {
		t.Error("the author name carries a test hook; the message row's location hook is " +
			"`data-seq`, which `Chat.Seq` makes unique within a campaign")
	}
}

// --- The audits, as functions ---------------------------------------------------------
//
// Each takes the document's name so its findings say *which* document they came from: a
// reader of the output needs to know whether the chat surface or the search surface is
// the one that broke, and a failure that says only "the document" sends them to both.

// assertLandmarksArePresentAndDistinguishing is §7.2's landmark rule, reduced to what
// these two documents can break.
//
// The full rule — every landmark labelled, two of one role not sharing a name — is
// `internal/httpapi/wiki`'s and `internal/httpapi/search`'s, and duplicating it here
// would be a third copy to keep in step. What this surface adds is the possibility of
// breaking the shell's, so what is asserted is that a `<main>` is here and the document
// has an `<h1>`, and the negative control proves the rule can say so.
func assertLandmarksArePresentAndDistinguishing(
	failer auditFailer,
	name string,
	document *html.Node,
) {
	failer.Helper()

	mains := countElements(document, "main")
	if mains != 1 {
		failer.Errorf("%s has %d <main> elements, want 1; the shell renders the centre "+
			"landmark and nothing here may add a second", name, mains)
	}

	// **Exactly one**, not at least one. §7.2 allows one `<h1>` per document, and a
	// second one is a document outline with two roots — which is why the count is here
	// rather than a `> 0` check that would pass on two.
	headings := countElements(document, "h1")
	if headings != 1 {
		failer.Errorf("%s has %d <h1> elements, want exactly 1; §7.2 allows one per "+
			"document and a reader who lands on either of two is told the wrong one is "+
			"the page's name", name, headings)
	}
}

// assertHeadingLevelsNeverSkip is UI §7.2.
//
// Read in **document order**, not per element, because the failure is about the
// reader's outline and an element's own level says nothing about the level above it.
func assertHeadingLevelsNeverSkip(failer auditFailer, name string, document *html.Node) {
	failer.Helper()

	previous := 0

	walk(failer, document, func(node *html.Node) {
		level, heading := headingLevel(node)
		if !heading {
			return
		}

		if previous != 0 && level > previous+1 {
			failer.Errorf("%s: a heading jumps from h%d to h%d at %s; §7.2 requires the "+
				"outline not to skip a level", name, previous, level, where(node))
		}

		previous = level
	})
}

// assertEveryReferenceResolves is reference integrity, which §10.2's list implies and
// does not spell out.
//
// The four reference attributes plus `label[for]`. An unresolvable reference is an
// element with no accessible name or a hint that says nothing, and neither shows in a
// rendering.
func assertEveryReferenceResolves(failer auditFailer, name string, document *html.Node) {
	failer.Helper()

	ids := make(map[string]int)

	walk(failer, document, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		if value, present := attribute(node, "id"); present {
			ids[value]++
		}
	})

	for id, count := range ids {
		if count > 1 {
			failer.Errorf("%s: the id %q appears %d times; a reference to it is ambiguous",
				name, id, count)
		}
	}

	referenceAttributes := map[string]string{
		"aria-labelledby":       "the landmark or region it names",
		"aria-describedby":      "the hint it points at",
		"aria-controls":         "the element it controls",
		"aria-owns":             "the element it owns",
		"aria-activedescendant": "the active descendant it points at",
	}

	walk(failer, document, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		if node.Data == "label" {
			for target := range strings.FieldsSeq(attributeOr(node, "for")) {
				if _, present := ids[target]; !present {
					failer.Errorf("%s: a <label for=%q> at %s names no element, so the "+
						"field has no accessible name", name, target, where(node))
				}
			}
		}

		for attributeName, describedAs := range referenceAttributes {
			for target := range strings.FieldsSeq(attributeOr(node, attributeName)) {
				if _, present := ids[target]; !present {
					failer.Errorf("%s: %s=%q at %s resolves to no element; it is %s",
						name, attributeName, target, where(node), describedAs)
				}
			}
		}
	})
}

// assertNoPositiveTabindex is §7.4, and it is a **numeric** comparison.
//
// `strconv.Atoi` on the trimmed value rather than a string comparison against `"0"`,
// because `+1` and ` 1 ` sort before `"0"` as strings and would pass a comparison while
// a browser reads them as one — which is the failure where this application and the
// browser disagree about the tab order. `0` is also a finding: §7.4 asks for natural
// document order and no reordering, and `tabindex="0"` is an author asserting an order
// that happens to match.
func assertNoPositiveTabindex(failer auditFailer, name string, document *html.Node) {
	failer.Helper()

	walk(failer, document, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		raw, present := attribute(node, "tabindex")
		if !present {
			return
		}

		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			failer.Errorf("%s: <%s> at %s has tabindex=%q, which is not an integer",
				name, node.Data, where(node), raw)

			return
		}

		if value > 0 {
			failer.Errorf("%s: <%s> at %s has tabindex=%d; §7.4 permits only -1, and a "+
				"positive value puts this application and the browser in disagreement "+
				"about the tab order", name, node.Data, where(node), value)
		}

		if value == 0 {
			failer.Errorf("%s: <%s> at %s has tabindex=0; §7.4 asks for natural document "+
				"order and no reordering, so an author is not permitted to assert one",
				name, node.Data, where(node))
		}
	})
}

// assertNoInlineOutlineSuppression is §7.2's focus-ring rule.
//
// Both `outline: none` and `outline: 0`, in any spacing, because a rule that matched
// one spelling would pass on the other — and the second spelling is the one a rule
// written as a `strings.Contains(outline, "none")` forgets.
func assertNoInlineOutlineSuppression(failer auditFailer, name string, document *html.Node) {
	failer.Helper()

	walk(failer, document, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		style, present := attribute(node, "style")
		if !present {
			return
		}

		// Whitespace removed rather than normalised to one space, so `outline : 0` and
		// `outline:0` are the same string — which is the whole point of the rule.
		normalised := strings.NewReplacer(" ", "", "\t", "", "\n", "").
			Replace(strings.ToLower(style))

		if strings.Contains(normalised, "outline:none") ||
			strings.Contains(normalised, "outline:0") {
			failer.Errorf("%s: <%s> at %s has style=%q (normalised %q), which suppresses "+
				"the focus ring; §7.2 prohibits outline suppression with no visible "+
				"replacement", name, node.Data, where(node), style, normalised)
		}
	})
}

// assertNoAriaHiddenOnAFocusStop is §7.10, and it walks **ancestors** rather than the
// element.
//
// The case that catches real bugs is an `aria-hidden` on a wrapper: a rule that only
// read the attribute on the element would pass it, and a keyboard user would find a
// focusable thing in a subtree they were told does not exist.
func assertNoAriaHiddenOnAFocusStop(failer auditFailer, name string, document *html.Node) {
	failer.Helper()

	walk(failer, document, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		if hidden, _ := attribute(node, "aria-hidden"); hidden != "true" {
			return
		}

		if containsFocusStop(node) {
			failer.Errorf("%s: the aria-hidden subtree at %s contains focusable elements",
				name, where(node))
		}
	})
}

// assertSkipLinksComeFirstAndResolve is §7.2, and the chat surface's own obligation is
// that it does not break the shell's: a panel that put a focusable element before the
// skip links would make "the first focusable elements in the document" false.
func assertSkipLinksComeFirstAndResolve(failer auditFailer, name string, document *html.Node) {
	failer.Helper()

	var skips []string

	walk(failer, document, func(node *html.Node) {
		if node.Type != html.ElementNode || !hasClass(node, "skip-link") {
			return
		}

		href, present := attribute(node, "href")
		if !present {
			failer.Errorf("%s: a skip link at %s has no href", name, where(node))

			return
		}

		if !strings.HasPrefix(href, "#") {
			failer.Errorf("%s: a skip link at %s has href=%q; §7.2's links are fragments",
				name, where(node), href)
		}

		skips = append(skips, href)
	})

	if len(skips) == 0 {
		failer.Errorf("%s has no skip link; §7.2 puts the first focusable elements in the "+
			"document before the banner", name)

		return
	}

	// Nothing focusable may precede the first skip link, and the comparison is over
	// the **focus-stop order** rather than over the visit order: "before" here is a
	// statement about what a keyboard reaches first, which is not the same as "appears
	// earlier in the byte stream" once a skip link is a link with an href.
	first := -1

	for position, stop := range focusStops(document) {
		if hasClass(stop, "skip-link") {
			first = position

			break
		}
	}

	if first < 0 {
		return
	}

	for position, stop := range focusStops(document) {
		if position >= first {
			break
		}

		failer.Errorf("%s: <%s> at %s precedes the first skip link in the tab order; "+
			"§7.2 makes the skip links the first focusable elements in the document",
			name, stop.Data, where(stop))
	}
}

// assertEveryFocusStopCarriesTheTargetClass is UI §10.6, over the whole subtree.
//
// An `<a>` with no `href` is not a focus stop and is not a finding: a rule that
// demanded the class of every anchor would be enforcing a rule §10.6 does not have.
func assertEveryFocusStopCarriesTheTargetClass(failer auditFailer, document *html.Node) {
	failer.Helper()

	walk(failer, document, func(node *html.Node) {
		if !isFocusStop(node) {
			return
		}

		if node.Data == "a" && !hasAttribute(node, "href") {
			return
		}

		if !hasClass(node, "target") {
			failer.Errorf("<%s> at %s does not carry the .target class; §10.6's target "+
				"size is enforced by the class being in the served document, not by the "+
				"script that arrives with the behaviour", node.Data, where(node))
		}
	})
}

// assertNoRetiredVocabulary is UI §1.2 and §10.2's last clause, scoped to **this
// package's own copy**.
//
// The scope is a subtree test on the element carrying `chat-history`, and
// `TestTheVocabularyCarveOutIsExactlyTheHistoryList` is what holds it to that width.
func assertNoRetiredVocabulary(failer auditFailer, document *html.Node) {
	failer.Helper()

	walkAll(failer, document, func(node *html.Node) {
		if isCampaignContent(node) {
			return
		}

		switch node.Type {
		case html.TextNode:
			reportRetired(failer, node, "a text node", node.Data)
		case html.CommentNode:
			reportRetired(failer, node, "an HTML comment", node.Data)
		case html.ElementNode:
			for _, attr := range node.Attr {
				reportRetired(failer, node, "the attribute "+attr.Key, attr.Val)
				reportRetired(failer, node, "an attribute name", attr.Key)
			}
		case html.DoctypeNode:
		default:
		}
	})
}

// isCampaignContent reports whether a node is **inside** the chat history list, whose
// contents are campaign content and outside the vocabulary audit.
//
// **Ancestors, and strictly above the node.** Two decisions in one predicate and each
// of them was a control failing before it was written this way:
//
//   - *Ancestors rather than the element itself.* Campaign content is what is **in**
//     the list. The list's own attributes are this package's markup — a `title` on the
//     `<ol>` is a sentence this package wrote — so a carve-out that skipped the
//     element as well as its subtree would exempt more than it means to.
//   - *Strictly above.* The list element's own `class`, `aria-live` and `data-testid`
//     are all this package's, and each of them is a place the word could be smuggled
//     past a scan.
func isCampaignContent(node *html.Node) bool {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type != html.ElementNode {
			continue
		}

		if attributeOr(ancestor, "data-testid") == chat.HistoryTestID {
			return true
		}
	}

	return false
}

// reportRetired is the vocabulary finding itself.
func reportRetired(failer auditFailer, node *html.Node, place, text string) {
	lowered := strings.ToLower(text)

	for _, retired := range retiredEntities {
		if strings.Contains(lowered, retired) {
			failer.Errorf("%s at %s contains %q; neither entity exists, and §1.2 makes "+
				"the words appear nowhere in this package's own copy",
				place, where(node), retired)
		}
	}
}

// assertLiveRegionCount is §7.5's live-region rule as a count, which is the form both
// halves of the contrast need.
//
// A count rather than a boolean so a finding can say *how many*, which is what makes
// "the chat document has 3" different from "the chat document has at least one".
//
// **Counts only the regions that announce.** `announcingRegions` drops the elements
// carrying `aria-live="off"`, and that is the deliberate half of the rule rather than a
// concession to the chat surface: an element that has chosen `off` is the *conclusion*
// of the deliberation §7.5 asks for, and a gate that treated it as a violation would
// forbid a surface from saying "these are not announcements" — which is the whole of
// §7.5's replay rule and is how a reader joining mid-table avoids hearing the table.
func assertLiveRegionCount(failer auditFailer, document *html.Node, want int) {
	failer.Helper()

	regions := announcingRegions(document)
	if len(regions) == want {
		return
	}

	if want == 0 {
		failer.Errorf("the document has %d live regions, want none: %v; §7.5 says "+
			"search is not live, and a region that fires on every load of that URL is "+
			"the reader's own keystrokes read back to them", len(regions), regions)

		return
	}

	failer.Errorf("the document has %d live regions, want %d: %v", len(regions), want, regions)
}
