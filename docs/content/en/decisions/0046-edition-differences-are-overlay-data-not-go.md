---
title: "0046 — Edition differences are overlay data, not Go"
description: "`internal/domain/systems/dnd5e/overlays` ships 2014 and 2024 as two `go:embed`ed YAML overlays over the engine's base pack. All four edition differences are rows, switches or a label; no third hook was added, and the diff between the two files is machine-checked so it stays reviewable."
lede: "§10.4 promises that 2014 and 2024 differ as reviewable data diffs. Two decisions make that promise measurable rather than aspirational: each overlay states its own answer for every switch its edition has an opinion about, and a column-level difference on an inherited row is paid for as a whole row because the merge cannot yet do better."
weight: 3
date: "2026-10-02"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

[0045]({{ "decisions/0045-the-5e-engine-is-shared-mechanics-and-a-data-pack/" | relURL }}) settled
what a 5e pack is and drew the line between data and a Go hook. Phase 8's second half had to
spend that line, and the architecture record states the bill:

> *base pack (shared 5e data)*
> *├── overlay: 2014 — race naming, crit on 20 only, no mastery properties*
> *└── overlay: 2024 — species, mastery properties, crit on any natural 20*
>
> *"The differences show up as reviewable data diffs. Only genuinely procedural rules — weapon
> mastery resolution, 2024 crit — become Go hooks."*

Two things make that harder to do than it reads. The first is that **§10.4's three differences
are not the engine's three**: `dnd5e`'s own `hooks.go` describes a fourth, the 2024 rule that an
attack against an incapable, paralysed or unconscious creature does not automatically count as a
critical hit. A work item that transcribed the architecture list would have shipped an overlay
that silently dropped it — a rule that quietly does nothing, which is the failure mode this whole
split is built to avoid and the one a ruleset fingerprint cannot detect.

The second is that **"reviewable" is a property of a diff, not of a file**. §10.4's merge rule —
a collection is merged by slug, a mapping by key, a scalar is replaced when declared and inherited
when it is not — makes a row-level change readable. It says nothing about what happens when two
editions want to differ on *part of a row*, and the engine's answer is that a restated row is
replaced whole. That is a real cost, in lines of duplicated text, and it had to be paid
deliberately rather than discovered.

## Decision

**Four differences, all data, and no third hook.**

| Difference | 2014 | 2024 | Expressed as |
| --- | --- | --- | --- |
| Critical hit | a natural 20 from the attack die | and a damage die at its own maximum | `toggles.crit_damage_die_max` |
| Attack against a helpless target | always crits on a 20 | not automatically a critical hit | `toggles.crit_ignored_by_incapacitated` plus `critical_exempt` on three condition rows |
| Weapon mastery properties | do not exist | exist | `attack.mastery.enabled` |
| What an ancestry is called | "Race" | "Species" | the `ancestry` kind's `label` |

Nothing needed a hook, and that is the finding rather than the coincidence: every one of the four
is a **policy the shared procedure already reads**. The critical rule takes three booleans the
pack sets, so the editions differ by two of its inputs and not by its shape; the mastery rule folds
two lists and 2014 declines to call it. A hook is for a rule whose *question* differs between
editions, and none of these four asks a different question. `dnd5e`'s
`TestEveryHookIsNamedInThePackAndThePackNamesNoOther` holds the count at two and this work item
adds nothing to it.

**Each overlay states its own answer for every switch its edition has an opinion about — including
the one both editions agree on.** So both files carry `crit_attack_die_max: true`.

This is the decision worth arguing, because it costs four lines and the obvious alternative is to
write none of them. The base pack's value for `crit_damage_die_max` and for
`crit_ignored_by_incapacitated` happens to be **2014's** answer. An overlay that relied on it would
make the base pack's *default* into an edition's *rule*: change either line in
`dnd5e/data/base.yaml` for an unrelated reason and 2014 silently becomes 2024's critical rule, with
the diff between the two overlay files showing nothing at all. And a reviewer reading the two files
side by side could not see the difference without also reading a third. The convention is one
`toggles:` block per file that is the complete statement of that edition's critical policy.

**A column-level difference on an inherited row costs a whole row, and the bill was paid rather
than dodged.** `critical_exempt` is a column, and 2024's exemption needs it on three rows the
overlay did not otherwise touch. `mergeBySlug` replaces a row whole, so those three rows are
restated in full — nineteen lines of text that is a copy of `dnd5e/data/base.yaml`'s.

The alternative was leaving 2024's exemption unimplemented, and it was rejected outright: an
overlay that silently does not apply is the failure a fingerprint cannot detect, because the
overlay loaded, its version was recorded, and the campaign resolved under 2014's rule while
claiming 2024's. A duplicated paragraph that is *tested* against the original is a smaller problem
than a missing rule. The duplication is held honest by
`TestTheTwentyTwentyfourConditionRowsAreTheBaseRowsPlusTheExemption`, which requires every field of
every restated row to equal the base pack's own except two named exceptions — so a typo fixed in the
base pack fails a test here rather than forking 2024's copy of that condition forever.

**The exemption covers three conditions and the engine's prose names two.** 2024 exempts an
*Incapacitated*, *Paralyzed* or *Unconscious* target — which is what the switch is called:
`crit_ignored_by_incapacitated`. `dnd5e`'s `hooks.go` and `packfile.go` both describe it as
"paralysed or unconscious". The column is the pack's to fill, so the pack fills it with the rule and
the engine's comment is narrower than the rule it describes. Reported rather than worked around.

**One slug, two labels.** `ancestry` names the kind in both editions and only its `label` moves. A
slug is the identity a campaign's pages and its `campaigns` row hold; two slugs for one concept
would be two entries in a kind registry and two answers to "which kind is this page", which is a
data-model change 0045 already decided against for system ids.

**The three refusals a caller can reach.** An id no embedded file answers is
`ErrUnknownEdition` — a configuration fault, and deliberately **not** under `dnd5e.ErrPack`,
because a composition root asking "did the packs load?" should not be answered about a pack that
was never read. The **zero `Edition` refuses** to build a system, because `dnd5e.Overlay.Zero()`
means something real and correct — an empty overlay *is* the base pack alone, and §10.4 makes that a
first-class system — while a zero `Edition` is a value nobody filled in. Building the base pack from
it would produce a campaign whose fingerprint has an empty `overlay` component and no way to tell
that is what happened. And `Source` hands out a **copy**, because `go:embed`'s value is package
state and a caller that appended to it would corrupt every edition not yet parsed.

## Consequences

**The reviewable diff is one command, and it is machine-checked.**
`diff data/dnd5e-2014.yaml data/dnd5e-2024.yaml` puts every edition difference on the screen and
nothing else.
`TestTheOnlyDifferencesBetweenTheTwoOverlayFilesAreTheEditionRules` flattens both files and compares
four computed sets against closed lists: what differs, what agrees, what only 2014 declares (nothing)
and what only 2024 declares (the three condition rows). Because the *agreement* set is closed, an
overlay that also restated the proficiency table with the same values fails — so "the difference
between these files is the whole of the difference between these editions" is an assertion and not a
reviewer's diligence.

**Both editions pass all five conformance audits.** `TestBothEditionsPassEveryConformanceAudit` runs
`conformance.Check` per edition, through the suite's `Resume` and `Classify` seams with an encoding
that mirrors `internal/realtime`'s. An edition that failed one would be a *system* that failed, not a
fixture that failed: the pack is where a system keeps its rules, so a conformance failure under one
edition and not the other is the exact shape of a data difference the engine cannot honour.

**An edition's identity is a fingerprint component and a house rule is not.**
`RulesetVersion` stays the engine's constant under both editions; the overlay's declared version is
the third of the four components, the base pack's own is the second, and
`TestTheFingerprintCarriesTheEditionAndNotAHouseRule` asserts all five claims including that the same
house rule, enabled and disabled, produces byte-identical fingerprints and the same campaign still
resumes. That is [0018]({{ "decisions/0018-ruleset-version-fingerprint/" | relURL }}) as bytes, and
it is the claim an overlay could plausibly break by fingerprinting an edition's *contents* rather
than its declared version.

**`mergeBySlug` is now the weakest layer in this split, and the fix is small.** Replacing a row whole
is right for every other collection — a restated ability should be the ability the overlay means —
and wrong for a column. The narrow fix is a field-wise merge for `conditions`, with pointer fields
on the row so absence is visible, which is the same reason `packFile` has pointers at all. That is an
edit to `dnd5e`, which this work item does not own, so it is reported here rather than made.

**An engine defect was found and is not fixed here.** `defaultCritical.Critical` short-circuits on
the defender's exemption *before* it consults the attack die, and `resolveAttack` asks the same hook
twice — once for "does a natural 20 hit on its own" and once for "is the hit critical". Under 2024
that conflates the two: measured against a defender with `ac: 30` carrying `unconscious`, a natural
20 with a total of 21 **misses** (`met: false`, one mutation), where 2014 and 2024 against an
unexempted defender resolve the identical roll as an automatic critical hit. In 5e a natural 20 is
always a hit; 2024 only denies that it *automatically counts as a critical hit*. The fix belongs to
the hook's input or to the resolver's two questions, and it is the engine's to make.

**A fifth edition difference needs a new record line.** The two lists in the test file are the whole
of what these two editions disagree about. A sixth difference — a new edition, a house edition, a
reflavoured pack — fails those lists rather than arriving quietly, which is the intended friction.

## Alternatives considered

**Two Go rules for the two editions' critical hit.** Rejected, and 0045 already refuses it. It works,
it is a dozen lines, and it puts the difference between the editions in a `switch` in `resolve.go`
where an overlay author will not find it. The critical difference here was measurable rather than
asserted: `TestTheEditionsResolveDifferentlyUnderOneSeed` resolves the *same intent under the same
seed* under both editions and requires different damage — three points of modifier for a finesse
weapon, exactly one d12 for a damage die at its maximum, and a critical that 2024 suppresses and
2014 does not. A flag that was merely set cannot produce a damage number twelve larger.

**Rely on the base pack's defaults and declare only what the base got "wrong" for this edition.**
Rejected, and the argument is in the Decision above: it makes the base pack's default into the
edition's rule, and it puts the difference in a third file. The cost is four lines per overlay.

**Restate the whole pack per edition.** Rejected: it is the fork §10.4 exists to avoid, and it makes
the two editions comparable only by a human reading two hundred lines to find four differences.
`TestAnOverlayRestatesNothingThatTheBasePackAlreadySays` asserts each overlay's *shape* — which
blocks it declares and how many rows each holds — so a restated `formulas:` block fails rather than
shipping.

**Leave 2024's exemption unimplemented to avoid the row copy.** Rejected above, and the asymmetry is
worth stating plainly: a wrong number in a duplicated paragraph is visible in a diff and testable, and
a missing rule is invisible until somebody at the table notices they cannot crit an unconscious
goblin.

**A different `kind` slug per edition — `race` and `species`.** Rejected. It is a data-model change
with a persisted consequence: the slug is what a campaign's page front matter and `campaigns` row
name, so two editions would want two entries in one registry and a GM moving a campaign between
editions would have to rewrite every page. The word a GM reads is a `label`; the word a page stores
is a slug; only one of them is an edition difference.

**A field-wise merge for rows, done here.** Rejected on ownership, not on merit. `dnd5e/overlay.go`
and `packfile.go` are the merged engine's, and this work item's instruction was explicit: report an
engine bug, do not fix it. The recommendation is in the Consequences.