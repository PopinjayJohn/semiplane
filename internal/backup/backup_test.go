package backup

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PopinjayJohn/semiplane/internal/migrate"
	_ "modernc.org/sqlite"
)

// seedKVDB makes a tiny standalone SQLite database at path (mirroring
// what SQLite is documented to accept: any dbPath, schema unknown to
// this package) in WAL mode, so the VACUUM INTO path is exercised
// against a WAL-journaled source, not just the default journal.
func seedKVDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("journal_mode WAL: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE kv(k TEXT PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for k, v := range map[string]string{
		"alpha": "letter a", "beta": "letter b", "gamma": "letter g",
	} {
		if _, err := db.Exec(`INSERT INTO kv(k, v) VALUES(?, ?)`, k, v); err != nil {
			t.Fatalf("insert %s: %v", k, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

func TestSQLiteVACUUMIntoCopiesRows(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	seedKVDB(t, src)

	target := filepath.Join(dir, "backup.db")
	if err := SQLite(src, target); err != nil {
		t.Fatalf("SQLite: %v", err)
	}

	// Open the SECOND database (the copy) and verify rows present.
	db, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT k, v FROM kv ORDER BY k`)
	if err != nil {
		t.Fatalf("query copy: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got map[string]string
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if got == nil {
			got = map[string]string{}
		}
		got[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := map[string]string{
		"alpha": "letter a", "beta": "letter b", "gamma": "letter g",
	}
	if len(got) != len(want) {
		t.Fatalf("copy rows: got %d (%v), want %d", len(got), got, len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("copy[%s] = %q, want %q", k, got[k], v)
		}
	}
}

func TestSQLiteErrTargetExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	seedKVDB(t, src)

	target := filepath.Join(dir, "full.db")
	if err := os.WriteFile(target, []byte("previous backup, keep me"), 0o644); err != nil {
		t.Fatalf("seed existing target: %v", err)
	}
	err := SQLite(src, target)
	if !errors.Is(err, ErrTargetExists) {
		t.Fatalf("SQLite into existing target: err = %v, want errors.Is(ErrTargetExists)", err)
	}
	// The pre-existing target is the caller's to decide about: check we
	// never truncated a file that happened to be a valid sqlite db.
	kept, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read existing target: %v", err)
	}
	if string(kept) != "previous backup, keep me" {
		t.Errorf("existing target clobbered: %q", kept)
	}
}

func TestSQLiteMissingSourceFailsNotTargetExists(t *testing.T) {
	dir := t.TempDir()
	err := SQLite(filepath.Join(dir, "does-not-exist.db"), filepath.Join(dir, "out.db"))
	if err == nil {
		t.Fatal("SQLite on missing source: want error, got nil")
	}
	if errors.Is(err, ErrTargetExists) {
		t.Errorf("missing source wrongly surfaced as ErrTargetExists: %v", err)
	}
}

// scratchVault builds the test tree: plain md files, a nested dir, a
// hidden extra-tool dir (kept), and .semiplane contents (excluded).
func scratchVault(t *testing.T) (vault string, included map[string]string) {
	t.Helper()
	vault = t.TempDir()
	included = map[string]string{
		"root-note.md":                  "# Root\n\nThe campaign starts here.\n",
		"campaign/gm-only.md":           "# GM Secrets\n\n:::gm\nthe lich is their brother\n:::\n",
		"characters/throg/character.md": "# Throg\n\nHP 45/45.\n",
		".obsidian/app.json":            "{}\n",
	}
	for rel, body := range included {
		full := filepath.Join(vault, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(vault, ".semiplane"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"index.db":     "derived sqlite index bytes",
		"secret.txt":   "derived oplog/logs belong to the process, not the backup",
		"plugins/x.js": "console.log('runtime plugin')",
	} {
		full := filepath.Join(vault, ".semiplane", rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir .semiplane/%s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write .semiplane/%s: %v", rel, err)
		}
	}
	return vault, included
}

func TestVaultZipContents(t *testing.T) {
	vault, included := scratchVault(t)
	out := filepath.Join(t.TempDir(), "vault.zip")
	if err := Vault(vault, out); err != nil {
		t.Fatalf("Vault: %v", err)
	}

	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer func() { _ = zr.Close() }()

	names := make([]string, 0, len(zr.File))
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		names = append(names, f.Name)
		byName[f.Name] = f
	}

	// .semiplane/* ABSENT (derived index.db + oplog/logs + plugins).
	for _, banned := range []string{
		".semiplane", ".semiplane/index.db", ".semiplane/secret.txt",
		".semiplane/plugins", ".semiplane/plugins/x.js",
	} {
		for _, n := range names {
			if n == banned || strings.HasPrefix(n, banned+"/") {
				t.Errorf("archive contains excluded path %q", n)
			}
		}
	}
	// Hidden dirs other than .semiplane are CONTENT (vault is source of truth).
	for _, must := range []string{"root-note.md", "campaign/gm-only.md", "characters/throg/character.md", ".obsidian/app.json"} {
		if _, ok := byName[must]; !ok {
			t.Errorf("archive missing %q; have %v", must, names)
		}
	}
	// md files present with correct bytes.
	for rel, body := range included {
		f := byName[rel]
		if f == nil {
			t.Errorf("archive missing %q", rel)
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", rel, err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		_ = rc.Close()
		if buf.String() != body {
			t.Errorf("archive %s bytes = %q, want %q", rel, buf.String(), body)
		}
	}

	// Zip entry count matches expectation (exactly the included files).
	if len(names) != len(included) {
		t.Errorf("entry count = %d (%v), want %d", len(names), names, len(included))
	}

	// No absolute paths inside the archive.
	for _, n := range names {
		if strings.HasPrefix(n, "/") || strings.Contains(n, "..") || strings.Contains(n, "\\") {
			t.Errorf("unsafe archive path %q", n)
		}
	}
	// Sorted (deterministic order).
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("archive not sorted: %q before %q", names[i-1], names[i])
		}
	}
	// Method Deflate on every entry; modtime suppressed (byte-reproducible).
	// Readers reconstruct zero modtimes as the DOS-zero artifact
	// (1979-11-30); the meaningful property is that every entry carries
	// the SAME constant, never a walk-time wall clock.
	dosZero := time.Date(1979, 11, 30, 0, 0, 0, 0, time.UTC)
	var stamp time.Time
	for i, f := range zr.File {
		if f.Method != zip.Deflate {
			t.Errorf("entry %q method = %d, want Deflate (%d)", f.Name, f.Method, zip.Deflate)
		}
		if i == 0 {
			stamp = f.Modified.UTC()
			if stamp.Sub(dosZero) > time.Minute || time.Since(stamp).Abs() < time.Minute {
				t.Errorf("entry modtime %v is a real timestamp, want the DOS-zero constant %v (writer must zero Modified)", stamp, dosZero)
			}
		} else if !f.Modified.UTC().Equal(stamp) {
			t.Errorf("entry %q modtime %v differs from entry 0's %v: timestamps leaked in", f.Name, f.Modified, stamp)
		}
	}
}

func TestVaultDeterministicBytes(t *testing.T) {
	vault, _ := scratchVault(t)
	out1 := filepath.Join(t.TempDir(), "one.zip")
	out2 := filepath.Join(t.TempDir(), "two.zip")
	// Second run happens later: any wall-clock leakage into the archive
	// would break byte identity.
	time.Sleep(2 * time.Second)
	if err := Vault(vault, out1); err != nil {
		t.Fatalf("Vault one: %v", err)
	}
	if err := Vault(vault, out2); err != nil {
		t.Fatalf("Vault two: %v", err)
	}
	b1, err := os.ReadFile(out1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(out2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b1, b2) {
		t.Errorf("two zips of the same tree differ (%d vs %d bytes): not reproducible", len(b1), len(b2))
	}
}

func TestVaultSkipsSymlinkSilently(t *testing.T) {
	vault, included := scratchVault(t)
	if err := os.Symlink(filepath.Join(vault, "root-note.md"), filepath.Join(vault, "dangling-link.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	out := filepath.Join(t.TempDir(), "vault.zip")
	if err := Vault(vault, out); err != nil {
		t.Fatalf("Vault with symlink present: %v (want silent skip)", err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.Name == "dangling-link.md" {
			t.Errorf("symlink archived; want silently skipped")
		}
	}
	// The symlink addition must change nothing else: count still equals
	// the plain tree (symlink skipped silently, not replaced by a copy).
	if len(zr.File) != len(included) {
		t.Errorf("entry count = %d, want %d (symlink should be skipped, not followed)", len(zr.File), len(included))
	}
}

func TestVaultRefusesEmptyRootAndTarget(t *testing.T) {
	if err := Vault("", filepath.Join(t.TempDir(), "x.zip")); err == nil {
		t.Error("Vault with empty vault root: want error")
	}
	vault, _ := scratchVault(t)
	if err := Vault(vault, ""); err == nil {
		t.Error("Vault with empty target: want error")
	}
}

func TestCheckHealthyDBAllOk(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".semiplane", "index.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	if err := migrate.Apply(db); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	vault := t.TempDir()

	res := Check(db, vault)
	if len(res) != 6 {
		t.Fatalf("Check returned %d results (%v), want 6", len(res), res)
	}
	wantOK := map[string]bool{"vault_dir": true, "users": true, "sessions_cleanup": true, "acls": true, "fts5": true, "vacuum_into": true}
	for _, line := range res {
		status, rest, _ := strings.Cut(line, ":")
		name, _, _ := strings.Cut(rest, ":")
		if status != "ok" && status != "skip" && status != "FAIL" {
			t.Errorf("bad result format %q (status %q not in ok/skip/FAIL)", line, status)
		}
		if !wantOK[name] {
			t.Errorf("unexpected check name %q in %q", name, line)
			continue
		}
		delete(wantOK, name)
	}
	for name := range wantOK {
		t.Errorf("Check did not report %q", name)
	}
}

func TestCheckUnmigratedDBReportsFailures(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "raw.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	vault := t.TempDir()

	res := Check(db, vault)
	joined := strings.Join(res, "\n")
	for _, name := range []string{"users", "sessions_cleanup", "acls"} {
		if !strings.Contains(joined, "FAIL:"+name+":") {
			t.Errorf("unmigrated db: want FAIL:%s in %v", name, res)
		}
	}
	// vacuum_into still succeeds: an unmigrated db is a valid empty
	// sqlite db, and backing it up is precisely what SQLite does.
	if !strings.Contains(joined, "ok:vacuum_into") {
		t.Errorf("want ok:vacuum_into in %v", res)
	}
}

func TestCheckInMemoryDBSkipsVacuum(t *testing.T) {
	// Unique name per test run: shared-cache in-memory dbs are
	// process-global by name.
	dsn := "file:doctor-probe-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	if err := migrate.Apply(db); err != nil {
		t.Fatal(err)
	}
	res := Check(db, "")
	joined := strings.Join(res, "\n")
	if !strings.Contains(joined, "skip:vacuum_into:") {
		t.Errorf("in-memory db: want skip:vacuum_into:<reason> in %v", res)
	}
	if !strings.Contains(joined, "skip:vault_dir:") {
		t.Errorf("empty vault root: want skip:vault_dir:<reason> in %v", res)
	}
	for _, name := range []string{"users", "sessions_cleanup", "acls", "fts5"} {
		if !strings.Contains(joined, "ok:"+name) {
			t.Errorf("in-memory db: want ok:%s in %v", name, res)
		}
	}
}

func TestCheckMisconfiguredVaultRootFails(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	if err := migrate.Apply(db); err != nil {
		t.Fatal(err)
	}
	// vault root points at a FILE, not a directory.
	notADir := filepath.Join(dir, "campaign.md")
	if err := os.WriteFile(notADir, []byte("# nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := Check(db, notADir)
	joined := strings.Join(res, "\n")
	if !strings.Contains(joined, "FAIL:vault_dir:") {
		t.Errorf("want FAIL:vault_dir in %v", res)
	}
}
