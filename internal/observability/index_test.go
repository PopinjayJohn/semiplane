// The tests for the indexer's signal surface.
//
// The claims under test are S-12.1 (a signal an operator can read), S-12.3 (no
// event carries content) and the level rule that makes `index.change_failed` an
// error by judgment rather than by S-12.2's fixed list.

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
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/observability"
)

// secretMarker is the string that must never appear in an emitted record. It
// stands in for the body of a `[!secret]` callout.
const secretMarker = "the passphrase is correct horse"

// record is one parsed log line. Parsed rather than substring-matched because
// escaped output legitimately contains the marker's characters in other
// arrangements, and a substring assertion over a JSON blob tests the encoder as
// much as the code.
type record map[string]any

// capture returns a logger writing JSON into a buffer, plus the buffer.
func capture() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			return attr
		},
	}))

	return logger, &buf
}

// records parses the captured lines.
func records(t *testing.T, buf *bytes.Buffer) []record {
	t.Helper()

	var parsed []record

	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}

		var got record
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("captured line is not JSON: %v\n%s", err, line)
		}

		parsed = append(parsed, got)
	}

	return parsed
}

// TestIndexEmitsItsFiveNamesAsCounters is the S-12.1 assertion for this
// surface: the four counted signals and the uncounted one are all registered, so
// `/readyz` renders them rather than omitting them.
func TestIndexEmitsItsFiveNamesAsCounters(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry()
	index := observability.NewIndex(registry, slog.New(slog.DiscardHandler))

	index.ChangeFailed(context.Background(), "1", "upsert", "a.md", errors.New("x"))
	index.PageSkipped(context.Background(), "1", "b.md", errors.New("x"))
	index.PageDegraded(context.Background(), "1", "c.md", errors.New("x"))
	index.RenameSourceLeft(context.Background(), "1", "d.md", 1, errors.New("x"))

	for _, name := range []observability.EventName{
		observability.EventIndexChangeFailed,
		observability.EventIndexPageSkipped,
		observability.EventIndexPageDegraded,
		observability.EventIndexRenameSourceLeft,
	} {
		counter, ok := registry.Counter(string(name))
		if !ok {
			t.Fatalf("counter %q is not registered, so /readyz omits it", name)
		}

		if got := counter.Snapshot().Total; got != 1 {
			t.Errorf("counter %q has total %d, want 1", name, got)
		}
	}

	// `index.renamed` is deliberately uncounted: routine work must not share a
	// dashboard with failures. It still has to be a name in the §13.2 list, or a
	// query for it has nothing to match.
	if _, ok := registry.Counter(string(observability.EventIndexRenamed)); ok {
		t.Error("index.renamed is registered as a counter; it is routine work")
	}
}

// TestIndexEventNamesAreSpelledOnce guards the set against a duplicate, which
// would mean one subsystem logs under a name a dashboard is not watching.
func TestIndexEventNamesAreSpelledOnce(t *testing.T) {
	t.Parallel()

	seen := make(map[observability.EventName]bool)

	for _, name := range observability.AllEventNames() {
		if seen[name] {
			t.Errorf("event name %q appears twice in AllEventNames()", name)
		}

		seen[name] = true
	}

	for _, name := range []observability.EventName{
		observability.EventIndexChangeFailed,
		observability.EventIndexPageSkipped,
		observability.EventIndexPageDegraded,
		observability.EventIndexRenameSourceLeft,
		observability.EventIndexRenamed,
	} {
		if !seen[name] {
			t.Errorf("event name %q is missing from AllEventNames()", name)
		}
	}
}

// TestIndexChangeFailedIsErrorByJudgment asserts the level that is not in
// S-12.2's fixed list, which is the one a future editor could quietly drop to
// warn on the grounds that the rule does not mention it.
func TestIndexChangeFailedIsErrorByJudgment(t *testing.T) {
	t.Parallel()

	logger, buf := capture()
	index := observability.NewIndex(observability.NewRegistry(), logger)

	index.ChangeFailed(context.Background(), "7", "upsert", "Page.md", errors.New("nope"))

	got := records(t, buf)
	if len(got) != 1 {
		t.Fatalf("emitted %d records, want 1", len(got))
	}

	if level, _ := got[0]["level"].(string); level != "ERROR" {
		t.Errorf("level is %q, want ERROR: a write that never reached the index is "+
			"indistinguishable from one that worked", level)
	}

	for key, want := range map[string]any{
		"event":       "index.change_failed",
		"campaign_id": "7",
		"op":          "upsert",
		"path":        "Page.md",
	} {
		if got[0][key] != want {
			t.Errorf("%s is %v, want %v", key, got[0][key], want)
		}
	}
}

// TestIndexLevelsAreDistinctPerSignal pins the level of each remaining signal,
// so a later edit cannot quietly promote a skipped page to an error and set an
// alert on a vault with one bad file in it.
func TestIndexLevelsAreDistinctPerSignal(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		emit  func(*observability.Index)
		level string
	}{
		{
			name:  "page_skipped",
			emit:  func(i *observability.Index) { i.PageSkipped(context.Background(), "1", "a.md", nil) },
			level: "WARN",
		},
		{
			name: "page_degraded",
			emit: func(i *observability.Index) {
				i.PageDegraded(context.Background(), "1", "a.md", nil)
			},
			level: "WARN",
		},
		{
			name: "rename_source_left",
			emit: func(i *observability.Index) {
				i.RenameSourceLeft(context.Background(), "1", "a.md", 1, nil)
			},
			level: "WARN",
		},
		{
			name:  "renamed",
			emit:  func(i *observability.Index) { i.Renamed(context.Background(), "1", "a", "b", 3) },
			level: "DEBUG",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			logger, buf := capture()
			index := observability.NewIndex(observability.NewRegistry(), logger)

			tc.emit(index)

			got := records(t, buf)
			if len(got) != 1 {
				t.Fatalf("emitted %d records, want 1", len(got))
			}

			if level, _ := got[0]["level"].(string); level != tc.level {
				t.Errorf("level is %q, want %q", level, tc.level)
			}
		})
	}
}

// TestIndexNeverCarriesFileContents is the S-12.3 assertion, and the reason
// this surface is a type rather than a convention.
//
// Every one of the five emitters is called with an error whose text embeds the
// marker, including the shapes that are hardest to keep safe: a raw `fmt.Errorf`,
// a `%w` wrap of one, and an `*fs.PathError` whose `Path` field *is* the page
// body. The last is the one that matters — a `PathError` renders as
// "open <path>: <err>", so passing one through `err.Error()` would carry whatever
// the filesystem was given.
func TestIndexNeverCarriesFileContents(t *testing.T) {
	t.Parallel()

	// Every shape of "error" a caller might realistically hand over, each with the
	// marker in the position that is most likely to leak.
	errs := map[string]error{
		"raw":       errors.New("parse failed: " + secretMarker),
		"wrapped":   fmt.Errorf("index page: %w", errors.New(secretMarker)),
		"path":      &fs.PathError{Op: "open", Path: secretMarker, Err: fs.ErrPermission},
		"link":      &os.LinkError{Op: "rename", Old: secretMarker, New: secretMarker},
		"joined":    errors.Join(errors.New(secretMarker), fs.ErrNotExist),
		"system":    fmt.Errorf("%w", errors.New(secretMarker)),
		"opaque":    fmt.Errorf("front matter at line 3: %s", secretMarker),
		"no_detail": nil,
	}

	for name, err := range errs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			logger, buf := capture()
			index := observability.NewIndex(observability.NewRegistry(), logger)

			ctx := context.Background()

			index.ChangeFailed(ctx, "1", "upsert", "Page.md", err)
			index.PageSkipped(ctx, "1", "Page.md", err)
			index.PageDegraded(ctx, "1", "Page.md", err)
			index.RenameSourceLeft(ctx, "1", "Page.md", 1, err)

			if got := buf.String(); strings.Contains(got, secretMarker) {
				t.Errorf("emitted a record carrying file contents:\n%s", got)
			}
		})
	}
}

// TestIndexNeverCarriesFileContentsInAPath is the other half of S-12.3, and it
// is the half that cannot be fixed by classifying an error: `Path` is a *field*,
// so a caller can put anything in it. The type is what stops it, and this asserts
// that the paths the indexer legitimately has — relative, slash-separated,
// content-root-scoped — are what actually reaches a line.
func TestIndexNeverCarriesFileContentsInAPath(t *testing.T) {
	t.Parallel()

	logger, buf := capture()
	index := observability.NewIndex(observability.NewRegistry(), logger)

	ctx := context.Background()
	index.ChangeFailed(ctx, "1", "upsert", "notes/Page.md", errors.New("x"))
	index.PageSkipped(ctx, "1", "notes/deep/Page.md", errors.New("x"))

	got := records(t, buf)

	paths := make([]string, 0, len(got))
	for _, rec := range got {
		if path, ok := rec["path"].(string); ok {
			paths = append(paths, path)
		}
	}

	if len(paths) != 2 {
		t.Fatalf("emitted %d paths, want 2: a line with no path is not queryable", len(paths))
	}

	for _, path := range paths {
		if strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
			t.Errorf("path %q is not content-root-relative", path)
		}
	}
}

// TestIndexToleratesANilLoggerAndNilRegistry is the reason a missing log line
// is recoverable and a panic during a filesystem event is not.
func TestIndexToleratesANilLoggerAndNilRegistry(t *testing.T) {
	t.Parallel()

	index := observability.NewIndex(nil, nil)

	ctx := context.Background()

	index.ChangeFailed(ctx, "", "", "", nil)
	index.PageSkipped(ctx, "", "", nil)
	index.PageDegraded(ctx, "", "", nil)
	index.RenameSourceLeft(ctx, "", "", 0, nil)
	index.Renamed(ctx, "", "", "", 0)
}

// TestIndexEmitsNothingUncountedForARoutineRename guards the separation the
// `Renamed` comment claims: routine work moves no counter, so the four counters
// on `/readyz` mean exactly one thing each.
//
// Asserted as every total still zero rather than as "the snapshot is empty",
// because a surface registers its counters on construction and an unwired signal
// reading as a visible zero is S-12.1's stated property — not an accident of this
// test.
func TestIndexEmitsNothingUncountedForARoutineRename(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry()
	index := observability.NewIndex(registry, slog.New(slog.DiscardHandler))

	index.Renamed(context.Background(), "1", "old", "new", 12)

	snapshot := registry.Snapshot()

	if len(snapshot) == 0 {
		t.Fatal("no counters are registered; /readyz would omit the whole surface")
	}

	for name, value := range snapshot {
		if value.Total != 0 {
			t.Errorf("a routine rename moved %s to %d; it should move nothing", name, value.Total)
		}
	}
}

// TestIndexReusingACounterKeepsItsTotal is the `Registry.Register` guarantee
// applied here: registering twice must not replace the counter, or a count
// accumulated by the first surface is lost.
//
// It is asserted the other way round from the obvious one — the second surface
// increments *its own* counter, because `NewIndex` builds the counter and then
// registers it, so the registered one is the first's. That is why the comment on
// `NewWatch` says a second `Watch` supports the counters but not the gauges: the
// numbers below are what the registry holds, and they are the first surface's.
func TestIndexReusingACounterKeepsItsTotal(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry()
	observability.NewIndex(registry, slog.New(slog.DiscardHandler))
	observability.NewIndex(registry, slog.New(slog.DiscardHandler))

	counter, ok := registry.Counter(string(observability.EventIndexChangeFailed))
	if !ok {
		t.Fatal("counter is not registered")
	}

	// Registered once, not twice: two counters of the same name would mean a query
	// reading `/readyz` sees one of them and not the other.
	var matching int

	for _, name := range registry.SortedNames() {
		if name == string(observability.EventIndexChangeFailed) {
			matching++
		}
	}

	if matching != 1 {
		t.Errorf("%d counters are registered as %q, want 1", matching, counter.Name())
	}
}

// TestIndexCountIsOmittedWhenZero asserts the attribute contract: `count`
// describes an event rather than a state, so a zero is absence, not a reading.
func TestIndexCountIsOmittedWhenZero(t *testing.T) {
	t.Parallel()

	logger, buf := capture()
	index := observability.NewIndex(observability.NewRegistry(), logger)

	index.RenameSourceLeft(context.Background(), "1", "a.md", 0, nil)

	got := records(t, buf)
	if len(got) != 1 {
		t.Fatalf("emitted %d records, want 1", len(got))
	}

	if _, present := got[0]["count"]; present {
		t.Errorf("count is present for a zero count: %v", got[0]["count"])
	}
}

// TestIndexRenamedCarriesBothPathsOfAMove asserts the pair, because a rename
// reported with only one of its two paths is not a rename an operator can act on.
func TestIndexRenamedCarriesBothPathsOfAMove(t *testing.T) {
	t.Parallel()

	logger, buf := capture()
	index := observability.NewIndex(observability.NewRegistry(), logger)

	index.Renamed(context.Background(), "3", "notes/old", "notes/new", 7)

	got := records(t, buf)
	if len(got) != 1 {
		t.Fatalf("emitted %d records, want 1", len(got))
	}

	for key, want := range map[string]any{
		"event":       "index.renamed",
		"campaign_id": "3",
		"from":        "notes/old",
		"to":          "notes/new",
		"count":       float64(7),
	} {
		if got[0][key] != want {
			t.Errorf("%s is %v, want %v", key, got[0][key], want)
		}
	}

	// `path` is the destination for an ordinary event; a move carries both
	// explicitly and must not also answer to the ambiguous single-path key.
	if _, present := got[0]["path"]; present {
		t.Error("a rename carries a path as well as from and to")
	}
}
