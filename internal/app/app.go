// Package app wires configuration and the HTTP server.
//
// It owns server bootstrap only. Route definitions live in internal/api;
// static SPA serving lives in internal/webui.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/PopinjayJohn/semiplane/internal/acl"
	"github.com/PopinjayJohn/semiplane/internal/api"
	"github.com/PopinjayJohn/semiplane/internal/auth"
	"github.com/PopinjayJohn/semiplane/internal/index"
	"github.com/PopinjayJohn/semiplane/internal/middleware"
	"github.com/PopinjayJohn/semiplane/internal/oplog"
	"github.com/PopinjayJohn/semiplane/internal/sse"
	"github.com/PopinjayJohn/semiplane/internal/vault"
	"github.com/PopinjayJohn/semiplane/internal/webui"
)

// rateLimit is the production per-client-IP request budget: 60 requests per
// minute. A tabletop LAN deployment is human-driven (a note read or edit, a
// roll, one SSE handshake per device) and stays far below this under normal
// play, so 60/min is a conservative DoS guard rather than a throttle on
// legitimate table traffic. It is an interim per-RemoteAddr limit; behind a
// reverse proxy it should move to a trusted X-Forwarded-For policy (see the
// Phase 1 review follow-ups in docs/implementation-plan.md). Auth middleware
// sits inside rate limiting, so unauthenticated attempts are counted too.
const rateLimit = 60

// Config follows flags > env > file > defaults (tech-stack §9).
type Config struct {
	Addr     string
	VaultDir string

	// Session lifetime minted by the auth endpoints; 0 ⇒ 24h (auth's defaultSessionTTL).
	SessionTTL time.Duration

	// ForceSecure always sets the Secure flag on session cookies, even when
	// X-Forwarded-Proto is not https (deployments behind a proxy that does
	// not forward the header).
	ForceSecure bool

	// OIDC provider config, from env/flags (issuer, client id/secret, redirect
	// URL). All unset ⇒ authDeps.Provider/Config stay nil and the two OIDC
	// routes answer 503 problem+json instead of panicking.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
}

// Server is the runnable HTTP server.
type Server struct {
	cfg     Config
	handler http.Handler
}

// NewServer builds the full /api/v1 mux via internal/api with real Phase-1
// implementations (A's render inside handlers; B's vault/index and the SSE hub
// + oplog journal wired here), opens the vault's `.semiplane/index.db` once
// (schema already applied by migrate) and builds auth.Users, SessionService,
// LanCodeService and OIDCService over that single *sql.DB, calls
// auth.Register on the API mux, mounts the acl middleware around the
// vault-content routes, and wraps everything in the middleware chain
// (RequestID → Logging → RateLimit → Recovery → auth → routes; auth inside
// rate limiting). Fails to boot if the vault index DB cannot be opened: auth
// is mandatory on the merged tree, so failing open is not an option.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	vaultDir := cfg.VaultDir
	if vaultDir == "" {
		vaultDir = "."
	}
	dbPath := filepath.Join(vaultDir, ".semiplane", "index.db")

	// One DB for the whole server: the index schema (applied by migrate) also
	// carries the auth tables (sessions, tokens, lan_codes, oidc_states, acls,
	// users). Every service shares it on the single-connection pool.
	db, err := index.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("app: open vault index db %q: %w", dbPath, err)
	}

	dbACL := acl.New(db)
	deps := &api.Deps{
		Vault:  vault.New(vaultDir),
		Hub:    sse.NewHub(),
		Oplog:  oplog.New(filepath.Join(vaultDir, ".semiplane", "oplog")),
		Index:  index.New(db),
		PackID: "none", // TODO: thread the real system pack id (implementation-plan follow-up 4)
	}
	deps.VaultCan = func(ctx context.Context, path, mode string) bool {
		ident, ok := acl.IdentityFrom(ctx)
		if !ok {
			return false
		}
		allowed, err := dbACL.Can(ctx, ident, path, acl.Mode(mode))
		if err != nil {
			return false
		}
		return allowed
	}

	authDeps := auth.Deps{
		Users:       auth.NewUsers(db),
		Sessions:    auth.NewSessionService(db),
		LanCodes:    auth.NewLanCodeService(db),
		OIDC:        auth.NewOIDCService(db),
		SessionTTL:  cfg.SessionTTL,
		ForceSecure: cfg.ForceSecure,
	}
	if cfg.OIDCIssuer != "" {
		provider, err := oidc.NewProvider(context.Background(), cfg.OIDCIssuer)
		if err != nil {
			return nil, fmt.Errorf("app: oidc provider %q: %w", cfg.OIDCIssuer, err)
		}
		authDeps.Provider = provider
		authDeps.Config = &oauth2.Config{
			ClientID:     cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.OIDCRedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "profile"},
		}
	}

	mux := api.NewMuxWithDeps(deps)
	auth.Register(mux, authDeps)
	// SPA fallback: embedded UI for everything else (GET/HEAD).
	mux.HandleFunc("GET /{$}", webui.Handler())
	mux.HandleFunc("GET /{path...}", webui.Handler())

	verifier := &acl.Verifier{DB: db}
	// Map keys are mount prefixes (see internal/acl matchModes): the vault path
	// checked is the request path with the matched prefix stripped, so
	// /api/v1/notes/characters/player1/x.md checks characters/player1/x.md.
	// GET /api/v1/notes (the exact list path, no trailing slash) does NOT match
	// the /api/v1/notes/ prefix entry; the list route is covered by per-item
	// filtering in api.handleListNotes instead (task: derived-content leak).
	modes := acl.Map{
		"GET /api/v1/notes/":  acl.ModeRead,
		"POST /api/v1/notes/": acl.ModeWrite,
	}

	limited := middleware.RateLimit(middleware.NewLimiter(rateLimit, time.Minute))
	handler := middleware.Chain(mux,
		middleware.RequestIDMW,
		middleware.Logging,
		limited,
		func(next http.Handler) http.Handler {
			return middleware.Recovery(next, func(v any, stack []byte) {
				slog.Error("panic recovered", "panic", v, "stack", string(stack))
			})
		},
		authMW(verifier, dbACL, modes),
	)
	return &Server{cfg: cfg, handler: handler}, nil
}

// authMW requires a valid session (cookie or bearer token) for every route
// under /api/v1 except the self-authenticating /api/v1/auth/* surface (login
// creates the session; it must not require one). /api/v1/version stays public.
// POST /api/v1/reveal additionally requires the gm role, and note reads/writes
// are gated by the acl path-prefix Map. Everything else (/healthz, the SPA
// fallback) passes through unauthenticated.
func authMW(verifier *acl.Verifier, a *acl.ACL, modes acl.Map) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
				next.ServeHTTP(w, r) // /healthz, SPA shell, static assets: public
				return
			}
			if r.URL.Path == "/api/v1/version" || strings.HasPrefix(r.URL.Path, "/api/v1/auth/") {
				next.ServeHTTP(w, r) // public, or self-authenticating (login/logout/me/lan/oidc)
				return
			}
			// Authenticated vault/API surface: session (cookie or bearer) first
			// so the identity lands in context for everything downstream.
			h := next
			h = acl.Handler(modes, a)(h)
			if r.URL.Path == "/api/v1/reveal" && r.Method == http.MethodPost {
				h = acl.RequireRole(acl.RoleGM)(h)
			}
			h = verifier.RequireSession()(h)
			h.ServeHTTP(w, r)
		})
	}
}

// Handler exposes the mux for httptest integration tests without binding a port.
func (s *Server) Handler() http.Handler { return s.handler }

// Run serves until ctx is cancelled, then drains with a graceful timeout.
func (s *Server) Run(ctx context.Context) error {
	hs := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	slog.Info("listening", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() { errCh <- hs.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return hs.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}
