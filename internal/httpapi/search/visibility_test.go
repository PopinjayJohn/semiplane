package search_test

// S-8.2 and S-14.3, asserted end to end against a real database.
//
// # Why these are not in the fake-based file
//
// The property is a *row this reader must not see*. A fake has no tier, so it cannot
// express "this reader may not read this page": it answers the same rows to everybody,
// which makes every absence assertion against it either vacuous or an assertion that
// the *route* filtered — the thing S-8.2 forbids. So the index here is `*store.Store`
// with real campaigns, real memberships and real `pages_fts` rows, and the gate reads
// the same handle, so the tier a reader gets is the one `domain.ResolveAccess` derives
// from a membership row rather than one a test asserted.
//
// What the fake-based file asserts is the other half and the one a database cannot
// show: *what the route asked*. Passing the wrong campaign scope is invisible in the
// response — the store answers it correctly either way — and visible only in the
// recorded call.
//
// # One store for the whole package
//
// `store.Open` claims the process's single-instance slot by design (ADR 0004), so a
// fixture that opened its own could only ever be one per process and every second
// test would fail with "a Store is already open". Sharing one store and separating the
// tests by *campaign* is what makes them independent without fighting that guarantee,
// and the tests are therefore sequential — which is also what sharing one SQLite file
// requires. It costs wall-clock and buys a claim worth making: the single-instance
// rule is load-bearing enough that a suite quietly routing around it would be testing
// a configuration semiplane does not support.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// The two campaigns, and the terms only one of them contains.
//
// `greyhaven` is **public**, so an anonymous reader reaches its search route at all
// and a test measures the route rather than the gate. `curse` is **private**, so the
// rows that must never appear in a stranger's results are genuinely unreachable
// rather than merely labelled.
//
// Each term appears in exactly one page body, so a search for the *other* campaign's
// term inside this one matches nothing at all — and any appearance of the other
// campaign's marker in that response is a cross-tenant leak rather than a near miss.
const (
	publicSlug = "greyhaven"
	otherSlug  = "curse"

	// publicTerm is in `greyhaven`'s page title and body and nowhere else.
	publicTerm = "Glimmerling"
	// otherTerm is in `curse`'s page body and nowhere else.
	otherTerm = "Emberwrit"
	// otherMarker is in `curse`'s page body and in nothing else on the instance.
	otherMarker = "WARDENBARGAIN-7f31"
)

// sharedStore is the one database this package's tests use.
var sharedStore *store.Store

// TestMain opens the shared store and removes its directory afterwards.
//
// The instance is seeded lazily from the first test that needs it rather than here,
// because seeding needs a `*testing.T` — `t.TempDir`, `t.Fatal` — and `TestMain` has
// only an `exit` code. `sync.Once` makes the seed happen exactly once whichever test
// runs first.
func TestMain(m *testing.M) {
	code := func() int {
		dir, err := os.MkdirTemp("", "semiplane-search-test")
		if err != nil {
			panic("create the test database directory: " + err.Error())
		}

		defer func() {
			if removeErr := os.RemoveAll(dir); removeErr != nil {
				panic("remove the test database directory: " + removeErr.Error())
			}
		}()

		db, err := store.Open(context.Background(), "file:"+filepath.Join(dir, "semiplane.db"))
		if err != nil {
			panic("open the test store: " + err.Error())
		}

		defer func() {
			if closeErr := db.Close(); closeErr != nil {
				panic("close the test store: " + closeErr.Error())
			}
		}()

		sharedStore = db

		return m.Run()
	}()

	os.Exit(code)
}

// seedOnce guards the one-time instance seed.
//
// The seeded user ids are held in their own variables rather than reusing the
// harness's `gmUser`/`playerUser` constants, because a real database assigns ids by
// autoincrement and the harness's are hand-written. They are written exactly once,
// inside the `Once`, and read only after `seed` has returned — which `sync.Once`
// makes a happens-before edge — so the parallel fake-based tests, which use the
// constants and never read these, cannot race with the write.
var (
	seedOnce       sync.Once
	seededGMID     int64
	seededPlayerID int64
)

// seededGM is the seeded instance's GM of both campaigns.
func seededGM() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: seededGMID, Username: "mira"}
}

// seededPlayer is a member of the public campaign with the player role.
func seededPlayer() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: seededPlayerID, Username: "tobin"}
}

// seed creates the campaigns, users, memberships and pages the visibility assertions
// search for, exactly once per process.
//
// The GM of `greyhaven` is **also** the GM of `curse`, and that is the load-bearing
// choice: the scope test needs a reader who may read *both* campaigns, so the only
// thing that can keep `curse`'s rows off `greyhaven`'s search page is the campaign
// scope the route passed. A reader who could not read `curse` would prove nothing —
// the store would have excluded the row anyway, which is the visibility test's job
// and not the scope test's.
func seed(t *testing.T) {
	t.Helper()

	seedOnce.Do(func() {
		greyhaven := createCampaign(t, publicSlug, domain.VisibilityPublic)
		curse := createCampaign(t, otherSlug, domain.VisibilityPrivate)

		gm := createUser(t, "mira")
		player := createUser(t, "tobin")

		seededGMID = gm.ID
		seededPlayerID = player.ID

		membership(t, greyhaven.ID, gm.ID, domain.RoleGM)
		membership(t, greyhaven.ID, player.ID, domain.RolePlayer)
		membership(t, curse.ID, gm.ID, domain.RoleGM)

		indexPage(t, greyhaven.ID, "Adventures/Glimmerling Market.md",
			"Glimmerling Market",
			"Stalls, rumours and a Glim lantern over the "+publicMarker+".")
		indexPage(t, curse.ID, "Rites/Warden's bargain.md",
			"Warden's bargain",
			"The Emberwrit rite, and the price of the "+otherMarker+".")
	})
}

// TestAPrivateRowNeverReachesAReaderWhoMayNotReadIt is S-8.2 and S-14.3.
//
// **Asserted as an absence, and the positive case comes first**, twice over: a member
// of the private campaign finds the row when they search *that* campaign, and a reader
// of the public campaign finds the public row when they search *this* one. Without
// both, an absence is satisfied by an index that matches nothing — which is the
// failure mode a gate that 404s everything has, and this suite has one of those tests
// on purpose.
//
// Mutation: a route passing `CampaignID: 0` would *still* pass the anonymous case,
// because the store's join excludes a private campaign from an anonymous reader's
// rows even at instance scope. What it fails is `TestASearchNeverReturnsAnotherCampaignsRows`.
func TestAPrivateRowNeverReachesAReaderWhoMayNotReadIt(t *testing.T) {
	seed(t)

	t.Run("a member of the private campaign finds its row", func(t *testing.T) {
		recorder := newHarness(t).real(sharedStore).
			get("/c/"+otherSlug+"/search?q="+otherTerm, seededGM())

		if recorder.Code != http.StatusOK {
			t.Fatalf("the GM's search of %s = %d, want 200", otherSlug, recorder.Code)
		}

		if !strings.Contains(pageText(document(t, "curse, as GM", recorder)), otherMarker) {
			t.Fatalf("the GM's search of the private campaign does not contain %q, so "+
				"the negative assertion below would be satisfied by an index that "+
				"matches nothing", otherMarker)
		}
	})

	t.Run("a reader of the public campaign finds its own row", func(t *testing.T) {
		recorder := newHarness(t).real(sharedStore).
			get("/c/"+publicSlug+"/search?q="+publicTerm, anonymousRequestor())

		if recorder.Code != http.StatusOK {
			t.Fatalf("an anonymous search of a public campaign = %d, want 200; "+
				"§8.1 makes public visibility grant wiki read", recorder.Code)
		}

		if !strings.Contains(
			pageText(document(t, "greyhaven, anonymous", recorder)),
			publicMarker,
		) {
			t.Fatalf("the anonymous search does not contain %q, so the negative "+
				"assertion below would be vacuous", publicMarker)
		}
	})

	t.Run("an anonymous search carries no private row", func(t *testing.T) {
		recorder := newHarness(t).real(sharedStore).
			get("/c/"+publicSlug+"/search?q="+otherTerm, anonymousRequestor())

		rendered := document(t, "greyhaven, anonymous, other campaign's term", recorder)

		if strings.Contains(pageText(rendered), otherMarker) {
			t.Errorf("an anonymous search of the public campaign contains the private "+
				"marker %q. S-8.2 makes visibility a join rather than a filter the "+
				"route applies, and a bare query returning a private title is a "+
				"security failure, not a display bug (S-14.3)", otherMarker)
		}
	})

	t.Run("a player of the public campaign carries no private row", func(t *testing.T) {
		recorder := newHarness(t).real(sharedStore).
			get("/c/"+publicSlug+"/search?q="+otherTerm, seededPlayer())

		if strings.Contains(pageText(document(t, "greyhaven, player", recorder)), otherMarker) {
			t.Errorf("a player's search of their own campaign contains the private "+
				"marker %q; membership of one campaign is not membership of another "+
				"(S-8.2)", otherMarker)
		}
	})
}

// TestASearchNeverReturnsAnotherCampaignsRows is the scope, isolated from the
// visibility.
//
// The reader here **may** read both campaigns: they are the GM of `curse`, so every
// row they could have seen is a row the store would have returned at instance scope.
// The only thing that keeps `curse`'s rows off `/c/greyhaven/search` is the campaign
// scope the route passed — and the only thing that keeps `curse`'s rows off a route
// whose URL names one campaign is what the route asked for.
//
// This is the assertion that fails if `CampaignID: 0` ever reaches the store, and it
// is a *response* assertion rather than a recorded-call one because the consequence of
// a wrong scope is a reader being told about a campaign their URL does not name.
func TestASearchNeverReturnsAnotherCampaignsRows(t *testing.T) {
	seed(t)

	t.Run("the row is findable in its own campaign", func(t *testing.T) {
		recorder := newHarness(t).real(sharedStore).
			get("/c/"+otherSlug+"/search?q="+otherTerm, seededGM())

		if !strings.Contains(pageText(document(t, "the private campaign", recorder)), otherMarker) {
			t.Fatalf("the GM's own search of %s does not contain %q, so the assertion "+
				"below would be satisfied by an index that matches nothing",
				otherSlug, otherMarker)
		}
	})

	t.Run("and not on another campaign's search page", func(t *testing.T) {
		recorder := newHarness(t).real(sharedStore).
			get("/c/"+publicSlug+"/search?q="+otherTerm, seededGM())

		rendered := document(t, "the public campaign", recorder)

		if strings.Contains(pageText(rendered), otherMarker) {
			t.Errorf("a search of %s returned a row from %s to a reader who is a member "+
				"of both. store.PageSearch reads CampaignID 0 as \"every campaign the "+
				"requestor may read\", and a URL-scoped search that reaches it lists one "+
				"campaign's pages on another campaign's page",
				publicSlug, otherSlug)
		}
	})
}

// TestTheInstanceWideQueryIsNotOfferedByThisRoute is the surface half of the same
// rule: there is no way to ask for it from here.
//
// `store.PageSearch` documents `CampaignID: 0` and ADR 0007 describes a
// cross-campaign query; neither is reachable from this route's URL, and a reader who
// could pass the scope themselves could pass `0`.
func TestTheInstanceWideQueryIsNotOfferedByThisRoute(t *testing.T) {
	seed(t)

	recorder := newHarness(t).real(sharedStore).
		get("/c/"+publicSlug+"/search?q="+otherTerm+"&campaign=0&campaign_id=0&scope=all",
			gmRequestor())

	if strings.Contains(pageText(document(t, "a scope parameter", recorder)), otherMarker) {
		t.Errorf("a search with campaign-scoping parameters returned another campaign's " +
			"row; S-9 gives this route one URL and it carries a slug, so the scope is " +
			"the path and nothing else")
	}
}

// --- The seeding helpers --------------------------------------------------------

// createCampaign is a campaign with a content root under the store's test directory.
//
// Absolute because `CreateCampaign` refuses a relative one, and a fixture that
// tripped that guard would turn every test that seeded a campaign into a test of the
// guard.
func createCampaign(t *testing.T, slug string, visibility domain.Visibility) domain.Campaign {
	t.Helper()

	campaign, err := sharedStore.CreateCampaign(t.Context(), domain.Campaign{
		Slug:           slug,
		Name:           slug,
		ContentRoot:    filepath.Join(t.TempDir(), slug),
		Visibility:     visibility,
		SystemID:       "5e-2024",
		RulesetVersion: "1",
	})
	if err != nil {
		t.Fatalf("CreateCampaign(%q) error = %v, want nil", slug, err)
	}

	return campaign
}

// createUser is an account the test does not authenticate.
//
// The password hash is a literal because nothing here verifies hashing: that is
// `internal/httpapi/auth`'s job, and a test here that depended on it would fail for a
// reason that has nothing to do with search.
func createUser(t *testing.T, username string) domain.User {
	t.Helper()

	user, err := sharedStore.CreateUser(t.Context(), domain.User{
		Username:     username,
		PasswordHash: "not-a-real-hash",
	})
	if err != nil {
		t.Fatalf("CreateUser(%q) error = %v, want nil", username, err)
	}

	return user
}

// membership records a user's role in a campaign.
func membership(t *testing.T, campaignID, userID int64, role domain.Role) {
	t.Helper()

	if _, err := sharedStore.CreateMembership(t.Context(), domain.Membership{
		CampaignID: campaignID,
		UserID:     userID,
		Role:       role,
	}); err != nil {
		t.Fatalf("CreateMembership(%d, %d, %s) error = %v, want nil",
			campaignID, userID, role, err)
	}
}

// indexPage writes one page row and its FTS entry, which is what makes it findable.
//
// `UpsertPage` is the only writer of `pages` and it maintains `pages_fts` in the same
// transaction (S-11.2), so this is the whole of "make this page searchable" — no
// separate index step to forget.
func indexPage(t *testing.T, campaignID int64, pagePath, title, body string) {
	t.Helper()

	// `PageText` embeds `Page`, so its fields are written as promoted names rather
	// than as a nested `Page: domain.Page{…}` — the same shape `content/index.go`
	// writes, and the one the embedded-literal lint enforces.
	if _, err := sharedStore.UpsertPage(t.Context(), domain.PageText{
		CampaignID:  campaignID,
		Path:        pagePath,
		Kind:        domain.KindProse,
		Title:       title,
		ContentHash: "fixture-hash",
		ByteSize:    int64(len(body)),
		BodyPlain:   body,
	}); err != nil {
		t.Fatalf("UpsertPage(%d, %q) error = %v, want nil", campaignID, pagePath, err)
	}
}
