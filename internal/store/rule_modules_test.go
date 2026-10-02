package store_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
	"github.com/semiplane/semiplane/internal/store"
)

// TestRuleModuleRoundTrip is the round trip over every column that can be wrong.
//
// A module row is configuration a GM reads back, so a value that did not survive
// the write is a house rule silently running with its defaults — on, and not doing
// what it says, which is the failure §10.5's whole design is trying to avoid.
func TestRuleModuleRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		module determinism.Module
		// wantConfig is what the row should hold when it comes back, where the
		// caller's spelling and the stored one differ. Empty means "the same as
		// was written".
		wantConfig json.RawMessage
	}{
		{
			name: "enabled with configuration",
			module: determinism.Module{
				ModuleID: "flanking-optional",
				Enabled:  true,
				Config:   json.RawMessage(`{"optional":true}`),
				Position: 10,
			},
		},
		{
			name: "disabled, because a toggle turned off is still a row",
			module: determinism.Module{
				ModuleID: "averaging",
				Enabled:  false,
				Config:   json.RawMessage(`{}`),
				Position: 0,
			},
		},
		{
			name: "no configuration, which normalises to an object",
			module: determinism.Module{
				ModuleID: "charmed",
				Enabled:  true,
				Config:   nil,
				Position: -5,
			},
			// The zero value comes back as `{}`, because that is what the column's
			// DEFAULT says "no configuration" is, and a caller comparing two rows
			// should not have to know which spelling "absent" arrived as.
			wantConfig: json.RawMessage(`{}`),
		},
		{
			name: "whitespace in the config, compacted on write",
			module: determinism.Module{
				ModuleID: "resilient",
				Enabled:  true,
				Config:   json.RawMessage("{\n  \"dc\" : 15,\n  \"tag\" : \"x\"\n}"),
				Position: 3,
			},
			// Compacted on the way in, so two modules configured the same way
			// store byte-identical configs and a diff between two databases'
			// rows means something.
			wantConfig: json.RawMessage(`{"dc":15,"tag":"x"}`),
		},
		{
			name: "a negative position, which is how a module is put first",
			module: determinism.Module{
				ModuleID: "crit-on-any",
				Enabled:  true,
				Config:   json.RawMessage(`{}`),
				Position: -1,
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := openTestStore(t)

			campaign := seedCampaign(t, db, "modules-"+slugify(testCase.name))

			want := testCase.module
			want.CampaignID = campaign.ID

			wantStored := want.Config
			if len(testCase.wantConfig) > 0 {
				wantStored = testCase.wantConfig
			}

			if err := db.ReplaceRuleModules(
				t.Context(),
				campaign.ID,
				[]determinism.Module{want},
			); err != nil {
				t.Fatalf("ReplaceRuleModules() error = %v, want nil", err)
			}

			got, err := db.RuleModulesForCampaign(t.Context(), campaign.ID)
			if err != nil {
				t.Fatalf("RuleModulesForCampaign() error = %v, want nil", err)
			}

			if len(got) != 1 {
				t.Fatalf("RuleModulesForCampaign() returned %d modules, want 1: %v", len(got), got)
			}

			if string(got[0].Config) != string(wantStored) {
				t.Errorf("config = %q, want %q", got[0].Config, wantStored)
			}

			want.Config = wantStored

			if !reflect.DeepEqual(got[0], want) {
				t.Errorf("the module came back\n %+v\nwant\n %+v", got[0], want)
			}
		})
	}
}

// TestAnEmptyModuleSetIsNotAnError covers the case a campaign that has never
// enabled a house rule actually hits.
//
// The loop that applies a campaign's effective ruleset reads this, and a caller
// that had to distinguish "none" from "error" would write a length check that
// `core` and every fresh registration never exercises.
func TestAnEmptyModuleSetIsNotAnError(t *testing.T) {
	t.Parallel()

	db := openTestStore(t)

	campaign := seedCampaign(t, db, "no-modules")

	got, err := db.RuleModulesForCampaign(t.Context(), campaign.ID)
	if err != nil {
		t.Fatalf("RuleModulesForCampaign() error = %v, want nil", err)
	}

	if len(got) != 0 {
		t.Fatalf("RuleModulesForCampaign() = %v, want an empty set", got)
	}

	if got == nil {
		t.Error("RuleModulesForCampaign() returned a nil slice; a caller ranging over it " +
			"is fine and a caller comparing it to an empty slice is not, and the second " +
			"caller is the one this is written for")
	}
}

// TestRuleModulesForCampaignIsOrderedByPositionThenModuleID is §10.5's conflict
// policy, as stored.
//
// The three modules below all share a position, which is the arrangement the rule
// exists for: a GM editing a list has no reason to notice two rows at 0, and over
// an order that is not total "first-match-wins" has no referent. The expectation is
// stated in `position, module_id` order rather than in insertion order, because the
// insertion order is the thing under test.
func TestRuleModulesForCampaignIsOrderedByPositionThenModuleID(t *testing.T) {
	db := openTestStore(t)

	campaign := seedCampaign(t, db, "ordering")

	// Deliberately out of order, and deliberately with ties.
	given := []determinism.Module{
		{ModuleID: "zebra", Enabled: true, Config: json.RawMessage(`{}`), Position: 2},
		{ModuleID: "charmed", Enabled: true, Config: json.RawMessage(`{}`), Position: 0},
		{ModuleID: "averaging", Enabled: true, Config: json.RawMessage(`{}`), Position: 0},
		{ModuleID: "flanking-optional", Enabled: true, Config: json.RawMessage(`{}`), Position: 0},
		{ModuleID: "first", Enabled: true, Config: json.RawMessage(`{}`), Position: -10},
	}

	if err := db.ReplaceRuleModules(t.Context(), campaign.ID, given); err != nil {
		t.Fatalf("ReplaceRuleModules() error = %v, want nil", err)
	}

	got, err := db.RuleModulesForCampaign(t.Context(), campaign.ID)
	if err != nil {
		t.Fatalf("RuleModulesForCampaign() error = %v, want nil", err)
	}

	want := []string{"first", "averaging", "charmed", "flanking-optional", "zebra"}

	if ids := store.RuleModuleIDs(got); !slices.Equal(ids, want) {
		t.Fatalf("RuleModulesForCampaign() = %v, want %v", ids, want)
	}
}

// TestRuleModulesForCampaignMatchesTheOrderTheDomainStates is what keeps the SQL and
// the Go from being two answers to "what order do a campaign's modules apply in".
//
// `determinism.Order` is the authority — it is pure, it is in `domain`, and it is
// what a caller that assembles a set in memory uses. The SELECT states the same
// order in SQL because the database is what has the rows. Two spellings of one rule
// is a hazard only if nothing holds them together, and this is what holds them
// together: the same input, read both ways, compared.
//
// The input is one with ties in it, deliberately, because a fixture with distinct
// positions would agree under both orderings even if one of them were broken.
func TestRuleModulesForCampaignMatchesTheOrderTheDomainStates(t *testing.T) {
	db := openTestStore(t)

	campaign := seedCampaign(t, db, "same-order")

	given := []determinism.Module{
		{ModuleID: "delta", Enabled: true, Config: json.RawMessage(`{}`), Position: 1},
		{ModuleID: "bravo", Enabled: true, Config: json.RawMessage(`{}`), Position: 0},
		{ModuleID: "echo", Enabled: true, Config: json.RawMessage(`{}`), Position: 0},
		{ModuleID: "alpha", Enabled: true, Config: json.RawMessage(`{}`), Position: 1},
		{ModuleID: "charlie", Enabled: true, Config: json.RawMessage(`{}`), Position: 0},
	}

	if err := db.ReplaceRuleModules(t.Context(), campaign.ID, given); err != nil {
		t.Fatalf("ReplaceRuleModules() error = %v, want nil", err)
	}

	read, err := db.RuleModulesForCampaign(t.Context(), campaign.ID)
	if err != nil {
		t.Fatalf("RuleModulesForCampaign() error = %v, want nil", err)
	}

	if got, want := store.RuleModuleIDs(
		read,
	), store.RuleModuleIDs(
		determinism.Order(given),
	); !slices.Equal(
		got,
		want,
	) {
		t.Fatalf("the SQL order %v and determinism.Order's %v disagree", got, want)
	}
}

// TestReplaceRuleModulesReplacesRatherThanAppends is the transaction's whole point.
//
// Applying a half-written module set is not a state this project can describe, so
// the unit is the whole set: a module dropped from the caller's slice must not
// survive in the table, and one added must be there. Both directions in one test
// because a delete-and-replace that only deleted, or only inserted, would pass a
// test of either alone.
func TestReplaceRuleModulesReplacesRatherThanAppends(t *testing.T) {
	db := openTestStore(t)

	campaign := seedCampaign(t, db, "replace")

	first := []determinism.Module{
		{ModuleID: "kept", Enabled: true, Config: json.RawMessage(`{}`), Position: 0},
		{ModuleID: "dropped", Enabled: true, Config: json.RawMessage(`{}`), Position: 1},
	}
	if err := db.ReplaceRuleModules(t.Context(), campaign.ID, first); err != nil {
		t.Fatalf("first ReplaceRuleModules() error = %v, want nil", err)
	}

	second := []determinism.Module{
		{ModuleID: "kept", Enabled: false, Config: json.RawMessage(`{"x":1}`), Position: 4},
		{ModuleID: "added", Enabled: true, Config: json.RawMessage(`{}`), Position: 2},
	}
	if err := db.ReplaceRuleModules(t.Context(), campaign.ID, second); err != nil {
		t.Fatalf("second ReplaceRuleModules() error = %v, want nil", err)
	}

	got, err := db.RuleModulesForCampaign(t.Context(), campaign.ID)
	if err != nil {
		t.Fatalf("RuleModulesForCampaign() error = %v, want nil", err)
	}

	if ids := store.RuleModuleIDs(got); !slices.Equal(ids, []string{"added", "kept"}) {
		t.Fatalf("after the second write the set is %v, want [added kept]", ids)
	}

	for _, module := range got {
		if module.ModuleID == "kept" {
			if module.Enabled {
				t.Error("a module disabled by the second write came back enabled")
			}

			if module.Position != 4 {
				t.Errorf("a module repositioned by the second write came back at %d, want 4",
					module.Position)
			}
		}
	}
}

// TestARejectedModuleSetLeavesThePreviousOneIntact covers the "validate before the
// delete" half of the transaction.
//
// A caller that submits a set with one bad row must not lose the set it had. That
// is what validating on the caller's slice before opening the transaction buys, and
// it is the reason the checks are not inside the loop: a rolled-back transaction is
// correct, but a *rolled-forward* one is not, and a caller who reads
// "ReplaceRuleModules failed" as "the old set is gone" has to be wrong about the
// world to be right about their code.
func TestARejectedModuleSetLeavesThePreviousOneIntact(t *testing.T) {
	db := openTestStore(t)

	campaign := seedCampaign(t, db, "rejected")

	good := []determinism.Module{
		{ModuleID: "kept", Enabled: true, Config: json.RawMessage(`{}`), Position: 0},
	}
	if err := db.ReplaceRuleModules(t.Context(), campaign.ID, good); err != nil {
		t.Fatalf("ReplaceRuleModules() error = %v, want nil", err)
	}

	cases := []struct {
		name    string
		modules []determinism.Module
		wantErr error
	}{
		{
			name: "an unusable module id",
			modules: []determinism.Module{
				{ModuleID: "Not A Module", Enabled: true, Position: 0},
			},
			wantErr: rules.ErrInvalidID,
		},
		{
			name: "the same module twice",
			modules: []determinism.Module{
				{ModuleID: "twice", Enabled: true, Position: 0},
				{ModuleID: "twice", Enabled: false, Position: 1},
			},
			wantErr: store.ErrDuplicateRuleModule,
		},
		{
			name: "a config that is not a JSON object",
			modules: []determinism.Module{
				{
					ModuleID: "bad-config",
					Enabled:  true,
					Config:   json.RawMessage(`[1,2,3]`),
					Position: 0,
				},
			},
			wantErr: store.ErrInvalidRuleModuleConfig,
		},
		{
			name: "a config that is not JSON at all",
			modules: []determinism.Module{
				{ModuleID: "worse", Enabled: true, Config: json.RawMessage(`{nope`), Position: 0},
			},
			wantErr: store.ErrInvalidRuleModuleConfig,
		},
		{
			name: "a good module beside a bad one",
			modules: []determinism.Module{
				{ModuleID: "fine", Enabled: true, Config: json.RawMessage(`{}`), Position: 0},
				{ModuleID: "Bad", Enabled: true, Config: json.RawMessage(`{}`), Position: 1},
			},
			wantErr: rules.ErrInvalidID,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := db.ReplaceRuleModules(t.Context(), campaign.ID, testCase.modules)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("ReplaceRuleModules() = %v, want one wrapping %v", err, testCase.wantErr)
			}

			got, err := db.RuleModulesForCampaign(t.Context(), campaign.ID)
			if err != nil {
				t.Fatalf("RuleModulesForCampaign() error = %v, want nil", err)
			}

			if ids := store.RuleModuleIDs(got); !slices.Equal(ids, []string{"kept"}) {
				t.Errorf("after the rejected write the set is %v, want [kept] unchanged",
					ids)
			}
		})
	}
}

// TestAMistypedModuleIdIsReportedInPreferenceToAnUnknownCampaign covers a caller who
// has made two mistakes at once, and says which one is reported.
//
// Validating before the transaction is *not* observable in the way it looks: the
// writer wraps whatever comes back with `ErrWriteFailed` and a failed transaction
// rolls back, so moving the checks inside the write function produces the same
// errors.Is answer with a longer chain. That was measured — three mutations of the
// ordering, two of which no test noticed — and it is worth recording rather than
// claiming a property the code does not have.
//
// What *is* observable is which mistake wins. A set carrying an unusable module id,
// submitted for a campaign that does not exist, is two errors; this answers with the
// one about a value the caller typed, because that is the one they can fix without
// going and looking up a campaign they entered correctly. Both orderings give this
// answer, and the assertion is here so that a future reorder — a delete before the
// checks, say — cannot quietly change it to `ErrForeignKey`.
func TestAMistypedModuleIdIsReportedInPreferenceToAnUnknownCampaign(t *testing.T) {
	db := openTestStore(t)

	err := db.ReplaceRuleModules(t.Context(), 9999, []determinism.Module{
		{ModuleID: "Not A Module", Enabled: true, Config: json.RawMessage(`{}`), Position: 0},
	})

	if !errors.Is(err, rules.ErrInvalidID) {
		t.Fatalf("ReplaceRuleModules() with a bad id and an unknown campaign = %v, want one "+
			"wrapping rules.ErrInvalidID: the id is the mistake the caller can fix", err)
	}

	if errors.Is(err, store.ErrForeignKey) {
		t.Error("the unknown campaign was reported, which means the delete ran before the " +
			"checks: a caller who mistyped a module id was sent to look up a campaign " +
			"they named correctly")
	}
}

// TestReplaceRuleModulesRefusesAnUnknownCampaign covers the foreign key.
//
// ErrForeignKey and not ErrNotFound, for the reason memberships.go gives: the two
// demand different answers. Note that it is the *write* that catches it and not the
// delete — an unknown campaign's delete succeeds and affects no rows, so without the
// insert's foreign key the write would report success for a campaign that does not
// exist, and the campaign load would then read an empty set and believe the GM had
// no house rules.
func TestReplaceRuleModulesRefusesAnUnknownCampaign(t *testing.T) {
	db := openTestStore(t)

	err := db.ReplaceRuleModules(t.Context(), 9999, []determinism.Module{
		{ModuleID: "anything", Enabled: true, Config: json.RawMessage(`{}`), Position: 0},
	})

	if !errors.Is(err, store.ErrForeignKey) {
		t.Fatalf("ReplaceRuleModules() for a campaign that does not exist = %v, "+
			"want one wrapping ErrForeignKey", err)
	}
}

// TestAModulesCampaignIDComesFromTheArgumentNotTheCaller covers the one field the
// write does not take from the slice.
//
// A caller assembling a set for campaign 7 out of rows it read for campaign 8 has
// made a mistake, and honouring the row's own `CampaignID` would write eight rows
// into the wrong campaign — silently, because the foreign key is satisfied by
// campaign 8 existing. The argument is the tenancy decision, and it wins.
func TestAModulesCampaignIDComesFromTheArgumentNotTheCaller(t *testing.T) {
	db := openTestStore(t)

	campaign := seedCampaign(t, db, "tenancy")
	other := seedCampaign(t, db, "tenancy-other")

	err := db.ReplaceRuleModules(t.Context(), campaign.ID, []determinism.Module{{
		CampaignID: other.ID,
		ModuleID:   "mislabelled",
		Enabled:    true,
		Config:     json.RawMessage(`{}`),
		Position:   0,
	}})
	if err != nil {
		t.Fatalf("ReplaceRuleModules() error = %v, want nil", err)
	}

	got, err := db.RuleModulesForCampaign(t.Context(), campaign.ID)
	if err != nil {
		t.Fatalf("RuleModulesForCampaign() error = %v, want nil", err)
	}

	if len(got) != 1 || got[0].CampaignID != campaign.ID {
		t.Fatalf(
			"the module landed in %d, want campaign %d: %v",
			got[0].CampaignID,
			campaign.ID,
			got,
		)
	}

	elsewhere, err := db.RuleModulesForCampaign(t.Context(), other.ID)
	if err != nil {
		t.Fatalf("RuleModulesForCampaign() error = %v, want nil", err)
	}

	if len(elsewhere) != 0 {
		t.Errorf("campaign %d gained %v; the argument, not the row, decides the tenancy",
			other.ID, elsewhere)
	}
}

// TestDeletingACampaignRemovesItsRuleModules is the cascade, and it is asserted
// rather than assumed because migration 0010 adds it as a departure from the
// architecture record's sketch.
//
// A module row that outlives its campaign is a row that can never be read and
// cannot be joined to anything — and the one thing it must never do is be inherited
// by a campaign registered later. `campaigns.id` is AUTOINCREMENT, so a new
// campaign's id cannot collide with a deleted one's; the cascade is hygiene, and
// this is the test that says so while it is true.
func TestDeletingACampaignRemovesItsRuleModules(t *testing.T) {
	db := openTestStore(t)

	campaign := seedCampaign(t, db, "doomed")

	err := db.ReplaceRuleModules(t.Context(), campaign.ID, []determinism.Module{
		{ModuleID: "one", Enabled: true, Config: json.RawMessage(`{}`), Position: 0},
		{ModuleID: "two", Enabled: true, Config: json.RawMessage(`{}`), Position: 1},
	})
	if err != nil {
		t.Fatalf("ReplaceRuleModules() error = %v, want nil", err)
	}

	if err := db.DeleteCampaign(t.Context(), campaign.ID); err != nil {
		t.Fatalf("DeleteCampaign() error = %v, want nil", err)
	}

	if got := countRows(t, db, "campaign_rule_modules"); got != 0 {
		t.Errorf("campaign_rule_modules holds %d rows after its campaign was deleted, "+
			"want 0: a module row is meaningless without its campaign", got)
	}
}

// TestAModulesTableIsScopedPerCampaign covers the composite key's other half.
//
// Two campaigns, the same module id in both, one set each. A `campaign_id`-only key
// would make the second write a conflict, and a `module_id`-only key would make the
// second campaign's module apply to the first — which is the multi-tenancy failure
// this schema's tenancy comments go on about at length.
func TestAModulesTableIsScopedPerCampaign(t *testing.T) {
	db := openTestStore(t)

	alpha := seedCampaign(t, db, "alpha-modules")
	zulu := seedCampaign(t, db, "zulu-modules")

	for _, campaign := range []int64{alpha.ID, zulu.ID} {
		err := db.ReplaceRuleModules(t.Context(), campaign, []determinism.Module{
			{ModuleID: "shared", Enabled: true, Config: json.RawMessage(`{}`), Position: 0},
		})
		if err != nil {
			t.Fatalf("ReplaceRuleModules(campaign %d) error = %v, want nil", campaign, err)
		}
	}

	for _, campaign := range []int64{alpha.ID, zulu.ID} {
		got, err := db.RuleModulesForCampaign(t.Context(), campaign)
		if err != nil {
			t.Fatalf("RuleModulesForCampaign(campaign %d) error = %v, want nil", campaign, err)
		}

		if len(got) != 1 || got[0].ModuleID != "shared" {
			t.Errorf("campaign %d holds %v, want exactly one module named shared",
				campaign, store.RuleModuleIDs(got))
		}
	}
}

// TestTheRuleModuleSchemaIsTheOneTheDomainAssumes holds the migration to the shape
// this package's queries assume.
//
// Every column's declared type, in one place, because a type mismatch in SQLite is
// not a compile error and not a read failure: a `campaign_id` declared TEXT would
// store the id as text and every `WHERE campaign_id = ?` issued with an int64 would
// match nothing — silently, at campaign load, as an effective ruleset with no
// modules in it. That is migration 0010's documented departure from architecture
// §10.7's sketch, and this is the test that says the departure is real and not a
// comment.
func TestTheRuleModuleSchemaIsTheOneTheDomainAssumes(t *testing.T) {
	db := openTestStore(t)

	want := map[string]string{
		"campaign_id": "INTEGER",
		"module_id":   "TEXT",
		"enabled":     "INTEGER",
		"config":      "TEXT",
		"position":    "INTEGER",
	}

	for column, declared := range want {
		var got string

		err := db.DB().
			QueryRowContext(t.Context(),
				"SELECT type FROM pragma_table_info('campaign_rule_modules') WHERE name = ?",
				column).
			Scan(&got)
		if err != nil {
			t.Errorf("campaign_rule_modules has no %s column: %v", column, err)

			continue
		}

		if got != declared {
			t.Errorf("campaign_rule_modules.%s is declared %s, want %s", column, got, declared)
		}
	}

	// The composite key, because `campaign_id` alone would make the second
	// campaign's identical module id a conflict.
	key := primaryKeyColumns(t, db, "campaign_rule_modules")
	if !slices.Equal(key, []string{"campaign_id", "module_id"}) {
		t.Errorf("campaign_rule_modules is keyed on %v, want [campaign_id module_id]", key)
	}

	// And the foreign key to the campaign, with the cascade the header records.
	// Read with `QueryRowContext` rather than a cursor, for the reason
	// campaign_state_test.go uses that shape: the answer is one row, and a cursor
	// left open on a single-connection pool blocks the next query instead of
	// erroring.
	var count int

	references := db.DB().QueryRowContext(t.Context(),
		"SELECT count(*) FROM pragma_foreign_key_list('campaign_rule_modules')")
	if err := references.Scan(&count); err != nil {
		t.Fatalf("reading campaign_rule_modules foreign keys: %v", err)
	}

	if count != 1 {
		t.Errorf("campaign_rule_modules declares %d foreign keys, want 1; every "+
			"campaign-scoped table in this schema names its campaign", count)
	}

	var (
		target    string
		column    string
		onDelete  string
		foreignTo = db.DB().QueryRowContext(t.Context(),
			`SELECT "table", "from", on_delete FROM pragma_foreign_key_list('campaign_rule_modules')`)
	)

	if err := foreignTo.Scan(&target, &column, &onDelete); err != nil {
		t.Fatalf("reading the campaign_rule_modules foreign key: %v", err)
	}

	if target != "campaigns" || column != "campaign_id" {
		t.Errorf("campaign_rule_modules references %s.%s, want campaigns.campaign_id",
			target, column)
	}

	if onDelete != "CASCADE" {
		t.Errorf("campaign_rule_modules's foreign key deletes with %q, want CASCADE: "+
			"a module row is meaningless without its campaign", onDelete)
	}
}
