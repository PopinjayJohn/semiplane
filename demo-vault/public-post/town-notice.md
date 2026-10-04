---
title: The notice on the board
---

## An open board is a specific kind of promise

Hollowfield keeps its noticeboard outside the north gate, under a roof, in weather.
Everything pinned there is fair copy: the toll schedule, the ferry rota, the standing
offer on bridge work, and the ward's notices about the fen road. It is pinned there
rather than handed out because the town decided a stranger ought to be able to read it
without asking anybody.

That decision is the whole of this campaign. Semiplane's `public` visibility is the
same decision expressed as a column, and the pages in this vault are what it looks like
when somebody actually makes it.

## What is on the board

The toll on the Hollowfield bridge is two coppers a cart, and a cart that is carrying
stone pays nothing, which is the arrangement that has kept the bridge mended for a
century and will keep it mended as long as nobody proposes otherwise.

The fen road is passable as far as the old tollhouse and no further. Three feet of
water over the causeway at the low crossing, and the ward's own notice says so in
those words, which is more than most towns manage.

![[maps/hollowfield-and-the-fen-road.svg]]

## The one thing not yet posted

The ferry rota for the coming week was written, and it has not gone up.

> [!secret]- The ferry rota for the coming week
>
> The Lighter crosses at first light and again at dusk, and on the third day of the
> week it does not cross at all — the ferryman has his own reasons and has posted
> none of them. The ward would rather a stranger planned around that than discovered
> it standing on the bank.

You cannot see that paragraph. It is not hidden with CSS, not blurred, and not
present in the bytes the browser received — the callout was **removed from the source
before anything rendered it**, so there is nothing in this page to un-hide. What is in
this page instead is a line saying a thing is here.

## Why a callout is the wrong lock for a published page

This is the one thing on this page a reader should take away, and it is worth being
blunt about it:

**A `[!secret]` callout controls disclosure. It does not make anything private.**

The callout above hides a paragraph from a reader who has not been given the Game
Master's permission to see it. The moment that permission is granted, **every word is
public** — because this campaign is public, this page is readable by anybody, and there
is no second copy of this page held somewhere more private. Revealing the callout does
not move the text somewhere safer. It publishes it, in place, to an audience that was
never a party to the decision.

The Game Master can still do it, and for a game being played at a table it is exactly
the right control: the callout is a way of handing over one paragraph at the moment the
table is ready for it, rather than all at once. That is a genuinely useful thing and
this campaign is not arguing against it.

What it is not is a lock. A reader who wants the text and has the permission gets it,
and so does anyone the permission is then shared with — a reader who forwards the URL,
a browser cache, a proxy between here and them. In a **private** campaign the callout
sits behind the access gate and the two controls reinforce each other. In a **public**
one, the access gate is already open, so the callout is the only control there is, and
it is a curtain rather than a lock.

The honest summary: *in this campaign, anything not yet posted should not be written
down at all until it is posted.* A callout is for text you intend to reveal.

## And the title is a different problem again

A page's `title:` is **not** redacted, and it cannot be, because a title is one line of
front matter with no callout structure in it and therefore no boundary to redact to. A
rule that stripped titles would have to guess which words are secret, and a guess that
fires breaks a real title while a guess that misses publishes the secret.

So a title in this campaign is served to a stranger in three places: the `<h1>` above
the prose, the nav tree, and every search result that matches it. That is why this
page's title is "The notice on the board" and nothing more dramatic. Write the
headline in the body instead — bodies have callouts, and a stranger searching this
campaign for the phrase is exactly the reader who would surface it.

[[index]] · [[searching-without-signing-in]] explains why the search box is the sharpest
edge of all this.