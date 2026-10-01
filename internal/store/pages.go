package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/semiplane/semiplane/internal/domain"
)

// The page statements. Named constants for the reason users.go gives: a SELECT
// list and the scan list that reads it have to agree, and building both from one
// name is what makes a mismatch impossible to introduce rather than merely
// unlikely.
//
// Every statement that touches `pages_fts` appears here and nowhere else. There
// are no triggers on `pages` to maintain the index (migration 0007 explains why,
// and the constraint mapping in errors.go explains what a trigger would cost), so
// the invariant that keeps the two in step is that reindexPage is the only writer
// of `pages` and it updates the index in the same transaction. A second writer --
// a migration that bulk-loads, a repair command -- has to do the same, and
// `INSERT INTO pages_fts (pages_fts) VALUES ('rebuild')` is the way to check
// rather than assume.
const (
	// pageColumns is the row as the read side wants it. No `body_plain`: the
	// campaign listing carries paths and titles, and a thousand-page vault would
	// otherwise pull every body through a request that asked for none of them.
	pageColumns = "id, campaign_id, path, kind, title, content_hash, byte_size, created_at, updated_at"

	// prefixedPageColumns is pageColumns with every column qualified by the alias
	// the search query joins under, plus nothing else -- `c.slug` is appended by
	// the statement that needs it, so this stays the page row and only the page
	// row.
	prefixedPageColumns = "p.id, p.campaign_id, p.path, p.kind, p.title, p.content_hash," +
		" p.byte_size, p.created_at, p.updated_at"

	// The upsert writes `created_at` on insert and leaves it alone on update, so a
	// re-index after an edit keeps the row's age while moving `updated_at`. Both
	// are Unix seconds, never a time.Time: see the note in errors.go about a
	// formatted timestamp landing in an INTEGER column as TEXT.
	//
	// `body_plain` is in the SET list because the FTS index is derived from it and
	// the two are updated together or not at all.
	upsertPage = `INSERT INTO pages (campaign_id, path, kind, title, body_plain, content_hash,
		byte_size, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (campaign_id, path) DO UPDATE SET
			kind = excluded.kind,
			title = excluded.title,
			body_plain = excluded.body_plain,
			content_hash = excluded.content_hash,
			byte_size = excluded.byte_size,
			updated_at = excluded.updated_at`

	// The rowid is the FTS5 `rowid`, which is `pages.id`. An external-content
	// table cannot be updated in place: dropping terms requires the values that
	// were indexed, so the writer reads them first and hands them back here.
	insertIntoPagesFTS = "INSERT INTO pages_fts (rowid, title, body_plain) VALUES (?, ?, ?)"

	deleteFromPagesFTS = "INSERT INTO pages_fts (pages_fts, rowid, title, body_plain)" +
		" VALUES ('delete', ?, ?, ?)"

	rebuildPagesFTS = "INSERT INTO pages_fts (pages_fts) VALUES ('rebuild')"

	// What the upsert must know before it writes: the rowid, title and body the
	// index currently holds for this path.
	selectIndexedText = "SELECT id, title, body_plain FROM pages WHERE campaign_id = ? AND path = ?"

	selectPageByPath = "SELECT " + pageColumns + " FROM pages WHERE campaign_id = ? AND path = ?"

	selectPagesForCampaign = "SELECT " + pageColumns + " FROM pages WHERE campaign_id = ? ORDER BY path"

	deletePage = "DELETE FROM pages WHERE campaign_id = ? AND path = ?"

	// visibleToViewer is S-8.2, and it is a predicate rather than a filter the
	// caller may omit.
	//
	// It is the SQL spelling of `domain.Tier.CanRead`, and the two have to change
	// together: a member of the campaign reads it, and a campaign whose
	// visibility is `public` reads it, and nothing else does. `campaigns` is
	// joined rather than read from a column on `pages` for the same reason the
	// campaign list is built from a join: visibility is the tenancy's property,
	// and a copy of it on a content row would be a second answer to "who may read
	// this" that a later change updates in one place.
	//
	// The roles are parameters, not literals, so the S-2.6 vocabulary is stated by
	// `domain.Role` and nowhere else. An unrecognised role matches neither, which
	// is the same answer ResolveAccess gives it: no access, not a guess.
	//
	// An anonymous requestor arrives here as user id 0, which matches no
	// membership row -- the anonymous case is the same statement as every other
	// case rather than a variant of the query somebody has to remember to pick.
	visibleToViewer = `(c.visibility = ? OR EXISTS (
		SELECT 1 FROM campaign_members AS m
		WHERE m.campaign_id = c.id AND m.user_id = ? AND m.role IN (?, ?)))`

	// searchPages is the only search statement, and it is one statement rather
	// than a scoped and an unscoped variant. The campaign scope is a pair of
	// parameters where 0 means "every campaign the requestor may read", so there
	// is no second query text in which the visibility predicate could be left out
	// -- TestSearchPagesCannotDropTheVisibilityPredicate asserts exactly that, and
	// a variant would make the assertion a lie.
	//
	// The excerpt markers are empty, so a snippet is plain text and never markup:
	// it carries no HTML for a template to be tempted into trusting.
	searchPages = "SELECT " + prefixedPageColumns + ", c.slug," +
		" snippet(pages_fts, 1, '', '', '…', 12)" +
		" FROM pages_fts" +
		" JOIN pages AS p ON p.id = pages_fts.rowid" +
		" JOIN campaigns AS c ON c.id = p.campaign_id" +
		" WHERE pages_fts MATCH ?" +
		" AND " + visibleToViewer +
		" AND (? = 0 OR p.campaign_id = ?)" +
		" ORDER BY pages_fts.rank, p.path LIMIT ?"
)

// The search query policy. Small, generous limits, stated rather than implied,
// because a search box is attacker-reachable in exactly the way front matter is
// (S-9 puts `?q=` in a URL) and "the query was reasonable" is not a thing this
// package can check.
const (
	// defaultSearchLimit is what a caller that expressed no preference gets.
	defaultSearchLimit = 20

	// maxSearchLimit caps what any caller may ask for. A search is a full scan of
	// the matching rows with an excerpt computed for each, so an unbounded limit
	// is a request that asks for as much work as the index can be made to do.
	maxSearchLimit = 100

	// maxSearchTerms caps how many terms are ANDed together. Every extra term
	// narrows the result and widens the scan; eight is past any real query and
	// well short of a generated one.
	maxSearchTerms = 8

	// maxSearchTermBytes bounds one term. A term longer than this cannot be a
	// word anybody typed, and it is quoted whole into the MATCH expression, so it
	// is the length of a string this package builds.
	maxSearchTermBytes = 64
)

// PageSearch is one search request.
//
// A value rather than four parameters, because the four of them only mean
// anything together and the campaign scope is the one that is easy to get wrong:
// 0 means "every campaign the requestor may read", which is the cross-campaign
// search ADR 0007 describes, and a caller that meant "this campaign" has to say
// so.
type PageSearch struct {
	// Query is what the search box held. Interpreted by buildMatchQuery, which
	// quotes every term: a caller cannot reach FTS5's own syntax, so `q=` is
	// never an operator injection and never a syntax error.
	Query string
	// CampaignID scopes the search to one campaign; 0 searches every campaign the
	// requestor may read.
	CampaignID int64
	// Limit caps the result count. At or below zero means defaultSearchLimit, and
	// anything above maxSearchLimit is reduced to it.
	Limit int
}

// indexedText is what `pages_fts` currently holds for one page.
//
// Read before a write because an external-content FTS5 table cannot be told to
// forget a row's terms without being given the terms: the index is not a copy of
// the content, it is derived from it, and after the update the previous values
// are gone.
type indexedText struct {
	id        int64
	title     string
	bodyPlain string
}

// UpsertPage writes one page row and brings `pages_fts` in step with it, in one
// transaction, and returns the row as stored.
//
// It is the only writer of `pages`. `DeletePage` removes a row and its index
// entries; nothing else in this package names the table, which is the whole of
// the no-triggers invariant migration 0007 describes.
//
// The returned Page is read back rather than echoed: an upsert of an existing row
// leaves `created_at` alone, so a value built from the caller's input would
// claim the page was created now, every time it was touched. Reading the row
// costs one statement on the unique index and is the only way the returned value
// can be the value that is in the table.
func (s *Store) UpsertPage(ctx context.Context, indexed domain.PageText) (domain.Page, error) {
	if indexed.Path == "" {
		return domain.Page{}, fmt.Errorf("%w: upsert page", ErrInvalidPagePath)
	}

	// The index's own clock, not the file's mtime. The filesystem is the source of
	// truth (S-3.1) and mtime is exactly the value that must not be trusted
	// (S-5.2), so a row's timestamps say when semiplane indexed it and nothing
	// more. A caller wanting the file's mtime reads the file.
	page := indexed.Page

	if page.CreatedAt.IsZero() {
		page.CreatedAt = nowFunc()
	}

	page.CreatedAt = storedTime(page.CreatedAt)
	page.UpdatedAt = storedTime(nowFunc())

	what := "upsert page " + page.Path

	var stored domain.Page

	err := s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := reindexPage(ctx, tx, page, indexed.BodyPlain, what); err != nil {
			return err
		}

		var err error

		stored, err = scanPage(
			tx.QueryRowContext(ctx, selectPageByPath, page.CampaignID, page.Path),
			what,
		)

		return err
	})
	if err != nil {
		return domain.Page{}, err
	}

	return stored, nil
}

// reindexPage writes the page and its FTS row together, in the caller's
// transaction.
//
// The order is fixed and the reason is the external-content table: the old terms
// are dropped using the values that produced them, then `pages` is written, then
// the new values are indexed. Reversing the first two would index a document
// that is about to be replaced, and the FTS index would then hold terms for text
// no file contains -- invisible, because an external-content index answers no
// query of its own.
func reindexPage(ctx context.Context, tx *sql.Tx, page domain.Page, bodyPlain, what string) error {
	// The read's error is named apart from the write's rather than reused, so the
	// two are distinguishable and neither shadows the other.
	previous, found, readErr := readIndexedText(ctx, tx, page.CampaignID, page.Path)
	if readErr != nil {
		return readErr
	}

	if found {
		if err := unindexPage(ctx, tx, previous, what); err != nil {
			return err
		}
	}

	result, err := tx.ExecContext(ctx, upsertPage,
		page.CampaignID,
		page.Path,
		page.Kind.String(),
		page.Title,
		bodyPlain,
		page.ContentHash,
		page.ByteSize,
		unixSeconds(page.CreatedAt),
		unixSeconds(page.UpdatedAt),
	)
	if err != nil {
		return translateWrite(err, what)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return translateWrite(err, what)
	}

	// The conflict path updated an existing row and LastInsertId is then whatever
	// this connection last inserted -- stale, and possibly another page's. The
	// id we read before the write is the one the update kept.
	if found {
		id = previous.id
	}

	if _, err := tx.ExecContext(ctx, insertIntoPagesFTS, id, page.Title, bodyPlain); err != nil {
		return translateWrite(err, what)
	}

	return nil
}

// unindexPage removes one page's terms from `pages_fts`, in the caller's
// transaction.
//
// The values are passed rather than looked up because an external-content FTS5
// table cannot be told to forget a row without being given what was indexed: it
// stores a derived structure, not the text, so after the `pages` row changes there
// is nothing left to compute the old terms from. Every caller therefore has to
// have read them first, which is what `readIndexedText` is for, and this function
// being the only way to drop them is what keeps that discipline from being
// forgotten on a third path.
func unindexPage(ctx context.Context, tx *sql.Tx, text indexedText, what string) error {
	if _, err := tx.ExecContext(ctx, deleteFromPagesFTS,
		text.id, text.title, text.bodyPlain,
	); err != nil {
		return translateWrite(err, what)
	}

	return nil
}

// DeletePage removes one page row and its FTS entries.
//
// ErrNotFound when the row is not there, which a caller reconciling a tree
// should treat as success: the watcher's delete event for a file that was never
// indexed is the normal case, not a fault. It is still an error here because a
// handler that asked for a page by path needs to be able to tell "gone" from
// "did not happen", and ErrNotFound is the only way to say so.
func (s *Store) DeletePage(ctx context.Context, campaignID int64, path string) error {
	if path == "" {
		return fmt.Errorf("%w: delete page", ErrInvalidPagePath)
	}

	what := "delete page " + path

	return s.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		previous, found, err := readIndexedText(ctx, tx, campaignID, path)
		if err != nil {
			return err
		}

		if !found {
			return fmt.Errorf("%w: %s", ErrNotFound, what)
		}

		if unindex := unindexPage(ctx, tx, previous, what); unindex != nil {
			return unindex
		}

		if _, err := tx.ExecContext(ctx, deletePage, campaignID, path); err != nil {
			return translateWrite(err, what)
		}

		return nil
	})
}

// PageByPath reads one page of one campaign.
//
// ErrNotFound carries no statement about whether the page ever existed, which is
// what makes it safe to answer a request with: a private campaign and a campaign
// that does not exist have to be indistinguishable (AGENTS.md, no-access-is-404).
// The route mounts the access gate; this is not where authorisation happens
// (ADR 0024).
func (s *Store) PageByPath(
	ctx context.Context,
	campaignID int64,
	path string,
) (domain.Page, error) {
	if path == "" {
		return domain.Page{}, fmt.Errorf("%w: read page", ErrInvalidPagePath)
	}

	return scanPage(
		s.db.QueryRowContext(ctx, selectPageByPath, campaignID, path),
		"read page "+path,
	)
}

// PagesForCampaign lists one campaign's pages, ordered by path.
//
// Ordered by path because that is the order a wiki index and a broken-link report
// want, and because it is the order the unique index already yields: the listing
// is a range scan of one campaign rather than a sort over the table.
func (s *Store) PagesForCampaign(ctx context.Context, campaignID int64) ([]domain.Page, error) {
	const what = "list pages for a campaign"

	// closeRows rather than a bare `defer rows.Close()`, for the reason
	// campaigns.go gives: errcheck runs with check-blank on, and an unclosed cursor
	// on this single-connection pool blocks the next query instead of erroring.
	rows, err := s.db.QueryContext(ctx, selectPagesForCampaign, campaignID)
	if err != nil {
		return nil, translateRead(err, what)
	}

	defer closeRows(rows, what)

	pages := make([]domain.Page, 0, 16)

	for rows.Next() {
		page, err := scanPageFields(rows)
		if err != nil {
			return nil, translateRead(err, what)
		}

		pages = append(pages, page)
	}

	if err := rows.Err(); err != nil {
		return nil, translateRead(err, what)
	}

	return pages, nil
}

// SearchPages runs an FTS query and returns what the requestor may read.
//
// The visibility constraint is not a parameter of this function and cannot be
// turned off: it is in the statement, with the requestor passed in as data
// (S-8.2). There is no exported way to search without it, which is the property
// ADR 0007 asks a test to assert -- a bare `SELECT` over FTS returns private
// titles to an anonymous user, and the omission is invisible in review because
// the query still looks like a search.
//
// req decides what comes back and nothing else. is_admin raises nothing (S-2.7),
// a member of another campaign is not a member of this one, and authentication is
// required before a membership is consulted at all -- which is
// `domain.ResolveAccess`'s own order, and a test found it: a Requestor the
// middleware failed to authenticate but did populate with an id was reaching a
// private campaign's pages through the membership subquery, because the query had
// no way to ask whether the id had been proven.
func (s *Store) SearchPages(
	ctx context.Context,
	search PageSearch,
	req domain.Requestor,
) ([]domain.SearchHit, error) {
	match, err := buildMatchQuery(search.Query)
	if err != nil {
		return nil, err
	}

	// Zero is not a user, and it is what an unauthenticated requestor searches as.
	// Both facts are in one line so that the statement's parameters cannot be
	// filled in a way that skips the check: an authenticated requestor with a
	// negative id is nobody, and an unauthenticated one is nobody whatever its id.
	var userID int64

	if req.Authenticated {
		userID = max(req.UserID, int64(0))
	}

	// closeRows for the reason PagesForCampaign gives.
	rows, err := s.db.QueryContext(ctx, searchPages,
		match,
		domain.VisibilityPublic.String(),
		userID,
		domain.RoleGM.String(),
		domain.RolePlayer.String(),
		search.CampaignID,
		search.CampaignID,
		searchLimit(search.Limit),
	)
	if err != nil {
		return nil, translateRead(err, "search pages")
	}

	defer closeRows(rows, "search pages")

	hits := make([]domain.SearchHit, 0, 16)

	for rows.Next() {
		hit, err := scanSearchHit(rows)
		if err != nil {
			return nil, translateRead(err, "search pages")
		}

		hits = append(hits, hit)
	}

	if err := rows.Err(); err != nil {
		return nil, translateRead(err, "search pages")
	}

	return hits, nil
}

// searchLimit resolves a caller's requested limit against the policy.
//
// Two steps, and the order matters: a caller that expressed no preference gets the
// default rather than zero rows, and a caller that asked for more than the policy
// allows is reduced rather than refused -- a limit the caller got wrong should not
// turn a search into an error.
func searchLimit(limit int) int {
	if limit <= 0 {
		return defaultSearchLimit
	}

	return min(limit, maxSearchLimit)
}

// buildMatchQuery turns a search box's text into an FTS5 MATCH expression.
//
// Every term is quoted, and a double quote inside one is doubled, which is
// FTS5's string escape. That is the whole reason this function exists rather
// than a caller passing `q=` through: FTS5's query language is not a search box's
// language. Unquoted, `q=dragon OR secret` searches for either term and `q=title:x`
// reaches a column, and a lone `(` is a syntax error that has to become a 400
// rather than a 500. Quoted, none of those are reachable and none of them are a
// query.
//
// The last term gets a trailing `*`. ADR 0007 records why: there is no stemmer,
// so `goblins` does not find `goblin` unless the final term is a prefix, and that
// compromise is what buys the proper nouns their spelling back.
//
// Terms that contain no letter or digit are dropped. They cannot tokenise to
// anything, so keeping one would spend a scan to match nothing.
func buildMatchQuery(text string) (string, error) {
	terms := usableTerms(text)
	if len(terms) == 0 {
		return "", fmt.Errorf("%w: %q", ErrInvalidSearchQuery, strings.TrimSpace(text))
	}

	quoted := make([]string, 0, len(terms))

	for _, term := range terms {
		quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}

	last := len(quoted) - 1
	quoted[last] += "*"

	return strings.Join(quoted, " "), nil
}

// usableTerms splits a query into the terms worth indexing, within the policy.
//
// Refusing rather than truncating: silently dropping the ninth word of a query is
// a search that answers a different question from the one asked, and the caller
// cannot tell from the results.
func usableTerms(text string) []string {
	fields := strings.Fields(text)

	if len(fields) > maxSearchTerms {
		fields = fields[:maxSearchTerms]
	}

	terms := make([]string, 0, len(fields))

	for _, field := range fields {
		if len(field) > maxSearchTermBytes {
			// Dropped rather than refused, and this is the one place truncation is
			// right: an over-long "term" is a pasted URL or a base64 blob, and
			// keeping its first 64 characters would search for a prefix of
			// something the author did not ask for. Dropping it leaves the rest of
			// the query intact.
			continue
		}

		if strings.ContainsFunc(field, isWordRune) {
			terms = append(terms, field)
		}
	}

	return terms
}

// isWordRune reports whether r could be part of a search term.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// readIndexedText reads what the FTS index holds for one page.
//
// (zero, false, nil) when there is no row: that is the ordinary state for a page
// being indexed for the first time, and it is reported rather than raised because
// the caller has two different things to do about it.
func readIndexedText(
	ctx context.Context,
	tx *sql.Tx,
	campaignID int64,
	path string,
) (indexedText, bool, error) {
	var text indexedText

	what := "read indexed text for " + path

	err := tx.QueryRowContext(ctx, selectIndexedText, campaignID, path).Scan(
		&text.id,
		&text.title,
		&text.bodyPlain,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return indexedText{}, false, nil
		}

		return indexedText{}, false, translateRead(err, what)
	}

	return text, true, nil
}

// scanPage reads one page row and maps a failure onto this package's vocabulary.
func scanPage(row *sql.Row, what string) (domain.Page, error) {
	page, err := scanPageFields(row)
	if err != nil {
		return domain.Page{}, translateRead(err, what)
	}

	return page, nil
}

// scanPageFields reads one page row, from a QueryRow or from an open cursor.
//
// One function for both, for the reason scanCampaignFields gives: two copies of a
// ten-column scan is two places for a column to be added to one and forgotten in
// the other.
//
// The kind is stored verbatim rather than validated. It is the *honoured* kind, so
// it is `prose` or something the registry had at the time of the write; whether
// this build still recognises it is the registry's question, asked where the page
// is rendered rather than here. This mirrors `campaigns.system_id`, where an
// unregistered value refuses the game and still serves the wiki (S-14.8).
func scanPageFields(row rowScanner) (domain.Page, error) {
	var (
		page      domain.Page
		kind      string
		createdAt int64
		updatedAt int64
	)

	if err := row.Scan(
		&page.ID,
		&page.CampaignID,
		&page.Path,
		&kind,
		&page.Title,
		&page.ContentHash,
		&page.ByteSize,
		&createdAt,
		&updatedAt,
	); err != nil {
		return domain.Page{}, fmt.Errorf("scan page row: %w", err)
	}

	page.Kind = domain.PageKind(kind)
	page.CreatedAt = unixTime(createdAt)
	page.UpdatedAt = unixTime(updatedAt)

	return page, nil
}

// scanSearchHit reads one result row.
//
// The snippet comes last because it is the only column not present in pageColumns,
// and the campaign slug before it because that is what makes a cross-campaign
// result usable (ADR 0007).
func scanSearchHit(row rowScanner) (domain.SearchHit, error) {
	var (
		hit       domain.SearchHit
		kind      string
		createdAt int64
		updatedAt int64
	)

	if err := row.Scan(
		&hit.ID,
		&hit.CampaignID,
		&hit.Path,
		&kind,
		&hit.Title,
		&hit.ContentHash,
		&hit.ByteSize,
		&createdAt,
		&updatedAt,
		&hit.CampaignSlug,
		&hit.Snippet,
	); err != nil {
		return domain.SearchHit{}, fmt.Errorf("scan search hit row: %w", err)
	}

	hit.Kind = domain.PageKind(kind)
	hit.CreatedAt = unixTime(createdAt)
	hit.UpdatedAt = unixTime(updatedAt)

	return hit, nil
}
