// The link layer: what a `[[wikilink]]` and an `![[embed]]` resolve to, and the
// record a reference leaves behind when it does not resolve.
//
// render.go has already parsed a page's references and rendered a skeleton for
// them — a bare `<a>` per wikilink, an empty `<span>` per embed, each carrying
// its document-order ordinal as `data-ref-index`. This file supplies the other
// half: the address for each of those elements, and the record the broken-link
// report is built from. Nothing here renders and nothing here re-parses.
//
// There are two entry points, and the split between them is the whole design.
//
//	Link, Links    the render path. Answers from the home campaign and nothing
//	               else, so the markup it produces is a function of the page's
//	               bytes and the home campaign's own index.
//	Records        the index-time path. Applies S-5.5's full three-step order,
//	               and is *handed* the set of campaigns the viewer may see.
//
// The render path is incapable of consulting another campaign. A Resolver holds
// one *Root and one PageIndex, both of the home campaign, and there is no field,
// no constructor parameter and no accessor through which a second campaign could
// arrive. "Never probe a campaign the viewer cannot see" (S-5.5) is therefore not
// a discipline this file follows; it is a capability the type does not have. The
// index-time path is where the visible set enters, and it enters as an argument,
// so the campaigns it can probe are exactly the campaigns its caller named.
//
// Nothing here derives visibility either. A Resolver is never handed a requestor,
// a membership list or a `*store.Store`, so a second copy of the S-8 matrix cannot
// appear in this file — the access decision stays where ADR 0024 put it, in the
// gate the route mounts.
//
// The split is not a convenience. If the render path searched the viewer's
// visible campaigns, the href it produced would differ between a GM and a
// player, and the linking page's HTML would stop being byte-identical for every
// viewer. That is the render cache's invariant (S-5.1): exactly two variants per
// page, not one per visible-campaign-set. ADR 0017 therefore makes a
// cross-campaign reference resolve for the *record* and not for the *markup*, and
// what the reader is shown is always the same anchor. The record is
// viewer-dependent and the HTML is not, which is exactly why the HTML must not
// encode anything about resolution.
//
// There is a second structural property, and it is the one that makes inlining
// impossible rather than merely discouraged: **nothing in this file returns
// another page's bytes.** Not a Target, not a rendered body, not a Target for a
// campaign other than the home one. A caller that wants an embed filled goes and
// renders the target itself, through the cache, for a target this file has
// already established is in the home campaign. The refusal in ADR 0017 is
// therefore not a rule a future change has to remember; it is the absence of a
// capability.

package content

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/semiplane/semiplane/internal/content/ext"
	"github.com/semiplane/semiplane/internal/domain"
)

const (
	// campaignRouteSegment, wikiRouteSegment and assetRouteSegment are the fixed
	// segments of the URL scheme (S-9): `/c/{slug}/wiki/{path}` and
	// `/c/{slug}/assets/{path}`.
	//
	// Constants rather than literals because they appear in an href on every link
	// of every page, and a typo in one produces a 404 that reads as broken content
	// rather than as a bug.
	campaignRouteSegment = "c"
	wikiRouteSegment     = "wiki"
	assetRouteSegment    = "assets"

	// pageExtension is the suffix a page's path loses on its way into a URL.
	//
	// Obsidian writes `[[Goblin]]` and `[[Goblin.md]]` for the same page, and
	// render.go hands the target over verbatim precisely so that this layer is
	// where that decision is made. It is not put back, which is why ADR 0017's
	// example ends in `/wiki/Some%20Page`: the wiki route has to accept the
	// extensionless spelling, and an href that carried the extension would also
	// have to know when to drop it again.
	pageExtension = ".md"

	// pathSeparator is the one separator a root-relative path may contain.
	// Obsidian writes `/` on every platform, and a Windows-authored vault has to
	// resolve the same way, so the reference grammar is slash-only even on a host
	// whose path separator is not.
	pathSeparator = "/"

	// rootRefPrefix is the Obsidian vault-relative spelling. A reference written
	// `/Target` means the top of the vault, which here is the top of *this*
	// campaign: a cross-campaign reference spells its campaign out (see
	// splitQualifier), so a leading slash is never a filesystem root — which is
	// what Root.Resolve would, correctly, refuse it for.
	rootRefPrefix = "/"

	// anchorPrefix is the `#` render.go leaves on an anchor, because it keeps the
	// sigil so a `#Heading` can be told from a `^block-id`. This layer strips it:
	// a fragment in a URL is introduced by the fragment separator and must not
	// carry a second one.
	anchorPrefix = "#"

	// dotSegment and parentSegment are the two path segments with special
	// meaning, named so the checks below read as claims rather than as string
	// comparisons.
	dotSegment    = "."
	parentSegment = ".."
)

// ErrCrossCampaignEmbed means a reference asked to embed a page or asset that
// resolves outside its campaign.
//
// Its own answer rather than one of Root's three, because the path was inside a
// root and it was well formed. Reporting it as an escape would tell the author
// their link was malformed, and would tell a caller the refusal was about
// confinement when it is about the render cache. ADR 0017 refuses the embed on
// exactly the argument that makes a cross-campaign *link* a plain hyperlink: an
// embed is an inline.
var ErrCrossCampaignEmbed = errors.New("content: cross-campaign embed is refused")

// Origin is where a page's references are written: the page's own path, which an
// anchor-only reference needs, and the directory they resolve against.
//
// A value rather than a `Dir` parameter, because those two are the likeliest
// mistake in this layer's API and a `Dir` cannot catch it. `DirOf` returns a
// directory; `[[#Heading]]` names a place on the *page*; and the page's own href
// is what that reference has to resolve to. Passing the page's path gets both
// right by construction, and there is no longer a way to pass one without the
// other.
//
// The zero Origin is the content root, which is a page named "." — not a page, so
// `Link` refuses it. Build one with NewOrigin.
type Origin struct {
	page string
	dir  Dir
}

// NewOrigin returns the Origin for a page at pagePath.
//
// pagePath is the page's root-relative path, which is what `Rendered` and the wiki
// URL both carry. A path that does not stay inside the root is refused with Root's
// own sentinel rather than normalised, because a page whose path escapes is a
// broken page and not a reference anybody may follow.
func NewOrigin(pagePath string) (Origin, error) {
	dir, err := DirOf(pagePath)
	if err != nil {
		return Origin{}, fmt.Errorf("reference origin for %s: %w", pagePath, err)
	}

	cleaned, err := cleanRef(pagePath)
	if err != nil {
		return Origin{}, fmt.Errorf("reference origin for %s: %w", pagePath, err)
	}

	return Origin{page: cleaned, dir: dir}, nil
}

// Page returns the page's root-relative path, with its markdown extension.
//
// The value Origin was built from, and what an anchor-only reference resolves to.
func (o Origin) Page() string {
	return o.page
}

// Dir returns the directory the page's references resolve against.
//
// The referring page's *directory*, which is what a relative reference in
// markdown means, and what makes `[[../sibling]]` in `notes/deep/page.md` a page
// in `notes/` rather than a page in the campaign root.
func (o Origin) Dir() Dir {
	return o.dir
}

// parsed is a reference after the normalisation render.go deliberately left to
// this layer: the vault-root marker moved out of the target, the `.md` taken off,
// and the `#` taken off the anchor.
//
// Three separate adjustments and one value, because they have to happen in that
// order — a vault-root reference to `Page.md` is `/` then `Page.md` then `Page` —
// and a caller that did them in the other order would produce a link to `/Page.md`
// for a page whose real path is `notes/Page.md`.
type parsed struct {
	target    string
	anchor    string
	vaultRoot bool
}

// parse normalises one reference, and reports whether it names a page at all.
//
// The anchor keeps its `^`, because S-5.9 resolves a block reference and a
// heading differently and the sigil is how it tells them apart. Everything else
// the author wrote inside the target is preserved: no case folding, no
// normalisation of separators, no `path.Clean` — an uncleaned target is what lets
// a `..` segment reach resolve and be *refused* with Root's own sentinel instead
// of being silently absorbed here.
func parse(ref Reference) (parsed, bool) {
	// ASCII spaces only, for the reason ext.trimSpace gives: `strings.TrimSpace`
	// also trims Unicode spaces, which would make whether a reference resolves
	// depend on which space an author pasted.
	target := trimASCII(ref.Target)

	out := parsed{
		vaultRoot: strings.HasPrefix(target, rootRefPrefix),
		anchor:    strings.TrimPrefix(trimASCII(ref.Anchor), anchorPrefix),
	}

	target = strings.TrimPrefix(target, rootRefPrefix)
	out.target = strings.TrimSuffix(target, pageExtension)

	// An empty target is legal only with an anchor: `[[#Heading]]` is how
	// Obsidian links inside the page you are on. `[[]]` names nothing and never
	// gets this far — render.go's scanner refuses it — but the type is exported
	// and a caller may build one by hand.
	if out.target == "" && out.anchor == "" {
		return parsed{}, false
	}

	return out, true
}

// Refusal is why a reference was not turned into an address.
//
// A type rather than a bool and an error, because a refusal is not a failure.
// S-3.3's rule that a malformed thing is inert rather than fatal is the same rule
// here: the page still renders, the reference still leaves a record, and the
// reason is something a renderer can *show*. "This link points outside the
// campaign" is a sentence an author can act on, where a bool is not, and an
// `error` would put it on a return value a caller has to remember to inspect.
type Refusal int

const (
	// Allowed is the zero value, and the only one that produces an address.
	Allowed Refusal = iota

	// RefuseOutsideRoot means the reference leaves the content root — Root's own
	// ErrOutsideRoot, reached through Resolver.resolvePath and also caught ahead of
	// time for a reference that names another campaign, where there is no root to
	// ask.
	RefuseOutsideRoot

	// RefuseMalformed means the reference never named a location: empty, absolute
	// where a relative one was required, or carrying a NUL. Root's ErrInvalidRef.
	RefuseMalformed

	// RefuseSymlink means a symlink the policy refuses is on the path. Root's
	// ErrSymlink, which is a vault to go and look at rather than an attempt — which
	// is why it keeps its own answer instead of being reported as an escape.
	RefuseSymlink

	// RefuseCrossCampaignEmbed means the reference asked to embed a page or asset
	// that resolves outside its campaign. ADR 0017: an embed is an inline, so it
	// is refused on the argument that made a cross-campaign link a plain hyperlink.
	RefuseCrossCampaignEmbed

	// RefuseUnavailable means the Resolver has no content root, so it can confine
	// nothing and answers nothing. Root's ErrNoRoot.
	RefuseUnavailable

	// RefuseUnsupported means the reference came from an extension that does not
	// name a path: a `{{statblock:Name}}` operand is a game object for a plugin's
	// registry and a `{{dice:…}}` argument is an expression, and neither is a file
	// in a content root. Phase 8 resolves the first; this layer resolves neither.
	RefuseUnsupported
)

// Err returns the sentinel a Refusal stands for, or nil for Allowed.
//
// The mapping is what lets a caller assert on Root's vocabulary instead of this
// file's: a confinement test written against ErrOutsideRoot keeps working whether
// the answer came from Resolver or from a page read.
func (refusal Refusal) Err() error {
	switch refusal {
	case Allowed:
		return nil
	case RefuseOutsideRoot:
		return ErrOutsideRoot
	case RefuseMalformed:
		return ErrInvalidRef
	case RefuseSymlink:
		return ErrSymlink
	case RefuseCrossCampaignEmbed:
		return ErrCrossCampaignEmbed
	case RefuseUnavailable:
		return ErrNoRoot
	case RefuseUnsupported:
		return fmt.Errorf("%w: %s is not a page reference", ErrInvalidRef, refusal)
	default:
		return fmt.Errorf("%w: refusal %d", ErrInvalidRef, int(refusal))
	}
}

// Reason is the sentence to show a reader, and the `title` of whatever the
// renderer emits for a refused reference.
//
// What the reader sees is the reference's own label, unlinked, with this as its
// title and a marker a stylesheet can hang a warning icon off. Not a 404, and not
// a silent strip: a link an author wrote and cannot follow is a thing they have
// to be able to see, or the page reads as complete when it is not.
func (refusal Refusal) Reason() string {
	switch refusal {
	case Allowed:
		return ""
	case RefuseOutsideRoot:
		return "This link points outside the campaign."
	case RefuseMalformed:
		return "This link is not a path this wiki can read."
	case RefuseSymlink:
		return "This link points through a symbolic link."
	case RefuseCrossCampaignEmbed:
		return "A page from another campaign cannot be embedded."
	case RefuseUnavailable:
		return "This campaign's content is unavailable."
	case RefuseUnsupported:
		return "This is not a page link."
	default:
		return "This link cannot be followed."
	}
}

// String names the refusal, for logs and for the `%v` of a failing assertion.
func (refusal Refusal) String() string {
	switch refusal {
	case Allowed:
		return "allowed"
	case RefuseOutsideRoot:
		return "outside-root"
	case RefuseMalformed:
		return "malformed"
	case RefuseSymlink:
		return "symlink"
	case RefuseCrossCampaignEmbed:
		return "cross-campaign-embed"
	case RefuseUnavailable:
		return "unavailable"
	case RefuseUnsupported:
		return "unsupported"
	default:
		return fmt.Sprintf("refusal(%d)", int(refusal))
	}
}

// LinkKind is what a resolved reference points at, so a renderer knows which
// element to put in the slot render.go left.
//
// Two values rather than one, because `![[map.png]]` and `[[Map]]` are both
// references and must not produce the same element.
type LinkKind int

const (
	// LinkToPage points at a page, and is rendered as an anchor.
	LinkToPage LinkKind = iota

	// LinkToAsset points at a non-markdown file, and is rendered as an image when
	// the reference was an embed and as an anchor when it was a link.
	LinkToAsset
)

// String names the kind, for logs.
func (kind LinkKind) String() string {
	if kind == LinkToAsset {
		return "asset"
	}

	return "page"
}

// Link is one reference's outcome: the address for the element render.go left
// empty, and what to say about it.
//
// Every field is a function of the home campaign's bytes and index alone. Nothing
// here was read from the campaign a cross-campaign reference names, and nothing
// here differs between a GM and a player — which is the property ADR 0017 asks
// for, and the one Resolver's doc comment explains at length.
//
// A caller must escape Label before it reaches a template, exactly as it would any
// other author-supplied string. Href needs no escaping: WikiHref and AssetHref
// escape every path segment themselves, so a href from here is already an escaped
// URL and escaping it again would turn `%20` into `%2520`.
type Link struct {
	// Index is the reference's document-order ordinal — the value render.go put in
	// the element's `data-ref-index`, and the position of this reference in the
	// slice Links returned. A caller attaches the address by matching the two, and
	// never by searching the HTML.
	Index int
	// Href is the escaped, root-relative URL, or empty when Refusal is not
	// Allowed. Never a bare reference and never an unescaped path: an href is the
	// one value on a page that a browser will treat as a URL, so a reference
	// beginning `javascript:` has to arrive as a path segment under the campaign
	// prefix rather than as a scheme.
	Href string
	// Label is the text between the tags, which is `Reference.Label`'s answer —
	// the author's alias or the author's target, and never a title read from the
	// target page.
	//
	// render.go has already written it into the element, so this exists for a
	// caller assembling the anchor itself. It is here, and not recomputed, so there
	// is one answer to "what does this link say" rather than two that could
	// disagree.
	Label string
	// Extension is the syntax that produced the reference, so a caller can tell a
	// wikilink from an embed from a `{{statblock}}` operand without re-reading the
	// slice it was iterating.
	Extension ext.Kind
	// Kind says whether Href addresses a page or an asset.
	Kind LinkKind
	// Broken reports that the home campaign's index holds no page at the address
	// the reference names.
	//
	// Only ever about the home campaign. It is never a report about another
	// campaign, because the render path does not look there: a marker that appeared
	// for a GM and not for a player would be the exact viewer-dependence ADR 0017
	// forbids. A cross-campaign reference therefore carries no marker even when
	// the target campaign does not exist, and a renderer must not infer one from
	// Href's campaign segment.
	Broken bool
	// CrossCampaign reports that the reference named another campaign, so Href
	// addresses that campaign rather than the home one.
	//
	// It says the reference was *written* that way, not that the target was found.
	// It is viewer-independent for the same reason Broken is, and ADR 0017's rule
	// — a plain `<a href>`, the author's text as its label, no lookup and no
	// inlining — is what a renderer does with it.
	CrossCampaign bool
	// Refusal is why there is no Href, and Allowed otherwise.
	Refusal Refusal
}

// Outbound is one reference's record: what was written, and where it went.
//
// The input to the broken-link report and to the index. Unlike the markup, it is
// **viewer-dependent** — the same `[[Other/Foo]]` is resolved for a GM who can see
// `Other` and unresolved for a player who cannot — and that asymmetry is why the
// markup may not encode any of it. A renderer that drew a distinction this record
// draws would produce a body that varies with the viewer's memberships, which is
// the per-user cache S-5.1 rules out.
//
// A reference to a campaign the viewer cannot see produces exactly the record a
// reference to nothing at all produces. Not a refusal, not a hint, and not an
// empty campaign slug: the two answers must be indistinguishable or the record
// becomes an existence oracle with a report in front of it.
type Outbound struct {
	// Index is the reference's document-order ordinal, so a report can point at
	// the element and at a line.
	Index int
	// Reference is the target as the author wrote it, verbatim and un-normalised.
	// A report tells an author what they typed, and `links.md` is the spelling
	// they typed even when the report's answer is `links`.
	Reference string
	// Resolved reports that the reference names a page the viewer may reach.
	Resolved bool
	// TargetCampaign is the slug of the campaign the reference resolves in, and
	// empty exactly when Resolved is false. A cross-campaign resolution names a
	// campaign the caller supplied to Records.
	TargetCampaign string
	// TargetPath is the page's root-relative path, without its `.md`, and empty
	// exactly when Resolved is false.
	TargetPath string
	// Refusal is why the reference was refused, and Allowed when it merely did not
	// resolve. A refusal is not a broken link: the first is a fault in the
	// reference and the second is a page that is not there.
	Refusal Refusal
}

// PageIndex is a campaign's pages, keyed the two ways link resolution asks for
// them: by full root-relative path, and by base name.
//
// Built from `[]domain.Page` — exactly what `store.PagesForCampaign` returns — so
// building one needs no database handle and this package needs no dependency on
// `store`. The caller lists the pages and hands them over; P4 owns when that
// listing happens and owns keeping it current.
//
// Two authorities answer two different questions, and keeping them apart is the
// point. *Existence* is answered here, from a list the indexer debounced and
// size-stable-confirmed (S-4.3): a render that asked the filesystem instead would
// see whatever a sync client had written at that instant, so the same bytes could
// render two different ways, a cached body could not be reproduced from the bytes
// it was cached against, and a broken-link report would flicker with the watcher's
// timing. A file the process cannot read — a synced vault whose mode excludes it —
// is answered here too, where the filesystem would answer it with a permission
// error this layer would have to report as a refusal, making a page's links depend
// on file modes. *Confinement* is answered by `Root.At`, because that is the
// boundary and this package does not get a second opinion; the cost of that is one
// `lstat` per path component for a path-shaped reference, which is the boundary's
// own cost and not something this file can avoid by asking less of it.
//
// Assets are not in it. `store.PagesForCampaign` returns pages, so an asset's
// existence is answered by the caller that serves `/c/{slug}/assets/…` (P6), and
// this layer reports an asset reference's address without claiming to know whether
// the file is there.
//
// Safe for concurrent use: nothing writes to it after NewPageIndex returns.
type PageIndex struct {
	// paths holds every page's path with the markdown extension removed, which is
	// the only spelling a reference carries after parse.
	paths map[string]struct{}
	// byBase maps a base name to the paths that carry it, sorted, so a duplicate
	// resolves to the same page on every run.
	byBase map[string][]string
	// under holds every directory a page sits in, so a reference's first segment
	// can be asked whether the home campaign has one by that name.
	under map[string]struct{}
}

// NewPageIndex indexes pages for link resolution.
//
// The path stored for each page is its root-relative path without the `.md`
// suffix, and the base name is that path's last segment, also without one. A page
// whose path does not end in `.md` is indexed as written, which is harmless: the
// index is a set of names, and a file that is not markdown is not a page and will
// not be in the list.
//
// Two pages with the same base name in different directories are both kept. They
// cannot both be answered, so Lookup returns the lexicographically first, which
// makes the choice deterministic across runs rather than dependent on the order
// the database happened to return rows in — a vault with two `Index.md` files
// resolves the same way on every server.
func NewPageIndex(pages []domain.Page) PageIndex {
	index := PageIndex{
		paths:  make(map[string]struct{}, len(pages)),
		byBase: make(map[string][]string, len(pages)),
		under:  make(map[string]struct{}, len(pages)),
	}

	for at := range pages {
		rel := strings.TrimSuffix(pages[at].Path, pageExtension)
		if rel == "" {
			continue
		}

		index.paths[rel] = struct{}{}

		base := path.Base(rel)
		index.byBase[base] = append(index.byBase[base], rel)

		index.addParents(rel)
	}

	for _, paths := range index.byBase {
		slices.Sort(paths)
	}

	return index
}

// HasPath reports whether the campaign has a page at rel.
//
// rel is taken in a reference's own spelling — no `.md`, no leading `./` — and the
// extension is stripped here rather than at every call site, because the one
// caller with a real on-disk path (`Target.Path()`) and the one with an author's
// text differ on exactly that suffix and nowhere else.
func (index PageIndex) HasPath(rel string) bool {
	_, present := index.paths[strings.TrimSuffix(rel, pageExtension)]

	return present
}

// Lookup returns the campaign's page whose base name is name, and whether one was
// found.
//
// The page's root-relative path without its extension. When more than one page
// carries the name the lexicographically first is returned; see NewPageIndex.
func (index PageIndex) Lookup(name string) (string, bool) {
	found, ok := index.byBase[strings.TrimSuffix(name, pageExtension)]
	if !ok || len(found) == 0 {
		return "", false
	}

	return found[0], true
}

// HasUnder reports whether the campaign has any page in the directory dir.
//
// This is what tells a cross-campaign reference apart from a broken reference into
// this campaign, without asking any other campaign anything. `[[Other/Foo]]` is a
// reference into another campaign unless this campaign has a directory named
// `Other`, in which case it is a reference into this one that names no page — and
// the two readings must be decided the same way for every viewer, so the decision
// may consult the home index and nothing else.
func (index PageIndex) HasUnder(dir string) bool {
	_, present := index.under[dir]

	return present
}

// Len returns how many pages the index holds.
//
// For a report of what was indexed. Not a resolution question: a page whose row has
// not been written yet is simply not answerable, and this is not the place that
// decides what to do about it.
func (index PageIndex) Len() int {
	return len(index.paths)
}

// addParents records every directory rel sits in, so a reference's first segment
// can be asked whether the home campaign has one by that name.
//
// The leaf directory is recorded too, not only its ancestors, because the
// question being asked is "does this campaign have anything at `notes/`", and that
// includes a page written directly there.
func (index PageIndex) addParents(rel string) {
	for dir := path.Dir(rel); dir != dotSegment && dir != rootRefPrefix; dir = path.Dir(dir) {
		index.under[dir] = struct{}{}
	}
}

// VisibleCampaign is one campaign a viewer may see, as the index-time path sees
// it: a slug and the pages that slug's campaign holds.
//
// Deliberately no `*Root`. Link resolution never opens a page — not in this
// campaign and not in another — so handing a Resolver a root would grant a
// capability nothing here calls, and a capability nothing calls is one a later
// change will call. What a broken-link report needs is which page a name resolves
// to, and that is a question about names.
//
// Constructed by the caller from the access the route already resolved. This type
// cannot tell whether a campaign belongs in the slice: it is told, and its
// containment is the guarantee. A campaign the viewer may not see is absent, so
// probing it is not a mistake that can be made here — it requires constructing a
// VisibleCampaign for it, which is a decision made where authorisation is decided.
type VisibleCampaign struct {
	// Slug is the campaign's slug, which is also the first segment of a
	// campaign-qualified reference and the second segment of its href.
	Slug string
	// Index is the campaign's basename and path index.
	Index PageIndex
}

// Resolver turns references into links and into records, for one campaign.
//
// Constructed explicitly, with a root and a list of pages, and holding nothing
// else. There is no constructor overload that takes a requestor, a campaign list
// or a store, and no package-level default — so a Resolver cannot answer a
// question about a campaign it was not built for, and two requests in one process
// cannot share one by accident.
//
// What a caller must supply:
//
//   - The home campaign's *Root. It is the confinement boundary; every path a
//     reference carries is joined against it and interpreted by it.
//   - The home campaign's pages, as `[]domain.Page`. `store.PagesForCampaign`
//     returns that type, so building the resolver is a listing and a call.
//
// What it guarantees, and the guarantee is the design rather than a promise in a
// comment: the render path reads nothing outside the home campaign and consults no
// other campaign's index, so its output cannot differ between two viewers of the
// same bytes however differently they are able to see the rest of the instance.
// Cross-campaign resolution happens in Records, which is told which campaigns to
// consider and is handed no way to find another. And this file has no method that
// returns a page's bytes, so no caller of it can inline one — including across a
// campaign boundary, which is the refusal ADR 0017 requires and which is therefore
// enforced by an absence rather than by a check.
//
// Safe for concurrent use. Every field is written once by NewResolver and read
// afterwards, and nothing inside a PageIndex is written after NewPageIndex.
type Resolver struct {
	homeSlug string
	home     *Root
	index    PageIndex
}

// NewResolver returns a Resolver for one campaign's content root and its pages.
//
// home may be nil, and the answer is then that every path-shaped reference is
// refused with RefuseUnavailable — there is no boundary to confine anything — while
// every bare name still resolves through the index and produces no address, because
// an address needs a slug and the slug comes from the root. A campaign whose content
// root is missing is one an operator has to hear about (S-4.5), and a handler
// rendering its pages still has to render something: a nil dereference in the middle
// of a request is not that.
//
// The slug comes from the root rather than from a parameter, so an href and the log
// line that names the campaign cannot disagree about which campaign they are about.
func NewResolver(home *Root, pages []domain.Page) *Resolver {
	resolver := &Resolver{index: NewPageIndex(pages)}
	if home != nil {
		resolver.home = home
		resolver.homeSlug = home.Slug()
	}

	return resolver
}

// Slug returns the campaign this resolver resolves within.
//
// The same value every href it produces carries. Not a capability and not an
// authorisation input, for the reason Root.Slug is neither: it is here so a log
// line and a link cannot name different campaigns.
func (r *Resolver) Slug() string {
	return r.homeSlug
}

// Index returns the home campaign's page index.
//
// For a caller that wants to resolve a name itself — a search result's "related
// pages", say. It is the index the resolver uses, not a copy, and it describes the
// home campaign only.
func (r *Resolver) Index() PageIndex {
	return r.index
}

// Link turns one reference into the address for the element render.go left empty.
//
// The render path. It consults the home campaign's index and the home campaign's
// root, and nothing else — which is what makes the result identical for a GM, a
// player and an anonymous reader of the same bytes, and what keeps ADR 0017's
// invariant intact without a second cache variant.
//
// origin is where the reference was written; see NewOrigin. A caller iterating
// `Rendered.References` should use Links rather than this, so the ordinals line
// up by construction.
func (r *Resolver) Link(origin Origin, ref Reference) Link {
	link := Link{
		Index:     ref.Index,
		Label:     ref.Label(),
		Extension: ref.Extension,
		Kind:      LinkToPage,
		Refusal:   RefuseUnsupported,
	}

	switch ref.Extension {
	case ext.KindWikilink, ext.KindEmbed:
		// The two that name a path. See below for the embed's extra check.
	default:
		return link
	}

	parsedRef, ok := parse(ref)
	if !ok {
		link.Refusal = RefuseMalformed

		return link
	}

	out := r.resolve(origin, parsedRef)

	link.Refusal = out.refusal
	link.Href = withAnchor(out.href, parsedRef.anchor)
	link.Kind = out.kind
	link.CrossCampaign = out.external

	// Never set for a cross-campaign reference and never set for `[[#Heading]]`,
	// which names this page and so names something that is by definition there. Both
	// exclusions are the same rule: the render path did not look, so it has no
	// answer, and a marker that appeared for a GM and not for a player would be the
	// exact viewer-dependence ADR 0017 forbids. A renderer must not infer one from
	// the campaign segment in Href.
	link.Broken = !out.external && !out.self && out.path == ""

	if out.refusal != Allowed || !out.external || !r.isEmbed(ref) {
		return link
	}

	// An embed of something in another campaign gets no address at all: there is
	// nothing to give it, because the only thing semiplane could put there is the
	// target's content, and ADR 0017 refuses exactly that. Checked here rather than
	// inside resolve so the refusal has one place that owns it.
	link.Refusal = RefuseCrossCampaignEmbed
	link.Href = ""
	link.CrossCampaign = false
	link.Kind = LinkToPage

	return link
}

// Links turns one page's references into their addresses, in the order given.
//
// The entry point C3's `data-ref-index` was designed for: `links[i]` is about
// `refs[i]`, and `links[i].Index` is `refs[i].Index`, so a caller attaches an
// address to an element by position and never by searching the HTML.
//
// One `Link` per reference, always — including for a `{{statblock}}` or
// `{{dice}}` reference, which comes back with `Refusal == RefuseUnsupported` and
// no address. Dropping those would shift every later ordinal and put each href on
// the wrong element, which is the failure a positional seam exists to prevent.
func (r *Resolver) Links(origin Origin, refs []Reference) []Link {
	links := make([]Link, 0, len(refs))

	for _, ref := range refs {
		links = append(links, r.Link(origin, ref))
	}

	return links
}

// Records applies S-5.5's three-step order to a page's references and reports what
// each one resolved to, in the order given.
//
// The index-time path, and the only place another campaign is consulted. visible is
// the set of campaigns the viewer may see — an input, not something derived here —
// and the campaigns this function can probe are exactly the ones in it. A campaign
// that is not in the slice is not consulted, cannot be consulted, and leaves no
// trace in the answer: a reference that would have resolved inside it comes back
// unresolved, identically to a reference that names nothing.
//
// The order is S-5.5's. Steps 1 and 2 — the relative path, then the basename index
// within the home campaign — are delegated to resolve, and so are identically the
// ones the markup used; the two paths cannot disagree because they are the same
// code. Step 3 asks each visible campaign in the order given, so the order a caller
// lists them in is the order that wins a tie, which is the caller's to decide
// because only the caller knows which of two visible campaigns a reader means.
//
// One `Outbound` per reference, including the refused and unsupported ones, for the
// same positional reason Links has.
func (r *Resolver) Records(origin Origin, refs []Reference, visible []VisibleCampaign) []Outbound {
	records := make([]Outbound, 0, len(refs))

	for _, ref := range refs {
		records = append(records, r.record(origin, ref, visible))
	}

	return records
}

// resolved is one reference's answer, before the caller has decided what a
// refusal or an embed means for it.
//
// rel is the path the address is built from and path is the page the reference
// resolved to *in the home campaign*. They differ exactly when nothing answered: an
// unresolved reference still gets an address, pointing at the place it would have
// pointed at had it resolved, so following it produces a 404 inside a campaign the
// reader is already authorised for rather than a link that goes nowhere. path being
// empty is therefore the whole of step 3's precondition.
//
// self says the reference named no page and only an anchor — `[[#Heading]]` — in
// which case it addresses the page it was written on and no resolution at all is
// needed.
type resolved struct {
	href     string
	rel      string
	path     string
	kind     LinkKind
	refusal  Refusal
	external bool
	self     bool
}

// record is Records for one reference.
func (r *Resolver) record(origin Origin, ref Reference, visible []VisibleCampaign) Outbound {
	rec := Outbound{Index: ref.Index, Reference: ref.Target}

	if ref.Extension != ext.KindWikilink && ref.Extension != ext.KindEmbed {
		rec.Refusal = RefuseUnsupported

		return rec
	}

	parsedRef, ok := parse(ref)
	if !ok {
		rec.Refusal = RefuseMalformed

		return rec
	}

	out := r.resolve(origin, parsedRef)
	rec.Refusal = out.refusal

	switch {
	case out.refusal != Allowed:
		// Not resolved, and not recorded as broken: a refused reference is a fault
		// in the reference, and a report listing it beside a page that was merely
		// deleted would be reporting two different problems as one.
		return rec
	case r.isEmbed(ref) && out.external:
		rec.Refusal = RefuseCrossCampaignEmbed

		return rec
	case out.self:
		// `[[#Heading]]`: the page resolves to itself, so the record names this
		// page rather than leaving the reference looking broken.
		rec.Resolved = true
		rec.TargetCampaign = r.homeSlug
		rec.TargetPath = strings.TrimSuffix(origin.Page(), pageExtension)

		return rec
	case out.path != "":
		rec.Resolved = true
		rec.TargetCampaign = r.homeSlug
		rec.TargetPath = out.path

		return rec
	}

	return r.recordOutside(parsedRef, rec, visible)
}

// isEmbed reports whether a reference came from the `![[embed]]` extension.
//
// Read from the extension rather than carried on the reference, because
// render.go's Reference is the shared observation every extension produces and a
// bool added to it would be a second place the two extensions' difference lives.
func (r *Resolver) isEmbed(ref Reference) bool {
	return ref.Extension == ext.KindEmbed
}

// recordOutside is step 3 of S-5.5: the reference found nothing in the home
// campaign, so the visible ones are asked, in the order they were given.
//
// Whether a campaign-qualified reference crosses a boundary is decided here by the
// same rule the render path used and from the same evidence — whether the home
// campaign has a directory by that name — rather than by asking who is visible. A
// rule that consulted the visible set would make the *markup* viewer-dependent,
// which is the failure ADR 0017 exists to prevent.
func (r *Resolver) recordOutside(ref parsed, rec Outbound, visible []VisibleCampaign) Outbound {
	if qualifier, rel, qualified := splitQualifier(ref); qualified {
		if !r.index.HasUnder(qualifier) {
			cleaned, ok := cleanInside(rel)
			if !ok {
				rec.Refusal = RefuseOutsideRoot

				return rec
			}

			return r.recordQualified(rec, visible, qualifier, cleaned)
		}
	}

	name := ref.target
	if !isName(name) {
		name = path.Base(name)
	}

	for _, campaign := range visible {
		if found, ok := campaign.Index.Lookup(name); ok {
			rec.Resolved = true
			rec.TargetCampaign = campaign.Slug
			rec.TargetPath = found

			return rec
		}
	}

	return rec
}

// recordQualified is step 3 for a reference that named its campaign outright.
//
// The only campaigns it can reach are the ones in visible, and it asks only whether
// the *named* campaign is one of them. A hit on any other campaign's index is not
// attempted and cannot be: the loop compares slugs, so a campaign absent from the
// slice is never examined.
func (r *Resolver) recordQualified(
	rec Outbound,
	visible []VisibleCampaign,
	qualifier, rel string,
) Outbound {
	for _, campaign := range visible {
		if campaign.Slug != qualifier || !campaign.Index.HasPath(rel) {
			continue
		}

		rec.Resolved = true
		rec.TargetCampaign = campaign.Slug
		rec.TargetPath = strings.TrimSuffix(rel, pageExtension)

		return rec
	}

	// Unresolved, and left exactly as a reference naming nothing at all would be.
	// The author wrote a campaign this viewer cannot see; the report says the link
	// does not resolve, which is true, and says nothing else about whether the
	// campaign is there.
	return rec
}

// resolve is the home-campaign half of resolution, and the only half the render
// path uses.
//
// Three distinguishable outcomes, in S-5.5's order: a page the home campaign
// holds, an address that names no page, or a refusal. A reference that looks like a
// path is a path and is confined by Root.At; a reference that is a bare name is a
// *name*, and Obsidian resolves a name by searching the vault for a file whose base
// name matches — which is what PageIndex is for. A name is deliberately not joined
// against the referring page's directory, because `[[Goblin]]` two directories down
// is Obsidian's most common link and it means "the Goblin page", not "the Goblin
// page next to this one".
func (r *Resolver) resolve(origin Origin, ref parsed) resolved {
	if ref.target == "" {
		// parse guarantees an empty target arrived with an anchor, so this is
		// `[[#Heading]]` and the only place it can point is the page it is written
		// on. Answered before the root is consulted, because nothing about it is a
		// path into the campaign and conflating the two would put an author's
		// in-page link through the confinement machinery for no reason.
		return resolved{
			href: WikiHref(r.homeSlug, strings.TrimSuffix(origin.Page(), pageExtension)),
			rel:  origin.Page(),
			self: true,
		}
	}

	if isName(ref.target) {
		// Answered with no root consulted at all: a bare name names no location to
		// confine, it names a page to find, and the index is the whole of the answer.
		// A Resolver with no content root still resolves every name in it, because
		// that is the one question a root has no part in.
		return r.resolveName(ref.target)
	}

	return r.resolvePath(origin, ref)
}

// resolveName is step 2: the basename index within the home campaign.
func (r *Resolver) resolveName(name string) resolved {
	if found, ok := r.index.Lookup(name); ok {
		return resolved{href: WikiHref(r.homeSlug, found), rel: found, path: found}
	}

	// Nothing matched, so the address is the name at the top of the campaign. The
	// referring page's directory is deliberately not part of it: two pages linking
	// `[[Goblin]]` before the Goblin page existed would otherwise produce two
	// different broken addresses for one author, and only one of them would be the
	// address the link should have had.
	kind := extensionKind(name)

	return resolved{href: hrefFor(kind, r.homeSlug, name), rel: name, kind: kind}
}

// resolvePath is step 1: a path, confined and then looked up.
func (r *Resolver) resolvePath(origin Origin, ref parsed) resolved {
	if r.home == nil {
		return resolved{kind: LinkToPage, refusal: RefuseUnavailable}
	}

	base := origin.Dir()
	if ref.vaultRoot {
		base = RootDir()
	}

	target, err := r.home.At(path.Join(base.String(), ref.target))
	if err != nil {
		return resolved{kind: LinkToPage, refusal: refusalFor(err)}
	}

	rel := target.Path()

	if r.index.HasPath(rel) {
		return resolved{href: WikiHref(r.homeSlug, rel), rel: rel, path: rel}
	}

	if qualifier, rest, qualified := splitQualifier(ref); qualified {
		if !r.index.HasUnder(qualifier) {
			// Cross-campaign, and the address is built from the author's text alone:
			// the campaign is not looked up, its root is not opened and its index is
			// not read, so the address is the same string for every viewer whether or
			// not that campaign exists. That is what makes the linking page's markup
			// byte-identical across viewers, and it means a reader who cannot see the
			// target clicks and is refused by the gate — which discloses nothing the
			// author did not already write down. See ADR 0017.
			inside, ok := cleanInside(rest)
			if !ok {
				return resolved{kind: LinkToPage, refusal: RefuseOutsideRoot}
			}

			return resolved{
				href:     hrefFor(extensionKind(inside), qualifier, inside),
				rel:      inside,
				kind:     extensionKind(inside),
				external: true,
			}
		}
	}

	// A reference into this campaign that names no page. The address is still a real
	// one under this campaign's prefix, so following it is a 404 for a reader who is
	// already authorised for the campaign — an answer about a page, never an answer
	// about the filesystem.
	kind := extensionKind(rel)

	return resolved{href: hrefFor(kind, r.homeSlug, rel), rel: rel, kind: kind}
}

// isName reports whether a reference target is a bare name rather than a path.
//
// The whole of Obsidian's branch, and the reason a bare name is looked up in the
// basename index instead of being joined against the referring page's directory. A
// target with no separator cannot name a location relative to anything, so the only
// question it can be asking is "which page is called this".
func isName(target string) bool {
	return target != "" && !strings.Contains(target, pathSeparator)
}

// splitQualifier splits a vault-root reference into its campaign and the path
// inside it, and reports whether it named a campaign at all.
//
// `[[/Other/Notes/Foo]]` is a reference into a campaign called `Other`, and that is
// the only way semiplane spells one. The leading slash is Obsidian's own spelling
// for a vault-absolute link, and the vault root here is this campaign — so a
// vault-absolute reference whose first segment is not a directory this campaign has
// is a reference into another one. The shape then matches the URL scheme's own
// (`/c/{slug}/wiki/{path}`), so a reference and the address it produces read the
// same way.
//
// **The leading slash is required, and that is the whole point.** A reference
// written without it is relative to the referring page, and the relative reading
// always wins — so `[[Other/Foo]]` inside `notes/Page.md` is a reference to
// `notes/Other/Foo`, exactly as Obsidian reads it, and a campaign can only be named
// from a page at the top or by saying so. The alternative — reading the first
// segment of a relative reference as a campaign when no home directory matches —
// makes `[[deep/../Goblin]]` depend on whether a `deep/` directory happens to
// exist, and a link whose meaning depends on the tree's shape is a link that
// changes meaning when a folder is added.
//
// A leading `.` or `..` segment is not a qualifier either: those are relative
// directions, and a reference that begins one has not named a campaign. (Such a
// reference is refused by `Root.At` before it reaches here.)
func splitQualifier(ref parsed) (qualifier, rest string, ok bool) {
	if !ref.vaultRoot {
		return "", "", false
	}

	first, remainder, found := strings.Cut(ref.target, pathSeparator)
	if !found || remainder == "" {
		return "", "", false
	}

	if first == "" || first == dotSegment || first == parentSegment {
		return "", "", false
	}

	return first, remainder, true
}

// cleanInside normalises the path part of a campaign-qualified reference, and
// reports whether it names anything inside that campaign.
//
// The one place a `..` is interpreted lexically rather than by `Root.At`, and it has
// to be: `[[Other/../etc/passwd]]` is a path in a campaign this process has no root
// for, so there is nothing to ask — and `path.Clean` on its own would quietly turn
// it into `/etc/passwd`, a path with no campaign in it at all. `Root.At` answers the
// same question for a reference into the home campaign, at the boundary; this is the
// case where there is no boundary to reach.
//
// A `..` that *stays* inside is fine, as it is everywhere else:
// `[[Other/notes/../Page]]` is a page called `Page`.
func cleanInside(rel string) (string, bool) {
	cleaned := path.Clean(rel)

	if cleaned == dotSegment || cleaned == parentSegment ||
		strings.HasPrefix(cleaned, parentSegment+pathSeparator) {
		return "", false
	}

	return cleaned, true
}

// extensionKind says whether a target the index did not answer names a file rather
// than a page.
//
// parse has stripped `.md`, so whatever extension is left on a target is the only
// one it can carry — which makes "the base name has an extension" the whole test.
// `Goblin` has none and is a page; `map.png` has one and is a file. A page whose
// name genuinely contains a dot is answered by the index first, so it is only ever
// misread when it is *missing*, where the two costs are a `/wiki/` address and an
// `/assets/` one for a link that 404s either way.
func extensionKind(target string) LinkKind {
	if path.Ext(path.Base(target)) == "" {
		return LinkToPage
	}

	return LinkToAsset
}

// hrefFor builds the address for a target of the given kind.
//
// A link and an embed of the same target produce the same address and differ only
// in the element a renderer puts in the slot, which is what LinkKind is for.
// Routing an asset through `/wiki/` would serve a markdown page from the wrong
// route, and routing a page through `/assets/` would 404 a link the author wrote.
func hrefFor(kind LinkKind, slug, rel string) string {
	if kind == LinkToAsset {
		return AssetHref(slug, rel)
	}

	return WikiHref(slug, rel)
}

// withAnchor appends an escaped fragment to an address, and returns the address
// unchanged when there is no anchor or nothing to append to.
//
// A refused reference has no address and no fragment: the reason it was refused is
// the reason its href would have been wrong, and attaching the anchor to nothing
// would produce `/c/slug/wiki/#Heading` — the campaign index, which is a page that
// exists.
func withAnchor(href, anchor string) string {
	if href == "" || anchor == "" {
		return href
	}

	return href + anchorPrefix + url.PathEscape(anchor)
}

// trimASCII removes leading and trailing spaces and tabs.
//
// ASCII only, and deliberately not `strings.TrimSpace`: that also trims Unicode
// spaces, which would make whether a reference resolves depend on which space an
// author pasted — a link that works for one reader and 404s for another who copied
// it through a tool that substitutes U+00A0.
func trimASCII(value string) string {
	return strings.Trim(value, " \t")
}

// refusalFor maps a confinement answer onto this file's vocabulary.
//
// Only Root's refusals can arrive — `Root.At` does not stat, so it cannot say
// ErrNotExist — and each keeps its own meaning rather than collapsing into one "no
// link": an escape is an attempt, a malformed reference is a typo in the author's
// file, and a symlink is a vault to go and look at. The default is
// RefuseUnavailable, the direction that refuses to answer.
func refusalFor(err error) Refusal {
	switch {
	case errors.Is(err, ErrOutsideRoot):
		return RefuseOutsideRoot
	case errors.Is(err, ErrSymlink):
		return RefuseSymlink
	case errors.Is(err, ErrInvalidRef):
		return RefuseMalformed
	case errors.Is(err, ErrNotExist), errors.Is(err, ErrNoRoot):
		return RefuseUnavailable
	default:
		return RefuseUnavailable
	}
}

// WikiHref builds the wiki URL for a page in a campaign.
//
// Per-segment escaping, and the whole point is the segments: `url.PathEscape`
// escapes `/` as well as everything else, so escaping `notes/Some Page` in one call
// produces `notes%2FSome%20Page` — a single segment where the URL scheme expects
// several, and therefore a path no route matches. It reads like it worked, because
// the result is still a well-formed URL. So each segment is escaped on its own and
// the separators are written back unescaped, and a `..` segment is escaped even
// though the reference check refuses one long before here: this function is
// exported, and a browser that normalises `/a/../../b` out of an href would climb
// out of the prefix this file exists to keep it inside.
//
// Empty when slug or rel is empty. A reference that names nothing has no address,
// and inventing one — `/c//wiki/` or `/c/slug/wiki/` — produces a link to the
// campaign index that looks exactly like a link to a page.
func WikiHref(slug, rel string) string {
	return campaignHref(slug, wikiRouteSegment, rel)
}

// AssetHref builds the asset URL for a file in a campaign.
//
// The same construction as WikiHref and the same reasoning; the separate name is
// because the two route segments are different strings, and a caller that spelled
// the wrong one would serve a markdown page from the asset route or the reverse.
func AssetHref(slug, rel string) string {
	return campaignHref(slug, assetRouteSegment, rel)
}

// campaignHref assembles `/c/{slug}/{section}/{escaped path}`.
func campaignHref(slug, section, rel string) string {
	if slug == "" || rel == "" || rel == dotSegment {
		return ""
	}

	var href strings.Builder

	href.WriteByte('/')
	href.WriteString(campaignRouteSegment)
	href.WriteByte('/')
	href.WriteString(url.PathEscape(slug))
	href.WriteByte('/')
	href.WriteString(section)

	for segment := range strings.SplitSeq(rel, pathSeparator) {
		href.WriteByte('/')

		// Written as `%2E%2E` rather than handed to PathEscape, which leaves `..`
		// alone on the grounds that it is not a special character *in a name*. In a
		// URL it is.
		if segment == parentSegment {
			href.WriteString("%2E%2E")

			continue
		}

		href.WriteString(url.PathEscape(segment))
	}

	return href.String()
}
