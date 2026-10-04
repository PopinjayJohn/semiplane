package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/semiplane/semiplane/internal/campaignroots"
	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/accounts"
	"github.com/semiplane/semiplane/internal/httpapi/assets"
	"github.com/semiplane/semiplane/internal/httpapi/edit"
	"github.com/semiplane/semiplane/internal/httpapi/events"
	pluginroutes "github.com/semiplane/semiplane/internal/httpapi/plugins"
	"github.com/semiplane/semiplane/internal/httpapi/search"
	"github.com/semiplane/semiplane/internal/httpapi/secrets"
	"github.com/semiplane/semiplane/internal/httpapi/theme"
	"github.com/semiplane/semiplane/internal/httpapi/wiki"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
	"github.com/semiplane/semiplane/internal/web/plugins/linkpreview"
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

// realtimeFlushBudget bounds the realtime plane's shutdown flush.
//
// Ten seconds, and the number is a reasoning rather than a measurement: the flush
// is one small row per live campaign on a writer queue that is otherwise idle,
// because by this point the HTTP server has stopped accepting and the content
// pipeline has stopped publishing. Phase 6 measured the whole drain at 110ms with
// a live stream, so this is roughly ninety times the observed cost — which leaves
// room for a slow disk and bounds the case where there is no slow disk but a
// wedged writer.
//
// **Not** the configured shutdown budget, and that is deliberate: `cfg.ShutdownTimeout`
// is the *HTTP* drain's budget and this is a different resource with a different
// cost. Sharing one would mean a slow flush eats the time the in-flight requests
// needed, and a slow request eats the time the flush needed. Separate budgets, and
// a shutdown that overruns the sum of them is a shutdown with a wedged disk.
const realtimeFlushBudget = 10 * time.Second

// productVersion is the build's version as the rail shows it, and as the demo
// command reports it.
//
// # It was a `const`, and the comment above this line said when that would stop
// being right. That moment has arrived.
//
// The comment read: *"The moment the release workflow stamps a version this becomes a
// var, and nothing else changes."* Phase 11's artefact work item added the release
// workflow — and then found, by running the release path rather than reading it, that
// `-ldflags -X` against a **constant is silently ignored**. The linker writes to
// variables, so a `-X` naming a `const` compiles, links, exits 0, and changes nothing.
//
// That made the demo seed's version-skew refusal **unreachable in every build of this
// tree**: `internal/demo.Check` compares the artefact's declared `product` against this
// value, and this value was permanently `""`, so `Check` took its versionless branch on
// every run — warning and proceeding — rather than its refusal branch. A gate whose
// condition cannot be met is not a gate, and this one looked armed in the source and
// disarmed in the binary.
//
// The distinction matters more than it looks: `-X` on a `const` produces **no error and
// no warning**, so nothing in the release logs would ever have said the version was not
// stamped. That is the failure this repository keeps meeting in a new costume — correct
// markup, absent behaviour, nothing red.
//
// # Why a var is not simply a smaller version of the same thing
//
// A `var` with no `-X` is still `""`, which is the truthful answer for a development
// build and is what `Check`'s versionless branch already handles. So the change is
// behaviour-preserving for every build that does not stamp one, and it is the *stamped*
// build that gains the refusal. The rail renders whatever it holds either way, which is
// what the old comment meant by "nothing else changes".
//
// # The two consumers, and why they want the same value
//
// `components.InstanceView.Version` renders in the rail, and `cmd/server/demo.go` hands it
// to `demo.Env.Version` for `Check`. They want the same thing — what release this binary
// is — so they read one name rather than two that could disagree, which is the same
// reason `cmd/server/systems.go` exists.
var productVersion = ""

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
// `kinds` is the **plugin registry's** kind table (`plugins.pageKinds`), handed in
// rather than looked up here: this package does not know what a gameplay plugin
// declares, and a constructor that reached for it would have to be edited the first
// time the answer moved. `pages` is the maintained `pages` table for the same reason
// on the other axis — the index is a rebuildable cache of the filesystem, and a
// route that walked the tree itself would be a second source of truth about what
// the campaign contains.
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

// themeNotices adapts the theme route's refusal memory to the campaign
// overview's `accounts.ThemeNotices`.
//
// **In the composition root, and not beside either side**, for the reason
// `kindRegistry` is there: `theme.Notice` and `components.CampaignNotice` are
// two types answering one question, and the place that knows both is the place
// that writes every registration in this process. Putting the adapter in either
// package would make one import the other to translate two strings -- and the
// string it would be translating is a *refusal message*, which is exactly the
// sort of value that must not acquire a second representation that can drift.
//
// `campaignID` is passed through untouched. This adapter does no gating and must
// not: the caller decides who may see a notice, and a lookup that refused would
// be authorisation in a function with no request to authorise.
func themeNotices(handler *theme.Handler) accounts.ThemeNotices {
	if handler == nil {
		return nil
	}

	return themeNoticeLookup{handler}
}

// themeNoticeLookup is the method set, so the closure above is not the only
// shape a reader has to imagine.
type themeNoticeLookup struct{ handler *theme.Handler }

func (lookup themeNoticeLookup) ThemeNotice(
	campaignID int64,
) (components.CampaignNotice, bool) {
	notice, ok := lookup.handler.Notice(campaignID)
	if !ok {
		return components.CampaignNotice{}, false
	}

	return components.CampaignNotice{Token: notice.Token, Reason: notice.Reason}, true
}

// newThemeRoute builds the campaign theme handler (UI §4.12).
//
// `Roots` is the same `*content.Registry` the wiki and assets routes are given,
// and that sharing is the point rather than a convenience: the theme manifest is
// read through the **same `os.Root`** as every page beside it, so a manifest is
// confined by exactly the boundary pages are. A second registry, or a path
// resolved against the campaign's directory by hand, would be a second answer to
// "may this name be read" — and S-3.5's confinement is the kind of boundary a
// second implementation quietly does not have.
//
// The logger is passed for the reason it is on every other route: a refused
// manifest is an error line an operator greps for, and `theme.brand_invalid` on
// a campaign nobody looks at is a silently unreadable UI.
func newThemeRoute(roots *content.Registry, logger *slog.Logger) *theme.Handler {
	return &theme.Handler{
		Roots:  roots,
		Logger: logger,
	}
}

// newSecretRoute builds the campaign-scoped reveal handler over the store.
//
// `*store.Store` is passed as the `secrets.Ledger` interface directly, and there is
// no adapter, for the reason `newSearchRoute` gives for its own interface: the
// adapter would be a second place to get the campaign scope right, and the campaign
// scope is what this route's authorization rests on. A row written for the wrong
// campaign is a disclosure recorded against the wrong reader's history.
//
// `Roots` is the same `*content.Registry` every other campaign route takes, so path
// confinement is the same `os.Root` per campaign and not a second implementation.
func newSecretRoute(
	roots *content.Registry,
	ledger *store.Store,
	logger *slog.Logger,
) *secrets.Handler {
	return &secrets.Handler{
		Roots:  roots,
		Ledger: ledger,
		Logger: logger,
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

// newPluginRoute builds the campaign-scoped plugin handler.
//
// # Four fields, and each one is a decision
//
//   - `UI` is the UI tier from `systems.go`, so the page types and render hooks a
//     plugin registered are the ones this route serves. A second registry would be a
//     second answer to "which page types does this build have", and the failure is the
//     bad one: a route serving a page type nothing registered renders whatever the
//     mismatch produced.
//   - `Hub` is the realtime hub, which is what makes the roller's send a *gameplay*
//     act rather than a form post. §10.6's roller "dispatches the same intents a human
//     player would, so it passes identical authorisation and validation", and the only
//     way that is true is for the button and the keystroke to reach the same hub.
//   - `Systems` reports a campaign's system, so the roller shows **the campaign's own
//     notation** rather than guessing one. That is §10.4's whole claim — the table is
//     data and the resolver reads it — and `rules.Grammar`'s own comment is the reason
//     guessing is forbidden: "the protocol never assumes d20". It is a **function over
//     the store** rather than the registry, because the answer lives in a column, and
//     it degrades: a campaign whose system this build does not resolve renders the
//     widget with no notation and the widget says so, which is S-10.6's first row
//     reached from the UI side.
//   - `Preview` is the link-preview fetcher. **Constructed here with `linkpreview.New()`**
//     rather than left nil, and the nil-tolerant 503 branch in `plugins.Handler` is not
//     exercised by this build — which is the right way round: the honest failure is a
//     preview that cannot be fetched, and the branch exists for a deployment that
//     genuinely has no fetcher rather than so the composition root can skip a line.
//
// The logger is the process logger, for the reason the asset route's is: a refused
// roll is a line an operator greps for, and "there is no logger here" is not a reason
// for there to be none.
func newPluginRoute(
	backing *store.Store,
	registered plugins,
	hub *realtime.Hub,
	logger *slog.Logger,
) *pluginroutes.Handler {
	return &pluginroutes.Handler{
		UI:      registered.ui,
		Hub:     hub,
		Systems: registered.systemFor(backing),
		Preview: linkpreview.New(),
		Logger:  logger,
	}
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
