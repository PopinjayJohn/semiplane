package edit

// The revision log: the append-only record of what this editor has published.
//
// S-6.4 makes a successful write append a `page_revisions` row, and S-6.2 makes a
// *refused* write append none — so this file is the one place in the request path
// that writes to the database, and its two halves are the whole of the conflict
// contract's storage side. The refusal half is not a flag on this type; it is the
// absence of a call, which is why `Append` is reached from exactly one branch of
// the save path and why the test for the §14 row asserts the *table* rather than
// a call count.
//
// # Why the statement lives here and not in `internal/store`
//
// Two reasons, and the second is the one that matters. The first is path
// ownership: `internal/store/*.go` belongs to another work item, and AGENTS.md's
// first rule for parallel work is that a work item touches only the paths it
// declares. The second is that a *narrow interface with a real implementation*
// is what makes the phase's own requirement testable: S-14's row is "a stale
// If-Match gives 412, the disk is unchanged, and no revision row is written", and
// a test can only assert the third clause against a table. A test double would
// assert that a spy was not called, which is a different statement about a
// different thing — it passes just as happily against a row written by some other
// path, a trigger, or a statement this test never exercised.
//
// The write itself goes through the store's writer queue, never through
// `Store.DB()`: that handle is documented for reads, and a request goroutine
// writing on it bypasses the single-writer queue whose whole job is to stop two
// writers contending for SQLite's lock. `Writer` is the seam, and it is a
// *function type* rather than an interface because `store.Store.Write` takes an
// unexported `writeFunc` — an interface method would need that exact named type
// and no other package can name it, whereas a closure in the composition root
// converts one to the other implicitly. `NewRevisionLog` documents the one line
// the composition root needs.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Source is who performed a write, and it is the column `page_revisions` CHECKs.
//
// A named type rather than a bare string because the CHECK in migration 0008 is
// the schema's half of this contract and a `string` would let a caller hand it
// anything. Three values, all of them this project's decisions, and each one names
// a writer that exists today or is specified to: `web` is this editor, `obsidian`
// is a change a sync client brought, `system` is a write semiplane performed on a
// GM's behalf (architecture §7.4's "export to journal" is the first).
type Source string

const (
	// SourceWeb is a write from the web editor, and the only value this route
	// writes. Fixed rather than derived from a request header, so a client cannot
	// claim a write happened as something else.
	SourceWeb Source = "web"
	// SourceObsidian is a change that arrived through the vault.
	SourceObsidian Source = "obsidian"
	// SourceSystem is a write semiplane performed on a GM's behalf.
	SourceSystem Source = "system"
)

// valid reports whether s is one of the three the schema accepts.
func (s Source) valid() bool {
	switch s {
	case SourceWeb, SourceObsidian, SourceSystem:
		return true
	default:
		return false
	}
}

// ErrInvalidRevision is a revision this package refuses to write.
var ErrInvalidRevision = errors.New("edit: invalid page revision")

// Revision is one row of `page_revisions`: what a page was published as.
type Revision struct {
	// CampaignID is the campaign whose root the path is relative to. The tenancy
	// key, and the column the foreign key enforces — a revision for a campaign
	// that does not exist is a row nothing can ever read.
	CampaignID int64
	// Path is the page's root-relative path, spelled as `pages.path` spells it.
	Path string
	// Content is the whole source as it was written, unredacted. See migration
	// 0008 for why there is no redacted variant and why that is not a leak: the
	// row is readable only through this GM-only route, and a history with the
	// secrets edited out cannot explain the file it is a history of.
	Content string
	// AuthorID is the account that performed the write, or 0 when there was none —
	// a change a sync client brought has no semiplane account behind it, which is
	// why the column is nullable (migration 0008).
	AuthorID int64
	// Source is who performed it. See `Source`.
	Source Source
}

// Revisions is the append-only log, as the route needs it.
//
// An interface so the route depends on the *capability* rather than on the SQL,
// and so a test can hand the save path a log that fails — the 500 branch is
// otherwise unreachable from a test, and an unreachable branch is an unverified
// one. `*RevisionLog` satisfies it, and so does a test's recorder.
//
// One method, and deliberately not a read: nothing in this phase reads history.
// §4.4's revision list in the editor's rail needs a read, and adding one here
// rather than at the point it is needed keeps the interface to the question the
// write path actually asks.
type Revisions interface {
	// Append records one published revision. It returns an error rather than
	// swallowing one because the caller has already changed the world by the time
	// it is called — see the ordering note on the save path.
	Append(ctx context.Context, revision Revision) error
}

// appendRevision is the one statement that writes `page_revisions`.
//
// Named rather than inlined at the call site for the reason `store/pages.go`
// names its statements: a column list and the arguments filling it have to agree,
// and one name is what makes a mismatch impossible to introduce rather than
// merely unlikely.
//
// The column order is the migration's. `created_at` is filled from Go rather than
// from SQLite's clock, and the reason is `store/migrate.go`'s: the timestamps in
// this schema are Unix seconds written as integers, and a value produced by the
// driver or by `unixepoch()` is a second answer to the same question with a
// different type. One derivation, in Go, is one answer.
const appendRevision = `INSERT INTO page_revisions
	(campaign_id, path, content, author_id, source, created_at)
	VALUES (?, ?, ?, ?, ?, ?)`

// Writer runs fn inside a transaction and returns after it commits.
//
// A function type rather than an interface method, and the reason is mechanical
// rather than stylistic: `store.Store.Write` takes an unexported `writeFunc`, and
// Go requires an interface method's parameter types to be *identical* rather than
// merely assignable. There is therefore no interface `*store.Store` can satisfy
// that expresses this, and a closure in the composition root converts one to the
// other without naming either. The queue it reaches is the only sanctioned write
// path in the project; see the file comment for why bypassing it is not on the
// table.
type Writer func(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error

// RevisionLog is the `Revisions` implementation over a campaign's page history.
//
// A value rather than a pointer-to-interface, and it holds no connection of its
// own: the writer seam is the store's queue, so a log constructed at startup
// costs nothing and there is nothing to close.
type RevisionLog struct {
	write Writer
}

// NewRevisionLog returns a log that writes through the given seam.
//
// The composition root's one line, and the conversion is the whole of the trick:
//
//	edit.NewRevisionLog(func(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
//		return backing.Write(ctx, fn)
//	})
//
// A nil `write` is tolerated and refuses every append, for the reason
// `wiki.Handler.Logger` is: a misconfigured instance should fail a save loudly at
// the first attempt rather than nil-panic on it, and a store-less router has
// nothing to record history in.
func NewRevisionLog(write Writer) *RevisionLog {
	return &RevisionLog{write: write}
}

// Append writes one revision row, and returns the first failure it meets.
//
// The validation is here rather than at the call site because the two fields that
// can be wrong are the two the schema cannot catch: `campaign_id` of 0 fails the
// foreign key only if foreign keys are enforced (they are, but that is a pragma
// rather than a type), and an unknown `source` is a CHECK failure whose message is
// a SQLITE_CONSTRAINT string. Both are bugs here rather than conditions, so both
// are refused with a wrapped sentinel a caller can test for — and neither reaches
// the statement.
//
// The row is written even when the file write that follows it fails, and that is
// the architecture record's order rather than an oversight. §6.1 puts the append
// before the write, and the argument for it is which of the two failures is
// recoverable: a revision row for content that did not land is noise a later save
// duplicates, while a file that landed with no row is a hole in the one part of
// this project's state that the vault cannot rebuild (ADR 0008). The cost is
// stated at the call site too, because a reader deciding to reverse the order
// deserves to know what they are trading.
func (l *RevisionLog) Append(ctx context.Context, revision Revision) error {
	if err := revision.validate(); err != nil {
		return err
	}

	if l == nil || l.write == nil {
		return fmt.Errorf("%w: no revision log is configured", ErrInvalidRevision)
	}

	// NULL rather than 0 for a write with no account behind it: `users.id` is
	// 1-based, so 0 is not an account, and a foreign key pointing at it would fail
	// rather than record "nobody" (migration 0008).
	author := sql.NullInt64{Int64: revision.AuthorID, Valid: revision.AuthorID > 0}

	if err := l.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, appendRevision,
			revision.CampaignID,
			revision.Path,
			revision.Content,
			author,
			string(revision.Source),
			time.Now().Unix(),
		); err != nil {
			return fmt.Errorf("write the revision row: %w", err)
		}

		return nil
	}); err != nil {
		return fmt.Errorf("append page revision for %s: %w", revision.Path, err)
	}

	return nil
}

// validate refuses a revision that is not a fact about a page.
func (r Revision) validate() error {
	switch {
	case r.CampaignID <= 0:
		return fmt.Errorf("%w: campaign id %d", ErrInvalidRevision, r.CampaignID)
	case r.Path == "":
		return fmt.Errorf("%w: empty path", ErrInvalidRevision)
	case !r.Source.valid():
		return fmt.Errorf("%w: source %q", ErrInvalidRevision, string(r.Source))
	default:
		return nil
	}
}
