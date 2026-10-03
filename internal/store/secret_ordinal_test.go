package store_test

// The ordinal column, end to end: written on a reveal, read back as a position, and
// **distinguishable from not having one**.
//
// # Why this file is separate from `secrets_test.go`
//
// Migration 0012's argument is entirely about one distinction — `NULL` and `0` are
// different facts — and that distinction is invisible to every other test in the
// package, because they all write a known ordinal and read it straight back. So the
// tests that could fail are gathered here where the column is the subject, rather
// than spread through a file whose subject is the ledger.
//
// # The failure this exists to prevent
//
// §5.6.3's repair re-points a row by the position it recorded. If a row's missing
// position were read as `0`, the repair would attach that GM's disclosure to
// whichever callout now sits first on the page — a different secret, to a different
// reader. That is a disclosure produced by a **migration**, which is the worst place
// for one to come from: nobody is watching a `DEFAULT 0`.

import (
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// TestARevealedRowKeepsItsOrdinal is the ordinary case: a caller that knows the
// position gets it back.
func TestARevealedRowKeepsItsOrdinal(t *testing.T) {
	db, campaign, user := ordinalFixture(t)

	reveal, err := db.RevealSecret(
		t.Context(), campaign.ID, "lore/vault.md", "vault", user.ID, 3, true)
	if err != nil {
		t.Fatalf("RevealSecret() error = %v, want nil", err)
	}

	if reveal.Ordinal != 3 {
		t.Errorf("Ordinal = %d, want 3", reveal.Ordinal)
	}

	if !reveal.OrdinalKnown {
		t.Error("OrdinalKnown = false for a row written with a position")
	}

	// And it survives a re-read, which is the only thing that matters: the repair
	// pass reads rows it did not write.
	read, err := db.SecretReveal(
		t.Context(), campaign.ID, "lore/vault.md", "vault")
	if err != nil {
		t.Fatalf("SecretReveal() error = %v, want nil", err)
	}

	if read.Ordinal != 3 || !read.OrdinalKnown {
		t.Errorf(
			"re-read ordinal = %d (known %v), want 3 (known true)",
			read.Ordinal,
			read.OrdinalKnown,
		)
	}
}

// TestOrdinalZeroIsAPositionAndNotAnAbsence is the distinction, in both directions.
//
// Two facts that a single `int` cannot hold: a secret that was **first on its page**
// is at position 0, and a row whose position was never recorded is at no position.
// Reading either as the other is a disclosure.
func TestOrdinalZeroIsAPositionAndNotAnAbsence(t *testing.T) {
	db, campaign, user := ordinalFixture(t)

	// Position zero, genuinely.
	zero, err := db.RevealSecret(
		t.Context(), campaign.ID, "lore/first.md", "first", user.ID, 0, true)
	if err != nil {
		t.Fatalf("RevealSecret() at position 0 error = %v, want nil", err)
	}

	if !zero.OrdinalKnown {
		t.Error("a secret written at position 0 reads back as not-known. §5.6.3's " +
			"repair would then refuse to re-associate the first secret on a page, " +
			"which is the one position it is most likely to need")
	}

	// No position at all.
	unknown, err := db.RevealSecret(
		t.Context(), campaign.ID, "lore/second.md", "second", user.ID, 0, false)
	if err != nil {
		t.Fatalf("RevealSecret() without a position error = %v, want nil", err)
	}

	if unknown.OrdinalKnown {
		t.Errorf("OrdinalKnown = true for a row written with no position, holding "+
			"Ordinal = %d. §5.6.3's repair would re-point this GM's disclosure "+
			"onto whichever callout now holds position 0", unknown.Ordinal)
	}

	// Both rows coexist, which is the point: they are distinguishable, so one did
	// not overwrite the other.
	rows := listReveals(t, db, campaign.ID)
	if len(rows) != 2 {
		t.Fatalf("read %d rows, want 2 — a row at position 0 and a row with no "+
			"position must both survive", len(rows))
	}
}

// TestTheOrdinalIsNotCorrectedOnAReReveal: `ON CONFLICT DO NOTHING` leaves the
// existing row alone, ordinal included.
//
// A re-reveal that *moved* the recorded position would be the repair pass trusting
// its own output — and a repair that can rewrite its own input can never detect
// drift, because the evidence is what it just changed.
func TestTheOrdinalIsNotCorrectedOnAReReveal(t *testing.T) {
	db, campaign, user := ordinalFixture(t)

	first, err := db.RevealSecret(
		t.Context(), campaign.ID, "lore/vault.md", "vault", user.ID, 2, true)
	if err != nil {
		t.Fatalf("first RevealSecret() error = %v, want nil", err)
	}

	// The callout has moved, and the GM reveals it again from its new position.
	second, err := db.RevealSecret(
		t.Context(), campaign.ID, "lore/vault.md", "vault", user.ID, 5, true)
	if err != nil {
		t.Fatalf("second RevealSecret() error = %v, want nil", err)
	}

	if second.Ordinal != first.Ordinal {
		t.Errorf("a re-reveal moved the recorded position from %d to %d. The "+
			"ordinal records where the secret sat when the disclosure was made, "+
			"and a repair pass that rewrites its own evidence cannot detect drift",
			first.Ordinal, second.Ordinal)
	}
}

// TestARowWrittenBeforeTheColumnReadsBackAsAbsent is the migration case, and it is
// the one the current code path cannot produce — so it is produced the way a database
// written by the previous build would hold it.
func TestARowWrittenBeforeTheColumnReadsBackAsAbsent(t *testing.T) {
	db, campaign, user := ordinalFixture(t)

	// `ordinalKnown: false` is exactly what a caller that cannot know the position
	// writes, and it lands in the column as NULL rather than as 0. The migration's
	// own claim — that a row with no recorded position must stay distinguishable
	// from a secret at position 0 — is therefore testable through the public API,
	// without a second connection to the database file.
	if _, err := db.RevealSecret(
		t.Context(), campaign.ID, "lore/old.md", "old", user.ID, 0, false,
	); err != nil {
		t.Fatalf("RevealSecret() error = %v, want nil", err)
	}

	read, err := db.SecretReveal(t.Context(), campaign.ID, "lore/old.md", "old")
	if err != nil {
		t.Fatalf("SecretReveal() error = %v, want nil", err)
	}

	if read.OrdinalKnown {
		t.Errorf("OrdinalKnown = true for a row written with no position, holding "+
			"Ordinal = %d. §5.6.3's repair would re-point this GM's disclosure onto "+
			"whichever callout now holds position 0", read.Ordinal)
	}

	// And it coexists with a real position 0 on the same page, which is the pair a
	// single `int` could not represent.
	if _, err := db.RevealSecret(
		t.Context(), campaign.ID, "lore/old.md", "first", user.ID, 0, true,
	); err != nil {
		t.Fatalf("RevealSecret() at position 0 error = %v, want nil", err)
	}

	both := listReveals(t, db, campaign.ID)
	if len(both) != 2 {
		t.Fatalf("read %d rows, want 2 — one at position 0 and one with no position",
			len(both))
	}

	known, absent := 0, 0

	for _, row := range both {
		if row.OrdinalKnown {
			known++
		} else {
			absent++
		}
	}

	if known != 1 || absent != 1 {
		t.Errorf("the page holds %d rows with a position and %d without, want 1 and "+
			"1. They are different facts about the same page and both must survive "+
			"the round trip", known, absent)
	}
}

// TestTheOrdinalIsNotInThePrimaryKey: the key stays `(campaign_id, path, anchor)`.
//
// A key that included the ordinal would make one disclosure insertable twice under
// two positions, and a ledger that can hold two rows for one secret is a ledger
// whose row count means nothing. So a second row with the same anchor and a
// different ordinal must collide rather than insert.
func TestTheOrdinalIsNotInThePrimaryKey(t *testing.T) {
	db, campaign, user := ordinalFixture(t)

	if _, err := db.RevealSecret(
		t.Context(), campaign.ID, "lore/vault.md", "vault", user.ID, 1, true,
	); err != nil {
		t.Fatalf("first RevealSecret() error = %v, want nil", err)
	}

	// Same anchor, different ordinal. Idempotent on the key, so this succeeds and
	// changes nothing — the assertion is that it does not *insert*.
	if _, err := db.RevealSecret(
		t.Context(), campaign.ID, "lore/vault.md", "vault", user.ID, 9, true,
	); err != nil {
		t.Fatalf("second RevealSecret() error = %v, want nil", err)
	}

	rows := listReveals(t, db, campaign.ID)
	if len(rows) != 1 {
		t.Errorf("the ledger holds %d rows for one anchor, want 1. The ordinal is "+
			"a repair hint, not an identity", len(rows))
	}

	if rows[0].Ordinal != 1 {
		t.Errorf("the surviving row is at position %d, want 1 — the original, not "+
			"the second attempt's", rows[0].Ordinal)
	}
}

// TestTheRenameReKeyCarriesTheOrdinal is the reason the column is on the row at all:
// a page move must not lose the position, or every derived anchor on that page stops
// resolving at once.
func TestTheRenameReKeyCarriesTheOrdinal(t *testing.T) {
	db, campaign, user := ordinalFixture(t)

	if _, err := db.RevealSecret(
		t.Context(), campaign.ID, "lore/old.md", "vault", user.ID, 4, true,
	); err != nil {
		t.Fatalf("RevealSecret() error = %v, want nil", err)
	}

	moved, err := db.RenameSecretReveals(
		t.Context(), campaign.ID, "lore/old.md", "lore/new.md")
	if err != nil {
		t.Fatalf("RenameSecretReveals() error = %v, want nil", err)
	}

	if moved != 1 {
		t.Errorf("RenameSecretReveals() moved %d rows, want 1", moved)
	}

	read, err := db.SecretReveal(t.Context(), campaign.ID, "lore/new.md", "vault")
	if err != nil {
		t.Fatalf("SecretReveal() at the new path error = %v, want nil", err)
	}

	if read.Ordinal != 4 || !read.OrdinalKnown {
		t.Errorf("the re-keyed row is at position %d (known %v), want 4 (known true). "+
			"A rename that dropped the position would leave every derived anchor on "+
			"the page unresolvable, and §5.6.3's repair is the only thing that can "+
			"put them back", read.Ordinal, read.OrdinalKnown)
	}
}

// ordinalFixture is a campaign with a GM in it, which is the minimum a reveal needs:
// `RevealSecret` refuses anyone who is not a GM of the campaign, so a fixture
// without one would be testing the refusal rather than the column.
func ordinalFixture(t *testing.T) (*store.Store, domain.Campaign, domain.User) {
	t.Helper()

	// **No `t.Parallel()` anywhere in this file**, and that is ADR 0004 rather than
	// an oversight: a `*store.Store` is a process-wide single-instance slot, so two
	// parallel fixtures collide on it and the collision surfaces as "a Store is
	// already open for this process". The existing `secrets_test.go` is sequential
	// for the same reason.
	//
	// The slug is lowercased from the test name because a campaign slug is
	// `a-z0-9-` and `t.Name()` carries capitals — and the resulting rejection is a
	// good demonstration that the slug rule is real, but it is not what these tests
	// are about.
	db := openTestStore(t)

	name := strings.ToLower(t.Name())

	campaign := seedCampaign(t, db, "ordinal-"+name)
	user := seedUser(t, db, "gm-"+name)
	seedRevealer(t, db, campaign.ID, user.ID)

	return db, campaign, user
}

// listReveals reads every ledger row for a campaign, for a count assertion.
//
// A helper rather than an inline query because three of the tests above want the
// same thing and three copies of a `SELECT` is three places a column list can drift.
func listReveals(t *testing.T, db *store.Store, campaignID int64) []domain.SecretReveal {
	t.Helper()

	rows, err := db.SecretsRevealedForCampaign(t.Context(), campaignID)
	if err != nil {
		t.Fatalf("SecretsRevealedForCampaign() error = %v, want nil", err)
	}

	return rows
}
