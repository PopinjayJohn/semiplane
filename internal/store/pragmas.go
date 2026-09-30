package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Pragmas the project depends on. Each one is load-bearing, and each is applied
// here rather than in the DSN so that a configured DSN cannot turn it off.
const (
	// WAL. Readers do not block the writer and vice versa, which is what makes
	// a single connection viable: without it, one long read stops every write
	// for its duration.
	pragmaJournalMode = "journal_mode=WAL"

	// NORMAL rather than FULL. Under WAL, FULL fsyncs the WAL on every commit;
	// NORMAL fsyncs only on checkpoint. The difference is the durability window
	// on power loss, and the cost of FULL here is an fsync per mutation — a
	// tabletop where a token's hit points cost a disk round-trip.
	//
	// The trade is safe for this data specifically: the filesystem holds the
	// authoritative content, and campaign_state is a debounced cache of the
	// last state (ADR 0004, S-7.5). A page_revisions row lost to power loss is
	// a lost history entry, not a lost page.
	pragmaSynchronous = "synchronous=NORMAL"

	// Wait rather than fail. A transaction that cannot get the write lock
	// retries for five seconds before erroring, which covers a concurrent
	// writer and a checkpoint without turning either into a 500.
	pragmaBusyTimeout = "busy_timeout=5000"

	// Off by default in SQLite, which surprises people. The schema relies on it:
	// a campaign_state row whose campaign no longer exists is a bug, and
	// silently keeping it is how a deleted campaign's game state survives.
	pragmaForeignKeys = "foreign_keys=ON"

	// Not a pragma, but applied the same way. NORMAL is a read *and* write
	// setting; the deprecated `read_uncommitted` spelling is not.
	pragmaTempStore = "temp_store=MEMORY"
)

// applyPragmas configures the connection.
//
// journal_mode returns the mode it set rather than a bare success, so a driver
// that silently ignored the request is caught here instead of surfacing later
// as a busy error nobody can explain.
func applyPragmas(ctx context.Context, db *sql.DB) error {
	for _, pragma := range []string{
		pragmaBusyTimeout,
		pragmaForeignKeys,
		pragmaSynchronous,
		pragmaTempStore,
	} {
		if _, err := db.ExecContext(ctx, "PRAGMA "+pragma); err != nil {
			return fmt.Errorf("apply %s: %w", pragma, err)
		}
	}

	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA "+pragmaJournalMode).Scan(&mode); err != nil {
		return fmt.Errorf("apply %s: %w", pragmaJournalMode, err)
	}

	// strings.EqualFold rather than ==, and not a hand-rolled ASCII loop: the
	// loop is a thing to get subtly wrong for a comparison of two constants
	// the driver just returned. Unicode folding costs nothing at one call per
	// process.
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("apply %s: got %q, want wal; without it a single connection "+
			"serialises every read behind every write", pragmaJournalMode, mode)
	}

	return nil
}
