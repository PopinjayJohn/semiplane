-- Migration 0005: the campaigns table.
--
-- First of the tenancy family. Everything below a campaign -- pages, assets,
-- search rows, live state -- is scoped by this table's id, so it lands before
-- `campaign_members` names it and before any phase that indexes content.

CREATE TABLE campaigns (
    -- AUTOINCREMENT for the same reason as `users.id`: a bare rowid is reused
    -- after the last row is deleted, and here the consequence is worse than an
    -- orphaned index entry. `campaign_state` (migration 0002) references this
    -- column and has no foreign key, because `campaigns` did not exist when it
    -- was written. Without AUTOINCREMENT a deleted campaign's id would be handed
    -- to the next campaign registered, and that campaign would inherit the dead
    -- one's live game state -- a table's hit points, a scene, a token's
    -- conditions -- as its own opening position. AUTOINCREMENT makes the
    -- handover impossible rather than merely unlikely.
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    -- The tenancy key: it appears in every URL, every cache key and the ETag's
    -- partition, so two campaigns differing only in slug are two tenants and a
    -- slug that renders ambiguously is two tenants for one reader. Uniqueness
    -- is enforced here rather than by `domain.ValidateSlug`, which owns the
    -- *form* of a slug; the two cannot be confused because only one of them can
    -- see another writer's committed row.
    slug          TEXT    NOT NULL,
    name          TEXT    NOT NULL,
    -- Absolute, and written once. `os.Root` is built from this value and is the
    -- path-confinement boundary for the whole campaign (AGENTS.md, §12), while a
    -- relative path would resolve against whatever directory the process
    -- happened to be started in -- so the same database would confine two
    -- deployments to different directories. The query layer refuses a relative
    -- value on write; nothing here can, because SQLite has no notion of an
    -- absolute path and inventing one in a CHECK would assume a filesystem.
    content_root  TEXT    NOT NULL,
    -- `private` | `public` (S-8.1). Deliberately not a CHECK constraint: the
    -- vocabulary is two closed values the domain already owns, and
    -- `domain.ParseVisibility` refuses anything else, so a check here would be a
    -- second implementation of a rule that has one. It would also be the more
    -- expensive of the two -- SQLite cannot ALTER a CHECK, so extending the
    -- vocabulary would become a full table rebuild in a forward-only schema --
    -- and the failure it would guard against already fails closed: an
    -- unrecognised visibility resolves to no access rather than to a published
    -- campaign.
    visibility    TEXT    NOT NULL,
    -- Present from the start rather than added by the ALTERs in architecture
    -- record §10.7, so no later phase needs one (plan D17). No DEFAULT, unlike
    -- the ALTER's `DEFAULT '5e-2024'`: that default was an artefact of adding a
    -- column to tables that already had rows, and carried into a CREATE it would
    -- make a campaign silently depend on a gameplay plugin that may not be
    -- registered -- an unknown `system_id` refuses the game and still serves the
    -- wiki (S-14.8). Registration states the system it wants.
    system_id     TEXT    NOT NULL,
    -- Fingerprints the resolution semantics the persisted state was written
    -- under, not the house-rule configuration: toggling a house rule must not
    -- strand a campaign (ADR 0018). The empty string is a meaningful value --
    -- state written under no particular ruleset -- and it is stated explicitly
    -- rather than defaulted, so that a row is never half-specified.
    ruleset_version TEXT  NOT NULL,
    created_at    INTEGER NOT NULL
);

CREATE UNIQUE INDEX campaigns_slug_key ON campaigns (slug);

-- `campaign_state` still carries no foreign key, and 0002 is not edited: a
-- shipped migration is immutable (ADR 0008). SQLite has no ALTER for adding a
-- constraint, so the foreign key needs the documented table rebuild, whose
-- first step is `PRAGMA foreign_keys=OFF` *outside* a transaction -- and the
-- runner executes each migration inside one, where SQLite documents that pragma
-- as a no-op. Doing it correctly is therefore a change to the migration runner,
-- not a change to this table, and it belongs with the work item that owns
-- `campaign_state` (phase 4, W4: "exactly one row per campaign").
--
-- What the rebuild is actually deciding is a question no phase has asked yet:
-- whether deleting a campaign may delete its live game state. Cascading here is
-- irreversible and irreversible is the wrong default for a tabletop mid-session,
-- restricting it means a forgotten campaign cannot be removed. Until the
-- deletion path exists there is nothing to protect against, and the AUTOINCREMENT
-- above reduces the exposure in the meantime: a stale row can no longer bind to
-- a different campaign.
