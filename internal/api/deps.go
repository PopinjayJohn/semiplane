package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/PopinjayJohn/semiplane/internal/index"
	"github.com/PopinjayJohn/semiplane/internal/oplog"
	"github.com/PopinjayJohn/semiplane/internal/sse"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// Deps wires the Phase-1 streams into the HTTP surface (plan Phase 1/E):
// A's Render, B's vault/index, D's dice/rules. Handlers program against the
// Phase-0 interfaces (pluginapi.*), so httptest suites swap in fakes and
// scratch vaults without touching the real tree.
type Deps struct {
	// Vault is the source of truth (B). Required for note routes.
	Vault pluginapi.NoteRepository
	// Index is the derived FTS5 + backlink index (B). Nil degrades search,
	// graph, and backlinks to 503 ("index unavailable") instead of failing
	// the whole mux — the vault stays readable.
	Index *index.Index
	// Hub broadcasts reveals and rolls to GET /api/v1/events (SSE).
	Hub *sse.Hub
	// Oplog journals server-authoritative resolutions (replayable log).
	Oplog *oplog.Log
	// PackID names the active system pack for oplog replay ("none" unset).
	PackID string
	// VaultCan gates per-item results against the vault ACL (mode "read"/"write").
	// Wired by internal/app from the shared ACL over the same index DB. Nil
	// ⇒ every check passes (auth deferred to the auth stream's middleware).
	VaultCan func(ctx context.Context, path, mode string) bool
}

// NewMuxWithDeps builds the full /api/v1 mux. A nil Hub or Oplog is
// replaced with a live no-op-scoped default so handlers never nil-panic;
// a nil Vault fails writes/reads per-route with 503, never at boot.
func NewMuxWithDeps(d *Deps) *http.ServeMux {
	if d == nil {
		d = &Deps{}
	}
	if d.Hub == nil {
		d.Hub = sse.NewHub()
	}
	if d.Oplog == nil {
		d.Oplog = oplog.New("")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /api/v1/version", handleVersion)

	mux.HandleFunc("GET /api/v1/notes", d.handleListNotes)
	mux.HandleFunc("GET /api/v1/notes/{path...}", d.handleGetNote)
	mux.HandleFunc("POST /api/v1/notes/{path...}", d.handlePostNote)
	mux.HandleFunc("GET /api/v1/search", d.handleSearch)
	mux.HandleFunc("GET /api/v1/graph", d.handleGraph)
	mux.HandleFunc("POST /api/v1/dice/roll", d.handleDiceRoll)
	mux.HandleFunc("POST /api/v1/reveal", d.handleReveal)
	mux.HandleFunc("GET /api/v1/events", d.handleEvents)
	return mux
}

func errStatus(err error) (int, string) {
	switch {
	case errors.Is(err, pluginapi.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, pluginapi.ErrConflict):
		return http.StatusConflict, "conflict"
	case errors.Is(err, pluginapi.ErrUnauthorized):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, pluginapi.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

// writeRouteError is the single error path for dependency failures: sentinel
// errors from pluginapi map to their status, everything else is a 500 with
// the detail preserved for operators (same problem+json shape, one path).
func writeRouteError(w http.ResponseWriter, err error, detail string) {
	status, title := errStatus(err)
	if detail == "" {
		detail = err.Error()
	}
	WriteProblem(w, status, title, detail)
}
