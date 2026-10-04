---
title: "0026 — A cross-campaign wikilink is spelled `[[/campaign/Page]]`, with the leading slash"
description: "A leading slash is what makes a cross-campaign wikilink's target campaign written down in the author's text rather than guessed, which is what ADR 0017 needs. It does not make the two readings disjoint — the resolver retries a failed relative reference as a bare page name."
lede: "ADR 0017 requires a cross-campaign link to render identically for every viewer, which is only possible if the target campaign is written down in the author's text and never looked up. The spelling that achieves it is Obsidian's own vault-absolute form, because without the slash a reference is ambiguous with a relative path."
weight: 220
date: "2026-10-01"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

[ADR 0017]({{ "decisions/0017-cross-campaign-links-never-inline/" | relURL }}) settles that a
cross-campaign wikilink renders as a plain hyperlink, is never inlined, and carries the literal
wikilink text as its label — because the linking page's HTML must be **byte-identical for every
viewer**, or the two-variant render cache stops existing.

That requirement has a consequence the record does not spell out. A viewer-independent
`<a href="/c/{other}/wiki/{path}">` is only possible if `{other}` and `{path}` are already known
at render time. If resolving the reference involved a *lookup* — find the campaign that has a page
called `Some Page` — then whether the lookup could happen would depend on who is asking, and the
href would vary by viewer. The requirement therefore pushes the campaign name out of the lookup
and into the source text.

So the question is the grammar, and the obvious answer is wrong. `[[Other/Foo]]` looks like a
cross-campaign link, and it is ambiguous with a relative path: does `Other` name a campaign, or a
directory inside this one? Reading the first segment as a campaign makes resolution depend on
whether a directory called `Other` happens to exist in this vault — so `[[deep/../Goblin]]`
resolves differently in two campaigns that both contain a `Goblin.md`, and an author cannot predict
which. Reading it as a path means there is no way at all to name another campaign.

## Decision

A cross-campaign reference is **vault-absolute**, written with a leading slash:
`[[/public-post/Some Page]]`.

The leading slash is Obsidian's own spelling for a path from the vault root, and it is what makes
the cross-campaign target **written down**. A reference beginning `/` can only be vault-absolute,
and one that does not is read as relative first.

**The two readings are disjoint in the *authoring* sense and not in the *resolving* sense, and
this record originally claimed the stronger thing.** It said the slash "makes the two readings
disjoint" and that a slashless `[[Other/Foo]]` "can only be relative". Both halves of that are
about what the text *says*, and neither is about what the resolver then does:

> A reference that fails the relative reading is re-tried as a **bare page name**.
> `recordOutside` step 3 takes `path.Base` of the target and looks that up across the visible
> campaigns.

So `[[public-post/town-notice]]` written from inside `public-post` — which this record says is a
relative link naming `notes/public-post/town-notice` — resolves anyway, because the relative
reading failed and the basename `town-notice` was found. It reaches **the same page** the slashed
spelling reaches, which is why nothing about it looked broken.

The relative reading is still tried **first** and still wins whenever it finds anything, which is
what keeps `[[deep/../Goblin]]` meaning one thing. And the fallback is the same one a bare
`[[Goblin]]` uses, which is why it is broad: it is what makes a broken-link report able to say
"probably meant". Neither is changed by this correction — **the code is right and this sentence
was wrong**, and `TestASlashlessCrossCampaignReferenceResolvesByItsBasename` holds the difference
so it cannot be re-claimed.

What a reader should take from the correction: **prescribe the slash** — both readings agree on
it, it is unambiguous, and it survives a page being moved up a directory — but do not rely on a
slashless cross-campaign spelling being *broken*, because it may well resolve to the page you
meant.

The consequence is stated rather than discovered: **a reference with a leading slash never resolves
within the home campaign.** That is the cost, and it is the right one, because the alternative
costs more.

### What is rendered

- A vault-absolute reference becomes `<a href="/c/{slug}/wiki/{path}">literal text</a>`, for every
  viewer, identically. No lookup, no title lookup, no inlining. The target's existence is not
  consulted and its absence is not reported — the reader who follows the link and gets a 404 learns
  the same thing a reader would learn in any wiki.
- A **cross-campaign embed** is refused. Rendered as a non-interactive span carrying the reason. The
  refusal covers assets as well as pages, which is stricter than required: an `<img>` would be the
  safe direction, and a uniform rule is the one that cannot be half-remembered.
- An **unqualified name** that happens to exist in another campaign the viewer may see resolves
  *only in the index-time record*, never in the markup. It cannot produce a viewer-independent
  href — that is the whole point — so the rendered link points at the home campaign's address and
  the record names the campaign that actually has the page. The broken-link report is the only
  place an unqualified cross-campaign reference surfaces.

### Where the guarantee is structural

Three mechanisms, none of them a convention:

1. `Resolver.Links` and `Resolver.Link` have **no parameter through which a visible set could
   arrive**. Not "they ignore it" — the vocabulary is absent. Probing another campaign is not a
   mistake available on the render path; it requires adding a field.
2. `VisibleCampaign` carries a slug and an index and **no `*Root`**. Nothing downstream can read
   another campaign's files even if a future change wanted to.
3. Nothing in the resolver **returns another page's bytes**. Inlining is an absent capability, not
   a check that could be forgotten.

The stronger property is the third one: a caller wanting an embed filled goes and renders the
target itself, through the render cache, for a target the resolver has already established is
local.

## Consequences

- An author writing `[[Other/Foo]]` gets a *relative* link to `Other/Foo` inside their own
  campaign, and a broken one if no such directory exists. It is not an error and it is not
  reported as one. This is the most likely way to be surprised by this decision, and it is
  CommonMark-and-Obsidian consistent: a relative path is what a relative-looking thing means.
- The page index is still needed, but only for *local* resolution: a relative path, and a bare
  basename. The cross-campaign case needs no index at all, which is a simplification the
  alternative would have lost.
- The broken-link report for a campaign is complete for local references and incomplete for
  cross-campaign ones by construction. A missing cross-campaign target is invisible to it. This is
  the accepted price of a viewer-independent href and it is stated in the resolver's own docs, so
  the next reader of a report that "missed" one is not left guessing.
- If the vault-relative form proves too easy to get wrong in practice, the alternative is a
  distinct sigil — `[[@campaign:Page]]` — which is unambiguous without a prefix. It was rejected
  because it invents syntax where Obsidian already has one, and an author migrating an existing
  vault would have to learn a spelling that appears nowhere in the tool they wrote the vault in.

## Alternatives considered

**Read the first segment as a campaign when no such directory exists.** Rejected: resolution then
depends on the contents of the home vault, so `[[deep/../Goblin]]` resolves differently in two
campaigns that both contain `Goblin.md`. An author's link would change meaning because someone
else created a directory.

**Look the target up and inline it when it is in a campaign the viewer may see.** Rejected
directly by ADR 0017. It also destroys the render cache: the variant count would depend on how
many campaigns the viewer can see, so "exactly two variants" stops being true.

**Infer the campaign from the link's target when the target is unique across visible campaigns.**
Rejected for the same reason as the first alternative, with an extra failure: uniqueness is a
property of the *viewer's* visible set, so the same link resolves for a GM and breaks for a player.

**A separate link syntax for cross-campaign references.** Rejected as inventing syntax where
Obsidian has one. The cost is the ambiguity above, and the ambiguity is what the leading slash
removes for free.
