// Partial-write safety: the moment at which a path is allowed to be believed.
//
// S-4.3 requires **two** mechanisms and says so in one sentence, because one of
// them alone is known to be insufficient and the reason it is insufficient here
// is specific:
//
//   - **Per-path debounce**, with that path's timer restarted by every new event
//     for it. Per path and not global, for two reasons that are separate: a
//     global timer lets one campaign's continuous writer hold back every other
//     campaign's settles, and a timer that is not restarted never fires for a
//     page that is being written continuously.
//   - **A size-stable confirmation.** After the timer fires, `stat` the path and
//     require an unchanged size across two samples. Time-debounce alone is
//     documented as unreliable, and it is unreliable *here* because a slow
//     writer mid-write produces a steady stream of events: the timer fires at an
//     instant that has nothing to do with the write finishing. The second sample
//     is the only thing in this file that asks the filesystem a question.
//
// # What a truncated parse costs
//
// The reason this file exists at all is a cache that never invalidates. A page
// read mid-write hashes to a `content_hash` that disagrees with the bytes on
// disk (S-5.2), so the render cache key for that page is a key nothing will ever
// produce again, and the stale entry is served until the next rescan. A watcher
// that indexes the third of ten events is therefore not "slightly early", it is
// a wrong answer that looks right.
//
// # One goroutine, one timer
//
// The whole file runs on **one scheduler goroutine and one `time.Timer`**, no
// matter how many paths are pending. The obvious design — a `time.AfterFunc` per
// pending path — costs one runtime timer per pending path and spawns a
// goroutine *per firing*, and resetting a timer means Stop-plus-new, which is
// exactly where a leak lives. A vault that a sync client rewrites has thousands
// of pending paths at once, and that is the case S-4.3 exists for.
//
// The rejected alternative is a fixed ticker plus a scan of a deadline map: one
// timer is right, but it cannot express "the next deadline is in 40ms, so sleep
// 40ms", so it either polls far more often than the debounce period or adds up
// to a whole tick of latency. A deadline heap answers that question exactly, in
// O(log n), and an idle vault with nothing pending costs zero wakeups.
//
// The heap holds stale entries and skips them, because a path's deadline moves
// forward on every event and a heap has no cheap "remove and reinsert".
// `compactLocked` rebuilds it once it holds more than twice the live entries,
// which bounds the queue at O(pending) for any burst size.
//
// # Renames, and what is not claimed
//
// `Touch` **never** reports `OpRename`. It cannot: a path that no longer exists
// is indistinguishable from a path that was renamed away, and a rename's
// destination is a path this component has never seen. Pairing them by guessing
// — same size, same window, same campaign — is how a wrong `OpRename` happens,
// and a wrong rename is worse than two correct simpler operations because
// `RenamePage` re-keys the row *and keeps its `content_hash`*: mis-pair two
// unrelated operations and the index holds one path's hash against another
// path's bytes, which is the precise defect this file was written to remove.
// `Indexer.rename` also handles a remove followed by a rename, so the two
// operations converge.
//
// So a rename is reported only when the watcher says so, through `Moved`, which
// takes the old path. `Moved`'s one job beyond that is to **not** report the
// source as a deletion, and to settle the destination like anything else — an
// atomic save renames over a path, and the destination is worth confirming
// before the index reads it.
//
// # A stuck writer is not a settled file
//
// The confirmation has a budget, and exhausting it emits
// `content.stable_read_timeout` and **drops** the path. It does not emit it.
// "The size did not settle in time" and "the size settled" are different
// findings, and collapsing them is how a stuck writer becomes silent data loss
// rather than a visible one. A dropped path is picked up by the next event, or
// by the periodic rescan (S-4.5) if no more events are coming.

package content

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/semiplane/semiplane/internal/observability"
)

// The default timings, and why each is where it is.
//
// A caller may override all three through `SettleTimings`, and a zero value
// means "the default" rather than "no waiting": a Debouncer built without
// stated timings is the safe one, for the same reason `SymlinkPolicy`'s zero
// value is the refusing one.
const (
	// defaultQuietPeriod is how long a path must go without an event before its
	// size is sampled at all.
	//
	// 150ms. An editor's save is a burst of events over a few milliseconds and a
	// sync client's over a few hundred, so the number has to clear the second of
	// them and stay under the half-second at which a GM watching their own save
	// starts to wonder. The rejected alternative is the "one second, because it
	// is a round number" rule: it doubles the latency of every save in the
	// system, including the web editor's own (S-6.4 makes the watcher the save
	// path's feedback mechanism), to buy margin a second sample provides for
	// free.
	defaultQuietPeriod = 150 * time.Millisecond

	// defaultSampleInterval is the gap between the two samples the confirmation
	// compares.
	//
	// 50ms, which is three orders of magnitude above a local `write(2)` and one
	// order below what a person perceives as a pause. Two samples further apart
	// than about a second stop being "the same instant" and start being two
	// events, at which point the confirmation is a second debounce with extra
	// bookkeeping. The interval is also the granularity at which a slow writer is
	// noticed, so it must be short relative to the budget.
	defaultSampleInterval = 50 * time.Millisecond

	// defaultSettleBudget bounds how long the confirmation may keep re-sampling
	// before it gives up on a path.
	//
	// Two seconds: ten sample intervals at the default, which is long enough
	// that a network filesystem flushing a large page does not trip it and short
	// enough that a genuinely stuck writer is visible while the operator is still
	// looking. The rejected alternative is retrying until the size holds, which
	// has no termination condition at all — a writer that appends one byte a
	// second would hold a path unsettled for the lifetime of the campaign, and
	// the index would never see the page.
	//
	// A budget at or below the sample interval can never confirm anything, and
	// every path will report a timeout instead. The defaults are four times apart
	// and a caller overriding one should override the other.
	defaultSettleBudget = 2 * time.Second
)

// queueSlack is how many stale queue entries may accumulate below the point at
// which the queue is rebuilt.
//
// A non-zero slack because the rebuild itself is O(n log n): with a slack of
// zero, a vault whose pending set is large and shrinking would rebuild on every
// settle. Sixty-four is arbitrary and only has to be larger than the number of
// pages one tick of a busy vault settles.
const queueSlack = 64

// transientSuffixes are the names a file is written under while it is being
// written, and which are therefore not pages.
//
// The list rather than a rule because these are spelled by other programs and
// the spellings are facts about those programs, not derivable: Vim's swap
// (`.swp`, `.swo`, `.swx`), the `.tmp` a sync client and this process's own
// atomic write stage through, the `~` Emacs leaves behind, the `.part` and
// `.crdownload` a browser or a resumable sync leaves behind. Every one of them
// is a *suffix*, not a component, because every one of them is a prefix of the
// page name: `page.md.tmp` is a page being written and `page.tmp.md` is a page
// whose author has unfortunate taste.
var transientSuffixes = []string{
	".tmp",
	".swp",
	".swo",
	".swx",
	".part",
	".crdownload",
	"~",
}

// SettleTimings are the three durations S-4.3's two mechanisms run on.
//
// A value rather than three constructor arguments because they are one decision
// — how patient this process is with its writers — and a caller that has to
// choose them in the right order to get a working filter will get one wrong.
type SettleTimings struct {
	// QuietPeriod is how long a path must go without an event before its size is
	// sampled. S-4.3's per-path debounce.
	QuietPeriod time.Duration

	// SampleInterval is the gap between the two samples a confirmation compares.
	// S-4.3's size-stable confirmation, measured.
	SampleInterval time.Duration

	// SettleBudget bounds the whole confirmation. When it runs out the path is
	// reported as `content.stable_read_timeout` and dropped rather than emitted.
	SettleBudget time.Duration
}

// withDefaults fills the zero values, and is the only place the defaults above
// are read.
//
// Non-positive rather than only zero, so that a computed negative duration — the
// result of one configuration value minus another — produces a working filter
// rather than a `time.Timer` that fires in the past forever.
func (t SettleTimings) withDefaults() SettleTimings {
	if t.QuietPeriod <= 0 {
		t.QuietPeriod = defaultQuietPeriod
	}

	if t.SampleInterval <= 0 {
		t.SampleInterval = defaultSampleInterval
	}

	if t.SettleBudget <= 0 {
		t.SettleBudget = defaultSettleBudget
	}

	return t
}

// Debouncer decides when a path has stopped moving, and hands the change to a
// `ChangeSink` only once it has.
//
// The watcher is event-driven (S-4.1) and its events are a narrator rather than
// a record: one save arrives as several, a sync client delivers a directory as
// one, and an atomic write stages a temporary file and renames it. Reading a
// page on the first of those is reading it mid-write. This component is the
// half of the pipeline that turns that narrator into a conclusion, and the
// conclusion it reaches for a path is "nothing has happened to this for a while
// *and* the file agrees with itself", which is the only combination that means
// the bytes on disk are the bytes the index is about to read.
//
// Safe for concurrent use. `Touch` and `Moved` are called from the watcher's
// event goroutine, the confirmations run on the scheduler goroutine, and every
// piece of mutable state is behind one mutex that is never held across a
// `stat`, a `sink` call, or a log line — because all three of those can block,
// and a filesystem call under a lock is a lock every other campaign's writer
// waits behind.
type Debouncer struct {
	sink   ChangeSink
	roots  *Registry
	watch  *observability.Watch
	timing SettleTimings

	// mu guards everything below it. One lock rather than one per phase because
	// a single pending map is the whole state, and a design where arming a path
	// and confirming it took different locks would be a design with a window
	// between them — which is the window this file exists to close.
	mu      sync.Mutex
	pending map[pendingKey]*pendingPage
	queue   queue

	// ticket is the monotonic counter behind the one live queue entry per
	// pending page. See pendingPage.ticket.
	ticket uint64

	// stopped is set by Close and by a cancelled context, and makes arming a
	// no-op: without it a watcher that keeps calling Touch after shutdown grows
	// a map nothing will ever read.
	stopped bool

	// wake carries one bit, never more. The scheduler needs to be told "the
	// earliest deadline may have moved earlier" and nothing else; the deadline
	// itself is read from the queue under the lock, because a value sent over a
	// channel could be stale by the time it is read.
	wake chan struct{}

	// stop closes to ask the scheduler to return, and done closes when it has.
	// Separate because Close has to be able to wait for the goroutine to be
	// gone rather than merely for its own signal to be delivered.
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// NewDebouncer returns a Debouncer that settles changes for every campaign whose
// content root `roots` retains, and hands them to `sink`.
//
// ctx is the debouncer's lifetime. Cancelling it stops the scheduler and drops
// everything pending, exactly as `Close` does, because a debouncer whose
// scheduler has gone must not keep accepting events it will never settle. That
// is the whole of its relationship with the context: `Touch` and `Moved` take
// none, because neither does any I/O — they arm a deadline under a mutex and
// return, and the only filesystem access in this component happens on the
// scheduler, which has the context.
//
// `roots` is the same registry the indexer reads through, and that is load-
// bearing rather than convenient: every sample is an `os.Root`-confined `stat`,
// so a settle decision is made through the same boundary that will later read
// the bytes, and a symlink or an escaping path is refused identically in both
// places (S-3.5, S-4.4).
//
// `watch` and `sink` must not be nil. A nil `watch` would panic on the first
// timeout rather than skip the signal, which is worse: the signal is the only
// evidence a stuck writer produced.
//
// A Debouncer owns one goroutine from this call until `Close`. There is no lazy
// start, because a filter that decides whether it has a scheduler is a filter
// with a state a caller cannot see.
func NewDebouncer(
	ctx context.Context,
	sink ChangeSink,
	roots *Registry,
	watch *observability.Watch,
	timing SettleTimings,
) *Debouncer {
	deb := &Debouncer{
		sink:    sink,
		roots:   roots,
		watch:   watch,
		timing:  timing.withDefaults(),
		pending: make(map[pendingKey]*pendingPage),
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}

	go deb.run(ctx)

	return deb
}

// Touch reports that a path changed, and starts (or restarts) its quiet period.
//
// The name is the whole contract: an event does not say *what* happened, it says
// the path moved, and the answer to "what happened" is decided later by asking
// the filesystem. There is deliberately no operation parameter — a watcher that
// passed on what it thought happened would be forwarding an inference made
// before the write finished, which is the inference S-4.3 exists to stop being
// made too early.
//
// Called from the watcher's event goroutine, so it must not block: the only work
// it does is a mutex, a map and a wake bit, and the scheduler holds that mutex
// only for the instructions that move a deadline.
//
// A path that is not a page produces nothing at all, and neither does one that is
// already gone — the latter is not special-cased here, because classifying it
// needs the quiet period first. That is deliberate: a delete followed by a
// recreate inside one window settles as an upsert, where classifying at event time
// would have reported a removal for a file that is about to exist again.
func (d *Debouncer) Touch(campaignID int64, slug, rel string) {
	if !settleablePath(rel) {
		return
	}

	now := time.Now()

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.stopped {
		return
	}

	d.armLocked(d.pageLocked(campaignID, slug, rel), now.Add(d.timing.QuietPeriod))
}

// Moved reports that a watcher identified a path as having moved, and settles
// the destination as an `OpRename` carrying `oldRel`.
//
// The only route to `OpRename`, and optional: `Touch` alone reports a rename as
// a removal plus an upsert, which converges (see the file comment). A caller that
// *can* name both ends should say so, for the row's benefit rather than the
// index's — a remove-then-upsert loses `created_at` and makes a renamed page
// look new to search.
//
// Three things are true here that are not true of `Touch`:
//
//   - The destination is settled like any other path, because an atomic save
//     renames a temporary file over the destination and the destination is worth
//     confirming before the index reads it.
//   - A pending entry for the source is dropped rather than confirmed as a
//     removal, because the move subsumes the deletion. Its `oldPath` is carried
//     forward when it has one, so a chain of moves before the first settle
//     reports the chain's start rather than an intermediate name the index never
//     held a row for.
//   - Neither path is required to look like a page, because a rename may name a
//     directory: `Indexer.rename` decides between a page and a subtree from
//     `indexablePath`, and the whole tree under a moved folder is cheaper to
//     re-key in one operation than to re-index file by file. Only the buried
//     check applies, which is the part of the filter that is about names this
//     pipeline never settles at all.
func (d *Debouncer) Moved(campaignID int64, slug, oldRel, newRel string) {
	if buried(oldRel) || buried(newRel) {
		return
	}

	now := time.Now()

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.stopped {
		return
	}

	from := oldRel

	if source := d.pending[pendingKey{campaignID: campaignID, path: oldRel}]; source != nil {
		if source.oldPath != "" {
			from = source.oldPath
		}

		delete(d.pending, source.key())
	}

	destination := d.pageLocked(campaignID, slug, newRel)
	destination.moved = true
	destination.oldPath = from

	d.armLocked(destination, now.Add(d.timing.QuietPeriod))
}

// Close stops the scheduler and drops every path still pending.
//
// Pending work is dropped rather than settled on the way out, which is the
// right side to fail on: the process is shutting down, the index converges from
// the filesystem at the next boot, and settling on the way out would mean
// writing a database from a shutdown path that may already be closing it.
//
// Idempotent, and it waits. A second call returns immediately because the first
// one waited for the goroutine to exit, and waiting is the point: a caller that
// returns from `Close` and then tears down the sink's dependencies must not race
// a settle still in flight.
func (d *Debouncer) Close() {
	d.closeOnce.Do(func() {
		close(d.stop)
	})

	<-d.done
}

// run is the scheduler: one goroutine, one timer, one deadline at a time.
func (d *Debouncer) run(ctx context.Context) {
	// The stopped flag is set here rather than in Close alone, so that a
	// cancelled context stops the filter exactly as a Close does. Without it,
	// arming would continue into a queue nobody drains, which is the leak a
	// shutdown path is least likely to be tested for.
	defer func() {
		d.mu.Lock()
		d.stopped = true
		d.pending = nil
		d.queue = nil
		d.mu.Unlock()

		close(d.done)
	}()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Reset without draining is safe because this module's Go version gives
	// `Timer` the Go 1.23 semantics, where a stale value cannot be received
	// after a Reset or a Stop. Written the older way it would need a drain after
	// every wake, and the drain is the kind of code that is correct until it is
	// not.
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		deadline, armed := d.nextDeadline()
		if !armed {
			// Nothing pending: sleep on the wake bit rather than on a timer, so
			// an idle vault costs no wakeups at all.
			select {
			case <-d.wake:
			case <-d.stop:
				return
			case <-ctx.Done():
				return
			}

			continue
		}

		timer.Reset(max(time.Until(deadline), 0))

		select {
		case <-timer.C:
		case <-d.wake:
		case <-d.stop:
			return
		case <-ctx.Done():
			return
		}

		d.settleDue(runCtx)
	}
}

// settleDue confirms every path whose deadline has passed, in deadline order.
//
// The whole batch is taken under one lock and then confirmed outside it, so a
// vault with a thousand due paths costs a thousand lock acquisitions rather than
// two thousand, and no `stat` is ever serialised against a writer's `Touch`.
func (d *Debouncer) settleDue(ctx context.Context) {
	for {
		due := d.takeDue()
		if len(due) == 0 {
			return
		}

		for _, page := range due {
			d.confirm(ctx, page)
		}
	}
}

// nextDeadline returns the earliest live deadline, discarding stale queue
// entries above it.
func (d *Debouncer) nextDeadline() (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.discardStaleLocked()

	entry, armed := d.queue.peek()
	if !armed {
		return time.Time{}, false
	}

	return entry.at, true
}

// takeDue removes and returns every pending page whose deadline has passed.
func (d *Debouncer) takeDue() []*pendingPage {
	now := time.Now()

	d.mu.Lock()
	defer d.mu.Unlock()

	var due []*pendingPage

	for {
		entry, armed := d.queue.peek()
		if !armed {
			break
		}

		page := d.pending[entry.key]
		if page == nil || page.ticket != entry.ticket {
			d.queue.pop()

			continue
		}

		if entry.at.After(now) {
			break
		}

		d.queue.pop()

		due = append(due, page)
	}

	return due
}

// discardStaleLocked drops queue entries whose page has settled, been dropped or
// been re-armed with a newer ticket.
//
// Cheap and lazy rather than an indexed removal, and the cost is bounded by
// `queueSlack` through `compactLocked`. An entry is stale exactly when it is not
// the one its page's current ticket names, which is also the condition
// `decide` uses to notice that a page moved while it was being sampled: one
// mechanism, two questions, one invariant.
func (d *Debouncer) discardStaleLocked() {
	for {
		entry, armed := d.queue.peek()
		if !armed {
			return
		}

		page := d.pending[entry.key]
		if page != nil && page.ticket == entry.ticket {
			return
		}

		d.queue.pop()
	}
}

// confirm samples one pending path and acts on what it found.
func (d *Debouncer) confirm(ctx context.Context, page *pendingPage) {
	ticket := d.ticketOf(page)

	sample, err := d.sample(page)
	if err != nil {
		// A `stat` that is neither a success nor "not there" cannot be
		// classified into any operation, and reporting one anyway would be
		// reporting a fact about the filesystem nobody asked about. The path is
		// dropped, which leaves the index stale for this change and no staler
		// than a dropped event already does; the index's own read path reports
		// the same condition as `index.page_skipped`, and the periodic rescan
		// (S-4.5) closes the gap. Inventing an event name for it here would mean
		// a signal W5's surface does not have.
		d.forget(page, ticket)

		return
	}

	switch outcome, change := d.decide(page, ticket, sample); outcome {
	case outcomeSettled:
		d.sink(ctx, change)

	case outcomeTimeout:
		d.watch.StableReadTimeout(ctx, strconv.FormatInt(page.campaignID, 10), page.path)

	case outcomeRetry, outcomeDropped:
		// Re-armed under the lock, or deliberately forgotten.
	}
}

// decide turns one sample into the next step for a pending path, and is the
// only place S-4.3's state machine is written down.
//
// Everything it touches is under the lock and nothing in it does I/O, so the
// gap between "the samples agreed" and "the page is out of the map" is a few
// instructions with no syscall in it. That gap is the last place a write could
// slip in unnoticed, and the ticket check is what closes it.
func (d *Debouncer) decide(
	page *pendingPage,
	ticket uint64,
	sample pageSample,
) (confirmOutcome, Change) {
	now := time.Now()
	key := page.key()

	d.mu.Lock()
	defer d.mu.Unlock()

	if page.ticket != ticket {
		// An event arrived while the path was being sampled, and it has already
		// restarted the quiet period. Emitting now would report the bytes as of
		// a sample that an event has already invalidated.
		return outcomeDropped, Change{}
	}

	switch page.phase {
	case phaseQuiet:
		return d.beginLocked(page, key, sample, now), Change{}

	case phaseConfirm:
		return d.finishLocked(page, key, sample, now)

	default:
		// Unreachable: `armLocked` writes no other phase. Forgetting the path
		// rather than panicking, because a panic on the scheduler goroutine
		// takes every campaign's settle filter down with it.
		delete(d.pending, key)

		return outcomeDropped, Change{}
	}
}

// beginLocked takes the first of a confirmation's two samples.
//
// Re-arming rather than emitting: one sample is not evidence, and the whole
// point of the second mechanism is that a single `stat` says nothing about
// whether a writer is finished. The budget starts here, and is never extended,
// so the total time a path may spend being confirmed is bounded no matter how
// many times it is re-sampled.
func (d *Debouncer) beginLocked(
	page *pendingPage,
	key pendingKey,
	sample pageSample,
	now time.Time,
) confirmOutcome {
	if sample.dir && !page.moved {
		delete(d.pending, key)

		return outcomeDropped
	}

	page.phase = phaseConfirm
	page.first = sample
	page.budget = now.Add(d.timing.SettleBudget)
	d.scheduleLocked(page, now.Add(d.timing.SampleInterval))

	return outcomeRetry
}

// finishLocked takes the second sample and decides.
//
// Four outcomes, and the order of the cases is the order of how much they
// matter: a directory is silence, an unstable size is another look, and only
// then is the path settled or given up on.
func (d *Debouncer) finishLocked(
	page *pendingPage,
	key pendingKey,
	sample pageSample,
	now time.Time,
) (confirmOutcome, Change) {
	if sample.dir && !page.moved {
		delete(d.pending, key)

		return outcomeDropped, Change{}
	}

	if !sample.stableAgainst(page.first) {
		next := now.Add(d.timing.SampleInterval)
		if !next.Before(page.budget) {
			// The confirmation gave up. Deliberately not an emission: see the
			// file comment. The path is dropped so the next event re-arms it
			// from a clean state rather than resuming a confirmation whose first
			// sample belongs to a write that has since been superseded.
			delete(d.pending, key)

			return outcomeTimeout, Change{}
		}

		page.first = sample
		d.scheduleLocked(page, next)

		return outcomeRetry, Change{}
	}

	change := Change{
		CampaignID: page.campaignID,
		Slug:       page.slug,
		Path:       page.path,
		OldPath:    page.oldPath,
	}

	switch {
	case !sample.present && page.oldPath != "":
		// The destination of a watcher-reported move is not there, so the page
		// is not at either end of the move. The row the index holds, if any, is
		// at the source the chain began with, because a destination that never
		// settled has no row of its own — so that is the path to remove, and the
		// destination is not mentioned at all rather than mentioned and found
		// missing.
		change.Op = OpRemove
		change.Path = page.oldPath
		change.OldPath = ""

	case !sample.present:
		// A removal for a row the index never held is harmless — `DeletePage`
		// reports it and `Indexer.ApplyChange` logs it — and it is the price of
		// not remembering which paths this process has seen. A page created and
		// deleted inside one debounce window produces one of them. The
		// alternative is a "did I ever see this" flag per pending path, kept only
		// to avoid a log line.
		change.Op = OpRemove

	case page.oldPath != "":
		change.Op = OpRename

	default:
		change.Op = OpUpsert
	}

	delete(d.pending, key)

	return outcomeSettled, change
}

// sample stats one pending path through its campaign's confined root.
//
// The confinement is not incidental: this is the only filesystem access in the
// component, and it goes through `Root.At` for the same reason the indexer's
// read does (S-3.5). A path that is not there is not an error here — it is the
// answer that classifies a removal — and every other failure is one this
// component cannot interpret, because `Root.classify` collapses the refusals
// into `ErrOutsideRoot` by design.
func (d *Debouncer) sample(page *pendingPage) (pageSample, error) {
	root, err := d.roots.Get(page.slug)
	if err != nil {
		return pageSample{}, fmt.Errorf("resolve the content root of %s: %w", page.slug, err)
	}

	target, err := root.At(page.path)
	if err != nil {
		return pageSample{}, fmt.Errorf("confine %s for sampling: %w", page.path, err)
	}

	info, err := target.Stat()
	if err != nil {
		if errors.Is(err, ErrNotExist) {
			return pageSample{}, nil
		}

		return pageSample{}, fmt.Errorf("stat %s: %w", page.path, err)
	}

	return pageSample{present: true, size: info.Size(), dir: info.IsDir()}, nil
}

// ticketOf reads a page's current ticket.
//
// A lock for two field reads, which looks like overhead and is not: without it
// this read races the watcher's `Touch` on the same page, and the race is not a
// torn int but a *decision taken from a mixture of two states* — which is the
// one failure mode in this file that no later check can catch.
func (d *Debouncer) ticketOf(page *pendingPage) uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()

	return page.ticket
}

// forget drops a pending path unless it has been re-armed since.
func (d *Debouncer) forget(page *pendingPage, ticket uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if page.ticket != ticket {
		return
	}

	delete(d.pending, page.key())
}

// pageLocked returns the pending page for a path, creating it if there is none.
//
// Both halves of the identity are fixed here, at creation, and never written
// again: the campaign id is what the index and the `Change` speak, and the slug is
// what this component resolves the content root by. They travel together because
// `Change` carries both — the id for the store and the slug for a log line — and
// the caller is the only place that knows the pairing, so it is asserted here
// rather than re-derived.
func (d *Debouncer) pageLocked(campaignID int64, slug, rel string) *pendingPage {
	key := pendingKey{campaignID: campaignID, path: rel}

	page := d.pending[key]
	if page != nil {
		return page
	}

	page = &pendingPage{
		campaignID: campaignID,
		slug:       slug,
		path:       rel,
	}

	d.pending[key] = page

	return page
}

// armLocked restarts a path's quiet period and takes it back to the first
// mechanism.
//
// The phase and the first sample are cleared rather than kept: an event means
// something was written, and a size measured before that write describes a file
// that no longer exists. The budget goes with them, and is re-armed at the next
// sample — which is what makes a *stuck* writer distinguishable from an *active*
// one. A path whose events keep arriving never reaches its first sample and so
// never times out; a path whose events have stopped but whose size will not hold
// times out once.
func (d *Debouncer) armLocked(page *pendingPage, deadline time.Time) {
	page.phase = phaseQuiet
	page.first = pageSample{}

	d.scheduleLocked(page, deadline)
}

// scheduleLocked queues one deadline for a page and takes its ticket.
//
// Every push takes a new ticket, including the scheduler's own re-arms, so the
// invariant is uniform: a page's live queue entry is the one whose ticket the
// page holds, and every earlier entry for it is stale by construction.
//
// The deadline is written to both the page and the entry it queues. That is a
// second copy on purpose: `compactLocked` rebuilds the queue from the pages
// alone, and a design that read the deadline back out of the queue to do that
// would be O(n²) exactly when the queue is largest — which is the one moment
// where quadratic is not affordable.
func (d *Debouncer) scheduleLocked(page *pendingPage, deadline time.Time) {
	d.ticket++
	page.ticket = d.ticket
	page.deadline = deadline

	earliest, armed := d.queue.peek()

	d.queue.push(scheduleEntry{key: page.key(), at: deadline, ticket: page.ticket})

	// The scheduler sleeps until the earliest deadline it read, so it has to be
	// woken only when this entry is the new earliest — or when the queue was
	// empty, which is the case where it was asleep on the wake bit rather than on
	// a timer. Arming a later deadline needs no signal, because the scheduler's
	// existing wakeup is still early enough to notice it.
	if !armed || deadline.Before(earliest.at) {
		d.signalLocked()
	}

	if len(d.queue) > 2*len(d.pending)+queueSlack {
		d.compactLocked()
	}
}

// signalLocked tells the scheduler that the earliest deadline may have moved
// earlier.
//
// Non-blocking, and one buffered bit, because the signal carries no information
// — the deadline is read from the queue under the lock — so a bit that is
// already set means the scheduler has not yet looked at what prompted it, and one
// look is enough. Blocking here would put the watcher's event goroutine behind
// the scheduler's, which is the opposite of what a wake-up is for.
func (d *Debouncer) signalLocked() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// compactLocked rebuilds the queue from the live entries alone.
//
// The only way stale entries are ever reclaimed, and the bound it maintains is
// the one that makes the lazy scheme affordable: a burst of a thousand events on
// one path queues a thousand entries and rebuilds down to one, and a vault whose
// pending set empties rebuilds to nothing.
func (d *Debouncer) compactLocked() {
	live := make([]scheduleEntry, 0, len(d.pending))
	for _, page := range d.pending {
		live = append(live, scheduleEntry{
			key:    page.key(),
			at:     page.deadline,
			ticket: page.ticket,
		})
	}

	d.queue.rebuild(live)
}

// confirmOutcome is what one sample concluded about a pending path.
type confirmOutcome uint8

const (
	// outcomeRetry means the page was re-queued: the quiet period restarted, or
	// another sample was taken.
	outcomeRetry confirmOutcome = iota

	// outcomeSettled means the path is out of the map and the returned change is
	// the answer.
	outcomeSettled

	// outcomeDropped means the path is out of the map and there is nothing to
	// say: not a page, already gone, or superseded by an event that re-armed it.
	outcomeDropped

	// outcomeTimeout means the confirmation ran out of budget.
	outcomeTimeout
)

// settlePhase is which of S-4.3's two mechanisms is holding a path.
type settlePhase uint8

const (
	// phaseQuiet is the per-path debounce: waiting for the path to go quiet.
	phaseQuiet settlePhase = iota

	// phaseConfirm is the size-stable confirmation: comparing samples.
	phaseConfirm
)

// pageSample is one `stat` of a pending path.
//
// A value rather than a pair of fields because the comparison is the whole
// mechanism, and one method that answers "did anything change" cannot drift from
// the fields it compares.
type pageSample struct {
	// present is whether the path was there at all. A removal is settled by
	// exactly the same two samples as an upsert, which is what makes a delete
	// that is really an atomic rename's first half converge instead of firing a
	// removal for a file that is about to exist again.
	present bool

	// size is the byte count, meaningless when `dir` is set.
	size int64

	// dir is whether the path is a directory.
	dir bool
}

// stableAgainst reports whether two consecutive samples describe the same state.
//
// Existence first, then kind, then size. A directory's size is deliberately not
// compared: a directory's own size on disk is not a write in progress, it is an
// inode's bookkeeping, and comparing it would report a stuck writer for every
// folder move that happens to carry files into the move. That matters because a
// directory destination is exactly what `Moved` exists to report.
func (s pageSample) stableAgainst(prev pageSample) bool {
	if s.present != prev.present || s.dir != prev.dir {
		return false
	}

	if s.dir {
		return true
	}

	return s.size == prev.size
}

// pendingKey identifies one path in one campaign.
//
// Both halves because a path alone is not unique: two campaigns may both have
// `index.md`, they are two files with two rows and two readers, and keying by
// path would let one campaign's writer reset the other's timer — which is the
// cross-campaign interference S-4.3's per-path rule exists to prevent.
type pendingKey struct {
	campaignID int64
	path       string
}

// pendingPage is one path being waited on.
//
// Fields are written only under `Debouncer.mu`, and `key`, `campaignID`, `slug`
// and `path` are written only at construction and never again — which is what
// lets `sample` and `confirm` read them without the lock.
type pendingPage struct {
	campaignID int64
	slug       string
	path       string

	// oldPath is the start of a watcher-reported move this settle is finishing,
	// and is empty for every other change.
	oldPath string

	// moved marks a page that arrived through `Moved`, and is the only thing that
	// lets a *directory* settle at all: for a page that arrived through `Touch`,
	// a directory is not a page and the answer is silence.
	moved bool

	// ticket names the one queue entry that is live for this page. Every entry
	// for this path whose ticket differs is stale, and the difference is how a
	// confirmation notices that the path moved while it was being sampled.
	ticket uint64

	// deadline is when this page is next due. The queue holds the same instant,
	// and the two are written together in `scheduleLocked` so that a compaction
	// can rebuild the queue without reading it.
	deadline time.Time

	// phase is which mechanism is currently holding the path.
	phase settlePhase

	// first is the first of the confirmation's two samples. Meaningful only
	// while `phase` is phaseConfirm.
	first pageSample

	// budget is when this confirmation runs out of patience, taken at the first
	// sample and never extended.
	budget time.Time
}

// key returns this page's map key.
func (p *pendingPage) key() pendingKey {
	return pendingKey{campaignID: p.campaignID, path: p.path}
}

// queue is a min-heap of scheduled deadlines, ordered by deadline and then by
// insertion order.
//
// A purpose-built heap rather than `container/heap` because that interface
// cannot be generic and a filter that settles one path per wake does not need
// the eight methods an `any`-shaped abstraction is written for. Ties are broken
// by ticket so that two paths that come due in the same nanosecond settle in a
// defined order — map iteration order would make the emission order of a
// burst depend on the runtime, and a test that cannot predict its own ordering
// cannot assert on it.
type queue []scheduleEntry

// scheduleEntry is one deadline for one pending page.
type scheduleEntry struct {
	key    pendingKey
	at     time.Time
	ticket uint64
}

// push adds one entry.
func (q *queue) push(entry scheduleEntry) {
	items := append(*q, entry)

	index := len(items) - 1
	for index > 0 {
		parent := (index - 1) / 2
		if !earlier(items[index], items[parent]) {
			break
		}

		items[index], items[parent] = items[parent], items[index]
		index = parent
	}

	*q = items
}

// peek returns the earliest entry without removing it.
func (q *queue) peek() (scheduleEntry, bool) {
	if len(*q) == 0 {
		return scheduleEntry{}, false
	}

	return (*q)[0], true
}

// pop removes the earliest entry.
func (q *queue) pop() {
	items := *q

	last := len(items) - 1

	items[0] = items[last]
	// Cleared rather than truncated around, so a page path stays reachable from
	// the queue only while it is queued. A slice that keeps its tail's strings
	// alive after the entries are gone is a leak with a very long fuse.
	items[last] = scheduleEntry{}
	*q = items[:last]

	(*q).siftDown(0)
}

// rebuild replaces the heap with entries and re-establishes the invariant.
//
// One linear pass rather than a push per entry: this is the path taken after a
// burst, when the queue is at its largest and every push would sift to the top.
func (q *queue) rebuild(entries []scheduleEntry) {
	*q = entries

	for index := len(entries)/2 - 1; index >= 0; index-- {
		(*q).siftDown(index)
	}
}

// siftDown moves the entry at index down to its place.
func (q *queue) siftDown(index int) {
	items := *q

	for {
		left := 2*index + 1
		if left >= len(items) {
			return
		}

		smallest := left
		if right := left + 1; right < len(items) && earlier(items[right], items[left]) {
			smallest = right
		}

		if !earlier(items[smallest], items[index]) {
			return
		}

		items[index], items[smallest] = items[smallest], items[index]
		index = smallest
	}
}

// earlier reports whether a must be settled before b.
//
// Ticket as the tie-break, and it doubles as the insertion order because it is
// monotonic. Two entries with the same deadline and the same ticket are the same
// entry, which cannot happen.
func earlier(a, b scheduleEntry) bool {
	if !a.at.Equal(b.at) {
		return a.at.Before(b.at)
	}

	return a.ticket < b.ticket
}

// settleablePath reports whether a path is a page this filter will settle at
// all.
//
// `indexablePath` is the indexer's own predicate and the answer is that
// predicate, reused rather than restated: a filter with its own idea of what a
// page is would emit changes for files the index refuses to index, and the
// visible symptom is a page that appears in search on an event and disappears
// at the next rescan. **The two must stay in step**, and the cheapest way to
// keep them in step is for there to be one of them.
func settleablePath(rel string) bool {
	if buried(rel) {
		return false
	}

	return indexablePath(rel)
}

// buried reports whether a path is one this pipeline never settles, whatever its
// extension.
//
// Dot-components and `node_modules` are `skipDirectory`'s rule — the indexer's,
// applied to every component of a path rather than only to directories — and the
// transient suffixes are this file's own addition, because the indexer does not
// need one: every name a writer stages through fails `indexablePath` already,
// since none of them ends in `.md`.
//
// That redundancy is the point, and it is load-bearing the day `indexablePath`
// widens. A filter whose silence for `page.md.swp` rests on an unrelated
// predicate is a filter that starts indexing swap files the moment somebody adds
// support for another extension, and the reason those files are silent belongs
// next to the filter rather than being an emergent property of a suffix check in
// another file.
func buried(rel string) bool {
	for segment := range strings.SplitSeq(rel, "/") {
		if skipDirectory(segment) {
			return true
		}
	}

	for _, suffix := range transientSuffixes {
		if strings.HasSuffix(rel, suffix) {
			return true
		}
	}

	return false
}
