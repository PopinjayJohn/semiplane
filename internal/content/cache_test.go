package content_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/content"
)

// These tests were written by the integrator, not by the work item's author: C5
// shipped the cache without them, and an untested single-flight implementation is
// exactly the code where a race hides from `-race` because the interleaving has to
// be arranged rather than stumbled into.

// key builds a cache key with the fields a test cares about varying.
func key(path, hash string, secrets bool) content.CacheKey {
	return content.CacheKey{
		CampaignID:     1,
		Path:           path,
		ContentHash:    hash,
		IncludeSecrets: secrets,
	}
}

// entry builds a cache entry with a recognisable body.
func entry(body string) content.Entry {
	return content.Entry{Rendered: content.Rendered{HTML: body}}
}

// TestETagIsExactlyTheS5Formula asserts the *formula*, not a golden string.
//
// S-5.3 and ADR 0016: `ETag = W/"<sha256(content_hash + ':' + include_secrets)>"`.
// Asserting the pre-image independently is what makes a change to the salt a test
// failure rather than a regenerated golden file — and a regenerated golden file is
// how a secret leak gets blessed.
func TestETagIsExactlyTheS5Formula(t *testing.T) {
	t.Parallel()

	cases := []content.CacheKey{
		key("Page.md", "abc123", false),
		key("Page.md", "abc123", true),
		key("Page.md", "", false),
		key("Page.md", "", true),
		key("Some Page.md", "deadbeef", false),
		key("Page.md", "café/ünïcode", true),
		// Long enough that a truncation bug would show.
		key("Page.md", "0123456789abcdef0123456789abcdef", false),
	}

	for _, k := range cases {
		// Recomputed here, from the spec, with no reference to the
		// implementation's constants.
		preimage := k.ContentHash + ":" + strconv.FormatBool(k.IncludeSecrets)
		sum := sha256.Sum256([]byte(preimage))
		want := `W/"` + hex.EncodeToString(sum[:]) + `"`

		if got := k.ETag(); got != want {
			t.Errorf("ETag() = %s, want %s (pre-image %q)", got, want, preimage)
		}
	}
}

// TestETagSeparatesTheTwoVariants is the release-gating property of S-5.3 and
// §S-14.2: a GM's response and a player's response must never share a validator.
//
// A cache holding both bodies would otherwise serve whichever it stored first,
// which is a direct secret leak to a player.
func TestETagSeparatesTheTwoVariants(t *testing.T) {
	t.Parallel()

	hashes := []string{
		"", "a", "abc", "0123456789abcdef",
		string([]byte{0x00, 0xff, 0xfe}),
	}

	for _, hash := range hashes {
		redacted := key("Page.md", hash, false).ETag()
		secret := key("Page.md", hash, true).ETag()

		if redacted == secret {
			t.Errorf("hash %q: both variants produced %s — S-5.3 violated", hash, redacted)
		}
	}
}

// TestETagDoesNotDependOnCampaignOrPath pins the two omissions the implementation
// documents. S-5.3 specifies the pre-image exactly, and a validator that changed
// when a page moved would invalidate a body nobody changed.
func TestETagDoesNotDependOnCampaignOrPath(t *testing.T) {
	t.Parallel()

	base := content.CacheKey{
		CampaignID:     1,
		Path:           "Page.md",
		ContentHash:    "abc",
		IncludeSecrets: true,
	}

	other := base
	other.CampaignID = 99
	other.Path = "Elsewhere/Other.md"

	if base.ETag() != other.ETag() {
		t.Errorf(
			"ETag changed with campaign and path: %s vs %s — S-5.3's pre-image is the hash and the flag only",
			base.ETag(),
			other.ETag(),
		)
	}
}

// TestCacheControlFollowsTheVariant is S-5.4: `private, no-store` on every
// include_secrets=true response, and a cacheable value otherwise.
func TestCacheControlFollowsTheVariant(t *testing.T) {
	t.Parallel()

	secret := content.CacheControl(key("Page.md", "abc", true))
	if secret != "private, no-store" {
		t.Errorf("GM variant Cache-Control = %q, want %q", secret, "private, no-store")
	}

	redacted := content.CacheControl(key("Page.md", "abc", false))
	if redacted == secret {
		t.Errorf(
			"both variants produced %q — the GM's uncacheable response is the whole point of S-5.4",
			redacted,
		)
	}

	// The redacted value must actually be cacheable by a private cache, or the
	// wiki is uncacheable for every player and the cache earns nothing.
	if redacted == "no-store" {
		t.Error("the redacted variant is no-store, so the cache never helps a player")
	}
}

// TestGetPutRoundTrip covers the basic contract, including the case the
// implementation's comment calls out: a zero-value Entry is a legitimate cache
// hit, so the boolean is the only signal.
func TestGetPutRoundTrip(t *testing.T) {
	t.Parallel()

	cache := content.NewCache(16)
	k := key("Page.md", "abc", false)

	if _, found := cache.Get(k); found {
		t.Error("an empty cache returned a hit")
	}

	cache.Put(k, entry("<p>body</p>"))

	got, found := cache.Get(k)
	if !found {
		t.Fatal("Put then Get missed")
	}

	if got.Rendered.HTML != "<p>body</p>" {
		t.Errorf("HTML = %q, want %q", got.Rendered.HTML, "<p>body</p>")
	}

	// A cached entry that happens to be the zero value must still be a hit.
	empty := key("Empty.md", "def", false)
	cache.Put(empty, content.Entry{})

	if _, found := cache.Get(empty); !found {
		t.Error("a cached zero-value Entry reported as a miss")
	}
}

// TestCacheIsBounded forces the bound, including the documented meaning of a
// non-positive size.
func TestCacheIsBounded(t *testing.T) {
	t.Parallel()

	const limit = 8
	cache := content.NewCache(limit)

	for i := range 100 {
		cache.Put(key(fmt.Sprintf("Page%03d.md", i), fmt.Sprintf("h%03d", i), false), entry("x"))
	}

	if got := cache.Len(); got > limit {
		t.Errorf("Len() = %d after 100 puts into a %d-entry cache", got, limit)
	}

	// A non-positive bound must mean "store nothing" rather than "store
	// everything": unbounded is a memory leak with a trigger.
	for _, bound := range []int{0, -1} {
		none := content.NewCache(bound)
		none.Put(key("Page.md", "abc", false), entry("x"))

		if got := none.Len(); got != 0 {
			t.Errorf("NewCache(%d).Len() after one put = %d, want 0", bound, got)
		}
	}
}

// TestRenderCallsProduceOnceUnderConcurrency is the single-flight property.
//
// Twenty campaigns' worth of links to the same statblock arriving together is the
// normal case, not a stress test; without single-flight that is twenty goldmark
// passes and twenty sanitiser walks for one answer.
func TestRenderCallsProduceOnceUnderConcurrency(t *testing.T) {
	t.Parallel()

	const goroutines = 50

	cache := content.NewCache(64)
	k := key("Popular.md", "abc", false)

	var calls atomic.Int64

	// The producer can fail, because `Cache.Render` takes a producer that can
	// and unparam — correctly — says a closure which cannot is not exercised
	// properly. The test context is the failure source, and it never fires here.
	ctx := t.Context()

	release := make(chan struct{})
	entered := make(chan struct{}, 1)

	produce := func() (content.Entry, error) {
		calls.Add(1)

		// Hold the render open so every goroutine is definitely waiting on the
		// same flight rather than arriving after it finished.
		entered <- struct{}{}
		<-release

		if ctxErr := ctx.Err(); ctxErr != nil {
			return content.Entry{}, fmt.Errorf("test context done: %w", ctxErr)
		}

		return entry("<p>once</p>"), nil
	}

	var wg sync.WaitGroup

	results := make([]content.Entry, goroutines)

	for i := range goroutines {
		wg.Go(func() {
			got, err := cache.Render(k, produce)
			if err != nil {
				t.Errorf("Render: %v", err)
			}

			results[i] = got
		})
	}

	<-entered
	// Give the followers a moment to pile up on the flight. Deterministic enough
	// for a test that is about the count, and the count is asserted either way.
	time.Sleep(50 * time.Millisecond)
	close(release)

	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("produce was called %d times, want exactly 1", got)
	}

	for i, got := range results {
		if got.Rendered.HTML != "<p>once</p>" {
			t.Fatalf("goroutine %d got %q, want the shared entry", i, got.Rendered.HTML)
		}
	}
}

// TestRenderDoesNotBlockOnAnUnrelatedKey is the lock-holding bug.
//
// A slow render must not stop a *different* page being served. If `Render` held
// the write lock across `produce`, this would deadlock: the second call would
// wait for the first's produce, which waits for the second to release something
// it never acquired.
func TestRenderDoesNotBlockOnAnUnrelatedKey(t *testing.T) {
	t.Parallel()

	cache := content.NewCache(64)

	slow := key("Slow.md", "abc", false)
	fast := key("Fast.md", "def", false)

	holding := make(chan struct{})
	release := make(chan struct{})

	done := make(chan struct{})

	go func() {
		defer close(done)

		if _, err := cache.Render(slow, func() (content.Entry, error) {
			close(holding)
			<-release

			return entry("slow"), nil
		}); err != nil {
			t.Errorf("slow Render: %v", err)
		}
	}()

	<-holding

	// The unrelated key must complete while the slow one is still rendering.
	served := make(chan struct{})

	go func() {
		defer close(served)

		if _, err := cache.Render(fast, func() (content.Entry, error) {
			return entry("fast"), nil
		}); err != nil {
			t.Errorf("fast Render: %v", err)
		}
	}()

	select {
	case <-served:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("an unrelated key blocked behind a slow render: the lock is held across produce")
	}

	close(release)
	<-done
}

// TestRenderDoesNotCacheFailures: the cache is never a dependency.
func TestRenderDoesNotCacheFailures(t *testing.T) {
	t.Parallel()

	cache := content.NewCache(8)
	k := key("Broken.md", "abc", false)

	failure := errors.New("render exploded")

	if _, err := cache.Render(k, func() (content.Entry, error) {
		return content.Entry{}, failure
	}); !errors.Is(err, failure) {
		t.Fatalf("Render error = %v, want %v", err, failure)
	}

	if _, found := cache.Get(k); found {
		t.Error("a failed render was cached: a later request would be served the failure")
	}

	// And a retry must actually work.
	if _, err := cache.Render(k, func() (content.Entry, error) {
		return entry("recovered"), nil
	}); err != nil {
		t.Fatalf("retry: %v", err)
	}

	got, found := cache.Get(k)
	if !found || got.Rendered.HTML != "recovered" {
		t.Errorf("after retry: found=%v html=%q", found, got.Rendered.HTML)
	}
}

// TestInvalidateCampaignRemovesOnlyThatCampaign checks the scope of the
// watcher's hook: one campaign, not the whole cache.
func TestInvalidateCampaignRemovesOnlyThatCampaign(t *testing.T) {
	t.Parallel()

	cache := content.NewCache(16)

	one := content.CacheKey{CampaignID: 1, Path: "A.md", ContentHash: "h"}
	two := content.CacheKey{CampaignID: 2, Path: "A.md", ContentHash: "h"}

	cache.Put(one, entry("one"))
	cache.Put(two, entry("two"))

	cache.InvalidateCampaign(1)

	if _, found := cache.Get(one); found {
		t.Error("campaign 1's entry survived invalidation")
	}

	if _, found := cache.Get(two); !found {
		t.Error("campaign 2's entry was invalidated along with campaign 1's")
	}
}

// TestStatsCountEveryOutcome checks the §13.2 counters a `/readyz` scrape
// reads, on both the hit and the miss path.
func TestStatsCountEveryOutcome(t *testing.T) {
	t.Parallel()

	cache := content.NewCache(8)
	k := key("Page.md", "abc", false)

	before := cache.Stats()

	if _, err := cache.Render(k, func() (content.Entry, error) {
		return entry("x"), nil
	}); err != nil {
		t.Fatal(err)
	}

	afterMiss := cache.Stats()
	if afterMiss.Misses != before.Misses+1 {
		t.Errorf("a miss did not count: %d -> %d", before.Misses, afterMiss.Misses)
	}

	cache.Get(k)
	cache.Get(k)

	afterHits := cache.Stats()
	if afterHits.Hits != afterMiss.Hits+2 {
		t.Errorf("hits did not count: %d -> %d", afterMiss.Hits, afterHits.Hits)
	}
}

// TestStatsAreSafeUnderConcurrentTraffic, because `/readyz` reads them while every
// request is running and a data race there is a race on the whole process.
func TestStatsAreSafeUnderConcurrentTraffic(t *testing.T) {
	t.Parallel()

	cache := content.NewCache(64)

	var wg sync.WaitGroup

	stop := make(chan struct{})

	// A reader, as /readyz would be.
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = cache.Stats()
				_ = cache.Len()
			}
		}
	})

	for i := range 16 {
		wg.Go(func() {
			for range 200 {
				k := key(fmt.Sprintf("P%d.md", i%4), "h", i%2 == 0)
				_, _ = cache.Render(k, func() (content.Entry, error) {
					return entry("x"), nil
				})
			}
		})
	}

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	stats := cache.Stats()
	if stats.Hits+stats.Misses == 0 {
		t.Error("no requests were counted at all")
	}
}

// TestTwoVariantsOfOnePageDoNotCollide is the cache-level counterpart to
// TestETagSeparatesTheTwoVariants: the key carries the flag, so a GM's render and
// a player's render are two entries and neither is served to the other.
func TestTwoVariantsOfOnePageDoNotCollide(t *testing.T) {
	t.Parallel()

	cache := content.NewCache(8)

	redacted := key("Page.md", "abc", false)
	secret := key("Page.md", "abc", true)

	cache.Put(redacted, entry("<p>redacted body</p>"))
	cache.Put(secret, entry("<p>UNREDACTED body</p>"))

	gotRedacted, _ := cache.Get(redacted)
	gotSecret, _ := cache.Get(secret)

	if gotRedacted.Rendered.HTML == gotSecret.Rendered.HTML {
		t.Fatal("both variants returned the same body")
	}

	if gotRedacted.Rendered.HTML != "<p>redacted body</p>" {
		t.Errorf("a player was served %q", gotRedacted.Rendered.HTML)
	}

	if gotSecret.Rendered.HTML != "<p>UNREDACTED body</p>" {
		t.Errorf("a GM was served %q", gotSecret.Rendered.HTML)
	}

	// And the response directives differ, which is what stops a shared cache from
	// storing the GM's body under a validator the player also advertises.
	if content.CacheControl(redacted) == content.CacheControl(secret) {
		t.Error("both variants produced the same Cache-Control")
	}
}
