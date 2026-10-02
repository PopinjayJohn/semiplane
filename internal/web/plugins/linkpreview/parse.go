package linkpreview

// Reading three fields out of an HTML document, and the rules that keep a hostile page
// from making the reader do something.
//
// # Parsed, never scanned
//
// `golang.org/x/net/html` and not a `regexp` over the bytes, for three reasons in order
// of weight:
//
//  1. **A regex cannot see structure.** `<title>a</title><meta name="description"
//     content="b">` and `<title>a<script>var x = '<meta name="description" content="b">'
//     </script></title>` differ by whether the meta is inside a script, and a scan
//     cannot tell. Every page is attacker-reachable through the link that named it, so
//     the parser is the boundary.
//  2. **Entities.** A title of `Fish &amp; Chips` must render as `Fish & Chips`, and
//     the tokenizer decodes that; a scan would emit the raw text and put an entity in
//     the card.
//  3. **It is already a dependency** and already the parser `internal/content` uses for
//     the same job, so there is no second HTML reader in the process.
//
// # The document is walked, and the walk stops at the fields
//
// The parser is fed `MaxBodyBytes`, so the walk is bounded by construction. It does not
// stop early once all three fields are found, because a `<meta>` in a `<noscript>` can
// still be the one a reader's browser would honour and stopping would make the answer
// depend on document order in a way no test could pin.
//
// # Every field is truncated on a rune boundary
//
// `truncate` cuts by runes rather than bytes for the reason the wire's `maxToken` and
// `search`'s echoed query both argue: a byte slice splits a multi-byte character and
// renders a replacement glyph in the middle of somebody's title.

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// The meta names this package reads, as a table rather than a `switch`.
//
// A table because **the names are attacker-supplied text in both directions**: a page
// chooses them, and a page that named `description` twice with different values must
// not have the answer depend on which one the reader happened to see. One table, one
// comparison per field, and `firstWins` is what makes the answer deterministic.
//
// `og:` is checked before the plain name because Open Graph is what a page states about
// itself in a form meant for another site, and a plain `description` meta is frequently
// written for a search engine rather than for a preview.
var metaNames = []struct {
	// attribute is the attribute holding the name — `name`, `property` or `itemprop`.
	attribute string

	// value is the name itself, lowercase.
	value string
}{
	{attribute: attrProperty, value: "og:title"},
	{attribute: attrName, value: "og:title"},
	{attribute: attrName, value: "twitter:title"},
	{attribute: attrProperty, value: "og:description"},
	{attribute: attrName, value: "og:description"},
	{attribute: attrName, value: "twitter:description"},
	{attribute: attrName, value: "description"},
	{attribute: attrProperty, value: "og:image"},
	{attribute: attrName, value: "og:image"},
	{attribute: attrName, value: "twitter:image"},
}

// The meta attribute names this package reads, as constants.
//
// A page's HTML is attacker input in both directions — it chooses the attribute and the
// value — so the spellings are written out once here rather than spelled at each of the
// eleven table rows below, where a typo would be a silently unread field.
const (
	attrName     = "name"
	attrProperty = "property"
	attrContent  = "content"
)

// collected is the three fields as they are found, with `fields` holding which names
// have already produced a value.
//
// `seen` rather than an empty-string check, because a page whose first
// `name="description"` is empty and whose second is not must take the second: an empty
// string is a *value*, and treating it as absence is how a page with
// `<meta name="description" content="">` at the top of its head loses its description to
// nothing at all.
type collected struct {
	// fields maps a meta name to its content, first occurrence winning.
	fields map[string]string

	// title is the document's `<title>` text, accumulated across its text nodes.
	title strings.Builder

	// inTitle reports whether the walk is inside `<title>`.
	inTitle bool

	// sawTitleText reports whether `<title>` held any text at all. An empty `<title>`
	// is a page that said nothing, and it must not be overwritten by a meta.
	sawTitleText bool
}

// readPreview parses one document and returns its preview.
//
// `base` is the URL the document was **finally** served from, and it is a parameter
// because a relative `og:image` resolves against where the page ended up rather than
// where the reader asked to go. Getting that wrong produces a card whose image is a
// path on a different host, which is a fetch this package did not vet.
//
// `addresses` is the caller's address policy, passed rather than read so the same switch
// that governed the fetch governs the result: an `Unfurler` built with `NewOver` can reach a
// loopback server, and a preview whose image then pointed at loopback would be a card loading
// something this package never vetted.
func readPreview(body []byte, base *url.URL, addresses bool) (Preview, error) {
	if base == nil {
		return Preview{}, fmt.Errorf("%w: the response carried no final url", ErrNotFetched)
	}

	tree, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return Preview{}, fmt.Errorf("%w: %s", ErrNotHTML, Class(err))
	}

	found := &collected{fields: make(map[string]string, len(metaNames))}
	walk(found, tree)

	preview := Preview{URL: base.String()}

	// `<title>` first, then the metas, and the metas never overwrite each other — the
	// order is what makes "a page that states a title and an `og:title` disagrees"
	// resolve to the document's own rather than to whichever the parser met last.
	if found.sawTitleText {
		preview.Title = truncate(found.title.String())
	}

	if preview.Title == "" {
		preview.Title = truncate(found.first("og:title", "twitter:title"))
	}

	preview.Description = truncate(
		found.first("og:description", "twitter:description", "description"),
	)

	if image := found.first("og:image", "twitter:image"); image != "" {
		preview.Image = resolveImage(image, base, addresses)
	}

	return preview, nil
}

// walk visits every element in the tree, collecting.
func walk(found *collected, node *html.Node) {
	if node.Type == html.ElementNode {
		switch node.Data {
		case "title":
			found.inTitle = true
		case "meta":
			found.readMeta(node)
		case "html", "head", "body":
		default:
		}
	}

	if found.inTitle && node.Type == html.TextNode {
		found.sawTitleText = true
		found.title.WriteString(node.Data)
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walk(found, child)
	}

	// Closed on the way out rather than toggled, so a `<title>` inside a comment or a
	// foreign namespace cannot leave the accumulator open for the rest of the document.
	if node.Type == html.ElementNode && node.Data == "title" {
		found.inTitle = false
	}
}

// readMeta reads one `<meta>` element into `fields`, if its name is one this package
// looks for.
func (c *collected) readMeta(node *html.Node) {
	name := ""

	for _, attribute := range node.Attr {
		value := strings.ToLower(strings.TrimSpace(attribute.Key))

		for _, candidate := range metaNames {
			if value != candidate.attribute {
				continue
			}

			if strings.ToLower(strings.TrimSpace(attribute.Val)) == candidate.value {
				name = candidate.value
			}
		}
	}

	if name == "" {
		return
	}

	content := attrValue(node, attrContent)

	if _, already := c.fields[name]; already {
		// First wins, and the reason is in the type comment: an answer that depends on
		// which of two contradictory tags the parser reached first is an answer no test
		// could pin and no page author could reason about.
		return
	}

	if strings.TrimSpace(content) == "" {
		return
	}

	c.fields[name] = content
}

// first returns the first named field that has a value, in the order given.
func (c *collected) first(names ...string) string {
	for _, name := range names {
		if value := c.fields[name]; value != "" {
			return value
		}
	}

	return ""
}

// attrValue returns an element's attribute, case-insensitively.
//
// The parser lowercases attribute **keys** but not their values, and `content` is the
// only key this package reads — so the case-insensitive key lookup is what stops a page
// writing `CONTENT=` from losing its own description, which is a real thing pages do.
func attrValue(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) {
			return attribute.Val
		}
	}

	return ""
}

// resolveImage turns a page's `og:image` into an absolute URL, or empty.
//
// **Resolved, then re-checked, and the check is the same one `Unfurl` applies.** An
// `og:image` of `http://169.254.169.254/latest/meta-data/` is a URL a browser would
// fetch the moment the card rendered, and a browser fetching it is an SSRF from every
// reader of the page. So the value is run through `checkURL` and — because the *dial*
// is where the address is known — through `isPublic` on the host when the host is
// already an address literal.
//
// The honest limit, stated rather than papered over: a **name** whose `A` record is
// private is not caught here, because resolving it would be a second resolver's answer
// to one question and the dial hook only ever sees a URL this package fetched. What
// this stops is the case a page can name directly, and what the card does with the
// rest is `referrerpolicy="no-referrer"` plus the fact that the browser is the fetcher
// and not this process. `TestAnImageNamingAMetadataAddressIsDropped` holds the part
// that is enforceable.
// `addresses` is the caller's address policy, threaded from the fetch so **one switch governs
// both** — an unfurl that reached a loopback server because of the seam must not then produce a
// card whose image points at loopback.
func resolveImage(image string, base *url.URL, addresses bool) string {
	trimmed := strings.TrimSpace(image)
	if trimmed == "" {
		return ""
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}

	// A protocol-relative URL is resolved against the base's scheme rather than
	// refused, because it is ordinary HTML and refusing it would drop the image from
	// every page that used it.
	// Resolved whenever it carries no scheme, which covers both `/path` and `//host/path`:
	// a protocol-relative URL has an authority and no scheme, so the `parsed.Scheme == ""`
	// test alone is what catches it, and treating it as a relative path would resolve it
	// against the base's **path** rather than its authority — a card showing an image from
	// the wrong place.
	if parsed.Scheme == "" {
		parsed = base.ResolveReference(parsed)
	}

	checked, err := checkURL(parsed.String())
	if err != nil {
		return ""
	}

	// **The same address policy the fetch applies**, by calling `checkAddress` rather than
	// testing `net.ParseIP` here: two places testing the same rule is two answers for it,
	// and the one that matters is the one a browser would act on.
	if addresses {
		if err := checkAddress(checked); err != nil {
			return ""
		}
	}

	return truncate(checked.String())
}

// truncate shortens a field to `maxFieldRunes`, on a rune boundary, and collapses the
// whitespace a title picks up from being written across lines.
//
// **Collapsing is not cosmetic.** `<title>\n  Fish &amp; Chips\n</title>` is three
// lines of source and one title to a reader, and a card that shows the source's line
// breaks is a card whose height depends on the author's formatting. `strings.Fields`
// joining on a single space is the same treatment `html`'s own renderers give
// whitespace, and it is bounded: the fields loop stops at the first string that is not
// a newline, a tab or a space.
func truncate(field string) string {
	collapsed := strings.Join(strings.Fields(field), " ")

	runes := []rune(collapsed)
	if len(runes) <= maxFieldRunes {
		return collapsed
	}

	// Cut, then trim: a truncation that lands on a trailing space leaves a card with a
	// space at its end, which is invisible until two cards are stacked.
	return strings.TrimRight(string(runes[:maxFieldRunes]), " ")
}
