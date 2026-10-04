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
	"github.com/semiplane/semiplane/internal/httpapi/events"
	"github.com/semiplane/semiplane/internal/httpapi/wiki"
	"github.com/semiplane/semiplane/internal/observability"
	"github.com/semiplane/semiplane/internal/store"
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
	subcommandDemo  = "demo"
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
  semiplane demo seed           populate this instance from the demo vault
  semiplane demo reset          remove the demo campaigns and their state
  semiplane help                show this message

Run "semiplane admin help" for the admin subcommands' flags, and
"semiplane demo help" for the demo subcommands'.

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
	case subcommandDemo:
		return runDemo(rest)
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
		// Named `closeErr` rather than `err`: the pipeline wiring below declares
		// `err` in this same scope, and a deferred close that shadows it reads as a
		// statement about what this function returns, which it does not.
		if closeErr := db.Close(); closeErr != nil {
			logger.Error("close store", slog.String("error", closeErr.Error()))
		}
	}()

	// The process's one counter registry. The watcher, cache, secrets, hub and
	// plugin phases each register their own, so a §13.2 signal still reads zero
	// on /readyz until then — which is how a subsystem that never wired itself up
	// becomes visible rather than invisible.
	registry := observability.NewRegistry()

	// The event hub, and it is built **here**, before anything that could publish to
	// it and long before anything that could subscribe.
	//
	// Two dependencies point at it — the content pipeline, as a `ChangeSink` beside
	// the indexer, and the event route, as its broker — and it is the process's only
	// broker between a file changing on disk and a GM's browser. Constructing it
	// early is what lets the pipeline take `hub.Sink()` as an argument instead of
	// the route reaching back for a global, and it is why the shutdown order below
	// is a statement about this value rather than about a package.
	hub := events.NewHub()

	// The account routes: sign in, sign out, and the campaign list. Constructed
	// here rather than in the router because it is where the store meets the
	// components, and both are already in hand.
	accountRoutes := &accounts.Router{
		Store:  db,
		Logger: logger,
		Secure: cfg.IsProduction(),
		// Set below, once the content roots are open: the instance view carries
		// which campaigns are degraded, and at this point nothing is known. A
		// zero value here would report every campaign healthy.
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

	// Closed before the store and after the pipeline. Deferred here, at
	// construction, because the deferred calls below run last-in-first-out: the
	// pipeline registers its close *after* this one, so the pipeline stops first,
	// and this runs before the store's. An `os.Root` handle closed while the
	// settle filter still stats through it is a failure manufactured by this
	// function rather than by anything on the host.
	defer func() {
		if closeErr := contentRoots.Close(); closeErr != nil {
			logger.Error("close campaign content roots", slog.String("error", closeErr.Error()))
		}
	}()

	registered := mustListCampaigns(ctx, db)

	// # The plugins, built first of everything that resolves anything
	//
	// `systems.go` holds the registrations and this is the one line that runs them.
	// It is here rather than lower down for two reasons, and the second is why it
	// cannot move:
	//
	//  - The realtime plane's resolver is `plugin.Resolver` over this gameplay
	//    registry, and the gate's expected fingerprint is the engine's own four
	//    components. Both are below.
	//  - **The content pipeline's page-kind registry is this gameplay registry too**
	//    (§10.7: `kind` is registry-backed, so a page's `ancestry` is a game object
	//    in a build shipping 5e and prose in one that is not). The pipeline is
	//    constructed further down with `pageKinds`, so registering after it would
	//    mean the wiki renders every plugin kind as prose in a build that knows it —
	//    a product that is quietly right about nothing.
	//
	// A refusal stops the boot, and `systems.go` says why that is the right
	// direction: every one of them is about a compiled-in data pack or about this
	// composition root, never about anything an operator did to their instance.
	// Named `built` rather than `plugins`, because `plugins` is the package-level
	// type this function's own signature space uses and a local of that name hides it
	// for the rest of the function. It read as harmless until `runDemo` needed the
	// type and `govet` pointed at the shadowing.
	built, err := registerPlugins(logger)
	if err != nil {
		return fmt.Errorf("register the plugins: %w", err)
	}

	// # The realtime plane, built here and before any route that needs it
	//
	// Four values with a strict dependency flow — registry, then gate, then
	// resolver, then hub — and `realtime.go` states each step's reason. Two things
	// about *where* it is built are worth saying here rather than there:
	//
	//  - **Before the campaign routes**, because `playRoute` is one of them and
	//    because the boot pass over the fingerprints needs the campaign list,
	//    which `registered` is.
	//  - **After the store and the counter registry**, because its three closures
	//    close over the store handle and its `WriteRecorder` is an
	//    `observability.Writes` registered into the same registry as the
	//    pipeline's surfaces. Both of those already exist at this point and
	//    neither exists later.
	plane := newRealtimePlane(ctx, db, registry, built, logger)

	// The page-kind registry every renderer and the editor share, held once and
	// handed down. **One value rather than three lookups** because `content.NewRenderer`
	// and `wiki.Handler` each keep it and a second registry would be a second answer
	// to "which kinds does this build know" — and the failure mode is the bad one: a
	// page that renders as a game object on the wiki and as prose in the editor, with
	// nothing in either log to say which side is wrong.
	kinds := built.pageKinds()

	// Every campaign's fingerprint is checked against what this build resolves
	// under, and nothing is opened. The boot pass is read-only by construction
	// (`Gate.Inspect`), and it is here for the reason `realtime.go` states at
	// length: an incompatible game is an operator's log line at startup rather
	// than a GM's failed join in the middle of a session. It runs before the
	// server listens, alongside `pipeline.buildIndex`, and for the same
	// underlying reason — a subsystem that is not asked a question at boot
	// answers it on a user's first request.
	resumeCampaignStates(ctx, plane, registered, logger)

	// The same argument for the same reason, over the other half of §10.8: a campaign
	// whose `system_id` names no registered system is refused at every intent by the
	// resolver, and a per-intent refusal is seen by a client rather than by an operator.
	// This pass is what puts "campaign X names system Y, which this build does not
	// resolve" in a log before the server listens.
	reportMissingSystems(built.gameplay, registered, logger)

	// The shutdown step for the realtime plane. **Not** a `defer`: it has to run
	// in the same `beforeDrain` step as the event hub and before the HTTP drain,
	// and a `defer` in this function runs *after* `serve` returns — which is after
	// the drain. A live WebSocket never returns on its own, so closing the realtime
	// hub after the drain is a drain that blocks for the whole shutdown budget and
	// then returns `context deadline exceeded`, on every shutdown, for a reason that
	// reads as a server fault rather than as a long-lived response. Phase 6 measured
	// a clean exit in 110ms with a live stream; that measurement is this ordering.
	//
	// The closure rather than a bare reference because the step has no error
	// channel: `Hub.Close` and `Registry.Close` both return one, and the process is
	// on its way out with nobody to return to, so `closeRealtimePlane` logs them.
	closeRealtime := func() {
		closeRealtimePlane(ctx, plane, logger, realtimeFlushBudget)
	}

	// The §13.2 surfaces of the content pipeline: the watcher subsystem's counters
	// and the indexer's four, registered into the process's registry so `/readyz`
	// renders them as zeros before anything fails. Constructed once and shared by
	// the watcher, the settle filter and the indexer, because the gauges on
	// `observability.Watch` live in the value rather than in the counter.
	signals := newContentSignals(registry, logger)

	campaignIDs := campaignIDBySlug(registered)

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

		// The gauge as well as the line. S-4.5 says a missing content root marks
		// the campaign degraded, and `watch.degraded`'s `current` is a count of the
		// campaigns in that state — so `/readyz` answers "how many campaigns are
		// degraded right now" for a reader who never opens a log.
		//
		// Nothing clears it in this phase. There is no re-verify loop yet, so a
		// vault that comes back stays degraded until the process restarts; that is
		// a limitation of the surface rather than a claim that the vault is
		// broken, and it fails toward reporting a problem rather than hiding one.
		if id, known := campaignIDs[entry.Slug]; known {
			signals.watch.Degraded(ctx, campaignSignalID(id), "content_root_missing")
		}
	}

	// One instance view, computed once and shared by every document this process
	// renders. Assigned here rather than at each construction because the account
	// routes are built before any content root exists, and a degraded campaign is
	// only knowable once they have all been tried — and because five handlers each
	// building their own would be five chances for one of them to end up with a
	// zero value, which renders an empty name, an empty version, and (worst of all)
	// a *healthy* instance: an empty `Degraded` slice means healthy by
	// construction, so a campaign whose content root vanished would be computed as
	// degraded by the pipeline and then rendered as fine by the interface. Both
	// halves reporting success is the worst shape that bug can take.
	instance := instanceView(cfg, degraded)
	accountRoutes.Instance = instance

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
	//
	// This map is the process's **only** renderer set. The wiki route and the
	// editor are handed views of *this* map, not copies of it — see
	// `editorRenderers` — so "which campaigns have a renderer" has one answer in
	// this binary rather than one per route, and a campaign cannot render on one
	// surface and report a load error on another.
	renderers := make(wiki.CampaignRenderers, len(registered))

	// Indexed: domain.Campaign is 128 bytes, and a renderer map keyed by slug
	// wants the slug, not the row.
	for i := range registered {
		if _, openErr := contentRoots.Get(registered[i].Slug); openErr != nil {
			continue
		}

		renderers[registered[i].Slug] = content.NewRenderer(registered[i].Slug, kinds)
	}

	// The content pipeline: one watcher, one settle filter, one indexer over every
	// campaign whose content root opened. `pipeline.go` states the graph; this is
	// the wiring, and it is here rather than in a package so that the order the
	// three are constructed and stopped in is visible in one place.
	//
	// `hub.Sink()` goes in beside the indexer rather than beside the watcher,
	// which is the placement that makes the notice true: a sink on the watcher
	// would fire on the raw filesystem event, and a GM would be told their page
	// changed while the bytes were still moving. Off the settle filter, a notice
	// means the file stopped changing.
	pipeline, err := newContentPipeline(
		ctx,
		contentRoots,
		registered,
		db,
		kinds,
		signals,
		hub.Sink(),
		// Reconciliation is wired here rather than inside `newContentPipeline`
		// because it needs the concrete `*store.Store` and the root registry, and
		// the pipeline's own signature deliberately takes the narrow
		// `content.PageStore`. A pipeline that built it would have to widen that
		// parameter to admit SQL it does not otherwise use.
		//
		// It is the **last** sink and the ordering is load-bearing: a successful pass
		// rewrites one byte, which settles as a change of its own and arrives back
		// here. Convergence is on the state being fixed, not on a flag, so the second
		// visit finds nothing pending and writes nothing.
		secretReconciler(contentRoots, reconcileLedger{store: db}, logger),
	)
	if err != nil {
		return fmt.Errorf("wire the content pipeline: %w", err)
	}

	// Deferred after the store's and the roots' own closes, so Go's
	// last-in-first-out deferral runs it first: the pipeline stops, then the roots
	// are closed, then the store. That order is load-bearing rather than incidental
	// — see `contentPipeline.Close`.
	defer func() {
		// Logged rather than returned, for the reason the store's close is: the
		// process is on its way out and there is nobody left to tell. A watcher
		// that would not close leaks an inotify descriptor into a process that is
		// exiting, so the cost is a line in the log.
		if closeErr := pipeline.Close(); closeErr != nil {
			logger.Error("close content pipeline", slog.String("error", closeErr.Error()))
		}
	}()

	// The startup index, before the server listens and therefore before any request
	// can ask the wiki route which pages a campaign contains. An empty `pages` table
	// resolves no reference at all, so a wiki served against one marks every
	// `[[wikilink]]` broken — the symptom of a link-resolution bug, caused by a
	// missing boot step. `pageLister` walked the content root until this ran.
	pipeline.buildIndex(ctx, logger)

	// The wiki read path. Built last, over a table that is already populated, and
	// through the same constructor the tests use so that a test asserting a page
	// resolves is asserting it about this handler.
	wikiRoute := newWikiRoute(
		contentRoots,
		renderers,
		kinds,
		pageLister{db: db},
		instance,
		logger,
	)

	// The other three campaign-scoped routes, over the same four dependencies the
	// wiki route just got — the same confined roots, the same renderers, the same
	// store, the same instance view — because they are four views of one campaign's
	// content rather than four subsystems.
	//
	// Constructed here and passed to `NewRouter` rather than assembled inside the
	// router, for the reason the rest of this function is the composition root at
	// all: this is the one place that knows a `*store.Store` is what satisfies both
	// `search.Pages` and the writer queue behind `edit.Revisions`, and putting the
	// conversions in the router would mean the router knew about the store's
	// internals.
	assetRoute := newAssetRoute(contentRoots, instance, logger)
	searchRoute := newSearchRoute(db, instance, logger)
	editRoute := newEditRoute(
		contentRoots,
		db,
		editorRenderers(renderers),
		kinds,
		instance,
		logger,
	)
	eventRoute := newEventRoute(hub, logger)
	playRoute := newPlayRoute(plane.hub, logger)
	pluginRoute := newPluginRoute(db, built, plane.hub, logger)
	themeRoute := newThemeRoute(contentRoots, logger)
	// `db`, not `httpStore`. `httpStore` is the union the HTTP surface needs and its
	// point is to assert that `db` satisfies three interfaces at compile time; the
	// reveal handler's `secrets.Ledger` is a fourth, and passing the concrete handle
	// is what makes that assertion happen at this call rather than nowhere.
	secretRoute := newSecretRoute(contentRoots, db, logger)
	// Set here rather than in the literal above, for the reason the literal's own
	// comment gives: the account routes are built before the content roots are
	// open, and the theme handler cannot exist without them. §4.12.3's GM notice
	// reaches the campaign overview through this field, so leaving it nil would
	// not fail anything -- it would simply never show a GM the brand pair their
	// campaign is failing.
	accountRoutes.Theme = themeNotices(themeRoute)

	server := &http.Server{
		Addr: cfg.Addr,
		Handler: httpapi.NewRouter(
			logger,
			cfg,
			registry,
			accountRoutes,
			httpStore,
			wikiRoute,
			assetRoute,
			searchRoute,
			editRoute,
			eventRoute,
			playRoute,
			pluginRoute,
			themeRoute,
			secretRoute,
		),
		ReadHeaderTimeout: cfg.ReadTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	// # The shutdown order, and why each step is where it is
	//
	// Three things have to be stopped, and the order is the dependency rather than
	// the tidiness:
	//
	//  1. **The event hub, before the HTTP server drains.** `serve` closes it, and
	//     it is closed there rather than by a defer here because
	//     `http.Server.Shutdown` waits for every in-flight request to return — and an
	//     event stream never returns on its own. `events.stream` ends on the hub's
	//     close or on a failed write, and deliberately not on the request context
	//     being cancelled, because a stream that ended at the handler budget would be
	//     a reconnect loop. So a hub closed *after* the drain is a drain that blocks
	//     for the whole shutdown budget and then returns
	//     `context deadline exceeded`, on every shutdown, for a reason that reads as
	//     a server fault rather than as a long-lived response. Closing it first turns
	//     the same shutdown into: streams end, the last frames are written, the
	//     handlers return, the drain completes inside the budget.
	//  2. **The content pipeline, before the roots and before the store.** Already
	//     true by Go's last-in-first-out deferral — the pipeline registers its close
	//     after both of theirs, so it runs first — and the reason is that the
	//     pipeline is the only thing still *writing*: its watcher and its settle
	//     filter stat through `contentRoots` and its indexer writes to `db`. Closing
	//     either of those first is a failure manufactured by this function rather
	//     than by anything on the host. `contentPipeline.Close` states the order
	//     *within* the pipeline, which is the same argument one level down.
	//  3. **The hub is closed before the pipeline, and that is also deliberate.** The
	//     pipeline is the hub's only publisher, so after step 1 every `Publish` is a
	//     no-op — which is correct, because there is no longer a reader to tell. The
	//     reverse order would also be safe, so this is stated rather than claimed as
	//     load-bearing: it is a consequence of doing 1 first, not a separate rule.
	//
	// The store is last of all, and that is not a close but a commit point: every
	// writer in the process is a writer *queue* over this handle, and a queue still
	// holding a request has to be drained before the handle under it goes away.
	// `store.Close` is what does the draining.
	if err := serve(
		ctx,
		server,
		logger,
		cfg.ShutdownTimeout,
		beforeDrain(closeEventHub(hub, logger), closeRealtime),
	); err != nil {
		return err
	}

	logger.Info("http server stopped cleanly")

	return nil
}

// closeEventHub returns the shutdown step that ends every open event stream.
//
// A function rather than `hub.Close` itself for one reason: `Hub.Close` returns an
// error, and the shutdown step this is used as cannot — it runs while the process
// is on its way out, after the drain has not yet happened, and there is nowhere to
// return an error to. A `Hub.Close` that failed would leave streams open, and the
// honest thing to do about that is record it and carry on, exactly as the store's
// close does for the same reason.
//
// Idempotent by construction (`Hub.Close` is), which matters because this is a
// value a caller might reasonably also close on a panic path.
func closeEventHub(hub *events.Hub, logger *slog.Logger) func() {
	return func() {
		if err := hub.Close(); err != nil {
			logger.Error("close the event hub", slog.String("error", err.Error()))

			return
		}

		stats := hub.Stats()

		logger.Info("event hub closed",
			slog.Int64("published", stats.Published),
			slog.Int64("delivered", stats.Delivered),
			slog.Int64("dropped", stats.Dropped),
			slog.Int("open_subscriptions", stats.Subscribers),
		)
	}
}

// beforeDrain composes the steps that must run before `http.Server.Shutdown`.
//
// Left to right, and the order is the dependency rather than the tidiness:
//
//  1. **The event hub.** `events.stream` ends on the hub's close or on a failed
//     write and deliberately not on the request context being cancelled, so the
//     streams have to be ended by closing the broker that owns them.
//  2. **The realtime plane.** `Hub.Close` ends every peer, which ends every
//     `/play` read loop, and then flushes the state registry. A table is the
//     second long-lived response this process has, and it has the same property:
//     the loop does not return on its own, so closing after the drain burns the
//     whole budget.
//
// Both before the drain because the drain is what waits for in-flight requests and
// neither request type ends by itself. Both idempotent, because `serve` calls the
// step on the error path too — a `ListenAndServe` that failed outright has no
// in-flight requests to end and closing both is harmless.
//
// A variadic rather than a slice of `func()` because the composition root names
// the steps as arguments and this function is the only place their order is
// decided. `sync.Once` is not needed and not used: each step's contract already
// says it is safe to call twice, and a wrapper that hid a second call would be a
// second thing to reason about at the one place a shutdown goes wrong.
func beforeDrain(steps ...func()) func() {
	return func() {
		for _, step := range steps {
			step()
		}
	}
}

// serve runs the HTTP server until it fails or the context is cancelled, then
// ends the long-lived responses and drains in-flight requests within the shutdown
// budget.
//
// # Why there is a `beforeDrain` step
//
// `http.Server.Shutdown` closes the listeners and then **waits for every in-flight
// request to return**, bounded by the context it is given. A request that does not
// return on its own therefore consumes the entire budget and turns every shutdown
// into a failure. This project has one such request: `GET /c/{slug}/events`, the
// event stream, whose loop ends on the hub's close or on a failed write and
// deliberately *not* on the request context's cancellation — selecting on that
// would cut every stream at the handler budget and leave the client reconnecting
// forever, which is a reconnect loop rather than a stream.
//
// So `beforeDrain` runs after the listener has stopped accepting and before
// `Shutdown` is called, and the composition root uses it to close the event hub.
// The order inside is therefore: stop accepting → end the streams → drain the
// rest. Doing it the other way round is a shutdown that always times out.
//
// The alternative — clearing the request context, or giving the handler a way to
// learn the server is going away — was rejected because it hands every long-lived
// handler a new obligation, and this process has exactly one of them. A named step
// is one line at the call site and one contract at the definition.
//
// Called for the error path too, not only for the signal: if `ListenAndServe`
// fails outright there are no in-flight requests to end, and closing the hub is
// idempotent, so the same call is correct on both exits.
func serve(
	ctx context.Context,
	server *http.Server,
	logger *slog.Logger,
	shutdownTimeout time.Duration,
	beforeDrain func(),
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
			beforeDrain()

			return fmt.Errorf("listen: %w", err)
		}
	case <-ctx.Done():
	}

	logger.Info("shutdown signal received")

	// Before the drain, and before the shutdown context exists, because a step that
	// could block has no business holding a deadline that is about to expire.
	// `events.Hub.Close` cannot block — it takes a mutex and closes channels — so
	// this is immediate in practice; the placement is what makes it *safe* rather
	// than what makes it fast.
	beforeDrain()

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	return nil
}
