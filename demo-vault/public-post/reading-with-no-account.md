---
title: Reading with no account
---

## What you are, right now

You are an **anonymous requestor**. Not a guest, not a trial, not a read-mostly account:
a request that carries no cookie, which semiplane cannot tell apart from any other
request that arrives without one.

Inside this campaign you resolve to exactly one level, and the level has a name that is
worth quoting because it is deliberately not "public":

> **read-only** — the wiki and public assets, nothing else.

Three tiers exist above that: `player`, which is a member with the play role, and `gm`,
which is a member who may write content. There is deliberately **no tier above `gm` for
instance administrators** — holding the ability to register campaigns and manage users
on an instance is not the same thing as owning a campaign's pages, and a scheme that
made them comparable would eventually hand every private campaign to every
administrator.

The rule that put you where you are is one sentence: **a non-member of a public
campaign reads its wiki, and gets nothing else.** Visibility is a read grant. It is
never a membership, and there is no value of it that could be.

## The whole surface, request by request

This campaign's visibility is `public`, and its one member is the Game Master. So for a
stranger, every route under `/c/public-post/` answers like this:

| Request | Answers | Why |
| --- | --- | --- |
| `GET /c/public-post/wiki/town-notice` | **200**, the page | the wiki is the public surface |
| `GET /c/public-post/assets/maps/hollowfield-and-the-fen-road.svg` | **200**, the file | see [[assets-follow-the-campaign]] |
| `GET /c/public-post/search?q=toll` | **200**, the results | search is part of reading |
| `GET /c/public-post/play` | **401**, `sign in to continue` | the Table is not public |
| `GET /c/public-post/ws` | **401**, same | the socket behind it |
| `GET /c/public-post/edit/town-notice` | **401**, same | writing is the GM's |
| `PUT /c/public-post/secrets/town-notice` | **401**, same | revealing a callout is a write |

Every one of those is a real route with a real gate on it, and the gates are not
permitted to forget: a campaign route is registered in one list, and being on that list
is what puts the access check in front of it. A route added outside the list is behind
nothing, which is why the list is the enforcement and not the handler.

**Notably absent: a 403 anywhere in that table.** You are refused by being asked to
identify yourself, not by being told you are the wrong kind of person. That is
deliberate, and the next section is why.

## Why the Table answers 401 and not 404

This is the single most surprising thing about a public campaign, and it deserves an
explanation rather than a shrug.

Semiplane has one rule for hiding things: **no access is a 404, never a 403.** A private
campaign you are not a member of and a campaign that does not exist have to answer
*identically* — same status, same body — or the status alone becomes a machine for
discovering which campaigns an instance has. A 403 says "this exists and you may not
have it", and that sentence is an existence oracle the moment anybody can compare it
against a 404.

So why is the tabletop a 401? Because the rule is about hiding the **campaign**, and
the tabletop request is not hiding it:

> Reaching a capability gate at all means the campaign is not itself a secret to this
> request. It is public, or the requester is a member of it.

You just read three pages of it. A 404 here would be a lie — it would claim there is
nothing here when the reader is holding the campaign's own contents — and it would be a
*worse* lie than a 403, because a 403 at least admits the campaign exists, whereas a 404
would refuse the very thing that was just demonstrated. So the answer is the one that
tells the truth and helps:

```
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Cookie realm="semiplane"
```

`Cookie` is a registered authentication scheme, which is what a sign-in cookie is, and
the realm names this application. A 401 is a **challenge**, not a refusal. It is the
one 4xx in this product that means *ask again as somebody else*.

And for comparison: the same anonymous request to a **private** campaign — any page of
[[/forgotten-realm/index|the Forgotten Realm]] — is a **404**, for every route on the
list, including the wiki. Not because the answer differs, but because there the campaign
*is* the secret, and pretending otherwise would be the leak.

## The same rule, one tier up

Signing in does not by itself help. Membership does.

This campaign has one member: the Game Master. Sign in as any account on this instance
and you are still `read-only` here, because **an authenticated non-member of a public
campaign gets exactly what an anonymous visitor gets.** The campaign is open to
everybody; belonging to it is a different question, and the demo vault happens to have
no player in this one — the player account belongs to the showcase.

The step that unlocks the tabletop is not signing in. It is *being a member*, with a
role, in this campaign. Which is why the table below has two axes and not one:

| | anonymous | a member (`player` or `gm`) |
| --- | --- | --- |
| wiki, assets, search | 200 | 200 |
| the Table and its socket | 401 — sign in to continue | 200 |
| edit, reveal | 401 — sign in to continue | `gm` only; a `player` is refused **403** |

That last cell is the only 403 in this campaign, and it is not a contradiction. You
have already proved who you are and you are already entitled to be in the campaign, so
there is nothing left to hide and the honest answer is the blunt one: you are in the
right building and you do not have the key.

## The response you should not cache

One more thing is true about every refusal on this page, and it is invisible until it
is not: **every one of these responses is `private, no-store`.**

That is not tidiness. The same URL is a 404 for you and a 200 for the Game Master,
because "no access" is answered without saying why. Nothing in the body tells those
two apart — deliberately, so the status cannot become an existence oracle — and that is
exactly what makes the response unsafe to store. A reverse proxy in front of a
self-hosted instance is the ordinary deployment, and one that had cached your 401 would
hand it to the Game Master at the table.

So the refusal is correct and uncacheable at the same time, and both halves are load
bearing.

[[index]] · next: [[assets-follow-the-campaign]], because the second row of that first
table is the one people get wrong.