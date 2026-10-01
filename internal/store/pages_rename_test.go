package store_test

import (
	"errors"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// The convergence tests.
//
// A directory watcher is a lossy narrator: it drops events, it coalesces them, a
// sync client renames a directory as one event and deletes a hundred files as a
// hundred more. So the property under test is never "the events were applied" —
// it is that the index agrees with the tree afterwards, through every sequence
// that can produce a file being moved, removed, or brought back.
//
// The FTS assertions are the load-bearing half. `pages_fts` is an external-content
// table that stores no copy of the text, so a row and its index entries can drift
// without anything failing: the index answers no query of its own, and the drift
// surfaces only as a search result naming a page that no longer exists. Every test
// below ends with `assertSearchIndexAgrees`, which is FTS5's own check and the
// only one that notices.

// TestRenamePageMovesTheRowAndItsSearchEntry is the basic sequence: one page
// renamed, one row, and the search hit follows.
//
// The row's identity is the point, and it is asserted rather than assumed. A
// rename re-keys; it does not delete and re-insert, because the row's `id` is the
// FTS5 `rowid` and `created_at` is the page's age. An implementation that did a
// remove plus an upsert would pass every assertion below except these two, and
// would make a page renamed in Obsidian look like a page created today.
func TestRenamePageMovesTheRowAndItsSearchEntry(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	before := seedPage(t, db, campaign, "lore/old.md", "Svartalfheim", "the wyvern sleeps")

	if err := db.RenamePage(t.Context(), campaign.ID, "lore/old.md", "lore/new.md"); err != nil {
		t.Fatalf("RenamePage() error = %v, want nil", err)
	}

	// One row, at the new path, and none at the old. Counted rather than probed,
	// so a rename that duplicated the row — an upsert that forgot to delete the
	// source — fails instead of looking correct.
	pages, err := db.PagesForCampaign(t.Context(), campaign.ID)
	if err != nil {
		t.Fatalf("PagesForCampaign() error = %v, want nil", err)
	}

	if len(pages) != 1 {
		t.Fatalf("PagesForCampaign() returned %d rows, want 1; the rename duplicated the page",
			len(pages))
	}

	if pages[0].Path != "lore/new.md" {
		t.Errorf("Path = %q, want %q", pages[0].Path, "lore/new.md")
	}

	if pages[0].ID != before.ID {
		t.Errorf("ID = %d, want %d; a rename re-keys the row and does not replace it",
			pages[0].ID, before.ID)
	}

	if !pages[0].CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s; a moved page is not a new page",
			pages[0].CreatedAt, before.CreatedAt)
	}

	// The content is unchanged, so the hash is carried across untouched. A rename
	// that re-read and re-hashed could only have got the same value by luck.
	if pages[0].ContentHash != before.ContentHash {
		t.Errorf(
			"ContentHash = %q, want %q; the bytes did not change",
			pages[0].ContentHash,
			before.ContentHash,
		)
	}

	// And the search hit moved with it. Searched by a word from the *old* path's
	// neighbours so the hit is attributed to the row, not to a stale FTS entry: the
	// path is not in the index, only the body is.
	hits, err := db.SearchPages(t.Context(), store.PageSearch{Query: "wyvern"}, domain.Requestor{})
	if err != nil {
		t.Fatalf("SearchPages() error = %v, want nil", err)
	}

	if len(hits) != 1 {
		t.Fatalf("SearchPages() returned %d hits, want 1", len(hits))
	}

	if hits[0].Path != "lore/new.md" {
		t.Errorf("hit.Path = %q, want %q; the FTS entry did not move with the row",
			hits[0].Path, "lore/new.md")
	}

	assertSearchIndexAgrees(t, db)
}

// TestDeleteAndRestoreIsTheSameIndex is undelete convergence: a page removed from
// the filesystem and put back must arrive at the same state, which for this
// schema means a new rowid and the same content.
//
// `AUTOINCREMENT` is why the new rowid is expected rather than tolerated — see
// migration 0007. Reusing one would bind the deleted page's orphaned FTS entries
// to whatever page took the id next, and a search result would then name a file
// that never contained the words.
func TestDeleteAndRestoreIsTheSameIndex(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	const path = "lore/restore.md"

	first := seedPage(t, db, campaign, path, "Restore", "the vault of svartalfheim")

	if err := db.DeletePage(t.Context(), campaign.ID, path); err != nil {
		t.Fatalf("DeletePage() error = %v, want nil", err)
	}

	// No row and no hit. Both, because a row without its index entries is a page
	// no listing shows and no search finds, and a hit without its row is a search
	// result for a page that 404s — the failure mode a prune exists to remove.
	assertSearchMisses(t, db, store.PageSearch{Query: "vault"})

	if _, err := db.PageByPath(t.Context(), campaign.ID, path); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("PageByPath() after delete error = %v, want ErrNotFound", err)
	}

	// The file comes back, which the watcher reports as an upsert.
	second := seedPage(t, db, campaign, path, "Restore", "the vault of svartalfheim")

	if second.ID == first.ID {
		t.Errorf("restored page reused id %d; AUTOINCREMENT exists so a deleted page's "+
			"orphaned FTS entries cannot bind to the next page", second.ID)
	}

	hits, err := db.SearchPages(t.Context(), store.PageSearch{Query: "vault"}, domain.Requestor{})
	if err != nil {
		t.Fatalf("SearchPages() after restore error = %v, want nil", err)
	}

	if len(hits) != 1 {
		t.Fatalf("SearchPages() after restore returned %d hits, want 1", len(hits))
	}

	if hits[0].ID != second.ID {
		t.Errorf(
			"hit.ID = %d, want %d; the index is naming the previous row",
			hits[0].ID,
			second.ID,
		)
	}

	assertSearchIndexAgrees(t, db)
}

// TestRenamePagesUnderMovesAWholeDirectory is the operation RenamePage cannot
// express: a sync client renames a folder and produces one event for it.
//
// The per-page answer would be N upserts plus N deletes, which re-reads and
// re-hashes every file in the folder — the one thing a re-key exists to avoid —
// and would still need the old paths pruned afterwards.
func TestRenamePagesUnderMovesAWholeDirectory(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	// Nested, because a folder rename that only handles the files directly inside
	// it leaves the subdirectory's pages behind, and that is the shape a real
	// vault has.
	seedPage(t, db, campaign, "lore/shore/cliffs.md", "Cliffs", "the cliffs of svartalfheim")
	seedPage(t, db, campaign, "lore/shore/cave/hollow.md", "Hollow", "a hollow below svartalfheim")
	seedPage(t, db, campaign, "lore/shore/cave/deep/strata.md", "Strata", "strata beneath")

	// Outside the subtree, and named so that a prefix match on `lore` rather than
	// on `lore/shore` would pick it up.
	seedPage(t, db, campaign, "lore/shorehouse.md", "Shorehouse", "the shorehouse")

	moved, err := db.RenamePagesUnder(t.Context(), campaign.ID, "lore/shore", "lore/coast")
	if err != nil {
		t.Fatalf("RenamePagesUnder() error = %v, want nil", err)
	}

	if moved != 3 {
		t.Errorf("RenamePagesUnder() moved %d rows, want 3", moved)
	}

	want := []string{
		"lore/coast/cave/deep/strata.md",
		"lore/coast/cave/hollow.md",
		"lore/coast/cliffs.md",
		"lore/shorehouse.md",
	}

	assertPagePaths(t, db, campaign.ID, want)

	// Every moved page is still findable, which is the FTS half of the move: a
	// subtree rename that moved the rows and dropped the entries would leave a
	// wiki whose pages are all reachable by URL and by none of its own links.
	for _, query := range []string{"cliffs", "hollow", "strata", "beneath"} {
		hits, err := db.SearchPages(
			t.Context(),
			store.PageSearch{Query: query},
			domain.Requestor{},
		)
		if err != nil {
			t.Fatalf("SearchPages(%q) error = %v, want nil", query, err)
		}

		if len(hits) != 1 {
			t.Errorf("SearchPages(%q) returned %d hits, want 1", query, len(hits))
		}
	}

	assertSearchIndexAgrees(t, db)
}

// TestRenamePagesUnderResolvesADestinationCollision is the case where two files
// would end up with one name, and it is a decision rather than a refusal.
//
// The filesystem has already made the call: one file is at the destination path
// now, and the row for the other describes a page that no longer exists under
// that name. Refusing with ErrConflict would leave a folder move that has already
// happened unindexed, and the only recovery a full rescan — so the destination
// row is dropped and the moved one takes its place.
func TestRenamePagesUnderResolvesADestinationCollision(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	seedPage(t, db, campaign, "old/cave.md", "From Old", "the wyvern of svartalfheim")
	seedPage(t, db, campaign, "new/cave.md", "From New", "the vault of svartalfheim")

	if _, err := db.RenamePagesUnder(t.Context(), campaign.ID, "old", "new"); err != nil {
		t.Fatalf("RenamePagesUnder() error = %v, want nil", err)
	}

	pages, err := db.PagesForCampaign(t.Context(), campaign.ID)
	if err != nil {
		t.Fatalf("PagesForCampaign() error = %v, want nil", err)
	}

	if len(pages) != 1 {
		t.Fatalf("PagesForCampaign() returned %d rows, want 1; the collision left a duplicate",
			len(pages))
	}

	// The moved row, not the one that was already there: the file at that path is
	// the one that moved.
	if pages[0].Title != "From Old" {
		t.Errorf("Title = %q, want %q; the destination row survived the collision",
			pages[0].Title, "From Old")
	}

	// The displaced page's terms are gone, which is the assertion a "delete then
	// insert" implementation would fail.
	assertSearchMisses(t, db, store.PageSearch{Query: "vault"})

	assertSearchIndexAgrees(t, db)
}

// TestRenamePageRefusesADestinationCollision is the single-page half, and it is
// the opposite decision on purpose.
//
// A page rename knows both paths and can be refused, because a caller that gets
// ErrConflict converges by indexing the destination and dropping the source. A
// subtree rename has no such caller — it is answering an event for a folder, and
// the folder is already moved — so it resolves. Leaving the single-page one
// strict means a caller that wanted the other behaviour has to ask for it.
func TestRenamePageRefusesADestinationCollision(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	source := seedPage(t, db, campaign, "a.md", "A", "the wyvern sleeps")
	occupied := seedPage(t, db, campaign, "b.md", "B", "the vault holds a secret")

	err := db.RenamePage(t.Context(), campaign.ID, source.Path, occupied.Path)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("RenamePage() onto an occupied path error = %v, want ErrConflict", err)
	}

	// Refused, not half-applied: the transaction rolled back, so both rows are
	// where they were.
	assertPagePaths(t, db, campaign.ID, []string{"a.md", "b.md"})

	assertSearchIndexAgrees(t, db)
}

// TestRenamePageReportsAMissingSource is the sequence the indexer's fallback
// exists for: a rename the watcher saw as a remove followed by a rename, which is
// legal and which means the source row is already gone.
func TestRenamePageReportsAMissingSource(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	err := db.RenamePage(t.Context(), campaign.ID, "lore/never-existed.md", "lore/new.md")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RenamePage() of a missing source error = %v, want ErrNotFound", err)
	}
}

// TestRenamePagesUnderRefusesTheContentRoot is the argument-validation half. The
// root's name is `campaigns.content_root` and changing it is a registration, not
// a page operation; a caller that means it has a bug rather than a case.
func TestRenamePagesUnderRefusesTheContentRoot(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	cases := map[string]struct{ oldDir, newDir string }{
		"an empty source":         {oldDir: "", newDir: "lore"},
		"an empty destination":    {oldDir: "lore", newDir: ""},
		"the root as source":      {oldDir: ".", newDir: "lore"},
		"the root as destination": {oldDir: "lore", newDir: "."},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := db.RenamePagesUnder(
				t.Context(),
				campaign.ID,
				testCase.oldDir,
				testCase.newDir,
			)
			if !errors.Is(err, store.ErrInvalidPagePath) {
				t.Errorf("RenamePagesUnder(%q, %q) error = %v, want ErrInvalidPagePath",
					testCase.oldDir, testCase.newDir, err)
			}
		})
	}
}

// TestRenamePagesUnderMovesNothingWhenTheSubtreeIsAbsent is the no-op. A watcher
// reports the moves of directories a vault does not have — an empty folder, a
// folder that was already renamed by somebody else — and a zero count is the
// correct answer rather than a fault.
func TestRenamePagesUnderMovesNothingWhenTheSubtreeIsAbsent(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	seedPage(t, db, campaign, "lore/here.md", "Here", "the wyvern sleeps")

	moved, err := db.RenamePagesUnder(t.Context(), campaign.ID, "absent", "lore")
	if err != nil {
		t.Fatalf("RenamePagesUnder() error = %v, want nil", err)
	}

	if moved != 0 {
		t.Errorf("RenamePagesUnder() moved %d rows, want 0", moved)
	}

	assertPagePaths(t, db, campaign.ID, []string{"lore/here.md"})
}

// TestRenamePagesUnderIsScopedToOneCampaign is the tenancy half. A path is
// campaign-relative, so `lore/x.md` exists once per campaign and a rename in one
// of them must not touch the other.
func TestRenamePagesUnderIsScopedToOneCampaign(t *testing.T) {
	db := openTestStore(t)

	first := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)
	second := seedVisibleCampaign(t, db, "grey-hollow", domain.VisibilityPublic)

	seedPage(t, db, first, "lore/moving.md", "Moving", "the wyvern sleeps")
	seedPage(t, db, second, "lore/staying.md", "Staying", "the wyvern sleeps")

	if _, err := db.RenamePagesUnder(t.Context(), first.ID, "lore", "coast"); err != nil {
		t.Fatalf("RenamePagesUnder() error = %v, want nil", err)
	}

	assertPagePaths(t, db, first.ID, []string{"coast/moving.md"})
	assertPagePaths(t, db, second.ID, []string{"lore/staying.md"})
}

// assertPagePaths asserts a campaign's rows, in path order, against want.
//
// Compared as a set with a length check rather than probed one at a time, so a
// rename that duplicated a row or dropped one is caught by the same assertion
// that checks the moves.
func assertPagePaths(t *testing.T, db *store.Store, campaignID int64, want []string) {
	t.Helper()

	pages, err := db.PagesForCampaign(t.Context(), campaignID)
	if err != nil {
		t.Fatalf("PagesForCampaign() error = %v, want nil", err)
	}

	got := make([]string, 0, len(pages))
	for idx := range pages {
		got = append(got, pages[idx].Path)
	}

	if len(got) != len(want) {
		t.Fatalf("campaign %d holds %v, want %v", campaignID, got, want)
	}

	for idx := range want {
		if got[idx] != want[idx] {
			t.Errorf("path %d = %q, want %q", idx, got[idx], want[idx])
		}
	}
}
