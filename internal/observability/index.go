// The page indexer's observability surface.
//
// A sibling of `watch.go`, and deliberately a separate type rather than four more
// methods on `Watch`: the watcher reports on *observation* and the indexer on
// *the derived state*, they fail for unrelated reasons, and a caller that has
// only one of them should not have to construct the other. The counters are
// registered into the same `Registry`, so `/readyz` renders all ten together
// (S-12.1).
//
// # Why these four names are not in the architecture record
//
// §13.2 fixes the observability contract, and this adds to it. The record is
// immutable, so the correction is a record of its own —
// 0032-index-signals-beyond-the-architecture-list — rather than an edit there.
// The alternative was not "no signal": it was emitting through `slog` at the call
// site, which is what the indexer did first. That works, and it is how three of
// this repository's signals were written before this package existed, and it is
// the reason S-12.3 is a convention rather than a guarantee: a `slog.Any` at a
// call site accepts anything, and nothing stops it being handed a page's bytes.
//
// So the four names are the price of the guarantee. They are a deviation, and the
// deviation is cheaper than the hole.
//
// # Why four and not one per failure mode
//
// The indexer distinguishes seven failure shapes internally and emits four. The
// difference is that `op` is an attribute rather than part of the name, so a
// remove that failed and a rename that failed are both `index.change_failed` with
// a different `op` — one signal to alert on, one row on `/readyz`, one query.
//
// The alternative — a name per failure shape — was rejected because it makes the
// dashboard the unit of counting. An operator's question is "is the index keeping
// up", and seven counters that each answer "did this one thing happen" is seven
// queries to answer it, with the failure mode that the seventh is the one that is
// broken.
//
// # What an event may carry
//
// Inherited from `EventAttributes` and adding nothing: no `[]byte`, no `any`, no
// free-form context bag. `ErrorClass` reduces the error text to a class for the
// reason `watch.go` gives — a parser quotes the line it choked on, and that line
// is frequently a secret callout body.

package observability

import (
	"context"
	"log/slog"
)

// Index is the page indexer's signal surface: four counters and four emitters.
//
// One per process, built by the composition root alongside `Watch` and passed to
// the indexer. A nil registry and a nil logger are both tolerated, for the reason
// `NewWatch` gives: a missing line is recoverable and a panic during a filesystem
// event is not.
type Index struct {
	logger *slog.Logger

	changeFailed     *Counter
	pageSkipped      *Counter
	pageDegraded     *Counter
	renameSourceLeft *Counter
}

// NewIndex registers the indexer's four counters and returns the surface.
//
// A second `Index` over the same registry reuses the first's counters rather than
// replacing them, so a count accumulated by the first is not lost — the same
// guarantee `Registry.Register` already gives, inherited rather than reimplemented.
func NewIndex(registry *Registry, logger *slog.Logger) *Index {
	index := &Index{
		logger:           logger,
		changeFailed:     NewCounter(string(EventIndexChangeFailed)),
		pageSkipped:      NewCounter(string(EventIndexPageSkipped)),
		pageDegraded:     NewCounter(string(EventIndexPageDegraded)),
		renameSourceLeft: NewCounter(string(EventIndexRenameSourceLeft)),
	}

	if registry != nil {
		registry.
			Register(index.changeFailed).
			Register(index.pageSkipped).
			Register(index.pageDegraded).
			Register(index.renameSourceLeft)
	}

	return index
}

// ChangeFailed reports a settled change the indexer could not apply.
//
// Error, and deliberately not routed through S-12.2's rule-fixed list — that list
// is the floor, not the ceiling, and this one clears it by judgment for the same
// reason `watch.add_failed` does. A GM saves a page, the save returns 200, and
// the page is absent from search until the next rescan: from outside, that is
// indistinguishable from a save that worked, which is the specific shape of the
// failure this system is most prone to and least able to notice.
//
// `err` may be nil for the same reason `Watch.AddFailed` accepts one: the
// condition is a fact rather than an error value. `op` is the operation that
// failed, from the caller's closed vocabulary.
func (i *Index) ChangeFailed(ctx context.Context, campaignID, operation, path string, err error) {
	i.changeFailed.Inc()

	Event(ctx, i.logger, EventIndexChangeFailed, slog.LevelError, EventAttributes{
		CampaignID: campaignID,
		Path:       path,
		Detail:     ErrorClass(err),
		Op:         operation,
	})
}

// PageSkipped reports a file the walk found that is not in the index.
//
// Warn, and the reasoning is the opposite of `ChangeFailed`'s: one unreadable page
// in a vault of five hundred is an operator's problem to look at, not a service
// that is failing. The rescan will try again, and a page that is over the size cap
// will be over it next time too.
//
// `path` and `campaignID` may both be empty when the file could not be attributed
// to a campaign at all.
func (i *Index) PageSkipped(ctx context.Context, campaignID, path string, err error) {
	i.pageSkipped.Inc()

	Event(ctx, i.logger, EventIndexPageSkipped, slog.LevelWarn, EventAttributes{
		CampaignID: campaignID,
		Path:       path,
		Detail:     ErrorClass(err),
	})
}

// PageDegraded reports a page that **is** indexed but whose front matter did not
// interpret, so its kind and title fell back to prose (S-3.3).
//
// Warn, and a separate signal from `PageSkipped` because the two answers are
// opposites: a skipped page is unfindable, a degraded page is findable and
// readable with the wrong title. An operator chasing "why does this page not
// render as a token" is looking for this line and the other one would mislead them.
func (i *Index) PageDegraded(ctx context.Context, campaignID, path string, err error) {
	i.pageDegraded.Inc()

	Event(ctx, i.logger, EventIndexPageDegraded, slog.LevelWarn, EventAttributes{
		CampaignID: campaignID,
		Path:       path,
		Detail:     ErrorClass(err),
	})
}

// RenameSourceLeft reports a rename whose fallback indexed the destination but
// could not drop the old row, so a search hit still names the path the page left.
//
// Warn rather than error because it is self-healing: the next prune removes the
// row, and the prune is on the rescan path S-4.5 requires. Not silent, because
// until that prune runs the observable symptom is a search result that 404s, and an
// operator seeing a 404 from search has no way to connect it to a rename that
// happened minutes earlier.
//
// `count` is how many rows are believed left behind, which is always one here but
// is carried rather than assumed so the field's meaning does not depend on this
// being the only caller.
func (i *Index) RenameSourceLeft(
	ctx context.Context,
	campaignID, from string,
	count int,
	err error,
) {
	i.renameSourceLeft.Inc()

	Event(ctx, i.logger, EventIndexRenameSourceLeft, slog.LevelWarn, EventAttributes{
		CampaignID: campaignID,
		Path:       from,
		Detail:     ErrorClass(err),
		Count:      count,
	})
}

// Renamed reports a directory rename that moved a subtree of rows.
//
// Debug, and the only method here below warn. That is the point of it: moving a
// subtree is the system working, and the number an operator reads off the line is
// the count, not the event. Were it a counted signal it would sit in the same
// dashboard as the four failures above it, and a rising line would mean two
// opposite things depending on which of the five it was.
func (i *Index) Renamed(ctx context.Context, campaignID, from, to string, count int) {
	Event(ctx, i.logger, EventIndexRenamed, slog.LevelDebug, EventAttributes{
		CampaignID: campaignID,
		From:       from,
		To:         to,
		Count:      count,
	})
}
