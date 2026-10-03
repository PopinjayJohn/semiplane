package chrome

// Package chrome is the shell's four landmarks — the header, the footer, the
// campaign navigation and the utilities rail — together with the view models
// they take.
//
// The four are separate components rather than branches inside a shell template
// for one reason, and the reason is UI §3.7: every tier is a CSS variant of one
// canonical DOM. A tier re-decides which landmark persists, which becomes a
// sheet and which disappears, and that decision is entirely in the stylesheet
// (UI §3.1, §3.2, §3.5). So the markup cannot vary by tier, cannot vary by
// route, and cannot be assembled per page — a plugin page type fills the centre
// slot and inherits this accessibility contract unmodified (UI §4.11.1), and
// UI §4.6 removes the navigation before a campaign exists, which is an absence
// the markup has to express rather than a branch to fill with nothing.
//
// # The dependency direction
//
// Nothing in this package imports `components`. The direction is the opposite
// one, and it has to stay that way: a campaign shell composes these landmarks,
// so `components` will import this package, and a package that imports its own
// caller does not compile. That is why the view models below are declared here
// rather than in `components/viewmodels.go` — a file this work item does not
// own. `DegradedView`, `AccountView` and the instance name are the three shapes
// the two packages now both declare; the integration report names the
// conversion so there is one of each rather than two.
//
// # The one rule about controls
//
// A control here is either a real control that acts over ordinary HTTP, or a
// control that renders in the state which already works and declares its client
// hook in a `data-chrome` attribute. Nothing renders in a state that needs the
// client layer to become useful: a menu button that opens nothing, a disclosure
// that hides content behind itself, a theme button pressed to a value it cannot
// leave. Each of those is a focus stop that lies, and `components/shell.templ`
// already makes the call once, for the account menu. So every one of them
// defaults to the functional state — `aria-expanded="true"`, "Theme: auto" — and
// phase 9 rewrites the label when it takes the control over.
//
// This comment is here, below the package clause, and not above it. templ writes
// `// templ: version: ...` into every file it generates, and a comment directly
// above a package clause is that file's doc comment — so a hand-written one above
// `package chrome` here gives the package two godocs, because every generated
// file in it already carries one, and godoclint fails the build with "package
// has more than one godoc". `components/viewmodels.go` is written the same way and
// says so at length; every `.templ` file here follows the same shape.

import "strconv"

// productName names the software where the operator has not configured an
// instance name. The same fallback and the same reasoning as
// `components/productName`: every operator reaches an unconfigured instance
// before any name is set, and a blank slot in the header is the first thing a
// new install looks like. The duplication is deliberate for now and is named in
// the integration report.
const productName = "semiplane"

// The DOM ids the shell's skip links target (UI §7.2). The first two are also
// what `components/shell.templ` already renders, so a shell that adopts these
// landmarks keeps every skip link it has.
const (
	// navID is the campaign navigation, and the target of the third skip link
	// in UI §7.2's order. The element is focusable so a skip link lands
	// somewhere a screen reader announces rather than at the top of a scroll
	// container.
	navID = "nav"
	// railID is the utilities rail.
	railID = "rail"
)

// destinationsID is the id of the navigation's destination list, and the target
// of the collapse control's `aria-controls`. The collapse control changes the
// width of the list rather than showing and hiding it, so the relationship is
// stated as a disclosure of the labels inside it.
const destinationsID = "nav-destinations"

// The two nested lists the navigation's disclosures reveal, and the id prefix the
// wiki tree's folders are named under. Named rather than built by string
// arithmetic at the call site, because each list id is a `data-testid`, an
// `aria-controls` target and a disclosure's claim, and a mismatch between the
// three would be a disclosure that reveals nothing while claiming otherwise.
const (
	campaignsListID = "nav-campaigns-list"
	wikiListID      = "nav-wiki-list"
	wikiPath        = "wiki"
)

// DegradedView is one subsystem of this instance that is not working.
//
// Named rather than counted, for the reason `components.DegradedView` gives: a
// content watcher that stopped, a cache serving a stale page, a reconciliation
// loop that gave up. Every one of them reads to an operator as a sentence, and a
// counter is a number nobody can act on.
type DegradedView struct {
	// Name is the subsystem, as the status view names it.
	Name string
	// Detail is one clause on what it means, in the operator's terms. Empty
	// renders the name alone rather than an empty line.
	Detail string
}

// AccountView is the signed-in identity, and the one action the header's account
// zone can take without a client.
//
// The sign-out target lives here rather than beside the account because a form
// that posts nowhere is a focus stop that does nothing, which is worse than no
// button: an empty SignOutHref renders no form at all.
type AccountView struct {
	// Username is the account's own name. Empty means nobody is signed in,
	// which is the normal state of /login and renders no account zone.
	Username string
	// SignOutHref is where the account zone's form posts.
	SignOutHref string
}

// CampaignRef is the campaign a route is inside.
//
// An empty Slug is the pre-campaign case, and it is what UI §4.6 keys on: the
// left nav is entirely campaign-scoped, so with no campaign there is no nav,
// and the header's left zone falls back to the instance's own name because
// there is no campaign name to show. The zero value is therefore meaningful
// rather than merely unpopulated, and a caller that forgets produces the
// designed pre-campaign shell instead of a broken one.
type CampaignRef struct {
	// Name is what the reader calls this campaign. A campaign registered
	// without one still has a slug, and the slug is the honest fallback.
	Name string
	// Slug is the URL segment, validated by the domain before it reaches a
	// template. The header and the navigation derive their addresses from it,
	// which is why this is a reference rather than a bag of hrefs.
	Slug string
}

// ThemeStylesheet is the campaign's own brand sheet (UI §4.12.2).
//
// A sibling of `href` and not a field on the view model, because the slug is
// already here and a second copy of it in a model is a second answer to "which
// campaign is this document about" — the shape of bug AGENTS.md records for the
// `front_matter` column and the design-record index both.
//
// **Per-campaign, not per-user** (§4.12.4), and that is the load-bearing part:
// the address is derived from the slug, which is already in the path, so this
// costs the document nothing under §3.7's `no Vary: Cookie`. A per-user theme
// would put a reader's preference into a URL that is not keyed by the reader,
// which is either a shared cache serving one account's brand to another or a
// cookie-varying document — and ADR 0035 is the record of why the second is not
// acceptable.
//
// Exported, unlike `href` and `label`, because the `<link>` is emitted by the
// shell in `internal/web/components` rather than by a component in this package:
// templ generates into the caller's package, so an unexported method here is not
// reachable from there. That is the only reason it is exported, and the comment
// is here so a reader does not take the asymmetry for an oversight.
func (campaign CampaignRef) ThemeStylesheet() string {
	return campaign.href() + "/theme.css"
}

// label is the name to show for the campaign, falling back to its slug.
//
// Unexported: the fallback is a rendering decision, and a route that wanted a
// different one has a Name to put in the model.
func (campaign CampaignRef) label() string {
	if campaign.Name == "" {
		return campaign.Slug
	}

	return campaign.Name
}

// href is the campaign's own address, and the only address the shell builds
// itself.
//
// The other destinations' hrefs are supplied by the caller rather than derived,
// because the routes behind them are P6's and the shape of a wiki path is the
// content pipeline's to decide. A shell that guessed them would be guessing at
// three different URL schemes in one place.
func (campaign CampaignRef) href() string {
	return "/c/" + campaign.Slug
}

// SearchForm is the header's search zone: a `GET` form over ordinary HTTP.
//
// Both fields are empty on a pre-campaign route, and the form is then absent
// rather than disabled or pointed at `/`. There is nothing to search before a
// campaign exists, and a search field that searches nothing is a focus stop in
// the one place the reader is looking for the answer.
type SearchForm struct {
	// Action is where the query goes, `/c/{slug}/search` inside a campaign.
	Action string
	// Query is what to put in the field: a submitted query echoed back, and
	// empty otherwise. Search is submit-to-navigate over ordinary HTTP and not
	// a live region (UI §7.5), so the field is a form, not a fetch.
	Query string
}

// Connection is the state of the route's live connection, as the header's
// indicator reports it.
//
// A closed enum rather than a caller-supplied sentence, so the wording lives in
// one place and the live-region policy can be a property of the value: zero is
// ConnectionNone, which renders no region at all. That is the mechanism behind
// UI §7.5's rule that the search route has no live region — the route is not
// live, so a caller leaves the zero value and the whole `role="status"` element
// is absent rather than empty. A connection notice on the search surface is not
// a thing this package can be asked to render.
type Connection int

const (
	// ConnectionNone means the route is not live: no WebSocket, no SSE. It
	// renders nothing, and it is the zero value so that "forgot to set it" and
	// "not a live route" are the same markup.
	ConnectionNone Connection = iota
	// ConnectionLive is a healthy connection.
	ConnectionLive
	// ConnectionReconnecting is a connection that has dropped and is coming
	// back. UI §9 keeps it to the indicator: a reconnect must not move focus,
	// and must not raise a toast over an open dialog.
	ConnectionReconnecting
	// ConnectionOffline is a connection that is down and not retrying.
	ConnectionOffline
)

// label is the word the indicator shows, and the whole of UI §6.5's rule for it:
// the state is a text label, so hue is never the only carrier. The three words
// are UI §4.1's, verbatim — a reader learns "Offline" once.
func (state Connection) label() string {
	switch state {
	case ConnectionNone:
		return ""
	case ConnectionLive:
		return "Live"
	case ConnectionReconnecting:
		return "Reconnecting"
	case ConnectionOffline:
		return "Offline"
	default:
		// A value built by hand rather than through the enum is a bug, not a
		// new state. Saying nothing keeps a stranger's word out of the header.
		return ""
	}
}

// Theme is the theme the reader has chosen, as the header's single button
// reports it.
//
// The document is not allowed to vary by it: UI §3.7 resolves both root
// attributes client-side before first paint, and UI §6.6's conclusion is that
// no `Vary: Cookie` is emitted. So the server renders ThemeAuto — the default,
// and the only value it is entitled to assert — and the client rewrites the
// label when the reader cycles it.
type Theme int

const (
	// ThemeAuto follows the operating system's polarity.
	ThemeAuto Theme = iota
	// ThemeLight pins the document to the light theme.
	ThemeLight
	// ThemeDark pins the document to the dark theme.
	ThemeDark
)

// label names the result rather than the action, which is UI §4.1's rule for
// this control and is what makes a cycling button legible: the reader is told
// where they are, not what pressing it will do.
func (theme Theme) label() string {
	switch theme {
	case ThemeAuto:
		return "Theme: auto"
	case ThemeLight:
		return "Theme: light"
	case ThemeDark:
		return "Theme: dark"
	default:
		return "Theme: auto"
	}
}

// pressed reports whether the theme is an explicit choice.
//
// False for auto, because auto is the absence of a choice: a pressed button says
// "this is on", and what is on in the auto state is the operating system's
// polarity, which the reader did not set here.
func (theme Theme) pressed() bool {
	return theme == ThemeLight || theme == ThemeDark
}

// HeaderView is everything the banner needs, for a route with or without a
// campaign.
//
// There is no field for a menu button or a mode switcher. Both are deferred to
// the client layer and named in the integration report: UI §4.1 puts them in the
// banner's left zone, and rendering them now would mean shipping a button that
// opens nothing and a `<select>` that submits nothing.
type HeaderView struct {
	// InstanceName is the running instance's configured name, and the banner's
	// brand link. It renders only when there is no campaign: inside a campaign
	// the campaign name is the current location (UI §8.3's rank 1) and the
	// instance name is noise in a 56px bar. Empty falls back to the product
	// name.
	InstanceName string
	// Account is the signed-in identity and its sign-out form. An empty
	// Username renders no account zone.
	Account AccountView
	// Campaign is the campaign this route is inside. An empty Slug is the
	// pre-campaign case, and the header renders its brand link instead of a
	// campaign name.
	Campaign CampaignRef
	// Search is the search zone. Both fields empty renders no form.
	Search SearchForm
	// Connection is the live connection's state, and ConnectionNone — the zero
	// value — renders no live region at all.
	Connection Connection
	// Theme is the theme the reader has chosen, which the server may only
	// assert as ThemeAuto.
	Theme Theme
}

// instanceLabel is the name the banner shows for this instance, falling back to
// the product's.
func (view HeaderView) instanceLabel() string {
	if view.InstanceName == "" {
		return productName
	}

	return view.InstanceName
}

// BarDestinations is the compact bar's four destinations: Wiki, Search, Table,
// More.
//
// Four optional addresses rather than a struct the shell fills in, because
// UI §4.3 makes three of them conditionally absent — Table is absent for an
// anonymous reader and for a campaign whose gameplay system is not installed,
// and "absent, not disabled" is the rule. An empty address renders no link, and
// if all four are empty the bar itself is absent.
type BarDestinations struct {
	// Wiki is the campaign's page tree.
	Wiki string
	// Search is the campaign's search route.
	Search string
	// Table is the live tabletop. A single destination, never a list: a
	// campaign has at most one, so there is nothing to enumerate.
	Table string
	// More is whatever the instance offers beyond the three above. Empty omits
	// the destination rather than pointing at a route that answers 404.
	More string
}

// empty reports whether there is nothing for the bar to navigate to.
func (destinations BarDestinations) empty() bool {
	return destinations.Wiki == "" &&
		destinations.Search == "" &&
		destinations.Table == "" &&
		destinations.More == ""
}

// PlayBar is the footer's row on the table route, where UI §4.2 replaces the
// four destinations with the game clock and a way out.
//
// A pointer on the footer, so "not on the table" and "on the table with nothing
// in it" cannot be the same value — the compact bar is a navigation landmark
// here and a status row there, and a shell that could not tell which would
// render four navigation links on the one page whose footer must not be them.
type PlayBar struct {
	// Clock is the game clock's own text, supplied rather than formatted here:
	// the clock is the realtime plane's (§4.9) and its formatting is that
	// system's, not the shell's.
	Clock string
	// Connection is the live connection's state as the footer reports it. It
	// is plain text and deliberately not a live region: UI §4.1 puts the
	// indicator in the header and UI §9 calls that the only signal for a
	// reconnect, so a second region here would announce one fact twice.
	Connection Connection
	// LeaveHref is where "Leave table" goes. Empty omits the link, because
	// leaving is not something a reader should be offered and then fail to do.
	LeaveHref string
}

// FooterView is everything the contentinfo landmark needs.
//
// The degraded subsystems ride here rather than in a toast because UI §4.2 puts
// a persistent warning in the footer: a campaign whose content root has gone
// missing is an ongoing condition, not an event, and a banner that announces
// itself once then leaves is a banner about a different problem.
type FooterView struct {
	// Version is the build identifier, shown when set.
	Version string
	// StatusHref is the instance status link. Empty omits it rather than
	// pointing at a route that answers 404.
	StatusHref string
	// Latency is the live connection's round-trip time, as text, on live routes
	// only. Plain text and not a live region: a number that changes on a timer
	// is a screen-reader loop, and nothing in UI §7.5's table is announced from
	// here.
	Latency string
	// Degraded names the subsystems that are not working. Empty is healthy, and
	// healthy renders nothing — a footer that says "everything is fine" on
	// every page is noise that trains a GM to ignore the footer.
	Degraded []DegradedView
	// Bar is the compact bottom bar's destinations. All four empty renders no
	// bar, which is the pre-campaign case.
	Bar BarDestinations
	// Play replaces Bar on the table route (UI §4.2). Nil everywhere else.
	Play *PlayBar
}

// PageNode is one row of the wiki tree, and a folder when it has children.
//
// The tree is capped by the caller rather than by this package, and deliberately
// so: which pages a navigation lists is a content decision, and a component that
// truncated it would be deciding it silently. UI §4.3 accepts the cost it names
// — a long tab sequence, mitigated by the skip link and a capped depth — and
// this package contributes the other half of the answer, the skip link.
type PageNode struct {
	// Title is the page's own name.
	Title string
	// Href is the page's address inside the campaign. Caller-supplied because
	// the shape of a wiki path is the content pipeline's to decide.
	Href string
	// Kind is the label the gameplay system's registry gives this page's kind,
	// resolved by the caller (UI §4.11.3). Data, not a string this package
	// chose: a UI plugin registering a new kind needs no shell change, and an
	// unrecognised kind arrives already degraded to "Page" rather than as an
	// error. Empty renders no badge.
	Kind string
	// Children are the pages nested under this one. A node with children is a
	// folder, and its row carries the disclosure UI §4.3 asks for.
	Children []PageNode
}

// NavView is everything the campaign navigation needs.
//
// A campaign has at most one live tabletop, so nothing here describes a list of
// them: Table is one address or none. Every address is a string rather than a
// bool, because "absent, not disabled" is UI §4.3's rule for every conditional
// entry and a boolean plus a constant is how a disabled link gets shipped.
type NavView struct {
	// Campaigns are the reader's campaigns, for the Campaigns section. Empty
	// omits the section: a switcher with nothing to switch between is a heading
	// above no links. CampaignRef rather than a purpose-made type, so the
	// unnamed-campaign fallback to the slug is written once for the banner and
	// once for the list rather than twice in two structs.
	Campaigns []CampaignRef
	// Wiki is the campaign's page tree. A nil Wiki omits the Wiki section: a
	// campaign with no pages is a designed empty state in the centre
	// (UI §4.7), not a navigation section listing nothing.
	Wiki *PageNode
	// TableHref is the live tabletop. Empty omits the link, for an anonymous
	// reader and for a campaign whose gameplay system is not installed
	// (UI §4.7's `system_id` row).
	TableHref string
	// SearchHref is the campaign's search route. Empty omits the link.
	SearchHref string
	// AdminHref is the campaign's settings route, and the whole of its GM-only
	// surface. Empty for a player and for an anonymous reader, which is how
	// UI §4.3's "edit affordances are absent, not disabled" is met by absence
	// rather than by a permission check inside a template.
	AdminHref string
}

// hasWiki reports whether the Wiki section has anything to list.
//
// A non-nil Wiki is not enough: the tree's root is a container rather than a
// page, so a campaign with no pages arrives as a root with no children — and
// UI §4.7 puts "This campaign has no pages yet" in the *centre*, where the
// reader is looking for it, not in a navigation section above no links.
func (view NavView) hasWiki() bool {
	return view.Wiki != nil && len(view.Wiki.Children) > 0
}

// navNodeID is the DOM id of a wiki node's disclosure region, built from the
// index path of the branch it sits on.
//
// From the path rather than from the title or the href because both of those
// can contain anything — a title can hold a quote, an href can hold a slash and
// a percent escape — and an id built from either produces markup a parser
// rejects and a selector nobody can write. The path is a caller's own ordering
// of its own rows, so it is unique within one render by construction.
func navNodeID(path string, index int) string {
	return "wiki-" + path + strconv.Itoa(index)
}
