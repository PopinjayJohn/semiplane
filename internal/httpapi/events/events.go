package events

// The route: one long-lived response, one coalescing loop, and four lines of SSE
// per notice.
//
// # Why the stream outlives the handler budget
//
// Two layers above this handler will end it if it lets them, and both are correct
// for the responses they were written for:
//
//   - `middleware.Timeout` gives every request a deadline and buffers the body so
//     it can substitute a 504. Its `Flush` is the escape: the first flush commits
//     the response to being a stream, the buffer is written out, and every later
//     write goes straight through. So this handler flushes *before* it waits for
//     anything, and the buffer is empty when it does — a stream that waited for its
//     first notice before flushing would be buffered until the budget expired and
//     would then be answered 504, which for a client that connected and waited is
//     indistinguishable from the server refusing the connection.
//   - `http.Server.WriteTimeout` bounds how long a response body may take. It is
//     cleared here with `http.ResponseController.SetWriteDeadline`, which reaches
//     the real writer through each middleware wrapper's `Unwrap` — a chain that
//     `middleware` provides deliberately, and one of the two reasons it does.
//
// What is left is the deadline's *context* cancellation, which no layer can
// withdraw from a handler that has already started streaming, and this handler
// deliberately does not select on it. See `stream`.
//
// # A comment line is not an insertion
//
// The keep-alive every `keepAliveAfter` is an SSE comment (`: text`), which
// per the event-stream grammar is ignored by the client: it exists to move bytes so
// that an intermediary's idle timer and a vanished socket are both noticed. It does
// not insert anything, so §7.5's one-per-second throttle does not apply to it — a
// rule that would otherwise make a 15-second keep-alive impossible, since a
// 1/second limit on *all* writes would still permit it but a 1/second limit on
// writes *into a live region* is plainly about announcements, not about liveness.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/web/components/edit"
	"github.com/semiplane/semiplane/internal/web/components/live"
)

// The intervals, and both are constants rather than configuration.
//
// A knob here would be a knob an operator could set to zero, and a zero notice
// interval is a live region re-rendering as fast as a sync client writes — which is
// the failure §7.5's throttle exists to prevent and which no amount of client
// politeness can prevent from the other end.
const (
	// noticeInterval is §7.5's "insertions into a live region are throttled to
	// 1/second". It is a *floor* on the gap between two patches, not a target
	// rate: a notice that arrives just after a patch waits almost a second, and one
	// that arrives just before it goes out immediately.
	noticeInterval = time.Second

	// keepAliveAfter is how long a quiet stream waits before writing a comment.
	//
	// It is also how long a *vanished* client goes unnoticed, because a write to a
	// closed socket is the only signal that a browser is gone: the request context
	// is not usable for that (see `stream`), and TCP will happily accept writes into
	// a socket whose peer has gone until the buffer fills. A quarter of a minute is
	// a compromise between the two: long enough not to be chatty, short enough that a
	// disconnected GM's subscription is not held for a minute afterwards.
	keepAliveAfter = 15 * time.Second
)

// The response headers an event stream needs.
const (
	// eventStreamType is the media type from the event-stream grammar. A browser
	// refuses to hand a response to `EventSource` under any other type, so this is
	// not a preference.
	eventStreamType = "text/event-stream; charset=utf-8"
	// noBuffering is nginx's header for "do not buffer this response". A reverse
	// proxy that buffers turns the route into a route that works in testing and
	// delivers nothing in production, and it is invisible from here: the handler
	// flushes correctly and the bytes sit in the proxy.
	noBuffering = "X-Accel-Buffering"
)

// The Datastar patch event, written out rather than produced by a helper.
//
// Datastar's own Go SDK would be the tidier way to write these four lines, and it
// is not used, for two reasons that are the repository's rules rather than taste:
// adding a third-party dependency needs a decision record this work item cannot
// write, and `go.mod` belongs to the integrator. The wire format itself is
// Datastar's documented `datastar-patch-elements` event with its `selector`,
// `mode` and `elements` fields — a *rendered fragment* plus where it goes, which is
// the shape §7.5 requires, and deliberately not a JSON document a client renders.
//
// Named so that a test can assert them: the selector is the half of the patch that
// carries §7.5's "patches must never touch the focused element", and a test that
// cannot name the selector cannot check it.
const (
	patchEvent = "datastar-patch-elements"
	// patchModeInner replaces the target's *children* and leaves the target itself
	// alone. `outer` would replace the target element, and the target is the
	// container the editor document owns — replacing it would put the notice
	// somewhere other than where the editor put it, and would destroy any state the
	// client had attached to that element.
	patchModeInner = "inner"
	// noticeSelector is the editor's notice container, and the only element the
	// editor's patch may name. It is the container `edit.EditorRail` renders, and
	// the test that holds the focus rule asserts that this id is in the editor
	// document, that the element is not focusable, and that it holds nothing
	// focusable.
	noticeSelector = "#editor-change-notice"
)

// dataPrefix is the event-stream field name Datastar reads its attributes from,
// and **the correction this work item made to phase 7's frames**.
//
// ## Measured, not assumed
//
// Phase 7 wrote `selector …`, `mode …` and `elements …` as bare field lines, with
// no `data:` prefix, and its own test parsed the frame with a reader that accepted
// bare lines — so the test asserted the encoder against itself and passed while
// the product received nothing. The event-stream grammar ends a field at the first
// colon and treats a line without one as a field name with an empty value, so a
// bare `selector #x` line is not a field a client can read at all. Datastar's own
// parser is narrower still: it switches on the field name and handles `data`,
// `event`, `id` and `retry`, ignoring every other name without complaint.
//
// Two frames were pushed through a real Chromium against
// `internal/web/static/vendor/star.js` at the pinned version, the only difference
// being the prefix, each at `mode: inner` and again at `mode: append` with three
// `elements` lines:
//
//   - `data: selector #target`, `data: mode inner`, `data: elements <p>…</p>`
//     → the target's contents became the fragment, and the three-line append landed
//     as three siblings;
//   - `selector #target`, `mode inner`, `elements <p>…</p>`
//     → the target was **byte-identical afterwards**, and the console reported
//     nothing.
//
// The silent half is why this needed a browser. A handler that throws on a bad
// frame is a frame a reader sees missing and a GM reports as a bug; a handler that
// writes a frame nothing reads is a feature that never arrives, and the only
// symptom is a page that works. `TestTheFrameIsWrittenTheWayTheVendoredModuleReadsIt`
// is what keeps it fixed, by asserting the exact bytes rather than re-parsing them
// with a reader this repository also wrote.
//
// `event:` and `id:` are **not** prefixed, and that is not an oversight: they are
// standard event-stream fields with standard meanings, Datastar's parser handles
// both by name, and the event name is what selects the watcher. Prefixing them
// would put Datastar's attributes where the grammar's own fields belong.
const dataPrefix = "data: "

// Handler serves the event stream.
//
// Two fields and no state: the hub is the broker, the logger is optional, and
// everything a connection needs is either a local variable in `stream` or read from
// the hub. A handler with per-connection state would need it to be per-goroutine,
// and a `Handler` is shared by every request.
type Handler struct {
	// Hub is the broker. Required: a route with no hub has nothing to subscribe to,
	// and answering that with an empty stream would be a connection that looks
	// healthy and never fires.
	Hub *Hub

	// Logger receives this route's lines. Nil is allowed and discards them, so a
	// test does not have to construct a logger to open a stream.
	Logger *slog.Logger

	// Chrome renders the sidebar fragments a `Kind` change patches in, or nil for a
	// stream that carries only the editor's notice.
	//
	// **A function and not a field**, and that is the load-bearing shape: what a
	// sidebar change renders is a function of the campaign's live state *and* of
	// which reader this connection is — a player's chat and a GM's differ, because
	// the decision to omit a secret line happens before any view model exists. This
	// package therefore holds no gameplay and no state knowledge, exactly as
	// `internal/httpapi/play`'s own header says of itself: it is the transport, the
	// authorisation boundary and the queue discipline.
	//
	// Nil is the editor-only configuration and is what phase 7 shipped, so a build
	// that wires nothing new keeps working and a build that does gets fragments.
	Chrome ChromeRenderer

	// KeepAlive overrides `keepAliveAfter`. Zero means the shipped value.
	//
	// **A field and not a knob**, for the reason `internal/httpapi/play` gives for
	// its two: it is not configuration, it is the one duration a test has to be able
	// to make fire inside a test's lifetime. Interleaving keep-alives with
	// insertions is one of §7.5's four prohibitions' evidence, and a 15-second
	// keep-alive would put that test at half a minute of wall clock. Nothing reads
	// it but this package and its tests — no environment variable, no flag.
	KeepAlive time.Duration
}

// Mount registers the stream on mux, behind the play gate.
//
// **The gate is the play gate, and that is a change from phase 7's edit gate** —
// and the reason it had to change is that UI §7.5's resolution table puts two
// different payloads on this one URL. The stream carries the editor's
// external-change notice *and* the play surface's sidebar fragments, and a player
// needs the second and must not have the first. One path cannot sit behind two
// gates, so the gate is the lower one and the *payload* is filtered by tier inside
// the handler: the editor's notice carries a page path, and a page path is the
// campaign's own directory structure, so it is emitted only for a reader whose tier
// can edit. `TestAPlayerIsServedTheSidebarAndNeverTheEditorsPagePaths` is what
// holds that, and it is the same line the wiki route draws for redaction.
//
// This is not ADR 0024 being bent. That record says authorisation is a gate a route
// mounts rather than a check inside a handler, and it is honoured: the gate decides
// whether this reader may see the campaign's **table** at all, before the handler
// runs and without consulting the handler. Filtering *which fragments* an entitled
// reader is told about is the same job `internal/httpapi/wiki` does with secrets,
// and it is a different question from whether the reader may look.
//
// One method, `GET`, and the reason is the same as the editor's: an `EventSource`
// is a `GET`, and a `POST` to this URL is a method that exists nowhere in the
// design, so `net/http`'s 405 is the honest answer.
//
// The campaign's id is read from the context rather than from the URL, for the same
// reason the editor reads it there: the gate resolved it, and reading the slug again
// would be a second lookup that could answer a different campaign if the row had
// changed in between.
func Mount(mux *http.ServeMux, handler *Handler) {
	mux.Handle("GET /c/{slug}/events", campaigns.RequirePlay(handler))
}

// ServeHTTP opens the stream and holds it until the connection or the hub ends it.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := campaigns.AccessFrom(ctx)

	if h.Hub == nil {
		// A wiring fault, and a 500 rather than a silent empty stream: a client
		// connected, received 200 and a comment, and would wait for a notice that
		// could never arrive.
		http.Error(w, "event stream unavailable", http.StatusInternalServerError)

		return
	}

	// Headers before anything is written, and the deadline cleared before the
	// first byte. Both orders are load-bearing and both are easy to get wrong: a
	// header set after `WriteHeader` is a header the client never sees, and a
	// deadline cleared after the first write is a deadline the first write already
	// obeyed.
	header := w.Header()
	header.Set("Content-Type", eventStreamType)
	// `no-store`, and not `private, no-cache`: this response is never a cacheable
	// representation of anything, and a cache that held one would hold a stream.
	header.Set("Cache-Control", "no-store")
	// Says "no" to a proxy, and says it in the header proxies look for. A reverse
	// proxy that buffers this response produces a route that appears to work — the
	// 200 and the comment arrive — and then delivers nothing, ever.
	header.Set(noBuffering, "no")

	clearWriteDeadline(ctx, w)

	// Subscribed after the headers and before the flush, so that a change arriving
	// between the flush and the subscription cannot be missed. The opposite order
	// has a window, and the window is exactly the window in which a GM's own save
	// lands — which is the change they most want to be told about.
	subscription := h.Hub.Subscribe(access.Campaign.ID)
	defer subscription.Close()

	w.WriteHeader(http.StatusOK)

	// The opening comment, and the first flush. The flush is what commits the
	// response to being a stream (see the file comment); without it the whole
	// response would sit in the timeout middleware's buffer until this handler
	// returned, which is never.
	if !h.comment(w, "connected") {
		return
	}

	if !h.flush(ctx, w) {
		return
	}

	h.log(ctx, slog.LevelDebug, "events.stream_opened",
		slog.String("campaign", access.Campaign.Slug),
		slog.Int("subscribers", h.Hub.Stats().Subscribers),
	)

	h.stream(ctx, w, access.Campaign.Slug, access, subscription)
}

// keepAliveInterval is this handler's keep-alive, defaulted.
//
// A method and not a second constant so that a test can shorten it: see `KeepAlive`'s
// own comment, which is the argument for the field existing at all.
func (h *Handler) keepAliveInterval() time.Duration {
	if h.KeepAlive > 0 {
		return h.KeepAlive
	}

	return keepAliveAfter
}

// stream is the connection's loop: changes in, one insertion a second out.
//
// ## The request context is not the loop's lifetime
//
// The loop selects on the subscription, on the notice ticker and on the keep-alive
// ticker, and *not* on `r.Context().Done()`. That is deliberate, and it is the one
// place in this route where the usual rule is suspended.
//
// `middleware.Timeout` cancels the request context at the handler budget — 25
// seconds by default — because for every other route that is the point: a handler
// that has not returned in 25 seconds is stuck. A stream *is* a handler that has not
// returned and is not stuck, so the context's cancellation and "the client went
// away" are indistinguishable from inside the handler, and selecting on it would cut
// every stream at the budget and leave the client reconnecting forever.
//
// So the two conditions are separated by using two different signals: the hub's
// close ends a stream deliberately, and a failed write ends it because the socket
// is gone. The cost is stated rather than hidden — a stream to a client that has
// gone away without the socket noticing is held until the next keep-alive write
// fails, which is at most `keepAliveAfter`. The alternative, a context-aware select,
// trades that bounded delay for an unbounded one: the stream ends at 25 seconds and
// the client reconnects, which is a reconnect loop rather than a stream.
//
// This is a property of the *chain*, not of this file, and phase 7's WebSocket
// upgrade needs the same two decisions for the same two reasons — and takes them,
// in `internal/httpapi/play`.
//
// ## One ticker, one budget, and both kinds of change
//
// There is **one** `noticeInterval` ticker and it gates every insertion, whatever
// produced it. That is §7.5's rule and §7.10's prohibition read together: "a live
// region firing more than once per second" is a property of the *region*, not of the
// event that happened to fill it, so a chat line and a page edit compete for the
// same budget rather than each getting their own.
//
// Two slots, because two delivery policies meet here. `pending` is the coalescing
// slot — the newest replaces what was waiting, because three page edits in a second
// are one fact ("the disk moved") and §7.5's table is explicit that a reader is told
// the fact rather than the count. `queued` is the FIFO slot for chat and dice lines,
// because three messages in a second are three messages and coalescing them would
// leave the reader with a transcript with holes in it. The hub makes the same split
// for the same reason; see `noticeBuffer` and `lineBuffer`.
//
// **The coalescing slot is drained first**, and the order is §7.5's own row order
// rather than an arbitrary preference: a turn change and a chat line in the same
// second produce the turn change now and the line a second later, because the turn
// is the fact the reader was waiting on and the line is a thing they will still get.
func (h *Handler) stream(
	ctx context.Context,
	w http.ResponseWriter,
	slug string,
	access campaigns.Access,
	subscription *Subscription,
) {
	notices := time.NewTicker(noticeInterval)
	defer notices.Stop()

	keepAlive := time.NewTicker(h.keepAliveInterval())
	defer keepAlive.Stop()

	// The event id, monotonic per connection. Datastar uses it to resume, and it is
	// the only way a client can tell "I missed nothing" from "I missed something":
	// an event with no id cannot be replayed, and a stream that silently drops a
	// notice looks identical to a stream that had nothing to say.
	var (
		eventID int64
		// pending is the coalescing change waiting for the next tick. Nil when there
		// is nothing to say, and holding it rather than writing immediately is what
		// makes the interval a floor rather than a target.
		pending *Notice
		// queued are the chat and dice lines waiting behind the coalescing slot. The
		// hub already holds a queue in `Subscription.Lines`; this one is the drain, and
		// it exists because the loop must choose *one* change per tick and the choice
		// is a policy rather than a channel receive.
		queued []Notice
	)

	for {
		select {
		case notice, open := <-subscription.Notices():
			if !open {
				h.ended(ctx, slug)

				return
			}

			// Replaced rather than queued, and the hub's buffer of one is why: a burst
			// of five page edits in a second becomes one patch, which is §7.5's throttle
			// arriving by the other road and is why a reader is told the disk moved
			// rather than how many times it moved. `Kind.Queues` kinds never arrive
			// here - the hub routes those to `Lines` - so this slot cannot swallow a
			// chat line.
			current := notice
			pending = &current

		case notice, open := <-subscription.Lines():
			if !open {
				h.ended(ctx, slug)

				return
			}

			// A queued kind behind a pending coalescing one. It waits its turn rather
			// than replacing it, because a chat line is an event that happened and the
			// coalescing slot holds the newest statement of a fact about the present.
			queued = append(queued, notice)

		case <-notices.C:
			next, ok := nextChange(&pending, &queued)
			if !ok {
				continue
			}

			if !h.patch(ctx, w, slug, access, next, eventID) {
				return
			}

			// The id advances even when the patch was refused. An event id has to be
			// monotonic or a resuming client's `last-event-id` means nothing, and a
			// refused patch is an event that was sent and consumed.
			eventID++

		case <-keepAlive.C:
			// **The keep-alive is a comment and the throttle does not apply to it** -
			// see the file header. It writes no fields, inserts nothing, and is
			// invisible to the announcement machinery: this branch touches neither
			// `pending` nor `queued`, and the test that proves it interleaves a
			// keep-alive every few milliseconds and measures the gap between two
			// announced insertions.
			if !h.comment(w, "keep-alive") || !h.flush(ctx, w) {
				return
			}
		}
	}
}

// ended records that the hub closed and the stream is finishing.
//
// One line rather than an inline `slog.DebugContext`, because it is now reached
// from two branches - either channel closing means the hub is going away - and a
// log line written twice is a log line two places to keep in step.
func (h *Handler) ended(ctx context.Context, slug string) {
	// The hub closed: shutdown, or a test's teardown. The stream returns here, which
	// is the whole of the clean-shutdown story.
	h.log(ctx, slog.LevelDebug, "events.stream_closed",
		slog.String("campaign", slug),
	)
}

// nextChange takes the change this tick will emit, and removes it from whichever
// slot held it.
//
// The coalescing slot first, and `pending` is cleared before the patch is written
// rather than after: a change that arrives while a slow write is in progress must
// not be swallowed by the write it was waiting behind, because the reader's own
// next change is the one they are waiting for.
func nextChange(pending **Notice, queued *[]Notice) (Notice, bool) {
	if *pending != nil {
		next := **pending
		*pending = nil

		return next, true
	}

	if len(*queued) == 0 {
		return Notice{}, false
	}

	next := (*queued)[0]
	*queued = (*queued)[1:]

	return next, true
}

// patch writes one change as a Datastar element patch, and reports whether the
// connection is still usable.
//
// Two egress representations meet here and the choice between them is one switch on
// the change's `Kind`: a `KindChange` is the editor's external-change notice, and
// every other kind is a sidebar fragment rendered by `Chrome`. Both go out as a
// rendered DOM fragment — §7.5 forbids a client renderer on this route — and the
// only difference is which component produced the markup and which selector it
// names.
//
// Every fragment is offered to `live.Decide` before it is written, so §7.5's "a
// fragment cannot be rendered into the wrong region" and "patches must never touch
// the focused element" are decisions this process makes about what it emits rather
// than properties a client is trusted to have. A refusal is logged with its reason
// and the connection continues: the next change may be fine, and dropping a GM's
// stream over one bad fragment turns a bug into an outage.
func (h *Handler) patch(
	ctx context.Context,
	w http.ResponseWriter,
	slug string,
	access campaigns.Access,
	notice Notice,
	eventID int64,
) bool {
	fragment, ok := h.fragment(ctx, slug, access, notice)
	if !ok {
		return true
	}

	// One debug line per insertion, and it is what makes `Kind` observable to an
	// operator at all: a sidebar that updates silently and a sidebar that has stopped
	// are the same observation from every angle except this one.
	h.log(ctx, slog.LevelDebug, "events.fragment_sent",
		slog.String("campaign", slug),
		slog.String("kind", notice.Kind.String()),
		slog.String("target", fragment.Target.Name),
	)

	selector, mode := patchTargetFor(fragment)

	var frame strings.Builder

	frame.WriteString("event: ")
	frame.WriteString(patchEvent)
	frame.WriteByte('\n')

	frame.WriteString("id: ")
	frame.WriteString(strconv.FormatInt(eventID, 10))
	frame.WriteByte('\n')

	frame.WriteString(dataPrefix)
	frame.WriteString("selector ")
	frame.WriteString(selector)
	frame.WriteByte('\n')

	frame.WriteString(dataPrefix)
	frame.WriteString("mode ")
	frame.WriteString(mode)
	frame.WriteByte('\n')

	// One `elements` line per line of the fragment, and *not* one line with
	// embedded newlines: the event-stream grammar terminates a field at the first
	// newline, so a multi-line fragment in a single field is a truncated patch. The
	// repeated field is Datastar's own encoding for a multi-line value — its parser
	// joins every `data:` payload with a newline before splitting the result into
	// `name value` pairs — and the client rejoins them.
	//
	// **Measured**, against the vendored module in a real browser: three `data:
	// elements` lines carrying three sibling `<li>`s arrive as three siblings, and
	// the same three lines without the prefix arrive as nothing at all.
	for line := range strings.SplitSeq(fragment.Markup, "\n") {
		frame.WriteString(dataPrefix)
		frame.WriteString("elements ")
		frame.WriteString(line)
		frame.WriteByte('\n')
	}

	frame.WriteByte('\n')

	if _, err := io.WriteString(w, frame.String()); err != nil {
		h.log(ctx, slog.LevelDebug, "events.write_failed",
			slog.String("campaign", slug),
		)

		return false
	}

	return h.flush(ctx, w)
}

// patchTargetFor is the selector and mode a fragment goes out with.
//
// For a sidebar fragment, the two come from the target `live.Targets` declares —
// which is what makes "a fragment cannot be rendered into the wrong region" a
// property of the type rather than of a call site. For the editor's notice, they
// are this package's own two constants, and the notice is not one of `live`'s
// targets because it is not the play surface's: it is a GM's right rail, rendered by
// `internal/web/components/edit`, and it already carries its own `role="status"`.
//
// The editor's region is `RegionStatus` and its mode `ModeInner` in both cases, so
// it goes through the same `Decide` as everything else — the editor's notice holds a
// `<button>`, which is a focus stop, which means `Decide` refuses it. **That is
// correct and it is why the refusal is scoped**: see `fragment`.
func patchTargetFor(fragment live.Fragment) (string, string) {
	if fragment.Target.Name == "" {
		return noticeSelector, patchModeInner
	}

	return fragment.Target.Selector, fragment.Target.Mode
}

// renderNotice renders the notice fragment for one change.
//
// The context is the request's **minus its cancellation**, and that is not a
// convenience: `templ`'s generated `Render` checks `ctx.Err()` before every element
// and returns the error, and the request's context is cancelled at the handler budget
// (see `stream`). So a render that used `ctx` directly would succeed for the first
// 25 seconds of a stream and fail on every notice after that — a stream that goes
// permanently silent, logging one error per change, and the only symptom a GM sees
// is an editor that stops telling them about their own files.
//
// `context.WithoutCancel` is the right tool rather than `context.Background()`: it
// keeps the request's *values* — so a logger reached from inside the render still
// correlates the line with the request that opened the stream — and drops only the
// deadline and the cancellation. The fragment needs nothing from the request but the
// slug, which arrives as a parameter for exactly this reason.
func renderNotice(ctx context.Context, slug string, notice Notice) (string, error) {
	view := edit.ChangeNoticeView{
		Page: pageName(notice.Path),
		Path: notice.Path,
		Op:   notice.Op,
	}

	// A removal has nothing to review: the page is gone, and a Review action that
	// led to a 404 would be an affordance that reports a fault rather than a fact.
	// The button is absent rather than disabled, for UI §4.5's reason — a control a
	// viewer cannot use is not in the document.
	if notice.Op != content.OpRemove {
		view.ReviewHref = editHref(slug, notice.Path)
	}

	var fragment strings.Builder

	if err := edit.ChangeNotice(view).Render(context.WithoutCancel(ctx), &fragment); err != nil {
		return "", fmt.Errorf("render change notice: %w", err)
	}

	return fragment.String(), nil
}

// comment writes an SSE comment, which no client acts on and every intermediary
// counts as traffic.
func (h *Handler) comment(w http.ResponseWriter, text string) bool {
	if _, err := io.WriteString(w, ": "+text+"\n\n"); err != nil {
		return false
	}

	return true
}

// flush pushes what has been written, and reports whether the connection is still
// usable.
//
// ## It flushes the whole wrapper chain, and that is not belt and braces
//
// `middleware.Timeout` buffers a response body so it can substitute a 504, and its
// `Flush` is the one-shot commit that stops the buffering: the first flush writes the
// buffered body into the writer *beneath* it and marks itself committed. But the
// writer beneath it is `*http.response`, which has a buffer of its own — so a single
// `Flush` through the chain puts the bytes one level too high, and they do not reach
// the socket until something else flushes again. A stream that flushed once and then
// waited would therefore deliver its opening comment only at the *next* write: with
// a 15-second keep-alive, a client sees nothing for fifteen seconds after connecting,
// and every test of the first notice fails for a reason that looks like a bug in the
// hub.
//
// The fix is to flush each layer in turn, following `Unwrap` — the interface
// `net/http` defines for exactly this, and which `middleware` implements on both its
// writers *specifically* so that an outer layer can reach the socket. A chain of two
// layers is two flushes; a chain of five is five, and the loop stops when the writer
// no longer unwraps.
//
// `http.ResponseController` is the tidier spelling and is **not** usable here: it
// unwraps only until it finds something that can flush, and the timeout layer is
// exactly that, so it stops one level short for the same reason.
//
// A flush error is also the only reliable signal that a browser is gone, for the
// reason `stream` sets out: the request context is not a disconnect detector on a
// committed stream. So a failure ends the connection rather than being retried.
func (h *Handler) flush(ctx context.Context, w http.ResponseWriter) bool {
	for current := w; ; {
		flusher, canFlush := current.(http.Flusher)
		if !canFlush {
			// A `ResponseWriter` with no flusher cannot stream, and pretending
			// otherwise would produce a response that looks like a stream and is
			// not: the bytes would sit in a buffer until the handler returned, which
			// is never. This is a wiring fault in whatever wrapped the writer, and it
			// is reported rather than swallowed because there is nowhere else it
			// would show up.
			h.log(ctx, slog.LevelError, "events.response_not_flusher")

			return false
		}

		flusher.Flush()

		unwrapper, hasParent := current.(interface{ Unwrap() http.ResponseWriter })
		if !hasParent {
			return true
		}

		current = unwrapper.Unwrap()
	}
}

// clearWriteDeadline removes the server's write deadline from this response.
//
// `http.Server.WriteTimeout` bounds how long a response body may take, which is the
// right default for a page and fatal for a stream: with a 30-second write timeout
// and a 25-second handler budget, a stream that did not clear the deadline would
// live for 25 seconds and then be closed by whichever limit bit first — and the
// client would reconnect, and be closed again, forever.
//
// `http.ResponseController` reaches the real writer through each wrapper's
// `Unwrap`, and `middleware` implements `Unwrap` on both of its writers for
// exactly this kind of case. An error is tolerated and reported rather than
// treated as fatal: a `ResponseWriter` that does not support deadlines (a test
// recorder, an HTTP/1.0 peer) is not a stream that cannot work, and refusing to
// serve because of it would make the route untestable and would not help a real
// deployment.
func clearWriteDeadline(ctx context.Context, w http.ResponseWriter) {
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		slog.DebugContext(ctx, "events.write_deadline_not_cleared",
			slog.String("error", err.Error()))
	}
}

// editHref builds the editor URL for a page in a campaign.
//
// Per-segment escaping, and the reason is `content.WikiHref`'s in full: escaping
// `notes/Some Page` in one call produces `notes%2FSome%20Page`, a single segment
// where the URL scheme expects several — a well-formed URL that no route matches,
// which reads like it worked. Each segment is escaped on its own and a `..` is
// written as `%2E%2E` even though the page path has been confined by `os.Root` long
// before here (S-3.5), because this function is reachable from an event that
// originated on disk and the browser is the last place a `..` must not be
// normalised away.
//
// Spelled here rather than added to `content` because `internal/content` is not this
// work item's to edit, and the alternative — a `href` of
// `content.WikiHref` with `/wiki/` replaced by `/edit/` — is string surgery on a
// URL, which is how a scheme ends up with two spellings.
func editHref(slug, rel string) string {
	if slug == "" || rel == "" {
		return ""
	}

	var href strings.Builder

	href.WriteString("/c/")
	href.WriteString(url.PathEscape(slug))
	href.WriteString("/edit")

	for segment := range strings.SplitSeq(rel, "/") {
		href.WriteByte('/')

		if segment == ".." {
			href.WriteString("%2E%2E")

			continue
		}

		href.WriteString(url.PathEscape(segment))
	}

	return href.String()
}

// pageName is what a changed page is called in the notice.
//
// The base name without the extension, for the reason `wiki/handler.go` strips it:
// one page has one name everywhere it is referred to, and a notice that said
// "Vault.md" while the editor's heading said "Vault" gives a GM two names for one
// page in two places thirty centimetres apart.
func pageName(pagePath string) string {
	return strings.TrimSuffix(path.Base(pagePath), pageExtension)
}

// pageExtension is the suffix stripped from a page's name.
//
// Spelled here rather than imported because `content`'s own is unexported and owned
// by the link layer's grammar — the same reason the editor route spells its own.
const pageExtension = ".md"

// log writes one line, discarding it when no logger is configured.
func (h *Handler) log(ctx context.Context, level slog.Level, event string, attrs ...slog.Attr) {
	if h.Logger == nil {
		return
	}

	h.Logger.LogAttrs(ctx, level, "events",
		append([]slog.Attr{slog.String("event", event)}, attrs...)...)
}
