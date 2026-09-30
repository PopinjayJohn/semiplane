package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/httpapi/middleware"
)

func TestChainAppliesOutermostFirst(t *testing.T) {
	t.Parallel()

	var order []string

	record := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, "before:"+name)
				next.ServeHTTP(w, r)
				order = append(order, "after:"+name)
			})
		}
	}

	handler := middleware.Chain(
		http.HandlerFunc(
			func(http.ResponseWriter, *http.Request) { order = append(order, "handler") },
		),
		record("first"),
		record("second"),
	)

	handler.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody),
	)

	// The first argument is outermost, so it enters first and leaves last. This
	// ordering is what lets Recoverer catch a panic in every layer below it, and
	// what puts Log outside Timeout so it observes the 504.
	want := []string{
		"before:first", "before:second", "handler", "after:second", "after:first",
	}

	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestChainWithNoMiddlewaresReturnsHandler(t *testing.T) {
	t.Parallel()

	called := false
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })

	middleware.Chain(handler).ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody),
	)

	if !called {
		t.Error("the handler was not called through an empty chain")
	}
}

// TestChainSkipsNilMiddleware covers a wiring bug that would otherwise panic at
// the first request, in whichever layer happens to run first, rather than at
// startup where it is visible.
func TestChainSkipsNilMiddleware(t *testing.T) {
	t.Parallel()

	called := false
	order := 0

	handler := middleware.Chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }),
		nil,
		func(next http.Handler) http.Handler {
			order++

			return next
		},
	)

	handler.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody),
	)

	if !called {
		t.Error("the handler was not reached; a nil middleware must be skipped, not applied")
	}

	if order != 1 {
		t.Errorf("the real middleware ran %d times, want 1", order)
	}
}
