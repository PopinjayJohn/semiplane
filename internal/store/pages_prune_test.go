package store_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// The prune and the bulk rebuild.
//
// Both exist for the same reason: a watcher drops events. A watch limit that was
// exhausted at startup (S-4.5), a directory a sync client moved in without a
// per-file event, a file deleted while the process was not running — each leaves
// a row behind, and a row is a search result for a page that 404s. Neither
// operation needs an event to hang off, which is the whole reason the index can
// be said to converge rather than merely to catch up.

// TestPrunePagesDeletesWhatTheWalkDidNotSee is the prune's contract, asserted in
// both directions: rows whose file is gone go, and rows whose file is there stay.
//
// The predicate is a function rather than a set of paths for a reason the store's
// doc comment states: it runs inside the write transaction on the single
// connection, so the walk that feeds it must not be holding one.
func TestPrunePagesDeletesWhatTheWalkDidNotSee(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	present := []string{"lore/here.md", "lore/there.md"}
	gone := []string{"lore/deleted.md", "top.md"}

	for _, path := range present {
		seedPage(t, db, campaign, path, "Here", "the wyvern of svartalfheim")
	}

	for _, path := range gone {
		seedPage(t, db, campaign, path, "Gone", "a vault of svartalfheim")
	}

	live := make(map[string]struct{}, len(present))
	for _, path := range present {
		live[path] = struct{}{}
	}

	pruned, err := db.PrunePages(t.Context(), campaign.ID, func(path string) bool {
		_, kept := live[path]

		return kept
	})
	if err != nil {
		t.Fatalf("PrunePages() error = %v, want nil", err)
	}

	if pruned != len(gone) {
		t.Errorf("PrunePages() deleted %d rows, want %d", pruned, len(gone))
	}

	assertPagePaths(t, db, campaign.ID, present)

	// The FTS entries went with the rows. A pruned row whose terms survived is
	// worse than an unpruned row: it is a search hit that 404s, and it cannot be
	// explained by looking at the page list.
	assertSearchMisses(t, db, store.PageSearch{Query: "vault"})

	assertSearchIndexAgrees(t, db)
}

// TestPrunePagesIsScopedToOneCampaign is the tenancy half of the prune, and the
// one whose failure is the most expensive: a predicate built from a single
// campaign's walk, passed with another campaign's id, deletes a whole tenant.
func TestPrunePagesIsScopedToOneCampaign(t *testing.T) {
	db := openTestStore(t)

	first := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)
	second := seedVisibleCampaign(t, db, "grey-hollow", domain.VisibilityPublic)

	seedPage(t, db, first, "lore/here.md", "Here", "the wyvern sleeps")
	seedPage(t, db, second, "lore/there.md", "There", "the wyvern sleeps")

	// Everything in the second campaign is absent from the first's walk, which is
	// the exact predicate a bug would pass.
	pruned, err := db.PrunePages(t.Context(), first.ID, func(path string) bool {
		return path == "lore/here.md"
	})
	if err != nil {
		t.Fatalf("PrunePages() error = %v, want nil", err)
	}

	if pruned != 0 {
		t.Errorf("PrunePages() deleted %d rows, want 0", pruned)
	}

	assertPagePaths(t, db, second.ID, []string{"lore/there.md"})
}

// TestPrunePagesRefusesNoPredicate is the guard on the one input a caller can
// forget. A nil predicate would panic on the writer goroutine, which is the one
// place a panic in this package is least recoverable: it would take the process's
// only writer down with it.
func TestPrunePagesRefusesNoPredicate(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	seedPage(t, db, campaign, "lore/here.md", "Here", "the wyvern sleeps")

	if _, err := db.PrunePages(t.Context(), campaign.ID, nil); err == nil {
		t.Error("PrunePages(nil) error = nil, want an error")
	}

	// And the row is still there, which is the property that matters: a refused
	// prune must not be a partial one.
	assertPagePaths(t, db, campaign.ID, []string{"lore/here.md"})
}

// TestRebuildPagesFTSRepairsDrift is S-11.2's operation, and the reason it is a
// rebuild rather than a sequence of row updates.
//
// `pages_fts` cannot be `ALTER`ed, so a bulk change is one statement. The drift is
// created here by writing `pages` directly, which is what a migration that
// bulk-loads and a repair command would do, and which is why the no-triggers
// invariant needs an escape hatch that does not depend on the writer having been
// disciplined.
func TestRebuildPagesFTSRepairsDrift(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	// Written behind the store's back, so the FTS index does not know about it
	// and no search can find it.
	const drift = "the drifted page of svartalfheim"

	result, execErr := db.DB().ExecContext(t.Context(),
		`INSERT INTO pages (campaign_id, path, kind, title, body_plain, content_hash,
			byte_size, created_at, updated_at) VALUES (?, ?, 'prose', ?, ?, ?, ?, 0, 0)`,
		campaign.ID, "lore/drifted.md", "Drifted", drift, contentHash(drift), len(drift))
	if execErr != nil {
		t.Fatalf("insert a drifted row directly: %v", execErr)
	}

	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read the drifted rowid: %v", err)
	}

	// Positive first: the drift is real, so a rebuild that fixed nothing would
	// otherwise be indistinguishable from one that worked.
	assertSearchMisses(t, db, store.PageSearch{Query: "drifted"})

	if rebuildErr := db.RebuildPagesFTS(t.Context()); rebuildErr != nil {
		t.Fatalf("RebuildPagesFTS() error = %v, want nil", rebuildErr)
	}

	hits, err := db.SearchPages(t.Context(), store.PageSearch{Query: "drifted"}, domain.Requestor{})
	if err != nil {
		t.Fatalf("SearchPages() after the rebuild error = %v, want nil", err)
	}

	if len(hits) != 1 {
		t.Fatalf("SearchPages() after the rebuild returned %d hits, want 1", len(hits))
	}

	if hits[0].ID != id {
		t.Errorf("hit.ID = %d, want %d", hits[0].ID, id)
	}

	assertSearchIndexAgrees(t, db)
}

// TestRebuildPagesFTSKeepsEveryCampaign is the tenancy half of the rebuild.
//
// `INSERT INTO pages_fts(pages_fts) VALUES('rebuild')` takes no campaign argument:
// it is whole-table by construction, because an external-content table cannot be
// narrowed to a subset of its own content. A test that only asserted on one
// campaign would pass for a statement that had grown a filter by accident.
func TestRebuildPagesFTSKeepsEveryCampaign(t *testing.T) {
	db := openTestStore(t)

	first := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)
	second := seedVisibleCampaign(t, db, "grey-hollow", domain.VisibilityPublic)

	seedPage(t, db, first, "lore/here.md", "Here", "the wyvern of svartalfheim")
	seedPage(t, db, second, "lore/there.md", "There", "the wyvern of svartalfheim")

	if err := db.RebuildPagesFTS(t.Context()); err != nil {
		t.Fatalf("RebuildPagesFTS() error = %v, want nil", err)
	}

	hits, err := db.SearchPages(
		t.Context(),
		store.PageSearch{Query: "svartalfheim"},
		domain.Requestor{},
	)
	if err != nil {
		t.Fatalf("SearchPages() after the rebuild error = %v, want nil", err)
	}

	if len(hits) != 2 {
		t.Fatalf("SearchPages() returned %d hits, want 2; the rebuild dropped a campaign",
			len(hits))
	}

	slugs := make([]string, 0, len(hits))
	for idx := range hits {
		slugs = append(slugs, hits[idx].CampaignSlug)
	}

	for _, want := range []string{first.Slug, second.Slug} {
		if !slices.Contains(slugs, want) {
			t.Errorf("SearchPages() did not return %q; got %v", want, slugs)
		}
	}

	assertSearchIndexAgrees(t, db)
}

// TestAPruneOfEveryRowLeavesNoSearchHit is the end state of the worst case.
//
// Every row pruned, and the index empty. It is asserted rather than assumed
// because a prune that reported the right count and left the terms behind would
// satisfy the count, the row list, and `assertSearchIndexAgrees` would not notice
// — an external-content index answers no query of its own, so the only thing that
// can detect it is a search for a word that is no longer anywhere.
func TestAPruneOfEveryRowLeavesNoSearchHit(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	seedPage(t, db, campaign, "lore/one.md", "One", "the wyvern sleeps")
	seedPage(t, db, campaign, "lore/two.md", "Two", "the vault holds a secret")

	pruned, err := db.PrunePages(t.Context(), campaign.ID, func(string) bool { return false })
	if err != nil {
		t.Fatalf("PrunePages() error = %v, want nil", err)
	}

	if pruned != 2 {
		t.Errorf("PrunePages() deleted %d rows, want 2", pruned)
	}

	assertSearchMisses(t, db, store.PageSearch{Query: "wyvern"})
	assertSearchMisses(t, db, store.PageSearch{Query: "secret"})

	assertSearchIndexAgrees(t, db)
}

// TestThePruneDoesNotDisturbAConcurrentCampaign is the isolation claim, and it is
// worth a test only because the prune's transaction is long: it holds the writer
// for every row it examines, and the claim is that a second campaign's rows are
// untouched by it rather than merely unaffected in the assertions above.
func TestThePruneDoesNotDisturbAConcurrentCampaign(t *testing.T) {
	db := openTestStore(t)

	first := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)
	second := seedVisibleCampaign(t, db, "grey-hollow", domain.VisibilityPublic)

	for idx := range 20 {
		seedPage(
			t,
			db,
			first,
			"lore/page"+string(rune('a'+idx))+".md",
			"First",
			"the wyvern sleeps",
		)
		seedPage(
			t,
			db,
			second,
			"lore/page"+string(rune('a'+idx))+".md",
			"Second",
			"the wyvern sleeps",
		)
	}

	pruned, err := db.PrunePages(t.Context(), first.ID, func(path string) bool {
		return strings.HasSuffix(path, "a.md") || strings.HasSuffix(path, "b.md")
	})
	if err != nil {
		t.Fatalf("PrunePages() error = %v, want nil", err)
	}

	if pruned != 18 {
		t.Errorf("PrunePages() deleted %d rows, want 18", pruned)
	}

	pages, err := db.PagesForCampaign(t.Context(), second.ID)
	if err != nil {
		t.Fatalf("PagesForCampaign() error = %v, want nil", err)
	}

	if len(pages) != 20 {
		t.Errorf("the other campaign holds %d rows, want 20; the prune crossed a campaign",
			len(pages))
	}

	if _, err := db.PageByPath(t.Context(), second.ID, "lore/pagea.md"); err != nil {
		t.Errorf("PageByPath() in the untouched campaign error = %v, want nil", err)
	}

	assertSearchIndexAgrees(t, db)
}
