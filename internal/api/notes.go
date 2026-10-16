package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/PopinjayJohn/semiplane/internal/index"
	"github.com/PopinjayJohn/semiplane/internal/render"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// NoteDoc is the wire shape stream F consumes (charter follow-up):
// fragment (role-rendered HTML) + meta (frontmatter registry, see
// render.META_KEYS) + backlinks (vault-relative .md paths) + ETag (content
// hash). content carries the raw Markdown for the editor; readers use
// fragment.
type NoteDoc struct {
	Path      string         `json:"path"`
	Content   string         `json:"content"`
	Fragment  string         `json:"fragment"`
	Meta      map[string]any `json:"meta"`
	Backlinks []string       `json:"backlinks"`
	ETag      string         `json:"etag"`
}

// maxNoteBody caps note upserts (1 MiB of Markdown is generous; larger
// pastes chunk client-side).
const maxNoteBody = 1 << 20

// roleOf resolves ?role= (default "player"): players get :::gm stripped
// server-side, never client-side (security property, see render package).
func roleOf(r *http.Request) (render.Role, error) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("role")))
	if q == "" || q == "player" {
		return render.RolePlayer, nil
	}
	if q == "gm" {
		return render.RoleGM, nil
	}
	return "", errors.New("role must be gm or player")
}

// buildNoteDoc renders note for role and attaches backlinks + ETag.
func (d *Deps) buildNoteDoc(r *http.Request, note pluginapi.Note, role render.Role) (NoteDoc, error) {
	fragment, meta, err := render.Render([]byte(note.Content), role)
	if err != nil {
		return NoteDoc{}, err
	}
	if meta == nil {
		meta = map[string]any{}
	}
	backlinks := []string{}
	if d.Index != nil {
		bl, err := d.Index.Backlinks(note.Path)
		if err != nil {
			return NoteDoc{}, err
		}
		backlinks = bl
	}
	return NoteDoc{
		Path:      note.Path,
		Content:   note.Content,
		Fragment:  fragment,
		Meta:      meta,
		Backlinks: backlinks,
		ETag:      note.Hash,
	}, nil
}

func (d *Deps) handleListNotes(w http.ResponseWriter, r *http.Request) {
	if d.Vault == nil {
		WriteProblem(w, http.StatusServiceUnavailable, "vault unavailable", "no NoteRepository wired")
		return
	}
	q := r.URL.Query()
	// ?folder= is the documented alias; ?prefix= is the literal form.
	prefix := q.Get("folder")
	if p := q.Get("prefix"); p != "" {
		prefix = p
	}
	paths, err := d.Vault.List(r.Context(), prefix)
	if err != nil {
		writeRouteError(w, err, "")
		return
	}
	type entry struct {
		Path  string `json:"path"`
		Title string `json:"title"`
	}
	out := make([]entry, 0, len(paths))
	for _, p := range paths {
		out = append(out, entry{Path: p, Title: index.ParseTitle(p, "")})
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": out})
}

func (d *Deps) handleGetNote(w http.ResponseWriter, r *http.Request) {
	if d.Vault == nil {
		WriteProblem(w, http.StatusServiceUnavailable, "vault unavailable", "no NoteRepository wired")
		return
	}
	role, err := roleOf(r)
	if err != nil {
		WriteProblem(w, http.StatusBadRequest, "bad request", err.Error())
		return
	}
	path := r.PathValue("path")
	note, err := d.Vault.Get(r.Context(), path)
	if err != nil {
		writeRouteError(w, err, "")
		return
	}
	etag := `"` + note.Hash + `"`
	w.Header().Set("ETag", etag)
	if matchETag(r.Header.Get("If-None-Match"), note.Hash) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	doc, err := d.buildNoteDoc(r, note, role)
	if err != nil {
		writeRouteError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// postNoteBody is the upsert request. The hash field branches explicitly
// (decided ruling): empty hash means CREATE (no precondition; 409 when the
// path already exists), non-empty hash means UPDATE guarded by the
// read-time hash (409 on concurrent external edit, tech-stack §5).
type postNoteBody struct {
	Content string `json:"content"`
	Hash    string `json:"hash"`
}

func (d *Deps) handlePostNote(w http.ResponseWriter, r *http.Request) {
	if d.Vault == nil {
		WriteProblem(w, http.StatusServiceUnavailable, "vault unavailable", "no NoteRepository wired")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxNoteBody)
	var body postNoteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteProblem(w, http.StatusBadRequest, "bad request", "body must be JSON {content, hash}: "+err.Error())
		return
	}
	path := r.PathValue("path")
	role, err := roleOf(r)
	if err != nil {
		WriteProblem(w, http.StatusBadRequest, "bad request", err.Error())
		return
	}
	created := false
	if body.Hash == "" {
		// Create branch: refuse to clobber an existing note.
		if _, err := d.Vault.Get(r.Context(), path); err == nil {
			WriteProblem(w, http.StatusConflict, "conflict",
				"note already exists: re-read for its hash, then retry with hash set (update branch)")
			return
		} else if !errors.Is(err, pluginapi.ErrNotFound) {
			writeRouteError(w, err, "")
			return
		}
		created = true
	}
	stored, err := d.Vault.Put(r.Context(), pluginapi.Note{Path: path, Content: body.Content, Hash: body.Hash})
	if err != nil {
		if errors.Is(err, pluginapi.ErrConflict) {
			WriteProblem(w, http.StatusConflict, "conflict",
				"note changed since you read it (hash mismatch): re-read, merge, retry")
			return
		}
		writeRouteError(w, err, "")
		return
	}
	// Best-effort derived-index sync: the vault is the source of truth, so
	// a lagging index never fails the write (reindex rebuilds it).
	if d.Index != nil {
		_ = d.Index.Upsert(stored.Path, stored.Content, stored.Hash, 0)
	}
	doc, err := d.buildNoteDoc(r, stored, role)
	if err != nil {
		writeRouteError(w, err, "")
		return
	}
	w.Header().Set("ETag", `"`+stored.Hash+`"`)
	if created {
		writeJSON(w, http.StatusCreated, doc)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// matchETag reports whether the If-None-Match header matches hash.
// Supports quoted, W/ weak, bare, comma lists, and "*".
func matchETag(header, hash string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for _, part := range strings.Split(header, ",") {
		p := strings.TrimSpace(part)
		p = strings.TrimPrefix(p, "W/")
		p = strings.Trim(p, `"`)
		if p == hash {
			return true
		}
	}
	return false
}
