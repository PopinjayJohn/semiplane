package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/PopinjayJohn/semiplane/internal/render"
	"github.com/PopinjayJohn/semiplane/internal/sse"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// revealBody is the GM→player reveal request: page names the vault note,
// blockId names the :::gm block within it.
type revealBody struct {
	Page    string `json:"page"`
	BlockID string `json:"blockId"`
}

// handleReveal broadcasts one :::gm block to players (tech-stack §3.2: the
// reveal is pre-rendered server-side HTML in the block_revealed SSE event,
// never trust-the-client). v0 broadcasts the page's GM fragment with the
// block id; per-block HTML slicing is a later slice.
func (d *Deps) handleReveal(w http.ResponseWriter, r *http.Request) {
	if d.Vault == nil {
		WriteProblem(w, http.StatusServiceUnavailable, "vault unavailable", "no NoteRepository wired")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body revealBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteProblem(w, http.StatusBadRequest, "bad request", "body must be JSON {page, blockId}: "+err.Error())
		return
	}
	page := strings.TrimSpace(body.Page)
	block := strings.TrimSpace(body.BlockID)
	if page == "" || block == "" {
		WriteProblem(w, http.StatusBadRequest, "bad request", "page and blockId are required")
		return
	}
	note, err := d.Vault.Get(r.Context(), page)
	if err != nil {
		if errors.Is(err, pluginapi.ErrNotFound) {
			WriteProblem(w, http.StatusNotFound, "not found", "no such page: "+page)
			return
		}
		writeRouteError(w, err, "")
		return
	}
	fragment, _, err := render.Render([]byte(note.Content), render.RoleGM)
	if err != nil {
		writeRouteError(w, err, "reveal render failed: "+err.Error())
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"page": page, "blockId": block, "html": fragment,
	})
	evt := d.Hub.Publish(sse.Event{Type: "block_revealed", Data: string(payload)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "eventId": evt.ID})
}

// handleEvents serves the SSE broadcast stream (tech-stack §3.5, §7):
// text/event-stream with Last-Event-ID replay, so a client that slept
// through a roll sees it on reconnect. Both the Last-Event-ID header and
// ?lastEventId= are honored (header wins); unparsable values replay from 0.
func (d *Deps) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteProblem(w, http.StatusInternalServerError, "internal server error", "streaming unsupported")
		return
	}
	var lastID uint64
	if h := strings.TrimSpace(r.Header.Get("Last-Event-ID")); h != "" {
		if v, err := strconv.ParseUint(h, 10, 64); err == nil {
			lastID = v
		}
	} else if q := strings.TrimSpace(r.URL.Query().Get("lastEventId")); q != "" {
		if v, err := strconv.ParseUint(q, 10, 64); err == nil {
			lastID = v
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	ch, unsub := d.Hub.Subscribe(lastID)
	defer unsub()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			_, _ = fmt.Fprint(w, sse.Format(e))
			flusher.Flush()
		}
	}
}
