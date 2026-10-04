package content

// Bounded sync reconciliation: the half of §5.6.2 that converges a page's secret
// markers against the ledger.
//
// # What this is for
//
// Obsidian syncs a copy of the vault that predates a reveal. Periodically it
// writes that copy back, and a `[!secret]+` the GM revealed an hour ago becomes
// `[!secret]-` again — with no error anywhere and a ledger that still says the
// secret was disclosed. S-5.10's answer is that the watcher must notice, diff, and
// re-apply, and that the whole loop must be **bounded** and **fail toward hiding**.
//
// # Every failure path resolves toward hiding, and that is the whole design rule
//
// There are four ways this can go wrong: the budget runs out, the write conflicts,
// the ledger cannot be read or written, and something unexpected happens. All four
// leave the secret collapsed. There is no branch anywhere below that writes a `+`
// because it could not decide whether to, and there is no branch that retries a
// conflict in a loop:
//
//   - **Budget exhausted** → `secret.reconcile_capped` at **error** (S-12.2), no
//     write, the secret stays `-`.
//   - **Write conflict** → no retry, no write, the secret stays `-`, and the next
//     change event comes back through here.
//   - **Ledger unreadable** → nothing is decided at all, so nothing is written.
//   - **A callout that nests another** → refused, because §5.6.2's rule has no
//     exception and `secret.go` already forces such a callout to `SecretCollapsed`
//     whatever its marker byte says, so a `+` written over it would be a byte the
//     scanner disagrees with.
//
// A late reveal costs a GM one more keystroke, or one more sync cycle. An
// accidentally revealed secret is a character in the game's central mystery,
// visible to every player, permanently, in a page body that `curl` can read. The
// asymmetry is not close, and it is the reason every branch above ends the same way.
//
// # No path writes a `+` it could not verify
//
// The only write in this file is guarded by three things, and removing any one of
// them is a disclosure:
//
//   1. **The bytes were read whole**, by the caller, through the campaign's
//      `os.Root`. This package never opens a file.
//   2. **The offsets come from one scan of those exact bytes.** `scanSecrets`
//      produced them; nothing here re-derives a position by searching for text.
//   3. **The write carries the digest of those bytes as its precondition**
//      (`ReconcilePrecondition`). A `ReconcileWriter` that cannot confirm the page
//      still has that digest must return `ErrReconcileConflict` and change
//      nothing — which is `If-Match` (S-6.2) with the file rather than the request,
//      and the same contract `internal/httpapi/secrets` holds for a GM's reveal.
//
// # Determinism, and the clock
//
// No function here reads a clock. `Reconcile` takes `now` as an argument and the
// only thing it decides with it is the pass budget — so a test drives the window by
// passing times, and the decision logic is a pure function of (page, rows, now).
// `TestReconcileReachesNoClockForItself` walks this file's AST to hold it, because
// "it was written without a clock" is exactly the claim that erodes one innocuous
// `time.Now()` at a time.
//
// Ordering is total: `Reassociate` returns rows in the order it was given them (the
// store reads them `ORDER BY anchor`), the reversals are collected through a map
// keyed by ordinal and then **sorted by that ordinal**, and the spliced output is a
// single forward pass over offsets that were themselves sorted. There is no map
// iteration anywhere a result depends on.
//
// # What it does not do
//
//   - It does not read the filesystem. The caller has the bytes; see the wiring
//     note on `Reconcile`.
//   - It does not re-key a stale derived anchor. `anchor.go` says "the
//     reconciliation already does" a `Repair` pass, and there is no `Repair` in this
//     package and no store method that would rewrite `secrets_revealed.anchor`. So a
//     row that `Reassociate` had to repair by ordinal keeps its stored anchor, is
//     repaired again on the next pass, and is reported every time. That is
//     idempotent and safe; it is a gap in the *repair*, not in the safety.
//   - It does not touch `OpRemove` or `OpRename`. A removed page has nothing to
//     reconcile and a renamed page's bytes did not change.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/observability"
)

// S-5.10's two numbers, and they are constants rather than configuration for the
// reason ADR 0011 gives: a cap that can be raised by an environment variable is a
// cap nobody reviews.
//
// `ReconcilePassLimit` is the number of passes **per file per minute** and it is
// published in three places already — S-5.10, §5.6.2, and
// `domain.SecretReveal.RevertedCount`'s own comment, which calls this the security
// bound on how fast a counter can climb. Three is therefore what ships, and
// "capped at 3" means *three writes land and the fourth is refused*, which is what
// `TestTheCapRefusesTheFourthPassOnTheSameFile` holds.
const (
	// ReconcilePassLimit is how many reconciliation passes one file may have in
	// ReconcilePassWindow.
	ReconcilePassLimit = 3

	// ReconcilePassWindow is that window. A rolling window rather than an aligned
	// one, because an aligned window lets a file have six passes in the twelve
	// seconds either side of a boundary and the cap's whole job is to bound the
	// rate this subsystem writes at.
	ReconcilePassWindow = time.Minute
)

// ErrReconcileConflict is a page that changed between the read and the write.
//
// A writer returns it, wrapped or not, when the precondition did not hold, and it
// **must have changed nothing**. It is the file-shaped sibling of `edit`'s 412 and
// `secrets`' 412, and it is the reason §5.6.2 step 4 says to back off rather than
// retry: the next event re-enters `Reconcile` with fresh bytes and fresh offsets,
// and a retry here would be a retry on offsets read from a file that has since
// changed.
var ErrReconcileConflict = errors.New("content: the page changed since it was read")

// ReconcilePage is one page as the caller read it.
//
// **The bytes are the caller's, not this package's.** A reconciler that opened the
// file itself would read it a second time and then write against the first read's
// offsets, which is the exact window `ReconcilePrecondition` exists to refuse. So
// the source arrives here and the whole decision is made about those bytes.
type ReconcilePage struct {
	// CampaignID is the campaign whose root the path is relative to, and the key
	// both the ledger and the derived anchor hash over.
	CampaignID int64

	// Slug is that campaign's slug. Carried for the same reason `Change.Slug` is:
	// for a log line, never for authorisation and never as a capability.
	Slug string

	// Path is the page's path relative to the campaign's content root, spelled
	// exactly as `pages.path` spells it — the ledger's own key half.
	Path string

	// Source is the page's whole byte sequence as the caller read it, front matter
	// included.
	//
	// **A local, and it never leaves the process.** It is read, scanned, spliced and
	// handed back to the writer; it reaches no event, no error and no report field
	// (S-12.3), and `ReconcileReport` has no field a page body could be written
	// into.
	Source string
}

// ReconcileLedger is the two ledger capabilities reconciliation needs, and nothing
// else.
//
// `internal/store` is not imported from here — `content` depends inward on
// `domain`, never on the package that owns SQL — so the two are declared here and
// `*store.Store`'s `SecretsRevealedForPage` and `MarkSecretReverted` are satisfied
// through `LedgerRows` (see the report on what the integrator wires).
//
// Two methods rather than one "sync the ledger" method, because the two have
// different failure directions and collapsing them would hide which one failed: a
// read that fails means **nothing is decided**, so nothing is written, while a write
// that fails means the byte is already public and the counter is a display value.
type ReconcileLedger interface {
	// Rows lists one page's ledger rows.
	//
	// An error is not a fault to retry inside this call: the reconciler treats it
	// as "nothing is known", writes nothing, and returns it on the report.
	Rows(ctx context.Context, campaignID int64, path string) ([]LedgerRow, error)

	// MarkReverted records that sync overwrote a `+` back to `-` and that it has
	// been re-applied.
	//
	// Called **once per reversion, and only after the byte is on disk**. `store`'s
	// own `MarkSecretReverted` saturates at `domain.MaxRevertedCount`, so a double
	// call is visible only as a count that is one too high — which is why
	// `TestOneReversionIncrementsTheLedgerExactlyOnce` counts the calls rather than
	// reading the counter.
	//
	// `anchor` is the row's **stored** anchor, not the one `Reassociate` recomputed.
	// The database still holds the former, and the increment is an `UPDATE ... WHERE
	// anchor = ?`; see the "does not re-key" note in the file header.
	MarkReverted(ctx context.Context, campaignID int64, path, anchor string) error
}

// LedgerRows adapts the store's rows to the resolver's local type.
//
// It exists because `anchor.go` argues for `content` owning `LedgerRow` rather than
// reading `domain.SecretReveal`, and that argument is right — the resolver is in
// this package and the ledger row is a *position* question as much as a key one.
// The cost of being right is one conversion, and this is it, so that the
// integrator's adapter is a single line rather than a twelve-line loop that has to
// be right about `OrdinalKnown`.
//
// **`Revealed: true` for every row, unconditionally, and that is a fact rather
// than an assumption.** `secrets_revealed` has no revealed column: a row's existence
// *is* a disclosure in force, and `UnrevealSecret` deletes the row rather than
// clearing a flag. A caller that wants a row which is not revealed does not have
// one.
func LedgerRows(reveals []domain.SecretReveal) []LedgerRow {
	if len(reveals) == 0 {
		return nil
	}

	rows := make([]LedgerRow, 0, len(reveals))

	for _, reveal := range reveals {
		rows = append(rows, LedgerRow{
			Path:         reveal.Path,
			Anchor:       reveal.Anchor,
			Ordinal:      reveal.Ordinal,
			OrdinalKnown: reveal.OrdinalKnown,
			Revealed:     true,
		})
	}

	return rows
}

// ReconcileWriter is the conditional atomic write, and its one obligation.
//
// A reconciler cannot take a write lock — Obsidian is a separate process that has
// never heard of this server — so the guarantee is a compare-and-swap on the
// digest of what was read, which is what `If-Match` is for everywhere else in this
// project (S-6.2). An implementation is `content.Target.WriteFile` guarded by a
// read of the page's current digest.
type ReconcileWriter interface {
	// WriteIfUnchanged replaces the page's bytes, and only if the page still has
	// the digest `want` names.
	//
	// **Returning `ErrReconcileConflict` means nothing was written**, and that is
	// the only thing it means. An implementation that writes first and compares
	// afterwards has not implemented this, and no test in this package can catch it
	// — the obligation is the implementation's, and it is stated here because it is
	// the one line of this contract that a disclosure depends on.
	//
	// Any other error is a write that did not happen. `content.Target.WriteFile`
	// documents the distinction this interface inherits: an error *after* the
	// rename means durability unknown, not not-written, which is why the reconciler
	// does not mark anything reverted after a failure.
	WriteIfUnchanged(
		ctx context.Context,
		page ReconcilePage,
		want ReconcilePrecondition,
		body []byte,
	) error
}

// ReconcilePrecondition is the digest a page must still have for a write to land.
//
// **Unsalting it is correct here and would be wrong on the wire.** ADR 0016 salts
// the wiki's `ETag` with `include_secrets` because one URL has two legitimate
// answers and a cache must not hand the GM's to a player. There is exactly one
// answer here — the reconciler writes for every reader — and the value never leaves
// the process: it is the same unsalted hex SHA-256 that `pages.content_hash` holds
// and that `internal/httpapi/edit` puts in a 412, so a writer can compare against
// what the indexer already recorded rather than recomputing a second digest over
// the same bytes.
type ReconcilePrecondition string

// String renders the digest as it is stored, without quotes.
func (want ReconcilePrecondition) String() string { return string(want) }

// PagePrecondition is the precondition for writing over `source`: the digest of
// exactly those bytes.
//
// It cannot fail and it takes no parameters that could make it wrong, which is why
// it is a function rather than something `Reconcile` computes inline: a caller that
// had to assemble the digest itself would be a second implementation of
// `contentHash`, and a second one is a second answer to "what version is this
// page".
func PagePrecondition(source string) ReconcilePrecondition {
	return ReconcilePrecondition(contentHash([]byte(source)))
}

// Reconciler converges one page's markers against the ledger.
//
// Constructed by `NewReconciler` and immutable afterwards, so a reconciler in
// flight cannot have its budget or its ledger swapped underneath it. The value
// receiver is deliberate: every field is a pointer or an interface, so a copy
// shares the budget and the two collaborators.
type Reconciler struct {
	ledger ReconcileLedger
	writer ReconcileWriter
	budget *passLog
	logger *slog.Logger
}

// NewReconciler returns a Reconciler with S-5.10's budget.
//
// There is no way to build one without a budget, and that is the point: a
// reconciler whose cap is a nil pointer is an uncapped one, and "unlimited
// re-application against a sync client that is winning" is a hot loop writing to a
// vault forever. The limit is `ReconcilePassLimit` and it is not a parameter.
func NewReconciler(
	ledger ReconcileLedger,
	writer ReconcileWriter,
	logger *slog.Logger,
) Reconciler {
	return Reconciler{
		ledger: ledger,
		writer: writer,
		budget: newPassLog(ReconcilePassLimit, ReconcilePassWindow),
		logger: logger,
	}
}

// Reconcile converges one page and reports what it did.
//
// # What the caller owes it
//
// The bytes, read **whole** and **through the campaign's `os.Root`** (S-3.5), and a
// `now` this caller chose. The wiring that satisfies both is a `ChangeSink` wrapped
// around the indexer's own, invoked for `OpUpsert` only: read the page, index it,
// then reconcile it. Running reconciliation **after** the index rather than before
// means the write this may perform produces its own settled change, and the index
// converges through that one rather than having two writes land on the same bytes.
//
// # It is a fixed point
//
// A successful pass puts every candidate back to `+`, which is what the ledger
// already said, so the next event finds nothing to do and consumes no budget. That
// is what stops reconciliation's own write from becoming the next fight:
// `TestASecondPassOverAReconciledPageWritesNothing` holds it, and it is the property
// the wiring depends on — nothing anywhere needs to recognise "this event is
// mine".
func (r Reconciler) Reconcile(
	ctx context.Context,
	now time.Time,
	page ReconcilePage,
) ReconcileReport {
	report := ReconcileReport{CampaignID: page.CampaignID, Path: page.Path}

	rows, err := r.ledger.Rows(ctx, page.CampaignID, page.Path)
	if err != nil {
		// Nothing is known, so nothing is decided, so nothing is written.
		report.Err = fmt.Errorf("read the secret ledger for %s: %w", page.Path, err)

		return report
	}

	resolutions := Reassociate(page.CampaignID, page.Path, rows, page.Source)

	if drift := ReportDrift(resolutions); drift.Unresolved > 0 {
		report.Unresolved = drift.Unresolved
		r.reportDrift(ctx, page, drift.Unresolved)
	}

	secrets := ScanSecrets(page.Source)
	reversals, collisions := reversalCandidates(secrets, resolutions)

	report.Collisions = collisions

	// `Withheld` counts from here on, so `Reverted + Withheld` is always the
	// number of reversals the ledger asked for — a refusal decided before the write
	// is withheld here, and one decided at it is added below. A separate slice
	// rather than a filter in place, because `reversals` is in ordinal order and the
	// offsets that order comes from must survive into `spliceMarkers` unrenumbered.
	var pending []reversal

	for _, rev := range reversals {
		if rev.refused != withheldNever {
			report.Withheld++

			continue
		}

		pending = append(pending, rev)
	}

	if len(pending) == 0 {
		return report
	}

	if !r.budget.allowed(now, page.CampaignID, page.Path) {
		report.Withheld += len(pending)
		report.Capped = true

		r.reportCapped(ctx, page, len(pending))

		return report
	}

	// **The write, then the ledger, and never the other way round.** §5.6.2's
	// precedence table puts "is it revealed right now?" on the file, so a ledger
	// row with no `+` behind it is a GM looking at a hidden secret and a log line;
	// the other order records a disclosure that has not happened, and the whole
	// point of the ledger is that it can re-apply a real one.
	if err := r.writer.WriteIfUnchanged(
		ctx,
		page,
		PagePrecondition(page.Source),
		[]byte(spliceMarkers(page.Source, secrets, pending)),
	); err != nil {
		report.Withheld += len(pending)

		if errors.Is(err, ErrReconcileConflict) {
			// **No retry.** Not a bounded one, not one attempt, not one. The next
			// settled change re-enters this function with bytes and offsets read
			// after the conflict, and a retry here would splice against offsets
			// that have already been invalidated — which is how a re-application
			// becomes a rewrite of somebody's prose.
			report.Conflicted = true

			return report
		}

		report.Err = fmt.Errorf("re-apply a revealed marker on %s: %w", page.Path, err)

		return report
	}

	report.Reverted = len(pending)

	marked := 0

	var failures []error

	for _, rev := range pending {
		if err := r.ledger.MarkReverted(
			ctx, page.CampaignID, page.Path, rev.anchor,
		); err != nil {
			failures = append(failures,
				fmt.Errorf("count the reversion on %s: %w", page.Path, err))

			continue
		}

		marked++
	}

	if len(failures) > 0 {
		// Every row is attempted even after one fails: the bytes are already
		// public for all of them, so stopping would leave the rest of a page's
		// fight invisible in the ledger. `Reverted` is not reduced — the markers
		// really are on disk — and the error is what says the counter disagrees.
		report.Err = errors.Join(failures...)
	}

	if marked > 0 {
		r.reportReverted(ctx, page, marked)
	}

	return report
}

// withheldReason is why a reversal the ledger asked for will not be written.
type withheldReason uint8

const (
	// withheldNever is not withheld: it is a pending reversal.
	withheldNever withheldReason = iota

	// withheldNested is a callout that contains another one. `secret.go` forces
	// such a callout to `SecretCollapsed` "whatever its marker byte says", so a `+`
	// written over it would be a byte the scanner reads as hidden — the state
	// `internal/httpapi/secrets` refuses a GM's reveal for, and for the same reason.
	withheldNested

	// withheldDisagree is two ledger rows claiming one callout, one of which says
	// the disclosure is not in force. There is no answer that satisfies both, and
	// the direction that fails is the one that fails toward hiding.
	withheldDisagree

	// withheldUnnamed is a row whose stored anchor is the empty string.
	//
	// **It should not be reachable and the refusal is still the right one.**
	// `store.RevealSecret` refuses an empty anchor with `ErrInvalidSecretAnchor`, so
	// no row can carry one — but `Reassociate` will happily re-associate such a row
	// by ordinal, because `repairableByOrdinal` reads the *position* and never the
	// key. So this state is reachable through this package's own API with a
	// hand-built `LedgerRow`, and it is not this package's job to decide the store
	// is right.
	//
	// What it cannot do is write the `+`. The row is the only thing that would say
	// which disclosure is in force, and an empty key names every other empty key —
	// so the byte has no verified author, and §5.6.2's rule is that a write with no
	// verified author does not happen. Counting it is refused for the same reason:
	// `MarkReverted` is `UPDATE ... WHERE anchor = ?`, so it would either match
	// nothing or match whichever other row happens to be keyed on the empty string.
	//
	// The cost of this refusal is a reveal that stops converging, which is the
	// direction S-5.10 accepts: a late or dropped reveal, never an accidental one.
	withheldUnnamed
)

// reversal is one secret the ledger says is revealed and the file says is not.
type reversal struct {
	// ordinal is the callout's position among the page's secrets, and the index
	// into the `ScanSecrets` result every offset comes from.
	ordinal int

	// anchor is the **stored** ledger key, which is what `MarkReverted` matches on.
	anchor string

	// refused is non-`withheldNever` when this reversal must not be written.
	refused withheldReason
}

// reversalCandidates decides which secrets the ledger says are revealed and the
// file says are not, and which of those may not be acted on.
//
// # Rows to page, and why the collisions are decided rather than reported
//
// `Reassociate` is deliberately rows-to-page, because a page has one correct answer
// per callout and iterating the other way would silently pick a winner. So two rows
// *can* resolve to one ordinal, and this function is where that is answered. Both
// shapes are handled rather than ignored, because both are disclosure-shaped:
//
//   - **They agree** (both claim a disclosure in force). There is nothing to decide
//     about the byte, and refusing would leave a page whose ledger is malformed
//     permanently unreconciled — a GM watching a reveal they performed vanish,
//     with nothing in the log. One candidate, one write, one counter.
//   - **They disagree** (one claims a disclosure, one does not). `withheldDisagree`.
//     Satisfying one row means disclosing something the other says was not
//     disclosed, and §5.6.2's rule picks which of those two failures is
//     acceptable.
//
// **The anchor of a collided ordinal is the lexicographically smallest**, so the
// answer does not depend on the order the rows arrived in. The store reads them
// `ORDER BY anchor` so today it would not have mattered, and a rule that is correct
// only because of an ordering a caller might not have is a rule one caller away from
// being wrong.
func reversalCandidates(secrets []Secret, resolutions []Resolution) ([]reversal, int) {
	// One accumulator per ordinal, and the candidate list is read back out of the
	// map **sorted** — see the file header. A list built by ranging a map would
	// splice the same bytes in a different order on different runs of the same
	// page.
	claimed := make(map[int]*ordinalClaim, len(resolutions))

	var collisions int

	for _, res := range resolutions {
		// **The bounds check is the unresolved-row guard, and it is here rather than
		// an `OutcomeUnresolved` test because that test is redundant and a redundant
		// guard is one nobody can fail.**
		//
		// `Reassociate` constructs an unresolved `Resolution` with `Ordinal: -1` and
		// only ever assigns a real index on the other two outcomes, so
		// `res.Ordinal < 0` *is* the unresolved test today. The mutation that
		// replaced this with `res.Outcome == OutcomeUnresolved` and dropped the
		// bounds check left every test green, which is the proof that the outcome
		// comparison was doing nothing here — and it was doing nothing *by
		// coincidence of another file's construction*, which is not a property this
		// package should depend on.
		//
		// What is being protected is the disclosure: a row whose anchor names
		// nothing has a reveal whose fate is unknown, and attaching it to whichever
		// callout happened to land in the slot is another GM's secret.
		// `TestAnUnresolvedRowIsReportedAndNothingIsWritten` pins that end to end —
		// it hands in a row whose anchor matches nothing and whose ordinal was never
		// recorded, and requires no write, no counter increment and a drift event.
		if res.Ordinal < 0 || res.Ordinal >= len(secrets) {
			continue
		}

		// **The claim is registered before the marker check, and that order is the
		// whole of `Collisions`.** A second row naming this callout is a collision
		// whether or not the byte needs changing — the report is about the ledger,
		// and "two rows claim one callout" is true whether or not there is anything
		// to write. Registering afterwards would report zero for a page whose ledger
		// is malformed and whose markers happen to be settled, which is the page an
		// operator most wants to hear about.
		claim, seen := claimed[res.Ordinal]
		if !seen {
			claim = &ordinalClaim{}
			claimed[res.Ordinal] = claim
		} else {
			collisions++
		}

		claim.rows++

		if !res.Revealed {
			// A row asserting no disclosure in force cannot ask for a `+`. It is
			// counted and nothing more, and `ordinalClaim.disagrees` turns its
			// presence against a revealed row.
			claim.silent++

			continue
		}

		claim.revealed++

		if secrets[res.Ordinal].State.IsRevealed() {
			// The marker already says what the ledger says. A pass over a settled
			// page is not a pass, and must not cost budget: a GM saving a page four
			// times a minute would otherwise exhaust the cap on an ordinary edit and
			// have the next real sync reversion refused.
			continue
		}

		claim.wants = true
		claim.anchor = smaller(claim.anchor, res.Row.Anchor)
	}

	candidates := make([]reversal, 0, len(claimed))

	for ordinal, claim := range claimed {
		// **`wants` is the question, not `anchor`.** A settled callout has rows and a
		// revealed row and no byte to change; a wanted one with an empty key has the
		// byte to change and no key to change it under. Only the first is a non-event
		// and only the second is a refusal.
		//
		// A settled callout is also not *withheld*, which is right: nothing asked of
		// it. `Reverted + Withheld` is the number of reversals the ledger asked for,
		// and a settled callout was never one.
		if !claim.wants {
			continue
		}

		candidate := reversal{ordinal: ordinal}

		if claim.anchor == nil {
			candidate.refused = withheldUnnamed
		} else {
			candidate.anchor = *claim.anchor
		}

		if hasNestedSecret(secrets, ordinal) {
			candidate.refused = withheldNested
		}

		if claim.disagrees() {
			// **Refusal wins over nested**, and the order is the other way from what
			// the two look like they should be. Both are refusals, so the choice is
			// about the reason a reader gets: "the ledger's rows contradict each
			// other" names the fault that needs a human, and "it nests another
			// secret" is a property of the page that will still be true after the
			// ledger is fixed.
			candidate.refused = withheldDisagree
		}

		candidates = append(candidates, candidate)
	}

	slices.SortFunc(candidates, func(first, second reversal) int {
		return first.ordinal - second.ordinal
	})

	return candidates, collisions
}

// ordinalClaim is what every ledger row that resolved to one callout said about it.
//
// **A structure rather than two integers beside the map**, because the facts are
// read together and there is no reading of any one alone: `rows > 1` counts two rows
// that *agree*, which is the ordinary shape of a duplicated key and needs no special
// answer, and it also counts a revealed row beside a silent one, which is the case
// that must be refused.
type ordinalClaim struct {
	// rows is how many resolved rows named this ordinal at all.
	rows int

	// revealed is how many of them asserted a disclosure in force.
	revealed int

	// silent is how many asserted none.
	silent int

	// wants is whether some revealed row found the marker collapsed and is
	// therefore asking for a `+`.
	//
	// **A flag rather than an inference from `anchor`, and this is the one place in
	// this file where the distinction matters.** `anchor == nil` covers two facts:
	// nothing asked for this ordinal, and something asked and had no usable key to
	// count it against. Reading the first as the second would refuse every settled
	// callout as "unnamed"; reading the second as the first would write a `+` that
	// no row can be held to have asked for. Both are wrong and only a third fact
	// tells them apart.
	wants bool

	// anchor is the stored ledger key to count the reversion against, or nil when
	// no revealed row supplied a usable one.
	//
	// A pointer because "no anchor yet" and "the anchor is the empty string" are
	// different facts, and an empty anchor is the value `SecretAnchor.Empty` calls
	// the single most dangerous one this package can hold.
	anchor *string
}

// disagrees reports whether rows claimed this ordinal and contradict each other
// about whether a disclosure is in force.
//
// Only reachable when a caller hands in a `LedgerRow` with `Revealed: false`, which
// `LedgerRows` never produces: a row's existence in `secrets_revealed` **is** a
// disclosure in force, and `UnrevealSecret` deletes the row rather than clearing a
// flag. The branch is kept because `LedgerRow.Revealed` is this package's own
// vocabulary and a reader will construct a row by hand; it resolves toward hiding
// if that reader is wrong, which is the direction this file exists for.
func (claim *ordinalClaim) disagrees() bool {
	return claim.rows > 1 && claim.revealed > 0 && claim.silent > 0
}

// smaller returns the lexicographically smaller of two ledger keys, or nil when
// neither is a usable name.
//
// **Empty loses to anything, and equal loses to itself**, so the answer does not
// depend on the order the rows arrived in. An empty anchor should not be reachable
// — `Resolve` never returns one and `Reassociate` never repairs onto one — but if
// one arrives, this returns nil and the caller treats the ordinal as having nothing
// to write, which is `SecretAnchor.Empty`'s instruction followed literally: an empty
// name matches every other empty name, so counting against one would attribute a
// GM's reversion to whichever row happened to be keyed on it.
func smaller(incumbent *string, candidate string) *string {
	if candidate == "" {
		return incumbent
	}

	if incumbent == nil || candidate < *incumbent {
		return &candidate
	}

	return incumbent
}

// hasNestedSecret reports whether a callout's body holds another callout.
//
// **Containment, read off one scan, and it is the same relation
// `redact.go`'s `outermost` filters on.** Re-deriving the grammar here would be a
// second answer to "what is nested", and the two would agree until a vault found
// the difference — and this one disagrees with the scanner in the direction that
// writes a `+`.
//
// **It cannot be read off the reported state, and the reason is worth stating**
// because the obvious one-liner does not work. `ScanSecrets` forces a callout
// containing another to `SecretCollapsed` whatever its marker byte says, so a `-` on
// disk and a forced `-` are indistinguishable — and the forced state is a *report*.
// Nesting is a *shape*, and the shape is what this reads.
func hasNestedSecret(secrets []Secret, index int) bool {
	outer := secrets[index]

	for at, candidate := range secrets {
		if at == index {
			continue
		}

		if candidate.BodyStart >= outer.BodyStart && candidate.BodyEnd <= outer.BodyEnd {
			return true
		}
	}

	return false
}

// spliceMarkers returns source with every named secret's marker byte set to `+`.
//
// **One pass over sorted offsets, and each edit is exactly one byte**, so the
// offsets do not move as the result is built. `SetMarker` exists and is the right
// tool for rewriting *one* marker from a fresh scan; this rewrites *several* at
// once from one scan of the bytes the write's precondition covers, which is the
// whole reason it is here — two `SetMarker` calls in sequence would each re-scan,
// and the second would re-derive offsets against a string the first had already
// changed, which is how a one-byte edit becomes a rewrite of prose.
//
// The offsets are sorted rather than assumed ascending. They are, because
// `scanSecrets` assigns ordinals in document order and a parent's header precedes
// its children's — but "they are" is a claim about another file, and the cost of
// being wrong here is a page corrupted in the one place this package promises not
// to touch.
func spliceMarkers(source string, secrets []Secret, reversals []reversal) string {
	offsets := make([]int, 0, len(reversals))

	for _, rev := range reversals {
		offsets = append(offsets, secrets[rev.ordinal].MarkerOffset)
	}

	// **This sort is currently a no-op, and it is here anyway — with a test that
	// says so rather than with a comment that says so.**
	//
	// `reversalCandidates` already returns candidates in ordinal order and
	// `scanSecrets` assigns ordinals in document order, so the offsets arrive
	// sorted. Removing this line is therefore unobservable today, which is
	// exactly the shape of a defensive line that a later reader deletes while
	// making an unrelated change. It stays because the cost of being wrong is a
	// page corrupted in the one place this package promises not to touch, and
	// `TestTheScanAlwaysNamesItsMarkerBytesInAscendingOrder` is what makes the
	// assumption it rests on a held property rather than a belief.
	slices.Sort(offsets)

	var out strings.Builder

	out.Grow(len(source))

	copied := 0

	for _, at := range offsets {
		out.WriteString(source[copied:at])
		out.WriteString(SecretRevealed.String())

		copied = at + 1
	}

	out.WriteString(source[copied:])

	return out.String()
}

// ReconcileReport is what one pass did, in terms an operator can act on.
//
// **`Reverted + Withheld` is always the number of reversals the ledger asked for**,
// so a report cannot claim a convergence it did not achieve and cannot hide one that
// did not happen. `Path` is in it because a log line needs to say which file, and
// nothing derived from the file's bytes is: `Err` is a wrapper over a store or
// filesystem error class, never over the source (S-12.3).
type ReconcileReport struct {
	// CampaignID and Path name the page this report is about.
	CampaignID int64
	Path       string

	// Reverted is how many `+` markers were re-applied to the file.
	Reverted int

	// Withheld is how many reversions the ledger asked for were **left hidden**,
	// whether refused before the write (a nested callout, a disagreement), refused
	// at it (the cap), or lost to it (a conflict).
	Withheld int

	// Unresolved is how many ledger rows `Reassociate` could not place on the page
	// at all. A reveal whose fate is unknown, reported rather than dropped.
	Unresolved int

	// Collisions is how many ordinals two or more ledger rows claimed between them.
	// A fault in the ledger rather than in the page; see `reversalCandidates`.
	Collisions int

	// Capped reports that this pass hit S-5.10's budget and wrote nothing.
	Capped bool

	// Conflicted reports that the page changed between the read and the write, so
	// nothing was written and nothing will be retried until the next event.
	Conflicted bool

	// Err is the first ledger or write failure, or the join of several counter
	// failures. Non-nil means the ledger and the file may disagree.
	Err error
}

// String renders a report for a log line.
//
// A path and five counts. No anchor — an anchor is twelve characters of the
// secret's own first line when it is derived, and S-12.3's rule is that no event
// carries page content.
func (report ReconcileReport) String() string {
	var out strings.Builder

	out.WriteString(report.Path)
	out.WriteString(" reverted=")
	out.WriteString(itoa(int64(report.Reverted)))
	out.WriteString(" withheld=")
	out.WriteString(itoa(int64(report.Withheld)))
	out.WriteString(" unresolved=")
	out.WriteString(itoa(int64(report.Unresolved)))
	out.WriteString(" collisions=")
	out.WriteString(itoa(int64(report.Collisions)))

	if report.Capped {
		out.WriteString(" capped")
	}

	if report.Conflicted {
		out.WriteString(" conflicted")
	}

	return out.String()
}

// reportReverted records that sync's reversion was undone.
//
// One line per pass rather than one per secret, with the count on it:
// `EventAttributes.Count` exists for exactly this, and a page whose sync client is
// fighting it would otherwise produce three log lines a minute forever.
func (r Reconciler) reportReverted(
	ctx context.Context,
	page ReconcilePage,
	count int,
) {
	observability.Event(ctx, r.logger, observability.EventSecretReverted, slog.LevelInfo,
		observability.EventAttributes{
			CampaignID: strconv.FormatInt(page.CampaignID, 10),
			Path:       page.Path,
			Detail:     "reconciled",
			Count:      count,
		},
	)
}

// reportCapped records that the budget ran out and the secret stayed hidden.
//
// **Error, and not a level this call site picks**: S-12.2 names
// `secret.reconcile_capped` as never below error, because the line is the system
// saying it *chose* to hide something and a sync fight is unresolved. An operator
// alerting on "sync is fighting a campaign" needs this to fire, and it would not
// fire at warn.
func (r Reconciler) reportCapped(
	ctx context.Context,
	page ReconcilePage,
	count int,
) {
	observability.Event(ctx, r.logger, observability.EventSecretReconcileCapped,
		slog.LevelError,
		observability.EventAttributes{
			CampaignID: strconv.FormatInt(page.CampaignID, 10),
			Path:       page.Path,
			Detail:     "pass_limit",
			Count:      count,
		},
	)
}

// reportDrift records ledger rows whose anchor names nothing on the page.
//
// At **warn**, and the only one of the three secret events this file emits below
// error. S-5.9 requires the event and says the reveal is never silently dropped;
// it does not claim the page is in trouble, and a single renamed page is an
// ordinary occurrence. `anchor.go` owns the name and this file does not redeclare
// it.
func (r Reconciler) reportDrift(ctx context.Context, page ReconcilePage, count int) {
	observability.Event(ctx, r.logger, AnchorDriftEventName, slog.LevelWarn,
		observability.EventAttributes{
			CampaignID: strconv.FormatInt(page.CampaignID, 10),
			Path:       page.Path,
			Detail:     "unresolved",
			Count:      count,
		},
	)
}

// passLog counts reconciliation passes per file inside a rolling window.
//
// # Why a rolling window
//
// An aligned window — "three passes per minute, aligned to the minute" — admits
// three at 12:00:59 and three more at 12:01:00, so the bound it actually enforces
// is six per minute at a boundary. The whole job of this type is to bound how hard
// this subsystem writes at a vault, and a bound with a 2× hole at every boundary is
// not a bound.
//
// # Why the timestamps and not a counter
//
// Because a counter cannot be reset per file per minute without either a sweep or a
// second key, and both of those are a window's edge that has to be reasoned about.
// A rolling window of at most `limit` timestamps is the same computation with no
// edge: `allowed` drops everything older than `now - window`, and what remains is
// the truth about the last minute.
//
// # Memory
//
// At most `limit` timestamps per file that has had a reversion inside the window, and
// `passLogSweepThreshold` is what stops the map growing to the size of the vault: a
// campaign with a hundred thousand pages would otherwise keep a slice per page for
// ever, because nothing ever revisits a file that has stopped fighting. The sweep
// ranges the map — and it **cannot change an answer**, because it removes only
// entries whose whole window has expired, which is precisely what the next `allowed`
// call for that file would have done anyway.
type passLog struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[passFile][]time.Time
}

// passFile is a pass log's key: a file, in a campaign.
//
// Campaign **and** path, because `pages` is keyed the same way and two campaigns may
// hold the same relative path. A key of path alone would let one campaign's sync
// fight exhaust another's budget — which is not a disclosure, but it is a page
// whose reveal stops converging because a different vault is busy.
type passFile struct {
	campaignID int64
	path       string
}

// passLogSweepThreshold is when the map is swept for expired entries.
//
// A bound rather than a policy: below it the memory is a few hundred slices of
// three or four timestamps, and above it the sweep is a map range that runs on a
// page that just fought with a sync client, which is not a hot path.
const passLogSweepThreshold = 256

// newPassLog returns an empty pass log.
func newPassLog(limit int, window time.Duration) *passLog {
	return &passLog{
		limit:   limit,
		window:  window,
		entries: make(map[passFile][]time.Time),
	}
}

// allowed reports whether this file may take another pass at `now`, and records it.
//
// **An attempt counts, whether or not it succeeded**, and that is the half of the
// cap that is not obvious. The alternative — counting only successful writes —
// leaves a page whose writer always conflicts free to be retried on every event
// forever, which is the fight the cap exists to bound, and it is a *hot loop against
// a vault* rather than three writes a minute. A no-op pass costs nothing: a page
// with nothing to re-apply never reaches here.
//
// The returned bool is the whole decision and the caller may do anything with it,
// which is the trap this function documents rather than removes: `true` means "you
// may write", and nothing was written by asking.
func (p *passLog) allowed(now time.Time, campaignID int64, path string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.limit < 1 {
		// A limit of zero is a configuration fault, and the safe reading of it is
		// "refuse everything": an uncapped reconciler fights forever, and a capped
		// one that hides a secret is a GM pressing reveal again.
		return false
	}

	key := passFile{campaignID: campaignID, path: path}
	cutoff := now.Add(-p.window)

	kept := p.prune(key, cutoff)

	if len(kept) >= p.limit {
		// The window is unchanged on a refused pass, so a caller that keeps asking
		// within the same minute is refused until it ages out — and the next
		// attempt is the one that counts.
		p.entries[key] = kept

		return false
	}

	kept = append(kept, now)
	p.entries[key] = kept

	// **The sweep is a memory bound and no test observes it**, which is stated here
	// rather than papered over with a test that could not fail. Removing this call
	// changes no answer: `prune` drops a file's expired entries on that file's next
	// pass whether or not a sweep ever runs. It is the same shape as
	// `indexOfAnchor`'s empty-value guard, and the same reasoning — a line whose
	// absence is invisible is kept on a stated reason, or deleted.
	//
	// It is kept because the map is keyed by file and nothing ever revisits a page that
	// has stopped fighting, so without it the map's size is the size of the vault: a few
	// hundred bytes per page a sync client once reverted, retained for the process's
	// life. That is a slow leak whose symptom is a server that gets slower for reasons
	// nobody can attribute, and the threshold is what keeps it from being paid on every
	// pass.
	if len(p.entries) > passLogSweepThreshold {
		p.sweep(cutoff)
	}

	return true
}

// prune returns the timestamps inside the window, dropping the entry when none are.
//
// The caller holds the lock.
func (p *passLog) prune(key passFile, cutoff time.Time) []time.Time {
	// **`After` and not `Before`, so the window is half-open**: a pass taken
	// exactly `window` ago is out. `[now-window, now]` would be closed and a
	// campaign whose sync client fights at exactly one pass per window would never
	// recover — the boundary would be reachable and would hold the budget shut
	// forever, which is the failure `TestTheWindowRollsSoAFightCanRecover` pins.
	//
	// `DeleteFunc` rather than a binary search over the sorted prefix, because the
	// sortedness is an assumption about the caller's clock and not a property of
	// this type. `allowed` appends whatever `now` it was handed, so a clock that
	// steps backwards across an NTP correction leaves the slice unsorted — and a
	// binary search over unsorted input silently keeps entries it should have
	// dropped, which would silently extend a cap. The slice is at most
	// `ReconcilePassLimit` long, so the cost of not assuming anything is nil.
	kept := slices.DeleteFunc(p.entries[key],
		func(stamp time.Time) bool { return !stamp.After(cutoff) },
	)

	if len(kept) == 0 {
		delete(p.entries, key)

		return nil
	}

	return kept
}

// sweep drops every entry whose whole window has expired.
//
// The caller holds the lock. It ranges the map and that is deliberate: the entries
// it removes are exactly the ones the next `allowed` for those files would have
// removed, so no answer depends on which ones it reached.
func (p *passLog) sweep(cutoff time.Time) {
	for key, recent := range p.entries {
		if len(recent) > 0 && recent[len(recent)-1].Before(cutoff) {
			delete(p.entries, key)
		}
	}
}
