package middleware

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"runtime/debug"
	"time"
)

// Recoverer turns a panic in a downstream handler into a 500 rather than a
// dropped connection and a dead server.
//
// net/http's own recover closes the connection without a response, which a
// browser reports as a network error rather than a 500 — and the panic text
// never reaches the log. Both matter: a client retrying a failed write needs a
// status it can branch on, and an operator needs the panic.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A named function rather than an inline closure: contextcheck
			// requires the deferred function to receive the request's context
			// as a parameter, and this is the form that satisfies it without a
			// suppression. It also keeps the panic path out of the request
			// function's body, which is where it does not belong.
			defer recoverPanic(r.Context(), logger, w, r)

			next.ServeHTTP(w, r)
		})
	}
}

// recoverPanic logs a panic and writes a 500. Runs as a deferred call, so it
// receives what it needs rather than closing over the request.
func recoverPanic(
	ctx context.Context,
	logger *slog.Logger,
	w http.ResponseWriter,
	r *http.Request,
) {
	recovered := recover()
	if recovered == nil {
		return
	}

	// A panic is a bug, and the stack is the only part of it that says where.
	// Logged, not rendered: the response body is visible to whoever made the
	// request, and a Go stack trace discloses file paths, package structure,
	// and often credentials held in package-level variables.
	logger.ErrorContext(ctx, "http.panic",
		slog.Any("panic", recovered),
		slog.String("request_id", MustRequestID(ctx)),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("stack", string(debug.Stack())),
	)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	writeBody(w, `{"status":"error","error":"internal server error"}`)
}

// Timeout bounds how long a handler may take, answering 504 when the budget is
// exceeded.
//
// The buffer-then-commit mechanism is http.TimeoutHandler's, and the reason is
// not subtlety: a status line cannot be changed once it is written, so a
// handler that has streamed 10KB of a page and *then* exceeds its budget has
// already committed a 200. Holding the body until the handler returns is the
// only way the timeout can still substitute a 504.
//
// What differs from http.TimeoutHandler is that the wrapped writer is passed
// through rather than swapped at the boundary, so every other middleware in the
// chain keeps wrapping the one writer it received. TimeoutHandler's swap means
// each layer has to be correct about two different ResponseWriter
// implementations, and this chain already stacks three.
func Timeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			wrapped := &timeoutWriter{
				ResponseWriter: w,
				expired:        ctx.Done(),
				header:         make(http.Header, len(w.Header())),
				body:           &bytes.Buffer{},
			}

			// A closure, not a plain deferred call. `defer commit(spent)` evaluates
			// its argument *now*, before the handler has run, so the flag was
			// captured as false and the timeout never fired — the writer buffered
			// and then committed the partial body as a 200, which is the precise
			// failure the buffering exists to prevent.
			defer func() {
				wrapped.commit(wrapped.budgetSpent())
			}()

			next.ServeHTTP(wrapped, r.WithContext(ctx))
		})
	}
}

// timeoutWriter buffers a response so the status line stays undecided until the
// handler returns.
//
// It holds the deadline's done channel rather than the context: a
// ResponseWriter outliving the request is a bug, and storing the context would
// make that possible where a channel is enough. The channel is all the writer
// needs — it answers "is the budget spent?" and nothing else.
type timeoutWriter struct {
	http.ResponseWriter

	expired    <-chan struct{}
	header     http.Header
	body       *bytes.Buffer
	wroteHead  bool
	committed  bool
	streaming  bool
	statusCode int
}

func (tw *timeoutWriter) Header() http.Header {
	// Before the first write, delegate to the parent: a handler that sets a
	// header and never writes a body still has to have it applied. After the
	// first write the copy takes over, so a later Header().Set is a no-op
	// rather than a silent surprise — the contract http.ResponseWriter
	// documents for a superseded header.
	if tw.wroteHead {
		return tw.header
	}

	return tw.ResponseWriter.Header()
}

func (tw *timeoutWriter) WriteHeader(status int) {
	if tw.wroteHead {
		return
	}

	tw.wroteHead = true
	tw.statusCode = status
}

func (tw *timeoutWriter) Write(body []byte) (int, error) {
	// Transparent once a handler has flushed. Reached by every write of a
	// streaming response, and by writes that follow the timeout commit.
	if tw.streaming {
		n, err := tw.ResponseWriter.Write(body)
		if err != nil {
			return n, fmt.Errorf("write streamed response: %w", err)
		}

		return n, nil
	}

	if !tw.wroteHead {
		tw.WriteHeader(http.StatusOK)
	}

	// Returning an error rather than a short write: the handler asked to send
	// n bytes and none were sent, which is what a non-nil error means, and
	// inventing a truncated success is how a corrupt response body happens.
	if tw.budgetSpent() {
		return 0, context.DeadlineExceeded
	}

	n, err := tw.body.Write(body)
	if err != nil {
		return n, fmt.Errorf("buffer response body: %w", err)
	}

	return n, nil
}

// Flush satisfies http.Flusher, which server-sent events need, and its presence
// is the signal that buffering must stop.
//
// An SSE handler cannot be buffered: its whole job is to deliver bytes as they
// happen, and holding them until the handler returns delivers nothing until the
// client has already disconnected. Once a handler flushes, the response has
// committed to being a stream — the status line is on the wire and a 504 is no
// longer possible — so the buffer is committed and every later write goes
// straight through.
//
// The first flush is the commit point, so this is the only place the buffered
// path and the streaming path meet. A handler that flushes after its budget
// expired gets the 504, which is correct: the deadline came first.
//
// The parent is flushed, and that is not a detail. `commit` writes the status
// line and the buffered body into the writer underneath, and `net/http` holds a
// response's bytes in its own buffer until that buffer fills or something asks it
// not to. So a `Flush` that committed without flushing produces a response that
// is correct, complete, and sitting in a buffer — which for a streaming response
// means a handler that will not return, and therefore bytes that never leave.
//
// The cost of getting this wrong is invisible in a test that reads a recorder's
// body after the handler returns, and total in production. It was found by the
// server-sent-events route, which worked around it by walking the `Unwrap` chain
// and flushing every layer itself rather than through here.
func (tw *timeoutWriter) Flush() {
	// Already committed: either the budget ran out, or this is a later flush
	// on a live stream. Flushing through is harmless in both cases.
	if tw.committed {
		tw.flushParent()

		return
	}

	if tw.budgetSpent() {
		tw.commit(true)

		return
	}

	// Set before commit so the commit takes the streaming branch and the writer
	// goes transparent from here.
	tw.streaming = true
	tw.commit(false)

	// The status line and the body are in the parent now, and the parent is what
	// talks to the socket. Without this the first frame of every stream — and
	// every frame after it — is written correctly and delivered never.
	tw.flushParent()
}

// Unwrap exposes the wrapped writer to net/http and to middleware that needs
// the underlying implementation's optional interfaces — http.Hijacker for a
// WebSocket upgrade, http.Pusher for HTTP/2 server push.
//
// Without it a handler type-asserts for Hijacker, fails, and the WebSocket
// route cannot upgrade for a reason that has nothing to do with the network.
func (tw *timeoutWriter) Unwrap() http.ResponseWriter {
	return tw.ResponseWriter
}

// flushParent flushes the writer underneath this one, if it can flush.
//
// A type assertion rather than `http.ResponseController`, and the reason is the
// same one the SSE route's own workaround gives: the controller stops at the
// *first* `http.Flusher` it finds, which is this writer, so it would call back
// into `Flush` and never reach the parent. Walking down by `Unwrap` is the only
// way to flush the layer that actually owns the socket.
//
// Tolerates a parent with no flusher — an HTTP/2 response writer that has already
// sent its headers has nothing left to flush, and a `httptest` recorder behaves
// similarly. There is nothing to report: the bytes are written either way, and a
// writer that cannot flush is not a response that failed.
func (tw *timeoutWriter) flushParent() {
	if flusher, canFlush := tw.ResponseWriter.(http.Flusher); canFlush {
		flusher.Flush()
	}
}

// budgetSpent reports whether the deadline has passed. A non-blocking select,
// so it is cheap enough to call on the write path.
func (tw *timeoutWriter) budgetSpent() bool {
	select {
	case <-tw.expired:
		return true
	default:
		return false
	}
}

// commit writes the buffered response, or a 504 if the budget ran out. Called
// exactly once, deferred, after the handler has returned — including after a
// panic, since Recoverer is the outer layer and recovers before this runs.
func (tw *timeoutWriter) commit(timedOut bool) {
	if tw.committed {
		return
	}

	tw.committed = true

	// A flushed response is already on the wire, so a 504 would append an error
	// document to a live event stream. The buffer is still written, though: it
	// holds everything the handler produced before the flush, and returning here
	// silently dropped the first event of every stream.
	if timedOut && !tw.streaming {
		// Written straight to the parent: the buffer is discarded, and headers
		// the handler set are deliberately not forwarded. A Content-Type
		// describing a page that was never sent is a lie, and forwarding a
		// Content-Length with a body that is not coming is a hang.
		header := tw.ResponseWriter.Header()
		clear(header)
		header.Set("Content-Type", "application/json; charset=utf-8")

		tw.ResponseWriter.WriteHeader(http.StatusGatewayTimeout)
		writeBody(tw.ResponseWriter, `{"status":"error","error":"request timed out"}`)

		return
	}

	header := tw.ResponseWriter.Header()
	maps.Copy(header, tw.header)

	status := tw.statusCode
	if status == 0 {
		// No explicit WriteHeader. net/http would default to 200, and doing it
		// here keeps the committed response identical to what the handler would
		// have produced unwrapped.
		status = http.StatusOK
	}

	tw.ResponseWriter.WriteHeader(status)
	writeBody(tw.ResponseWriter, tw.body.String())
}

// Hijack gives up the connection to a handler that is taking it over — a WebSocket
// upgrade, or any protocol that stops being HTTP mid-response.
//
// # Why this exists at all
//
// Without it, `Timeout` **silently breaks every WebSocket route in the product**,
// and the failure looks like a network fault rather than a wiring one.
//
// The mechanism: `websocket.Accept` writes `101 Switching Protocols` through this
// writer and *then* hijacks. This writer **buffers** the status line — that is what
// the whole mechanism exists for, because a status line cannot be changed once it is
// on the wire — and hands it to `commit` in a `defer` that runs when the handler
// returns. A handler that hijacked has returned by then, so the deferred commit
// writes `101` to a connection `net/http` no longer owns, the bytes are discarded,
// and the client sees a bare `EOF` with no status at all.
//
// Without `Hijack` on this type it is worse than that: the type would not satisfy
// `http.Hijacker` at all, so `http.ResponseController` would walk past it to the
// parent and hijack the *raw* connection, leaving this writer still holding the
// status line. Same outcome, and the `101` is now unsalvageable by construction.
//
// # Why the buffer is committed *before* the hijack
//
// That is the whole fix, and the order is the fix. The status line has to reach the
// socket **before** `net/http` hands the connection over, because after the
// hijack `net/http` refuses to write to it — the log line
// `http: response.WriteHeader on hijacked connection` is that refusal, and it is
// what the deferred commit provokes today.
//
// So: commit, then mark streaming so nothing else is buffered, then delegate. The
// budget cannot be enforced past this point, and that is correct rather than a gap
// — a hijacked connection is not a response `Timeout` can substitute a 504 into,
// and a handler that has taken the socket is by definition past the point where
// buffering has meaning. `play` bounds its own read side with `ReadTimeout` and
// bounds its own lifetime with `hub.Close`, which is the correct arrangement: the
// budget that a socket cannot obey is replaced by two that it can.
//
// Idempotent against the deferred `commit`: `commit` is guarded by `committed`,
// which the call below sets, so the handler's return writes nothing further.
//
// # Error path
//
// A parent that cannot be hijacked — HTTP/2, where there is no connection to take
// over — returns `errNotHijackable`, the same sentinel `statusRecorder.Hijack`
// uses. It is *not* a silent success and *not* a commit: a handler that was refused
// its hijack still has a response to send, and the buffer is left intact so the
// deferred commit delivers it.
func (tw *timeoutWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := tw.ResponseWriter.(http.Hijacker)
	if !ok {
		// Not committed, deliberately. See the error path above: the handler is
		// going to return and the deferred commit is what answers it, and it can
		// still substitute a 504 because nothing has been written yet.
		return nil, nil, errNotHijackable
	}

	// The load-bearing line. Before the hijack, because after it `net/http` owns
	// nothing and the 101 is discarded.
	tw.commit(false)

	// Past the point where buffering means anything: a hijacked connection has no
	// response left for a 504 to replace. Also what keeps a `Write` after the
	// hijack from being swallowed into a buffer nobody will ever commit.
	tw.streaming = true

	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, fmt.Errorf("hijack connection: %w", err)
	}

	return conn, buffered, nil
}

// writeBody writes a complete response body, discarding the write error.
//
// Every call site has already committed — or is about to commit — a status, and
// has nowhere to report a failure to: the client has gone, or the connection is
// about to close. Propagating the error would mean every caller in this file has
// to decide what to do about a write that cannot succeed.
//
// The status is not set here. commit has already written it, and a second
// WriteHeader would be ignored anyway; a caller that has not written a status
// yet sets one before calling, so the two concerns stay in one place each.
func writeBody(w http.ResponseWriter, body string) {
	//nolint:errcheck // A failed write here is unactionable; the status is already committed.
	_, _ = w.Write([]byte(body))
}
