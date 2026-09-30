// Package httpapi exposes the semiplane HTTP surface.
package httpapi

import (
	"net/http"

	"github.com/semiplane/semiplane/internal/observability"
)

// readyHandler reports readiness plus the §13.2 counters as JSON.
//
// Read-only and unauthenticated, which is what a load balancer or a container
// orchestrator needs it to be. The counters are counts and gauges of internal
// activity — no content, no file contents, no dice results — so there is nothing
// in the payload that a caller without access to any campaign could not
// otherwise learn (S-12.3).
type readyHandler struct {
	registry *observability.Registry
}

func newReadyHandler(registry *observability.Registry) http.Handler {
	return readyHandler{registry: registry}
}

// readinessResponse is the `/readyz` body.
//
// Status is separate from the counters so a future readiness check — a
// migration that has not run, a campaign whose root is missing — has somewhere
// to live without changing the shape a consumer already parses.
type readinessResponse struct {
	Status   string                                `json:"status"`
	Counters map[string]observability.CounterValue `json:"counters"`
}

func (h readyHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, readinessResponse{
		Status:   "ready",
		Counters: h.registry.Snapshot(),
	})
}
