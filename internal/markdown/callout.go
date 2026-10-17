package markdown

import (
	"bytes"
	"io"
	"strings"

	gast "github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer"
	"github.com/yuin/goldmark/v2/text"
	"github.com/yuin/goldmark/v2/util"
)

// Callout is an Obsidian-style admonition: a "> [!type] Title" opener
// followed by ">"-prefixed body blocks. The marker line is consumed at
// parse time, so the title never duplicates into the body — the renderer
// emits it once as the callout heading.
type Callout struct {
	gast.BaseBlock
	// Type is the lowercased marker word ("note", "warning", ...).
	// Unrecognized types are kept verbatim (class callout-<type>) rather
	// than refused: an unknown admonition must degrade to a plain box,
	// never drop author prose.
	Type string
	// Title is the marker-line remainder, or "" when the marker stands
	// alone (the renderer then falls back to the capitalized type).
	Title string
}

// KindCallout is the NodeKind of Callout nodes.
var KindCallout = gast.NewNodeKind("Callout")

// Kind implements gast.Node.Kind.
func (n *Callout) Kind() gast.NodeKind { return KindCallout }

// Dump implements gast.Node.Dump.
func (n *Callout) Dump(source []byte) *gast.NodeDump {
	return gast.NewNodeDump(n, map[string]any{"Type": n.Type, "Title": n.Title})
}

// NewCallout returns a Callout node.
func NewCallout(typ, title string) *Callout {
	n := &Callout{Type: typ, Title: title}
	n.Init(n)
	return n
}

type calloutParser struct{}

func (b *calloutParser) Trigger() []byte { return []byte{'>'} }

// consumeMarker strips one "> " prefix and reports whether the line opens
// a callout. Only space-separated markers qualify — a tab after ">" stays
// a plain blockquote, mirroring the conservative side of blockquote.process.
func consumeMarker(line []byte) *Callout {
	pos := 1 // past '>'
	if pos < len(line) && line[pos] == ' ' {
		pos++
	}
	rest := line[pos:]
	if !bytes.HasPrefix(rest, []byte("[!")) {
		return nil
	}
	end := bytes.IndexByte(rest, ']')
	if end < 0 {
		return nil
	}
	typ := rest[2:end]
	if len(typ) == 0 || !isCalloutType(typ) {
		return nil
	}
	after := rest[end+1:]
	// The marker must end the line: "[!note]" or "[!note] Title".
	// Anything else ("[!note]x") is quote prose, left for blockquote.
	// '\r' is accepted so CRLF documents behave like LF ones (a bare
	// "[!note]\r\n" must still open a callout, not a quote).
	if len(after) > 0 && after[0] != ' ' && after[0] != '\t' && after[0] != '\n' && after[0] != '\r' {
		return nil
	}
	return NewCallout(strings.ToLower(string(typ)), string(bytes.TrimSpace(after)))
}

func isCalloutType(b []byte) bool {
	for _, c := range b {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func (b *calloutParser) Open(_ gast.Node, reader text.Reader, pc parser.Context) (gast.Node, parser.State) {
	line, _ := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 || pos >= len(line) || line[pos] != '>' {
		return nil, parser.NoChildren
	}
	node := consumeMarker(line[pos:])
	if node == nil {
		return nil, parser.NoChildren
	}
	// The marker line is fully consumed (title lives on the node); the
	// body starts on the following ">" lines.
	reader.AdvanceToEOL()
	return node, parser.HasChildren
}

// stripQuotePrefix mirrors blockquote.process: it consumes one "> "
// prefix so child blocks parse the remainder. Lines without the prefix
// close the callout (lazy paragraph continuation inside still works via
// the paragraph parser, exactly as in blockquotes).
func stripQuotePrefix(reader text.Reader) bool {
	line, _ := reader.PeekLine()
	if len(line) == 0 {
		return false
	}
	w, pos := util.IndentWidth(line, reader.LineOffset())
	if w > 3 || pos >= len(line) || line[pos] != '>' {
		return false
	}
	pos++
	if pos >= len(line) {
		reader.Advance(pos)
		return true
	}
	if line[pos] == '\n' {
		reader.Advance(pos)
		return true
	}
	reader.Advance(pos)
	if line[pos] == ' ' {
		reader.Advance(1)
		return true
	}
	if line[pos] == '\t' {
		padding := util.TabWidth(reader.LineOffset()) - 1
		reader.AdvanceAndSetPadding(1, padding)
		return true
	}
	return true
}

func (b *calloutParser) Continue(_ gast.Node, reader text.Reader, _ parser.Context) parser.State {
	if stripQuotePrefix(reader) {
		return parser.Continue | parser.HasChildren
	}
	return parser.Close
}

func (b *calloutParser) Close(_ gast.Node, _ text.Reader, _ parser.Context) {}

func (b *calloutParser) CanInterruptParagraph() bool { return true }

func (b *calloutParser) CanAcceptIndentedLine() bool { return false }

type calloutParserExtension struct{}

func (e calloutParserExtension) ParserOptions(_ *parser.Config) []parser.Option {
	return []parser.Option{
		// Before the stock blockquote parser (800) on '>' lines; lines
		// that are not callout markers fall through to it.
		parser.WithBlockParsers(util.Prioritized[parser.BlockParser](&calloutParser{}, 750)),
	}
}

func renderCallout(w io.Writer, _ []byte, node gast.Node, entering bool, _ renderer.Context) (gast.WalkStatus, error) {
	n := node.(*Callout)
	bw := w.(util.BufWriter)
	if entering {
		title := n.Title
		if title == "" {
			if n.Type == "" {
				title = "Note"
			} else {
				title = strings.ToUpper(n.Type[:1]) + n.Type[1:]
			}
		}
		_, _ = bw.WriteString(`<div class="callout callout-`)
		_, _ = bw.Write(util.EscapeHTML([]byte(n.Type)))
		_, _ = bw.WriteString(`" data-testid="callout" data-callout="`)
		_, _ = bw.Write(util.EscapeHTML([]byte(n.Type)))
		_, _ = bw.WriteString(`">` + "\n")
		_, _ = bw.WriteString(`<p class="callout-title">`)
		_, _ = bw.Write(util.EscapeHTML([]byte(title)))
		_, _ = bw.WriteString("</p>\n")
	} else {
		_, _ = bw.WriteString("</div>\n")
	}
	return gast.WalkContinue, nil
}
