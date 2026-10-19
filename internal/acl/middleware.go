// Middleware half of the acl package: context identity plumbing plus
// net/http middleware for token scopes, session fallback auth, role checks,
// and path-prefix ACL enforcement.
//
// CONTEXT KEY DECISION (one, on purpose): identity travels in request
// contexts under the single unexported type ctxKeyIdentity below. Exposing
// the raw key type would let callers store identities under their own keys
// and fork the convention; instead everything goes through the exported
// WithIdentity / IdentityFrom pair, so E's handlers (and every future
// middleware) read identity the same one way.
//
// SCOPE HIERARCHY (documented shape decision): token scopes form one linear
// rank, broadest-wins — rules:read < rules:write < characters:read <
// characters:write < admin. A token whose stored scope outranks the
// required scope satisfies it (admin satisfies characters:write; a token
// holding only rules:read does not satisfy characters:write). The columns
// are a single TEXT scope value, but the comparison accepts a
// space-separated list as the union of its members (single-scope tokens are
// just one-element lists; unknown scope names rank 0 and satisfy nothing).
// "Smallest sufficient" applies when MINTING tokens: pick the least-ranked
// scope that still satisfies the client's needs.
//
// REALM SEMANTICS: a request that fails the scope check is 401, not 403.
// Scope says nothing about a path — it says the principal never proved it
// belongs to this realm/audience; 403 is reserved for authenticated
// principals denied on a specific vault path by the ACL.
package acl

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/PopinjayJohn/semiplane/internal/api"
	"github.com/PopinjayJohn/semiplane/internal/auth"
	"github.com/PopinjayJohn/semiplane/internal/identity"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// Re-export the canonical identity context functions from internal/identity
// to break the import cycle between internal/api and internal/acl.
var (
	WithIdentity = identity.WithIdentity
	IdentityFrom = identity.IdentityFrom
)

// Verifier performs DB-backed credential lookups against the sessions and
// tokens tables. Credentials are stored hashed; see hashToken for the
// hash convention.
type Verifier struct {
	DB *sql.DB
}

// SessionCookieName aliases the ONE canonical session cookie name from the
// auth stream (constant expression, no runtime dep): middleware that sets
// and middleware that reads the cookie must never disagree. HttpOnly,
// Secure behind TLS, SameSite=Lax.
const SessionCookieName = auth.SessionCookieName

// hashToken is the credential-hash convention for tokens/sessions rows:
// hex(SHA-256(raw)), aliased from the auth stream so both packages hash
// identically (opaque random server-side values; passwords are Argon2id
// in the auth stream and never flow through here).
func hashToken(raw string) string { return auth.HashToken(raw) }

// bearer extracts "<token>" from an "Authorization: Bearer <token>" header,
// returning "" when absent or not bearer-formatted.
func bearer(header string) string {
	if header == "" {
		return ""
	}
	auth, val, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(auth, "Bearer") {
		return ""
	}
	return strings.TrimSpace(val)
}

// scopeRank is the linear scope hierarchy (see package doc). Absent names
// rank 0 and satisfy nothing.
var scopeRank = map[string]int{
	"rules:read":       1,
	"rules:write":      2,
	"characters:read":  3,
	"characters:write": 4,
	"admin":            5,
}

// scopeSatisfies reports whether the token's scope value (possibly a
// space-separated list) contains at least one scope outranking required.
// An empty required scope means "any valid token" and is satisfied by
// authentication alone.
func scopeSatisfies(tokenScope, required string) bool {
	if required == "" {
		return true
	}
	reqRank, ok := scopeRank[required]
	if !ok {
		return false // unknown required scope: fail closed
	}
	for _, s := range strings.Fields(tokenScope) {
		if r, ok := scopeRank[s]; ok && r >= reqRank {
			return true
		}
	}
	return false
}

// verifyBearer authenticates raw against the tokens table with the required
// scope and resolves the owning user from the users table. Unknown
// credentials, unknown users, and insufficient scope all yield an error
// (mapped to 401 by callers — never 403).
func (v *Verifier) verifyBearer(ctx context.Context, raw, required string) (pluginapi.Identity, error) {
	if raw == "" {
		return pluginapi.Identity{}, fmt.Errorf("acl: %w", pluginapi.ErrUnauthorized)
	}
	var userID, tokenScope string
	err := v.DB.QueryRowContext(ctx,
		`SELECT user_id, scope FROM tokens WHERE hash = ?`, hashToken(raw)).
		Scan(&userID, &tokenScope)
	if err != nil {
		return pluginapi.Identity{}, fmt.Errorf("acl: %w", pluginapi.ErrUnauthorized)
	}
	if !scopeSatisfies(tokenScope, required) {
		return pluginapi.Identity{}, fmt.Errorf("acl: token scope insufficient: %w", pluginapi.ErrUnauthorized)
	}
	return v.resolveUser(ctx, userID)
}

// verifySession authenticates raw against the sessions table (expired
// sessions do not authenticate).
func (v *Verifier) verifySession(ctx context.Context, raw string) (pluginapi.Identity, error) {
	if raw == "" {
		return pluginapi.Identity{}, fmt.Errorf("acl: %w", pluginapi.ErrUnauthorized)
	}
	var userID string
	err := v.DB.QueryRowContext(ctx,
		`SELECT user_id FROM sessions WHERE hash = ? AND expires_at > ?`,
		hashToken(raw), time.Now().Unix()).Scan(&userID)
	if err != nil {
		return pluginapi.Identity{}, fmt.Errorf("acl: %w", pluginapi.ErrUnauthorized)
	}
	return v.resolveUser(ctx, userID)
}

// resolveUser turns a user_id into an Identity via the users table. A token
// without a backing user row does not authenticate (fail closed).
func (v *Verifier) resolveUser(ctx context.Context, userID string) (pluginapi.Identity, error) {
	var role string
	err := v.DB.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, userID).Scan(&role)
	if err != nil {
		return pluginapi.Identity{}, fmt.Errorf("acl: %w", pluginapi.ErrUnauthorized)
	}
	return pluginapi.Identity{ID: userID, Role: role}, nil
}

// TokenScope returns middleware authenticating "Authorization: Bearer
// <token>" against the tokens table and requiring the named scope. On
// success the resolved Identity is set in context (WithIdentity) and the
// request proceeds; otherwise a 401 problem+json is written and the chain
// stops. The scope argument follows the package-doc hierarchy.
func (v *Verifier) TokenScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := bearer(r.Header.Get("Authorization"))
			if raw == "" {
				api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "bearer token required")
				return
			}
			ident, err := v.verifyBearer(r.Context(), raw, scope)
			if err != nil {
				// Wrong scope = wrong realm: 401, never 403 (see package doc).
				api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "invalid token or insufficient scope")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), ident)))
		})
	}
}

// RequireToken is an alias of TokenScope under the Require* naming family.
func (v *Verifier) RequireToken(scope string) func(http.Handler) http.Handler {
	return v.TokenScope(scope)
}

// RequireSession returns combined auth middleware with an explicit
// precedence: a present Authorization: Bearer header is authoritative and
// must authenticate as a token (any scope); otherwise the session cookie is
// looked up; anything unresolved is 401 problem+json. On either success path
// the Identity lands in context under the single canonical key.
func (v *Verifier) RequireSession() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if raw := bearer(r.Header.Get("Authorization")); raw != "" {
				ident, err := v.verifyBearer(r.Context(), raw, "")
				if err != nil {
					api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "invalid token")
					return
				}
				next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), ident)))
				return
			}
			cookie, err := r.Cookie(SessionCookieName)
			if err != nil {
				api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
				return
			}
			ident, err := v.verifySession(r.Context(), cookie.Value)
			if err != nil {
				api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "invalid or expired session")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), ident)))
		})
	}
}

// RequireRole returns middleware requiring an Identity in context with
// exactly the given role. No identity → 401 (never authenticated); wrong
// role → 403. It reads the single canonical context key, so it can sit
// anywhere behind TokenScope / RequireSession / an upstream auth handler.
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ident, ok := IdentityFrom(r.Context())
			if !ok {
				api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
				return
			}
			if ident.Role != role {
				api.WriteProblem(w, http.StatusForbidden, "Forbidden", "insufficient role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Map keys HTTP routes to the Mode their requests require on vault content.
// Keys are "METHOD /request-prefix" in stdlib ServeMux pattern style, e.g.
//
//	Map{
//	  "GET /api/v1/notes/": ModeRead,
//	  "PUT /api/v1/notes/": ModeWrite,
//	}
//
// The vault path checked by the ACL is the request path with the matched
// prefix stripped (so /api/v1/notes/characters/bob/x.md checks
// characters/bob/x.md). The LONGEST matching prefix wins; requests matching
// no entry pass through unchecked (mount this middleware only on authenticated
// vault routes). Malformed keys (no METHOD portion) are ignored.
type Map map[string]Mode

// matchModes finds the longest "METHOD prefix" entry whose prefix is on the
// request path.
func matchModes(m Map, method, reqPath string) (prefix string, mode Mode, found bool) {
	for key, m := range m {
		methodKey, prefixKey, ok := strings.Cut(key, " ")
		if !ok || methodKey != method {
			continue
		}
		if !strings.HasPrefix(reqPath, prefixKey) {
			continue
		}
		if !found || len(prefixKey) > len(prefix) {
			prefix, mode, found = prefixKey, m, true
		}
	}
	return prefix, mode, found
}

// Handler returns middleware enforcing vault path access per request.
//
// Naming deviation, documented: this wrapper was requested as `ACL(modes
// Map, acl *ACL)`, but the middleware function name and the ACL struct type
// cannot both be `ACL` in one Go package. The package doc's own phrase
// ("acl.Handler wrapper") resolves it: the struct stays `ACL` (it carries
// Can and is the thing E's handlers will hold), and the middleware is
// `Handler`. It expects
// an Identity in context (from TokenScope / RequireSession upstream); a
// missing identity is 401. When the request matches a Map entry, acl.Can
// gates the request: denied paths are 403 problem+json whose detail NEVER
// names the target path (a denied path's existence is itself sensitive —
// see the security-tag tests). Path validation failures (traversal) are 403
// exactly like denials; infrastructure errors are a bare 500.
func Handler(modes Map, acl *ACL) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ident, ok := IdentityFrom(r.Context())
			if !ok {
				api.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
				return
			}
			if prefix, mode, found := matchModes(modes, r.Method, r.URL.Path); found {
				vaultPath := strings.TrimPrefix(r.URL.Path, prefix)
				allowed, err := acl.Can(r.Context(), ident, vaultPath, mode)
				if err != nil {
					if errors.Is(err, pluginapi.ErrForbidden) {
						// Rejected path: same verdict shape as a denial.
						api.WriteProblem(w, http.StatusForbidden, "Forbidden", deniedDetail)
						return
					}
					api.WriteProblem(w, http.StatusInternalServerError, "ACL error", "path check failed")
					return
				}
				if !allowed {
					// SECURITY: never echo the target path into problem details.
					api.WriteProblem(w, http.StatusForbidden, "Forbidden", deniedDetail)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// deniedDetail is the only detail ever written for ACL denials: fixed text,
// no path, no identity.
const deniedDetail = "insufficient permission for this vault path"
