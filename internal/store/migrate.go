package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

// migrationFS holds the migration files.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationsDir is the directory the embed directive above names. Named rather
// than repeated so a rename cannot leave the embed and the reader disagreeing.
const migrationsDir = "migrations"

// schemaMigrationsTable records what has been applied. Created by the first
// migration, not by the runner: the runner must be able to read the table
// before it knows whether it exists, and a runner that creates its own
// bookkeeping outside the migration history is a runner whose bookkeeping can
// disagree with it.
const schemaMigrationsTable = "schema_migrations"

// Migration is one forward-only schema step.
type Migration struct {
	// Version is the numeric prefix of the filename. Ordered by this, not by
	// filename: 0009 and 0010 sort the same either way, and 00010 and 0009 do
	// not.
	Version int
	// Name is the filename without its extension, for the error message.
	Name string
	// SQL is the file's contents, run as one transaction.
	SQL string
}

// Migrate applies every migration the database has not yet seen, in version
// order, each in its own transaction.
//
// Each migration commits or rolls back on its own. A batch transaction would
// make a failure at version 7 roll back versions 1 through 6 too, which is
// worse than it sounds: a partially applied schema with no record of where it
// stopped is the state this design exists to avoid.
func Migrate(ctx context.Context, db *sql.DB) error {
	pending, err := pendingMigrations(ctx, db)
	if err != nil {
		return err
	}

	for _, migration := range pending {
		if err := applyMigration(ctx, db, migration); err != nil {
			return err
		}
	}

	return nil
}

// pendingMigrations returns the migrations not yet applied, in version order.
func pendingMigrations(ctx context.Context, db *sql.DB) ([]Migration, error) {
	all, err := loadMigrations()
	if err != nil {
		return nil, err
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}

	pending := make([]Migration, 0, len(all))

	for _, migration := range all {
		if _, done := applied[migration.Version]; !done {
			pending = append(pending, migration)
		}
	}

	return pending, nil
}

// loadMigrations reads every migration file and checks the invariants that make
// the ordering meaningful: the filenames are zero-padded numeric prefixes, the
// prefixes are unique, and no file is empty.
//
// Each of those is checked rather than assumed. A duplicate version means one
// migration silently never runs; a non-numeric prefix means sort order is
// whatever the filename happens to be; an empty file means a schema step that
// did nothing, which is always a mistake.
func loadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("read migrations directory: %w", err)
	}

	migrations := make([]Migration, 0, len(entries))
	seen := make(map[int]string, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		version, name, err := parseMigrationName(entry.Name())
		if err != nil {
			return nil, err
		}

		if previous, clash := seen[version]; clash {
			return nil, fmt.Errorf(
				"duplicate migration version %d: %s and %s; a version is never reused and "+
					"never renumbered (ADR 0008)",
				version, previous, entry.Name(),
			)
		}

		seen[version] = entry.Name()

		data, err := migrationFS.ReadFile(migrationsDir + "/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		body := strings.TrimSpace(string(data))
		if body == "" {
			return nil, fmt.Errorf("migration %s is empty", entry.Name())
		}

		migrations = append(migrations, Migration{Version: version, Name: name, SQL: body})
	}

	if len(migrations) == 0 {
		return nil, errors.New("no migrations found; the embedded migration set is empty")
	}

	// Sorting the parsed version rather than trusting the directory order,
	// which fs.ReadDir does guarantee but only incidentally.
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}

// parseMigrationName extracts the version and the human name from a filename of
// the form NNNN-some-name.sql.
func parseMigrationName(filename string) (version int, name string, err error) {
	base, _, _ := strings.Cut(filename, ".")

	prefix, rest, found := strings.Cut(base, "-")
	if !found {
		return 0, "", fmt.Errorf("migration %s: want NNNN-name.sql", filename)
	}

	parsed, convErr := strconv.Atoi(prefix)
	if convErr != nil {
		return 0, "", fmt.Errorf("migration %s: version %q is not a number", filename, prefix)
	}

	if parsed <= 0 {
		return 0, "", fmt.Errorf("migration %s: version must be positive", filename)
	}

	return parsed, rest, nil
}

// appliedVersions reads the bookkeeping table, returning an empty set when it
// does not exist yet.
//
// An absent table is the normal first-run state rather than an error: the very
// first migration creates it.
func appliedVersions(ctx context.Context, db *sql.DB) (map[int]struct{}, error) {
	exists, err := schemaTableExists(ctx, db, schemaMigrationsTable)
	if err != nil || !exists {
		return map[int]struct{}{}, err
	}

	return readAppliedVersions(ctx, db)
}

// schemaTableExists reports whether a table is present.
//
// A separate query rather than reusing one rows cursor, because the existence
// check and the version read are two statements and this connection allows one
// at a time. A cursor left open across a second query deadlocks: the second
// waits for the connection the first has not released, and with a single-writer
// pool there is no other connection to give it.
func schemaTableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var name string

	row := db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table)

	if err := row.Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}

		return false, fmt.Errorf("inspect schema for %s: %w", table, err)
	}

	return true, nil
}

// readAppliedVersions returns the set of versions already applied.
//
// One statement, one cursor, consumed to completion before returning. Anything
// that leaves a cursor open across another query on a single-connection pool
// deadlocks, and the failure surfaces as a hang rather than an error — which is
// why every cursor in this file is drained and closed on the same path.
func readAppliedVersions(ctx context.Context, db *sql.DB) (map[int]struct{}, error) {
	rows, err := db.QueryContext(ctx, "SELECT version FROM "+schemaMigrationsTable)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", schemaMigrationsTable, err)
	}
	defer closeRows(rows, schemaMigrationsTable)

	applied := make(map[int]struct{})

	for rows.Next() {
		var version int

		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan %s: %w", schemaMigrationsTable, err)
		}

		applied[version] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", schemaMigrationsTable, err)
	}

	return applied, nil
}

// closeRows releases a cursor, turning a failure into a log line rather than an
// error return.
//
// A deferred close cannot return, and an unclosed cursor on a single-connection
// pool makes every later query block. That failure is severe enough to be worth
// a panic: it can only happen if the connection is already broken, and every
// query after it would fail anyway. A silent drop would leave a hang whose
// cause is three frames away.
func closeRows(rows *sql.Rows, what string) {
	if err := rows.Close(); err != nil {
		panic("store: closing " + what + " rows: " + err.Error())
	}
}

// applyMigration runs one migration and records it, atomically.
//
// The bookkeeping row is written inside the same transaction as the schema
// change. If they were separate, a crash between them would leave a migration
// applied but unrecorded — and the runner would re-run it, which for a CREATE
// TABLE is an error and for anything else is a silent second effect.
func applyMigration(ctx context.Context, db *sql.DB, migration Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %04d (%s): begin: %w", migration.Version, migration.Name, err)
	}

	if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
		// Rollback rather than returning: a failed migration inside an open
		// transaction holds SQLite's write lock until the connection closes.
		rollback(tx)

		return fmt.Errorf("migration %04d (%s): %w", migration.Version, migration.Name, err)
	}

	// A Unix second count, not a time.Time. The driver renders a time.Time as a
	// formatted string, and SQLite's dynamic typing stores that in a column
	// declared INTEGER as TEXT — so the column would hold a value its own type
	// declaration denies, and a future `WHERE applied_at < ?` against an integer
	// would compare text to integer, which sorts every text value last. Silent,
	// and wrong in the direction that hides old rows.
	record := "INSERT INTO " + schemaMigrationsTable + " (version, name, applied_at) VALUES (?, ?, ?)"
	if _, err := tx.ExecContext(
		ctx,
		record,
		migration.Version,
		migration.Name,
		nowFunc().Unix(),
	); err != nil {
		rollback(tx)

		return fmt.Errorf("migration %04d (%s): record: %w", migration.Version, migration.Name, err)
	}

	if err := tx.Commit(); err != nil {
		rollback(tx)

		return fmt.Errorf("migration %04d (%s): commit: %w", migration.Version, migration.Name, err)
	}

	return nil
}

// nowFunc is the package's single clock, indirected so a test can pin it. A
// timestamp is the one place where a clock reading ends up in the database, and it
// is otherwise impossible to assert against; funnelling every read through one
// variable is what keeps a second time.Now from creeping in beside it.
var nowFunc = time.Now
