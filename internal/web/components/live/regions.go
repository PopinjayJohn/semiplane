package live

// The live chrome's contract with the event stream: what a fragment may be
// patched into, what region it announces into, and the two decisions the server
// makes before it emits one.
//
// # Why the region is data and not markup
//
// UI §7.5's announced-content table is the specification for which region each
// thing goes in, and a fragment that disagrees with it is worse than no fragment:
// a `role="alert"` for a dice result interrupts a table mid-turn, and a chat line
// in the initiative track announces nothing a reader needed to hear. So the
// region is a field on each target **and** on the fragment's view model, and the
// server refuses a patch whose two disagree — see `Decide`. That is the whole of
// "a fragment cannot be rendered into the wrong region": it is a refusal on the
// server, not a review convention.
//
// The table itself lives in `announced.go`, as data, and a test asserts that
// every SSE row of §7.5's table is a target this file declares and that no
// WebSocket row is. That is what "one hub, two egress representations" means
// here: the sidebar's regions are the SSE half, and the canvas's are nobody's.
//
// # Why a target is an attribute selector and not an id
//
// The selector is data the **fragment family** declares, never one the route
// invents, so binding a fragment into a document is a one-line decision made by
// whoever owns that document rather than a rename in two places. `[data-chrome=
// "…"]` rather than `#id` for two further reasons:
//
//   - the chat log's container is declared by `components/chat`, which this work
//     item does not own, and it declares the hook as `data-chrome` — the comment
//     on `chat.LogChromeHook` says that attribute is "a promise to phase 9's
//     script", which is this package. Reading that constant rather than spelling
//     the string here is what keeps the promise to one owner;
//   - an id has to be unique in a whole document, and the live chrome is mounted
//     into a rail that another package is building. An attribute hook is unique
//     among elements carrying that attribute, which is a weaker and sufficient
//     condition: exactly one element per document is the claim, and
//     `TestEveryTargetAppearsExactlyOnceInTheChrome` is what holds it.
//
// # Why the focus decision is on the server
//
// "Patches must never touch the focused element" is a rule about the DOM, and the
// server is the only party that knows which DOM a patch will land in — it is the
// party that chose the selector. So the property is decided before the bytes are
// written, from two facts: the target's own focusability (a constant, proved
// against the rendered chrome by `TestNoPatchTargetIsFocusableOrHoldsAFocusStop`)
// and the fragment's contents (parsed here, every time). A fragment that would
// put a button, a link or a field inside a region somebody might be typing in is
// refused rather than written, because the alternative is a patch that removes a
// reader's caret with no way to put it back.

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/web/components/chat"
)

// The chrome hook names and the attribute that carries them.
//
// One attribute for every hook, and the value is the hook: `data-chrome` is the
// contract `components/chat`'s own comment sets out ("a promise to phase 9's
// script"), and reusing the spelling rather than inventing a second one is what
// keeps a document from carrying two vocabularies for one idea.
const (
	// ChromeAttribute is the attribute every patch target carries.
	ChromeAttribute = "data-chrome"

	// InitiativeHook is the initiative tracker's target. The value is the hook, so
	// a target's name, its attribute value and its selector are one fact written
	// once.
	InitiativeHook = "initiative-track"

	// DiceHook is the dice log's target.
	DiceHook = "dice-log"

	// AnnounceHook is the polite status region: the turn change, the hit-point
	// change, the delta applied.
	AnnounceHook = "live-announce"

	// NoticeHook is the assertive region: a degraded watcher, an exhausted secret
	// reconciliation, a stream the server cannot serve whole.
	NoticeHook = "live-notice"

	// SocketHook is the connection state, and **not** a patch target: the server
	// ships both of its sentences in the document and the client reveals one of
	// them. A client-authored string here would be a second place deciding what
	// a lost connection is called.
	SocketHook = "live-socket"

	// WebSocketHook is the one element the client reads the tabletop socket's URL
	// from. §7.5's second Task is "the play page opens exactly one WS and one
	// SSE", and this is the WS half, counted in the document.
	WebSocketHook = "live-ws"

	// EventStreamHook is the one element carrying the `data-init` that opens the
	// event stream. It is the SSE half of the same Task.
	EventStreamHook = "live-events"
)

// ChatLogHook is the chat log's target, read from the package that declares it.
//
// **`chat.LogChromeHook` and not a literal here**, for the reason every hook in
// this repository is a constant: a hook spelled two ways is a hook that appears
// twice, and §10.2's rule then fails a document for a reason nobody would guess.
// The chat log is the one target this package does not render, because
// `components/chat` does — and reading its constant is what makes "put anything
// new in components/live" true rather than aspirational.
const ChatLogHook = chat.LogChromeHook

// The patch modes, and the two this product uses.
//
// A closed list rather than a string, because the mode is half of what §7.5's
// focus rule is about: `outer` replaces the target element itself, which destroys
// whatever state the client attached to it, and `inner` is the only mode that can
// leave the container alone. Every target here is a container another package
// rendered.
const (
	// ModeInner replaces the target's children and leaves the target element in
	// place. The editor's notice has always used it and the reason is in
	// `internal/httpapi/events`.
	ModeInner = "inner"
	// ModeAppend adds the fragment to the target's children, which is how a log
	// grows. `append` rather than `prepend` so the newest line is the last child
	// and the scroll region's scroll position means what a reader expects.
	ModeAppend = "append"
)

// Region is where a fragment announces itself, and the closed set is UI §7.5's
// own vocabulary.
//
// Four values and no more, and the zero value is `RegionSilent` so a view model
// that forgets to say which region it belongs in is *refused* rather than
// announced politely into the wrong place — the failure being a fragment nobody
// hears is better than the failure being a dice result read as an emergency.
type Region int

const (
	// RegionSilent is not a live region at all, and is a first-class value
	// rather than "no value". UI §7.5's table has two rows with no region —
	// presence cursors and map-side state — and a type with no member for them
	// would let a fragment be written into the canvas's place.
	RegionSilent Region = iota

	// RegionStatus is §7.5's `role="status"` with polite politeness: the turn
	// change, the hit-point change, the delta applied, the editor's external-change
	// notice.
	RegionStatus

	// RegionLog is §7.5's chat and dice row as `components/chat` builds it — a
	// container whose implicit politeness is polite, with `aria-relevant="additions"`
	// so an appended line is announced as itself. The design record writes
	// `role="status"` here; `chat.templ` sets out at length why `role="log"` is
	// the container that form cannot be, and this package follows the code rather
	// than adding a second opinion to a decision a merged package already made.
	RegionLog

	// RegionAlert is `role="alert"` with assertive politeness: the degraded states
	// of §9 and the secret reconciliation of §4.7.
	RegionAlert
)

// String names a region, for a failure message.
func (r Region) String() string {
	switch r {
	case RegionSilent:
		return "silent"
	case RegionStatus:
		return "status"
	case RegionLog:
		return "log"
	case RegionAlert:
		return "alert"
	case RegionSilent - 1, RegionAlert + 1:
		// A value outside the closed set. Named rather than rendered as a number,
		// because the one thing a reader needs to know about a region this package
		// does not have is that it is not one of them.
		return "not a region"
	default:
		return "not a region"
	}
}

// Announces reports whether a region speaks.
//
// `RegionSilent` does not, and that is the whole of the property: a fragment
// announced into a silent region is drawn and never read, which is correct for
// the initiative tracker (its own `aria-current` says whose turn it is) and
// wrong for everything else.
func (r Region) Announces() bool {
	return r != RegionSilent
}

// Target is one element a patch may be written into.
//
// Five fields and no more. `Region` is the property §7.5's table decides, `Mode`
// is the property the focus rule depends on, `Focusable` is the claim the mounted
// document has to keep true, and `Name` and `Hook` are the identity a route and
// a document both refer to.
type Target struct {
	// Name is the target's own name, used in a failure message and by a caller
	// that wants to patch into it without spelling the selector.
	Name string

	// Hook is the `data-chrome` value the element carries.
	Hook string

	// Selector is the CSS selector the patch names. Derived from `Hook` by
	// `SelectorFor` rather than written out, because a selector and a hook value
	// that disagree is a patch that patches nothing — silently, since the client
	// finds no element and reports nothing.
	Selector string

	// Region is the region the target *is*, and the region a fragment patched
	// into it must declare. See the package comment.
	Region Region

	// Mode is the only patch mode this target accepts.
	Mode string

	// Focusable reports whether the element is itself a focus stop.
	//
	// **False for every target, and asserted rather than assumed.** A `true` here
	// is not a style choice: it makes `Decide` refuse every patch into this
	// target, so a target that is genuinely focusable cannot be patched at all.
	// That is the correct outcome — §7.5 says a patch must never touch the focused
	// element, and a target that can hold focus is one a reader can be inside.
	Focusable bool
}

// SelectorFor is the CSS selector a patch names for a chrome hook.
//
// An attribute selector, and the escaping is not decoration: a hook is a
// constant here, and an attribute selector is the only spelling that keeps the
// value out of a position where a character in it would change the selector's
// meaning.
func SelectorFor(hook string) string {
	return `[` + ChromeAttribute + `="` + hook + `"]`
}

// targets is the closed set of patch targets, in the order a reader meets them.
//
// A function rather than a package-level slice because the slice would be mutable
// state shared by every caller, and this repository has no second answer to any
// question (`internal/realtime`'s header is the statement of that rule). Building
// it per call costs nothing: a stream emits one patch a second.
func targets() []Target {
	hooks := []struct {
		name   string
		hook   string
		region Region
		mode   string
	}{
		{name: "initiative", hook: InitiativeHook, region: RegionSilent, mode: ModeInner},
		{name: "chat log", hook: ChatLogHook, region: RegionLog, mode: ModeAppend},
		{name: "dice log", hook: DiceHook, region: RegionLog, mode: ModeAppend},
		{name: "announcement", hook: AnnounceHook, region: RegionStatus, mode: ModeInner},
		{name: "notice", hook: NoticeHook, region: RegionAlert, mode: ModeInner},
	}

	all := make([]Target, 0, len(hooks))

	for _, entry := range hooks {
		all = append(all, Target{
			Name:      entry.name,
			Hook:      entry.hook,
			Selector:  SelectorFor(entry.hook),
			Region:    entry.region,
			Mode:      entry.mode,
			Focusable: false,
		})
	}

	return all
}

// Targets returns every element a patch may be written into.
//
// The whole set, and the reason the order is fixed rather than a map's is that
// `TestEveryTargetAppearsExactlyOnceInTheChrome` walks it and a test that reads
// a map would have no order to report a failure in.
func Targets() []Target {
	return targets()
}

// TargetByName returns the target with this name, or nil.
//
// The lookup a route uses, so that a patch names a target rather than a selector
// and a selector cannot be invented at a call site.
func TargetByName(name string) (Target, bool) {
	for _, target := range targets() {
		if target.Name == name {
			return target, true
		}
	}

	return Target{}, false
}

// Fragment is one rendered patch: where it goes, what it announces into, and the
// markup.
//
// Three fields because the first two are what the server *decides* and the third
// is what it *writes*. Keeping the decision and the payload in one value is what
// lets `Decide` be a function of the pair rather than a rule a caller remembers
// to apply before calling something else.
type Fragment struct {
	// Target is where the fragment goes.
	Target Target

	// Region is where the fragment announces. It is a field rather than a property
	// `Decide` reads off the markup, because the fragment's own element is
	// *inside* the region and the region is the element it is patched into: a
	// fragment that carried `role="status"` would be a second, nested live region
	// inside the one the document already declares.
	Region Region

	// Markup is the rendered DOM. Produced by a templ component and escaped by it,
	// so there is no escaping decision left to the caller.
	Markup string
}

// Refusal is why a patch was not written.
//
// A closed set of strings rather than an error value with prose, because the
// caller writes one of them into a log line and an operator has to be able to
// match it. Every refusal is a **server-side decision about what it emits**, and
// none of them ends the connection: a refused patch is a fragment this package
// chose not to write, and the next one may be fine.
type Refusal string

const (
	// RefusedUnknownTarget is a patch naming a target this package does not
	// declare. It means a caller built a `Target` by hand, which is the one thing
	// `TargetByName` exists to prevent.
	RefusedUnknownTarget Refusal = "unknown_target"

	// RefusedWrongRegion is a fragment whose declared region is not the region of
	// the target it goes into. §7.5's announced-content table, as a refusal.
	RefusedWrongRegion Refusal = "wrong_region"

	// RefusedFragmentHoldsFocusStop is a fragment carrying an element a keyboard
	// can reach. Inserting it into a region a reader may be interacting with puts
	// a new focus stop inside their focus, and removing it later takes that stop
	// away — §7.5's "patches must never touch the focused element", from the
	// other direction.
	RefusedFragmentHoldsFocusStop Refusal = "fragment_holds_focus_stop"

	// RefusedTargetFocusable is a target that is itself a focus stop, which makes
	// every patch into it a candidate for having destroyed `document.activeElement`.
	RefusedTargetFocusable Refusal = "target_focusable"

	// RefusedWrongMode is a fragment asking for a patch mode the target does not
	// accept. `outer` on a container another package rendered replaces the
	// container, and the state the client attached to it goes with it.
	RefusedWrongMode Refusal = "wrong_mode"

	// RefusedSilent is a fragment that announces into a silent target while
	// claiming to announce. The reverse — a fragment with no region into a
	// speaking target — is allowed, and is how a fragment updates a region
	// without saying anything itself.
	RefusedSilent Refusal = "silent_fragment_announced"
)

// Decision is what the server concluded about one fragment.
type Decision struct {
	// Write reports whether the patch goes out.
	Write bool

	// Reason is why not, and is empty when `Write` is true. One refusal at a
	// time, first in the order above, because a fragment that is both in the
	// wrong region and holding a focus stop has two bugs and one of them is enough
	// to act on.
	Reason Refusal

	// FocusStops names the elements that caused `RefusedFragmentHoldsFocusStop`,
	// so a log line says what was in the fragment rather than only that
	// something was.
	FocusStops []string
}

// Decide is the server's decision about one fragment, and it is where two of
// §7.5's four prohibitions are code rather than a promise.
//
// The order of the checks is the order a reader would want them reported in, and
// each refusal is a distinct bug:
//
//  1. **unknown target** — the caller invented a selector, so nothing downstream
//     can mean anything;
//  2. **wrong region** — §7.5's announced-content table, as a refusal;
//  3. **wrong mode** — `outer` on a container, which destroys the container;
//  4. **target focusable** — §7.5's focus rule, from the target's side;
//  5. **fragment holds a focus stop** — §7.5's focus rule, from the payload's.
//
// The last check parses the markup, and it parses on **every** patch rather than
// caching a verdict per fragment family. A patch is at most one a second
// (§7.5's throttle) and a fragment is a few hundred bytes, so the parse is
// microseconds against a socket write, and the alternative — trusting that a
// family of fragments never grew a button — is a rule that a later change to one
// templ component would break with nothing failing.
func Decide(fragment Fragment) Decision {
	known, found := TargetByName(fragment.Target.Name)
	if !found || known.Selector != fragment.Target.Selector {
		return Decision{Reason: RefusedUnknownTarget}
	}

	// A silent target may be patched by a fragment that announces nothing, and a
	// speaking one by a fragment that announces into it. A silent target patched
	// by a fragment that *claims* to announce is the bug the direction matters
	// for: the reader would hear a turn change from the initiative track, which
	// §7.5's table does not say happens.
	if fragment.Region != fragment.Target.Region {
		if fragment.Region.Announces() && !fragment.Target.Region.Announces() {
			return Decision{Reason: RefusedSilent}
		}

		return Decision{Reason: RefusedWrongRegion}
	}

	if fragment.Target.Mode != ModeInner && fragment.Target.Mode != ModeAppend {
		// Unreachable for the five declared targets; reachable for a hand-built
		// one, and refused rather than written.
		return Decision{Reason: RefusedWrongMode}
	}

	if fragment.Target.Focusable {
		return Decision{Reason: RefusedTargetFocusable}
	}

	root, err := html.Parse(strings.NewReader(fragment.Markup))
	if err != nil {
		// A fragment that will not parse is a fragment that cannot be reasoned
		// about, and this function's whole job is reasoning about it. Refusing is
		// the only answer that does not write bytes nobody has checked.
		return Decision{Reason: RefusedFragmentHoldsFocusStop}
	}

	stops := focusStopsIn(root)
	if len(stops) != 0 {
		return Decision{Reason: RefusedFragmentHoldsFocusStop, FocusStops: stops}
	}

	return Decision{Write: true}
}

// focusStopsIn returns a description of every focus stop in a parsed fragment.
//
// The same seven element kinds UI §10.6 names, plus the seven this repository's
// own gate walks, and `<input type="hidden">` excluded for the reason
// `plugins`' audit gives: a hidden field is not a focus stop and refusing a
// fragment over one would be a rule a correct fragment could not satisfy.
func focusStopsIn(root *html.Node) []string {
	var found []string

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && isFocusStop(node) {
			found = append(found, describeStop(node))
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	return found
}

// isFocusStop reports whether a keyboard can reach the element.
func isFocusStop(node *html.Node) bool {
	switch node.Data {
	case "a":
		return hasAttribute(node, "href")
	case "button", "select", "textarea", "summary", "iframe", "audio", "video":
		return true
	case "input":
		return !strings.EqualFold(attrOf(node, "type"), "hidden")
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

// attrOf returns an attribute's value, or the empty string.
func attrOf(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}

	return ""
}

// describeStop names one focus stop.
//
// **The tag name and nothing else, and that is a security decision rather than a
// terseness one.** An earlier version included the element's attributes and, for a
// short fragment, the fragment's own text — which is how a chat message body or a
// roll's expression reaches a log line through a refusal path. S-12.3 forbids an
// event carrying content, and a refusal is an event. The tag is enough to act on:
// "a fragment grew a `<button>`" names the fix, and the fragment is one `git diff`
// away from whoever needs to look.
func describeStop(node *html.Node) string {
	return "<" + node.Data + ">"
}

// TargetError is the error a fragment builder returns for a target it does not
// declare.
//
// An error rather than a zero `Fragment`, because a zero fragment patched into
// nothing is the silent failure this package exists to be unable to produce: the
// stream would emit an empty patch and the reader would see nothing, and the log
// line would say a patch went out.
type TargetError struct {
	// Name is the target the caller asked for.
	Name string
}

// Error implements `error`.
func (e *TargetError) Error() string {
	return fmt.Sprintf("live: %q is not a patch target; the targets are %s",
		e.Name, strings.Join(targetNames(), ", "))
}

// targetNames lists the declared targets, for the error message.
func targetNames() []string {
	names := make([]string, 0, 5)

	for _, target := range targets() {
		names = append(names, strconv.Quote(target.Name))
	}

	return names
}

// TargetByNameOrError returns a target or the error a builder should return.
//
// The shape a fragment builder wants, so that "look it up and complain" is one
// call rather than four lines repeated in five places — and so that the refusal
// happens while the fragment is being built, not when it is being written.
func TargetByNameOrError(name string) (Target, error) {
	target, found := TargetByName(name)
	if !found {
		return Target{}, &TargetError{Name: name}
	}

	return target, nil
}
