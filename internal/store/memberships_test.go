package store_test

import (
	"errors"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// TestMembershipRoundTrip is the round trip, over both roles. `role` is the single
// place in the schema where a campaign privilege comes from (S-2.6), so a value
// that did not survive the write would be a privilege nobody holds.
func TestMembershipRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		role domain.Role
	}{
		{name: "gm", role: domain.RoleGM},
		{name: "player", role: domain.RolePlayer},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := openTestStore(t)

			user := seedUser(t, db, "member-"+testCase.name)
			campaign := seedCampaign(t, db, "campaign-"+testCase.name)

			want := domain.Membership{
				CampaignID: campaign.ID,
				UserID:     user.ID,
				Role:       testCase.role,
				CreatedAt:  pinnedTime(),
			}

			created, err := db.CreateMembership(t.Context(), want)
			if err != nil {
				t.Fatalf("CreateMembership() error = %v, want nil", err)
			}

			assertMembershipEqual(t, created, want)

			got, err := db.Membership(t.Context(), campaign.ID, user.ID)
			if err != nil {
				t.Fatalf("Membership() error = %v, want nil", err)
			}

			assertMembershipEqual(t, got, created)
		})
	}
}

// TestCreateMembershipRejectsDuplicatePair covers the composite primary key.
//
// One user has exactly one role in one campaign. A second row claiming otherwise
// would be two answers to one question with nothing to say which wins -- and the
// wrong one would win by insertion order.
func TestCreateMembershipRejectsDuplicatePair(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "twice")
	campaign := seedCampaign(t, db, "twice-campaign")

	seedMembership(t, db, campaign.ID, user.ID, domain.RolePlayer)

	_, err := db.CreateMembership(t.Context(), domain.Membership{
		CampaignID: campaign.ID,
		UserID:     user.ID,
		Role:       domain.RoleGM,
	})

	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("CreateMembership() of an existing pair = %v, want one wrapping ErrConflict", err)
	}

	// The first row's role is untouched. A rejected insert that had promoted the
	// player to GM would be the worst possible version of this bug.
	got, err := db.Membership(t.Context(), campaign.ID, user.ID)
	if err != nil {
		t.Fatalf("Membership() error = %v, want nil", err)
	}

	if got.Role != domain.RolePlayer {
		t.Errorf("role = %q after a rejected insert, want %q", got.Role, domain.RolePlayer)
	}
}

// TestCreateMembershipRejectsUnknownRole covers the write-time role check.
//
// domain.Role.Valid is the one implementation of the vocabulary; this calls it
// rather than restating it. Without the check the row would be written and would
// fail closed on every read, which is safe but silent -- a privilege that never
// grants anything and nobody can see why.
func TestCreateMembershipRejectsUnknownRole(t *testing.T) {
	cases := []struct {
		name string
		role domain.Role
	}{
		{name: "zero value", role: domain.Role("")},
		{name: "editor", role: domain.Role("editor")},
		{name: "wrong case", role: domain.Role("GM")},
		{name: "padded", role: domain.Role("gm ")},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := openTestStore(t)

			user := seedUser(t, db, "role-"+slugify(testCase.name))
			campaign := seedCampaign(t, db, "role-campaign-"+slugify(testCase.name))

			_, err := db.CreateMembership(t.Context(), domain.Membership{
				CampaignID: campaign.ID,
				UserID:     user.ID,
				Role:       testCase.role,
			})

			if !errors.Is(err, domain.ErrInvalidRole) {
				t.Errorf("CreateMembership() with role %q = %v, want one wrapping "+
					"domain.ErrInvalidRole", testCase.role, err)
			}
		})
	}
}

// TestCreateMembershipRejectsMissingEndpoints covers the foreign keys.
//
// ErrForeignKey and not ErrNotFound, because the two mean different things to a
// caller: this is a request that named something absent, where ErrNotFound is a
// request asking for something that is not there.
func TestCreateMembershipRejectsMissingEndpoints(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "present")
	campaign := seedCampaign(t, db, "present-campaign")

	cases := []struct {
		name       string
		campaignID int64
		userID     int64
	}{
		{name: "unknown user", campaignID: campaign.ID, userID: 9999},
		{name: "unknown campaign", campaignID: 9999, userID: user.ID},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := db.CreateMembership(t.Context(), domain.Membership{
				CampaignID: testCase.campaignID,
				UserID:     testCase.userID,
				Role:       domain.RolePlayer,
			})

			if !errors.Is(err, store.ErrForeignKey) {
				t.Errorf("CreateMembership() = %v, want one wrapping ErrForeignKey", err)
			}
		})
	}
}

// TestMembershipMissing covers ErrNotFound for the pair. This is the read the S-8
// matrix depends on, and "no membership" has to be a value a caller can branch on
// rather than a message it has to parse.
func TestMembershipMissing(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "outsider")
	campaign := seedCampaign(t, db, "closed-campaign")

	_, err := db.Membership(t.Context(), campaign.ID, user.ID)

	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Membership() of a non-member = %v, want one wrapping ErrNotFound", err)
	}
}

// TestMembershipsForCampaign covers the member roster.
//
// Ordered by user id, which means by creation order -- the ids come from the
// database, not from the test, so the expectation names the users it seeded in the
// order it seeded them rather than pretending to know their numbers.
func TestMembershipsForCampaign(t *testing.T) {
	db := openTestStore(t)

	campaign := seedCampaign(t, db, "roster")
	outsider := seedUser(t, db, "not-here")

	player := seedUser(t, db, "player")
	general := seedUser(t, db, "gm")

	seedMembership(t, db, campaign.ID, player.ID, domain.RolePlayer)
	seedMembership(t, db, campaign.ID, general.ID, domain.RoleGM)

	got, err := db.MembershipsForCampaign(t.Context(), campaign.ID)
	if err != nil {
		t.Fatalf("MembershipsForCampaign() error = %v, want nil", err)
	}

	want := []domain.Membership{
		{CampaignID: campaign.ID, UserID: player.ID, Role: domain.RolePlayer},
		{CampaignID: campaign.ID, UserID: general.ID, Role: domain.RoleGM},
	}

	if len(got) != len(want) {
		t.Fatalf("MembershipsForCampaign() returned %d rows, want %d", len(got), len(want))
	}

	for idx, expected := range want {
		if got[idx].UserID != expected.UserID {
			t.Errorf("memberships[%d].UserID = %d, want %d; the roster is ordered by user id",
				idx, got[idx].UserID, expected.UserID)
		}

		if got[idx].Role != expected.Role {
			t.Errorf("memberships[%d].Role = %q, want %q", idx, got[idx].Role, expected.Role)
		}
	}

	for _, membership := range got {
		if membership.UserID == outsider.ID {
			t.Error("the roster contains a user that was never added")
		}
	}
}

// TestMembershipsForUser covers the by-user direction, which is the one the
// request path issues on every authenticated request and the one the composite
// primary key cannot answer.
func TestMembershipsForUser(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "busy")
	other := seedUser(t, db, "elsewhere")

	alpha := seedCampaign(t, db, "alpha-user")
	zulu := seedCampaign(t, db, "zulu-user")

	seedMembership(t, db, zulu.ID, user.ID, domain.RolePlayer)
	seedMembership(t, db, alpha.ID, user.ID, domain.RoleGM)
	seedMembership(t, db, zulu.ID, other.ID, domain.RolePlayer)

	got, err := db.MembershipsForUser(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("MembershipsForUser() error = %v, want nil", err)
	}

	want := []domain.Membership{
		{CampaignID: alpha.ID, UserID: user.ID, Role: domain.RoleGM},
		{CampaignID: zulu.ID, UserID: user.ID, Role: domain.RolePlayer},
	}

	if len(got) != len(want) {
		t.Fatalf("MembershipsForUser() returned %d rows, want %d", len(got), len(want))
	}

	for idx, expected := range want {
		if got[idx].CampaignID != expected.CampaignID {
			t.Errorf("memberships[%d].CampaignID = %d, want %d; the list is ordered by campaign id",
				idx, got[idx].CampaignID, expected.CampaignID)
		}

		if got[idx].Role != expected.Role {
			t.Errorf("memberships[%d].Role = %q, want %q", idx, got[idx].Role, expected.Role)
		}
	}
}

// TestMembershipsForCampaignEmpty covers the empty roster, which is a brand new
// campaign rather than an error.
func TestMembershipsForCampaignEmpty(t *testing.T) {
	db := openTestStore(t)

	campaign := seedCampaign(t, db, "empty-roster")

	got, err := db.MembershipsForCampaign(t.Context(), campaign.ID)
	if err != nil {
		t.Fatalf("MembershipsForCampaign() error = %v, want nil", err)
	}

	if len(got) != 0 {
		t.Errorf("MembershipsForCampaign() returned %d rows, want 0", len(got))
	}
}

// TestDeleteMembershipRemovesAccess covers the deliberate counterpart of the
// RESTRICT on DeleteUser: a role is removed one campaign at a time, by whoever is
// about to remove a GM.
func TestDeleteMembershipRemovesAccess(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "revoked")
	campaign := seedCampaign(t, db, "revoked-campaign")

	seedMembership(t, db, campaign.ID, user.ID, domain.RoleGM)

	if err := db.DeleteMembership(t.Context(), campaign.ID, user.ID); err != nil {
		t.Fatalf("DeleteMembership() error = %v, want nil", err)
	}

	if _, err := db.Membership(
		t.Context(),
		campaign.ID,
		user.ID,
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Errorf("Membership() after deletion = %v, want ErrNotFound", err)
	}

	// The user survives. Removing a role and removing an account are separate
	// operations, and conflating them is how a GM loses access to a campaign they
	// still run.
	if _, err := db.UserByID(t.Context(), user.ID); err != nil {
		t.Errorf("UserByID() after removing one membership = %v, want nil", err)
	}
}

// TestDeleteMembershipMissingIsSuccess covers the idempotent delete.
func TestDeleteMembershipMissingIsSuccess(t *testing.T) {
	db := openTestStore(t)

	if err := db.DeleteMembership(t.Context(), 1, 1); err != nil {
		t.Errorf("DeleteMembership() of an absent pair = %v, want nil", err)
	}
}

// assertMembershipEqual compares two memberships field by field.
func assertMembershipEqual(t *testing.T, got, want domain.Membership) {
	t.Helper()

	if got.CampaignID != want.CampaignID {
		t.Errorf("CampaignID = %d, want %d", got.CampaignID, want.CampaignID)
	}

	if got.UserID != want.UserID {
		t.Errorf("UserID = %d, want %d", got.UserID, want.UserID)
	}

	if got.Role != want.Role {
		t.Errorf("Role = %q, want %q", got.Role, want.Role)
	}

	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, want.CreatedAt)
	}
}
