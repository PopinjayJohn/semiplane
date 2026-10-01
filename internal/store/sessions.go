package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AuthSession is one row of `auth_sessions`.
//
// Held here rather than in domain because the domain package models identity and
// tenancy entities, and a session is neither: it is a credential record with a
// lifetime, it is never part of a business decision, and nothing about the S-8
// matrix reads it. TokenHash is the only field that leaves this package in a
// lookup, and it is a hash -- see migration 0004.
type AuthSession struct {
	TokenHash string
	UserID    int64
	CreatedAt time.Time
	ExpiresAt time.Time
}

// The session statements.
//
// The lookup selects on the primary key and filters on expiry in one statement,
// so "unexpired" is decided by the same read that decides "exists". Filtering in
// Go instead would mean fetching a dead row and comparing it against a clock, and
// the comparison would then be a line of code a later caller can forget.
const (
	sessionColumns = "token_hash, user_id, created_at, expires_at"

	insertSession = `INSERT INTO auth_sessions (token_hash, user_id, created_at, expires_at)
		VALUES (?, ?, ?, ?)`

	selectSession = "SELECT " + sessionColumns +
		" FROM auth_sessions WHERE token_hash = ? AND expires_at > ?"

	deleteSession = "DELETE FROM auth_sessions WHERE token_hash = ?"

	deleteExpiredSessions = "DELETE FROM auth_sessions WHERE expires_at <= ?"
)

// CreateSession records a session and returns it as stored.
//
// The token hash is required and so is the expiry, and the expiry is refused
// rather than defaulted: the hash's whole purpose is to be unguessable, so a
// caller that has none has not finished minting the session, and a session with
// no expiry is a bearer token that never stops working. The usual caller is
// auth.NewSessionToken, which returns the token and its hash together precisely so
// that this insert cannot be done half of the pair.
//
// The raw token is not a parameter to this function, and there is no overload
// that takes one. Nothing in this package holds it.
func (s *Store) CreateSession(ctx context.Context, session AuthSession) (AuthSession, error) {
	if session.TokenHash == "" {
		return AuthSession{}, fmt.Errorf("%w: create session", ErrInvalidTokenHash)
	}

	if session.ExpiresAt.IsZero() {
		return AuthSession{}, fmt.Errorf("%w: create session", ErrSessionExpiryRequired)
	}

	if session.CreatedAt.IsZero() {
		session.CreatedAt = nowFunc()
	}

	session.CreatedAt = storedTime(session.CreatedAt)
	session.ExpiresAt = storedTime(session.ExpiresAt)

	// Translated inside the closure, where the driver error is still unwrapped and
	// the operation is still named; the writer wraps whatever comes back with
	// ErrWriteFailed, which preserves the chain.
	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, insertSession,
			session.TokenHash,
			session.UserID,
			unixSeconds(session.CreatedAt),
			unixSeconds(session.ExpiresAt),
		)

		return translateWrite(err, "create session")
	})
	if err != nil {
		return AuthSession{}, err
	}

	return session, nil
}

// SessionByTokenHash resolves a session from a token's hash.
//
// An unknown hash, a revoked one and an expired one are all ErrNotFound, and the
// hash is never logged or returned in the error: the "read session <hash>" wrapper
// that every other read here uses would write a credential into a log line.
func (s *Store) SessionByTokenHash(ctx context.Context, tokenHash string) (AuthSession, error) {
	var (
		session   AuthSession
		createdAt int64
		expiresAt int64
	)

	row := s.db.QueryRowContext(ctx, selectSession, tokenHash, unixSeconds(nowFunc()))

	err := row.Scan(
		&session.TokenHash,
		&session.UserID,
		&createdAt,
		&expiresAt,
	)
	if err != nil {
		return AuthSession{}, translateRead(err, "resolve session")
	}

	session.CreatedAt = unixTime(createdAt)
	session.ExpiresAt = unixTime(expiresAt)

	return session, nil
}

// DeleteSession revokes a session -- logout.
//
// The row is deleted rather than tombstoned; migration 0004 states why, and the
// short version is that a revoked token must not be a row some read has to
// remember to exclude.
//
// Idempotent by design: a token that is already gone is the same success as one
// that was just revoked, so a handler clears the cookie either way and a
// double-submitted logout cannot tell anybody apart.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, deleteSession, tokenHash)

		// No token hash in the wrapper, for the reason SessionByTokenHash's is.
		return translateWrite(err, "delete session")
	})
}

// DeleteExpiredSessions removes every session that had expired at or before
// before, and reports how many it removed.
//
// The bound is a parameter rather than a clock read here, so the sweep is a pure
// function of what the caller asks for: a caller with a grace period passes
// `now - grace`, and a test passes a time it chose. Nothing but the caller
// decides what "expired" means, and a sweep that silently changed its own
// definition would be unassertable.
//
// The count is returned so a caller can log how much it reclaimed, which is the
// only thing that distinguishes a sweep that found nothing from one that is not
// running.
func (s *Store) DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	var removed int64

	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, deleteExpiredSessions, unixSeconds(before))
		if err != nil {
			return translateWrite(err, "delete expired sessions")
		}

		removed, err = result.RowsAffected()

		return translateWrite(err, "count deleted sessions")
	})
	if err != nil {
		return 0, err
	}

	return removed, nil
}
