---
title: "Operating semiplane"
description: "Deploying, backing up, upgrading, and putting a reverse proxy in front of it."
lede: "One process, one SQLite file, one directory. There is no cluster to operate, and that is the design constraint most of this page is explaining."
weight: 30
---

## The single-instance rule

Authoritative game state lives in memory and the realtime hub is process-local.
**Two instances behind a load balancer will silently diverge.**

SQLite's single-writer limit is *not* the constraint — in-memory state ownership
is. This is fine for one table on one machine, which is what the tool is for. If
that ever changes, a shared broker is required before anything else. This is
recorded here rather than left implicit because "helpfully" putting a second
replica behind a load balancer is a mistake nobody makes deliberately.

## Why SQLite is enough

Game state is written on a trailing debounce — roughly two seconds after the last
mutation — and on shutdown. A busy table produces a handful of writes per minute
instead of hundreds per second, so the single-writer limit never binds. That
cadence is the main reason a file-backed database is adequate here.

The cost is a crash floor: the last debounced state. A mid-game restart resumes
from it, losing at most the last couple of seconds of movement.

## Backups

Two artifacts that can desync, so back up both:

| Artifact | Method | Why |
|---|---|---|
| **Content root** | Ordinary file copy, server stopped or no writes in flight | The source of truth for pages. Losing it loses content. |
| **SQLite file** | `.backup` API or `VACUUM INTO` | Never copy the raw file while the server is running. |

The database is a **rebuildable index plus live game state**. If you can restore
the vault alone, everything except the last few seconds of a game can be
regenerated. That asymmetry is worth remembering when deciding what to back up
first.

Page revision history is retained without a cap in v1. It is small text; a cap
becomes worthwhile only if sync churn turns out to be noisy.

## Reverse proxy

Serve over TLS with HTTP/2. The reason is specific rather than general: browsers
allow roughly six concurrent connections per origin over HTTP/1.1, and every
long-lived stream holds one permanently. HTTP/2 removes that ceiling entirely.

Do not buffer responses. The game route upgrades to a WebSocket and the event
routes stream; a buffering proxy holds both until they time out.

## Upgrades

Migrations are forward-only and live in the source tree. **A shipped migration is
never edited** — a new one is added. This is not fastidiousness: an edited
migration silently diverges for anyone whose database was already built from the
old version, and the resulting failure surfaces at the worst possible moment,
mid-campaign.

Before upgrading a live instance: take both backups, then stop the process
cleanly. The shutdown handler flushes campaign state, so a clean stop is what
makes the crash floor irrelevant.

## Degraded campaigns

A campaign whose content root has gone missing is reported as **degraded** in a
persistent banner in the footer, not as a toast. The condition is ongoing, not an
event, and a toast would have required re-notifying every load.

Degradation never takes down the instance. One campaign's missing directory
leaves every other campaign and the whole wiki serving. The same is true of a
content root that is deleted while running: it is marked degraded and retried
with backoff.

The condition this exists for is real and common: a sync client that has not yet
materialised a campaign's directory on this machine yet.

## Resource behaviour worth knowing

- **The watcher watches directories, not files.** A file that is atomically
  replaced stops being watched, and re-establishing the watch has a gap. Watching
  the containing directory survives files inside it being replaced.
- **Watch descriptors are per directory, not per file**, but the *number of
  inotify instances* is a much smaller limit on Linux. semiplane uses one watcher
  for all campaigns and routes by path prefix. If the limit is ever exhausted the
  failure is loud and falls back to periodic full rescan — never silent.
- **Assets are streamed with range requests.** Maps are tens of megabytes; they
  are not loaded into memory to be served.

## Further reading

- [Security]({{ "operating/security/" | relURL }}) — the trust boundary and how to report a problem
- [Content model]({{ "concepts/content-model/" | relURL }}) — where the content pipeline fits
