// Package webui serves the embedded SPA built from web/.
//
// Build flow (docs/repo-layout.md): `make web` builds web/dist, then
// `make embed` copies it into internal/webui/dist. Bare `go build` without
// that step serves the placeholder below — developer-only, never shipped.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist
var embedded embed.FS

// Handler serves the SPA with fallback to index.html for client-side routes.
// Until `make embed` has copied a real build (no index.html in dist), it
// serves a loud 503 placeholder instead of silently 404ing.
func Handler() http.HandlerFunc {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return servePlaceholder
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return servePlaceholder
	}
	fileServer := http.FileServer(http.FS(sub))
	return func(w http.ResponseWriter, r *http.Request) {
		// SPA fallback: unknown paths resolve to index.html.
		if r.URL.Path != "/" {
			if _, err := fs.Stat(sub, trimLeadingSlash(r.URL.Path)); err != nil {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		// Never cache the shell during development; real caching headers
		// arrive with the asset pipeline.
		w.Header().Set("Cache-Control", "no-store")
		fileServer.ServeHTTP(w, r)
	}
}

func servePlaceholder(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(placeholderHTML))
}

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}

const placeholderHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Semiplane</title></head>
<body style="font-family:system-ui;max-width:40rem;margin:4rem auto;padding:0 1rem">
<h1>Semiplane (thin slice)</h1>
<p>The web UI has not been built into this binary.</p>
<p>Run <code>make web &amp;&amp; make embed</code>, then rebuild.</p>
<p data-testid="placeholder">placeholder</p>
</body></html>
`
