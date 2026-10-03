package play

// The models the table's *document* takes — the whole page at `/c/{slug}/play`
// rather than one panel inside it — and the copy the action bar owns.
//
// # The chrome arrives as `chrome`'s own view models, and not as a second layer
//
// `components` declares the route-facing layer (`ShellView`) and converts it into
// `chrome.HeaderView` and friends, and this package deliberately does **not**
// repeat that. The conversion exists because four routes fill one `ShellView` and
// each of them already had it; the table route fills exactly one document, and a
// `DocumentView` wrapping `chrome.HeaderView` in a struct of its own would add a
// conversion whose only job is to copy five fields across. So the route assembles
// `chrome.HeaderView`, `chrome.NavView` and `chrome.FooterView` directly, and what
// is declared here is only what has no chrome equivalent: the title, the `<h1>`,
// the map surface and the action bar.
//
// **No field in `DocumentView` may vary with the reader's device.** UI §3.7 makes
// every tier a CSS variant of one canonical DOM and ADR 0035 holds the document's
// bytes constant across `sp_ui` values, so a field carrying a tier, a form factor
// or a pointer type would be a field the stylesheet is supposed to own. The two
// lists `RailView` keeps (`Tiers`, `TabletTiers`, `LaptopTiers`) are the rule a
// *client* applies; nothing here resolves one.

import (
	"github.com/semiplane/semiplane/internal/web/components/chrome"
	"github.com/semiplane/semiplane/internal/web/components/ui"
)

// The strings the action bar is built from, and the three sentences it owns.
//
// Constants rather than literals in the template for the reason `KeysHint` and
// `LayerUnknown` are: UI §1.2's vocabulary rule is policed over rendered strings,
// and copy that lives in a template is copy the audit has to find by parsing
// markup. Each of these is also asserted directly, so a wording change is a test
// change rather than a silent one.
const (
	// ActionsTestID is the action bar itself.
	ActionsTestID = "play-actions"
	// ActionsLabel is the action bar's landmark name. It must differ from
	// "Campaign" (the navigation) and from "Primary" (the compact footer bar), or
	// §7.2's "both must carry distinguishing labels" fails and landmark navigation
	// becomes useless. On this document the footer's four-destination bar is
	// absent, so the second navigation here is this one.
	ActionsLabel = "Table actions"

	// TokenTestID and TokenControls are the Token chip: the control that opens the
	// Tokens sheet, which is the utilities rail.
	TokenTestID = "play-token"
	// TokenControls names the element the chip's `aria-controls` points at.
	//
	// `chrome.railID` is unexported and this is the same id, spelled here for the
	// same reason `components/chrome` spells `"nav"` where the skip link needs it:
	// the value is load-bearing in three places (the rail's own `id`, the skip
	// link's `href`, this `aria-controls`) and a constant in each package with a
	// test that compares them is cheaper than exporting a DOM id so one package
	// can read it.
	TokenControls = "rail"

	// RollTestID is the die sheet's panel. `ui.Dialog` derives the trigger's hook
	// from it (`RollTestID + "-trigger"`), so one constant is one thing to rename.
	RollTestID = "play-roll"

	// EndTurnTestID and ChatTestID are the two controls that render in the state
	// which already works, with their reason alongside.
	EndTurnTestID   = "play-end-turn"
	EndTurnReasonID = "play-end-turn-reason"
	ChatTestID      = "play-chat"
	ChatReasonID    = "play-chat-reason"
)

// The three sentences, and each one is the *whole* reason rather than a hint.
//
// UI §4.9: "End turn (enabled only on your turn, disabled with a stated reason)"
// and, one row down, "an absent control is worse than a disabled one with a
// reason". A reason that said "unavailable" would satisfy the shape of the rule
// while telling a reader nothing they could act on, so each sentence says what is
// missing and why pressing it does nothing.
const (
	// EndTurnReason is why the control does nothing.
	//
	// It is a fact about the protocol rather than about this reader's turn:
	// `realtime.Document` carries a revision, a paused flag and placements, and no
	// turn — so there is no op to send and no server-side fact from which "your
	// turn" could be derived. UI §4.9's dependency row is explicit that the answer
	// is to flag it and not to invent a client-side turn flag, which would be a
	// control whose state the server cannot check and a reader cannot trust.
	EndTurnReason = "Turns are not tracked on this table yet."

	// ChatReason is why the control does nothing.
	//
	// The chat surface is not in this document: its transport and its panel are a
	// different work item, and a button that opened an empty sheet would be a
	// focus stop that lies about what it does.
	ChatReason = "Chat is not connected to this table yet."

	// RollUnavailable is what the die sheet says when the campaign has no grammar
	// to offer.
	//
	// Reachable by three ordinary routes — a build with no gameplay system
	// registered, a campaign whose `system_id` is empty, and a system this build
	// cannot resolve — and each of them is a real state rather than a fault, which
	// is S-10.6's first row seen from the UI side. The sentence therefore names the
	// *notation* as the missing thing rather than telling a reader to install
	// something they may not control.
	RollUnavailable = "This campaign's gameplay system does not publish a roll notation."
)

// DocumentView is the whole table document.
type DocumentView struct {
	// Title is the `<title>` element's text, in UI §7.2's three-part form:
	// "Table — Campaign — Instance". Composed by the route, because the separator
	// and the fallbacks are the same ones the wiki route spells and this package
	// should not become a third copy of them.
	Title string

	// Header is the banner. `Connection` is `ConnectionLive` on this route and
	// never `ConnectionNone`, because the route's one live region is the header's
	// and §7.5's "no live region" rule is about routes that are not live.
	Header chrome.HeaderView

	// Nav is the campaign navigation. Its `Wiki` is nil on this route — see the
	// route's `navigation` — so §4.3's Wiki section is absent rather than empty.
	Nav chrome.NavView

	// Footer is the contentinfo. `Play` is set, which replaces the four
	// destinations with the table's own row (UI §4.2) and is why this document
	// never renders a "Primary" navigation landmark.
	Footer chrome.FooterView

	// Heading is the document's `<h1>`, and it is the campaign's name.
	Heading string

	// Scene is the map surface, or the designed empty state when there is none.
	Scene SceneView

	// Actions is the compact action bar (UI §4.9).
	Actions ActionBarView
}

// SceneView is the map surface, as `static/js/map/table.js` expects to find it.
//
// # Why every field is empty in the product today
//
// The client's contract is fixed and non-negotiable: `mount` **refuses** unless
// the surface carries `aria-hidden="true"` and a non-empty `data-map-src`, because
// an unlabelled graphic announced to assistive technology is a failure no amount
// of correct drawing fixes. So a surface rendered without an image would be a
// surface that throws on boot — taking nothing with it (the token list still
// works) but logging an error on every table, forever.
//
// And there is no image to give it. `domain.FrontMatter` carries no scene field,
// no asset route resolves "the campaign's current scene", and inventing a path —
// a blank `data:` URL, a guessed `/c/{slug}/assets/maps/…` — would satisfy the
// attribute check with a byte that fails to decode, which is a different failure
// in the same place. So the route leaves `Source` empty, the template renders
// §4.7's designed empty state instead of a surface, and the contract above is
// held by `TestTheMapSurfaceSatisfiesTheClientContract` over a **fixture** that
// does have an image.
//
// Filling this is one field on `domain.FrontMatter` plus the asset route that
// serves it; both are outside this work item, and both are named in the
// integration report rather than guessed at here.
type SceneView struct {
	// Source is the scene image's address. Empty renders the empty state and
	// **no `data-map-surface` at all**, because a surface the client refuses to
	// mount is worse than no surface: the error is on every table, every load.
	Source string
	// Width, Height and Grid are the scene's intrinsic size and grid step, in the
	// client's own units. All three are optional — the image is the authority on
	// its own size — so empty means "let the image say", not "unknown".
	Width  string
	Height string
	Grid   string
}

// ActionBarView is the four controls UI §4.9 fixes, in the order it lists them:
// Token · Roll · End turn · Chat.
//
// There is deliberately no field for a placement the reader is acting as, for a
// turn, or for a connection. Each of those is client state — the first two do not
// exist anywhere in `realtime.Document` — and a field the route could fill would
// be a field the server renders *wrong* on the first request, before any client
// has said anything.
type ActionBarView struct {
	// Roll is the die sheet's contents: the campaign's own grammar, or nothing.
	Roll RollView
}

// RollView is one system's notation, as the die sheet renders it.
//
// The four fields are `rules.Grammar`'s four, copied rather than imported, and
// `rules` is not this package's to depend on: the interface package holds no rule
// and no system, and a template taking `rules.Grammar` directly would be a
// template whose shape is fixed by a type that a gameplay system's own contract
// may grow. The route does the one conversion.
type RollView struct {
	// Notation names the notation — `d20`, `2d6-pool`. It is a label and nothing
	// here switches on it, for the same reason `rules.Grammar.Notation` does not.
	Notation string
	// Summary is one sentence describing the notation.
	Summary string
	// Example is a worked expression, shown so a player can see the shape before
	// typing one.
	Example string
	// Terms are the shapes a valid expression may take, in the system's own order.
	Terms []RollTerm
}

// RollTerm is one shape a valid expression may take.
type RollTerm struct {
	// Name identifies the term within the notation, e.g. `roll` or `pool`.
	Name string
	// Summary is one sentence about it. Empty renders the name alone rather than
	// an empty line.
	Summary string
}

// available reports whether the sheet has something to offer.
//
// The same predicate `rules.Grammar.Valid` applies — a notation *and* at least
// one term — and applied here as well as at the route on purpose: a route that
// forgot the check would hand this package a `Notation` with no terms, and the
// template's job is to render the honest empty state rather than a heading over
// an empty list.
func (view RollView) available() bool {
	return view.Notation != "" && len(view.Terms) > 0
}

// dialog is the die sheet as `ui.Dialog` takes it.
//
// Built here rather than at the route because the two halves of a dialog travel
// together for a reason `ui.DialogView` states: the trigger's `aria-controls` must
// name the panel's id, and two independently assembled halves are two answers to
// "which element does this button open".
func (view ActionBarView) dialog() ui.DialogView {
	title := "Roll"
	if view.Roll.available() {
		// The notation joins the title rather than replacing it: a reader with the
		// sheet open is reading one notation, and "2d6-pool" alone is a heading that
		// answers "which system" but not "what is this dialog for".
		title = "Roll — " + view.Roll.Notation
	}

	return ui.DialogView{
		ID:           RollTestID,
		TriggerLabel: "Roll",
		Title:        title,
		Description:  view.Roll.Summary,
		Body:         rollBody(view.Roll),
		TestID:       RollTestID,
	}
}
