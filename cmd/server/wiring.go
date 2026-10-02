package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/semiplane/semiplane/internal/campaignroots"
	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/assets"
	"github.com/semiplane/semiplane/internal/httpapi/edit"
	"github.com/semiplane/semiplane/internal/httpapi/events"
	"github.com/semiplane/semiplane/internal/httpapi/search"
	"github.com/semiplane/semiplane/internal/httpapi/wiki"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

// signOutHref is where every campaign-scoped document's sign-out form posts.
//
// One constant rather than a literal per handler, because the four routes that
// render a shell all have to name the same URL: a header with two different
// sign-out destinations is a header that signs one account out and leaves the
// other signed in, and the only way to find that is to click both. The account
// routes own the other end (`accounts.Router.Mount` registers `/logout`), and this
// is the string that has to agree with it.
//
// `""` renders no form, so a test that does not care about the account zone does
// not have to set it — which is why the constructors above take the instance view
// and the href from `runServer` rather than looking either up.
const signOutHref = "/logout"

// The composition root's smaller wirings. Each is here rather than inline in
// main because each answers a question a reader of main should not have to hold
// in their head, and because a type with a test is a type whose behaviour is
// stated rather than implied.

// renderCacheEntries bounds the render cache.
//
// 512 rendered pages. The reasoning is in `internal/content/cache.go` — eviction
// is random rather than LRU, precisely because a bound this size is never reached
// in a real vault and a real vault should not pay LRU bookkeeping to prove it. A
// campaign with more distinct (page, content hash, variant) triples than this
// degrades to rendering more often, which costs CPU and is correct; the bound
// exists so that a pathological vault cannot make the process's memory grow
// without limit.
const renderCacheEntries = 512

// pageKinds is the page-kind registry `content.Parse` discriminates against.
//
// Empty, and deliberately so: the plugin registry is P8, and a page whose `kind`
// names something this build does not know degrades to prose (S-3.3), which is
// exactly the behaviour an empty registry produces. Wiring a hand-written list of
// semiplane's own five kinds here would mean two vocabularies — this one and the
// registry's — that agree until P8 disagrees with this one, and a page that
// renders as a token in development and as prose in production is the kind of
// defect that is only ever found by a user.
//
// `nil` would behave identically today, and an explicit empty type is better: it
// names the decision rather than leaving it to the reader's inference about what
// nil does.
type pageKinds map[string]struct{}

// HasPageKind reports whether a kind is registered. Always false until P8.
func (pageKinds) HasPageKind(string) bool { return false }

// pageLister answers the wiki route's one query on a cache miss: which pages does
// this campaign contain.
//
// The `pages` table, and no longer a walk of the content root. Phase 4 made the
// table maintained — `content.Indexer` writes it from settled changes, and the
// composition root runs `ReindexCampaign` for every campaign before the server
// listens — so reading it is both cheaper than the walk it replaces and more
// correct: a walk read every page's bytes on every cache miss, and this is a range
// scan of one campaign's rows.
//
// The direction is the one S-3.1 points anyway. The filesystem is the source of
// truth and this is a rebuildable index of it, so the question a reader of this
// comment should be asking is not "why is the table authoritative" — it never is —
// but "what keeps the table equal to the tree", and the answer is the watcher plus
// the startup index that has to run before the first request. `TestTheIndexIsBuiltBeforeTheRouterServes`
// is that claim, asserted.
type pageLister struct {
	db *store.Store
}

// PagesForCampaign lists a campaign's indexed pages for link resolution.
//
// The error names the campaign id, and that is the whole reason this type still
// exists rather than the store handle being handed to the route directly: a bare
// store error says a query failed, and this one says whose links are about to be
// wrong. A campaign the query cannot answer produces an error rather than an empty
// list, because an empty list makes every link on every page broken — which looks
// like a content problem and is not one.
//
// A campaign whose content root could not be opened is deliberately **not**
// answered here. Its rows are stale, because nothing walked its tree to prune them,
// and the route fails earlier and more honestly: the root lookup refuses and the
// response is a load error naming a request id (S-4.5, ADR 0024). A 404 for it
// would read as "this campaign does not exist", which is a different statement and
// a false one.
func (l pageLister) PagesForCampaign(
	ctx context.Context,
	campaignID int64,
) ([]domain.Page, error) {
	pages, err := l.db.PagesForCampaign(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf(
			"list the indexed pages of campaign %d: %w",
			campaignID,
			err,
		)
	}

	return pages, nil
}

// productVersion is the build's version as the rail shows it.
//
// A constant rather than an ldflag-injected variable because nothing injects one
// yet and an empty version is a truthful answer where "0.0.0" would not be. The
// moment the release workflow stamps a version this becomes a var, and nothing
// else changes: the rail renders whatever it holds.
const productVersion = ""

// newWikiRoute builds the campaign-scoped wiki handler over the content roots, the
// per-campaign renderers, the kind registry and the maintained page index.
//
// One constructor rather than a struct literal in the composition root because the
// nine fields are one decision — *what serves a campaign's pages* — and a literal
// copied into a test is a second place to forget one of them. `runServer` builds
// the handler through this and the tests build it through this, so a test asserting
// that a page's references resolve is asserting it about the handler the product
// serves rather than about a fixture that resembles it.
//
// `kinds` and `pages` are parameters rather than being read from a registry inside
// here: P8 replaces the former with the plugin registry and this phase replaced
// the latter with the maintained table, and a constructor that looked them up would
// have to be edited for each.
func newWikiRoute(
	roots *content.Registry,
	renderers wiki.CampaignRenderers,
	kinds domain.PageKindRegistry,
	pages wiki.Pages,
	instance components.InstanceView,
	logger *slog.Logger,
) *wiki.Handler {
	return &wiki.Handler{
		Roots:     roots,
		Renderers: renderers,
		Kinds:     kinds,
		Pages:     pages,
		// P10 replaces this. Until then `[!secret]` content is **not** redacted,
		// and this is the one place on the request path that fact is written down.
		Redactor: content.NoSecrets(),
		Cache:    content.NewCache(renderCacheEntries),
		Logger:   logger,
		// The instance's own identity, carried from the composition root rather
		// than left zero.
		//
		// A zero `InstanceView` renders an empty name and an empty version in the
		// header and the rail on every page. `displayName()` falls back to the
		// product name, so the *name* survives — but the rail's version line and,
		// more importantly, `Degraded` cannot: an empty slice means healthy by
		// construction, so a campaign whose content root vanished (S-4.5) was
		// computed as degraded by the pipeline and then rendered as healthy by
		// the interface. That is the worst shape this bug can take, because both
		// halves reported success.
		Instance:    instance,
		SignOutHref: signOutHref,
	}
}

// newAssetRoute builds the campaign-scoped asset handler over the content roots.
//
// The dependency set is the smallest one that can serve the route, and each entry
// is a decision rather than a convenience:
//
//   - `Roots` and nothing else for finding content. The route resolves a slug to a
//     confined root and refuses anything else, so the handle it holds is the
//     confinement boundary (S-3.5) rather than a way to reach a filesystem.
//   - `Instance` and `SignOutHref` only for the *failure* states. A found asset is
//     bytes and a media type from a closed table; nothing about it needs the
//     instance's identity, and the shell exists here so a reader who mistyped a
//     path lands on a designed page rather than net/http's plain-text 404.
//
// The logger is optional in the handler's own contract, and the composition root
// passes the process logger anyway — a refused asset is a line an operator greps
// for, and "there is no logger here" is not a reason for there to be none.
func newAssetRoute(
	roots *content.Registry,
	instance components.InstanceView,
	logger *slog.Logger,
) *assets.Handler {
	return &assets.Handler{
		Roots:       roots,
		Logger:      logger,
		Instance:    instance,
		SignOutHref: signOutHref,
	}
}

// newSearchRoute builds the campaign-scoped search handler over the store.
//
// `Pages` is the store handle itself, because `*store.Store` satisfies
// `search.Pages` and a second adapter would be a second place to get the campaign
// scope right — and the campaign scope is the field this route's security rests
// on. `store.PageSearch.CampaignID` of 0 means *every campaign the requestor may
// read*, so an adapter that forgot to set it would not fail a test, it would leak
// private page titles into a stranger's results. The fewer translations between
// the composition root and the query, the fewer ways that can happen.
func newSearchRoute(
	pages *store.Store,
	instance components.InstanceView,
	logger *slog.Logger,
) *search.Handler {
	return &search.Handler{
		Pages:       pages,
		Instance:    instance,
		SignOutHref: signOutHref,
		Logger:      logger,
	}
}

// newEditRoute builds the campaign-scoped editor handler.
//
// `Revisions` is the one field that cannot be a plain assignment, and the reason
// is a Go language rule rather than a design preference. `store.Store.Write` takes
// an **unexported** parameter type, `store.writeFunc`:
//
//	func (s *Store) Write(ctx context.Context, fn writeFunc) error
//
// An interface method's parameter types must be *identical*, not merely
// assignable, and no package outside `store` can even name `writeFunc`. So there
// is no interface `*store.Store` satisfies that expresses "run this in a
// transaction", and the only way through is a closure — which converts one
// function type to the other implicitly, without either being named at the call
// site.
//
// The closure is not a convenience and it is not the only way to reach a
// transaction; it is the only way to reach **the writer queue**. `Store.DB()` is
// documented for reads, and a request goroutine writing on it bypasses the queue
// whose entire job is to keep exactly one statement in flight so SQLite's
// single-writer limit is never contended. Two writers is a `SQLITE_BUSY`, so the
// second one to arrive loses a GM's save.
//
// `Renderers` is the *same map* the wiki route holds, not a second one. Both named
// types are `map[string]*content.Renderer` and a Go conversion between two named
// map types shares the underlying map rather than copying it, so there is one set
// of renderers in this process and therefore one answer to "which campaigns have
// a renderer". Two maps would be two answers, and the shape of the failure is the
// bad one: a campaign present in one and absent from the other renders a page on
// the wiki route and a load error in the editor, with nothing in the logs to say
// which side is wrong. `TestTheEditorAndTheWikiRouteHoldTheSameRenderers` is the
// assertion.
//
// The redactor is `content.NoSecrets()`, which removes nothing. P10 replaces it,
// and the editor still calls it with `include_secrets=true` so the ordering S-5.7
// requires is in place before the redactor starts removing things. A comment
// saying so sits on the field rather than only here, because the field is where a
// reader looks.
func newEditRoute(
	roots *content.Registry,
	backing *store.Store,
	renderers edit.CampaignRenderers,
	kinds domain.PageKindRegistry,
	instance components.InstanceView,
	logger *slog.Logger,
) *edit.Handler {
	return &edit.Handler{
		Roots: roots,
		Revisions: edit.NewRevisionLog(
			func(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
				return backing.Write(ctx, fn)
			},
		),
		Renderers: renderers,
		Kinds:     kinds,
		// P10 replaces this. Until then `[!secret]` content is **not** redacted
		// anywhere, and the editor is the surface where that matters most because
		// it is GM-only and renders with `include_secrets=true`.
		Redactor:    content.NoSecrets(),
		Logger:      logger,
		Instance:    instance,
		SignOutHref: signOutHref,
	}
}

// newEventRoute builds the campaign-scoped event stream over the hub.
//
// Two fields, and the handler holds no state: a `Handler` is shared by every
// request, so anything per-connection has to be a local variable in the handler's
// own goroutine, which `net/http` already accounts for.
func newEventRoute(hub *events.Hub, logger *slog.Logger) *events.Handler {
	return &events.Handler{Hub: hub, Logger: logger}
}

// editorRenderers is the wiki route's renderer map seen as the editor's.
//
// A conversion and nothing else. Go does not copy a map when converting between
// two named types with the same underlying type — the result shares the same
// header, so a renderer built once is the renderer both routes hold and a
// campaign added to one is in the other. That is the whole point of doing it this
// way rather than ranging over the map into a second one, and the assertion is
// `TestTheEditorAndTheWikiRouteHoldTheSameRenderers`.
//
// The parameter is `wiki.CampaignRenderers` rather than the underlying type so
// that the two routes' names meet in exactly one place. If either package's
// declaration of the map changes, this is the line that stops compiling, which is
// the moment a reader wants to know.
func editorRenderers(renderers wiki.CampaignRenderers) edit.CampaignRenderers {
	return edit.CampaignRenderers(renderers)
}

// instanceView assembles the instance identity every page's header and rail
// renders.
//
// Two jobs, and the second is the one that mattered. `campaignroots.Open` already
// knows which campaigns have no readable content root (S-4.5) — it returns that
// list and the server logs it — and nothing carried it into the interface. The
// pipeline marks a campaign degraded and logs `watch.degraded`; the rail then
// rendered "everything is fine", because an empty `Degraded` slice means healthy
// by construction and nothing ever put an entry in it.
//
// So the degradation the boot already computed becomes the notice the operator
// sees. It is a value rather than a call into a registry, because it is read on
// every request and must not be a thing that can fail at render time.
//
// The version is the product name alone until the build stamps one; a build
// without a version is the normal case here, and an empty version line is better
// than a fabricated one.
func instanceView(cfg config.Config, degraded campaignroots.Degraded) components.InstanceView {
	view := components.InstanceView{
		Name:    cfg.InstanceName,
		Version: productVersion,
	}

	for _, entry := range degraded {
		view.Degraded = append(view.Degraded, components.DegradedView{
			// The campaign's slug is operator-supplied, from their own
			// registration, so naming it here discloses nothing.
			Name:   "Campaign " + entry.Slug,
			Detail: "its content root is not readable, so its pages cannot load",
		})
	}

	return view
}

// mustListCampaigns enumerates campaigns at startup.
//
// The name says what it costs: a failure here is logged and treated as an empty
// list, because by the time this runs the enumeration has already succeeded once
// — `campaignroots.Open` did it moments earlier — and a second failure is
// vanishingly unlikely. Panicking or aborting the boot over it would be a
// response to an event that has no realistic cause, and the consequence would be
// an instance that refuses to start with no explanation on the console.
//
// Returns nil on failure, so the loop that follows builds no renderers and every
// campaign route then answers a load error naming a request id — which is a
// diagnosable state, and a better one than a silent empty wiki. The pipeline reads
// the same empty list, which means it watches and indexes nothing rather than
// watching and indexing the wrong thing.
func mustListCampaigns(ctx context.Context, db *store.Store) []domain.Campaign {
	campaigns, err := db.Campaigns(ctx)
	if err != nil {
		slog.Error("campaign enumerate failed at startup; no renderers built",
			slog.String("error", err.Error()),
		)

		return nil
	}

	return campaigns
}

// ensure the adapter satisfies the route's interface at compile time rather than
// by a test that fails only when the route is exercised.
var _ interface {
	PagesForCampaign(ctx context.Context, campaignID int64) ([]domain.Page, error)
} = pageLister{}

// ensure the kind registry satisfies the domain's, for the same reason.
var _ domain.PageKindRegistry = pageKinds{}
