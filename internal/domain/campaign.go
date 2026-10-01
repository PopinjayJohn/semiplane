package domain

import (
	"errors"
	"fmt"
	"time"
)

// Campaign is a tenant: one content root, one membership list, one live
// tabletop, one gameplay system (S-2.1). Everything below it — pages, assets,
// state, search rows — is scoped by campaign id, and a slug is how a caller
// names one in a URL.
type Campaign struct {
	ID   int64
	Slug string
	Name string
	// ContentRoot is the absolute path to this campaign's vault. The schema
	// fixes it as absolute because a relative path is resolved against whatever
	// directory the process happens to be started in, and the `os.Root` handle
	// built from it is the path-confinement boundary (AGENTS.md, §12). Nothing
	// in this package touches the filesystem, so nothing here can check that
	// the path exists; registration creates the root and the campaign row
	// together, and the row's value is written once.
	ContentRoot string
	// Visibility gates anonymous read. See Visibility for why it is not a
	// permission.
	Visibility Visibility
	// SystemID names the gameplay plugin the campaign resolves with, e.g.
	// `5e-2024`. An unregistered value refuses the game and still serves the
	// wiki (S-14.8), which is a decision for the plugin registry and not for a
	// type.
	SystemID string
	// RulesetVersion fingerprints the resolution semantics the persisted state
	// was written under, not the house-rule configuration. A campaign must not
	// be stranded by someone toggling a house rule, so a value change here is
	// a migration event and never a per-campaign edit (ADR 0018).
	RulesetVersion string
	CreatedAt      time.Time
}

// slugMaxLen bounds a slug at 64 bytes. Every character a slug may contain is
// ASCII, so this is also 64 characters; stating it in bytes keeps the check
// honest for the input that fails it, which is by definition not ASCII.
const slugMaxLen = 64

// ErrInvalidSlug is returned by ValidateSlug. It is a sentinel so a handler can
// answer 400 without matching on the message, and the message still carries the
// rule that was broken for the operator reading the log.
var ErrInvalidSlug = errors.New("domain: invalid campaign slug")

// ValidateSlug enforces the campaign-slug rules.
//
// A slug is the tenancy key in every URL, in every cache key, and in the
// ETag's partition, so the rules belong to the domain rather than to whichever
// handler happens to see the slug first. Two campaigns differing only in slug
// are two tenants, and a slug that renders ambiguously is two tenants for one
// reader.
//
// The rules: 1..64 bytes; lowercase ASCII letters, digits, and single hyphens;
// no leading or trailing hyphen; no consecutive hyphens. Uppercase is rejected
// rather than folded, because folding makes the stored slug differ from the one
// in the URL and the difference surfaces as a second cache entry for the same
// campaign. Every non-ASCII byte is rejected rather than transliterated for the
// same reason, plus homographs: two slugs that differ only by a lookalike
// character are one campaign to a reader and two to the router.
//
// The error names the first rule broken. A slug is a handful of characters, so
// the caller is fixing the string, not debugging a report.
func ValidateSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("%w: empty", ErrInvalidSlug)
	}

	if len(slug) > slugMaxLen {
		return fmt.Errorf("%w: %d bytes, maximum is %d", ErrInvalidSlug, len(slug), slugMaxLen)
	}

	if slug[0] == '-' || slug[len(slug)-1] == '-' {
		return fmt.Errorf("%w: %q may not begin or end with a hyphen", ErrInvalidSlug, slug)
	}

	// Bytes, not runes: a multi-byte rune is rejected by the default branch
	// rather than folded into something ASCII, and the length check above has
	// already bounded how much of it there can be.
	previousHyphen := false

	for idx := range len(slug) {
		char := slug[idx]

		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			previousHyphen = false
		case char == '-':
			if previousHyphen {
				return fmt.Errorf("%w: %q has consecutive hyphens", ErrInvalidSlug, slug)
			}

			previousHyphen = true
		default:
			return fmt.Errorf(
				"%w: %q contains %q at offset %d; allowed: a-z, 0-9, -",
				ErrInvalidSlug, slug, char, idx,
			)
		}
	}

	return nil
}
