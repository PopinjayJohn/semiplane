package content_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// The maintained index, driven through a real store.
//
// A real `*store.Store` and a real `content.Registry` rather than fakes, because
// almost every property worth asserting here is a property of the *pair*: a row
// and the FTS entries that shadow it, a hash and the bytes it was computed from,
// a title and the callout that must not have contributed to it. A fake store would
// let all of those pass and would assert nothing.
//
// It costs one process-wide Store, which is fine because `store.Open` is
// single-instance and Go runs one package's tests in one process at a time.

// A secret marker that appears in exactly one page and nowhere else.
//
// Greppable, and its whole job is to fail loudly if it turns up in a column or a
// search result. A word like "secret" would not do: it is the sort of word that
// appears in ordinary prose about secrets, and a test that failed on it would be a
// test about the fixture.
const (
	hiddenSecret = "EMBERGLASS-PASSPHRASE-4c1f"
	shownSecret  = "OBSIDIAN-SIGIL-9a72"
	ordinaryWord = "wyvern"
)

// One store for the whole package.
//
// `store.Open` claims the process's single-instance slot by design (ADR 0004), so
// a fixture that opened its own could only ever be one per process and every
// second test in this package would fail with "a Store is already open". Sharing
// one store and separating the tests by *campaign* — every assertion here is
// scoped to `f.campaign.ID` — is what makes them independent without fighting
// that guarantee.
//
// The tests are therefore sequential rather than `t.Parallel`, which is also what
// sharing a SQLite file requires. It costs wall-clock and buys a claim worth
// making: the single-instance rule is load-bearing enough that a test suite
// quietly routing around it would be testing a configuration semiplane does not
// support.
var sharedStore *store.Store

func TestMain(m *testing.M) {
	code := func() int {
		dir, err := os.MkdirTemp("", "semiplane-index-test")
		if err != nil {
			panic("create the test database directory: " + err.Error())
		}

		defer func() {
			if removeErr := os.RemoveAll(dir); removeErr != nil {
				panic("remove the test database directory: " + removeErr.Error())
			}
		}()

		db, err := store.Open(context.Background(), "file:"+filepath.Join(dir, "semiplane.db"))
		if err != nil {
			panic("open the test store: " + err.Error())
		}

		defer func() {
			if err := db.Close(); err != nil {
				panic("close the test store: " + err.Error())
			}
		}()

		sharedStore = db

		return m.Run()
	}()

	os.Exit(code)
}

// indexFixture is one campaign with a content root, the shared store, and an
// Indexer over the two.
type indexFixture struct {
	t        *testing.T
	slug     string
	campaign domain.Campaign
	dir      string
	roots    *content.Registry
	store    *store.Store
	index    *content.Indexer
}

// newCampaignFixture registers a campaign whose content root is a fresh
// temporary directory.
//
// The slug carries a counter because two campaigns in one database must not share
// a slug, and two tests in one process must not share a content root either.
func newCampaignFixture(t *testing.T, slug string, visibility domain.Visibility) *indexFixture {
	t.Helper()

	slug = fmt.Sprintf("%s-%d", slug, campaignCounter.Add(1))
	dir := filepath.Join(t.TempDir(), slug)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create the content root: %v", err)
	}

	roots := content.NewRegistry(content.RefuseSymlinks)
	if _, err := roots.Open(slug, dir); err != nil {
		t.Fatalf("open the content root: %v", err)
	}

	t.Cleanup(func() {
		if err := roots.Close(); err != nil {
			t.Errorf("close the content roots: %v", err)
		}
	})

	campaign, err := sharedStore.CreateCampaign(t.Context(), domain.Campaign{
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

	return &indexFixture{
		t:        t,
		slug:     slug,
		campaign: campaign,
		dir:      dir,
		roots:    roots,
		store:    sharedStore,
		index:    content.NewIndexer(roots, sharedStore, nil),
	}
}

// campaignCounter keeps every fixture's slug unique within one process.
var campaignCounter atomic.Int64

// write puts a page in the tree and reports the change the watcher would.
func (f *indexFixture) write(rel, body string) content.Change {
	f.t.Helper()

	full := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		f.t.Fatalf("create the directory for %s: %v", rel, err)
	}

	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		f.t.Fatalf("write %s: %v", rel, err)
	}

	return content.Change{
		CampaignID: f.campaign.ID,
		Slug:       f.slug,
		Op:         content.OpUpsert,
		Path:       rel,
	}
}

// remove deletes a page from the tree and reports the change.
func (f *indexFixture) remove(rel string) content.Change {
	f.t.Helper()

	if err := os.Remove(filepath.Join(f.dir, filepath.FromSlash(rel))); err != nil {
		f.t.Fatalf("remove %s: %v", rel, err)
	}

	return content.Change{
		CampaignID: f.campaign.ID,
		Slug:       f.slug,
		Op:         content.OpRemove,
		Path:       rel,
	}
}

// move renames a file in the tree, creating the destination's directory the way a
// sync client would, and reports no change.
//
// Split from `rename` because a test needs the filesystem move and the *event*
// separately: a move reported as a remove plus a rename has already happened on
// disk, and a fixture that conflated the two could not express that sequence at
// all.
func (f *indexFixture) move(oldRel, newRel string) {
	f.t.Helper()

	full := filepath.Join(f.dir, filepath.FromSlash(newRel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		f.t.Fatalf("create the directory for %s: %v", newRel, err)
	}

	if err := os.Rename(
		filepath.Join(f.dir, filepath.FromSlash(oldRel)),
		full,
	); err != nil {
		f.t.Fatalf("rename %s to %s: %v", oldRel, newRel, err)
	}
}

// rename moves a file in the tree and reports the change.
func (f *indexFixture) rename(oldRel, newRel string) content.Change {
	f.t.Helper()

	f.move(oldRel, newRel)

	return content.Change{
		CampaignID: f.campaign.ID,
		Slug:       f.slug,
		Op:         content.OpRename,
		Path:       newRel,
		OldPath:    oldRel,
	}
}

// applyOK applies a change and fails the test if it could not be applied.
//
// It calls ApplyChange rather than HandleChange, because a `ChangeSink` has nowhere
// to return an error to and this fixture would rather fail loudly than silently.
func (f *indexFixture) applyOK(change content.Change) {
	f.t.Helper()

	if err := f.index.ApplyChange(f.t.Context(), change); err != nil {
		f.t.Fatalf("ApplyChange(%s) error = %v, want nil", change, err)
	}
}

// paths lists a campaign's indexed paths, in the order the store returns them.
func (f *indexFixture) paths() []string {
	f.t.Helper()

	pages, err := f.store.PagesForCampaign(f.t.Context(), f.campaign.ID)
	if err != nil {
		f.t.Fatalf("PagesForCampaign() error = %v, want nil", err)
	}

	paths := make([]string, 0, len(pages))
	for idx := range pages {
		paths = append(paths, pages[idx].Path)
	}

	return paths
}

// assertPaths asserts a campaign's indexed paths against want.
func (f *indexFixture) assertPaths(want ...string) {
	f.t.Helper()

	got := f.paths()

	if len(got) != len(want) {
		f.t.Fatalf("indexed paths = %v, want %v", got, want)
	}

	for idx := range want {
		if got[idx] != want[idx] {
			f.t.Errorf("path %d = %q, want %q", idx, got[idx], want[idx])
		}
	}
}

// search runs a search as a requestor, and is how every assertion about what is
// *reachable* is made. `body_plain` is what a snippet is built from, so a search
// is the only thing that proves the column is not carrying a word.
func (f *indexFixture) search(query string, req domain.Requestor) []domain.SearchHit {
	f.t.Helper()

	hits, err := f.store.SearchPages(
		f.t.Context(),
		store.PageSearch{Query: query, CampaignID: f.campaign.ID},
		req,
	)
	if err != nil {
		f.t.Fatalf("SearchPages(%q) error = %v, want nil", query, err)
	}

	return hits
}

// bodyOf reads one page's `body_plain`, which is the column under test.
//
// Read through the database rather than through a method on the Indexer, because
// the assertion is about what is *stored*: a helper that returned the value the
// indexer had computed would test the computation twice and the column never.
func (f *indexFixture) bodyOf(rel string) string {
	f.t.Helper()

	var body string

	err := f.store.DB().QueryRowContext(f.t.Context(),
		"SELECT body_plain FROM pages WHERE campaign_id = ? AND path = ?",
		f.campaign.ID, rel).Scan(&body)
	if err != nil {
		f.t.Fatalf("read body_plain for %s: %v", rel, err)
	}

	return body
}

// TestAnUpsertIndexesThePageAndItsWords is the base case, and the positive half
// every exclusion test below depends on: an ordinary page is indexed, its title
// and kind are read from its front matter, and its prose is findable.
func TestAnUpsertIndexesThePageAndItsWords(t *testing.T) {
	f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

	body := "---\ntitle: The Coast\nkind: lore\n---\n\nThe wyvern sleeps in Svartalfheim.\n"

	f.applyOK(f.write("lore/coast.md", body))

	page, err := f.store.PageByPath(t.Context(), f.campaign.ID, "lore/coast.md")
	if err != nil {
		t.Fatalf("PageByPath() error = %v, want nil", err)
	}

	if page.Title != "The Coast" {
		t.Errorf("Title = %q, want %q", page.Title, "The Coast")
	}

	// `prose`, because the fixture's registry is empty — the same answer P8's
	// empty registry gives, and the same one an unregistered kind degrades to
	// (S-3.3). A page that declared a kind this build does not know is indexed as
	// prose rather than rejected.
	if page.Kind != domain.KindProse {
		t.Errorf("Kind = %q, want %q", page.Kind, domain.KindProse)
	}

	hits := f.search("svartalfheim", domain.Requestor{})
	if len(hits) != 1 {
		t.Fatalf("search for the page's own word returned %d hits, want 1", len(hits))
	}

	// The hash is the digest of the file's whole bytes, front matter included,
	// because the render cache is keyed on the file and not on the prose (S-5.2).
	if page.ContentHash == "" || page.ByteSize != int64(len(body)) {
		t.Errorf("ContentHash = %q, ByteSize = %d; want a digest of %d bytes",
			page.ContentHash, page.ByteSize, len(body))
	}
}

// TestBodyPlainExcludesSecretCalloutsInEveryRevealState is S-5.11 and the schema's
// own promise, asserted as an absence over every surface the text could travel.
//
// The two states are the point. S-5.8 says the *file* records whether a secret is
// revealed and `secrets_revealed` records who revealed it, so a `-` becomes a `+`
// when a GM reveals a secret and the index is rewritten — and an indexer that
// filtered on the fold marker would index the text the moment the secret was
// revealed. Both markers are excluded here, so an indexer that got it wrong fails
// on the `-` case rather than on the `+`.
//
// The positive is in the same test on purpose: a page that indexes nothing
// satisfies every exclusion assertion in it, so the ordinary prose is asserted
// first and its presence is what makes the absence mean something.
//
// It is asserted over the column, over a search hit, and over the *snippet*, for
// the reason `domain.SearchHit` documents: a snippet is what a reader is shown,
// and a column that excluded the secret correctly while a search returned it
// anyway would be a leak through a different door.
func TestBodyPlainExcludesSecretCalloutsInEveryRevealState(t *testing.T) {
	f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

	body := strings.Join([]string{
		"---",
		"title: The Vault",
		"---",
		"",
		"The vault door is iron, and the wyvern sleeps above it.",
		"",
		"> [!secret]- Hidden",
		"> The combination is " + hiddenSecret + ".",
		"> It is written on the back of the wyvern's scale.",
		"",
		"> [!secret]+ Revealed",
		"> The token is " + shownSecret + ".",
		"",
		"The tide came in and the door held.",
		"",
	}, "\n")

	f.applyOK(f.write("lore/vault.md", body))

	indexed := f.bodyOf("lore/vault.md")

	for _, marker := range []string{hiddenSecret, shownSecret} {
		if strings.Contains(indexed, marker) {
			t.Errorf("body_plain contains the secret %q; S-5.11 excludes callout content "+
				"in every reveal state", marker)
		}
	}

	// The positive, before the search: the ordinary prose on *both sides* of the
	// callouts is indexed, so the exclusion above is a subtraction rather than an
	// index that stores nothing — and so a rule that excluded "the rest of the
	// document" instead of "the rest of the callout" fails here rather than
	// passing.
	//
	// `combination` is deliberately absent from this list: it appears only inside
	// the hidden callout, and it is asserted as a search absence below. A test that
	// asserted the exclusion against a word that is also in the ordinary prose
	// would be asserting nothing.
	for _, word := range []string{"wyvern", "tide", "iron", "door"} {
		if !strings.Contains(indexed, word) {
			t.Errorf("body_plain does not contain %q; the exclusion removed ordinary prose:\n%s",
				word, indexed)
		}
	}

	// The marker itself did not survive. Asserted as the marker's own spelling
	// rather than as an absence of the word "secret", because `secret` is a word a
	// GM writes *about* secrets and its presence would be correct.
	if strings.Contains(indexed, "[!") {
		t.Errorf("body_plain still carries a callout marker:\n%s", indexed)
	}

	// A search for each secret returns nothing, and so does a search for the prose
	// that only appears inside a callout — which is the sharper assertion, because
	// the words of a secret are ordinary words.
	assertNoSearchHit(t, f, hiddenSecret)
	assertNoSearchHit(t, f, shownSecret)
	assertNoSearchHit(t, f, "scale")
	assertNoSearchHit(t, f, "token")

	// And the snippet, which is the only place the text is shown to a reader. The
	// positive query is the one whose excerpt would be built from the vault's own
	// prose, and it must come back without either secret anywhere in it.
	hits := f.search("wyvern", domain.Requestor{})
	if len(hits) != 1 {
		t.Fatalf("search for the page's own word returned %d hits, want 1", len(hits))
	}

	for _, marker := range []string{hiddenSecret, shownSecret} {
		if strings.Contains(hits[0].Snippet, marker) {
			t.Errorf("the search snippet carries the secret %q; a snippet is built from "+
				"body_plain and is shown to a reader", marker)
		}
	}

	// And the page is still findable by its own words, which is the property that
	// makes the exclusion a usable index rather than a silent page.
	if len(f.search("tide", domain.Requestor{})) != 1 {
		t.Error("the page is not findable by its ordinary prose; the exclusion took too much")
	}
}

// TestBodyPlainKeepsWhatAKeeperWouldSearchFor is the other half of the derivation,
// and the reason it exists rather than indexing the raw file.
//
// The words in a wiki are not the words of a text file. A reference names a page,
// a link carries a label, a statblock header is prose to the person who wrote it,
// and a GM searching for a thing in this project would type the *name* of it.
func TestBodyPlainKeepsWhatAKeeperWouldSearchFor(t *testing.T) {
	f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

	body := strings.Join([]string{
		"# The Coast",
		"",
		"See [[Svartalfheim]] for the realm, and [[lore/The Wyvern|the wyvern's lair]].",
		"",
		"A [statblock](statblocks/wyvern) for the beast.",
		"",
		"> [!note] A note",
		"> The tide turns at dusk.",
		"",
		"- Roll 1d20 with `d20` for insight",
		"",
		"An image of the coast: ![the cove](images/cove.png)",
		"",
	}, "\n")

	f.applyOK(f.write("lore/coast.md", body))

	indexed := f.bodyOf("lore/coast.md")

	// A reference with no alias contributes the target's own name, which is how
	// the corpus writes it and what a reader would type.
	assertContains(t, indexed, "Svartalfheim", "a wikilink with no alias")

	// A reference with an alias contributes the alias: it is what the page reads
	// as, and searching for the target would be searching for the file's name
	// rather than for the thing the author linked to.
	assertContains(t, indexed, "wyvern's lair", "a wikilink alias")

	// A statblock reference is a link whose href is a path. The path must not
	// become searchable text — it is host-relative information about the vault's
	// layout, and S-3.5 makes every path attacker-reachable — but the link's text
	// must, because it is the words the author chose.
	assertContains(t, indexed, "statblock", "a link's text")

	if strings.Contains(indexed, "statblocks/wyvern") {
		t.Errorf("body_plain carries a link's href:\n%s", indexed)
	}

	// An image contributes its alt text and not its source, for the same reason.
	assertContains(t, indexed, "the cove", "an image's alt text")

	if strings.Contains(indexed, "images/cove.png") {
		t.Errorf("body_plain carries an image's source:\n%s", indexed)
	}

	// The fence and the emphasis are markup, not language.
	for _, marker := range []string{"#", "**", "[[", "]]", "```"} {
		if strings.Contains(indexed, marker) {
			t.Errorf("body_plain carries the markup %q:\n%s", marker, indexed)
		}
	}

	// And every one of them is findable, which is the assertion that says the
	// flattening produced words rather than noise.
	for _, word := range []string{"Coast", "wyvern", "tide", "insight", "cove"} {
		assertSearchHit(t, f, word)
	}

	// A second page for the three constructs that would have crowded the one
	// above: a footnote label, a task box and a table delimiter row.
	//
	// Stripping punctuation alone leaves `^1` → `1`, `- [x]` → `x` and `|---|---|`
	// → three `---`, so a vault with a checklist in it answers a search for "x" and
	// every page with a numbered footnote answers a search for "1". Asserted on the
	// leftovers by name, because a whole-body `Contains` cannot tell one stray
	// token from another.
	second := strings.Join([]string{
		"Roll for insight[^1] before the crossing.",
		"",
		"- [x] the ford is forded",
		"- [ ] the gate is opened",
		"",
		"| ford | gate |",
		"|------|------|",
		"| shallow | barred |",
		"",
		"[^1]: roll twice and take the higher",
		"",
	}, "\n")

	f.applyOK(f.write("lore/passage.md", second))

	crossing := f.bodyOf("lore/passage.md")

	for _, leftover := range []string{"^", "[x]", "---"} {
		if strings.Contains(crossing, leftover) {
			t.Errorf("body_plain carries the leftover %q:\n%s", leftover, crossing)
		}
	}

	// The words of the constructs that were stripped whole are still there.
	for _, word := range []string{"insight", "ford", "gate", "shallow", "barred"} {
		assertContains(t, crossing, word, "a table cell, a task item or ordinary prose")
	}
}

// TestAnUpsertSkipsFilesThatAreNotPages is the policy the walk and the incremental
// path have to agree on, asserted through the incremental path.
//
// The disagreement is the failure: a row written for a path the walk skipped
// appears in search results on a filesystem event and vanishes on the next prune,
// and the symptom looks like a bug in the watcher rather than a bug in the index.
//
// The temp-file case is the load-bearing one. It is `.semiplane-…` and it ends in
// `.md` in the middle, and a predicate that only checked the suffix would index
// every page semiplane was halfway through writing.
func TestAnUpsertSkipsFilesThatAreNotPages(t *testing.T) {
	f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

	for _, change := range []content.Change{
		f.write("lore/a-page.md", "The wyvern sleeps.\n"),
		f.write("lore/a-note.txt", "The wyvern sleeps.\n"),
		f.write("lore/a-draft.md.tmp", "The wyvern sleeps.\n"),
		f.write(".semiplane-a-page.md.9f3a2b7c.tmp", "The wyvern sleeps.\n"),
		f.write(".hidden.md", "The wyvern sleeps.\n"),
		f.write(".obsidian/workspace.md", "The wyvern sleeps.\n"),
		f.write(".trash/a-page.md", "The wyvern sleeps.\n"),
		f.write("node_modules/a-page.md", "The wyvern sleeps.\n"),
		f.write("images/a-map.png", "The wyvern sleeps.\n"),
	} {
		f.applyOK(change)
	}

	f.assertPaths("lore/a-page.md")

	// And the reindex agrees, which is the point: the two paths are one policy.
	report, err := f.index.ReindexCampaign(t.Context(), f.slug, f.campaign.ID)
	if err != nil {
		t.Fatalf("ReindexCampaign() error = %v, want nil", err)
	}

	f.assertPaths("lore/a-page.md")

	if report.Skipped != 0 {
		t.Errorf("ReindexCampaign() skipped %d files, want 0; a skipped file is one the "+
			"walk found and the index could not read", report.Skipped)
	}

	if report.Found != 1 {
		t.Errorf("ReindexCampaign() found %d files, want 1", report.Found)
	}
}

// TestRenameConvergesThroughEveryLegalSequence is the `OpRename` contract.
//
// Three sequences, and each is one a watcher legitimately produces:
//
//   - a plain rename, where the re-key applies and the file is not read at all;
//   - a remove followed by a rename, which is what a watcher reports for a move
//     across directories it watches separately, and where the source row is gone;
//   - a rename onto a path that already has a row, where the destination wins
//     because the filesystem has already decided which file is there.
//
// The assertion in every case is the same, which is the point: one row, at the
// destination, findable by its words, and no row at the source. An implementation
// that special-cased only the first would pass a single-file rename test.
func TestRenameConvergesThroughEveryLegalSequence(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		// arrange is the sequence of changes the watcher would emit, and the path
		// the tree ends up holding.
		arrange func(t *testing.T, f *indexFixture) string
	}{
		"a plain rename": {
			arrange: func(t *testing.T, f *indexFixture) string {
				f.applyOK(f.write("lore/old.md", "The wyvern sleeps.\n"))
				f.applyOK(f.rename("lore/old.md", "lore/new.md"))

				return "lore/new.md"
			},
		},
		"a remove then a rename": {
			arrange: func(t *testing.T, f *indexFixture) string {
				f.applyOK(f.write("lore/old.md", "The wyvern sleeps.\n"))

				// The file has moved on disk, and the two events arrive as a
				// remove and a rename rather than as one rename — which is what two
				// directory watches produce for a move across the boundary between
				// them, and what an editor's save produces when it unlinks before it
				// links.
				f.move("lore/old.md", "lore/new.md")

				f.applyOK(content.Change{
					CampaignID: f.campaign.ID,
					Slug:       f.slug,
					Op:         content.OpRemove,
					Path:       "lore/old.md",
				})
				f.applyOK(content.Change{
					CampaignID: f.campaign.ID,
					Slug:       f.slug,
					Op:         content.OpRename,
					Path:       "lore/new.md",
					OldPath:    "lore/old.md",
				})

				return "lore/new.md"
			},
		},
		"a rename onto an indexed path": {
			arrange: func(t *testing.T, f *indexFixture) string {
				f.applyOK(f.write("lore/old.md", "The wyvern sleeps.\n"))
				f.applyOK(f.write("lore/taken.md", "Something else entirely.\n"))

				// The source moves over the destination, so the destination's row
				// describes a file that is not there and the source's row is the
				// page that path names now.
				f.move("lore/old.md", "lore/taken.md")

				f.applyOK(content.Change{
					CampaignID: f.campaign.ID,
					Slug:       f.slug,
					Op:         content.OpRename,
					Path:       "lore/taken.md",
					OldPath:    "lore/old.md",
				})

				return "lore/taken.md"
			},
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

			want := testCase.arrange(t, f)

			// One row, at the destination. The length check is what catches a
			// rename that duplicated the page or left the source behind.
			f.assertPaths(want)

			if len(f.search("wyvern", domain.Requestor{})) != 1 {
				t.Error("the renamed page is not findable by its own words")
			}

			// And a reindex agrees, which is the strongest statement available:
			// the index now matches the tree whatever the event sequence was.
			if _, err := f.index.ReindexCampaign(t.Context(), f.slug, f.campaign.ID); err != nil {
				t.Fatalf("ReindexCampaign() error = %v, want nil", err)
			}

			f.assertPaths(want)
		})
	}
}

// TestDeleteAndRestoreConverges is the undelete sequence, end to end.
//
// A page that vanishes and comes back — an Obsidian trash restore, a sync client
// re-creating a file it briefly lost — is the ordinary way a vault changes. The
// row is not the same row (AUTOINCREMENT, migration 0007) and the search hit must
// be the new one rather than an orphan bound to the old rowid, which is what an
// implementation that only deleted the row would leave behind.
func TestDeleteAndRestoreConverges(t *testing.T) {
	f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

	const rel = "lore/vault.md"

	f.applyOK(f.write(rel, "The wyvern sleeps.\n"))

	first := f.search("wyvern", domain.Requestor{})
	if len(first) != 1 {
		t.Fatalf("search before the delete returned %d hits, want 1", len(first))
	}

	f.applyOK(f.remove(rel))

	// No row and no hit. Both, and the second is the assertion a row-only delete
	// fails: a hit with no row behind it is a search result that 404s.
	f.assertPaths()

	if len(f.search("wyvern", domain.Requestor{})) != 0 {
		t.Error("a deleted page is still findable; its FTS entries outlived the row")
	}

	f.applyOK(f.write(rel, "The wyvern sleeps.\n"))

	second := f.search("wyvern", domain.Requestor{})
	if len(second) != 1 {
		t.Fatalf("search after the restore returned %d hits, want 1", len(second))
	}

	if second[0].ID == first[0].ID {
		t.Errorf("the restored page reused rowid %d; AUTOINCREMENT exists so the deleted "+
			"page's orphaned FTS entries cannot bind to it", second[0].ID)
	}

	if second[0].Snippet == "" {
		t.Error("the restored page has no snippet; the FTS entry did not come back with the row")
	}
}

// TestADirectoryRenameMovesEveryPageUnderneath is the operation a per-page rename
// cannot express, and the one a sync client performs.
//
// One filesystem event for a folder, and the pages inside it did not change — only
// where they are. A per-file answer would re-read and re-hash every file in the
// folder, and would still leave the old paths for the prune to find.
//
// The subtree is nested on purpose: a folder rename that handles only the files
// directly inside it leaves the subdirectory's pages behind, which is the shape a
// real vault has.
func TestADirectoryRenameMovesEveryPageUnderneath(t *testing.T) {
	f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

	f.applyOK(f.write("lore/shore/cliffs.md", "The cliffs stand.\n"))
	f.applyOK(f.write("lore/shore/cave/hollow.md", "The hollow is deep.\n"))
	f.applyOK(f.write("lore/shorehouse.md", "The shorehouse.\n"))

	// The folder itself, which is what a watcher reports for a directory move.
	if err := os.Rename(
		filepath.Join(f.dir, "lore", "shore"),
		filepath.Join(f.dir, "lore", "coast"),
	); err != nil {
		t.Fatalf("rename the directory: %v", err)
	}

	f.applyOK(content.Change{
		CampaignID: f.campaign.ID,
		Slug:       f.slug,
		Op:         content.OpRename,
		Path:       "lore/coast",
		OldPath:    "lore/shore",
	})

	// Every page moved, the nested one included, and the page whose name merely
	// shares a prefix did not.
	f.assertPaths(
		"lore/coast/cave/hollow.md",
		"lore/coast/cliffs.md",
		"lore/shorehouse.md",
	)

	for _, word := range []string{"cliffs", "hollow", "shorehouse"} {
		if len(f.search(word, domain.Requestor{})) != 1 {
			t.Errorf("the page moved to %q is not findable by %q", "lore/coast", word)
		}
	}
}

// TestAReindexConvergesFromAnyStartingPoint is the claim the whole file rests on.
//
// The invariant is stated against the tree rather than against the event stream: a
// page is indexed if and only if a readable `.md` file says so. So each case here
// starts from a different kind of wrong — nothing indexed at all, a row for a
// deleted file, a row whose hash disagrees with its file — and asserts that one
// reindex reaches the same state.
//
// The third case is the one a status assertion would miss. A row whose
// `content_hash` does not match the bytes on disk produces a render cache entry
// that never invalidates (S-5.2), and nothing about it is visible in a page
// listing; only the hash moving is.
func TestAReindexConvergesFromAnyStartingPoint(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		// disturb puts the index into the wrong state, or leaves it alone.
		disturb func(t *testing.T, f *indexFixture)
	}{
		"nothing was indexed": {
			disturb: func(_ *testing.T, _ *indexFixture) {},
		},
		"a row for a file that is gone": {
			disturb: func(t *testing.T, f *indexFixture) {
				f.applyOK(f.write("lore/deleted.md", "The wyvern sleeps.\n"))
				// The file goes; the event is lost, which is the whole premise.
				if err := os.Remove(filepath.Join(f.dir, "lore", "deleted.md")); err != nil {
					t.Fatalf("remove the file: %v", err)
				}
			},
		},
		"a row whose hash disagrees with the file": {
			disturb: func(t *testing.T, f *indexFixture) {
				f.applyOK(f.write("lore/one.md", "The wyvern sleeps.\n"))

				// Rewritten behind the index's back — what a sync client's write
				// looks like to a watcher whose event never arrived. One of the two
				// pages the tree converges to, so nothing is gained or lost by the
				// write itself.
				if err := os.WriteFile(
					filepath.Join(f.dir, "lore", "one.md"),
					[]byte("The wyvern sleeps, and the tide is out.\n"),
					0o600,
				); err != nil {
					t.Fatalf("rewrite the file: %v", err)
				}
			},
		},
		"a row that is not in the tree at all": {
			disturb: func(t *testing.T, f *indexFixture) {
				// A path the walk will never see, written through the store so it
				// is a row rather than a file. A prune alone would remove it; a
				// reindex has to as well.
				_, err := f.store.UpsertPage(t.Context(), domain.PageText{
					CampaignID:  f.campaign.ID,
					Path:        "lore/ghost.md",
					Title:       "Ghost",
					ContentHash: "0",
					ByteSize:    5,
					BodyPlain:   "ghostly",
				})
				if err != nil {
					t.Fatalf("UpsertPage() error = %v, want nil", err)
				}
			},
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

			// The tree every case converges to.
			f.applyOK(f.write("lore/one.md", "The wyvern sleeps.\n"))
			f.applyOK(f.write("lore/two.md", "The vault stands.\n"))

			testCase.disturb(t, f)

			report, err := f.index.ReindexCampaign(t.Context(), f.slug, f.campaign.ID)
			if err != nil {
				t.Fatalf("ReindexCampaign() error = %v, want nil", err)
			}

			f.assertPaths("lore/one.md", "lore/two.md")

			if report.Found != 2 {
				t.Errorf("ReindexCampaign() found %d files, want 2", report.Found)
			}

			if report.Skipped != 0 {
				t.Errorf("ReindexCampaign() skipped %d files, want 0", report.Skipped)
			}

			// The search agrees with the page list, which is what makes the index
			// *searchable* rather than merely present.
			for _, word := range []string{"wyvern", "vault"} {
				if len(f.search(word, domain.Requestor{})) != 1 {
					t.Errorf("the converged index is not findable by %q", word)
				}
			}

			if len(f.search("ghostly", domain.Requestor{})) != 0 {
				t.Error("a row with no file behind it is still findable")
			}
		})
	}
}

// TestAReindexPrunesRowsWithNoFile is the prune in isolation, and the assertion
// that separates it from a reindex.
//
// This is what makes "converge" true rather than aspirational: without it, a
// dropped delete event leaves a search hit pointing at a page that no longer
// exists, and the only way to notice is to click the result.
func TestAReindexPrunesRowsWithNoFile(t *testing.T) {
	f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

	f.applyOK(f.write("lore/kept.md", "The wyvern sleeps.\n"))
	f.applyOK(f.write("lore/lost.md", "The vault holds a secret.\n"))

	// The delete event never arrives.
	if err := os.Remove(filepath.Join(f.dir, "lore", "lost.md")); err != nil {
		t.Fatalf("remove the file: %v", err)
	}

	pruned, err := f.index.PruneMissingPages(t.Context(), f.slug, f.campaign.ID)
	if err != nil {
		t.Fatalf("PruneMissingPages() error = %v, want nil", err)
	}

	if pruned != 1 {
		t.Errorf("PruneMissingPages() deleted %d rows, want 1", pruned)
	}

	f.assertPaths("lore/kept.md")

	// And the search hit went with it, which is the whole point: a row left
	// behind is a result that 404s.
	if len(f.search("secret", domain.Requestor{})) != 0 {
		t.Error("a page with no file behind it is still findable")
	}
}

// TestAReindexDoesNotRewritePagesThatDidNotChange is the property that makes a
// periodic rescan affordable.
//
// S-4.5's fallback is a *periodic full rescan*, and a rescan that rewrites every
// row on every tick is a fallback nobody leaves running: it moves `updated_at` on
// every page in the vault, so every "recently changed" listing is permanently
// wrong, and it costs a write transaction per page per tick.
//
// The count is the assertion. `Found` and `Written` are reported separately, and
// the case where they diverge is this one.
func TestAReindexDoesNotRewritePagesThatDidNotChange(t *testing.T) {
	f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

	f.applyOK(f.write("lore/one.md", "The wyvern sleeps.\n"))
	f.applyOK(f.write("lore/two.md", "The vault stands.\n"))

	report, err := f.index.ReindexCampaign(t.Context(), f.slug, f.campaign.ID)
	if err != nil {
		t.Fatalf("ReindexCampaign() error = %v, want nil", err)
	}

	if report.Written != 0 {
		t.Errorf("a second reindex wrote %d rows, want 0", report.Written)
	}

	if report.Unchanged != report.Found {
		t.Errorf("a second reindex left %d of %d files unchanged",
			report.Unchanged, report.Found)
	}

	// One file changed, and only that one is written. Written straight to the
	// filesystem rather than through `write`, because the point is that no change
	// reaches the index — the watcher's event is the thing being withheld.
	if writeErr := os.WriteFile(
		filepath.Join(f.dir, "lore", "two.md"),
		[]byte("The vault stands, and the tide is out.\n"),
		0o600,
	); writeErr != nil {
		t.Fatalf("rewrite the page: %v", writeErr)
	}

	report, err = f.index.ReindexCampaign(t.Context(), f.slug, f.campaign.ID)
	if err != nil {
		t.Fatalf("ReindexCampaign() after a change error = %v, want nil", err)
	}

	if report.Written != 1 {
		t.Errorf("a reindex after one change wrote %d rows, want 1", report.Written)
	}

	assertSearchHit(t, f, "tide")
}

// TestABurstOfWritesNeverLeavesATruncatedRow is the partial-write property, and
// it is asserted as an invariant over many indexings rather than as one
// observation.
//
// S-4.3 settles *when* a write is finished happening — that is the settle filter's
// work, and this test does not own it — so the property asserted here is narrower
// and is the half that *is* the indexer's: **whatever** bytes the indexer reads,
// the `content_hash` and the `byte_size` it records describe that one read and no
// other. A writer that hashed one read and stat'ed another would record a
// `content_hash` for one file and a `byte_size` for a different one, and the pair
// would disagree for as long as the write lasted.
//
// The invariant is therefore stated over every *prefix* of every variant, because
// a non-atomic writer truncates before it writes and a reader racing it sees a
// prefix: `content_hash` must be the digest of some byte sequence of exactly
// `byte_size` bytes. A pair assembled from two different reads satisfies neither.
//
// The second half is the convergence claim, and it is asserted the way it would
// actually be reached: once the file is settled — the settle filter's guarantee,
// simulated here by confirming the size across two samples before indexing — the
// indexed hash is the digest of the *complete* final bytes, and a hash that
// disagreed with the file is corrected rather than retained.
func TestABurstOfWritesNeverLeavesATruncatedRow(t *testing.T) {
	const (
		variants = 24
		writes   = 120
	)

	f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

	// Every byte sequence the indexer could possibly have read, keyed by digest,
	// with the length that digest is the digest of. Populated with every prefix of
	// every variant for the reason in the doc comment; the prefixes are what a
	// racing reader sees.
	readable := newReadableSet(variants)

	// The page exists before the burst starts, so every indexing attempt has a file
	// to read. A read of a file that is not there is a different assertion — a
	// dropped event — and would abort this loop before it read anything.
	f.write("lore/vault.md", burstBody(0))

	writer := &burstWriter{
		dir:      f.dir,
		path:     "lore/vault.md",
		variants: variants,
	}

	var indexer sync.WaitGroup

	indexer.Go(func() {
		writer.run(f.t.Context(), writes)
	})

	// Index continuously while the writer rewrites the file, so the indexer reads
	// at every stage of a burst rather than at a chosen one.
	for seen := 1; seen <= writes; seen++ {
		if err := f.index.ApplyChange(
			context.Background(),
			content.Change{
				CampaignID: f.campaign.ID,
				Slug:       f.slug,
				Op:         content.OpUpsert,
				Path:       "lore/vault.md",
			},
		); err != nil {
			t.Fatalf("ApplyChange() during the burst (attempt %d) error = %v, want nil",
				seen, err)
		}

		if err := assertRowIsSelfConsistent(f, readable); err != nil {
			t.Fatalf("after %d indexings: %v", seen, err)
		}
	}

	indexer.Wait()

	// Convergence, reached the way it is reached in production: the settle filter
	// has confirmed the write finished, so the indexer reads bytes that will not
	// change under it.
	final := burstBody(variants - 1)
	if err := os.WriteFile(
		filepath.Join(f.dir, "lore", "vault.md"),
		[]byte(final),
		0o600,
	); err != nil {
		t.Fatalf("write the final bytes: %v", err)
	}

	if err := f.index.ApplyChange(
		t.Context(),
		content.Change{
			CampaignID: f.campaign.ID,
			Slug:       f.slug,
			Op:         content.OpUpsert,
			Path:       "lore/vault.md",
		},
	); err != nil {
		t.Fatalf("ApplyChange() on the settled bytes error = %v, want nil", err)
	}

	page, err := f.store.PageByPath(t.Context(), f.campaign.ID, "lore/vault.md")
	if err != nil {
		t.Fatalf("PageByPath() after the burst error = %v, want nil", err)
	}

	// A hash that disagrees with the file is corrected, not retained. This is the
	// assertion that would fail if the indexer skipped a write because the path
	// looked already-known.
	if page.ContentHash != contentHashFor(final) {
		t.Errorf("ContentHash = %q, want the digest of the settled bytes %q; a re-index on "+
			"those bytes must correct whatever the burst left",
			page.ContentHash, contentHashFor(final))
	}

	if page.ByteSize != int64(len(final)) {
		t.Errorf("ByteSize = %d, want %d", page.ByteSize, len(final))
	}

	// And the row is coherent with the file rather than merely present, which is
	// the property S-5.2's cache depends on.
	if got := f.bodyOf("lore/vault.md"); !strings.Contains(got, "wyvern") {
		t.Errorf("body_plain = %q; the settled write did not reach the index", got)
	}
}

// TestABadFileIsSkippedAndTheRestIsIndexed is the per-file failure half, and the
// three cases are the three ways a vault file fails.
//
// A build that aborts on any of them would make a wiki unavailable for a reason
// an operator cannot see from a 500, and the page that failed is a page whose
// *links* break — not a page that takes the campaign's index with it.
//
// Malformed front matter is the interesting case: it is indexed, as prose. S-3.3
// says a block that did not interpret is inert, the page renders, and refusing to
// index it here would break every link to a page that works.
func TestABadFileIsSkippedAndTheRestIsIndexed(t *testing.T) {
	f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

	f.applyOK(f.write("lore/good.md", "The wyvern sleeps.\n"))
	f.applyOK(f.write("lore/unreadable.md", "The wyvern sleeps.\n"))
	f.applyOK(f.write("lore/malformed.md", "---\nkind: [a, b\ntitle: x\n---\n\nThe tide.\n"))

	// Over the index cap, and written directly rather than through `write` because
	// `write` applies the change — and the change would have to fail. A page this
	// large cannot be produced through the content package, which is the same
	// reason the cap exists.
	oversized := "The wyvern sleeps.\n" + strings.Repeat("filler ", 400_000)
	if err := os.WriteFile(
		filepath.Join(f.dir, "lore", "huge.md"),
		[]byte(oversized),
		0o600,
	); err != nil {
		t.Fatalf("write the oversized page: %v", err)
	}

	// An unreadable file, asserted only where the process is not privileged
	// enough to read it anyway. root bypasses the permission bits, so under root
	// the file is readable and there is nothing to assert — which is why this is
	// skipped rather than counted. The oversized page above is the deterministic
	// half of "a file the index cannot read", and the two overlap on purpose: the
	// contract is that the walk reports it and the pass continues, and one
	// deterministic witness is enough to hold it.
	unreadableHeld := makeUnreadable(filepath.Join(f.dir, "lore", "unreadable.md"))
	defer func() {
		if unreadableHeld {
			if err := os.Chmod(filepath.Join(f.dir, "lore", "unreadable.md"), 0o600); err != nil {
				t.Errorf("restore the unreadable page's mode: %v", err)
			}
		}
	}()

	report, err := f.index.ReindexCampaign(t.Context(), f.slug, f.campaign.ID)
	if err != nil {
		t.Fatalf("ReindexCampaign() error = %v, want nil; one bad file must not abort the index",
			err)
	}

	// The oversized page is always skipped. The unreadable one is skipped only
	// when the process genuinely cannot read it, which is why the expectation is
	// built from what the fixture achieved rather than written out.
	wantSkipped := 1
	if unreadableHeld {
		wantSkipped++
	}

	if report.Skipped != wantSkipped {
		t.Errorf("ReindexCampaign() skipped %d files, want %d", report.Skipped, wantSkipped)
	}

	// Everything else is indexed — and the malformed one is indexed as prose,
	// because S-3.3 says it renders.
	kept := []string{"lore/good.md", "lore/malformed.md"}
	if !unreadableHeld {
		kept = append(kept, "lore/unreadable.md")
	}

	f.assertPaths(kept...)

	assertSearchHit(t, f, "tide")

	page, err := f.store.PageByPath(t.Context(), f.campaign.ID, "lore/malformed.md")
	if err != nil {
		t.Fatalf("PageByPath() for the malformed page error = %v, want nil", err)
	}

	if page.Kind != domain.KindProse {
		t.Errorf("Kind = %q for a page with malformed front matter, want %q",
			page.Kind, domain.KindProse)
	}

	// The title is absent because the block did not interpret, and an absent title
	// is an answer rather than a failure: `pages.title` is NOT NULL and empty is
	// what a page without a title holds.
	if page.Title != "" {
		t.Errorf("Title = %q for a page whose front matter did not interpret, want empty",
			page.Title)
	}
}

// TestAnAnonymousSearchReturnsNoPrivateRow is S-8.2, asserted the way
// `internal/httpapi/wiki/partition_test.go` asserts its own partition property: as
// an absence over every spelling, with the positive first.
//
// The failure mode is not a wrong status. It is a perfectly correct 200 carrying a
// private title and a private snippet, and a test that asserted on the status
// would pass while the product leaked. So the marker is a string that appears in
// exactly one page of one private campaign, the positive is asserted first so the
// negatives cannot pass by serving nothing, and then the marker is looked for in
// every field of every hit — including the snippet, which is where a *leak* would
// actually be, since a title is often the least interesting part.
func TestAnAnonymousSearchReturnsNoPrivateRow(t *testing.T) {
	t.Parallel()

	marker := "PRIVATEMARKER-7d41e0"

	private := newCampaignFixture(t, "closed-vault", domain.VisibilityPrivate)
	public := newCampaignFixture(t, "open-road", domain.VisibilityPublic)

	// The private page, with the marker in every field a result could carry it in:
	// the title, the body, and therefore the snippet.
	private.applyOK(private.write("lore/secret.md",
		"---\ntitle: "+marker+"\n---\n\nThe wyvern sleeps near "+marker+".\n"))

	// A public page with the same word, so the exclusion cannot pass by matching
	// nothing: the query has to reach *a* row.
	public.applyOK(public.write("lore/shore.md",
		"---\ntitle: The Shore\n---\n\nThe wyvern sleeps by the wyvern sea.\n"))

	// The positive, first and explicitly. A member of the private campaign must
	// receive the marker in all three fields, or every negative below is vacuous.
	reader, err := private.store.CreateUser(t.Context(), domain.User{
		Username:     "reader",
		PasswordHash: "not-a-real-hash",
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v, want nil", err)
	}

	if _, memberErr := private.store.CreateMembership(t.Context(), domain.Membership{
		CampaignID: private.campaign.ID,
		UserID:     reader.ID,
		Role:       domain.RolePlayer,
	}); memberErr != nil {
		t.Fatalf("CreateMembership() error = %v, want nil", memberErr)
	}

	member := private.search(marker, domain.Requestor{
		Authenticated: true,
		UserID:        reader.ID,
	})
	if len(member) != 1 {
		t.Fatalf("a member received %d hits, want 1 — the rest of this test would be vacuous",
			len(member))
	}

	// The marker is in the title and the body, so it must arrive in the title and
	// the snippet. Not asserted on the path, which is the page's name and carries
	// nothing — asserting it there would be asserting that the file is called
	// something it is not called.
	for name, field := range map[string]string{
		"title":   member[0].Title,
		"snippet": member[0].Snippet,
	} {
		if !strings.Contains(field, marker) {
			t.Errorf("a member's hit %s does not contain the marker; the negatives below "+
				"would be vacuous. Got %q", name, field)
		}
	}

	// A second user who is a member of the *public* campaign and not of this one.
	// Without a genuinely non-member, every case below would be a member wearing a
	// different label, and the test would prove nothing about the predicate.
	outsider, err := public.store.CreateUser(t.Context(), domain.User{
		Username:     "outsider",
		PasswordHash: "not-a-real-hash",
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v, want nil", err)
	}

	if _, memberErr := public.store.CreateMembership(t.Context(), domain.Membership{
		CampaignID: public.campaign.ID,
		UserID:     outsider.ID,
		Role:       domain.RolePlayer,
	}); memberErr != nil {
		t.Fatalf("CreateMembership() error = %v, want nil", memberErr)
	}

	// The negatives, over every requestor that is not a member of the private
	// campaign and every way of asking. Listing them is the point: a test that
	// checks only the anonymous case checks a fraction of the surface, and the
	// instance administrator is the one most likely to be waved through — S-2.7
	// says administration is not campaign read.
	nonMembers := map[string]domain.Requestor{
		"anonymous": {},
		"an authenticated non-member of the private campaign": {
			Authenticated: true,
			UserID:        outsider.ID,
		},
		"an unauthenticated requestor carrying a member's id": {
			UserID: reader.ID,
		},
		"an instance administrator who is not a member": {
			Authenticated: true,
			UserID:        outsider.ID,
			IsAdmin:       true,
		},
		"a requestor with a negative id": {
			Authenticated: true,
			UserID:        -1,
		},
	}

	queries := map[string]string{
		"the marker itself":               marker,
		"a word in the body":              "sleeps",
		"the marker truncated":            marker[:10],
		"an operator the marker contains": marker[:6] + " OR " + marker,
	}

	for role, requestor := range nonMembers {
		for shape, query := range queries {
			hits, err := private.store.SearchPages(
				t.Context(),
				store.PageSearch{Query: query},
				requestor,
			)
			if err != nil {
				t.Fatalf("SearchPages(%q) as %s error = %v, want nil", query, role, err)
			}

			for _, hit := range hits {
				assertNoMarker(t, hit, marker, role, shape)
			}
		}
	}

	// The public page is still reachable, which is the other half: the predicate is
	// filtering by visibility and not by refusing to answer.
	if len(public.search("wyvern", domain.Requestor{})) != 1 {
		t.Error("the public page is not findable by its own words; the visibility " +
			"predicate is excluding rather than partitioning")
	}
}

// TestACampaignWithNoContentRootIsReported is the degraded case, and it is a
// distinct error because the remedy is different.
//
// S-4.5: a missing content root marks a campaign degraded and the server still
// starts. The Indexer therefore reports it and returns; it does not invent a
// directory and it does not prune — a prune against a root that could not be
// walked would delete every row in a campaign whose files are all still there,
// which is the one failure this package could cause rather than observe.
func TestACampaignWithNoContentRootIsReported(t *testing.T) {
	f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

	f.applyOK(f.write("lore/one.md", "The wyvern sleeps.\n"))

	const unknown = "never-opened"

	if _, err := f.index.ReindexCampaign(
		t.Context(),
		unknown,
		f.campaign.ID,
	); !errors.Is(err, content.ErrNoContentRoot) {
		t.Errorf("ReindexCampaign() of an unopened campaign error = %v, want ErrNoContentRoot", err)
	}

	if _, err := f.index.PruneMissingPages(
		t.Context(),
		unknown,
		f.campaign.ID,
	); !errors.Is(err, content.ErrNoContentRoot) {
		t.Errorf("PruneMissingPages() of an unopened campaign error = %v, want ErrNoContentRoot",
			err)
	}

	// Nothing was touched, which is the property that matters.
	f.assertPaths("lore/one.md")
}

// TestTheIndexerIsASink is the compile-time statement that the watcher can hand it
// changes without knowing anything else about the index.
//
// A method value rather than an interface assertion on the type, because
// `ChangeSink` is a function type and that is what the watcher holds.
var _ content.ChangeSink = content.NewIndexer(nil, nil, nil).HandleChange

// makeUnreadable removes a file's read permission and reports whether that
// actually made it unreadable *to this process*.
//
// A bool rather than an assumption, because the test process may be running as
// root and root bypasses the permission bits. Returning the fact means the test
// states its expectation from what is true rather than from what the fixture
// intended, and a run under root degrades to the deterministic cases rather than
// failing on a fixture it cannot produce.
func makeUnreadable(name string) bool {
	if err := os.Chmod(name, 0o200); err != nil {
		return false
	}

	_, readErr := os.ReadFile(name)

	return readErr != nil
}

// burstWriter rewrites one page in a loop, producing the partial-write condition
// a real editor produces: the file is a different byte sequence at every instant
// the indexer might read it.
type burstWriter struct {
	dir      string
	path     string
	variants int
}

// run rewrites the file writes times.
//
// `os.WriteFile` rather than the confined root's atomic write, and that is the
// point. The atomic path stages through a temp file and renames, so a reader never
// sees a partial document — which is exactly the condition this test exists to put
// the indexer under. A `cp` into a mounted vault, a sync client writing in place,
// and an editor whose filesystem has no atomic rename all truncate first.
func (w *burstWriter) run(ctx context.Context, writes int) {
	full := filepath.Join(w.dir, filepath.FromSlash(w.path))

	for idx := range writes {
		if ctx.Err() != nil {
			return
		}

		body := burstBody(idx % w.variants)

		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			return
		}
	}
}

// burstBody is one complete write, and each is a different length so that a
// truncated read is detectable by size alone.
func burstBody(idx int) string {
	return strings.Repeat("The wyvern sleeps. ", idx+1) + fmt.Sprintf("burst %02d\n", idx)
}

// newReadableSet is every byte sequence a racing reader could have seen, keyed by
// digest, with the length that digest belongs to.
//
// Every prefix of every variant, because `os.WriteFile` truncates before it writes
// and a reader can land anywhere in that window. Without the prefixes the
// invariant would assert that the indexer never reads a partial document — which
// is the *settle filter's* guarantee, not this one's, and asserting it here would
// be asserting somebody else's requirement through a test that cannot fix it.
//
// The prefixes are a bounded set: 24 variants of a few hundred bytes each.
func newReadableSet(variants int) map[string]int {
	readable := make(map[string]int, variants*64)

	for idx := range variants {
		body := burstBody(idx)

		for length := range len(body) + 1 {
			readable[contentHashFor(body[:length])] = length
		}
	}

	return readable
}

// assertRowIsSelfConsistent is the burst invariant, on one row.
//
// `content_hash` must be the digest of some byte sequence of exactly `byte_size`
// bytes. A pair assembled from two different reads — hash one, stat another —
// satisfies neither half, because a digest is a digest of one specific sequence
// and a truncated sequence's digest is not any complete one's.
func assertRowIsSelfConsistent(f *indexFixture, readable map[string]int) error {
	var (
		digest string
		size   int64
	)

	err := f.store.DB().QueryRowContext(f.t.Context(),
		"SELECT content_hash, byte_size FROM pages WHERE campaign_id = ? AND path = ?",
		f.campaign.ID, "lore/vault.md").Scan(&digest, &size)
	if err != nil {
		return fmt.Errorf("read the indexed row: %w", err)
	}

	length, known := readable[digest]
	if !known {
		return fmt.Errorf(
			"content_hash %q is the digest of no byte sequence the file ever held; the row "+
				"was built from bytes that were not read whole", digest,
		)
	}

	if length != int(size) {
		return fmt.Errorf(
			"content_hash %q is paired with byte_size %d, but that digest belongs to a "+
				"sequence of %d bytes; the two came from different reads", digest, size, length,
		)
	}

	return nil
}

// contentHashFor is the digest `pages.content_hash` holds.
//
// Computed here rather than imported, for the reason `tokenHash` in the store's
// fixtures gives: the content package must not depend on the store's internals
// even in a test, and the shape is a contract worth restating — 64 lowercase hex
// characters, because that is what the column is sized for and what an ETag
// quotes (S-5.3).
func contentHashFor(body string) string {
	sum := sha256.Sum256([]byte(body))

	return hex.EncodeToString(sum[:])
}

// assertContains asserts that indexed holds want, naming what put it there.
func assertContains(t *testing.T, indexed, want, from string) {
	t.Helper()

	if !strings.Contains(indexed, want) {
		t.Errorf("body_plain does not contain %q, which came from %s:\n%s", want, from, indexed)
	}
}

// assertSearchHit asserts a query finds the indexFixture's page.
func assertSearchHit(t *testing.T, f *indexFixture, query string) {
	t.Helper()

	if len(f.search(query, domain.Requestor{})) == 0 {
		t.Errorf("search for %q returned nothing; body_plain did not keep the words a reader "+
			"would type", query)
	}
}

// assertNoSearchHit asserts a query finds nothing in the indexFixture's campaign.
func assertNoSearchHit(t *testing.T, f *indexFixture, query string) {
	t.Helper()

	hits := f.search(query, domain.Requestor{})

	if len(hits) == 0 {
		return
	}

	for idx := range hits {
		t.Errorf("search for %q returned %q (snippet %q); body_plain carried text it must not",
			query, hits[idx].Path, hits[idx].Snippet)
	}
}

// assertNoMarker asserts a search hit carries no private marker, in any field.
func assertNoMarker(t *testing.T, hit domain.SearchHit, marker, role, shape string) {
	t.Helper()

	for name, field := range map[string]string{
		"title":         hit.Title,
		"path":          hit.Path,
		"snippet":       hit.Snippet,
		"content_hash":  hit.ContentHash,
		"campaign_slug": hit.CampaignSlug,
	} {
		if strings.Contains(field, marker) {
			t.Errorf("%s received a private %s (%q) searching %q; S-8.2 says every FTS query "+
				"joins campaigns on visibility", role, name, field, shape)
		}
	}
}
