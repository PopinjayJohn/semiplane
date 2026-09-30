-- Migration 0002: per-campaign live state.
--
-- Exists as a second migration so the runner's ordering, idempotence and
-- per-migration transaction behaviour are exercised against a real pair rather
-- than a single file. `campaigns` itself arrives in phase 2, so there is no
-- foreign key here yet; the column shape matches what phase 2 will create.

CREATE TABLE campaign_state (
    campaign_id INTEGER PRIMARY KEY,
    state       BLOB    NOT NULL,
    version     INTEGER NOT NULL DEFAULT 0,
    updated_at  INTEGER NOT NULL
);
