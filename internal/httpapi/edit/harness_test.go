package edit_test

// The harness, and the one test file's worth of fixtures the whole package shares.
//
// The route is exercised through the same chain `router.go` builds —
// `campaigns.Resolve` outside `campaigns.RequireEdit` outside the campaign mux,
// mounted at `/c/{slug}/` — for the reason `wiki/handler_test.go` assembles its own:
// `router.go` is not this work item's to edit and its `mountCampaignRoutes` is empty
// until the integrator adds this route, so the chain is written out here where a
// change on either side is a failing test rather than a route that quietly lost its
// gate.
//
// Real where the property under test is the real thing's: a real `content.Root` for
// the confinement, a real `content.Renderer` for the preview, and a real migrated
// SQLite database for the revision log. The database is the important one — S-14's
// row for this phase is "a stale `If-Match` gives 412, the disk is unchanged, and
// **no revision row is written**", and only the last clause of that is about a
// table. A spy would assert that a function was not called, which is a different
// statement about a different thing.
//
// The database is opened through `sql.Open` and migrated with `store.Migrate` -- the
// shipped runner, the shipped migration set -- so the schema under test is the
// schema that ships, `page_revisions` included. `modernc.org/sqlite` is blank-
// imported for its driver, exactly as `internal/store` does.
//
// Fakes appear only where nothing real exists yet: the campaign rows and the
// identity, which the access gate reads and which the schema's foreign keys would
// otherwise make every test's setup longer than the test.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	_ "modernc.org/sqlite"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/edit"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
	"github.com/semiplane/semiplane/internal/web/components/secret"
)

// The campaign these tests edit, and the identifiers its rows carry.
const (
	testSlug   = "greyhaven"
	testCampID = int64(1)
	gmUser     = int64(10)
	playerUser = int64(11)
)

// gmRequestor is the campaign's GM: the only tier the route admits (S-6.5).
func gmRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: gmUser, Username: "mira"}
}

// playerRequestor is a member with the player role, which S-8 says has no content
// write and which S-14.4 says is a 403.
func playerRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: playerUser, Username: "tobin"}
}

// anonymousRequestor is a signed-out reader of a public campaign.
func anonymousRequestor() domain.Requestor {
	return domain.Requestor{}
}

// strangerRequestor is signed in and a member of no campaign at all.
//
// The case ADR 0024's "no access is 404" is actually about: an authenticated reader
// whose identity is real and whose membership is not. A 403 here would say the
// campaign exists, and the reader would learn a fact the matrix says they do not have.
func strangerRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: 99, Username: "wanderer"}
}

// campaignStore answers the two queries the access gate reads and refuses the
// three it does not need.
//
// Hand-written rather than pointed at the database so that a change to the schema
// cannot alter what the access matrix resolves to — which is half of what the
// authorisation tests are about. The database is still opened and migrated, because
// the revision log's foreign keys need the campaigns and users rows to exist.
type campaignStore struct {
	campaign domain.Campaign
	members  map[int64]domain.Role
}

func (s campaignStore) CampaignBySlug(_ context.Context, slug string) (domain.Campaign, error) {
	if slug != s.campaign.Slug {
		return domain.Campaign{}, store.ErrNotFound
	}

	return s.campaign, nil
}

func (s campaignStore) Membership(
	_ context.Context,
	campaignID, userID int64,
) (domain.Membership, error) {
	role, member := s.members[userID]
	if !member || campaignID != s.campaign.ID {
		return domain.Membership{}, store.ErrNotFound
	}

	return domain.Membership{CampaignID: campaignID, UserID: userID, Role: role}, nil
}

func (s campaignStore) CreateCampaign(
	_ context.Context,
	_ domain.Campaign,
) (domain.Campaign, error) {
	return domain.Campaign{}, errors.New("campaignStore: CreateCampaign is not used")
}

func (s campaignStore) CreateMembership(
	_ context.Context,
	_ domain.Membership,
) (domain.Membership, error) {
	return domain.Membership{}, errors.New("campaignStore: CreateMembership is not used")
}

func (s campaignStore) DeleteCampaign(_ context.Context, _ int64) error {
	return errors.New("campaignStore: DeleteCampaign is not used")
}

// harness is one assembled route, the vault behind it and the database the
// revision log writes to.
type harness struct {
	t    *testing.T
	root *content.Registry
	dir  string
	db   *sql.DB
	// revisions is the real log over the harness's database, so "no row was
	// written" is a statement about a table.
	revisions *edit.RevisionLog
	// redactor is a field because P10 replaces it and a test may want a different
	// one; the default is the pass-through the product installs today.
	redactor content.Redactor
	// logger is a field for the same reason, and because `handler()` builds a fresh
	// `edit.Handler` per request: a test that set a field on the value `handler()`
	// returned would be setting it on a value the next request never sees.
	logger     *slog.Logger
	visibility domain.Visibility
}

// logTo points the route's logger at a handler, for the observability assertions.
func (h *harness) logTo(handler slog.Handler) {
	h.t.Helper()

	h.logger = slog.New(handler)
}

// newHarness builds a route over an empty campaign and a migrated database.
//
// The database is a file in the test's own temporary directory rather than
// `:memory:` because the writer seam opens transactions on one connection, and an
// in-memory database is per-connection in a way that turns a transaction boundary
// into a puzzle. The migration runner is `store.Migrate` — the shipped one — so the
// schema under test is the schema that ships, migration 0008 included.
func newHarness(t *testing.T) *harness {
	t.Helper()

	dir := t.TempDir()
	registry := content.NewRegistry(content.RefuseSymlinks)

	if _, err := registry.Open(testSlug, dir); err != nil {
		t.Fatalf("open content root: %v", err)
	}

	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close content root: %v", err)
		}
	})

	db, log := newDatabase(t, dir)

	fixed := &harness{
		t:          t,
		root:       registry,
		dir:        dir,
		db:         db,
		revisions:  log,
		redactor:   content.NoSecrets(),
		visibility: domain.VisibilityPublic,
	}

	return fixed
}

// newDatabase opens a migrated database beside a content root and returns it with a
// revision log over it.
//
// Two return values because every test that wants the log also wants to read the
// table it wrote to, and handing back only the log would mean a second helper for the
// same pair. `dir` is the content root the campaign row names, so the fixture is
// internally consistent with the vault the route reads.
func newDatabase(t *testing.T, dir string) (*sql.DB, *edit.RevisionLog) {
	t.Helper()

	db := openDatabase(t, dir)

	return db, edit.NewRevisionLog(transactionalWriter(db))
}

// private makes the harness's campaign private, so a reader who is not a member is
// refused rather than served.
//
// The default is public and that is deliberate: a gate that answers 404 for
// everything is indistinguishable from a route that is working, and a suite whose
// failures are invisible is worse than one that is slow. A private campaign is what
// the "no access is 404" test needs, and it is one method call rather than a field
// every test has to remember to set.
func (h *harness) private() *harness {
	h.visibility = domain.VisibilityPrivate

	return h
}

// openDatabase opens a migrated database with the identity rows the schema's
// foreign keys need.
//
// The users and campaigns rows are inserted by hand rather than through the store's
// constructors because the store's single-instance claim (`store.Open`) is a
// process-level slot and these tests are parallel: opening ten real stores in one
// test binary is nine failures that say nothing about the editor. The *schema* is
// still the shipped one, which is the part under test.
func openDatabase(t *testing.T, dir string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "edit-test.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	// One connection, as the store configures: the writer seam opens transactions
	// and a second connection would be a second writer.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})

	// `foreign_keys=ON`, applied here because `store.Open` is not reachable without
	// claiming the process's single-instance slot and these tests are parallel.
	// SQLite leaves it off by default, and every claim this package makes about the
	// revision table's foreign keys — a write for a campaign that does not exist is
	// refused, a deleted account leaves the history behind — is a claim about a
	// constraint that is *not enforced* without it. The pragma is a copy rather
	// than an import because `applyPragmas` is unexported and `internal/store/*.go`
	// belongs to another work item.
	if _, err := db.ExecContext(t.Context(), "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}

	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	seedIdentity(t, db, dir)

	return db
}

// seedIdentity writes the users and the campaign the revision row's foreign keys
// name.
//
// The campaign's `content_root` is the harness's own directory, so the row is
// internally consistent with the vault the route reads. Nothing in the route
// consults it — the content root comes from `content.Registry`, keyed by slug — and
// that is exactly why it is inserted at all: the foreign key on
// `page_revisions.campaign_id` is real, and a revision row for a campaign that does
// not exist is a write the database refuses. A test that inserted nothing would see
// every save fail on the constraint and learn nothing about the precondition.
func seedIdentity(t *testing.T, db *sql.DB, contentRoot string) {
	t.Helper()

	ctx := t.Context()

	if _, err := db.ExecContext(ctx,
		"INSERT INTO users (id, username, password_hash, is_admin, created_at)"+
			" VALUES (?, ?, '', 0, 0)", gmUser, "mira"); err != nil {
		t.Fatalf("seed gm user: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		"INSERT INTO users (id, username, password_hash, is_admin, created_at)"+
			" VALUES (?, ?, '', 0, 0)", playerUser, "tobin"); err != nil {
		t.Fatalf("seed player user: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		"INSERT INTO campaigns (id, slug, name, content_root, visibility, system_id,"+
			" ruleset_version, created_at)"+
			" VALUES (?, ?, ?, ?, ?, '', '', 0)",
		testCampID, testSlug, "Greyhaven", contentRoot, string(domain.VisibilityPublic),
	); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
}

// transactionalWriter is the composition root's one line, spelled out.
//
// The same closure the integrator writes, over the test's database rather than the
// store's writer queue. Writing it here is what makes the shape a *checked* claim:
// if `store.Store.Write` ever stopped accepting a plain
// `func(context.Context, *sql.Tx) error`, this file would fail to compile before the
// composition root did.
func transactionalWriter(db *sql.DB) edit.Writer {
	return func(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin: %w", err)
		}

		if err := fn(ctx, tx); err != nil {
			// The transaction is being discarded and its caller has a real error to
			// report; a rollback failure cannot change the outcome.
			_ = tx.Rollback()

			return err
		}

		return tx.Commit()
	}
}

// write puts a page into the campaign's content root, creating its directory.
//
// Written with `os` because the test is the vault's author here, not the route: the
// confinement under test is the one between a request and a page, and a test that
// could not put a page in the root could not test a route that reads one.
func (h *harness) write(name, body string) {
	h.t.Helper()

	full := filepath.Join(h.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		h.t.Fatalf("create page directory: %v", err)
	}

	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		h.t.Fatalf("write page %s: %v", name, err)
	}
}

// read returns a page's bytes as they are on disk.
//
// The evidence half of "the disk is unchanged": it reads the file rather than asking
// the route what it thinks it wrote, so a handler that reported success without
// writing anything would still fail the test.
func (h *harness) read(name string) string {
	h.t.Helper()

	data, err := os.ReadFile(filepath.Join(h.dir, filepath.FromSlash(name)))
	if err != nil {
		h.t.Fatalf("read page %s: %v", name, err)
	}

	return string(data)
}

// revisionsFor returns every `page_revisions` row for one path, oldest first.
func (h *harness) revisionsFor(pagePath string) []revisionRow {
	h.t.Helper()

	rows, err := h.db.QueryContext(h.t.Context(),
		"SELECT content, author_id, source FROM page_revisions"+
			" WHERE campaign_id = ? AND path = ? ORDER BY id", testCampID, pagePath)
	if err != nil {
		h.t.Fatalf("read revisions: %v", err)
	}

	defer closeTestRows(rows)

	var found []revisionRow

	for rows.Next() {
		var (
			row    revisionRow
			author sql.NullInt64
			source string
		)

		if err := rows.Scan(&row.Content, &author, &source); err != nil {
			h.t.Fatalf("scan revision: %v", err)
		}

		row.AuthorID, row.Source = author.Int64, source
		found = append(found, row)
	}

	if err := rows.Err(); err != nil {
		h.t.Fatalf("read revisions: %v", err)
	}

	return found
}

// allRevisions returns every `page_revisions` row, oldest first.
//
// The whole table rather than one path's rows, because "no revision row was written"
// is a statement about the table and a lookup by path would report it satisfied even
// when the row landed somewhere else.
func (h *harness) allRevisions() []revisionRow {
	h.t.Helper()

	rows, err := h.db.QueryContext(h.t.Context(),
		"SELECT path, content, author_id, source FROM page_revisions ORDER BY id")
	if err != nil {
		h.t.Fatalf("read revisions: %v", err)
	}

	defer closeTestRows(rows)

	var found []revisionRow

	for rows.Next() {
		var (
			row    revisionRow
			author sql.NullInt64
		)

		if err := rows.Scan(&row.Path, &row.Content, &author, &row.Source); err != nil {
			h.t.Fatalf("scan revision: %v", err)
		}

		row.AuthorID = author.Int64
		found = append(found, row)
	}

	if err := rows.Err(); err != nil {
		h.t.Fatalf("read revisions: %v", err)
	}

	return found
}

// revisionRow is one row of `page_revisions`, as the tests read it.
type revisionRow struct {
	// Path is which page the row is about, which is what makes `allRevisions` able to
	// name it in a failure message.
	Path     string
	Content  string
	AuthorID int64
	Source   string
}

// closeTestRows releases a cursor. A bare `defer rows.Close()` leaves errcheck
// unhappy, and this repository runs it with check-blank on.
func closeTestRows(rows *sql.Rows) {
	if err := rows.Close(); err != nil {
		panic("edit_test: closing rows: " + err.Error())
	}
}

// handler builds the route under test.
func (h *harness) handler() *edit.Handler {
	return &edit.Handler{
		Roots:       h.root,
		Revisions:   h.revisions,
		Renderers:   edit.CampaignRenderers{testSlug: content.NewRenderer(testSlug, nil)},
		Redactor:    h.redactor,
		Logger:      h.logger,
		Instance:    components.InstanceView{Name: "Greyhaven", Version: "0.6.0"},
		SignOutHref: "/logout",
	}
}

// noPages is the page listing the cross-route test gives the wiki handler.
//
// The wiki route resolves a page's references against the campaign's listing, and
// `Vault.md` has none, so an empty listing is the truth for this fixture rather than
// a stub that happens to be empty.
type noPages struct{}

func (noPages) PagesForCampaign(_ context.Context, _ int64) ([]domain.Page, error) {
	return nil, nil
}

// serve builds the whole chain, in the order `router.go` assembles it.
//
// `campaigns.Resolve` outside `RequireEdit` outside the campaign mux, mounted at
// `/c/{slug}/` — and the outer pattern carries the wildcard because a `net/http`
// path value comes from the pattern that matched, not from the mux underneath. At
// `/c/` the gate would resolve nothing and refuse every request under it, which is
// the bug `wiki`'s `TestTheCampaignMountMustCarryTheSlug` exists for; the same
// reasoning applies to this route and `TestTheEditorMountCarriesTheSlug` holds it.
func (h *harness) serve() http.Handler {
	campaignMux := http.NewServeMux()
	edit.Mount(campaignMux, h.handler())

	backing := campaignStore{
		campaign: domain.Campaign{
			ID:         testCampID,
			Slug:       testSlug,
			Name:       "Greyhaven",
			Visibility: h.visibility,
		},
		members: map[int64]domain.Role{
			gmUser:     domain.RoleGM,
			playerUser: domain.RolePlayer,
		},
	}

	outer := http.NewServeMux()
	outer.Handle("/c/{slug}/",
		campaigns.Resolve(backing)(
			middleware.Chain(campaignMux, campaigns.RequireEdit),
		),
	)

	return middleware.RequestID(outer)
}

// request issues one request against a freshly built chain.
//
// A fresh chain per request rather than one shared handler, because a shared one
// would hold the *first* request's dependencies and a test that changes the
// redactor or the renderer between two requests would be testing the first.
func (h *harness) request(
	method, target string,
	requestor domain.Requestor,
	header http.Header,
	body string,
) *httptest.ResponseRecorder {
	h.t.Helper()

	req := httptest.NewRequestWithContext(
		h.t.Context(),
		method,
		target,
		strings.NewReader(body),
	)
	req = req.WithContext(identity.WithRequestor(req.Context(), requestor))

	maps.Copy(req.Header, header)

	recorder := httptest.NewRecorder()
	h.serve().ServeHTTP(recorder, req)

	return recorder
}

// get issues a GET as requestor.
func (h *harness) get(target string, requestor domain.Requestor) *httptest.ResponseRecorder {
	h.t.Helper()

	return h.request(http.MethodGet, target, requestor, nil, "")
}

// put issues a PUT carrying ifMatch (empty for none) and body.
func (h *harness) put(
	target string,
	requestor domain.Requestor,
	ifMatch string,
	body string,
) *httptest.ResponseRecorder {
	h.t.Helper()

	header := http.Header{}
	if ifMatch != "" {
		header.Set("If-Match", ifMatch)
	}

	return h.request(http.MethodPut, target, requestor, header, body)
}

// validatorFor fetches the editor's own validator for a page, which is the value a
// save must present.
//
// A request rather than a local computation, and that is the point: the tests must
// exercise the validator the *route* mints, or they would be testing a second
// derivation — which is exactly the hazard `contentHash` in `precondition.go` is
// written about.
func (h *harness) validatorFor(pagePath string) string {
	h.t.Helper()

	recorder := h.get("/c/greyhaven/edit/"+pagePath, gmRequestor())
	if recorder.Code != http.StatusOK {
		h.t.Fatalf("GET editor for the validator = %d, want 200", recorder.Code)
	}

	validator := recorder.Header().Get("ETag")
	if validator == "" {
		h.t.Fatal("the editor response carried no ETag")
	}

	return validator
}

// pageBody is one page's markdown, with a front-matter block so that a test is
// exercising a real document rather than a bare fragment.
func pageBody(prose string) string {
	return "---\ntitle: A page\n---\n" + prose
}

// --- The DOM helpers ---------------------------------------------------------
//
// Parsed, never substring-matched, for the reason AGENTS.md states about the
// accessibility gate: a substring test passes on a document whose attribute is
// spelled across an interpolation boundary, and the word it is looking for can sit
// in a comment or an `aria-label` where a reader would hear it.

type document struct {
	t      *testing.T
	where  string
	root   *html.Node
	source string
}

// parse renders a response's markup into a tree the assertions can walk.
func parse(t *testing.T, where, body string) *document {
	t.Helper()

	if !strings.Contains(strings.ToLower(body), "<html") {
		t.Fatalf("%s: the response is not an HTML document:\n%s", where, body)
	}

	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("%s: parse rendered document: %v", where, err)
	}

	return &document{t: t, where: where, root: root, source: body}
}

// elements calls visit for every element, in document order.
func (d *document) elements(visit func(*html.Node)) {
	d.t.Helper()

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)

			if child.Type == html.ElementNode {
				visit(child)
			}
		}
	}

	walk(d.root)
}

// byTestID returns the element carrying a `data-testid`, or nil.
func (d *document) byTestID(testID string) *html.Node {
	d.t.Helper()

	var found *html.Node

	d.elements(func(node *html.Node) {
		if found == nil && attribute(node, "data-testid") == testID {
			found = node
		}
	})

	return found
}

// requireTestID fails unless exactly one element carries the hook.
func (d *document) requireTestID(testID string) *html.Node {
	d.t.Helper()

	count := 0

	var found *html.Node

	d.elements(func(node *html.Node) {
		if attribute(node, "data-testid") == testID {
			count++
			found = node
		}
	})

	if count != 1 {
		d.t.Fatalf("%s: %d elements carry data-testid=%q, want exactly 1",
			d.where, count, testID)
	}

	return found
}

// text returns an element's descendant text.
func text(node *html.Node) string {
	var out strings.Builder

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			out.WriteString(current.Data)
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return out.String()
}

// attribute returns an attribute's value, or the empty string.
func attribute(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}

	return ""
}

// hasAttribute reports whether an attribute is present, whatever its value.
func hasAttribute(node *html.Node, name string) bool {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return true
		}
	}

	return false
}

// hasClassToken reports whether the element's class list contains token.
//
// Split on whitespace rather than compared as a substring, because `class="targeted"`
// contains the letters of `target` and §10.6's audit is about the token.
func hasClassToken(node *html.Node, token string) bool {
	return slices.Contains(strings.Fields(attribute(node, "class")), token)
}

// isFocusable reports whether a keyboard can reach the element.
//
// The same set the accessibility gate uses: the natively focusable elements plus
// anything with a `tabindex`. A `tabindex="-1"` is focusable *programmatically* and
// is included deliberately, because the gate's own rule ("no `aria-hidden` on a
// focusable element") treats it as focusable and a helper that disagreed would make
// this file's assertions weaker than the gate's.
func isFocusable(node *html.Node) bool {
	if hasAttribute(node, "tabindex") {
		return true
	}

	switch node.DataAtom {
	case atom.A:
		return hasAttribute(node, "href")
	case atom.Button, atom.Input, atom.Select, atom.Textarea, atom.Summary:
		return true
	case atom.Iframe, atom.Audio, atom.Video:
		return true
	default:
		return false
	}
}

// pathOf is a readable location for a failure message.
func pathOf(node *html.Node) string {
	parts := []string{}

	for current := node; current != nil && current.Type == html.ElementNode; current = current.Parent {
		label := current.Data

		switch {
		case attribute(current, "data-testid") != "":
			label += "[" + attribute(current, "data-testid") + "]"
		case attribute(current, "id") != "":
			label += "#" + attribute(current, "id")
		default:
			if class := strings.Fields(attribute(current, "class")); len(class) > 0 {
				label += "." + class[0]
			}
		}

		parts = append([]string{label}, parts...)
	}

	if len(parts) > 4 {
		parts = parts[len(parts)-4:]
	}

	return strings.Join(parts, " > ")
}

// hasTestID reports whether exactly one element carries `data-testid`.
//
// A predicate beside `requireTestID` because "this is absent" and "this is present
// exactly once" are both assertions an audit makes and only one of them is a fatal
// helper: `requireTestID` calls `Fatalf`, which cannot be used to check an absence.
func (d *document) hasTestID(testID string) bool {
	d.t.Helper()

	count := 0

	d.elements(func(node *html.Node) {
		if attribute(node, "data-testid") == testID {
			count++
		}
	})

	return count > 0
}

// textOf returns an element's descendant text, trimmed of nothing.
//
// `requireTestID` must find it, so a missing element is fatal here rather than
// silently yielding an empty string — which would make an "it is empty" assertion
// pass for the wrong reason, having found nothing at all.
func (d *document) textOf(testID string) string {
	d.t.Helper()

	node := d.requireTestID(testID)

	var text strings.Builder

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			text.WriteString(current.Data)
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return text.String()
}

// disclosuresPanel returns the editor's disclosure surface and whether it is there.
//
// **A named predicate rather than a general `findTestID`.** The general shape was
// written first and the linter was right that it had exactly one caller and one
// constant: a helper parameterised by a value nothing varies is a helper shaped by
// its first use, and the next caller would have found it did not fit.
//
// The pair of assertions that need it are the same pair — "the panel is here" and
// "the panel is not here" — and both are about the panel.
func (d *document) disclosuresPanel() (*html.Node, bool) {
	d.t.Helper()

	var found *html.Node

	count := 0

	d.elements(func(node *html.Node) {
		if attribute(node, "data-testid") == secret.DisclosuresTestID {
			count++
			found = node
		}
	})

	return found, count > 0
}

// textUnder returns the descendant text of one node, which is how a subtree is
// checked for content the subtree must not have.
func textUnder(d *document, node *html.Node) string {
	d.t.Helper()

	return subtreeText(node)
}

// subtreeText is an element's descendant text, with **no `*document` receiver**.
//
// Separate from `textUnder` because one caller — `labelInName`, in the label-in-name
// audit — has a node and no document. Giving `textUnder` a nil document would look
// fine and panic on its `t.Helper()` call, so the walker takes only what it needs and
// the document-aware wrapper stays a wrapper.
func subtreeText(node *html.Node) string {
	var text strings.Builder

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			text.WriteString(current.Data)
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return text.String()
}

// labelInName is an element's visible text, which §10.2's label-in-name rule measures
// against its accessible name.
//
// **The subtree's text, not an `aria-label`.** §10.2's rule is that the accessible
// name contains the visible label, so a component that renders the label as visible
// text and the extra words in `aria-label` passes; reading only the attribute would
// pass a component that renders nothing visible and still be correct for a
// screen-reader user and wrong for everyone else.
func labelInName(node *html.Node) string {
	return strings.TrimSpace(subtreeText(node))
}

// textOfTestID is `requireTestID` plus `textUnder`, for the one call site that wants
// both and does not care that a missing element is fatal.
func textOfTestID(t *testing.T, d *document, testID string) string {
	t.Helper()

	return textUnder(d, d.requireTestID(testID))
}

// countAttribute counts elements carrying `data-<name>`.
//
// Separate from `countTestIDPrefix` because `data-testid` is a **test hook** and
// these attributes are the **component's own contract** — `data-secret-ordinal` is one
// element per callout, while `data-testid` has several values per callout (the list
// item, its state, its reveal control). Counting a hook to learn about the product's
// structure is how a test ends up asserting a component's naming scheme.
func (d *document) countAttribute(name string) int {
	d.t.Helper()

	count := 0

	d.elements(func(node *html.Node) {
		if attribute(node, name) != "" {
			count++
		}
	})

	return count
}
