---
title: "0009 — Server-authoritative intents"
description: "Clients send intent and never state. The server validates, applies, versions, and broadcasts."
lede: "A tabletop is a shared document that everyone trusts at the same moment. Trust has to live in one place, or two clients can each believe they are right and neither is."
weight: 90
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

A tabletop shows a map that several people are looking at, and they are acting on it at the
same time. This is the concurrency problem the rest of the project inherits, and its shape is
set by two things: the server must decide what is true, and players are on flaky connections
doing optimistic updates to stay responsive.

## Decision

**Clients send intent, never state.** The protocol carries operations:

```jsonc
// client → server
{"t":"intent","seq":7,"op":"move_token","args":{"placement":"p1","x":420,"y":180}}
{"t":"intent","seq":8,"op":"roll","args":{"expr":"1d20+5","reason":"perception"}}

// server → client
{"t":"applied","seq":7,"version":44,"op":"move_token","args":{…},"by":"u1"}
{"t":"rejected","seq":7,"reason":"not_your_turn"}
{"t":"delta","version":45,"changes":[{"op":"set_hp","placement":"p1","hp":7}]}
```

Five things follow, and each is load-bearing:

1. **`seq` is the client's sequence number.** It exists so a client can match an `applied` or
   `rejected` to the intent it sent, and reconcile its optimistic update. It is not an ordering
   authority.
2. **`version` is the server's monotonic version, and it is the only ordering authority.**
   Two clients cannot disagree about order when one of them is authoritative.
3. **Dice are evaluated server-side.** The client never supplies a result. A client-side roll is
   unverifiable, and it would break the audit trail the moment anyone at the table wanted to
   check whether a number was fair.
4. **Versioning is per-placement, not global.** Global versioning would make two players moving
   unrelated tokens contend for the same counter.
5. **Reconnect sends `since` for a delta**, not a full snapshot, so a client returning after a
   dropped connection does not re-download state it already has.

### Authority is enforced server-side

GM-only operations — pausing the game, setting another token's hit points, revealing a secret —
are checked on the server. Client-side hiding is cosmetic and is treated as such: it exists to
avoid showing a player a control they cannot use, and nothing more.

### Presence is ephemeral

Cursors and focus are broadcast but **never persisted**. They are not game state and do not
appear in `campaign_state`.

## Consequences

- Clients apply optimistically and reconcile on version mismatch. That is a real cost in the
  client, and it buys responsiveness on a bad connection.
- `roll` cannot be previewed with certainty — the client can parse and validate an expression
  against the system's grammar, but the result arrives from the server. The `Grammar()` method
  exists precisely so a d20 system and a 2d6-pool system can both describe their own notation
  without the protocol assuming either.
- Because mutations are **returned** by the resolver rather than applied in place, no code path
  — plugin or otherwise — can write state or forge a broadcast. See
  [0012 — plugin
  authority]({{ "decisions/0012-plugin-authority/" | relURL }}).
- Replay works, because resolution is a pure function of (state, intent, seed). That is what
  makes the determinism rule in the architecture record's §10.3 a requirement rather than a
  nicety.

## Alternatives considered

**Client-authoritative state with server validation at write time.** The client computes the
result and the server checks it. Cheaper for the server, and it breaks as soon as two clients
diverge: the server must then either reject the second write or accept state no client believes.
It also makes every GM-only operation a UI affordance rather than a rule.

**CRDTs.** Genuinely the right answer for some shapes of concurrent editing. Rejected because a
tabletop is not collaboratively edited prose — it has a turn order, a permission structure, and
rules that must be applied, and a CRDT would have to be told about all three. The complexity is
the same either way; the authority model would be much harder to reason about.

**Last-write-wins without versions.** The cheapest thing that could possibly work, and the thing
that silently loses a token's position. Explicitly rejected.