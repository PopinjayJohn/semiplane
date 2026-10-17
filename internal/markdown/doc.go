// Package markdown is the Semiplane content pipeline: Obsidian-flavored
// Markdown in, role-aware HTML out.
//
// The pipeline is three phases over goldmark v2
// (github.com/yuin/goldmark/v2):
//
//  1. Frontmatter is split off first with ParseFrontmatter (yaml.v3; there
//     is no goldmark-meta port for v2, so this stays a pre-parse step).
//  2. Parsers build the AST: CommonMark + GFM tables/strikethrough/task
//     lists plus Semiplane extensions — [[wikilink]] inline nodes, :::gm
//     secret-block containers, and > [!type] callouts.
//  3. A role-aware AST transformer strips GMBlock subtrees server-side for
//     non-GM roles, then the HTML renderer emits the tree with unsafe raw
//     HTML disabled.
//
// NOTE on goldmark v2 vs v1: v2 removed the top-level goldmark.New /
// Markdown interface / Extender. The pipeline constructor here (New)
// replaces it: parser.New + html.New are wired directly, with Unsafe left
// at its default (false) so raw HTML renders as "<!-- raw HTML omitted -->".
// No go.mod change was needed: the Phase-0 pin (v2.1.6) already applies.
package markdown

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Role is the viewer a render is produced for. GMBlock subtrees survive
// only for RoleGM; every other role gets them stripped server-side before
// rendering, so secret prose never reaches the client.
type Role string

const (
	// RoleGM sees everything, including :::gm secret blocks.
	RoleGM Role = "gm"
	// RolePlayer gets GMBlock subtrees stripped before rendering.
	RolePlayer Role = "player"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	return r == RoleGM || r == RolePlayer
}

// Doc is a parsed vault note: YAML frontmatter metadata plus the Markdown
// body that feeds the goldmark pipeline.
type Doc struct {
	// Meta is the frontmatter mapping (possibly empty, never nil after a
	// successful parse). Only the semiplane: namespace and plain display
	// keys live here; machine blocks stay in the body.
	Meta map[string]any
	// Body is the Markdown source with the frontmatter block removed.
	Body []byte
}

var (
	bom              = []byte{0xEF, 0xBB, 0xBF}
	frontmatterFence = []byte("---")
	yamlEnd          = []byte("...")
)

// ParseFrontmatter splits src into its YAML frontmatter mapping and the
// remaining Markdown body. A document has frontmatter only when its first
// line is exactly "---" (trailing spaces/tabs allowed) and a closing
// "---" (or YAML "..." end marker) line follows; otherwise the whole
// source is the body. CRLF line endings are accepted. Invalid YAML is a
// hard error: silently dropping author metadata would corrupt the index
// stream B builds from it.
func ParseFrontmatter(src []byte) (Doc, error) {
	rest := bytes.TrimPrefix(src, bom)
	first, restAfterFirst := cutLine(rest)
	if !isFenceLine(first, frontmatterFence) {
		return Doc{Meta: map[string]any{}, Body: src}, nil
	}
	var raw bytes.Buffer
	body := restAfterFirst
	for {
		if len(body) == 0 {
			// No closing fence: not frontmatter (e.g. a document that
			// opens with a thematic break). Fail open to literal body.
			return Doc{Meta: map[string]any{}, Body: src}, nil
		}
		var line []byte
		line, body = cutLine(body)
		if isFenceLine(line, frontmatterFence) || isFenceLine(line, yamlEnd) {
			break
		}
		raw.Write(line)
		raw.WriteByte('\n')
	}
	meta := map[string]any{}
	if out := bytes.TrimSpace(raw.Bytes()); len(out) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(out))
		dec.KnownFields(false)
		if err := dec.Decode(&meta); err != nil {
			return Doc{}, fmt.Errorf("markdown: invalid frontmatter YAML: %w", err)
		}
		if meta == nil {
			meta = map[string]any{}
		}
	}
	return Doc{Meta: meta, Body: body}, nil
}

// TagsFromMeta extracts the `tags` value from parsed frontmatter metadata
// for storage as a plain JSON string list. Only the TOP-LEVEL `tags` key is
// read: a nested `semiplane:` mapping's tags-like keys must not leak into
// the note's tag set.
//
// Accepted value shapes (tags carry case; declaration order is preserved —
// callers that need a canonical form must normalize at save time):
//
//	[]string            → as-is
//	[]any               → string items kept in order; non-string items
//	                      are dropped (yaml.v3 decodes flow and block
//	                      lists as []any, never []string)
//	string scalar       → one tag (matches the old index scalar
//	                      semantics; no comma splitting)
//	nil / absent key    → non-nil empty slice
//
// Each item is trimmed of surrounding whitespace and single/double quotes;
// empty results are dropped (preserving the trimmed behavior of the
// hand-rolled ParseTags this replaces).
func TagsFromMeta(meta map[string]any) []string {
	out := make([]string, 0)
	if meta == nil {
		return out
	}
	trimTags := func(items []any) {
		for _, raw := range items {
			s, ok := raw.(string)
			if !ok {
				continue // non-string item dropped
			}
			s = strings.TrimSpace(s)
			s = strings.Trim(s, `"'`)
			if s != "" {
				out = append(out, s)
			}
		}
	}
	switch v := meta["tags"].(type) {
	case []string:
		anyItems := make([]any, len(v))
		for i, s := range v {
			anyItems[i] = s
		}
		trimTags(anyItems)
	case []any:
		trimTags(v)
	case string:
		trimTags([]any{v})
	default:
		// nil, not a string/list, or absent: empty by contract.
	}
	return out
}

// cutLine splits the first line (without its ending) from b. It accepts
// "\n" and "\r\n" endings; the final line may be unterminated.
func cutLine(b []byte) (line, rest []byte) {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return bytes.TrimSuffix(b[:i], []byte{'\r'}), b[i+1:]
	}
	return bytes.TrimSuffix(b, []byte{'\r'}), nil
}

// isFenceLine reports whether line is exactly fence plus horizontal
// whitespace (the trailing newline is already stripped by cutLine).
func isFenceLine(line, fence []byte) bool {
	rest, ok := bytes.CutPrefix(line, fence)
	if !ok {
		return false
	}
	for _, c := range rest {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
}
