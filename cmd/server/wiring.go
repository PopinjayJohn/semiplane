package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/wiki"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

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
	logger *slog.Logger,
) *wiki.Handler {
	return &wiki.Handler{
		Roots:     roots,
		Renderers: renderers,
		Kinds:     kinds,
		Pages:     pages,
		// P10 replaces this. Until then `[!secret]` content is **not** redacted,
		// and this is the one place on the request path that fact is written down.
		Redactor:    content.NoSecrets(),
		Cache:       content.NewCache(renderCacheEntries),
		Logger:      logger,
		Instance:    components.InstanceView{},
		SignOutHref: "/logout",
	}
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
