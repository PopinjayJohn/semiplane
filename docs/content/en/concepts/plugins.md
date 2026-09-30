---
title: "Plugins"
description: "How a gameplay system plugs in, and what a plugin is allowed to do."
lede: "Plugins are compiled-in Go modules, not files dropped into a directory. That single decision explains almost everything else on this page."
weight: 23
---

## Three tiers, not two

| Tier | Authority | What it is | Examples |
|---|---|---|---|
| **Gameplay plugin** | Resolves intents into state mutations | Go package plus embedded data packs | D&D 5e 2024, 5e 2014, a house-rule module |
| **UI plugin** | Reads state, renders. **Never writes state.** | Go package, templ components, JS | Link previews, a graphical dice roller |
| **Theme layer** | None | Plain files in the campaign content root | CSS, fonts, a header image |

Two corrections to the obvious taxonomy, both of which matter:

1. **House rules are a gameplay plugin**, not a UI modifier. They change
   mechanics, so they belong with the thing that resolves mechanics.
2. **A modifier that only adds CSS is not a plugin.** It is the theme layer:
   served from content, no code, no authority.

## The structural difference

> A gameplay plugin **defines the operation vocabulary and resolves it into
> mutations**. A UI plugin may only **emit operations that some gameplay system
> already resolves**.

Everything else follows:

| | Gameplay | UI | Theme |
|---|---|---|---|
| Runs | server | browser, plus server rendering | browser |
| Input | intent, state, seeded RNG | DOM events, render query | CSS cascade |
| Output | `[]Mutation` | DOM patches | nothing |
| Writes campaign state | **yes** | **never** | never |
| Determinism required | **yes** | no | no |
| Can define a new operation | yes | no | no |

A UI plugin cannot write state, and this is enforced structurally rather than by
convention: a gameplay plugin's `Apply` *returns* mutations instead of applying
them in place, so no plugin — UI or gameplay — can mutate state or forge a
broadcast in place of the hub's version counter. A UI plugin dispatches exactly
the same intents a human player would, and so passes identical authorization and
validation.

## The interface

```go
type System interface {
    ID() ID          // stable; stored in campaign state, never renamed
    Title() string
    RulesetVersion() string

    // This system's expression grammar, so the client can preview and validate
    // a roll before sending it.
    Grammar() Grammar
    Parse(expr string) (Expr, error)

    // Resolve a validated intent into mutations for the hub to version and
    // broadcast.
    Apply(ctx Context, state *State, in Intent) ([]Mutation, error)

    // Data for the UI: modifiers, DCs, sheet summaries. Opaque to semiplane.
    Derive(state *State, q Query) (any, error)

    // The views this system wants that data rendered through.
    Views() []View

    // Rules-content kinds this system defines: spell, class, feat, creature,
    // ancestry. NOT token, scene, journal, handout, or index.
    ContentKinds() []Kind
}
```

`Context` carries the seeded RNG, the campaign, the actor, and the actor's role —
never a wall clock, and never an ambient random source.

## Determinism is a discipline requirement

Because plugins are compiled in, there is no sandbox to catch a nondeterministic
rule. So rule code is forbidden from calling `time.Now`, ranging over a map, or
calling `crypto/rand` directly — enforced by a lint rule plus tests.

That is not ceremony. Campaign state supports optimistic client application,
resume-by-version, and replay. All three break if resolution is not a pure
function of *(state, intent, seed)*. A die roll that quietly consults the clock
is a roll nobody can audit after a rules dispute.

## Data packs, and why there is an overlay strategy

A pack is versioned embedded YAML: stat blocks, conditions, reference tables, and
the set of `kind`s the system recognises.

For 5e, which is roughly 90% identical across editions, a shared engine plus data
packs plus a narrow Go escape hatch is the split:

```
base pack (shared 5e data)
  ├── overlay: 2014  — race naming, crit on 20 only, no mastery properties
  └── overlay: 2024  — species, mastery properties, crit on any natural 20
resolver hooks (the escape hatch, only where data genuinely cannot express it)
```

The edition differences become reviewable data diffs, and only genuinely
procedural rules become code.

**Overlay is one strategy among several, not a requirement.** A plugin sharing
nothing with 5e — Pathfinder, say — ships one complete standalone pack and its
own resolver hooks, implements `System`, and touches neither the engine nor
semiplane. The only shared code is the interface, the intent and mutation types,
and the conformance suite.

The engine is therefore *shared 5e mechanics*, not "the rules engine". A system
with nothing in common with 5e does not use it.

## House rules

Applied at campaign load, producing an effective ruleset:

```
system ID → base pack → overlay → enabled house-rule modules (declared order)
         → effective ruleset (+ ruleset_version)
```

Two hard constraints:

- **House rules are data-level, not code-level.** A house rule may toggle
  `flanking_optional`, change a constant in a DC formula, or disable a condition.
  It may not reorder resolution or introduce nondeterminism — otherwise replay and
  audit die. This is what keeps house rules auditable and shareable.
- **Order is explicit and deterministic.** Modules carry a position; conflicts
  resolve first-match-wins and are logged with both module IDs. Never
  last-write-wins.

## Failure behaviour

| Condition | Behaviour |
|---|---|
| `system_id` unknown (plugin removed or renamed) | The game refuses to start and says which ID it wants. **The wiki still serves.** |
| `ruleset_version` differs from persisted state | Refuse to resume rather than silently misresolve past game state. The GM starts a new game. |
| House-rule conflict | First-match-wins by position, logged with both module IDs |
| A plugin panics inside `Apply` | Recovered at the `Apply` boundary; an error is returned and **state is unmodified**, because mutations are returned, never applied in place |
| A UI plugin throws in the browser | Contained to that one tab. The server is unaffected. |
| A stale `kind` after removing a plugin | The page degrades to prose. The content is still a file on disk, so nothing is lost. |

That last row is what makes extending `kind` safe: an unknown kind is inert, not
fatal, and the markdown is always recoverable.

## Conformance

A published suite every `System` must pass: determinism across repeated runs,
tolerance of unknown `kind`s, refusal on a ruleset-version mismatch, enforcement
of GM-only operations, and containment of a panic inside `Apply`.

**A system that shares nothing with 5e must pass it without touching the engine.**
That is the test that proves the plugin system generalises rather than merely
existing.

## API stability

Plugins are compiled in, so there is no semver contract across releases.
`Intent`, `Mutation` and `Kind` are therefore semi-public. If plugins ever move
out of tree, these need versioning before that happens — and the reason to do it
then, rather than now, is that versioning an interface before it has ever been
frozen is guesswork.
