// The content watcher: one filesystem watch for every campaign, and one event
// goroutine for all of them.
//
// This is the half of S-4.1 that turns a file moving into a `Change`, and the
// half with the most ways to be quietly wrong: a watcher that has stopped
// watching looks exactly like a vault nobody has edited. Nothing here reports
// success, so the specification's failure mode is the default and every rule
// below is about not being silent in it.
//
// # Why directories and never files (S-4.2)
//
// An fsnotify watch on a path is bound to the **inode** behind it, and the
// project's primary write path is an atomic rename (S-6.4: temp file in the
// same directory, `fsync`, `os.Rename`) — which is also what Obsidian does, and
// what most editors do. The rename replaces the inode at the destination and
// destroys the old one's last link, so:
//
//   - a watch on `Page.md` is gone the first time `Page.md` is saved, and the
//     second save is invisible forever;
//   - a watch on the *directory* is untouched, because the directory's own inode
//     did not change.
//
// That is not "a file watch is less reliable". It is a file watch that is
// broken for the write path this project is built around, and it fails
// silently: the process is up, `/healthz` is green, and the campaign stops
// seeing edits. The alternative that lost was watching the files and watching
// their directories too — a file watch adds nothing over its directory for the
// atomic-save case, and it spends a watch descriptor on every page, which is the
// resource S-4.5 says runs out. So: every directory in every campaign root, and
// nothing else.
//
// # What follows from watching directories
//
// Three obligations, each of which is a way the tree and the watch set drift
// apart:
//
//   - A directory **created** must be watched. A sync client that drops a
//     folder into a vault emits one event for the folder and none for the pages
//     inside it, because there was no watch on the folder's parent to see them.
//   - A directory **removed or renamed** must be un-watched. inotify drops the
//     watch itself, silently; the bookkeeping has to match or the set reports a
//     directory that is being watched and is not there.
//   - A watch can be **lost without any event at all** — an inode-bound watch
//     that outlived its name, an ENOSPC, an EMFILE. Nothing detects that from
//     the event stream, which is why S-4.5 requires a slow-timer re-verify.
//     `Reverify` is that capability, and the composition root owns the timer.
//
// # Why a rename is not guessed at (S-4.1, `Change.OpRename`)
//
// fsnotify reports a rename as a `Rename` event carrying the **old** path, and
// — on the backends that pair them — a `Create` event carrying the new one. The
// pairing fsnotify computes internally is keyed on the kernel's rename cookie
// and is exposed on no exported field in v1.10.1 (`Event.renamedFrom` is
// unexported and there is no accessor), so it cannot be read.
//
// Without the cookie, a `Rename` and the *next* event are not provably the same
// operation: `mv page.md /elsewhere/ && touch other.md` arrives as exactly
// `Rename(page.md), Create(other.md)` — adjacent, and unrelated. So a page
// rename is reported as `OpRemove` then `OpUpsert`, which is what `change.go`
// says the index converges through, and the cost is exactly the cost
// `change.go` names: the row's `created_at` restarts. The alternative that lost
// was pairing by adjacency, because the wrong `OpRename` does not merely
// re-key a row — it moves one page's `content_hash` and `body_plain` onto
// different bytes, which is a search result built from a page nobody read.
//
// A directory is the one exception, and the reason is asymmetry rather than
// confidence. A moved directory's pages are otherwise *unreachable* — their
// events moved with the inode, so nothing reports the subtree and every row
// under the old name outlives the directory — so the choice is a certain silent
// staleness against a pairing whose only failure mode is a concurrent `mkdir`,
// which `Reverify` reports as a dropped watch and which the rescan that follows a
// drop repairs. Exactly one `OpRename` is emitted for a directory move; see
// `created`.
//
// # What this file deliberately does not do
//
// No debouncing and no settling. fsnotify delivers several events per write and
// the settle filter (W2) decides when a change has stopped happening (S-4.3);
// duplicating that here would be a second copy of the timing policy with none of
// its tests. The only state this file holds between events is one pending
// rename, which is a single observation waiting for its other half rather than
// a cache, and one watch set, which is the truth S-4.5's re-verify reconciles.
//
// No raw logging. Every signal goes through `observability.Watch`, so the level
// of `watch.add_failed` is fixed in one file rather than at a call site that a
// later reader can get wrong (S-12.2, S-12.3).

package content

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/fsnotify/fsnotify"

	"github.com/semiplane/semiplane/internal/observability"
)

// WatchRoot names one campaign's content root for the watcher to watch.
//
// The directory is passed in rather than asked for because it is the one piece
// of the campaign's placement `Root` deliberately does not publish: a
// `Root` hands out nothing absolute, so that a path cannot escape into a log
// line or a cache key, and an absolute path is exactly what a prefix routing
// table is made of. The composition root already holds it — it is the
// `campaigns.content_root` column the registrar wrote — so passing it down is
// not a second source of truth, it is the same one arriving by the only route
// that is allowed to name a host path.
type WatchRoot struct {
	// CampaignID is the campaign, and the identity every change from this root
	// carries. Carried rather than resolved later for the reason `Change`
	// documents: the index is keyed by id, so a change that had to look a slug
	// up to reach it would be a change that can fail for a reason unrelated to
	// the file.
	CampaignID int64

	// Slug is the campaign's slug, for `Change.Slug` and for the operator's log.
	Slug string

	// Dir is the campaign's absolute content root directory. Absolute, because
	// the routing table compares it with the absolute paths fsnotify reports
	// and a relative one would be resolved against whatever the process's
	// working directory happened to be at startup.
	Dir string
}

// Notifier is the source of filesystem events the Watcher reads from.
//
// `fsnotify.Watcher`'s own surface, under a name, so that a test can inject the
// two failures the kernel will not produce on demand: a watch limit that
// answers ENOSPC (S-4.5), and an event for a path under no campaign root.
// Neither can be provoked for real — the first would need `fs.inotify.
// max_user_watches` set to zero, the second needs a watch somewhere the watcher
// would not have added one — and a test that cannot provoke the condition tests
// the code that runs when it does not.
type Notifier interface {
	// Add starts watching a directory. Adding a path that is already watched is
	// a no-op rather than an error, which is what makes re-adding a whole
	// subtree on a re-verify safe.
	Add(path string) error

	// Remove stops watching a directory, non-recursively.
	Remove(path string) error

	// Close drops every watch and closes the event and error channels.
	Close() error

	// Events is the stream of filesystem events.
	Events() <-chan fsnotify.Event

	// Errors is the stream of watcher-level failures, which carry no path: the
	// watcher is one instance shared by every campaign, so there is no path an
	// error could be attributed to.
	Errors() <-chan error

	// WatchList returns the paths this notifier believes it is watching.
	//
	// On the seam rather than assumed of the implementation because it is what
	// makes `Reverify` a *reconciliation* rather than a self-consistency check.
	// The watcher's own set is a claim about the notifier; a watch can be lost
	// with the claim intact — an inode-bound watch outliving its name, an ENOSPC
	// that was never observed — and reconciling a claim against itself would
	// report `Kept` for a directory nothing is watching, which is the one answer
	// that guarantees the silent failure S-4.5 exists to prevent.
	//
	// Paths the notifier returns that this watcher does not know about are
	// ignored rather than removed: it is a shared resource in principle, and a
	// directory this watcher never added is not its to un-watch.
	WatchList() []string
}

// WatcherOption configures a Watcher at construction.
//
// An interface rather than a `func` so that the configuration's own type stays
// unexported: the composition root configures a watcher through the `With…`
// functions below, and cannot reach in and set a field.
type WatcherOption interface {
	apply(config *watcherConfig)
}

// watcherConfig is the configuration `WatcherOption`s write into.
type watcherConfig struct {
	newNotifier func() (Notifier, error)
}

// newFsnotifyNotifier is the default event source.
func newFsnotifyNotifier() (Notifier, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify: %w", err)
	}

	return fsnotifyNotifier{watcher: watcher}, nil
}

// notifierOption carries a Notifier supplied by the caller.
type notifierOption struct {
	notifier Notifier
}

func (o notifierOption) apply(config *watcherConfig) {
	config.newNotifier = func() (Notifier, error) { return o.notifier, nil }
}

// WithNotifier makes the Watcher read its events from notifier instead of a new
// fsnotify watcher.
//
// For a test that needs to *cause* one of the two failures the specification
// names — an exhausted watch limit, or an event for a path that belongs to no
// campaign — rather than wait for a kernel to produce one. The alternative,
// raising the watch limit the test runs under, makes the whole test suite's
// outcome depend on a sysctl and fails on the machine where it is already
// exhausted.
func WithNotifier(notifier Notifier) WatcherOption {
	return notifierOption{notifier: notifier}
}

// VerifyReport is what one campaign's watch set looks like after a
// reconciliation, and is the only thing the composition root needs in order to
// decide what S-4.5's degradation mode should be.
//
// Every field is a number an operator can act on rather than a verdict. The
// decision they feed is the caller's — `watch.rescan_fallback` and
// `watch.degraded` are *not* emitted from here, because this file can see that
// a watch is missing and cannot see whether the integrator has already decided
// what to do about it.
type VerifyReport struct {
	// CampaignID is the campaign this report is about.
	CampaignID int64

	// Slug is that campaign's slug.
	Slug string

	// Added is how many directories gained a watch.
	Added int

	// Kept is how many watched directories are still in the tree. It is the
	// number that says the pass had nothing to do, and a campaign whose Kept is
	// the whole tree on every tick is a campaign that is healthy.
	Kept int

	// Dropped is how many watches named a directory that is no longer in the
	// tree. Non-zero is the signal that the tree moved under the watcher — a
	// directory renamed or deleted — and the rows under those directories are
	// stale until the next reindex.
	//
	// Zero does not mean nothing moved: a `Remove` event drops the watch as it
	// is handled, so by the time a pass runs there is nothing left to drop. The
	// number is what the *reconciliation* had to clean up, not a census of what
	// the campaign lost, which is why an integrator that cares about the latter
	// reindexes on any non-zero result rather than trying to count the pages.
	Dropped int

	// Refused is how many symlinks the tree scan did not descend into (S-4.4).
	//
	// Counted rather than signalled, and the reason is that
	// `watch.add_failed` is fixed at error level by S-12.2 for the case where
	// the watcher has stopped watching, and a refused symlink is S-4.4 doing
	// precisely what the specification asks — the read path refuses the same
	// path in the same way, and `AllowSymlinks` is the configuration in which
	// no error is right at all. Erroring on the specified default would train
	// operators to ignore the one line in this subsystem that must never be
	// ignored.
	Refused int

	// Failed names the root-relative directories that could not be watched, in
	// the order they were attempted. Root-relative, so it can be pasted into a
	// log line without publishing the host's directory layout.
	Failed []string

	// LimitHit reports that a watch could not be added because the limit was
	// exhausted — ENOSPC or EMFILE. This is S-4.5's condition, and it is the
	// one field on this report that should make the integrator start a periodic
	// full rescan: a missing watch is indistinguishable from a quiet vault.
	LimitHit bool

	// Missing reports that the campaign's content root is not there. S-4.5's
	// degraded campaign, which the server still starts for.
	Missing bool

	// WalkError is why the tree could not be walked, for a reason other than
	// the root being absent. `Missing` is the `fs.ErrNotExist` case of this,
	// and it is separated because the two call for different reactions: one is
	// "an operator has to look", the other is "this campaign is unreadable".
	WalkError error
}

// watchRoute is one campaign's entry in the prefix routing table.
type watchRoute struct {
	// campaignID and slug identify the campaign a change belongs to.
	campaignID int64
	slug       string

	// dir is the campaign's absolute content root, cleaned, and the key this
	// route is filed under. It is the *only* absolute path in the watcher.
	dir string

	// root is the confined root every read and every stat below goes through.
	// A path from an event is turned back into a root-relative one and then
	// handed to `root.At`, so the confinement boundary is re-established for
	// every event rather than assumed from the prefix match that found it.
	root *Root
}

// watchTarget is a directory the tree says should be watched.
type watchTarget struct {
	// absolute is the path to hand the notifier.
	absolute string

	// rel is the same directory root-relative and slash-separated, which is
	// what a signal carries.
	rel string
}

// pendingRename is a `Rename` event whose other half has not arrived.
//
// One observation held rather than a cache: it is the very next event that
// settles it, and anything else flushes it.
type pendingRename struct {
	// route is the campaign the old path was in.
	route watchRoute

	// rel is the old path, root-relative.
	rel string

	// absolute is the old path on the host, which is what the watch set is
	// keyed by.
	absolute string

	// directory reports that the old path was a directory this watcher was
	// watching, which is the only evidence available about what moved and is
	// the distinction the rename handling turns on.
	directory bool
}

// entryKind is what a created path turned out to be.
type entryKind int

const (
	// entryGone is a path that was not there by the time it was asked about,
	// which is an ordinary race with a writer and not a failure.
	entryGone entryKind = iota

	// entryFile is an ordinary file or something that is not a directory.
	entryFile

	// entryDirectory is a directory, which may need a watch of its own.
	entryDirectory

	// entryRefused is a symlink the content root's policy refuses (S-4.4).
	entryRefused
)

// Watcher watches every registered campaign's content root and turns filesystem
// events into settled `Change`s.
//
// One value for the whole process, built by the composition root and closed on
// shutdown, for the same reason `Registry` is one value: the set of campaigns
// with a root is a fact about this process, and a package global would be a
// second source of truth shared by every test in the process.
//
// Safe for concurrent use. The event goroutine owns the pending rename and the
// callers of `Reverify` may come from a timer, so everything they share is
// behind `mu` — and `mu` is never held across a call into the notifier, because
// the notifier is a syscall boundary and holding a lock across one serialises
// every campaign's events behind it.
type Watcher struct {
	// routing maps a watched directory to its campaign. Built once, at
	// construction, and never rebuilt per event: see `routeFor`.
	routing map[string]watchRoute

	// campaigns is the same set ordered by slug, so a verification report is
	// byte-identical between two runs over the same tree. A map's iteration
	// order is randomised, and a report that reorders is one nobody can diff.
	campaigns []watchRoute

	// notifier is the event source.
	notifier Notifier

	// sink receives the changes. Nil is tolerated and drops them, which exists
	// so a test that is only about routing does not have to invent a consumer.
	sink ChangeSink

	// signals is the §13.2 surface. Never nil: a nil one is replaced with a
	// counting-only `Watch` at construction, so that a caller who has not wired
	// the observability registry gets a visible zero on `/readyz` rather than a
	// nil pointer on the first filesystem event.
	signals *observability.Watch

	// stop is created here rather than by the caller, because closing it is
	// `Close`'s business and a channel a caller could close would be a shutdown
	// the watcher does not know about.

	// mu guards watched. See the type comment.
	mu sync.Mutex

	// watched is the absolute directories this watcher believes it is watching.
	// The tree is the truth (S-3.1) and this is a claim about the tree, which is
	// why `Reverify` reconciles one against the other rather than trusting the
	// event stream to keep them equal — inotify removes a watch silently when a
	// directory goes away.
	watched map[string]struct{}

	// pending is the rename waiting for its other half. Touched only by the
	// event goroutine, and read by nobody else, so it is deliberately outside
	// `mu`: a field that needs a lock nobody takes is a field two readers will
	// eventually disagree about.
	pending *pendingRename

	// stop is closed by Close and is how the loop learns to finish.
	//
	// A channel of its own rather than relying on the notifier's channels being
	// closed, because "Close returned means nothing is being delivered any more"
	// has to hold for *this* watcher rather than for a notifier's good behaviour.
	// fsnotify does close them, and a `Notifier` that did not would hang this
	// goroutine forever — which is not a failure any caller can see, because the
	// process is up and answering.
	stop chan struct{}

	// closeOnce makes Close idempotent, and done waits for the event goroutine.
	closeOnce sync.Once
	closeErr  error
	done      sync.WaitGroup
}

// NewWatcher opens a watcher over watched, adds a watch for every directory in
// every campaign root, and starts delivering changes to sink.
//
// ctx is the process's lifetime, not a request's: cancelling it stops delivery.
// It is a parameter rather than a field because a goroutine holding a context
// forever is a goroutine that cannot be shut down, and `containedctx` is right
// about that. The changes themselves are handed to the sink under this same
// context, which is what lets a sink that blocks on I/O abandon the call on
// shutdown.
//
// watched must name exactly the campaigns `roots` retains, and the check is a
// startup error rather than a warning. A campaign the watcher silently does not
// watch is the failure this whole file is organised against: its pages stop
// being indexed, nothing says so, and the symptom is a wiki whose links rot a
// little more every week.
//
// opts configures the watcher; see `WithNotifier`.
func NewWatcher(
	ctx context.Context,
	roots *Registry,
	watched []WatchRoot,
	sink ChangeSink,
	signals *observability.Watch,
	opts ...WatcherOption,
) (*Watcher, error) {
	config := watcherConfig{newNotifier: newFsnotifyNotifier}

	for _, opt := range opts {
		if opt != nil {
			opt.apply(&config)
		}
	}

	notifier, err := config.newNotifier()
	if err != nil {
		return nil, fmt.Errorf("open the content watcher: %w", err)
	}

	routes, err := routingTable(roots, watched)
	if err != nil {
		// Closed here rather than handed back, because the caller gets no
		// watcher to close and an inotify instance nobody owns is a descriptor
		// that stays open for the life of the process.
		if closeErr := notifier.Close(); closeErr != nil {
			return nil, errors.Join(
				fmt.Errorf("open the content watcher: %w", err),
				fmt.Errorf("close the unwatched watcher: %w", closeErr),
			)
		}

		return nil, err
	}

	if signals == nil {
		signals = observability.NewWatch(nil, nil)
	}

	watcher := &Watcher{
		routing:   routes,
		campaigns: sortedRoutes(routes),
		notifier:  notifier,
		sink:      sink,
		signals:   signals,
		watched:   make(map[string]struct{}),
		stop:      make(chan struct{}),
	}

	// The first pass before the goroutine starts, so that a campaign whose root
	// is missing or unwatchable is reported at construction rather than at the
	// first edit. The reports are discarded because `AddFailed` has already
	// carried the per-directory failures to the log and the counters; the caller
	// that wants the numbers calls `Reverify`, which is idempotent and costs one
	// walk per campaign.
	for _, route := range watcher.campaigns {
		watcher.reconcile(ctx, route)
	}

	watcher.done.Add(1)

	go watcher.loop(ctx)

	return watcher, nil
}

// Close stops delivery and releases the notifier.
//
// Idempotent, because shutdown paths reach it more than once — a deferred
// close, a signal handler and the composition root's own teardown — and the
// alternative to tolerating that is a process that panics on the way out.
// Concurrent calls are safe and all of them wait for the event goroutine, which
// is what makes "Close returned" mean "no change is being delivered any more".
//
// It waits for the goroutine rather than signalling it, and a sink that blocks
// while ignoring its context holds this open. That is the contract `ChangeSink`
// states, and the alternative — returning while a change is still being handed
// to a sink — would close the watcher out from under a call in flight.
func (w *Watcher) Close() error {
	w.closeOnce.Do(func() {
		// The signal first, so the loop stops reading before the notifier's
		// channels go away. The reverse order would be a select racing a closed
		// channel and a `Notifier` whose `Close` does not close them at all, which
		// is a goroutine parked forever with nobody waiting on it visibly.
		close(w.stop)

		w.closeErr = w.notifier.Close()
	})

	w.done.Wait()

	return w.closeErr
}

// Reverify reconciles the watch set against the tree and reports what it did,
// per campaign, ordered by slug.
//
// The capability S-4.5 requires and the event stream cannot provide: a watch is
// lost silently — an inode-bound watch outliving its name, an ENOSPC that was
// never observed, a directory tree that arrived wholesale — and nothing in a
// stream of events reports a watch that is *not* arriving. The composition root
// must call this on a slow timer, and must act on the report:
//
//   - `LimitHit` or a non-empty `Failed`: the watch limit is exhausted
//     (S-4.5). Start a periodic full rescan with `Indexer.ReindexCampaign` and
//     emit `Watch.RescanFallback`.
//   - `Missing`: the campaign's content root is gone. Emit `Watch.Degraded`, and
//     keep serving the wiki (S-4.5).
//   - `Dropped`: the tree moved under the watcher, so the rows under those
//     directories are stale. Reindex the campaign.
//   - `Refused`: S-4.4 declined to descend into symlinks. Nothing to do; it is
//     reported so an operator whose pages are not appearing can see the reason.
//
// It emits `Watch.AddFailed` for every directory it could not add, and returns
// one report per campaign whether or not anything changed. A report with only
// `Kept` set is the healthy case and is worth having, because the absence of a
// report is indistinguishable from a timer that stopped firing.
func (w *Watcher) Reverify(ctx context.Context) []VerifyReport {
	reports := make([]VerifyReport, 0, len(w.campaigns))

	for _, route := range w.campaigns {
		reports = append(reports, w.reconcile(ctx, route))
	}

	return reports
}

// loop delivers events until the notifier is closed or ctx is cancelled.
//
// It owns nothing but the pending rename, and it recovers: a panic on a
// goroutine is not a failed request, it is a failed *process*, and this
// goroutine is fed by a channel the kernel fills. One panic ends the loop and
// marks every campaign degraded, because an invariant that has been broken once
// will be broken again on the next event and a loop that panics per event is a
// process with a hot spin rather than a process that is down.
func (w *Watcher) loop(ctx context.Context) {
	defer w.done.Done()
	defer w.recoverPanic(ctx)

	events := w.notifier.Events()
	failures := w.notifier.Errors()

	for {
		select {
		case <-ctx.Done():
			return

		case <-w.stop:
			return

		case event, open := <-events:
			if !open {
				return
			}

			w.deliver(ctx, event)

		case failure, open := <-failures:
			if !open {
				// Nil disables the case rather than spinning on a closed
				// channel, which would report the same nil error forever.
				failures = nil

				continue
			}

			w.reportFailure(ctx, failure)
		}
	}
}

// deliver turns one filesystem event into changes.
//
// A panic here is contained by `loop`'s recovery, and this function does not
// recover again: two layers of recovery would mean a panic in the *reporting*
// path — the one path that must not be skipped — could be swallowed by a
// handler meant to contain a panic in the routing path.
func (w *Watcher) deliver(ctx context.Context, event fsnotify.Event) {
	// The zero value a closed channel delivers. fsnotify's own documentation
	// says every event carries at least one bit, so an event with none is not an
	// event and is not something to route.
	if event.Op == 0 {
		return
	}

	absolute := filepath.Clean(event.Name)

	route, ok := w.routeFor(absolute)
	if !ok {
		// Under no campaign root. Nothing is watching it, so this cannot happen
		// with a real notifier — and it is exactly the case a test injects, which
		// is why the path is a branch rather than an assumption.
		return
	}

	rel, ok := w.relative(route, absolute)
	if !ok {
		return
	}

	// Ordered strongest first, and the order is the argument: a multi-bit event
	// is several observations of one path, and only some of them are about
	// whether the path is *there*. A `Rename` bit means it is not, whatever else
	// arrived with it; a `Remove` means the same; a `Create` and a `Write` are
	// both "it is here and may have changed", and one upsert says that as
	// cheaply as the other. So the strongest observation wins rather than every
	// bit being taken literally, which would report one atomic save as an
	// upsert, a remove and a rename and then re-index a page that is not there.
	switch {
	case event.Has(fsnotify.Rename):
		w.renamed(route, rel, absolute)
	case event.Has(fsnotify.Remove):
		w.removed(ctx, route, rel, absolute)
	case event.Has(fsnotify.Create):
		w.created(ctx, route, rel)
	case event.Has(fsnotify.Write):
		w.written(ctx, route, rel)
	default:
		// `Chmod`, and the four unportable bits, which this watcher does not
		// subscribe to. A `Chmod` is deliberately not a change: on Linux every
		// remove arrives with one attached, so treating it as a write would
		// re-index the campaign's whole tree on every delete.
	}
}

// routeFor returns the campaign an absolute path belongs to.
//
// Walks the path's ancestors and looks each one up, shallowest last. The
// alternative that lost is a scan of every campaign's prefix per event, in
// descending prefix length: it is correct — longest match wins — and it is
// linear in the number of campaigns, which is a number the operator chooses by
// registering more of them. This is linear in the depth of the path, which is a
// number the *file* chooses and which is single digits.
//
// Longest-match falls out of the walk rather than being arranged: the first
// ancestor that is a campaign root is the deepest one, so a campaign root nested
// inside another's wins without the table needing an order at all. That case is
// not hypothetical — `SEMIPLANE_CONTENT_ROOT_BASE` is operator-supplied, so two
// campaigns can be pointed at a base directory and a subdirectory of it, and a
// first-match scan would send every page of the inner campaign to the outer one.
func (w *Watcher) routeFor(absolute string) (watchRoute, bool) {
	for dir := absolute; ; {
		if route, ok := w.routing[dir]; ok {
			return route, true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the top of the filesystem without finding a root.
			return watchRoute{}, false
		}

		dir = parent
	}
}

// relative converts an absolute event path back to the root-relative,
// slash-separated form `Change` and `pages.path` both use.
//
// The routing match proves the path is *under* the root, and this proves it once
// more before the path is used, because the two are separate claims: `filepath.Rel`
// can fail (a different volume on Windows) and returns `..` for a path it
// considers unrelated, and a `..` that reached `Root.At` would be refused there
// — but it is refused as a *refusal*, which is a different answer from "no such
// page", and refusing is not what an event under a campaign root means.
func (w *Watcher) relative(route watchRoute, absolute string) (string, bool) {
	rel, err := filepath.Rel(route.dir, absolute)
	if err != nil || rel == rootDirName {
		return "", false
	}

	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}

	return filepath.ToSlash(rel), true
}

// created handles a path that appeared.
//
// The one operation whose meaning depends on *what* appeared, which is why it is
// the only one that asks the filesystem. A directory needs a watch and needs
// the subtree adopted; a refused symlink is S-4.4 and is not followed, not
// watched and not reported; a page is an upsert.
func (w *Watcher) created(ctx context.Context, route watchRoute, rel string) {
	// The dot-directory rule, applied before anything is touched. `.obsidian`
	// and `.trash` churn on every keystroke in Obsidian, and a page under a
	// dot-directory is not indexable, so a watch inside one is a descriptor
	// spent on events whose correct answer is silence — and descriptors are the
	// resource S-4.5 runs out of.
	if skipDirectory(path.Base(rel)) {
		return
	}

	kind := w.kindOf(route, rel)
	if kind == entryGone {
		// Created and gone again before the event was delivered, which is what a
		// `mv` of a freshly created file looks like. Nothing to report.
		return
	}

	if pending := w.takePending(); pending != nil {
		if w.settlePending(ctx, route, rel, kind, pending) {
			return
		}
	}

	switch kind {
	case entryRefused:
		// S-4.4. Not followed — `kindOf` already refused it through the
		// campaign's own policy rather than a second one here — and not
		// reported, because a page the read path refuses is a page that was
		// never going to render, and an index row for it would be a link to a
		// 404. This is the skip the specification asks for; the refusal is
		// visible through `VerifyReport.Refused`, which counts it on the next
		// verification.
	case entryDirectory:
		// A folder appeared, and the pages already inside it arrived with it
		// rather than as events — there was no watch on its parent to see them.
		// Adopting it reports what is inside so the index converges now instead
		// of at the next rescan.
		w.enterSubtree(ctx, route, rel, func(page string) {
			w.reportPage(ctx, route, page)
		})
	case entryFile:
		w.reportPage(ctx, route, rel)
	case entryGone:
	}
}

// written handles a path that was written to.
//
// A `Write` says the contents changed and nothing about what is at the path, so
// it does not ask the filesystem: a stat per write event on a busy vault is a
// cost paid on the hot path to learn something the answer does not change. The
// path is a page or it is not, and that is a predicate over the name.
func (w *Watcher) written(ctx context.Context, route watchRoute, rel string) {
	w.reportPage(ctx, route, rel)
}

// removed handles a path that went away.
//
// The subtree is un-watched whether or not it was a directory, because that is
// the question and the answer is cheap when the path is not a watched directory:
// `forgetSubtree` returns on the first lookup miss. Inotify has already dropped
// the watch; the bookkeeping is what has to match it.
func (w *Watcher) removed(ctx context.Context, route watchRoute, rel, absolute string) {
	w.forgetSubtree(ctx, route, absolute)

	// A removed directory's pages each carry their own remove event, so the
	// directory itself is not a change. On the backends that do not deliver them
	// — Windows, and kqueue, which report a watched directory's removal
	// without promising anything about its contents — the rows under it go stale
	// until the rescan the dropped watches tell the integrator to run.
	if indexablePath(rel) {
		w.emit(ctx, route, OpRemove, rel, "")
	}
}

// renamed handles a path that moved, and decides whether the watcher has seen
// enough to say what it moved to. It stashes the observation rather than
// reporting it; see `settlePending`.
//
// The old path is what the event carries, and there is no other half to pair it
// with that can be proven (see the package comment). So the decision is a
// narrowing: a path this watcher watches is a directory, a path the index would
// keep is a page, and anything else is a staging file or a backup that no
// consumer wants to hear about. An atomic save is exactly that third case —
// `.semiplane-…tmp` renamed over the page — which is why the temporary file
// semiplane's own write path stages through produces no change at all, and why
// the destination arrives as the plain upsert it is.
//
// No context, and that is a deliberate absence rather than an oversight: this
// function emits nothing and signals nothing. `unparam` is right that the
// parameter is always nil, and a context parameter added only to satisfy a
// convention is one the next reader has to check for a cancellation that cannot
// happen.
func (w *Watcher) renamed(route watchRoute, rel, absolute string) {
	directory := w.isWatched(absolute)

	if !directory && !indexablePath(rel) {
		return
	}

	w.pending = &pendingRename{
		route:     route,
		rel:       rel,
		absolute:  absolute,
		directory: directory,
	}
}

// settlePending decides a pending rename against the event that arrived after it,
// and reports whether it answered the event completely.
//
// The two cases that pair, and the reasoning for each, are in the package
// comment. The cases that do not pair all flush — a rename out of the campaign,
// a rename across a campaign boundary, and a rename that turns out to be
// unrelated to whatever was created next — and then say so, because the event
// that flushed them is still an event that happened.
func (w *Watcher) settlePending(
	ctx context.Context,
	route watchRoute,
	rel string,
	kind entryKind,
	pending *pendingRename,
) bool {
	// A directory moved *within* one campaign: one `OpRename`, which the indexer
	// reads as a subtree move. The only pairing attempted, because the events
	// for the pages inside a moved directory moved with the inodes and nothing
	// else will ever report them.
	if pending.directory && kind == entryDirectory && pending.route.campaignID == route.campaignID {
		w.forgetSubtree(ctx, pending.route, pending.absolute)
		w.emit(ctx, route, OpRename, rel, pending.rel)
		w.enterSubtree(ctx, route, rel, noPage)

		return true
	}

	// A page moved within one campaign: two correct simpler changes, upsert
	// first. The order is the indexer's own — the destination is indexed before
	// the source is dropped, so a failure part-way leaves the page indexed twice
	// rather than not at all, and the prune resolves the duplicate.
	if !pending.directory && kind != entryRefused && kind != entryDirectory &&
		indexablePath(rel) && pending.route.campaignID == route.campaignID {
		w.reportPage(ctx, route, rel)
		w.emit(ctx, route, OpRemove, pending.rel, "")

		return true
	}

	// A directory moved somewhere this watcher did not see the other half of:
	// across a campaign boundary, or out of the tree entirely. Its watches are
	// dead either way, and no page under it can be named from here.
	if pending.directory {
		w.forgetSubtree(ctx, pending.route, pending.absolute)

		return false
	}

	// A page that left, and the event that flushed it is not its destination.
	// An `ErrNotExist` from the indexer's delete is the ordinary answer for a
	// page that was never indexed.
	w.emit(ctx, route, OpRemove, pending.rel, "")

	return false
}

// takePending takes the pending rename, if there is one.
//
// It does not check the campaign, and that is deliberate: a rename out of one
// campaign and into another arrives as one pending and one create, and each
// half has to be answered in the campaign it belongs to.
func (w *Watcher) takePending() *pendingRename {
	pending := w.pending
	w.pending = nil

	return pending
}

// kindOf asks the confined root what a created path is.
//
// The refusal of S-4.4 is `Root`'s and not this file's: `Root.At` refuses a
// symlink the policy refuses, and `fs.WalkDir` never descends into one under
// either policy, so a link that is permitted is one the operator asked to follow
// and `os.Root` still confines it. What this adds is the one question the
// read path's answer cannot give an event handler: whether what appeared was a
// directory, because only a directory needs a watch of its own.
func (w *Watcher) kindOf(route watchRoute, rel string) entryKind {
	target, err := route.root.At(rel)
	if err != nil {
		if errors.Is(err, ErrSymlink) {
			return entryRefused
		}

		return entryGone
	}

	info, err := target.Stat()
	if err != nil {
		return entryGone
	}

	if info.IsDir() {
		return entryDirectory
	}

	return entryFile
}

// enterSubtree watches a directory that appeared, and reports the pages already
// inside it.
//
// `onPage` is the difference between adopting a subtree for the indexer and
// merely covering it with watches: a directory that arrived by a rename already
// had its rows re-keyed by the `OpRename` above it, so its pages are not
// reported again, and a directory that arrived as a creation has pages nobody
// has heard of.
func (w *Watcher) enterSubtree(
	ctx context.Context,
	route watchRoute,
	rel string,
	onPage func(string),
) {
	if skipDirectory(path.Base(rel)) {
		return
	}

	_, err := scanSubtree(route.root, rel, func(child string) error {
		return w.addWatch(ctx, route, child)
	}, onPage)
	if err != nil {
		// A subtree this watcher could not walk is a subtree it is not watching,
		// which is `watch.add_failed`'s meaning whatever the cause was.
		w.signals.AddFailed(ctx, signalID(route.campaignID), rel, err)
	}
}

// reportPage emits the upsert for a path that is a page.
//
// The one place the "is this a page" question is asked, so that no caller has to
// remember it: `indexablePath` is the same predicate `Indexer` applies, and a
// watcher that reported an upsert for a `.tmp` file or an asset would be a
// change the indexer has to be trusted to discard — which is how a path the read
// path refuses ends up in the search index (the argument `index.go` makes).
func (w *Watcher) reportPage(ctx context.Context, route watchRoute, rel string) {
	if !indexablePath(rel) {
		return
	}

	w.emit(ctx, route, OpUpsert, rel, "")
}

// noPage is the `onPage` for a subtree whose pages are already accounted for.
func noPage(string) {}

// scanSubtree walks one directory and everything under it, calling onDir for
// each directory and onPage for each page.
//
// It opens through `Root.At` rather than using `Root.List`, because `List`
// refuses a whole directory's listing when any entry is a symlink the policy
// refuses — a correct answer for a caller that asked for a *set*, and the wrong
// one here: one link in a folder would leave the folder unwatched, and whether a
// directory is watched would depend on whether it happened to contain a link.
// Refusing a symlink means not descending into it, which is exactly what
// `fs.WalkDir` does and therefore exactly what `Root.Walk` does under either
// policy, and doing that here keeps this the only enumerator that can disagree
// with the indexer's own walk out of being one.
//
// It cannot start at the campaign root: `Root.At(".")` is `ErrInvalidRef`, by
// design, so a whole-tree scan is `Root.Walk` and starts there. That is why
// `reconcile` uses `Root.Walk` and this is used only for a directory that
// arrived mid-run — which is the one case that cannot wait for the next
// verification pass, because every page inside it arrived without an event.
func scanSubtree(
	root *Root,
	rel string,
	onDir func(rel string) error,
	onPage func(page string),
) (refused int, err error) {
	// The watch goes on **before** the directory is read, and the order is the
	// whole point rather than an implementation detail. A directory that arrived
	// mid-run is adopted exactly once: whatever the listing below sees is
	// reported from the listing, and whatever happened after the watch was added
	// arrives as an event. Read first and add second leaves a window between the
	// two in which a page is in neither — it was created after the listing was
	// taken and before the watch existed — and nothing will ever report it: the
	// event is already gone and the next `Reverify` only reconciles *directories*
	// against the tree, never pages. That window is not theoretical. A test that
	// does `mkdir lore && write lore/Gate.md` loses the page whenever the write
	// lands inside it, and in production it is the ordinary `mkdir -p` + write, or
	// a sync client dropping a folder and its contents in one go.
	//
	// The cost is a duplicate: a page created between the `Add` and the listing is
	// both in the listing and the event queue. `emit` documents duplicates as
	// expected and cheap, and the alternative to one is a silently lost page, so
	// the duplicate is the right trade.
	//
	// `addErr` rather than the named `err`: the shadow check and the
	// sloppy-reassign check disagree about whether this may reuse it, and a name
	// that satisfies neither is not worth the argument. It is the watch that
	// failed, and `addErr` says so.
	addErr := onDir(rel)
	if addErr != nil {
		return 0, addErr
	}

	target, err := root.At(rel)
	if err != nil {
		return 0, fmt.Errorf("open %s to watch it: %w", rel, err)
	}

	dir, err := target.Open()
	if err != nil {
		return 0, fmt.Errorf("open %s to watch it: %w", rel, err)
	}

	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()

	if readErr != nil {
		return 0, fmt.Errorf("read %s: %w", rel, readErr)
	}

	if closeErr != nil {
		return 0, fmt.Errorf("close %s: %w", rel, closeErr)
	}

	for _, entry := range entries {
		child := path.Join(rel, entry.Name())

		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			// S-4.4: not followed, and not counted as anything else. `Refused` on
			// the verification report is how an operator learns a folder of
			// theirs is not being watched.
			refused++
		case skipDirectory(entry.Name()):
			// `node_modules` and the dot-dirs. See `created`.
		case entry.IsDir():
			deeper, recurseErr := scanSubtree(root, child, onDir, onPage)
			refused += deeper

			if recurseErr != nil {
				return refused, recurseErr
			}
		case indexablePath(child):
			onPage(child)
		}
	}

	return refused, nil
}

// reconcile brings one campaign's watch set in line with its tree.
func (w *Watcher) reconcile(ctx context.Context, route watchRoute) VerifyReport {
	report := VerifyReport{CampaignID: route.campaignID, Slug: route.slug}

	desired := make(map[string]watchTarget)

	walkErr := route.root.Walk(func(rel string, entry fs.DirEntry, err error) error {
		if err != nil {
			// S-4.4 again, and the walk's own contract: a refused symlink is
			// reported and the walk continues, because a tree scan has to reach
			// every page and a vault containing one link is a vault whose other
			// pages still need watching.
			if errors.Is(err, ErrSymlink) {
				report.Refused++

				return nil
			}

			return err
		}

		if !entry.IsDir() {
			return nil
		}

		// The root arrives as ".", which matches the leading-dot rule, so it is
		// named out loud here. Skipping it would skip the whole campaign.
		if rel != rootDirName && skipDirectory(entry.Name()) {
			return fs.SkipDir
		}

		absolute := route.absolute(rel)
		desired[absolute] = watchTarget{absolute: absolute, rel: rel}

		return nil
	})
	if walkErr != nil {
		report.Missing = errors.Is(walkErr, fs.ErrNotExist)
		report.WalkError = fmt.Errorf("walk the content root of %s: %w", route.slug, walkErr)

		// The root is gone, so every watch under it is dead. Saying so is the
		// report's job; re-establishing the set is not, because there is
		// nothing to watch yet.
		report.Dropped = w.forgetCampaign(ctx, route)

		return report
	}

	// What the notifier is actually watching, not what this watcher believes it
	// asked for. See `Notifier.WatchList`: reconciling the watcher's own
	// bookkeeping against the tree is a self-consistency check, and a check that
	// can only ever say "kept" is what makes a silently lost watch permanent.
	live := w.liveWatches()

	for _, absolute := range slices.Sorted(maps.Keys(desired)) {
		target := desired[absolute]

		if _, ok := live[absolute]; ok {
			report.Kept++

			continue
		}

		if err := w.addWatch(ctx, route, target.rel); err != nil {
			report.Failed = append(report.Failed, target.rel)
			report.LimitHit = report.LimitHit || watchLimit(err)

			continue
		}

		report.Added++
	}

	for _, dir := range w.watchedUnder(route) {
		if _, kept := desired[dir]; kept {
			continue
		}

		w.dropWatch(ctx, route, dir)

		report.Dropped++
	}

	// Forgetting the dropped paths, so the next pass does not drop them again.
	//
	// Without this the bookkeeping and the notifier disagree in the *other*
	// direction: `dropWatch` asks the notifier to stop watching a directory the
	// watcher still believes it watches, so the next `reconcile` finds the same
	// directory in `watchedUnder`, finds it absent from `desired` again, drops it
	// again, and reports `Dropped: 1` for as long as the process runs. A
	// supervisor that treats a drop as "reindex once" then reindexes on every
	// verify pass forever, and a report whose `Dropped` never reaches zero cannot
	// be read as "nothing is wrong".
	//
	// Cleared here rather than inside `dropWatch` because `dropWatch` is also
	// called on a `Remove` event, where the bookkeeping is removed by the event
	// path and a second deletion would be a no-op at best and a double decrement
	// at worst.
	w.forgetUnder(route, desired)

	return report
}

// addWatch adds one directory's watch and records it.
//
// The failure path is S-4.5's: `watch.add_failed`, at error level by rule, from
// the one function that can add a watch, so no call site can express a quieter
// level for the highest-impact silent failure in the content pipeline.
func (w *Watcher) addWatch(ctx context.Context, route watchRoute, rel string) error {
	absolute := route.absolute(rel)

	if err := w.notifier.Add(absolute); err != nil {
		w.signals.AddFailed(ctx, signalID(route.campaignID), rel, err)

		return fmt.Errorf("watch %s of %s: %w", rel, route.slug, err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	w.watched[absolute] = struct{}{}

	return nil
}

// isWatched reports whether a directory is in the watch set.
func (w *Watcher) isWatched(absolute string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	_, ok := w.watched[absolute]

	return ok
}

// liveWatches is what the notifier is actually watching, as a set of absolute
// paths.
//
// The notifier's answer rather than `isWatched`, and the difference is the whole
// reason `Reverify` exists: inotify drops a watch when the directory it is bound
// to is renamed or removed, and it does so *silently*, so the watcher's own set
// goes on claiming a directory that is no longer being observed. Reconciling that
// set against the tree would answer `Kept` for a watch that is gone, which is
// the failure mode S-4.5 exists to prevent rather than to describe.
//
// The paths are cleaned because the notifier returns whatever it was given, and
// this file always gives it cleaned absolute paths — but a notifier that
// normalises them its own way (`filepath.Clean`, an `EvalSymlinks`, a trailing
// separator stripped) must not turn every `Kept` into an `Added`, and cleaning
// here is what makes the two comparable.
func (w *Watcher) liveWatches() map[string]struct{} {
	live := make(map[string]struct{})

	for _, dir := range w.notifier.WatchList() {
		live[filepath.Clean(dir)] = struct{}{}
	}

	return live
}

// watchedUnder returns the watched directories under a campaign's root, sorted.
//
// Sorted because this is the set whose difference from the tree decides whether
// a verification report is `Kept` or `Dropped`, and a report that reorders
// between two runs over the same tree is one nobody can diff.
func (w *Watcher) watchedUnder(route watchRoute) []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	found := make([]string, 0, len(w.watched))

	for dir := range w.watched {
		if dir == route.dir || isBelow(dir, route.dir) {
			found = append(found, dir)
		}
	}

	slices.Sort(found)

	return found
}

// forgetSubtree drops every watch at or under a directory.
//
// The early return is the point: a removed *file* is not a watched directory,
// and the common case should cost one map lookup rather than a scan of the
// whole watch set.
func (w *Watcher) forgetSubtree(ctx context.Context, route watchRoute, absolute string) {
	w.mu.Lock()
	_, watched := w.watched[absolute]
	w.mu.Unlock()

	if !watched {
		return
	}

	stale := make([]string, 0, len(w.watched))

	w.mu.Lock()

	for dir := range w.watched {
		if dir == absolute || isBelow(dir, absolute) {
			stale = append(stale, dir)
		}
	}

	w.mu.Unlock()

	for _, dir := range stale {
		w.dropWatch(ctx, route, dir)
	}

	w.mu.Lock()

	for _, dir := range stale {
		delete(w.watched, dir)
	}

	w.mu.Unlock()
}

// forgetCampaign drops every watch of one campaign, and reports how many.
//
// The count is returned rather than logged because the caller is `reconcile`,
// whose whole output is a report, and a dropped watch the operator cannot see in
// the report is a dropped watch nobody will know to reindex around.
func (w *Watcher) forgetCampaign(ctx context.Context, route watchRoute) int {
	held := w.watchedUnder(route)

	for _, dir := range held {
		w.dropWatch(ctx, route, dir)

		w.mu.Lock()
		delete(w.watched, dir)
		w.mu.Unlock()
	}

	return len(held)
}

// dropWatch removes one watch and forgets it.
//
// A path that was never watched, and a watcher that is already closed, are not
// failures: the first is what a concurrent re-verify produces and the second is
// what shutdown produces, and both mean the same thing, which is that there is
// no watch to remove. Anything else is a watch that is still there and will have
// to be re-added, which is `watch.add_failed`'s meaning.
// forgetUnder removes from the watcher's bookkeeping every directory under a
// campaign's root that the reconciliation no longer wants.
//
// `desired` is the set of directories the last walk found, keyed by absolute
// path. Anything this watcher claims and `desired` does not name is a watch that
// should not exist, and leaving it claimed is what makes a drop repeat on every
// pass.
func (w *Watcher) forgetUnder(route watchRoute, desired map[string]watchTarget) {
	stale := w.watchedUnder(route)

	w.mu.Lock()

	for _, dir := range stale {
		if _, keep := desired[dir]; !keep {
			delete(w.watched, dir)
		}
	}

	w.mu.Unlock()
}

func (w *Watcher) dropWatch(ctx context.Context, route watchRoute, absolute string) {
	err := w.notifier.Remove(absolute)
	if err == nil || errors.Is(err, fsnotify.ErrNonExistentWatch) ||
		errors.Is(err, fsnotify.ErrClosed) {
		return
	}

	w.signals.AddFailed(ctx, signalID(route.campaignID), route.relOf(absolute), err)
}

// emit hands one change to the sink.
//
// No filtering and no rate limiting: the settle filter (S-4.3) is what decides
// when a change has stopped happening, and it is downstream of here by
// construction. Duplicates are expected and cheap — an atomic save, a file
// created by a sync client and then written by an editor, an adopted subtree
// reporting pages whose own create events are still in the channel — and the
// indexer's answer to a change it has already applied is to hash the page and
// write nothing.
func (w *Watcher) emit(
	ctx context.Context,
	route watchRoute,
	operation Op,
	rel, oldRel string,
) {
	if w.sink == nil {
		return
	}

	w.sink(ctx, Change{
		CampaignID: route.campaignID,
		Slug:       route.slug,
		Op:         operation,
		Path:       rel,
		OldPath:    oldRel,
	})
}

// reportFailure reports an error from the notifier's error channel.
//
// Attributed to every campaign rather than to none: the notifier is one
// instance shared by all of them, its errors carry no path, and an inotify
// queue overflow (`fsnotify.ErrEventOverflow`) means every campaign's delivery
// is suspect. Naming them one line each is what makes the counter per campaign
// true and tells the integrator exactly which rescan fallbacks to start.
func (w *Watcher) reportFailure(ctx context.Context, failure error) {
	for _, route := range w.campaigns {
		w.signals.AddFailed(ctx, signalID(route.campaignID), "", failure)
	}
}

// recoverPanic reports a panic on the event goroutine and lets it end the loop.
//
// `Watch.Degraded` for every campaign rather than `AddFailed`, because the
// condition is precisely `Degraded`'s: a watcher that has stopped delivering
// events, for a reason that is a bug here rather than an exhausted limit. And
// the loop stops rather than continuing, because the loop's invariants have
// been shown wrong once and there is no version of continuing that is safer
// than stopping and letting the rescan fallback take over.
func (w *Watcher) recoverPanic(ctx context.Context) {
	if recovered := recover(); recovered == nil {
		return
	}

	for _, route := range w.campaigns {
		w.signals.Degraded(ctx, signalID(route.campaignID), "watch_panic")
	}
}

// routingTable builds the prefix routing table, and refuses a table that would
// make an event's campaign a matter of chance.
//
// The two cross-checks against the registry are startup errors rather than
// warnings. A `WatchRoot` for a campaign the registry does not retain names a
// directory nobody opened, and a retained campaign with no `WatchRoot` is a
// campaign whose pages silently stop being indexed — the same shape of failure
// as a watch that was never added, and it is only invisible if it is allowed to
// be.
func routingTable(roots *Registry, watched []WatchRoot) (map[string]watchRoute, error) {
	ordered := slices.SortedFunc(slices.Values(watched), func(one, other WatchRoot) int {
		return strings.Compare(one.Slug, other.Slug)
	})

	routing := make(map[string]watchRoute, len(ordered))
	slugs := make(map[string]struct{}, len(ordered))

	for _, entry := range ordered {
		if entry.Slug == "" {
			return nil, errors.New("content: a watched root has no slug")
		}

		if _, duplicate := slugs[entry.Slug]; duplicate {
			return nil, fmt.Errorf("content: %s is watched twice", entry.Slug)
		}

		if !filepath.IsAbs(entry.Dir) {
			return nil, fmt.Errorf("content: the content root of %s is not absolute", entry.Slug)
		}

		dir := filepath.Clean(entry.Dir)

		// Two campaigns at one directory would make every event's campaign a
		// coin toss, so it is refused here rather than resolved by map order.
		if _, taken := routing[dir]; taken {
			return nil, fmt.Errorf("content: two campaigns are watched at %s", dir)
		}

		root, err := roots.Get(entry.Slug)
		if err != nil {
			return nil, fmt.Errorf("content: cannot watch %s: %w", entry.Slug, err)
		}

		slugs[entry.Slug] = struct{}{}

		routing[dir] = watchRoute{
			campaignID: entry.CampaignID,
			slug:       entry.Slug,
			dir:        dir,
			root:       root,
		}
	}

	for _, slug := range roots.Slugs() {
		if _, covered := slugs[slug]; !covered {
			return nil, fmt.Errorf("content: %s has a content root but is not watched", slug)
		}
	}

	return routing, nil
}

// sortedRoutes returns the routing table's campaigns ordered by slug.
func sortedRoutes(routing map[string]watchRoute) []watchRoute {
	routes := make([]watchRoute, 0, len(routing))

	for _, route := range routing {
		routes = append(routes, route)
	}

	slices.SortFunc(routes, func(one, other watchRoute) int {
		return strings.Compare(one.slug, other.slug)
	})

	return routes
}

// absolute converts a root-relative path into the host path the notifier takes.
//
// The one conversion from a confined path to an absolute one, and it is a join
// rather than a splice: `rel` came out of `Root.At` or out of the routing
// walk, so it is already cleaned and already inside the root, and joining cannot
// make it anything else.
func (r watchRoute) absolute(rel string) string {
	return filepath.Join(r.dir, filepath.FromSlash(rel))
}

// relOf converts a host path back into a root-relative one for a signal.
func (r watchRoute) relOf(absolute string) string {
	rel, err := filepath.Rel(r.dir, absolute)
	if err != nil {
		return ""
	}

	return filepath.ToSlash(rel)
}

// isBelow reports whether a path is strictly inside a directory.
func isBelow(dir, parent string) bool {
	return strings.HasPrefix(dir, parent+string(filepath.Separator))
}

// watchLimit reports whether an error is the watch limit rather than a
// particular path's problem.
//
// S-4.5's condition, and the one the integrator has to act on, because the
// difference between it and any other failure is that it does not go away: a
// path that could not be watched is retried on the next pass, while an exhausted
// limit means every subsequent `Add` fails too. ENFILE is included because
// kqueue spends a descriptor per watched file and per watched directory's
// entries, and the operator's response — a larger limit, or fewer trees — is
// the same for all three.
func watchLimit(err error) bool {
	return errors.Is(err, syscall.ENOSPC) ||
		errors.Is(err, syscall.EMFILE) ||
		errors.Is(err, syscall.ENFILE)
}

// signalID renders a campaign id for the observability surface, which carries it
// as a string.
//
// A conversion rather than a change of type: `Change.CampaignID` is an `int64`
// because the index is keyed by one, and `EventAttributes.CampaignID` is a
// string because an event line is text. Neither is the wrong type; they are
// two vocabularies for the same id.
func signalID(campaignID int64) string {
	return strconv.FormatInt(campaignID, 10)
}

// fsnotifyNotifier adapts fsnotify's channel-shaped Watcher to Notifier.
//
// A struct rather than a type alias because `fsnotify.Watcher` publishes
// `Events` and `Errors` as fields, and `Notifier` wants methods: a field cannot
// satisfy an interface without the adapter.
type fsnotifyNotifier struct {
	watcher *fsnotify.Watcher
}

// Add starts watching a directory, naming the call in any error it returns.
//
// The wrapping is not ceremony. These three errors reach `AddFailed`, whose
// `Detail` is an error *class* — `watch_limit`, `ENOENT`, `EBADF` — computed by
// `errors.Is` against the sentinel and the errno. `%w` keeps that chain intact
// so the class survives the wrap, and the prefix says which of the three calls
// failed, because an operator reading `watch.add_failed` with no path cannot
// otherwise tell a failed `Add` from a `Close` that raced shutdown.
func (n fsnotifyNotifier) Add(dir string) error {
	if err := n.watcher.Add(dir); err != nil {
		return fmt.Errorf("fsnotify add watch for %s: %w", dir, err)
	}

	return nil
}

// Remove stops watching a directory.
func (n fsnotifyNotifier) Remove(dir string) error {
	if err := n.watcher.Remove(dir); err != nil {
		return fmt.Errorf("fsnotify remove watch for %s: %w", dir, err)
	}

	return nil
}

// Close drops every watch and closes the channels.
func (n fsnotifyNotifier) Close() error {
	if err := n.watcher.Close(); err != nil {
		return fmt.Errorf("fsnotify close: %w", err)
	}

	return nil
}

func (n fsnotifyNotifier) Events() <-chan fsnotify.Event {
	return n.watcher.Events
}

func (n fsnotifyNotifier) Errors() <-chan error {
	return n.watcher.Errors
}

func (n fsnotifyNotifier) WatchList() []string {
	return n.watcher.WatchList()
}
