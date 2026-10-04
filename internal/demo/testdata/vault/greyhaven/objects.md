---
title: The Grey Warden
kind: token
---

# The Grey Warden

A token is one instance of a game object placed on the table. The rules plugin
owns the notation and the numbers; the token kind is semiplane's, so a system
written for a different game needs no client work to place one.

## Statblock

{{statblock:goblin}}

## Dice

{{dice:1d20+5}}

Dice are **not** rolled at render time. The expression is carried through the
render verbatim and resolved server-side, so every roll is in the audit trail and
a client cannot compute one the server never saw. The arithmetic above is
`[[notes/broken-links|the same caveat]]` — see that page for what a link that
cannot resolve looks like.
