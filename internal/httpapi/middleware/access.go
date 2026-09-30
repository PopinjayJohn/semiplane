package middleware

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"
)

type clientIPKey struct{}

// WithClientIP returns a context carrying the resolved client address. Exported
// for tests and for code that builds a request context outside the chain.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip)
}

// ClientIP returns the resolved client address and whether one was resolved.
func ClientIP(ctx context.Context) (string, bool) {
	ip, ok := ctx.Value(clientIPKey{}).(string)

	return ip, ok
}

// MustClientIP returns the resolved client address, or the empty string. For
// call sites where a missing address is a chain-wiring bug rather than a
// condition to branch on.
func MustClientIP(ctx context.Context) string {
	ip, _ := ClientIP(ctx)

	return ip
}

// RealIP resolves the client's address from the forwarding headers, and refuses
// to invent one.
//
// Refusal is the point. Every rate limit, every access-log line, and every
// future ban decision reads this value, and a forwarding header is
// caller-controlled the moment the server is reachable through anything. The
// trusted-proxy list is therefore the only thing that decides whether a header
// is believed, and a request from an untrusted peer contributes nothing.
func RealIP(trustedProxies []string) func(http.Handler) http.Handler {
	trusted := parseTrustedProxies(trustedProxies)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := resolveClientIP(trusted, r)

			next.ServeHTTP(w, r.WithContext(WithClientIP(r.Context(), ip)))
		})
	}
}

// parseTrustedProxies turns a configured list into prefixes, dropping entries
// that do not parse.
//
// Dropping rather than failing is deliberate: the list is operator-supplied,
// and a typo in a proxy address must not stop the server from booting. The cost
// is that one bad entry silently trusts nothing, which fails toward the
// transport address — the safe direction.
func parseTrustedProxies(values []string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(values))

	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}

		prefix, err := netip.ParsePrefix(trimmed)
		if err != nil {
			// A bare address is a common way to write a single-host proxy, so
			// accept it and treat it as a /32 or /128 rather than rejecting an
			// entry the operator clearly meant.
			addr, addrErr := netip.ParseAddr(trimmed)
			if addrErr != nil {
				continue
			}

			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}

		prefixes = append(prefixes, prefix.Masked())
	}

	return prefixes
}

// resolveClientIP walks the forwarding chain right to left and returns the
// first address that did not come from a trusted proxy.
//
// Right to left is the only correct order. Each hop appends the address it saw,
// so the list is nearest-first and its rightmost entry is the one closest to
// this server. A left-to-right walk returns the leftmost value — which any
// client sets freely — as the client's address.
func resolveClientIP(trusted []netip.Prefix, r *http.Request) string {
	remote, remoteOK := remoteAddr(r)

	// Each header may itself hold a comma-separated chain, and the chain runs
	// right to left within the header as well, so the two loops nest with the
	// same direction.
	for _, header := range r.Header.Values("X-Forwarded-For") {
		hops := splitHeaderList(header)
		for _, hop := range slices.Backward(hops) {
			addr, err := netip.ParseAddr(hop)
			if err != nil {
				// A malformed hop means the chain cannot be trusted beyond
				// this point. Falling through to a hop further left would
				// launder a client-chosen value into the answer, so the
				// transport address is the only safe result here.
				return resolvedOr(remote, remoteOK)
			}

			if !isTrusted(addr, trusted) {
				return addr.Unmap().String()
			}
		}
	}

	// X-Real-IP predates the standard and is single-valued. Honoured only
	// because a client behind a trusted proxy may send it, and scanned for the
	// same reason X-Forwarded-For is: the peer decides what arrives here.
	if header := strings.TrimSpace(r.Header.Get("X-Real-IP")); header != "" {
		if addr, err := netip.ParseAddr(header); err == nil && !isTrusted(addr, trusted) {
			return addr.Unmap().String()
		}
	}

	return resolvedOr(remote, remoteOK)
}

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}

// remoteAddr returns the transport peer address with its port stripped. The
// port is never part of a client identity, and keeping it makes every
// comparison against an allowlist fail.
func remoteAddr(r *http.Request) (string, bool) {
	if r.RemoteAddr == "" {
		return "", false
	}

	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host, true
	}

	// No port: a unix socket, or a test that set a bare address.
	return r.RemoteAddr, true
}

// resolvedOr returns the transport address when there is one, and a value that
// says so plainly when there is not. httptest.NewRequest leaves RemoteAddr
// empty, and an empty client IP in a log line is worse than one that admits it
// is unknown.
func resolvedOr(remote string, ok bool) string {
	if ok {
		return remote
	}

	return "unknown"
}

func splitHeaderList(value string) []string {
	parts := strings.Split(value, ",")
	hops := make([]string, 0, len(parts))

	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			hops = append(hops, trimmed)
		}
	}

	return hops
}

// Log records one structured line per request, carrying the correlation id.
//
// The line is emitted after the handler returns, because a log entry written
// before the response cannot know the status or the size — and a request log
// without a status is close to useless when diagnosing why someone saw a 500.
func Log(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(recorder, r)

			logger.LogAttrs(r.Context(), levelForStatus(recorder.status),
				"http.request",
				slog.String("request_id", MustRequestID(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("client_ip", MustClientIP(r.Context())),
				slog.Int("status", recorder.status),
				slog.Int("bytes", recorder.written),
				slog.Duration("elapsed", time.Since(started)),
			)
		})
	}
}

// levelForStatus puts a server fault at error and a client mistake at warn.
// Logging 404s at error is how a log becomes unreadable, and it teaches
// whoever reads it to skip the errors.
func levelForStatus(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// statusRecorder captures the status and size of a response.
//
// It implements http.Flusher, http.Hijacker and Unwrap alongside the three
// ResponseWriter methods. A wrapper that omits an optional interface disables
// the feature silently, and the symptom appears far from the cause — an event
// stream that never flushes, a WebSocket that cannot upgrade.
type statusRecorder struct {
	http.ResponseWriter

	status      int
	written     int
	wroteHeader bool
}

func (sr *statusRecorder) WriteHeader(status int) {
	if sr.wroteHeader {
		return
	}

	sr.wroteHeader = true
	sr.status = status

	sr.ResponseWriter.WriteHeader(status)
}

func (sr *statusRecorder) Write(body []byte) (int, error) {
	sr.wroteHeader = true

	written, err := sr.ResponseWriter.Write(body)
	sr.written += written

	if err != nil {
		// Wrapped so a caller can tell a transport failure from a handler
		// failure. The byte count passes through unchanged: it is what the
		// handler wrote, which is what a log line wants.
		return written, fmt.Errorf("write response: %w", err)
	}

	return written, nil
}

func (sr *statusRecorder) Flush() {
	flusher, ok := sr.ResponseWriter.(http.Flusher)
	if !ok {
		return
	}

	if !sr.wroteHeader {
		sr.WriteHeader(http.StatusOK)
	}

	flusher.Flush()
}

func (sr *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := sr.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errNotHijackable
	}

	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, fmt.Errorf("hijack connection: %w", err)
	}

	return conn, rw, nil
}

// Unwrap exposes the wrapped writer, so an outer wrapper can still reach
// interfaces this layer does not implement.
func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
}

// errNotHijackable is returned when the underlying writer cannot be hijacked,
// which in practice means HTTP/2, where there is no connection to take over.
var errNotHijackable = errors.New("middleware: response writer is not an http.Hijacker")
