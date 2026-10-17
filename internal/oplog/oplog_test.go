package oplog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PopinjayJohn/semiplane/internal/events"
)

func TestAppendAssignsIDAndDefaults(t *testing.T) {
	l := New(filepath.Join(t.TempDir(), "oplog"))
	e, err := l.Append(Entry{
		Kind: "dice_roll", Formula: "2d6", Seed: 9,
		Faces: []int{3, 4}, Contributions: []string{"2d6 = 7"},
		Conditions: []string{}, Total: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != 1 {
		t.Fatalf("first id = %d, want 1", e.ID)
	}
	if e.Ontology != events.OntologyVersion {
		t.Fatalf("ontology = %q, want %q", e.Ontology, events.OntologyVersion)
	}
	if e.Pack != DefaultPack {
		t.Fatalf("pack = %q, want %q", e.Pack, DefaultPack)
	}
	if e.TS == "" {
		t.Fatal("TS must be stamped")
	}
	e2, err := l.Append(Entry{Kind: "dice_roll", Seed: 1, Faces: []int{1}, Total: 1})
	if err != nil {
		t.Fatal(err)
	}
	if e2.ID != 2 {
		t.Fatalf("second id = %d, want 2", e2.ID)
	}
}

func TestAppendPersistsJSONLines(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oplog")
	l := New(dir)
	if _, err := l.Append(Entry{Kind: "dice_roll", Seed: 5, Faces: []int{2, 3}, Total: 5}); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("glob = %v, err = %v; want one day file", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatal("journal lines must be newline-terminated")
	}
	var decoded Entry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Seed != 5 || decoded.Total != 5 || len(decoded.Faces) != 2 {
		t.Fatalf("round-trip = %+v", decoded)
	}
}
