package index

import (
	"path/filepath"
	"testing"
)

func TestGraphNodesAndEdges(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ix := New(db)
	upserts := map[string]string{
		"a.md": "# A\n\nSee [[b]].\n",
		"b.md": "# B\n\nSee [[a]] and [[missing]].\n",
	}
	for path, content := range upserts {
		if err := ix.Upsert(path, content, "hash-"+path, 0); err != nil {
			t.Fatal(err)
		}
	}
	nodes, edges, err := ix.Graph()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].Path != "a.md" || nodes[1].Path != "b.md" {
		t.Fatalf("nodes = %+v, want [a.md b.md] lexical", nodes)
	}
	if nodes[0].Title != "A" {
		t.Fatalf("node title = %q, want A", nodes[0].Title)
	}
	// b→a, b→missing, a→b: three stored edges, dangling target included.
	if len(edges) != 3 {
		t.Fatalf("edges = %+v, want 3", edges)
	}
}
