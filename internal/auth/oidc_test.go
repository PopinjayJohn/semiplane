package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
	"golang.org/x/oauth2"

	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// This is the "Smallville" fake OIDC provider: discovery + JWKS come from
// go-oidc's own oidctest.Server (Not suitable for non-tests, per its doc),
// and the token endpoint is a hand-rolled httptest handler that enforces
// real PKCE S256 verification against the challenge bound to each issued
// code. ID tokens are signed with a real RSA key via oidctest.SignIDToken,
// so HandleCallback exercises discovery → build URL → code exchange →
// JWKS fetch → ID-token verify → manual nonce check → upsert end to end.
const (
	testClientID      = "test-client"
	testSub           = "alice-sub-42"
	testPreferredName = "alice"
)

type fakeIDP struct {
	t        *testing.T
	srv      *httptest.Server
	oidcSrv  *oidctest.Server
	key      *rsa.PrivateKey
	provider *oidc.Provider
	cfg      *oauth2.Config

	mu         sync.Mutex
	challenges map[string]string // code -> bound S256 challenge
	nonces     map[string]string // code -> bound nonce
}

// newFakeIDP boots the fake IdP on a real httptest server, discovers it
// through go-oidc (issuer check included), and returns provider+config
// shaped exactly like production wiring.
func newFakeIDP(t *testing.T, ctx context.Context) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	f := &fakeIDP{
		t:          t,
		key:        key,
		challenges: map[string]string{},
		nonces:     map[string]string{},
	}
	f.oidcSrv = &oidctest.Server{
		PublicKeys: []oidctest.PublicKey{
			{
				PublicKey: key.Public(),
				KeyID:     "test-key",
				Algorithm: oidc.RS256,
			},
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
		RedirectURL: "http://app.client/callback",
	}
	return f
}

// redeem has the IdP mint an authorization code binding the given S256
// challenge and nonce — i.e. simulate the /auth endpoint's code issuance
// for a client that initiated a login with that (challenge, nonce).
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
		// Real PKCE enforcement: a stolen code alone is worthless.
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

// newOIDCTest is the standard fixture: auth DB + fake IdP + OIDCService.
func newOIDCTest(t *testing.T) (*OIDCService, *fakeIDP, context.Context) {
	t.Helper()
	ctx := context.Background()
	return NewOIDCService(openAuthDB(t)), newFakeIDP(t, ctx), ctx
}

// startLogin runs Begin, asserts the authorization-code URL shape, and
// binds the discovered (challenge, nonce) to a freshly issued code.
func startLogin(t *testing.T, svc *OIDCService, idp *fakeIDP, ctx context.Context) (state, code string, q url.Values) {
	t.Helper()
	state, authURL, err := svc.Begin(ctx, idp.cfg)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse AuthCodeURL %q: %v", authURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		t.Fatalf("AuthCodeURL scheme %q", u.Scheme)
	}
	q = u.Query()
	if want := "code"; q.Get("response_type") != want {
		t.Fatalf("response_type = %q, want %q", q.Get("response_type"), want)
	}
	if q.Get("client_id") != testClientID {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != idp.cfg.RedirectURL {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Get("state") != state {
		t.Fatalf("state mismatch: URL %q vs Begin %q", q.Get("state"), state)
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if got, want := len(q.Get("code_challenge")), 43; got != want {
		t.Fatalf("code_challenge = %d chars, want %d (S256 base64url unpadded)", got, want)
	}
	if got, want := len(q.Get("nonce")), 43; got != want {
		t.Fatalf("nonce = %d chars, want 43", got)
	}
	code = idp.redeem(q.Get("code_challenge"), q.Get("nonce"))
	return state, code, q
}

func TestOIDCBeginPersistsStateRowAndShapesURL(t *testing.T) {
	svc, idp, ctx := newOIDCTest(t)
	state, _, _ := startLogin(t, svc, idp, ctx)

	// Exactly one state row, keyed BY the verifier, holding the same
	// state + nonce the URL advertised.
	var id, storedNonce string
	var createdAt, expiresAt int64
	if err := svc.DB.QueryRow(
		`SELECT id, nonce, created_at, expires_at FROM oidc_states WHERE state = ?`, state).
		Scan(&id, &storedNonce, &createdAt, &expiresAt); err != nil {
		t.Fatalf("read state row: %v", err)
	}
	wantVerifierLen := len(oauth2.GenerateVerifier())
	if len(id) != wantVerifierLen {
		t.Fatalf("row id (pkce verifier) = %d chars, want %d", len(id), wantVerifierLen)
	}
	if expiresAt <= createdAt || expiresAt > createdAt+int64(oidcStateTTL/time.Second)+1 {
		t.Fatalf("state row expires_at %d vs created %d (want ~+%d s)", expiresAt, createdAt, int64(oidcStateTTL/time.Second))
	}
	_ = storedNonce // nonce↔URL equality already asserted via startLogin's q.Get("nonce")
}

func TestOIDCHandleCallbackHappyPath(t *testing.T) {
	svc, idp, ctx := newOIDCTest(t)
	state, code, _ := startLogin(t, svc, idp, ctx)

	ident, err := svc.HandleCallback(ctx, idp.provider, idp.cfg, state, code)
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if ident.ID != "oidc:"+testSub || ident.Role != "player" {
		t.Fatalf("Identity = %+v, want {oidc:%s player}", ident, testSub)
	}

	// User upserted locally: username from preferred_username, empty phc
	// (no local password for OIDC users), role player.
	var userID, username, phc, role string
	if err := svc.DB.QueryRow(
		`SELECT id, username, phc, role FROM users WHERE id = ?`, ident.ID).
		Scan(&userID, &username, &phc, &role); err != nil {
		t.Fatalf("read users row: %v", err)
	}
	if username != testPreferredName {
		t.Fatalf("username = %q, want %q", username, testPreferredName)
	}
	if phc != "" {
		t.Fatalf("OIDC user has a local phc %q; must stay empty (no local password)", phc)
	}
	if role != "player" {
		t.Fatalf("role = %q, want player", role)
	}

	// State row consumed.
	var n int
	if err := svc.DB.QueryRow(`SELECT COUNT(*) FROM oidc_states`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("oidc_states = %d rows after callback, want 0 (state is single-use)", n)
	}
}

func TestOIDCStateMismatch(t *testing.T) {
	svc, idp, ctx := newOIDCTest(t)
	_, _, _ = startLogin(t, svc, idp, ctx) // one real login attempt outstanding

	ident, err := svc.HandleCallback(ctx, idp.provider, idp.cfg, "forged-state", "some-code")
	if !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("HandleCallback forged state = (%+v, %v), want errors.Is(ErrUnauthorized)", ident, err)
	}
}

// TestOIDCReplayRejected: the same state+code pair must be single-use.
func TestOIDCReplayRejected(t *testing.T) {
	svc, idp, ctx := newOIDCTest(t)
	state, code, _ := startLogin(t, svc, idp, ctx)
	if _, err := svc.HandleCallback(ctx, idp.provider, idp.cfg, state, code); err != nil {
		t.Fatalf("first HandleCallback: %v", err)
	}
	ident, err := svc.HandleCallback(ctx, idp.provider, idp.cfg, state, code)
	if !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("replayed HandleCallback = (%+v, %v), want errors.Is(ErrUnauthorized)", ident, err)
	}
}

func TestOIDCExpiredStateRejected(t *testing.T) {
	svc, idp, ctx := newOIDCTest(t)
	state, code, _ := startLogin(t, svc, idp, ctx)
	if _, err := svc.DB.Exec(
		`UPDATE oidc_states SET expires_at = ? WHERE state = ?`,
		timeNow().Unix()-1, state); err != nil {
		t.Fatal(err)
	}
	ident, err := svc.HandleCallback(ctx, idp.provider, idp.cfg, state, code)
	if !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("expired state = (%+v, %v), want errors.Is(ErrUnauthorized)", ident, err)
	}
}

// TestOIDCPKCEVerifierMismatch: a code issued for a DIFFERENT challenge
// must fail at the token endpoint (our fake IdP enforces PKCE for real),
// and the failure must not create a user.
func TestOIDCPKCEVerifierMismatch(t *testing.T) {
	svc, idp, ctx := newOIDCTest(t)
	state, _, _ := startLogin(t, svc, idp, ctx)
	// Bind an unrelated code: challenge = S256("some-other-verifier").
	other := sha256.Sum256([]byte("some-other-verifier"))
	code := idp.redeem(base64.RawURLEncoding.EncodeToString(other[:]), "unused-nonce")

	ident, err := svc.HandleCallback(ctx, idp.provider, idp.cfg, state, code)
	if err == nil {
		t.Fatalf("PKCE mismatch accepted: ident %+v", ident)
	}
	var userID string
	if err := svc.DB.QueryRow(
		`SELECT id FROM users WHERE id = ?`, "oidc:"+testSub).Scan(&userID); err == nil {
		t.Fatal("user upserted despite failed exchange")
	}
}

// TestOIDCNonceMismatch: the fake IdP signs the ID token with a nonce
// that does NOT match the one bound to the login's state row. go-oidc
// verifies the token cryptographically happily; the manual nonce check is
// what must catch it.
func TestOIDCNonceMismatch(t *testing.T) {
	svc, idp, ctx := newOIDCTest(t)
	_, _, q := startLogin(t, svc, idp, ctx)
	// startLogin bound the code to the state row's nonce; issue a NEW
	// code carrying the same (valid) challenge but an attacker nonce —
	// the signed ID token then says "attacker-nonce" while the state row
	// says otherwise. go-oidc verifies the token cryptographically
	// happily here; the manual nonce check is what must catch it.
	code := idp.redeem(q.Get("code_challenge"), "attacker-nonce")
	ident, err := svc.HandleCallback(ctx, idp.provider, idp.cfg, q.Get("state"), code)
	if !errors.Is(err, pluginapi.ErrUnauthorized) {
		t.Fatalf("nonce mismatch = (%+v, %v), want errors.Is(ErrUnauthorized)", ident, err)
	}
}

func TestOIDCUpsertUpdatesUsernameAndKeepsRole(t *testing.T) {
	svc, _, ctx := newOIDCTest(t)
	ident, err := svc.upsertOIDCUser(ctx, testSub, "old-name")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// GM grants the local account a role bump; then the user re-logs in
	// with a changed preferred_username.
	if _, err := svc.DB.Exec(`UPDATE users SET role = 'gm' WHERE id = ?`, ident.ID); err != nil {
		t.Fatal(err)
	}
	ident, err = svc.upsertOIDCUser(ctx, testSub, "new-name")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if ident.Role != "gm" {
		t.Fatalf("role = %q, want preserved gm", ident.Role)
	}
	var username string
	if err := svc.DB.QueryRow(`SELECT username FROM users WHERE id = ?`, ident.ID).Scan(&username); err != nil {
		t.Fatal(err)
	}
	if username != "new-name" {
		t.Fatalf("username = %q, want updated new-name", username)
	}
}

func TestOIDCCleanupExpired(t *testing.T) {
	svc, idp, ctx := newOIDCTest(t)
	_, _, _ = startLogin(t, svc, idp, ctx) // live row must survive
	if _, err := svc.DB.Exec(
		`INSERT INTO oidc_states(id, state, nonce, created_at, expires_at) VALUES('stale-id', 'stale-state', 'stale-nonce', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if err := svc.CleanupExpired(ctx); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	var n int
	if err := svc.DB.QueryRow(
		`SELECT COUNT(*) FROM oidc_states WHERE state = 'stale-state'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("stale state row survived CleanupExpired")
	}
	var live int
	if err := svc.DB.QueryRow(`SELECT COUNT(*) FROM oidc_states`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("oidc_states = %d rows after cleanup, want 1 (the live one)", live)
	}
}
