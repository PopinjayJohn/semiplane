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
	"time"

	"github.com/PopinjayJohn/semiplane/internal/api"
)

// Config follows flags > env > file > defaults (tech-stack §9).
// The thin slice only carries what serve needs; fields grow with features.
type Config struct {
	Addr     string
	VaultDir string
}

// Server is the runnable HTTP server.
type Server struct {
	cfg Config
	mux *http.ServeMux
}

// NewServer builds the ServeMux via internal/api and returns the server.
func NewServer(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	return &Server{cfg: cfg, mux: api.NewMux()}
}

// Handler exposes the mux for httptest integration tests without binding a port.
func (s *Server) Handler() http.Handler { return s.mux }

// Run serves until ctx is cancelled, then drains with a graceful timeout.
func (s *Server) Run(ctx context.Context) error {
	hs := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.mux,
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
