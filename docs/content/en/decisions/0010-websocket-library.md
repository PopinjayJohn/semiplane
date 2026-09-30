---
title: "0010 — coder/websocket, not gorilla"
description: "gorilla/websocket carries an advisory for a weak PRNG in its mask key and panics on concurrent writes."
lede: "A WebSocket library sits directly on the trust boundary of a multiplayer game. Choosing one with known advisories in its masking path, to save one dependency, is the wrong trade."
weight: 100
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

The realtime plane needs WebSockets, and there are two production Go implementations. They are
close in quality, which makes this a decision about advisories.

**gorilla/websocket** carries advisory **GO-2026-6278**: the masking key is generated from a weak
pseudorandom source. It also panics on concurrent writes to a connection, which makes every
caller responsible for a write mutex that the library's API invites you to forget.

## Decision

**`coder/websocket`, the maintained fork of nhooyr's library.**

Two reasons, in order of weight:

1. **No advisory in the masking path.** WebSocket masking exists to stop a malicious page on a
   shared connection from poisoning a proxy's cache. A weak mask key weakens a security
   property, in the library whose entire job is the connection. The fork fixes it.
2. **`context` is a first-class parameter.** Every operation takes a `context.Context`, which
   composes with the shutdown story in
   [0004]({{ "decisions/0004-single-process-constraint/" | relURL }}) and removes the goroutine
   that nhooyr's API previously required for per-connection deadlines.

The concurrent-write problem is real and worth naming explicitly: the hub serialises writes per
connection. Broadcasting a delta to fifty clients is fifty writes, and if two of them race, the
one that loses must not panic. That serialisation lives in the hub and is covered by the
`go test -race` requirement, not delegated to the library.

## Consequences

- `Origin` is checked **explicitly on upgrade**, not inferred. Same-origin is the rule; anything
  else is refused before the handshake completes.
- Authentication on the WebSocket upgrade is **identical to HTTP**. Same session lookup, same
  membership check. A websocket that authenticates differently is a websocket that
  authenticates worse.
- Every connection gets a read deadline. A peer that stops reading is detected and closed —
  `ws.stale` is a gauge, because a leaked connection is silent otherwise.
- The library is less widely used than gorilla. That is a maintenance-risk trade taken
  deliberately, not an oversight.

## Alternatives considered

**gorilla/websocket.** The incumbent, and for years the only serious choice. Rejected on the
advisory plus the concurrent-write panic. If the advisory is ever resolved with no behavioural
change, this is the record to revisit — but not before.

**`nhooyr.io/websocket` (the original).** Effectively unmaintained; `coder/websocket` is its
continuation. Naming it separately because older documents and blog posts reference it, and
installing it today gets a library that will not receive security fixes.

**Server-sent events plus a POST endpoint for commands.** No WebSocket dependency at all, and
simpler to proxy. Rejected because it costs a second HTTP connection per client for every frame
of a game that moves tokens continuously, against a browser's ~6-connection-per-origin ceiling
under HTTP/1.1. SSE is used where it fits — the editor's change notice and rendered sidebar
fragments — and WebSocket carries state. The split is deliberate and is documented in §7.5 of
the UI specification.