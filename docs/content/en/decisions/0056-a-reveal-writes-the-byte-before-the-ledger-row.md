---
title: "0056 — The reveal endpoint writes the byte before the ledger row"
description: "A secret reveal is a one-byte edit to the author's file followed by one row in `secrets_revealed` — in that order, the reverse of the wiki editor's. A ledger row is permission rather than history: reconciliation diffs it for secrets that are now `-` and re-applies the `+`, so a row whose write failed is an instruction sitting in the table to publish that secret later."
lede: "The ledger row is permission, not history, so it cannot be written first — and the wiki editor's revision row, which is history, is the reason the two write paths look alike and disagree."
weight: 5
date: "2026-10-04"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

A GM reveals a secret. Two things have to happen: the callout's marker byte flips from
`-` to `+`, and a row lands in `secrets_revealed` recording who did it and when. The wiki
editor establishes the opposite ordering for its own writes — revision row before file —
and argues for it on the grounds that a revision row for content that never landed is
noise.

The ledger is not history, and that is the whole of the difference.

§5.6.2's precedence table puts "is this secret revealed right now?" on the **file**. The
table exists because the file is authoritative for content (S-3.1, ADR 0006) and because
the file cannot say who flipped the byte or what happened next. Reconciliation diffs the
ledger for secrets that are "now `-`" and re-applies the `+`; `reverted_count` records how
many times a sync client has fought it.

So a `secrets_revealed` row is not a statement about the past. It is an **instruction**:
*this secret has been disclosed, and if the file says otherwise, put it back.* A row
whose file write failed is an instruction sitting in the table to publish that secret
later, triggered by our failure and no act of the GM's.

## Decision

**Write the byte first, then the ledger row.**

A failed file write therefore leaves no row, which is unambiguous: reconciliation has
nothing to re-apply and the secret stays hidden. A failed ledger write leaves a public
byte and no record of who published it, and answers **500** — not 204, because the byte
may now be public and the ledger does not say who revealed it, and not 412, because the
precondition did match and telling a GM their reveal conflicted would send them to
reconcile a conflict that does not exist. A retry converges.

There is no compensating write. A second write that removed the byte would narrow the
window rather than close it, and it would be a write that can itself fail.

`store.RevealSecret` is one transaction, so a ledger failure leaves **no row at all**
rather than a partial one, and no row is something reconciliation never re-applies.

## Consequences

The failure mode is asymmetric in the safe direction: every path leaves the secret hidden,
and the two that can leave it public leave it public with a 500 and a log line carrying
`secrets.ledger_failed`.

A GM whose reveal returns 500 cannot tell from the status whether the byte flipped. That is
deliberate — a status implying "nothing happened" would be a lie about a byte that may be
public — and it is why the client re-reads the page rather than assuming.

The ordering is the opposite of the editor's, which will look like an inconsistency to a
reader comparing the two write paths. The comment at the call site says so and gives the
reason, because the two paths look identical in a diff and disagree about which of two
records goes first.

## Alternatives considered

**Revision row before byte, matching the editor.** Rejected: it makes a ledger row an
instruction that our own failure armed. The editor's revision row is history — it describes
a document that exists — so the shared argument does not apply.

**One transaction spanning both.** Impossible: one is a SQLite row and the other is an
`os.Root` rename on a filesystem. There is no transaction across them, and pretending
otherwise is how the ordering question arises at all.

**Ledger first, then byte, with a compensating delete on failure.** Rejected: it adds a
second failure mode and narrows rather than closes the window.