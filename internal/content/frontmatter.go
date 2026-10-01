// Front matter: the YAML block at the top of a markdown file, and the `kind` it
// discriminates.
//
// Everything here treats the file as hostile input, because S-3.1 makes the
// filesystem the source of truth and Obsidian Sync is what puts files in it — a
// shared vault, a community plugin, a compromised device. A page can therefore
// arrive from outside the instance, and this file is the boundary where that
// arrival is bounded: a document larger than MaxDocumentBytes is refused before
// a parser sees it, and the YAML parser's own alias-expansion and depth limits
// are asserted by TestParseRefusesAnAliasBomb rather than trusted.
//
// Nothing here breaks the loader. S-3.3 says a malformed front-matter block is
// inert, and that is a structural property of the API rather than a promise in a
// comment: Parse hands back a complete Document whatever the input was, and the
// one fact a caller might need to report — that the block did not interpret —
// travels beside it in FrontMatter.Err instead of in a return value a caller has
// to remember to handle.

package content

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/semiplane/semiplane/internal/domain"
)

// The size policy, in one place, because it is one decision applied twice: once
// before the bytes are read and once before they are parsed.
const (
	// MaxDocumentBytes is the largest markdown document semiplane will read or
	// interpret, at 4 MiB.
	//
	// It is a cap and not a validation, and it exists because the alternative is
	// unbounded: a wiki page is fetched, indexed, rendered and diffed, and a file
	// that a sync client can grow without limit is a lever on all four at once.
	// 4 MiB is far past any page a person writes and past what a browser-based
	// editor posts in one request, so nothing in normal operation comes close —
	// which is the property that makes it safe to refuse rather than to truncate.
	//
	// The cap bounds the *work*; it does not bound a small document's expansion.
	// A few hundred bytes of YAML aliases can expand into gigabytes of decoded
	// values, and that is what the parser's own limits are for.
	MaxDocumentBytes = 4 << 20

	// frontMatterMaxBytes bounds the YAML block alone, at 256 KiB, so the claim
	// "the parser is never handed more than this" is a statement about the input
	// rather than an inference from MaxDocumentBytes minus a body. Nothing
	// Obsidian or a semiplane kind writes comes near it, and the point is to
	// bound the parser, not to be right about where the limit is.
	frontMatterMaxBytes = 256 << 10
)

const (
	// frontMatterDelimiter is the marker on its own line that opens and closes
	// the block. Only this form, because it is the one Obsidian, Jekyll and Hugo
	// all agree on; YAML's own `...` document-end marker is not recognised,
	// since accepting two spellings is how two parsers of the same file start
	// disagreeing about where the prose begins.
	frontMatterDelimiter = "---"

	// frontMatterKindKey and frontMatterTitleKey are the two keys semiplane
	// promotes to typed fields. Everything else stays in FrontMatter.Fields,
	// which is what makes an unknown key a non-event (S-3.3).
	frontMatterKindKey  = "kind"
	frontMatterTitleKey = "title"

	// utf8BOM is stripped before the opening delimiter is looked for. An editor
	// that writes one is an editor whose author's front matter silently stopped
	// working, and it costs three bytes to not be that editor.
	utf8BOM = "\xef\xbb\xbf"

	// notAStringKind is what FrontMatter.DeclaredKind holds when `kind` was
	// present but was not a string. See declaredKind for why the value itself is
	// not reproduced.
	//
	// The surrounding brackets and the capitalisation are the point: a kind name
	// in this project is lowercase, hyphen-separated, and would never start with
	// a bracket, so a validation message carrying this cannot be read as a kind a
	// registry recognised — which is the misreading that would make a GM believe
	// the page is a game object of an unknown type rather than prose.
	notAStringKind = "[not a string]"
)

// The answers a caller can act on. Both are per-document facts rather than
// per-request ones, and neither means "this file is not allowed" — a page with a
// broken block is a page that renders as prose.
var (
	// ErrDocumentTooLarge means the document is over MaxDocumentBytes, or its
	// front-matter block is over frontMatterMaxBytes. Distinct from
	// ErrMalformedFrontMatter because the two are fixed by different things: a
	// malformed block is a typo to be fixed in Obsidian, and an oversized file is
	// a page that has to be split or a sync client that has to be stopped.
	ErrDocumentTooLarge = errors.New("content: document exceeds the size limit")

	// ErrMalformedFrontMatter means a block was present and did not interpret:
	// not YAML, not a mapping, a duplicate key, or refused by the parser's own
	// expansion limits (S-4.7).
	ErrMalformedFrontMatter = errors.New("content: front matter is not valid YAML")
)

// Document is one markdown file, split into its front matter and its prose.
//
// The two are separated, never reordered: Body is the file exactly as it was
// written after the closing delimiter, so a renderer sees the author's bytes and
// not a normalised copy of them. FrontMatter.Kind is always domain.KindProse
// unless the block declared a kind the registry recognised — including when there
// was no block, because "this page is prose" is the answer in every one of those
// cases and a caller should never have to tell an absent block from a refused one
// to know it (S-3.3). Raw is the block verbatim whether or not it interpreted: the
// file an author has to fix is not in the database and not in the parsed form,
// and this is the only place a caller gets to see it.
type Document struct {
	// FrontMatter is the interpreted block. Every field is zero except Kind
	// unless the block itself interpreted; see domain.FrontMatter.
	FrontMatter domain.FrontMatter
	// Body is everything after the closing delimiter, or the whole file when
	// there was no block. Empty exactly when the file was nothing but a block.
	Body string
	// Raw is the block between the delimiters, verbatim. Empty when the file has
	// no block, which Present distinguishes from an empty one.
	Raw string
	// Present reports that a delimited block was found. It is false for a file
	// with no block and for a file whose opening `---` was never closed — the
	// second case because an unterminated block is not a block. Treating it as
	// prose is the only safe reading: a horizontal rule at the top of a file is
	// ordinary markdown, and guessing that it started a block would eat the
	// author's prose on the way to rendering it.
	Present bool
}

// ReadDocument reads a document, refusing one that is over MaxDocumentBytes.
//
// The refusal here is the primary one, and it is deliberately at the read rather
// than at the parse: a limit applied after the bytes are in memory bounds the
// parser but not the buffer, and a file is read from a host directory that
// anything on the host can write to.
func ReadDocument(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("content: nil document reader")
	}

	// One byte past the cap, so a document that is exactly the cap is accepted
	// and one that is a byte more is refused, rather than both being decided by
	// the same comparison against an off-by-one.
	data, err := io.ReadAll(io.LimitReader(reader, MaxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("content: read document: %w", err)
	}

	if len(data) > MaxDocumentBytes {
		return nil, fmt.Errorf(
			"%w: over the %d-byte limit", ErrDocumentTooLarge, MaxDocumentBytes,
		)
	}

	return data, nil
}

// Parse splits src into front matter and prose, and interprets the block against
// registry.
//
// registry answers only one question — is this kind registered — and may be nil,
// in which case every page is prose. It is a parameter because the set of kinds
// belongs to the plugin registry that phase 8 builds; see domain.PageKind for why
// that set cannot be a list written into this repository.
//
// Parse is total. It has no error return, because the two ways it could fail are
// both answers rather than faults: an oversized document and an unreadable block
// each produce a Document whose FrontMatter.Err says which, and both still carry
// a Body. A caller that ignores Err renders a page. A caller that reads it can
// tell a GM that their page is prose because the front matter did not parse, or
// because the kind they wrote is not registered — which is the difference between
// a typo and a plugin they have not installed.
func Parse(src []byte, registry domain.PageKindRegistry) Document {
	src = bytes.TrimPrefix(src, []byte(utf8BOM))

	// Checked before the block is even located. An oversized document gets no
	// front matter at all rather than a front matter that may or may not be
	// complete, because a partly-interpreted block is the one outcome that must
	// not reach a renderer.
	if len(src) > MaxDocumentBytes {
		return Document{
			FrontMatter: inertFrontMatter(fmt.Errorf(
				"%w: %d bytes, maximum is %d",
				ErrDocumentTooLarge, len(src), MaxDocumentBytes,
			)),
			Body: string(src),
		}
	}

	block, body, present := splitFrontMatter(src)

	document := Document{Body: string(body), Present: present}
	if !present {
		document.FrontMatter = inertFrontMatter(nil)

		return document
	}

	document.Raw = string(block)

	if len(block) > frontMatterMaxBytes {
		document.FrontMatter = inertFrontMatter(fmt.Errorf(
			"%w: front matter is %d bytes, maximum is %d",
			ErrDocumentTooLarge, len(block), frontMatterMaxBytes,
		))

		return document
	}

	document.FrontMatter = interpretFrontMatter(block, registry)

	return document
}

// inertFrontMatter is the front matter of a page whose block did not interpret.
//
// Kind is prose in every case, including the case where the page has no block at
// all, because that is the answer and a caller that has to distinguish "no block"
// from "refused block" to know it is being handed an unset kind where the
// specification says it gets a page. DeclaredKind stays empty: nothing was
// declared, or nothing was read, and the two must not be confused either.
func inertFrontMatter(reason error) domain.FrontMatter {
	return domain.FrontMatter{Kind: domain.KindProse, Err: reason}
}

// interpretFrontMatter parses a block into a domain.FrontMatter.
//
// The target is `map[string]any` and nothing narrower, for three reasons that
// each rule out a typed struct here. An unknown key is tolerated rather than an
// error, because Obsidian plugins add keys and semiplane must not break on them
// (S-3.3). A key whose value is the wrong shape — `kind: [a, b]` where a string
// was expected — must not fail the block, because one page's `cover` being a
// list is not a reason its `kind` stops working. And a partly-populated struct
// cannot be distinguished from a whole one by the caller, which is precisely the
// thing S-3.3 rules out: a malformed block is either interpreted or inert, never
// half of each.
//
// Any error at all means the whole block is discarded. The parser returns a
// partially filled map alongside its error — a billion-laughs document yields
// three keys before it gives up — and reading what it managed to decode would
// mean rendering a page from a block that was refused for expanding into
// gigabytes. Inert is the requirement; partial is not a third option.
func interpretFrontMatter(block []byte, registry domain.PageKindRegistry) domain.FrontMatter {
	var fields map[string]any

	if err := yaml.Unmarshal(block, &fields); err != nil {
		// The parser's own message is dropped rather than wrapped, and that is
		// the convention `auth.parsePasswordHash` sets: a parser's text can quote
		// the input — its duplicate-key error formats the offending key — and an
		// error whose text varies with an attacker's document is a channel into
		// the log. The block itself is on Document.Raw for anyone who needs to
		// see what was written, and a line number is not worth a channel.
		return inertFrontMatter(ErrMalformedFrontMatter)
	}

	// An empty block, or one that is only comments, decodes to a nil map. Kept
	// distinct from "no block": the document had front matter and it was empty,
	// which is a fact a validator can report and a nil Fields cannot. The Kind is
	// spelled out rather than left zero because a caller must never have to tell
	// these two apart to know it is being handed prose.
	if fields == nil {
		return domain.FrontMatter{Kind: domain.KindProse, Fields: map[string]any{}}
	}

	// A key whose value is not a string is treated as absent rather than coerced:
	// coercing here would mean inventing a display name or a kind the author did
	// not write, and `kind: [token, scene]` must degrade to prose without costing
	// the page its title.
	return domain.FrontMatter{
		Kind:         domain.ResolvePageKind(stringField(fields, frontMatterKindKey), registry),
		DeclaredKind: declaredKind(fields),
		Title:        stringField(fields, frontMatterTitleKey),
		Fields:       fields,
	}
}

// stringField reads a key's value when it is a string, and the empty string when
// it is anything else.
//
// Absent rather than an error, and that is the whole behaviour: a `kind` written as
// a list and a `title` written as a number are two malformed fields in an
// otherwise readable block, and S-3.3 says a malformed front-matter block must not
// cost an author their page. Refusing the block would also make one bad key hide
// every good one.
func stringField(fields map[string]any, key string) string {
	value, isString := fields[key].(string)
	if !isString {
		return ""
	}

	return value
}

// declaredKind is what the author wrote under `kind`, as text, for reporting.
//
// Separate from stringField because `DeclaredKind` answers a different question.
// stringField answers "what kind is this page", where a non-string is correctly
// absent and the page degrades to prose. DeclaredKind answers "what would a GM
// need to be told to fix this" — and `kind: [token, scene]` is exactly the
// mistake a GM needs told about, because a page that renders as prose with *no*
// declared kind is indistinguishable in every report from a page that never
// declared one.
//
// A string passes through verbatim, which is the case that carries the message
// ("kind `tokne` is not registered"). Anything else becomes a fixed marker
// rather than the value: a non-string kind is a different document rather than
// a mistyped one, and re-encoding attacker-controlled structure into a field
// that ends up in a validation message is the thing to avoid here. The marker is
// deliberately not a plausible kind name, so it can never be mistaken for one
// that a registry recognised.
func declaredKind(fields map[string]any) string {
	value, present := fields[frontMatterKindKey]
	if !present || value == nil {
		return ""
	}

	if text, isString := value.(string); isString {
		return text
	}

	return notAStringKind
}

// splitFrontMatter separates the block from the body.
//
// Returns the block and the body, and whether a delimited block was there at
// all. It is byte-oriented rather than line-oriented because the body must come
// back byte-for-byte: a `strings.Split` and a `strings.Join` would rewrite the
// line endings of a file whose author wrote CRLF.
func splitFrontMatter(src []byte) (block, body []byte, present bool) {
	rest, opened := afterOpeningDelimiter(src)
	if !opened {
		return nil, src, false
	}

	start := 0

	for start < len(rest) {
		line := rest[start:]
		next := len(rest)

		if newline := bytes.IndexByte(rest[start:], '\n'); newline >= 0 {
			line = rest[start : start+newline]
			next = start + newline + 1
		}

		if isDelimiter(line) {
			return rest[:start], rest[next:], true
		}

		start = next
	}

	// Opened and never closed: not a block. See Document.Present.
	return nil, src, false
}

// afterOpeningDelimiter returns the bytes after a leading delimiter line, and
// whether the document opened with one.
//
// The delimiter must be the first line and nothing may precede it. Leading
// whitespace, a stray blank line, or prose above the block all mean the file does
// not begin with front matter, and searching for one further down would find a
// thematic break and eat the prose above it.
func afterOpeningDelimiter(src []byte) (rest []byte, opened bool) {
	line := src
	consumed := len(src)

	if newline := bytes.IndexByte(src, '\n'); newline >= 0 {
		line = src[:newline]
		consumed = newline + 1
	}

	if !isDelimiter(line) {
		return nil, false
	}

	return src[consumed:], true
}

// isDelimiter reports whether a line is a `---` marker.
//
// Trailing whitespace and a carriage return are trimmed, so a file saved with
// CRLF line endings is not silently a file without front matter. Nothing else is
// trimmed and nothing else is accepted: `--- # a comment` is not the marker, and
// treating it as one would make a file's validity depend on a parser feature
// nobody's front matter uses.
func isDelimiter(line []byte) bool {
	return string(bytes.TrimRight(line, " \t\r")) == frontMatterDelimiter
}
