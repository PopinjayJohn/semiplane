-- Migration 0007: the pages index and its FTS5 shadow.
--
-- The first table scoped by a campaign that is not identity. Everything below a
-- campaign lands here in phase 3 because the content read path needs the index
-- to exist before it can answer a page request: the render cache is keyed by
-- `content_hash` (S-5.2), the wiki route reads a row by path, and search reads
-- `pages_fts`.
--
-- This is an index, not the content. S-3.1 and ADR 0006 make the filesystem the
-- source of truth, and every column below is derived from a file that can be
-- re-read. `make demo-check` and the watcher in phase 4 rebuild it from scratch;
-- nothing in the schema makes it authoritative, and nothing should be written
-- here that a rebuild would not reproduce.

CREATE TABLE pages (
    -- AUTOINCREMENT, for a harder reason than `users.id` and `campaigns.id` have.
    -- Those two reuse a bare rowid only across a deletion, which is rare. Here it
    -- is the whole lifecycle of a page: the watcher deletes the row every time a
    -- file is removed and re-inserts it when the file comes back, which on a sync
    -- client is a routine sequence. A reused rowid would then bind the orphaned
    -- `pages_fts` entries for the old page to whatever page now owns the id, so
    -- a search result would name a file that never contained the words. There are
    -- deliberately no triggers keeping the FTS table in step (see below), so
    -- AUTOINCREMENT is the only thing standing between a deleted page and another
    -- page inheriting its search hits.
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    -- CASCADE, like `campaign_members.campaign_id`. A page is scoped to its
    -- campaign and is meaningless without one, and the alternative is a row that
    -- answers for a campaign no longer registered -- which, once `pages_fts` is
    -- involved, means a search result pointing into a deleted tenant.
    --
    -- The cascade leaves the FTS entries behind: FTS5 is a virtual table and no
    -- foreign key reaches it. Those rows join to nothing, so they cannot be
    -- returned, but they do occupy the index. `DeleteCampaign` rebuilds the index
    -- afterwards for that reason, and the rebuild is also the cheapest way to
    -- prove the index still agrees with `pages`.
    campaign_id INTEGER NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    -- Slash-separated, relative to the campaign's content root. UNIQUE with
    -- `campaign_id` because the pair is the page's identity: the same path in two
    -- campaigns is two pages, and a URL names a campaign and a path (S-9.1).
    --
    -- The architecture record lists a `front_matter` column here and this schema
    -- does not have one, deliberately. The front matter is in the file, the file
    -- is the source of truth, and a second copy of a block the parser can
    -- regenerate in a millisecond is a second copy that can disagree with the
    -- first -- the failure S-3.1 and ADR 0006 exist to prevent. Anything that
    -- needs the front matter re-reads it through the content package.
    path        TEXT    NOT NULL,
    -- The *honoured* kind: `prose` when the page declared no kind, and equally
    -- when it declared one this build does not know (S-3.3).
    --
    -- `prose` rather than an empty string, so a blank column is always a bug and
    -- never an answer. No CHECK, for the reason `campaigns.visibility` has none:
    -- the vocabulary is whatever the plugin registry holds (S-3.4), which is not
    -- a closed set this schema could enumerate, and an unrecognised kind is
    -- written as `prose` by the writer rather than being rejected here. Storing
    -- the declared value instead would be the alternative, and it would put a
    -- column in the index that says something about a *build* rather than about
    -- the page -- every row in it would need rewriting when a plugin is
    -- installed or removed.
    kind        TEXT    NOT NULL,
    -- The display name, empty when the front matter gave none. NOT NULL because
    -- "no title" is an answer and an empty column would be indistinguishable from
    -- a scan that failed to read one.
    title       TEXT    NOT NULL,
    -- The searchable text, and the *redacted* text: `[!secret]` callout content is
    -- excluded from it in every reveal state (S-5.11). A search snippet is built
    -- from this column, so it is what stops a player being shown a secret through
    -- the search box.
    --
    -- One column, and not a plain plus an unredacted variant, because a second
    -- column is a second thing that has to remember to exclude, and what it would
    -- record -- that a secret is currently revealed -- is already in the file and
    -- in `secrets_revealed` (S-5.8). The schema therefore cannot represent
    -- "indexed with secrets", which is the point: the impossible state is the one
    -- that has to be unrepresentable.
    body_plain  TEXT    NOT NULL,
    -- The digest of the file's bytes. The render cache is keyed on it and never on
    -- mtime (S-5.2): sync paths coarsen mtime and 1-second granularity lets two
    -- distinct writes collide. Hex, because `ETag` quotes it and an ETag cannot
    -- carry arbitrary bytes (S-5.3).
    content_hash TEXT   NOT NULL,
    -- The file's length on disk. The watcher compares it across two `stat`
    -- samples before indexing (S-4.3), and it is the size the document cap is
    -- measured against (S-4.7) -- so recording it here is what lets both of those
    -- be answered from a row rather than from a second `stat` that a rename may
    -- already have invalidated.
    byte_size   INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- The identity of a page, and the campaign-scoped read path.
--
-- The unique index *is* the read path's index: it leads with `campaign_id`, so
-- every lookup by campaign -- listing a campaign's pages for the wiki index, for
-- a broken-link report, for the token list in phase 9 -- is a range scan of one
-- campaign's rows already in path order, and the read that names a page by path
-- is a direct probe. A separate index on `campaign_id` would be the same keys in
-- the same order, which is why there is not one.
--
-- The name says `key` rather than `idx` because it enforces a constraint, which
-- `ON CONFLICT (campaign_id, path)` in the upsert depends on by name. Renaming it
-- would silently change what the upsert's conflict target resolves to.
CREATE UNIQUE INDEX pages_campaign_path_key ON pages (campaign_id, path);

-- The one listing the leading column cannot serve: a campaign's pages of one
-- kind. Phase 9's token list and phase 8's game-object loader both ask this, and
-- neither can be answered by the unique index above without scanning every page
-- in the campaign to find the tokens among them.
CREATE INDEX pages_campaign_kind_idx ON pages (campaign_id, kind);

-- The FTS5 shadow over `pages`. ADR 0007 fixes the tokenizer and the reason:
-- `unicode61 remove_diacritics 2` and deliberately no `porter`, because TTRPG
-- proper nouns stem badly -- a search for `Svartalfheim` must not be folded into
-- a word nobody typed, and it must fold diacritics because a GM typing without
-- the accent is a keyboard, not a mistake.
--
-- External content, so the index stores no copy of the text: `body_plain` exists
-- once, in `pages`, and there is no second copy to fall out of step with the
-- first. The cost of that choice is that every query must join `pages` to get
-- anything at all, and `pages_fts` is not ALTERable -- a bulk change goes through
-- `INSERT INTO pages_fts (pages_fts) VALUES ('rebuild')`, which
-- `DeleteCampaign` uses.
--
-- There are deliberately no triggers on `pages` to maintain this. FTS5's
-- maintenance commands are the same `INSERT INTO pages_fts(...)` statements the
-- writer issues, so a trigger would move them somewhere a test cannot reach them
-- -- and this schema declares no triggers precisely because `errors.go`'s
-- constraint mapping treats SQLITE_CONSTRAINT_TRIGGER as a foreign-key violation,
-- which an immediate trigger would falsify. The invariant that keeps them in step
-- is instead stated once, in pages.go: the only writer of `pages` is the upsert,
-- and the upsert updates the index in the same transaction.
CREATE VIRTUAL TABLE pages_fts USING fts5(
    title,
    body_plain,
    content='pages',
    content_rowid='id',
    tokenize='unicode61 remove_diacritics 2'
);
