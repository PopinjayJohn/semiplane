// Session tokens: opaque 32-byte rand tokens stored sha256-hashed in the
// sessions table, plus the cookie helpers that carry them (tech-stack
// §3.6). (Package doc lives in auth.go.)
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// SessionCookieName is the browser cookie carrying the opaque session
// token.
const SessionCookieName = "semiplane_session"

// SessionService manages opaque session tokens (sessions table).
// Tokens are persisted as HashToken fingerprints (col `hash`) — the
// plaintext token exists only at Create time and in the client's cookie.
type SessionService struct {
	DB *sql.DB
}

// NewSessionService returns a session manager over an opened
// (schema-applied) DB — Open or the shared internal/index pool.
func NewSessionService(db *sql.DB) *SessionService {
	return &SessionService{DB: db}
}

// Create issues a new session for userID, persisting the hash, and
// returns the plaintext token for the caller to place in a cookie. ttl is
// rounded to whole seconds (schema stores INTEGER unix seconds).
func (s *SessionService) Create(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	token, hash, err := NewToken()
	if err != nil {
		return "", fmt.Errorf("auth: new session token: %w", err)
	}
	now := timeNow()
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO sessions(id, user_id, created_at, expires_at, hash) VALUES(?, ?, ?, ?, ?)`,
		hash, userID, now.Unix(), now.Add(ttl).Unix(), hash); err != nil {
		return "", fmt.Errorf("auth: persist session: %w", err)
	}
	return token, nil
}

// Verify resolves a session token to its Identity by joining users for
// the role. The token is expired iff its stored expires_at is not strictly
// in the future (INTEGER unix seconds). Unknown, revoked, expired, or
// user-gone sessions all wrap pluginapi.ErrUnauthorized — deliberately
// indistinguishable, same as Login.
func (s *SessionService) Verify(ctx context.Context, token string) (pluginapi.Identity, error) {
	if strings.TrimSpace(token) == "" {
		return pluginapi.Identity{}, fmt.Errorf("auth: empty session token: %w", pluginapi.ErrUnauthorized)
	}
	var userID, role string
	var exp int64
	err := s.DB.QueryRowContext(ctx,
		`SELECT s.user_id, u.role, s.expires_at
		   FROM sessions s LEFT JOIN users u ON u.id = s.user_id
		  WHERE s.hash = ?`, HashToken(token)).
		Scan(&userID, &role, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return pluginapi.Identity{}, fmt.Errorf("auth: session unknown, revoked, or expired: %w", pluginapi.ErrUnauthorized)
	}
	if err != nil {
		return pluginapi.Identity{}, fmt.Errorf("auth: session lookup: %w", err)
	}
	if exp <= timeNow().Unix() {
		return pluginapi.Identity{}, fmt.Errorf("auth: session expired: %w", pluginapi.ErrUnauthorized)
	}
	if !validRole(role) {
		// Session row joined a users row with a corrupt/foreign role
		// (LEFT JOIN keeps userID even when the user row vanished —
		// cannot yield an Identity with an empty/wrong Role).
		return pluginapi.Identity{}, fmt.Errorf("auth: session user role %q invalid: %w", role, pluginapi.ErrUnauthorized)
	}
	return pluginapi.Identity{ID: userID, Role: role}, nil
}

// Revoke deletes a single session (logout). Revoking an already-gone
// token is not an error.
func (s *SessionService) Revoke(ctx context.Context, token string) error {
	if _, err := s.DB.ExecContext(ctx,
		`DELETE FROM sessions WHERE hash = ?`, HashToken(token)); err != nil {
		return fmt.Errorf("auth: revoke session: %w", err)
	}
	return nil
}

// RevokeAllFor kills every session of one user (password change, account
// disable, "log me out everywhere").
func (s *SessionService) RevokeAllFor(ctx context.Context, userID string) error {
	if _, err := s.DB.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("auth: revoke all sessions for %q: %w", userID, err)
	}
	return nil
}

// CleanupExpired deletes expired session rows. Cheap; call from a
// maintenance tick alongside the other CleanupExpired implementations.
func (s *SessionService) CleanupExpired(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at <= ?`, timeNow().Unix()); err != nil {
		return fmt.Errorf("auth: cleanup expired sessions: %w", err)
	}
	return nil
}

// SetSessionCookie writes the session cookie: HttpOnly (never readable
// from JS), SameSite=Lax (CSRF default for top-level safe navigations),
// Path=/, and the Secure flag controlled by the caller. Secure must come
// from CookieSecure — the app has no in-app TLS and honors
// X-Forwarded-Proto behind a reverse proxy (tech-stack §3.6).
func SetSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// ClearSessionCookie expires the session cookie in the browser.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1, // delete now
		SameSite: http.SameSiteLaxMode,
	})
}

// CookieSecure reports whether the session cookie for req should carry
// the Secure flag: true when X-Forwarded-Proto is "https" (the app trusts
// the reverse proxy for transport — tech-stack §3.6) or when an always-on
// config flag is set (e.g. deployments where the proxy doesn't forward
// the header). It takes the force flag explicitly instead of reading
// package state so wiring stays pure.
//
// NOTE (signature deviation from the task brief): the brief's one-arg
// `CookieSecure(req)` suggestion was widened to `CookieSecure(req,
// forceSecure bool)` so the always-on flag part of the same sentence has
// somewhere to come from without introducing a mutable global.
func CookieSecure(req *http.Request, forceSecure bool) bool {
	return forceSecure ||
		strings.EqualFold(req.Header.Get("X-Forwarded-Proto"), "https")
}
