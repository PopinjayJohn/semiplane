// Package middleware provides the HTTP middleware chain and the per-request
// values it attaches to a request context.
//
// The chain is composed by wrapping: Chain(outer, inner)(handler) runs outer's
// preamble, then inner's, then the handler. Composition order is therefore
// written once, in one place, and every middleware here is independent of the
// others — none of them assumes what has already run.
package middleware

import (
	"net/http"
	"slices"
)

// Chain applies middlewares so that the first argument is the outermost layer:
// the first one's preamble runs first and its deferred work runs last.
//
// An empty chain returns the handler unchanged, so a caller wiring nothing in a
// test does not need a special case.
func Chain(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	// Applied back to front so the first listed ends up outermost. Written
	// this way rather than by folding from the left because the fold has to
	// reverse the slice, and a reversed copy plus a reverse-iteration fold is
	// the version someone eventually gets wrong.
	slices.Reverse(middlewares)

	for _, wrap := range middlewares {
		if wrap == nil {
			// A nil middleware is a wiring bug that would otherwise panic at
			// the first request, in whichever layer happens to run first,
			// rather than at startup. Skipping it silently is the one
			// defensible option: refusing to start would be worse, and there
			// is nowhere to report it from here.
			continue
		}

		handler = wrap(handler)
	}

	return handler
}
