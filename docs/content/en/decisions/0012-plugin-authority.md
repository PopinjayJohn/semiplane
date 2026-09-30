---
title: "0012 — Plugin authority is enforced structurally"
description: "A UI plugin can emit operations a gameplay system resolves. It cannot write state, and it does not need to be trusted not to."
lede: "The claim 'a plugin must not do X' is worth nothing on its own. This record is about making the architecture such that the forbidden thing has no code path, rather than about asking plugins politely."
weight: 120
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: []
---

## Context

There are three plugin tiers, and only one of them has authority over game state. The dangerous
proposition is: **a UI plugin must never be able to write `campaign_state`.**

That is a security property. It is also, phrased as an instruction, unenforceable — it asks
every plugin author to remember a rule, and it asks every reviewer to verify it on every
change.

## Decision

**Enforce it by construction.** The structural difference, stated precisely:

> A gameplay plugin defines the operation vocabulary and resolves it into mutations. A UI plugin
> may only emit operations that some gameplay system already resolves.

Two design choices do the work.

**1. Resolution returns mutations; it never applies them.**

```go
Apply(ctx Context, state *State, in Intent) ([]Mutation, error)
```

Not `Apply` mutating `state` in place. The resolver is a pure function of (state, intent,
seed) returning a description of what should change. The hub applies them, increments `version`,
and broadcasts. So:

- No plugin can write state, because nothing a plugin returns *is* state.
- No plugin can forge a broadcast, because it never reaches the broadcast path.
- A panic inside `Apply` leaves `campaign_state` byte-identical to before, because the mutations
  that would have changed it were never applied. The hub recovers, returns an error, and the
  failure blast radius is one campaign state.

**2. `Context` carries everything ambient, and nothing else.**

It carries the seeded RNG, campaign identity, actor identity, and actor role. It does **not**
carry a wall clock and it does **not** provide an ambient random source. Determinism is a
discipline requirement enforced by a lint rule and tests — rule code may not call `time.Now`,
range over a map, or call `crypto/rand` — because there is no sandbox and the packages are
compiled in.

## What a UI plugin actually does

It dispatches the same intents a human player would:

| Plugin | Behaviour |
|---|---|
| Graphical dice roller | Renders a die widget. On send, emits `{"op":"roll"}` and renders the **server's** result. It must not roll client-side — an unverifiable roll breaks the audit trail. |
| Link preview | A render hook on external links. The server-side unfurl helper is read-only and returns `{title, description, image}`. It must not become a path for reading private campaign content. |
| House-rules widget | Displays which house rules are active. Read-only; the rule data comes from the gameplay module, never from the UI. |

A UI plugin passes **identical** authorization and validation, because it is sending the same
intents. That is the whole trick: there is no separate, weaker path for plugins to take.

## Consequences

- Plugin isolation is testable rather than asserted: panic inside `Apply`, assert
  `campaign_state` is byte-identical. Assert a UI plugin has no reachable write path at all.
- **Kind ownership is part of authority.** semiplane owns `journal`, `handout`, `index`, `token`,
  and `scene`; a gameplay plugin declares only rules-content kinds (`spell`, `class`, `feat`,
  `creature`, `ancestry`). `token` and `scene` being semiplane-owned is the load-bearing part:
  the PixiJS map layer renders placements, fog, and initiative without knowing any rules, so a
  new system needs no client work.
- House rules stay **data-level**. A module may toggle `flanking_optional`, change a DC formula
  constant, or disable a condition. It may not reorder resolution or introduce nondeterminism —
  which is what keeps replay and audit intact, and is why a module that tries is rejected.
- Determinism is not a style preference. It is what lets `campaign_state` support optimistic
  client apply, resume-by-version, and replay. Any of those break if resolution is not a pure
  function of its inputs.

## Alternatives considered

**Sandboxing the plugin code.** A separate process, a seccomp profile, WASM. This is the answer
if plugins ever move out of tree. It is not an answer today, because it costs a process boundary
and a protocol to solve a threat that compiled-in packages do not have — a plugin with access
to `campaign_state` is not an *attacker* here, it is a mistake, and mistakes are caught by types
and tests.

**Capability-passing runtime (an interface exposing only the allowed operations).** Attractive,
and closer to the ideal. Rejected as more machinery than the problem needs: the `[]Mutation`
return already gives the capability restriction, in the type, for free.

**Documenting the rule and relying on review.** Rejected on the grounds this record exists to
explain. A rule with no code path behind it is a comment, and comments do not survive refactors.