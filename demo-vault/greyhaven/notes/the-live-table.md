---
title: The live table
---

## Where the table is, and what it is not

The Table is at **`/c/{slug}/play`**. There is one per campaign, it is the same URL for
every reader entitled to it, and it is behind the play gate — a membership tier above
the one that reads the wiki, because a tabletop's socket is a standing capability: a
table reachable by anyone who can reach the port is a campaign whose live state anyone
who can reach the port can read and write.

## The URL names the campaign and nothing else

There is no identifier in the address for a particular sitting, and no route that
takes one. Semiplane has no noun for it, and the interface never uses one. Three
consequences fall out of that single decision:

- There is **no list of them**, because a campaign has at most one, so a list would
  have one row.
- There is **no join code and no share link that means anything later**, because
  there is no second address to arrive at.
- Reconnecting means reloading `/c/{slug}/play`. The server sends the current state
  document on connect, so a reader who closed the tab mid-combat is not asked to
  reconstruct anything.

The awkward word the design record uses for "a particular sitting" is one the product
deliberately does not surface, and this page follows the interface's lead rather than
the record's: if you go looking for it in the routes, in an `aria-label` or in an empty
state, you will not find it.

## One process owns the state

This is the single most important operational fact about the Table, and it is not a
configuration choice:

> The authoritative state lives in **memory** and the hub that fans it out is
> **process-local**. Two instances behind a load balancer will silently diverge — not
> fail loudly, diverge, with each client showing a coherent and wrong game.

SQLite's single-writer limit is not the constraint; in-memory state ownership is.
Nothing scales semiplane until there is a shared broker, and that is a different
architecture rather than a flag. So do not put a second instance behind the balancer,
and do not look for the setting that lets you.

Persistence is the other half: the hub applies changes in memory immediately and
**debounces** the write to the database. That is why a roll is instant, and also why a
process killed mid-debounce can lose the last fraction of a second — which is why a
campaign carries a ruleset fingerprint and refuses to resume rather than loading state
that was written under different rules.

## What is on the page

- the **map** — the scene's artwork with the placements drawn over it;
- the **token list** — a real list of buttons, one per placement, naming it and giving
  its hit points, conditions and layer;
- the **action bar** — roll, the token sheet, end turn, and the chat panel;
- the **chat log** and the **dice log** — what was said and what was rolled.

Of the four action-bar controls, two are honest about being incomplete on this build:
turns are not tracked yet, and the chat log is connected for reading while sending
needs a chat operation the Table does not have. Both are `aria-disabled` with a stated
reason rather than natively `disabled`, because a disabled button is not focusable and
its `aria-describedby` is then read by nobody.

## The canvas is a mirror, not the interface

`app.canvas` is `aria-hidden="true"`, and the renderer refuses to mount a map surface
that is not hidden before it does anything else. Every value painted on the canvas is
also in the token list, which is the accessible representation and the source of
truth; the canvas exists so a sighted reader does not have to move a pointer to read
the table.

- On a **television**, where dragging is unavailable, the token list is the primary way
  to change what the map shows.
- On a **phone**, the whole map is a readout. It draws, it never takes a gesture, and
  the list is the only representation — so on a small screen the list is the
  load-bearing component and the canvas is the mirror.
- The input handlers are bound and unbound from the coarse-pointer media query's own
  change event, so a surface that crosses into coarse has **none attached at all**,
  rather than attached and inert.

## The camera, in map units

Zoom is expressed in **map units per viewport unit** — the inverse of a zoom factor —
and never in pixels. The client fits the map's extent to the viewport on load, and a
resize **preserves the map centre**: the same point keeps the same fractional position
in the window, so crossing a breakpoint does not re-frame the scene. That is not a
detail; at the screen sizes semiplane targets, breakpoint crossings happen constantly,
and a camera that remembered "at 1200 units wide this looked right" would re-derive
itself on every one.

Two behaviours that surprise people, both deliberate:

- **The camera is not clamped to the map's edges.** Clamping would move the centre on
  a resize, which is exactly the re-framing above. The map is letterboxed instead.
- **The far zoom bound is the fit scale for the current viewport**, recomputed as the
  viewport changes, so a map cannot be zoomed out past fitting. The near bound is 1:1.
  Every outline on the canvas is stroked at 3 **screen** pixels regardless of zoom,
  because a constant width in map units is 1px at one zoom and 30px at another — the
  difference between a contrast overlay and a decoration.

[[north-road]] is the scene this table shows, and it says what its extent is for.

## What the map does not know

The renderer will draw **fog** and an **initiative strip** if the state document
carries them, and the shapes are documented in the client rather than invented there.
Neither is on the wire yet: the live document is a revision, a paused flag and a list
of placements. So the map is placements over artwork, which is the correct
degradation — and a client that computed fog or a turn order of its own would be a
second answer to a question the server owns.

What *is* on the wire, per placement: a position, hit points and a maximum, a
condition set, a visibility flag, and a **version**. The version is the ordering
authority, and it is per placement: two clients moving two different tokens do not
contend. The `gh-sealed-cipher` placement in this campaign is invisible, and a player
is not sent it at all — there is no "hidden" marker to find in the payload.

Back to [[index]].