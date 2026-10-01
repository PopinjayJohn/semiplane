package store_test

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// These helpers exist because every test below needs a tenant to hang a subject
// off, and four copies of that fixture is four places for it to drift from what
// the schema actually accepts.

// seedUser creates a user with a role the test does not care about.
//
// The password hash is a literal rather than a real hash because no test in this
// file verifies hashing: `internal/httpapi/auth` owns that, and a test here that
// depended on it would fail for a reason that has nothing to do with the store.
func seedUser(t *testing.T, db *store.Store, username string) domain.User {
	t.Helper()

	user, err := db.CreateUser(t.Context(), domain.User{
		Username:     username,
		PasswordHash: "not-a-real-hash",
	})
	if err != nil {
		t.Fatalf("CreateUser(%q) error = %v, want nil", username, err)
	}

	return user
}

// seedCampaign creates a campaign with an absolute content root under the test's
// temporary directory.
//
// Absolute because CreateCampaign refuses a relative one, and the refusal is the
// behaviour under test elsewhere; a fixture that tripped it would turn every test
// that seeded a campaign into a test of the guard.
func seedCampaign(t *testing.T, db *store.Store, slug string) domain.Campaign {
	t.Helper()

	campaign, err := db.CreateCampaign(t.Context(), domain.Campaign{
		Slug:           slug,
		Name:           slug,
		ContentRoot:    filepath.Join(t.TempDir(), slug),
		Visibility:     domain.VisibilityPrivate,
		SystemID:       "5e-2024",
		RulesetVersion: "1",
	})
	if err != nil {
		t.Fatalf("CreateCampaign(%q) error = %v, want nil", slug, err)
	}

	return campaign
}

// seedMembership makes userID a member of campaignID with role.
func seedMembership(
	t *testing.T,
	db *store.Store,
	campaignID int64,
	userID int64,
	role domain.Role,
) domain.Membership {
	t.Helper()

	membership, err := db.CreateMembership(t.Context(), domain.Membership{
		CampaignID: campaignID,
		UserID:     userID,
		Role:       role,
	})
	if err != nil {
		t.Fatalf("CreateMembership(campaign %d, user %d, %q) error = %v, want nil",
			campaignID, userID, role, err)
	}

	return membership
}

// tokenHash produces the value `auth.HashSessionToken` would produce for label.
//
// The same shape -- 64 lowercase hex characters -- because that is what
// `auth_sessions.token_hash` is sized for, and a fixture of some other shape would
// not test the column it is meant to be testing. The hash itself is
// self-contained rather than imported: store must not depend on httpapi, and the
// dependency that does exist runs the other way.
func tokenHash(label string) string {
	sum := sha256.Sum256([]byte(label))

	return hex.EncodeToString(sum[:])
}

// pinnedTime is a fixed instant, used where a test needs a timestamp it can state
// rather than one it has to discover.
func pinnedTime() time.Time {
	return time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
}

// slugify turns a subtest name into a slug.
//
// Present because the slugs here go through domain.ValidateSlug like every other
// slug does, and a table-driven case named "wrong case" would otherwise fail on its
// fixture instead of on the behaviour it is testing.
func slugify(name string) string {
	var out strings.Builder

	for _, char := range strings.ToLower(name) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			out.WriteRune(char)
		default:
			if out.Len() > 0 && !strings.HasSuffix(out.String(), "-") {
				out.WriteByte('-')
			}
		}
	}

	return strings.Trim(out.String(), "-")
}
