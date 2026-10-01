// The wiring assertion that the pipeline runs S-4.5's degraded mode.
//
// The supervisor was merged with its own tests covering every report case, and
// those are the right place for the policy. This file covers the one thing they
// cannot: that the composition root *starts* it. A supervisor that is constructed
// correctly, tested correctly, and never started is a subsystem whose tests are
// green and whose behaviour is absent — the hardest shape of defect to notice,
// because every assertion in the repository passes.

package main

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
)

// TestThePipelineRunsItsSupervisor is the assertion, and the one that would have
// failed had the wiring been left to the phase's last reader.
//
// It is deliberately not a test of the supervisor's behaviour — `supervisor_test.go`
// covers all five report cases with a scripted verifier — but of the *seam*: the
// pipeline holds a supervisor, and that supervisor was handed the watcher, the
// indexer and the signal surface. A pass built against no components can report
// nothing, because it has nothing to report about.
func TestThePipelineRunsItsSupervisor(t *testing.T) {
	t.Parallel()

	// `newInstance` already registers `fixtureSlug`, so this reuses that campaign
	// rather than registering a second one — two campaigns at one slug is a
	// uniqueness failure, and the fixture's own campaign is the one this test means.
	inst := newInstance(t)

	pipeline, _ := inst.assemble(t.Context(), []domain.Campaign{inst.campaign})

	if pipeline.supervisor == nil {
		t.Fatal("the pipeline holds no supervisor: S-4.5's degraded mode is " +
			"unimplemented, so a watch lost without an event stays lost for the " +
			"life of the process")
	}

	// The other half of the seam. A supervisor built against a nil watcher or a
	// nil indexer would construct successfully and then be unable to act on any
	// report, which is the same absence wearing a different hat.
	if pipeline.watcher == nil || pipeline.indexer == nil {
		t.Fatal("the pipeline's watcher or indexer is nil; the supervisor would " +
			"have nothing to reconcile and nothing to reindex")
	}
}

// TestThePipelineStopsItsSupervisor asserts the shutdown half.
//
// Not `t.Parallel`: `runtime.NumGoroutine` is process-wide, so a concurrent test
// starting its own watcher would make the measurement meaningless. The reason is
// in the comment because the reason is the test — `internal/content`'s own
// goroutine-leak test is non-parallel for the same reason, and two tests that
// measure the same process-wide counter and disagree are worse than one.
//
// A ticker that outlives its owner is not observable from inside the process it
// is in, which is exactly why it needs a measurement from outside.
func TestThePipelineStopsItsSupervisor(t *testing.T) {
	// `newInstance` already registers `fixtureSlug`; see the note in the test above.
	inst := newInstance(t)

	// A context this test owns, deliberately **not** `t.Context()`. The test
	// context is cancelled when the test returns, which is a second and
	// indistinguishable way for the supervisor's loop to stop — so a measurement
	// taken against it cannot tell "Close stopped it" from "the harness did".
	// Cancelling this one *after* the measurement leaves Close as the only
	// remaining exit, which is what makes the assertion mean anything.
	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(cancel)

	// Settled before the baseline is taken, because the baseline is the point:
	// this compares against the goroutines the process had *before* a pipeline
	// existed, not against the pipeline's own peak. Measuring from the peak would
	// accept a partial cleanup — three of four components stopped, one left
	// running — as success, and one leaked loop is exactly the thing being tested.
	settleGoroutines()

	before := runtime.NumGoroutine()

	pipeline, roots := inst.assemble(ctx, []domain.Campaign{inst.campaign})

	settleGoroutines()

	running := runtime.NumGoroutine()

	if running <= before {
		t.Fatalf("assembling the pipeline added no goroutines (before=%d "+
			"running=%d), so there is nothing for Close to stop and this test "+
			"cannot fail", before, running)
	}

	if err := pipeline.Close(); err != nil {
		t.Fatalf("close the pipeline: %v", err)
	}

	if err := roots.Close(); err != nil {
		t.Fatalf("close the content roots: %v", err)
	}

	settleGoroutines()

	after := runtime.NumGoroutine()

	// Back to the pre-pipeline count, not merely lower than the peak.
	if after > before {
		t.Errorf("the pipeline left %d goroutine(s) behind after Close "+
			"(before=%d running=%d after=%d); the supervisor's policy loop is "+
			"one of the candidates, and a loop nothing stopped keeps "+
			"re-verifying a tree nobody is watching for the life of the process",
			after-before, before, running, after)
	}

	// The fixture's own cleanup would close both a second time. That is the
	// property being relied on — `Close` is reached from a defer, a signal handler
	// and a test cleanup, so three of them arrive and two are late — and it is
	// asserted here rather than left to the cleanup's own failure mode.
	if err := pipeline.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// settleGoroutines waits for a pipeline's asynchronous goroutines to have
// started, so a count is not taken mid-startup.
func settleGoroutines() {
	time.Sleep(250 * time.Millisecond)
}
