package realtime

// The wire vocabulary: §7.1's three client frames and six server frames, and the
// four rules that make a socket a hostile endpoint rather than a trusted one.
//
// # What this file is, and is not
//
// It is a *codec*. It moves frames across a byte boundary and it refuses frames
// it does not understand. It holds no game state, no connection, no campaign
// and no clock, and it does not know which operations a system resolves: which
// ops exist is the `System` registry's answer (§10.3), and a codec that
// enumerated them would need an edit from every plugin. The hub (`hub.go`) owns
// the connection and the authority; the in-memory `campaign_state` document is
// `state.go`'s.
//
// The one thing that does not belong to either is the campaign *state document*
// on a snapshot. `ServerSnapshot.State` is `json.RawMessage` for that reason and
// for exactly one reason: the codec transports it, it does not model it. If the
// codec declared the state document, then `state.go` and the wire format could
// not be changed independently, and every gameplay system would be a wire
// protocol change.
//
// # Four rules, and each one is a security boundary
//
//  1. **`seq` is the client's; `version` is the server's.** S-7.2 calls `version`
//     "the only ordering authority" and ADR 0009 calls it out as rule 2 of five.
//     They look like two counters and they are not interchangeable: `ClientSeq`,
//     `Since` and `Version` are three *distinct* named types, so converting one
//     into another is an explicit, greppable act. More to the point, **no inbound
//     frame has anywhere to put a `version`**: the client frames below contain no
//     `Version` field, and an unknown field is a rejection, so `{"t":"hello",
//     "since":1,"version":999999999}` is refused rather than believed. There is no
//     code path in this file that can produce a `Version` from client bytes.
//     (A test walks the inbound type graph and asserts both facts.)
//
//  2. **The client supplies no dice result.** S-7.3 and §7.1 are the same
//     sentence: "roll is evaluated server-side; the client never supplies a
//     result". An opaque `args json.RawMessage` would be a field in which a
//     result arrives, so there is none: every inbound payload is a *typed* struct
//     (`IntentArgs`, `PresenceArgs`), the fields are the parameters §7.1 itself
//     shows, and none of them is a result. An unknown field is rejected, so a
//     client that tries `{"result":20}` is told no rather than silently ignored.
//     (Two reflection tests: no inbound field is opaque, and no inbound field name
//     is a roll outcome.)
//
//  3. **A rejected frame's error carries no frame bytes.** AGENTS.md's last
//     security invariant: *a parser that quotes what it choked on will put a
//     `[!secret]` callout body into a log.* `encoding/json` quotes the offending
//     character in a syntax error and the offending literal in a type error, and
//     `DisallowUnknownFields` names the field it did not like — all three are
//     attacker-chosen bytes. So a rejected frame yields a `FrameError` whose text
//     is a *class*, the underlying `json` error is dropped rather than wrapped,
//     and no validation error interpolates a decoded value. A test plants a
//     secret-shaped string in every field that can hold one and requires it to
//     appear nowhere in `err.Error()`.
//
//  4. **The frame is bounded before it is decoded.** `ReadFrame` reads through a
//     cap and refuses anything larger, and `Decode` re-checks the length so a
//     caller holding a `[]byte` cannot skip the bound. A size check *after* the
//     decode is not a size check: it has already allocated whatever the frame
//     demanded. (A test uses a reader that panics if read past the cap, and an
//     oversize frame that is *valid* — so a decode that ran would have succeeded
//     and the test would fail.)
//
// # Unknown fields: rejected, not ignored
//
// `DisallowUnknownFields` is on, *and* the accepted key set is written out
// literally in `clientGrammar` so the wire grammar is one table a reader can hold
// in their head. Rejecting is right for three reasons, in order of weight:
//
//   - An unknown field is a client and a server that disagree about the protocol.
//     Ignoring it is how a protocol grows unnoticed: the field arrives, is dropped,
//     and both ends ship. A rejection is a visible, immediate failure.
//   - The dangerous shape of "ignore the unknown field" is exactly rule 2 above.
//     A future client that sends a roll *result* gets its result ignored by an
//     older server, and the GM's table shows a number nobody rolled. Refusing is
//     the only answer that cannot be mistaken for a rolled value.
//   - Forward-compatibility is not a property this protocol has. Both ends are the
//     same build of the same page (`make run` serves the client), so there is no
//     rolling upgrade for a lenient decoder to buy.
//
// # Two limits live here and two live in the hub, and they are not the same two
//
// The distinction matters because a limit enforced in one of two places is a limit
// one caller routes around (AGENTS.md, on the stylesheet import; the same
// argument).
//
//   - **Here, and only here:** frame size (`MaxClientFrameBytes` in, and
//     `MaxServerFrameBytes` on the way out), field count (the grammar allowlist),
//     token length and charset, counter range, required fields, and the closed
//     `RejectReason` vocabulary. All of them are properties of the bytes, and all
//     of them hold no matter which caller decodes.
//   - **The hub:** the transport's own read limit, the per-campaign connection
//     count, the per-connection frame rate, and the size of the state document
//     that goes into a snapshot. The transport's limit must be **larger** than
//     `MaxClientFrameBytes`, because a smaller one silently redefines the protocol
//     — the hub becomes the thing that decides a frame is too big, and this
//     constant stops meaning anything.
//   - **Not here, and deliberately:** the `Op` vocabulary. `validOpToken` checks
//     shape, never membership.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
)

// MaxClientFrameBytes is the largest inbound frame this codec will read or
// decode.
//
// The largest frame §7.1 can produce on the client→server side is an intent
// carrying a dice expression, which is tens of bytes; 16 KiB is two orders of
// magnitude of headroom and is still a bound a socket cannot make unbounded. The
// transport's read limit belongs to the hub and must be larger, for the reason
// given in the file comment.
const MaxClientFrameBytes = 16 << 10

// MaxServerFrameBytes is the largest outbound frame `Encode` will produce.
//
// A snapshot carries the whole `campaign_state` document, so this is the codec's
// one non-adversarial size limit and it is generous. It is enforced rather than
// trusted anyway, because an over-large encode is a fault in *this* project, and a
// fault refused at the boundary is one the transport never has to carry.
const MaxServerFrameBytes = 1 << 20

// maxToken is the longest short string the wire carries: an op name, a placement
// id, a rejection reason, a user id.
//
// It is a codec limit, not a hub one, because a token's length is a property of
// the bytes. The limit a *campaign* imposes on, say, a chat line is the hub's,
// and is a different limit for a different field.
const maxToken = 64

// maxText is the longest free text a client frame carries: a dice expression and
// its reason. 256 bytes is longer than any dice notation in any system this
// project ships, and a system that needs more belongs behind a system op with its
// own validation.
const maxText = 256

// maxCounter is the largest value any of `ClientSeq`, `Since` and `Version` may
// hold: 2^53, the largest integer a JavaScript `number` represents exactly.
//
// The bound is not a round number of convenience. The client is a browser
// (`§7.1`: templ renders the shell, PixiJS owns the canvas), and a version above
// 2^53 does not survive `JSON.parse` intact — so accepting one is accepting a
// value this project will hand to a client that cannot hold it, and the first
// symptom is an ordering bug at a campaign's 285th year rather than a rejection.
// A counter at this size is unreachable in every honest case: the frame would be
// megabytes of `seq` digits.
const maxCounter = 1 << 53

// maxArgsLen bounds the *serialised* `args` of an outbound frame, because
// `json.RawMessage` is opaque and an opaque field is unbounded by definition.
const maxArgsLen = 8 << 10

// Type is a frame's `t` discriminator.
//
// It is one type rather than two so that a log line, a metric label and a switch
// can all name a frame without knowing its direction; the direction is in which
// interface a frame satisfies, not in its name.
type Type string

// The nine frame types of §7.1.
const (
	// TypeHello is the client's opening frame, and the only one that may carry
	// `since`. It opens a connection; it is not a request-response step.
	TypeHello Type = "hello"
	// TypeIntent carries an operation the client wants applied.
	TypeIntent Type = "intent"
	// TypePresence carries a cursor or a focus, inbound. S-7.6: ephemeral, never
	// persisted.
	//
	// §7.1 uses the same `t` for the outbound roster, with a different body: the
	// client sends its own pointer, the server sends everyone's. One constant for
	// one wire value, and the two directions are separated by the Go types
	// (`ClientPresence` and `ServerPresence`), so the collision never reaches a
	// type switch.
	TypePresence Type = "presence"

	// TypeSnapshot is the whole state document, sent on connect or when a `since`
	// cannot be satisfied.
	TypeSnapshot Type = "snapshot"
	// TypeApplied answers one `seq` affirmatively, with the authoritative version.
	TypeApplied Type = "applied"
	// TypeRejected answers one `seq` negatively.
	TypeRejected Type = "rejected"
	// TypeDelta carries the changes a returning client missed.
	TypeDelta Type = "delta"
	// TypeClock carries the table's clock and whether it is paused.
	TypeClock Type = "clock"
)

// ClientSeq is the client's own sequence number, echoed in `applied` and
// `rejected` so a client can match the answer to the intent it sent and roll
// back its optimistic update (ADR 0009, rule 1).
//
// It is *not* an ordering authority and it is not comparable with a `Version`.
// These are three distinct named types precisely so that assigning one to
// another requires a conversion someone chose to write.
type ClientSeq uint64

// Since is a journal position a reconnecting client already holds, asked for in
// `hello` so the server can answer with a `delta` instead of a `snapshot`
// (§7.1's third bullet).
//
// It is deliberately *not* a `Version`. Versioning is per-placement (§7.2), so
// "p1 is at 7" and "p2 is at 3" are not comparable and there is no single number
// that answers "what have I missed". The journal position is that number: an
// index into the campaign's ordered log of applied changes, which answers the
// resync question and says nothing about any placement's currency. Conflating
// the two would either put a global counter back into the protocol — the thing
// §7.2 rejects — or let a client's `since` be read as a version, which is the
// forgery this type exists to prevent.
type Since uint64

// Version is the server's monotonic version for one placement, and per ADR 0009
// rule 2 the only ordering authority (S-7.2).
//
// "For one placement" is the whole point. A global counter would make two players
// moving unrelated tokens contend for it, so a version means nothing without the
// placement it belongs to — which is why every frame carrying a `Version` also
// carries a `PlacementID`.
//
// There is no way to obtain a `Version` from client bytes. This file has no code
// path that constructs one from a decoded frame, no inbound type has a `Version`
// field, and an unknown field is a rejection rather than a discarded value.
type Version uint64

// Op is an operation name: `move_token`, `roll`, `set_hp`, `pause` and whatever a
// system registers in due course (§10.3).
//
// It is a string and not an enum on purpose. The codec validates an op's *shape*
// — lower case, digits and underscores, bounded length — and never its membership,
// because membership is the system registry's answer and a codec that carried it
// would need an edit per plugin. An op this codec cannot vouch for is the hub's to
// answer with `RejectUnknownOp`.
type Op string

// PlacementID names a placement on the table: a token, a marker, anything with a
// position. §7.1's examples use `p1`.
type PlacementID string

// UserID is the wire form of `domain.User.ID`, used for `by` and for the
// recipient in a presence roster.
type UserID int64

// RejectReason is why the server refused an intent.
//
// It is a closed set rather than a free string, and the reason is not tidiness: a
// free string is a field a plugin's error can be poured into, and a plugin error
// quotes the thing it choked on. S-12.3 forbids an event carrying file contents
// and the same reasoning applies to the wire — this frame goes to a browser, where
// it is rendered. A closed vocabulary means the worst thing that can appear here
// is one of eight words the author of this file chose.
type RejectReason string

// The rejection reasons. `not_permitted` is deliberately coarse: S-8 answers "no
// access" without saying why, and a finer vocabulary would turn a rejection into
// an oracle.
const (
	// RejectNotYourTurn is a turn-order refusal.
	RejectNotYourTurn RejectReason = "not_your_turn"
	// RejectNotPermitted is the GM-only rule of §7.2 and the access matrix of S-8.
	RejectNotPermitted RejectReason = "not_permitted"
	// RejectUnknownOp is an op this campaign's systems do not resolve.
	RejectUnknownOp RejectReason = "unknown_op"
	// RejectInvalidArgs is an op whose parameters do not parse.
	RejectInvalidArgs RejectReason = "invalid_args"
	// RejectStaleVersion is a client acting on state it has not resynced.
	RejectStaleVersion RejectReason = "stale_version"
	// RejectOutOfOrder is an intent against a placement another change already
	// superseded.
	RejectOutOfOrder RejectReason = "out_of_order"
	// RejectNoSuchPlacement is an intent naming a placement that does not exist.
	RejectNoSuchPlacement RejectReason = "no_such_placement"
	// RejectServerError is the one reason that says nothing about what failed.
	RejectServerError RejectReason = "server_error"
)

// The rejection reasons, as the slice `Encode` checks membership against.
//
// A slice rather than a `map[RejectReason]struct{}` because it is written once
// and read once per rejected frame, and a map would be a global with a write
// lock for no gain.
var rejectReasons = []RejectReason{
	RejectNotYourTurn,
	RejectNotPermitted,
	RejectUnknownOp,
	RejectInvalidArgs,
	RejectStaleVersion,
	RejectOutOfOrder,
	RejectNoSuchPlacement,
	RejectServerError,
}

// ErrFrameRejected is the umbrella sentinel every rejection satisfies.
//
// A hub that wants one question — "was this frame the client's fault, or ours?"
// — asks `errors.Is(err, ErrFrameRejected)`. The class is on the `FrameError`
// when the hub wants the detail, for the reason given on `FrameError`.
var ErrFrameRejected = errors.New("realtime: frame rejected")

// Sentinels for the individual rejections. Each is also the `Unwrap` target of
// the `FrameError` that reports it, so a caller can ask one question.
var (
	// ErrClientFrameTooLarge is a frame over MaxClientFrameBytes.
	ErrClientFrameTooLarge = errors.New("realtime: client frame too large")
	// ErrServerFrameTooLarge is a frame over MaxServerFrameBytes.
	ErrServerFrameTooLarge = errors.New("realtime: server frame too large")
	// ErrNilReader is a nil io.Reader, which is a wiring fault and not a client's.
	ErrNilReader = errors.New("realtime: nil frame reader")
	// ErrNotJSON is a frame that is not a JSON object.
	ErrNotJSON = errors.New("realtime: frame is not a JSON object")
	// ErrMissingType is a frame with no `t`.
	ErrMissingType = errors.New("realtime: frame has no type")
	// ErrUnknownType is a `t` outside the three a client may send.
	ErrUnknownType = errors.New("realtime: unknown message type")
	// ErrUnknownField is a field outside the frame type's grammar.
	ErrUnknownField = errors.New("realtime: unknown field")
	// ErrMissingField is a required field that is absent.
	ErrMissingField = errors.New("realtime: missing field")
	// ErrMalformedFrame is a frame that did not decode. It covers a syntax error,
	// a field whose JSON type is wrong, and a field of the right type whose value
	// is nonsense.
	ErrMalformedFrame = errors.New("realtime: malformed frame")
	// ErrNegativeCounter is a negative `seq`, `since` or `version`.
	ErrNegativeCounter = errors.New("realtime: negative counter")
	// ErrCounterOutOfRange is a counter past maxCounter, or a counter written in a
	// form no client should use (`1e3`, `+7`, `7.0`).
	ErrCounterOutOfRange = errors.New("realtime: counter out of range")
	// ErrBadToken is a token that is empty, too long, or outside its charset.
	ErrBadToken = errors.New("realtime: malformed token")
	// ErrBadRole is a `Viewer.Role` that is not a role.
	ErrBadRole = errors.New("realtime: malformed role")
	// ErrMissingState is a snapshot with no state document.
	ErrMissingState = errors.New("realtime: snapshot carries no state")
	// ErrOversizeArgs is an outbound frame whose `args` exceed maxArgsLen.
	ErrOversizeArgs = errors.New("realtime: oversized args")
	// ErrUnknownReason is a `RejectReason` outside the closed set.
	ErrUnknownReason = errors.New("realtime: unknown reject reason")
)

// FrameError is what `Decode`, `ReadFrame` and `Encode` return for a refused
// frame.
//
// Its text is a **class**, never the frame's bytes and never the decoder's
// complaint. `encoding/json` puts the offending character in a `SyntaxError` and
// the offending literal in an `UnmarshalTypeError`, and `DisallowUnknownFields`
// puts the field name in a bare error — all attacker-chosen, and on a wiki page
// any of them can be a `[!secret]` callout body, which is S-12.3's exact subject.
// So the underlying error is dropped here rather than wrapped: `Unwrap` returns
// one of this file's sentinels, never a `*json.SyntaxError`.
//
// `Class` exists because `observability.errorClass` reduces an error to its
// dynamic type, which for a `*FrameError` would be `*realtime.FrameError` and
// would lose the distinction between "a client sent nonsense" and "a client sent
// too much". The class is an identifier this file chose, so it is safe to log
// verbatim — which is what a hub should do with it.
type FrameError struct {
	class string
	cause error
}

// Error returns the class and nothing else. It is content-free by construction,
// and a test plants secret-shaped strings in every field that can hold one and
// requires this to still read as a class.
func (e *FrameError) Error() string {
	return "realtime: frame rejected: " + e.class
}

// Unwrap returns the sentinel for the class, so `errors.Is` answers one question
// and no caller has to string-match this file's own error text.
func (e *FrameError) Unwrap() error { return e.cause }

// Class returns the short, content-free reason the frame was refused. It is one
// of: `too_large`, `oversize`, `nil_reader`, `not_json`, `missing_type`,
// `unknown_type`, `unknown_field`, `missing_field`, `malformed`, `json_type`,
// `negative_counter`, `counter_range`, `bad_token`, `bad_role`, `no_state`,
// `oversize_args`, `unknown_reason`.
func (e *FrameError) Class() string { return e.class }

// reject builds a `*FrameError` from a class and the sentinel for it.
func reject(class string, cause error) error {
	return &FrameError{class: class, cause: cause}
}

// classifyJSON reduces a decoder's error to a class.
//
// Two classes, not one, because they mean different things to whoever is on call:
// a `*json.SyntaxError` is a client speaking something that is not JSON at all,
// and a `*json.UnmarshalTypeError` is a client speaking JSON with a field of the
// wrong type — which is what a negative counter looks like before this file's own
// counter parsing sees it. Neither one's *text* is ever read.
func classifyJSON(err error) string {
	if hasJSONError[*json.SyntaxError](err) {
		return "malformed"
	}

	if hasJSONError[*json.UnmarshalTypeError](err) {
		return "json_type"
	}

	return "malformed"
}

// hasJSONError reports whether err is of the decoder error type T.
//
// It exists as a generic because the two decoder errors are the only ones the
// class depends on, and a helper parameterised by type keeps the two cases in
// `classifyJSON` to one line each — which is the point: a classification that
// takes four lines to read is one that gets edited carelessly.
func hasJSONError[T error](err error) bool {
	var target T

	return errors.As(err, &target)
}

// ClientFrame is a frame a client may send. The three are `ClientHello`,
// `ClientIntent` and `ClientPresence`, and the interface is **sealed**: no type
// outside this package satisfies it, because the marker method is unexported.
//
// Sealing is the type-level half of "reject an unknown message type"; `Decode` is
// the byte-level half, and both exist. A decoder that ignored a `t` it did not
// recognise would be a protocol that grows unnoticed — a client sending a
// `whisper` frame to a server that predates the feature gets silence rather than
// a refusal, and silence is indistinguishable from a dropped connection.
type ClientFrame interface {
	// frameType is the frame's `t`, and one half of the seal.
	frameType() Type
	// inbound is the other half: nothing outside this package can satisfy it, so
	// the set of frames a client may send is closed at the type level. `ServerFrame`
	// has its own marker, which is also what keeps the two interfaces distinct
	// rather than one shape with two names.
	inbound()
}

// ServerFrame is a frame the server may send. The six are `ServerSnapshot`,
// `ServerApplied`, `ServerRejected`, `ServerDelta`, `ServerPresence` and
// `ServerClock`, sealed for the same reason and with the same two halves.
type ServerFrame interface {
	// frameType is the frame's `t`, and one half of the seal.
	frameType() Type
	// outbound is the other half. Two markers rather than one shared method is
	// what lets a `ServerSnapshot` never be passed where a `ClientFrame` is
	// expected even though both carry a `Type`.
	outbound()
}

// ClientHello is the opening frame: `{"t":"hello","since":42}`.
//
// It may arrive once per connection, and a second one is not an error the codec
// has an opinion about — the hub answers it with a fresh snapshot or delta, which
// is the right answer for a client that reconnected and forgot it had.
type ClientHello struct {
	// Type is always TypeHello.
	Type Type `json:"t"`
	// Since is the journal position the client already holds, or nil for a client
	// that has nothing. Absent and zero are different frames in effect: absent is
	// always answered with a full snapshot, and zero may be answered with a delta
	// when nothing has been pruned. `Resync` is the one place that decides.
	Since *Since `json:"since,omitempty"`
}

// ClientIntent is an operation the client wants applied:
// `{"t":"intent","seq":7,"op":"move_token","args":{"placement":"p1","x":420,"y":180}}`.
//
// The client sends *intent*, never state (S-7.1, ADR 0009). What the server did
// with it arrives separately as `ServerApplied` or `ServerRejected`, and the
// `seq` is what pairs them.
type ClientIntent struct {
	// Type is always TypeIntent.
	Type Type `json:"t"`
	// Seq is the client's sequence number, and it is required: an intent with no
	// `seq` can never be answered, and a zero default would let it be silently
	// matched against the first thing the server applied.
	Seq ClientSeq `json:"seq"`
	// Op is the operation name.
	Op Op `json:"op"`
	// Args are the parameters, typed rather than opaque — see the file comment,
	// rule 2. Optional on the wire, because `pause` has none; a zero value is
	// indistinguishable from an absent one, which is correct for a struct whose
	// every field is optional.
	Args IntentArgs `json:"args"`
}

// IntentArgs are an intent's parameters: the flat union of what §7.1's own
// examples carry.
//
// It is flat and untyped on purpose, and the cost is stated here rather than
// discovered later: **adding a system whose operation needs a parameter this
// struct does not have is an edit to this file.** That is a real tax, and it is
// the right one for a public endpoint. The alternative — an opaque
// `json.RawMessage` — is a field in which a client may put anything at all,
// including the dice result S-7.3 forbids, and it also moves the boundary: an
// opaque payload would make *this* file the place where every argument is
// validated, which is not where any of them are validated today. A flat typed
// union keeps the codec responsible for the envelope and the resolver responsible
// for the arguments, and neither can be confused for the other.
//
// A field here is a field the codec will reject as unknown when absent from the
// struct, so a parameter cannot be added to the wire without adding it here.
type IntentArgs struct {
	// Placement is the placement an op acts on: `move_token`, `set_hp`.
	Placement PlacementID `json:"placement,omitempty"`
	// X and Y are a table position in points.
	X float64 `json:"x,omitempty"`
	Y float64 `json:"y,omitempty"`
	// HP is a hit point value, for `set_hp`.
	HP float64 `json:"hp,omitempty"`
	// Expr is a dice expression, for `roll`. The client may *ask* for a roll; it
	// never says what the roll was. There is no field for a result, by S-7.3.
	Expr string `json:"expr,omitempty"`
	// Reason is why the roll was asked for, shown in the recap.
	Reason string `json:"reason,omitempty"`
}

// ClientPresence is a cursor or a focus, inbound:
// `{"t":"presence","args":{"cursor":[120,88],"focus":"p1"}}`.
//
// S-7.6 and §7.2: presence is ephemeral and never persisted. Nothing in this
// frame reaches `campaign_state`, and the reason this codec has no field for it
// to reach is that there is nowhere else for it to go.
type ClientPresence struct {
	// Type is always TypePresence.
	Type Type `json:"t"`
	// Args are the cursor and focus. Optional: a presence frame with no args
	// clears them, which is how a client says it has stopped pointing at anything.
	Args PresenceArgs `json:"args"`
}

// PresenceArgs is a pointer and a focus.
//
// Both are optional, and the cursor is a pointer to a fixed-size pair rather than
// a slice: a slice would let a client send a 16 KiB cursor inside a frame that
// every other field of fits in 40 bytes.
type PresenceArgs struct {
	// Cursor is the reader's pointer position in table points, or nil.
	Cursor *[2]float64 `json:"cursor,omitempty"`
	// Focus is the placement the reader is looking at, or empty.
	Focus PlacementID `json:"focus,omitempty"`
}

// ServerSnapshot is the whole state document:
// `{"t":"snapshot","state":{…},"you":{"id":"u1","role":"gm"}}`.
//
// **It carries no top-level `version`, and that is a deliberate reading of §7.1
// rather than an omission.** The record's illustrative frame shows one, but S-7.2
// makes versioning per-placement, so there is no single number that describes "how
// current this snapshot is": `p1` at 7 and `p2` at 3 are not comparable, and a
// top-level `version` would have to be the global counter §7.2 rejects. The
// per-placement versions travel inside `State`, which is `state.go`'s document,
// and a client that needs to know whether *one placement* is current reads that
// placement's entry. `Since` is echoed so a client can tell which resync question
// this snapshot answered.
type ServerSnapshot struct {
	// Type is always TypeSnapshot.
	Type Type `json:"t"`
	// Since is the `since` that produced this snapshot, or nil when the client did
	// not ask — which is the case the server must answer with a snapshot whatever
	// the journal holds.
	Since *Since `json:"since,omitempty"`
	// State is the campaign state document, verbatim. The codec transports it and
	// does not model it: see the file comment.
	State json.RawMessage `json:"state"`
	// You is the recipient's identity and role, which is how a client learns what
	// it may do. The server is the only source for this, and a client that decided
	// it itself would be doing §7.2's authorisation client-side, which the record
	// calls cosmetic.
	You Viewer `json:"you"`
}

// Viewer is who a server frame is addressed to.
type Viewer struct {
	// ID is the recipient's `domain.User.ID`.
	ID UserID `json:"id"`
	// Role is the recipient's campaign role. It is `domain.Role`, so a role this
	// build does not have a name for is a role it refuses to send.
	Role domain.Role `json:"role"`
}

// ServerApplied answers one `seq` affirmatively:
// `{"t":"applied","seq":7,"version":44,"placement":"p1","op":"move_token","args":{…},"by":"u1"}`.
//
// `Args` is the *authoritative* form of what was applied, which is not necessarily
// what was asked for — the resolver's answer, not the request's. `Placement` is
// there because the `Version` beside it is a per-placement version and a version
// without its placement is a number about nothing.
type ServerApplied struct {
	// Type is always TypeApplied.
	Type Type `json:"t"`
	// Seq echoes the `ClientIntent.Seq` this answers.
	Seq ClientSeq `json:"seq"`
	// Version is the placement's new version, and the ordering authority.
	Version Version `json:"version"`
	// Placement is the placement Version belongs to.
	Placement PlacementID `json:"placement"`
	// Op is the operation that was applied.
	Op Op `json:"op"`
	// Args is what was actually applied, resolver-shaped and opaque. It is
	// `json.RawMessage` on purpose: outbound payloads are this project's own
	// bytes, produced by a system that knows its own shape, and modelling them
	// here would make every system a wire protocol change. The inbound side has no
	// such field, for the reason on `IntentArgs`.
	Args json.RawMessage `json:"args,omitempty"`
	// By is the user whose intent this was, for the audit trail.
	By UserID `json:"by"`
}

// ServerRejected answers one `seq` negatively:
// `{"t":"rejected","seq":7,"reason":"not_your_turn"}`.
//
// No version, because nothing was applied and so no placement moved. A rejection
// that carried a version would be a claim about currency it has not established.
type ServerRejected struct {
	// Type is always TypeRejected.
	Type Type `json:"t"`
	// Seq echoes the `ClientIntent.Seq` this answers.
	Seq ClientSeq `json:"seq"`
	// Reason is from the closed set. Never a plugin's error text.
	Reason RejectReason `json:"reason"`
}

// ServerDelta is what a reconnecting client receives instead of a snapshot:
// `{"t":"delta","since":42,"changes":[…]}`.
//
// Like `ServerSnapshot` it carries no top-level `version`, for the same reason:
// a set of per-placement versions has no maximum that means anything. Each
// `Change` carries its own placement and its own version, which is the only shape
// in which "here is what changed" and "here is how current it is" are the same
// information.
//
// There is no "changes since 42" that is allowed to be partial. `Resync` answers
// a `since` with either a delta that covers every change since it, or a snapshot
// that covers everything; see its comment for why the middle option is the worst
// of the three.
type ServerDelta struct {
	// Type is always TypeDelta.
	Type Type `json:"t"`
	// Since is the `since` this delta answers, or nil for a delta that is not a
	// resync answer — a live broadcast carries no `since`.
	Since *Since `json:"since,omitempty"`
	// Changes are the changes, each naming its placement and its version.
	Changes []Change `json:"changes"`
}

// Change is one applied change inside a delta.
type Change struct {
	// Placement is the placement this change moved. It is not optional: without it
	// the change has no address, and a delta addressed to nothing is a frame the
	// client must drop.
	Placement PlacementID `json:"placement"`
	// Version is that placement's new version.
	Version Version `json:"version"`
	// Op is what happened.
	Op Op `json:"op"`
	// Args is the resolver's payload for this change, opaque and bounded.
	Args json.RawMessage `json:"args,omitempty"`
	// By is the user it happened to.
	By UserID `json:"by"`
}

// ServerPresence is the roster, outbound: `{"t":"presence","users":[…]}`.
//
// It is every reader's pointer, and it is the whole of presence: S-7.6 makes
// presence ephemeral, so there is no state to carry in it and nothing here is
// persisted.
type ServerPresence struct {
	// Type is always TypePresence.
	Type Type `json:"t"`
	// Users is the roster.
	Users []PresenceUser `json:"users"`
}

// PresenceUser is one reader's pointer in a roster.
type PresenceUser struct {
	// ID is the reader.
	ID UserID `json:"id"`
	// Cursor is the reader's pointer position, or nil when they have none.
	Cursor *[2]float64 `json:"cursor,omitempty"`
	// Focus is the placement the reader is looking at, or empty.
	Focus PlacementID `json:"focus,omitempty"`
}

// ServerClock is the table's clock: `{"t":"clock","world_time":"2026-09-30T14:00:00Z","paused":false}`.
//
// The field name is `world_time` because that is §7.1's, verbatim, and a wire
// field is not interface copy: the vocabulary rule in `AGENTS.md` covers
// `aria-label`, `title`, empty states and error copy, and the §10.2 audit reads
// rendered documents. Renaming a wire field would break every client the record
// describes and buy nothing the rule asks for.
type ServerClock struct {
	// Type is always TypeClock.
	Type Type `json:"t"`
	// WorldTime is the table's in-fiction time.
	WorldTime time.Time `json:"world_time"` //nolint:tagliatelle // §7.1's wire field is world_time
	// Paused is whether the clock is stopped.
	Paused bool `json:"paused"`
}

// frameType implements ClientFrame.
func (f *ClientHello) frameType() Type { return TypeHello }

// inbound seals ClientFrame.
func (f *ClientHello) inbound() {}

// frameType implements ClientFrame.
func (f *ClientIntent) frameType() Type { return TypeIntent }

// inbound seals ClientFrame.
func (f *ClientIntent) inbound() {}

// frameType implements ClientFrame.
func (f *ClientPresence) frameType() Type { return TypePresence }

// inbound seals ClientFrame.
func (f *ClientPresence) inbound() {}

// frameType implements ServerFrame.
func (f *ServerSnapshot) frameType() Type { return TypeSnapshot }

// outbound seals ServerFrame.
func (f *ServerSnapshot) outbound() {}

// frameType implements ServerFrame.
func (f *ServerApplied) frameType() Type { return TypeApplied }

// outbound seals ServerFrame.
func (f *ServerApplied) outbound() {}

// frameType implements ServerFrame.
func (f *ServerRejected) frameType() Type { return TypeRejected }

// outbound seals ServerFrame.
func (f *ServerRejected) outbound() {}

// frameType implements ServerFrame.
func (f *ServerDelta) frameType() Type { return TypeDelta }

// outbound seals ServerFrame.
func (f *ServerDelta) outbound() {}

// frameType implements ServerFrame.
func (f *ServerPresence) frameType() Type { return TypePresence }

// outbound seals ServerFrame.
func (f *ServerPresence) outbound() {}

// frameType implements ServerFrame.
func (f *ServerClock) frameType() Type { return TypeClock }

// outbound seals ServerFrame.
func (f *ServerClock) outbound() {}

// grammar is one inbound message type's wire shape, written out.
//
// `fields` is the allowlist and the reason `DisallowUnknownFields` is belt and
// braces rather than the mechanism: an allowlist this project can read is one a
// client author can read too, and it is what makes "unknown field" a first-class
// rejection this file classifies rather than an error message it has to guess at.
// `newFrame` returns a pointer, because the sealed interfaces are satisfied by
// pointer receivers and that is what keeps the set closed.
type grammar struct {
	newFrame func() ClientFrame
	fields   map[string]struct{}
	required []string
}

// clientGrammar is §7.1's three client frames, spelled out.
//
// It is a table rather than a `switch` on `reflect.Type` so that the grammar is
// data: a test reads it and compares it against the struct tags, so an allowlist
// entry with no field (a field the server would silently drop) and a field with
// no allowlist entry (a field the server would refuse) are both failures.
var clientGrammar = map[Type]grammar{
	TypeHello: {
		newFrame: func() ClientFrame { return &ClientHello{} },
		fields:   set("t", "since"),
		required: []string{"t"},
	},
	TypeIntent: {
		newFrame: func() ClientFrame { return &ClientIntent{} },
		fields:   set("t", "seq", "op", "args"),
		required: []string{"t", "seq", "op"},
	},
	TypePresence: {
		newFrame: func() ClientFrame { return &ClientPresence{} },
		fields:   set("t", "args"),
		required: []string{"t"},
	},
}

// set builds a key set from literal names.
func set(keys ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		out[key] = struct{}{}
	}

	return out
}

// Decode reads one client frame from data.
//
// Every refusal is a `*FrameError` whose text is a class, for the reason on
// `FrameError`; there is no path through this function that returns an error
// mentioning anything the frame said.
//
// The order of the checks *is* the order of the function, and two of them are
// load-bearing:
//
//   - The length is checked first, so an oversize frame costs one comparison and
//     is never handed to a decoder that would allocate what it asked for.
//   - The frame is decoded into a `map[string]json.RawMessage` before its `t` is
//     read. That is what lets the discriminator be examined without trusting a
//     field order, and it is why `{"t":123}` is a type error rather than a frame
//     with an unknown type: `encoding/json` will not put an `int` into a `Type`.
//
// A duplicate key is not an error here. `encoding/json` keeps the last occurrence,
// and a frame is not authenticated at the frame level, so there is nothing for a
// duplicate to smuggle past; it is noted because it is the sort of thing a reader
// assumes has been thought about.
func Decode(data []byte) (ClientFrame, error) {
	if len(data) > MaxClientFrameBytes {
		return nil, reject("too_large", ErrClientFrameTooLarge)
	}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, reject(classifyJSON(err), ErrNotJSON)
	}

	if probe == nil {
		// `null` and `[]` unmarshal into a nil map without an error, and a nil map
		// is a frame with no fields, which is a frame with no `t`.
		return nil, reject("missing_type", ErrMissingType)
	}

	rawType, ok := probe["t"]
	if !ok {
		return nil, reject("missing_type", ErrMissingType)
	}

	var frameType Type
	if err := json.Unmarshal(rawType, &frameType); err != nil {
		return nil, reject(classifyJSON(err), ErrMalformedFrame)
	}

	shape, known := clientGrammar[frameType]
	if !known {
		// Not ignored, and not defaulted to a hello: a `t` this build does not
		// implement is a client and a server that disagree, and the disagreement
		// has to be visible.
		return nil, reject("unknown_type", ErrUnknownType)
	}

	if err := checkFields(probe, shape); err != nil {
		return nil, err
	}

	frame := shape.newFrame()
	if err := decodeStrict(data, frame); err != nil {
		return nil, reject(classifyJSON(err), ErrMalformedFrame)
	}

	if err := validate(frame); err != nil {
		return nil, err
	}

	return frame, nil
}

// checkFields enforces the allowlist and the required set against the frame's own
// keys, before any typed decode.
func checkFields(probe map[string]json.RawMessage, shape grammar) error {
	for key := range probe {
		if _, ok := shape.fields[key]; !ok {
			// The key is not in the message. It is deliberately not in this error:
			// it is client-chosen, and this file never interpolates one.
			return reject("unknown_field", ErrUnknownField)
		}
	}

	for _, key := range shape.required {
		if _, ok := probe[key]; !ok {
			return reject("missing_field", ErrMissingField)
		}
	}

	return nil
}

// decodeStrict decodes data into target with unknown fields refused and trailing
// bytes refused.
//
// The trailing check is the one `json.Unmarshal` would have done for free. It is
// done here anyway, because the decoder is what `DisallowUnknownFields` comes
// with, and `Decoder.Decode` stops at the end of the first value: without the
// check, `{"t":"hello"}{"t":"intent",…}` is two frames delivered as one message,
// and the second is never read. The socket is one message per frame and a
// message that holds two frames is two frames where the protocol has room for
// one.
func decodeStrict(data []byte, target ClientFrame) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(target); err != nil {
		return err
	}

	if decoder.More() {
		return errTrailingValue
	}

	return nil
}

// errTrailingValue marks a message carrying more than one JSON value.
//
// It is a sentinel so that `classifyJSON` needs no special case: it is not a
// `*json.SyntaxError` and not a `*json.UnmarshalTypeError`, so it lands on
// `malformed`, which is what it is.
var errTrailingValue = errors.New("realtime: more than one value in a frame")

// validate applies the semantic rules the JSON types cannot: token shapes,
// counter ranges and cross-field requirements.
//
// Each check returns a class and a sentinel and never a value, which is the same
// rule `FrameError` exists to enforce, applied at the point where decoded values
// are first in hand.
func validate(frame ClientFrame) error {
	switch frame := frame.(type) {
	case *ClientHello:
		return validateHello(frame)
	case *ClientIntent:
		return validateIntent(frame)
	case *ClientPresence:
		return validatePresence(frame)
	default:
		// Unreachable: `clientGrammar` builds the frame. It is here because a
		// `default` that falls through would return `nil` and silently accept a
		// frame nobody checked, and a codec's default branch must fail closed.
		return reject("unknown_type", ErrUnknownType)
	}
}

// validateHello checks a hello's optional `since`.
func validateHello(hello *ClientHello) error {
	if hello.Since == nil {
		return nil
	}

	if *hello.Since > maxCounter {
		return reject("counter_range", ErrCounterOutOfRange)
	}

	return nil
}

// validateIntent checks an intent's op and arguments.
func validateIntent(intent *ClientIntent) error {
	if !validOpToken(intent.Op) {
		return reject("bad_token", ErrBadToken)
	}

	if intent.Args.Placement != "" && !validPlacementToken(intent.Args.Placement) {
		return reject("bad_token", ErrBadToken)
	}

	if len(intent.Args.Expr) > maxText {
		return reject("bad_token", ErrBadToken)
	}

	if len(intent.Args.Reason) > maxText {
		return reject("bad_token", ErrBadToken)
	}

	if !finite(intent.Args.X) || !finite(intent.Args.Y) || !finite(intent.Args.HP) {
		// `encoding/json` refuses NaN and ±Inf on the way *in*, so this cannot be
		// reached from a decoded frame. It is on the outbound path, where a
		// resolver's float can be either, and `json.Marshal` would refuse it with
		// an error naming the value.
		return reject("bad_token", ErrBadToken)
	}

	return nil
}

// validatePresence checks a presence frame's cursor and focus.
func validatePresence(presence *ClientPresence) error {
	if presence.Args.Focus != "" && !validPlacementToken(presence.Args.Focus) {
		return reject("bad_token", ErrBadToken)
	}

	if presence.Args.Cursor != nil && !finite(presence.Args.Cursor[0]) &&
		!finite(presence.Args.Cursor[1]) {
		return reject("bad_token", ErrBadToken)
	}

	return nil
}

// Encode writes one server frame.
//
// It validates before it marshals, and it bounds the result, so a hub cannot
// write a frame this project considers malformed: an unknown `RejectReason`, a
// snapshot with no state, an op that is not a token, a role that is not a role.
// Refusing at the boundary is what keeps a server bug from becoming a client
// crash — and, for `RejectReason` in particular, what keeps a plugin's error text
// off the wire.
//
// The result is a single `[]byte` rather than a stream, because a frame is one
// JSON value and a WebSocket message is one frame.
func Encode(frame ServerFrame) ([]byte, error) {
	if err := validateServer(frame); err != nil {
		return nil, err
	}

	data, err := json.Marshal(frame)
	if err != nil {
		// Unreachable for a validated frame, and classified rather than passed
		// through for the same reason as everywhere else: a marshaller's error can
		// quote the value it choked on, and this project's outbound frames carry
		// resolver output.
		return nil, reject("malformed", ErrMalformedFrame)
	}

	if len(data) > MaxServerFrameBytes {
		return nil, reject("oversize", ErrServerFrameTooLarge)
	}

	return data, nil
}

// validateServer applies the outbound rules.
//
// The `default` branch refuses rather than accepts: a `ServerFrame` is sealed, so
// the only way to reach it is a `nil` typed pointer inside the interface, and a
// nil frame must not marshal into `null` and go out as one.
func validateServer(frame ServerFrame) error {
	switch frame := frame.(type) {
	case *ServerSnapshot:
		return validateSnapshot(frame)
	case *ServerApplied:
		return validateApplied(frame)
	case *ServerRejected:
		return validateRejected(frame)
	case *ServerDelta:
		return validateDelta(frame)
	case *ServerPresence:
		return validatePresenceOut(frame)
	case *ServerClock:
		return validateClock(frame)
	default:
		return reject("unknown_type", ErrUnknownType)
	}
}

// validateSnapshot checks a snapshot's state document, role and echoed `since`.
func validateSnapshot(snapshot *ServerSnapshot) error {
	if snapshot == nil || len(snapshot.State) == 0 {
		return reject("no_state", ErrMissingState)
	}

	if !snapshot.You.Role.Valid() {
		return reject("bad_role", ErrBadRole)
	}

	if snapshot.You.ID < 0 {
		return reject("bad_token", ErrBadToken)
	}

	return checkSince(snapshot.Since)
}

// validateApplied checks an `applied`'s op, placement, user and payload.
func validateApplied(applied *ServerApplied) error {
	if applied == nil || !validOpToken(applied.Op) {
		return reject("bad_token", ErrBadToken)
	}

	if !validPlacementToken(applied.Placement) {
		return reject("bad_token", ErrBadToken)
	}

	if applied.By < 0 || len(applied.Args) > maxArgsLen {
		return reject("bad_token", ErrBadToken)
	}

	return nil
}

// validateRejected checks a rejection's reason against the closed set.
func validateRejected(rejected *ServerRejected) error {
	if rejected == nil {
		return reject("unknown_type", ErrUnknownType)
	}

	if !validRejectReason(rejected.Reason) {
		return reject("unknown_reason", ErrUnknownReason)
	}

	return nil
}

// validateDelta checks every change in a delta, and the delta's placement
// addressing as a whole.
//
// The `Changes` slice is the reason this is a loop and not a check on the first
// element: a delta with one good change and one change naming no placement is a
// delta the client must partially drop, which is the outcome the whole resync
// design exists to prevent.
func validateDelta(delta *ServerDelta) error {
	if delta == nil {
		return reject("unknown_type", ErrUnknownType)
	}

	if err := checkSince(delta.Since); err != nil {
		return err
	}

	for i := range delta.Changes {
		change := &delta.Changes[i]

		if !validOpToken(change.Op) || !validPlacementToken(change.Placement) {
			return reject("bad_token", ErrBadToken)
		}

		if change.By < 0 || len(change.Args) > maxArgsLen {
			return reject("bad_token", ErrBadToken)
		}
	}

	return nil
}

// validatePresenceOut checks a roster's ids and cursors.
func validatePresenceOut(roster *ServerPresence) error {
	if roster == nil {
		return reject("unknown_type", ErrUnknownType)
	}

	for i := range roster.Users {
		user := &roster.Users[i]

		if user.ID < 0 {
			return reject("bad_token", ErrBadToken)
		}

		if user.Focus != "" && !validPlacementToken(user.Focus) {
			return reject("bad_token", ErrBadToken)
		}

		if user.Cursor != nil && !finite(user.Cursor[0]) && !finite(user.Cursor[1]) {
			return reject("bad_token", ErrBadToken)
		}
	}

	return nil
}

// validateClock checks a clock frame's echoed `since`.
func validateClock(clock *ServerClock) error {
	if clock == nil {
		return reject("unknown_type", ErrUnknownType)
	}

	return nil
}

// checkSince bounds an echoed `since`.
func checkSince(since *Since) error {
	if since != nil && *since > maxCounter {
		return reject("counter_range", ErrCounterOutOfRange)
	}

	return nil
}

// validRejectReason reports whether reason is in the closed set.
func validRejectReason(reason RejectReason) bool {
	return slices.Contains(rejectReasons, reason)
}

// validOpToken reports whether op is a well-formed operation name: lower case,
// digits and underscores, starting with a letter, no longer than maxToken.
//
// The charset is a security property as much as a naming one. An op name is used
// as a map key by resolvers, and a client that could send `"../../etc/passwd"` or
// a 4 KiB string of NULs would be a client choosing what a resolver does with an
// unrecognised name. Restricting the alphabet to `[a-z0-9_]` makes the name a
// token, so "not a known op" is the only thing that can happen with an unknown
// one.
func validOpToken(name Op) bool {
	if name == "" || len(name) > maxToken || name[0] < 'a' || name[0] > 'z' {
		return false
	}

	for i := 1; i < len(name); i++ {
		char := name[i]
		isLower := char >= 'a' && char <= 'z'
		isDigit := char >= '0' && char <= '9'

		if !isLower && !isDigit && char != '_' {
			return false
		}
	}

	return true
}

// validPlacementToken reports whether id is a well-formed placement id: printable
// ASCII with no space, no longer than maxToken.
//
// Wider than an op's charset because placement ids come from this project's own
// data rather than from a client's typing, but still bounded and still free of
// whitespace, because a placement id is concatenated into selectors and log
// lines elsewhere in the project and a value that can carry a space or a newline
// is a value that can carry a line break into a log.
func validPlacementToken(id PlacementID) bool {
	if id == "" || len(id) > maxToken {
		return false
	}

	for i := 0; i < len(id); i++ {
		char := id[i]
		if char <= ' ' || char >= 0x7f {
			return false
		}
	}

	return true
}

// finite reports whether a float is a number JSON can carry.
func finite(value float64) bool {
	return value == value && value-value == 0
}

// ReadFrame reads one client frame from r, bounding it before it is decoded.
//
// The bound is applied to the *read*, not to the decoded value, and that is the
// only order that bounds anything: `io.ReadAll` on an unbounded reader allocates
// whatever the peer says, and a peer on a socket decides how much that is. The
// reader is wrapped at `MaxClientFrameBytes + 1` bytes — one byte past the limit,
// so an oversize frame is *detectable* rather than merely truncated — and then
// `Decode` re-checks the length for the caller who already holds a `[]byte`.
//
// The context is honoured through `readFrameBytes`, so a cancelled hub stops reading
// rather than discovering the cancellation after the next frame. `io.ReadAll` has
// no context of its own, and a read that only checks `ctx` between frames is a
// read that cannot be interrupted mid-frame — which for a peer streaming an
// endless 16 KiB frame is the difference between a cancel and a hang.
func ReadFrame(ctx context.Context, r io.Reader) (ClientFrame, error) {
	if r == nil {
		return nil, reject("nil_reader", ErrNilReader)
	}

	data, err := readFrameBytes(ctx, r)
	if err != nil {
		return nil, err
	}

	return Decode(data)
}

// readFrameBytes reads at most `MaxClientFrameBytes + 1` bytes from r.
//
// The `+ 1` is what makes an oversize frame *detectable*: a limit that truncates
// silently turns an oversized frame into a malformed one, and the hub would then
// report a client bug for a client that sent a frame the codec simply could not
// accept.
func readFrameBytes(ctx context.Context, r io.Reader) ([]byte, error) {
	limited := io.LimitReader(
		readerFunc(func(p []byte) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, fmt.Errorf("realtime: read frame: %w", err)
			}

			count, readErr := r.Read(p)

			// `io.EOF` is `io.ReadAll`'s terminator, and it must arrive as the
			// sentinel itself: `io.ReadAll` compares it with `==`, so a wrapped copy
			// is not recognised and the read never finishes. Anything else is the
			// transport's error, wrapped so `errors.Is` still reaches its sentinel
			// and no frame byte can appear in it.
			var outErr error

			switch {
			case readErr == nil:
				outErr = nil
			case errors.Is(readErr, io.EOF):
				outErr = io.EOF
			default:
				outErr = fmt.Errorf("realtime: read frame: %w", readErr)
			}

			return count, outErr
		}),
		MaxClientFrameBytes+1,
	)

	data, err := io.ReadAll(limited)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// The cancellation is reported as itself rather than as the read's
			// error, which may be a closed socket the cancellation caused.
			return nil, fmt.Errorf("realtime: read frame: %w", ctxErr)
		}

		return nil, reject("malformed", ErrMalformedFrame)
	}

	return data, nil
}

// readerFunc adapts a function to `io.Reader`.
//
// It exists so the context check and the read share one stack frame without a
// struct carrying a `context.Context` — a reader that keeps a context in a field
// is a reader whose lifetime became its constructor's business, and
// `containedctx` is right to object. The context lives in the closure for the
// length of one read instead.
type readerFunc func(p []byte) (int, error)

// Read implements io.Reader.
func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// WriteFrame encodes frame and writes it to w in one call.
//
// One call, because a frame is one WebSocket message: two writes would be two
// messages, and a client that reads a message at a time would see a truncated
// JSON value and reject it — while a codec that had written it as two would
// report success. The size bound is enforced by `Encode`, so this cannot be the
// path that overruns the limit.
func WriteFrame(ctx context.Context, w io.Writer, frame ServerFrame) error {
	if w == nil {
		return reject("nil_reader", ErrNilReader)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("realtime: write frame: %w", err)
	}

	data, err := Encode(frame)
	if err != nil {
		return err
	}

	if _, err := w.Write(data); err != nil {
		// A transport error is not a frame error: it is the hub's to classify and
		// to close on. It is wrapped so `errors.Is` still reaches the transport's
		// own sentinel, and no frame byte is in it.
		return fmt.Errorf("realtime: write frame: %w", err)
	}

	return nil
}

// UnmarshalJSON decodes a client sequence number.
//
// It refuses anything that is not a bare JSON integer, which is why the negative
// and absurd cases are refusals *here* rather than something that fails later: a
// `seq` that became a wrapped-around `uint32` by the time the hub compared it to
// the last seq is an intent that would be silently reordered rather than
// rejected.
func (s *ClientSeq) UnmarshalJSON(data []byte) error {
	value, err := parseCounter(data)
	if err != nil {
		return err
	}

	*s = ClientSeq(value)

	return nil
}

// UnmarshalJSON decodes a journal position. See `ClientSeq.UnmarshalJSON`.
func (s *Since) UnmarshalJSON(data []byte) error {
	value, err := parseCounter(data)
	if err != nil {
		return err
	}

	*s = Since(value)

	return nil
}

// UnmarshalJSON decodes a placement version.
//
// It exists so that `Version` has the same strictness as the other two counters,
// and so that the reader of this file can see that nothing decodes a `Version`
// from anywhere but here — which no inbound frame reaches.
func (v *Version) UnmarshalJSON(data []byte) error {
	value, err := parseCounter(data)
	if err != nil {
		return err
	}

	*v = Version(value)

	return nil
}

// parseCounter decodes a bare JSON integer in `[0, maxCounter]`.
//
// The grammar is a subset of JSON's, deliberately: `encoding/json` would accept
// `7e0`, `7.0` and `+7` for an integer-typed field (well, not the last, but the
// first two), and a counter that can be written three ways is a counter whose
// log lines disagree about the same value. Rejecting the forms costs a client
// nothing and buys a protocol where one number has one spelling.
//
// The error carries the class and never `data`, which is client-chosen bytes.
func parseCounter(data []byte) (uint64, error) {
	if len(data) == 0 {
		return 0, reject("json_type", ErrMalformedFrame)
	}

	if data[0] == '-' {
		return 0, reject("negative_counter", ErrNegativeCounter)
	}

	// Anything other than a digit at this point is a sign, a point, an exponent,
	// a quote or a brace — a value in a form this counter does not have. Note
	// that `data` here is exactly the token `encoding/json` extracted, so leading
	// whitespace is already gone.
	for _, char := range data {
		if char < '0' || char > '9' {
			return 0, reject("counter_range", ErrCounterOutOfRange)
		}
	}

	// A counter with more digits than `uint64` has is refused by ParseUint, and
	// the error it returns mentions the input, so it is classified rather than
	// wrapped.
	value, err := strconv.ParseUint(string(data), 10, 64)
	if err != nil || value > maxCounter {
		return 0, reject("counter_range", ErrCounterOutOfRange)
	}

	return value, nil
}

// ResyncKind is how a `since` was answered.
type ResyncKind string

// The two answers. There is no third, and `TestResyncNeverAnswersAPartialDelta`
// is what holds that.
const (
	// ResyncSnapshot is a full snapshot: everything, from nothing.
	ResyncSnapshot ResyncKind = "snapshot"
	// ResyncDelta is every change since the client's position: complete for that
	// position, and nothing else.
	ResyncDelta ResyncKind = "delta"
)

// ChangeLog is the window of retained changes a `since` is answered against.
//
// It is the seam between this file and `state.go`, and it is an interface rather
// than a dependency for the reason the file comment gives for `State` being
// `json.RawMessage`: the codec owns the resync *rule* and the campaign owns the
// log. A hub implements this over its journal; a hub with no log answers
// `ResyncSnapshot`, because failing toward a snapshot is the only direction that
// is safe.
type ChangeLog interface {
	// Latest returns the journal position of the newest retained change.
	Latest() Since
	// Earliest returns the journal position of the oldest retained change, or zero
	// when nothing has been pruned.
	Earliest() Since
	// Pruned reports whether anything has been pruned from the front of the log.
	// It is separate from `Earliest` because zero is a legitimate position and
	// "nothing pruned" is a different fact.
	Pruned() bool
}

// Resync decides whether a client's `since` is satisfiable with a delta or must
// be answered with a snapshot.
//
// **There is no partial answer, and that is the point of the function.** A
// protocol with a third outcome — "here is the part of the gap I still have" —
// hands the client a state it cannot describe. The client would have to know which
// placements it missed and which it did not, which is precisely the question the
// per-placement version answers, and it cannot answer it for a change it was
// never shown. So the client's local state after a partial resync is a state no
// version describes, and the reconciler that is supposed to fix that up —
// "clients apply optimistically and reconcile on version mismatch", ADR 0009 —
// has nothing to reconcile against. The failure is silent and it is a wrong
// tabletop, which is the one outcome §7.2's server-authority rule exists to make
// impossible. A full snapshot is expensive and correct; a partial delta is cheap
// and silently divergent.
//
// So the rule is: a delta is sent only when every change the client missed is
// still retained.
//
//   - `since == nil` (the client sent no `since`) is always a snapshot. It asked
//     nothing, so there is nothing to be partial about.
//   - `since > Latest` is a snapshot: the client claims to hold something this
//     campaign has never produced, which is a client that was talking to a
//     different server or a stale local store, and either way it must be told the
//     truth from the beginning.
//   - `since == Latest` is a delta with no changes. It is the cheapest correct
//     answer and it is not the same as a snapshot.
//   - With a pruned log, `since` must be at or after `Earliest - 1`, because a
//     client at `since` holds positions up to `since` and the oldest change it can
//     still be missing is `since + 1`.
//
// A nil `log` answers `ResyncSnapshot`. A hub that wired no log should send the
// whole state, not nothing.
func Resync(log ChangeLog, since *Since) ResyncKind {
	if log == nil || since == nil {
		return ResyncSnapshot
	}

	if *since > log.Latest() {
		return ResyncSnapshot
	}

	if log.Pruned() && log.Earliest() > 0 && *since+1 < log.Earliest() {
		return ResyncSnapshot
	}

	return ResyncDelta
}
