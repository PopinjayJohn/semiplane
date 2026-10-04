package domain

import "time"

// SecretReveal is one row of `secrets_revealed`: the ledger entry for one
// secret callout on one page of one campaign.
//
// The type is deliberately narrow about what it records. It says **who**
// revealed a secret, **when**, and **how many times sync has since fought
// that** — and nothing about the secret itself. The file is authoritative for
// whether the secret is currently revealed (S-5.8); this row is authoritative
// for the history of the disclosure and for re-applying a reveal that sync
// reverted. A caller that conflates the two will build a reveal path that
// trusts the database for a fact the database does not have.
//
// The secret's body is not here and must never be here. It is the one value
// that would turn this table into a leak: a ledger row is read by whoever
// audits a campaign, and a body copied into it would be a body stored outside
// the file that is the trust boundary (§5.6.6). The anchor identifies the
// callout without disclosing it.
type SecretReveal struct {
	// CampaignID is the tenancy key, and part of the primary key.
	CampaignID int64
	// Path is the page's path relative to the campaign's content root, spelled
	// exactly as `pages.path` spells it. Part of the primary key, and the
	// reason a rename is a re-key rather than a new row: the anchor survives
	// the move, so the ledger follows the page.
	Path string
	// Anchor identifies the callout within the page. Part of the primary key.
	//
	// Two shapes, both opaque to this type (S-5.9):
	//
	//   - An Obsidian block id — the characters following `^` in the source,
	//     e.g. `traitor` for `> [!secret]+ … ^traitor`. Stable across any edit.
	//   - A derived id — `sha256(campaign_id, path, ordinal,
	//     first-line-of-body)[:12]`, twelve lowercase hex characters, used when
	//     the author supplied no block id.
	//
	// The ledger treats both as an opaque string and does not care which it is;
	// the distinction belongs to the anchor resolver (phase 10 S3), which
	// computes one or the other. Storing the block id without its `^` keeps
	// the value the id rather than the Obsidian syntax that marks it.
	Anchor string
	// Ordinal is the callout's position among the secrets on the page, as last
	// observed, and it is what §5.6.3's repair re-associates by.
	//
	// **Zero is not "unknown".** A `NULL` in the column becomes `OrdinalKnown:
	// false` with `Ordinal: 0`, and the two are different facts: 0 means the secret
	// was the first on its page, and not-known means the row predates migration 0012
	// or the position was never recorded. Collapsing them would let the repair pass
	// re-point a historical row onto whatever callout now holds position 0 — which
	// is one GM's disclosure moving onto a different secret.
	//
	// A derived anchor cannot give this back: it is a truncated sha256 over
	// (campaign, path, ordinal, first line), so the ordinal is inside the hash and
	// there is no inverse. Hence the column, and hence the flag.
	Ordinal int

	// OrdinalKnown reports whether `Ordinal` was recorded. Not a sentinel, because
	// the row crosses a SQL boundary where NULL is not a Go zero value.
	OrdinalKnown bool

	// RevealedBy is the account that performed the reveal. NOT NULL and
	// deliberately not a foreign key to `users`, for the reason `audit_log
	// .actor_id` is not (migration 0009): the row must still name the account
	// after that account is deleted, because the question it answers — who
	// disclosed this — does not expire with the account.
	RevealedBy int64
	// RevealedAt is when the reveal happened, Unix seconds on read.
	RevealedAt time.Time
	// RevertedCount is how many times sync has since overwritten the `+` back
	// to `-` and reconciliation has re-applied it. It makes the sync fight
	// visible rather than mysterious: a count that is climbing is a campaign
	// whose GM and whose sync client disagree, and that is worth surfacing
	// rather than silently winning.
	//
	// It saturates at MaxRevertedCount. The bound is a display bound, not a
	// security bound — the security bound is S-5.10's reconciliation cap (3
	// passes per file per minute), which limits how fast the count can climb.
	// Once a secret has been reverted MaxRevertedCount times the fight is
	// unambiguous and the exact number no longer changes anyone's response.
	RevertedCount int
}

// MaxRevertedCount is the saturation point for SecretReveal.RevertedCount.
//
// 999 rather than a tighter bound, because the count's job is to distinguish a
// brief fight from a sustained one, and a count that stops at 3 would report
// every persistent fight as the same small number. 999 rather than no bound,
// because an unbounded integer in a column that is read for display is a value
// that grows forever for as long as two writers disagree, and the bound is the
// statement that past this point the count has said what it has to say.
const MaxRevertedCount = 999
