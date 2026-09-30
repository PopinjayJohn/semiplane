// Package observability is the whole of semiplane's observability surface: a
// counter registry, and typed event logging.
//
// There is no metrics server and no tracing stack, by decision (ADR 0003,
// S-12.1). What exists instead is a `slog` handler with a stable `event` key —
// greppable, alertable-on — and a set of counters rendered as JSON on `/readyz`.
//
// The package exists as a package rather than as helpers scattered across the
// subsystems because the invariants are collective: one spelling per event
// name, one place that decides what a log line may carry, one place that knows
// the full set of counters so a later phase cannot silently invent a spelling.
package observability

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
)

// EventName is a stable, greppable identifier for a class of event.
//
// The `event` key in a log line is this value, never the message. Messages get
// reworded; a dotted name that changes is a silent break for every dashboard
// and alert built on it.
type EventName string

// The §13.2 signals, in the order the architecture record lists them. Each
// subsystem that owns one registers it in the composition root; the constants
// exist so the spelling is written once.
//
// This is a const list rather than a registry of live counters, because the
// counters themselves arrive with the subsystem that increments them. A name
// that is declared but never incremented is a gap the `/readyz` surface makes
// visible, which is the point.
//
// Three of these are never below error level (S-12.2):
// EventWatchAddFailed, EventContentRenderError and EventSecretReconcileCapped.
// The last is security-relevant — it is the system saying it *chose* to hide
// something and a sync fight is unresolved.
const (
	// EventWatchAddFailed is a watcher that has stopped watching. The
	// highest-impact silent failure in the system.
	EventWatchAddFailed EventName = "watch.add_failed"

	EventWatchDegraded       EventName = "watch.degraded"
	EventWatchRecovered      EventName = "watch.recovered"
	EventWatchRescanFallback EventName = "watch.rescan_fallback"

	// EventContentStableReadTimeout is a writer that appears stuck mid-write.
	EventContentStableReadTimeout EventName = "content.stable_read_timeout"

	// EventContentRenderError is a page that would not render. Never serve a
	// broken page silently.
	EventContentRenderError EventName = "content.render_error"

	// EventCacheHit and EventCacheMiss are cache outcomes, by tier. A
	// permanently zero include_secrets hit rate means the GM's view is being
	// regenerated on every request.
	EventCacheHit  EventName = "cache.hit"
	EventCacheMiss EventName = "cache.miss"

	// EventConflict412 is a stale editor save. An elevated rate means Obsidian
	// and the web editor are fighting.
	EventConflict412 EventName = "conflict.412"

	// EventSecretReverted is sync undoing a reveal — the single most surprising
	// failure this system can have.
	EventSecretReverted EventName = "secret.reverted"

	// EventSecretAnchorDrift is a ledger re-association after a rename.
	EventSecretAnchorDrift EventName = "secret.anchor_drift"

	// EventSecretReconcileCapped is reconciliation exhausting its budget and
	// leaving a secret hidden. Every failure path resolves toward hiding.
	EventSecretReconcileCapped EventName = "secret.reconcile_capped"

	// EventWSConnected, EventWSClosed and EventWSStale are hub gauges. A rising
	// closed/stale pair against a flat connected count is a leak.
	EventWSConnected EventName = "ws.connected"
	EventWSClosed    EventName = "ws.closed"
	EventWSStale     EventName = "ws.stale"

	// EventStateWriteMs is debounced persistence latency; a spike means the
	// writer is contended.
	EventStateWriteMs EventName = "state.write_ms"

	// EventPluginMissing and EventPluginVersionMismatch are plugin resolution
	// failures. Both mean a campaign cannot start its game, and its wiki must
	// still serve.
	EventPluginMissing         EventName = "plugin.missing"
	EventPluginVersionMismatch EventName = "plugin.version_mismatch"
)

// AllEventNames is every §13.2 signal name, in declaration order.
//
// Exported so a test can assert the list is complete and unique, and so a later
// phase registering a counter can check its spelling against it rather than
// against a comment.
func AllEventNames() []EventName {
	return slices.Clone(eventNames)
}

var eventNames = []EventName{
	EventWatchAddFailed,
	EventWatchDegraded,
	EventWatchRecovered,
	EventWatchRescanFallback,
	EventContentStableReadTimeout,
	EventContentRenderError,
	EventCacheHit,
	EventCacheMiss,
	EventConflict412,
	EventSecretReverted,
	EventSecretAnchorDrift,
	EventSecretReconcileCapped,
	EventWSConnected,
	EventWSClosed,
	EventWSStale,
	EventStateWriteMs,
	EventPluginMissing,
	EventPluginVersionMismatch,
}

// Counter is a monotonically increasing value, and optionally a gauge that can
// also go down.
//
// The distinction is real: a connection count that only ever increments reports
// a steadily growing number and never tells you a hub is leaking, which is the
// exact failure ws.closed and ws.stale exist to catch.
type Counter struct {
	name  string
	total atomic.Int64
	now   atomic.Int64
}

// NewCounter returns a counter with the given name. Names are the §13.2 dotted
// spellings.
func NewCounter(name string) *Counter {
	return &Counter{name: name}
}

// Name returns the counter's stable identifier.
func (c *Counter) Name() string { return c.name }

// Inc adds one.
func (c *Counter) Inc() { c.total.Add(1) }

// Add adds a delta. Negative deltas are ignored: silently decrementing a
// monotonic counter produces a number that looks like traffic and is not.
func (c *Counter) Add(delta int64) {
	if delta < 0 {
		return
	}

	c.total.Add(delta)
}

// SetGauge records an absolute current value, and leaves the total alone.
func (c *Counter) SetGauge(value int64) { c.now.Store(value) }

// Snapshot returns the counter's current state.
//
// Two values rather than one because a gauge and a total answer different
// questions, and collapsing them means one of the two is always wrong. A
// counter that has never been gauged reports a zero current value, which is
// honest rather than absent.
func (c *Counter) Snapshot() CounterValue {
	return CounterValue{Total: c.total.Load(), Current: c.now.Load()}
}

// CounterValue is one counter's state at a point in time.
type CounterValue struct {
	// Total is the monotonic count of events.
	Total int64 `json:"total"`
	// Current is the gauge reading, or 0 for a pure counter.
	Current int64 `json:"current"`
}

// Registry holds the process's counters and renders them for `/readyz`.
//
// Absent counters are emitted as zero rather than omitted. The reason is that
// omission makes the response shape a function of which subsystems happen to be
// wired: a consumer would have to handle a key appearing and disappearing
// across versions, and a counter that reads 0 is a fact while a counter that
// is absent is a question. The list of what *should* exist is `AllEventNames`,
// so "not implemented yet" is visible as a zero rather than hidden.
type Registry struct {
	mu       sync.RWMutex
	counters map[string]*Counter
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{counters: make(map[string]*Counter)}
}

// Register adds a counter, returning the registry so registration reads as one
// expression in the composition root. Registering the same name twice returns
// the existing counter rather than replacing it, so a subsystem that registers
// twice cannot lose the count it accumulated the first time.
func (r *Registry) Register(counter *Counter) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.counters[counter.Name()]; !exists {
		r.counters[counter.Name()] = counter
	}

	return r
}

// Counter returns a registered counter by name.
func (r *Registry) Counter(name string) (*Counter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	counter, ok := r.counters[name]

	return counter, ok
}

// Snapshot returns every counter's state, keyed by name.
//
// Sorted by name, not map order. A JSON body that reorders between two calls
// with nothing having changed is a body a test cannot compare and a human
// cannot diff.
func (r *Registry) Snapshot() map[string]CounterValue {
	r.mu.RLock()
	defer r.mu.RUnlock()

	values := make(map[string]CounterValue, len(r.counters))
	for name, counter := range r.counters {
		values[name] = counter.Snapshot()
	}

	return values
}

// SortedNames returns the registered counter names in sorted order.
func (r *Registry) SortedNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.counters))
	for name := range r.counters {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

// EventAttributes describes what a log line is allowed to carry.
//
// The type is the point. S-12.3 says no event carries secret content, file
// contents, or dice results, and a free-form `slog.Attr` bag makes that
// convention rather than a constraint: `slog.Any("detail", pageBytes)` compiles
// and ships. Here there is no field to put a `[]byte` or a rendered page into,
// so the invariant holds without a reviewer having to notice anything.
//
// Fields are deliberately few. `Detail` is a short, human-meaningful
// discriminator — a filename's base, an error class, a status code — and the
// comment on it is the contract.
type EventAttributes struct {
	// CampaignID is the tenant the event concerns, when it concerns one.
	CampaignID string

	// Path is the content path, when the event concerns one. Base name or a
	// short relative path; never file contents.
	Path string

	// Detail is a short discriminator: an error class, a retry count, a token
	// id. Never secret text, never a file body, never a dice result.
	Detail string
}

// Event logs a named event with its permitted attributes.
//
// The `event` key carries the stable name so a log query never has to match on
// prose. The level is chosen by the caller, because the three security-relevant
// events are errors by rule (S-12.2) and letting each call site choose would
// make that rule advisory.
func Event(
	ctx context.Context,
	logger *slog.Logger,
	name EventName,
	level slog.Level,
	attrs EventAttributes,
) {
	if logger == nil {
		return
	}

	record := []slog.Attr{slog.String("event", string(name))}

	if attrs.CampaignID != "" {
		record = append(record, slog.String("campaign_id", attrs.CampaignID))
	}

	if attrs.Path != "" {
		record = append(record, slog.String("path", attrs.Path))
	}

	if attrs.Detail != "" {
		record = append(record, slog.String("detail", attrs.Detail))
	}

	logger.LogAttrs(ctx, level, string(name), record...)
}
