package events_test

// The live chrome over the stream, and the two §7.5 tasks.
//
// # Why these are socket tests and the notice tests are not
//
// The four prohibitions are all claims about **bytes and time**, and a recorder
// cannot produce either. `events_test.go` says at length why this route cannot be
// driven through `httptest.NewRecorder` — the handler blocks for the life of the
// stream — and everything here inherits that. The wire format is read off a real
// socket's frames; the throttle is measured as the gap between two frames' arrival
// times; the log-line claim reads what the handler actually wrote.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/httpapi/events"
	"github.com/semiplane/semiplane/internal/web/components/chat"
	"github.com/semiplane/semiplane/internal/web/components/live"
)

// The chrome a test installs: a renderer that turns every sidebar kind into the
// fragment §7.5's table says it belongs in.
//
// **One renderer for every kind, and it is a fixture rather than the product's** —
// because what is under test here is the *route*: which target each kind goes to,
// whether the gate keeps the editor's notice from a player, whether the throttle
// holds, and what the log lines say. The product's renderer is
// `internal/httpapi/play`'s wiring, which is not this work item's file.
func testChrome() events.ChromeRenderer {
	return func(ctx context.Context, slug string, change events.Notice) (live.Fragment, error) {
		switch change.Kind {
		case events.KindInitiative:
			return live.RenderInitiative(ctx, live.InitiativeView{
				Turn: "p2",
				Entries: []live.InitiativeEntry{
					{PlacementID: "p1", Name: "Ashbear", Initiative: 18},
					{PlacementID: "p2", Name: "Mirela", Initiative: 14},
				},
			})
		case events.KindChat:
			return live.RenderChatLine(ctx, chat.Message{
				Seq: 7, Author: "Tobin", Body: "It opens.", At: momentUTC,
			})
		case events.KindDice:
			return live.RenderDiceLine(ctx, live.DiceLineView{
				Actor: "Mirela", Expression: "2d6+3", Total: 14,
				Breakdown: []int{6, 5}, At: momentUTC,
			})
		case events.KindTurn:
			return live.RenderAnnouncement(ctx, live.AnnouncementView{
				Sentence: "It is Mirela's turn.",
			})
		case events.KindDegradedWatch:
			return live.RenderNotice(ctx, live.NoticeView{
				Kind:   live.NoticeDegradedWatch,
				Detail: "Pages may be behind what the watch has seen.",
			})
		case events.KindReconcileCapped:
			return live.RenderNotice(ctx, live.NoticeView{
				Kind: live.NoticeReconcileCapped,
			})
		case events.KindStreamDegraded:
			return live.RenderNotice(ctx, live.NoticeView{
				Kind: live.NoticeStreamDegraded,
			})
		case events.KindChange, events.KindNone, events.KindNotAKind:
			// Unreachable: `fragment` handles `KindChange` before it calls the
			// renderer, and a kind outside the closed set is a fixture bug.
			return live.Fragment{}, errors.New("events_test: that kind has no renderer")
		default:
			return live.Fragment{}, errors.New("events_test: that kind is not one this " +
				"build knows")
		}
	}
}

// sidebar is the server this file's tests run against: the stream, a chrome
// renderer, and a log every line goes to.
//
// The hub comes first in the return so a test publishes to it, then the log, then the
// server — which is the order a reader wants, because publishing is the act and
// reading the wire is the assertion.
func sidebar(
	t *testing.T,
	mutate func(*events.Handler),
) (*events.Hub, *logCapture, *httptest.Server) {
	t.Helper()

	hub := newHub(t)
	captured := &logCapture{t: t}

	server := serveWith(t, hub, captured, func(handler *events.Handler) {
		handler.Chrome = testChrome()

		if mutate != nil {
			mutate(handler)
		}
	})

	return hub, captured, server
}

// TestTheSidebarCarriesTheFiveFragmentFamilies is the route's central claim, restated
// for the play surface: every kind reaches its own target, at its own region, with
// its own mode.
//
// **Both halves per kind**, and the second is the one that catches a real mistake: a
// frame that carries the right fragment at the right *selector* is only half the
// claim — `mode: append` into the tracker would replace the order rather than extend
// it, and a reader would watch combatants vanish.
func TestTheSidebarCarriesTheFiveFragmentFamilies(t *testing.T) {
	t.Parallel()

	for _, subject := range []struct {
		kind     events.Kind
		selector string
		mode     string
		contains string
	}{
		{
			kind: events.KindInitiative, selector: initiativeSelector(), mode: "inner",
			contains: "Ashbear",
		},
		{
			kind: events.KindChat, selector: chatSelector(), mode: "append",
			contains: "It opens.",
		},
		{
			kind: events.KindDice, selector: diceSelector(), mode: "append",
			contains: "14",
		},
		{
			kind: events.KindTurn, selector: announceSelector(), mode: "inner",
			contains: "It is Mirela&#39;s turn.",
		},
		{
			kind: events.KindDegradedWatch, selector: noticeSelector(), mode: "inner",
			contains: "not being watched",
		},
	} {
		t.Run(subject.kind.String(), func(t *testing.T) {
			t.Parallel()

			hub, _, server := sidebar(t, nil)

			//nolint:bodyclose // `open` registers the body's close as a cleanup, and this
			// test's whole subject is a response that is deliberately never closed by
			// the test body.
			lines := read(open(t, server, gmRequestor()))
			lines.next(t) // the opening comment

			hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: subject.kind})

			frame := sseFields(lines.next(t))
			joined := strings.Join(frame.lines(), "\n")

			if got := frame.get("selector"); got != subject.selector {
				t.Errorf("the frame's selector = %q, want %q. Every family has its own "+
					"mount point, and a patch into somebody else's replaces their content",
					got, subject.selector)
			}

			if got := frame.get("mode"); got != subject.mode {
				t.Errorf("the frame's mode = %q, want %q. `append` is how a log grows and "+
					"`inner` is how a panel's contents are replaced; the two are not "+
					"interchangeable and a log that replaces its contents loses the "+
					"reader's transcript",
					got, subject.mode)
			}

			if !strings.Contains(joined, subject.contains) {
				t.Errorf("the fragment does not carry %q:\n%s", subject.contains, joined)
			}

			// And it parses as HTML rather than being JSON a client would render.
			// §7.5's rule is "rendered DOM fragments", and a client renderer could not
			// reproduce the server's escaping.
			if strings.HasPrefix(strings.TrimSpace(joined), "{") {
				t.Errorf("the payload is a JSON document; §7.5 requires rendered DOM "+
					"fragments:\n%s", joined)
			}
		})
	}
}

// The selectors, spelled out here so the frame assertions cannot drift from the
// handler's own constants — which are unexported, and a test that read them would be
// testing a constant against itself.
func initiativeSelector() string { return `[data-chrome="` + live.InitiativeHook + `"]` }
func chatSelector() string       { return `[data-chrome="` + live.ChatLogHook + `"]` }
func diceSelector() string       { return `[data-chrome="` + live.DiceHook + `"]` }
func announceSelector() string   { return `[data-chrome="` + live.AnnounceHook + `"]` }
func noticeSelector() string     { return `[data-chrome="` + live.NoticeHook + `"]` }

// --- Prohibition 1: a patch whose target is the focused element ---------------------

// TestAPatchIsRefusedWhenItsTargetIsFocusable is §7.5's first prohibition, as a
// **server-side decision about what it emits**.
//
// The renderer here returns a fragment for a target that `live.Targets` says is a
// focusable element — which is exactly the state a mistake produces, and the state a
// client-side guard would have to catch after the bytes had already gone out. The
// route must refuse to write it, and the refusal must be **visible in the log**,
// because a silently dropped fragment is indistinguishable from nothing happening.
func TestAPatchIsRefusedWhenItsTargetIsFocusable(t *testing.T) {
	t.Parallel()

	hub, captured, server := sidebar(t, func(handler *events.Handler) {
		handler.Chrome = func(
			ctx context.Context, slug string, change events.Notice,
		) (live.Fragment, error) {
			fragment, err := testChrome()(ctx, slug, change)
			if err != nil {
				return live.Fragment{}, fmt.Errorf("render the fixture fragment: %w", err)
			}

			// The one mutation, and **only** for the turn change, so the recovery
			// assertion below exercises a fragment the product would really write. A
			// mutator that broke every kind would make "the connection survived" true
			// for the wrong reason.
			if change.Kind != events.KindTurn {
				return fragment, nil
			}

			target, err := live.TargetByNameOrError("announcement")
			if err != nil {
				return live.Fragment{}, fmt.Errorf("look the target up: %w", err)
			}

			// The target declares itself a focus stop, which is the state §7.5's rule
			// is about. Everything else about the fragment is the product's own.
			target.Focusable = true
			fragment.Target = target

			return fragment, nil
		}
	})

	//nolint:bodyclose // See above: `open` owns the close.
	lines := read(open(t, server, gmRequestor()))
	lines.next(t)

	hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: events.KindTurn})

	// Nothing goes out. The opening comment is the only record, and the stream is
	// still alive, which is what distinguishes a refusal from a dropped connection.
	if !lines.silentFor(t, 1500*time.Millisecond) {
		t.Error("a patch into a focusable target was written. §7.5 forbids a patch that " +
			"can touch the focused element, and the server is the only party that knows " +
			"which DOM the patch will land in")
	}

	if !captured.contains("fragment_refused") {
		t.Errorf("the refusal was not logged; the log lines were:\n%s", captured.text())
	}

	if !captured.contains("target_focusable") {
		t.Errorf("the refusal did not name its reason; an operator reading "+
			"\"fragment_refused\" cannot tell a focus problem from a region problem. "+
			"The lines were:\n%s", captured.text())
	}

	// And the connection survives, because a refusal is a decision and not a fault.
	hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: events.KindDice})

	if frame := sseFields(lines.next(t)); len(frame.lines()) == 0 {
		t.Error("the connection did not recover after a refusal; dropping a GM's stream " +
			"over one bad fragment turns a bug into an outage")
	}
}

// TestAPatchIsRefusedWhenItsFragmentCarriesAFocusStop is the same prohibition from the
// payload's side.
//
// A fragment holding a `<button>` inserted into a region a reader may be interacting
// with puts a new focus stop inside their focus, and removing it later takes that stop
// away. The chat log's line and the editor's notice both carry buttons or could, and
// the refusal is what makes that a build failure rather than a reader's lost caret.
func TestAPatchIsRefusedWhenItsFragmentCarriesAFocusStop(t *testing.T) {
	t.Parallel()

	hub, captured, server := sidebar(t, func(handler *events.Handler) {
		handler.Chrome = func(
			ctx context.Context, slug string, change events.Notice,
		) (live.Fragment, error) {
			// A well-formed fragment for the *right* target, holding a control. That
			// is the shape a component change produces: a chat line that grows a
			// "recall" button, say.
			return live.Fragment{
				Target: mustTarget(t, "chat log"),
				Region: live.RegionLog,
				Markup: `<li class="chat-message"><button type="button" ` +
					`class="target">Recall</button></li>`,
			}, nil
		}
	})

	//nolint:bodyclose // See above: `open` owns the close.
	lines := read(open(t, server, gmRequestor()))
	lines.next(t)

	hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: events.KindChat})

	if !lines.silentFor(t, 1500*time.Millisecond) {
		t.Error("a fragment carrying a focus stop was written. §7.5 forbids a patch " +
			"that can touch the focused element, and a new focus stop inside a region a " +
			"reader may be interacting with is that")
	}

	if !captured.contains("fragment_holds_focus_stop") {
		t.Errorf("the refusal did not name its reason; the log lines were:\n%s",
			captured.text())
	}

	// And it names *what* held the stop, without naming what the fragment said — the
	// fragment's own text is content S-12.3 forbids in an event.
	if !captured.contains("button") {
		t.Errorf("the refusal did not name the offending element; an operator cannot "+
			"act on \"a fragment held something\". The lines were:\n%s", captured.text())
	}

	if captured.contains("Recall") {
		t.Errorf("the log lines carry the fragment's own text; S-12.3 forbids an event "+
			"carrying content, and a chat message body is content. The lines were:\n%s",
			captured.text())
	}
}

// TestAPatchIsRefusedWhenTheFragmentDoesNotMatchItsTarget is prohibition four: a
// fragment cannot be rendered into the wrong region.
//
// §7.5's announced-content table decides which region each thing goes in, and the
// only way that survives a change to a component is if the server refuses the
// mismatch rather than relying on a reader of the code.
func TestAPatchIsRefusedWhenTheFragmentDoesNotMatchItsTarget(t *testing.T) {
	t.Parallel()

	for _, subject := range []struct {
		name   string
		target string
		region live.Region
		want   string
	}{
		{
			name: "a turn change into the initiative track",
			// The tracker is silent, and a fragment that claims to announce into it is
			// a re-order read aloud.
			target: "initiative", region: live.RegionStatus, want: "silent_fragment_announced",
		},
		{
			name:   "a roll into the polite status region",
			target: "announcement", region: live.RegionLog, want: "wrong_region",
		},
		{
			name:   "a degraded condition into the polite status region",
			target: "announcement", region: live.RegionAlert, want: "wrong_region",
		},
	} {
		t.Run(subject.name, func(t *testing.T) {
			t.Parallel()

			hub, captured, server := sidebar(t, func(handler *events.Handler) {
				handler.Chrome = func(
					ctx context.Context, slug string, change events.Notice,
				) (live.Fragment, error) {
					return live.Fragment{
						Target: mustTarget(t, subject.target),
						Region: subject.region,
						Markup: `<p>A sentence.</p>`,
					}, nil
				}
			})

			//nolint:bodyclose // See above: `open` owns the close.
			lines := read(open(t, server, gmRequestor()))
			lines.next(t)

			hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: events.KindTurn})

			if !lines.silentFor(t, 1500*time.Millisecond) {
				t.Errorf("a fragment declaring region %q was written into the %q target, "+
					"whose region is not that. §7.5's table decides this, and the server "+
					"is the only party that can enforce it",
					subject.region, subject.target)
			}

			if !captured.contains(subject.want) {
				t.Errorf("the refusal did not name %q; the log lines were:\n%s",
					subject.want, captured.text())
			}
		})
	}
}

// mustTarget is a target lookup that fails the test rather than returning an error.
func mustTarget(t *testing.T, name string) live.Target {
	t.Helper()

	target, err := live.TargetByNameOrError(name)
	if err != nil {
		t.Fatalf("look up the %q target: %v", name, err)
	}

	return target
}

// --- Prohibition 2: insertions into a live region are throttled to 1/second ----------

// TestTwoAnnouncedInsertionsAreNeverCloserThanOneSecond is §7.5's throttle, measured
// on the wire rather than inferred from a constant.
//
// **The gap between arrival times, not the number of frames.** "Nothing arrives" and
// "a frame arrives" are each satisfied by a broken throttle — one that never sends and
// one that sends everything — and only the interval between two real frames is a
// claim about the rule itself.
func TestTwoAnnouncedInsertionsAreNeverCloserThanOneSecond(t *testing.T) {
	t.Parallel()

	hub, _, server := sidebar(t, nil)

	//nolint:bodyclose // See above: `open` owns the close.
	lines := read(open(t, server, gmRequestor()))
	lines.next(t)

	// Six chat lines, published as fast as the test can. Five of them must coalesce
	// into one second's worth of deliveries and one must wait its turn; the point is
	// the *gaps*, and six is enough to have five of them.
	for range 6 {
		hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: events.KindChat})
	}

	const want = 5

	for index := range want {
		before := time.Now()
		frame := sseFields(lines.next(t))

		gap := time.Since(before)

		if len(frame.lines()) == 0 {
			t.Fatalf("insertion %d carried no fragment: %v", index+1, frame.fields)
		}

		// The first insertion may go immediately — the interval is a floor, not a
		// delay — so only the ones after it are bounded from above and below.
		if index == 0 {
			continue
		}

		if gap < 900*time.Millisecond {
			t.Errorf("insertion %d arrived %s after the previous one, under the "+
				"shipped %s floor. §7.5 throttles insertions into a live region to "+
				"1/second, and §7.10 lists a live region firing more than once per "+
				"second as prohibited",
				index+1, gap.Round(time.Millisecond), time.Second)
		}
	}
}

// TestAKeepAliveIsNotAnInsertionAndDoesNotConsumeTheBudget is the second half of the
// same prohibition, and it is the half that is easy to break and invisible when it is.
//
// An SSE comment (`: text`) inserts nothing: it exists so an intermediary's idle timer
// and a vanished socket are both noticed. So a stream that interleaves keep-alives
// every few milliseconds must still deliver announcements one a second apart — and the
// keep-alive must not *delay* one either, which is what would happen if the comment
// path and the announcement path shared a ticker.
//
// The handler's `KeepAlive` field is set to 5ms here for exactly this reason, and it
// is the only knob in the package: `internal/httpapi/play`'s two durations are fields
// for the same reason, and this one exists because a 15-second keep-alive would put
// this test at half a minute of wall clock.
func TestAKeepAliveIsNotAnInsertionAndDoesNotConsumeTheBudget(t *testing.T) {
	t.Parallel()

	hub, _, server := sidebar(t, func(handler *events.Handler) {
		handler.KeepAlive = 5 * time.Millisecond
	})

	//nolint:bodyclose // See above: `open` owns the close.
	lines := read(open(t, server, gmRequestor()))
	lines.next(t)

	// Let several keep-alives go by before the first announcement, which is the
	// interesting order: a keep-alive that *spent* the budget would delay the
	// announcement by a whole interval each time.
	time.Sleep(80 * time.Millisecond)

	start := time.Now()
	hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: events.KindTurn})

	// `nextInsertion`, not `next`: a keep-alive every 5ms means the next *record* is
	// almost always a comment, and the assertion is about the first record that is
	// not one.
	if frame := lines.nextInsertion(t); len(frame.lines()) == 0 {
		t.Fatalf("no announcement arrived after a quiet period carrying several "+
			"keep-alives: %v", frame.fields)
	}

	// **One interval and a half, and not less.** The throttle is a *floor on the gap*
	// and not a delay before the first insertion, but the ticker starts when the stream
	// does — so the first announcement waits for the next tick, which is at most one
	// interval away. A keep-alive that had spent the budget would add an interval per
	// keep-alive, and five of those is what the bound rules out.
	const oneIntervalAndAHalf = 1500 * time.Millisecond

	first := time.Since(start)

	if first > oneIntervalAndAHalf {
		t.Errorf("the first announcement took %s after its change, past %s. The ticker "+
			"has been running since the stream opened, so the wait is at most one "+
			"interval — a longer one means the comment path spent the announcement "+
			"budget, and a comment inserts nothing",
			first.Round(time.Millisecond), oneIntervalAndAHalf)
	}

	hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: events.KindDice})

	before := time.Now()
	if frame := lines.nextInsertion(t); len(frame.lines()) == 0 {
		t.Fatalf("the second announcement carried no fragment: %v", frame.fields)
	}

	if gap := time.Since(before); gap < 900*time.Millisecond {
		t.Errorf("the second announcement arrived %s after the first, under the "+
			"shipped %s floor, with a keep-alive every 5ms throughout. The comment path "+
			"and the announcement path share a connection and must not share a budget",
			gap.Round(time.Millisecond), time.Second)
	}
}

// TestTwoQueuedLinesAreBothDelivered is the coalescing/queueing split, and it is the
// half of the throttle a reader would notice.
//
// Three page edits in a second are one fact and coalescing them is right — phase 7's
// test says so. Three **chat messages** are three messages, and coalescing them leaves
// the reader with a transcript with holes in it. So the two kinds must behave
// differently under the same one-a-second budget, and the difference is asserted by
// counting what arrives rather than by reading the code that decides it.
func TestTwoQueuedLinesAreBothDelivered(t *testing.T) {
	t.Parallel()

	hub, _, server := sidebar(t, func(handler *events.Handler) {
		handler.KeepAlive = time.Hour // no keep-alive noise; this test reads frames
	})

	//nolint:bodyclose // See above: `open` owns the close.
	lines := read(open(t, server, gmRequestor()))
	lines.next(t)

	for range 3 {
		hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: events.KindChat})
	}

	// Three announcements over two seconds, each carrying a fragment. Without the
	// queue the hub's one-slot buffer would have kept two of them and the reader
	// would have seen one line and lost two.
	for index := range 3 {
		if frame := sseFields(lines.next(t)); len(frame.lines()) == 0 {
			t.Fatalf("chat line %d of 3 carried no fragment: %v", index+1, frame.fields)
		}
	}
}

// --- Prohibition 3: the stream must not replay history into a live region ----------

// TestAConnectAnnouncesNothing is the prohibition, at the connection.
//
// A user joining mid-table must not hear the whole table read aloud. The three ways
// to get that wrong are all invisible from the server's side unless the server checks
// them, and this asserts all three on the wire: **a stream that emits anything on
// connect** (a snapshot fragment), **a fragment carrying the history**, and **an
// alert whose contents the document already had**.
//
// The last is a document property rather than a stream one, and
// `internal/web/components/live`'s
// `TestTheLiveRegionsAreEmptyAtLoadAndTheHistoryIsNot` is where it is held — this test
// is the socket half: a connected stream that says nothing.
func TestAConnectAnnouncesNothing(t *testing.T) {
	t.Parallel()

	_, _, server := sidebar(t, nil)

	//nolint:bodyclose // See above: `open` owns the close.
	lines := read(open(t, server, gmRequestor()))

	// The opening comment, and then nothing at all for longer than the throttle. A
	// stream that replayed the sidebar's history on connect would emit inside that
	// window whatever the campaign held.
	if first := sseFields(lines.next(t)); !first.isComment() {
		t.Errorf("the first record is %v, want a comment. A stream that opens with a "+
			"fragment is a stream that announces history before a reader asked for "+
			"anything, which is §7.5's replay rule", first.fields)
	}

	if !lines.silentFor(t, 2500*time.Millisecond) {
		t.Error("a freshly connected stream emitted an insertion. §7.5: the chat and " +
			"dice logs must not replay history into a live region on connect, and a " +
			"reader who has script must hear only what arrives after they did")
	}
}

// TestAPlayerIsServedTheSidebarAndNeverTheEditorsPagePaths is the gate change, in both
// directions, and it is the security claim the change costs.
//
// The stream's gate moved from `RequireEdit` to `RequirePlay` so a player could have the
// sidebar, and the cost of that move is that the *payload* is now filtered inside the
// handler. Both halves are asserted, and the order is the order of the risk:
//
//  1. **the player is served** — a positive assertion, because a fix that silenced the
//     stream for players would satisfy half two alone and leave the sidebar dead;
//  2. **the player is never told a page path** — the editor's notice carries one, and a
//     page path is the campaign's own directory structure, so the assertion is that a
//     player receives **nothing at all** when the watcher publishes a change. Not a
//     redacted notice: nothing;
//  3. **the GM still is** — because the change moved a gate, not a feature.
func TestAPlayerIsServedTheSidebarAndNeverTheEditorsPagePaths(t *testing.T) {
	t.Parallel()

	hub, _, server := sidebar(t, nil)

	//nolint:bodyclose // See above: `open` owns the close.
	player := read(open(t, server, playerRequestor()))
	player.next(t)

	// One: the player's stream is a real one and carries a real fragment.
	hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: events.KindChat})

	if frame := player.nextInsertion(t); len(frame.lines()) == 0 {
		t.Fatalf("a player's stream carried no fragment: %v", frame.fields)
	}

	// Two: a page change says nothing to them at all.
	hub.Publish(changed("Vault.md", content.OpUpsert))

	if !player.silentFor(t, 2500*time.Millisecond) {
		t.Error("a player received the editor's external-change notice. The stream's " +
			"gate is the play gate so that players can have the sidebar, and the notice " +
			"carries a page path — which is the campaign's own directory structure")
	}

	// And the GM still gets it: a fix that silenced the notice for everybody would
	// satisfy the assertion above.
	//nolint:bodyclose // See above: `open` owns the close.
	gm := read(open(t, server, gmRequestor()))
	gm.next(t)

	hub.Publish(changed("Vault.md", content.OpUpsert))

	frame := sseFields(gm.next(t))
	fragment := strings.Join(frame.lines(), "\n")

	if !strings.Contains(fragment, "This page changed on disk.") {
		t.Errorf("the GM did not receive the editor's notice:\n%s", fragment)
	}

	if got := frame.get("selector"); got != "#editor-change-notice" {
		t.Errorf("the GM's frame names the selector %q, want the editor's own notice "+
			"container. The editor's notice is `internal/web/components/edit`'s "+
			"element and is deliberately not one of the live chrome's five targets, "+
			"because a container with two owners gets patched into by whichever "+
			"selector a caller invents", got)
	}
}

// --- The wire format, held against the vendored module -----------------------------

// TestTheFrameIsWrittenTheWayTheVendoredModuleReadsIt is the correction, held, over
// the **raw bytes**.
//
// Phase 7 wrote `selector …`, `mode …` and `elements …` as bare event-stream field
// lines with no `data:` prefix, and its own test parsed the frame with a reader that
// accepted bare lines — so the test asserted the encoder against itself and passed
// while the product received nothing. The `data:` prefix is the correction, and this
// test pins it in the one place no reader this repository also wrote can be the thing
// under test: the bytes off the socket.
//
// Why it matters that the assertion is over raw bytes and not a parsed frame: a
// parser that drops unknown fields would make the unprefixed spelling *unreadable*
// rather than *wrong*, and a test written against the parser would then be testing
// the parser. The grammar says a colon-less line is a field name with an empty value;
// the client's parser then handles four names and ignores the rest **silently**, and
// "silently" is the whole problem.
//
// Measured, in a real Chromium against `internal/web/static/vendor/star.js` at the
// pinned version, twice — at `mode: inner` and at `mode: append` with three `elements`
// lines: the prefixed frame patched the target, and the unprefixed frame left it
// **byte-identical** with an empty console.
func TestTheFrameIsWrittenTheWayTheVendoredModuleReadsIt(t *testing.T) {
	t.Parallel()

	hub, _, server := sidebar(t, func(handler *events.Handler) {
		handler.KeepAlive = time.Hour
	})

	//nolint:bodyclose // `open` registers the body's close as a cleanup, and this test's
	// whole subject is a response that is deliberately never closed by the test body.
	resp := open(t, server, gmRequestor())

	// A raw reader rather than `read`, and the comment says why: this test asserts the
	// exact bytes *including* the blank line that terminates a record, and the shared
	// reader assembles records and drops their terminators.
	reader := bufio.NewReader(resp.Body)

	if _, isComment := readRecord(t, reader); !isComment {
		t.Fatal("the stream's first record is not a comment; the handler flushes before " +
			"it waits for anything, so the buffer is empty when it does")
	}

	hub.Publish(changed("Vault.md", content.OpUpsert))

	record, isComment := readRecord(t, reader)
	if isComment {
		t.Fatal("the change was answered with a comment rather than a patch")
	}

	want := strings.Join([]string{
		"event: datastar-patch-elements",
		"id: 0",
		"data: selector #editor-change-notice",
		"data: mode inner",
	}, "\n") + "\n"

	prefix := want + "data: elements "

	if !strings.HasPrefix(record, prefix) {
		t.Errorf("the record does not begin %q.\nIt begins:\n%s\n"+
			"A field with no `data:` prefix is not a field the vendored module reads: "+
			"the event-stream grammar treats a colon-less line as a field name with an "+
			"empty value, and Datastar's parser handles four names — `data`, `event`, "+
			"`id`, `retry` — and ignores every other without complaint",
			prefix, truncate(record, 400))
	}

	// The blank line that terminates a record is **consumed by `readRecord`** and so is
	// not in the returned bytes; that the terminator was there is structural, because
	// the loop below cannot return without having seen one. What is asserted here is
	// the half that is not: the last line is newline-terminated, so the terminator is
	// a *blank* line rather than an unterminated final field.
	if !strings.HasSuffix(record, "\n") {
		t.Errorf("the record's last line carries no newline:\n%s. The blank line "+
			"that terminates a record has to begin on a fresh line, or the final field "+
			"is unterminated and the record is never dispatched", truncate(record, 200))
	}

	// Every remaining line carries the prefix, because a multi-line fragment is one
	// `data: elements` per line — the parser joins the payloads with a newline before
	// splitting the result into `name value` pairs. One unprefixed line in the middle
	// would truncate the fragment there.
	for index, line := range strings.Split(strings.TrimSuffix(record, "\n"), "\n") {
		if index < 5 {
			continue
		}

		if !strings.HasPrefix(line, "data: elements ") {
			t.Errorf("record line %d = %q, want the `data: elements ` prefix on every "+
				"line of the fragment. One line without it is a line the client never "+
				"sees, so the fragment arrives truncated", index+1, truncate(line, 120))
		}
	}
}

// readRecord reads one event-stream record and reports whether it was a comment.
//
// The bytes, not a parsed frame, and the blank line is consumed and not returned: the
// grammar's record separator is a fact about the bytes and a test about the format has
// to see it.
func readRecord(t *testing.T, reader *bufio.Reader) (string, bool) {
	t.Helper()

	var record strings.Builder

	isComment := true

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read a record: %v", err)
		}

		line = strings.TrimRight(line, "\r\n")

		if strings.TrimSpace(line) == "" {
			if record.Len() == 0 {
				// Unreachable from a client, and a real failure if it happens: a blank
				// line where no record was open is the grammar's way of saying the
				// previous record ended and this one has not started, and a reader that
				// treated it as a record would return an empty frame.
				t.Fatal("a record separator arrived with no record before it")
			}

			return record.String(), isComment
		}

		if !strings.HasPrefix(line, ":") {
			isComment = false
		}

		record.WriteString(line)
		record.WriteByte('\n')
	}
}

// truncate shortens a body for a failure message.
func truncate(body string, limit int) string {
	if len(body) <= limit {
		return body
	}

	return body[:limit] + "…"
}

// TestTheVendoredModuleListensForTheEventThisRouteWrites closes the loop between the
// two halves.
//
// A digest proves the vendored bytes have not changed since somebody wrote the
// digest; it does not prove they are listening for what this route writes. So this
// reads the committed module and asserts the event name and the three field names the
// handler emits are in its source — and the negative half, that a field the handler
// does **not** write is not what the module dispatches on.
//
// This is the test that would have caught phase 7's frame, and it is why it exists
// rather than the digest alone.
func TestTheVendoredModuleListensForTheEventThisRouteWrites(t *testing.T) {
	t.Parallel()

	module, err := os.ReadFile(filepath.Join(repositoryRoot(t), vendoredDatastarPath))
	if err != nil {
		t.Fatalf("read %s: %v\nthe vendored module is the other half of this route's "+
			"wire format, and a route whose client was never staged is a stream "+
			"nothing acts on", vendoredDatastarPath, err)
	}

	source := string(module)

	for _, wanted := range []string{
		`"datastar-patch-elements"`,
		`"elements"`,
		`"selector"`,
		`"mode"`,
	} {
		if !strings.Contains(source, wanted) {
			t.Errorf("%s does not mention %s. The route writes that field on every "+
				"patch, so the client and the server would be speaking two wire "+
				"formats and nothing would arrive",
				vendoredDatastarPath, wanted)
		}
	}

	// The negative half: the module's *other* event name is the one this route must
	// not write. Every published npm build of Datastar before 1.0.0 called this
	// `datastar-merge-fragments`, and pinning one of those would produce a sidebar
	// that never updates — measured, not assumed.
	if strings.Contains(source, "datastar-merge-fragments") {
		t.Errorf("%s dispatches on `datastar-merge-fragments`, which is the spelling "+
			"every pre-1.0.0 build used and which this route does not write. Pin "+
			"Datastar 1.0.0 or later: the newest *published* npm package is 1.0.0-beta.11 "+
			"and speaks the old name",
			vendoredDatastarPath)
	}

	// And the pin names the version whose bytes these are.
	manifest := readVendorManifest(t)

	pinned := false

	for _, pkg := range manifest.Packages {
		for _, file := range pkg.Files {
			if file.Path != vendoredDatastarPath {
				continue
			}

			pinned = true

			if !strings.Contains(source, "Datastar v"+pkg.Version) {
				t.Errorf("%s does not carry the banner %q. The file's own embedded "+
					"version is how a reader of a vendored blob can tell which upstream "+
					"release it is without trusting the digest that describes it",
					vendoredDatastarPath, "Datastar v"+pkg.Version)
			}
		}
	}

	if !pinned {
		t.Errorf("no package in the vendor pin declares %s, so `make vendor-check` "+
			"verifies nothing about it and the module this route's wire format depends "+
			"on is trusted rather than checked", vendoredDatastarPath)
	}
}

// vendoredDatastar and vendoredDatastarPath are the same file named two ways: one for
// `os.ReadFile` relative to this test's directory, one for the manifest's paths,
// which are repository-relative.
const (
	// The paths are repository-relative because that is what the manifest declares,
	// and this test reads the manifest. `repositoryRoot` turns them into paths this
	// test's directory can open.
	//
	// **The manifest hangs off the repository root rather than off this directory**,
	// because those two paths are the contract and putting a copy of the manifest
	// beside the assets it describes would be a second answer to it.
	vendoredDatastarPath = "internal/web/static/vendor/star.js"
	repositoryDir        = "../../.."
	vendorManifest       = "../../../tools/vendor.json"
)

// repositoryRoot resolves the repository root from this test's directory.
//
// Three levels up: `events` → `httpapi` → `internal` → the repository root.
func repositoryRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(repositoryDir)
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}

	return root
}

// vendorFile is one committed vendor file as the pin describes it.
type vendorFile struct {
	Path   string `json:"path"`
	Origin string `json:"origin"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// The two fields below are read by nothing and declared only so the struct's shape is
// the manifest's, which is what makes `json.Unmarshal` into it a check on the file's
// syntax rather than on a subset of its keys.

// vendorManifestDoc is the pin.
type vendorManifestDoc struct {
	Schema   int `json:"schema"`
	Packages []struct {
		Name    string       `json:"name"`
		Version string       `json:"version"`
		Files   []vendorFile `json:"files"`
	} `json:"packages"`
}

// readVendorManifest reads `tools/vendor.json`.
func readVendorManifest(t *testing.T) vendorManifestDoc {
	t.Helper()

	raw, err := os.ReadFile(vendorManifest)
	if err != nil {
		t.Fatalf("read %s: %v", vendorManifest, err)
	}

	var doc vendorManifestDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", vendorManifest, err)
	}

	return doc
}

// --- The log lines -----------------------------------------------------------------------

// logCapture is the route's logger, written into a buffer a test can read.
//
// **`*testing.T` as well as the buffer**, and the reason is phase 7's own note: the
// handler's failure paths are deliberately recoverable — a fragment that will not
// render logs an error and keeps the connection — so with a nil logger those paths
// are invisible and a test watching only the wire cannot tell "no notice arrived"
// from "a notice arrived and failed to render".
type logCapture struct {
	t *testing.T

	mu    sync.Mutex
	lines []string
}

// Write implements `io.Writer`, one log record at a time.
func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, string(p))
	l.t.Logf("%s", p)

	return len(p), nil
}

// contains reports whether any recorded line holds the fragment.
func (l *logCapture) contains(fragment string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, line := range l.lines {
		if strings.Contains(line, fragment) {
			return true
		}
	}

	return false
}

// text is every recorded line, for a failure message.
func (l *logCapture) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return strings.Join(l.lines, "\n")
}

// TestNoLogLineCarriesARollOrAChatBody is S-12.3 for this route, from the other side.
//
// The fragment carries a roll's numbers and a chat message's text **to the reader** —
// that is the dice log's whole content. No **log line** may carry them. The distinction
// is worth stating because it is easy to collapse: the rule is about observability,
// and the enforcement is that `events.Notice` has no field a roll or a body could be
// passed through, so there is nowhere for one to go even by accident.
func TestNoLogLineCarriesARollOrAChatBody(t *testing.T) {
	t.Parallel()

	hub, captured, server := sidebar(t, nil)

	//nolint:bodyclose // See above: `open` owns the close.
	lines := read(open(t, server, gmRequestor()))
	lines.next(t)

	hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: events.KindDice})
	if frame := sseFields(lines.next(t)); len(frame.lines()) == 0 {
		t.Fatalf("no roll arrived: %v", frame.fields)
	}

	hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: events.KindChat})
	if frame := sseFields(lines.next(t)); len(frame.lines()) == 0 {
		t.Fatalf("no chat line arrived: %v", frame.fields)
	}

	hub.Publish(changed("Vault.md", content.OpUpsert))
	if frame := sseFields(lines.next(t)); len(frame.lines()) == 0 {
		t.Fatalf("no editor notice arrived: %v", frame.fields)
	}

	// The three bodies the stream is carrying right now.
	for _, forbidden := range []string{"2d6+3", "It opens.", "This page changed on disk."} {
		if captured.contains(forbidden) {
			t.Errorf("a log line carries %q. AGENTS.md's invariant is that no event "+
				"carries secret content, file contents or dice results, and a log "+
				"aggregator is where a `[!secret]` callout ends up when somebody is "+
				"debugging. The lines were:\n%s",
				forbidden, captured.text())
		}
	}

	// The positive half: the lines exist at all, and they name the kinds. A suite that
	// passed because nothing was logged would satisfy the assertions above vacuously.
	for _, wanted := range []string{"stream_opened", "dice", "chat"} {
		if !captured.contains(wanted) {
			t.Errorf("no log line mentions %q; the assertions above would pass "+
				"vacuously if the route were silent. The lines were:\n%s",
				wanted, captured.text())
		}
	}
}

// TestAHostileChatBodyArrivesEscaped is the escaping claim, as a positive assertion
// over the product rather than as a fixture in the audits' violation table.
//
// **A chat message is the one piece of this surface a reader cannot be trusted to have
// authored**, and it is the only thing here that is not this repository's own copy. So
// the payload is the shape of a real injection — a quote that closes the attribute, a
// handler, and a tag — and the assertions are that nothing in it executes: no
// `<script>`, no `on*` attribute anywhere in the fragment.
//
// The positive half matters: a fragment that rendered nothing would satisfy both, and
// `TestAHostileChatBodyArrivesEscaped` requires the payload to still be *present* as
// text. That is the difference between "the escaper worked" and "the fragment was
// discarded".
func TestAHostileChatBodyArrivesEscaped(t *testing.T) {
	t.Parallel()

	const hostile = `a" onfocus="alert(1)"><script>alert(2)</script><img src=x onerror=alert(3)>`

	hub, _, server := sidebar(t, func(handler *events.Handler) {
		handler.Chrome = func(
			ctx context.Context, slug string, change events.Notice,
		) (live.Fragment, error) {
			return live.RenderChatLine(ctx, chat.Message{
				Seq: 7, Author: hostile, Body: hostile, At: momentUTC,
			})
		}
	})

	//nolint:bodyclose // See above: `open` owns the close.
	lines := read(open(t, server, gmRequestor()))
	lines.next(t)

	hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: events.KindChat})

	fragment := strings.Join(lines.nextInsertion(t).lines(), "\n")

	// Present as text, in both fields the row renders.
	if !strings.Contains(fragment, "onfocus") || !strings.Contains(fragment, "onerror") {
		t.Errorf("the hostile body is not in the fragment at all; the fields discarded "+
			"their input, which would make the assertions below vacuous. Fragment:\n%s",
			truncate(fragment, 400))
	}

	audit := parseDocument(
		t,
		renderedDocument{where: "a hostile chat line", body: []byte(fragment)},
	)

	scripts := findAll(audit.root, "script")
	if len(scripts) != 0 {
		t.Errorf("the fragment carries %d <script> element(s). The chat body went out as "+
			"markup rather than as text, and a message body is the one thing on this "+
			"surface a reader chooses:\n%s", len(scripts), truncate(fragment, 400))
	}

	audit.elements(func(node *html.Node) {
		for _, attribute := range node.Attr {
			name := strings.ToLower(attribute.Key)
			if strings.HasPrefix(name, "on") {
				t.Errorf("%s carries %s=%q; the body reached the reader as markup, and a "+
					"reader who could run script in another reader's message would have "+
					"the whole campaign",
					nodePath(node), attribute.Key, truncate(attribute.Val, 80))
			}
		}
	})
}
