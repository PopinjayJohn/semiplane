package determinism_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
)

// TestOrderIsTotalEvenWhenPositionsTie is the reason `Order` exists, and the whole
// of §10.5's conflict policy with it.
//
// "Modules carry a `position`; conflicts resolve first-match-wins and are logged,
// never last-write-wins." Two modules sharing a `position` is not something the
// schema prevents — nothing declares positions unique, and a GM editing a list in
// a form has no reason to notice two rows at 0. Over a set whose order is not total,
// "first" has no referent: the winner would be whichever row the storage engine
// returned first, which is exactly the last-write-wins behaviour the sentence
// forbids, arriving by a different route.
//
// The tie-break is `module_id`, which is unique within a campaign by the primary
// key, so it can never tie either.
func TestOrderIsTotalEvenWhenPositionsTie(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		given    []determinism.Module
		wantIDs  []rules.ID
		wantName string
	}{
		{
			name: "positions alone, which is the easy half",
			given: []determinism.Module{
				{ModuleID: "zebra", Position: 2},
				{ModuleID: "alpha", Position: 0},
				{ModuleID: "middle", Position: 1},
			},
			wantIDs:  []rules.ID{"alpha", "middle", "zebra"},
			wantName: "ascending position",
		},
		{
			name: "three modules at position zero, which is the half that needs a rule",
			given: []determinism.Module{
				{ModuleID: "charmed", Position: 0},
				{ModuleID: "flanking-optional", Position: 0},
				{ModuleID: "averaging", Position: 0},
			},
			wantIDs:  []rules.ID{"averaging", "charmed", "flanking-optional"},
			wantName: "a tie on position, broken by module id",
		},
		{
			name: "a tie on the first two of three",
			given: []determinism.Module{
				{ModuleID: "second", Position: 1},
				{ModuleID: "zeta", Position: 0},
				{ModuleID: "alpha", Position: 0},
			},
			wantIDs:  []rules.ID{"alpha", "zeta", "second"},
			wantName: "the tie is broken only among equals",
		},
		{
			name: "negative positions, which are a legitimate way to say first",
			given: []determinism.Module{
				{ModuleID: "middle", Position: 0},
				{ModuleID: "first", Position: -10},
				{ModuleID: "last", Position: 1000},
			},
			wantIDs:  []rules.ID{"first", "middle", "last"},
			wantName: "no clamping, so a GM can put a module first",
		},
		{
			name:     "nothing",
			given:    nil,
			wantIDs:  nil,
			wantName: "an empty set orders to itself",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := determinism.Order(testCase.given)

			var gotIDs []rules.ID
			for _, module := range got {
				gotIDs = append(gotIDs, module.ModuleID)
			}

			if !slices.Equal(gotIDs, testCase.wantIDs) {
				t.Errorf("Order() = %v, want %v: %s", gotIDs, testCase.wantIDs, testCase.wantName)
			}
		})
	}
}

// TestOrderIsATotalOrderOverEveryArrangement is the property, not one instance of
// it.
//
// One arrangement proving total order would be one lucky input. What matters is
// that *every* arrangement of the same set produces the same output — which is what
// "first-match-wins" needs, because the set's order on arrival is not something the
// caller controls: it comes from a database, from a form, from a map somebody
// iterated. The three modules below all share a position, which is the arrangement
// most likely to expose an unstable sort, and the permutations are enumerated
// rather than sampled so a test run is a proof over the whole set.
func TestOrderIsATotalOrderOverEveryArrangement(t *testing.T) {
	t.Parallel()

	given := []determinism.Module{
		{ModuleID: "averaging", Position: 0, Enabled: true},
		{ModuleID: "charmed", Position: 0, Enabled: false},
		{ModuleID: "flanking-optional", Position: 0, Enabled: true},
	}

	want := make([]rules.ID, 0, len(given))
	for _, module := range determinism.Order(given) {
		want = append(want, module.ModuleID)
	}

	for _, permutation := range permutations(given) {
		got := determinism.Order(permutation)

		for index, module := range got {
			if module.ModuleID != want[index] {
				t.Fatalf("the arrangement %v ordered to %v, want %v",
					idsOf(permutation), idsOf(got), want)
			}
		}
	}
}

// TestOrderDoesNotReorderItsArgument is a contract, not a nicety.
//
// A sort that mutates its slice reorders the caller's slice, and the caller here is
// a loader building a campaign's effective ruleset: a reordering sort in the middle
// of that pipeline moves the order the *previous* stage handed over, and the first
// stage's log line stops describing what was applied.
func TestOrderDoesNotReorderItsArgument(t *testing.T) {
	t.Parallel()

	given := []determinism.Module{
		{ModuleID: "zeta", Position: 9},
		{ModuleID: "alpha", Position: 0},
		{ModuleID: "middle", Position: 5},
	}

	before := idsOf(given)

	ordered := determinism.Order(given)
	if len(ordered) != len(given) {
		t.Fatalf("Order() returned %d modules, want %d", len(ordered), len(given))
	}

	if !slices.Equal(idsOf(given), before) {
		t.Errorf("Order() reordered its argument to %v, want %v untouched", idsOf(given), before)
	}

	if ordered[0].ModuleID != "alpha" {
		t.Errorf("Order() returned %v, want the argument sorted in the result", idsOf(ordered))
	}
}

// TestOrderCarriesEveryField proves the order moves whole modules.
//
// `Order` sorts by position and module id; it must not accidentally drop or blank
// the fields that decide what a module *does*. `config` in particular: a module
// whose configuration was emptied by being sorted is a module silently running with
// its defaults, which is a house rule that is on and not doing what it says.
func TestOrderCarriesEveryField(t *testing.T) {
	t.Parallel()

	given := []determinism.Module{
		{
			CampaignID: 7,
			ModuleID:   "charmed",
			Enabled:    false,
			Config:     json.RawMessage(`{"save_dc":15}`),
			Position:   3,
		},
		{
			CampaignID: 7,
			ModuleID:   "averaging",
			Enabled:    true,
			Config:     json.RawMessage(`{}`),
			Position:   1,
		},
	}

	ordered := determinism.Order(given)

	if ordered[0].ModuleID != "averaging" || ordered[1].ModuleID != "charmed" {
		t.Fatalf("Order() = %v, want averaging then charmed", idsOf(ordered))
	}

	if ordered[1].CampaignID != 7 || ordered[1].Enabled || ordered[1].Position != 3 {
		t.Errorf("the second module came back as %+v, want its fields intact", ordered[1])
	}

	if string(ordered[1].Config) != `{"save_dc":15}` {
		t.Errorf("config = %q, want it carried through unchanged", ordered[1].Config)
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
// Written out rather than generated because there are six of them and a generator
// is another thing to be wrong. A duplicate entry would make this lie — it does not
// return the factorial of the length for a slice with repeats — which the caller's
// ids comparison would then catch, so the assumption is stated rather than defended.
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
