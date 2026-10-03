package play

// The table's document: `GET /c/{slug}/play` as a browser receives it.
//
// # Why this route renders a document at all
//
// Until this work item `/play` *was* the WebSocket upgrade, so S-9's own URL
// scheme was unreachable: a player who opened `/c/{slug}/play` in a browser got
// a 400 from a handshake, and the spec's "VTT (members only)" row described a
// route that did not exist. The document is therefore not an addition to the
// socket — it is the other half of the pair the record fixes, and the socket
// moved to `/ws` so that both halves could be true at once.
//
// # The document is this route's, and `components.CampaignShell` is not used
//
// `components.CampaignShell` is the wiki's shell and three of its decisions are
// wrong here rather than merely inconvenient: its skip-link block is fixed at
// three where UI §7.2 gives `/play` a fourth, its centre is `shellMain` where
// this route needs the measure override and the map surface, and it renders
// nothing between the rail and the footer where the action bar has to sit as a
// sibling in the grid. `internal/httpapi/plugins/document.go` is the precedent
// for a route that composes the chrome itself: the *landmarks* are still
// `chrome`'s, and only the document around them belongs to this file. See
// `internal/web/components/play/document.templ`, which argues the same thing
// from the template's side.
//
// # Every document field is optional, and that is the point
//
// `Instance`, `SignOutHref`, `StatusHref`, `Campaigns`, `Systems` and `Snapshot`
// are each a thing a composition root may not have wired yet, and each renders
// the same way it does on the wiki route when it is absent: no instance name
// falls back to the product's, a nil lister omits the Campaigns section, a
// campaign whose system this build cannot resolve renders §4.7's honest empty
// state in the die sheet, and a nil `Snapshot` renders the token list's own
// empty state. A nil dereference on the request path would be a worse answer
// than a shorter navigation, which is the argument `wiki.Handler.Campaigns`
// already makes for the same field.
//
// # What this file deliberately does not do
//
// It re-checks nothing. `Mount` puts `campaigns.RequirePlay` in front of both
// routes, so by the time `serveDocument` runs the campaign is resolved, the tier
// is known and the membership exists — ADR 0024's "a gate the route mounts,
// never a check inside a handler". The one *permission-shaped* decision here is
// which placements a reader may see, and that is not access to the campaign: it
// is filtering one response's contents by the role the gate already resolved,
// for the reason UI §4.5 states — a hidden panel is one CSS rule away from
// visible, so what a player must not see is absent from their bytes.

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/web/components"
	"github.com/semiplane/semiplane/internal/web/components/chat"
	"github.com/semiplane/semiplane/internal/web/components/chrome"
	"github.com/semiplane/semiplane/internal/web/components/live"
	webplay "github.com/semiplane/semiplane/internal/web/components/play"
)

// The document's own constants: the content type, the cache policy, the title
// separator and the address scheme.
//
// The cache pair is not the wiki's pair and the difference is deliberate. The
// wiki derives `Cache-Control` from a render key because a page's bytes depend
// on four inputs the key records; this document depends on who is asking — the
// banner carries their name, the navigation carries their memberships and the
// token list carries what their role may see — so the only correct policy is
// `private, no-store`, which is also what every gate response in this project
// sets. `Vary: Cookie` is the additive half of the same statement: a shared
// cache that keyed only on the URL would hand one reader another reader's
// banner, and ADR 0035's corrected text says exactly this about the one route
// that already emits it.
const (
	documentContentType = "text/html; charset=utf-8"
	documentCache       = "private, no-store"
	documentVary        = "Cookie"

	// titleSeparator joins the title's three parts, and titlePage is the first.
	//
	// **Both are `components`' own values, spelled again here**, and the
	// duplication is named rather than hidden: `documentTitleForCampaign` and
	// `titleSeparator` are unexported in a package this route does not own, and
	// exporting them would make the shell's title formatter a public API whose
	// only second caller is a route that renders its own document. `chrome`'s
	// `productName` is unexported for the same reason, so the instance-name
	// fallback below is a third copy of it. Three copies of two constants is a
	// smaller cost than three ways for a title to read differently on two routes,
	// and `TestTheTitleIsThreePartsInTheRecordsForm` asserts the shape this route
	// actually serves.
	titleSeparator  = " — "
	titlePage       = "Table"
	playProductName = "semiplane"

	// The campaign route segments, and the prefix they hang off.
	//
	// `wiki` spells the same four in its own `nav.go` and `chrome.CampaignRef.href`
	// spells the prefix a third time. A second spelling in a second package is a
	// smaller thing than a wrong one, and the alternative — an exported constant
	// in a package neither of these imports — would move the URL scheme into a
	// package whose subject is content.
	campaignRoutePrefix = "c"
	pathSeparator       = "/"

	segmentPlay     = "play"
	segmentSearch   = "search"
	segmentSettings = "settings"
)

// CampaignLister lists the campaigns a reader is a member of, which is what the
// navigation's Campaigns section switches between.
//
// The same shape, and the same narrowness, as `wiki.CampaignLister`: a handler
// that could list every campaign on the instance could be asked about a campaign
// the reader has no business knowing exists, and the query behind this one is
// membership-scoped so its rows are their own answer to that.
type CampaignLister interface {
	CampaignsForUser(ctx context.Context, userID int64) ([]domain.Campaign, error)
}

// Systems is what this route asks about a campaign's gameplay system.
//
// A function type and not the gameplay registry, for the reason
// `plugins.Systems` gives: the answer lives in `campaigns.system_id`, the store
// is not this package's to read, and the composition root is the only place that
// can join them. A nil or an error is not fatal — the die sheet renders §4.7's
// empty state, which is S-10.6's first row reached from the UI side.
type Systems func(ctx context.Context, campaignID int64) (rules.System, error)

// SnapshotFunc reads a campaign's live placements for the first render.
//
// **A function rather than a `*realtime.Hub` method because the hub has no such
// method.** `Hub` exposes `Stats`, `Join`, `Publish`, `Apply`, `Dispatch` and
// `Close`, and its `states` registry is unexported; `realtime.Registry.Get`
// answers only for states that are already open, so a route that called it would
// render an empty token list for every campaign no client has connected to yet —
// which is precisely the moment a document matters, because it is the render
// before the socket delivers the snapshot.
//
// The composition root is where the two can be joined (it holds the registry and
// the campaign ids), so this is a seam with one obvious implementation rather
// than an abstraction over one. `false` is "no state to show", not a failure: an
// empty token list is a designed surface (UI §4.7).
type SnapshotFunc func(ctx context.Context, campaignID int64) (realtime.Document, bool)

// serveDocument answers `GET /c/{slug}/play` with the table's document.
//
// The status is written before the document renders, and the split mirrors
// `wiki.writePage`'s reasoning: the view models are assembled first, the headers
// are then constant, and a failure while writing the body has already told the
// reader more than an error page would have — so it is logged with an event name
// an operator can match rather than answered twice.
func (h *Handler) serveDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := campaigns.AccessFrom(ctx)

	view, rail := h.documentView(ctx, access)

	header := w.Header()
	header.Set("Content-Type", documentContentType)
	header.Set("Cache-Control", documentCache)
	header.Set("Vary", documentVary)

	w.WriteHeader(http.StatusOK)

	//nolint:contextcheck // See `documentView`: templ's constructors take no
	// context and the only place one enters this route is the Render below.
	if err := webplay.Document(view, rail).Render(ctx, w); err != nil {
		h.log(ctx, slog.LevelError, "play.document_render_failed",
			slog.String("campaign", access.Campaign.Slug),
			slog.String("error", err.Error()),
		)
	}
}

// The two connection addresses the sidebar reads, and why they are composed here
// rather than read from the request.
//
// `r.URL` would be the obvious source and the wrong one: the document is served
// behind `campaigns.Resolve`, whose own pattern is `/c/{slug}/`, and a request
// that arrived at `/c/greyhaven/play` says nothing about where the socket is. The
// slug is the fact, it has been through `domain.ValidateSlug`, and both addresses
// are **derived from it rather than echoed from a header** — which is the same
// reason architecture §9 puts the slug in the path: it partitions every cache key
// by visibility.
//
// Two connections and not four (architecture §7's ~6 ceiling): the WebSocket
// carries structured state to the canvas, and the event stream carries rendered
// fragments to the sidebar. `TestTheDocumentOpensExactlyOneOfEach` counts both in
// the rendered document rather than trusting this comment.
func socketHref(slug string) string { return "/c/" + slug + "/ws" }

func eventsHref(slug string) string { return "/c/" + slug + "/events" }

// degradedNotices is what §4.7's degraded conditions would be read from.
//
// **Empty, and that is a stated gap rather than a stub.** The notices C3 renders
// are the degraded watcher, an exhausted secret reconciliation, and a stream the
// server cannot serve whole. Two of those three are phase 10's work and do not
// exist yet; the third is `events.Handler`'s business and has no accessor on this
// route.
//
// What this route *can* see is nothing: `campaigns.Access` carries the campaign,
// the membership and the tier, and a degraded watcher is not among them. Inventing
// a signal here would be a notice that is never true, and a notice region that
// renders an empty state forever is the honest answer — it is exactly what a
// healthy table looks like, which is how `ChromeView.Notices` documents its zero
// value.
//
// The socket state beside it is the notice a reader actually gets today, and it
// is client-owned: both sentences are rendered and the client reveals one.
func (h *Handler) degradedNotices(campaigns.Access) []live.NoticeView {
	return nil
}

// messages is the chat history the document renders before any stream opens.
//
// **Nil, and this is the phase's one real gap on this route.** §7.5 requires the
// chat log to carry its scrollback in the document while announcing none of it —
// "a user joining mid-table would otherwise hear the whole table read aloud" — so
// the intended shape is history-in-the-DOM, silence-on-connect.
//
// `realtime.Hub` exposes no history accessor: `Join`, `Publish`, `Apply`,
// `Dispatch` and `Stats` are its whole surface, and the campaign state it holds is
// placements and initiative rather than a chat ring. So there is nothing to read
// here, and the panel renders its empty state while lines arrive over the stream.
//
// The consequence is real and worth stating rather than hiding: **a reload loses
// the chat scrollback**, because the only writer is the stream. Carrying it needs
// either a ring on the hub or a column in `campaign_state`, and both are a
// decision about what survives a restart — which is a phase 12 question about
// persistence, not a field to add here. `TestTheChatPanelIsPresentAndCarriesTheLogHook`
// holds the container so the omission is one field rather than a missing region.
func (h *Handler) messages() []chat.Message {
	return nil
}

// documentView assembles the whole document: the title, the four landmarks'
// models and the centre, plus the rail component the document takes as its
// second argument.
//
// Both halves come out of one function rather than two, because they are built
// from the same four facts — the campaign, the reader, the instance and the live
// state — and two functions each reading three of them is how the navigation and
// the banner end up disagreeing about which campaign this is.
// context enters at `Render(ctx, …)` in `serveDocument` and nowhere else; the
// component closures templ generates capture nothing and take no context, so
// there is nothing here for the linter to see passing one. The fix it asks for —
// threading `ctx` into `Chrome(...)`, `Rail(...)` and `TokenList(...)` — is not
// an API templ has, and `internal/httpapi/search` and `components/live` carry
// the same suppression for the same reason.
//
// **The context is the request's, unchanged.** `internal/httpapi/events` detaches
// it with `context.WithoutCancel` because an SSE stream is meant to outlive the
// handler budget; a document render is bounded by the same budget as the request
// that asked for it, so detaching this one would buy nothing and would hide the
// next real use of the context.
//
//nolint:contextcheck // templ components are context-free constructors. The
func (h *Handler) documentView(
	ctx context.Context,
	access campaigns.Access,
) (webplay.DocumentView, templ.Component) {
	requestor := campaigns.Requestor(ctx)
	campaign := chrome.CampaignRef{Name: access.Campaign.Name, Slug: access.Campaign.Slug}

	view := webplay.DocumentView{
		Title:   documentTitle(h.Instance, campaign),
		Heading: campaignHeading(access.Campaign),
		Header: chrome.HeaderView{
			InstanceName: instanceDisplayName(h.Instance),
			Account: chrome.AccountView{
				Username:    requestor.Username,
				SignOutHref: h.SignOutHref,
			},
			Campaign: campaign,
			// `ConnectionLive` and not `ConnectionNone`: this route's one live
			// region is the banner's indicator, and §7.5's "no live region" rule
			// is about routes that are not live. The server cannot know whether a
			// socket is open yet, and the record's own answer to that is that the
			// indicator is client-owned — so the *document* asserts the state the
			// page is about to establish, and the client corrects it.
			Connection: chrome.ConnectionLive,
			// `Search` is left empty deliberately. §4.1's banner zones are
			// campaign, search, connection, theme and user, and the search *form*
			// is a second affordance for a route whose navigation already carries
			// Search — at 320px the banner is 3.5rem tall and one control wide.
			Theme: chrome.ThemeAuto,
		},
		Nav:    h.navigation(ctx, access),
		Footer: h.footer(access),
		Scene:  webplay.SceneView{},
		Actions: webplay.ActionBarView{
			Roll: h.rollView(ctx, access.Campaign.ID),
		},
	}

	// The rail carries three components, and **each renders its own hook** rather
	// than this route naming one.
	//
	// That is the whole contract between here and C3's package, and it is why
	// this function composes components instead of writing markup: the five patch
	// targets are `data-chrome` attributes on elements `live.Chrome` renders, and
	// the chat log's container is rendered by `components/chat`. A route that
	// hand-wrote those attributes would be a second spelling of every hook, and
	// `live.Decide` refuses a fragment whose `Target.Selector` does not match the
	// one `live.TargetByName` holds — so a hand-written hook is a patch refused
	// with `unknown_target`, silently, on every frame.
	//
	// **Order is the reader's**, and it is the record's: the token list is the
	// accessibility source of truth for the table (§7.6), the tracker and the dice
	// log are what happened, and the chat log is what was said. The live chrome's
	// own notice and announcement regions sit above them because a degraded
	// watcher is the one thing a reader must not have to scroll to find.
	return view, templ.Join(
		live.Chrome(live.ChromeView{
			EventsHref: eventsHref(access.Campaign.Slug),
			SocketHref: socketHref(access.Campaign.Slug),
			Notices:    h.degradedNotices(access),
		}),
		webplay.Rail(webplay.RailView{
			Label:    "Table panels",
			Campaign: access.Campaign.Slug,
			Tabs: []webplay.Tab{
				webplay.TokensTab(webplay.TokenList(webplay.TokenListView{
					Placements: h.placements(ctx, access),
				})),
			},
		}),
		chat.Panel(chat.PanelView{
			Messages: h.messages(),
		}),
	)
}

// documentTitle composes the `<title>` in UI §7.2's three-part form:
// "Table — <campaign> — <instance>".
//
// The campaign falls back to its slug and the instance to the product's name,
// for the same reason the wiki route's own `documentTitle` does — a campaign
// registered without a name still has a URL, and an unconfigured instance still
// has a first page. Neither fallback is this route's to invent; both are the
// ones the shell already uses, spelled here because they are unexported there.
func documentTitle(instance components.InstanceView, campaign chrome.CampaignRef) string {
	name := campaign.Name
	if name == "" {
		name = campaign.Slug
	}

	if name == "" {
		// Neither field set: the form without its middle part rather than a
		// stray separator pair. Unreachable through the gate, which always
		// resolves a campaign, and present because a title of "Table — — "
		// tells a reader nothing.
		return titlePage + titleSeparator + instanceDisplayName(instance)
	}

	return titlePage + titleSeparator + name + titleSeparator + instanceDisplayName(instance)
}

// instanceDisplayName is the instance's configured name, falling back to the
// product's.
func instanceDisplayName(instance components.InstanceView) string {
	if instance.Name == "" {
		return playProductName
	}

	return instance.Name
}

// campaignHeading is the document's `<h1>`, and it is the campaign's name.
//
// UI §8.3's rank 1 is the current location, and §12.3's type scale puts the
// campaign name at `--text-2xl` with `--text-3xl` on this route — so the heading
// is the campaign, not "Table". A campaign registered without a name gets its
// slug, which is what the wiki route's heading and the title above both do.
func campaignHeading(campaign domain.Campaign) string {
	if campaign.Name == "" {
		return campaign.Slug
	}

	return campaign.Name
}

// navigation builds the campaign navigation for this route.
//
// **The Wiki tree is absent rather than empty**, and that is a gap this file
// states rather than papers over: `wiki.wikiTree` is unexported, building the
// tree needs the campaign's page listing, and this route reads no content at all.
// §4.3's section order is Campaigns, Wiki, Table, Search, Admin, so this document
// renders four of five sections — with the Wiki section *absent*, which is the
// designed form of "nothing to list" (`NavView.hasWiki`) rather than a heading
// above no links. Nothing is unreachable: every page has an address, and §7.5's
// search is one link down.
//
// `TableHref` points at this route. It is present for a reader who may play and
// whose campaign has a system installed — `tableHref`'s rule, mirrored from
// `wiki.nav.go` because the two must not disagree about when the destination
// exists, and a link that answered 404 on the one page it appears on would be
// worse than no link.
func (h *Handler) navigation(ctx context.Context, access campaigns.Access) chrome.NavView {
	return chrome.NavView{
		Campaigns:  h.readerCampaigns(ctx),
		TableHref:  tableHref(access),
		SearchHref: campaignRouteHref(access.Campaign.Slug, segmentSearch),
		AdminHref:  adminHref(access),
	}
}

// tableHref is the live tabletop's address, or empty when this reader or this
// campaign has none. Mirrors `wiki.tableHref`, including both halves of its
// condition: membership (`Tier.CanPlay`) because no amount of public visibility
// makes a tabletop public, and `SystemID` because a member of a campaign with no
// gameplay system has no table to offer.
func tableHref(access campaigns.Access) string {
	if !access.Tier.CanPlay() || access.Campaign.SystemID == "" {
		return ""
	}

	return campaignRouteHref(access.Campaign.Slug, segmentPlay)
}

// adminHref is the campaign's settings address, and empty for anybody who is not
// its GM — UI §4.3's "absent, not disabled", mirrored from `wiki.adminHref`.
func adminHref(access campaigns.Access) string {
	if !access.Tier.CanEdit() {
		return ""
	}

	return campaignRouteHref(access.Campaign.Slug, segmentSettings)
}

// campaignRouteHref addresses one of a campaign's own routes: `/c/{slug}/{segment}`.
//
// Escaped, because `domain.ValidateSlug` making a slug a safe path segment is
// its own invariant and an href builder that relied on somebody else having
// checked is one validation away from an address that leaves the campaign.
// Empty for an empty slug, so a request that never passed the gate cannot
// produce a link that reads like a campaign's.
func campaignRouteHref(slug, segment string) string {
	if slug == "" {
		return ""
	}

	return pathSeparator + campaignRoutePrefix + pathSeparator +
		url.PathEscape(slug) + pathSeparator + segment
}

// readerCampaigns is the navigation's Campaigns section: the reader's own
// campaigns.
//
// Everything `wiki.readerCampaigns` says about this applies verbatim — the
// requestor rather than this membership, nil for an anonymous reader, and a
// failure that is a missing section rather than a failed document — so the shape
// is copied rather than shared: the two handlers hold different fields and a
// shared helper would take a lister, a logger and an event name, which is a
// function whose only purpose is to be called twice from two packages.
func (h *Handler) readerCampaigns(ctx context.Context) []chrome.CampaignRef {
	requestor := campaigns.Requestor(ctx)
	if h.Campaigns == nil || !requestor.Authenticated || requestor.UserID <= 0 {
		return nil
	}

	listed, err := h.Campaigns.CampaignsForUser(ctx, requestor.UserID)
	if err != nil {
		h.log(ctx, slog.LevelWarn, "play.nav_campaigns_unavailable",
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

// footer is the contentinfo model, with `Play` set — which is what replaces the
// compact bar's four destinations with the table's own row (UI §4.2) and what
// keeps this document from rendering a second `aria-label="Primary"` navigation
// on top of the action bar's "Table actions".
//
// `Clock` is empty because there is no clock to render: the game clock is the
// realtime plane's (§4.9) and nothing in a first server render can know it. The
// field renders nothing rather than a placeholder, so the row reads "Live · Leave
// table" until a client has a number to put in it.
func (h *Handler) footer(access campaigns.Access) chrome.FooterView {
	return chrome.FooterView{
		Version:    h.Instance.Version,
		StatusHref: h.StatusHref,
		Degraded:   degradedViews(h.Instance.Degraded),
		Play: &chrome.PlayBar{
			Connection: chrome.ConnectionLive,
			LeaveHref:  campaignOverviewHref(access.Campaign.Slug),
		},
	}
}

// degradedViews converts the instance's unhealthy subsystems into the footer's
// model. Nil in, nil out, so an empty list is distinguishable from a populated
// one at the call site — the same rule `components.chromeDegraded` follows.
func degradedViews(items []components.DegradedView) []chrome.DegradedView {
	if len(items) == 0 {
		return nil
	}

	converted := make([]chrome.DegradedView, 0, len(items))
	for at := range items {
		converted = append(converted, chrome.DegradedView{
			Name:   items[at].Name,
			Detail: items[at].Detail,
		})
	}

	return converted
}

// campaignOverviewHref is the campaign's own address, `/c/{slug}`, which is
// where "Leave table" goes.
//
// Not `/c/{slug}/play` with the segment omitted — an empty segment would produce
// a trailing slash, and this project's campaign route is registered as
// `/c/{slug}`, so the two addresses are one redirect apart and a link that
// redirects is a link whose target a reader cannot read off it.
func campaignOverviewHref(slug string) string {
	if slug == "" {
		return ""
	}

	return pathSeparator + campaignRoutePrefix + pathSeparator + url.PathEscape(slug)
}

// placements is the token list's rows, filtered by what this reader may see.
//
// **The filter is on the response, not on the panel.** UI §4.5 states the rule
// for rail panels — absent from the DOM for viewers below a tier, never
// CSS-hidden — and a placement a player must not see is the same claim one level
// down: a `display:none` row is a row in the response, and the response is the
// thing a player's own browser holds.
//
// A GM sees every placement; a player sees the ones marked visible. The GM
// condition is the **role** and not the tier, because `Tier` answers "may this
// reader reach this campaign's table" and both roles answer yes — the question
// here is what their role entitles them to *in* it.
//
// `Name`, `Kind` and `Layer` are left empty on purpose: `realtime.Placement`
// carries position, hit points, conditions, visibility and version, and there is
// no name, no registered kind and no layer in live state to fill them from. The
// row's own fallback (`nameLabel`) then announces the placement id, which is the
// documented exception rather than an invented label — and the three missing
// sources are named in the integration report rather than guessed at here.
func (h *Handler) placements(ctx context.Context, access campaigns.Access) []webplay.PlacementView {
	if h.Snapshot == nil {
		return nil
	}

	state, found := h.Snapshot(ctx, access.Campaign.ID)
	if !found {
		return nil
	}

	seesHidden := access.Membership != nil && access.Membership.Role == domain.RoleGM

	views := make([]webplay.PlacementView, 0, len(state.Placements))
	for at := range state.Placements {
		placement := state.Placements[at]
		if !seesHidden && !placement.Visible {
			continue
		}

		views = append(views, webplay.PlacementView{
			ID:         string(placement.ID),
			HP:         placement.HP,
			MaxHP:      placement.MaxHP,
			Conditions: placement.Conditions,
		})
	}

	return views
}

// rollView asks the campaign's system for the die sheet's notation.
//
// The three empty cases are each a real state and not a fault, for the reason
// `plugins.grammar` gives: no `Systems` wired, a campaign whose system this
// build cannot resolve, and a system that fails to answer. Each renders the
// honest empty state — "This campaign's gameplay system does not publish a roll
// notation." — instead of a heading over an empty list, and none of them is
// worth a 500 on a document whose four landmarks already rendered.
//
// The conversion out of `rules.Grammar` is this package's: the component takes
// four plain fields because a template should not be shaped by a type a gameplay
// system's contract may grow, and `RollView.available()` re-applies the same
// predicate `rules.Grammar.Valid` would so a notation with no terms never
// reaches the template as a usable sheet.
func (h *Handler) rollView(ctx context.Context, campaignID int64) webplay.RollView {
	if h.Systems == nil || campaignID <= 0 {
		return webplay.RollView{}
	}

	system, err := h.Systems(ctx, campaignID)
	if err != nil || system == nil {
		h.log(ctx, slog.LevelWarn, "play.grammar_unavailable",
			slog.Int64("campaign_id", campaignID),
			slog.String("class", "play.grammar_unavailable"),
		)

		return webplay.RollView{}
	}

	grammar := system.Grammar()

	view := webplay.RollView{
		Notation: grammar.Notation,
		Summary:  grammar.Summary,
		Example:  grammar.Example,
		Terms:    make([]webplay.RollTerm, 0, len(grammar.Terms)),
	}

	for at := range grammar.Terms {
		view.Terms = append(view.Terms, webplay.RollTerm{
			Name:    grammar.Terms[at].Name,
			Summary: grammar.Terms[at].Summary,
		})
	}

	return view
}
