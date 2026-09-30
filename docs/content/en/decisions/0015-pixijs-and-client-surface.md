---
title: "0015 — PixiJS v8, and a deliberately small client"
description: "WebGL for the map. The client surface stays system-agnostic because token and scene are semiplane-owned."
lede: "The promise 'a new rules system costs no client work' is only true if the client knows nothing about rules. Everything below exists to keep that true."
weight: 150
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

The map needs retained-mode WebGL: hundreds of tokens, fog layers, camera transforms, and a
game loop that redraws while a token is dragged. The 2D-canvas approach means reimplementing
sprite batching, and the hand-rolled approach means writing a renderer.

## Decision

**PixiJS v8.** The same engine Foundry uses, which is the relevant precedent: it handles
tabletop-scale sprite counts without a bespoke renderer.

### The client surface is the part that matters

The load-bearing decision is not the library, it is **kind ownership**.

| Owner | Kinds | Why |
|---|---|---|
| semiplane — wiki | `journal`, `handout`, `index` | Content, not rules |
| semiplane — VTT | `token`, `scene` | Present on every map in every system |
| **gameplay plugin** | `spell`, `class`, `feat`, `creature`, `ancestry` | Genuinely system-specific |

Because `token` and `scene` are **semiplane-owned**, the PixiJS map layer renders placements,
fog, and initiative order without knowing any rules at all. A system sharing nothing with D&D —
Pathfinder, Starfinder — registers its own kinds and needs **no client work**.

This is also why a gameplay plugin does not own `token`: if it did, adding a second system would
mean two claims on the same kind, and the map layer would have to become rules-aware to decide
between them.

### Accessibility: the canvas is the mirror, the token list is the truth

The canvas is `aria-hidden="true"`. The accessible representation is real DOM — a `<ul>` of
`<button>` elements, one per placement, each naming its hit points, conditions, and layer.

That is not a fallback. On a television, where dragging is unavailable, **the token list is the
primary way to change what the map shows.** On a phone, the map is a readout and the list is
the only representation of it. A canvas-only design would be unusable on two of the three form
factors this project targets.

`role="application"` is prohibited.

### Camera rule

Zoom is expressed in **world coordinate units, never pixels.** The client fits the world bounds
to the viewport and preserves the world centre across resize and orientation change. Without
this the map re-frames on every breakpoint crossing — which, at the 14-inch laptop this is
designed against, happens constantly.

A **map contrast overlay** is a first-class control: `--map-dim` on the map layer, a 3px
`--map-outline` on every placement and grid line. Much VTT artwork is low-contrast by design,
and without this the map fails WCAG 1.4.11 in practice.

## Consequences

- The vendored PixiJS build is a committed, integrity-pinned file served from the app's own
  origin. See the delivery note in the delivery plan's decision table; it is the same
  "no runtime third-party origin" rule that keeps the product air-gap friendly.
- The client JavaScript surface is deliberately small. The map layer, the token list, and the
  live chrome. Anything else a plugin wants to render is server-rendered (see below).
- **System-specific views are rendered server-side**, by templ, chosen by the system. A
  character sheet, a spell list, a DC summary share almost nothing between systems, and
  `Derive` returns opaque data that something has to turn into HTML. The choice to do it
  server-side means no plugin JavaScript, and that the client never interprets a
  system-specific mutation.
- The accepted limitation: a plugin cannot ship a richly interactive client-side sheet. Editing
  happens in the markdown editor, which is Obsidian-compatible anyway. If that hurts later,
  a `ClientView` can be added alongside templ without changing `Derive` — additive.

## Alternatives considered

**Plain `<canvas>` with hand-sprited tokens.** No dependency, total control. Rejected on the
performance work: batching hundreds of tokens with fog layers is a real renderer, and
reimplementing one is not this project's problem.

**Three.js.** Broader, and a 3D engine for a 2D map. Rejected as carrying more surface than the
task needs.

**SVG for the map.** Crisp, and accessible for free. Rejected on fog layers and sprite counts —
SVG has no retained mode, so every camera move is a full re-layout. It is the right answer for
the demo vault's map asset, which is exactly why that asset is a hand-authored SVG with a
declared world extent (see the delivery plan, D19).