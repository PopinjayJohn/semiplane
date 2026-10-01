-- Migration 0003: the users table.
--
-- First of the identity family, and first of this phase's four migrations
-- because `auth_sessions` names this table as a foreign key and `campaigns`
-- reaches it through `campaign_members`. Ordering by the reference graph rather
-- than by which table sounds most important keeps the foreign keys resolvable at
-- the moment each one is created, so a mistake here is a migration error rather
-- than a deferred one at first insert.
--
-- Every timestamp in this schema is a Unix second count in an INTEGER column,
-- for the reason migration 0001 records against `applied_at`: the driver renders
-- a time.Time as a formatted string, SQLite's dynamic typing then stores that in
-- an INTEGER-declared column as TEXT, and a later `WHERE created_at < ?` against
-- an integer compares text to integer -- which sorts every text value last, in
-- the direction that hides exactly the rows the query was written to find.

CREATE TABLE users (
    -- AUTOINCREMENT, not a bare INTEGER PRIMARY KEY. A bare rowid is reused
    -- once the highest row is deleted, and a reused account id would hand every
    -- row that outlived its user -- a membership today, a revision and an audit
    -- entry in later phases -- to whichever account happened to get the number
    -- next. That is a privilege granted to the wrong person by arithmetic, and
    -- AUTOINCREMENT is the only thing in SQLite that forbids it.
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    -- Compared verbatim, and the unique index is an exact-match one. Case folding
    -- is a *domain* decision that internal/domain has not made: folding would
    -- make the stored username differ from the one the user typed, so the
    -- account a login resolves carries a name its owner never entered, and it
    -- would make `Alice` and `alice` one person by a rule no requirement states.
    -- `domain.Role` and `domain.Visibility` refuse an unrecognised value for the
    -- same class of reason, so a username this build cannot interpret is a
    -- question for the domain package, not a normalisation to perform here.
    -- TestUsersUsernameIsCaseSensitive pins the current behaviour.
    username      TEXT    NOT NULL,
    -- NOT NULL with an empty default rather than nullable: `domain.User.Email`
    -- is a string, so a nullable column would need a *string and would give the
    -- one fact -- "there is no address on file" -- two representations to keep
    -- straight at every read.
    email         TEXT    NOT NULL DEFAULT '',
    -- A credential. Never selected into a log line, never rendered, never
    -- serialised into an error: `domain.User` has no String method for the same
    -- reason, and the read path here returns the field without formatting it.
    password_hash TEXT    NOT NULL,
    -- INTEGER, because SQLite has no boolean and Go's driver writes a bool as
    -- 0 or 1. Instance-level only (S-2.7): it authorises campaign registration
    -- and user management, and it is not a campaign role.
    is_admin      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL
);

-- The uniqueness the login path depends on, enforced where two concurrent
-- registrations cannot both believe they won. A check-then-insert in Go would
-- have a window between the two statements, and on this connection that window
-- is not even narrow: the single-writer queue runs the pair as two separate
-- transactions.
CREATE UNIQUE INDEX users_username_key ON users (username);
