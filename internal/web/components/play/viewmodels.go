package play

// The view models for the live tabletop's server-rendered surface: one placement
// as the token list reads it, the list itself, and the tabbed utilities rail that
// holds it.
//
// # Why this package holds its own types rather than taking `realtime.Placement`
//
// The same reason `components/chat` declares a `Message` rather than taking
// `realtime.Message`: this package must not import `internal/realtime`, so that
// the wire codec, the hub and the interface can change independently, and so a
// template is never handed a value carrying fields nobody has decided a reader
// may see.
//
// The decision that matters is therefore *upstream* of this type, exactly as it
// is for chat: **a placement the reader may not see never becomes a
// `PlacementView`.** There is no `Visible bool` to check and no `GMOnly bool` to
// branch on, so a template cannot forget to check. The route decides, from the
// viewer's role, and omits.
//
// # The two fields the domain does not have yet
//
// `Name` and `Layer` are the reported gap, and both are load-bearing rather than
// cosmetic:
//
//   - **`realtime.Placement` has no name.** It carries an id, a position, hit
//     points, conditions, a visibility flag and a version. The *name* of a game
//     object is the page's title (§4.2 puts the durable half of a placement's
//     definition in front matter), so a caller resolves it from the page index.
//   - **`realtime.Placement` has no layer.** UI §7.6 says the token list names
//     each placement's layer, and UI §4.1 has a GM cycling map layers, so a layer
//     is part of the design — but nothing in `internal/domain` or
//     `internal/realtime` carries one. Until it does, `Layer` is what the caller
//     knows and an empty one is rendered as an honest absence rather than a
//     fabricated label. `LayerUnknown` is the string for that, and
//     `TestALayerThatWasNotSuppliedSaysSoRatherThanGuessing` is what holds it.
//
// Inventing a layer type here would have been the wrong fix: a type in the
// interface package becomes the vocabulary the domain then has to match, and a
// guess is harder to correct than a missing field is to fill.

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/a-h/templ"
)

// The test hooks and DOM ids the token list and the rail share.
//
// Named constants rather than literals in the templates, for the reason
// `components/chat` gives: §10.2's hook rule asserts every `data-testid` appears
// exactly once and is not empty, so a hook spelled two ways in two files is a
// hook that appears twice and fails an audit for a reason nobody would guess.
// Each is also the id an `aria-labelledby` resolves to, so a rename has to move
// both ends.
const (
	// PanelTestID is the token list's `<section>`.
	PanelTestID = "token-list-panel"
	// PanelHeadingID is that section's heading, which its `aria-labelledby`
	// names.
	PanelHeadingID = "token-list-heading"
	// PanelRegionID is the token list's element id: the target of UI §7.2's
	// fourth skip link and the name of the region its `role="region"` declares.
	//
	// A separate constant from `PanelTestID` because an id and a hook are two
	// namespaces a reader should not have to compare by eye, and because this one
	// is *addressed* — by an `href` and by the focus a skip link moves — where a
	// `data-testid` is only ever selected. A rename here that does not move the
	// skip link is a link that moves focus nowhere, which §7.2 calls worse than no
	// skip link at all.
	PanelRegionID = "play-token-list"
	// CountTestID is the panel's own count line.
	CountTestID = "token-list-count"
	// EmptyTestID is the no-placements-yet state.
	EmptyTestID = "token-list-empty"
	// ListTestID is the `<ul>` the client binds `keydown` to.
	ListTestID = "token-list"
	// KeysTestID is the keyboard hint under the list.
	KeysTestID = "token-list-keys"

	// RowChromeHook is the attribute every row's button carries, and the
	// selector the client walks. It is `data-token` rather than a `data-testid`
	// for the reason `chat`'s message rows carry `data-seq` and not a hook: a
	// hook on every row appears N times and selects nothing.
	//
	// The **attribute name**, not the value: the template writes it valueless
	// (`data-token`) and the client selects `[data-token]`, so a Go constant
	// holding `"token"` would be a second spelling of the same fact.
	RowChromeHook = "data-token"
	// RowIDAttribute is the attribute carrying the placement id. Rows are
	// located by it, and it is unique within a campaign because
	// `realtime.PlacementID` is.
	RowIDAttribute = "data-placement"

	// RailChromeHook is the wrapper `ui.Tabs` is mounted in, and what the D16
	// script binds to.
	RailChromeHook = "data-rail-tabs"
	// RailDefaultsAttribute is the rule table: which tiers default to which tab.
	RailDefaultsAttribute = "data-tab-defaults"
	// RailCampaignAttribute is the campaign slug, which keys the remembered tab.
	RailCampaignAttribute = "data-campaign"

	// TokensTabID is the token list's tab, and the tab this package owns.
	TokensTabID = "tokens"
	// InitiativeTabID is the initiative tracker's tab. Named here because D16's
	// rule is expressed in terms of both, and a rule that named one of its two
	// answers in a template literal would be a rule with two owners.
	InitiativeTabID = "initiative"

	// KeysHint is the sentence under the list.
	KeysHint = "Up and down move between tokens."
)

// The tiers UI §3.7's resolver can write, and the two halves D16 splits them
// into.
//
// **A closed list, and the reason it is here rather than in the client.** The
// rule "Tokens on TV and phone, Initiative on laptop" has to be applied from
// `data-ui`, so it cannot be a server-side branch — and a tier added to the
// resolver without being added here would then silently fall through to whatever
// the client defaults to. So the *rule* is server-authored, in this package, and
// it is handed to the client as data. The client knows no tier names at all: it
// reads the document's own rule and applies it.
//
// That inverts the usual failure. A tier in two tabs' lists is an ambiguous
// default and a tier in none is no default, and both are asserted over this list
// rather than discovered in a browser.
var (
	// Tiers is every value `data-ui` can carry.
	Tiers = []string{"compact", "compact-short", "medium", "large", "wide", "tv", "tv-wide"}

	// TabletTiers are the tiers where the token list is the primary way to change
	// what the map shows — TV because dragging is unavailable (UI §7.6) and the
	// phone tiers because the map is a readout there (UI §4.9) — so D16's default
	// is `Tokens` on all five.
	TabletTiers = []string{"compact", "compact-short", "tv", "tv-wide"}
	// LaptopTiers are the tiers with a pointer and a large enough rail for the
	// initiative tracker to be the thing a GM looks at first.
	LaptopTiers = []string{"medium", "large", "wide"}
)

// TabDefaults is D16's rule, as the document carries it: which tiers make which
// tab the default.
//
// **Rendered in full on every document**, which is the whole of §13.5's byte
// identity for this surface: a document carrying all seven answers cannot have
// chosen one, so it does not vary by device, and a server that resolved the rule
// would carry one key where this carries seven. `TestTheRuleIsCarriedWholeAndNoTierIsResolved`
// is what holds that.
type TabDefaults map[string][]string

// json renders the rule as the compact JSON the client parses.
func (d TabDefaults) json() string {
	encoded, err := json.Marshal(map[string][]string(d))
	if err != nil {
		// Unreachable: every key and value is a string drawn from `Tiers`, which
		// is a package-level list of literals. Panicking rather than returning an
		// error is the same call `web.Dist` makes for a constant the build already
		// guarantees, and a template cannot handle an error from a string method.
		panic("play: the tab-default rule is not encodable: " + err.Error())
	}

	return string(encoded)
}

// DefaultTabDefaults is the rule D16 fixes, and the only value the product ships.
func DefaultTabDefaults() TabDefaults {
	return TabDefaults{
		TokensTabID:     slices.Clone(TabletTiers),
		InitiativeTabID: slices.Clone(LaptopTiers),
	}
}

// PlacementView is one placement as the token list reads it.
//
// The four facts UI §7.6 requires a row to carry — the name, the hit points, the
// conditions and the layer — are four fields, and each is stated rather than
// derived so the rendering has nothing to infer.
type PlacementView struct {
	// ID is the placement's id within its campaign, and is what the row carries
	// as `data-placement`. Required: a row a client cannot identify is a row no
	// intent can be about.
	ID string

	// Name is what the reader calls the placement — the game object's title.
	//
	// Required, and **not** derived from `ID`: an id is a protocol identifier and
	// reads as nothing to a person, while this string is announced. §7.6's "each
	// naming it" is this field.
	Name string

	// Kind is the placement's registered kind label, as the registry spells it —
	// "Token" for a `token` placement, "Scene" for a `scene` one (UI §1.2). It
	// is a label rather than a kind id for the reason that table gives: the map
	// and the token list both say *Token*.
	//
	// Empty renders as nothing rather than as a placeholder. A row reading
	// "Goblin · 7 of 7 hit points · Layer: not recorded" is incomplete and says
	// so; a row reading "Goblin · kind unknown" invents a claim.
	Kind string

	// HP and MaxHP are the placement's current and maximum hit points.
	//
	// Two fields rather than a label because §4.2 puts the maximum in front
	// matter and the current in live state, so the two have different lifetimes
	// and a caller may legitimately have one without the other.
	HP    int
	MaxHP int

	// Conditions are the placement's status conditions, as the reader reads them.
	//
	// **Sorted and de-duplicated during rendering**, not trusted from the caller:
	// `realtime.Placement.Conditions` keeps that invariant in memory, and a
	// rendering whose byte order depended on the order two clients happened to
	// act in is a document no test can compare.
	Conditions []string

	// Layer is the map layer this placement sits on, in the words the rail and
	// the map use for it. Empty renders `LayerUnknown`.
	Layer string

	// Selected is whether this placement is the selected one.
	//
	// False in every server-rendered document, deliberately: selection is client
	// state (§7.6's "Enter selects and moves the camera"), and a server that
	// rendered it as selected would be asserting something the reader did not do.
	// The client sets it through `aria-pressed`.
	Selected bool
}

// LayerUnknown is what a row says about its layer when the caller supplied none.
//
// A sentence rather than an omission, and the reason is §7.6's own: the row names
// the layer, so a row that silently has no layer says four things about a
// placement and is silent about the fifth, which reads to a reader as "this
// placement has no layer" when the truth is "nothing told this template". The
// wording distinguishes them.
const LayerUnknown = "not recorded"

// TokenListView is the token list: the panel's placements, in the order they are
// read.
type TokenListView struct {
	// Placements are the placements this reader may see, in the order the row
	// order is read. Arrow traversal is this order, so a caller that sorts them
	// is deciding what the arrow keys do.
	//
	// Empty is a designed surface (UI §4.7) and not an error: a campaign's table
	// before the first token is placed is an ordinary state.
	Placements []PlacementView
}

// RailView is the tabbed utilities rail: the panels, and the rule that decides
// which one a device opens on.
//
// **No field for a tier, a form factor or a remembered tab, and the absence is
// the contract.** Every one of those is a device- or reader-dependent input, and
// a field carrying one is a field a route can fill — which is how a document
// starts varying by the thing §13.5 and ADR 0035 rule out.
// `TestTheViewModelHasNoFieldThatCouldVaryTheDocument` walks the struct and
// fails on a field whose name reaches for any of them.
type RailView struct {
	// Label is the tablist's accessible name. Required, for the reason
	// `ui.TabsView.Label` gives: a tablist with no name is a group of buttons a
	// reader cannot tell apart from the toolbar beside it.
	Label string

	// Tabs are the panels, in order. The first is the one a client with no
	// `data-ui`, no storage and no script keeps.
	Tabs []Tab

	// Defaults is the tier rule rendered onto the wrapper. `DefaultTabDefaults`
	// is the product's rule; the field exists so a test can render a *wrong* one
	// and be met by the client, which is the only way a rule about storage and
	// tiers can be tested at all.
	//
	// The zero value is replaced by `DefaultTabDefaults` during rendering, so a
	// caller who forgets it gets the shipped rule rather than a rail with no
	// defaults at all.
	Defaults TabDefaults

	// Campaign is the campaign's slug, which keys the remembered tab in
	// `localStorage`. Empty renders no key and the client then falls back to the
	// tier rule on every visit — the same silent fallback a reader with storage
	// disabled gets, which is why it is a supported state and not an error.
	Campaign string
}

// Tab is one rail panel: an id, a label, and its body.
//
// A three-field struct local to this package rather than `ui.Tab`, because the
// rail composes `ui.Tabs` whole and this is the caller-side shape the route
// fills. Converting it is one function, `tabsView`, and the two types cannot
// drift in the fields they share because one of them is a superset of the other
// and `TestTheRailCarriesEveryPanelTheViewModelPromises` asserts the mapping.
//
// Exported because a route fills it: a struct literal in `internal/httpapi` is
// how a panel gets its id and forgets its label, and an unexported type here
// would make every caller go through a constructor that cannot be bypassed.
type Tab struct {
	// ID is the tab's element id and its `data-tab` value. Unique within the
	// rail; §7.2's reference-integrity rule is what enforces it against the
	// rendered document.
	ID string
	// Label is the tab's visible text and its accessible name.
	Label string
	// Content is the panel's body.
	Content templ.Component
}

// defaults returns the tier rule, defaulting the zero value.
func (view RailView) defaults() TabDefaults {
	if len(view.Defaults) == 0 {
		return DefaultTabDefaults()
	}

	return view.Defaults
}

// --- The copy ---------------------------------------------------------------------

// nameLabel is the placement's name, falling back to its id.
//
// The fallback is a documented exception to the rule this package follows
// elsewhere (`chat` omits an absent line; `search` names no layer). A token row
// whose first span is empty announces the row's remaining three facts with no
// subject, and a placement id is the only thing the row can honestly say it has.
func nameLabel(placement PlacementView) string {
	if placement.Name != "" {
		return placement.Name
	}

	return placement.ID
}

// hpLabel is the hit points, as a reader reads them.
//
// "7 of 7 hit points" rather than "7/7", because a screen reader announces a
// slash as "slash" and the two numbers are then indistinguishable in the
// announcement. A max of zero renders the current figure alone: a placement whose
// maximum is unknown has no ratio to report, and "7 of 0 hit points" is a claim
// about the placement rather than about what this row knows.
func hpLabel(placement PlacementView) string {
	if placement.MaxHP <= 0 {
		return strconv.Itoa(placement.HP) + " hit points"
	}

	return strconv.Itoa(placement.HP) + " of " + strconv.Itoa(placement.MaxHP) + " hit points"
}

// conditionsLabel is the placement's conditions, as one clause.
//
// Sorted, de-duplicated and empty-filtered during rendering — see
// `Conditions`'s doc comment. The empty case is omitted from the row entirely
// rather than rendered as "no conditions", because a condition-free placement is
// the ordinary case and a row that said so four times over would bury the three
// rows that have some.
func conditionsLabel(placement PlacementView) string {
	names := make([]string, 0, len(placement.Conditions))

	seen := make(map[string]struct{}, len(placement.Conditions))

	for _, condition := range placement.Conditions {
		trimmed := strings.TrimSpace(condition)
		if trimmed == "" {
			continue
		}

		if _, already := seen[trimmed]; already {
			continue
		}

		seen[trimmed] = struct{}{}

		names = append(names, trimmed)
	}

	slices.Sort(names)

	if len(names) == 0 {
		return ""
	}

	if len(names) == 1 {
		return "Condition: " + names[0]
	}

	return "Conditions: " + strings.Join(names, ", ")
}

// layerLabel is the placement's layer, or that it was not recorded.
func layerLabel(placement PlacementView) string {
	if placement.Layer == "" {
		return "Layer: " + LayerUnknown
	}

	return "Layer: " + placement.Layer
}

// countLabel is the panel's own count line.
//
// The count of what is **on screen**, not of what the table holds: a reader who
// sees two tokens is told two, and a count taken from anywhere else would say
// more and be wrong in a way that looks like a bug in the data rather than in
// the wording.
func countLabel(placements []PlacementView) string {
	switch len(placements) {
	case 0:
		return "Nothing on the table."
	case 1:
		return "1 token."
	default:
		return strconv.Itoa(len(placements)) + " tokens."
	}
}

// panelState is the panel's `data-state`, so a test asserts the state rather than
// inferring it from a count.
//
// One of `empty` or `populated`, and written out rather than left to the absence
// of an attribute: a state rendered half-way is invisible to an audit that only
// counts what is present.
func panelState(placements []PlacementView) string {
	if len(placements) == 0 {
		return "empty"
	}

	return "populated"
}
