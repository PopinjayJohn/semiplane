package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/PopinjayJohn/semiplane/internal/app"
	"github.com/PopinjayJohn/semiplane/internal/index"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "semiplane:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("semiplane", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "listen address")
	vault := fs.String("vault", ".", "vault directory")
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
		fmt.Println("semiplane doctor: OK (thin slice)")
		return nil
	case "reindex":
		return runReindex(*vault)
	case "migrate", "backup", "user", "plugin":
		return fmt.Errorf("%s: not implemented in thin slice", cmd)
	default:
		return fmt.Errorf("unknown command %q (try: serve, doctor)", cmd)
	}

	cfg := app.Config{Addr: *addr, VaultDir: *vault}
	srv := app.NewServer(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("semiplane serving", "addr", cfg.Addr, "vault", cfg.VaultDir)
	return srv.Run(ctx)
}

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
