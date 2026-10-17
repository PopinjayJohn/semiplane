// doctor.go is the minimal self-check feeding `semiplane doctor`
// (tech-stack §3.7: log/config/path diagnostics attached to bug reports).
//
// Scope decision, per the integrator: this file ships ONLY the shared
// Check(db *sql.DB, vaultRoot string) []string primitive. A `RunChecks`
// command-facing wrapper (flag parsing, log plumbing, bundle assembly) is
// left to the doctor command's integrator so doctor and backup do not
// race on CLI evolution.
//
// Result contract: each returned string is exactly one of
//
//	"ok:<name>"               — the semantic check passed
//	"skip:<name>:<reason>"    — waived, never a failure (documented extension)
//	"FAIL:<name>:<detail>"    — the check failed, with driver/ex detail
//
// Results are returned in a fixed order so doctor output diff-stably.
//
// Check NEVER writes the caller's data: table checks are reads; the
// sessions-expiry check performs its INSERT/DELETE dance inside a
// transaction that is always ROLLed BACK (net effect zero rows); the
// VACUUM INTO check targets a fresh OS temp dir, never the vault.
package backup

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Check runs the doctor's database self-checks against an OPEN,
// schema-applied database (migrate.Apply'ed — the shared index/auth pool
// pattern) plus the vault root.
//
// Checks, in fixed order:
//
//   - vault_dir      the configured vault root exists and is a directory
//     (skipped when vaultRoot is empty: the caller may be
//     checking a bare database).
//   - users          the users table is reachable (SELECT COUNT(*)):
//     proves the table exists AND is materialized.
//   - sessions_cleanup  expired-session cleanup works: inside an IMMEDIATE
//     transaction, insert one deliberately expired row,
//     delete by expires_at <= now, assert RowsAffected == 1
//     to prove the predicate matches real rows, then ROLLBACK.
//     The write is never committed, so the caller's session
//     table is untouched whatever the outcome.
//   - acls           the acls table is reachable (SELECT COUNT(*)).
//   - fts5           the ENABLE_FTS5 compile option is still present —
//     the guard internal/index.Open also asserts; repeated
//     here because doctor must catch a binary built
//     against a different SQLite than the vault expects.
//   - vacuum_into    a backup (SQLite: VACUUM INTO) of THIS database
//     succeeds into a fresh OS temp dir: the exact backup
//     path, exercised end to end, writing nothing near the
//     vault. Skipped for in-memory databases (no file to
//     back up is not a failure).
func Check(db *sql.DB, vaultRoot string) []string {
	res := make([]string, 0, 6)
	appendResult := func(name string, err error, skipped string) {
		switch {
		case err != nil:
			res = append(res, fmt.Sprintf("FAIL:%s:%s", name, compactError(err)))
		case skipped != "":
			res = append(res, fmt.Sprintf("skip:%s:%s", name, skipped))
		default:
			res = append(res, "ok:"+name)
		}
	}

	// vault_dir
	if strings.TrimSpace(vaultRoot) == "" {
		appendResult("vault_dir", nil, "no vault root configured")
	} else if info, err := os.Stat(vaultRoot); err != nil {
		appendResult("vault_dir", fmt.Errorf("stat %q: %w", vaultRoot, err), "")
	} else if !info.IsDir() {
		appendResult("vault_dir", fmt.Errorf("%q is not a directory", vaultRoot), "")
	} else {
		appendResult("vault_dir", nil, "")
	}

	// users
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		appendResult("users", err, "")
	} else {
		appendResult("users", nil, "")
	}

	// sessions_cleanup: prove the expiry predicate deletes rows, inside a
	// transaction that is ALWAYS rolled back (net-zero on the caller's data).
	appendResult("sessions_cleanup", checkSessionsCleanup(context.Background(), db), "")

	// acls
	if err := db.QueryRow(`SELECT COUNT(*) FROM acls`).Scan(&n); err != nil {
		appendResult("acls", err, "")
	} else {
		appendResult("acls", nil, "")
	}

	// fts5 compile option (same guard recipe as internal/index.Open).
	var fts5 int
	if err := db.QueryRow(`SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&fts5); err != nil {
		appendResult("fts5", err, "")
	} else if fts5 != 1 {
		appendResult("fts5", fmt.Errorf("sqlite built without ENABLE_FTS5"), "")
	} else {
		appendResult("fts5", nil, "")
	}

	// vacuum_into: exercise the real backup path into a throwaway dir.
	skip, verr := checkVacuumInto(context.Background(), db)
	appendResult("vacuum_into", verr, skip)
	return res
}

// checkSessionsCleanup verifies the expired-session delete predicate hits
// at least one real row. Runs on a single pinned connection inside an
// explicit IMMEDIATE transaction that is always rolled back — mirroring
// internal/auth's write pattern so the check exercises the same locking
// path as production cleanup.
func checkSessionsCleanup(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	const probeID = "doctor-expired-probe"
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO sessions(id, user_id, created_at, expires_at, hash) VALUES(?, ?, ?, ?, ?)`,
		probeID, probeID, time.Now().Unix()-7200, time.Now().Unix()-3600, "doctor-probe"); err != nil {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	dx, err := conn.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix())
	if err != nil {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	n, err := dx.RowsAffected()
	if err != nil {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	if n != 1 {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return fmt.Errorf("expired-cleanup delete matched %d rows, want 1", n)
	}
	// Always end in ROLLBACK: the probe row is erased and the caller's
	// session table never sees it outside this transaction.
	if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
		return fmt.Errorf("rollback after probe: %w", err)
	}
	return nil
}

// checkVacuumInto runs the real SQLite backup path (VACUUM INTO) from the
// doctor's open database into a fresh OS temp dir and checks the copy is
// a non-empty file. It never writes into the vault and never touches the
// caller's database file beyond its own read connection. An in-memory
// database yields a non-empty skip reason (with no file backing there is
// nothing to assert) and an empty error, which the caller renders as
// "skip:vacuum_into:<reason>". A real failure yields a non-empty error.
func checkVacuumInto(ctx context.Context, db *sql.DB) (skip string, err error) {
	path, skipReason, err := mainDatabaseFile(ctx, db)
	if err != nil {
		return "", err
	}
	if skipReason != "" {
		return skipReason, nil
	}
	tmp, err := os.MkdirTemp("", "semiplane-doctor-vacuum-*")
	if err != nil {
		return "", fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	target := filepath.Join(tmp, "vacuum-check.db")
	if err := SQLite(path, target); err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("vacuum copy at %q is empty", target)
	}
	return "", nil
}

// mainDatabaseFile returns the on-disk path of the connection's "main"
// database via PRAGMA database_list. skipReason is non-empty exactly when
// vacuum_into has nothing meaningful to assert (in-memory main — the
// pragma reports it with an empty file column).
func mainDatabaseFile(ctx context.Context, db *sql.DB) (path string, skipReason string, err error) {
	rows, err := db.QueryContext(ctx, `PRAGMA database_list`)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			_ = rows.Close()
			return "", "", err
		}
		if name == "main" {
			_ = rows.Close()
			if file == "" {
				return "", "main database is in-memory: nothing to back up", nil
			}
			return file, "", nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	return "", "", nil
}
