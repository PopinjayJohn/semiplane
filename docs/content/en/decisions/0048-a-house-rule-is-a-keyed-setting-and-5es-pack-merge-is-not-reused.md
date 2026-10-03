---
title: "0048 — A house rule is a keyed setting, and 5e's pack merge is not reused"
description: "The house-rule layer is a set of settings identified by `(capability, key)`, resolved first-match-wins by `determinism.Order`'s `(position, module_id)`, with every conflict logged naming both module ids and carrying nothing else. The three data-level capabilities map one-to-one onto the three shapes a change can have, which makes a module's declared `Scope` the bound on its own body. Registration is the only door a definition enters and it is where `determinism.Scope.Admissible` runs, so there is one admissibility check and not two. `dnd5e`'s pack merge is deliberately not shared: it is last-write-wins by construction, unexported on purpose, and speaks YAML."
lede: "A house rule and a 5e overlay are the same kind of patch, which is exactly why they are not the same function. Sharing the merge would have imported last-write-wins — the behaviour §10.5 forbids by name — into the one place that must not have it, inverted the dependency direction, and made a system with nothing in common with 5e depend on 5e."
weight: 3
date: "2026-10-03"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

§10.5 has one line of pseudocode and two sentences of constraint:

```
system ID → base pack → overlay → enabled house-rule modules (declared order)
         → effective ruleset (+ ruleset_version)
```

**House rules are data-level, not code-level.** A module may toggle `flanking_optional`, change a DC formula constant, or disable a condition; it may not reorder resolution or introduce nondeterminism, because replay and audit both depend on resolution being a pure function of `(state, intent, seed)`. **Application order is explicit and deterministic.** Modules carry a `position`; conflicts resolve first-match-wins and are logged, never last-write-wins.

Everything upstream of this record had already been built, and two of those decisions constrain this one hard.

`determinism.Module.Config` is opaque `json.RawMessage`, deliberately unvalidated beyond being a JSON object, because *which keys a module understands belongs to the module*. [0044]({{ "decisions/0044-determinism-is-enforced-by-lint-because-plugins-are-compiled-in/" | relURL }}) settled that and recorded that the vocabulary is this package's to write. So this package receives a JSON document it must not interpret and a set of compiled-in modules it must not trust on their own say-so.

`determinism.Scope.Admissible` exists and is tested, and is the enforcement point for the first constraint. It is a pure function over a compiled-in value with no configuration, no database and no registry, which is what makes it callable from here without either package importing the other.

The merge seam next door is `dnd5e.mergeFiles`: a collection is merged by slug, a mapping by key, a scalar replaced when declared and inherited when not. It is the right rule for an overlay, it is thoroughly commented, and it is **last-write-wins**: `mergeMapping` copies the base and then copies the overlay over it, and `mergeBySlug` writes the overlay's row at the base's index. §10.5 forbids last-write-wins for house rules *by name*. That is the whole tension, and it is the reason this record exists.

There is also the question of what "an effective ruleset" is, once house rules are in the chain. It is not a pack. §10.4's packs are 5e's, [0045]({{ "decisions/0045-the-5e-engine-is-shared-mechanics-and-a-data-pack/" | relURL }}) is the record for why the 5e engine is shared mechanics over a data pack rather than *the* rules engine, and a system with nothing in common with 5e does not use it at all.

## Decision

**A house rule is a keyed setting.** A module's `Apply` returns `[]houserules.Change`, and a change's identity is the pair `(capability, key)` — `toggle flanking_optional`, `constant dc_passive_exploration`, `disable prone`. Two modules conflict when and only when they name the same pair.

**The three data-level capabilities are the three shapes a change can have, and that correspondence is forced rather than documented.** `CapToggle` carries a boolean, `CapConstant` carries a number, `CapDisable` carries nothing — "disable a condition" is a verb, not an assignment, and allowing it to carry a value would let a disable shadow a toggle, which is reordering resolution reached through the data and exactly what S-10.5 refuses. `TestEveryDataLevelCapabilityHasAChangeShape` runs one change per member of `determinism.DataLevel()`, so a build that classifies a fourth capability as data-level fails that test until it says what such a change carries — the moment that decision is cheap.

**A module's declared `Scope` bounds its body.** A change whose kind is not in the scope is refused at application, under `ErrUndeclaredCapability`. A declaration nobody enforces is a comment, and this is the difference between a `Scope` being a permission and being a description. The scope check runs before the shape check, so a module that returned a `reorder` change is told "it declares toggle, and returned a reorder change" rather than "that is not a shape a change has" — the first names what went wrong.

**There is exactly one admissibility check, and it is `determinism`'s.** `Registry.Register` calls `Scope.Admissible` and refuses a scope that is not data-level. Nothing here classifies capabilities, names a refusal, or second-guesses `Admissible`. Everything below it assumes a registered module's scope is already admissible, and `TestNoRegisterableScopeCanCarryTheTwoNamedRefusals` holds that over the whole vocabulary rather than over two literals — so the bound on a module's body does not rest on this package repeating a list `determinism` owns.

**Registration is the only door a definition enters.** `Registry.Apply` resolves every module through `Lookup`, so there is no second path by which an unchecked definition could reach a campaign. `Definition` is a compiled-in value registered explicitly in the composition root; there is no `init()` and no package-level mutable state (S-10.1, [0011]({{ "decisions/0011-compiled-in-plugins/" | relURL }})).

**Conflicts resolve first-match-wins, and the mechanism is the absence of an assignment.** `Apply` never replaces a recorded setting: the first module to declare one owns it, and a later declaration becomes a `Conflict`. Last-write-wins is the same function with one line added, which is why `TestAConflictResolvesToTheEarlierModuleAndNotTheLaterOne` reads the *resolved value* rather than the conflict list — two modules toggling one setting to opposite values is the case the two policies answer differently, and an assertion that only required "a conflict was recorded" would pass under both.

**The order is `determinism.Order`'s and there is no third order.** The store computes the order in SQL (`ORDER BY position, module_id`) and `determinism.Order` states the same rule in Go; [0044]({{ "decisions/0044-determinism-is-enforced-by-lint-because-plugins-are-compiled-in/" | relURL }}) holds those two together structurally and behaviourally, and this package calls the Go half and sorts nothing. `TestTheApplicationOrderIsTheOneDeterminismStates` runs every permutation of a three-module set — two of them sharing a position, all three declaring the same setting — and requires one answer, so a sort by module id alone, by position alone, by arrival order, or by map iteration each fail it.

**A conflict is one line per losing module, naming both module ids and nothing else.** Three modules declaring one setting is two conflicts, because the second and third each lost to the first and neither lost to the other. The line carries the setting, the winner's id and position, the shadowed module's id and position, and a fixed message. It never reads a module's `Config` — a JSON document a GM's own browser wrote — and never reads a change's value. `TestTheConflictLineNamesBothModulesAndCarriesNoConfiguration` builds both modules so that decoding the note is the only sensible thing their body does, and requires the rendered JSON to contain neither module id missing nor any part of the note.

**The conflict line goes through `log/slog` directly, not through `internal/observability`.** `domain` imports nothing from the project; `conformance` states the same reason for the same omission. Adding a twenty-fifth `EventName` would move a count `observability.AllEventNames` asserts at 24, in a file this work item does not own, and folding a routine configuration conflict into `plugin.version_mismatch` would report "this campaign cannot start its game" for a campaign that starts perfectly well — which is the misclassification ADR 0032 records `index.change_failed` and `content.render_error` as having avoided.

**An enabled row naming a module this build does not have refuses the whole application; a row switched off is ignored.** The first is S-10.6's shape. The direction matters: a skipped row is a campaign playing by different rules than its GM configured, with nothing in the log saying so, and that is the failure this repository cares most about because it is silent and coherent. The second is why `Enabled` exists at all — a module a later release removes cannot strand the campaigns that had it switched off.

**`Effective` is two ordered slices and no map.** `Applied()` and `Conflicts()` return clones, `Lookup` is a linear walk, and there is deliberately no keyed accessor: a map field in `Effective` would put `for k := range` inside `internal/domain/rules`, which is the construct `determinism.Audit` exists to refuse, and a gameplay system wanting a name-keyed index builds it at campaign load from `Applied()` at a seam that is not rule code.

## Consequences

Adding a kind of change is three edits and one forced decision: a capability in `determinism.DataLevel`, a case in `Change.setting`, and a payload check in `Change.validate` — with `TestEveryDataLevelCapabilityHasAChangeShape` failing until the first two are done. That is the point of deriving the shapes from the capability list rather than restating it.

The `disable` capability carrying no value means "disable `prone` and give it this modifier instead" is two changes from two modules, and a reader who wants to know which modules disagreed gets it from `Conflicts()`. If a future system needs a disable to carry a value, that is a new capability and a new record, not a widening of this one.

`Effective` does not expose a map, so every consumer builds its own index. For a gameplay system that is one loop over a handful of settings at campaign load. For a caller that does it per resolution it is a hot path with an O(n) lookup — which is the intended pressure: the layer is applied at load, not per roll, and a resolution that walks it every time is a resolution doing campaign-load work.

A gameplay system consumes this by folding `Applied()` into whatever shape its packs have. `dnd5e.Options` currently takes only an `Overlay`, so **a system-side consumer is a later work item**; nothing in this record requires one, and the conformance suite is unaffected because ADR 0043 gives it no house-rule module of its own.

Fifteen mutations were run against the tests and each named the rule it broke. Two are worth recording because they are about the tests rather than the code. **`builder.absorb` replaced by a no-op** failed thirteen tests including the fingerprint one — which is how the fingerprint test's non-vacuity was established: it asserts *how many* settings each module set applied and *how many* conflicts were recorded before asserting that the version did not move, so a layer that changes nothing fails it. **And `TestTheEffectiveLayerHandsOutCopies` indexed its fixture without checking the fixture's length**, so under the no-op mutation it panicked, which aborts the whole test binary and hides every other test's result. A panicking test is a silent test: it turns one package's failure into an uninterpretable run. The length is now asserted before it is indexed.

`ORDER BY position` alone passing every behavioural test is recorded in ADR 0044, and this package's answer is that it never sorts — it does not have a `sort` to drop. The permutation test is the second half of that answer: it fails if `Apply` starts sorting, and it fails if `Apply` starts trusting its input's order.

## Alternatives considered

**Reuse `dnd5e.mergeFiles`, exported.** Rejected on four independent grounds, and the first is decisive on its own. It is last-write-wins by construction, so getting first-match-wins out of it means not using it for the merge. It is unexported because its own comment says merging is not an operation a caller may perform — "a pack that could be re-merged between two resolutions of one campaign would make resolution a function of more than `(state, intent, seed)`" — and that reason applies to an overlay at load for the same reason it applies to an overlay mid-campaign. It returns no record of what it replaced, so §10.5's "and are logged" would have to be reconstructed by diffing before and after, which is a second merge. And importing `internal/domain/systems/dnd5e` from `internal/domain/rules/houserules` inverts the dependency direction — `rules` is the base `systems` is built on — and would make a house rule require 5e, which §10.3 forbids for any system with nothing in common.

**A house-rule module is an overlay.** Tempting, because the shape is so close: a partial pack, merged over a base, by the same rule. Rejected: an overlay is authored with the pack's vocabulary and validated against it, so `ParseOverlay` refuses a pack that names another system or carries no version, and a compilation step resolves every default once. A house rule is authored by a GM in a form and validated against nothing — its `Config` is opaque JSON this package does not read. Turning one into the other would put a YAML schema and a compiler in the path between "a GM ticked a box" and "a rule changed", and the compiled pack would be rebuilt per campaign rather than once per build.

**The effective ruleset is a flat `map[string]any` of patches.** Rejected: it loses the distinction between a toggle and a condition with the same slug, it makes the resolved value's *type* a property of whoever reads the map rather than of the change, and it would put a map range in a rule package. `Applied()` being an ordered slice of typed changes is the same information without any of the three.

**A conflict is one record naming a winner and a list of losers.** Rejected: §10.5 and ADR 0018 both say "logged with both module IDs", and a record with a list cannot be read as the pair it is. Three modules on one setting is two pairs, and the second and third modules genuinely were in conflict with the winner one at a time — no arrangement of the set makes the third lose to the second.

**Validate a module's `Config` keys here, in a shared vocabulary.** Rejected twice over. ADR 0044 already decided the store does not do it, with the reason that the vocabulary belongs to `houserules`; and `houserules` has no business knowing it either, because a module's config is the module's and a second validator here would be two answers to what a build accepts. `TestTheStoredConfigurationReachesAModuleVerbatim` holds the opacity: the bytes arrive byte-identical, so a "harmless" normalisation fails a test rather than quietly becoming a schema.

**A refusal per module rather than per application.** Rejected, and it is the most tempting simplification available. A half-applied module set resolves a campaign under a partial reading of what its GM configured, and the symptom is a rule that quietly did not apply — the same silent-and-coherent failure `ErrUnknownModule` names. Every refusal here therefore returns an empty `Effective`, and the tests assert that, so a future change that starts returning the partial layer fails rather than shipping.

**Refusing a disabled row that names a module this build lacks.** Rejected: it would make removing a module a backwards-incompatible change to an unrelated feature, because no campaign could ever have it switched off. `Enabled` exists so that "this campaign tried this and turned it off" stays a fact the table can answer, and a row that records it must stay loadable by a build that no longer offers the module.

**A new `observability.EventName` for `houserules.conflict`.** Rejected: it moves a count asserted at 24 in a file this work item does not own, and an event whose only attributes are two plugin identifiers earns its name more slowly than that. If an operator later wants a counter for conflict frequency, the right home is a record in ADR 0032 alongside the four `index.*` signals — with the fifth event name added there, not here.