// The Phase 1/C charter's httptest ACCEPTANCE MATRIX for the auth HTTP
// surface (internal/auth/http.go): every endpoint exercised through a real
// *httptest.Server against the real SQLite schema, plus the cross-package
// sections (internal/acl path ACL and token scopes) the charter demands
// live next to it. This file is package auth_test — black box, exported
// API only. The fake-OIDC-provider machinery is copied from oidc_test.go
// (package auth), whose helpers are unexported.
package auth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
	"golang.org/x/oauth2"

	"github.com/PopinjayJohn/semiplane/internal/acl"
	"github.com/PopinjayJohn/semiplane/internal/api"
	"github.com/PopinjayJohn/semiplane/internal/auth"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// ---------------------------------------------------------------------------
// Fake OIDC provider (copied minimal from internal/auth/oidc_test.go —
// helpers there are unexported and this file is package auth_test).
// ---------------------------------------------------------------------------

const (
	testClientID      = "test-client"
	testSub           = "alice-sub-42"
	testPreferredName = "alice"
)

type fakeIDP struct {
	srv      *httptest.Server
	oidcSrv  *oidctest.Server
	key      *rsa.PrivateKey
	provider *oidc.Provider
	cfg      *oauth2.Config

	mu         sync.Mutex
	challenges map[string]string // code -> bound S256 challenge
	nonces     map[string]string // code -> bound nonce
}

// newFakeIDP boots the fake IdP: discovery + JWKS from go-oidc's
// oidctest.Server, plus a hand-rolled /token endpoint enforcing REAL PKCE
// S256 verification and signing ID tokens with a real RSA key.
func newFakeIDP(t *testing.T, ctx context.Context) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	f := &fakeIDP{key: key, challenges: map[string]string{}, nonces: map[string]string{}}
	f.oidcSrv = &oidctest.Server{
		PublicKeys: []oidctest.PublicKey{
			{PublicKey: key.Public(), KeyID: "test-key", Algorithm: oidc.RS256},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", f.handleToken)
	mux.Handle("/", f.oidcSrv) // discovery + JWKS
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.oidcSrv.SetIssuer(f.srv.URL)
	provider, err := oidc.NewProvider(ctx, f.srv.URL)
	if err != nil {
		t.Fatalf("oidc.NewProvider: %v", err)
	}
	f.provider = provider
	f.cfg = &oauth2.Config{
		ClientID:    testClientID,
		Endpoint:    provider.Endpoint(),
		RedirectURL: "http://app.client/callback", // fixed after server exists
	}
	return f
}

// redeem mints an authorization code binding (challenge, nonce) — the
// stand-in for the IdP's authorization endpoint issuing a code after the
// user "logs in".
func (f *fakeIDP) redeem(challenge, nonce string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	code := fmt.Sprintf("code-%d", len(f.challenges))
	f.challenges[code] = challenge
	f.nonces[code] = nonce
	return code
}

func (f *fakeIDP) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "token endpoint expects POST", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	code := r.PostFormValue("code")
	verifier := r.PostFormValue("code_verifier")
	if r.PostFormValue("grant_type") != "authorization_code" || code == "" {
		f.grantError(w, "unsupported_grant_type", "want authorization_code with code")
		return
	}
	if got := r.PostFormValue("redirect_uri"); got != f.cfg.RedirectURL {
		f.grantError(w, "invalid_request", "redirect_uri mismatch")
		return
	}
	f.mu.Lock()
	challenge, okC := f.challenges[code]
	nonce, okN := f.nonces[code]
	f.mu.Unlock()
	if !okC || !okN {
		f.grantError(w, "invalid_grant", "unknown code")
		return
	}
	sum := sha256.Sum256([]byte(verifier))
	if subtle.ConstantTimeCompare([]byte(challenge),
		[]byte(base64.RawURLEncoding.EncodeToString(sum[:]))) != 1 {
		f.grantError(w, "invalid_grant", "PKCE verifier does not match challenge")
		return
	}
	now := time.Now()
	rawClaims := fmt.Sprintf(`{"iss":%q,"aud":%q,"sub":%q,"exp":%d,"iat":%d,"nonce":%q,"preferred_username":%q}`,
		f.srv.URL, testClientID, testSub,
		now.Add(time.Hour).Unix(), now.Add(-time.Minute).Unix(),
		nonce, testPreferredName)
	idt := oidctest.SignIDToken(f.key, "test-key", oidc.RS256, rawClaims)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "at-" + code,
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     idt,
	})
}

func (f *fakeIDP) grantError(w http.ResponseWriter, code, desc string) {
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
}

// ---------------------------------------------------------------------------
// Matrix wiring: one shared SQLite DB, servers built per concern.
// ---------------------------------------------------------------------------

const (
	gmUser = "thyme"   // gm
	p1User = "player1" // player
	p2User = "player2" // player
	gmPass = "salt-swamp"
	p1Pass = "clam"
	p2Pass = "clam2"
)

// noRedirectClient never follows redirects: the OIDC routes answer 302 and
// the matrix must inspect Location + Set-Cookie on the 302 itself.
func noRedirectClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// newMatrixDB opens the shared in-memory auth DB (auth.Open(":memory:")
// maps to the file:memdb shared-cache DSN) and seeds the charter's users.
// All subtests share it — they are assertions over ONE wired deployment,
// exactly like the live binary.
func newMatrixDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := auth.Open(":memory:")
	if err != nil {
		t.Fatalf("auth.Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mustUser(t, db, gmUser, "gm", gmPass)
	mustUser(t, db, p1User, "player", p1Pass)
	mustUser(t, db, p2User, "player", p2Pass)
	return db
}

func mustUser(t *testing.T, db *sql.DB, id, role, pass string) {
	t.Helper()
	if err := auth.NewUsers(db).Create(id, role, pass); err != nil {
		t.Fatalf("Users.Create(%q, %q): %v", id, role, err)
	}
}

// newAuthServer builds a server with auth.Register over fresh Deps.
// mutate adjusts Deps BEFORE mounting (e.g. to wire the fake IdP).
func newAuthServer(t *testing.T, db *sql.DB, mutate func(*auth.Deps)) *httptest.Server {
	t.Helper()
	d := auth.Deps{
		Users:      auth.NewUsers(db),
		Sessions:   auth.NewSessionService(db),
		LanCodes:   auth.NewLanCodeService(db),
		SessionTTL: time.Hour,
	}
	if mutate != nil {
		mutate(&d)
	}
	mux := http.NewServeMux()
	auth.Register(mux, d)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// doJSON performs one request with the matrix's no-redirect client.
func doJSON(t *testing.T, client *http.Client, method, target string, body any, header http.Header) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, target, rd)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// login issues a password login and returns the response (caller closes /
// inspects cookies). Helper around POST {username,password}.
func login(t *testing.T, client *http.Client, base, user, pass string, header http.Header) *http.Response {
	t.Helper()
	return doJSON(t, client, http.MethodPost, base+"/api/v1/auth/login",
		map[string]string{"username": user, "password": pass}, header)
}

// mustCookie finds the session cookie in a response's Set-Cookie headers.
func mustCookie(t *testing.T, resp *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q Set-Cookie in response", name)
	return nil
}

// mustProblem asserts RFC 7807 shape and decodes the problem body.
func mustProblem(t *testing.T, resp *http.Response, wantStatus int) api.Problem {
	t.Helper()
	if got := resp.StatusCode; got != wantStatus {
		t.Fatalf("status = %d, want %d", got, wantStatus)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	var p api.Problem
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatalf("decode problem body: %v", err)
	}
	if p.Status != wantStatus {
		t.Fatalf("problem.status = %d, want %d", p.Status, wantStatus)
	}
	return p
}

// writeToken seeds one tokens row the way the acl package's convention
// hashes credentials (hex sha256, internal/acl Verifier verifyBearer).
func seedToken(t *testing.T, db *sql.DB, id, userID, scope, raw string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO tokens(id, user_id, scope, created_at, hash) VALUES(?, ?, ?, ?, ?)`,
		id, userID, scope, time.Now().Unix(), auth.HashToken(raw))
	if err != nil {
		t.Fatalf("seed token %q: %v", id, err)
	}
}

// absentCode returns a 6-digit code whose hash provably has no lan_codes
// row (the mints are crypto-random; a 1-in-a-million accidental match
// must not make the "wrong code" test nondeterministic).
func absentCode(t *testing.T, db *sql.DB) string {
	t.Helper()
	for _, c := range []string{"000000", "111111", "222222", "333333"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM lan_codes WHERE hash = ?`, auth.HashToken(c)).Scan(&n); err != nil {
			t.Fatalf("check lan_codes hash: %v", err)
		}
		if n == 0 {
			return c
		}
	}
	t.Fatal("all wrong-code candidates collide with live rows")
	return ""
}

// seedLanCode inserts a lan_codes row for userID whose plaintext is code —
// the deterministic stand-in for "GM minted this exact code".
func seedLanCode(t *testing.T, db *sql.DB, code, userID string) {
	t.Helper()
	now := time.Now().Unix()
	_, err := db.Exec(`INSERT INTO lan_codes(id, user_id, created_at, expires_at, used, hash) VALUES(?, ?, ?, ?, 0, ?)`,
		"lc-"+code, userID, now, now+600, auth.HashToken(code))
	if err != nil {
		t.Fatalf("seed lan code: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The matrix.
// ---------------------------------------------------------------------------

func TestAuthHTTPMatrix(t *testing.T) {
	ctx := context.Background()
	db := newMatrixDB(t)
	client := noRedirectClient()
	main := newAuthServer(t, db, nil)

	t.Run("schema tables exist", func(t *testing.T) {
		// Probe sqlite_master (rows may legitimately be empty; a leaked
		// *sql.Rows via db.Query would pin the pool's single conn —
		// QueryRow keeps the lifecycle self-contained).
		for _, table := range []string{"users", "sessions", "tokens", "lan_codes", "oidc_states", "acls"} {
			var name string
			if err := db.QueryRow(
				`SELECT name FROM sqlite_master WHERE name = ? AND type = 'table'`, table).
				Scan(&name); err != nil {
				t.Fatalf("table %s missing/broken: %v", table, err)
			}
		}
	})

	t.Run("login happy path + cookie flags", func(t *testing.T) {
		resp := login(t, client, main.URL, p1User, p1Pass, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var body map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode login body: %v", err)
		}
		if body["user_id"] != p1User || body["role"] != "player" {
			t.Fatalf("login body = %v, want user_id player1 + role player", body)
		}
		c := mustCookie(t, resp, auth.SessionCookieName)
		// Raw Set-Cookie field assertions (parsed resp.Cookies()).
		if !c.HttpOnly {
			t.Fatal("session cookie HttpOnly = false")
		}
		if c.SameSite != http.SameSiteLaxMode {
			t.Fatalf("SameSite = %v, want Lax", c.SameSite)
		}
		if c.Path != "/" {
			t.Fatalf("Path = %q, want /", c.Path)
		}
		if c.Secure {
			t.Fatal("Secure = true without X-Forwarded-Proto: https")
		}
		if c.MaxAge != 0 {
			t.Fatalf("Max-Age = %d, want 0 (session cookie, cleared only via logout)", c.MaxAge)
		}
		// Behind a TLS-terminating proxy the Secure flag must flip on.
		resp2 := login(t, client, main.URL, p1User, p1Pass,
			http.Header{"X-Forwarded-Proto": []string{"https"}})
		c2 := mustCookie(t, resp2, auth.SessionCookieName)
		if !c2.Secure {
			t.Fatal("Secure = false with X-Forwarded-Proto: https")
		}
		if !c2.HttpOnly || c2.SameSite != http.SameSiteLaxMode || c2.Path != "/" {
			t.Fatalf("XFP cookie flags changed alongside Secure: %+v", c2)
		}
	})

	t.Run("login wrong password and unknown user are identical 401s", func(t *testing.T) {
		resp := login(t, client, main.URL, p1User, "wrong-password", nil)
		pWrong := mustProblem(t, resp, http.StatusUnauthorized)
		resp2 := login(t, client, main.URL, "nobody-here", "wrong-password", nil)
		p2 := mustProblem(t, resp2, http.StatusUnauthorized)
		// Same verdict, same body shape: no account enumeration.
		if p2.Title != pWrong.Title || resp.StatusCode != resp2.StatusCode {
			t.Fatalf("unknown-user problem differs from wrong-password: %+v vs %+v", p2, pWrong)
		}
	})

	t.Run("login wrong-shape body is 400", func(t *testing.T) {
		url := main.URL + "/api/v1/auth/login"
		// Numeric username: JSON, but the wrong shape.
		resp := doJSON(t, client, http.MethodPost, url, map[string]any{"username": 7, "password": "x"}, nil)
		mustProblem(t, resp, http.StatusBadRequest)
		// Empty body.
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(""))
		if err != nil {
			t.Fatalf("build empty-body request: %v", err)
		}
		resp2, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		mustProblem(t, resp2, http.StatusBadRequest)
		// Oversized body (>4 KiB) rejected by MaxBytesReader.
		big := strings.Repeat("a", 8<<10)
		resp3 := doJSON(t, client, http.MethodPost, url, map[string]string{"username": big, "password": "x"}, nil)
		mustProblem(t, resp3, http.StatusBadRequest)
	})

	t.Run("me with and without session", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodGet, main.URL+"/api/v1/auth/me", nil, nil)
		mustProblem(t, resp, http.StatusUnauthorized)

		c := mustCookie(t, login(t, client, main.URL, p2User, p2Pass, nil), auth.SessionCookieName)
		resp2 := doJSON(t, client, http.MethodGet, main.URL+"/api/v1/auth/me", nil,
			http.Header{"Cookie": []string{c.Name + "=" + c.Value}})
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp2.StatusCode)
		}
		var body map[string]string
		if err := json.NewDecoder(resp2.Body).Decode(&body); err != nil {
			t.Fatalf("decode me body: %v", err)
		}
		if body["user_id"] != p2User || body["role"] != "player" {
			t.Fatalf("me body = %v, want player2/player", body)
		}
	})

	t.Run("logout revokes the cookie's session", func(t *testing.T) {
		c := mustCookie(t, login(t, client, main.URL, p1User, p1Pass, nil), auth.SessionCookieName)
		cookieHdr := http.Header{"Cookie": []string{c.Name + "=" + c.Value}}
		resp := doJSON(t, client, http.MethodPost, main.URL+"/api/v1/auth/logout", nil, cookieHdr)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("logout status = %d, want 204", resp.StatusCode)
		}
		// The old token is dead server-side: /me with it is 401.
		resp2 := doJSON(t, client, http.MethodGet, main.URL+"/api/v1/auth/me", nil, cookieHdr)
		mustProblem(t, resp2, http.StatusUnauthorized)
		// Logout is idempotent (no valid session left) — still 204.
		resp3 := doJSON(t, client, http.MethodPost, main.URL+"/api/v1/auth/logout", nil, cookieHdr)
		if resp3.StatusCode != http.StatusNoContent {
			t.Fatalf("second logout status = %d, want 204", resp3.StatusCode)
		}
	})

	t.Run("lan: gm-only create, join, single-use, wrong code", func(t *testing.T) {
		// Anonymous and player refer requests are refused.
		resp := doJSON(t, client, http.MethodPost, main.URL+"/api/v1/auth/lan", nil, nil)
		mustProblem(t, resp, http.StatusUnauthorized)
		pc := mustCookie(t, login(t, client, main.URL, p1User, p1Pass, nil), auth.SessionCookieName)
		resp = doJSON(t, client, http.MethodPost, main.URL+"/api/v1/auth/lan", nil,
			http.Header{"Cookie": []string{pc.Name + "=" + pc.Value}})
		mustProblem(t, resp, http.StatusForbidden)

		// GM mints a code through the endpoint.
		gc := mustCookie(t, login(t, client, main.URL, gmUser, gmPass, nil), auth.SessionCookieName)
		gmHdr := http.Header{"Cookie": []string{gc.Name + "=" + gc.Value}}
		resp = doJSON(t, client, http.MethodPost, main.URL+"/api/v1/auth/lan", nil, gmHdr)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("create status = %d, want 200", resp.StatusCode)
		}
		var created map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			t.Fatalf("decode lan create body: %v", err)
		}
		if len(created["code"]) != 6 {
			t.Fatalf("code = %q, want 6 digits", created["code"])
		}

		// Wrong (provably absent) code → 401.
		resp = doJSON(t, client, http.MethodPost, main.URL+"/api/v1/auth/lan/join",
			map[string]string{"code": absentCode(t, db)}, nil)
		mustProblem(t, resp, http.StatusUnauthorized)

		// Join the GM-minted code → 200 + a working session cookie.
		resp = doJSON(t, client, http.MethodPost, main.URL+"/api/v1/auth/lan/join",
			map[string]string{"code": created["code"]}, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("join status = %d, want 200", resp.StatusCode)
		}
		jc := mustCookie(t, resp, auth.SessionCookieName)
		jHdr := http.Header{"Cookie": []string{jc.Name + "=" + jc.Value}}
		resp = doJSON(t, client, http.MethodGet, main.URL+"/api/v1/auth/me", nil, jHdr)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("joined-session /me = %d, want 200", resp.StatusCode)
		}

		// A seeded code owned by player1 joins AS player1 (player role).
		seedLanCode(t, db, "271828", p1User)
		resp = doJSON(t, client, http.MethodPost, main.URL+"/api/v1/auth/lan/join",
			map[string]string{"code": "271828"}, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("player1 join status = %d, want 200", resp.StatusCode)
		}
		var joined map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&joined); err != nil {
			t.Fatalf("decode join body: %v", err)
		}
		if joined["user_id"] != p1User || joined["role"] != "player" {
			t.Fatalf("join body = %v, want player1/player", joined)
		}
		p1c := mustCookie(t, resp, auth.SessionCookieName)

		// Single-use: the SAME code again → 401, even with the joiner's
		// fresh cookie attached.
		resp = doJSON(t, client, http.MethodPost, main.URL+"/api/v1/auth/lan/join",
			map[string]string{"code": "271828"},
			http.Header{"Cookie": []string{p1c.Name + "=" + p1c.Value}})
		mustProblem(t, resp, http.StatusUnauthorized)
	})

	t.Run("lan join rate limit: wrong AND right codes blocked at 11th try", func(t *testing.T) {
		// A fresh LanCodeService = fresh per-IP limiter (in-process state,
		// see lantoken.go): the matrix's earlier joins must not pre-burn
		// this window.
		rl := newAuthServer(t, db, func(d *auth.Deps) {
			d.LanCodes = auth.NewLanCodeService(db)
		})
		wrong := absentCode(t, db)
		// 10 attempts are allowed through (each a 401: wrong code).
		for i := 0; i < 10; i++ {
			resp := doJSON(t, client, http.MethodPost, rl.URL+"/api/v1/auth/lan/join",
				map[string]string{"code": wrong}, nil)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("wrong-code try %d: status = %d, want 401", i+1, resp.StatusCode)
			}
		}
		// The 11th wrong-code try trips the limiter → 429.
		resp := doJSON(t, client, http.MethodPost, rl.URL+"/api/v1/auth/lan/join",
			map[string]string{"code": wrong}, nil)
		mustProblem(t, resp, http.StatusTooManyRequests)
		// A RIGHT code from the same (now blocked) address is ALSO 429:
		// limiter.allow runs before the hash comparison (lantoken.go), so
		// brute force cannot even keep testing guesses — valid or not.
		seedLanCode(t, db, "314159", p2User)
		resp = doJSON(t, client, http.MethodPost, rl.URL+"/api/v1/auth/lan/join",
			map[string]string{"code": "314159"}, nil)
		mustProblem(t, resp, http.StatusTooManyRequests)
		// Control: a brand-new LanCodeService (fresh limiter state) serves
		// the same code fine — proving the 429s came from per-IP limiter
		// state, not from the code itself.
		fresh := newAuthServer(t, db, func(d *auth.Deps) {
			d.LanCodes = auth.NewLanCodeService(db)
		})
		resp = doJSON(t, client, http.MethodPost, fresh.URL+"/api/v1/auth/lan/join",
			map[string]string{"code": "314159"}, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("fresh-limiter valid join = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("acl cross-user: own write ok, other's write 403, campaign 403", func(t *testing.T) {
		acls := acl.New(db)
		// acl.Map keys are MOUNT prefixes, not vault segments (see
		// middleware.go docs + acl's own tests): the vault path the ACL
		// sees is the request path with the mount prefix stripped, so
		// "characters/<user>/*" stays intact.
		modes := acl.Map{
			"GET /api/v1/notes/": acl.ModeRead,
			"PUT /api/v1/notes/": acl.ModeWrite,
		}
		reached := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("reached"))
		})
		// Player1's session -> ACL gate -> reached handler, on a TEST mux
		// (identity injected via WithIdentity; the auth mux never sees
		// vault paths).
		build := func(ident pluginapi.Identity) *httptest.Server {
			mux := http.NewServeMux()
			mux.Handle("/", withIdentity(ident, acl.Handler(modes, acls)(reached)))
			ts := httptest.NewServer(mux)
			t.Cleanup(ts.Close)
			return ts
		}
		srv := build(pluginapi.Identity{ID: p1User, Role: "player"})

		// Own character file: allowed through to the next handler.
		resp := doJSON(t, client, http.MethodPut, srv.URL+"/api/v1/notes/characters/player1/hero.md", nil, nil)
		if resp.StatusCode != http.StatusOK {
			bb, _ := io.ReadAll(resp.Body)
			t.Fatalf("own write = %d, want 200 (headers %v, body %s)", resp.StatusCode, resp.Header, bb)
		}
		b, _ := io.ReadAll(resp.Body)
		if !bytes.Contains(b, []byte("reached")) {
			t.Fatalf("next handler not reached, body %q", b)
		}

		// Someone else's character file: 403 whose detail NEVER names
		// the target path (denied path existence is sensitive).
		resp = doJSON(t, client, http.MethodPut, srv.URL+"/api/v1/notes/characters/player2/hero.md", nil, nil)
		p := mustProblem(t, resp, http.StatusForbidden)
		deniedBody, _ := json.Marshal(p)
		if strings.Contains(string(deniedBody), "player2") || strings.Contains(string(deniedBody), "hero.md") {
			t.Fatalf("403 problem leaks target path: %s", deniedBody)
		}

		// campaign/ is GM-only: player read is 403 for the same reason.
		resp = doJSON(t, client, http.MethodGet, srv.URL+"/api/v1/notes/campaign/secrets.md", nil, nil)
		mustProblem(t, resp, http.StatusForbidden)
	})

	t.Run("token scope: characters:write required", func(t *testing.T) {
		for _, s := range []struct{ id, raw, scope string }{
			{"tok-rules", "matrix-raw-rules-token", "rules:write"},
			{"tok-chars", "matrix-raw-chars-token", "characters:write"},
			{"tok-admin", "matrix-raw-admin-token", "admin"},
		} {
			seedToken(t, db, s.id, p1User, s.scope, s.raw)
		}
		// The hander answers with the resolved identity so the matrix
		// sees both the scope verdict AND who came through.
		v := &acl.Verifier{DB: db}
		final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ident, ok := acl.IdentityFrom(r.Context())
			if !ok {
				t.Error("TokenScope middleware did not set identity")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "%s/%s", ident.ID, ident.Role)
		})
		mux := http.NewServeMux()
		mux.Handle("/protected", v.TokenScope("characters:write")(final))
		ts := httptest.NewServer(mux)
		t.Cleanup(ts.Close)

		// Scope rules:write < characters:write → 401 (realm failure).
		resp := doJSON(t, client, http.MethodGet, ts.URL+"/protected", nil,
			http.Header{"Authorization": []string{"Bearer matrix-raw-rules-token"}})
		mustProblem(t, resp, http.StatusUnauthorized)

		// Exact scope → 200 with identity.
		resp = doJSON(t, client, http.MethodGet, ts.URL+"/protected", nil,
			http.Header{"Authorization": []string{"Bearer matrix-raw-chars-token"}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("characters:write token = %d, want 200", resp.StatusCode)
		}
		b, _ := io.ReadAll(resp.Body)
		if !bytes.Contains(b, []byte(p1User)) || !bytes.Contains(b, []byte("player")) {
			t.Fatalf("identity not resolved: %s", b)
		}

		// admin outranks characters:write → 200.
		resp = doJSON(t, client, http.MethodGet, ts.URL+"/protected", nil,
			http.Header{"Authorization": []string{"Bearer matrix-raw-admin-token"}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("admin token = %d, want 200", resp.StatusCode)
		}

		// Missing Authorization header → 401 problem.
		resp = doJSON(t, client, http.MethodGet, ts.URL+"/protected", nil, nil)
		mustProblem(t, resp, http.StatusUnauthorized)
	})

	t.Run("oidc unconfigured: 503 problem on both routes", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodGet, main.URL+"/api/v1/auth/oidc/start", nil, nil)
		mustProblem(t, resp, http.StatusServiceUnavailable)
		resp = doJSON(t, client, http.MethodGet, main.URL+"/api/v1/auth/oidc/callback?state=x&code=y", nil, nil)
		mustProblem(t, resp, http.StatusServiceUnavailable)
	})

	t.Run("oidc full loop against fake provider", func(t *testing.T) {
		idp := newFakeIDP(t, ctx)
		oidcSrv := newAuthServer(t, db, func(d *auth.Deps) {
			d.OIDC = auth.NewOIDCService(db)
			d.Provider = idp.provider
			d.Config = idp.cfg
		})
		// The OAuth client's redirect_uri IS this server's callback route.
		idp.cfg.RedirectURL = oidcSrv.URL + "/api/v1/auth/oidc/callback"

		// start: 302 to the provider with state + S256 challenge + nonce.
		resp := doJSON(t, client, http.MethodGet, oidcSrv.URL+"/api/v1/auth/oidc/start", nil, nil)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("start = %d, want 302", resp.StatusCode)
		}
		loc, err := resp.Location()
		if err != nil {
			t.Fatalf("Location parse: %v", err)
		}
		q := loc.Query()
		if q.Get("response_type") != "code" {
			t.Fatalf("response_type = %q", q.Get("response_type"))
		}
		if q.Get("client_id") != testClientID {
			t.Fatalf("client_id = %q", q.Get("client_id"))
		}
		if q.Get("code_challenge_method") != "S256" {
			t.Fatalf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
		}
		if n := len(q.Get("code_challenge")); n != 43 {
			t.Fatalf("code_challenge = %d chars, want 43", n)
		}
		if n := len(q.Get("nonce")); n != 43 {
			t.Fatalf("nonce = %d chars, want 43", n)
		}
		if q.Get("state") == "" {
			t.Fatal("state empty in authorization URL")
		}
		if q.Get("redirect_uri") != idp.cfg.RedirectURL {
			t.Fatalf("redirect_uri = %q, want %q", q.Get("redirect_uri"), idp.cfg.RedirectURL)
		}

		// The "user logs into the provider": mint a code bound to the
		// advertised challenge + nonce, then hit OUR callback with it.
		// The fake /token endpoint enforces PKCE against the state row's
		// stored verifier end to end.
		code := idp.redeem(q.Get("code_challenge"), q.Get("nonce"))
		cb := func(state, c string) *http.Response {
			return doJSON(t, client, http.MethodGet,
				oidcSrv.URL+"/api/v1/auth/oidc/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(c), nil, nil)
		}
		resp = cb(q.Get("state"), code)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("callback = %d, want 302", resp.StatusCode)
		}
		if l2 := resp.Header.Get("Location"); l2 != "/" {
			t.Fatalf("callback Location = %q, want /", l2)
		}
		// The minted session cookie works on /me as the OIDC user.
		sc := mustCookie(t, resp, auth.SessionCookieName)
		resp = doJSON(t, client, http.MethodGet, oidcSrv.URL+"/api/v1/auth/me", nil,
			http.Header{"Cookie": []string{sc.Name + "=" + sc.Value}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("oidc /me = %d, want 200", resp.StatusCode)
		}
		var me map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
			t.Fatalf("decode me: %v", err)
		}
		if me["user_id"] != "oidc:"+testSub || me["role"] != "player" {
			t.Fatalf("oidc /me body = %v, want oidc:%s/player", me, testSub)
		}

		t.Run("state mismatch is 401", func(t *testing.T) {
			// A second, independent login attempt…
			resp := doJSON(t, client, http.MethodGet, oidcSrv.URL+"/api/v1/auth/oidc/start", nil, nil)
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("second start = %d, want 302", resp.StatusCode)
			}
			q2, _ := resp.Location()
			qv := q2.Query()
			code := idp.redeem(qv.Get("code_challenge"), qv.Get("nonce"))
			// … presented with a forged state.
			resp = cb("forged-state-xyz", code)
			mustProblem(t, resp, http.StatusUnauthorized)
		})

		t.Run("replay of same state+code is 401", func(t *testing.T) {
			// The HAPPY path's state row was consumed above; presenting
			// the identical pair again must not mint a second session
			// (the state store is single-use inside BEGIN IMMEDIATE).
			resp := cb(q.Get("state"), code)
			mustProblem(t, resp, http.StatusUnauthorized)
		})
	})
}

// withIdentity injects a fixed Identity into the request context (the
// matrix's stand-in for RequireSession) before handing to the ACL chain.
func withIdentity(ident pluginapi.Identity, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(acl.WithIdentity(r.Context(), ident)))
	})
}
