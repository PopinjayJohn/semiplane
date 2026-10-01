package domain

import "time"

// User is a semiplane account.
//
// A user exists independently of any campaign: instance administration is the
// only capability that is not scoped to one (S-2.7), and it is carried on this
// row rather than derived from a membership.
//
// There is deliberately no String method. A safe one would have to redact
// PasswordHash, and then it would be a second, differently-shaped view of an
// account that a log line silently used instead of the real one; an unsafe one
// would be a hash in every log. Both are worse than a caller naming the fields
// it wants.
type User struct {
	ID       int64
	Username string
	Email    string
	// PasswordHash is a credential, not a field to carry around. It is never
	// rendered into a template, never logged, and never serialised into an
	// error; a handler that needs the account's identity needs the User, not
	// the row.
	PasswordHash string
	IsAdmin      bool
	CreatedAt    time.Time
}
