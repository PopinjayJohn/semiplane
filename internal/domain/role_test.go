package domain_test

import (
	"errors"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
)

// TestParseRoleAcceptsOnlyTheTwoSchemaValues pins the vocabulary. S-2.6 is
// {gm, player} and there is no ambiguous middle, so a value outside this table
// is a fault in the data rather than a role to be accommodated.
func TestParseRoleAcceptsOnlyTheTwoSchemaValues(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  domain.Role
	}{
		{name: "gm", input: "gm", want: domain.RoleGM},
		{name: "player", input: "player", want: domain.RolePlayer},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := domain.ParseRole(tc.input)
			if err != nil {
				t.Fatalf("ParseRole(%q) error = %v, want nil", tc.input, err)
			}

			if got != tc.want {
				t.Errorf("ParseRole(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestParseRoleRefusesRatherThanNormalising is the privilege-boundary test. A
// role column that is trimmed, case-folded, or defaulted is a column whose
// contents nobody has to be right about: "gm " would silently become a GM and
// "GM" would too, and neither would appear in a log. Every variant here is a
// value the schema forbids, and every one of them must be an error.
func TestParseRoleRefusesRatherThanNormalising(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"",
		" ",
		"gm ",
		" gm",
		"gm\n",
		"gm\t",
		"GM",
		"Gm",
		"Player",
		"PLAYER",
		"editor",
		"owner",
		"admin",
		"gamemaster",
		"player,gm",
		"player ",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			got, err := domain.ParseRole(input)
			if err == nil {
				t.Fatalf("ParseRole(%q) = %q, want an error", input, got)
			}

			if !errors.Is(err, domain.ErrInvalidRole) {
				t.Errorf("ParseRole(%q) error = %v, want ErrInvalidRole", input, err)
			}
		})
	}
}

// TestParseRoleNeverYieldsAValidRoleFromAnUnparseableValue is the sharper
// version of the same rule, and it is the one that matters: a caller that
// handles the error by carrying on must be holding a value that grants
// nothing. If any input below produced a usable Role, the caller's "log it and
// continue" would be a privilege decision made by a typo.
func TestParseRoleNeverYieldsAValidRoleFromAnUnparseableValue(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"",
		"gm ",
		" GM",
		"GM",
		"player ",
		" editor",
		"none",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			got, err := domain.ParseRole(input)
			if err == nil {
				t.Fatalf("ParseRole(%q) = %q, want an error", input, got)
			}

			if got.Valid() {
				t.Errorf("ParseRole(%q) returned the valid role %q alongside its error", input, got)
			}

			if got == domain.RolePlayer {
				t.Errorf(
					"ParseRole(%q) = player; an unknown value must never default to a member",
					input,
				)
			}
		})
	}
}

// TestValidRolesAreExactlyGMAndPlayer asserts the enum has no third value
// waiting to be introduced by a convenient constant, and that the zero value
// is not one of them.
func TestValidRolesAreExactlyGMAndPlayer(t *testing.T) {
	t.Parallel()

	if !domain.RoleGM.Valid() {
		t.Error("RoleGM.Valid() = false, want true")
	}

	if !domain.RolePlayer.Valid() {
		t.Error("RolePlayer.Valid() = false, want true")
	}

	if domain.Role("").Valid() {
		t.Error(`Role("").Valid() = true, want false; the zero value is not a role`)
	}
}

// TestRoleStringIsTheStoredValue documents that String is identity, not a
// validity check. An unrecognised role renders as the bytes that were read —
// which is what makes a log line useful — while a caller comparing against a
// role constant still cannot be handed a privilege it did not have.
func TestRoleStringIsTheStoredValue(t *testing.T) {
	t.Parallel()

	if got := domain.RoleGM.String(); got != "gm" {
		t.Errorf("RoleGM.String() = %q, want %q", got, "gm")
	}

	if got := domain.RolePlayer.String(); got != "player" {
		t.Errorf("RolePlayer.String() = %q, want %q", got, "player")
	}

	if got := domain.Role("gm ").String(); got != "gm " {
		t.Errorf(`Role("gm ").String() = %q, want %q`, got, "gm ")
	}
}
