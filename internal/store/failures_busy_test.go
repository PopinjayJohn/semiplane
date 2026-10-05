package store_test

// Failure row §13 "SQLite busy": a single writer goroutine plus `busy_timeout`,
// never a pool of writers — so concurrent game writes serialise rather than
// fail.
//
// The serialisation itself is covered and recorded rather than re-asserted:
// `store_test.go`'s `TestWriteSerialises` puts ten goroutines through `Write`
// and requires every one to succeed with no lock error. Repeating that shape
// would be a second answer to the same question.
//
// What nothing pins is the two settings that half depends on, and the game
// write it is actually about:
//
//   - `busy_timeout` holding at five seconds (`pragmas.go`). Without it, the
//     first transient lock contention is a 500 rather than a wait.
//   - the pool being exactly one connection (`store.go`). A pool buys
//     contention SQLite serialises internally anyway, and reintroduces the busy
//     errors the queue exists to avoid.
//   - concurrent writes in the `campaign_state` read-modify-write shape —
//     select the version, bump it — landing every write with no busy failure
//     and a final version equal to the number of writers.
//
// Each test opens its own store and closes it on cleanup, sequentially: no
// `t.Parallel` anywhere in this file, because `store.Open` claims the
// process's single-instance slot (ADR 0004) and two simultaneous holders fail
// by design.

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/store"
)

// TestBusyTimeoutWaitsRatherThanFails pins the pragma the single-writer queue
// treats as belt and braces: a transaction that cannot take the write lock
// retries for five seconds before erroring.
func TestBusyTimeoutWaitsRatherThanFails(t *testing.T) {
	db := openTestStore(t)

	var timeout int
	if err := db.DB().
		QueryRowContext(t.Context(), "PRAGMA busy_timeout").
		Scan(&timeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}

	if timeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000; without the wait a transient writer "+
			"collision is a failed request", timeout)
	}
}

// TestThePoolIsOneConnection pins "never a pool of writers" as a number: one
// connection for the process, so exactly one statement is ever in flight and
// SQLite's single-writer limit is never contended from inside this process.
func TestThePoolIsOneConnection(t *testing.T) {
	db := openTestStore(t)

	if got := db.DB().Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1; a pool reintroduces the busy "+
			"errors the single writer queue exists to avoid", got)
	}
}

// TestASecondOpenIsRefusedWhileOneIsHeld pins the single-instance slot: a
// second handle would mean a second writer queue, so the second `Open` is an
// error rather than a connection. The slot is released on `Close`, so opening
// again afterwards must succeed — otherwise a restart-shaped sequence wedges.
func TestASecondOpenIsRefusedWhileOneIsHeld(t *testing.T) {
	first, err := store.Open(t.Context(), "file:"+filepath.Join(t.TempDir(), "single-slot.db"))
	if err != nil {
		t.Fatalf("first store.Open() error = %v, want nil", err)
	}

	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Errorf("first store.Close() error = %v, want nil", err)
		}
	})

	second, err := store.Open(
		t.Context(),
		"file:"+filepath.Join(t.TempDir(), "single-slot-second.db"),
	)
	if err == nil {
		_ = second.Close()
		t.Error("a second store.Open() while one is held = nil error, want a refusal; " +
			"two handles would mean two writer queues and the ADR 0004 guarantee " +
			"would quietly not apply")
	}

	if err := first.Close(); err != nil {
		t.Fatalf("first store.Close() error = %v, want nil", err)
	}

	// The slot came back with the close: one `Open` after one `Close` is the
	// restart, and it must work.
	reopened, err := store.Open(t.Context(), "file:"+filepath.Join(t.TempDir(), "single-slot.db"))
	if err != nil {
		t.Fatalf("store.Open() after Close() error = %v, want nil", err)
	}

	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("reopened store.Close() error = %v, want nil", err)
		}
	})
}

// TestConcurrentGameWritesSerialise pins the game-write shape of the queue:
// read-modify-write upserts against one `campaign_state` row, from many
// goroutines at once, with every write landing, none reporting busy, and the
// final version equal to the number of writers.
//
// The queue is what makes the read-modify-write safe: each transaction runs to
// commit before the next begins, so no two writers ever read the same version.
func TestConcurrentGameWritesSerialise(t *testing.T) {
	db := openTestStore(t)

	const campaignID = int64(1)

	if err := db.Write(t.Context(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO campaigns
			(slug, name, content_root, visibility, system_id, ruleset_version, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"gilded-cage", "The Gilded Cage", t.TempDir(),
			"private", "5e-2024", "core:v1", 1_700_000_000,
		); err != nil {
			return err
		}

		_, err := tx.ExecContext(ctx, `INSERT INTO campaign_state
			(campaign_id, state, version, updated_at) VALUES (?, ?, ?, ?)`,
			campaignID, []byte("spstate1:{\"revision\":0}"), 0, time.Now().Unix(),
		)

		return err
	}); err != nil {
		t.Fatalf("seed campaign_state: %v", err)
	}

	const writers = 16

	var (
		wg       sync.WaitGroup
		failures atomic.Int64
		busy     atomic.Int64
	)

	for range writers {
		wg.Go(func() {
			err := db.Write(t.Context(), func(ctx context.Context, tx *sql.Tx) error {
				var version int64
				if err := tx.QueryRowContext(ctx,
					`SELECT version FROM campaign_state WHERE campaign_id = ?`,
					campaignID,
				).Scan(&version); err != nil {
					return err
				}

				_, err := tx.ExecContext(ctx, `UPDATE campaign_state
					SET version = ?, updated_at = ? WHERE campaign_id = ?`,
					version+1, time.Now().Unix(), campaignID,
				)

				return err
			})
			if err != nil {
				failures.Add(1)

				if strings.Contains(strings.ToLower(err.Error()), "busy") ||
					strings.Contains(strings.ToLower(err.Error()), "locked") {
					busy.Add(1)
				}

				t.Errorf("Write() error = %v, want nil", err)
			}
		})
	}

	wg.Wait()

	if got := failures.Load(); got != 0 {
		t.Errorf("%d of %d concurrent game writes failed, want 0", got, writers)
	}

	if got := busy.Load(); got != 0 {
		t.Errorf("%d failures were busy/locked errors; the single writer queue "+
			"exists so game writes wait their turn rather than fail", got)
	}

	var version int64

	if err := db.DB().QueryRowContext(t.Context(),
		`SELECT version FROM campaign_state WHERE campaign_id = ?`, campaignID,
	).Scan(&version); err != nil {
		t.Fatalf("read the final version: %v", err)
	}

	if version != writers {
		t.Errorf("campaign_state.version = %d, want %d; every serialised write "+
			"read the previous commit, so the counter must equal the writer count",
			version, writers)
	}
}
