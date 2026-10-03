// Package houserules resolves a campaign's enabled house-rule modules into the
// house-rule layer of its effective ruleset — the last two arrows of §10.5's chain.
//
//	system ID → base pack → overlay → enabled house-rule modules (declared order)
//	         → effective ruleset (+ ruleset_version)
//
// # What a module may be
//
// S-10.5 is one sentence with two halves, and the second half is the one with
// teeth: "House rules are **data-level only**. A module that reorders resolution or
// introduces nondeterminism is rejected." A module's claim about what it may do is a
// `determinism.Scope`, and `Scope.Admissible` is what turns that sentence into a
// check — `Registry.Register` calls it and refuses a scope that is not data-level.
//
// **There is no second admissibility check in this package, and that is a decision
// rather than an omission.** `Admissible` is a pure function over a compiled-in
// value, already tested, and already the thing ADR 0044 records as the enforcement
// point; a second gate here would be a second list of capabilities that a later
// build could widen without widening that one, and the wider of the two would
// decide. Everything below assumes `Register` has already refused `CapReorder` and
// `CapUnseededRandomness`, and the tests assert that assumption structurally
// (`TestNoRegisterableScopeCanContainTheTwoNamedRefusals`) rather than trusting it.
//
// What *is* checked here is the other half of the declaration: that each change a
// module returns is a kind of change it declared, and that the change is well
// formed. Those are questions about the module's body rather than about its
// authority, and a compiled-in module is a compiled-in module — ADR 0012 concedes
// that a mistake in one is a bug rather than an attacker.
//
// # A module declares changes; it does not merge packs
//
// `determinism.Module.Config` is opaque `json.RawMessage` and stays opaque here.
// A module's `Apply` decodes its own configuration and returns a `[]Change`, and
// this package never learns the vocabulary of that document. P1d decided that
// deliberately — "which keys a module understands is your vocabulary" — and the
// alternative (validating a config's keys here) would put a second implementation
// of a module's schema in a package that has no business knowing it.
//
// # Conflicts are keyed, and the earlier module wins
//
// Two modules conflict when they declare **the same setting**, and "the same
// setting" is a `(capability, key)` pair. A house rule is therefore data — a
// toggle's name, a constant's number, a condition's slug — and the whole of the
// conflict policy is which value for that pair survives.
//
// The answer is the module that comes first in `determinism.Order`, and the
// mechanism is that `Apply` **never replaces a recorded setting**: the first module
// to declare one owns it and a later module's declaration becomes a `Conflict`.
// Last-write-wins would be the same code with one line changed, which is exactly
// why `TestAConflictResolvesToTheEarlierModuleAndNotTheLaterOne` exists and why it
// asserts the *earlier* module's value surviving — the one case the two policies
// answer differently, and the one a "did it not crash" test never reaches.
//
// Every conflict is logged, naming both module ids. That is §10.5's "logged" and
// ADR 0018's "logged with both module IDs", and the line carries the winning
// module, the shadowed module, both positions and the setting — and nothing else,
// because S-12.3 forbids an event carrying secret content and the only strings
// that can reach it are `rules.ID` values and setting names.
// `TestTheConflictLineNamesBothModulesAndCarriesNoConfiguration` asserts both
// halves on the rendered JSON rather than on the fields.
//
// # The order is `determinism.Order`'s, and only `determinism.Order`'s
//
// `store.RuleModulesForCampaign` computes the order in SQL
// (`ORDER BY position, module_id`) and `determinism.Order` states the same rule in
// Go; P1d holds those two together, structurally and behaviourally. This package
// calls `determinism.Order` and sorts nothing, so a campaign load has exactly one
// answer to "what order are these in" whichever way the rows arrived. A module set
// assembled from a form, from a map, or from a slice a test shuffled applies
// identically, and `TestTheApplicationOrderIsTheOneDeterminismStates` requires that
// over every arrangement rather than over one.
//
// # What a caller does with the result
//
// `Effective` is the answer, and it is deliberately not a `dnd5e.Pack`: this
// package has no idea what a toggle or a condition is, and ADR 0045 is the record
// for why the 5e engine is shared mechanics over a data pack rather than the rules
// engine. A gameplay system folds `Effective.Applied()` into whatever shape its
// packs have, once, at campaign load — and `Effective` does not expose a map,
// because a caller that ranged one would break S-10.4 in the package `determinism`
// audits, and the way to offer it was to build it from `Applied()` at a seam that
// is not rule code.
package houserules
