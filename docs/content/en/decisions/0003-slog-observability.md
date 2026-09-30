---
title: "0003 — slog, with no metrics server and no tracing"
description: "Structured logs with a stable event key, plus counters on /readyz. Nothing else."
lede: "Four subsystems in this system fail silently by default. A watcher that stops watching, a cache that serves a stale tier, a reconciliation loop that gives up, and a hub that leaks connections each need one greppable signal. That is the entire observability requirement, and it does not justify a metrics server."
weight: 30
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

The design record names four subsystems that fail quietly:

| Subsystem | How it fails silently |
|---|---|
| The watcher | Exhausts its inotify watches and stops noticing edits |
| The render cache | Serves a stale or wrong-tier variant |
| Secret reconciliation | Gives up and leaves a secret hidden, forever |
| The WebSocket hub | Leaks connections until the table cannot connect |

Each of these is invisible to a health check that only reports "the process is up". The
question is how little machinery is needed to make them visible.

## Decision

**`log/slog` is the only observability mechanism.** No metrics server, no tracing stack, no
statsd, no OpenTelemetry.

Every event is emitted with a stable `event` key so it is greppable and alertable-on:

```go
logger.Error("watch.add_failed",
    slog.String("event", "watch.add_failed"),
    slog.String("campaign_id", id),
    slog.String("error", err.Error()))
```

Plus **counters exposed on `/readyz` as JSON**, because a counter in a log is only useful if
something reads it, and `/readyz` is already the endpoint a supervisor polls.

The required signals are enumerated in the architecture record. Three of them must never be
below error level, because they are the security-relevant ones:

- `secret.reconcile_capped` — the system *chose* to hide something and a sync fight is
  unresolved. The word "chose" is the point: this is the system reporting that it gave up on
  the user's behalf.
- `watch.add_failed` — watch exhaustion is the highest-impact silent failure in the content
  pipeline. It must be an error, never a debug line.
- `content.render_error` — never serve a broken page quietly.

### Two rules on payloads

Every event carries `campaign_id`, and `path` where applicable. **No event carries secret
content, file contents, or dice results.** This is asserted by a test, because the natural
thing to do when debugging a render failure is to log the thing that failed to render, and
that thing is frequently a secret callout.

## Consequences

- Operators write log-based alerts. `event=secret.reconcile_capped` is a one-line query with
  no infrastructure to stand up first.
- There is no per-request latency histogram in the usual sense. `state.write_ms` is the one
  histogram, because debounced persistence latency is the signal that says the single SQLite
  writer is contended — which is the failure mode §13 of the architecture record calls out.
- Adding a real metrics pipeline later is additive, because the event names are already stable
  and the counters are already exposed. A scraper can read them; nothing has to change.

## Alternatives considered

**Prometheus client + `/metrics`.** The conventional answer, and genuinely better for rate and
histogram work. Rejected because it adds a second exposition format, a second thing to run, and
a dependency, in exchange for capabilities this project does not yet have a use for. If the
`state.write_ms` histogram or the `cache.hit`/`cache.miss` by-tier counters turn out to need
real aggregation, this is the record to revisit first.

**OpenTelemetry.** Would give request traces across the WebSocket upgrade and the SSE
lifecycle, which is the hardest part to debug. Rejected as speculative: the hard part is
currently unshipped, and adopting a tracing stack before there is a trace to trace is cost
without benefit.

**Nothing beyond `http.Server.ErrorLog`.** Cheapest of all, and it is what the scaffold had.
Rejected because it cannot distinguish the four silent failures above, which is the whole
reason the record enumerates them.