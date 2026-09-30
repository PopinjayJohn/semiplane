package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// The filename parser is the gate through which every migration passes, and
// its rejections are what stop a mistyped file from being silently skipped or
// applied out of order. These are internal tests because the function is
// unexported; the exported surface is exercised through Migrate.
func TestParseMigrationName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		filename  string
		wantVer   int
		wantName  string
		wantError bool
	}{
		{
			name:     "simple",
			filename: "0001-schema-migrations.sql",
			wantVer:  1,
			wantName: "schema-migrations",
		},
		{name: "four digits", filename: "0010-users.sql", wantVer: 10, wantName: "users"},
		{name: "five digits", filename: "00100-deep.sql", wantVer: 100, wantName: "deep"},
		{name: "single name segment", filename: "0002-users.sql", wantVer: 2, wantName: "users"},
		{
			// The name is whatever follows the first hyphen, so a multi-word
			// name survives intact — it is only used in error messages, but an
			// error naming the wrong migration is an error nobody can act on.
			name: "multi-word", filename: "0003-add-campaign-state.sql",
			wantVer: 3, wantName: "add-campaign-state",
		},

		{name: "no hyphen", filename: "0001.sql", wantError: true},
		{name: "no version", filename: "schema-migrations.sql", wantError: true},
		{name: "non-numeric version", filename: "abcd-users.sql", wantError: true},
		{name: "zero version", filename: "0000-users.sql", wantError: true},
		{name: "negative version", filename: "-1-users.sql", wantError: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			version, name, err := parseMigrationName(testCase.filename)

			if testCase.wantError {
				if err == nil {
					t.Fatalf("parseMigrationName(%q) = %d, nil; want an error",
						testCase.filename, version)
				}

				return
			}

			if err != nil {
				t.Fatalf("parseMigrationName(%q) error = %v, want nil", testCase.filename, err)
			}

			if version != testCase.wantVer {
				t.Errorf("version = %d, want %d", version, testCase.wantVer)
			}

			if name != testCase.wantName {
				t.Errorf("name = %q, want %q", name, testCase.wantName)
			}
		})
	}
}

// TestLoadMigrationsSortsNumerically is the reason the runner sorts on the
// parsed integer rather than trusting the directory order.
//
// Lexicographically, "0010" sorts before "0009". Applied in that order,
// version 9's statements run after version 10's — and a schema that references
// a table created by a later-numbered migration fails at a point that looks
// like a bug in the migration rather than in the ordering.
func TestLoadMigrationsSortsNumerically(t *testing.T) {
	t.Parallel()

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v, want nil", err)
	}

	if len(migrations) == 0 {
		t.Fatal("loadMigrations() returned nothing; the embedded set is not empty")
	}

	for idx := 1; idx < len(migrations); idx++ {
		if migrations[idx-1].Version >= migrations[idx].Version {
			t.Errorf("migrations[%d].Version = %d is not below migrations[%d].Version = %d",
				idx-1, migrations[idx-1].Version, idx, migrations[idx].Version)
		}
	}
}

// TestLoadMigrationsNamesAgreeWithFilenames: the recorded name is what an
// operator sees in `schema_migrations` and in an error message, so it has to
// be the name in the file rather than something derived.
func TestLoadMigrationsNamesAgreeWithFilenames(t *testing.T) {
	t.Parallel()

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v, want nil", err)
	}

	for _, migration := range migrations {
		if migration.Name == "" {
			t.Errorf("migration %d has an empty name", migration.Version)
		}

		if migration.SQL == "" {
			t.Errorf("migration %d (%s) has an empty body", migration.Version, migration.Name)
		}
	}
}

// TestApplyMigrationRollsBackOnFailure is the load-bearing property of
// per-migration transactions, and it has to be driven through applyMigration
// directly: the embedded set is a fixed input, and a permanently-broken
// migration in it would break every other test and the server's boot.
//
// The failing migration creates a table and *then* fails, so the rollback has
// something real to undo. Its bookkeeping row must not appear either —
// otherwise the migration is applied-but-unrecorded, and no later run retries it.
func TestApplyMigrationRollsBackOnFailure(t *testing.T) {
	t.Parallel()

	db := openRawDB(t)
	createBookkeeping(t, db)

	good := Migration{
		Version: 1,
		Name:    "applied-ok",
		SQL:     "CREATE TABLE applied_ok (id INTEGER PRIMARY KEY)",
	}
	bad := Migration{
		Version: 2,
		Name:    "should-not-survive",
		SQL: `
			CREATE TABLE should_not_survive (id INTEGER PRIMARY KEY);
			INSERT INTO table_that_does_not_exist (id) VALUES (1);
		`,
	}

	if err := applyMigration(t.Context(), db, good); err != nil {
		t.Fatalf("applyMigration(good) error = %v, want nil", err)
	}

	if err := applyMigration(t.Context(), db, bad); err == nil {
		t.Fatal("applyMigration(bad) = nil error, want a failure")
	}

	// The failed migration's effect is rolled back.
	if tableExists(t, db, "should_not_survive") {
		t.Error("should_not_survive exists; the failing migration was not rolled back")
	}

	// And it is not recorded, or a later run would skip it forever.
	if recorded := migrationRecorded(t, db, "should-not-survive"); recorded {
		t.Error("the failed migration was recorded as applied; it would never be retried")
	}

	// The earlier migration survives. This is what a batch transaction would
	// destroy: one failure rolling back work that had already succeeded.
	if !tableExists(t, db, "applied_ok") {
		t.Error("applied_ok is gone; migrations are not independent transactions")
	}

	if !migrationRecorded(t, db, "applied-ok") {
		t.Error("the successful migration was not recorded")
	}
}

// TestApplyMigrationRecordsAndCommitsTogether covers the other half of the same
// transaction: a migration that succeeds also records itself, and a failure to
// record rolls the schema change back rather than leaving it applied-but-
// unrecorded.
func TestApplyMigrationRecordsAndCommitsTogether(t *testing.T) {
	t.Parallel()

	db := openRawDB(t)
	createBookkeeping(t, db)

	applied := Migration{
		Version: 7,
		Name:    "recorded-together",
		SQL:     "CREATE TABLE recorded_together (id INTEGER PRIMARY KEY)",
	}

	if err := applyMigration(t.Context(), db, applied); err != nil {
		t.Fatalf("applyMigration() error = %v, want nil", err)
	}

	if !tableExists(t, db, "recorded_together") {
		t.Error("the migration's effect is missing")
	}

	if !migrationRecorded(t, db, "recorded-together") {
		t.Error("the migration ran but was not recorded; a rerun would apply it twice")
	}
}

// TestAppliedAtIsAnInteger is the assertion behind the Unix-second decision.
// The driver renders a time.Time as a formatted string, and SQLite stores that
// in a column declared INTEGER as TEXT — so the column holds a value its own
// type denies, and a comparison against an integer sorts every text value last.
func TestAppliedAtIsAnInteger(t *testing.T) {
	t.Parallel()

	db := openRawDB(t)
	createBookkeeping(t, db)

	applied := Migration{
		Version: 1,
		Name:    "timestamp-shape",
		SQL:     "CREATE TABLE timestamp_shape (id INTEGER PRIMARY KEY)",
	}

	if err := applyMigration(t.Context(), db, applied); err != nil {
		t.Fatalf("applyMigration() error = %v, want nil", err)
	}

	var appliedAt int64
	if err := db.QueryRowContext(
		t.Context(),
		"SELECT applied_at FROM schema_migrations WHERE version = ?",
		1,
	).Scan(&appliedAt); err != nil {
		t.Fatalf("scan applied_at: %v", err)
	}

	if appliedAt == 0 {
		t.Error("applied_at is zero; a runner checking that would re-apply the migration")
	}

	// The storage class is the assertion, not the value. A TEXT-stored timestamp
	// scans into an int64 here only if the driver coerces, so check the column's
	// declared affinity agrees with what was written.
	var storageClass string
	if err := db.QueryRowContext(
		t.Context(),
		"SELECT typeof(applied_at) FROM schema_migrations WHERE version = ?",
		1,
	).Scan(&storageClass); err != nil {
		t.Fatalf("scan typeof(applied_at): %v", err)
	}

	if storageClass != "integer" {
		t.Errorf("applied_at has storage class %q, want integer; a text timestamp in an "+
			"INTEGER column compares wrong against every future integer bound", storageClass)
	}
}

// openRawDB returns a bare *sql.DB on a temp file, so these tests drive the
// runner directly rather than through store.Open.
func openRawDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "internal.db"))
	if err != nil {
		t.Fatalf("sql.Open() error = %v, want nil", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	// One connection, matching production. A pool would hide exactly the
	// cursor-discipline bug these tests exist to catch: on a single connection
	// a leaked cursor deadlocks the next query, and on a pool it silently works.
	db.SetMaxOpenConns(1)

	return db
}

// createBookkeeping makes the runner's bookkeeping table exist, so a
// directly-driven migration can record itself.
func createBookkeeping(t *testing.T, db *sql.DB) {
	t.Helper()

	const ddl = `CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    applied_at INTEGER NOT NULL
)`

	if _, err := db.ExecContext(t.Context(), ddl); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
}

// tableExists reports whether a table is present.
func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	var found string

	row := db.QueryRowContext(t.Context(),
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", name)

	err := row.Scan(&found)

	return err == nil
}

// migrationRecorded reports whether the bookkeeping table has a row for a name.
func migrationRecorded(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	var found string

	row := db.QueryRowContext(t.Context(),
		"SELECT name FROM schema_migrations WHERE name = ?", name)

	err := row.Scan(&found)

	return err == nil
}
