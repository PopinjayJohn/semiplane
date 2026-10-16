// Package middleware owns the HTTP cross-cutting chain (repo-layout:
// internal/middleware; wired by internal/app around internal/api).
//
// Chain order: RequestID → Logging (slog, request-scoped) → RateLimit →
// Recovery. Errors converge on api.WriteProblem (application/problem+json) —
// the single error path (tech-stack §9); middleware never invents its own
// error shape.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// requestIDKey scopes the request ID in the context for slog handlers.
type requestIDKey struct{}

// RequestID returns the request's ID from ctx ("" when unset).
func RequestID(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey{}).(string)
	return v
}

// RequestIDHeader is echoed back on every response for trace correlation.
const RequestIDHeader = "X-Request-ID"

// RequestID assigns a random 128-bit hex ID per request, honoring a client
// supplied X-Request-ID when it is 1..64 printable ASCII bytes.
func RequestIDMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validClientID(id) {
			id = newID()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func validClientID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "fallback-id"
	}
	return hex.EncodeToString(b[:])
}

// Logging emits one slog line per request (method, path, status, bytes,
// duration, request_id). JSON in prod, text locally — the handler owns the
// format; this middleware only supplies the fields.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", RequestID(r.Context()),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Limiter is a per-key fixed-window rate limiter.
type Limiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	max    int
	window time.Duration
}

// NewLimiter returns a limiter allowing max requests per window per key.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{hits: make(map[string][]time.Time), max: max, window: window}
}

// Allow reports whether key may proceed, recording the hit.
func (l *Limiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

// RateLimit rejects with 429 problem+json when the client IP exceeds max
// requests per window. Tests construct small limits; production wiring
// picks generous defaults so table devices never trip it.
func RateLimit(l *Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(r.RemoteAddr) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"type":"about:blank","title":"rate limited","status":429}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
