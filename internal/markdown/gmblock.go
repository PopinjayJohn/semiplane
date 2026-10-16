package markdown

import (
	"bytes"
	"io"

	gast "github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer"
	"github.com/yuin/goldmark/v2/text"
	"github.com/yuin/goldmark/v2/util"
)

// GMBlock is a GM-secret container: everything between a :::gm opener and
// its ::: closer. Children are ordinary parsed Markdown, so secrets can
// hold any vault prose (lists, tables, wikilinks). Visibility is enforced
// by stripGMs before rendering — the renderer only ever sees GMBlocks for
// RoleGM, and emits them as <section> for the GM stylesheet / SSE reveal
// payloads (stream E) to target.
type GMBlock struct {
	gast.BaseBlock
	// Title is the optional opener suffix (":::gm <Title>"), rendered as a
	// heading line inside the section for GM readers.
	Title string
}

// KindGMBlock is the NodeKind of GMBlock nodes.
var KindGMBlock = gast.NewNodeKind("GMBlock")

// Kind implements gast.Node.Kind.
func (n *GMBlock) Kind() gast.NodeKind { return KindGMBlock }

// Dump implements gast.Node.Dump.
func (n *GMBlock) Dump(source []byte) *gast.NodeDump {
	return gast.NewNodeDump(n, map[string]any{"Title": n.Title})
}

// NewGMBlock returns a GMBlock node with the given opener title.
func NewGMBlock(title string) *GMBlock {
	n := &GMBlock{Title: title}
	n.Init(n)
	return n
}

var (
	gmOpener = []byte(":::gm")
	gmCloser = []byte(":::")
)

type gmBlockParser struct{}

func (b *gmBlockParser) Trigger() []byte { return []byte{':'} }

// isGMOpener reports whether line, at first-non-space pos, opens a GM
// block, and returns the stripped title (possibly empty).
func isGMOpener(line []byte, pos int) (string, bool) {
	rest := line[pos:]
	if !bytes.HasPrefix(rest, gmOpener) {
		return "", false
	}
	after := rest[len(gmOpener):]
	// The opener keyword must end here: bare ":::gm", or ":::gm <Title>".
	// '\r' is accepted so CRLF documents behave like LF ones — without
	// it a bare ":::gm\r\n" would stay literal prose and leak the secret
	// to every role (fail-open). See edge_test.go TestEdgeCRLFBareMarkers.
	if len(after) > 0 && after[0] != ' ' && after[0] != '\t' && after[0] != '\n' && after[0] != '\r' {
		return "", false // e.g. ":::gmx" is not our opener
	}
	return string(bytes.TrimSpace(after)), true
}

func (b *gmBlockParser) Open(_ gast.Node, reader text.Reader, pc parser.Context) (gast.Node, parser.State) {
	line, _ := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 || pos >= len(line) {
		return nil, parser.NoChildren
	}
	title, ok := isGMOpener(line, pos)
	if !ok {
		return nil, parser.NoChildren
	}
	reader.AdvanceToEOL()
	return NewGMBlock(title), parser.HasChildren
}

func (b *gmBlockParser) Continue(_ gast.Node, reader text.Reader, _ parser.Context) parser.State {
	line, _ := reader.PeekLine()
	if line == nil {
		return parser.Close
	}
	if bytes.Equal(bytes.TrimSpace(line), gmCloser) {
		reader.AdvanceToEOL()
		return parser.Close
	}
	// Any other line — including blank lines, headings, fences — belongs
	// to the secret body. The block therefore fails closed: an unclosed
	// :::gm swallows to EOF for every role (players see nothing, the GM
	// sees the leak and fixes the fence).
	return parser.Continue | parser.HasChildren
}

func (b *gmBlockParser) Close(_ gast.Node, _ text.Reader, _ parser.Context) {}

func (b *gmBlockParser) CanInterruptParagraph() bool { return true }

func (b *gmBlockParser) CanAcceptIndentedLine() bool { return false }

type gmParserExtension struct{}

func (e gmParserExtension) ParserOptions(_ *parser.Config) []parser.Option {
	return []parser.Option{
		parser.WithBlockParsers(util.Prioritized[parser.BlockParser](&gmBlockParser{}, 750)),
	}
}

func renderGMBlock(w io.Writer, _ []byte, node gast.Node, entering bool, _ renderer.Context) (gast.WalkStatus, error) {
	n := node.(*GMBlock)
	bw := w.(util.BufWriter)
	if entering {
		_, _ = bw.WriteString(`<section class="gm-block" data-testid="gm-block">` + "\n")
		if n.Title != "" {
			_, _ = bw.WriteString(`<p class="gm-title">`)
			_, _ = bw.Write(util.EscapeHTML([]byte(n.Title)))
			_, _ = bw.WriteString("</p>\n")
		}
	} else {
		_, _ = bw.WriteString("</section>\n")
	}
	return gast.WalkContinue, nil
}

// stripGMsPriority runs the GM strip after every other tree rewrite so no
// transformer can resurrect (or relocate out of) a secret subtree.
const stripGMsPriority = 900

type stripGMs struct {
	role Role
}

func newStripGMs(role Role) parser.ASTTransformer {
	return &stripGMs{role: role}
}

// Transform removes every GMBlock subtree unless role is RoleGM. Removal
// is collect-then-unlink: mutating sibling links during ast.Walk would
// skip nodes. The GM renderer therefore only ever runs for GM pipelines,
// but stripping (not renderer gating) is the security boundary — player
// HTML provably contains no secret bytes, regardless of renderer bugs.
func (s *stripGMs) Transform(doc *gast.Document, _ text.Reader, _ parser.Context) {
	if s.role == RoleGM {
		return
	}
	var victims []gast.Node
	_ = gast.Walk(doc, func(n gast.Node, entering bool) (gast.WalkStatus, error) {
		if entering {
			if _, ok := n.(*GMBlock); ok {
				victims = append(victims, n)
				return gast.WalkSkipChildren, nil
			}
		}
		return gast.WalkContinue, nil
	})
	for _, v := range victims {
		if parent := v.Parent(); parent != nil {
			parent.RemoveChild(v)
		}
	}
}
