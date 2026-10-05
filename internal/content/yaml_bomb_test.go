package content_test

// The YAML bomb in front matter, at the layer where it stops being a parser's
// problem.
//
// Front matter is **attacker-reachable**. S-3.1 makes the filesystem the source of
// truth and Obsidian Sync is what puts files in it — a shared vault, a community
// plugin, a compromised device — so a page can arrive from outside the instance
// without the GM doing anything. `MaxDocumentBytes` bounds the *bytes*; it does not
// bound a small document's *expansion*, which is what `policy.go`'s own note says
// and what `yaml.v3`'s alias-expansion limit is for.
//
// `TestParseRefusesAnAliasBomb` already holds that limit, and holds it as an
// allocation bound, which is the strongest form available. So this file is not a
// second copy of it. It asks the question the parser-level test cannot:
//
// **when a block is refused, what has the rest of the system already decided from
// it?**
//
// `interpretFrontMatter` discards the whole block on any error, because the parser
// returns a partly-filled map alongside its error and a partly-interpreted block is
// the one outcome that must not reach a renderer. That is a claim about a function's
// return value, and this file asserts it where the consequence is: the *indexed
// row*. A page whose front matter was refused still gets a row (S-3.3 — a malformed
// block is inert, not fatal, and skipping the page would break every link to it), so
// the row's `kind`, `title` and `body_plain` are written from a document whose block
// was thrown away. If anything from that block reached them, the limit would be
// enforced at the parser and unenforced everywhere downstream — which is the shape
// of bug this file exists to make impossible, because a bomb that decides a page's
// kind decides how the rest of the product renders it.
//
// The bait is placed at the **root** of the mapping, above the expanding keys, so
// that accepting the block produces observable rows rather than an observable
// allocation: `kind: token` and `title: …` are exactly what the indexer reads.

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
)

// The bomb's bait, and the strings that must reach nothing.
//
// `bombTitleBait` is at the root of the block, so a block that *was* interpreted
// writes it into `pages.title` and makes it searchable — which is what makes the
// assertions below able to fail at all. The other two sit inside the expanding
// values, where they can only reach `Fields`, and are asserted for the all-or-
// nothing property rather than for redaction.
const (
	bombTitleBait = "WOLFGLASS-PASSPHRASE-6f2a"
	bombValueBait = "OSSIARY-BEACON-51cd"
	bombProse     = "the wyvern sleeps above the iron door"
)

// rootedAliasBomb builds a billion-laughs document whose mapping *starts* with the
// two keys the indexer promotes.
//
// Distinct from `frontmatter_test.go`\'s `aliasBomb`, and deliberately: that one
// puts its bait `title:` at the **end**, which is right for asserting that a refused
// block yields nothing. This one puts it at the **start**, so that a block the parser
// *accepted* would write an observable kind and an observable title into the index —
// which is what makes the assertions below able to fail at all rather than only able
// to pass.
//
// The fan-out is nine and the seed is literal, which is the shape yaml.v3\'s ratio
// check is written against; a fan-out of one at the seed is not refused at three
// levels, because the ratio check needs a thousand decodes before it fires at all,
// and a bomb shallower than that is not a bomb. levels is the number of expansions
// beyond the seed, so four is 6561 values out of well under a kilobyte.
func rootedAliasBomb(levels int) string {
	var b strings.Builder

	b.WriteString("kind: token\n")
	b.WriteString("title: " + bombTitleBait + "\n")

	seed := "\"" + bombValueBait + "\""
	b.WriteString("seed: &seed [" + strings.Repeat(seed+",", 8) + seed + "]\n")

	previous := "seed"

	for level := range levels {
		name := "level" + strconv.Itoa(level)

		b.WriteString(name + ": &" + name + " [" +
			strings.Repeat("*"+previous+",", 9) + "*" + previous + "]\n")

		previous = name
	}

	return b.String()
}

// storedPage reads one page row, which is the only place the indexed kind and title
// exist as stored rather than as the indexer's inputs.
func storedPage(t *testing.T, f *indexFixture, rel string) domain.Page {
	t.Helper()

	page, err := f.store.PageByPath(t.Context(), f.campaign.ID, rel)
	if err != nil {
		t.Fatalf("read the indexed row for %s: %v", rel, err)
	}

	return page
}

// TestARefusedFrontMatterBlockDecidesNothingAboutTheIndexedPage is the claim this
// file exists for, asserted on the row the indexer wrote rather than on the
// `Document` the parser handed back.
//
// Six assertions, and each is a different way the refused block could still have
// decided something:
//
//  1. The page **is** indexed. S-3.3: a block that did not interpret is inert, and
//     skipping the page would break every link to a page that renders perfectly
//     well. Without this the file's other assertions would be satisfied by an empty
//     index.
//  2. `kind` is `prose`, so the bomb did not decide the page's kind. This is the one
//     with the widest blast radius: `kind` is registry-backed, so a bomb that set it
//     would decide which plugin renders the page, how the nav tree labels it and
//     whether the page is a game object at all.
//  3. `title` is empty, so the bait reached no column and no search hit.
//  4. `body_plain` holds the prose outside the block, so the exclusion took the
//     block and not the document.
//  5. Neither bait is searchable.
//  6. The page **is** findable by its own words, which is what makes 3 and 4
//     exclusions rather than a page that vanished.
func TestARefusedFrontMatterBlockDecidesNothingAboutTheIndexedPage(t *testing.T) {
	f := newCampaignFixture(t, "ashen-coast", domain.VisibilityPublic)

	const rel = "lore/bomb.md"

	f.applyOK(f.write(rel, "---\n"+rootedAliasBomb(4)+"---\n\n"+bombProse+", and the tide held.\n"))

	// 1. Indexed, not skipped.
	f.assertPaths(rel)

	// 2. The kind. `prose` is the answer for every page whose block did not
	// interpret, including a page with no block at all (S-3.3), so a caller never
	// has to distinguish the two to know it is holding prose.
	indexed := storedPage(t, f, rel)

	if indexed.Kind != domain.KindProse {
		t.Errorf("indexed kind = %q, want %q: a refused block must not decide the "+
			"page's kind, and kind is registry-backed — a bomb that set it would "+
			"choose which plugin renders the page", indexed.Kind, domain.KindProse)
	}

	// 3. The title, verbatim from the block's second line.
	if indexed.Title != "" {
		t.Errorf("indexed title = %q, want empty: the block was refused, so nothing "+
			"in it reached a column — and pages.title is served in the search index, "+
			"the nav tree and the h1 (ADR 0036)", indexed.Title)
	}

	// 4. The body, which is derived from the source and is where a partly-filled
	// map would have shown up.
	bodyPlain := f.bodyOf(rel)

	if !strings.Contains(bodyPlain, "wyvern") {
		t.Fatalf("body_plain does not contain the page's own prose:\n%s", bodyPlain)
	}

	for _, bait := range []string{bombTitleBait, bombValueBait, "stage"} {
		if strings.Contains(bodyPlain, bait) {
			t.Errorf("body_plain carries %q from a refused block:\n%s", bait, bodyPlain)
		}
	}

	// 5. Neither bait is reachable through the API either.
	assertNoSearchHit(t, f, bombTitleBait)
	assertNoSearchHit(t, f, bombValueBait)

	// 6. And the page is still findable, which is the property that makes the
	// exclusions above usable rather than a silently missing page.
	if hits := f.search("wyvern", domain.Requestor{}); len(hits) != 1 {
		t.Errorf("search for the page's own word returned %d hits, want 1", len(hits))
	}
}

// TestTheParserRefusesTheBombWhateverShapeItArrivesIn covers the two spellings an
// expansion can take, and the second one is the interesting one.
//
// A billion-laughs is usually written as nested aliases, and that is the shape
// `TestParseRefusesAnAliasBomb` uses. A **merge key** — `<<: *anchor` — expands by
// the same arithmetic and is what an Obsidian plugin's front matter is far more
// likely to contain, and it reaches the decoder by a different path: a merge is
// resolved while the *mapping* is being built rather than when a value is decoded.
// A limit that counted only one of the two would leave the other as an unbounded
// expansion, and the test for it is a table row rather than an argument.
//
// The positive is in the same test on purpose: a parser that refused every alias
// would pass every row above, and `TestParseKeepsOrdinaryAliases` covers the legal
// case. It is repeated here because this file's whole argument is that *this* test
// can tell a limit from a blanket refusal, and that argument needs the positive to
// be inside the test making it.
func TestTheParserRefusesTheBombWhateverShapeItArrivesIn(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"nested aliases": rootedAliasBomb(4),

		// The merge-key spelling: one anchor holding a mapping of ten keys, then a
		// mapping that merges it ten times, then one that merges *that* ten times.
		"merge keys": "kind: token\n" +
			"title: " + bombTitleBait + "\n" +
			"anchor: &anchor {" + strings.Repeat("k"+bombValueBait+": v, ", 9) +
			"k9" + bombValueBait + ": v}\n" +
			"m0: &m0 {<<: *anchor}\n" +
			"m1: &m1 {<<: *m0, <<: *m0, <<: *m0, <<: *m0, <<: *m0, " +
			"<<: *m0, <<: *m0, <<: *m0, <<: *m0, <<: *m0}\n" +
			"m2: &m2 {<<: *m1, <<: *m1, <<: *m1, <<: *m1, <<: *m1, " +
			"<<: *m1, <<: *m1, <<: *m1, <<: *m1, <<: *m1}\n",

		// An alias pointing at an anchor that contains itself, which is not an
		// expansion at all and must still be refused rather than looping.
		"a self-referential anchor": "kind: token\n" +
			"title: " + bombTitleBait + "\n" +
			"loop: &loop\n" +
			"  again: *loop\n",
	}

	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			source := []byte("---\n" + block + "---\n\nThe prose survives.\n")

			if len(source) > content.MaxDocumentBytes {
				t.Fatalf("the bomb is %d bytes, so the size cap would be what refuses "+
					"it and this row would not be testing the parser's limit", len(source))
			}

			document := content.Parse(source, registry())

			if document.FrontMatter.Err == nil {
				// Deliberately not the whole struct: a partly-filled map for an
				// accepted bomb is the expansion itself, and printing it would put
				// megabytes into a failure message.
				t.Errorf("Parse() accepted the document: Kind=%q Title=%q, and Fields "+
					"holds %d key(s)", document.FrontMatter.Kind, document.FrontMatter.Title,
					len(document.FrontMatter.Fields))
			}

			if !errors.Is(document.FrontMatter.Err, content.ErrMalformedFrontMatter) {
				t.Errorf("FrontMatter.Err = %v, want ErrMalformedFrontMatter",
					document.FrontMatter.Err)
			}

			// All or nothing: the parser hands back a partly-filled map alongside its
			// error, and reading what it managed to decode would mean rendering a
			// page from a block that was refused.
			if document.FrontMatter.Fields != nil {
				t.Error("FrontMatter.Fields is populated for a refused block; the " +
					"parser's partly-filled map reached the caller")
			}

			if document.FrontMatter.Title != "" {
				t.Errorf("FrontMatter.Title = %q, want empty", document.FrontMatter.Title)
			}

			if document.FrontMatter.Kind != domain.KindProse {
				t.Errorf("FrontMatter.Kind = %q, want %q",
					document.FrontMatter.Kind, domain.KindProse)
			}

			// The prose survives, because a refused block is inert rather than fatal
			// and a page that lost its content would be data loss rather than
			// tolerance.
			if !strings.Contains(document.Body, "The prose survives.") {
				t.Errorf("Body = %q, want the prose after the block", document.Body)
			}
		})
	}

	// The positive, in the same test: a legal merge key still works, so the rows
	// above are a limit and not a refusal of the feature.
	legal := "---\ndefaults: &defaults\n  size: medium\n  disposition: hostile\n" +
		"goblin:\n  <<: *defaults\n  name: Grinnax\ntitle: Bestiary\n---\nA goblin.\n"

	document := content.Parse([]byte(legal), registry())

	if document.FrontMatter.Err != nil {
		t.Fatalf("FrontMatter.Err = %v, want nil for a legal merge key",
			document.FrontMatter.Err)
	}

	if document.FrontMatter.Title != "Bestiary" {
		t.Errorf("FrontMatter.Title = %q, want %q", document.FrontMatter.Title, "Bestiary")
	}
}

// TestNestingIsBoundedByTheSizeCapAndNotByTheParser states the limit that is
// there, because a reader of the two tests above would otherwise conclude that
// yaml.v3 bounds the *shape* of a block and not only its expansion.
//
// It does not. There is no nesting-depth limit in the decoder: a block that is a
// hundred thousand `[` deep parses, and the only thing standing between that and an
// unbounded structure is `MaxDocumentBytes` and the block's own cap. So the claim
// here is a documented one rather than a comfortable one:
//
//   - A deeply nested block **inside** the cap is accepted, and lands in `Fields`
//     as a structure as deep as the bytes allowed.
//   - A deeply nested block **outside** the cap is refused with
//     `ErrDocumentTooLarge`, before the parser is handed it at all.
//
// The depth here is 2000 rather than 100000 because a test that exhausts memory to
// make its point is a test that takes the machine with it, and 2000 is far past
// anything a hand-written or a plugin-written front matter contains — which is the
// statement the size cap is making anyway.
//
// The consequence for a reader is real and is why this test exists rather than a
// comment: `domain.FrontMatter.Fields` is attacker-controlled and arbitrarily deep,
// so **nothing that walks it recursively without a depth bound is safe**. That is a
// constraint on future code, and the honest way to record a constraint on future
// code is a test that states the shape of the input it applies to.
func TestNestingIsBoundedByTheSizeCapAndNotByTheParser(t *testing.T) {
	t.Parallel()

	const depth = 2000

	// A flow sequence nested `depth` deep: two bytes per level plus the scalar.
	nested := "nest: " + strings.Repeat("[", depth) + "x" + strings.Repeat("]", depth) + "\n"

	inside := "---\n" + nested + "---\nThe prose survives.\n"

	if len(inside) > content.MaxDocumentBytes {
		t.Fatalf("the nested block is %d bytes, which is over the cap, so this test "+
			"would be asserting the cap rather than the shape", len(inside))
	}

	document := content.Parse([]byte(inside), registry())

	if document.FrontMatter.Err != nil {
		t.Errorf("FrontMatter.Err = %v, want nil: a nested block inside the size cap "+
			"is accepted, and that is what makes the size cap load-bearing", document.FrontMatter.Err)
	}

	if document.FrontMatter.Fields == nil {
		t.Error("FrontMatter.Fields is nil; the nested block did not decode")
	}

	// The same shape, one byte over the document cap: refused, and refused with the
	// document's own error rather than a parse failure, because the cap is applied
	// before the block is located at all.
	oversized := "---\n" + nested +
		strings.Repeat("x", content.MaxDocumentBytes-len(inside)+1) +
		"\n---\nThe prose survives.\n"

	refused := content.Parse([]byte(oversized), registry())

	if !errors.Is(refused.FrontMatter.Err, content.ErrDocumentTooLarge) {
		t.Errorf("FrontMatter.Err = %v, want ErrDocumentTooLarge", refused.FrontMatter.Err)
	}

	if refused.Present {
		t.Error("Present is true for a document refused before it was split")
	}
}
