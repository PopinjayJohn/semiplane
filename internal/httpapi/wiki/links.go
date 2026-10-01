package wiki

import (
	"html"
	"strconv"
	"strings"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/content/ext"
)

// Address attachment: writing each reference's resolved address into the element
// the renderer left empty.
//
// This step belongs to the caller rather than to the renderer, and the reason is
// S-5.1: rendered output is permission-neutral by construction, and an `href` is
// not neutral, because whether one can be produced depends on the campaign's
// index and therefore on when the page was read. So `content.Renderer` emits a
// bare `<a class="wikilink" data-ext="wikilink" data-ref-index="3">Goblin</a>`
// with no destination at all — see `content.Reference` and ADR 0017 — and the
// route fills the address in afterwards, keyed on the ordinal, which is the same
// number on both sides by construction rather than by a search.
//
// The substitution runs *after* sanitisation, which is the one thing about it that
// needs arguing rather than stating: it means this file writes markup no
// sanitiser has seen. Every form below is therefore chosen to be one the policy
// in `content/policy.go` already allows, so that a page which had been
// sanitised *after* the substitution would be identical — an attribute the policy
// did not allow, or an element it did not allow, would be a second difference
// between the HTML a reader receives and the HTML the two layers between here
// and the browser would have produced on their own.
//
//   - `href` on `a`, and `src`/`alt` on `img`: both are on the policy.
//   - `title` on a `span`: a standard attribute, allowed globally.
//   - `class`, `data-ext` and `data-ref-index`: written by the renderer, on `a`
//     and `span`, and carried through unchanged.
//
// `TestAttachmentWritesOnlySanitiserAllowedMarkup` is the test that holds this
// file to that claim, and it is a table rather than a review because a reviewer
// cannot check a policy against a diff of a string builder.

// The pieces of an extension's element this file matches on. Spelled out rather
// than composed from the renderer's own constants, which are unexported, and
// spelled as literals rather than as a regexp because the whole job is three
// substring searches over a fragment.
const (
	// refIndexAttr names the attribute, and ordinalAttribute builds the exact
	// `data-ref-index="7"` a given ordinal appears as. The closing quote is part
	// of it deliberately: searching for the bare prefix would match
	// `data-ref-index="17"` as well, and the wrong element is a wrong href.
	refIndexAttr = `data-ref-index="`

	// extensionAttr and its value pattern identify an element as one of ours.
	//
	// Checked rather than trusted, because the other way round is not safe: an
	// author can write the text `data-ref-index="0"` in a paragraph, and a search
	// that stopped at the nearest preceding `<` would then attribute the address
	// to whatever element that paragraph happens to sit in. The prose cannot
	// produce a `<a>` — goldmark escapes it and the policy would strip it — so
	// requiring a `data-ext` that matches the reference's own extension is what
	// separates a real element from a mention of one.
	extensionAttr = `data-ext="`
)

// attachAddresses writes each link's address into the element carrying its
// ordinal, and returns the page.
//
// Every byte of the input is preserved: the walk copies the spans between
// elements through unchanged, so a page whose references cannot be matched comes
// back byte-identical rather than shortened. That is the direction to fail in —
// a link with no address is a dead link an author can see, and a page with a
// missing paragraph is a page nobody can see the damage to.
func attachAddresses(page string, links []content.Link) string {
	rewriter := markup{page: page}
	// Room for the growth the addresses cause, so the common case of a page with
	// a few references does not reallocate per element.
	rewriter.out.Grow(len(page) + addressRoomPerLink*len(links))

	for _, link := range links {
		rewriter.write(link)
	}

	rewriter.out.WriteString(page[rewriter.cursor:])

	return rewriter.out.String()
}

// addressRoomPerLink is how many bytes each address is assumed to need when the
// output buffer is sized. A hint and not a limit: an href can be longer than this
// and the buffer grows, and one shorter than this wastes a few bytes per link.
const addressRoomPerLink = 64

// markup is the one-pass rewrite, carrying the input, the output and how far
// through the input the walk has got.
//
// A struct rather than three values threaded through three functions, because the
// recursion here would otherwise be six parameters wide and the interesting part
// — where the walk resumes — would be one of them.
type markup struct {
	page   string
	out    strings.Builder
	cursor int
}

// write rewrites the element this link addresses, if it can be found.
//
// Leaves the cursor alone when it cannot, so the next link's search starts from
// where this one did rather than from a position derived from a false match.
func (m *markup) write(link content.Link) {
	start, end, name, found := m.elementFor(link)
	if !found {
		return
	}

	m.out.WriteString(m.page[m.cursor:start])
	m.out.WriteString(rewritten(m.page[start:end], name, link))
	m.cursor = end
}

// elementFor returns the half-open byte range of the element carrying this
// link's ordinal, and the element's tag name.
//
// Searches forward from the cursor, because the ordinals are in document order
// and the ordinal of the next link is after the one just written: searching the
// whole page would let a match belonging to an element already rewritten be found
// again. A rejected candidate advances the search rather than ending it, so prose
// that merely mentions an ordinal does not cost the reference its address.
func (m *markup) elementFor(link content.Link) (start, end int, name string, found bool) {
	ordinal := ordinalAttribute(link.Index)

	from := m.cursor

	for from <= len(m.page) {
		offset := strings.Index(m.page[from:], ordinal)
		if offset < 0 {
			return 0, 0, "", false
		}

		candidate := from + offset

		first, last, tag, ok := elementAt(m.page, candidate, link.Extension)
		if ok {
			return first, last, tag, true
		}

		from = candidate + len(ordinal)
	}

	return 0, 0, "", false
}

// elementAt returns the range of the element whose opening tag contains the
// ordinal attribute at offset, and that tag's element name.
//
// Two invariants make a substring search sound here, and both come from the
// output being *sanitised*: every `<` in the page begins a tag and every `>`
// ends one, because a sanitiser escapes both in text and in attribute values. So
// the nearest `<` before the attribute is the element's own opening bracket and
// the next `>` after it is that tag's end. Without the escaping this would be a
// guess, and a guess in a string rewriter is how a page gets an `href` inside a
// paragraph.
func elementAt(page string, offset int, extension ext.Kind) (start, end int, name string, ok bool) {
	open := strings.LastIndexByte(page[:offset], '<')
	if open < 0 {
		return 0, 0, "", false
	}

	tagEnd := strings.IndexByte(page[offset:], '>')
	if tagEnd < 0 {
		return 0, 0, "", false
	}

	tagEnd += offset

	name = tagName(page[open:tagEnd])
	if name != "a" && name != "span" {
		return 0, 0, "", false
	}

	if !strings.Contains(page[open:tagEnd], extensionAttr+extension.String()+`"`) {
		return 0, 0, "", false
	}

	// The closing tag of the same name. Sound because the extensions write flat
	// elements — an anchor whose text is the author's escaped bytes, an embed that
	// is empty — so nothing nests inside one and the first `</a>` after the tag is
	// this element's own.
	closing := "</" + name + ">"

	tail := strings.Index(page[tagEnd+1:], closing)
	if tail < 0 {
		return 0, 0, "", false
	}

	return open, tagEnd + 1 + tail + len(closing), name, true
}

// tagName returns the element name of an opening tag, or empty when the text is
// not one.
//
// `<a class="wikilink">` gives `a`; `<span` gives `span`; and `<p>` gives `p`,
// which is how a mention of an ordinal in a paragraph is rejected. A closing tag,
// a comment and a doctype all give empty: none of them can begin a reference
// element, and treating them as something they are not is the failure this
// function exists to prevent.
func tagName(tag string) string {
	rest, ok := strings.CutPrefix(tag, "<")
	if !ok {
		return ""
	}

	for at := range len(rest) {
		switch char := rest[at]; {
		case char == '>' || char == '/' || char == ' ' || char == '\t' || char == '\n':
			return rest[:at]
		case char >= 'a' && char <= 'z':
			continue
		default:
			return ""
		}
	}

	return ""
}

// ordinalAttribute is the exact `data-ref-index="7"` one ordinal appears as.
func ordinalAttribute(index int) string {
	return refIndexAttr + strconv.Itoa(index) + `"`
}

// rewritten returns one element with its address written in.
//
// Three forms, and the choice between them is the whole of what this file
// decides about markup:
//
//   - An address and a page or asset behind it becomes an `href`, or an `img` for
//     an asset embed. The element keeps every attribute the renderer gave it,
//     because they are its identity and the policy allows all three.
//   - A refused reference becomes a `span` carrying the refusal's sentence as its
//     `title` — an `a` with no `href` is not a link to a screen reader, and a
//     silent strip is worse, because an author who wrote `[[../outside]]` and
//     sees nothing at all cannot tell a refusal from a broken link. `title` is a
//     standard attribute, so this is markup the policy would also have allowed.
//   - A reference that resolved to nothing still gets its `href`, because
//     `content.Resolver` gives an unresolved reference the address it *would*
//     have had — a page the author names may be created by a sync an hour later,
//     so refusing the address would be refusing a page that is about to exist.
//     What it additionally carries is `data-broken="true"`, which the stylesheet
//     renders as a dead link. The combination is deliberate: the link works if the
//     page appears, and looks broken until it does, so a reader clicking it gets
//     either the page or a 404 about a page — never a silent no-op.
//
// `data-broken` is on the policy allowlist, constrained to the single value
// `true`, so this file is still writing only markup a sanitiser would also have
// allowed. That constraint is what keeps
// `TestAttachmentWritesOnlySanitiserAllowedMarkup` a real test rather than a
// description of a coincidence.
func rewritten(element, name string, link content.Link) string {
	tagEnd := strings.IndexByte(element, '>')
	attrs := element[1+len(name) : tagEnd]
	inner := element[tagEnd+1 : len(element)-len(name)-3]

	switch {
	case link.Refusal != content.Allowed:
		return `<span` + attrs + ` title="` +
			html.EscapeString(link.Refusal.Reason()) + `">` + inner + `</span>`
	case link.Kind == content.LinkToAsset && link.Extension == ext.KindEmbed:
		return `<img src="` + link.Href + `" alt="` + html.EscapeString(link.Label) + `">`
	case link.Broken:
		// Appended rather than spliced: `attrs` already carries the
		// renderer's own attributes and their spacing, and rebuilding it would be
		// a second place to forget one.
		return `<a` + attrs + ` href="` + link.Href + `" data-broken="true">` +
			inner + `</a>`
	default:
		return `<a` + attrs + ` href="` + link.Href + `">` + inner + `</a>`
	}
}
