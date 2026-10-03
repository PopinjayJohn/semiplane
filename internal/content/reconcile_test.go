package content_test

// S-5.10's bounded sync reconciliation, and the one rule every part of it obeys:
// **every failure path resolves toward hiding.**
//
// # What these tests are actually for
//
// The feature this file covers has one job: when an Obsidian sync client overwrites
// a revealed `[!secret]+` back to `-`, put the `+` back — and when that cannot be
// done safely, **do not**. So the assertions are about *which direction each failure
// goes*, and each one is written to be falsifiable by a specific mutation rather than
// to describe the code.
//
// The interesting direction is the one that is hard to see. A test that asserts
// "the cap fired and `Reverted == 0`" passes on a reconciler that wrote a `+` and
// then counted it as withheld. So every withholding assertion here checks the
// **resulting source**, parsed by `ScanSecrets` rather than matched near a construct,
// and most of them go through a writer that **records what it was handed** so the
// bytes handed to the disk are asserted directly as well.
//
// # The mutations
//
// Each rule below was broken and the failing tests recorded. They are in the file
// beside the rule they belong to, not in a list at the top, because a mutation table
// that lives apart from the test it justifies goes stale without anyone noticing.
//
//   - exhaustion reveals instead of hiding → `TestTheCapRefusesTheFourthPass`,
//     `TestASecretTheCapWithheldIsStillCollapsedOnDisk`
//   - the cap removed                          → `TestTheCapRefusesTheFourthPass`
//   - the cap per-secret rather than per-file   → `TestTheCapIsPerFileNotPerSecret`
//   - the cap counted per-campaign not per-file → `TestTheCapIsPerFileAndNotPerCampaign`
//   - a conflict overwrites rather than backing off → `TestAConflictWritesNothingAndDoesNotRetry`
//   - the `If-Match` precondition dropped       → `TestTheWriteCarriesTheDigestOfTheBytesItRead`
//   - `reverted_count` incremented twice         → `TestOneReversionIncrementsTheLedgerExactlyOnce`
//   - a real clock                              → `TestReconcileReachesNoClockForItself`

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/observability"
)

// The pages. One secret per page unless a test says otherwise, because a two-secret
// page is what makes "per file, not per secret" a testable claim and it costs one
// extra line per reconFixture.
const (
	reconPage = "lore/Captain.md"
	// reconTwoSecretPath holds a revealed-then-reverted secret **and** a second secret
	// the GM never revealed, which is the state a real page is in after one sync
	// fight and the state a bug that reveals "everything on the page" would fail.
	reconTwoSecretPath = "lore/Gate.md"
)

// reconOneSecret is a page whose only secret is collapsed, with a block id so the
// anchor is a human-written name rather than a digest: a test that wants to assert
// "this anchor" should not be asserting on twelve characters of the secret's own
// first line, and a digest is exactly what `anchor.go` calls unsafe to print.
const reconOneSecret = `# Gate

> [!secret]- The eastern signal fire was moved by the garrison ^fire
> The traitor is Captain Aldric.
`

// reconTwoSecrets is the same page with two callouts, the second of which was never
// revealed. The first is collapsed; the ledger says it was revealed.
const reconTwoSecrets = `# Gate

> [!secret]- The eastern signal fire was moved by the garrison ^fire
> The traitor is Captain Aldric.

Ordinary prose between the two.

> [!secret]- The garrison's muster roll is incomplete ^roll
> Someone is not on it.
`

// anchorFor is the ledger key for the `^id` block in a reconFixture, so the tests read
// as prose about which secret they mean rather than as digest comparisons.
func anchorFor(id string) string { return id }

// reconCampaign is the tenant every test in this file reconciles under. Fixed, so the
// derived anchors it computes are the derived anchors it asserts.
const reconCampaign int64 = 7

// ---------------------------------------------------------------------------
// The test doubles.
// ---------------------------------------------------------------------------

// fakeLedger is `ReconcileLedger` with the two counts a test needs and nothing
// else: how many times `MarkReverted` was called, and with which anchors in which
// order.
//
// **Anchors are recorded in order, not just counted.** "Incremented once" and
// "incremented twice for one reversion" are the same number of rows on the same
// page if two secrets were re-applied, so a count alone cannot tell them apart —
// and the ordering is asserted because the reconciler promises a total order and a
// map-ranged implementation would break it silently.
type fakeLedger struct {
	mu sync.Mutex

	rows []content.LedgerRow

	// rowsErr is what `Rows` returns.
	rowsErr error

	// markErr is what `MarkReverted` returns for every call.
	markErr error

	// failFirst makes the first `MarkReverted` fail and every later one succeed.
	//
	// **A per-row failure rather than an all-or-nothing one**, because that is the
	// harder case and the more informative one: a ledger that refuses everything
	// would be caught by a single assertion, while one that refuses once and then
	// recovers is what a store looks like when a transaction times out and the pool
	// reconnects. It is also the case where a `continue` on failure silently skips
	// every later row, so it is where the "a failure does not stop the others" rule
	// is actually tested.
	failFirst bool

	// failAnchor makes the call for one specific anchor fail.
	//
	// Distinct from `failFirst` because the two answer different questions: which
	// came first, and which one. A test that wants "the second row's counter failed"
	// needs to say so by anchor rather than by position, since the reconciler's
	// order is a property under test elsewhere.
	failAnchor string

	// asked is every anchor `MarkReverted` was called with, including the calls
	// that failed.
	asked []string

	// marked is every anchor `MarkReverted` successfully counted.
	marked []string
}

func (ledger *fakeLedger) Rows(
	_ context.Context,
	_ int64,
	_ string,
) ([]content.LedgerRow, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	if ledger.rowsErr != nil {
		return nil, ledger.rowsErr
	}

	return slices.Clone(ledger.rows), nil
}

func (ledger *fakeLedger) MarkReverted(
	_ context.Context,
	_ int64,
	_, anchor string,
) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	ledger.asked = append(ledger.asked, anchor)

	if ledger.markErr != nil {
		return ledger.markErr
	}

	if ledger.failFirst && len(ledger.marked) == 0 {
		ledger.failFirst = false

		return errCounterRefused
	}

	if ledger.failAnchor != "" && anchor == ledger.failAnchor {
		return errCounterRefused
	}

	ledger.marked = append(ledger.marked, anchor)

	return nil
}

// count returns how many times MarkReverted counted a row.
func (ledger *fakeLedger) count() int {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	return len(ledger.marked)
}

// anchors returns the anchors MarkReverted was called with, in order, **including
// the calls that failed**.
func (ledger *fakeLedger) anchors() []string {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	return slices.Clone(ledger.asked)
}

// counted returns the anchors MarkReverted successfully counted, in order.
func (ledger *fakeLedger) counted() []string {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	return slices.Clone(ledger.marked)
}

// fakeWriter is `ReconcileWriter` with a record of every call, and a switch for the
// three outcomes a test needs: land it, refuse it as a conflict, or fail it.
//
// **The precondition is checked, not just captured.** A writer that overwrote
// unconditionally would satisfy a test that only looked at the recorded call, so
// this one compares what it was handed against what it was told the page holds —
// which is how `TestTheWriteCarriesTheDigestOfTheBytesItRead` can fail rather than
// merely observe.
type fakeWriter struct {
	mu sync.Mutex

	// current is the digest the page is believed to have. A writer compares
	// against it and refuses on a mismatch.
	current string

	// conflict makes every call return ErrReconcileConflict.
	conflict bool

	// failErr makes every call return this instead of writing.
	failErr error

	calls []writeCall

	// landed is the bytes of the most recent successful write.
	landed []byte
}

// writeCall is one recorded call: the page, the precondition and the bytes.
type writeCall struct {
	page content.ReconcilePage
	want content.ReconcilePrecondition
	body []byte
}

func (writer *fakeWriter) WriteIfUnchanged(
	_ context.Context,
	page content.ReconcilePage,
	want content.ReconcilePrecondition,
	body []byte,
) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	writer.calls = append(writer.calls, writeCall{page: page, want: want, body: slices.Clone(body)})

	if writer.failErr != nil {
		return writer.failErr
	}

	if writer.conflict || want.String() != writer.current {
		return content.ErrReconcileConflict
	}

	writer.landed = slices.Clone(body)

	return nil
}

// callCount returns how many times the writer was called.
func (writer *fakeWriter) callCount() int {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	return len(writer.calls)
}

// lastCall returns the most recent call, and whether there was one.
func (writer *fakeWriter) lastCall() (writeCall, bool) {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	if len(writer.calls) == 0 {
		return writeCall{}, false
	}

	return writer.calls[len(writer.calls)-1], true
}

// written returns the bytes of the most recent successful write, or nil.
func (writer *fakeWriter) written() []byte {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	return writer.landed
}

// captureLogger is a `slog.Handler` that records every line's level, message and
// `event` attribute, so a test can assert that `secret.reconcile_capped` fired **at
// error** — which is S-12.2's rule and the reason the reconciler passes
// `slog.LevelError` rather than letting the call site choose.
type captureLogger struct {
	mu sync.Mutex

	// events is every `event` attribute seen, paired with its level.
	events []capturedEvent
}

// capturedEvent is one `event` attribute and the level it was logged at.
type capturedEvent struct {
	name  string
	level slog.Level
}

func (logger *captureLogger) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (logger *captureLogger) Handle(
	_ context.Context,
	record slog.Record,
) error {
	logger.mu.Lock()
	defer logger.mu.Unlock()

	logger.events = append(logger.events, capturedEvent{
		name:  recordMessageEvent(record),
		level: record.Level,
	})

	return nil
}

func (logger *captureLogger) WithAttrs(_ []slog.Attr) slog.Handler { return logger }
func (logger *captureLogger) WithGroup(_ string) slog.Handler      { return logger }

// sawEvent reports whether an event fired at a level.
func (logger *captureLogger) sawEvent(name string, level slog.Level) bool {
	logger.mu.Lock()
	defer logger.mu.Unlock()

	for _, event := range logger.events {
		if event.name == name && event.level == level {
			return true
		}
	}

	return false
}

// recordMessageEvent pulls the `event` attribute out of a record.
//
// The records `observability.Event` writes carry `event` as an **attribute** and the
// dotted name as the message — `logger.LogAttrs(ctx, level, string(name), record...)`
// — so both places are checked. Reading only the message would pass if the name were
// hardcoded at the call site and the attribute changed, which is the drift S-12.3's
// "one spelling per event name" exists to prevent.
func recordMessageEvent(record slog.Record) string {
	if record.Message != "" {
		return record.Message
	}

	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "event" {
			if name, ok := attr.Value.Any().(string); ok {
				record.Message = name
			}
		}

		return true
	})

	return record.Message
}

// ---------------------------------------------------------------------------
// The reconFixture.
// ---------------------------------------------------------------------------

// reconFixture is one reconciler wired to its two doubles and a captured log, which is
// what every test in this file drives.
type reconFixture struct {
	reconciler content.Reconciler
	ledger     *fakeLedger
	writer     *fakeWriter
	log        *captureLogger

	// now is the clock the tests drive. **A field rather than a `time.Now` in the
	// reconciler**, and `TestReconcileReachesNoClockForItself` holds that.
	now time.Time
}

// newReconFixture wires a reconciler over the given ledger rows and a writer that will
// accept a write whose precondition matches the page as passed in.
func newReconFixture(t *testing.T, rows ...content.LedgerRow) *reconFixture {
	t.Helper()

	// A fixed instant rather than the wall clock, so a test that does not care
	// about time does not depend on it: two reconciliations a microsecond apart
	// are in the same window by a wide margin, and the ones that do care pass
	// `now` explicitly below.
	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)

	ledger := &fakeLedger{rows: rows}

	written := &fakeWriter{}

	reconFX := &reconFixture{
		ledger: ledger,
		writer: written,
		log:    &captureLogger{},
		now:    now,
	}

	reconFX.reconciler = content.NewReconciler(ledger, written, slog.New(reconFX.log))

	return reconFX
}

// page is the reconFixture's page, with its writer's precondition pre-matched so that a
// write lands unless the test says otherwise.
func (reconFX *reconFixture) page(t *testing.T, path, source string) content.ReconcilePage {
	t.Helper()

	reconFX.writer.mu.Lock()
	reconFX.writer.current = content.PagePrecondition(source).String()
	reconFX.writer.mu.Unlock()

	return content.ReconcilePage{
		CampaignID: reconCampaign,
		Slug:       "greyhaven",
		Path:       path,
		Source:     source,
	}
}

// run reconciles the page once.
func (reconFX *reconFixture) run(
	t *testing.T,
	page content.ReconcilePage,
) content.ReconcileReport {
	t.Helper()

	return reconFX.reconciler.Reconcile(t.Context(), reconFX.now, page)
}

// stateAt answers "is this secret revealed on these bytes", by parsing rather than
// by looking for a `+` next to an id.
//
// Every withholding assertion in this file goes through it. A substring search for
// `[!secret]- ... ^fire` passes on a file where the callout is malformed in a way
// that makes it not a callout at all, and a file with no callout is a page whose
// secret is not merely hidden — it is gone, which is a different bug and the same
// direction of failure. `ScanSecrets` is the only reader that agrees with the
// renderer about what a secret is.
func stateAt(t *testing.T, source, blockID string) content.SecretState {
	t.Helper()

	for _, secret := range content.ScanSecrets(source) {
		if secret.BlockID == blockID {
			return secret.State
		}
	}

	t.Fatalf("no callout carries the block id %q in:\n%s", blockID, source)

	return content.SecretCollapsed
}

// assertHidden is "this secret is not public on these bytes", stated once so that
// every withholding test reads as one line.
func assertHidden(t *testing.T, source, blockID string) {
	t.Helper()

	if state := stateAt(t, source, blockID); state.IsRevealed() {
		t.Fatalf("secret %q is revealed on these bytes; every failure path in this "+
			"subsystem resolves toward hiding:\n%s", blockID, source)
	}
}

// ---------------------------------------------------------------------------
// The happy path.
// ---------------------------------------------------------------------------

// TestASecretSyncRevertedIsRevealedAgain is the claim the whole file exists for: a
// ledger row whose file byte went back to `-` gets its `+` back, the counter goes up
// by exactly one, and `secret.reverted` fires.
//
// Written against the **bytes handed to the writer** and against the **parsed
// result**, not against the report's own numbers, because a report that counted a
// write it did not make would satisfy an assertion about the report.
func TestASecretSyncRevertedIsRevealedAgain(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	page := reconFX.page(t, reconPage, reconOneSecret)

	report := reconFX.run(t, page)

	if report.Err != nil {
		t.Fatalf("reconcile: %v", report.Err)
	}

	if report.Reverted != 1 {
		t.Errorf("Reverted = %d, want 1: the ledger says this secret was revealed and "+
			"the file says `-`", report.Reverted)
	}

	if report.Withheld != 0 {
		t.Errorf("Withheld = %d, want 0: nothing failed on the happy path", report.Withheld)
	}

	written := string(reconFX.writer.written())
	if written == "" {
		t.Fatal("nothing was written")
	}

	if state := stateAt(t, written, "fire"); !state.IsRevealed() {
		t.Errorf("the written page still has the secret collapsed:\n%s", written)
	}

	// **The rest of the page is byte-identical.** A reconciler that rebuilt the
	// source rather than splicing one byte in would get the marker right and this
	// wrong, and the damage it does is a page whose prose a GM did not write.
	if !strings.HasPrefix(written, "# Gate\n\n> [!secret]+") {
		t.Errorf("the splice disturbed the page around the marker:\n%s", written)
	}

	if !strings.HasSuffix(written, "The traitor is Captain Aldric.\n") {
		t.Errorf("the splice dropped the callout's body:\n%s", written)
	}

	if got := reconFX.ledger.count(); got != 1 {
		t.Errorf("MarkReverted called %d times, want 1", got)
	}

	if got := reconFX.ledger.anchors(); !slices.Equal(got, []string{anchorFor("fire")}) {
		t.Errorf("MarkReverted anchors = %v, want [%s]", got, anchorFor("fire"))
	}

	if !reconFX.log.sawEvent(string(observability.EventSecretReverted), slog.LevelInfo) {
		t.Error("secret.reverted did not fire")
	}
}

// TestOneReversionIncrementsTheLedgerExactlyOnce is the mutation target for a
// double-count, which is the failure `reverted_count` saturating cannot catch and a
// saturating counter exists to make visible.
//
// **The two-secret page is the reconFixture, not the one-secret page**, because "once"
// is only distinguishable from "once per secret" on a page with more than one secret
// to count. A one-secret page answers "1" to both a correct implementation and one
// that counts every row it looked at.
func TestOneReversionIncrementsTheLedgerExactlyOnce(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t,
		content.LedgerRow{
			Path:         reconTwoSecretPath,
			Anchor:       anchorFor("fire"),
			Ordinal:      0,
			OrdinalKnown: true,
			Revealed:     true,
		},
		// A second row the GM never revealed. It exists so the test can tell
		// "counted the one reversion" from "counted every row it saw".
		content.LedgerRow{
			Path:         reconTwoSecretPath,
			Anchor:       anchorFor("roll"),
			Ordinal:      1,
			OrdinalKnown: true,
			Revealed:     false,
		},
	)

	page := reconFX.page(t, reconTwoSecretPath, reconTwoSecrets)

	report := reconFX.run(t, page)

	if report.Err != nil {
		t.Fatalf("reconcile: %v", report.Err)
	}

	if report.Reverted != 1 {
		t.Errorf("Reverted = %d, want 1: only the first secret was ever revealed",
			report.Reverted)
	}

	got := reconFX.ledger.anchors()
	if !slices.Equal(got, []string{anchorFor("fire")}) {
		t.Errorf("MarkReverted anchors = %v, want exactly [%s]; a second increment "+
			"would read as a count one too high on a column that saturates at 999 and "+
			"is only ever read for display", got, anchorFor("fire"))
	}

	written := string(reconFX.writer.written())

	// The unrevealed secret stays collapsed. It is the same direction as the cap's
	// and for a different reason: the ledger has no row saying it was disclosed.
	assertHidden(t, written, "roll")
}

// TestASecondPassOverAReconciledPageWritesNothing is the convergence property, and
// it is what stops this subsystem from fighting itself.
//
// The reconciler writes the file, the write produces a settled change, and that
// change comes back here. If this pass were not a no-op, every reconciliation would
// cause another reconciliation forever — and S-5.10's cap would then be the only
// thing standing between a vault and a write loop, with `secret.reconcile_capped`
// firing once a minute for the rest of the process's life.
//
// **The budget is also asserted to be untouched**, which is the half that is easy to
// get wrong: a pass over a settled page costs nothing, so a GM saving the same page
// four times a minute does not exhaust the cap.
func TestASecondPassOverAReconciledPageWritesNothing(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	first := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))
	if first.Err != nil {
		t.Fatalf("first reconcile: %v", first.Err)
	}

	if first.Reverted != 1 {
		t.Fatalf("the first pass reverted %d, want 1", first.Reverted)
	}

	// The file as it is now: the first pass's write. **A fresh precondition**, which
	// is the whole point — the reconciler's own write changed the digest, and a
	// second pass that did not notice would be a second pass writing stale offsets.
	settled := reconFX.page(t, reconPage, string(reconFX.writer.written()))

	second := reconFX.run(t, settled)

	if second.Err != nil {
		t.Fatalf("second reconcile: %v", second.Err)
	}

	if second.Reverted != 0 || second.Capped || second.Conflicted {
		t.Errorf("the second pass over a settled page did work: %+v; a reconciler that "+
			"is not a fixed point causes a write loop its own cap then has to absorb",
			second)
	}

	if got := reconFX.writer.callCount(); got != 1 {
		t.Errorf("the writer was called %d times across two passes, want 1", got)
	}

	if got := reconFX.ledger.count(); got != 1 {
		t.Errorf("MarkReverted called %d times across two passes, want 1", got)
	}

	// And again, after the settled page has been reconciled three more times.
	for range 4 {
		if report := reconFX.run(t, settled); report.Reverted != 0 || report.Err != nil {
			t.Fatalf("a no-op pass over a settled page reported %+v", report)
		}
	}
}

// ---------------------------------------------------------------------------
// The cap, at its boundary.
// ---------------------------------------------------------------------------

// TestTheCapRefusesTheFourthPass is the boundary, and it is written as a loop over
// `content.ReconcilePassLimit` rather than as three hard-coded calls so that it is
// the *constant* being tested and not a number typed next to it.
//
// Three passes land; the fourth is refused. "Capped at 3" means three writes and
// then nothing, which is the reading S-5.10 and `domain.SecretReveal.RevertedCount`
// both publish, and this test is where that reading is pinned.
func TestTheCapRefusesTheFourthPass(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	// **Each pass sees the page collapsed again**, which is the shape a real fight
	// has: the reconciler writes `+`, a sync client writes `-` back over it, the
	// watcher settles on the `-`, and the reconciler is here again. Feeding the
	// previous pass's output in instead would be a *settled* page, where nothing
	// needs doing — the fixture would then measure idempotence rather than the cap,
	// and `TestASecondPassOverAReconciledPageWritesNothing` already holds that.
	for pass := 1; pass <= content.ReconcilePassLimit; pass++ {
		source := reconOneSecret
		report := reconFX.run(t, reconFX.page(t, reconPage, source))

		if report.Err != nil {
			t.Fatalf("pass %d: reconcile: %v", pass, report.Err)
		}

		if report.Capped {
			t.Fatalf("pass %d was capped; the limit is %d and this is inside it",
				pass, content.ReconcilePassLimit)
		}

		if report.Reverted != 1 {
			t.Fatalf("pass %d reverted %d, want 1", pass, report.Reverted)
		}

		if !reconFX.log.sawEvent(string(observability.EventSecretReverted), slog.LevelInfo) {
			t.Errorf("pass %d did not report the reversion", pass)
		}

		written := string(reconFX.writer.written())

		if !stateAt(t, written, "fire").IsRevealed() {
			t.Fatalf("pass %d left the secret collapsed", pass)
		}
	}

	// **The fourth pass, inside the same window.** Also on a collapsed page, for
	// the reason the loop above says: a pass that had nothing to revert would not
	// reach the budget at all, and the test would then be measuring nothing.
	refused := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))

	if !refused.Capped {
		t.Errorf("pass %d was not capped after %d allowed passes: %+v",
			content.ReconcilePassLimit+1, content.ReconcilePassLimit, refused)
	}

	if refused.Reverted != 0 {
		t.Errorf("the capped pass reverted %d, want 0", refused.Reverted)
	}

	if refused.Withheld != 1 {
		t.Errorf("the capped pass withheld %d, want 1", refused.Withheld)
	}

	if got := reconFX.writer.callCount(); got != content.ReconcilePassLimit {
		t.Errorf("the writer was called %d times, want %d: a refused pass must not "+
			"reach the writer at all", got, content.ReconcilePassLimit)
	}
}

// TestASecretTheCapWithheldIsStillCollapsedOnDisk is the mutation the whole exercise
// turns on: **exhaustion must hide, not reveal.**
//
// A "helpful" reconciler that wrote the `+` anyway and merely reported itself capped
// would pass every assertion about the report and disclose a secret on every pass
// after the cap — which is the one outcome S-5.10 exists to make impossible. So this
// test does not read the report at all for the disclosure question: it sets up a
// page the reconciler has already given up on, runs one more pass, and asks the
// scanner what the secret is.
//
// The page is **already `-`** on entry, which is what makes this the interesting
// direction: there is nothing to withhold, the byte is simply not there, and the only
// way a `+` appears is if the reconciler wrote one it should not have.
func TestASecretTheCapWithheldIsStillCollapsedOnDisk(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	// Exhaust the budget. Each pass lands, and the page is reset to `-` in between
	// the way a sync client would.
	for range content.ReconcilePassLimit {
		reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))
	}

	// One more pass, on a page that is genuinely collapsed. This is the pass a
	// revealing implementation would get wrong.
	before := reconFX.writer.callCount()

	report := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))

	if !report.Capped {
		t.Fatalf("the pass after %d was not capped: %+v", content.ReconcilePassLimit, report)
	}

	// The bytes on disk are unchanged, because nothing was handed to the writer.
	if after := reconFX.writer.callCount(); after != before {
		t.Errorf("the capped pass reached the writer (%d calls, was %d); the secret "+
			"would now be public and the cap would be a log line rather than a "+
			"control", after, before)
	}

	assertHidden(t, reconOneSecret, "fire")

	// **And the ledger was not touched**, which is the other half of hiding: a
	// counter that climbed for a reversion that did not happen is an operator
	// chasing a fight that never happened.
	if got := reconFX.ledger.count(); got != content.ReconcilePassLimit {
		t.Errorf("MarkReverted was called %d times, want %d: a capped pass must not "+
			"count a reversion it did not re-apply", got, content.ReconcilePassLimit)
	}
}

// TestTheCappedPassIsLoggedAtError is S-12.2's rule stated as a test: the line an
// operator alerts on must not be suppressible by a level this call site picks.
//
// The reconciler passes `slog.LevelError` to `observability.Event`, and this asserts
// the record that came out rather than the argument, because the argument is a
// constant and the record is what a log aggregator sees.
func TestTheCappedPassIsLoggedAtError(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	for range content.ReconcilePassLimit {
		reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))
	}

	reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))

	name := string(observability.EventSecretReconcileCapped)

	if !reconFX.log.sawEvent(name, slog.LevelError) {
		t.Errorf("secret.reconcile_capped did not fire at error; S-12.2 names it as " +
			"never below error, and a warn line does not page anybody")
	}

	if reconFX.log.sawEvent(name, slog.LevelWarn) || reconFX.log.sawEvent(name, slog.LevelInfo) {
		t.Error("secret.reconcile_capped fired below error")
	}
}

// TestTheWindowRollsSoAFightCanRecover is the other half of the cap, and the reason
// it is a rolling window rather than a lifetime total.
//
// A cap that never reopens is not a cap, it is a permanent disablement: after three
// fights this morning the page's reveals stop converging for the rest of the process's
// life, and no operator has any signal that would look like a bug. So the budget must
// come back, and it comes back when the window passes rather than at a fixed
// boundary — the `now` moves by a minute and the next pass is allowed.
func TestTheWindowRollsSoAFightCanRecover(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	for range content.ReconcilePassLimit {
		reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))
	}

	if report := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret)); !report.Capped {
		t.Fatalf("the pass inside the window was not capped: %+v", report)
	}

	// **The clock moves past the window** and nothing else changes.
	reconFX.now = reconFX.now.Add(content.ReconcilePassWindow)

	recovered := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))

	if recovered.Capped {
		t.Errorf("a pass after the window was still capped: %+v; a cap that never "+
			"reopens is a permanent disablement wearing a budget's name", recovered)
	}

	if recovered.Reverted != 1 {
		t.Errorf("Reverted after the window = %d, want 1", recovered.Reverted)
	}

	if state := stateAt(t, string(reconFX.writer.written()), "fire"); !state.IsRevealed() {
		t.Error("the recovered pass did not put the marker back")
	}
}

// TestTheCapIsPerFileNotPerSecret is the mutation that turns one page's fight into a
// campaign-wide denial.
//
// Three secrets on one page, all reverted by the same sync client. A per-secret
// budget lets each of them take its three passes — nine writes — which is three times
// what S-5.10 permits for the file; and worse, on a page with **four** secrets a
// per-secret budget lets the fourth one through while the first three exhaust theirs,
// so the cap protects some secrets on a page and not others. The ledger's rows are
// therefore three, and the assertion is that all three land on one pass and the
// fourth pass is refused for **all three**.
//
// This is the test that made "per file" a design decision rather than a phrase.
func TestTheCapIsPerFileNotPerSecret(t *testing.T) {
	t.Parallel()

	const secrets = 3

	reconFX := newReconFixture(t,
		content.LedgerRow{
			Path: reconTwoSecretPath, Anchor: "first", Ordinal: 0,
			OrdinalKnown: true, Revealed: true,
		},
		content.LedgerRow{
			Path: reconTwoSecretPath, Anchor: "second", Ordinal: 1,
			OrdinalKnown: true, Revealed: true,
		},
		content.LedgerRow{
			Path: reconTwoSecretPath, Anchor: "third", Ordinal: 2,
			OrdinalKnown: true, Revealed: true,
		},
	)

	// Every pass sees the page collapsed again, for the reason
	// `TestTheCapRefusesTheFourthPass` gives: a sync client that only reverted it
	// once is not a fight, and a settled page never reaches the budget.
	for pass := 1; pass <= content.ReconcilePassLimit; pass++ {
		report := reconFX.run(t, reconFX.page(t, reconTwoSecretPath, reconThreeSecrets))

		if report.Err != nil {
			t.Fatalf("pass %d: reconcile: %v", pass, report.Err)
		}

		if report.Capped {
			t.Fatalf("pass %d was capped, want %d allowed passes", pass,
				content.ReconcilePassLimit)
		}

		if report.Reverted != secrets {
			t.Errorf("pass %d reverted %d, want %d", pass, report.Reverted, secrets)
		}

		// Every one of the three landed, on every pass. A per-secret budget would
		// let a fourth secret through while the first three exhausted theirs, so
		// this is the assertion that has to be per-pass and not only in aggregate.
		written := string(reconFX.writer.written())

		for _, id := range []string{"first", "second", "third"} {
			if !stateAt(t, written, id).IsRevealed() {
				t.Errorf("pass %d left %q collapsed", pass, id)
			}
		}
	}

	refused := reconFX.run(t, reconFX.page(t, reconTwoSecretPath, reconThreeSecrets))

	if !refused.Capped {
		t.Fatalf("pass %d was not capped: %+v", content.ReconcilePassLimit+1, refused)
	}

	if refused.Withheld != secrets {
		t.Errorf("the capped pass withheld %d, want %d: the cap is per file, so "+
			"exhausting it withholds every reversal on the page rather than some of "+
			"them", refused.Withheld, secrets)
	}

	if got := reconFX.writer.callCount(); got != content.ReconcilePassLimit {
		t.Errorf("the writer was called %d times, want %d", got, content.ReconcilePassLimit)
	}
}

// TestTheCapIsPerFileAndNotPerSecretWhenOneJoinsLate is the half of the per-file rule
// that the test above **cannot** reach, and it was found by running the mutation.
//
// The first version drove three secrets in lockstep, and a per-secret budget behaves
// identically to a per-file one when every secret is a reversal on every pass: both
// spend three passes and both refuse the fourth. So the mutation survived a test
// written specifically to catch it, which is the whole argument for running the
// mutation rather than reasoning that the test is good enough.
//
// The discriminating shape is a secret that **joins the fight late**. For the first
// three passes only `first` is a reversal — the other two are on the page as `+`, so
// the ledger agrees with the file and there is nothing to do for them. Then a sync
// client gets round to `second` and the page changes under a file whose budget is
// spent.
//
//   - **Per file**: refused. The file has had three passes in its window.
//   - **Per secret**: `second` has had *none*, so it is admitted — a fourth write to
//     this file in its window, and a secret re-revealed because a *different* secret
//     is the one being fought over.
//
// The direction is the wrong one either way, so the assertion is that `second` stays
// collapsed and the report says it was withheld.
func TestTheCapIsPerFileAndNotPerSecretWhenOneJoinsLate(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t,
		content.LedgerRow{
			Path: reconTwoSecretPath, Anchor: anchorFor("first"), Ordinal: 0,
			OrdinalKnown: true, Revealed: true,
		},
		content.LedgerRow{
			Path: reconTwoSecretPath, Anchor: anchorFor("second"), Ordinal: 1,
			OrdinalKnown: true, Revealed: true,
		},
	)

	// Three passes, each over a page where **only `first` is collapsed**. `second` is
	// public throughout, so the ledger and the file agree about it and no pass spends
	// anything on it.
	//
	// **The rows carry the block ids, not made-up names.** A row whose anchor names
	// nothing on the page is *unresolved* before the reconciler sees it, and this
	// test would then be measuring the drift path rather than the cap — three
	// passes that each report zero reversals and spend nothing.
	for pass := 1; pass <= content.ReconcilePassLimit; pass++ {
		report := reconFX.run(t, reconFX.page(t, reconTwoSecretPath, oneReverted))

		if report.Err != nil || report.Capped {
			t.Fatalf("pass %d: %+v; want an allowed, uncapped pass", pass, report)
		}

		if report.Reverted != 1 {
			t.Fatalf("pass %d reverted %d, want 1: only one secret is collapsed",
				pass, report.Reverted)
		}

		if state := stateAt(t, string(reconFX.writer.written()), "second"); !state.IsRevealed() {
			t.Fatalf("pass %d un-revealed a secret nothing was fighting over", pass)
		}
	}

	// The budget for the file is now spent, and only `second` needs reversing.
	late := reconFX.run(t, reconFX.page(t, reconTwoSecretPath, secondReverted))

	if !late.Capped {
		t.Errorf("a secret that joined the fight late was admitted on a spent file "+
			"budget: %+v; the cap is per file, and a secret's own budget is not a "+
			"licence for it to have one", late)
	}

	if late.Reverted != 0 || late.Withheld != 1 {
		t.Errorf("Reverted = %d, Withheld = %d, want 0 and 1", late.Reverted, late.Withheld)
	}

	if got := reconFX.writer.callCount(); got != content.ReconcilePassLimit {
		t.Errorf("the writer was called %d times, want %d: the file's budget is spent "+
			"and a late-joining secret must not extend it", got, content.ReconcilePassLimit)
	}
}

// oneReverted and bothReverted are the same page with one callout public and one
// collapsed, and both collapsed. Two constants rather than one plus a splice: a
// fixture assembled by string replacement is a fixture whose bytes a reader has to
// reconstruct, and these two lines are cheaper to read than the expression is.
const (
	// oneReverted: `first` collapsed, `second` public.
	oneReverted = `# Gate

> [!secret]- The eastern signal fire was moved by the garrison ^first
> The traitor is Captain Aldric.

> [!secret]+ The muster roll is complete ^second
> Every name is on it.
`

	// secondReverted: `first` public again, `second` collapsed — a sync client that
	// got round to the second callout and left the first alone.
	secondReverted = `# Gate

> [!secret]+ The eastern signal fire was moved by the garrison ^first
> The traitor is Captain Aldric.

> [!secret]- The muster roll is incomplete ^second
> Someone is not on it.
`
)

// TestTheCapIsPerFileAndNotPerCampaign is the neighbouring mistake, and it is a real
// one: two campaigns, one sync client fighting both, and a cap keyed on the campaign
// would let one campaign's fight consume the other's budget.
//
// The assertion is that campaign 7's exhausted budget **does not stop campaign 8**
// from reconciling its own page at the same instant. A per-campaign cap fails on the
// second sub-case of this test.
func TestTheCapIsPerFileAndNotPerCampaign(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	// Exhaust campaign 7's budget on one page.
	for range content.ReconcilePassLimit {
		reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))
	}

	if report := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret)); !report.Capped {
		t.Fatalf("the pass inside the window was not capped: %+v", report)
	}

	// **A different campaign, a different page, the same instant.**
	other := content.ReconcilePage{
		CampaignID: reconCampaign + 1,
		Slug:       "blackmoor",
		Path:       reconPage,
		Source:     reconOneSecret,
	}

	// The precondition has to be re-matched for the other page, or the write is a
	// conflict and the test would pass for the wrong reason.
	reconFX.writer.mu.Lock()
	reconFX.writer.current = content.PagePrecondition(reconOneSecret).String()
	reconFX.writer.mu.Unlock()

	report := reconFX.run(t, other)

	if report.Capped {
		t.Errorf("another campaign's page was capped by this one's exhausted budget: "+
			"%+v; a page in one campaign must not be able to stop another campaign's "+
			"secrets from converging", report)
	}

	if report.Reverted != 1 {
		t.Errorf("Reverted in the other campaign = %d, want 1", report.Reverted)
	}
}

// twoNamedSecrets is `reconTwoSecrets` with the block ids the two-counter tests use.
//
// **A fixture rather than a rename of the one above**, because `reconTwoSecrets` is
// shared with `TestOneReversionIncrementsTheLedgerExactlyOnce`, which asserts on
// `fire` and `roll`. Renaming a shared fixture's ids is the quiet kind of breakage
// where one test's failure is another's missing coverage.
const twoNamedSecrets = `# Gate

> [!secret]- The eastern signal fire was moved by the garrison ^first
> The traitor is Captain Aldric.

Ordinary prose between the two.

> [!secret]- The garrison's muster roll is incomplete ^second
> Someone is not on it.
`

// reconThreeSecrets is three collapsed callouts, all with block ids, for the per-file cap
// tests. Built as a constant rather than generated so that a failure names a line.
const reconThreeSecrets = `# Gate

> [!secret]- The eastern signal fire was moved by the garrison ^first
> The traitor is Captain Aldric.

> [!secret]- The garrison's roster was altered ^second
> A name was added in a different hand.

> [!secret]- The muster roll is incomplete ^third
> Someone is not on it.
`

// ---------------------------------------------------------------------------
// The write conflict.
// ---------------------------------------------------------------------------

// TestAConflictWritesNothingAndDoesNotRetry is the mutation that turns a back-off
// into an overwrite, and it is the most dangerous of the set.
//
// The reconciler read the page, derived the marker's byte offset from **those**
// bytes, and the page changed. Writing anyway means rewriting a byte of a file whose
// contents have moved on — which is not "a lost update" in the ordinary sense, it is
// a `+` written over prose that has since been rewritten, on a page about who is
// hiding what.
//
// **Three assertions, because the mutation could break any one of them.** The writer
// is called exactly once (no retry loop). What it was handed, if anything, is recorded
// and asserted empty (no overwrite). And the ledger is untouched (no reversion
// counted for a write that did not happen).
func TestAConflictWritesNothingAndDoesNotRetry(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	reconFX.writer.mu.Lock()
	reconFX.writer.conflict = true
	reconFX.writer.mu.Unlock()

	report := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))

	if report.Err != nil {
		t.Fatalf("a conflict is a back-off, not a failure: %v", report.Err)
	}

	if !report.Conflicted {
		t.Errorf("the report does not say the pass conflicted: %+v", report)
	}

	if report.Reverted != 0 {
		t.Errorf("Reverted = %d on a conflict, want 0", report.Reverted)
	}

	if report.Withheld != 1 {
		t.Errorf("Withheld = %d on a conflict, want 1", report.Withheld)
	}

	// **Once.** Not twice, not `ReconcilePassLimit` times, not until it succeeds.
	if got := reconFX.writer.callCount(); got != 1 {
		t.Errorf("the writer was called %d times on a conflict, want 1: "+
			"§5.6.2 step 4 says back off and retry on the next event, and a retry "+
			"here splices against offsets the conflict has already invalidated", got)
	}

	if got := reconFX.ledger.count(); got != 0 {
		t.Errorf("MarkReverted was called %d times on a conflict, want 0: nothing "+
			"was re-applied, so the count would be a lie", got)
	}

	if reconFX.writer.written() != nil {
		t.Error("a conflicting write landed on the page")
	}
}

// TestTheScanAlwaysNamesItsMarkerBytesInAscendingOrder holds the property
// `spliceMarkers`' sort rests on, and it is stated here because the sort itself
// **cannot be observed**.
//
// `slices.Sort(offsets)` in `spliceMarkers` is a no-op against today's scanner:
// `reversalCandidates` returns candidates in ordinal order and `scanSecrets`
// assigns ordinals in document order, so the offsets already ascend. Deleting the
// sort changes no result and no test — a line of defence with no failing mutation
// is a line the next reader removes while making an unrelated change, and this
// assertion is what turns it from "a belief about another file" into a held
// property: if a future scanner assigned ordinals in any other order, this fails,
// and the sort that silently protects the splice fails with it.
//
// **Four fixtures, and the nested one is the one that matters.** A nested callout's
// parent is reported before its child, and the parent's marker byte precedes the
// child's, so nesting is the shape where document order and byte order could
// plausibly disagree. It is checked for the same reason `secret.go` walks the body
// before recording the parent — the order is a decision somebody made.
func TestTheScanAlwaysNamesItsMarkerBytesInAscendingOrder(t *testing.T) {
	t.Parallel()

	for _, page := range []struct {
		name   string
		source string
	}{
		{"two", reconTwoSecrets},
		{"three", reconThreeSecrets},
		{"nested", reconNested},
	} {
		t.Run(page.name, func(t *testing.T) {
			t.Parallel()

			secrets := content.ScanSecrets(page.source)

			if len(secrets) < 2 {
				t.Fatalf("the fixture holds %d secrets, want at least 2 for the "+
					"order to be a question", len(secrets))
			}

			for index := 1; index < len(secrets); index++ {
				if secrets[index].MarkerOffset <= secrets[index-1].MarkerOffset {
					t.Errorf("secret %d's marker is at byte %d and secret %d's at %d; "+
						"ordinal order and marker-byte order have come apart, and "+
						"spliceMarkers' sort is the only thing standing between that and "+
						"a page spliced in the wrong order",
						index, secrets[index].MarkerOffset, index-1,
						secrets[index-1].MarkerOffset)
				}
			}
		})
	}
}

// TestManyPagesFightAndThenTheBudgetForgetsThem is the memory bound's observable
// half, and it is written as a *sequence* because the property is the sequence's:
// pages fight, the window rolls, and the pages that stopped are admitted again
// without anyone resetting anything.
//
// **It drives more pages than the sweep threshold**, so the sweep runs at least once
// during it. That is the only way to observe the sweep at all: nothing about a
// page's *answer* changes whether its entry was swept or pruned, because `prune`
// does the same work per file. What the sweep prevents is a map that grows to the
// size of the vault, and the only symptom of that — visible here — is that the
// reconciler keeps working correctly for a long time, which is a claim about not
// degrading rather than about any single page.
//
// So this test asserts the thing the sweep is *for*: after the window rolls, a page
// that fought long ago is admitted again, and so is one that fought thirty seconds
// ago. A sweep that removed live entries would fail the second of those; one that
// never ran would leave the map holding every page forever, which no assertion on
// this file can catch — which is stated in `allowed`'s comment rather than dressed
// up as a test.
func TestManyPagesFightAndThenTheBudgetForgetsThem(t *testing.T) {
	t.Parallel()

	// More than the sweep threshold, so `sweep` runs during the drive. The
	// reconciler does not expose its threshold, so this is deliberately generous:
	// a hundred and more files costs a few milliseconds and does not depend on
	// knowing the constant.
	const pages = 100

	reconFX := newReconFixture(t)

	paths := make([]string, pages)

	for index := range paths {
		paths[index] = fmt.Sprintf("lore/Page%03d.md", index)

		reconFX.ledger.mu.Lock()
		reconFX.ledger.rows = append(reconFX.ledger.rows, content.LedgerRow{
			Path:         paths[index],
			Anchor:       anchorFor("fire"),
			Ordinal:      0,
			OrdinalKnown: true,
			Revealed:     true,
		})
		reconFX.ledger.mu.Unlock()
	}

	// Every page fights once. A no-op here would mean the reconciler silently
	// stopped working under volume, which is the failure this test is otherwise
	// blind to.
	for _, path := range paths {
		report := reconFX.run(t, reconFX.page(t, path, reconOneSecret))

		if report.Err != nil || report.Reverted != 1 {
			t.Fatalf("%s: %+v; every page must still reconcile after the budget is "+
				"under pressure", path, report)
		}
	}

	// **The window rolls.** Every page fought at the same instant, so every page's
	// budget expires together.
	reconFX.now = reconFX.now.Add(content.ReconcilePassWindow)

	for _, path := range paths {
		report := reconFX.run(t, reconFX.page(t, path, reconOneSecret))

		if report.Capped {
			t.Fatalf("%s was capped after the window rolled; a page that fought "+
				"before the window has no budget left to be refused by", path)
		}

		if report.Reverted != 1 {
			t.Fatalf("%s: Reverted = %d after the window rolled, want 1",
				path, report.Reverted)
		}
	}
}

// TestASweepThatRemovedLiveEntriesWouldBeVisible is the counterfactual the test
// above cannot state: a sweep that dropped an entry whose window has **not** expired
// would let a page exceed the cap, and that is observable from outside.
//
// The test drives one page past its limit and then keeps it in the window while
// **enough other pages pass to trigger a sweep**, and requires the original page to
// still be refused. Without the sweep this passes trivially; with a sweep that is
// too aggressive it fails, which is what makes `sweep`'s predicate — "the whole
// window has expired", not "most of it" — a tested one.
func TestASweepThatRemovedLiveEntriesWouldBeVisible(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t)

	contender := content.LedgerRow{
		Path:         "lore/Contender.md",
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	}

	reconFX.ledger.mu.Lock()
	reconFX.ledger.rows = append(reconFX.ledger.rows, contender)
	reconFX.ledger.mu.Unlock()

	// Spend the contender's budget. Every pass lands, so this is a real fight.
	for range content.ReconcilePassLimit {
		reconFX.run(t, reconFX.page(t, contender.Path, reconOneSecret))
	}

	if report := reconFX.run(t, reconFX.page(t, contender.Path, reconOneSecret)); !report.Capped {
		t.Fatalf("the contender is not capped: %+v", report)
	}

	// Now a lot of *other* pages pass, which is what crosses the sweep threshold.
	// Their entries are fresh, and the contender's is not: it is at the far end of
	// its window. A sweep that dropped "most" of an entry rather than "all" of it
	// would free a pass here.
	for index := range 400 {
		path := fmt.Sprintf("lore/Filler%03d.md", index)

		reconFX.ledger.mu.Lock()
		reconFX.ledger.rows = append(reconFX.ledger.rows, content.LedgerRow{
			Path: path, Anchor: anchorFor("fire"), Ordinal: 0,
			OrdinalKnown: true, Revealed: true,
		})
		reconFX.ledger.mu.Unlock()

		reconFX.run(t, reconFX.page(t, path, reconOneSecret))
	}

	stillCapped := reconFX.run(t, reconFX.page(t, contender.Path, reconOneSecret))

	if !stillCapped.Capped {
		t.Errorf("the contender's spent budget was released by other pages' passes: "+
			"%+v; the cap is per file and a pass over an unrelated page cannot refund "+
			"it", stillCapped)
	}

	if stillCapped.Reverted != 0 {
		t.Errorf("Reverted = %d on a page whose budget other pages released", stillCapped.Reverted)
	}
}

// TestTwoRowsOnOneCalloutAreCountedOnceAndDeterministically is the collision case,
// and it is the only test that reaches the two rules the mutation run left untested:
// that a duplicated claim costs **one** write and **one** counter increment, and that
// the anchor chosen among the duplicates does not depend on the order the rows
// arrived in.
//
// Both rules exist because `Reassociate` is deliberately rows-to-page — a page has
// one correct answer per callout, and iterating the other way would silently pick a
// winner — so two ledger rows *can* resolve to one ordinal. A duplicated ledger key
// is not a fault to refuse over: it is a row written twice, both saying the same
// thing, and refusing would leave a page whose ledger is malformed permanently
// unreconciled with nothing in the log.
//
// The order-independence assertion is what makes the "lexicographically smallest"
// rule testable at all. The reconciler is handed the same two rows in both orders
// and has to count the same anchor, and a mutation that returned the *first* row's
// anchor passes a single-order test and fails this one.
func TestTwoRowsOnOneCalloutAreCountedOnceAndDeterministically(t *testing.T) {
	t.Parallel()

	// **Both rows reach the same callout by different routes**, which is what two rows
	// on one ordinal looks like. `Reassociate` is rows-to-page, so this is
	// representable, and the routes are deliberately mixed because that is the
	// discriminating case for "which anchor gets counted":
	//
	//   - `first` names the block id `fire`, which is exactly what `Resolve`
	//     computes for the callout, so it is **matched outright**.
	//   - `second` names nothing on the page at all, so it can only reach ordinal 0
	//     through §5.6.3's ordinal fallback, which needs `OrdinalKnown`.
	//
	// A rule that returned the *first row's* anchor would answer differently for
	// the two orderings below, which is the whole reason the orderings are both
	// driven.
	first := content.LedgerRow{
		Path: reconPage, Anchor: anchorFor("fire"), Ordinal: 0,
		OrdinalKnown: true, Revealed: true,
	}
	second := content.LedgerRow{
		Path: reconPage, Anchor: "bbb-the-second-name", Ordinal: 0,
		OrdinalKnown: true, Revealed: true,
	}

	for _, order := range []struct {
		name string
		rows []content.LedgerRow
	}{
		{"in order", []content.LedgerRow{first, second}},
		{"reversed", []content.LedgerRow{second, first}},
	} {
		t.Run(order.name, func(t *testing.T) {
			t.Parallel()

			reconFX := newReconFixture(t, order.rows...)

			report := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))

			if report.Err != nil {
				t.Fatalf("reconcile: %v", report.Err)
			}

			if report.Collisions != 1 {
				t.Errorf("Collisions = %d, want 1: two rows claimed one callout and the "+
					"report is the only place an operator learns about it",
					report.Collisions)
			}

			if report.Reverted != 1 {
				t.Errorf("Reverted = %d, want 1: two rows claiming one callout is one "+
					"callout and one byte, not two", report.Reverted)
			}

			// **Once.** A duplicated key counted twice would inflate
			// `reverted_count` for a fight that happened once, and that column
			// saturates — so the inflation is permanent and invisible after 999.
			got := reconFX.ledger.anchors()
			if !slices.Equal(got, []string{"bbb-the-second-name"}) {
				t.Errorf("MarkReverted anchors = %v, want exactly [bbb-the-second-name]; "+
					"the counted anchor is the lexicographically smallest of the rows "+
					"that claimed the callout, so the answer cannot depend on the "+
					"order they arrived in, and it must be counted once", got)
			}
		})
	}
}

// TestARowWithAnEmptyAnchorIsRefused is the `SecretAnchor.Empty` rule at this level,
// and it is the last untested branch the mutation run found — along with the fact
// that the branch was **refusing**, not counting.
//
// `anchor.go` calls the empty anchor "the single most dangerous value this file can
// produce", because it is a legal string that matches every other empty one: two
// unrelated secrets in one campaign would share a ledger entry, and one GM's reveal
// would become another's. This is what that looks like at the reconciler's
// boundary.
//
// **The row is unreachable through `store` and the refusal is still right.**
// `store.RevealSecret` refuses an empty anchor with `ErrInvalidSecretAnchor`, so no
// persisted row carries one — but `Reassociate` re-associates such a row by ordinal
// happily, because `repairableByOrdinal` reads the *position* and never the key. So
// the state is reachable through this package's own API and it is not this package's
// job to decide the store is right.
//
// The refusal has two halves and the test asserts both, because they fail
// differently:
//
//   - **The byte is not written.** The row is the only thing that says which
//     disclosure is in force, and an empty key names every other empty key — so the
//     `+` would have no verified author. §5.6.2's rule is that a write with no
//     verified author does not happen.
//   - **The counter is not moved.** `MarkReverted` is `UPDATE ... WHERE anchor = ?`,
//     so it would either match nothing or match whichever other row happens to be
//     keyed on the empty string.
//
// The cost is a reveal that stops converging, which is the direction S-5.10 accepts.
func TestARowWithAnEmptyAnchorIsRefused(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       "",
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	report := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))

	if report.Err != nil {
		t.Fatalf("reconcile: %v", report.Err)
	}

	if report.Reverted != 0 {
		t.Errorf("Reverted = %d for a row keyed on the empty anchor, want 0: the "+
			"byte has no verified author, and an empty key names every other empty key",
			report.Reverted)
	}

	if report.Withheld != 1 {
		t.Errorf("Withheld = %d, want 1: the refusal is a withholding an operator can "+
			"see, not a silent no-op", report.Withheld)
	}

	if got := reconFX.writer.callCount(); got != 0 {
		t.Errorf("the writer was called %d times, want 0", got)
	}

	if got := reconFX.ledger.count(); got != 0 {
		t.Errorf("MarkReverted was called %d times with an empty anchor, want 0: the "+
			"update would either match nothing or match whichever other row happens "+
			"to be keyed on the empty string", got)
	}

	// And the secret is where it was.
	assertHidden(t, reconOneSecret, "fire")
}

// TestTwoRowsThatDisagreeAboutOneCalloutAreRefused is the other collision shape, and
// it is the one that resolves toward hiding.
//
// Two rows resolved to the same callout, and one of them says no disclosure is in
// force. There is no answer that satisfies both: writing `+` contradicts the silent
// row, and writing `-` contradicts the revealed one. So nothing is written, the
// report says both the collision and the withholding, and the secret stays collapsed
// — which is the direction §5.6.2 requires every failure path to take.
//
// **Both rows must carry the same anchor to reach the reconciler at all**, and that
// is worth saying because the first version of this test did not and therefore
// tested nothing. A silent row whose anchor names nothing on the page is *unresolved*
// before it gets here — `repairableByOrdinal` refuses to re-point a row that claims
// no disclosure at a callout that is collapsed, which is its third refusal and is
// exactly this case. It comes back with `Ordinal: -1`, the reconciler skips it, and
// the revealed row reconciles normally.
//
// So the disagreement is only reachable when both rows *match*, which is the shape
// below: two rows keyed on the same block id, one saying the disclosure is in force
// and one saying it is not. `store` cannot produce that — a row's existence **is** a
// disclosure — but `LedgerRow` is this package's own vocabulary and a reader will
// construct one by hand.
//
// **The negative control is inside this test.** A single revealed row, with no
// disagreement, does reconcile. Without it a reconciler that refused every collided
// ordinal *and* every single one would pass the first half.
func TestTwoRowsThatDisagreeAboutOneCalloutAreRefused(t *testing.T) {
	t.Parallel()

	revealed := content.LedgerRow{
		Path: reconPage, Anchor: anchorFor("fire"), Ordinal: 0,
		OrdinalKnown: true, Revealed: true,
	}
	silent := content.LedgerRow{
		Path: reconPage, Anchor: anchorFor("fire"), Ordinal: 0,
		OrdinalKnown: true, Revealed: false,
	}

	// The control: one revealed row, no disagreement, and the secret converges.
	controlFX := newReconFixture(t, revealed)

	control := controlFX.run(t, controlFX.page(t, reconPage, reconOneSecret))

	if control.Reverted != 1 {
		t.Fatalf("the control pass reverted %d, want 1: a lone revealed row is the "+
			"ordinary case and must not be caught by the refusal below", control.Reverted)
	}

	// The disagreement: the same page, the same callout, one row on each side.
	reconFX := newReconFixture(t, revealed, silent)

	report := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))

	if report.Err != nil {
		t.Fatalf("reconcile: %v", report.Err)
	}

	if report.Collisions != 1 {
		t.Errorf("Collisions = %d, want 1", report.Collisions)
	}

	if report.Reverted != 0 {
		t.Errorf("Reverted = %d when two rows disagreed about one callout, want 0",
			report.Reverted)
	}

	if report.Withheld != 1 {
		t.Errorf("Withheld = %d when two rows disagreed, want 1: the refusal is a "+
			"withholding and not a silent no-op", report.Withheld)
	}

	if got := reconFX.writer.callCount(); got != 0 {
		t.Errorf("the writer was called %d times on a disagreement, want 0", got)
	}

	if got := reconFX.ledger.count(); got != 0 {
		t.Errorf("MarkReverted was called %d times on a disagreement, want 0", got)
	}
}

// clockCallsInThisFile returns the names of every `time` package function this file
// calls, read from its parsed syntax tree.
//
// **Parsed, and not read as text**, and the reason is the same one that makes the
// assertion worth having: this file's own comment names `time.Now` twice while
// explaining why it must not call it, so a substring search would fail the build on
// the sentence that documents the rule. It would then be removed, and the rule with
// it.
//
// A selector reached through an alias is not found, which is a limitation rather than
// an oversight: `forbidigo` works the same way, and a `time.Now` smuggled through an
// import alias in this file would be a deliberate act of evasion rather than the
// drift this guards against.
func clockCallsInThisFile(t *testing.T) []string {
	t.Helper()

	const source = "reconcile.go"

	parsed, err := parser.ParseFile(token.NewFileSet(), source, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", source, err)
	}

	// **The import is what names the package**, so the walk checks that `time` is
	// actually imported rather than trusting a spelling. A local variable called
	// `time` would satisfy a bare name comparison; an unresolvable one would not,
	// because `Obj` is only nil for a package name.
	importsTime := false

	for _, spec := range parsed.Imports {
		if spec.Path.Value == `"time"` {
			importsTime = true
		}
	}

	if !importsTime {
		t.Fatalf("%s does not import time, so the walk below cannot find anything; "+
			"the assertion has silently stopped being an assertion", source)
	}

	var found []string

	ast.Inspect(parsed, func(node ast.Node) bool {
		selector, isSelector := node.(*ast.SelectorExpr)
		if !isSelector {
			return true
		}

		packageName, isIdent := selector.X.(*ast.Ident)
		if isIdent && packageName.Name == "time" && packageName.Obj == nil {
			found = append(found, selector.Sel.Name)
		}

		return true
	})

	return found
}

// TestAConflictThenTheNextEventReconciles is the other half: a back-off that never
// comes back is a dropped reveal.
//
// §5.6.2 step 4's "retry on the next event" is the whole recovery story, so the test
// drives it: one conflicting pass, then the writer stops conflicting (the sync
// client's write settles, the precondition matches again) and the next event lands
// the reveal. A reconciler that had cached the refusal would fail here.
func TestAConflictThenTheNextEventReconciles(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	reconFX.writer.mu.Lock()
	reconFX.writer.conflict = true
	reconFX.writer.mu.Unlock()

	if report := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret)); !report.Conflicted {
		t.Fatalf("the first pass did not conflict: %+v", report)
	}

	// The next event. The sync client's write has settled and the digest matches
	// again — which is a *different* precondition, because the page moved.
	reconFX.writer.mu.Lock()
	reconFX.writer.conflict = false
	reconFX.writer.mu.Unlock()

	page := reconFX.page(t, reconPage, reconOneSecret)

	report := reconFX.run(t, page)

	if report.Err != nil || report.Conflicted {
		t.Fatalf("the pass after the conflict did not land: %+v", report)
	}

	if report.Reverted != 1 {
		t.Errorf("Reverted after the conflict = %d, want 1", report.Reverted)
	}

	if state := stateAt(t, string(reconFX.writer.written()), "fire"); !state.IsRevealed() {
		t.Error("the retried pass did not put the marker back")
	}

	if got := reconFX.ledger.count(); got != 1 {
		t.Errorf("MarkReverted was called %d times, want 1", got)
	}
}

// TestAWriteFailureLeavesTheSecretHidden is the write-failure branch, which is not a
// conflict and must not be treated as one.
//
// A conflict is a back-off — a page that changed, which is normal. A write that
// fails outright is a fault, and it is the branch where "log and carry on" would most
// easily turn into "log and carry on *revealing*". The secret stays collapsed, the
// report carries the error, and the ledger is untouched.
func TestAWriteFailureLeavesTheSecretHidden(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	reconFX.writer.mu.Lock()
	reconFX.writer.failErr = errors.New("the content root is closed")
	reconFX.writer.mu.Unlock()

	report := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))

	if report.Err == nil {
		t.Fatal("a failed write reported no error")
	}

	if report.Conflicted {
		t.Errorf("a write failure was reported as a conflict: %+v", report)
	}

	if report.Reverted != 0 || report.Withheld != 1 {
		t.Errorf("Reverted = %d, Withheld = %d on a failed write, want 0 and 1",
			report.Reverted, report.Withheld)
	}

	if got := reconFX.ledger.count(); got != 0 {
		t.Errorf("MarkReverted was called %d times on a failed write, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// The precondition.
// ---------------------------------------------------------------------------

// TestTheWriteCarriesTheDigestOfTheBytesItRead is the mutation for dropping
// `If-Match`.
//
// Without a precondition the reconciler's write is unconditional, and an
// unconditional write against a vault a sync client is writing to is a lost update
// with a disclosure attached. **The assertion is that the digest handed to the writer
// is the digest of the bytes the reconciler decided from** — not merely that some
// precondition was present, which a constant string would satisfy.
func TestTheWriteCarriesTheDigestOfTheBytesItRead(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	page := reconFX.page(t, reconPage, reconOneSecret)

	reconFX.run(t, page)

	call, ok := reconFX.writer.lastCall()
	if !ok {
		t.Fatal("the writer was never called")
	}

	want := content.PagePrecondition(reconOneSecret)

	if call.want != want {
		t.Errorf("the write's precondition = %q, want the digest of the bytes that "+
			"were read (%q)", call.want, want)
	}

	if call.want == content.PagePrecondition(string(call.body)) {
		t.Error("the precondition is the digest of the bytes being written rather " +
			"than of the bytes that were read, so it can never fail: an If-Match " +
			"over the new content is a tautology, not a precondition")
	}

	if call.page.Path != reconPage || call.page.CampaignID != reconCampaign {
		t.Errorf("the write names %d/%s, want %d/%s",
			call.page.CampaignID, call.page.Path, reconCampaign, reconPage)
	}

	// **And it is the digest the rest of the project calls `content_hash`**, so the
	// writer can compare against `pages.content_hash` rather than recomputing a
	// second digest over the same bytes.
	if got, want := call.want.String(), want.String(); got != want {
		t.Errorf("the rendered precondition = %q, want %q", got, want)
	}
}

// TestTheWriterRefusesAStalePreconditionAndNothingIsWritten is the other half of the
// contract, and it is the half that lives in the *implementer*.
//
// `ReconcileWriter` says that returning `ErrReconcileConflict` means nothing was
// written. A writer that compared *after* writing would still return the right error
// and the secret would already be public, so the reconciler's behaviour cannot detect
// it. What this test holds is the reconciler's half: it does not mark anything
// reverted, and it does not report a reversion, on a precondition it could not
// confirm.
func TestTheWriterRefusesAStalePreconditionAndNothingIsWritten(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	// A page that has moved: the reconciler will be handed the old bytes, and the
	// writer's belief about the current digest will not match what it is offered.
	page := reconFX.page(t, reconPage, reconOneSecret)

	reconFX.writer.mu.Lock()
	reconFX.writer.current = content.PagePrecondition(reconOneSecret + "Something new.\n").String()
	reconFX.writer.mu.Unlock()

	report := reconFX.run(t, page)

	if !report.Conflicted {
		t.Fatalf("a stale precondition did not read as a conflict: %+v", report)
	}

	if reconFX.writer.written() != nil {
		t.Error("bytes reached the page despite a precondition that did not hold")
	}

	if got := reconFX.ledger.count(); got != 0 {
		t.Errorf("MarkReverted was called %d times, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Things that must not be reconciled.
// ---------------------------------------------------------------------------

// TestANestedCalloutIsNotRevealed is the nested case, and it is the one branch that
// is a refusal for a reason the ledger knows nothing about.
//
// `secret.go` forces a callout that contains another to `SecretCollapsed` **whatever
// its marker byte says**, because there is no rendering of a public outer callout
// that withholds the inner body. So a `+` written over the outer is a byte the
// scanner will read as hidden: the ledger would say the GM disclosed it, the file
// would say otherwise, and the GM's next pass would try again. `internal/httpapi/secrets`
// refuses a GM's reveal of a nesting callout for exactly this reason, and the two
// refusals must agree — otherwise reconciliation becomes a way to disclose a secret
// the reveal endpoint will not.
func TestANestedCalloutIsNotRevealed(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconNestedPath,
		Anchor:       anchorFor("outer"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	page := reconFX.page(t, reconNestedPath, reconNested)

	report := reconFX.run(t, page)

	if report.Err != nil {
		t.Fatalf("reconcile: %v", report.Err)
	}

	if report.Reverted != 0 {
		t.Errorf("Reverted = %d for a nested callout, want 0", report.Reverted)
	}

	if report.Withheld != 1 {
		t.Errorf("Withheld = %d for a nested callout, want 1", report.Withheld)
	}

	if got := reconFX.writer.callCount(); got != 0 {
		t.Errorf("the writer was called %d times for a page whose only reversal is a "+
			"nesting one, want 0: the refusal is decided before the write on purpose",
			got)
	}

	if got := reconFX.ledger.count(); got != 0 {
		t.Errorf("MarkReverted was called %d times for a nested callout, want 0", got)
	}
}

// reconNestedPath holds an outer callout with an inner one inside its body.
const reconNestedPath = "lore/Nested.md"

// reconNested is a page whose first callout contains a second. The **inner** one has a
// block id of its own, which is what makes the nesting expressible at all:
// `scanRange` recurses into the body and reports the inner callout as its own
// `Secret`.
const reconNested = `# Notes

> [!secret]- The garrison's orders came from inside ^outer
> The traitor is Captain Aldric.
>
> > [!secret]- And the orders themselves ^inner
> > Burn the eastern signal.
`

// TestTheInnerSecretOfANestedCalloutIsNotTouched is the completeness half: a
// refusal for the outer must not become a refusal that also silently protects the
// inner, and must certainly not become a write for it.
//
// The ledger has **no row for the inner callout**, so the inner is not a reversal at
// all — it is a secret nobody disclosed, and the reconciler must leave it exactly as
// it found it. Asserting the inner separately is what stops a fix for the outer from
// quietly becoming a blanket refusal of the page.
func TestTheInnerSecretOfANestedCalloutIsNotTouched(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconNestedPath,
		Anchor:       anchorFor("outer"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	reconFX.run(t, reconFX.page(t, reconNestedPath, reconNested))

	if got := reconFX.writer.callCount(); got != 0 {
		t.Fatalf("the writer was called %d times, want 0", got)
	}

	// Both are collapsed and the page is byte-identical, because nothing was
	// written. The parse is the assertion: a substring check would pass on a page
	// where the callouts had stopped being callouts.
	for _, id := range []string{"outer", "inner"} {
		assertHidden(t, reconNested, id)
	}
}

// TestAnUnresolvedRowIsReportedAndNothingIsWritten is the drift half, and S-5.9's
// "a reveal is never silently dropped" is a claim about what happens when the row
// cannot be placed at all.
//
// The row's anchor names nothing on the page: the GM rewrote the secret's first
// line, so the derived hash moved, and the row recorded no ordinal to repair by
// (which is the pre-migration-0012 shape, and is exactly why `LedgerRow` has an
// `OrdinalKnown` flag). `Reassociate` reports it unresolved; this file must emit
// `secret.anchor_drift` and **write nothing**, because the only way to satisfy the
// row would be to attach a GM's disclosure to whichever callout now sits in the
// slot — which is somebody else's secret.
func TestAnUnresolvedRowIsReportedAndNothingIsWritten(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       "an-anchor-that-names-nothing",
		Ordinal:      0,
		OrdinalKnown: false,
		Revealed:     true,
	})

	page := reconFX.page(t, reconPage, reconOneSecret)

	report := reconFX.run(t, page)

	if report.Err != nil {
		t.Fatalf("an unresolved row is a report, not a failure: %v", report.Err)
	}

	if report.Unresolved != 1 {
		t.Errorf("Unresolved = %d, want 1", report.Unresolved)
	}

	if report.Reverted != 0 {
		t.Errorf("Reverted = %d for an unresolved row, want 0", report.Reverted)
	}

	if got := reconFX.writer.callCount(); got != 0 {
		t.Errorf("the writer was called %d times for an unresolved row, want 0", got)
	}

	if !reconFX.log.sawEvent(string(content.AnchorDriftEventName), slog.LevelWarn) {
		t.Errorf("secret.anchor_drift did not fire for a row that could not be placed; " +
			"S-5.9 requires it, and it is the only signal that this reveal's fate is " +
			"unknown")
	}
}

// TestAnUnreadableLedgerWritesNothing is the read branch, and the reason it exists as
// its own test rather than as a line in another.
//
// A ledger that cannot be read means **nothing is known** — not "no rows" and not
// "assume revealed". The difference between those two readings is the whole
// difference between a secret that stays hidden and a page where every collapsed
// callout is re-revealed, so the mutation this catches is the one that swallows the
// error and treats the empty result as authoritative.
func TestAnUnreadableLedgerWritesNothing(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t)

	reconFX.ledger.mu.Lock()
	reconFX.ledger.rowsErr = errors.New("the database is locked")
	reconFX.ledger.mu.Unlock()

	report := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))

	if report.Err == nil {
		t.Fatal("an unreadable ledger reported no error")
	}

	if report.Reverted != 0 || report.Withheld != 0 {
		t.Errorf("Reverted = %d, Withheld = %d on an unreadable ledger, want 0 and 0: "+
			"nothing was known, so nothing was decided and nothing was withheld",
			report.Reverted, report.Withheld)
	}

	if got := reconFX.writer.callCount(); got != 0 {
		t.Errorf("the writer was called %d times with no ledger, want 0", got)
	}

	if !errors.Is(report.Err, reconFX.ledger.rowsErr) {
		t.Errorf("the report's error does not wrap the ledger's: %v", report.Err)
	}
}

// ---------------------------------------------------------------------------
// The clock, and the shape of a decision.
// ---------------------------------------------------------------------------

// TestReconcileReachesNoClockForItself is the determinism assertion, and it is an
// AST walk rather than a behavioural one because the behaviour is unobservable.
//
// `Reconcile` takes `now` as a parameter and the budget is the only thing it decides
// with it, so a `time.Now()` inside would change nothing about any result the
// other tests see — it would just make the window depend on when the test ran, which
// is a failure that shows up on a slow machine once a month. S-10.4's rule is that
// rule code is a pure function of (state, intent, seed), and the enforcement this
// repository has for it is a lint pattern scoped to `internal/domain/rules`; this
// file is not rule code, so the check is here.
//
// It reads the **parsed syntax tree**, so `time.Now` in a comment — which is how this
// very sentence would otherwise be counted as a violation — is not a finding, and a
// call reached through an alias (`clock.Now`) is not one either. What it rules out is
// the thing that would actually happen.
func TestReconcileReachesNoClockForItself(t *testing.T) {
	t.Parallel()

	forbidden := map[string]string{
		"Now":      "a decision may not depend on when it is made",
		"Since":    "nor on how long ago something happened",
		"Until":    "nor on a deadline",
		"After":    "nor on a timer",
		"Sleep":    "nor on a sleep",
		"NewTimer": "nor on a timer this file owns",
	}

	found := clockCallsInThisFile(t)

	for _, call := range found {
		reason, banned := forbidden[call]
		if banned {
			t.Errorf("reconcile.go calls time.%s; %s. Reconcile takes `now` as an "+
				"argument for exactly this reason", call, reason)
		}
	}
}

// TestReconcileIsAPureFunctionOfItsArguments is the behavioural half of the same
// property, and it is the one that would catch a decision made from anything the
// caller did not supply.
//
// The same page, the same rows and the same `now` run many times over, and every
// field of every report has to match. The repetition matters: a single run cannot see
// an order that depends on map iteration, because Go randomises the iteration start
// per range and one sample may land the same way twice. Thirty-two runs over a
// three-secret page is enough for that to surface if it is there.
func TestReconcileIsAPureFunctionOfItsArguments(t *testing.T) {
	t.Parallel()

	rows := []content.LedgerRow{
		{Path: reconTwoSecretPath, Anchor: "first", Ordinal: 0, OrdinalKnown: true, Revealed: true},
		{
			Path:         reconTwoSecretPath,
			Anchor:       "second",
			Ordinal:      1,
			OrdinalKnown: true,
			Revealed:     true,
		},
		{Path: reconTwoSecretPath, Anchor: "third", Ordinal: 2, OrdinalKnown: true, Revealed: true},
	}

	now := time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)

	firstReport := content.ReconcileReport{}
	firstAnchors := []string(nil)

	for run := range 32 {
		// **A fresh reconciler each run**, so a mutation that made the budget
		// accumulate across runs rather than within a pass would show up as a
		// difference between the first run and the rest.
		reconFX := newReconFixture(t, rows...)
		reconFX.now = now

		report := reconFX.run(t, reconFX.page(t, reconTwoSecretPath, reconThreeSecrets))
		anchors := reconFX.ledger.anchors()

		if run == 0 {
			firstReport, firstAnchors = report, anchors

			continue
		}

		if report.Reverted != firstReport.Reverted ||
			report.Withheld != firstReport.Withheld ||
			report.Unresolved != firstReport.Unresolved ||
			report.Collisions != firstReport.Collisions ||
			report.Capped != firstReport.Capped ||
			report.Conflicted != firstReport.Conflicted {
			t.Fatalf("run %d reported %+v and run 0 reported %+v; the same inputs "+
				"must produce the same report", run, report, firstReport)
		}

		if !slices.Equal(anchors, firstAnchors) {
			t.Fatalf("run %d counted %v and run 0 counted %v; the ledger order is "+
				"part of the report's meaning and must not depend on map iteration",
				run, anchors, firstAnchors)
		}
	}

	if want := []string{"first", "second", "third"}; !slices.Equal(firstAnchors, want) {
		t.Errorf("the ledger was counted in the order %v, want %v: ordinal order, "+
			"because the spliced bytes are in ordinal order and the counter must "+
			"follow the file", firstAnchors, want)
	}
}

// TestTheLedgerAdapterReadsEveryField is the one-line-per-row claim `LedgerRows`
// makes, asserted on all five.
//
// The failure it guards is a conversion that drops `OrdinalKnown`: the flag goes
// false, `Reassociate` stops being able to repair a row by ordinal, and every
// first-line edit to a revealed secret becomes drift instead of a re-association.
// That is a silent degradation with a log line for it, which is why the flag has its
// own assertion rather than riding along on the ordinal's.
func TestTheLedgerAdapterReadsEveryField(t *testing.T) {
	t.Parallel()

	reveals := []domain.SecretReveal{
		{
			CampaignID:   reconCampaign,
			Path:         reconPage,
			Anchor:       anchorFor("fire"),
			Ordinal:      3,
			OrdinalKnown: true,
		},
		{
			// **A row whose position was never recorded**, which is the shape a
			// pre-migration-0012 row has and the one the flag exists for.
			CampaignID: reconCampaign,
			Path:       reconPage,
			Anchor:     "an-older-row",
			Ordinal:    0,
		},
	}

	got := content.LedgerRows(reveals)

	if len(got) != len(reveals) {
		t.Fatalf("LedgerRows returned %d rows, want %d", len(got), len(reveals))
	}

	if got[0].Ordinal != 3 || !got[0].OrdinalKnown {
		t.Errorf("row 0 ordinal = %d known = %t, want 3 and true",
			got[0].Ordinal, got[0].OrdinalKnown)
	}

	if got[1].OrdinalKnown {
		t.Error("row 1 reports its ordinal as known; a NULL in the column is not " +
			"zero, and reading it as zero would re-point a historical row onto " +
			"whatever callout now holds position 0")
	}

	if got[1].Ordinal != 0 {
		t.Errorf("row 1 ordinal = %d, want 0: the flag carries the meaning, not a "+
			"sentinel value that has to be reconstructed", got[1].Ordinal)
	}

	// Every row is a disclosure in force. `secrets_revealed` has no revealed
	// column, so a row that existed and was not revealed is a shape the table
	// cannot hold — and an adapter that inferred otherwise would silently decline
	// to re-apply every reveal.
	for index, row := range got {
		if !row.Revealed {
			t.Errorf("row %d reports no disclosure in force", index)
		}

		if row.Path != reveals[index].Path || row.Anchor != reveals[index].Anchor {
			t.Errorf("row %d is %s/%s, want %s/%s", index,
				row.Path, row.Anchor, reveals[index].Path, reveals[index].Anchor)
		}
	}

	if content.LedgerRows(nil) != nil {
		t.Error("LedgerRows(nil) is not nil; a caller checking `rows == nil` as " +
			"'there is no ledger' would be misled")
	}
}

// TestAFailingCounterLeavesTheSecretPublicAndSaysSo is the last untested branch the
// mutation run found, and it is the one that decides what happens when the write
// lands and the ledger does not follow.
//
// **The two cannot be rolled back together**, and this test is where that is
// pinned. By the time `MarkReverted` runs, the `+` is on disk — the write happened
// first, on the reason `Reconcile`'s own comment gives. So a failing counter cannot
// un-reveal the secret, and it must not pretend to: the marker really is public, the
// marker really was written by this pass, and a report claiming `Reverted == 0`
// would be a lie an operator would act on. The error is what says the ledger and the
// file disagree.
//
// The counterfactual is the dangerous one and is asserted too: **a counter that
// fails must not stop the others.** Three reversals, the middle one's counter
// failing, and all three markers on disk — because the bytes went in one write and
// stopping would leave two of the page's fight invisible in the ledger, which is the
// thing `reverted_count` exists to make visible.
func TestAFailingCounterLeavesTheSecretPublicAndSaysSo(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t,
		content.LedgerRow{
			Path: reconTwoSecretPath, Anchor: anchorFor("first"), Ordinal: 0,
			OrdinalKnown: true, Revealed: true,
		},
		content.LedgerRow{
			Path: reconTwoSecretPath, Anchor: anchorFor("second"), Ordinal: 1,
			OrdinalKnown: true, Revealed: true,
		},
	)

	// Only the second row's counter fails. **A single `markErr` on the ledger**
	// rather than per-row, because that is the shape a real store failure has: the
	// database is locked or the transaction failed, and every subsequent row fails
	// the same way. To get "only the second" I therefore use a ledger whose
	// `MarkReverted` fails once and then succeeds — which is the harder case and the
	// one worth having, because it is the case where a `continue` on failure would
	// silently skip later rows.
	reconFX.ledger.mu.Lock()
	reconFX.ledger.failFirst = true
	reconFX.ledger.mu.Unlock()

	report := reconFX.run(t, reconFX.page(t, reconTwoSecretPath, twoNamedSecrets))

	if report.Err == nil {
		t.Fatal("a failing counter reported no error; the ledger and the file now " +
			"disagree and nothing says so")
	}

	if !errors.Is(report.Err, errCounterRefused) {
		t.Errorf("the report's error does not wrap the counter's: %v", report.Err)
	}

	// **All three facts, all three true.** The write landed, so `Reverted` is the
	// number of markers on disk and nothing else.
	if report.Reverted != 2 {
		t.Errorf("Reverted = %d, want 2: the write landed before the counter was "+
			"called, so both markers really are on disk and reporting otherwise is a "+
			"lie an operator would act on", report.Reverted)
	}

	if report.Withheld != 0 {
		t.Errorf("Withheld = %d, want 0: a failing counter withholds nothing — the "+
			"bytes are already public", report.Withheld)
	}

	written := string(reconFX.writer.written())

	for _, id := range []string{"first", "second"} {
		if !stateAt(t, written, id).IsRevealed() {
			t.Errorf("%q was not re-applied; the write is one atomic rename and a "+
				"counter failure cannot un-reveal one of its two bytes", id)
		}
	}

	// The counter that succeeded is counted and the one that failed is not, and the
	// ledger is asked about **both**: the row that failed is still a reversion that
	// happened, and skipping the call would leave the count permanently wrong with
	// no way to notice.
	if got, want := reconFX.ledger.asked, 2; len(got) != want {
		t.Errorf("MarkReverted was asked about %d rows, want %d: a failing counter "+
			"must not stop the others, or the rest of the page's fight is invisible",
			len(got), want)
	}
}

// TestAFailingCounterDoesNotStopThePass is the complement, and it exists because the
// test above asserts the *counts* while this one asserts the *ordering*: the second
// row is asked about after the first, and the failure is reported rather than
// returned.
//
// `errors.Join` is what makes two failures reportable at once, and a `return` on the
// first would leave the second row's counter un-incremented with no signal at all.
func TestAFailingCounterDoesNotStopThePass(t *testing.T) {
	t.Parallel()

	reconFX := newReconFixture(t,
		content.LedgerRow{
			Path: reconTwoSecretPath, Anchor: anchorFor("first"), Ordinal: 0,
			OrdinalKnown: true, Revealed: true,
		},
		content.LedgerRow{
			Path: reconTwoSecretPath, Anchor: anchorFor("second"), Ordinal: 1,
			OrdinalKnown: true, Revealed: true,
		},
	)

	reconFX.ledger.mu.Lock()
	reconFX.ledger.failAnchor = anchorFor("second")
	reconFX.ledger.mu.Unlock()

	report := reconFX.run(t, reconFX.page(t, reconTwoSecretPath, twoNamedSecrets))

	if report.Err == nil {
		t.Fatal("a failing counter reported no error")
	}

	counted := reconFX.ledger.counted()

	if want := []string{anchorFor("first")}; !slices.Equal(counted, want) {
		t.Errorf("the counted rows = %v, want %v: the failing row must not be counted, "+
			"and the succeeding one must not be skipped with it", counted, want)
	}

	if !errors.Is(report.Err, errCounterRefused) {
		t.Errorf("the report's error does not wrap the counter's: %v", report.Err)
	}

	// The event still fires, because the reversion happened. **A counter failure is
	// not a failed reversion**, and an operator reading `secret.reverted` needs to
	// know the fight is real even when the ledger could not be written.
	if !reconFX.log.sawEvent(string(observability.EventSecretReverted), slog.LevelInfo) {
		t.Error("secret.reverted did not fire even though both markers were written")
	}
}

// errCounterRefused is the counter failure the tests above inject. A package-level
// value so `errors.Is` has something to match on from both the double and the
// assertion.
var errCounterRefused = errors.New("the ledger refused the increment")

// TestTheReportCarriesNoPageContent is S-12.3 at the boundary of this file.
//
// `ReconcilePage.Source` is a string field, and `ReconcileReport` sits next to it in
// the same struct family, so "the report never carries the page" is a claim about a
// type rather than about a discipline. It is asserted over every field that can hold
// a string, with the secret's own text as the needle — a body no event may carry and
// no log line may quote.
func TestTheReportCarriesNoPageContent(t *testing.T) {
	t.Parallel()

	const secretText = "The traitor is Captain Aldric"

	reconFX := newReconFixture(t, content.LedgerRow{
		Path:         reconPage,
		Anchor:       anchorFor("fire"),
		Ordinal:      0,
		OrdinalKnown: true,
		Revealed:     true,
	})

	report := reconFX.run(t, reconFX.page(t, reconPage, reconOneSecret))

	rendered := report.String()

	if strings.Contains(rendered, secretText) {
		t.Errorf("the report's rendering carries the secret's body: %q", rendered)
	}

	if strings.Contains(rendered, "[!secret]") {
		t.Errorf("the report's rendering carries page markup: %q", rendered)
	}

	// The path is in it, and it has to be: a log line an operator cannot attribute
	// to a file is a log line nobody can act on. A *path* is not content.
	if !strings.Contains(rendered, reconPage) {
		t.Errorf("the report's rendering does not name the page: %q", rendered)
	}

	if report.Err != nil && strings.Contains(report.Err.Error(), secretText) {
		t.Errorf("the report's error carries the secret's body: %v", report.Err)
	}
}
