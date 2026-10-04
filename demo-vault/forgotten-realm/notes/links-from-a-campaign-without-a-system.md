---
title: Links from a campaign without a system
demo:
  broken:
    - The Sunken Chapterhouse
    - /public-post/The Ledger of Hollowfield
---

## A broken link is a fact about content

This page carries two references that go nowhere, and this campaign's game does not
start. Those two facts are unrelated, and the second reason to write the page is to
say why they are unrelated:

> The broken-link report is computed from the vault's content and the reader's
> accessible campaigns. **It never asks whether a game can start.**

Nothing in link resolution touches a `system:` column. A wikilink is a question about
which page is called what, and the answer is a question about files. So the same
report is produced for this campaign, and for a campaign playing 5e with every feature
switched on, and the two reports are comparable.

That is worth stating plainly because the natural assumption runs the other way — that
a campaign in a degraded state is *more* broken than usual. It is not. It is a campaign
whose playing surface is dead and whose reading surface is entirely healthy, and the
report above is the reading surface's report.

## Links still leave this campaign

A campaign nobody can play still links outward, and the links work:

[[/public-post/town-notice]]

That crosses into the **public** campaign, and it is worth being precise about what
makes it work, because the interesting fact is not that it resolves — it is *who* it
resolves for.

- You cannot reach this page at all. This campaign is private; an anonymous request
  gets a 404.
- A member of this campaign — the Game Master — follows that link and gets the page.
- A reader of `public-post` can follow the same page without an account, because that
  campaign's reading surface is open to strangers.

So one target is reachable by three quite different routes with three different
amounts of trust behind them, and the resolver's answer depends only on which of them
is asking. Nothing about the target changes. What changes is the set of campaigns the
reader may see, and that set is an **input** to the question rather than something
discovered while answering it.

[[the-slash-and-the-line|The same rule, from the public side]] is the other half: a
public campaign's link into a private one is a link an anonymous reader cannot follow,
and it looks identical to them.

## Two links that go nowhere on purpose

Like every campaign in this vault that has a boundary to teach, this one breaks two
references deliberately, and declares them in this page's front matter above.

**A page that was in this campaign and is not now:**

[[The Sunken Chapterhouse]]

The address is a real one inside this campaign, so following it is a **404** for a
reader who is already entitled to be here — an answer about a page, never about the
filesystem, and never a 403. Nothing about the missing page is a fault in the vault;
it is the state of a repository nobody has looked at in a long time, which is more or
less what this campaign is named for.

**A page in another campaign that is not there:**

[[/public-post/The Ledger of Hollowfield]]

This one exists to make a point that is easy to get backwards. `public-post` is a
campaign you *can* read. The link still fails, because the target page does not exist.

**The failure was never about visibility.** Visibility decides which campaigns are
*asked*; existence decides what the answer is. A reader who cannot see a campaign gets
the same unresolved report as a reader who can see it and finds the page missing — and
that indistinguishability is the security property, because it is what stops a
broken-link report from being a directory listing of the instance with extra steps.

## What the declaration is for

`make demo-check` reads `demo.broken` from this page's front matter, and the gate's
unresolved-link budget is derived from the **length** of that list rather than from a
number written into the gate. Two entries here, a different two on the public
campaign's page, and a vault demonstrating a different number again would all pass.

What the declaration buys is that it cannot rot in either direction, and both halves
are findings:

- a reference that starts failing with **no** entry is an accidental breakage somebody
  did not mean — the one that matters, because it is a real defect hiding among the
  demonstrations;
- an entry that **starts resolving** is a demonstration that somebody repaired by
  accident, or a page somebody renamed.

Either rule alone would be satisfiable by a hardcoded number. Together they cannot be.

An entry is the reference **exactly as written between the brackets**, leading slash
included on a cross-campaign one. The gate compares your bytes rather than a normalised
path, so there is exactly one spelling to get right and it is the one you would type.

[[index]]