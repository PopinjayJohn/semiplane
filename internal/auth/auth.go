// Package auth implements Semiplane's local authentication surface
// (tech-stack §3.6): Argon2id password hashing (PHC strings), opaque
// session tokens persisted as sha256 hashes in SQLite, single-use 6-digit
// LAN join codes, and OIDC Authorization Code + S256 PKCE login against a
// server-side state store. All storage targets the shared migrate schema
// (sessions/tokens/lan_codes/oidc_states/users) over the single-pool
// SQLite pattern used by internal/index — MaxOpenConns(1) and explicit
// BEGIN IMMEDIATE transactions for multi-statement writes.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/PopinjayJohn/semiplane/internal/migrate"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
	_ "modernc.org/sqlite"
)

// Open opens the auth database. It is the index.Open DSN/wiring verbatim
// (WAL, busy_timeout, FK on, txlock immediate, pool capped at one
// connection, schema applied) minus the FTS5 assertion, which is a search
// concern, not an auth one. Callers sharing a single vault DB pass an
// already-open *sql.DB to the services instead; Open exists for standalone
// auth deployments (e.g. doctor smoke tests).
func Open(dbPath string) (*sql.DB, error) {
	var dsn string
	if dbPath == ":memory:" {
		dsn = "file:memdb?mode=memory&cache=shared&_busy_timeout=5000&_foreign_keys=on&_txlock=immediate"
	} else {
		dsn = dbPath + "?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on&_txlock=immediate"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate.Apply(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// inImmediateTx runs fn on a single pinned connection inside an explicit
// BEGIN IMMEDIATE / COMMIT transaction, rolling back on error. Mirrors
// internal/index's pattern: SQLite authorities are single-writer, so
// check-then-act logic (single-use codes, state consume) must hold the
// write lock across the statements.
func inImmediateTx(ctx context.Context, db *sql.DB, fn func(ctx context.Context, conn *sql.Conn) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := fn(ctx, conn); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

// AuthProvider is the pluginapi.AuthProvider implementation for local
// sessions.
//
// Scope note: Verify resolves SESSION tokens only. Scoped API tokens
// (the `tokens` table) are a different credential class with scope
// enforcement; they are resolved by the token-scope middleware in
// internal/acl, not here, and deliberately never by this Verify — an
// AuthProvider answers "who is calling", an ACL answers "what may they
// do". Keeping the two apart means a leaked session cookie can never be
// replayed as an API token against the same columns.
type AuthProvider struct {
	sessions *SessionService
}

// compile-time conformance check.
var _ pluginapi.AuthProvider = (*AuthProvider)(nil)

// NewAuthProvider wires an AuthProvider onto a SessionService.
func NewAuthProvider(sessions *SessionService) *AuthProvider {
	return &AuthProvider{sessions: sessions}
}

// Verify resolves an opaque session token to its Identity. Unknown,
// revoked, or expired tokens wrap pluginapi.ErrUnauthorized.
func (ap *AuthProvider) Verify(ctx context.Context, token string) (pluginapi.Identity, error) {
	return ap.sessions.Verify(ctx, token)
}

// Users manages local user accounts in the users table.
type Users struct {
	DB *sql.DB
}

// NewUsers returns a user manager over an opened (schema-applied) DB.
func NewUsers(db *sql.DB) *Users { return &Users{DB: db} }

// validRole reports whether role is one of the two canonical roles. The
// vocabulary is closed (pluginapi.Identity): "gm" or "player".
func validRole(role string) bool {
	return role == "gm" || role == "player"
}

// Create hashes password with Argon2id and inserts a new local user.
// userID is stored as the row id AND the username; the UNIQUE constraint
// on users.username is the dedupe authority. Duplicate usernames wrap
// pluginapi.ErrConflict; unknown roles are rejected up-front.
func (u *Users) Create(userID, role, password string) error {
	if !validRole(role) {
		return fmt.Errorf("auth: invalid role %q (want \"gm\" or \"player\")", role)
	}
	phc, err := HashPassword(password)
	if err != nil {
		return fmt.Errorf("auth: hash password for %q: %w", userID, err)
	}
	dx, err := u.DB.Exec(`INSERT INTO users(id, role, username, phc, created_at) VALUES(?, ?, ?, ?, ?)`,
		userID, role, userID, phc, timeNowUnix())
	if err != nil {
		if isUniqueConstraint(err) {
			return fmt.Errorf("auth: user %q: %w", userID, pluginapi.ErrConflict)
		}
		return fmt.Errorf("auth: create user %q: %w", userID, err)
	}
	if n, err := dx.RowsAffected(); err == nil && n != 1 {
		return fmt.Errorf("auth: create user %q: inserted %d rows, want 1", userID, n)
	}
	return nil
}

// Login authenticates a username/password against the users table, for
// the PWA login form (tech-stack §3.6 httptest matrix: login/logout).
// Unknown username or wrong password wraps pluginapi.ErrUnauthorized;
// the error is deliberately identical for both so it can't be used to
// enumerate accounts.
func (u *Users) Login(ctx context.Context, username, password string) (pluginapi.Identity, error) {
	var userID, role, phc string
	err := u.DB.QueryRowContext(ctx,
		`SELECT id, role, phc FROM users WHERE username = ?`, username).
		Scan(&userID, &role, &phc)
	if errors.Is(err, sql.ErrNoRows) {
		return pluginapi.Identity{}, fmt.Errorf("auth: login %q: %w", username, pluginapi.ErrUnauthorized)
	}
	if err != nil {
		return pluginapi.Identity{}, fmt.Errorf("auth: login %q: %w", username, err)
	}
	// Note: VerifyPassword returning (false, err) below can ONLY be a
	// malformed stored PHC (hen we wrote it), never attacker input —
	// verify takes the stored string verbatim. Either way it's a refusal.
	ok, err := VerifyPassword(password, phc)
	if err != nil || !ok {
		return pluginapi.Identity{}, fmt.Errorf("auth: login %q: %w", username, pluginapi.ErrUnauthorized)
	}
	return pluginapi.Identity{ID: userID, Role: role}, nil
}

// isUniqueConstraint reports whether a modernc.org/sqlite driver error was
// a UNIQUE constraint violation (UNIQUE conflicts have stable error text
// from SQLite itself; pool is single-connection so pre-checks would only
// be racy theater — the constraint is always the final word).
func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "unique constraint") ||
		strings.Contains(err.Error(), "UNIQUE constraint")
}
