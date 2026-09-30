---
title: "0011 — Plugins are compiled-in, registered explicitly"
description: "No dynamic loading. No init(). Registration happens in the composition root where it can be read."
lede: "A plugin system is usually sold on extensibility. This one exists for a narrower reason: keeping rules and presentation in separate packages with their own tests. Nothing about it requires loading code at runtime."
weight: 110
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

The requirement is that a new gameplay system costs no client work, and that rules code is
testable in isolation. It is not a requirement that a user can install a plugin by dropping a
file into a directory.

Those are different products. This one is the first, and it is much simpler.

## Decision

**Plugins are compiled-in Go modules, registered explicitly in the composition root. There is no
`init()`.**

```go
// cmd/server/main.go — the composition root
registry := plugin.NewRegistry()
registry.RegisterSystem(dnd5e.New2024())
registry.RegisterSystem(dnd5e.New2014())
registry.RegisterUIPage(linkpreview.New())
registry.RegisterUIRender(diceroller.New())
```

### Why not `init()`

`init()` is the idiomatic Go registration pattern, and it is wrong here for two specific
reasons:

1. **It hides ordering.** The reader cannot see what is registered or in what order without
   running the program. With versioned rules packs, registration order becomes meaningful —
   a base pack and an overlay have to be composed in the right order — and an invisible order is
   an order nobody can review.
2. **It leaks state between tests.** A package that registers itself into a global at import time
   is registered for every test in the binary that imports it. Two tests wanting different
   registrations cannot both have them.

Both become real problems exactly when rules packs get versioned, which is the design this
project is aiming at.

### Three tiers, and only one of them has authority

| Tier | Authority | Can define a new `op`? |
|---|---|---|
| **Gameplay** | Resolves intents into mutations | **Yes** |
| **UI** | Reads state, emits operations an existing system resolves | No |
| **Theme** | None — plain files in the campaign content root | No |

Two corrections to the original brief are baked in here, because both were wrong:

- **House rules are a gameplay plugin**, not a UI modifier. They change mechanics, so they sit
  in the tier with authority.
- **A modifier that only adds CSS is not a plugin.** It is the theme layer: files served from
  the campaign's content root, with no code and no authority.

## Consequences

- A new system is a Go package plus YAML data packs, added to the composition root. No
  installation step, no version negotiation with a plugin host, no sandbox to build.
- `campaigns.system_id` that no plugin resolves **does not fail the boot**. The campaign's wiki
  still serves; only the game refuses to start, and it says which ID it wanted. Removing a
  plugin degrades a feature rather than taking down a site.
- Because plugins are compiled in, there is no semver contract to honour across releases.
  `Intent`, `Mutation`, and `Kind` are therefore semi-public — which is a real constraint, and
  the reason they are named and documented rather than incidental.
- Data packs are versioned *with the code*, via `go:embed`. A pack cannot drift from the code
  that reads it.

## Alternatives considered

**HashiCorp go-plugin or a gRPC plugin host.** Real dynamic loading, at the cost of a process
boundary, a protocol version, a discovery mechanism, and a security model for loading code. All
of that infrastructure exists to solve a problem this project does not have, and each part is a
thing that can break.

**`.so` plugins via a C ABI.** Would allow a genuinely non-Go system. Rejected: it would make
rule code non-deterministic across a boundary, and determinism is the property that makes replay
and audit work.

**One monolithic rules package with a type switch.** No plugin system at all. Rejected because
it puts a new system in the same package as every existing one, so they cannot be tested
separately and cannot be built separately — which was the actual reason for the plugin boundary.