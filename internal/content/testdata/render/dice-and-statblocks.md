---
title: Dice and stat blocks
kind: handout
---

# Dice

The GM rolls, the server rolls, and this page never does. Roll
{{dice:1d20+5}} for the attack.

The quoted spelling the documentation uses: {{dice "2d6"}}

No argument at all: {{dice}}

A different pool: {{dice:4d6 drop lowest}}

A 2d6-pool system describes a different grammar from a d20 one, so the
expression is carried verbatim and the protocol never assumes d20. What lives in
the DOM is the expression, in an attribute, and the element's text is empty.

# Stat blocks

The stat block for a named game object: {{statblock:Goblin}}

One with a path: {{statblock:creatures/Goblin}}

A stat block's shape is the gameplay plugin's business — which fields, in what
order, whether an ability score reads as a modifier or a number — so the slot
carries the operand and nothing else.

# What is not a directive

An unknown name stays text, because a page must not be destroyed by a plugin
that is not installed: {{blast:3d6}}

Neither does a stray brace pair: {{}} or {{ dice }} or {{dice:}}

And a `{{…}}` written in backticks is text too: `{{dice:1d20+5}}`, as is
`[[The Lighthouse]]` in a code span.
