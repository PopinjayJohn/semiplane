package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Sentinels the query layer maps onto. A handler answers a 404, a 409 and a 400
// from these with errors.Is; nothing here needs to read a message, and no test
// asserts on one.
var (
	// ErrNotFound means no row matched. It stands for four unrelated facts --
	// an unknown username, a slug nobody registered, a token that never existed,
	// a session that expired -- and they are deliberately indistinguishable. A
	// caller that could tell them apart could use that to answer differently for
	// an account that does not exist from one that does, which is an account
	// enumeration oracle built into the storage layer.
	ErrNotFound = errors.New("store: no such row")

	// ErrConflict means a uniqueness constraint refused the write: a username
	// or a slug that is already taken, or a membership pair that already exists.
	// It exists so campaign registration can answer 409 without inspecting a
	// driver's result code, and because the loser of two concurrent registrations
	// has to learn *why* it lost.
	ErrConflict = errors.New("store: uniqueness constraint violated")

	// ErrForeignKey means a referenced row does not exist. Distinct from
	// ErrNotFound because the two demand different answers: a membership for a
	// user who does not exist is a mistake in the caller's request and a 400,
	// while a campaign that does not exist is a 404.
	ErrForeignKey = errors.New("store: referenced row does not exist")

	// ErrInvalidUsername means the username was empty. The form of a username --
	// its length, its characters -- is a domain rule that does not exist yet and
	// is not the store's to invent; the empty string alone is refused, because it
	// is the one value that cannot be a name at all, and a row holding it would
	// answer a login form that submitted no username.
	ErrInvalidUsername = errors.New("store: username is empty")

	// ErrInvalidContentRoot means the campaign's content root was not an
	// absolute path. See migration 0005: the value becomes an `os.Root`
	// confinement boundary, so a relative path is a security defect rather than
	// an inconvenience.
	ErrInvalidContentRoot = errors.New("store: campaign content root is not absolute")

	// ErrSessionExpiryRequired means a session was offered without an expiry. A
	// bearer token with no expiry never stops working, and that is what happens
	// when the field is forgotten rather than when it is chosen.
	ErrSessionExpiryRequired = errors.New("store: auth session has no expiry")

	// ErrInvalidTokenHash means a session was offered without a hash. There is
	// no column for a raw token to arrive in, and an empty hash is the shape that
	// mistake takes when one is made.
	ErrInvalidTokenHash = errors.New("store: auth session token hash is empty")
)

// SQLite extended result codes, from sqlite3.h. SQLite assigns each one once and
// never renumbers it, so these are stable for the life of the file format.
//
// Two details here are not obvious. `users.username` carries a UNIQUE index and
// therefore reports SQLITE_CONSTRAINT_UNIQUE, while `auth_sessions.token_hash` and
// the `campaign_members` pair are declared PRIMARY KEY and report
// SQLITE_CONSTRAINT_PRIMARYKEY -- both are a caller-visible conflict. And SQLite
// does *not* report an immediate foreign-key violation as
// SQLITE_CONSTRAINT_FOREIGNKEY; it raises SQLITE_CONSTRAINT_TRIGGER, because the
// check runs as a trigger. This schema declares no triggers of its own, so that
// code is unambiguous here; a trigger added later would need the mapping widened
// rather than this comment rewritten.
const (
	resultConstraint = 19

	resultConstraintUnique     = resultConstraint | (8 << 8)
	resultConstraintPrimaryKey = resultConstraint | (6 << 8)
	resultConstraintForeignKey = resultConstraint | (3 << 8)
	resultConstraintTrigger    = resultConstraint | (7 << 8)
)

// sqliteResultCode is the shape of the driver's error.
//
// Matched structurally with errors.As rather than by naming the driver type, so
// the package keeps one blank import in one place (store.go registers it) and
// does not depend on the driver's generated internals for three numbers.
type sqliteResultCode interface {
	Code() int
}

// constraintSentinel returns the sentinel a constraint violation maps onto, or
// nil when err is not one.
func constraintSentinel(err error) error {
	var coded sqliteResultCode
	if !errors.As(err, &coded) {
		return nil
	}

	switch coded.Code() {
	case resultConstraintUnique, resultConstraintPrimaryKey:
		return ErrConflict
	case resultConstraintForeignKey, resultConstraintTrigger:
		return ErrForeignKey
	default:
		return nil
	}
}

// translateRead maps a read's failure onto this package's vocabulary.
//
// Every read in this package funnels its error through here, so the mapping from
// "no rows" to ErrNotFound lives in one place rather than being reimplemented --
// and slightly differently -- per query.
func translateRead(err error, what string) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, sql.ErrNoRows) {
		return errors.Join(ErrNotFound, detail(what, err))
	}

	return detail(what, err)
}

// translateWrite is translateRead plus the constraint mapping, which a read
// never needs: only a write can be refused by a constraint.
//
// errors.Join rather than a single %w wrap, because both the sentinel and the
// driver's own message are wanted: a caller needs errors.Is to decide the
// response, and an operator reading the log needs to know which constraint
// fired. Joining keeps both reachable; wrapping one keeps only one.
func translateWrite(err error, what string) error {
	if err == nil {
		return nil
	}

	if sentinel := constraintSentinel(err); sentinel != nil {
		return errors.Join(sentinel, detail(what, err))
	}

	return translateRead(err, what)
}

// detail wraps a driver error with the operation that produced it.
func detail(what string, err error) error {
	return fmt.Errorf("store: %s: %w", what, err)
}

// unixSeconds converts a time for storage.
//
// Never hand a time.Time to the driver for an INTEGER column: it renders as a
// formatted string, SQLite's dynamic typing stores that as TEXT, and every later
// comparison against an integer sorts the text values last -- hiding exactly the
// rows the comparison was written to find. Migration 0001 records the same
// decision against `applied_at`.
func unixSeconds(moment time.Time) int64 {
	return moment.UTC().Unix()
}

// unixTime is unixSeconds' inverse, always in UTC.
func unixTime(seconds int64) time.Time {
	return time.Unix(seconds, 0).UTC()
}

// storedTime is the resolution the schema actually keeps.
//
// A create function returns the value it stored rather than the one it was handed,
// so this is applied on the way in: a second is what an INTEGER column holds, and
// a returned time carrying sub-second precision would compare unequal to the row it
// came from -- which is the sort of mismatch a round-trip test reports as a store
// defect and nobody can afterwards reproduce.
func storedTime(moment time.Time) time.Time {
	return unixTime(moment.Unix())
}
