-- Migration 0009: `audit_log`, the record of acts rather than of content.
--
-- `page_revisions` (0008) is the only history a vault cannot supply, and it is
-- about *text*: what a path held. This table is about *acts*, and the difference
-- is why the columns are what they are rather than the ones `page_revisions`
-- uses. The first use is S-7.8's recovery path — a GM discarding a campaign's
-- persisted game state because the ruleset it was written under no longer
-- matches the registered one. That action is destructive, irreversible, GM-only
-- and confirmation-gated, and the four properties that make it defensible are
-- the same four that make leaving no record indefensible: nobody else can undo
-- it, nothing else records that it happened, and a table that did not exist
-- would be added *casually*, which is how audit gaps are built.
--
-- The row and the deletion it describes are written in **one transaction** by
-- `realtime.Discarder`. That is the whole reason this table is here rather than
-- a log line: a log line is best-effort and a transaction is not, and "the state
-- is gone and the record of why is missing" is the failure the pairing exists to
-- make impossible.
--
-- No `campaign_state` foreign key, and the same is true of `campaign_state`
-- itself (migration 0005 explains at length: adding one needs a table rebuild,
-- and the runner executes each migration inside a transaction where
-- `PRAGMA foreign_keys=OFF` is documented as a no-op). The link is by
-- `campaign_id`, and a row is written by code that already holds the campaign.

CREATE TABLE audit_log (
    -- AUTOINCREMENT, for the reason `page_revisions.id` carries it and for a
    -- second one: an audit row is append-only, so its id is also its order. A
    -- bare rowid is reused after the last row is deleted, which would make "the
    -- third thing that happened" a question with two answers.
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    -- CASCADE, like every other campaign-scoped table that this project writes:
    -- an audit row is meaningless without its campaign, and the alternative is
    -- history that answers for a tenant nobody can reach any more.
    campaign_id INTEGER NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    -- NOT NULL, and deliberately **not** a foreign key to `users`. Both halves of
    -- that are the same decision: an audit row must still name the account that
    -- performed the action after that account is deleted, so the id is kept as a
    -- record even once the row it names is gone.
    --
    -- This is a deviation from `page_revisions.author_id`, which is
    -- `ON DELETE SET NULL` and nullable, and the reason the two differ is that
    -- they are not the same kind of table. A revision is a record of *content*,
    -- and its author is incidental metadata; an audit row is a record of *an
    -- act*, and the actor is the whole of what it says. A nullable actor_id
    -- would also make "a GM discarded this table's state" and "an anonymous
    -- request discarded this table's state" the same row, which is precisely the
    -- distinction S-7.8 requires the log to keep.
    --
    -- NOT NULL and unconstrained therefore means an unattributed action cannot
    -- be written at all. A system-initiated act — one with no account behind it —
    -- is a real case and is handled by naming the service's account, not by
    -- widening this column.
    actor_id    INTEGER NOT NULL,
    -- The closed vocabulary of *what was done*, dot-separated and lowest-first:
    -- the subject, then the verb. The architecture record fixes the column, not
    -- the values, and the values are closed here for the reason `page_revisions
    -- .source` is CHECKed: who performed an action and what class of action it
    -- was are decided by this project, so a value outside the set is a bug rather
    -- than a difference between two builds. Adding an action is a migration,
    -- which is the cost a forward-only schema already charges.
    --
    -- The one value phase 7 writes is `state.discard`.
    action      TEXT    NOT NULL,
    -- What the act was done *to*, in a form a reader can match on without
    -- parsing `detail`. For `state.discard` it is `campaign_state/<id>`: the
    -- table and the row, because the row is the whole of what was lost and the
    -- table alone would not say which one.
    target      TEXT    NOT NULL,
    -- The human-meaningful specifics: the versions involved, the numbers, the
    -- reason. A sentence rather than a bag of columns because the set of
    -- specifics is different per action, and a column per fact is a migration per
    -- fact.
    --
    -- It carries the *shape* of what was lost and never any of it — a revision
    -- number and a count, never a placement, a hit-point value, a page body or a
    -- `[!secret]` callout. S-12.3 forbids an event carrying secret content, and
    -- the temptation here is specific: a state document is a bag of everything on
    -- the tabletop, and writing it into an audit row would be the easiest way in
    -- this project to put a GM's whole campaign in a place nobody gated. The
    -- values written are plugin identifiers, version strings, and integers this
    -- project minted.
    detail      TEXT    NOT NULL,
    -- Unix seconds, never a time.Time, for the reason `migrate.go` states: the
    -- driver renders a time.Time as a formatted string and SQLite stores that in
    -- a column declared INTEGER as TEXT.
    created_at  INTEGER NOT NULL
);

-- "What has this campaign had done to it, newest first?" — which is how an
-- operator reads an audit log, and the one question a table of acts is asked.
-- `(campaign_id, id)` rather than `(campaign_id, created_at)`: the two are equal
-- in practice only because nothing here deletes a row, and the id is the one
-- ordering a reader can rely on being total even if two rows land in the same
-- second, which on a debounced writer and a fast test is not a hypothetical.
CREATE INDEX audit_log_campaign_id_idx ON audit_log (campaign_id, id);

-- "Who did this, and when?" — the second question, and the one an incident review
-- actually starts from. It is a separate index because the first one's leading
-- column is `campaign_id`, and a review that knows the actor but not the campaign
-- would otherwise be a full scan of a table that is never small in a busy
-- instance.
CREATE INDEX audit_log_actor_idx ON audit_log (actor_id, id);
