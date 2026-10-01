-- Migration 0004: the auth session table.
--
-- Second of the identity family, keyed on the *hash* of the session token and
-- never on the token. `auth.HashSessionToken` produces the hex SHA-256 of the
-- 32 bytes of crypto/rand it minted, and that hash is all this table ever sees.
-- The consequence is the one that matters: a read of this table -- a stolen
-- backup, a stray SQL dump, a support ticket attachment, a page that somehow
-- reaches a log -- yields 64 hex characters that authenticate nobody, because
-- possessing them does not let anybody reconstruct the token they were derived
-- from. Storing the token itself would turn every one of those reads into a set
-- of live credentials.
--
-- The primary key is therefore the token hash. It is the only key: a session is
-- looked up by its token and listed by nothing else, so a surrogate id would be
-- a second thing to keep in step with the hash and a second way to name a
-- session in a log line.

CREATE TABLE auth_sessions (
    -- NOT NULL, stated explicitly, and load-bearing. In SQLite a `TEXT PRIMARY
    -- KEY` on an ordinary rowid table does *not* imply NOT NULL -- the quirk is
    -- documented, and it is why this column would otherwise accept a NULL. A
    -- NULL in a primary key defeats the point of the column: a lookup with a
    -- NULL token would match the NULL row, so a cookie carrying no token at all
    -- would authenticate. TestSessionTokenHashRejectsNull is the guard.
    --
    -- 64 hex characters for SHA-256. The column carries no length constraint
    -- because SQLite's declared types are affinity rather than enforcement, and
    -- a CHECK on length() would be a second statement of a fact the Go function
    -- already guarantees. Anything that is not 64 characters simply never
    -- matches.
    token_hash TEXT    NOT NULL PRIMARY KEY,
    -- CASCADE, and this is the one place a cascade is unambiguously right. A
    -- session is a bearer credential for exactly one account: with the account
    -- gone the row authenticates nothing, so retaining it protects nobody, and
    -- "delete this account" would otherwise have to be two statements in one
    -- transaction for no gain. The user_id index below exists so the cascade
    -- does not scan this table -- SQLite does not index a foreign key's child
    -- column on its own.
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    -- Mandatory, and the query layer refuses to write a session without one. A
    -- session with no expiry is a bearer token that never stops working, which
    -- is the failure mode of forgetting to set it rather than of choosing to:
    -- "lasts as long as the cookie" is already the cookie's Max-Age.
    expires_at INTEGER NOT NULL
);

-- Serves the per-user sweep a logout-everywhere and an account deletion both
-- want, and keeps the cascade above off a full scan.
CREATE INDEX auth_sessions_user_id_idx ON auth_sessions (user_id);

-- Serves expired-session cleanup. Without it the sweep is a scan of the whole
-- table on every run, and the sweep is the only thing standing between a
-- long-lived instance and an unbounded table of dead credentials.
CREATE INDEX auth_sessions_expires_at_idx ON auth_sessions (expires_at);

-- Revocation is row deletion, and there is deliberately no `revoked_at` column.
--
-- A tombstone means a revoked token is still a row, so every read has to
-- remember to exclude it, and the cost of forgetting is a revoked session
-- authenticating -- the one bug in this table that is silent. Deletion leaves
-- no such state to remember: a token that is gone does not resolve, exactly like
-- a token that never existed, and a second logout is the same success as the
-- first. What is lost is "this token was revoked at T", which is an audit fact
-- and belongs in `audit_log` (architecture record §4), not in the credential
-- table; when that table arrives, logout writes there.
