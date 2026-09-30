---
title: "0014 — Datastar for live chrome only"
description: "SSE patches rendered DOM fragments. It never carries content delivery and it never carries the map."
lede: "Reactive streams are the right tool for a live sidebar and exactly the wrong tool for everything else in this product. Drawing that line is what keeps pages cacheable."
weight: 140
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

A live tabletop needs several things updating in real time: the initiative tracker, the chat
log, the dice log, the connection indicator. Server-sent events are the natural transport, and
it is tempting to make them the default mechanism for anything that changes.

Doing so would break a property the whole content pipeline depends on: **rendered page content is
cacheable and stable within a session.** Filesystem markdown does not change because a token
moved. Shipping it over a reactive transport would mean it could not be cached, and would mean
every reader of a page holds a connection open to receive content that is not changing.

## Decision

**Datastar patches DOM over SSE, for genuinely reactive fragments only. Content delivery is
always ordinary HTTP with an `ETag`.**

SSE appears in exactly two places:

1. **Live game chrome** — `/c/{slug}/events`, carrying rendered sidebar fragments.
2. **The editor's external-change notice** — "this page changed on disk", so a GM editing in
   the browser knows when Obsidian has moved underneath them.

### What never flows over it

- **Campaign content.** Ordinary HTTP, `ETag`, cacheable. Filesystem markdown is
  session-stable, so treating it as live would cost a connection per open page and gain
  nothing.
- **Search.** Submit-to-navigate over `GET /c/{slug}/search?q=`, `ETag`-cacheable, with the
  result count in the `<h1>`. A client-side search renderer is prohibited outright — it cannot
  reproduce the server's FTS tokenisation (see
  [0007]({{ "decisions/0007-fts5-tokenizer/" | relURL }})) and would produce different results
  for the same query.
- **The map.** PixiJS owns one `<div>` and everything inside it. templ renders the shell and the
  sidebar; Datastar patches the DOM *around* the canvas. The canvas is a structured-data
  consumer, not a DOM consumer.

### The connection budget

Browsers allow roughly six concurrent HTTP/1.1 connections per origin, and **every SSE stream
holds one permanently.** So the play page opens exactly one SSE and exactly one WebSocket — two
of six — and the shell opens none. Content pages open none.

This is why the map must not be an SSE-driven element, and why search is not type-ahead. Both
would multiply permanent connections per user.

## Consequences

- Content pages stay cacheable, shareable, and free of open connections. That is the point.
- The play page's sidebar needs both transports, because the sidebar wants rendered HTML while
  the canvas wants structured deltas. One hub, two egress representations of the same state.
- Search cannot feel like an instant-as-you-type experience, and that is accepted deliberately.
- Moving to HTTP/2 removes the connection ceiling entirely. A deployment recommendation, not a
  code change.

## Alternatives considered

**WebSocket for everything, including content.** One transport, one connection. Rejected: it
would make cacheable content uncacheable to get a connection-count saving that is not currently
a problem.

**Full client-side reactivity (a SPA framework owning the document).** Better DX for the
reactive parts, and the standard answer for dashboards. Rejected for the same reason as
[0013]({{ "decisions/0013-templ/" | relURL }}): the document here has a deliberate
accessibility contract, a specific answer about live regions, and a canvas the server must not
touch. A framework that owns the document owns all three.

**Polling instead of SSE.** Cheaper infrastructure, no permanent connections. Rejected on
latency for chat and dice at a table, where a round-trip per second is visible to everyone.