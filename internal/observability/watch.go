// The content-watcher subsystem's observability surface.
//
// The watcher, the settle filter and the indexer are three components with one
// author's worth of shared mistakes between them, and the mistakes that matter
// here are all the same shape: something stopped working and the process still
// answers `/healthz`. So the signals live here rather than at the call sites.
// One method per §13.2 signal means the level, the spelling and the set of
// attributes an event may carry are decided once, in a file whose whole subject
// is that decision.
//
// # Why typed methods instead of the Event function
//
// `Event` takes a `slog.Level`, and that is the right shape for a package that
// only offers the level rules as comments — because a comment is advisory.
// S-12.2 says `watch.add_failed` and `content.render_error` are never below
// error, and the way that rule stops being advisory is that a call site cannot
// express a lower level for them. `w.AddFailed(ctx, id, path, err)` has no
// level to get wrong; `Event(ctx, logger, EventWatchAddFailed, slog.LevelDebug,
// ...)` does, and a reviewer has to catch it.
//
// # Why the state-shaped counters are gauges
//
// `watch.degraded`, `watch.recovered` and `watch.rescan_fallback` describe a
// condition a watcher is *in*, not a thing that happened. A counter alone would
// answer "how many times has this happened", which for a watcher that has been
// degraded since the container started is a number that stops moving while
// everything is wrong — and an operator watching a graph for "is anything
// broken right now" is watching `current`, not `total`. That is the whole
// reason `Counter` has two values (`S-12.1` counters are rendered as JSON on
// `/readyz`, and both fields are rendered): `total` answers "how often", and
// without `current` the answer to "how many watchers are degraded at this
// moment" does not exist on the surface at all.
//
// The gauge is a *count of campaigns in the state*, not a boolean, because one
// process watches many campaigns and a per-process flag would report a
// single healthy campaign as an unhealthy system.
//
// # What an event may carry
//
// `EventAttributes` has three string fields and no bag, and this file adds
// nothing to it — no `[]byte`, no `any`, no `map[string]any` "context". S-12.3
// is enforced by that type rather than by review, and the one place where an
// attacker-influenced string could reach a line is the error text, which
// `ErrorClass` reduces to a class. An error from a Markdown parser quotes the
// line it choked on, and that line is frequently a `[!secret]` callout body, so
// `err.Error()` is never passed through.
//
// `Detail` from a caller is the one remaining input and it is the caller's
// discipline, not this type's guarantee: it is a discriminator — an error
// class, a reason, a count — and a caller who finds themselves wanting to write
// a sentence into it has found a missing event name instead.

package observability

import (
	"context"
	"log/slog"
	"sync"
)

// watchState is a set of the state-shaped conditions one campaign's watcher can
// be in, tracked so the gauges on `watch.degraded` and `watch.rescan_fallback`
// report how many campaigns are in the state rather than how many ever entered
// it.
//
// A bitmask because the two conditions overlap in practice — a watcher that has
// lost a watch is both degraded and in the rescan fallback — and because
// `Recovered` clears them together, which needs to know what was set.
type watchState uint8

const (
	// stateDegraded is a watcher that cannot see its content root: the root is
	// missing, or an inotify error stopped delivery.
	stateDegraded watchState = 1 << iota

	// stateFallback is a watcher delivering changes by periodic full rescan
	// instead of by filesystem event. S-4.5's degradation mode.
	stateFallback
)

// Watch is the watcher subsystem's §13.2 surface: the seven counters it registers
// and the six typed emitters the watcher, the settle filter and the indexer
// call.
//
// # Construction
//
// One per process, built by the composition root and shared. The counters are
// registered so `/readyz` renders them as zeros even before anything fails,
// which is the point of S-12.1's surface: an unwired signal reads as a visible
// zero rather than as an absent key.
//
// Constructing a second `Watch` over the same registry is supported for the
// counters — a second registration reuses the first `Counter`, so no count is
// lost — but *not* for the gauges, whose bookkeeping lives here rather than in
// the counter. Two values would each move the shared gauge for the same
// transition and double it. A nil registry is tolerated rather than fatal, in
// keeping with the rest of this package: the counters still count, they are
// simply not on `/readyz`.
type Watch struct {
	logger *slog.Logger

	addFailed         *Counter
	degraded          *Counter
	recovered         *Counter
	rescanFallback    *Counter
	stableReadTimeout *Counter
	settleFailed      *Counter
	renderError       *Counter

	// mu guards state. It is not held while logging: a handler that blocks
	// must not stop a second campaign's watcher from reporting that it is
	// broken.
	mu    sync.Mutex
	state map[string]watchState
}

// NewWatch registers the watcher's seven counters and returns the surface.
//
// `registry` may be nil and `logger` may be nil. Both are tolerated for the
// same reason and with the same cost: logging must not be the thing that takes
// down a watcher, and a missing line is recoverable where a panic during a
// filesystem event is not.
func NewWatch(registry *Registry, logger *slog.Logger) *Watch {
	watch := &Watch{
		logger:            logger,
		addFailed:         NewCounter(string(EventWatchAddFailed)),
		degraded:          NewCounter(string(EventWatchDegraded)),
		recovered:         NewCounter(string(EventWatchRecovered)),
		rescanFallback:    NewCounter(string(EventWatchRescanFallback)),
		stableReadTimeout: NewCounter(string(EventContentStableReadTimeout)),
		settleFailed:      NewCounter(string(EventContentSettleFailed)),
		renderError:       NewCounter(string(EventContentRenderError)),
		state:             make(map[string]watchState),
	}

	if registry != nil {
		registry.
			Register(watch.addFailed).
			Register(watch.degraded).
			Register(watch.recovered).
			Register(watch.rescanFallback).
			Register(watch.stableReadTimeout).
			Register(watch.settleFailed).
			Register(watch.renderError)
	}

	return watch
}

// AddFailed reports a path the watcher could not watch, and is the only signal
// here that is an error by rule rather than by judgment.
//
// S-12.2 fixes it at error because a watcher that has stopped watching looks
// exactly like a quiet vault: the process is up, `/healthz` is green, every
// route answers, and the campaign silently stops seeing edits. Warn was the
// alternative and it lost — a warn-level line in a container that logs a lot is
// a line nobody reads, and this is the highest-impact silent failure in the
// content pipeline.
//
// It deliberately does *not* mark the campaign degraded. A watch that could not
// be added is often retried successfully on the next sweep, and a gauge that
// latches on a transient would report a broken system where the caller is
// about to report a working one. `Degraded` is the caller's to emit, at the
// moment it actually gives up.
//
// `err` may be nil: exhausting the watch limit is a condition rather than a
// failed read, and there is no error value to hand over. `path` may be empty
// when the failure is about the limit rather than a file.
func (w *Watch) AddFailed(ctx context.Context, campaignID, path string, err error) {
	w.addFailed.Inc()

	Event(ctx, w.logger, EventWatchAddFailed, slog.LevelError, EventAttributes{
		CampaignID: campaignID,
		Path:       path,
		Detail:     ErrorClass(err),
	})
}

// Degraded reports a watcher that has stopped delivering events for a campaign
// and is falling back to periodic full rescan.
//
// Warn, not error, and that is a deliberate demotion of a loud-sounding event:
// S-4.5 requires the server to keep starting and keep serving when a content
// root is missing, so this is a condition the system is designed to live with
// rather than a failure it must be woken for. The error level lives on the
// event that precedes it — `AddFailed` — and an alert belongs there.
//
// The gauge moves to one-per-campaign-in-state. Repeating the call for a
// campaign already in the state does not double it: a watcher that re-reports
// on every failed sweep is the normal case, not an error in the caller.
func (w *Watch) Degraded(ctx context.Context, campaignID, detail string) {
	w.degraded.Inc()
	w.enter(campaignID, stateDegraded, w.degraded)

	Event(ctx, w.logger, EventWatchDegraded, slog.LevelWarn, EventAttributes{
		CampaignID: campaignID,
		Detail:     detail,
	})
}

// Recovered reports a watcher that is delivering events again.
//
// Info rather than warn or error, and this is the one signal here whose level
// is *below* its apparent importance. A recovery is good news and an operator
// acts on it by closing an incident, not by looking at a pager; emitting it at
// warn would make every routine recovery look like something that needs a
// decision, which is how alert fatigue starts.
//
// It clears both state gauges for the campaign. A watcher that is watching again
// is neither degraded nor in the rescan fallback, and leaving the fallback
// gauge set would keep reporting a fallback that is no longer running — the
// half-resolved gauge that makes a counter surface worse than none.
func (w *Watch) Recovered(ctx context.Context, campaignID, detail string) {
	w.recovered.Inc()
	w.leave(campaignID)

	Event(ctx, w.logger, EventWatchRecovered, slog.LevelInfo, EventAttributes{
		CampaignID: campaignID,
		Detail:     detail,
	})
}

// RescanFallback reports that a watcher has switched from filesystem events to
// periodic full rescan, which is S-4.5's degradation mode.
//
// Warn, for the same reason as `Degraded`: the wiki still serves and edits
// still arrive, late. Gauged the same way, per campaign and once per entry.
func (w *Watch) RescanFallback(ctx context.Context, campaignID, detail string) {
	w.rescanFallback.Inc()
	w.enter(campaignID, stateFallback, w.rescanFallback)

	Event(ctx, w.logger, EventWatchRescanFallback, slog.LevelWarn, EventAttributes{
		CampaignID: campaignID,
		Detail:     detail,
	})
}

// StableReadTimeout reports that the settle filter gave up waiting for a path's
// size to stop changing.
//
// It means the confirmation expired, not that it succeeded. S-4.3 requires both
// a per-path debounce and a size-stable confirmation across two `stat` samples,
// and this is the second one running out: something is holding the file open
// mid-write, or writing far slower than the bound allows. The path is still
// indexed with the bytes that were readable, which is why this is a warn rather
// than an error — a page is served, and possibly a truncated one, which
// `RenderError` or a later re-read is what surfaces. Error was the alternative
// and it lost: a slow network sync client would page an operator for a write
// that completes.
func (w *Watch) StableReadTimeout(ctx context.Context, campaignID, path string) {
	w.stableReadTimeout.Inc()

	Event(ctx, w.logger, EventContentStableReadTimeout, slog.LevelWarn, EventAttributes{
		CampaignID: campaignID,
		Path:       path,
	})
}

// SettleFailed reports a path the settle filter could not `stat` for a reason
// other than its absence.
//
// The absence case is a settled removal and is not reported here: a file that was
// deleted is a change, and it is delivered as one. This is the other branch —
// EACCES after a permission change, EIO, a vanished mount — where the filter has
// no answer and drops the path. The page stays unindexed until the next rescan,
// which is a repairable state, but *silently* unindexed is not: from outside it is
// indistinguishable from a vault nobody is editing.
//
// Warn, and deliberately not merged into `StableReadTimeout`. That one means the
// confirmation ran out of budget on a file it could see; this one means the
// filter could not look at the file at all. Different causes, different fixes,
// and an operator reading the first would not think to check the filesystem.
func (w *Watch) SettleFailed(ctx context.Context, campaignID, path string, err error) {
	w.settleFailed.Inc()

	Event(ctx, w.logger, EventContentSettleFailed, slog.LevelWarn, EventAttributes{
		CampaignID: campaignID,
		Path:       path,
		Detail:     ErrorClass(err),
	})
}

// RenderError reports a page that would not render.
//
// Error by rule (S-12.2), and the rule exists because of what the alternative
// looks like: a page that fails to render has to be served as *something*, and
// the something a request handler reaches for is a cached or empty body. That
// is a GM looking at a missing page and a player looking at a missing page, and
// neither is visibly a failure unless something said so. So `err` is logged as
// its class and never as its text — see `ErrorClass`.
func (w *Watch) RenderError(ctx context.Context, campaignID, path string, err error) {
	w.renderError.Inc()

	Event(ctx, w.logger, EventContentRenderError, slog.LevelError, EventAttributes{
		CampaignID: campaignID,
		Path:       path,
		Detail:     ErrorClass(err),
	})
}

// enter marks a campaign as having entered a state and moves the counter's
// gauge, once per campaign per state.
//
// The idempotence is the point: the gauge is a count of campaigns currently in
// the state, and a watcher that reports the same degradation on every failed
// sweep must not inflate it into a count of sweeps. `campaignID` empty is not
// tracked, because a gauge keyed on a campaign that does not exist cannot be
// maintained — there is no recovery to clear it — and a gauge that only ever
// rises is the failure this file exists to avoid.
func (w *Watch) enter(campaignID string, state watchState, counter *Counter) {
	if campaignID == "" {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	current, known := w.state[campaignID]
	if known && current&state != 0 {
		return
	}

	w.state[campaignID] = current | state

	gauge(counter, 1)
}

// leave clears every state for a campaign and drops the entry.
//
// The whole entry goes rather than just its bits because `stateDegraded` and
// `stateFallback` are the only two states, and keeping an entry that holds
// neither would mean one map entry per campaign that ever degraded, forever.
func (w *Watch) leave(campaignID string) {
	if campaignID == "" {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	current, known := w.state[campaignID]
	if !known {
		return
	}

	if current&stateDegraded != 0 {
		gauge(w.degraded, -1)
	}

	if current&stateFallback != 0 {
		gauge(w.rescanFallback, -1)
	}

	delete(w.state, campaignID)
}

// gauge applies a delta to a counter's current value, clamped at zero.
//
// The clamp is unreachable through one `Watch` — `enter` and `leave` are
// balanced per campaign — and it exists for the documented case of two values
// over one registry, where a shared counter can be decremented without a
// matching increment. A negative gauge is a number no operator can act on and
// it casts doubt on every other number in the body, so it is not worth being
// certain it cannot happen.
func gauge(counter *Counter, delta int64) {
	counter.SetGauge(max(counter.Snapshot().Current+delta, 0))
}
