package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

func testVault(t *testing.T) *Vault {
	t.Helper()
	return New(t.TempDir())
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	v := testVault(t)

	got, err := v.Put(ctx, pluginapi.Note{Path: "notes/hello.md", Content: "# hi\n"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "notes/hello.md" {
		t.Fatalf("path = %q", got.Path)
	}
	if got.Hash == "" {
		t.Fatal("empty hash")
	}
	if want := HashContent([]byte("# hi\n")); got.Hash != want {
		t.Fatalf("hash = %q want %q", got.Hash, want)
	}

	back, err := v.Get(ctx, "notes/hello.md")
	if err != nil {
		t.Fatal(err)
	}
	if back.Content != "# hi\n" || back.Hash != got.Hash || back.Path != "notes/hello.md" {
		t.Fatalf("get = %+v", back)
	}

	list, err := v.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0] != "notes/hello.md" {
		t.Fatalf("list = %v", list)
	}
}

func TestPutConflict(t *testing.T) {
	ctx := context.Background()
	v := testVault(t)

	first, err := v.Put(ctx, pluginapi.Note{Path: "a.md", Content: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	// Matching precondition succeeds.
	if _, err := v.Put(ctx, pluginapi.Note{Path: "a.md", Content: "v2", Hash: first.Hash}); err != nil {
		t.Fatal(err)
	}
	// Stale hash fails.
	if _, err := v.Put(ctx, pluginapi.Note{Path: "a.md", Content: "v3", Hash: first.Hash}); !errors.Is(err, pluginapi.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	// Precondition on nonexistent file fails.
	if _, err := v.Put(ctx, pluginapi.Note{Path: "new.md", Content: "x", Hash: "deadbeef"}); !errors.Is(err, pluginapi.ErrConflict) {
		t.Fatalf("expected ErrConflict for new file, got %v", err)
	}
	// Empty hash disables the check.
	if _, err := v.Put(ctx, pluginapi.Note{Path: "a.md", Content: "v4"}); err != nil {
		t.Fatal(err)
	}
}

func TestTraversalRejection(t *testing.T) {
	ctx := context.Background()
	v := testVault(t)

	for _, p := range []string{"../x.md", "/abs.md", "..\\x.md", "a\\b.md", "a.txt", "noext", ".md/.."} {
		if _, err := v.Get(ctx, p); err == nil {
			t.Fatalf("Get(%q) accepted", p)
		}
		if _, err := v.Put(ctx, pluginapi.Note{Path: p, Content: "x"}); err == nil {
			t.Fatalf("Put(%q) accepted", p)
		}
		if err := v.Delete(ctx, p); err == nil {
			t.Fatalf("Delete(%q) accepted", p)
		}
	}
	// Get on missing valid path → NotFound.
	if _, err := v.Get(ctx, "missing.md"); !errors.Is(err, pluginapi.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	// Ensure no file escaped the vault root.
	entries, err := os.ReadDir(v.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("vault root not empty: %v", entries)
	}
	parent := filepath.Dir(v.Root)
	if _, err := os.Stat(filepath.Join(parent, "x.md")); !os.IsNotExist(err) {
		t.Fatal("traversal wrote outside vault")
	}
}

func TestListPrefix(t *testing.T) {
	ctx := context.Background()
	v := testVault(t)

	for _, p := range []string{"a.md", "notes/b.md", "notes/c.md", "other/d.md", "notes-extra/e.md"} {
		if _, err := v.Put(ctx, pluginapi.Note{Path: p, Content: "x"}); err != nil {
			t.Fatal(err)
		}
	}

	all, err := v.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.md", "notes-extra/e.md", "notes/b.md", "notes/c.md", "other/d.md"}
	if len(all) != len(want) {
		t.Fatalf("list all = %v", all)
	}
	for i := range want {
		if all[i] != want[i] {
			t.Fatalf("list all = %v want %v", all, want)
		}
	}

	sub, err := v.List(ctx, "notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(sub) != 2 || sub[0] != "notes/b.md" || sub[1] != "notes/c.md" {
		t.Fatalf("list notes = %v", sub)
	}

	file, err := v.List(ctx, "notes/b")
	if err != nil {
		t.Fatal(err)
	}
	if len(file) != 1 || file[0] != "notes/b.md" {
		t.Fatalf("list file-prefix = %v", file)
	}

	if _, err := v.List(ctx, "../x"); err == nil {
		t.Fatal("List traversal accepted")
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	v := testVault(t)

	if _, err := v.Put(ctx, pluginapi.Note{Path: "gone.md", Content: "bye"}); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete(ctx, "gone.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(ctx, "gone.md"); !errors.Is(err, pluginapi.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := v.Delete(ctx, "gone.md"); !errors.Is(err, pluginapi.ErrNotFound) {
		t.Fatalf("expected ErrNotFound on second delete, got %v", err)
	}
}
