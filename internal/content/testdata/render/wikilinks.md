---
title: Every shape a wikilink can take
kind: index
---

# Wikilinks

The plain form: [[The Lighthouse]]

With a path: [[notes/The Lighthouse]]

With the extension Obsidian allows: [[notes/The Lighthouse.md]]

From the vault root: [[/The Lighthouse]]

With an alias: [[The Lighthouse|the lighthouse itself]]

With a heading anchor: [[The Lighthouse#The Beam]]

With a block anchor: [[The Lighthouse#^beam-1815]]

With both: [[The Lighthouse#The Beam|read about the beam]]

Into this page: [[#Wikilinks]]

Spaced, as a vault author writes it: [[  The Lighthouse  ]]

With a parent segment, which resolution must refuse rather than absorb:
[[../sibling]]

Adjacent, which must be two links and not one link with a stray bracket:
[[One]][[Two]]

With a hash inside the alias, which belongs to the alias:
[[The Lighthouse#The Beam|the # part]]

# Embeds

An embedded asset: ![[chart.png]]

An embedded page with an alias: ![[notes/The Lighthouse|the lighthouse]]

An embed is a slot. C4 fills it, and refuses a cross-campaign target outright
(ADR 0017), because an embed is an inline and an inline is what would make this
page's HTML depend on another document.

# Labels

The label of a link is the author's own text. It is never a title read from the
target, which is what keeps this page's bytes identical for every reader.
