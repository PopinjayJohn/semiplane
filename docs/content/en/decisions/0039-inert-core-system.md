---
title: "0039 — The inert `core` system, so phase 7 runs against something real"
description: "Intent resolution belongs to a gameplay `System` in phase 8. Rather than stub it or reorder the phases, phase 7 registers a system that parses and version-checks intents and resolves nothing — and every operation is refused by name."
lede: "A stub that returns 'not implemented' cannot carry a client through the protocol, so the hub, the monotonic version, the persistence cadence and the codec would be testable only against rejection paths. An inert system that refuses *by name* can be driven end to end, and phase 8 replaces it rather than extending it."
weight: 229
date: "2026-10-02"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

Phase 7 is the realtime plane: an in-memory `campaign_state`, a hub, a protocol codec, GM-only
operations and debounced persistence. Its Definition of Done includes the §14 row — *two clients
issuing concurrent intents converge with monotonic versions* — and UI §7.5's requirement that the
play page opens and stays current.

None of that is testable without a `Resolver`, and intent resolution belongs to a gameplay
`System`, which is phase 8. The plan anticipated this and named three ways out. Two are bad:

**Reorder the phases**, so plugins land before the realtime plane. That inverts the plan's own
dependency order, and the plugin contract (ADR 0011) is specified *in terms of* the realtime
version and intent vocabulary — there is nothing for it to plug into yet.

**Stub it**, with a resolver that returns "not implemented" for everything. This is the attractive
option and it does not work, for a reason worth stating plainly: **a system that refuses everything
cannot carry a client through the protocol.** A browser that connects, greets, sends presence, sends
an intent, and then receives a refusal for every intent exercises the codec, the hub, the read loop
and the reconciliation path only along their *error* edges. The optimistic-reconcile loop — the thing
`seq` exists for — never runs, because there is no applied change to reconcile against. The phase's
central test would pass against a product that cannot play a game.

## Decision

**Phase 7 registers an inert `core` system. It parses and version-checks intents and resolves
nothing, and every operation is refused by name.**

"Refused by name" is the whole of it. `not_your_turn` is not a valid answer for a system that has no
turn order, and a refusal that says nothing a client can act on is a client that retries forever. The
system answers with `protocol.go`'s closed reasons, so a client is told what it is being told by the
same vocabulary a real system will use.

Three properties, each of which is a decision rather than an implementation detail:

**The operation is shape-checked and never membership-checked.** The vocabulary belongs to the
gameplay system. A list here would be a second source of truth that phase 8 contradicts, and — worse
— a 5e client would be told by this system that its `roll` does not exist. What can be checked
without the vocabulary is that the token is shaped like one: non-empty, bounded, lowercase.

**A refusal broadcasts nothing.** A rejection that went to the campaign would tell every browser on
it that this client attempted something, and the reason travels on the wire. A refusal is between the
actor and the server.

**The role check runs before the "nothing is resolved" refusal.** A player attempting a GM-only
operation is refused `not_permitted` rather than `unknown_op`, because the first answers "is this
allowed" about *them* and the second answers "is anything allowed" about the server. Ordered the other
way, the word on the wire is a role oracle anyone on the campaign can read.

**Phase 8 replaces this type. It does not extend it** — an implementation that also does phase 8's
work would make "what does the inert system do" a question with two answers.

## Consequences

**The §14 row becomes testable.** Two real clients can connect to a real server, send concurrent
intents, and converge — because the hub, the version and the codec are the parts under test, and the
resolver being inert is a *named refusal* rather than a dropped connection.

**The GM-only rule gets its first real exercise.** `not_permitted` for a player is reachable, so
§7.2's authority rule is a live path rather than a comment until phase 8 supplies the real
vocabulary.

**There is a second, deliberately incomplete implementation of `Resolver` in the tree until phase 8.**
That is the cost, and it is paid once, for one phase, in exchange for phase 7 being verifiable rather
than merely compilable.

**The inert system's `isGMOnlyShape` list is a shape test, not an authority.** It exists so the
`not_permitted` path is reachable and testable, and it names the four operations whose *names* are
unambiguous. It confers nothing: this system resolves no operation for any role, so a player cannot
be harmed by it. It is replaced wholesale in phase 8, and the comment in the code says so.

## Alternatives considered

**Reorder the phases so plugins precede the realtime plane.** Rejected: ADR 0011's plugin contract is
specified in terms of the version and intent vocabulary that phase 7 defines. There is nothing for it
to plug into, and writing the vocabulary first would put the protocol's authority rules in a package
that has no use for them.

**A stub returning "not implemented".** Rejected above: it makes the phase's central test pass against
a product that cannot play a game, which is the exact shape of failure this repository's gates exist
to prevent. A green gate that measures the wrong thing is worse than a red one.

**Let `Resolver` be a nil interface and have the hub reject everything.** Rejected: it moves the
refusal into the hub, so `Hub.Apply` grows a second answer path, and the hub's tests would then
exercise the hub rather than the resolver — so a phase 8 resolver would arrive against a hub already
containing a competing answer to every intent.

**Have the inert system apply a trivial real change** — move a token to coordinates the client sent.
Rejected: it would make the hub's fan-out testable, but it would also mean shipping a state mutation
whose rules belong to a gameplay system, and a token that moved to coordinates nothing validated is a
token in the wrong place with a version stamp asserting it is authoritative.