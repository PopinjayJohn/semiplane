package realtime_test

// Race coverage for the hub: the four interactions `hub_test.go` reaches one at a
// time, and which only exist under concurrency.
//
// # What the existing tests do not reach, and why it matters here
//
// `hub_test.go` is a strong suite and it is worth being precise about the gap
// rather than asserting one. Read for concurrency, it holds:
//
//   - `TestConcurrentJoinsToOneCampaignAllSucceed` fires sixteen goroutines at
//     `Join`, and it is the only test in the file that does. Every one of them
//     calls the same method.
//   - `TestAPeerThatStopsReadingGoesStale` does run the sweeper on the hub's own
//     goroutine, so `Publish` and `sweep` *can* overlap — but the publish happens
//     before the clock is advanced, so the interleaving the two lines allow is not
//     one the test arranges.
//   - `TestShutdownWithLivePeersIsCleanAndBounded` and
//     `TestAJoinAfterTheShutdownIsRefused` close a hub with nobody else running.
//
// So `h.mu` is the piece of this component that keeps everything else true, and
// **nothing in the suite removes it.** A `Publish` that read `h.rooms` without the
// lock, a `Send` that queued without it, a `sweep` that walked a room without it,
// a `detach` that double-closed a channel — every one of those passes every test
// in `hub_test.go`, because every one of them is a claim about what happens when
// two of the file's methods run at the same time and no test in the file runs two
// at once. That is the same failure `AGENTS.md` records three times over: a green
// gate over a synchronisation nobody exercised.
//
// # The three answers a concurrent shutdown may give
//
// The tests below accept a small closed set of errors from an operation that
// races `Close`, and nothing else. That set is not leniency — it is the contract,
// and it has three members because three orderings are possible:
//
//   - `nil`, the operation landed before the shutdown reached that peer.
//   - `ErrPeerClosed`, it landed after, and a retired peer is a closed connection
//     rather than a silently dropped frame (ADR 0009 rule 1).
//   - `ErrAnswersFull`, the peer's reply queue was already at its bound, which
//     retires the connection from `Send` itself.
//
// Anything else — and above all a **panic** — is the defect. A send on a closed
// channel is the one this file most exists to catch: it arrives in whichever
// goroutine published a delta, which is a resolver returning a dice roll rather
// than anywhere it can be recovered.
//
// # No `t.Parallel`, and the same reason `hub_test.go` gives
//
// `store.Open` claims one Store per process (ADR 0004) and every fixture here goes
// through `openDatabase`. A parallel test here would hold that slot while the
// package's parallel protocol and ruleset tests ran, and the failure would be
// reported against a correctness property of the product.
//
// # One property with no external observation, recorded rather than faked
//
// "Exactly one authority per campaign" cannot be asserted by counting opens: a
// hub that called `Open` on every join would produce the same **one** committed
// write, because `state.go` refuses the second `Open` from `reserve` before it
// reads or writes anything. `mutate-hub.sh` records this as expected survivor
// number two. Nothing below pretends to hold it; what these tests hold instead is
// the part that *is* observable — that the fan-out, the peer lifetime and the
// sweep stay consistent while the set of peers changes under them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/realtime"
)

// newRaceHub is `newTestHubWith` with a caller-supplied `HubClock`.
//
// It exists because the fixture there hard-codes the fake clock, and one of the
// tests below needs the **real** one: the point of that test is that the sweeper
// runs while the test's own goroutines are joining and leaving, and a fake clock
// only sweeps when a test advances it — which would make "the sweep ran" an
// arrangement rather than an observation. Everything else is that fixture,
// restated rather than refactored because it is shared by eleven other tests and
// a signature change to reach one clock would touch all of them.
func newRaceHub(
	t *testing.T,
	resolve realtime.Resolver,
	clock realtime.HubClock,
) (*realtime.Hub, *realtime.Registry) {
	t.Helper()

	database, writer, reader := openDatabase(t)

	insertSecondCampaign(t, database)

	registry := realtime.NewRegistry(t.Context(), realtime.Config{Write: writer, Read: reader})

	hub := realtime.NewHub(t.Context(), realtime.HubConfig{
		States:  registry,
		Resolve: resolve,
		Clock:   clock,
	})

	t.Cleanup(func() {
		// The hub first, because its `Close` is the flush and the registry is
		// closed again after it — `Registry.Close` is idempotent, and a caller
		// handed a hub should not have to know that. `context.WithoutCancel`
		// because `t.Context()` is cancelled before cleanups run.
		if err := hub.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("hub.Close() error = %v, want nil", err)
		}

		if err := registry.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("registry.Close() error = %v, want nil", err)
		}
	})

	return hub, registry
}

// complaints collects what a concurrent test found, from the goroutines that found
// it.
//
// A `sync.WaitGroup` is not enough on its own: `t.Errorf` from a goroutine that
// outlives the test body panics with "Log in goroutine after test has completed",
// which turns a real finding into a confusing one. So the goroutines record
// strings and the test reports them once every goroutine has returned.
type complaints struct {
	mu       sync.Mutex
	recorded []string
}

// add records one problem.
func (c *complaints) add(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.recorded = append(c.recorded, fmt.Sprintf(format, args...))
}

// report fails the test with everything recorded, or does nothing.
func (c *complaints) report(t *testing.T) {
	t.Helper()

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, problem := range c.recorded {
		t.Error(problem)
	}
}

// racedSend reports whether err is one of the three answers a `Send` racing a
// shutdown may give.
//
// The operation name is not a parameter because there is only one caller and
// `unparam` is right that a string every call spells the same way is a second
// thing to keep in step.
func racedSend(err error) bool {
	return err == nil ||
		errors.Is(err, realtime.ErrPeerClosed) ||
		errors.Is(err, realtime.ErrAnswersFull)
}

// TestPeersJoiningAndLeavingWhileFramesArePublishedConverge is the state map under
// load, which is the claim `h.mu` exists for and which no other test removes.
//
// `Join` inserts into `rooms` and into a room's `peers`; `Publish` walks both to
// fan out; `Leave` deletes from both and deletes the room itself when it empties.
// Three methods, one map, and a test that runs them one after another observes
// nothing at all: a `Publish` that read `h.rooms` without the lock would deliver
// correctly every time, because nobody was writing while it read.
//
// **The assertions are convergence, not liveness.** Every joiner leaves, so the
// end state is fully determined: no peers and no campaigns. That is the strongest
// thing observable here, and it is the thing a missing `delete` in `detach` would
// break — the room that outlives its last peer, which is one of the two real bugs
// `hub_test.go` was written against. It is asserted concurrently because the
// interesting way to fail it is a fan-out that reads a room between another
// goroutine's insert and its delete.
func TestPeersJoiningAndLeavingWhileFramesArePublishedConverge(t *testing.T) {
	hub, _ := newTestHub(t, nil)

	const (
		joiners    = 24
		publishers = 4
		rounds     = 150
	)

	// Two barriers, for the reason `TestConcurrentJoinsToOneCampaignAllSucceed`
	// states: `ready` says every goroutine is *launched*, so the operations
	// overlap and the race is actually hit; `done` says every one has
	// *returned*, so the results below are finished ones.
	var ready, done sync.WaitGroup

	var found complaints

	ready.Add(joiners + publishers + 1)
	done.Add(joiners + publishers + 1)

	for index := range joiners {
		go func() {
			defer done.Done()

			ready.Done()

			campaign := int64(1 + index%2)

			peer, err := hub.Join(t.Context(), campaign, viewerGM)
			if err != nil {
				found.add("Join(%d) error = %v, want nil", campaign, err)

				return
			}

			// `Advance` is the reader side of the staleness mechanism and it is
			// one of the few lines in this package that write a peer's counters
			// from outside the hub's own goroutines. Concurrent with the
			// publishers' `offer`, it is the assertion that those writes are
			// serialised rather than merely correct when they do not overlap.
			peer.Advance()
			peer.Advance()

			peer.Leave()
		}()
	}

	for range publishers {
		go func() {
			defer done.Done()

			ready.Done()

			for tick := range rounds {
				frame := clockDelta(epochClock.Add(time.Duration(tick) * time.Second))
				if err := hub.Publish(int64(1+tick%2), frame); err != nil {
					found.add("Publish() error = %v, want nil", err)
				}
			}
		}()
	}

	// A reader of the hub's own counters, which is what `/readyz` does. It reads
	// `rooms` under the lock and the four counters without one, so it is the one
	// goroutine here that exercises both halves of `Stats` at the same time.
	go func() {
		defer done.Done()

		ready.Done()

		for range rounds {
			stats := hub.Stats()

			if stats.Peers < 0 || stats.Campaigns < 0 {
				found.add("Stats() reported %d peers in %d campaigns",
					stats.Peers, stats.Campaigns)
			}
		}
	}()

	ready.Wait()
	done.Wait()
	found.report(t)

	// Every peer left, so the hub holds nothing. A room that outlived its last
	// peer would show here, and it is the leak `hub.go`'s own comment names as
	// the only thing standing between a long-lived hub and unbounded growth.
	if got := hub.Stats().Peers; got != 0 {
		t.Errorf("Stats().Peers = %d after every peer left, want 0", got)
	}

	if got := hub.Stats().Campaigns; got != 0 {
		t.Errorf("Stats().Campaigns = %d after every peer left, want 0; an empty room is "+
			"a campaign the hub will never answer again", got)
	}

	// And every publish was counted, whichever campaign it named. The counter is
	// an atomic and `Publish` increments it before it takes the lock, so a missing
	// increment would be invisible to every assertion above.
	if want := int64(publishers * rounds); hub.Stats().Published != want {
		t.Errorf("Stats().Published = %d, want %d", hub.Stats().Published, want)
	}
}

// TestClosingTheHubUnderTrafficClosesEveryPeerExactlyOnce is the teardown, and it
// is the one place a panic can arrive with nobody to recover it.
//
// `hub.go` states the two halves: the peers are closed first and the sweeper is
// waited for second, and both happen under the lock, "which is what makes the
// close safe: every send into those channels also happens under this lock, so no
// send can be mid-flight when the channel closes, and a send on a closed channel
// panics — a panic that would arrive in whichever goroutine published, which is a
// resolver returning a dice roll rather than anywhere it could be recovered."
//
// Three paths detach a peer — `Close`, `Leave`, and the sweep — and each of them
// reaches the same two `close` calls through `detach`'s `closedOnce`. This test
// puts two of them in flight against one another, which is the only observation
// that says the guard holds rather than that it was never needed. The sweeper is
// left to the other test here, which runs one; between them the three paths are
// covered without a third fixture.
func TestClosingTheHubUnderTrafficClosesEveryPeerExactlyOnce(t *testing.T) {
	hub, _ := newTestHub(t, nil)

	const peers = 16

	joined := make([]*realtime.Peer, peers)
	for index := range joined {
		joined[index] = join(t, hub, 1)
	}

	var ready, done sync.WaitGroup

	var found complaints

	// One gate rather than a barrier: every traffic goroutine waits on it and the
	// shutdown closes it. A goroutine that started before the shutdown would race
	// it by luck rather than by arrangement, and "concurrent" is the whole claim.
	gate := make(chan struct{})

	stop := make(chan struct{})

	// `left` is announced by the teardown goroutine after each round, so the
	// shutdown cannot begin until two rounds of `Leave` have already run.
	//
	// **A handshake rather than a sleep, and two rounds rather than one, and both
	// are the reason this test catches the mutation it is written for.** `detach`
	// deletes the peer from its room *before* it closes anything, so a shutdown that
	// follows a single round finds only the peers nobody left: one close each, and
	// `closedOnce` is never asked to absorb anything. The second round is the one
	// that re-tears-down peers the first already closed, which is what a route's
	// deferred leave does on its error exit. Without the handshake the leaver is
	// merely *runnable* when the shutdown starts, and a scheduler that does not run
	// it first lets it retire zero times — the phase-4 lesson about assertions on a
	// fixture's own scheduling, applied here where the fixture *is* the thing that
	// interleaves.
	left := make(chan struct{}, 8)

	ready.Add(3)
	done.Add(4)

	// The fan-out, walking rooms that are about to be emptied.
	go func() {
		defer done.Done()

		ready.Done()
		<-gate

		for tick := range 400 {
			frame := clockDelta(epochClock.Add(time.Duration(tick) * time.Second))
			if err := hub.Publish(1, frame); err != nil {
				found.add("Publish() error = %v, want nil", err)
			}
		}
	}()

	// The answer path, which is the one that can panic. Each answer is drained
	// immediately so the reply queue does not simply fill and retire every peer in
	// the first few microseconds — a send path that has already retired its peers
	// is not overlapping the shutdown with anything.
	go func() {
		defer done.Done()

		ready.Done()
		<-gate

		frame := clockDelta(epochClock)

		for range 400 {
			for _, peer := range joined {
				if err := peer.Send(frame); !racedSend(err) {
					found.add("Send() error = %v, want nil, ErrPeerClosed or ErrAnswersFull", err)

					return
				}

				select {
				case <-peer.Answers():
				default:
				}
			}
		}
	}()

	// The route's own teardown, which is what makes `Leave` idempotent: it runs on
	// every exit from the read loop, including the error exits. Looping until the
	// shutdown has finished is what makes the two detaches contend rather than
	// happen in whichever order the scheduler picked.
	go func() {
		defer done.Done()

		ready.Done()
		<-gate

		for {
			select {
			case <-stop:
				return
			default:
			}

			for index, peer := range joined {
				if index%2 == 0 {
					peer.Leave()
				}
			}

			select {
			case left <- struct{}{}:
			case <-stop:
				return
			}

			time.Sleep(poll)
		}
	}()

	ready.Wait()
	close(gate)

	// Two rounds. The second is the one that asks `detach`'s guard to absorb a
	// teardown of a peer it has already torn down.
	<-left
	<-left

	go func() {
		defer done.Done()

		if err := hub.Close(context.WithoutCancel(t.Context())); err != nil {
			found.add("hub.Close() error = %v, want nil", err)
		}

		close(stop)
	}()

	done.Wait()
	found.report(t)

	// Every peer is closed, whichever of the two paths got to it. One that is not
	// would still be holding a socket's worth of channels for the life of the
	// process.
	for index, peer := range joined {
		if !peer.Stats().Closed {
			t.Errorf("peer %d was not closed by the shutdown", index)
		}
	}

	if got := hub.Stats().Peers; got != 0 {
		t.Errorf("Stats().Peers = %d after the shutdown, want 0", got)
	}

	// And the hub still refuses work afterwards, which is what `Close` is for.
	if _, err := hub.Join(t.Context(), 1, viewerGM); !errors.Is(err, realtime.ErrHubClosed) {
		t.Errorf("Join() after the shutdown error = %v, want ErrHubClosed", err)
	}
}

// TestTheSweepRetiresBehindPeersWhilePeersJoinAndLeave is the sweeper against
// live traffic, and it is the one test here that runs on the **real** clock.
//
// `hub_test.go` drives the sweep with a fake clock, which is right for asserting
// the staleness window exactly and wrong for this: the question is not "is a peer
// behind for longer than 45 seconds" but "does a sweep that walks a room while
// other goroutines are inserting into and deleting from it stay consistent". So
// the window is two milliseconds and the clock is `time.Now`, and the sweep runs
// continuously for the whole test rather than once on command.
//
// **The window is two milliseconds and not larger because a test that waited for a
// real sweep at the production window would take 45 seconds** and would still only
// observe one sweep. Nothing about the mechanism depends on the number; what the
// test claims is that a sweep interleaved with traffic is safe.
//
// The non-vacuity condition is the assertion that matters most here: `Staled` must
// be non-zero. `Send` also increments it, and this test sends nothing, so every
// one of those increments is a peer the sweeper retired — a run in which no sweep
// ever fired would otherwise look exactly like one in which hundreds did.
func TestTheSweepRetiresBehindPeersWhilePeersJoinAndLeave(t *testing.T) {
	hub, _ := newRaceHub(t, nil, realtime.HubClock{Stale: 2 * time.Millisecond})

	const peers = 32

	joined := make([]*realtime.Peer, peers)
	for index := range joined {
		joined[index] = join(t, hub, 1)
	}

	var done sync.WaitGroup

	var found complaints

	stop := make(chan struct{})

	done.Add(2)

	// Publishing until the sweeper has retired somebody, and no longer. No reader
	// drains these peers' broadcast slots, which is the condition `offer` arms the
	// staleness clock on — a slot that stayed full for the whole window.
	go func() {
		defer done.Done()

		for tick := 0; ; tick++ {
			select {
			case <-stop:
				return
			default:
			}

			frame := clockDelta(epochClock.Add(time.Duration(tick) * time.Second))
			if err := hub.Publish(1, frame); err != nil {
				found.add("Publish() error = %v, want nil", err)

				return
			}
		}
	}()

	// Leaving, and reading the counters, from under the sweep. A peer the sweeper
	// retires and a peer that leaves reach `detach` from two goroutines, and both
	// need the same `closedOnce` for the two channel closes.
	go func() {
		defer done.Done()

		for round := 0; ; round++ {
			select {
			case <-stop:
				return
			default:
			}

			stats := hub.Stats()

			if stats.Peers < 0 || stats.Campaigns < 0 {
				found.add("Stats() reported %d peers in %d campaigns",
					stats.Peers, stats.Campaigns)

				return
			}

			joined[round%peers].Leave()

			time.Sleep(poll)
		}
	}()

	waitFor(t, "the sweep to retire a peer behind its whole window", func() bool {
		return hub.Stats().Staled > 0
	})

	close(stop)
	done.Wait()
	found.report(t)

	// Everyone leaves now, and a peer the sweep already retired accepts that
	// idempotently — which is the same `closedOnce` the sweep needed.
	for index, peer := range joined {
		peer.Leave()

		if !peer.Stats().Closed {
			t.Errorf("peer %d was not closed after its Leave()", index)
		}
	}

	if got := hub.Stats().Peers; got != 0 {
		t.Errorf("Stats().Peers = %d after every peer left, want 0", got)
	}

	if got := hub.Stats().Staled; got == 0 {
		t.Error("no peer was retired by the sweep, so the sweep never ran and this test " +
			"observed nothing; check that the injected window was not ignored")
	}
}

// TestEveryAppliedIntentIsAnsweredExactlyOnceAndReachesTheTable is the intent
// path under concurrency, and it is the "a mutation applied to a state nobody
// broadcasts" question asked of the hub.
//
// `Apply` is the only path by which anything a client says becomes state, and it
// has two audiences: the actor's own `seq` on `Answers`, and the campaign on
// `Broadcasts`. A hub that answered one and not the other is broken in two
// opposite ways — a client that rolls its optimistic update back and waits forever
// on the first, and a table that silently stops seeing the campaign move on the
// second. Neither is visible from a single call's return value, which is why the
// existing `Apply` tests are sequential.
//
// Both assertions are **exact counts**, and they are the reason this test is not
// the sequential one restated: N actors issuing M intents each produce exactly NM
// answers, exactly NM resolver calls, and exactly one answer per `seq` across the
// whole set. A single-flight bug, a double answer, or a lost broadcast all move one
// of those numbers.
//
// The `seq` values are **globally unique** rather than unique per actor. They are
// what a client waits on, and two answers carrying the same one is two rollbacks;
// making them unique per actor would let a hub that answered actor 2's first
// intent with actor 1's answer pass the whole test.
func TestEveryAppliedIntentIsAnsweredExactlyOnceAndReachesTheTable(t *testing.T) {
	resolver := &stubResolver{total: 20}
	hub, _ := newTestHub(t, resolver)

	const (
		actors  = 8
		intents = 4
	)

	// The table's other half: a peer that applies nothing and answers nothing. Its
	// broadcast slot is one deep and superseded, so what can honestly be claimed
	// about it is that the table was told, not which actor it was told about.
	watcher := join(t, hub, 1)

	players := make([]*realtime.Peer, actors)
	for index := range players {
		players[index] = join(t, hub, 1)
	}

	// Decoded here rather than inside the goroutines, because `decodeIntent` reports
	// a fixture fault through `t.Fatalf` and a `Fatalf` from a goroutine that
	// outlives the test body is a panic rather than a diagnosis.
	frames := make([][]*realtime.ClientIntent, actors)

	for index := range frames {
		frames[index] = make([]*realtime.ClientIntent, intents)

		for slot := range frames[index] {
			raw := fmt.Sprintf(
				`{"t":"intent","seq":%d,"op":"move_token",`+
					`"args":{"placement":"p%d","x":1,"y":2}}`,
				index*intents+slot+1, index,
			)

			frames[index][slot] = decodeIntent(t, raw)
		}
	}

	var ready, done sync.WaitGroup

	var found complaints

	ready.Add(actors)
	done.Add(actors)

	for index, peer := range players {
		go func() {
			defer done.Done()

			ready.Done()

			for _, intent := range frames[index] {
				if err := hub.Apply(t.Context(), peer, intent); err != nil {
					found.add("Apply() by actor %d seq %d error = %v, want nil",
						index, intent.Seq, err)
				}
			}
		}()
	}

	ready.Wait()
	done.Wait()
	found.report(t)

	// Every actor drained exactly its own intents, each `seq` answered once. The
	// answers are read from the encoded frames rather than from the queue length,
	// because a queue of the right length holding the wrong frames is the failure
	// this is looking for.
	answered := 0

	for index, peer := range players {
		for _, want := range frames[index] {
			frame := receive(t, peer.Answers())
			answered++

			var decoded struct {
				Type realtime.Type      `json:"t"`
				Seq  realtime.ClientSeq `json:"seq"`
			}

			if err := json.Unmarshal(frame, &decoded); err != nil {
				t.Fatalf("decode the applied frame %s: %v", frame, err)
			}

			if decoded.Type != realtime.TypeApplied {
				t.Errorf("actor %d received a %s frame, want an applied", index, decoded.Type)
			}

			if decoded.Seq != want.Seq {
				t.Errorf("actor %d was handed the answer for seq %d, want %d; an answer "+
					"addressed to another actor's seq is a client rolling back twice",
					index, decoded.Seq, want.Seq)
			}
		}

		expectNothing(t, peer.Answers())
	}

	if want := actors * intents; answered != want {
		t.Errorf("%d answers were delivered, want %d", answered, want)
	}

	if got := hub.Stats().Answered; got != int64(answered) {
		t.Errorf("Stats().Answered = %d, want %d; the counter is what an operator reads, "+
			"and it has to agree with the frames", got, answered)
	}

	// The resolver saw every intent exactly once, each bound to its own actor. This
	// is S-7.3's authority half asserted under concurrency: `seen` is written
	// through the stub's own mutex, so a hub that handed the same intent over twice
	// shows up here as a duplicate rather than as a race.
	handed := resolver.handed()
	if len(handed) != answered {
		t.Fatalf("the resolver was handed %d intents, want %d", len(handed), answered)
	}

	sequences := make([]int64, 0, len(handed))

	for _, intent := range handed {
		if intent.Campaign != 1 {
			t.Errorf("the resolver was told campaign %d, want 1", intent.Campaign)
		}

		sequences = append(sequences, int64(intent.Frame.Seq))
	}

	slices.Sort(sequences)

	for index := 1; index < len(sequences); index++ {
		if sequences[index] == sequences[index-1] {
			t.Errorf("seq %d reached the resolver twice; one seq resolved twice is two "+
				"answers and two rollbacks", sequences[index])
		}
	}

	// And the table heard. The slot is one deep, so the frame it holds is the
	// newest delta and that is exactly the claim: a hub that answered its actors
	// and never announced would leave this queue empty.
	assertCarriesTotal(t, receive(t, watcher.Broadcasts()), 20)

	// No `seq` was invented for the watcher, who sent nothing. A delta carrying
	// one would pair a table frame with an intent that was not the actor's.
	expectNothing(t, watcher.Answers())
}
