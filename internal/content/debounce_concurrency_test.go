package content_test

// Race coverage for the settle filter: events arriving *during* debounce and
// *during* the confirmation, and the one field the filter's whole correctness rests
// on.
//
// # What the existing tests do not reach
//
// `debounce_test.go` is thorough about what the filter must do with a page and says
// nothing about the two things that only exist when time and events interleave:
//
//   - **`Touch` is only ever called from one goroutine.** Every existing test drives
//     it from the test goroutine, one write at a time, with sleeps between. The
//     filter's own doc says "`Touch` and `Moved` are called from the watcher's event
//     goroutine" — true in production — and then makes a concurrency claim about
//     `mu` anyway: "Safe for concurrent use… every piece of mutable state is behind
//     one mutex that is never held across a `stat`, a `sink` call, or a log line."
//     **Nothing removes that mutex.** `d.pending` is one map, and `pageLocked`,
//     `armLocked`, `scheduleLocked`, `Moved` and `takeDue` all write it. A `Touch`
//     without the lock is a concurrent map write — a runtime fatal, not a wrong
//     answer, which is the best kind of bug to have and the kind most likely to be
//     absent from a suite.
//   - **No test manufactures an event *between the two samples*.** That is the
//     window `debounce.go` calls "the last place a write could slip in unnoticed",
//     and closing it is spread over three lines — `armLocked` clearing the phase and
//     the first sample, `takeDue` dropping the stale queue entry, and `decide`'s own
//     ticket check for an event arriving inside the `stat`. None of them is named by
//     any test, and removing `armLocked`'s clearing is invisible to every assertion
//     in `debounce_test.go`.
//   - **`Close` racing `Touch` is untested.** `run`'s deferred teardown sets
//     `stopped = true` and then `pending = nil`; `Touch`'s only defence is
//     `if d.stopped { return }` **inside the same critical section**. Without it, a
//     `Touch` after `Close` assigns into a nil map and panics.
//
// # How the timing assertions here are sound
//
// `AGENTS.md` is emphatic that a watcher test asserting a clock is asserting a claim
// about its own goroutine, and this file follows the rule rather than the letter of
// it. Every arrival is judged by **entitlement** (the fixture's `entitled`) rather
// than by "did nothing happen for N milliseconds", and where a duration *is* the
// claim it is a **lower bound with no slack**:
//
//	settle ≥ touch + QuietPeriod + SampleInterval
//
// A lower bound is sound in one direction only, and that is the direction that
// matters. `beginLocked` schedules the second sample at `now + SampleInterval`, and a
// `time.Timer` cannot fire early, so a correct filter settles no sooner than that
// and a machine that deschedules the scheduler only pushes it later. A filter that
// believed its pre-touch first sample settles at `touch + QuietPeriod` instead —
// `SampleInterval` short of the bound, on every machine, which is arithmetic rather
// than a sleep.
//
// The **plain** silence windows that do appear (`fx.silent`) all come after the
// writers have stopped, which is exactly the condition `silent`'s own comment names
// as the sound one: "Only sound where no background writer is running: it is a
// wall-clock claim, and a wall-clock claim about a page that something else is
// writing is a claim about that writer's scheduling." Where a writer *is* running,
// `silentWhileWriting` is used instead.

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/observability"
)

// allDelivered reports whether every owed path has settled.
//
// A helper rather than an inline loop because the collection loop's condition is
// read far more easily when the loop's body is only about what it is collecting.
func allDelivered(owed map[string]bool) bool {
	for _, delivered := range owed {
		if !delivered {
			return false
		}
	}

	return true
}

// stuckWriterReported reports whether the filter logged a stable-read timeout.
//
// **Its own function rather than a call to the fixture's `hasEvent`.** `hasEvent`
// takes the event name, and every caller in the package now passes the same
// constant — which `unparam` correctly reads as a parameter that carries no
// information. Widening a shared helper to fix that would mean editing a file this
// work item does not own; a name-free local helper says the same thing without
// changing anyone else's signature.
func stuckWriterReported(fx *settleFixture) bool {
	return strings.Contains(fx.logs.String(),
		`"event":"`+string(observability.EventContentStableReadTimeout)+`"`)
}

// wideSample is the sample interval the confirmation-within-a-confirmation tests
// use.
//
// Six hundred milliseconds, and the number is not tuning — it is the width of the
// window the test needs to manufacture. The requirement is "an event arriving
// between the two samples must prevent the settle", and the only way to express that
// is a second sample far enough away that a deliberate touch lands comfortably inside
// the gap. The cost is that each such test takes about as long as this number, which
// is why there is one of them and not five.
const wideSample = 600 * time.Millisecond

// TestConcurrentWritersAcrossManyCampaignsSettleOnlyWholePages is the pending map
// under concurrent writers, and the settle-budget pressure they put on each other.
//
// Every existing settle test has one writer. This one has four, each owning a
// campaign and each cycling through that campaign's four paths, so `d.pending`,
// `d.queue` and `d.ticket` are written by four goroutines and read by the scheduler
// running alongside all four. The claims, all of which are about the *page* rather
// than about a count:
//
//   - Every settle is a whole page. `pageVersion` grows with the version, so a read
//     that caught a write mid-flight cannot end in the terminator and cannot match
//     any `pageVersion`. This is the assertion that would fail if two campaigns'
//     pending entries collided.
//   - Every settle names a `(campaign, slug, path)` triple the fixture actually
//     wrote, and the campaign id agrees with the slug the fixture assigned it. That
//     pair is the one `pendingKey` holds together, and `pendingPage`'s comment says
//     why: keying by path alone would let one campaign's writer reset another's
//     timer.
//   - Every settle is **entitled**, which is `entitled`'s judgement rather than this
//     file's: a filter that emitted without waiting out its quiet period would fail
//     here, and a machine that paused a writer long enough to entitle an early
//     settle would not.
//
// The writers stop at a fixed instant and the test then collects **one** arrival per
// outstanding path, which is the convergence claim: a filter that lost an entry
// leaves a page unsent forever, and `queue.push`/`pop`/`rebuild` are where it would
// be lost.
func TestConcurrentWritersAcrossManyCampaignsSettleOnlyWholePages(t *testing.T) {
	t.Parallel()

	const (
		campaigns = 4
		paths     = 4
		writers   = campaigns
		versions  = 25
	)

	slugs := make([]string, campaigns)
	for index := range slugs {
		slugs[index] = fmt.Sprintf("crowd-%d", index)
	}

	fx := newSettleFixture(t, settleTimings, slugs...)

	// The path each writer cycles through. Two writers never name the same pending
	// key — the campaign id is half of it — so the map is contended across sixteen
	// entries rather than fought over one.
	rel := func(writer, step int) string {
		return fmt.Sprintf("page-%d.md", (writer+step)%paths)
	}

	var written sync.WaitGroup

	written.Add(writers)

	for writer := range writers {
		go func() {
			defer written.Done()

			for version := range versions {
				fx.write(slugs[writer], rel(writer, version), pageVersion(version+1))
			}
		}()
	}

	written.Wait()

	// Every path owes at least one settle, and the arrivals arrive in whatever order
	// the scheduler reaches them — so they are collected by key rather than read in
	// sequence. `next()` is bounded by `wantArrived`, which is a ceiling on a failure
	// rather than a cost on a success, so a filter that lost an entry fails here with
	// a message naming what is still missing.
	owed := make(map[string]bool, campaigns*paths)

	for _, slug := range slugs {
		for step := range paths {
			owed[eventKey(slug, rel(0, step))] = false
		}
	}

	// A cap well above the number owed, so an over-settling filter is reported as
	// extra arrivals rather than as a hang.
	for range campaigns * paths * 3 {
		if allDelivered(owed) {
			break
		}

		seen := fx.next()
		key := eventKey(seen.change.Slug, seen.change.Path)

		if _, expected := owed[key]; !expected {
			t.Errorf("settled %s, which no writer wrote", seen.change)

			continue
		}

		owed[key] = true

		if seen.change.CampaignID != fx.ids[seen.change.Slug] {
			t.Errorf("settled campaign id = %d for slug %q, want %d; `pendingKey` holds "+
				"the id and the path together precisely so one campaign cannot answer "+
				"for another", seen.change.CampaignID, seen.change.Slug,
				fx.ids[seen.change.Slug])
		}

		if seen.err != nil {
			t.Fatalf("reading %s after its change settled: %v", seen.change.Path, seen.err)
		}

		checkWholePage(t, seen.raw)

		entitled, since := fx.entitled(seen)
		if !entitled {
			t.Errorf("a change settled %s after its last event, which is shorter than "+
				"the %s quiet period: %s", since, quietPeriod, seen.change)
		}
	}

	for key, delivered := range owed {
		if !delivered {
			t.Errorf("no change settled for %q; a filter that lost a pending entry leaves "+
				"its page unsent until the periodic rescan", strings.ReplaceAll(key, "\x00", " in "))
		}
	}

	// Nothing settles after the writers stop, which is sound here because no writer
	// is running: there is no goroutine the machine can pause to create an entitlement.
	fx.silent(wantSilence)
}

// TestAnEventArrivingBetweenTheTwoSamplesPreventsTheSettle is the ticket check,
// and it is the invariant the file's whole reason for existing.
//
// `debounce.go` states the gap as the last one in the design: "Everything it
// touches is under the lock and nothing in it does I/O, so the gap between 'the
// samples agreed' and the page is out of the map is a few instructions with no
// syscall in it. That gap is the last place a write could slip in unnoticed, and
// the ticket check is what closes it."
//
// So: arm a page, let its **first** sample be taken, then deliver an event that
// changes nothing on disk — the size is identical, so the samples *agree*. A filter
// that emitted on agreement would settle here and report the bytes as of a sample
// that an event has already invalidated. The correct filter starts again.
//
// **Which lines do the work, because naming the wrong one is how this test would
// have been written to pass a mutation.** Not `decide`'s `page.ticket != ticket` —
// that one closes a far narrower window, the interval between `ticketOf` and
// `decide`, i.e. an event arriving *while the `stat` is in flight*. An event
// arriving between the two samples is handled earlier and by two other lines:
// `armLocked` clears the phase back to `phaseQuiet` and throws the first sample
// away, and `takeDue` drops the now-stale queue entry because its ticket no longer
// names the page's. Removing `decide`'s check **survives** this test, which is
// correct and worth knowing: the window that check closes is only reachable when an
// event lands inside a single `stat`, and the mutation is recorded as such rather
// than quietly dropped.
//
// The assertion is the **lower bound** the header describes, and it is what makes
// this test sound rather than a claim about a wall clock:
//
//	settle ≥ touch + QuietPeriod + SampleInterval
//
// A timer cannot fire early, so that bound holds for the correct filter on any
// machine; a filter that believed its pre-touch first sample settles at
// `touch + QuietPeriod` instead, which is `SampleInterval` short of the bound on
// every machine. A machine that pauses anything only widens the gap.
//
// The second half asserts the page was **not lost**: the stale entry is dropped from
// the queue but the pending page stays, because `Touch` queued a fresh deadline for
// it. A filter that dropped the page instead would leave it unsent until the
// periodic rescan, which is precisely the "stale index the rescan closes minutes
// later" outcome the settle budget exists to avoid.
func TestAnEventArrivingBetweenTheTwoSamplesPreventsTheSettle(t *testing.T) {
	t.Parallel()

	timing := content.SettleTimings{
		QuietPeriod:    quietPeriod,
		SampleInterval: wideSample,
		SettleBudget:   5 * time.Second,
	}

	fx := newSettleFixture(t, timing, "interlopers")
	const rel = "interlopers.md"

	fx.write("interlopers", rel, pageVersion(1))

	// **Fixture arrangement, not synchronisation**, and the margin is the point:
	// the first sample is due at `quietPeriod` (40ms) and the touch is at 200ms, so
	// the sample is five times overdue before the event arrives. There is no
	// observation of "the first sample was taken" available from outside the filter,
	// so the gap is made wide rather than asserted — the same trade
	// `TestSizeStableConfirmationGates` makes, for the same reason.
	time.Sleep(5 * quietPeriod)

	// An event that changes **nothing**: no write, no size change, only the event.
	// The samples will agree perfectly, which is what makes this the interesting
	// case — a filter that believed agreeing samples would settle here on the
	// strength of a sample the event has already invalidated.
	touchedAt := time.Now()

	fx.touch("interlopers", rel)

	seen := fx.next()
	fx.mustBeWhole(seen)

	// The lower bound, with no slack. See the header.
	settledAfter := seen.at.Sub(touchedAt)
	lowerBound := quietPeriod + wideSample

	if settledAfter < lowerBound {
		t.Fatalf("the page settled %s after the event that arrived between its samples, "+
			"which is less than the %s a fresh confirmation needs; it was believed from "+
			"the samples taken before the event (%s, %d bytes read)",
			settledAfter, lowerBound, seen.change, len(seen.raw))
	}

	// Entitled, so the second half of the file's rule holds: the settle really did
	// follow an event-free window rather than being tolerated.
	if entitled, since := fx.entitled(seen); !entitled {
		t.Errorf("the page settled %s after its last event, want at least the %s quiet "+
			"period: %s", since, quietPeriod, seen.change)
	}

	// One settle. A second would mean the filter believed the same page twice, and
	// the arrival above already removed it from the map.
	fx.silent(wantSilence)
}

// TestTheConfirmationRefusesToSettleOnASingleSample is the other half of that
// window, and it is here because the two mutations are independent: a suite holding
// only the test above would pass a filter that settles on its first sample, and one
// holding only this one would pass a filter that ignores the event between the
// samples.
//
// `SettleTimings.SettleBudget` documents why one sample cannot be a confirmation:
// "The second sample is the only thing in this file that asks the filesystem a
// question." `beginLocked` therefore re-arms rather than emitting.
//
// **A lower bound, not a silence window**, for the reason the header gives and
// because the alternative is not sound here: "no settle in the first
// `quietPeriod + sampleGap`" is a claim about the machine as much as about the
// filter, and a first sample taken 15ms late by a loaded runner under `-race` lands
// outside a 55ms window. The bound — `settle ≥ write + QuietPeriod +
// SampleInterval` — cannot be broken by slowness, only by a filter that did not wait.
//
// The default timings apply, so this costs one settle cycle and is by a wide margin
// the cheapest test in this file.
func TestTheConfirmationRefusesToSettleOnASingleSample(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "one-sample")
	const rel = "one-sample.md"

	armed := time.Now()

	fx.write("one-sample", rel, pageVersion(1))

	seen := fx.next()
	fx.mustBeWhole(seen)

	if version := seenVersion(t, seen.raw); version != 1 {
		t.Fatalf("settled version = %d, want 1", version)
	}

	waited := seen.at.Sub(armed)
	lowerBound := quietPeriod + sampleGap

	if waited < lowerBound {
		t.Fatalf("the page settled %s after its event, which is less than the %s one "+
			"sample plus one confirmation takes; a single `stat` is not a confirmation, "+
			"and this is what a filter that believed the first one looks like (%s, %d bytes)",
			waited, lowerBound, seen.change, len(seen.raw))
	}

	// Exactly one settle, and no stuck-writer signal for a page that simply settled.
	// Sound because nothing is writing: a page that settled is out of the map, and
	// there is no goroutine the machine can pause to re-arm it.
	fx.silent(wantSilence)

	if stuckWriterReported(fx) {
		t.Errorf("a page that settled on its second sample reported a stable-read timeout:\n%s",
			fx.logs.String())
	}
}

// TestTouchingDuringAndAfterCloseIsSafeAndSettlesNothing is `Close` against the
// event stream, and the half of it that is a *nil map*.
//
// `Debouncer.Close` "stops the scheduler and drops every path still pending", and
// `run`'s deferred teardown sets `stopped` and then nils both `pending` and `queue`
// inside one critical section. `Touch` reads `d.stopped` in the same critical
// section and returns without arming. **That pairing is the whole safety property**:
// a `Touch` that ran between the two assignments would write into a nil map, and a
// `Touch` that ran after them without the `stopped` check would do the same.
//
// So the test does both, in the order that makes them different:
//
//   - **During:** sixteen goroutines call `Touch` on a live page in a tight loop
//     while another goroutine closes the filter. A `Touch` that lost the race is
//     dropped, which is the documented behaviour, and a `Touch` that won must not
//     arm anything the closed scheduler will ever drain.
//   - **After:** the same sixteen goroutines call `Touch` once the `Close` has
//     returned, where the `stopped` check is the only thing standing between them
//     and an assignment into a nil map.
//
// The assertion that nothing settles is a plain silence window and is sound here:
// the only page pending was already dropped by `Close`, and nothing is being
// written. The page the touches name does not even exist on disk, which removes the
// removal-versus-upsert question entirely — a `Touch` for an absent path settles as
// a remove if it settles at all, and neither is owed after a close.
func TestTouchingDuringAndAfterCloseIsSafeAndSettlesNothing(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "closing-race")

	const (
		touchers = 16
		rounds   = 200
	)

	stop := make(chan struct{})

	var during sync.WaitGroup

	during.Add(touchers)

	for index := range touchers {
		go func() {
			defer during.Done()

			for range rounds {
				select {
				case <-stop:
					return
				default:
				}

				// `Touch` directly rather than through `fx.deliver`: this fixture's
				// event bookkeeping exists to make `entitled` sound, and an arrival is
				// not owed after a close — so recording events nobody asked about would
				// only make a later assertion read better than the situation deserves.
				fx.debouncer.Touch(int64(index+1), "closing-race",
					fmt.Sprintf("ghost-%d.md", index%4))
			}
		}()
	}

	// Let the touches be in flight before the close, or the whole test is the
	// "after" case twice over.
	time.Sleep(quietPeriod / 2)

	fx.debouncer.Close()
	close(stop)
	during.Wait()

	// The second half: sixteen goroutines touching a closed filter. A closed filter
	// is the crash case rather than the normal one, and `AGENTS.md`'s rule is that a
	// caller must not crash the process on the way out.
	var after sync.WaitGroup

	after.Add(touchers)

	var panics atomic.Int64

	for index := range touchers {
		go func() {
			defer after.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					panics.Add(1)

					t.Errorf("Touch() after Close panicked: %v; `run` nils the pending "+
						"map and only `if d.stopped { return }` keeps a late event out of it",
						recovered)
				}
			}()

			fx.debouncer.Touch(int64(index+1), "closing-race", "ghost.md")
		}()
	}

	after.Wait()

	if got := panics.Load(); got != 0 {
		t.Errorf("%d of %d post-close touches panicked", got, touchers)
	}

	// `Close` is idempotent and still waits for the goroutine it already stopped,
	// which is the property a deferred close in a shutdown path depends on.
	fx.debouncer.Close()

	// And nothing settled. Sound because there is no writer: the paths above were
	// never created, and the filter's scheduler is gone, so any arrival would be a
	// settle from a scheduler that should have stopped.
	fx.silent(wantSilence)

	if stuckWriterReported(fx) {
		t.Errorf("a filter that was closed reported a stable-read timeout:\n%s", fx.logs.String())
	}
}

// TestTheFilterRejectsAZeroLengthPageUnderConcurrentWriters is the settle budget
// under load, and it is here because ADR 0037's fix is the one branch that can be
// re-armed indefinitely — every other outcome leaves the pending map.
//
// The claim: "A zero-length sample is therefore **not stable**: the page is re-armed
// as though an event had arrived — the quiet period restarts… and the budget is *not*
// taken again, so the time a path may spend empty stays bounded." Two halves, and
// they are the reason the branch is written out rather than delegated to `armLocked`:
// the quiet period **is** taken again and the budget **is not**. A test that only
// checked "the page eventually settles whole" would pass against an implementation
// that re-took the budget on every re-arm, which bounds nothing at all.
//
// So: a path is created empty, sixteen writers then race to fill it, and the
// assertions are that it settles **once**, that it settles **whole** — never as an
// empty page — and that the whole thing finished inside a bounded number of budgets
// rather than unboundedly. The bound is asserted as a *lower* bound on what must be
// waited and an *upper* bound on the total, and both are generous: this test is
// about the shape of the re-arm, not about its latency.
func TestTheFilterRejectsAZeroLengthPageUnderConcurrentWriters(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "zero-race")
	const rel = "zero-race.md"

	// The created-but-unwritten shape (S-3.5): the event for the create is
	// delivered and the bytes are late.
	fx.write("zero-race", rel, "")

	const (
		writers = 16
		rounds  = 40
	)

	stop := make(chan struct{})

	var writing sync.WaitGroup

	writing.Add(writers)

	for index := range writers {
		go func() {
			defer writing.Done()

			version := 0

			for range rounds {
				select {
				case <-stop:
					return
				default:
				}

				version++
				fx.write("zero-race", rel, pageVersion(index*100+version))
			}
		}()
	}

	seen := fx.next()

	close(stop)
	writing.Wait()

	fx.mustBeWhole(seen)

	// Never an empty page, and never a torn one. `pageVersion` grows with the
	// version, so a read that caught one of the sixteen writers mid-write cannot
	// match any of them.
	if version := seenVersion(t, seen.raw); version < 100 {
		t.Errorf("settled version = %d, want one of the writers' (100 or more); the "+
			"zero-length re-arm let the empty page through", version)
	}

	// And the path was not settled twice out of the same burst. Every arrival after
	// the first is checked for entitlement rather than for absence, because a
	// machine that pauses a writer for a whole quiet period entitles a settle and
	// reporting that would be reporting the scheduler — the failure `AGENTS.md`
	// records twice for this very fixture.
	fx.silentWhileWriting(wantSilence)
}

// A sink that blocked forever would hold `Close` open, which is the one
// `ChangeSink` contract worth naming in a file about closing:
//
//	fx.debouncer.Close()
//
// is only bounded because the fixture's sink returns.
