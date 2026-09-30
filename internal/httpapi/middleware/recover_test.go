package middleware_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/semiplane/semiplane/internal/httpapi/middleware"
)

// logCapture collects slog records so a test can assert on levels and
// attributes.
type logCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, record slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.records = append(c.records, record.Clone())

	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }

// WithGroup is required by slog.Handler. Returning c rather than a wrapper
// keeps every record in one slice, which is what makes assertion simple.
func (c *logCapture) WithGroup(string) slog.Handler { return c }

// messages returns each record's message, in order.
func (c *logCapture) messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	messages := make([]string, 0, len(c.records))
	for idx := range c.records {
		messages = append(messages, c.records[idx].Message)
	}

	return messages
}

// levels returns each record's level, in order.
func (c *logCapture) levels() []slog.Level {
	c.mu.Lock()
	defer c.mu.Unlock()

	levels := make([]slog.Level, 0, len(c.records))
	for idx := range c.records {
		levels = append(levels, c.records[idx].Level)
	}

	return levels
}

// attr returns the first value logged under a key, and whether it was present.
func (c *logCapture) attr(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for idx := range c.records {
		var (
			value   string
			present bool
		)

		c.records[idx].Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				value = a.Value.String()
				present = true
			}

			return true
		})

		if present {
			return value, true
		}
	}

	return "", false
}

func (c *logCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.records)
}

func newTestLogger() (*slog.Logger, *logCapture) {
	capture := &logCapture{}

	return slog.New(capture), capture
}

// discardLogger returns a logger that throws output away, for tests that only
// care about the response.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// TestRecovererTurnsPanicInto500 covers the difference from net/http's own
// recovery, which closes the connection without a response. A browser reports
// that as a network error rather than a 500, and a client retrying a failed
// write has no status to branch on.
func TestRecovererTurnsPanicInto500(t *testing.T) {
	t.Parallel()

	logger, capture := newTestLogger()

	handler := middleware.Recoverer(logger)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			panic("boom")
		},
	))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/wiki/page", http.NoBody),
	)

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", recorder.Code)
	}

	if got := capture.levels(); len(got) != 1 || got[0] != slog.LevelError {
		t.Errorf("levels = %v, want exactly one error record", got)
	}
}

// TestRecovererKeepsPanicOutOfTheResponse is the security half. A Go stack
// trace discloses file paths, package structure, and often credentials held in
// package-level variables, and the response is visible to whoever made the
// request.
func TestRecovererKeepsPanicOutOfTheResponse(t *testing.T) {
	t.Parallel()

	handler := middleware.Recoverer(discardLogger())(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			panic("super-secret-token-value")
		},
	))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody),
	)

	body := recorder.Body.String()

	for _, forbidden := range []string{"super-secret-token-value", "goroutine", ".go:"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("response body contains %q: %s", forbidden, body)
		}
	}
}

// TestRecovererLogsThePanic is the operator half: a panic whose text never
// reaches the log is a bug nobody can diagnose.
func TestRecovererLogsThePanic(t *testing.T) {
	t.Parallel()

	logger, capture := newTestLogger()

	handler := middleware.Chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("the-actual-cause")
		}),
		middleware.RequestID,
		middleware.Recoverer(logger),
	)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"/c/greyhaven/wiki/goblin",
			http.NoBody,
		),
	)

	messages := capture.messages()
	if len(messages) != 1 || messages[0] != "http.panic" {
		t.Fatalf("messages = %v, want one http.panic", messages)
	}

	if _, ok := capture.attr("stack"); !ok {
		t.Error("no stack in the log; the stack is the only part of a panic that says where")
	}

	if _, ok := capture.attr("request_id"); !ok {
		t.Error("no request_id on the panic record; it cannot be tied to a request")
	}
}

// TestRecovererLeavesNonPanickingHandlersAlone is the regression guard: a
// recovery layer that writes something unconditionally would corrupt every
// successful response.
func TestRecovererLeavesNonPanickingHandlersAlone(t *testing.T) {
	t.Parallel()

	logger, capture := newTestLogger()

	handler := middleware.Recoverer(logger)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("created"))
		},
	))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", http.NoBody),
	)

	if recorder.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", recorder.Code)
	}

	if got := recorder.Body.String(); got != "created" {
		t.Errorf("body = %q, want %q", got, "created")
	}

	if got := capture.count(); got != 0 {
		t.Errorf("logged %d records for a successful request, want 0", got)
	}
}

// TestRecovererHandlesPanicAfterStatusCommitted covers the awkward case: the
// handler has already told the client the request succeeded, and then panics.
// The connection must still terminate and the panic must still be recorded, or
// the failure is invisible to both sides.
func TestRecovererHandlesPanicAfterStatusCommitted(t *testing.T) {
	t.Parallel()

	logger, capture := newTestLogger()

	handler := middleware.Recoverer(logger)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("partial"))
			panic("after-commit")
		},
	))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody),
	)

	// The committed status stands: it is already on the wire and a second
	// WriteHeader would be ignored anyway.
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want the already-committed 200", recorder.Code)
	}

	if got := capture.levels(); len(got) != 1 || got[0] != slog.LevelError {
		t.Errorf("levels = %v, want the panic recorded at error", got)
	}
}
