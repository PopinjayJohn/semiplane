package render

import (
	"strings"
	"testing"
)

func TestRenderContract(t *testing.T) {
	t.Run("frontmatter split from html", func(t *testing.T) {
		htmlOut, meta, err := Render([]byte("---\ntitle: T\ntags: [a]\n---\n# Body [[x]]\n"), RoleGM)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if meta["title"] != "T" {
			t.Errorf("meta = %#v", meta)
		}
		if strings.Contains(htmlOut, "title: T") {
			t.Errorf("frontmatter leaked into HTML:\n%s", htmlOut)
		}
		if !strings.Contains(htmlOut, "<h1>Body") {
			t.Errorf("body missing:\n%s", htmlOut)
		}
	})
	t.Run("unknown role errors", func(t *testing.T) {
		if _, _, err := Render([]byte("# hi\n"), "dm"); err == nil {
			t.Error("want error for unknown role")
		}
	})
	t.Run("invalid frontmatter errors", func(t *testing.T) {
		if _, _, err := Render([]byte("---\ntitle: [x\n---\nbody\n"), RoleGM); err == nil {
			t.Error("want error for invalid YAML")
		}
	})
	t.Run("empty note", func(t *testing.T) {
		htmlOut, meta, err := Render(nil, RolePlayer)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if htmlOut != "" {
			t.Errorf("want empty HTML, got %q", htmlOut)
		}
		if len(meta) != 0 {
			t.Errorf("want empty meta, got %#v", meta)
		}
	})
	t.Run("roles are shared vocabulary", func(t *testing.T) {
		if RoleGM != "gm" || RolePlayer != "player" {
			t.Errorf("role constants drifted: %q %q", RoleGM, RolePlayer)
		}
	})
}
