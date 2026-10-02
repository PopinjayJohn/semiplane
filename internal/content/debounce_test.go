package content_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/observability"
)

// Partial-write safety, driven through real files and a real clock.
//
// Everything here is the real filesystem and the real scheduler: the properties
// under test are properties of *time* — a timer that resets, two `stat` samples
// that disagree, a writer that stops — and a fixture that controlled time would
// be asserting against itself. What is injected instead is only the *scale*: the
// durations, which are the one thing a test may choose without changing what is
// being claimed.
//
// A clock interface was rejected rather than not considered. This component's
// entire job is waiting, so a fake clock would double its API to remove the only
// waiting it does, and every assertion here is a bounded wait for an event from
// another goroutine — which is the form of synchronisation that does not flake,
// because it fails on a deadline rather than guessing at a sleep.

const (
	// quietPeriod is longer than one settle cycle's worth of nothing, so a
	// filter that failed to reset its timer would emit inside a burst.
	quietPeriod = 40 * time.Millisecond

	// sampleGap is the gap between a confirmation's two samples. Well above the
	// scheduling jitter of a loaded CI machine running the race detector, and
	// well below the quiet period so a settle costs 55ms rather than seconds.
	sampleGap = 15 * time.Millisecond

	// settleBudget is short enough that a test which exhausts it does not pay a
	// long wait, and long enough that the retries leading to it are several
	// samples rather than one.
	settleBudget = 150 * time.Millisecond

	// wantArrived bounds every wait for something that must happen. Generous, so
	// that a slow machine fails on the assertion rather than on the clock.
	wantArrived = 10 * time.Second

	// wantSilence is how long a test watches for something that must not happen.
	//
	// Several times a full settle cycle (quiet + 2 × sample), so that a filter
	// which emitted one cycle early is caught with a wide margin rather than a
	// lucky one.
	wantSilence = 400 * time.Millisecond
)

// settleTimings are the timings every test uses unless it is specifically about
// one of the three durations.
var settleTimings = content.SettleTimings{
	QuietPeriod:    quietPeriod,
	SampleInterval: sampleGap,
	SettleBudget:   settleBudget,
}

// The terminator every generated page ends in.
//
// A constant rather than a literal at each use, because its whole job is to be
// the same string everywhere: a read that caught a write mid-flight cannot end
// in it, and that is the only way this suite can tell a truncated parse from a
// complete one that happens to be short.
const pageTerminator = "TERMINATED"

// pageVersion is one complete version of a page.
//
// The body grows with the version, so successive writes differ in size — a fixed
// size would make a truncated read of the previous version byte-identical to a
// complete read of this one, and the suite's central assertion would be
// vacuous.
func pageVersion(version int) string {
	return "---\ntitle: v" + strconv.Itoa(version) +
		"\n---\n" + strings.Repeat("x", 64+version*41) + "\n" + pageTerminator + "\n"
}

// checkWholePage asserts that raw is exactly one whole version of the page.
//
// Byte equality against `pageVersion` for the version its own front matter
// declares, which is stronger than "it parses" and stronger than "it ends in the
// terminator": a page assembled from the tail of one write and the head of
// another cannot pass, and neither can a page that is short by a byte. The
// version is read out of the bytes rather than passed in, so a caller cannot
// assert the wrong expectation by construction.
func checkWholePage(t *testing.T, raw []byte) {
	t.Helper()

	doc := content.Parse(raw, nil)
	if doc.FrontMatter.Err != nil {
		t.Fatalf("the settled page's front matter did not parse: %v\n%s", doc.FrontMatter.Err, raw)

		return
	}

	version, err := strconv.Atoi(strings.TrimPrefix(doc.FrontMatter.Title, "v"))
	if err != nil {
		t.Fatalf("the settled page's title %q is not a version title", doc.FrontMatter.Title)

		return
	}

	if want := pageVersion(version); string(raw) != want {
		t.Fatalf(
			"the sink read a page that is not one whole version\n got %d bytes\nwant %d bytes\nbody: %q",
			len(raw),
			len(want),
			raw,
		)
	}
}

// syncBuffer is a log sink the scheduler goroutine and the test goroutine share.
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

// Write implements io.Writer for the slog handler.
//
// `bytes.Buffer.Write` never fails, and the error is the interface's rather than
// this buffer's, so it is returned unchanged rather than wrapped into something a
// caller would then have to unwrap.
func (b *syncBuffer) Write(chunk []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	written, err := b.buffer.Write(chunk)
	if err != nil {
		return written, fmt.Errorf("append to the test log buffer: %w", err)
	}

	return written, nil
}

// String returns everything logged so far.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.String()
}

// arrival is one settled change together with what was on disk when it arrived.
type arrival struct {
	change content.Change
	raw    []byte
	err    error

	// at is when the sink ran, which is when the filter finished believing the
	// path. The entitlement checks below are questions about this instant's
	// distance from the last event for the same path, so it cannot be inferred
	// from the arrival itself.
	at time.Time
}

// settleFixture is one Debouncer over real content roots, one campaign per slug,
// with a sink that reads the file the change names.
//
// The sink reads rather than recording a path, because the guarantee is about
// bytes: S-5.2 keys the render cache on a hash of what the *reader* got, so a
// settle is only safe if a read taken at that moment is a whole page.
type settleFixture struct {
	t         *testing.T
	ids       map[string]int64
	dirs      map[string]string
	roots     *content.Registry
	debouncer *content.Debouncer
	arrivals  chan arrival
	logs      *syncBuffer

	// events is when this fixture last delivered an event, per path. It exists so
	// that an arrival can be asked whether the filter was entitled to make it,
	// which is the question the tests below were really asking and could not
	// previously express. See `entitled`.
	eventsMu sync.Mutex
	events   map[string]time.Time
}

// newSettleFixture opens one content root per slug and a Debouncer over them.
//
// The roots are registered before the Debouncer so that cleanup closes the
// Debouncer first: t.Cleanup is LIFO, and a settle still in flight at teardown
// must not outlive the roots it stats.
func newSettleFixture(t *testing.T, timing content.SettleTimings, slugs ...string) *settleFixture {
	t.Helper()

	fx := &settleFixture{
		t:        t,
		ids:      make(map[string]int64, len(slugs)),
		dirs:     make(map[string]string, len(slugs)),
		roots:    content.NewRegistry(content.RefuseSymlinks),
		arrivals: make(chan arrival, 4096),
		logs:     &syncBuffer{},
		events:   make(map[string]time.Time),
	}

	for index, slug := range slugs {
		dir := filepath.Join(t.TempDir(), slug)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create the content root for %s: %v", slug, err)
		}

		if _, err := fx.roots.Open(slug, dir); err != nil {
			t.Fatalf("open the content root for %s: %v", slug, err)
		}

		fx.ids[slug] = int64(index + 1)
		fx.dirs[slug] = dir
	}

	t.Cleanup(func() {
		if err := fx.roots.Close(); err != nil {
			t.Errorf("close the content roots: %v", err)
		}
	})

	handler := slog.NewJSONHandler(fx.logs, &slog.HandlerOptions{Level: slog.LevelDebug})
	watch := observability.NewWatch(observability.NewRegistry(), slog.New(handler))

	fx.debouncer = content.NewDebouncer(t.Context(), fx.sink, fx.roots, watch, timing)

	t.Cleanup(fx.debouncer.Close)

	return fx
}

// sink is the ChangeSink: it records the change and reads the file.
func (fx *settleFixture) sink(_ context.Context, change content.Change) {
	recorded := arrival{change: change, at: time.Now()}

	root, err := fx.roots.Get(change.Slug)
	if err == nil {
		var target *content.Target
		target, err = root.At(change.Path)
		if err == nil {
			recorded.raw, err = target.ReadFile()
		}
	}

	recorded.err = err
	fx.arrivals <- recorded
}

// deliver reports an event for a path and records when it was reported.
//
// Every fixture method that reaches the debouncer goes through here, which is
// what makes `entitled` sound: an event the fixture did not record is an event
// the entitlement arithmetic cannot account for, and a test that quietly missed
// one would pass for the wrong reason.
func (fx *settleFixture) deliver(slug, rel string) {
	fx.eventsMu.Lock()
	fx.events[eventKey(slug, rel)] = time.Now()
	fx.eventsMu.Unlock()

	fx.debouncer.Touch(fx.ids[slug], slug, rel)
}

// deliverMove reports a watcher-identified move and records both of its ends.
//
// Both, because `Moved` arms the destination and drops the source: the
// destination's deadline is what an arrival about it would be measured against,
// and the source's last event is what makes a removal of it legible.
func (fx *settleFixture) deliverMove(slug, oldRel, newRel string) {
	now := time.Now()

	fx.eventsMu.Lock()
	fx.events[eventKey(slug, oldRel)] = now
	fx.events[eventKey(slug, newRel)] = now
	fx.eventsMu.Unlock()

	fx.debouncer.Moved(fx.ids[slug], slug, oldRel, newRel)
}

// eventKey is one path in one campaign, as the fixture addresses it.
func eventKey(slug, rel string) string {
	return slug + "\x00" + rel
}

// entitled reports whether the filter was entitled to emit this arrival, and how
// long the path had been event-free when it did.
//
// **This is the property the timing-sensitive tests were asserting by assumption
// and are now able to assert directly.** S-4.3 is a conditional: *if* a path has
// gone without an event for the quiet period *and* two samples agree, *then* it
// settles. A page being written continuously therefore never satisfies the
// antecedent, and a settle during such a burst is a defect. But "continuously" is
// a claim about a **test-owned goroutine**, and a goroutine can be descheduled for
// longer than any quiet period — under `-race`, on a throttled CI runner, for a
// GC cycle or a slice of steal time. When that happens the antecedent *is*
// satisfied, the settle is the specified behaviour, and a test that treats the
// arrival as a failure is reporting the scheduler rather than the filter.
//
// The window measured runs from the last event for **this path** to the instant
// the sink ran. It is a lower bound on what the filter waited, because the filter
// also spends a sample interval confirming between its first sample and the
// emission, and may have re-armed more than once. So `false` is sound — the filter
// emitted without its quiet period, which is the defect — while `true` means only
// that it was entitled, never that it was prompt.
func (fx *settleFixture) entitled(seen arrival) (bool, time.Duration) {
	fx.eventsMu.Lock()
	last, known := fx.events[eventKey(seen.change.Slug, seen.change.Path)]
	fx.eventsMu.Unlock()

	if !known {
		// An arrival for a path this fixture never reported an event for. It
		// cannot be `Moved` (that names a destination the fixture does deliver
		// events for) and cannot be explained by a stalled writer, so it is
		// reported as unentitled rather than guessed about.
		return false, 0
	}

	since := seen.at.Sub(last)

	return since >= quietPeriod, since
}

// write puts a page in a campaign's tree and reports the event the watcher would
// deliver for it.
func (fx *settleFixture) write(slug, rel, body string) {
	fx.t.Helper()

	full := filepath.Join(fx.dirs[slug], filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		fx.t.Fatalf("create the directory for %s: %v", rel, err)
	}

	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		fx.t.Fatalf("write %s: %v", rel, err)
	}

	fx.deliver(slug, rel)
}

// writeQuietly puts a page in the tree and reports **no** event for it.
//
// The whole point of the size-stable confirmation: a file can change while no
// event says so, and a filter that only ever reacts to events cannot tell.
func (fx *settleFixture) writeQuietly(slug, rel, body string) {
	fx.t.Helper()

	full := filepath.Join(fx.dirs[slug], filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		fx.t.Fatalf("create the directory for %s: %v", rel, err)
	}

	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		fx.t.Fatalf("write %s: %v", rel, err)
	}
}

// tornWrite replaces a page with two writes and reports an event for each.
//
// The shape a writer that is not atomic produces: the file is truncated and
// holds half a page, and only then does it hold all of it. The gap is short —
// deliberately, because S-4.3 does not promise to survive a writer that stops
// mid-write for longer than the quiet period, and a fixture that asserted that
// it did would be asserting something the requirement does not say.
func (fx *settleFixture) tornWrite(slug, rel, body string) {
	fx.t.Helper()

	full := filepath.Join(fx.dirs[slug], filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		fx.t.Fatalf("create the directory for %s: %v", rel, err)
	}

	handle, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		fx.t.Fatalf("open %s for a torn write: %v", rel, err)
	}

	half := len(body) / 2

	if _, err := handle.WriteString(body[:half]); err != nil {
		fx.t.Fatalf("write the first half of %s: %v", rel, err)
	}

	// Arranging the torn state, not synchronising on it: the filter must not
	// settle inside this window, and the assertion that proves it is made from
	// the arrival channel afterwards.
	time.Sleep(time.Millisecond)

	fx.deliver(slug, rel)

	if _, err := handle.WriteString(body[half:]); err != nil {
		fx.t.Fatalf("write the second half of %s: %v", rel, err)
	}

	if err := handle.Close(); err != nil {
		fx.t.Fatalf("close %s: %v", rel, err)
	}

	fx.deliver(slug, rel)
}

// stalledWrite truncates a page in place, produces **no** event for the
// truncation, and is then descheduled before its first write.
//
// The shape of a writer stopped between two syscalls, and it is the one case
// `tornWrite` cannot reach: `open(O_TRUNC)` empties the file and the `write` that
// refills it is a separate call, so the file spends an interval existing, being a
// page by name, and holding nothing — with an armed deadline over it and no event
// to restart the quiet period. `tornWrite` calls `Touch` between its two writes
// and therefore never leaves that state unobserved.
//
// `stall` is how long the writer is descheduled. Longer than a quiet period plus
// a sample interval is what puts **both** of the confirmation's samples inside the
// empty window, and shorter than the settle budget is what makes the writer slow
// rather than stuck — which is the distinction the filter has to be able to make.
func (fx *settleFixture) stalledWrite(slug, rel, body string, stall time.Duration) {
	fx.t.Helper()

	full := filepath.Join(fx.dirs[slug], filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		fx.t.Fatalf("create the directory for %s: %v", rel, err)
	}

	handle, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		fx.t.Fatalf("open %s for a stalled write: %v", rel, err)
	}

	// Arranging the empty window, not synchronising on it: the filter must not
	// settle inside it, and the assertion that proves it is made from the arrival
	// channel afterwards.
	time.Sleep(stall)

	if _, err := handle.WriteString(body); err != nil {
		fx.t.Fatalf("write %s: %v", rel, err)
	}

	if err := handle.Close(); err != nil {
		fx.t.Fatalf("close %s: %v", rel, err)
	}

	fx.deliver(slug, rel)
}

// touch reports an event for a path that already exists.
func (fx *settleFixture) touch(slug, rel string) {
	fx.deliver(slug, rel)
}

// remove deletes a file and reports the event the watcher would deliver.
func (fx *settleFixture) remove(slug, rel string) {
	fx.t.Helper()

	full := filepath.Join(fx.dirs[slug], filepath.FromSlash(rel))
	if err := os.Remove(full); err != nil {
		fx.t.Fatalf("remove %s: %v", rel, err)
	}

	fx.deliver(slug, rel)
}

// mkdir creates a directory in a campaign's tree and reports its event.
func (fx *settleFixture) mkdir(slug, rel string) {
	fx.t.Helper()

	if err := os.MkdirAll(
		filepath.Join(fx.dirs[slug], filepath.FromSlash(rel)),
		0o700,
	); err != nil {
		fx.t.Fatalf("create the directory %s: %v", rel, err)
	}

	fx.deliver(slug, rel)
}

// next waits for one settled change.
func (fx *settleFixture) next() arrival {
	fx.t.Helper()

	select {
	case seen := <-fx.arrivals:
		return seen
	case <-time.After(wantArrived):
		fx.t.Fatalf("no settled change within %s", wantArrived)

		return arrival{}
	}
}

// silent asserts that nothing settles within the window.
//
// Only sound where no background writer is running: it is a wall-clock claim, and
// a wall-clock claim about a page that something else is writing is a claim about
// that writer's scheduling. Use `silentWhileWriting` where one is.
func (fx *settleFixture) silent(window time.Duration) {
	fx.t.Helper()

	select {
	case seen := <-fx.arrivals:
		fx.t.Fatalf("a change settled that should not have: %s, %d bytes read",
			seen.change, len(seen.raw))
	case <-time.After(window):
	}
}

// silentWhileWriting asserts that nothing settles in the window **that the filter
// was not entitled to settle**, which is the property S-4.3's first mechanism
// provides and the property a burst test exists to check.
//
// It replaces a plain silence window in the tests that run a writer on their own
// goroutine, and the difference is the whole fix. A plain window assumes the
// writer never paused for a quiet period; that assumption is about the *test*, it
// is not enforceable — any goroutine can be descheduled for longer than 40 ms —
// and when it fails the filter has settled correctly and the test has reported
// the scheduler. See `entitled`.
//
// An entitled arrival neither fails the test nor ends the window: the rest of the
// window is still worth watching, so the loop continues and a later *unentitled*
// arrival is still a failure. What a stalled run gives up is coverage, and it says
// so on the log; what it never does is report a defect that is not there.
func (fx *settleFixture) silentWhileWriting(window time.Duration) {
	fx.t.Helper()

	timer := time.NewTimer(window)
	defer timer.Stop()

	tolerated := 0

	for {
		select {
		case seen := <-fx.arrivals:
			entitled, since := fx.entitled(seen)

			if !entitled {
				fx.t.Fatalf(
					"a change settled %s after its last event, which is shorter than the %s "+
						"quiet period: %s, %d bytes read. A page being written must not settle "+
						"at all while it is being written",
					since, quietPeriod, seen.change, len(seen.raw),
				)
			}

			tolerated++
		case <-timer.C:
			if tolerated > 0 {
				fx.t.Logf(
					"%d arrival(s) tolerated: each followed an event-free window of at least "+
						"the %s quiet period, so this machine paused the writer and S-4.3 "+
						"required the settle. The rest of the window was still asserted",
					tolerated, quietPeriod,
				)
			}

			return
		}
	}
}

// awaitEvent waits for one logged event with the given name and returns its line.
//
// A poll with a deadline, not a sleep: the event is produced by the scheduler
// goroutine, and the only question is whether it arrives inside the budget. The
// log line rather than the counter because the line carries the event name and
// its attributes, which is what S-12.3 is about.
//
// `premise` is the writer whose continuity the awaited event depends on, and it
// may be nil. When it is not, a pause long enough to satisfy S-4.3's quiet period
// ends the wait **immediately and by name**: without it, a writer this machine
// descheduled produces a full `wantArrived` wait and then a message about a
// missing event, which points at the filter when the cause was the fixture.
func (fx *settleFixture) awaitEvent(name string, premise *gapRecorder) string {
	fx.t.Helper()

	deadline := time.Now().Add(wantArrived)
	needle := `"event":"` + name + `"`

	for {
		for line := range strings.SplitSeq(fx.logs.String(), "\n") {
			if strings.Contains(line, needle) {
				return line
			}
		}

		if premise != nil {
			if longest := premise.longest(); longest >= quietPeriod {
				fx.t.Fatalf(
					"the writer went %s between its steps, which is at least the %s quiet period, "+
						"so the premise this test rests on was void: the page stopped moving, S-4.3 "+
						"settled it, and no %s event was owed. That is a property of the machine, "+
						"not of the filter; the log held:\n%s",
					longest,
					quietPeriod,
					name,
					fx.logs.String(),
				)
			}
		}

		if time.Now().After(deadline) {
			fx.t.Fatalf(
				"no %s event within %s; the log held:\n%s",
				name,
				wantArrived,
				fx.logs.String(),
			)

			return ""
		}

		time.Sleep(2 * time.Millisecond)
	}
}

// gapRecorder measures the longest interval a background writer left between two
// of its own steps.
//
// It exists for writers that produce **no** events, where `entitled` cannot help:
// the stuck-writer test appends a byte every few milliseconds without ever calling
// `Touch`, so the only way to know its premise still holds is to have watched it.
type gapRecorder struct {
	mu           sync.Mutex
	previous     time.Time
	longestSoFar time.Duration
}

// mark records a step and folds the interval since the previous one into the
// longest. The first call establishes the baseline and counts for nothing.
func (g *gapRecorder) mark() {
	now := time.Now()

	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.previous.IsZero() {
		g.longestSoFar = max(g.longestSoFar, now.Sub(g.previous))
	}

	g.previous = now
}

// longest reports the biggest interval between two consecutive steps.
func (g *gapRecorder) longest() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.longestSoFar
}

// hasEvent reports whether an event with the given name has been logged.
func (fx *settleFixture) hasEvent(name string) bool {
	return strings.Contains(fx.logs.String(), `"event":"`+name+`"`)
}

// mustBeWhole asserts that an arrival was an upsert of one complete page.
func (fx *settleFixture) mustBeWhole(seen arrival) {
	fx.t.Helper()

	if seen.err != nil {
		fx.t.Fatalf("reading %s after its change settled: %v", seen.change.Path, seen.err)
	}

	if seen.change.Op != content.OpUpsert {
		fx.t.Fatalf("settled operation = %s, want upsert (%s)", seen.change.Op, seen.change)
	}

	checkWholePage(fx.t, seen.raw)
}

// A burst of writes produces one change, and the one change is a whole page.
//
// The cadence is the interesting part of the fixture, and it has to be shorter
// than the quiet period: an event restarts the timer, so a path whose writes keep
// arriving can never settle while they are arriving. The burst as a whole is
// nevertheless longer than one settle cycle, so a filter that armed its timer
// once and let it run would settle part-way through — into the middle of a torn
// write, and hand the index a page that is half of one write and half of another.
// With the restart, the burst settles once, at the end, on the last complete
// version.
//
// **The cadence is asserted, not assumed.** "Shorter than the quiet period" is a
// claim about the interval between two events, and the interval is produced by
// this test's own goroutine on a machine that may deschedule it — so the property
// is checked against the event log rather than inferred from a sleep. See
// `entitled` for why an inferred version of this assertion cannot be sound.
func TestBurstOfEventsSettlesOnceAndWhole(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "burst")
	const rel = "pages/burst.md"

	for version := 1; version <= 12; version++ {
		fx.tornWrite("burst", rel, pageVersion(version))

		// Half a sample interval, so the gap between two events is around 10ms
		// against a 40ms quiet period: four times the margin, and still a burst
		// of roughly 120ms in aggregate, which is longer than the one settle
		// cycle (quiet period plus two samples, 70ms) that a filter which armed
		// its timer once would have completed inside.
		time.Sleep(sampleGap / 2)
	}

	// The first arrival carries the property under test, and it is checked as
	// entitlement rather than as a version: a filter that armed once would settle
	// *here*, in the middle of the burst, with the path only a few milliseconds
	// past its last event.
	first := fx.next()
	fx.mustBeWhole(first)

	if entitled, since := fx.entitled(first); !entitled {
		t.Fatalf(
			"the burst settled %s after its last event, which is shorter than the %s quiet "+
				"period: %s, %d bytes read. A filter that armed its timer once and let it run "+
				"settles mid-burst, and this is that",
			since, quietPeriod, first.change, len(first.raw),
		)
	}

	// Which version the first arrival carries depends on the machine: if the
	// writer was paused for a whole quiet period part-way through the burst, the
	// filter was entitled to believe the version on disk at that point, and a
	// second settle follows the last event. What cannot vary is that some arrival
	// carries version 12, because the burst's final event is version 12's.
	if version := seenVersion(t, first.raw); version != 12 {
		final := fx.next()
		fx.mustBeWhole(final)

		if entitled, since := fx.entitled(final); !entitled {
			t.Fatalf(
				"the burst settled %s after its last event, which is shorter than the %s quiet "+
					"period: %s, %d bytes read",
				since, quietPeriod, final.change, len(final.raw),
			)
		}

		if settled := seenVersion(t, final.raw); settled != 12 {
			t.Fatalf(
				"the burst settled version %d and then %d, want the last one written (12)",
				version, settled,
			)
		}
	}

	// Past the last event there is nothing left to write, so a plain silence
	// window is sound here and asserts what it says: the burst settled once.
	fx.silent(wantSilence)
}

// seenVersion reads the version out of a settled page's own front matter.
func seenVersion(t *testing.T, raw []byte) int {
	t.Helper()

	doc := content.Parse(raw, nil)

	version, err := strconv.Atoi(strings.TrimPrefix(doc.FrontMatter.Title, "v"))
	if err != nil {
		t.Fatalf("the settled page's title %q is not a version title", doc.FrontMatter.Title)
	}

	return version
}

// Every settled read is a whole page, across every settle in a run of them.
//
// The absence asserted here is across *all* invocations rather than the last
// one: a filter that settles correctly for one burst and mid-write for the next
// is the failure this component exists to prevent, and a suite that checked only
// the final arrival would pass it.
func TestEverySettledReadIsAWholePage(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "cycles")
	const rel = "cycle.md"

	version := 0

	for cycle := range 4 {
		for range 3 {
			version++
			fx.tornWrite("cycles", rel, pageVersion(version))
		}

		// Let this cycle's burst settle on its own, so the loop below sees one
		// arrival per cycle rather than one for the whole run.
		time.Sleep(2 * quietPeriod)

		seen := fx.next()
		if seen.err != nil {
			t.Fatalf(
				"cycle %d: reading %s after its change settled: %v",
				cycle,
				seen.change.Path,
				seen.err,
			)
		}

		// The assertion that must hold for every invocation: the bytes the index
		// would hash are one whole version of the page.
		checkWholePage(t, seen.raw)

		if settled := seenVersion(t, seen.raw); settled != version {
			t.Fatalf("cycle %d: settled version = %d, want %d", cycle, settled, version)
		}

		// And exactly one per cycle: a second arrival here would mean a settle
		// that survived the events still arriving.
		fx.silent(2 * quietPeriod)
	}
}

// One write, one change — not one per event, and not two.
func TestSingleWriteSettlesExactlyOnce(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "single")
	const rel = "only.md"

	fx.write("single", rel, pageVersion(1))

	seen := fx.next()
	fx.mustBeWhole(seen)

	if seen.change.CampaignID != fx.ids["single"] {
		t.Fatalf("campaign id = %d, want %d", seen.change.CampaignID, fx.ids["single"])
	}

	if seen.change.Path != rel {
		t.Fatalf("path = %q, want %q", seen.change.Path, rel)
	}

	fx.silent(wantSilence)
}

// Two campaigns do not interfere: a path that is written continuously in one
// campaign does not delay or reorder another's settle.
//
// **The same relative path in both campaigns**, which is what makes this test
// discriminate. A pending map keyed by path alone would give the two campaigns one
// shared timer, and A's continuous stream of events would keep resetting it — so B
// would never settle at all, and the arrival that did eventually come would carry
// whichever campaign's slug the entry happened to hold.
//
// The load is a burst whose events are closer together than the quiet period, so
// campaign A's page is never settled for as long as the burst runs.
//
// **"Closer together than the quiet period" is checked, not assumed.** The interval
// between two of the noise generator's events is produced by a goroutine on a
// machine that may deschedule it, and this test has now failed here twice for that
// reason alone — once at `0 bytes read`, which is ADR 0037's zero-length settle,
// and once at `4196 bytes read`, which is `pageVersion(100)`, the very first
// version this generator writes. Both are the same event: the writer stopped for
// longer than the quiet period, the size held, and S-4.3 settled it correctly. So
// the assertion below is about entitlement rather than about the clock. See
// `entitled`.
func TestTwoCampaignsDoNotInterfere(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "noisy", "quiet-one")

	const rel = "shared.md"

	stop := make(chan struct{})
	finished := make(chan struct{})

	// The noise runs on its own goroutine so that the test can watch B settle
	// while A is still being written. It writes a distinct version each time so
	// that the file really is moving rather than being re-created identically.
	go func() {
		defer close(finished)

		for version := 100; ; version++ {
			select {
			case <-stop:
				return
			default:
			}

			fx.tornWrite("noisy", rel, pageVersion(version))
			time.Sleep(sampleGap / 2)
		}
	}()

	t.Cleanup(func() {
		close(stop)
		<-finished
	})

	fx.write("quiet-one", rel, pageVersion(1))

	seen := fx.next()
	fx.mustBeWhole(seen)

	if seen.change.Slug != "quiet-one" {
		t.Fatalf("settled slug = %q, want the quiet campaign", seen.change.Slug)
	}

	// Version 1 is B's only write. Anything from A's burst would be version 100
	// or later, so this also says the arrival came from the campaign that asked.
	if version := seenVersion(t, seen.raw); version != 1 {
		t.Fatalf("settled version = %d, want the quiet campaign's (1)", version)
	}

	// A's page settled nothing the filter was not entitled to settle. On a machine
	// that keeps the generator running this is the silence the comment above
	// describes; on one that does not, it is the arrivals the generator's pause
	// explains, and those are S-4.3 doing its job rather than a defect.
	fx.silentWhileWriting(wantSilence)
}

// The size-stable confirmation gates: a path whose size changes between the two
// samples is not settled yet.
//
// This is the half of S-4.3 that the timer cannot do. The file changes at 60ms —
// after the quiet period has expired and the first sample has been taken, and
// with **no** event to say so — and the second sample therefore disagrees. A
// filter with only the debounce would have settled at 40ms and read the ten-byte
// version.
//
// The sample interval is stretched to a quarter of a second for this one test, so
// that the mutation lands in a wide window between the two samples rather than in
// a fifteen-millisecond one. Everything else about the filter is unchanged, which
// is the point: the gate is what is under test.
func TestSizeStableConfirmationGates(t *testing.T) {
	t.Parallel()

	timing := content.SettleTimings{
		QuietPeriod:    quietPeriod,
		SampleInterval: 250 * time.Millisecond,
		SettleBudget:   2 * time.Second,
	}

	fx := newSettleFixture(t, timing, "sizing")
	const rel = "gated.md"

	fx.write("sizing", rel, pageVersion(1))

	// Fixture arrangement rather than synchronisation: the first sample lands at
	// about 40ms and the second at about 290ms, so a change made here lands
	// between them with a very wide margin. The assertion below is the arrival
	// channel, not this sleep.
	time.Sleep(2 * quietPeriod)

	// No Touch. This is the case a purely event-driven filter cannot see.
	fx.writeQuietly("sizing", rel, pageVersion(2))

	// Past the second sample, which must have found the size changed. A filter
	// without the gate settled at 40ms; one that gates settles at about 555ms,
	// once two samples in a row have agreed.
	fx.silent(quietPeriod + sampleGap)

	seen := fx.next()
	fx.mustBeWhole(seen)

	if version := seenVersion(t, seen.raw); version != 2 {
		t.Fatalf("settled version = %d, want the grown page (2)", version)
	}

	fx.silent(wantSilence)
}

// **Zero bytes is not a stable size.** It is the first state of every in-place
// write rather than a state any of them end in, so two samples that agree on it
// agree on a file that has not been written yet.
//
// The window this asserts is the one `tornWrite` cannot produce, and it is the
// one a sync client produces routinely (S-3.5): the writer truncates in place, its
// first `write` has not happened yet, and the truncation delivered no event
// because the event a watcher sees belongs to the *write*. `stalledWrite` holds
// the file at zero for a quiet period plus a sample interval, which puts both of
// the confirmation's samples inside the window — a filter that believed them would
// settle an upsert of **no bytes**, and hand the index a row whose `content_hash`
// is the digest of the empty string. The stall is shorter than the settle budget,
// so the writer is slow rather than stuck and the page must settle whole.
func TestAZeroLengthSampleIsNotASettledSize(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "zero-window")
	const rel = "zero-window.md"

	// The event a watcher delivers for the create, which arms the deadline.
	fx.write("zero-window", rel, "")

	fx.stalledWrite("zero-window", rel, pageVersion(1), quietPeriod+2*sampleGap)

	seen := fx.next()
	fx.mustBeWhole(seen)

	if version := seenVersion(t, seen.raw); version != 1 {
		t.Fatalf("settled version = %d, want 1", version)
	}

	// No stuck-writer signal either: the writer was slow, not stuck, and the
	// filter waited for it rather than reporting it.
	if fx.hasEvent(string(observability.EventContentStableReadTimeout)) {
		t.Errorf("a slow-but-live writer reported a stable-read timeout:\n%s", fx.logs.String())
	}

	fx.silent(wantSilence)
}

// The shape Obsidian Sync produces: the page appears as a directory entry and its
// bytes arrive afterwards. The event for the create is delivered; the bytes are
// late.
//
// Nothing may settle in the interval between the two, and that interval is
// asserted at half the settle budget rather than at the old threshold of a quiet
// period plus a sample interval — because half a budget is the honest lower bound
// on how patient the filter has to be to tell this writer from a blank note.
func TestACreatedButUnwrittenPageIsNotSettledEarly(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "syncing")
	const rel = "synced.md"

	fx.write("syncing", rel, "")

	fx.silent(settleBudget / 2)

	// The bytes land, and the page settles as one whole version.
	fx.write("syncing", rel, pageVersion(1))

	seen := fx.next()
	fx.mustBeWhole(seen)

	if version := seenVersion(t, seen.raw); version != 1 {
		t.Fatalf("settled version = %d, want 1", version)
	}

	fx.silent(wantSilence)
}

// The other half of the rule above, and the reason it is a rule about *time*
// rather than a refusal: a blank note is a page a user can legitimately create,
// so the filter must still settle one.
//
// A filter that treated zero bytes as "never settled" would drop it, and the
// periodic rescan (S-4.5) would index it minutes later — so the operator would
// see a page appear on its own and a `content.stable_read_timeout` line for every
// new empty note in the vault. A stuck-writer signal that fires on healthy input
// is an audit of nothing.
func TestAnEmptyPageSettlesAndIsReported(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "blank")
	const rel = "blank.md"

	fx.write("blank", rel, "")

	seen := fx.next()
	if seen.change.Op != content.OpUpsert {
		t.Fatalf("settled operation = %s, want upsert (%s)", seen.change.Op, seen.change)
	}

	if len(seen.raw) != 0 {
		t.Fatalf("an empty page settled %d bytes, want none", len(seen.raw))
	}

	// Reported, though: a path that holds zero bytes for a whole settle budget is
	// the only place a writer stalled between its `open` and its `write` leaves a
	// trace, and the settle is what says the trace was a slow writer rather than a
	// blank note.
	line := fx.awaitEvent(string(observability.EventContentStableReadTimeout), nil)
	if !strings.Contains(line, `"path":"`+rel+`"`) {
		t.Errorf("the settled empty page reported a timeout for another path: %s", line)
	}

	fx.silent(wantSilence)
}

// A path whose size never settles is reported and then dropped.
//
// The two halves are the point. `content.stable_read_timeout` must fire, because
// a writer that never stops is a thing an operator has to know about; and the sink
// must **not** receive the path, because "the confirmation gave up" and "the
// confirmation succeeded" are different findings and emitting on the first is how
// a stuck writer becomes silent data loss.
//
// **The writer's continuity is watched, not assumed.** This is the one test whose
// writer delivers no events at all, so `entitled` cannot speak for it: if this
// machine pauses the appender for a whole quiet period, the size holds across both
// samples, S-4.3 settles the page, and no timeout is owed. That is the third way
// this file's timing assumptions have been mistaken for properties of the filter,
// and `gapRecorder` turns it into a named failure at the moment it happens rather
// than a `wantArrived` wait followed by a complaint about a missing event.
func TestStableReadTimeoutWhenTheSizeNeverSettles(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "stuck")
	const rel = "stuck.md"

	fx.write("stuck", rel, pageVersion(1))

	stop := make(chan struct{})
	finished := make(chan struct{})

	var gaps gapRecorder

	// One byte every few milliseconds, and no event after the first: the quiet
	// period expires, the confirmation starts, and every pair of samples
	// disagrees. Exactly the "writer appears stuck" case.
	go func() {
		defer close(finished)

		full := filepath.Join(fx.dirs["stuck"], filepath.FromSlash(rel))

		for tick := 1; ; tick++ {
			select {
			case <-stop:
				return
			case <-time.After(sampleGap / 3):
			}

			handle, err := os.OpenFile(full, os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return
			}

			if _, err := handle.WriteString("x"); err != nil {
				handle.Close()

				return
			}

			handle.Close()

			gaps.mark()
		}
	}()

	t.Cleanup(func() {
		close(stop)
		<-finished
	})

	line := fx.awaitEvent(string(observability.EventContentStableReadTimeout), &gaps)
	if !strings.Contains(line, `"campaign_id":"`+strconv.FormatInt(fx.ids["stuck"], 10)+`"`) {
		t.Errorf("the timeout event carries no campaign_id: %s", line)
	}

	if !strings.Contains(line, `"path":"`+rel+`"`) {
		t.Errorf("the timeout event carries no path: %s", line)
	}

	// The confirmation gave up rather than succeeded: nothing was emitted, and
	// nothing will be while the file keeps moving without an event.
	//
	// **A plain silence window, deliberately, and not `silentWhileWriting`.** This
	// writer delivers no events after the first, so every arrival here is trivially
	// entitled — `entitled` measures the distance from the last *event*, and there
	// is none — and the entitlement form would tolerate every arrival and assert
	// nothing. Any arrival at all is the defect. The machine's ability to pause
	// this writer is caught above instead, by `awaitEvent`'s premise check, which
	// names the cause rather than waiting out the deadline.
	fx.silent(wantSilence)
}

// The converse: a path that settles reports no timeout, so the signal means
// something.
func TestASettledWriteReportsNoTimeout(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "calm")
	const rel = "calm.md"

	fx.write("calm", rel, pageVersion(1))
	fx.mustBeWhole(fx.next())

	if fx.hasEvent(string(observability.EventContentStableReadTimeout)) {
		t.Errorf("a settled write reported a stable-read timeout:\n%s", fx.logs.String())
	}
}

// Anything that is not a page produces no change at all — no upsert, no remove,
// and nothing in the log to say it was considered.
//
// `notes.md` is in the list deliberately and is the interesting one: it passes
// every name-based check this component makes, because it ends in `.md` and has
// no dot-component, and it is refused only because it is a directory. A filter
// that checked names alone would try to index it.
func TestNonPagesProduceNoChange(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "noise")
	const rel = "notes.md"

	// The dot-prefixed page and the two directories a vault is full of.
	fx.write("noise", ".hidden.md", pageVersion(1))
	fx.write("noise", ".obsidian/workspace.md", pageVersion(1))
	fx.write("noise", "node_modules/dep/readme.md", pageVersion(1))
	fx.mkdir("noise", "lore")
	fx.mkdir("noise", rel)

	// The names a writer stages through, each of which exists on disk because an
	// editor or a sync client had just created it.
	fx.write("noise", rel+".tmp", pageVersion(1))
	fx.write("noise", rel+".swp", pageVersion(1))
	fx.write("noise", rel+"~", pageVersion(1))

	fx.silent(wantSilence)

	if fx.hasEvent(string(observability.EventContentStableReadTimeout)) {
		t.Errorf("a non-page reported a stable-read timeout:\n%s", fx.logs.String())
	}

	// And the fixture is not simply broken: a real page in the same tree settles
	// normally, which is what makes the silence above a statement about the
	// filter rather than about the fixture.
	fx.write("noise", "real.md", pageVersion(1))
	fx.mustBeWhole(fx.next())
}

// A path that is gone settles as a removal, not as an upsert.
//
// An upsert for a deleted file would send the indexer to read a file that is not
// there and report a read error, and the row it should have deleted would stay.
func TestDeletionSettlesAsRemove(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "deleting")
	const rel = "doomed.md"

	fx.write("deleting", rel, pageVersion(1))
	fx.mustBeWhole(fx.next())

	fx.remove("deleting", rel)

	seen := fx.next()
	if seen.change.Op != content.OpRemove {
		t.Fatalf("settled operation = %s, want remove (%s)", seen.change.Op, seen.change)
	}

	if seen.change.Path != rel {
		t.Fatalf("path = %q, want %q", seen.change.Path, rel)
	}

	if !errors.Is(seen.err, content.ErrNotExist) {
		t.Fatalf("reading the removed page: got %v, want ErrNotExist", seen.err)
	}

	fx.silent(wantSilence)
}

// A watcher-reported move settles as one rename carrying the old path, and the
// source is never also reported as a deletion.
//
// The directory case is the one that earns the test: a directory is refused as a
// page everywhere else, and only `Moved` — a caller that named both ends of the
// move — settles one. `Indexer.rename` is what turns it into a subtree re-key.
//
// The rename happens on disk first, because the watcher observes a move it did not
// perform: the user's editor moved the file, and the watcher's job is to say which
// two paths it was between.
func TestMovedSettlesOneRename(t *testing.T) {
	t.Parallel()

	fx := newSettleFixture(t, settleTimings, "moves")

	tree(fx.t, fx.dirs["moves"], map[string]string{
		"before.md":       pageVersion(1),
		"folder/below.md": pageVersion(1),
	})

	fx.write("moves", "before.md", pageVersion(1))
	fx.mustBeWhole(fx.next())

	fx.renameOnDisk("moves", "before.md", "after.md")
	fx.renameOnDisk("moves", "folder", "folder-renamed")

	// Both moves, one after the other: the first destination settles while the
	// second is still pending, and neither source may be reported as a deletion.
	fx.deliverMove("moves", "before.md", "after.md")
	fx.deliverMove("moves", "folder", "folder-renamed")

	moved := make(map[string]string)

	for range 2 {
		seen := fx.next()
		if seen.change.Op != content.OpRename {
			t.Fatalf("settled operation = %s, want rename (%s)", seen.change.Op, seen.change)
		}

		moved[seen.change.OldPath] = seen.change.Path
	}

	for old, want := range map[string]string{
		"before.md": "after.md",
		"folder":    "folder-renamed",
	} {
		if moved[old] != want {
			t.Errorf("rename from %q = %q, want %q", old, moved[old], want)
		}
	}

	// Two renames, two changes: no removal for either source, which is what would
	// make the index delete a row the rename re-keys.
	fx.silent(wantSilence)
}

// renameOnDisk moves a path within a campaign's tree, as a user's editor would.
func (fx *settleFixture) renameOnDisk(slug, oldRel, newRel string) {
	fx.t.Helper()

	from := filepath.Join(fx.dirs[slug], filepath.FromSlash(oldRel))
	to := filepath.Join(fx.dirs[slug], filepath.FromSlash(newRel))
	if err := os.Rename(from, to); err != nil {
		fx.t.Fatalf("rename %s to %s: %v", oldRel, newRel, err)
	}
}

// Shutdown drops pending work, and Close is idempotent.
//
// Not `t.Parallel`, and that is load-bearing rather than incidental: the goroutine
// count is only meaningful while nothing else in the package is running, and Go
// releases a parallel test's body only after every serial test has finished.
func TestCloseDropsPendingWorkAndIsIdempotent(t *testing.T) {
	before := runtime.NumGoroutine()

	fx := newSettleFixture(t, settleTimings, "closing")
	const rel = "pending.md"

	fx.write("closing", rel, pageVersion(1))

	// The scheduler has been running for a moment, so it is awake rather than
	// waiting for its first deadline, and Close therefore has something to stop.
	time.Sleep(quietPeriod / 2)

	fx.debouncer.Close()

	after := runtime.NumGoroutine()
	if after > before {
		t.Errorf(
			"goroutines after Close = %d, want no more than the %d before the Debouncer",
			after,
			before,
		)
	}

	// Pending work is dropped rather than settled on the way out: the process is
	// shutting down and the index converges from the filesystem at the next boot.
	fx.silent(wantSilence)

	// Idempotent. The second call waits on the same done channel and returns.
	fx.debouncer.Close()
	fx.debouncer.Close()
}

// A thousand pending paths cost no additional goroutines and no additional
// timers, and all of them settle.
//
// The design claim, asserted rather than asserted-in-a-comment: the whole filter
// is one goroutine and one `time.Timer` whatever is pending, and the deadline
// queue is what makes that true. A per-path timer design would be a thousand
// runtime timers here and a thousand goroutines as each fired.
//
// Serial for the same reason as the shutdown test: the goroutine count is read
// while nothing else in the package is running.
func TestAThousandPendingPathsShareOneGoroutine(t *testing.T) {
	const pages = 1000

	fx := newSettleFixture(t, settleTimings, "wide")

	files := make(map[string]string, pages)
	for page := range pages {
		files[fmt.Sprintf("page-%04d.md", page)] = pageVersion(page)
	}

	tree(t, fx.dirs["wide"], files)

	before := runtime.NumGoroutine()

	for page := range pages {
		fx.touch("wide", fmt.Sprintf("page-%04d.md", page))
	}

	during := runtime.NumGoroutine()
	if during > before+1 {
		t.Errorf("goroutines with %d paths pending = %d, want no more than %d (+1)",
			pages, during, before)
	}

	// Every one of them settles, which is also the assertion that the deadline
	// queue drains: a heap that lost an entry would leave a page unsent forever.
	settled := make([]string, 0, pages)

	for range pages {
		seen := fx.next()
		if seen.err != nil {
			t.Fatalf("reading %s after its change settled: %v", seen.change.Path, seen.err)
		}

		checkWholePage(t, seen.raw)
		settled = append(settled, seen.change.Path)
	}

	slices.Sort(settled)

	for page, path := range settled {
		if want := fmt.Sprintf("page-%04d.md", page); path != want {
			t.Fatalf("settled paths are not complete: at %d got %q, want %q", page, path, want)
		}
	}

	if after := runtime.NumGoroutine(); after > before+1 {
		t.Errorf(
			"goroutines after %d settles = %d, want no more than %d (+1)",
			pages,
			after,
			before,
		)
	}
}
