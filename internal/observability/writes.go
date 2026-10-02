// The `state.write_ms` surface: the write-latency histogram and the gauge beside it.
//
// # Why a histogram and not a counter
//
// `state.write_ms` is §13.2's entry for the realtime plane's debounced
// persistence, and a count of writes answers "how often has this happened" — which
// for a debounced writer is a function of how long the process has been up, and
// says nothing about whether the writer is healthy. What an operator asks at three
// in the morning is "how slow is persistence getting", and that question needs a
// distribution: a mean hides a bimodal writer (fast when the queue is empty, slow
// when it is not) and a maximum alone is one SQLite checkpoint away from paging.
//
// The histogram is the same shape as `Watch`'s state gauges and not a new kind of
// thing: it holds numbers, it is registered into the same `Registry`, and it does
// not answer a question the counters cannot.
//
// # The two numbers, and why the gauge is failures rather than latency
//
// `Counter` has two values and this surface uses both, for two different questions.
//
//   - `Total` counts observations: one per write attempt, whatever its outcome. A
//     permanently zero rate means the debounce has stopped, which is a different
//     failure from a slow write and a louder one.
//   - `Current` counts **campaigns whose most recent write failed**, not a latency.
//     The reason is the one `watch.go` states for its own state gauges: an operator
//     watching a graph for "is anything broken right now" is watching `current`. A
//     failed write is the condition that persists — `state.go` leaves the state
//     dirty and re-arms the window, so the next attempt carries it — and a campaign
//     that has been failing to persist for an hour is the case worth paging over. A
//     last-latency gauge would sit at a stale value from a writer that is no longer
//     writing, and would read as healthy.
//
// # Latency on the log line without a new attribute
//
// `EventAttributes` has no duration field and this file does not add one: the type's
// whole purpose is that S-12.3 holds by construction, and a `time.Duration` is the
// first field a caller would eventually pass a document's size to. So the
// observation's *class* on the log line is its **latency bucket** — `<=5ms`,
// `>1000ms` — which is a discriminator, exactly what `Detail` is documented to be,
// and content-free. That makes the question "is persistence slow" answerable from a
// log query with no histogram storage, while the histogram keeps the shape for
// whoever looks at it properly.
//
// # No new event name
//
// `state.write_ms` is in §13.2 and in `AllEventNames()` already, so this file adds a
// counter under the existing spelling and moves nothing. A seventh `state.*` name
// would break the count another file's test asserts, and would split one operator's
// query into two.

package observability

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// writeBucketBoundsMS is the upper bound, in milliseconds, of each latency bucket.
//
// Fixed rather than configurable because there is no second question about it: a
// bucket an operator can predict is a bucket they can alert on, and a
// configurable boundary set is a config file that has to be read before a log line
// means anything. The values are the ones a debounced write actually takes — SQLite
// with `synchronous=NORMAL` commits a single small row in well under a millisecond,
// so `<=1ms` is the "nothing is wrong" bucket, and the ladder widens from there
// toward the multi-second end where a checkpoint or a contended writer shows up.
var writeBucketBoundsMS = []int64{1, 5, 10, 25, 50, 100, 250, 500, 1000}

// Histogram is a non-cumulative latency distribution over `writeBucketBoundsMS`.
//
// Non-cumulative: every observation lands in exactly one bucket, so the counts sum
// to the number of observations. Cumulative buckets are the more common choice for
// percentile estimation, and they are the wrong one here — a reader summing a
// cumulative set has to know that, a reader summing a non-cumulative one does not,
// and the failure of getting it backwards is a number that is silently too large.
//
// Safe for concurrent use. A write is recorded from whichever goroutine flushed,
// which is the state's single scheduler or a `Close` racing it, and the two are
// separate goroutines.
type Histogram struct {
	mu sync.Mutex
	// observations is the total count, and is the sum of buckets plus overflow.
	observations int64
	// totalMS is the sum, kept separately rather than recovered from buckets
	// because a bucket cannot produce a mean.
	totalMS float64
	// minMS and maxMS start at zero and are distinguished by observations == 0,
	// which is why `HistogramValue` reports `Observations` before them: zero is a
	// legal minimum, and a histogram that reported `min: 0` before anything had been
	// observed would be a claim rather than an absence.
	minMS float64
	maxMS float64
	// buckets is one counter per entry of writeBucketBoundsMS.
	buckets []int64
	// overflow counts observations above the last bound.
	overflow int64
}

// NewHistogram returns an empty histogram.
func NewHistogram() *Histogram {
	return &Histogram{buckets: make([]int64, len(writeBucketBoundsMS))}
}

// Observe records one duration.
func (h *Histogram) Observe(elapsed time.Duration) {
	milliseconds := float64(elapsed) / float64(time.Millisecond)

	h.mu.Lock()
	defer h.mu.Unlock()

	h.observations++

	if h.observations == 1 || milliseconds < h.minMS {
		h.minMS = milliseconds
	}

	if milliseconds > h.maxMS {
		h.maxMS = milliseconds
	}

	h.totalMS += milliseconds

	for at, bound := range writeBucketBoundsMS {
		if milliseconds <= float64(bound) {
			h.buckets[at]++

			return
		}
	}

	h.overflow++
}

// Snapshot returns the distribution as of now.
//
// A value rather than pointers into the histogram, and a copy of the bucket slice,
// because the caller renders it — into `/readyz`, into a test's failure message, or
// into a support ticket — and a value that changed under it would make a report
// that disagrees with itself.
func (h *Histogram) Snapshot() HistogramValue {
	h.mu.Lock()
	defer h.mu.Unlock()

	value := HistogramValue{
		Observations: h.observations,
		Overflow:     h.overflow,
	}

	if h.observations == 0 {
		return value
	}

	value.MinMS = h.minMS
	value.MaxMS = h.maxMS
	value.MeanMS = h.totalMS / float64(h.observations)
	value.Buckets = make([]BucketValue, len(h.buckets))

	for at, bound := range writeBucketBoundsMS {
		value.Buckets[at] = BucketValue{UpperMS: bound, Count: h.buckets[at]}
	}

	return value
}

// BucketValue is one bucket's upper bound and its count.
//
// `UpperMS` rather than a label, so the value is data and the label is derived
// where it is rendered. A stored label is a stored opinion about formatting.
type BucketValue struct {
	// UpperMS is the inclusive upper bound in milliseconds.
	UpperMS int64 `json:"upperMs"`
	// Count is how many observations fell at or below UpperMS.
	Count int64 `json:"count"`
}

// HistogramValue is one histogram's state at a point in time.
type HistogramValue struct {
	// Observations is how many samples this is, and is the number every other field
	// here is a statement about. Zero means the fields below are absent rather than
	// zero.
	Observations int64 `json:"observations"`
	// Buckets is one entry per bound, in `writeBucketBoundsMS` order, and the
	// counts sum with `Overflow` to `Observations`.
	Buckets []BucketValue `json:"buckets"`
	// Overflow counts observations above the last bound.
	Overflow int64 `json:"overflow"`
	// MinMS, MaxMS and MeanMS are milliseconds, and are only meaningful when
	// Observations is above zero.
	MinMS  float64 `json:"minMs"`
	MaxMS  float64 `json:"maxMs"`
	MeanMS float64 `json:"meanMs"`
}

// Label returns the bucket's name, for a log line's `Detail`.
//
// The spelling is the reason a histogram is worth logging at all: an alert on
// `detail=>1000ms` needs the value written somewhere, and there is no field on
// `EventAttributes` it could go in. A `<=` is used rather than a range because the
// bucket is inclusive at the top and half-open at the bottom, and `0 < x <= 5ms` on
// every line of a log is noise.
func (b BucketValue) Label() string {
	return "<=" + strconv.FormatInt(b.UpperMS, 10) + "ms"
}

// OverflowLabel is the `Detail` for an observation above the last bound.
//
// A constant rather than a method, because there is one spelling and a
// `BucketValue` has no bound to derive it from.
const OverflowLabel = ">" + "1000ms"

// Writes is the realtime plane's `state.write_ms` surface: one counter, one
// histogram, one gauge.
//
// One per process, built by the composition root and handed to the realtime
// registry as its `WriteRecorder`. `Record` is the seam, and its signature is
// deliberately identical to `realtime.WriteRecorder` so the wiring is an assignment
// and this package never imports the subsystem it measures — an import would be
// needed for nothing and would make the dependency run the wrong way.
//
// A nil registry and a nil logger are both tolerated, for the reason `NewWatch`
// gives: a missing log line is recoverable and a panic during a flush is not, since
// the flush is the code path a table is relying on to keep its game.
type Writes struct {
	logger *slog.Logger

	// counter is `state.write_ms` on `/readyz`. Its Total is observations and its
	// Current is campaigns currently failing to persist; see the header.
	counter *Counter

	// latency holds the distribution. Not registered, because `Registry` renders
	// counters and a histogram is not one — a registerable histogram would change
	// the `/readyz` body shape for every consumer of it, including the ones that
	// exist today.
	latency *Histogram

	// mu guards failing. Not held while logging, for the reason `Watch.mu` is not:
	// a handler that blocks must not stop another campaign's write from reporting
	// that it failed.
	mu sync.Mutex
	// failing is the set of campaigns whose most recent write failed, so the gauge
	// counts campaigns in the state rather than writes that ever entered it. A
	// writer that fails on every attempt for ten minutes would otherwise report a
	// count of six hundred, and an operator reading six hundred campaigns in
	// failure would be reading a fiction.
	failing map[string]struct{}
}

// NewWrites registers the `state.write_ms` counter and returns the surface.
func NewWrites(registry *Registry, logger *slog.Logger) *Writes {
	writes := &Writes{
		logger:  logger,
		counter: NewCounter(string(EventStateWriteMs)),
		latency: NewHistogram(),
		failing: make(map[string]struct{}),
	}

	if registry != nil {
		registry.Register(writes.counter)
	}

	return writes
}

// Record receives the outcome of one persisted write.
//
// The signature is `realtime.WriteRecorder` without importing it, and the
// measurement is on the **injected** clock the state was configured with: under a
// test clock `elapsed` is zero by construction, which is honest — the value is a
// measurement and there was nothing to measure. That is also why a test asserting a
// *bucket* rather than a duration is the assertion that works under both clocks.
//
// `campaignID` is rendered as a decimal string because `EventAttributes.CampaignID`
// is a string and every other campaign in this package is a slug; a numeric id
// rendering as `"7"` is unambiguous in a line that also names the event.
func (w *Writes) Record(ctx context.Context, campaignID int64, elapsed time.Duration, err error) {
	w.counter.Inc()

	// Every write is observed, successful or not. A histogram that dropped its
	// failures would describe a process that is faster than it is, which is the one
	// direction of error a latency distribution must not err in.
	w.latency.Observe(elapsed)

	campaign := strconv.FormatInt(campaignID, 10)
	detail := writeBucketLabel(elapsed)

	level := slog.LevelInfo

	if err != nil {
		detail = ErrorClass(err)
		level = slog.LevelWarn

		w.enterFailure(campaign)
	} else {
		w.leaveFailure(campaign)
	}

	Event(ctx, w.logger, EventStateWriteMs, level, EventAttributes{
		CampaignID: campaign,
		Detail:     detail,
	})
}

// Snapshot returns the write surface's state: the distribution and how many
// campaigns are currently failing to persist.
//
// The two halves are one value because the question a caller has is one question —
// "is persistence healthy" — and a caller that took the histogram and then read the
// gauge could be handed a pair from two different instants.
func (w *Writes) Snapshot() WriteSnapshot {
	latency := w.latency.Snapshot()

	return WriteSnapshot{
		Latency:         latency,
		Failures:        w.counter.Snapshot().Current,
		Observations:    w.counter.Snapshot().Total,
		LastBucket:      latency.lastBucket(),
		HasObservations: latency.Observations > 0,
	}
}

// WriteSnapshot is `state.write_ms` at a point in time.
type WriteSnapshot struct {
	// Latency is the distribution.
	Latency HistogramValue
	// Observations is how many writes have been recorded, and is the counter's
	// total rather than a recount.
	Observations int64
	// Failures is how many campaigns' most recent write failed, and is the
	// counter's gauge.
	Failures int64
	// LastBucket is the label of the bucket the most recent observation fell into,
	// which is what a `Detail` on the last line said.
	LastBucket string
	// HasObservations is whether anything has been observed, so a reader can tell
	// "no writes yet" from "writes are all in the first bucket".
	HasObservations bool
}

// writeBucketLabel returns the label of the bucket a duration falls in.
//
// Computed from `writeBucketBoundsMS` and not from a `HistogramValue`, and that is
// load bearing rather than a matter of taste: a `HistogramValue` carries no bounds
// until something has been observed, so deriving the label from one reports every
// first write as `>1000ms`. The bounds are a constant; the value is a measurement of
// them, and the answer to "which bucket is this" is a question about the constant.
func writeBucketLabel(elapsed time.Duration) string {
	milliseconds := float64(elapsed) / float64(time.Millisecond)

	for _, bound := range writeBucketBoundsMS {
		if milliseconds <= float64(bound) {
			return (&BucketValue{UpperMS: bound}).Label()
		}
	}

	return OverflowLabel
}

// lastBucket returns the label of the highest non-empty bucket, or the empty string
// when nothing has been observed.
//
// "Highest non-empty" and not "most recent": `WriteSnapshot` renders a summary and
// the summary of a distribution is its furthest point, not its last sample. A
// renderer wanting the last sample reads the log line, which carries the same label.
func (h HistogramValue) lastBucket() string {
	last := ""

	for _, bucket := range h.Buckets {
		if bucket.Count > 0 {
			last = bucket.Label()
		}
	}

	return last
}

// enterFailure marks a campaign as failing to persist, once.
func (w *Writes) enterFailure(campaign string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, already := w.failing[campaign]; already {
		return
	}

	w.failing[campaign] = struct{}{}
	w.counter.SetGauge(int64(len(w.failing)))
}

// leaveFailure clears a campaign that has written successfully.
func (w *Writes) leaveFailure(campaign string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, failing := w.failing[campaign]; !failing {
		return
	}

	delete(w.failing, campaign)
	w.counter.SetGauge(int64(len(w.failing)))
}
