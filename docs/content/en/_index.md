---
title: "semiplane"
description: "A self-hosted, system-agnostic TTRPG wiki and virtual tabletop."
---

semiplane is a self-hosted, system-agnostic TTRPG wiki and virtual tabletop. One
Go binary, one SQLite file, and campaign content in an Obsidian vault you
already keep in sync.

It exists because the two halves of running a game — the wiki your group writes
and the tabletop you play on — are usually two programs with two models of the
same content, and keeping them in agreement is manual. Here, a page *is* the
content: a markdown file in your vault is simultaneously prose, a stat block, a
map, and a handout.

> **Status: pre-release.** The repository is a scaffold. Nothing described on
> this site ships yet, and the [roadmap]({{ "roadmap/" | relURL }}) is the
> authoritative statement of what exists. Read it before you plan around
> anything here.

## What it is going to be

- **A wiki that reads your vault.** Filesystem markdown is the source of truth.
  Edit in Obsidian, in the browser, or both; the watcher converges and the search
  index follows. Nothing is trapped in a database you cannot read.
- **A tabletop with no rules baked in.** Gameplay systems — D&D 5e, Pathfinder,
  anything else — plug in as compiled-in modules behind one interface. The map
  and the token layer know nothing about any rules system, so a new one costs no
  client work.
- **A server you can actually run.** One process, one SQLite file, no external
  services, no account to create anywhere.

## What it is not

Stated up front because the limits are load-bearing:

| Not | Why |
|---|---|
| Horizontally scalable | Authoritative game state lives in memory in one process. Two instances diverge silently. |
| A replacement for Obsidian | Obsidian is where you *write*. semiplane is where you *read and play*. |
| A hosted service | There is no SaaS. If you want it, you run it. |
| Dynamically extensible at runtime | Plugins are compiled-in Go modules, not `.so` files dropped into a directory. |
| Encrypted at rest | See [secrets]({{ "concepts/secrets/" | relURL }}). Secrets are hidden from viewers, not from anyone with filesystem access. |

## Start here

{{< buttons >}}
  {{< button href="install/" >}}Installation guide{{< /button >}}
  {{< button href="concepts/" ghost="true" >}}Concepts and terminology{{< /button >}}
  {{< button href="design/" ghost="true" >}}Design records{{< /button >}}
{{< /buttons >}}

New to the project? Read the [concepts page]({{ "concepts/" | relURL }}) before
anything else. semiplane is deliberate about vocabulary, and half of the design
records will make more sense once "campaign" and "session" stop meaning the same
thing in your head.

## The honest caveat

The [architecture]({{ "design/architecture/" | relURL }}) and
[UI specification]({{ "design/ui-ux/" | relURL }}) are published verbatim,
including their open questions and the decisions that were deliberately deferred.
They are the best available description of intent, and they are not documentation
of a working system. Where the two disagree, the code is right.
