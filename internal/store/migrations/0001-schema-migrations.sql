-- Migration 0001: the bookkeeping table.
--
-- Every later migration records itself here, inside the same transaction as its
-- schema change, so a migration cannot be applied-but-unrecorded. The runner
-- reads this table to decide where to start, which is why it is a migration
-- rather than something the runner creates: a runner-owned table can disagree
-- with the history it is supposed to describe.
--
-- A shipped migration is never edited. Changes are new migrations with higher
-- versions (ADR 0008).

CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    applied_at INTEGER NOT NULL  -- Unix seconds; no timezone to get wrong
);

-- The campaign identity and tenancy tables arrive in phase 2, where `users` and
-- `campaigns` land together. This migration is deliberately empty of domain
-- schema: the one thing phase 1 needs is the mechanism, proven against a table
-- that has to exist before any other migration can be recorded.
