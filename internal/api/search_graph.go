package api

import (
	"net/http"
	"strconv"
	"strings"
)

// handleSearch runs a BM25-ranked FTS5 query (B's index). ?q= is required
// (400 otherwise); ?limit= clamps to 1..50, defaulting to 10 in the index.
func (d *Deps) handleSearch(w http.ResponseWriter, r *http.Request) {
	if d.Index == nil {
		WriteProblem(w, http.StatusServiceUnavailable, "index unavailable", "no SearchIndex wired")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		WriteProblem(w, http.StatusBadRequest, "bad request", "missing required query param q")
		return
	}
	limit := 10
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			WriteProblem(w, http.StatusBadRequest, "bad request", "limit must be a positive integer")
			return
		}
		limit = n
	}
	hits, err := d.Index.Search(q, limit)
	if err != nil {
		writeRouteError(w, err, "search failed: "+err.Error())
		return
	}
	type hit struct {
		Path    string  `json:"path"`
		Snippet string  `json:"snippet"`
		Score   float64 `json:"score"`
	}
	out := make([]hit, 0, len(hits))
	for _, h := range hits {
		out = append(out, hit{Path: h.Path, Snippet: h.Snippet, Score: h.Score})
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": out})
}

// handleGraph returns nodes/edges for link visualisation: every indexed
// note plus every stored wikilink edge, both lexical. Targets with no note
// row dangle until created (the vault is the source of truth).
func (d *Deps) handleGraph(w http.ResponseWriter, r *http.Request) {
	if d.Index == nil {
		WriteProblem(w, http.StatusServiceUnavailable, "index unavailable", "no SearchIndex wired")
		return
	}
	nodes, edges, err := d.Index.Graph()
	if err != nil {
		writeRouteError(w, err, "graph failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "edges": edges})
}
