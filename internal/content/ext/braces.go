package ext

import (
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// bracesParser recognises the `{{name}}` and `{{name:arg}}` forms.
//
// One parser for every `{{…}}` extension, dispatching on the keyword, because
// the keyword is what distinguishes them and a parser per keyword would give
// every plugin its own copy of a grammar that must stay identical across all of
// them. An extension is added by naming it, not by writing a scanner.
type bracesParser struct {
	// byName is the registered keyword of every definition sharing this trigger.
	// A keyword that is not in it is not an extension, and the construct is text.
	byName map[string]Definition
}

// newBracesParser returns the inline parser for a `{{…}}` trigger.
func newBracesParser(defs []Definition) parser.InlineParser {
	return &bracesParser{byName: byName(defs)}
}

// Trigger implements parser.InlineParser.Trigger.
func (p *bracesParser) Trigger() []byte {
	return []byte{openBrace}
}

// Parse implements parser.InlineParser.Parse.
//
// A `{{…}}` that names no installed extension returns nil, which is the whole
// degradation story for the two directives a gameplay plugin brings with it: the
// braces reach the page as the braces the author typed. The alternative —
// swallowing the span, or rendering an error element — destroys prose over a
// missing plugin, and a page is prose first.
func (p *bracesParser) Parse(_ gast.Node, block text.Reader, _ parser.Context) gast.Node {
	line, segment := block.PeekLine()

	name, arg, consumed, ok := scanBraces(line)
	if !ok {
		return nil
	}

	def, known := p.byName[name]
	if !known {
		return nil
	}

	block.Advance(consumed)

	return &Node{kind: def.Kind, inner: arg, offset: segment.Start}
}

// The punctuation and bytes of the `{{…}}` grammar.
const (
	openBrace  = '{'
	closeBrace = '}'
	quote      = '"'
	colon      = ':'
)

// scanBraces reads a `{{name}}` or `{{name:arg}}` construct at the start of line.
//
// Both argument spellings the documentation uses are accepted, because the
// documentation uses both:
//
//	{{dice}}              no argument
//	{{dice:1d20+5}}       colon-separated
//	{{dice "1d20+5"}}     quoted, as the content-model page writes it
//
// The quoted form is closed-quote-then-`}}` and nothing else: trailing content
// that is not whitespace means this is not the construct, and guessing would mean
// swallowing prose a page happened to write after a stray `{{`. The argument is
// returned exactly as written apart from surrounding whitespace and is never
// interpreted here — what `1d20+5` means belongs to the gameplay plugin's
// grammar, and the protocol never assumes d20.
func scanBraces(line []byte) (name, arg string, consumed int, ok bool) {
	if len(line) < 4 || line[0] != openBrace || line[1] != openBrace {
		return "", "", 0, false
	}

	// `cursor` rather than `at`: this is the scan position and it is live across
	// the whole function, which is exactly the scope varnamelen measures.
	cursor := 2
	start := cursor

	for cursor < len(line) && isNameByte(line[cursor], cursor == start) {
		cursor++
	}

	if cursor == start {
		// `{{ }}`, `{{|x}}`: no keyword, so no extension.
		return "", "", 0, false
	}

	name = string(line[start:cursor])

	arg, cursor, ok = scanArgument(line, cursor)

	if !ok {
		return "", "", 0, false
	}

	if cursor+1 >= len(line) || line[cursor] != closeBrace || line[cursor+1] != closeBrace {
		return "", "", 0, false
	}

	return name, arg, cursor + 2, true
}

// scanArgument reads the optional argument of a `{{name…}}` construct, returning
// the empty string and the position of the closing braces when there is none.
func scanArgument(line []byte, cursor int) (arg string, next int, ok bool) {
	if cursor < len(line) && line[cursor] == colon {
		return scanBareArgument(line, cursor+1)
	}

	return scanQuotedArgument(line, cursor)
}

// scanBareArgument reads everything up to the closing braces after a `:`.
func scanBareArgument(line []byte, cursor int) (arg string, next int, ok bool) {
	start := cursor

	for cursor < len(line) {
		if line[cursor] == closeBrace && cursor+1 < len(line) && line[cursor+1] == closeBrace {
			arg = trimSpace(line[start:cursor])
			if arg == "" {
				// `{{dice:}}` is a directive with no argument, which is a
				// mistake rather than a different directive. Rendering it as
				// `{{dice:}}` tells the author that; rendering it as a bare
				// `{{dice}}` would invent an argument-free directive they did not
				// write.
				return "", 0, false
			}

			return arg, cursor, true
		}

		if line[cursor] == openBrace {
			// A nested `{{` means the author is writing something else, or made
			// a typo. Either way the rest of the line is not this argument.
			return "", 0, false
		}

		cursor++
	}

	return "", 0, false
}

// scanQuotedArgument reads a space-separated double-quoted argument.
func scanQuotedArgument(line []byte, cursor int) (arg string, next int, ok bool) {
	for cursor < len(line) && isASCIISpace(line[cursor]) {
		cursor++
	}

	if cursor >= len(line) || line[cursor] != quote {
		// No argument at all: the construct ends here.
		return "", cursor, true
	}

	cursor++
	start := cursor

	for cursor < len(line) && line[cursor] != quote {
		if line[cursor] == '\n' || line[cursor] == '\r' {
			return "", 0, false
		}

		cursor++
	}

	if cursor >= len(line) {
		return "", 0, false
	}

	arg = trimSpace(line[start:cursor])
	cursor++

	// Only whitespace may sit between the closing quote and the braces.
	for cursor < len(line) && isASCIISpace(line[cursor]) {
		cursor++
	}

	return arg, cursor, true
}

// isNameByte reports whether char may appear in a keyword, and whether it is the
// keyword's first byte.
//
// The first byte must be a letter, so `{{1d20}}` is not a keyword and a directive
// cannot start with a digit. Letters and digits after that, plus `-` and `_`: the
// shapes a plugin name plausibly takes. Case is not folded, and `{{Dice}}` not
// matching `{{dice}}` is the same decision `domain.ResolvePageKind` makes for a
// page's `kind` — an identifier is matched exactly, because a name that resolves
// case-insensitively in one place and exactly in another is a name two things
// disagree about.
func isNameByte(char byte, first bool) bool {
	switch {
	case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z':
		return true
	case first:
		return false
	case char >= '0' && char <= '9', char == '-', char == '_':
		return true
	default:
		return false
	}
}

// ensure the parser satisfies the interface it is installed as, at compile time
// rather than in a test that only runs when somebody builds one.
var _ parser.InlineParser = (*bracesParser)(nil)
