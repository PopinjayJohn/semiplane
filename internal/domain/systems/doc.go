// Package systems is where gameplay systems live: the compiled-in Go packages that
// implement `internal/domain/rules.System` and ship their data with `go:embed`.
//
// It exists as a package before it has a line of rule code, and that is the point of
// the directory being here in phase 8 rather than arriving with the 5e engine. Three
// rules are load-bearing about it and none of them is visible in this file, so they
// are written down where the first system will read them:
//
//  1. **A system implements `rules.System` and nothing else of semiplane's.** It does
//     not import `internal/realtime`, `internal/store`, `internal/httpapi` or
//     `internal/plugin`. The dependency runs inward — `plugin → realtime → domain` —
//     so a system that reached for the hub would create a cycle the compiler refuses,
//     which is a fine outcome for a first attempt and a confusing one for a third.
//     `internal/domain` imports nothing from the project, and a system is domain's
//     own vocabulary: it needs no package to be right.
//
//  2. **A system holds no ambient state.** No clock, no `crypto/rand`, no map
//     iteration whose order reaches a result, no handle. S-10.4 makes determinism a
//     discipline requirement with no sandbox behind it, and the enforcement available
//     to a *type* is subtraction: `rules.Context` carries four fields and the only
//     randomness it offers is `Rand(label)`, derived from the recorded seed. A system
//     that reaches for `time.Now` compiles, ships, and resolves a replay differently.
//
//  3. **A system declares what it owns and nothing more.** `ContentKinds` is
//     rules-content kinds — `spell`, `class`, `feat`, `creature`, `ancestry` — and
//     `rules.Validate` refuses one that declares `token`, `scene`, `journal`,
//     `handout` or `index`, because those are semiplane's in every build and forever.
//     `token` and `scene` being semiplane's is what lets the PixiJS map layer render a
//     table without knowing any rules, and so what makes a system sharing nothing with
//     5e need no client work at all.
//
// ## What belongs here, and what does not
//
// A system is the rules: the shared mechanics of 5e, the data packs
// (`go:embed`ed YAML, versioned with the code), the resolver hooks for the procedural
// rules a pack cannot express, and the `rules.System` implementation that ties them
// together. House rules are here too, and they are here because ADR 0011 reclassified
// them: a house rule changes mechanics, so it belongs to the tier with authority and
// not to a UI modifier that does not exist.
//
// The adapter is **not** here. `internal/plugin` translates between a system and the
// hub — it owns the mapping from `rules.Mutation` to `realtime.Change`, the recovery
// boundary around `Apply`, and the per-resolution seed. A system that wanted to write
// a broadcast would have to reimplement that, which is the one thing §10.2's sentence
// forbids, so the boundary has one owner and a system has none.
//
// ## What a system still has to supply, and where it goes
//
// `internal/plugin`'s `Codec`: the translation between a hub placement and the
// system's own encoding of a game object, in both directions. It exists because
// `rules.Mutation.Args` is the system's opaque payload and `realtime.Placement` is
// semiplane's envelope, and only the code that wrote the encoding can read it back —
// so `Codec` is a system value, and a system registers one alongside itself. The
// shared `plugin.PlacementCodec` is the projection with no opinion in it: an object is
// its placement, encoded as that placement's own JSON. A system with per-object state
// of its own — a 5e creature's action economy, a Starfinder's cargo — writes its own.
//
// # What this package does not contain
//
// No code, no `init()`, and no registration. S-10.1 and ADR 0011: a plugin is
// registered in the composition root, where the order is readable, because with
// versioned packs the order is reviewable information. `internal/plugin` has a test
// that parses this package's directory — and every other plugin package — for `init`,
// because a rule that is easy to write down and easy to lose in a refactor is worth a
// test rather than a comment.
package systems
