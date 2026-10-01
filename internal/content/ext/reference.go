package ext

import (
	"path"

	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// The punctuation of the `[[…]]` grammar, as bytes.
//
// Named because they appear in comparisons against a reader's line, and a bare
// `'['` in a condition is a byte literal that says nothing about which side of
// `[[` is being tested.
const (
	openBracket  = '['
	closeBracket = ']'
	pipe         = '|'
	hash         = '#'
	bang         = '!'
)

// newRefParser returns the inline parser for one of the two `[[…]]` triggers.
//
// The two extensions differ only in the punctuation that introduces them and in
// the element they render, so they share the parser and differ in one bool. Two
// parsers over the same scanner would be duplication waiting to happen and, worse,
// two grammars that could drift: a form accepted by one and not the other is a
// defect nobody reproduces, because the report names the extension and not the
// scanner.
//
// The kind is read off the definition rather than inferred from the trigger byte,
// so a definition that arrives with the wrong trigger is a wrong render rather
// than a silently different grammar.
func newRefParser(trigger byte, defs []Definition) parser.InlineParser {
	built := &refParser{}

	if trigger == bang {
		built.leading = bang
	}

	for _, def := range defs {
		if def.Trigger == trigger {
			built.kind = def.Kind
		}
	}

	return built
}

// refParser recognises one of the two `[[…]]` forms.
type refParser struct {
	// kind is the extension this parser produces nodes for.
	kind Kind

	// leading is the byte that must precede the opening `[[`, or zero when there
	// is none: `'!'` for an embed, zero for a wikilink.
	leading byte
}

// Trigger implements parser.InlineParser.Trigger.
func (p *refParser) Trigger() []byte {
	return []byte{p.triggerByte()}
}

// Parse implements parser.InlineParser.Parse.
//
// Returning nil is the whole of "this is not my syntax": goldmark then treats the
// trigger byte as ordinary text and moves on, so a `!` in prose, a `[` that opens
// a markdown link, and a `[[` that is never closed all pass through untouched.
//
// The reader is advanced by the full length of the construct, delimiters
// included, so what follows parses as if the construct had never been there. That
// is what keeps `[[A]]-[[B]]` two links rather than one link and a stray `]]`.
func (p *refParser) Parse(_ gast.Node, block text.Reader, _ parser.Context) gast.Node {
	line, segment := block.PeekLine()

	// How many bytes of leading punctuation to step over before scanning for the
	// `[[`. Zero for a wikilink, one for an embed.
	skip := 0

	if p.leading != 0 {
		if len(line) < 2 || line[0] != p.leading {
			return nil
		}

		skip = 1
	}

	inner, consumed, ok := scanReference(line[skip:])
	if !ok {
		return nil
	}

	block.Advance(skip + consumed)

	return &Node{kind: p.kind, inner: inner, offset: segment.Start + skip}
}

// triggerByte is the byte this parser dispatches on: the leading punctuation when
// it has one, and the opening bracket otherwise.
func (p *refParser) triggerByte() byte {
	if p.leading != 0 {
		return p.leading
	}

	return openBracket
}

// scanReference reads a `[[…]]` construct at the start of line and returns the
// text between the brackets, verbatim, with the bytes consumed.
//
// The grammar it recognises is Obsidian's:
//
//	[[Target]]
//	[[Target|Alias]]
//	[[Target#Heading]]
//	[[Target#Heading|Alias]]
//	[[Target#^block-id]]
//
// It does **not** split the inner text into a target, an anchor and an alias.
// That is `content.ParseReference`'s job, in `internal/content/links.go`, and
// doing it here as well would be the second grammar for one syntax — the thing
// this package exists to avoid. What is returned is the author's bytes, and the
// caller's parser is the authority on what they mean.
//
// So the only validation here is lexical: the brackets balance, the construct is
// on one line, and there is something between the brackets. Whether the text
// inside names a page is not this function's question, and `[[#Heading]]` — which
// names an anchor in the current page and no page at all — is a well-formed
// reference that a caller may legitimately hand on.
//
// The scan never crosses a newline and refuses a nested `[` or `]`. A reference
// is a flat run of text, and a grammar that balanced brackets would be a grammar
// whose failure mode is a scan that does not terminate on a hostile document.
//
// It returns no error because there is nothing a caller could do with one. An
// unparseable reference is prose, and prose is what the author gets.
func scanReference(line []byte) (inner string, consumed int, ok bool) {
	if len(line) < 2 || line[0] != openBracket || line[1] != openBracket {
		return "", 0, false
	}

	body, end, ok := closingBrackets(line, 2)
	if !ok {
		return "", 0, false
	}

	if len(body) == 0 {
		// `[[]]` names nothing, and rendering it as a link would put an anchor
		// with no target in the page. The author's brackets are text instead.
		return "", 0, false
	}

	return string(body), end, true
}

// closingBrackets returns the text between a `[[` opening at `from` and the
// `]]` that closes it, plus the index just past that `]]`.
func closingBrackets(line []byte, from int) (body []byte, end int, ok bool) {
	for cursor := from; cursor+1 < len(line); cursor++ {
		switch line[cursor] {
		case openBracket, closeBracket:
			// A bracket that is not the closing pair ends the scan without
			// producing one, so `[[a[b]]` is not a reference to `a[b`.
			if line[cursor] == closeBracket && line[cursor+1] == closeBracket {
				return line[from:cursor], cursor + 2, true
			}

			return nil, 0, false

		case '\n', '\r':
			// A reference does not span lines. Refusing rather than continuing is
			// what stops a page with one stray `[[` from swallowing the rest of
			// itself into a single link.
			return nil, 0, false

		default:
		}
	}

	return nil, 0, false
}

// label is the text a link to a reference displays: the alias when there is one,
// and otherwise the target's own base name.
//
// It follows `content.Reference.Label`'s rule and deliberately does not share its
// implementation, because this package cannot import `internal/content` — that
// package imports this one. Two implementations of one display rule is a real
// duplication, and it is held in place by `TestLabelAgreesWithTheLinkLayer` in
// `internal/content/render_test.go`, which runs both over one table of
// references. That test is the invariant; this function is the cheaper half of
// it, because the label has to be known at render time and the link layer is not
// consulted at render time at all.
func label(inner string) string {
	head, alias, hasAlias := cut([]byte(inner), pipe)

	// A blank line after the split, because the split assigns three values and the
	// condition below reads only one of them. Cuddling them reads as though the
	// condition were about the whole call.
	if hasAlias && trimSpace(alias) != "" {
		return trimSpace(alias)
	}

	target, anchor, hasAnchor := cut(head, hash)
	trimmed := trimSpace(target)

	if trimmed == "" {
		// `[[#Heading]]` names an anchor in the current page and no path at all,
		// so there is no base name to take. The anchor is the only text the author
		// wrote, and it is what the link says — `path.Base("")` would be a period.
		if hasAnchor {
			return trimSpace(anchor)
		}

		return ""
	}

	return path.Base(trimmed)
}

// cut splits at the first occurrence of sep and reports whether it was there.
//
// The separator is removed from both halves: a target never contains a `|` and an
// anchor never contains one either, so a half that still holds one was not split
// at all.
func cut(value []byte, sep byte) (before, after []byte, found bool) {
	for at := range len(value) {
		if value[at] == sep {
			return value[:at], value[at+1:], true
		}
	}

	return value, nil, false
}

// trimSpace removes the ASCII whitespace Obsidian trims around a reference's
// parts.
//
// ASCII only, and deliberately not `strings.TrimSpace`: that trims Unicode
// spaces, which would make whether a reference resolves depend on which Unicode
// space an author happened to paste.
func trimSpace(value []byte) string {
	start := 0
	for start < len(value) && isASCIISpace(value[start]) {
		start++
	}

	stop := len(value)
	for stop > start && isASCIISpace(value[stop-1]) {
		stop--
	}

	return string(value[start:stop])
}

// isASCIISpace reports whether b is a space or a tab.
func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t'
}

// ensure the parser satisfies the interface it is installed as, at compile time
// rather than in a test that only runs when somebody builds one.
var _ parser.InlineParser = (*refParser)(nil)
