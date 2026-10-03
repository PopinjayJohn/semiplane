---
title: "0041 — The rules contract returns mutations and carries its own randomness"
description: "`internal/domain/rules` follows §10.3's shape in every place where the record is right, and departs from it in three: `Apply` takes a `context.Context` and a state by value, and `Derive` returns a named `Payload`. `Context` carries a seed and nothing ambient, so `(state, intent, seed) → mutations` is reproducible by construction rather than by discipline."
lede: "The record's `Apply(ctx Context, state *State, in Intent)` is a pointer to live state and a `Context` with no way to stop it. Both are wrong for this codebase: the pointer is the in-place write S-10.2 exists to forbid, and a `*rand.Rand` on the system rather than on the resolution is the one shape that passes a replay test while breaking every audit."
weight: 3
date: "2026-10-02"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

Phase 8's first work item is the contract every later phase is typed against: the registry (P1b), the conformance suite (P1c), the determinism audit (P1d), the 5e engine and overlays (P2), house rules (P2d) and the UI-plugin contract. The architecture overview's §10.3 sketches that contract in nine methods, and the sketch is the right shape in seven of them. It is wrong in three places, and all three are places where the sketch predates what this repository has since built.

**The sketch's `state *State`.** §10.3 pairs "returns `[]Mutation`" with `state *State`, and the pointer reads as an implementation detail. It is not: it is a live pointer into whatever holds the authoritative tabletop, handed to a compiled-in plugin that has no sandbox and runs in-process with full authority. S-10.2's promise — *"a plugin therefore cannot write `campaign_state` or forge a broadcast. A panic inside `Apply` leaves `campaign_state` byte-identical"* — would then rest on every plugin author remembering not to write through a pointer they were given, which is a review comment rather than a property. `realtime` settled the same question for versions: `state.go` stamps `Placement.Version` back *after* the caller's function returns, "so authority cannot be delegated to a gameplay system". A pointer argument undoes that for the whole document.

**The sketch's `Context` with no cancellation.** §10.3 says `Context` carries the seeded RNG, campaign identity, actor identity and actor role. Nothing says how a resolution is stopped, and the hub's `Resolver.Resolve` already takes a `context.Context` that it does not pass anywhere. A plugin that hangs holds the hub's goroutine for the life of the process, and neither `Hub.Close` nor the request context can end it.

**The sketch's `Derive(state *State, q Query) (any, error)`.** Two problems in one line. The pointer is the same one. And a bare `any` loses the one fact semiplane needs about the answer: §10.6.1 makes semiplane's entire role in derived data *choosing the renderer*, so a payload that does not say which view it answers forces the choice to be made again at the call site — where a caller that got it wrong renders a character sheet with a stat block's renderer and produces HTML nobody can debug.

Two further constraints arrived with the code that already exists. `realtime.Op` validates an op's *shape* and never its membership, because membership is the registry's answer (protocol.go, rule 5) — so `rules.Op` has to accept exactly the charset the codec accepts, or an op this package validates would be one the wire refuses. And `realtime` mints placement ids, so `rules.ObjectID`'s validity rule has to be the same rule, restated under a comment rather than re-derived, because a stricter one here would produce a state the hub accepted and the system refused — a refusal that would look like a plugin bug. The system this contract replaces is
[0039]({{ "decisions/0039-inert-core-system/" | relURL }})'s inert `core`, which resolves nothing; the ruleset fingerprint the resolver must report is
[0018]({{ "decisions/0018-ruleset-version-fingerprint/" | relURL }})'s.

The fourth constraint is the one this record is mostly about. S-10.4 makes rule code deterministic — no `time.Now`, no map iteration, no direct `crypto/rand` — and is explicit that there is no sandbox, so the enforcement is a lint rule (P1d) plus tests. Those two mechanisms both work by noticing something. A lint rule notices a *call*. A test notices a *result*. Neither notices a *field*: a `Context` carrying a `Now func() time.Time` would not call anything, and every one of those gates would stay green while resolution had a clock in it.

## Decision

`Apply` returns mutations, takes the state by value, and takes a `context.Context` beside the rule context:

```go
Apply(ctx context.Context, call Context, state State, in Intent) ([]Mutation, error)
```

**`State` keeps its fields unexported.** A value with an exported `[]Object` would still share its backing array, so a plugin writing one byte into `Objects[0].Data[0]` would corrupt the hub's own copy with no compiler complaint and no lock involved. `NewState` sorts and copies, and `Lookup`, `Objects` and `OfKind` each hand out fresh copies. That is the whole of "a panic inside `Apply` leaves `campaign_state` byte-identical": there is no field to write to. The cost is a small allocation per read, which is the right trade at this scale, and the alternative's cost is a correctness property held by a comment.

**`rules.Mutation` carries no version and no revision.** It is the system's *statement* — target, op, opaque payload, whether the object is removed — and the hub's `realtime.Mutation` is the *receipt* — placement, version, revision, no op, no args. The translation is the adapter's, one line in each direction, and the two omissions are the safety: everything the hub owns arrives from the hub and everything the system owns arrives from the system, so neither can forge the other's half.

**`context.Context` rides beside `rules.Context`, never inside it.** `containedctx` forbids the alternative, but the reason to want the interface is the one the linter cannot state: a cancellation may *end* a resolution and may not *decide* one. A system that observes cancellation returns `(nil, ctx.Err())` and never a partial mutation list, because a list half-built on the way to being discarded is a list whose contents depend on when the hub cancelled. The two parameters are different types with opposite rules, and they are named differently — `ctx` and `call` — so neither can be passed where the other belongs.

**`Context` is four fields: campaign, actor, role, seed.** Nothing else, which is the whole enforcement available to a type: there is no clock field for a resolver to be handed, no handle, and no ambient source. Randomness comes from `Rand(label)`, which derives a fresh deterministic source from the seed and the label by a pure function, mixed through SHA-256 under a versioned domain string. A label, because two draws in one resolution need two names; the same label twice yields the same numbers, which is what makes a replay reproduce them. `*rand.Rand` rather than a narrow `Roll`/`Sum` interface, because those methods would be a dice vocabulary in the contract and §10.3's first requirement is that **the protocol never assumes d20**.

**A seed is not a secret and has no "unseeded" value.** §16.3 says the seeded RNG is supplied through `Context` "which makes a per-session seed + roll log auditable at no extra cost"; an audit nobody can check is not an audit, so the seed prints in full wherever the fingerprint that gates a resume prints. The zero seed is a legal seed that produces the same numbers forever, because a sentinel that reads as a seed is a campaign whose every roll anyone can reproduce by typing one hex digit.

**Grammar is data, and `Expr` records its owner.** A grammar carries patterns and an example so a client can preview and validate an expression before sending it; `Parse` remains the server-side authority, because S-7.3 makes the client able to ask for a roll and never to say what it was. `Expr` records the system that parsed it so a hub can refuse to hand a 2d6-pool expression to a d20 system — S-10.3's rule applied to the notation rather than the operation. Its node is `any`, which says what is true: a system value, routed back to the same system, read by nobody else. A concrete expression tree here would be a d20 tree with d20 node kinds.

**Views carry a renderer, not a component.** `View` is a name, a title, a `Renderer` and a `Shape`, and the plugin's templ component lives in `internal/web/plugins` under that name. `internal/domain/rules` does not import templ, which is what keeps the domain free of a rendering library and a plugin's component a Go value rather than a template name resolved at render time. A shape is required for a built-in view and forbidden for a plugin-rendered one, both directions, because one of the two has to decide.

**Ownership is enforced by `Validate`, and `Validate` is a function.** The five semiplane-owned kinds are named constants, and a `System` declaring one is refused with an `*OwnershipError` naming the kind and the claimant — "a kind is owned" is not something a plugin author can act on. A method on the interface would have made it a tenth method and every future method a breaking change; the function is what the registry calls once per plugin at startup.

## Consequences

The three deviations from §10.3 are recorded here rather than left as a silent divergence from a dated record, per AGENTS.md: `Apply` gains a `context.Context`; `state *State` becomes `State`; `Derive` returns `Payload` rather than `any`. Everything else in the sketch is unchanged — the same nine methods, the same names, the same responsibilities — so the conformance suite and the 5e engine are written against the record and this.

`State`'s accessors allocate. A resolution over a hundred placements makes a few hundred small allocations, which is nothing next to the encoding the system does anyway, and it buys a write path that does not exist.

`Validate` calls `system.ContentKinds()`, `system.Views()` and `system.Grammar()`, so registration runs a system's methods before the campaign does. A plugin whose constructor validates its own data packs is the shape this assumes; a plugin that needs to be told its packs are fine has to say so somewhere, and its constructor is the only place that can.

Two things follow for work this item does not own. The determinism audit (P1d) has three call sites to point at — `time.Now`, map iteration, and the package-level `math/rand` functions — and `Context.Rand` is the answer for the third; the `gosec` G404 finding on that one call is suppressed inline with that reason, because `crypto/rand` there would make every resolution unreproducible. And the adapter in P4 owns the translation table, including the `Expr.Owner()` comparison and the decision that a system returning mutations *and* an error has applied nothing.

Reproducibility rests on `rand.PCG`'s output, which the standard library holds fixed with a golden-output regression test rather than an API promise. If that changed, nothing already recorded would misresolve — past draws are in the state and in the log and are not re-derived — but a campaign's next roll would differ from what a reader with the same seed computes, which is the audit §16.3 is for. Writing a generator here would remove that dependency and add a second thing to get wrong.

## Alternatives considered

**Keep `state *State` and rely on the doc comment.** Rejected: it makes the most expensive invariant in the phase a promise. S-10.2 exists precisely because a plugin cannot be trusted to honour a convention when handing it the authority would be trivial, and the pointer is the authority.

**Put the cancellation inside `rules.Context`.** Rejected twice over: `containedctx` refuses it, and a context stored in a struct is by construction part of the value a resolution is a function of — which is the opposite of what the seed is.

**Give `Context` a `*rand.Rand` field.** Rejected: it is the one design that passes a replay test while breaking every audit. A resolver is called twice with identical inputs and, holding a shared source, draws different numbers the second time — reproducible only in the sense the property test checks, which is the failure S-14.6 exists to catch and the one §10.3's own wording invites. Deriving per label costs a hash.

**A narrow `Roll`/`Sum` interface instead of `*rand.Rand`.** Rejected: it puts a dice vocabulary in the contract. A d20 system wants `1 + IntN(20)`; a pool system wants six `IntN(6)` counted against a target. Any interface expressive enough for both is `Rand` wearing a hat, and one expressive enough for only one is the protocol assuming d20.

**A closed `Op` enum.** Rejected: §10.2 says a gameplay plugin defines the operation vocabulary, and a list here is a list a plugin cannot join.

**An `ErrInvalidKind`-style check only, with the five kinds as documentation.** Rejected: `token` and `scene` being semiplane-owned is exactly what lets the client render a table without knowing any rules. A plugin that could redefine them puts client work back on the list for every new system, and the cost of refusing is one function.

**Let `Context` carry a `Now func() time.Time` for systems that need elapsed time.** Rejected: there is nothing in a tabletop resolution that needs elapsed time, and a clock field is the one thing that makes a resolution's result depend on when it ran. If a rule genuinely needs one — a campaign-long timer, say — the answer is state the campaign's own clock holds, read through `Derive`, not ambient time.
