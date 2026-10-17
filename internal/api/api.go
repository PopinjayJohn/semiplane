// Package api defines the HTTP surface: /api/v1 routes, health, and the SPA
// fallback. Errors are RFC 7807 application/problem+json (one path only).
package api

import (
	"encoding/json"
	"net/http"

	"github.com/PopinjayJohn/semiplane/internal/webui"
)

// NewMux builds the stdlib ServeMux. No router frameworks (tech-stack §3.1).
func NewMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /api/v1/version", handleVersion)

	// SPA fallback: embedded UI for everything else (GET/HEAD).
	mux.HandleFunc("GET /{$}", webui.Handler())
	mux.HandleFunc("GET /{path...}", webui.Handler())
	return mux
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": "0.0.0-slice"})
}

// Problem is the RFC 7807 error body.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// WriteProblem is the single error path for all handlers.
func WriteProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: detail,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
