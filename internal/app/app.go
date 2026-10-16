// Package app wires configuration and the HTTP server.
//
// It owns server bootstrap only. Route definitions live in internal/api;
// static SPA serving lives in internal/webui.
package app

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/PopinjayJohn/semiplane/internal/api"
	"github.com/PopinjayJohn/semiplane/internal/index"
	"github.com/PopinjayJohn/semiplane/internal/middleware"
	"github.com/PopinjayJohn/semiplane/internal/oplog"
	"github.com/PopinjayJohn/semiplane/internal/sse"
	"github.com/PopinjayJohn/semiplane/internal/vault"
	"github.com/PopinjayJohn/semiplane/internal/webui"
)

// Config follows flags > env > file > defaults (tech-stack §9).
type Config struct {
	Addr     string
	VaultDir string
}

// Server is the runnable HTTP server.
type Server struct {
	cfg     Config
	handler http.Handler
}

// NewServer builds the full /api/v1 mux via internal/api with real Phase-1
// implementations (A's render is called inside handlers; B's vault/index
// and the SSE hub + oplog journal are wired here), wraps it in the
// middleware chain (request ID, slog, rate limit, recovery), and falls back
// to the embedded SPA for anything else.
func NewServer(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	vaultDir := cfg.VaultDir
	if vaultDir == "" {
		vaultDir = "."
	}
	deps := &api.Deps{
		Vault: vault.New(vaultDir),
		Hub:   sse.NewHub(),
		Oplog: oplog.New(filepath.Join(vaultDir, ".semiplane", "oplog")),
	}
	if db, err := index.Open(filepath.Join(vaultDir, ".semiplane", "index.db")); err != nil {
		slog.Warn("app: index unavailable, search/graph/backlinks degraded", "err", err)
	} else {
		deps.Index = index.New(db)
	}
	mux := api.NewMuxWithDeps(deps)
	// SPA fallback: embedded UI for everything else (GET/HEAD).
	mux.HandleFunc("GET /{$}", webui.Handler())
	mux.HandleFunc("GET /{path...}", webui.Handler())

	limited := middleware.RateLimit(middleware.NewLimiter(600, time.Minute))
	handler := middleware.Chain(mux,
		middleware.RequestIDMW,
		middleware.Logging,
		limited,
		func(next http.Handler) http.Handler {
			return middleware.Recovery(next, func(v any, stack []byte) {
				slog.Error("panic recovered", "panic", v, "stack", string(stack))
			})
		},
	)
	return &Server{cfg: cfg, handler: handler}
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
