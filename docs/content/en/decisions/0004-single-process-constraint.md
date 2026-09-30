---
title: "0004 — One process, and what that forecloses"
description: "Authoritative game state lives in memory. Two instances behind a load balancer will silently diverge."
lede: "This is the single most important constraint in the project, and it is the one most likely to be 'helpfully' worked around by someone who has not read it. Nothing scales semiplane until a shared broker exists, and that is a different architecture."
weight: 40
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

semiplane holds live tabletop state — token positions, current hit points, conditions,
initiative order — in memory, and persists it to `campaign_state` on a trailing debounce of
roughly two seconds after the last mutation.

The WebSocket hub is process-local. A client connected to instance A receives deltas applied by
instance A's in-memory state. A client connected to instance B receives deltas applied by
instance B's copy. Nothing coordinates them.

**Two instances behind a load balancer will silently diverge.** Not fail loudly — diverge. Each
client sees a coherent, plausible, wrong game. This is the worst failure shape available,
because nothing errors and a table plays on.

It is worth being precise about why. The tempting assumption is that SQLite's single-writer
limit is the constraint, and that swapping in PostgreSQL would fix it. It would not. The
constraint is **state ownership**: whichever instance owns a token's position owns the truth,
and with more than one owner there is no truth. The debounced write cadence (§7.3 of the
architecture record) exists precisely so that the write volume never approaches SQLite's limit,
so SQLite's limit is not what binds — memory ownership is.

## Decision

**One process. One instance. Always.**

Not "one process by default" and not "one process until you need more". There is no clustering
code, no leader election, no sticky-session requirement to remember, and no configuration knob
that would permit a second instance to start.

The constraint is recorded in `AGENTS.md` rather than only here, because an agent reading the
repository will not necessarily read a dated design record, and this is exactly the kind of
constraint that looks like an oversight.

## Consequences

- **Deployment is one process behind whatever you already run.** systemd, Docker, a reverse
  proxy — any of them, as long as there is one of them.
- **HTTP/2 is recommended, not required.** The design record notes that the browser's ~6
  concurrent HTTP/1.1 connection ceiling per origin is what constrains concurrent SSE streams.
  HTTP/2 removes it. That is a deployment recommendation, not a code change.
- **No horizontal scaling without a shared broker.** If a real deployment needs it, the broker
  comes *first* — before anything else — and it is a different architecture: the in-memory
  state moves behind it, or the authoritative model moves to a single designated writer with
  everyone else reading from it. Neither is a configuration change, and neither is additive to
  what is here.
- **A restart resumes from the last debounced state.** The crash floor is roughly two seconds
  of gameplay. A table that restarts mid-combat resumes from the last write, which is the
  correct and acceptable trade for the simplicity.
- **Upgrades are stop-start.** There is no rolling upgrade, because there is nothing to roll.

## Alternatives considered

**Run multiple instances with a shared SQLite file on a network filesystem.** Rejected. SQLite's
locking assumes a single host; over NFS or similar the locking guarantees do not hold, and the
result is corruption rather than divergence — strictly worse.

**PostgreSQL and move authoritative state into it.** This removes the in-memory ownership and
is the right answer at a much larger scale. Rejected for now on cost: an operator has to run
and back up a second service, which breaks the one-process deployment story that is the
product's premise. If a deployment outgrows one instance, this is the first option to revisit.

**A shared state broker (NATS, Redis Streams) in front of one authoritative writer.** The
middle path: one writer owns state, replicas serve static content and proxy game traffic.
Rejected as speculative — it is a real distributed-systems project with real failure modes, and
building it before there is a deployment that needs it means building it untested against
reality. It is named here so the next person to hit the ceiling starts from an answer rather
than a search.