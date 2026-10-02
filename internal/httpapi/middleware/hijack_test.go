// The upgrade-through-the-buffer regression, and its fix.
//
// # Why this file exists and why it dials
//
// `Timeout` buffers a response so it can still substitute a 504 for a handler that
// overran its budget, and `websocket.Accept` writes `101 Switching Protocols`
// through that buffer and then hijacks. Those two facts together silently broke
// every WebSocket route in the product: the client saw a bare `EOF` with no status,
// which a browser reports as an intermittent network error and an operator reads as
// a reverse proxy or a firewall.
//
// Every earlier test passed, and the reason is worth recording because it is the
// same reason the `play` package's own harness says a hijack "would still work
// through" this layer: **every earlier test used a `httptest.ResponseRecorder`**,
// which is not a connection, so a hijack through it fails for a reason that has
// nothing to do with buffering, and the test asserted the hijack's *return value*
// rather than what the client received. Asserting that a hijack returned a
// connection is not asserting that the client got a 101 — the connection can be
// perfectly good and the status line can be sitting in a buffer nobody will commit.
//
// So the assertion here is the client's: a real `http.Transport` over a real
// listener, asking for an upgrade, and the status it observes. That is the only
// observation that can see this bug, and it is the one a browser makes.

package middleware_test

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/httpapi/middleware"
)

// hijackBudget is the handler budget these tests run under.
//
// Long, so that a test failing because the budget fired is unambiguously a failure
// and not a slow machine. The point of the tests is that the budget is irrelevant
// to an upgrading handler, and a short one would make a passing test ambiguous.
const hijackBudget = 30 * time.Second

// upgradeHandler writes `101` and takes the connection, which is exactly the
// sequence `websocket.Accept` performs.
//
// A real hijack through a real connection rather than a recorder, for the reason
// this file's header gives: the bug is invisible to a recorder and the whole test
// suite being green while the product was broken is what this file exists to
// prevent.
func upgradeHandler(t *testing.T, dialed chan<- struct{}) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The order `websocket.Accept` uses, and the order the bug depends on:
		// status line first, hijack second.
		w.Header().Set("Upgrade", "websocket")
		w.Header().Set("Connection", "Upgrade")
		w.WriteHeader(http.StatusSwitchingProtocols)

		conn, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack through the timeout layer: %v", err)

			return
		}

		// The reader is drained and the connection closed without a reply, which is
		// what a real client sees when the server stops talking. The client below
		// must already have its status by this point, and that ordering is the
		// assertion: the bytes were on the wire before the connection went away.
		_ = buffered
		_ = conn.Close()

		close(dialed)
	})
}

// TestTheUpgradeStatusReachesTheClientThroughTheTimeoutLayer is the regression test
// for the defect, and it fails against the unfixed writer.
//
// One status, observed by the client. Before the fix it is `EOF`; after it, 101.
//
// It goes through the *whole* product chain rather than `Timeout` alone, because
// the bug is a property of the composition: `Log`'s `statusRecorder` also wraps the
// writer and also implements `Hijack`, so a fix that satisfied `Timeout` in
// isolation could still fail behind it. `internal/httpapi/router.go` is the authority
// on the real order and this is the same order.
func TestTheUpgradeStatusReachesTheClientThroughTheTimeoutLayer(t *testing.T) {
	t.Parallel()

	dialed := make(chan struct{})

	handler := middleware.Chain(
		upgradeHandler(t, dialed),
		middleware.Log(discardLogger()),
		middleware.Timeout(hijackBudget),
	)

	status := observeUpgrade(t, handler)

	if status != http.StatusSwitchingProtocols {
		t.Fatalf("the client saw %d, want 101. `Timeout` buffers the status line and "+
			"commits it in a defer that runs after the hijack, so the bytes are "+
			"discarded and the client sees nothing", status)
	}

	// The handler ran to completion rather than being abandoned, so the client got
	// its status from a finished upgrade and not from a lucky race.
	select {
	case <-dialed:
	case <-time.After(time.Second):
		t.Error("the upgrading handler never finished")
	}
}

// TestTheTimeoutStillBoundsAHandlerThatDoesNotHijack is the other direction, and it
// is here because the fix touches the writer every request goes through.
//
// A fix for the upgrade bug that made the writer transparent too early would break
// the layer's entire purpose: the 504 would never be substituted because the status
// line had already gone out. So the budget is asserted **still working**, on the
// same writer, after the same change.
func TestTheTimeoutStillBoundsAHandlerThatDoesNotHijack(t *testing.T) {
	t.Parallel()

	// The handler waits on **its request context**, not on a channel this test owns.
	// That is how a real handler learns its budget is gone, and it is why the
	// deferred commit can run at all: `Timeout` is synchronous, so the 504 is only
	// written when the handler returns, and a handler that waits on a channel the
	// test releases after the assertion would deadlock rather than fail.
	//
	// `TestTimeoutSubstitutes504AfterAPartialWrite` covers the substitution over a
	// partial body. This one covers the narrower thing the hijack fix could break:
	// that a request which **never wrote at all** still gets a 504 rather than a
	// silent 200 with an empty body.
	handler := middleware.Timeout(20*time.Millisecond)(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody),
	)

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("a handler that overran its budget without writing = %d, want 504", recorder.Code)
	}
}

// TestAHijackThatIsRefusedLeavesTheResponseIntact is the error path, and the reason
// it is a separate test.
//
// When the parent cannot be hijacked — HTTP/2, which has no connection to take
// over — `Hijack` must **not** have committed. A writer that flushed the buffer and
// *then* failed the hijack has sent a 101 to a client that will never get a socket,
// which is worse than the bug this file is about: the client believes it is
// connected and is not.
//
// The assertion is that the handler's own response still arrives, which is only
// possible if the deferred commit remained intact.
func TestAHijackThatIsRefusedLeavesTheResponseIntact(t *testing.T) {
	t.Parallel()

	// A recorder is not a connection and not a Hijacker, so the refusal is real
	// rather than simulated — it is the same situation as an HTTP/2 request.
	handler := middleware.Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, _, err := http.NewResponseController(w).Hijack(); err == nil {
			t.Error("a recorder hijacked successfully")
		}

		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("still here"))
	}), middleware.Timeout(hijackBudget))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	if recorder.Code != http.StatusTeapot {
		t.Fatalf("the status = %d, want 418: a refused hijack must leave the "+
			"handler's response intact for the deferred commit to deliver", recorder.Code)
	}

	if body := recorder.Body.String(); body != "still here" {
		t.Errorf("the body = %q, want %q", body, "still here")
	}
}

// observeUpgrade asks for an upgrade and reports the status the client saw.
//
// Zero when there was no response at all, which is the whole point: "no status" and
// "a status" have to be distinguishable, and a helper that returned the transport
// error as a status would turn the regression into a 0-versus-101 comparison that
// reads like a test about numbers.
func observeUpgrade(t *testing.T, handler http.Handler) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	// A real `http.Server` rather than `httptest.NewServer`, because the writer
	// `Timeout` wraps is the one `net/http` builds and the hijack only works
	// against it. `httptest.NewServer` would also be fine, and using `net.Listen`
	// directly is only so the failure message names the thing that was wrong.
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}

	serving := make(chan struct{})

	go func() {
		defer close(serving)

		if serveErr := server.Serve(listener); serveErr != nil &&
			!errors.Is(serveErr, http.ErrServerClosed) {
			t.Errorf("serve: %v", serveErr)
		}
	}()

	t.Cleanup(func() {
		_ = server.Close()
		<-serving
	})

	request, err := http.NewRequest(
		http.MethodGet, "http://"+listener.Addr().String()+"/", http.NoBody,
	)
	if err != nil {
		t.Fatalf("build the upgrade request: %v", err)
	}

	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")

	response, err := http.DefaultTransport.RoundTrip(request)
	if err != nil {
		// No response at all. Zero rather than the error, because the error is what
		// a browser reports as an intermittent network fault and the number is what
		// makes the difference legible in a failure message.
		return 0
	}

	t.Cleanup(func() { _ = response.Body.Close() })

	return response.StatusCode
}


