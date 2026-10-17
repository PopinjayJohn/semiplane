package index

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func openTestDB(t *testing.T) *Index {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func TestUpsertSearchBacklinksDelete(t *testing.T) {
	ix := openTestDB(t)

	a := "# Alpha\nThe dragon hoards gold in the mountain vault. See [[b]] and [[b|Bee]].\n"
	b := "# Beta\nA quiet village with no dragons. See [[a]].\n"
	if err := ix.Upsert("a.md", a, "hash-a", 1); err != nil {
		t.Fatalf("Upsert a: %v", err)
	}
	if err := ix.Upsert("b.md", b, "hash-b", 2); err != nil {
		t.Fatalf("Upsert b: %v", err)
	}

	hits, err := ix.Search("dragon", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("Search dragon = %d hits, want 2", len(hits))
	}
	if len(hits) == 2 && hits[0].Score < hits[1].Score {
		t.Fatalf("hits not in score order: %+v", hits)
	}
	gotPaths := map[string]bool{hits[0].Path: true, hits[1].Path: true}
	if !gotPaths["a.md"] || !gotPaths["b.md"] {
		t.Fatalf("Search dragon paths = %+v, want a.md + b.md", hits)
	}

	// Discriminating query: only a.md mentions hoards/mountain.
	hits, err = ix.Search("hoards mountain", 10)
	if err != nil {
		t.Fatalf("Search hoards mountain: %v", err)
	}
	if len(hits) != 1 || hits[0].Path != "a.md" {
		t.Fatalf("Search hoards mountain = %+v, want [a.md]", hits)
	}
	if hits[0].Snippet == "" {
		t.Fatal("empty snippet")
	}

	hits, err = ix.Search("village", 10)
	if err != nil {
		t.Fatalf("Search village: %v", err)
	}
	if len(hits) != 1 || hits[0].Path != "b.md" {
		t.Fatalf("Search village = %+v, want [b.md]", hits)
	}

	bl, err := ix.Backlinks("b.md")
	if err != nil {
		t.Fatalf("Backlinks: %v", err)
	}
	if !reflect.DeepEqual(bl, []string{"a.md"}) {
		t.Fatalf("Backlinks(b.md) = %v, want [a.md]", bl)
	}
	bl, err = ix.Backlinks("a.md")
	if err != nil {
		t.Fatalf("Backlinks: %v", err)
	}
	if !reflect.DeepEqual(bl, []string{"b.md"}) {
		t.Fatalf("Backlinks(a.md) = %v, want [b.md]", bl)
	}

	// Bad MATCH syntax must fall back to a quoted phrase, not error.
	hits, err = ix.Search(`"unbalanced`, 10)
	if err != nil {
		t.Fatalf("Search bad syntax errored: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("Search bad syntax = %d hits, want 0", len(hits))
	}

	if err := ix.Delete("a.md"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	hits, err = ix.Search("dragon hoards mountain", 10)
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}
	for _, h := range hits {
		if h.Path == "a.md" {
			t.Fatalf("deleted a.md still in search: %+v", hits)
		}
	}
	bl, err = ix.Backlinks("b.md")
	if err != nil {
		t.Fatalf("Backlinks after delete: %v", err)
	}
	if len(bl) != 0 {
		t.Fatalf("Backlinks(b.md) after delete = %v, want []", bl)
	}
	// b.md's edge b->a must also be gone.
	bl, err = ix.Backlinks("a.md")
	if err != nil {
		t.Fatalf("Backlinks after delete: %v", err)
	}
	if len(bl) != 0 {
		t.Fatalf("Backlinks(a.md) after delete = %v, want []", bl)
	}
}

func TestReindex(t *testing.T) {
	vault := t.TempDir()
	files := map[string]string{
		"one.md":          "# One\napples and oranges\n",
		"sub/two.md":      "# Two\noranges only, see [[one]]\n",
		"three.md":        "no heading here, just bananas\n",
		"skip.txt":        "not markdown\n",
		".git/x.md":       "should be skipped\n",
		".semiplane/y.md": "should be skipped\n",
	}
	for rel, body := range files {
		p := filepath.Join(vault, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ix := openTestDB(t)
	n, err := ix.Reindex(vault)
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if n != 3 {
		t.Fatalf("Reindex = %d, want 3", n)
	}

	hits, err := ix.Search("oranges", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("Search oranges = %+v, want 2 hits", hits)
	}
	bl, err := ix.Backlinks("one.md")
	if err != nil {
		t.Fatalf("Backlinks: %v", err)
	}
	if !reflect.DeepEqual(bl, []string{"sub/two.md"}) {
		t.Fatalf("Backlinks(one.md) = %v, want [sub/two.md]", bl)
	}

	// Remove a file: reindex must drop its rows.
	if err := os.Remove(filepath.Join(vault, "three.md")); err != nil {
		t.Fatal(err)
	}
	n, err = ix.Reindex(vault)
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if n != 2 {
		t.Fatalf("Reindex = %d, want 2", n)
	}
	hits, err = ix.Search("bananas", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("removed three.md still searchable: %+v", hits)
	}
}

func TestRebuildFTS(t *testing.T) {
	ix := openTestDB(t)
	if err := ix.Upsert("a.md", "# A\nrebuild me\n", "h", 1); err != nil {
		t.Fatal(err)
	}
	if err := ix.RebuildFTS(); err != nil {
		t.Fatalf("RebuildFTS: %v", err)
	}
	hits, err := ix.Search("rebuild", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("after rebuild, Search = %+v, want 1 hit", hits)
	}
}

func TestParseLinks(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"simple", "see [[other]]", []string{"other.md"}},
		{"alias", "see [[other|Label]]", []string{"other.md"}},
		{"with ext", "see [[other.md]]", []string{"other.md"}},
		{"case kept", "see [[Other]] and [[other]]", []string{"Other.md", "other.md"}},
		{"dedup sort", "[[b]] [[a]] [[b]]", []string{"a.md", "b.md"}},
		{"dot slash", "[[./sub/note]]", []string{"sub/note.md"}},
		{"spaces", "[[  spaced  ]]", []string{"spaced.md"}},
		{"empty dropped", "[[]] [[  ]]", []string{}},
		{"external dropped", "[[https://x/y]] [[http://x]]", []string{}},
		{"anchor only dropped", "[[#section]]", []string{}},
		{"no links", "plain [text](url) and [single]", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseLinks(tc.in)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseLinks(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseTitle(t *testing.T) {
	if got := ParseTitle("dir/a.md", "# Hello\nbody\n"); got != "Hello" {
		t.Fatalf("heading title = %q", got)
	}
	if got := ParseTitle("dir/a.md", "text\n## Second Level\n"); got != "Second Level" {
		t.Fatalf("h2 title = %q", got)
	}
	if got := ParseTitle("dir/a.md", "no heading\n"); got != "a" {
		t.Fatalf("fallback title = %q", got)
	}
	if got := ParseTitle("a.md", ""); got != "a" {
		t.Fatalf("empty fallback = %q", got)
	}
}

func TestParseTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		// Top-level shapes still supported after the swap to
		// markdown.ParseFrontmatter + TagsFromMeta.
		{"flow", "---\ntags: [a, b]\n---\nbody", []string{"a", "b"}},
		{"flow quoted", "---\ntags: [\"a\", 'b' ]\n---\n", []string{"a", "b"}},
		{"flow empty", "---\ntags: []\n---\n", []string{}},
		{"block", "---\ntags:\n  - a\n  - b\n---\n", []string{"a", "b"}},
		{"block quoted", "---\ntags:\n  - \"a\"\n  - 'b'\n---\n", []string{"a", "b"}},
		{"absent", "---\ntitle: x\n---\n", []string{}},
		{"no frontmatter", "# T\ntags: [a]\n", []string{}},
		{"scalar", "---\ntags: solo\n---\n", []string{"solo"}},

		// Fidelity over the old hand-rolled parser: full YAML is now
		// understood.
		{"multiline flow", "---\ntags: [a,\n b]\n---\n", []string{"a", "b"}},
		{"yaml end marker", "---\ntags: [a, b]\n...\n", []string{"a", "b"}},
		{"bom", "\xef\xbb\xbf---\ntags: [m]\n---\n", []string{"m"}},

		// Deliberate swallow contract: author-typo YAML yields empty,
		// never an error or panic.
		{"unclosed", "---\ntags: [a]\n", []string{}},
		{"invalid yaml", "---\ntags: [unclosed\n---\n", []string{}},
		{"crlf frontmatter", "---\r\ntags: [a, b]\r\n---\r\nbody\r\n", []string{"a", "b"}},
		{"crlf block", "---\r\ntags:\r\n  - c\r\n  - d\r\n---\r\n", []string{"c", "d"}},

		// Nested namespace must not leak: a `semiplane:` map's
		// tags-like key is not the note's tag set.
		{"nested semiplane no leak", "---\nsemiplane:\n  tags: [x, y]\ntags: [a, b]\n---\n", []string{"a", "b"}},
		{"nested only no leak", "---\nsemiplane:\n  tags:\n    - z\n---\n", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseTags(tc.in)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseTags = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOpenMemory(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open :memory:: %v", err)
	}
	defer func() { _ = db.Close() }()
	ix := New(db)
	if err := ix.Upsert("a.md", "# A\nhello world\n", "h", 1); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	hits, err := ix.Search("hello", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("Search = %+v, want 1 hit", hits)
	}
}
