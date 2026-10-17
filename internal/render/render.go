// Package render is the role-aware HTML surface over internal/markdown.
//
// Stream E (internal/api) consumes exactly one function: Render. The
// signature is frozen by the Phase-1 plan — Render(doc, role) — and any
// change to it is announce-only across streams (conflict map: stream E
// consumes this API).
//
// Security property: GM visibility is enforced by AST stripping inside
// the pipeline (RolePlayer trees never contain secret bytes), never by
// client-side hiding. Player HTML is safe to serve and to cache.
package render

import (
	"fmt"

	"github.com/PopinjayJohn/semiplane/internal/markdown"
)

// Role is the viewer a render is produced for. It aliases markdown.Role
// so both packages share one vocabulary: "gm" sees :::gm secret blocks,
// "player" gets them stripped server-side.
type Role = markdown.Role

const (
	// RoleGM renders everything, including :::gm secret blocks.
	RoleGM = markdown.RoleGM
	// RolePlayer strips :::gm subtrees before rendering.
	RolePlayer = markdown.RolePlayer
)

// Render converts a full vault note (YAML frontmatter included) to
// role-aware HTML. It returns the rendered fragment (no <html> wrapper —
// the SPA shell owns the document) plus the frontmatter metadata for the
// note endpoint to serve alongside the content.
//
// Frontmatter never leaks into the HTML: it is split off before parsing.
// Unknown roles and invalid frontmatter are hard errors; an empty note
// renders to "" with empty metadata.
// META_KEYS registry (stream E follow-up owed; stream F consumes this).
//
// Render returns frontmatter metadata as an untyped map[string]any because
// vault notes carry arbitrary per-system schemas (tech-stack §5: no
// migrations for new character fields). Known keys produced or consumed by
// the pipeline — documented here so the note endpoint shape stays stable
// for stream F (fragment + meta + backlinks + ETag):
//
//	title   string   first ATX heading fallback (index.ParseTitle)
//	tags    []string frontmatter `tags:` flow or block list (index.ParseTags)
//	semiplane mapping  machine-written namespace only (ingest tools,
//	                   marker blocks); user prose is never rewritten
//
// All other keys pass through untouched from the note's YAML frontmatter.
// Unknown keys are data, never errors.
func Render(doc []byte, role Role) (htmlOut string, meta map[string]any, err error) {
	if !role.Valid() {
		return "", nil, fmt.Errorf("render: unknown role %q", role)
	}
	parsed, err := markdown.ParseFrontmatter(doc)
	if err != nil {
		return "", nil, fmt.Errorf("render: %w", err)
	}
	pl, err := markdown.New(role)
	if err != nil {
		return "", nil, fmt.Errorf("render: %w", err)
	}
	out, err := pl.Convert(parsed.Body)
	if err != nil {
		return "", nil, fmt.Errorf("render: %w", err)
	}
	return string(out), parsed.Meta, nil
}
