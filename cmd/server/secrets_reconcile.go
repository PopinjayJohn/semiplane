package main

// Secret reconciliation, wired.
//
// # Why this file exists, and why it is not part of S7
//
// S7 built `internal/content/reconcile.go` and left the wiring here, because the two
// things it needs — the ledger and the atomic write — live in packages `content`
// depends *inward* of. `internal/content` cannot import `internal/store`, so it
// declares `ReconcileLedger` and `ReconcileWriter` as interfaces and this file is
// where the product satisfies them.
//
// # The obligation no test can check
//
// `ReconcileWriter.WriteIfUnchanged` returning `ErrReconcileConflict` **means nothing
// was written**, and that is the only thing it means. An implementation that writes
// first and compares afterwards has not implemented it, and no test in
// `internal/content` can catch that — the obligation belongs to the implementation.
//
// Which is why `reconcileWriter` reads, compares, and only then writes, in that
// order and with nothing between the comparison and the decision. The comment is
// repeated at the comparison site below rather than only here, because this is
// exactly the kind of contract that gets refactored into a `if err != nil { return
// err }` and one clause shorter.
//
// # Reconciliation runs *after* the indexer
//
// The order in `settledFanOut` is load-bearing and already documented there for the
// hub. Reconciliation joins at the end, for a different reason: it reads the page's
// bytes and decides whether to rewrite one byte of them. If it ran first, the write
// it makes would produce a settled change, which would arrive back here — and the
// indexer would then index the reconciled bytes twice, once per pass.
//
// It does not need to recognise "this event is mine". A successful pass leaves every
// marker as `+`, so the next pass finds nothing pending and returns without writing.
// The convergence is a property of the state, not of a flag, which is why there is no
// re-entrancy guard here.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/store"
)

// pageFileMode is the mode a reconciled page is written with: `0600`, the same value
// the wiki's own write path uses, because this is that path — a caller that wrote
// `0644` here would be the only writer in the product putting a campaign's content
// into a group- or world-readable file.
const pageFileMode fs.FileMode = 0o600

// reconcileLedger satisfies `content.ReconcileLedger` over the store.
//
// **A named type rather than a `*store.Store` passed directly**, because
// `content.ReconcileLedger` is two methods and `*store.Store` has hundreds: passing it
// directly would compile only because the interface is satisfied structurally, and
// nothing would say which two of those hundreds are the contract. Naming the two makes
// the dependency legible at the composition root.
type reconcileLedger struct {
	store *store.Store
}

// Rows reads one page's ledger rows.
//
// `LedgerRows` is `content`'s own exported adapter, so the only translation here is
// `[]domain.SecretReveal` to `[]content.LedgerRow` and it happens in one named
// function that both this and any future caller use.
func (l reconcileLedger) Rows(
	ctx context.Context, campaignID int64, path string,
) ([]content.LedgerRow, error) {
	reveals, err := l.store.SecretsRevealedForPage(ctx, campaignID, path)
	if err != nil {
		return nil, fmt.Errorf("read the ledger for %s: %w", path, err)
	}

	return content.LedgerRows(reveals), nil
}

// MarkReverted counts one sync reversion.
//
// The returned `domain.SecretReveal` is dropped deliberately: the reconciler already
// knows the row it asked about, and reading it back to learn its new count would be a
// second query for a value nothing here uses. `reverted_count` is a **display** bound
// — it makes the sync fight visible rather than mysterious — so a caller that wanted
// the new value would be reading a number for the interface's sake.
func (l reconcileLedger) MarkReverted(
	ctx context.Context, campaignID int64, path, anchor string,
) error {
	if _, err := l.store.MarkSecretReverted(ctx, campaignID, path, anchor); err != nil {
		return fmt.Errorf("count a reversion at %s: %w", path, err)
	}

	return nil
}

// reconcileWriter satisfies `content.ReconcileWriter` over a campaign's content root.
//
// The registry rather than one root, because a `Change` names a campaign by slug and
// resolving it per change is the same lookup every other campaign route does. §5.6.3's
// repair is per page, so the lookup is per page, and a registry is what makes that
// correct rather than a captured root that would reconcile the wrong campaign's file
// if two changed at once.
type reconcileWriter struct {
	roots *content.Registry
}

// WriteIfUnchanged replaces a page's bytes, and only if the page still has the digest
// `want` names.
//
// # The order is the contract
//
// **Read, compare, then write.** The comparison happens before anything is opened for
// writing and before any temporary file exists, so a conflict costs one read and
// leaves the file byte-identical. Writing first and comparing afterwards would leave
// a window in which a sync client's write is overwritten by reconciliation's — and
// that window is a secret becoming public, which is the one outcome S-5.10 says every
// failure path here must avoid.
//
// # Why the re-read is not a race the caller has to worry about
//
// Between the comparison and the rename, the file can change — and that is fine,
// because the rename is the same atomic `WriteFile` the wiki's own save path uses and
// the same race the wiki's `If-Match` has. What this closes is the *long* window: the
// seconds between a page settling and reconciliation reaching it. An `If-Match` that
// spanned a settle would reject almost every write, because the indexer's own write
// lands between them.
func (w reconcileWriter) WriteIfUnchanged(
	ctx context.Context,
	page content.ReconcilePage,
	want content.ReconcilePrecondition,
	body []byte,
) error {
	root, err := w.roots.Get(page.Slug)
	if err != nil {
		return fmt.Errorf("content root for %s: %w", page.Slug, err)
	}

	target, err := root.At(page.Path)
	if err != nil {
		return fmt.Errorf("open %s for reconciliation: %w", page.Path, err)
	}

	// **The comparison, before the write.** Everything above this line is a read or a
	// lookup; everything below it is a write. There is no path through this function
	// that reaches the rename without passing the comparison, and that is the whole
	// of what S-5.10 asks of this method.
	current, err := target.ReadFile()
	if err != nil {
		return fmt.Errorf("read %s to reconcile it: %w", page.Path, err)
	}

	if got := content.PagePrecondition(string(current)); got != want {
		return content.ErrReconcileConflict
	}

	if err := target.WriteFile(ctx, body, pageFileMode); err != nil {
		return fmt.Errorf("write the reconciled %s: %w", page.Path, err)
	}

	return nil
}

// secretReconciler is the `content.ChangeSink` half: one settled change in, one
// reconciliation attempt out.
//
// # Only `OpUpsert`, and only a page with a body
//
// A deletion has no bytes to reconcile, and a rename has already been re-keyed by
// `RenameSecretReveals` — reconciling either would be reading a page that is not the
// one the ledger row names.
//
// # The page is read whole, from the root
//
// **From `os.Root`, never from the index.** The `pages` table's `content_hash` is what
// a precondition is compared against, and the bytes that hash was taken from are the
// bytes on disk; reading the index instead would compare a digest against itself and
// the precondition would never fail — which is a conflict check that cannot conflict.
func secretReconciler(
	roots *content.Registry,
	ledger content.ReconcileLedger,
	logger *slog.Logger,
) content.ChangeSink {
	reconciler := content.NewReconciler(ledger, reconcileWriter{roots: roots}, logger)

	return func(ctx context.Context, change content.Change) {
		if change.Op != content.OpUpsert {
			return
		}

		root, err := roots.Get(change.Slug)
		if err != nil {
			// A slug the watcher resolved whose root is gone is an operator's fault,
			// and it is not reconcilable: there is nothing to reconcile against.
			logger.WarnContext(ctx, "secrets.reconcile_no_root",
				slog.String("campaign", change.Slug),
				slog.String("path", change.Path),
				slog.String("error", err.Error()),
			)

			return
		}

		target, err := root.At(change.Path)
		if err != nil {
			return
		}

		raw, err := target.ReadFile()
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				logger.WarnContext(ctx, "secrets.reconcile_unreadable",
					slog.String("campaign", change.Slug),
					slog.String("path", change.Path),
					slog.String("error", err.Error()),
				)
			}

			return
		}

		report := reconciler.Reconcile(ctx, time.Now(), content.ReconcilePage{
			CampaignID: change.CampaignID,
			Slug:       change.Slug,
			Path:       change.Path,
			Source:     string(raw),
		})

		// `Reconcile` returns a report rather than an error, and the report carries
		// its own: every failure path in it resolves toward hiding, so `report.Err`
		// means nothing was revealed. Logged rather than swallowed, because a
		// campaign quietly failing to repair a sync reversion is a GM whose disclosed
		// secret keeps coming back and nobody has said why.
		//
		// The silence below is the ordinary case and is deliberate: a pass that
		// reverted nothing and reported nothing is what a healthy campaign does
		// between fights, and a log line per settled page would bury the two that
		// matter.
		if report.Err != nil {
			logger.WarnContext(ctx, "secrets.reconcile_failed",
				slog.String("campaign", change.Slug),
				slog.String("path", change.Path),
				slog.String("error", report.Err.Error()),
			)
		}

		if report.Reverted == 0 && report.Err == nil {
			return
		}

		logger.InfoContext(ctx, "secrets.reconciled",
			slog.String("campaign", change.Slug),
			slog.String("path", change.Path),
			slog.Int("reverted", report.Reverted),
			slog.Int("withheld", report.Withheld),
			slog.Int("unresolved", report.Unresolved),
			slog.Int("collisions", report.Collisions),
			slog.Int("capped", capInt(report.Capped)),
			slog.Int("conflicted", capInt(report.Conflicted)),
		)
	}
}

// capInt is `bool` to `int` for a `slog.Int` attribute.
//
// A one-line helper rather than an inline `map[bool]int`, because the inline version
// reads as though the mapping were interesting and it is not — it exists so a boolean
// reaches a structured field that takes an integer, which is what a counter-shaped log
// consumer can index.
func capInt(capped bool) int {
	if capped {
		return 1
	}

	return 0
}

// The counters for `secret.reverted` and `secret.anchor_drift` are **not**
// registered here, and the reason is worth recording.
//
// `observability.NewCounter` is called from inside `internal/observability` — one
// block per subsystem, next to the code that emits — not from the composition root.
// That is the right arrangement and it is also why this file cannot do it: the
// reconciler emits the two events itself, and adding a counter for an event means
// editing the package that owns the event list, which is not this file's path.
//
// So the two events are readable in a log and **invisible in `/readyz`**, and "how
// often is sync fighting this campaign" is a question the registry cannot answer
// until someone adds them. That is a real gap rather than a style note, and it is
// recorded rather than worked around: `secret.reverted` is the single most surprising
// failure this system can have, per that constant's own comment, and it is the one an
// operator most wants to see climbing without reading logs.
//
// An `init()` here would have registered them in one line, and it would also have
// been a second door into the event registry and a violation of the rule that
// registration happens in the composition root — so it is not an option that was
// weighed and declined.
