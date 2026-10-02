// The hub: who is connected, who gets told, and what a connection costs when it
// stops reading.
//
// # Which of the plane's limits are here, and which are not
//
// `protocol.go` splits them in two and says so in its own header: the codec owns
// every limit that is a property of the bytes, and the hub owns the transport's
// read limit, the per-campaign connection count, the per-connection frame rate and
// the size of the state document that goes into a snapshot. This file is the
// connections half. It holds:
//
//   - `MaxTransportReadBytes`, which must exceed the codec's own bound — see its
//     comment, because the reason is not generosity.
//   - `MaxPeersPerCampaign`.
//   - `DefaultStaleAfter` and the sweep that applies it.
//
// The per-connection frame rate is the one of the four that is not here, and the
// reason is that it is a property of the **read loop**, which belongs to the route:
// the loop is the only place that sees a frame, and a hub that counted them would
// have to be told about each one by the goroutine that already counts them.
// Declaring it here without enforcing it would be the failure `AGENTS.md` names
// for the stylesheet import — a rule in one of two places is a rule one caller
// routes around.
//
// # There is no goroutine per connection, and that is the design rather than an
// omission
//
// A broadcaster can be written three ways — a goroutine per subscriber, a goroutine
// per campaign, or none — and the constraint picks one. The publisher is the state
// mutation path and the intent path, both of which run *inside* somebody's
// request: a resolver returning a dice roll, a watcher settling a page, a `PUT`
// that moved a token. A hub whose send could block would put a browser tab between
// an operation and the answer to it, so every send here is non-blocking, and a
// non-blocking send has nowhere to put a subscriber that is not reading.
//
// So there is no per-peer goroutine to be blocked. A peer's lifetime is:
//
//   - one buffered channel of size one for broadcasts (`broadcastBuffer`),
//   - one bounded channel for the frames that answer a `seq` (`MaxQueuedAnswers`),
//   - a mutex, a map, and two counters.
//
// The receiving side is the route's own read loop, on the goroutine `net/http`
// already gave the request. That goroutine ends when the socket ends and `net/http`
// already accounts for it, so an idle connection costs one channel and one map
// entry rather than a stack. `TestJoiningAConnDoesNotStartAGoroutine` asserts that
// by count, because it is the one property in this paragraph that no other
// observation can see.
//
// # Two queues per peer, and the reason they are not one
//
// A single slot per peer would be the obvious simplification and it is wrong.
// `delta`, `presence` and `clock` are *broadcasts*: two of them inside one second
// are one fact, and the newest is the one the reader wants, so the older is
// superseded. `applied` and `rejected` are *answers*: each resolves exactly one
// `seq`, and a client that never receives it rolls its optimistic update back and
// waits forever for a number that is not coming (ADR 0009, rule 1). One shared slot
// means a delta published microseconds after an `applied` eats the `applied`.
//
// So broadcasts are a replacing slot of one, and answers are a bounded queue that
// is never replaced. The bound is a ceiling on a client that is not reading its own
// replies, and reaching it closes the connection rather than dropping an answer:
// "fail toward closing" is the same rule `state.go` follows on a failed flush, and
// for the same reason — a silently dropped answer is a client that hangs with no
// way to find out why.
//
// # Why a stale peer is detected by a full queue rather than a write deadline
//
// A WebSocket write blocks when the peer's receive window is full, so "the write
// timed out" is a perfectly good detector and it is the wrong one here, for three
// reasons. It needs a context per write, which means the hub has to own the
// per-write deadline and every caller has to remember to set one — a limit one
// caller can forget is the stylesheet-import failure. It measures the socket, not
// the peer: a client behind a slow mobile link is a peer in good standing whose
// write is slow, and closing it because its link is slow is a policy nobody chose.
// And it cannot be observed without writing, which means a test for it needs a
// socket.
//
// A full outbound queue is the same fact read from this side. The hub already knows
// the queue is full, already knows how long it has been full, and can retire the
// peer with no I/O at all. The socket-side half of the same detection is the read
// deadline ADR 0010 requires, and it belongs to the route's loop; the hub's half is
// this one, and the two are complementary rather than redundant — the read deadline
// catches a peer whose TCP window is full and whose queue happens to be empty,
// because it is not reading either, and the queue catches a peer whose reader is
// alive and whose application is not draining.
//
// The detection is a **clock**, injected, for the reason `state.go`'s `Cadence` is:
// "this peer has been behind for more than `Stale`" is a claim about the absence of
// an event, and asserting absence in real time means sleeping past the window, which
// makes a test both slow and weaker at once.
//
// # Authorisation is not in this file, on purpose
//
// ADR 0024: authorisation is a gate the route *mounts*, never a check inside a
// handler. `Join` therefore takes an identity and a campaign that the route has
// already resolved — `campaigns.RequirePlay` ran before the upgrade, using the same
// session lookup and the same membership check as the HTTP surface — and this file
// has no role, no user and no session to check anything against. That is visible
// here as an absence: `Join` reads no authorisation input at all, which is the
// property to grep for when someone adds one.
//
// # The roll, and where it comes from
//
// S-7.3: "roll is evaluated server-side; the client never supplies a result".
// `protocol.go` makes that true at the byte level — no inbound field can hold a
// result, and an unknown field is a rejection rather than a discarded value — and
// `Resolver` makes it true at the authority level: `Hub.Apply` reads a result out
// of a resolver and out of nowhere else. There is no method on `Hub` or `Peer` that
// accepts a resolved value from a caller, and `Intent` carries only the decoded
// client frame plus the campaign and actor the *server* bound it to.
//
// # Shutdown order
//
// `Close` ends the peers, stops the sweeper, and then closes the state registry.
// The registry goes last because `Registry.Close` is the flush: a state opened by
// a join and not flushed is a table's session lost to a clean shutdown, and the
// composition root's `store.Close` cannot be reached until this returns. The context
// is the caller's and must be `context.WithoutCancel` — the same requirement and for
// the same reason `Registry.Close` states.

package realtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/semiplane/semiplane/internal/domain"
)

// MaxTransportReadBytes is the read limit a connection that carries this codec
// must be configured with.
//
// It MUST be larger than `protocol.go`'s `MaxClientFrameBytes`, and that is not
// generosity. A smaller transport limit silently redefines the protocol: the
// transport becomes the thing that decides a frame is too big, the codec's own
// bound stops meaning anything, and the two disagree about the same frame in a way
// only a client ever observes. Twice the codec's bound leaves room for the
// framing a WebSocket implementation adds around a message — and nothing else,
// because the codec's bound is a property of the bytes and the transport's is a
// property of the pipe.
//
// `TestTheTransportLimitExceedsTheCodecBound` holds the relationship, because a
// constant that can be lowered below the one it must exceed is a constant nobody
// re-reads.
const MaxTransportReadBytes = MaxClientFrameBytes << 1

// broadcastBuffer is how many undelivered broadcasts one peer may hold.
//
// One, and the number is policy rather than tuning. A broadcast says "the table
// moved"; two inside one second are one fact, and the newest is the one the reader
// needs — the same reasoning `internal/httpapi/events`'s `noticeBuffer` gives, for
// the same reason. A deeper buffer would let a burst replay into a client one
// superseded frame at a time, which is latency dressed up as completeness.
const broadcastBuffer = 1

// MaxQueuedAnswers is how many unanswered frames one peer may hold.
//
// Sixteen, and the ceiling is on a client that is not reading its own replies,
// which is a client whose socket has stopped moving. A browser sends an intent per
// user gesture and applies it optimistically, so sixteen outstanding means the
// connection is not the bottleneck — the network is — and by the time a
// seventeenth answer is owed the optimistic update the first one would have
// confirmed is no longer worth confirming.
//
// Reaching it closes the connection rather than dropping the oldest answer. A
// dropped answer is a `seq` a client waits on forever, which is the failure mode a
// silent queue has; a closed connection is a `hello` the client retries.
const MaxQueuedAnswers = 16

// MaxPeersPerCampaign is the most simultaneous connections one campaign may hold.
//
// A limit rather than a preference, because the resource being protected is the
// hub's own: every peer is a map entry and two channels, so a campaign a client
// script fans out across is a campaign whose memory grows with the script. 512 is
// four ordinary tables' worth of players and gms and is high enough that no honest
// deployment meets it.
const MaxPeersPerCampaign = 512

// DefaultStaleAfter is how long a peer's outbound queue may stay full before the
// peer is retired as stale.
//
// Forty-five seconds, and the choice is about what the reader loses. A retired peer
// reconnects and is answered with a fresh snapshot or a delta, so the cost of
// retiring too eagerly is one resync; the cost of retiring too late is a socket
// and two channels held for every tab a browser has ever abandoned, which is
// unbounded over the life of a process and is the failure this exists to prevent.
//
// It is not a write timeout and it does not measure one; see the file comment for
// the three reasons a socket-side deadline is the wrong detector.
const DefaultStaleAfter = 45 * time.Second

// The sentinels a caller branches on. None carries anything a log line would
// repeat, and `ErrHubClosed` and `ErrPeerClosed` are deliberately distinct: one is
// "this process is going away" and the other is "this connection is over", and a
// reconnect loop that cannot tell them apart treats a shutdown as a refusal.
var (
	// ErrHubClosed means the hub has been shut down.
	ErrHubClosed = errors.New("realtime: the hub is shut down")

	// ErrPeerClosed means the peer has left, or was retired.
	ErrPeerClosed = errors.New("realtime: the peer is closed")

	// ErrNoCampaign means the campaign id is not a campaign.
	ErrNoCampaign = errors.New("realtime: not a campaign")

	// ErrNoViewer means the identity offered for a join is not one this build can
	// name a role for.
	ErrNoViewer = errors.New("realtime: viewer identity is not usable")

	// ErrCampaignFull means the campaign already holds MaxPeersPerCampaign peers.
	ErrCampaignFull = errors.New("realtime: the campaign is at its connection limit")

	// ErrNoResolver means the hub has no resolver, and therefore no way to apply an
	// intent. A wiring fault rather than a client's, and it is checked before the
	// frame is answered so the caller learns it is not the client's frame.
	ErrNoResolver = errors.New("realtime: no resolver is configured")

	// ErrAnswersFull means the peer's reply queue is at MaxQueuedAnswers, which is
	// the condition for retiring it: it is not reading its own answers.
	ErrAnswersFull = errors.New("realtime: the peer's reply queue is full")
)

// Hub is the process's fan-out for one campaign's live peers.
//
// Process-local and not shared, for the reason ADR 0004 is about: the WebSocket
// plane is the second half of "authoritative state lives in memory", so two
// instances behind a load balancer diverge silently. A hub is a value the
// composition root constructs and hands to the route and to the resolver — not a
// package global and not something an `init()` fills in, because a global is a
// second source of truth about who is connected and is shared by every test in the
// process.
//
// Safe for concurrent use. The mutex is held only for map and channel operations
// and never across I/O, so one campaign's slow client cannot hold up another's
// fan-out.
type Hub struct {
	states  *Registry
	resolve Resolver
	clock   HubClock

	// cancel stops the sweeper. The context it cancels is a parameter of the
	// goroutine rather than a field, so no `context.Context` is ever stored — which
	// is `containedctx` right, and is why `NewHub` keeps the derivation local.
	cancel context.CancelFunc
	// sweeperDone closes when the sweeper goroutine has returned. `Close` waits on
	// it, so a shutdown cannot report itself finished while a goroutine this hub
	// started is still walking a room.
	sweeperDone chan struct{}

	// mu guards everything below it.
	mu sync.Mutex
	// rooms is the live peers of every campaign with at least one, keyed by campaign
	// id and then by peer id. A map per campaign rather than one flat map with a
	// filter, because unlike a page-change notice — a handful of subscribers per
	// editor — a broadcast here fans out to every connected player of every campaign
	// in the process, so the filter would be an O(total peers) scan per mutation on
	// the table's hottest path.
	rooms map[int64]*room
	// nextID hands out peer ids, which are process-unique and never reused. A
	// reused id would make two different connections indistinguishable in a log
	// line about a campaign that had just lost one of them.
	nextID int64
	// closed is set by Close, after which Join refuses and every peer is closed.
	closed bool

	// published counts broadcasts handed to Publish, superseded counts broadcasts
	// replaced by a newer one, answered counts replies queued, and staled counts
	// peers retired for being behind. `staled` is the one an operator reads first:
	// ADR 0010 names `ws.stale` as a gauge because a leaked connection is otherwise
	// silent, and a counter that only rises is the shape that cannot be silent.
	published  atomic.Int64
	superseded atomic.Int64
	answered   atomic.Int64
	staled     atomic.Int64

	// closeOnce guards the one-time shutdown.
	closeOnce sync.Once
}

// HubClock is the clock the staleness sweep runs on.
//
// Injected for the reason `Cadence` is, and it is the same reason rather than a
// different one: "this peer has been behind for longer than the window" is a claim
// about the absence of an event, and asserting an absence in real time means
// sleeping past the window — which makes a test slow and its assertion weaker at
// the same time. With an injected clock the window is a number the test chooses.
//
// The zero value is the production default rather than "no sweep", for the reason
// `Cadence.withDefaults` gives: a hub built without a stated clock is the safe one.
type HubClock struct {
	// Stale is how long a peer's outbound queue may stay full before the peer is
	// retired. Zero or negative means DefaultStaleAfter.
	//
	// In the clock rather than beside it because the window and the clock that
	// measures it are one decision, for the reason `Cadence` is a struct: a caller
	// handed a window and a clock in the wrong order gets a sweep that fires on one
	// timeline and measures on another.
	Stale time.Duration

	// Now reads the clock. Nil means `time.Now`.
	Now func() time.Time

	// After returns a channel that receives once d has elapsed on the same clock.
	// Nil means `time.After`.
	//
	// A function rather than a `*time.Ticker` for the reason `Cadence.After` gives:
	// a fake timer cannot be driven by a fake clock without reimplementing the
	// runtime's timer wheel, and reimplementing the wheel is how a test ends up
	// agreeing with a defect in the reimplementation.
	After func(d time.Duration) <-chan time.Time
}

// withDefaults fills the zero values, and is the only place the defaults above are
// read.
func (c HubClock) withDefaults() HubClock {
	if c.Stale <= 0 {
		c.Stale = DefaultStaleAfter
	}

	if c.Now == nil {
		c.Now = time.Now
	}

	if c.After == nil {
		c.After = time.After
	}

	return c
}

// HubConfig is the set of seams a Hub is built over.
type HubConfig struct {
	// States is the registry of live campaign states. Required: it is what makes
	// "exactly one authority per campaign" true, and the hub is the only thing that
	// causes a state to be opened.
	States *Registry

	// Resolve applies a client's intent. Optional, because a hub that never receives
	// an intent is a working read-only hub — and refusing at the call rather than at
	// the constructor is what makes the failure name the thing that needed it.
	Resolve Resolver

	// Clock is the sweep's window and clock. The zero value is the production default.
	Clock HubClock
}

// withDefaults fills the zero values, and is the only place the defaults above are
// read.
func (c HubConfig) withDefaults() HubConfig {
	c.Clock = c.Clock.withDefaults()

	return c
}

// NewHub returns a hub over cfg, and starts its one goroutine.
//
// ctx is the hub's lifetime: cancelling it stops the sweeper without closing any
// peer, which is the crash case and is deliberately the same observation as a
// process that died. `Close` is the shutdown — it ends every peer, and then flushes
// every campaign state. A composition root that wants a graceful stop calls `Close`
// with a `context.WithoutCancel` context first.
//
// The one goroutine is the sweeper, and it is one for the whole process rather than
// one per campaign: it walks rooms and retires peers, so a hub with a thousand
// campaigns and a hub with one cost the same single goroutine. Every peer
// contributes a channel and a map entry and nothing else, which is the property
// `TestJoiningAConnDoesNotStartAGoroutine` counts.
func NewHub(ctx context.Context, cfg HubConfig) *Hub {
	cfg = cfg.withDefaults()

	hubCtx, cancel := context.WithCancel(ctx)

	hub := &Hub{
		states:      cfg.States,
		resolve:     cfg.Resolve,
		clock:       cfg.Clock,
		cancel:      cancel,
		sweeperDone: make(chan struct{}),
		rooms:       make(map[int64]*room),
	}

	go hub.sweepUntil(hubCtx)

	return hub
}

// room is one campaign's live peers.
type room struct {
	// campaign is the campaign whose peers these are, carried so a `Peer` can be
	// detached without being handed its campaign back.
	campaign int64
	// peers is keyed by peer id, so a retraction is a delete rather than a scan.
	peers map[int64]*Peer
}

// Stats is the hub's counters.
//
// A struct rather than four return values because they are read together and
// `Superseded` is only meaningful next to `Published`: a hub that superseded nothing
// out of a million broadcasts is healthy, and a hub that superseded all of them is
// a hub whose clients are not reading. The ratio is the fact.
type Stats struct {
	// Peers is how many connections are open now, across every campaign.
	Peers int
	// Campaigns is how many campaigns hold at least one peer.
	Campaigns int
	// Published is how many broadcasts were handed to Publish.
	Published int64
	// Superseded is how many were replaced by a newer one because their peer was
	// not draining.
	Superseded int64
	// Answered is how many replies were queued for one peer.
	Answered int64
	// Staled is how many peers were retired for being behind. Cumulative, and the
	// gauge ADR 0010 names.
	Staled int64
}

// Stats returns the hub's counters.
//
// The peer and campaign counts are read under the lock and the counters are not,
// which is correct: the counters are monotonic, so a reader with a slightly stale
// `Superseded` has a log line a moment behind, whereas a peer count read without
// the lock could be read while the map is being written.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	peers, campaigns := 0, len(h.rooms)

	for _, group := range h.rooms {
		peers += len(group.peers)
	}

	h.mu.Unlock()

	return Stats{
		Peers:      peers,
		Campaigns:  campaigns,
		Published:  h.published.Load(),
		Superseded: h.superseded.Load(),
		Answered:   h.answered.Load(),
		Staled:     h.staled.Load(),
	}
}

// Join admits a connection to a campaign and returns its peer.
//
// campaignID and who are **already resolved**. The route mounted the access gate
// before the upgrade (ADR 0024), so this function reads no authorisation input at
// all — see the file comment. That absence is the property, and it is worth grepping
// for if someone is about to add a role check here: a check inside a handler is the
// shape ADR 0024 was written about, and S-8's "no access is 404" cannot be
// reproduced from inside a hub at all.
//
// The campaign's live state is found with `Registry.Get` and opened only when
// absent. An unconditional `Open` would be refused with `ErrStateOpen` by the second
// join to a live campaign — which is `state.go`'s deliberate answer to a second
// authority — so it is not a call this function may make speculatively.
//
// A join after the hub is closed is `ErrHubClosed`. A join to a campaign already at
// `MaxPeersPerCampaign` is `ErrCampaignFull`; neither is silent, because a route
// that treated them as a normal connection would report a healthy socket to a client
// that is receiving nothing.
func (h *Hub) Join(ctx context.Context, campaignID int64, who Viewer) (*Peer, error) {
	if campaignID <= 0 {
		return nil, fmt.Errorf("%w: campaign id %d", ErrNoCampaign, campaignID)
	}

	// `Role.Valid()` rather than a membership lookup, because the role arrived from
	// a lookup and re-checking it would be a second answer to one question. What
	// this refuses is the build's own gap: a membership row naming a role this
	// binary has no name for, which `Encode` would then refuse to send as a
	// `snapshot`'s `you`.
	if !who.Role.Valid() {
		return nil, fmt.Errorf(
			"%w: viewer %d carries a role with no name in this build",
			ErrNoViewer, who.ID,
		)
	}

	if h.isClosed() {
		return nil, ErrHubClosed
	}

	// The state first, and outside the lock: `Registry.Open` reads and writes a row,
	// and holding the hub's mutex across a database round trip would put every other
	// campaign's fan-out behind this campaign's first join.
	if err := h.openAuthority(ctx, campaignID); err != nil {
		return nil, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Re-checked under the lock, because `openAuthority` is outside it and a hub
	// that closed in between has nothing to admit anybody to.
	if h.closed {
		return nil, ErrHubClosed
	}

	group, opened := h.rooms[campaignID]
	if !opened {
		group = &room{campaign: campaignID, peers: make(map[int64]*Peer)}
		h.rooms[campaignID] = group
	}

	if len(group.peers) >= MaxPeersPerCampaign {
		if !opened {
			delete(h.rooms, campaignID)
		}

		return nil, fmt.Errorf(
			"%w: campaign %d holds %d",
			ErrCampaignFull, campaignID, len(group.peers),
		)
	}

	h.nextID++

	peer := &Peer{
		id:         h.nextID,
		campaignID: campaignID,
		who:        who,
		hub:        h,
		broadcasts: make(chan []byte, broadcastBuffer),
		answers:    make(chan []byte, MaxQueuedAnswers),
	}

	group.peers[peer.id] = peer

	return peer, nil
}

// Publish broadcasts one frame to every peer of a campaign.
//
// The frame is encoded **once** and the same bytes go to every peer. Fifty clients
// is fifty writes of one buffer, which is the whole point of fanning out from the
// mutation that caused it: a resolver that rolled a die has already paid for the
// JSON, and re-encoding it fifty times would be fifty chances to disagree about the
// bytes.
//
// Non-blocking by construction, and that is a contract with the caller rather than an
// implementation detail. The publisher is the mutation path and the intent path,
// both of which run inside somebody's request, so a hub whose send could block would
// put a browser tab between a dice roll and the answer to it.
//
// A broadcast that cannot be placed **replaces** the one already there. The other
// two answers are both wrong: blocking stalls the caller, and dropping warns about
// an older change than the one that actually happened.
func (h *Hub) Publish(campaignID int64, frame ServerFrame) error {
	payload, err := Encode(frame)
	if err != nil {
		return fmt.Errorf("realtime: broadcast to campaign %d: %w", campaignID, err)
	}

	h.published.Add(1)

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil
	}

	group, open := h.rooms[campaignID]
	if !open {
		return nil
	}

	for _, peer := range group.peers {
		h.offer(peer, payload)
	}

	return nil
}

// Apply resolves one client intent, answers the actor, and broadcasts what changed.
//
// The one path by which anything a client says becomes state, and the reason the
// resolver exists rather than the hub doing the work: **the result comes from the
// resolver and from nowhere else.** `intent` is a decoded frame whose grammar has no
// field a roll outcome could have arrived in (`protocol.go`, and it is a rejection
// rather than a discarded value), and this function reads exactly two things out of
// it — `Seq`, to address the answer, and the op and arguments, which go to the
// resolver and come back as whatever the resolver chose.
//
// A refusal is **not** an error this function returns. It is answered with a
// `rejected` carrying one reason from the closed set and reported as success, because
// the client asked a question and got a complete answer; returning the resolver's
// error as well would invite the route to answer a second time, and two answers to
// one `seq` is a client that rolls back twice.
func (h *Hub) Apply(ctx context.Context, peer *Peer, intent *ClientIntent) error {
	if peer == nil || intent == nil {
		return fmt.Errorf("%w: an intent needs a peer and a frame", ErrNoResolver)
	}

	if h.resolve == nil {
		return fmt.Errorf("%w: nothing can answer an intent", ErrNoResolver)
	}

	// The campaign and the actor come from the peer, not from the frame. There is no
	// campaign field on a client frame and one must not be added: a frame that named
	// its own campaign would be a frame that chose which table it was playing on.
	resolution, err := h.resolve.Resolve(ctx, Intent{
		Campaign: peer.campaignID,
		Actor:    peer.who.ID,
		Role:     peer.who.Role,
		Frame:    intent,
	})
	if err != nil {
		return peer.Send(&ServerRejected{
			Type:   TypeRejected,
			Seq:    intent.Seq,
			Reason: reasonFor(err),
		})
	}

	if resolution.Answer != nil {
		if err := peer.Send(resolution.Answer); err != nil {
			return err
		}
	}

	if len(resolution.Broadcast) == 0 {
		return nil
	}

	// Broadcast to everyone, the actor included. The actor's own `applied` carries
	// the authoritative version for the placement it asked about, and the delta
	// carries the same version, so a client that applies the delta first finds its
	// optimistic write already current — which is the reconcile ADR 0009 rule 1 is
	// about, not a correction.
	//
	// No `since`, because a live broadcast is not a resync answer and §7.1's `delta`
	// uses `since` for exactly that question. `ServerDelta.Since` documents the same.
	return h.Publish(peer.campaignID, &ServerDelta{
		Type:    TypeDelta,
		Changes: resolution.Broadcast,
	})
}

// Close ends every peer, stops the sweeper, and flushes every campaign state.
//
// In that order, and each step is load-bearing. Ending the peers first means no
// publish can arrive while the registry is being flushed; waiting for the sweeper
// second means the shutdown does not report itself finished while a goroutine this
// hub started is still walking a room; closing the registry last is the flush, and
// `state.go` is explicit that a registry that was cancelled instead of closed is a
// crash, so a shutdown that reached `store.Close` first would lose the last debounce
// of every campaign in the process.
//
// Bounded, and the bound is the sweeper: it selects on the hub's context, so it
// returns on the cancel above rather than waiting for its next tick. Nothing here
// waits on a peer, because a peer cannot block — that is what the non-blocking send
// bought.
//
// ctx must not be the shutdown context. Pass `context.WithoutCancel` of it: the flush
// is the reason `Close` takes one, and a caller that hands over an already-cancelled
// context has asked for the crash case and got it.
func (h *Hub) Close(ctx context.Context) error {
	h.shutdown()

	// Outside the `once`, and unconditionally: `Registry.Close` is idempotent, and a
	// second call here should report a flush failure if one occurred rather than
	// reporting nothing at all.
	return h.states.Close(ctx)
}

// shutdown performs the one-time teardown of the peers and the sweeper.
func (h *Hub) shutdown() {
	h.closeOnce.Do(func() {
		// Cancelled first so the sweeper cannot be part-way through a sweep when
		// the rooms below are emptied from under it.
		h.cancel()

		h.mu.Lock()
		h.closed = true

		for campaignID, group := range h.rooms {
			for id, peer := range group.peers {
				h.detach(peer)
				delete(group.peers, id)
			}

			delete(h.rooms, campaignID)
		}

		h.mu.Unlock()

		<-h.sweeperDone
	})
}

// openAuthority finds the campaign's one live state, or opens it.
//
// `Get` first and always: `state.go` refuses a second `Open` for a live campaign
// with `ErrStateOpen`, so a hub that opened per join would have every join after the
// first one fail on a campaign that is working perfectly. The `ErrStateOpen` branch
// is the racing second join — two peers arriving together, both finding nothing, both
// calling `Open` — and the right answer to losing that race is to take the winner's
// state rather than to fail.
func (h *Hub) openAuthority(ctx context.Context, campaignID int64) error {
	if _, live := h.states.Get(campaignID); live {
		return nil
	}

	if _, err := h.states.Open(ctx, campaignID); err != nil {
		if !errors.Is(err, ErrStateOpen) {
			return fmt.Errorf("realtime: open the live state for campaign %d: %w", campaignID, err)
		}

		if _, live := h.states.Get(campaignID); live {
			return nil
		}

		return fmt.Errorf(
			"%w: campaign %d is reserved but not readable, so a join is racing the "+
				"holder's own load; refusing rather than handing out an empty tabletop",
			ErrStateOpen, campaignID,
		)
	}

	return nil
}

// isClosed reports whether the hub has been shut down, without taking the lock for
// the caller's benefit.
//
// The lock, not an atomic: the flag is written under the mutex and a read that
// raced it would be a read of a `bool` concurrent with a write, which the race
// detector is right to object to and which would make the early `Join` check a lie
// rather than an optimisation.
func (h *Hub) isClosed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.closed
}

// sweepUntil runs the staleness sweep on a timer for as long as ctx lives.
//
// The interval is the stale window rather than something shorter, because a shorter
// one would only make the sweep more eager to retire a peer it has not yet given the
// full window. One goroutine for the whole process, and the reason this file can
// claim no goroutine per connection.
func (h *Hub) sweepUntil(ctx context.Context) {
	defer close(h.sweeperDone)

	for {
		select {
		case <-ctx.Done():
			return

		case <-h.clock.After(h.staleWindow()):
		}

		h.sweep(h.clock.Now())
	}
}

// sweep retires every peer whose outbound queue has been full since before now.
func (h *Hub) sweep(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	for campaignID, group := range h.rooms {
		for id, peer := range group.peers {
			if !peer.behind(now, h.staleWindow()) {
				continue
			}

			h.staled.Add(1)
			h.detach(peer)

			delete(group.peers, id)
		}

		if len(group.peers) == 0 {
			delete(h.rooms, campaignID)
		}
	}
}

// offer puts a broadcast on a peer's replacing slot, or supersedes what is there.
//
// Both sends are non-blocking `select`s, called with the hub's lock held, so two
// publishers cannot interleave their replacement of the same slot. A peer only ever
// receives from this channel, so it needs no lock of its own and cannot deadlock
// against this.
//
// `behind` is the whole of the staleness signal, and it is set only when **both**
// sends failed — that is, when the slot stayed full for the entire publish. A peer
// that drains between the two selects is a peer that is keeping up, and arming the
// clock on a successful send would retire a healthy client for being busy.
func (h *Hub) offer(peer *Peer, payload []byte) {
	select {
	case peer.broadcasts <- payload:
		peer.delivered++
		peer.behindSince = time.Time{}

		return

	default:
	}

	// Full. Take the stale notice out so the newest can take its place, then try
	// again. The second `default` is reachable — the peer may have drained the slot
	// in between — and reaching it costs one broadcast, which is the documented
	// policy rather than a failure.
	select {
	case <-peer.broadcasts:
		peer.superseded++
		h.superseded.Add(1)

	default:
	}

	select {
	case peer.broadcasts <- payload:
		peer.delivered++
		peer.behindSince = time.Time{}

		return

	default:
	}

	if peer.behindSince.IsZero() {
		peer.behindSince = h.clock.Now()
	}
}

// detach closes a peer's queues and removes it from its room.
//
// The lock is held by the caller, which is what makes the close safe: every send
// into those channels also happens under this lock, so no send can be mid-flight
// when the channel closes, and a send on a closed channel panics — a panic that
// would arrive in whichever goroutine published, which is a resolver returning a
// dice roll rather than anywhere it could be recovered.
//
// Closing the channel is the whole of a peer's teardown. Its receive loop sees a
// closed channel, its own `select` returns, and the socket closes with it — there is
// no timer to stop and no other teardown condition to remember.
func (h *Hub) detach(peer *Peer) {
	if group, open := h.rooms[peer.campaignID]; open {
		delete(group.peers, peer.id)
	}

	peer.closedOnce.Do(func() {
		// Set before the closes rather than after, so a concurrent `Send` sees a
		// closed peer rather than a half-closed one. Both happen under the lock, so
		// the window the flag would close does not exist — the flag is here so the
		// invariant is stated where the closes are.
		peer.closedState = true

		close(peer.broadcasts)
		close(peer.answers)
	})
}

// staleWindow returns the configured window. The config value is copied into the
// hub at construction and read back through this, so there is one place a default
// can be applied and no field a caller could change under a running sweep.
func (h *Hub) staleWindow() time.Duration { return h.clock.Stale }

// Peer is one connection's view of one campaign's live table.
//
// Immutable after `Join` except for the three counters and the staleness clock, all
// of which live under the hub's mutex. `campaignID` and `who` are written once and
// read without the lock, which is why they are documented as fixed rather than
// guarded: a value written before the peer was published into a map and never
// changed after cannot race with anything.
//
// A peer is not a connection. It is the hub's half of one, and it holds no socket —
// the route owns the socket and the loop that drains these channels, for the reason
// the file comment gives.
type Peer struct {
	// id is the process-unique peer id.
	id int64
	// campaignID is the campaign this peer was admitted to.
	campaignID int64
	// who is the identity the route resolved before the upgrade. A log line field
	// and the addressing of a `snapshot`'s `you`; never an authorisation input, for
	// the reason `Notice.Path` is not one in `internal/httpapi/events`.
	who Viewer
	// hub is the peer back to its hub, for detaching.
	hub *Hub

	// broadcasts is the replacing slot: capacity one, newest wins.
	broadcasts chan []byte
	// answers is the reply queue: bounded at MaxQueuedAnswers, never replaced.
	answers chan []byte

	// behindSince is when this peer's outbound queue last became full and stayed
	// full. Zero when the queue is empty. Guarded by `hub.mu`.
	behindSince time.Time
	// delivered, superseded and the closure are this peer's counters and its
	// one-time teardown. Guarded by `hub.mu`, and `closedOnce` because a channel
	// closed twice panics and a panic on a shutdown path is the one failure a
	// graceful stop cannot survive.
	delivered   int64
	superseded  int64
	closedOnce  sync.Once
	closedState bool
}

// ID returns the process-unique peer id.
func (p *Peer) ID() int64 { return p.id }

// CampaignID returns the campaign this peer was admitted to.
func (p *Peer) CampaignID() int64 { return p.campaignID }

// Viewer returns the identity and role the route resolved for this peer.
func (p *Peer) Viewer() Viewer { return p.who }

// Broadcasts is the replaceable broadcast slot.
//
// Receive-only, because a send would be a reader pretending to be a writer and the
// hub is the only writer. A closed channel yields a zero value and `false`, which is
// the loop's signal to end — so the receive loop needs no other teardown condition
// and cannot miss one.
func (p *Peer) Broadcasts() <-chan []byte { return p.broadcasts }

// Answers is the peer's reply queue: the frames that resolve its outstanding `seq`.
//
// Receive-only, for the reason `Broadcasts` is. Nothing here is ever replaced, so a
// loop may drain it at its own pace without risking an `applied` being eaten by a
// later delta.
func (p *Peer) Answers() <-chan []byte { return p.answers }

// Advance reports that the connection took a frame off one of its queues.
//
// This is what separates a peer that is behind from a peer that is gone, and the
// distinction is the entire staleness mechanism: a queue that is full *and has been
// for the whole window* is a peer that is not draining, and a queue that is full
// because the last publish landed microseconds ago is a healthy peer. Without this
// call the hub can only see the second kind and would retire a table mid-game.
//
// Cheap enough to call per frame, which is the only rate that can be accurate: a
// counter of drains sampled periodically would retire a peer whose queue was full at
// the sample and drained in between.
func (p *Peer) Advance() {
	p.hub.mu.Lock()
	defer p.hub.mu.Unlock()

	p.behindSince = time.Time{}
}

// Leave removes the peer from its campaign and closes its queues.
//
// Idempotent, and it has to be: a route's deferred leave runs on every exit from
// the read loop, including the error exits, and a second leave arriving
// concurrently with a stale sweep must not double-close a channel. Both paths go
// through the hub's `detach`, which holds the lock and closes once.
func (p *Peer) Leave() {
	p.hub.mu.Lock()
	defer p.hub.mu.Unlock()

	p.hub.detach(p)
}

// Send queues one frame addressed to this peer alone.
//
// This is the path for everything that is not a broadcast: a `snapshot` answering a
// `hello`, the `applied` or `rejected` answering one `seq`, a roster. It never
// writes to a socket — the connection's loop does that — and it never blocks.
//
// A full reply queue retires the connection rather than dropping the frame. The
// alternative is a `seq` a client waits on forever, which is the failure a silent
// queue has and the reason `ErrAnswersFull` closes rather than returns.
func (p *Peer) Send(frame ServerFrame) error {
	payload, err := Encode(frame)
	if err != nil {
		return fmt.Errorf("realtime: reply to peer %d: %w", p.id, err)
	}

	p.hub.mu.Lock()
	defer p.hub.mu.Unlock()

	if p.isClosedLocked() {
		return fmt.Errorf("%w: peer %d", ErrPeerClosed, p.id)
	}

	select {
	case p.answers <- payload:
		p.hub.answered.Add(1)

		return nil

	default:
	}

	// The queue is full, so this peer is not reading the replies it has already been
	// sent. That is the stale condition, and answering it by retiring the connection
	// is the same action the sweep would take — taken now rather than in up to one
	// window, because the caller is here and the sweep is not.
	p.hub.staled.Add(1)
	p.hub.detach(p)

	return fmt.Errorf("%w: peer %d holds %d", ErrAnswersFull, p.id, MaxQueuedAnswers)
}

// PeerStats is one peer's counters.
//
// Separate from `Hub.Stats` because a hub-wide `Superseded` says nothing about which
// connection is the problem, and the connection is the thing an operator can act on.
type PeerStats struct {
	// ID is the peer these counters belong to.
	ID int64
	// Delivered is how many broadcasts reached the slot.
	Delivered int64
	// Superseded is how many were replaced by a newer one.
	Superseded int64
	// Queued is how many replies are waiting to be written.
	Queued int
	// BehindFor is how long the queue has been continuously full, or zero if it is
	// not full.
	BehindFor time.Duration
	// Closed is whether the peer has been detached.
	Closed bool
}

// Stats returns this peer's counters, read under the hub's lock.
func (p *Peer) Stats() PeerStats {
	p.hub.mu.Lock()
	defer p.hub.mu.Unlock()

	var behindFor time.Duration

	if !p.behindSince.IsZero() {
		behindFor = p.hub.clock.Now().Sub(p.behindSince)
	}

	return PeerStats{
		ID:         p.id,
		Delivered:  p.delivered,
		Superseded: p.superseded,
		Queued:     len(p.answers),
		BehindFor:  behindFor,
		Closed:     p.isClosedLocked(),
	}
}

// behind reports whether this peer's queue has been full for a whole window.
func (p *Peer) behind(cutoff time.Time, window time.Duration) bool {
	if p.behindSince.IsZero() {
		return false
	}

	return cutoff.Sub(p.behindSince) >= window
}

// isClosedLocked reports whether the peer's queues have been closed. The hub's lock
// must be held, because `closedState` is written under it.
func (p *Peer) isClosedLocked() bool { return p.closedState }

// Conn is the write half of one peer connection: the whole of the transport this
// hub depends on.
//
// One method because one is all anything here needs, and a narrow interface is what
// lets a hub be exercised against a channel rather than a socket. It is exported so
// the route can hold a `Conn` without naming the library's type.
//
// Implementations must be safe for concurrent use. `WebSocketConn` is, with a mutex.
type Conn interface {
	// Write sends one already-encoded frame.
	Write(ctx context.Context, payload []byte) error
}

// WebSocketConn adapts a `coder/websocket` connection to Conn, serialising writes.
//
// The library is `coder/websocket` rather than `gorilla/websocket` for the two
// reasons ADR 0010 gives: no advisory in the masking path, and a `context.Context`
// on every operation, which is what composes with the single-instance shutdown.
//
// The mutex is the serialisation ADR 0010 asks this project to hold rather than
// delegate, and it is here rather than in the route because a write mutex in a
// handler is a mutex a handler can forget. In the shape this file describes there is
// exactly **one** writer — the connection's own receive loop, which is the only thing
// that can take a frame off `Peer.Broadcasts` — so the mutex is not what makes the
// common case correct; it is what makes a *second* writer impossible to get wrong.
// The library panics on concurrent writes, and a panic in the goroutine that
// published a delta is a panic with nobody to recover it.
type WebSocketConn struct {
	// mu serialises writes and the close. Held across the write, deliberately: a
	// release before the write finished would serialise nothing.
	mu   sync.Mutex
	conn *websocket.Conn
	// closed is whether Close has run, under `mu`. `websocket.Conn.Close` is
	// idempotent in practice, but a second close carrying a second reason is a second
	// status frame on a socket nobody will read, and the counter reads as a bug.
	closed bool
}

// NewWebSocketConn returns conn as a Conn.
func NewWebSocketConn(conn *websocket.Conn) *WebSocketConn {
	return &WebSocketConn{conn: conn}
}

// Write implements Conn.
func (w *WebSocketConn) Write(ctx context.Context, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return fmt.Errorf("%w: the socket is closed", ErrPeerClosed)
	}

	// A transport error is not a frame error, and it is wrapped so `errors.Is` still
	// reaches the library's own sentinel. No frame byte is in it.
	if err := w.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return fmt.Errorf("realtime: write a frame: %w", err)
	}

	return nil
}

// Close ends the connection with a status code and a reason, once.
//
// The reason is this file's own words — a class, never a value that came from a
// frame — for the reason `FrameError` gives: a reason travels to a browser and is
// rendered there, so it may not carry a plugin's error text.
func (w *WebSocketConn) Close(code websocket.StatusCode, reason string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return nil
	}

	w.closed = true

	if err := w.conn.Close(code, reason); err != nil {
		return fmt.Errorf("realtime: close the socket: %w", err)
	}

	return nil
}

// Intent is one client intent, bound to the connection it arrived on.
//
// The binding is the point. `Frame` is client-controlled and `Campaign`, `Actor` and
// `Role` are not: they come from the peer, which the route admitted after the access
// gate. A resolver therefore cannot be told which campaign to play on by the frame
// it is resolving, because the frame has no field in which to say so.
//
// The sealed `*ClientIntent` is carried whole rather than restated as a flat struct
// of this package's own. A second struct over the same data is a second answer to
// "what is an intent", and `state.go` argues against exactly that when it embeds
// `Placement` in `Document` rather than declaring a wire struct. What a resolver gets
// is the frame the codec validated.
type Intent struct {
	// Campaign is the campaign the connection was admitted to.
	Campaign int64
	// Actor is the connection's user.
	Actor UserID
	// Role is the actor's role in this campaign, which is how §7.2's GM-only rules
	// are enforced without a database round trip. The access gate has already run;
	// this is gameplay authority, not entry.
	Role domain.Role
	// Frame is the decoded client intent.
	Frame *ClientIntent
}

// Resolver applies a client's intent and reports what happened.
//
// Narrow on purpose — one method — and declared here rather than imported from a
// systems package so that `internal/realtime` does not depend on `internal/plugin`,
// and so the **authority** stays in this file's caller. S-7.3 says a roll is
// evaluated server-side; this interface is where "server-side" is an interface, which
// is what makes it checkable: `Hub.Apply` reads a result out of a `Resolver` and out
// of nothing else.
//
// Implementations are registered explicitly in the composition root, never in an
// `init()`. R3b wires the inert `core` system into it.
type Resolver interface {
	// Resolve applies intent and reports the frame answering the actor's `seq` and
	// the changes the campaign is told about.
	Resolve(ctx context.Context, intent Intent) (Resolution, error)
}

// Resolution is a resolver's answer: the reply, and the broadcast.
//
// Two halves rather than one because they go to different audiences. The reply
// carries a `seq` and is addressed to one client; the changes are addressed to the
// campaign and carry no `seq`, because a `delta` with a `seq` on it would pair
// itself with an intent that was not the actor's.
type Resolution struct {
	// Answer resolves the actor's `seq`: a `*ServerApplied` or a `*ServerRejected`.
	// Optional, because a resolver that resolved something the client cannot be told
	// — a campaign-level pause, say — has nothing to answer with, and inventing an
	// empty `applied` would claim a version that does not exist.
	Answer ServerFrame
	// Broadcast is what the campaign is told. Empty when nothing changed, which is a
	// real answer: an intent that was applied to nothing (a presence, a rejected
	// client-side guess) broadcasts nothing.
	Broadcast []Change
}

// RejectionError is a resolver's refusal, naming one of `protocol.go`'s closed reasons.
//
// The reason is separate from the error so the two cannot be confused. The reason
// goes on the wire and is rendered in a browser; the error is for a log line and may
// be anything the resolver wants. `reasonFor` copies the reason and never the error's
// text, and `Encode` refuses a frame whose reason is outside the closed set — so the
// worst thing that can reach a client is one of the eight words in `rejectReasons`.
type RejectionError struct {
	// Reason is what the actor is told. From the closed set, and the hub verifies
	// that rather than assuming it.
	Reason RejectReason
	// Err is what the resolver returns to its own caller. May be nil.
	Err error
}

// Error returns the reason and nothing else.
//
// Content-free by construction, and for the reason `FrameError` gives: this text
// reaches a log line, and a resolver's error quotes the thing it choked on, which on
// a wiki page is routinely a `[!secret]` callout body (S-12.3).
func (r *RejectionError) Error() string { return "realtime: intent rejected: " + string(r.Reason) }

// Unwrap returns the resolver's own error, so `errors.Is` reaches it.
func (r *RejectionError) Unwrap() error { return r.Err }

// reasonFor reduces a resolver's error to one of the eight reasons a client may be
// told.
//
// `RejectServerError` for anything unrecognised, and it is the *safe* default in a
// way the other seven are not: it says nothing about what failed. A resolver that
// returns an error this file does not recognise has failed in a way this file cannot
// classify, and inventing a specific reason would be asserting a fact nobody
// established — and `not_your_turn` in particular is a hint about the game's rules
// that a stranger must not be given.
func reasonFor(err error) RejectReason {
	rejection, ok := errors.AsType[*RejectionError](err)
	if ok && slices.Contains(rejectReasons, rejection.Reason) {
		return rejection.Reason
	}

	return RejectServerError
}

// FrameClass reduces an error from this package to the short, content-free class a
// log line should carry, and it is the value `observability` should be given.
//
// This exists because `observability.errorClass` ends in `fmt.Sprintf("%T", err)`,
// which for a protocol refusal is always `*realtime.FrameError` — so every refusal in
// a log reads identically and "a client sent nonsense" cannot be told from "a client
// sent too much" from "a client sent an unknown field". The class is an identifier
// this package chose, so it is safe to log verbatim, which is what `FrameError.Class`
// says and why the class exists.
//
// Anything that is not a `*FrameError` reduces to `realtime`, and that is deliberate
// rather than a placeholder: a non-frame error from this package is a wiring fault,
// and a fixed word in a log line is more use than `*fmt.wrapError` would be. The
// observability-side fix is a separate work item and this is the call site's half of
// it.
func FrameClass(err error) string {
	if frameErr, ok := errors.AsType[*FrameError](err); ok {
		return frameErr.Class()
	}

	return "realtime"
}
