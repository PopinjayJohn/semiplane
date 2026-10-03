package secret

// The disclosure surface: the one place a `[!secret]` callout becomes a control,
// a state, or a line of prose a player can read.
//
// # What this package is, and what it deliberately is not
//
// UI §4.10 is the disclosure surface, and it has four parts. Two of them are
// **rendering** — the revealed and hidden callouts' treatments — and two are
// **controls**: the GM's reveal affordance in the editor, and the two alerts.
// This package owns all four, because all four are markup and the markup is what
// a screen reader sees.
//
// It does **not** own whether a secret is rendered. That decision is made on the
// *source*, before the render, by `internal/content`'s redactor (S-5.7, ADR
// 0029), and nothing here can influence it. That ordering is the whole security
// property: for a non-GM the bytes never reach a template, so no template can
// leak them by forgetting a branch. What this package decides is only what a
// reader sees **around** an omission, and what a GM is told about one.
//
// # THE ACCESSIBILITY DECISION, and why it is silence
//
// **A redacted secret is not announced to assistive technology, and nothing is
// left in its place.** Under the default, a page with a collapsed callout renders
// as though the callout had never been written: no element, no `aria-hidden`, no
// placeholder, no `role="presentation"`, no live region, no `data-` attribute, no
// comment. The player-facing document's accessibility tree is byte-for-byte what
// it would be on a page the GM never wrote a secret into.
//
// Three reasons, and the third is the one that decides it:
//
//  1. **Announcing is a disclosure, and a worse one than the stub.** §5.6.1 rules
//     out the stub because a placeholder tells a non-GM *that a secret exists
//     here*. "A GM-only note was removed" says the same thing more loudly, at the
//     exact position of the secret, to every reader rather than only to the ones
//     looking. It converts the one disclosure the campaign chose to make (visible
//     seam in the prose) into an oracle (an index of secret locations).
//  2. **The record already decided this, and it is right.** §4.10.1: "A redacted
//     secret is not announced to assistive technology, and nothing is left in its
//     place", and the rejected alternative is named. §5.6.1's reason is existence
//     and position. Silence is the only rendering that discloses neither.
//  3. **Silence is the only choice that is *equal*.** §4.10.1 states the
//     asymmetry as correct: "a sighted player may notice a seam in the prose
//     where a callout used to be, and a screen-reader user will not perceive it.
//     **That asymmetry is the correct behaviour under the default**". Making the
//     two match would require adding something to the tree for the screen-reader
//     user — and everything available to add is a disclosure. The asymmetry is not
//     a defect to be closed; it is what omission *means*.
//
// The consequence, stated rather than discovered: **a GM is the only reader who is
// told a secret exists.** The GM's editor shows the callout, its state, and its
// controls. A player sees the prose with a hole in it. §4.10.2's opt-in stub
// exists for campaigns that would rather close the hole, and it closes it for
// everybody at the cost of disclosing existence — which is a **campaign's** choice
// to make, not this package's.
//
// **This is enforced, not asserted in a comment.** `secret_test.go` builds the
// player-facing document through the real pipeline (`content.OmitSecrets` into
// `content.Renderer`, into the shell, into the editor's preview slot) and walks
// the parsed DOM looking for any trace of the callout: the element, the class, the
// attribute, the title text, the body text, an announcement, or a live region that
// fires. The negative controls feed it six ways of leaving a trace and require it
// to object to each.
//
// # The GM's reveal affordance is not a save
//
// §4.10.3: "Reveal is **not** a save." S-6.2/S-6.3 make `If-Match`
// load-bearing, and the reveal endpoint (`PUT /c/{slug}/secrets/{path...}`) is
// the editor's write path with one byte of the body replaced.
//
// So the control has its **own** action, its **own** `If-Match` value, and its
// **own** request, and — the part this package can actually assert — its **own
// element**. `RevealControl` renders a `<button>` carrying `data-secret-reveal`
// and `data-secret-href`; the editor's save is `data-editor-save` on a different
// button. There is no shared code path between them in this package, and
// `TestTheRevealControlIsNotTheSaveControl` asserts the two markup shapes are
// disjoint: a button carrying both hooks, or a reveal control inside the form
// the save posts, is a finding.
//
// **A reveal does not send the buffer and a save does not send a marker.** The
// reveal control's request is `{anchor, revealed}` against the page's own
// validator; the save's is the whole document. Carrying one on the other is the
// failure this separation exists to make impossible, and the reason the two
// controls are two components in two files rather than one component with a flag.
//
// # Why the alert is assertive and the status is not
//
// Two announcements, both the GM's, both `role="alert" aria-live="assertive"`,
// and the split between them and everything polite is §7.5's:
//
//   - **Reveal outcome.** The GM pressed a button that made something public to
//     every member. They need to know it took effect, now — a reveal that failed
//     silently is a secret the GM believes is public and a party that was never
//     told. §4.10.3 names it `role="alert"`.
//   - **Reconcile-capped.** S-5.10: the system *chose* to hide something, having
//     exhausted its reapplication budget. §4.10.3 calls it an error-level event
//     and says "never silently". A GM who does not hear it will keep believing a
//     secret is public.
//
// Both are assertive because both are **outcomes of an action the GM took or a
// failure the system chose**, and in each case "I am not sure what state my table
// is in" is the dangerous reading. A player has no such stake: for them the page
// is the page.
//
// # `Secret()` is a mount point, not a wrapper
//
// The integrator mounts these components; this package does not wrap the shell.
// `Disclosures` goes in the editor's preview pane, `CappedAlert` in the play
// document's assertive region, and `Revealed` on a revealed callout wherever the
// body is rendered. Three mount points, three components, no shared frame — a
// wrapper would have to be told which of the three it is, which is a field a
// template can be passed the wrong value for.

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// The three hooks the client script binds to. One per control, and each is a
// **different** name from the editor's save hook for the reason the package
// comment gives: a reveal is not a save and the markup says so.
const (
	// RevealHook is the attribute on the per-callout reveal button.
	RevealHook = "data-secret-reveal"
	// UnrevealHook is the attribute on the per-callout hide button. A separate
	// name rather than a value on RevealHook, because the script's two
	// behaviours differ (one is a disclosure and is confirmed, one is not) and
	// a hook that means "reveal=true" is a boolean where a name is clearer.
	UnrevealHook = "data-secret-unreveal"
	// RevealAllHook is the attribute on the page-level "reveal all" control.
	RevealAllHook = "data-secret-reveal-all"
	// ConfirmHook is the attribute on the confirmation control a reveal
	// requires (§4.10.3: "Confirm on reveal: it is visible to every member").
	ConfirmHook = "data-secret-confirm"
)

// EndpointTemplate is where a reveal request goes.
//
// A function rather than a constant so a caller cannot build one URL for every
// page in a campaign: the path is the page's, and a reveal aimed at the wrong
// page is a disclosure of the wrong thing.
const EndpointTemplate = "/c/%s/secrets/%s"

// Endpoint is the reveal URL for one page of one campaign.
//
// Written here rather than imported from `internal/httpapi/secrets` because that
// package's URL lives in its `Mount` pattern and there is nothing to import; a
// second implementation of "where does a reveal go" is the shape of bug this
// repository keeps recording, so the test
// `TestTheEndpointIsTheRouteTheRevealHandlerServes` compares this against the
// handler's own mounted pattern.
func Endpoint(slug, pagePath string) string {
	return fmt.Sprintf(EndpointTemplate, slug, pagePath)
}

// State is which way a callout's marker byte goes.
//
// A closed enum rather than `content.SecretState` itself, because a view model
// a route builds should not carry a byte the way a file does — and because
// `State` has a third value the byte cannot express. **Zero is
// `StateHidden`**, so a view model that forgot to say is *hidden*, which is the
// direction §5.6.2 requires every failure path to resolve toward.
type State int

const (
	// StateHidden is `[!secret]-`: the body is absent for a non-GM. The zero
	// value, and the reason is the safety direction rather than the frequency:
	// a page of callouts is mostly hidden, and a forgotten field must not
	// publish.
	StateHidden State = iota
	// StateRevealed is `[!secret]+`: the body is public.
	StateRevealed
)

// String names a state, for a `data-` attribute and for a failure message.
func (state State) String() string {
	switch state {
	case StateHidden:
		return "hidden"
	case StateRevealed:
		return "revealed"
	default:
		// A value outside the closed set. Named rather than rendered as a
		// number, for `content/secret.go`'s reason: the one thing a reader
		// needs to know about a state this package does not have is that it
		// is not one of them.
		return "unknown"
	}
}

// Marker is whether a callout's revealed marker is shown.
//
// D15 — the plan item this phase owns answering. §4.10.4 splits it: "on in the
// editor, off on the published page", and §14's open question 1 asks whether the
// published page should have it on by default.
//
// **The answer is no, and it is answered by making the marker a per-campaign
// setting whose default is off, with the editor overriding it to on.** Three
// things follow from that, and each is a decision rather than a default:
//
//   - The published page's marker is **off unless the campaign asks for it**.
//     §4.10.4 gives the reason: "Fiction breaks around a 'Revealed' label during
//     play", and a label a player reads is the one piece of GM bookkeeping that
//     leaks into the fiction. §4.12.1's protected-token list already refuses a
//     campaign `--callout-*` override, so the campaign cannot reach this through
//     the theme layer either — the setting is the only door, which is what makes
//     it a decision rather than an accident.
//   - The editor's marker is **on regardless**, because the editor is not the
//     fiction. A GM auditing a page needs to see which callouts are public at a
//     glance, and §4.10.4 says so directly: "a GM auditing wants it".
//   - **The marker is a text label, not a colour and not an icon.** §4.10.4:
//     "it is the one place the UI must be understood without sight *or* without
//     colour perception". So it is a real text node, and `TestTheMarkerIsAText
//     LabelAndNotOnlyAColour` asserts the word is in the rendered document.
//
// The zero value is `MarkerOff`, so a caller that forgets is off — which is the
// direction that discloses nothing to a player.
type Marker int

const (
	// MarkerOff is no marker. The zero value and the published page's default.
	MarkerOff Marker = iota
	// MarkerShown is the marker, as a text label inside the callout.
	MarkerShown
)

// CalloutView is one callout as the editor's preview and the disclosure controls
// render it.
//
// **No `Body` field, and its absence is the security property.** The callout's
// body is inside the rendered HTML the editor already holds — `EditorView.
// Preview` — and putting it on a view model here would create a second copy of
// secret text in a struct that could be logged, formatted, or compared. The same
// reasoning `components/live` gives for `Message` and `chat.Message`.
//
// What this carries is the *control* vocabulary: which callout, which way, and
// which endpoint. `Anchor` is §5.6.3's resolved name — a block id or twelve hex
// characters — and is the selector the reveal request carries. It is not secret:
// it is a key the ledger is keyed by, and the endpoint resolves it against the
// file rather than trusting it (`secrets.resolve`).
type CalloutView struct {
	// Anchor is §5.6.3's resolved name for this callout. Preferred over
	// `Ordinal` by the endpoint, and required here: a control that could only
	// name a position would rewrite the wrong callout's byte after an edit
	// above it, which is a disclosure of the wrong thing.
	Anchor string

	// Ordinal is the callout's position among the page's secrets. Carried for
	// the script's benefit and for the test hook; the request prefers the
	// anchor, and `RevealRequest` documents why.
	Ordinal int

	// Title is the callout's header line, or empty. **Rendered as the control's
	// accessible name suffix and shown to the GM, and never to a player** —
	// under the default there is no player-facing render of a collapsed
	// callout at all, which is the package's whole subject.
	Title string

	// State is which way the marker byte goes.
	State State
}

// Name is the callout's accessible-name suffix.
//
// The title when the author wrote one, and the ordinal otherwise, because a
// control named "Reveal" three times on a page is a control a screen-reader user
// cannot act on. The ordinal is a *position*, which is safe to speak for the one
// reader who is entitled to this surface.
func (callout CalloutView) Name() string {
	if title := strings.TrimSpace(callout.Title); title != "" {
		return title
	}

	return "secret " + strconv.Itoa(callout.Ordinal+1)
}

// RevealRequest is what a reveal control sends.
//
// A value rather than a set of data attributes scattered across a template, for
// the reason the package comment gives: a disclosure and a save are different
// requests to different endpoints with different bodies, and a struct is where
// that difference is visible. `Body` is the endpoint's `revealBody` and nothing
// else — `anchor`, `revealed` — because S-6.3's precondition lives in the header
// and a body carrying a validator would be a second answer to "what is this page
// currently".
type RevealRequest struct {
	// Method is always `PUT`. Written out rather than a constant read at the
	// call site so a test can assert the control's request is not a `POST`.
	Method string
	// URL is `Endpoint`'s output for this page.
	URL string
	// IfMatch is the page's validator, and the only precondition. Required:
	// a reveal without one is a 428 (RFC 6585), and a client that dropped the
	// header would believe it had revealed something.
	IfMatch string
	// Anchor and Ordinal name the callout. The endpoint prefers the anchor.
	Anchor  string
	Ordinal int
	// Revealed is which way the marker goes, and it is required by the wire
	// type — an absent `revealed` is a 400 rather than a default-to-hide,
	// because a mistyped request must not default into a silent change to the
	// GM's page.
	Revealed bool
}

// Body is the request's JSON body.
//
// Hand-built rather than `json.Marshal`ed from a struct so the field order and
// the exact set are visible here, and so the test can assert **the body carries
// no page content**: a reveal is one byte of one marker and nothing else, and a
// body carrying a buffer would be a save wearing a reveal's name.
func (request RevealRequest) Body() string {
	return fmt.Sprintf(
		`{"anchor":%s,"revealed":%s}`,
		strconv.Quote(request.Anchor),
		strconv.FormatBool(request.Revealed),
	)
}

// Outlet is where a fragment is patched into the document.
//
// The same shape as `live.Target`, deliberately: a route that had to learn two
// patch-target vocabularies would eventually write one hook name in the other's
// slot, and the failure is a patch into nothing. `Decide` is the only thing that
// says whether a fragment may be written here, and it is the same refusal set.
type Outlet struct {
	// Hook is the `data-chrome` value the element carries.
	Hook string
	// Selector is the attribute selector a patch names, derived from Hook by
	// `SelectorFor` so the two cannot disagree.
	Selector string
}

// The three outlets this surface writes into.
//
// Named rather than declared by a caller, for the reason `live.TargetByName`
// exists: an invented hook is a patch into nothing, silently. Each is a
// **different** element, so a reveal outcome and a reconcile-capped notice
// cannot overwrite one another.
const (
	// OutcomeHook is the assertive region a reveal's result is patched into.
	// Empty at load (§7.5's replay rule: a live region present on arrival has
	// not transitioned), so it is a target rather than a rendered alert.
	OutcomeHook = "secret-outcome"
	// MarkerHook is where a revealed callout's marker is patched. Not a live
	// region and not focusable: it is a text label the GM reads.
	MarkerHook = "secret-marker"
)

// ChromeAttribute is the attribute every patch target carries.
//
// `live.ChromeAttribute`'s value, spelled here rather than imported, for the
// reason `live`'s `ChromeAttribute` is a constant at all: one attribute, one
// vocabulary. A test asserts the two agree so a rename cannot reach only one.
const ChromeAttribute = "data-chrome"

// outlets returns this surface's patch targets.
//
// A function rather than a package-level slice: a shared mutable list is a second
// answer to a question every caller would have to trust.
func outlets() []Outlet {
	hooks := []string{OutcomeHook, MarkerHook}

	all := make([]Outlet, 0, len(hooks))

	for _, hook := range hooks {
		all = append(all, Outlet{Hook: hook, Selector: SelectorFor(hook)})
	}

	return all
}

// Outlets returns every element a fragment may be written into.
func Outlets() []Outlet { return outlets() }

// OutletFor returns the outlet with this hook, and whether it is one.
//
// The lookup a route uses, so a route names a hook rather than inventing a
// selector — the same reason `live.TargetByName` exists.
func OutletFor(hook string) (Outlet, bool) {
	for _, outlet := range outlets() {
		if outlet.Hook == hook {
			return outlet, true
		}
	}

	return Outlet{}, false
}

// SelectorFor is the CSS selector a hook names.
//
// An attribute selector, for `live.SelectorFor`'s reason: it is the only spelling
// that keeps a hook value out of a position where a character in it would change
// the selector's meaning.
func SelectorFor(hook string) string {
	return "[" + ChromeAttribute + "=\"" + hook + "\"]"
}

// Refusal is why a fragment was not written, and a closed set of strings for the
// same reason `live.Refusal` is one: an operator has to be able to match a log
// line, and a prose error message is not matchable.
type Refusal string

const (
	// RefusedUnknownOutlet is a fragment naming an outlet this package does not
	// declare — a caller built an `Outlet` by hand, which is what
	// `OutletFor` exists to prevent.
	RefusedUnknownOutlet Refusal = "unknown_outlet"
	// RefusedAnnouncedIntoSilent is a fragment carrying `role="alert"` aimed
	// at an outlet that is not one. The marker outlet is silent by
	// construction (§4.10.4: a marker is a text label, not an event), so a
	// fragment that announces into it is a bug in the fragment.
	RefusedAnnouncedIntoSilent Refusal = "announced_into_silent"
	// RefusedHoldsFocusStop is a fragment carrying an element a keyboard can
	// reach. Patching a focus stop into a region a GM may be reading puts a
	// control in their focus, and removing it later takes it away — §7.5's
	// "patches must never touch the focused element", from the payload's
	// side. **The marker and the outcome carry no focus stops**, and that is
	// enforced here rather than in a review.
	RefusedHoldsFocusStop Refusal = "holds_focus_stop"
)

// Decision is what the server concluded about one fragment.
type Decision struct {
	// Write reports whether the patch goes out.
	Write bool
	// Reason is why not, and empty when Write is true.
	Reason Refusal
}

// Decide is the server's decision about one fragment.
//
// **This is what makes the accessibility decision a property of the server
// rather than of a template.** The player's document is rendered by
// `internal/content`'s redactor and no fragment is ever patched into it — but the
// same seam serves the GM's editor, and the rule this function encodes is the one
// that keeps it from drifting into a channel that announces secrets. A fragment
// aimed at the marker outlet may not carry `role="alert"`, because the marker is
// a label a GM reads and an announcement would make a *page that re-renders*
// speak it (§7.5's replay failure).
//
// The fragment is parsed on every patch rather than trusted per family. A patch
// is at most a second and a fragment is a few hundred bytes, so the parse is
// microseconds against a socket write — and the alternative is trusting that a
// family of fragments never grew a live region, which a later change to one
// templ component would break with nothing failing.
func Decide(fragment Fragment) Decision {
	known, found := OutletFor(fragment.Outlet.Hook)
	if !found || known.Selector != fragment.Outlet.Selector {
		return Decision{Reason: RefusedUnknownOutlet}
	}

	// The split is per outlet and not a property of the fragment: the outcome
	// outlet *is* an alert region and the marker outlet is not, so an
	// announcement aimed at either is correct only at one of them. Declared
	// rather than derived from the markup so a fragment carrying no role is
	// allowed at both, which is the case that actually occurs.
	if announces(fragment.Markup) {
		if fragment.Outlet.Hook != OutcomeHook {
			return Decision{Reason: RefusedAnnouncedIntoSilent}
		}
	}

	root, err := html.Parse(strings.NewReader(fragment.Markup))
	if err != nil {
		// A fragment that will not parse cannot be reasoned about, and this
		// function's whole job is reasoning about it.
		return Decision{Reason: RefusedHoldsFocusStop}
	}

	if len(focusStopsIn(root)) != 0 {
		return Decision{Reason: RefusedHoldsFocusStop}
	}

	return Decision{Write: true}
}

// Fragment is one rendered patch: where it goes, what it is, and the markup.
//
// Three fields, because the first two are what the server *decides* and the third
// is what it *writes*. `Outlet` is data the fragment family declares, never one a
// route invents.
type Fragment struct {
	// Outlet is where the fragment goes.
	Outlet Outlet
	// Markup is the rendered DOM. Produced by a templ component and escaped by
	// it, so there is no escaping decision left to the caller.
	Markup string
}

// announces reports whether a fragment's markup carries a live region.
//
// **Parsed, not substring-matched.** `role="alert"` and `aria-live=` inside a
// text node, an attribute *name*, or an HTML comment would all answer `true` to
// `strings.Contains`, and this phase has made that mistake in three places
// already — a heading fixture that stopped violating its rule, a control-name
// audit that read subtree text, and a selector walk that read comments. The
// construct is the question; a word near it is not.
func announces(markup string) bool {
	root, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		// Unparseable markup is treated as announcing, which refuses the
		// patch. A fragment nobody can read is not one to write blind.
		return true
	}

	found := false

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		// **Always descend, and only inspect elements.** The root of a parsed
		// fragment is a *document* node, so an early return on
		// `node.Type != html.ElementNode` would stop before reaching the
		// first element and this would answer `false` for every fragment —
		// which is the failure mode of a gate wired to nothing, and the one
		// the mutation `TestTheMarkerOutletIsSilent` exists to catch.
		if node.Type == html.ElementNode {
			for _, attribute := range node.Attr {
				if attribute.Key == "role" && attribute.Val == "alert" {
					found = true
				}

				if attribute.Key == "aria-live" && attribute.Val != "off" {
					found = true
				}
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	return found
}

// focusStopsIn returns every focus stop in a parsed fragment.
//
// The same seven element kinds UI §10.6 names, plus this repository's own gate,
// and `<input type="hidden">` excluded for `live`'s reason: a hidden field is
// not a focus stop and refusing a fragment over one would be a rule a correct
// fragment could not satisfy.
func focusStopsIn(root *html.Node) []*html.Node {
	var found []*html.Node

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && isFocusStop(node) {
			found = append(found, node)
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	return found
}

// isFocusStop reports whether an element is one a keyboard can reach.
func isFocusStop(node *html.Node) bool {
	switch node.Data {
	case "a":
		return hasAttribute(node, "href")
	case "button", "select", "textarea", "summary", "iframe", "audio", "video":
		return true
	case "input":
		return !strings.EqualFold(attributeOf(node, "type"), "hidden")
	default:
		return hasAttribute(node, "tabindex")
	}
}

// hasAttribute reports presence, whatever the value — `tabindex="-1"` is
// programmatically focusable and §10.2's `aria-hidden` rule treats it as such.
func hasAttribute(node *html.Node, name string) bool {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return true
		}
	}

	return false
}

// attributeOf returns an attribute's value, or the empty string.
func attributeOf(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}

	return ""
}
