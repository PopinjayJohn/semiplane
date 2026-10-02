package edit_test

// The revision log against the shipped schema.
//
// These tests exist because S-14's row for this phase says "no revision row is
// written", and only a table can be asked that question. The database here is opened
// with `sql.Open` and migrated with `store.Migrate` — the shipped runner, the shipped
// migration set — so the schema under test is the schema that ships, and a change to
// migration 0008 that broke the INSERT would fail here rather than in production.

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/semiplane/semiplane/internal/httpapi/edit"
)

// TestTheRevisionLogRecordsWhatWasPublished is the row's own contract: the whole
// source, the writer, the campaign, and a timestamp in the schema's own units.
//
// The timestamp is asserted as an integer rather than as a `time.Time`, because that
// is the migration's stated rule (`migrate.go` explains the failure: the driver
// renders a `time.Time` as a formatted string and SQLite stores that in an
// INTEGER-declared column as TEXT). A test that compared a `time.Time` would pass
// against a row holding text, because the driver converts on the way out too.
func TestTheRevisionLogRecordsWhatWasPublished(t *testing.T) {
	t.Parallel()

	db, log := newDatabase(t, t.TempDir())

	if err := log.Append(t.Context(), edit.Revision{
		CampaignID: testCampID,
		Path:       "Vault.md",
		Content:    "---\ntitle: A page\n---\nIron and rust.\n",
		AuthorID:   gmUser,
		Source:     edit.SourceWeb,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	var (
		content   string
		author    sql.NullInt64
		source    string
		createdAt int64
		path      string
	)

	row := db.QueryRowContext(t.Context(),
		"SELECT path, content, author_id, source, created_at FROM page_revisions")
	if err := row.Scan(&path, &content, &author, &source, &createdAt); err != nil {
		t.Fatalf("read the revision: %v", err)
	}

	if path != "Vault.md" {
		t.Errorf("path = %q, want %q", path, "Vault.md")
	}

	if content != "---\ntitle: A page\n---\nIron and rust.\n" {
		t.Errorf("content = %q, want the whole source", content)
	}

	if !author.Valid || author.Int64 != gmUser {
		t.Errorf("author_id = %+v, want the GM (%d)", author, gmUser)
	}

	if source != "web" {
		t.Errorf("source = %q, want %q (S-6.4)", source, "web")
	}

	if createdAt <= 0 {
		t.Errorf("created_at = %d, want a Unix second count; a zero or negative value "+
			"is the TEXT-in-an-INTEGER-column failure `migrate.go` documents", createdAt)
	}
}

// TestARevisionWithNoAuthorIsStoredAsNull is the `obsidian` case from the
// architecture record: a change a sync client brought has no semiplane account behind
// it, and the column is nullable for exactly that.
//
// Asserted rather than assumed, because the alternative implementations are
// indistinguishable from this side in every other test: writing `0` instead of NULL
// would satisfy a scan into an `int64` and fail the foreign key, and writing the
// *route's* own user id would be a plausible bug that no other test here would see.
func TestARevisionWithNoAuthorIsStoredAsNull(t *testing.T) {
	t.Parallel()

	db, log := newDatabase(t, t.TempDir())

	if err := log.Append(t.Context(), edit.Revision{
		CampaignID: testCampID,
		Path:       "Vault.md",
		Content:    "changed by a sync client\n",
		Source:     edit.SourceObsidian,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	var author sql.NullInt64

	if err := db.QueryRowContext(t.Context(),
		"SELECT author_id FROM page_revisions").Scan(&author); err != nil {
		t.Fatalf("read the revision: %v", err)
	}

	if author.Valid {
		t.Errorf("author_id = %+v, want NULL; a change with no account behind it must "+
			"not borrow the writer's", author)
	}
}

// TestARevisionSurvivesTheAccountBeingDeleted is the foreign key's `ON DELETE SET
// NULL` doing its job, and the reason it is in the migration.
//
// A revision is a statement about a page, not about a person. An operator who
// deletes an account has not retracted what was published under it, and a cascade
// would take the history with it — and the history is the one part of this project's
// state the vault cannot rebuild (ADR 0008).
func TestARevisionSurvivesTheAccountBeingDeleted(t *testing.T) {
	t.Parallel()

	db, log := newDatabase(t, t.TempDir())

	if err := log.Append(t.Context(), edit.Revision{
		CampaignID: testCampID,
		Path:       "Vault.md",
		Content:    "published by the gm\n",
		AuthorID:   gmUser,
		Source:     edit.SourceWeb,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if _, err := db.ExecContext(t.Context(), "DELETE FROM users WHERE id = ?",
		gmUser); err != nil {
		t.Fatalf("delete the account: %v", err)
	}

	var (
		rows   int
		author sql.NullInt64
	)

	if err := db.QueryRowContext(t.Context(),
		"SELECT COUNT(*), MIN(author_id) FROM page_revisions").Scan(&rows, &author); err != nil {
		t.Fatalf("read the revisions: %v", err)
	}

	if rows != 1 {
		t.Errorf("%d revisions remain, want 1; deleting an account must not delete a "+
			"page's history (ADR 0008)", rows)
	}

	if author.Valid {
		t.Errorf("author_id = %+v, want NULL after the account was deleted", author)
	}
}

// TestTheRevisionLogRefusesWhatItCannotStore is the validation, as a table.
//
// Both halves matter and they fail differently. A bad `source` is refused here with a
// wrapped sentinel, and it would *also* be refused by the schema's CHECK — so the test
// is that the refusal arrives as an `ErrInvalidRevision` a caller can test for rather
// than as a `SQLITE_CONSTRAINT` string. A zero campaign id is refused here and would
// otherwise be refused by the foreign key, if foreign keys were enabled; they are, but
// that is a pragma rather than a type, and "it happens to be enforced today" is not a
// validation.
func TestTheRevisionLogRefusesWhatItCannotStore(t *testing.T) {
	t.Parallel()

	_, log := newDatabase(t, t.TempDir())

	for _, testCase := range []struct {
		name     string
		revision edit.Revision
	}{
		{
			name:     "no campaign",
			revision: edit.Revision{Path: "Vault.md", Source: edit.SourceWeb},
		},
		{
			name:     "no path",
			revision: edit.Revision{CampaignID: testCampID, Source: edit.SourceWeb},
		},
		{
			name: "an unknown source",
			revision: edit.Revision{
				CampaignID: testCampID,
				Path:       "Vault.md",
				Source:     edit.Source("from a header"),
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := log.Append(t.Context(), testCase.revision)
			if !errors.Is(err, edit.ErrInvalidRevision) {
				t.Errorf("Append error = %v, want one wrapping ErrInvalidRevision", err)
			}
		})
	}
}

// TestARevisionLogWithNoWriterFailsLoudly: a log with no store behind it refuses
// rather than dropping the write on the floor.
//
// A nil-tolerant log is the right shape for a router built without a database — and
// the wrong shape for a *save*, which is the one operation where a silent success is
// data loss. So the append fails and the route answers 500 rather than reporting a
// save that no history records.
func TestARevisionLogWithNoWriterFailsLoudly(t *testing.T) {
	t.Parallel()

	log := edit.NewRevisionLog(nil)

	err := log.Append(t.Context(), edit.Revision{
		CampaignID: testCampID,
		Path:       "Vault.md",
		Source:     edit.SourceWeb,
	})
	if !errors.Is(err, edit.ErrInvalidRevision) {
		t.Errorf("Append error = %v, want one wrapping ErrInvalidRevision", err)
	}
}

// TestTheSchemaRefusesAnUnknownSource is the migration's CHECK, asserted through raw
// SQL so it is the *database* refusing and not the log's validation.
//
// The two are different properties and only this one survives a second writer. A
// repair command, a future phase, or a hand-edited database all bypass
// `RevisionLog.Append`, and the CHECK is what says "there are three sources and no
// more" to all of them.
func TestTheSchemaRefusesAnUnknownSource(t *testing.T) {
	t.Parallel()

	db, _ := newDatabase(t, t.TempDir())

	_, err := db.ExecContext(context.Background(),
		"INSERT INTO page_revisions"+
			" (campaign_id, path, content, author_id, source, created_at)"+
			" VALUES (?, ?, '', NULL, 'from-a-header', 0)",
		testCampID, "Vault.md")
	if err == nil {
		t.Error("the schema accepted a source outside 'web' | 'obsidian' | 'system'; " +
			"migration 0008's CHECK is what every other writer depends on")
	}
}
