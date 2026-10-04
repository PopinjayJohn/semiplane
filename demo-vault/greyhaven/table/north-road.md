---
title: The North Road
kind: scene
---

## The moor outside the north gate

The moor outside Greyhaven's north gate, where the demo's seeded tabletop is already
standing. This is a **scene** — one of the five page kinds semiplane owns — and a
scene is a map plus the prose that says what is on it.

![[../maps/greyhaven-north-road.svg]]

## The number at the top of that file

The SVG is `width="4800" height="3200"`. That is not a print size and not a
resolution; it is the map **declaring how large it is**, in map units, and it is the
only place the extent has to be written for this build.

The client reads it in two steps, and the order matters:

```
data-map-width  ──override──┐
                            ├──▶  the map's bounds
the image's own size ─fallback┘
```

The surface the server renders for a table carries `data-map-width`,
`data-map-height` and `data-map-grid` as optional attributes. Where they are absent,
the scene module falls back to the loaded image's intrinsic size — which for an SVG is
exactly the `width` and `height` on its root element. So declaring the extent on the
image is not a fallback nobody reads; on this build it is the path that is read.

**A map that fits cannot demonstrate a camera.** The camera works in map units per
viewport unit, and its far bound is the fit scale for the *current* viewport. A map
that already fits has nothing to pan to and nothing to zoom out of. At 4,800 by 3,200
against a 3,840-unit-wide viewport, the fit scale is **0.8 map units per viewport unit**:
the map is wider than the window, so panning has somewhere to go. Zooming out is
stopped at that fit scale; zooming in is stopped at 1:1.

And a large extent with few bytes are independent properties of a vector image, which
is why this file is hand-authored rather than a photograph of a table. It costs about
ten kilobytes, it does not blur when the camera comes in to 1:1, and the client tints
the placements over the top of it — so the only thing the artwork has to get right is
that the terrain under a placement is legible.

## What is on it

The map is drawn from the demo manifest's own seeded placements, so the three tokens a
reader sees are standing on terrain this file actually has:

| Placement id | At | What |
| --- | --- | --- |
| `gh-iron-vigil` | 900, 620 | a Warden at the north gate — [[iron-vigil]] |
| `gh-mirror-hound` | 1240, 700 | a mirror hound in the heather — [[mirror-hound]] |
| `gh-sealed-cipher` | 1560, 480 | the standing stone, **invisible to players** |

The seed also gives the Warden 31 of 44 hit points and a concentration condition, and
the hound 12 of 12 and prone. A placement's identifier is a Game Master's own string
chosen at seeding time; nothing joins it to a page, which is why the table above names
pages in the last column by hand.

## What a scene is not

It is not a map editor. Semiplane draws the artwork, the placements, their conditions,
a grid, and fog where the state carries it. It does not draw walls, doors, light
sources or line of sight, because each of those is a rules question and semiplane
deliberately has none of the answers — see [[game-objects]].

## The camera, if you want to drive it

Drag to pan, wheel or pinch to zoom about the pointer, and click a placement to select
it — which also moves the camera to it from the token list. On a television, where
dragging is unavailable, the token list is the way to do all three. On a phone the map
is a readout and takes no gestures at all. The reasoning is on
[[the-live-table]].

Back to [[index]].