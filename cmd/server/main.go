// Command semiplane runs the server and the administrative subcommands.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/httpapi"
	"github.com/semiplane/semiplane/internal/observability"
	"github.com/semiplane/semiplane/internal/store"
)

// usageText is written on a usage error and on an explicit `help`. Kept in one
// place so a new subcommand cannot be added without updating what the operator
// is told exists.
//
// `admin create` is deliberately absent. The plan puts it in this phase, but it
// cannot be built here: it needs the `users` table (phase 2's I2) and a
// password-hashing decision that is a new dependency (phase 2's I3). Shipping a
// command that parses a password and then cannot store it is worse than not
// shipping it, so it lands with the schema that backs it.
const usageText = `semiplane — self-hosted TTRPG wiki and virtual tabletop

Usage:
  semiplane                     run the server (the default)
  semiplane serve               run the server
  semiplane help                show this message

Configuration is documented at https://popinjayjohn.github.io/semiplane/install/.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		// A usage error is the operator's typo, not a server fault, so it goes
		// to stderr plainly and exits 2. Everything else is a slog error: those
		// are the lines a log aggregator is watching.
		if usageErr, ok := errors.AsType[*usageError](err); ok {
			fmt.Fprintf(os.Stderr, "%s\n\n%s", usageErr, usageText)
			os.Exit(2)
		}

		slog.Error("fatal", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// usageError marks an error caused by the command line rather than by the
// system. It exists so main can choose the exit code and the presentation
// without string-matching a message.
type usageError struct {
	msg string
}

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// run dispatches a subcommand. `serve` is the default, so `semiplane` and
// `semiplane serve` are the same thing — a container entrypoint should not have
// to name a subcommand to start the product.
func run(args []string) error {
	subcommand, rest := splitSubcommand(args)

	switch subcommand {
	case "", "serve":
		return runServer(rest)
	case "admin":
		return runAdmin(rest)
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usageText)

		return nil
	default:
		return usagef("unknown command %q", subcommand)
	}
}

// splitSubcommand peels the subcommand off the argument list. A leading flag
// belongs to the default subcommand rather than to the program, which is what
// lets `semiplane --addr :9000` work without naming a subcommand at all.
func splitSubcommand(args []string) (string, []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", args
	}

	return args[0], args[1:]
}

// runAdmin dispatches an admin subcommand. Nothing is registered yet — see
// usageText for why — but the dispatch shape is here so the first subcommand is
// a case rather than a restructure, and so `semiplane admin` fails with a usage
// error rather than silently starting a server.
func runAdmin(args []string) error {
	if len(args) == 0 {
		return usagef("admin has no subcommands yet")
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usageText)

		return nil
	default:
		return usagef("unknown admin subcommand %q", args[0])
	}
}

func runServer(_ []string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	// The composition root. Everything is constructed here, in dependency
	// order, and passed down explicitly: no package-level state, no init()
	// registration, no lookup of a global. A test can therefore build the same
	// wiring the binary does, and a phase that adds a subsystem adds it to this
	// function rather than to a hidden initialiser somewhere.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		// A failed close leaves a WAL that the next start has to recover, which
		// it can, so this is logged rather than returned: the server is already
		// shutting down and there is nobody left to tell.
		if err := db.Close(); err != nil {
			logger.Error("close store", slog.String("error", err.Error()))
		}
	}()

	// The process's one counter registry. Nothing in phase 1 increments
	// anything yet — the watcher, cache, secrets, hub and plugin phases each
	// register their own — so every §13.2 signal currently reads zero on
	// /readyz, which is how a subsystem that never wired itself up becomes
	// visible rather than invisible.
	registry := observability.NewRegistry()

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.NewRouter(logger, cfg, registry),
		ReadHeaderTimeout: cfg.ReadTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	if err := serve(ctx, server, logger, cfg.ShutdownTimeout); err != nil {
		return err
	}

	logger.Info("http server stopped cleanly")

	return nil
}

// serve runs the HTTP server until it fails or the context is cancelled, then
// drains in-flight requests within the shutdown budget.
func serve(
	ctx context.Context,
	server *http.Server,
	logger *slog.Logger,
	shutdownTimeout time.Duration,
) error {
	errCh := make(chan error, 1)

	go func() {
		logger.Info("http server listening", slog.String("addr", server.Addr))

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}

		close(errCh)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
	case <-ctx.Done():
	}

	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	return nil
}
