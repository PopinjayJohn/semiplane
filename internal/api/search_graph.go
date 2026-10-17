package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
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
	// Derived-content leak fix: a player must never see campaign/* hits.
	// Filter per-item through the vault ACL (ModeRead, ≤50 hits, cheap).
	if d.VaultCan != nil {
		kept := make([]pluginapi.SearchHit, 0, len(hits))
		for _, h := range hits {
			if d.VaultCan(r.Context(), h.Path, "read") {
				kept = append(kept, h)
			}
		}
		hits = kept
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
	// Derived-content leak fix: a player must never see campaign/* nodes or
	// edges referencing them. Filter per-item through the vault ACL (ModeRead).
	if d.VaultCan != nil {
		readable := make(map[string]bool, len(nodes))
		kept := nodes[:0]
		for _, n := range nodes {
			if d.VaultCan(r.Context(), n.Path, "read") {
				readable[n.Path] = true
				kept = append(kept, n)
			}
		}
		nodes = kept
		edgeKept := edges[:0]
		for _, e := range edges {
			if readable[e.Src] && readable[e.Dst] {
				edgeKept = append(edgeKept, e)
			}
		}
		edges = edgeKept
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "edges": edges})
}
