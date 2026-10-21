package index

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseSemiplane(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"absent", "---\ntags: [x]\n---\nbody\n", false},
		{"null", "---\nsemiplane:\n---\nbody\n", false},
		{"invalid yaml", "---\nsemiplane: [unclosed\n---\nbody\n", false},
		{"mapping", "---\nsemiplane:\n  class:\n    name: Barbarian\n---\nbody\n", true},
		{"empty mapping", "---\nsemiplane: {}\n---\nbody\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, ok := ParseSemiplane(tc.in)
			if ok != tc.want {
				t.Fatalf("ParseSemiplane ok = %v, want %v", ok, tc.want)
			}
			if !ok {
				return
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("ParseSemiplane returned invalid JSON %q: %v", raw, err)
			}
		})
	}
}

func TestFacetPaths(t *testing.T) {
	ix := openTestDB(t)

	classNote := "---\nsemiplane:\n  class:\n    name: Barbarian\n    source: XPHB\n---\n# Barbarian\n"
	subclassNote := "---\nsemiplane:\n  subclass:\n    name: Path of the Berserker\n---\n# Berserker\n"
	plain := "---\ntags: [class]\n---\n# Plain\n"
	for i, tc := range []struct {
		path    string
		content string
	}{
		{"classes/barbarian.md", classNote},
		{"classes/berserker.md", subclassNote},
		{"classes/plain.md", plain},
	} {
		if err := ix.Upsert(tc.path, tc.content, "hash", int64(i)); err != nil {
			t.Fatalf("Upsert %s: %v", tc.path, err)
		}
	}

	cases := []struct {
		name string
		key  string
		want []string
	}{
		{"namespace", "", []string{"classes/barbarian.md", "classes/berserker.md"}},
		{"class", "class", []string{"classes/barbarian.md"}},
		{"subclass", "subclass", []string{"classes/berserker.md"}},
		{"nested", "class.name", []string{"classes/barbarian.md"}},
		{"missing key", "class.missing", []string{}},
		{"missing namespace", "nope", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			namespace := "semiplane"
			if tc.name == "missing namespace" {
				namespace = "other"
			}
			got, err := ix.FacetPaths(namespace, tc.key)
			if err != nil {
				t.Fatalf("FacetPaths: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("FacetPaths(%q, %q) = %v, want %v", namespace, tc.key, got, tc.want)
			}
		})
	}

	// Updating a note without the namespace must drop its facet row.
	if err := ix.Upsert("classes/barbarian.md", plain, "hash2", 2); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	got, err := ix.FacetPaths("semiplane", "class")
	if err != nil {
		t.Fatalf("FacetPaths after update: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("class facet after update = %v, want []", got)
	}

	// Deleting a note must drop its facet row.
	if err := ix.Delete("classes/berserker.md"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err = ix.FacetPaths("semiplane", "")
	if err != nil {
		t.Fatalf("FacetPaths after delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("semiplane facets after delete = %v, want []", got)
	}
}

func TestReindexFacetsFromVault(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "vaults", "facets")
	ix := openTestDB(t)
	if _, err := ix.Reindex(root); err != nil {
		t.Fatalf("Reindex: %v", err)
	}

	got, err := ix.FacetPaths("semiplane", "")
	if err != nil {
		t.Fatalf("FacetPaths: %v", err)
	}
	want := []string{
		"rules/classes/barbarian-xphb-path-of-the-berserker-xphb.md",
		"rules/classes/barbarian-xphb.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("semiplane facets = %v, want %v", got, want)
	}

	got, err = ix.FacetPaths("semiplane", "class")
	if err != nil {
		t.Fatalf("FacetPaths class: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"rules/classes/barbarian-xphb.md"}) {
		t.Fatalf("class facet = %v", got)
	}

	// Copy the vault to a scratch dir so removal can prove reindex cleanup.
	scratch := t.TempDir()
	for _, rel := range want {
		src := filepath.Join(root, filepath.FromSlash(rel))
		dst := filepath.Join(scratch, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix2 := openTestDB(t)
	if _, err := ix2.Reindex(scratch); err != nil {
		t.Fatalf("Reindex scratch: %v", err)
	}
	if err := os.Remove(filepath.Join(scratch, "rules", "classes", "barbarian-xphb.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := ix2.Reindex(scratch); err != nil {
		t.Fatalf("Reindex after remove: %v", err)
	}
	got, err = ix2.FacetPaths("semiplane", "class")
	if err != nil {
		t.Fatalf("FacetPaths after remove: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("stale class facet after remove = %v, want []", got)
	}
}
