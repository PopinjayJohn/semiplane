package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type requestIDKey struct{}

// RequestIDHeader is both the request header read for a correlation id and the
// response header echoed back with the generated one. One name in both
// directions: a client that sends a bad value and a client that sends none
// should see the same header in the reply.
const RequestIDHeader = "X-Request-Id"

// maxInboundRequestID bounds what an inbound header can inject into a log
// line. An unauthenticated caller controls this value, and log aggregation is
// exactly where an unescaped newline turns into a forged log entry.
const maxInboundRequestID = 64

// requestIDPrefix distinguishes a server-generated id from a client-supplied
// one in a log line. A 32-hex-char inbound value is well-formed and
// indistinguishable from ours, so the prefix is the only way to tell them
// apart — which matters when reading a trace and asking whether the id can be
// trusted as ours.
const (
	generatedPrefix = "gen-"
	forwardedPrefix = "fwd-"
)

// RequestIDFrom returns the correlation id carried by the context, and whether
// one was present. The second return is false for a request that never passed
// through the RequestID middleware, which lets a log line distinguish "no id"
// from "the id was the empty string".
func RequestIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey{}).(string)

	return id, ok
}

// MustRequestID returns the correlation id, or the empty string. For call
// sites — mostly handlers and templates — where a missing id is already a bug
// in the chain rather than a condition to branch on.
func MustRequestID(ctx context.Context) string {
	id, _ := RequestIDFrom(ctx)

	return id
}

// WithRequestID returns a context carrying the given correlation id. Exported
// for tests and for any code that constructs a request context outside the
// middleware chain.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID generates a correlation id, puts it on the request context, and
// echoes it on the response.
//
// An inbound id is honoured when it is short, printable ASCII — that is what
// makes a request traceable across a reverse proxy — and replaced with a
// generated one otherwise. Honouring it unvalidated is how a caller injects a
// newline into every log line carrying that id, and this header is
// unauthenticated by construction.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := inboundRequestID(r.Header.Get(RequestIDHeader))
		if !ok {
			id = newRequestID()
		}

		// Set on the response before the handler runs. A handler that writes a
		// body and then fails has already committed the status line, and a
		// header set afterwards is silently dropped — which loses the id on
		// exactly the responses worth correlating.
		w.Header().Set(RequestIDHeader, id)

		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}

// inboundRequestID validates a caller-supplied id and returns it with its
// prefix marker.
func inboundRequestID(raw string) (string, bool) {
	if raw == "" || len(raw) > maxInboundRequestID {
		return "", false
	}

	// Printable ASCII only. Anything else — a control character, a newline, a
	// NUL — is a caller writing into a log format, and this header cannot
	// carry a legitimate one.
	for _, r := range raw {
		if r < 0x20 || r > 0x7e {
			return "", false
		}
	}

	return forwardedPrefix + raw, true
}

// newRequestID returns a 128-bit random id. crypto/rand rather than a counter
// because the id is attacker-visible: a guessable sequence is a counter, and a
// counter correlates traffic across requests.
func newRequestID() string {
	var buf [16]byte

	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand's Read cannot fail on any platform this builds for, and
		// the alternative is worse: a predictable or constant id in every log
		// line is harder to notice than a startup crash.
		panic("middleware: crypto/rand unavailable: " + err.Error())
	}

	return generatedPrefix + hex.EncodeToString(buf[:])
}
