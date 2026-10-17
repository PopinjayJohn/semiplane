package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/PopinjayJohn/semiplane/internal/acl"
	"github.com/PopinjayJohn/semiplane/internal/app"
	"github.com/PopinjayJohn/semiplane/internal/auth"
	"github.com/PopinjayJohn/semiplane/internal/backup"
	"github.com/PopinjayJohn/semiplane/internal/index"
	"github.com/PopinjayJohn/semiplane/pkg/pluginapi"
)

// envDefault returns the env var value when set, else def. Used so flags
// honor SEMIPLANE_* environment variables (flags > env > defaults).
func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envBool reports whether the env var key is set to a truthy value.
func envBool(key string) bool {
	return strings.EqualFold(os.Getenv(key), "true")
}

// mustParseDuration parses a duration, panicking on a bad env value so the
// operator learns at boot rather than at request time.
func mustParseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		panic(fmt.Sprintf("invalid duration %q: %v", s, err))
	}
	return d
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "semiplane:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("semiplane", flag.ContinueOnError)
	addr := fs.String("addr", envDefault("SEMIPLANE_ADDR", ":8080"), "listen address")
	vault := fs.String("vault", envDefault("SEMIPLANE_VAULT", "."), "vault directory")
	sessionTTL := fs.Duration("session-ttl", mustParseDuration(envDefault("SEMIPLANE_SESSION_TTL", "24h")), "session lifetime")
	forceSecure := fs.Bool("force-secure", envBool("SEMIPLANE_FORCE_SECURE"), "always set Secure on session cookies")
	// OIDC provider config from env/flags. All unset ⇒ auth.Deps.Provider/Config
	// stay nil and the two OIDC routes answer 503 problem+json (never panic).
	oidcIssuer := fs.String("oidc-issuer", envDefault("SEMIPLANE_OIDC_ISSUER", ""), "OIDC issuer URL")
	oidcClientID := fs.String("oidc-client-id", envDefault("SEMIPLANE_OIDC_CLIENT_ID", ""), "OIDC client id")
	oidcClientSecret := fs.String("oidc-client-secret", envDefault("SEMIPLANE_OIDC_CLIENT_SECRET", ""), "OIDC client secret")
	oidcRedirectURL := fs.String("oidc-redirect-url", envDefault("SEMIPLANE_OIDC_REDIRECT_URL", ""), "OIDC redirect URL")
	// Accept flags before or after the subcommand:
	// `semiplane reindex --vault DIR` and `semiplane --vault DIR reindex`.
	cmd := "serve"
	restArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		restArgs = args[1:]
	}
	if err := fs.Parse(restArgs); err != nil {
		return err
	}

	// Minimal CLI: `semiplane serve` is the default; accept an optional subcommand.
	if rest := fs.Args(); cmd == "serve" && len(rest) > 0 {
		cmd = rest[0]
	}

	switch cmd {
	case "serve":
		// No-op for the thin slice.
	case "doctor":
		return runDoctor(*vault)
	case "reindex":
		return runReindex(*vault)
	case "migrate", "backup", "user", "plugin":
		return fmt.Errorf("%s: not implemented in thin slice", cmd)
	default:
		return fmt.Errorf("unknown command %q (try: serve, doctor)", cmd)
	}

	cfg := app.Config{
		Addr:             *addr,
		VaultDir:         *vault,
		SessionTTL:       *sessionTTL,
		ForceSecure:      *forceSecure,
		OIDCIssuer:       *oidcIssuer,
		OIDCClientID:     *oidcClientID,
		OIDCClientSecret: *oidcClientSecret,
		OIDCRedirectURL:  *oidcRedirectURL,
	}
	srv, err := app.NewServer(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("semiplane serving", "addr", cfg.Addr, "vault", cfg.VaultDir)
	return srv.Run(ctx)
}

// runReindex rebuilds the derived SQLite index (.semiplane/index.db) from
// the Markdown vault. Losing index.db loses nothing; this command proves it.
// runDoctor extends the thin-slice doctor with the Phase 1/C self-check
// (tech-stack §3.7): against a scratch vault it opens the derived index
// DB (schema applied), seeds one user through the real auth stream, runs
// a login round-trip plus an ACL denial probe, verifies an Argon2id
// verify round-trip, and confirms VACUUM INTO + vault zip snapshots work.
// All writes land in a temp dir; the vault is never touched.
func runDoctor(vaultDir string) error {
	scratch, err := os.MkdirTemp("", "semiplane-doctor-*")
	if err != nil {
		return fmt.Errorf("doctor: create scratch: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	db, err := auth.Open(filepath.Join(scratch, "doctor.db"))
	if err != nil {
		return fmt.Errorf("doctor: open scratch db: %w", err)
	}
	defer func() { _ = db.Close() }()

	// Seed one user + prove the Argon2id round-trip and login path.
	const pw = "doctor-passphrase-9"
	const userID = "doctor-gm"
	if err := usersFor(db).Create(userID, "gm", pw); err != nil {
		return fmt.Errorf("doctor: create user: %w", err)
	}
	var phc string
	if err := db.QueryRow(`SELECT phc FROM users WHERE id = ?`, userID).Scan(&phc); err != nil {
		return fmt.Errorf("doctor: read user: %w", err)
	}
	if ok, err := auth.VerifyPassword(pw, phc); err != nil || !ok {
		return fmt.Errorf("doctor: argon2id verify round-trip failed (ok=%v err=%v)", ok, err)
	}
	if _, err := usersFor(db).Login(context.Background(), userID, pw); err != nil {
		return fmt.Errorf("doctor: login round-trip failed: %w", err)
	}
	const wrongPw = "wrong-guess"
	if _, err := usersFor(db).Login(context.Background(), userID, wrongPw); err == nil {
		return fmt.Errorf("doctor: wrong password accepted")
	}

	// ACL probe: player identity must be denied campaign/ (GM-only).
	ident := pluginapi.Identity{ID: "doctor-player", Role: "player"}
	a := acl.New(db)
	if ok, err := a.Can(context.Background(), ident, "campaign/secrets.md", acl.ModeRead); err != nil || ok {
		return fmt.Errorf("doctor: acl denied campaign read? ok=%v err=%v", ok, err)
	}
	// Vault probe: doctor never writes the real vault. Check runs against
	// a scratch vault (tech-stack: derived/rebuildable self-checks); the
	// configured vaultDir is only existence-verified.
	if fi, err := os.Stat(vaultDir); err != nil || !fi.IsDir() {
		fmt.Printf("doctor: FAIL:vault_dir: %s (%v)\n", vaultDir, err)
	} else {
		fmt.Printf("doctor: ok:vault_dir %s\n", vaultDir)
	}
	scratchVault := filepath.Join(scratch, "vault")
	if err := os.MkdirAll(scratchVault, 0o755); err != nil {
		return fmt.Errorf("doctor: scratch vault: %w", err)
	}
	note := filepath.Join(scratchVault, "doctor-probe.md")
	if err := os.WriteFile(note, []byte("# Doctor\n"), 0o644); err != nil {
		return fmt.Errorf("doctor: seed vault note: %w", err)
	}

	fail := false
	for _, line := range backup.Check(db, scratchVault) {
		fmt.Println("doctor:", line)
		if strings.Contains(line, "FAIL:") {
			fail = true
		}
	}
	if fail {
		return fmt.Errorf("doctor: checks failed (see lines above)")
	}
	// VACUUM INTO smoke via the real backup path: snapshot the scratch db.
	snap := filepath.Join(scratch, "snapshot.db")
	if err := backup.SQLite(filepath.Join(scratch, "doctor.db"), snap); err != nil {
		return fmt.Errorf("doctor: vacuum into: %w", err)
	}
	if fi, err := os.Stat(snap); err != nil || fi.Size() == 0 {
		return fmt.Errorf("doctor: vacuum snapshot missing/empty")
	}
	fmt.Println("doctor: OK (auth, acl, backup self-checks passed)")
	return nil
}

// usersFor adapts the bare db to the auth stream's Users service (the
// doctor is a consumer, not part of the auth wiring).
func usersFor(db *sql.DB) *auth.Users { return auth.NewUsers(db) }

// runReindex rebuilds the derived SQLite index (.semiplane/index.db) from
// the Markdown vault. Losing index.db loses nothing; this command proves it.
func runReindex(vaultDir string) error {
	fi, err := os.Stat(vaultDir)
	if err != nil {
		return fmt.Errorf("reindex: vault %q: %w", vaultDir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("reindex: vault %q is not a directory", vaultDir)
	}
	dbPath := filepath.Join(vaultDir, ".semiplane", "index.db")
	db, err := index.Open(dbPath)
	if err != nil {
		return fmt.Errorf("reindex: open index: %w", err)
	}
	defer func() { _ = db.Close() }()
	ix := index.New(db)
	n, err := ix.Reindex(vaultDir)
	if err != nil {
		return fmt.Errorf("reindex: %w", err)
	}
	fmt.Printf("reindex: indexed %d notes from %s into %s\n", n, vaultDir, dbPath)
	return nil
}
