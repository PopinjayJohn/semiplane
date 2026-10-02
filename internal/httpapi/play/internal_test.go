package play

// The white-box tests.
//
// Everything else in this package's tests goes through a socket, because every
// claim the route owns is a transport claim. Three are not, and they are here
// rather than smuggled into a socket test for two reasons: a socket test for the
// window arithmetic of a rate meter would need sixty-one frames inside a real
// second, and a close code is a pure function of a sentinel — a function whose
// only interesting inputs are the three sentinels and a `*websocket.CloseError`.
//
// This is the same split `internal/realtime`'s `protocol_internal_test.go` makes.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/semiplane/semiplane/internal/realtime"
)

// The frame meter, exactly, with an injected clock.
//
// Three claims and each has bitten somebody: the limit is inclusive (a meter
// written `count > limit` allows one frame too many), the window comparison is
// `>=` (a meter written `>` closes the window a nanosecond late), and the budget is
// **whole again** in the next window (a meter that keeps counting across a boundary
// retires a peer permanently for a burst that ended).
func TestTheFrameMeterAllowsExactlyItsLimitPerWindow(t *testing.T) {
	t.Parallel()

	const (
		limit  = 3
		window = time.Second
	)

	meter := frameMeter{limit: limit, window: window}
	start := time.Date(2026, time.September, 30, 14, 0, 0, 0, time.UTC)

	// The first frame opens the window rather than being counted into a zero one.
	for frame := range limit {
		if !meter.allow(start) {
			t.Fatalf("frame %d of %d in the first window was refused", frame+1, limit)
		}
	}

	if meter.allow(start) {
		t.Error("a frame past the limit in the first window was allowed")
	}

	// One nanosecond short of the next window is still this window. This is the half
	// a `>` written where a `>=` belongs gets wrong, and it is invisible to a test
	// that only advances by whole windows.
	if meter.allow(start.Add(window - time.Nanosecond)) {
		t.Error("a frame one nanosecond before the window closed was allowed")
	}

	// The next window is open and the budget is whole: a client that tripped the
	// rate recovers without being disconnected again a second later.
	for frame := range limit {
		if !meter.allow(start.Add(window)) {
			t.Fatalf("frame %d of %d in the second window was refused", frame+1, limit)
		}
	}

	if meter.allow(start.Add(window)) {
		t.Error("a frame past the limit in the second window was allowed")
	}
}

// The close codes, and the order the classification runs in.
//
// Each row is a different answer, and the *order* is the claim: a `CloseError`
// first because the peer named its own reason, the sentinels next because they say
// whose fault it was, and a transport failure last at 1001 rather than 1011
// because the server has no evidence it is the one that broke.
func TestCloseForNamesTheCauseRatherThanTheSymptom(t *testing.T) {
	t.Parallel()

	peerClosed := &websocket.CloseError{Code: websocket.StatusNormalClosure}
	peerPolicy := &websocket.CloseError{
		Code: websocket.StatusPolicyViolation, Reason: "the peer said so",
	}

	timeout := &net.OpError{Op: "read", Err: timeoutError{}}

	// A **real** codec error rather than a constructed one: `FrameError`'s class is
	// unexported and settable only through the decoder, and a test that built one
	// with a hand-written class would be asserting against its own fixture. This is
	// the exact error the read loop wraps, obtained the way it obtains it.
	_, codecErr := realtime.Decode([]byte(`{"t":"intent","seq":1,"op":"move_token","nope":1}`))
	if codecErr == nil {
		t.Fatal("a frame with an unknown field decoded, so the codec refuses nothing " +
			"and the close-code table's client-fault row is unreachable")
	}

	cases := []struct {
		name   string
		err    error
		code   websocket.StatusCode
		reason string
	}{
		{
			// The peer's code wins, and the reason does **not** echo the peer's own
			// text: a reason is rendered in a browser.
			name: "a peer's own close is echoed with our reason", err: peerClosed,
			code: websocket.StatusNormalClosure, reason: closeNormal,
		},
		{
			name: "a peer's policy close keeps its code", err: peerPolicy,
			code: websocket.StatusPolicyViolation, reason: closeNormal,
		},
		{
			name: "a codec refusal is 1008 named by the codec's class",
			err:  fmt.Errorf("%w: %w", errFrame, codecErr),
			code: websocket.StatusPolicyViolation,
			// The class is the codec's, read through the same call the route reads it
			// through — so this row asserts the wiring of `FrameClass` into the close
			// reason and not a string this file invented.
			reason: "frame_" + realtime.FrameClass(codecErr),
		},
		{
			name: "a rate trip is 1008", err: errFrameRate,
			code: websocket.StatusPolicyViolation, reason: closeReadBound,
		},
		{
			// The one that is this process's fault, and the only 1011 here.
			name: "a resolver failure is 1011", err: errResolve,
			code: websocket.StatusInternalError, reason: closeFault,
		},
		{
			name: "a read bound is 1008 and not 1011", err: context.DeadlineExceeded,
			code: websocket.StatusPolicyViolation, reason: closeReadBound,
		},
		{
			name: "a network timeout is a read bound", err: timeout,
			code: websocket.StatusPolicyViolation, reason: closeReadBound,
		},
		{
			name: "a closed connection is 1001", err: net.ErrClosed,
			code: websocket.StatusGoingAway, reason: closeShutdown,
		},
		{
			// Asserted deliberately: 1011 would be a claim that the server broke
			// something, and this is an error nobody classified.
			name: "an unclassified failure is 1001", err: errors.New("something"),
			code: websocket.StatusGoingAway, reason: closeTransport,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			code, reason := closeFor(testCase.err)
			if code != testCase.code {
				t.Errorf("closeFor(%v) code = %d, want %d", testCase.err, code, testCase.code)
			}

			if reason != testCase.reason {
				t.Errorf("closeFor(%v) reason = %q, want %q", testCase.err, reason, testCase.reason)
			}

			// Every reason this route sends fits in a control frame. A reason over
			// 123 bytes is a protocol error the peer will not see at all, so the
			// assertion is on the property rather than on the numbers.
			if len(reason) > 123 {
				t.Errorf("close reason %q is %d bytes, over a control frame's 123",
					reason, len(reason))
			}
		})
	}
}

// The read-limit floor, as a unit.
//
// `readLimit` refuses to return less than the codec's own bound. Asserted directly
// because a socket cannot tell the two apart: both refusals arrive as a close, and
// a test that sent "a frame the transport limit refused" would pass against a
// handler whose limit was the codec's bound rather than the floor for it.
func TestTheReadLimitIsFlooredAtTheCodecsOwnBound(t *testing.T) {
	t.Parallel()

	t.Run("zero means the hub's own transport limit", func(t *testing.T) {
		t.Parallel()

		handler := &Handler{}

		if got := handler.readLimit(); got != realtime.MaxTransportReadBytes {
			t.Errorf("readLimit() = %d, want %d", got, realtime.MaxTransportReadBytes)
		}
	})

	t.Run("a configured limit is honoured when it is large enough", func(t *testing.T) {
		t.Parallel()

		const want = realtime.MaxTransportReadBytes * 4
		handler := &Handler{ReadLimit: want}

		if got := handler.readLimit(); got != want {
			t.Errorf("readLimit() = %d, want the configured %d", got, want)
		}
	})

	t.Run("a limit below the codec's own is raised to it", func(t *testing.T) {
		t.Parallel()

		handler := &Handler{ReadLimit: realtime.MaxClientFrameBytes - 1}

		if got := handler.readLimit(); got != realtime.MaxClientFrameBytes {
			t.Errorf("readLimit() = %d, want it floored at the codec's own %d",
				got, realtime.MaxClientFrameBytes)
		}
	})

	t.Run("a negative limit is the default rather than a panic", func(t *testing.T) {
		t.Parallel()

		handler := &Handler{ReadLimit: -1}

		if got := handler.readLimit(); got != realtime.MaxTransportReadBytes {
			t.Errorf("readLimit() = %d, want the default %d", got, realtime.MaxTransportReadBytes)
		}
	})
}

// The read-timeout default, for the same reason as the limit: a socket test cannot
// distinguish a handler that defaulted it from one that set it to zero and got a
// deadline of zero, which would close every table the instant it opened.
func TestTheReadTimeoutDefaultsAndHonoursAConfiguredValue(t *testing.T) {
	t.Parallel()

	if got := (&Handler{}).readTimeout(); got != defaultReadTimeout {
		t.Errorf("readTimeout() = %d, want the default %d", got, defaultReadTimeout)
	}

	if got := (&Handler{ReadTimeout: -1}).readTimeout(); got != defaultReadTimeout {
		t.Errorf("readTimeout() with -1 = %d, want the default %d", got, defaultReadTimeout)
	}

	const configured = 250 * time.Millisecond
	if got := (&Handler{ReadTimeout: configured}).readTimeout(); got != configured {
		t.Errorf("readTimeout() = %d, want the configured %d", got, configured)
	}
}

// timeoutError is a `net.Error` that reports a timeout, so the table above can
// exercise the network-timeout branch without waiting for one.
type timeoutError struct{}

func (timeoutError) Error() string { return "i/o timeout" }

func (timeoutError) Timeout() bool { return true }

func (timeoutError) Temporary() bool { return true }
