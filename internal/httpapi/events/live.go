package events

// The second egress: what a sidebar change is, which fragment it becomes, and the
// two decisions the server makes before the bytes are written.
//
// # One hub, two egress representations of the same state
//
// UI §7.5's resolution table is the rule this file implements, and the table is
// worth restating here where a reader will meet it:
//
//	| Channel            | Carries                                                                                       |
//	|--------------------|-----------------------------------------------------------------------------------------------|
//	| WebSocket /ws      | client to server: intents, presence. server to client: ordered state — placement deltas,      |
//	|                    | rolls, clock, snapshot, applied/rejected — consumed by PixiJS as structured data                |
//	| SSE /events        | server to client: rendered DOM fragments for the sidebar — initiative, chat, dice log,          |
//	|                    | connection and degraded notices — patched by Datastar. Plus the editor's external-change notice|
//
// **Two connections on the play page**, against plan §7's ceiling of about six, and
// §7's "no SSE on the VTT page" read as *no SSE for map state*. Presence rides the
// WebSocket because §7.1's protocol already defines it there and it needs no
// versioning; `announced.go` in `internal/web/components/live` records the whole
// table as data and asserts that no WebSocket row has a target here.
//
// The canvas needs structured deltas and the sidebar needs HTML, so the *state* is
// the same and the *representation* is not. That is why `Kind` exists: it says
// which representation a change is for, and it is the only thing this package's
// delivery policy branches on.
//
// # Why `Kind` is a field and not a second hub
//
// A second broker would mean a second set of buffers, a second set of counters and
// a second set of teardown semantics for the same campaign — and the whole reason
// the hub holds no goroutines is that it is a mutex, a map and a set of channels.
// One hub with two channels (see `noticeBuffer` and `lineBuffer`) is one object with
// two delivery policies, which is what the two egresses actually are.
//
// # Why nothing here knows what a chat message is
//
// `ChromeRenderer` is a function the composition root supplies, and this package
// holds no gameplay, no state and no `internal/realtime` import. That is the same
// line `internal/httpapi/play` draws — "a reader who finds a rule in this file has
// found it in the wrong layer" — and it is what lets the two routes change
// independently: `play` learns a new protocol frame and this package does not move.

import (
	"context"
	"log/slog"
	"strings"

	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/observability"
	"github.com/semiplane/semiplane/internal/web/components/live"
)

// Kind is which egress representation a change is for.
//
// The zero value is `KindChange`, the filesystem notice phase 7 shipped, so every
// existing publisher and every existing test is unchanged by the arrival of this
// field — which is deliberate, and the reason it is not a pointer or a separate
// type: a *default* that behaves as phase 7 behaved is a field a new publisher
// cannot get wrong by omitting it.
type Kind int

const (
	// KindChange is a settled change in the campaign's content root, and it is the
	// editor's external-change notice. It is the only kind with a path, and the only
	// one a GM is told about: the path is the campaign's own directory structure.
	//
	// `PublishSidebar` refuses it — see its doc comment for why that refusal is the
	// point rather than an oversight.
	KindChange Kind = iota

	// KindInitiative is the turn order as it now stands.
	//
	// **Coalescing**, and the reason is the target's patch mode: the tracker is
	// patched `inner`, so a newer order *replaces* the older one and nothing is
	// lost by dropping the older one. A reader who missed order 3 sees order 4, and
	// order 4 is the one that is true.
	KindInitiative

	// KindTurn is a turn change, which is §7.5's polite status row.
	KindTurn

	// KindChat is one chat line, and the first kind that **queues**.
	KindChat

	// KindDice is one roll, and queues for `KindChat`'s reason.
	KindDice

	// KindDegradedWatch is S-4.5's watcher in degraded mode.
	KindDegradedWatch

	// KindReconcileCapped is §4.7's secret reconciliation budget exhausted.
	KindReconcileCapped

	// KindStreamDegraded is the server saying it cannot serve this sidebar whole.
	KindStreamDegraded
)

// Queues reports whether a kind's deliveries are queued rather than coalesced.
//
// **Two policies and the difference is what a reader would miss.** A coalescing kind
// states a *fact about current state* — the order, whose turn it is, whether the
// watcher is degraded — and the newest statement of a fact supersedes the older one,
// so one buffer slot is correct and dropping the older is free. A queuing kind
// carries an *event that happened*, and dropping one of two chat messages in the
// same second leaves a transcript with a hole in it, so those get a queue and the
// oldest goes if it must.
//
// The rule is not "chat queues and everything else coalesces", because that is a
// list rather than a property. It is: does the newer value make the older one
// **untrue**, or merely **old**?
func (k Kind) Queues() bool {
	switch k {
	case KindChat, KindDice:
		return true
	case KindChange, KindInitiative, KindTurn,
		KindDegradedWatch, KindReconcileCapped, KindStreamDegraded:
		return false
	case KindNone, KindNotAKind:
		return false
	default:
		return false
	}
}

// KindNone and KindNotAKind bound the closed set, so a value that did not come from
// one of these constants is detectable rather than quietly behaving like the zero
// value. `KindNotAKind` is `Kind(len(kinds)) + 1` in spirit and is written as a
// literal because a test asserting the *count* is more useful than one asserting an
// arithmetic identity.
const (
	// KindNone is not a kind. It is a distinct value from `KindChange` so that a
	// zero-valued `Notice` built by hand rather than by a publisher is detectable:
	// `KindChange` is a real delivery, and treating an unset field as one would
	// deliver a notice naming a page called "".
	KindNone Kind = -1

	// KindNotAKind is one past the end, used only by the tests that assert the set
	// is closed.
	KindNotAKind Kind = 64
)

// kinds is the closed set, in §7.5's own order of announced content.
//
// A function rather than a package-level slice, for the reason `live.Targets()`
// returns one rather than holding one.
func kinds() []Kind {
	return []Kind{
		KindChange,
		KindInitiative,
		KindTurn,
		KindChat,
		KindDice,
		KindDegradedWatch,
		KindReconcileCapped,
		KindStreamDegraded,
	}
}

// Kinds returns every declared kind, in §7.5's order.
func Kinds() []Kind {
	return kinds()
}

// String names a kind for a log line.
//
// **The name and not a number**, and not the content it announces: an event name
// that reaches a log aggregator must not carry a chat line, a roll or a page path,
// and a kind's name is the one string about it that cannot.
func (k Kind) String() string {
	switch k {
	case KindChange:
		return "change"
	case KindInitiative:
		return "initiative"
	case KindTurn:
		return "turn"
	case KindChat:
		return "chat"
	case KindDice:
		return "dice"
	case KindDegradedWatch:
		return "degraded_watch"
	case KindReconcileCapped:
		return "reconcile_capped"
	case KindStreamDegraded:
		return "stream_degraded"
	case KindNone, KindNotAKind:
		return "not_a_kind"
	default:
		return "not_a_kind"
	}
}

// ChromeRenderer turns a sidebar change into the fragment that goes out.
//
// Two arguments and no more, and the reason is §7.5's first prohibition rather than
// tidiness: the renderer needs to know **which reader** it is rendering for, because
// a fragment carrying a focus stop is refused, and one carrying content a player may
// not see is worse. So the reader travels with the change rather than being captured
// in a closure at mount time — a closure over the requestor is how a stream opened by
// a GM keeps rendering for a GM after a re-authentication.
type ChromeRenderer func(ctx context.Context, slug string, change Notice) (live.Fragment, error)

// fragment is the one place the two egresses meet, and it is where §7.5's
// prohibitions are decided rather than promised.
//
// Five outcomes, and each is a different bug:
//
//  1. **the change is the editor's notice and the reader cannot edit** — refused
//     here rather than at the gate, because the gate has to be the lower one for the
//     sidebar to reach players at all (see `Mount`). The path never crosses the
//     connection.
//  2. **no renderer is wired** — a wiring fault. It is *not* an error: the editor
//     stream works without one, and a build that wires no chrome is a build whose
//     sidebar does not update, which is a degradation rather than a refusal.
//  3. **the renderer failed** — logged, connection kept, same reasoning as phase 7's
//     render failure.
//  4. **`live.Decide` refused the fragment** — §7.5's region rule and its focus
//     rule, as code. Logged with the reason; the connection continues.
//  5. **written**.
//
// The editor's notice is **not** offered to `Decide`, and that asymmetry is
// deliberate and is the reason `Decide` is scoped to `live`'s targets. The editor's
// notice contains a `<button>` — the Review control — and §7.5's focus rule is about
// *replacing* a focused element, not about a container being unable to hold a
// control. The button is inside a container the editor document owns, which is
// `inner`-patched, so the button itself is never replaced and the reader who is
// typing in the editor's textarea keeps their caret. Applying a live-region rule
// written for the play surface to the editor's rail would refuse a working feature
// and teach the next reader that the rule is advisory; the focus rule it *is* held
// by `TestAPatchCanNeverTouchTheFocusedElement`, over the editor's own document.
func (h *Handler) fragment(
	ctx context.Context,
	slug string,
	access campaigns.Access,
	notice Notice,
) (live.Fragment, bool) {
	if notice.Kind == KindChange {
		if !access.Tier.CanEdit() {
			// A player connected to the play surface. The stream stays open — it is
			// the player's sidebar — and this change is simply not for them. Nothing
			// about the campaign's page structure is written, not even the *existence*
			// of a change.
			return live.Fragment{}, false
		}

		fragment, err := renderNotice(ctx, slug, notice)
		if err != nil {
			h.logRenderFailure(ctx, slug, notice, err)

			return live.Fragment{}, false
		}

		return live.Fragment{
			Target: live.Target{
				Name:     "editor notice",
				Selector: noticeSelector,
				Region:   live.RegionStatus,
				Mode:     live.ModeInner,
			},
			Region: live.RegionStatus,
			Markup: fragment,
		}, true
	}

	if h.Chrome == nil {
		return live.Fragment{}, false
	}

	// **The context is the request's minus its cancellation**, for the reason
	// `renderNotice` sets out in `events.go` and the reason this line is load-bearing
	// rather than tidy: `templ`'s generated `Render` checks `ctx.Err()` before every
	// element and returns, and `middleware.Timeout` cancels the request context at the
	// handler budget. A renderer given the request's own context works for the first
	// slice of a stream's life and then fails on every change afterwards, logging
	// `deadline_exceeded` once per change while the sidebar silently stops updating —
	// which is precisely what happened here until this line was added, and the log
	// lines of the failing test are the record.
	fragment, err := h.Chrome(context.WithoutCancel(ctx), slug, notice)
	if err != nil {
		h.logRenderFailure(ctx, slug, notice, err)

		return live.Fragment{}, false
	}

	decision := live.Decide(fragment)
	if !decision.Write {
		h.log(ctx, slog.LevelWarn, "events.fragment_refused",
			slog.String("campaign", slug),
			slog.String("kind", notice.Kind.String()),
			slog.String("target", fragment.Target.Name),
			slog.String("reason", string(decision.Reason)),
			// The offending elements' **tag names**, never the fragment. A refusal is
			// an event and S-12.3 forbids an event carrying content, and a fragment
			// holds chat text and dice numbers — so the log says what kind of element
			// was in it and nothing about what it said.
			slog.String("focus_stops", strings.Join(decision.FocusStops, ",")),
		)

		return live.Fragment{}, false
	}

	return fragment, true
}

// logRenderFailure records a fragment that would not render.
//
// **The campaign, the kind and the path — and nothing else**, for the reason
// AGENTS.md states as an invariant: no event carries secret content, file contents
// or dice results. The kind is a closed label; the path is the one field S-12.3
// explicitly permits; the error is a class name rather than a message, because a
// Markdown or YAML parser quotes the line it choked on and a chat body is not a
// line any parser choked on but a body is a body.
func (h *Handler) logRenderFailure(ctx context.Context, slug string, notice Notice, err error) {
	h.log(ctx, slog.LevelError, "events.fragment_render_failed",
		slog.String("campaign", slug),
		slog.String("kind", notice.Kind.String()),
		slog.String("path", notice.Path),
		slog.String("error_class", observability.ErrorClass(err)),
	)
}
