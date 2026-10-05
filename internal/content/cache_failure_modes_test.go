package content_test

// §13's "cache miss" row: a miss re-renders synchronously, and the cache is
// never a dependency.
//
// What the existing suite already holds, and these tests do not repeat: a
// failed render is not cached and a retry works
// (`TestRenderDoesNotCacheFailures`), a panicked render releases its flight
// and the next render succeeds
// (`TestAPanicInAProducerReleasesItsFlightAndEveryWaiterLearnsOfIt`), and the
// two variants of one page never collide
// (`TestTwoVariantsOfOnePageDoNotCollide`). The three gaps below are the ones
// that row still leaves open:
//
//   - Invalidation must cost exactly one synchronous re-render: the entry is
//     gone, so the next `Render` misses, produces, and caches again, rather
//     than serving the entry it just dropped.
//   - The single-flight accounting must be exact: the leader that produced is
//     the one miss, and every waiter answered from memory is a hit. The
//     comment on `Stats` fixes that meaning, and nothing asserted it.
//   - Both tiers must be counted. `Stats` carries no per-tier split — the tier
//     separation lives in the key — so what is asserted is that a miss and a
//     hit in each tier all count, and each tier keeps its own body.

import (
	"sync"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/content"
)

// TestInvalidateCampaignForcesASynchronousRerender is the invalidation seam of
// "the cache is never a dependency".
//
// `TestInvalidateCampaignRemovesOnlyThatCampaign` drops entries and reads them
// back through `Get`, which only proves the entries are gone. This proves the
// read path survives them being gone: the next `Render` for an invalidated key
// produces synchronously — the body is the producer's, in the same call — and
// caches it again, so the request after that is a hit on the fresh body.
func TestInvalidateCampaignForcesASynchronousRerender(t *testing.T) {
	t.Parallel()

	cache := content.NewCache(8)
	k := key("Page.md", "abc", false)

	cache.Put(k, entry("<p>stale</p>"))
	cache.InvalidateCampaign(1)

	before := cache.Stats()

	if _, found := cache.Get(k); found {
		t.Fatal("the invalidated entry is still served; the miss below would not be one")
	}

	produced := false

	got, err := cache.Render(k, func() (content.Entry, error) {
		produced = true

		return entry("<p>fresh</p>"), nil
	})
	if err != nil {
		t.Fatalf("Render() after invalidation error = %v, want nil", err)
	}

	if !produced {
		t.Error("Render() after invalidation did not produce; the cache answered " +
			"from something it had just been told to drop")
	}

	if got.Rendered.HTML != "<p>fresh</p>" {
		t.Errorf("Render() after invalidation served %q, want the freshly produced body",
			got.Rendered.HTML)
	}

	// The miss counted, exactly once: the `Get` above is the +1 miss that
	// proves the entry was gone, and the `Render` is the +1 that proves the
	// re-render went through the miss path rather than around it.
	if misses := cache.Stats().Misses - before.Misses; misses != 2 {
		t.Errorf("the invalidation and its re-render counted %d misses, want 2 "+
			"(the proving Get and the Render)", misses)
	}

	// And the fresh body is cached again: the request after that is a hit, on
	// the new body and not on the stale one.
	cached, found := cache.Get(k)
	if !found {
		t.Fatal("the re-rendered entry was not cached; every request would produce")
	}

	if cached.Rendered.HTML != "<p>fresh</p>" {
		t.Errorf("the cached entry holds %q, want the re-rendered body", cached.Rendered.HTML)
	}

	if hits := cache.Stats().Hits - before.Hits; hits != 1 {
		t.Errorf("counted %d hits after the re-render, want 1", hits)
	}
}

// TestFlightWaitersCountAsHitsAndTheLeaderCountsTheMiss pins the accounting
// `Stats` documents: "a call that became the leader of a flight is a miss" and
// "a caller that waited on somebody else's flight is a hit.
//
// The counts are exact rather than "at least", and they hold whatever the
// schedule does: a waiter that arrives after the leader published still counts
// exactly one hit through the re-read under the lock, so late arrivals cannot
// move either number.
func TestFlightWaitersCountAsHitsAndTheLeaderCountsTheMiss(t *testing.T) {
	t.Parallel()

	const callers = 8

	cache := content.NewCache(64)
	k := key("Popular.md", "abc", false)

	release := make(chan struct{})
	entered := make(chan struct{}, 1)

	var wg sync.WaitGroup

	for range callers {
		wg.Go(func() {
			got, err := cache.Render(k, func() (content.Entry, error) {
				entered <- struct{}{}
				<-release

				return entry("<p>once</p>"), nil
			})
			if err != nil {
				t.Errorf("Render() error = %v, want nil", err)

				return
			}

			if got.Rendered.HTML != "<p>once</p>" {
				t.Errorf("Render() served %q, want the shared entry", got.Rendered.HTML)
			}
		})
	}

	<-entered
	// Give the followers a moment to pile onto the flight, as the
	// produce-once test does. The counts below do not depend on it — a
	// follower that arrives late is still exactly one hit — but a flight with
	// no waiters would assert nothing about waiters.
	time.Sleep(50 * time.Millisecond)
	close(release)

	wg.Wait()

	stats := cache.Stats()

	if stats.Misses != 1 {
		t.Errorf("Misses = %d over %d concurrent renders of one key, want exactly 1: "+
			"only the leader produces", stats.Misses, callers)
	}

	if stats.Hits != callers-1 {
		t.Errorf("Hits = %d over %d concurrent renders of one key, want %d: every "+
			"caller the leader answered for is a hit",
			stats.Hits, callers, callers-1)
	}
}

// TestStatsCountBothTiers is the "by tier" half of §13.2's `cache.hit` /
// `cache.miss` row, as far as this type can express it: the counters are
// global, and the tier separation lives in the key, so the assertion is that a
// miss and a hit in *each* tier all count, and that each tier's body is its
// own.
//
// Table-driven because the claim is the same accounting twice, and an
// accounting asserted for one tier only is where a variant that stopped
// counting would hide.
func TestStatsCountBothTiers(t *testing.T) {
	t.Parallel()

	for _, tier := range []struct {
		name    string
		secrets bool
		body    string
	}{
		{name: "redacted", secrets: false, body: "<p>redacted body</p>"},
		{name: "gm", secrets: true, body: "<p>UNREDACTED body</p>"},
	} {
		t.Run(tier.name, func(t *testing.T) {
			t.Parallel()

			cache := content.NewCache(8)
			k := key("Page.md", "abc", tier.secrets)

			rendered, err := cache.Render(k, func() (content.Entry, error) {
				return entry(tier.body), nil
			})
			if err != nil {
				t.Fatalf("Render() error = %v, want nil", err)
			}

			if rendered.Rendered.HTML != tier.body {
				t.Fatalf("Render() served %q, want %q", rendered.Rendered.HTML, tier.body)
			}

			stats := cache.Stats()
			if stats.Misses != 1 || stats.Hits != 0 {
				t.Errorf("after one %s render: hits = %d, misses = %d, want 0 and 1",
					tier.name, stats.Hits, stats.Misses)
			}

			cached, found := cache.Get(k)
			if !found {
				t.Fatalf("the %s render was not cached", tier.name)
			}

			if cached.Rendered.HTML != tier.body {
				t.Errorf("the %s entry holds %q, want %q",
					tier.name, cached.Rendered.HTML, tier.body)
			}

			stats = cache.Stats()
			if stats.Misses != 1 || stats.Hits != 1 {
				t.Errorf("after one %s render and one reread: hits = %d, misses = %d, "+
					"want 1 and 1", tier.name, stats.Hits, stats.Misses)
			}
		})
	}
}
