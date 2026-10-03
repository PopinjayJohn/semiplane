package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/semiplane/semiplane/internal/store"
)

// openRawDB returns a bare *sql.DB on a temp file, with no pragmas and no
// migration run. The migration tests need the runner's own behaviour rather
// than store.Open's, and several of them need to leave the schema in a state
// store.Open would not produce.
func openRawDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "migrate.db"))
	if err != nil {
		t.Fatalf("sql.Open() error = %v, want nil", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	// One connection, matching production. A pool here would hide exactly the
	// cursor-discipline bug these tests exist to catch: an unclosed rows cursor
	// on a single-connection pool deadlocks the next query, and on a pool it
	// silently works.
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(t.Context(), "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}

	return db
}

// appliedVersions reads what the runner recorded.
func appliedVersions(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()

	rows, err := db.QueryContext(
		t.Context(),
		"SELECT version, name FROM schema_migrations ORDER BY version",
	)
	if err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close rows: %v", err)
		}
	}()

	applied := make(map[string]int)

	for rows.Next() {
		var (
			version int
			name    string
		)

		if err := rows.Scan(&version, &name); err != nil {
			t.Fatalf("scan: %v", err)
		}

		applied[name] = version
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	return applied
}

// TestMigrateAppliesEveryEmbeddedMigration is the assertion that a fresh
// database is fully migrated. A runner that silently applied nothing would pass
// any test that only reads.
func TestMigrateAppliesEveryEmbeddedMigration(t *testing.T) {
	db := openRawDB(t)

	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatalf("Migrate() error = %v, want nil", err)
	}

	applied := appliedVersions(t, db)

	if len(applied) < 2 {
		t.Fatalf("applied %d migrations, want at least 2: %v", len(applied), applied)
	}

	if _, ok := applied["schema-migrations"]; !ok {
		t.Errorf("schema-migrations is unapplied: %v", applied)
	}

	if _, ok := applied["campaign-state"]; !ok {
		t.Errorf("campaign-state is unapplied: %v", applied)
	}
}

// TestMigrateIsIdempotent covers a restart. Re-running a CREATE TABLE would
// error, so this also proves the bookkeeping table is consulted rather than
// ignored.
func TestMigrateIsIdempotent(t *testing.T) {
	db := openRawDB(t)

	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatalf("first Migrate() error = %v, want nil", err)
	}

	before := appliedVersions(t, db)

	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatalf("second Migrate() error = %v, want nil; the runner is not consulting its "+
			"bookkeeping table", err)
	}

	after := appliedVersions(t, db)

	if len(after) != len(before) {
		t.Errorf("migration count went from %d to %d on a second run", len(before), len(after))
	}
}

// TestMigrateRecordsMetadata is the assertion that a recorded migration carries
// the version and name from the filename and a real timestamp. A zero
// applied_at is how a migration gets applied twice by a runner that thinks it
// was never applied.
func TestMigrateRecordsMetadata(t *testing.T) {
	db := openRawDB(t)

	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatalf("Migrate() error = %v, want nil", err)
	}

	var (
		version   int
		name      string
		appliedAt int64
	)

	row := db.QueryRowContext(
		t.Context(),
		"SELECT version, name, applied_at FROM schema_migrations WHERE name = ?",
		"schema-migrations",
	)

	if err := row.Scan(&version, &name, &appliedAt); err != nil {
		t.Fatalf("scan recorded migration: %v", err)
	}

	if version != 1 {
		t.Errorf("version = %d, want 1", version)
	}

	if appliedAt == 0 {
		t.Error("applied_at is zero; a runner checking that could re-apply the migration")
	}
}

// TestMigrateLeavesNoOpenCursor is the cursor-discipline claim.
//
// Every cursor in the runner must be consumed and closed before another
// statement runs, because the process holds a single connection: a leaked
// cursor does not error, it deadlocks. The test asserts it by running more
// statements immediately after a migrate, and the package's own test timeout
// is what catches a regression.
func TestMigrateLeavesNoOpenCursor(t *testing.T) {
	db := openRawDB(t)

	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatalf("Migrate() error = %v, want nil", err)
	}

	// Several statements back to back. With MaxOpenConns(1), a leaked cursor
	// blocks the second one rather than returning an error.
	for range 5 {
		var count int
		if err := db.QueryRowContext(t.Context(),
			"SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
			t.Fatalf("query after migrate: %v", err)
		}
	}
}

// TestMigrateToleratesABookkeepingTableThatDoesNotExist is the first-run state:
// the runner has to be able to ask whether it has run before the table that
// records the answer exists.
func TestMigrateToleratesABookkeepingTableThatDoesNotExist(t *testing.T) {
	db := openRawDB(t)

	// Prove the table is genuinely absent first, so a pass cannot be explained
	// by store.Open having created it.
	var name string

	row := db.QueryRowContext(t.Context(),
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", "schema_migrations")

	if err := row.Scan(&name); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("schema_migrations exists before Migrate(): err = %v", err)
	}

	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatalf("Migrate() on an empty database: %v, want nil", err)
	}
}

// TestStoreOpenMigrates is the end-to-end assertion: a caller does not run
// Migrate itself, so Open is what makes a fresh database usable.
func TestStoreOpenMigrates(t *testing.T) {
	db, err := store.Open(t.Context(), "file:"+filepath.Join(t.TempDir(), "open.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v, want nil", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("Close() error = %v, want nil", closeErr)
		}
	}()

	// campaign_state exists only from migration 0002, so its presence proves
	// the embedded set was applied rather than just the first migration.
	if _, err := db.DB().ExecContext(context.Background(),
		"INSERT INTO campaign_state (campaign_id, state, version, updated_at) VALUES (?, ?, ?, ?)",
		1, []byte("{}"), 0, 1); err != nil {
		t.Errorf("insert into campaign_state: %v; store.Open did not apply every migration", err)
	}
}

// TestMigrateAppliesToThePreviousVersion is the upgrade-path assertion: a
// database migrated by the previous phase's build is at version 10, and the
// new migration must apply on top of it cleanly.
//
// The previous version is simulated rather than checked out, because the
// runner has no "apply only these" mode: the full set is applied, then the
// newest migration's bookkeeping row is deleted and its table dropped, which
// leaves the database in exactly the state a version-10 build would have
// left it. The assertion is then that Migrate applies 0011 and nothing else —
// a runner that re-applied an earlier migration would fail on a CREATE TABLE,
// and one that applied 0011 twice would fail on the primary key.
func TestMigrateAppliesToThePreviousVersion(t *testing.T) {
	db := openRawDB(t)

	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatalf("initial Migrate() error = %v, want nil", err)
	}

	// Roll back to version 10: drop the newest table and its bookkeeping row.
	if _, err := db.ExecContext(t.Context(), "DROP TABLE secrets_revealed"); err != nil {
		t.Fatalf("drop secrets_revealed: %v", err)
	}

	if _, err := db.ExecContext(t.Context(),
		"DELETE FROM schema_migrations WHERE name = ?", "secrets-revealed",
	); err != nil {
		t.Fatalf("delete bookkeeping row: %v", err)
	}

	// The database is now at version 10. Migrate must apply exactly 0011.
	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatalf("Migrate() at the previous version error = %v, want nil", err)
	}

	applied := appliedVersions(t, db)

	if _, ok := applied["secrets-revealed"]; !ok {
		t.Error("secrets-revealed is unapplied after Migrate at the previous version")
	}

	// And the table is back.
	var name string

	row := db.QueryRowContext(t.Context(),
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", "secrets_revealed")

	if err := row.Scan(&name); err != nil {
		t.Errorf("secrets_revealed is missing after Migrate at the previous version: %v", err)
	}
}

// TestMigrateShippedSetIsExactlyTheExpectedSet is the assertion that the
// shipped migrations are unmodified and complete.
//
// The forward-only rule (AGENTS.md: "Never edit a shipped migration — add a
// new one") is a process rule, and this is the practical enforcement available
// to a test: the embedded migration set is exactly the expected set, so an
// addition, a rename, or a removal is caught. A modification of a shipped
// migration's *body* is not caught by this test — the name set is unchanged —
// and that gap is stated rather than hidden: the guard is the process rule
// plus the review that enforces it, and a test that claimed to catch body
// edits would be a test that cannot fail.
//
// The table-existence check is the other half: a migration whose CREATE TABLE
// was deleted would still record its name in schema_migrations (the SQL would
// be a comment-only no-op), so the name set alone would pass. Asserting the
// table exists is what catches a migration that was gutted rather than removed.
func TestMigrateShippedSetIsExactlyTheExpectedSet(t *testing.T) {
	db := openRawDB(t)

	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatalf("Migrate() error = %v, want nil", err)
	}

	applied := appliedVersions(t, db)

	want := []string{
		"schema-migrations",
		"campaign-state",
		"users",
		"auth-sessions",
		"campaigns",
		"campaign-members",
		"pages-and-search",
		"page-revisions",
		"audit-log",
		"campaign-rule-modules",
		"secrets-revealed",
	}

	if len(applied) != len(want) {
		t.Errorf("applied %d migrations, want %d: %v", len(applied), len(want), applied)
	}

	for _, name := range want {
		if _, ok := applied[name]; !ok {
			t.Errorf("migration %q is unapplied: %v", name, applied)
		}
	}

	// The table the newest migration creates must exist. A migration whose
	// CREATE TABLE was deleted would still be recorded as applied (the SQL
	// would be a comment-only no-op), so the name set alone would pass.
	var name string

	row := db.QueryRowContext(t.Context(),
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", "secrets_revealed")

	if err := row.Scan(&name); err != nil {
		t.Errorf("secrets_revealed is missing after Migrate(): %v", err)
	}
}
