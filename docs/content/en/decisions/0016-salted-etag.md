---
title: "0016 — Salt the ETag with include_secrets"
description: "The design record's ETag rule is a defect. Two body variants cannot share one validator without leaking secrets to players."
lede: "This record corrects the architecture overview. Without the salt, a GM's unredacted page and a player's redacted page advertise the same validator, and any cache holding both serves whichever it stored first."
weight: 160
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

The architecture overview (§5.5) states two rules that cannot both hold:

> Rendered output is **permission-neutral by construction**, with one deliberate exception:
> `[!secret]` callouts (§5.6), whose body differs by viewer.
>
> …
> `ETag` = the same hash, so browser and server caches share one invalidation signal

The first is right. The second was written before the second body variant existed, and the same
section defines two variants from one `content_hash`. They are stated together and they conflict.

## The defect

The render cache has two variants per page, keyed `(campaign_id, path, content_hash,
include_secrets)`. `include_secrets` is true only for a campaign GM; everyone else gets the
redacted variant.

If both variants carry `ETag: "<content_hash>"`, then a GM's response and a player's response
advertise **the same validator**. Now consider any cache holding both — a shared CDN, a
corporate proxy, a browser shared across accounts:

1. The GM requests the page. The cache stores the unredacted body under hash `H`.
2. A player requests the same page. The validator matches, so the cache serves its hit.
3. The player receives the GM's page, with every secret in it.

The redaction is correct in the handler and defeated in transit. This is the single highest-value
security test in the project, per §12 and §14 of the architecture overview, and it is defeated by
a header.

## Decision

**Salt the `ETag` with the variant flag:**

```
ETag = W/"<sha256(content_hash + ':' + include_secrets)>"
```

Two variants of one page now carry two validators, and a cache cannot confuse them.

**The salt is not a secret.** It does not need to be unpredictable — it needs to be *different*,
so that a collision between the two variants is impossible. Using the flag verbatim is correct
and keeps the construction obvious to anyone reading the code.

### Cache directives follow the same reasoning

| Response | `Cache-Control` | Why |
|---|---|---|
| Wiki page, `include_secrets=false` | normal, `ETag`-revalidated | The body is identical for every reader in this tier, so a shared cache is safe. |
| Wiki page, `include_secrets=true` | **`private, no-store`** | Must never enter a shared cache under any configuration. |

A redacted page is permission-neutral *within its tier*, which is why only the GM variant needs
`no-store`. The cost is that a GM navigating between wiki pages re-fetches — negligible next to
the alternative.

Note this does **not** change the cache key's shape, and it does **not** create a per-user
cache. There are still exactly two variants per page. See
[0017 — cross-campaign links never inline]({{ "decisions/0017-cross-campaign-links-never-inline/" | relURL }})
for the other rule that keeps it that way.

## Consequences

- A test asserts a GM response and a player response never share an `ETag`. That is the test
  this record exists to make possible.
- `no-store` on the GM variant is asserted too, rather than assumed.
- The document still does **not** vary by the theme cookie, so **no `Vary: Cookie` is emitted** —
  the `ETag` is about the content hash, and a theme preference is not a function of it.

## Why the design record is not edited

The architecture overview is a dated design record, published verbatim on the docs site, and its
`design/_index.md` page exists specifically to list known staleness. Editing the record would
destroy its value as a record. This ADR carries the correction, and the staleness note names it.

## Alternatives considered

**A per-user cache.** Would sidestep the collision entirely by never sharing anything. Rejected
for the same reason `no-store` is only applied to the GM variant: it destroys the shared-cache
story for the common case, which is the reader who is not a GM.

**Keeping one variant and post-processing.** Render one redacted page and reveal secrets
client-side. This is the obvious alternative and it is exactly what §5.6.1 forbids: the secret
body would be in the response, where any player recovers it from view-source. Redaction happens
in the render, before templ output, so no intermediate buffer holds an unredacted copy for a
non-GM.

**Strong `ETag`s.** `ETag = "<hash>"` without the weak prefix. Irrelevant to this defect — the
collision is in the value, not in the strength.