package markdown

import (
	"bytes"
	"io"
	"net/url"
	"strings"

	gast "github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer"
	"github.com/yuin/goldmark/v2/text"
	"github.com/yuin/goldmark/v2/util"
)

// WikiLink is an Obsidian-style internal link: [[page]], [[page|alias]]
// or [[page#anchor|alias]]. Stream B (internal/index) resolves targets to
// backlinks; the renderer only emits a deterministic href — link existence
// is an index concern, never a render-time one, so missing targets still
// render as links (class wikilink) rather than 404ing at authoring time.
type WikiLink struct {
	gast.BaseInline
	// Target is the link target page as written (trimmed, without #anchor
	// or |alias parts).
	Target string
	// Anchor is the optional #heading fragment, without the '#'.
	Anchor string
	// Text is the rendered label: the |alias when present, else the full
	// target as written (including any #anchor).
	Text string
}

// KindWikiLink is the NodeKind of WikiLink nodes.
var KindWikiLink = gast.NewNodeKind("WikiLink")

// Kind implements gast.Node.Kind.
func (n *WikiLink) Kind() gast.NodeKind { return KindWikiLink }

// Dump implements gast.Node.Dump.
func (n *WikiLink) Dump(source []byte) *gast.NodeDump {
	return gast.NewNodeDump(n, map[string]any{
		"Target": n.Target,
		"Anchor": n.Anchor,
		"Text":   n.Text,
	})
}

// NewWikiLink returns a WikiLink node. Callers must pass trimmed parts.
func NewWikiLink(target, anchor, text string) *WikiLink {
	n := &WikiLink{Target: target, Anchor: anchor, Text: text}
	n.Init(n)
	return n
}

type wikilinkParser struct{}

func (s *wikilinkParser) Trigger() []byte { return []byte{'['} }

// Parse matches a single-line [[...]] span. Multi-line spans, empty
// targets and nested [[ are left literal: a half-written link must stay
// visible (and editable) rather than vanish into a broken node.
func (s *wikilinkParser) Parse(_ gast.Node, block text.Reader, _ parser.Context) gast.Node {
	line, _ := block.PeekLine()
	if len(line) < 5 || line[0] != '[' || line[1] != '[' {
		return nil
	}
	rest := line[2:]
	end := bytes.Index(rest, []byte("]]"))
	if end < 0 {
		return nil
	}
	inner := rest[:end]
	if len(inner) == 0 || bytes.Contains(inner, []byte("[[")) {
		return nil
	}
	target, display, _ := strings.Cut(string(inner), "|")
	page, anchor, _ := strings.Cut(target, "#")
	page = strings.TrimSpace(page)
	anchor = strings.TrimSpace(anchor)
	if page == "" {
		return nil
	}
	label := strings.TrimSpace(display)
	if label == "" {
		label = strings.TrimSpace(string(inner))
	}
	block.Advance(end + 4) // "[[" + inner + "]]"
	return NewWikiLink(page, anchor, label)
}

type wikilinkParserExtension struct{}

func (e wikilinkParserExtension) ParserOptions(_ *parser.Config) []parser.Option {
	return []parser.Option{
		// Before the stock link parser (200) so [[ is never misread as
		// an empty-text [...] shortcut.
		parser.WithInlineParsers(util.Prioritized[parser.InlineParser](&wikilinkParser{}, 150)),
	}
}

// wikilinkHref builds the SPA path for a target page. Shape is
// /notes/<page>[#<anchor>] with segments path-escaped; a trailing .md is
// tolerated and stripped so [[page]] and [[page.md]] converge.
// ASSUMPTION (needs stream F ruling): the SPA wiki route. If F routes
// pages elsewhere, only this function changes — node shape is unaffected.
func wikilinkHref(target, anchor string) string {
	page := strings.TrimSuffix(target, ".md")
	var b strings.Builder
	b.WriteString("/notes/")
	b.WriteString(url.PathEscape(page))
	if anchor != "" {
		b.WriteByte('#')
		b.WriteString(url.PathEscape(anchor))
	}
	return b.String()
}

func renderWikiLink(w io.Writer, _ []byte, node gast.Node, entering bool, _ renderer.Context) (gast.WalkStatus, error) {
	n := node.(*WikiLink)
	if !entering {
		return gast.WalkContinue, nil
	}
	bw := w.(util.BufWriter)
	_, _ = bw.WriteString(`<a class="wikilink" data-testid="wikilink" href="`)
	_, _ = bw.Write(util.EscapeHTML([]byte(wikilinkHref(n.Target, n.Anchor))))
	_, _ = bw.WriteString(`">`)
	_, _ = bw.Write(util.EscapeHTML([]byte(n.Text)))
	_, _ = bw.WriteString(`</a>`)
	return gast.WalkContinue, nil
}
