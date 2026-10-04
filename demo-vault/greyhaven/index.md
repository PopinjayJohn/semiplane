---
title: Greyhaven
kind: index
---

## The showcase campaign

This page is the front door of the showcase campaign, and it is also the campaign's
tutorial. Every feature semiplane has is introduced **once, below, in the order a
newcomer meets it**, and each one is a page of its own with prose that says what the
feature does *and what it does not do*. Read them in order and you should end up
able to run a campaign without having read a manual.

Greyhaven is a walled town on a moor. Its players are the Wardens, four of its
twenty-four pages carry a secret, and its tabletop is seeded with three placements
already standing on the north road outside the north gate.

## What you are looking at

The campaign is **private**, with one Game Master and one player. Sign in as
`demo-gm` and you see everything, including all five collapsed callouts. Sign in as
`demo-player` and the same URLs answer with the same pages minus anything hidden — and,
on the four pages that carry one, with fewer bytes rather than different ones.

Everything below is reachable from this page by `[[wikilink]]`. That is not decoration:
`make demo-check` fails if any page in the campaign cannot be reached from here by a
chain of links, because a page nothing links to is a page nobody finds.

## Reading a page

Every page here is a Markdown file in a vault — an ordinary Obsidian folder, watched
for changes and rendered by semiplane. Nothing is stored in a rich-text editor and
nothing is stored in a database row you cannot read.

- [[notes/prose-and-links]] — headings, tables, `[[wikilinks]]`, aliases, anchors,
  and what a page with no `kind` renders as.

## Secrets

A Game Master needs somewhere to put what the players do not know yet. That is one
block-quote and one character.

- [[notes/secrets]] — the two states of a secret callout, what a reveal records,
  and why a secret is *removed* rather than hidden.
- [[table/watch-log]] — a journal page shaped like an exported chat log, with a
  collapsed callout in the middle of it.

## Embeds

- [[notes/embeds]] — `![[…]]` inlines a page or an image instead of linking to it,
  and an image is served under its real media type so a browser can draw it.

## Game objects

Semiplane itself owns five page kinds, because a client has to be able to draw a
placement without knowing any rules at all. A gameplay plugin owns the rest, and
neither list is written by hand — both come from the registries at startup.

- [[notes/game-objects]] — the five kinds semiplane owns, and what each one is for.
- [[table/north-road]] — a **scene**: the map, its extent, and the placements on it.
- [[table/iron-vigil]] — a **token**: one instance of a game object.
- [[handouts/millers-letter]] — a **handout**: material handed to the players.
- [[table/watch-log]] — a **journal**: a log kept in the vault rather than in chat.
- This page — an **index**: the front page, and the root every link walk starts from.

## Rules content

The 5e plugin's data pack declares eight more kinds. They are prose in a build that
does not ship that plugin, which is the honest answer rather than a failure.

- [[notes/rules-content]] — how a page's kind is resolved, and what happens to a kind
  this build has never heard of.
- [[rules/lantern-folk]] — `ancestry`.
- [[rules/warden]] — `class`.
- [[rules/steady-breath]] — `feat`.
- [[rules/soldier]] — `background`.
- [[rules/lantern-light]] — `spell`.
- [[rules/poisoned]] — `condition`.
- [[rules/mending-draught]] — `item`.
- [[rules/mirror-hound]] — `creature`.

## Statblocks and dice

- [[notes/rolling]] — `{{statblock:…}}` and `{{dice:…}}`, and why a dice expression
  is a **request to the server** rather than a number the page computes.

## The live table

- [[notes/the-live-table]] — the Table route, the socket behind it, and why the URL
  names the campaign and nothing else.

## House rules

- [[notes/house-rules]] — a rule your table plays by that the rules do not: keyed,
  first-match-wins data, and deliberately **not** part of the campaign's ruleset
  fingerprint. This campaign has none enabled, which is itself worth knowing.

## This campaign's brand

- [[notes/theme]] — the four things a campaign may change about how semiplane looks,
  the two contrast floors they must clear, and what happens when one misses. The file
  is `theme.yaml` at the campaign's content root — beside this page — and it is two
  declarations long.

## Another campaign

- [[notes/cross-campaign]] — the leading-slash link, and why the answer a reader gets
  depends on who they are while the markup stays identical for everyone.

## Where the boundaries are

- [[notes/boundaries]] — three references on that page fail three different ways on
  purpose, plus what semiplane refuses to be, and how this vault's own completeness
  gate is written.

## The map

One hand-authored SVG, embedded here and again on the scene page:

![[maps/greyhaven-north-road.svg]]

It is 4,800 by 3,200 map units, which is the point of it rather than a detail —
[[table/north-road]] explains what the number is for and where the client reads it.