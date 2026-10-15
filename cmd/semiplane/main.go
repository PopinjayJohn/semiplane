package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/PopinjayJohn/semiplane/internal/app"
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
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Minimal CLI: `semiplane serve` is the default; accept an optional subcommand.
	rest := fs.Args()
	cmd := "serve"
	if len(rest) > 0 {
		cmd = rest[0]
	}

	switch cmd {
	case "serve":
		// No-op for the thin slice.
	case "doctor":
		fmt.Println("semiplane doctor: OK (thin slice)")
		return nil
	case "reindex", "migrate", "backup", "user", "plugin":
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
