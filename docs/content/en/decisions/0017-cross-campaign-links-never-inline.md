---
title: "0017 — Cross-campaign links never inline"
description: "A wikilink may point into another campaign. Its content is never inlined, because inlining breaks the two-variant render cache."
lede: "The architecture record asks whether authors may *write* a cross-campaign wikilink, and leaves it as a product call. The answer is constrained by something the record does not connect: the render cache is permission-neutral by construction."
weight: 170
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

§16.1 of the architecture overview:

> **Cross-campaign wikilinks** — resolution already respects viewer permissions. Whether authors
> should be able to *write* them is a product call. Default: allowed within campaigns the viewer
> can see.

Resolution is settled. Writing was left open. Answering it requires connecting it to something
else in the record.

§5.5 makes the render cache **permission-neutral by construction**: exactly two variants per
page, keyed `(campaign_id, path, content_hash, include_secrets)`, and the explicit statement
*"never a per-user cache"*.

That invariant has already been broken exactly once, deliberately: `[!secret]` made the body
viewer-dependent, which is why [0016]({{ "decisions/0016-salted-etag/" | relURL }}) had to salt
the `ETag`. One exception, accounted for, with a mechanism to keep it from being confused with
the other variant.

## Why inlining would break it a second time

If a cross-campaign link inlined its target's rendered content, the linking page's body would
depend on **which campaigns the viewer can see**. The number of variants would then be a function
of the viewer's membership — not two variants, but one per visible-campaign-set. The cache key
would need that set. That is precisely the per-user cache §5.5 rules out, and unlike the secret
exception there is no bounded set of variants to enumerate.

So the trade is not "inlining is nicer" against "inlining is worse". Inlining is not available
at this price.

## Decision

**A cross-campaign wikilink renders as a plain hyperlink. Its content is never inlined.**

```html
<a href="/c/other-campaign/wiki/Some%20Page">Some Page</a>
```

- The label is the **literal wikilink text** — no lookup of the target's title, no rendering of
  the target's front matter. Nothing is read from the other campaign at render time.
- Therefore the linking page's HTML is **byte-identical for GM, player, and anonymous** alike.
  The cache invariant holds with no new variant.
- A viewer who cannot see the target clicks and gets a 403. The link discloses nothing the
  author did not already choose to write down.

**Cross-campaign *embed* (`![[Page]]`) is refused outright.** Embedding is inlining, so it is
subject to exactly the argument above.

Index-time resolution still records the link in the broken-link report, scoped to the viewer's
visibility — never probing a campaign the viewer cannot see.

## Consequences

- **A test asserts the byte-identity.** Render one page containing a cross-campaign link as GM,
  as player, and as anonymous, and diff the three bodies. This is the assertion that keeps the
  implementation from drifting into inlining, which is the intuitive thing to write and the
  thing that would silently destroy the cache.
- There is a real cost: a cross-campaign reference shows the link text, not the target's content.
  A GM who wants a page's prose inlined has to copy it, or accept a link. Accepted.
- Resolution within a campaign is unaffected: relative path first, then basename index within
  the campaign, then within campaigns the viewer can see. That is the existing §5.4 behaviour.
- Authors who genuinely want a plain absolute link can already write one. What this record adds
  is that the *wikilink* form works across campaigns and renders predictably.

## Alternatives considered

**Disallow cross-campaign links entirely.** Simplest, zero new surface. Rejected because it
leaves no supported way to express a genuine cross-campaign reference, and the demo vault needs
to demonstrate one.

**Allow inlining, and put the viewer's visible-campaign set in the cache key.** Fully capable,
and what a user copying a shared vault would expect. Rejected: it is the per-user cache, which
§5.5 excludes, and it multiplies variants in a way that no salt or `Vary` header can bound.

**Allow inlining only for public campaigns.** A bounded exception, since an anonymous viewer has
exactly one view of a public campaign. Rejected as a special case that would need its own cache
key variant, its own `Vary`, and its own test — for a case the plain hyperlink already covers.