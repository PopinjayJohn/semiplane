package observability_test

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/semiplane/semiplane/internal/observability"
)

// TestAllEventNamesAreUniqueAndComplete is the assertion that the §13.2
// spelling is written once. A duplicate constant means one subsystem logs an
// event a dashboard is not watching, under a name that looks right.
func TestAllEventNamesAreUniqueAndComplete(t *testing.T) {
	t.Parallel()

	names := observability.AllEventNames()

	// A defensive count: the architecture record lists eighteen, and phase 4 adds
	// five for the indexer under ADR 0032. If this fails, a signal was added
	// without updating the test, which is the intended alarm — and, if the addition
	// came with no record, this failure is the only thing standing between an
	// unrecorded deviation and a shipped one.
	const wantCount = 24

	if len(names) != wantCount {
		t.Errorf("AllEventNames() has %d entries, want %d", len(names), wantCount)
	}

	seen := make(map[observability.EventName]struct{}, len(names))

	for _, name := range names {
		if previous, dup := seen[name]; dup {
			t.Errorf("duplicate event name %q (also %q)", name, previous)
		}

		seen[name] = struct{}{}

		// The dotted form is what a log query matches on, so a name that is not
		// dotted is almost certainly a typo.
		if !strings.Contains(string(name), ".") {
			t.Errorf("event name %q is not dotted", name)
		}
	}
}

// TestAllEventNamesReturnsACopy guards the exported list against a caller
// mutating the package's own slice.
func TestAllEventNamesReturnsACopy(t *testing.T) {
	t.Parallel()

	names := observability.AllEventNames()
	if len(names) == 0 {
		t.Fatal("AllEventNames() is empty")
	}

	names[0] = "mutated"

	if observability.AllEventNames()[0] == "mutated" {
		t.Error("AllEventNames() returned the package's own slice; a caller can corrupt it")
	}
}

func TestCounterCounts(t *testing.T) {
	t.Parallel()

	counter := observability.NewCounter("test.counter")

	counter.Inc()
	counter.Add(4)

	got := counter.Snapshot()
	if got.Total != 5 {
		t.Errorf("Total = %d, want 5", got.Total)
	}
}

// TestCounterIgnoresNegativeDeltas covers the monotonic promise. A counter that
// accepts a negative delta reports a number that looks like traffic and is not.
func TestCounterIgnoresNegativeDeltas(t *testing.T) {
	t.Parallel()

	counter := observability.NewCounter("test.counter")
	counter.Add(10)
	counter.Add(-4)

	if got := counter.Snapshot(); got.Total != 10 {
		t.Errorf("Total = %d, want 10; a negative delta must be ignored", got.Total)
	}
}

// TestCounterGaugeIsIndependentOfTotal covers the distinction the two fields
// exist for: a hub's live connection count goes down, a request count does not.
func TestCounterGaugeIsIndependentOfTotal(t *testing.T) {
	t.Parallel()

	counter := observability.NewCounter("ws.connected")
	counter.Inc()
	counter.Inc()
	counter.SetGauge(2)
	counter.SetGauge(1)

	got := counter.Snapshot()
	if got.Current != 1 {
		t.Errorf("Current = %d, want 1", got.Current)
	}

	if got.Total != 2 {
		t.Errorf("Total = %d, want 2; a gauge must not change the monotonic total", got.Total)
	}
}

func TestRegistryRegistersAndSnapshots(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry().
		Register(observability.NewCounter("cache.hit")).
		Register(observability.NewCounter("cache.miss"))

	if _, ok := registry.Counter("cache.hit"); !ok {
		t.Error(`registry.Counter("cache.hit") = not found, want found`)
	}

	snapshot := registry.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("Snapshot() has %d entries, want 2", len(snapshot))
	}
}

// TestRegistryRegisterIsIdempotent covers a subsystem registering twice: the
// second registration must not orphan the count the first accumulated.
func TestRegistryRegisterIsIdempotent(t *testing.T) {
	t.Parallel()

	counter := observability.NewCounter("cache.hit")
	counter.Add(7)

	registry := observability.NewRegistry().Register(counter).Register(counter)

	snapshot := registry.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("Snapshot() has %d entries, want 1", len(snapshot))
	}

	if got := snapshot["cache.hit"].Total; got != 7 {
		t.Errorf("cache.hit Total = %d, want 7; re-registering must not reset it", got)
	}
}

// TestRegistrySnapshotIsIndependent covers the returned map being the
// registry's own. A caller that mutates it would corrupt every later read.
func TestRegistrySnapshotIsIndependent(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry().Register(observability.NewCounter("cache.hit"))

	first := registry.Snapshot()
	delete(first, "cache.hit")

	if _, ok := registry.Snapshot()["cache.hit"]; !ok {
		t.Error("mutating a snapshot changed the registry; Snapshot must return a copy")
	}
}

// TestRegistrySortedNamesIsStable is the assertion that makes two snapshots
// comparable. A body whose keys reorder between calls with nothing having
// changed cannot be diffed by a human or compared by a test.
func TestRegistrySortedNamesIsStable(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry().
		Register(observability.NewCounter(string(observability.EventSecretReverted))).
		Register(observability.NewCounter(string(observability.EventCacheHit))).
		Register(observability.NewCounter(string(observability.EventWSConnected)))

	want := []string{"cache.hit", "secret.reverted", "ws.connected"}

	for range 20 {
		got := registry.SortedNames()
		if len(got) != len(want) {
			t.Fatalf("SortedNames() = %v, want %v", got, want)
		}

		for idx := range want {
			if got[idx] != want[idx] {
				t.Fatalf("SortedNames() = %v, want %v", got, want)
			}
		}
	}
}

// TestRegistryIsSafeForConcurrentUse is the assertion the gate's `-race` is
// really checking. `/readyz` is hit on a health-check interval by every
// orchestrator, while subsystems increment from every request handler.
func TestRegistryIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	const (
		counters       = 4
		readers        = 8
		incrementsEach = 500
	)

	registry := observability.NewRegistry()
	for idx := range counters {
		registry.Register(observability.NewCounter("concurrent." + string(rune('a'+idx))))
	}

	var wg sync.WaitGroup

	for reader := range readers {
		wg.Go(func() {
			for range incrementsEach {
				registry.Snapshot()
				registry.SortedNames()
				_ = reader
			}
		})
	}

	for _, name := range registry.SortedNames() {
		counter, ok := registry.Counter(name)
		if !ok {
			t.Fatalf("Counter(%q) = not found after registration", name)
		}

		wg.Go(func() {
			for range incrementsEach {
				counter.Inc()
			}
		})
	}

	wg.Wait()

	for _, name := range registry.SortedNames() {
		counter, _ := registry.Counter(name)
		if got := counter.Snapshot().Total; got != incrementsEach {
			t.Errorf("%s Total = %d, want %d", name, got, incrementsEach)
		}
	}
}

// captureHandler records slog records so a test can assert on level and
// attributes without writing to a real log.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.records = append(h.records, record.Clone())

	return nil
}

// WithAttrs is required by slog.Handler and deliberately ignores the
// attributes: observability.Event passes everything through LogAttrs, so a
// test that wanted them here would be testing a different code path.
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *captureHandler) WithGroup(string) slog.Handler { return h }

// attr returns the value logged under a key across all captured records, and
// whether the key was ever present.
func (h *captureHandler) attr(key string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Indexed rather than ranged: a slog.Record is a large struct, and
	// `for _, record := range` copies one per iteration for no benefit.
	for idx := range h.records {
		var (
			value   string
			present bool
		)

		h.records[idx].Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				value = a.Value.String()
				present = true
			}

			// Always continue: a record may carry the key alongside others, and
			// stopping at the first attribute would miss it.
			return true
		})

		if present {
			return value, true
		}
	}

	return "", false
}

func (h *captureHandler) levels() []slog.Level {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Indexed rather than ranged: a slog.Record is a large struct, and
	// `for _, record := range` copies one per iteration for no benefit.
	levels := make([]slog.Level, 0, len(h.records))
	for idx := range h.records {
		levels = append(levels, h.records[idx].Level)
	}

	return levels
}

// TestEventEmitsStableEventKey is the assertion S-12.1 makes: a stable `event`
// key, so a log query never has to match on prose that gets reworded.
func TestEventEmitsStableEventKey(t *testing.T) {
	t.Parallel()

	capture := &captureHandler{}
	logger := slog.New(capture)

	observability.Event(
		t.Context(),
		logger,
		observability.EventWatchAddFailed,
		slog.LevelError,
		observability.EventAttributes{CampaignID: "greyhaven", Path: "towns/greyhaven.md"},
	)

	if got, ok := capture.attr("event"); !ok {
		t.Error("no `event` attribute was logged")
	} else if got != string(observability.EventWatchAddFailed) {
		t.Errorf("event = %q, want %q", got, observability.EventWatchAddFailed)
	}

	if got, ok := capture.attr("campaign_id"); !ok {
		t.Error("no `campaign_id` attribute was logged")
	} else if got != "greyhaven" {
		t.Errorf("campaign_id = %q, want %q", got, "greyhaven")
	}

	if _, ok := capture.attr("path"); !ok {
		t.Error("no `path` attribute was logged")
	}

	levels := capture.levels()
	if len(levels) != 1 || levels[0] != slog.LevelError {
		t.Errorf("levels = %v, want exactly one error", levels)
	}
}

// TestEventOmitsEmptyAttributes keeps a log line from carrying
// `campaign_id=""`, which reads as a real value in an aggregation.
func TestEventOmitsEmptyAttributes(t *testing.T) {
	t.Parallel()

	capture := &captureHandler{}
	logger := slog.New(capture)

	observability.Event(t.Context(), logger, observability.EventWSStale, slog.LevelWarn,
		observability.EventAttributes{})

	for _, key := range []string{"campaign_id", "path", "detail"} {
		if _, ok := capture.attr(key); ok {
			t.Errorf("`%s` was logged despite being empty", key)
		}
	}
}

// TestEventToleratesNilLogger covers the composition root not having wired a
// logger yet. An observability helper that panics takes down the request it was
// supposed to describe.
func TestEventToleratesNilLogger(t *testing.T) {
	t.Parallel()

	observability.Event(t.Context(), nil, observability.EventCacheHit, slog.LevelInfo,
		observability.EventAttributes{CampaignID: "greyhaven"})
}

// TestEventAttributesCarryNoFreeFormBag is the structural assertion behind
// S-12.3. The type has no field that accepts a `[]byte` or a rendered page, so
// a secret cannot reach a log line even by mistake. If someone later adds an
// `Attrs []slog.Attr` field, this test's companion check — that the struct has
// exactly these fields — is what should fail first.
func TestEventAttributesCarryNoFreeFormBag(t *testing.T) {
	t.Parallel()

	attrs := observability.EventAttributes{
		CampaignID: "greyhaven",
		Path:       "towns/greyhaven.md",
		Detail:     "ENOENT",
	}

	// The point is the *shape* of the struct, not its values: it holds only strings
	// and one bounded integer, so there is no field a secret, a file body or a
	// dice result could be passed through. A `[]byte`, an `any`, or an
	// `Attrs []slog.Attr` bag is what this is checking for, and adding any of
	// them is what should break this test.
	typ := reflect.TypeOf(attrs)

	const wantFields = 7

	if got := typ.NumField(); got != wantFields {
		t.Errorf("EventAttributes has %d fields, want %d", got, wantFields)
	}

	for _, forbidden := range []string{"Content", "Body", "Secret", "Result", "Attrs", "Detail2"} {
		if _, present := typ.FieldByName(forbidden); present {
			t.Errorf("EventAttributes exposes a %q field", forbidden)
		}
	}

	// Every field must be a string or an int. A string is bounded by what a
	// caller puts in it, which is the whole discipline S-12.3 rests on and which
	// the field names and their comments state; an int carries no text at all, so
	// `Count` is safe by type rather than by convention. Anything else — a slice,
	// a map, an interface — is the hole the invariant closes.
	for _, field := range reflect.VisibleFields(typ) {
		switch field.Type.Kind() {
		case reflect.String, reflect.Int:
		default:
			t.Errorf("EventAttributes.%s is %s; a container or interface field is a way "+
				"to carry content a log line must never hold", field.Name, field.Type)
		}
	}
}
