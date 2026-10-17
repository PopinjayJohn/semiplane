package markdown

import (
	"bytes"
	"fmt"

	"github.com/yuin/goldmark/v2/extension"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer/html"
	"github.com/yuin/goldmark/v2/util"
)

// Pipeline is a role-bound Markdown→HTML converter: one goldmark parser
// plus one HTML renderer, composed once so stream E (internal/api) pays
// construction per role, not per request. Construct with New; a Pipeline
// is safe for concurrent Convert calls (goldmark parsers hold no
// per-parse state outside the parse Context).
type Pipeline struct {
	role     Role
	parser   parser.Parser
	renderer html.Renderer
}

// New builds the full content pipeline for role: CommonMark + GFM tables,
// strikethrough and task lists, plus the Semiplane extensions (wikilinks,
// :::gm blocks, callouts, ad-statblock/dice fences). The GM-strip
// transformer is bound to role here, so a player Pipeline can never emit
// secret subtrees even if the caller forgets to filter. Unknown roles are
// a hard error — silently defaulting to either visibility would be a
// secret-leak (or secret-loss) bug waiting for a typo.
func New(role Role) (*Pipeline, error) {
	if !role.Valid() {
		return nil, fmt.Errorf("markdown: unknown role %q (want %q or %q)", role, RoleGM, RolePlayer)
	}
	p := parser.New(
		parser.WithExtensions(
			extension.TableParser,
			extension.StrikethroughParser,
			extension.TaskListItemParser,
			wikilinkParserExtension{},
			gmParserExtension{},
			calloutParserExtension{},
		),
		parser.WithASTTransformers(
			util.Prioritized(newStripGMs(role), stripGMsPriority),
		),
	)
	r := html.New(
		html.WithExtensions(
			extension.TableHTMLRenderer,
			extension.StrikethroughHTMLRenderer,
			extension.TaskListItemHTMLRenderer,
			semiplaneHTMLRenderer{},
		),
		// Deliberately no html.WithUnsafe(): raw HTML blocks/inline render
		// as "<!-- raw HTML omitted -->" (Unsafe defaults to false).
		// Rendered vault content is untrusted input — strict CSP depends
		// on the server never passing author HTML through.
	)
	return &Pipeline{role: role, parser: p, renderer: r}, nil
}

// Role reports the viewer role this Pipeline renders for.
func (pl *Pipeline) Role() Role { return pl.role }

// Convert parses body and renders it to HTML for the Pipeline's role.
func (pl *Pipeline) Convert(body []byte) ([]byte, error) {
	doc := pl.parser.Parse(body)
	var buf bytes.Buffer
	if err := pl.renderer.Render(&buf, body, doc); err != nil {
		return nil, fmt.Errorf("markdown: render: %w", err)
	}
	return buf.Bytes(), nil
}
