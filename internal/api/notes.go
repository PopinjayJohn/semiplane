package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/PopinjayJohn/semiplane/internal/identity"
	"github.com/PopinjayJohn/semiplane/internal/index"
	"github.com/PopinjayJohn/semiplane/internal/markdown"
	"github.com/PopinjayJohn/semiplane/internal/render"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// NoteDoc is the wire shape stream F consumes (charter follow-up):
// fragment (role-rendered HTML) + meta (frontmatter registry, see
// render.META_KEYS) + backlinks (vault-relative .md paths) + ETag (content
// hash). content carries the raw Markdown for the editor; readers use
// fragment.
type NoteDoc struct {
	Path     string         `json:"path"`
	Content  string         `json:"content"`
	Fragment string         `json:"fragment"`
	Meta     map[string]any `json:"meta"`
	// GMBlocks lists the opaque per-block ids (gm-0, gm-1, …) of the note's
	// :::gm blocks, in source order. It carries no content, so it is safe to
	// send to every role: it exists so players/TV can show reveal-pending
	// affordances and agree on block ids with the GM even though Content and
	// Fragment have the blocks stripped for players. Empty ⇒ no GM blocks.
	GMBlocks  []string `json:"gm_blocks,omitempty"`
	Backlinks []string `json:"backlinks"`
	ETag      string   `json:"etag"`
}

// maxNoteBody caps note upserts (1 MiB of Markdown is generous; larger
// pastes chunk client-side).
const maxNoteBody = 1 << 20

// roleOf resolves the effective render role for the request.
// It reads the authenticated identity from context (set by ACL middleware)
// and validates the optional ?role= query parameter against it:
// - GM sessions may request ?role=gm (default) or ?role=player
// - Player sessions may only use ?role=player (default); ?role=gm is 403
// - Unknown roles return an error (400) but fail-closed to RolePlayer
// - Without identity (tests), validate role parameter but default to player
func roleOf(r *http.Request) (render.Role, error) {
	ident, ok := identity.IdentityFrom(r.Context())
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("role")))

	if !ok {
		// No identity in context (e.g., tests without auth middleware).
		// Still validate the role parameter.
		switch q {
		case "", "player":
			return render.RolePlayer, nil
		case "gm":
			return render.RoleGM, nil
		default:
			return render.RolePlayer, errors.New("role must be gm or player")
		}
	}

	switch ident.Role {
	case "gm":
		if q == "" || q == "gm" {
			return render.RoleGM, nil
		}
		if q == "player" {
			return render.RolePlayer, nil
		}
		return render.RolePlayer, errors.New("role must be gm or player")
	case "player":
		if q == "gm" {
			return render.RolePlayer, errors.New("forbidden: player cannot request gm role")
		}
		// Default to player for empty, "player", or any other value
		return render.RolePlayer, nil
	default:
		// Unknown role: fail closed
		return render.RolePlayer, nil
	}
}

// buildNoteDoc renders note for role and attaches backlinks + ETag.
// The Content field is stripped of :::gm blocks for non-GM readers,
// unless the note is a character note owned by the requesting user
// (characters/<user_id>/*).
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

	// Determine the effective role for Content stripping.
	// GM role sees everything. For player role, check character ownership.
	contentRole := role
	if role == render.RolePlayer {
		ident, ok := identity.IdentityFrom(r.Context())
		if ok && isCharacterOwner(note.Path, ident.ID) {
			// Character owners see their own GM blocks
			contentRole = render.RoleGM
		}
	}

	content := note.Content
	if contentRole == render.RolePlayer {
		content = string(markdown.StripGMBlocks([]byte(note.Content), markdown.RolePlayer))
	}

	// Block ids come from the raw content (same fence rules the renderer and
	// StripGMBlocks use), so gm/player clients agree on them. Ids only — no
	// block content is exposed to roles that had it stripped.
	var gmBlocks []string
	if n := markdown.CountGMBlocks([]byte(note.Content)); n > 0 {
		gmBlocks = make([]string, 0, n)
		for i := 0; i < n; i++ {
			gmBlocks = append(gmBlocks, fmt.Sprintf("gm-%d", i))
		}
	}

	return NoteDoc{
		Path:      note.Path,
		Content:   content,
		Fragment:  fragment,
		Meta:      meta,
		GMBlocks:  gmBlocks,
		Backlinks: backlinks,
		ETag:      note.Hash,
	}, nil
}

// isCharacterOwner reports whether the given note path is a character note
// owned by the given user ID (i.e., characters/<userID>/*).
func isCharacterOwner(notePath, userID string) bool {
	if userID == "" {
		return false
	}
	// Path is vault-relative, slash-separated
	// Check if it starts with "characters/<userID>/"
	prefix := "characters/" + userID + "/"
	return strings.HasPrefix(notePath, prefix)
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
	// Derived-content leak fix: a player must never see campaign/* paths in
	// the list. Filter per-item through the vault ACL (ModeRead). Nil VaultCan
	// means auth is deferred to middleware; every route here is behind
	// RequireSession already, so an identity is present.
	if d.VaultCan != nil {
		kept := make([]string, 0, len(paths))
		for _, p := range paths {
			if d.VaultCan(r.Context(), p, "read") {
				kept = append(kept, p)
			}
		}
		paths = kept
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
		if strings.HasPrefix(err.Error(), "forbidden:") {
			WriteProblem(w, http.StatusForbidden, "forbidden", strings.TrimPrefix(err.Error(), "forbidden: "))
		} else {
			WriteProblem(w, http.StatusBadRequest, "bad request", err.Error())
		}
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
		if strings.HasPrefix(err.Error(), "forbidden:") {
			WriteProblem(w, http.StatusForbidden, "forbidden", strings.TrimPrefix(err.Error(), "forbidden: "))
		} else {
			WriteProblem(w, http.StatusBadRequest, "bad request", err.Error())
		}
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
