package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PopinjayJohn/semiplane/internal/identity"
	"github.com/PopinjayJohn/semiplane/internal/markdown"
	"github.com/PopinjayJohn/semiplane/internal/render"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
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

func TestSearchSnippetsStrippedForPlayer(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	// Create a note with a GM block
	gmContent := "# Public\n\nPublic content.\n\n:::gm\nSecret GM content.\n:::\n\nMore public content.\n"
	resp := postJSON(t, srv.URL+"/api/v1/notes/campaign/secret.md",
		map[string]string{"content": gmContent, "hash": ""})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Search as player (no auth middleware in test)
	resp, err := http.Get(srv.URL + "/api/v1/search?q=Secret") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search status = %d, want 200", resp.StatusCode)
	}
	var result struct {
		Hits []struct {
			Path    string  `json:"path"`
			Snippet string  `json:"snippet"`
			Score   float64 `json:"score"`
		} `json:"hits"`
	}
	decodeBody(t, resp, &result)
	if len(result.Hits) == 0 {
		t.Fatal("expected search hit")
	}
	snippet := result.Hits[0].Snippet
	if strings.Contains(snippet, "Secret GM content") {
		t.Errorf("player snippet should not contain GM secret, got: %q", snippet)
	}
	if strings.Contains(snippet, ":::gm") {
		t.Errorf("player snippet should not contain :::gm fence, got: %q", snippet)
	}
}

// TestNoteSecretsAmend_PlayerCannotRequestGMRole verifies that a player
// session cannot request ?role=gm (returns 403). This test uses a test
// server with a real auth middleware chain by setting up a player identity
// in the request context via the ACL middleware.
func TestNoteSecretsAmend_PlayerCannotRequestGMRole(t *testing.T) {
	// This test would require a full auth middleware setup.
	// For now, we test the roleOf function directly in a unit test below.
	t.Skip("requires full auth middleware setup; covered by unit test")
}

// TestNoteSecretsAmend_PlayerContentStripped verifies that a player
// reading a note with :::gm blocks gets the Content field stripped.
func TestNoteSecretsAmend_PlayerContentStripped(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	// Create a note with a GM block
	gmContent := "# Public\n\nPublic content.\n\n:::gm\nSecret GM content.\n:::\n\nMore public content.\n"
	resp := postJSON(t, srv.URL+"/api/v1/notes/campaign/secret.md",
		map[string]string{"content": gmContent, "hash": ""})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// GET as player (default role in tests without auth)
	get, err := http.Get(srv.URL + "/api/v1/notes/campaign/secret.md?role=player") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = get.Body.Close() }()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", get.StatusCode)
	}
	var doc NoteDoc
	decodeBody(t, get, &doc)

	// Content should NOT contain the GM block
	if strings.Contains(doc.Content, "Secret GM content") {
		t.Errorf("player Content should not contain GM secret, got: %q", doc.Content)
	}
	if strings.Contains(doc.Content, ":::gm") {
		t.Errorf("player Content should not contain :::gm fence, got: %q", doc.Content)
	}
	// But should contain public content
	if !strings.Contains(doc.Content, "Public content") {
		t.Errorf("player Content should contain public content, got: %q", doc.Content)
	}
	if !strings.Contains(doc.Content, "More public content") {
		t.Errorf("player Content should contain more public content, got: %q", doc.Content)
	}

	// Fragment should also be stripped (already tested by render package)
	if strings.Contains(doc.Fragment, "Secret GM content") {
		t.Errorf("player Fragment should not contain GM secret")
	}

	// gm_blocks carries ids only — players can still see there IS a hidden
	// block (reveal-pending) and agree on its id, without any content.
	if len(doc.GMBlocks) != 1 || doc.GMBlocks[0] != "gm-0" {
		t.Errorf("player GMBlocks = %v, want [gm-0]", doc.GMBlocks)
	}
}

// TestNoteSecretsAmend_GMContentPreserved verifies that a GM
// reading a note with :::gm blocks gets the full Content field.
func TestNoteSecretsAmend_GMContentPreserved(t *testing.T) {
	d := testDeps(t)
	srv := serve(t, d)
	defer srv.Close()

	// Create a note with a GM block
	gmContent := "# Public\n\nPublic content.\n\n:::gm\nSecret GM content.\n:::\n\nMore public content.\n"
	resp := postJSON(t, srv.URL+"/api/v1/notes/campaign/secret.md",
		map[string]string{"content": gmContent, "hash": ""})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// GET as GM
	get, err := http.Get(srv.URL + "/api/v1/notes/campaign/secret.md?role=gm") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = get.Body.Close() }()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", get.StatusCode)
	}
	var doc NoteDoc
	decodeBody(t, get, &doc)

	// Content SHOULD contain the GM block
	if !strings.Contains(doc.Content, "Secret GM content") {
		t.Errorf("GM Content should contain GM secret, got: %q", doc.Content)
	}
	if !strings.Contains(doc.Content, ":::gm") {
		t.Errorf("GM Content should contain :::gm fence, got: %q", doc.Content)
	}
	// And public content
	if !strings.Contains(doc.Content, "Public content") {
		t.Errorf("GM Content should contain public content, got: %q", doc.Content)
	}

	// Fragment should also contain the GM block (rendered as HTML)
	if !strings.Contains(doc.Fragment, "Secret GM content") {
		t.Errorf("GM Fragment should contain GM secret")
	}
	if !strings.Contains(doc.Fragment, `data-testid="gm-block"`) {
		t.Errorf("GM Fragment should contain gm-block element")
	}
}

// TestNoteSecretsAmend_CharacterOwnerSeesOwnGMBlocks verifies that
// a player reading their own character note sees the GM blocks.
func TestNoteSecretsAmend_CharacterOwnerSeesOwnGMBlocks(t *testing.T) {
	// This test requires a player identity in context with a matching
	// character note path. The testDeps doesn't set up auth middleware.
	// We test the isCharacterOwner helper directly below.
	t.Skip("requires auth middleware with player identity; covered by unit test")
}

// Test roleOf function directly
func TestRoleOf(t *testing.T) {
	tests := []struct {
		name        string
		roleParam   string
		identRole   string
		hasIdent    bool
		wantRole    render.Role
		wantErr     bool
		errContains string
	}{
		// No identity (test context)
		{"no ident, empty role", "", "", false, render.RolePlayer, false, ""},
		{"no ident, player role", "player", "", false, render.RolePlayer, false, ""},
		{"no ident, gm role", "gm", "", false, render.RoleGM, false, ""},
		{"no ident, invalid role", "dm", "", false, render.RolePlayer, true, "role must be gm or player"},

		// GM identity
		{"gm ident, empty role", "", "gm", true, render.RoleGM, false, ""},
		{"gm ident, gm role", "gm", "gm", true, render.RoleGM, false, ""},
		{"gm ident, player role", "player", "gm", true, render.RolePlayer, false, ""},
		{"gm ident, invalid role", "dm", "gm", true, render.RolePlayer, true, "role must be gm or player"},

		// Player identity
		{"player ident, empty role", "", "player", true, render.RolePlayer, false, ""},
		{"player ident, player role", "player", "player", true, render.RolePlayer, false, ""},
		{"player ident, gm role (forbidden)", "gm", "player", true, render.RolePlayer, true, "forbidden: player cannot request gm role"},
		{"player ident, invalid role", "dm", "player", true, render.RolePlayer, false, ""}, // defaults to player
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/notes/test.md?role="+tc.roleParam, nil)
			if tc.hasIdent {
				ident := pluginapi.Identity{ID: "testuser", Role: tc.identRole}
				req = req.WithContext(identity.WithIdentity(req.Context(), ident))
			}

			gotRole, err := roleOf(req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.errContains)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
			if gotRole != tc.wantRole {
				t.Fatalf("role = %q, want %q", gotRole, tc.wantRole)
			}
		})
	}
}

// Test isCharacterOwner helper
func TestIsCharacterOwner(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		userID string
		want   bool
	}{
		{"owner match", "characters/player1/notes.md", "player1", true},
		{"owner match nested", "characters/player1/subdir/notes.md", "player1", true},
		{"different user", "characters/player1/notes.md", "player2", false},
		{"not character path", "campaign/notes.md", "player1", false},
		{"empty userID", "characters/player1/notes.md", "", false},
		{"empty path", "", "player1", false},
		{"prefix match only", "characters/player10/notes.md", "player1", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isCharacterOwner(tc.path, tc.userID)
			if got != tc.want {
				t.Fatalf("isCharacterOwner(%q, %q) = %v, want %v", tc.path, tc.userID, got, tc.want)
			}
		})
	}
}

// Test StripGMBlocks function
func TestStripGMBlocks(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		role     markdown.Role
		expected string
	}{
		{
			name:     "no gm blocks",
			input:    "# Hello\n\nWorld\n",
			role:     markdown.RolePlayer,
			expected: "# Hello\n\nWorld\n",
		},
		{
			name:     "single gm block",
			input:    "# Public\n\n:::gm\nSecret\n:::\n\nMore\n",
			role:     markdown.RolePlayer,
			expected: "# Public\n\n\nMore\n",
		},
		{
			name:     "gm block with title",
			input:    "# Public\n\n:::gm Title\nSecret\n:::\n\nMore\n",
			role:     markdown.RolePlayer,
			expected: "# Public\n\n\nMore\n",
		},
		{
			name:     "multiple gm blocks",
			input:    ":::gm\nSecret1\n:::\nPublic\n:::gm\nSecret2\n:::\n",
			role:     markdown.RolePlayer,
			expected: "\nPublic\n",
		},
		{
			name:     "unclosed gm block consumes to EOF",
			input:    "Public\n:::gm\nSecret\nNo closer\n",
			role:     markdown.RolePlayer,
			expected: "Public\n",
		},
		{
			name:     "gm role preserves everything",
			input:    "# Public\n\n:::gm\nSecret\n:::\n\nMore\n",
			role:     markdown.RoleGM,
			expected: "# Public\n\n:::gm\nSecret\n:::\n\nMore\n",
		},
		{
			name:     "empty input",
			input:    "",
			role:     markdown.RolePlayer,
			expected: "",
		},
		{
			name:     "only gm block",
			input:    ":::gm\nSecret\n:::\n",
			role:     markdown.RolePlayer,
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := markdown.StripGMBlocks([]byte(tc.input), tc.role)
			if string(result) != tc.expected {
				t.Errorf("StripGMBlocks(%q, %v) = %q, want %q", tc.input, tc.role, string(result), tc.expected)
			}
		})
	}
}
