package store

import (
	"cmp"
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
)

// TestTheRuleModuleOrderByIsTotal exists because no behavioural test in this
// package can hold it, and that was measured rather than assumed.
//
// With the tie-break removed — `ORDER BY position` alone — every test here still
// passed. The reason is specific and worth recording: `ReplaceRuleModules` inserts
// in `determinism.Order` order, and SQLite answers the `WHERE campaign_id = ?`
// lookup from the primary key index, so a table built through this package's own
// write path comes back in module-id order whichever `ORDER BY` it is asked for.
// Insert the same rows by hand, in reverse id order, and the query still returns
// them in id order. Two spellings of one order, agreeing on every input this
// package can produce, which is the worst possible arrangement: the difference
// only appears for rows written by something else, and only when the planner
// chooses differently.
//
// So the order is asserted where it is written. The keys are read out of the
// compiled statement rather than restated, and then used: the fixture is sorted by
// *those keys* and compared against `determinism.Order`, so the SQL and the
// domain function are two independent implementations held to one answer. Drop the
// second key from the statement and this fails at the first assertion.
func TestTheRuleModuleOrderByIsTotal(t *testing.T) {
	t.Parallel()

	keys := orderByKeys(t, selectRuleModulesForCampaign)

	if !slices.Equal(keys, []string{"position", "module_id"}) {
		t.Fatalf("the SELECT orders by %v, want [position module_id]. Without the second "+
			"key two modules sharing a position have no order between them, and "+
			"first-match-wins over an unordered set has no referent -- the winner "+
			"becomes whichever row the storage engine returned first, which is the "+
			"last-write-wins S-10.5 forbids arriving by a different route", keys)
	}

	// Three modules at one position, which is the arrangement the second key exists
	// for, plus one at a position of its own so the first key is exercised too.
	given := []determinism.Module{
		{ModuleID: "zulu", Position: 1},
		{ModuleID: "alfa", Position: 0},
		{ModuleID: "mike", Position: 0},
		{ModuleID: "bravo", Position: 0},
	}

	want := moduleIDs(determinism.Order(given))

	got := moduleIDs(sortByKeys(given, keys))
	if !slices.Equal(got, want) {
		t.Errorf("sorting by the statement's own keys %v gives %v, and determinism.Order "+
			"gives %v; the SQL and the domain function are two answers to one question",
			keys, got, want)
	}
}

// orderByKeys returns the keys of a SELECT's ORDER BY clause, in sequence.
//
// A scan rather than a parser, and it is enough because the clause is this file's
// own constant: it has no expressions, no `COLLATE`, no direction and no
// parentheses, and a test that stops reading correctly when one of those appears
// would fail loudly rather than pass quietly, because the key list it returns
// would no longer equal `[]string{"position", "module_id"}`.
func orderByKeys(t *testing.T, query string) []string {
	t.Helper()

	_, clause, found := strings.Cut(query, "ORDER BY")
	if !found {
		t.Fatalf("the statement has no ORDER BY clause: %q", query)
	}

	parts := strings.Split(clause, ",")

	keys := make([]string, 0, len(parts))

	for _, part := range parts {
		key, _, _ := strings.Cut(strings.TrimSpace(part), " ")

		keys = append(keys, key)
	}

	return keys
}

// sortByKeys orders modules ascending by the named keys, in sequence.
//
// The independent implementation `TestTheRuleModuleOrderByIsTotal` compares
// against. An unknown key returns 0 rather than panicking, so a key this function
// has not been taught about shows up as a comparison that never separates anything
// — and the test's first assertion is what catches that, before this function is
// asked.
func sortByKeys(modules []determinism.Module, keys []string) []determinism.Module {
	ordered := slices.Clone(modules)

	slices.SortStableFunc(ordered, func(first, second determinism.Module) int {
		for _, key := range keys {
			switch key {
			case "position":
				if result := cmp.Compare(first.Position, second.Position); result != 0 {
					return result
				}
			case "module_id":
				if result := strings.Compare(
					first.ModuleID.String(),
					second.ModuleID.String(),
				); result != 0 {
					return result
				}
			}
		}

		return 0
	})

	return ordered
}

// moduleIDs renders a module set as its ids, in order.
func moduleIDs(modules []determinism.Module) []string {
	ids := make([]string, 0, len(modules))
	for _, module := range modules {
		ids = append(ids, module.ModuleID.String())
	}

	return ids
}
