---
title: "0058 — Every reconciliation failure path leaves the secret hidden"
description: "S-5.10's rule, made structural: the reconciler has no code path that writes a `+` it could not verify, convergence is on the state rather than on a flag, and the shipped pass limit is asserted rather than left to the constant. An accidentally revealed secret is treated as worse than a late or dropped reveal."
lede: "Every part of reconciliation can fail and the failures are not symmetric, so the table enumerates them and each resolves toward hiding — the only direction that is recoverable."
weight: 5
date: "2026-10-04"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

Obsidian syncs a copy of a vault. A GM reveals a secret in the web app; the sync client
has a version from before the reveal and periodically writes it back, turning `+` into
`-`. Nothing tells either party this happened. The GM believes the secret is public; it
is not, and their players have already been told the scene.

Reconciliation is the answer: on any change to a page, diff the ledger against the
callouts' current markers and re-apply the `+` through the ordinary atomic write.

The problem is that every part of that can fail, and the failures are not symmetric.
A reconciliation pass that gives up early has cost a GM a disclosure they made. A
reconciliation pass that presses on after it cannot tell what it is looking at has
published a secret nobody read, and an accidentally revealed secret is worse than a late
reveal in a way that is not a matter of degree — one is recoverable by telling the party,
the other is not.

## Decision

**Every failure path resolves toward hiding.** Enumerated, because a principle that is
not enumerated is a principle that is not implemented:

| Failure | Outcome |
|---|---|
| the pass budget is exhausted | hidden; `secret.reconcile_capped` at **error** |
| the file changed since it was read | hidden; back off, retry on the next change event |
| the write failed | hidden; **nothing** is counted as reverted |
| the ledger could not be read | hidden; **nothing** is decided |
| a row's anchor does not resolve | hidden; `secret.anchor_drift`, the row is reported |
| the callout is nested inside another | hidden; refused, matching what the reveal endpoint refuses |

Three structural consequences rather than three checks:

**There is no code path that writes a `+` it could not verify.** The writer's contract is
that returning `ErrReconcileConflict` means *nothing was written*, and
`internal/content` says in the same comment that no test in that package can catch an
implementation that violates it — because an implementation that writes first and
compares afterwards returns the same error for the same input. The obligation belongs to
the implementation in `cmd/server`, and the test for it asserts on the **inode**, since a
temp-file-and-rename writer produces identical bytes while having destroyed what it was
supposed to preserve.

**Convergence is on the state, not on a flag.** A successful pass rewrites one byte,
which settles as a change of its own and arrives back at the reconciler. There is no
re-entrancy guard because a pass that reverted nothing has nothing pending and writes
nothing. A guard would be a second mechanism for a property the design already has.

**The cap is per file per minute, and the shipped value is asserted.** `ReconcilePassLimit`
is 3. Every budget test is written against the constant — right for a boundary test, and
wrong for the one test that matters, because raising the constant to a thousand moves the
boundary with it and leaves the suite green. Each successful pass produces a settled
change, which produces another pass; an unasserted limit is a loop nobody bounded.

## Consequences

A GM and a sync client that never stop agreeing produce a fight that stops after three
writes a minute and says so — `secret.reconcile_capped` at error, surfaced by S8's banner
rather than only in a log. The secret is hidden, which is the safe answer, and the GM has
something to act on.

A stale `If-Match` costs one read and leaves the file byte-identical. It does not narrow
to a window we can hit reliably: the digest comes from `pages.content_hash`, and the
indexer's own write lands between a settle and the reconciler reaching the file, so a
precondition spanning the settle would reject almost every write.

`secret.reverted` and `secret.anchor_drift` are readable in a log and **invisible in
`/readyz`** — their counters are registered inside `internal/observability`, one block per
subsystem, and nothing adds them for this path. Recorded rather than worked around: an
`init()` would have registered them in one line and would also have been a second door
into the event registry.

## Alternatives considered

**Press on past a conflict and re-read.** Rejected: it is a read-modify-write loop
against a writer that does not stop, with a disclosure at the end of it.

**Reveal first, reconcile later, and tell the GM which secrets are at risk.** Rejected:
it publishes before verifying, which is the failure the rule exists to prevent.

**Cap per campaign rather than per file.** Rejected: a campaign with a hundred pages
fights one at a time, and a cap shared across them means one page's fight silently
withholds a *different* page's reveal.