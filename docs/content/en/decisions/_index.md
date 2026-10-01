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

A fourth was found in phase 3:

- **0027** — the overview's `pages` table lists a `front_matter` column, and the table does not
  have one. The file is the source of truth, so a second copy of a block the parser regenerates in
  a millisecond is a second answer to "what does this page declare".

Three more were found in phase 5. The first is a conflict *within* one record — the UI
specification stating two things that cannot both hold — and the other two are decisions the
record left to the implementation:

- **0033** — §4.3 persists the collapsed navigation in `sp_ui` while §6.6 fixes `sp_ui` to two
  fields and rules the rest out, and §4.1's campaign and mode switchers cannot be
  server-rendered without making the document vary by `sp_ui`. In both cases the later and
  more specific rule governs, and both resolutions are recorded rather than picked in a
  template.
- **0034** — TV is a `[data-ui]` mode and never a width band, because a 55-inch television and a
  12.9-inch tablet in landscape are indistinguishable to CSS and need opposite layouts.
  The record's own accepted failure mode — an unrecognised TV browser shows the laptop
  layout until someone presses the switcher once — is kept rather than fixed, and the
  user-agent allowlist stays narrow.
- **0035** — no `Vary` header is emitted at all, and the document's independence from
  `sp_ui` is held by asserting the *bytes* are identical rather than by asserting the
  header's absence. The one blocking inline resolver is under a byte budget the build
  enforces.

## The records