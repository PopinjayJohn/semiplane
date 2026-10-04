package main

// The reconciliation wiring, and the one obligation `internal/content` cannot test
// for itself.
//
// # Why the writer needs its own test here
//
// `content.ReconcileWriter`'s contract says returning `ErrReconcileConflict` **means
// nothing was written**, and says in the same comment that no test in that package can
// catch an implementation that violates it. This is that test.
//
// The violation is not hypothetical and it is not subtle to write by accident: read
// the page, open it for writing, compare, and only then decide. Three of the four steps
// look the same whichever order they are in, and the fourth — whether a temp file
// exists on disk when the comparison fails — is invisible to every assertion that looks
// at the *returned* value. Both implementations return the same error for the same
// input. Only one of them touched the file.
//
// So the assertions here are about **the file and the directory**, not about the error.
//
// # What else is here
//
// Three rows of the S-8 matrix as this wiring answers it, and the pass-budget's
// interaction with a real write. The matrix proper belongs to the package that owns
// the gate; this is the composition root's half of the claim, which is that the sink
// is *reached* at all.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
)

// A page with one collapsed callout carrying a block id, and prose on both sides.
const (
	reconcilePagePath = "lore/vault.md"

	reconcilePageHidden = "---\ntitle: The Vault\n---\n\n" +
		"The vault door is iron.\n\n" +
		"> [!secret]- The combination is hunter2.  ^vault\n" +
		"> It is written on the back of the wyvern's scale.\n\n" +
		"The tide came in and the door held.\n"
)

// reconcilePageRevealed is the same page with the marker flipped, **written out
// rather than derived by `strings.Replace` at the fixture's use site**.
//
// The derived form is the obvious way to say it and it is a trap: the whole point of
// these tests is that exactly one byte differs between the two, and a `Replace` in the
// test hides whether that is still true. If a future edit to the hidden fixture added
// a second `[!secret]-`, the derived "revealed" page would flip both and the writer
// tests would pass against a page that is not the one a sync client produces.
const reconcilePageRevealed = "---\ntitle: The Vault\n---\n\n" +
	"The vault door is iron.\n\n" +
	"> [!secret]+ The combination is hunter2.  ^vault\n" +
	"> It is written on the back of the wyvern's scale.\n\n" +
	"The tide came in and the door held.\n"

// TestTheWriterRefusesAStaleDigestAndTouchesNothing is the contract.
//
// The mutation this exists for: an implementation that writes first and compares
// afterwards. It returns the *same* `ErrReconcileConflict`, so an assertion on the
// error passes, and it has already replaced the file with reconciliation's copy —
// which for a sync client mid-write is a disclosure.
func TestTheWriterRefusesAStaleDigestAndTouchesNothing(t *testing.T) {
	// No `t.Parallel()`: ADR 0004, a `*store.Store` is a process-wide slot.
	inst := newInstance(t)
	_, _ = inst.assemble(t.Context(), inst.registered)

	inst.writePage(inst.campaign, reconcilePagePath, reconcilePageHidden)

	writer := reconcileWriter{roots: inst.roots}

	before, err := os.ReadFile(inst.pagePath(inst.campaign))
	if err != nil {
		t.Fatalf("read the page before: %v", err)
	}

	beforeStat, err := os.Stat(inst.pagePath(inst.campaign))
	if err != nil {
		t.Fatalf("stat the page before: %v", err)
	}

	// A digest of *different* bytes, which is what a page that settled and was then
	// edited looks like to a reconciler holding the older copy.
	stale := content.PagePrecondition("a page that has since changed")

	err = writer.WriteIfUnchanged(
		t.Context(),
		content.ReconcilePage{
			CampaignID: inst.campaign.ID,
			Slug:       inst.campaign.Slug,
			Path:       reconcilePagePath,
			Source:     reconcilePageHidden,
		},
		stale,
		[]byte(reconcilePageRevealed),
	)

	if err == nil {
		t.Fatal("WriteIfUnchanged() error = nil for a stale digest. The reconciler " +
			"would overwrite whatever wrote last")
	}

	// The bytes are unchanged.
	after, err := os.ReadFile(inst.pagePath(inst.campaign))
	if err != nil {
		t.Fatalf("read the page after: %v", err)
	}

	if !bytes.Equal(after, before) {
		t.Errorf("the page was rewritten on a conflict:\n got %q\nwant %q",
			after, before)
	}

	// **And the inode is unchanged**, which is the half a byte comparison cannot
	// see: a temp-file-and-rename implementation replaces the file, so its contents
	// may coincidentally match while the write happened. Comparing `os.SameFile`
	// catches the implementation that wrote identical bytes — which is exactly what
	// a reconciler does when it re-applies a marker that was already applied, and
	// exactly the case where "wrote first, compared after" is invisible in the
	// output.
	afterStat, err := os.Stat(inst.pagePath(inst.campaign))
	if err != nil {
		t.Fatalf("stat the page after: %v", err)
	}

	if !os.SameFile(beforeStat, afterStat) {
		t.Error("the file was replaced on a conflict. A writer that renames into " +
			"place has already destroyed the bytes it was supposed to preserve, " +
			"even when the replacement is identical")
	}
}

// TestTheWriterAppliesWhenTheDigestMatches is the positive: same digest, and the
// marker goes back to `+`.
//
// Without this the test above would also pass for a writer that never writes
// anything, which is the other way to be wrong.
func TestTheWriterAppliesWhenTheDigestMatches(t *testing.T) {
	inst := newInstance(t)
	_, _ = inst.assemble(t.Context(), inst.registered)

	inst.writePage(inst.campaign, reconcilePagePath, reconcilePageHidden)

	writer := reconcileWriter{roots: inst.roots}

	current, err := os.ReadFile(inst.pagePath(inst.campaign))
	if err != nil {
		t.Fatalf("read the page: %v", err)
	}

	want := content.PagePrecondition(string(current))

	if writeErr := writer.WriteIfUnchanged(
		t.Context(),
		content.ReconcilePage{
			CampaignID: inst.campaign.ID,
			Slug:       inst.campaign.Slug,
			Path:       reconcilePagePath,
			Source:     string(current),
		},
		want,
		[]byte(reconcilePageRevealed),
	); writeErr != nil {
		t.Fatalf("WriteIfUnchanged() error = %v, want nil", writeErr)
	}

	after, err := os.ReadFile(inst.pagePath(inst.campaign))
	if err != nil {
		t.Fatalf("read the page after: %v", err)
	}

	if !bytes.Equal(after, []byte(reconcilePageRevealed)) {
		t.Errorf("the page was not written:\n got %q\nwant %q", after, reconcilePageRevealed)
	}

	if !strings.Contains(string(after), "[!secret]+") {
		t.Error("the marker was not re-applied; the reconciler would report a " +
			"reversion that did not happen")
	}
}

// TestTheReconcilerRunsOnASettledUpsert is the composition root's own claim, and it
// is the one a reader of `settledFanOut` cannot verify by reading: that the sink is
// *reached*, and reached with a page it can actually reconcile.
//
// Driven through `settledFanOut` rather than calling `secretReconciler` directly,
// because the thing being tested is the wiring — a sink that is built and never handed
// to the fan-out reconciles nothing and every test of it passes.
func TestTheReconcilerRunsOnASettledUpsert(t *testing.T) {
	inst := newInstance(t)
	_, _ = inst.assemble(t.Context(), inst.registered)

	inst.writePage(inst.campaign, reconcilePagePath, reconcilePageRevealed)

	// The GM's disclosure is on record and the file says `-` — which is what an
	// Obsidian sync client produces when it overwrites a page with a copy from
	// before the reveal.
	inst.seedReveal(t, inst.campaign)

	inst.writePage(inst.campaign, reconcilePagePath, reconcilePageHidden)

	var seen int

	fanOut := settledFanOut{
		index: func(context.Context, content.Change) {
			seen++
		},
		secrets: secretReconciler(
			inst.roots, reconcileLedger{store: inst.store}, discardLogger()),
	}

	fanOut.settle(t.Context(), content.Change{
		CampaignID: inst.campaign.ID,
		Slug:       inst.campaign.Slug,
		Op:         content.OpUpsert,
		Path:       reconcilePagePath,
	})

	if seen != 1 {
		t.Errorf("the indexer ran %d times, want 1. This test is about the secret "+
			"sink; if the index count is wrong the change it built is wrong", seen)
	}

	after, err := os.ReadFile(inst.pagePath(inst.campaign))
	if err != nil {
		t.Fatalf("read the page after: %v", err)
	}

	if !strings.Contains(string(after), "[!secret]+") {
		t.Errorf("the sync reversion was not repaired:\n%s\nA GM whose sync client "+
			"reverts a disclosure has a secret that keeps coming back with no "+
			"explanation anywhere", after)
	}

	if !strings.Contains(string(after), "The tide came in") {
		t.Errorf("reconciliation damaged the page's prose:\n%s", after)
	}
}

// TestTheReconcilerIgnoresEverythingButAnUpsert: a deletion has no bytes to
// reconcile and a rename has already been re-keyed. Reconciling either would read a
// page that is not the one the ledger row names.
func TestTheReconcilerIgnoresEverythingButAnUpsert(t *testing.T) {
	inst := newInstance(t)
	_, _ = inst.assemble(t.Context(), inst.registered)

	inst.writePage(inst.campaign, reconcilePagePath, reconcilePageHidden)

	inst.seedReveal(t, inst.campaign)

	ignored := []content.Op{content.OpRemove, content.OpRename}

	for _, op := range ignored {
		given := op

		settledFanOut{
			index: func(context.Context, content.Change) {},
			secrets: secretReconciler(
				inst.roots, reconcileLedger{store: inst.store}, discardLogger()),
		}.settle(t.Context(), content.Change{
			CampaignID: inst.campaign.ID,
			Slug:       inst.campaign.Slug,
			Op:         given,
			Path:       reconcilePagePath,
			OldPath:    reconcilePagePath,
		})
	}

	after, err := os.ReadFile(inst.pagePath(inst.campaign))
	if err != nil {
		t.Fatalf("read the page after: %v", err)
	}

	if !strings.Contains(string(after), "[!secret]-") {
		t.Errorf("one of %v rewrote the page:\n%s\nOnly an upsert has bytes to "+
			"reconcile; a rename's ledger rows have already been re-keyed and a "+
			"removal has nothing to read", ignored, after)
	}
}

// TestTheReconcileLedgerReadsAndCounts is the store adapter, on the two methods the
// interface names.
//
// The count is the assertion that matters: `reverted_count` is what makes a sync
// fight visible rather than mysterious, and an adapter that quietly no-ops it would
// leave a GM watching a secret toggle forever with nothing to look at.
func TestTheReconcileLedgerReadsAndCounts(t *testing.T) {
	inst := newInstance(t)
	_, _ = inst.assemble(t.Context(), inst.registered)

	inst.writePage(inst.campaign, reconcilePagePath, reconcilePageRevealed)

	inst.seedReveal(t, inst.campaign)

	ledger := reconcileLedger{store: inst.store}

	rows, err := ledger.Rows(t.Context(), inst.campaign.ID, reconcilePagePath)
	if err != nil {
		t.Fatalf("Rows() error = %v, want nil", err)
	}

	if len(rows) != 1 {
		t.Fatalf("Rows() returned %d rows, want 1", len(rows))
	}

	if !rows[0].Revealed {
		t.Error("Rows() returned a row with Reveived = false. Every row in " +
			"`secrets_revealed` records a disclosure — the table has no revealed " +
			"column and does not need one, because §5.6.2 puts the current state on " +
			"the file")
	}

	if !rows[0].OrdinalKnown {
		t.Error("Rows() lost the ordinal. The row was written through the reveal " +
			"route, which knows the position, so `NULL` here means migration 0012's " +
			"column was not written")
	}

	if markErr := ledger.MarkReverted(
		t.Context(), inst.campaign.ID, reconcilePagePath, "vault",
	); markErr != nil {
		t.Fatalf("MarkReverted() error = %v, want nil", markErr)
	}

	after, err := ledger.Rows(t.Context(), inst.campaign.ID, reconcilePagePath)
	if err != nil {
		t.Fatalf("Rows() after MarkReverted() error = %v, want nil", err)
	}

	if len(after) != 1 {
		t.Fatalf("Rows() returned %d rows after a reversion, want 1", len(after))
	}

	// The row survives a reversion — that is the whole point of the ledger — so the
	// count is what changed. Reading it back through the store rather than through
	// the adapter is deliberate: `MarkReverted` returns the row and this test wants
	// the *stored* one.
	reveals, err := inst.store.SecretsRevealedForPage(
		t.Context(), inst.campaign.ID, reconcilePagePath)
	if err != nil {
		t.Fatalf("SecretsRevealedForPage() error = %v", err)
	}

	if reveals[0].RevertedCount != 1 {
		t.Errorf("RevertedCount = %d, want 1. The counter is what makes a sync fight "+
			"visible rather than mysterious", reveals[0].RevertedCount)
	}

	if reveals[0].RevertedCount >= domain.MaxRevertedCount {
		t.Errorf("RevertedCount = %d, at or past the saturation point %d; a single "+
			"reversion should be nowhere near it", reveals[0].RevertedCount,
			domain.MaxRevertedCount)
	}
}

// TestTheReconcilerIsBoundedByItsOwnBudget is a wiring-level check that the budget is
// reached through the sink rather than only inside the package.
//
// A `Reconciler` built here and discarded — never handed to the fan-out, or handed a
// nil writer — would reconcile nothing and every other test in this file would still
// pass. So the sink is driven repeatedly against a page that keeps being reverted,
// and the budget is asserted to stop it.
func TestTheReconcilerIsBoundedByItsOwnBudget(t *testing.T) {
	inst := newInstance(t)
	_, _ = inst.assemble(t.Context(), inst.registered)

	inst.writePage(inst.campaign, reconcilePagePath, reconcilePageRevealed)

	inst.seedReveal(t, inst.campaign)

	fanOut := settledFanOut{
		index:   func(context.Context, content.Change) {},
		secrets: secretReconciler(inst.roots, reconcileLedger{store: inst.store}, discardLogger()),
	}

	// Revert the page, then let the sink run far more times than the budget allows,
	// all inside one window.
	inst.writePage(inst.campaign, reconcilePagePath, reconcilePageHidden)

	change := content.Change{
		CampaignID: inst.campaign.ID,
		Slug:       inst.campaign.Slug,
		Op:         content.OpUpsert,
		Path:       reconcilePagePath,
	}

	for range content.ReconcilePassLimit * 3 {
		fanOut.settle(t.Context(), change)

		// Revert again, so every pass has something pending. This is a sync client
		// that never stops, which is the case the cap exists for.
		inst.writePage(inst.campaign, reconcilePagePath, reconcilePageHidden)
	}

	reveals, err := inst.store.SecretsRevealedForPage(
		t.Context(), inst.campaign.ID, reconcilePagePath)
	if err != nil {
		t.Fatalf("SecretsRevealedForPage() error = %v", err)
	}

	count := reveals[0].RevertedCount

	if count > content.ReconcilePassLimit {
		t.Errorf("the sink re-applied %d times in one window, above the limit of %d. "+
			"A GM and a sync client that never stop agreeing would rewrite this file "+
			"forever", count, content.ReconcilePassLimit)
	}

	if count == 0 {
		t.Error("the sink never re-applied anything; the budget was not reached " +
			"because nothing was attempted")
	}
}

// pagePath is the on-disk path of the fixture's page in a campaign's content root.
//
// **No `rel` parameter**, and that is the linter being right: every caller in this
// file reconciles `reconcilePagePath`, so a parameter would be a second answer to
// "which page is this about" that could disagree with the constant beside it. A
// second page arrives as a second constant.
func (i *instance) pagePath(campaign domain.Campaign) string {
	return filepath.Join(campaign.ContentRoot, filepath.FromSlash(reconcilePagePath))
}

// seedReveal records one disclosure in the ledger, through the same store method the
// reveal route calls.
//
// **Through `RevealSecret` rather than by inserting a row**, so the fixture's row has
// the ordinal migration 0012 added and the `revealed_at`/`revealed_by` a real reveal
// writes. A hand-inserted row would leave `ordinal` NULL, and the reconciler's
// ordinal re-association is half of what these tests exercise.
func (i *instance) seedReveal(t *testing.T, campaign domain.Campaign) {
	t.Helper()

	user := i.createUser("revealed-by", "correct horse battery")
	i.addMember(campaign.ID, user.ID, domain.RoleGM)

	if _, err := i.store.RevealSecret(
		t.Context(), campaign.ID, reconcilePagePath, "vault", user.ID, 0, true,
	); err != nil {
		t.Fatalf("RevealSecret() error = %v, want nil", err)
	}
}
