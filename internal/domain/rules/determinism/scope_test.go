package determinism_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
)

// TestScopeAdmissibleAcceptsEveryDataLevelCapability is S-10.5's "data-level only",
// from the affirmative side, and it is a table rather than three assertions because
// the table is what "data-level only" *is* — three capabilities, and the claim
// fails the day a fourth is added without a decision.
//
// §10.5's own examples are the three, taken verbatim from the record: toggle
// `flanking_optional`, change a DC formula constant, disable a condition.
func TestScopeAdmissibleAcceptsEveryDataLevelCapability(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		scope determinism.Scope
	}{
		{name: "nothing at all", scope: nil},
		{name: "the zero value", scope: determinism.Scope{}},
		{name: "a toggle", scope: determinism.Scope{determinism.CapToggle}},
		{name: "a constant", scope: determinism.Scope{determinism.CapConstant}},
		{name: "a disabled condition", scope: determinism.Scope{determinism.CapDisable}},
		{
			name: "all three, which is a module a GM could actually configure",
			scope: determinism.Scope{
				determinism.CapDisable,
				determinism.CapToggle,
				determinism.CapConstant,
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if err := testCase.scope.Admissible(); err != nil {
				t.Fatalf("Admissible() on %v = %v, want nil", testCase.scope, err)
			}
		})
	}
}

// TestScopeAdmissibleRefusesTheTwoNamedFailures is S-10.5's rejection, and the two
// rejections are named rather than lumped together because the caller acts on them
// differently: a module that reorders resolution has to be rewritten, and a module
// that reaches for unseeded randomness has to be rewritten *differently*, because
// the second one is the failure the whole phase is about.
func TestScopeAdmissibleRefusesTheTwoNamedFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		scope   determinism.Scope
		wantErr error
	}{
		{
			name:    "reordering resolution",
			scope:   determinism.Scope{determinism.CapReorder},
			wantErr: determinism.ErrReordersResolution,
		},
		{
			name:    "drawing unseeded randomness",
			scope:   determinism.Scope{determinism.CapUnseededRandomness},
			wantErr: determinism.ErrUnseededRandomness,
		},
		{
			name: "both, where the first is the one reported",
			scope: determinism.Scope{
				determinism.CapUnseededRandomness,
				determinism.CapReorder,
			},
			wantErr: determinism.ErrUnseededRandomness,
		},
		{
			name:    "a data-level capability beside a refused one",
			scope:   determinism.Scope{determinism.CapToggle, determinism.CapReorder},
			wantErr: determinism.ErrReordersResolution,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := testCase.scope.Admissible()
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("Admissible() on %v = %v, want one wrapping %v",
					testCase.scope, err, testCase.wantErr)
			}

			if !errors.Is(err, determinism.ErrInadmissibleModule) {
				t.Error("the refusal does not satisfy ErrInadmissibleModule, so a " +
					"registrar asking one question cannot act on it")
			}
		})
	}
}

// TestScopeAdmissibleRefusesACapabilityNobodyHasClassified is the direction the
// whole check exists to get right, and it is the one a naive implementation gets
// wrong by omission.
//
// An unrecognised capability could be treated as harmless — "not one of the two we
// refuse" — and that is the fail-open reading. It must be refused, because the next
// build to add a capability may classify it as a refusal, and a build that enabled
// the module in the meantime has already run it.
func TestScopeAdmissibleRefusesACapabilityNobodyHasClassified(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		capability determinism.Capability
	}{
		{name: "empty", capability: determinism.Capability("")},
		{name: "a near miss on a real one", capability: determinism.Capability("Toggle")},
		{name: "a trailing space", capability: determinism.Capability("toggle ")},
		{name: "a plausible invention", capability: determinism.Capability("rewrite-pack")},
		{name: "a plausible invention, hyphenated", capability: determinism.Capability("re-order")},
		{name: "the word on its own", capability: determinism.Capability("nondeterminism")},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := determinism.Scope{testCase.capability}.Admissible()
			if !errors.Is(err, determinism.ErrUnknownCapability) {
				t.Fatalf("Admissible() on %q = %v, want one wrapping ErrUnknownCapability",
					testCase.capability, err)
			}

			if !errors.Is(err, determinism.ErrInadmissibleModule) {
				t.Error("the refusal does not satisfy ErrInadmissibleModule")
			}
		})
	}
}

// TestEveryCapabilityIsClassified is the closure over the constants.
//
// Every `Capability` this package declares is either on the data-level list or one
// of the two named refusals — and nothing else. Without this, a new capability
// added as a constant and forgotten in the switch would be refused by
// `Admissible` (correctly, but for the wrong reason: as *unrecognised*, which is a
// message that sends a plugin author looking for a typo rather than for the rule
// they broke).
func TestEveryCapabilityIsClassified(t *testing.T) {
	t.Parallel()

	declared := []determinism.Capability{
		determinism.CapToggle,
		determinism.CapConstant,
		determinism.CapDisable,
		determinism.CapReorder,
		determinism.CapUnseededRandomness,
	}

	dataLevel := determinism.DataLevel()

	if len(dataLevel) == 0 {
		t.Fatal("DataLevel() is empty, so nothing is admissible and this test is vacuous")
	}

	for _, capability := range declared {
		admissible := determinism.Scope{capability}.Admissible()

		if slices.Contains(dataLevel, capability) {
			if admissible != nil {
				t.Errorf("%q is on DataLevel() and is still refused: %v", capability, admissible)
			}

			continue
		}

		// Refused, and *specifically* refused rather than as an unknown capability.
		if errors.Is(admissible, determinism.ErrUnknownCapability) {
			t.Errorf("%q is a named refusal but Admissible() reports it as "+
				"unrecognised, so the message sends the reader looking for a typo", capability)
		}

		if !errors.Is(admissible, determinism.ErrInadmissibleModule) {
			t.Errorf("%q is not data-level, yet Admissible() = %v", capability, admissible)
		}
	}

	// And the list is not longer than the constants: a data-level entry with no
	// constant is a capability nobody can declare, and one that cannot be written
	// down cannot be audited.
	for _, capability := range dataLevel {
		if !slices.Contains(declared, capability) {
			t.Errorf("DataLevel() names %q, which has no Capability constant", capability)
		}
	}
}

// TestTheRefusalNamesTheCapability is about the message, which is the part a plugin
// author acts on.
//
// "a module is inadmissible" is not something anybody can fix. The capability's own
// spelling in the error is the difference between a message and a shrug, and it is
// the same argument `rules.OwnershipError` makes about naming the kind and the
// claimant.
func TestTheRefusalNamesTheCapability(t *testing.T) {
	t.Parallel()

	err := determinism.Scope{determinism.CapReorder}.Admissible()
	if err == nil {
		t.Fatal("Admissible() on a reordering module = nil, want a refusal")
	}

	if !strings.Contains(err.Error(), string(determinism.CapReorder)) {
		t.Errorf("the refusal does not name the capability: %s", err)
	}
}

// TestCapabilityStringIsIdentityAndNotAValidityCheck pins the printing rule.
//
// A capability read from a registration prints as it was written, so the operator
// reading the log sees the value that failed rather than a normalised one. That is
// the same contract `rules.ID.String` and `domain.Role.String` have, and the reason
// is the same: a folded spelling in a log is a value nobody can grep for.
func TestCapabilityStringIsIdentityAndNotAValidityCheck(t *testing.T) {
	t.Parallel()

	cases := []struct {
		capability determinism.Capability
		want       string
	}{
		{capability: determinism.CapToggle, want: "toggle"},
		{capability: determinism.CapReorder, want: "reorder"},
		{capability: determinism.CapUnseededRandomness, want: "unseeded-randomness"},
		{capability: determinism.Capability("Toggle"), want: "Toggle"},
		{capability: determinism.Capability(""), want: ""},
	}

	for _, testCase := range cases {
		if got := testCase.capability.String(); got != testCase.want {
			t.Errorf("Capability(%q).String() = %q, want %q", testCase.want, got, testCase.want)
		}
	}
}

// TestScopeAdmissibleIsDeterministic is the same discipline this package exists to
// enforce, applied to itself.
//
// `Admissible` reads a slice and runs one comparison per element, so it is
// deterministic by construction — but "by construction" is a claim, and the claim
// is checkable: two hundred calls over the same scope produce the same answer, and
// the answer is a pure function of the declaration. There is no clock, no map and
// no ambient source in it, and this is the test that would notice if a later edit
// added one.
func TestScopeAdmissibleIsDeterministic(t *testing.T) {
	t.Parallel()

	const runs = 200

	admissible := determinism.Scope{
		determinism.CapConstant,
		determinism.CapToggle,
		determinism.CapDisable,
	}

	refused := determinism.Scope{
		determinism.CapDisable,
		determinism.CapReorder,
	}

	for range runs {
		if err := admissible.Admissible(); err != nil {
			t.Fatalf("Admissible() on %v = %v, want nil", admissible, err)
		}

		if err := refused.Admissible(); err == nil {
			t.Fatalf("Admissible() on %v = nil, want a refusal", refused)
		}
	}
}
