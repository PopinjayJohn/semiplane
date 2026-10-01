// The sanitisation policy: which HTML the render pipeline is willing to emit.
//
// This is a security boundary and it is written as one. S-4.6 forbids
// `html.WithUnsafe()` because the content root is writable by an Obsidian sync,
// by an Obsidian community plugin, and by any device that syncs that vault — so
// every byte reaching this file is attacker-reachable by a GM who was persuaded
// to accept a shared vault, and by a plugin the GM installed unread. Two layers
// enforce that: goldmark runs without `WithUnsafe()`, so raw HTML in the source
// never becomes markup, and bluemonday then removes anything that got through
// anyway. The first layer does the work; the second exists so that a future
// change to the first is not immediately a stored XSS against every public
// campaign.
//
// One consequence of the first layer is content loss rather than a security
// property, and it belongs here because a GM will eventually report it as a bug.
// A **block-level** raw HTML element in a markdown file — `<p>text</p>`,
// `<h1>heading</h1>`, a hand-written `<table>`, a `<script>` — is an *HTML block*
// in CommonMark terms. goldmark does not convert it into the document AST; it
// writes `<!-- raw HTML omitted -->` in its place, and the element's text goes
// with it.
//
// An *inline* raw element is different: `a <span>b</span> c` is parsed, the text
// survives, and this policy strips the tag. So the rule a GM needs is that prose
// goes in markdown rather than in HTML — which is not a quirk of this
// implementation but what CommonMark specifies, and what Obsidian and GitHub do
// too. What would be unacceptable is for it to be an accident nobody wrote down,
// so the test suite states it: TestRawHTMLBlockTextIsDropped.
//
// The policy is an **allowlist**, never a denylist. Every element and attribute
// absent from it is removed, so an element nobody thought of is removed too. A
// denylist has to be updated when the attack surface changes, and nothing in this
// repo is in a position to know when that is.
//
// It is built from `UGCPolicy()` and then narrowed, rather than from an empty
// `NewPolicy()`. The base is a well-reviewed statement of "HTML that is safe for
// user-generated content", and discarding it would mean re-deriving a decision
// about seventy elements no campaign needs. Narrowing is the part that is this
// project's decision rather than the library's, and it is spelled out below.
//
// # What survives, and why
//
// **Block structure**: `p`, `h1`–`h6`, `blockquote`, `pre`, `ul`/`ol`/`li`,
// `dl`/`dt`/`dd`, `hr`, `br`, `table` and its parts, `figure`/`figcaption`,
// `article`/`section`/`aside`, `details`/`summary`, `div`, `span`, `sup`, `sub`.
// A page is prose, and prose is these elements.
//
// **Phrase content**: `em`, `strong`, `del`, `code`, `kbd`, `samp`, `var`, `q`,
// `cite`, `abbr`, `dfn`, `mark`, `small`, `s`, `u`, `time`.
//
// **Links and images**: `a[href]`, `img[src|alt|width|height]`.
//
// **Attributes that survive globally**: `id`, `title`, `dir`, `lang` — all four
// from bluemonday's `AllowStandardAttributes`.
//
// **Attributes that survive on named elements only**: `class`, `role`, `align` on
// `th`/`td`, and the extensions' three `data-` names. Each is argued below.
//
// **The elements the semiplane extensions emit**: `a` and `span`, and nothing
// else. An extension that wanted a third element would add it here, in review.
//
// # What does not survive, and why
//
// Each exclusion is a class of thing, not a list of things that occurred to
// somebody:
//
//   - **Script and style**: `script`, `style`, `noscript`, `template`.
//     `AllowUnsafe(false)` — the default, and never called with `true` here — is
//     what makes bluemonday drop their *content* along with the tag, so a
//     `<style>`-wrapped payload does not survive as visible text either.
//   - **Framing and plugins**: `iframe`, `frame`, `frameset`, `object`, `embed`,
//     `applet`, `param`, `portal`. `embed` is worth naming twice: it is forbidden
//     here as an *element*, and it is also the name of a semiplane extension. The
//     two are unrelated — the extension emits a `<span>`, and the HTML element is
//     gone — and the collision in the vocabulary is exactly the kind of thing a
//     reviewer stops to re-read, so it is stated here.
//   - **Vector and math**: `svg`, `math`. Both have a long history as sanitiser
//     bypasses (`svg` via a `<script>` inside it, `math` via `annotation-xml`
//     and `mtext`), and a TTRPG wiki has no use for either.
//   - **Forms**: `form`, `button`, `select`, `option`, `textarea`, `label`,
//     `fieldset`, `output`, `progress`, `meter`. A page is not a form. S-6.5 makes
//     content editing GM-only and it happens in a *route*, not in a page body, so
//     a form in content is an attempt to collect something from a reader.
//     `input` is the single exception and is bounded to an inert
//     `type="checkbox" disabled` with no name, no value and no form, which is what
//     GFM's task list emits and the only reason `input` is here at all.
//   - **Media that loads and plays**: `audio`, `video`, `source`, `track`,
//     `canvas`. `img` survives because a battle map is a picture; audio and video
//     autoplay, are a reader-tracking surface, and nothing in the product needs
//     them.
//   - **Document-level**: `html`, `head`, `body`, `title`, `base`, `link`,
//     `meta`. The output is a fragment inserted into a templ shell, so a
//     `<base>` in particular would retarget every relative URL on the page
//     including the campaign's own.
//   - **Attributes that carry behaviour**: every `on*` handler (bluemonday drops
//     all of them by construction, since none is on the allowlist), `style`,
//     `srcset`, `formaction`, `ping`, `target`, `rel`, `accesskey`, `tabindex`,
//     `contenteditable`, `autofocus`, `is`, `accesskey`.
//
// # The judgement calls
//
// ## `class` survives, but only these values
//
// A callout has to be *styled*, and the styles are semiplane's, so a `class` has
// to reach the browser. UGCPolicy forbids `class` outright, on the stated ground
// that "we are not allowing users to style their own content" — which is right
// for a comment box and wrong here, because semiplane *does* style its own
// callouts and phase 10's `[!secret]` needs to mark one.
//
// So `class` is allowed and narrowed to the values this pipeline emits:
// `wikilink`, `embed`, `statblock`, `dice` for the extensions;
// `footnote-ref`, `footnote-backref`, `footnotes` for goldmark's footnote
// extension; and `language-…` for a fenced code block's language tag. Two
// consequences, both intended:
//
//   - An author cannot invent a class that does anything. Behaviour is bound to a
//     *selector* in semiplane's own stylesheet, and an author cannot write a
//     selector. A class of their choosing matches no rule, so the answer to
//     "can an attacker trigger behaviour with a class" is that there is no
//     behaviour to trigger — there is only a rule that does not match.
//   - The list is a maintenance obligation: a new extension adds its class here,
//     and a class added here with no rule behind it is inert. That is the safe
//     direction for the failure — an inert class is a missing style, and an
//     unlisted class is a missing element.
//
// `data-testid` is not allowed. The UI's test ids live in templ components, which
// are not sanitised, and a `data-testid` arriving from a vault would be an author
// naming a test hook — a way to collide with a real one.
//
// ## `style` does not survive
//
// `style` is the most dangerous attribute on any allowlist and it buys almost
// nothing here. It enables clickjacking overlays (`position:fixed` with a high
// `z-index`), UI redress, exfiltration through `background:url(…)` pointed at a
// third party, and content hiding a reader cannot see. A page's appearance is the
// design system's, per the UI record; an author's `style` attribute is not a
// supported way to change it.
//
// The one thing `style` was carrying is table alignment, and it is *moved* rather
// than lost: goldmark's table extension is configured to emit `align="…"` instead
// of `style="text-align:…"`, and `align` is allowed on `th`/`td` against
// bluemonday's own `CellAlign` pattern. The alignment survives; the escape hatch
// does not.
//
// ## `id` is allowed, `name` is not
//
// `id` is allowed globally, which bluemonday's `AllowStandardAttributes` does.
// Two reasons, and they are the two halves of the anchor problem:
//
//   - goldmark's `WithAutoHeadingID` emits an `id` on every heading, and both
//     `[[Page#Heading]]` and a table of contents need something to point at. A
//     sanitiser that stripped `id` would leave every anchor link on the wiki dead.
//   - goldmark's id generator **de-duplicates within a document**: two headings
//     with the same text get `the-coast` and `the-coast-1`. So the ids in a page
//     are unique by construction, which is the property that makes them usable as
//     link targets — and it is a property of the *generator*. A post-pass that
//     rewrote duplicates would have to re-derive the same numbering and would have
//     to guess at the author's intent while doing it.
//
// The residual risk of allowing `id` is DOM clobbering: an element with
// `id="someGlobalName"` shadowing a variable the page's own script reads. That is
// bounded rather than eliminated, because the only ids that can appear are ones
// goldmark generated from heading text and heading text is author-chosen, so
// `id="campaignTitle"` is reachable. It is accepted because the alternative is
// every in-page anchor on the wiki broken, and because the values an author can
// reach are heading-text-shaped while the names a clobbering attack needs are
// chosen by the application. If that trade ever goes the other way, the fix is a
// prefix on generated ids — not the removal of `id`.
//
// `name` is **not** allowed. It is the legacy anchor attribute, redundant with
// `id` on every browser semiplane supports, and it is a second way to name an
// element that a clobbering attempt could use. UGCPolicy allows it only on
// `map`/`area`, neither of which survives here, so the attribute is simply absent
// from the policy.
//
// ## `role` is allowed, narrowly
//
// goldmark's footnote extension emits `role="doc-noteref"`, `doc-footnote"`,
// `"doc-backlink"` and `"doc-endnotes"`, and those are how a screen reader
// announces a footnote. Dropping them would be an accessibility regression on
// every page that has one.
//
// `role` is also one of the more dangerous attributes to allow loosely, because
// it overrides an element's implicit role: `role="button"` on a link, or
// `role="application"` on a container, changes what assistive technology does with
// the page. So it is allowed against a pattern matching exactly the four values
// the footnote extension emits. A `role` an author writes is dropped, which is
// correct — a page body has no business declaring roles.

package content

import (
	"regexp"

	"github.com/microcosm-cc/bluemonday"
)

// The patterns the policy matches attribute values against.
//
// bluemonday's own are reused rather than rewritten: `CellAlign` is the library's
// answer for `align`, and a second pattern for the same four values would be a
// second answer.
var (
	// extensionClasses is the set of class values `internal/content/ext` emits,
	// one per extension. A new extension adds its class here.
	extensionClasses = regexp.MustCompile(`^(wikilink|embed|statblock|dice)$`)

	// extensionNames is the same set as a bare alternation, for `data-ext`. Kept
	// separate from extensionClasses because the two attributes have different
	// jobs — one is a styling hook phase 5 may rename, the other is the identity a
	// live layer keys on — and one pattern serving both would make a rename of
	// either silently change the other.
	extensionNames = regexp.MustCompile(`^(wikilink|embed|statblock|dice)$`)

	// footnoteClasses is the set of class values goldmark's footnote extension
	// emits on its link, backlink and container elements.
	footnoteClasses = regexp.MustCompile(`^(footnote-ref|footnote-backref|footnotes)$`)

	// languageClass matches a fenced code block's `class="language-…"`.
	//
	// It is the one class value that is not an exact list, and the reason is that
	// syntax highlighting is keyed on the language *name*: a highlighter looks up
	// `.language-rust`, and there is no way to enumerate the languages an author
	// might write. What the pattern buys is that the value cannot contain a quote,
	// a space or a `<`, so it cannot close the attribute or introduce a second one.
	// The sanitiser escapes it regardless; this bounds what survives *after*
	// escaping, which is the class an author can put semiplane's own rules in
	// front of.
	languageClass = regexp.MustCompile(`^language-[A-Za-z0-9_+#.-]+$`)

	// footnoteRoles is the set of ARIA roles goldmark's footnote extension emits.
	// Anything else is dropped: `role` overrides an element's implicit role, so an
	// open pattern here is a way to change what a screen reader does with a page.
	footnoteRoles = regexp.MustCompile(`^(doc-noteref|doc-footnote|doc-backlink|doc-endnotes)$`)

	// digits matches a non-negative integer.
	//
	// `data-ref-index` is an ordinal the render pipeline assigns, so the only value
	// that can appear is a decimal integer. Bounding it means a `data-` value that
	// somehow carried a space or a quote is dropped rather than passed on.
	digits = regexp.MustCompile(`^\d+$`)

	// brokenMarkerValue is the only value `data-broken` may carry. Anchored, so
	// `true-ish` and `TRUE` are both stripped rather than half-matched.
	brokenMarkerValue = regexp.MustCompile(`^true$`)
)

// pageElements is every element a rendered page may contain.
//
// The allowlist, and the whole security argument of the second layer: what is not
// on this list does not reach a reader's browser even if the first layer lets it
// through. It is the union of what CommonMark and GFM produce, what goldmark's
// footnote and table extensions add, and what the semiplane extensions emit.
var pageElements = []string{
	// Sectioning and grouping.
	"article", "aside", "section", "figure", "figcaption", "div", "p",

	// Headings and separation.
	"h1", "h2", "h3", "h4", "h5", "h6", "hr", "br", "wbr",

	// Quotation.
	"blockquote", "q", "cite",

	// Lists. `dl`/`dt`/`dd` are not CommonMark, but a definition list is ordinary
	// prose and UGCPolicy's `AllowLists` already covers them.
	"ul", "ol", "li", "dl", "dt", "dd",

	// Tables, GFM.
	"table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption", "colgroup", "col",

	// Code.
	"pre", "code", "kbd", "samp", "var",

	// Inline phrasing.
	"em", "strong", "del", "ins", "sub", "sup", "small", "s", "u", "mark",
	"abbr", "dfn", "bdi", "bdo", "time",

	// Links and images. `a` and `span` are also what the extensions emit.
	"a", "img", "span",

	// GFM's task-list checkbox, and nothing else. See the `input` rule in
	// newPolicy: an inert `type="checkbox" disabled` with no name, no value and no
	// form, which is a picture of a box rather than a control.
	"input",

	// Disclosure. `details`/`summary` is how a GM collapses a long aside, and
	// `open` on `details` is a boolean with no script attached.
	"details", "summary",
}

// newPolicy builds the render pipeline's sanitiser policy.
//
// Built once per Renderer and shared across every render it serves: a
// `bluemonday.Policy` is read-only after construction, and building one per
// render would make the sanitiser a per-page-read cost for no benefit.
//
// bluemonday offers no way to *remove* an element from a policy, so narrowing
// UGCPolicy means building a fresh policy and re-applying what is kept. That is
// why the attribute rules live in their own function: they are applied to the
// fresh policy, and a rule added to one place cannot be forgotten in the other.
func newPolicy() *bluemonday.Policy {
	policy := bluemonday.NewPolicy()

	// Every element, with attributes optional.
	//
	// `AllowNoAttrs` rather than `AllowElements` because bluemonday's two
	// spellings differ on a case that does come up: an element that matches
	// `AllowElements` but has no attribute rule keeps its *text* either way, so
	// the difference is not text loss — it is that `AllowElements` matches
	// elements nobody has decided about yet, so a later `pageElements` edit
	// would change which attributes survive on every element in one step. Naming
	// the elements and their attributes separately is what makes the policy
	// readable as a list of decisions.
	//
	// What this cannot fix, and what is worth knowing before someone reaches for
	// it: a *block-level* raw HTML element never arrives here as an element at
	// all. goldmark treats `<p>text</p>` in a markdown file as an HTML block, does
	// not convert it to the document AST, and writes `<!-- raw HTML omitted -->`
	// — so its text is gone before this policy runs. The package comment above
	// explains the distinction; the sanitiser is the second of two layers, not the
	// one that decides this.
	policy.AllowNoAttrs().OnElements(pageElements...)

	// `id`, `title`, `dir`, `lang`, globally. `id` is why this call is here at
	// all; see the `id` discussion in this file's package comment.
	policy.AllowStandardAttributes()

	// The URL policy, set explicitly rather than through `AllowStandardURLs`.
	//
	// `AllowStandardURLs` is the convenience that also turns on
	// `rel="nofollow"` on every fully-qualified link. That is a search-engine hint
	// with no meaning on a self-hosted wiki, it would be rewritten onto every link
	// in every campaign, and it is not a security control — so only the URL
	// *validation* is wanted, and it is stated here in full: relative URLs, and
	// `http`, `https`, `mailto`. `javascript:`, `data:`, `vbscript:` and `file:`
	// are not in the list, which is what neutralises them.
	policy.RequireParseableURLs(true)
	policy.AllowRelativeURLs(true)
	policy.AllowURLSchemes("http", "https", "mailto")

	// `href` on an anchor, and nothing else. This is the one attribute in the
	// policy that turns a page into a page a reader can navigate *out of*, so it
	// is worth being explicit about what happens to a bad value: bluemonday parses
	// the URL and requires its scheme to be one of the three above, so
	// `javascript:`, `data:` and `vbscript:` are removed and the anchor is left
	// with its text and no destination. That is a neutralisation, not a refusal —
	// the sentence still reads, which is what a GM whose sync pulled in a hostile
	// link wants to happen.
	//
	// `target` is deliberately absent, so no link can open a window holding a
	// handle to this one, and `rel` is absent because the `nofollow` that
	// `AllowStandardURLs` would add is meaningless here.
	policy.AllowAttrs("href").OnElements("a")

	// Images, spelled out rather than via `AllowImages` so the attribute set is
	// visible here: `src` (URL-validated by the policy above), `alt`, and the two
	// dimensions. `alt` is an accessibility requirement rather than a nicety —
	// the UI record's 1.1.1 floor applies to every image on a page — and `width`
	// and `height` exist so a map does not reflow the prose around it as it loads.
	policy.AllowAttrs("src").OnElements("img")
	policy.AllowAttrs("alt").Matching(bluemonday.Paragraph).OnElements("img")
	policy.AllowAttrs("width", "height").Matching(bluemonday.NumberOrPercent).OnElements("img")

	// A GFM task-list checkbox, as an inert `<input type="checkbox" disabled>`.
	//
	// The element is normally forbidden as a form control, and this is the one
	// exception, so the exception is spelled out rather than implied: `type` is
	// constrained to `checkbox` and nothing else, `disabled` and `checked` are the
	// only other attributes permitted, and there is no `name`, no `value`, no `form`
	// and no event handler. An `input` built that way cannot be submitted, cannot
	// carry a value anywhere, cannot be focused, and cannot be made to do anything
	// by an author. It is a picture of a box, which is all a rendered wiki page can
	// honestly offer — the real checkbox belongs to a form, and content is not one.
	policy.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	policy.AllowAttrs("checked", "disabled").
		Matching(regexp.MustCompile(`^(|checked|disabled)$`)).
		OnElements("input")

	// `open` on `details`, bounded to the empty string or `open` — the only two
	// values the attribute has.
	policy.AllowAttrs("open").
		Matching(regexp.MustCompile(`^(|open)$`)).
		OnElements("details")

	// Blockquote and quote citations are URLs, so they get the URL policy, and
	// `datetime` on `time`/`del`/`ins` is bounded by ISO 8601 rather than free.
	policy.AllowAttrs("cite").OnElements("blockquote", "q", "del", "ins")
	policy.AllowAttrs("datetime").Matching(bluemonday.ISO8601).OnElements("time", "del", "ins")

	// `dir` on `bdi`/`bdo` needs a value from a fixed set, which is a different
	// rule from the global `dir` UGCPolicy allows.
	policy.AllowAttrs("dir").Matching(bluemonday.Direction).OnElements("bdi", "bdo")

	// colspan and rowspan are what makes a GFM table usable, and they are
	// integers. UGCPolicy's `AllowTables` covers them; restated because this
	// policy is built fresh and a rule that only exists in the base would be a
	// rule this file does not mention.
	policy.AllowAttrs("colspan", "rowspan").Matching(bluemonday.Integer).OnElements("td", "th")

	applyAttributePolicy(policy)

	return policy
}

// applyAttributePolicy adds the rules that are about *semiplane's* output rather
// than about HTML in general: the class values the extensions and goldmark's
// footnote extension emit, the footnote ARIA roles, the alignment attribute that
// replaced `style`, and the three `data-` names the extensions write.
//
// A function rather than inline code so the narrowing above has exactly one place
// where per-element rules live. `newPolicy` is called once per Renderer and there
// is no second copy of these rules anywhere.
func applyAttributePolicy(policy *bluemonday.Policy) {
	policy.AllowAttrs("class").Matching(extensionClasses).OnElements("a", "span")
	policy.AllowAttrs("class").Matching(footnoteClasses).OnElements("a", "div")
	policy.AllowAttrs("class").Matching(languageClass).OnElements("code")

	policy.AllowAttrs("role").Matching(footnoteRoles).Globally()

	policy.AllowAttrs("align").Matching(bluemonday.CellAlign).OnElements("th", "td")

	// The extensions' `data-` attributes, and only those four.
	//
	// `AllowDataAttributes()` is deliberately *not* called. It is one call, and it
	// allows every `data-*` attribute on every element — a much wider door than it
	// looks, because a `data-` attribute is inert until some code reads it, and the
	// failure mode of the wide version is that a later phase adds a client that
	// reads one, at which point vault content can drive that client. Naming the
	// attributes the renderer actually writes makes adding a fifth a change to
	// this file, in review, rather than a consequence of a library default.
	policy.AllowAttrs("data-ext").Matching(extensionNames).OnElements("a", "span")
	policy.AllowAttrs("data-ref-index").Matching(digits).OnElements("a", "span")
	policy.AllowAttrs("data-arg").OnElements("span")

	// `data-broken` marks a reference the resolver could not satisfy, so the
	// stylesheet can render it as a dead link rather than as a working one.
	//
	// The alternative is a link that 404s: the resolver gives an unresolved
	// reference a real href, because the page may be created later by a sync, and
	// a 404 inside a campaign the reader is already authorised for is a worse
	// answer than a link that looks dead. So the marker is not cosmetic: without
	// it, a reader clicking a broken wikilink gets the not-found page and cannot
	// tell that from clicking a working one.
	//
	// The value is constrained to `true` and nothing else. A marker whose value a
	// vault could choose would be a second, author-controlled vocabulary on the
	// same element, and there is no reader that needs more than one spelling.
	// bluemonday's `Matching` takes a regexp, so "exactly true" is an anchored
	// one; the alternative, allowing the attribute bare, would let a vault write
	// `data-broken="maybe"`, which no stylesheet matches and which therefore
	// renders as a link that is neither broken nor fine.
	policy.AllowAttrs("data-broken").Matching(brokenMarkerValue).OnElements("a")
}
