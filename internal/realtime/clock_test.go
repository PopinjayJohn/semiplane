package realtime_test

import (
	"sync"
	"testing"
	"time"
)

// fakeClock is the injected `realtime.Cadence` clock, driven by hand.
//
// # Why this exists rather than a sleep
//
// The debounce is the one thing about `campaign_state` that cannot be
// deterministic, so it is the one thing whose tests must be. Three claims in
// `state_test.go` are impossible to assert in real time, and each fails in a
// different direction:
//
//   - **"M mutations inside the window produce one write"** is a claim about a
//     count. In real time the test would sleep past the window and read whatever
//     count it found, which cannot distinguish "coalesced to one" from "coalesced
//     to one because the machine was busy".
//   - **"A mutation at T−1ms does not produce a write at T"** is a claim about the
//     *absence* of an event. The only way to assert absence in real time is to
//     sleep longer than the window, which makes the assertion weaker at exactly
//     the moment it makes the test slower: a longer sleep passes against an
//     implementation that is wrong by a factor of ten.
//   - **"Shutdown with nothing pending writes nothing"** is a claim that a write
//     *would falsify*. Asserting only "the row is correct" passes against an
//     implementation that rewrites identical bytes on every shutdown.
//
// # Why the clock is not the scheduler
//
// The fake clock does not run the state's goroutine, because that is the code under
// test and a test that reimplements it agrees with its bugs. It supplies `Now` and
// `After` and nothing else: the scheduler's own loop decides what to do with a fired
// timer.
//
// What the clock *does* supply is observability of that loop, and the two waits
// below are the whole reason the tests here are deterministic rather than merely
// fast:
//
//   - `arm` waits until the scheduler has called `After`. Without it, a test that
//     mutates and immediately advances time has a race: if the scheduler has not
//     been scheduled yet, `advance` finds nothing to deliver and the write never
//     happens. A test that "passes" because of that race is a gate wired to
//     nothing — the same failure mode `content`'s a11y guard was written for.
//   - `quiet` waits until the scheduler has stopped calling `After`. Without it, a
//     test that advances time and immediately reads the write count is reading
//     before the goroutine has run, and the assertion is a coin flip.
//
// Both wait on a counter the implementation increments — the number of timers it
// has armed — so they observe the real loop rather than a model of it.

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*fakeWaiter

	// arms counts every `After` call. The waiters below exist so a test can tell
	// "the scheduler is waiting" from "the scheduler is working", and neither is
	// visible from the waiter list alone: a scheduler that has finished leaves an
	// abandoned waiter behind, and one that has not started leaves none.
	arms int
	// deliveries counts how many timers `advance` has fired, so a test can tell
	// "the timer went off" from "the clock moved and nothing was due".
	deliveries int
}

// fakeWaiter is one armed `After`.
type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

// newFakeClock returns a clock at a fixed, arbitrary instant.
//
// The instant is not the Unix epoch and not zero: a test that reasoned about
// absolute times would be reasoning about the wrong thing, and a clock at the epoch
// makes a `time.Time` zero value indistinguishable from "never set", which is a
// confusion this package goes to some lengths to avoid elsewhere.
func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, time.September, 30, 14, 0, 0, 0, time.UTC)}
}

// Now reports the current fake time.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

// After returns a channel that `advance` will deliver on once the clock has moved
// the given duration.
//
// A deadline that is **already in the past delivers immediately**, which is what
// `time.After` does for a non-positive duration and what makes the clock safe
// against a scheduler that arms late: the deadline has passed, the timer is due,
// and the state flushes rather than waiting for a test that has already moved on.
// Without this, a test whose `advance` raced the scheduler's `After` would hang
// until the budget expired, and the fix would be a sleep — which is the thing this
// file exists to avoid.
//
// An immediately-delivered timer is **not** added to the pending list. It was
// delivered, and a later `advance` delivering it a second time would block on a
// channel nobody is reading — the first version of this file did exactly that, and
// it hung a `-count=3` run for five minutes. The invariant is that every waiter is
// delivered exactly once, either here or by `advance`, and `advance`'s send is a
// blocking one *because* of it: a second delivery hangs rather than being dropped,
// which is the only way a bug in here announces itself instead of quietly
// shortening a window.
//
// Buffered with one slot, as `time.After` is, so a delivery is never dropped by a
// scheduler that has not reached its `select` yet.
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.arms++

	waiter := &fakeWaiter{at: c.now.Add(d), ch: make(chan time.Time, 1)}

	if !waiter.at.After(c.now) {
		waiter.ch <- c.now

		return waiter.ch
	}

	c.waiters = append(c.waiters, waiter)

	return waiter.ch
}

// advance moves the clock forward and delivers on every timer that came due.
//
// Delivered **after** `now` is moved and in the same critical section, so a
// scheduler that reads `Now` when the timer fires sees the time the timer was
// armed for — which is what makes the trailing-edge test exact. Moving the clock
// and delivering in two steps would let a scheduler observe an earlier `now` than
// the one that fired it, and the "did the deadline move" check would be testing the
// test.
//
// Timers already due at the moment of the call are delivered too, for the same
// reason `After` delivers them.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)

	// Copied out and cleared before delivering, so a timer delivered here cannot be
	// delivered again by a later `advance`, and so a scheduler that re-arms during
	// delivery registers a fresh waiter rather than one this pass will fire.
	var due []*fakeWaiter

	remaining := c.waiters[:0]

	for _, waiter := range c.waiters {
		if !waiter.at.After(c.now) {
			due = append(due, waiter)

			continue
		}

		remaining = append(remaining, waiter)
	}

	for index := len(remaining); index < len(c.waiters); index++ {
		c.waiters[index] = nil
	}

	c.waiters = remaining
	c.deliveries += len(due)

	for _, waiter := range due {
		waiter.ch <- c.now
	}
}

// Arms returns how many timers the scheduler has armed. The mark `arm` and `quiet`
// are given.
func (c *fakeClock) Arms() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.arms
}

// arm waits until the scheduler has armed at least one timer beyond `mark`, and
// then until it has stopped arming. Returns a fresh mark.
//
// The wait on the mark is the part that makes "mutate, then advance" safe: it
// proves the scheduler saw the mutation and armed a window for it, rather than the
// test assuming it did.
func (c *fakeClock) arm(t *testing.T, mark int) int {
	t.Helper()

	deadline := time.Now().Add(settleBudget)

	for c.Arms() <= mark {
		if time.Now().After(deadline) {
			t.Fatalf("the scheduler armed no timer within %s of a mutation; a mutation that "+
				"does not arm the debounce window is a mutation that is never written",
				settleBudget)
		}

		time.Sleep(poll)
	}

	return c.quiet(t)
}

// quiet waits until the scheduler has stopped arming, and returns a fresh mark.
//
// Real time, deliberately: the alternative is a fake scheduler goroutine the test
// drives step by step, which is a reimplementation of the loop under test and
// agrees with whatever bug it was written from. The budget is a ceiling on a
// failure, not a cost on a success.
func (c *fakeClock) quiet(t *testing.T) int {
	t.Helper()

	deadline := time.Now().Add(settleBudget)

	stable := 0

	var (
		previousArms int
		previousFire int
		sampled      bool
	)

	for stable < stablePolls {
		if time.Now().After(deadline) {
			t.Fatalf("the scheduler did not settle within %s; it is still arming timers, so a "+
				"window is moving under a test that believes the state has settled", settleBudget)
		}

		arms, fired := c.counters()
		if sampled && arms == previousArms && fired == previousFire {
			stable++
		} else {
			previousArms, previousFire, sampled = arms, fired, true
			stable = 0
		}

		time.Sleep(poll)
	}

	arms, _ := c.counters()

	return arms
}

// counters returns the arm and delivery counts together, so a `quiet` sample is a
// consistent pair rather than two reads that could straddle an arm.
func (c *fakeClock) counters() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.arms, c.deliveries
}
