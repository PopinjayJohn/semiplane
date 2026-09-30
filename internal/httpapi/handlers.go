package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/semiplane/semiplane/internal/httpapi/middleware"
)

type healthResponse struct {
	Status string `json:"status"`
}

// healthHandler answers `/healthz`: is the process alive. Deliberately does not
// touch the store — liveness that fails when the database is busy turns a
// database problem into a restart loop.
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// notFoundHandler answers unmatched routes with a 404 rather than net/http's
// bare text/plain one, so an unauthenticated probe cannot distinguish "no such
// route" from "this path needs a role you do not have". The body is identical
// in both cases by construction — the access log carries the difference.
func notFoundHandler(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.DebugContext(r.Context(), "http.route_miss",
			slog.String("request_id", middleware.MustRequestID(r.Context())),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
		)

		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not found"})
	})
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The status line is already committed, so there is nothing left to
		// tell the client. The log is all that remains, and a failure here
		// means the client received a truncated body — worth recording.
		slog.Error("encode response", slog.String("error", err.Error()))
	}
}

// securityHeaders sets the response headers that apply to every route.
//
// Applied inside the middleware chain rather than in the server, because a
// header set here is set on every response including the ones the chain
// produces itself, which is the entire point: a 504 from Timeout or a 500 from
// Recoverer is exactly the response a scanner probes for.
func securityHeaders(next http.Handler) http.Handler {
	// A fixed map, iterated per request. Cheap relative to a request, and a
	// slice of pairs would only trade an allocation for a slightly clearer
	// loop. Header order is not significant.
	headers := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for key, value := range headers {
			w.Header().Set(key, value)
		}

		next.ServeHTTP(w, r)
	})
}
