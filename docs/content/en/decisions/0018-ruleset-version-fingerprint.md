---
title: "0018 — ruleset_version fingerprints semantics, not configuration"
description: "House-rule enablement is deliberately excluded from the fingerprint that gates resume, and drift is recoverable."
lede: "Read literally, the design record's resolution chain would put house rules inside the version that gates resuming a game — so a GM enabling a house rule mid-campaign would be refused on their own campaign."
weight: 180
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

Two problems in §10.5 and §10.8 of the architecture overview, connected but not connected *to
each other*.

**The chain.** §10.5 resolves a campaign's effective ruleset as:

> system ID → base pack → overlay → enabled house-rule modules (declared order) → effective
> ruleset (+ `ruleset_version`)

Read literally, `ruleset_version` includes the enabled house-rule set. But house rules are
**data applied at resolution time**: a module may toggle `flanking_optional`, change a DC formula
constant, or disable a condition. Turning one on changes *outcomes*, not the semantics of how
state is stored or resolved.

So under the literal reading, a GM enabling a house rule mid-campaign changes
`ruleset_version`, and the next resume **refuses to start their own game.** That reads as a bug,
and it would be "fixed" the wrong way.

**The recovery path.** §10.8 says:

> `ruleset_version` differs from persisted state | Refuse to resume rather than silently
> misresolve; prompt the GM to start a new session

There is no session entity. A campaign has at most one live tabletop (§2.1). So "start a new
session" means **discard the current game**. Read literally with no escape, a plugin author
bumping `RulesetVersion()` in a patch release strands every live campaign, with a recovery path
of "restore a backup".

## Decision

### What the fingerprint contains

**System ID + the system's `RulesetVersion()` + base pack and overlay pack versions.**

The enabled house-rule set is **excluded**. It stays stored, ordered by `position`, and
audited — it simply is not part of the identity of the resolution semantics.

Drift then means exactly what §10.8 cares about: *the semantics of resolving an intent changed*,
so persisted mutations may no longer replay correctly.

### What a house rule may and may not do

House rules remain **data-level**, and this is what makes the exclusion safe rather than
convenient:

- May: toggle a flag, change a DC formula constant, disable a condition.
- May not: reorder resolution, or introduce nondeterminism.

A module that tries is rejected, because replay and audit both depend on determinism — which is
the same discipline requirement [0012]({{ "decisions/0012-plugin-authority/" | relURL }}) places
on rule code generally.

Conflicts between modules resolve **first-match-wins by `position`**, logged with both module
IDs. Never last-write-wins, which would make the outcome depend on configuration order.

### Recovery from drift

**Refuse to resume, with an explicit, audited escape.**

- `/c/{slug}/status` names the persisted version, the expected version, and the consequence.
- `/play` shows a GM an alert saying the game cannot resume and why.
- Recovery is a **separate, GM-only, confirmation-gated action** that discards persisted state
  and starts fresh, and it writes to `audit_log`.

This satisfies §16.5's actual concern — *silently misresolving past state is worse than an extra
click* — without the software inflicting the loss. Nothing resolves past state silently; the GM
chooses.

## Consequences

- Enabling, disabling, or reordering house rules mid-game is safe and does not strand a
  campaign. The rules apply from the next resolution.
- A pack or overlay revision **does** gate resume, which is the intended behaviour: it is the
  case where persisted mutations genuinely might not replay.
- A pack revision still requires a GM decision, so the escape path has to work and be
  discoverable. It is the reason it is surfaced on two pages rather than buried in an error.
- **`ruleset_version` must never be hardcoded** by anything that seeds state — including the
  demo seed, which resolves it through the real house-rule path for exactly this reason.

## Alternatives considered

**Include the enabled house-rule set, literally as §10.5 reads.** Maximally conservative: any
change to the effective ruleset forces a re-resolve. Rejected because it makes a routine GM
action — turning on a house rule at the table — into a destructive-looking error, and because it
buys a guarantee that is already provided by the data-level restriction on what house rules may
do.

**Auto-migrate state forward.** Convenient. Rejected because it silently rewrites a persisted
game state, which is the outcome §16.5 names as worse than an extra click, and it would have to
be implemented per-system.

**Warn and let the GM continue into the new semantics.** Nothing lost, and no session
interrupted. Rejected because the persisted state would then mix mutations resolved under two
different semantics, so replay and the audit trail — both of which depend on determinism — stop
being trustworthy. It resolves the usability problem by giving up the property the fingerprint
exists to protect.