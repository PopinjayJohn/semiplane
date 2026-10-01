package domain

import (
	"errors"
	"fmt"
)

// Visibility gates anonymous read.
//
// `public` means the wiki and public assets are readable without a session, and
// nothing more: games always require membership (S-8.1), and no visibility makes
// a non-member a player. It is the one setting in the schema that is
// deliberately not a permission — a value that reads as an access level is how
// a public campaign's private-looking paths get treated as open.
//
// The zero value is invalid, and it is read as *not* public. A campaign whose
// visibility was never set, or was set by a build that meant something else,
// stays private: the failure mode of a misread visibility is a published
// campaign, and a published campaign cannot be unpublished retroactively in
// anybody's browser cache.
type Visibility string

const (
	// VisibilityPrivate restricts every read to members.
	VisibilityPrivate Visibility = "private"
	// VisibilityPublic permits anonymous wiki read. Never anonymous play.
	VisibilityPublic Visibility = "public"
)

// ErrInvalidVisibility is returned by ParseVisibility for a value that is
// neither `private` nor `public`. It is distinguishable from a storage failure
// because the two demand different responses: one is bad data, the other is a
// broken database.
var ErrInvalidVisibility = errors.New("domain: unknown campaign visibility")

// ParseVisibility converts a stored visibility to its Visibility.
//
// The match is exact, for the same reason ParseRole's is: this is a
// caller-controlled column deciding who may read a tenant, and a value the
// schema forbids is a fault to report rather than an input to accommodate. No
// trimming, no case folding, no defaulting to private — the last because
// quietly downgrading a public campaign breaks its owner, and quietly
// upgrading a private one publishes it.
func ParseVisibility(s string) (Visibility, error) {
	visibility := Visibility(s)
	if !visibility.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidVisibility, s)
	}

	return visibility, nil
}

// String returns the stored text, verbatim. As with Role.String this is
// identity rather than a validity check; ask Valid to know whether the value is
// one this build understands.
func (v Visibility) String() string {
	return string(v)
}

// Valid reports whether v is one of the two visibilities.
func (v Visibility) Valid() bool {
	switch v {
	case VisibilityPrivate, VisibilityPublic:
		return true
	default:
		return false
	}
}
