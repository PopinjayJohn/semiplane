package ext

import (
	"fmt"
	"strconv"

	"github.com/yuin/goldmark/util"
)

// The `data-` attribute names this package emits.
//
// Three names, and the set is closed. bluemonday can be told to allow arbitrary
// `data-*` attributes in one call, and that call is *not* made here: a `data-`
// attribute is inert until some code reads it, and the failure mode of a
// permissive set is that the *next* phase adds a client that reads
// `data-something`, at which point a GM-authored page — or a page that arrived
// through Obsidian Sync — can drive that client. Naming the three attributes the
// renderer actually writes means adding a fourth is a change to this file, in
// review, rather than a consequence of a library default.
const (
	// dataExt names the extension, duplicating the class so a live layer can
	// find a node without depending on a stylesheet's hook name.
	dataExt = "data-ext"

	// dataRefIndex is the node's document-order ordinal, which is its index in
	// the reference list `Renderer.Render` returns. This is the whole of the seam
	// with link resolution: C4 matches a rendered element to a reference by this
	// number, so nothing downstream re-parses the HTML.
	dataRefIndex = "data-ref-index"

	// dataArg carries a `{{name:arg}}` argument verbatim. It is a number-free
	// string on the page — for `{{dice:1d20+5}}` it is the expression, not a
	// result.
	dataArg = "data-arg"
)

// Slot returns a Write function that renders an empty element carrying the
// definition's own class and `data-` attributes, and nothing else.
//
// The helper a phase-10 or phase-8 extension should reach for rather than writing
// its own, and it exists because the two properties it guarantees are the two that
// are easy to get wrong when writing one by hand:
//
//   - The element is **empty**. An empty slot is permission-neutral, which is what
//     S-5.1 requires and what ADR 0017's byte-identity test asserts; a slot with
//     anything in it that was derived from another page is a variant per viewer,
//     which is the per-user cache the specification rules out.
//   - Every author byte that *is* written is escaped, because an attribute value is
//     a place an unescaped quote ends.
//
// The inner text is **not** written. A `[[…]]` extension's inner text is a
// reference, and the reference belongs in the `References` slice C4 receives — not
// in an attribute a browser can read and a clobbering script could look for. A
// `{{…}}` extension's inner text is an operand the live layer needs, which is what
// `OperandSlot` is for.
//
// `element` is a parameter rather than a constant because a slot is either a
// `<span>` (inline, and the usual case) or a `<div>` (when a plugin wants a block
// slot, and phase 5 will want one for a stat block). It is not validated here: a
// caller passing `"script"` gets a `<script>`, and the *sanitiser policy* in
// `internal/content` is what stops it — an allowlist, so an element nobody allowed
// is removed regardless of what wrote it. That is the right place for the check,
// because it is the only place that sees the final bytes.
func Slot(element string) func(w util.BufWriter, node *Node) error {
	return func(writer util.BufWriter, node *Node) error {
		if err := open(writer, element, node); err != nil {
			return err
		}

		return write(writer, "></"+element+">")
	}
}

// write is the only place this package writes to a goldmark BufWriter.
//
// One function rather than six call sites because the error it wraps is the same
// every time and the wrapping is the same every time: goldmark's BufWriter is an
// interface whose WriteString belongs to no package here, so an unwrapped
// return is a bare implementation detail in an error a reader will one day be
// asked to interpret. The context names the extension, which is the one fact
// that makes such an error actionable.
func write(writer util.BufWriter, markup string) error {
	if _, err := writer.WriteString(markup); err != nil {
		return fmt.Errorf("write %d byte(s) of extension markup: %w", len(markup), err)
	}

	return nil
}

// OperandSlot is Slot for a `{{name:arg}}` extension, which additionally writes
// the operand into `data-arg`.
//
// The distinction is not cosmetic. A `{{…}}` argument is a *free string* rather
// than a page reference — a dice expression, a game object's name — and the live
// layer has to read it from the DOM to act on it, because a page render is what
// put it there. A `[[…]]` reference, by contrast, arrives to C4 in the
// `References` slice, already split, and putting it in the DOM as well would be a
// second copy of author-controlled text for a script to find.
//
// The operand is escaped, so a `{{dice:1d20" onload="…}}` reaches the output as
// characters and not as an attribute.
func OperandSlot(element string) func(w util.BufWriter, node *Node) error {
	return func(writer util.BufWriter, node *Node) error {
		if err := open(writer, element, node); err != nil {
			return err
		}

		if err := writeArg(writer, node.inner); err != nil {
			return err
		}

		return write(writer, "></"+element+">")
	}
}

// open writes `<element` and the attributes every semiplane extension element
// carries: the class the stylesheet targets, the extension's identity, and its
// reference index.
//
// The class and `data-ext` carry the same value on purpose. The class is a
// styling hook that phase 5 writes rules against and may reasonably rename;
// `data-ext` is the identity a live layer keys on so it does not have to depend
// on a name the stylesheet owns. Two spellings of one fact is the price of not
// making behaviour depend on a styling decision, and they are written from one
// field so they cannot drift apart.
func open(writer util.BufWriter, element string, node *Node) error {
	kind := string(node.kind)

	return write(writer,
		"<"+element+
			` class="`+kind+`"`+
			` `+dataExt+`="`+kind+`"`+
			` `+dataRefIndex+`="`+strconv.Itoa(node.index)+`"`,
	)
}

// writeArg writes `data-arg`, or nothing when the extension was written without
// an argument.
//
// A `{{dice}}` with no expression and a `{{dice:1d20+5}}` with one are different
// documents and must not render identically: the first asks the live layer to
// roll something unspecified, the second asks for a particular roll, and an
// element that cannot tell them apart cannot do either.
func writeArg(writer util.BufWriter, arg string) error {
	if arg == "" {
		return nil
	}

	return write(writer, ` `+dataArg+`="`+escape(arg)+`"`)
}

// escape HTML-escapes a value interpolated into an attribute or into element
// text.
//
// The one function every writer routes its author-supplied bytes through, and it
// is here rather than left to the caller because the whole security argument for
// this package is that a fixed template plus escaped interpolation cannot become
// markup (S-4.6). `util.EscapeHTML` is goldmark's own escaper — the same one its
// code-span and code-block renderers use — so an escaped value here is escaped
// exactly as carefully as one inside a fenced code block.
func escape(value string) string {
	return string(util.EscapeHTML([]byte(value)))
}
