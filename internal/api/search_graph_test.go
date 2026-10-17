package api

import (
	"net/http"
	"testing"
)

func seedTwoLinkedNotes(t *testing.T, srvURL string) {
	t.Helper()
	for path, content := range map[string]string{
		"alpha.md": "# Alpha\n\nLinks to [[beta]].\n",
		"beta.md":  "# Beta\n\nLinks to [[alpha]].\n",
	} {
		r := postJSON(t, srvURL+"/api/v1/notes/"+path,
			map[string]string{"content": content, "hash": ""})
		_ = r.Body.Close()
		if r.StatusCode != http.StatusCreated {
			t.Fatalf("seed %s = %d, want 201", path, r.StatusCode)
		}
	}
}

func TestSearchFindsSeededNote(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()
	seedTwoLinkedNotes(t, srv.URL)

	resp, err := http.Get(srv.URL + "/api/v1/search?q=alpha") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Hits []struct {
			Path    string  `json:"path"`
			Snippet string  `json:"snippet"`
			Score   float64 `json:"score"`
		} `json:"hits"`
	}
	decodeBody(t, resp, &out)
	if len(out.Hits) == 0 {
		t.Fatal("search returned no hits")
	}
	found := false
	for _, h := range out.Hits {
		if h.Path == "alpha.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("alpha.md missing from hits: %+v", out.Hits)
	}
}

func TestSearchRequiresQ(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/search") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q, want problem+json", ct)
	}
}

func TestGraphNodesAndEdges(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()
	seedTwoLinkedNotes(t, srv.URL)

	resp, err := http.Get(srv.URL + "/api/v1/graph") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Nodes []struct {
			Path  string `json:"path"`
			Title string `json:"title"`
		} `json:"nodes"`
		Edges []struct {
			Src string `json:"src"`
			Dst string `json:"dst"`
		} `json:"edges"`
	}
	decodeBody(t, resp, &out)
	if len(out.Nodes) != 2 {
		t.Fatalf("nodes = %+v, want 2", out.Nodes)
	}
	if len(out.Edges) != 2 {
		t.Fatalf("edges = %+v, want alpha<->beta pair", out.Edges)
	}
}

func TestNoteBacklinksPopulated(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()
	seedTwoLinkedNotes(t, srv.URL)

	resp, err := http.Get(srv.URL + "/api/v1/notes/beta.md") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	var doc NoteDoc
	decodeBody(t, resp, &doc)
	if len(doc.Backlinks) != 1 || doc.Backlinks[0] != "alpha.md" {
		t.Fatalf("beta backlinks = %v, want [alpha.md]", doc.Backlinks)
	}
}
