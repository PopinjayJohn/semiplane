package live_test

// The live chrome's own contract: the targets, the regions, the two-egress table,
// and the three claims the mount points have to keep true.
//
// # Why these tests are here and not in `internal/httpapi/events`
//
// The route renders *fragments*; the document renders the *containers* those
// fragments go into. A fragment audit over five small strings proves very little about
// a 200-element sidebar, and a document audit over the sidebar proves nothing about a
// fragment's contents — so both are asserted, in the package that owns each.
//
// The audits the route package needs for §10.2 live in
// `internal/httpapi/events/route_a11y_test.go`, which is the package `A11Y_ROUTE_PKGS`
// would name. This file is the other half: it holds the document-level claims that
// only exist once the chrome and `components/chat`'s panel are assembled together,
// because **the chat log's container is `components/chat`'s and this package does not
// render it** — see `live.templ`'s package comment.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/web/components/chat"
	"github.com/semiplane/semiplane/internal/web/components/live"
)

// sidebarView is the fixture the document tests render: a populated chrome beside a
// populated chat panel, which is the shape the play page's rail takes.
func sidebarView() live.ChromeView {
	moment := time.Date(2026, time.September, 30, 14, 0, 0, 0, time.UTC)

	return live.ChromeView{
		EventsHref: "/c/greyhaven/events",
		SocketHref: "/c/greyhaven/ws",
		Initiative: live.InitiativeView{
			Turn: "p2",
			Entries: []live.InitiativeEntry{
				{PlacementID: "p1", Name: "Ashbear", Initiative: 18},
				{PlacementID: "p2", Name: "Mirela", Initiative: 14},
				{PlacementID: "p3", Name: "Tobin", Initiative: 9},
			},
		},
		Dice: live.DicePanelView{
			History: []live.DiceLineView{
				{
					Actor:      "Mirela",
					Expression: "2d6+3",
					Total:      14,
					Breakdown:  []int{6, 5},
					At:         moment,
				},
			},
			Truncated: true,
		},
		Notices: []live.NoticeView{
			{
				Kind:   live.NoticeDegradedWatch,
				Detail: "Pages may be behind what the watch has seen.",
			},
		},
	}
}

// renderChrome renders the chrome on its own.
func renderChrome(t *testing.T, view live.ChromeView) string {
	t.Helper()

	var out strings.Builder

	if err := live.Chrome(view).Render(t.Context(), &out); err != nil {
		t.Fatalf("render the chrome: %v", err)
	}

	return out.String()
}

// renderSidebar renders the chrome **and** `chat.Panel`, which is the whole sidebar.
//
// The two together, and that is the point rather than a convenience: the chat log's
// target is declared by `components/chat`, so a document holding the chrome alone has
// four of the five targets and a document holding both has all five exactly once.
func renderSidebar(t *testing.T, view live.ChromeView) string {
	t.Helper()

	var out strings.Builder

	if err := live.Chrome(view).Render(t.Context(), &out); err != nil {
		t.Fatalf("render the chrome: %v", err)
	}

	if err := chat.Panel(chat.PanelView{
		Messages: []chat.Message{{
			Seq: 1, Author: "Tobin", Body: "The door is barred.", At: view.Dice.History[0].At,
		}},
		Compose: chat.ComposeView{
			Action: "/c/greyhaven/chat", SignedIn: true, Max: 4096,
		},
	}).Render(t.Context(), &out); err != nil {
		t.Fatalf("render the chat panel: %v", err)
	}

	return out.String()
}

// parseDocument parses rendered markup into a tree.
//
// `golang.org/x/net/html` rather than string matching, for the reason AGENTS.md states
// as an invariant: substring matching is how "no world" passes while the word sits in
// an HTML comment, an `aria-label` or a `data-` attribute.
func parseDocument(t *testing.T, body string) *html.Node {
	t.Helper()

	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse the rendered document: %v", err)
	}

	return root
}

// attr is one element's attribute value, or the empty string.
func attr(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}

	return ""
}

// hasClass reports whether an element carries a class token.
func hasClass(node *html.Node, token string) bool {
	return slices.Contains(strings.Fields(attr(node, "class")), token)
}

// elementChildren counts an element's element children.
func elementChildren(node *html.Node) int {
	count := 0

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode {
			count++
		}
	}

	return count
}

// --- The target contract ----------------------------------------------------------------

// TestEveryTargetAppearsExactlyOnceInTheSidebar is the binding C4's play document
// depends on, and it is the claim that a patch's selector resolves to one element.
//
// **Once, over the chrome and the chat panel together.** Two elements carrying
// `[data-chrome="chat-log"]` is a document where `querySelector` takes the first and
// the reader's log goes nowhere, and §10.2's duplicate-`data-testid` rule would not
// see it because `chat-log` is a `data-chrome` and not a test hook.
//
// The reverse direction matters just as much: **a target nobody renders is a patch
// that patches nothing**, which is the same silent failure with the opposite cause.
func TestEveryTargetAppearsExactlyOnceInTheSidebar(t *testing.T) {
	t.Parallel()

	root := parseDocument(t, renderSidebar(t, sidebarView()))

	for _, target := range live.Targets() {
		found := findByHook(root, target.Hook)

		switch len(found) {
		case 1:
		case 0:
			t.Errorf("the sidebar renders no element carrying %s. %s patches into it, "+
				"and a patch whose selector matches nothing is a feature that never "+
				"arrives", target.Selector, target.Name)
		default:
			names := make([]string, 0, len(found))
			for _, node := range found {
				names = append(names, "<"+node.Data+">")
			}

			t.Errorf("the sidebar renders %d elements carrying %s (%s); %s needs "+
				"exactly one, because a selector matching several patches the first and "+
				"leaves the rest stale",
				len(found), target.Selector, strings.Join(names, ", "), target.Name)
		}
	}
}

// TestTheChatLogHookIsTheOneTheChatPackageDeclares is the single-owner claim, in the
// direction a rename breaks.
//
// The chat log's container belongs to `components/chat`, and this package must not
// declare its own: two owners for one hook is two spellings waiting to diverge, and
// `chat.LogChromeHook`'s own comment says the attribute is "a promise to phase 9's
// script", which is this package. So the constant this package names must be the
// chat package's constant — read from Go, so a rename on either side fails here.
func TestTheChatLogHookIsTheOneTheChatPackageDeclares(t *testing.T) {
	t.Parallel()

	if live.ChatLogHook != chat.LogChromeHook {
		t.Errorf("the live chrome names the chat log's hook %q and `components/chat` "+
			"declares %q. Two spellings of one hook is a selector that matches nothing, "+
			"and the reader's chat simply stops updating",
			live.ChatLogHook, chat.LogChromeHook)
	}

	root := parseDocument(t, renderSidebar(t, sidebarView()))

	if found := findByHook(root, chat.LogChromeHook); len(found) != 1 {
		t.Errorf("the chat panel renders %d elements carrying %q, want exactly 1",
			len(found), chat.LogChromeHook)
	}
}

// TestNoPatchTargetIsFocusableOrHoldsAFocusStop is §7.5's first prohibition, over
// the served document.
//
// The reasoning that makes it checkable rather than aspirational: a patch replaces an
// element's children, so it cannot move focus **if and only if** the element it
// targets holds nothing focusable and is not itself focusable. So the test renders the
// sidebar and asserts both, for all five targets.
//
// `live.Decide` refuses a *focusable* target at runtime from the constant it carries;
// this is what proves the constant. A target that really were focusable would make
// every patch into it refused — correct behaviour, and a sidebar that never updates.
func TestNoPatchTargetIsFocusableOrHoldsAFocusStop(t *testing.T) {
	t.Parallel()

	root := parseDocument(t, renderSidebar(t, sidebarView()))

	for _, target := range live.Targets() {
		found := findByHook(root, target.Hook)
		if len(found) != 1 {
			// `TestEveryTargetAppearsExactlyOnceInTheSidebar` names this precisely; a
			// second finding here would say nothing a reader could act on.
			continue
		}

		element := found[0]

		if isFocusStop(element) {
			t.Errorf("%s is %s and it is focusable (it carries %s). §7.5 forbids a patch "+
				"that can touch the focused element, and replacing or updating a focus "+
				"stop can move it. `live.Decide` refuses every patch into a focusable "+
				"target, so this is a sidebar that never updates",
				target.Selector, element.Data, describeAttributes(element))
		}

		if stops := focusStopsWithin(element); len(stops) != 0 {
			t.Errorf("%s holds %d focusable elements (%s). A patch replaces its children, "+
				"so anything focusable inside it is a reader's caret or selection the "+
				"next patch destroys",
				target.Selector, len(stops), strings.Join(stops, ", "))
		}
	}
}

// TestTheLiveRegionsAreEmptyAtLoadAndTheHistoryIsNot is §7.5's replay rule for this
// surface, in both halves.
//
// "The chat and dice logs must not replay history into a live region on connect — a
// user joining mid-table would otherwise hear the whole table read aloud."
//
// Two halves, and the second is what makes the first meaningful:
//
//   - the live log has **no element children** — everything that arrived with the
//     document is somewhere else;
//   - the list it is in is `aria-live="off"` — so the history is neither announced
//     directly nor by inheriting a politeness from an ancestor.
//
// And the initiative tracker, which is the one target that carries its content
// server-rendered: it is **`aria-live="off"`** as a panel rather than being empty,
// because its contents *are* the state and there is nothing to defer. It is silent,
// and that is a different mechanism for the same rule.
func TestTheLiveRegionsAreEmptyAtLoadAndTheHistoryIsNot(t *testing.T) {
	t.Parallel()

	root := parseDocument(t, renderSidebar(t, sidebarView()))

	for _, target := range live.Targets() {
		if !target.Region.Announces() {
			continue
		}

		found := findByHook(root, target.Hook)
		if len(found) != 1 {
			continue
		}

		if children := elementChildren(found[0]); children != 0 {
			t.Errorf("%s carries %d element(s) at load. It is a live region, so a "+
				"reader who has script hears its whole contents the moment they connect — "+
				"which is §7.5's replay rule. The history belongs in a silent list "+
				"beside it",
				target.Selector, children)
		}
	}

	for _, history := range []struct{ hook, testID string }{
		{hook: live.DiceHook, testID: live.DiceHistoryTestID},
	} {
		found := findByTestID(root, history.testID)
		if len(found) != 1 {
			t.Errorf("the sidebar renders %d elements with %s, want exactly 1; without "+
				"it the rolls that arrived with the document are nowhere",
				len(found), history.testID)
			continue
		}

		if politness := attr(found[0], "aria-live"); politness != live.SilentLive {
			t.Errorf("the %s list is aria-live=%q, want %q. Stating `off` is what makes "+
				"\"these are not announced\" a property of this element rather than of "+
				"whichever live region it happens to sit inside",
				history.testID, politness, live.SilentLive)
		}

		if attr(found[0], "role") != "" {
			t.Errorf("the %s list carries role=%q; it is the history, not the log, and a "+
				"role here would make it a second region",
				history.testID, attr(found[0], "role"))
		}
	}
}

// TestTheInitiativeTrackerIsNotALiveRegion is §7.5's table read as a negative.
//
// The table has a row for "turn change" and none for "the order changed", and the
// difference is the whole design: the turn change arrives in the polite status region
// as a sentence, while the tracker is a *view* of the same fact. A tracker that
// announced itself would read the whole order aloud every time the order changed,
// which is the replay failure wearing different clothes.
func TestTheInitiativeTrackerIsNotALiveRegion(t *testing.T) {
	t.Parallel()

	target, found := live.TargetByName("initiative")
	if !found {
		t.Fatal("no target is named \"initiative\"; the tracker is one of §7.5's five")
	}

	if target.Region.Announces() {
		t.Errorf("the initiative tracker's region is %q, want a silent one. §7.5's table "+
			"gives the *turn change* a polite status region and the order no region at "+
			"all, and a tracker that announced itself would read every combatant aloud "+
			"on every re-order", target.Region)
	}

	root := parseDocument(t, renderSidebar(t, sidebarView()))

	element := findByHook(root, target.Hook)
	if len(element) != 1 {
		t.Fatalf("the sidebar renders %d elements carrying %s, want 1",
			len(element), target.Selector)
	}

	for _, attribute := range []string{"aria-live", "role", "aria-relevant", "aria-atomic"} {
		if value := attr(element[0], attribute); value != "" {
			t.Errorf("the initiative tracker carries %s=%q. It is not a live region and "+
				"must not carry any of the four attributes that make one: the attribute "+
				"is present on an element and a §10.2 audit that counts attributes, not "+
				"intentions, would have to treat it as announcing",
				attribute, value)
		}
	}
}

// TestTheCurrentTurnIsAnnouncedAsStateAndNotAsASentence is the accessible
// equivalent §7.6 argues for, applied to the tracker.
//
// `aria-current="true"` on exactly one row: state a reader can ask about rather than
// a sentence they are interrupted with. **Exactly one**, because a tracker with two
// current rows tells a screen-reader user the table is in two states at once, and
// because "is any row current" is not a question `aria-current` can answer.
func TestTheCurrentTurnIsAnnouncedAsStateAndNotAsASentence(t *testing.T) {
	t.Parallel()

	root := parseDocument(t, renderSidebar(t, sidebarView()))

	current := findByAttribute(root, "aria-current", "true")
	if len(current) != 1 {
		t.Fatalf("%d rows carry aria-current=\"true\", want exactly 1. The tracker "+
			"names whose turn it is with that attribute, and zero means a reader is "+
			"never told and two means they are told two contradictory things",
			len(current))
	}

	if placement := attr(current[0], "data-placement"); placement != sidebarView().Initiative.Turn {
		t.Errorf("the current row is %q, want %q. The attribute and the view model's "+
			"turn must agree, or the tracker says one thing and the data says another",
			placement, sidebarView().Initiative.Turn)
	}
}

// TestEveryFragmentCarriesTheRegionItIsPatchedInto is the §7.5 table as a property of
// the fragments, not of the containers.
//
// Each of the five builders must produce a fragment `live.Decide` accepts, and each
// must be the *only* region for its target. The pair matters: a fragment accepted into
// the wrong region is announced with the wrong urgency, and §7.10's "a live region
// firing more than once per second" is a rule about the region rather than the event.
func TestEveryFragmentCarriesTheRegionItIsPatchedInto(t *testing.T) {
	t.Parallel()

	moment := time.Date(2026, time.September, 30, 14, 0, 0, 0, time.UTC)

	for _, subject := range []struct {
		name    string
		build   func(context.Context) (live.Fragment, error)
		region  live.Region
		targets string
	}{
		{
			name: "the initiative tracker",
			build: func(ctx context.Context) (live.Fragment, error) {
				return live.RenderInitiative(ctx, sidebarView().Initiative)
			},
			region:  live.RegionSilent,
			targets: "initiative",
		},
		{
			name: "a chat line",
			build: func(ctx context.Context) (live.Fragment, error) {
				return live.RenderChatLine(ctx, chat.Message{
					Seq: 7, Author: "Tobin", Body: "It opens.", At: moment,
				})
			},
			region:  live.RegionLog,
			targets: "chat log",
		},
		{
			name: "a dice line",
			build: func(ctx context.Context) (live.Fragment, error) {
				return live.RenderDiceLine(ctx, live.DiceLineView{
					Actor: "Mirela", Expression: "1d20+5", Total: 23,
					Breakdown: []int{18}, At: moment,
				})
			},
			region:  live.RegionLog,
			targets: "dice log",
		},
		{
			name: "a turn change",
			build: func(ctx context.Context) (live.Fragment, error) {
				return live.RenderAnnouncement(ctx, live.AnnouncementView{
					Sentence: "It is Mirela's turn.",
				})
			},
			region:  live.RegionStatus,
			targets: "announcement",
		},
		{
			name: "a degraded watcher",
			build: func(ctx context.Context) (live.Fragment, error) {
				return live.RenderNotice(ctx, live.NoticeView{
					Kind:   live.NoticeDegradedWatch,
					Detail: "Pages may be behind what the watch has seen.",
				})
			},
			region:  live.RegionAlert,
			targets: "notice",
		},
	} {
		t.Run(subject.name, func(t *testing.T) {
			t.Parallel()

			fragment, err := subject.build(t.Context())
			if err != nil {
				t.Fatalf("build the fragment: %v", err)
			}

			if fragment.Target.Name != subject.targets {
				t.Errorf("the fragment targets %q, want %q", fragment.Target.Name,
					subject.targets)
			}

			if fragment.Region != subject.region {
				t.Errorf("the fragment declares region %q, want %q. §7.5's table decides "+
					"this, and `Decide` refuses a mismatch — a fragment accepted into the "+
					"wrong region is announced with the wrong urgency",
					fragment.Region, subject.region)
			}

			if decision := live.Decide(fragment); !decision.Write {
				t.Fatalf("the fragment is refused (%s, focus stops %v). A fragment the "+
					"product's own builder produces must be writable; a refusal here means "+
					"the region and the focus rules and the markup disagree",
					decision.Reason, decision.FocusStops)
			}
		})
	}
}

// TestEveryNoticeKindHasItsOwnHeadingAndHeading is the copy contract.
//
// Two notices of the same kind would share a heading id, and §10.2's rule that an id
// is unique is what makes a duplicate a failure rather than an ambiguity a screen
// reader resolves by picking the first. So the fixture renders every declared kind
// and every heading id must differ.
func TestEveryNoticeKindHasItsOwnHeading(t *testing.T) {
	t.Parallel()

	view := sidebarView()
	view.Notices = nil

	for _, kind := range live.NoticeKinds() {
		view.Notices = append(view.Notices, live.NoticeView{Kind: kind})
	}

	body := renderChrome(t, view)
	root := parseDocument(t, body)

	seen := map[string]string{}

	for _, notice := range findByClass(root, "live-notice-item") {
		heading := notice.FirstChild
		if heading == nil || heading.Type != html.ElementNode {
			continue
		}

		id := attr(heading, "id")

		if id == "" {
			t.Errorf("a notice's heading carries no id, so its section's aria-labelledby " +
				"resolves to nothing (UI §7.2)")
		}

		if text := headingText(heading); text == "" {
			t.Errorf("the heading at %s is empty; §7.5's assertive row announces this "+
				"condition, and a heading that names nothing names the alert nothing", id)
		}

		if previous, duplicate := seen[id]; duplicate {
			t.Errorf("two notices share the heading id %q (%s and %s); every reference to "+
				"it is ambiguous and a screen reader resolves it by picking the first",
				id, previous, textOf(heading))
		}

		seen[id] = textOf(heading)
	}
}

// TestANoticeWithNothingToSayRendersNothing is §4.5's rule, extended to a notice.
//
// An empty `role="alert"` section is a landmark in the outline that names a problem
// which does not exist, and §7.5's assertive row is for conditions a reader must hear
// *now* — a reader who has learned that this region means now stops reading it.
func TestANoticeWithNothingToSayRendersNothing(t *testing.T) {
	t.Parallel()

	view := sidebarView()
	view.Notices = []live.NoticeView{{Kind: live.NoticeNone}}

	root := parseDocument(t, renderChrome(t, view))

	if found := findByClass(root, "live-notice-item"); len(found) != 0 {
		t.Errorf("a notice of kind `NoticeNone` rendered %d section(s), want none. "+
			"`NoticeNone` is the zero value, so a view model that forgot to say which "+
			"condition it is must render nothing rather than an empty alert",
			len(found))
	}
}

// --- The §7.5 table as data ------------------------------------------------------------------

// TestTheAnnouncedContentTableAgreesWithTheTargets is §7.5's table, asserted against
// the code rather than restated in a comment.
//
// Three claims, and each catches a different drift:
//
//  1. **every SSE row names a target this package declares**, at the region the table
//     says — so the specification and the code cannot disagree;
//  2. **every WebSocket row names none** — so "one hub, two egress representations" is
//     a property of the data and not of a reader's memory;
//  3. **every SSE row's region is one `Targets()` declares at all** — so a row added
//     with a region nothing renders is a finding rather than a dead entry.
func TestTheAnnouncedContentTableAgreesWithTheTargets(t *testing.T) {
	t.Parallel()

	declared := map[string]live.Target{}
	for _, target := range live.Targets() {
		declared[target.Name] = target
	}

	for _, row := range live.AnnouncedContent() {
		switch row.Transport {
		case live.TransportEventStream:
			if row.Elsewhere != "" {
				if row.Target != "" {
					t.Errorf("§7.5 gives %s the target %q and also says the target belongs "+
						"to %s. One of the two is wrong, and the second is the one a reader "+
						"of the table cannot check",
						rowName(row), row.Target, row.Elsewhere)
				}

				continue
			}

			target, known := declared[row.Target]
			if !known {
				t.Errorf("§7.5 says %s arrives over SSE, but %q is not one of the targets "+
					"this package declares (%v). Either the table is stale or the target "+
					"is missing, and a patch aimed at an undeclared target is refused",
					rowName(row), row.Target, live.Targets())
				continue
			}

			if target.Region != row.Region {
				t.Errorf("§7.5 gives %s the region %q and %s is declared %q. One of them "+
					"is a reader's idea of when to interrupt them",
					rowName(row), row.Region, target.Selector, target.Region)
			}

		case live.TransportWebSocket:
			if row.Target != "" {
				t.Errorf("§7.5 says %s rides the WebSocket, but the row names the "+
					"patch target %q. One hub, two egress representations: a thing that "+
					"arrives as structured state has no DOM to be patched into",
					rowName(row), row.Target)
			}

		case live.TransportHTTP:
			// The 412 is a response to a request the reader made. It is not a target
			// here, and it must not become one.
			if row.Target != "" {
				t.Errorf("§7.5 gives the HTTP response row the target %q; an HTTP "+
					"response is a document, not a patch", row.Target)
			}

		case live.TransportNone:
			t.Errorf("the row %s names no transport", rowName(row))

		case live.TransportNone - 1, live.TransportHTTP + 1:
			t.Errorf("the row %s carries a transport outside the closed set", rowName(row))
		}
	}
}

// TestThePresenceCursorsAndTheMapAreNotAnnounced is §7.5's two "no live region" rows,
// held as negatives.
//
// Both are on the WebSocket, and both are deliberately absent from `Targets()`. The
// reason is in `announced.go` and it is worth one sentence here: UI §7.6 makes the
// **token list** the accessible equivalent of the map, so a canvas that announced
// itself would be a second, worse representation — and a presence cursor is a visual
// only, `aria-hidden` by name in the record.
func TestThePresenceCursorsAndTheMapAreNotAnnounced(t *testing.T) {
	t.Parallel()

	for _, silent := range []string{"Presence cursors", "Map-side state"} {
		found := false

		for _, row := range live.AnnouncedContent() {
			if row.Content != silent {
				continue
			}

			found = true

			if row.Region != live.RegionSilent {
				t.Errorf("§7.5 marks %q as **no live region** and the row declares %q. The "+
					"map's accessible equivalent is the token list (§7.6) and a presence "+
					"cursor is `aria-hidden` by name in the record; a region here is a "+
					"second, worse representation of something already represented",
					silent, row.Region)
			}
		}

		if !found {
			t.Errorf("the table has no row for %q. §7.5's table lists it, and the rows "+
				"this package does not serve are in the table precisely so this test can "+
				"look at them — a table holding only its own rows would make the absence "+
				"true by construction and the assertion nothing", silent)
		}
	}
}

// TestTheConnectionStateIsNotAPatchTarget is the one §7.5 row this package
// deliberately cannot serve, and the reason is the finding.
//
// "Ruleset drift, game ended, connection lost" rides the WebSocket. A patch target for
// the connection state would mean the notice arrived over the channel whose loss is
// the condition — the one design that guarantees the reader who needed it most is the
// one who does not get it. So `live-notice` is a target and the connection state is
// not: both sentences ship in the document and the client reveals one.
func TestTheConnectionStateIsNotAPatchTarget(t *testing.T) {
	t.Parallel()

	for _, target := range live.Targets() {
		if target.Hook == live.SocketHook {
			t.Errorf("%q is declared a patch target. The connection's own loss is the "+
				"condition a patch for it would have to travel over, and §9's answer is "+
				"that the header indicator is the only signal during a reconnect",
				target.Selector)
		}
	}

	body := renderChrome(t, sidebarView())
	root := parseDocument(t, body)

	container := findByTestID(root, live.SocketPanelTestID)
	if len(container) != 1 {
		t.Fatalf("the sidebar renders %d connection containers, want 1", len(container))
	}

	if role := attr(container[0], "role"); role != live.AlertRole {
		t.Errorf("the connection container's role is %q, want %q. §7.5's row for "+
			"\"connection lost\" is `role=\"alert\"`, assertive, and the reason is that "+
			"the reader's view of the table has stopped being current",
			role, live.AlertRole)
	}

	if politeness := attr(container[0], "aria-live"); politeness != live.Assertive {
		t.Errorf("the connection container is aria-live=%q, want %q",
			politeness, live.Assertive)
	}

	// Both sentences ship, and exactly one is showing. The client moves the attribute
	// rather than authoring a string.
	hidden := 0

	for _, state := range []string{live.SocketConnectedTestID, live.SocketLostTestID} {
		found := findByTestID(root, state)
		if len(found) != 1 {
			t.Errorf("the connection container renders %d blocks with %s, want 1",
				len(found), state)
			continue
		}

		if hasAttr(found[0], "hidden") {
			hidden++
		}
	}

	if hidden != 1 {
		t.Errorf("%d of the two connection blocks are hidden at load, want exactly 1. "+
			"Both sentences are server-rendered and the client reveals one; two showing "+
			"is a document asserting two things about the connection, and none showing "+
			"is a notice nobody is told about",
			hidden)
	}
}

// TestTheChromeDeclaresExactlyOneOfEachConnection is §7.5's second task, on the
// document half.
//
// The client half is `internal/web/static/js/live`'s
// `TestTheClientOpensExactlyOneWebSocketAndNoSecondTransport`, which counts the
// `new WebSocket` call sites in the shipped bytes. This is the other half: the two
// elements that name the connections, counted in the served document. Both are needed
// — a document carrying two `data-init` openers is two streams, and a client opening
// two sockets is two sockets, and neither is visible from the other.
func TestTheChromeDeclaresExactlyOneOfEachConnection(t *testing.T) {
	t.Parallel()

	root := parseDocument(t, renderSidebar(t, sidebarView()))

	if found := findByHook(root, live.WebSocketHook); len(found) != 1 {
		t.Errorf("the sidebar carries %d elements with %s=%q, want exactly 1: the play "+
			"page opens exactly one WebSocket (UI §7.5)",
			len(found), live.ChromeAttribute, live.WebSocketHook)
	}

	if found := findByTestID(root, live.EventsTestID); len(found) != 1 {
		t.Errorf("the sidebar carries %d elements with %s, want exactly 1: the play page "+
			"opens exactly one SSE (UI §7.5)", len(found), live.EventsTestID)
	}

	// And the opener really is an opener: an element carrying a test hook and no
	// `data-init` is a counted element that opens nothing, which is the silent half of
	// this assertion.
	openers := findByTestID(root, live.EventsTestID)
	if len(openers) == 1 {
		action := attr(openers[0], "data-init")
		if action == "" {
			t.Errorf("the element with %s carries no data-init, so nothing opens the "+
				"event stream. A counted element that opens nothing is the failure this "+
				"assertion exists to prevent", live.EventsTestID)
			return
		}

		if !strings.Contains(action, sidebarView().EventsHref) {
			t.Errorf("the opener's action is %q, which does not name %q. The two are "+
				"filled by the same view model field, so a mismatch means one of the two "+
				"spellings of the stream's URL has drifted",
				action, sidebarView().EventsHref)
		}
	}
}

// --- Helpers ------------------------------------------------------------------------------

// findByHook returns every element carrying the given `data-chrome` value.
func findByHook(root *html.Node, hook string) []*html.Node {
	return findByAttribute(root, live.ChromeAttribute, hook)
}

// findByTestID returns every element carrying the given `data-testid`.
func findByTestID(root *html.Node, id string) []*html.Node {
	return findByAttribute(root, "data-testid", id)
}

// findByAttribute returns every element whose attribute has the given value.
func findByAttribute(root *html.Node, name, value string) []*html.Node {
	found := []*html.Node{}

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && attr(node, name) == value {
			found = append(found, node)
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	return found
}

// findByClass returns every element carrying the given class token.
func findByClass(root *html.Node, class string) []*html.Node {
	found := []*html.Node{}

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && hasClass(node, class) {
			found = append(found, node)
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	return found
}

// isFocusStop reports whether a keyboard can reach the element.
func isFocusStop(node *html.Node) bool {
	switch node.Data {
	case "a":
		return attr(node, "href") != ""
	case "button", "select", "textarea", "summary", "iframe", "audio", "video":
		return true
	case "input":
		return !strings.EqualFold(attr(node, "type"), "hidden")
	default:
		is := hasAttr(node, "tabindex")

		return is
	}
}

// focusStopsWithin returns the tag names of every focus stop in a subtree.
func focusStopsWithin(node *html.Node) []string {
	found := []string{}

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if current != node && isFocusStop(current) {
			found = append(found, current.Data)
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walk(child)
	}

	return found
}

// lookupAttr reports whether an attribute is present, whatever its value.
//
// **Presence and not the value**, and that is the distinction `document_test.go`'s own
// prose leans on twice: a `tabindex="-1"` is programmatically focusable even though it
// is not in the tab order, and §10.2's `aria-hidden` rule treats it as a focus stop.
func hasAttr(node *html.Node, name string) bool {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return true
		}
	}

	return false
}

// describeAttributes names an element's identifying attributes for a failure message.
func describeAttributes(node *html.Node) string {
	var described strings.Builder

	described.WriteString("<" + node.Data)

	for _, name := range []string{"id", "role", "aria-live", "data-chrome", "data-testid"} {
		if value := attr(node, name); value != "" {
			described.WriteString(" " + name + "=" + value)
		}
	}

	described.WriteString(">")

	return described.String()
}

// headingText is a heading's own text.
func headingText(node *html.Node) string {
	return strings.TrimSpace(textOf(node))
}

// textOf is an element's text content.
func textOf(node *html.Node) string {
	var out strings.Builder

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			out.WriteString(current.Data)
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return out.String()
}

// rowName renders a table row for a failure message.
func rowName(row live.Announced) string {
	return "\"" + row.Content + "\""
}
