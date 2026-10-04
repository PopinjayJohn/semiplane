---
title: Mirror hound
kind: creature
---

## A hound that shows you what it last saw

A **creature** is a rules object with statistics. The kind is declared in the 5e data
pack rather than in Go, which is what makes it portable: a system written for a
different game describes its own creatures with its own vocabulary and no client work.

{{statblock:mirror-hound}}

That slot is filled by the plugin, and it is filled by a plugin for a reason. Which
fields a stat block has, in what order, and whether an ability is shown as a modifier
or as a number is a **rules question**, and semiplane has no opinion about it — see
[[rolling]].

## What it does

A mirror hound is a hound of about the size of a lurcher with a coat like still water.
It will not cross running water, and it goes *around* rather than *through*, which the
party worked out on the third day and which is the only reliable thing anyone can say
about it.

| Trait | Value |
| --- | --- |
| Armour class | 14 — its coat breaks up its outline rather than its hide |
| Hit dice | 4d8 |
| Speed | 40, and it will not enter water deeper than a paw |

It is **not** a shapeshifter and it does not read minds. It tracks by reflection: what
it shows you is what it last saw, which is why the party cannot catch it by looking
away.

## Where one is standing

`gh-mirror-hound`, at 1240, 700 on [[north-road]] — in the heather south of the road,
12 of 12 hit points and **prone**. It went down there on Day 3 and has not moved.

It is also carrying [[poisoned]], which is the point of having [[poisoned]] be a page:
`condition` is one of the five kinds semiplane owns, so the tabletop applies it to a
placement without knowing what poison *does*. Only the rules engine knows that.

## What this page is not

It is not a stat block. The table above is prose written for a reader; the machine
version is in the plugin's data pack, and the page is where the Game Master puts the
table-specific material — what happened to the last one, what the party has worked out,
what it wants. Two copies of a creature's armour class, one in a YAML pack and one in
Markdown, would be two answers to one question and the Markdown one would be the one
that drifts.

Back to [[index]].