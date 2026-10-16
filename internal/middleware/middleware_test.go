package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestIDSetAndEchoed(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
	})
	h := RequestIDMW(next)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get(RequestIDHeader) == "" {
		t.Fatal("response missing X-Request-ID")
	}
	if seen == "" {
		t.Fatal("request context missing request id")
	}
	// Client-supplied IDs pass through when sane.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set(RequestIDHeader, "client-1")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Header().Get(RequestIDHeader) != "client-1" {
		t.Fatalf("client id not honored: %q", rec2.Header().Get(RequestIDHeader))
	}
}

func TestRateLimit429IsProblemJSON(t *testing.T) {
	l := NewLimiter(2, time.Minute)
	h := RateLimit(l)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	var last *httptest.ResponseRecorder
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "1.2.3.4:5678"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		last = rec
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("third request = %d, want 429", last.Code)
	}
	if ct := last.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q, want problem+json", ct)
	}
}

func TestRecoveryConvertsPanic(t *testing.T) {
	var logged bool
	h := Recovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), func(any, []byte) { logged = true })
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q, want problem+json", ct)
	}
	if !logged || strings.Contains(rec.Body.String(), "boom") {
		t.Fatal("panic must be logged server-side, never leaked to the client")
	}
}
