package secrets_test

// The harness, and the one test file's worth of fixtures the whole package shares.
//
// The route is exercised through the same chain `router.go` builds —
// `campaigns.Resolve` outside `campaigns.RequireRead` outside the campaign mux,
// mounted at `/c/{slug}/` — for the reason `edit/harness_test.go` assembles its own:
// `router.go` is not this work item's to edit and its `mountCampaignRoutes` is empty
// until the integrator adds this route, so the chain is written out here where a
// change on either side is a failing test rather than a route that quietly lost its
// gate.
//
// # What is real and what is not, and why each is what it is
//
// **The vault is real.** A real `content.Root`, because the confinement and the
// one-byte splice are the two things under test and neither has anything to do with
// the route's logic.
//
// **The ledger is real.** `*store.Store` — the shipped type, the shipped schema,
// migration 0011 and 0009 included — reached through the route's own `Ledger`
// interface. This is the important one: §5.6.4 requires a reveal to be written to
// `audit_log` **and** to `secrets_revealed`, and S-5.8 makes the ledger the authority
// for *who revealed it and when*. "No ledger row" is a statement about a table, and a
// spy would only assert that a function was not called — a different statement about a
// different thing, satisfied just as happily by a row written by some other path.
//
// Because `store.Open` claims the process's single-instance slot
// (`internal/store/store.go`), there is **one** store for the whole test binary and it
// is opened in `TestMain`. That is why every harness gets **its own campaign**: it
// makes every table assertion scoped to one campaign, so the parallel tests cannot
// see each other's rows and a whole-table assertion is still a whole-table assertion.
//
// **The access gate is a hand-written store.** Its queries are the two the gate reads,
// and answering them from memory rather than from the database means a change to the
// schema cannot alter what the authorisation matrix resolves to — which is half of
// what the authorisation tests are about. The database still holds the memberships,
// because `store.RevealSecret` re-checks the GM *inside its own transaction* and that
// check is one of the things under test.
//
// **The validator comes from the wiki route**, not from this package. A harness that
// computed its own `If-Match` would be testing a second derivation of the same fact —
// exactly the hazard `contentHash` in `precondition.go` is written about. So every
// `If-Match` in this package is a value the *product* minted on a `GET`, taken from
// the surface a GM actually reads before revealing a secret.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/httpapi/secrets"
	"github.com/semiplane/semiplane/internal/httpapi/wiki"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

// The accounts every harness resolves against.
//
// Seeded once in the shared database, because `store.RevealSecret` re-checks the GM's
// membership inside its own transaction and a hand-written id with no row behind it
// would make every reveal fail on the check rather than on the route. Their *roles* in
// the access gate come from `campaignStore` instead, which is the memory-sourced half
// explained in the file header.
var (
	accountsOnce sync.Once
	gmUser       int64
	playerUser   int64
	strangerUser int64
)

// seedAccounts creates the three accounts, once per process.
//
// `sync.Once` rather than per-harness creation because these are the identities every
// test asks the gate about, and a test whose GM had a different id from its neighbour's
// would make each test's assertions about "the GM" mean something different.
func seedAccounts(t *testing.T) {
	t.Helper()

	accountsOnce.Do(func() {
		ctx := t.Context()

		gmUser = createUser(t, "mira")
		playerUser = createUser(t, "tobin")
		strangerUser = createUser(t, "wanderer")
		_ = ctx
	})

	if gmUser <= 0 || playerUser <= 0 || strangerUser <= 0 {
		t.Fatal("the accounts were not seeded")
	}
}

// gmRequestor is the campaign's GM: the only tier the route admits (S-6.5, §5.6.4).
func gmRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: gmUser, Username: "mira"}
}

// playerRequestor is a member with the player role, which S-8 says has no content
// write and which S-14.4 says is a 403.
func playerRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: playerUser, Username: "tobin"}
}

// anonymousRequestor is a signed-out reader of a public campaign.
func anonymousRequestor() domain.Requestor { return domain.Requestor{} }

// strangerRequestor is signed in and a member of no campaign at all.
//
// The case ADR 0024's "no access is 404" is about: an authenticated reader whose
// identity is real and whose membership is not. A 403 here would say the campaign
// exists, and the reader would learn a fact the matrix says they do not have.
func strangerRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: strangerUser, Username: "wanderer"}
}

// campaignCounter names one campaign per harness, so parallel tests do not collide in
// the shared database's uniquely-slugged rows.
var campaignCounter atomic.Int64

// campaignStore answers the two queries the access gate reads and refuses the three it
// does not need.
//
// Hand-written rather than pointed at the database for the reason the file header
// gives. The database is still seeded with real memberships, because the store's own
// `RevealSecret` check is not this fake's business.
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

// harness is one assembled route, the vault behind it and the campaign it belongs to.
type harness struct {
	t    *testing.T
	root *content.Registry
	dir  string

	// campaign is this harness's campaign: a row in the shared database and the slug
	// the access gate resolves. Every table assertion is scoped to its id, which is
	// what lets the tests run in parallel against one store.
	campaign domain.Campaign

	// ledger is what the route records into. The shared store by default; `failing`
	// replaces it, which is the only way to reach the 500 branch — an unreachable
	// branch is an unverified one.
	ledger secrets.Ledger

	// logger is a field because a test that wants to assert on this route's log lines
	// sets it before the first request, and `handler()` builds a fresh `secrets.Handler`
	// per request — a test that set a field on the value `handler()` returned would be
	// setting it on a value the next request never sees.
	logger *slog.Logger
}

// newHarness builds a route over a **public** campaign, a fresh vault and the shared
// store.
//
// Public is the default and that is deliberate: a gate that answers 404 for everything
// is indistinguishable from a route that is working, and a suite whose failures are
// invisible is worse than one that is slow. `newPrivateHarness` is the other case, and
// it is one function rather than a field every test has to remember to set.
func newHarness(t *testing.T) *harness {
	t.Helper()

	return buildHarness(t, domain.VisibilityPublic)
}

// newPrivateHarness builds a route over a **private** campaign, so a reader who is not
// a member is refused rather than served.
//
// That is what ADR 0024's "no access is 404" needs: a private campaign is 404 to
// everybody who is not a member, including a player who would otherwise be entitled to
// a 403, and that equality is the property under test.
func newPrivateHarness(t *testing.T) *harness {
	t.Helper()

	return buildHarness(t, domain.VisibilityPrivate)
}

// buildHarness is the one constructor both wrappers call.
func buildHarness(t *testing.T, visibility domain.Visibility) *harness {
	t.Helper()

	seedAccounts(t)

	ctx := t.Context()

	dir := t.TempDir()
	slug := "reveal-" + strconv.FormatInt(campaignCounter.Add(1), 10)

	campaign, err := sharedStore.CreateCampaign(ctx, domain.Campaign{
		Slug:           slug,
		Name:           "Greyhaven",
		ContentRoot:    filepath.Join(dir, slug),
		Visibility:     visibility,
		SystemID:       "5e-2024",
		RulesetVersion: "1",
	})
	if err != nil {
		t.Fatalf("CreateCampaign(%q) error = %v, want nil", slug, err)
	}

	// Real memberships, because `store.RevealSecret` re-reads them inside its own
	// transaction and a reveal for a campaign with no GM row would fail there rather
	// than on anything this route does. The stranger is deliberately **not** a member:
	// it is the authenticated non-member ADR 0024's rule is about.
	if _, err := sharedStore.CreateMembership(ctx, domain.Membership{
		CampaignID: campaign.ID, UserID: gmUser, Role: domain.RoleGM,
	}); err != nil {
		t.Fatalf("CreateMembership(gm) error = %v, want nil", err)
	}

	if _, err := sharedStore.CreateMembership(ctx, domain.Membership{
		CampaignID: campaign.ID, UserID: playerUser, Role: domain.RolePlayer,
	}); err != nil {
		t.Fatalf("CreateMembership(player) error = %v, want nil", err)
	}

	registry := content.NewRegistry(content.RefuseSymlinks)

	if _, err := registry.Open(slug, dir); err != nil {
		t.Fatalf("open content root: %v", err)
	}

	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close content root: %v", err)
		}
	})

	return &harness{
		t:        t,
		root:     registry,
		dir:      dir,
		campaign: campaign,
		ledger:   sharedStore,
	}
}

// failing replaces the ledger with one that refuses every write, which is how the 500
// branch after the byte write is reached.
//
// The error is one a caller would see from a store that could not commit, and the
// assertions around it are about what *did* and did not happen — not about which
// status a sentinel maps to, which `internal/store`'s own tests hold.
func (h *harness) failing(cause error) *harness {
	h.ledger = refusingLedger{cause: cause}

	return h
}

// refusingLedger is a `secrets.Ledger` that refuses both directions.
type refusingLedger struct {
	cause error
}

func (l refusingLedger) RevealSecret(
	_ context.Context, _ int64, _, _ string, _ int64, _ int, _ bool,
) (domain.SecretReveal, error) {
	return domain.SecretReveal{}, l.cause
}

func (refusingLedger) UnrevealSecret(
	_ context.Context, _ int64, _, _ string, _ int64,
) error {
	return errors.New("secrets_test: the unreveal ledger was not configured to fail")
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
// The evidence half of "the file is unchanged" and "only one byte changed": it reads
// the file rather than asking the route what it thinks it wrote, so a handler that
// reported success without writing anything would still fail the test.
func (h *harness) read(name string) string {
	h.t.Helper()

	data, err := os.ReadFile(filepath.Join(h.dir, filepath.FromSlash(name)))
	if err != nil {
		h.t.Fatalf("read page %s: %v", name, err)
	}

	return string(data)
}

// writeFileMode reports a page's permission bits, so the write's mode is asserted from
// the filesystem rather than from what the route was asked for.
func (h *harness) writeFileMode(name string) os.FileMode {
	h.t.Helper()

	info, err := os.Stat(filepath.Join(h.dir, filepath.FromSlash(name)))
	if err != nil {
		h.t.Fatalf("stat page %s: %v", name, err)
	}

	return info.Mode().Perm()
}

// pageStat is what `stat` reads: the identity of a file as the filesystem sees it.
//
// The inode and the modification time are here rather than only the mode because those
// are the two facts that make "this route rewrote the page" observable. The bytes are
// not: a rewrite of an unchanged page produces identical bytes, an identical digest and
// an identical validator, so three of the four obvious comparisons cannot see it.
type pageStat struct {
	// inode is the file's identity, from `os.SameFile`'s own criterion. A temp-file
	// write renames a *new* file over the target, so a rewritten page has a different
	// inode and an untouched one does not.
	inode uint64
	// size and mode are carried so a caller can use this as a whole `stat`.
	size int64
	mode os.FileMode
}

// stat reports a page's filesystem identity.
func (h *harness) stat(name string) pageStat {
	h.t.Helper()

	info, err := os.Stat(filepath.Join(h.dir, filepath.FromSlash(name)))
	if err != nil {
		h.t.Fatalf("stat page %s: %v", name, err)
	}

	inode, ok := fileIdentity(info)
	if !ok {
		h.t.Skipf("this host's filesystem reports no inode for %s, so a rewrite is "+
			"not observable here", name)
	}

	return pageStat{inode: inode, size: info.Size(), mode: info.Mode().Perm()}
}

// fileIdentity is a file's inode number, or false where the platform does not report one.
//
// `os.SameFile` compares two `FileInfo`s by inode and device without exposing either, so
// this reaches for `Sys()` and is the one place in the suite that touches it. Written as
// a separate function with a `false` arm so a platform that cannot answer does not turn
// the assertion into a panic — and so the reason a `stat` is unavailable is a message
// rather than a type assertion failure.
//
// **`uint64` rather than the kernel's own type, and that is deliberate**: the only
// comparison is `!=` against another value from this same function, so the width is a
// property of the harness and not of the host.
func fileIdentity(info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}

	return stat.Ino, true
}

// handler builds the route under test.
func (h *harness) handler() *secrets.Handler {
	return &secrets.Handler{
		Roots:  h.root,
		Ledger: h.ledger,
		Logger: h.logger,
	}
}

// wikiHandler builds the read route, mounted beside this one so the tests can take a
// validator from the surface a GM actually reads.
//
// It is here rather than only in the interop test because **every** `If-Match` in this
// package comes from it. A harness that computed its own validator would test a second
// derivation of one fact, which is the exact hazard `contentHash` in
// `precondition.go` is written about — and it would make the whole suite agree with
// itself about the wrong value.
func (h *harness) wikiHandler() *wiki.Handler {
	return &wiki.Handler{
		Roots: h.root,
		Renderers: wiki.CampaignRenderers{
			h.campaign.Slug: content.NewRenderer(h.campaign.Slug, nil),
		},
		Pages:       noPages{},
		Redactor:    content.OmitSecrets(),
		Cache:       content.NewCache(8),
		Instance:    components.InstanceView{Name: "Greyhaven", Version: "0.10.0"},
		SignOutHref: "/logout",
	}
}

// noPages is the page listing this fixture gives the wiki handler.
//
// The wiki route resolves a page's references against the campaign's listing, and the
// fixtures below have none, so an empty listing is the truth for them rather than a
// stub that happens to be empty.
type noPages struct{}

func (noPages) PagesForCampaign(_ context.Context, _ int64) ([]domain.Page, error) {
	return nil, nil
}

// serve builds the whole chain, in the order `router.go` assembles it.
//
// `campaigns.Resolve` outside `RequireRead` outside the campaign mux, mounted at
// `/c/{slug}/` — and the outer pattern carries the wildcard because a `net/http` path
// value comes from the pattern that matched, not from the mux underneath. At `/c/` the
// gate would resolve nothing and refuse every request under it;
// `TestTheRevealMountCarriesTheSlug` holds both directions of that.
func (h *harness) serve() http.Handler {
	campaignMux := http.NewServeMux()
	wiki.Mount(campaignMux, h.wikiHandler())
	secrets.Mount(campaignMux, h.handler())

	backing := campaignStore{
		campaign: h.campaign,
		members: map[int64]domain.Role{
			gmUser:     domain.RoleGM,
			playerUser: domain.RolePlayer,
		},
	}

	outer := http.NewServeMux()
	outer.Handle("/c/{slug}/",
		campaigns.Resolve(backing)(
			middleware.Chain(campaignMux, campaigns.RequireRead),
		),
	)

	return middleware.RequestID(outer)
}

// request issues one request against a freshly built chain.
//
// A fresh chain per request rather than one shared handler, because a shared one would
// hold the *first* request's dependencies and a test that swaps the ledger or the
// logger between two requests would be testing the first.
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

// reveal issues a `PUT` to the reveal endpoint with ifMatch and a JSON body.
func (h *harness) reveal(
	pagePath string,
	requestor domain.Requestor,
	ifMatch, body string,
) *httptest.ResponseRecorder {
	h.t.Helper()

	header := http.Header{"Content-Type": {"application/json"}}
	if ifMatch != "" {
		header.Set("If-Match", ifMatch)
	}

	return h.request(http.MethodPut, "/c/"+h.campaign.Slug+"/secrets/"+pagePath,
		requestor, header, body)
}

// validatorFor fetches the wiki route's `ETag` for a page, which is the value a reveal
// must present.
//
// A request rather than a local computation, and that is the whole point: the tests
// must exercise the validator the *product* mints, or they would be testing a second
// derivation — the failure mode `contentHash` in `precondition.go` documents.
func (h *harness) validatorFor(pagePath string) string {
	h.t.Helper()

	recorder := h.get("/c/"+h.campaign.Slug+"/wiki/"+strings.TrimSuffix(pagePath, ".md"),
		gmRequestor())
	if recorder.Code != http.StatusOK {
		h.t.Fatalf("GET the page for its validator = %d, want 200; body:\n%s",
			recorder.Code, recorder.Body)
	}

	validator := recorder.Header().Get("ETag")
	if validator == "" {
		h.t.Fatal("the wiki response carried no ETag")
	}

	return validator
}

// revealBody is the JSON a reveal request carries, encoded from the same shape a
// client would send.
func revealBody(t *testing.T, payload map[string]any) string {
	t.Helper()

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode the reveal body: %v", err)
	}

	return string(encoded)
}

// ledgerRow is one `secrets_revealed` row, as the assertions read it.
type ledgerRow struct {
	Path          string
	Anchor        string
	RevealedBy    int64
	RevealedAt    int64
	RevertedCount int
}

// ledgerRows returns every ledger row for this harness's campaign, ordered by path
// then anchor.
//
// The whole campaign rather than one path, because "no ledger row was written" is a
// statement about the table and a lookup by path would report it satisfied even when a
// row landed somewhere else — which is exactly the shape of defect a handler that
// resolved its path wrongly would produce.
func (h *harness) ledgerRows() []ledgerRow {
	h.t.Helper()

	rows, err := sharedStore.SecretsRevealedForCampaign(h.t.Context(), h.campaign.ID)
	if err != nil {
		h.t.Fatalf("read the ledger: %v", err)
	}

	found := make([]ledgerRow, 0, len(rows))
	for _, reveal := range rows {
		found = append(found, ledgerRow{
			Path:          reveal.Path,
			Anchor:        reveal.Anchor,
			RevealedBy:    reveal.RevealedBy,
			RevealedAt:    reveal.RevealedAt.Unix(),
			RevertedCount: reveal.RevertedCount,
		})
	}

	return found
}

// auditRow is one `audit_log` row, as the assertions read it.
type auditRow struct {
	Action string
	Target string
	Detail string
	Actor  int64
}

// auditRows returns every audit row for this harness's campaign, oldest first.
//
// **Read with a plain `SELECT` rather than through the store**, and that is a path
// choice rather than a shortcut: `internal/store` has no audit-log *read* — it has
// `RevealSecret` and `UnrevealSecret`, which write — and adding one would be another
// work item's package. §5.6.4's "every reveal is written to `audit_log` **and** to the
// ledger" is two clauses; the ledger clause is `ledgerRows`, which goes through the
// store's own reader, and this clause reads the **table** directly. That is the right
// level for the claim: what has to be true is that a row is in `audit_log`, and a
// reader that only the route itself can reach could not prove it.
//
// The column list and the scan list are written out together for the reason
// `internal/store/secrets.go` names its statements: a column list and the scan that
// reads it have to agree, and one name is what makes a mismatch impossible to
// introduce rather than merely unlikely.
func (h *harness) auditRows() []auditRow {
	h.t.Helper()

	rows, err := sharedRead.QueryContext(h.t.Context(),
		"SELECT action, target, detail, actor_id FROM audit_log"+
			" WHERE campaign_id = ? ORDER BY id",
		h.campaign.ID,
	)
	if err != nil {
		h.t.Fatalf("read the audit log: %v", err)
	}

	defer closeTestRows(rows)

	found := make([]auditRow, 0, 4)

	for rows.Next() {
		var row auditRow

		if err := rows.Scan(&row.Action, &row.Target, &row.Detail, &row.Actor); err != nil {
			h.t.Fatalf("scan an audit row: %v", err)
		}

		found = append(found, row)
	}

	if err := rows.Err(); err != nil {
		h.t.Fatalf("read the audit log: %v", err)
	}

	return found
}

// closeTestRows releases a cursor.
//
// A bare `defer rows.Close()` leaves `errcheck` unhappy, and this repository runs it
// with `check-blank` on — so the panic is the honest spelling of "a test's own cursor
// could not be closed", which is a fault in the test rather than in the product.
func closeTestRows(rows *sql.Rows) {
	if err := rows.Close(); err != nil {
		panic("secrets_test: closing rows: " + err.Error())
	}
}

// createUser is an account the test does not authenticate.
//
// The password hash is a literal because nothing here verifies hashing: that is
// `internal/httpapi/auth`'s job, and a test here that depended on it would fail for a
// reason that has nothing to do with secrets.
func createUser(t *testing.T, username string) int64 {
	t.Helper()

	user, err := sharedStore.CreateUser(t.Context(), domain.User{
		Username:     username,
		PasswordHash: "not-a-real-hash",
	})
	if err != nil {
		t.Fatalf("CreateUser(%q) error = %v, want nil", username, err)
	}

	return user.ID
}

// TestMain opens the one store this package's tests use and removes its directory
// afterwards.
//
// One store, because `store.Open` claims a process-level slot and a second open is an
// error by design (ADR 0004's single-writer rule). The instance is seeded lazily from
// the first test that needs accounts, because seeding needs a `*testing.T` and
// `TestMain` has only an exit code.
func TestMain(m *testing.M) {
	code := func() int {
		dir, err := os.MkdirTemp("", "semiplane-secrets-test")
		if err != nil {
			panic("create the test database directory: " + err.Error())
		}

		defer func() {
			if removeErr := os.RemoveAll(dir); removeErr != nil {
				panic("remove the test database directory: " + removeErr.Error())
			}
		}()

		path := filepath.Join(dir, "semiplane.db")

		db, err := store.Open(context.Background(), "file:"+path)
		if err != nil {
			panic("open the test store: " + err.Error())
		}

		defer func() {
			if closeErr := db.Close(); closeErr != nil {
				panic("close the test store: " + closeErr.Error())
			}
		}()

		// A second handle on the same file, for the one table the store cannot read.
		// `store.Open` is a single-shot process claim and `internal/store` has no
		// audit-log reader, so a plain `sql.Open` over the shipped schema is how the
		// `audit_log` clause of §5.6.4 gets asserted. It is **read only** — every
		// write under test goes through the store's own writer queue, and a second
		// writer would be a second writer queue, which is the thing ADR 0004 exists
		// to forbid.
		readOnly, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if err != nil {
			panic("open the read-only handle: " + err.Error())
		}

		defer func() {
			if closeErr := readOnly.Close(); closeErr != nil {
				panic("close the read-only handle: " + closeErr.Error())
			}
		}()

		sharedStore, sharedRead = db, readOnly

		return m.Run()
	}()

	os.Exit(code)
}

// The two handles onto the one database this package's tests use.
//
// A package variable rather than a `sync.Once` because `store.Open` is a single-shot
// process claim: opening it in `TestMain` is the only place it can be opened exactly
// once, and a lazily-opened handle would race with a parallel test.
var (
	// sharedStore is what the route records through — the shipped type, the shipped
	// schema.
	sharedStore *store.Store
	// sharedRead is the read-only handle the audit-log assertions use.
	sharedRead *sql.DB
)

// describeLedger renders ledger rows for a failure message.
//
// One row per line rather than `%v` on the slice, because a slice of structs prints
// as a wall of field names and the one fact a reader needs from a failure is *which*
// row it was.
func describeLedger(rows []ledgerRow) string {
	var out strings.Builder

	for _, row := range rows {
		fmt.Fprintf(&out, "\n\tpath=%q anchor=%q by=%d at=%d reverted=%d",
			row.Path, row.Anchor, row.RevealedBy, row.RevealedAt, row.RevertedCount)
	}

	return out.String()
}

// describeAudit renders audit rows for a failure message.
func describeAudit(rows []auditRow) string {
	var out strings.Builder

	for _, row := range rows {
		fmt.Fprintf(&out, "\n\taction=%q target=%q detail=%q actor=%d",
			row.Action, row.Target, row.Detail, row.Actor)
	}

	return out.String()
}
