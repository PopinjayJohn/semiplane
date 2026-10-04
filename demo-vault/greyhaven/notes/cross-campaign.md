---
title: Another campaign
---

## The leading slash, and what it buys

A campaign is its own content root, and a page in one can point at a page in
another. The spelling is a **leading slash**:

```
[[/public-post/town-notice]]     another campaign
[[public-post/town-notice]]      a relative path inside *this* campaign
```

That is the whole rule, and the slash is doing all the work. Written without it,
`public-post/town-notice` is relative to the directory of the page that wrote it — from
`notes/`, that is `notes/public-post/town-notice`, exactly as Obsidian would read it.
The leading slash is Obsidian's own spelling for a link at the top of the vault, and
here the top of the vault *is this campaign*, so a vault-absolute reference whose first
segment is not a directory this campaign has is a reference into another campaign.

Here is one, pointing at Greyhaven by name:

[[/greyhaven/index|the showcase campaign's front page]]

It resolves, and it is worth noticing what that resolution did and did not do.

## What resolution is allowed to look at

The rendering of this page — its HTML, its byte length, everything a browser receives —
**never consults another campaign**. That is a property of the code rather than a
promise in it: the render path is handed the home campaign's index and its content
root and has no way to find anything else.

So this page's bytes are **identical for every reader**, including a reader who may see
only this campaign and a Game Master who may see all of them. Inlining the target would
change that, and the render cache holds exactly two variants of a page — keyed on
whether secrets are included. If the variant count became "one per set of campaigns the
reader can see", it would be unbounded and per reader, and the cache would stop being a
cache.

Which is why an **embed** across the boundary is refused rather than merely
discouraged: `![[/public-post/town-notice]]` is a request semiplane declines. See
[[embeds]].

## The answer depends on the reader; the markup does not

Whether a cross-campaign link *works* is a different question from what it looks like,
and it is answered somewhere else. When the campaign's links are checked, the reader's
accessible set of campaigns is asked — and a campaign outside it is not consulted at
all. It cannot be: the set is the input, and a reference into a campaign that is not in
it comes back unresolved, identically to a reference that names nothing.

Two properties fall out:

- **A broken link is not an existence oracle.** The answer for a reader who may see the
  target campaign and the answer for a reader who may not is *the same*. Nothing about
  the report distinguishes them, so a link cannot be used to discover which campaigns
  exist.
- **The linking page cannot disagree with itself.** A GM sees a working link where a
  player sees a dead one, and both of them are reading bytes that were rendered without
  knowing who they were.

## Where the demo's other two campaigns are

The seeded demo instance has three campaigns. This one is the showcase; the other two
are boundaries, and this page deliberately does **not** link to them, because a link
here has to resolve for whoever is reading it:

- **The Public Post** — `public`. Reads with nobody signed in at all: no membership, no
  account, an anonymous 200. Its assets are fetched the same way, and that is the
  boundary an operator gets wrong most often — the one nothing demonstrates.
- **The Forgotten Realm** — private, and its `system:` is a system this binary does not
  have. Its pages answer 200 with their content while its **game refuses to start**,
  and the refusal names the id it wanted. That is the §10.8 requirement and it has
  exactly one demonstration in the vault, which is why `make demo-check` is
  deliberately wired to turn **red** if a Pathfinder plugin is ever registered.

Change this page's link above to `/public-post/index` once both are seeded and the
demonstration becomes a live cross-campaign hop rather than a resolved self-reference;
the property being demonstrated — markup identical, answer reader-dependent — is the
same either way.

Back to [[index]].