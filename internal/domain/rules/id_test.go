package rules_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

func TestParseIDAcceptsOnlyLowercaseTokenLikeNames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   string
		want bool
	}{
		{name: "a versioned system", id: "5e-2024", want: true},
		{name: "a bare name", id: "pathfinder", want: true},
		{name: "a name with digits", id: "sys4", want: true},
		{name: "a single character", id: "x", want: true},
		{name: "empty", id: "", want: false},
		{name: "uppercase", id: "5E-2024", want: false},
		{name: "a leading hyphen", id: "-5e", want: false},
		{name: "a trailing hyphen", id: "5e-", want: false},
		{name: "consecutive hyphens", id: "5e--2024", want: false},
		{name: "an underscore", id: "five_e", want: false},
		{name: "a space", id: "5e 2024", want: false},
		{name: "a dot", id: "5e.2024", want: false},
		{name: "a non-ascii letter", id: "5é", want: false},
		{name: "a newline", id: "5e\n2024", want: false},
		{name: "over the bound", id: strings.Repeat("a", 65), want: false},
		{name: "exactly at the bound", id: strings.Repeat("a", 64), want: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			id, err := rules.ParseID(testCase.id)

			if testCase.want {
				if err != nil {
					t.Fatalf("ParseID(%q): %v", testCase.id, err)
				}

				if id.String() != testCase.id {
					t.Errorf("ParseID(%q) = %q", testCase.id, id)
				}

				return
			}

			if !errors.Is(err, rules.ErrInvalidID) {
				t.Fatalf("ParseID(%q) was accepted: %v", testCase.id, err)
			}

			if id != "" {
				t.Errorf("a refused id must come back empty, got %q", id)
			}
		})
	}
}

// TestAnIDRoundTripsThroughItsOwnString is what makes a stored id re-readable: the
// value that reaches `campaigns.system_id` has to survive a `ParseID` on the way
// back, or a resume would refuse a campaign whose id this build itself wrote.
func TestAnIDRoundTripsThroughItsOwnString(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"5e-2024", "5e-2014", "pathfinder", "x", strings.Repeat("a", 64)} {
		first, err := rules.ParseID(text)
		if err != nil {
			t.Fatalf("ParseID(%q): %v", text, err)
		}

		second, err := rules.ParseID(first.String())
		if err != nil {
			t.Fatalf("ParseID(%q) on the round trip: %v", first, err)
		}

		if first != second {
			t.Errorf("%q round-tripped to %q", text, second)
		}

		if !first.Valid() {
			t.Errorf("%q parsed but does not report itself valid", text)
		}
	}
}

// TestAnIDIsPrintedAsItWasRead, because a refusal that prints the *fixed* form of a
// value an operator must go and find is a refusal that makes the work harder.
func TestAnIDIsPrintedAsItWasRead(t *testing.T) {
	t.Parallel()

	unregistered := rules.ID("5e-224")

	if !strings.Contains(unregistered.String(), "224") {
		t.Fatalf("String() = %q, want the value verbatim", unregistered)
	}
}
