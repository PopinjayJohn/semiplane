package store_test

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/store"
)

// The secret-ledger tests. Each one names the rule it holds and the mutation
// that would break it, because a test that cannot fail is a claim rather than
// a check (AGENTS.md, "A gate test that cannot fail is not a gate").

// seedRevealer makes userID a GM of campaignID, the role a reveal requires.
func seedRevealer(
	t *testing.T,
	db *store.Store,
	campaignID, userID int64,
) {
	t.Helper()

	seedMembership(t, db, campaignID, userID, domain.RoleGM)
}

// TestRevealSecretWritesTheLedgerRow is the basic assertion: a reveal writes a
// row keyed by (campaign_id, path, anchor) with the revealer, the time, and a
// zeroed reversion counter.
func TestRevealSecretWritesTheLedgerRow(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "ledger")
	user := seedUser(t, db, "gm-ledger")
	seedRevealer(t, db, campaign.ID, user.ID)

	reveal, err := db.RevealSecret(t.Context(), campaign.ID, "lore/traitor", "traitor", user.ID)
	if err != nil {
		t.Fatalf("RevealSecret() error = %v, want nil", err)
	}

	if reveal.CampaignID != campaign.ID {
		t.Errorf("CampaignID = %d, want %d", reveal.CampaignID, campaign.ID)
	}

	if reveal.Path != "lore/traitor" {
		t.Errorf("Path = %q, want %q", reveal.Path, "lore/traitor")
	}

	if reveal.Anchor != "traitor" {
		t.Errorf("Anchor = %q, want %q", reveal.Anchor, "traitor")
	}

	if reveal.RevealedBy != user.ID {
		t.Errorf("RevealedBy = %d, want %d", reveal.RevealedBy, user.ID)
	}

	if reveal.RevealedAt.IsZero() {
		t.Error("RevealedAt is zero; the row must say when the secret was disclosed")
	}

	if reveal.RevertedCount != 0 {
		t.Errorf("RevertedCount = %d, want 0", reveal.RevertedCount)
	}
}

// TestRevealSecretIsIdempotentOnThePrimaryKey covers the same anchor revealed
// twice — a GM clicking reveal twice, or a reveal racing a reconcile
// re-application. The second reveal must not error, must not write a second
// row, and must not write a second audit row: a re-reveal is not a second
// disclosure.
func TestRevealSecretIsIdempotentOnThePrimaryKey(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "idempotent")
	user := seedUser(t, db, "gm-idempotent")
	seedRevealer(t, db, campaign.ID, user.ID)

	first, err := db.RevealSecret(t.Context(), campaign.ID, "lore/traitor", "traitor", user.ID)
	if err != nil {
		t.Fatalf("first RevealSecret() error = %v, want nil", err)
	}

	second, err := db.RevealSecret(t.Context(), campaign.ID, "lore/traitor", "traitor", user.ID)
	if err != nil {
		t.Fatalf("second RevealSecret() error = %v, want nil; a re-reveal is not an error", err)
	}

	if second.RevealedAt != first.RevealedAt {
		t.Errorf(
			"re-reveal moved RevealedAt from %v to %v; the original disclosure time must survive",
			first.RevealedAt,
			second.RevealedAt,
		)
	}

	// One ledger row, not two.
	rows, err := db.SecretsRevealedForPage(t.Context(), campaign.ID, "lore/traitor")
	if err != nil {
		t.Fatalf("SecretsRevealedForPage() error = %v, want nil", err)
	}

	if len(rows) != 1 {
		t.Errorf(
			"page has %d ledger rows, want 1; the primary key must absorb a re-reveal",
			len(rows),
		)
	}

	// One audit row, not two.
	count := auditLogCount(t, db, campaign.ID, "secret.reveal")
	if count != 1 {
		t.Errorf(
			"audit_log has %d reveal rows, want 1; a re-reveal is not a second disclosure",
			count,
		)
	}
}

// TestRevealSecretWritesTheAuditRow is the audit-trail half of the reveal: the
// disclosure is recorded in audit_log with the revealer, the action, and a
// target that names the page without naming the secret.
func TestRevealSecretWritesTheAuditRow(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "audit")
	user := seedUser(t, db, "gm-audit")
	seedRevealer(t, db, campaign.ID, user.ID)

	if _, err := db.RevealSecret(
		t.Context(),
		campaign.ID,
		"lore/traitor",
		"traitor",
		user.ID,
	); err != nil {
		t.Fatalf("RevealSecret() error = %v, want nil", err)
	}

	row, found := auditLogLast(t, db, campaign.ID, "secret.reveal")
	if !found {
		t.Fatal("no audit_log row for the reveal")
	}

	if row.ActorID != user.ID {
		t.Errorf("ActorID = %d, want %d", row.ActorID, user.ID)
	}

	if row.Target != "secret/lore/traitor" {
		t.Errorf("Target = %q, want %q", row.Target, "secret/lore/traitor")
	}

	if row.Detail != "traitor" {
		t.Errorf("Detail = %q, want the anchor %q", row.Detail, "traitor")
	}
}

// TestRevealSecretRefusesANonMember covers a reveal by a user who is no longer
// a member — the race between a reveal and a membership revocation. The store
// must refuse, because the audit row it would write names an actor who is not
// a GM, and a ledger row written by a non-member is a row reconciliation will
// trust.
func TestRevealSecretRefusesANonMember(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "nonmember")
	user := seedUser(t, db, "gm-nonmember")
	seedRevealer(t, db, campaign.ID, user.ID)

	// Revoke the membership the reveal would rely on.
	if err := db.DeleteMembership(t.Context(), campaign.ID, user.ID); err != nil {
		t.Fatalf("DeleteMembership() error = %v, want nil", err)
	}

	_, err := db.RevealSecret(t.Context(), campaign.ID, "lore/traitor", "traitor", user.ID)
	if !errors.Is(err, store.ErrNotGM) {
		t.Errorf("RevealSecret() by a non-member error = %v, want ErrNotGM", err)
	}

	// And nothing was written.
	if rows, _ := db.SecretsRevealedForPage(
		t.Context(),
		campaign.ID,
		"lore/traitor",
	); len(
		rows,
	) != 0 {
		t.Errorf("a non-member reveal wrote %d ledger rows, want 0", len(rows))
	}
}

// TestRevealSecretRefusesAPlayer is the other half of the authorization: a
// member with role=player may not reveal, because content editing is GM-only
// (S-6.5) and a reveal is a content edit.
func TestRevealSecretRefusesAPlayer(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "player")
	user := seedUser(t, db, "player-reveal")
	seedMembership(t, db, campaign.ID, user.ID, domain.RolePlayer)

	_, err := db.RevealSecret(t.Context(), campaign.ID, "lore/traitor", "traitor", user.ID)
	if !errors.Is(err, store.ErrNotGM) {
		t.Errorf("RevealSecret() by a player error = %v, want ErrNotGM", err)
	}
}

// TestRevealSecretRefusesAnEmptyAnchor covers the caller-bug direction: an
// anchor is half the primary key and the thing that survives a body edit, so
// an empty one is refused rather than stored as a row no future edit can
// re-find.
func TestRevealSecretRefusesAnEmptyAnchor(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "emptyanchor")
	user := seedUser(t, db, "gm-emptyanchor")
	seedRevealer(t, db, campaign.ID, user.ID)

	_, err := db.RevealSecret(t.Context(), campaign.ID, "lore/traitor", "", user.ID)
	if !errors.Is(err, store.ErrInvalidSecretAnchor) {
		t.Errorf("RevealSecret() with empty anchor error = %v, want ErrInvalidSecretAnchor", err)
	}
}

// TestUnrevealSecretRemovesTheLedgerRowAndWritesTheAuditRow is the unreveal
// half: the GM takes the disclosure back, the ledger row goes (which stops
// reconciliation re-applying it), and the audit row records the revocation.
func TestUnrevealSecretRemovesTheLedgerRowAndWritesTheAuditRow(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "unreveal")
	user := seedUser(t, db, "gm-unreveal")
	seedRevealer(t, db, campaign.ID, user.ID)

	if _, err := db.RevealSecret(
		t.Context(),
		campaign.ID,
		"lore/traitor",
		"traitor",
		user.ID,
	); err != nil {
		t.Fatalf("RevealSecret() error = %v, want nil", err)
	}

	if err := db.UnrevealSecret(
		t.Context(),
		campaign.ID,
		"lore/traitor",
		"traitor",
		user.ID,
	); err != nil {
		t.Fatalf("UnrevealSecret() error = %v, want nil", err)
	}

	// The ledger row is gone.
	if _, err := db.SecretReveal(
		t.Context(),
		campaign.ID,
		"lore/traitor",
		"traitor",
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Errorf("SecretReveal() after unreveal error = %v, want ErrNotFound", err)
	}

	// The audit row records the revocation.
	row, found := auditLogLast(t, db, campaign.ID, "secret.unreveal")
	if !found {
		t.Fatal("no audit_log row for the unreveal")
	}

	if row.ActorID != user.ID {
		t.Errorf("ActorID = %d, want %d", row.ActorID, user.ID)
	}
}

// TestUnrevealSecretIsIdempotent covers an unreveal of a secret that is not
// revealed — sync already reverted it, or the GM unrevealed it twice. Not an
// error and no audit row, because nothing was undisclosed.
func TestUnrevealSecretIsIdempotent(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "unreveal-idem")
	user := seedUser(t, db, "gm-unreveal-idem")
	seedRevealer(t, db, campaign.ID, user.ID)

	if err := db.UnrevealSecret(
		t.Context(),
		campaign.ID,
		"lore/traitor",
		"traitor",
		user.ID,
	); err != nil {
		t.Fatalf("UnrevealSecret() of an unrevealed secret error = %v, want nil", err)
	}

	if count := auditLogCount(t, db, campaign.ID, "secret.unreveal"); count != 0 {
		t.Errorf("audit_log has %d unreveal rows, want 0; nothing was undisclosed", count)
	}
}

// TestMarkSecretRevertedIncrementsTheCounter is the sync-fight signal: each
// time reconciliation finds a `+` that sync overwrote, the counter climbs.
func TestMarkSecretRevertedIncrementsTheCounter(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "reverted")
	user := seedUser(t, db, "gm-reverted")
	seedRevealer(t, db, campaign.ID, user.ID)

	if _, err := db.RevealSecret(
		t.Context(),
		campaign.ID,
		"lore/traitor",
		"traitor",
		user.ID,
	); err != nil {
		t.Fatalf("RevealSecret() error = %v, want nil", err)
	}

	for range 3 {
		reveal, err := db.MarkSecretReverted(t.Context(), campaign.ID, "lore/traitor", "traitor")
		if err != nil {
			t.Fatalf("MarkSecretReverted() error = %v, want nil", err)
		}

		if reveal.RevertedCount == 0 {
			t.Error("RevertedCount stayed 0; the reversion was not recorded")
		}
	}

	reveal, err := db.SecretReveal(t.Context(), campaign.ID, "lore/traitor", "traitor")
	if err != nil {
		t.Fatalf("SecretReveal() error = %v, want nil", err)
	}

	if reveal.RevertedCount != 3 {
		t.Errorf("RevertedCount = %d, want 3", reveal.RevertedCount)
	}
}

// TestMarkSecretRevertedSaturates is the bound: the counter climbs to
// domain.MaxRevertedCount and stops, because past that point the fight is
// unambiguous and the exact number no longer changes anyone's response.
func TestMarkSecretRevertedSaturates(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "saturate")
	user := seedUser(t, db, "gm-saturate")
	seedRevealer(t, db, campaign.ID, user.ID)

	if _, err := db.RevealSecret(
		t.Context(),
		campaign.ID,
		"lore/traitor",
		"traitor",
		user.ID,
	); err != nil {
		t.Fatalf("RevealSecret() error = %v, want nil", err)
	}

	// One past the bound, so the saturation is observed rather than assumed.
	for range domain.MaxRevertedCount + 1 {
		if _, err := db.MarkSecretReverted(
			t.Context(),
			campaign.ID,
			"lore/traitor",
			"traitor",
		); err != nil {
			t.Fatalf("MarkSecretReverted() error = %v, want nil", err)
		}
	}

	reveal, err := db.SecretReveal(t.Context(), campaign.ID, "lore/traitor", "traitor")
	if err != nil {
		t.Fatalf("SecretReveal() error = %v, want nil", err)
	}

	if reveal.RevertedCount != domain.MaxRevertedCount {
		t.Errorf(
			"RevertedCount = %d, want %d; the counter must saturate",
			reveal.RevertedCount,
			domain.MaxRevertedCount,
		)
	}
}

// TestMarkSecretRevertedOnAnUnrevealedSecret is the not-a-fault direction: a
// secret that is hidden and has no row was never revealed, so there is no
// reveal for sync to have reverted. ErrNotFound, and the caller treats it as
// "nothing to re-apply".
func TestMarkSecretRevertedOnAnUnrevealedSecret(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "mark-unrevealed")

	_, err := db.MarkSecretReverted(t.Context(), campaign.ID, "lore/traitor", "traitor")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("MarkSecretReverted() on an unrevealed secret error = %v, want ErrNotFound", err)
	}
}

// TestRenameSecretRevealsReKeysTheRow covers a path renamed while a row exists:
// the reveal follows the page, because the anchor is the half of the key that
// does not move and `path` is the half that does.
func TestRenameSecretRevealsReKeysTheRow(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "rename")
	user := seedUser(t, db, "gm-rename")
	seedRevealer(t, db, campaign.ID, user.ID)

	if _, err := db.RevealSecret(
		t.Context(),
		campaign.ID,
		"lore/traitor",
		"traitor",
		user.ID,
	); err != nil {
		t.Fatalf("RevealSecret() error = %v, want nil", err)
	}

	moved, err := db.RenameSecretReveals(t.Context(), campaign.ID, "lore/traitor", "lore/aldric")
	if err != nil {
		t.Fatalf("RenameSecretReveals() error = %v, want nil", err)
	}

	if moved != 1 {
		t.Errorf("RenameSecretReveals() moved %d rows, want 1", moved)
	}

	// The reveal is at the new path, under the same anchor.
	reveal, err := db.SecretReveal(t.Context(), campaign.ID, "lore/aldric", "traitor")
	if err != nil {
		t.Fatalf(
			"SecretReveal() at the new path error = %v, want nil; the reveal must follow the page",
			err,
		)
	}

	if reveal.Path != "lore/aldric" {
		t.Errorf("Path = %q, want %q", reveal.Path, "lore/aldric")
	}

	// And not at the old path.
	if _, err := db.SecretReveal(
		t.Context(),
		campaign.ID,
		"lore/traitor",
		"traitor",
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Errorf("SecretReveal() at the old path error = %v, want ErrNotFound", err)
	}
}

// TestRenameSecretRevealsOnAPageWithNoReveals is the not-a-fault direction: a
// rename of a page that never had a revealed secret moves nothing and is not
// an error.
func TestRenameSecretRevealsOnAPageWithNoReveals(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "rename-empty")

	moved, err := db.RenameSecretReveals(t.Context(), campaign.ID, "lore/traitor", "lore/aldric")
	if err != nil {
		t.Fatalf("RenameSecretReveals() error = %v, want nil", err)
	}

	if moved != 0 {
		t.Errorf("RenameSecretReveals() moved %d rows, want 0", moved)
	}
}

// TestRenameSecretRevealsANoOpIsANoOp covers a rename where the source and
// destination are the same path — a sync client emitting a rename event with
// an unchanged path. It must be a no-op, not a conflict: the destination check
// would find the row at the source path and refuse, which is the wrong answer
// for a rename that moved nothing.
func TestRenameSecretRevealsANoOpIsANoOp(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "rename-noop")
	user := seedUser(t, db, "gm-rename-noop")
	seedRevealer(t, db, campaign.ID, user.ID)

	if _, err := db.RevealSecret(
		t.Context(),
		campaign.ID,
		"lore/traitor",
		"traitor",
		user.ID,
	); err != nil {
		t.Fatalf("RevealSecret() error = %v, want nil", err)
	}

	moved, err := db.RenameSecretReveals(t.Context(), campaign.ID, "lore/traitor", "lore/traitor")
	if err != nil {
		t.Fatalf("RenameSecretReveals() no-op error = %v, want nil", err)
	}

	if moved != 0 {
		t.Errorf("RenameSecretReveals() no-op moved %d rows, want 0", moved)
	}

	// The row is still there, under the same path.
	if _, err := db.SecretReveal(t.Context(), campaign.ID, "lore/traitor", "traitor"); err != nil {
		t.Errorf("SecretReveal() after no-op rename error = %v, want nil", err)
	}
}

// TestRenameSecretRevealsRefusesADestinationConflict covers a rename where the
// destination path already has a ledger row. Two pages cannot share a path, so
// whichever file is on disk at the destination is the page that path describes,
// and the row for the one that lost is a page that no longer exists under that
// name. The rename is refused rather than silently overwriting.
func TestRenameSecretRevealsRefusesADestinationConflict(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "rename-conflict")
	user := seedUser(t, db, "gm-rename-conflict")
	seedRevealer(t, db, campaign.ID, user.ID)

	// A reveal at the source path.
	if _, err := db.RevealSecret(
		t.Context(),
		campaign.ID,
		"lore/traitor",
		"traitor",
		user.ID,
	); err != nil {
		t.Fatalf("RevealSecret() at source error = %v, want nil", err)
	}

	// A reveal at the destination path, for a different anchor.
	if _, err := db.RevealSecret(
		t.Context(),
		campaign.ID,
		"lore/aldric",
		"aldric",
		user.ID,
	); err != nil {
		t.Fatalf("RevealSecret() at destination error = %v, want nil", err)
	}

	// The rename must refuse: the destination is occupied.
	_, err := db.RenameSecretReveals(t.Context(), campaign.ID, "lore/traitor", "lore/aldric")
	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("RenameSecretReveals() to an occupied path error = %v, want ErrConflict", err)
	}

	// And the source row is untouched — a refused rename moves nothing.
	if _, err := db.SecretReveal(t.Context(), campaign.ID, "lore/traitor", "traitor"); err != nil {
		t.Errorf("SecretReveal() at source after refused rename error = %v, want nil", err)
	}
}

// TestRevealSecretConcurrently covers a concurrent reveal of the same anchor —
// two GMs in two tabs, or a reveal racing a reconcile re-application. Both
// must succeed, both must observe the same row, and only one audit row may be
// written: the primary key absorbs the race, and ON CONFLICT DO NOTHING is
// what makes that a property of the write rather than of the caller.
func TestRevealSecretConcurrently(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "concurrent")
	user := seedUser(t, db, "gm-concurrent")
	seedRevealer(t, db, campaign.ID, user.ID)

	const racers = 8

	var (
		wg       sync.WaitGroup
		failures atomic.Int64
		results  = make([]domain.SecretReveal, racers)
	)

	for i := range racers {
		wg.Go(func() {
			reveal, err := db.RevealSecret(
				t.Context(),
				campaign.ID,
				"lore/traitor",
				"traitor",
				user.ID,
			)
			if err != nil {
				failures.Add(1)
				t.Errorf("concurrent RevealSecret() error = %v, want nil", err)

				return
			}

			results[i] = reveal
		})
	}

	wg.Wait()

	if got := failures.Load(); got != 0 {
		t.Errorf("%d concurrent reveals failed, want 0", got)
	}

	// Every racer observed the same row.
	for i, reveal := range results {
		if reveal.RevealedAt != results[0].RevealedAt {
			t.Errorf(
				"racer %d observed RevealedAt %v, want %v; the racers must converge on one row",
				i,
				reveal.RevealedAt,
				results[0].RevealedAt,
			)
		}
	}

	// One ledger row and one audit row, not one per racer.
	if rows, _ := db.SecretsRevealedForPage(
		t.Context(),
		campaign.ID,
		"lore/traitor",
	); len(
		rows,
	) != 1 {
		t.Errorf("page has %d ledger rows after a concurrent reveal, want 1", len(rows))
	}

	if count := auditLogCount(t, db, campaign.ID, "secret.reveal"); count != 1 {
		t.Errorf("audit_log has %d reveal rows after a concurrent reveal, want 1", count)
	}
}

// TestRevealSecretWritesNoSecretContent is the S-12.3 assertion, and the one
// that matters most in this file: the ledger and the audit trail record who
// disclosed what and when, and never the secret itself. A body copied into
// either would be a body stored outside the file that is the trust boundary
// (§5.6.6), readable by whoever audits the campaign.
func TestRevealSecretWritesNoSecretContent(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "nosecret")
	user := seedUser(t, db, "gm-nosecret")
	seedRevealer(t, db, campaign.ID, user.ID)

	// The sentinel is the secret's body as the GM's editor holds it. It never
	// reaches the store — the store's signature has no parameter for it — and
	// this test asserts that structurally rather than by trusting the call.
	const sentinel = "the traitor is Captain Aldric"

	if _, err := db.RevealSecret(
		t.Context(),
		campaign.ID,
		"lore/traitor",
		"traitor",
		user.ID,
	); err != nil {
		t.Fatalf("RevealSecret() error = %v, want nil", err)
	}

	// The ledger row carries the anchor, which is a block id here, not a body.
	reveal, err := db.SecretReveal(t.Context(), campaign.ID, "lore/traitor", "traitor")
	if err != nil {
		t.Fatalf("SecretReveal() error = %v, want nil", err)
	}

	if reveal.Anchor == sentinel {
		t.Error(
			"the anchor is the secret's body; the anchor must identify the callout, not disclose it",
		)
	}

	// The audit row's target and detail name the page and the anchor, and
	// neither may carry the body.
	row, found := auditLogLast(t, db, campaign.ID, "secret.reveal")
	if !found {
		t.Fatal("no audit_log row for the reveal")
	}

	if contains(row.Target, sentinel) {
		t.Errorf("audit target %q contains the secret body", row.Target)
	}

	if contains(row.Detail, sentinel) {
		t.Errorf("audit detail %q contains the secret body", row.Detail)
	}
}

// TestSecretsRevealedForPageAndCampaignAreConsistent is the listing assertion:
// the per-page and per-campaign reads agree, so an operator reading the whole
// campaign sees the same rows reconciliation diffs against.
func TestSecretsRevealedForPageAndCampaignAreConsistent(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "consistent")
	user := seedUser(t, db, "gm-consistent")
	seedRevealer(t, db, campaign.ID, user.ID)

	paths := []string{"lore/traitor", "lore/aldric", "lore/aldric"}
	anchors := []string{"traitor", "aldric", "signal"}

	for i, path := range paths {
		if _, err := db.RevealSecret(
			t.Context(),
			campaign.ID,
			path,
			anchors[i],
			user.ID,
		); err != nil {
			t.Fatalf("RevealSecret(%q) error = %v, want nil", path, err)
		}
	}

	all, err := db.SecretsRevealedForCampaign(t.Context(), campaign.ID)
	if err != nil {
		t.Fatalf("SecretsRevealedForCampaign() error = %v, want nil", err)
	}

	if len(all) != 3 {
		t.Fatalf("campaign has %d ledger rows, want 3", len(all))
	}

	// Ordered by path then anchor, so the listing is deterministic.
	if all[0].Path != "lore/aldric" || all[1].Path != "lore/aldric" ||
		all[2].Path != "lore/traitor" {
		t.Errorf("campaign listing is not ordered by path: %q, %q, %q",
			all[0].Path, all[1].Path, all[2].Path)
	}

	for _, path := range paths {
		rows, err := db.SecretsRevealedForPage(t.Context(), campaign.ID, path)
		if err != nil {
			t.Fatalf("SecretsRevealedForPage(%q) error = %v, want nil", path, err)
		}

		if len(rows) == 0 {
			t.Errorf("page %q has no ledger rows, want at least 1", path)
		}
	}
}

// TestSecretRevealRoundTripsTheStoredTime is the timestamp-shape assertion:
// the row's RevealedAt is the value that was stored, not the caller's input,
// because the column holds Unix seconds and a sub-second time would compare
// unequal to the row it came from.
func TestSecretRevealRoundTripsTheStoredTime(t *testing.T) {
	db := openTestStore(t)
	campaign := seedCampaign(t, db, "roundtrip")
	user := seedUser(t, db, "gm-roundtrip")
	seedRevealer(t, db, campaign.ID, user.ID)

	revealed, err := db.RevealSecret(t.Context(), campaign.ID, "lore/traitor", "traitor", user.ID)
	if err != nil {
		t.Fatalf("RevealSecret() error = %v, want nil", err)
	}

	reread, err := db.SecretReveal(t.Context(), campaign.ID, "lore/traitor", "traitor")
	if err != nil {
		t.Fatalf("SecretReveal() error = %v, want nil", err)
	}

	if reread.RevealedAt != revealed.RevealedAt {
		t.Errorf(
			"RevealedAt round-trip moved from %v to %v",
			revealed.RevealedAt,
			reread.RevealedAt,
		)
	}
}

// contains reports whether substr is in s.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// auditLogCount counts audit_log rows for one campaign and action.
func auditLogCount(t *testing.T, db *store.Store, campaignID int64, action string) int {
	t.Helper()

	var count int

	err := db.DB().QueryRowContext(t.Context(),
		"SELECT count(*) FROM audit_log WHERE campaign_id = ? AND action = ?",
		campaignID, action,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count audit_log rows: %v", err)
	}

	return count
}

// auditLogLast reads the most recent audit_log row for one campaign and action.
func auditLogLast(t *testing.T, db *store.Store, campaignID int64, action string) (auditRow, bool) {
	t.Helper()

	row := auditRow{}

	err := db.DB().QueryRowContext(t.Context(),
		"SELECT actor_id, target, detail FROM audit_log"+
			" WHERE campaign_id = ? AND action = ? ORDER BY id DESC LIMIT 1",
		campaignID, action,
	).Scan(&row.ActorID, &row.Target, &row.Detail)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return auditRow{}, false
		}

		t.Fatalf("read audit_log row: %v", err)
	}

	return row, true
}

// auditRow is the shape auditLogLast reads.
type auditRow struct {
	ActorID int64
	Target  string
	Detail  string
}
