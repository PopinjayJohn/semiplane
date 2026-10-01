package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/semiplane/semiplane/internal/observability"
)

// logRecord is one parsed JSON line. The tests assert on the parsed record
// rather than on the rendered bytes because the `event` key is what a log query
// matches — the message is prose and gets reworded (S-12.1).
type logRecord map[string]any

// recorder captures `slog` records through a real JSON handler over a buffer,
// which is the closest a test gets to what an aggregator sees.
type recorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// logger returns a JSON logger writing into the recorder's buffer.
func (r *recorder) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&r.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// records parses every line written so far.
func (r *recorder) records(t *testing.T) []logRecord {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	var out []logRecord

	for line := range strings.SplitSeq(strings.TrimSpace(r.buf.String()), "\n") {
		if line == "" {
			continue
		}

		var record logRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("emitted line is not JSON: %v\nline: %s", err, line)
		}

		out = append(out, record)
	}

	return out
}

// newWatch returns a `Watch` over a recorder and the recorder itself.
func newWatch(t *testing.T) (*observability.Watch, *recorder) {
	t.Helper()

	rec := &recorder{}

	return observability.NewWatch(observability.NewRegistry(), rec.logger()), rec
}

// only returns the single record the call under test emitted, failing on zero
// or on more than one. A signal that emits twice under one call has a
// deduplication bug, and a test that only checked `records()[0]` would not see
// it.
func only(t *testing.T, rec *recorder) logRecord {
	t.Helper()

	records := rec.records(t)
	if len(records) != 1 {
		t.Fatalf("got %d records, want exactly 1", len(records))
	}

	return records[0]
}

// level returns a record's level as the string slog wrote it as.
func (r logRecord) level() string {
	value, _ := r["level"].(string)

	return value
}

// TestWatchAddFailedEmitsTheExactSpelling is the S-12.1 assertion applied to
// each method: the `event` key is the dotted name, and a name that differs by
// one character from the one in the architecture record is a signal no
// dashboard is watching.
func TestWatchAddFailedEmitsTheExactSpelling(t *testing.T) {
	t.Parallel()

	watch, rec := newWatch(t)

	watch.AddFailed(t.Context(), "greyhaven", "towns/greyhaven.md", fs.ErrNotExist)

	record := only(t, rec)

	if got, want := record["event"], string(observability.EventWatchAddFailed); got != want {
		t.Errorf("event = %v, want %q", got, want)
	}

	if got := record["campaign_id"]; got != "greyhaven" {
		t.Errorf("campaign_id = %v, want %q", got, "greyhaven")
	}

	if got := record["path"]; got != "towns/greyhaven.md" {
		t.Errorf("path = %v, want %q", got, "towns/greyhaven.md")
	}
}

// TestWatchEmitsTheExactSpellingForEveryMethod walks all six. Each is its own
// case because a copy-paste slip in one method's name — `Recovered` emitting
// `watch.degraded`, say — leaves the other five tests green and the incident
// invisible.
func TestWatchEmitsTheExactSpellingForEveryMethod(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		event observability.EventName
		emit  func(ctx context.Context, watch *observability.Watch)
		want  map[string]string
	}{
		{
			name:  "add failed",
			event: observability.EventWatchAddFailed,
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.AddFailed(ctx, "greyhaven", "towns/greyhaven.md", fs.ErrNotExist)
			},
			want: map[string]string{
				"campaign_id": "greyhaven",
				"path":        "towns/greyhaven.md",
				"detail":      "ENOENT",
			},
		},
		{
			name:  "degraded",
			event: observability.EventWatchDegraded,
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.Degraded(ctx, "greyhaven", "content_root_missing")
			},
			want: map[string]string{"campaign_id": "greyhaven", "detail": "content_root_missing"},
		},
		{
			name:  "recovered",
			event: observability.EventWatchRecovered,
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.Recovered(ctx, "greyhaven", "root_reappeared")
			},
			want: map[string]string{"campaign_id": "greyhaven", "detail": "root_reappeared"},
		},
		{
			name:  "rescan fallback",
			event: observability.EventWatchRescanFallback,
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.RescanFallback(ctx, "greyhaven", "watch_limit")
			},
			want: map[string]string{"campaign_id": "greyhaven", "detail": "watch_limit"},
		},
		{
			name:  "stable read timeout",
			event: observability.EventContentStableReadTimeout,
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.StableReadTimeout(ctx, "greyhaven", "towns/greyhaven.md")
			},
			want: map[string]string{"campaign_id": "greyhaven", "path": "towns/greyhaven.md"},
		},
		{
			name:  "render error",
			event: observability.EventContentRenderError,
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.RenderError(ctx, "greyhaven", "towns/greyhaven.md", errors.New("boom"))
			},
			want: map[string]string{
				"campaign_id": "greyhaven",
				"path":        "towns/greyhaven.md",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			watch, rec := newWatch(t)
			tc.emit(t.Context(), watch)

			record := only(t, rec)

			if got, want := record["event"], string(tc.event); got != want {
				t.Errorf("event = %v, want %q", got, want)
			}

			for key, want := range tc.want {
				if got := record[key]; got != want {
					t.Errorf("%s = %v, want %q", key, got, want)
				}
			}
		})
	}
}

// TestWatchLevelsAreNeverBelowTheDocumentedFloor is S-12.2 as an assertion.
//
// The requirement names `watch.add_failed` and `content.render_error` as never
// below error, and it exists precisely because "letting each call site choose"
// would make the rule advisory. So this test is the rule: the methods that carry
// the rule cannot express a lower level, and a future edit that demotes one
// fails here rather than in production.
func TestWatchLevelsAreNeverBelowTheDocumentedFloor(t *testing.T) {
	t.Parallel()

	// Ordered most to least severe. slog levels are numerically increasing, so
	// the comparison reads directly.
	debug, info := slog.LevelDebug, slog.LevelInfo
	warn, errLevel := slog.LevelWarn, slog.LevelError

	testCases := []struct {
		name string
		emit func(ctx context.Context, watch *observability.Watch)
		want slog.Level
	}{
		{
			name: "add_failed is an error by rule",
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.AddFailed(ctx, "greyhaven", "towns/greyhaven.md", syscall.EMFILE)
			},
			want: errLevel,
		},
		{
			name: "render_error is an error by rule",
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.RenderError(ctx, "greyhaven", "towns/greyhaven.md", errors.New("boom"))
			},
			want: errLevel,
		},
		{
			name: "degraded is at least a warning",
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.Degraded(ctx, "greyhaven", "content_root_missing")
			},
			want: warn,
		},
		{
			name: "rescan_fallback is at least a warning",
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.RescanFallback(ctx, "greyhaven", "watch_limit")
			},
			want: warn,
		},
		{
			name: "stable_read_timeout is at least a warning",
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.StableReadTimeout(ctx, "greyhaven", "towns/greyhaven.md")
			},
			want: warn,
		},
		{
			name: "recovered is at least informational",
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.Recovered(ctx, "greyhaven", "root_reappeared")
			},
			want: info,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			watch, rec := newWatch(t)
			tc.emit(t.Context(), watch)

			record := only(t, rec)

			// Round-trip through the JSON handler's own spelling rather than
			// reading a slog.Level off the record: what matters is the level an
			// aggregator sees, and a handler is free to rename or rescale one.
			var got slog.Level
			if err := got.UnmarshalText([]byte(record.level())); err != nil {
				t.Fatalf("level %q is not a slog level: %v", record.level(), err)
			}

			if got < tc.want {
				t.Errorf("level = %s, want at least %s (debug=%s info=%s warn=%s)",
					got, tc.want, debug, info, warn)
			}
		})
	}
}

// contentMarker is a distinctive string standing in for a page body. It is
// long enough that an accidental substring match elsewhere is implausible and
// short enough to read in a failure message.
const contentMarker = "GM-ONLY-TREASURE-CODEX-4711"

// secretPage is the kind of page whose body must never reach a log line: a GM's
// note, which is what S-12.3 exists for.
var secretPage = strings.Join([]string{
	"# The Duke's Ledger",
	"",
	"[!secret]",
	contentMarker,
	"[/secret]",
}, "\n")

// TestWatchNeverCarriesFileContents is S-12.3 as an assertion.
//
// The error channel is where page contents actually reach a log line, so it is
// what this attacks. Every method that takes an error is handed errors built to
// embed the marker: raw text, a `%w` wrap, a `*fs.PathError` whose `Path` field
// is the page, and a Markdown parser's error quoting the source line it choked
// on. That last one is the case ADR 0003 names, and it is the reason
// `errorClass` exists rather than `err.Error()`.
//
// The other four methods take no error, so there is nothing here to hold them
// to beyond `Detail` and `Path` — both of which are caller-supplied strings whose
// contract is that the caller puts a discriminator and a path in them. A test
// asserting that `Degraded(ctx, id, pageBody)` emits no body would be asserting
// a guarantee this package does not make: refusing to log a string the caller
// handed over means either silently rewriting it or dropping the reason for the
// event, and both are worse than the documented contract. The guarantee that a
// *future* method cannot widen is `TestWatchMethodsCannotAcceptContent`.
func TestWatchNeverCarriesFileContents(t *testing.T) {
	t.Parallel()

	// A markdown parser quotes the offending source line, which is why
	// err.Error() must never be the detail.
	quotedLine := fmt.Errorf("render: %q: unexpected callout", secretPage)

	testCases := []struct {
		name string
		emit func(ctx context.Context, watch *observability.Watch)
	}{
		{
			name: "add failed with a page body in the error",
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.AddFailed(ctx, "greyhaven", "towns/duke.md", quotedLine)
			},
		},
		{
			name: "add failed with the error wrapped by the caller",
			emit: func(ctx context.Context, watch *observability.Watch) {
				err := fmt.Errorf("add watch: %w", quotedLine)
				watch.AddFailed(ctx, "greyhaven", "towns/duke.md", err)
			},
		},
		{
			name: "add failed with a path error whose path is the page",
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.AddFailed(ctx, "greyhaven", "towns/duke.md", &fs.PathError{
					Op:   "open",
					Path: "/vault/towns/" + secretPage,
					Err:  syscall.ENOENT,
				})
			},
		},
		{
			name: "add failed with a real open error on a marker-named file",
			emit: func(ctx context.Context, watch *observability.Watch) {
				_, err := os.Open("/vault/towns/" + contentMarker + ".md")
				watch.AddFailed(ctx, "greyhaven", "towns/duke.md", err)
			},
		},
		{
			name: "render error with a page body in the error",
			emit: func(ctx context.Context, watch *observability.Watch) {
				watch.RenderError(ctx, "greyhaven", "towns/duke.md", quotedLine)
			},
		},
		{
			name: "render error with a front matter error quoting the line",
			emit: func(ctx context.Context, watch *observability.Watch) {
				err := fmt.Errorf("yaml: line 3: %s", secretPage)
				watch.RenderError(ctx, "greyhaven", "towns/duke.md", err)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			watch, rec := newWatch(t)
			tc.emit(t.Context(), watch)

			assertNoMarker(t, rec.records(t))
		})
	}
}

// assertNoMarker fails on any attribute in any record whose text contains the
// page marker, and also on the record's own message — the `event` value is
// checked as an attribute, and `slog` renders it as the message too.
func assertNoMarker(t *testing.T, records []logRecord) {
	t.Helper()

	for _, record := range records {
		for key, value := range record {
			text, ok := value.(string)
			if !ok {
				continue
			}

			if strings.Contains(text, contentMarker) {
				t.Errorf(
					"record %q carries the page body: %s=%q",
					record["event"],
					key,
					text,
				)
			}
		}
	}
}

// TestWatchMethodsCannotAcceptContent is the structural assertion behind S-12.3,
// and the one that holds as the surface grows.
//
// `EventAttributes` has no content field, so the remaining way a page body could
// reach a line is a method that *accepts* one. So this walks the method set of
// `Watch` and asserts every parameter is a context, a string or an error: a
// `[]byte`, a `map[string]any` context bag, or a `slog.Attr` variadic is what
// this is looking for, and adding one is what should break this test.
func TestWatchMethodsCannotAcceptContent(t *testing.T) {
	t.Parallel()

	// The pointer type, not the value type: every method has a pointer receiver,
	// and a value type's method set is empty, which would make this test pass
	// vacuously.
	typ := reflect.TypeFor[*observability.Watch]()

	ctxType := reflect.TypeFor[context.Context]()
	errType := reflect.TypeFor[error]()

	wantMethods := map[string]struct{}{
		"AddFailed":         {},
		"Degraded":          {},
		"Recovered":         {},
		"RescanFallback":    {},
		"StableReadTimeout": {},
		"SettleFailed":      {},
		"RenderError":       {},
	}

	for method := range typ.Methods() {
		if _, expected := wantMethods[method.Name]; !expected {
			t.Errorf("Watch has an unexpected method %q; add it to this test "+
				"and to the spelling and level tables", method.Name)
		}

		delete(wantMethods, method.Name)

		fn := method.Func.Type()

		if got := fn.NumOut(); got != 0 {
			t.Errorf("Watch.%s returns %d values; a signal returns nothing", method.Name, got)
		}

		// Index 0 is the receiver, so index 1 is the first real parameter, and
		// it is the context: S-12.1's events are scoped to a watcher or a
		// request, and a signal with no context cannot be cancelled with its
		// caller.
		if got := fn.NumIn(); got < 3 {
			t.Errorf("Watch.%s takes %d parameters, want at least 2 after the receiver",
				method.Name, got)
		}

		if got := fn.In(1); got != ctxType {
			t.Errorf("Watch.%s parameter 1 is %s, want context.Context", method.Name, got)
		}

		for arg := 2; arg < fn.NumIn(); arg++ {
			param := fn.In(arg)

			if param == errType || param.Kind() == reflect.String {
				continue
			}

			t.Errorf("Watch.%s parameter %d is %s; a parameter that is not a "+
				"context, a string or an error is a way to carry a page body",
				method.Name, arg, param)
		}
	}

	for name := range wantMethods {
		t.Errorf("Watch is missing %q", name)
	}
}

// TestErrorClassIsNeverTheErrorText is the S-12.3 guarantee at the point it is
// made. The classification is what stands between a parser's quoted source line
// and a log aggregator, so it is asserted directly rather than only through the
// methods that use it.
func TestErrorClassIsNeverTheErrorText(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "missing", err: fs.ErrNotExist, want: "ENOENT"},
		{name: "permission", err: fs.ErrPermission, want: "EACCES"},
		{name: "exists", err: fs.ErrExist, want: "EEXIST"},
		{name: "invalid", err: fs.ErrInvalid, want: "EINVAL"},
		{name: "closed", err: fs.ErrClosed, want: "EBADF"},
		{name: "unsupported", err: errors.ErrUnsupported, want: "ENOTSUP"},
		{name: "deadline", err: context.DeadlineExceeded, want: "deadline_exceeded"},
		{name: "canceled", err: context.Canceled, want: "canceled"},
		{name: "watch limit", err: syscall.EMFILE, want: "watch_limit"},
		{name: "symlink loop", err: syscall.ELOOP, want: "ELOOP"},
		{name: "unknown errno is a number", err: syscall.EBADMSG, want: "errno_74"},
		{
			name: "unknown error is its type",
			err:  errors.New("anything"),
			want: "*errors.errorString",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			watch, rec := newWatch(t)
			watch.RenderError(t.Context(), "greyhaven", "towns/duke.md", tc.err)

			record := only(t, rec)

			if tc.want == "" {
				if _, present := record["detail"]; present {
					t.Errorf("detail = %v, want it absent for a nil error", record["detail"])
				}

				return
			}

			if got := record["detail"]; got != tc.want {
				t.Errorf("detail = %v, want %q", got, tc.want)
			}
		})
	}
}

// TestWatchStripsPathErrorNoise covers the specific noise the task calls out: an
// `*fs.PathError`'s text repeats the operation, the syscall and the path, and
// the path is already carried in its own attribute. An operator reading
// `detail` wants "ENOENT", not "open /vault/x.md: no such file or directory".
func TestWatchStripsPathErrorNoise(t *testing.T) {
	t.Parallel()

	pathErr := &fs.PathError{Op: "open", Path: "/vault/towns/duke.md", Err: syscall.ENOENT}

	watch, rec := newWatch(t)
	watch.AddFailed(t.Context(), "greyhaven", "towns/duke.md", pathErr)

	record := only(t, rec)

	if got := record["detail"]; got != "ENOENT" {
		t.Errorf("detail = %v, want %q", got, "ENOENT")
	}
}

// TestWatchRegistersEveryCounter is the S-12.1 assertion for `/readyz`: every
// name is present, which is what makes an unwired signal a visible zero rather
// than an absent key.
func TestWatchRegistersEveryCounter(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry()
	observability.NewWatch(registry, slog.Default())

	// Five are §13.2's; `content.settle_failed` is the sixth, added by the settle
	// filter's phase under ADR 0032. Held here rather than derived from
	// `AllEventNames` so that dropping one from this surface fails the test
	// instead of silently shrinking it.
	want := []observability.EventName{
		observability.EventWatchAddFailed,
		observability.EventWatchDegraded,
		observability.EventWatchRecovered,
		observability.EventWatchRescanFallback,
		observability.EventContentStableReadTimeout,
		observability.EventContentSettleFailed,
		observability.EventContentRenderError,
	}

	snapshot := registry.Snapshot()

	for _, name := range want {
		if _, ok := snapshot[string(name)]; !ok {
			t.Errorf("%s is not in the /readyz snapshot", name)
		}
	}

	if got := len(snapshot); got != len(want) {
		t.Errorf("snapshot has %d counters, want %d", got, len(want))
	}
}

// TestWatchCountersReachTheRegistry checks the counts, not just the presence.
// A counter that is registered but never incremented is a counter that reports
// a healthy system forever.
func TestWatchCountersReachTheRegistry(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry()
	watch := observability.NewWatch(registry, slog.Default())

	ctx := t.Context()

	watch.AddFailed(ctx, "greyhaven", "towns/duke.md", syscall.EMFILE)
	watch.RenderError(ctx, "greyhaven", "towns/duke.md", errors.New("boom"))
	watch.StableReadTimeout(ctx, "greyhaven", "towns/duke.md")
	watch.Degraded(ctx, "greyhaven", "content_root_missing")
	watch.RescanFallback(ctx, "greyhaven", "watch_limit")
	watch.Recovered(ctx, "greyhaven", "root_reappeared")

	want := map[observability.EventName]int64{
		observability.EventWatchAddFailed:           1,
		observability.EventWatchDegraded:            1,
		observability.EventWatchRecovered:           1,
		observability.EventWatchRescanFallback:      1,
		observability.EventContentStableReadTimeout: 1,
		observability.EventContentRenderError:       1,
	}

	snapshot := registry.Snapshot()

	for name, wantTotal := range want {
		if got := snapshot[string(name)].Total; got != wantTotal {
			t.Errorf("%s Total = %d, want %d", name, got, wantTotal)
		}
	}
}

// TestStateCountersAreGauges is the reason `Counter` carries two values, asserted
// through `Watch`.
//
// A counter that only ever rises reports a steadily growing number and never
// tells an operator that a watcher is dead right now: a process that has been
// degraded since startup reports exactly the same `current` as one that
// degraded a second ago.
func TestStateCountersAreGauges(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry()
	watch := observability.NewWatch(registry, slog.Default())

	ctx := t.Context()

	watch.Degraded(ctx, "greyhaven", "content_root_missing")
	watch.RescanFallback(ctx, "greyhaven", "watch_limit")

	snapshot := registry.Snapshot()

	if got := snapshot[string(observability.EventWatchDegraded)].Current; got != 1 {
		t.Errorf("watch.degraded Current = %d, want 1", got)
	}

	if got := snapshot[string(observability.EventWatchRescanFallback)].Current; got != 1 {
		t.Errorf("watch.rescan_fallback Current = %d, want 1", got)
	}

	watch.Recovered(ctx, "greyhaven", "root_reappeared")

	snapshot = registry.Snapshot()

	if got := snapshot[string(observability.EventWatchDegraded)].Current; got != 0 {
		t.Errorf("watch.degraded Current = %d, want 0 after recovery", got)
	}

	if got := snapshot[string(observability.EventWatchRescanFallback)].Current; got != 0 {
		t.Errorf("watch.rescan_fallback Current = %d, want 0 after recovery; "+
			"a recovery must clear every state it latched", got)
	}

	// The transition count survives recovery: an operator asking "how often does
	// this happen" still needs an answer after it stops happening.
	if got := snapshot[string(observability.EventWatchDegraded)].Total; got != 1 {
		t.Errorf("watch.degraded Total = %d, want 1; recovery must not reset the total", got)
	}

	if got := snapshot[string(observability.EventWatchRecovered)].Total; got != 1 {
		t.Errorf("watch.recovered Total = %d, want 1", got)
	}
}

// TestStateGaugesCountCampaignsNotTransitions is the failure the gauge
// bookkeeping exists to prevent: a watcher that re-reports on every failed
// sweep must not inflate the gauge into a count of sweeps.
func TestStateGaugesCountCampaignsNotTransitions(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry()
	watch := observability.NewWatch(registry, slog.Default())

	ctx := t.Context()

	const sweeps = 5
	for range sweeps {
		watch.Degraded(ctx, "greyhaven", "content_root_missing")
		watch.RescanFallback(ctx, "greyhaven", "watch_limit")
	}

	snapshot := registry.Snapshot()

	if got := snapshot[string(observability.EventWatchDegraded)].Current; got != 1 {
		t.Errorf("watch.degraded Current = %d after %d sweeps, want 1", got, sweeps)
	}

	if got := snapshot[string(observability.EventWatchRescanFallback)].Current; got != 1 {
		t.Errorf("watch.rescan_fallback Current = %d after %d sweeps, want 1", got, sweeps)
	}

	// The transition count still rises: the sweeps did happen, and an operator
	// asking about a rate is asking about sweeps.
	if got := snapshot[string(observability.EventWatchDegraded)].Total; got != sweeps {
		t.Errorf("watch.degraded Total = %d, want %d", got, sweeps)
	}
}

// TestStateGaugesArePerCampaign: one process watches many campaigns, so the
// gauge has to count campaigns rather than being a single flag. A per-process
// flag would report one healthy campaign as a healthy system.
func TestStateGaugesArePerCampaign(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry()
	watch := observability.NewWatch(registry, slog.Default())

	ctx := t.Context()

	watch.Degraded(ctx, "greyhaven", "content_root_missing")
	watch.Degraded(ctx, "ravensmoor", "content_root_missing")
	watch.RescanFallback(ctx, "greyhaven", "watch_limit")

	snapshot := registry.Snapshot()

	if got := snapshot[string(observability.EventWatchDegraded)].Current; got != 2 {
		t.Errorf("watch.degraded Current = %d, want 2", got)
	}

	watch.Recovered(ctx, "greyhaven", "root_reappeared")

	snapshot = registry.Snapshot()

	if got := snapshot[string(observability.EventWatchDegraded)].Current; got != 1 {
		t.Errorf("watch.degraded Current = %d, want 1; one campaign recovered and "+
			"the other has not", got)
	}

	if got := snapshot[string(observability.EventWatchRescanFallback)].Current; got != 0 {
		t.Errorf("watch.rescan_fallback Current = %d, want 0", got)
	}
}

// TestWatchToleratesNilLogger is the documented nil-logger choice, asserted for
// every method.
//
// A watcher that panics because logging was not configured is a worse outcome
// than a missing line: the panic takes down the goroutine that was watching the
// vault, which is the silent failure this file exists to make visible.
func TestWatchToleratesNilLogger(t *testing.T) {
	t.Parallel()

	watch := observability.NewWatch(observability.NewRegistry(), nil)

	ctx := t.Context()

	// Each of these is the assertion; a panic in any is the failure.
	watch.AddFailed(ctx, "greyhaven", "towns/duke.md", syscall.EMFILE)
	watch.Degraded(ctx, "greyhaven", "content_root_missing")
	watch.Recovered(ctx, "greyhaven", "root_reappeared")
	watch.RescanFallback(ctx, "greyhaven", "watch_limit")
	watch.StableReadTimeout(ctx, "greyhaven", "towns/duke.md")
	watch.RenderError(ctx, "greyhaven", "towns/duke.md", errors.New("boom"))
}

// TestWatchToleratesNilRegistry covers the other half of the same choice: the
// counters still count, they are simply not on `/readyz`.
func TestWatchToleratesNilRegistry(t *testing.T) {
	t.Parallel()

	watch := observability.NewWatch(nil, slog.Default())

	watch.Degraded(t.Context(), "greyhaven", "content_root_missing")
	watch.Recovered(t.Context(), "greyhaven", "root_reappeared")
}

// TestWatchToleratesEmptyIdentifiers: a path is optional for AddFailed when the
// failure is about the watch limit rather than a file, and an empty campaign id
// must not panic or emit a fabricated `campaign_id=""`.
func TestWatchToleratesEmptyIdentifiers(t *testing.T) {
	t.Parallel()

	watch, rec := newWatch(t)

	ctx := t.Context()

	watch.AddFailed(ctx, "greyhaven", "", nil)
	watch.StableReadTimeout(ctx, "greyhaven", "")

	for _, record := range rec.records(t) {
		if _, present := record["path"]; present {
			t.Errorf("path = %v, want it absent for an empty path", record["path"])
		}

		if _, present := record["detail"]; present {
			t.Errorf("detail = %v, want it absent for a nil error", record["detail"])
		}
	}

	// A separate value, because an empty campaign has to be as harmless as an
	// empty path and is checked in the same place.
	watch.Degraded(ctx, "", "content_root_missing")
	watch.Recovered(ctx, "", "root_reappeared")

	for _, record := range rec.records(t)[2:] {
		if _, present := record["campaign_id"]; present {
			t.Errorf("campaign_id = %v, want it absent for an empty campaign",
				record["campaign_id"])
		}
	}
}

// TestWatchIsSafeForConcurrentUse is what the gate's `-race` is really checking:
// the watcher calls these from its own goroutine, the settle filter from a
// timer, and `/readyz` reads the gauges while all of that is happening.
func TestWatchIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	const (
		callers        = 8
		callsPerCaller = 200
	)

	registry := observability.NewRegistry()
	watch := observability.NewWatch(registry, slog.New(slog.DiscardHandler))

	var wg sync.WaitGroup

	for caller := range callers {
		wg.Go(func() {
			ctx := t.Context()
			campaign := fmt.Sprintf("campaign-%d", caller%2)

			for range callsPerCaller {
				watch.AddFailed(ctx, campaign, "towns/duke.md", syscall.EMFILE)
				watch.Degraded(ctx, campaign, "content_root_missing")
				watch.RescanFallback(ctx, campaign, "watch_limit")
				watch.StableReadTimeout(ctx, campaign, "towns/duke.md")
				watch.RenderError(ctx, campaign, "towns/duke.md", errors.New("boom"))
				watch.Recovered(ctx, campaign, "root_reappeared")
			}
		})
	}

	wg.Go(func() {
		for range callsPerCaller {
			registry.Snapshot()
		}
	})

	wg.Wait()

	snapshot := registry.Snapshot()
	for _, name := range []observability.EventName{
		observability.EventWatchAddFailed,
		observability.EventContentStableReadTimeout,
		observability.EventContentRenderError,
	} {
		if got := snapshot[string(name)].Total; got != callers*callsPerCaller {
			t.Errorf("%s Total = %d, want %d", name, got, callers*callsPerCaller)
		}
	}
}
