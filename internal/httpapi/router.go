package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/web"
)

// NewRouter builds the application HTTP handler with all routes registered.
//
// The middleware order is the part of this function that matters, and it is
// one list rather than an assembly at each call site so the reasoning is
// visible where it is decided. Listed outermost first, which is the order
// requests traverse it:
//
//  1. RequestID — first, because every layer below it logs, and a log line
//     without a correlation id cannot be tied back to a request.
//  2. Recoverer — second, so it catches a panic in any layer below it, which
//     includes a handler that has already committed a status line. That case is
//     why the response is a log entry plus whatever the server can still write,
//     not simply a dropped connection.
//  3. RealIP — before Log, because Log's client_ip attribute has to be the
//     resolved address rather than the proxy's.
//  4. Log — times the handler, and sits outside Timeout so it observes the 504
//     rather than the 200 a timed-out handler wrote. A timeout invisible in the
//     access log is the failure this ordering prevents.
//  5. Timeout — innermost of the timed layers, so the budget covers the handler
//     and nothing else. Outermost would also time the logging and the panic
//     recovery, and a timeout firing while the error path runs turns a 500 into
//     a truncated response.
//  6. securityHeaders — innermost, applied to the mux. It sets headers before
//     delegating, so it has to be inside every layer that can write a response
//     of its own, or a 504 from Timeout would ship without them.
func NewRouter(logger *slog.Logger, cfg config.Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.HandleFunc("GET /readyz", readinessHandler)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(web.Dist())))
	mux.Handle("/", notFoundHandler(logger))

	return middleware.Chain(
		securityHeaders(mux),
		middleware.RequestID,
		middleware.Recoverer(logger),
		middleware.RealIP(cfg.TrustedProxies),
		middleware.Log(logger),
		middleware.Timeout(cfg.HandlerTimeout),
	)
}
