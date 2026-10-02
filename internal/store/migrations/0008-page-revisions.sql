-- Migration 0008: `page_revisions`, the only history a vault cannot supply.
--
-- Every other table here is either identity, tenancy, or a cache the filesystem
-- can rebuild (S-3.1, ADR 0006). `page_revisions` is the exception ADR 0008
-- names when it explains why the upgrade path is the only path: a row here is
-- not derivable from any file, because a revision is the content a page *used
-- to hold* and the vault only ever holds what it holds now. Delete this table
-- and the history is gone; nothing re-derives it, and Obsidian's own history is
-- not a substitute because it lives in whatever sync provider the GM happens to
-- use, if any.
--
-- S-6.4 makes the write path append a row, and S-6.2 makes a *refused* write
-- append none — so the row is the audit trail of what semiplane published, and
-- the reason the columns are what they are:
--
--   - `content` is the whole source, not a diff and not a hash. A merge needs
--     the text; a hash only proves two things differ.
--   - `author_id` is nullable because the architecture record's `source`
--     vocabulary includes `obsidian`, and a change a sync client made has no
--     semiplane account behind it. The FK is ON DELETE SET NULL for the same
--     reason plus one more: deleting an account must not delete the history of
--     a page, which is a fact about the page rather than about the person.
--   - `source` is CHECKed, and this is the one column in the schema whose
--     vocabulary *is* closed by the schema. `pages.kind` is deliberately not
--     (migration 0007 explains: the vocabulary belongs to the plugin registry,
--     and a column that names a build is a column every plugin change rewrites),
--     but who performed a write is decided by this project and nothing else, so
--     a value outside the three is a bug here rather than a difference between
--     two builds. Adding a source is a migration, which is the cost
--     forward-only already charges for a schema change.
--
-- No `content_hash` column. The architecture record's column list does not have
-- one, and a hash next to the content it digests is a second answer to "what
-- did this page hold" that can disagree with the first — the same argument
-- migration 0007 makes for not putting front matter on `pages`.

CREATE TABLE page_revisions (
    -- AUTOINCREMENT for the reason `pages.id` carries it, and it is a stronger
    -- reason here: `content` is immutable history, so a rowid reused after a
    -- deletion would hand a later reader a revision that is not the one they
    -- asked for, with no way to tell. The cost is a monotonically growing
    -- sqlite_sequence per table, which for a per-save history is the right
    -- trade — this table grows with saves, not with pages.
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    -- CASCADE, like every other campaign-scoped table: a revision row is
    -- meaningless without its campaign, and the alternative is history that
    -- answers for a tenant nobody can reach any more.
    campaign_id INTEGER NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    -- Slash-separated and relative to the campaign's content root, spelled
    -- exactly as `pages.path` spells it. It is the *page's* path at the moment
    -- of the write, and it is deliberately not updated when the page is renamed:
    -- a revision is a statement about what a path held, and rewriting it on a
    -- rename would make the history claim the page was never called anything
    -- else. A reader that wants a page's history across renames asks the
    -- watcher, which knows the old path.
    path        TEXT    NOT NULL,
    -- The page's whole source as it was written, unredacted. There is no
    -- redacted variant column and there must not be one: S-5.11 excludes
    -- `[!secret]` content from the *search index* in every reveal state, and
    -- this table is not an index — it is the record of what was published, and
    -- a history with the secrets edited out is a history that cannot explain
    -- the file it is a history of. Access to it is the GM's (S-6.5), and it is
    -- never rendered to anybody else.
    content     TEXT    NOT NULL,
    -- ON DELETE SET NULL, and nullable: see the header.
    author_id   INTEGER REFERENCES users (id) ON DELETE SET NULL,
    -- CHECKed: see the header. The three values are the architecture record's
    -- `web` | `obsidian` | `system`.
    source      TEXT    NOT NULL
                CHECK (source IN ('web', 'obsidian', 'system')),
    -- Unix seconds, never a time.Time, for the reason `migrate.go` states: the
    -- driver renders a time.Time as a formatted string and SQLite stores that in
    -- a column declared INTEGER as TEXT.
    created_at  INTEGER NOT NULL
);

-- The two questions this table is asked.
--
-- 1. "What did this page hold when the conflict happened?" — the most recent
--    revision of one path, which is the common ancestor a real three-way merge
--    would need (architecture §6.2). Leading with (campaign_id, path) and
--    trailing with id makes that a range scan that reads backwards, and makes
--    the whole history of a page a forward scan of the same range.
-- 2. "What are this page's revisions, newest first?" — the same range with a
--    descending read.
--
-- Both are served by one index because the questions differ only in direction,
-- and two indexes on the same columns in different orders is a write cost
-- bought for a convenience a planner can already provide. `(campaign_id, path,
-- id)` and not `(campaign_id, path)`: without the id a per-page listing cannot
-- be ordered, and `ORDER BY id DESC` over an unordered range is a sort of every
-- revision of every page in the campaign.
CREATE INDEX page_revisions_campaign_path_idx ON page_revisions (campaign_id, path, id);
