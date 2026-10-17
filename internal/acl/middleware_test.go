package acl

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PopinjayJohn/semiplane/internal/auth"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

func seedToken(t *testing.T, db *sql.DB, userID, scope, raw string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO tokens(id, user_id, scope, created_at, hash) VALUES(?, ?, ?, ?, ?)`,
		"tok-"+userID+"-"+scope, userID, scope, time.Now().Unix(), hashToken(raw)); err != nil {
		t.Fatalf("seed token: %v", err)
	}
}

func seedSession(t *testing.T, db *sql.DB, userID, raw string, expiresAt int64) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO sessions(id, user_id, created_at, expires_at, hash) VALUES(?, ?, ?, ?, ?)`,
		"sess-"+userID, userID, time.Now().Unix(), expiresAt, hashToken(raw)); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

// probe echoes auth success/failure and the resolved identity into headers
// for assertions.
func probe() (http.Handler, *int, *string, *string) {
	code := 0
	idOut, roleOut := "", ""
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ident, ok := IdentityFrom(r.Context())
		if ok {
			idOut, roleOut = ident.ID, ident.Role
		}
		code = http.StatusOK
	})
	return h, &code, &idOut, &roleOut
}

func run(h http.Handler, method, target string, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertProblem(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, mustNotContain ...string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Errorf("status = %d, want %d (body: %s)", rec.Code, wantStatus, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content-type = %q, want application/problem+json", ct)
	}
	var p map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body not problem+json: %v (%s)", err, rec.Body.String())
	}
	for _, frag := range mustNotContain {
		if strings.Contains(rec.Body.String(), frag) {
			t.Errorf("SECURITY: problem detail leaks %q (body: %s)", frag, rec.Body.String())
		}
	}
}

func TestTokenScope(t *testing.T) {
	db := openTestDB(t)
	seedUser(t, db, player1.ID, RolePlayer)
	seedUser(t, db, player2.ID, RolePlayer)
	seedToken(t, db, player1.ID, "characters:write", "tok-cw")
	seedToken(t, db, player1.ID, "rules:write", "tok-rw")
	seedToken(t, db, player1.ID, "rules:read", "tok-rr")
	seedToken(t, db, player1.ID, "admin", "tok-admin")
	seedToken(t, db, player1.ID, "rules:read characters:write", "tok-multi")
	seedToken(t, db, player1.ID, "weird-scope", "tok-weird")
	seedToken(t, db, "ghost", "admin", "tok-ghost")
	next, code, idOut, roleOut := probe()
	v := &Verifier{DB: db}

	for _, tc := range []struct {
		name       string
		required   string
		token      string
		wantStatus int
		wantID     string // checked only on success
	}{
		{"exact scope 200", "characters:write", "tok-cw", 200, player1.ID},
		{"broader token satisfies narrower request", "rules:read", "tok-cw", 200, player1.ID},
		{"admin satisfies characters:write", "characters:write", "tok-admin", 200, player1.ID},
		{"admin satisfies rules:read", "rules:read", "tok-admin", 200, player1.ID},
		{"multi-scope token satisfies member scope", "characters:write", "tok-multi", 200, player1.ID},
		{"insufficient scope: rules:write vs characters:write", "characters:write", "tok-rw", 401, ""},
		{"insufficient scope: rules:read vs characters:read", "characters:read", "tok-rr", 401, ""},
		{"read does not satisfy write", "characters:write", "tok-rr", 401, ""},
		{"unknown token scope satisfies nothing", "rules:read", "tok-weird", 401, ""},
		{"token without backing user row", "rules:read", "tok-ghost", 401, ""},
		{"missing Authorization header", "rules:read", "", 401, ""},
		{"unknown token value", "rules:read", "tok-nope", 401, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := v.TokenScope(tc.required)(next)
			rec := run(h, "PUT", "/api/v1/notes/rules/foo.md", tc.token)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				assertProblem(t, rec, tc.wantStatus)
				return
			}
			// Assert identity actually landed in context under the ONE key.
			if *code != http.StatusOK {
				t.Fatalf("next never ran")
			}
			if *idOut != tc.wantID {
				t.Errorf("context identity ID = %q, want %q", *idOut, tc.wantID)
			}
			if *roleOut != RolePlayer {
				t.Errorf("context identity Role = %q, want %q", *roleOut, RolePlayer)
			}
			*code = 0 // reset probe state
		})
	}
	t.Run("non-bearer Authorization header", func(t *testing.T) {
		req := httptest.NewRequest("PUT", "/api/v1/notes/rules/foo.md", nil)
		req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
		rec := httptest.NewRecorder()
		v.TokenScope("rules:read")(next).ServeHTTP(rec, req)
		assertProblem(t, rec, http.StatusUnauthorized)
	})
}

// TestTokenScopeWrongRealmIs401Not403 pins the realm decision: an
// authenticated-but-insufficiently-scoped token is 401, NEVER 403. 403 in
// this package is reserved for an established principal denied on a specific
// path by the ACL (a scope tell would leak which realm the endpoint serves
// to the wrong audience — this codebase treats that as a security tag).
func TestTokenScopeWrongRealmIs401Not403(t *testing.T) {
	db := openTestDB(t)
	seedUser(t, db, player1.ID, RolePlayer)
	seedToken(t, db, player1.ID, "rules:write", "tok-rw")
	v := &Verifier{DB: db}
	next, _, _, _ := probe()
	rec := run(v.TokenScope("characters:write")(next), "PUT", "/api/v1/notes/characters/player1/x.md", "tok-rw")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if rec.Code == http.StatusForbidden {
		t.Fatal("wrong realm answered 403 — realm leak")
	}
	assertProblem(t, rec, http.StatusUnauthorized)
}

func TestRequireSessionAndFallback(t *testing.T) {
	db := openTestDB(t)
	seedUser(t, db, player1.ID, RolePlayer)
	seedUser(t, db, player2.ID, RolePlayer)
	seedSession(t, db, player1.ID, "sess-raw", time.Now().Unix()+3600)
	seedSession(t, db, player2.ID, "sess-expired", time.Now().Unix()-3600)
	seedToken(t, db, player2.ID, "characters:write", "tok-p2")
	next, code, idOut, _ := probe()
	v := &Verifier{DB: db}
	h := v.RequireSession()(next)

	t.Run("valid session cookie", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/version", nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "sess-raw"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 || *code != 200 || *idOut != player1.ID {
			t.Errorf("session auth failed: status %d, id %q", rec.Code, *idOut)
		}
		*code = 0
	})
	t.Run("expired session cookie", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/version", nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "sess-expired"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assertProblem(t, rec, http.StatusUnauthorized)
	})
	t.Run("no header no cookie", func(t *testing.T) {
		rec := run(h, "GET", "/api/v1/version", "")
		assertProblem(t, rec, http.StatusUnauthorized)
	})
	t.Run("invalid cookie value", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/version", nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "garbage"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assertProblem(t, rec, http.StatusUnauthorized)
	})
	t.Run("bearer token without cookie wins fallback", func(t *testing.T) {
		rec := run(h, "GET", "/api/v1/version", "tok-p2")
		if rec.Code != 200 || *idOut != player2.ID {
			t.Errorf("bearer fallback failed: status %d, id %q", rec.Code, *idOut)
		}
		*code = 0
	})
	t.Run("bearer precedence: token user, not session user", func(t *testing.T) {
		// Both a valid token (player2) and a valid session (player1) are
		// present; the explicit Authorization header is authoritative.
		req := httptest.NewRequest("GET", "/api/v1/version", nil)
		req.Header.Set("Authorization", "Bearer tok-p2")
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "sess-raw"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if *idOut != player2.ID {
			t.Errorf("precedence: identity = %q, want player2 (token) not player1 (session)", *idOut)
		}
		*code = 0
	})
	t.Run("invalid bearer with valid cookie: bearer is authoritative", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/version", nil)
		req.Header.Set("Authorization", "Bearer garbage")
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "sess-raw"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assertProblem(t, rec, http.StatusUnauthorized)
	})
}

func TestRequireRole(t *testing.T) {
	next, code, _, _ := probe()
	t.Run("gm passes", func(t *testing.T) {
		h := RequireRole(RoleGM)(next)
		ctx := WithIdentity(context.Background(), gm)
		req := httptest.NewRequest("GET", "/x", nil).WithContext(ctx)
		rec2 := httptest.NewRecorder()
		h.ServeHTTP(rec2, req)
		if rec2.Code != 200 || *code != 200 {
			t.Errorf("gm blocked by RequireRole(gm): status %d", rec2.Code)
		}
		*code = 0
	})
	t.Run("player role mismatch 403", func(t *testing.T) {
		h := RequireRole(RoleGM)(next)
		ctx := WithIdentity(context.Background(), player1)
		req := httptest.NewRequest("GET", "/x", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assertProblem(t, rec, http.StatusForbidden)
	})
	t.Run("no identity 401", func(t *testing.T) {
		h := RequireRole(RoleGM)(next)
		rec := run(h, "GET", "/x", "")
		assertProblem(t, rec, http.StatusUnauthorized)
	})
}

// TestACLHandler is the END-TO-END middleware matrix: TokenScope → ACL(Map)
// → handler. SECURITY TAG below: denied paths must never appear in problem
// details.
func TestACLHandler(t *testing.T) {
	db := openTestDB(t)
	seedUser(t, db, gm.ID, RoleGM)
	seedUser(t, db, player1.ID, RolePlayer)
	seedUser(t, db, player2.ID, RolePlayer)
	seedToken(t, db, gm.ID, "admin", "tok-gm")
	seedToken(t, db, player1.ID, "characters:write", "tok-p1w")
	a := New(db)
	v := &Verifier{DB: db}
	next, code, _, _ := probe()
	handler := v.TokenScope("characters:write")(
		Handler(Map{
			"GET /api/v1/notes/": ModeRead,
			"PUT /api/v1/notes/": ModeWrite,
		}, a)(next))

	t.Run("gm allow everywhere", func(t *testing.T) {
		rec := run(handler, "PUT", "/api/v1/notes/characters/player2/x.md", "tok-gm")
		if rec.Code != 200 {
			t.Errorf("gm PUT characters/other = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		*code = 0
	})

	// player write own characters/<id>/* pass
	t.Run("player write own character", func(t *testing.T) {
		rec := run(handler, "PUT", "/api/v1/notes/characters/player1/hero.md", "tok-p1w")
		if rec.Code != 200 {
			t.Errorf("player1 PUT own = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		*code = 0
	})
	// player read own characters/*
	t.Run("player read own character", func(t *testing.T) {
		rec := run(handler, "GET", "/api/v1/notes/characters/player1/hero.md", "tok-p1w")
		if rec.Code != 200 {
			t.Errorf("player1 GET own = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		*code = 0
	})

	// SECURITY TAG: player write another player's characters/<other>/* → 403,
	// problem detail must NOT leak the target path (existence of a denied
	// path is itself sensitive).
	t.Run("player write other character: 403, path not leaked", func(t *testing.T) {
		rec := run(handler, "PUT", "/api/v1/notes/characters/player2/hero.md", "tok-p1w")
		assertProblem(t, rec, http.StatusForbidden,
			"characters/player2", "hero.md", "x.md", "/api/v1/notes/characters/player2/hero.md")
	})
	t.Run("player read campaign: 403, path not leaked", func(t *testing.T) {
		rec := run(handler, "GET", "/api/v1/notes/campaign/session-1.md", "tok-p1w")
		assertProblem(t, rec, http.StatusForbidden,
			"campaign", "session-1", "/api/v1/notes/campaign/session-1.md")
	})

	t.Run("player read campaign via traversal: 403", func(t *testing.T) {
		rec := run(handler, "GET", "/api/v1/notes/../campaign/session-1.md", "tok-p1w")
		assertProblem(t, rec, http.StatusForbidden, "session-1", "campaign/session")
	})
	t.Run("absolute-style path: 403", func(t *testing.T) {
		rec := run(handler, "GET", "/api/v1/notes//abs.md", "tok-p1w")
		assertProblem(t, rec, http.StatusForbidden, "abs.md")
	})
	t.Run("backslash path: 403", func(t *testing.T) {
		rec := run(handler, "PUT", `/api/v1/notes/characters/player1\a.md`, "tok-p1w")
		assertProblem(t, rec, http.StatusForbidden, "a.md")
	})

	t.Run("longest prefix wins", func(t *testing.T) {
		//Mechanics check for Map matching, not a policy example: the narrow
		// "GET /api/v1/notes/characters/player2/" entry demands WRITE, the
		// broad "GET /api/v1/notes/" demands READ. Player reads of another
		// player's characters are public (default policy) — so if the BROAD
		// entry won, this would be 200; the narrow entry winning makes it 403.
		h2 := v.TokenScope("characters:write")(Handler(Map{
			"GET /api/v1/notes/":                    ModeRead,
			"GET /api/v1/notes/characters/player2/": ModeWrite,
		}, a)(next))
		rec := run(h2, "GET", "/api/v1/notes/characters/player2/doc.md", "tok-p1w")
		if rec.Code != http.StatusForbidden {
			t.Errorf("narrow entry did not win: got %d, want 403 (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("no acl-matching prefix passes unchecked", func(t *testing.T) {
		h := v.TokenScope("characters:write")(
			Handler(Map{"PUT /api/v1/notes/": ModeWrite}, a)(next))
		rec := run(h, "GET", "/healthz", "tok-p1w")
		if rec.Code != 200 || *code != 200 {
			t.Errorf("unmatched prefix gatewayed: %d", rec.Code)
		}
		*code = 0
	})

	t.Run("missing identity before ACL: 401", func(t *testing.T) {
		h := Handler(Map{"GET /api/v1/notes/": ModeRead}, a)(next)
		rec := run(h, "GET", "/api/v1/notes/rules/foo.md", "")
		assertProblem(t, rec, http.StatusUnauthorized)
	})

	t.Run("problem detail is fixed string", func(t *testing.T) {
		rec := run(handler, "PUT", "/api/v1/notes/characters/player2/hero.md", "tok-p1w")
		var p map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("bad body: %v", err)
		}
		if got := fmt.Sprint(p["detail"]); got != deniedDetail {
			t.Errorf("detail = %q, want fixed %q", got, deniedDetail)
		}
	})
}

func TestScopeSatisfies(t *testing.T) {
	cases := []struct {
		token, required string
		want            bool
	}{
		{"admin", "characters:write", true},
		{"admin", "rules:read", true},
		{"characters:write", "characters:read", true},
		{"characters:write", "characters:write", true},
		{"characters:read", "characters:write", false},
		{"rules:write", "characters:write", false},
		{"rules:read", "rules:read", true},
		{"rules:read characters:write", "rules:write", true},
		{"weird", "rules:read", false},
		{"admin", "bogus:scope", false}, // unknown required scope: fail closed
		{"", "rules:read", false},
	}
	for _, c := range cases {
		if got := scopeSatisfies(c.token, c.required); got != c.want {
			t.Errorf("scopeSatisfies(%q, %q) = %v, want %v", c.token, c.required, got, c.want)
		}
	}
}

func TestHashTokenShape(t *testing.T) {
	// Credential rows store hex(SHA-256(raw)); pin the convention so the
	// auth stream's minting code cannot silently diverge.
	sum := sha256.Sum256([]byte("x"))
	want := hex.EncodeToString(sum[:])
	if got := hashToken("x"); got != want {
		t.Errorf("hashToken = %q, want %q", got, want)
	}
}

func TestHashTokenMatchesAuthStream(t *testing.T) {
	// acl reads credentials minted by internal/auth; the hash convention
	// must be literally the same function, not a parallel implementation.
	if got, want := hashToken("probe-value"), auth.HashToken("probe-value"); got != want {
		t.Errorf("acl/auth hash conventions diverge: acl %q vs auth %q", got, want)
	}
}

func TestContextKeyOne(t *testing.T) {
	// WithIdentity → IdentityFrom round-trip under the single key, with the
	// type assert failing closed for foreign values.
	ident := pluginapi.Identity{ID: "u1", Role: RolePlayer}
	got, ok := IdentityFrom(WithIdentity(context.Background(), ident))
	if !ok || got != ident {
		t.Fatalf("round-trip: (%v, %v)", got, ok)
	}
	if _, ok := IdentityFrom(context.Background()); ok {
		t.Error("empty context reported identity")
	}
}
