package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/store"
)

// openTestStore returns an open Store on a temporary database, closed on cleanup.
//
// A real file rather than :memory:. An in-memory database belongs to the
// connection that created it, so the single-connection model and the migration
// runner's transaction boundaries are not exercised at all — which is most of
// what these tests are for.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()

	return openTestStoreAt(t, filepath.Join(t.TempDir(), "semiplane.db"))
}

func openTestStoreAt(t *testing.T, path string) *store.Store {
	t.Helper()

	db, err := store.Open(t.Context(), "file:"+path)
	if err != nil {
		t.Fatalf("store.Open() error = %v, want nil", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("store.Close() error = %v, want nil", err)
		}
	})

	return db
}

// TestOpenAppliesMigrations is the assertion that a fresh database is usable
// after Open. Without it, a migration runner that silently applied nothing
// would pass every test that only reads.
func TestOpenAppliesMigrations(t *testing.T) {
	db := openTestStore(t)

	var count int

	err := db.DB().
		QueryRowContext(t.Context(), "SELECT count(*) FROM schema_migrations").
		Scan(&count)
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}

	if count == 0 {
		t.Error("schema_migrations is empty; Open ran no migrations")
	}
}

// TestOpenEnablesWAL checks the pragma that the single-connection model
// depends on. Without WAL, one long read blocks every write, and the symptom
// is a slow server rather than a failing one.
func TestOpenEnablesWAL(t *testing.T) {
	db := openTestStore(t)

	var mode string
	if err := db.DB().QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}

	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

// TestOpenEnforcesForeignKeys covers the pragma SQLite ships disabled. The
// schema relies on it: a campaign_state row outliving its campaign is exactly
// the class of orphan a foreign key exists to prevent.
func TestOpenEnforcesForeignKeys(t *testing.T) {
	db := openTestStore(t)

	var enabled int
	if err := db.DB().
		QueryRowContext(t.Context(), "PRAGMA foreign_keys").
		Scan(&enabled); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}

	if enabled != 1 {
		t.Error("foreign_keys is off; Open must enable it explicitly")
	}
}

// TestOpenIsIdempotent covers a restart: the second Open must find the
// migrations already applied and leave the database alone. Re-running a CREATE
// TABLE would error, so this also proves the bookkeeping table is consulted.
func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "semiplane.db")

	first := openTestStoreAt(t, path)

	var before int
	if err := first.DB().
		QueryRowContext(t.Context(), "SELECT count(*) FROM schema_migrations").
		Scan(&before); err != nil {
		t.Fatalf("count migrations: %v", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	second := openTestStoreAt(t, path)

	var after int
	if err := second.DB().
		QueryRowContext(t.Context(), "SELECT count(*) FROM schema_migrations").
		Scan(&after); err != nil {
		t.Fatalf("count migrations: %v", err)
	}

	if after != before {
		t.Errorf("migration count = %d after reopen, want %d", after, before)
	}
}

// TestWriteSerialises is the assertion the whole writer goroutine exists for:
// concurrent writers all succeed, and none sees a lock error.
//
// Ten goroutines each writing in a transaction. If the queue were a pool or a
// bare connection, SQLite's single-writer limit would surface as
// SQLITE_BUSY here, intermittently and on a loaded machine — which is the worst
// way for this to fail.
func TestWriteSerialises(t *testing.T) {
	db := openTestStore(t)

	const writers = 10

	var (
		wg       sync.WaitGroup
		failures atomic.Int64
	)

	for i := range writers {
		wg.Go(func() {
			// The transaction's own context, not a fresh one: the writer
			// hands the request's context to the function, and using it here
			// is what proves that context actually reaches the statement.
			err := db.Write(t.Context(), func(ctx context.Context, tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx,
					"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
					1000+i, fmt.Sprintf("probe-%d", i), time.Now().Unix()); err != nil {
					return fmt.Errorf("insert: %w", err)
				}

				return nil
			})
			if err != nil {
				failures.Add(1)
				t.Errorf("Write() error = %v, want nil", err)
			}
		})
	}

	wg.Wait()

	if got := failures.Load(); got != 0 {
		t.Errorf("%d concurrent writes failed, want 0", got)
	}

	var total int

	err := db.DB().
		QueryRowContext(t.Context(), "SELECT count(*) FROM schema_migrations").
		Scan(&total)
	if err != nil {
		t.Fatalf("count rows: %v", err)
	}

	// Every concurrent write landed, not just the ones that happened not to
	// collide.
	if total < writers {
		t.Errorf("schema_migrations has %d rows, want at least %d", total, writers)
	}
}

// TestWriteRollsBackOnError covers the transaction contract. A partial write
// that survives is worse than a failed one: the caller is told the operation
// did not happen, and the database disagrees.
func TestWriteRollsBackOnError(t *testing.T) {
	db := openTestStore(t)

	sentinel := errors.New("deliberate failure")

	err := db.Write(t.Context(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
			2000, "rolled-back", time.Now().Unix()); err != nil {
			return fmt.Errorf("insert: %w", err)
		}

		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Write() error = %v, want one wrapping %v", err, sentinel)
	}

	if !errors.Is(err, store.ErrWriteFailed) {
		t.Errorf("Write() error = %v, want one wrapping ErrWriteFailed", err)
	}

	var count int

	err = db.DB().QueryRowContext(t.Context(),
		"SELECT count(*) FROM schema_migrations WHERE version = ?", 2000).Scan(&count)
	if err != nil {
		t.Fatalf("count rolled-back rows: %v", err)
	}

	if count != 0 {
		t.Error("a failed transaction left a row behind; rollback is not happening")
	}
}

// TestWriteRefusesAfterClose covers shutdown ordering. A write that arrives
// after the store is gone must fail rather than block forever on a queue
// nobody drains — a goroutine leak that only appears under a slow shutdown.
func TestWriteRefusesAfterClose(t *testing.T) {
	db, err := store.Open(t.Context(), "file:"+filepath.Join(t.TempDir(), "semiplane.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v, want nil", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}

	writeErr := db.Write(t.Context(), func(context.Context, *sql.Tx) error {
		return nil
	})
	if !errors.Is(writeErr, store.ErrWriteFailed) {
		t.Errorf("Write() after Close error = %v, want one wrapping ErrWriteFailed", writeErr)
	}
}

// TestCloseIsIdempotent covers the path where shutdown and a test cleanup both
// reach Close. A second sql.DB.Close returns an error, and reporting that as a
// shutdown failure would be misleading.
func TestCloseIsIdempotent(t *testing.T) {
	db, err := store.Open(t.Context(), "file:"+filepath.Join(t.TempDir(), "semiplane.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v, want nil", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("first Close() error = %v, want nil", err)
	}

	if err := db.Close(); err != nil {
		t.Errorf("second Close() error = %v, want nil", err)
	}
}

// TestOpenRejectsPragmaDSN covers a configuration that would silently turn off
// something the package depends on. journal_mode=off in a DSN discards
// durability, and nothing would report it.
func TestOpenRejectsPragmaDSN(t *testing.T) {
	_, err := store.Open(
		t.Context(),
		"file:"+filepath.Join(t.TempDir(), "x.db")+"?_pragma=journal_mode(off)",
	)
	if err == nil {
		t.Error("store.Open() with a pragma DSN = nil error, want a rejection")
	}
}

func TestOpenRejectsEmptyDSN(t *testing.T) {
	if _, err := store.Open(t.Context(), "   "); err == nil {
		t.Error("store.Open(\"\") = nil error, want a rejection")
	}
}

// TestOpenAcceptsLegacySQLiteURL covers the DSN form the docs and older
// configuration use. Without the translation modernc would take `sqlite://x`
// literally as a filename and create a file called `sqlite:`, which is a
// confusing outcome rather than an error.
func TestOpenAcceptsLegacySQLiteURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	db, err := store.Open(t.Context(), "sqlite://"+path)
	if err != nil {
		t.Fatalf("store.Open() error = %v, want nil", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("Close() error = %v, want nil", closeErr)
		}
	}()

	var count int

	err = db.DB().
		QueryRowContext(t.Context(), "SELECT count(*) FROM schema_migrations").
		Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if count == 0 {
		t.Error("schema_migrations is empty; the legacy DSN did not reach the same database")
	}
}
