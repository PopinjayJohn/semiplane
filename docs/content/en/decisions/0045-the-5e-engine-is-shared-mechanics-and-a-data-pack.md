---
title: "0045 — The 5e engine is shared mechanics and a data pack"
description: "`internal/domain/systems/dnd5e` is one Go engine plus one `go:embed`ed YAML pack. Every edition difference is a row, a formula, a toggle or the name of a Go hook; exactly two rules are hooks, and a test asserts the list is exactly two so a third is a decision rather than an entry in a map."
lede: "§10.4 says 5e 2014 and 5e 2024 are ~90% identical and splits the engine accordingly. That split is only a claim until something says what is allowed to become a third hook, and a hook table that grows silently is a data/engine boundary that erodes one row at a time."
weight: 3
date: "2026-10-02"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

§10.4 settles the shape of a gameplay system in four sentences, and three of them are about
data:

> *"A pack is `go:embed`ed YAML, versioned with the code: stat blocks, conditions, reference
> tables, and the set of `kind`s the system recognises."*
> *"A shared engine plus data packs plus a narrow Go escape hatch is the chosen split, because
> 5e 2014 and 5e 2024 are ~90% identical."*
> *"The engine is therefore shared 5e mechanics, not 'the rules engine'. Systems with nothing
> in common do not use it."*
> *"the hooks are the rules data genuinely cannot express"* — naming two: *weapon mastery
> resolution* and *2024 crit*.

The contract those sentences are written against is
[0041]({{ "decisions/0041-the-rules-contract-returns-mutations-and-carries-its-own-rng/" | relURL }})'s,
and the enforcement is
[0044]({{ "decisions/0044-determinism-is-enforced-by-lint-because-plugins-are-compiled-in/" | relURL }})'s.
What §10.4 does **not** supply is the test that keeps the split a split. A hook table with three
entries is still a shared engine plus a data pack plus an escape hatch, and nothing in the
architecture says otherwise; the boundary erodes one plausible-looking rule at a time, each step
defensible in review, and the end state is a fork that nobody chose. So the record this page
exists for is about **where the line is and what keeps it**, not about what the rules do.

There is a second thing in the same place, and it is smaller. §10.4 says the pack is "versioned
with the code" and is one component of the fingerprint
[0018]({{ "decisions/0018-ruleset-version-fingerprint/" | relURL }}) defines. Four components name
one campaign's resolution semantics: the system, the engine's ruleset version, the base pack's
version and the overlay's. ADR 0018 requires the fingerprint to be **independent of house-rule
enablement**, and this is the first system for which that requirement has four components to be
independent of, so the exclusions have to be stated where the components are built rather than
left to a deployment's encoder.

## Decision

**One engine, one embedded pack, and exactly two hooks.** `data/base.yaml` is `go:embed`ed and
carries all four things §10.4 names: the dice the notation admits, the abilities, the conditions
and what being in one does, the weapon masteries and their effects, the reference tables
(constants, the level-to-bonus bands, the named difficulty classes), four formulas, the attack
toggles, the declared content kinds, and the pack's `hooks:` block naming one rule per hook.
Everything a second edition needs to differ by is a row, a formula, a toggle or a hook name, and
the merge rule is one sentence: **a collection is merged by slug; a mapping is merged by key; a
scalar is replaced when declared and inherited when it is not.**

**A rule became a hook when the thing it computes is a choice among pack rows that the rows
cannot rank between themselves.**

- **Mastery** is a hook because "the highest of these the attacker also holds" is a fold over a
  partial order. Every row is locally correct and none of them is the answer. The *pairings*
  stayed data — each mastery grants a list of named effects — so adding a mastery is a YAML
  edit.
- **Critical** is a hook for a different reason, and the reason is the one worth reading twice:
  it is a hook because **the procedure is shared and only the policy moved**. 2014 crits on a
  natural 20 from the attack die; 2024 also crits when a damage die shows its own maximum. That
  is **one boolean in `toggles` and not two Go rules**. It would have been easy to write two
  rules, and the edition difference would then have been a `switch` in `resolve.go` rather than
  two lines of YAML — which is the failure this record exists to prevent.

`hookIDs` is the whole of "a narrow Go escape hatch", and
`TestEveryHookIsNamedInThePackAndThePackNamesNoOther` asserts it is exactly the two entries
`critical` and `mastery` **and** that every shipped pack names exactly those. A third hook fails
that test, and the failure message is the question this page answers: *a rule became a hook*.
Adding one is a decision about the data/engine boundary, and a decision should be visible in a
diff rather than in a second entry in a map.

**A pack naming a rule this build does not ship is a load failure**, not a skip. A pack naming
`critical: crit-on-anything` against a build with only `default` would otherwise resolve every
attack **without a critical rule** and report success — a game where a natural 20 is an ordinary
hit, discovered by a player months later. That is the one outcome a ruleset fingerprint cannot
cover: the pack loaded, so the fingerprint matched, so the resume was permitted. The same
argument, with the same answer, applies to a weapon granting a mastery the pack does not declare:
which masteries a weapon offers lives on a creature's runtime state, which no load-time check
sees, so that refusal is at resolution rather than at load — and skipping it would resolve the
attack with no mastery and report success.

**The four fingerprint components are four, and only three of them are data.**
`RulesetVersion` is a **constant** and is this engine's resolution *semantics*. The base pack's
version and the overlay's version are separate components so that a pack revision gates a resume
on its own, with its own name in the refusal. House-rule enablement is none of them: a house rule
changes outcomes and not the meaning of a stored mutation, and a fingerprint that hashed the
enabled set would refuse to resume every campaign whose GM enabled a rule at the table — a fault
that reads as a feature, because the error would be perfectly reasonable on its face. Folding the
packs into `RulesetVersion` would strand a campaign **twice** for one change and name nothing.

## Consequences

**A second edition is a YAML file.** P2b's overlays replace `toggles` (the two critical rules and
the incapacity exemption), set `attack.mastery.enabled: false` — which is the whole of 2014's
structural difference — and add rows. The diff a reviewer reads points at the rows that changed,
because the merge is by slug and an index-keyed merge would turn "2024 adds a row" into "every
row after it changed".

**A pack cannot express a house rule that changes what a score means.** ADR 0012's second example
is "change a DC formula constant", and every constant such a rule needs is in `reference.constants`.
The ability modifier's formula is **not**: a house rule that changed it would change what a
*stored* creature's numbers mean, which is exactly the "meaning of a persisted mutation" ADR 0018
exists to keep out of the fingerprint. So it is code, versioned by `EngineVersion`, and unreachable
from a module.

**A fourth kind of thing is not available.** There is no expression in the pack, no predicate, no
trigger. §10.4's split does not have a place for one and this engine does not invent a place; a
rule needing one is the moment this record is revisited rather than the moment a second mechanism
is added beside the first.

**A malformed pack fails at startup, not mid-session.** `rules.System` has no `Check() error`
(ADR 0041's argument), so `New` is a constructor that can fail and every refusal in it names the
row it is about — an unknown hook, a formula naming a variable that is not in scope, an effect
kind outside the closed vocabulary, a proficiency table with a gap, a misspelled column. §10.2's
table puts the blast radius of a broken gameplay plugin at the campaign state, and a pack of zeroes
reaching a resolver is a creature with no abilities and a proficiency bonus of nothing.

**Unknown kinds and stale conditions are inert.** The resolver reads the object an intent addressed
and one the arguments name, and never walks its state, so a tabletop carrying a placement whose kind
nothing registers resolves exactly as it would not have been. A condition slug a pack no longer
declares is **dropped on write**, not refused on read: refusing would make every resolution against
that creature fail, which is §10.8's forbidden failure reached by a different road. Applying such a
slug is a refusal, because that is a claim about the pack's vocabulary.

## Alternatives considered

**Two Go rules for the two editions' critical hit.** Rejected, and it is the alternative this
record most directly refuses. It works, it is a dozen lines, and it puts the difference between
2014 and 2024 in a `switch` in `resolve.go` where an overlay author will not find it. §10.4's
"~90% identical" is an argument for making the difference *data*; a second rule makes it code and
leaves the hook table at one entry while quietly splitting the engine in two.

**A plugin-defined hook mechanism** — a pack naming a method on a Go type by reflection, or an
expression language over the state. Rejected on §12's grounds and on the table's: a pack is compiled
in, but a *house-rule* pack may be authored from something a GM pasted, and a grammar somebody can
extend is a grammar with room in it for something nobody reviewed. The closed vocabulary is
validated at load precisely so that "this effect kind is not one this engine understands" is a
startup failure rather than a mastery that silently grants nothing.

**One `Engine` per edition, with a shared library.** Rejected: it is the fork the split exists to
avoid, and it would make the edition part of the system id — which would name the same input twice
in a four-component fingerprint, because the pack versions already carry it.

**A `codec.go` in this package.** §10.8's codec row says only the code that wrote an encoding can
read it back, and a 5e creature's action economy, attacks and condition set are 5e's facts — so the
projection onto a placement belongs to `internal/plugin` and this package does not reach for it.
The dependency runs inward and the compiler refuses the alternative, which is a fine outcome for a
first attempt.

## What this record does not claim

The engine resolves seven operations: `roll`, `attack`, `heal`, `apply_condition`,
`clear_condition`, `apply_status` (GM-only) and `remove_token` (GM-only). It does **not** resolve
movement, spellcasting, class features, exhaustion levels, flanking, cover, inspiration, or
initiative — four of the switches the shipped pack declares (`inspiration`, `cover_affects_ac`,
`flanking_optional`, and the conditions' `speed_multiplier`) are carried by the pack and read by
nothing in this slice. A pack carrying a switch its engine ignores is harmless; a pack missing one
its engine needs is a rule that never fires, and declaring the whole list is what makes reading one
later a change to this engine rather than a change to every pack.