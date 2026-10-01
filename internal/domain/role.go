package domain

import (
	"errors"
	"fmt"
)

// Role is a campaign membership role.
//
// The two constants are the entire vocabulary (S-2.6). Content editing is
// GM-only and players play, so there is nothing in between, and a third value
// would mean every authorisation check in the project grows a branch for a
// privilege nobody has specified. In particular there is no `editor` role: a
// campaign with a member who may edit content and may not run the game is a
// campaign whose GM is missing.
//
// The zero value is invalid on purpose. A Role(0) is a field nobody set, a
// scan that failed, or a value written by a build that meant something else by
// it, and none of those is a player. ResolveAccess locks such a member out
// rather than guessing.
type Role string

const (
	// RoleGM may edit content, control the game, and invite members.
	RoleGM Role = "gm"
	// RolePlayer may play. Never a content write: a player PUT is a 403
	// (S-14.4).
	RolePlayer Role = "player"
)

// ErrInvalidRole is returned by ParseRole for a value that is not one of the
// two roles. It exists so a caller can tell "this column holds something I do
// not understand" from a storage failure, which are not the same event and not
// the same severity.
var ErrInvalidRole = errors.New("domain: unknown campaign role")

// ParseRole converts a stored role to its Role.
//
// The match is exact, and that is the whole convention: no case folding, no
// surrounding whitespace, no fallthrough to a default. `role` is a privilege
// column, so a value the schema forbids means this row is not the row the code
// thinks it is — a migration that did not apply, a hand-edited database, a
// build from a different version. Every normalisation above is a guess about
// which side of a privilege boundary the value belongs on, and a guess made
// silently is how a `"gm "` becomes a player and a `"GM"` becomes a GM without
// either appearing in a log. Refusing turns that into one loud failure at
// startup or at login.
func ParseRole(s string) (Role, error) {
	role := Role(s)
	if !role.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidRole, s)
	}

	return role, nil
}

// String returns the stored text, verbatim.
//
// Identity is deliberate and is not a validity check: an unrecognised role
// prints as the value that was read, which is what a log line needs, and a
// caller comparing the result against a role constant still cannot be fooled
// into accepting a value the schema forbids. Anything that needs to *know* the
// role is understood must ask Valid.
func (r Role) String() string {
	return string(r)
}

// Valid reports whether r is one of the two roles.
func (r Role) Valid() bool {
	switch r {
	case RoleGM, RolePlayer:
		return true
	default:
		return false
	}
}
