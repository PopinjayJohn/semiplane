// The content pipeline's composition-root wiring: the confined roots, one watcher,
// one settle filter, one indexer, and the startup index that makes the `pages`
// table able to answer a request at all.
//
// # The order, and why construction runs against the flow
//
// S-4.1 makes the pipeline event-driven, and events travel
//
//	content root → watcher → settle filter → indexer → `pages` → the wiki route
//
// so construction runs the other way: each stage is the **sink** of the one after
// it, and nothing can be handed a collaborator that does not exist yet. The three
// lines are therefore in reverse order on purpose, and two properties of that graph
// are worth stating because they are what a later edit breaks silently:
//
//   - The settle filter, **not** the indexer, is the watcher's sink. Both are a
//     `func(context.Context, content.Change)`, so wiring the watcher's sink straight
//     to `Indexer.HandleChange` compiles — and reinstates exactly the defect
//     S-4.3 exists to prevent: a page indexed from bytes that were still moving,
//     hashed against a file that never held them, and a render-cache entry that
//     never invalidates.
//
//   - `Debouncer.Touch` and `Debouncer.Moved` take **no** `context.Context`, and that
//     is not an oversight to correct. They arm a deadline under a mutex and do no
//     I/O; the debouncer's constructor context is its lifetime. A context parameter
//     here would be one a reader has to check for a cancellation that cannot happen,
//     which is exactly what `containedctx` and `unparam` disagree about and what
//     `debounce.go` settled. `changeRouter` therefore discards the sink's context,
//     and says so where it does it.
//
// # What the composition root owns that no subsystem does
//
// Two things, and both are invisible until they are missing:
//
//   - The one `observability.Watch` and the one `observability.Index`, shared by
//     every component below. The gauges on `Watch` live in the value rather than in
//     the counter, so two `Watch` values over one registry would each move the same
//     gauge for one transition and double it.
//   - The **startup index**, run before the server listens. An empty `pages` table
//     resolves no reference at all, so a wiki served against one marks every
//     `[[wikilink]]` broken — which is a link-resolution bug's symptom with a
//     missing boot step as its cause. That is the defect phase 3 shipped and this
//     wiring removes.
//
// What it does **not** own: the periodic re-verify and the rescan fallback of
// S-4.5. `content.Watcher.Reverify` exists and its own comment says the composition
// root must call it on a slow timer, and no loop here does. That is a separate work
// item with its own state — a per-campaign rescan schedule and the transitions that
// move `watch.degraded` and `watch.rescan_fallback` — and a loop added without those
// decisions would emit a fallback that nothing runs.

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/observability"
)

// contentSignals is the §13.2 surface the content pipeline reports through: the
// watcher subsystem's counters and the indexer's four.
//
// A struct rather than two return values because they are constructed together,
// shared together, and passed together into three components, and a caller that
// could mix one from this process with another from a different one is the mistake
// the sharing exists to prevent.
type contentSignals struct {
	// watch is the watcher, settle filter and render surface's surface. S-12.2
	// fixes `watch.add_failed` and `content.render_error` at error level, and this
	// is where that rule stops being advisory.
	watch *observability.Watch

	// index is the page indexer's surface: four counters that are not in the
	// architecture record, recorded as a deviation in ADR 0032.
	index *observability.Index
}

// newContentSignals constructs both surfaces over the process's one counter
// registry, and returns them for sharing.
//
// Constructed here rather than at each use site because the registration *is* the
// wiring: `observability.NewWatch` and `NewIndex` register their counters as they
// construct, so a surface built anywhere else is a surface that never reached
// `/readyz`. Registering at construction is what makes S-12.1's promise hold —
// eleven keys rendered at zero before anything has failed, rather than eleven keys
// that appear one at a time as subsystems are wired.
//
// The alternative that lost — a helper per component, each constructing its own —
// is the one the `Watch` type's own comment rules out: the gauges are per value, so
// a second `Watch` over one registry doubles every transition the first records.
func newContentSignals(
	registry *observability.Registry,
	logger *slog.Logger,
) contentSignals {
	return contentSignals{
		watch: observability.NewWatch(registry, logger),
		index: observability.NewIndex(registry, logger),
	}
}

// pipelineCounterNames are the counters this wiring puts on `/readyz`, in the order
// a reader wants them: what the watcher is doing, then what the indexer made of it.
//
// Listed rather than derived from `observability.AllEventNames`, because that list
// is the whole specification's surface and this is the part of it phase 4 wires.
// Asserted by `TestReadyzRendersThePipelineCountersAsZeros`: an unwired signal is a
// visible zero (S-12.1), and a visible zero only exists if somebody looked for the
// key.
var pipelineCounterNames = []string{
	// The watcher's own four: a watch that could not be added, a watcher that
	// stopped delivering, one that resumed, and one that fell back to rescanning.
	"watch.add_failed",
	"watch.degraded",
	"watch.recovered",
	"watch.rescan_fallback",
	// The settle filter's two: a confirmation that ran out of budget, and a `stat`
	// that failed for a reason other than the path being gone.
	"content.stable_read_timeout",
	"content.settle_failed",
	// The indexer's four: a change that did not reach the table, a file the walk
	// found that is not in it, a page whose front matter did not interpret, and a
	// rename whose source row outlived the move.
	"index.change_failed",
	"index.page_skipped",
	"index.page_degraded",
	"index.rename_source_left",
}

// renderErrorCounter is the eleventh key this wiring registers, and it is called
// out separately because it belongs to the render surface rather than to the
// watcher.
//
// `content.render_error` is one of S-12.2's three rule-fixed names, and it counts a
// page that would not render. `observability.Watch` constructs and registers it,
// because the render cache and the content pipeline share a process and a reader
// watches them together; it is listed apart from the ten so that nobody reads this
// file and concludes the render surface is unwired.
const renderErrorCounter = "content.render_error"

// changeRouter is the watcher's `ChangeSink`: the small adapter between the two
// halves of the pipeline that are not the index.
//
// A type rather than a closure because it is the one place in the composition root
// where a wrong call is invisible — a closure that forwarded straight to
// `Indexer.HandleChange` would look identical in a diff and would skip the settle
// filter entirely, which is the failure mode above.
type changeRouter struct {
	debouncer *content.Debouncer
}

// settle hands one change the watcher has delivered to the settle filter.
//
// `Touch` for **every** change, and `Moved` additionally for the one the watcher
// paired. Both, in that order, rather than a switch on the operation, because
// `Moved` is additive: it marks the destination as a move and re-arms it, so a
// `Touch` that ran first is superseded rather than contradicted. The alternative that
// lost — switch on `Op` and let `Moved` be the whole answer for a rename — puts the
// decision of what counts as a move in two files, and `debounce.go` refuses to
// infer one for good reason: `OpRename` re-keys a row and keeps its `content_hash`,
// so a wrongly paired move holds one path's hash against another path's bytes.
//
// `Touch` on a paired move costs nothing, and that is worth saying because the pair
// looks redundant: the only move the watcher pairs is a directory's, and `Touch`
// refuses any path that is not a `.md` page, so for every change that reaches
// `Moved` the `Touch` above returned at its first predicate.
//
// The context is discarded, and `unparam` is right that it is always the watcher's
// lifetime: `Touch` and `Moved` take none, deliberately, because neither does any
// I/O. See the file comment.
func (r changeRouter) settle(_ context.Context, change content.Change) {
	r.debouncer.Touch(change.CampaignID, change.Slug, change.Path)

	if change.Op != content.OpRename || change.OldPath == "" {
		return
	}

	r.debouncer.Moved(change.CampaignID, change.Slug, change.OldPath, change.Path)
}

// contentPipeline is the assembled pipeline of S-4.1: one watcher, one settle
// filter and one indexer over the campaigns whose content root opened.
//
// A value rather than three separate handles at the call site because the three
// share a lifecycle: they are constructed together, they are stopped together, and
// the order they are stopped in is fixed by the order they were wired in. A caller
// holding three handles has to remember that ordering on its own, and what it
// produces when it does not is a goroutine writing to a store that has already
// answered `Close`.
type contentPipeline struct {
	watcher   *content.Watcher
	debouncer *content.Debouncer
	indexer   *content.Indexer

	// supervisor owns S-4.5's degraded mode: the slow-timer re-verify and the
	// rescan fallback. It is a field rather than something `newContentPipeline`
	// starts and forgets, because the pipeline's `Close` has to stop it, and a
	// supervisor nothing holds a reference to is a goroutine that outlives the
	// process's request to stop.
	supervisor *content.Supervisor

	// campaigns are the campaigns this pipeline watches and indexes: the ones whose
	// content root this process opened, in slug order. A campaign without one is
	// absent from all three components, and its pages are answered by the route as
	// a load failure naming a request id (S-4.5, ADR 0024).
	campaigns []domain.Campaign

	// signals is the §13.2 surface, carried so `buildIndex` reports a campaign it
	// could not index through the watcher gauges rather than through a log line
	// nobody is watching.
	signals contentSignals
}

// newContentPipeline assembles the pipeline, and reports a wiring fault rather than
// papering over one.
//
// ctx is the process's lifetime. The watcher and the settle filter each take it as
// theirs and stop when it is cancelled, which is what makes a SIGTERM reach
// goroutines that were started before the listener existed.
//
// campaigns is every registered campaign; the ones whose root is not in roots are
// dropped here, once, and `roots` is authoritative over the list. That is not
// tidiness: `content.NewWatcher` cross-checks the two and refuses a watch table that
// does not name exactly the campaigns the registry retains, in both directions, so a
// degraded campaign must be filtered before that check or the process would refuse
// to start over a vault that is merely unmounted — which is the one thing S-4.5 says
// it must not do.
//
// A failure here is fatal, and the failure is fatal because it is a wiring fault
// rather than a content one: an inotify instance that cannot be opened, or a watch
// table that does not match the registry, is this function being wrong about the
// process it is building. Retrying would produce the same answer.
func newContentPipeline(
	ctx context.Context,
	roots *content.Registry,
	campaigns []domain.Campaign,
	pages content.PageStore,
	kinds domain.PageKindRegistry,
	signals contentSignals,
) (*contentPipeline, error) {
	watched := watchableCampaigns(campaigns, roots)

	pipeline := &contentPipeline{
		indexer:   content.NewIndexer(roots, pages, kinds, signals.index),
		campaigns: watched,
		signals:   signals,
	}

	// The zero `SettleTimings` rather than stated durations, because
	// `SettleTimings.withDefaults` is the only place the debounce numbers are
	// written and each of them carries the reasoning for its value. Restating them
	// here would be a second place to change them, and the composition root is the
	// place a change would be *forgotten*.
	pipeline.debouncer = content.NewDebouncer(
		ctx,
		pipeline.indexer.HandleChange,
		roots,
		signals.watch,
		content.SettleTimings{},
	)

	watcher, err := content.NewWatcher(
		ctx,
		roots,
		watchRootPaths(watched),
		changeRouter{debouncer: pipeline.debouncer}.settle,
		signals.watch,
	)
	if err != nil {
		// The settle filter's scheduler is already running, and a constructor that
		// failed hands the caller no pipeline to close — so nothing would ever stop
		// it. Closed here rather than left to a defer that is about to be skipped.
		pipeline.debouncer.Close()

		return nil, fmt.Errorf("open the content watcher: %w", err)
	}

	pipeline.watcher = watcher

	// The supervisor is constructed last because it needs all three of the
	// components above, and started last because a re-verify that runs before the
	// watch set exists would report every campaign as dropped.
	//
	// Started here rather than by the caller because "the pipeline runs its own
	// degraded-mode policy" is a property of the pipeline, and a caller that
	// remembers to start it is a caller that can forget.
	supervisor := content.NewSupervisor(
		roots,
		pipeline.indexer,
		watcher,
		signals.watch,
		content.SupervisorTimings{},
	)

	if err := supervisor.Start(ctx); err != nil {
		// A wiring fault, not a campaign in trouble: the supervisor's own
		// comment says `Start` errors only for a bad argument. Both dependents
		// are already running, so they are closed here rather than left to a
		// defer that is about to be skipped.
		watchErr := watcher.Close()
		pipeline.debouncer.Close()

		if watchErr != nil {
			return nil, errors.Join(
				fmt.Errorf("start the content supervisor: %w", err),
				fmt.Errorf("close the content watcher: %w", watchErr),
			)
		}

		return nil, fmt.Errorf("start the content supervisor: %w", err)
	}

	pipeline.supervisor = supervisor

	return pipeline, nil
}

// Close stops the pipeline: the watcher first, then the settle filter.
//
// The order is the dependency and reversing it manufactures a failure. The watcher
// is what calls `Touch`, so closing the settle filter first would leave a live
// watcher arming deadlines in a filter whose scheduler has gone: the arms would sit
// in a map, and the pages they named would stop being indexed with nothing on any
// surface to say so.
//
// The settle filter then drops what is pending rather than settling it, which is the
// right side to fail on — the index converges from the filesystem at the next boot,
// and settling on the way out would mean writing to a store this process is about to
// close. The caller closes the store after this returns, and closing it first would
// be the same manufactured failure from the other end: an `os.Root`-confined `stat`
// and a database write against handles that have already been shut.
func (p *contentPipeline) Close() error {
	// Supervisor, then watcher, then settle filter.
	//
	// The supervisor first because it is the only component that *calls* the other
	// two: closing it while its timer is mid-pass would leave a pass reindexing
	// through a watcher that is shutting down. The watcher before the settle
	// filter because the reverse order leaves a live watcher arming deadlines in a
	// filter whose scheduler has gone — a timer that can never fire and never
	// expires.
	// `Supervisor.Close` returns nothing and is idempotent by construction, so
	// there is no error to join here — only the watcher's.
	p.supervisor.Close()

	watchErr := p.watcher.Close()

	p.debouncer.Close()

	if watchErr != nil {
		return fmt.Errorf("close the content watcher: %w", watchErr)
	}

	return nil
}

// buildIndex indexes every campaign whose content root is open.
//
// Called before the server listens, and that ordering is the entire reason the
// method exists: an empty `pages` table resolves no reference at all, so a wiki
// served against one marks every `[[wikilink]]` broken. `pageLister` walked the
// content root until this call ran, and the walk is what made the defect invisible
// in phase 3 — it papered over the missing index rather than over the missing boot
// step, so the product worked and the table did not exist.
//
// A campaign that cannot be indexed is degraded and not fatal (S-4.5). Its slug is
// named, `watch.degraded` moves so `/readyz` answers "how many campaigns are
// degraded right now" without anybody reading a container log, and the server
// starts for the campaigns that can. The alternative that lost — returning the
// first failure — is one unmounted disk refusing to start an instance that has nine
// healthy campaigns behind it.
//
// The report is logged and **not** asserted on. `Written` is deliberately not
// `Found`: `Indexer.indexPath` skips a write when the row already holds the file's
// hash, which is precisely what makes the periodic rescan of S-4.5 affordable. A
// first boot over a fresh vault reports `Written == Found`; the next reports
// `Found == Written + Unchanged`; and a check written against either would be a
// check that fails on the other.
//
// It runs before the listener, which is also its cost: a vault large enough to take
// minutes to walk delays the first request rather than answering it against a table
// that does not exist yet. That is the trade S-4.1 makes — a boot step is cheaper
// than a walk per render-cache miss, and both are cheap next to serving a wiki
// whose every link is broken.
func (p *contentPipeline) buildIndex(ctx context.Context, logger *slog.Logger) {
	degraded := make([]string, 0, len(p.campaigns))

	for index := range p.campaigns {
		if ctx.Err() != nil {
			// A shutdown that arrived mid-boot. The remaining campaigns are not
			// degraded — nothing is wrong with any of them — and reporting them as
			// such would be the pipeline's last act on its way out, so the loop
			// stops and the next boot indexes them from the filesystem.
			return
		}

		campaign := &p.campaigns[index]

		report, err := p.indexer.ReindexCampaign(ctx, campaign.Slug, campaign.ID)
		if err != nil {
			degraded = append(degraded, campaign.Slug)

			p.signals.watch.Degraded(ctx, campaignSignalID(campaign.ID), indexFailureDetail(err))

			logger.ErrorContext(ctx, "campaign.index_unavailable",
				slog.String("slug", campaign.Slug),
				slog.String("error", err.Error()),
			)

			continue
		}

		logger.InfoContext(ctx, "campaign.index_built",
			slog.String("slug", campaign.Slug),
			slog.Int("found", report.Found),
			slog.Int("written", report.Written),
			slog.Int("unchanged", report.Unchanged),
			slog.Int("skipped", report.Skipped),
			slog.Int("pruned", report.Pruned),
		)
	}

	if len(degraded) == 0 {
		return
	}

	// One line naming all of them, after one line each. The per-campaign error
	// carries the reason and the request for help; this carries the count, which is
	// what a boot log is read for. The slugs are joined rather than logged as a
	// `[]string` so that no attribute at this call site can hold anything but the
	// operator's own registered names.
	logger.WarnContext(ctx, "instance degraded: some campaigns have no page index",
		slog.Int("degraded_campaigns", len(degraded)),
		slog.String("slugs", strings.Join(degraded, ", ")),
	)
}

// campaignIDBySlug indexes the registered campaigns by slug.
//
// S-12.3 requires every event to carry `campaign_id`, and
// `campaignroots.Degraded` carries a slug because a slug is what the operator
// registered and what the log line is readable with. This is the join between the
// two vocabularies, and it is a boot-time map rather than a query: the enumeration
// that built the degraded list and the one that built this read the same table
// moments apart, and a query here would be a second read that can fail where the
// first did not.
//
// A slug absent from the map is one the enumeration did not return, and the caller
// skips the signal rather than emitting `campaign_id=0` — a fabricated id on an
// event line is worse than no event, because it is one an operator cannot tell from
// a real one.
func campaignIDBySlug(campaigns []domain.Campaign) map[string]int64 {
	ids := make(map[string]int64, len(campaigns))

	for i := range campaigns {
		ids[campaigns[i].Slug] = campaigns[i].ID
	}

	return ids
}

// watchableCampaigns are the campaigns whose content root this process opened, in
// slug order.
//
// The registry is the authority and this is a filter against it rather than a
// second opinion of its own; see `newContentPipeline` for why the filter has to
// happen before the watcher's own cross-check.
//
// Sorted because a routing table built in whatever order the store returned would
// add watches in a different order on two runs over the same data, and a
// verification report that reorders is one nobody can diff.
func watchableCampaigns(
	campaigns []domain.Campaign,
	roots *content.Registry,
) []domain.Campaign {
	watchable := make([]domain.Campaign, 0, len(campaigns))

	for i := range campaigns {
		if _, err := roots.Get(campaigns[i].Slug); err != nil {
			continue
		}

		watchable = append(watchable, campaigns[i])
	}

	slices.SortFunc(watchable, func(one, other domain.Campaign) int {
		return strings.Compare(one.Slug, other.Slug)
	})

	return watchable
}

// watchRootPaths turns the watchable campaigns into the watcher's routing table
// rows.
//
// `Dir` is `campaigns.content_root`, the column the registrar wrote when the
// campaign was registered, and it is passed down rather than asked of `content.Root`
// for the reason `content.WatchRoot` gives: a `Root` publishes nothing absolute, on
// purpose, and an absolute path is exactly what a prefix routing table is made of.
// The composition root already holds the one absolute path per campaign, so this is
// that value arriving by the only route allowed to name a host path.
//
// Not `filepath.Clean`: the watcher cleans each entry itself and refuses a relative
// one, and a second normalisation here would be a second place for the two to
// disagree about what the same directory is called.
func watchRootPaths(campaigns []domain.Campaign) []content.WatchRoot {
	roots := make([]content.WatchRoot, len(campaigns))

	for i := range campaigns {
		roots[i] = content.WatchRoot{
			CampaignID: campaigns[i].ID,
			Slug:       campaigns[i].Slug,
			Dir:        campaigns[i].ContentRoot,
		}
	}

	return roots
}

// indexFailureDetail is the discriminator `watch.degraded` carries for a campaign
// this process could not index at startup.
//
// "content_root_missing" is separated from everything else because the two have
// different owners and the same symptom. S-4.5's degraded campaign has an operator
// to mount a disk, and any other failure is a fault in the vault or in the store
// underneath it. Collapsing them into one class would send whoever is on call to
// the wrong machine.
//
// The distinction is available because the chain is intact: `ReindexCampaign` wraps
// its walk error with `%w`, and a `Root` whose directory is gone answers
// `fs.ErrNotExist` through `os.Root` rather than with a message this function would
// have to parse.
func indexFailureDetail(err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "content_root_missing"
	}

	return "index_unavailable"
}

// campaignSignalID renders a campaign id for the observability surface, which
// carries it as a string.
//
// A conversion rather than a change of type: a campaign's id is an `int64` because
// the database keys rows by one, and an event line is text. Neither is the wrong
// type; they are two vocabularies for the same id, and this is the only place in
// the process where the two meet.
func campaignSignalID(campaignID int64) string {
	return strconv.FormatInt(campaignID, 10)
}
