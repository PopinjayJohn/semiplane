package houserules_test

import (
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
	"github.com/semiplane/semiplane/internal/realtime"
)

// TestTogglingEveryModuleLeavesTheRulesetVersionUnchanged is ADR 0018's exclusion,
// asserted against **this package's own output** rather than against a descriptor
// somebody filled in by hand.
//
// ADR 0018 exists because §10.5's chain reads literally as
// "system ID → base pack → overlay → enabled house-rule modules (declared order) →
// effective ruleset (+ ruleset_version)", and read literally a GM enabling a house
// rule at the table changes the version that gates resuming their own game. The fix
// was to exclude the enabled house-rule set from `realtime.Fingerprint` — and
// `realtime`'s own `TestTheFingerprintExcludesHouseRules` already asserts that
// exclusion over a hand-written descriptor.
//
// This test is the other half and it is the half that would catch the mistake this
// package could actually make. `realtime` can only assert that a descriptor's
// `HouseRules` field is not read. It cannot assert anything about *where the field's
// contents come from*, and the contents come from here: a composition root reads the
// rows, applies them here, and fills the descriptor with the result. So this test runs
// the real application for five module sets — none, all enabled, all disabled,
// mixed, and reordered — requires every one of them to resolve, and requires the
// `ruleset_version` computed from each to be **byte-identical** to the one computed
// from no house rules at all.
//
// Byte-identity rather than `Equal`, for the reason `realtime`'s own test gives: an
// exclusion asserted by comparing fields would also be satisfied by a fingerprint that
// hashed the enabled set into a component nobody compares. The encoded bytes are what
// `campaigns.ruleset_version` holds.
//
// **A control is in here too**, and it is the part that makes the rest mean anything:
// a fingerprint that never changes satisfies every assertion above. So the test also
// requires a change to the *overlay pack version* — an input the fingerprint really
// does name — to move the string. Without that, "toggling every module changes no
// `ruleset_version`" would be indistinguishable from "this package has no effect on
// anything".
func TestTogglingEveryModuleLeavesTheRulesetVersionUnchanged(t *testing.T) {
	t.Parallel()

	registry := registryOf(t,
		declaring("flanking-optional", changeOfToggle("flanking_optional", true)),
		declaring("averaging", changeOfToggle("flanking_optional", false)),
		declaring("generous-dcs", changeOfConstant("dc_passive_exploration", 14)),
		declaring("no-prone", changeOf(determinism.CapDisable, "prone")),
	)

	cases := []struct {
		name  string
		given []determinism.Module
		// wantApplied and wantConflicts are the non-vacuity requirement: this
		// package's output has to have actually *changed* for "and the version
		// did not" to be a statement about anything.
		wantApplied   int
		wantConflicts int
	}{
		{
			name:        "no house rules at all",
			given:       nil,
			wantApplied: 0,
		},
		{
			name: "every module enabled, two of which conflict",
			given: []determinism.Module{
				enabled(module("flanking-optional", 0)),
				enabled(module("averaging", 1)),
				enabled(module("generous-dcs", 2)),
				enabled(module("no-prone", 3)),
			},
			wantApplied:   3,
			wantConflicts: 1,
		},
		{
			name: "every module switched off",
			given: []determinism.Module{
				disabled(module("flanking-optional", 0)),
				disabled(module("averaging", 1)),
				disabled(module("generous-dcs", 2)),
				disabled(module("no-prone", 3)),
			},
		},
		{
			name: "half enabled, and the winner of the conflict switched off",
			given: []determinism.Module{
				disabled(module("flanking-optional", 0)),
				enabled(module("averaging", 1)),
				enabled(module("generous-dcs", 2)),
			},
			wantApplied: 2,
		},
		{
			name: "every module enabled with its positions reversed",
			given: []determinism.Module{
				enabled(module("no-prone", 0)),
				enabled(module("generous-dcs", 1)),
				enabled(module("averaging", 2)),
				enabled(module("flanking-optional", 3)),
			},
			// The same three settings and the same one conflict: reversing the
			// positions changes which module wins `flanking_optional` and nothing
			// else, which is exactly the reordering ADR 0018 says must be safe.
			wantApplied:   3,
			wantConflicts: 1,
		},
	}

	baseline := fingerprint(t, nil)

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			effective, err := registry.Apply(t.Context(), testCase.given, nil)
			if err != nil {
				t.Fatalf("Apply(%v) = %v, want nil; a set this test claims is legal is not",
					idsOf(testCase.given), err)
			}

			if got := len(effective.Applied()); got != testCase.wantApplied {
				t.Errorf("Apply(%v) applied %d settings, want %d: %s",
					idsOf(testCase.given), got, testCase.wantApplied, rendered(effective))
			}

			if got := len(effective.Conflicts()); got != testCase.wantConflicts {
				t.Errorf("Apply(%v) recorded %d conflicts, want %d: %s",
					idsOf(testCase.given), got, testCase.wantConflicts,
					renderedConflicts(effective.Conflicts()))
			}

			got := fingerprint(t, testCase.given)

			if got != baseline {
				t.Errorf(
					"the ruleset_version with %v is\n%q\nand without any house rules it is\n%q\n"+
						"ADR 0018 requires the two to be byte-identical: toggling a house rule must not "+
						"strand a GM on their own campaign",
					idsOf(testCase.given),
					got,
					baseline,
				)
			}
		})
	}
}

// TestAPackRevisionStillMovesTheRulesetVersion is the control the test above needs,
// kept as its own function so a failure names which half moved.
//
// A fingerprint that never changed would satisfy every exclusion assertion this
// repository has, including `realtime`'s. So the counter-example is asserted here:
// `OverlayPack` is one of the four components the fingerprint really does name
// (ADR 0018's first sentence), and changing it moves the encoded string — with the
// house rules held identical, so nothing else can be the cause.
//
// If this fails, every exclusion test above is passing for the wrong reason and this
// is the one that says so.
func TestAPackRevisionStillMovesTheRulesetVersion(t *testing.T) {
	t.Parallel()

	house := []determinism.Module{
		enabled(module("flanking-optional", 0)),
		enabled(module("generous-dcs", 1)),
	}

	before := fingerprint(t, house)

	after := fingerprintOf(t, realtime.Descriptor{
		System:      descriptorSystem,
		Ruleset:     descriptorRuleset,
		BasePack:    descriptorBasePack,
		OverlayPack: "2024-1.0.1",
		HouseRules:  houseRulesOf(house),
	})

	if after == before {
		t.Errorf("the ruleset_version is %q both before and after an overlay pack revision, so a "+
			"fingerprint that never moves is satisfying every exclusion test in this repository, "+
			"including this package's", after)
	}

	// And the reason it moves is the one the record names, not a coincidence.
	if difference := mustParse(
		t,
		before,
	).Diff(mustParse(t, after)); difference != realtime.ComponentOverlayPack {
		t.Errorf("the component that moved is %q, want %q: a pack revision is the case where "+
			"persisted mutations genuinely might not replay, and it is the one input the "+
			"fingerprint exists to name", difference, realtime.ComponentOverlayPack)
	}
}

// The descriptor every fingerprint in this file is built over: the four inputs a
// campaign resolved to, none of them a house rule.
const (
	descriptorSystem   = "5e"
	descriptorRuleset  = "5e-2024"
	descriptorBasePack = "core-1.0.0"
	descriptorOverlay  = "2024-1.0.0"
)

// fingerprint returns the encoded ruleset_version for a campaign with these house
// rules, over the fixed descriptor above.
func fingerprint(t *testing.T, house []determinism.Module) string {
	t.Helper()

	return fingerprintOf(t, realtime.Descriptor{
		System:      descriptorSystem,
		Ruleset:     descriptorRuleset,
		BasePack:    descriptorBasePack,
		OverlayPack: descriptorOverlay,
		HouseRules:  houseRulesOf(house),
	})
}

// fingerprintOf returns the encoded ruleset_version a descriptor resolves to.
//
// The seam a composition root uses: `realtime.FingerprintOf` is the only encoder, and
// `Fingerprint.String` is the bytes `campaigns.ruleset_version` holds. Nothing here
// reimplements the format — a test that built the string itself would be asserting
// against its own spelling rather than against the one a resume parses back.
func fingerprintOf(t *testing.T, descriptor realtime.Descriptor) string {
	t.Helper()

	resolved, err := realtime.FingerprintOf(descriptor)
	if err != nil {
		t.Fatalf("FingerprintOf(%+v) = %v, want nil: this fixture names a descriptor a "+
			"composition root could build, and a refusal here is a wiring fault rather than a "+
			"finding", descriptor, err)
	}

	return resolved.String()
}

// mustParse parses an encoded fingerprint or fails the test.
func mustParse(t *testing.T, encoded string) realtime.Fingerprint {
	t.Helper()

	parsed, err := realtime.ParseFingerprint(encoded)
	if err != nil {
		t.Fatalf("ParseFingerprint(%q) = %v, want nil", encoded, err)
	}

	return parsed
}

// houseRulesOf maps stored rows to the descriptor's shape.
//
// **The mapping is the whole of what this test shares with `realtime`**, and it is
// three fields. `conformance.HouseRule` documents why the shape is declared twice
// rather than shared: "`domain` imports nothing from the project", and `realtime` owns
// the database. What is shared is a shape, and a shape two packages agree on is not the
// second answer to a question the first one answers.
func houseRulesOf(modules []determinism.Module) []realtime.HouseRule {
	house := make([]realtime.HouseRule, 0, len(modules))

	for _, module := range modules {
		house = append(house, realtime.HouseRule{
			ModuleID: module.ModuleID.String(),
			Position: module.Position,
			Enabled:  module.Enabled,
		})
	}

	return house
}
