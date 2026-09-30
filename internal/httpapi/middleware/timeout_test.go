package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/httpapi/middleware"
)

// shortBudget is the timeout used by the tests that need one to expire. Long
// enough that a loaded machine does not expire it by accident, short enough
// that the suite stays fast.
const shortBudget = 50 * time.Millisecond

// generousBudget never expires during a test.
const generousBudget = 30 * time.Second

// TestTimeoutPassesThroughAnInBudgetResponse is the ordinary case: a handler
// that returns in time gets exactly what it wrote, byte for byte.
func TestTimeoutPassesThroughAnInBudgetResponse(t *testing.T) {
	t.Parallel()

	handler := middleware.Timeout(generousBudget)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("hello"))
		},
	))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody),
	)

	if recorder.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", recorder.Code)
	}

	if got := recorder.Body.String(); got != "hello" {
		t.Errorf("body = %q, want %q", got, "hello")
	}

	if got := recorder.Header().Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type = %q, want %q", got, "text/plain")
	}
}

// TestTimeoutSubstitutes504AfterAPartialWrite is the load-bearing property, and
// the reason the writer buffers at all.
//
// The handler has already written 10KB of a page and *then* overruns. Its status
// line is committed at the moment it began writing, so without buffering there
// is no way to substitute a 504 — the client receives a truncated 200 and never
// learns the response was incomplete.
func TestTimeoutSubstitutes504AfterAPartialWrite(t *testing.T) {
	t.Parallel()

	handler := middleware.Timeout(shortBudget)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(strings.Repeat("a", 1024)))

			// Block until the budget is gone, then keep writing: this is the
			// handler that would have streamed a truncated page.
			<-r.Context().Done()
			_, _ = w.Write([]byte("more"))
		},
	))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody),
	)

	if recorder.Code != http.StatusGatewayTimeout {
		t.Errorf(
			"status = %d, want 504; a partial write must not be served as a 200",
			recorder.Code,
		)
	}

	if strings.Contains(recorder.Body.String(), "aaaa") {
		t.Error("the partial body was served; the buffered content must be discarded on timeout")
	}

	if !strings.Contains(recorder.Body.String(), "timed out") {
		t.Errorf("body = %q, want a timeout message", recorder.Body.String())
	}
}

// TestTimeoutCancelsTheDownstreamContext is the mechanism the whole design rests
// on: a handler blocked on a database read has to learn its budget is gone, or
// the deadline is decorative.
func TestTimeoutCancelsTheDownstreamContext(t *testing.T) {
	t.Parallel()

	var (
		mu           sync.Mutex
		cancelled    bool
		deadlineSet  bool
		seenDeadline time.Time
	)

	handler := middleware.Timeout(shortBudget)(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			deadline, ok := r.Context().Deadline()
			if ok {
				mu.Lock()
				deadlineSet = true
				seenDeadline = deadline
				mu.Unlock()
			}

			<-r.Context().Done()

			mu.Lock()
			cancelled = true
			mu.Unlock()
		},
	))

	handler.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody),
	)

	mu.Lock()
	defer mu.Unlock()

	if !deadlineSet {
		t.Error("no deadline on the context the handler received")
	}

	if seenDeadline.IsZero() {
		t.Error("the deadline is zero")
	}

	if !cancelled {
		t.Error("the handler's context was never cancelled; a blocked handler would hang forever")
	}
}

// TestTimeoutDoesNotFireOnAnInBudgetHandler is the regression guard for an
// off-by-one that would 504 every slow-but-legitimate request.
func TestTimeoutDoesNotFireOnAnInBudgetHandler(t *testing.T) {
	t.Parallel()

	handler := middleware.Timeout(generousBudget)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
				t.Error("the budget expired during a handler with a 30s budget")
			case <-time.After(time.Millisecond):
			}

			_, _ = w.Write([]byte("done"))
		},
	))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody),
	)

	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorder.Code)
	}

	if got := recorder.Body.String(); got != "done" {
		t.Errorf("body = %q, want %q", got, "done")
	}
}

// TestTimeoutIsTransparentToFlushedResponses is the server-sent-events case, and
// the reason the writer has a streaming mode at all.
//
// An SSE handler's entire job is to deliver bytes as they happen. Held in a
// buffer until the handler returns — which for a stream is when the client
// disconnects — it delivers nothing at all. So the first Flush is the commit
// point: the buffer goes out, the writer goes transparent, and no 504 is
// appended to a live event stream.
func TestTimeoutIsTransparentToFlushedResponses(t *testing.T) {
	t.Parallel()

	flushed := make(chan struct{}, 4)

	handler := middleware.Timeout(generousBudget)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Error(
					"the wrapped writer is not an http.Flusher; SSE cannot work through this chain",
				)

				return
			}

			w.Header().Set("Content-Type", "text/event-stream")

			for _, event := range []string{"data: one\n\n", "data: two\n\n"} {
				_, _ = w.Write([]byte(event))
				flusher.Flush()
				flushed <- struct{}{}
			}

			// Hold the stream open after both events: the point is that the
			// writer stays transparent rather than re-buffering.
			<-r.Context().Done()
		}),
	)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	recorder := httptest.NewRecorder()
	done := make(chan struct{})

	go func() {
		defer close(done)
		handler.ServeHTTP(recorder,
			httptest.NewRequestWithContext(ctx, http.MethodGet, "/c/x/events", http.NoBody))
	}()

	for range 2 {
		select {
		case <-flushed:
		case <-time.After(2 * time.Second):
			t.Fatal("the handler never flushed; a buffered SSE stream delivers nothing")
		}
	}

	// Read what reached the recorder while the handler was still running: the
	// point is that the bytes are already out, not delivered at the end.
	live := recorder.Body.String()
	if !strings.Contains(live, "data: one") {
		t.Errorf("body during the stream = %q, want the first event already delivered", live)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler did not return after its context was cancelled")
	}

	body := recorder.Body.String()

	if strings.Contains(body, "504") || strings.Contains(body, "timed out") {
		t.Errorf("an error document was appended to a live event stream: %s", body)
	}

	if !strings.Contains(body, "data: two") {
		t.Errorf("body = %q, want both events", body)
	}
}

// TestTimeoutDefaultStatusIsOK covers a handler that writes a body without
// calling WriteHeader. net/http would default to 200, and the buffered commit
// has to match what the handler would have produced unwrapped.
func TestTimeoutDefaultStatusIsOK(t *testing.T) {
	t.Parallel()

	handler := middleware.Timeout(generousBudget)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("implicit"))
		},
	))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody),
	)

	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for a handler that never called WriteHeader", recorder.Code)
	}
}

// TestTimeoutHonoursAHeaderSetWithoutABody covers the Header delegation: a
// handler that sets a header and writes nothing still has to have it applied.
func TestTimeoutHonoursAHeaderSetWithoutABody(t *testing.T) {
	t.Parallel()

	handler := middleware.Timeout(generousBudget)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			w.WriteHeader(http.StatusNoContent)
		},
	))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody),
	)

	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q, want it applied", got)
	}
}
