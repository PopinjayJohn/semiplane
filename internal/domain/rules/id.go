package rules

import (
	"fmt"
)

// maxIDLen bounds a system id at 64 bytes.
//
// 64 is not derived from anything. It is `domain`'s slug bound, applied because
// the two values are read side by side by a human — `5e-2024` in a URL path and
// in a status page — and a bound that differed between them would be a question
// nobody would think to ask. Every permitted character is ASCII, so it is also 64
// characters; stating it in bytes keeps the check honest for the input that fails
// it, which is by definition not ASCII.
const maxIDLen = 64

// ID names a gameplay system: `5e-2024`, `5e-2014`, `pathfinder`.
//
// A string and not an enum because the set of systems is a property of the running
// build (the registry), for the reason `domain.PageKind` is a string: a closed list
// here would be a list a compiled-in plugin could not join.
//
// It is a **distinct type** from `realtime.Op` and from `domain.Campaign.SystemID`
// even though all three hold the same characters. `campaigns.system_id` is a TEXT
// column read through a string, `ParseID` is what a persisted value goes through
// before it is believed, and the naming makes "the registry looked this up and
// found nothing" (S-10.6) a question a reader can answer from the type at the
// call site.
type ID string

// ErrInvalidID is returned by `ParseID`.
//
// Named here rather than in `rules.go` for the reader's sake alone: the whole set
// is declared in one place, and a type whose validity has its own rule is
// documented next to that rule.
var ErrInvalidID = fmt.Errorf("%w: the system id is not a usable identifier", ErrMalformedSystem)

// ParseID converts a stored or configured id to an ID.
//
// The rules are 1..64 bytes of lowercase ASCII letters, digits and single hyphens,
// with no leading, trailing or consecutive hyphen — the same shape as
// `domain.ValidateSlug`, and **not** the same function: a slug is a tenancy key
// chosen by a person and changeable by an administrator, while an id is written
// into `campaigns.system_id` and into persisted state that a resume compares
// against. Coupling the two would mean a rule about how a URL looks could strand
// a campaign.
//
// Uppercase is refused rather than folded, and so is every non-ASCII byte. Both
// for the reason `ValidateSlug` gives — folding makes the stored value differ
// from the one on the wire and the difference surfaces as a second entry for one
// system — and one more: a homoglyph id is one system to a reader and two to a
// lookup, and the reader is the one who would have to tell them apart at the
// table.
//
// The error names the first rule broken and quotes the value, which is a
// compiled-in identifier rather than anything a client sent.
func ParseID(text string) (ID, error) {
	id := ID(text)
	if !id.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidID, text)
	}

	return id, nil
}

// String returns the stored text, verbatim.
//
// Identity and not a validity check, as with `domain.Role.String`: an id read from
// a column is printed as what was read, so an operator can see the value that
// failed. A caller that needs to *know* the id is registered must ask the
// registry, not believe a string.
func (id ID) String() string {
	return string(id)
}

// Valid reports whether id is a usable system identifier.
func (id ID) Valid() bool {
	if id == "" || len(id) > maxIDLen {
		return false
	}

	if id[0] == '-' || id[len(id)-1] == '-' {
		return false
	}

	previousHyphen := false

	for idx := range len(id) {
		char := id[idx]

		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			previousHyphen = false
		case char == '-':
			if previousHyphen {
				return false
			}

			previousHyphen = true
		default:
			return false
		}
	}

	return true
}
