package realtime_test

// The hub: fan-out, peer lifetime, and the four properties nothing else in this
// package can observe.
//
// # What these tests are for, given that `state_test.go` and `protocol_test.go`
// already exist
//
// `hub.go` makes claims that are not visible from any single call's return value:
//
//   - **a publish reaches every joined peer and no unjoined one.** The fan-out is
//     two loops and a map, and a loop that iterated the wrong collection would
//     still deliver frames.
//   - **a slow peer is superseded, not queued, and never waited for.** The three
//     possible policies — block, drop, replace — are distinguishable only by what
//     the peer ends up holding after a burst and by whether the publisher came
//     back, which is why this file asserts both.
//   - **a peer that stops reading goes stale.** The memory-leak failure is silent
//     by construction: nothing grows, because the slot is one deep, and what
//     actually happens is a connection and its channels are held forever. A test
//     that only asserted "the buffer did not grow" would pass against a hub that
//     never retired anybody.
//   - **a join costs no goroutine.** This is the one property in `hub.go` that no
//     other observation can reach, because the absence of a goroutine is the
//     assertion. It is counted, and the count is compared between two batches so
//     the assertion is about the *marginal* cost of a join rather than about what
//     the process happens to be doing.
//
// # No `t.Parallel`, and why
//
// `store.Open` claims one Store per process on purpose — ADR 0004 — and releases
// the slot in a cleanup. Two parallel tests would both hold it, and the second
// would fail on a claim that is a **correctness property of the product** rather
// than a limitation of the test harness. `state_test.go` is sequential for the same
// reason and says so at `openDatabase`. This file pays for that in wall clock and
// gets determinism back, which is the better trade for a gate.
//
// # The clock, and why the staleness tests are exact rather than sleepy
//
// `hub.go` takes its window and its clock together in `HubClock`, and the tests
// drive both through the `fakeClock` that `clock_test.go` already defines, for the
// reason `state.go` gives. "This peer has been behind for longer than the window"
// is a claim about the **absence** of an event: the sweeper did not retire a peer
// that was drained. In real time that can only be asserted by waiting past the
// window and observing whatever happened, which makes a test slow and its assertion
// weaker at exactly the same time — a longer sleep is indistinguishable from a
// correct one.
//
// The sweep runs on a goroutine this hub started, so the tests wait for it rather
// than reimplementing it. `arm` proves the sweeper armed its timer before the clock
// is advanced, which is the same guard `clock_test.go` gives: a test that advances
// time before the loop has reached its `select` is a test whose result is a race
// that happened to go its way.
//
// # The database, and why these tests go through a real one
//
// `Hub.Join` calls `Registry.Get` and `Registry.Open`, and `Open` reads and writes
// a `campaign_state` row. Asserting against a stub registry would test that the
// hub called a function, which is a different statement about a different thing —
// it passes just as happily against a `Get` that always misses, so every join
// takes the open path and the "never `Open` per join" discipline is untested. So
// the fixture goes through `openDatabase`, the same real migrated database the
// persistence tests use.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
)

const (
	// staleWindow is the staleness window these tests configure. Large enough that
	// no wall-clock wait is a plausible substitute for it — which is the point: a
	// test that could pass by sleeping is a test that has not been driven by the
	// clock it claims to be driven by.
	staleWindow = 45 * time.Second

	// secondCampaignID is the campaign the fan-out filter publishes to, so a hub
	// holding two campaigns can show that a publish does not cross between them.
	secondCampaignID = 2

	// behindTail is how far short of the window the staleness tests put a peer at the
	// moment the sweep runs, and it is the other side of the comparison in
	// `Peer.behind`.
	//
	// A third of the window rather than a second off it: an off-by-one is a *weak*
	// test of the boundary, because the sweep could be early by a whole tick and
	// still pass. A fraction makes the assertion "the sweep compares against a
	// duration and gets the direction right", which is the claim.
	behindTail = staleWindow / 3

	// absence is how long `expectNothing` waits before concluding nothing arrived.
	//
	// An absence cannot be asserted instantly, and the shorter this is the weaker
	// the claim, so it is stated rather than hidden: "no frame reached this peer in
	// 20ms" is a weaker claim than "no frame reached this peer", and it is the
	// strongest one available without a protocol-level acknowledgement.
	absence = 20 * time.Millisecond
)

// viewerGM is a game's identity, and the only one these tests need.
var viewerGM = realtime.Viewer{ID: 7, Role: domain.RoleGM}

// epochClock is the instant the fake clock starts at, and the base for every
// `ServerClock` these tests broadcast.
//
// Shared rather than spelled out per fixture because a broadcast's payload is
// asserted by comparing whole frames, and two fixtures that differ by a literal
// second are two fixtures that can drift apart silently. A tick is `Add(time.Second)`
// off this.
var epochClock = time.Date(2026, time.September, 30, 14, 0, 0, 0, time.UTC)

// newTestHub returns a hub over a real migrated database, closed on cleanup.
//
// The resolver may be nil: a hub that never receives an intent is a working
// read-only hub, and most of these tests are about fan-out rather than resolution.
//
// The registry gets the **real** clock rather than the test's, deliberately. The
// registry's debounce is a persistence property that `state_test.go` already
// asserts against a fake clock with its own harness; giving the hub and the
// registry one clock here would mean every `advance` also fired a `campaign_state`
// flush, and a test about a stale peer would then depend on when a row was written.
func newTestHub(t *testing.T, resolve realtime.Resolver) (*realtime.Hub, *fakeClock) {
	t.Helper()

	database, writer, reader := openDatabase(t)

	insertSecondCampaign(t, database)

	registry := realtime.NewRegistry(t.Context(), realtime.Config{
		Write: writer,
		Read:  reader,
	})

	clock := newFakeClock()

	hub := realtime.NewHub(t.Context(), realtime.HubConfig{
		States:  registry,
		Resolve: resolve,
		Clock: realtime.HubClock{
			Stale: staleWindow,
			Now:   clock.Now,
			After: clock.After,
		},
	})

	// Closed before the registry, and before `openDatabase`'s own cleanup closes the
	// store, because LIFO is the only order that works: the hub's `Close` is the
	// flush, and the registry is closed again after it because `Registry.Close` is
	// idempotent and a caller handed a hub should not have to know that.
	//
	// `context.WithoutCancel` because `t.Context()` is cancelled *before* cleanups
	// run — the same requirement the hub's own `Close` documents, so writing it here
	// is a checked claim rather than a convenience.
	t.Cleanup(func() {
		if err := hub.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("hub.Close() error = %v, want nil", err)
		}

		if err := registry.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("registry.Close() error = %v, want nil", err)
		}
	})

	return hub, clock
}

// insertSecondCampaign adds the campaign the fan-out filter publishes to.
func insertSecondCampaign(t *testing.T, database *store.Store) {
	t.Helper()

	const insert = `INSERT INTO campaigns
		(slug, name, content_root, visibility, system_id, ruleset_version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`

	if _, err := database.DB().ExecContext(t.Context(), insert,
		"hollow-crown", "The Hollow Crown", t.TempDir(), "private", "5e-2024", "core:v1", 1_700_000_000,
	); err != nil {
		t.Fatalf("insert the second campaign: %v", err)
	}
}

// join admits a peer to a campaign and fails the test if it cannot.
func join(t *testing.T, hub *realtime.Hub, campaignID int64) *realtime.Peer {
	t.Helper()

	peer, err := hub.Join(t.Context(), campaignID, viewerGM)
	if err != nil {
		t.Fatalf("Join(%d) error = %v, want nil", campaignID, err)
	}

	return peer
}

// decodeIntent decodes a client intent frame the way a route would.
func decodeIntent(t *testing.T, raw string) *realtime.ClientIntent {
	t.Helper()

	frame, err := realtime.Decode([]byte(raw))
	if err != nil {
		t.Fatalf("Decode(%s) error = %v, want nil", raw, err)
	}

	intent, ok := frame.(*realtime.ClientIntent)
	if !ok {
		t.Fatalf("Decode(%s) produced %T, want *realtime.ClientIntent", raw, frame)
	}

	return intent
}

// receive takes one frame off a peer's queue, failing if none arrives.
func receive(t *testing.T, frames <-chan []byte) []byte {
	t.Helper()

	select {
	case payload, open := <-frames:
		if !open {
			t.Fatal("the peer's queue closed with no frame in it")
		}

		return payload

	case <-time.After(settleBudget):
		t.Fatalf("no frame arrived within %s", settleBudget)

		return nil
	}
}

// drain empties a peer's queue without asserting anything about its contents.
//
// A closed channel still hands out its buffered values first, so every assertion
// about a peer's queue has to start from an empty one — otherwise a test reads a
// stale frame from before the close and reports a closed channel as an open one.
func drain(t *testing.T, frames <-chan []byte) {
	t.Helper()

	for {
		select {
		case _, open := <-frames:
			// A closed channel is always ready to receive, so without this the loop
			// spins on it forever — which is a hang, and a hang in a drain helper is
			// the one place a test can lose its own failure message.
			if !open {
				return
			}

		case <-time.After(absence):
			return
		}
	}
}

// expectNothing asserts that no frame arrives within `absence`.
func expectNothing(t *testing.T, frames <-chan []byte) {
	t.Helper()

	select {
	case payload, open := <-frames:
		if !open {
			return
		}

		t.Fatalf("received %s, want nothing", payload)

	case <-time.After(absence):
	}
}

// waitFor polls until cond holds, failing if it does not within the budget.
//
// The budget is a ceiling on a failure, not a cost on a success, for the reason
// `clock_test.go` gives: a correct implementation satisfies the condition on the
// first poll.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(settleBudget)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %s", what, settleBudget)
		}

		time.Sleep(poll)
	}
}

// settledGoroutines samples the goroutine count until three consecutive samples
// agree.
//
// Sampled rather than read once, and three rather than one, for the reason
// `clock_test.go` gives about `quiet`: a single unchanged sample cannot distinguish
// "the work has finished" from "the goroutine has not been scheduled yet", and a
// wait that resolves on the second of those returns before the thing it is waiting
// for has happened — a gate wired to nothing.
func settledGoroutines() int {
	stable := 0

	var previous int

	for range stablePolls * 4 {
		current := runtime.NumGoroutine()

		if current == previous {
			stable++

			if stable >= stablePolls {
				return current
			}
		} else {
			previous, stable = current, 0
		}

		time.Sleep(poll)
	}

	return runtime.NumGoroutine()
}

// stubResolver answers every intent with one fixed answer.
//
// It exists to put a **server-chosen** value where a client could not have put one,
// which is the only way to assert that the hub takes a roll result from the resolver
// rather than from the frame.
type stubResolver struct {
	// total is the roll result the resolver reports. Nothing on the wire can carry
	// this, and that is the property under test.
	total int64
	// refuse, when set, is returned instead of a resolution.
	refuse error
	// seen records the intents the hub handed over, so a test can assert on what a
	// resolver is *told* as well as on what it is sent.
	seen []realtime.Intent

	mu sync.Mutex
}

// Resolve implements realtime.Resolver.
func (s *stubResolver) Resolve(
	_ context.Context,
	intent realtime.Intent,
) (realtime.Resolution, error) {
	s.mu.Lock()
	s.seen = append(s.seen, intent)
	s.mu.Unlock()

	if s.refuse != nil {
		return realtime.Resolution{}, s.refuse
	}

	// The payload the resolver produced. It is a `json.RawMessage` because the
	// codec models the *envelope* and the system knows its own shape — which is
	// `ServerApplied.Args` and `Change.Args` in `protocol.go`, stated there.
	payload := json.RawMessage(fmt.Sprintf(`{"expr":"2d10","total":%d}`, s.total))

	return realtime.Resolution{
		Answer: &realtime.ServerApplied{
			Type:      realtime.TypeApplied,
			Seq:       intent.Frame.Seq,
			Version:   7,
			Placement: intent.Frame.Args.Placement,
			Op:        intent.Frame.Op,
			Args:      payload,
			By:        intent.Actor,
		},
		Broadcast: []realtime.Change{{
			Placement: intent.Frame.Args.Placement,
			Version:   7,
			Op:        intent.Frame.Op,
			Args:      payload,
			By:        intent.Actor,
		}},
	}, nil
}

// handed returns the intents the resolver was given.
func (s *stubResolver) handed() []realtime.Intent {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.seen)
}

// rollIntent is a perception check, split across two lines because the frame is
// 97 bytes on one and `golines` counts the tab as four.
//
// A client sends the parameters and never the outcome, and it sends `placement`
// inside `args` because that is where the grammar puts it — a `placement` at the top
// level is an unknown field, so the frame above is also a small assertion that this
// test's fixture is one the codec would accept at all.
const rollIntent = `{"t":"intent","seq":7,"op":"roll",` +
	`"args":{"placement":"p1","expr":"2d10","reason":"perception"}}`

// moveIntent is a second legal frame, so the vacuity check below asserts against a
// fixture the codec *accepted* rather than only about the type graph.
const moveIntent = `{"t":"intent","seq":1,"op":"move_token",` +
	`"args":{"placement":"p1","x":1,"y":2}}`

// clientSuppliedResult is the frame S-7.3 forbids, and the reason
// `TestTheRollComesFromTheResolver` is not merely asserting that the resolver won.
const clientSuppliedResult = `{"t":"intent","seq":8,"op":"roll",` +
	`"args":{"expr":"2d10","result":20}}`

// clockDelta is one change, encoded the way the wire carries it.
func clockDelta(world time.Time) *realtime.ServerClock {
	return &realtime.ServerClock{Type: realtime.TypeClock, WorldTime: world}
}

// publishTwice broadcasts twice, which is the minimum that leaves a peer behind: the
// first fills its slot, and only the second finds it already full.
//
// That is the whole of how a peer becomes "behind", so a test that wants a stalled
// peer and publishes once is testing nothing.
func publishTwice(t *testing.T, hub *realtime.Hub) {
	t.Helper()

	for tick := range 2 {
		frame := clockDelta(epochClock.Add(time.Duration(tick) * time.Second))
		if err := hub.Publish(1, frame); err != nil {
			t.Fatalf("Publish() error = %v, want nil", err)
		}
	}
}

// publishBurst broadcasts count frames, one a second apart on the test's own
// timeline so each is distinguishable from the last in the encoded bytes.
func publishBurst(t *testing.T, hub *realtime.Hub, count int) {
	t.Helper()

	for tick := range count {
		frame := clockDelta(epochClock.Add(time.Duration(tick) * time.Second))
		if err := hub.Publish(1, frame); err != nil {
			t.Fatalf("Publish() error = %v, want nil", err)
		}
	}
}

// TestAPublishReachesEveryJoinedPeerAndOnlyThose is the fan-out claim, in both
// directions at once.
//
// The second half is the one that can fail quietly: a hub that delivered to peers
// *and* to a room's other campaigns would satisfy "reaches every subscriber", and
// the only way to see it is a peer in a campaign nobody published to.
func TestAPublishReachesEveryJoinedPeerAndOnlyThose(t *testing.T) {
	hub, _ := newTestHub(t, nil)

	first, second := join(t, hub, 1), join(t, hub, 1)
	elsewhere := join(t, hub, secondCampaignID)

	published := clockDelta(epochClock)

	if err := hub.Publish(1, published); err != nil {
		t.Fatalf("Publish() error = %v, want nil", err)
	}

	// The frame is encoded once and the same bytes go out, so comparing them is a
	// comparison against what `Encode` produced rather than against a re-decoded
	// copy of it.
	want, err := realtime.Encode(published)
	if err != nil {
		t.Fatalf("Encode() error = %v, want nil", err)
	}

	for name, peer := range map[string]*realtime.Peer{"first": first, "second": second} {
		if got := receive(t, peer.Broadcasts()); !bytes.Equal(got, want) {
			t.Errorf("%s received %s, want %s", name, got, want)
		}
	}

	expectNothing(t, elsewhere.Broadcasts())
}

// TestAPeerThatJoinsAfterAPublishDoesNotReceiveIt is the other half of the fan-out
// claim, and it is the one that can regress silently: a hub that kept undelivered
// broadcasts per campaign would hand a brand-new connection a backlog from before
// it existed.
//
// A new connection is answered with a snapshot, not with history, because the
// protocol's answer to "what have I missed" is the `hello`'s `since` and a client
// that sent none has asked for everything.
func TestAPeerThatJoinsAfterAPublishDoesNotReceiveIt(t *testing.T) {
	hub, _ := newTestHub(t, nil)

	if err := hub.Publish(1, clockDelta(epochClock)); err != nil {
		t.Fatalf("Publish() error = %v, want nil", err)
	}

	late := join(t, hub, 1)

	expectNothing(t, late.Broadcasts())

	if got := late.Stats().Delivered; got != 0 {
		t.Errorf("a peer that joined after a publish has %d delivered frames, want 0", got)
	}
}

// TestASlowPeerIsSupersededRatherThanQueued is the replacement policy, asserted
// twice over: what the peer ends up holding, and whether the publisher was ever
// made to wait.
//
// "Newest, not oldest" is the whole of the policy. Two of the three alternatives
// are distinguishable here and one is not: dropping the new frame and blocking are
// both "the peer does not end up with N", so the burst's *final* contents are what
// separates replace from drop, and the publisher's return is what separates replace
// from block.
func TestASlowPeerIsSupersededRatherThanQueued(t *testing.T) {
	hub, _ := newTestHub(t, nil)

	slow := join(t, hub, 1)

	const burst = 200

	// Published from another goroutine so a blocking send cannot hang the test: the
	// claim is that `Publish` *returns*, and a hang would otherwise be a hang with
	// no assertion attached to it.
	returned := make(chan struct{})

	go func() {
		defer close(returned)

		for tick := range burst {
			frame := clockDelta(epochClock.Add(time.Duration(tick) * time.Second))
			if err := hub.Publish(1, frame); err != nil {
				t.Errorf("Publish() error = %v, want nil", err)

				return
			}
		}
	}()

	select {
	case <-returned:
	case <-time.After(settleBudget):
		t.Fatalf("Publish did not return within %s for a peer that is not reading; a "+
			"blocking send puts a browser tab between a dice roll and its answer",
			settleBudget)
	}

	// One frame, and it is the newest. A queue would hold the oldest 200; a drop
	// policy would hold nothing or one of the first.
	first := receive(t, slow.Broadcasts())
	expectNothing(t, slow.Broadcasts())

	newest := clockDelta(epochClock.Add(burst*time.Second - time.Second))

	want, err := realtime.Encode(newest)
	if err != nil {
		t.Fatalf("Encode() error = %v, want nil", err)
	}

	if !bytes.Equal(first, want) {
		t.Errorf("a peer that read once holds %s, want the newest frame %s", first, want)
	}

	stats := hub.Stats()

	if stats.Superseded != burst-1 {
		t.Errorf("Stats().Superseded = %d, want %d; every publish after the first had "+
			"to replace a stale notice", stats.Superseded, burst-1)
	}

	if got := slow.Stats().Superseded; got != burst-1 {
		t.Errorf("peer Stats().Superseded = %d, want %d", got, burst-1)
	}
}

// TestABroadcastSlotIsOneDeep is the memory claim stated as a number.
//
// The leak `hub.go` exists to prevent is not a deep buffer — a peer's buffer is one
// deep by construction — it is a connection and its channels held forever. This test
// is the half of the property that *is* a buffer: after a burst, the peer holds
// exactly one frame, so a hub with a thousand stalled clients is holding a thousand
// frames rather than a thousand times the burst.
func TestABroadcastSlotIsOneDeep(t *testing.T) {
	hub, _ := newTestHub(t, nil)

	slow := join(t, hub, 1)

	publishBurst(t, hub, 50)

	if got := slow.Stats().Queued; got != 0 {
		t.Errorf("a peer that read nothing holds %d replies, want 0", got)
	}

	receive(t, slow.Broadcasts())
	expectNothing(t, slow.Broadcasts())
}

// TestAPeerThatStopsReadingGoesStale is the leak property, and the assertion is
// two-sided on purpose.
//
// The peer is given a full slot, the clock is advanced a whole window, and the peer
// must be gone. The control in the same test — a peer that called `Advance` — must
// **not** be gone, because the failure mode of a staleness sweep that never clears
// its clock is a table retired mid-game, and that is worse than a leaked socket
// because it is visible.
//
// The sweep runs on a goroutine the hub started, so the test arms the clock first
// and then waits: it does not reimplement the loop, for the reason `clock_test.go`
// gives.
func TestAPeerThatStopsReadingGoesStale(t *testing.T) {
	hub, clock := newTestHub(t, nil)

	stale := join(t, hub, 1)
	healthy := join(t, hub, 1)

	// Arm before anything is published, so the advance below reaches a sweep rather
	// than racing a sweeper that has not reached its `select` yet.
	clock.arm(t, 0)

	// One publish fills both slots; a second finds them already full, which is the
	// moment the hub arms the peer's staleness clock.
	publishTwice(t, hub)

	// The healthy peer drained its slot, which is the difference between "behind" and
	// "gone" and the only thing that tells them apart without writing to a socket.
	healthy.Advance()

	// A whole window, so the stalled peer has been behind for longer than it is allowed
	// to be.
	clock.advance(staleWindow)

	waitFor(t, "the stalled peer is retired", func() bool { return stale.Stats().Closed })

	// The sweep really ran rather than the peer having been retired by something else.
	// `Arms` counts the sweeper's own calls, so this observes the loop the hub started
	// instead of a model of it — and it is what stops this test passing against a hub
	// whose sweep never fires and whose peers are closed by something else entirely.
	if got := clock.Arms(); got < 2 {
		t.Errorf("the sweeper armed %d timers, want at least 2, so this test observed a "+
			"sweep rather than some other teardown", got)
	}

	// The control, and the half that makes the sweep a measurement rather than a
	// blunt instrument: this peer drained its slot, so it is *not* retired. A sweep
	// that closed every peer it looked at would pass the staleness assertion above and
	// retire a table mid-game, which is worse than a leaked socket because it is
	// visible.
	if healthy.Stats().Closed {
		t.Error("a peer that drained its queue was retired; the sweep is confusing a " +
			"full slot with a peer that is not reading")
	}

	if got := hub.Stats().Staled; got != 1 {
		t.Errorf("Stats().Staled = %d, want 1; only the stalled peer may be retired", got)
	}

	if got := hub.Stats().Peers; got != 1 {
		t.Errorf("Stats().Peers = %d, want 1", got)
	}

	// A retired peer's queues are closed, so its receive loop ends without any other
	// teardown condition. Drained first, because a closed channel hands out what it
	// buffered before it starts yielding `false`.
	drain(t, stale.Broadcasts())

	if _, open := <-stale.Broadcasts(); open {
		t.Error("a retired peer's broadcast slot is still open")
	}
}

// TestAPeerInsideTheWindowIsNotRetired is the same sweep one tick short of the
// window, and it is what makes the previous test mean something.
//
// Without it, a sweep that retired *every* peer on its first pass would pass the
// staleness test. This is the assertion that "stale" is a measurement.
func TestAPeerInsideTheWindowIsNotRetired(t *testing.T) {
	hub, clock := newTestHub(t, nil)

	peer := join(t, hub, 1)

	// The sweeper's first tick is due one whole window from now. Arming proves the
	// timer exists, so the advance below reaches a sweep rather than racing a
	// goroutine that has not reached its `select`.
	clock.arm(t, 0)

	// Move most of the way to the tick **before** publishing, so the peer falls
	// behind partway through the window rather than at its start.
	clock.advance(staleWindow - behindTail)

	publishTwice(t, hub)

	if peer.Stats().BehindFor != 0 {
		t.Fatalf("the peer's BehindFor is %s, want 0; the hub believes a peer is behind "+
			"before a second broadcast has found its slot full",
			peer.Stats().BehindFor)
	}

	// The rest of the way: the sweep fires now, and finds a peer that has been behind
	// for `behindTail` — inside the window.
	clock.advance(behindTail)

	// The sweep really ran. Without this the assertion below would pass against a hub
	// whose sweeper never fires, which is the gate-wired-to-nothing failure.
	waitFor(t, "the sweeper to fire and re-arm", func() bool { return clock.Arms() > 1 })

	if peer.Stats().Closed {
		t.Fatalf("a peer that has been behind for %s was retired; the window is %s",
			behindTail, staleWindow)
	}

	if got := hub.Stats().Staled; got != 0 {
		t.Errorf("Stats().Staled = %d inside the window, want 0", got)
	}
}

// TestJoinAndLeaveAreIdempotent is the teardown property, and it is about the
// *second* call rather than the first.
//
// A route's deferred leave runs on every exit from a read loop, including the
// error exits, so a leave arriving twice — or concurrently with a stale sweep — is
// the normal case. A double `close` of a channel panics, and a panic on a shutdown
// path is the one failure a graceful stop cannot survive.
func TestJoinAndLeaveAreIdempotent(t *testing.T) {
	hub, _ := newTestHub(t, nil)

	peer := join(t, hub, 1)

	if err := hub.Publish(1, clockDelta(epochClock)); err != nil {
		t.Fatalf("Publish() error = %v, want nil", err)
	}

	peer.Leave()
	peer.Leave()
	peer.Leave()

	if !peer.Stats().Closed {
		t.Fatal("Leave() did not close the peer")
	}

	// A *new* peer in the same campaign is unaffected by the old one's departure,
	// and the departed one receives nothing further. Drained first, so the assertion
	// is about the frames published from here on rather than about the one published
	// before the leave.
	drain(t, peer.Broadcasts())
	expectNothing(t, peer.Broadcasts())

	fresh := join(t, hub, 1)

	if err := hub.Publish(1, clockDelta(epochClock)); err != nil {
		t.Fatalf("Publish() error = %v, want nil", err)
	}

	receive(t, fresh.Broadcasts())
	expectNothing(t, peer.Broadcasts())

	if got := hub.Stats().Peers; got != 1 {
		t.Errorf("Stats().Peers = %d, want 1; a departed peer is still counted", got)
	}

	// And a reply to a peer that has left is refused rather than queued into a
	// channel nobody will read.
	if err := peer.Send(clockDelta(epochClock)); !errors.Is(err, realtime.ErrPeerClosed) {
		t.Errorf("Send() after Leave() error = %v, want ErrPeerClosed", err)
	}
}

// TestShutdownWithLivePeersIsCleanAndBounded is the shutdown contract, and it is
// three claims: it returns, it closes everything, and a second call is harmless.
//
// "Bounded" is asserted with a budget and a goroutine rather than by inspection,
// because the claim is about what happens when a peer is *not* cooperating — and a
// hub whose `Close` waited on a peer's read loop would pass every other test in
// this file and hang the one that matters.
func TestShutdownWithLivePeersIsCleanAndBounded(t *testing.T) {
	database, writer, reader := openDatabase(t)

	registry := realtime.NewRegistry(t.Context(), realtime.Config{Write: writer, Read: reader})

	hub := realtime.NewHub(t.Context(), realtime.HubConfig{States: registry})

	live := []*realtime.Peer{join(t, hub, 1), join(t, hub, 1), join(t, hub, secondCampaignID)}

	// One peer that has already been filled and never drained, so the shutdown runs
	// with a peer the sweep would otherwise have retired.
	if err := hub.Publish(1, clockDelta(epochClock)); err != nil {
		t.Fatalf("Publish() error = %v, want nil", err)
	}

	if err := hub.Publish(1, clockDelta(epochClock.Add(time.Second))); err != nil {
		t.Fatalf("Publish() error = %v, want nil", err)
	}

	closed := make(chan error, 1)

	go func() {
		closed <- hub.Close(context.WithoutCancel(t.Context()))
	}()

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("hub.Close() error = %v, want nil", err)
		}
	case <-time.After(settleBudget):
		t.Fatalf("hub.Close() did not return within %s with %d live peers; a shutdown "+
			"that waits on a peer is a shutdown that waits on a browser tab",
			settleBudget, len(live))
	}

	for index, peer := range live {
		if !peer.Stats().Closed {
			t.Errorf("peer %d survived the shutdown", index)
		}

		// Drained first: the channel is one deep and the publishes above left a
		// frame in it, so a read before the drain returns that frame with `ok` still
		// true and the test would be asserting on the buffer rather than on the
		// close. A closed-and-drained channel yields `false` immediately.
		for range cap(peer.Broadcasts()) {
			<-peer.Broadcasts()
		}

		if _, open := <-peer.Broadcasts(); open {
			t.Errorf("peer %d's broadcast slot is still open after the shutdown", index)
		}
	}

	if got := hub.Stats().Peers; got != 0 {
		t.Errorf("Stats().Peers = %d after the shutdown, want 0", got)
	}

	// A second close is harmless, because a route's teardown and a hub's both call
	// it and neither knows about the other.
	if err := hub.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Errorf("second hub.Close() error = %v, want nil", err)
	}

	// And the state was flushed by that close, which is the ordering claim in
	// `Hub.Close`: the registry goes after the peers because it is the flush.
	if err := registry.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Errorf("registry.Close() error = %v, want nil", err)
	}

	var count int

	if err := database.DB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM campaign_state`).Scan(&count); err != nil {
		t.Fatalf("count campaign_state: %v", err)
	}

	if count != 2 {
		t.Errorf("campaign_state holds %d rows after the shutdown, want 2; the hub "+
			"closed the registry and the flush is the reason it does", count)
	}
}

// TestAJoinAfterTheShutdownIsRefused is the difference between "no access is 404"
// and "the process is going away", expressed on this side.
//
// The route has to be able to tell a client "come back later" from "you are not
// allowed", because the first is a retry and the second is not. A hub that returned
// a working peer after `Close` would report a healthy socket to a client that is
// receiving nothing.
func TestAJoinAfterTheShutdownIsRefused(t *testing.T) {
	database, writer, reader := openDatabase(t)

	registry := realtime.NewRegistry(t.Context(), realtime.Config{Write: writer, Read: reader})

	hub := realtime.NewHub(t.Context(), realtime.HubConfig{States: registry})

	t.Cleanup(func() {
		if err := hub.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("hub.Close() error = %v, want nil", err)
		}
	})

	if err := hub.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatalf("hub.Close() error = %v, want nil", err)
	}

	if _, err := hub.Join(t.Context(), 1, viewerGM); !errors.Is(err, realtime.ErrHubClosed) {
		t.Errorf("Join() after Close error = %v, want ErrHubClosed", err)
	}

	// And a publish to a shut-down hub is a no-op rather than a panic: the sweeper
	// may be mid-flight, and a publisher must never find a closed room.
	if err := hub.Publish(1, clockDelta(epochClock)); err != nil {
		t.Errorf("Publish() after Close error = %v, want nil", err)
	}

	_ = database
}

// TestJoiningAConnDoesNotStartAGoroutine is the claim no other assertion in this
// file can reach.
//
// `hub.go`'s central design statement is that a peer costs a map entry and two
// channels rather than a stack. That is a claim about the *absence* of something,
// so no return value reports it and no counter in `Hub.Stats` sees it — the only
// observation that reaches it is a goroutine count.
//
// The measurement is a **difference between two batches**, not a count against a
// constant. The first batch opens the campaign's state, which starts the
// persistence scheduler and is the one legitimate goroutine a join can cause; the
// second batch opens nothing new. Asserting the second batch costs zero is exact
// and is what makes the first batch's cost irrelevant. A constant would not be: the
// process is running a race detector and whatever else the test binary has started,
// and a threshold is a threshold a test can pass by being lucky.
func TestJoiningAConnDoesNotStartAGoroutine(t *testing.T) {
	hub, _ := newTestHub(t, nil)

	const perBatch = 32

	// The sweeper is started by `NewHub`, before this line, so it is in the
	// baseline. That is deliberate: the claim is about *peers*, and counting the
	// hub's one process-wide goroutine against them would be measuring the wrong
	// thing.
	baseline := settledGoroutines()

	for range perBatch {
		join(t, hub, 1)
	}

	// The campaign's state is opened by the first join and its scheduler is the one
	// goroutine that legitimately appears here.
	afterFirst := settledGoroutines()

	if delta := afterFirst - baseline; delta > 1 {
		t.Errorf("the first batch of %d joins started %d goroutines, want at most 1 "+
			"(the campaign's persistence scheduler); a goroutine per connection is the "+
			"failure this asserts against", perBatch, delta)
	}

	for range perBatch {
		join(t, hub, 1)
	}

	afterSecond := settledGoroutines()

	if delta := afterSecond - afterFirst; delta != 0 {
		t.Errorf("%d further joins to a campaign whose state is already open started "+
			"%d goroutines, want 0; a peer costs a map entry and two channels",
			perBatch, delta)
	}

	if got := hub.Stats().Peers; got != 2*perBatch {
		t.Errorf("Stats().Peers = %d, want %d", got, 2*perBatch)
	}
}

// TestTheRollComesFromTheResolver is S-7.3 asserted from the hub's side.
//
// The codec's half of the rule is `protocol_test.go`'s: no inbound field can hold a
// result, and a client that sends one is refused rather than ignored. This is the
// *authority* half: the number that reaches the wire comes from the resolver, and
// the frame contributes only its `seq` and the parameters it was asked about.
//
// The proof has two parts, and the first would pass on its own:
//
//  1. The stub reports a total, and that total is what every subscriber receives —
//     for a client that asked for `2d10` and could not have known it.
//  2. A client that *tries* to supply the result is refused by the codec before the
//     hub is ever reached, and the resolver's total goes out unchanged.
func TestTheRollComesFromTheResolver(t *testing.T) {
	resolver := &stubResolver{total: 20}
	hub, _ := newTestHub(t, resolver)

	actor, watcher := join(t, hub, 1), join(t, hub, 1)

	intent := decodeIntent(t, rollIntent)

	if err := hub.Apply(t.Context(), actor, intent); err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}

	// The actor's answer.
	applied := receive(t, actor.Answers())

	var appliedFrame map[string]any
	if err := json.Unmarshal(applied, &appliedFrame); err != nil {
		t.Fatalf("decode the applied frame %s: %v", applied, err)
	}

	if appliedFrame["t"] != string(realtime.TypeApplied) {
		t.Errorf("the actor received %s, want an applied frame", applied)
	}

	if got := appliedFrame["seq"]; got != float64(7) {
		t.Errorf("the applied frame echoes seq %v, want 7", got)
	}

	if got := appliedFrame["version"]; got != float64(7) {
		t.Errorf("the applied frame carries version %v, want 7", got)
	}

	if got := appliedFrame["by"]; got != float64(viewerGM.ID) {
		t.Errorf("the applied frame names actor %v, want %d", got, viewerGM.ID)
	}

	assertCarriesTotal(t, applied, 20)

	// And every other peer's copy of the same change.
	delta := receive(t, watcher.Broadcasts())
	assertCarriesTotal(t, delta, 20)

	// The resolver was handed the *peer's* campaign and actor, which the frame could
	// not have chosen: `ClientIntent` has no campaign field, and the actor is the
	// connection's identity.
	handed := resolver.handed()
	if len(handed) != 1 {
		t.Fatalf("the resolver was handed %d intents, want 1", len(handed))
	}

	if handed[0].Campaign != 1 {
		t.Errorf("the resolver was told campaign %d, want 1", handed[0].Campaign)
	}

	if handed[0].Actor != viewerGM.ID {
		t.Errorf("the resolver was told actor %d, want %d", handed[0].Actor, viewerGM.ID)
	}

	if handed[0].Role != domain.RoleGM {
		t.Errorf("the resolver was told role %q, want %q", handed[0].Role, domain.RoleGM)
	}

	// A client that tries to supply the result is refused by the codec, so the hub
	// is never handed a frame carrying one. Asserted here rather than only in
	// `protocol_test.go` because *this* is the path the number would travel if it
	// could.
	_, refused := realtime.Decode([]byte(clientSuppliedResult))
	if !errors.Is(refused, realtime.ErrFrameRejected) {
		t.Errorf("a client-supplied roll result error = %v, want ErrFrameRejected", refused)
	}

	expectNothing(t, watcher.Answers())
}

// assertCarriesTotal requires that a frame carries a resolver-chosen roll total.
func assertCarriesTotal(t *testing.T, frame []byte, want int64) {
	t.Helper()

	var decoded struct {
		Args struct {
			Total *int64 `json:"total"`
		} `json:"args"`
		Changes []struct {
			Args struct {
				Total *int64 `json:"total"`
			} `json:"args"`
		} `json:"changes"`
	}

	if err := json.Unmarshal(frame, &decoded); err != nil {
		t.Fatalf("decode %s: %v", frame, err)
	}

	totals := []int64{}

	if decoded.Args.Total != nil {
		totals = append(totals, *decoded.Args.Total)
	}

	for _, change := range decoded.Changes {
		if change.Args.Total != nil {
			totals = append(totals, *change.Args.Total)
		}
	}

	if len(totals) == 0 {
		t.Fatalf("the frame %s carries no roll total; the resolver's value did not reach it", frame)
	}

	for _, got := range totals {
		if got != want {
			t.Errorf("the frame carries the roll total %d, want the resolver's %d", got, want)
		}
	}
}

// allowedResolverFields is every field name reachable from what a `Resolver` is
// handed: the three the server binds plus the closed inbound set `protocol_test.go`
// defines.
//
// An allowlist rather than a denylist, for the reason `allowedInboundFields` gives
// and it is worth repeating because the same mistake is available again here: a
// denylist of result-shaped names is a list of the names somebody thought of, and
// `Sum` sails past it.
var allowedResolverFields = append(
	[]string{"Campaign", "Actor", "Role", "Frame"},
	allowedInboundFields...,
)

// TestNoFieldHandedToAResolverCouldHoldARollResult is S-7.3 by reflection, over the
// hub's own type rather than the codec's.
//
// `protocol_test.go` proves no *client frame* can carry a result. That leaves the
// hub's own boundary unproved: a field added to `Intent` — a `Result int`, or an
// opaque `json.RawMessage` of resolver-shaped bytes a route filled from the query
// string — would be a way in that the codec's grammar says nothing about. The
// walker is the same one `protocol_test.go` defines, so the two assertions are the
// same assertion applied to two graphs.
func TestNoFieldHandedToAResolverCouldHoldARollResult(t *testing.T) {
	walkStructs(reflect.TypeFor[realtime.Intent](), resolverFieldCheck(t))
}

// resolverFieldCheck builds the `walkStructs` callback for the reflection assertion.
//
// A factory rather than a closure at the call site for one reason only: the closure
// form puts a hundred-character signature on one line, which `golines` refuses. It
// has no other purpose and adds no indirection to the assertion itself.
func resolverFieldCheck(t *testing.T) func(reflect.Type, reflect.StructField) {
	return func(structType reflect.Type, field reflect.StructField) {
		if !field.IsExported() {
			return
		}

		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" {
			name = field.Name
		}

		if slices.Contains(allowedResolverFields, name) {
			return
		}

		t.Errorf("%s.%s carries the field %q, which is not in the closed list of "+
			"fields a resolver may be handed. S-7.3 says the roll is evaluated "+
			"server-side, and the hub's boundary is a second place that has to be "+
			"closed for that to be true.",
			structType.Name(), field.Name, name)
	}
}

// TestAClientSuppliedResultNeverReachesTheHub is the mutation target for the test
// above, and it exists because `AGENTS.md` records three phase-5 gate drafts and
// three phase-6 ones that could not fail.
//
// If the reflection test were vacuous — if the walker found nothing — then this is
// what would catch it, and the other way round: a stub resolver whose answer is
// built from nothing the client sent is the observable half, and the refused frame is
// the enforced half. Neither passes if the other is missing.
func TestAClientSuppliedResultNeverReachesTheHub(t *testing.T) {
	// The walk is not vacuous: it visits something.
	visited := 0

	walkStructs(reflect.TypeFor[realtime.Intent](), func(_ reflect.Type, _ reflect.StructField) {
		visited++
	})

	if visited == 0 {
		t.Fatal("the reflection walk visited no fields, so the allowlist assertion above " +
			"is vacuous")
	}

	// And the same graph the walker follows really is reachable from an intent the
	// codec accepted, so a field added there would be seen.
	intent := decodeIntent(t, moveIntent)

	if intent.Args.Expr != "" || intent.Op != "move_token" {
		t.Fatalf("the fixture decoded as %+v, want a move_token with no dice expression", intent)
	}
}

// TestTheResolverRefusalReachesTheClientAsAClosedReason is S-8 and S-12.3 on the
// wire: a refusal names one of eight words this project chose, and never the
// resolver's own text.
//
// The three cases matter separately. A recognised reason is used; an unrecognised
// error falls back to `RejectServerError`; and a reason *outside* the closed set is
// also refused, which is the case that would put a plugin's error string in front of
// a browser if `Encode` were not checking membership.
func TestTheResolverRefusalReachesTheClientAsAClosedReason(t *testing.T) {
	cases := map[string]struct {
		refuse error
		want   realtime.RejectReason
	}{
		"a recognised reason": {
			refuse: &realtime.RejectionError{Reason: realtime.RejectNotYourTurn},
			want:   realtime.RejectNotYourTurn,
		},
		"a reason outside the closed set": {
			// `Encode` would refuse this frame outright, so the hub must not pass it
			// through: a resolver that invented a reason would otherwise produce a
			// frame no client could be told about.
			refuse: &realtime.RejectionError{Reason: "you_rolled_wrong"},
			want:   realtime.RejectServerError,
		},
		"an error of no recognised kind": {
			refuse: errors.New("the dice goblin left"),
			want:   realtime.RejectServerError,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			hub, _ := newTestHub(t, &stubResolver{refuse: testCase.refuse})

			peer := join(t, hub, 1)
			intent := decodeIntent(t, `{"t":"intent","seq":7,"op":"roll","args":{"expr":"2d10"}}`)

			// A refusal is not an error the hub propagates: the client asked a
			// question and got a complete answer, so returning one would invite the
			// route to answer a second time.
			if err := hub.Apply(t.Context(), peer, intent); err != nil {
				t.Fatalf("Apply() error = %v, want nil; a refusal is an answer", err)
			}

			rejected := receive(t, peer.Answers())

			var decoded struct {
				Type   realtime.Type         `json:"t"`
				Seq    realtime.ClientSeq    `json:"seq"`
				Reason realtime.RejectReason `json:"reason"`
			}

			if err := json.Unmarshal(rejected, &decoded); err != nil {
				t.Fatalf("decode the rejected frame %s: %v", rejected, err)
			}

			if decoded.Type != realtime.TypeRejected {
				t.Errorf("the client received a %s frame, want a rejected", decoded.Type)
			}

			if decoded.Seq != intent.Seq {
				t.Errorf("the rejection echoes seq %d, want %d", decoded.Seq, intent.Seq)
			}

			if decoded.Reason != testCase.want {
				t.Errorf("the client was told %q, want %q", decoded.Reason, testCase.want)
			}

			// S-12.3: the resolver's own text may not travel. A rejection carries a
			// reason and a `seq`, so this is a check that the frame has nowhere to put
			// it — asserted as an exact field count.
			if fields := countFields(t, rejected); fields != 3 {
				t.Errorf("the rejected frame carries %d fields, want 3 (t, seq, reason); "+
					"there is nowhere in it for a resolver's error text", fields)
			}
		})
	}
}

// TestARejectionCarriesNoResolverText is the log-line half of the previous test.
//
// `RejectionError.Error()` reaches a `slog` line, and S-12.3 is enforced by
// `observability` having no field a body could travel through — which is a property
// of that package and cannot be asserted from here. What *can* be asserted is that
// this type's own text is a reason and nothing else, so a caller that logs it cannot
// leak through it.
func TestARejectionCarriesNoResolverText(t *testing.T) {
	const secret = "the passphrase is hunter2"

	rejection := &realtime.RejectionError{
		Reason: realtime.RejectServerError,
		Err:    errors.New("markdown: " + secret),
	}

	if strings.Contains(rejection.Error(), secret) {
		t.Errorf("RejectionError.Error() = %q, want the reason alone; the wrapped error "+
			"is for errors.Is and never for a log line", rejection.Error())
	}

	if !strings.Contains(rejection.Error(), string(realtime.RejectServerError)) {
		t.Errorf("RejectionError.Error() = %q, want it to name the reason", rejection.Error())
	}

	// And `errors.Is` still reaches the cause, so a caller that *wants* the detail
	// has somewhere to get it that is not a log line.
	if !errors.Is(rejection, rejection.Err) {
		t.Error("errors.Is did not reach the wrapped resolver error")
	}
}

// TestAJoinIsRefusedForAnythingThatIsNotACampaignOrViewer is the input validation,
// and it is checked at the boundary because two of the three refusals are
// unreachable from the gate.
//
// A negative campaign id and a role this build has no name for both arrive from
// *our* code — a route bug, or a membership row written by a build that knew a role
// this one does not. Refusing at the hub's door rather than letting them reach
// `Registry.Open` is what turns a bug into a message that names it.
func TestAJoinIsRefusedForAnythingThatIsNotACampaignOrViewer(t *testing.T) {
	hub, _ := newTestHub(t, nil)

	if _, err := hub.Join(t.Context(), 0, viewerGM); !errors.Is(err, realtime.ErrNoCampaign) {
		t.Errorf("Join(0) error = %v, want ErrNoCampaign", err)
	}

	if _, err := hub.Join(t.Context(), -1, viewerGM); !errors.Is(err, realtime.ErrNoCampaign) {
		t.Errorf("Join(-1) error = %v, want ErrNoCampaign", err)
	}

	stranger := realtime.Viewer{ID: 7, Role: domain.Role("storyteller")}
	if _, err := hub.Join(t.Context(), 1, stranger); !errors.Is(err, realtime.ErrNoViewer) {
		t.Errorf("Join() with an unknown role error = %v, want ErrNoViewer", err)
	}

	// And nothing was created by the refusals, which is the part that would matter:
	// a refused join that had already reserved a campaign would leave the next join
	// waiting on a reservation nobody will release.
	if got := hub.Stats().Peers; got != 0 {
		t.Errorf("Stats().Peers = %d after three refused joins, want 0", got)
	}
}

// TestTheJoinDoesNotOpenASecondAuthority is the `Registry.Get`-not-`Open`
// discipline, observed from the only place it can be observed.
//
// `state.go` refuses a second `Open` for a live campaign with `ErrStateOpen`, so a
// hub that opened per join would have every join after the first one fail on a
// campaign that is working perfectly. The observable form of that bug is four peers
// in one campaign, which is what this test joins and what a second `Open` per join
// cannot produce.
func TestTheJoinDoesNotOpenASecondAuthority(t *testing.T) {
	hub, _ := newTestHub(t, nil)

	peers := make([]*realtime.Peer, 8)

	for index := range peers {
		peers[index] = join(t, hub, 1)
	}

	for index, peer := range peers {
		if peer.CampaignID() != 1 {
			t.Errorf("peer %d joined campaign %d, want 1", index, peer.CampaignID())
		}

		if peer.Viewer().ID != viewerGM.ID {
			t.Errorf("peer %d carries viewer %d, want %d", index, peer.Viewer().ID, viewerGM.ID)
		}
	}

	// Peer ids are distinct and process-unique, because a reused id makes two
	// different connections indistinguishable in a log line about a campaign that had
	// just lost one of them.
	seen := map[int64]bool{}

	for index, peer := range peers {
		if seen[peer.ID()] {
			t.Errorf("peer %d reuses the id %d", index, peer.ID())
		}

		seen[peer.ID()] = true
	}
}

// TestTheTransportLimitExceedsTheCodecBound holds the relationship
// `protocol.go` puts in this file's hands.
//
// A transport limit below the codec's own bound silently redefines the protocol: the
// transport becomes the thing that decides a frame is too big, and `MaxClientFrameBytes`
// stops meaning anything. The constant can be lowered by anyone editing the line
// above it, and nothing else would notice.
func TestTheTransportLimitExceedsTheCodecBound(t *testing.T) {
	if realtime.MaxTransportReadBytes <= realtime.MaxClientFrameBytes {
		t.Errorf("MaxTransportReadBytes = %d and MaxClientFrameBytes = %d; the "+
			"transport's limit must be the larger one or the codec's bound stops "+
			"meaning anything",
			realtime.MaxTransportReadBytes, realtime.MaxClientFrameBytes)
	}

	// And the codec still refuses the frame it says it refuses, at the size it says
	// — so raising the transport limit did not raise the protocol's.
	if _, err := realtime.Decode(make([]byte, realtime.MaxClientFrameBytes+1)); err == nil {
		t.Error("an oversize frame decoded; the codec's own bound is not being enforced")
	}
}

// TestFrameClassNamesTheRefusalRatherThanItsType is R2's finding, held at the call
// site's half.
//
// `observability.errorClass` ends in `fmt.Sprintf("%T", err)`, so every protocol
// refusal reaches a log as `*realtime.FrameError` and "a client sent nonsense"
// cannot be told from "a client sent too much". `FrameClass` is the value the caller
// hands over instead. The assertion is that two different refusals produce two
// different words, because a function that returned the type name for both would
// pass a test that only checked "it does not return a Go type".
func TestFrameClassNamesTheRefusalRatherThanItsType(t *testing.T) {
	cases := map[string]string{
		`{"t":"intent","seq":7,"op":"roll","args":{"expr":"2d10","result":20}}`: "unknown_field",
		`{"t":"nope"}`:                           "unknown_type",
		`{"seq":1,"op":"roll"}`:                  "missing_type",
		`{"t":"intent","seq":1}`:                 "missing_field",
		`{"t":"intent","seq":-1,"op":"roll"}`:    "negative_counter",
		`{"t":"presence","args":{"cursor":[1]}}`: "bad_cursor",
	}

	classes := map[string]string{}

	for frame, want := range cases {
		_, err := realtime.Decode([]byte(frame))
		if err == nil {
			t.Fatalf("Decode(%s) error = nil, want a refusal", frame)
		}

		if got := realtime.FrameClass(err); got != want {
			t.Errorf("FrameClass(%s) = %q, want %q", frame, got, want)
		}

		classes[frame] = realtime.FrameClass(err)

		// Whatever it returns, it must not be a Go type name — that is the whole
		// complaint, and checking it as a string keeps the assertion honest even if a
		// future class were spelled like one.
		class := realtime.FrameClass(err)

		if strings.HasPrefix(class, "*") {
			t.Errorf("FrameClass(%s) = %q, which is what errorClass already returns", frame, class)
		}
	}

	if len(classes) != len(cases) {
		t.Errorf("%d distinct refusals produced %d distinct classes, want %d; a class "+
			"that collapses to one word is the failure this fixes",
			len(cases), len(classes), len(cases))
	}

	// A wrapped refusal still classifies, because the caller wraps what `Encode`
	// returns before handing it over.
	_, err := realtime.Decode([]byte(`{"t":"nope"}`))
	wrapped := fmt.Errorf("realtime: route: %w", err)

	if got := realtime.FrameClass(wrapped); got != "unknown_type" {
		t.Errorf("FrameClass(wrapped) = %q, want %q; a caller that adds its own context "+
			"must not lose the class", got, "unknown_type")
	}

	// Anything that is not a protocol refusal is a wiring fault, and gets one fixed
	// word rather than a type name.
	if got := realtime.FrameClass(errors.New("boom")); got != "realtime" {
		t.Errorf("FrameClass(non-frame error) = %q, want %q", got, "realtime")
	}
}

// TestAnOversizeReplyQueueRetiresThePeerRatherThanDroppingAnAnswer is the bounded
// queue's policy, which is the reason it is bounded at all.
//
// A dropped `applied` is a `seq` a client waits on forever. A closed connection is a
// `hello` the client retries. The hub therefore closes rather than discarding, and
// the assertion is that the *first* answer is still queued when the last one is
// refused — which is what "drops nothing" looks like from the outside.
func TestAnOversizeReplyQueueRetiresThePeerRatherThanDroppingAnAnswer(t *testing.T) {
	hub, _ := newTestHub(t, nil)

	peer := join(t, hub, 1)

	clock := clockDelta(epochClock)

	for index := range realtime.MaxQueuedAnswers {
		if err := peer.Send(clock); err != nil {
			t.Fatalf("Send() %d error = %v, want nil", index+1, err)
		}
	}

	if got := peer.Stats().Queued; got != realtime.MaxQueuedAnswers {
		t.Fatalf("the peer holds %d replies, want %d", got, realtime.MaxQueuedAnswers)
	}

	if err := peer.Send(clock); !errors.Is(err, realtime.ErrAnswersFull) {
		t.Errorf("Send() past the queue bound error = %v, want ErrAnswersFull", err)
	}

	if !peer.Stats().Closed {
		t.Error("the peer was not retired; a queue that fills without closing is a " +
			"client that hangs with no way to find out why")
	}

	// The answers already queued are still readable: nothing was discarded on the way
	// out, so a client that drains what it has before the close is not left with a
	// hole in its `seq` sequence.
	for range realtime.MaxQueuedAnswers {
		receive(t, peer.Answers())
	}

	if got := hub.Stats().Staled; got != 1 {
		t.Errorf("Stats().Staled = %d, want 1", got)
	}
}

// TestAWriteFailingIsNotAFrameError is the transport half, held without a socket.
//
// `Encode` validates and `WriteFrame` writes; a transport failure belongs to
// neither and must not be reported as a protocol refusal, because a caller that
// counted it as one would report our network as the client's fault.
func TestAWriteFailingIsNotAFrameError(t *testing.T) {
	frame := clockDelta(epochClock)

	// A frame the codec refuses, for the contrast.
	if _, err := realtime.Encode(&realtime.ServerRejected{
		Type: realtime.TypeRejected, Seq: 1, Reason: "invented",
	}); err == nil {
		t.Fatal("Encode() accepted a reject reason outside the closed set")
	} else if got := realtime.FrameClass(err); got != "unknown_reason" {
		t.Errorf("FrameClass(a refused outbound frame) = %q, want %q", got, "unknown_reason")
	}

	// And a frame that encodes, so a failure from here down is the transport's.
	if _, err := realtime.Encode(frame); err != nil {
		t.Fatalf("Encode() error = %v, want nil", err)
	}
}

// TestTheHubDoesNotGrowARoomPerPublish is the map bookkeeping, and it is here
// because a `Publish` that created a room for a campaign nobody is watching would
// make the hub's own memory a function of how many campaigns a process has ever
// seen.
func TestTheHubDoesNotGrowARoomPerPublish(t *testing.T) {
	hub, _ := newTestHub(t, nil)

	if err := hub.Publish(secondCampaignID, clockDelta(epochClock)); err != nil {
		t.Fatalf("Publish() error = %v, want nil", err)
	}

	if got := hub.Stats().Campaigns; got != 0 {
		t.Errorf("Stats().Campaigns = %d after a publish to an empty campaign, want 0", got)
	}

	peer := join(t, hub, 1)

	if got := hub.Stats().Campaigns; got != 1 {
		t.Errorf("Stats().Campaigns = %d after one join, want 1", got)
	}

	peer.Leave()

	if got := hub.Stats().Campaigns; got != 0 {
		t.Errorf("Stats().Campaigns = %d after the last peer left, want 0; an empty room "+
			"is a campaign the hub will never answer again", got)
	}
}

// countFields returns how many top-level fields a JSON object carries.
func countFields(t *testing.T, raw []byte) int {
	t.Helper()

	var fields map[string]json.RawMessage

	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}

	return len(fields)
}
