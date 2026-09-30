// Package store owns persistence: the SQLite connection, the migration runner,
// and every query.
//
// The filesystem is the source of truth for content. What lives here is a
// rebuildable index plus the state that has no home in a file — see ADR 0006.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	// Registers the pure-Go SQLite driver. Blank import on purpose: the package
	// is a driver, not a library, and naming it would be an unused import.
	_ "modernc.org/sqlite"
)

// Store is the process's handle on the database.
//
// One *Store per process, never copied. Every value holding one shares the same
// connection and the same writer queue, which is what makes the single-writer
// rule in ADR 0004 enforceable rather than aspirational.
type Store struct {
	db     *sql.DB
	writer *writer

	// release gives the process's single-instance slot back. Held rather than
	// reached for through a package variable, so Close works on a Store whose
	// slot has already been reclaimed.
	release func()

	// closeOnce guards Close. Shutdown and a test's cleanup can both reach it,
	// and a second sql.DB.Close returns an error rather than being harmless.
	closeOnce sync.Once
	closeErr  error
}

// Open opens the database at the given DSN, applies the pragmas the project
// depends on, and runs any outstanding migrations.
//
// Only one *Store may exist per process. A second Open is an error rather than a
// second connection: two handles means two writer queues, and the entire
// single-writer guarantee in ADR 0004 would quietly not apply.
func Open(ctx context.Context, dsn string) (*Store, error) {
	// Claimed before any work, so a second caller fails immediately rather
	// than after opening a connection and running migrations it should not have
	// run. Every failure path below returns through release.
	release, err := claimProcess()
	if err != nil {
		return nil, err
	}

	dsn, err = normaliseDSN(dsn)
	if err != nil {
		release()

		return nil, err
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		release()

		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// One connection. Not a pool. SQLite serialises writes internally anyway,
	// so a pool buys contention rather than throughput, and it reintroduces
	// exactly the busy errors the single writer queue exists to avoid. Readers
	// do not serialise behind the writer for long: WAL gives each one a
	// consistent snapshot without blocking.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := openResources(ctx, db); err != nil {
		// Closing before returning: a failed Open that leaves a *sql.DB behind
		// leaks a connection, and with it a chance of the file staying open
		// under a process the operator believes has exited. The close error is
		// not reported — the Open error is the cause, and the caller has no
		// action for the other.
		_ = db.Close()

		release()

		return nil, err
	}

	return &Store{db: db, writer: newWriter(db), release: release}, nil
}

// openResources applies the pragmas and runs the migrations. Split out so Open
// has exactly one failure path to clean up on: every resource Open has acquired
// by that point is released in one place, which is what stops a later failure
// from leaking the connection.
func openResources(ctx context.Context, db *sql.DB) error {
	if err := applyPragmas(ctx, db); err != nil {
		return err
	}

	return Migrate(ctx, db)
}

// claimProcess reserves the process's single Store slot, returning the function
// that gives it back.
//
// A bool return would be enough for one caller, but returning the release
// function makes the failure path symmetric: a caller cannot forget to give the
// slot back because it never had it.
func claimProcess() (func(), error) {
	processMu.Lock()
	defer processMu.Unlock()

	if active {
		return nil, errors.New(
			"store: a Store is already open for this process; semiplane is single-instance " +
				"by design (ADR 0004), so a second handle would mean a second writer queue",
		)
	}

	active = true

	return func() {
		processMu.Lock()
		active = false
		processMu.Unlock()
	}, nil
}

// processMu guards the single-instance claim. A bare bool would be a data race
// the moment a test opens a store from a parallel subtest, and a race here is a
// race in the guarantee the mutex exists to protect.
var (
	processMu sync.Mutex
	active    bool
)

// DB returns the underlying handle for read queries.
//
// Exposed deliberately: reads are safe to issue concurrently, and routing them
// through the writer queue would serialise a wiki page render behind a
// campaign_state flush. Anything that *writes* must go through Write instead.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Close releases the database. Safe to call more than once.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		// The writer is drained first so an in-flight write is not abandoned
		// mid-statement: SQLite would roll it back, and the caller would see a
		// success it did not get.
		s.writer.stop()
		s.closeErr = s.db.Close()
		s.release()
	})

	return s.closeErr
}

// normaliseDSN converts the configured DSN into a form modernc.org/sqlite
// understands, and refuses anything that would silently weaken the guarantees
// this package makes.
//
// The default in config is a modernc DSN (`file:semiplane.db`). A URI form
// (`sqlite://...`) appears in older configuration and in the docs, and
// modernc would take it literally as a filename — creating a file called
// `sqlite:` rather than reporting a mistake. Translating it here turns a
// confusing outcome into the obvious one.
func normaliseDSN(dsn string) (string, error) {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return "", errors.New("store: empty DSN")
	}

	if rest, ok := strings.CutPrefix(trimmed, "sqlite://"); ok {
		// sqlite:///abs/path and sqlite://relative both land here. The
		// three-slash form is a file: URI with an absolute path, and the
		// two-slash form is a path relative to the working directory.
		if abs, found := strings.CutPrefix(rest, "/"); found {
			trimmed = "file:/" + abs
		} else {
			trimmed = "file:" + rest
		}
	}

	// A DSN carrying its own pragma parameters is a DSN that can turn off
	// something this package depends on. journal_mode=off in particular
	// silently discards durability, and busy_timeout=0 turns a transient
	// writer collision into a failed request. Reject rather than override: an
	// operator who wrote it wants to know.
	if strings.Contains(trimmed, "_pragma") ||
		strings.Contains(trimmed, "?") && strings.Contains(trimmed, "pragma") {
		return "", fmt.Errorf(
			"store: DSN must not set pragmas; semiplane configures journal_mode, "+
				"busy_timeout and foreign_keys itself (got %q)",
			dsn,
		)
	}

	// A path with a query string is only meaningful if it is a file: URI. A
	// bare path is returned untouched: escaping it would mangle the separators
	// and any Windows drive letter, and sqlite takes a plain path as-is.
	return trimmed, nil
}
