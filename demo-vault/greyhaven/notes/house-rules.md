---
title: House rules
---

## Playing by something the rules do not say

Every table plays by something the rules do not say. Semiplane's answer is that a
house rule is **data**, keyed and first-match-wins, applied at resolution time — not a
patch, not a fork, and not a build.

## What one looks like

A house-rule **module** declares the settings it may change and a priority that says
where it sits among the others. A campaign's module set is a row per module: which
ones are on, in what order, with what configuration. Turning a rule on is a row.

The resolution rules are three, and they are all there to make the answer
explainable rather than merely correct:

- **A row naming a module this build has not registered is a refusal, not a skip.**
  Skipping it would be a campaign quietly playing by rules its Game Master did not
  choose, and the symptom would be a rule that mysteriously does not apply.
- **First module wins.** Where two enabled modules change the same setting, the one
  that comes first in the declared order decides, and the log line names the module id
  *and* its position — because "flanking is optional because module `x` said so at
  position 0" is the answer a Game Master needs, and a bare `true` cannot say which of
  three modules to switch off.
- **A refusal is total.** If any module in the set cannot be applied, the campaign load
  produces nothing rather than applying the half that worked. A partially applied
  house-rule set is a campaign playing a game nobody chose.

## Why it is not in the fingerprint

The subtle part, and the one worth getting right: house rules are **deliberately
absent** from the `ruleset_version` that fingerprints a campaign's resolution
semantics.

The fingerprint names *what the rules mean* — the system id, the system's own ruleset
version, and the versions of the base data pack and its edition overlays. House-rule
*enablement* is configuration, not semantics.

The alternative strands campaigns. If the enabled set were part of the identity, a
Game Master switching a rule on mid-evening would change the fingerprint, and the
resume gate would refuse to load their own live game with a message about a version
mismatch. So the set is stored, ordered and audited, and it is simply not part of the
identity. Switching a rule on does not restart anything; a pack *revision* does, and
that one is deliberate.

## Greyhaven has none enabled

Which is worth stating rather than leaving to be discovered: this campaign's module
set is **empty**, so every roll in it resolves with no house-rule layer applied. The
modules a build ships are declared in the composition root, and this build declares
none — so a demo cannot show a rule being switched on, and pretending otherwise would
mean either shipping a module nobody asked for or writing a page that describes a
control which does not exist.

What it does show is the half that is easy to get wrong: **an empty set is a working
campaign.** The registry is consulted on every campaign load rather than only when
somebody has configured something, precisely so "nobody checked" cannot be
indistinguishable from "there is nothing to apply". A missing registry is an error; an
empty set is not.

## Where a rule can and cannot reach

A house rule changes a **setting** the resolution chain already exposes — armour
class, damage bonuses, save DCs, critical handling, sizes, flanking, cover. What it
cannot do is add a mechanic, because a mechanic is code and code does not come from a
row.

That is the trade the design makes: a table can agree that flanking is optional
without patching the engine, and cannot agree that grappling requires a grapple action
without writing a module. Both halves are limits. The one that bites a table hardest
is the second, and it is the honest cost of a rule system that runs in a binary a
Game Master cannot recompile.

Back to [[index]].