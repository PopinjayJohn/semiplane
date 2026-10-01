package store_test

import (
	"database/sql"
	"testing"

	"github.com/semiplane/semiplane/internal/store"
)

// S-7.5's schema requirement is one sentence — "`campaign_state`: exactly one row
// per campaign" — and it is a *schema* requirement rather than a convention the
// write path is trusted to keep. The architecture record's data model says
// `campaign_state(campaign_id PRIMARY KEY, state, version, updated_at)`, and the
// whole of it is the primary key: everything else about this table is a column
// the realtime plane will fill in, and the one property that has to hold before
// any of it does is that two rows for one campaign are unrepresentable.
//
// Every test below runs against the table **migration 0002 actually created**,
// reached through `openTestStore` rather than through a hand-written copy of the
// DDL. A duplicated DDL is a second answer that fails open: a future migration
// that changed the key would leave both copies describing a table that no longer
// exists, and every assertion here would still be green.
//
// There are no accessors in this file, and that is the point. Phase 7 owns the
// read and the write, and writing them "for later" would be the second answer this
// codebase argues against everywhere: an accessor written before the write path
// exists is a second implementation of the invariant, and two implementations of
// an invariant are two things to keep in step. What phase 7 inherits is a key.

// TestCampaignStateHoldsExactlyOneRowPerCampaign is S-7.5's requirement asserted
// behaviourally: the constraint is enforced by the database, for a second write.
//
// An assertion about the DDL would pass against a table whose key was declared and
// then never enforced, and a test that only reads a schema is a test asserting
// about a string. This inserts the row twice and requires the second to fail.
func TestCampaignStateHoldsExactlyOneRowPerCampaign(t *testing.T) {
	db := openTestStore(t)

	const insert = `INSERT INTO campaign_state (campaign_id, state, version, updated_at)
        VALUES (?, ?, ?, ?)`

	if _, err := db.DB().
		ExecContext(t.Context(), insert, 1, []byte("first"), 1, 1_700_000_000); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// The second row for the *same* campaign, with different bytes throughout —
	// which is what makes this a test of the key rather than of a byte comparison.
	// A unique key over `(campaign_id, state)` would accept it, because the state
	// blob differs between two writes.
	if _, err := db.DB().ExecContext(
		t.Context(), insert, 1, []byte("second"), 2, 1_700_000_100,
	); err == nil {
		t.Fatal("a second campaign_state row for one campaign was accepted; S-7.5 " +
			"requires exactly one row per campaign")
	}

	// A different campaign is a different row, so the constraint is not simply
	// refusing all writes — the failure a test asserting only "the insert failed"
	// cannot tell from a broken table.
	if _, err := db.DB().ExecContext(
		t.Context(), insert, 2, []byte("other"), 1, 1_700_000_000,
	); err != nil {
		t.Fatalf("a row for a second campaign was refused: %v", err)
	}

	if got := countRows(t, db, "campaign_state"); got != 2 {
		t.Errorf("campaign_state holds %d rows, want 2 (one per campaign)", got)
	}
}

// TestCampaignStatePrimaryKeyIsTheSingleCampaignIDColumn is the half no
// behavioural test can make, and the reason the two above are not the whole
// story.
//
// A `UNIQUE (campaign_id, state)` index satisfies every assertion in
// `TestCampaignStateHoldsExactlyOneRowPerCampaign` and is still the wrong schema:
// it permits a second row for one campaign the moment the state blob differs, and
// it makes the constraint a property of the *data* rather than of the row. A bare
// `INTEGER PRIMARY KEY` is what makes "exactly one row" a guarantee rather than a
// convention — SQLite's rowid alias, enforced on every insert whatever the bytes
// are.
func TestCampaignStatePrimaryKeyIsTheSingleCampaignIDColumn(t *testing.T) {
	db := openTestStore(t)

	keyed := primaryKeyColumns(t, db, "campaign_state")

	if len(keyed) != 1 || keyed[0] != "campaign_id" {
		t.Errorf("campaign_state is keyed on %v, want [campaign_id]; a bare campaign_id "+
			"primary key is what makes S-7.5's 'exactly one row per campaign' a schema "+
			"guarantee rather than a convention the write path is trusted to keep", keyed)
	}
}

// TestCampaignStateColumnsAreTheOnesTheRealtimePlaneWillName asserts the other
// three columns exist, with the types the architecture record spells.
//
// Not a phase-7 accessor and not a phase-7 schema — a test that says "the column
// the write path will name is already there" is the difference between phase 7
// finding a gap on arrival and phase 7 shipping a migration for a table that
// already has what it needs.
//
// `state` is a `BLOB` and not `TEXT` because the record writes it unqualified and
// the realtime plane will put a codec's output in it: a JSON blob stored as TEXT
// is a blob whose bytes went through a UTF-8 validation, and the first non-UTF-8
// field a plugin's state carries would then fail to store. `version` and
// `updated_at` are INTEGERs for the reason `errors.go` gives about timestamps —
// an INTEGER column that holds text compares wrong against every integer bound.
func TestCampaignStateColumnsAreTheOnesTheRealtimePlaneWillName(t *testing.T) {
	db := openTestStore(t)

	for _, column := range []struct{ name, want string }{
		{"state", "BLOB"},
		{"version", "INTEGER"},
		{"updated_at", "INTEGER"},
	} {
		var declared string

		row := db.DB().QueryRowContext(t.Context(),
			"SELECT type FROM pragma_table_info('campaign_state') WHERE name = ?", column.name)

		if err := row.Scan(&declared); err != nil {
			t.Errorf("campaign_state has no %s column: %v", column.name, err)

			continue
		}

		if declared != column.want {
			t.Errorf("campaign_state.%s is declared %s, want %s",
				column.name, declared, column.want)
		}
	}
}

// TestCampaignStateVersionDefaultsToZero is the one default worth asserting,
// because it is what lets phase 7's first write be an insert of a state that
// nobody has mutated yet.
//
// A row that exists before the first intent is a real state — an empty table is a
// game that has not been joined, and a game that has been joined and left is not
// the same thing. Requiring the writer to state a version it has no reason to know
// yet would be a column that is always passed a constant.
func TestCampaignStateVersionDefaultsToZero(t *testing.T) {
	db := openTestStore(t)

	if _, err := db.DB().ExecContext(t.Context(),
		`INSERT INTO campaign_state (campaign_id, state, updated_at) VALUES (?, ?, ?)`,
		1, []byte("untouched"), 1_700_000_000); err != nil {
		t.Fatalf("insert without a version: %v", err)
	}

	var version int64

	row := db.DB().QueryRowContext(t.Context(),
		"SELECT version FROM campaign_state WHERE campaign_id = ?", 1)

	if err := row.Scan(&version); err != nil {
		t.Fatalf("read the stored version: %v", err)
	}

	if version != 0 {
		t.Errorf("version defaulted to %d, want 0", version)
	}
}

// TestCampaignStateHasNoForeignKeyYet records what migration 0002 could not do,
// so that adding one later is a deliberate forward migration rather than a
// surprise found in a diff.
//
// 0002's own comment says it: "`campaigns` itself arrives in phase 2, so there is
// no foreign key here yet; the column shape matches what phase 2 will create."
// 0005 has since created `campaigns`, and every other table in this schema that
// references one carries the key. Asserting the absence is what makes the presence
// a change somebody decided to make — and it is the seam phase 7 must not close by
// editing a shipped migration.
//
// It is also why `semiplane demo reset` (P11), which deletes a campaign and its
// state, has no cascade to rely on today.
func TestCampaignStateHasNoForeignKeyYet(t *testing.T) {
	db := openTestStore(t)

	var references int

	row := db.DB().QueryRowContext(t.Context(),
		"SELECT count(*) FROM pragma_foreign_key_list('campaign_state')")

	if err := row.Scan(&references); err != nil {
		t.Fatalf("read campaign_state foreign keys: %v", err)
	}

	if references != 0 {
		t.Errorf("campaign_state declares %d foreign keys; migration 0002 declares none, "+
			"because `campaigns` did not exist when it shipped. Adding one is a new "+
			"forward migration, never an edit to 0002", references)
	}
}

// TestCampaignStateIsNotWrittenByTheStore asserts the narrowest thing about this
// file's own scope: nothing in `store` writes `campaign_state` yet.
//
// Phase 7 owns the accessors, and a test that says so is a test that fails when
// somebody adds a speculative one — the second answer this codebase argues
// against, in the one place where it would be hardest to notice. It also documents
// why S-7.5's cadence has no implementation to point at: `campaign_state` is
// written on a trailing debounce of about two seconds after the last mutation
// (S-7.5), and there is no mutation to debounce until the realtime plane exists.
//
// Campaigns are seeded first, so the campaign ids are real and a row could have
// been written against them. The count is therefore a fact about the code rather
// than about the fixture.
func TestCampaignStateIsNotWrittenByTheStore(t *testing.T) {
	db := openTestStore(t)

	seedUser(t, db, "gm")
	seedCampaign(t, db, "gilded-cage")
	seedCampaign(t, db, "iron-vault")

	if got := countRows(t, db, "campaign_state"); got != 0 {
		t.Errorf("store wrote %d campaign_state rows; phase 7 owns the accessors, and "+
			"nothing in this package should be writing live game state yet", got)
	}
}

// countRows counts a table's rows.
func countRows(t *testing.T, db *store.Store, table string) int {
	t.Helper()

	// The table name is a literal from this file rather than anything a caller
	// supplies; `gosec` is right to flag a spliced identifier and there is nothing
	// to parameterise a table name with.
	var count int

	if err := db.DB().
		QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).
		Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}

	return count
}

// primaryKeyColumns returns the columns of a table's primary key, in key order.
func primaryKeyColumns(t *testing.T, db *store.Store, table string) []string {
	t.Helper()

	rows, err := db.DB().QueryContext(t.Context(), "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()

	var keyed []string

	for rows.Next() {
		var (
			cid       int
			name      string
			declared  sql.NullString
			notNull   int
			dfltValue sql.NullString
			pk        int
		)

		if err := rows.Scan(&cid, &name, &declared, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}

		// `pk` is the column's *position* in the key, so it is non-zero for every
		// column of a composite key — which is the case the assertion is about.
		if pk > 0 {
			keyed = append(keyed, name)
		}
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err() = %v, want nil", err)
	}

	return keyed
}
