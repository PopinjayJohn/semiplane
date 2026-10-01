package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/semiplane/semiplane/internal/domain"
)

// The users statements. Named constants rather than inline literals so the
// column list appears exactly once: the SELECT list and the scan list in
// UserByID have to agree, and two literals are two things to keep in step.
//
// The insert does not name `id`. The store assigns primary keys, so a caller
// cannot hand out an id and collide with a row another registration committed --
// which, on this connection, would not even be a race so much as a certainty.
const (
	userColumns = "id, username, email, password_hash, is_admin, created_at"

	insertUser = `INSERT INTO users (username, email, password_hash, is_admin, created_at)
		VALUES (?, ?, ?, ?, ?)`

	selectUserByID       = "SELECT " + userColumns + " FROM users WHERE id = ?"
	selectUserByUsername = "SELECT " + userColumns + " FROM users WHERE username = ?"

	deleteUser = "DELETE FROM users WHERE id = ?"
)

// CreateUser inserts a user and returns it as stored.
//
// The id is assigned here, and CreatedAt is filled from the clock when the
// caller left it zero -- a fixture and a seed state a time deliberately, and a
// registration does not. The returned value is what the database holds rather
// than what was asked for, which is what makes a round trip assertable.
//
// A username already in use comes back as ErrConflict, and the database is what
// decides: the check-then-insert alternative has a window, and the single-writer
// queue runs the two statements as two separate transactions.
func (s *Store) CreateUser(ctx context.Context, user domain.User) (domain.User, error) {
	if user.Username == "" {
		return domain.User{}, fmt.Errorf("%w: %s", ErrInvalidUsername, "create user")
	}

	if user.CreatedAt.IsZero() {
		user.CreatedAt = nowFunc()
	}

	user.CreatedAt = storedTime(user.CreatedAt)

	what := "create user " + user.Username

	var id int64

	// Translated inside the closure, where the driver error is still unwrapped and
	// the operation is still named. The writer wraps whatever comes back with
	// ErrWriteFailed, which preserves the chain -- so a caller reaching for
	// ErrConflict still finds it.
	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, insertUser,
			user.Username, user.Email, user.PasswordHash, user.IsAdmin,
			unixSeconds(user.CreatedAt),
		)
		if err != nil {
			return translateWrite(err, what)
		}

		id, err = result.LastInsertId()

		return translateWrite(err, what)
	})
	if err != nil {
		return domain.User{}, err
	}

	user.ID = id

	return user, nil
}

// UserByUsername reads one account by its exact username.
//
// An unknown username is ErrNotFound, and it is deliberately indistinguishable
// from a wrong password: both mean "these credentials do not authenticate", and
// a login form that can tell them apart reports which usernames exist.
func (s *Store) UserByUsername(ctx context.Context, username string) (domain.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, selectUserByUsername, username),
		"read user "+username)
}

// UserByID reads one account by id, for a caller that already resolved it --
// from a session row, or from a membership.
func (s *Store) UserByID(ctx context.Context, id int64) (domain.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, selectUserByID, id),
		fmt.Sprintf("read user %d", id))
}

// DeleteUser removes an account.
//
// A user who is a member of any campaign is refused with ErrForeignKey: the
// membership points at a row that must not disappear. See migration 0006 for why
// this one deletion cascades and that one does not. Removing a user who does not
// exist is not an error -- there is nothing left to be inconsistent about.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, deleteUser, id)

		return translateWrite(err, fmt.Sprintf("delete user %d", id))
	})
}

// scanUser reads a single user row.
//
// A row.Scan rather than an inline scan in each query, so that UserByID and
// UserByUsername cannot drift apart in what they return.
func scanUser(row *sql.Row, what string) (domain.User, error) {
	var (
		user      domain.User
		createdAt int64
	)

	err := row.Scan(
		&user.ID,
		&user.Username,
		&user.Email,
		&user.PasswordHash,
		&user.IsAdmin,
		&createdAt,
	)
	if err != nil {
		return domain.User{}, translateRead(err, what)
	}

	user.CreatedAt = unixTime(createdAt)

	return user, nil
}
