package api

import (
	"context"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/PopinjayJohn/semiplane/internal/index"
	"github.com/PopinjayJohn/semiplane/internal/oplog"
	"github.com/PopinjayJohn/semiplane/internal/sse"
	"github.com/PopinjayJohn/semiplane/internal/vault"
)

// facetDeps wires the curated facet vault from testdata with a scratch index
// (never inside the fixture tree) and reindexes it once.
func facetDeps(t *testing.T) *Deps {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "vaults", "facets")
	dir := t.TempDir()
	db, err := index.Open(filepath.Join(dir, ".semiplane", "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ix := index.New(db)
	if _, err := ix.Reindex(root); err != nil {
		t.Fatalf("reindex facet vault: %v", err)
	}
	return &Deps{
		Vault: vault.New(root),
		Index: ix,
		Hub:   sse.NewHub(),
		Oplog: oplog.New(filepath.Join(dir, ".semiplane", "oplog")),
	}
}

func listPaths(t *testing.T, url string) []string {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test-local
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", url, resp.StatusCode)
	}
	var list struct {
		Notes []struct {
			Path string `json:"path"`
		} `json:"notes"`
	}
	decodeBody(t, resp, &list)
	out := make([]string, 0, len(list.Notes))
	for _, n := range list.Notes {
		out = append(out, n.Path)
	}
	return out
}

func TestNoteListFacetFilter(t *testing.T) {
	srv := serve(t, facetDeps(t))
	defer srv.Close()

	cases := []struct {
		name string
		url  string
		want []string
	}{
		{
			"namespace",
			"/api/v1/notes?facet=semiplane",
			[]string{
				"rules/classes/barbarian-xphb-path-of-the-berserker-xphb.md",
				"rules/classes/barbarian-xphb.md",
				"rules/systems/dnd5e-2024/charcreation.md",
			},
		},
		{
			"charcreation",
			"/api/v1/notes?facet=semiplane.charcreation",
			[]string{"rules/systems/dnd5e-2024/charcreation.md"},
		},
		{
			"class",
			"/api/v1/notes?facet=semiplane.class",
			[]string{"rules/classes/barbarian-xphb.md"},
		},
		{
			"subclass",
			"/api/v1/notes?facet=semiplane.subclass",
			[]string{"rules/classes/barbarian-xphb-path-of-the-berserker-xphb.md"},
		},
		{
			"nested key",
			"/api/v1/notes?facet=semiplane.class.name",
			[]string{"rules/classes/barbarian-xphb.md"},
		},
		{
			"missing key",
			"/api/v1/notes?facet=semiplane.ancestry",
			[]string{},
		},
		{
			"combines with folder",
			"/api/v1/notes?facet=semiplane&folder=rules/classes/barbarian-xphb.md",
			[]string{"rules/classes/barbarian-xphb.md"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := listPaths(t, srv.URL+tc.url)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("GET %s = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestNoteListFacetInvalidIsProblemJSON(t *testing.T) {
	srv := serve(t, facetDeps(t))
	defer srv.Close()

	for _, raw := range []string{"", "other", "semiplane..class", "semiplane.class!"} {
		t.Run(raw, func(t *testing.T) {
			resp, err := http.Get(srv.URL + "/api/v1/notes?facet=" + raw) //nolint:gosec // test-local
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("facet=%q status = %d, want 400", raw, resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("facet=%q content-type = %q, want problem+json", raw, ct)
			}
		})
	}
}

func TestNoteListFacetRespectsACL(t *testing.T) {
	d := facetDeps(t)
	// Pretend the class note lives in a GM-only prefix: the facet index still
	// matches it, but the list must filter it out per item.
	d.VaultCan = func(_ context.Context, path, _ string) bool {
		return path != "rules/classes/barbarian-xphb.md"
	}
	srv := serve(t, d)
	defer srv.Close()

	got := listPaths(t, srv.URL+"/api/v1/notes?facet=semiplane.class")
	if len(got) != 0 {
		t.Fatalf("facet list leaked unreadable path: %v", got)
	}
}

func TestNoteListFacetSeesPostIndexedNote(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	content := "---\nsemiplane:\n  class:\n    name: Wizard\n    source: XPHB\n---\n# Wizard\n"
	resp := postJSON(t, srv.URL+"/api/v1/notes/rules/classes/wizard.md",
		map[string]string{"content": content, "hash": ""})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}

	got := listPaths(t, srv.URL+"/api/v1/notes?facet=semiplane.class")
	if !reflect.DeepEqual(got, []string{"rules/classes/wizard.md"}) {
		t.Fatalf("facet after POST = %v, want [rules/classes/wizard.md]", got)
	}
}
