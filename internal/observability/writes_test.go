// The tests for the `state.write_ms` surface.
//
// # What is being claimed
//
// Three things, and each is a claim a naive implementation gets wrong in a way the
// others do not catch:
//
//   - The histogram is **non-cumulative**: every observation lands in exactly one
//     bucket, and the counts sum with the overflow to the observation count. A
//     cumulative implementation sums to something else, and every assertion about
//     "how slow is persistence" would then be a number that is too large — quietly,
//     because it is still monotonically increasing.
//   - The gauge counts **campaigns**, not writes. A writer that fails on every
//     attempt for ten minutes must report a handful of campaigns, not six hundred,
//     because a reader believes the number it reads.
//   - The histogram is registered under §13.2's existing spelling, so
//     `AllEventNames()` does not move.
//
// # Why the elapsed durations are wall-clock literals
//
// `realtime.WriteRecorder` hands over a duration measured on the state's injected
// clock, and under a test clock that is zero by construction. A test here therefore
// feeds `Record` the durations directly rather than trying to make a state produce
// them, and asserts on **buckets** rather than on microseconds: a bucket is what the
// log line says and what an alert matches, so it is the more durable claim anyway.

package observability_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/observability"
)

// millisecond is one millisecond, and it is the unit every duration below is written
// in so the cases read as the numbers a reader would recognise rather than as
// `1000000`.
const millisecond = time.Millisecond

// TestTheWriteHistogramIsNonCumulativeAndComplete is the arithmetic every reader of a
// distribution relies on.
func TestTheWriteHistogramIsNonCumulativeAndComplete(t *testing.T) {
	t.Parallel()

	writes, _ := newWrites(t)

	// One observation per bucket's lower edge, plus two above the last bound, so
	// every bucket is populated and the overflow is exercised.
	observed := []time.Duration{
		0,
		millisecond,
		2 * millisecond,
		6 * millisecond,
		11 * millisecond,
		30 * millisecond,
		60 * millisecond,
		300 * millisecond,
		700 * millisecond,
		999 * millisecond,
		1500 * millisecond,
		4000 * millisecond,
	}

	for _, elapsed := range observed {
		writes.Record(t.Context(), 1, elapsed, nil)
	}

	snapshot := writes.Snapshot()

	if !snapshot.HasObservations {
		t.Fatal("Snapshot() reported no observations after twelve records")
	}

	if snapshot.Observations != int64(len(observed)) {
		t.Errorf("Observations = %d, want %d", snapshot.Observations, len(observed))
	}

	// The sum is the property that distinguishes non-cumulative from cumulative, and
	// it is asserted as an equation over the reported value rather than against a
	// restated expectation, so a change to the bucket set cannot silently invalidate
	// the test.
	var total int64

	for _, bucket := range snapshot.Latency.Buckets {
		total += bucket.Count
	}

	total += snapshot.Latency.Overflow

	if total != snapshot.Observations {
		t.Errorf("buckets sum with overflow to %d, want %d: a cumulative histogram would sum "+
			"higher, and every percentile read off it would be too large", total, snapshot.Observations)
	}

	if snapshot.Latency.Overflow != 2 {
		t.Errorf(
			"Overflow = %d, want 2 (the two observations above 1000ms)",
			snapshot.Latency.Overflow,
		)
	}

	// The min, max and mean bracket the sample, and the two ends are the two
	// observations the bucket boundaries exist to separate.
	if snapshot.Latency.MinMS != 0 {
		t.Errorf("MinMS = %v, want 0", snapshot.Latency.MinMS)
	}

	if snapshot.Latency.MaxMS != 4000 {
		t.Errorf("MaxMS = %v, want 4000", snapshot.Latency.MaxMS)
	}

	if snapshot.Latency.MeanMS <= 0 || snapshot.Latency.MeanMS >= 4000 {
		t.Errorf("MeanMS = %v, want it strictly between 0 and the maximum of 4000",
			snapshot.Latency.MeanMS)
	}
}

// TestTheWriteHistogramReportsNothingBeforeAnythingIsObserved holds the fixed point
// the header states: a zero observation count is not a set of zero measurements.
//
// Without it, a fresh process would report `min: 0, max: 0, mean: 0` and a reader
// would conclude persistence is instantaneous rather than that it has not happened.
func TestTheWriteHistogramReportsNothingBeforeAnythingIsObserved(t *testing.T) {
	t.Parallel()

	writes, _ := newWrites(t)

	snapshot := writes.Snapshot()

	if snapshot.HasObservations {
		t.Error("Snapshot() reported observations on a surface nothing has written through")
	}

	if snapshot.Latency.Observations != 0 || snapshot.Latency.Buckets != nil {
		t.Errorf("Latency = %+v, want no buckets and no observations", snapshot.Latency)
	}

	if snapshot.Latency.MinMS != 0 || snapshot.Latency.MaxMS != 0 || snapshot.Latency.MeanMS != 0 {
		t.Errorf(
			"Latency = %+v, want its three statistics absent rather than zero",
			snapshot.Latency,
		)
	}
}

// TestTheFailureGaugeCountsCampaignsAndNotWrites is the second of the three claims,
// and the one a per-write counter gets wrong.
//
// A writer failing every two seconds for an hour is thirty samples and *one* broken
// campaign. A gauge that counted samples would read 30, and the reader's question —
// "how much is broken right now" — would get a number about the past.
func TestTheFailureGaugeCountsCampaignsAndNotWrites(t *testing.T) {
	t.Parallel()

	writes, _ := newWrites(t)

	// Campaign 1 fails repeatedly. Campaign 2 fails once. Campaign 3 writes fine.
	for range 10 {
		writes.Record(t.Context(), 1, 3*millisecond, errors.New("database is locked"))
	}

	writes.Record(t.Context(), 2, 3*millisecond, errors.New("database is locked"))
	writes.Record(t.Context(), 3, 3*millisecond, nil)

	if got := writes.Snapshot().Failures; got != 2 {
		t.Errorf("Failures = %d, want 2: eleven failures across two campaigns is two broken "+
			"campaigns, and a gauge counting samples would read 11", got)
	}

	// Campaign 1 recovers. The gauge falls by exactly one and not by eleven, because
	// the entry is the campaign's, not the sample's.
	writes.Record(t.Context(), 1, 3*millisecond, nil)

	if got := writes.Snapshot().Failures; got != 1 {
		t.Errorf("Failures = %d, want 1 after campaign 1 wrote successfully", got)
	}

	// And a second success for a campaign that never failed does not push it below
	// zero, which is the clamp `gauge` exists for.
	writes.Record(t.Context(), 3, 3*millisecond, nil)

	if got := writes.Snapshot().Failures; got != 1 {
		t.Errorf("Failures = %d, want 1: a succeeding write for a campaign that was never failing "+
			"must not decrement the gauge", got)
	}
}

// TestTheWriteGaugeIsVisibleOnReadyz is S-12.1: an unwired signal reads as a visible
// zero rather than an absent key.
//
// The counter is registered under §13.2's existing spelling, so the `/readyz` body
// gains a value and not a key — a consumer of that body should not have to handle a
// key appearing across versions.
func TestTheWriteGaugeIsVisibleOnReadyz(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry()
	rec := &recorder{}
	writes := observability.NewWrites(registry, rec.logger())

	counter, ok := registry.Counter("state.write_ms")
	if !ok {
		t.Fatal(
			"state.write_ms is not registered, want it present even before anything has been written",
		)
	}

	if got := counter.Snapshot().Current; got != 0 {
		t.Errorf("gauge = %d before any write, want 0", got)
	}

	writes.Record(t.Context(), 4, 2*millisecond, nil)

	counter, _ = registry.Counter("state.write_ms")
	if got := counter.Snapshot().Current; got != 0 {
		t.Errorf("gauge = %d after a successful write, want 0", got)
	}

	if got := counter.Snapshot().Total; got != 1 {
		t.Errorf("total = %d, want 1", got)
	}

	writes.Record(t.Context(), 4, 2*millisecond, errors.New("database is locked"))

	counter, _ = registry.Counter("state.write_ms")
	if got := counter.Snapshot().Current; got != 1 {
		t.Errorf("gauge = %d after a failed write, want 1", got)
	}

	// The name is the §13.2 spelling, and it is in the registry's list — the assertion
	// that the counter was registered under the documented name rather than one that
	// merely happens to match a string.
	if !slices.Contains(registry.SortedNames(), "state.write_ms") {
		t.Errorf("SortedNames() = %v, want it to contain state.write_ms", registry.SortedNames())
	}
}

// TestTheWriteLogLineCarriesTheBucketIs the claim that a latency is answerable from a
// log query.
//
// `EventAttributes` has no duration field and this work item does not add one, so the
// observation's class on the line is its bucket. The assertion is on the `detail`
// attribute because that is what a dashboard groups by.
func TestTheWriteLogLineCarriesTheBucket(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		elapsed time.Duration
		want    string
	}{
		{"instant", 0, "<=1ms"},
		{"a millisecond", millisecond, "<=1ms"},
		{"just over a millisecond", 1500 * time.Microsecond, "<=5ms"},
		{"a slow checkpoint", 900 * millisecond, "<=1000ms"},
		{"a second and a half", 1500 * millisecond, ">1000ms"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			writes, rec := newWrites(t)
			writes.Record(t.Context(), 7, tc.elapsed, nil)

			record := only(t, rec)

			if record["event"] != "state.write_ms" {
				t.Errorf("event = %v, want state.write_ms", record["event"])
			}

			if got := record["detail"]; got != tc.want {
				t.Errorf("detail = %v, want %q: a log query has to be able to answer "+
					"\"is persistence slow\" with no histogram storage", got, tc.want)
			}

			if got := record["campaign_id"]; got != "7" {
				t.Errorf("campaign_id = %v, want %q", got, "7")
			}
		})
	}
}

// TestAFailedWriteLogsTheErrorClassAndNotTheBucket is the two-branch claim, and it is
// where the two properties meet.
//
// A failure's class is more useful than its latency, and only one of them can be in a
// single-string attribute — so the failure wins, and the histogram keeps the latency
// for whoever asks in process. The mutation this kills is the reverse order, which
// reports every failure as `<=1000ms` and makes the error class unrecoverable.
func TestAFailedWriteLogsTheErrorClassAndNotTheBucket(t *testing.T) {
	t.Parallel()

	writes, rec := newWrites(t)
	writes.Record(t.Context(), 7, 900*millisecond, errors.New("database is locked"))

	record := only(t, rec)

	if got := record["detail"]; got != "*errors.errorString" {
		t.Errorf("detail = %v, want the error's class; a failed write in the slow bucket is "+
			"still a failed write, and the class is the part an alert matches", got)
	}

	if got := record["level"]; got != "WARN" {
		t.Errorf("level = %v, want WARN", got)
	}

	// The latency is still recorded: a dropped observation would make the histogram
	// describe a process that is faster than it is.
	if got := writes.Snapshot().Observations; got != 1 {
		t.Errorf("Observations = %d, want 1: a failed write is still an observation", got)
	}
}

// TestWritesAddsNoEventName is the constraint the brief is explicit about, asserted
// rather than assumed.
//
// `state.write_ms` is §13.2's and was already registered, so this work item adds a
// counter under an existing spelling and moves nothing. The count is a number another
// file's test owns, and it is the only thing standing between a phase and an event
// name nobody documented.
func TestWritesAddsNoEventName(t *testing.T) {
	t.Parallel()

	names := observability.AllEventNames()

	if len(names) != 24 {
		t.Errorf("AllEventNames() has %d names, want 24", len(names))
	}

	writes, _ := newWrites(t)
	snapshot := writes.Snapshot()

	if snapshot.Observations != 0 {
		t.Errorf("Observations = %d, want 0", snapshot.Observations)
	}

	// The registered name is the constant, not a literal, so this fails if a later
	// change to the spelling does not change the registration.
	registry := observability.NewRegistry()
	observability.NewWrites(registry, nil)

	if _, ok := registry.Counter(string(observability.EventStateWriteMs)); !ok {
		t.Errorf("state.write_ms is not registered under its EventStateWriteMs spelling")
	}
}

// TestWritesToleratesNoLoggerAndNoRegistry is the same tolerance `NewWatch` has, and
// it is load bearing: the flush is the code path a table is relying on to keep its
// game, so a panic in the measurement would lose the game.
func TestWritesToleratesNoLoggerAndNoRegistry(t *testing.T) {
	t.Parallel()

	writes := observability.NewWrites(nil, nil)

	writes.Record(t.Context(), 1, millisecond, nil)
	writes.Record(t.Context(), 1, millisecond, errors.New("database is locked"))

	if got := writes.Snapshot().Observations; got != 2 {
		t.Errorf("Observations = %d, want 2: recording must happen whether or not anyone is "+
			"measuring it", got)
	}

	if got := writes.Snapshot().Failures; got != 1 {
		t.Errorf("Failures = %d, want 1", got)
	}
}

// newWrites returns a `Writes` over a recorder, and the recorder.
func newWrites(t *testing.T) (*observability.Writes, *recorder) {
	t.Helper()

	rec := &recorder{}

	return observability.NewWrites(observability.NewRegistry(), rec.logger()), rec
}
