---
title: Game objects
---

## Five kinds, and who owns them

A page's `kind:` says what the page **is** rather than how it looks. It is one word,
and it is resolved through a registry rather than matched against a list in a
template — which is the difference between a plugin being able to add a kind and a
plugin having to ask semiplane to add one.

## The five semiplane owns

These five are semiplane's in **every build**, and a gameplay system may not claim
one of them. They exist because a client has to be able to draw a placement, list a
token and lay out a page without knowing a single rule of the game:

| Kind | What the page is | This campaign's |
| --- | --- | --- |
| `index` | the campaign's front page | [[index]] |
| `scene` | a map with placements and fog | [[north-road]] |
| `token` | one instance of a game object, on a table | [[iron-vigil]] |
| `journal` | a log kept in the vault rather than in chat | [[watch-log]] |
| `handout` | material handed to the players | [[millers-letter]] |

If a system declared `token`, that would be a system author who thinks `token` is
theirs, and it is refused at startup with a message naming the pack. The reason is
one-directional: a client cannot render a placement it has no vocabulary for.

## What a token actually is

A token page is a **description**. The thing on the table is a *placement*, and a
placement is a position, a hit-point pair, a condition set and a visibility flag in
the campaign's live state — four numbers and a list. There is no join between a token
page and a placement: the placement's identifier is whatever the Game Master gave it,
and the page is whatever the Game Master's vault holds. Semiplane does not pretend
those are the same thing, because making them the same would mean the live state had
to be invalidated whenever a page was renamed, and a page rename is the most ordinary
event in a wiki.

This campaign's seeded table uses these placement identifiers, and they match the
ids in the demo's own manifest rather than any page path:

- `gh-iron-vigil` — a Warden at the north gate. [[iron-vigil]]
- `gh-mirror-hound` — a mirror hound on the road. [[mirror-hound]]
- `gh-sealed-cipher` — at the standing stone, and **not visible to players**. Nothing
  on the table says "hidden"; a player is not sent the placement at all.

## What a scene is, and what it is not

A scene page embeds a map image and names the things on it in prose. It is **not** a
map editor: semiplane draws placements, conditions and a grid over the artwork, and
fog where the state carries it. It does not draw walls, doors, light sources or line
of sight, because those are rules questions and semiplane has none of the answers.
If your table needs them, they belong to a plugin — which is a page kind and a route,
and the way to add one.

[[north-road]] has the map, and explains the number at the top of it.

## The kinds this build registers, and where they come from

Nothing in this list is written into semiplane. It is read at startup from
`plugin.Registry`, which is handed the gameplay system's declared kinds in
`cmd/server/systems.go` — the composition root, which is the only place a registration
happens in the process.

The consequence worth knowing is the one in the next page: a build that ships a
different system registers different kinds, and every page of a kind that build does
not have still renders.

Back to [[index]].