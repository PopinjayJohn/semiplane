// The render pipeline: markdown bytes in, sanitised HTML out, plus the
// structural facts the rest of the content path needs from the render itself.
//
// Three decisions shape this file, and all three are consequences of S-5.1 —
// rendered output is permission-neutral by construction — rather than
// preferences:
//
//   - **No package-level goldmark instance, and no `init()`.** A `Renderer` is
//     constructed explicitly by the composition root, per campaign, and holds the
//     campaign slug and the kind registry it needs. A shared mutable
//     `goldmark.Markdown` is state a test cannot isolate, and a map that fills up
//     as a side effect of importing a package is registration that happened
//     somewhere nobody can see — the argument `content.Registry` already makes
//     for itself.
//   - **The render reports its own structure.** References and anchors come back
//     from `Render`, not from a second parse of the HTML by whoever needs them.
//     C4's broken-link report and C5's cache would otherwise both need an HTML
//     parser, and two HTML parsers over sanitised output is two answers to
//     "what does this page reference".
//   - **Sanitisation is the last thing that happens, and it is not optional.**
//     goldmark runs without `html.WithUnsafe()` (S-4.6) so raw HTML in a vault
//     never becomes markup, and bluemonday then removes what a future change
//     could let through. Two layers, one boundary. The policy is explained where
//     it is built.

package content

import (
	"bytes"
	"fmt"
	"path"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/semiplane/semiplane/internal/content/ext"
	"github.com/semiplane/semiplane/internal/domain"
)

// aliasSeparator is the `|` between a `[[…]]` reference's target and its display
// text.
//
// Distinct from `ext.pipe`, which is the same byte in the scanner that finds the
// construct. Two names for one byte in two packages, and the duplication is worth
// it: this file splits, that one scans, and a reader comparing the two needs to
// see that they are the same character rather than infer it.
const aliasSeparator = "|"

// Renderer renders a campaign's pages.
//
// One per campaign, built by the composition root and shared by every request for
// that campaign, exactly as a `content.Root` is. Safe for concurrent use: see
// the note on `markdown` below.
//
// The two fields it holds are what make it per-campaign rather than a
// free function with four parameters. The slug is needed by C4 to label a
// broken-link report, and by every log line the render path emits; the kind
// registry is what `Parse` discriminates `kind` against, and the renderer
// re-resolves the kind from the same registry so that the value it reports and
// the value the page was written with cannot disagree (S-3.3).
type Renderer struct {
	slug     string
	kind     domain.PageKindRegistry
	markdown goldmark.Markdown
	policy   *bluemonday.Policy
}

// NewRenderer returns a Renderer for one campaign.
//
// kinds may be nil, in which case every page is prose — the same answer
// `Parse` gives for a nil registry, and for the same reason: a caller must never
// have to distinguish "no registry" from "a registry that knows nothing" to know
// it is being handed a page.
//
// The sanitiser policy is built here rather than per render because building it
// is the expensive part and because a policy is immutable once built; sharing one
// across every render of a campaign is what makes the sanitiser a boundary rather
// than a step that might be skipped.
func NewRenderer(slug string, kinds domain.PageKindRegistry) *Renderer {
	policy := newPolicy()

	return &Renderer{
		slug: slug,
		kind: kinds,
		markdown: goldmark.New(
			goldmark.WithExtensions(
				// GFM's parts, named individually rather than as `extension.GFM`,
				// because the table extension has to be constructed with an option
				// and `extension.GFM` is a value that takes none. Naming the four
				// is what makes the alignment setting below possible at all; with
				// `extension.GFM` registered as well, two renderers would claim the
				// table node kind and which one won would depend on the order an
				// unstable sort happened to leave them in.
				extension.Linkify,
				extension.Strikethrough,
				extension.TaskList,
				// Table alignment is rendered as `align="…"` rather than
				// `style="text-align:…"`, because the policy allows no `style`
				// attribute at all. goldmark's default would emit a `style`
				// attribute the sanitiser then strips, and every aligned table in
				// every campaign would lose its alignment silently.
				extension.NewTable(
					extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute),
				),
				extension.Footnote,
				extension.Typographer,
				ext.New(ext.Builtins()...),
			),
			goldmark.WithParserOptions(
				// WithAutoHeadingID gives every heading an `id`, so a
				// `[[Page#Heading]]` anchor and a table of contents both have
				// something to point at.
				//
				// An option rather than a post-pass because goldmark's generator
				// already de-duplicates: two headings with the same text get `foo`
				// and `foo-1`. A pass that rewrote duplicates over the HTML would
				// have to re-derive that numbering and would be guessing at the
				// author's intent while doing it.
				parser.WithAutoHeadingID(),

				// `parser.WithAttribute` is deliberately *not* enabled, though it is
				// what would let an author write `## Heading {#custom}` and choose
				// the id. Two reasons, and the second is the one that decides it:
				//
				// The plan asks for generated ids, and a generated id cannot
				// disagree with itself. An authored one is a second source of truth
				// for the same `id` namespace — an author who writes `{#the-coast}`
				// on the third of three identically-named headings gets a page whose
				// anchor map is the author's arithmetic rather than goldmark's, and
				// a `[[Page#The Coast]]` written before the edit now lands somewhere
				// else.
				//
				// And it is a second way for a vault to plant an id of its choosing,
				// which is the DOM-clobbering surface `policy.go` already bounds as
				// best it can. Generated ids are heading-text-shaped; an authored id
				// is any string at all. If semiplane ever wants named anchors, the
				// right shape is a wiki directive with its own namespace — not a
				// markdown attribute block that silently changes what `id` means.
			),
		),
		policy: policy,
	}
}

// Slug returns the campaign this renderer serves. For labels on a broken-link
// report and for log lines; it is not a capability and not an authorisation
// input.
func (r *Renderer) Slug() string {
	return r.slug
}

// Rendered is the result of rendering one page: the sanitised HTML and the
// structural facts taken from the same parse.
//
// The three facts are here rather than re-derived by callers because each of them
// would otherwise need an HTML parser over sanitised output, and a second parser
// over the same bytes is a second answer:
//
//   - References are what C4 resolves and what the index-time broken-link report
//     is built from (S-5.5). They are in document order, and `Index` is the
//     number the rendered element carries as `data-ref-index`, so a caller can
//     find the element for a reference without searching.
//   - Anchors are the heading ids in the page, in document order, for a table of
//     contents and for telling a reader where they are in a long page.
//   - Kind is the page's kind, re-resolved against the registry rather than
//     copied from the document, so a caller holding a `Document` cannot talk the
//     renderer into reporting a kind the registry does not have.
type Rendered struct {
	// HTML is the sanitised body. It is a fragment, not a document: the renderer
	// emits no `<html>`, `<head>` or `<body>`, because a page is inserted into a
	// templ shell and a nested document is how a stylesheet stops applying.
	HTML string

	// Kind is the kind the page renders as, re-resolved from the document's
	// declared kind against the renderer's registry.
	Kind domain.PageKind

	// References are the page's outbound references in document order, across
	// every extension. Never nil; a page with none has an empty slice, so
	// `len(result.References) == 0` and a nil check are not two ways to ask the
	// same question.
	//
	// `Reference.Index` is the value the rendered element carries as
	// `data-ref-index`, and it is this file's job to make the two the same number:
	// `Resolver.Links` returns its answers in the order it was given this slice, so
	// a caller attaches an `href` by position and never searches the HTML.
	References []Reference

	// Anchors are the page's heading ids in document order, with the level and
	// the heading's own text. Never nil, for the same reason.
	Anchors []Anchor
}

// Reference is one reference a page makes, as the render pipeline observed it and
// as `links.go` receives it.
//
// The split of the two halves is the seam between C3 and C4, and it is drawn at
// the *normalisation* rather than at the parsing:
//
//   - This file owns the **grammar**: where a `[[…]]` construct begins and ends,
//     whether it is a link or an embed, and what text an author put between the
//     delimiters. Splitting that text into a target, an anchor and an alias
//     happens here too, because a split is grammar.
//   - `links.go` owns the **meaning**: taking the vault-root `/` off, taking the
//     `.md` off, deciding what the target names, and resolving it. Those are
//     answers about a campaign, and a renderer has no campaign but its own.
//
// So `Target` and `Anchor` are the author's bytes with the separators removed and
// *nothing else*. A leading `/` is still there, a `.md` is still there, a `..` is
// still there, and no case has been folded. That is deliberate: `links.go` refuses
// what it cannot resolve rather than normalising it away, and a renderer that
// cleaned a target first would turn a reference that ought to be refused into one
// that silently means something else.
//
// The target is **not resolved** and the target's title is **not read**. ADR 0017
// requires the linking page's HTML to be byte-identical for every viewer, which it
// cannot be if a render reads anything out of another page — or out of a page the
// viewer happens to be able to see.
type Reference struct {
	// Index is this reference's position in document order across all
	// extensions, and the value the rendered element carries as
	// `data-ref-index`. It is assigned before rendering, from the same walk that
	// fills the slice, so the element and this struct cannot disagree about which
	// is which.
	Index int

	// Extension is which syntax produced the reference, so a caller can tell a
	// broken `[[Page]]` from a broken `{{statblock:Name}}` and report the two
	// differently, and so `links.go` knows which of its two rules applies.
	Extension ext.Kind

	// Target is the path the author wrote, verbatim, with the `#` anchor split off
	// and nothing else removed.
	//
	// Empty for the two `{{…}}` extensions, whose argument is a free string rather
	// than a page reference — see Arg. Empty also for `[[#Heading]]`, which names
	// an anchor in the current page and no target at all; `links.go` recognises
	// that case from `Anchor` being set and `Target` being empty.
	Target string

	// Anchor is what followed the first `#`, with the `#` kept.
	//
	// The sigil is kept because S-5.9 resolves a `^block-id` and a heading
	// differently and the sigil is how it tells them apart. Empty when the
	// reference named no anchor.
	Anchor string

	// Alias is the display text the author wrote, or empty for none.
	Alias string

	// Arg is the `{{name:arg}}` argument verbatim, and empty for the two `[[…]]`
	// forms. For `{{dice:1d20+5}}` this is the expression, and it is never
	// evaluated here: dice are rolled server-side, and a roll computed at render
	// time or in the browser is unverifiable and breaks the audit trail the
	// realtime plane maintains.
	Arg string

	// Line is the 1-based line of the page body the reference appears on, so a
	// broken-link report can point a GM at a line rather than at a page.
	//
	// Counted from the node's byte offset rather than tracked while parsing,
	// because the parse has no notion of a line: goldmark's inline parser sees a
	// line, not a document. Inventing one there would be a second answer to "where
	// is this" that the collector would then have to agree with.
	Line int
}

// Label returns the text a link to this reference should display.
//
// The author's alias, or the base name of the author's target, and never a title
// read from the target page. That is ADR 0017's rule, and it is a rule about the
// *cache* as much as about privacy: a label derived from the target would make the
// linking page's HTML depend on a document in another campaign, and — for a local
// target — on a title a front-matter edit can change without changing the linking
// page's content hash, so a cached anchor would keep a stale label.
//
// `path.Base` rather than the whole target, so `[[notes/Goblin]]` reads as
// `Goblin` and `[[Some%20Page]]` in another campaign reads as ADR 0017's example
// does. Obsidian's own rule.
//
// The rendered element already carries this text, and so does every `Link` the
// resolver returns. This method exists so that all three are one answer rather
// than three that could disagree.
func (ref Reference) Label() string {
	switch {
	case ref.Alias != "":
		return ref.Alias
	case ref.Target == "":
		// `[[#Heading]]` names an anchor in the current page and no target at all,
		// so there is no path to take a base name of. The anchor is the only text
		// the author wrote, and it is what the link says.
		//
		// Without this the label would be `path.Base("")`, which is `"."` — a
		// period, in the middle of a sentence, on every in-page link in a
		// campaign.
		return strings.TrimPrefix(ref.Anchor, anchorPrefix)
	default:
		return path.Base(ref.Target)
	}
}

// Anchor is one heading in a rendered page: the id it carries and the text it
// displays.
type Anchor struct {
	// ID is the `id` attribute on the heading, as goldmark generated it. Stable
	// for a given page's content, which is what an in-page link needs.
	ID string

	// Level is the heading level, 1 through 6.
	Level int

	// Text is the heading's own inline text, with its markup removed. It is the
	// text a table of contents shows, and it is taken from the AST rather than
	// from the HTML so it does not have to be un-escaped out of an attribute.
	Text string
}

// Render converts a document's prose to sanitised HTML.
//
// The `Document` rather than its `Body` because the front matter has already been
// interpreted and the kind it declares is an input: a page whose `kind` is
// registered renders as that kind, and one whose is not degrades to prose
// (S-3.3). The front-matter block itself never reaches the renderer — it was
// stripped by `Parse` — so no value from it can become markup, which is the
// property `content-model.md` states about the split.
//
// **Concurrency.** goldmark's `Markdown` is safe for concurrent `Convert` once
// constructed, and this renderer relies on that. Both halves of it are: the
// parser and the renderer each guard their one-time setup with a `sync.Once` and
// afterwards only read their configured parser and renderer slices, and
// `Convert` allocates everything mutable per call — a `text.Reader`, a
// `parser.Context` (which owns the heading-id table, so ids do not leak between
// two pages), and a fresh AST. The mutable state is the AST and the parse
// context, and neither is shared. So a `*Renderer` is safe for concurrent
// `Render`, which is what lets one per campaign serve every request, and
// `TestRenderIsConcurrencySafe` holds that under `-race` rather than asserting
// it in a comment.
//
// One thing this method is *not* safe against is being mutated while in use:
// there is no mutator, and that is deliberate.
func (r *Renderer) Render(doc Document) (Rendered, error) {
	source := []byte(doc.Body)
	reader := text.NewReader(source)

	tree := r.markdown.Parser().Parse(reader)

	result := Rendered{
		Kind:       domain.ResolvePageKind(doc.FrontMatter.DeclaredKind, r.kind),
		References: make([]Reference, 0, referenceCapacity),
		Anchors:    make([]Anchor, 0, anchorCapacity),
	}

	// Collected before rendering, and from the same walk that assigns each
	// node's index, so `Ref.Index` and the `data-ref-index` in the element are the
	// same number by construction. That number is the whole of the seam with C4: a
	// resolved href is attached by ordinal, not by re-parsing the HTML.
	collect(&result, tree, source)

	var raw bytes.Buffer
	if err := r.markdown.Renderer().Render(&raw, source, tree); err != nil {
		return Rendered{}, fmt.Errorf("render %s: %w", r.slug, err)
	}

	//nolint:misspell // Sanitize is bluemonday's own method name (the linter's locale is UK).
	result.HTML = r.policy.Sanitize(raw.String())

	return result, nil
}

// referenceCapacity and anchorCapacity are the initial sizes of the two slices a
// render allocates.
//
// Small, and not zero, because they are only an allocation hint: a typical page
// has a handful of references and a few headings, and starting at zero and
// growing means two reallocations each. A page with more grows normally. They are
// not limits — nothing here caps how many references a page may make, because a
// limit on references would be a limit on what an author may write.
const (
	referenceCapacity = 8
	anchorCapacity    = 8
)

// collect walks a parsed document once and fills result's References and
// Anchors, assigning each extension node its document-order index on the way.
//
// One walk rather than two, and the index assignment happens *here* rather than
// in a renderer, for a reason that is about C4: the ordinal in the slice and the
// ordinal in the HTML have to be the same number, and the only way to guarantee
// that is to derive both from a single traversal in a single order.
//
// The walk is in document order because `ast.Walk` is a depth-first pre-order
// traversal, and it is the *only* order used anywhere in this package. A
// references list in any other order would make the index meaningless, and a
// cache keyed on a content hash (S-5.2) over a page whose bytes depended on
// traversal order would be a cache that missed at random.
//
// The error `ast.Walk` returns is dropped, and deliberately. The only function
// this walk calls is the collector's own, which returns nothing, so the error can
// only be `WalkStop` from a callback that chose to stop — and none does. A
// `//nolint` here would be suppressing a real possibility in the general case and
// a fiction in this one, so the case is stated instead: if a future extension's
// collector needs to fail, this is the signature it changes.
func collect(result *Rendered, tree gast.Node, source []byte) {
	//nolint:errcheck // the walk's only callback returns no error; see above.
	_ = gast.Walk(tree, func(node gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}

		switch typed := node.(type) {
		case *ext.Node:
			appendReference(result, typed, source)
		case *gast.Heading:
			appendAnchor(result, typed, source)
		}

		return gast.WalkContinue, nil
	})
}

// appendReference records one extension node as a Reference and gives the node
// its index.
//
// The split of a `[[…]]` body into target, anchor and alias happens here, and
// only the *normalisation* of those parts is left to `links.go`. See Reference for
// why the line is drawn there: a split is grammar, and a target's meaning is a
// question about a campaign that a renderer cannot answer.
//
// The `{{…}}` forms carry their text in Arg and nothing else, because their
// argument is a free string and not a page reference. `{{statblock:Goblin}}` names
// a game object and `{{dice:1d20+5}}` names an expression, and treating the second
// as a path would put a dice formula in front of a resolver.
func appendReference(result *Rendered, node *ext.Node, source []byte) {
	ref := Reference{
		Index:     len(result.References),
		Extension: node.Extension(),
		Line:      lineOf(source, node.Offset()),
	}

	if node.Extension() == ext.KindStatblock || node.Extension() == ext.KindDice {
		ref.Arg = node.Inner()
	} else {
		ref.Target, ref.Anchor, ref.Alias = splitReference(node.Inner())
	}

	result.References = append(result.References, ref)
	node.SetIndex(ref.Index)
}

// splitReference divides the text between a `[[…]]` construct's brackets into a
// target, an anchor and an alias.
//
// The alias is everything after the first `|`, and the split happens first so a
// `#` inside an alias belongs to the alias: `[[Page#Head|the # part]]` links to
// `Page#Head` and is labelled `the # part`, which is what Obsidian means. The
// anchor is then everything after the first `#` of what is left, and it keeps its
// sigil — S-5.9 resolves `^block-id` and a heading differently, and the sigil is
// how it tells them apart.
//
// Nothing is removed beyond the separators. A leading `/`, a `.md`, a `..`, a
// space, a case: all of it survives, because `links.go` refuses what it cannot
// resolve rather than absorbing it, and a cleaner here would silently turn a
// reference that ought to be refused into one that means something else.
//
// An anchor-only reference — `[[#Heading]]` — arrives as an empty target and a
// non-empty anchor, which is a well-formed reference to the current page and is
// reported as one.
func splitReference(inner string) (target, anchor, alias string) {
	head, after, hasAlias := strings.Cut(inner, aliasSeparator)
	if hasAlias {
		alias = trimASCII(after)
	}

	head, after, hasAnchor := strings.Cut(head, anchorPrefix)
	if hasAnchor {
		anchor = anchorPrefix + after
	}

	return trimASCII(head), anchor, alias
}

// lineOf returns the 1-based line of source that offset falls on.
//
// A byte-offset lookup rather than a tracked line counter, because the parse has
// no notion of a line: goldmark's inline parser sees a line, not a document, and
// counting lines during the walk would mean counting newlines in every text node
// that is not a reference. One `bytes.Count` over the prefix is O(n) in the
// prefix and happens once per reference, which for a page with a handful of
// references is nothing, and for a page with thousands is still cheaper than
// walking the prose.
//
// An offset past the end of the source — which cannot happen, and which would mean
// the parse and the source disagree — yields the last line rather than an error.
// A report that says "line 1" for a reference that exists is wrong; a report that
// refuses to name a line for one is worse.
func lineOf(source []byte, offset int) int {
	if offset <= 0 {
		return 1
	}

	if offset > len(source) {
		offset = len(source)
	}

	return bytes.Count(source[:offset], []byte{'\n'}) + 1
}

// appendAnchor records one heading as an Anchor.
//
// A heading with no id is skipped rather than recorded with an empty one: an
// anchor list containing an empty id is a table of contents with an entry that
// goes nowhere, and the id is generated rather than authored, so an absent one
// means the heading is not a heading — a `#` with no text, which goldmark
// discards, or a heading whose attribute was stripped.
//
// The level and text are read from the AST, never from the HTML. Reading them
// from the HTML would mean either an un-escaper or a regex, and both are a second
// opinion about a document that has already been rendered once.
func appendAnchor(result *Rendered, heading *gast.Heading, source []byte) {
	value, ok := heading.AttributeString("id")
	if !ok {
		return
	}

	id, ok := value.([]byte)
	if !ok {
		return
	}

	result.Anchors = append(result.Anchors, Anchor{
		ID:    string(id),
		Level: heading.Level,
		Text:  headingText(heading, source),
	})
}

// headingText is a heading's displayed text with its markup removed.
//
// The concatenation of its text, string, code-span and autolink children, which
// is what the heading *shows*: emphasis delimiters and link syntax are not in the
// text nodes, so what is left is the words. Entity references are resolved,
// because a heading written `(c)` should read `©` in a table of contents rather
// than `&copy;` — and they are resolved rather than escaped, because this is
// plain text for a template, not markup, and escaping it here would give the
// template `&amp;` to display.
func headingText(heading *gast.Heading, source []byte) string {
	var out strings.Builder

	for child := heading.FirstChild(); child != nil; child = child.NextSibling() {
		appendText(&out, child, source)
	}

	return out.String()
}

// appendText appends a node's textual content to out.
//
// Three node types carry text of their own and are read directly; everything else
// is recursed into, which is what makes `**Drowned**` and `[a link](x)` and a
// `code span` all contribute their words rather than nothing. Recursing generally
// rather than listing every container is deliberate: a container goldmark adds in a
// future version should show its text in a table of contents without this function
// being taught about it, and a container that contributes no text contributes none.
//
// `RawHTML` is the exception and is skipped, not recursed into. S-4.6 means raw
// HTML never reaches the output, so there is nothing to show for it, and
// reproducing the author's markup here would put author-controlled bytes into a
// string the sanitiser does not see.
func appendText(out *strings.Builder, node gast.Node, source []byte) {
	switch typed := node.(type) {
	case *gast.Text:
		// Entities resolved rather than escaped: this is plain text for a
		// template, and escaping here would hand the template `&amp;` to display.
		out.Write(
			util.ResolveNumericReferences(util.ResolveEntityNames(typed.Segment.Value(source))),
		)
	case *gast.String:
		out.Write(typed.Value)
	case *gast.AutoLink:
		// The URL, because that is what an `<https://…>` autolink displays.
		out.Write(typed.URL(source))
	case *gast.RawHTML:
		return
	default:
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			appendText(out, child, source)
		}
	}
}
