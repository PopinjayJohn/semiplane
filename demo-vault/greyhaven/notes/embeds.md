---
title: Embeds
---

## Inlining something else

An embed is a `[[wikilink]]` with one `!` in front. Same grammar, one different
character, and the difference is not cosmetic: a link is a pointer you can click
through, and an embed **puts the target's content in this page**.

Here is a page, inlined:

![[millers-letter]]

And here is the campaign's map asset:

![[../maps/greyhaven-north-road.svg]]

## The two targets, and why they are different questions

A **page** target and an **asset** target are written identically and resolved by
different machinery, because they are different questions:

- A page is in the campaign's page index, so the question is "which page is called
  this?" and the index answers it.
- An asset is a file on disk. The page index holds pages and nothing else, so an
  asset reference is never *resolved* — it is **fetched** from the campaign's asset
  route, and whether it is there is answered by the filesystem.

That split is why a broken *asset* is a different kind of defect from a broken
*link*, and why the demo's own completeness gate counts the two separately: putting an
asset in the unresolved-link budget would make the budget a count of embeds.

## How an image is served

The asset route serves a file under its **real media type**, and for SVG that means
`image/svg+xml` with `Content-Disposition: attachment` and a sandboxed content
security policy. The combination is deliberate and the two halves are not
interchangeable:

- The media type is what lets an `<img>` draw it. An SVG served as
  `application/octet-stream` renders as a download and a broken image.
- The attachment disposition is what makes *navigating* to the URL download it
  rather than execute it in a context that could reach the reader's cookies.
- The sandbox is what stops an SVG that somehow contains script from running it.

So the map above draws, and clicking through to it hands you a file.

## What an embed is not allowed to do

**A cross-campaign embed is refused.** `![[/public-post/town-notice]]` is not an
error in the vault and it is not a broken link; it is a request semiplane declines.
The reason is not fussiness. Inlining another campaign's page would make this page's
HTML depend on a document the reader may not be able to read, and the render cache
holds exactly two variants of a page — keyed on whether secrets are included. The
variant count would stop being two and start being "one per set of campaigns the
reader can see", which is unbounded and per-reader. A **link** across that boundary is
fine, and renders identically for everyone: see [[cross-campaign]].

A reference that resolves to nothing leaves an **empty slot**, not a placeholder
saying so. An empty slot is the same for every reader, which is the property the
whole permission-neutral design rests on; a missing image is reported to the Game
Master in the broken-link report instead, which is viewer-appropriate and therefore
cannot be in the page's bytes.

## Embeds and secrets

A same-campaign page embed **does** render the target, so embedding a page carrying a
secret callout inlines that callout for a reader who is allowed to see it and omits it
for one who is not. The embedding page's own bytes are still the two cached variants
and nothing more, because the decision is made about *this* page and is keyed on the
same include-secrets flag.

Which is the reason the handout above is a good choice for this page and
[[secrets]] would have been a strange one: the demo has two variants to show and it
should spend them where a reader will actually look.

Back to [[index]].