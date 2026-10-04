---
title: The Forgotten Realm
kind: index
---

## A campaign whose game will not start

You are reading this and it works. Every page of this campaign renders, every link on
them resolves, every file beside them is served. Nothing here is broken in the way a
broken page is broken.

The **game** — the Table, the live tabletop with its map and its tokens — will refuse
to open. Not because of this campaign's pages, and not because of anything anybody did
wrong, but because this campaign asks for a gameplay system that **this binary does not
have**:

```
system: pathfinder-2e
```

That is the entire fault, and it is a fault on purpose. This campaign is the only
demonstration in the demo vault of what happens when a campaign names a system its
instance cannot resolve, and it is here because that failure is otherwise very hard to
explain and impossible to see. It is also the reason the demo's own completeness gate
is **wired to turn red** if a Pathfinder plugin is ever registered in this build: a
demonstration that repairs itself is not a demonstration.

## The three things that are true, separately

The requirement this campaign exists to satisfy has three parts, and a page that
demonstrates one of them is not demonstrating it. All three hold here at once:

| | Claim | Where |
| --- | --- | --- |
| 1 | **The wiki answers 200, with the page's content.** | this page, and every other one |
| 2 | **The dispatch is refused.** | [[what-the-game-master-sees]] |
| 3 | **The refusal names the id it wanted.** | [[what-the-operator-sees]] and the page before it |

Any one of the three can hold while the others fail, and two of them failing look like
one working. A product that served a 500 for the wiki would satisfy "the game does not
work" while breaking the claim that actually matters, which is that **the reading
surface and the playing surface are allowed to fail independently.** A campaign whose
rules are gone is still a campaign whose notes somebody needs at the table.

## What is here

- [[the-system-it-asked-for]] — what `system:` actually is, where the list of systems
  comes from, and why an unrecognised id is a *real state* rather than a fault in the
  row.
- [[what-the-game-master-sees]] — the refusal at the Table, and the honest part: the
  browser is not told which system is missing.
- [[what-the-operator-sees]] — the log line that names it, printed before the server
  begins listening.
- [[notes/links-from-a-campaign-without-a-system]] — the links out of here, two of them
  broken on purpose, and why the broken-link report is a fact about *content* and has
  nothing to do with whether the game runs.

## And the map

![[maps/the-unvaulted-march.svg]]

A hand-drawn map, served like any other file in this campaign, from the same route and
under the same gate. There is no table to put it on. It is here because the file route
and the game are **different surfaces failing independently** — which is the same
claim as claim 1 above, made about a PNG instead of a paragraph.

## This campaign is private

Visibility is `private`, and its one member is the Game Master. An anonymous reader
asking for any page here gets a **404** — the same answer, byte for byte, that a
campaign which does not exist would give, because no access is a 404 and never a 403.
`public-post` is the campaign that answers strangers; this one does not, and the
contrast is on [[the-system-it-asked-for]] for why the two boundaries are worth
demonstrating separately.