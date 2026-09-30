package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/httpapi/middleware"
)

// requestIDHandler reports the correlation id it was given, so the context
// plumbing and the response header can be asserted from one place.
func requestIDHandler(observed *string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*observed = middleware.MustRequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequestIDGeneratesWhenAbsent(t *testing.T) {
	t.Parallel()

	var observed string

	recorder := httptest.NewRecorder()
	middleware.RequestID(requestIDHandler(&observed)).
		ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

	if observed == "" {
		t.Fatal("no request id on the context")
	}

	if !strings.HasPrefix(observed, "gen-") {
		t.Errorf("id = %q, want the gen- prefix for one this server generated", observed)
	}

	if got := recorder.Header().Get(middleware.RequestIDHeader); got != observed {
		t.Errorf("response header = %q, want the context id %q", got, observed)
	}
}

func TestRequestIDsAreDistinct(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 50)

	for range 50 {
		var observed string

		middleware.RequestID(requestIDHandler(&observed)).
			ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

		if _, dup := seen[observed]; dup {
			t.Fatalf("duplicate request id %q; a predictable id is a counter, and a counter "+
				"correlates traffic across requests", observed)
		}

		seen[observed] = struct{}{}
	}
}

func TestRequestIDHonoursInboundValue(t *testing.T) {
	t.Parallel()

	var observed string

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set(middleware.RequestIDHeader, "abc123")

	recorder := httptest.NewRecorder()
	middleware.RequestID(requestIDHandler(&observed)).ServeHTTP(recorder, req)

	// Marked fwd- rather than gen-, because a 32-hex-character inbound value is
	// well-formed and indistinguishable from a generated one. The prefix is the
	// only way to tell in a log line whether the id can be trusted as ours.
	if !strings.HasPrefix(observed, "fwd-") {
		t.Errorf("id = %q, want the fwd- prefix for a client-supplied value", observed)
	}

	if !strings.HasSuffix(observed, "abc123") {
		t.Errorf("id = %q, want it to end with the supplied value", observed)
	}
}

// TestRequestIDRejectsUntrustedInboundValues is the security case.
//
// X-Request-Id is unauthenticated by construction. An inbound value that is
// copied verbatim into a log line is a caller writing newlines into every log
// line carrying that id, which is how a forged entry gets into a log
// aggregator that nobody questions.
func TestRequestIDRejectsUntrustedInboundValues(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value string
	}{
		{"newline", "abc\ndef"},
		{"carriage return", "abc\rdef"},
		{"null byte", "abc\x00def"},
		{"tab", "abc\tdef"},
		{"escape", "abc\x1b[31mdef"},
		{"non-ascii", "abc-ünïcodé-def"},
		{"too long", strings.Repeat("a", 65)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var observed string

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
			req.Header.Set(middleware.RequestIDHeader, testCase.value)

			recorder := httptest.NewRecorder()
			middleware.RequestID(requestIDHandler(&observed)).ServeHTTP(recorder, req)

			if !strings.HasPrefix(observed, "gen-") {
				t.Errorf(
					"id = %q, want a generated one; the inbound value was not rejected",
					observed,
				)
			}

			// The header the client sees must be the safe one too, or a caller
			// gets its injection echoed back for someone else to log.
			if echoed := recorder.Header().
				Get(middleware.RequestIDHeader); !strings.HasPrefix(
				echoed,
				"gen-",
			) {
				t.Errorf("echoed header = %q, want a generated id", echoed)
			}
		})
	}
}

func TestRequestIDAcceptsBoundaryLength(t *testing.T) {
	t.Parallel()

	// 64 characters is the limit and must be accepted; the rejection test above
	// covers 65. Without this, the limit could silently be off by one.
	inbound := strings.Repeat("a", 64)

	var observed string

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set(middleware.RequestIDHeader, inbound)

	middleware.RequestID(requestIDHandler(&observed)).ServeHTTP(
		httptest.NewRecorder(), req,
	)

	if !strings.HasSuffix(observed, inbound) {
		t.Errorf("id = %q, want a 64-character value to be accepted", observed)
	}
}

func TestRequestIDFromReportsAbsence(t *testing.T) {
	t.Parallel()

	// A request that never passed through the middleware has no id, and the
	// caller needs to tell that from an id that happens to be empty.
	if _, ok := middleware.RequestIDFrom(t.Context()); ok {
		t.Error("RequestIDFrom reported an id on a context that never had one")
	}

	if got := middleware.MustRequestID(t.Context()); got != "" {
		t.Errorf("MustRequestID = %q, want empty", got)
	}

	recorder := httptest.NewRecorder()
	middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := middleware.RequestIDFrom(r.Context())
		if !ok {
			t.Error("RequestIDFrom reported no id on a request that passed through the middleware")
		}

		if got != middleware.MustRequestID(r.Context()) {
			t.Errorf("RequestIDFrom = %q, MustRequestID = %q; they must agree",
				got, middleware.MustRequestID(r.Context()))
		}

		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
}

func TestWithRequestIDIsUsableOutsideTheChain(t *testing.T) {
	t.Parallel()

	ctx := middleware.WithRequestID(t.Context(), "fwd-manual")

	if got := middleware.MustRequestID(ctx); got != "fwd-manual" {
		t.Errorf("MustRequestID = %q, want %q", got, "fwd-manual")
	}
}
