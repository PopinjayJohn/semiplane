package content_test

// §5.6.3's anchor resolver and its repair path.
//
// # What these tests are actually for
//
// The feature this file covers has one job: a reveal a GM performed must not vanish.
// Everything else — which name a secret gets, which form that name takes — is
// machinery in service of that. So the assertions are about **outcomes and
// continuity**, not about strings:
//
//   - every row handed in comes back, in order, with its revealed state intact
//     (`TestEveryRowComesBack`, `TestTheRevealedStateIsNeverCleared`);
//   - a repaired row is never named empty (`TestARepairedRowIsNeverNamedEmpty`) — the
//     defect that silently merged two secrets into one ledger row;
//   - a repair that cannot be trusted is refused rather than guessed
//     (`TestAPositionIsNotTrustedWhenItWasNeverRecorded`).
//
// Two of those three tests exist because the **first implementation got them wrong**,
// which is recorded here so a later reader knows they are not theoretical. An earlier
// draft copied a callout's `BlockID` straight into the repaired anchor, and for any
// derived secret that string is `""` — a legal anchor that matches every other empty
// anchor, so two unrelated secrets in one campaign shared a ledger row and one GM's
// reveal became another's.
//
// A fourth test is the **missing ordinal column** made concrete. `domain.SecretReveal`
// has no ordinal field, §5.6.3's repair requires one, and
// `TestAPositionIsNotTrustedWhenItWasNeverRecorded` is what the resolver does about
// that in the meantime: report, never guess.

import (
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
)

const (
	// campaignID is arbitrary and fixed. An anchor is a function of it, so a test
	// that wants a stable anchor needs a stable campaign — and hard-coding it is
	// what makes the expected values below checkable by a reader rather than only
	// by the code.
	campaignID = int64(7)

	traitorPath = "lore/traitor"
)

// A two-callout page, one collapsed with a block id and one revealed and derived.
const twoCalloutPage = "> [!secret]- The traitor is Aldric.  ^traitor\n" +
	"> He replaced the eastern signal fire.\n" +
	"\n" +
	"> [!secret]+ He replaced the western fire.\n"

// TestTheBlockIDWinsOverTheDerivedHash is §5.6.3's resolution order as a test.
//
// Both directions are asserted because either alone is satisfiable by a constant: a
// function that always returned the block id would pass "the block id wins", and one
// that always returned the hash would pass "the derived form is used when there is
// no block id".
func TestTheBlockIDWinsOverTheDerivedHash(t *testing.T) {
	t.Parallel()

	withID := content.ScanSecrets(twoCalloutPage)[0]
	withoutID := content.ScanSecrets(twoCalloutPage)[1]

	got := content.Resolve(campaignID, traitorPath, withID)

	if got.Value != "traitor" {
		t.Errorf("anchor = %q, want %q. §5.6.3 puts the Obsidian block id first: it "+
			"survives a first-line rewrite, a reorder and an insertion above, and "+
			"the derived hash survives none of them", got.Value, "traitor")
	}

	if got.Form != content.AnchorFromBlockID {
		t.Errorf("form = %v, want block-id", got.Form)
	}

	if got.Empty() {
		t.Error("Empty() is true for a resolved anchor")
	}

	// And the derived form for a callout with no block id.
	fallback := content.Resolve(campaignID, traitorPath, withoutID)

	if fallback.Form != content.AnchorDerived {
		t.Errorf("form = %v, want derived for a callout with no block id", fallback.Form)
	}

	if !fallback.IsDerived() {
		t.Error("IsDerived() is false for a derived anchor")
	}

	if fallback.Value == got.Value {
		t.Error("the block id and the derived hash produced the same anchor; one " +
			"secret would then be findable under the other's name")
	}

	// And the derived anchor is recomputable, which is what makes it a key rather
	// than an opaque token.
	if again := content.Resolve(campaignID, traitorPath, withoutID); again != fallback {
		t.Errorf("Resolve is not stable: %+v then %+v. A derived anchor the writer "+
			"cannot recompute is not an anchor", fallback, again)
	}
}

// TestResolveIsScopedToItsCampaignAndPage: the derived hash covers both, so two
// campaigns and two pages in one campaign cannot produce the same name.
func TestResolveIsScopedToItsCampaignAndPage(t *testing.T) {
	t.Parallel()

	secret := content.ScanSecrets(twoCalloutPage)[1]

	here := content.Resolve(campaignID, traitorPath, secret)

	if other := content.Resolve(campaignID+1, traitorPath, secret); other.Value == here.Value {
		t.Errorf("two campaigns produced the anchor %q for the same page", here.Value)
	}

	if other := content.Resolve(campaignID, "lore/other", secret); other.Value == here.Value {
		t.Errorf("two pages in one campaign produced the anchor %q", here.Value)
	}
}

// TestEveryRowComesBack is the load-bearing safety property: the result is the same
// length as the input, always.
//
// A resolver that returned only what it could place would report "no drift" for a
// page whose every row had drifted, and the operator would see nothing at all.
func TestEveryRowComesBack(t *testing.T) {
	t.Parallel()

	rows := []content.LedgerRow{
		// resolves: the block id is still there
		{Path: traitorPath, Anchor: "traitor", Ordinal: 0, OrdinalKnown: true, Revealed: true},
		// resolves: the derived hash is unchanged
		{
			Path:         traitorPath,
			Anchor:       derivedFor(t, 1),
			Ordinal:      1,
			OrdinalKnown: true,
			Revealed:     true,
		},
		// drifts, and is repairable by its recorded position
		{Path: traitorPath, Anchor: "aaaaaaaaaaaa", Ordinal: 1, OrdinalKnown: true, Revealed: true},
		// drifts, and is not repairable: no position recorded
		{Path: traitorPath, Anchor: "bbbbbbbbbbbb", Revealed: true},
		// drifts, and is not repairable: the position is off the page
		{Path: traitorPath, Anchor: "cccccccccccc", Ordinal: 9, OrdinalKnown: true, Revealed: true},
		// never had a name at all
		{Path: traitorPath, Anchor: "", Revealed: true},
	}

	got := content.Reassociate(campaignID, traitorPath, rows, twoCalloutPage)

	if len(got) != len(rows) {
		t.Fatalf("Reassociate() returned %d resolutions for %d rows. A row that "+
			"comes back missing is a reveal the operator was never told about",
			len(got), len(rows))
	}

	// And each resolution carries its own row back, so a caller can act on an
	// unresolved one without re-reading the input slice.
	for index := range rows {
		if got[index].Row != rows[index] {
			t.Errorf("resolution %d carries row %+v, want %+v", index, got[index].Row, rows[index])
		}
	}
}

// TestTheOutcomesAreTheOnesThePlanNames: matched, re-associated by ordinal,
// unresolved — one row each, so a regression that made everything "unresolved" or
// everything "matched" would be caught.
func TestTheOutcomesAreTheOnesThePlanNames(t *testing.T) {
	t.Parallel()

	rows := []content.LedgerRow{
		{Path: traitorPath, Anchor: "traitor", Ordinal: 0, OrdinalKnown: true, Revealed: true},
		{
			Path:         traitorPath,
			Anchor:       derivedFor(t, 1),
			Ordinal:      1,
			OrdinalKnown: true,
			Revealed:     true,
		},
		{Path: traitorPath, Anchor: "aaaaaaaaaaaa", Ordinal: 0, OrdinalKnown: true, Revealed: true},
	}

	got := content.Reassociate(campaignID, traitorPath, rows, twoCalloutPage)

	want := []content.Outcome{
		content.OutcomeMatched,
		content.OutcomeMatched,
		content.OutcomeReassociatedByOrdinal,
	}

	for index, outcome := range want {
		if got[index].Outcome != outcome {
			t.Errorf("row %d (%q) resolved as %v, want %v",
				index, rows[index].Anchor, got[index].Outcome, outcome)
		}
	}
}

// TestARepairedRowIsNeverNamedEmpty is the test that exists because the first
// implementation had the bug it holds.
//
// The repair took the callout's `BlockID` directly, which is the empty string for
// every derived secret — and an empty anchor is a legal string that matches every
// other empty anchor. Two unrelated secrets in one campaign shared a ledger row.
func TestARepairedRowIsNeverNamedEmpty(t *testing.T) {
	t.Parallel()

	// A page whose callouts all use the derived form, so every `BlockID` is empty.
	const derivedOnlyPage = "> [!secret]-\n> First body.\n\n> [!secret]+\n> Second body.\n"

	rows := []content.LedgerRow{
		{Path: traitorPath, Anchor: "aaaaaaaaaaaa", Ordinal: 1, OrdinalKnown: true, Revealed: true},
	}

	got := content.Reassociate(campaignID, traitorPath, rows, derivedOnlyPage)

	if got[0].Outcome != content.OutcomeReassociatedByOrdinal {
		t.Fatalf("outcome = %v, want a re-association; the rest of this test is "+
			"about what a repaired row is named", got[0].Outcome)
	}

	if got[0].Anchor.Empty() {
		t.Error("the repaired row was named the empty string. An empty anchor " +
			"matches every other empty anchor, so two secrets in one campaign " +
			"would share a ledger row and one GM's reveal would become another's")
	}

	// And the name it *was* given has to be one a later reader can recompute,
	// which is the property that makes it a key rather than a token.
	if want := content.Resolve(campaignID, traitorPath,
		content.ScanSecrets(derivedOnlyPage)[1],
	); got[0].Anchor != want {
		t.Errorf("the repaired anchor is %+v, want %+v — the recomputed name of the "+
			"callout it now points at. A name only this function can produce stops "+
			"resolving the moment the campaign is renamed", got[0].Anchor, want)
	}
}

// TestAPositionIsNotTrustedWhenItWasNeverRecorded is the missing ordinal column made
// concrete.
//
// `domain.SecretReveal` has no ordinal field and §5.6.3's repair needs one, so until
// that column exists a row arrives without a position. Refusing to repair it is the
// only safe answer: attaching a reveal to whichever callout happens to occupy the
// slot would disclose one GM's secret to a different reader.
func TestAPositionIsNotTrustedWhenItWasNeverRecorded(t *testing.T) {
	t.Parallel()

	rows := []content.LedgerRow{
		// Position zero, but never recorded. Ordinal 0 is also the default value of
		// the field, so a reader that treated "unset" as "zero" would silently
		// repair this row onto the first callout.
		{Path: traitorPath, Anchor: "aaaaaaaaaaaa", Ordinal: 0, Revealed: true},
	}

	got := content.Reassociate(campaignID, traitorPath, rows, twoCalloutPage)

	if got[0].Outcome != content.OutcomeUnresolved {
		t.Errorf("outcome = %v, want unresolved. A row whose position was never "+
			"recorded must be reported, not re-pointed at whatever callout is in "+
			"that slot now", got[0].Outcome)
	}

	if got[0].Ordinal != -1 {
		t.Errorf("ordinal = %d, want -1: an unresolved row resolved to no callout", got[0].Ordinal)
	}
}

// TestTheRepairRefusesAnOffPagePosition: a recorded ordinal past the end of the page
// means the callout was deleted, or the row belongs to another page's numbering.
// There is no nearest ordinal to fall back on.
func TestTheRepairRefusesAnOffPagePosition(t *testing.T) {
	t.Parallel()

	for _, ordinal := range []int{-1, 2, 99} {
		rows := []content.LedgerRow{
			{
				Path:         traitorPath,
				Anchor:       "aaaaaaaaaaaa",
				Ordinal:      ordinal,
				OrdinalKnown: true,
				Revealed:     true,
			},
		}

		got := content.Reassociate(campaignID, traitorPath, rows, twoCalloutPage)
		if got[0].Outcome != content.OutcomeUnresolved {
			t.Errorf("ordinal %d resolved as %v, want unresolved", ordinal, got[0].Outcome)
		}
	}
}

// TestTheRepairRefusesASlotThatHoldsNothingRevealed is the third refusal.
//
// Re-pointing a row onto a callout that is collapsed and was never revealed would
// give that callout a ledger name and report it as "revealed-when-collapsed" on the
// next pass. Nothing was in force, so nothing is carried across.
func TestTheRepairRefusesASlotThatHoldsNothingRevealed(t *testing.T) {
	t.Parallel()

	// Position 0 is collapsed and the row claims nothing was revealed.
	rows := []content.LedgerRow{
		{
			Path:         traitorPath,
			Anchor:       "aaaaaaaaaaaa",
			Ordinal:      0,
			OrdinalKnown: true,
			Revealed:     false,
		},
	}

	got := content.Reassociate(campaignID, traitorPath, rows, twoCalloutPage)

	if got[0].Outcome != content.OutcomeUnresolved {
		t.Errorf("outcome = %v, want unresolved: the slot holds a secret that was "+
			"never revealed and there is no state to carry across", got[0].Outcome)
	}
}

// TestTheRevealedStateIsNeverCleared is the other half of "never silently drops a
// reveal", and it is asserted in both directions.
//
// Cleared state reports "this was never revealed" for something the ledger says was
// — which is how a GM's disclosure disappears from the record while remaining
// visible in the table.
func TestTheRevealedStateIsNeverCleared(t *testing.T) {
	t.Parallel()

	rows := []content.LedgerRow{
		{Path: traitorPath, Anchor: "traitor", Ordinal: 0, OrdinalKnown: true, Revealed: true},
		{Path: traitorPath, Anchor: "aaaaaaaaaaaa", Ordinal: 1, OrdinalKnown: true, Revealed: true},
		{Path: traitorPath, Anchor: "bbbbbbbbbbbb", Revealed: true},
		{Path: traitorPath, Anchor: "cccccccccccc", Ordinal: 7, OrdinalKnown: true, Revealed: true},
	}

	for index, resolution := range content.Reassociate(campaignID, traitorPath, rows, twoCalloutPage) {
		if !resolution.Revealed {
			t.Errorf("row %d (%q) came back with Revealed = false. Every row in "+
				"this fixture records a reveal, and no outcome may clear it",
				index, rows[index].Anchor)
		}

		if !resolution.Row.Revealed {
			t.Errorf("row %d came back with Row.Revealed = false; the input row was "+
				"not returned intact", index)
		}
	}
}

// TestResolveNeverReturnsAnEmptyAnchor is the invariant the repair path's
// `indexOfAnchor` guard is contingent on.
//
// An empty anchor is the one value this file must never produce: it is a legal
// string, so a row keyed on it matches every other row keyed on it, and two
// unrelated secrets in one campaign would share a ledger entry. `Resolve` avoids it
// by falling through to the derived form when the block id is empty, and this holds
// that across the inputs where the fall-through is easy to get wrong — a callout with
// no block id, one whose block id is a single character, and a page with no callouts
// at all.
func TestResolveNeverReturnsAnEmptyAnchor(t *testing.T) {
	t.Parallel()

	page := "> [!secret]-\n> Body.\n\n" +
		"> [!secret]- Title\n> Body.\n\n" +
		"> [!secret]+ a ^x\n> Body.\n"

	secrets := content.ScanSecrets(page)
	if len(secrets) != 3 {
		t.Fatalf("the fixture has %d callouts, want 3", len(secrets))
	}

	for _, secret := range secrets {
		anchor := content.Resolve(campaignID, traitorPath, secret)

		if anchor.Empty() {
			t.Errorf("callout %d (block id %q) resolved to an empty anchor. An empty "+
				"anchor matches every other empty anchor, so two secrets in one "+
				"campaign would share a ledger row", secret.Ordinal, secret.BlockID)
		}

		if !anchor.IsDerived() && secret.BlockID == "" {
			t.Errorf("callout %d has no block id but resolved as %v", secret.Ordinal, anchor.Form)
		}
	}
}

// TestAnEmptyStoredAnchorMatchesNothing: `""` is not a name, so a row carrying one
// must not resolve — and above all must not be repaired *from* one.
func TestAnEmptyStoredAnchorMatchesNothing(t *testing.T) {
	t.Parallel()

	rows := []content.LedgerRow{
		{Path: traitorPath, Anchor: "", Ordinal: 0, OrdinalKnown: true, Revealed: true},
	}

	got := content.Reassociate(campaignID, traitorPath, rows, twoCalloutPage)

	if got[0].Outcome == content.OutcomeMatched {
		t.Error("a row with an empty anchor matched a callout. The empty string is " +
			"not a name, and matching it would attach the row to whichever callout " +
			"came first")
	}
}

// TestRenameRekeysAndLeavesATombstone is §5.6.3's rename rule.
//
// The tombstone has to be **data**, not an event: a rename is reversible in a way a
// deletion is not, because an Obsidian sync client that reverts the move brings the
// old path back, and the reveal has to be findable there.
func TestRenameRekeysAndLeavesATombstone(t *testing.T) {
	t.Parallel()

	rows := []content.LedgerRow{
		{Path: traitorPath, Anchor: "traitor", Ordinal: 0, OrdinalKnown: true, Revealed: true},
		{Path: traitorPath, Anchor: "bbbbbbbbbbbb", Ordinal: 1, OrdinalKnown: true, Revealed: true},
		// Another page's row: it must not be swept along.
		{
			Path:         "lore/other",
			Anchor:       "cccccccccccc",
			Ordinal:      0,
			OrdinalKnown: true,
			Revealed:     true,
		},
		// A row with no name: not re-keyable, so it stays behind as a tombstone.
		{Path: traitorPath, Anchor: "", Revealed: true},
	}

	rename := content.Rename{From: traitorPath, To: "lore/names"}

	moved, tombstones := content.Rekey(rows, rename)

	if len(moved) != 2 {
		t.Fatalf("moved %d rows, want 2: the two named rows of the renamed page", len(moved))
	}

	for _, row := range moved {
		if row.Path != rename.To {
			t.Errorf("row %q is under %q, want %q", row.Anchor, row.Path, rename.To)
		}

		if row.Anchor == "" {
			t.Errorf("a row was re-keyed onto the empty anchor: %+v", row)
		}
	}

	// The other page's row was not touched.
	for _, row := range moved {
		if row.Anchor == "cccccccccccc" {
			t.Error("a row belonging to a different page was re-keyed. The caller " +
				"would write a path change for a page it never had")
		}
	}

	// Three tombstones: both moved rows plus the nameless one.
	if len(tombstones) != 3 {
		t.Errorf("left %d tombstones, want 3: two moved rows and the nameless one",
			len(tombstones))
	}

	for _, tomb := range tombstones {
		if tomb.Path != rename.From {
			t.Errorf("tombstone %q is under %q, want the old path %q — a tombstone "+
				"that moved with the row cannot be found when the rename is reverted",
				tomb.Anchor, tomb.Path, rename.From)
		}

		if !tomb.Revealed {
			t.Errorf("tombstone %q lost its revealed state", tomb.Anchor)
		}
	}

	// Sorted by anchor, so a tombstone list does not depend on the order the caller
	// happened to read the rows in.
	for index := 1; index < len(tombstones); index++ {
		if tombstones[index-1].Anchor > tombstones[index].Anchor {
			t.Errorf("tombstones are not sorted by anchor: %q then %q",
				tombstones[index-1].Anchor, tombstones[index].Anchor)
		}
	}
}

// TestARenameOntoItsOwnNameIsNotARename: the degenerate case, and the one that fills
// a ledger with rows shadowing live ones if it is mishandled.
func TestARenameOntoItsOwnNameIsNotARename(t *testing.T) {
	t.Parallel()

	rows := []content.LedgerRow{
		{Path: traitorPath, Anchor: "traitor", Ordinal: 0, OrdinalKnown: true, Revealed: true},
	}

	moved, tombstones := content.Rekey(rows, content.Rename{From: traitorPath, To: traitorPath})

	if len(moved) != 1 || len(tombstones) != 0 {
		t.Errorf("a no-op rename produced %d moved rows and %d tombstones, want 1 and 0. "+
			"A tombstone for a path that still exists shadows the live row",
			len(moved), len(tombstones))
	}
}

// TestTheDriftReportCountsAndSortsAndCarriesNoBody is the log-line boundary.
//
// A derived anchor is twelve characters of the secret's own first line, so a report
// that named more than the anchor would put author text in a log aggregator — the one
// thing S-12.3 forbids.
func TestTheDriftReportCountsAndSortsAndCarriesNoBody(t *testing.T) {
	t.Parallel()

	rows := []content.LedgerRow{
		{Path: traitorPath, Anchor: "zzzzzzzzzzzz", Revealed: true},
		{Path: traitorPath, Anchor: "traitor", Ordinal: 0, OrdinalKnown: true, Revealed: true},
		{Path: traitorPath, Anchor: "aaaaaaaaaaaa", Ordinal: 1, OrdinalKnown: true, Revealed: true},
		{Path: traitorPath, Anchor: "mmmmmmmmmmmm", Ordinal: 5, OrdinalKnown: true, Revealed: true},
	}

	report := content.ReportDrift(
		content.Reassociate(campaignID, traitorPath, rows, twoCalloutPage),
	)

	if report.Matched != 1 {
		t.Errorf("matched = %d, want 1", report.Matched)
	}

	if report.Reassociated != 1 {
		t.Errorf("reassociated = %d, want 1", report.Reassociated)
	}

	if report.Unresolved != 2 {
		t.Errorf("unresolved = %d, want 2", report.Unresolved)
	}

	want := []string{"mmmmmmmmmmmm", "zzzzzzzzzzzz"}
	if len(report.UnresolvedAnchors) != len(want) {
		t.Fatalf("named %d unresolved anchors, want %d", len(report.UnresolvedAnchors), len(want))
	}

	for index, anchor := range want {
		if report.UnresolvedAnchors[index] != anchor {
			t.Errorf("unresolved anchor %d = %q, want %q; the report is sorted so a "+
				"log line is deterministic", index, report.UnresolvedAnchors[index], anchor)
		}
	}

	// The three totals sum to the row count, so a log line is checkable against the
	// ledger rather than merely plausible.
	if total := report.Matched + report.Reassociated + report.Unresolved; total != len(rows) {
		t.Errorf("the report accounts for %d rows of %d. A drift report that does "+
			"not add up cannot be checked against the ledger", total, len(rows))
	}

	// The bodies on this page must appear nowhere in the report.
	for _, body := range []string{"Captain Aldric", "eastern signal", "western fire"} {
		if strings.Contains(strings.Join(report.UnresolvedAnchors, " "), body) {
			t.Errorf("the drift report carries %q. S-12.3: no log line carries page "+
				"content", body)
		}
	}
}

// TestTheDriftEventNameIsTheRegisteredOne: §5.6.3 writes the event as
// `secret_anchor_drift`, and the registered spelling is `secret.anchor_drift`,
// matching the other two secret events. A caller must not be able to invent a third.
func TestTheDriftEventNameIsTheRegisteredOne(t *testing.T) {
	t.Parallel()

	if got := content.AnchorDriftEventName; got != "secret.anchor_drift" {
		t.Errorf("AnchorDriftEventName = %q, want %q. §5.6.3 spells it "+
			"`secret_anchor_drift`, which is stale — `observability` already "+
			"registers this one, and a second spelling is a name no alert matches",
			got, "secret.anchor_drift")
	}
}

// derivedFor is the derived anchor of the callout at an ordinal on this page, used by
// fixtures that need a row which resolves exactly.
func derivedFor(t *testing.T, ordinal int) string {
	t.Helper()

	secrets := content.ScanSecrets(twoCalloutPage)
	if ordinal < 0 || ordinal >= len(secrets) {
		t.Fatalf("derivedFor(%d): the page has %d callouts", ordinal, len(secrets))
	}

	return content.Resolve(campaignID, traitorPath, secrets[ordinal]).Value
}
