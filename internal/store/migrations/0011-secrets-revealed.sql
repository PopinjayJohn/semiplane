-- Migration 0011: `secrets_revealed`, the ledger of who revealed what and when.
--
-- Architecture §5.6.2 fixes the split this table lives inside, and the split
-- is the reason the columns are what they are:
--
--   The **file** is authoritative for whether a secret is currently revealed.
--   This table is authoritative for **who** revealed it, **when**, and whether
--   sync has since fought that. A reveal is a one-byte edit to the file
--   (`-` → `+`), and the file is the source of truth for content (S-3.1, ADR
--   0006) — so the current state of a secret is read from the file and never
--   from here. What the file cannot say is who flipped the byte and what
--   happened next: Obsidian syncs a copy that predates the reveal and will
--   periodically overwrite the `+` back to `-`, and the only thing that can
--   re-apply the reveal is the record that it was ever revealed. That record
--   is this table.
--
-- The primary key is `(campaign_id, path, anchor)` (S-5.8). The anchor is what
-- makes the key survive the two events that would otherwise lose a row:
--
--   - **Editing a secret's text.** The anchor is an Obsidian block id when the
--     callout carries one, else a derived id (S-5.9). Either way it is stable
--     across a body edit, so editing a revealed secret does not orphan its
--     row — which matters because an orphaned row is a reveal the next sync
--     reversion will not re-apply.
--   - **Renaming a page.** A rename re-keys the ledger on
--     `(campaign_id, new_path, anchor)` and leaves a tombstone for the old
--     path (§5.6.3). The anchor is the part of the key that does not move;
--     `path` is the part that does, and the re-key is what carries the reveal
--     across.
--
-- `anchor` is TEXT with no length limit and no CHECK, deliberately. The two
-- shapes it holds — an Obsidian block id of arbitrary length, and a
-- twelve-character derived hash — have nothing in common but being opaque, and
-- a CHECK that constrained them would be a second implementation of the anchor
-- resolver's rules that could not be ALTERed once shipped. The resolver (phase
-- 10 S3) owns the shape; this column stores it.
--
-- `revealed_by` is NOT NULL and **not** a foreign key to `users`, for the
-- reason `audit_log.actor_id` is not (migration 0009): the row must still name
-- the account that disclosed the secret after that account is deleted. The
-- question "who revealed this" does not expire with the account, and a
-- nullable column would make "a GM revealed this" and "nobody revealed this"
-- the same row.
--
-- `reverted_count` is the sync fight made visible. Every time reconciliation
-- (phase 10 S7) finds a `+` that sync has overwritten back to `-`, it
-- increments this. A campaign whose count is climbing has a sync client and a
-- GM who disagree, and that is worth surfacing rather than silently winning.
-- It saturates at 999 (`domain.MaxRevertedCount`): the bound is a display
-- bound, not a security bound — the security bound is S-5.10's reconciliation
-- cap, which limits how fast the count can climb. The DEFAULT 0 is the state
-- of a reveal sync has never fought.
--
-- No index beyond the primary key, and the reason is the same one migration
-- 0010 gives: every query this table is asked is a prefix of the key. Reading
-- one secret is a probe of all three columns; listing a page's reveals is a
-- range scan of `(campaign_id, path)`; listing a campaign's is a range scan
-- of `campaign_id`. A separate index on any of those would be the same keys in
-- the same order, bought for a query the planner can already answer.
CREATE TABLE secrets_revealed (
    -- INTEGER and CASCADE, like every other campaign-scoped table: a ledger
    -- row is meaningless without its campaign, and the alternative is history
    -- that answers for a tenant nobody can reach any more.
    campaign_id INTEGER NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    -- Slash-separated and relative to the campaign's content root, spelled
    -- exactly as `pages.path` spells it. Part of the primary key, and the
    -- half of it a rename moves.
    path        TEXT    NOT NULL,
    -- The callout's anchor: an Obsidian block id or a derived hash, either
    -- opaque to this table. See the header for why there is no CHECK.
    anchor      TEXT    NOT NULL,
    -- NOT NULL and unconstrained: see the header.
    revealed_by INTEGER NOT NULL,
    -- Unix seconds, never a time.Time, for the reason `migrate.go` states: the
    -- driver renders a time.Time as a formatted string and SQLite stores that
    -- in a column declared INTEGER as TEXT.
    revealed_at INTEGER NOT NULL,
    -- The sync fight counter, saturating at 999. See the header.
    reverted_count INTEGER NOT NULL DEFAULT 0,
    -- The identity of a revealed secret, and the reason a rename is a re-key
    -- rather than a new row.
    PRIMARY KEY (campaign_id, path, anchor)
);
