package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/semiplane/semiplane/internal/config"
	accountroutes "github.com/semiplane/semiplane/internal/httpapi/accounts"
	assetroutes "github.com/semiplane/semiplane/internal/httpapi/assets"
	campaignroutes "github.com/semiplane/semiplane/internal/httpapi/campaigns"
	editroutes "github.com/semiplane/semiplane/internal/httpapi/edit"
	eventroutes "github.com/semiplane/semiplane/internal/httpapi/events"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	playroutes "github.com/semiplane/semiplane/internal/httpapi/play"
	pluginroutes "github.com/semiplane/semiplane/internal/httpapi/plugins"
	searchroutes "github.com/semiplane/semiplane/internal/httpapi/search"
	wikiroutes "github.com/semiplane/semiplane/internal/httpapi/wiki"
	"github.com/semiplane/semiplane/internal/observability"
	"github.com/semiplane/semiplane/internal/web"
)

// Store is every query the HTTP surface needs, as the union of the three
// subsystems' interfaces.
//
// One parameter rather than three overlapping ones. `accounts.Store`,
// `campaigns.Store` and `identity.Reader` are each satisfied by the same
// `*store.Store`, so passing them separately would mean the composition root
// naming the same handle three times and a caller having to know which of the
// three to reach for. Stating the union here makes that assertion at compile
// time, which is the check that catches a signature change in one subsystem when
// the other two are untouched — and the mistake that produced the bug this
// comment exists beside, where `Authenticate` was never given a reader and every
// request resolved as anonymous.
type Store interface {
	accountroutes.Store
	campaignroutes.Store
	identity.Reader
}

// NewRouter builds the application HTTP handler with all routes registered.
//
// The middleware order is the part of this function that matters, and it is
// one list rather than an assembly at each call site so the reasoning is
// visible where it is decided. Listed outermost first, which is the order
// requests traverse it:
//
//  1. RequestID — first, because every layer below it logs, and a log line
//     without a correlation id cannot be tied back to a request.
//
//  2. Recoverer — second, so it catches a panic in any layer below it, which
//     includes a handler that has already committed a status line. That case is
//     why the response is a log entry plus whatever the server can still write,
//     not simply a dropped connection.
//
//  3. RealIP — before Log, because Log's client_ip attribute has to be the
//     resolved address rather than the proxy's.
//
//  4. Log — times the handler, and sits outside Timeout so it observes the 504
//     rather than the 200 a timed-out handler wrote. A timeout invisible in the
//     access log is the failure this ordering prevents.
//
//  5. Timeout — innermost of the timed layers, so the budget covers the handler
//     and nothing else. Outermost would also time the logging and the panic
//     recovery, and a timeout firing while the error path runs turns a 500 into
//     a truncated response.
//
//  6. securityHeaders — innermost, applied to the mux. It sets headers before
//     delegating, so it has to be inside every layer that can write a response
//     of its own, or a 504 from Timeout would ship without them.
//
//  7. identity.Authenticate — inside the timed layers and above the mux, so the
//     session and user lookups it performs are both covered by the handler budget
//     and logged with the request's correlation id.
//
// registry supplies the §13.2 counters reported on `/readyz`. It is a parameter
// rather than a package-level singleton so a test can build a router with its own
// registry, and so the process has exactly one by construction.
//
// accountRoutes and backing are the two things that turn a mux into the product.
// Both are nil-tolerant: a router built without them serves `/healthz`,
// `/readyz` and the assets, which is what a test exercising only the middleware
// chain wants, and what a router with no database wants. The
// independently-runnable invariant from architecture §15 — the server starts and
// answers /healthz — depends on neither.
//
// The seven campaign-scoped handlers are nil-tolerant for the same reason and one
// more: a route package is wired by the composition root, so a router built
// without one registers the rest and answers 404 for that route. That is a
// *better* failure than the alternative, which is a router that panics on a nil
// handler and takes the process down at boot over a wiring mistake in a subsystem
// that has not shipped yet.
//
// They are six parameters rather than one slice for two reasons, and the second
// is the one that decides it. A slice would need an element type, and a common
// interface over six handlers whose only shared method is `ServeHTTP` would be
// that type — so the list would be `[]http.Handler`, which names nothing and
// makes every mount an unlabelled entry. Six typed parameters say which handler
// is which at every call site, and the types are distinct, so a transposition is a
// compile error rather than a route that serves another route's document.
func NewRouter(
	logger *slog.Logger,
	cfg config.Config,
	registry *observability.Registry,
	accountRoutes *accountroutes.Router,
	backing Store,
	wikiRoute *wikiroutes.Handler,
	assetRoute *assetroutes.Handler,
	searchRoute *searchroutes.Handler,
	editRoute *editroutes.Handler,
	eventsRoute *eventroutes.Handler,
	playRoute *playroutes.Handler,
	pluginRoute *pluginroutes.Handler,
) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.Handle("GET /readyz", newReadyHandler(registry))
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(web.Dist())))
	mux.Handle("/", notFoundHandler(logger))

	if accountRoutes != nil {
		accountRoutes.Mount(mux)
	}

	// Campaign-scoped routes are mounted as a group behind the access gates, so
	// a route added to mountCampaignRoutes inherits the S-8 matrix by being
	// listed there rather than by remembering to wrap itself.
	//
	// The ordering is load-bearing: Resolve outermost, so the RequireRead beneath
	// it reads the tier it is deciding on.
	//
	// The pattern is `/c/{slug}/` and **not** `/c/`, and the difference is a bug
	// that is invisible until a campaign route exists. `net/http` sets a path
	// value from the pattern that matched, and the inner mux does not match
	// until this middleware has already run — so mounted at `/c/`, no wildcard is
	// named at the point `Resolve` reads it, the tier stays TierNone, and
	// RequireRead answers 404 for *every* request under `/c/`, including a page
	// that exists and a reader who is entitled to it.
	//
	// That is not hypothetical: it was the state of this function until the wiki
	// route landed, and the only test covering it passed, because 404-for-
	// everything is indistinguishable from the correct answer while no campaign
	// route is registered. `wikiroutes.TestTheCampaignMountMustCarryTheSlug`
	// asserts both directions and is the guard.
	//
	// Every campaign route is registered on this one mux rather than on the outer
	// one, and that is the whole reason the gates are a single mount list: a route
	// added to `mountCampaignRoutes` is behind `Resolve` and `RequireRead` because
	// it is on the list, and a route added anywhere else is behind nothing.
	campaignMux := http.NewServeMux()
	mountCampaignRoutes(
		campaignMux,
		wikiRoute, assetRoute, searchRoute, editRoute, eventsRoute, playRoute,
		pluginRoute,
	)

	if backing != nil {
		mux.Handle("/c/{slug}/", campaignroutes.Resolve(backing)(
			middleware.Chain(campaignMux, campaignroutes.RequireRead),
		))
	}

	chain := []func(http.Handler) http.Handler{
		securityHeaders,
		middleware.RequestID,
		middleware.Recoverer(logger),
		middleware.RealIP(cfg.TrustedProxies),
		middleware.Log(logger),
		middleware.Timeout(cfg.HandlerTimeout),
	}

	// Authenticate is inside the chain and above the mux, so every route sees an
	// identity — including the campaign gates, which read the requestor to
	// resolve a membership.
	//
	// Its position is not free. It must be outside Timeout's handler budget and
	// inside Log, so a sign-in that does a session lookup and a user lookup is
	// both timed and logged with its correlation id. Outside securityHeaders
	// would be harmless; inside Timeout is the part that matters, because a
	// session lookup on an unresponsive database is exactly the request that
	// should hit the handler budget rather than hold a connection open.
	//
	// It is skipped when there is no store, because with no store there is no
	// session to resolve and every request is anonymous — which is the truth,
	// and is what a read-only instance is.
	if backing != nil {
		chain = append(chain, identity.Authenticate(backing))
	}

	return middleware.Chain(mux, chain...)
}

// mountCampaignRoutes registers the handlers under `/c/{slug}`.
//
// One list, so a route added here is behind the gates and a route added anywhere
// else is not. That is the whole enforcement mechanism (ADR 0024): the failure
// mode of forgetting a gate is a private page answering 200, and the way to make
// that unexpressible is for there to be one place to add a route.
//
// Every handler is nil-tolerant and registers nothing when nil, so a router built
// for a read-only or content-less instance still serves the liveness routes, the
// account surfaces, and the campaign routes that *were* wired. That is what a
// store-less router is for, and it is also what lets a test mount exactly the one
// route it is auditing.
//
// The list's order is documentation rather than behaviour: `net/http`'s mux
// resolves overlapping patterns by specificity, not by registration order, so
// reordering these lines cannot change which handler answers a path. It is ordered
// the way a reader meets the surfaces — read the page, fetch what it names, find
// it again, change it, be told it changed, roll a die, then sit at the table — so
// that the list reads as a product rather than as an alphabet.
//
// Four of the seven mount their own gate, and that is not an inconsistency:
// `edit.Mount`, `events.Mount`, `play.Mount` and `plugins.Mount` wrap themselves in
// `campaigns.RequireEdit`, `RequireEdit`, `RequirePlay` and — for the plugin route —
// `RequirePlay`/`RequireRead` per route, because they are not all readable by the
// same reader. A caller who had to remember the gate would eventually register the
// route without it. The `RequireRead` this function sits under is layered underneath,
// not replaced: a `player` clears it and is refused 403 by the route's own gate, and
// an anonymous reader of a public campaign is challenged 401.
//
// **The plugin route mounts its own two gates and derives its own patterns**, and
// neither is this file's to do: `plugins.Mount` reads the patterns from the UI
// registry (§10.6's "naming a route is a matter of registering it") and wraps the
// roller in `RequirePlay` and the link preview in `RequireRead`. So the only job here
// is to pass the handler through — and listing it is still what makes it behind this
// list's `Resolve` and `RequireRead`, which is the layer underneath.
//
// `/play` is the route where layering two gates matters most, because it is the
// one whose absence of the inner gate is not a defect anybody would notice until
// it is exploited: a socket is a standing capability, so a tabletop reachable
// without `RequirePlay` is a campaign whose live state anybody who can reach the
// port can read and write. `play.Mount` mounts it, so this list's job is only to
// pass the handler through.
//
// The patterns carry the `/c/` prefix even though this mux is itself mounted
// there. A Go 1.22 mux routes on a prefix and then hands the *whole* path to
// whatever matched, so a pattern written without the prefix would never fire.
func mountCampaignRoutes(
	mux *http.ServeMux,
	wikiRoute *wikiroutes.Handler,
	assetRoute *assetroutes.Handler,
	searchRoute *searchroutes.Handler,
	editRoute *editroutes.Handler,
	eventsRoute *eventroutes.Handler,
	playRoute *playroutes.Handler,
	pluginRoute *pluginroutes.Handler,
) {
	if wikiRoute != nil {
		wikiroutes.Mount(mux, wikiRoute)
	}

	if assetRoute != nil {
		assetroutes.Mount(mux, assetRoute)
	}

	if searchRoute != nil {
		searchroutes.Mount(mux, searchRoute)
	}

	if editRoute != nil {
		editroutes.Mount(mux, editRoute)
	}

	if eventsRoute != nil {
		eventroutes.Mount(mux, eventsRoute)
	}

	if playRoute != nil {
		playroutes.Mount(mux, playRoute)
	}

	if pluginRoute != nil {
		pluginroutes.Mount(mux, pluginRoute)
	}
}
