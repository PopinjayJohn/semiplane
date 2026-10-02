package wiki

import (
	"cmp"
	"context"
	"log/slog"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/web/components/chrome"
)

// The campaign navigation: the model `components.CampaignShell` fills its left
// slot with, and the three questions it answers.
//
// # Why this is a file and not a block in writePage
//
// `writePage` is the request path's security surface, and the navigation is
// three permission decisions wearing a list's clothes: which destinations a
// reader may see at all (UI §4.3's "absent, not disabled"), which pages of the
// campaign are listed, and what each row is called. Each of those is the kind of
// question that is easy to get wrong while reading a template and hard to see
// once it is inline, and each has a test beside it in this package.
//
// # The destinations, and where their addresses come from
//
// S-9 fixes the URL scheme and §4.4's table fixes which route is which, so
// nothing here is a guess about a shape — but `chrome.NavView` takes the
// addresses rather than deriving them, and that is deliberate on the chrome's
// side: the routes behind them belong to other work items and other phases, and a
// shell that derived them would be deriving three URL schemes in one place. So
// the route supplies them, and this file is where "is this destination shown at
// all" is answered:
//
//   - **Table** — one address, never a list (UI §1.2, §4.3): a campaign has at
//     most one live tabletop, so there is nothing to enumerate. Present for a
//     member of a campaign whose gameplay system is installed. The membership
//     half is `Tier.CanPlay` and not `Tier.CanRead`, because S-8.1 says games
//     always require membership and no amount of `public` grants play; the
//     system half is `campaign.SystemID`, which is what UI §4.7's `system_id` row
//     is about. A member of a campaign with no system gets no Table link rather
//     than one that answers 404.
//   - **Search** — for every reader who reached this route. It reads the same
//     tier of content the reader may already read here and S-8.2 puts the
//     visibility join in the query rather than in the caller, so offering it to
//     every reader is safe, and omitting it would be a smaller promise than the
//     product makes.
//   - **Admin** — a GM only. S-6.5 makes content editing GM-only and UI §4.3 wants
//     the whole GM surface *absent* for a player, so the check is here rather
//     than in the template: a template handed a permission flag is a template
//     that eventually renders the thing it was told not to.
//
// # What a row is called
//
// The page's own base name, and never its front matter's `title:`. That is
// `pageName`'s existing refusal (see it) extended from the `<h1>` to every row of
// the tree, for the same reason plus one: the index stores the front-matter title
// **unredacted** — `content.Indexer` writes `doc.FrontMatter.Title` into
// `pages.title`, and S-5.11's guarantee is about `body_plain` — so a title in the
// navigation would put text a non-GM response must not carry into a non-GM
// response. One page, one name, everywhere it is referred to.

// The three campaign routes the navigation addresses (S-9, UI §4.4).
//
// Constants rather than literals because a misspelling produces a destination
// that answers 404 and reads to a reader as a broken product rather than as a
// defect — the same argument `content`'s own route segments make.
const (
	playRouteSegment     = "play"
	searchRouteSegment   = "search"
	settingsRouteSegment = "settings"

	// campaignRoutePrefix and campaignPathSeparator assemble `/c/{slug}/{segment}`.
	// Written out rather than borrowed from `content`, whose route segments are
	// unexported and whose two href builders both take a page path this route does
	// not have. A second spelling of the campaign prefix in a second package is a
	// smaller thing than a wrong one: `chrome.CampaignRef.href()` spells it the
	// same way, and the two are read together.
	campaignRoutePrefix   = "c"
	campaignPathSeparator = "/"
)

// navTreeDepth is how many folder levels the navigation's page tree descends.
//
// A cap because UI §4.3 names one: the navigation is nested lists and every row
// is a tab stop, so the cost of a large vault is a tab sequence, and the record's
// two answers are the skip link and a capped depth. The record names no number.
//
// Three, and a depth rather than a row budget because nesting is what makes the
// navigation unusable at the tiers §3.5 calls compact-short: at 3.5rem the rail
// shows icons, and an icon whose meaning is "three more icons" is a row a reader
// has to expand to learn whether the page they want is a sibling or a child.
// Three levels is where the current page's siblings are still discoverable
// without expanding anything.
//
// What lies past the cap is **omitted from the tree**, not flattened into it: a
// row whose real location is four folders down and whose label reads like a
// sibling of the page beside it is a worse navigation than an absent one, and
// nothing is unreachable — the centre slot renders any page by URL and §7.5's
// search reaches every indexed page in the campaign.
const navTreeDepth = 3

// CampaignLister lists the campaigns a reader is a member of, which is what the
// navigation's Campaigns section switches between.
//
// Narrower than `store.Store` for the reason `RootLookup` is narrower than
// `content.Registry`: a handler that could list every campaign on the instance is
// a handler that could be asked about a campaign the reader has no business
// knowing exists. The query behind it is membership-scoped, so its rows are the
// reader's own — S-8.2's join is a property of *search*, not of a membership
// list, and a membership list cannot return a campaign the reader is not in.
type CampaignLister interface {
	CampaignsForUser(ctx context.Context, userID int64) ([]domain.Campaign, error)
}

// navigation builds the campaign navigation for one request.
//
// Every field is derived from the access the gate resolved or from the campaign's
// own index. Nothing here reads a cookie, a query parameter or a header, for the
// reason `includeSecrets` does not: each of those is a way for a reader to ask
// for a destination or a page they have no business being offered.
//
// `pages` is the campaign's listing, passed in rather than fetched because the
// response already read it to resolve this page's references (see `addresses`).
// One query per response, and the two uses of it — this page's link addresses and
// the tree — cannot disagree because there is only one answer.
func (h *Handler) navigation(
	ctx context.Context,
	access campaigns.Access,
	pages []domain.Page,
) chrome.NavView {
	slug := access.Campaign.Slug

	return chrome.NavView{
		Campaigns: h.readerCampaigns(ctx),
		Wiki:      wikiTree(pages, slug),
		TableHref: tableHref(access),
		// Search for every reader who reached this route. §4.3's "absent, not
		// disabled" is about affordances a reader may not *use*, not about one
		// every reader of the wiki may.
		SearchHref: campaignRouteHref(slug, searchRouteSegment),
		AdminHref:  adminHref(access),
	}
}

// tableHref is the live tabletop's address, or empty when this reader or this
// campaign has none.
//
// Empty means the destination is absent rather than present and inert, which is
// UI §4.3's rule and §7.2's reason to prefer absence: a control a reader can see
// and cannot use is a focus stop that lies.
func tableHref(access campaigns.Access) string {
	if !access.Tier.CanPlay() || access.Campaign.SystemID == "" {
		return ""
	}

	return campaignRouteHref(access.Campaign.Slug, playRouteSegment)
}

// adminHref is the campaign's settings address, and empty for anybody who is not
// its GM.
func adminHref(access campaigns.Access) string {
	if !access.Tier.CanEdit() {
		return ""
	}

	return campaignRouteHref(access.Campaign.Slug, settingsRouteSegment)
}

// readerCampaigns is the Campaigns section's rows: the reader's own campaigns.
//
// Empty for an anonymous reader, which is the normal case on a public campaign,
// and the section is then absent rather than a heading above no links — the same
// rule the whole navigation follows before a campaign exists.
//
// A failure is a missing section and not a failed page. The reader came for a
// page, the page rendered, and a switcher that cannot be listed is a smaller
// loss than a 500 over a query that has nothing to do with the document. It is
// logged, because a database that cannot answer a membership query is a fault an
// operator must see — the same reasoning `campaigns.membershipFor` gives.
func (h *Handler) readerCampaigns(ctx context.Context) []chrome.CampaignRef {
	// From the requestor rather than from `access.Membership`: that is the
	// membership of *this* campaign, and the section switches between all of them.
	// The requestor is the only thing that names a reader's other campaigns, and
	// the gate has already resolved it for this request.
	//
	// `UserID <= 0` is its own condition rather than part of the authentication
	// check, because `domain.Requestor` is a struct and a caller can build one
	// with `Authenticated: true` and no user — the two are separate facts and the
	// query is about the second one.
	requestor := campaigns.Requestor(ctx)
	if h.Campaigns == nil || !requestor.Authenticated || requestor.UserID <= 0 {
		return nil
	}

	listed, err := h.Campaigns.CampaignsForUser(ctx, requestor.UserID)
	if err != nil {
		h.log(ctx, slog.LevelWarn, "wiki.nav_campaigns_unavailable",
			slog.Int64("user_id", requestor.UserID),
			slog.String("error", err.Error()),
		)

		return nil
	}

	refs := make([]chrome.CampaignRef, 0, len(listed))
	for at := range listed {
		refs = append(refs, chrome.CampaignRef{
			Name: listed[at].Name,
			Slug: listed[at].Slug,
		})
	}

	return refs
}

// campaignRouteHref addresses one of a campaign's own routes: `/c/{slug}/{segment}`.
//
// A slug is escaped for the same reason it is escaped in `content.campaignHref`:
// `domain.ValidateSlug` already makes one a safe path segment, and an href
// builder that relied on its input having been validated somewhere else makes
// that somebody else's invariant.
//
// Empty for an empty slug, so a request that never passed the access gate cannot
// produce a link pointing at the campaign index that reads as a link to a page.
func campaignRouteHref(slug, segment string) string {
	if slug == "" {
		return ""
	}

	return campaignPathSeparator + campaignRoutePrefix + campaignPathSeparator +
		url.PathEscape(slug) + campaignPathSeparator + segment
}

// wikiTree builds the navigation's page tree from a campaign's listing.
//
// Nil when there is nothing to list, and nil rather than an empty root because
// `chrome.NavView.hasWiki` is what decides between "no Wiki section" and a
// section above no links: a non-nil root with no children is a campaign with no
// pages, and §4.7 puts "This campaign has no pages yet" in the *centre*, where
// the reader is looking for it.
//
// A `treeNode` rather than a `chrome.PageNode` because the chrome's value type
// cannot be built incrementally: a folder is appended to its parent by value, so
// a page added to it afterwards would be appended to a copy the parent does not
// hold. Two types make the two-phase build obvious — assemble, then convert.
//
// Deterministic throughout. The listing is sorted by path before anything is
// inserted, so folders appear in a fixed order rather than in the order the
// index happened to return them, and every level is sorted again on the way out.
// Rule code must be deterministic and map iteration is the usual way it stops
// being so; the map below is only ever *looked up*, never walked.
func wikiTree(pages []domain.Page, slug string) *chrome.PageNode {
	if slug == "" || len(pages) == 0 {
		return nil
	}

	ordered := slices.SortedFunc(slices.Values(pages), func(a, b domain.Page) int {
		return strings.Compare(a.Path, b.Path)
	})

	root := &treeNode{}

	// Keyed by a folder's root-relative path, with no extension.
	folders := map[string]*treeNode{}

	// Indexed rather than ranged by value: `domain.Page` is six fields and a
	// timestamp, so a `for _, page := range` copies 136 bytes per iteration and
	// `gocritic` is right that a tree walk over a whole vault is a lot of copying
	// for two fields of it.
	for at := range ordered {
		page := &ordered[at]

		dir := pageDir(page.Path)
		if treeDepth(dir) > navTreeDepth {
			continue
		}

		folder := folderAt(root, folders, dir, slug)
		folder.children = append(folder.children, &treeNode{
			title: pageName(page.Path),
			href:  content.WikiHref(slug, strings.TrimSuffix(page.Path, pageExtension)),
		})
	}

	return &chrome.PageNode{Children: chromeNodes(root.children)}
}

// treeNode is one row of the tree while it is being assembled.
//
// Whether a node is a folder or a page is decided by whether `children` is empty,
// which is exactly the test `chrome.navItems` applies — so this type and the
// component that renders it cannot disagree about which a node is.
type treeNode struct {
	// title is what the row reads and href is where it goes. Both are empty only
	// on the root, which is never emitted.
	title string
	href  string
	// children holds folders and pages together and unsorted. The sort happens on
	// the way out, which is what makes it safe for a page to be appended to a
	// folder after that folder was appended to its own parent.
	children []*treeNode
}

// folderAt walks to the node for a root-relative directory, creating the folders
// on the way.
//
// Creates rather than looks up because a vault's directories are implied by the
// pages in them: nothing writes a directory row, so a listing of a campaign
// holding `notes/Goblin.md` and `notes/Orc.md` and no `notes` is the normal
// shape. The map is the same tree, indexed, so a folder reached twice is one
// folder.
func folderAt(
	root *treeNode,
	folders map[string]*treeNode,
	dir, slug string,
) *treeNode {
	if dir == "" {
		return root
	}

	segments := strings.Split(dir, campaignPathSeparator)
	parent := root

	for at := range segments {
		key := strings.Join(segments[:at+1], campaignPathSeparator)

		child, seen := folders[key]
		if !seen {
			child = &treeNode{title: segments[at], href: content.WikiHref(slug, key)}
			folders[key] = child

			parent.children = append(parent.children, child)
		}

		parent = child
	}

	return parent
}

// chromeNodes converts one level of the assembled tree, sorting each level first.
//
// Sorted by label rather than folders-first-then-labels: a wiki is read
// alphabetically and a reader looking for the Kobolds page is looking for a name,
// not for a category. The comparison is total — case-folded label, then the exact
// label, then the address — because two rows can share a label (a page and a
// folder of the same name, which a vault allows), and a sort that treated those
// as equal would leave the rendered order dependent on the index's own ordering.
func chromeNodes(nodes []*treeNode) []chrome.PageNode {
	slices.SortStableFunc(nodes, func(first, second *treeNode) int {
		if byLabel := cmp.Compare(
			strings.ToLower(first.title),
			strings.ToLower(second.title),
		); byLabel != 0 {
			return byLabel
		}

		if byCase := cmp.Compare(first.title, second.title); byCase != 0 {
			return byCase
		}

		return cmp.Compare(first.href, second.href)
	})

	converted := make([]chrome.PageNode, 0, len(nodes))
	for _, node := range nodes {
		converted = append(converted, chrome.PageNode{
			Title:    node.title,
			Href:     node.href,
			Children: chromeNodes(node.children),
		})
	}

	return converted
}

// pageDir returns the root-relative directory a page sits in, and empty for one
// at the root of the campaign's content.
//
// `path` and not `filepath`: the listing's separator is `/` on every platform,
// because the path came out of a vault and `domain.Page.Path` is defined as
// slash-separated. A Windows host must not turn the split into a two-word
// directory. `path.Dir`'s `"."` for a top-level page is turned back into empty,
// because the tree's root is the empty directory and not a row called `.`.
func pageDir(pagePath string) string {
	dir := path.Dir(pagePath)
	if dir == "." {
		return ""
	}

	return dir
}

// treeDepth returns how many folder levels a root-relative directory sits under.
//
// Counted rather than measured by walking a built tree, because the cap is
// applied while inserting: a page that is too deep is never added, so nothing
// downstream has to notice it and take it out again.
func treeDepth(dir string) int {
	if dir == "" {
		return 0
	}

	return strings.Count(dir, campaignPathSeparator) + 1
}
