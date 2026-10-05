package content_test

// Race coverage for the render cache: the five things `cache_test.go` proves one
// caller at a time, and which only exist under concurrency.
//
// # What the existing tests do not reach
//
// `cache_test.go` is not a weak suite — `TestRenderCallsProduceOnceUnderConcurrency`
// and `TestStatsAreSafeUnderConcurrentTraffic` are real — but read for concurrency
// the gap is specific:
//
//   - The panic path is **absent entirely**. `ErrRenderFailed`, `Cache.release`'s
//     `recover`, and the reason the flight is deleted from `inflight` on the way
//     out have no test at all. `cache.go` says what happens without them: "a panic
//     inside produce leaves the flight in `inflight` forever: every later request
//     for that key finds it, waits on a channel nobody will ever close, and hangs.
//     One panic in one render would become a permanently unavailable page instead
//     of a 500 — a cache turning a bug into an outage, which is the one thing this
//     file is not allowed to do." That is a claim about a *deadlock*, and a
//     deadlock is only observable with a waiter, so nothing here could have found it
//     without one.
//   - `InvalidateCampaign` is only ever called on a quiet cache. Its own comment
//     says in-flight renders are not cancelled and re-insert an unreachable entry,
//     and no test runs one against the other.
//   - The **eviction path is never reached concurrently**. Every existing test uses
//     a bound larger than the number of distinct keys it writes, so `evictLocked`
//     never runs while another goroutine holds the lock — and `evictLocked` is the
//     one function in this file that walks the whole map.
//   - `Put` and `Get` are only ever called sequentially. `Render` exercises the
//     read lock through `lookup`; nothing exercises a `Put` racing a `Render` for
//     the same key, which is the case the double read of `c.entries` under the
//     write lock exists for.
//   - The **disabled cache** (`maxEntries <= 0`) is checked on a quiet cache. Its
//     documented reading is "it simply produces on every call", and every call is
//     therefore a miss with its own flight — a very different shape from anything
//     the suite runs.
//
// # The invariant each test below is really about
//
// The cache is **never a dependency**. A miss re-renders synchronously, a failure
// is not cached, and an eviction can cost a re-render and nothing else — because
// the content hash is in the key, so no reader can be handed a stale body at any
// eviction rate. Every assertion below is a version of that: under every schedule,
// the body a caller gets back is a body *its own key* produced. That is why the
// per-call body checks matter more than the counters, and why a cache that dropped
// the variant from its key would be caught by a test that only ever looks at
// `Len()`.
//
// These tests are parallel. A `Cache` needs no store, no registry and no
// goroutine of its own until it has a flight, so there is nothing here for ADR
// 0004's process-wide slot to collide over.

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/content"
)

// cacheComplaints is the same idea as the hub file's, for the same reason: a
// `t.Errorf` from a goroutine that outlives the test body is a panic rather than a
// diagnosis.
type cacheComplaints struct {
	mu       sync.Mutex
	recorded []string
}

// add records one problem.
func (c *cacheComplaints) add(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.recorded = append(c.recorded, fmt.Sprintf(format, args...))
}

// report fails the test with everything recorded, or does nothing.
func (c *cacheComplaints) report(t *testing.T) {
	t.Helper()

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, problem := range c.recorded {
		t.Error(problem)
	}
}

// TestAPanicInAProducerReleasesItsFlightAndEveryWaiterLearnsOfIt is the untested
// panic path, and the waiters are the only thing that can observe it.
//
// Without `release`'s `recover`, a waiter is not given `ErrRenderFailed`; it is
// given `Entry{}` and a **nil** error, because `render` assigns the answer after
// `produce` returns and a panic skips that assignment. So a caller would be handed
// an empty body and told the render succeeded — which is the failure the whole type
// exists to make impossible, arriving by the panic route instead of the eviction
// route. That is what the `ErrRenderFailed` assertion below catches, and it is a
// *value* assertion rather than a timeout, which matters: a mutation that left the
// waiters blocked forever would fail as a ten-minute timeout naming nothing.
//
// The second assertion is the deadlock claim. Without `delete(c.inflight, key)`
// nothing after the panic is wrong at the moment it happens — the waiters have been
// woken and have the right error — and the page is simply unavailable until the
// process ends. So the test renders the same key again *after* the panic and
// requires it to succeed.
//
// The leader's own panic is recovered by the test rather than allowed to escape:
// `cache.go` re-raises it so the middleware boundary can log the stack, and a
// `t.Fatalf` from a non-test goroutine is a panic with a worse message.
func TestAPanicInAProducerReleasesItsFlightAndEveryWaiterLearnsOfIt(t *testing.T) {
	t.Parallel()

	const waiters = 8

	cache := content.NewCache(16)
	k := key("Panicked.md", "abc", false)

	var (
		calls   atomic.Int64
		entered = make(chan struct{}, 1)
		release = make(chan struct{})
	)

	produce := func() (content.Entry, error) {
		calls.Add(1)

		// Held open so every waiter is definitely parked on this flight rather than
		// arriving after it finished. The same arrangement
		// `TestRenderCallsProduceOnceUnderConcurrency` uses, for the same reason.
		entered <- struct{}{}
		<-release

		panic("the render exploded")
	}

	var (
		wg     sync.WaitGroup
		panics = make([]any, waiters)
		errs   = make([]error, waiters)
		values = make([]string, waiters)
	)

	wg.Add(waiters)

	for index := range waiters {
		go func() {
			defer wg.Done()

			// Only the first caller becomes the leader; the rest join its flight. A
			// `recover` in every one of them is fine — the followers do not panic,
			// and the leader's does.
			defer func() { panics[index] = recover() }()

			got, err := cache.Render(k, produce)
			values[index] = got.Rendered.HTML
			errs[index] = err
		}()
	}

	<-entered
	// A pause, so the followers have piled onto the flight. The assertion does not
	// depend on it — a follower that arrived late would still be answered, because
	// `Render` re-checks `entries` under the lock and finds the leader's result
	// once it has been stored.
	time.Sleep(50 * time.Millisecond)

	close(release)
	wg.Wait()

	// One attempt, and the leader's panic is still the leader's panic.
	if got := calls.Load(); got != 1 {
		t.Errorf("produce was called %d times, want exactly 1", got)
	}

	exactlyOne := 0

	for index, recovered := range panics {
		if recovered != nil {
			exactlyOne++

			continue
		}

		if !errors.Is(errs[index], content.ErrRenderFailed) {
			t.Errorf("waiter %d was given %v and an empty body %q; a waiter on a panicked "+
				"render must be told it failed rather than handed an empty page",
				index, errs[index], values[index])
		}
	}

	if exactlyOne != 1 {
		t.Errorf("%d goroutines panicked, want exactly 1: the leader re-raises so the "+
			"middleware boundary can log the stack, and every waiter is handed the error",
			exactlyOne)
	}

	// The deadlock claim. A flight left in `inflight` is invisible above — the
	// waiters were woken and answered — and turns the key permanently unavailable.
	if _, err := cache.Render(k, func() (content.Entry, error) {
		return entry("<p>recovered</p>"), nil
	}); err != nil {
		t.Fatalf("Render() after a panicked render error = %v, want nil; the flight was "+
			"never released, so this key is now unavailable for the life of the process", err)
	}

	got, found := cache.Get(k)
	if !found || got.Rendered.HTML != "<p>recovered</p>" {
		t.Errorf("after the retry: found = %v, html = %q", found, got.Rendered.HTML)
	}
}

// TestConcurrentRendersOfBothVariantsNeverCross is S-5.2 and S-5.6 under
// concurrency, which is where a leak of one would actually happen.
//
// The two variants of one page are two different bodies, and the flag is in the
// key for the reason `cache.go` states: "a cache that returned the GM's variant to
// a player would be S-5.6's omission undone at the last possible moment". Nothing
// else in the suite puts two variants of the same `(campaign, path, hash)` in flight
// at once — `TestTwoVariantsOfOnePageDoNotCollide` puts them in a cache with two
// `Put` calls, so a `Render` that keyed its lookup by anything but the whole struct
// would pass it.
//
// Every goroutine asserts the body it got back, and it asserts against a marker
// only its own producer could have written. A lookup that dropped the flag would
// hand every GM the redacted body; a single-flight keyed too narrowly would hand
// one of two players the other's.
//
// Both variants' producers are held open until every goroutine has arrived, so the
// flights really are concurrent rather than serialised by a fast first render.
func TestConcurrentRendersOfBothVariantsNeverCross(t *testing.T) {
	t.Parallel()

	const perVariant = 32

	cache := content.NewCache(8)

	redacted := key("Shared.md", "abc", false)
	secret := key("Shared.md", "abc", true)

	const (
		redactedBody = "<p>redacted body</p>"
		secretBody   = "<p>UNREDACTED body</p>"
	)

	var arrived sync.WaitGroup

	arrived.Add(2)

	// A closed channel rather than a second `WaitGroup`: the two producers are
	// released by one event, and a counter would need one `Done` per producer while
	// the release is a single close.
	release := make(chan struct{})

	produce := func(body string) func() (content.Entry, error) {
		return func() (content.Entry, error) {
			arrived.Done()
			<-release

			return entry(body), nil
		}
	}

	var found cacheComplaints

	var wg sync.WaitGroup

	wg.Add(2 * perVariant)

	for index := range 2 * perVariant {
		go func() {
			defer wg.Done()

			variant := index % 2

			var (
				k         content.CacheKey
				produceIt func() (content.Entry, error)
				want      string
			)

			if variant == 0 {
				k, produceIt, want = redacted, produce(redactedBody), redactedBody
			} else {
				k, produceIt, want = secret, produce(secretBody), secretBody
			}

			got, err := cache.Render(k, produceIt)
			if err != nil {
				found.add("Render(IncludeSecrets=%v) error = %v, want nil", variant == 1, err)

				return
			}

			if got.Rendered.HTML != want {
				found.add("a request for IncludeSecrets=%v was served %q, want %q",
					variant == 1, got.Rendered.HTML, want)
			}
		}()
	}

	arrived.Wait()
	close(release)
	wg.Wait()
	found.report(t)

	// Two entries, not one and not `perVariant`: the bound is eight and the two
	// variants are the only keys in play, so this is exact rather than an upper
	// bound — a cache that stored a per-goroutine copy of either body would show
	// here, and so would a cache whose single-flight collapsed the two variants
	// into one key.
	if got := cache.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2: one entry per variant of the page", got)
	}
}

// TestInvalidateCampaignRacingRendersAndGetsKeepsTheCacheConsistent is the
// watcher's hook under load, which is the combination `InvalidateCampaign`'s own
// comment is written about and the one no existing test reaches.
//
// The comment's claim has three parts and each is asserted separately, because they
// fail in different directions:
//
//   - Nothing served after an invalidation is stale, because the content hash is in
//     the key. Each renderer here re-reads a key that changes under it, so a cache
//     that handed back the previous version's body would be caught by the body
//     check rather than by a count.
//   - In-flight renders are not cancelled and not waited for: a flight that started
//     before the invalidation publishes afterwards and re-inserts an entry that is
//     already unreachable. So `Len()` is allowed to grow back, and the assertion is
//     the **bound**, not emptiness.
//   - The invalidation is scoped to one campaign, so campaign 2's entries are not
//     collateral. A second campaign's renderer runs throughout and must be answered
//     from memory every time, which is only true if the invalidation is selective.
//
// The eviction path is *not* exercised here, on purpose: it picks a random victim,
// so it cannot coexist with an assertion about which entries survive. It has its
// own test below.
func TestInvalidateCampaignRacingRendersAndGetsKeepsTheCacheConsistent(t *testing.T) {
	t.Parallel()

	const (
		// `bound` is deliberately generous — larger than every key this test writes
		// — because **eviction is the confound here**. `evictLocked` picks a random
		// victim, so a tight bound would eventually drop campaign 2's entry and the
		// "it was re-rendered" assertion below would report an eviction as an
		// invalidation that reached past its campaign. The two failures are different
		// and only one of them is this test's subject; eviction under contention is
		// `TestConcurrentPutsEvictWithoutEverServingTheWrongBody`'s.
		bound        = 4096
		renderers    = 8
		invalidators = 2
		iterations   = 150
	)

	cache := content.NewCache(bound)

	// Campaign 2's one entry is stored before the storm, and no invalidation below
	// names campaign 2. So its producer must never run, and a producer that *does*
	// run is the whole assertion: an invalidation that reached past its campaign.
	//
	// **A pre-stored entry rather than one the storm renders**, because the first
	// `Render` for a fresh key legitimately produces — that is the single flight's
	// leader — and a storm that raced to create the entry would have one legitimate
	// produce per goroutine that arrived first. Pre-storing removes the ambiguity
	// instead of reasoning about it.
	quiet := content.CacheKey{CampaignID: 2, Path: "Quiet.md", ContentHash: "quiet-hash"}

	cache.Put(quiet, entry("<p>quiet</p>"))

	var found cacheComplaints

	var wg sync.WaitGroup

	wg.Add(renderers + invalidators + 1)

	// Campaign 1: three keys per renderer, invalidated underneath, all the way
	// through. The content hash changes on every round, which is what makes the
	// body check meaningful — a key answered with the previous round's body is a
	// stale render, and the content hash in the key is the only thing that can
	// prevent it.
	for index := range renderers {
		go func() {
			defer wg.Done()

			for round := range iterations {
				which := (index + round) % 3

				k := key(fmt.Sprintf("Busy%d.md", which),
					fmt.Sprintf("hash%d-%d", which, round), false)
				want := fmt.Sprintf("<p>busy%d round %d</p>", which, round)

				got, err := cache.Render(k, func() (content.Entry, error) {
					return entry(want), nil
				})
				if err != nil {
					found.add("Render() error = %v, want nil", err)

					return
				}

				if got.Rendered.HTML != want {
					found.add("campaign 1 was served %q for a key whose render produced %q",
						got.Rendered.HTML, want)

					return
				}
			}
		}()
	}

	// Campaign 2, hit after every one of those invalidations.
	go func() {
		defer wg.Done()

		for range iterations {
			got, err := cache.Render(quiet, func() (content.Entry, error) {
				found.add("campaign 2's key was re-rendered, so an invalidation named for " +
					"campaign 1 reached past its campaign")

				return entry("<p>wrong</p>"), nil
			})
			if err != nil {
				found.add("Render() for campaign 2 error = %v, want nil", err)

				return
			}

			if got.Rendered.HTML != "<p>quiet</p>" {
				found.add("campaign 2 was served %q", got.Rendered.HTML)

				return
			}
		}
	}()

	for range invalidators {
		go func() {
			defer wg.Done()

			for range iterations {
				cache.InvalidateCampaign(1)
				cache.Len()
			}
		}()
	}

	wg.Wait()
	found.report(t)

	// The bound, and not emptiness: `InvalidateCampaign` is a memory optimisation
	// and a flight that was already running publishes afterwards by design.
	if got := cache.Len(); got > bound {
		t.Errorf("Len() = %d after racing %d invalidations into a %d-entry cache, "+
			"want at most %d", got, invalidators*iterations, bound, bound)
	}

	// And the campaign the invalidation did not name still holds its entry, so the
	// selectivity survives past the storm rather than being read off a mid-run
	// counter.
	if got, ok := cache.Get(quiet); !ok || got.Rendered.HTML != "<p>quiet</p>" {
		t.Errorf("campaign 2's entry after the storm: found = %v, html = %q",
			ok, got.Rendered.HTML)
	}
}

// TestConcurrentPutsEvictWithoutEverServingTheWrongBody is the bound and the
// eviction under contention, which is the claim `Cache`'s eviction notes make and
// which no existing test can reach.
//
// The claim is: "an eviction here cannot serve wrong content. Because
// `ContentHash` is in the key, a dropped entry costs one re-render on the next
// request and nothing else: no reader can be handed a stale body, ever, at any
// eviction rate." A test that only asserted `Len() <= bound` would pass against a
// cache that evicted the *wrong* entry — including one that evicted a key a reader
// is holding a body for — because the count would be right either way. So every
// `Get` here asserts the body it found, and a concurrent `Render` keeps running
// beside the `Put`s so the read path is under the same contention.
//
// The bound is small and the working set is much larger, so eviction is the common
// case rather than the exception: `evictLocked` collects every key in the map on
// every overflowing store, and it does so under the write lock from many goroutines
// at once.
func TestConcurrentPutsEvictWithoutEverServingTheWrongBody(t *testing.T) {
	t.Parallel()

	const (
		bound      = 8
		writers    = 12
		iterations = 200
	)

	cache := content.NewCache(bound)

	// `written` records what each key's last `Put` stored, so the end-of-run check
	// is exact. A `sync.Map` rather than a mutex-guarded map because this runs
	// under the same `-race` as everything else and the point of the test is that
	// the *cache* is consistent, not that a test fixture needed a second lock.
	var written sync.Map

	var found cacheComplaints

	var wg sync.WaitGroup

	wg.Add(writers + 2)

	for index := range writers {
		go func() {
			defer wg.Done()

			for round := range iterations {
				name := fmt.Sprintf("Page%02d.md", (index+round)%40)
				want := fmt.Sprintf("<p>%s by %d round %d</p>", name, index, round)

				k := key(name, fmt.Sprintf("hash-%d-%d", index, round), index%2 == 0)
				cache.Put(k, entry(want))
				written.Store(k, want)

				if round%8 == 0 {
					got, ok := cache.Get(k)
					if ok && got.Rendered.HTML != want {
						found.add("Get(%s) returned %q after Put stored %q", name,
							got.Rendered.HTML, want)

						return
					}
				}
			}
		}()
	}

	// A renderer, so `Render`'s own `lookup`, its re-read under the write lock and
	// its flight registration all run against `evictLocked` at the same time.
	go func() {
		defer wg.Done()

		for round := range iterations {
			k := key("Hot.md", fmt.Sprintf("hot-%d", round), false)
			want := fmt.Sprintf("<p>hot round %d</p>", round)

			got, err := cache.Render(k, func() (content.Entry, error) {
				return entry(want), nil
			})
			if err != nil {
				found.add("Render() error = %v, want nil", err)

				return
			}

			if got.Rendered.HTML != want {
				found.add("Render() served %q for a key whose render produced %q",
					got.Rendered.HTML, want)

				return
			}
		}
	}()

	// And a reader of the counters, which is what `/readyz` does. The assertion is
	// deliberately **not** about the counts: `TestStatsAreSafeUnderConcurrentTraffic`
	// already holds that a reader sees them at all, and a reader that starts before
	// the writers have run would see zero and report nothing. What this goroutine is
	// here for is the race — two atomics read without stopping the world while every
	// other goroutine is writing them.
	go func() {
		defer wg.Done()

		for range iterations {
			_ = cache.Stats()
		}
	}()

	wg.Wait()
	found.report(t)

	if got := cache.Len(); got > bound {
		t.Errorf("Len() = %d after %d puts into a %d-entry cache, want at most %d",
			got, writers*iterations, bound, bound)
	}

	// And every entry that survived holds **the body its own last `Put` wrote**.
	// `Put` replaces rather than evicts when the key is present, so the expected
	// body is the one the final `Put` for that key recorded — which makes this an
	// exact check rather than "no entry is empty".
	written.Range(func(k, v any) bool {
		got, ok := cache.Get(k.(content.CacheKey))
		if !ok {
			// Evicted. That is the documented cost and not a finding.
			return true
		}

		if got.Rendered.HTML != v.(string) {
			t.Errorf("the cache served %q for a key whose last Put wrote %q",
				got.Rendered.HTML, v.(string))
		}

		return true
	})
}

// TestTheDisabledCacheIsStillCorrectUnderConcurrency is `maxEntries <= 0` under
// load, and its documented reading is only true when every call is a miss.
//
// "`maxEntries <= 0` stores nothing. Not 'unbounded'… and not an error… `Put`
// becomes a no-op, `Get` always misses, `Len` is always zero, and `Render` still
// works — it simply produces on every call." Every existing assertion about the
// disabled cache is on a quiet cache with one call, which cannot distinguish "every
// call produces" from "the first call produces and the rest hit": the second shape
// would satisfy every test in `cache_test.go` and serve a stale body for the life
// of the process.
//
// So every goroutine here counts its own produces and checks the count against its
// own `Render` calls, and the store is never allowed to hold anything.
func TestTheDisabledCacheIsStillCorrectUnderConcurrency(t *testing.T) {
	t.Parallel()

	const (
		workers    = 16
		iterations = 100
	)

	cache := content.NewCache(0)

	var found cacheComplaints

	var wg sync.WaitGroup

	wg.Add(workers)

	for index := range workers {
		go func() {
			defer wg.Done()

			var produces atomic.Int64

			for round := range iterations {
				k := key(fmt.Sprintf("Page%02d.md", round%4),
					fmt.Sprintf("hash-%d-%d", index, round), index%2 == 0)
				want := fmt.Sprintf("<p>%s by %d round %d</p>", k.Path, index, round)

				got, err := cache.Render(k, func() (content.Entry, error) {
					produces.Add(1)

					return entry(want), nil
				})
				if err != nil {
					found.add("Render() error = %v, want nil", err)

					return
				}

				if got.Rendered.HTML != want {
					found.add("a disabled cache served %q, want the body this call "+
						"produced (%q)", got.Rendered.HTML, want)

					return
				}

				cache.Put(k, entry("<p>stored</p>"))

				if _, ok := cache.Get(k); ok {
					found.add("a disabled cache reported a hit for %s", k.Path)

					return
				}

				if cache.Len() != 0 {
					found.add("a disabled cache holds %d entries", cache.Len())

					return
				}
			}

			// One render per call, every time: that is the whole reading.
			if got := produces.Load(); got != iterations {
				found.add("a disabled cache produced %d times over %d renders; every "+
					"call must produce, because nothing is ever cached", got, iterations)
			}
		}()
	}

	wg.Wait()
	found.report(t)

	if got := cache.Len(); got != 0 {
		t.Errorf("Len() = %d on a disabled cache, want 0", got)
	}

	if _, ok := cache.Get(key("Page00.md", "anything", false)); ok {
		t.Error("a disabled cache reported a hit")
	}

	// `InvalidateCampaign` on a cache with nothing in it is a no-op rather than an
	// error, and the disabled reading makes it the same shape as the enabled one.
	cache.InvalidateCampaign(1)

	if got := cache.Len(); got != 0 {
		t.Errorf("Len() = %d after invalidating an empty cache, want 0", got)
	}
}
