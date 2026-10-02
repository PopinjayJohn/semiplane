---
title: "0043 — The conformance suite is the only shared code a foreign system needs"
description: "`internal/domain/rules/conformance` is a published suite every `rules.System` must pass, taking a `rules.System` and four explicit seams and nothing else; `internal/domain/systems/notfive` is a complete system sharing nothing with 5e, and three AST audits make that claim falsifiable rather than documented."
lede: "A suite that has only ever run against a 5e engine proves nothing about generalisation, because a 5e engine is the one system for which it can have been written around shared helpers by accident. So the suite's API has no fifth seam, the counterexample is a system with no d20 at all, and the claim that it shares nothing is an import audit that fails on the first 5e import rather than a sentence in a doc comment."
weight: 3
date: "2026-10-02"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

§10.4 makes the generalisation claim in one sentence — *"a plugin sharing nothing
with 5e — say Pathfinder — ships one complete standalone pack and its own resolver
hooks, and implements `System` without touching `internal/domain/rules`, the engine, or
semiplane itself. The only shared code is the interface, the intent and mutation types,
and the shared conformance suite (§14)"* — and S-14.9 turns it into the requirement that
decides whether phase 8 generalised or merely grew:

> **A system sharing nothing with 5e passes the conformance suite without touching
> `internal/domain/rules`. This is the test that proves the plugin system generalises.**

The half of that sentence which is easy is the second one. A suite that takes a
`rules.System` and asserts five properties is straightforward to write, and it is the kind
of code that reads as generalisation while proving nothing: run it against a 5e engine
once and it is green, and a reviewer has no way to tell whether the suite is general or
merely familiar. The two ways it goes wrong are both invisible. A suite can hold helpers
the 5e engine happens to use — a `Context` accessor nobody else can, a helper in
`rules` that only makes sense for d20 — and a system that shares nothing with 5e will not
be able to call them, so the suite would not even be *runnable* on the case it was written
for. Or a suite can be right in general and wrong in detail, catching the five properties
in the shapes the first system happened to have.

Both are the failure this repository has already paid for three times, in
`make a11y`: the wiki route held 23 tests and the assets route 49, not one of them a
§10.2 audit, and the gate was green because `A11Y_PKGS` named three packages and
`go test -run` exits 0 when the pattern matches nothing. The lesson recorded in
`AGENTS.md` is not "write the tests". It is that **a test's name is what makes the gate
find it**, and that renaming one to match a pattern is not the fix — so this work item's
first question was not what to assert but how an assertion that *cannot fail* would be
told apart from one that can.

Three constraints shape what follows, and two of them come from
[0041]({{ "decisions/0041-the-rules-contract-returns-mutations-and-carries-its-own-rng/" | relURL }}).

**The contract's shape is settled and this work item cannot change it.** `Apply` returns
`[]Mutation` and takes the state by value; `Context` is four fields; `Validate` is a
function. The suite is typed against that shape and adds nothing to it, which is what
makes it possible to write without touching `internal/domain/rules`.

**Four of §14's five properties cannot be observed from a `rules.System` alone**, and this
is the constraint the design turns on. Determinism, panic containment and unknown-`kind`
tolerance are observable — the suite resolves an intent and compares. §7.2's GM-only rule
is not: *which* operations are GM-only is a system's own answer, and the contract has no
opinion (`rules.Op` is a string precisely so §10.2's "a gameplay plugin defines the
operation vocabulary" is true). And §10.8's `ruleset_version` row is not observable at all:
whether a campaign may resume is a *deployment's* decision, because the fingerprint
encoding is `realtime`'s and the column is `campaigns.ruleset_version`, and a campaign-level
rule about it belongs in
[0018]({{ "decisions/0018-ruleset-version-fingerprint/" | relURL }}) rather than in a rule
resolution.

**The wire vocabulary is `realtime`'s and `domain` cannot reach it.** `internal/domain`
imports nothing from the project and `realtime` owns a database. §7.2's rule is not "refuse
a player" — it is "refuse a player in a way the client can be told", and the word is
`not_permitted`, which `realtime.Core` orders ahead of `unknown_op` deliberately because the
second answers "is anything allowed" about the server rather than "is this allowed" about
the player, and on the wire that ordering is a role oracle anybody on the campaign can read.
A suite that cannot name a wire word cannot check that ordering.

## Decision

**`internal/domain/rules/conformance` is the suite, and its API is a `rules.System` plus
four seams. `Config` takes those four and nothing else.**

| Seam | What it is | Why the suite cannot do it |
|---|---|---|
| `Scenario` | one typical resolution: the tabletop, the intent, the rule context | a system's `Args` are its own encoding; the suite would have to know it |
| `GMOnly` | the ops §7.2 reserves to the GM, **required non-empty** | §10.2 makes the vocabulary the system's answer, not the contract's |
| `Classify` | the composition root's error → wire-word map | `realtime.RejectReason` is eight words on a wire this package cannot import |
| `Resume` | `Fingerprint(system, house)` and `Check(system, house, persisted)` | the encoding is `realtime`'s and the column is a store column |

The set is **closed at four, and that is the whole design.** A fifth seam is what a suite
grows when it starts guessing instead of asking, and every guess is a rule a foreign system
cannot act on. The suite's stance is the opposite: it asserts only what the interface can
promise and takes everything else as an argument. A knob that can be left at its zero value
is a knob that will be, and a suite that passes on defaults is a suite nobody has
configured.

**A suite that could find nothing is refused rather than run.** `New` returns
`ErrNoGMOnlyOps` for an empty `GMOnly`, `ErrNoScenario` for a tabletop with no content or
an intent the wire would never have produced, `ErrNoClassify` and `ErrNoResume` for the
missing seams, and `ErrGMOnlyScenario` when the scenario's own operation is also declared
GM-only — which would make the role audit require one resolution to be both refused and
allowed. All of them satisfy the `ErrIncompleteSuite` umbrella, so an author wiring a suite
asks one question. `Runs` is the exception and it is **a constant, not a field**: S-14.6
names one hundred, a `Config` field would be one an author sets to two to make a flaky test
pass, and the failure that hides is the one the audit exists for.

**Two of the five audits assert a property of the *configuration* rather than of the
system, and say so.** The determinism audit refuses to certify a scenario that produces no
mutation, because a resolution that changes nothing is reproducible whether or not the
resolver is deterministic. The role audit builds its probe intents **with no arguments**
and checks the GM run for *not* saying `not_permitted` rather than for succeeding, because
the suite cannot build a valid intent for an op whose parameters only that system
understands — demanding success would be demanding a second fixture per GM-only op, and
demanding that the role is what decides is the claim itself.

**The containment boundary is production code that lives here, and the audit that certifies
it induces the panic itself.** `Contain` is the `recover()` §10.8's table requires, and the
composition root's adapter should call it rather than write a sixth copy. It is in the
suite because the only system that will panic on request is one the suite broke itself, and
a boundary whose behaviour is only ever exercised by its author's good behaviour is a
boundary that has never been tested: `recover` would be a line nothing ever reached, and
deleting it would change no test's outcome. The probe is the system under certification
with `Apply` replaced, which is also the most honest fixture available — a plugin shipping a
data pack it did not validate is exactly that, with everything else correct. The audit
policies its own fixture: a probe that stopped panicking would produce a finding rather
than a pass.

**The two refusals of §10.8's table are told apart by interchangeability, and the check
includes the root.** A handler routes a refusal with `errors.Is` and nothing finer, so two
refusals a handler cannot separate are two refusals a GM cannot be told apart, whatever
their messages say. `errors.Is` in one direction is not enough, because the drift error here
is the whole message-bearing error rather than a sentinel, and a deployment wrapping one
sentinel in two different sentences would pass a comparison no handler could make. So the
deepest error each wraps is compared as well: `realtime` answers with a `*DriftError`,
which has an `Is` and no `Unwrap`, against something wrapping `ErrRulesetUnreadable`, and
those are different objects; a deployment answering both with `ErrRulesetDrift` has the same
root twice.

**The wire's eight words are copied, and the audit claims membership rather than
equality.** The words are restated in the suite because `domain` imports nothing from the
project and `realtime` owns a database — and the claim is deliberately weaker than a mirror
would be: every refusal has to map into a word this package *knows*, not that the word is
one `realtime` has. A plugin whose errors map to `not_permitted`, `invalid_args` or
`no_such_placement` passes even after a ninth word is added upstream, because the adapter the
author already wrote does the mapping. What fails is an error that becomes its own sentence,
which no client can be told and which quotes whatever the system choked on. A suite asserting
set equality would fail every plugin's certification for a reason no plugin could act on;
that asymmetry is the design.

**`internal/domain/systems/notfive` is the counterexample, and it is a loom.** Spools of
thread, `spin` / `weave` / `dye` with `unwind` and `recut` reserved to the GM, a `2x12`
notation, content kinds `spool` / `bolt` / `dye_lot`, and a payload of two structs. No d20,
no ability scores, no conditions, no data pack, no engine. It **draws** — every operation
takes its number from `call.Rand` under a label derived from the operation and the target —
because a system with no randomness would pass S-14.6 trivially and the determinism audit
over it would prove nothing. Its only draw is an *index into a declared palette*, which is
§10.3's "the protocol never assumes d20" demonstrated rather than asserted.

**S-14.9 is falsifiable in three separate ways, because they fail differently.**

1. **The import set is exact and closed**, every entry with the reason it is permitted
   rather than merely allowed. Closed because an allowlist would still admit a 5e package
   the day one exists; a closed set admits nothing until somebody edits the table, and the
   edit is a review comment about a counterexample to S-14.9. The audit is over **parsed**
   imports, not the file's text, because `math/rand/v2` and `math/rand` are one character
   apart. It runs over the whole package's non-test files, because a dependency split across
   two files is one dependency and a per-file audit certifies whichever file is short. It also
   objects to a *permitted* import the file never uses, because the table is a claim about
   what this package needs.
2. **Every `rules.X` selector is on an allowlist** of what implementing nine methods
   requires. Imports are a coarse instrument: importing something and not using it satisfies
   them, and what a shared assumption looks like is a selector. The base of a selector chain
   is resolved, so a method expression such as `rules.Context.Rand` — which parses as a
   selector whose `X` is another selector — is seen rather than walked past.
3. **No identifier and no string literal carries 5e's vocabulary.** Fragments, matched
   case-insensitively with separators removed, because the vocabulary's whole problem is that
   it arrives in every spelling a Go identifier can take. **Comments are excluded, and the
   exclusion is a decision with a test in both directions** — the package's comments are full
   of "not a d20 system", and a comment is documentation rather than vocabulary in use.

## Consequences

A system author outside this repository writes one test, supplies four seams, and is
certified. `notfive`'s test is the worked example, and it is the file to copy.

Every audit is reachable one at a time — `Suite.Determinism`, `Suite.UnknownKind`,
`Suite.Version`, `Suite.Role`, `Suite.Containment` — which is what an author uses to pin one
in a focused test and what the suite's own meta-test uses to prove each can fail. That
meta-test requires **both** halves per row: the broken configuration produces a finding *and
every finding it produces is that audit's*, and the intact configuration produces none. The
second half is the one that does the work; without it, every row would pass just as happily
against a suite that rejected everything.

**Three of this item's tests did not fail when the thing they guard was broken, and each
produced a change.** Mutation verification is not a formality here; it found the argument for
three of the five audits' teeth, and the reasons are the reasons this repository keeps
relearning. Reducing `Runs` to two survived, because the fixture — a resolver carrying its
own `*rand.Rand` — diverges on the *second* call, so an audit comparing two runs would catch
it; the fixture that makes the number executable is one that agrees for two resolutions and
diverges on the third. Removing the role audit's requirement that a player's refusal map to
`not_permitted` survived, because the fixtures broke the role in the two directions that
produce *no* refusal at all and none that produce the *wrong word*; the oracle case needed
its own fixture. And removing the containment boundary's discard of a failed resolution's
mutations survived, because the branch was **dead code**: `mutations` is only ever assigned
by the one statement at the bottom of `Resolve`, so a panic cannot leave a partial list in
it and an error was already answered with `nil`. It is deleted rather than kept as a
belt-and-braces a reader cannot point at, and the two paths that *can* be broken each have a
test that turns red when they are.

The state-identity assertion is a **guard on another package**, and it is the one place this
work item verifies a mutation in code it does not own. `Apply` returns its changes, the state
arrives by value and `rules.State` keeps its fields unexported, so a resolver that writes to
every object it reads corrupts nothing — which is S-10.2, holding because of a *type*. The
cost of a type-level guarantee is that an edit to the type can undo it with nothing here
noticing, so `TestAScribblingResolverChangesNothingItWasHanded` exists and was verified by
neutering `cloneObject` in `internal/domain/rules/state.go`: the test goes red and nothing
else does. That mutation is applied, run and reverted; `rules` is not this item's file.

**There is a second boundary, and it disagrees with this one about the panic's value.**
P1b's `internal/plugin` landed a `recover()` of its own in `settle`, and it is the right
one to be there — it is wider than `Contain` (it also contains a codec panic) and it records
the recovered value's **type** rather than the value, deliberately, on S-12.3's reasoning that
a panic value is whatever a plugin panicked with. `Contain` keeps the value and prints it, for
the other half of that reasoning: a panic value is a string or an error about a plugin's own
code path, not an answer to a roll. Two boundaries with two policies is a hazard, and the two
are not interchangeable, so the honest statement is narrower than "the adapter should call
`Contain`": `plugin.settle` is the product's boundary and stays; `Contain` is the one for
anywhere a system is resolved **without** the adapter — a conformance run, a UI-tier
dispatch, a future second adapter — and it is here because it is tested on every run. Which
of the two panic policies wins is a phase decision, not this item's, and this record does not
pretend to settle it.

**`Classify` is the seam the registry has to fill, and P1b's `wireReason` is half of it.**
`internal/plugin`'s adapter deliberately holds no operation table — its own comment says a
list of GM-only ops there "would be a second answer to a question `realtime.Core` already had
to fake" — so a plugin's GM-only set is the plugin's declaration and the suite's `GMOnly` is
where it goes. `wireReason` already maps the contract's refusals onto the eight words, and
falls through to `server_error` for anything it does not recognise: which is exactly right for
an unrecognised failure and exactly wrong for a plugin's own `not_permitted`, so the
composition root has to extend it per plugin. That is a one-line switch, it is the only place
in the product that holds both a `rules.System` and a `realtime.RejectReason`, and it is the
reason `Classify` is a required seam rather than a convenience.

**A finding never quotes a mutation's payload.** The determinism audit compares answers as a
digest and prints op, target and payload *length*, so two runs differing only in payload
bytes are distinguishable without a resolved roll reaching a CI log — S-12.3 is about the
second and the first is what makes the comparison exact. Nothing here is an observability
event: `observability.EventAttributes` has no field a `Finding` could be passed through,
which is the structural half of the rule and the reason this package needs no import of
`internal/observability` to keep it.

Two things are deliberately not here. `rules.Validate` already checks what the contract can
check, so the suite does not repeat it, and `notfive`'s test asserts `Validate` accepts the
loom separately. And the suite has **no house-rule module of its own**: ADR 0018's exclusion
is asserted by toggling the same module twice through `Resume`, which is the shape ADR 0018
already settled and the reason the fingerprint's exclusion belongs in `realtime`'s
`FingerprintOf` rather than here.

## Alternatives considered

**Let a system pass the suite by calling helpers in `internal/domain/rules`.** Rejected: it
is the failure, not the alternative. A helper the suite needs is a helper every system
needs, so it belongs on the interface; a helper only 5e needs belongs in the 5e package.

**Publish the suite as a Go module a plugin imports.** Rejected, and worth recording because
it is the obvious shape: S-10.1 makes plugins **compiled-in** modules registered explicitly
at the composition root, and a published module would be runtime third-party code of exactly
the kind §10.1 rules out. The suite is a package in this repository and the "published" in
§14's sentence means *written down and documented for an author outside it*, which is what
the package comment and `notfive`'s test are.

**Have the suite infer the GM-only operations from the vocabulary.** Rejected: there is no
vocabulary to read. A `System` declares the ops it resolves nowhere in the interface, and
guessing which are reserved would make the audit test the suite's guess.

**Assert set equality against `realtime`'s `RejectReason` values.** Rejected, and the
asymmetry is the argument: two copies drifting apart is a failure the suite cannot see, and
a suite that could would fail every plugin's certification when a ninth word was added, for
a reason no plugin could act on. Membership is the property an adapter can actually rely on.

**Have the suite check `rules.Validate` too, so one call certifies everything.** Rejected:
`Validate` is the registry's call at startup, it is about the *contract* rather than the
*system's behaviour*, and folding it into the suite would make "the suite passed" mean two
things — one of which a registry already guarantees.

**Put the suite outside `internal/`, so an outside author could import it.** Rejected: the
suite takes `rules.System`, which is an `internal/` type. An exported copy would be a second
contract that could drift from the first, which is the "second answer to a question" fault
`AGENTS.md` is explicit about.

**Let the unknown-`kind` audit require `Derive` to tolerate it too.** Rejected as
over-specification: §14's separate row covers view rendering and the renderer is semiplane's,
and the loom's `Derive` happens to skip an unreadable row because a list view gets that for
free rather than because the suite asked.