package houserules_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
	"github.com/semiplane/semiplane/internal/domain/rules/houserules"
)

// TestAConflictResolvesToTheEarlierModuleAndNotTheLaterOne is the test that
// distinguishes first-match-wins from last-write-wins, and it is deliberately built
// around the case the two policies answer **differently**.
//
// §10.5: "Modules carry a `position`; conflicts resolve first-match-wins and are
// logged, never last-write-wins." Two modules, both toggling `flanking_optional`,
// the earlier one setting it true and the later one setting it false. First-match-wins
// answers true; last-write-wins answers false. So this test reads the *resolved
// value*, not the conflict list and not the winner's name — an assertion that only
// required "a conflict was recorded" or "the first module was logged" would pass
// under both policies and prove nothing about which one this package implements.
//
// The assertion that fails if `Apply` is last-write-wins is therefore the value, and
// the one that fails if the log line lost the shadowed module's id is
// `TestTheConflictLineNamesBothModulesAndCarriesNoConfiguration`.
func TestAConflictResolvesToTheEarlierModuleAndNotTheLaterOne(t *testing.T) {
	t.Parallel()

	registry := registryOf(t,
		declaring("flanking-optional", changeOfToggle("flanking_optional", true)),
		declaring("averaging", changeOfToggle("flanking_optional", false)),
	)

	effective, err := registry.Apply(t.Context(), []determinism.Module{
		enabled(module("flanking-optional", 0)),
		enabled(module("averaging", 1)),
	}, nil)
	if err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}

	applied, found := effective.Lookup(determinism.CapToggle, "flanking_optional")
	if !found {
		t.Fatalf(
			"Lookup(CapToggle, \"flanking_optional\") reported nothing: %s",
			rendered(effective),
		)
	}

	if applied.Toggle == nil {
		t.Fatalf("the resolved setting carries no value: %+v", applied)
	}

	if !*applied.Toggle {
		t.Errorf("the resolved setting is %v, want true: the module at position 0 declared it "+
			"first and first-match-wins keeps the first declaration. A false here is "+
			"last-write-wins, which is what S-10.5 forbids by name", *applied.Toggle)
	}

	if applied.Winner.ID != "flanking-optional" || applied.Winner.Position != 0 {
		t.Errorf("the resolved setting names %q at position %d as its winner, want "+
			"%q at position 0", applied.Winner.ID, applied.Winner.Position, rules.ID("flanking-optional"))
	}

	conflicts := effective.Conflicts()
	if len(conflicts) != 1 {
		t.Fatalf("Conflicts() = %s, want exactly one: two modules declared one setting",
			renderedConflicts(conflicts))
	}

	conflict := conflicts[0]

	if conflict.Winner.ID != "flanking-optional" || conflict.Shadowed.ID != "averaging" {
		t.Errorf("the conflict names %q as the winner and %q as shadowed, want %q and %q",
			conflict.Winner.ID, conflict.Shadowed.ID,
			rules.ID("flanking-optional"), rules.ID("averaging"))
	}

	if conflict.Setting() != "toggle flanking_optional" {
		t.Errorf("the conflict names the setting %q, want %q",
			conflict.Setting(), "toggle flanking_optional")
	}
}

// TestAConflictIsOneLinePerShadowedModule is what "logged with both module IDs"
// means when three modules declare one setting.
//
// A setting three modules declared is **two** conflicts, not one conflict with two
// losers. The reason is the same in both directions: a record that names a winner and
// a list of losers cannot be read as the pair it is, and the second and third
// modules genuinely were in conflict with the winner one at a time — the second lost to
// the first, and the third lost to the first, and no arrangement makes the third lose
// to the second.
func TestAConflictIsOneLinePerShadowedModule(t *testing.T) {
	t.Parallel()

	registry := registryOf(t,
		declaring("first", changeOfToggle("flanking_optional", true)),
		declaring("second", changeOfToggle("flanking_optional", false)),
		declaring("third", changeOfToggle("flanking_optional", false)),
	)

	effective, err := registry.Apply(t.Context(), []determinism.Module{
		enabled(module("first", 0)),
		enabled(module("second", 1)),
		enabled(module("third", 2)),
	}, nil)
	if err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}

	conflicts := effective.Conflicts()
	if len(conflicts) != 2 {
		t.Fatalf("Conflicts() = %s, want two: three modules declaring one setting is two pairs, "+
			"not one pair with a list", renderedConflicts(conflicts))
	}

	for index, want := range []rules.ID{"second", "third"} {
		if conflicts[index].Winner.ID != "first" {
			t.Errorf("conflict %d names %q as the winner, want %q; the winner is the module that "+
				"got there first, not the one immediately before", index, conflicts[index].Winner.ID,
				rules.ID("first"))
		}

		if conflicts[index].Shadowed.ID != want {
			t.Errorf("conflict %d names %q as shadowed, want %q", index,
				conflicts[index].Shadowed.ID, want)
		}

		if conflicts[index].Shadowed.Position != index+1 {
			t.Errorf("conflict %d names position %d for the shadowed module, want %d",
				index, conflicts[index].Shadowed.Position, index+1)
		}
	}
}

// TestATieOnPositionIsBrokenByModuleIDAndNotByArrival is the second half of the total
// order, and it is why both keys matter.
//
// Two modules at the same `position`, which nothing in the schema prevents and a GM
// editing a list in a form has no reason to notice. Over an order that is not total,
// "first" has no referent: the winner would be whichever row arrived first, which is
// last-write-wins arriving by a different route. So this feeds the pair **in both
// arrival orders** and requires the same winner and the same value from each — the
// arrival order is the only input the two differ in, so a tie-break on arrival fails
// here and a tie-break on `module_id` passes.
func TestATieOnPositionIsBrokenByModuleIDAndNotByArrival(t *testing.T) {
	t.Parallel()

	// `alpha-rule` sets the toggle true and `zebra-rule` sets it false, and `alpha-rule`
	// is the lower id — so first-match-wins over `(position, module_id)` answers
	// **true**. An arrival-order tie-break answers whichever was written first in the
	// slice, which the second arrangement flips.
	registry := registryOf(t,
		declaring("alpha-rule", changeOfToggle("flanking_optional", true)),
		declaring("zebra-rule", changeOfToggle("flanking_optional", false)),
	)

	cases := []struct {
		name    string
		given   []determinism.Module
		wantIDs []rules.ID
	}{
		{
			name: "the lower id arrives first",
			given: []determinism.Module{
				enabled(module("alpha-rule", 0)),
				enabled(module("zebra-rule", 0)),
			},
			wantIDs: []rules.ID{"alpha-rule", "zebra-rule"},
		},
		{
			name: "the higher id arrives first",
			given: []determinism.Module{
				enabled(module("zebra-rule", 0)),
				enabled(module("alpha-rule", 0)),
			},
			wantIDs: []rules.ID{"alpha-rule", "zebra-rule"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			effective, err := registry.Apply(t.Context(), testCase.given, nil)
			if err != nil {
				t.Fatalf("Apply() = %v, want nil", err)
			}

			applied, found := effective.Lookup(determinism.CapToggle, "flanking_optional")
			if !found {
				t.Fatalf("Lookup(CapToggle, \"flanking_optional\") reported nothing: %s",
					rendered(effective))
			}

			if applied.Toggle == nil || !*applied.Toggle {
				t.Errorf(
					"the resolved setting is %v, want true: both modules sit at position 0, so "+
						"the winner is decided by module id and `alpha-rule` is the lower one",
					value(applied.Change),
				)
			}

			if applied.Winner.ID != testCase.wantIDs[0] {
				t.Errorf("the winner is %q, want %q", applied.Winner.ID, testCase.wantIDs[0])
			}

			if applied.Winner.Position != 0 {
				t.Errorf(
					"the winner's position is %d, want 0: the tie is on position, which is why "+
						"the id had to decide it",
					applied.Winner.Position,
				)
			}

			conflicts := effective.Conflicts()
			if len(conflicts) != 1 || conflicts[0].Shadowed.ID != testCase.wantIDs[1] {
				t.Errorf("Conflicts() = %s, want exactly one naming %q as shadowed",
					renderedConflicts(conflicts), testCase.wantIDs[1])
			}
		})
	}
}

// TestTheApplicationOrderIsTheOneDeterminismStates is requirement "no third order",
// as a property over **every** arrangement rather than over one input.
//
// `store.RuleModulesForCampaign` orders the rows in SQL — `ORDER BY position,
// module_id` — and `determinism.Order` states the same rule in Go; P1d holds those two
// together, structurally and behaviourally. This package therefore calls
// `determinism.Order` and sorts nothing, so the order a campaign's settings resolve in
// is the order the domain states whatever the caller's slice happened to look like.
//
// One arrangement proving that would be one lucky input. The set below is built to
// expose a third order specifically: three enabled modules, **two of them sharing a
// position** and all three declaring the same setting, so an implementation that sorted
// by module id alone, by position alone, by arrival order, or by map iteration each
// produce a different winner or a different conflict list. The permutations are
// enumerated rather than sampled, so a run is a proof over the whole set.
func TestTheApplicationOrderIsTheOneDeterminismStates(t *testing.T) {
	t.Parallel()

	given := []determinism.Module{
		enabled(module("averaging", 0)),
		enabled(module("charmed", 0)),
		enabled(module("flanking-optional", 1)),
	}

	registry := registryOf(t,
		declaring("averaging", changeOfToggle("flanking_optional", true)),
		declaring("charmed", changeOfToggle("flanking_optional", false)),
		declaring("flanking-optional", changeOfToggle("flanking_optional", false)),
	)

	want, err := registry.Apply(t.Context(), given, nil)
	if err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}

	// The answer has to be the one `determinism.Order` states, or "the same over every
	// arrangement" would be satisfied by any order at all.
	ordered := idsOf(determinism.Order(given))
	if !slices.Equal(ordered, []rules.ID{"averaging", "charmed", "flanking-optional"}) {
		t.Fatalf(
			"the fixture does not exercise the order it claims to: determinism.Order(%v) = %v, "+
				"want averaging, charmed, flanking-optional",
			idsOf(given),
			ordered,
		)
	}

	if wantConflicts := len(want.Conflicts()); wantConflicts != 2 {
		t.Fatalf(
			"the fixture produced %d conflicts, want two: three modules declaring one setting "+
				"is two pairs",
			wantConflicts,
		)
	}

	for _, permutation := range permutations(given) {
		got, err := registry.Apply(t.Context(), permutation, nil)
		if err != nil {
			t.Fatalf("Apply(%v) = %v, want nil", idsOf(permutation), err)
		}

		if rendered(got) != rendered(want) {
			t.Fatalf("the arrangement %v resolved to\n%s\nwant\n%s",
				idsOf(permutation), rendered(got), rendered(want))
		}
	}
}

// TestASettingIsIdentifiedByItsKindAndItsName is why a conflict is a conflict.
//
// Two modules declaring the same *name* under different kinds are not in conflict: a
// condition called `prone` and a toggle called `prone` are different settings, and
// folding them into one namespace would let a house rule disable a condition by
// accident naming a toggle that shares its slug.
func TestASettingIsIdentifiedByItsKindAndItsName(t *testing.T) {
	t.Parallel()

	registry := registryOf(t,
		declaring("disables-prone", changeOf(determinism.CapDisable, "prone")),
		declaring("toggles-prone", changeOfToggle("prone", true)),
	)

	effective, err := registry.Apply(t.Context(), []determinism.Module{
		enabled(module("disables-prone", 0)),
		enabled(module("toggles-prone", 1)),
	}, nil)
	if err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}

	if conflicts := effective.Conflicts(); len(conflicts) != 0 {
		t.Errorf("Conflicts() = %s, want none: a disabled condition and a toggle of the same "+
			"slug are two settings", renderedConflicts(conflicts))
	}

	if _, found := effective.Lookup(determinism.CapDisable, "prone"); !found {
		t.Errorf("Lookup(CapDisable, \"prone\") reported nothing: %s", rendered(effective))
	}

	if _, found := effective.Lookup(determinism.CapToggle, "prone"); !found {
		t.Errorf("Lookup(CapToggle, \"prone\") reported nothing: %s", rendered(effective))
	}
}

// TestADisabledModuleContributesNothingAndIsNeverShadowed is what `Enabled` is for.
//
// `determinism.Module` carries `Enabled` so a module can be off without being deleted,
// and "this campaign tried this and turned it off" stays a fact the table can answer.
// The consequence for this package is that a disabled module is not merely absent: it
// must not appear as the *winner* of a setting, and it must not appear as a shadowed
// module in a conflict either — a module that declared no view on a setting has not
// conflicted with anything, and putting its id in a log line for a decision it had no
// part in is a line that misleads whoever reads it.
func TestADisabledModuleContributesNothingAndIsNeverShadowed(t *testing.T) {
	t.Parallel()

	registry := registryOf(t,
		declaring("off-rule", changeOfToggle("flanking_optional", false)),
		declaring("on-rule", changeOfToggle("flanking_optional", true)),
	)

	effective, err := registry.Apply(t.Context(), []determinism.Module{
		disabled(module("off-rule", 0)),
		enabled(module("on-rule", 1)),
	}, nil)
	if err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}

	applied, found := effective.Lookup(determinism.CapToggle, "flanking_optional")
	if !found {
		t.Fatalf(
			"Lookup(CapToggle, \"flanking_optional\") reported nothing: %s",
			rendered(effective),
		)
	}

	if applied.Winner.ID != "on-rule" {
		t.Errorf("the setting's winner is %q, want %q: the module at position 0 is switched off",
			applied.Winner.ID, rules.ID("on-rule"))
	}

	if applied.Toggle == nil || !*applied.Toggle {
		t.Errorf("the resolved value is %v, want true from %q", value(applied.Change),
			rules.ID("on-rule"))
	}

	if conflicts := effective.Conflicts(); len(conflicts) != 0 {
		t.Errorf("Conflicts() = %s, want none: a disabled module declared no view on the setting",
			renderedConflicts(conflicts))
	}

	// And the whole set off, which is what `core` and a fresh registration have.
	off, err := registry.Apply(t.Context(), []determinism.Module{
		disabled(module("off-rule", 0)),
		disabled(module("on-rule", 1)),
	}, nil)
	if err != nil {
		t.Fatalf("Apply(all disabled) = %v, want nil", err)
	}

	if !off.Empty() {
		t.Errorf("Apply(all disabled) = %s, want an empty layer", rendered(off))
	}
}

// TestAnEmptyModuleSetResolvesToAnEmptyLayer is the ordinary case, and it is here
// because a layer that reported something for "no house rules" would make every
// caller carry a length check — and because a campaign with no house rules is what
// `core` and a fresh registration both have, so it is the case that runs most.
func TestAnEmptyModuleSetResolvesToAnEmptyLayer(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		given []determinism.Module
	}{
		{name: "nil", given: nil},
		{name: "an empty slice", given: []determinism.Module{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			registry := registryOf(t, declaring("unused"))

			effective, err := registry.Apply(t.Context(), testCase.given, nil)
			if err != nil {
				t.Fatalf("Apply() = %v, want nil", err)
			}

			if !effective.Empty() {
				t.Errorf("Apply() = %s, want an empty layer", rendered(effective))
			}

			if got := effective.Applied(); len(got) != 0 {
				t.Errorf("Applied() = %v, want none", got)
			}

			if got := effective.Conflicts(); len(got) != 0 {
				t.Errorf("Conflicts() = %v, want none", got)
			}

			if _, found := effective.Lookup(determinism.CapToggle, "flanking_optional"); found {
				t.Error("Lookup() found a setting in an empty layer")
			}
		})
	}
}

// TestAnEnabledRowNamingAModuleThisBuildDoesNotHaveRefusesTheApplication is S-10.6's
// shape, and the direction of the refusal is the whole of it.
//
// §10.6: a campaign whose `system_id` resolves to nothing still serves its wiki, and
// only the game refuses to start, naming the id it wanted. The same applies to a
// house-rule row: this build has no such module.
//
// **The whole application is refused, not the row skipped.** A skipped row is a
// campaign playing by different rules than its GM configured, with nothing in the log
// saying so — and that is the failure this repository cares most about, because it is
// silent and coherent: every client shows a working game and every rule in it is
// wrong. So a module set this build cannot honour is a load failure that names the
// module, and the fixture asserts the earlier module's contribution is *not* returned
// alongside the error, because half an effective ruleset is not an answer.
func TestAnEnabledRowNamingAModuleThisBuildDoesNotHaveRefusesTheApplication(t *testing.T) {
	t.Parallel()

	registry := registryOf(t, declaring("known", changeOfToggle("flanking_optional", true)))

	cases := []struct {
		name  string
		given []determinism.Module
	}{
		{
			name:  "the only module",
			given: []determinism.Module{enabled(module("no-such-module", 0))},
		},
		{
			name: "one module among several that this build does have",
			given: []determinism.Module{
				enabled(module("known", 0)),
				enabled(module("no-such-module", 1)),
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			effective, err := registry.Apply(t.Context(), testCase.given, nil)

			if !errors.Is(err, houserules.ErrUnknownModule) {
				t.Fatalf("Apply() = (%s, %v), want an error satisfying errors.Is(_, "+
					"houserules.ErrUnknownModule)", rendered(effective), err)
			}

			if !strings.Contains(err.Error(), "no-such-module") {
				t.Errorf("Apply() = %v, want the refusal to name the module it could not resolve, "+
					"because S-10.6 requires the refusal to name the id it wanted", err)
			}

			if !effective.Empty() {
				t.Errorf("Apply() returned %s alongside a refusal; a load that cannot honour the "+
					"whole module set must not return the half it could", rendered(effective))
			}
		})
	}
}

// TestANilRegistryIsRefused is the one refusal that is not about a campaign's data.
//
// A composition root that wired nothing would otherwise panic on the first lookup, and
// a panic at campaign load is a worse first impression than a refusal naming the seam.
// It is checked first in `Apply` and under its own sentinel, so the message a reader
// gets is "a definition is not usable: no registry to resolve modules against" rather
// than a refusal about the campaign's module set, which would send them looking at a
// row that is fine.
func TestANilRegistryIsRefused(t *testing.T) {
	t.Parallel()

	var registry *houserules.Registry

	if err := registry.Register(
		declaring("anything"),
	); !errors.Is(
		err,
		houserules.ErrIncompleteDefinition,
	) {
		t.Errorf("Register() on a nil registry = %v, want an error satisfying errors.Is(_, "+
			"houserules.ErrIncompleteDefinition): the composition root that forgot to call "+
			"NewRegistry should hear about it here rather than at a campaign load", err)
	}

	effective, err := registry.Apply(t.Context(),
		[]determinism.Module{enabled(module("anything", 0))}, nil)

	if !errors.Is(err, houserules.ErrIncompleteDefinition) {
		t.Fatalf("Apply() on a nil registry = (%s, %v), want an error satisfying errors.Is(_, "+
			"houserules.ErrIncompleteDefinition)", rendered(effective), err)
	}

	if errors.Is(err, houserules.ErrUnknownModule) {
		t.Error("the nil-registry refusal also satisfies errors.Is(_, ErrUnknownModule), so a " +
			"caller reading it would go looking at the campaign's rows rather than at the wiring")
	}

	if !effective.Empty() {
		t.Errorf("Apply() returned %s alongside a refusal", rendered(effective))
	}
}

// TestADisabledRowNamingAModuleThisBuildDoesNotHaveIsIgnored is the other half of the
// case above, and it is a different decision rather than an inconsistency.
//
// `Enabled` exists so a module can be off without being deleted. A row switched off is
// a fact about the past — "this campaign tried this and turned it off" — and it is the
// whole point of keeping the row that a future build *can* resolve it. Refusing a
// campaign load for a module that is off would mean no campaign could ever have a
// module switched off that a later release removed, which is a backwards-incompatible
// change to an unrelated feature.
func TestADisabledRowNamingAModuleThisBuildDoesNotHaveIsIgnored(t *testing.T) {
	t.Parallel()

	registry := houserules.NewRegistry()

	effective, err := registry.Apply(t.Context(), []determinism.Module{
		disabled(module("removed-in-this-release", 0)),
	}, nil)
	if err != nil {
		t.Fatalf("Apply() = %v, want nil: a row switched off is ignored, so a module a later "+
			"release removes cannot strand the campaigns that had it off", err)
	}

	if !effective.Empty() {
		t.Errorf("Apply() = %s, want an empty layer", rendered(effective))
	}
}

// TestAModuleThatCannotReadItsConfigurationRefusesTheApplication is the fail-closed
// direction, and it is why `Apply`'s errors are not "skip and carry on".
//
// A module whose `Apply` failed has a configuration the campaign load cannot honour.
// Returning the rest of the set would resolve the campaign under a partial reading of
// what its GM configured, and the symptom would be a rule that quietly did not apply —
// the same silent-and-coherent failure `ErrUnknownModule` names. So the error travels
// and the layer does not.
func TestAModuleThatCannotReadItsConfigurationRefusesTheApplication(t *testing.T) {
	t.Parallel()

	broken := errors.New("no `value` key")
	registry := registryOf(t,
		declaring("known", changeOfToggle("flanking_optional", true)),
		scoped("picky", determinism.Scope{determinism.CapToggle},
			func(json.RawMessage) ([]houserules.Change, error) { return nil, broken }),
	)

	given := []determinism.Module{
		enabled(module("known", 0)),
		enabled(module("picky", 1)),
	}

	effective, err := registry.Apply(t.Context(), given, nil)

	if !errors.Is(err, broken) {
		t.Fatalf("Apply() = (%s, %v), want it to satisfy errors.Is(_, %v)",
			rendered(effective), err, broken)
	}

	if !effective.Empty() {
		t.Errorf("Apply() returned %s alongside a refusal; a half-applied module set resolves the "+
			"campaign under rules its GM did not choose", rendered(effective))
	}
}

// TestTheStoredConfigurationReachesAModuleVerbatim is P1d's opacity decision, held.
//
// `determinism.Module.Config` is `json.RawMessage` and deliberately unvalidated
// beyond being a JSON object, because *which keys a module understands* belongs to the
// module. This package therefore hands the bytes through untouched and never looks at
// them: a fixture module reads them back and compares them byte for byte, so a
// "harmless" normalisation here — re-marshalling, dropping unknown keys, refusing an
// empty object — fails the test rather than quietly becoming a second implementation
// of a vocabulary this package does not own.
func TestTheStoredConfigurationReachesAModuleVerbatim(t *testing.T) {
	t.Parallel()

	const config = `{ "value" : true, "note" : "a GM wrote this" }`

	var seen json.RawMessage

	registry := registryOf(t, scoped("opaque", determinism.Scope{determinism.CapToggle},
		func(received json.RawMessage) ([]houserules.Change, error) {
			seen = received

			return []houserules.Change{changeOf(determinism.CapToggle, "flanking_optional")}, nil
		}))

	given := enabled(module("opaque", 0))
	given.Config = json.RawMessage(config)

	if _, err := registry.Apply(t.Context(), []determinism.Module{given}, nil); err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}

	if string(seen) != config {
		t.Errorf("the module received %q, want %q verbatim: the configuration's keys are the "+
			"module's vocabulary and this package does not have one", string(seen), config)
	}
}

// TestANilLoggerStillResolvesTheLayer is the nil-tolerance half of the log line.
//
// An application is not a diagnostic: a campaign whose GM has enabled no two modules
// that conflict loads identically whether or not anybody is watching. Making the
// logger mandatory would mean a package with no other reason to know about `slog` had
// one, and would make the common case the one that needs a dependency.
func TestANilLoggerStillResolvesTheLayer(t *testing.T) {
	t.Parallel()

	registry := registryOf(t,
		declaring("first", changeOfToggle("flanking_optional", true)),
		declaring("second", changeOfToggle("flanking_optional", false)),
	)

	effective, err := registry.Apply(t.Context(), []determinism.Module{
		enabled(module("first", 0)),
		enabled(module("second", 1)),
	}, nil)
	if err != nil {
		t.Fatalf("Apply(logger=nil) = %v, want nil: a conflict is a resolution, not a failure", err)
	}

	if len(effective.Conflicts()) != 1 {
		t.Errorf("Conflicts() = %s, want one: a nil logger must not change what resolved",
			renderedConflicts(effective.Conflicts()))
	}
}

// TestTheEffectiveLayerHandsOutCopies is a contract, not a nicety.
//
// `Applied()` and `Conflicts()` return `slices.Clone` of the caller's own value. A
// caller that sorted the returned slice to build its own index would otherwise be
// reordering the layer every other caller reads — and the order *is* the answer, per
// §10.5, so the second reader would get a different ruleset from the same campaign.
func TestTheEffectiveLayerHandsOutCopies(t *testing.T) {
	t.Parallel()

	registry := registryOf(t,
		declaring("first", changeOfToggle("flanking_optional", true)),
		declaring("second", changeOfConstant("dc_passive", 14)),
	)

	effective, err := registry.Apply(t.Context(), []determinism.Module{
		enabled(module("first", 0)),
		enabled(module("second", 1)),
	}, nil)
	if err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}

	before := rendered(effective)

	// **The length is asserted before it is indexed.** Two modules declaring two
	// different settings is the fixture, so a layer that came back empty or short
	// means the fixture did not build — and indexing a short slice panics, which
	// takes the whole test binary down and hides every other test's result. That is
	// how a mutation elsewhere in this package managed to abort the run before this
	// file's other assertions were reached.
	applied := effective.Applied()
	if len(applied) != 2 {
		t.Fatalf("Applied() = %v, want the fixture's two settings: %s", applied, before)
	}

	applied[0], applied[1] = applied[1], applied[0]
	applied[0].Key = "rewritten-by-the-caller"

	conflicts := effective.Conflicts()
	for index := range conflicts {
		conflicts[index].Key = "rewritten-by-the-caller"
	}

	if after := rendered(effective); after != before {
		t.Errorf(
			"reordering and rewriting the slices Applied() and Conflicts() returned changed "+
				"the layer:\n%s\nwant\n%s",
			after,
			before,
		)
	}
}

// registryOf registers definitions and fails the test if any is refused.
//
// A refusal here is a fixture fault rather than a finding, so this reports it against
// the module rather than letting the assertion under test inherit an error that has
// nothing to do with what it is checking.
func registryOf(t *testing.T, definitions ...*houserules.Definition) *houserules.Registry {
	t.Helper()

	registry := houserules.NewRegistry()

	for _, definition := range definitions {
		if err := registry.Register(definition); err != nil {
			t.Fatalf("Register(%q) = %v, want nil: this is a fixture, not the assertion",
				definition.ID, err)
		}
	}

	return registry
}

// declaring returns a definition that declares every data-level capability and
// returns the given changes whatever its configuration says.
//
// `dataLevel()` is cloned rather than aliased, so a fixture cannot reach into
// `determinism`'s table through the scope it was handed.
func declaring(id rules.ID, changes ...houserules.Change) *houserules.Definition {
	return scoped(id, dataLevel(),
		func(json.RawMessage) ([]houserules.Change, error) { return changes, nil })
}

// scoped returns a definition with the given scope and body.
func scoped(
	id rules.ID,
	scope determinism.Scope,
	body func(config json.RawMessage) ([]houserules.Change, error),
) *houserules.Definition {
	return &houserules.Definition{ID: id, Scope: scope, Apply: body}
}

// noChanges is a module that changes nothing, which is legitimate: `Admissible` treats
// a nil and an empty scope as admissible precisely because "a module that changes
// nothing is a module that cannot break anything".
func noChanges(json.RawMessage) ([]houserules.Change, error) { return nil, nil }

// dataLevel returns a clone of the data-level capabilities as a scope.
func dataLevel() determinism.Scope {
	return determinism.Scope(slices.Clone(determinism.DataLevel()))
}

// changeOf returns the one change a capability carries.
//
// **The default arm is the test's forcing function.** It returns a change with no
// payload, which `Change.validate` refuses — so a fourth capability added to
// `determinism.DataLevel` without a shape here fails
// `TestEveryDataLevelCapabilityHasAChangeShape` with a message naming the capability,
// rather than shipping a module that registered and then could not be applied.
func changeOf(capability determinism.Capability, key string) houserules.Change {
	switch capability {
	case determinism.CapToggle:
		return houserules.Change{Kind: capability, Key: key, Toggle: &yes}
	case determinism.CapConstant:
		number := 15.0

		return houserules.Change{Kind: capability, Key: key, Constant: &number}
	case determinism.CapDisable:
		return houserules.Change{Kind: capability, Key: key}
	default:
		return houserules.Change{Kind: capability, Key: key}
	}
}

// changeOfToggle returns a toggle change carrying the given value, which is the
// distinction a first-match-wins test has to be able to make: two modules that toggled
// the same setting to the *same* value would conflict without the value distinguishing
// the winner from the loser.
func changeOfToggle(key string, value bool) houserules.Change {
	return houserules.Change{Kind: determinism.CapToggle, Key: key, Toggle: &value}
}

// changeOfConstant returns a constant change carrying the given number.
func changeOfConstant(key string, number float64) houserules.Change {
	return houserules.Change{Kind: determinism.CapConstant, Key: key, Constant: &number}
}

// enabled returns an enabled row.
func enabled(module determinism.Module) determinism.Module {
	module.Enabled = true

	return module
}

// disabled returns a row switched off without being deleted.
func disabled(module determinism.Module) determinism.Module {
	module.Enabled = false

	return module
}

// module returns a stored module row at the given position.
func module(id rules.ID, position int) determinism.Module {
	return determinism.Module{ModuleID: id, Position: position}
}

// rendered renders an effective layer as one string, so two of them can be compared
// whole — settings *and* conflicts, because a layer that agrees on every value and
// disagrees on which module won is not the same ruleset.
func rendered(effective houserules.Effective) string {
	var out strings.Builder

	for _, applied := range effective.Applied() {
		fmt.Fprintf(
			&out,
			"  %s = %s, by %s at %d\n",
			applied.Name(),
			value(applied.Change),
			applied.Winner.ID,
			applied.Winner.Position,
		)
	}

	out.WriteString(renderedConflicts(effective.Conflicts()))

	return out.String()
}

// renderedConflicts renders conflicts one per line.
func renderedConflicts(conflicts []houserules.Conflict) string {
	var out strings.Builder

	for _, conflict := range conflicts {
		fmt.Fprintf(&out, "  conflict on %s: %s at %d over %s at %d\n",
			conflict.Setting(),
			conflict.Winner.ID, conflict.Winner.Position,
			conflict.Shadowed.ID, conflict.Shadowed.Position,
		)
	}

	return out.String()
}

// value renders a change's value, so a failure message shows which of the two policies
// produced it rather than only that it was wrong.
func value(change houserules.Change) string {
	switch {
	case change.Toggle != nil:
		return strconv.FormatBool(*change.Toggle)
	case change.Constant != nil:
		return strconv.FormatFloat(*change.Constant, 'g', -1, 64)
	default:
		return "disabled"
	}
}

// idsOf renders a slice of modules as their ids, for a failure message.
func idsOf(modules []determinism.Module) []rules.ID {
	ids := make([]rules.ID, 0, len(modules))

	for _, module := range modules {
		ids = append(ids, module.ModuleID)
	}

	return ids
}

// permutations returns every arrangement of a slice.
//
// Written out rather than generated, for the reason `determinism`'s own helper gives:
// there are six of them and a generator is another thing to be wrong.
func permutations(modules []determinism.Module) [][]determinism.Module {
	switch len(modules) {
	case 0:
		return [][]determinism.Module{nil}
	case 1:
		return [][]determinism.Module{{modules[0]}}
	case 2:
		return [][]determinism.Module{
			{modules[0], modules[1]},
			{modules[1], modules[0]},
		}
	case 3:
		return [][]determinism.Module{
			{modules[0], modules[1], modules[2]},
			{modules[0], modules[2], modules[1]},
			{modules[1], modules[0], modules[2]},
			{modules[1], modules[2], modules[0]},
			{modules[2], modules[0], modules[1]},
			{modules[2], modules[1], modules[0]},
		}
	default:
		return nil
	}
}
