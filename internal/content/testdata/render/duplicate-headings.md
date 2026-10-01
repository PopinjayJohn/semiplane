---
title: Two headings with the same text
kind: handout
---

# The Coast

The first one.

## The Coast

The second one gets a distinct id, so `[[This Page#The Coast]]` and an
in-page link each have somewhere to land that is not ambiguous.

## The Coast

The third one, and its id is distinct again.

### The coast

Capitalisation folds, so this collides with the first three and is numbered too.

#### The Coast

A heading with markup in it: the **Drowned** `Lighthouse`

##### The Coast {#custom}

An authored id is not honoured: `parser.WithAttribute` is not enabled, so the
text after the hash is part of the heading and the id is generated from all of
it. See the note in `render.go` for why an author may not choose an id.
