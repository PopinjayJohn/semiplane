package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"
)

const (
	// SessionCookieName is the cookie carrying the raw session token.
	//
	// The name is stable because it is a contract with every browser that has
	// ever logged in, and it is visible to the user in their cookie storage.
	SessionCookieName = "sp_session"

	// sessionTokenBytes is the entropy of a session token. 32 bytes is the
	// conventional figure and, more to the point, anything less would make the
	// token worth searching rather than waiting for.
	sessionTokenBytes = 32

	// SessionCookieMaxAge is how long the session cookie lives.
	//
	// It is 30 days, and it is the cookie's Max-Age rather than an expiry the
	// session row also carries — the two are set from the same constant by the
	// handler, so the browser and the database cannot disagree about how long a
	// session lasts. Revocation is immediate through ClearSessionCookie; this is
	// only how long a browser that is never seen again keeps offering a token.
	SessionCookieMaxAge = 30 * 24 * time.Hour
)

// NewSessionToken mints a session token and returns it together with its hash.
//
// Both halves come back together, and that is the entire reason this is one
// function. A caller cannot mint a token and then forget to store its hash,
// because the hash is already in hand and the token is already the thing that
// goes into the cookie. The caller has two jobs left: insert the hash into
// `auth_sessions`, and put the token in a cookie with SetSessionCookie.
//
// The returned token must never be logged, never placed in a URL, and never
// included in an error message. See ADR 0022.
func NewSessionToken() (string, string, error) {
	raw := make([]byte, sessionTokenBytes)

	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("auth: read session token: %w", err)
	}

	// base64url without padding, so the token is a cookie value that needs no
	// quoting. A cookie is not a URL and never goes in one.
	token := base64.RawURLEncoding.EncodeToString(raw)

	return token, HashSessionToken(token), nil
}

// HashSessionToken returns the hex-encoded SHA-256 of a raw session token.
//
// This is the value `auth_sessions.token_hash` stores and its primary key. The
// raw token exists in exactly two places afterwards: the response that set the
// cookie, and the browser's cookie jar. A read of `auth_sessions` — a stolen
// backup, a stray SQL dump, a support ticket attachment — yields hashes that do
// not authenticate anyone, which is the point. ADR 0022 has the reasoning.
//
// This is a plain SHA-256 and not a password hash, which is correct for exactly
// one reason: the token is 32 bytes of crypto/rand, so there is nothing to
// brute-force and stretching would only add half a second to every authenticated
// request. The same shortcut on a human-chosen secret would be wrong.
func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}

// SetSessionCookie writes the session cookie carrying token.
//
// secure is a parameter rather than a constant because a hardcoded Secure logs
// everyone out of a self-hosted server permanently. semiplane reached over
// plain HTTP on a LAN is a real deployment, not a mistake, and a browser
// silently refusing to store its session cookie is not a failure anybody can
// diagnose from the server's log. Pass cfg.IsProduction().
func SetSessionCookie(w http.ResponseWriter, token string, secure bool) {
	//nolint:gosec // G124 cannot see that HttpOnly and SameSite are set and that
	// Secure is the caller's cfg.IsProduction(). A hardcoded Secure logs every
	// self-hoster on plain HTTP out permanently, which is not diagnosable from
	// the server's log. The attributes themselves are asserted in the tests.
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(SessionCookieMaxAge / time.Second),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie expires the session cookie.
//
// Every attribute here must match what SetSessionCookie wrote. A browser keys
// its cookie jar on name, path and domain, so a clear that omits Path or
// mismatches Domain leaves the original cookie in place and logout silently
// does nothing — the session row is gone, but the token keeps being offered and
// keeps failing, forever. TestClearSessionCookieMatchesSetSessionCookie is the
// guard; do not change one function's attributes without the other.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	//nolint:gosec // G124 cannot see that HttpOnly and SameSite are set and that
	// Secure mirrors SetSessionCookie's. See the note there; the two must agree.
	http.SetCookie(w, &http.Cookie{
		Name:  SessionCookieName,
		Value: "",
		Path:  "/",
		// Max-Age -1, not 0: -1 emits `Max-Age=0`, which every browser treats as
		// "delete this now". Zero is a valid age and would re-save the cookie
		// on some clients.
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// SessionToken returns the raw session token from the request, if there is one.
//
// A cookie that is absent, unparseable, or present but empty all report false.
// The token is returned raw — the caller hashes it with HashSessionToken and
// looks that up, because nothing else in the process is allowed to hold the raw
// value.
func SessionToken(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return "", false
	}

	// An empty value is a deleted cookie arriving late, or a hand-written one.
	// Either way there is no token, and returning the empty string with a true
	// ok would send a caller looking up the hash of nothing.
	if cookie.Value == "" {
		return "", false
	}

	return cookie.Value, true
}
