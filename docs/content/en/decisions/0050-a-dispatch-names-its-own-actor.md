---
title: "0050 — A dispatch names its own actor, and an actor cannot be decoded from bytes"
description: "`Hub.Dispatch` resolves an intent on behalf of an identity rather than a connection, so a UI plugin can dispatch without a socket. The identity is a `realtime.Actor` with unexported fields and a refusing `UnmarshalJSON`, so 'every field is server-derived' is a property of the type rather than a habit of its call sites."
lede: "The plugin tier shipped with an `Emitter` interface and no way to implement it, because `Hub.Apply` needed a `*Peer` and a peer is minted by `Join`. The obvious fix — let a handler pass an identity struct — would have made 'act as any user in any campaign' a capability any route could hand out."
weight: 3
date: "2026-10-02"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

[0042]({{ "decisions/0042-the-plugin-registry-is-explicit-and-a-ui-plugin-can-only-emit-known-ops/" | relURL }})
built the UI plugin tier and left one thing conspicuously unwired. Its `Emitter` is the only
authority a UI plugin holds over game state, and it dispatches an intent rather than
holding a state handle — which is [S-10.3]({{ "contributing/spec/" | relURL }})'s whole point, and
which cannot work. `Hub.Apply` takes a `*Peer`, and a `Peer` is minted by `Join`: a real
connection, a presence entry, a slot in the campaign's peer limit.

A UI plugin renders during an ordinary HTTP request. It has no socket. So the tier shipped
with an interface and no implementation, and a plugin that rendered a die widget could not
fire it.

Two shapes would fix it. The first is a hub method that takes an identity value — a campaign
id, a user id, a role — and resolves on that. The second is what the tier's own doc comment
already warned against: a capability. A struct with exported fields, built by whatever
handler had one, is a struct a future route can populate from a form post, and what it would
be handing out is the ability to act as any user in any campaign. That is not a new bug. It
is [0024]({{ "decisions/0024-authorisation-gates-mount-not-per-handler/" | relURL }})'s subject from
the other direction: authorisation is a gate the route mounts, and a value a route *fills in*
has moved the decision back inside the handler where S-8 says it must not be.

## Decision

`Hub.Dispatch(ctx, who Actor, frame) (Resolution, error)` resolves an intent as `who`, tells
the campaign what changed, and returns the answer. `Apply` keeps its own order: answer the
peer's `seq`, then announce.

The two share `announce`, so "how a change reaches the table" has one implementation. They do
not share an order, and that is deliberate.

`Actor`'s fields are unexported. `NewActor` builds one and refuses a campaign that is not
positive. `Dispatch` checks again, because the zero value is constructible without
`NewActor`. `Actor` implements `UnmarshalJSON`, and it refuses.

## Consequences

**A UI plugin can dispatch, on the same road as a keystroke.** The access gate, the codec, the
codec's bounds and the system's own role check apply to it exactly as they apply to a player,
because it is the same call.

**"Server-derived" is now enforced by the type rather than by review.** The forged identity has
to survive a `json.Unmarshal`, and the answer is an error at the line that made the mistake.

**The wire order did not change, and finding that out cost three tests.** The first version of
this had `Dispatch` publish and `Apply` answer afterwards, on the argument that the resolver
has already mutated state by then, so a peer whose answer queue failed must not be the reason
the other clients went stale. It passed every hub test and failed three in
`internal/httpapi/play`, which read the socket's first frame after an intent and require it to
be the `applied`. That is the right outcome: the order is client-visible, three committed tests
pin it, and phase 8 is about plugins. So `Apply` answers first and the cost is recorded above
in the code instead of fixed here — a peer whose answer queue has failed is still not told,
which is phase 7's behaviour, unchanged.

**A refusal is an error from `Dispatch` and a frame from `Apply`.** Only a peer has a `seq` to
reject; a UI plugin needs the reason as a value it can render, not as a frame nobody would read.

**`ruleset.Actor` was renamed to `ruleset.AuditActor`.** The package was using one word for two
identities: `Intent.Actor` is the user a resolution is performed *as*, and the audit type is who
an `audit_log` row is attributed *to*, carrying a display name and no campaign and no role.
The dispatch seam needed the word `Actor` and found it taken. Two names, each saying which
question it answers, beat a third word or a collision.

**A test that could not be written is part of the argument.** `TestAnActorCannotBeDecodedFromBytes`
was written first against unexported fields and no `UnmarshalJSON`, and it failed.
`encoding/json` does not reject a struct it cannot populate: it returns no error and changes
nothing. So the decode "succeeded", the actor came out zero, and the only thing between that and
a dispatch as the zero actor was `Dispatch`'s campaign check — one layer away, with a message
about a campaign. A silent failure wearing a loud-looking one. The method exists because the
test failed, not because the shape looked right.

**The call-site audit could not see what it was written for, twice.** `internal/httpapi`'s
`TestNoRouteBuildsAnActorFromRequestData` walks every handler looking for an identity built out
of something the client chose. Its first version searched the *arguments* of a reader call for
the type name, and passed on a tree containing exactly the forgery it exists to prevent —
because Go passes variables, not types. Its second version watched the right calls but skipped
`id, _ := strconv.ParseInt(v, 10, 64)`, which is the commonest way a handler turns a request
into a number. Both were caught by injecting the forgery and watching the audit stay green. It
resolves one hop of local initialisers and says so in its own doc comment, because one hop is
the honest limit; `go/types` is the answer if this ever has to survive an adversary rather than
a refactor.

**`r.PathValue` is deliberately not a client-chosen reader.** A first draft of the reader set
included it and reported `edit/edit.go`, because the campaign *is* meant to come from the URL
path. The rule is not "nothing in the request may reach an identity"; it is "the campaign comes
from the path and the user comes from the session", and a reader set that cannot tell those two
apart would have to be deleted for being wrong.

## Alternatives considered

**Let the emitter hold a `*realtime.CampaignState`.** Rejected: it is the write path the tier
exists to withhold, and [S-10.2]({{ "contributing/spec/" | relURL }})'s refusal to apply in place
would be the only thing standing between a UI plugin and `campaign_state`.

**Export `Actor`'s fields and rely on review.** Rejected, and it is the decision this record
exists to prevent from being made again by accident. It compiles, it works, and "we do not do
that" is a habit.

**Mint a synthetic `Peer` for a UI plugin's dispatch.** Rejected: a peer is a presence entry and
occupies a slot in the campaign's peer limit, so every render of a widget would be a ghost
connection, and a plugin would have been able to address itself to the room rather than answer
one request.

**Have `Apply` delegate to `Dispatch` so there is one publish site.** This is what was built
first and it did not work: delegating fixes the *implementation* and inherits the *order*, so
the wire changed with it. Sharing `announce` keeps one implementation and leaves each caller
choosing when, which is the actual distinction between them.

**Make `Actor` fields unexported and stop there.** Rejected because it does not work, which is
the finding rather than a preference: `encoding/json` walks past an unexported struct and
returns no error. The type needed the method too.
