package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"database/sql"

	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// openAuthDB returns a real SQLite DB (same migrate schema as the vault)
// on a private temp file. File-backed on purpose: Open(":memory:") binds
// to a SHARED-cache memdb whose lifetime spans the process, which would
// bleed rows across tests.
func openAuthDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("Open auth db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustCreateUser(t *testing.T, db *sql.DB, id, role, password string) {
	t.Helper()
	if err := NewUsers(db).Create(id, role, password); err != nil {
		t.Fatalf("Users.Create(%q, %q): %v", id, role, err)
	}
}

func TestSessionCreateVerifyRoundTrip(t *testing.T) {
	db := openAuthDB(t)
	mustCreateUser(t, db, "gm1", "gm", "swordfish")
	mustCreateUser(t, db, "p1", "player", "clam")

	svc := NewSessionService(db)
	ctx := context.Background()
	tok, err := svc.Create(ctx, "gm1", time.Minute)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ident, err := svc.Verify(ctx, tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ident.ID != "gm1" || ident.Role != "gm" {
		t.Fatalf("Verify = %+v, want {gm1 gm}", ident)
	}

	tok2, err := svc.Create(ctx, "p1", time.Minute)
	if err != nil {
		t.Fatalf("Create player: %v", err)
	}
	ident, err = svc.Verify(ctx, tok2)
	if err != nil {
		t.Fatalf("Verify player: %v", err)
	}
	if ident.ID != "p1" || ident.Role != "player" {
		t.Fatalf("Verify player = %+v, want {p1 player}", ident)
	}
}

func TestSessionOnlyHashAtRest(t *testing.T) {
	db := openAuthDB(t)
	mustCreateUser(t, db, "u1", "player", "pw")
	svc := NewSessionService(db)
	tok, err := svc.Create(context.Background(), "u1", time.Minute)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var id, stored string
	if err := db.QueryRow(
		`SELECT id, hash FROM sessions WHERE user_id = 'u1'`).Scan(&id, &stored); err != nil {
		t.Fatalf("read session row: %v", err)
	}
	if id != HashToken(tok) || stored != HashToken(tok) {
		t.Fatalf("session row id/hash = %q/%q, want both = token hash %q", id, stored, HashToken(tok))
	}
	if stored == tok {
		t.Fatal("plaintext token persisted — MUST store hash only")
	}
}

func TestSessionUnknownTokenUnauthorized(t *testing.T) {
	db := openAuthDB(t)
	svc := NewSessionService(db)
	_, err := svc.Verify(context.Background(), "not-a-real-token")
	if !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("Verify unknown = %v, want errors.Is(ErrUnauthorized)", err)
	}
}

func TestSessionExpired(t *testing.T) {
	db := openAuthDB(t)
	mustCreateUser(t, db, "u1", "player", "pw")
	svc := NewSessionService(db)
	ctx := context.Background()
	// ttl of a nanosecond rounds to expires_at == now (unix-second
	// columns), which Verify must treat as already expired.
	tok, err := svc.Create(ctx, "u1", time.Nanosecond)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ident, err := svc.Verify(ctx, tok)
	if !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("Verify expired = (%+v, %v), want errors.Is(ErrUnauthorized)", ident, err)
	}
}

func TestSessionRevokeAndRevokeAll(t *testing.T) {
	db := openAuthDB(t)
	mustCreateUser(t, db, "u1", "player", "pw")
	svc := NewSessionService(db)
	ctx := context.Background()

	tok, _ := svc.Create(ctx, "u1", time.Minute)
	if err := svc.Revoke(ctx, tok); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := svc.Verify(ctx, tok); !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("Verify after revoke = %v, want ErrUnauthorized", err)
	}
	// Revoking a dead token is not an error.
	if err := svc.Revoke(ctx, tok); err != nil {
		t.Fatalf("Revoke dead token: %v", err)
	}

	t1, _ := svc.Create(ctx, "u1", time.Minute)
	t2, _ := svc.Create(ctx, "u1", time.Minute)
	if err := svc.RevokeAllFor(ctx, "u1"); err != nil {
		t.Fatalf("RevokeAllFor: %v", err)
	}
	for _, tok := range []string{t1, t2} {
		if _, err := svc.Verify(ctx, tok); !errors.Is(err, pluginapi.ErrUnauthorized) {
			t.Fatalf("Verify after RevokeAllFor = %v, want ErrUnauthorized", err)
		}
	}
}

func TestSessionCleanupExpired(t *testing.T) {
	db := openAuthDB(t)
	mustCreateUser(t, db, "u1", "player", "pw")
	svc := NewSessionService(db)
	tok, err := svc.Create(context.Background(), "u1", time.Minute)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Kill the other one too: forge an expired extra row.
	if _, err := db.Exec(
		`INSERT INTO sessions(id, user_id, created_at, expires_at, hash)
		   VALUES('stale', 'u1', 0, 0, 'stale-hash')`); err != nil {
		t.Fatal(err)
	}
	if err := svc.CleanupExpired(context.Background()); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	// The live session survives.
	if _, err := svc.Verify(context.Background(), tok); err != nil {
		t.Fatalf("live session destroyed by cleanup: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("sessions after cleanup = %d rows, want 1 (live only)", n)
	}
}

func TestSessionCookieFlags(t *testing.T) {
	rec := httptest.NewRecorder()
	SetSessionCookie(rec, "tok-123", true)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("SetSessionCookie wrote %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != SessionCookieName || c.Value != "tok-123" {
		t.Fatalf("cookie name/value = %q/%q", c.Name, c.Value)
	}
	if !c.HttpOnly {
		t.Fatal("HttpOnly flag missing")
	}
	if !c.Secure {
		t.Fatal("Secure flag missing when secure=true")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite = %v, want Lax", c.SameSite)
	}
	if c.Path != "/" {
		t.Fatalf("Path = %q, want /", c.Path)
	}

	// secure=false (plain-LAN callsite): Secure must be OFF, the others on.
	rec2 := httptest.NewRecorder()
	SetSessionCookie(rec2, "tok-123", false)
	c2 := rec2.Result().Cookies()[0]
	if c2.Secure {
		t.Fatal("Secure flag set with secure=false — would break LAN http logins")
	}
	if !c2.HttpOnly || c2.SameSite != http.SameSiteLaxMode {
		t.Fatalf("HttpOnly/SameSite lost with secure=false: %+v", c2)
	}

	// ClearSessionCookie expires the cookie in the browser.
	rec3 := httptest.NewRecorder()
	ClearSessionCookie(rec3)
	c3 := rec3.Result().Cookies()[0]
	if c3.Value != "" || c3.MaxAge >= 0 {
		t.Fatalf("clear cookie = value %q maxage %d, want empty/-1", c3.Value, c3.MaxAge)
	}
}

func TestCookieSecure(t *testing.T) {
	req := httptest.NewRequest("GET", "http://lan.local/", nil)
	if CookieSecure(req, false) {
		t.Fatal("plain http request must not get Secure cookie")
	}
	if !CookieSecure(req, true) {
		t.Fatal("forceSecure flag ignored")
	}
	proxyReq := httptest.NewRequest("GET", "http://lan.local/", nil)
	proxyReq.Header.Set("X-Forwarded-Proto", "https")
	if !CookieSecure(proxyReq, false) {
		t.Fatal("X-Forwarded-Proto https must yield Secure cookie")
	}
	plainProxy := httptest.NewRequest("GET", "http://lan.local/", nil)
	plainProxy.Header.Set("X-Forwarded-Proto", "http")
	if CookieSecure(plainProxy, false) {
		t.Fatal("X-Forwarded-Proto http must not yield Secure cookie")
	}
	// Case-insensitive header value.
	upcase := httptest.NewRequest("GET", "http://lan.local/", nil)
	upcase.Header.Set("X-Forwarded-Proto", "HTTPS")
	if !CookieSecure(upcase, false) {
		t.Fatal("X-Forwarded-Proto HTTPS (case-insensitive) must yield Secure cookie")
	}
}

func TestAuthProviderVerifyDelegates(t *testing.T) {
	db := openAuthDB(t)
	mustCreateUser(t, db, "u1", "player", "pw")
	ap := NewAuthProvider(NewSessionService(db))
	tok, err := NewSessionService(db).Create(context.Background(), "u1", time.Minute)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ident, err := ap.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("AuthProvider.Verify: %v", err)
	}
	if ident.ID != "u1" || ident.Role != "player" {
		t.Fatalf("AuthProvider.Verify = %+v, want {u1 player}", ident)
	}
	if _, err := ap.Verify(context.Background(), "bogus"); !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("AuthProvider.Verify bogus = %v, want ErrUnauthorized", err)
	}
}

func TestUsersCreateLoginAndConflict(t *testing.T) {
	db := openAuthDB(t)
	u := NewUsers(db)
	ctx := context.Background()
	if err := u.Create("u1", "player", "pw-one"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Duplicate id/username → ErrConflict (username == userID; the PK
	// and the UNIQUE column both fire the SQLite UNIQUE error, and the
	// constraint is the dedupe authority).
	if err := u.Create("u1", "player", "other-password"); !errors.Is(err, pluginapi.ErrConflict) {
		t.Fatalf("Create duplicate = %v, want ErrConflict", err)
	}
	// The username column itself is UNIQUE: a different id can't claim it.
	if _, err := db.Exec(
		`INSERT INTO users(id, role, username, phc, created_at) VALUES('other-id','player','u1','x',0)`); err == nil {
		t.Fatal("users.username UNIQUE constraint not enforced")
	}
	// Invalid role rejected up-front.
	if err := u.Create("u3", "admin", "pw"); err == nil {
		t.Fatal("Create role=admin accepted; roles are closed-vocabulary gm/player")
	}
	if _, err := u.Login(ctx, "u1", "wrong"); !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("Login wrong password = %v, want ErrUnauthorized", err)
	}
	if _, err := u.Login(ctx, "ghost", "pw"); !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("Login unknown user = %v, want ErrUnauthorized (indistinguishable)", err)
	}
	ident, err := u.Login(ctx, "u1", "pw-one")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if ident.ID != "u1" || ident.Role != "player" {
		t.Fatalf("Login = %+v, want {u1 player}", ident)
	}
	// Hash is stored, not the password — and it verifies under our floor.
	var phc string
	if err := db.QueryRow(`SELECT phc FROM users WHERE id='u1'`).Scan(&phc); err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPassword("pw-one", phc); err != nil || !ok {
		t.Fatalf("VerifyPassword(stored phc) = (%t, %v), want (true, nil)", ok, err)
	}
}

// Guard the assertions above: every ErrUnauthorized path tested must be
// reachable via wrapping (this is a tautology unless err is non-nil).
func TestUnauthorizedWrappingShape(t *testing.T) {
	if !errors.Is(fmt.Errorf("wrapped: %w", pluginapi.ErrUnauthorized), pluginapi.ErrUnauthorized) {
		t.Fatal("precondition: errors.Is through %w broken")
	}
}
