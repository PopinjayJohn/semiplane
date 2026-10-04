---
title: "0057 — The ledger records where a secret sat"
description: "`secrets_revealed` gained a nullable `ordinal` column, because §5.6.3's repair re-associates a drifted row by the callout's position and a truncated sha256 cannot give the position back. Never backfilled, never corrected, and never in the primary key — it is a repair hint rather than an identity."
lede: "A derived anchor hashes the ordinal, so it cannot give it back. Without the column §5.6.3's repair has nothing to re-associate by, and the edit it was written for is a GM rewriting a revealed secret's first line."
weight: 5
date: "2026-10-04"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

§5.6.3 gives a secret two possible names. An Obsidian block id is stable across any
edit. The derived anchor — `sha256(campaign_id, path, ordinal, first-line-of-body)[:12]`
— is stable across body edits *below* the first line and across nothing else, and the
record calls that fallback "best-effort and self-healing".

Self-healing is the part that needed a column. The record's sentence for it is: when the
watcher finds a ledger row whose anchor no longer resolves, re-associate it by
`ordinal`, carry the revealed state across, and log `secret_anchor_drift`.

A **truncated** sha256 cannot give the ordinal back. Truncation is what makes a
twelve-character hash a plausible key, and it is also what makes the ordinal
unrecoverable: there is no inverse to run. Without the column the repair path has
nothing to re-associate by, and the honest outcome for every drifted row is
`OutcomeUnresolved`.

Migration 0011 shipped `anchor` as opaque `TEXT` with deliberately **no CHECK**, on the
argument that the resolver owns the shape. That was right about the *anchor* and silent
about the *ordinal*: the resolver turned out to need a second value that the anchor
cannot contain.

## Decision

**Add a nullable `ordinal` column, recording the callout's position among the secrets on
the page as last observed, and a partial index over it.**

Nullable, with no `NOT NULL` and no `DEFAULT`. A `NULL` means *this row predates the
column, or its position was never recorded* — and that is the honest value. Backfilling
`0` would assert that every historical secret sat at the top of its page, which is false
for all but the first, and `content.Reassociate` would then re-point those rows onto
whatever callout now holds position 0: one GM's disclosure moved onto a different
secret.

So a `NULL` row is reported as drift and never repaired by position. The cost is that the
repair does not help rows written before the migration; the benefit is that it never
helps them *wrongly*, and the second is what S-5.10 rests on.

`domain.SecretReveal` carries `Ordinal int` **and** `OrdinalKnown bool` rather than a
sentinel, because `NULL` and `0` are different facts and the row crosses a SQL boundary
where `NULL` is not a Go zero value.

**Not in the primary key.** The key stays `(campaign_id, path, anchor)`. The ordinal is a
repair hint, not an identity: including it would let one disclosure be inserted twice
under two positions, and a ledger holding two rows for one secret is a ledger whose row
count means nothing.

**Never corrected after insert.** `ON CONFLICT DO NOTHING` leaves a re-reveal's position
alone, because a repair that rewrites its own evidence cannot detect drift — the evidence
is what it just changed.

## Consequences

§5.6.3's repair now works for the edit it was written for: a GM rewriting the first line
of a revealed secret. Without the column that reveal becomes a row naming something that
is no longer there, which the next sync reversion will not re-apply — the reveal is not
lost from the ledger, it is lost from the *file*, quietly, by a client the GM has never
heard of.

Rows written before the migration get no repair. They are reported, which is the
correct outcome: a GM sees drift rather than a disclosure moved onto the wrong secret.

The column is written by the reveal route, which resolves the callout against the page in
that very request and so knows the position without approximation. Writing `NULL` there
would throw away the only repair hint §5.6.3 has.

## Alternatives considered

**Recover the ordinal by widening the hash to 64 characters.** Rejected: the ledger is
keyed on the value the resolver computes from *source bytes*, so a wider hash is a
different anchor and would orphan every existing row on the first upgrade.

**Store the ordinal inside the anchor string.** Rejected: it makes the key a composite
that SQL cannot compare for equality against a caller-supplied anchor, and §5.6.3's
resolution order needs an anchor to be a single opaque value the store treats as
identity.

**No column; report every drifted row forever.** Rejected: it is the status quo the plan
assumed was fine and it is not. The exact-match path covers edits below the first line,
which is the common edit, so the defect is invisible until a GM does the one thing the
feature was built to survive.