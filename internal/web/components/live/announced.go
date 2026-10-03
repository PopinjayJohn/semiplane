package live

// UI §7.5's announced-content table, as data, and the claim this package makes
// about which half this package serves.
//
// # Why the table is here and not only in a comment
//
// §7.5's table is the specification for which region each thing goes in, and it
// is a table *about two transports*. This file is the transport boundary made
// executable:
//
//   - every row whose transport is the event stream must name a region this
//     package declares, at the target `Targets()` declares — so the table and the
//     code cannot disagree without a test failing;
//   - every row whose transport is the WebSocket must name **no** target here, so
//     "one hub, two egress representations" is a property of the data rather than
//     an aspiration. Presence join/leave is a socket message because the protocol
//     already defines it there; presence cursors and map-side state are not
//     announced at all, because UI §7.6 makes the token list their accessible
//     equivalent and a canvas that announced itself would be a second, worse one.
//
// # The three rows this package deliberately does not serve
//
// §7.5's last row — "ruleset drift, game ended, connection lost | WebSocket |
// `role="alert"`" — is **absent from `Targets()`**, and its absence is asserted.
// Two of the three are the server refusing to serve (`ruleset_version` drift is
// §4.7's block on `/play`, and a game that ended is a state the document
// renders); the third is the *client's own* socket closing, which no server can
// announce to a client that is no longer listening. `SocketHook` exists for it and
// carries no patch target, because the sentences for both states ship in the
// document and the client only reveals one.
//
// That is a load-bearing distinction. A patch target for the connection state
// would mean the notice arrived over the channel whose loss is the condition —
// the one design that guarantees the notice that a reader needed most is the one
// that does not arrive.

// Transport is which of §7.5's two channels a row of the table rides.
type Transport int

const (
	// TransportNone is not a transport, and is the zero value so a row that
	// forgets says something an operator can see is wrong.
	TransportNone Transport = iota
	// TransportWebSocket is `/c/{slug}/ws`: structured, ordered state and client
	// intents. Consumed by PixiJS as data (architecture §7).
	TransportWebSocket
	// TransportEventStream is `/c/{slug}/events`: rendered DOM fragments, patched.
	TransportEventStream
	// TransportHTTP is the response to a request the reader made — the 412, which
	// is §7.5's one assertive row that is neither of the two streams.
	TransportHTTP
)

// String names a transport for a failure message.
func (t Transport) String() string {
	switch t {
	case TransportWebSocket:
		return "WebSocket"
	case TransportEventStream:
		return "SSE"
	case TransportHTTP:
		return "HTTP"
	case TransportNone:
		return "none"
	case TransportNone - 1, TransportHTTP + 1:
		return "not a transport"
	default:
		return "not a transport"
	}
}

// Announced is one row of §7.5's table.
type Announced struct {
	// Content is what the record announces, in §7.5's own words. Kept verbatim
	// because a test reads it and a paraphrase would make the test assert against
	// a sentence nobody wrote down.
	Content string

	// Transport is the channel the content arrives on.
	Transport Transport

	// Region is where it announces. `RegionSilent` for the two rows §7.5 marks
	// "no live region".
	Region Region

	// Target is the name in `Targets()` this row is patched into, and empty for
	// every row this package does not serve.
	Target string

	// Elsewhere reports that this row's target belongs to a package this one does
	// not own, and says which.
	//
	// **It is a field and not an omission**, because the two errors are opposites and
	// a test has to tell them apart. An SSE row naming a target this package does
	// not declare is normally a stale table or a missing target; the editor's notice
	// row is the one case where the target is real and somebody else's, and without
	// this field the table's assertion would have to carve out a sentence of prose to
	// do what a boolean does.
	Elsewhere string
}

// announcedContent is §7.5's table.
//
// Every row, in the record's order, with the two "no live region" rows present
// rather than omitted — a table holding only the rows this package serves would
// make "no target for the presence cursors" true by construction, and the test
// that asserts it would then be asserting nothing. The rows that are not this
// package's business are in the table precisely so the test can look at them.
var announcedContent = []Announced{
	{
		Content:   "Dice result, HP change, turn change, chat line, delta applied",
		Transport: TransportEventStream,
		Region:    RegionLog,
		Target:    "chat log",
	},
	{
		Content:   "Dice result",
		Transport: TransportEventStream,
		Region:    RegionLog,
		Target:    "dice log",
	},
	{
		Content:   "HP change, turn change, delta applied",
		Transport: TransportEventStream,
		Region:    RegionStatus,
		Target:    "announcement",
	},
	{
		Content:   "Presence join/leave",
		Transport: TransportWebSocket,
		Region:    RegionStatus,
	},
	{
		// §7.5 writes "**no live region** — visual only, `aria-hidden`". Carried as
		// `RegionSilent` with a target of "" so the absence is a row rather than a
		// gap in the data.
		Content:   "Presence cursors",
		Transport: TransportWebSocket,
		Region:    RegionSilent,
	},
	{
		Content:   "Map-side state",
		Transport: TransportWebSocket,
		Region:    RegionSilent,
	},
	{
		Content:   "Editor external-change notice",
		Transport: TransportEventStream,
		Region:    RegionStatus,
		// The editor's own notice container, declared by `internal/web/components/edit`
		// and patched by the editor's half of this route. It is a GM's right rail, not
		// the play surface's sidebar, and `live` deliberately does not declare it — a
		// second declaration would give one container two owners.
		Elsewhere: "internal/web/components/edit",
	},
	{
		Content:   "412 conflict on save or reveal",
		Transport: TransportHTTP,
		Region:    RegionAlert,
	},
	{
		Content:   "Secret reveal, reconcile-capped",
		Transport: TransportEventStream,
		Region:    RegionAlert,
		Target:    "notice",
	},
	{
		Content:   "Ruleset drift, game ended, connection lost",
		Transport: TransportWebSocket,
		Region:    RegionAlert,
	},
}

// AnnouncedContent returns §7.5's table.
//
// A fresh slice per call, for the reason `Targets()` returns one: a package-level
// slice a caller could sort in place would be a second answer to what §7.5 says,
// held in a variable every caller shares.
func AnnouncedContent() []Announced {
	return append([]Announced(nil), announcedContent...)
}

// The initiative tracker, and the row that is not in §7.5's table.
//
// It is here rather than in `announcedContent` because §7.5 does not list it: the
// table says "turn change" goes to the sidebar's polite status region, and the
// tracker is a *view* of the same fact rather than an announcement of it. A tracker
// that announced itself would read the whole order aloud every time the order
// changed, which is the replay failure §7.5's sentence is about wearing different
// clothes.
//
// So the tracker's target is `RegionSilent`, and what it offers a screen reader
// instead is `aria-current="true"` on the row whose turn it is — state a reader
// can ask about, rather than a sentence they are interrupted with. That is
// UI §7.6's whole argument about the canvas and the token list, applied to the
// tracker.
const initiativeContent = "Initiative order"

// InitiativeContent is the sentence above, exported so the test naming it does not
// have to restate it.
func InitiativeContent() Announced {
	return Announced{
		Content:   initiativeContent,
		Transport: TransportEventStream,
		Region:    RegionSilent,
		Target:    "initiative",
	}
}
