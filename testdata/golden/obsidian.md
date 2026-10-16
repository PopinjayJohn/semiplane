<!-- testdata/golden/obsidian.md — Obsidian-flavored Markdown fixture for the
     Markdown→HTML pipeline (Phase 1, stream A: internal/markdown + render).

     Covers: YAML frontmatter, [[wikilink]] (plain + aliased), :::gm secret
     block, ```ad-statblock fence, > [!note] callout, and a table.

     NOTE: GM-vs-Player HTML divergence assertions (gm block present for GM,
     server-side stripped for players) land in Phase 1/A as golden files
     (*.gm.html / *.player.html) plus `make golden` TestGolden coverage.
     This file is the shared input only; do not assert rendered output here. -->

---
title: The Yonder Vault
aliases: [yonder, misty-ruin]
tags: [location, dungeon]
---

# The Yonder Vault

A misty ruin a day south of the [[tavern]]. The barkeep at the
[[tavern|Rusty Flagon]] swears the lights below are just marsh gas.
See also [[hidden-lore]].

> [!note] Delver's warning
> Bring rope. Count your party twice — once at the door, once below.

| Level | Hazard       | Light  |
|-------|--------------|--------|
| Upper | Cellar rats  | Torch  |
| Lower | Falling rock | Lantern |

```ad-statblock
name: Cellar Rat
size: Small
hp: 4
```

:::gm
The vault door was built from the inside. The barkeep feeds the rats.
Reveal only when a player opens the sealed orders.
:::
