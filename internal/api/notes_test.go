package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestNoteCreateGetRoundTrip(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	// Create branch: empty hash = create.
	resp := postJSON(t, srv.URL+"/api/v1/notes/campaign/intro.md",
		map[string]string{"content": "# Intro\n\nHello [[other]].\n", "hash": ""})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	var created NoteDoc
	decodeBody(t, resp, &created)
	if created.ETag == "" {
		t.Fatal("create response missing etag")
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatal("create response missing ETag header")
	}
	if created.Content != "# Intro\n\nHello [[other]].\n" {
		t.Fatalf("content mismatch: %q", created.Content)
	}

	// GET with role=player: fragment rendered, meta present, raw hash ETag.
	get, err := http.Get(srv.URL + "/api/v1/notes/campaign/intro.md?role=player") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	if get.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", get.StatusCode)
	}
	var doc NoteDoc
	decodeBody(t, get, &doc)
	if !strings.Contains(doc.Fragment, "Hello") {
		t.Fatalf("fragment missing body text: %q", doc.Fragment)
	}
	if doc.Meta == nil {
		t.Fatal("meta must be non-nil (empty map when no frontmatter)")
	}
	if doc.Backlinks == nil {
		t.Fatal("backlinks must be non-nil (empty slice when none)")
	}

	// ETag / If-None-Match round-trip.
	etag := get.Header.Get("ETag")
	if etag == "" {
		t.Fatal("GET missing ETag header")
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/notes/campaign/intro.md", nil)
	req.Header.Set("If-None-Match", etag)
	cached, err := http.DefaultClient.Do(req) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	_ = cached.Body.Close()
	if cached.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match status = %d, want 304", cached.StatusCode)
	}
}

func TestNoteCreateConflictWhenExists(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	url := srv.URL + "/api/v1/notes/a.md"
	resp := postJSON(t, url, map[string]string{"content": "one", "hash": ""})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create = %d, want 201", resp.StatusCode)
	}
	// Empty hash on an existing path is the create branch: must 409, never
	// silent last-write-wins (tech-stack §5).
	resp2 := postJSON(t, url, map[string]string{"content": "two", "hash": ""})
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second create = %d, want 409", resp2.StatusCode)
	}
	if ct := resp2.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("conflict content-type = %q, want problem+json", ct)
	}
}

func TestNoteUpdateBranchAndStaleHash409(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	url := srv.URL + "/api/v1/notes/a.md"
	resp := postJSON(t, url, map[string]string{"content": "v1", "hash": ""})
	var created NoteDoc
	decodeBody(t, resp, &created)

	// Update branch: non-empty hash matching disk wins (200).
	resp2 := postJSON(t, url, map[string]string{"content": "v2", "hash": created.ETag})
	var updated NoteDoc
	decodeBody(t, resp2, &updated)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d, want 200", resp2.StatusCode)
	}
	if updated.Content != "v2" {
		t.Fatalf("updated content = %q, want v2", updated.Content)
	}

	// Stale hash: the read-time precondition no longer matches disk → 409.
	resp3 := postJSON(t, url, map[string]string{"content": "v3", "hash": created.ETag})
	_ = resp3.Body.Close()
	if resp3.StatusCode != http.StatusConflict {
		t.Fatalf("stale update = %d, want 409", resp3.StatusCode)
	}
}

func TestNoteGet404IsProblemJSON(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/notes/nope/missing.md") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q, want problem+json", ct)
	}
}

func TestNoteBadRole400(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/notes/a.md?role=dm") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Either 400 (bad role, when the note exists) or 404 (missing note)
	// proves the error path; create first to pin the 400.
	if resp.StatusCode == http.StatusNotFound {
		c := postJSON(t, srv.URL+"/api/v1/notes/a.md", map[string]string{"content": "x", "hash": ""})
		_ = c.Body.Close()
		resp2, err := http.Get(srv.URL + "/api/v1/notes/a.md?role=dm") //nolint:gosec
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp2.Body.Close() }()
		if resp2.StatusCode != http.StatusBadRequest {
			t.Fatalf("bad role status = %d, want 400", resp2.StatusCode)
		}
		return
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad role status = %d, want 400", resp.StatusCode)
	}
}

func TestNoteList(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	for _, p := range []string{"a.md", "campaign/b.md"} {
		r := postJSON(t, srv.URL+"/api/v1/notes/"+p, map[string]string{"content": "# hi", "hash": ""})
		_ = r.Body.Close()
	}
	resp, err := http.Get(srv.URL + "/api/v1/notes?folder=campaign") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Notes []struct {
			Path  string `json:"path"`
			Title string `json:"title"`
		} `json:"notes"`
	}
	decodeBody(t, resp, &list)
	if len(list.Notes) != 1 || list.Notes[0].Path != "campaign/b.md" {
		t.Fatalf("folder filter = %+v, want [campaign/b.md]", list.Notes)
	}
}

func TestNoteTraversalRejected(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/notes/../evil.md") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	// Go's ServeMux cleans the path; anything outside the vault root must
	// never read vault content — accept 400/404, never 200 with content.
	if resp.StatusCode == http.StatusOK {
		t.Fatal("traversal attempt returned 200")
	}
}
