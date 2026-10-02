package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// sessionLifetime is comfortably longer than any test's runtime, so a session
// written with it is unambiguously live.
const sessionLifetime = 24 * time.Hour

// TestSessionRoundTrip is the round trip: the hash, the user and both timestamps
// come back as written.
//
// The hash is the primary key and the only thing a request can look a session up
// by, so a value that changed on the way in would log every user out on the next
// request.
func TestSessionRoundTrip(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "session-holder")
	hash := tokenHash("round-trip")

	want := store.AuthSession{
		TokenHash: hash,
		UserID:    user.ID,
		CreatedAt: pinnedTime(),
		ExpiresAt: time.Now().Add(sessionLifetime),
	}

	created, err := db.CreateSession(t.Context(), want)
	if err != nil {
		t.Fatalf("CreateSession() error = %v, want nil", err)
	}

	got, err := db.SessionByTokenHash(t.Context(), hash)
	if err != nil {
		t.Fatalf("SessionByTokenHash() error = %v, want nil", err)
	}

	assertSessionEqual(t, got, created)

	if len(got.TokenHash) != 64 {
		t.Errorf("TokenHash is %d characters, want 64; the column is sized for the hex "+
			"SHA-256 that auth.HashSessionToken produces", len(got.TokenHash))
	}
}

// TestSessionByTokenHashRejectsUnknown covers the miss.
//
// An unknown hash, a revoked one and an expired one are all ErrNotFound, and the
// error text must not carry the hash. Every other read in this package names its
// subject ("read user mira"), which for a session would write a credential into a
// log line -- so the assertion is that the value is absent, not that a message
// reads a particular way.
func TestSessionByTokenHashRejectsUnknown(t *testing.T) {
	db := openTestStore(t)

	unknown := tokenHash("never-minted")

	_, err := db.SessionByTokenHash(t.Context(), unknown)

	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SessionByTokenHash() of an unknown hash = %v, want one wrapping ErrNotFound", err)
	}

	if strings.Contains(err.Error(), unknown) {
		t.Error("the error carries the token hash; a credential reached the error text")
	}
}

// TestExpiredSessionDoesNotResolve covers expiry as the query decides it.
//
// The session row exists for the whole test -- the sweep has not run -- so the
// only thing that can make it unresolvable is the expiry comparison inside the
// lookup. An implementation that filtered expiry in Go, or not at all, would fail
// here rather than in production where nobody is watching.
func TestExpiredSessionDoesNotResolve(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "expiring")
	hash := tokenHash("expired")

	created, err := db.CreateSession(t.Context(), store.AuthSession{
		TokenHash: hash,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateSession() of an already-expired session = %v, want nil", err)
	}

	if created.ExpiresAt.IsZero() {
		t.Fatal("CreateSession() dropped the expiry it was given")
	}

	if _, err := db.SessionByTokenHash(t.Context(), hash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SessionByTokenHash() of an expired session = %v, want ErrNotFound", err)
	}

	// The row is still on disk. Expiry is a read-time decision, not a background
	// one, so a server that has never run the sweep still answers correctly.
	var count int

	if err := db.DB().
		QueryRowContext(t.Context(), "SELECT count(*) FROM auth_sessions WHERE token_hash = ?", hash).
		Scan(&count); err != nil {
		t.Fatalf("count session rows: %v", err)
	}

	if count != 1 {
		t.Errorf("auth_sessions has %d rows for the expired hash, want 1; something "+
			"deleted it rather than declining to resolve it", count)
	}
}

// TestSessionTokenHashRejectsNull is the guard for the NOT NULL on the primary
// key.
//
// In SQLite a `TEXT PRIMARY KEY` on an ordinary rowid table does not imply NOT
// NULL -- the quirk is documented -- and a NULL in a primary key defeats the
// column: a lookup with a NULL token would match the NULL row, so a cookie
// carrying no token at all would authenticate. Nothing in the query layer can
// produce this row, which is exactly why the test writes it by hand.
func TestSessionTokenHashRejectsNull(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "null-hash")

	_, err := db.DB().ExecContext(t.Context(),
		"INSERT INTO auth_sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)",
		nil, user.ID, 1, 2,
	)
	if err == nil {
		t.Fatal("inserting a NULL token_hash succeeded; a cookie with no token would authenticate")
	}
}

// TestCreateSessionRefusesMissingFields covers the two refusals on the write.
//
// Neither is a defensive check against a hostile caller -- there is no hostile
// caller here -- and both are checks against a half-finished mint. A session
// without a hash is not a session, and one without an expiry is a bearer token
// that never stops working.
func TestCreateSessionRefusesMissingFields(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "half-minted")

	cases := []struct {
		name    string
		session store.AuthSession
		want    error
	}{
		{
			name: "no token hash",
			session: store.AuthSession{
				UserID:    user.ID,
				ExpiresAt: time.Now().Add(sessionLifetime),
			},
			want: store.ErrInvalidTokenHash,
		},
		{
			name:    "no expiry",
			session: store.AuthSession{TokenHash: tokenHash("no-expiry"), UserID: user.ID},
			want:    store.ErrSessionExpiryRequired,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := db.CreateSession(t.Context(), testCase.session)

			if !errors.Is(err, testCase.want) {
				t.Errorf("CreateSession() = %v, want one wrapping %v", err, testCase.want)
			}
		})
	}
}

// TestCreateSessionRefusesUnknownUser covers the foreign key. A session for an
// account that does not exist authenticates nobody and would outlive the account
// it names.
func TestCreateSessionRefusesUnknownUser(t *testing.T) {
	db := openTestStore(t)

	_, err := db.CreateSession(t.Context(), store.AuthSession{
		TokenHash: tokenHash("orphan"),
		UserID:    9999,
		ExpiresAt: time.Now().Add(sessionLifetime),
	})

	if !errors.Is(err, store.ErrForeignKey) {
		t.Errorf("CreateSession() for an unknown user = %v, want one wrapping ErrForeignKey", err)
	}
}

// TestCreateSessionRejectsDuplicateHash covers the primary key as a uniqueness
// constraint.
//
// The token is 32 bytes of crypto/rand, so a collision is not a thing that
// happens -- which is the point: the constraint is what makes that reasoning
// unnecessary. If it ever fires, either the randomness or the lookup is wrong.
func TestCreateSessionRejectsDuplicateHash(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "collider")
	hash := tokenHash("collide")

	if _, err := db.CreateSession(t.Context(), store.AuthSession{
		TokenHash: hash,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(sessionLifetime),
	}); err != nil {
		t.Fatalf("first CreateSession() error = %v, want nil", err)
	}

	_, err := db.CreateSession(t.Context(), store.AuthSession{
		TokenHash: hash,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(sessionLifetime),
	})

	if !errors.Is(err, store.ErrConflict) {
		t.Errorf(
			"second CreateSession() with the same hash = %v, want one wrapping ErrConflict",
			err,
		)
	}
}

// TestDeleteSessionRevokes covers logout, and that a revoked token stops resolving.
func TestDeleteSessionRevokes(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "logging-out")
	hash := tokenHash("logout")

	if _, err := db.CreateSession(t.Context(), store.AuthSession{
		TokenHash: hash,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(sessionLifetime),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v, want nil", err)
	}

	if err := db.DeleteSession(t.Context(), hash); err != nil {
		t.Fatalf("DeleteSession() error = %v, want nil", err)
	}

	if _, err := db.SessionByTokenHash(t.Context(), hash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SessionByTokenHash() after logout = %v, want ErrNotFound", err)
	}
}

// TestDeleteSessionIsIdempotent covers the double logout. A second submission is
// not a different event from the first, so it must not report one -- a handler
// that treated "nothing to revoke" as a failure would fail a user who simply
// pressed the button twice.
func TestDeleteSessionIsIdempotent(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "twice-logged-out")
	hash := tokenHash("logout-twice")

	if _, err := db.CreateSession(t.Context(), store.AuthSession{
		TokenHash: hash,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(sessionLifetime),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v, want nil", err)
	}

	for attempt := range 2 {
		if err := db.DeleteSession(t.Context(), hash); err != nil {
			t.Errorf("DeleteSession() attempt %d = %v, want nil", attempt+1, err)
		}
	}

	// And a token that never existed is the same success.
	if err := db.DeleteSession(t.Context(), tokenHash("never-existed")); err != nil {
		t.Errorf("DeleteSession() of an unknown hash = %v, want nil", err)
	}
}

// TestDeleteExpiredSessions covers the sweep.
//
// The bound is a parameter rather than a clock read here, so the test can state
// exactly which rows are in scope: everything that had expired by the moment it
// chose, and nothing that had not.
func TestDeleteExpiredSessions(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "swept")

	stale := tokenHash("stale")
	recent := tokenHash("recent")
	live := tokenHash("live")

	sessions := []struct {
		hash   string
		expiry time.Time
	}{
		{hash: stale, expiry: time.Now().Add(-30 * 24 * time.Hour)},
		{hash: recent, expiry: time.Now().Add(-time.Hour)},
		{hash: live, expiry: time.Now().Add(sessionLifetime)},
	}

	for _, session := range sessions {
		if _, err := db.CreateSession(t.Context(), store.AuthSession{
			TokenHash: session.hash,
			UserID:    user.ID,
			ExpiresAt: session.expiry,
		}); err != nil {
			t.Fatalf("CreateSession(%s) error = %v, want nil", session.hash[:8], err)
		}
	}

	removed, err := db.DeleteExpiredSessions(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("DeleteExpiredSessions() error = %v, want nil", err)
	}

	if removed != 2 {
		t.Errorf("DeleteExpiredSessions() removed %d rows, want 2", removed)
	}

	if _, err := db.SessionByTokenHash(t.Context(), live); err != nil {
		t.Errorf("the live session did not survive the sweep: %v", err)
	}

	for _, gone := range []string{stale, recent} {
		if _, err := db.SessionByTokenHash(t.Context(), gone); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("SessionByTokenHash(%s) after the sweep = %v, want ErrNotFound",
				gone[:8], err)
		}
	}
}

// TestDeleteExpiredSessionsHonoursItsBound covers the bound.
//
// A sweep that ignored its argument and read the clock itself would still pass the
// test above, because the test's bound and the clock agree there. Here they
// deliberately do not: a caller applying a grace period is asking to keep rows the
// clock has already declared dead.
func TestDeleteExpiredSessionsHonoursItsBound(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "grace-period")
	hash := tokenHash("within-grace")

	expired := time.Now().Add(-time.Hour)

	if _, err := db.CreateSession(t.Context(), store.AuthSession{
		TokenHash: hash,
		UserID:    user.ID,
		ExpiresAt: expired,
	}); err != nil {
		t.Fatalf("CreateSession() error = %v, want nil", err)
	}

	removed, err := db.DeleteExpiredSessions(t.Context(), expired.Add(-time.Hour))
	if err != nil {
		t.Fatalf("DeleteExpiredSessions() error = %v, want nil", err)
	}

	if removed != 0 {
		t.Errorf("DeleteExpiredSessions() removed %d rows, want 0; the bound was an hour "+
			"before the expiry", removed)
	}

	// The row is checked for directly rather than through the lookup, because the
	// lookup would refuse it either way: the session really has expired, and this
	// test is about whether the sweep deleted it, not whether it resolves.
	var count int

	if err := db.DB().
		QueryRowContext(t.Context(),
			"SELECT count(*) FROM auth_sessions WHERE token_hash = ?", hash).
		Scan(&count); err != nil {
		t.Fatalf("count session rows: %v", err)
	}

	if count != 1 {
		t.Errorf("auth_sessions has %d rows for the within-grace hash, want 1; the sweep "+
			"ignored its bound and used the clock", count)
	}
}

// TestDeleteExpiredSessionsOnAnEmptyTable covers the sweep finding nothing, which
// is the common case on a server that runs it often. A non-zero count here would
// mean the count is measuring something other than reclaimed rows.
func TestDeleteExpiredSessionsOnAnEmptyTable(t *testing.T) {
	db := openTestStore(t)

	removed, err := db.DeleteExpiredSessions(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("DeleteExpiredSessions() error = %v, want nil", err)
	}

	if removed != 0 {
		t.Errorf("DeleteExpiredSessions() removed %d rows from an empty table, want 0", removed)
	}
}

// TestSessionTimestampsAreIntegers repeats the storage-class assertion for the
// session table.
//
// It matters more here than elsewhere: the expiry comparison in the lookup is a
// numeric comparison against a bound, and a TEXT-stored timestamp would sort last
// against every integer bound -- which for `expires_at > ?` means an expired
// session comparing greater than now and authenticating forever.
func TestSessionTimestampsAreIntegers(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "storage-class-session")
	hash := tokenHash("storage-class")

	if _, err := db.CreateSession(t.Context(), store.AuthSession{
		TokenHash: hash,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(sessionLifetime),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v, want nil", err)
	}

	rows, err := db.DB().
		QueryContext(t.Context(),
			"SELECT typeof(created_at), typeof(expires_at) FROM auth_sessions WHERE token_hash = ?",
			hash)
	if err != nil {
		t.Fatalf("read session storage classes: %v", err)
	}

	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			t.Errorf("close rows: %v", closeErr)
		}
	}()

	if !rows.Next() {
		t.Fatal("the session row is gone; the sweep must not have run")
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	var created, expires string

	if err := rows.Scan(&created, &expires); err != nil {
		t.Fatalf("scan storage classes: %v", err)
	}

	if created != "integer" {
		t.Errorf("created_at has storage class %q, want integer", created)
	}

	if expires != "integer" {
		t.Errorf("expires_at has storage class %q, want integer; a text expiry compares "+
			"wrong against every integer bound in the lookup", expires)
	}
}

// TestSessionAndUserResolveConsistently covers the two reads the auth middleware
// makes, and the reason they may be separate statements.
//
// The process holds one connection, so no write can interleave between them: a
// user deleted after the session resolved would have taken the session with it
// before the second read ran. That is a property of the single-connection model
// (ADR 0004), and this test is where a future pooling change would break it.
func TestSessionAndUserResolveConsistently(t *testing.T) {
	db := openTestStore(t)

	user := seedUser(t, db, "middleware")
	hash := tokenHash("middleware")

	if _, err := db.CreateSession(t.Context(), store.AuthSession{
		TokenHash: hash,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(sessionLifetime),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v, want nil", err)
	}

	session, err := db.SessionByTokenHash(t.Context(), hash)
	if err != nil {
		t.Fatalf("SessionByTokenHash() error = %v, want nil", err)
	}

	resolved, err := db.UserByID(t.Context(), session.UserID)
	if err != nil {
		t.Fatalf("UserByID() error = %v, want nil", err)
	}

	if resolved.ID != user.ID {
		t.Errorf("resolved user %d, want %d", resolved.ID, user.ID)
	}

	// The two together are what domain.ResolveAccess wants, and it is worth
	// asserting that they compose: the requestor is not authenticated merely
	// because a session row existed.
	tier := domain.ResolveAccess(domain.Campaign{ID: 1, Visibility: domain.VisibilityPrivate},
		&domain.Membership{CampaignID: 1, UserID: user.ID, Role: domain.RolePlayer},
		domain.Requestor{UserID: resolved.ID, IsAdmin: resolved.IsAdmin},
	)

	if tier != domain.TierNone {
		t.Errorf("tier = %s for an unauthenticated requestor, want none", tier)
	}
}

// assertSessionEqual compares two sessions field by field.
func assertSessionEqual(t *testing.T, got, want store.AuthSession) {
	t.Helper()

	if got.TokenHash != want.TokenHash {
		t.Errorf("TokenHash = %q, want %q", got.TokenHash, want.TokenHash)
	}

	if got.UserID != want.UserID {
		t.Errorf("UserID = %d, want %d", got.UserID, want.UserID)
	}

	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, want.CreatedAt)
	}

	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt = %s, want %s", got.ExpiresAt, want.ExpiresAt)
	}
}
