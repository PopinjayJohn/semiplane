---
title: "0053 — `/play` serves the table and `/ws` upgrades, and a control the protocol cannot honour is rendered disabled with its reason"
description: "The WebSocket upgrade moves to `/c/{slug}/ws` so `/c/{slug}/play` can serve the document architecture §9 always described, and the action bar's End turn and Chat controls render disabled with the reason stated rather than inventing a client-side turn."
lede: "Phase 7 mounted the socket at `/play` because there was no document to serve there yet, so a player who opened the table in a browser received a 400 from a WebSocket handshake. Restoring §9 costs one URL, and the two controls the protocol cannot honour cost a sentence each."
weight: 10
date: "2026-10-03"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

Architecture §9 lists four campaign routes and gives each one a job:

```
/c/{slug}/play                 VTT (members only)   — no session id
/c/{slug}/ws                   WebSocket upgrade
/c/{slug}/events               SSE (editors + live chrome)
```

`spec.md` carries the same table. Phase 7 built the realtime plane and mounted the
WebSocket at `GET /c/{slug}/play` — **because at that point there was no document
to serve there.** The deviation was reasonable when it was made and became
untenable the moment phase 9 built the document the URL was always reserved for: a
player who opened `/c/{slug}/play` in a browser got `400` from a handshake, and a
player who got a document would get nothing at `/ws`, because no such route
existed.

So `/play` was a route that could only ever answer for a browser that could not
use it.

Two of the four controls UI §4.9 puts in the compact action bar are for intents
the protocol does not have. The 5e pack's op set is `roll`, `attack`, `heal`,
`apply_condition`, `clear_condition`, `apply_status`, `remove_token` and
`set_hit_points`. There is no `turn_end` and no `chat`. §4.9 anticipates this
exactly:

> `roll` and turn-end must exist in the plan's §7.1 intent set. If turn-end is not
> an op, the UI cannot ship without a plan amendment — **flag it, do not invent a
> client-side turn flag.**

## Decision

**`play.Mount` registers both patterns, and each keeps its own gate.**

```go
gate := campaigns.RequirePlay
mux.Handle("GET /c/{slug}/ws", gate(handler))
mux.Handle("GET /c/{slug}/play", gate(http.HandlerFunc(handler.serveDocument)))
```

One `gate` value for both, rather than two calls to `campaigns.RequirePlay`, and
the reason is testable rather than stylistic: `mutate.sh`'s gate mutation
replaces the **assignment**, so dropping it takes `/ws` and `/play` together. A
mutation applied to one call site would be a partial mutation the S-8 matrix could
not distinguish from a pass, because the matrix asserts both paths.

The gate is not optional on either route, and on `/ws` it is the load-bearing
one: a socket is a standing capability, so a tabletop reachable without
`RequirePlay` is a campaign whose live state anybody who can reach the port can
read and write. This is the route whose missing gate is not a defect anybody would
notice until it is exploited.

**A control the protocol cannot honour renders present, disabled, and states the
reason.** `End turn` and `Chat` are in the action bar and both are disabled. The
reason is a `visually-hidden` paragraph the button's `aria-describedby` points at,
and it says something the reader can act on:

- `End turn` — "Turns are not tracked on this table yet."
- `Chat` — "The chat log is connected; sending needs a chat operation the table
  does not have yet."

Neither is a client-side flag. There is no `isYourTurn` in the view model that a
server could not check, because `realtime.Document` carries a revision, a paused
flag and placements, and no turn. A client-side turn flag would be a control whose
state the server cannot contradict and a reader cannot trust — which is the
failure §4.9's dependency row exists to prevent, and which S-7.1's "clients send
intent, never state" rules out at the protocol.

**The chat reason names the log, not the feature.** An earlier draft said "Chat is
not connected to this table yet", which was true while the panel was unwired and
became false the moment `chat.Panel` was mounted into the rail. A stated reason a
reader can disprove by looking at the panel beside it is worse than no reason,
because it teaches them the other reasons are unreliable too. What remains true is
narrower: the log is connected and lines arrive over the event stream; the compose
form is inert, because `chat.PanelView.Compose`'s zero value renders the
signed-out paragraph and there is no `chat` op to send one.

## Consequences

**A socket at the old URL stops working.** Nothing in this repository connected to
`/play` for a socket after this change — the two dials in `cmd/server`'s wiring
tests moved with it — and phase 9 has not shipped, so no deployed instance has a
client to break. A self-hosted instance that has already run phase 7 from a branch
would need its client pointed at `/ws`, which is one line.

**The two disabled controls are the honest state of the protocol, not a gap in the
UI.** They are visible, explained, and focusable, so a GM looking at a phone
learns what the table cannot do yet. That is the record's own preference, stated
as "an absent control is worse than a disabled one with a reason".

**Turn order has to be built before End turn means anything.** It is hub state, so
it belongs beside `campaign_state` — which makes it a phase 12 question about what
survives a restart, and a decision about whether a turn is authoritative in memory
or derived from a campaign row. Recording it here rather than solving it here is
the point of flagging.

**The document is a second `os.Root` consumer and a third composition root entry.**
`documentView` composes `live.Chrome`, `webplay.Rail` and `chat.Panel`, and each
renders its own `data-chrome` hook rather than this route naming one — because
`live.Decide` resolves a fragment's target against `live.TargetByName`, so a
hand-written hook is refused with `unknown_target` on every frame, silently.
`TestTheDocumentCarriesEveryHookTheLiveChromePatches` is what makes the seam
exist; nothing else in the tree says the two work items meet.

## Alternatives considered

**Leave the socket at `/play` and serve the document somewhere else** — at
`/c/{slug}/table`, say. Rejected: §9 and `spec.md` both name `/play` for the
document, and moving the document instead of the socket trades a URL nobody has
shipped for one §9 has always reserved. It also splits the two halves of one page
across two addresses, which is the mistake the scheme's slug-in-the-path design
exists to prevent.

**Serve the document at `/play` and the socket at the same path, content-negotiated.**
Rejected: `Upgrade` is a header, and a route that answers HTML to a `GET` and a
socket to a `GET` with `Upgrade` is two answers decided by a request header on one
URL. The scheme's own reasoning — the slug in the path partitions every cache key
by visibility — works precisely because each route is one thing.

**Give the client a `turn_end` intent and let the hub refuse it.** Rejected, and it
is the alternative §4.9 rules out by name. It puts a turn flag in the client, which
S-7.1 forbids ("clients send intent, never state"), and it makes a control whose
failure the reader cannot see: the button would press, the socket would carry
something, and nothing would happen for a reason no document states.

**Render no End turn control at all.** Rejected: §4.9 says an absent control is
worse than a disabled one with a reason, and a player will look for it. A phone
whose action bar has three controls where the record names four reads as a bug.

**Put the reason in `title` alone.** Rejected: `title` is not reachable by
keyboard and is inconsistently announced. `aria-describedby` pointing at a
`visually-hidden` paragraph is reachable, is read after the name, and is
assertable over the parsed DOM.