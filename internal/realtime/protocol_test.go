// The codec as a hostile endpoint: what it accepts, what it refuses, and what it
// refuses *without saying what it read*.
//
// Every rejection here is a `*realtime.FrameError` whose text is a class, and the
// assertions are on decoded values, on classes and on exact bytes — never on a
// substring of an error. A test that greps an error for the word it planted
// passes whenever the error happens to differ by a prefix; a test that requires
// the error to be exactly a class cannot.
//
// Two properties get more than the usual care because they are the ones this file
// exists for:
//
//   - **No input echo.** S-12.3 and `AGENTS.md`'s last security invariant: *a
//     parser that quotes what it choked on will put a `[!secret]` callout body into
//     a log.* Every fixture that can carry a string plants one, and the assertion
//     is that it appears in neither `err.Error()` nor any error in the chain —
//     including the chain, because a wrapped `*json.SyntaxError` is an echo that
//     `Error()` happens to hide today.
//   - **The size bound is before the decode.** `TestReadFrameRefusesAnOversizeFrame
//     WithoutReadingOrDecodingIt` uses a reader that panics if read past the cap
//     and a payload that is *valid JSON*, so a codec that decoded first would
//     either succeed — failing the test — or drain the reader, tripping the panic.
package realtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/realtime"
)

// secret is what a `[!secret]` callout body looks like once a parser has quoted
// the line it choked on. It is planted in every field that can hold a string.
const secret = "[!secret] The passphrase is hunter2"

// The six `RejectReason` values, so a test asserting a closed set has a set.
var everyReason = []realtime.RejectReason{
	realtime.RejectNotYourTurn,
	realtime.RejectNotPermitted,
	realtime.RejectUnknownOp,
	realtime.RejectInvalidArgs,
	realtime.RejectStaleVersion,
	realtime.RejectOutOfOrder,
	realtime.RejectNoSuchPlacement,
	realtime.RejectServerError,
}

// decodeClass returns the class of a rejection, failing if err is not one.
func decodeClass(t *testing.T, err error) string {
	t.Helper()

	if err == nil {
		t.Fatal("expected a rejection, got nil")
	}

	var frameErr *realtime.FrameError
	if !errors.As(err, &frameErr) {
		t.Fatalf("expected a *realtime.FrameError, got %T: %v", err, err)
	}

	return frameErr.Class()
}

// assertNoEcho requires that neither the error's text nor anything it wraps
// mentions needle.
func assertNoEcho(t *testing.T, err error, needle string) {
	t.Helper()

	if err == nil {
		t.Fatal("expected a rejection, got nil")
	}

	if strings.Contains(err.Error(), needle) {
		t.Fatalf("error echoed its input: %v", err)
	}

	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if strings.Contains(cause.Error(), needle) {
			t.Fatalf("wrapped error echoed its input at depth: %v", cause)
		}
	}
}

// TestTheThreeClientFramesDecodeToTheirDecodedValues is the round trip for §7.1's
// client side, asserted on the decoded values rather than on the bytes that
// produced them.
func TestTheThreeClientFramesDecodeToTheirDecodedValues(t *testing.T) {
	t.Parallel()

	t.Run("hello carries its since", func(t *testing.T) {
		t.Parallel()

		frame, err := realtime.Decode([]byte(`{"t":"hello","since":42}`))
		if err != nil {
			t.Fatalf("hello with since: %v", err)
		}

		hello, ok := frame.(*realtime.ClientHello)
		if !ok {
			t.Fatalf("want *ClientHello, got %T", frame)
		}

		if hello.Since == nil {
			t.Fatal("since was dropped")
		}

		if *hello.Since != 42 {
			t.Fatalf("since = %d, want 42", *hello.Since)
		}
	})

	t.Run("hello without since is not zero since", func(t *testing.T) {
		t.Parallel()

		frame, err := realtime.Decode([]byte(`{"t":"hello"}`))
		if err != nil {
			t.Fatalf("bare hello: %v", err)
		}

		hello, ok := frame.(*realtime.ClientHello)
		if !ok {
			t.Fatalf("want *ClientHello, got %T", frame)
		}

		// Absent and zero are different frames in effect: absent is always answered
		// with a snapshot, zero may be answered with a delta.
		if hello.Since != nil {
			t.Fatalf("since = %v, want nil", *hello.Since)
		}
	})

	t.Run("intent carries its op and args", func(t *testing.T) {
		t.Parallel()

		frame, err := realtime.Decode(
			[]byte(
				`{"t":"intent","seq":7,"op":"move_token","args":{"placement":"p1","x":420,"y":180}}`,
			),
		)
		if err != nil {
			t.Fatalf("move_token: %v", err)
		}

		intent, ok := frame.(*realtime.ClientIntent)
		if !ok {
			t.Fatalf("want *ClientIntent, got %T", frame)
		}

		if intent.Seq != 7 {
			t.Fatalf("seq = %d, want 7", intent.Seq)
		}

		if intent.Op != "move_token" {
			t.Fatalf("op = %q, want move_token", intent.Op)
		}

		if intent.Args.Placement != "p1" {
			t.Fatalf("placement = %q, want p1", intent.Args.Placement)
		}

		if intent.Args.X != 420 || intent.Args.Y != 180 {
			t.Fatalf("position = (%v,%v), want (420,180)", intent.Args.X, intent.Args.Y)
		}
	})

	t.Run("roll carries its expression and nothing else", func(t *testing.T) {
		t.Parallel()

		frame, err := realtime.Decode(
			[]byte(
				`{"t":"intent","seq":8,"op":"roll","args":{"expr":"1d20+5","reason":"perception"}}`,
			),
		)
		if err != nil {
			t.Fatalf("roll: %v", err)
		}

		intent, ok := frame.(*realtime.ClientIntent)
		if !ok {
			t.Fatalf("want *ClientIntent, got %T", frame)
		}

		if intent.Args.Expr != "1d20+5" || intent.Args.Reason != "perception" {
			t.Fatalf("args = %+v, want the expression and reason", intent.Args)
		}

		// S-7.3: nothing else arrived. Every numeric field is still zero, which is
		// what "the client never supplies a result" looks like in a decoded value.
		if intent.Args.HP != 0 || intent.Args.X != 0 || intent.Args.Y != 0 {
			t.Fatalf("a roll intent decoded a number it was not sent: %+v", intent.Args)
		}
	})

	t.Run("presence carries its cursor and focus", func(t *testing.T) {
		t.Parallel()

		frame, err := realtime.Decode(
			[]byte(`{"t":"presence","args":{"cursor":[120,88],"focus":"p1"}}`),
		)
		if err != nil {
			t.Fatalf("presence: %v", err)
		}

		presence, ok := frame.(*realtime.ClientPresence)
		if !ok {
			t.Fatalf("want *ClientPresence, got %T", frame)
		}

		if presence.Args.Cursor == nil {
			t.Fatal("cursor was dropped")
		}

		if presence.Args.Cursor[0] != 120 || presence.Args.Cursor[1] != 88 {
			t.Fatalf("cursor = %v, want [120 88]", *presence.Args.Cursor)
		}

		if presence.Args.Focus != "p1" {
			t.Fatalf("focus = %q, want p1", presence.Args.Focus)
		}
	})
}

// TestTheSixServerFramesEncodeToTheExactBytesOfTheRecord is the outbound round
// trip. The assertion is byte equality against §7.1's jsonc, because a client
// parses field names and a test that checked "contains" would pass on a renamed
// field and on a reordered one.
func TestTheSixServerFramesEncodeToTheExactBytesOfTheRecord(t *testing.T) {
	t.Parallel()

	since := realtime.Since(42)
	cursor := realtime.Cursor{120, 88}
	tableClock := time.Date(2026, time.September, 30, 14, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		frame realtime.ServerFrame
		want  string
	}{
		"snapshot": {
			frame: &realtime.ServerSnapshot{
				Type:  realtime.TypeSnapshot,
				State: json.RawMessage(`{"placements":{"p1":{"version":43}}}`),
				You:   realtime.Viewer{ID: 1, Role: domain.RoleGM},
			},
			want: `{"t":"snapshot","state":{"placements":{"p1":{"version":43}}},"you":{"id":1,"role":"gm"}}`,
		},
		"snapshot answering a since": {
			frame: &realtime.ServerSnapshot{
				Type:  realtime.TypeSnapshot,
				Since: &since,
				State: json.RawMessage(`{}`),
				You:   realtime.Viewer{ID: 2, Role: domain.RolePlayer},
			},
			want: `{"t":"snapshot","since":42,"state":{},"you":{"id":2,"role":"player"}}`,
		},
		"applied": {
			frame: &realtime.ServerApplied{
				Type:      realtime.TypeApplied,
				Seq:       7,
				Version:   44,
				Placement: "p1",
				Op:        "move_token",
				Args:      json.RawMessage(`{"x":420,"y":180}`),
				By:        1,
			},
			want: `{"t":"applied","seq":7,"version":44,"placement":"p1",` +
				`"op":"move_token","args":{"x":420,"y":180},"by":1}`,
		},
		"rejected": {
			frame: &realtime.ServerRejected{
				Type:   realtime.TypeRejected,
				Seq:    7,
				Reason: realtime.RejectNotYourTurn,
			},
			want: `{"t":"rejected","seq":7,"reason":"not_your_turn"}`,
		},
		"delta": {
			frame: &realtime.ServerDelta{
				Type:  realtime.TypeDelta,
				Since: &since,
				Changes: []realtime.Change{{
					Placement: "p1",
					Version:   45,
					Op:        "set_hp",
					Args:      json.RawMessage(`{"hp":7}`),
					By:        1,
				}},
			},
			want: `{"t":"delta","since":42,"changes":[{"placement":"p1","version":45,` +
				`"op":"set_hp","args":{"hp":7},"by":1}]}`,
		},
		"delta broadcast, carrying no since": {
			frame: &realtime.ServerDelta{
				Type: realtime.TypeDelta,
				Changes: []realtime.Change{{
					Placement: "p1",
					Version:   46,
					Op:        "set_hp",
					By:        1,
				}},
			},
			want: `{"t":"delta","changes":[{"placement":"p1","version":46,"op":"set_hp","by":1}]}`,
		},
		"presence": {
			frame: &realtime.ServerPresence{
				Type:  realtime.TypePresence,
				Users: []realtime.PresenceUser{{ID: 1, Cursor: &cursor}},
			},
			want: `{"t":"presence","users":[{"id":1,"cursor":[120,88]}]}`,
		},
		"clock": {
			frame: &realtime.ServerClock{
				Type:      realtime.TypeClock,
				WorldTime: tableClock,
				Paused:    false,
			},
			want: `{"t":"clock","world_time":"2026-09-30T14:00:00Z","paused":false}`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data, err := realtime.Encode(tc.frame)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}

			if string(data) != tc.want {
				t.Fatalf("bytes:\n got %s\nwant %s", data, tc.want)
			}
		})
	}
}

// TestAnOutboundFrameSurvivesEncodeDecode checks the outbound half against the
// inbound half, on the fields both halves share. A `version` and a `seq` that
// cannot cross the boundary are a protocol with two incompatible ends.
func TestAnOutboundFrameSurvivesEncodeDecode(t *testing.T) {
	t.Parallel()

	data, err := realtime.Encode(&realtime.ServerRejected{
		Type:   realtime.TypeRejected,
		Seq:    7,
		Reason: realtime.RejectNotYourTurn,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// A client would send `seq` back inside an intent; the value round-trips
	// through the same wire grammar in both directions.
	frame, err := realtime.Decode([]byte(fmt.Sprintf(`{"t":"intent","seq":%d,"op":"pause"}`, 7)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	intent, ok := frame.(*realtime.ClientIntent)
	if !ok {
		t.Fatalf("want *ClientIntent, got %T", frame)
	}

	if intent.Seq != 7 {
		t.Fatalf("seq = %d, want the 7 the server echoed back", intent.Seq)
	}

	if !strings.Contains(string(data), `"seq":7`) {
		t.Fatalf("encoded seq lost: %s", data)
	}
}

// wantClasses is every class the file can produce.
//
// It is written out rather than derived, because deriving it from the
// implementation would make the test agree with whatever the implementation
// happens to do. A class added to `protocol.go` and not to this list fails the
// build, which is the point: a class nobody has a fixture for is a class nobody has
// checked.
var wantClasses = []string{
	"malformed",
	"json_type",
	"missing_type",
	"unknown_type",
	"unknown_field",
	"missing_field",
	"bad_token",
	"bad_cursor",
	"negative_counter",
	"counter_range",
}

// TestEveryRejectionClassIsReachable is the meta-test. Each rejection in the file
// has a fixture that trips it, because `AGENTS.md` is explicit that a rule no
// fixture reaches is as useless as one that cannot fail — and because a
// classification table with a class no test produces is a class nothing produces.
//
// The table is also the assertion that a rejection is *distinguishable*: two
// distinct classes for the same mistake would make a class useless, and one class
// for two unrelated mistakes would make it a lie. Every entry below asserts
// `errors.Is` against the sentinel its class belongs to, so a class and a sentinel
// cannot drift apart without a test failing.
// TestTheTwoDirectionsAreNotInterchangeable is the sealed-interface property from
// the other side: nothing that satisfies `ClientFrame` satisfies `ServerFrame`, and
// vice versa. A frame type that satisfied both would let a hub hand a broadcast to
// the request dispatcher.
//
// The list is written out rather than reflected, so adding a ninth frame type
// without a row here fails the test rather than passing unnoticed.
func TestTheTwoDirectionsAreNotInterchangeable(t *testing.T) {
	t.Parallel()

	clientFrames := []realtime.ClientFrame{
		&realtime.ClientHello{Type: realtime.TypeHello},
		&realtime.ClientIntent{Type: realtime.TypeIntent},
		&realtime.ClientPresence{Type: realtime.TypePresence},
	}

	serverFrames := []realtime.ServerFrame{
		&realtime.ServerSnapshot{Type: realtime.TypeSnapshot},
		&realtime.ServerApplied{Type: realtime.TypeApplied},
		&realtime.ServerRejected{Type: realtime.TypeRejected},
		&realtime.ServerDelta{Type: realtime.TypeDelta},
		&realtime.ServerPresence{Type: realtime.TypePresence},
		&realtime.ServerClock{Type: realtime.TypeClock},
	}

	for _, frame := range clientFrames {
		if _, ok := any(frame).(realtime.ServerFrame); ok {
			t.Errorf("%T also satisfies ServerFrame", frame)
		}
	}

	for _, frame := range serverFrames {
		if _, ok := any(frame).(realtime.ClientFrame); ok {
			t.Errorf("%T also satisfies ClientFrame", frame)
		}
	}
}

// TestAnUnknownMessageTypeIsRefusedRatherThanGuessed is the other half of the
// seal: the decoder has no default branch that picks a type, so a `t` outside the
// grammar is an error and never a hello that happens to have no required fields.
//
// Both halves are asserted because either alone leaves a hole — a decoder that
// refused unknown `t` values but had a `default:` in its switch, or a sealed
// interface paired with a decoder that ignored the field.
func TestAnUnknownMessageTypeIsRefusedRatherThanGuessed(t *testing.T) {
	t.Parallel()

	for _, frameType := range []string{
		"",
		"HELLO",
		"Hello",
		"hello ",
		" hello",
		"snapshot",
		"applied",
		"whisper",
		"hello\n",
	} {
		_, err := realtime.Decode([]byte(fmt.Sprintf(`{"t":%q}`, frameType)))
		if err == nil {
			t.Fatalf("t=%q was accepted", frameType)
		}

		if got := decodeClass(t, err); got != "unknown_type" {
			t.Errorf("t=%q: class = %q, want unknown_type", frameType, got)
		}
	}
}

func TestEveryRejectionClassIsReachable(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		frame string
		class string
		want  error
	}{
		"an unterminated object": {
			frame: `{"t":"hello"`, class: "malformed", want: realtime.ErrNotJSON,
		},
		"an array rather than an object": {
			// Not a syntax error: the bytes *are* JSON, the shape is wrong. The
			// decoder calls that a type error and the class says so, rather than
			// claiming the client spoke something that was not JSON at all.
			frame: `["hello"]`, class: "json_type", want: realtime.ErrNotJSON,
		},
		"no t at all": {
			frame: `{"since":1}`, class: "missing_type", want: realtime.ErrMissingType,
		},
		"a t no client may send": {
			frame: `{"t":"whisper","text":"hi"}`,
			class: "unknown_type",
			want:  realtime.ErrUnknownType,
		},
		"a field outside the grammar": {
			frame: `{"t":"hello","colour":"red"}`,
			class: "unknown_field",
			want:  realtime.ErrUnknownField,
		},
		"an intent with no seq": {
			frame: `{"t":"intent","op":"pause"}`,
			class: "missing_field",
			want:  realtime.ErrMissingField,
		},
		"an op that is not a token": {
			frame: `{"t":"intent","seq":1,"op":"Move_Token"}`,
			class: "bad_token",
			want:  realtime.ErrBadToken,
		},
		"a t that is not a string": {
			frame: `{"t":7}`,
			class: "json_type",
			want:  realtime.ErrMalformedFrame,
		},
		"a counter that is a string": {
			// `{"seq":"7"}` reaches `ClientSeq.UnmarshalJSON`, which is handed the
			// quoted literal. A first-byte check that accepted a non-digit would let
			// `strconv` fail on it and report `counter_range` — a client bug reported
			// as a range violation, which is the wrong page in somebody's log, and a
			// counter is the one place the class tells an operator whether they are
			// looking at a broken client or at somebody probing.
			frame: `{"t":"intent","seq":"7","op":"pause"}`,
			class: "json_type",
			want:  realtime.ErrMalformedFrame,
		},
		"a cursor with three coordinates": {
			frame: `{"t":"presence","args":{"cursor":[1,2,3]}}`,
			class: "bad_cursor", want: realtime.ErrBadCursor,
		},
		"a negative seq": {
			frame: `{"t":"intent","seq":-1,"op":"pause"}`,
			class: "negative_counter", want: realtime.ErrNegativeCounter,
		},
		"a seq written as an exponent": {
			frame: `{"t":"intent","seq":7e0,"op":"pause"}`,
			class: "counter_range", want: realtime.ErrCounterOutOfRange,
		},
	}

	// The bookkeeping is done here rather than inside the parallel subtests, so
	// the reachability check below runs after every class has been claimed. Two
	// parallel subtests writing one map would be a race, and a reachability check
	// that reads a map another goroutine may still be writing is a check that
	// passes or fails by scheduling.
	// `seen` maps a class to the name of the first fixture that produced it. Several
	// causes legitimately share a class — `json_type` covers a `t` that is a number
	// and a `seq` that is a string, and the class is what tells an operator whether
	// they are looking at a broken client or a probe. So a class is claimed once and
	// may be reached again; what the loop below checks is that no class is claimed
	// *twice over* with a different expectation, which the per-entry `class` assertion
	// already covers.
	seen := map[string]string{}

	for name, tc := range cases {
		if _, claimed := seen[tc.class]; !claimed {
			seen[tc.class] = name
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := realtime.Decode([]byte(tc.frame))
			if err == nil {
				t.Fatal("expected a rejection")
			}

			if got := decodeClass(t, err); got != tc.class {
				t.Fatalf("class = %q, want %q", got, tc.class)
			}

			if !errors.Is(err, tc.want) {
				t.Fatalf("errors.Is(%v) is false: %v", tc.want, err)
			}

			if !errors.Is(err, realtime.ErrFrameRejected) {
				t.Fatal("every rejection must satisfy ErrFrameRejected")
			}
		})
	}

	for _, class := range wantClasses {
		if _, ok := seen[class]; !ok {
			t.Fatalf("class %q is declared but no fixture produces it", class)
		}
	}

	// Frames that reach a class the table already claims, each with its own
	// reason for being here.
	otherRejected := map[string]string{
		"a bare string":        `"hello"`,
		"null":                 `null`,
		"t not a string":       `{"t":7}`,
		"missing op":           `{"t":"intent","seq":1}`,
		"since is a bool":      `{"t":"hello","since":true}`,
		"two frames one msg":   `{"t":"hello"}{"t":"intent","seq":1,"op":"pause"}`,
		"trailing junk":        `{"t":"hello"} trailing`,
		"bad op leading digit": `{"t":"intent","seq":1,"op":"1move"}`,
		"bad op empty":         `{"t":"intent","seq":1,"op":""}`,
		"bad op with a space":  `{"t":"intent","seq":1,"op":"move token"}`,
		"bad placement":        `{"t":"intent","seq":1,"op":"move_token","args":{"placement":"p 1"}}`,
		"placement too long": `{"t":"intent","seq":1,"op":"move_token","args":{"placement":"` + strings.Repeat(
			"p",
			100,
		) + `"}}`,
		"negative since":       `{"t":"hello","since":-1}`,
		"absurd seq":           `{"t":"intent","seq":9007199254740993,"op":"pause"}`,
		"absurd since":         `{"t":"hello","since":99999999999999999999}`,
		"fractional seq":       `{"t":"intent","seq":7.5,"op":"pause"}`,
		"cursor too short":     `{"t":"presence","args":{"cursor":[1]}}`,
		"cursor not numbers":   `{"t":"presence","args":{"cursor":["1","2"]}}`,
		"cursor an object":     `{"t":"presence","args":{"cursor":{"x":1,"y":2}}}`,
		"focus with a space":   `{"t":"presence","args":{"focus":"p 1"}}`,
		"focus with a newline": `{"t":"presence","args":{"focus":"p` + "\n" + `1"}}`,
	}

	for name, frame := range otherRejected {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := realtime.Decode([]byte(frame))
			if err == nil {
				t.Fatalf("%s was accepted", frame)
			}

			if !errors.Is(err, realtime.ErrFrameRejected) {
				t.Fatalf("not a rejection: %v", err)
			}
		})
	}
}

// TestTheThreeCountersAreDistinctTypes is the compile-time claim restated as a
// runtime one: `ClientSeq`, `Since` and `Version` are three types, not one type
// with three names, so assigning one to another needs a conversion somebody
// wrote on purpose. S-7.2 and ADR 0009 rule 2 make `version` the only ordering
// authority, and two interchangeable counters are how a client's `seq` ends up
// compared against a placement's version.
func TestTheThreeCountersAreDistinctTypes(t *testing.T) {
	t.Parallel()

	types := map[string]reflect.Type{
		"ClientSeq": reflect.TypeFor[realtime.ClientSeq](),
		"Since":     reflect.TypeFor[realtime.Since](),
		"Version":   reflect.TypeFor[realtime.Version](),
	}

	seen := map[reflect.Type]string{}

	for name, typ := range types {
		if other, dup := seen[typ]; dup {
			t.Fatalf("%s and %s are the same type %v", name, other, typ)
		}

		seen[typ] = name

		if typ.Kind() != reflect.Uint64 {
			t.Fatalf(
				"%s is %v, want an unsigned integer so no negative can exist",
				name,
				typ.Kind(),
			)
		}
	}
}

// TestNoInboundFieldCarriesAVersion is the second half of S-7.2, and it is the
// half a comment cannot hold.
//
// A `hello` carrying a version is *rejected* — see
// `TestAClientSuppliedVersionIsRefused` — but a rejection test only proves the
// allowlist holds today. This walks the inbound type graph and requires that
// there is nowhere for a version to *land*, which means the same test still holds
// after someone adds a field.
func TestNoInboundFieldCarriesAVersion(t *testing.T) {
	t.Parallel()

	versionType := reflect.TypeFor[realtime.Version]()
	sinceType := reflect.TypeFor[realtime.Since]()

	for _, root := range inboundRoots() {
		walkStructs(root, func(structType reflect.Type, field reflect.StructField) {
			if field.Type == versionType {
				t.Fatalf("%s.%s is a Version: a client-supplied version could become authority",
					structType.Name(), field.Name)
			}

			name := strings.ToLower(field.Name + " " + strings.Split(field.Tag.Get("json"), ",")[0])
			if strings.Contains(name, "version") {
				t.Fatalf(
					"%s.%s names a version; no inbound field may",
					structType.Name(),
					field.Name,
				)
			}

			// `Since` is the journal position and must not be confused with a
			// version either — they are different numbers answering different
			// questions, and the type is what keeps them apart.
			if field.Type == sinceType && strings.Contains(name, "version") {
				t.Fatalf("%s.%s conflates since with version", structType.Name(), field.Name)
			}
		})
	}
}

// TestNoInboundPayloadIsOpaque holds S-7.3 at the type level.
//
// A `json.RawMessage` on an inbound type is a field a client may fill with
// anything, and it is the field a dice result would arrive in. The outbound types
// have three — resolver output is this project's own bytes — so the assertion is
// scoped to the inbound graph and says so.
func TestNoInboundPayloadIsOpaque(t *testing.T) {
	t.Parallel()

	rawMessage := reflect.TypeFor[json.RawMessage]()

	for _, root := range inboundRoots() {
		walkStructs(root, func(structType reflect.Type, field reflect.StructField) {
			if field.Type == rawMessage {
				t.Fatalf("%s.%s is a json.RawMessage: an inbound payload a client "+
					"controls, and therefore somewhere a roll result could arrive",
					structType.Name(), field.Name)
			}
		})
	}
}

// allowedInboundFields is every JSON field name an inbound frame may carry.
//
// **An allowlist rather than a denylist, and the difference is not stylistic.** A
// denylist of result-shaped names — `result`, `roll`, `total`, `value`, `outcome`,
// `score` — was the first draft, and a mutation check killed it: a field named
// `Sum` sails past a list of six words nobody thought of. The set of names a
// protocol legitimately carries is closed and known; the set of names a client
// might reach for is neither. So the test enumerates what *is* allowed, and adding
// a field is a deliberate act that requires adding it here.
var allowedInboundFields = []string{
	// Frame discriminators and the three envelope fields.
	"t", "seq", "op", "args", "since",
	// Presence.
	"cursor", "focus",
	// IntentArgs: the parameters §7.1's own examples carry. There is no result,
	// and no field whose name could be read as one.
	"placement", "x", "y", "hp", "expr", "reason",
}

// TestNoInboundFieldCouldHoldARollResult is S-7.3 by reflection, and it is the
// test the brief asks for: the inbound `intent` frame must have no field in which
// a result could arrive.
//
// The assertion is closed: every field name reachable from an inbound frame must
// be one this test lists. A field called `Result` in `IntentArgs` is a hole, and so
// is one called `Sum` — the second is the case a denylist misses.
func TestNoInboundFieldCouldHoldARollResult(t *testing.T) {
	t.Parallel()

	for _, root := range inboundRoots() {
		walkStructs(root, func(structType reflect.Type, field reflect.StructField) {
			if !field.IsExported() {
				return
			}

			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "" {
				name = field.Name
			}

			if !slices.Contains(allowedInboundFields, name) {
				t.Fatalf("%s.%s carries the wire field %q, which is not in the list of "+
					"fields a client frame may have. S-7.3 says the client never "+
					"supplies a result, and a closed list is what holds that: a "+
					"denylist of result-shaped names is a list of the names somebody "+
					"thought of.",
					structType.Name(), field.Name, name)
			}
		})
	}
}

// TestTheClosedListIsNotAStrangerThanTheStructs keeps the allowlist honest in the
// other direction: a name on the list that no field carries is a name a reader of
// the list would believe exists.
func TestTheClosedListIsNotAStrangerThanTheStructs(t *testing.T) {
	t.Parallel()

	present := map[string]bool{}

	for _, root := range inboundRoots() {
		walkStructs(root, func(_ reflect.Type, field reflect.StructField) {
			if !field.IsExported() {
				return
			}

			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "" {
				name = field.Name
			}

			present[name] = true
		})
	}

	for _, allowed := range allowedInboundFields {
		if !present[allowed] {
			t.Errorf("%q is on the closed list but no inbound field carries it", allowed)
		}
	}
}

// TestAClientSuppliedRollResultIsRefused proves the reflection test is not
// vacuous: the field the previous test forbids really is refused on the wire.
func TestAClientSuppliedRollResultIsRefused(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"result":           `{"t":"intent","seq":8,"op":"roll","args":{"expr":"1d20+5","result":20}}`,
		"result at top":    `{"t":"intent","seq":8,"op":"roll","result":20}`,
		"total":            `{"t":"intent","seq":8,"op":"roll","args":{"expr":"1d20+5","total":25}}`,
		"value":            `{"t":"intent","seq":8,"op":"roll","args":{"expr":"1d20+5","value":20}}`,
		"outcome":          `{"t":"intent","seq":8,"op":"roll","args":{"expr":"1d20+5","outcome":20}}`,
		"rolled":           `{"t":"intent","seq":8,"op":"roll","args":{"expr":"1d20+5","rolled":20}}`,
		"score":            `{"t":"intent","seq":8,"op":"roll","args":{"expr":"1d20+5","score":20}}`,
		"result as string": `{"t":"intent","seq":8,"op":"roll","args":{"expr":"1d20+5","result":"20"}}`,
	}

	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := realtime.Decode([]byte(frame))
			if err == nil {
				t.Fatal("a client-supplied roll result was accepted")
			}

			if got := decodeClass(t, err); got != "unknown_field" {
				t.Fatalf("class = %q, want unknown_field", got)
			}
		})
	}

	t.Run("but a set_hp intent is an intent", func(t *testing.T) {
		t.Parallel()

		// `set_hp` with an `hp` is a legal *intent* — asking to set hit points is
		// exactly what a player does. The codec cannot tell an intent to set from a
		// claim about what they are, and it must not try: §7.2 puts the
		// authorisation on the server. So this frame decodes, and the case is here
		// to say so out loud — the rule is "no field a *result* arrives in", not
		// "no field a number arrives in".
		frame, err := realtime.Decode(
			[]byte(`{"t":"intent","seq":8,"op":"set_hp","args":{"placement":"p1","hp":9999}}`),
		)
		if err != nil {
			t.Fatalf("a set_hp intent was refused: %v", err)
		}

		intent, ok := frame.(*realtime.ClientIntent)
		if !ok {
			t.Fatalf("want *ClientIntent, got %T", frame)
		}

		if intent.Args.HP != 9999 {
			t.Fatalf("hp = %v, want 9999", intent.Args.HP)
		}
	})
}

// TestAClientSuppliedVersionIsRefused is S-7.2's property as a client sees it.
func TestAClientSuppliedVersionIsRefused(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"high version on a hello":  `{"t":"hello","since":1,"version":999999999}`,
		"version on an intent":     `{"t":"intent","seq":1,"op":"pause","version":999999999}`,
		"version instead of since": `{"t":"hello","version":42}`,
		"version on presence":      `{"t":"presence","version":42,"args":{"cursor":[1,2]}}`,
	}

	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := realtime.Decode([]byte(frame))
			if err == nil {
				t.Fatalf("a client-supplied version was accepted as %T", got)
			}

			if class := decodeClass(t, err); class != "unknown_field" {
				t.Fatalf("class = %q, want unknown_field", class)
			}

			if got != nil {
				t.Fatalf("a refused frame returned %T", got)
			}
		})
	}
}

// TestASatisfiableSinceIsAnsweredWithADeltaAndAnUnsatisfiableOneWithASnapshot
// is §7.1's third bullet, and it is why `Resync` exists as a pure function rather
// than as a branch inside the hub where nothing could reach it.
func TestASatisfiableSinceIsAnsweredWithADeltaAndAnUnsatisfiableOneWithASnapshot(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		log   realtime.ChangeLog
		since *realtime.Since
		want  realtime.ResyncKind
	}{
		"no log at all": {
			log: nil, since: since(5), want: realtime.ResyncSnapshot,
		},
		"no since at all": {
			log: fakeLog{latest: 50}, since: nil, want: realtime.ResyncSnapshot,
		},
		"a since inside a whole log": {
			log: fakeLog{latest: 50}, since: since(5), want: realtime.ResyncDelta,
		},
		"a since equal to the latest is up to date": {
			log: fakeLog{latest: 50}, since: since(50), want: realtime.ResyncDelta,
		},
		"a since beyond the latest is a client from another server": {
			log: fakeLog{latest: 50}, since: since(51), want: realtime.ResyncSnapshot,
		},
		"a since the log still covers": {
			log:   fakeLog{latest: 50, earliest: 10, pruned: true},
			since: since(10), want: realtime.ResyncDelta,
		},
		"a since one before the oldest retained change": {
			log:   fakeLog{latest: 50, earliest: 11, pruned: true},
			since: since(10), want: realtime.ResyncDelta,
		},
		"a since older than the log keeps": {
			log:   fakeLog{latest: 50, earliest: 11, pruned: true},
			since: since(9), want: realtime.ResyncSnapshot,
		},
		"a pruned log and a since of zero": {
			log:   fakeLog{latest: 50, earliest: 11, pruned: true},
			since: since(0), want: realtime.ResyncSnapshot,
		},
		"an unpruned log answers every since below it": {
			log:   fakeLog{latest: 50, earliest: 0, pruned: false},
			since: since(0), want: realtime.ResyncDelta,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := realtime.Resync(tc.log, tc.since); got != tc.want {
				t.Fatalf("Resync = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResyncNeverAnswersAPartialDelta is the reason the function above exists.
//
// A third outcome — "here is the part of the gap I still have" — would leave the
// client holding a state no version describes, and the reconciler that fixes that
// up has nothing to reconcile against. The failure is silent and it is a wrong
// tabletop. So the enum has two values and the test enumerates every reachable
// input to prove no path leaves them.
func TestResyncNeverAnswersAPartialDelta(t *testing.T) {
	t.Parallel()

	seen := map[realtime.ResyncKind]bool{}

	for _, latest := range []realtime.Since{0, 1, 10, 1 << 20} {
		for _, earliest := range []realtime.Since{0, 1, 10, 1 << 20} {
			for _, pruned := range []bool{false, true} {
				for _, since := range []realtime.Since{0, 1, 9, 10, 11, 1 << 20} {
					log := fakeLog{latest: latest, earliest: earliest, pruned: pruned}

					got := realtime.Resync(log, &since)
					if got != realtime.ResyncSnapshot && got != realtime.ResyncDelta {
						t.Fatalf("Resync returned a third kind %q", got)
					}

					seen[got] = true
				}
			}
		}
	}

	// A missing entry here would mean one of the two answers is unreachable, and
	// an unreachable answer is an answer no code path produces.
	if len(seen) != 2 {
		t.Fatalf("only %d of the two answers were reachable: %v", len(seen), seen)
	}
}

// panickyReader is an `io.Reader` that fails the test if it is read past a bound.
//
// It is how "the size check happened before the decode" is proven rather than
// asserted. A codec that read the whole message — or decoded it and *then*
// measured it — would trip this, and the frame it is fed is valid JSON, so a
// codec that decoded first would instead succeed and fail the test on its error.
type panickyReader struct {
	t     *testing.T
	data  []byte
	read  int
	limit int
}

// Read implements io.Reader.
func (p *panickyReader) Read(target []byte) (int, error) {
	if p.read > p.limit {
		p.t.Fatalf("read %d bytes past the %d-byte limit: the bound did not come first",
			p.read-p.limit, p.limit)
	}

	if p.read >= len(p.data) {
		return 0, io.EOF
	}

	end := min(p.read+len(target), len(p.data), p.limit+1)

	count := copy(target, p.data[p.read:end])
	p.read += count

	return count, nil
}

// oversizeFrame is a *valid* client frame larger than the limit, so the only
// reason it can be refused is the limit.
func oversizeFrame() []byte {
	padding := strings.Repeat("x", realtime.MaxClientFrameBytes*2)

	return []byte(fmt.Sprintf(
		`{"t":"intent","seq":1,"op":"roll","args":{"expr":"1d20","reason":%q}}`, padding,
	))
}

// TestReadFrameRefusesAnOversizeFrameWithoutReadingOrDecodingIt proves the bound
// is a bound on the read, not a check afterwards.
func TestReadFrameRefusesAnOversizeFrameWithoutReadingOrDecodingIt(t *testing.T) {
	t.Parallel()

	data := oversizeFrame()
	if len(data) <= realtime.MaxClientFrameBytes {
		t.Fatalf("the fixture is %d bytes, which is not over the %d-byte limit",
			len(data), realtime.MaxClientFrameBytes)
	}

	// A reader that would have to be drained to reach the end, and that fails the
	// test if the codec tries.
	source := &panickyReader{t: t, data: data, limit: realtime.MaxClientFrameBytes + 1}

	frame, err := realtime.ReadFrame(context.Background(), source)
	if err == nil {
		t.Fatalf("an oversize frame decoded as %T", frame)
	}

	if got := decodeClass(t, err); got != "too_large" {
		t.Fatalf("class = %q, want too_large", got)
	}

	if !errors.Is(err, realtime.ErrClientFrameTooLarge) {
		t.Fatalf("errors.Is(err, ErrClientFrameTooLarge) is false: %v", err)
	}

	// The same frame through `Decode`, which must refuse it on the length alone
	// rather than on anything the JSON says.
	if _, err := realtime.Decode(data); err == nil {
		t.Fatal("Decode accepted an oversize frame")
	} else if got := decodeClass(t, err); got != "too_large" {
		t.Fatalf("Decode class = %q, want too_large", got)
	}
}

// TestAFrameAtTheLimitIsStillAccepted guards the other edge: a bound that is off
// by one in the refusing direction is a bound that breaks a legitimate client.
//
// The fixture pads with JSON whitespace rather than with a payload field,
// because every payload field this codec accepts is itself bounded — a 16 KiB
// frame is not a frame this protocol can carry, whatever the limit says — and
// whitespace is the one way to reach the limit without changing the frame's
// meaning. A codec that rejected at `>=` instead of `>` would fail here.
func TestAFrameAtTheLimitIsStillAccepted(t *testing.T) {
	t.Parallel()

	head := `{"t":"hello",`
	tail := `"since":1}`

	pad := realtime.MaxClientFrameBytes - len(head) - len(tail)
	frame := head + strings.Repeat(" ", pad) + tail

	if len(frame) != realtime.MaxClientFrameBytes {
		t.Fatalf("fixture is %d bytes, want exactly the %d-byte limit",
			len(frame), realtime.MaxClientFrameBytes)
	}

	decoded, err := realtime.Decode([]byte(frame))
	if err != nil {
		t.Fatalf("a frame exactly at the limit was refused: %v", err)
	}

	hello, ok := decoded.(*realtime.ClientHello)
	if !ok {
		t.Fatalf("want *ClientHello, got %T", decoded)
	}

	if hello.Since == nil || *hello.Since != 1 {
		t.Fatalf("since = %v, want 1", hello.Since)
	}
}

// TestARejectedFrameNeverEchoesItsInput is S-12.3 at the boundary.
//
// Every fixture plants a secret-shaped string in a field that can hold one, and
// the assertion is that it appears in no error in the chain. The chain matters as
// much as the text: a wrapped `*json.SyntaxError` is an echo that `Error()`
// happens to hide today, and the hub is free to print the wrapped one.
func TestARejectedFrameNeverEchoesItsInput(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"secret in an op":                `{"t":"intent","seq":1,"op":"` + secret + `"}`,
		"secret in a placement":          `{"t":"intent","seq":1,"op":"move_token","args":{"placement":"` + secret + `"}}`,
		"secret in a focus":              `{"t":"presence","args":{"focus":"` + secret + `"}}`,
		"secret in a field name":         `{"t":"hello","` + secret + `":"x"}`,
		"secret in a t":                  `{"t":"` + secret + `"}`,
		"secret then garbage":            `{"t":"hello","since":1,"` + secret + `":}`,
		"secret as raw bytes":            `{"t":"hello","since":` + secret + `}`,
		"unterminated secret":            `{"t":"` + secret[:8],
		"secret beside an unknown field": `{"t":"intent","seq":1,"op":"roll","args":{"expr":"` + secret + `","result":20}}`,
		"secret beside a bad number":     `{"t":"intent","seq":` + secret + `,"op":"pause"}`,
	}

	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := realtime.Decode([]byte(frame))
			if err == nil {
				t.Fatal("expected a rejection")
			}

			assertNoEcho(t, err, secret)
			assertNoEcho(t, err, "hunter2")
		})
	}
}

// TestARejectedFrameUnwrapsToASentinelNotADecoderError is the mechanism behind
// the test above, stated so a future change cannot quietly reintroduce the echo.
//
// `encoding/json` puts the offending character in a `SyntaxError`, the offending
// literal in an `UnmarshalTypeError`, and the field name in a bare error. If any
// of those were wrapped, the frame's bytes would be one `err.Error()` away from
// a log line.
func TestARejectedFrameUnwrapsToASentinelNotADecoderError(t *testing.T) {
	t.Parallel()

	frames := []string{
		`{"t":"hello",`,
		`{"t":"hello","since":"forty two"}`,
		`{"t":"hello","since":1,"colours":"many"}`,
	}

	for _, frame := range frames {
		_, err := realtime.Decode([]byte(frame))
		if err == nil {
			t.Fatalf("%s was accepted", frame)
		}

		if _, ok := errors.AsType[*json.SyntaxError](err); ok {
			t.Fatalf("%s: a *json.SyntaxError reached the caller: %v", frame, err)
		}

		if _, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
			t.Fatalf("%s: a *json.UnmarshalTypeError reached the caller: %v", frame, err)
		}
	}
}

// TestTheRejectionReasonsAreAClosedSet is what keeps a plugin's error text off the
// wire. A free-form `reason` is a field a plugin error can be poured into, and a
// plugin error quotes the thing it choked on.
func TestTheRejectionReasonsAreAClosedSet(t *testing.T) {
	t.Parallel()

	for _, reason := range everyReason {
		data, err := realtime.Encode(&realtime.ServerRejected{
			Type:   realtime.TypeRejected,
			Seq:    1,
			Reason: reason,
		})
		if err != nil {
			t.Fatalf("reason %q was refused: %v", reason, err)
		}

		if !strings.Contains(string(data), `"reason":"`+string(reason)+`"`) {
			t.Fatalf("reason %q did not survive: %s", reason, data)
		}
	}

	refused := []realtime.RejectReason{
		"not your turn",
		"NOT_YOUR_TURN",
		realtime.RejectReason(secret),
		"internal_panic_at_handler.go:42",
	}

	for _, reason := range refused {
		_, err := realtime.Encode(&realtime.ServerRejected{
			Type:   realtime.TypeRejected,
			Seq:    1,
			Reason: reason,
		})
		if err == nil {
			t.Fatalf("reason %q was accepted", reason)
		}

		if got := decodeClass(t, err); got != "unknown_reason" {
			t.Fatalf("class for %q = %q, want unknown_reason", reason, got)
		}

		assertNoEcho(t, err, string(reason))
	}

	t.Run("and an empty reason", func(t *testing.T) {
		t.Parallel()

		// Its own case rather than one entry in the table above, because
		// `strings.Contains(x, "")` is true of every string and an assertion built
		// on it would have passed vacuously.
		if _, err := realtime.Encode(&realtime.ServerRejected{
			Type: realtime.TypeRejected, Seq: 1,
		}); err == nil {
			t.Fatal("an empty reason was accepted")
		}
	})
}

// TestEncodeRefusesAFrameThatWouldBeWrong is `Encode`'s half of the contract: a
// hub cannot put a frame on the wire that this project considers malformed.
func TestEncodeRefusesAFrameThatWouldBeWrong(t *testing.T) {
	t.Parallel()

	state := json.RawMessage(`{}`)

	cases := map[string]struct {
		frame realtime.ServerFrame
		class string
	}{
		"snapshot with no state": {
			frame: &realtime.ServerSnapshot{
				Type: realtime.TypeSnapshot, You: realtime.Viewer{Role: domain.RoleGM},
			},
			class: "no_state",
		},
		"snapshot with an unknown role": {
			frame: &realtime.ServerSnapshot{
				Type: realtime.TypeSnapshot, State: state,
				You: realtime.Viewer{Role: domain.Role(secret)},
			},
			class: "bad_role",
		},
		"snapshot with a zero role": {
			frame: &realtime.ServerSnapshot{
				Type: realtime.TypeSnapshot, State: state, You: realtime.Viewer{},
			},
			class: "bad_role",
		},
		"applied with a forgotten type": {
			frame: &realtime.ServerApplied{
				Version: 1, Placement: "p1", Op: "move_token",
			},
			class: "unknown_type",
		},
		"applied naming no placement": {
			frame: &realtime.ServerApplied{
				Type: realtime.TypeApplied, Version: 1, Op: "move_token",
			},
			class: "bad_token",
		},
		"applied with an op that is not a token": {
			frame: &realtime.ServerApplied{
				Type: realtime.TypeApplied, Version: 1, Placement: "p1", Op: "Move",
			},
			class: "bad_token",
		},
		"delta with a change naming no placement": {
			frame: &realtime.ServerDelta{
				Type: realtime.TypeDelta,
				Changes: []realtime.Change{
					{Placement: "p1", Version: 2, Op: "set_hp"},
					{Version: 3, Op: "set_hp"},
				},
			},
			class: "bad_token",
		},
		"delta with oversized args": {
			// 64 KiB of whitespace: over `maxArgsLen`, well under
			// `MaxServerFrameBytes`, so this fixture cannot be passing because of
			// the frame bound. `TestEncodeBoundsTheWholeFrameNotOnlyItsParts` is
			// the one that covers that.
			frame: &realtime.ServerDelta{
				Type: realtime.TypeDelta,
				Changes: []realtime.Change{{
					Placement: "p1", Version: 3, Op: "set_hp",
					Args: json.RawMessage(strings.Repeat(" ", 64<<10)),
				}},
			},
			class: "bad_token",
		},
		"roster with a negative id": {
			frame: &realtime.ServerPresence{
				Type: realtime.TypePresence, Users: []realtime.PresenceUser{{ID: -1}},
			},
			class: "bad_token",
		},
		"roster with a NaN coordinate": {
			// `json.Marshal` would refuse this with an error naming the value, so
			// the check that catches it first is the one under test. The second
			// coordinate is the interesting one: a check that refused only when both
			// were non-finite would pass a cursor whose `x` is fine and whose `y` is
			// not, which is the coordinate a client would be looking at.
			frame: &realtime.ServerPresence{
				Type: realtime.TypePresence,
				Users: []realtime.PresenceUser{{
					ID:     1,
					Cursor: &realtime.Cursor{12, math.NaN()},
				}},
			},
			class: "bad_cursor",
		},
		"roster with an infinite coordinate": {
			frame: &realtime.ServerPresence{
				Type: realtime.TypePresence,
				Users: []realtime.PresenceUser{{
					ID:     1,
					Cursor: &realtime.Cursor{math.Inf(1), 12},
				}},
			},
			class: "bad_cursor",
		},
		"roster with a focus carrying a space": {
			frame: &realtime.ServerPresence{
				Type:  realtime.TypePresence,
				Users: []realtime.PresenceUser{{ID: 1, Focus: "p 1"}},
			},
			class: "bad_token",
		},
		"clock with a forgotten type": {
			frame: &realtime.ServerClock{Paused: true},
			class: "unknown_type",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data, err := realtime.Encode(tc.frame)
			if err == nil {
				t.Fatalf("encoded as %s", data)
			}

			if got := decodeClass(t, err); got != tc.class {
				t.Fatalf("class = %q, want %q", got, tc.class)
			}
		})
	}
}

// TestEncodeBoundsTheWholeFrameNotOnlyItsParts holds the outbound limit where the
// constant says it is.
//
// The two bounds are independent and both are needed. `maxArgsLen` bounds one
// opaque payload, because an opaque field is unbounded by definition; the frame
// bound is the one that stops a *legitimately formed* frame from being enormous,
// and a campaign with a very large state document is exactly how that happens with
// no single field out of range. A test that only built one huge `args` would pass
// with the frame bound deleted.
func TestEncodeBoundsTheWholeFrameNotOnlyItsParts(t *testing.T) {
	t.Parallel()

	// Many changes, each well inside `maxArgsLen`, together past the frame bound.
	// 20_000 changes at ~60 bytes each is ~1.2 MiB, over the 1 MiB frame bound and
	// under it by a wide margin if the bound were doubled — so this is a fixture
	// that distinguishes the two numbers rather than tripping both.
	delta := &realtime.ServerDelta{Type: realtime.TypeDelta}
	for i := range 20_000 {
		delta.Changes = append(delta.Changes, realtime.Change{
			Placement: realtime.PlacementID(fmt.Sprintf("p%d", i)),
			Version:   realtime.Version(i + 1),
			Op:        "set_hp",
			Args:      json.RawMessage(`{"hp":7}`),
			By:        1,
		})
	}

	data, err := realtime.Encode(delta)
	if err == nil {
		t.Fatalf("a %d-byte frame encoded", len(data))
	}

	if got := decodeClass(t, err); got != "oversize" {
		t.Fatalf("class = %q, want oversize", got)
	}

	if !errors.Is(err, realtime.ErrServerFrameTooLarge) {
		t.Fatalf("errors.Is(err, ErrServerFrameTooLarge) is false: %v", err)
	}

	// And the check is on the *encoded* size, not the in-memory size: a delta of
	// many tiny changes must still encode when there are few enough of them.
	small := *delta
	small.Changes = delta.Changes[:10]

	encoded, err := realtime.Encode(&small)
	if err != nil {
		t.Fatalf("ten changes did not encode: %v", err)
	}

	if len(encoded) >= realtime.MaxServerFrameBytes {
		t.Fatalf("ten changes encoded to %d bytes, which is not under the bound", len(encoded))
	}
}

// TestWriteFrameWritesOneWholeFrame guards the single-write rule. A frame is one
// WebSocket message: two writes are two messages, and a client reading a message
// at a time sees a truncated JSON value.
func TestWriteFrameWritesOneWholeFrame(t *testing.T) {
	t.Parallel()

	var sink countingWriter

	err := realtime.WriteFrame(context.Background(), &sink, &realtime.ServerRejected{
		Type: realtime.TypeRejected, Seq: 7, Reason: realtime.RejectNotYourTurn,
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	if sink.writes != 1 {
		t.Fatalf("wrote %d times, want 1", sink.writes)
	}

	if want := `{"t":"rejected","seq":7,"reason":"not_your_turn"}`; sink.written != want {
		t.Fatalf("wrote %s, want %s", sink.written, want)
	}
}

// countingWriter counts the calls made against it.
type countingWriter struct {
	writes  int
	written string
}

// Write implements io.Writer.
func (c *countingWriter) Write(p []byte) (int, error) {
	c.writes++
	c.written += string(p)

	return len(p), nil
}

// TestAWriteFailureReachesTheHub proves `WriteFrame` does not swallow a transport
// error, and does not turn one into a frame rejection: the hub has to be able to
// tell "this frame was wrong" from "the socket died".
func TestAWriteFailureReachesTheHub(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("socket closed")

	err := realtime.WriteFrame(context.Background(), failingWriter{sentinel: sentinel},
		&realtime.ServerClock{Type: realtime.TypeClock})
	if err == nil {
		t.Fatal("a failing write reported success")
	}

	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is(err, sentinel) is false: %v", err)
	}

	if errors.Is(err, realtime.ErrFrameRejected) {
		t.Fatalf("a transport error was reported as a frame rejection: %v", err)
	}
}

// failingWriter always fails.
type failingWriter struct {
	sentinel error
}

// Write implements io.Writer.
func (f failingWriter) Write(p []byte) (int, error) { return 0, f.sentinel }

// TestReadFrameHonoursACancelledContext: a cancelled hub stops reading rather
// than discovering the cancellation after the next frame, which for a peer
// streaming an endless frame is the difference between a cancel and a hang.
func TestReadFrameHonoursACancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := realtime.ReadFrame(ctx, endlessReader{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(err, context.Canceled) is false: %v", err)
	}
}

// endlessReader never stops.
type endlessReader struct{}

// Read implements io.Reader.
func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}

	return len(p), nil
}

// TestANilReaderIsAWiringFaultRatherThanAClientFault: `ReadFrame` refuses a nil
// reader by class, so the hub's log says "we passed nil" and not "a client sent
// something odd".
func TestANilReaderIsAWiringFaultRatherThanAClientFault(t *testing.T) {
	t.Parallel()

	_, err := realtime.ReadFrame(context.Background(), nil)
	if err == nil {
		t.Fatal("a nil reader was accepted")
	}

	if got := decodeClass(t, err); got != "nil_reader" {
		t.Fatalf("class = %q, want nil_reader", got)
	}

	if errors.Is(err, realtime.ErrFrameRejected) {
		t.Fatal("a nil reader is not the client's fault and must not be a rejection")
	}
}

//

// TestTheFrameErrorsTextIsAClass is the contract a hub logs: whatever went wrong,
// the string is an identifier this project chose.
func TestTheFrameErrorsTextIsAClass(t *testing.T) {
	t.Parallel()

	_, err := realtime.Decode([]byte(`{"t":"whisper"}`))

	want := "realtime: frame rejected: unknown_type"
	if err.Error() != want {
		t.Fatalf("text = %q, want %q", err.Error(), want)
	}
}

// fakeLog is a `ChangeLog` for the `Resync` tests.
type fakeLog struct {
	latest   realtime.Since
	earliest realtime.Since
	pruned   bool
}

// Latest implements realtime.ChangeLog.
func (f fakeLog) Latest() realtime.Since { return f.latest }

// Earliest implements realtime.ChangeLog.
func (f fakeLog) Earliest() realtime.Since { return f.earliest }

// Pruned implements realtime.ChangeLog.
func (f fakeLog) Pruned() bool { return f.pruned }

// since returns a pointer to a `Since`.
//
// `Resync` takes `*Since` so that "no `since` at all" is distinguishable from
// "`since` is zero", and a table of resync cases needs to say which of the two each
// row is. A row that wrote `&n` over a loop variable would need a local per row and
// a comment explaining the take.
//
// Concrete rather than generic: a generic version is flagged by a linter that
// thinks it is an inlinable wrapper around `new(expr)`, and `new(expr)` is not
// valid Go for a value — `new(T)` allocates the zero `T`. The suggestion is a false
// positive, and a concrete function has one caller type and no need to argue with
// the linter about it.
//
//nolint:modernize // new(expr) is not valid Go for a value: new(T) allocates the zero T.
func since(
	value realtime.Since,
) *realtime.Since {
	return &value
}

// inboundRoots are the three types a client frame may be, and the graph every
// reflection assertion above walks.
func inboundRoots() []reflect.Type {
	return []reflect.Type{
		reflect.TypeFor[realtime.ClientHello](),
		reflect.TypeFor[realtime.ClientIntent](),
		reflect.TypeFor[realtime.ClientPresence](),
	}
}

// walkStructs visits every field of every struct reachable from root, including
// root, following struct and pointer-to-struct fields.
func walkStructs(root reflect.Type, visit func(reflect.Type, reflect.StructField)) {
	seen := map[reflect.Type]bool{}
	queue := []reflect.Type{root}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if current.Kind() == reflect.Pointer {
			current = current.Elem()
		}

		if current.Kind() != reflect.Struct || seen[current] {
			continue
		}

		seen[current] = true

		for field := range current.Fields() {
			visit(current, field)

			queue = append(queue, field.Type)
		}
	}
}
