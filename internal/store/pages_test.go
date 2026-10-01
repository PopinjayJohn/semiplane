package store_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// seedPage writes one page into a campaign and returns it as stored.
//
// The hash is a real digest of the body rather than a fixed label, so a test that
// changes the body and asserts the hash moved is testing the column rather than
// the fixture. `pages.content_hash` is what the render cache is keyed on
// (S-5.2), so a store that mangled it would cost every phase after this one a
// cache that never invalidates.
func seedPage(
	t *testing.T,
	db *store.Store,
	campaign domain.Campaign,
	path, title, body string,
) domain.Page {
	t.Helper()

	page, err := db.UpsertPage(t.Context(), domain.PageText{
		CampaignID:  campaign.ID,
		Path:        path,
		Kind:        domain.KindProse,
		Title:       title,
		ContentHash: contentHash(body),
		ByteSize:    int64(len(body)),
		BodyPlain:   body,
	})
	if err != nil {
		t.Fatalf("UpsertPage(%q) error = %v, want nil", path, err)
	}

	return page
}

// seedVisibleCampaign is seedCampaign with a stated visibility. seedCampaign fixes
// `private`, which is right for the tenancy tests and wrong here: the visibility
// join is the thing under test.
func seedVisibleCampaign(
	t *testing.T,
	db *store.Store,
	slug string,
	visibility domain.Visibility,
) domain.Campaign {
	t.Helper()

	campaign, err := db.CreateCampaign(t.Context(), domain.Campaign{
		Slug:        slug,
		Name:        slug,
		ContentRoot: t.TempDir(),
		Visibility:  visibility,
		SystemID:    "5e-2024",
	})
	if err != nil {
		t.Fatalf("CreateCampaign(%q) error = %v, want nil", slug, err)
	}

	return campaign
}

// contentHash is the digest `pages.content_hash` holds.
func contentHash(body string) string {
	sum := sha256.Sum256([]byte(body))

	return hex.EncodeToString(sum[:])
}

// assertSearchIndexAgrees runs FTS5's own consistency check.
//
// The strongest assertion available for an external-content index, and the one
// that matters because the index stores no copy of the text: 'integrity-check'
// re-reads `pages` and compares, so it fails if the writer ever updated one
// without the other. Migration 0007 records that there are no triggers, which
// makes this test the enforcement of the invariant the triggers would otherwise
// have provided.
func assertSearchIndexAgrees(t *testing.T, db *store.Store) {
	t.Helper()

	rows, err := db.DB().QueryContext(
		t.Context(),
		"INSERT INTO pages_fts (pages_fts) VALUES ('integrity-check')",
	)
	if err != nil {
		t.Fatalf("run integrity-check: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close integrity-check rows: %v", err)
		}
	}()

	var violations int

	for rows.Next() {
		violations++
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("read integrity-check rows: %v", err)
	}

	if violations != 0 {
		t.Errorf("pages_fts disagrees with pages in %d rows; the two are not written together",
			violations)
	}
}

// assertPageEqual compares the fields a page round trip has to preserve.
func assertPageEqual(t *testing.T, got, want domain.Page) {
	t.Helper()

	if got.ID != want.ID {
		t.Errorf("ID = %d, want %d", got.ID, want.ID)
	}

	if got.CampaignID != want.CampaignID {
		t.Errorf("CampaignID = %d, want %d", got.CampaignID, want.CampaignID)
	}

	if got.Path != want.Path {
		t.Errorf("Path = %q, want %q", got.Path, want.Path)
	}

	if got.Kind != want.Kind {
		t.Errorf("Kind = %q, want %q", got.Kind, want.Kind)
	}

	if got.Title != want.Title {
		t.Errorf("Title = %q, want %q", got.Title, want.Title)
	}

	if got.ContentHash != want.ContentHash {
		t.Errorf("ContentHash = %q, want %q", got.ContentHash, want.ContentHash)
	}

	if got.ByteSize != want.ByteSize {
		t.Errorf("ByteSize = %d, want %d", got.ByteSize, want.ByteSize)
	}

	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, want.CreatedAt)
	}

	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("UpdatedAt = %s, want %s", got.UpdatedAt, want.UpdatedAt)
	}
}

// TestPageRoundTrip is upsert, read back, and the hash. The row is the rebuildable
// index the whole content read path is built on, so every column it carries is
// asserted rather than the one a later phase happens to want.
func TestPageRoundTrip(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	body := "the dragon sleeps in Svartalfheim"

	created := seedPage(t, db, campaign, "lore/svartalfheim.md", "Svartalfheim", body)

	if created.ID <= 0 {
		t.Errorf("UpsertPage() returned id %d, want a positive rowid", created.ID)
	}

	if created.Kind != domain.KindProse {
		t.Errorf("Kind = %q, want %q", created.Kind, domain.KindProse)
	}

	// The stored value, not the caller's: the schema keeps seconds, and a
	// returned time with sub-second precision would compare unequal to the row it
	// came from.
	if created.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero; the writer did not stamp it")
	}

	if got, want := created.ContentHash, contentHash(body); got != want {
		t.Errorf("ContentHash = %q, want %q", got, want)
	}

	got, err := db.PageByPath(t.Context(), campaign.ID, created.Path)
	if err != nil {
		t.Fatalf("PageByPath(%q) error = %v, want nil", created.Path, err)
	}

	assertPageEqual(t, got, created)
}

// TestUpsertPageUpdatesInPlace is the second write, which is where the FTS
// bookkeeping has to be right. An external-content index cannot be updated in
// place -- dropping the old terms needs the values that produced them -- so a
// writer that got that wrong would leave the index holding text no file contains,
// and the symptom would be a search result pointing at a paragraph that was
// deleted a week ago.
func TestUpsertPageUpdatesInPlace(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	created := seedPage(t, db, campaign, "lore/goblin.md", "Goblin", "a goblin of svartalfheim")

	updated, err := db.UpsertPage(t.Context(), domain.PageText{
		ID:          created.ID,
		CampaignID:  created.CampaignID,
		Path:        created.Path,
		Kind:        domain.PageKind("token"),
		Title:       "Grinnax, Goblin Chief",
		ContentHash: contentHash("a goblin chief, nine feet of it"),
		ByteSize:    34,
		CreatedAt:   created.CreatedAt,
		BodyPlain:   "a goblin chief, nine feet of it",
	})
	if err != nil {
		t.Fatalf("UpsertPage() second write error = %v, want nil", err)
	}

	if updated.ID != created.ID {
		t.Errorf("ID = %d, want %d; the upsert inserted a second row instead of updating",
			updated.ID, created.ID)
	}

	if updated.Kind != domain.PageKind("token") {
		t.Errorf("Kind = %q, want %q", updated.Kind, domain.PageKind("token"))
	}

	// created_at is left alone by the conflict clause, and the returned value is
	// the row rather than the caller's input -- so this is the assertion that a
	// re-index does not make every page look new.
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s; an update must not restamp the row's age",
			updated.CreatedAt, created.CreatedAt)
	}

	listed, err := db.PagesForCampaign(t.Context(), campaign.ID)
	if err != nil {
		t.Fatalf("PagesForCampaign() error = %v, want nil", err)
	}

	if len(listed) != 1 {
		t.Fatalf("PagesForCampaign() returned %d pages, want 1; the upsert duplicated the row",
			len(listed))
	}

	assertPageEqual(t, listed[0], updated)

	// The new words are findable and the old ones are not.
	assertSearchFinds(t, db, store.PageSearch{Query: "chief"}, updated.Path)
	assertSearchMisses(t, db, store.PageSearch{Query: "svartalfheim"})

	assertSearchIndexAgrees(t, db)
}

// TestDeletePageRemovesItAndItsSearchEntry covers the deletion, including the
// half that is easy to forget: the index. A row deleted without its FTS entries
// leaves a page that no listing shows and no request can reach, yet that a search
// still names -- and its excerpt.
func TestDeletePageRemovesItAndItsSearchEntry(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	kept := seedPage(t, db, campaign, "lore/keep.md", "Keep", "a keeper of svartalfheim")
	removed := seedPage(t, db, campaign, "lore/remove.md", "Remove", "a secret of svartalfheim")

	if err := db.DeletePage(t.Context(), campaign.ID, removed.Path); err != nil {
		t.Fatalf("DeletePage(%q) error = %v, want nil", removed.Path, err)
	}

	if _, err := db.PageByPath(
		t.Context(),
		campaign.ID,
		removed.Path,
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Errorf("PageByPath(%q) error = %v, want ErrNotFound", removed.Path, err)
	}

	// The other page survives, so the deletion was scoped and not merely total.
	if _, err := db.PageByPath(t.Context(), campaign.ID, kept.Path); err != nil {
		t.Errorf("PageByPath(%q) error = %v, want nil", kept.Path, err)
	}

	// And the index no longer knows about the deleted page, which is the assertion
	// that the FTS delete ran with the values it needed.
	assertSearchMisses(t, db, store.PageSearch{Query: "secret"})

	assertSearchIndexAgrees(t, db)
}

// TestDeletePageReportsNotFound is the contract a watcher relies on: a delete for
// a path that was never indexed is a fact, not a fault, and it arrives as
// ErrNotFound so a caller can tell it from a write that failed.
func TestDeletePageReportsNotFound(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	err := db.DeletePage(t.Context(), campaign.ID, "lore/never-existed.md")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeletePage() error = %v, want ErrNotFound", err)
	}
}

// TestPagesForCampaignIsScopedAndOrdered is the listing's two promises: another
// campaign's pages are not in it, and the order is the path order the wiki index
// and the broken-link report both want.
func TestPagesForCampaignIsScopedAndOrdered(t *testing.T) {
	db := openTestStore(t)

	first := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)
	second := seedVisibleCampaign(t, db, "grey-hollow", domain.VisibilityPublic)

	// Inserted out of order on purpose: a listing that sorted by insertion would
	// pass every single-page test there is.
	seedPage(t, db, first, "lore/zend.md", "Zend", "z")
	seedPage(t, db, first, "lore/arden.md", "Arden", "a")
	seedPage(t, db, first, "index.md", "Index", "i")
	seedPage(t, db, second, "lore/elsewhere.md", "Elsewhere", "e")

	pages, err := db.PagesForCampaign(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("PagesForCampaign() error = %v, want nil", err)
	}

	want := []string{"index.md", "lore/arden.md", "lore/zend.md"}
	if len(pages) != len(want) {
		t.Fatalf("PagesForCampaign() returned %d pages, want %d", len(pages), len(want))
	}

	for idx, path := range want {
		if pages[idx].Path != path {
			t.Errorf("pages[%d].Path = %q, want %q", idx, pages[idx].Path, path)
		}
	}
}

// TestPagesForCampaignIsEmptyForACampaignWithNoPages is the other answer, and the
// one a handler is more likely to mishandle than a missing page.
func TestPagesForCampaignIsEmptyForACampaignWithNoPages(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "empty", domain.VisibilityPublic)

	pages, err := db.PagesForCampaign(t.Context(), campaign.ID)
	if err != nil {
		t.Fatalf("PagesForCampaign() error = %v, want nil", err)
	}

	if len(pages) != 0 {
		t.Errorf("PagesForCampaign() returned %d pages, want none", len(pages))
	}
}

// TestSearchPagesFindsBodyText is the search working at all, and it is searched
// on the *body* rather than the title, because `pages_fts` is declared over both
// and a test that only used titles would pass with the body's column dropped.
func TestSearchPagesFindsBodyText(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	page := seedPage(
		t,
		db,
		campaign,
		"lore/svartalfheim.md",
		"Cold North",
		"the dragon sleeps in Svartalfheim, and the goblins gather at the gate",
	)

	hits, err := db.SearchPages(
		t.Context(),
		store.PageSearch{Query: "svartalfheim"},
		domain.Requestor{},
	)
	if err != nil {
		t.Fatalf("SearchPages() error = %v, want nil", err)
	}

	if len(hits) != 1 {
		t.Fatalf("SearchPages() returned %d hits, want 1", len(hits))
	}

	if hits[0].Path != page.Path {
		t.Errorf("hit.Path = %q, want %q", hits[0].Path, page.Path)
	}

	if hits[0].Title != page.Title {
		t.Errorf("hit.Title = %q, want %q", hits[0].Title, page.Title)
	}

	if hits[0].CampaignSlug != campaign.Slug {
		t.Errorf("hit.CampaignSlug = %q, want %q", hits[0].CampaignSlug, campaign.Slug)
	}

	// The excerpt is the searchable text, so it carries the match and not the
	// page's whole body.
	if !strings.Contains(strings.ToLower(hits[0].Snippet), "svartalfheim") {
		t.Errorf("hit.Snippet = %q, want it to contain the matched word", hits[0].Snippet)
	}
}

// TestSearchPagesPrefixesTheLastTerm is ADR 0007's stated consequence: there is no
// stemmer, so `goblins` finds `goblin` only because the final term is a prefix.
// Without the `*` this returns nothing, and the workaround a user would reach for
// is to type the singular -- which is the behaviour the record rejected.
func TestSearchPagesPrefixesTheLastTerm(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	seedPage(t, db, campaign, "lore/goblins.md", "Goblins", "the goblins gather")

	hits, err := db.SearchPages(t.Context(), store.PageSearch{Query: "goblin"}, domain.Requestor{})
	if err != nil {
		t.Fatalf("SearchPages() error = %v, want nil", err)
	}

	if len(hits) != 1 {
		t.Errorf("SearchPages(\"goblin\") returned %d hits, want 1; the last term is not a prefix",
			len(hits))
	}
}

// TestSearchPagesHonoursVisibility is S-8.2, and it is the test ADR 0007 asks for:
// a bare query over FTS returns private titles to an anonymous user, and the
// omission is invisible in review because the query still looks like a search.
//
// Every row is written by a member of its own campaign and every one of them
// contains the same word, so a result set is only right if the join did the work.
func TestSearchPagesHonoursVisibility(t *testing.T) {
	db := openTestStore(t)

	reader := seedUser(t, db, "reader")
	outsider := seedUser(t, db, "outsider")
	admin := seedUser(t, db, "instance-admin")

	public := seedVisibleCampaign(t, db, "open-road", domain.VisibilityPublic)
	private := seedVisibleCampaign(t, db, "closed-vault", domain.VisibilityPrivate)

	seedMembership(t, db, private.ID, reader.ID, domain.RolePlayer)
	seedMembership(t, db, public.ID, outsider.ID, domain.RolePlayer)

	publicPage := seedPage(
		t,
		db,
		public,
		"lore/open.md",
		"Open Road",
		"the wyvern circles svartalfheim",
	)
	privatePage := seedPage(
		t,
		db,
		private,
		"lore/closed.md",
		"Closed Vault",
		"the wyvern circles svartalfheim",
	)

	cases := []struct {
		name      string
		requestor domain.Requestor
		wantPaths []string
	}{
		{
			name:      "anonymous sees only the public campaign",
			requestor: domain.Requestor{},
			wantPaths: []string{publicPage.Path},
		},
		{
			// S-8.1: `public` grants wiki read and nothing else, so an
			// authenticated non-member of a private campaign is in the same
			// position as an anonymous visitor and no further along.
			name: "an authenticated non-member sees only the public campaign",
			requestor: domain.Requestor{
				UserID:        outsider.ID,
				Authenticated: true,
			},
			wantPaths: []string{publicPage.Path},
		},
		{
			name: "a member of the private campaign sees both",
			requestor: domain.Requestor{
				UserID:        reader.ID,
				Authenticated: true,
			},
			wantPaths: []string{privatePage.Path, publicPage.Path},
		},
		{
			// S-2.7 and S-8: instance administration is not campaign read. An
			// administrator who could search every campaign would read every
			// campaign, which is the distinction the whole role split exists for.
			name: "an instance administrator sees only the public campaign",
			requestor: domain.Requestor{
				UserID:        admin.ID,
				Authenticated: true,
				IsAdmin:       true,
			},
			wantPaths: []string{publicPage.Path},
		},
		{
			// A requestor the middleware failed to authenticate. The membership
			// subquery asks about the id, so this one is not a member of anything
			// and gets nothing it was not already entitled to.
			name: "an unauthenticated requestor with an id sees only the public campaign",
			requestor: domain.Requestor{
				UserID: reader.ID,
			},
			wantPaths: []string{publicPage.Path},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			hits, err := db.SearchPages(
				t.Context(),
				store.PageSearch{Query: "svartalfheim"},
				testCase.requestor,
			)
			if err != nil {
				t.Fatalf("SearchPages() error = %v, want nil", err)
			}

			gotPaths := make([]string, 0, len(hits))
			for _, hit := range hits {
				gotPaths = append(gotPaths, hit.Path)
			}

			if len(gotPaths) != len(testCase.wantPaths) {
				t.Fatalf("SearchPages() returned %v, want %v", gotPaths, testCase.wantPaths)
			}

			// Both orders are ascending by path, which is the tie-break the
			// statement declares, so a set comparison is a fair one.
			for idx := range gotPaths {
				if gotPaths[idx] != testCase.wantPaths[idx] {
					t.Errorf("hit %d = %q, want %q", idx, gotPaths[idx], testCase.wantPaths[idx])
				}
			}
		})
	}
}

// TestSearchPagesScopingIsNeverOptional is the same property from the other side:
// a caller that wants one campaign's results asks for that campaign, and asking is
// what narrows the search rather than the query quietly defaulting to a
// campaign.
func TestSearchPagesScopesToOneCampaign(t *testing.T) {
	db := openTestStore(t)

	first := seedVisibleCampaign(t, db, "open-road", domain.VisibilityPublic)
	second := seedVisibleCampaign(t, db, "grey-hollow", domain.VisibilityPublic)

	seedPage(t, db, first, "lore/here.md", "Here", "the wyvern of svartalfheim")
	seedPage(t, db, second, "lore/there.md", "There", "the wyvern of svartalfheim")

	scoped, err := db.SearchPages(
		t.Context(),
		store.PageSearch{Query: "svartalfheim", CampaignID: second.ID},
		domain.Requestor{},
	)
	if err != nil {
		t.Fatalf("SearchPages() error = %v, want nil", err)
	}

	if len(scoped) != 1 {
		t.Fatalf("scoped SearchPages() returned %d hits, want 1", len(scoped))
	}

	if scoped[0].CampaignSlug != second.Slug {
		t.Errorf("hit.CampaignSlug = %q, want %q; the campaign scope was not applied",
			scoped[0].CampaignSlug, second.Slug)
	}

	cross, err := db.SearchPages(
		t.Context(),
		store.PageSearch{Query: "svartalfheim"},
		domain.Requestor{},
	)
	if err != nil {
		t.Fatalf("SearchPages() error = %v, want nil", err)
	}

	if len(cross) != 2 {
		t.Errorf("unscoped SearchPages() returned %d hits, want 2", len(cross))
	}
}

// TestSearchPagesTreatsTheQueryAsText is the reason the MATCH expression is built
// rather than passed through.
//
// `q=` is a URL parameter, so it is attacker-reachable, and FTS5's query language
// is not a search box's language: unquoted, `dragon OR secret` is an OR, `title:x`
// reaches a column, and a lone `(` is a syntax error. Quoting makes none of those
// reachable and none of them an error.
func TestSearchPagesTreatsTheQueryAsText(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	seedPage(t, db, campaign, "lore/secret.md", "Secrets", "the vault holds a secret")

	cases := []struct {
		name      string
		query     string
		wantHits  int
		wantError error
	}{
		{
			// Quoted, the three words are one phrase each and all three must match,
			// so nothing comes back. Read as FTS5 syntax they would be an OR and
			// this page would come back -- which is exactly what the assertion is
			// for: `q=` must not be able to choose the operator.
			name:     "an OR is text, not an operator",
			query:    "secret OR dragon",
			wantHits: 0,
		},
		{
			// The positive half: an ordinary multi-word query is an AND and works.
			name:     "several words are all required",
			query:    "vault secret",
			wantHits: 1,
		},
		{
			// A column filter would match the page through the title column.
			name:     "a column filter is text, not a filter",
			query:    "title:secret",
			wantHits: 0,
		},
		{
			// A syntax error if it reached the parser. Here it has nothing to be
			// one: the parenthesis is dropped with every other term that cannot
			// tokenise, and what is left is "dragon".
			name:     "an unbalanced parenthesis is not a syntax error",
			query:    "(dragon",
			wantHits: 0,
		},
		{
			name:     "a double quote in a term is not a syntax error",
			query:    `the "wyvern`,
			wantHits: 0,
		},
		{
			name:      "an empty query is refused",
			query:     "   ",
			wantError: store.ErrInvalidSearchQuery,
		},
		{
			name:      "a query with nothing searchable in it is refused",
			query:     "!!! ???",
			wantError: store.ErrInvalidSearchQuery,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			hits, err := db.SearchPages(
				t.Context(),
				store.PageSearch{Query: testCase.query},
				domain.Requestor{},
			)

			if testCase.wantError != nil {
				if !errors.Is(err, testCase.wantError) {
					t.Fatalf("SearchPages(%q) error = %v, want %v",
						testCase.query, err, testCase.wantError)
				}

				return
			}

			if err != nil {
				t.Fatalf("SearchPages(%q) error = %v, want nil", testCase.query, err)
			}

			if len(hits) != testCase.wantHits {
				t.Errorf("SearchPages(%q) returned %d hits, want %d",
					testCase.query, len(hits), testCase.wantHits)
			}
		})
	}
}

// TestSearchPagesAppliesItsLimits is the resource half. A search is a scan of the
// matching rows with an excerpt computed for each, so an unbounded limit is a
// request that asks for as much work as the index can be made to do.
func TestSearchPagesAppliesItsLimits(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	for idx := range 5 {
		seedPage(t, db, campaign, "lore/page-"+string(rune('a'+idx))+".md",
			"Page", "the wyvern of svartalfheim")
	}

	limited, err := db.SearchPages(
		t.Context(),
		store.PageSearch{Query: "svartalfheim", Limit: 2},
		domain.Requestor{},
	)
	if err != nil {
		t.Fatalf("SearchPages() error = %v, want nil", err)
	}

	if len(limited) != 2 {
		t.Errorf("SearchPages(limit 2) returned %d hits, want 2", len(limited))
	}

	// An over-large request is reduced rather than refused: a limit the caller got
	// wrong should not turn a search into an error.
	huge, err := db.SearchPages(
		t.Context(),
		store.PageSearch{Query: "svartalfheim", Limit: 1 << 30},
		domain.Requestor{},
	)
	if err != nil {
		t.Fatalf("SearchPages() error = %v, want nil", err)
	}

	if len(huge) != 5 {
		t.Errorf("SearchPages(limit 2^30) returned %d hits, want all 5", len(huge))
	}
}

// TestDeleteCampaignRebuildsTheSearchIndex is the housekeeping half of the pages
// cascade. The FTS entries outlive the rows they described -- a virtual table has
// no foreign key reaching it and there is no trigger -- and they can never be
// returned, but they would occupy the index forever. The rebuild clears them, and
// doubles as the assertion that the index still agreed with `pages`.
func TestDeleteCampaignRebuildsTheSearchIndex(t *testing.T) {
	db := openTestStore(t)

	kept := seedVisibleCampaign(t, db, "open-road", domain.VisibilityPublic)
	removed := seedVisibleCampaign(t, db, "closed-vault", domain.VisibilityPublic)

	seedPage(t, db, kept, "lore/here.md", "Here", "the wyvern of svartalfheim")
	seedPage(t, db, removed, "lore/there.md", "There", "the wyvern of svartalfheim")

	if err := db.DeleteCampaign(t.Context(), removed.ID); err != nil {
		t.Fatalf("DeleteCampaign() error = %v, want nil", err)
	}

	if _, err := db.PageByPath(
		t.Context(),
		removed.ID,
		"lore/there.md",
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Errorf("PageByPath() error = %v, want ErrNotFound after the campaign was deleted", err)
	}

	hits, err := db.SearchPages(
		t.Context(),
		store.PageSearch{Query: "svartalfheim"},
		domain.Requestor{},
	)
	if err != nil {
		t.Fatalf("SearchPages() error = %v, want nil", err)
	}

	if len(hits) != 1 {
		t.Fatalf("SearchPages() returned %d hits, want 1; the deleted campaign is still indexed",
			len(hits))
	}

	if hits[0].CampaignSlug != kept.Slug {
		t.Errorf("hit.CampaignSlug = %q, want %q", hits[0].CampaignSlug, kept.Slug)
	}

	assertSearchIndexAgrees(t, db)
}

// TestPagePathIsRefusedWhenEmpty is the one value that cannot be a path. A row at
// `(campaign_id, ”)` would be listed, indexed, and answer a request for a page
// whose path is not one.
func TestPagePathIsRefusedWhenEmpty(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	_, err := db.UpsertPage(t.Context(), domain.PageText{CampaignID: campaign.ID})
	if !errors.Is(err, store.ErrInvalidPagePath) {
		t.Errorf("UpsertPage(\"\") error = %v, want ErrInvalidPagePath", err)
	}

	if err := db.DeletePage(
		t.Context(),
		campaign.ID,
		"",
	); !errors.Is(
		err,
		store.ErrInvalidPagePath,
	) {
		t.Errorf("DeletePage(\"\") error = %v, want ErrInvalidPagePath", err)
	}

	if _, err := db.PageByPath(
		t.Context(),
		campaign.ID,
		"",
	); !errors.Is(
		err,
		store.ErrInvalidPagePath,
	) {
		t.Errorf("PageByPath(\"\") error = %v, want ErrInvalidPagePath", err)
	}
}

// TestPageByPathReportsNotFound distinguishes "gone" from "did not happen", which
// a handler cannot tell any other way.
func TestPageByPathReportsNotFound(t *testing.T) {
	db := openTestStore(t)

	campaign := seedVisibleCampaign(t, db, "ashen-coast", domain.VisibilityPublic)

	_, err := db.PageByPath(t.Context(), campaign.ID, "lore/never-written.md")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("PageByPath() error = %v, want ErrNotFound", err)
	}
}

// TestTheSamePathInTwoCampaignsIsTwoPages is the tenancy claim behind
// `(campaign_id, path)`. A vault shared between two campaigns -- a GM running the
// same world twice -- must not have one campaign's page overwrite the other's.
func TestTheSamePathInTwoCampaignsIsTwoPages(t *testing.T) {
	db := openTestStore(t)

	first := seedVisibleCampaign(t, db, "open-road", domain.VisibilityPublic)
	second := seedVisibleCampaign(t, db, "grey-hollow", domain.VisibilityPublic)

	firstPage := seedPage(t, db, first, "lore/svartalfheim.md", "First", "the first vault")
	secondPage := seedPage(t, db, second, "lore/svartalfheim.md", "Second", "the second vault")

	if firstPage.ID == secondPage.ID {
		t.Fatalf("both rows got id %d; the two campaigns are sharing a page", firstPage.ID)
	}

	for campaign, want := range map[domain.Campaign]string{first: "First", second: "Second"} {
		got, err := db.PageByPath(t.Context(), campaign.ID, "lore/svartalfheim.md")
		if err != nil {
			t.Fatalf("PageByPath() error = %v, want nil", err)
		}

		if got.Title != want {
			t.Errorf("campaign %s title = %q, want %q", campaign.Slug, got.Title, want)
		}
	}
}

// TestUpsertPageRefusesAnUnknownCampaign is the foreign key, and it is worth an
// assertion of its own because of how SQLite reports it: an immediate violation is
// raised as a trigger violation, which errors.go maps onto ErrForeignKey. A writer
// that swallowed it would index a page belonging to a campaign that does not exist,
// and that page would then be unreachable by every listing and reachable by search
// only for as long as the row survived.
func TestUpsertPageRefusesAnUnknownCampaign(t *testing.T) {
	db := openTestStore(t)

	_, err := db.UpsertPage(t.Context(), domain.PageText{
		CampaignID: 9999,
		Path:       "lore/orphan.md",
		Kind:       domain.KindProse,
		Title:      "Orphan",
		BodyPlain:  "no campaign to belong to",
	})
	if !errors.Is(err, store.ErrForeignKey) {
		t.Errorf("UpsertPage() error = %v, want ErrForeignKey", err)
	}
}

// assertSearchFinds asserts that a query returns a page, and which one.
func assertSearchFinds(t *testing.T, db *store.Store, search store.PageSearch, path string) {
	t.Helper()

	hits, err := db.SearchPages(t.Context(), search, domain.Requestor{})
	if err != nil {
		t.Fatalf("SearchPages(%q) error = %v, want nil", search.Query, err)
	}

	// By index rather than by value: a SearchHit embeds a Page and copying one per
	// iteration to read a string out of it is 168 bytes of copying per result.
	for idx := range hits {
		if hits[idx].Path == path {
			return
		}
	}

	t.Errorf("SearchPages(%q) returned %d hits, none of them %q", search.Query, len(hits), path)
}

// assertSearchMisses asserts that a query returns nothing.
func assertSearchMisses(t *testing.T, db *store.Store, search store.PageSearch) {
	t.Helper()

	hits, err := db.SearchPages(t.Context(), search, domain.Requestor{})
	if err != nil {
		t.Fatalf("SearchPages(%q) error = %v, want nil", search.Query, err)
	}

	if len(hits) != 0 {
		t.Errorf("SearchPages(%q) returned %d hits, want none", search.Query, len(hits))
	}
}
