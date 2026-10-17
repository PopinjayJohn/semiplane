package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/PopinjayJohn/semiplane/internal/index"
	"github.com/PopinjayJohn/semiplane/internal/oplog"
	"github.com/PopinjayJohn/semiplane/internal/sse"
	"github.com/PopinjayJohn/semiplane/internal/vault"
)

// testDeps builds fully wired deps on scratch dirs: vault + index on
// t.TempDir(), never the real tree (AGENTS.md: vault isolation per test).
func testDeps(t *testing.T) *Deps {
	t.Helper()
	dir := t.TempDir()
	db, err := index.Open(filepath.Join(dir, ".semiplane", "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Deps{
		Vault: vault.New(dir),
		Index: index.New(db),
		Hub:   sse.NewHub(),
		Oplog: oplog.New(filepath.Join(dir, ".semiplane", "oplog")),
	}
}

func serve(t *testing.T, d *Deps) *httptest.Server {
	t.Helper()
	return httptest.NewServer(NewMuxWithDeps(d))
}

func decodeBody(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw)) //nolint:gosec // test-local
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
