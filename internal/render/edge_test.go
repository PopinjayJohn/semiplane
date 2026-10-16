package render

// Edge cases for the Render surface: role isolation across calls,
// frontmatter type fidelity, hostile input tolerance, and the
// fragment (not document) output contract.

import (
	"strings"
	"testing"
)

func mustRender(t *testing.T, doc string, role Role) (string, map[string]any) {
	t.Helper()
	htmlOut, meta, err := Render([]byte(doc), role)
	if err != nil {
		t.Fatalf("Render(%q): %v", role, err)
	}
	return htmlOut, meta
}

func TestEdgeRenderRoleIsolation(t *testing.T) {
	// Two Pipelines (one per Render call) must not cross-leak: rendering
	// as GM first must not make a later player render of the same bytes
	// see the secret, and the GM render must still work afterwards.
	const doc = "# Title\n\npublic\n\n:::gm Secret\ntrapdoor under the altar\n:::\n"
	gmFirst, _ := mustRender(t, doc, RoleGM)
	if !strings.Contains(gmFirst, "trapdoor under the altar") {
		t.Errorf("gm render missing secret:\n%s", gmFirst)
	}
	player, _ := mustRender(t, doc, RolePlayer)
	for _, leak := range []string{"trapdoor under the altar", "gm-block"} {
		if strings.Contains(player, leak) {
			t.Errorf("player render after gm render leaks %q:\n%s", leak, player)
		}
	}
	if !strings.Contains(player, "public") {
		t.Errorf("player render missing public prose:\n%s", player)
	}
	gmAgain, _ := mustRender(t, doc, RoleGM)
	if !strings.Contains(gmAgain, "trapdoor under the altar") {
		t.Errorf("second gm render missing secret:\n%s", gmAgain)
	}
}

func TestEdgeRenderFrontmatterTypes(t *testing.T) {
	// Scalar and nested YAML types survive into meta verbatim; none of
	// the frontmatter text may appear in the HTML fragment.
	const doc = "---\ntitle: T\ncount: 5\nenabled: true\nratio: 1.5\nnested:\n  x: 1\n  tags: [a, b]\n---\n# Body\n"
	htmlOut, meta := mustRender(t, doc, RoleGM)
	if meta["title"] != "T" {
		t.Errorf("title = %#v", meta["title"])
	}
	if meta["count"] != 5 {
		t.Errorf("count = %#v (type %T), want int 5", meta["count"], meta["count"])
	}
	if meta["enabled"] != true {
		t.Errorf("enabled = %#v", meta["enabled"])
	}
	if meta["ratio"] != 1.5 {
		t.Errorf("ratio = %#v", meta["ratio"])
	}
	nested, ok := meta["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested = %#v, want map[string]any", meta["nested"])
	}
	if nested["x"] != 1 {
		t.Errorf("nested.x = %#v", nested["x"])
	}
	tags, ok := nested["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Errorf("nested.tags = %#v", nested["tags"])
	}
	for _, leak := range []string{"count: 5", "enabled: true", "nested:"} {
		if strings.Contains(htmlOut, leak) {
			t.Errorf("frontmatter leaked into HTML: %q:\n%s", leak, htmlOut)
		}
	}
	if !strings.Contains(htmlOut, "<h1>Body") {
		t.Errorf("body missing:\n%s", htmlOut)
	}
}

func TestEdgeRenderNonUTF8Tolerant(t *testing.T) {
	// Invalid UTF-8 input must not error or crash; behavior documented:
	// the raw bytes pass through into a paragraph (no sanitization or
	// replacement is applied at this layer).
	htmlOut, meta, err := Render([]byte{0xff, 0xfe, '#', ' ', 'h', 'i', '\n'}, RolePlayer)
	if err != nil {
		t.Fatalf("Render(non-UTF8): %v", err)
	}
	if len(meta) != 0 {
		t.Errorf("want empty meta, got %#v", meta)
	}
	if !strings.Contains(htmlOut, "hi") {
		t.Errorf("body text lost:\n%s", htmlOut)
	}
	if strings.Contains(htmlOut, "<html") {
		t.Errorf("fragment must not gain a wrapper:\n%s", htmlOut)
	}
}

func TestEdgeRenderNoHTMLWrapper(t *testing.T) {
	// Render returns a fragment for the SPA shell: no document wrapper
	// even for a rich note, while real blocks still render.
	const doc = "---\ntitle: T\n---\n# H [[x]]\n\n| A |\n|---|\n| 1 |\n\n:::gm Secret\ns\n:::\n"
	for _, role := range []Role{RoleGM, RolePlayer} {
		htmlOut, _ := mustRender(t, doc, role)
		for _, wrapper := range []string{"<html", "<head", "<body", "<!DOCTYPE"} {
			if strings.Contains(htmlOut, wrapper) {
				t.Errorf("role %q: output contains document wrapper %q:\n%s", role, wrapper, htmlOut)
			}
		}
	}
	gm, _ := mustRender(t, doc, RoleGM)
	for _, want := range []string{"<h1>H", "<table>", `data-testid="wikilink"`, `data-testid="gm-block"`} {
		if !strings.Contains(gm, want) {
			t.Errorf("gm output missing %q:\n%s", want, gm)
		}
	}
}

func TestEdgeRenderCRLFEndToEnd(t *testing.T) {
	// CRLF frontmatter + CRLF body through the full surface.
	htmlOut, meta, err := Render([]byte("---\r\ntitle: T\r\n---\r\n# Hi\r\n"), RoleGM)
	if err != nil {
		t.Fatalf("Render(CRLF): %v", err)
	}
	if meta["title"] != "T" {
		t.Errorf("meta = %#v", meta)
	}
	if !strings.Contains(htmlOut, "<h1>Hi") {
		t.Errorf("body missing:\n%s", htmlOut)
	}
	if strings.Contains(htmlOut, "title: T") {
		t.Errorf("frontmatter leaked into HTML:\n%s", htmlOut)
	}
}
