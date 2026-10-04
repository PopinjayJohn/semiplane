---
title: "0059 — A redacted secret is silent"
description: "UI §4.10 asks whether a redacted secret is announced to assistive technology, which risks disclosing its existence. The answer is nothing at all: no element, no class, no `aria-hidden`, no placeholder, no live region. The prose gap is left as it falls, because everything available to close the sighted and screen-reader asymmetry is itself a disclosure."
lede: "Omission settles the body and not the absence, so the question is whether to announce that something was hidden. A redacted secret says nothing — including the fact that it exists."
weight: 5
date: "2026-10-04"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

§5.6.1 is that the secret body is **absent** from the response a non-GM receives.
Redaction is omission: not `display:none`, not a `hidden` attribute, not an inline
comment, not a CSS class. Every one of those ships the text in the HTML, where any
player can recover it from view-source.

That settles the body. It does not settle the *absence*. §4.10 asks whether a redacted
secret is announced to assistive technology at all — and announcing "a secret was hidden
here" discloses that something was hidden, which is a weaker form of the same
disclosure.

The two audiences experience different documents. A sighted player may notice a prose
seam where a callout was removed mid-sentence. A screen-reader user, under the default,
will not: the sentence simply runs on. The asymmetry is real, and the question is
whether anything should be done about it.

## Decision

**A redacted secret is silent. Nothing is added to the tree.**

No element, no class, no `data-secret*`, no `aria-hidden`, no placeholder, no live region,
no "secret hidden" text. The prose gap is left as it falls.

Three reasons, and the third is the one that decides:

1. **An announcement is a worse disclosure than a stub.** A stub is opt-in and read by
   someone who has gone looking at that spot. An announcement is pushed to everyone, at
   the exact position, unasked.
2. **`aria-hidden` adds.** §5.6.1's mechanism is omission; `aria-hidden` puts a node in
   the tree whose presence says something was there. It is hiding, with extra steps.
3. **The asymmetry is correct, and matching it requires a disclosure.** Everything
   available to close the gap between a sighted player and a screen-reader user is
   something added to the tree, and everything added to the tree says a secret was here.

The GM's own controls are the exception, and a deliberate one: the reveal control's
outcome and the reconcile-capped banner are `role="alert"` with `aria-live="assertive"`,
because a GM needs to know whether their action took effect and whether the system chose
to hide something on their behalf. §7.5's list of assertive surfaces is a 412, a reveal,
and a capped reconciliation — three, and the editor's own `TestTheSaveStateIsAPoliteStatus`
counts them.

## Consequences

A screen-reader-using player and a sighted player get different documents, and neither is
told why. That is the accepted cost of §5.6.1 and it is not a defect to be scheduled
away.

The per-campaign **stub** is a separate, opt-in, never-default surface for a GM who wants
their players to see *that* something was withheld. It is not this record: it needs a
sanitiser change, and it is a deliberate act by the campaign's owner rather than a default
that discloses.

Two tests hold the decision rather than the comment: one asserts the player-facing
document's accessibility tree carries no trace of a hidden secret, and one asserts the
GM's alerts are announced and assertive. `TestAPageWithNoSecretsRendersNoDisclosurePanel`
holds the third — an empty "Disclosures" panel on a page with no secrets tells a GM their
page has secrets, which is the existence disclosure running in the other direction.

## Alternatives considered

**A visually-hidden "a secret is hidden here".** Rejected: hidden text is announced, so
"visually hidden" is a description of the sighted experience and not of the other one.

**`aria-hidden` on the callout's slot.** Rejected: it adds a node whose presence is the
disclosure, which is the mechanism §5.6.1 rules out.

**A role that conveys "content removed" without naming it.** Rejected: there is no such
role, and the nearest candidates (`deleted`, `deletion`) are announced as deletions,
which is a claim about the document that is false.

**Close the sighted/screen-reader asymmetry by inserting a filler.** Rejected: a filler at
a *known position* is an oracle for where the secrets are, which discloses their layout
to everyone.