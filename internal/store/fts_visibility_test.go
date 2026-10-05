package store_test

// FTS visibility: what the search index is allowed to hold, and where it is allowed
// to hold it.
//
// The claim has two halves and they are deliberately asymmetric, which is why one
// file asserts both:
//
//   - **`body_plain` excludes `[!secret]` content in every reveal state.** ADR 0031
//     and S-5.11. The words inside a callout are ordinary words, so this is a
//     subtraction rather than a filter on a magic token, and it holds for the
//     revealed marker as well as the collapsed one: a `-` becomes a `+` when a GM
//     reveals a secret, and an indexer that filtered on the fold marker would index
//     the text the moment the secret was revealed.
//   - **`pages.title` is not redacted at all.** ADR 0036. A title is one line of
//     front matter with no callout structure and therefore no boundary to redact to,
//     and a rule that stripped titles would have to guess which words are secret —
//     a guess that fires breaks a real title while a guess that misses leaks. So a
//     title holding `hunter2` is indexed verbatim, in every reveal state, and the
//     measurement is in `AGENTS.md`.
//
// What is new here is **where** the exclusion is asserted. `internal/content`'s
// `TestBodyPlainExcludesSecretCalloutsInEveryRevealState` asserts it about the
// column and about `SearchPages`, and this file adds the door neither of them opens:
// `pages_fts` is an external-content FTS5 table, so it can be queried **directly**,
// with no join to `pages`, no campaign scope and no visibility predicate. That is not
// a hypothetical — it is a table in the same database the process holds open, and it
// is where an external-content index keeps a *derived* structure that no column
// governs. If the secret reached the index rather than only the column, a query
// through `SearchPages` would still hide it and this file is what would not.
//
// The second half of the file is the maintenance discipline, because that is the
// only way the derived structure can come to hold a term no row contains. FTS5
// cannot be told to forget a document's terms without being handed the values that
// produced them, so `reindexPage` reads the old values, deletes with them and
// indexes the new ones. A writer that handed the delete command anything other than
// what it indexed leaves the old terms behind — invisible to `SearchPages`, because
// the row is gone and the join finds nothing, and visible to anything that queries
// the index on its own. So `UpsertPage`, `RenamePage`, `RenamePagesUnder`,
// `RebuildPagesFTS` and `DeletePage` are each driven here and the raw index is
// re-queried after every one.
//
// **This file opens a store**, so it is sequential: `store.Open` claims the
// process's single-instance slot by design (ADR 0004) and two Opens in one process
// are an error rather than a second connection.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// The three markers, and each is a string that appears in exactly one page and
// nowhere else.
//
// A word like "secret" would not do: it is the sort of word that appears in
// ordinary prose *about* secrets, and a test that failed on it would be a test about
// its own fixture.
const (
	// ftsHidden is inside a collapsed callout.
	ftsHidden = "ZEBRAFISH-TALLY-7b31"
	// ftsShown is inside a revealed callout, which is the case a filter on the fold
	// marker would get wrong.
	ftsShown = "OBSIDIAN-SIGIL-9a72"
	// ftsTitleSecret is in the title, where ADR 0036 says redaction must not reach.
	ftsTitleSecret = "hunter2"
	// ftsOrdinary is prose outside every callout, and is what makes every absence
	// below a subtraction rather than an index that holds nothing.
	ftsOrdinary = "wyvern"
)

// vaultPage is the page under test: a title holding a secret, both reveal states of
// a callout, and ordinary prose on both sides of them.
const vaultPage = "---\n" +
	"title: The passphrase is " + ftsTitleSecret + "\n" +
	"---\n" +
	"\n" +
	"The vault door is iron, and the wyvern sleeps above it.\n" +
	"\n" +
	"> [!secret]- Hidden\n" +
	"> The combination is " + ftsHidden + ".\n" +
	"> It is written on the back of the wyvern's scale.\n" +
	"\n" +
	"> [!secret]+ Revealed\n" +
	"> The token is " + ftsShown + ".\n" +
	"\n" +
	"The tide came in and the door held.\n"

// ftsFixture is one campaign with a content root, a real store, and an Indexer over
// the two.
//
// A real store and a real `content.Registry` rather than fakes, because almost every
// property worth asserting here is a property of the *pair*: a row and the FTS
// entries that shadow it, a title and the callout that must not have contributed to
// it. A fake would let all of them pass and would assert nothing. It also means the
// redaction is the production derivation rather than a hand-written fixture, which is
// what makes the exclusion a claim about this repository instead of about the test.
type ftsFixture struct {
	t        *testing.T
	slug     string
	campaign domain.Campaign
	dir      string
	roots    *content.Registry
	db       *store.Store
	index    *content.Indexer
}

// newFTSFixture registers a public campaign over a fresh temporary directory and
// indexes the one page under test.
//
// Public, and the visibility is not incidental: a private campaign's rows are
// filtered out of `SearchPages` for a non-member, so a test using one could not tell
// a redaction that works from a predicate that hides. The tenancy half of the claim
// is asserted separately and deliberately.
func newFTSFixture(t *testing.T, slug string, visibility domain.Visibility) *ftsFixture {
	t.Helper()

	dir := filepath.Join(t.TempDir(), slug)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create the content root: %v", err)
	}

	db := openTestStore(t)

	roots := content.NewRegistry(content.RefuseSymlinks)

	if _, err := roots.Open(slug, dir); err != nil {
		t.Fatalf("open the content root: %v", err)
	}

	t.Cleanup(func() {
		if err := roots.Close(); err != nil {
			t.Errorf("close the content roots: %v", err)
		}
	})

	campaign, err := db.CreateCampaign(t.Context(), domain.Campaign{
		Slug:           slug,
		Name:           slug,
		ContentRoot:    dir,
		Visibility:     visibility,
		SystemID:       "5e-2024",
		RulesetVersion: "1",
	})
	if err != nil {
		t.Fatalf("CreateCampaign() error = %v, want nil", err)
	}

	fixture := &ftsFixture{
		t:        t,
		slug:     slug,
		campaign: campaign,
		dir:      dir,
		roots:    roots,
		db:       db,
		index:    content.NewIndexer(roots, db, nil, nil),
	}

	fixture.writePage("lore/vault.md", vaultPage)

	return fixture
}

// writePage puts a page in the tree and applies the change the watcher would report.
func (f *ftsFixture) writePage(rel, body string) {
	f.t.Helper()

	full := filepath.Join(f.dir, filepath.FromSlash(rel))

	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		f.t.Fatalf("create the directory for %s: %v", rel, err)
	}

	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		f.t.Fatalf("write %s: %v", rel, err)
	}

	// `ApplyChange` rather than `HandleChange`: a `ChangeSink` has nowhere to return
	// an error to, and a fixture that could not hear one would report success for a
	// write that did not happen.
	if err := f.index.ApplyChange(f.t.Context(), content.Change{
		CampaignID: f.campaign.ID,
		Slug:       f.slug,
		Op:         content.OpUpsert,
		Path:       rel,
	}); err != nil {
		f.t.Fatalf("ApplyChange(%s) error = %v, want nil", rel, err)
	}
}

// indexed reads one page's stored `body_plain` and `title`.
//
// Read through the database rather than through a method on the Indexer, because the
// assertion is about what is *stored*: a helper returning the value the indexer had
// computed would test the computation twice and the column never.
func (f *ftsFixture) indexed(rel string) (bodyPlain, title string) {
	f.t.Helper()

	err := f.db.DB().QueryRowContext(f.t.Context(),
		"SELECT body_plain, title FROM pages WHERE campaign_id = ? AND path = ?",
		f.campaign.ID, rel).Scan(&bodyPlain, &title)
	if err != nil {
		f.t.Fatalf("read the indexed row for %s: %v", rel, err)
	}

	return bodyPlain, title
}

// search runs `SearchPages` scoped to this campaign, as an anonymous reader.
//
// Anonymous on purpose, and that is a narrowing rather than a convenience: a private
// campaign's rows are filtered out for a non-member, so a search made as a *member*
// could report a redaction working when the predicate was hiding it. This fixture's
// campaign is public for the same reason — a filter that hides the page makes every
// absence below vacuous.
//
// Scoped to one campaign because the cross-campaign shape is asserted separately: an
// unscoped search returning nothing for a secret could be explained by the scope
// rather than by the redaction.
func (f *ftsFixture) search(query string) []domain.SearchHit {
	f.t.Helper()

	hits, err := f.db.SearchPages(
		f.t.Context(),
		store.PageSearch{Query: query, CampaignID: f.campaign.ID},
		domain.Requestor{},
	)
	if err != nil {
		f.t.Fatalf("SearchPages(%q) error = %v, want nil", query, err)
	}

	return hits
}

// rawIndexMatches counts the rows `pages_fts` matches **on its own**.
//
// The door. No join to `pages`, no campaign scope, no visibility predicate, and no
// caller's query builder — this is the derived structure itself, queried the way
// anything holding a database handle could query it. Everything else in this file
// goes through a statement that has been narrowed on purpose, so an exclusion
// asserted only there is an exclusion from *this project's* search box rather than
// from the index.
func (f *ftsFixture) rawIndexMatches(t *testing.T, match string) int {
	t.Helper()

	var count int

	err := f.db.DB().QueryRowContext(
		t.Context(),
		"SELECT count(*) FROM pages_fts WHERE pages_fts MATCH ?", match,
	).Scan(&count)
	if err != nil {
		t.Fatalf("query pages_fts directly for %q: %v", match, err)
	}

	return count
}

// assertIndexHoldsNothing requires that neither the column nor the raw index carries
// any of the given strings.
//
// Both, and together: the column is what `SearchPages` joins against and the index
// is what it matches, and an exclusion that reached only one of them would leave the
// other as a door.
func (f *ftsFixture) assertIndexHoldsNothing(t *testing.T, rel string, wantAbsent ...string) {
	t.Helper()

	bodyPlain, _ := f.indexed(rel)

	for _, marker := range wantAbsent {
		if strings.Contains(bodyPlain, marker) {
			t.Errorf("body_plain carries %q:\n%s", marker, bodyPlain)
		}

		if got := f.rawIndexMatches(t, `"`+marker+`"`); got != 0 {
			t.Errorf("pages_fts matches %q on its own, in %d row(s): the derived "+
				"structure holds a term no row contains", marker, got)
		}

		if hits := f.search(marker); len(hits) != 0 {
			t.Errorf("SearchPages(%q) returned %d hit(s) with snippet %q",
				marker, len(hits), hits[0].Snippet)
		}
	}
}

// TestTheSearchIndexHoldsNoSecretInAnyRevealState is the claim, asserted over the
// column, the raw index and the API — and the positive is first in effect, because
// an index that stored nothing would satisfy every absence below.
func TestTheSearchIndexHoldsNoSecretInAnyRevealState(t *testing.T) {
	f := newFTSFixture(t, "ashen-coast", domain.VisibilityPublic)

	bodyPlain, _ := f.indexed("lore/vault.md")

	// The positive, first: the page's own words are indexed, on both sides of the
	// callouts, so every exclusion below is a subtraction.
	for _, word := range []string{"wyvern", "tide", "iron", "door"} {
		if !strings.Contains(bodyPlain, word) {
			t.Fatalf("body_plain does not contain %q, so the exclusions below would be "+
				"an index that stores nothing:\n%s", word, bodyPlain)
		}
	}

	if got := f.rawIndexMatches(t, `"wyvern"`); got == 0 {
		t.Fatalf("pages_fts matches nothing for the page's own word; the index is empty "+
			"and every exclusion below is vacuous:\n%s", bodyPlain)
	}

	// The markers themselves, in both states.
	f.assertIndexHoldsNothing(t, "lore/vault.md", ftsHidden, ftsShown)

	// The prose that appears **only** inside a callout, which is the sharper
	// assertion: "combination", "scale" and "token" are ordinary words, so a filter
	// that looked for a secret-shaped token rather than for the callout's boundary
	// would index them and pass every marker assertion above.
	f.assertIndexHoldsNothing(t, "lore/vault.md", "combination", "scale", "token")

	// The callout marker itself did not survive into the index, asserted as the
	// marker's own spelling rather than as an absence of the word "secret" — a GM
	// writes "secret" in ordinary prose and its presence would be correct.
	if strings.Contains(bodyPlain, "[!") {
		t.Errorf("body_plain still carries a callout marker:\n%s", bodyPlain)
	}

	// The snippet is the only place the text reaches a reader, and it is built from
	// the column, so it is where a column that excluded the secret correctly while a
	// snippet returned it anyway would be caught.
	hits := f.search(ftsOrdinary)
	if len(hits) != 1 {
		t.Fatalf("search for the page's own word returned %d hits, want 1", len(hits))
	}

	for _, marker := range []string{ftsHidden, ftsShown} {
		if strings.Contains(hits[0].Snippet, marker) {
			t.Errorf("the snippet carries %q:\n%s", marker, hits[0].Snippet)
		}
	}

	// The page is still findable, which is the property that makes the exclusion a
	// usable index rather than a silently missing page.
	if len(f.search("tide")) != 1 {
		t.Error("the page is not findable by its ordinary prose; the exclusion took too much")
	}

	// And the index and the column agree, which is the invariant the absence of
	// triggers leaves to a writer.
	assertSearchIndexAgrees(t, f.db)
}

// TestTheSearchIndexHoldsNoSecretAcrossCampaigns is the same exclusion with the
// campaign scope removed.
//
// Scoped to one campaign, "no hit for the secret" and "no row for the secret in the
// index" are two different claims and only the first is guaranteed by a scope. The
// cross-campaign search is the one a reader runs from the search box with nothing
// selected, so it is the one where a term left in the index would surface.
func TestTheSearchIndexHoldsNoSecretAcrossCampaigns(t *testing.T) {
	first := newFTSFixture(t, "ashen-coast", domain.VisibilityPublic)

	// A second campaign in the same database, holding the same title and the same
	// ordinary prose, so the index carries two copies of them and a match that came
	// back would be unambiguous.
	//
	// Created through the store rather than through the fixture because the fixture's
	// slug is also its content root's directory name, and a second campaign would
	// need a second tree for a claim that is about rows and terms.
	other := seedVisibleCampaign(t, first.db, "greyhaven", domain.VisibilityPublic)

	seedPage(t, first.db, other, "lore/vault.md",
		"The passphrase is "+ftsTitleSecret, ftsOrdinary+" sleeps above the door")

	for _, marker := range []string{ftsHidden, ftsShown, "combination", "scale"} {
		hits, searchErr := first.db.SearchPages(
			first.t.Context(),
			store.PageSearch{Query: marker},
			domain.Requestor{},
		)
		if searchErr != nil {
			t.Fatalf("SearchPages(%q) error = %v, want nil", marker, searchErr)
		}

		if len(hits) != 0 {
			t.Errorf("an unscoped search for %q returned %d hit(s), first snippet %q",
				marker, len(hits), hits[0].Snippet)
		}
	}

	// The positive for the unscoped form, over both campaigns: the ordinary word is
	// reachable and comes back twice, once per campaign.
	hits, err := first.db.SearchPages(
		t.Context(),
		store.PageSearch{Query: ftsOrdinary},
		domain.Requestor{},
	)
	if err != nil {
		t.Fatalf("SearchPages() error = %v, want nil", err)
	}

	if len(hits) != 2 {
		t.Errorf("an unscoped search for %q returned %d hits, want 2", ftsOrdinary, len(hits))
	}

	// The title asymmetry, at the same door. ADR 0036 says a title is not redacted,
	// and the measurement in AGENTS.md is that a title naming a passphrase lands in
	// `pages.title` in every reveal state — so this asserts the accepted cost rather
	// than the desired behaviour, and the reason it is stated is that it is the one
	// place in this file where the answer is "yes, it is searchable".
	scoped, err := first.db.SearchPages(t.Context(), store.PageSearch{
		Query:      ftsTitleSecret,
		CampaignID: first.campaign.ID,
	}, domain.Requestor{})
	if err != nil {
		t.Fatalf("SearchPages(%q) error = %v, want nil", ftsTitleSecret, err)
	}

	if len(scoped) != 1 {
		t.Errorf("a scoped search for the title's secret word returned %d hits, want 1: "+
			"ADR 0036 does not redact titles, so this is the accepted cost and its "+
			"absence would mean something else had changed", len(scoped))
	}

	_, storedTitle := first.indexed("lore/vault.md")
	if storedTitle != "The passphrase is "+ftsTitleSecret {
		t.Errorf("pages.title = %q, want it verbatim: a title has no boundary to "+
			"redact to and S-5.11 scopes redaction to body_plain", storedTitle)
	}

	assertSearchIndexAgrees(t, first.db)
}

// TestNoIndexMaintenancePathResurrectsTheSecret is the half of the claim about the
// *derived* structure, and it is the half no query through the API can see.
//
// FTS5 cannot be told to forget a document's terms without being handed the values
// that produced them, so every writer in `pages.go` reads the old row, hands it to
// the delete command, writes the new row and hands the new values to the insert. A
// writer that deleted with one pair of values and indexed with another leaves the
// first pair's terms in the index forever: `SearchPages` will never surface them,
// because the join finds no row, and a query against the index on its own will.
//
// So each maintenance operation is driven in turn and the raw index is re-queried
// after every one. The page is written back unchanged between steps where a step
// needs the row to be present, which is what a watcher re-index does.
func TestNoIndexMaintenancePathResurrectsTheSecret(t *testing.T) {
	f := newFTSFixture(t, "ashen-coast", domain.VisibilityPublic)

	const path = "lore/vault.md"

	// The terms that must never come back: the markers and the callout-only prose.
	absent := []string{ftsHidden, ftsShown, "combination", "scale", "token"}

	page, err := f.db.PageByPath(t.Context(), f.campaign.ID, path)
	if err != nil {
		t.Fatalf("read the indexed page: %v", err)
	}

	// A `PageText` carrying what the column already holds, which is what every one
	// of these operations re-indexes from.
	redacted := domain.PageText{
		CampaignID:  f.campaign.ID,
		Path:        path,
		Kind:        domain.KindProse,
		Title:       page.Title,
		ContentHash: page.ContentHash,
		ByteSize:    page.ByteSize,
		BodyPlain:   firstBodyPlain(t, f, path),
	}

	steps := []struct {
		name string
		run  func()
	}{
		{
			// The watcher re-indexing an unchanged page, and the case a stale
			// delete/insert pair would break: the same values go in as went out.
			name: "an upsert of the same values",
			run: func() {
				if _, err := f.db.UpsertPage(t.Context(), redacted); err != nil {
					t.Fatalf("UpsertPage() error = %v, want nil", err)
				}
			},
		},
		{
			// A page moved within its campaign. `RenamePage` is a re-key that still
			// runs the unindex/index pair, so it is the second path through the index.
			name: "a rename",
			run: func() {
				if err := f.db.RenamePage(
					t.Context(), f.campaign.ID, path, "lore/vault-moved.md",
				); err != nil {
					t.Fatalf("RenamePage() error = %v, want nil", err)
				}
			},
		},
		{
			// A folder move, which is a sync client's shape and re-keys every row
			// under it.
			name: "a subtree rename",
			run: func() {
				if _, err := f.db.RenamePagesUnder(
					t.Context(), f.campaign.ID, "lore", "chronicle",
				); err != nil {
					t.Fatalf("RenamePagesUnder() error = %v, want nil", err)
				}
			},
		},
		{
			// A full rebuild, which re-derives the index from the column. It is the
			// one operation that cannot pass stale values to a delete command, so it
			// is the one that proves the *column* is clean rather than the writer
			// being careful.
			name: "a full index rebuild",
			run: func() {
				if err := f.db.RebuildPagesFTS(t.Context()); err != nil {
					t.Fatalf("RebuildPagesFTS() error = %v, want nil", err)
				}
			},
		},
		{
			// A remove and a re-add, which is what a page deleted and recreated by a
			// sync client looks like.
			name: "a delete and a re-add",
			run: func() {
				if err := f.db.DeletePage(
					t.Context(),
					f.campaign.ID,
					"chronicle/vault-moved.md",
				); err != nil {
					t.Fatalf("DeletePage() error = %v, want nil", err)
				}

				redacted.Path = "chronicle/vault-moved.md"

				if _, err := f.db.UpsertPage(t.Context(), redacted); err != nil {
					t.Fatalf("UpsertPage() after a delete error = %v, want nil", err)
				}
			},
		},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			step.run()

			for _, marker := range absent {
				if got := f.rawIndexMatches(t, `"`+marker+`"`); got != 0 {
					t.Errorf("after %s, pages_fts matches %q on its own, in %d row(s): "+
						"the derived structure holds a term no row contains",
						step.name, marker, got)
				}
			}

			// The page is still there and still findable, so a step that emptied the
			// index cannot pass the assertions above.
			if got := f.rawIndexMatches(t, `"`+ftsOrdinary+`"`); got == 0 {
				t.Errorf("after %s, pages_fts matches nothing for the page's own word: "+
					"the index was emptied rather than maintained", step.name)
			}

			assertSearchIndexAgrees(t, f.db)
		})
	}

	// And after everything, the exported API agrees with the raw index: a term in
	// the index is not reachable and a page is.
	if hits := f.search(ftsHidden); len(hits) != 0 {
		t.Errorf("SearchPages(%q) returned %d hits after every maintenance path ran",
			ftsHidden, len(hits))
	}
}

// firstBodyPlain reads the column a maintenance path will re-index from.
func firstBodyPlain(t *testing.T, f *ftsFixture, rel string) string {
	t.Helper()

	bodyPlain, _ := f.indexed(rel)

	return bodyPlain
}
