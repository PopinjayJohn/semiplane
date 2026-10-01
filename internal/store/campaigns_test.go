package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// TestCampaignRoundTrip is the round trip, over a campaign carrying every column
// this phase settled on.
//
// The two columns a later phase would otherwise have had to ALTER in are set
// here -- system_id and ruleset_version -- because a fixture that left them
// empty would keep a change to their handling from being noticed until phase 8.
func TestCampaignRoundTrip(t *testing.T) {
	db := openTestStore(t)

	want := domain.Campaign{
		Slug:           "ashen-coast",
		Name:           "The Ashen Coast",
		ContentRoot:    t.TempDir(),
		Visibility:     domain.VisibilityPublic,
		SystemID:       "5e-2024",
		RulesetVersion: "2024.2",
		CreatedAt:      pinnedTime(),
	}

	created, err := db.CreateCampaign(t.Context(), want)
	if err != nil {
		t.Fatalf("CreateCampaign() error = %v, want nil", err)
	}

	if created.ID <= 0 {
		t.Errorf("CreateCampaign() returned id %d, want a positive rowid", created.ID)
	}

	got, err := db.CampaignBySlug(t.Context(), want.Slug)
	if err != nil {
		t.Fatalf("CampaignBySlug() error = %v, want nil", err)
	}

	assertCampaignEqual(t, got, created)
}

// TestCampaignVisibilityRoundTrip covers both visibilities through the domain's
// parser. The stored text is what `domain.Visibility.String()` produced and the
// read value is what `ParseVisibility` made of it, so a round trip through a
// value the domain does not recognise is a read-time error -- not a campaign
// handed back with a Visibility this build cannot reason about.
func TestCampaignVisibilityRoundTrip(t *testing.T) {
	cases := []struct {
		name       string
		visibility domain.Visibility
	}{
		{name: "private", visibility: domain.VisibilityPrivate},
		{name: "public", visibility: domain.VisibilityPublic},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := openTestStore(t)

			created, err := db.CreateCampaign(t.Context(), domain.Campaign{
				Slug:        "visibility-" + testCase.name,
				Name:        testCase.name,
				ContentRoot: t.TempDir(),
				Visibility:  testCase.visibility,
				SystemID:    "5e-2024",
			})
			if err != nil {
				t.Fatalf("CreateCampaign() error = %v, want nil", err)
			}

			got, err := db.CampaignBySlug(t.Context(), created.Slug)
			if err != nil {
				t.Fatalf("CampaignBySlug() error = %v, want nil", err)
			}

			if got.Visibility != testCase.visibility {
				t.Errorf("Visibility = %q, want %q", got.Visibility, testCase.visibility)
			}
		})
	}
}

// TestCreateCampaignRejectsDuplicateSlug covers slug uniqueness as the database
// enforces it.
//
// This is the constraint campaign registration leans on: two campaigns differing
// only in slug are two tenants as far as a URL is concerned, so the loser of a
// race has to learn that it lost rather than quietly replacing the winner.
func TestCreateCampaignRejectsDuplicateSlug(t *testing.T) {
	db := openTestStore(t)

	seedCampaign(t, db, "taken-slug")

	_, err := db.CreateCampaign(t.Context(), domain.Campaign{
		Slug:        "taken-slug",
		Name:        "Second",
		ContentRoot: t.TempDir(),
		Visibility:  domain.VisibilityPrivate,
		SystemID:    "5e-2024",
	})

	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("CreateCampaign() of a taken slug = %v, want one wrapping ErrConflict", err)
	}
}

// TestCreateCampaignRejectsInvalidSlug covers the form check, which the database
// does not and cannot make: uniqueness is a storage property, the character set
// is domain.ValidateSlug's, and a slug reaches a URL and a cache key either way.
func TestCreateCampaignRejectsInvalidSlug(t *testing.T) {
	cases := []struct {
		name string
		slug string
	}{
		{name: "empty", slug: ""},
		{name: "uppercase", slug: "Taken"},
		{name: "leading hyphen", slug: "-taken"},
		{name: "consecutive hyphens", slug: "ta--ken"},
		{name: "path separator", slug: "a/b"},
		{name: "non-ascii", slug: "café"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := openTestStore(t)

			_, err := db.CreateCampaign(t.Context(), domain.Campaign{
				Slug:        testCase.slug,
				Name:        testCase.name,
				ContentRoot: t.TempDir(),
				Visibility:  domain.VisibilityPrivate,
				SystemID:    "5e-2024",
			})

			if !errors.Is(err, domain.ErrInvalidSlug) {
				t.Errorf("CreateCampaign(%q) = %v, want one wrapping domain.ErrInvalidSlug",
					testCase.slug, err)
			}
		})
	}
}

// TestCreateCampaignRejectsRelativeContentRoot covers the path-confinement guard.
//
// The content root becomes the os.Root boundary for the whole campaign. A
// relative value would resolve against whatever directory the process was started
// in, so the same database would confine two deployments differently -- which is a
// security property that only shows up on the deployment nobody tested.
func TestCreateCampaignRejectsRelativeContentRoot(t *testing.T) {
	cases := []struct {
		name        string
		contentRoot string
	}{
		{name: "relative", contentRoot: "vaults/ashen-coast"},
		{name: "dot relative", contentRoot: "./vault"},
		{name: "empty", contentRoot: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := openTestStore(t)

			_, err := db.CreateCampaign(t.Context(), domain.Campaign{
				Slug:        "root-" + slugify(testCase.name),
				Name:        testCase.name,
				ContentRoot: testCase.contentRoot,
				Visibility:  domain.VisibilityPrivate,
				SystemID:    "5e-2024",
			})

			if !errors.Is(err, store.ErrInvalidContentRoot) {
				t.Errorf("CreateCampaign() with content root %q = %v, want one wrapping "+
					"ErrInvalidContentRoot", testCase.contentRoot, err)
			}
		})
	}
}

// TestCampaignBySlugMissing covers ErrNotFound. A slug nobody registered is the
// 404 the access matrix is built from, and it must be reachable as a value rather
// than as a message.
func TestCampaignBySlugMissing(t *testing.T) {
	db := openTestStore(t)

	_, err := db.CampaignBySlug(t.Context(), "no-such-campaign")

	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("CampaignBySlug() of an unknown slug = %v, want one wrapping ErrNotFound", err)
	}
}

// TestCampaignsForUserListsOnlyMemberships covers the list the campaign-list route
// is built from.
//
// A public campaign the user is not a member of is reachable by its slug and
// through the search that joins on visibility. Listing it here would say the user
// belongs to a campaign they hold no role in, which is the kind of claim a
// campaign list is read as making.
func TestCampaignsForUserListsOnlyMemberships(t *testing.T) {
	db := openTestStore(t)

	viewer := seedUser(t, db, "viewer")
	outsider := seedUser(t, db, "outsider")

	zulu := seedCampaign(t, db, "zulu-campaign")
	alpha := seedCampaign(t, db, "alpha-campaign")
	joined := seedCampaign(t, db, "joined-campaign")

	seedMembership(t, db, zulu.ID, viewer.ID, domain.RolePlayer)
	seedMembership(t, db, alpha.ID, viewer.ID, domain.RoleGM)
	seedMembership(t, db, joined.ID, outsider.ID, domain.RoleGM)

	got, err := db.CampaignsForUser(t.Context(), viewer.ID)
	if err != nil {
		t.Fatalf("CampaignsForUser() error = %v, want nil", err)
	}

	wantSlugs := []string{"alpha-campaign", "zulu-campaign"}

	if len(got) != len(wantSlugs) {
		t.Fatalf("CampaignsForUser() returned %d campaigns, want %d", len(got), len(wantSlugs))
	}

	for idx, want := range wantSlugs {
		if got[idx].Slug != want {
			t.Errorf("campaigns[%d].Slug = %q, want %q; the list is ordered by slug",
				idx, got[idx].Slug, want)
		}
	}

	if got[1].ID != zulu.ID || got[0].ID != alpha.ID {
		t.Error("CampaignsForUser() returned rows that are not the ones seeded")
	}
}

// TestCampaignsForUserEmpty covers the no-memberships case, which is a new
// account's first request rather than an error condition. It must be an empty
// slice: a nil slice and an empty one render identically, and a caller reaching
// for the first element wants an empty range, not a panic.
func TestCampaignsForUserEmpty(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "newcomer")

	got, err := db.CampaignsForUser(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("CampaignsForUser() error = %v, want nil", err)
	}

	if len(got) != 0 {
		t.Errorf("CampaignsForUser() returned %d campaigns, want 0", len(got))
	}
}

// TestCampaignIDsAreNotReused is the guard behind leaving `campaign_state` without
// a foreign key.
//
// `campaign_state` (migration 0002) references `campaigns(id)` and has no foreign
// key, because `campaigns` did not exist when it was written. With a bare rowid,
// deleting a campaign frees its id for the next one registered -- and that campaign
// would inherit the dead one's live game state as its opening position. The
// AUTOINCREMENT on `campaigns.id` is what makes that impossible, and this test is
// what would notice if the declaration were ever dropped.
func TestCampaignIDsAreNotReused(t *testing.T) {
	db := openTestStore(t)

	first := seedCampaign(t, db, "first-campaign")

	if err := db.DeleteCampaign(t.Context(), first.ID); err != nil {
		t.Fatalf("DeleteCampaign() error = %v, want nil", err)
	}

	second := seedCampaign(t, db, "second-campaign")

	if second.ID <= first.ID {
		t.Errorf("second campaign id = %d, want greater than the deleted %d; a reused id "+
			"would hand the first campaign's orphaned rows to a different tenant",
			second.ID, first.ID)
	}
}

// TestDeleteCampaignCascadesMemberships covers the cascade on
// campaign_members.campaign_id. A membership names a campaign's privilege, so
// leaving the row behind is not a stale record -- it is a grant against a tenant
// that no longer exists, waiting for a rowid to be reused.
func TestDeleteCampaignCascadesMemberships(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "member")
	campaign := seedCampaign(t, db, "doomed")

	seedMembership(t, db, campaign.ID, user.ID, domain.RoleGM)

	if err := db.DeleteCampaign(t.Context(), campaign.ID); err != nil {
		t.Fatalf("DeleteCampaign() error = %v, want nil", err)
	}

	if _, err := db.Membership(
		t.Context(),
		campaign.ID,
		user.ID,
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Errorf("Membership() after the campaign was deleted = %v, want ErrNotFound", err)
	}

	campaigns, err := db.CampaignsForUser(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("CampaignsForUser() error = %v, want nil", err)
	}

	if len(campaigns) != 0 {
		t.Errorf(
			"CampaignsForUser() returned %d campaigns after the cascade, want 0",
			len(campaigns),
		)
	}
}

// TestDeleteCampaignMissingIsSuccess covers the idempotent delete, for the same
// reason DeleteUser's does.
func TestDeleteCampaignMissingIsSuccess(t *testing.T) {
	db := openTestStore(t)

	if err := db.DeleteCampaign(t.Context(), 4242); err != nil {
		t.Errorf("DeleteCampaign() of an absent campaign = %v, want nil", err)
	}
}

// TestCampaignCreatedAtIsAnInteger repeats the storage-class assertion for the
// tenancy table; see TestUserCreatedAtIsAnInteger for why it matters.
func TestCampaignCreatedAtIsAnInteger(t *testing.T) {
	db := openTestStore(t)

	campaign := seedCampaign(t, db, "storage-class-campaign")

	var storageClass string

	err := db.DB().
		QueryRowContext(t.Context(), "SELECT typeof(created_at) FROM campaigns WHERE id = ?",
			campaign.ID).
		Scan(&storageClass)
	if err != nil {
		t.Fatalf("read typeof(created_at): %v", err)
	}

	if storageClass != "integer" {
		t.Errorf("created_at has storage class %q, want integer", storageClass)
	}
}

// TestCampaignStampsCreatedAt covers the zero-time default, and checks the
// stored value falls inside this test's own window rather than merely being
// non-zero: Unix(0) is not a zero time.Time, so a check for IsZero would pass a
// row that sorts before every real campaign.
func TestCampaignStampsCreatedAt(t *testing.T) {
	db := openTestStore(t)

	before := time.Now().Add(-time.Minute)

	created, err := db.CreateCampaign(t.Context(), domain.Campaign{
		Slug:        "timed-campaign",
		Name:        "Timed",
		ContentRoot: t.TempDir(),
		Visibility:  domain.VisibilityPrivate,
		SystemID:    "5e-2024",
	})
	if err != nil {
		t.Fatalf("CreateCampaign() error = %v, want nil", err)
	}

	if created.CreatedAt.Before(before) || created.CreatedAt.After(time.Now().Add(time.Minute)) {
		t.Errorf("CreatedAt = %s, want a reading from this test's own window", created.CreatedAt)
	}
}

// assertCampaignEqual compares two campaigns field by field.
func assertCampaignEqual(t *testing.T, got, want domain.Campaign) {
	t.Helper()

	if got.ID != want.ID {
		t.Errorf("ID = %d, want %d", got.ID, want.ID)
	}

	if got.Slug != want.Slug {
		t.Errorf("Slug = %q, want %q", got.Slug, want.Slug)
	}

	if got.Name != want.Name {
		t.Errorf("Name = %q, want %q", got.Name, want.Name)
	}

	if got.ContentRoot != want.ContentRoot {
		t.Errorf("ContentRoot = %q, want %q", got.ContentRoot, want.ContentRoot)
	}

	if got.Visibility != want.Visibility {
		t.Errorf("Visibility = %q, want %q", got.Visibility, want.Visibility)
	}

	if got.SystemID != want.SystemID {
		t.Errorf("SystemID = %q, want %q", got.SystemID, want.SystemID)
	}

	if got.RulesetVersion != want.RulesetVersion {
		t.Errorf("RulesetVersion = %q, want %q", got.RulesetVersion, want.RulesetVersion)
	}

	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, want.CreatedAt)
	}
}
