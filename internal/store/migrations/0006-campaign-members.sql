-- Migration 0006: the campaign membership table.
--
-- Last of the four, because it is the only one with two foreign keys and the
-- only one that grants anything. `role` here is the single place in the whole
-- schema where a campaign privilege comes from (S-2.6), which is why the pair
-- is the primary key rather than a surrogate id: one user has exactly one role
-- in one campaign, and a second row claiming otherwise would be two answers to
-- one question with nothing to say which wins.

CREATE TABLE campaign_members (
    -- CASCADE. A membership is scoped to its campaign and grants nothing outside
    -- it, so a campaign that no longer exists has nothing left to be a member
    -- of. Left behind, the row would also be an active hazard rather than a
    -- curiosity: SQLite reuses a bare rowid, so the next campaign registered
    -- could inherit it and hand its GM role to somebody who was never invited.
    campaign_id INTEGER NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    -- RESTRICT, and the one deliberate asymmetry in this schema.
    --
    -- Cascading a user's memberships away would make `DELETE FROM users` silently
    -- strip the last GM from every campaign that user ran -- an operator deletes
    -- a dormant account and returns days later to a campaign nobody can edit,
    -- with no error anywhere to explain it. RESTRICT refuses the delete instead,
    -- so the memberships have to be removed deliberately, one campaign at a
    -- time, by whoever is about to remove a GM. The two other cascades are
    -- unrestrictive because neither can orphan a privilege: a session without
    -- its user authenticates nothing, and a membership without its campaign has
    -- no campaign to grant anything in.
    user_id     INTEGER NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    -- `gm` | `player`. No CHECK, for the reason `campaigns.visibility` has none:
    -- `domain.ParseRole` is the one implementation of the vocabulary and refuses
    -- anything else, and an unrecognised role resolves to *no access* rather
    -- than to a GM. The empty string is refused on write by the query layer, so
    -- the failure is loud rather than merely denied.
    role        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    -- The pair, so the uniqueness the access matrix depends on is a property of
    -- the table rather than of the code that reads it.
    PRIMARY KEY (campaign_id, user_id)
);

-- The composite primary key serves every lookup *by campaign* -- reading one
-- membership, listing a campaign's members -- but not a lookup by user, because
-- campaign_id leads the key. Campaign lists are built per viewer, so the
-- by-user direction is the one the access path issues on every request, and
-- without this index it is a scan of every membership in the instance.
CREATE INDEX campaign_members_user_id_idx ON campaign_members (user_id);
