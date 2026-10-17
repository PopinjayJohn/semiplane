// The HTTP surface of the auth stream (tech-stack §3.6 Phase 1/C): the
// /api/v1/auth/* endpoints wired from the password/session/LAN-code/OIDC
// services onto a stdlib ServeMux. Errors are RFC 7807 problem+json through
// the ONE canonical path, internal/api.WriteProblem — handlers never write
// bare non-2xx statuses of their own. (Package doc lives in auth.go.)
package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/PopinjayJohn/semiplane/internal/api"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// maxBodyBytes bounds every parsed request body: none of these endpoints
// has a legitimate payload larger than a login form or a 6-digit code.
const maxBodyBytes = 4 << 10 // 4 KiB

// defaultSessionTTL applies when Deps.SessionTTL is unset.
const defaultSessionTTL = 24 * time.Hour

// Deps bundles everything Register's handlers need. Config/Provider (and
// OIDC) may all be nil: when no OIDC provider is configured, the two OIDC
// routes answer 503 problem+json ("oidc not configured") instead of
// panicking — optional machinery must 503, never nil-deref.
type Deps struct {
	Users    *Users
	Sessions *SessionService
	LanCodes *LanCodeService
	OIDC     *OIDCService

	// Provider and Config implement the OIDC flow; nil ⇒ OIDC disabled.
	// Config must carry the right Endpoint (provider.Endpoint()) and
	// RedirectURL (the callback route below).
	Provider *oidc.Provider
	Config   *oauth2.Config

	// SessionTTL is the lifetime of sessions minted by these endpoints
	// (password login, LAN join, OIDC callback). Zero ⇒ defaultSessionTTL.
	SessionTTL time.Duration

	// ForceSecure forces the Secure flag on every session cookie this
	// surface sets (CookieSecure's always-on argument; behind a proxy that
	// doesn't forward X-Forwarded-Proto).
	ForceSecure bool
}

// ttl resolves the configured (or default) session lifetime.
func (d Deps) ttl() time.Duration {
	if d.SessionTTL <= 0 {
		return defaultSessionTTL
	}
	return d.SessionTTL
}

// oidcReady reports whether the OIDC half of Deps is wired.
func (d Deps) oidcReady() bool {
	return d.OIDC != nil && d.Provider != nil && d.Config != nil
}

// Register mounts the auth endpoints on mux. Route names are FIXED — the
// Phase 1/C charter's httptest matrix targets these exact paths, and
// stream E builds on them.
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("POST /api/v1/auth/login", d.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", d.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/me", d.handleMe)
	mux.HandleFunc("POST /api/v1/auth/lan", d.handleLanCreate)
	mux.HandleFunc("POST /api/v1/auth/lan/join", d.handleLanJoin)
	mux.HandleFunc("GET /api/v1/auth/oidc/start", d.handleOIDCStart)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", d.handleOIDCCallback)
}

// handleLogin authenticates {username,password}, mints a session, sets
// the session cookie, and returns 200 {user_id, role}. Bad credentials
// and wrong-shape/malformed/oversized bodies are the two failure modes
// (401 / 400), and both are deliberately uninformative.
func (d Deps) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ident, err := d.Users.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, pluginapi.ErrUnauthorized) {
			// Same words for unknown user and wrong password: no enumeration.
			api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "invalid username or password")
			return
		}
		api.WriteProblem(w, http.StatusInternalServerError, "Login failed", "internal error")
		return
	}
	token, err := d.Sessions.Create(r.Context(), ident.ID, d.ttl())
	if err != nil {
		api.WriteProblem(w, http.StatusInternalServerError, "Login failed", "session mint failed")
		return
	}
	SetSessionCookie(w, token, CookieSecure(r, d.ForceSecure))
	writeAuthJSON(w, http.StatusOK, authBody{UserID: ident.ID, Role: ident.Role})
}

// handleLogout revokes the presented session (when there is one) and
// always clears the cookie. 204 regardless: logout is idempotent and must
// not leak whether a cookie was alive.
func (d Deps) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		// Revoke of an unknown/already-gone token is deliberately not an
		// error (session.go) — no Verify round-trip needed first.
		_ = d.Sessions.Revoke(r.Context(), c.Value)
	}
	ClearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleMe answers the identity of the session cookie holder.
func (d Deps) handleMe(w http.ResponseWriter, r *http.Request) {
	ident, ok := d.sessionIdentity(w, r)
	if !ok {
		return
	}
	writeAuthJSON(w, http.StatusOK, authBody{UserID: ident.ID, Role: ident.Role})
}

// handleLanCreate mints a join code. GM session required: players get 403,
// the anonymous 401, and an unconfigured LanCodeService a 503.
func (d Deps) handleLanCreate(w http.ResponseWriter, r *http.Request) {
	if d.LanCodes == nil {
		api.WriteProblem(w, http.StatusServiceUnavailable, "Service Unavailable", "lan codes not configured")
		return
	}
	ident, ok := d.sessionIdentity(w, r)
	if !ok {
		return
	}
	if ident.Role != "gm" {
		api.WriteProblem(w, http.StatusForbidden, "Forbidden", "gm role required")
		return
	}
	code, err := d.LanCodes.Create(r.Context(), ident.ID)
	if err != nil {
		api.WriteProblem(w, http.StatusInternalServerError, "LAN code", "code mint failed")
		return
	}
	writeAuthJSON(w, http.StatusOK, map[string]string{"code": code})
}

// handleLanJoin consumes a 6-digit code and starts a session for the code's
// owner. UNauthenticated by design (that's the point of the zero-friction
// code), rate-limited per client IP: over the limit is 429 — with a WRONG
// code and a right one alike, so brute force gains nothing.
func (d Deps) handleLanJoin(w http.ResponseWriter, r *http.Request) {
	if d.LanCodes == nil {
		api.WriteProblem(w, http.StatusServiceUnavailable, "Service Unavailable", "lan codes not configured")
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteIP = r.RemoteAddr
	}
	userID, err := d.LanCodes.Consume(r.Context(), req.Code, remoteIP)
	if err != nil {
		switch {
		case errors.Is(err, ErrRateLimited):
			api.WriteProblem(w, http.StatusTooManyRequests, "Too Many Requests", "too many join attempts; wait a minute and retry")
		case errors.Is(err, pluginapi.ErrUnauthorized):
			api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "code is invalid, expired, or already used")
		default:
			api.WriteProblem(w, http.StatusInternalServerError, "LAN join", "code consume failed")
		}
		return
	}
	token, err := d.Sessions.Create(r.Context(), userID, d.ttl())
	if err != nil {
		api.WriteProblem(w, http.StatusInternalServerError, "LAN join", "session mint failed")
		return
	}
	ident, err := d.Sessions.Verify(r.Context(), token)
	if err != nil || !validRole(ident.Role) {
		// The token was minted a line above; this is corruption insurance.
		api.WriteProblem(w, http.StatusInternalServerError, "LAN join", "session verify failed")
		return
	}
	SetSessionCookie(w, token, CookieSecure(r, d.ForceSecure))
	writeAuthJSON(w, http.StatusOK, authBody{UserID: ident.ID, Role: ident.Role})
}

// handleOIDCStart begins the Authorization Code + PKCE flow. The CSRF
// state lives server-side (oidc_states); the redirect target carries it
// to the provider and back — no extra helper cookie.
func (d Deps) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if !d.oidcReady() {
		api.WriteProblem(w, http.StatusServiceUnavailable, "Service Unavailable", "oidc not configured")
		return
	}
	_, authCodeURL, err := d.OIDC.Begin(r.Context(), d.Config)
	if err != nil {
		api.WriteProblem(w, http.StatusInternalServerError, "OIDC start", "state persistence failed")
		return
	}
	http.Redirect(w, r, authCodeURL, http.StatusFound)
}

// handleOIDCCallback completes the flow: consume state, exchange code,
// verify ID token + nonce, upsert the mapped user, mint a session, set
// the cookie, and send the browser to "/". Provider error parameters
// (user denied, provider down) and any identity failure are 401s — the
// user retries login; nothing session-shaped exists yet.
func (d Deps) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if !d.oidcReady() {
		api.WriteProblem(w, http.StatusServiceUnavailable, "Service Unavailable", "oidc not configured")
		return
	}
	q := r.URL.Query()
	if qp := q.Get("error"); qp != "" {
		api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "oidc provider error: "+qp)
		return
	}
	state, code := q.Get("state"), q.Get("code")
	if state == "" || code == "" {
		api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "missing state or code")
		return
	}
	ident, err := d.OIDC.HandleCallback(r.Context(), d.Provider, d.Config, state, code)
	if err != nil {
		if errors.Is(err, pluginapi.ErrUnauthorized) {
			api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "oidc login failed")
			return
		}
		// Exchange/verify problems are upstream (identity provider)
		// failures wearing our callback URL.
		api.WriteProblem(w, http.StatusBadGateway, "OIDC callback", "provider exchange failed")
		return
	}
	token, err := d.Sessions.Create(r.Context(), ident.ID, d.ttl())
	if err != nil {
		api.WriteProblem(w, http.StatusInternalServerError, "OIDC callback", "session mint failed")
		return
	}
	SetSessionCookie(w, token, CookieSecure(r, d.ForceSecure))
	http.Redirect(w, r, "/", http.StatusFound)
}

// sessionIdentity resolves the request's session cookie to an Identity,
// writing the 401 problem itself when there is no valid session.
func (d Deps) sessionIdentity(w http.ResponseWriter, r *http.Request) (pluginapi.Identity, bool) {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return pluginapi.Identity{}, false
	}
	ident, err := d.Sessions.Verify(r.Context(), c.Value)
	if err != nil {
		if errors.Is(err, pluginapi.ErrUnauthorized) {
			api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "invalid or expired session")
			return pluginapi.Identity{}, false
		}
		api.WriteProblem(w, http.StatusInternalServerError, "Auth", "session lookup failed")
		return pluginapi.Identity{}, false
	}
	return ident, true
}

// authBody is the {user_id, role} shape shared by login/me/lan-join.
type authBody struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// decodeJSON parses one strict JSON object out of r.Body (capped at
// maxBodyBytes). It writes the 400 problem itself and returns false when
// the body is empty, oversized, or malformed — handlers assume the happy
// path after a true return.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		api.WriteProblem(w, http.StatusBadRequest, "Bad Request", "body must be a JSON object with the expected fields")
		return false
	}
	// One object, then EOF: trailing content is malformed, not ignored.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		api.WriteProblem(w, http.StatusBadRequest, "Bad Request", "body must contain exactly one JSON object")
		return false
	}
	return true
}

// writeAuthJSON is this file's success writer (api.writeJSON is unexported,
// and Problem is the error shape, not this one).
func writeAuthJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
