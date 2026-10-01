package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
)

// TestValidateSlugAcceptsConformantSlugs covers the shapes a real campaign
// name takes once punctuation is stripped.
func TestValidateSlugAcceptsConformantSlugs(t *testing.T) {
	t.Parallel()

	slugs := []string{
		"a",
		"9",
		"curse-of-strawberry",
		"the-man-who-was-thursday",
		"d100",
		"5e-2024",
		"a-b-c-d",
		"b1-b2-b3",
		strings.Repeat("a", 64),
		strings.Repeat("ab-", 21) + "c", // 64 bytes, ending on a letter
	}

	for _, slug := range slugs {
		t.Run(slug, func(t *testing.T) {
			t.Parallel()

			if err := domain.ValidateSlug(slug); err != nil {
				t.Errorf("ValidateSlug(%q) error = %v, want nil", slug, err)
			}
		})
	}
}

// TestValidateSlugRejectsNonConformantSlugs is the table of every way a slug
// can be wrong. A slug is the tenancy key in the URL, so a rejected one is a
// registration refused rather than a page that 404s later.
func TestValidateSlugRejectsNonConformantSlugs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		slug string
	}{
		{name: "empty", slug: ""},
		{name: "space", slug: " "},
		{name: "leading hyphen", slug: "-curse"},
		{name: "trailing hyphen", slug: "curse-"},
		{name: "both hyphens", slug: "-curse-"},
		{name: "hyphen only", slug: "-"},
		{name: "consecutive hyphens", slug: "curse--of"},
		{name: "consecutive hyphens at the end", slug: "curse--"},
		{name: "consecutive hyphens at the start", slug: "--curse"},
		{name: "uppercase", slug: "Curse"},
		{name: "all uppercase", slug: "CURSE"},
		{name: "mixed case", slug: "Curse-Of"},
		{name: "underscore", slug: "curse_of"},
		{name: "dot", slug: "curse.of"},
		{name: "double dot", slug: ".."},
		{name: "slash", slug: "curse/of"},
		{name: "backslash", slug: `curse\of`},
		{name: "leading slash", slug: "/curse"},
		{name: "parent directory", slug: "../curse"},
		{name: "leading dot", slug: ".curse"},
		{name: "tilde", slug: "~curse"},
		{name: "percent escape", slug: "curse%2f"},
		{name: "query separator", slug: "curse?q="},
		{name: "fragment separator", slug: "curse#frag"},
		{name: "space inside", slug: "curse of"},
		{name: "leading space", slug: " curse"},
		{name: "trailing space", slug: "curse "},
		{name: "newline", slug: "curse\nof"},
		{name: "tab", slug: "curse\tof"},
		{name: "nul byte", slug: "curse\x00of"},
		{name: "trailing newline", slug: "curse\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := domain.ValidateSlug(tc.slug)
			if err == nil {
				t.Fatalf("ValidateSlug(%q) = nil, want an error", tc.slug)
			}

			if !errors.Is(err, domain.ErrInvalidSlug) {
				t.Errorf("ValidateSlug(%q) error = %v, want ErrInvalidSlug", tc.slug, err)
			}
		})
	}
}

// TestValidateSlugBoundsLengthAtSixtyFour checks the boundary in both
// directions, because a length limit that is only tested from above passes just
// as happily with the limit in the wrong place.
func TestValidateSlugBoundsLengthAtSixtyFour(t *testing.T) {
	t.Parallel()

	if err := domain.ValidateSlug(strings.Repeat("a", 64)); err != nil {
		t.Errorf("ValidateSlug(64 bytes) error = %v, want nil", err)
	}

	if err := domain.ValidateSlug(strings.Repeat("a", 65)); err == nil {
		t.Error("ValidateSlug(65 bytes) = nil, want an error")
	}
}

// TestValidateSlugRejectsNonASCIIWithoutFolding is the homograph test. Every
// slug allowed here is ASCII, so any non-ASCII byte is either a transliteration
// that would make the stored slug differ from the URL, or a lookalike for a
// character that is not there — and two campaigns differing only by a lookalike
// are one campaign to a reader and two to the router.
func TestValidateSlugRejectsNonASCIIWithoutFolding(t *testing.T) {
	t.Parallel()

	// Every slug below is spelled in escapes on purpose: the point of the test
	// is which bytes are present, and a literal that renders as ordinary text
	// in a diff hides exactly the thing being asserted.
	slugs := []struct {
		name string
		slug string
	}{
		{name: "trailing accent", slug: "caf\u00e9"},
		{name: "leading accent", slug: "\u00e9camp"},
		{name: "cyrillic homograph", slug: "c\u0430mp"},
		{name: "greek omicron homograph", slug: "c\u03bfmp"},
		{name: "fullwidth", slug: "\uff43\uff41\uff4d\uff50"},
		{name: "em dash", slug: "curse\u2014of"},
		{name: "en dash", slug: "curse\u2013of"},
		{name: "non-breaking hyphen", slug: "curse\u2011of"},
		{name: "soft hyphen", slug: "curse\u00adof"},
		{name: "zero width joiner", slug: "curse\u200dof"},
		{name: "right-to-left override", slug: "curse\u202eof"},
		{name: "emoji", slug: "curse-of-straw\u1f353"},
	}

	for _, tc := range slugs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := domain.ValidateSlug(tc.slug); err == nil {
				t.Errorf("ValidateSlug(%q) = nil, want an error for non-ASCII input", tc.slug)
			}
		})
	}
}

// TestValidateSlugIsPure asserts the same slug always gets the same answer, and
// that a valid slug stays valid. Registration and the resolver read the same
// field, and a validator whose verdict moved would make a live campaign
// unreachable without any change to its content.
func TestValidateSlugIsPure(t *testing.T) {
	t.Parallel()

	for range 16 {
		if err := domain.ValidateSlug("curse-of-strawberry"); err != nil {
			t.Fatalf("ValidateSlug error = %v, want nil on every call", err)
		}
	}
}
