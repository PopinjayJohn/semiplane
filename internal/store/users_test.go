package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// TestIdentityTablesAreCreated is the assertion that this phase's migrations ran
// at all. A runner that silently applied nothing would pass every query test in
// this file, because each of them would then fail for its own reason and be
// misread as a defect in the query.
func TestIdentityTablesAreCreated(t *testing.T) {
	db := openTestStore(t)

	applied := appliedVersions(t, db.DB())

	for _, name := range []string{"users", "auth-sessions", "campaigns", "campaign-members"} {
		if _, ok := applied[name]; !ok {
			t.Errorf("migration %q is unapplied: %v", name, applied)
		}
	}
}

// TestUserRoundTrip is the round trip: a user written with every field set comes
// back equal, through both of the lookups a request can arrive by.
//
// PasswordHash is compared even though it is a credential, because it has to
// survive: it is the one field the login path reads, and a mapping that dropped it
// would look like a correct store and lock every account out.
func TestUserRoundTrip(t *testing.T) {
	db := openTestStore(t)

	want := domain.User{
		Username:     "mira",
		Email:        "mira@example.invalid",
		PasswordHash: "argon2id$v=19$m=65536,t=1,p=2$c2FsdA$aGFzaA",
		IsAdmin:      true,
		CreatedAt:    pinnedTime(),
	}

	created, err := db.CreateUser(t.Context(), want)
	if err != nil {
		t.Fatalf("CreateUser() error = %v, want nil", err)
	}

	if created.ID <= 0 {
		t.Errorf("CreateUser() returned id %d, want a positive rowid", created.ID)
	}

	cases := []struct {
		name string
		read func() (domain.User, error)
	}{
		{
			name: "by id",
			read: func() (domain.User, error) { return db.UserByID(t.Context(), created.ID) },
		},
		{
			name: "by username",
			read: func() (domain.User, error) {
				return db.UserByUsername(t.Context(), want.Username)
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := testCase.read()
			if err != nil {
				t.Fatalf("read error = %v, want nil", err)
			}

			assertUserEqual(t, got, created)
		})
	}
}

// TestCreateUserAssignsTheId is the claim that callers cannot choose a primary
// key. A caller that could would collide with a row another registration
// committed, and the collision would surface as somebody else's account.
func TestCreateUserAssignsTheId(t *testing.T) {
	db := openTestStore(t)

	first, err := db.CreateUser(t.Context(), domain.User{
		Username:     "first",
		PasswordHash: "hash",
		ID:           4242,
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v, want nil", err)
	}

	if first.ID == 4242 {
		t.Error("CreateUser() honoured the caller's id; the store assigns primary keys")
	}

	stored, err := db.UserByID(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("UserByID(%d) error = %v, want nil", first.ID, err)
	}

	if stored.Username != "first" {
		t.Errorf("username = %q, want %q; the row the caller named was not written",
			stored.Username, "first")
	}
}

// TestCreateUserStampsCreatedAt covers the zero-time default. A row that stored
// Unix(0) instead would sort before every real account in any ordering by age,
// and nothing else in the table would reveal it.
func TestCreateUserStampsCreatedAt(t *testing.T) {
	db := openTestStore(t)

	created, err := db.CreateUser(t.Context(), domain.User{
		Username:     "unstamped",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v, want nil", err)
	}

	if created.CreatedAt.IsZero() {
		t.Fatal("CreateUser() returned a zero CreatedAt; the clock default did not apply")
	}

	stored, err := db.UserByID(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("UserByID() error = %v, want nil", err)
	}

	if !stored.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("stored CreatedAt = %s, want %s", stored.CreatedAt, created.CreatedAt)
	}
}

// TestUserCreatedAtIsAnInteger is the storage-class assertion behind the
// Unix-second decision, repeated here because every table in this schema repeats
// it.
//
// A time.Time handed to the driver is rendered as a string, and SQLite's dynamic
// typing stores that in an INTEGER column as TEXT -- so the column would hold a
// value its own type declaration denies, and a later `WHERE created_at < ?`
// against an integer would compare text to integer, which sorts every text value
// last. Silent, and wrong in the direction that hides old rows.
func TestUserCreatedAtIsAnInteger(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "storage-class")

	var storageClass string

	err := db.DB().
		QueryRowContext(t.Context(), "SELECT typeof(created_at) FROM users WHERE id = ?", user.ID).
		Scan(&storageClass)
	if err != nil {
		t.Fatalf("read typeof(created_at): %v", err)
	}

	if storageClass != "integer" {
		t.Errorf("created_at has storage class %q, want integer", storageClass)
	}
}

// TestCreateUserRejectsDuplicateUsername covers uniqueness as the database
// enforces it, not as a pre-check. A read-then-insert would leave a window
// between two statements, and on this connection the two are separate
// transactions.
func TestCreateUserRejectsDuplicateUsername(t *testing.T) {
	db := openTestStore(t)

	seedUser(t, db, "taken")

	_, err := db.CreateUser(t.Context(), domain.User{Username: "taken", PasswordHash: "hash"})

	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("CreateUser() of a taken username = %v, want one wrapping ErrConflict", err)
	}
}

// TestUsersUsernameIsCaseSensitive pins the deliberate absence of case folding.
//
// A folded index would make `Alice` and `alice` one person by a rule no
// requirement states, and would make the stored username differ from the one the
// owner typed. If that is ever wanted it belongs in domain, with a rule and a
// reason -- and this test is where the change would be argued.
func TestUsersUsernameIsCaseSensitive(t *testing.T) {
	db := openTestStore(t)

	seedUser(t, db, "Alice")

	if _, err := db.CreateUser(t.Context(), domain.User{
		Username:     "alice",
		PasswordHash: "hash",
	}); err != nil {
		t.Fatalf("CreateUser(alice) alongside Alice = %v, want nil; usernames are exact", err)
	}

	upper, err := db.UserByUsername(t.Context(), "Alice")
	if err != nil {
		t.Fatalf("UserByUsername(Alice) error = %v, want nil", err)
	}

	if upper.ID == 0 {
		t.Error("UserByUsername(Alice) found nothing")
	}

	lower, err := db.UserByUsername(t.Context(), "alice")
	if err != nil {
		t.Fatalf("UserByUsername(alice) error = %v, want nil", err)
	}

	if lower.ID == upper.ID {
		t.Error("Alice and alice resolved to the same row; the lookup is folding case")
	}
}

// TestCreateUserRejectsEmptyUsername covers the one value that cannot be a name.
// A row holding it would answer a login form that submitted no username, so the
// guard is an authentication property rather than a tidiness one.
func TestCreateUserRejectsEmptyUsername(t *testing.T) {
	db := openTestStore(t)

	_, err := db.CreateUser(t.Context(), domain.User{PasswordHash: "hash"})

	if !errors.Is(err, store.ErrInvalidUsername) {
		t.Errorf(
			"CreateUser() of an empty username = %v, want one wrapping ErrInvalidUsername",
			err,
		)
	}
}

// TestUserLookupMissing covers ErrNotFound for both lookups. An unknown username
// and a wrong password have to be indistinguishable to the caller, and this is
// where that indistinguishability is established.
func TestUserLookupMissing(t *testing.T) {
	db := openTestStore(t)

	cases := []struct {
		name string
		read func() (domain.User, error)
	}{
		{
			name: "unknown username",
			read: func() (domain.User, error) {
				return db.UserByUsername(t.Context(), "nobody")
			},
		},
		{
			name: "unknown id",
			read: func() (domain.User, error) {
				return db.UserByID(t.Context(), 9999)
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := testCase.read()

			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("read error = %v, want one wrapping ErrNotFound", err)
			}
		})
	}
}

// TestDeleteUserRefusesWhileAMember covers the one RESTRICT in this schema.
//
// A cascade here would let deleting a dormant account silently strip the last GM
// from every campaign that user ran, and the symptom -- a campaign nobody can edit
// -- would appear days later with nothing in any log to explain it.
func TestDeleteUserRefusesWhileAMember(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "gm")
	campaign := seedCampaign(t, db, "guarded-by-restrict")
	seedMembership(t, db, campaign.ID, user.ID, domain.RoleGM)

	err := db.DeleteUser(t.Context(), user.ID)

	if !errors.Is(err, store.ErrForeignKey) {
		t.Fatalf("DeleteUser() of a member error = %v, want one wrapping ErrForeignKey", err)
	}

	// The row is still there. A refused delete that removed anything anyway would
	// be a worse bug than the one the constraint prevents.
	if _, err := db.UserByID(t.Context(), user.ID); err != nil {
		t.Errorf("UserByID() after a refused delete = %v, want the user still present", err)
	}

	// Removing the membership deliberately, then the account, is the path the
	// constraint is asking for.
	if err := db.DeleteMembership(t.Context(), campaign.ID, user.ID); err != nil {
		t.Fatalf("DeleteMembership() error = %v, want nil", err)
	}

	if err := db.DeleteUser(t.Context(), user.ID); err != nil {
		t.Fatalf("DeleteUser() after removing the membership = %v, want nil", err)
	}
}

// TestDeleteUserCascadesSessions covers the cascade on auth_sessions.user_id. A
// session is a bearer credential for exactly one account, so with the account
// gone the row authenticates nothing and retaining it protects nobody.
func TestDeleteUserCascadesSessions(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "leaving")
	hash := tokenHash("leaving")

	if _, err := db.CreateSession(t.Context(), store.AuthSession{
		TokenHash: hash,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v, want nil", err)
	}

	if err := db.DeleteUser(t.Context(), user.ID); err != nil {
		t.Fatalf("DeleteUser() error = %v, want nil", err)
	}

	if _, err := db.SessionByTokenHash(t.Context(), hash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SessionByTokenHash() after the user was deleted = %v, want ErrNotFound", err)
	}
}

// TestDeleteUserMissingIsSuccess covers the idempotent delete. A user who is not
// there leaves nothing to be inconsistent about, and reporting it as an error
// would make a retry loop fail on the second attempt.
func TestDeleteUserMissingIsSuccess(t *testing.T) {
	db := openTestStore(t)

	if err := db.DeleteUser(t.Context(), 4242); err != nil {
		t.Errorf("DeleteUser() of an absent user = %v, want nil", err)
	}
}

// assertUserEqual compares two users field by field.
//
// Written out rather than reflect.DeepEqual because the fields are few and named:
// a comparison that reports *which* field differed is the difference between a
// failing test that says something and one that says "not equal".
func assertUserEqual(t *testing.T, got, want domain.User) {
	t.Helper()

	if got.ID != want.ID {
		t.Errorf("ID = %d, want %d", got.ID, want.ID)
	}

	if got.Username != want.Username {
		t.Errorf("Username = %q, want %q", got.Username, want.Username)
	}

	if got.Email != want.Email {
		t.Errorf("Email = %q, want %q", got.Email, want.Email)
	}

	if got.PasswordHash != want.PasswordHash {
		t.Errorf("PasswordHash does not round-trip; the login path would read an empty hash")
	}

	if got.IsAdmin != want.IsAdmin {
		t.Errorf("IsAdmin = %t, want %t", got.IsAdmin, want.IsAdmin)
	}

	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, want.CreatedAt)
	}
}
