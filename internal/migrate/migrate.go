package migrate

import (
	"database/sql"
	_ "embed"
	"log/slog"
)

//go:embed schema.sql
var schemaSQL string

// currentVersion is the user-data schema version seeded by schema.sql.
const currentVersion = 1

// Apply executes the initial schema (schema.sql). It is idempotent and safe
// to run on every open: all statements use CREATE ... IF NOT EXISTS /
// DROP ... IF EXISTS / INSERT OR IGNORE.
func Apply(db *sql.DB) error {
	if _, err := db.Exec(schemaSQL); err != nil {
		return err
	}
	var version int
	if err := db.QueryRow(`SELECT version FROM user_version WHERE id = 1`).Scan(&version); err != nil {
		return err
	}
	slog.Info("migrate: schema applied", "user_version", version, "target", currentVersion)
	return nil
}
