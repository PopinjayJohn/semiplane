---
title: Statblocks and dice
---

## Two things that look like arithmetic

Two more pieces of syntax, and they are the two that look like they compute something.
Neither does.

## A statblock

{{statblock:mirror-hound}}

`{{statblock:…}}` names a **game object**. What comes out is not a rendered stat block
— it is an empty slot carrying the name in an attribute, waiting to be filled.

That is the whole design, and it is a boundary rather than an omission. Which fields
a stat block has, in what order, whether an ability is shown as a modifier or as a
number, is a **rules question**. A d20 system and a 2d6-pool system describe different
objects, and a renderer in the middle of the wiki that knew the answer would be a
second implementation of a rules system — and the one that would be wrong, because it
would be the copy that drifts. So the wiki provides the slot and the plugin provides
the block.

The slot is also **permission-neutral by construction**: it cannot show the target's
title, because a title is a lookup, and a lookup is a read of another document.

## A dice expression

{{dice:1d20+5}}

`{{dice:…}}` renders as an empty slot carrying the expression in an attribute, and
**no number anywhere**. That is not a placeholder waiting to be filled in by the
browser. Nothing in the page computes it.

A roll is an **intent sent to the server**, which validates it, applies it, increments
the placement's version and broadcasts the result. Three reasons the arithmetic
cannot happen at render time:

1. **The cache.** A page's rendered bytes are cached under a hash of its content. A
   roll computed during rendering would be a different number on every request, so
   the cache would be caching a random number and the hash would mean nothing.
2. **The audit trail.** Every roll is a server event with an actor and an outcome. A
   client-computed total is in nobody's trail.
3. **The bytes.** Two renders of the same file are byte-identical — which is what
   makes the cache key meaningful and the `ETag` stable, and it is also what makes the
   salted-validator story work. A GM's page and a player's page must never share a
   validator, and the salt is whether secrets are included.

The expression itself is carried **verbatim and never parsed by the wiki**. The
grammar belongs to the gameplay plugin: a d20 system and a pool system spell their
expressions differently, and the protocol never assumes either. So `{{dice:1d20+5}}`,
`{{dice:4d6 drop lowest}}` and `{{dice:3d6kh2}}` are all accepted, and the wiki has no
opinion about any of them.

## The dice roller

The Table route also carries a **graphical dice roller**, at
`/c/{slug}/plugins/dice-roller`. It is a UI plugin rather than a page type of the
rules system, and the distinction is enforced at registration: a UI plugin that
imported the 5e package to learn its vocabulary would have made itself a 5e plugin,
so the operation it emits is written as a literal and checked against what a
registered system actually resolves. A build with no 5e refuses the roller at
startup rather than offering a control that cannot work.

## Neither slot has content for a reason

Both are empty elements with a class, a `data-ext`, an index and an operand. That is
four pieces of state a live layer can key on without depending on a class name a
stylesheet owns — the class is a styling hook and may reasonably be renamed, and
behaviour that depended on it would break the day somebody renamed it. Two spellings of
one fact, written from one field, is the price of not making behaviour depend on a
styling decision.

What fills the slots: [[north-road]] carries the tokens a reader will see on the map,
and [[iron-vigil]] is the page where a stat block and a roll would appear in a real
vault.

Back to [[index]].