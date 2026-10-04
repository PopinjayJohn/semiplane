package ext

// The `[!secret]` callout, rendered — for the readers who may see it.
//
// # The renderer never decides whether to render
//
// §5.6.1: the secret body is **absent from the response** for non-GM viewers, and
// the way it is absent is omission — the callout is removed from the *source*
// before the render, so the non-GM pipeline never constructs the string. See
// `internal/content/redact.go` for why every other position is worse, and
// `internal/content/secret.go` for the scanner that finds the span to cut.
//
// This file therefore has no policy to apply and no flag to check. It is handed a
// document in which a callout is either wholly present or wholly absent, and it
// renders the ones it is given. A gate here would be a second, weaker version of
// the real one, and a second version of a security control is a second chance to
// get it wrong.
//
// # Why this is a block and not an inline extension
//
// Every other extension in this package is an inline parser, and `Definition`
// carries `Trigger byte` and a `Write` over `*Node`, which embeds
// `ast.BaseInline`. A callout is none of that: Obsidian writes it as a block
// quote whose first paragraph is a header line, so goldmark hands it to us as
// `ast.Blockquote` with children — and by the time it is an AST, the `[!secret]-`
// header has become a text node with no record of where it started.
//
// So the inline `Definition` cannot express it, and pretending otherwise would
// mean either recovering the header from a text node by string-matching it (the
// "match a word near the construct" mistake this phase has made five times) or
// extending `Definition` with fields every inline extension would then carry and
// ignore. The block half is therefore its own thing: an AST transform that
// replaces a recognised block quote with a typed node, and a renderer for that
// node.
//
// # What the transform matches, and what it refuses
//
// The header must be the **first paragraph** of the block quote and the `[!secret]`
// must be at the **start** of it, because that is what Obsidian requires and
// anything looser would let a sentence mentioning the keyword become a secret with
// a body. The marker byte must be `-` or `+`; anything else is left alone, so a
// typo renders as the prose it is.

import (
	"fmt"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// secretAttribute marks a block quote the transform recognised as a callout.
//
// **An attribute on the block quote rather than a new node kind.** goldmark's
// renderer table is keyed by `ast.Kind`, so a distinct kind means replacing the
// node — `parent.SetChild` does not exist in this version of the library, and the
// portable spelling is a remove-then-append, which is node surgery on a subtree
// that has already been parsed. An attribute needs none of that, and the check
// stays exact: a block quote carries this attribute or it does not, and there is
// no string to guess at.
//
// The cost is that this package registers a renderer for `ast.KindBlockquote`
// and therefore shadows goldmark's default for **every** block quote on every
// page. So the writer below delegates to a plain block quote for the ones without
// the attribute, and `TestAnOrdinaryBlockQuoteStillRenders` holds that it produces
// goldmark's own output. That is the trade: a shadowed default in exchange for no
// subtree surgery.
const secretAttribute = "sp-secret"

// The attribute's value, which is the marker byte as a string.
//
// **Stored rather than inferred from the value's presence**, so a GM's page and a
// player's differ in one attribute a test can read, and so the two states cannot
// be confused by a refactor that changes what else the node carries.
const (
	secretCollapsed = "collapsed"
	secretRevealed  = "revealed"
)

// blockQuoteWriter renders block quotes: the callout when the transform marked
// one, and goldmark's plain block quote when it did not.
type blockQuoteWriter struct{}

// RegisterFuncs implements `renderer.NodeRenderer`. The plural is the library's
// spelling; a `RegisterFunc` here is simply never called.
func (*blockQuoteWriter) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindBlockquote, renderBlockQuote)
}

// renderBlockQuote writes a block quote, as a callout when it is one.
func renderBlockQuote(
	writer util.BufWriter,
	source []byte,
	node ast.Node,
	entering bool,
) (ast.WalkStatus, error) {
	state := secretStateOf(node)

	if state == "" {
		// Not ours. **Exactly the element goldmark writes**, including the
		// attribute order, so a page with no secret callout is byte-identical to
		// one rendered without this package installed — which is the property that
		// makes the shadowing above safe to do at all.
		if entering {
			if _, err := writer.WriteString("<blockquote>\n"); err != nil {
				return ast.WalkStop, fmt.Errorf("ext: write blockquote: %w", err)
			}
		} else {
			if _, err := writer.WriteString("</blockquote>\n"); err != nil {
				return ast.WalkStop, fmt.Errorf("ext: close blockquote: %w", err)
			}
		}

		return ast.WalkContinue, nil
	}

	// The state and the class are derived from one value so they cannot disagree:
	// a `secret--collapsed` class on a `revealed` callout would be a styling lie
	// about who may see it.
	class := "secret secret--" + state

	if entering {
		_, err := writer.WriteString(
			`<div class="` + class + `" data-ext="secret" data-secret="` + state + `">`)
		if err != nil {
			return ast.WalkStop, fmt.Errorf("ext: open secret callout: %w", err)
		}

		return ast.WalkContinue, nil
	}

	if _, err := writer.WriteString("</div>\n"); err != nil {
		return ast.WalkStop, fmt.Errorf("ext: close secret callout: %w", err)
	}

	return ast.WalkContinue, nil
}

// secretStateOf reads the marker's state off a node, or "" when it carries none.
func secretStateOf(node ast.Node) string {
	value, found := node.AttributeString(secretAttribute)
	if !found {
		return ""
	}

	// **`[]byte`, not `string`.** goldmark's `SetAttributeString` stores whatever
	// it was handed through `any` and does not convert, so a `string` assertion
	// here fails and every block quote reads as an ordinary one -- which is not a
	// crash and not a wrong page, it is a page that renders correctly and holds a
	// secret nobody can reveal. Both shapes are accepted because the other is the
	// one a reader would reach for.
	switch state := value.(type) {
	case []byte:
		return string(state)
	case string:
		return state
	default:
		return ""
	}
}

// secretTransform is the AST transform that marks the callouts.
//
// # It runs at this package's shared priority, 150, and after goldmark's own
//
// `footnote` registers its transform at 999 and `linkify` at 999, so both have
// already run by the time this does. That is correct, and it is worth saying why
// rather than leaving a plausible-sounding claim that it runs first: this
// transform reads the header line's **source**, not the inline tree, and rewrites
// the tree from that source range. Whatever an earlier transform made of
// `[!secret]- ^traitor` does not matter, because `^traitor` is not a footnote
// marker and `[!secret]` is not a link.
//
// The earlier worry -- that a header resolved before this ran would leave an anchor
// in the document as text -- was real but misfiled. The block id is cut by
// `keepTitle`, from the same byte range that cuts the marker, so it does not depend
// on running first at all.
type secretTransform struct{}

// Transform implements `parser.ASTTransformer`.
func (*secretTransform) Transform(node *ast.Document, reader text.Reader, _ parser.Context) {
	// The walk's error is discarded, as it is in `render.go`'s collector: the only
	// callback below returns a nil error, and `Transform` has no error to report
	// one through, so there is nothing a caller could do with it.
	//nolint:errcheck // see above, and render.go:458.
	ast.Walk(node, func(current ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || current.Kind() != ast.KindBlockquote {
			return ast.WalkContinue, nil
		}

		quote, isQuote := current.(*ast.Blockquote)
		if !isQuote {
			return ast.WalkContinue, nil
		}

		head, revealed, matched := matchCallout(quote, reader)
		if !matched {
			return ast.WalkContinue, nil
		}

		quote.SetAttributeString(secretAttribute, []byte(stateOf(revealed)))

		// The header is **cut down to its title**, not left alone and not
		// re-written. Leaving it alone would print the author's `[!secret]-` as
		// visible text on a callout whose element already says what it is, and
		// re-writing it would mean a second copy of author text, escaped a second
		// time and held in a second place. Truncating the text node's segment
		// keeps the title as the *original source bytes* in the *original node*,
		// rendered by goldmark's own text renderer -- so it is escaped exactly
		// once, and exactly as any other paragraph is.
		head.keepTitle()

		return ast.WalkContinue, nil
	})
}

// header is a block quote's first paragraph, recognised as a callout marker.
type header struct {
	paragraph *ast.Paragraph

	// base and stop are the document offsets bounding the **header line**: `base`
	// is its first content byte (after the `> ` quoting, which goldmark strips
	// before the paragraph sees it) and `stop` is one past its content, newline
	// included.
	//
	// They are kept because the header and the body are usually **one paragraph**.
	// CommonMark makes `> [!secret]-` and `> The traitor` a single paragraph with a
	// soft line break between them, because nothing separates them -- Obsidian's
	// callout marker is not a block, it is the first line of a block quote's first
	// paragraph. So a transform that removed the header paragraph removed the body
	// with it, and §5.6's own example rendered as an empty div. Cutting a *node*
	// range is what makes the two separable.
	base, stop int

	// start and end are byte offsets **within the header line**, added to base to
	// make a document offset. They are relative because base is a number goldmark
	// computed and this package did not, so quoting them as absolutes would be
	// claiming to know where the line began.
	start, end int
}

// keepTitle rewrites the header paragraph so the marker is gone and both the title
// and the body survive.
//
// # Why this is not one call
//
// CommonMark makes `> [!secret]-` and `> The traitor` **one paragraph** with a soft
// line break between them: Obsidian's callout marker is not a block, it is the
// first line of a block quote's first paragraph. So the thing to cut out is a *byte
// range of a line inside a paragraph*, not a node, and three things follow from
// that and each has a failure:
//
//   - **A node cannot simply be removed.** Removing the header paragraph removes
//     the body with it, and §5.6's own example renders as an empty div.
//   - **A node cannot simply be truncated either.** `[` is an inline trigger, so
//     goldmark splits `[!secret]- Aldric` across four nodes and the first holds one
//     byte. A node-by-position walk cannot say which of the four are the marker's.
//   - **A range that is not the title's does not work either.** Cutting only the
//     marker leaves `!secret]-` on screen, because the node holding `!secret]- `
//     straddles the boundary.
//
// So the rule is: every inline node belonging to the header line is **clipped** to
// the title's byte range, and a node that clips to nothing is removed. Clipping
// keeps the title's own inline formatting -- `> [!secret]- **Aldric**` renders its
// emphasis -- because the nodes that survive are the ones goldmark already parsed.
//
// # What is deliberately not clipped
//
// Only nodes **within the header line**. The body lives in the same paragraph, one
// byte range later, and a rule that reached past the line's end would join the
// body's own paragraph breaks into one run of text. The line's bound is `stop`.
//
// # One thing this file used to have, and why it is gone
//
// An earlier version removed the header line's soft break **explicitly**, on the
// theory that a titleless callout would otherwise open its paragraph with a line
// break. It was instrumented and found to never fire: the range rule already takes
// that node, because the break sits at the very end of the header line and the
// titleless case's title range is empty. So it was dead code carrying a comment
// that claimed a behaviour, which is the arrangement this repository has been bitten
// by repeatedly -- a claim in a comment that nothing exercises, outliving the fact
// it describes. It is deleted rather than kept "in case", and
// `TestTheBodySurvivesTheCut` covers the behaviour that mattered.
func (head *header) keepTitle() {
	titleFrom, titleTo := head.base+head.start, head.base+head.end

	for child := head.paragraph.FirstChild(); child != nil; {
		next := child.NextSibling()

		node, isText := child.(*ast.Text)
		if !isText {
			// A non-text inline node -- emphasis, a link, a code span -- can only
			// belong to the title or the body, never to the marker: `[!secret]-` is
			// plain text however goldmark chose to split it. So it is left alone,
			// and the consequence is stated rather than hidden: a callout whose
			// *title* is empty but which carries inline markup on the header line
			// keeps it. That shape is not legal anyway -- there is nothing after the
			// marker for markup to sit in.
			child = next

			continue
		}

		segment := node.Segment
		if segment.Stop > head.stop {
			// The body. Hands off.
			child = next

			continue
		}

		switch {
		case segment.Stop <= titleFrom || segment.Start >= titleTo:
			head.paragraph.RemoveChild(head.paragraph, child)
		case segment.Start < titleFrom || segment.Stop > titleTo:
			node.Segment = text.NewSegment(
				max(segment.Start, titleFrom),
				min(segment.Stop, titleTo),
			)
		}

		child = next
	}
}

// stateOf is the attribute value for a marker byte, derived from the same value
// the element's class is derived from so the two cannot disagree: a
// `secret--collapsed` class on a revealed callout is a styling lie about who may
// see it.
func stateOf(revealed bool) string {
	if revealed {
		return secretRevealed
	}

	return secretCollapsed
}

// matchCallout answers whether a block quote opens a secret callout, and if so
// returns its header paragraph together with the byte range of the title inside it.
//
// The header must be the block quote's **first child**, and it must be a paragraph:
// Obsidian writes `> [!secret]- Title`, and anything else is a paragraph that
// happens to mention the keyword, which is prose and must stay prose. This is the
// construct rather than a word near it, which is the distinction this phase has had
// to learn five times.
func matchCallout(quote *ast.Blockquote, reader text.Reader) (*header, bool, bool) {
	first := quote.FirstChild()
	if first == nil {
		return nil, false, false
	}

	paragraph, isParagraph := first.(*ast.Paragraph)
	if !isParagraph {
		return nil, false, false
	}

	// **The header is read from the paragraph's own source lines, not from its
	// child text nodes.** `[` is an inline trigger, so goldmark splits the very
	// line this has to recognise: `> [!secret]- Aldric` parses to a `Text` node
	// holding `[`, then more nodes, and the first one holds a single byte. A
	// transform that read the first child -- which is the obvious thing to write --
	// therefore sees `[` and matches nothing, and the callout renders as a plain
	// block quote with the marker visible in it. `Lines()` is the paragraph's
	// source before inline parsing touched it, and it is the only place the
	// header line still exists as the author wrote it.
	lines := paragraph.Lines()
	if lines.Len() == 0 {
		return nil, false, false
	}

	firstLine := lines.At(0)

	head := &header{paragraph: paragraph, base: firstLine.Start}

	// **The trailing newline is trimmed before anything is read off the line.**
	// goldmark's paragraph lines carry it, and it is load-bearing twice over: the
	// block id check would see `traitor\n` and reject it as not an id -- so the id
	// stays in the title and renders as visible text -- and the title's end offset
	// would run one byte past the line, so the title paragraph ends with a newline
	// the author did not write. `base` is unaffected, because trimming the tail
	// moves nothing that comes before it.
	line := strings.TrimRight(string(firstLine.Value(reader.Source())), "\r\n")

	head.stop = head.base + len(line) + 1

	remainder, cut := strings.CutPrefix(line, calloutPrefix)
	if !cut || remainder == "" {
		return nil, false, false
	}

	var revealed bool

	switch remainder[0] {
	case '-':
		revealed = false
	case '+':
		revealed = true
	default:
		// A marker that is neither is not a callout. §5.6's grammar is closed,
		// and a third byte rendering as a callout would let a typo carry a body.
		return nil, false, false
	}

	// The title begins after the marker byte and ends before the trailing block
	// id. Trimming the padding on both sides is a real requirement rather than
	// tidiness: this range is where the title is cut *out of*, so a range that
	// included the author's padding would render whitespace they did not write,
	// and one that stopped early would render half a word.
	rest := remainder[1:]
	lead := len(rest) - len(strings.TrimLeft(rest, " \t"))
	trailing := len(strings.TrimRight(rest, " \t"))

	head.start = len(calloutPrefix) + 1 + lead
	head.end = len(calloutPrefix) + 1 + trailing

	// The block id is trailing, and cutting it is part of cutting the title: a
	// header that rendered `^traitor` would put an anchor in the document as
	// text, where it resolves for nobody and reads to a GM as a typo.
	if before, after, found := strings.CutLast(rest, " ^"); found && isBlockID(after) {
		// **The spaces before the caret go too.** Cutting at the caret's own offset
		// would leave the padding the author typed between the title and the id,
		// so `> [!secret]- Aldric  ^traitor` would render as `Aldric ` -- a
		// trailing space, which is invisible in a preview and obvious in a diff
		// against Obsidian's own output.
		head.end = len(calloutPrefix) + 1 + len(strings.TrimRight(before, " \t"))
	}

	return head, revealed, true
}

// SecretBlockExtension installs the callout transform and its renderer.
//
// **Not a `Definition`, and named differently on purpose.** `Definition` is the
// inline mechanism and every field of it is inline-shaped. Returning one of those
// for a block construct would invite a caller to wire it through `New(defs...)`
// and get a parser that never fires.
//
// `New` installs it automatically once a definition with `Kind == KindSecret` is
// present, so the ordinary `ext.New(ext.Builtins()...)` call site does not change
// — which is what keeps "adding an extension is adding a definition" true for the
// inline case without lying about the block one.
func SecretBlockExtension() (parser.ASTTransformer, renderer.NodeRenderer) {
	return &secretTransform{}, &blockQuoteWriter{}
}

// calloutPrefix is the Obsidian callout marker this transform recognises.
//
// **Duplicated from `internal/content`'s scanner, and the reason is an import
// cycle.** `internal/content` imports *this* package; sharing a constant would mean
// the other way round, and a cycle between the parser and the thing that parses
// is not a thing to build a shared constant to avoid. What holds the two together
// is `TestTheTwoSpellingsAgree`, which renders a page through this transform and
// scans the same source with the other, and fails if they ever disagree.
const calloutPrefix = "[!secret]"

// isBlockID reports whether a candidate is an Obsidian block id: letters, digits,
// `-` and `_`, and nothing else.
func isBlockID(candidate string) bool {
	if candidate == "" {
		return false
	}

	for _, r := range candidate {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}

	return true
}
