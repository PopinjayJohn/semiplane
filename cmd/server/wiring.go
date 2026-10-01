package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
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
// It walks the campaign's content root rather than reading the `pages` table,
// and that is a decision rather than an omission. ADR 0006 makes the filesystem
// the source of truth and the database a rebuildable index, so a walk *is* the
// index — and in this phase it is the only thing that works at all, because
// nothing writes `pages` until the watcher (P4) indexes the tree. Reading the
// table would resolve no links at all, and the observable result is a wiki where
// every wikilink is marked broken.
//
// The swap is one line here: when P4's index is maintained, this becomes the
// store query and the walk goes away. The route does not move, because the seam
// is this type.
type pageLister struct {
	roots *content.Registry
	db    *store.Store
}

// PagesForCampaign lists a campaign's pages for link resolution.
//
// The campaign is named by id, which is why this holds the registry rather than a
// single root: the route resolves one campaign per request and the registry is
// the thing that turns a slug into a confined root. An id the registry does not
// hold is a wiring fault, and it is reported as an error naming the id rather
// than as an empty list — an empty list makes every link broken, which looks like
// a content problem and is not one.
func (l pageLister) PagesForCampaign(
	ctx context.Context,
	campaignID int64,
) ([]domain.Page, error) {
	campaign, err := l.db.CampaignByID(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("look up campaign %d for its page index: %w", campaignID, err)
	}

	root, err := l.roots.Get(campaign.Slug)
	if err != nil {
		return nil, fmt.Errorf(
			"content root for campaign %d (%s): %w",
			campaignID,
			campaign.Slug,
			err,
		)
	}

	index, err := content.BuildPageIndex(root, campaign.ID)
	if err != nil {
		return nil, fmt.Errorf("build page index for campaign %d: %w", campaignID, err)
	}

	return index.Pages(), nil
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
// diagnosable state, and a better one than a silent empty wiki.
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
