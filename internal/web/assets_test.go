package web_test

import (
	"io"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/web"
)

// stylesheet reads the embedded stylesheet, failing the test if it is absent.
func stylesheet(t *testing.T) []byte {
	t.Helper()

	data, err := io.ReadAll(mustOpen(t, "app.css"))
	if err != nil {
		t.Fatalf("read embedded stylesheet: %v", err)
	}

	return data
}

func mustOpen(t *testing.T, name string) io.ReadCloser {
	t.Helper()

	f, err := web.Dist().Open(name)
	if err != nil {
		t.Fatalf("open embedded %s: %v", name, err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Errorf("close embedded %s: %v", name, err)
		}
	})

	return f
}

// TestDistEmbedsBuiltStylesheet is the assertion that the gate is wired: it
// fails if `make css` did not run before `go build`, which is the whole reason
// the Makefile checks for the file. An embed of an empty directory compiles
// fine and serves a 404 for every asset, so the file's existence is the only
// thing standing between a green build and an unstyled product.
func TestDistEmbedsBuiltStylesheet(t *testing.T) {
	t.Parallel()

	if data := stylesheet(t); len(data) == 0 {
		t.Fatal("embedded stylesheet is empty; `make css` produced nothing")
	}
}

// TestEmbeddedStylesheetIsPlainCSS guards the boundary the server depends on:
// what it serves is CSS, not something that re-introduces a script context. A
// user-supplied import in a shared vault is exactly how that happens.
func TestEmbeddedStylesheetIsPlainCSS(t *testing.T) {
	t.Parallel()

	body := strings.ToLower(string(stylesheet(t)))

	for _, forbidden := range []string{"<script", "javascript:", "@import url(", "expression("} {
		if strings.Contains(body, forbidden) {
			t.Errorf("embedded stylesheet contains %q", forbidden)
		}
	}
}
