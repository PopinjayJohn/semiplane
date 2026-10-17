package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// collect gathers events until quiet (no event for quiet window) or the
// deadline passes. It never sleeps for correctness: every wait is a bounded
// poll against a deadline.
func collect(ch <-chan Event, total, quiet time.Duration) []Event {
	var out []Event
	deadline := time.Now().Add(total)
	quietTimer := time.NewTimer(quiet)
	defer quietTimer.Stop()
	for {
		remain := time.Until(deadline)
		if remain <= 0 {
			return out
		}
		if !quietTimer.Stop() {
			select {
			case <-quietTimer.C:
			default:
			}
		}
		quietTimer.Reset(minDur(quiet, remain))
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-quietTimer.C:
			if len(out) > 0 {
				return out
			}
			// Nothing yet; keep polling until the deadline.
		}
	}
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func drain(ch <-chan Event) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func newTestWatcher(t *testing.T, root string, debounce time.Duration) *Watcher {
	t.Helper()
	w, err := New(root, debounce)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return w
}

func TestCreateFileEmitsEvent(t *testing.T) {
	root := t.TempDir()
	w := newTestWatcher(t, root, 100*time.Millisecond)

	target := filepath.Join(root, "hello.md")
	if err := os.WriteFile(target, []byte("# hi\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	events := collect(w.Events(), 3*time.Second, 300*time.Millisecond)
	for _, ev := range events {
		if ev.Path == "hello.md" && (ev.Op == OpCreate || ev.Op == OpWrite) {
			return
		}
	}
	t.Fatalf("no create/write event for hello.md, got %v", events)
}

func TestAtomicWriteRenameYieldsTargetEvent(t *testing.T) {
	root := t.TempDir()
	w := newTestWatcher(t, root, 100*time.Millisecond)

	tmp, err := os.CreateTemp(root, "*.tmp")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	if _, err := tmp.WriteString("# atomic\n"); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		t.Fatalf("close tmp: %v", err)
	}
	target := filepath.Join(root, "target.md")
	if err := os.Rename(tmpPath, target); err != nil {
		t.Fatalf("rename: %v", err)
	}

	events := collect(w.Events(), 3*time.Second, 300*time.Millisecond)
	for _, ev := range events {
		if ev.Path == "target.md" {
			return
		}
	}
	t.Fatalf("no event for rename target target.md, got %v", events)
}

func TestNewSubdirectoryIsWatched(t *testing.T) {
	root := t.TempDir()
	w := newTestWatcher(t, root, 100*time.Millisecond)

	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Give the watcher a bounded moment to pick up the new directory
	// before writing inside it; the assertion below still polls.
	deadline := time.Now().Add(3 * time.Second)
	for {
		inner := filepath.Join(sub, "inner.md")
		if err := os.WriteFile(inner, []byte("# inner\n"), 0o644); err != nil {
			t.Fatalf("write inner: %v", err)
		}
		events := collect(w.Events(), 500*time.Millisecond, 300*time.Millisecond)
		found := false
		for _, ev := range events {
			if ev.Path == filepath.ToSlash(filepath.Join("sub", "inner.md")) {
				found = true
				break
			}
		}
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no event for sub/inner.md, last batch %v", events)
		}
	}
}

func TestDebounceCollapsesRapidWrites(t *testing.T) {
	root := t.TempDir()
	w := newTestWatcher(t, root, 100*time.Millisecond)

	target := filepath.Join(root, "burst.md")
	if err := os.WriteFile(target, []byte("v0\n"), 0o644); err != nil {
		t.Fatalf("write v0: %v", err)
	}
	// Let the initial create flush out, then drain so the burst is measured alone.
	for _, ev := range collect(w.Events(), 3*time.Second, 300*time.Millisecond) {
		_ = ev
	}
	drain(w.Events())

	for i := 1; i <= 5; i++ {
		if err := os.WriteFile(target, []byte("# v\n"), 0o644); err != nil {
			t.Fatalf("write burst: %v", err)
		}
	}

	var count int
	for _, ev := range collect(w.Events(), 800*time.Millisecond, 300*time.Millisecond) {
		if ev.Path == "burst.md" {
			count++
		}
	}
	if count > 2 {
		t.Fatalf("debounce failed: %d events for 5 rapid writes, want <=2", count)
	}
	if count == 0 {
		t.Fatalf("no event at all for burst.md")
	}
}

func TestNonMarkdownIgnored(t *testing.T) {
	root := t.TempDir()
	w := newTestWatcher(t, root, 50*time.Millisecond)

	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	events := collect(w.Events(), 600*time.Millisecond, 300*time.Millisecond)
	for _, ev := range events {
		if ev.Path == "note.txt" {
			t.Fatalf("unexpected event for non-markdown file: %v", ev)
		}
	}
}

func TestRelativePath(t *testing.T) {
	rel, err := RelativePath("/vault", "/vault/a/b.md")
	if err != nil {
		t.Fatalf("RelativePath: %v", err)
	}
	if rel != "a/b.md" {
		t.Fatalf("got %q want a/b.md", rel)
	}
	if _, err := RelativePath("/vault", "/other/x.md"); err == nil {
		t.Fatalf("expected error for path outside root")
	}
}
