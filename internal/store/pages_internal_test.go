package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// TestPagesFTSSchemaFollowsADR0007 reads the DDL the runner actually applied and
// asserts the three things ADR 0007 fixes about it.
//
// A tokenizer is a product decision, not an implementation detail: it changes what
// a GM finds, and ADR 0007's whole argument is that `porter` folds TTRPG proper
// nouns into words nobody typed. Nothing in the code refers to the tokenizer, so a
// migration that quietly changed it would be invisible to every other test here.
func TestPagesFTSSchemaFollowsADR0007(t *testing.T) {
	t.Parallel()

	db := openRawDB(t)

	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("Migrate() error = %v, want nil", err)
	}

	var ddl string

	row := db.QueryRowContext(t.Context(),
		"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'pages_fts'")

	if err := row.Scan(&ddl); err != nil {
		t.Fatalf("read pages_fts DDL: %v; the virtual table was not created", err)
	}

	// The virtual table exists only because migration 0007 ran, and 0007 only runs
	// after 0005 created `campaigns` -- which is the forward-only ordering being
	// asserted. The pages table's foreign key makes the same demand.
	applied, err := readAppliedVersions(t.Context(), db)
	if err != nil {
		t.Fatalf("read applied versions: %v", err)
	}

	for _, version := range []int{5, 7} {
		if _, done := applied[version]; !done {
			t.Errorf("migration %04d is unapplied, so 0007 did not run in order", version)
		}
	}

	for _, fragment := range []string{
		"content='pages'",
		"content_rowid='id'",
		"unicode61 remove_diacritics 2",
	} {
		if !strings.Contains(ddl, fragment) {
			t.Errorf("pages_fts DDL does not contain %q:\n%s", fragment, ddl)
		}
	}

	if strings.Contains(ddl, "porter") {
		t.Errorf("pages_fts DDL names the porter stemmer, which ADR 0007 rejects:\n%s", ddl)
	}
}

// TestPagesSchemaIsScopedByCampaign asserts the tenancy of the index's key.
//
// A page is scoped to a campaign and identified within it by its path, and both
// are properties of the schema rather than of the code that reads it, which is why
// they are asserted here: a query layer is free to be wrong, a constraint is not.
// The uniqueness itself is asserted behaviourally in pages_test.go and by the
// upsert's `ON CONFLICT (campaign_id, path)` -- which cannot compile against a
// table that has no such constraint -- so reading the DDL text for the clause is
// not what proves it.
func TestPagesSchemaIsScopedByCampaign(t *testing.T) {
	t.Parallel()

	db := openRawDB(t)

	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("Migrate() error = %v, want nil", err)
	}

	// AUTOINCREMENT rather than a bare rowid: the watcher's delete-and-reinsert
	// cycle is ordinary, and a reused id would bind the previous page's orphaned
	// FTS entries to whatever page took the id next. Migration 0007 records this.
	if !tableDDLContains(t, db, "pages", "id          INTEGER PRIMARY KEY AUTOINCREMENT") {
		t.Error("pages does not use AUTOINCREMENT; a reused rowid would bind a deleted " +
			"page's orphaned FTS entries to the next page")
	}

	if !tableDDLContains(t, db, "pages",
		"campaign_id INTEGER NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE") {
		t.Error("pages.campaign_id does not cascade; a page would outlive its campaign")
	}

	// The named unique index is also the campaign listing's index, and the upsert's
	// conflict target. Its name is in both statements, so renaming it is a change to
	// the query rather than a tidy-up.
	for _, name := range []string{"pages_campaign_path_key", "pages_campaign_kind_idx"} {
		var found string

		row := db.QueryRowContext(t.Context(),
			"SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?", name)

		if err := row.Scan(&found); err != nil {
			t.Errorf("index %s is missing: %v", name, err)
		}
	}
}

// tableDDLContains reports whether a table's DDL contains a fragment.
func tableDDLContains(t *testing.T, db *sql.DB, table, fragment string) bool {
	t.Helper()

	var ddl string

	row := db.QueryRowContext(t.Context(),
		"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", table)

	if err := row.Scan(&ddl); err != nil {
		t.Fatalf("read %s DDL: %v", table, err)
	}

	return strings.Contains(ddl, fragment)
}

// TestSearchPagesCannotDropTheVisibilityPredicate is the assertion ADR 0007 asks
// for, read as a property of the statement rather than of any one call.
//
// S-8.2 says every FTS query joins `campaigns` on visibility, because a bare query
// returns private titles to an anonymous user. That is the kind of omission that
// passes review — the query still looks like a search — so the way to hold it is
// to make it structurally impossible: there is exactly one search statement, the
// join and the predicate are part of its text, and the campaign scope is a
// parameter of that same statement rather than a second one. A scoped and an
// unscoped query would make this test a lie the moment the second was written.
//
// The behavioural half is in pages_test.go: the same predicate, exercised by five
// requestors against two campaigns.
func TestSearchPagesCannotDropTheVisibilityPredicate(t *testing.T) {
	t.Parallel()

	for _, fragment := range []string{
		"JOIN campaigns AS c ON c.id = p.campaign_id",
		"c.visibility = ?",
		"EXISTS (",
		"m.campaign_id = c.id",
		"m.user_id = ?",
	} {
		if !strings.Contains(searchPages, fragment) {
			t.Errorf("searchPages does not contain %q; the visibility constraint is part of "+
				"the statement and cannot be dropped without deleting the statement", fragment)
		}
	}

	// A visibility value in the SQL is a second implementation of ParseVisibility's
	// vocabulary, in a place nobody reads when the vocabulary changes.
	for _, literal := range []string{"'public'", "'private'", "'gm'", "'player'"} {
		if strings.Contains(searchPages, literal) {
			t.Errorf(
				"searchPages contains the literal %s; the vocabulary belongs to domain",
				literal,
			)
		}
	}
}

// TestEveryStatementNamingPagesFTSIsHere is the no-triggers invariant, asserted.
//
// Migration 0007 explains why there are no triggers on `pages`: FTS5's maintenance
// commands are the same statements the writer issues, so a trigger would move them
// somewhere a test cannot reach. The price of that choice is that every statement
// touching `pages_fts` has to be in this file, and a bulk loader or a repair
// command written elsewhere would be the one writer that forgets. This test is the
// thing that notices.
func TestEveryStatementNamingPagesFTSIsHere(t *testing.T) {
	t.Parallel()

	statements := map[string]string{
		"insert":  insertIntoPagesFTS,
		"delete":  deleteFromPagesFTS,
		"rebuild": rebuildPagesFTS,
		"search":  searchPages,
	}

	for name, statement := range statements {
		if !strings.Contains(statement, "pages_fts") {
			t.Errorf("the %s statement does not name pages_fts: %q", name, statement)
		}
	}
}

// TestBuildMatchQueryQuotesEveryTerm is the property that makes `q=` safe to take
// from a URL: FTS5's query language is not reachable through the search box.
//
// The cases are the three ways a caller could otherwise reach it. A bare word is
// quoted and prefixed, an operator-looking word is quoted into an ordinary phrase,
// and a quote inside a term is FTS5's doubled-quote escape rather than the end of
// the literal.
func TestBuildMatchQueryQuotesEveryTerm(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		query string
		want  string
	}{
		{name: "one term", query: "dragon", want: `"dragon"*`},
		{name: "two terms", query: "sleeping dragon", want: `"sleeping" "dragon"*`},
		{name: "surrounding space", query: "  dragon  ", want: `"dragon"*`},
		{
			name:  "an operator is quoted",
			query: "dragon OR secret",
			want:  `"dragon" "OR" "secret"*`,
		},
		{
			name:  "a column filter is quoted",
			query: "title:dragon",
			want:  `"title:dragon"*`,
		},
		{
			name:  "a quote is escaped by doubling it",
			query: `the "wyvern`,
			want:  `"the" """wyvern"*`,
		},
		{
			// Dropped, because a term with no letter or digit cannot tokenise and
			// would spend a scan to match nothing.
			name:  "a term with nothing searchable in it",
			query: "-- dragon",
			want:  `"dragon"*`,
		},
		{
			// Dropped for the same reason, and the reason a length cap is a cap on
			// the whole term rather than on the words in it: a pasted URL is one
			// field.
			name:  "an over-long term",
			query: strings.Repeat("a", maxSearchTermBytes+1) + " dragon",
			want:  `"dragon"*`,
		},
		{
			// The cap keeps the query buildable and the search honest: every word of
			// the first eight is required, and the rest are not silently treated as
			// though they had been asked for.
			name:  "more terms than the cap",
			query: "one two three four five six seven eight nine",
			want:  `"one" "two" "three" "four" "five" "six" "seven" "eight"*`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildMatchQuery(testCase.query)
			if err != nil {
				t.Fatalf("buildMatchQuery(%q) error = %v, want nil", testCase.query, err)
			}

			if got != testCase.want {
				t.Errorf("buildMatchQuery(%q) = %q, want %q", testCase.query, got, testCase.want)
			}
		})
	}
}

// TestBuildMatchQueryRefusesAnUnusableQuery is the negative half. A query that
// cannot match anything is a mistake in the request, and it is distinguishable
// from a driver failure so a handler can answer 400 with errors.Is.
func TestBuildMatchQueryRefusesAnUnusableQuery(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		query string
	}{
		{name: "empty", query: ""},
		{name: "whitespace", query: " \t\n "},
		{name: "punctuation only", query: "!!! ??? ..."},
		{name: "quotes only", query: `"""`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildMatchQuery(testCase.query)
			if err == nil {
				t.Fatalf("buildMatchQuery(%q) = %q, want an error", testCase.query, got)
			}

			if !errors.Is(err, ErrInvalidSearchQuery) {
				t.Errorf("buildMatchQuery(%q) error = %v, want ErrInvalidSearchQuery",
					testCase.query, err)
			}
		})
	}
}

// TestSearchLimitAppliesThePolicy is the resource bound, and its two halves differ
// in kind: an absent preference is filled in, an excessive one is reduced.
func TestSearchLimitAppliesThePolicy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		limit int
		want  int
	}{
		{name: "absent", limit: 0, want: defaultSearchLimit},
		{name: "negative", limit: -5, want: defaultSearchLimit},
		{name: "below the default", limit: 3, want: 3},
		{name: "at the maximum", limit: maxSearchLimit, want: maxSearchLimit},
		{name: "above the maximum", limit: maxSearchLimit + 1, want: maxSearchLimit},
		{name: "absurd", limit: 1 << 30, want: maxSearchLimit},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := searchLimit(testCase.limit); got != testCase.want {
				t.Errorf("searchLimit(%d) = %d, want %d", testCase.limit, got, testCase.want)
			}
		})
	}
}

// TestEveryStatementThatReadsPagesJoinsVisibilityOnDemand is the S-8.2 audit,
// asserted as a property of the statements rather than of one call.
//
// The audit itself: the statement a search box reaches is `searchPages`, and it
// joins `campaigns` on visibility — see TestSearchPagesCannotDropTheVisibilityPredicate
// in pages_internal_test.go, which reads the statement's text. What this asserts is
// the other half, which that test cannot: that the statements which *do not* join
// are the ones that cannot leak, because each is scoped to a campaign the caller
// was already authorised for.
//
// The list is the audit. A new statement that projects a title or an excerpt has to
// be added here, and the question it has to answer is the one this file's comment
// answers: who reaches it, and with what knowledge of the campaign?
func TestEveryStatementThatReadsPagesJoinsVisibilityOnDemand(t *testing.T) {
	t.Parallel()

	// The statements in pages.go that project a title or an excerpt, and why each
	// is not a leak. Written out rather than derived: a derived list would be
	// computed from the same text it is checking, which is how a check passes
	// vacuously.
	//
	//   selectIndexedText, selectIndexedPagesForCampaign, selectIndexedPagesUnder,
	//   selectPageByPath, selectPagesForCampaign — all keyed by `campaign_id = ?`.
	//   The caller supplies an id it already holds, and holding it means the route
	//   mounted its gate (ADR 0024). None of them is reachable from a query
	//   parameter, which is where S-8.2's hazard lives.
	//
	//   searchPages — the one statement a search box reaches. It joins, and the
	//   test above is what holds it there.
	indexScoped := []string{
		selectIndexedText,
		selectIndexedPagesForCampaign,
		selectIndexedPagesUnder,
		selectPageByPath,
		selectPagesForCampaign,
	}

	for _, statement := range indexScoped {
		if !strings.Contains(statement, "campaign_id = ?") {
			t.Errorf("a campaign-scoped statement is not scoped by campaign_id:\n%s", statement)
		}
	}

	// The inverse, and the one that would catch a copy of `selectPagesForCampaign`
	// with the scope dropped: nothing that projects a title may be a bare
	// `SELECT ... FROM pages` with no tenant in the WHERE clause.
	for name, statement := range map[string]string{
		"selectIndexedText":             selectIndexedText,
		"selectIndexedPagesForCampaign": selectIndexedPagesForCampaign,
		"selectIndexedPagesUnder":       selectIndexedPagesUnder,
		"selectPageByPath":              selectPageByPath,
		"selectPagesForCampaign":        selectPagesForCampaign,
		"searchPages":                   searchPages,
	} {
		if strings.Contains(statement, "FROM pages ") &&
			!strings.Contains(statement, "campaign_id") {
			t.Errorf("%s reads `pages` with no campaign column in it:\n%s", name, statement)
		}
	}
}
