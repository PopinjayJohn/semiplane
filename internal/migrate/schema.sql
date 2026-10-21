-- semiplane initial schema (derived index, rebuildable via reindex).
--
-- Vault migrations are up-only and idempotent: every statement is safe to
-- re-run (CREATE TABLE IF NOT EXISTS / CREATE TRIGGER guarded by
-- DROP TRIGGER IF EXISTS / INSERT OR IGNORE for seed rows).
--
-- Design notes:
--   * notes is the content cache; the vault files on disk are canonical.
--     The index can be fully rebuilt by rescanning the vault (reindex).
--   * notes_fts is a derived FTS5 index over notes, kept in sync by the
--     notes_ai / notes_ad / notes_au triggers (delete+insert pattern).
--   * note_facets is a derived facet index over frontmatter namespaces. It
--     stores the JSON value of a namespace (currently only `semiplane:`) so
--     note consumers can discover notes by key (e.g. semiplane.class)
--     without reading every note. Like the other derived tables it is
--     rebuilt from content by code (upsert/delete/reindex).
--   * backlinks is a derived edge index (src -> dst note links). It carries
--     NO foreign keys to notes on purpose: edges are rebuilt from content
--     by code, and rows for deleted notes are cleaned up by code (delete
--     edges where src/dst no longer exists) so reindex/repair never trips
--     on FK violations mid-rebuild.
--   * All timestamps are INTEGER unix seconds.
--   * user_version is a single-row table tracking the user-data schema
--     version (enforced by id INTEGER PRIMARY KEY CHECK (id = 1)).

CREATE TABLE IF NOT EXISTS notes (
  path    TEXT PRIMARY KEY,
  content TEXT NOT NULL,
  hash    TEXT NOT NULL,
  mtime   INTEGER NOT NULL,
  title   TEXT,
  tags    TEXT NOT NULL DEFAULT '[]'
);

-- Derived facet rows: one per (note, frontmatter namespace) carrying data.
-- data is the namespace value encoded as JSON; queries test key presence
-- with SQLite's built-in JSON functions (json_type(data, '$.some.key')).
CREATE TABLE IF NOT EXISTS note_facets (
  path      TEXT NOT NULL,
  namespace TEXT NOT NULL,
  data      TEXT NOT NULL,
  PRIMARY KEY (path, namespace)
);
CREATE INDEX IF NOT EXISTS idx_note_facets_namespace ON note_facets(namespace);

CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
  path,
  title,
  content,
  tokenize='porter'
);

DROP TRIGGER IF EXISTS notes_ai;
CREATE TRIGGER notes_ai AFTER INSERT ON notes BEGIN
  INSERT INTO notes_fts (path, title, content) VALUES (new.path, new.title, new.content);
END;

DROP TRIGGER IF EXISTS notes_ad;
CREATE TRIGGER notes_ad AFTER DELETE ON notes BEGIN
  DELETE FROM notes_fts WHERE path = old.path;
END;

DROP TRIGGER IF EXISTS notes_au;
CREATE TRIGGER notes_au AFTER UPDATE ON notes BEGIN
  DELETE FROM notes_fts WHERE path = old.path;
  INSERT INTO notes_fts (path, title, content) VALUES (new.path, new.title, new.content);
END;

CREATE TABLE IF NOT EXISTS backlinks (
  src TEXT NOT NULL,
  dst TEXT NOT NULL,
  PRIMARY KEY (src, dst)
);
CREATE INDEX IF NOT EXISTS idx_backlinks_dst ON backlinks(dst);
CREATE INDEX IF NOT EXISTS idx_backlinks_src ON backlinks(src);

CREATE TABLE IF NOT EXISTS sessions (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  hash       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS tokens (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL,
  scope      TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  hash       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tokens_user_id ON tokens(user_id);

CREATE TABLE IF NOT EXISTS lan_codes (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used       INTEGER NOT NULL DEFAULT 0,
  hash       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_lan_codes_user_id ON lan_codes(user_id);
CREATE INDEX IF NOT EXISTS idx_lan_codes_expires_at ON lan_codes(expires_at);

CREATE TABLE IF NOT EXISTS oidc_states (
  id         TEXT PRIMARY KEY,
  state      TEXT NOT NULL,
  nonce      TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_oidc_states_expires_at ON oidc_states(expires_at);

CREATE TABLE IF NOT EXISTS acls (
  path_prefix TEXT PRIMARY KEY,
  role        TEXT NOT NULL,
  user_id     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_acls_user_id ON acls(user_id);

CREATE TABLE IF NOT EXISTS users (
  id         TEXT PRIMARY KEY,
  role       TEXT NOT NULL DEFAULT 'player',
  username   TEXT NOT NULL UNIQUE,
  phc        TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS user_version (
  id      INTEGER PRIMARY KEY CHECK (id = 1),
  version INTEGER NOT NULL
);
INSERT OR IGNORE INTO user_version (id, version) VALUES (1, 1);
