package middleware

import (
	"fmt"
	"net/http"
	"runtime/debug"
)

// Recovery converts panics into 500 problem+json instead of killing the
// server or hanging the connection. Panic value and stack go to the log;
// the client sees only the generic title (never internal details).
func Recovery(next http.Handler, onPanic func(v any, stack []byte)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				stack := debug.Stack()
				if onPanic != nil {
					onPanic(v, stack)
				}
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = fmt.Fprintf(w, `{"type":"about:blank","title":"internal server error","status":500,"detail":"request %s failed"}`,
					RequestID(r.Context()))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Chain wraps h in mws, applying the first middleware outermost.
func Chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
