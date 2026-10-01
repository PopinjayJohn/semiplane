package domain_test

import (
	"errors"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
)

// TestParseVisibilityAcceptsOnlyTheTwoSchemaValues pins the vocabulary. A
// visibility decides who may read a tenant without a session, so the set of
// values that can mean that is two.
func TestParseVisibilityAcceptsOnlyTheTwoSchemaValues(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  domain.Visibility
	}{
		{name: "private", input: "private", want: domain.VisibilityPrivate},
		{name: "public", input: "public", want: domain.VisibilityPublic},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := domain.ParseVisibility(tc.input)
			if err != nil {
				t.Fatalf("ParseVisibility(%q) error = %v, want nil", tc.input, err)
			}

			if got != tc.want {
				t.Errorf("ParseVisibility(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestParseVisibilityRefusesRatherThanNormalising is the same privilege test as
// the role one, on the column that decides anonymous read. Trimming or folding
// this value is how a typo in a hand-edited row becomes a published campaign.
func TestParseVisibilityRefusesRatherThanNormalising(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"",
		" ",
		"public ",
		" public",
		"public\n",
		"Public",
		"PUBLIC",
		"Public ",
		"unlisted",
		"protected",
		"none",
		"public,private",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			got, err := domain.ParseVisibility(input)
			if err == nil {
				t.Fatalf("ParseVisibility(%q) = %q, want an error", input, got)
			}

			if !errors.Is(err, domain.ErrInvalidVisibility) {
				t.Errorf("ParseVisibility(%q) error = %v, want ErrInvalidVisibility", input, err)
			}
		})
	}
}

// TestParseVisibilityNeverFallsBackToPublic is the assertion that an
// unparseable visibility cannot become the readable one by accident. A caller
// that logs the error and continues must be holding something that keeps the
// campaign private, because the failure of a misread visibility is a published
// tenant rather than a locked-out one.
func TestParseVisibilityNeverFallsBackToPublic(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"",
		"Public",
		"public ",
		" pubilc",
		"none",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			got, err := domain.ParseVisibility(input)
			if err == nil {
				t.Fatalf("ParseVisibility(%q) = %q, want an error", input, got)
			}

			if got == domain.VisibilityPublic {
				t.Errorf(
					"ParseVisibility(%q) = public; an unknown value must never widen access",
					input,
				)
			}

			if got.Valid() {
				t.Errorf(
					"ParseVisibility(%q) returned the valid value %q alongside its error",
					input,
					got,
				)
			}
		})
	}
}

// TestValidVisibilitiesAreExactlyPrivateAndPublic asserts the enum is closed
// and that its zero value is not a member of it: a Campaign built with no
// visibility set is not a public campaign.
func TestValidVisibilitiesAreExactlyPrivateAndPublic(t *testing.T) {
	t.Parallel()

	if !domain.VisibilityPrivate.Valid() {
		t.Error("VisibilityPrivate.Valid() = false, want true")
	}

	if !domain.VisibilityPublic.Valid() {
		t.Error("VisibilityPublic.Valid() = false, want true")
	}

	if domain.Visibility("").Valid() {
		t.Error(`Visibility("").Valid() = true, want false; the zero value is not a visibility`)
	}
}

// TestVisibilityStringIsTheStoredValue documents that String is identity, not a
// validity check.
func TestVisibilityStringIsTheStoredValue(t *testing.T) {
	t.Parallel()

	if got := domain.VisibilityPublic.String(); got != "public" {
		t.Errorf("VisibilityPublic.String() = %q, want %q", got, "public")
	}

	if got := domain.Visibility(" public").String(); got != " public" {
		t.Errorf(`Visibility(" public").String() = %q, want %q`, got, " public")
	}
}
