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

	"github.com/semiplane/semiplane/internal/campaignroots"
	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/httpapi"
	"github.com/semiplane/semiplane/internal/httpapi/accounts"
	"github.com/semiplane/semiplane/internal/httpapi/wiki"
	"github.com/semiplane/semiplane/internal/observability"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

// usageText is written on a usage error and on an explicit `help`. Kept in one
// place so a new subcommand cannot be added without updating what the operator
// is told exists.
//
// The subcommand and flag names, as constants because each appears in every
// dispatch that accepts it. A literal repeated across three switches is a
// literal that eventually gets a typo in one of them, and a typo in a subcommand
// name is a command that silently does not exist.
const (
	subcommandServe = "serve"
	subcommandAdmin = "admin"
	subcommandHelp  = "help"

	flagHelpShort = "-h"
	flagHelpLong  = "--help"

	adminSubcommandCreate   = "create"
	adminSubcommandCampaign = "campaign"
)

// The admin subcommands are listed rather than detailed; `semiplane admin help`
// carries their flags. A subcommand is named here or nowhere, so an operator who
// runs `semiplane admin` is never told it does not exist.
const usageText = `semiplane — self-hosted TTRPG wiki and virtual tabletop

Usage:
  semiplane                     run the server (the default)
  semiplane serve               run the server
  semiplane admin create        add an account
  semiplane admin campaign add  register a campaign
  semiplane help                show this message

Run "semiplane admin help" for the admin subcommands' flags.

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
	case "", subcommandServe:
		return runServer(rest)
	case subcommandAdmin:
		return runAdmin(rest)
	case subcommandHelp, flagHelpShort, flagHelpLong:
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

func runServer(_ []string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	// Cancelled on SIGINT or SIGTERM, which is what starts the graceful drain
	// below. Created before the store is opened so a signal arriving during
	// startup reaches a context the open is already watching.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The composition root. Everything is constructed here, in dependency
	// order, and passed down explicitly: no package-level state, no init()
	// registration, no lookup of a global. A test can therefore build the same
	// wiring the binary does, and a phase that adds a subsystem adds it to this
	// function rather than to a hidden initialiser somewhere.
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

	// The process's one counter registry. The watcher, cache, secrets, hub and
	// plugin phases each register their own, so a §13.2 signal still reads zero
	// on /readyz until then — which is how a subsystem that never wired itself up
	// becomes visible rather than invisible.
	registry := observability.NewRegistry()

	// The account routes: sign in, sign out, and the campaign list. Constructed
	// here rather than in the router because it is where the store meets the
	// components, and both are already in hand.
	accountRoutes := &accounts.Router{
		Store:  db,
		Logger: logger,
		Secure: cfg.IsProduction(),
	}

	// One handle, satisfying the union the HTTP surface needs. Stated as the
	// interface type rather than passed as *store.Store so the wiring asserts at
	// compile time that the store still satisfies the account routes, the access
	// gates and the session resolver — which is the check that catches a
	// signature change in one of the three when the other two are untouched.
	var httpStore httpapi.Store = db

	// The wiki read path. `contentRoots` holds one `os.Root` per campaign, which
	// is the confinement boundary for every path the route reads — the route
	// takes a `Get(slug)` and cannot be handed a path at all.
	//
	// A campaign whose row exists but whose vault is not mounted is *degraded*,
	// not fatal (S-4.5): the other campaigns keep serving and the instance stays
	// up. One unmounted disk must not take the whole thing down.
	contentRoots, degraded, rootsErr := campaignroots.Open(ctx, db, logger)
	if rootsErr != nil {
		return fmt.Errorf("open campaign content roots: %w", rootsErr)
	}

	// Indexed: the entry is 128 bytes and this runs on every boot.
	for i := range degraded {
		entry := &degraded[i]

		// Error, not warn. S-4.5 is explicit that a campaign that cannot be
		// watched or read is a fault an operator must see, and a warn line on a
		// boot with an unmounted vault is a line nobody reads.
		logger.Error("campaign.content_root_unusable",
			slog.String("slug", entry.Slug),
			slog.String("path", entry.Path),
			slog.String("error", entry.Err.Error()),
		)
	}

	if len(degraded) > 0 {
		logger.Warn("instance degraded: some campaigns have no readable content",
			slog.Int("degraded_campaigns", len(degraded)),
			slog.Any("slugs", degraded.Slugs()),
		)
	}

	// One renderer per campaign, not per request: a `*content.Renderer` holds a
	// built goldmark pipeline and an immutable sanitiser policy, and both are safe
	// for concurrent use, so this is one allocation per campaign rather than one
	// per request. Built here from the campaigns the store just listed, because a
	// renderer for a campaign with no root would be a renderer nothing can reach.
	renderers := make(wiki.CampaignRenderers, len(degraded))

	registered := mustListCampaigns(ctx, db)

	// Indexed: domain.Campaign is 128 bytes, and a renderer map keyed by slug
	// wants the slug, not the row.
	for i := range registered {
		if _, openErr := contentRoots.Get(registered[i].Slug); openErr != nil {
			continue
		}

		renderers[registered[i].Slug] = content.NewRenderer(registered[i].Slug, pageKinds{})
	}

	wikiRoute := &wiki.Handler{
		Roots:     contentRoots,
		Renderers: renderers,
		Kinds:     pageKinds{},
		Pages:     pageLister{roots: contentRoots, db: db},
		// P10 replaces this. Until then `[!secret]` content is **not** redacted,
		// and this is the one place on the request path that fact is written down.
		Redactor:    content.NoSecrets(),
		Cache:       content.NewCache(renderCacheEntries),
		Logger:      logger,
		Instance:    components.InstanceView{},
		SignOutHref: "/logout",
	}

	server := &http.Server{
		Addr: cfg.Addr,
		Handler: httpapi.NewRouter(
			logger,
			cfg,
			registry,
			accountRoutes,
			httpStore,
			wikiRoute,
		),
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
