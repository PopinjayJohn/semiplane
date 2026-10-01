package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/semiplane/semiplane/internal/config"
	accountroutes "github.com/semiplane/semiplane/internal/httpapi/accounts"
	campaignroutes "github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
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
func NewRouter(
	logger *slog.Logger,
	cfg config.Config,
	registry *observability.Registry,
	accountRoutes *accountroutes.Router,
	backing Store,
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
	// No route is mounted yet — the wiki surface is P3, the write path P6 — but
	// the chain is wired now so that the first one is a one-line addition and a
	// reviewer can see the ordering that matters: Resolve outermost, so the
	// RequireRead beneath it can read the tier it is deciding on.
	campaignMux := http.NewServeMux()
	mountCampaignRoutes(campaignMux)

	if backing != nil {
		mux.Handle("/c/", campaignroutes.Resolve(backing)(
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
// Empty in this phase, and the emptiness is deliberate rather than a placeholder
// left unfilled: this is the list P3 and P6 extend, and a route mounted outside
// it would bypass the gates entirely. A request under /c/ matching nothing
// falls through to the mux's own 404, whose body is identical to the answer for
// a campaign that does not exist — which is the property the S-8 matrix needs and
// the reason nothing is mounted ahead of its gate.
func mountCampaignRoutes(_ *http.ServeMux) {}
