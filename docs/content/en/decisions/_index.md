---
title: "Decisions"
description: "Architecture decision records: what was chosen, what it forecloses, and what lost."
lede: "An ADR records a decision and its cost. The alternatives section is not padding — it is the part that saves the next person from re-running the argument."
weight: 60
---

Every significant decision gets a record. A decision gets a record when a later reader could
reasonably have made a different choice and where being wrong would be expensive to undo: a new
dependency, a deviation from the
[architecture overview]({{ "design/architecture/" | relURL }}), a change to the data model, URL
scheme, protocol, or plugin authority, anything touching path confinement, sanitisation,
authorisation or secret redaction, and anything that changes the build or the gate.

Records are **immutable and numbered**. A number is never reused and never changed. A decision
that gets reversed does not have its record edited — the record's `status` becomes `superseded`,
`superseded_by` names the replacement, and both remain readable. That is the point: the reason a
decision was not made is usually the reason it should not be remade.

## Two records here correct the design records

The [architecture overview]({{ "design/architecture/" | relURL }}) and the
[UI specification]({{ "design/ui-ux/" | relURL }}) are dated records published verbatim, and
editing them would destroy their value as records. Where they are wrong, the correction lives
here:

- **0016** — the overview's `ETag` rule is a defect. Two body variants cannot share one
  validator without a cache serving a GM's unredacted page to a player.
- **0017** — and the cache invariant that 0016 protects is why a cross-campaign link may be
  *written* but never *inlined*.
- **0018** — the overview's ruleset chain, read literally, would refuse to resume a campaign
  whose GM merely toggled a house rule.

All three are also listed in the
[known staleness]({{ "design/" | relURL }}#known-staleness) note on the design records index.

## The records