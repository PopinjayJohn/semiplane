package content_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/observability"
)

// The fixtures here are the smallest ones that make the policy's *decisions*
// observable: a scripted verifier for the reports a filesystem will not produce,
// a recording indexer so a reindex is an assertion rather than a side effect,
// and the real `observability.Registry` so the gauge these tests assert on is the
// one `/readyz` renders.
//
// The watcher is the real one wherever a real one can produce the report, and the
// notifier seam is the only thing injected — for the same reason `watch_test.go`
// injects it and with the same comment: the watch limit cannot be exhausted for
// real without setting a sysctl for the whole machine, and a test that raises a
// system limit passes locally and fails on the machine where it is already
// exhausted.

// The timings these tests run on.
//
// Short enough that a policy pass happens within a test's patience, and — the
// point of the pair — *deliberately* in the same order the defaults are in
// (`RescanEvery` shorter than `VerifyEvery`), because a test that inverted them
// would pass against a supervisor whose two deadlines were swapped.
const (
	testVerifyEvery = 8 * time.Millisecond
	testRescanEvery = 2 * time.Millisecond
)

// supervisorDeadlines bounds how long a test waits for a pass to do something.
const supervisorDeadline = 30 * time.Second

// The identity of the one campaign these fixtures register and watch.
//
// Named because it appears in three places that must agree — the registry, the
// `WatchRoot`, and the scripted reports — and a literal in each is three places
// for a rename to leave one of them behind. The id is 1 for the same reason
// `newWatchFixture` uses sequential ids from 1: a report carrying the wrong
// campaign's id should be visible rather than accidentally right.
const (
	fixtureCampaign   = "gilded-cage"
	fixtureCampaignID = 1
)

// The second campaign in the one test that has two, which is reported about and
// never retained.
//
// Not a second fixture and not a parameter: it is only ever *reported* about,
// which is the shape that test needs — a campaign with no root is a campaign the
// registry does not hold, and giving it a directory would make it one the watcher
// watches. The id is 9 rather than 2 so that a campaign id leaking from the
// fixture's own campaign into this one is visible rather than coincidentally
// plausible.
const (
	siblingCampaign   = "unmounted-vault"
	siblingCampaignID = 9
)

// TestSupervisorTurnsLimitHitIntoRescanFallbackAndARescans is S-4.5's own
// sentence: watch-limit exhaustion falls back to a periodic full rescan.
//
// The limit is injected through the watcher's `Notifier` seam rather than
// provoked, for the reason `TestWatchLimitExhaustionIsAnError` gives at length,
// and the errno is EMFILE for the reason that test gives for it too — it is the
// errno `observability` names `watch_limit`, so the detail on the line is
// readable.
//
// The assertions are three, and the third is the one that makes it a policy
// rather than a log line: the campaign is reindexed, and it keeps being reindexed
// after the limit is still exhausted. A supervisor that emitted
// `watch.rescan_fallback` and reindexed once would satisfy S-4.5 for the length
// of one verify period and then stop being a fallback at all.
func TestSupervisorTurnsLimitHitIntoRescanFallbackAndARescans(t *testing.T) {
	t.Parallel()

	// `watchReports`: the report has to be the real watcher's, because the whole
	// claim is that *this watcher's* `Add` failures are what the policy reads.
	// A scripted `LimitHit` would be a test asserting against a report the test
	// wrote, and would pass against a supervisor wired to nothing.
	fx := newSupervisorFixtureWith(t, supervisorFixtureOptions{
		notifier:     &exhaustingNotifier{errno: syscall.EMFILE},
		watchReports: true,
	})

	startSupervisor(t, fx)

	record := awaitRecord(t, fx.events, "watch.rescan_fallback")
	if record.Level != slog.LevelWarn {
		t.Errorf("watch.rescan_fallback is warn, got %s", record.Level)
	}

	if record.Detail != "watch_limit" {
		t.Errorf("want detail=watch_limit for EMFILE, got %q", record.Detail)
	}

	if record.CampaignID != signalID(fixtureCampaignID) {
		t.Errorf("want the campaign named on the line, got %q", record.CampaignID)
	}

	// S-12.2: the error-level line is the watcher's `watch.add_failed`, and this
	// test would pass against a supervisor that downgraded it.
	if failed := awaitRecord(t, fx.events, "watch.add_failed"); failed.Level != slog.LevelError {
		t.Errorf("watch.add_failed is never below error (S-12.2), got %s", failed.Level)
	}

	awaitReindexes(t, fx, 1)

	// Periodic, not once: the fallback is a rescan that keeps running.
	awaitReindexes(t, fx, 5)
}

// TestSupervisorTurnsMissingIntoDegradedAndRecoveryClearsIt is S-4.5's other
// sentence: a missing content root marks the campaign degraded — and the gauge,
// not the log line, is the assertion, because `observability.Watch` tracks
// campaigns currently *in* a state and a counter that only ever rises is the
// half-resolved surface its own comment warns about.
//
// The report is scripted rather than produced, and the reason is a property of
// the filesystem rather than of this test: `Root` holds an **open descriptor**,
// so a content root deleted underneath it still walks and re-walks successfully,
// and the only way to get `VerifyReport.Missing` on Linux is for the walk itself
// to answer ENOENT. Deleting the directory and expecting a report is a test that
// would have failed against a correct watcher. The sibling case a real watcher
// *can* produce — a walk that fails — is covered by
// `TestSupervisorTurnsAFailedWalkIntoDegradedAndNeverRescans`.
//
// The recovery half matters as much as the degradation half: the gauge has to
// come back down on its own, or the only way to clear it is a process restart.
func TestSupervisorTurnsMissingIntoDegradedAndRecoveryCleitsIt(t *testing.T) {
	t.Parallel()

	fx := newSupervisorFixture(t)

	// Held, rather than scripted through a transition, so the gauge can be read
	// while it is *up*. At an eight-millisecond verify period a one-pass
	// transition is a value that rises and falls between two polls, and a test
	// that waited for it would be waiting for something it had already missed.
	fx.script.set(report(1, fixtureCampaign, missing()))

	startSupervisor(t, fx)

	awaitGauge(t, fx.registry, string(observability.EventWatchDegraded), 1)

	if record := awaitRecord(
		t,
		fx.events,
		"watch.degraded",
	); record.Detail != "content_root_missing" {
		t.Errorf("want detail=content_root_missing, got %q", record.Detail)
	}

	// No reindex while the root cannot be walked. `ReindexCampaign` is
	// walk-then-prune, so a rescan here would find no files and delete every row
	// the campaign has; a missing vault must cost the search index nothing.
	// Asserted after several passes rather than immediately, so it is not a race
	// with the first one.
	waitForPasses(t, fx.verifier, 5)

	if calls := fx.index.callsFor(fixtureCampaign); calls != 0 {
		t.Errorf("a degraded campaign must not be reindexed, got %d reindexes", calls)
	}

	// The root comes back — a new descriptor, which is the only thing that can
	// clear this state, and is `Registry.Open` on the registrar's behalf rather
	// than anything this policy does.
	fx.script.set(report(1, fixtureCampaign, kept(1)))

	awaitGauge(t, fx.registry, string(observability.EventWatchDegraded), 0)

	recovered := awaitRecord(t, fx.events, "watch.recovered")
	if recovered.CampaignID != signalID(fixtureCampaignID) {
		t.Errorf("want the recovery attributed to the campaign, got %q", recovered.CampaignID)
	}

	// And the recovery is also the moment to converge: whatever changed while the
	// root was unreadable is re-read now rather than at the next rescan.
	awaitReindexes(t, fx, 1)
}

// TestSupervisorTurnsAFailedWalkIntoDegradedAndNeverRescans is the same
// degradation reached through the real watcher and a real registry, because this
// is the form a real failure takes: the walk fails rather than the root being
// reported absent.
//
// The root's descriptor is closed behind the registry's back, which is the one
// thing this process can do to a confined walk that a filesystem will not do on
// demand. `VerifyReport.Missing` cannot be provoked this way at all — a `Root`
// holds an open descriptor, so a directory deleted underneath it still walks — and
// that is why this test covers the sibling case rather than the headline one.
func TestSupervisorTurnsAFailedWalkIntoDegradedAndNeverRescans(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	atomicWrite(t, dir, "Gate.md", "# Gate\n")

	registry := content.NewRegistry(content.RefuseSymlinks)

	root, err := registry.Open(fixtureCampaign, dir)
	if err != nil {
		t.Fatalf("open the content root: %v", err)
	}

	// `watchReports` and no scripted notifier: the report has to be the real
	// watcher's own, or the test would be asserting against a report this test
	// wrote — and a `WalkError` is the one report a fixture cannot honestly fake,
	// because producing one means breaking a descriptor.
	fx := newSupervisorFixtureWith(t, supervisorFixtureOptions{
		dir:          dir,
		roots:        registry,
		watchReports: true,
	})

	// Closed directly, which `Root.Close`'s own comment says only the registry
	// does, and which is exactly why this is a test rather than a production path:
	// a descriptor that has gone bad is the failure a `Missing` cannot express,
	// and the policy has to answer for it. The registry still holds the slug, so
	// the walk is consulted first and the detail names the walk — which is the
	// ordering assertion this test exists for.
	if err := root.Close(); err != nil {
		t.Fatalf("close the content root's descriptor: %v", err)
	}

	startSupervisor(t, fx)

	record := awaitRecord(t, fx.events, "watch.degraded")
	if record.Detail != "content_root_unwalkable" {
		t.Errorf("want detail=content_root_unwalkable, got %q", record.Detail)
	}

	awaitGauge(t, fx.registry, string(observability.EventWatchDegraded), 1)

	waitForPasses(t, fx.verifier, 5)

	if calls := fx.index.callsFor(fixtureCampaign); calls != 0 {
		t.Errorf("a campaign whose tree cannot be walked must not be reindexed, got %d", calls)
	}
}

// TestSupervisorReindexesDroppedAndIgnoresRefused is the two entries of the table
// that are not modes: a tree that moved is repaired once, and a refused symlink
// is S-4.4 doing what it is specified to do.
//
// The two subtests for `Dropped` differ in how the reports are produced, and the
// split is worth stating because it is not a test convenience. A real watcher over
// a real tree with a notifier that delivers no events reports `Dropped` on
// *every* pass after a silent removal: the watch set still holds the directory and
// nothing tells the watcher it went. That is correct behaviour and it is the
// watcher's, not this policy's — so the real-watcher subtest asserts that a
// dropped watch produces a repair and no mode, and the scripted subtest asserts
// the exact count, because only a scripted report sequence can decide how many
// reports there are.
func TestSupervisorReindexesDroppedAndIgnoresRefused(t *testing.T) {
	t.Parallel()

	t.Run("dropped is repaired, once per report", func(t *testing.T) {
		t.Parallel()

		// A subtree that exists when the watcher starts, so the startup scan
		// watches it, and a notifier that delivers no events, so nothing is ever
		// *told* the directory went. That is the shape S-4.5's re-verify exists
		// for: a watch the watcher still claims and the tree no longer has, which
		// a real fsnotify would have reported as a `Remove` and this cannot.
		fx := newSupervisorFixtureWith(t, supervisorFixtureOptions{
			notifier: &scriptedNotifier{
				events: make(chan fsnotify.Event),
				errors: make(chan error),
			},
			watchReports: true,
			prepare: func(t *testing.T, dir string) {
				t.Helper()

				lore := filepath.Join(dir, "lore")
				if err := os.MkdirAll(lore, 0o700); err != nil {
					t.Fatalf("mkdir the subtree: %v", err)
				}

				atomicWrite(t, lore, "Gate.md", "# Gate\n")
			},
		})

		fx.removeTree(t, fixtureCampaign, "lore")

		startSupervisor(t, fx)

		// The repair happens: a `Dropped` watch means the rows under the
		// directories that went are stale, and nothing else will re-read them.
		awaitReindexes(t, fx, 1)

		// And it is a **repair, not a mode**: no fallback, no degraded, so the
		// rescan deadline is never armed and the campaign does not become a
		// permanent rescan because of one tree move. This is the distinction the
		// table draws between `Dropped` and `LimitHit`, and a policy that treated
		// both as modes would leave every campaign that has ever had a directory
		// renamed in the fallback for the rest of the process.
		awaitGauge(t, fx.registry, string(observability.EventWatchRescanFallback), 0)
		awaitGauge(t, fx.registry, string(observability.EventWatchDegraded), 0)
	})

	t.Run("a clean report is not a repair", func(t *testing.T) {
		t.Parallel()

		// The other half of "one-off", asserted where it can be asserted exactly.
		// With a real watcher and a notifier that never delivers events, a removed
		// directory stays in the watch set and every pass reports `Dropped` again
		// — correct, and the watcher's business rather than this policy's. The
		// claim that a *single* `Dropped` report costs one reindex is therefore
		// made against a scripted report sequence, which is the only place the
		// number of reports is the test's to decide.
		fx := newSupervisorFixture(t)

		fx.script.script(
			// One pass with a dropped watch, then a healthy one.
			[]content.VerifyReport{report(1, fixtureCampaign, kept(2), dropped(1))},
			[]content.VerifyReport{report(1, fixtureCampaign, kept(1))},
		)

		startSupervisor(t, fx)

		awaitReindexes(t, fx, 1)

		waitForPasses(t, fx.verifier, 5)

		if got := fx.index.callsFor(fixtureCampaign); got != 1 {
			t.Errorf("a dropped watch is one repair, not a mode; got %d reindexes", got)
		}
	})

	t.Run("refused is not a repair and not a signal", func(t *testing.T) {
		t.Parallel()

		outside := t.TempDir()
		atomicWrite(t, outside, "Borrowed.md", "# Borrowed\n")

		fx := newSupervisorFixtureWith(t, supervisorFixtureOptions{
			notifier: &scriptedNotifier{
				events: make(chan fsnotify.Event),
				errors: make(chan error),
			},
			watchReports: true,
			prepare: func(t *testing.T, dir string) {
				t.Helper()

				// S-4.4: a symlink inside the content root, which the policy
				// refuses to descend into and the read path refuses to read.
				if err := os.Symlink(outside, filepath.Join(dir, "shortcut")); err != nil {
					t.Fatalf("symlink the directory: %v", err)
				}
			},
		})

		startSupervisor(t, fx)

		// The report really does carry the refusal, so the assertions below are
		// about a policy that saw one rather than about a tree that happened to
		// have nothing wrong with it.
		waitForRefusals(t, fx.watcher, 1)

		waitForPasses(t, fx.verifier, 5)

		if total := fx.index.total(); total != 0 {
			t.Errorf("a refused symlink is not stale rows, got %d reindexes", total)
		}

		for _, record := range fx.events.records() {
			switch record.Event {
			case "watch.degraded", "watch.rescan_fallback", "watch.recovered":
				t.Errorf("a refused symlink must be silent, got %s", record)
			}
		}

		awaitGauge(t, fx.registry, string(observability.EventWatchDegraded), 0)
	})
}

// TestSupervisorDistinguishesLimitHitFromAFailedDirectory is the one place the
// two `RescanFallback` details are told apart, and it is here because they are one
// reaction with two different instructions behind it: raise the watch limit, or
// find out why one directory refuses a watch.
//
// The reaction being identical is what S-4.5 specifies — the fallback is a
// periodic full rescan either way — and the detail is what an operator's alert
// matches on. A policy that reported `watch_limit` for a permission error would
// send them to raise a limit that is not exhausted, and one that reported
// `watch_add_failed` for ENOSPC would send them to a directory that is fine.
//
// Table-driven because the claim is the *pairing* between a report field and a
// detail, and a pairing asserted in two separate tests is a pairing somebody can
// change in one of them.
func TestSupervisorDistinguishesLimitHitFromAFailedDirectory(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		fields []func(*content.VerifyReport)
		want   string
	}{
		"limit hit alone": {
			fields: []func(*content.VerifyReport){limitHit},
			want:   "watch_limit",
		},
		"a directory failed alone": {
			fields: []func(*content.VerifyReport){addFailed("lore")},
			want:   "watch_add_failed",
		},
		// The ordinary case, and the one the table above has to get right: a
		// campaign whose every directory failed under an exhausted limit is
		// reported both ways, and the limit is the actionable one.
		"both, which is the ordinary case": {
			fields: []func(*content.VerifyReport){limitHit, addFailed("lore")},
			want:   "watch_limit",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fx := newSupervisorFixture(t)
			fx.script.set(report(1, fixtureCampaign, tc.fields...))

			startSupervisor(t, fx)

			record := awaitRecord(t, fx.events, "watch.rescan_fallback")
			if record.Detail != tc.want {
				t.Errorf("detail = %q, want %q", record.Detail, tc.want)
			}
		})
	}
}

// TestAMissingRootDoesNotStopAnything is S-4.5's "and the server still starts",
// asserted on the two things that phrase could mean: the other campaigns keep
// being reconciled, and the policy keeps running.
//
// The broken campaign's root is not even retained, which is the boot-time shape
// `campaignroots.Open` produces for a disk that is not mounted — and it is
// reported rather than returned, which is the only way the instance stays up.
func TestAMissingRootDoesNotStopAnything(t *testing.T) {
	t.Parallel()

	fx := newSupervisorFixture(t)

	fx.script.script(
		[]content.VerifyReport{
			// "no-content-root": a campaign this process cannot read at all. The
			// registry does not hold it, which is what `campaignroots` leaves
			// behind when a vault is not mounted.
			report(siblingCampaignID, siblingCampaign, kept(0)),
			// A healthy campaign whose tree moved under the watcher.
			report(1, fixtureCampaign, kept(1), dropped(1)),
		},
	)

	startSupervisor(t, fx)

	// The healthy campaign is repaired even though a sibling is unreadable.
	awaitReindexes(t, fx, 1)

	if calls := fx.index.callsFor(siblingCampaign); calls != 0 {
		t.Errorf("a campaign with no content root must not be reindexed, got %d", calls)
	}

	record := awaitRecord(t, fx.events, "watch.degraded")
	if record.CampaignID != signalID(siblingCampaignID) {
		t.Errorf("want the degraded campaign to be %d, got %q",
			siblingCampaignID, record.CampaignID)
	}

	if record.Detail != "content_root_not_retained" {
		t.Errorf("want detail=content_root_not_retained, got %q", record.Detail)
	}

	// One campaign in the state, and the gauge says so.
	awaitGauge(t, fx.registry, string(observability.EventWatchDegraded), 1)

	// And the loop is still running, which is the other half of "the server
	// still starts": a policy that stopped reconciling on the first broken
	// campaign would satisfy every assertion above.
	waitForPasses(t, fx.verifier, 5)
}

// TestSupervisorRetriesARepairThatDidNotConverge is the reason `repair` exists.
//
// A reindex that failed is not a repair, and a database that was briefly
// unavailable does not become available because the failure was dropped on the
// floor. The retry is bounded by the verify period — deliberately: this is a
// repair, not a hot loop — and the assertion is that it happens at all.
func TestSupervisorRetriesARepairThatDidNotConverge(t *testing.T) {
	t.Parallel()

	fx := newSupervisorFixture(t)

	fx.script.script(
		[]content.VerifyReport{report(1, fixtureCampaign, kept(1), dropped(1))},
	)

	// The first reindex fails; the second is not attempted until a pass later.
	fx.index.failNext()

	startSupervisor(t, fx)

	awaitReindexes(t, fx, 2)
}

// TestSupervisorCloseIsIdempotentAndLeaksNothing is the shutdown contract, and
// the only test in this file that is about resources rather than policy.
//
// Two leaks, asserted two ways. The **goroutine** is a process-wide count taken
// around a real supervisor over a real watcher, because that is the only
// measurement that catches a loop that never returns; the **timer** is asserted
// behaviourally, by counting verification passes after `Close` and waiting long
// enough for several periods — a `time.Ticker` that outlives its owner fires
// forever and no goroutine count would ever show it, because a tick is not a
// goroutine.
//
// The count is not taken with `t.Parallel`: this is a process-wide measurement,
// and a parallel test in this package is another goroutine that comes and goes
// while it is taken.
func TestSupervisorCloseIsIdempotentAndLeaksNothing(t *testing.T) {
	settledGoroutines(t)

	before := runtime.NumGoroutine()

	fx := newSupervisorFixture(t)

	fx.start()

	waitForPasses(t, fx.verifier, 3)

	// Three at once, and all of them waiting: shutdown reaches Close from a
	// deferred close, a signal handler and a test's cleanup, and every one of them
	// has to be a barrier rather than a race — "Close returned" must mean the
	// goroutine is gone for *the caller that returned*, not eventually.
	var closes sync.WaitGroup

	for range 3 {
		closes.Go(func() {
			fx.supervisor.Close()
		})
	}

	closes.Wait()

	// A cancelled context and a Close are the same shutdown; whichever a
	// composition root reaches first, the other is harmless. The fixture's cleanup
	// will call this again.
	fx.shutdown()

	passes := fx.verifier.passes()
	time.Sleep(20 * testVerifyEvery)

	if after := fx.verifier.passes(); after != passes {
		t.Errorf(
			"the verify timer is still firing after Close: %d passes in the window",
			after-passes,
		)
	}

	waitForGoroutines(t, before)
}

// TestSupervisorStartIsRefusedTwiceAndAfterClose is the wiring guard: a
// supervisor that is started twice would be two goroutines applying the same
// policy, and two of them would double every reindex and race every gauge.
func TestSupervisorStartIsRefusedTwiceAndAfterClose(t *testing.T) {
	t.Parallel()

	fx := newSupervisorFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := fx.supervisor.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}

	// Started on a *different* context, so the refusal is about the supervisor's
	// own state and not about a context that has already been cancelled.
	if err := fx.supervisor.Start(t.Context()); err == nil {
		t.Error("a second Start must be refused, got nil")
	}

	fx.supervisor.Close()

	if err := fx.supervisor.Start(t.Context()); err == nil {
		t.Error("Start after Close must be refused, got nil")
	}
}

// TestSupervisorRefusesMissingDependencies is the reason the checks are in Start
// rather than in the constructor: a policy with no watcher to verify is a watcher
// that has silently stopped watching, which is the one failure this file exists
// to make visible, and it has to be told about at the call site that caused it.
func TestSupervisorRefusesMissingDependencies(t *testing.T) {
	t.Parallel()

	for name, supervisor := range map[string]*content.Supervisor{
		"no registry": content.NewSupervisor(nil, &recordingReindexer{}, &scriptedVerifier{}, nil,
			content.SupervisorTimings{VerifyEvery: testVerifyEvery}),
		"no indexer": content.NewSupervisor(
			content.NewRegistry(content.RefuseSymlinks), nil, &scriptedVerifier{}, nil,
			content.SupervisorTimings{VerifyEvery: testVerifyEvery},
		),
		"no watcher": content.NewSupervisor(
			content.NewRegistry(content.RefuseSymlinks), &recordingReindexer{}, nil, nil,
			content.SupervisorTimings{VerifyEvery: testVerifyEvery},
		),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := supervisor.Start(t.Context()); err == nil {
				supervisor.Close()

				t.Errorf("Start() with %s must be refused, got nil", name)
			}
		})
	}
}

// TestSupervisorDefaultsAreUsedForUnsetTimings is the assertion that a zero
// `SupervisorTimings` is the *safe* answer rather than "never".
//
// A five-minute default is not observable from inside a test, so what is asserted
// is the property that makes the default reachable at all. An unset field must
// become a duration that is long, not a timer that fires in the past forever: a
// spin is visible as a pass count that runs away, which is the only way the
// difference between "the default is five minutes" and "the default is zero" can
// be told apart from out here. A *negative* one is the same claim for a computed
// duration, which is why `withDefaults` is non-positive rather than only zero.
func TestSupervisorDefaultsAreUsedForUnsetTimings(t *testing.T) {
	t.Parallel()

	for name, timing := range map[string]content.SupervisorTimings{
		"unset":        {},
		"negative":     {VerifyEvery: -time.Second, RescanEvery: -time.Second},
		"only verify":  {VerifyEvery: testVerifyEvery},
		"only rescan":  {RescanEvery: testRescanEvery},
		"rescan after": {VerifyEvery: testVerifyEvery, RescanEvery: time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// The recorder is what the supervisor is given, so the count is of the
			// calls the policy actually made rather than of a side channel.
			recorder := &passRecorder{WatchVerifier: &scriptedVerifier{}}
			index := &recordingReindexer{}

			supervisor := content.NewSupervisor(
				content.NewRegistry(content.RefuseSymlinks),
				index,
				recorder,
				nil,
				timing,
			)

			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			if err := supervisor.Start(ctx); err != nil {
				t.Fatalf("Start() error = %v, want nil", err)
			}

			// One pass, for every case: the first pass is immediate, and a timer
			// armed in the past would run it again at once and keep going.
			waitForPasses(t, recorder, 1)

			supervisor.Close()

			passes := recorder.passes()
			time.Sleep(50 * time.Millisecond)

			if after := recorder.passes(); after > passes+1 {
				t.Errorf("%s timing must not become a spin: %d extra passes in 50ms",
					name, after-passes)
			}
		})
	}
}

// supervisorFixtureOptions is what a few of these tests need to substitute.
//
// A struct rather than more positional parameters because three of the fields
// have perfectly good zero values that most tests want, and a constructor whose
// arguments are "notifier, prepare, roots, watchReports" is a constructor every
// call site has to read twice. The policy's periods are not among them: they are
// always the test ones, and the one test that needs the package defaults builds
// its own supervisor rather than asking the fixture for a five-minute wait.
type supervisorFixtureOptions struct {
	// notifier is the event source, or nil for a real fsnotify watcher.
	notifier content.Notifier

	// prepare runs against the campaign's content root before the watcher is
	// built, so that a tree the watcher must reconcile exists first.
	prepare func(t *testing.T, dir string)

	// roots replaces the registry, for a test that has to own one — which means
	// that test also has to supply `dir`, because the registry's root and the
	// watcher's `WatchRoot` have to name the same directory or the routing table
	// is watching a path nothing can read.
	roots *content.Registry

	// dir is the campaign's content root, for a test that created it. Nil means a
	// fresh `t.TempDir()`, which is what every test that is not provoking a
	// specific tree wants.
	dir string

	// watchReports makes the supervisor act on the **real watcher's** reports
	// rather than a scripted set, for the cases a filesystem can produce. The
	// recorder is still installed, so a test can count passes either way.
	watchReports bool
}

// supervisorFixture is a campaign's content root, a watcher over it, a
// supervisor driving it, and the three records the assertions read.
type supervisorFixture struct {
	// supervisor is the policy under test.
	supervisor *content.Supervisor

	// watcher is the real watcher, so that the reports are the watcher's own
	// whenever a real watcher can produce them.
	watcher *content.Watcher

	// roots is the content registry both the watcher and the supervisor read.
	roots *content.Registry

	// script is the scripted verifier, so a test can say what the next pass will
	// report. Always present, and unused when the fixture is driving the real
	// watcher's reports.
	script *scriptedVerifier

	// verifier is the pass recorder in front of whichever verifier the policy is
	// driving, because counting passes is what every test here needs and a test
	// that asserts on a *later* pass has no other way to know one happened.
	verifier *passRecorder

	// index is the recording reindexer.
	index *recordingReindexer

	// registry is the counter registry, so the gauges asserted on are the ones
	// `/readyz` renders.
	registry *observability.Registry

	// events captures the log lines, for the event name, the level and the
	// detail.
	events *logCapture

	// start starts the policy on a context the fixture owns, and shutdown stops it.
	//
	// Functions rather than a `ctx` field: `containedctx` is right that a struct
	// holding a context is a struct whose lifetime nobody can see, and a fixture is
	// the worst possible place for one. The context belongs to the policy; a test
	// reaches it only through these two.
	start    func()
	shutdown func()

	// campaign is the slug the fixture's one campaign is registered and watched
	// under, and the one `awaitReindexes` waits on.
	campaign string

	// dirs maps a campaign slug to its absolute content root.
	dirs map[string]string

	// options is what the fixture was built from, for the fields the body does
	// not need to read again.
	options supervisorFixtureOptions
}

// newSupervisorFixture is the common case: one campaign, one real watcher over a
// fresh directory, and a scripted verifier on top so the report is the test's to
// choose.
func newSupervisorFixture(t *testing.T) *supervisorFixture {
	t.Helper()

	return newSupervisorFixtureWith(t, supervisorFixtureOptions{})
}

// newSupervisorFixtureWith builds a fixture from its options.
//
// The verifier is always wrapped in a `passRecorder`, whether it is the scripted
// one or the real watcher, because counting passes is what every test here needs
// and a test that asserts on a *later* pass has no other way to know one
// happened. The policy's periods are always the test ones rather than the package
// defaults: a five-minute default is not observable from inside a test, and the
// one test that needs the defaults builds its own supervisor rather than asking
// the fixture to wait. They also keep the defaults' *order* — rescan shorter than
// verify — so the deadline arithmetic under test is the arithmetic production runs
// rather than an inversion the policy is not written for.
func newSupervisorFixtureWith(t *testing.T, options supervisorFixtureOptions) *supervisorFixture {
	t.Helper()

	// The directory the watcher is pointed at. A test that supplied one has
	// usually already opened it in a registry it owns, and a second `Open` of the
	// same slug would be refused — so the fixture only opens a root it made the
	// registry for.
	dir := options.dir
	if dir == "" {
		dir = t.TempDir()

		atomicWrite(t, dir, "Gate.md", "# Gate\n")
	}

	if options.prepare != nil {
		options.prepare(t, dir)
	}

	roots := options.roots
	if roots == nil {
		roots = content.NewRegistry(content.RefuseSymlinks)

		if _, err := roots.Open(fixtureCampaign, dir); err != nil {
			t.Fatalf("open the content root: %v", err)
		}
	}

	registry := observability.NewRegistry()
	events := newLogCapture()
	logger := slog.New(events)
	signals := observability.NewWatch(registry, logger)

	opts := []content.WatcherOption{}
	if options.notifier != nil {
		opts = append(opts, content.WithNotifier(options.notifier))
	}

	watchCtx, cancelWatch := context.WithCancel(context.Background())
	t.Cleanup(cancelWatch)

	watcher, err := content.NewWatcher(
		watchCtx,
		roots,
		[]content.WatchRoot{{CampaignID: fixtureCampaignID, Slug: fixtureCampaign, Dir: dir}},
		nil,
		signals,
		opts...,
	)
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}

	verifier := content.WatchVerifier(&scriptedVerifier{})
	if options.watchReports {
		verifier = watcher
	}

	// The recorder is what the *supervisor* is given, not merely what the test
	// holds: counting passes has to happen on the call the policy actually makes,
	// and a recorder hanging off the side would count nothing.
	recorder := &passRecorder{WatchVerifier: verifier}

	index := &recordingReindexer{}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fx := &supervisorFixture{
		supervisor: content.NewSupervisor(
			roots,
			index,
			recorder,
			signals,
			content.SupervisorTimings{
				VerifyEvery: testVerifyEvery,
				RescanEvery: testRescanEvery,
			},
		),
		watcher:  watcher,
		roots:    roots,
		script:   scripted(verifier),
		verifier: recorder,
		index:    index,
		registry: registry,
		events:   events,
		campaign: fixtureCampaign,
		dirs:     map[string]string{fixtureCampaign: dir},
		options:  options,
	}

	fx.start = func() {
		if err := fx.supervisor.Start(ctx); err != nil {
			t.Errorf("Start() error = %v, want nil", err)
		}
	}

	// Reports through `t`, so a shutdown failure is attributed to the test rather
	// than to whichever one runs next. The leak test needs the same shutdown
	// without a `t`, and it says so where it does it.
	fx.shutdown = func() {
		fx.supervisor.Close()
		cancel()

		if err := watcher.Close(); err != nil {
			t.Errorf("close watcher: %v", err)
		}
	}

	t.Cleanup(func() {
		// Idempotent by construction, which is why a test that already called
		// `shutdown` does not double-report: `Close` returns its first result to
		// every caller and a closed watcher is not an error the second time.
		fx.shutdown()
	})

	return fx
}

// signalID renders a campaign id the way the observability surface carries it.
//
// A test asserting `CampaignID == "1"` reads as a magic string; this reads as the
// conversion `EventAttributes.CampaignID` performs, which is a conversion rather
// than a change of type — the same reasoning `watch.go`'s own `signalID` gives.
func signalID(campaignID int64) string {
	return strconv.FormatInt(campaignID, 10)
}

// scripted returns the scripted verifier a fixture is driving, or nil when it is
// driving the real watcher's reports instead.
func scripted(verifier content.WatchVerifier) *scriptedVerifier {
	scripted, _ := verifier.(*scriptedVerifier)

	return scripted
}

// startSupervisor starts the policy through the fixture.
func startSupervisor(t *testing.T, fx *supervisorFixture) {
	t.Helper()

	fx.start()
}

// removeTree deletes a directory inside a campaign's content root, which is what
// makes the watch set stale without the watcher being told.
func (f *supervisorFixture) removeTree(t *testing.T, slug, rel string) {
	t.Helper()

	if err := os.RemoveAll(filepath.Join(f.dirs[slug], rel)); err != nil {
		t.Fatalf("remove %s from %s: %v", rel, slug, err)
	}
}

// report builds a verification report for one campaign, from field mutators, and
// healthy by default: one watch kept and nothing wrong.
//
// The mutators rather than a struct literal at each call site, because
// `VerifyReport` has ten fields and a literal means every test that sets two of
// them has to name the other eight — which is how a test whose assertion is about
// one thing turns into a diff of the whole report.
func report(
	campaignID int64,
	slug string,
	fields ...func(*content.VerifyReport),
) content.VerifyReport {
	built := content.VerifyReport{CampaignID: campaignID, Slug: slug, Kept: 1}

	for _, field := range fields {
		field(&built)
	}

	return built
}

// kept sets the number of watches the reconciliation kept.
func kept(count int) func(*content.VerifyReport) {
	return func(r *content.VerifyReport) { r.Kept = count }
}

// dropped sets the number of watches the reconciliation had to drop.
func dropped(count int) func(*content.VerifyReport) {
	return func(r *content.VerifyReport) { r.Dropped = count }
}

// missing reports that the content root is not there.
func missing() func(*content.VerifyReport) {
	return func(r *content.VerifyReport) { r.Missing = true }
}

// limitHit reports that a watch could not be added because the limit was
// exhausted.
func limitHit(r *content.VerifyReport) {
	r.LimitHit = true
}

// addFailed reports that a root-relative directory could not be watched.
func addFailed(rel string) func(*content.VerifyReport) {
	return func(r *content.VerifyReport) { r.Failed = append(r.Failed, rel) }
}

// scriptedVerifier hands out a set of reports, either one per pass from a script
// or whatever the test last set.
//
// Both, because the cases under test are of two kinds and neither kind is served
// by the other alone. A *script* is what a transition test needs, because the
// campaign has to be reported as missing and then as readable and the test has to
// know both happened in that order. A *set* is what a gauge test needs, because a
// gauge has to be observed while it is held: a scripted one-pass transition at an
// eight-millisecond period is a value that is up and down before a poll can see
// it, and a test that waits for a value it has already missed is a test that
// fails for a reason that has nothing to do with the policy.
//
// An unscripted verifier reports the healthy case — what a watcher over a vault
// nobody has touched reports, and what a test that says nothing about the tree is
// asserting against. A default rather than an empty report, for the reason
// `Reverify` gives: an absent report is indistinguishable from a timer that
// stopped firing.
type scriptedVerifier struct {
	mu       sync.Mutex
	scripted [][]content.VerifyReport
	current  []content.VerifyReport
	seen     int
}

// script sets the per-pass reports, the last of which then repeats.
func (v *scriptedVerifier) script(passes ...[]content.VerifyReport) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.scripted = passes
	v.current = nil
}

// set makes every subsequent pass report exactly these.
func (v *scriptedVerifier) set(reports ...content.VerifyReport) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.scripted = nil
	v.current = reports
}

func (v *scriptedVerifier) Reverify(context.Context) []content.VerifyReport {
	v.mu.Lock()
	defer v.mu.Unlock()

	if len(v.scripted) == 0 {
		if v.current != nil {
			return v.current
		}

		return []content.VerifyReport{report(1, fixtureCampaign, kept(1))}
	}

	// The last entry repeats, so a test that only scripts the interesting pass
	// still gets a stable state afterwards rather than an empty report that would
	// look like every campaign disappearing.
	index := min(v.seen, len(v.scripted)-1)
	v.seen++

	return v.scripted[index]
}

// passRecorder counts the passes a verifier has been asked for.
//
// The wrapper rather than a field on `scriptedVerifier` because the same counting
// is needed around a *real* watcher: the tests that assert on a later pass have
// no other way to know one has happened.
type passRecorder struct {
	content.WatchVerifier

	mu    sync.Mutex
	count int
}

func (r *passRecorder) Reverify(ctx context.Context) []content.VerifyReport {
	reports := r.WatchVerifier.Reverify(ctx)

	r.mu.Lock()
	r.count++
	r.mu.Unlock()

	return reports
}

func (r *passRecorder) passes() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.count
}

// reindexCall is one recorded `ReindexCampaign` call.
type reindexCall struct {
	slug       string
	campaignID int64
}

// recordingReindexer records what it was asked to converge, and can be made to
// fail a fixed number of times.
//
// A recorder rather than a `*content.Indexer` over a real store, because what is
// under test is *when* the policy converges, and a real indexer over a real vault
// would make every assertion about timing into an assertion about how fast a
// filesystem is.
type recordingReindexer struct {
	mu    sync.Mutex
	calls []reindexCall
	fails int
}

func (r *recordingReindexer) ReindexCampaign(
	_ context.Context,
	slug string,
	campaignID int64,
) (content.IndexReport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, reindexCall{slug: slug, campaignID: campaignID})

	if r.fails > 0 {
		r.fails--

		return content.IndexReport{}, errors.New("the database is unavailable")
	}

	return content.IndexReport{Found: 1, Written: 1}, nil
}

// failNext makes the next reindex fail, and only the next one.
func (r *recordingReindexer) failNext() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.fails = 1
}

func (r *recordingReindexer) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.calls)
}

func (r *recordingReindexer) callsFor(slug string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	found := 0

	for _, call := range r.calls {
		if call.slug == slug {
			found++
		}
	}

	return found
}

// awaitRecord waits for a line with the given event name.
func awaitRecord(t *testing.T, capture *logCapture, event string) logRecord {
	t.Helper()

	deadline := time.Now().Add(supervisorDeadline)

	for {
		if record := capture.find(event); record != nil {
			return *record
		}

		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s\ncaptured: %v",
				supervisorDeadline, event, capture.rendered())
		}

		time.Sleep(watchPoll)
	}
}

// awaitGauge waits for a counter's current value to be exactly want.
//
// The gauge rather than the log line, because that is the claim: `Watch` tracks
// campaigns *in* a state, so an event that fired once and a state that is still
// held look identical on the log and completely different here.
func awaitGauge(t *testing.T, registry *observability.Registry, name string, want int64) {
	t.Helper()

	deadline := time.Now().Add(supervisorDeadline)

	for {
		counter, registered := registry.Counter(name)
		if !registered {
			t.Fatalf("%s is not registered, so it is not on /readyz", name)
		}

		if got := counter.Snapshot().Current; got == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("%s gauge is %d, want %d", name, got, want)
		}

		time.Sleep(watchPoll)
	}
}

// awaitReindexes waits for the fixture's campaign to have been reindexed at least
// count times.
//
// The campaign comes from the fixture rather than a parameter, so that a test
// waiting on a reindex cannot name a campaign the fixture does not have — the
// failure mode of a slug parameter is a test that waits thirty seconds for a
// campaign nothing will ever report. The test with a second campaign reads that
// one's count through `callsFor`, which is a bare read rather than a wait.
func awaitReindexes(t *testing.T, fx *supervisorFixture, count int) {
	t.Helper()

	deadline := time.Now().Add(supervisorDeadline)

	for {
		if got := fx.index.callsFor(fx.campaign); got >= count {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %d reindexes of %s, got %d",
				supervisorDeadline, count, fx.campaign, fx.index.callsFor(fx.campaign))
		}

		time.Sleep(watchPoll)
	}
}

// waitForRefusals waits for the watcher's own reconciliation to have counted at
// least count refused symlinks, so a test asserting that a refusal produces no
// signal has first established that a refusal was reported.
func waitForRefusals(t *testing.T, watcher *content.Watcher, count int) {
	t.Helper()

	deadline := time.Now().Add(supervisorDeadline)

	for {
		refused := 0

		for _, rep := range watcher.Reverify(t.Context()) {
			refused += rep.Refused
		}

		if refused >= count {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for the watcher to report %d refusals, got %d",
				supervisorDeadline, count, refused)
		}

		time.Sleep(watchPoll)
	}
}

// waitForPasses waits for the verifier to have been asked at least count times.
func waitForPasses(t *testing.T, verifier *passRecorder, count int) {
	t.Helper()

	if !waitForPassesNoFatal(verifier, count) {
		t.Fatalf("timed out after %s waiting for %d verification passes, got %d",
			supervisorDeadline, count, verifier.passes())
	}
}

// waitForPassesNoFatal is waitForPasses for a goroutine, where t.Fatalf would not
// end the test and would instead be attributed to whichever test runs next.
func waitForPassesNoFatal(verifier *passRecorder, count int) bool {
	deadline := time.Now().Add(supervisorDeadline)

	for {
		if verifier.passes() >= count {
			return true
		}

		if time.Now().After(deadline) {
			return false
		}

		time.Sleep(watchPoll)
	}
}

// settledGoroutines waits for the process's goroutine count to stop moving, so a
// baseline is not taken while an earlier test's cleanup is still in flight.
func settledGoroutines(t *testing.T) {
	t.Helper()

	last := runtime.NumGoroutine()
	stable := 0

	deadline := time.Now().Add(supervisorDeadline)

	for {
		time.Sleep(10 * time.Millisecond)

		got := runtime.NumGoroutine()
		if got == last {
			stable++

			if stable >= 3 {
				return
			}
		} else {
			last = got
			stable = 0
		}

		if time.Now().After(deadline) {
			t.Fatalf("the process's goroutine count never settled, last saw %d", last)
		}
	}
}

// waitForGoroutines waits for the process's goroutine count to fall back to at
// most want, which is how "Close returned" is proved to mean "nothing of mine is
// still running".
func waitForGoroutines(t *testing.T, want int) {
	t.Helper()

	deadline := time.Now().Add(supervisorDeadline)

	for {
		got := runtime.NumGoroutine()
		if got <= want {
			return
		}

		if time.Now().After(deadline) {
			names := make([]string, 0, got)
			for i := range got {
				names = append(names, goroutineHeader(i))
			}

			t.Fatalf("%d goroutines after Close, want at most %d\n%s",
				got, want, strings.Join(names, "\n"))
		}

		time.Sleep(watchPoll)
	}
}

// goroutineHeader renders the top of a goroutine's stack, so a leak names itself.
func goroutineHeader(index int) string {
	buf := make([]byte, 1<<16)
	buf = buf[:runtime.Stack(buf, true)]

	lines := strings.Split(string(buf), "\n\n")
	if index >= len(lines) {
		return fmt.Sprintf("goroutine %d: <no stack>", index)
	}

	first, _, _ := strings.Cut(lines[index], "\n")

	return first
}
