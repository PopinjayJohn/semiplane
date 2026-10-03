// Package events serves `GET /c/{slug}/events`: the editor's external-change
// notice, over server-sent events.
//
// Three properties decide this package's shape, and each of them is a rule rather
// than a preference:
//
//   - **It carries notices, never content.** UI §7.5 confines content delivery to
//     ordinary HTTP with an `ETag` (ADR 0014), and a notice that said "here is
//     the page now" would be a second, uncacheable, unvalidatable copy of the file
//     on a connection with no `If-Match` and therefore no conflict detection. What
//     crosses this connection is a *fragment of DOM* saying the page moved; the
//     bytes are fetched over HTTP by the editor, under a precondition, exactly as
//     if no stream existed.
//   - **It patches DOM, it does not feed a renderer.** §7.5: "rendered DOM
//     fragments … patched by Datastar". The payload is markup the server rendered,
//     so a client cannot disagree with the server about what an external change is
//     called or what a Review action points at.
//   - **It is throttled and it is not allowed to steal focus.** §7.5, twice over:
//     "patches must never touch the focused element" and "insertions into a live
//     region are throttled to 1/second". Both are properties of *this* code rather
//     than of the client's, and both are asserted against the served markup and the
//     bytes on the wire.
//
// # The hub holds no goroutines
//
// A broker for a process-local stream could be written three ways — one goroutine
// per subscriber, one shared fan-out goroutine, or none at all — and the choice is
// forced by what the publisher is. The publisher is the content watcher's sink,
// and `content.ChangeSink`'s contract says a sink "must not block indefinitely":
// a hub that could block would stop a campaign's indexer behind a browser tab
// somebody left open. So:
//
//   - `Hub` is a mutex, a map and a set of buffered channels. No goroutine, no
//     queue, no goroutine-per-subscriber: the handler that owns a subscription runs
//     on the request's own goroutine, which `net/http` already accounts for and
//     which ends when the handler returns.
//   - Every send is non-blocking, and a subscriber that cannot keep up has its
//     *stale* notice replaced by the newer one rather than being waited for. A
//     notice says "this page moved"; two of them in the same second are one fact.
//   - `Close` closes every subscription, which is what ends every stream. There is
//     no other teardown, and no per-connection timer that has to notice anything.
package events

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/semiplane/semiplane/internal/content"
)

// Notice is one settled change, as the stream carries it.
//
// A projection of `content.Change` rather than the change itself, and the reason
// is the campaign filter: the hub is process-wide, so a subscriber is a campaign
// id and a notice must be able to say which campaign it belongs to without the
// subscriber re-deriving it. `Slug` is carried too, for the same reason
// `content.Change` carries it — a log line, never an authorisation input.
//
// Four fields, and none of them is content. A path is a name and a kind is a
// label; anything else would be content, and S-12.3 forbids an event carrying
// any. In particular there is **no field a chat body or a dice result could be
// passed through**, which is the whole enforcement behind that rule — the type
// is the gate, and a `slog.Any("detail", roll)` has nowhere to go.
type Notice struct {
	// CampaignID is the campaign whose root the path is relative to. The filter
	// every subscription is keyed on, and the reason a hub shared by ten campaigns
	// can be one object.
	CampaignID int64
	// Path is the page's path after the change, root-relative to that campaign's
	// content root. Not the absolute path and never a directory: the vault's layout
	// on the host is not something a browser is told.
	//
	// **Empty for every `Kind` but `KindChange`**, and that emptiness is the shape
	// of the two-egress decision: the sidebar's fragments carry no path at all,
	// because a path is the editor's business and the sidebar's announcements are
	// about the table.
	Path string
	// Op is what happened, and the copy the notice states depends on it. It is a
	// `content.Op` and only meaningful for `KindChange`.
	Op content.Op
	// Kind is which of the two egress representations this change is for. Zero is
	// `KindChange`, the filesystem notice phase 7 shipped, so every existing
	// publisher and every existing test is unchanged by this field's arrival.
	Kind Kind
}

// noticeBuffer is how many undelivered coalescing notices one subscriber may
// hold.
//
// One, and that number is the policy rather than a tuning choice. A notice is a
// hint that the disk moved; the newest supersedes the oldest because both are true
// and only the newest names a page the GM is likely looking at. A larger buffer
// would let a burst of changes replay into a live region one per second — which is
// §7.5's throttling arriving by a different road and is the failure the throttle
// exists to prevent.
//
// **It counts coalescing kinds only.** A chat line and a roll are not hints: they
// are the table's content, and a reader who saw three of four messages has a
// transcript with a hole in it. Those queue instead — see `lineBuffer` — and the
// split is the hub's whole contribution to UI §7.5's replay rule.
const noticeBuffer = 1

// lineBuffer is how many chat and dice lines one subscriber may fall behind by.
//
// Sixty-four, and it is a floor on the reader's backlog rather than a promise to
// keep it: a table that has run for six hours produces far more than this and a
// browser tab that has been suspended produces them in a burst. When the queue is
// full the **oldest** line goes, not the newest, because the reader's next line is
// the one they are waiting for and the oldest is the one most likely to have been
// read already.
//
// The cost is stated rather than hidden: a line dropped here is a line the reader
// never heard, and the only trace is `Stats.Dropped`. That is why the counter is
// hub-wide and reported in the route's log line rather than being per-subscriber:
// a dropped line is an operator-visible fact, not a per-connection metric.
const lineBuffer = 64

// Hub is the process's broker between the content watcher and the browsers
// watching a campaign's editor.
//
// A value the composition root constructs and passes to both sides — to
// `content.NewWatcher` as a `ChangeSink` and to the route as a dependency. Not a
// package global and not something an `init()` fills in, for the reason
// `content.Registry` is not: a global is a second source of truth about who is
// connected, it is shared by every test in the process, and there is no ordering
// story that makes it right.
//
// Safe for concurrent use. The mutex is held only for map and channel operations,
// never across I/O, so one slow browser cannot hold up the watcher.
type Hub struct {
	mu     sync.Mutex
	subs   map[int64]*Subscription
	nextID int64
	closed bool

	// published and dropped are the counters the route logs and the tests read.
	//
	// `dropped` is the interesting one: a rising count is a subscriber that cannot
	// keep up, which is a client problem, and it is *visible* rather than silent
	// because a hub that quietly discards notices is indistinguishable from a
	// watcher that stopped (the failure mode `watch.add_failed` exists to catch).
	published atomic.Int64
	dropped   atomic.Int64
	delivered atomic.Int64

	// rejected counts sidebar publishes refused for carrying the wrong `Kind`.
	//
	// **A counter rather than a log line, because the caller is in-process code and
	// a log line from the hub would put an event name the route did not choose into
	// the operator's stream.** One per mistake, so it is a loud enough signal that
	// nobody looks at it twice — which is the intent.
	rejected atomic.Int64
}

// NewHub returns an empty hub.
//
// No arguments, and that is the whole of its configuration: there is no buffer size
// to tune (see `noticeBuffer`), no queue depth, and no per-campaign registration
// step, because a campaign with no subscribers costs one map lookup per change.
func NewHub() *Hub {
	return &Hub{subs: make(map[int64]*Subscription)}
}

// Publish delivers a change to every subscriber of its campaign.
//
// Non-blocking by construction, and that is a contract with the watcher rather
// than an implementation detail: `content.ChangeSink` says a sink must not block
// indefinitely, and a hub whose send could block would put a browser tab between an
// Obsidian edit and a campaign's search index.
//
// A send that cannot proceed replaces the subscriber's undelivered notice with this
// one. Two notices for the same second are one fact, and the newest names the page
// the GM is most likely to be looking at.
//
// **`KindChange` and nothing else**, so the only publisher phase 7 wired — the
// content watcher's sink — cannot accidentally reach a sidebar path. The sidebar's
// changes go through `PublishSidebar`, whose argument is a `Kind` and a `Notice`
// and therefore cannot name a page path at all.
func (h *Hub) Publish(change content.Change) {
	h.published.Add(1)

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	notice := Notice{CampaignID: change.CampaignID, Path: change.Path, Op: change.Op}

	for _, subscription := range h.subs {
		if subscription.campaignID != change.CampaignID {
			continue
		}

		h.deliver(subscription, notice)
	}
}

// PublishSidebar delivers one sidebar change to every subscriber of its campaign.
//
// The second egress's entry point, and the split from `Publish` is the reason the
// hub is the right place for it: a chat line and a page edit are different kinds
// of thing with different delivery semantics (see `noticeBuffer` and
// `lineBuffer`), and a publisher that chose its own would have to know the
// subscriber's buffer policy — which is the hub's, because the hub owns the
// buffers.
//
// `KindChange` is **refused** rather than delivered, and that refusal is the point
// of a separate method: it is the one value for which coalescing and queueing are
// both wrong, and a caller reaching for this method with a filesystem change has
// made a mistake a counter should record rather than a slot should fill.
func (h *Hub) PublishSidebar(notice Notice) {
	if notice.Kind == KindChange {
		h.rejected.Add(1)

		return
	}

	h.published.Add(1)

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	for _, subscription := range h.subs {
		if subscription.campaignID != notice.CampaignID {
			continue
		}

		h.deliverSidebar(subscription, notice)
	}
}

// Sink returns the hub as a `content.ChangeSink`, for `content.NewWatcher`.
//
// A method rather than asking the composition root to write the closure, so the
// conversion is in the package that knows the hub and a reader of the composition
// root sees one value where two are meant. The context is accepted and ignored: a
// delivery that can block would make the context meaningful, and the whole point is
// that it cannot.
func (h *Hub) Sink() content.ChangeSink {
	return func(_ context.Context, change content.Change) {
		h.Publish(change)
	}
}

// Subscribe returns a subscription to one campaign's notices.
//
// The caller owns it and must `Close` it; the route does that in a defer. A
// subscription for a closed hub is returned already closed rather than nil or an
// error, so a handler that starts serving after shutdown ends its response
// immediately instead of having to know whether the hub was running.
func (h *Hub) Subscribe(campaignID int64) *Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.nextID++

	subscription := &Subscription{
		id:         h.nextID,
		campaignID: campaignID,
		hub:        h,
		notices:    make(chan Notice, noticeBuffer),
		lines:      make(chan Notice, lineBuffer),
	}

	if h.closed {
		subscription.closeChannels()

		return subscription
	}

	h.subs[subscription.id] = subscription

	return subscription
}

// Close ends every subscription and refuses new ones.
//
// The whole of the teardown story, and it exists because the alternative is a
// stream that outlives the process: a handler blocked on a channel nobody will ever
// write to is a goroutine and a socket held until the operating machine reboots.
// After `Close`, `Publish` does nothing, `Subscribe` returns a closed subscription,
// and every stream ends at its next read.
//
// A second call is harmless, and every subscriber is attempted even when one
// removal fails, because stopping at the first failure leaves the rest open for no
// benefit at all.
func (h *Hub) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil
	}

	h.closed = true

	for id, subscription := range h.subs {
		// Closed here, under the lock this function already holds, rather than
		// through `Subscription.Close` — which would take the same lock again and
		// deadlock, because `sync.Mutex` is not reentrant. The `closeOnce` still
		// guards it, so a subscriber that closes itself concurrently closes once.
		subscription.closeChannels()

		delete(h.subs, id)
	}

	return nil
}

// Stats is the hub's counters, for a log line and for a test.
//
// A struct rather than three return values because the three are read together and
// `dropped` is only meaningful next to `published`: a subscriber that has dropped
// nothing out of a hundred thousand published notices is not a problem, and the
// ratio is the fact.
type Stats struct {
	// Published is how many changes the hub has been handed.
	Published int64
	// Delivered is how many notices reached a subscriber's channel.
	Delivered int64
	// Dropped is how many were replaced by a newer one because the subscriber was
	// not keeping up.
	Dropped int64
	// Subscribers is how many are open now.
	Subscribers int
}

// Stats returns the hub's counters.
//
// The subscriber count is read under the lock and the counters are not, which is
// correct: the counters are monotonic and a reader that got a slightly stale
// `Dropped` has a log line that is a moment behind, whereas a subscriber count read
// without the lock could be read while the map is being written.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	subscribers := len(h.subs)
	h.mu.Unlock()

	return Stats{
		Published:   h.published.Load(),
		Delivered:   h.delivered.Load(),
		Dropped:     h.dropped.Load(),
		Subscribers: subscribers,
	}
}

// Subscription is one stream's view of one campaign's notices.
//
// Two channels and the split is the hub's contribution to §7.5's replay rule; see
// `noticeBuffer` and `lineBuffer`.
type Subscription struct {
	id         int64
	campaignID int64
	hub        *Hub
	notices    chan Notice
	lines      chan Notice

	// closeOnce guards the channel close, which happens from two directions: the
	// subscriber giving up, and the hub shutting down. A double `close` of a
	// channel panics, and a panic on a shutdown path is the one failure a
	// graceful stop cannot survive.
	closeOnce sync.Once
}

// Notices is the coalescing channel the stream reads.
//
// Closed when the subscription is closed or the hub is, and receiving from a
// closed channel yields `false` — which is the stream's signal to end. The channel
// is exported as receive-only because a send would be a writer pretending to be a
// reader, and the hub is the only writer.
//
// **Closed only together with `Lines`.** A publisher that could see one closed and
// the other open would send on a closed channel and panic in the watcher goroutine,
// where nothing recovers it; closing both under the hub's lock is what makes the
// pair atomic from every sender's point of view.
func (s *Subscription) Notices() <-chan Notice {
	return s.notices
}

// Lines is the queueing channel: chat lines and dice rolls, in order.
//
// Closed on the same conditions as `Notices` and for the same reason. A stream that
// reads both has to handle either closing first, which is why the route treats a
// closed channel as "the hub is going away" and returns.
func (s *Subscription) Lines() <-chan Notice {
	return s.lines
}

// CampaignID is the campaign this subscription is for.
func (s *Subscription) CampaignID() int64 {
	return s.campaignID
}

// Close ends the subscription and removes it from the hub.
//
// Called by the route in a defer, on every exit from the stream including the
// error exits. A subscriber that is not removed keeps receiving into a channel
// nobody reads, which fills it, which drops notices into a hub-wide counter that
// then reports a problem that is actually a leak.
func (s *Subscription) Close() {
	s.detach()
}

// detach drops the subscription from the hub and closes its channels.
//
// **The `closeOnce` is inside `remove`, under the hub's lock, and not wrapped around
// this call** — and the reason is a deadlock rather than a style preference.
// `sync.Once.Do` is not reentrant: a `Do` that fires another `Do` on the same `Once`
// waits for a lock the outer call is holding. An earlier shape had `detach` wrap
// `remove` in `closeOnce.Do` and `remove` close the channels through
// `closeOnce.Do`, so every `defer subscription.Close()` hung forever on the hub's
// mutex. One `Once`, entered from exactly one place, is the whole fix: both callers
// hold `h.mu` before entering it, so no two goroutines can be inside it at once and
// the second is a no-op.
func (s *Subscription) detach() {
	s.hub.remove(s)
}

// deliver puts a notice on one subscription's channel, replacing a stale one.
//
// Every send is a non-blocking `select`. Called with the hub's lock held, so two
// publishers cannot interleave their replacement of the same slot; the subscriber
// itself only ever receives, so it needs no lock and cannot deadlock against this.
//
// The two counters distinguish a put into an empty buffer from a replacement, and the
// distinction is the one an operator reads: `Delivered` rising on its own means
// subscribers are keeping up, and `Dropped` rising means a browser tab is not
// draining. Counting a replacement as a delivery would make a healthy hub look busy
// and a broken one look healthy.
func (h *Hub) deliver(subscription *Subscription, notice Notice) {
	select {
	case subscription.notices <- notice:
		h.delivered.Add(1)

		return
	default:
	}

	// Full. Take the stale one out so the newest can take its place, then try again.
	// The last `select`'s `default` is reachable — the subscriber may have drained
	// the slot in between — and reaching it means this notice is lost, which is the
	// documented policy rather than a failure.
	select {
	case <-subscription.notices:
		h.dropped.Add(1)
	default:
	}

	select {
	case subscription.notices <- notice:
	default:
	}
}

// remove is the hub's half of a subscription's close, and it holds the lock the
// publisher also holds so a send and a close cannot interleave — a send on a closed
// channel panics, and that panic would arrive in the watcher's goroutine, where
// nothing recovers it.
func (h *Hub) remove(subscription *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(h.subs, subscription.id)

	subscription.closeChannels()
}

// closeChannels closes both of a subscription's channels, once.
//
// **Every caller holds the hub's lock**, which is the invariant that makes this
// correct: it is why no two closers can be inside `closeOnce` at the same moment,
// and it is why a subscriber closing itself while the hub is shutting down cannot
// double-close and panic on the shutdown path — the one panic a graceful stop cannot
// survive. See `detach` for why the `Once` lives here and not one level out.
func (s *Subscription) closeChannels() {
	s.closeOnce.Do(func() {
		close(s.notices)
		close(s.lines)
	})
}

// deliverSidebar puts a sidebar change on one subscription's channel, choosing the
// channel from the kind's delivery policy.
//
// Every send is a non-blocking `select`, for the reason `deliver` gives: the watcher
// must not be able to block behind a browser tab. The two failure policies differ,
// and the difference is the interesting part:
//
//   - a **queuing** kind (chat, dice) that cannot proceed **drops itself**. The
//     oldest is more valuable than the newest of the same kind, because the reader's
//     next line is the one they are waiting for;
//   - a **coalescing** kind that cannot proceed **replaces** what is waiting, for
//     the reason `deliver` gives.
//
// Both count into `Dropped`, because a rising count means a client problem either
// way and `Stats` reports it against `Published` so the ratio is the fact.
func (h *Hub) deliverSidebar(subscription *Subscription, notice Notice) {
	channel := subscription.notices

	if notice.Kind.Queues() {
		channel = subscription.lines
	}

	select {
	case channel <- notice:
		h.delivered.Add(1)

		return
	default:
	}

	if notice.Kind.Queues() {
		// Full queue: make room by dropping the oldest, then try once more. The
		// `default` on the second select is reachable - the stream may have drained
		// the queue in between - and reaching it means this line is lost, which is
		// the documented policy rather than a failure.
		select {
		case <-channel:
			h.dropped.Add(1)
		default:
		}

		select {
		case channel <- notice:
		default:
		}

		return
	}

	h.deliver(subscription, notice)
}
