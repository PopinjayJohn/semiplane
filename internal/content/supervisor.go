// Degraded mode: the policy the watcher reports to, and the two periods it runs
// on.
//
// The watcher is a narrator. `Reverify` reconciles the watch set against the
// tree and hands back one `VerifyReport` per campaign saying what it found —
// watches added, kept, dropped, refused and failed, a missing content root, a
// walk that could not be completed — and it deliberately stops there. Nothing in
// `watch.go` decides what any of that means, because a component that can see
// that a watch is missing cannot see whether anybody has decided what to do
// about it, and `VerifyReport`'s own comment says so. This file is that
// decision, and the whole of it is S-4.5's table:
//
//	LimitHit, or a Failed directory  ->  the rescan fallback: a periodic full reindex
//	Missing                          ->  degraded, and sticky
//	Dropped                          ->  one reindex, now
//	Refused                          ->  nothing: S-4.4 working as specified
//	WalkError                        ->  degraded, and sticky
//
// Degraded is a **state, not an exit**. S-4.5 requires the server to keep
// starting and keep serving when a content root is missing, and the whole shape
// of this file follows from that: nothing here returns an error to a caller for
// a campaign in trouble, nothing here stops reconciling the other campaigns, and
// nothing here stops the goroutine. A vault on an unmounted disk is a
// condition the instance lives with and an operator is told about, and the
// alternative — one unmounted disk taking the whole thing down — is the failure
// S-4.5 exists to prevent.
//
// # Why the signals are split where they are
//
// The watcher emits `watch.add_failed` and `watch.degraded`; this file emits
// `watch.rescan_fallback` and `watch.recovered`. The line falls where the
// knowledge does.
//
// The first two are facts about a component's own capability, knowable at the
// instant the capability was lost: a directory that could not be watched, an
// event goroutine that panicked and is no longer delivering. The second two are
// statements about a **decision this process has made and is now running** —
// "this campaign is being delivered by periodic rescan instead of by event" and
// "it is not any more" — and the gauge half of those two counters is a count of
// campaigns currently *in* a state, which is a fact about the policy rather than
// about a watch. A watcher that emitted them would be announcing a fallback it
// does not implement and a recovery it has not performed, and would have to
// decide when to stop announcing one, which is the decision that lives here.
//
// The corollary is that a transition is announced once. A campaign whose
// condition persists is re-observed on every pass and said nothing about, which
// is the difference between a gauge and a pass counter — the reason
// `observability.Watch` keeps `CounterValue.Current` at all.
//
// # One goroutine, two deadlines, one timer
//
// One goroutine owns the policy, and the alternatives both lose for the same
// reason. A timer per campaign means a vault with N campaigns is N timers to arm
// and disarm as campaigns enter and leave the fallback, and arming is where a
// timer leak lives; a timer per policy means this file is a small scheduler
// instead of a loop. One loop takes the minimum of the two deadlines, exactly as
// the settle filter's scheduler does (`debounce.go`), so the two periods can
// differ — they have to, and `SupervisorTimings` says why — and a campaign in
// neither state costs nothing but a line in a map.
//
// A campaign that is not in the rescan fallback has no rescan deadline at all:
// the deadline is armed when one enters the fallback and disarmed when the last
// one leaves, so an instance where nothing is wrong sleeps on one timer and does
// one walk of each campaign's tree per verify period.

package content

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/semiplane/semiplane/internal/observability"
)

// The default periods, and why each is where it is.
//
// A caller may override both through `SupervisorTimings`, and a zero value
// means "the default" rather than "never", for the reason `SettleTimings`
// gives: a supervisor built without stated timings is the one whose numbers were
// argued for.
const (
	// defaultVerifyEvery is how often the watch set is reconciled against the
	// tree.
	//
	// Five minutes. The pass costs a full walk of every campaign's tree plus one
	// `WatchList`, and what it exists to catch is a watch that is gone — a
	// condition with **no event at all**, which is why only a timer can find it.
	// Nothing is unsafe while a watch is missing: reads go through `Root`, which
	// is a descriptor rather than a path, so a lost watch means edits stop being
	// *indexed*, not that a page 404s or answers with stale bytes. The filesystem
	// is the source of truth (S-3.1), so a late repair is the same repair.
	//
	// The rejected alternative is a shorter number, and thirty seconds is the one
	// that would be chosen: it feels urgent, and it walks every campaign's entire
	// tree 120 times an hour to notice that a search index is a few minutes
	// behind. Five minutes is comfortably inside the window in which a GM edits
	// something, switches to the wiki, and concludes the wiki is broken.
	defaultVerifyEvery = 5 * time.Minute

	// defaultRescanEvery is how often a campaign in the rescan fallback is
	// reindexed in full.
	//
	// One minute, and this number is not a cost number at all — it is a
	// user-visible one. A campaign in the fallback has no event stream, so every
	// edit it receives is discovered by this rescan and by nothing else: this is
	// the edit latency for the exact campaigns that are already degraded. A
	// minute is the difference between "the wiki caught up" and "the wiki is
	// broken" for the person looking at it.
	//
	// It is only charged to campaigns that are in the fallback, so the cost is
	// proportional to how broken the instance already is, and the timer is the
	// one this loop already owns. The rejected alternative is tying it to
	// `defaultVerifyEvery` for the sake of one number: five minutes of edit
	// latency on a campaign whose events are already gone, for no saving. The
	// rejected number on the other side is five seconds, which is a walk of
	// every page in a degraded campaign every five seconds — a hot loop wearing
	// a fallback's clothes.
	defaultRescanEvery = time.Minute
)

// The `Detail` discriminators this file can put on a line, named so that the
// pairing between a `VerifyReport` field and the word an operator greps for
// cannot drift between two branches of the same switch.
//
// All of them are short and none of them is a sentence: `Detail` is a
// discriminator (S-12.3), and a caller who finds themselves wanting to write a
// sentence into it has found a missing event name instead.
const (
	// detailWatching is the state a campaign recovered *to*: its watch set is
	// believed complete and filesystem events are the mechanism.
	detailWatching = "watching"

	// detailWatchStopped is the state a campaign left, and is used instead of
	// detailWatching for the one recovery that is not a recovery — a campaign
	// that fell out of the rescan fallback and straight into an unreadable
	// content root. Its watch set stopped; saying "watching" would be false.
	detailWatchStopped = "watch_stopped"

	// detailContentRootMissing is a content root the walk reported as absent.
	detailContentRootMissing = "content_root_missing"

	// detailContentRootUnwalkable is a content root whose walk failed for a
	// reason other than its absence — a descriptor that has gone bad, a mount
	// that has gone away.
	detailContentRootUnwalkable = "content_root_unwalkable"

	// detailNoContentRoot is a campaign this process holds no confined root for,
	// which is a different fault with a different owner: the root was never
	// opened, or it was dropped, and `campaignroots` reports the boot-time case
	// before this file ever runs.
	detailNoContentRoot = "content_root_not_retained"

	// detailWatchLimit is ENOSPC or EMFILE: the watch limit is exhausted, and
	// the operator's action is to raise it or to watch fewer trees.
	detailWatchLimit = "watch_limit"

	// detailWatchAddFailed is a directory that could not be watched for a reason
	// that is not the limit, where the action is to find out why that one
	// directory refuses a watch.
	detailWatchAddFailed = "watch_add_failed"

	// detailPolicyHalted is this goroutine panicking, which is a fault in this
	// file rather than a condition any campaign reported.
	detailPolicyHalted = "policy_halted"
)

// Reindexer is the convergence operation the supervisor runs when it cannot
// trust a campaign's event stream.
//
// An interface declared by the consumer for the reason `PageStore` gives: the
// supervisor's job is the *policy* of when to converge, and a policy that
// imported the indexer's other half — `ApplyChange`, `HandleChange`, the prune —
// would be a policy that had opinions about pages. `*Indexer` satisfies this
// structurally, so the composition root hands it over and no adapter exists.
type Reindexer interface {
	// ReindexCampaign brings one campaign's rows in line with its content root.
	ReindexCampaign(ctx context.Context, slug string, campaignID int64) (IndexReport, error)
}

// WatchVerifier is the reconciliation the supervisor drives on its timer.
//
// `*Watcher` satisfies it. The capability is named rather than the concrete type
// so that the policy can be exercised against a scripted set of reports — which
// is the only way to reach two of S-4.5's five cases, because a filesystem will
// not produce them on demand (see the tests). A supervisor that took `*Watcher`
// would be a supervisor whose degradation policy can only be tested against a
// kernel.
type WatchVerifier interface {
	// Reverify reconciles the watch set against the tree and reports what it did,
	// one report per campaign whether or not anything changed.
	Reverify(ctx context.Context) []VerifyReport
}

// SupervisorTimings are the two periods the degradation policy runs on.
//
// A value rather than two constructor arguments because they are one decision —
// how patient this process is with a watcher that has stopped watching — and a
// caller choosing them in the wrong order gets a policy that walks a tree every
// five seconds and rescans every five minutes, which is worse than either.
type SupervisorTimings struct {
	// VerifyEvery is how often the watch set is reconciled against the tree.
	// S-4.5's slow-timer re-verify, and the only thing that can find a watch
	// that is lost without an event.
	VerifyEvery time.Duration

	// RescanEvery is how often a campaign in the rescan fallback is reindexed in
	// full. It is the edit latency for a campaign with no event stream, so it
	// wants to be shorter than VerifyEvery.
	RescanEvery time.Duration
}

// withDefaults fills the zero values, and is the only place the constants above
// are read.
//
// Non-positive rather than only zero, so that a computed negative duration — the
// result of one configuration value minus another — produces a working policy
// rather than a timer that fires in the past forever.
func (t SupervisorTimings) withDefaults() SupervisorTimings {
	if t.VerifyEvery <= 0 {
		t.VerifyEvery = defaultVerifyEvery
	}

	if t.RescanEvery <= 0 {
		t.RescanEvery = defaultRescanEvery
	}

	return t
}

// supervisorMode is the mechanism currently keeping one campaign's rows in step
// with its content root.
type supervisorMode uint8

const (
	// modeEvents means the watch set is believed complete and filesystem events
	// are the mechanism. The zero value, so a campaign the supervisor has not
	// classified yet is classified optimistically and earns a `watch.recovered`
	// only by having been somewhere worse first.
	modeEvents supervisorMode = iota

	// modeFallback means the watch set cannot be completed — S-4.5's exhausted
	// limit — and a periodic full rescan is the mechanism instead.
	modeFallback

	// modeDegraded means the tree cannot be read at all, so neither mechanism
	// can run. The server still serves (S-4.5).
	modeDegraded
)

// supervisorCampaign is what the supervisor remembers about one campaign between
// passes.
type supervisorCampaign struct {
	// campaignID and slug are the pair every report and every indexer call
	// carries, fixed when the campaign is first seen and never written again.
	campaignID int64
	slug       string

	// mode is the mechanism currently in use. It is the whole of the state that
	// reaches the observability surface: every signal this file emits is a
	// *transition* of this field, so the gauge `observability.Watch` maintains
	// and the state this file believes can never disagree.
	mode supervisorMode

	// repair records that a reindex this supervisor started has not converged.
	//
	// It is the reason a campaign with a healthy watch set is reindexed again on
	// the next pass: a repair that failed is not a repair, and a database that
	// was briefly unavailable does not become available because the failure was
	// dropped. It is deliberately *not* a mode and carries no signal — see
	// `reindex`.
	repair bool
}

// Supervisor owns S-4.5's degradation policy: the slow-timer re-verify, the
// rescan fallback, and the degraded state a missing or unwalkable content root
// puts a campaign in.
//
// One per process, built by the composition root and closed on shutdown, for the
// reason `Watcher` and `Registry` are each one value: the set of campaigns in
// trouble is a fact about this process, and a package global would be a second
// source of truth that every test in the process shares.
//
// Safe for concurrent use. `Start` and `Close` are the two entry points and both
// take the same lock, so a supervisor that is closed while it is being started
// is one state or the other rather than a goroutine nobody is waiting for.
type Supervisor struct {
	roots   *Registry
	index   Reindexer
	watcher WatchVerifier
	signals *observability.Watch
	timing  SupervisorTimings

	// mu guards everything below it, and is never held across a call into the
	// indexer, the notifier or a log handler — all three can block, and this is
	// the lock every pass takes.
	mu sync.Mutex

	// campaigns is what is known about each campaign, keyed by id. Entries are
	// never removed: a campaign that stops being reported about is a campaign
	// this process still holds a root for, and dropping the entry would drop the
	// gauge bookkeeping that goes with it.
	campaigns map[int64]*supervisorCampaign

	// nextVerify is always armed while the loop runs. `nextRescan` is armed only
	// while some campaign is in the fallback; the zero time means "nothing is
	// rescan-degraded", and `untilNext` skips it.
	nextVerify time.Time
	nextRescan time.Time

	started bool
	closed  bool

	// stop asks the loop to return and done closes when it has. Separate because
	// `Close` has to be able to wait for the goroutine to be gone rather than
	// for its own signal to have been delivered — the same pair `Debouncer`
	// keeps, and for the same reason.
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// NewSupervisor returns a policy that reconciles the watch set on a timer and
// degrades per S-4.5.
//
// Wiring the four dependencies is the whole of the setup, and the supervisor owns
// everything after it: the caller starts it once and closes it once, and never
// decides anything about a degraded campaign.
//
// The arguments may be nil only for `signals`, which is replaced with a
// counting-only `Watch` for the reason `NewWatcher` replaces one — a nil
// dereference on the path that reports a broken vault is worse than a counter
// that is not on `/readyz`. The other three are required and are checked by
// `Start`, because a policy with no watcher to verify is a watcher that has
// silently stopped watching, which is the one failure this file exists to make
// visible, and a constructor that refused would refuse at the moment the value
// is built rather than at the moment the policy would have run.
//
// No context parameter: `Start` takes it, and a goroutine that captured a
// context at construction would be a context nothing can ever cancel, which
// `containedctx` is right about.
func NewSupervisor(
	roots *Registry,
	index Reindexer,
	watcher WatchVerifier,
	signals *observability.Watch,
	timing SupervisorTimings,
) *Supervisor {
	if signals == nil {
		signals = observability.NewWatch(nil, nil)
	}

	return &Supervisor{
		roots:     roots,
		index:     index,
		watcher:   watcher,
		signals:   signals,
		timing:    timing.withDefaults(),
		campaigns: make(map[int64]*supervisorCampaign),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Start begins the policy: the first reconciliation is due immediately, and the
// two deadlines run from there.
//
// Immediate rather than one verify period from now, because the condition this
// file exists for has no event to wait on. A vault that was never watchable at
// boot — the watch limit was already exhausted when the process started — would
// otherwise spend its first five minutes with an index that is not being
// maintained and nothing in the log about it, and the five minutes is the whole
// of the first impression.
//
// The pass itself runs on the goroutine, not here: `Start` does no I/O, so it
// cannot be the thing that makes a boot slow, and it returns as soon as the
// policy is scheduled. A caller that needs to know what the first pass found
// reads `/readyz`, where the gauges are, which is the surface the state belongs
// to anyway.
//
// ctx is the process's lifetime, not a request's. Cancelling it stops the
// policy exactly as `Close` does, so a caller may use either and the other is
// harmless.
//
// An error means the wiring is wrong — a nil dependency, a supervisor already
// running, or one that has been closed. It is a construction fault and never a
// report about a campaign: no campaign in trouble produces one, because S-4.5's
// answer to a broken vault is a state and not a failure to start.
func (s *Supervisor) Start(ctx context.Context) error {
	if err := s.dependencies(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case s.closed:
		return errors.New("content: the content supervisor is closed")
	case s.started:
		return errors.New("content: the content supervisor is already started")
	default:
	}

	s.started = true
	s.nextVerify = time.Now()

	go s.run(ctx)

	return nil
}

// Close stops the policy and waits for the goroutine to return.
//
// Idempotent, and it waits, because "Close returned" has to mean "no pass is in
// flight and no timer will fire again" — a composition root that closes the
// supervisor and then closes the store has to be able to trust that no reindex
// is about to run against it. Shutdown reaches this more than once (a deferred
// close, a signal handler, a test's cleanup) and the alternative to tolerating
// that is a process that panics on the way out.
//
// A supervisor that was never started is not waited for, because nothing will
// ever close its `done`. That is not a supported order — `Start` is the first
// thing a caller does — but returning is better than blocking for ever on a
// channel nobody closes.
//
// The second and later calls are the reason `closeOnce` wraps the wait and not
// only the signal: a concurrent `Close` blocks until the first one has finished
// waiting, so `Close` is a barrier rather than a hint.
func (s *Supervisor) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		started := s.started
		s.mu.Unlock()

		close(s.stop)

		if started {
			<-s.done
		}
	})
}

// run is the policy loop: one goroutine, one timer, the earliest of two
// deadlines.
//
// The timer is created here and stopped here, so it cannot outlive the
// goroutine — a `time.Ticker` owned by a caller is a timer that keeps firing
// into a closed store, and a ticker is also the wrong shape here: a rescan
// deadline is armed and disarmed as campaigns enter and leave the fallback, and
// a ticker has no way to express "not at the moment".
func (s *Supervisor) run(ctx context.Context) {
	// `done` first and the recovery second, so the recovery runs before the
	// channel closes: a panic that reached this point without being recovered
	// would leave every `Close` in the process waiting for ever, which is a
	// failed shutdown rather than a failed campaign.
	defer close(s.done)
	defer s.recoverPanic(ctx)

	// Already due: `Start` armed the verify deadline at the current instant, and
	// this is what picks it up. `Timer.Reset` below needs no drain, because this
	// module's Go version gives `Timer` the 1.23 semantics where a stale value
	// cannot be received after a reset.
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-s.stop:
			return

		case <-timer.C:
		}

		s.tick(ctx)

		delay, armed := s.untilNext()
		if !armed {
			// Unreachable while running: the verify deadline is armed by `Start`
			// and re-armed by every pass that observes one. The fallback is the
			// verify period because a policy that waited on nothing would wait for
			// ever, and this file's whole subject is a failure that has to be
			// noticed rather than one that may be slept through.
			delay = s.timing.VerifyEvery
		}

		timer.Reset(delay)
	}
}

// tick runs whatever is due and re-arms the deadlines from here.
//
// Deadlines are measured from *after* the work rather than from the instant it
// was due, which is the difference between a periodic rescan and a hot loop: a
// rescan of a large vault that takes longer than its own period would otherwise
// leave the next one already overdue and run them back to back for ever.
func (s *Supervisor) tick(ctx context.Context) {
	now := time.Now()

	verified := s.verifyIfDue(ctx, now)

	// The rescan's entire work is reindexing the campaigns in the fallback, and
	// a verification pass reindexes exactly those — so a tick where both were
	// due has already done it, and doing it again would walk every degraded
	// campaign's tree twice for one deadline. The deadline is still re-armed
	// below, so the skip costs a tick of latency and not a period.
	rescanDue := due(s.rescanDueAt(), now)
	if !verified && rescanDue {
		s.rescan(ctx)
	}

	base := time.Now()

	if verified {
		s.setVerifyDue(base.Add(s.timing.VerifyEvery))
	}

	if rescanDue {
		s.setRescanDue(base.Add(s.timing.RescanEvery))
	}
}

// verifyIfDue reconciles the watch set and applies the policy to what it finds.
func (s *Supervisor) verifyIfDue(ctx context.Context, now time.Time) bool {
	if !due(s.verifyDueAt(), now) {
		return false
	}

	for _, report := range s.watcher.Reverify(ctx) {
		s.apply(ctx, report)
	}

	return true
}

// rescan reindexes every campaign in the fallback, in slug order.
func (s *Supervisor) rescan(ctx context.Context) {
	for _, campaign := range s.fallbacks() {
		s.reindex(ctx, campaign)
	}
}

// apply turns one verification report into this supervisor's reaction, and is
// the whole of S-4.5's table.
func (s *Supervisor) apply(ctx context.Context, report VerifyReport) {
	campaign := s.record(report)

	// S-4.5: "a missing content root marks the campaign degraded and the server
	// still starts". Two unreadable cases, one branch, because the reaction is
	// the same and only the detail differs — a missing root is a disk that is not
	// mounted, a failed walk is a descriptor that has gone bad, and the
	// operator's next action is the same in both.
	//
	// Sticky, and that is the word S-4.5 does not use but means. The state is
	// left only by a pass that can read the tree again, and it cannot be left by
	// retrying: a `Root` holds an **open descriptor**, so a directory deleted
	// underneath it still walks and re-walks successfully, and the only way out
	// of this state is a new descriptor. The only thing in the process that opens
	// one is `Registry.Open` on the registrar's behalf, so recovery is the
	// registrar's call and this file's job is to hold the gauge honestly until a
	// pass sees a tree it can read.
	//
	// No reindex either, and that is the part with teeth. `ReindexCampaign` is
	// walk-then-prune, so a rescan allowed to run against a root that could not
	// be walked finds no files and deletes every row the campaign has: a
	// campaign whose disk is not mounted would lose its search index as well as
	// its vault, and the index is the only part that is rebuildable. Failing
	// closed here is the whole reason the degraded branch returns before the
	// reindex rather than after it.
	switch {
	case report.Missing:
		s.transition(ctx, campaign, modeDegraded, detailContentRootMissing)

		return

	case report.WalkError != nil:
		s.transition(ctx, campaign, modeDegraded, detailContentRootUnwalkable)

		return
	}

	// The registry is this process's own statement about which campaigns it can
	// read, and this is the one guard on the destructive side of the fallback: a
	// report for a campaign it does not hold is a wiring fault, and reindexing it
	// would resolve no root and prune against an empty walk. The real watcher
	// cannot produce such a report — `NewWatcher` refuses to start unless the two
	// agree — so this is the runtime version of a check that only ever ran at
	// startup, and it is here because a registry that changes while the process
	// runs is not covered by a construction-time check.
	if _, err := s.roots.Get(report.Slug); err != nil {
		s.transition(ctx, campaign, modeDegraded, detailNoContentRoot)

		return
	}

	// S-4.5's own sentence: watch-limit exhaustion falls back to a periodic full
	// rescan. `LimitHit` (ENOSPC, EMFILE or ENFILE) and a `Failed` directory are
	// one reaction and two details, because the operator's next action differs —
	// raise the limit, or work out why one directory refuses a watch — and an
	// alert that cannot tell them apart sends the operator to the wrong one.
	exhausted := report.LimitHit || len(report.Failed) > 0

	// What has to be re-read, and the three reasons, which are the only three:
	//
	//   - the watch limit is exhausted, so the fallback is a rescan and a pass
	//     that does not rescan is a fallback that is not running;
	//   - `Dropped`: the tree moved under the watcher, so the rows under the
	//     directories that went are stale. A one-off, not a mode — the watch set
	//     is otherwise fine, and the next pass reports nothing to do;
	//   - a repair that did not converge last time;
	//   - the campaign is coming out of `modeDegraded`, which is the repair the
	//     degraded branch above has been *not* running for as long as the root was
	//     unreadable. Whatever the tree did during that window is unknown by
	//     construction — which is why the degraded branch returns early rather
	//     than reindexing — so the first readable pass owes a full convergence and
	//     not merely a re-read of a known-good index.
	//
	// `Refused` is deliberately not in the list, and saying why is most of this
	// comment. S-4.4 declined to descend into a symlink: that is the specified
	// default, the read path refuses the same path in the same way, and a row for
	// a page that can never render is a search hit for a 404. It is counted on
	// the report so an operator whose pages are not appearing can see the reason,
	// and a supervisor that emitted a signal per count would put a line in the
	// log on every verification pass for a link somebody put there deliberately.
	recovering := campaign.mode == modeDegraded

	if exhausted || report.Dropped > 0 || campaign.repair || recovering ||
		campaign.mode == modeFallback {
		s.reindex(ctx, campaign)
	}

	if exhausted {
		detail := detailWatchAddFailed
		if report.LimitHit {
			detail = detailWatchLimit
		}

		s.transition(ctx, campaign, modeFallback, detail)

		return
	}

	s.transition(ctx, campaign, modeEvents, detailWatching)
}

// transition moves a campaign into a mode and emits the signal the move needs.
//
// Three rules, and the third is the one that is easy to get wrong:
//
//   - A mode that has not changed emits nothing. The gauges are counts of
//     campaigns currently *in* a state, and a policy that re-announces a
//     condition on every pass turns a gauge into a pass counter — which is the
//     failure `observability.Watch`'s own comment says a single-valued counter
//     would be.
//   - Entering `modeDegraded` or `modeFallback` emits that mode's signal with the
//     detail the caller passed, because the discriminator an operator acts on is
//     the report field that caused the move and only the caller knows which it
//     was.
//   - Leaving either of them emits `Recovered` **even when the campaign is going
//     straight into the other**, because `Recovered` is the only call that clears
//     a state and it clears both. A campaign that fell out of the rescan fallback
//     because its content root became unreadable would otherwise carry the
//     fallback gauge for the rest of the process, and a gauge that only ever rises
//     is the failure the surface exists to prevent. The detail is
//     `detailWatchStopped` rather than `detailWatching` in that case, because
//     two true lines beat one line that says the campaign is watching when it is
//     not.
func (s *Supervisor) transition(
	ctx context.Context,
	campaign supervisorCampaign,
	next supervisorMode,
	detail string,
) {
	if campaign.mode == next {
		return
	}

	s.setMode(campaign.campaignID, next)

	id := signalID(campaign.campaignID)

	if campaign.mode != modeEvents {
		left := detailWatching
		if next != modeEvents {
			left = detailWatchStopped
		}

		s.signals.Recovered(ctx, id, left)
	}

	switch next {
	case modeDegraded:
		s.signals.Degraded(ctx, id, detail)

	case modeFallback:
		s.signals.RescanFallback(ctx, id, detail)

	case modeEvents:
		// Nothing more: the recovery above is the signal for arriving here, and
		// a campaign that was already healthy is not a recovery.
	}
}

// reindex converges one campaign's rows with its content root, and records
// whether it managed to.
//
// The report is discarded. `Indexer` reports the only field of it an operator
// acts on — `Skipped`, per page, as `index.page_skipped` — and there is no §13.2
// event for "a rescan wrote N rows"; inventing one would be a signal W5's
// surface does not have, and a count on a line that already exists is a field
// rather than a name.
//
// A failure sets `repair` and is retried on the next pass, and it is
// deliberately **not** a mode and deliberately not signalled. `watch.degraded`
// means a watcher that cannot see its content root, and a campaign whose content
// root is perfectly readable is not that: its rows are behind, which is a
// different fact with a different owner, and the retry is what bounds it. The
// one thing this file will not do is widen a named gauge to cover a condition
// its name does not mean, because a gauge that stops meaning what it says is
// worse than a gap an operator can see in the store's own error lines.
func (s *Supervisor) reindex(ctx context.Context, campaign supervisorCampaign) {
	if _, err := s.index.ReindexCampaign(ctx, campaign.slug, campaign.campaignID); err != nil {
		s.setRepair(campaign.campaignID, true)

		return
	}

	s.setRepair(campaign.campaignID, false)
}

// recoverPanic reports a panic on the policy goroutine and lets it end.
//
// The same treatment `Watcher.loop` gives its own goroutine, and the same
// reason: this goroutine is fed by a filesystem and a database, and a panic on
// it is a failed process rather than a failed request. Every campaign is moved
// to `modeDegraded` and said so once, because the loop's invariants have been
// shown wrong once and nothing about the state it was maintaining can be trusted
// afterwards — including, for a campaign that was healthy, whether its last
// reindex finished.
//
// The loop ends rather than continuing, and both alternatives are worse:
// continuing re-runs a broken pass every period, and letting the panic reach the
// runtime takes every campaign's watcher down with it.
func (s *Supervisor) recoverPanic(ctx context.Context) {
	if recovered := recover(); recovered == nil {
		return
	}

	for _, campaign := range s.all() {
		s.transition(ctx, campaign, modeDegraded, detailPolicyHalted)
	}
}

// dependencies reports what the wiring is missing.
func (s *Supervisor) dependencies() error {
	switch {
	case s.roots == nil:
		return errors.New("content: the content supervisor has no content registry")
	case s.index == nil:
		return errors.New("content: the content supervisor has no indexer to rescan with")
	case s.watcher == nil:
		return errors.New("content: the content supervisor has no watcher to verify")
	default:
		return nil
	}
}

// record returns what is known about the campaign a report is about, creating
// the entry the first time one is seen.
//
// A copy rather than the stored pointer, so that the decision in `apply` is made
// against one consistent snapshot while the reindex that follows it runs without
// the lock held.
func (s *Supervisor) record(report VerifyReport) supervisorCampaign {
	s.mu.Lock()
	defer s.mu.Unlock()

	campaign, known := s.campaigns[report.CampaignID]
	if !known {
		campaign = &supervisorCampaign{campaignID: report.CampaignID, slug: report.Slug}
		s.campaigns[report.CampaignID] = campaign
	}

	return *campaign
}

// setMode records a campaign's mode and arms or disarms the rescan deadline to
// match.
//
// Armed on the way into the fallback and disarmed on the way out, and never
// nudged by anything else. A deadline that every verification pass pushed a
// little further out would stop firing, and a rescan that stops firing is the
// silent failure this whole file is about. Arming is a no-op when a deadline is
// already armed, so a second campaign entering the fallback does not delay the
// first one's rescan.
func (s *Supervisor) setMode(campaignID int64, next supervisorMode) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, known := s.campaigns[campaignID]
	if !known {
		// Unreachable: `transition` is only reached for a campaign `record`
		// created. Forgetting the move rather than panicking on the policy
		// goroutine is the trade `Debouncer.decide` makes for the same reason.
		return
	}

	previous := stored.mode
	stored.mode = next

	switch {
	case next == modeFallback && previous != modeFallback:
		if s.nextRescan.IsZero() {
			s.nextRescan = time.Now().Add(s.timing.RescanEvery)
		}

	case previous == modeFallback && next != modeFallback:
		s.nextRescan = time.Time{}
	}
}

// setRepair records whether a reindex this supervisor started has converged.
func (s *Supervisor) setRepair(campaignID int64, outstanding bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if stored, known := s.campaigns[campaignID]; known {
		stored.repair = outstanding
	}
}

// all returns every campaign the supervisor knows, in slug order.
func (s *Supervisor) all() []supervisorCampaign {
	s.mu.Lock()
	defer s.mu.Unlock()

	found := make([]supervisorCampaign, 0, len(s.campaigns))
	for _, campaign := range s.campaigns {
		found = append(found, *campaign)
	}

	sortBySlug(found)

	return found
}

// fallbacks returns the campaigns currently running on the rescan fallback.
func (s *Supervisor) fallbacks() []supervisorCampaign {
	s.mu.Lock()
	defer s.mu.Unlock()

	found := make([]supervisorCampaign, 0, len(s.campaigns))
	for _, campaign := range s.campaigns {
		if campaign.mode == modeFallback {
			found = append(found, *campaign)
		}
	}

	sortBySlug(found)

	return found
}

// sortBySlug puts campaigns in slug order.
//
// Because a map's iteration order is randomised, and this order decides the
// order the reindexes run in: two passes over the same state would otherwise
// issue the same queries in a different order, which is a log nobody can diff
// and a test that cannot assert.
func sortBySlug(campaigns []supervisorCampaign) {
	slices.SortFunc(campaigns, func(one, other supervisorCampaign) int {
		return strings.Compare(one.slug, other.slug)
	})
}

// verifyDueAt returns when the next reconciliation is due.
func (s *Supervisor) verifyDueAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.nextVerify
}

// rescanDueAt returns when the next rescan is due, or the zero time when no
// campaign is in the fallback.
func (s *Supervisor) rescanDueAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.nextRescan
}

// setVerifyDue points the verify deadline at an instant.
func (s *Supervisor) setVerifyDue(when time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextVerify = when
}

// setRescanDue points the rescan deadline at an instant.
func (s *Supervisor) setRescanDue(when time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextRescan = when
}

// untilNext returns how long until the earliest armed deadline, and whether one
// is armed at all.
func (s *Supervisor) untilNext() (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	earliest := s.nextVerify
	if armed := s.nextRescan; !armed.IsZero() && (earliest.IsZero() || armed.Before(earliest)) {
		earliest = armed
	}

	if earliest.IsZero() {
		return 0, false
	}

	return max(time.Until(earliest), 0), true
}

// due reports whether a deadline has arrived.
//
// A zero deadline is never due, which is what makes the zero time usable as
// "this timer is not armed": a rescan deadline of zero must not fire on every
// tick of an instance where nothing is degraded.
func due(deadline, now time.Time) bool {
	return !deadline.IsZero() && !deadline.After(now)
}

// ensure the interfaces the composition root hands over are named here, at the
// point that depends on them, so a signature change in either is a compile error
// in this file rather than a test that fails only when the pipeline is exercised.
var (
	_ Reindexer     = (*Indexer)(nil)
	_ WatchVerifier = (*Watcher)(nil)
)
