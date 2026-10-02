package rules_test

import (
	"errors"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
)

// draws renders a fixed-length sequence from a source so two sources can be compared
// as strings: a failure then prints the numbers themselves, and twenty of them is
// more than enough to separate two streams without being unreadable.
func draws(source *rand.Rand) string {
	values := make([]string, 0, 20)

	for range 20 {
		values = append(values, strconv.Itoa(source.IntN(1<<20)))
	}

	return strings.Join(values, ",")
}

func TestASeedRoundTripsThroughItsOwnHexForm(t *testing.T) {
	t.Parallel()

	entropy := make([]byte, rules.SeedLen)
	for idx := range entropy {
		entropy[idx] = byte(idx)
	}

	seed, err := rules.NewSeed(entropy)
	if err != nil {
		t.Fatalf("NewSeed: %v", err)
	}

	parsed, err := rules.ParseSeed(seed.String())
	if err != nil {
		t.Fatalf("ParseSeed: %v", err)
	}

	if parsed != seed {
		t.Fatalf("the seed round-tripped to %q", parsed)
	}

	if len(seed.String()) != 2*rules.SeedLen {
		t.Errorf("String() is %d characters, want %d", len(seed.String()), 2*rules.SeedLen)
	}
}

// TestTheZeroSeedIsALegalSeedAndNotASentinel is the assertion behind the type
// comment: there is no "unseeded" value. A sentinel that read as a seed would be a
// campaign whose every roll anyone can reproduce by guessing the sentinel.
//
// Two contexts are built rather than one used twice, so the comparison is between two
// values and not between an expression and itself.
func TestTheZeroSeedIsALegalSeedAndNotASentinel(t *testing.T) {
	t.Parallel()

	var seed rules.Seed

	if len(seed) != rules.SeedLen {
		t.Fatalf("a zero Seed is %d bytes, want %d", len(seed), rules.SeedLen)
	}

	first := rules.Context{Seed: seed}
	second := rules.Context{Seed: seed}

	if draws(first.Rand("draw")) != draws(second.Rand("draw")) {
		t.Error("the zero seed is not deterministic, so it is not a seed")
	}

	// And it is a real seed rather than an absent one: it has to disagree with any
	// other seed, or "zero" would be doing the job a sentinel would do — and a
	// campaign whose every roll is reproducible by typing one hex digit is a campaign
	// whose rolls are not a seed's business at all.
	if draws(first.Rand("draw")) == draws(rules.Context{Seed: rules.Seed{1}}.Rand("draw")) {
		t.Error("the zero seed draws the same numbers as another seed, so it reads as a sentinel")
	}
}

func TestASeedOfTheWrongLengthIsRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		given []byte
	}{
		{name: "empty", given: nil},
		{name: "one byte", given: []byte{1}},
		{name: "one byte short", given: make([]byte, rules.SeedLen-1)},
		{name: "one byte long", given: make([]byte, rules.SeedLen+1)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if _, err := rules.NewSeed(testCase.given); !errors.Is(err, rules.ErrInvalidSeed) {
				t.Fatalf("a seed of %d bytes was accepted: %v", len(testCase.given), err)
			}
		})
	}
}

func TestASeedOfTheWrongHexIsRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
	}{
		{name: "empty", text: ""},
		{name: "too short", text: strings.Repeat("a", 2*rules.SeedLen-1)},
		{name: "too long", text: strings.Repeat("a", 2*rules.SeedLen+1)},
		{name: "not hex", text: strings.Repeat("z", 2*rules.SeedLen)},
		{name: "uppercase hex is not accepted", text: strings.Repeat("A", 2*rules.SeedLen)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if _, err := rules.ParseSeed(testCase.text); !errors.Is(err, rules.ErrInvalidSeed) {
				t.Fatalf("ParseSeed(%q) was accepted: %v", testCase.text, err)
			}
		})
	}
}

// TestTheSameSeedAndLabelDrawTheSameNumbers is S-14.6 at the level it is actually
// reachable from: the source itself, not a resolver built on top of one.
func TestTheSameSeedAndLabelDrawTheSameNumbers(t *testing.T) {
	t.Parallel()

	call := rules.Context{Seed: rules.Seed{1, 2, 3}}

	first := draws(call.Rand("attack/1"))
	second := draws(call.Rand("attack/1"))

	if first != second {
		t.Fatalf("one context drew twice from the same label and got different numbers:\n%s\n%s",
			first, second)
	}
}

// TestDifferentLabelsAndDifferentSeedsDrawDifferentNumbers is the other half of the
// property, and it is the half a broken derivation fails silently: a source that
// ignored the label, or ignored the seed, would still be *deterministic* and the test
// above would pass.
func TestDifferentLabelsAndDifferentSeedsDrawDifferentNumbers(t *testing.T) {
	t.Parallel()

	call := rules.Context{Seed: rules.Seed{1, 2, 3}}
	other := rules.Context{Seed: rules.Seed{9, 9, 9}}

	reference := draws(call.Rand("attack/1"))

	cases := []struct {
		name string
		got  string
	}{
		{name: "another label", got: draws(call.Rand("attack/2"))},
		{name: "a label with a different length", got: draws(call.Rand("attack/10"))},
		{name: "a prefix of the label", got: draws(call.Rand("attack/"))},
		{name: "the empty label", got: draws(call.Rand(""))},
		{name: "another seed", got: draws(other.Rand("attack/1"))},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if testCase.got == reference {
				t.Error("this derivation returned the same numbers as the reference draw")
			}
		})
	}
}

// TestTheEmptyLabelIsUsableAtAll, because it is legal and a reader should not have
// to work that out from a comment: a resolution that needs exactly one draw names it
// anything, including nothing.
func TestTheEmptyLabelIsUsableAtAll(t *testing.T) {
	t.Parallel()

	call := rules.Context{Seed: rules.Seed{4, 5, 6}}

	if draws(call.Rand("")) == "" {
		t.Fatal("the empty label produced nothing")
	}
}

func TestARoleThisBuildDoesNotHaveNeverReachesAResolver(t *testing.T) {
	t.Parallel()

	cases := []domain.Role{
		domain.RoleGM,
		domain.RolePlayer,
	}

	for _, role := range cases {
		if _, err := rules.NewContext(1, 2, role, rules.Seed{}); err != nil {
			t.Errorf("the role %q was refused: %v", role, err)
		}
	}

	unknown := []domain.Role{"", "admin", "GM", "editor"}

	for _, role := range unknown {
		if _, err := rules.NewContext(
			1,
			2,
			role,
			rules.Seed{},
		); !errors.Is(
			err,
			rules.ErrInvalidRole,
		) {
			t.Errorf("the role %q was accepted: %v", role, err)
		}
	}
}

// TestTheZeroContextIsUsableWithoutARole, because the zero value of a struct with an
// exported role is a context a caller can build by accident and a resolver can read.
// What matters is that it does not panic and does not pretend to be anything: there
// is no ambient source behind it.
func TestTheZeroContextIsUsableWithoutARole(t *testing.T) {
	t.Parallel()

	var call rules.Context

	if draws(call.Rand("draw")) != draws(rules.Context{}.Rand("draw")) {
		t.Error("the zero context is not deterministic")
	}
}
