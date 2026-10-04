---
title: Greyhaven
kind: index
---

# Greyhaven

This page is the campaign's index, and it is the first thing a reader opens. It
introduces one page at a time, and every page in this campaign is reachable from
here by a `[[wikilink]]` — which is what `make demo-check` enforces, because a
page nothing links to is a page nobody finds.

## Prose and links

Ordinary markdown renders as prose. See [[prose]] for what a page body looks
like, and [[notes/broken-links]] for what a link that goes nowhere looks like.

## Secrets

A `[!secret]` callout is either collapsed or revealed. [[secrets]] carries one of
each, because a demo that shows only the collapsed state is a demo of the mistake
rather than of the feature.

## Embeds

An `![[embed]]` pulls another page or an asset into this one. [[embeds]] embeds
a page and both of the campaign's maps.

## Game objects

Semiplane owns five kinds and the plugins own the rest.

- [[journal]] — a log written in the vault.
- [[scene]] — a map with placements and fog.
- [[/public-post/town-notice]] — a page in another campaign, resolved because the
  demo reader may see both.

## Rules content

The rules plugin contributes these kinds, and each is a page:

- [[bestiary/goblin]] — `creature`
- [[rules/light-spell]] — `spell`
- [[rules/warden]] — `class`
- [[rules/lucky-feat]] — `feat`
- [[rules/grey-haven]] — `ancestry`
- [[rules/soldier]] — `background`
- [[rules/poisoned]] — `condition`
- [[rules/healing-potion]] — `item`

## Game objects on the table

[[objects]] is a `token`, and it is where a statblock and a dice expression live.
