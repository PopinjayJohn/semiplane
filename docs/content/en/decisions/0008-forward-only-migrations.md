---
title: "0008 — Forward-only migrations"
description: "A shipped migration is never edited. New schema means a new migration, and FTS5 tables are drop-and-recreate."
lede: "Editing a migration that has already run is the one schema mistake that corrupts an existing installation quietly. Making it impossible to do by accident is cheaper than detecting it afterwards."
weight: 80
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

An installation holds its database across upgrades. There is no "reinstall fresh" path a user
can be asked to take, because the database is not the source of truth — it holds `campaign_state`
and `page_revisions`, which are **not** in the vault and are therefore not recoverable from it.

That makes the upgrade path the only path. A migration that ran yesterday must still be
correct today.

## Decision

**Migrations are forward-only, ordered, and live under `internal/store/migrations/`. A shipped
migration is never edited — only added to.**

Applied migrations are recorded, so the runner knows where to start.

Two specific consequences:

- **A new migration is the only way to change schema.** Not `IF NOT EXISTS`, not a check for an
  existing column. Write the next migration.
- **FTS5 virtual tables are the exception the rule has to carve out.** They cannot be `ALTER`ed
  at all — not the columns, not the tokenizer. Changing one means dropping and recreating it and
  then running `INSERT INTO pages_fts(pages_fts) VALUES('rebuild')`.

The FTS carve-out exists because `pages_fts` is a view over `pages`, not a copy of the content.
Recreating it and rebuilding loses nothing, which is what makes the drop safe here and would not
make it safe anywhere else.

## Consequences

- A migration is immutable once it has shipped, and that is enforced socially by `AGENTS.md`
  and by the review checklist rather than by a tool. Reviewers watch for diffs to existing
  migration files.
- Schema history is append-only and readable. Someone upgrading from an old version can read
  every step the schema took to get to where they are.
- Column renames and type changes are expressed as add-then-migrate-then-drop across several
  migrations. That looks like clutter and is the correct cost.
- Changing the FTS tokenizer, as
  [0007]({{ "decisions/0007-fts5-tokenizer/" | relURL }}) contemplates for trigram, is a drop,
  a recreate, and a rebuild — which is safe only because the content lives in `pages`.

## Alternatives considered

**A migration tool with automatic down-migrations.** Rollback support is genuinely useful, and
its absence here is a real limitation. Rejected because an automatic down-migration is a guess:
it cannot know whether dropping a column loses a value the operator needed. For a tool whose
database holds irreplaceable game state, a migration that refuses to guess is safer than one
that does. Recovery, when needed, is a restore from backup.

**Hash-chained migrations, refusing to run if a recorded migration has changed.** This *would*
make the immutability rule mechanical rather than social, and it is a small amount of code —
store each migration's hash with its applied record and compare on startup. It was not adopted
in phase 0 because the phase already carries a large surface, and it is genuinely worth adding.
Recorded here as a known gap rather than left as a silent one.

**Rebuild the database on every upgrade.** Defensible, given that most of it is an index — but
it discards `campaign_state` and `page_revisions`, which are the parts that cannot be rebuilt.
Rejected on that basis alone.