package middleware_test

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/semiplane/semiplane/internal/httpapi/middleware"
)

// TestLogEmitsExactlyOneLine covers the accounting: a request log with two
// lines for one request, or none, is worse than no log at all.
func TestLogEmitsExactlyOneLine(t *testing.T) {
	t.Parallel()

	logger, capture := newTestLogger()

	middleware.Log(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody))

	if got := capture.count(); got != 1 {
		t.Fatalf("logged %d records for one request, want 1", got)
	}

	if got := capture.messages(); got[0] != "http.request" {
		t.Errorf("message = %q, want %q", got[0], "http.request")
	}
}

// TestLogCarriesTheDiagnosticFields is the assertion that a support report is
// actionable: every field here is something somebody asks for.
func TestLogCarriesTheDiagnosticFields(t *testing.T) {
	t.Parallel()

	logger, capture := newTestLogger()

	middleware.Chain(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}),
		middleware.RequestID,
		middleware.RealIP(nil),
		middleware.Log(logger),
	).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/c/greyhaven/wiki/goblin", http.NoBody),
	)

	for key, want := range map[string]string{
		"method":    http.MethodDelete,
		"path":      "/c/greyhaven/wiki/goblin",
		"status":    "404",
		"client_ip": "192.0.2.1",
	} {
		if got, ok := capture.attr(key); !ok {
			t.Errorf("no %q attribute was logged", key)
		} else if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	if id, ok := capture.attr("request_id"); !ok || id == "" {
		t.Error("no request_id on the access log line; it cannot be tied to a response")
	}

	if _, ok := capture.attr("bytes"); !ok {
		t.Error("no bytes attribute; a size is what distinguishes an empty page from a failed one")
	}
}

// TestLogLevelsSeparateServerFaultsFromClientMistakes is the property that
// keeps a log readable. Logging 404s at error is how a log becomes noise, and
// it teaches whoever reads it to skip the entries that matter.
func TestLogLevelsSeparateServerFaultsFromClientMistakes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status int
		want   string
	}{
		{http.StatusOK, "INFO"},
		{http.StatusFound, "INFO"},
		{http.StatusBadRequest, "WARN"},
		{http.StatusNotFound, "WARN"},
		{http.StatusConflict, "WARN"},
		{http.StatusInternalServerError, "ERROR"},
		{http.StatusBadGateway, "ERROR"},
	}

	for _, testCase := range cases {
		t.Run(testCase.want+"/"+http.StatusText(testCase.status), func(t *testing.T) {
			t.Parallel()

			logger, capture := newTestLogger()

			middleware.Log(logger)(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(testCase.status)
				},
			)).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

			levels := capture.levels()
			if len(levels) != 1 {
				t.Fatalf("logged %d records, want 1", len(levels))
			}

			if got := levels[0].String(); got != testCase.want {
				t.Errorf("level = %s for status %d, want %s", got, testCase.status, testCase.want)
			}
		})
	}
}

// TestLogRecordsOKForAHandlerThatNeverSetsAStatus: a body-only write still means
// 200, and a log line reporting 0 would be a lie a reader cannot detect.
func TestLogRecordsOKForAHandlerThatNeverSetsAStatus(t *testing.T) {
	t.Parallel()

	logger, capture := newTestLogger()

	middleware.Log(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("implicit"))
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

	if got, _ := capture.attr("status"); got != "200" {
		t.Errorf("status = %q, want %q", got, "200")
	}
}

// TestLogCountsBytes covers the size field, which is what distinguishes an
// empty response from a truncated one.
func TestLogCountsBytes(t *testing.T) {
	t.Parallel()

	logger, capture := newTestLogger()

	middleware.Log(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("0123456789"))
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

	if got, _ := capture.attr("bytes"); got != "10" {
		t.Errorf("bytes = %q, want %q", got, "10")
	}
}

// TestStatusRecorderCapturesTheFirstStatusOnly covers the rule that a second
// WriteHeader is ignored, which is net/http's own contract and the reason the
// recorder tracks whether it has already written.
func TestStatusRecorderCapturesTheFirstStatusOnly(t *testing.T) {
	t.Parallel()

	logger, capture := newTestLogger()

	middleware.Log(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.WriteHeader(http.StatusTeapot)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

	if got, _ := capture.attr("status"); got != "200" {
		t.Errorf("status = %q, want the first status 200", got)
	}
}

// TestLogIsSafeUnderConcurrentRequests runs the gate's -race mode against the
// path a real server takes: many requests, each with its own recorder.
func TestLogIsSafeUnderConcurrentRequests(t *testing.T) {
	t.Parallel()

	const requests = 64

	logger, capture := newTestLogger()
	handler := middleware.Chain(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		middleware.RequestID,
		middleware.Log(logger),
	)

	var wg sync.WaitGroup

	for range requests {
		wg.Go(func() {
			handler.ServeHTTP(
				httptest.NewRecorder(),
				httptest.NewRequestWithContext(
					t.Context(),
					http.MethodGet,
					"/healthz",
					http.NoBody,
				),
			)
		})
	}

	wg.Wait()

	if got := capture.count(); got != requests {
		t.Errorf("logged %d records for %d requests, want one each", got, requests)
	}
}

// hijackableRecorder is a ResponseRecorder that supports Hijacker, so the
// recorder's pass-through can be exercised.
type hijackableRecorder struct {
	*httptest.ResponseRecorder

	conn net.Conn
}

func (h hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h.conn == nil {
		return nil, nil, errors.New("test: no connection to hand over")
	}

	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}

// TestLogPreservesOptionalWriterInterfaces is the property the WebSocket route
// depends on. A logging wrapper that does not implement Hijacker silently
// disables the upgrade, and the symptom appears far from the cause: a
// connection that fails for reasons that have nothing to do with the network.
func TestLogPreservesOptionalWriterInterfaces(t *testing.T) {
	t.Parallel()

	logger, _ := newTestLogger()

	server, client := net.Pipe()
	defer func() {
		_ = server.Close()
		_ = client.Close()
	}()

	reached := make(chan error, 1)

	handler := middleware.Log(
		logger,
	)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				reached <- errors.New("the logged writer is not an http.Hijacker")

				return
			}

			conn, _, err := hijacker.Hijack()
			if err != nil {
				reached <- err

				return
			}

			reached <- conn.Close()
		}),
	)

	recorder := hijackableRecorder{ResponseRecorder: httptest.NewRecorder(), conn: server}
	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/c/greyhaven/ws", http.NoBody),
	)

	if err := <-reached; err != nil {
		t.Errorf("hijack through the logging wrapper: %v", err)
	}
}

// TestLogPreservesFlusher covers the same property for server-sent events.
func TestLogPreservesFlusher(t *testing.T) {
	t.Parallel()

	logger, _ := newTestLogger()

	flushed := make(chan bool, 1)

	middleware.Log(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			flushed <- false

			return
		}

		_, _ = w.Write([]byte("data: one\n\n"))
		flusher.Flush()
		flushed <- true
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/c/greyhaven/events", http.NoBody))

	if !<-flushed {
		t.Error("the logged writer is not an http.Flusher; an event stream cannot work through it")
	}
}
