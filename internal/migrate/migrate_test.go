package migrate

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestApplyIdempotent(t *testing.T) {
	db := openTestDB(t)

	for i := 0; i < 2; i++ {
		if err := Apply(db); err != nil {
			t.Fatalf("Apply #%d: %v", i+1, err)
		}
	}

	wantTables := []string{
		"notes", "notes_fts", "note_facets", "backlinks", "sessions", "tokens",
		"lan_codes", "oidc_states", "acls", "users", "user_version",
	}
	for _, table := range wantTables {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE name = ? AND (type = 'table' OR type = 'virtual table')`,
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", table, err)
			continue
		}
		if name != table {
			t.Errorf("table %q: got %q", table, name)
		}
	}

	wantTriggers := []string{"notes_ai", "notes_ad", "notes_au"}
	for _, trg := range wantTriggers {
		var name string
		if err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trg,
		).Scan(&name); err != nil {
			t.Errorf("trigger %q missing: %v", trg, err)
		}
	}

	var version int
	if err := db.QueryRow(`SELECT version FROM user_version WHERE id = 1`).Scan(&version); err != nil {
		t.Fatalf("user_version row missing: %v", err)
	}
	if version != 1 {
		t.Fatalf("user_version = %d, want 1", version)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_version`).Scan(&count); err != nil {
		t.Fatalf("count user_version: %v", err)
	}
	if count != 1 {
		t.Fatalf("user_version rows = %d, want 1", count)
	}
}

func TestTriggersSyncFTS(t *testing.T) {
	db := openTestDB(t)
	if err := Apply(db); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO notes (path, content, hash, mtime, title, tags) VALUES (?, ?, ?, ?, ?, ?)`,
		"a.md", "hello world", "h1", 1700000000, "Hello", `["x"]`,
	); err != nil {
		t.Fatalf("insert note: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notes_fts WHERE notes_fts MATCH 'hello'`).Scan(&n); err != nil {
		t.Fatalf("fts match after insert: %v", err)
	}
	if n != 1 {
		t.Fatalf("fts rows matching 'hello' = %d, want 1", n)
	}

	if _, err := db.Exec(`UPDATE notes SET content = 'goodbye', title = 'Bye' WHERE path = 'a.md'`); err != nil {
		t.Fatalf("update note: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM notes_fts WHERE notes_fts MATCH 'hello'`).Scan(&n); err != nil {
		t.Fatalf("fts match after update: %v", err)
	}
	if n != 0 {
		t.Fatalf("fts rows matching 'hello' after update = %d, want 0", n)
	}

	if _, err := db.Exec(`DELETE FROM notes WHERE path = 'a.md'`); err != nil {
		t.Fatalf("delete note: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM notes_fts`).Scan(&n); err != nil {
		t.Fatalf("fts count after delete: %v", err)
	}
	if n != 0 {
		t.Fatalf("fts rows after delete = %d, want 0", n)
	}
}
