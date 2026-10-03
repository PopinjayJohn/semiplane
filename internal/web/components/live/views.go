package live

// The live chrome's view models: what a fragment carries, and what the document
// that mounts the chrome carries.
//
// # Why these types and not `realtime`'s
//
// The reason `components/chat` and `components/play` both declare their own, and
// the sentence is worth repeating here because this package sits one level closer
// to the wire than either: a template must never be handed a value with fields
// nobody has decided a reader may see. So the decision to omit happens upstream,
// in the route, and **there is no `Secret bool` here for a template to forget to
// check** — a message that became a `chat.Message` has already been judged visible
// to this reader.
//
// # What a fragment may carry, and the reason it is so little
//
// A fragment crosses a connection with no `ETag`, no `If-Match` and no redaction
// step, which is why `internal/httpapi/events`' own header confines the stream to
// notices. A notice is a *shape*, not content: "Tobin rolled 17", "the watch is
// degraded", "it is Tobin's turn". Nothing here is a page, a file's contents, or
// anything with a path in it.
//
// One consequence is worth stating because it is easy to get backwards: a dice
// **result** crosses this stream — it is the content of the dice log — and no
// **log line** may carry one. AGENTS.md's "no event carries dice results" is
// about observability, and the distinction is enforced from both ends: the
// fragment carries the numbers to the reader, and `internal/httpapi/events`'s log
// lines carry the fragment's *kind* and nothing else. `TestNoLogLineCarriesARollOrAChatBody`
// is what holds the second half, because a log aggregator is the one place a
// `[!secret]` callout ends up when somebody is debugging.

import (
	"time"

	"github.com/semiplane/semiplane/internal/web/components/chat"
)

// InitiativeView is the order, as the tracker renders it.
//
// **No `Active` index and no `Focusable` flag**, and the absence is the point: the
// tracker's rows are not controls. UI §7.6 puts the interactive representation of
// a placement in the token list, and a tracker that re-rendered focusable rows
// every time the order changed would move focus under a reader — which is exactly
// what §7.5's first prohibition is about, arriving through the other door.
type InitiativeView struct {
	// Entries are the combatants, in initiative order, highest first. The order is
	// the row order a reader reads, so a caller that sorts it is deciding what the
	// tracker shows.
	Entries []InitiativeEntry

	// Turn is the placement whose turn it is, or empty when nothing has started.
	// Rendered as `aria-current="true"` on the matching row.
	Turn string

	// Empty states the condition, because a tracker with no combatants is a
	// designed surface (UI §4.7) and not a rendering accident.
	Empty string
}

// InitiativeEntry is one row of the tracker.
type InitiativeEntry struct {
	// PlacementID is the placement's id within its campaign, carried as
	// `data-placement` for the same reason `components/play`'s rows carry it: a row
	// no client can identify is a row no intent can be about.
	PlacementID string

	// Name is what the reader calls it — the game object's title. Required, and
	// not derived from the id for `components/play`'s reason: an id is a protocol
	// identifier and reads as nothing to a person.
	Name string

	// Initiative is its place in the order, as a reader reads a number. The
	// `components/play` counterpart is "7 of 7 hit points" rather than "7/7"
	// because a screen reader announces a slash as the word "slash".
	Initiative int
}

// DicePanelView is the dice log panel the document mounts: its history and its
// empty state.
//
// **No live region in this view model**, because the region is on the element the
// client patches into and this is the history beside it. The split is
// §7.5's replay rule and `components/chat`'s argument for it: history arrives
// with the document inside an `aria-live="off"` list, and the live log is empty
// until something arrives.
type DicePanelView struct {
	// History are the rolls the reader can see, oldest first, already filtered for
	// this reader.
	History []DiceLineView

	// Empty states that nothing has been rolled, which is a table's normal state
	// and not an error.
	Empty string

	// Truncated reports that older rolls were dropped from what the document
	// carried. Shown, because a transcript that reads as complete and is not is
	// the failure `chat.PanelView.Truncated` names.
	Truncated bool
}

// DiceLineView is one roll.
//
// Five fields and no more, and the absence of a `Secret` flag is the security
// property: the route decides whether this reader may see the roll, and a roll
// that became a `DiceLineView` is one they may. There is nothing for a template
// to forget.
type DiceLineView struct {
	// Actor is who rolled.
	Actor string

	// Expression is what they asked for, as the system's own grammar spells it.
	// Rendered rather than re-parsed, so a reader sees the notation they typed
	// rather than one this package invented.
	Expression string

	// Total is the result the server computed. S-7.3: dice are evaluated
	// server-side and the client never supplies a result, so this number is the
	// only one in the document.
	Total int

	// Breakdown is the individual dice, in the order the system rolled them. Empty
	// for a roll that is not dice — a flat number with a modifier — and then the
	// row carries the expression alone rather than a fabricated breakdown.
	Breakdown []int

	// At is when it was rolled, in UTC.
	At time.Time
}

// ChatLineView is the chat fragment's payload.
//
// **A type alias for `chat.Message` rather than a copy**, which is what makes the
// row component single-owned: `live` patches `chat`'s own `MessageRow` into
// `chat`'s own container, so the escaping, the `data-seq` attribute, the time
// format and the class vocabulary all have exactly one answer. A copy would be a
// second renderer for one conversation, and the two would drift the first time
// either was restyled.
type ChatLineView = chat.Message

// NoticeKind is which degraded condition a notice is, and a closed set.
//
// Three values, and each one is a row of UI §9 that **the server** can know while
// the client is still connected. The connection's own loss is deliberately not
// among them — see `SocketHook`, and `announced.go`'s closing paragraph for why a
// patch target for it would be the one design that guarantees the notice does not
// arrive.
type NoticeKind int

const (
	// NoticeNone is not a notice, and is the zero value so a `NoticeView` that
	// forgets which condition it is renders nothing rather than a wrong sentence.
	NoticeNone NoticeKind = iota

	// NoticeDegradedWatch is §4.5's watcher in degraded mode: the campaign's
	// index is being maintained by a slow timer rather than by the watch, so what
	// the wiki shows may be behind. S-4.5.
	NoticeDegradedWatch

	// NoticeReconcileCapped is §4.7's secret reconciliation budget exhausted: a
	// secret is still hidden and the GM is entitled to know it stayed hidden.
	// Failing toward hiding is the invariant; this is the sentence saying so.
	NoticeReconcileCapped

	// NoticeStreamDegraded is the stream itself: the server cannot serve the
	// sidebar whole, which is the one condition under which "the table is current"
	// is false without anybody having done anything wrong.
	NoticeStreamDegraded
)

// noticeHeadings is the heading each condition gets.
//
// In the record's own words where it has them, and `NoticeStreamDegraded` is the
// only one with no record sentence — which is stated in its own entry rather than
// invented quietly.
var noticeHeadings = map[NoticeKind]string{
	NoticeNone:            "",
	NoticeDegradedWatch:   "This campaign's pages are not being watched",
	NoticeReconcileCapped: "A secret stayed hidden",
	NoticeStreamDegraded:  "This table is not fully up to date",
}

// NoticeKinds returns the declared conditions, in the order a reader meets them.
//
// A function rather than a package-level slice, for the reason `Targets()` returns
// one rather than holding one: a shared mutable list is a second answer to a
// question every caller would have to trust.
func NoticeKinds() []NoticeKind {
	return []NoticeKind{NoticeDegradedWatch, NoticeReconcileCapped, NoticeStreamDegraded}
}

// String names a condition for a failure message.
func (kind NoticeKind) String() string {
	if heading, known := noticeHeadings[kind]; known && heading != "" {
		return heading
	}

	return "not a notice"
}

// Heading is the condition's heading text, or the empty string for a condition
// this package does not have.
func (kind NoticeKind) Heading() string {
	return noticeHeadings[kind]
}

// NoticeView is one degraded condition as the sidebar renders it.
type NoticeView struct {
	// Kind is which condition this is. Required: the fragment's copy and its
	// urgency both come from it, so a notice whose kind is `NoticeNone` has
	// nothing to say.
	Kind NoticeKind

	// Detail is the one line under the heading. Empty renders no paragraph, which
	// is the right outcome for a condition that needs no elaboration — §4.7's rule
	// that a section with nothing to say has nothing in it.
	Detail string
}

// AnnouncementView is one polite status line: a turn change, a hit-point change, a
// delta the server applied.
//
// One sentence and no more, and the reason is the target it is patched into. The
// announcement region is a `role="status"`, so **every** replacement of its
// contents is spoken; a fragment carrying a heading, a list and a paragraph would
// be read as a document, and the reader who wanted to know whose turn it is would
// be made to sit through the state of every token on the table.
type AnnouncementView struct {
	// Sentence is what is announced. Required: an announcement region patched with
	// nothing announces nothing, and a reader who was told nothing is worse off
	// than one who was told nothing new.
	Sentence string
}

// ChromeView is the sidebar as the play document mounts it.
//
// Three fields and no more, and **none of them is a device or a reader**: §13.5
// and ADR 0035 forbid a document varying by either, and a field carrying one is a
// field a route can fill — which is how a document starts varying by the thing the
// records rule out. The whole rule table for the tab defaults travels as an
// attribute (that is `components/play`'s `RailView.Defaults`, not this field).
type ChromeView struct {
	// EventsHref is where the event stream is, and it becomes the **only**
	// `data-init` that opens one. §7.5's second Task — "the play page opens exactly
	// one WS and one SSE" — is counted here rather than asserted in a comment, and
	// the two connection hooks are the elements a test counts.
	EventsHref string

	// SocketHref is the tabletop's WebSocket, read by the client from the one
	// element carrying `WebSocketHook`. Architecture §7's ~6-connection ceiling is
	// why there are two connections and not four: this is the structured one, and
	// the sidebar's fragments ride the other.
	SocketHref string

	// Initiative is the tracker panel.
	Initiative InitiativeView

	// Dice is the dice log panel.
	Dice DicePanelView

	// Notices are the degraded conditions, already judged for this reader. Empty
	// renders the empty region, which is what a healthy table looks like.
	Notices []NoticeView
}

// announcementRole and the other region attributes, as the constants the
// components and the tests both read.
//
// Written out in the markup rather than computed from `Region`, because the
// attribute values are what a browser and a §10.2 audit read, and a computed
// value would make "the markup says `role="log"`" a claim about this file rather
// than about the document. `TestEveryTargetCarriesTheRegionItDeclares` holds the
// two together.
const (
	// StatusRole and AlertRole are the two assertive-or-not choices §7.5's table
	// names, and LogRole is the container `components/chat` argues for at length.
	StatusRole = "status"
	// LogRole is `role="log"`: a container whose implicit politeness is polite.
	// §7.5's table writes `role="status"` for the chat and dice rows;
	// `components/chat`'s header sets out why `role="log"` is the form that row
	// cannot be, and following the shipped code rather than adding a second
	// opinion is what keeps one chat log in this product.
	LogRole = "log"
	// AlertRole is `role="alert"`, assertive by default and written out anyway so
	// the gate and the next reader both see the attribute.
	AlertRole = "alert"

	// Polite and Assertive are the two politeness values in play.
	Polite    = "polite"
	Assertive = "assertive"

	// AdditionsRelevant is `aria-relevant="additions"` on a log: only an appended
	// line is announced, so replacing the list's own machinery cannot speak.
	AdditionsRelevant = "additions"

	// SilentLive is `aria-live="off"`, and it is written on the histories rather
	// than omitted. An element with no `aria-live` inherits the politeness of its
	// nearest live-region ancestor, and this chrome is mounted into a document that
	// has regions in it — so stating `off` is what makes "these are not announced"
	// a property of these elements rather than of wherever they were placed.
	SilentLive = "off"
)
