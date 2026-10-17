// Package oplog is the append-only operation journal under
// .semiplane/oplog/ (tech-stack §9: seed for future multi-device sync and
// the replayable combat log).
//
// Every server-authoritative dice resolution appends one JSON line carrying
// the v0 replay fields decided in the Phase-1 plan: seed, faces,
// contributions, conditions, plus the pack id and the core ontology version
// so a trace replays across pack updates. Writes are O_APPEND with a
// per-process mutex; readers tail the file for sync/replay.
package oplog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/PopinjayJohn/semiplane/internal/events"
)

// Entry is one journal line. Kind names the resolution ("dice_roll" in v0;
// hook-mediated combat entries arrive with the rules engine's verdicts).
type Entry struct {
	// ID is the 1-based line number within the day file.
	ID int64 `json:"id"`
	// TS is the append time in RFC 3339 UTC.
	TS string `json:"ts"`
	// Kind is the entry kind ("dice_roll").
	Kind string `json:"kind"`
	// Formula is the evaluated dice formula (e.g. "2d20kh1+5").
	Formula string `json:"formula,omitempty"`
	// Seed is the deterministic roller seed (SEMIPLANE_TEST_SEED in tests).
	Seed int64 `json:"seed"`
	// Faces records every die face rolled, in roll order.
	Faces []int `json:"faces"`
	// Contributions renders one line per evaluated leaf (per-tier dice/flat
	// lines, so not-doubled-on-crit stays auditable per tech-stack §3.8).
	Contributions []string `json:"contributions"`
	// Conditions lists condition keys applied by the resolution (empty for
	// plain rolls; hook-mediated rolls name what they applied).
	Conditions []string `json:"conditions,omitempty"`
	// Pack is the active system-pack id ("none" when no pack is loaded).
	Pack string `json:"pack"`
	// Ontology is the core ontology version (events.OntologyVersion).
	Ontology string `json:"ontology"`
	// Total is the final numeric outcome.
	Total int `json:"total"`
}

// Log is an append-only JSONL journal rooted at dir.
type Log struct {
	mu  sync.Mutex
	dir string
}

// New returns a Log journaling into dir (created on first append).
func New(dir string) *Log {
	return &Log{dir: dir}
}

// DefaultPack is recorded when no system pack is active.
const DefaultPack = "none"

// Append writes one entry to the day file (YYYY-MM-DD.jsonl), assigning ID
// and TS. It fsyncs through file close so a crash never truncates user data
// (tech-stack §5 atomicity applies to the journal too).
func (l *Log) Append(e Entry) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return Entry{}, err
	}
	name := time.Now().UTC().Format("2006-01-02") + ".jsonl"
	path := filepath.Join(l.dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = f.Close() }()
	e.ID = nextID(path) + 1
	e.TS = time.Now().UTC().Format(time.RFC3339)
	if e.Ontology == "" {
		e.Ontology = events.OntologyVersion
	}
	if e.Pack == "" {
		e.Pack = DefaultPack
	}
	line, err := json.Marshal(e)
	if err != nil {
		return Entry{}, err
	}
	if _, err := fmt.Fprintln(f, string(line)); err != nil {
		return Entry{}, err
	}
	if err := f.Sync(); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// nextID counts existing lines in path (0 when the file is new).
func nextID(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var n int64
	for _, b := range raw {
		if b == '\n' {
			n++
		}
	}
	return n
}
