package realtime_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/observability"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
)

// S-7.5 is one sentence — "`campaign_state` is written on a trailing debounce of
// about two seconds after the last mutation, and on shutdown. The crash floor is
// the last debounced state" — and every test below is one of its words made
// falsifiable. S-7.2 supplies the second half: `version` is the only ordering
// authority, per-placement.
//
// # The clock, and why the timing tests are exact rather than sleepy
//
// The debounce is the one thing in this file that cannot be deterministic, so
// `realtime.Cadence` takes the clock (`Now`, `After`) and the tests supply a fake
// one. The reason is stated in `state.go` and it is worth repeating here because it
// is what makes three of these tests possible at all:
//
//   - "M mutations inside the window produce one write" is a claim about a
//     **count**. Asserting it in real time means sleeping past the window and
//     observing whatever happened, which cannot distinguish "coalesced to one" from
//     "coalesced to one by accident of scheduling".
//   - "A mutation at T−1ms does not produce a write at T" is a claim about the
//     **absence** of an event. In real time the only way to assert absence is to
//     sleep longer than the window — which makes the test slow and the assertion
//     weaker at the same time, since a longer sleep is indistinguishable from a
//     correct one.
//   - "Shutdown writes nothing when nothing is pending" is a claim that is
//     **falsified by a write**, so a test that only asserts what happened is a test
//     that passes when the implementation is wrong and the sleep is long enough.
//
// The fake clock is driven by a `fakeScheduler` that runs the state's own
// scheduler loop against a manually advanced clock, so the window is a number the
// test chooses rather than a duration it waits out.
//
// # The store, and why the tests go through a real database
//
// The write goes through `realtime.Writer`, which is a closure over
// `store.Store.Write`. These tests close it over a **real migrated database**
// rather than a recording double, for the reason `edit/harness_test.go` gives:
// S-7.5's requirement is about a *row*, and a test that asserts a spy was called
// is a different statement about a different thing — it passes just as happily
// against a column list that does not match the table, a typo in a constraint, or
// a migration that renamed something.
//
// The `stateWriteRecorder` wrapper is layered on top and counts *commits*, so the
// coalescing test asserts a number the reader cannot get from the table.

const (
	// debounce is the window these tests use. Short enough that a real-time test
	// would also pass, and chosen anyway so that a test which accidentally runs
	// against `time.After` still finishes — the fake clock makes the *value*
	// exact, not the runtime.
	debounce = 20 * time.Millisecond
	// settleBudget is how long a test waits for the scheduler goroutine to notice
	// something. Generous, because it is a *ceiling* on a failure rather than a
	// cost on a success: a correct implementation returns from the first poll.
	settleBudget = 2 * time.Second
	// poll is how often a test checks whether the scheduler has settled.
	poll = time.Millisecond
	// stablePolls is how many consecutive unchanged samples `quiet` requires.
	//
	// Three, and not one, because a single unchanged sample cannot distinguish
	// "the scheduler has finished" from "the scheduler has not been scheduled
	// yet" — and a wait that resolves on the second of those returns before the
	// work it is waiting for has happened, which is a gate wired to nothing.
	stablePolls = 3
)

// openDatabase opens a migrated database and returns it with the two closures the
// registry needs, plus a campaign row to hang the state off.
//
// The campaign row is inserted by hand rather than through the store's own
// `CreateCampaign`, for the reason `edit/harness_test.go` gives: this file is about
// `campaign_state`, and every other table the insert touches is a precondition
// rather than a subject.
func openDatabase(t *testing.T) (*store.Store, realtime.Writer, realtime.Reader) {
	t.Helper()

	// One `store.Open` per test, and therefore per process: `store.Open` claims the
	// process's single-instance slot on purpose (ADR 0004), so a test that wanted
	// two databases would have to reach past the store. Everything below needs one
	// database and several registries over it, which is the shape a restart has.
	db, err := store.Open(t.Context(), "file:"+t.TempDir()+"/semiplane.db")
	if err != nil {
		t.Fatalf("store.Open() error = %v, want nil", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("store.Close() error = %v, want nil", err)
		}
	})

	const insertCampaign = `INSERT INTO campaigns
		(slug, name, content_root, visibility, system_id, ruleset_version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`

	if _, err := db.DB().ExecContext(t.Context(), insertCampaign,
		"gilded-cage", "The Gilded Cage", t.TempDir(), "private", "5e-2024", "core:v1", 1_700_000_000,
	); err != nil {
		t.Fatalf("insert the campaign: %v", err)
	}

	// The composition root's two one-liners, spelled out. Written here so that the
	// shape is a *checked* claim: if `store.Store.Write` ever stopped accepting a
	// plain `func(context.Context, *sql.Tx) error`, this file would fail to compile
	// before `cmd/server` did.
	writer := func(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
		return db.Write(ctx, fn)
	}

	reader := func(ctx context.Context, campaignID int64) (realtime.Persisted, error) {
		var (
			blob      []byte
			version   int64
			updatedAt int64
		)

		err := db.DB().QueryRowContext(ctx,
			`SELECT state, version, updated_at FROM campaign_state WHERE campaign_id = ?`,
			campaignID,
		).Scan(&blob, &version, &updatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return realtime.Persisted{}, realtime.ErrNoState
		}

		if err != nil {
			return realtime.Persisted{}, fmt.Errorf("read campaign_state: %w", err)
		}

		return realtime.Persisted{
			Blob:      blob,
			Version:   version,
			UpdatedAt: time.Unix(updatedAt, 0).UTC(),
		}, nil
	}

	return db, writer, reader
}

// newRegistry returns a registry over the database, wired to the test's clock and
// to a write counter.
//
// The registry is closed on cleanup, so no test has to remember the shutdown — and
// every test therefore exercises `Registry.Close`, which is the path the
// composition root depends on. A test that wanted to observe the crash case
// cancels the *clock*'s context instead, which is the documented difference.
func newRegistry(t *testing.T) (*realtime.Registry, *fakeClock, *stateWriteRecorder) {
	t.Helper()

	_, writer, reader := openDatabase(t)

	clock := newFakeClock()
	counter := &stateWriteRecorder{write: writer}

	registry := realtime.NewRegistry(t.Context(), realtime.Config{
		Write: counter.commit,
		Read:  reader,
		Cadence: realtime.Cadence{
			Debounce: debounce,
			Now:      clock.Now,
			After:    clock.After,
		},
	})

	t.Cleanup(func() {
		// `context.WithoutCancel` because `t.Context()` is cancelled *before*
		// cleanups run, and a registry that could not flush during its own cleanup
		// would make every persistence test fail for a reason that has nothing to
		// do with the code under test. This is also the pattern the file comment
		// tells the composition root to use, so writing it here is a checked claim
		// rather than a convenience.
		if err := registry.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("registry.Close() error = %v, want nil", err)
		}
	})

	return registry, clock, counter
}

// stateWriteRecorder counts commits and, optionally, fails.
//
// Counting commits rather than calls is the point: a coalescing test that counted
// *calls* would pass against an implementation that issued one write per mutation
// into a transaction that rolled back, and the difference between the two is the
// whole of S-7.5.
type stateWriteRecorder struct {
	mu      sync.Mutex
	writes  int
	failure error
	write   realtime.Writer
}

func (c *stateWriteRecorder) commit(
	ctx context.Context,
	fn func(context.Context, *sql.Tx) error,
) error {
	c.mu.Lock()
	failure := c.failure
	c.mu.Unlock()

	if failure != nil {
		return failure
	}

	if err := c.write(ctx, fn); err != nil {
		return err
	}

	c.mu.Lock()
	c.writes++
	c.mu.Unlock()

	return nil
}

func (c *stateWriteRecorder) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.writes
}

func (c *stateWriteRecorder) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failure = err
}

func (c *stateWriteRecorder) repair() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failure = nil
}

// readRow returns the `campaign_state` row for campaign 1.
//
// Asserting against the table rather than against the state's own `Snapshot` is
// deliberate: `Snapshot` is the in-memory answer, and a test that compared it to
// the row would be comparing the code to itself.
//
// Campaign 1 rather than a parameter because every row in this file is campaign 1's,
// and a parameter no test varies is a parameter every test could get wrong.
func readRow(t *testing.T, db *store.Store) (realtime.Document, int64, bool) {
	t.Helper()

	var (
		blob      []byte
		version   int64
		updatedAt int64
	)

	row := db.DB().QueryRowContext(t.Context(),
		`SELECT state, version, updated_at FROM campaign_state WHERE campaign_id = 1`)

	if err := row.Scan(&blob, &version, &updatedAt); errors.Is(err, sql.ErrNoRows) {
		return realtime.Document{}, 0, false
	} else if err != nil {
		t.Fatalf("read campaign_state: %v", err)
	}

	document, err := realtime.DecodeDocument(blob)
	if err != nil {
		t.Fatalf("decode the persisted state: %v", err)
	}

	if updatedAt <= 0 {
		t.Errorf("updated_at is %d, want a Unix second count above zero; the schema's "+
			"timestamps are integers and a zero here is a row that says it was never written",
			updatedAt)
	}

	return document, version, true
}

// mustCreate places a placement and returns the change, failing the test if it
// cannot.
//
// The `must` helpers exist because of `govet`'s `shadow` check, which is enabled by
// this repository's configuration and which every `if _, err := ...` after an earlier
// `err` would trip. The alternative — declaring one `err` per function and threading
// it through a hundred call sites — makes the assertions harder to read, and the
// error handling a test wants is "fail the test now, with the same message the test
// would have written".
func mustCreate(
	t *testing.T,
	state *realtime.CampaignState,
	id realtime.PlacementID,
	initial realtime.Placement,
) realtime.Mutation {
	t.Helper()

	change, err := state.Create(id, initial)
	if err != nil {
		t.Fatalf("Create(%s) error = %v, want nil", id, err)
	}

	return change
}

// mustMutate applies apply to a placement at its current version, which is the shape
// almost every fixture wants: a test that is about versions states `seen` itself.
func mustMutate(
	t *testing.T,
	state *realtime.CampaignState,
	id realtime.PlacementID,
	seen uint64,
	apply func(*realtime.Placement),
) realtime.Mutation {
	t.Helper()

	change, err := state.Mutate(id, seen, apply)
	if err != nil {
		t.Fatalf("Mutate(%s) error = %v, want nil", id, err)
	}

	return change
}

// mustRemove takes a placement off the tabletop at its current version.
func mustRemove(
	t *testing.T,
	state *realtime.CampaignState,
	id realtime.PlacementID,
	seen uint64,
) realtime.Mutation {
	t.Helper()

	change, err := state.Remove(id, seen)
	if err != nil {
		t.Fatalf("Remove(%s) error = %v, want nil", id, err)
	}

	return change
}

// mustOpen opens a campaign's state and fails the test if it cannot.
func mustOpen(t *testing.T, registry *realtime.Registry, campaignID int64) *realtime.CampaignState {
	t.Helper()

	state, err := registry.Open(t.Context(), campaignID)
	if err != nil {
		t.Fatalf("Open(%d) error = %v, want nil", campaignID, err)
	}

	return state
}

// placement finds a placement in a document by id.
func placement(document realtime.Document, id realtime.PlacementID) (realtime.Placement, bool) {
	for _, candidate := range document.Placements {
		if candidate.ID == id {
			return candidate, true
		}
	}

	return realtime.Placement{}, false
}

// TestVersionIsMonotonicAndUniqueUnderConcurrency is S-7.2's "monotonic version,
// the only ordering authority", asserted against N goroutines rather than a loop.
//
// The assertion is deliberately two-sided. `never goes backwards` is what a client
// reconciles on, and `never repeats` is what makes the number an *ordering* rather
// than a counter: a version that repeated would make a stale client look current,
// and a resume built on that would skip a change. A monotonic counter that
// repeats is a clock reading the same tick twice.
//
// The test also checks **convergence**: after the storm, the persisted document's
// version for `p1` is the highest any goroutine was handed. Without that, an
// implementation that stamped a version and then discarded the mutation would pass
// the monotonicity check and lose a table's worth of hit points.
func TestVersionIsMonotonicAndUniqueUnderConcurrency(t *testing.T) {
	registry, clock, _ := newRegistry(t)

	state := mustOpen(t, registry, 1)

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 30, HP: 30})

	const (
		writers = 8
		perWtr  = 25
	)

	var (
		mu       sync.Mutex
		granted  []uint64
		sequence = make([][]uint64, writers)
		wg       sync.WaitGroup
	)

	for writer := range writers {
		wg.Go(func() {
			// Read-modify-write with a re-read on conflict, which is the only
			// correct client shape against a version authority: a writer that
			// assumed its own `seen` was current would be refused, and refusing is
			// the behaviour under test elsewhere.
			seen := uint64(1)

			for range perWtr {
				for {
					change, err := state.Mutate("p1", seen, func(placement *realtime.Placement) {
						placement.HP--
					})
					if errors.Is(err, realtime.ErrVersionMismatch) {
						current, _ := state.Version("p1")
						seen = current

						continue
					}

					if err != nil {
						t.Errorf("Mutate() error = %v, want nil", err)
					}

					mu.Lock()
					granted = append(granted, change.Version)
					sequence[writer] = append(sequence[writer], change.Version)
					mu.Unlock()
					seen = change.Version

					break
				}
			}
		})
	}

	wg.Wait()

	if len(granted) != writers*perWtr {
		t.Fatalf("granted %d versions, want %d; a refused mutation that was not retried "+
			"is a lost change", len(granted), writers*perWtr)
	}

	seen := make(map[uint64]bool, len(granted))
	for _, version := range granted {
		if seen[version] {
			t.Errorf("version %d was granted twice; a repeated version makes a stale client "+
				"look current, which is the failure S-7.2's 'only ordering authority' forecloses", version)
		}

		seen[version] = true
	}

	for writer, versions := range sequence {
		for index := 1; index < len(versions); index++ {
			if versions[index] <= versions[index-1] {
				t.Errorf("writer %d saw version %d after %d; a version that goes backwards is "+
					"not an ordering", writer, versions[index], versions[index-1])
			}
		}
	}

	// Convergence. The storm ends with one flush, and the row must hold the
	// highest version any writer was given — the last writer to win, not the last
	// writer to arrive.
	clock.advance(debounce)
	clock.quiet(t)

	final, _ := state.Version("p1")
	if final != uint64(1+writers*perWtr) {
		t.Errorf("p1 is at version %d, want %d; one mutation stamped two versions, or one "+
			"stamped none", final, 1+writers*perWtr)
	}

	if got := len(granted); got == 0 {
		t.Fatal("no versions were granted")
	}

	highest := granted[0]
	for _, version := range granted {
		highest = max(highest, version)
	}

	if final < highest {
		t.Errorf("p1 is at version %d, below the highest version %d any writer was granted",
			final, highest)
	}
}

// TestTheDebounceCoalescesIsS7sCadence asserts the count, not that "a write
// happened".
//
// 200 mutations inside one window, and the assertion is `writes == 1`. A test that
// asserted "the row is current" would pass against an implementation that wrote on
// every mutation, because the last of those writes is also correct — which is the
// shape of bug S-7.5 exists to prevent, and the reason §7.3 says the cadence is
// "the main reason SQLite's single-writer limit is adequate".
func TestTheDebounceCoalescesIsS7sCadence(t *testing.T) {
	registry, clock, counter := newRegistry(t)

	state := mustOpen(t, registry, 1)

	// The initial write `Open` performs, which is the baseline the count is taken
	// against. Asserted rather than assumed: a test that subtracted "1" from a
	// baseline it never checked is a test that would silently pass against an
	// implementation that wrote twice at open.
	if got := counter.count(); got != 1 {
		t.Fatalf("after Open, %d writes, want 1 (the initial row)", got)
	}

	const mutations = 200

	mark := clock.Arms()

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 30, HP: 30})

	for range mutations - 1 {
		mustMutate(t, state,
			"p1",
			state.VersionOf("p1"),
			func(*realtime.Placement) {},
		)
	}

	// The scheduler has armed a window and is waiting on it. This is what makes the
	// `advance` below mean something: without the wait, a scheduler that has not
	// been scheduled yet would leave nothing to fire and the test would be reading
	// a coin flip.
	clock.arm(t, mark)

	// Nothing yet. The window has not elapsed, and this is asserted rather than
	// inferred from the final count — a coalescing test that only checks the end
	// passes against a leading-edge implementation.
	if got := counter.count(); got != 1 {
		t.Fatalf("after %d mutations inside the window, %d writes, want 1; the debounce "+
			"wrote before the window elapsed", mutations, got)
	}

	clock.advance(debounce)
	clock.quiet(t)

	if got := counter.count(); got != 2 {
		t.Errorf("after the window elapsed, %d writes, want 2 (the initial row plus one "+
			"coalesced flush); a table produces a handful of writes per minute, and that "+
			"is what keeps SQLite's single-writer limit from binding", got)
	}

	placement, live := state.Placement("p1")
	if !live {
		t.Fatal("p1 is not placed")
	}

	if placement.Version != mutations {
		t.Errorf("p1 is at version %d, want %d; the coalesced write carried a stale version, "+
			"so a client resuming from it would be told it is current when it is not",
			placement.Version, mutations)
	}
}

// TestTheDebounceDoesNotFireEarly is the trailing edge, and it is the half of
// S-7.5 that a "did a write happen" test cannot reach.
//
// A mutation at T−1ms must not produce a write at T. The hazard is specific and
// ADR 0037 is the reason to know it: a timer armed for T fires at T even if a
// later mutation pushed the deadline to T+1s, and a flush that trusted the timer
// rather than re-reading the deadline would write on the leading edge of the
// *previous* window. The observable is that the state is still dirty afterwards.
//
// The second half is the harder one: after the early window, a mutation at exactly
// the deadline must still produce its own write. An implementation that simply
// stopped flushing would pass the first assertion.
func TestTheDebounceDoesNotFireEarly(t *testing.T) {
	registry, clock, counter := newRegistry(t)

	state := mustOpen(t, registry, 1)

	mark := clock.Arms()

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 10, HP: 10})

	// Just short of the window. The scheduler arms for the deadline, and the clock
	// moves to one millisecond before it.
	mark = clock.arm(t, mark)

	clock.advance(debounce - time.Millisecond)
	clock.quiet(t)

	// A mutation at T−1ms. The deadline moves to roughly T + debounce.
	mustMutate(t, state, "p1", 1, func(*realtime.Placement) {})

	mark = clock.arm(t, mark)

	// Now at T. The first window's timer fires here, and the deadline has moved.
	clock.advance(time.Millisecond)
	clock.quiet(t)

	if !state.Dirty() {
		t.Error("the state is clean at T after a mutation at T-1ms; the debounce fired on the " +
			"leading edge of the previous window, which is the failure a trailing debounce " +
			"exists to avoid")
	}

	if got := counter.count(); got != 1 {
		t.Errorf("%d writes at T, want 1 (the initial row only)", got)
	}

	// The state must still write, and soon: an implementation that armed the timer
	// once and never again would satisfy the assertion above forever.
	clock.advance(debounce)
	clock.quiet(t)

	_ = mark

	if state.Dirty() {
		t.Error("the state is still dirty a whole window after its last mutation")
	}

	if got := counter.count(); got != 2 {
		t.Errorf("%d writes after the second window, want 2", got)
	}
}

// TestShutdownWritesThePendingState is S-7.5's "and on shutdown".
//
// `Close` is the shutdown. A state with an unflushed mutation must reach the table,
// because the alternative is that a controlled restart loses two seconds of
// gameplay for no reason — the crash floor is a floor, not a target.
func TestShutdownWritesThePendingState(t *testing.T) {
	db, writer, reader := openDatabase(t)

	registry := realtime.NewRegistry(t.Context(), realtime.Config{
		Write:   writer,
		Read:    reader,
		Cadence: realtime.Cadence{Debounce: time.Hour},
	})

	state := mustOpen(t, registry, 1)

	mustCreate(t, state,
		"p1",
		realtime.Placement{MaxHP: 12, HP: 7, X: 40, Y: 90},
	)

	if err := registry.Close(t.Context()); err != nil {
		t.Fatalf("registry.Close() error = %v, want nil", err)
	}

	document, version, present := readRow(t, db)
	if !present {
		t.Fatal("campaign_state holds no row after a shutdown with a pending mutation")
	}

	if got, live := placement(document, "p1"); !live {
		t.Error("the persisted state has no p1; the shutdown did not write the pending state")
	} else {
		if got.HP != 7 {
			t.Errorf("p1 is at %d hp, want 7", got.HP)
		}

		if got.Version != 1 {
			t.Errorf("p1 is at version %d, want 1; a creation stamps the placement's first "+
				"version, and the shutdown wrote the state the mutation produced", got.Version)
		}
	}

	if version != 1 {
		t.Errorf("the version column is %d, want 1; it is the campaign's mutation counter, and "+
			"one creation is one mutation", version)
	}
}

// TestShutdownWithNothingPendingWritesNothing is the negative of the one above,
// and it is the half a shutdown test usually omits.
//
// A shutdown that always writes is a shutdown that always touches the disk: every
// campaign in the instance writes a row on every restart whether or not a game was
// ever played, and a `campaign_state` row's `updated_at` stops being evidence of
// anything. Asserted by **counting commits**, because a row that is rewritten with
// identical bytes is indistinguishable from one that is left alone once you read it
// back.
func TestShutdownWithNothingPendingWritesNothing(t *testing.T) {
	db, writer, reader := openDatabase(t)

	counter := &stateWriteRecorder{write: writer}

	registry := realtime.NewRegistry(t.Context(), realtime.Config{
		Write: counter.commit,
		Read:  reader,
		// An hour. Any write in this test is therefore a shutdown write, because
		// the debounce cannot have fired.
		Cadence: realtime.Cadence{Debounce: time.Hour},
	})

	state := mustOpen(t, registry, 1)

	// Dirty the state and flush it, so "nothing pending" is reached from a state
	// that has actually been used. Without this the state would still be clean from
	// the moment it opened, and the test would prove nothing about the case it
	// names.
	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 5, HP: 5})

	if err := state.Flush(t.Context()); err != nil {
		t.Fatalf("Flush() error = %v, want nil", err)
	}

	before := counter.count()
	if before != 2 {
		t.Fatalf("after one mutation and a flush, %d writes, want 2 (open plus flush)", before)
	}

	if state.Dirty() {
		t.Fatal("the state is dirty immediately after a successful flush")
	}

	if err := registry.Close(t.Context()); err != nil {
		t.Fatalf("registry.Close() error = %v, want nil", err)
	}

	if got := counter.count(); got != before {
		t.Errorf("shutdown with nothing pending issued %d writes, want 0; a shutdown that "+
			"always writes is a shutdown that always touches the disk, and a row rewritten "+
			"with identical bytes is indistinguishable from one left alone", got-before)
	}

	// The game is still there, which is the other half: "writes nothing" must not
	// become "loses the state".
	document, _, present := readRow(t, db)
	if !present {
		t.Fatal("campaign_state holds no row after a shutdown")
	}

	if _, live := placement(document, "p1"); !live {
		t.Error("the persisted state has no p1 after a shutdown that wrote nothing")
	}
}

// TestTheCrashFloorIsTheLastDebouncedState is S-7.5's third clause, asserted from
// both sides.
//
// "Kill the debouncer without shutdown" is modelled by cancelling the registry's
// context, which the file comment defines as the crash: the scheduler returns and
// writes nothing. The assertions are deliberately split into what **is** and what
// **is not** persisted, because a test that only checked the first would pass
// against an implementation that flushed on cancellation — which is the difference
// between a crash and a shutdown, and the whole reason the two are different calls.
//
// The in-memory state is asserted too, because "the crash floor is the last
// debounced state" describes what a resume gets, and a resume reads the table.
func TestTheCrashFloorIsTheLastDebouncedState(t *testing.T) {
	_, writer, reader := openDatabase(t)

	// A registry whose lifetime this test owns, so the crash is a cancel rather
	// than a t.Cleanup.
	ctx, cancel := context.WithCancel(t.Context())

	clock := newFakeClock()

	registry := realtime.NewRegistry(ctx, realtime.Config{
		Write:   writer,
		Read:    reader,
		Cadence: realtime.Cadence{Debounce: debounce, Now: clock.Now, After: clock.After},
	})

	state, err := registry.Open(ctx, 1)
	if err != nil {
		cancel()
		t.Fatalf("registry.Open() error = %v, want nil", err)
	}

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 20, HP: 20})

	// The first window elapses, so there is a debounced state on disk.
	clock.advance(debounce)
	clock.quiet(t)

	mustMutate(t, state, "p1", 1, func(placement *realtime.Placement) {
		placement.HP = 3
	})

	// The crash. No `Close`, no flush.
	cancel()
	clock.quiet(t)

	// What the state still holds in memory — the part a `Close` would have
	// persisted and a crash does not.
	live, present := state.Placement("p1")
	if !present {
		t.Fatal("p1 is not placed in memory after a simulated crash")
	}

	if live.HP != 3 {
		t.Errorf("p1 is at %d hp in memory, want 3; the crash lost more than the debounce window",
			live.HP)
	}

	// A resume reads the table, so the table is the crash floor. Asserted through
	// a second registry over the same database, which is the only way to state "what
	// a resume would have loaded" as an observation rather than as an inference.
	resumed := realtime.NewRegistry(t.Context(), realtime.Config{
		Write:   writer,
		Read:    reader,
		Cadence: realtime.Cadence{Debounce: time.Hour},
	})

	t.Cleanup(func() {
		if err := resumed.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("registry.Close() error = %v, want nil", err)
		}
	})

	reloaded := mustOpen(t, resumed, 1)

	restored, present := reloaded.Placement("p1")
	if !present {
		t.Fatal("the resumed state has no p1; the crash floor lost the last debounced state too")
	}

	if restored.HP != 20 {
		t.Errorf("the resumed p1 is at %d hp, want 20; this is the last **debounced** value. "+
			"A resume that returned the in-memory value would be reporting a state that "+
			"survived a crash it did not", restored.HP)
	}

	if restored.Version != 1 {
		t.Errorf("the resumed p1 is at version %d, want 1; the debounced write is the floor "+
			"for the version as well as for the values", restored.Version)
	}

	// And the state that did crash must refuse further mutations, because nothing
	// would ever persist them. This is the assertion that distinguishes a crash from
	// a shutdown in the *behaviour* rather than in a comment.
	mustMutate(t, reloaded, "p1", 1, func(*realtime.Placement) {})
}

// TestAFailedWriteIsNotAWrite is the failure mode the crash floor is most easily
// lost through.
//
// A flush that errors must leave the state dirty, so the next attempt carries it.
// The alternative — clearing `dirty` on the way out of a failed write — produces a
// row that is silently stale forever, and the symptom is a table that restarts to
// a position two hours old with nothing in the log to say so.
//
// Also asserted: the failure is **reported**. A write that fails inside a debounce
// window has no caller to return to, so `LastWriteError` is the only route a human
// has, and its absence is the difference between a known-bad disk and a mystery.
func TestAFailedWriteIsNotAWrite(t *testing.T) {
	_, writer, reader := openDatabase(t)

	counter := &stateWriteRecorder{write: writer}
	broken := errors.New("the disk is full")

	clock := newFakeClock()

	registry := realtime.NewRegistry(t.Context(), realtime.Config{
		Write:   counter.commit,
		Read:    reader,
		Cadence: realtime.Cadence{Debounce: debounce, Now: clock.Now, After: clock.After},
	})

	state := mustOpen(t, registry, 1)

	counter.fail(broken)

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 8, HP: 8})

	clock.advance(debounce)
	clock.quiet(t)

	if !state.Dirty() {
		t.Error("the state is clean after a failed write; a failed write is not a write, and " +
			"the crash floor is now quietly wrong")
	}

	if !errors.Is(state.LastWriteError(), broken) {
		t.Errorf("LastWriteError() = %v, want the write failure; a write that failed inside a "+
			"debounce window has no caller to return to", state.LastWriteError())
	}

	// Repair and let the re-armed window fire. The state must recover on its own,
	// without a mutation: the retry was armed by the failure, and a retry that
	// needed a new mutation would mean a game nobody touches again never persists.
	counter.repair()

	clock.advance(debounce)
	clock.quiet(t)

	if state.Dirty() {
		t.Error("the state is still dirty after a successful retry")
	}

	if err := state.LastWriteError(); err != nil {
		t.Errorf("LastWriteError() = %v after a successful write, want nil", err)
	}
}

// TestAClientsSeqCannotForgeAuthority is S-7.2 and S-7.1 together, from the
// client's side.
//
// `seq` is the client's optimistic-reconcile counter and it is **not** authority.
// The assertion is that a client holding a very high `seq` and a stale `version`
// is refused, and the reason it is worth a test is that the refusal has to happen
// *before* the mutation, at the point where the version is checked — a check that
// ran after the write would be a check that could not stop it.
//
// Three shapes are covered because three different things could go wrong: a stale
// version, a version for a placement that does not exist, and a resume claiming to
// be ahead of the campaign's counter.
func TestAClientsSeqCannotForgeAuthority(t *testing.T) {
	registry, _, _ := newRegistry(t)

	state := mustOpen(t, registry, 1)

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 10, HP: 10})

	mustMutate(t, state, "p1", 1, func(*realtime.Placement) {})

	current, known := state.Version("p1")
	if !known || current != 2 {
		t.Fatalf("p1 is at version %d (known %t), want 2", current, known)
	}

	// A client whose `seq` is enormous and whose copy is stale. The high `seq` is
	// the part that must be irrelevant: this API has no parameter to put it in, and
	// the test states that by having no way to pass one.
	if _, err := state.Mutate("p1", 1, func(*realtime.Placement) {
		t.Error("the mutation function ran for a stale version; the version check is supposed " +
			"to happen before the caller's function is given the placement")
	}); !errors.Is(err, realtime.ErrVersionMismatch) {
		t.Errorf("Mutate() with a stale version error = %v, want ErrVersionMismatch", err)
	}

	// A version for a placement that is not there. A client resuming against a
	// placement that has since been removed must be told to resynchronise, not
	// allowed to bring it back.
	if _, err := state.Mutate("p-gone", 99, func(*realtime.Placement) {}); !errors.Is(
		err, realtime.ErrVersionMismatch,
	) {
		t.Errorf("Mutate() on a removed placement error = %v, want ErrVersionMismatch", err)
	}

	if _, present := state.Placement("p-gone"); present {
		t.Error("a mutation against a removed placement recreated it")
	}

	// "Never seen this placement" is not a wildcard. A client resuming at 0 for a
	// placement that is live is stale, and treating 0 as "no check" would let the
	// first client to connect overwrite whatever happened while it was away.
	if _, err := state.Mutate("p1", 0, func(*realtime.Placement) {}); !errors.Is(
		err, realtime.ErrVersionMismatch,
	) {
		t.Errorf("Mutate() with version 0 error = %v, want ErrVersionMismatch; 0 is a claim "+
			"that the caller has never seen this placement, not a way to skip the check", err)
	}

	// A resume claiming to be ahead of the campaign's counter, on the same
	// incarnation. This is the ADR 0004 detector: two instances behind a load
	// balancer produce exactly this, and a snapshot would hide the divergence.
	incarnation := state.Incarnation()

	if _, err := state.Resume(incarnation, state.Revision()+1_000); !errors.Is(
		err, realtime.ErrFutureVersion,
	) {
		t.Errorf("Resume() ahead of the revision error = %v, want ErrFutureVersion", err)
	}

	// A high `seq` on a *different* incarnation is a restart, not a forgery, and
	// the answer is a full snapshot. Asserted because the two cases must not be
	// confused: refusing this one would break every client that survives a restart.
	other, err := realtime.ParseIncarnation(strings.Repeat("ab", realtime.IncarnationLen))
	if err != nil {
		t.Fatalf("ParseIncarnation() error = %v, want nil", err)
	}

	resumed, err := state.Resume(other, 1_000_000)
	if err != nil {
		t.Fatalf("Resume() on a different incarnation error = %v, want nil", err)
	}

	if !resumed.Changed {
		t.Error("Resume() on a different incarnation reported nothing changed; a client whose " +
			"incarnation does not match cannot know what it missed")
	}
}

// TestTwoOpensForOneCampaignRefuseTheSecond is the "exactly one" clause, and the
// decision it pins is **refusal**.
//
// The reason refusing beats replacing is in the file comment and it is worth
// restating because it is the part a reviewer will question: a replacement orphans
// the first holder. The hub holds `*CampaignState` pointers, so replacing the
// registry's entry does not remove the first authority — it leaves two, each
// handing out versions from its own counter, and every client on each sees a
// coherent and wrong game. That is ADR 0004's failure shape arrived at from the
// other direction.
//
// The assertions are that the second open is refused, that the first state is
// untouched by the attempt, and that `Get` is the way to reach it.
func TestTwoOpensForOneCampaignRefuseTheSecond(t *testing.T) {
	registry, _, _ := newRegistry(t)

	first := mustOpen(t, registry, 1)

	mustCreate(t, first, "p1", realtime.Placement{MaxHP: 4, HP: 4})

	second, err := registry.Open(t.Context(), 1)
	if !errors.Is(err, realtime.ErrStateOpen) {
		t.Fatalf("second Open() error = %v, want ErrStateOpen", err)
	}

	if second != nil {
		t.Error("a refused Open returned a state; a caller that ignores the error now holds a " +
			"second order of authority, which is the thing the refusal exists to prevent")
	}

	// The first state is intact, and still the only one.
	held, live := registry.Get(1)
	if !live {
		t.Fatal("registry.Get() reported no live state; the refused open released the first")
	}

	if held != first {
		t.Error("registry.Get() returned a different state than the one that was opened")
	}

	if got := registry.Live(); got != 1 {
		t.Errorf("registry.Live() = %d, want 1", got)
	}

	// Two campaigns are two states. The refusal is per-campaign, and a test that
	// only opened the same one twice would pass against a registry that held one
	// state in total.
	if _, err := registry.Open(t.Context(), 2); err != nil {
		t.Fatalf("Open() for a second campaign error = %v, want nil", err)
	}

	if got := registry.Live(); got != 2 {
		t.Errorf("registry.Live() = %d after opening a second campaign, want 2", got)
	}
}

// TestAConcurrentOpenStillProducesOneState is the race behind the refusal.
//
// `reserve` takes the slot before anything is read, so two goroutines racing to
// open the same campaign cannot both find it free. A test that opened twice
// sequentially would pass against an implementation that checked and then read,
// which is the version with a window in it.
func TestAConcurrentOpenStillProducesOneState(t *testing.T) {
	registry, _, _ := newRegistry(t)

	const racers = 8

	var (
		mu      sync.Mutex
		opened  int
		refused int
		wg      sync.WaitGroup
	)

	for range racers {
		wg.Go(func() {
			state, err := registry.Open(t.Context(), 1)

			mu.Lock()
			defer mu.Unlock()

			switch {
			case err == nil:
				opened++

				if state == nil {
					t.Error("a successful Open returned a nil state")
				}
			case errors.Is(err, realtime.ErrStateOpen):
				refused++
			default:
				t.Errorf("Open() error = %v, want nil or ErrStateOpen", err)
			}
		})
	}

	wg.Wait()

	if opened != 1 {
		t.Errorf("%d opens succeeded, want 1; two live states for one campaign means two "+
			"orders of authority, which is what ADR 0004 is about", opened)
	}

	if refused != racers-1 {
		t.Errorf("%d opens were refused, want %d", refused, racers-1)
	}

	if got := registry.Live(); got != 1 {
		t.Errorf("registry.Live() = %d, want 1", got)
	}
}

// TestPerPlacementVersionsDoNotContend is §7.2's "Per-placement version, not
// global" as a behavioural claim.
//
// A global counter would satisfy every other test in this file — it is monotonic,
// it never repeats, and it converges. What it cannot do is leave `p2` alone when
// `p1` moves, and that is the property with a cost: a global counter makes every
// placement's write serialise on one value, so two tables on two placements
// contend. The assertion is therefore about `p2`'s number specifically, and a
// global counter fails it.
//
// The campaign revision is asserted to have moved, because that is the difference
// between "per-placement" and "not tracked at all": both placements' versions
// frozen would also pass a naive reading of the test name.
func TestPerPlacementVersionsDoNotContend(t *testing.T) {
	registry, _, _ := newRegistry(t)

	state := mustOpen(t, registry, 1)

	for _, id := range []realtime.PlacementID{"p1", "p2"} {
		mustCreate(t, state, id, realtime.Placement{MaxHP: 10, HP: 10})
	}

	beforeP2, _ := state.Version("p2")
	beforeRevision := state.Revision()

	const moves = 50

	for range moves {
		mustMutate(t, state,
			"p1",
			state.VersionOf("p1"),
			func(*realtime.Placement) {},
		)
	}

	afterP2, _ := state.Version("p2")
	if afterP2 != beforeP2 {
		t.Errorf("p2 is at version %d, want %d; %d mutations to p1 advanced it. A global "+
			"version would do exactly this, and it is why §7.2 says per-placement",
			afterP2, beforeP2, moves)
	}

	if got := state.VersionOf("p1"); got != 1+moves {
		t.Errorf("p1 is at version %d, want %d; a per-placement version must still advance for "+
			"its own placement", got, 1+moves)
	}

	if state.Revision() == beforeRevision {
		t.Error("the campaign revision did not advance; per-placement versions and no campaign " +
			"counter at all are different designs, and only the second one can answer " +
			"'is the persisted row behind'")
	}
}

// TestARemovedPlacementKeepsItsVersion is why the version map outlives the
// placement.
//
// A re-created `p1` is stamped **above** where the removed one was. The alternative
// restarts at 1, and a client that cached version 3 of the removed `p1` would be
// handed a new `p1` at version 1, conclude it was already current, and apply a
// stale delta to a token that did not exist when it sent it. That is the specific
// failure the retention prevents, and it is invisible to every other test here.
func TestARemovedPlacementKeepsItsVersion(t *testing.T) {
	registry, _, _ := newRegistry(t)

	state := mustOpen(t, registry, 1)

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 10, HP: 10})

	mustMutate(t, state, "p1", 1, func(*realtime.Placement) {})

	mustRemove(t, state, "p1", 2)

	// The version survives the placement's removal, and the removal itself
	// consumed one — which is what makes the next stamp 4 rather than 3.
	afterRemoval, known := state.Version("p1")
	if !known || afterRemoval != 3 {
		t.Errorf("Version(p1) after removal = %d (known %t), want 3; the removal is a change "+
			"to the placement and it stamped a version like any other", afterRemoval, known)
	}

	if _, present := state.Placement("p1"); present {
		t.Error("p1 is still placed after Remove()")
	}

	recreated, err := state.Create("p1", realtime.Placement{MaxHP: 10, HP: 10})
	if err != nil {
		t.Fatalf("re-Create() error = %v, want nil", err)
	}

	if recreated.Version != 4 {
		t.Errorf("the re-created p1 is at version %d, want 4; restarting at 1 would let a client "+
			"that cached version 3 of the removed placement believe it was current", recreated.Version)
	}
}

// TestTheCallersFunctionCannotForgeAVersion is the boundary between this type and
// a gameplay system.
//
// S-7.2 makes `version` the ordering authority and §10.1 splits authority: a
// gameplay plugin "resolves intents into state mutations", and a mutation is a
// change to a placement's *values*. It is not the authority's job to say what
// version that change is — a system that could set `Version` would be able to hand
// two clients the same number, and every reconcile built on it would be wrong.
//
// The mutation function is also given the live pointer under the lock, which is why
// the stamping is an overwrite rather than an assertion.
func TestTheCallersFunctionCannotForgeAVersion(t *testing.T) {
	registry, _, _ := newRegistry(t)

	state := mustOpen(t, registry, 1)

	mustCreate(t, state,
		"p1",
		realtime.Placement{MaxHP: 10, HP: 10, Version: 4_000},
	)

	if got := state.VersionOf("p1"); got != 1 {
		t.Errorf("p1 is at version %d after Create(), want 1; Create ignored the caller's "+
			"Version and stamped its own", got)
	}

	change, err := state.Mutate("p1", 1, func(placement *realtime.Placement) {
		placement.Version = 9_999
		placement.ID = "hijacked"
		placement.HP = 4
	})
	if err != nil {
		t.Fatalf("Mutate() error = %v, want nil", err)
	}

	if change.Version != 2 {
		t.Errorf("the change carries version %d, want 2; the caller's function stamped its own",
			change.Version)
	}

	placement, live := state.Placement("p1")
	if !live {
		t.Fatal("the mutation function's ID change removed p1")
	}

	if placement.ID != "p1" {
		t.Errorf(
			"p1's id is %q, want p1; the caller's function renamed the placement",
			placement.ID,
		)
	}

	if placement.Version != 2 {
		t.Errorf("p1 is at version %d, want 2; the caller's function set 9999 and it stuck",
			placement.Version)
	}

	if placement.HP != 4 {
		t.Errorf("p1 is at %d hp, want 4; the caller's function is supposed to be able to change "+
			"values, and this asserts the stamping is not simply rejecting the whole write", placement.HP)
	}
}

// TestZeroBytesIsNotAnEmptyTabletop is ADR 0037's fixed point, in the place where it
// would cost a table its combat.
//
// A zero-length or unrecognised `state` blob must be **refused**, not decoded into
// an empty state. A state that was never written and a state that was written and
// is empty are the same observation if the decoder is lenient, and answering the
// first with the second replaces a real game with a blank one on resume.
//
// The three refusals are the three ways the same confusion arrives:
//   - a zero-length blob, which is what a truncated write leaves;
//   - a blob with no magic prefix, which is what a foreign or hand-edited column
//     holds;
//   - a document whose placement version is 0, which is unreachable by
//     construction and therefore evidence the bytes are not what was written.
func TestZeroBytesIsNotAnEmptyTabletop(t *testing.T) {
	for name, blob := range map[string][]byte{
		"empty":       {},
		"nil":         nil,
		"whitespace":  []byte("   "),
		"no magic":    []byte(`{"revision":0,"paused":false,"placements":[]}`),
		"truncated":   []byte(`spstate1:{"revision":3,"paused":fa`),
		"wrong magic": []byte(`spstate2:{"revision":3,"placements":[]}`),
	} {
		t.Run(name, func(t *testing.T) {
			document, err := realtime.DecodeDocument(blob)
			if !errors.Is(err, realtime.ErrStateUnreadable) {
				t.Fatalf("Decode() error = %v, want ErrStateUnreadable", err)
			}

			if len(document.Placements) != 0 {
				t.Errorf("Decode() returned %d placements for an unreadable blob; a failed "+
					"decode must not hand back a partial state", len(document.Placements))
			}
		})
	}
}

// TestADocumentThatCouldNotHaveBeenWrittenIsRefused is the rest of the fixed
// point: a document that decodes as JSON but describes a state this component
// cannot produce.
//
// Each case is a way two things that must be distinguishable have become the same
// value:
//   - a placement at version 0, which is what "never stamped" and "unknown" both
//     look like;
//   - a placement above the campaign revision, which means the two counters came
//     from different states;
//   - a placement listed twice, which is two answers to "where is p1".
func TestADocumentThatCouldNotHaveBeenWrittenIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"version zero": `{"revision":4,"paused":false,"placements":[{"id":"p1","version":0}]}`,
		"above revision": `{"revision":2,"paused":false,"placements":[` +
			`{"id":"p1","version":3}]}`,
		"listed twice": `{"revision":4,"paused":false,"placements":[` +
			`{"id":"p1","version":1},{"id":"p1","version":2}]}`,
		"unusable id": `{"revision":4,"paused":false,"placements":[` +
			`{"id":"","version":1}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			// The body is hand-written and the prefix is not, because the prefix is
			// a constant in `state.go` that a test cannot reach. It is obtained the
			// only way a test can: by encoding a document that is known to be legal.
			// A test that hard-coded the prefix would be a second answer to "what
			// does the encoding look like" and would keep passing if the prefix
			// changed.
			prefix := encodingPrefix(t)

			if _, err := realtime.DecodeDocument([]byte(prefix + body)); !errors.Is(
				err, realtime.ErrStateUnreadable,
			) {
				t.Errorf("Decode() error = %v, want ErrStateUnreadable", err)
			}
		})
	}
}

// encodingPrefix returns the prefix `Encode` writes, derived from a document the
// decoder accepts.
//
// Everything before the first `{`, and **not** the brace: the test bodies below begin
// with `{` themselves, so a prefix that kept the brace would produce
// `spstate1:{{"revision":…}` — invalid JSON, refused by `Decode` for a reason that has
// nothing to do with the rule under test. That is not a hypothetical: the first
// version of this helper used `TrimSuffix`, and the mutation check that removed
// `Document.check`'s version-0 rule reported the test as passing, because every case
// was already failing for the wrong reason. A gate that passes without looking is
// worse than no gate, and this is the second time in this file's development that a
// helper's convenience hid the thing it was meant to expose.
func encodingPrefix(t *testing.T) string {
	t.Helper()

	blob, err := realtime.EncodeDocument(realtime.Document{
		Revision:   1,
		Placements: []realtime.Placement{{ID: "p1", Version: 1}},
	})
	if err != nil {
		t.Fatalf("Encode() error = %v, want nil", err)
	}

	prefix, _, found := strings.Cut(string(blob), "{")
	if !found {
		t.Fatalf("Encode() produced %q, which has no JSON body", blob)
	}

	// Round-trip it: a prefix that this package's own encoder does not recognise
	// would make every case below fail for the wrong reason, which is the mistake
	// this helper exists to prevent and the one it made the first time.
	if _, err := realtime.DecodeDocument(
		[]byte(prefix + `{"revision":1,"paused":false,"placements":` +
			`[{"id":"p1","version":1}]}`),
	); err != nil {
		t.Fatalf("the derived prefix %q does not round-trip: %v", prefix, err)
	}

	return prefix
}

// TestCreateThenRemoveStillWrites is the byte-comparison fixed point, and it is the
// one that would be least obvious to find by reading the code.
//
// A campaign that creates a placement and immediately removes it serialises to the
// **same placement list** as a campaign that never had one. An implementation that
// decided whether to write by comparing the encoded form against the last-written
// form would skip the write — and lose the fact that the game advanced, which is
// the revision a client resumes from.
//
// So the assertion is that the write happens, and that the persisted revision is
// 2: the campaign mutated twice and the row says so.
func TestCreateThenRemoveStillWrites(t *testing.T) {
	db, writer, reader := openDatabase(t)

	counter := &stateWriteRecorder{write: writer}
	clock := newFakeClock()

	registry := realtime.NewRegistry(t.Context(), realtime.Config{
		Write:   counter.commit,
		Read:    reader,
		Cadence: realtime.Cadence{Debounce: debounce, Now: clock.Now, After: clock.After},
	})

	state := mustOpen(t, registry, 1)

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 6, HP: 6})

	mustRemove(t, state, "p1", 1)

	clock.advance(debounce)
	clock.quiet(t)

	if got := counter.count(); got != 2 {
		t.Errorf("%d writes, want 2 (the initial row plus the coalesced flush); the state "+
			"encodes to the same placements as one that was never touched, and deciding "+
			"'did anything change' from the bytes would have skipped this write",
			got)
	}

	document, version, present := readRow(t, db)
	if !present {
		t.Fatal("campaign_state holds no row")
	}

	if len(document.Placements) != 0 {
		t.Errorf("the persisted state has %d placements, want 0", len(document.Placements))
	}

	if version != 2 || document.Revision != 2 {
		t.Errorf("the persisted revision is %d (column %d), want 2; a create and a removal are "+
			"two mutations and the row must say so", document.Revision, version)
	}
}

// TestTheVersionColumnAndTheDocumentMustAgree is the second fixed point, on the
// write side.
//
// `campaign_state.version` and the document's own `revision` are one number written
// twice. A row whose two copies disagree is a state whose authority is a coin toss,
// and a load that trusted the blob alone would pick one arbitrarily. The row is
// corrupted deliberately — through SQL, because no exported path produces it — and
// the resume must refuse it rather than guess.
func TestTheVersionColumnAndTheDocumentMustAgree(t *testing.T) {
	db, writer, reader := openDatabase(t)

	registry := realtime.NewRegistry(t.Context(), realtime.Config{
		Write:   writer,
		Read:    reader,
		Cadence: realtime.Cadence{Debounce: time.Hour},
	})

	t.Cleanup(func() {
		if err := registry.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("registry.Close() error = %v, want nil", err)
		}
	})

	state := mustOpen(t, registry, 1)

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 3, HP: 3})

	if err := state.Flush(t.Context()); err != nil {
		t.Fatalf("Flush() error = %v, want nil", err)
	}

	// The blob is untouched; only the column moves. A load that trusted the blob
	// would resume happily, and a load that trusted the column alone would produce
	// a state whose placements are at a version it has never heard of.
	if _, err := db.DB().ExecContext(t.Context(),
		`UPDATE campaign_state SET version = 99 WHERE campaign_id = 1`,
	); err != nil {
		t.Fatalf("corrupt the version column: %v", err)
	}

	if _, err := registry.Open(t.Context(), 1); !errors.Is(err, realtime.ErrStateOpen) {
		t.Fatalf("Open() while the state is live error = %v, want ErrStateOpen; the "+
			"corruption check needs a second registry", err)
	}

	if err := registry.Close(t.Context()); err != nil {
		t.Fatalf("registry.Close() error = %v, want nil", err)
	}

	resumed := realtime.NewRegistry(t.Context(), realtime.Config{
		Write:   writer,
		Read:    reader,
		Cadence: realtime.Cadence{Debounce: time.Hour},
	})

	t.Cleanup(func() {
		if err := resumed.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("registry.Close() error = %v, want nil", err)
		}
	})

	if _, err := resumed.Open(t.Context(), 1); !errors.Is(err, realtime.ErrStateUnreadable) {
		t.Errorf("Open() on a row whose version column disagrees error = %v, want "+
			"ErrStateUnreadable; a resume that picked one of the two numbers arbitrarily "+
			"would produce a state whose authority is a coin toss", err)
	}
}

// TestAResumeContinuesTheVersionSequence is what a resume is *for*.
//
// A client that left at version 3 and comes back to a state whose `p1` is at 3
// sends its next intent stamped 4. A resume that restarted the sequence would hand
// the client a number it had already used, and its optimistic copy would look
// current when it is not.
func TestAResumeContinuesTheVersionSequence(t *testing.T) {
	_, writer, reader := openDatabase(t)

	clock := newFakeClock()

	first := realtime.NewRegistry(t.Context(), realtime.Config{
		Write:   writer,
		Read:    reader,
		Cadence: realtime.Cadence{Debounce: debounce, Now: clock.Now, After: clock.After},
	})

	state := mustOpen(t, first, 1)

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 9, HP: 9})

	mustMutate(t, state, "p1", 1, func(*realtime.Placement) {})

	clock.advance(debounce)
	clock.quiet(t)

	incarnation := state.Incarnation()

	if err := first.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}

	second := realtime.NewRegistry(t.Context(), realtime.Config{
		Write:   writer,
		Read:    reader,
		Cadence: realtime.Cadence{Debounce: time.Hour},
	})

	t.Cleanup(func() {
		if err := second.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("registry.Close() error = %v, want nil", err)
		}
	})

	resumed := mustOpen(t, second, 1)

	if got := resumed.Incarnation(); got == incarnation {
		t.Error("the resumed state has the same incarnation as the one it replaced; an " +
			"incarnation identifies a loading, not a campaign")
	}

	if got, _ := resumed.Version("p1"); got != 2 {
		t.Errorf("the resumed p1 is at version %d, want 2; the debounced sequence must "+
			"continue rather than restart", got)
	}

	change, err := resumed.Mutate("p1", 2, func(*realtime.Placement) {})
	if err != nil {
		t.Fatalf("Mutate() after a resume error = %v, want nil", err)
	}

	if change.Version != 3 {
		t.Errorf("the first mutation after a resume is version %d, want 3; the client that left "+
			"at 2 is owed 3", change.Version)
	}
}

// TestAResumeAtTheCurrentRevisionReportsNothingChanged is the "nothing happened"
// answer, and it is the case a client hits most often: a reconnect after a quiet
// minute.
//
// Same incarnation and `since` equal to the revision means the client's copy is
// current, and the honest response is an empty delta rather than a snapshot. This
// component holds no change log, so the two answers it can give are "nothing" and
// "everything" — and it must be able to give the first, or every reconnect would
// resynchronise a table that had not moved.
func TestAResumeAtTheCurrentRevisionReportsNothingChanged(t *testing.T) {
	registry, _, _ := newRegistry(t)

	state := mustOpen(t, registry, 1)

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 9, HP: 9})

	incarnation := state.Incarnation()

	resumed, err := state.Resume(incarnation, state.Revision())
	if err != nil {
		t.Fatalf("Resume() error = %v, want nil", err)
	}

	if resumed.Changed {
		t.Error("Resume() at the current revision reported a change; a client whose copy is " +
			"current must not be sent a snapshot")
	}

	if len(resumed.Placements) != 0 {
		t.Errorf("Resume() at the current revision carried %d placements, want 0",
			len(resumed.Placements))
	}
}

// TestOpenFailsLoudlyWhenTheInitialWriteFails is the negative half of `Open`'s
// initial-write contract, and the reason the contract is load-bearing.
//
// A row that exists before the first intent is a real state — a game that has been
// joined and left is not the same thing as a game that has not been joined — and
// the distinction is invisible from inside the process, which has a `CampaignState`
// either way. It is only visible in the table.
//
// The negative is the other half: an `Open` whose initial write fails must **not**
// register the state. A state with no row is a game whose only copy is in memory,
// and the failure would surface as a crash hours later rather than here. The
// reservation is released too, so a retry is possible — a registry that kept the
// slot would refuse every future attempt with `ErrStateOpen` and the campaign
// could never be opened at all.
func TestOpenFailsLoudlyWhenTheInitialWriteFails(t *testing.T) {
	_, _, reader := openDatabase(t)

	broken := errors.New("the disk is full")

	// A writer that refuses before the statement, rather than one that fails
	// inside the transaction. Both reach `Open` as the same error, and the second
	// is already covered by `TestAFailedWriteIsNotAWrite`; this test is about the
	// registration, not about the failure's shape.
	failing := func(_ context.Context, _ func(context.Context, *sql.Tx) error) error {
		return broken
	}

	registry := realtime.NewRegistry(t.Context(), realtime.Config{
		Write:   failing,
		Read:    reader,
		Cadence: realtime.Cadence{Debounce: time.Hour},
	})

	t.Cleanup(func() {
		if err := registry.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("registry.Close() error = %v, want nil", err)
		}
	})

	state, err := registry.Open(t.Context(), 1)
	if !errors.Is(err, broken) {
		t.Fatalf("Open() error = %v, want the write failure", err)
	}

	if state != nil {
		t.Error("a failed Open returned a state; the caller now holds a game whose only copy " +
			"is in memory")
	}

	if got := registry.Live(); got != 0 {
		t.Errorf("registry.Live() = %d after a failed Open, want 0; the reservation must be "+
			"released or a retry would be refused as already open", got)
	}
}

// TestAPlacementCopyDoesNotShareState is the reason `Placement.clone` exists.
//
// A placement is handed out as a **value**, and a value copy of a slice shares its
// backing array. A hub that sorted a returned placement's conditions in place would
// be writing to the authority from outside the lock — and the race detector would
// not see it, because nothing else happens to be touching that array at the time.
// A test that only read the copy would pass against an implementation that shared
// it.
func TestAPlacementCopyDoesNotShareState(t *testing.T) {
	registry, _, _ := newRegistry(t)

	state := mustOpen(t, registry, 1)

	mustCreate(t, state, "p1", realtime.Placement{
		MaxHP: 10, HP: 10, Conditions: []string{"prone", "blessed"},
	})

	copied, live := state.Placement("p1")
	if !live {
		t.Fatal("p1 is not placed")
	}

	if len(copied.Conditions) != 2 {
		t.Fatalf("p1 has %d conditions, want 2", len(copied.Conditions))
	}

	// Sorted on the way in, which is the determinism claim: the in-memory value is
	// already in its serialised order.
	if copied.Conditions[0] != "blessed" || copied.Conditions[1] != "prone" {
		t.Errorf("p1's conditions are %v, want [blessed prone]; the set is held sorted so the "+
			"encoder's output does not depend on a sort", copied.Conditions)
	}

	copied.Conditions[0] = "hijacked"
	copied.HP = 0

	// Read the local back first, so the writes above are observably writes rather
	// than statements whose only effect is on a value nobody looks at again. A test
	// whose mutations do nothing is a test that would pass against a `clone` that
	// shared everything.
	if copied.Conditions[0] != "hijacked" || copied.HP != 0 {
		t.Fatalf("the local copy did not take the writes: conditions %v, hp %d",
			copied.Conditions, copied.HP)
	}

	again, live := state.Placement("p1")
	if !live {
		t.Fatal("p1 is not placed after the copy was written to")
	}

	if again.Conditions[0] != "blessed" {
		t.Errorf("p1's first condition is %q after a caller wrote to its copy, want blessed; "+
			"a returned placement shares its slice with the authority", again.Conditions[0])
	}

	if again.HP != 10 {
		t.Errorf("p1 is at %d hp after a caller wrote to its copy, want 10", again.HP)
	}
}

// TestTheSameStateAlwaysEncodesToTheSameBytes is the determinism requirement, and
// it is a requirement rather than a nicety because `campaign_state` is compared,
// diffed and asserted on.
//
// Two states built by the same sequence of mutations in different orders must
// encode identically. A document whose bytes depend on map iteration order is a
// document a test cannot compare and a diff cannot show, and the whole point of
// holding the placement set sorted in memory is that the encoder has nothing left
// to decide.
//
// The revision is held equal by construction — both documents describe the same
// number of mutations — which is why the comparison is meaningful.
func TestTheSameStateAlwaysEncodesToTheSameBytes(t *testing.T) {
	registry, _, _ := newRegistry(t)

	ids := []realtime.PlacementID{"p1", "p2", "p3", "p4", "p5"}

	forward, err := realtime.EncodeDocument(buildState(t, registry, 1, ids))
	if err != nil {
		t.Fatalf("Encode() error = %v, want nil", err)
	}

	backward, err := realtime.EncodeDocument(buildState(t, registry, 2, reversed(ids)))
	if err != nil {
		t.Fatalf("Encode() error = %v, want nil", err)
	}

	if !bytes.Equal(forward, backward) {
		t.Errorf("the same state encoded to different bytes in two insertion orders:\n"+
			"forward:  %s\nbackward: %s", forward, backward)
	}
}

// TestASnapshotIsSortedAndSelfConsistent is what a hub hands a client.
//
// Sorted, so the order a client renders is the order the encoder would write, and
// self-consistent, so a `snapshot` and a persisted `campaign_state` are the same
// document. The identity is the claim: a wire struct that restated the fields would
// be a second answer, and the two would drift the first time a field was added.
func TestASnapshotIsSortedAndSelfConsistent(t *testing.T) {
	registry, _, _ := newRegistry(t)

	state := mustOpen(t, registry, 1)

	for _, id := range []realtime.PlacementID{"p3", "p1", "p2"} {
		mustCreate(t, state, id, realtime.Placement{MaxHP: 10, HP: 10})
	}

	snapshot := state.Snapshot()

	if len(snapshot.Placements) != 3 {
		t.Fatalf("the snapshot has %d placements, want 3", len(snapshot.Placements))
	}

	for index, id := range []realtime.PlacementID{"p1", "p2", "p3"} {
		if snapshot.Placements[index].ID != id {
			t.Errorf("snapshot.Placements[%d].ID = %q, want %q; a snapshot must be in the "+
				"order the encoder writes", index, snapshot.Placements[index].ID, id)
		}

		if snapshot.Placements[index].Version != 1 {
			t.Errorf(
				"%s is at version %d in the snapshot, want 1",
				id,
				snapshot.Placements[index].Version,
			)
		}
	}

	if snapshot.Revision != 3 {
		t.Errorf("the snapshot's revision is %d, want 3", snapshot.Revision)
	}
}

// TestAnIncarnationRoundTripsThroughItsString is the wire format's round trip, and
// the reason there is one function for it.
//
// The client echoes the incarnation on its next resume, so a server and a client
// that disagreed about the spelling of the same 16 bytes would treat every
// reconnect as a restart and send a full snapshot every time. A test that only
// compared two `Incarnation` values would pass against an implementation whose
// string form was wrong.
func TestAnIncarnationRoundTripsThroughItsString(t *testing.T) {
	registry, _, _ := newRegistry(t)

	state := mustOpen(t, registry, 1)

	incarnation := state.Incarnation()
	if incarnation.IsZero() {
		t.Error("the state's incarnation is the zero value; every client would then look like a " +
			"restart and no resume could ever be trusted")
	}

	parsed, err := realtime.ParseIncarnation(incarnation.String())
	if err != nil {
		t.Fatalf("ParseIncarnation() error = %v, want nil", err)
	}

	if parsed != incarnation {
		t.Errorf("ParseIncarnation(%q) = %x, want %x", incarnation.String(), parsed, incarnation)
	}

	if len(incarnation.String()) != 2*realtime.IncarnationLen {
		t.Errorf("the incarnation renders as %d characters, want %d", len(incarnation.String()),
			2*realtime.IncarnationLen)
	}

	// A different campaign's state has a different incarnation, which is what makes
	// "which loading is this" answerable at all.
	other := mustOpen(t, registry, 2)

	if other.Incarnation() == incarnation {
		t.Error("two states share an incarnation; a client that had connected to one would be " +
			"told it is current against the other")
	}
}

// TestAnUnparseableIncarnationIsARefusal is the other half of the round trip.
//
// A client that sends something unrecognised has to be answered, and the answer is
// the conservative one. This component cannot tell a corrupted incarnation from a
// client it has never seen, and both are answered by a full snapshot — so
// `ParseIncarnation` returns an error and the caller does not get to guess.
func TestAnUnparseableIncarnationIsARefusal(t *testing.T) {
	for name, encoded := range map[string]string{
		"not hex":     strings.Repeat("zz", realtime.IncarnationLen),
		"too short":   "abcd",
		"too long":    strings.Repeat("ab", realtime.IncarnationLen+1),
		"empty":       "",
		"odd length":  "abc",
		"upper case":  "",
		"with spaces": "ab cd",
	} {
		t.Run(name, func(t *testing.T) {
			if encoded == "" && name != "empty" {
				t.Skip("this case carries no value")
			}

			parsed, err := realtime.ParseIncarnation(encoded)
			if err == nil {
				t.Fatalf("ParseIncarnation(%q) = %x, want an error", encoded, parsed)
			}

			if !parsed.IsZero() {
				t.Errorf("ParseIncarnation(%q) returned a non-zero incarnation alongside its "+
					"error; a caller that ignored the error would then compare a fabricated "+
					"identity against a real one", encoded)
			}
		})
	}
}

// TestACampaignCanBeReopenedAfterShutdown is the lifecycle the crash floor needs.
//
// A restart is `Close` then `Open`, and the state that comes back must be the one
// that was written. Without this, the whole "resume from the last debounced state"
// story would be untested end to end: every other resume test opens a state that
// was never closed, which is a different path.
func TestACampaignCanBeReopenedAfterShutdown(t *testing.T) {
	db, writer, reader := openDatabase(t)

	clock := newFakeClock()

	first := realtime.NewRegistry(t.Context(), realtime.Config{
		Write:   writer,
		Read:    reader,
		Cadence: realtime.Cadence{Debounce: debounce, Now: clock.Now, After: clock.After},
	})

	state := mustOpen(t, first, 1)

	mustCreate(t, state,
		"p1",
		realtime.Placement{MaxHP: 14, HP: 11, Visible: false},
	)

	mustMutate(t, state, "p1", 1, func(placement *realtime.Placement) {
		placement.HP = 2
		placement.AddCondition("prone")
	})

	// The debounced write, so the restart has something to resume from.
	clock.advance(debounce)
	clock.quiet(t)

	if err := first.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}

	if got := first.Live(); got != 0 {
		t.Errorf("registry.Live() = %d after Close, want 0; a closed registry holding states "+
			"would refuse every future open for those campaigns", got)
	}

	// Exactly one row, and it is the state that was written.
	var rows int
	if err := db.DB().QueryRowContext(t.Context(),
		`SELECT count(*) FROM campaign_state WHERE campaign_id = 1`,
	).Scan(&rows); err != nil {
		t.Fatalf("count campaign_state: %v", err)
	}

	if rows != 1 {
		t.Errorf("campaign_state holds %d rows for one campaign, want 1; S-7.5 requires "+
			"exactly one and the primary key is what makes it a guarantee", rows)
	}

	second := realtime.NewRegistry(t.Context(), realtime.Config{
		Write:   writer,
		Read:    reader,
		Cadence: realtime.Cadence{Debounce: time.Hour},
	})

	t.Cleanup(func() {
		if err := second.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("registry.Close() error = %v, want nil", err)
		}
	})

	resumed := mustOpen(t, second, 1)

	restored, live := resumed.Placement("p1")
	if !live {
		t.Fatal("the resumed state has no p1")
	}

	if restored.HP != 2 {
		t.Errorf("the resumed p1 is at %d hp, want 2", restored.HP)
	}

	if restored.Version != 2 {
		t.Errorf("the resumed p1 is at version %d, want 2", restored.Version)
	}

	if len(restored.Conditions) != 1 || restored.Conditions[0] != "prone" {
		t.Errorf("the resumed p1's conditions are %v, want [prone]", restored.Conditions)
	}

	// The version the client left at is what it resumes against, which is the whole
	// point of stamping from the loaded sequence.
	mustMutate(t, resumed, "p1", restored.Version, func(*realtime.Placement) {})
}

// TestAnEmptyStateIsARealState is the other side of `Open`'s initial write, and the
// reason it is not an optimisation.
//
// A campaign that has been opened and never played is not the same as a campaign
// that has never been opened, and the difference is only visible in the table —
// in the process both are a `CampaignState` with no placements. The assertion is
// that the row exists with revision 0, which is the state phase 1's schema test
// says a first write should be able to express.
func TestAnEmptyStateIsARealState(t *testing.T) {
	db, writer, reader := openDatabase(t)

	registry := realtime.NewRegistry(t.Context(), realtime.Config{
		Write:   writer,
		Read:    reader,
		Cadence: realtime.Cadence{Debounce: time.Hour},
	})

	t.Cleanup(func() {
		if err := registry.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("registry.Close() error = %v, want nil", err)
		}
	})

	if _, err := registry.Open(t.Context(), 1); err != nil {
		t.Fatalf("Open() error = %v, want nil", err)
	}

	document, version, present := readRow(t, db)
	if !present {
		t.Fatal("campaign_state holds no row after Open(); a game that has been joined and left " +
			"is not the same thing as a game that has not been joined")
	}

	if document.Revision != 0 || version != 0 {
		t.Errorf(
			"the fresh row carries revision %d (column %d), want 0",
			document.Revision,
			version,
		)
	}

	if len(document.Placements) != 0 {
		t.Errorf("the fresh row carries %d placements, want 0", len(document.Placements))
	}
}

// TestAClosedStateRefusesMutations is the difference between a shutdown and a
// crash, as behaviour rather than as a comment.
//
// After `Close`, a mutation is refused. A state that accepted one would hold a
// change nothing would ever write, and the caller would have no way to know — the
// GM moves a token, the broadcast goes out, and the change evaporates at the next
// restart.
func TestAClosedStateRefusesMutations(t *testing.T) {
	registry, _, _ := newRegistry(t)

	state := mustOpen(t, registry, 1)

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 3, HP: 3})

	if err := registry.Close(t.Context()); err != nil {
		t.Fatalf("registry.Close() error = %v, want nil", err)
	}

	if _, err := state.Mutate(
		"p1",
		2,
		func(*realtime.Placement) {},
	); !errors.Is(
		err,
		realtime.ErrClosed,
	) {
		t.Errorf("Mutate() after Close error = %v, want ErrClosed", err)
	}

	if _, err := state.Create("p2", realtime.Placement{MaxHP: 3, HP: 3}); !errors.Is(
		err, realtime.ErrClosed,
	) {
		t.Errorf("Create() after Close error = %v, want ErrClosed", err)
	}

	if _, err := state.SetPaused(true); !errors.Is(err, realtime.ErrClosed) {
		t.Errorf("SetPaused() after Close error = %v, want ErrClosed", err)
	}

	// Idempotent, because a shutdown and a test's cleanup both reach it.
	if err := registry.Close(t.Context()); err != nil {
		t.Errorf("a second registry.Close() error = %v, want nil; Close must be safe to call "+
			"twice because a shutdown and a test's cleanup both reach it", err)
	}
}

// TestSetPausedAdvancesTheRevisionButNoVersion is the campaign-level half of §7.2.
//
// A pause changes no placement, so it stamps no placement version — a client
// reconciling `p1` must not be told its copy is stale because the GM paused the
// game. It *does* advance the campaign revision, because the campaign mutated and
// the persisted row has to show that.
func TestSetPausedAdvancesTheRevisionButNoVersion(t *testing.T) {
	registry, clock, counter := newRegistry(t)

	state := mustOpen(t, registry, 1)

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 5, HP: 5})

	beforeRevision := state.Revision()
	beforeVersion, _ := state.Version("p1")

	if _, err := state.SetPaused(true); err != nil {
		t.Fatalf("SetPaused() error = %v, want nil", err)
	}

	if !state.Paused() {
		t.Error("Paused() is false after SetPaused(true)")
	}

	if got, _ := state.Version("p1"); got != beforeVersion {
		t.Errorf("p1 is at version %d, want %d; a pause is not a change to a placement, and a "+
			"client reconciling p1 must not be told its copy is stale", got, beforeVersion)
	}

	if state.Revision() == beforeRevision {
		t.Error("the campaign revision did not advance after a pause")
	}

	// And it is persisted, because a restarted process has to know the table was
	// paused.
	clock.advance(debounce)
	clock.quiet(t)

	if got := counter.count(); got != 2 {
		t.Errorf("%d writes after a pause, want 2 (the initial row plus one flush)", got)
	}

	if err := state.LastWriteError(); err != nil {
		t.Errorf("LastWriteError() = %v, want nil", err)
	}
}

// TestEveryEventNameIsUnchanged is the count assertion, held here as well as in
// `observability`.
//
// `state.write_ms` is already in `observability.AllEventNames()`, so this work
// item's outcome is reported through the `WriteRecorder` seam rather than a new
// name. The count and the assertion in `internal/observability` own it; restating
// it here is a tripwire for the next person who reaches for a new event name
// rather than a seam, because the cost of finding that out is a failing test
// in another package that says nothing about this one.
//
// 24 became 25 when phase 9's theme layer added `theme.brand_invalid`, recorded in
// ADR 0054 — the tripwire firing exactly as its message asks, in the adding work
// item's own commit. `spec.md` needed no change: S-12.1 and S-12.3 govern how a
// signal is emitted and what it may carry, and neither enumerates names.
func TestEveryEventNameIsUnchanged(t *testing.T) {
	if got := len(observability.AllEventNames()); got != 25 {
		t.Errorf("observability.AllEventNames() has %d entries, want 25; this work item added "+
			"no event name (state.write_ms already exists), so any change is another work "+
			"item's and belongs in its own commit with spec.md — as ADR 0054 was", got)
	}

	var found bool

	for _, name := range observability.AllEventNames() {
		if string(name) == "state.write_ms" {
			found = true
		}
	}

	if !found {
		t.Error("state.write_ms is not in AllEventNames(); the WriteRecorder seam has nothing " +
			"to report into, and a later work item would have to add the name")
	}
}

// TestTheWriteRecorderSeesEveryWrite is the seam R6 will build the histogram on.
//
// Each write reports its outcome exactly once, through the seam rather than through
// a `slog` line. S-12.3 is enforced by `observability.EventAttributes` having no
// field a state document could be passed through, and routing around it with a
// `slog.Any` at this call site is the hole that type exists to prevent — so a test
// that checks the seam is wired is a test that checks the guarantee has a route.
func TestTheWriteRecorderSeesEveryWrite(t *testing.T) {
	_, writer, reader := openDatabase(t)

	clock := newFakeClock()

	var (
		mu       sync.Mutex
		observed int
		failures int
	)

	record := func(_ context.Context, campaignID int64, _ time.Duration, err error) {
		mu.Lock()
		defer mu.Unlock()

		observed++

		if campaignID != 1 {
			t.Errorf("the recorder saw campaign %d, want 1", campaignID)
		}

		if err != nil {
			failures++
		}
	}

	registry := realtime.NewRegistry(t.Context(), realtime.Config{
		Write:   writer,
		Read:    reader,
		Record:  record,
		Cadence: realtime.Cadence{Debounce: debounce, Now: clock.Now, After: clock.After},
	})

	t.Cleanup(func() {
		if err := registry.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("registry.Close() error = %v, want nil", err)
		}
	})

	state := mustOpen(t, registry, 1)

	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 2, HP: 2})

	clock.advance(debounce)
	clock.quiet(t)

	mu.Lock()
	seen := observed
	mu.Unlock()

	// The initial row and the coalesced flush. A test that asserted "at least one"
	// would pass against an implementation that reported nothing at all.
	if seen != 2 {
		t.Errorf("the recorder saw %d writes, want 2 (the initial row plus one flush)", seen)
	}

	if failures != 0 {
		t.Errorf("the recorder saw %d failures, want 0", failures)
	}
}

// buildState returns the snapshot of a campaign whose placements were created in
// the given order, each created and then mutated once.
//
// Two campaigns rather than two registries over one, because `store.Open` allows
// one database per process (ADR 0004) and because two *campaigns* is the honest
// comparison: the documents must be byte-identical, and the only thing that differs
// between them is the order the intents arrived in.
func buildState(
	t *testing.T,
	registry *realtime.Registry,
	campaignID int64,
	ids []realtime.PlacementID,
) realtime.Document {
	t.Helper()

	state := mustOpen(t, registry, campaignID)

	for _, id := range ids {
		mustCreate(t, state, id, realtime.Placement{MaxHP: 10, HP: 10})

		mustMutate(t, state, id, 1, func(*realtime.Placement) {})
	}

	return state.Snapshot()
}

// reversed returns ids in the opposite order, so a determinism test can build the
// same state two ways.
func reversed(ids []realtime.PlacementID) []realtime.PlacementID {
	out := make([]realtime.PlacementID, 0, len(ids))
	for _, id := range slices.Backward(ids) {
		out = append(out, id)
	}

	return out
}
