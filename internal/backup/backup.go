// Package backup implements the storage half of the `semiplane backup`
// command (tech-stack §3.7): a correct online SQLite backup via
// `VACUUM INTO` and a zipped snapshot of the vault's Markdown tree.
//
// Division of labor: Vault backs up the system of record — the .md tree is
// the real backup target and is Git-friendly by design (§1.2, §3.7). SQLite
// backs up the derived index (.semiplane/index.db) in consistent, portable
// form so a restore can skip a full reindex. Both are pure functions of a
// path pair; neither opens a server, daemon, or log file.
package backup

import (
	"archive/zip"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// ErrTargetExists is returned by SQLite when the output path already
// exists. It is a sentinel for callers to detect AND react to
// (errors.Is): refusing to overwrite is the package's job, deleting the
// old backup is the caller's decision (or rename-then-replace policy).
var ErrTargetExists = errors.New("backup: target file already exists")

// SQLite produces a consistent snapshot of the SQLite database at dbPath
// by running `VACUUM INTO ?` with the absolute target path, then closes
// the source (tech-stack §3.7: "correct online SQLite backup").
//
// Safety properties, and why no WAL checkpoint is needed here:
//
//   - VACUUM INTO walks the source's pages under a shared read lock,
//     including whatever is live in the -wal file, and writes a fully
//     defragmented, self-contained database to the target mid-traffic.
//     The snapshot is transaction-atomic for readers/writers on the
//     source: it either see a complete moment or retries under
//     busy_timeout. A checkpoint would only matter if we copied the
//     raw files byte-for-byte — we never do.
//   - The source is opened READ-ONLY ("file:...?mode=ro"): VACUUM INTO
//     never writes the source (it builds the copy aside), so a backup of
//     a live vault cannot itself corrupt or perturb the running index.
//   - The target must NOT pre-exist. SQLite's VACUUM INTO refuses to
//     overwrite, and we pre-check with os.Stat so the failure surfaces
//     as errors.Is(err, ErrTargetExists) instead of the driver's opaque
//     "file is not a database" / "output file already exists". SQLite's
//     own refusal remains the authoritative guard against a stat/exec
//     race; our pre-check is just the friendly, typed version of it.
//     Deleting an existing target is a caller decision, by design.
//
// The busy_timeout mirrors internal/index's DSN hygiene (§5 "WAL mode +
// busy_timeout") so a heavily-written vault yields "consistent-or-retry",
// not an instant SQLITE_BUSY. No _journal_mode is set on a read-only
// connection: journal mode is persistent in the database header and a
// reader cannot (and must not) flip it.
func SQLite(dbPath string, outPath string) error {
	if dbPath == "" {
		return errors.New("backup: SQLite: empty source path")
	}
	if outPath == "" {
		return errors.New("backup: SQLite: empty target path")
	}
	absTarget, err := filepath.Abs(outPath)
	if err != nil {
		return fmt.Errorf("backup: resolve target %q: %w", outPath, err)
	}
	if _, err := os.Stat(absTarget); err == nil {
		// VACUUM INTO refuses to overwrite; surface it as the sentinel.
		return fmt.Errorf("backup: %w: %s", ErrTargetExists, absTarget)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("backup: stat target %q: %w", absTarget, err)
	}
	// filepath.ToSlash keeps Windows drive-letter paths URL-parseable
	// ("file:C:/v/.semiplane/index.db"), matching modernc's DSN handling.
	dsn := "file:" + filepath.ToSlash(dbPath) + "?mode=ro&_busy_timeout=5000"
	src, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("backup: open source %q: %w", dbPath, err)
	}
	defer func() { _ = src.Close() }()
	if err := src.Ping(); err != nil {
		return fmt.Errorf("backup: open source %q: %w", dbPath, err)
	}
	// The bound parameter is evaluated as the INTO clause's filename
	// expression; modernc.org/sqlite v1.54.0 supports it (verified).
	if _, err := src.Exec(`VACUUM INTO ?`, absTarget); err != nil {
		return fmt.Errorf("backup: VACUUM INTO %s: %w", absTarget, err)
	}
	// Sanity: the driver could in principle succeed while producing an
	// empty target; refuse to report success for a zero-byte backup.
	info, err := os.Stat(absTarget)
	if err != nil {
		return fmt.Errorf("backup: stat vacuumed target %q: %w", absTarget, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("backup: VACUUM INTO produced empty target %q", absTarget)
	}
	return nil
}

// excludedDir is the vault's machine directory (tech-stack §10:
// plugins/oplog/logs + the derived index.db). Everything under it is
// rebuildable — `semiplane reindex` reconstructs the index from the .md
// tree (§3.3 Invariant, §5 "Index rebuildability") — so it is EXCLUDED
// from vault zips: it is not user content, and its plugin payloads are
// redistributed artifacts the user wants, not data they own alone.
const excludedDir = ".semiplane"

// Vault zips the vault at vaultRoot (the filesystem source of truth,
// tech-stack §1.2) to outPath, preserving relative slash-separated paths
// in sorted order so two zips of the same tree are byte-identical.
//
// What is included / excluded, and why:
//
//   - .semiplane/** is excluded (see excludedDir): derived oplog/logs
//     AND index.db are rebuildable (§5, §10). Every OTHER hidden
//     directory (.obsidian, .trash, .pandoc, …) is INCLUDED: the vault
//     is the source of truth and users hide real content there.
//   - Only REGULAR files are archived. Symlinks (and sockets, devices,
//     named pipes) are skipped silently, no error, no entry: zipping
//     through a symlink would double-include data or escape the vault
//     root entirely, and a hard link's second name is just a second
//     regular file (both copies land, share-on-disk is irrelevant to
//     the archive). A silently skipped symlink is the honest summary,
//     not a bug — callers wanting a manifest of skips should parse the
//     returned archive names, not the filesystem.
//   - outPath itself is skipped if it sits inside vaultRoot, so a backup
//     written back into the vault cannot include itself mid-write.
//
// Determinism: entries are written in sorted relative-path order with a
// zero ModTime (modification times differ per walk and would break
// byte-identical reruns; content, names, and modes are kept). Method is
// Deflate on every entry.
//
// Unlike SQLite (VACUUM INTO refuses to overwrite), an existing outPath
// is truncated and reused: callers wanting keep-history semantics pass a
// dated filename; the refusal sentinel ErrTargetExists stays reserved
// for the database path where SQLite itself enforces it.
func Vault(vaultRoot string, outPath string) error {
	if vaultRoot == "" {
		return errors.New("backup: Vault: empty vault root")
	}
	if outPath == "" {
		return errors.New("backup: Vault: empty target path")
	}
	if info, err := os.Stat(vaultRoot); err != nil {
		return fmt.Errorf("backup: vault root %q: %w", vaultRoot, err)
	} else if !info.IsDir() {
		return fmt.Errorf("backup: vault root %q is not a directory", vaultRoot)
	}
	absOut, err := filepath.Abs(outPath)
	if err != nil {
		return fmt.Errorf("backup: resolve target %q: %w", outPath, err)
	}

	// Pass one: collect relative slash paths of regular files. Walk errors
	// abort the zip loud and early, since the vault is the source of truth
	// and a partial archive claims more than it delivers.
	var names []string
	walkErr := filepath.WalkDir(vaultRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("backup: walk %q: %w", p, err)
		}
		if d.IsDir() {
			if d.Name() == excludedDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			// Symlinks, sockets, pipes, devices: skipped on purpose
			// (see doc comment). WalkDir does not follow symlinked
			// directories, so "return nil" ends the chain here.
			return nil
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("backup: resolve %q: %w", p, err)
		}
		if abs == absOut {
			return nil // don't fold the zip we are writing into the zip
		}
		rel, err := filepath.Rel(vaultRoot, p)
		if err != nil {
			return fmt.Errorf("backup: rel %q under %q: %w", p, vaultRoot, err)
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	sort.Strings(names)

	// Pass two: archive in sorted order, reading each file fresh.
	if err := os.MkdirAll(filepath.Dir(absOut), 0o755); err != nil {
		return fmt.Errorf("backup: create target dir %q: %w", filepath.Dir(absOut), err)
	}
	outFile, err := os.Create(absOut)
	if err != nil {
		return fmt.Errorf("backup: create %q: %w", absOut, err)
	}
	zw := zip.NewWriter(outFile)
	for _, name := range names {
		full := filepath.Join(vaultRoot, filepath.FromSlash(name))
		data, err := os.ReadFile(full)
		if err != nil {
			_ = zw.Close()
			_ = outFile.Close()
			return fmt.Errorf("backup: read %q: %w", full, err)
		}
		info, err := os.Stat(full)
		if err != nil {
			_ = zw.Close()
			_ = outFile.Close()
			return fmt.Errorf("backup: stat %q: %w", full, err)
		}
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(info.Mode())
		// h.Modified stays zero: a walk-relative timestamp would make
		// two runs of the same tree differ by bytes-for-time, which
		// defeats the reproducibility contract.
		w, err := zw.CreateHeader(h)
		if err == nil {
			_, err = w.Write(data)
		}
		if err != nil {
			_ = zw.Close()
			_ = outFile.Close()
			return fmt.Errorf("backup: write %q: %w", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		_ = outFile.Close()
		return fmt.Errorf("backup: close %q: %w", absOut, err)
	}
	if err := outFile.Close(); err != nil {
		return fmt.Errorf("backup: close %q: %w", absOut, err)
	}
	return nil
}

// compactError flattens multi-line driver errors into one line so each
// Check result is a single-line string.
func compactError(err error) string {
	return strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " | ")
}
