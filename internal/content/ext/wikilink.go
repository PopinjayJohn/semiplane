package ext

import (
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// Wikilink returns the definition for `[[Page]]`.
//
// The element is an `<a>` with no `href`, which is deliberate and is the whole
// seam with `internal/content/links.go`:
//
//   - A bare `<a>` is a valid, inert inline element. It is not focusable, not
//     activatable, and renders exactly as a `<span>`, so an unresolved reference
//     looks like unresolved text rather than like a link that goes nowhere —
//     which is the truth, and which is also what a broken-link report will say.
//   - C4 attaches the `href` after resolution, keyed on `data-ref-index`, which is
//     this node's ordinal in the `References` slice `Renderer.Render` returned. It
//     rewrites one attribute on one element per reference; it does not re-parse the
//     HTML, and it does not re-render.
//   - A `href` is deliberately *not* guessed here. A guess would be a resolution
//     with a different answer from C4's, and ADR 0017 requires the linking page's
//     HTML to be byte-identical for every viewer — which it cannot be if a
//     resolution that depends on the viewer's visible campaigns happens before
//     the cache. So the href is C4's, and only C4's.
//
// The label is the author's text: the alias, or the target when there is no
// alias, and never the target page's title. That is ADR 0017's rule and it is
// also why the reference is not resolved here — reading the target's front matter
// at render time is what would make the HTML vary by viewer.
func Wikilink() Definition {
	return Definition{
		Kind:    KindWikilink,
		Trigger: openBracket,
		Write:   writeWikilink,
	}
}

// writeWikilink renders one wikilink element.
func writeWikilink(writer util.BufWriter, node *Node) error {
	if err := open(writer, "a", node); err != nil {
		return err
	}

	// The label is the one part of this element that is author text, and it is
	// escaped for the same reason every other interpolation here is: it came out of
	// a file an Obsidian sync wrote.
	if err := write(writer, ">"+escape(label(node.inner))); err != nil {
		return err
	}

	return write(writer, "</a>")
}

// ensure the definition and its writer are wired to the interfaces they are
// installed as, at compile time.
var (
	_ parser.InlineParser   = (*refParser)(nil)
	_ renderer.NodeRenderer = (*nodeRenderer)(nil)
	_ gast.Node             = (*Node)(nil)
)
