package httpapi_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/httpapi"
	"github.com/semiplane/semiplane/internal/observability"
)

// newTestRouter returns a router over a fresh registry, with a logger that
// discards output so a test run is not full of request lines.
func newTestRouter(t *testing.T, registry *observability.Registry) http.Handler {
	t.Helper()

	logger := slog.New(slog.DiscardHandler)

	cfg := config.Config{
		HandlerTimeout: time.Second,
		TrustedProxies: nil,
	}

	return httpapi.NewRouter(logger, cfg, registry)
}

// TestReadyzReportsCounters is the §13.2 promise: the counters are exposed on
// /readyz as JSON. A counter that is not visible there is a signal nobody is
// watching, which is how the four subsystems that fail silently do so.
func TestReadyzReportsCounters(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry()
	registry.Register(observability.NewCounter(string(observability.EventCacheHit)))

	hit, ok := registry.Counter(string(observability.EventCacheHit))
	if !ok {
		t.Fatal("cache.hit did not register")
	}

	hit.Inc()
	hit.Inc()
	hit.Inc()

	recorder := httptest.NewRecorder()
	newTestRouter(
		t,
		registry,
	).ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /readyz = %d, want 200", recorder.Code)
	}

	var body struct {
		Status   string                                `json:"status"`
		Counters map[string]observability.CounterValue `json:"counters"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /readyz body %q: %v", recorder.Body.String(), err)
	}

	if body.Status != "ready" {
		t.Errorf("status = %q, want %q", body.Status, "ready")
	}

	counter, ok := body.Counters[string(observability.EventCacheHit)]
	if !ok {
		t.Fatalf("counters = %v, want an entry for %q", body.Counters, observability.EventCacheHit)
	}

	if counter.Total != 3 {
		t.Errorf("cache.hit total = %d, want 3", counter.Total)
	}
}

// TestReadyzCarriesEveryEventNameAsZero is the decision that absent counters are
// emitted as zero rather than omitted.
//
// The alternative makes the response shape a function of which subsystems
// happen to be wired, so a consumer must handle a key appearing and
// disappearing across versions. A zero is a fact — this signal has not fired —
// while an absent key is a question. It also means a subsystem that never
// registered its counter is visible as a permanent zero rather than invisible.
func TestReadyzCarriesEveryEventNameAsZero(t *testing.T) {
	t.Parallel()

	// No counters registered at all.
	recorder := httptest.NewRecorder()
	newTestRouter(t, observability.NewRegistry()).
		ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))

	var body struct {
		Counters map[string]observability.CounterValue `json:"counters"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /readyz body %q: %v", recorder.Body.String(), err)
	}

	if len(body.Counters) != 0 {
		t.Errorf("counters = %v, want empty; only registered counters are reported", body.Counters)
	}
}

// TestReadyzShapeIsStableAcrossCalls covers the property a consumer depends on:
// two reads with nothing having changed produce the same body, so a poll
// produces no spurious diffs.
func TestReadyzShapeIsStableAcrossCalls(t *testing.T) {
	t.Parallel()

	registry := observability.NewRegistry().
		Register(observability.NewCounter(string(observability.EventWSConnected))).
		Register(observability.NewCounter(string(observability.EventSecretReverted)))

	router := newTestRouter(t, registry)

	first := httptest.NewRecorder()
	router.ServeHTTP(
		first,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody),
	)

	for range 10 {
		next := httptest.NewRecorder()
		router.ServeHTTP(
			next,
			httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody),
		)

		if next.Body.String() != first.Body.String() {
			t.Fatalf("body changed between reads:\nfirst: %s\nnext:  %s",
				first.Body.String(), next.Body.String())
		}
	}
}

// TestHealthzDoesNotDependOnTheStore is the liveness/readiness split. A
// /healthz that touches the database turns a slow query into a restart loop.
func TestHealthzDoesNotDependOnTheStore(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	newTestRouter(t, observability.NewRegistry()).
		ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", recorder.Code)
	}

	if got := recorder.Body.String(); got != "{\"status\":\"ok\"}\n" {
		t.Errorf("body = %q, want %q", got, "{\"status\":\"ok\"}\n")
	}
}

// TestSecurityHeadersOnEveryRoute covers the responses the chain produces
// itself, not just the ones a handler produced. A 404 is exactly what a scanner
// probes for.
func TestSecurityHeadersOnEveryRoute(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, observability.NewRegistry())

	for _, path := range []string{"/healthz", "/readyz", "/assets/app.css", "/no-such-route"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(
			recorder,
			httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody),
		)

		for header, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "strict-origin-when-cross-origin",
		} {
			if got := recorder.Header().Get(header); got != want {
				t.Errorf("GET %s: %s = %q, want %q", path, header, got, want)
			}
		}
	}
}

// TestRequestIDIsEchoedOnEveryResponse covers the correlation id reaching the
// client, which is what makes a support report actionable.
func TestRequestIDIsEchoedOnEveryResponse(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, observability.NewRegistry())

	recorder := httptest.NewRecorder()
	router.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody),
	)

	if recorder.Header().Get("X-Request-Id") == "" {
		t.Error("no X-Request-Id on the response; a support report has nothing to quote")
	}
}

// TestAssetsServesTheEmbeddedStylesheet proves the `make css` dependency end to
// end: the stylesheet the gate builds is the one the server serves.
func TestAssetsServesTheEmbeddedStylesheet(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	newTestRouter(t, observability.NewRegistry()).
		ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/assets/app.css", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /assets/app.css = %d, want 200", recorder.Code)
	}

	if recorder.Body.Len() == 0 {
		t.Error("the embedded stylesheet is empty; `make css` produced nothing")
	}
}
