---
title: "0006 — The content root is the source of truth"
description: "Markdown files in an Obsidian vault are the content. SQLite is a rebuildable index plus live game state."
lede: "Nothing about your campaign is trapped in a database you cannot read. That is the product, not a convenience — and it decides what gets backed up, what gets lost, and what a bug in the server can cost you."
weight: 60
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

There are two writers to campaign content and no shared lock between them: the web editor, and
whatever the GM does in Obsidian — which includes three sync providers, on any number of
devices, at any time.

This is not a problem to be solved. It is the intended shape. The GM keeps their campaign in an
Obsidian vault because that is where they already keep it, with the plugins and backlinks and
sync they already rely on. A product that required content to live only inside semiplane would
be a worse product.

The cost is that the server cannot assume it is the only writer, and cannot hold a lock that
Obsidian respects — Obsidian is a separate process that has never heard of semiplane.

## Decision

**The filesystem is the source of truth. The database is a rebuildable index plus
`campaign_state`.**

| Stored in | What |
|---|---|
| The `.md` file | Every piece of content. Title, prose, front matter, token stats, scenes, handouts, journals. |
| `pages`, `pages_fts` | An index. Delete it and rebuild from the filesystem; the content is unaffected. |
| `campaign_state` | Live game state that has no home in a file. One row per campaign. |
| `page_revisions` | History. The only thing here that is not derivable from the vault. |
| `secrets_revealed` | Who revealed a secret, when, and whether a sync reverted it. |

### Placements are never written to files

The one rule this record exists to protect: **live placement state goes in
`campaign_state`, never in front matter.** A token's current hit points, position, and
conditions are runtime values.

If current HP lived in a page's front matter, every damage event would rewrite the file. That
would collide head-on with Obsidian's sync, which is simultaneously delivering a copy the GM
made five minutes ago — and would produce a permanent conflict storm in a shared vault.

Game *definitions* are files, written by the GM, stable: a token's maximum HP, AC, portrait,
size. Game *placements* are runtime. The architecture record's §4.2 table is the authority.

### What this costs

- **Backups are two artifacts that can desync.** The content root *and* a consistent SQLite
  snapshot — taken with the `.backup` API or `VACUUM INTO`, never a raw file copy while the
  process is running. The vault is the source of truth for pages; the database is an index
  plus game state.
- **A deleted database loses game state and history.** Content survives, because it is files.
  The reverse — losing the vault — loses everything, and there is no server-side copy of it.
- **The server can be wrong about a page and the file is right.** Every design decision in the
  content pipeline follows from this: render caches key on content hash rather than mtime,
  because sync paths preserve or coarsen mtime and 1-second filesystem granularity lets
  distinct writes collide.

## Consequences

- The file system is not an export format. It is the format. There is no import path, because
  there is nothing to import *from*.
- A campaign root can be opened in any text editor. A plugin that mishandles a file produces a
  broken file, not lost content.
- The watcher is load-bearing rather than an optimisation. Nothing renders on demand from disk;
  the pipeline is event-driven, and a missed watch event means stale content until something
  else triggers a re-read.
- Obsidian Sync is **untrusted input**: shared vaults, community plugins, compromised devices.
  Every path in front matter resolves inside `os.Root`, and all YAML is parsed with limits.

## Alternatives considered

**Content in the database, files as an export.** Rejected outright — it makes the Obsidian
vault a second-class copy, reinstates the sync conflict storm, and means the thing users are
told is authoritative is actually a derivative.

**A file lock between the two writers.** Rejected because it cannot work: Obsidian is unaware
of the lock and will overwrite regardless. Conflict resolution is an HTTP optimistic
precondition, not a lock — see §6 of the architecture record.

**Git for content history.** Rejected: the architecture record's §4 note is accurate that this
is awkward with two writers and content living outside the repository. `page_revisions` covers
the web-editor case; the vault's own sync provider covers the rest.