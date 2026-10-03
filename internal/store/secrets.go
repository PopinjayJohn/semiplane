package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/semiplane/semiplane/internal/domain"
)

// The secret-ledger statements. Named constants for the reason pages.go gives:
// a column list and the scan list that reads it have to agree, and building
// both from one name is what makes a mismatch impossible to introduce rather
// than merely unlikely.
const (
	// secretRevealColumns is the row as the read side wants it. The same list
	// for every statement here, because every one of them reads the whole row:
	// a ledger entry is six columns and no query wants a subset of them.
	secretRevealColumns = "campaign_id, path, anchor, revealed_by, revealed_at, reverted_count, ordinal"

	// The insert is idempotent on the primary key, and that is the whole of the
	// concurrency strategy. Two concurrent reveals of one anchor are a real
	// case — a GM's reveal racing a reconcile re-application, or two GMs in
	// two tabs — and the alternative to ON CONFLICT DO NOTHING is a second
	// insert that the primary key refuses, which surfaces as an error to a
	// caller who did nothing wrong. The caller learns whether it was the
	// writer from RowsAffects and reads the row back either way, so both
	// racers observe the same ledger entry.
	//
	// `ordinal` is written on insert and **never updated**, which is the whole
	// contract of the column (migration 0012): it records where the secret sat when
	// the disclosure was made, and §5.6.3's repair reads it to re-associate a row
	// whose derived anchor has since moved. It is not corrected when the callout is
	// edited or reordered, because correcting it would mean trusting a position that
	// only the repair pass is entitled to reason about -- and a repair that trusted
	// its own output would never detect drift.
	//
	// A conflict leaves the existing row untouched, ordinal included, so a
	// re-reveal does not move the recorded position.
	insertSecretReveal = `INSERT INTO secrets_revealed
		(campaign_id, path, anchor, revealed_by, revealed_at, ordinal)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (campaign_id, path, anchor) DO NOTHING`

	deleteSecretReveal = `DELETE FROM secrets_revealed
		WHERE campaign_id = ? AND path = ? AND anchor = ?`

	selectSecretReveal = "SELECT " + secretRevealColumns +
		" FROM secrets_revealed WHERE campaign_id = ? AND path = ? AND anchor = ?"

	// The destination check a rename needs: is there any reveal at this path,
	// whatever its anchor. One row is enough to know the answer, so LIMIT 1
	// keeps it a probe rather than a scan.
	selectAnySecretRevealForPath = "SELECT " + secretRevealColumns +
		" FROM secrets_revealed WHERE campaign_id = ? AND path = ? LIMIT 1"

	// A page's reveals, ordered by anchor. The order is total because the
	// primary key makes anchor unique within a page, so the answer does not
	// depend on which plan the planner picks — the property migration 0010
	// requires of an ORDER BY.
	selectSecretRevealsForPage = "SELECT " + secretRevealColumns +
		" FROM secrets_revealed WHERE campaign_id = ? AND path = ? ORDER BY anchor"

	// A campaign's reveals, ordered by path then anchor: the order an operator
	// reads the ledger in, which is the order of the pages the secrets live on.
	selectSecretRevealsForCampaign = "SELECT " + secretRevealColumns +
		" FROM secrets_revealed WHERE campaign_id = ? ORDER BY path, anchor"

	// The reversion counter, saturating. MIN rather than a read-then-write in
	// Go, because the read and the write would be two statements and a
	// concurrent MarkSecretReverted could interleave between them: both would
	// read N and both would write N+1, and one reversion would be lost. The
	// scalar MIN keeps the increment and the bound in one statement, which is
	// the only way the saturation is a property of the write rather than of
	// the caller.
	markSecretReverted = `UPDATE secrets_revealed
		SET reverted_count = MIN(reverted_count + 1, ?)
		WHERE campaign_id = ? AND path = ? AND anchor = ?`

	// The rename re-key. A reveal follows its page, so the row's path is
	// updated in place and the anchor — the half of the key that does not move
	// — is what carries the reveal across. No updated_at because there is no
	// such column: the ledger records when a secret was *revealed*, and a
	// rename is not a reveal.
	renameSecretReveals = `UPDATE secrets_revealed SET path = ?
		WHERE campaign_id = ? AND path = ?`

	// The audit row a reveal or unreveal writes. The statement is here rather
	// than in a shared helper because the only two writers are the two methods
	// below, and a helper used by two callers is a helper a third caller will
	// reach for with a fourth action — at which point the closed vocabulary
	// migration 0009 describes is being widened by the path of least
	// resistance.
	insertAuditLog = `INSERT INTO audit_log
		(campaign_id, actor_id, action, target, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`
)

// The `audit_log.action` values this file writes. The vocabulary is closed
// (migration 0009), so adding one is a migration; these two are the reveal
// half of it, and the unreveal half is their mirror.
const (
	auditActionSecretReveal   = "secret.reveal"
	auditActionSecretUnreveal = "secret.unreveal"
)

// auditTargetPrefix is prepended to the page path to form `audit_log.target`.
//
// The page is named as well as the callout because "what was revealed" and
// "where it lived" are different questions, and a target of
// `secret/traitor` answers both: the first path segment says the act was a
// secret's, and the rest says which page. The anchor goes in `detail`, which
// is the column for the part of the identity the target does not carry.
const auditTargetPrefix = "secret/"

// ErrInvalidSecretAnchor means a reveal was offered without an anchor. The
// anchor is half the primary key and the thing that survives a body edit and
// a rename (S-5.9), so an empty one is a bug in the caller rather than a
// value this package can refuse to store — a row keyed by an empty anchor
// would be a row no future edit could re-find.
var ErrInvalidSecretAnchor = errors.New("store: secret anchor is empty")

// ErrNotGM means the user is not a campaign GM and may not reveal or unreveal
// its secrets.
//
// Defence-in-depth, not the authorization decision. The route mounts
// RequireEdit (ADR 0024), and that gate is the check that decides who may act;
// this is the check that keeps a race between a reveal and a membership
// revocation from writing a ledger row the gate would have refused. The two
// are not the same implementation of one rule: the gate answers "may this
// request proceed", and this answers "is the account named on this row still
// the GM that was meant to be on it".
var ErrNotGM = errors.New("store: only a campaign GM may reveal secrets")

// RevealSecret records that the secret at (campaignID, path, anchor) was
// revealed by userID, and writes the matching audit_log row.
//
// Idempotent on the primary key. A secret that is already revealed returns its
// existing row without error and without a second audit row: a GM who clicks
// reveal twice, or a reveal racing a reconcile re-application, has done
// nothing the second time, and the ledger should not say otherwise. The
// caller learns whether it was the writer from the returned row's
// RevealedAt — a fresh reveal carries the current time, a re-reveal carries
// the original.
//
// The userID's membership is checked inside the transaction, for the reason
// ErrNotGM gives: the check and the write are one unit, so a revocation that
// lands between the gate and the write cannot be observed as a successful
// reveal.
//
// The secret's body is not a parameter and never is. The ledger records who
// disclosed what and when, and the body is the one value that would turn this
// table into a leak (see domain.SecretReveal).
func (s *Store) RevealSecret(
	ctx context.Context,
	campaignID int64,
	path, anchor string,
	userID int64,
	ordinal int,
	ordinalKnown bool,
) (domain.SecretReveal, error) {
	if path == "" {
		return domain.SecretReveal{}, fmt.Errorf("%w: reveal secret", ErrInvalidPagePath)
	}

	if anchor == "" {
		return domain.SecretReveal{}, fmt.Errorf(
			"%w: reveal secret at %s",
			ErrInvalidSecretAnchor,
			path,
		)
	}

	what := "reveal secret at " + path

	var stored domain.SecretReveal

	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		membership, err := revealerMembership(ctx, tx, campaignID, userID, what)
		if err != nil {
			return err
		}

		if membership.Role != domain.RoleGM {
			return fmt.Errorf(
				"%w: user %d is not a GM of campaign %d",
				ErrNotGM,
				userID,
				campaignID,
			)
		}

		revealedAt := storedTime(nowFunc())

		// `nil` rather than 0 for "not recorded". The column is nullable and the
		// distinction is load-bearing (migration 0012), so it has to survive the
		// driver rather than being reconstructed from a zero: `0` is a position.
		var ordinalArg any

		if ordinalKnown {
			ordinalArg = ordinal
		}

		result, err := tx.ExecContext(ctx, insertSecretReveal,
			campaignID, path, anchor, userID, unixSeconds(revealedAt), ordinalArg,
		)
		if err != nil {
			return translateWrite(err, what)
		}

		written, err := result.RowsAffected()
		if err != nil {
			return translateWrite(err, what)
		}

		// The audit row is written only when the ledger row was. A re-reveal
		// is not a second disclosure, and an audit log that recorded one would
		// be a log that lies about how many times a secret was revealed.
		if written > 0 {
			if auditErr := writeAuditLogTx(ctx, tx, campaignID, userID,
				auditActionSecretReveal, auditTargetPrefix+path, anchor, what,
			); auditErr != nil {
				return auditErr
			}
		}

		stored, err = scanSecretReveal(tx.QueryRowContext(ctx, selectSecretReveal,
			campaignID, path, anchor,
		), what)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return domain.SecretReveal{}, err
	}

	return stored, nil
}

// UnrevealSecret records that the secret at (campaignID, path, anchor) was
// hidden again by userID, removing its ledger row and writing the matching
// audit_log row.
//
// Idempotent in the same sense as RevealSecret: a secret that is not revealed
// is not an error and writes no audit row, because nothing was undisclosed.
// The unreveal is the GM's act of taking a disclosure back, and the ledger
// row's removal is what stops reconciliation from re-applying a reveal the GM
// has since revoked.
//
// ErrNotGM for the same reason RevealSecret checks: the gate is the route's,
// and this is the write's own guarantee that the account named on the audit
// row was a GM when it acted.
func (s *Store) UnrevealSecret(
	ctx context.Context,
	campaignID int64,
	path, anchor string,
	userID int64,
) error {
	if path == "" {
		return fmt.Errorf("%w: unreveal secret", ErrInvalidPagePath)
	}

	if anchor == "" {
		return fmt.Errorf("%w: unreveal secret at %s", ErrInvalidSecretAnchor, path)
	}

	what := "unreveal secret at " + path

	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		membership, err := revealerMembership(ctx, tx, campaignID, userID, what)
		if err != nil {
			return err
		}

		if membership.Role != domain.RoleGM {
			return fmt.Errorf(
				"%w: user %d is not a GM of campaign %d",
				ErrNotGM,
				userID,
				campaignID,
			)
		}

		result, err := tx.ExecContext(ctx, deleteSecretReveal, campaignID, path, anchor)
		if err != nil {
			return translateWrite(err, what)
		}

		deleted, err := result.RowsAffected()
		if err != nil {
			return translateWrite(err, what)
		}

		if deleted > 0 {
			if err := writeAuditLogTx(ctx, tx, campaignID, userID,
				auditActionSecretUnreveal, auditTargetPrefix+path, anchor, what,
			); err != nil {
				return err
			}
		}

		return nil
	})
}

// SecretReveal reads one ledger row by its full primary key.
//
// ErrNotFound when there is no row, which is the answer to "this secret was
// never revealed" and not a fault: the file is authoritative for whether it is
// revealed now (S-5.8), and a secret that is hidden and has no row is a
// secret nobody ever revealed, or one whose GM unrevealed it.
func (s *Store) SecretReveal(
	ctx context.Context,
	campaignID int64,
	path, anchor string,
) (domain.SecretReveal, error) {
	if path == "" {
		return domain.SecretReveal{}, fmt.Errorf("%w: read secret reveal", ErrInvalidPagePath)
	}

	if anchor == "" {
		return domain.SecretReveal{}, fmt.Errorf(
			"%w: read secret reveal at %s", ErrInvalidSecretAnchor, path,
		)
	}

	what := "read secret reveal at " + path

	return scanSecretReveal(
		s.db.QueryRowContext(ctx, selectSecretReveal, campaignID, path, anchor),
		what,
	)
}

// SecretsRevealedForPage lists one page's ledger rows, ordered by anchor.
//
// The read reconciliation issues when it diffs the file's current markers
// against the ledger: a page's reveals are the rows whose anchors the resolver
// re-computes from the file, and the diff is per-page by construction.
func (s *Store) SecretsRevealedForPage(
	ctx context.Context,
	campaignID int64,
	path string,
) ([]domain.SecretReveal, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: list secret reveals", ErrInvalidPagePath)
	}

	const what = "list secret reveals for a page"

	rows, err := s.db.QueryContext(ctx, selectSecretRevealsForPage, campaignID, path)
	if err != nil {
		return nil, translateRead(err, what)
	}

	defer closeRows(rows, what)

	reveals := make([]domain.SecretReveal, 0, 4)

	for rows.Next() {
		reveal, err := scanSecretRevealFields(rows)
		if err != nil {
			return nil, translateRead(err, what)
		}

		reveals = append(reveals, reveal)
	}

	if err := rows.Err(); err != nil {
		return nil, translateRead(err, what)
	}

	return reveals, nil
}

// SecretsRevealedForCampaign lists one campaign's ledger rows, ordered by path
// then anchor.
//
// The operator's read: "what has this campaign disclosed, and what is sync
// fighting?" The reverted_count is in the row, so the answer to the second
// question is a sort away, and the sort is over a campaign's pages rather than
// over the whole table.
func (s *Store) SecretsRevealedForCampaign(
	ctx context.Context,
	campaignID int64,
) ([]domain.SecretReveal, error) {
	const what = "list secret reveals for a campaign"

	rows, err := s.db.QueryContext(ctx, selectSecretRevealsForCampaign, campaignID)
	if err != nil {
		return nil, translateRead(err, what)
	}

	defer closeRows(rows, what)

	reveals := make([]domain.SecretReveal, 0, 8)

	for rows.Next() {
		reveal, err := scanSecretRevealFields(rows)
		if err != nil {
			return nil, translateRead(err, what)
		}

		reveals = append(reveals, reveal)
	}

	if err := rows.Err(); err != nil {
		return nil, translateRead(err, what)
	}

	return reveals, nil
}

// MarkSecretReverted increments the reversion counter on one ledger row and
// returns the row as stored.
//
// The write reconciliation (phase 10 S7) issues when it finds a `+` that sync
// has overwritten back to `-`: the reveal is being fought, and the counter is
// what makes the fight visible rather than mysterious. The increment saturates
// at domain.MaxRevertedCount — see the constant for why the bound exists and
// why it is a display bound rather than a security bound.
//
// ErrNotFound when there is no row, which is not a fault: a secret that is
// hidden and has no row was never revealed, so there is no reveal for sync to
// have reverted. The caller treats it as "nothing to re-apply".
//
// No membership check, deliberately: this is a system operation, not a user
// act, and the account it would attribute is the one already on the row.
func (s *Store) MarkSecretReverted(
	ctx context.Context,
	campaignID int64,
	path, anchor string,
) (domain.SecretReveal, error) {
	if path == "" {
		return domain.SecretReveal{}, fmt.Errorf("%w: mark secret reverted", ErrInvalidPagePath)
	}

	if anchor == "" {
		return domain.SecretReveal{}, fmt.Errorf(
			"%w: mark secret reverted at %s", ErrInvalidSecretAnchor, path,
		)
	}

	what := "mark secret reverted at " + path

	var stored domain.SecretReveal

	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, markSecretReverted,
			domain.MaxRevertedCount, campaignID, path, anchor,
		); err != nil {
			return translateWrite(err, what)
		}

		var err error

		stored, err = scanSecretReveal(tx.QueryRowContext(ctx, selectSecretReveal,
			campaignID, path, anchor,
		), what)

		return err
	})
	if err != nil {
		return domain.SecretReveal{}, err
	}

	return stored, nil
}

// RenameSecretReveals moves every ledger row at oldPath to newPath, in one
// transaction, and returns how many moved.
//
// The re-key a page rename needs and cannot get from the row itself: the
// anchor is the half of the key that does not move, and `path` is the half
// that does, so the reveal follows the page rather than being orphaned. An
// orphaned row is a reveal reconciliation will not re-apply, because
// reconciliation resolves anchors against the file's *current* paths — which
// is the failure S-5.9's "a reveal is never silently dropped" exists to
// prevent.
//
// No error when there is no row at oldPath: a rename of a page that never had
// a revealed secret is the normal case, not a fault, and the caller converges
// by re-indexing the destination. ErrConflict when a row already occupies
// newPath: two pages cannot share a path, so whichever file is on disk at
// newPath is the page that path describes, and the row for the one that lost
// is a page that no longer exists under that name.
func (s *Store) RenameSecretReveals(
	ctx context.Context,
	campaignID int64,
	oldPath, newPath string,
) (int, error) {
	if oldPath == "" || newPath == "" {
		return 0, fmt.Errorf("%w: rename secret reveals", ErrInvalidPagePath)
	}

	// A no-op rename — a sync client emitting a rename event with the same
	// path — is a no-op, not a conflict. Without this guard the destination
	// check below would find the row at oldPath and refuse the rename.
	if oldPath == newPath {
		return 0, nil
	}

	what := "rename secret reveals from " + oldPath + " to " + newPath

	var moved int

	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		moved = 0

		// The destination check before the update, for the reason RenamePage
		// gives: the primary key refuses the write that would land on an
		// occupied anchor, and an explicit check names the conflict rather
		// than reporting the constraint.
		destination, found, err := readSecretReveal(ctx, tx, campaignID, newPath)
		if err != nil {
			return err
		}

		if found {
			return fmt.Errorf("%w: %s (anchor %s)", ErrConflict, what, destination.Anchor)
		}

		result, err := tx.ExecContext(ctx, renameSecretReveals, newPath, campaignID, oldPath)
		if err != nil {
			return translateWrite(err, what)
		}

		count, err := result.RowsAffected()
		if err != nil {
			return translateWrite(err, what)
		}

		moved = int(count)

		return nil
	})
	if err != nil {
		return 0, err
	}

	return moved, nil
}

// revealerMembership reads the membership of userID in campaignID within the
// caller's transaction, mapping a missing row onto ErrNotGM.
//
// Inside the transaction rather than before it, because the check and the
// write are one unit: a membership check that ran before the transaction could
// be invalidated by a revocation that lands in between, and the gap is exactly
// the race ErrNotGM exists to close. The read uses the transaction's own
// statement so it sees the same snapshot the write will.
func revealerMembership(
	ctx context.Context,
	tx *sql.Tx,
	campaignID, userID int64,
	what string,
) (domain.Membership, error) {
	membership, err := scanMembership(tx.QueryRowContext(ctx, selectMembership, campaignID, userID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Membership{}, fmt.Errorf(
				"%w: user %d is not a member of campaign %d", ErrNotGM, userID, campaignID,
			)
		}

		return domain.Membership{}, translateRead(err, what)
	}

	return membership, nil
}

// writeAuditLogTx writes one audit row in the caller's transaction.
//
// The ledger row and the audit row are one unit for the reason migration 0009
// gives: a reveal that wrote the ledger and lost the audit row would be a
// disclosure nobody can account for, and an audit row without a ledger row
// would be a disclosure the ledger cannot re-apply. The two commit together
// or not at all.
func writeAuditLogTx(
	ctx context.Context,
	tx *sql.Tx,
	campaignID, userID int64,
	action, target, detail, what string,
) error {
	if _, err := tx.ExecContext(ctx, insertAuditLog,
		campaignID, userID, action, target, detail, unixSeconds(nowFunc()),
	); err != nil {
		return translateWrite(err, what)
	}

	return nil
}

// readSecretReveal reads one ledger row in the shape the rename's destination
// check needs: the row and whether it is there.
//
// By (campaignID, path) rather than by the full primary key, because the
// rename's question is "does any reveal already live at the destination path"
// — the anchor is not known and not relevant, since two pages cannot share a
// path and any row at newPath is a conflict whatever its anchor is.
func readSecretReveal(
	ctx context.Context,
	tx *sql.Tx,
	campaignID int64,
	path string,
) (domain.SecretReveal, bool, error) {
	what := "read secret reveal at " + path

	reveal, err := scanSecretRevealFields(
		tx.QueryRowContext(ctx, selectAnySecretRevealForPath, campaignID, path),
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.SecretReveal{}, false, nil
		}

		return domain.SecretReveal{}, false, translateRead(err, what)
	}

	return reveal, true, nil
}

// scanSecretReveal reads one ledger row and maps a failure onto this package's
// vocabulary.
func scanSecretReveal(row *sql.Row, what string) (domain.SecretReveal, error) {
	reveal, err := scanSecretRevealFields(row)
	if err != nil {
		return domain.SecretReveal{}, translateRead(err, what)
	}

	return reveal, nil
}

// scanSecretRevealFields reads one ledger row, from a QueryRow or from an open
// cursor.
//
// One function for both, for the reason scanPageFields gives: two copies of a
// six-column scan is two places for a column to be added to one and forgotten
// in the other.
func scanSecretRevealFields(row rowScanner) (domain.SecretReveal, error) {
	var (
		reveal     domain.SecretReveal
		revealedAt int64
	)

	// `ordinal` is scanned through a `sql.NullInt64` and not into the struct's own
	// field, because **NULL and 0 are different facts**: NULL means the row was
	// written before migration 0012 or its position was never recorded, and 0 means
	// the secret was the first on its page. Scanning NULL into an int would land on
	// 0 and silently assert the second about a row that only knows the first -- which
	// is how §5.6.3's repair re-points a GM's disclosure onto whichever callout now
	// holds position 0.
	var ordinal sql.NullInt64

	if err := row.Scan(
		&reveal.CampaignID,
		&reveal.Path,
		&reveal.Anchor,
		&reveal.RevealedBy,
		&revealedAt,
		&reveal.RevertedCount,
		&ordinal,
	); err != nil {
		return domain.SecretReveal{}, fmt.Errorf("scan secret reveal row: %w", err)
	}

	reveal.RevealedAt = unixTime(revealedAt)
	reveal.Ordinal, reveal.OrdinalKnown = int(ordinal.Int64), ordinal.Valid

	return reveal, nil
}
