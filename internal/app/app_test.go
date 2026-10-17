package app_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PopinjayJohn/semiplane/internal/app"
	"github.com/PopinjayJohn/semiplane/internal/auth"
)

// newTestServer opens the scratch index db (schema applied) to seed users,
// then builds the real app.Server over the scratch vault. Returns the server
// and the vault dir. The server's Handler() covers the full middleware chain
// (RequestID → Logging → RateLimit → Recovery → auth → routes).
func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatalf("mkdir vault: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(vaultDir, ".semiplane"), 0o755); err != nil {
		t.Fatalf("mkdir .semiplane: %v", err)
	}
	dbPath := filepath.Join(vaultDir, ".semiplane", "index.db")
	db, err := auth.Open(dbPath)
	if err != nil {
		t.Fatalf("auth.Open: %v", err)
	}
	// Seed a gm and a player.
	if err := auth.NewUsers(db).Create("gm", "gm", "gm-pass"); err != nil {
		t.Fatalf("create gm: %v", err)
	}
	if err := auth.NewUsers(db).Create("player", "player", "pw"); err != nil {
		t.Fatalf("create player: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}
	srv, err := app.NewServer(app.Config{
		VaultDir:    vaultDir,
		SessionTTL:  time.Hour,
		ForceSecure: false,
	})
	if err != nil {
		t.Fatalf("app.NewServer: %v", err)
	}
	return httptest.NewServer(srv.Handler()), vaultDir
}

// sessionCookie performs a POST login and returns the session cookie value.
func sessionCookie(t *testing.T, srv *httptest.Server, user, pass string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	resp, err := http.Post(srv.URL+"/api/v1/auth/login", "application/json", strings.NewReader(string(body))) //nolint:gosec
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName {
			return c.Value
		}
	}
	t.Fatal("no session cookie set")
	return ""
}

// withCookie returns a *http.Request carrying the session cookie.
func withCookie(t *testing.T, method, target, token string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	}
	return req
}

// mustProblem asserts RFC 7807 shape and returns the decoded problem.
func mustProblem(t *testing.T, resp *http.Response, wantStatus int) map[string]any {
	t.Helper()
	if got := resp.StatusCode; got != wantStatus {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d (body: %s)", got, wantStatus, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("content-type = %q, want application/problem+json", ct)
	}
	var p map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	return p
}

// writeCampaignNote seeds a campaign note on disk so reveal/get can target it.
func writeCampaignNote(t *testing.T, vaultDir, path, content string) {
	t.Helper()
	target := filepath.Join(vaultDir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}
}

func TestUnauthenticatedRoutesAre401(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	cases := []struct {
		method string
		target string
		body   any
	}{
		{http.MethodGet, "/api/v1/notes/a.md", nil},
		{http.MethodGet, "/api/v1/search?q=x", nil},
		{http.MethodPost, "/api/v1/reveal", map[string]string{"page": "a.md", "blockId": "b1"}},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s %s", c.method, c.target), func(t *testing.T) {
			var rd io.Reader
			if c.body != nil {
				b, _ := json.Marshal(c.body)
				rd = strings.NewReader(string(b))
			}
			req, _ := http.NewRequest(c.method, srv.URL+c.target, rd)
			if rd != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := http.DefaultClient.Do(req) //nolint:gosec
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			mustProblem(t, resp, http.StatusUnauthorized)
		})
	}
}

func TestEventsRequiresAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// Without a cookie: 401 problem+json (RequireSession short-circuits SSE).
	resp, err := http.Get(srv.URL + "/api/v1/events") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	mustProblem(t, resp, http.StatusUnauthorized)

	// With a cookie: 200 text/event-stream (stream stays open; we only assert
	// the handshake, then close which cancels the server-side context).
	gm := sessionCookie(t, srv, "gm", "gm-pass")
	client := &http.Client{Timeout: 3 * time.Second}
	req := withCookie(t, http.MethodGet, srv.URL+"/api/v1/events", gm)
	resp, err = client.Do(req) //nolint:gosec
	if err != nil {
		if strings.Contains(err.Error(), "timeout") {
			t.Fatal("events stream did not complete handshake before timeout")
		}
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("events status = %d, want 200 (body: %s)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
}

func TestLoginCookieNoteGetFlow(t *testing.T) {
	srv, vaultDir := newTestServer(t)
	defer srv.Close()

	writeCampaignNote(t, vaultDir, "campaign/x.md", "# Secret\n\n:::gm\ndragon\n:::\n")
	writeCampaignNote(t, vaultDir, "notes/welcome.md", "# Welcome\n")

	gm := sessionCookie(t, srv, "gm", "gm-pass")

	// GM gets the campaign note (200).
	resp := doWithCookie(t, srv, http.MethodGet, "/api/v1/notes/campaign/x.md?role=gm", gm)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("gm get campaign = %d, want 200 (body: %s)", resp.StatusCode, body)
	}

	// Player gets the campaign note denied (403).
	player := sessionCookie(t, srv, "player", "pw")
	resp = doWithCookie(t, srv, http.MethodGet, "/api/v1/notes/campaign/x.md?role=player", player)
	mustProblem(t, resp, http.StatusForbidden)

	// Player can read a non-campaign note.
	resp = doWithCookie(t, srv, http.MethodGet, "/api/v1/notes/notes/welcome.md?role=player", player)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("player get notes = %d, want 200 (body: %s)", resp.StatusCode, body)
	}
}

func TestPlayerReveal403GMReveal200(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// GM creates the campaign note via the API (vault + index).
	gm := sessionCookie(t, srv, "gm", "gm-pass")
	seed := doJSONWithCookie(t, srv, http.MethodPost, "/api/v1/notes/campaign/session.md",
		map[string]string{"content": "# Session\n\n:::gm\nhidden\n:::\n", "hash": ""}, gm)
	if seed.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(seed.Body)
		t.Fatalf("seed = %d, want 201 (body: %s)", seed.StatusCode, body)
	}

	player := sessionCookie(t, srv, "player", "pw")
	resp := doJSONWithCookie(t, srv, http.MethodPost, "/api/v1/reveal",
		map[string]string{"page": "campaign/session.md", "blockId": "b1"}, player)
	mustProblem(t, resp, http.StatusForbidden)

	resp = doJSONWithCookie(t, srv, http.MethodPost, "/api/v1/reveal",
		map[string]string{"page": "campaign/session.md", "blockId": "b1"}, gm)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("gm reveal = %d, want 200 (body: %s)", resp.StatusCode, body)
	}
}

func TestListSearchGraphExcludeCampaignForPlayer(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// Seed notes via the gm API (POST upserts into both vault and index).
	gm := sessionCookie(t, srv, "gm", "gm-pass")
	for _, p := range []string{"campaign/secrets.md", "notes/welcome.md"} {
		resp := doJSONWithCookie(t, srv, http.MethodPost, "/api/v1/notes/"+p,
			map[string]string{"content": "# " + p + "\n", "hash": ""}, gm)
		if resp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("seed %s = %d, want 201 (body: %s)", p, resp.StatusCode, body)
		}
	}

	player := sessionCookie(t, srv, "player", "pw")

	// List: no campaign/* paths.
	resp := doWithCookie(t, srv, http.MethodGet, "/api/v1/notes", player)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("player list = %d, want 200 (body: %s)", resp.StatusCode, body)
	}
	var list struct {
		Notes []struct {
			Path string `json:"path"`
		} `json:"notes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	for _, n := range list.Notes {
		if strings.HasPrefix(n.Path, "campaign/") {
			t.Fatalf("player list leaked campaign path: %s", n.Path)
		}
	}

	// Search: no campaign/* hits.
	resp = doWithCookie(t, srv, http.MethodGet, "/api/v1/search?q=secrets", player)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("player search = %d, want 200 (body: %s)", resp.StatusCode, body)
	}
	var search struct {
		Hits []struct {
			Path string `json:"path"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&search); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	for _, h := range search.Hits {
		if strings.HasPrefix(h.Path, "campaign/") {
			t.Fatalf("player search leaked campaign path: %s", h.Path)
		}
	}

	// Graph: no campaign/* nodes.
	resp = doWithCookie(t, srv, http.MethodGet, "/api/v1/graph", player)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("player graph = %d, want 200 (body: %s)", resp.StatusCode, body)
	}
	var graph struct {
		Nodes []struct {
			Path string `json:"path"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&graph); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	for _, n := range graph.Nodes {
		if strings.HasPrefix(n.Path, "campaign/") {
			t.Fatalf("player graph leaked campaign node: %s", n.Path)
		}
	}

	// GM sees the campaign/* paths in list, search and graph.
	gm = sessionCookie(t, srv, "gm", "gm-pass")
	assertGMSeesCampaign(t, srv, gm, "/api/v1/notes")
	assertGMSeesCampaign(t, srv, gm, "/api/v1/search?q=secrets")
	assertGMSeesCampaign(t, srv, gm, "/api/v1/graph")
}

// assertGMSeesCampaign fetches the target as gm and requires campaign/secrets.md
// to appear in the list/hits/nodes payload.
func assertGMSeesCampaign(t *testing.T, srv *httptest.Server, token, target string) {
	t.Helper()
	resp := doWithCookie(t, srv, http.MethodGet, target, token)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("gm %s = %d, want 200 (body: %s)", target, resp.StatusCode, body)
	}
	defer func() { _ = resp.Body.Close() }()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode gm %s: %v", target, err)
	}
	var paths []string
	switch v := raw["notes"].(type) {
	case []any:
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				paths = append(paths, fmt.Sprint(m["path"]))
			}
		}
	case nil:
		// hits or nodes shape
		for _, key := range []string{"hits", "nodes"} {
			if arr, ok := raw[key].([]any); ok {
				for _, e := range arr {
					if m, ok := e.(map[string]any); ok {
						paths = append(paths, fmt.Sprint(m["path"]))
					}
				}
			}
		}
	}
	found := false
	for _, p := range paths {
		if p == "campaign/secrets.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("gm %s did not include campaign/secrets.md (paths: %v)", target, paths)
	}
}

// doWithCookie performs a GET/POST without a body carrying the session cookie.
func doWithCookie(t *testing.T, srv *httptest.Server, method, target, token string) *http.Response {
	t.Helper()
	req := withCookie(t, method, srv.URL+target, token)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// doJSONWithCookie performs a JSON POST carrying the session cookie.
func doJSONWithCookie(t *testing.T, srv *httptest.Server, method, target string, body any, token string) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(method, srv.URL+target, strings.NewReader(string(raw))) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
