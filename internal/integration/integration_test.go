// Package integration proves the Phase 1/B storage round-trip
// (write → index → search → backlink → external-edit) against a copy of
// the e2e seed vault. It is the `make integration` evidence for the
// storage core; rule math stays in Go unit tests per the E2E contract.
package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/PopinjayJohn/semiplane/internal/index"
	"github.com/PopinjayJohn/semiplane/internal/vault"
	"github.com/PopinjayJohn/semiplane/internal/watch"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

func copyVault(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("copy vault: %v", err)
	}
	return dst
}

func TestStorageRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := copyVault(t, "../../testdata/vaults/e2e")

	v := vault.New(root)
	db, err := index.Open(filepath.Join(root, ".semiplane", "index.db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer func() { _ = db.Close() }()
	ix := index.New(db)

	// 1. Reindex the seed vault.
	n, err := ix.Reindex(root)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if n == 0 {
		t.Fatal("reindex indexed 0 notes")
	}

	// 2. Search finds seed content (quest-board.md posts cellar work).
	hits, err := ix.Search("cellar", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !slices.ContainsFunc(hits, func(h pluginapi.SearchHit) bool { return h.Path == "quest-board.md" }) {
		t.Fatalf("search 'cellar' missed quest-board.md: %+v", hits)
	}

	// 3. Backlinks: tavern.md links [[session-1]].
	back, err := ix.Backlinks("session-1.md")
	if err != nil {
		t.Fatalf("backlinks: %v", err)
	}
	if !slices.Contains(back, "tavern.md") {
		t.Fatalf("backlinks(session-1.md) missing tavern.md: %v", back)
	}

	// 4. Write path: atomic Put + same-tx index upsert.
	wrote, err := v.Put(ctx, pluginapi.Note{
		Path:    "field-notes.md",
		Content: "# Field Notes\n\nScouted [[tavern]]; the grimalkin prowls at dusk.\n",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	suppress := vault.NewSuppress()
	suppress.Note(wrote.Path, wrote.Hash)
	if err := ix.Upsert(wrote.Path, wrote.Content, wrote.Hash, time.Now().Unix()); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	hits, err = ix.Search("grimalkin", 10)
	if err != nil || len(hits) != 1 || hits[0].Path != "field-notes.md" {
		t.Fatalf("search new note: hits=%+v err=%v", hits, err)
	}
	back, err = ix.Backlinks("tavern.md")
	if err != nil {
		t.Fatalf("backlinks: %v", err)
	}
	if !slices.Contains(back, "field-notes.md") {
		t.Fatalf("backlinks(tavern.md) missing field-notes.md: %v", back)
	}

	// 5. Own-write echo: the watch event for our Put carries our hash → skip.
	if !suppress.IsEcho(wrote.Path, wrote.Hash) {
		t.Fatal("own write not recognised as echo")
	}

	// 6. External edit (Obsidian-style direct write) with a stale precondition
	//    must conflict on Put, then land via re-read + upsert.
	if _, err := v.Put(ctx, pluginapi.Note{Path: wrote.Path, Content: "stale", Hash: wrote.Hash}); err != nil {
		t.Fatalf("put with fresh hash should succeed: %v", err)
	}
	fresh, err := v.Get(ctx, wrote.Path)
	if err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if _, err := v.Put(ctx, pluginapi.Note{Path: wrote.Path, Content: "stale", Hash: wrote.Hash}); !errors.Is(err, pluginapi.ErrConflict) {
		t.Fatalf("stale put: want ErrConflict, got %v", err)
	}
	abs := filepath.Join(root, "field-notes.md")
	ext := "# Field Notes\n\nScouted [[tavern]]; the mooncalf trumpets at midnight.\n"
	if err := os.WriteFile(abs, []byte(ext), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	after, err := v.Get(ctx, "field-notes.md")
	if err != nil {
		t.Fatalf("get after external edit: %v", err)
	}
	if suppress.IsEcho(after.Path, after.Hash) {
		t.Fatal("external edit wrongly classified as echo")
	}
	_ = fresh
	fi, _ := os.Stat(abs)
	if err := ix.Upsert(after.Path, after.Content, after.Hash, fi.ModTime().Unix()); err != nil {
		t.Fatalf("upsert external edit: %v", err)
	}
	hits, err = ix.Search("mooncalf", 10)
	if err != nil || !slices.ContainsFunc(hits, func(h pluginapi.SearchHit) bool { return h.Path == "field-notes.md" }) {
		t.Fatalf("search external edit: hits=%+v err=%v", hits, err)
	}

	// 7. Watcher observes an external create.
	w, err := watch.New(root, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("watch new: %v", err)
	}
	defer func() { _ = w.Close() }()
	if err := os.WriteFile(filepath.Join(root, "watch-probe.md"), []byte("# Probe\n"), 0o644); err != nil {
		t.Fatalf("probe write: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	seen := false
	for !seen && time.Now().Before(deadline) {
		select {
		case ev := <-w.Events():
			if ev.Path == "watch-probe.md" {
				seen = true
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !seen {
		t.Fatal("watcher did not report watch-probe.md within 5s")
	}

	// 8. Delete cleans search + backlinks.
	if err := v.Delete(ctx, "field-notes.md"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := ix.Delete("field-notes.md"); err != nil {
		t.Fatalf("index delete: %v", err)
	}
	hits, err = ix.Search("mooncalf", 10)
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if slices.ContainsFunc(hits, func(h pluginapi.SearchHit) bool { return h.Path == "field-notes.md" }) {
		t.Fatalf("deleted note still searchable: %+v", hits)
	}
	back, err = ix.Backlinks("tavern.md")
	if err != nil {
		t.Fatalf("backlinks: %v", err)
	}
	if slices.Contains(back, "field-notes.md") {
		t.Fatalf("deleted note still in backlinks: %v", back)
	}
}
