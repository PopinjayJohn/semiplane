package realtime_test

// Failure row §13 "Crash mid-game": resume from the last debounced
// `campaign_state`, with the version sequence intact and no silent re-resolve.
//
// Two halves are covered elsewhere and recorded here rather than re-asserted:
//
//   - The single-generation crash floor — one debounced write, one unflushed
//     mutation, resume sees the debounced value — is `state_test.go`'s
//     `TestTheCrashFloorIsTheLastDebouncedState`. Repeating it would be a
//     second answer to the same question.
//   - "No silent re-resolve" is the ruleset gate's refusal, and it is
//     `ruleset_test.go`'s `TestMismatchRefusesResume`: a resume under a moved
//     fingerprint is refused with `ErrRulesetDrift` before any state opens,
//     naming the persisted version, the expected version, and the consequence.
//
// What neither pins is that the floor *advances*: after several debounces the
// resume must see the last one, not the first and not the in-memory tip — and
// the version sequence must continue from the floor with no gap and no reuse
// below it. That is this test. The crash itself is modelled the way `state.go`
// defines it: cancelling the registry's context stops the scheduler without a
// flush, which is what distinguishes a kill from a `Close`.

import (
	"context"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/realtime"
)

// TestTheCrashFloorAdvancesWithEveryDebounce is the multi-generation crash:
// three debounced generations, one unflushed mutation, then a kill.
//
// The floor must be the third generation — HP 15 at version 3 — and not the
// first write, which would mean debounces after the first never landed, and
// not the in-memory tip, which would mean the crash persisted something it did
// not. After the resume, the next mutation must stamp exactly floor+1: the lost
// version was never written, so reusing its number is correct, and skipping it
// would hand a reconnecting client a gap it cannot reconcile.
func TestTheCrashFloorAdvancesWithEveryDebounce(t *testing.T) {
	_, writer, reader := openDatabase(t)

	// A registry this test owns outright, so the crash is a cancel rather than
	// a cleanup — the documented difference between a kill and a shutdown.
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

	// Generation one, debounced.
	mustCreate(t, state, "p1", realtime.Placement{MaxHP: 30, HP: 30})

	clock.advance(debounce)
	clock.quiet(t)

	// Generations two and three, debounced together.
	mustMutate(t, state, "p1", 1, func(placement *realtime.Placement) {
		placement.HP = 20
	})
	mustMutate(t, state, "p1", 2, func(placement *realtime.Placement) {
		placement.HP = 15
	})

	clock.advance(debounce)
	clock.quiet(t)

	// Generation four, never flushed.
	mustMutate(t, state, "p1", 3, func(placement *realtime.Placement) {
		placement.HP = 5
	})

	crashedIncarnation := state.Incarnation()

	// The kill. No `Close`, no flush.
	cancel()
	clock.quiet(t)

	// A resume reads the table, so the table is the floor. A second registry
	// over the same database is the only way to state "what a restart loads" as
	// an observation rather than an inference.
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
		t.Fatal("the resumed state has no p1; the crash lost the debounced floor as well")
	}

	if restored.HP != 15 {
		t.Errorf("the resumed p1 is at %d hp, want 15: the floor is the last debounced "+
			"generation, not the first write (30) and not the unflushed tip (5)",
			restored.HP)
	}

	if restored.Version != 3 {
		t.Errorf("the resumed p1 is at version %d, want 3; the floor pins the version "+
			"as well as the values", restored.Version)
	}

	// The crash ended a loading, so a client presenting the crashed incarnation
	// must be told everything changed — a full snapshot, never a delta against
	// a loading that no longer exists. A delta here would reconcile the client
	// against a revision whose history died with the process.
	reconnect, err := reloaded.Resume(crashedIncarnation, reloaded.Revision())
	if err != nil {
		t.Fatalf("Resume() with the crashed incarnation error = %v, want nil", err)
	}

	if !reconnect.Changed {
		t.Error("Resume() with the crashed incarnation reported nothing changed; a " +
			"loading that died with the process cannot share a history with the new one")
	}

	if len(reconnect.Placements) != 1 {
		t.Errorf("Resume() with the crashed incarnation carried %d placements, want the "+
			"full snapshot of 1", len(reconnect.Placements))
	}

	// And the sequence continues from the floor: the next mutation stamps
	// exactly floor+1. The lost version 4 was never written, so its number is
	// free — and anything above 4 would be a gap no client can cross.
	next, err := reloaded.Mutate("p1", 3, func(placement *realtime.Placement) {
		placement.HP = 12
	})
	if err != nil {
		t.Fatalf("Mutate() after the resume error = %v, want nil", err)
	}

	if next.Version != 4 {
		t.Errorf("the first mutation after the resume stamped version %d, want 4; the "+
			"sequence continues from the floor it actually persisted", next.Version)
	}
}
