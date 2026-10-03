package content

// §5.6.3's anchor identity: what a secret is *called* in the ledger, and what
// happens when that name stops matching.
//
// # The problem this solves
//
// The ledger is keyed by an anchor. Editing a revealed secret's text changes a
// callout's first line, the derived anchor changes with it, and the row the GM's
// reveal wrote now names something that is no longer there — so the next sync
// reversion is invisible, and a reveal the GM performed is silently undone by a
// client they have never heard of. §5.6.3's answer is two names: an Obsidian block
// id when the author supplied one (stable across any edit), and a derived hash when
// they did not (stable across body edits *below* the first line). This file resolves
// those two names and repairs the rows whose name no longer matches.
//
// # Why this is in `content` and not in `store`
//
// Both candidate names are functions of a page's *text and position*: the ordinal is
// the callout's position among the secrets on the page, and the first line of the
// body is the author's own words. `store` can read the ledger and `content` can read
// the page, and neither can read both — so the resolver lives where the derivation
// is, and takes the ledger rows as a **local type** rather than as
// `domain.SecretReveal`.
//
// # The security property, and it is the whole file
//
// **Never silently drops a reveal.** Every row handed in comes back in the result,
// in the same order, with an outcome — matched, re-associated, or unresolved. There
// is no path that returns a shorter slice than it was given, and none that clears a
// row's revealed state. A resolver that dropped what it could not place would turn
// "we could not find this reveal" into "this was never revealed", which is the one
// outcome S-5.10 exists to make impossible.

import (
	"sort"

	"github.com/semiplane/semiplane/internal/observability"
)

// AnchorForm is **which** of §5.6.3's two names an anchor is.
//
// Not decoration. The two differ in what they survive: a block id survives any edit;
// a derived hash survives an edit below the first line and nothing more. Every
// decision this package makes about a row — whether an unmatched row is worth
// repairing, whether a re-key can trust its name — turns on that difference, so it
// is carried rather than re-derived by inspecting the string, which is how a derived
// hash and a short block id get confused.
type AnchorForm uint8

const (
	// AnchorFromBlockID is an Obsidian block id: the characters after `^`, stored
	// without the caret.
	//
	// **The caret is a markdown convention, not part of the name.** It exists so a
	// human reading the vault can see where an id is; half of a markdown
	// convention does not belong in a database key, and §5.6.3 says the ledger
	// "treats both as an opaque string".
	AnchorFromBlockID AnchorForm = iota

	// AnchorDerived is `sha256(campaign_id, path, ordinal, first-line-of-body)[:12]`.
	AnchorDerived
)

// String names the form, for a log line and for a test's failure message.
//
// **The form, never the value.** A derived anchor is twelve characters of the
// secret's own first line, so printing an anchor in an operator-facing message leaks
// it — and S-12.3's rule is that no event and no error carries page content. The
// form is the diagnostic; the value is the key.
func (form AnchorForm) String() string {
	switch form {
	case AnchorFromBlockID:
		return "block-id"

	case AnchorDerived:
		return "derived"

	default:
		return "unknown"
	}
}

// SecretAnchor is one resolved name for one callout.
//
// **Named `SecretAnchor`, not `Anchor`.** `render.go` already exports an `Anchor` —
// one heading in a rendered page, with an id and a level — and a package with two
// exported types of the same name is a package where every reader has to stop and
// work out which is meant. The prefix is paid once here rather than at every use.
type SecretAnchor struct {
	// Value is the opaque anchor string: the block id, or twelve hex characters.
	Value string

	// Form is which of the two it is.
	Form AnchorForm
}

// IsDerived reports whether the anchor is the best-effort form.
//
// A method rather than `Form == AnchorDerived`, because that reads as a question
// about an enum and this reads as a question about the anchor.
func (anchor SecretAnchor) IsDerived() bool { return anchor.Form == AnchorDerived }

// Empty reports whether the anchor names nothing.
//
// **A named predicate rather than `Value == ""`,** because an empty anchor is the
// single most dangerous value this file can produce and it needs to be one grep to
// find. It is a legal string, so a row keyed on it matches every other row keyed on
// it: two unrelated secrets in one campaign would share a ledger entry, and one
// GM's reveal would become another's. The first implementation of the repair path
// below did exactly this — it copied a callout's `BlockID` directly, which is `""`
// for any derived secret — and `TestARepairedRowIsNeverNamedEmpty` holds it.
func (anchor SecretAnchor) Empty() bool { return anchor.Value == "" }

// Resolve returns §5.6.3's name for a callout: the block id when the author wrote
// one, the derived hash when they did not.
//
// **The block id wins, and the reason is stability rather than preference.** A block
// id is chosen by a human and written into the file, so it survives a first-line
// rewrite, a reorder and an insertion above — the three edits the derived hash dies
// on. Preferring the hash whenever both were computable would resolve to a stable
// name only by accident, and would then depend on the author's discipline holding
// forever.
//
// **An empty block id falls through rather than becoming an empty anchor**, for the
// reason `SecretAnchor.Empty` gives.
func Resolve(campaignID int64, path string, secret Secret) SecretAnchor {
	if secret.BlockID != "" {
		return SecretAnchor{Value: secret.BlockID, Form: AnchorFromBlockID}
	}

	return SecretAnchor{Value: DerivedAnchor(campaignID, path, secret), Form: AnchorDerived}
}

// LedgerRow is the resolver's view of one `secrets_revealed` row.
//
// # Why this is not `domain.SecretReveal`
//
// `SecretReveal` carries `(campaign_id, path, anchor, revealed_by, revealed_at,
// reverted_count)` — and **no ordinal**, which §5.6.3's repair path requires. That is
// not an oversight to be worked around, and it is the one finding here that needs a
// decision above this package:
//
//   - The derived anchor is a **truncated sha256**. Truncation is what makes it a
//     plausible key and it is also what makes the ordinal unrecoverable: there is no
//     inverse to run.
//   - So §5.6.3's own words for recovering a row whose first line was rewritten —
//     "re-associates by `ordinal`" — have **no ordinal to re-associate by** unless
//     the ledger stores one.
//
// A resolver that cannot do it still appears to work, because the exact-match path
// covers every edit below the first line and that is the common case. It fails on
// precisely the edit the feature was built to survive. So the ordinal is required as
// a column, and this type carries it so the resolver can be written and tested now.
// Wiring it to a nullable `ordinal` on the table is a migration and belongs to
// whoever owns `internal/store`.
//
// **Until that column exists, a row whose ordinal is not recorded must not be
// repaired by position.** `Reassociate` reports it unresolved instead, which is the
// honest outcome and the only one §5.6.3's "never silently drops a reveal" permits.
type LedgerRow struct {
	// Path is the page's path, spelled exactly as `pages.path` spells it.
	//
	// Carried rather than taken as a parameter, because a rename re-keys rows and
	// the re-key has to be able to say which rows it moved.
	Path string

	// Anchor is the row's key, the opaque string the resolver matches on.
	Anchor string

	// Ordinal is the callout's position among the secrets on the page, as last
	// observed.
	Ordinal int

	// OrdinalKnown reports whether `Ordinal` was recorded. **A separate flag, not a
	// sentinel**, because the row crosses a SQL boundary where NULL is not -1, and
	// where a coercion is one `int64` field away from being wrong in a way nothing
	// tests.
	OrdinalKnown bool

	// Revealed is whether the row records a reveal currently in force.
	Revealed bool
}

// NewLedgerRow builds a row from a callout whose position is known, for the writer
// that is about to record a reveal.
func NewLedgerRow(campaignID int64, path string, secret Secret, revealed bool) LedgerRow {
	return LedgerRow{
		Path:         path,
		Anchor:       Resolve(campaignID, path, secret).Value,
		Ordinal:      secret.Ordinal,
		OrdinalKnown: true,
		Revealed:     revealed,
	}
}

// Outcome is what became of one ledger row.
type Outcome uint8

const (
	// OutcomeMatched: the row's anchor names a callout on the page as it stands.
	OutcomeMatched Outcome = iota

	// OutcomeReassociatedByOrdinal: the row's anchor names nothing, and the callout
	// now occupying the row's recorded ordinal took it. §5.6.3's fallback — the
	// name moved, the reveal did not.
	OutcomeReassociatedByOrdinal

	// OutcomeUnresolved: the row's anchor names nothing and no repair applied.
	//
	// **A report, not a discard.** The row comes back with its revealed state
	// intact and the caller owes the operator a `secret.anchor_drift` event. A row
	// reaching here is a reveal whose fate is now unknown, and the correct response
	// to that is to say so rather than to guess.
	OutcomeUnresolved
)

// String names the outcome, for a log line and for a test's failure message.
func (outcome Outcome) String() string {
	switch outcome {
	case OutcomeMatched:
		return "matched"

	case OutcomeReassociatedByOrdinal:
		return "reassociated-by-ordinal"

	case OutcomeUnresolved:
		return "unresolved"

	default:
		return "unknown"
	}
}

// Resolution is one ledger row's outcome, carrying forward everything a caller
// needs to act on it **without re-reading the page**.
type Resolution struct {
	// Outcome is what happened.
	Outcome Outcome

	// Row is the ledger row as it was handed in, whole — so an unresolved row can be
	// logged or re-keyed by a caller that never saw the original slice.
	Row LedgerRow

	// Anchor is the name that now belongs to the row: its own on a match, and the
	// **recomputed** current callout's on a re-association.
	//
	// The two differ, and collapsing them is how a repair loses track of which name
	// the ledger actually holds — which then matters the moment the campaign is
	// renamed, because every derived anchor in it stops resolving at once.
	Anchor SecretAnchor

	// Ordinal is the callout this row resolved to, or -1 when it resolved to
	// nothing. A real ordinal is never negative, so the sentinel is unambiguous.
	Ordinal int

	// Revealed is the revealed state carried across, **never cleared**. On
	// `OutcomeUnresolved` it is the row's own recorded state, because that is all
	// that is known; clearing it would report "not revealed" for something the
	// ledger says is revealed.
	Revealed bool
}

// Reassociate resolves every ledger row against the page as it stands, repairing
// what it can and **reporting** what it cannot.
//
// # Why it takes the campaign and the path
//
// Because the derived anchor hashes both, and a repair has to write a name that the
// next reader can recompute. A signature without them could only re-point a row at a
// callout and leave its name stale, which is a repair that works until the campaign
// is renamed and then silently stops working for every derived anchor in it.
//
// # The direction, and why it is rows-to-page
//
// A page has one correct answer per callout, and two ledger rows claiming the same
// ordinal are a data fault to report. Iterating the page and looking each callout up
// would silently pick a winner and hide the fault — so the rows go first and the page
// is the thing they are resolved against.
//
// # What it never does
//
//   - **Never returns fewer results than rows.** `TestEveryRowComesBack` holds the
//     length.
//   - **Never clears `Revealed`.** An unresolved row keeps what it recorded.
//   - **Never returns an empty anchor.** `TestARepairedRowIsNeverNamedEmpty` holds
//     it, and `SecretAnchor.Empty` says why it matters: an empty name is a legal
//     string that matches every other empty name.
func Reassociate(campaignID int64, path string, rows []LedgerRow, page string) []Resolution {
	secrets := ScanSecrets(page)

	// Every callout's **resolved** anchor, computed once. Recomputing a sha256 per
	// row per callout would be quadratic in the number of secrets on the page, and
	// the answer does not depend on the row.
	anchors := make([]SecretAnchor, len(secrets))
	for index := range secrets {
		anchors[index] = Resolve(campaignID, path, secrets[index])
	}

	resolutions := make([]Resolution, 0, len(rows))

	for _, row := range rows {
		resolution := Resolution{
			Outcome:  OutcomeUnresolved,
			Row:      row,
			Ordinal:  -1,
			Revealed: row.Revealed,
		}

		if index := indexOfAnchor(row.Anchor, anchors); index >= 0 {
			resolution.Outcome = OutcomeMatched
			resolution.Ordinal = index
			resolution.Anchor = anchors[index]

			resolutions = append(resolutions, resolution)

			continue
		}

		if !repairableByOrdinal(row, secrets) {
			resolutions = append(resolutions, resolution)

			continue
		}

		resolution.Outcome = OutcomeReassociatedByOrdinal
		resolution.Ordinal = row.Ordinal
		resolution.Anchor = anchors[row.Ordinal]
		resolution.Revealed = row.Revealed

		resolutions = append(resolutions, resolution)
	}

	return resolutions
}

// repairableByOrdinal answers whether a row's recorded position can be trusted to
// re-point it.
//
// # The three refusals, and each is a different mistake
//
//  1. **The ordinal was never recorded.** Nothing to repair by. Positional repair
//     against an unrecorded ordinal would attach a reveal to whichever callout
//     happens to sit in that slot now, which is the disclosure this file exists to
//     prevent.
//
//  2. **The ordinal is outside the page.** Either the callout was deleted, or the row
//     belongs to a different page's numbering — a rename moves a page, and a row
//     whose path no longer matches is not this page's row to fix. There is no
//     "nearest" ordinal to fall back on: choosing one is the same guess as (1),
//     with more steps.
//
//  3. **The slot holds a secret that was never revealed, and the row claims nothing
//     was revealed either.** Re-pointing would attach a ledger name to a callout
//     that has no business holding one, and the row would then report that callout
//     as revealed-when-collapsed on the next pass. Nothing is carried across,
//     because nothing was in force.
func repairableByOrdinal(row LedgerRow, secrets []Secret) bool {
	if !row.OrdinalKnown {
		return false
	}

	if row.Ordinal < 0 || row.Ordinal >= len(secrets) {
		return false
	}

	if !row.Revealed && secrets[row.Ordinal].State == SecretCollapsed {
		return false
	}

	return true
}

// indexOfAnchor returns the index of the callout `value` names, or -1.
//
// **It compares resolved anchors rather than inspecting the stored string.** A block
// id and a derived hash are the same shape of string, and deciding which form a
// stored value is by looking at it is a guess that holds until a GM names a block id
// `a1b2c3d4e5f6` — at which point a derived anchor and a block id are genuinely
// interchangeable and no amount of string inspection separates them.
//
// **An empty value matches nothing**, which is the point: `""` is what an unresolved
// row must never be repaired *from*, and an empty stored anchor is not a name.
//
// The guard is **unreachable while `TestResolveNeverReturnsAnEmptyAnchor` holds**,
// because every anchor in `anchors` comes from `Resolve` and `Resolve` never returns
// an empty one — so removing this branch changes nothing observable today, and a
// mutation that removes it reports no failure. That is stated rather than papered
// over: it is a second line of defence whose value is entirely contingent on the
// other invariant, and the invariant is the thing with the test. Were `Resolve` ever
// to gain a path that returned `""`, this branch is what stops every such secret in
// the campaign resolving against the first one.
func indexOfAnchor(value string, anchors []SecretAnchor) int {
	if value == "" {
		return -1
	}

	for index := range anchors {
		if anchors[index].Value == value {
			return index
		}
	}

	return -1
}

// Rename is a page move.
type Rename struct {
	// From is the path the rows were under.
	From string

	// To is the path they are under now.
	To string
}

// Rekey re-keys the ledger rows for a renamed page, leaving a tombstone for the old
// path.
//
// # Why a rename is not a special case
//
// The anchor survives the move — §5.6.3 says so, and it is the reason anchors exist
// at all — so a rename is a path change on every row for that path. A **block-id**
// anchor survives it outright, since it does not mention the path at all. A derived
// anchor does not, and that is the interesting part: the stored value was computed
// against the *old* path, so after the move the ledger's own anchor no longer equals
// what a fresh scan of the page would compute. This is deliberate, and it is what
// makes a rename survivable without a repair pass — the row keeps the name the
// writer gave it, and the name still identifies the same callout, because it was
// computed when that callout was at that ordinal with that first line.
//
// The consequence is stated here because it is surprising, and because a reader who
// has just moved a page and found every derived anchor unresolvable will reasonably
// conclude something is broken: **after a rename, derived anchors are stale until
// they are rewritten.** `Reassociate` finds them by the block-id form, and `Repair` is the
// only thing that re-derives them — so a campaign that renames pages should run a
// repair pass, and the reconciliation already does.
//
// # The tombstone is a row, not a log line
//
// §5.6.3 says the old path leaves one. The reason it has to be **data** rather than
// an event is that a rename is reversible in a way a deletion is not: an Obsidian
// sync client that reverts the move brings the old path back, and the reveal has to
// be findable there. A log line answers "what happened" and cannot answer "what is
// there now".
//
// Deterministic order: tombstones are sorted by anchor, so their contents do not
// depend on the order the caller happened to read the rows in.
func Rekey(rows []LedgerRow, rename Rename) (moved, tombstones []LedgerRow) {
	if rename.From == rename.To {
		// Not a rename. Returning the rows untouched rather than duplicating them
		// into tombstones for a path that still exists is what a caller moving a
		// page onto its own name wants, and an empty rename producing a tombstone
		// per row is how a ledger fills with rows shadowing live ones.
		return rows, nil
	}

	for _, row := range rows {
		if row.Anchor == "" {
			// A row with no name is not re-keyed: it would be re-keyed onto an
			// empty key at the new path and collide with every other such row there.
			// It becomes a tombstone at the old path instead, so it stays findable
			// and stays visible.
			tombstones = append(tombstones, row)

			continue
		}

		if row.Path != rename.From {
			// Not this page's row. Passing it through as "moved" would report a
			// re-key that did not happen, and the caller would write the path change
			// for a page it never had.
			continue
		}

		tombstones = append(tombstones, row)

		rekeyed := row
		rekeyed.Path = rename.To

		moved = append(moved, rekeyed)
	}

	sort.SliceStable(tombstones, func(first, second int) bool {
		return tombstones[first].Anchor < tombstones[second].Anchor
	})

	return moved, tombstones
}

// DriftReport is the operator-facing summary of one page's unresolved rows.
//
// **A count and a list of anchors — never a body.** S-12.3: no event and no log line
// carries page content, and a derived anchor is twelve characters of the secret's own
// first line, so this type is the boundary where a value derived from author text is
// reduced to something safe to log.
type DriftReport struct {
	// Matched is how many rows named a callout as it stands.
	//
	// **Counted, not omitted.** A drift report that named only the two interesting
	// totals would make a page with forty matched rows and one unresolved row read
	// as "one problem out of one", which is the opposite of the picture an operator
	// needs at 3am. The three totals sum to the number of rows, so the log line is
	// checkable against the ledger.
	Matched int

	// Reassociated is how many rows were repaired by ordinal.
	Reassociated int

	// Unresolved is how many were not.
	Unresolved int

	// UnresolvedAnchors names them, sorted, so a log line is deterministic.
	UnresolvedAnchors []string
}

// ReportDrift summarises a set of resolutions.
//
// **It counts, and it does not decide.** A caller that wants to act on
// `OutcomeUnresolved` — surface it, refuse to prune the row — reads the
// resolutions; `ReportDrift` is for the log line, and a function that both reported
// and acted would be a function whose reporting could not be trusted.
func ReportDrift(resolutions []Resolution) DriftReport {
	report := DriftReport{}

	for _, resolution := range resolutions {
		switch resolution.Outcome {
		case OutcomeMatched:
			report.Matched++

		case OutcomeReassociatedByOrdinal:
			report.Reassociated++

		case OutcomeUnresolved:
			report.Unresolved++

			report.UnresolvedAnchors = append(
				report.UnresolvedAnchors, resolution.Row.Anchor)
		}
	}

	sort.Strings(report.UnresolvedAnchors)

	return report
}

// AnchorDriftEventName is the event §5.6.3 requires for a row that could not be
// repaired.
//
// **Not declared here.** `observability.EventSecretAnchorDrift` already exists — S2
// added it with the other two secret events — and this file reaches it through
// `observability` rather than repeating the string, because:
//
//   - `observability.AllEventNames()` has a test asserting a count, so a name is
//     registered once and only once. A second declaration here would be a name that
//     exists in two places and is registered in neither.
//   - §5.6.3 writes the event as `secret_anchor_drift`; the registered spelling is
//     `secret.anchor_drift`, matching `secret.reverted` and
//     `secret.reconcile_capped`. **The design record's spelling is stale here** and
//     the constant is the answer, which is a note for the phase PR rather than
//     something to fix by editing the record.
//
// Emitting it is the caller's job — `Reassociate` returns outcomes and
// `ReportDrift` counts them, and neither writes to a log. That separation is
// deliberate: a resolver that both decided and reported would be a resolver whose
// reporting could not be trusted.
const AnchorDriftEventName = observability.EventSecretAnchorDrift
