package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/PopinjayJohn/semiplane/internal/migrate"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
	_ "modernc.org/sqlite"
)

// Open opens (creating parent dirs as needed) the SQLite index database,
// asserts FTS5 support, applies the schema, and restricts the pool to a
// single connection so the BEGIN IMMEDIATE transactions below always
// run on one connection.
func Open(dbPath string) (*sql.DB, error) {
	var dsn string
	if dbPath == ":memory:" {
		dsn = "file:memdb?mode=memory&cache=shared&_busy_timeout=5000&_foreign_keys=on&_txlock=immediate"
	} else {
		if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
		dsn = dbPath + "?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on&_txlock=immediate"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	var fts5 int
	// NOTE: the compile-option name is 'ENABLE_FTS5' (SQLITE_ENABLE_FTS5);
	// 'FTS5' alone reports 0 even though the module is present (verified
	// against pragma_module_list + a trial CREATE VIRTUAL TABLE ... USING
	// fts5 on modernc.org/sqlite v1.54.0 / SQLite 3.53.3).
	if err := db.QueryRow(`SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&fts5); err != nil {
		_ = db.Close()
		return nil, err
	}
	if fts5 != 1 {
		_ = db.Close()
		return nil, fmt.Errorf("index: sqlite FTS5 support not compiled in")
	}
	if err := migrate.Apply(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Index is the derived, rebuildable full-text + backlink index over a vault.
type Index struct {
	DB *sql.DB
}

// New wraps an already-opened database (schema applied) as an Index.
func New(db *sql.DB) *Index {
	return &Index{DB: db}
}

var (
	linkRe    = regexp.MustCompile(`\[\[([^\]|#]+)(?:\|[^\]]*)?\]`)
	headingRe = regexp.MustCompile(`^#{1,6}\s+(.+)$`)
	blockItem = regexp.MustCompile(`^\s*-\s+(.+)$`)
)

// ParseLinks extracts normalized wikilink targets from Markdown content.
// Alias (`[[t|label]]`) is dropped; empty targets and external links
// (containing "://" or starting with "#") are dropped. Targets are
// normalized by trimming leading "./" segments and appending ".md" when no
// ".md" suffix is present. Case is preserved. The result is sorted unique.
func ParseLinks(content string) []string {
	matches := linkRe.FindAllStringSubmatch(content, -1)
	set := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		t := strings.TrimSpace(m[1])
		if t == "" {
			continue
		}
		if strings.Contains(t, "://") || strings.HasPrefix(t, "#") {
			continue
		}
		for strings.HasPrefix(t, "./") {
			t = strings.TrimPrefix(t, "./")
		}
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !strings.HasSuffix(t, ".md") {
			t += ".md"
		}
		set[t] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// ParseTitle returns the first ATX heading (`# Title`) in content, falling
// back to the note path's basename without its .md suffix.
func ParseTitle(notePath, content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if m := headingRe.FindStringSubmatch(line); m != nil {
			if t := strings.TrimSpace(m[1]); t != "" {
				return t
			}
		}
	}
	return strings.TrimSuffix(path.Base(notePath), ".md")
}

// ParseTags parses leading YAML frontmatter (`---` ... `---`) for a `tags:`
// key. This is a minimal hand-rolled parser supporting exactly two shapes:
//
//	tags: [a, b]      (single-line flow list)
//	tags:             (block list)
//	  - a
//	  - b
//
// Anything else (no frontmatter, no tags key, multi-line flow, parse
// failure) yields an empty slice, never an error. Items are trimmed of
// surrounding whitespace and single/double quotes. A single-line scalar
// (`tags: foo`) is accepted as one tag.
func ParseTags(content string) []string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return []string{}
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return []string{}
	}
	fm := lines[1:end]
	for i, ln := range fm {
		trimmed := strings.TrimSpace(ln)
		if !strings.HasPrefix(trimmed, "tags:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "tags:"))
		if strings.HasPrefix(rest, "[") {
			if !strings.HasSuffix(rest, "]") {
				return []string{}
			}
			inner := strings.TrimSpace(rest[1 : len(rest)-1])
			if inner == "" {
				return []string{}
			}
			var out []string
			for _, p := range strings.Split(inner, ",") {
				t := strings.TrimSpace(strings.Trim(strings.TrimSpace(p), `"'`))
				if t != "" {
					out = append(out, t)
				}
			}
			if out == nil {
				return []string{}
			}
			return out
		}
		if rest != "" {
			t := strings.TrimSpace(strings.Trim(rest, `"'`))
			if t == "" {
				return []string{}
			}
			return []string{t}
		}
		var out []string
		for _, ln2 := range fm[i+1:] {
			m := blockItem.FindStringSubmatch(ln2)
			if m == nil {
				break
			}
			t := strings.TrimSpace(strings.Trim(strings.TrimSpace(m[1]), `"'`))
			if t != "" {
				out = append(out, t)
			}
		}
		if out == nil {
			return []string{}
		}
		return out
	}
	return []string{}
}

// inImmediateTx runs fn on a single pinned connection inside an explicit
// BEGIN IMMEDIATE / COMMIT transaction, rolling back on error.
func (ix *Index) inImmediateTx(ctx context.Context, fn func(context.Context, *sql.Conn) error) error {
	conn, err := ix.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := fn(ctx, conn); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

// Upsert inserts or replaces the cached note row and rebuilds its outgoing
// backlink edges in one IMMEDIATE transaction. FTS rows sync via the
// notes_ai/notes_ad/notes_au triggers automatically.
func (ix *Index) Upsert(notePath, content, hash string, mtime int64) error {
	title := ParseTitle(notePath, content)
	tags := ParseTags(content)
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	links := ParseLinks(content)
	ctx := context.Background()
	return ix.inImmediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx,
			`INSERT OR REPLACE INTO notes(path, content, hash, mtime, title, tags) VALUES(?, ?, ?, ?, ?, ?)`,
			notePath, content, hash, mtime, title, string(tagsJSON)); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM backlinks WHERE src = ?`, notePath); err != nil {
			return err
		}
		for _, dst := range links {
			if _, err := conn.ExecContext(ctx,
				`INSERT OR IGNORE INTO backlinks(src, dst) VALUES(?, ?)`, notePath, dst); err != nil {
				return err
			}
		}
		return nil
	})
}

// Delete removes the note row (FTS syncs via trigger) and every backlink
// edge touching the path as either source or destination.
func (ix *Index) Delete(notePath string) error {
	ctx := context.Background()
	return ix.inImmediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `DELETE FROM notes WHERE path = ?`, notePath); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `DELETE FROM backlinks WHERE src = ? OR dst = ?`, notePath, notePath)
		return err
	})
}

const searchSQL = `SELECT path, snippet(notes_fts, 2, '<b>', '</b>', '...', 10), bm25(notes_fts) FROM notes_fts WHERE notes_fts MATCH ? ORDER BY bm25(notes_fts) LIMIT ?`

func (ix *Index) runSearch(query string, limit int) ([]pluginapi.SearchHit, error) {
	rows, err := ix.DB.Query(searchSQL, query, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var hits []pluginapi.SearchHit
	for rows.Next() {
		var h pluginapi.SearchHit
		var rank float64
		if err := rows.Scan(&h.Path, &h.Snippet, &rank); err != nil {
			return nil, err
		}
		h.Score = -rank // bm25 is lower-is-better; negate to fit Score (higher=better)
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}

// Search runs a BM25-ranked FTS5 query, returning at most limit hits
// (clamped to 1..50, defaulting to 10 on limit <= 0). A query that fails
// with a MATCH syntax error is retried once as a quoted phrase.
func (ix *Index) Search(query string, limit int) ([]pluginapi.SearchHit, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	hits, err := ix.runSearch(query, limit)
	if err != nil {
		quoted := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
		hits, err = ix.runSearch(quoted, limit)
	}
	if err != nil {
		return nil, err
	}
	if hits == nil {
		hits = []pluginapi.SearchHit{}
	}
	return hits, nil
}

// Backlinks returns the sorted source paths linking to notePath.
func (ix *Index) Backlinks(notePath string) ([]string, error) {
	rows, err := ix.DB.Query(`SELECT src FROM backlinks WHERE dst = ? ORDER BY src`, notePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var src string
		if err := rows.Scan(&src); err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// Reindex walks vaultRoot for *.md files (skipping .semiplane and .git
// trees), upserting each into the index, then deletes note/backlink rows for
// paths no longer on disk. Files that fail to read are logged and skipped.
// It returns the number of notes indexed.
func (ix *Index) Reindex(vaultRoot string) (int, error) {
	disk := make(map[string]struct{})
	count := 0
	walkErr := filepath.WalkDir(vaultRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			slog.Warn("index: reindex walk error, skipping", "path", p, "err", err)
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".semiplane" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, err := filepath.Rel(vaultRoot, p)
		if err != nil {
			slog.Warn("index: reindex rel error, skipping", "path", p, "err", err)
			return nil
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(p)
		if err != nil {
			slog.Warn("index: reindex read error, skipping", "path", p, "err", err)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			slog.Warn("index: reindex stat error, skipping", "path", p, "err", err)
			return nil
		}
		sum := sha256.Sum256(data)
		if err := ix.Upsert(rel, string(data), hex.EncodeToString(sum[:]), info.ModTime().Unix()); err != nil {
			slog.Warn("index: reindex upsert error, skipping", "path", p, "err", err)
			return nil
		}
		disk[rel] = struct{}{}
		count++
		return nil
	})
	if walkErr != nil {
		return count, walkErr
	}
	ctx := context.Background()
	cleanupErr := ix.inImmediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `SELECT path FROM notes`)
		if err != nil {
			return err
		}
		var stale []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				_ = rows.Close()
				return err
			}
			if _, ok := disk[p]; !ok {
				stale = append(stale, p)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		const chunk = 500
		for i := 0; i < len(stale); i += chunk {
			j := i + chunk
			if j > len(stale) {
				j = len(stale)
			}
			holders := make([]string, 0, j-i)
			args := make([]any, 0, j-i)
			for _, p := range stale[i:j] {
				holders = append(holders, "?")
				args = append(args, p)
			}
			if _, err := conn.ExecContext(ctx,
				`DELETE FROM notes WHERE path IN (`+strings.Join(holders, ", ")+`)`, args...); err != nil {
				return err
			}
		}
		// Drop edges whose SOURCE is gone. Dangling destinations (links to
		// notes that do not exist yet) are kept: they resolve when the
		// target is created and feed the graph's "unlinked/dangling" view.
		_, err = conn.ExecContext(ctx,
			`DELETE FROM backlinks WHERE src NOT IN (SELECT path FROM notes)`)
		return err
	})
	if cleanupErr != nil {
		return count, cleanupErr
	}
	return count, nil
}

// RebuildFTS rebuilds the FTS5 index from the notes table content.
func (ix *Index) RebuildFTS() error {
	_, err := ix.DB.Exec(`INSERT INTO notes_fts(notes_fts) VALUES('rebuild')`)
	return err
}
