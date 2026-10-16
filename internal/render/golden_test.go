package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenDir is the committed fixture directory. Update flow: golden files
// are the reviewed contract — regenerate only deliberately with
// UPDATE_GOLDEN=1, review the diff, then commit. CI never sets the flag,
// so drift fails loudly instead of silently rebasing.
func goldenDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "golden")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("golden dir: %v", err)
	}
	return dir
}

// TestGolden renders the shared Obsidian fixture for both roles and
// diffs against the committed HTML. The GM/Player divergence (secret
// present vs server-side stripped) is the stream-A acceptance proof.
func TestGolden(t *testing.T) {
	dir := goldenDir(t)
	src, err := os.ReadFile(filepath.Join(dir, "obsidian.md"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	for _, tc := range []struct {
		role Role
		file string
	}{
		{RoleGM, "obsidian.gm.html"},
		{RolePlayer, "obsidian.player.html"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			htmlOut, meta, err := Render(src, tc.role)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if meta["title"] != "The Yonder Vault" {
				t.Errorf("meta[title] = %#v, want fixture title", meta["title"])
			}
			path := filepath.Join(dir, tc.file)
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.WriteFile(path, []byte(htmlOut), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				t.Logf("regenerated %s", tc.file)
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s (regenerate with UPDATE_GOLDEN=1): %v", tc.file, err)
			}
			if htmlOut != string(want) {
				t.Errorf("golden mismatch for %s:\n--- got ---\n%s\n--- want ---\n%s",
					tc.file, htmlOut, want)
			}
		})
	}
}

// TestGoldenDivergence is the acceptance invariant in words: the secret
// is present for the GM and absent (not hidden — absent) for the player,
// while public content survives for both.
func TestGoldenDivergence(t *testing.T) {
	dir := goldenDir(t)
	src, err := os.ReadFile(filepath.Join(dir, "obsidian.md"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	gm, _, err := Render(src, RoleGM)
	if err != nil {
		t.Fatalf("Render gm: %v", err)
	}
	player, _, err := Render(src, RolePlayer)
	if err != nil {
		t.Fatalf("Render player: %v", err)
	}
	const secret = "The vault door was built from the inside."
	if !strings.Contains(gm, secret) {
		t.Error("GM render missing secret")
	}
	if strings.Contains(player, secret) {
		t.Error("player render leaks secret")
	}
	if strings.Contains(player, "gm-block") {
		t.Error("player render contains gm-block markup")
	}
	for _, pub := range []string{"The Yonder Vault", "Rusty Flagon", "Cellar Rat", "Delver"} {
		if !strings.Contains(gm, pub) || !strings.Contains(player, pub) {
			t.Errorf("public content %q missing from a role render", pub)
		}
	}
}
