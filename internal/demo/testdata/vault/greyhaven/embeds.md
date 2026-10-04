---
title: Embeds
---

# Embeds

An embed starts with `!` and inlines its target instead of linking to it. A page
and an asset are both legal targets, and the difference is what the target is.

![[secrets]]

![[maps/greyhaven.svg]]

The map above is an SVG, which the assets route serves under its real media type
with `Content-Disposition: attachment` — a navigation downloads it, an `<img>`
still draws it. [[index]] goes back to the campaign index.
