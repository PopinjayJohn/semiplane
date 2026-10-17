package markdown

import (
	"io"
	"strings"

	gast "github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/renderer"
	"github.com/yuin/goldmark/v2/renderer/html"
	"github.com/yuin/goldmark/v2/util"
)

// Fence languages this pipeline interprets structurally instead of
// rendering as <pre><code>. Every other info string falls through to the
// stock fenced-code rendering (mirrored below), so Go/Rust/dataview
// snippets keep working untouched.
const (
	fenceStatblock = "ad-statblock"
	fenceDice      = "dice"
)

// semiplaneHTMLRenderer wires every Semiplane node renderer in one
// html.Extension. The KindCodeBlock entry overrides stock fence rendering
// only for the two structural languages; anything else is delegated to
// renderStockFence, a line-for-line mirror of goldmark's own
// renderCodeBlock under the pinned v2.1.6 (language class + escaped
// content + "</code></pre>"). If the pin ever moves, re-diff this func
// against renderer/html — the override must stay invisible for ordinary
// fences.
type semiplaneHTMLRenderer struct{}

func (e semiplaneHTMLRenderer) RendererOptions(_ *html.Config) []html.Option {
	return []html.Option{
		html.WithNodeRenderers(map[gast.NodeKind]html.NodeRenderer{
			KindWikiLink:       html.NodeRendererFunc(renderWikiLink),
			KindGMBlock:        html.NodeRendererFunc(renderGMBlock),
			KindCallout:        html.NodeRendererFunc(renderCallout),
			gast.KindCodeBlock: html.NodeRendererFunc(renderFenced),
		}),
	}
}

func fenceLanguage(source []byte, n *gast.CodeBlock) string {
	lang, ok := n.Language(source)
	if !ok {
		return ""
	}
	lang, _, _ = strings.Cut(lang, " ")
	return strings.ToLower(lang)
}

// fenceLines collects the raw fence content lines (trailing newline
// trimmed, CRLF tolerated).
func fenceLines(source []byte, n *gast.CodeBlock) []string {
	var out []string
	for _, seg := range n.Value.Segments() {
		line := seg.Str(source)
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		out = append(out, line)
	}
	return out
}

func renderFenced(w io.Writer, source []byte, node gast.Node, entering bool, rc renderer.Context) (gast.WalkStatus, error) {
	n := node.(*gast.CodeBlock)
	if n.CodeBlockKind != gast.CodeBlockKindFenced {
		return renderStockFence(w, source, node, entering, rc)
	}
	switch fenceLanguage(source, n) {
	case fenceStatblock:
		return renderStatblock(w, source, n, entering)
	case fenceDice:
		return renderDiceFence(w, source, n, entering)
	default:
		return renderStockFence(w, source, node, entering, rc)
	}
}

// renderStockFence mirrors goldmark's default fenced-code output.
func renderStockFence(w io.Writer, source []byte, node gast.Node, entering bool, rc renderer.Context) (gast.WalkStatus, error) {
	bw := w.(util.BufWriter)
	n := node.(*gast.CodeBlock)
	if entering {
		tw := html.ContextTextWriter(rc)
		_, _ = bw.WriteString("<pre><code")
		if language, ok := n.Language(source); ok {
			_, _ = bw.WriteString(` class="language-`)
			_, _ = tw.WriteString(language)
			_ = bw.WriteByte('"')
		}
		_ = bw.WriteByte('>')
		_, _ = n.Value.WriteTo(tw, source)
	} else {
		_, _ = bw.WriteString("</code></pre>\n")
	}
	return gast.WalkContinue, nil
}

// renderStatblock renders an ```ad-statblock fence as a definition list:
// "key: value" lines become <dt>/<dd> rows (first colon splits), other
// non-blank lines become statblock-text paragraphs. Malformed lines never
// drop content — they degrade to text rows.
func renderStatblock(w io.Writer, source []byte, n *gast.CodeBlock, entering bool) (gast.WalkStatus, error) {
	bw := w.(util.BufWriter)
	if entering {
		_, _ = bw.WriteString(`<div class="ad-statblock" data-testid="statblock">` + "\n<dl>\n")
		for _, line := range fenceLines(source, n) {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			key, value, found := strings.Cut(line, ":")
			if !found || strings.TrimSpace(key) == "" {
				_, _ = bw.WriteString(`<div class="statblock-text"><p>`)
				_, _ = bw.Write(util.EscapeHTML([]byte(line)))
				_, _ = bw.WriteString("</p></div>\n")
				continue
			}
			_, _ = bw.WriteString(`<div class="statblock-row"><dt>`)
			_, _ = bw.Write(util.EscapeHTML([]byte(strings.TrimSpace(key))))
			_, _ = bw.WriteString("</dt><dd>")
			_, _ = bw.Write(util.EscapeHTML([]byte(strings.TrimSpace(value))))
			_, _ = bw.WriteString("</dd></div>\n")
		}
	} else {
		_, _ = bw.WriteString("</dl>\n</div>\n")
	}
	return gast.WalkContinue, nil
}

// renderDiceFence renders a ```dice fence as a roll affordance: the
// formula is inert server-side (stream D owns evaluation; the same engine
// later serves /api/v1/dice/roll), so this emits a data-formula hook the
// SPA dice tray binds to. Multi-line content collapses to one formula by
// joining non-blank lines with spaces.
func renderDiceFence(w io.Writer, source []byte, n *gast.CodeBlock, entering bool) (gast.WalkStatus, error) {
	if !entering {
		return gast.WalkContinue, nil
	}
	var parts []string
	for _, line := range fenceLines(source, n) {
		if t := strings.TrimSpace(line); t != "" {
			parts = append(parts, t)
		}
	}
	formula := strings.Join(parts, " ")
	bw := w.(util.BufWriter)
	_, _ = bw.WriteString(`<div class="dice-block" data-testid="dice-roll" data-formula="`)
	_, _ = bw.Write(util.EscapeHTML([]byte(formula)))
	_, _ = bw.WriteString(`"><code class="dice-formula">`)
	_, _ = bw.Write(util.EscapeHTML([]byte(formula)))
	_, _ = bw.WriteString("</code></div>\n")
	return gast.WalkContinue, nil
}
