// Tests for the reconciliation convergence fix.
//
// One defect, one symptom, one argument. A dropped watch that stays in the
// watcher's bookkeeping is re-dropped on every verification pass, so a supervisor
// that reads `Dropped` as "reindex once" reindexes forever, and a report whose
// `Dropped` never reaches zero cannot be read as "nothing is wrong right now".

package content_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
)

// TestAReconcileReportConverges is the assertion: run `Reverify` twice against an
// unchanged tree and the second report must be a settled one.
//
// It is written as a *sequence* rather than a single check because that is the
// property being claimed. A test that verifies once can only say "the drop was
// reported", and a report that says so once is what the broken code does too.
func TestAReconcileReportConverges(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// A directory the watcher watches, which then disappears. `os.MkdirAll` plus
	// a `RemoveAll` is the shape a sync client produces, and it is the shape that
	// leaves a stale entry: the removal is not always delivered as an event, so
	// the reconciliation is what notices.
	if err := os.MkdirAll(filepath.Join(dir, "lore"), 0o700); err != nil {
		t.Fatalf("mkdir lore: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(dir, "lore", "Gate.md"),
		[]byte("# Gate\n"),
		0o600,
	); err != nil {
		t.Fatalf("write Gate.md: %v", err)
	}

	roots := content.NewRegistry(content.RefuseSymlinks)
	if _, err := roots.Open("greyhaven", dir); err != nil {
		t.Fatalf("open the content root: %v", err)
	}

	t.Cleanup(func() {
		if err := roots.Close(); err != nil {
			t.Errorf("close the roots: %v", err)
		}
	})

	watched, err := content.NewWatcher(
		context.Background(),
		roots,
		[]content.WatchRoot{{CampaignID: 1, Slug: "greyhaven", Dir: dir}},
		func(context.Context, content.Change) {},
		nil,
	)
	if err != nil {
		t.Fatalf("open the watcher: %v", err)
	}

	t.Cleanup(func() {
		if closeErr := watched.Close(); closeErr != nil {
			t.Errorf("close the watcher: %v", closeErr)
		}
	})

	if err := os.RemoveAll(filepath.Join(dir, "lore")); err != nil {
		t.Fatalf("remove lore: %v", err)
	}

	ctx := context.Background()

	// The first pass reconciles against the tree and drops the watch.
	first := watched.Reverify(ctx)
	if len(first) != 1 {
		t.Fatalf("want one report, got %d", len(first))
	}

	// The second pass sees an unchanged tree and an unchanged watch set. Everything
	// it reports is a watch it is still holding, and nothing is a watch it is
	// dropping for the second time.
	second := watched.Reverify(ctx)
	if len(second) != 1 {
		t.Fatalf("want one report, got %d", len(second))
	}

	if second[0].Dropped != 0 {
		t.Errorf("a settled tree dropped %d watches on the second pass, want 0: "+
			"the dropped directory is still in the watcher's bookkeeping, so it is "+
			"dropped again on every pass and a supervisor treating Dropped as "+
			"'reindex once' reindexes forever", second[0].Dropped)
	}

	if second[0].Added != 0 || second[0].Failed != nil {
		t.Errorf("a settled tree added %d watches and failed %v, want 0 and none",
			second[0].Added, second[0].Failed)
	}

	// And a third, for the same reason: convergence means the fixed point, not a
	// report that happens to be clean once.
	third := watched.Reverify(ctx)
	if len(third) != 1 {
		t.Fatalf("want one report, got %d", len(third))
	}

	if third[0].Dropped != 0 || third[0].Added != 0 {
		t.Errorf("the third pass is not settled: dropped=%d added=%d",
			third[0].Dropped, third[0].Added)
	}
}

// TestAReconcileStillReAddsAWatchThatVanished guards the fix from over-reaching.
//
// `forgetUnder` removes anything the last walk did not want. A watch that was
// *silently* lost — the notifier dropped it while the tree still has it — must
// still be re-added on the next pass, because that is the case S-4.5's re-verify
// exists for and it is invisible from the watcher's own bookkeeping.
func TestAReconcileStillReAddsAWatchThatVanished(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dir, "lore"), 0o700); err != nil {
		t.Fatalf("mkdir lore: %v", err)
	}

	roots := content.NewRegistry(content.RefuseSymlinks)
	if _, err := roots.Open("greyhaven", dir); err != nil {
		t.Fatalf("open the content root: %v", err)
	}

	t.Cleanup(func() {
		if err := roots.Close(); err != nil {
			t.Errorf("close the roots: %v", err)
		}
	})

	// A notifier that reports an empty watch list while every watch is still in
	// place: precisely the disagreement `Reverify` is for, and the one a real
	// inotify watch can produce without any event at all.
	// `leakyNotifier` is the shape this needs: a real fsnotify notifier with one
	// nominated directory hidden from `WatchList` while its watch stays in place.
	notifier := newLeakyNotifier(t)

	watched, err := content.NewWatcher(
		context.Background(),
		roots,
		[]content.WatchRoot{{CampaignID: 1, Slug: "greyhaven", Dir: dir}},
		func(context.Context, content.Change) {},
		nil,
		content.WithNotifier(notifier),
	)
	if err != nil {
		t.Fatalf("open the watcher: %v", err)
	}

	t.Cleanup(func() {
		if closeErr := watched.Close(); closeErr != nil {
			t.Errorf("close the watcher: %v", closeErr)
		}
	})

	notifier.lose(dir)

	reports := watched.Reverify(context.Background())
	if len(reports) != 1 {
		t.Fatalf("want one report, got %d", len(reports))
	}

	if reports[0].Added == 0 {
		t.Errorf("a notifier holding no watches produced %+v; the reconciliation "+
			"consulted the watcher's own bookkeeping instead of the notifier's, "+
			"which is the self-consistency check that makes a silently lost watch "+
			"permanent", reports[0])
	}

	if reports[0].Dropped != 0 {
		t.Errorf("re-adding lost watches also dropped %d; the two are different "+
			"answers and must not be confused", reports[0].Dropped)
	}
}
