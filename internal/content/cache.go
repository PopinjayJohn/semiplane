// The render cache: one rendered page per (campaign, path, content hash,
// variant), bounded, and shared by every request in the process.
//
// This file is a security surface and not only a performance one, which is worth
// saying before anything else. ADR 0016 exists because the obvious way to build
// this — one cache entry per page, one validator per entry — puts a campaign
// GM's unredacted page into a cache under a validator a player's request also
// advertises, and any cache holding both then serves whichever it stored first.
// Every design decision below is downstream of that: the variant flag is in the
// key (S-5.2), the variant flag is in the validator (S-5.3), and the GM variant
// is never storable by a shared cache (S-5.4).
//
// Three more decisions are argued where they are made rather than here:
// CacheKey.ContentHash on why validity is the content hash and not the mtime,
// Cache on why eviction is random and not LRU, and Cache.Render on why
// single-flight is a per-key in-flight record and not a mutex held across
// produce.
//
// No logger, and no observability dependency. §12.1 makes `slog` plus the
// counters on `/readyz` the whole of the observability surface, and this cache
// contributes exactly one thing to it: Stats. Wiring those counters into
// `observability.Registry` is the composition root's job, and taking a
// *slog.Logger here would put a logging decision inside the hot path of every
// cache hit for no gain.

package content

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
)

const (
	// etagWeakPrefix opens a weak validator. Weak rather than strong because the
	// body is a composition of several bytes-per-byte-equivalent transforms —
	// goldmark, then the sanitiser, then a template — and re-deriving it is not
	// guaranteed to be free of a semantically irrelevant change. ADR 0016 ruled
	// the strength irrelevant to the defect it fixes; the collision was in the
	// value, not in the strength.
	etagWeakPrefix = `W/"`

	// etagSaltSeparator sits between the content hash and the variant flag
	// inside the hashed pre-image, per S-5.3's formula.
	//
	// A separator rather than concatenation, because concatenation makes the
	// encoding ambiguous: a content hash of `ab` with the flag false and one of
	// `ab:false` with an empty flag are the same bytes, and a caller that ever
	// computed a hash any other way could mint a validator that collides with
	// another variant's. The hashes are fixed-length hex in practice, so this is
	// belt and braces — but the salt is load-bearing and belt and braces is
	// what you want around load-bearing.
	etagSaltSeparator = ":"

	// cacheControlSecret is S-5.4's mandated value, verbatim and unnegotiable:
	// a response carrying secrets must never be storable by a shared cache under
	// any configuration, so it says both `private` and `no-store` rather than
	// relying on either alone.
	cacheControlSecret = "private, no-store"

	// cacheControlRedacted is the value for the player variant: storable, but only
	// by a private cache, and always revalidated.
	//
	// `private` is stricter than ADR 0016 requires. The ADR observes that a
	// redacted body is permission-neutral *within its tier* and therefore safe in
	// a shared cache — which is a permission, not an obligation, and it stops
	// being true the moment a campaign is made private while a shared cache is
	// still holding its pages. `no-cache` is what makes the ETag useful: the
	// browser stores the body and revalidates on every navigation, so the GM
	// navigating a wiki pays one conditional request per page rather than a full
	// render plus a full transfer, and nobody else's cache can hold the body at
	// all.
	cacheControlRedacted = "private, no-cache"
)

// ErrRenderFailed is what a waiter on a shared render receives when the
// goroutine producing that render panicked.
//
// A panic is not an answer, and it is certainly not an empty page: serving
// `Entry{}` to the waiters would answer every concurrent request for that page
// with a blank body and no error, which is the failure mode this whole file
// exists to make impossible. It is also deliberately not the recovered value.
// Whatever panicked belongs to the caller, and for this pipeline that text is
// derived from markdown in a vault (S-12.3); a waiter's error is not a place to
// put it. The goroutine that called produce still gets its panic, unchanged, so
// the middleware boundary recovers it, logs the stack and answers 500 exactly
// as it would for any other panic in a handler.
var ErrRenderFailed = errors.New("content: render panicked")

// CacheKey identifies one cached render.
//
// The four fields are the whole key, and each is there for a stated reason — put
// those reasons in the field comments. A fifth field is a bug until someone
// explains which of the four it could have replaced. The two that look
// over-specified are IncludeSecrets (S-5.2 says four fields, and it is four) and
// ContentHash (see its own comment for why a key without it is worse than no
// cache at all).
//
// Comparable by value, which is load-bearing rather than incidental: it is a map
// key directly, so a lookup is one hash of two words and two strings with no
// encoding step, no allocation, and no way for two call sites to build the same
// key in two spellings. Every field is normalised by its contract rather than by
// this package — in particular the caller, not `NewCache`, is responsible for
// Path being root-relative, slash-separated and without a leading slash, since
// normalising it here would mean normalising it after the file was already
// located and could therefore change which file a key names.
type CacheKey struct {
	// CampaignID is the campaign the page belongs to, and it partitions the cache
	// (S-8.3). Two campaigns holding byte-identical files get two entries, and no
	// campaign's entry is ever served to a request for another — which is the
	// property a shared cache cannot offer, and the reason the cache is keyed by
	// identity rather than by anything a URL could be scoped to.
	CampaignID int64

	// Path is the page's location within its campaign: root-relative,
	// slash-separated, with no leading slash. It disambiguates two pages in one
	// campaign, and it is a display and reporting field as much as a key part —
	// §12.3 attaches it to a log line.
	Path string

	// ContentHash is the hex SHA-256 of the file's bytes, and its presence in this
	// key is S-5.2's whole argument.
	//
	// **Validity is the content hash, never the modification time.** A vault
	// arrives by sync (S-3.1), and a sync path either preserves mtime from the
	// writing device or coarsens it — some clients round, some carry a coarse
	// clock, some restore a backup whose timestamps are the backup's. mtime is
	// therefore not a function of the content. Worse, it is not even injective on
	// content: 1-second granularity means two distinct writes inside one second
	// collide, so a page rewritten twice in a second keeps serving the first
	// version until a third write moves the timestamp again. A cache keyed on
	// `(campaign, path)` alone has the same defect permanently: it can never
	// notice that anything changed, because nothing about its key changes. The
	// hash is the only input to this key that is a function of the bytes, and the
	// caller has already computed it — it read the file to render it — so the key
	// costs one hex string that was being produced anyway.
	ContentHash string

	// IncludeSecrets selects the body variant: true for the campaign's GM, false
	// for everyone else (S-5.1). It is in the key because the two variants of one
	// page are two different bodies, and a cache that could return a player's
	// variant to a GM would be merely unhelpful while a cache that returned the
	// GM's variant to a player would be S-5.6's omission undone at the last
	// possible moment.
	IncludeSecrets bool
}

// ETag returns the validator for this key: W/"<sha256(content_hash + ':' + include_secrets)>".
//
// S-5.3 and ADR 0016. The salt is the *entire point*: without it a GM's
// unredacted response and a player's redacted one advertise the same validator,
// and any cache holding both serves whichever it stored first — a direct secret
// leak. `include_secrets` is written as `true`/`false`, not `1`/`0`, and that
// spelling is load-bearing: two builds that disagree produce two validators for
// one body, and the disagreement is invisible until it leaks.
//
// Two things the formula deliberately does *not* contain, both worth stating
// because they look like omissions:
//
//   - **CampaignID and Path.** The validator is a body validator, not a lookup
//     key, and S-5.3 specifies the pre-image exactly. A cache stores by URL and
//     uses the ETag only to decide whether *its own* stored body for *that* URL
//     is current, so two different URLs carrying the same validator confuse
//     nothing. Adding the campaign would also make the validator change when a
//     page moves, which would invalidate a body nobody changed.
//   - **A secret.** ADR 0016: the salt needs to be *different*, not
//     unpredictable. An unpredictable salt would be a per-process value, and a
//     validator that changes on every restart is a validator that makes every
//     browser refetch every page after every deploy.
func (k CacheKey) ETag() string {
	preimage := k.ContentHash + etagSaltSeparator + strconv.FormatBool(k.IncludeSecrets)

	digest := sha256.Sum256([]byte(preimage))

	return etagWeakPrefix + hex.EncodeToString(digest[:]) + `"`
}

// CacheControl returns the Cache-Control header value for a response carrying
// this key's variant.
//
// S-5.4: `private, no-store` on every include_secrets=true response. A redacted
// page is permission-neutral *within its tier*, so the redacted variant is
// cacheable and only the GM's is not. A function rather than a middleware
// because the value is a property of the key, and a middleware would have to be
// told which variant it is looking at.
//
// Both values are named constants at the top of this file, with the reasoning
// for the redacted one — `private, no-cache`, stricter than ADR 0016's table
// asks for — stated there rather than here.
func CacheControl(key CacheKey) string {
	if key.IncludeSecrets {
		return cacheControlSecret
	}

	return cacheControlRedacted
}

// Entry is one cached render.
//
// One field, and the reason is that everything else a response needs about a
// page is either already in the key — the content hash the validator is built
// from, the variant the Cache-Control value is built from — or is not a property
// of the render at all. A page's title comes from its front matter, which the
// caller read from the file in order to compute ContentHash in the first place,
// and the HTML is the render.
//
// A field added here is a field with an eviction, a copy on every Put, and two
// body variants to keep in step, so it should be a field whose absence would
// force a caller to re-derive something rather than a field that is merely
// convenient.
type Entry struct {
	// Rendered is what `Renderer.Render` returned, held by value rather than by
	// pointer: a cached entry outlives the call that produced it, so it cannot
	// borrow the renderer's memory or the caller's.
	//
	// **The two slices inside it are shared.** Every `Get` and every `Render`
	// hands back the same backing arrays, so a caller must treat `References` and
	// `Anchors` as read-only. Copying them on every read would allocate twice per
	// hit, which is the cost the cache exists to avoid, and the alternative — a
	// private copy nobody can mutate — is not a property a Go value can have.
	Rendered Rendered
}

// Stats are the counters §13.2 reports as cache.hit and cache.miss.
//
// One increment per lookup, and never two. `Get` counts one, either way.
// `Render` counts one: a call answered from the cache is a hit, and a call that
// became the leader of a flight — the one that actually called produce — is a
// miss. A caller that waited on somebody else's flight is a hit, because its
// answer came from memory and no render ran for it; counting it as a miss as
// well would make the miss count exceed the number of pages that were never
// cached, and a hit ratio built from those two numbers is the number
// `observability.EventCacheHit` exists to be trustworthy.
//
// Read without stopping the world, and see Cache's counter fields for why.
type Stats struct {
	// Hits counts lookups answered from memory.
	Hits uint64

	// Misses counts lookups that found nothing in the cache.
	Misses uint64
}

// flight is one render in progress, shared by whichever goroutines asked for it
// at the same time.
//
// `entry` and `err` are written by the leader without holding the cache mutex,
// and read by the waiters after `done` is closed. That is safe because a channel
// close is a happens-before edge: everything the leader wrote before the close is
// visible to every goroutine that receives from it, and the race detector sees
// the same edge. No lock is needed for the answer, which is the point — a waiter
// must not queue behind the mutex that the leader is about to take again.
type flight struct {
	done  chan struct{}
	entry Entry
	err   error
}

// Cache is a bounded, concurrency-safe render cache.
//
// **Eviction policy: random replacement.** When the cache is full, one entry is
// dropped and the new one takes its place, with no regard for which.
//
// The argument, in the order the alternatives fail it:
//
//   - **LRU is worse here, not marginally.** An LRU is a read-mostly structure
//     that pays a write on every read: each `Get` has to touch the recency list,
//     so the cache's own mutex is on the request path for a reason unrelated to
//     answering the request. This cache's whole job is to keep a goldmark pass
//     and a sanitiser walk off the request path, and the difference between a
//     few nanoseconds of map access and a few nanoseconds of map access plus a
//     list splice is nothing against that.
//   - **The hit rate a better policy would win is not measurable at this size,
//     and the size is the design.** A campaign is a vault: hundreds of pages, a
//     few megabytes of rendered HTML, and a working set that is one campaign's
//     worth of a few thousand entries at most. At that size the bound is never
//     reached in normal operation, so the policy never runs; the failure mode
//     being designed for is a pathologically large campaign, and there the right
//     answer is a bigger `maxEntries` from configuration, not a cleverer eviction.
//   - **And crucially, an eviction here cannot serve wrong content.** Because
//     `ContentHash` is in the key, a dropped entry costs one re-render on the next
//     request and nothing else: no reader can be handed a stale body, ever, at
//     any eviction rate. That is the property that makes a policy this cheap
//     acceptable, and it is exactly the property LRU does not buy. A cache whose
//     eviction could produce a wrong answer would need the good policy; this one
//     does not.
//
// A plain `map` plus a counter is therefore not merely adequate, it is the right
// amount of machinery: the bound is enforced in `storeLocked`, the mutex is a
// `sync.RWMutex` over a typed map for the reasons `Registry.roots` gives, and
// `sync.Map` is rejected here for the same reason — it stores `any`, so every
// read is a type assertion that can fail at request time instead of at
// construction, and its write-amortised behaviour buys nothing when the write is
// already once per render.
//
// **maxEntries <= 0 stores nothing.** Not "unbounded", which is the reading a
// caller expects from a zero value and the one that turns a misconfigured limit
// into a memory leak with a trigger; and not an error, which would make a
// disabled cache impossible to construct for a test and would put a failure mode
// on a path whose whole contract is that it cannot fail. `Put` becomes a no-op,
// `Get` always misses, `Len` is always zero, and `Render` still works — it simply
// produces on every call. That is the safe direction for every mistake that
// produces it: a missing or negative configuration value disables a cache, and a
// disabled cache is slower, not wrong.
type Cache struct {
	// maxEntries is the bound on `len(entries)`, interpreted as documented on
	// the type. Read-only after NewCache.
	maxEntries int

	// mu guards both maps. Entries are read once per request and written once per
	// render, so the read side carries the traffic and RLock is the common case.
	mu      sync.RWMutex
	entries map[CacheKey]Entry

	// inflight is the single-flight registry: a key with a record here is a render
	// running right now. A separate map from entries rather than a second field
	// on the entry because a flight exists before there is anything to cache and
	// ceases to exist once there is.
	inflight map[CacheKey]*flight

	// evictions counts evictions, and is only touched under the write lock. It
	// picks which of a freshly collected key slice to drop; see evictLocked.
	evictions uint64

	// hits and misses are atomics rather than fields under `mu`, and the reason
	// is that Stats must be readable at any moment without stopping the world:
	// `/readyz` reports them (S-12.1) while every reader is running. An
	// `RWMutex` would be correct — these are plain uint64 under a lock — but it
	// would make every request contend on the same lock a `/readyz` scrape takes,
	// for a counter with no invariant across the two fields. `Add` is a single
	// atomic instruction on every platform this runs on, and there is no
	// read-modify-write relationship between a hit and a miss to protect.
	hits   atomic.Uint64
	misses atomic.Uint64
}

// NewCache returns a Cache holding at most maxEntries renders.
//
// maxEntries <= 0 yields a cache that stores nothing; see Cache for why that is
// the documented reading rather than an error or an unbounded cache.
func NewCache(maxEntries int) *Cache {
	return &Cache{
		maxEntries: maxEntries,
		entries:    make(map[CacheKey]Entry),
		inflight:   make(map[CacheKey]*flight),
	}
}

// Get returns the cached entry for key, and whether it was there.
//
// The boolean is the only signal. It is there because a cached render can
// legitimately *be* the zero value — a page whose body is empty renders to empty
// HTML with empty slices — and a caller that tested the entry instead would serve
// a wrong "not cached" answer for exactly those pages, then re-render them on
// every request forever.
//
// Every call counts, one way or the other, in the counters `Stats` reports.
func (c *Cache) Get(key CacheKey) (Entry, bool) {
	entry, found := c.lookup(key)
	if !found {
		c.misses.Add(1)

		return Entry{}, false
	}

	c.hits.Add(1)

	return entry, true
}

// Put stores entry under key, evicting one entry if the cache is full.
//
// Replaces whatever was under key without counting as an insert, so re-putting a
// key that is already present never evicts anything. That matters more than it
// sounds: `Render` publishes through this path, and a caller that re-renders the
// same page on a change would otherwise evict a neighbour each time it did.
//
// Nothing is copied defensively beyond what the struct field comments already
// say about the two slices, and nothing is validated — a `Put` of an entry built
// from a different key's content is a caller bug that only the caller can make,
// and this package is not in a position to detect it.
func (c *Cache) Put(key CacheKey, entry Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.storeLocked(key, entry)
}

// InvalidateCampaign drops every cached render belonging to campaignID.
//
// **This is a memory optimisation, not a correctness mechanism**, and the
// distinction is the reason the method can be wrong in isolation without
// consequence. The content hash is in the key (S-5.2), so a page whose file
// changed produces a different key the moment it is next read and misses
// naturally: nothing served after an edit can be stale whether or not this is ever
// called. What it cannot do is *reclaim* the entries for the old content, and
// that is the whole reason it exists — a campaign edited a hundred times would
// otherwise hold a hundred renders of the same page until random eviction
// happened to reach them.
//
// In-flight renders are not cancelled and are not waited for, deliberately. A
// flight that started before the invalidation publishes its result afterwards,
// under the content hash it was rendered from, so it re-inserts an entry that is
// already unreachable — a memory cost for one request's lifetime and never a
// wrong answer. Cancelling it would mean abandoning a render half way and
// blocking the watcher on whoever else is waiting for that page.
//
// Deleting from a map while ranging over it is defined behaviour in Go, and it is
// the only way to do this without collecting the victims first.
func (c *Cache) InvalidateCampaign(campaignID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key := range c.entries {
		if key.CampaignID == campaignID {
			delete(c.entries, key)
		}
	}
}

// Len returns how many entries the cache holds.
//
// The number `maxEntries` bounds. `render` in flight is not counted, because an
// entry nobody can read yet is not one the bound is about.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.entries)
}

// Stats returns the hit and miss counters.
//
// Two independent `Load`s, so the pair is not a consistent snapshot of a single
// instant. That is the right trade for a ratio reported on a scrape endpoint: the
// two loads are adjacent, the counters are monotonic, and the alternative — a
// lock across both — would put a request-path write behind every `/readyz` read
// for the sake of a consistency no reader of a hit ratio can detect.
func (c *Cache) Stats() Stats {
	return Stats{
		Hits:   c.hits.Load(),
		Misses: c.misses.Load(),
	}
}

// Render returns the cached entry for key, calling produce exactly once if absent.
//
// This is the single-flight seam: N concurrent requests for the same uncached
// page must cause ONE render, not N. Two requests for the same page arriving
// together is the normal case for a campaign list with twenty links to the same
// statblock, and without single-flight that is twenty goldmark passes and twenty
// sanitiser walks for one answer.
//
// The cache is never a dependency: if `produce` fails, the error is returned and
// nothing is cached. A later request retries. A cache that could fail a request
// is a cache that takes the wiki down when it evicts something.
//
// **No lock is held while produce runs.** That is the difference between this and
// a mutex around the whole operation, and holding one would be two bugs at once:
// every request in the process would queue behind whichever page was rendering,
// and — worse — a cache lookup for an unrelated page would block behind it too,
// so one slow page would stall the whole wiki rather than itself. The mechanism
// is therefore a per-key flight record instead:
//
//   - `lookup` first, on the read lock. The common case never writes anything.
//   - Then the write lock, taken only long enough to find or create the flight for
//     this key. Everything expensive happens outside it.
//   - The first goroutine to register a flight becomes its leader and calls
//     produce. The rest become waiters on that flight's `done` channel, which is
//     closed once the result is final, and then return the leader's answer —
//     including its error, so fifty concurrent requests for a page that will not
//     render get one render attempt and fifty copies of its error rather than
//     fifty attempts.
//
// The double read of `c.entries` is not redundancy. Between the unlocked `lookup`
// and the write lock, a concurrent `Render` or `Put` may have stored the very
// entry this call was about to produce; re-reading under the lock is what makes
// "exactly one render per key" a property of the lock rather than of a race that
// happens not to fire under load.
//
// `produce` must not ask this cache for the same key. A leader's own call would
// find its own flight, wait on its own channel, and deadlock; a key it is not the
// leader for is fine, and so is any other operation on the cache, because no lock
// is held while it runs.
func (c *Cache) Render(key CacheKey, produce func() (Entry, error)) (Entry, error) {
	cached, found := c.lookup(key)
	if found {
		c.hits.Add(1)

		return cached, nil
	}

	c.mu.Lock()

	if stored, present := c.entries[key]; present {
		c.mu.Unlock()
		c.hits.Add(1)

		return stored, nil
	}

	shared, joined := c.inflight[key]
	if !joined {
		shared = &flight{done: make(chan struct{})}
		c.inflight[key] = shared
	}

	c.mu.Unlock()

	if joined {
		<-shared.done
		c.hits.Add(1)

		return shared.entry, shared.err
	}

	return c.render(key, shared, produce)
}

// lookup reads one entry without counting anything, and without taking more than
// the read lock.
//
// Split out of `Get` so `Render` can share the read path without sharing the
// accounting: `Render` counts once, at the point where it knows whether it became
// the leader, and routing it through `Get` would count a miss on the way in and
// then a hit on the way out for the same call.
func (c *Cache) lookup(key CacheKey) (Entry, bool) {
	c.mu.RLock()

	entry, ok := c.entries[key]

	c.mu.RUnlock()

	return entry, ok
}

// render is the leader's half of a single-flight: run produce, publish the
// result, and release the waiters.
//
// The `defer` is armed before produce is called so it covers produce itself, and
// `release` is a separate function rather than a closure so that its `recover`
// runs *directly* in a deferred function — which is the only place `recover` has
// any effect.
func (c *Cache) render(
	key CacheKey,
	shared *flight,
	produce func() (Entry, error),
) (Entry, error) {
	c.misses.Add(1)

	defer c.release(key, shared)

	entry, err := produce()

	c.mu.Lock()

	if err == nil {
		c.storeLocked(key, entry)
	}

	c.mu.Unlock()

	shared.entry, shared.err = entry, err

	return entry, err
}

// release removes a flight from the registry and wakes its waiters.
//
// **The panic path is the reason this is not three lines of `defer`.** Without
// it, a panic inside produce leaves the flight in `inflight` forever: every later
// request for that key finds it, waits on a channel nobody will ever close, and
// hangs. One panic in one render would become a permanently unavailable page
// instead of a 500 — a cache turning a bug into an outage, which is the one thing
// this file is not allowed to do.
//
// The panic is re-raised rather than converted, so the goroutine that called
// produce still fails the way any panicking handler fails: the middleware
// boundary recovers it, logs the stack and answers 500. Swallowing it here would
// report success for a render that never happened.
func (c *Cache) release(key CacheKey, shared *flight) {
	recovered := recover()

	if recovered != nil {
		shared.entry, shared.err = Entry{}, ErrRenderFailed
	}

	c.mu.Lock()

	delete(c.inflight, key)

	c.mu.Unlock()

	close(shared.done)

	if recovered != nil {
		panic(recovered)
	}
}

// storeLocked puts entry under key, evicting one unrelated entry when the cache
// is full. The caller holds the write lock.
func (c *Cache) storeLocked(key CacheKey, entry Entry) {
	if c.maxEntries <= 0 {
		return
	}

	_, replacing := c.entries[key]
	if !replacing && len(c.entries) >= c.maxEntries {
		c.evictLocked()
	}

	c.entries[key] = entry
}

// evictLocked drops one entry, chosen by no policy at all.
//
// The keys are collected because the map has no way to be indexed directly, and
// the victim is picked by walking that slice with a counter rather than by
// reaching for `math/rand`: Go randomises map iteration order, so a fresh
// traversal is in a different order every time and any position in it is as good
// a choice as any other. That buys a random victim with no PRNG, no seeding
// question, and no `gosec` suppression to argue about — and per Cache's eviction
// notes, the difference between this and a genuinely optimal choice is not one a
// reader of this file can observe in the output.
//
// The collection allocates once per eviction. On the miss path that is a slice
// the size of the bound, which is real garbage churn, and it is the price of not
// keeping a ring buffer of insertion order in sync for a policy whose result
// cannot affect correctness.
func (c *Cache) evictLocked() {
	keys := slices.Collect(maps.Keys(c.entries))
	if len(keys) == 0 {
		return
	}

	victim := keys[c.evictions%uint64(len(keys))]
	c.evictions++

	delete(c.entries, victim)
}
