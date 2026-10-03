package secrets

// The ledger, as the route needs it: two capabilities and nothing else.
//
// §5.6.4 requires a reveal to be written to `audit_log` **and** to
// `secrets_revealed`, and migration 0009's rationale makes the two one unit: "a
// reveal that wrote the ledger and lost the audit row would be a disclosure nobody
// can account for, and an audit row without a ledger row would be a disclosure the
// ledger cannot re-apply. The two commit together or not at all." So this interface
// names **one method per direction** rather than a ledger write and an audit write,
// because a route that could write them separately would be able to produce exactly
// the state the migration forbids — and a caller with two methods has no way to be
// stopped from calling only the first.
//
// `*store.Store` satisfies this as it stands. `RevealSecret` and `UnrevealSecret`
// already do the whole job: the membership re-check inside the transaction, the
// ledger row, and the audit row. This package therefore writes **no SQL**, and the
// statements that name `audit_log.target` and `audit_log.action` are
// `internal/store`'s rather than this route's — which is the right owner for a
// closed vocabulary whose schema CHECKs it.
//
// The interface is also what makes the 500 branch testable: a test can hand the
// route a ledger that fails, and an unreachable branch is an unverified one.

import (
	"context"

	"github.com/semiplane/semiplane/internal/domain"
)

// Ledger records and withdraws one secret's disclosure.
//
// Both methods are GM-checked *inside their own transaction* by
// `store.RevealSecret` and `store.UnrevealSecret`. That is defence in depth rather
// than the authorisation decision — the gate the route mounts is the decision — and
// it is what closes the window between "the gate admitted this requestor" and "this
// write commits": a revocation landing in between makes the write fail rather than
// record a disclosure under an account that is no longer the GM.
type Ledger interface {
	// RevealSecret records that the secret at (campaignID, path, anchor) was
	// revealed by userID, and writes the matching audit row.
	//
	// Idempotent on the primary key: a secret that is already revealed returns its
	// existing row without error and **without a second audit row**, because a GM who
	// clicks reveal twice has done nothing the second time.
	RevealSecret(
		ctx context.Context,
		campaignID int64,
		path, anchor string,
		userID int64,
		ordinal int,
		ordinalKnown bool,
	) (domain.SecretReveal, error)

	// UnrevealSecret removes the ledger row for (campaignID, path, anchor) and
	// writes the matching audit row.
	//
	// Idempotent in the same sense: a secret that is not revealed is not an error
	// and writes no audit row, because nothing was undisclosed. The removal is what
	// stops reconciliation from re-applying a reveal the GM has since revoked.
	UnrevealSecret(ctx context.Context, campaignID int64, path, anchor string, userID int64) error
}
