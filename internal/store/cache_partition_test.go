package store_test

// Cache partitioning: a rendered page must never be served to a reader who may not
// see it.
//
// Two other files already hold part of this claim, and neither of them is this one:
//
//   - `internal/httpapi/wiki/partition_test.go` asserts it over real HTTP
//     responses: two roles reading one page get two cache entries, two validators
//     and byte-identical bodies. It is the strongest form of the claim and it needs
//     a mounted router to make it.
//   - `internal/content/cache_test.go` holds the cache's own mechanics: eviction,
//     single-flight, the panic path.
//
// What is left is the seam between them, and it has to be true for the HTTP
// assertions to mean anything: **the validator is salted with the reveal state, the
// variant is part of the key, and the campaign is part of the key.** A wiki test
// that observed two validators would pass against a cache whose `Get` ignored
// `IncludeSecrets`; a cache test about entries would pass against a key that had
// lost the campaign. So the assertions below are on `content.CacheKey` and
// `content.Cache` — the two types the wiki route builds its response headers out of
// — with the content hash produced by the real store, because that hash is what
// "byte-identical files in two campaigns" means here rather than as a thought
// experiment.
//
// ADR 0016 and S-5.2/S-5.3/S-5.4 are the records. The property in one sentence: for
// one page, the GM's bytes and the player's bytes have different validators,
// different cache entries and different storage rules, and neither reader's entry
// can be returned for the other — including across two campaigns holding
// byte-identical files, which is the case the content hash alone cannot tell apart.
//
// One store is opened by `openTestStore`, which registers its own cleanup on `t`.
// `store.Open` claims the process's single-instance slot by design (ADR 0004), so
// these tests are sequential and share the fixture rather than opening their own.

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
)

// The two bodies, deliberately different, so that "an entry was found" and "the
// right entry was found" are not the same observation.
const (
	gmBody     = `<p>The traitor is Captain Aldric.</p>`
	playerBody = `<p>The tide came in and the door held.</p>`
)

// onePageSource is a page with a secret callout in it: the reason the two variants
// differ at all, and the document whose hash stands in for `pages.content_hash`.
const onePageSource = "---\ntitle: The Vault\n---\n" +
	"> [!secret]-\n> The traitor is Captain Aldric.\n"

// pageKeys returns the GM's and the player's cache key for one page.
//
// A value-returning pair rather than a function of the flag, because the two
// variants are the subject and a caller that built them itself could build them
// the same way twice and learn nothing.
func pageKeys(t *testing.T, campaignID int64, source string) (gm, player content.CacheKey) {
	t.Helper()

	digest := contentHash(source)

	gm = content.CacheKey{
		CampaignID:     campaignID,
		Path:           "lore/vault.md",
		ContentHash:    digest,
		IncludeSecrets: true,
	}

	player = content.CacheKey{
		CampaignID:     campaignID,
		Path:           "lore/vault.md",
		ContentHash:    digest,
		IncludeSecrets: false,
	}

	return gm, player
}

// TestTheValidatorIsSaltedWithTheRevealState is ADR 0016, asserted as the formula
// rather than as a difference.
//
// S-5.3 specifies the pre-image exactly: `sha256(content_hash + ":" +
// include_secrets)`, written as `true`/`false`. Asserting only that the two
// variants differ would be satisfied by *any* salt that separates them, including
// one a later edit made per-process, and that failure is invisible in review. So the
// formula is computed here independently and compared, which also pins:
//
//   - **The `:` separator.** Without it the encoding is ambiguous: a content hash of
//     `ab` with the flag false and one of `ab:false` with no flag are the same
//     pre-image, and a caller computing the digest any other way could mint a
//     validator that collides with another variant's.
//   - **The `true`/`false` spelling.** Two builds that disagreed would produce two
//     validators for one body, which is invisible until it leaks.
//   - **That the salt is not a secret.** The digest is recomputed from the
//     documented inputs alone, so a per-process random salt fails — and a validator
//     that changes on every restart is a validator that makes every browser refetch
//     every page after every deploy.
//
// The campaign id and the path are deliberately *not* in the pre-image, and that is
// asserted too, because the omission reads like a bug. The validator is a body
// validator for one URL: a cache stores by URL and uses the ETag only to decide
// whether its own stored body *for that URL* is current.
func TestTheValidatorIsSaltedWithTheRevealState(t *testing.T) {
	t.Parallel()

	digest := contentHash(onePageSource)

	for _, revealSecrets := range []bool{true, false} {
		key := content.CacheKey{
			CampaignID:     1,
			Path:           "lore/vault.md",
			ContentHash:    digest,
			IncludeSecrets: revealSecrets,
		}

		preimage := digest + ":" + strconv.FormatBool(revealSecrets)

		sum := sha256.Sum256([]byte(preimage))
		want := `W/"` + hex.EncodeToString(sum[:]) + `"`

		if got := key.ETag(); got != want {
			t.Errorf("ETag() for IncludeSecrets=%v = %s, want %s.\n"+
				"The salt is the whole of ADR 0016: without the reveal state in the "+
				"pre-image a GM response and a player response advertise one validator, "+
				"and any cache holding both serves whichever it stored first",
				revealSecrets, got, want)
		}
	}

	gmKey, playerKey := pageKeys(t, 1, onePageSource)

	// A validator that changed between two reads of an unchanged page would make
	// every conditional request a full transfer, so determinism is asserted over a
	// **second, independently built** key rather than by calling the method twice:
	// comparing a call with itself would be the shape of a test that cannot fail, and
	// it is the mutation a per-process salt would need to survive.
	rebuilt := content.CacheKey{
		CampaignID:     gmKey.CampaignID,
		Path:           gmKey.Path,
		ContentHash:    gmKey.ContentHash,
		IncludeSecrets: gmKey.IncludeSecrets,
	}

	if rebuilt.ETag() != gmKey.ETag() {
		t.Errorf("two keys with the same fields produced %s and %s: the validator is not "+
			"a function of the key, and a salt that varies per process makes every "+
			"browser refetch every page after every deploy",
			gmKey.ETag(), rebuilt.ETag())
	}

	if gmKey.ETag() == playerKey.ETag() {
		t.Fatalf("both variants of one page carry the validator %s", gmKey.ETag())
	}

	elsewhere := gmKey
	elsewhere.CampaignID = 2
	elsewhere.Path = "lore/other.md"

	if elsewhere.ETag() != gmKey.ETag() {
		t.Error("the validator changed with the campaign or the path; S-5.3 specifies " +
			"the pre-image exactly and a validator is a body validator rather than a " +
			"lookup key")
	}
}

// TestTheUnredactedRenderIsNeverHandedToTheOtherReader is the half of ADR 0016
// that the validator cannot enforce on its own.
//
// A salted validator stops a *shared* cache from confusing two bodies. It does
// nothing for this process's own cache: the lookup is by the whole key, and a `Get`
// that ignored `IncludeSecrets` would hand a player the GM's stored HTML under a
// validator the GM's own response also advertises — a correct-looking 200 carrying
// the wrong bytes, which is the failure the wiki partition test cannot see because
// it observes headers.
//
// So the entry is stored and then looked up the other way round, and the assertion
// is a **miss**. A test that only asked whether the GM's own lookup hit would pass
// against a cache whose key had lost the variant.
func TestTheUnredactedRenderIsNeverHandedToTheOtherReader(t *testing.T) {
	t.Parallel()

	gmKey, playerKey := pageKeys(t, 1, onePageSource)

	cache := content.NewCache(8)
	cache.Put(gmKey, content.Entry{Rendered: content.Rendered{HTML: gmBody}})

	if _, found := cache.Get(playerKey); found {
		t.Fatal("a player's key returned the GM's cached render: the variant is not " +
			"part of the cache key, so the second reader is served the first one's body")
	}

	// The positive, first in effect: without it every miss below would be explained
	// by a cache that stores nothing.
	if _, found := cache.Get(gmKey); !found {
		t.Fatal("the GM's own key missed after a Put, so the cache stores nothing and " +
			"the miss above proves nothing")
	}

	// The other direction, which is the one that costs a GM their own page: a
	// player's render must not satisfy the GM's lookup either.
	cache.Put(playerKey, content.Entry{Rendered: content.Rendered{HTML: playerBody}})

	if got := cache.Len(); got != 2 {
		t.Fatalf("the cache holds %d entries for two variants of one page, want 2", got)
	}

	for _, want := range []struct {
		key  content.CacheKey
		html string
		who  string
	}{
		{key: gmKey, html: gmBody, who: "GM"},
		{key: playerKey, html: playerBody, who: "player"},
	} {
		entry, found := cache.Get(want.key)
		if !found {
			t.Errorf("the %s's key missed after both variants were stored", want.who)

			continue
		}

		if entry.Rendered.HTML != want.html {
			t.Errorf("the %s's entry holds %q, want %q: one variant's entry answered "+
				"for the other", want.who, entry.Rendered.HTML, want.html)
		}
	}
}

// TestTwoCampaignsHoldingTheSameBytesAreTwoEntries is the campaign half of the
// partition (S-8.3), and it cannot be folded into the variant case because the
// validator does not carry the campaign id.
//
// The first test establishes that omission is deliberate, so the hazard is real:
// two campaigns whose files are byte-identical produce the *same* validator on
// purpose. That is safe for a shared cache, which stores by URL, and it is a hazard
// for this process's cache, which stores by key — if `CampaignID` were not part of
// the key then campaign B's page would be served out of campaign A's entry, and the
// reader would get a coherent page of the wrong campaign.
//
// The premise is checked rather than assumed: the two pages are written through the
// real `UpsertPage` and their stored hashes compared, because a test that asserted
// the partition for two pages that did not hash alike would be asserting nothing
// about it.
func TestTwoCampaignsHoldingTheSameBytesAreTwoEntries(t *testing.T) {
	db := openTestStore(t)

	first := seedVisibleCampaign(t, db, "greyhaven", domain.VisibilityPublic)
	second := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	const shared = "the wyvern circles svartalfheim"

	firstPage := seedPage(t, db, first, "lore/same.md", "The Wyvern", shared)
	secondPage := seedPage(t, db, second, "lore/same.md", "The Wyvern", shared)

	if firstPage.ContentHash != secondPage.ContentHash {
		t.Skipf("the two pages hash differently (%s and %s), so the identical-content "+
			"premise this test needs does not hold and its assertions would be vacuous",
			firstPage.ContentHash, secondPage.ContentHash)
	}

	digest := firstPage.ContentHash

	firstKey := content.CacheKey{
		CampaignID:  first.ID,
		Path:        firstPage.Path,
		ContentHash: digest,
	}

	secondKey := content.CacheKey{
		CampaignID:  second.ID,
		Path:        secondPage.Path,
		ContentHash: digest,
	}

	if firstKey == secondKey {
		t.Fatal("two campaigns produced one cache key: the campaign is not part of the " +
			"partition and a reader would be served the other campaign's page")
	}

	// The validator is the same for both, on purpose. Asserted rather than left
	// implicit, because a reader of the first test might reasonably assume the
	// campaign was missing from the key by accident.
	if firstKey.ETag() != secondKey.ETag() {
		t.Error("the two campaigns' validators differ; S-5.3's pre-image has no " +
			"campaign in it and a validator that moved when a page moved would " +
			"invalidate a body nobody changed")
	}

	cache := content.NewCache(8)
	cache.Put(firstKey, content.Entry{Rendered: content.Rendered{HTML: `<p>greyhaven</p>`}})

	if _, found := cache.Get(secondKey); found {
		t.Fatal("campaign B's key returned campaign A's entry")
	}

	// The invalidation is scoped too: dropping one campaign's entries must not take
	// the other's with it, or one GM's edit would empty the cache for everybody in
	// every other campaign.
	secretVariant := secondKey
	secretVariant.IncludeSecrets = true

	cache.Put(secretVariant, content.Entry{Rendered: content.Rendered{HTML: `<p>ashen</p>`}})

	cache.InvalidateCampaign(first.ID)

	if _, found := cache.Get(firstKey); found {
		t.Error("InvalidateCampaign left an entry behind for the campaign it named")
	}

	if _, found := cache.Get(secretVariant); !found {
		t.Error("InvalidateCampaign dropped an entry belonging to another campaign")
	}
}

// TestOnlyTheUnredactedVariantIsUnstorable is S-5.4, and it is the third layer of
// the same property.
//
// The salt stops a shared cache from serving one body under the other's validator,
// and the key stops this process from doing it. Neither helps if a shared cache is
// *allowed to store* the GM's body at all: the ordinary deployment for a
// self-hosted instance has a reverse proxy in front of it, and a proxy that stored
// an unredacted page serves it to a player for as long as the entry lived.
//
// Both directions are asserted. A policy that returned `no-store` for everything
// would pass a test that only checked the GM's row, and it would cost every player a
// full transfer on every navigation.
func TestOnlyTheUnredactedVariantIsUnstorable(t *testing.T) {
	t.Parallel()

	gmKey, playerKey := pageKeys(t, 1, onePageSource)

	const unstorable = "private, no-store"

	if got := content.CacheControl(gmKey); got != unstorable {
		t.Errorf("CacheControl(IncludeSecrets=true) = %q, want %q.\n"+
			"A reverse proxy in front of a self-hosted instance is the ordinary "+
			"deployment, and one that stored an unredacted page would serve it to a "+
			"player for as long as the entry lived", got, unstorable)
	}

	playerPolicy := content.CacheControl(playerKey)

	if strings.Contains(playerPolicy, "no-store") {
		t.Errorf("CacheControl(IncludeSecrets=false) = %q: a redacted body is "+
			"permission-neutral within its tier, and refusing to store it costs a full "+
			"transfer on every navigation for no security gain", playerPolicy)
	}

	if !strings.Contains(playerPolicy, "private") {
		t.Errorf("CacheControl(IncludeSecrets=false) = %q, want it to be private: "+
			"staying permission-neutral within a tier stops being true the moment the "+
			"campaign is made private while a shared cache still holds its pages",
			playerPolicy)
	}

	// `no-store` is the directive that stops the write and `private` states the
	// reason to a cache that honours only one of the two, so the GM's response needs
	// both. Asserted as a set rather than as a string so that the reason each half
	// is here is visible in the assertion rather than only in the comment.
	for _, directive := range []string{"private", "no-store"} {
		if !strings.Contains(unstorable, directive) {
			t.Errorf("the unstorable policy %q is missing %q", unstorable, directive)
		}
	}
}
