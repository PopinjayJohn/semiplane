package play

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/semiplane/semiplane/internal/realtime"
)

// The close reasons this route sends.
//
// A close reason is **rendered in a browser** and lands in an operator's log, so
// the vocabulary here is this package's own words and never a value that came
// from a frame. `realtime`'s `WebSocketConn.Close` says the same about the
// socket, and the rule lives in both places because it is the kind of rule that
// gets broken in the one place nobody re-reads. The only value from elsewhere is
// `realtime.FrameError.Class`, which is an identifier the protocol package chose
// and is therefore safe to name — a codec error's own text quotes the bytes that
// broke it, and on a socket every byte is client input (S-12.3).
const (
	// closeNormal is the reason for a connection the peer closed cleanly, and for
	// nothing else. A peer that says 1000 is told 1000 back, and the reason is
	// this package's word rather than the peer's: a reason is rendered in a
	// browser, and echoing a client-chosen string would put it into a UI and a log.
	closeNormal = "normal"
	// closeShutdown is a hub that closed, a campaign that ended, or a peer the
	// sweep retired. All three are "come back later" and a client treats them the
	// same way, which is correct: they are the same thing to a player.
	closeShutdown = "table_going_away"
	// closeReadBound is the read deadline or the frame rate, both of which are
	// policy outcomes about this connection and not faults.
	closeReadBound = "read_bound"
	// closeTransport is a write or read failure with no other explanation.
	closeTransport = "transport"
	// closeFault is this process's fault: a resolver that would not answer.
	closeFault = "handler_fault"
)

// The close **codes** the pump chooses, named apart from the reasons so a reader
// of a return statement can see which half it is looking at.
//
// A retired peer is 1001 rather than 1000 because a client distinguishes the two
// and the distinction is worth keeping: 1000 is "we are finished, do not come
// back" and 1001 is "we are going away, try later", and a table that was retired
// for being behind should be retried while a table the hub closed mid-shutdown
// should not be retried until the process is back. Both are this route's own
// decision and neither is the peer's, which is why `closeFor` handles the peer's
// code first and separately.
const (
	// closeCodeRetired is a peer the hub detached: closed, swept as stale, or
	// retired by `Peer.Send` for a full answer queue.
	closeCodeRetired = websocket.StatusGoingAway
	// closeCodeTransport is a write that failed for no better reason.
	closeCodeTransport = websocket.StatusGoingAway
)

// The frame-rate meter, and why it is here rather than in the hub.
//
// `internal/realtime`'s header names **this route** as the owner: the
// per-connection frame rate is a property of the *read loop*, because the loop is
// the only place that sees a frame, and a hub that counted them would have to be
// told about each one by the goroutine that already counts them. A limit declared
// in one file and enforced in another is a rule one caller routes around, which is
// the stylesheet-import failure.
//
// A public endpoint with no rate is a public endpoint that can be made to burn a
// core by a client that sends `hello` in a loop. Decoding is the expensive half,
// not the socket.
const (
	// defaultFrameLimit is how many frames one connection may send per
	// `defaultFrameWindow`.
	//
	// Sixty a second, and the number is a ceiling rather than a target. A client
	// sends a frame per user gesture and a presence frame per cursor move, so a
	// genuinely busy table is tens of frames a second; a hundred and twenty would
	// be a client saturating its own tab's event loop before the server said
	// anything, which is a client bug the server should not absorb. A client that
	// trips it is not a slow client — it is sending frames nobody is reading, which
	// is the same shape of problem as a peer that is not draining its outbound
	// queue, and it is retired for it.
	defaultFrameLimit = 60

	// defaultFrameWindow is the width of the window `defaultFrameLimit` is counted
	// over.
	//
	// A fixed window rather than a token bucket, and the trade is named: a client
	// can send `2 × limit` frames across a window boundary. That is a factor of two
	// on a limit whose purpose is to stop a client monopolising a core, and it is
	// accepted in exchange for a counter that needs no goroutine, no ticker and no
	// per-connection state to be exactly right — the same trade `realtime.HubClock`
	// makes by taking a clock rather than a timer.
	defaultFrameWindow = time.Second
)

// The three sentinels the read loop ends a connection with, and why they are
// separate from `realtime`'s own.
//
// Each names a cause *this route* knows about rather than a class the protocol
// package chose, and the distinction is load-bearing because the close code the
// client is given is derived from which of the three it is: a frame the client got
// wrong and a frame rate the client exceeded are the client's to fix (1008), and a
// resolver that would not answer is this process's fault (1011).
var (
	// errFrame wraps a codec error, so the writer can tell a client fault from a
	// transport fault without matching on the protocol package's own sentinels.
	errFrame = errors.New("play: the client sent a frame the codec refused")

	// errFrameRate is a client sending frames faster than one connection may.
	errFrameRate = errors.New("play: the connection exceeded the frame rate")

	// errResolve wraps a failure of `Hub.Apply`.
	errResolve = errors.New("play: an intent could not be resolved")
)

// serve upgrades the connection and holds it until one of the three halves ends:
// the peer leaves, the hub closes, or the socket fails.
//
// ## Two goroutines, and why the design allows exactly these two
//
// One goroutine cannot both block in `Read` and select on the peer's two queues,
// and this route must do both: a broadcast is owed to a client that has said
// nothing for a minute, and a client that has said nothing for a minute is a game
// between turns rather than a broken connection. So the split is:
//
//   - **This goroutine, the one `net/http` gave the request, is the only writer.**
//     It selects on `Peer.Broadcasts`, `Peer.Answers` and the reader's exit, and
//     it is the single writer ADR 0010 asks this project to hold. `coder/websocket`
//     panics on concurrent writes, and the mutex inside `realtime.WebSocketConn`
//     is there to make a *second* writer impossible to get wrong rather than
//     because a second one is expected.
//   - **One spawned goroutine is the reader.** It decodes and never writes.
//
// `realtime`'s header says there is no per-peer goroutine, and the claim holds in
// the form it is actually about: the *hub* never needs a goroutine per subscriber,
// because its sends are non-blocking and a full queue is retired rather than waited
// on. It does not claim that a socket which reads and writes can be served from one
// goroutine, which it cannot. The reader is the one cost a duplex socket forces, and
// it is the goroutine `TestJoiningAConnDoesNotStartAGoroutine` cannot see — that test
// joins peers rather than serving routes — so this route's own
// `TestAConnectionCostsExactlyOneGoroutine` is what holds the number here.
//
// ## The request context is deliberately not the connection's lifetime
//
// See the package comment: `connCtx` is the request's context with its cancellation
// and deadline removed, and it is used for the reads **and** for `Hub.Apply`. The
// second half is the more surprising and is deliberate: the budget's twenty-five
// seconds would otherwise expire in the middle of a game and every intent after it
// would be answered `server_error`, which is a table that looks alive and refuses
// every move. A resolver that hangs holds one connection, which an operator can
// see and the read bound cannot bound — and the opposite trade is a table that
// stops working at a fixed interval.
func (h *Handler) serve(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	slug string,
	peer *realtime.Peer,
) {
	// Before the upgrade, for the reason `internal/httpapi/events` clears it before
	// its first flush: `http.Server.WriteTimeout` bounds how long a response body
	// may take, which is right for a page and fatal for a socket. The deadline is
	// set on the connection *before* the handler runs, the upgrade hijacks the
	// connection, and nothing clears it afterwards — so a deployment with a
	// thirty-second write timeout would close every table at thirty seconds and
	// every client would reconnect, forever.
	clearWriteDeadline(ctx, w)

	// `InsecureSkipVerify` is the load-bearing option here and it does not mean what
	// it says in any other context. This handler has already enforced `Origin`
	// itself, in `ServeHTTP`, before the join and before this call — and it did so
	// rather than letting the library do it because the library's check is a
	// *pattern* check whose documented escape hatch is a wildcard: a pattern added
	// later for a cross-origin deployment would widen a boundary this package is
	// supposed to own, and two authorities for one value is the stylesheet-import
	// failure. Turning the library's own check off is what leaves exactly one.
	raw, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		// Compression off, stated rather than omitted so a later reader does not read
		// the omission as an oversight: a state document is server-produced and a
		// `context` frames a compression oracle over it. The library's default is
		// already off; naming it is the difference between a decision and an
		// accident.
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		// `Accept` has already written a response — a protocol violation, a
		// non-hijackable writer, or a malformed handshake — with a status this
		// handler cannot change. Classified rather than `err.Error()`d: the
		// library's own message quotes request headers, which are client-chosen.
		h.log(ctx, slog.LevelWarn, "play.upgrade_failed",
			slog.String("campaign", slug),
			slog.String("class", classify(err)),
		)

		return
	}

	raw.SetReadLimit(h.readLimit())

	writer := realtime.NewWebSocketConn(raw)

	connCtx := context.WithoutCancel(ctx)
	// Buffered, so the reader's single send never blocks on a pump that has already
	// given up — which is the ordinary case on a write failure.
	reads := make(chan readExit, 1)
	readerDone := make(chan struct{})

	go func() {
		defer close(readerDone)

		h.read(connCtx, raw, peer, reads)
	}()

	code, reason := h.pump(ctx, slug, writer, peer, reads)

	// The close, once, from the writer, and before the wait — because the close is
	// what unblocks the reader. `WebSocketConn.Close` writes the close frame, waits
	// out the peer's handshake against the library's own bound, and then shuts the
	// connection down, so the read below cannot wait on a peer that has stopped
	// answering.
	//
	// A close error is reported and not returned: the status line was committed at
	// the upgrade and there is nothing left to answer with.
	if err := writer.Close(code, reason); err != nil {
		h.log(ctx, slog.LevelDebug, "play.close_failed",
			slog.String("campaign", slug),
			slog.String("class", classify(err)),
		)
	}

	<-readerDone

	h.log(ctx, slog.LevelDebug, "play.socket_closed",
		slog.String("campaign", slug),
		slog.Int64("peer", peer.ID()),
		slog.Int("code", int(code)),
		slog.String("reason", reason),
	)
}

// readExit is why the reader stopped, handed to the writer so the close code is the
// reader's story rather than a guess.
type readExit struct {
	// err is what ended the read loop. A clean client disconnect is an error here
	// too — a `*websocket.CloseError` with 1000 — because end-of-stream on a socket
	// is a transport fact, and inventing a reason for it would be a claim.
	err error
}

// read is the connection's reader: count, decode, hand to the hub, repeat.
//
// It never writes. Every frame on this socket is untrusted input, and the only
// thing this function does with a frame it cannot make sense of is end the
// connection deliberately with a reason that is a **class**.
func (h *Handler) read(
	ctx context.Context,
	conn *websocket.Conn,
	peer *realtime.Peer,
	exit chan<- readExit,
) {
	// One meter for the connection's life. A fixed window, so the count resets by
	// comparing the window's start rather than by a ticker: this loop is already
	// awake on every frame, and a second timer per connection would be a second
	// thing to leak.
	meter := frameMeter{limit: defaultFrameLimit, window: defaultFrameWindow}

	for {
		if !meter.allow(nowFunc()) {
			// Deliberate, named, and asserted by a test. The alternative — logging
			// and continuing — leaves a client that can hold a core by sending frames
			// nobody reads, which is the failure the limit exists to prevent.
			exit <- readExit{err: errFrameRate}

			return
		}

		readCtx, cancel := context.WithTimeout(ctx, h.readTimeout())

		_, payload, err := conn.Read(readCtx)

		cancel()

		if err != nil {
			exit <- readExit{err: err}

			return
		}

		frame, err := realtime.Decode(payload)
		if err != nil {
			// The class and not the text, and this is the most important log line on
			// the route: a codec error quotes the frame that broke it, and a socket
			// is a public endpoint (S-12.3). `errFrame` wraps it only so the writer
			// can tell a client fault from a transport fault; nothing else travels.
			exit <- readExit{err: fmt.Errorf("%w: %w", errFrame, err)}

			return
		}

		if err := h.intent(ctx, peer, frame); err != nil {
			// `Hub.Apply` failing is a fault in the hub or the resolver and not in the
			// client, and the loop ends so the connection does not sit there
			// applying nothing while reporting itself healthy.
			exit <- readExit{err: fmt.Errorf("%w: %w", errResolve, err)}

			return
		}
	}
}

// intent hands one decoded frame to the hub, and says plainly what this route does
// with the frames that are not intents.
//
// **A `ClientIntent` is the whole of what the hub can apply today.** `Hub.Apply`
// takes a `*ClientIntent` and a peer and nothing else, and it is the one path by
// which anything a client says becomes state (S-7.1). The other two inbound frames
// are routed to the same no-op, and that is a *deliberate* no-op rather than an
// oversight:
//
//   - `ClientHello` is answered with a snapshot, and a snapshot is the state
//     document a system produces. This route holds no state and resolves no
//     operation, so it has nothing to answer with; the answer belongs to the
//     `Resolver` the composition root registers, through the same peer. Ignoring
//     it rather than refusing is the smaller harm: an unanswered `hello` leaves a
//     client showing its own loading state, while a refused one is a table that
//     will not open at all.
//   - `ClientPresence` is ephemeral by construction (S-7.6) and carries no `seq`,
//     so there is nothing for it to be answered with even in principle. The roster
//     it would produce is a broadcast, and broadcasts come from the system.
//
// A client frame is never checked for authorisation here, and that is the shape
// ADR 0024 is about: this function has no role, no user and no session to check
// anything against, because the gate ran before the upgrade. What it does have is
// `peer`, and `Hub.Apply` takes the campaign and the actor from **that** — a frame
// has no field in which to name the campaign it is playing on, and one must not be
// added.
func (h *Handler) intent(
	ctx context.Context,
	peer *realtime.Peer,
	frame realtime.ClientFrame,
) error {
	intentFrame, isIntent := frame.(*realtime.ClientIntent)
	if !isIntent {
		return nil
	}

	if err := h.Hub.Apply(ctx, peer, intentFrame); err != nil {
		return wrapf("apply an intent: %w", err)
	}

	return nil
}

// pump is the connection's single writer: two queues in, one socket out.
//
// The queues are drained separately and the reason is the hub's own: a broadcast
// is a *replacing* slot of one, because two deltas inside a second are one fact,
// and an answer is a bounded queue that is never replaced, because `applied` and
// `rejected` each resolve exactly one `seq` and a delta published microseconds
// later must not eat one. Draining both through one channel would make the answer
// the thing that gets superseded, and a client that never receives its answer
// rolls its optimistic update back and waits forever (ADR 0009, rule 1).
//
// Answers are taken **first** and not by politeness: a broadcast and an answer are
// both ready at once whenever a GM's own move produced both, and the answer is the
// one carrying the `seq` the client is blocked on.
//
// **`Peer.Advance()` is called on every successful receive from either queue**, and
// it is the one call in this file that is easy to skip and expensive to skip. It
// is what separates a peer that is *behind* from a peer that is *gone*: the hub
// retires a peer whose outbound queue has been continuously full for a whole
// window, and without the call a full queue means only "the last publish landed
// microseconds ago" — so a healthy table is retired mid-game, and the symptom is a
// reconnect loop with an error nowhere in sight.
func (h *Handler) pump(
	ctx context.Context,
	slug string,
	writer realtime.Conn,
	peer *realtime.Peer,
	reads <-chan readExit,
) (websocket.StatusCode, string) {
	broadcasts := peer.Broadcasts()
	answers := peer.Answers()

	for {
		if usable := h.take(ctx, slug, writer, peer, answers); !usable {
			return closeCodeRetired, closeShutdown
		}

		if usable := h.take(ctx, slug, writer, peer, broadcasts); !usable {
			return closeCodeRetired, closeShutdown
		}

		select {
		case payload, open := <-answers:
			if !open {
				return closeCodeRetired, closeShutdown
			}

			peer.Advance()

			if err := h.write(ctx, slug, writer, payload); err != nil {
				return closeCodeTransport, closeReason(err)
			}

		case payload, open := <-broadcasts:
			if !open {
				return closeCodeRetired, closeShutdown
			}

			peer.Advance()

			if err := h.write(ctx, slug, writer, payload); err != nil {
				return closeCodeTransport, closeReason(err)
			}

		case done := <-reads:
			// The reader ended: a close, a refusal, a rate, or a transport failure.
			// Flushing what is still queued first would be generous and wrong —
			// continuing to write to a socket whose reader has given up is the
			// failure `closeTransport` names.
			code, reason := closeFor(done.err)

			h.log(ctx, slog.LevelDebug, "play.reader_finished",
				slog.String("campaign", slug),
				slog.String("class", classify(done.err)),
			)

			return code, reason
		}
	}
}

// take writes every frame currently queued on queue and reports whether the
// connection is still usable.
//
// A closed queue reports `false`, and that is the loop's whole teardown condition:
// the hub closes both channels in `detach` for every retirement, so "the hub
// closed", "the sweep retired this peer" and "`Peer.Send` gave up on a full answer
// queue" are one observation rather than three.
func (h *Handler) take(
	ctx context.Context,
	slug string,
	writer realtime.Conn,
	peer *realtime.Peer,
	queue <-chan []byte,
) bool {
	for {
		select {
		case payload, open := <-queue:
			if !open {
				return false
			}

			// The advance is here as well as in `pump`'s select arms, and the reason
			// is that this is the arm a *burst* takes: a peer that is behind has two
			// or three frames waiting after every turn, and this is the path that
			// actually empties them.
			peer.Advance()

			if err := h.write(ctx, slug, writer, payload); err != nil {
				return false
			}

		default:
			return true
		}
	}
}

// write puts one already-encoded frame on the socket.
func (h *Handler) write(
	ctx context.Context,
	slug string,
	writer realtime.Conn,
	payload []byte,
) error {
	if err := writer.Write(ctx, payload); err != nil {
		h.log(ctx, slog.LevelDebug, "play.write_failed",
			slog.String("campaign", slug),
			slog.String("class", classify(err)),
		)

		return wrapf("write a frame: %w", err)
	}

	return nil
}

// clearWriteDeadline removes the server's write deadline from this response.
//
// `http.Server.WriteTimeout` bounds how long a response body may take, and it is
// set on the connection *before* the handler runs — so an upgrade that does not
// clear it inherits a deadline that expires thirty seconds into a game, with
// nothing left to clear it because the connection is hijacked by then.
//
// An error is tolerated and reported rather than treated as fatal, for the reason
// `internal/httpapi/events`'s own `clearWriteDeadline` gives: a `ResponseWriter`
// that does not support deadlines is not a socket that cannot work, and refusing
// to serve because of it would make the route untestable for no benefit.
func clearWriteDeadline(ctx context.Context, w http.ResponseWriter) {
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		slog.DebugContext(ctx, "play.write_deadline_not_cleared",
			slog.String("class", classify(err)),
		)
	}
}

// closeFor turns why the reader stopped into the close code and reason the client
// is given.
//
// The order is the argument. A `*websocket.CloseError` first, because the peer
// named its own reason and echoing the code is the only answer that can be right.
// Then this package's sentinels, in the order of whose fault each is. Then the
// read bound. Then a transport failure, which is 1001 rather than 1011 because the
// server has no evidence it is the one that broke, and asserting 1011 for an error
// nobody classified is a claim nobody established.
func closeFor(err error) (websocket.StatusCode, string) {
	if closed, isClose := errors.AsType[*websocket.CloseError](err); isClose {
		return closed.Code, closeNormal
	}

	switch {
	case errors.Is(err, errFrame):
		// The codec's own class, on the wire, because it is the one identifier the
		// protocol package chose for "a client sent the wrong bytes" and a client
		// that is told which rule it broke can stop breaking it.
		return websocket.StatusPolicyViolation, "frame_" + realtime.FrameClass(err)
	case errors.Is(err, errFrameRate):
		return websocket.StatusPolicyViolation, closeReadBound
	case errors.Is(err, errResolve):
		return websocket.StatusInternalError, closeFault
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		// The read bound firing is a policy outcome and not a fault: the peer stopped
		// talking and nothing else would have noticed. 1008 says "this connection is
		// not acceptable", which is exactly what it was, and a client that reconnects
		// gets a fresh snapshot.
		return websocket.StatusPolicyViolation, closeReadBound
	case errors.Is(err, net.ErrClosed):
		return websocket.StatusGoingAway, closeShutdown
	default:
		return websocket.StatusGoingAway, closeTransport
	}
}

// closeReason reduces a write failure to a fixed word for the close frame.
//
// A write error's own text is the library's, and the library's text quotes the
// socket. Three fixed words are enough for a browser and none of them can carry
// anything a client chose.
func closeReason(err error) string {
	if isTimeout(err) {
		return closeReadBound
	}

	return closeTransport
}

// classify reduces an error to a short, content-free class for a log line.
//
// **This is the function that keeps an untrusted byte out of a log**, and all
// three of its sources are attacker-influenced:
//
//   - A codec error, which quotes the frame that broke it. `realtime.FrameClass` is
//     the identifier the protocol package chose for exactly this, and it is the
//     reason that method exists: the observability-side class ends in `%T`, so
//     without it every refusal in a log reads as `*realtime.FrameError` and "a
//     client sent nonsense" cannot be told from "sent too much" from "sent an
//     unknown field".
//   - A transport error, whose text quotes request headers and close reasons the
//     peer chose. Reduced to `ws_close_<code>` for a close and a fixed word for
//     anything else.
//   - This package's own sentinels, whose text is fixed and may be logged whole.
//
// The fallback is a word rather than a type name, for the reason
// `realtime.FrameClass` gives: a fixed string in a log line is more use than
// `*fmt.wrapError`.
func classify(err error) string {
	if err == nil {
		return ""
	}

	if frame, isFrame := errors.AsType[*realtime.FrameError](err); isFrame {
		return frame.Class()
	}

	if closed, isClose := errors.AsType[*websocket.CloseError](err); isClose {
		return fmt.Sprintf("ws_close_%d", closed.Code)
	}

	switch {
	case errors.Is(err, errFrameRate):
		return errFrameRate.Error()
	case errors.Is(err, errFrame), errors.Is(err, errResolve):
		return realtime.FrameClass(err)
	case errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed):
		return "transport_closed"
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return "read_timeout"
	default:
		return "transport"
	}
}

// isTimeout reports whether err is a network timeout.
//
// Written by hand because the standard library's interface is unexported, and
// because a read deadline expiring surfaces as a `*net.OpError` on most platforms
// and as a bare `context.DeadlineExceeded` on the rest — and the connection has to
// end the same way either way.
func isTimeout(err error) bool {
	var netErr net.Error

	return errors.As(err, &netErr) && netErr.Timeout()
}

// frameMeter counts frames over a fixed window.
//
// A struct with no clock of its own, and a method that takes `now`, so the window
// arithmetic is testable without a sleep and the loop stays the only thing that
// reads a clock. `realtime.HubClock` is the same decision for the same reason:
// asserting the absence of an event in real time means waiting past the window,
// which makes a test slow and its assertion weaker at the same time.
type frameMeter struct {
	// limit is how many frames a window may carry.
	limit int
	// window is the width of a window.
	window time.Duration
	// since is when the current window opened.
	since time.Time
	// count is how many frames the current window has carried.
	count int
}

// allow reports whether one more frame fits in the current window, opening the next
// window when the current one has expired.
//
// The first frame opens the window rather than being counted into a zero one,
// because a meter seeded at `time.Time{}` would need the clock's zero value to be
// "now" and no injected clock guarantees that.
func (m *frameMeter) allow(now time.Time) bool {
	if m.since.IsZero() || now.Sub(m.since) >= m.window {
		m.since = now
		m.count = 0
	}

	if m.count >= m.limit {
		return false
	}

	m.count++

	return true
}
