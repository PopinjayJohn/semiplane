package search_test

// The harness and the DOM helpers the assertions in this package share.
//
// # Why the chain is assembled here
//
// `router.go` is not this work item's to edit, and its `mountCampaignRoutes` is
// empty until the integrator adds this route, so the chain is built here — in the
// same order and with the same mount `router.go` assembles — and out loud, so that
// a change on either side shows up as a failing test rather than as a route that
// quietly lost its gate.
//
// The store is **not** the real one. This is the one place a real database would
// test the wrong thing: `store.SearchPages`'s visibility join is already asserted
// by phase 4's own audit, which fails if the join is dropped from the statement, so
// re-proving it here through a live database would test SQLite. What has to be
// proved *here* is that this route cannot ask the question wrongly — that the
// requestor and a resolved campaign scope reach the store, and that nothing is
// filtered afterwards. A recording `Pages` is what makes that observable, because
// the property is about what the route *passed*, not about what came back.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/httpapi/search"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

// The campaign these tests search, and the identifiers its rows carry.
const (
	testSlug   = "greyhaven"
	testCampID = int64(1)
	gmUser     = int64(10)
	playerUser = int64(11)
)

// The three markers that must appear in a response and must not appear in another.
//
// Each is a string in exactly one indexed row, and each exists to be greppable: the
// assertion that matters is an *absence*, so the marker has to be a string that
// cannot appear by accident. The private one is the S-8.2 finding — a bare query
// returning a private title to a reader who may not read the page it names.
const (
	privateMarker = "EMBERWRITHDECREE"
	publicMarker  = "GLIMMERLINGMARKET"
)

// gmRequestor is the campaign's GM: the one tier whose responses are salted
// differently from a player's.
func gmRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: gmUser, Username: "mira"}
}

// playerRequestor is a member with the player role, which S-8 says never includes
// secrets and which must be indistinguishable from anonymous as far as this route's
// rows are concerned.
func playerRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: playerUser, Username: "tobin"}
}

// anonymousRequestor is a signed-out reader of a public campaign: TierReadOnly.
func anonymousRequestor() domain.Requestor {
	return domain.Requestor{}
}

// searchCall is what the route asked the store.
type searchCall struct {
	// search is the request as it reached the store, verbatim. Every field matters
	// here, and `CampaignID` most of all: 0 means *every campaign the requestor may
	// read*, which is the S-8.2 leak in one integer.
	search store.PageSearch
	// req is the requestor as it reached the store. An unauthenticated requestor
	// reaching `SearchPages` with an id is the S-2.6 failure the store's own
	// comment describes, and the route must not construct one.
	req domain.Requestor
}

// recordingPages is a `search.Pages` that remembers every call and answers from a
// fixture.
//
// Recording rather than asserting inside the fake: the assertions about *what was
// passed* read better at the call site, where the test's intent is visible, and a
// fake that asserts is a fake whose failure message names the fake.
type recordingPages struct {
	mu sync.Mutex
	// calls is every request, in order.
	calls []searchCall
	// hits is the answer, returned whole to every caller. No filtering is possible
	// here by construction: the fake has no tier, so a test that tried to prove a
	// post-filter would have nothing to remove.
	hits []domain.SearchHit
	// err is returned when set.
	err error
}

// SearchPages records the call and returns the fixture rows.
func (p *recordingPages) SearchPages(
	_ context.Context,
	searchRequest store.PageSearch,
	req domain.Requestor,
) ([]domain.SearchHit, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls = append(p.calls, searchCall{search: searchRequest, req: req})

	if p.err != nil {
		return nil, p.err
	}

	return p.hits, nil
}

// calls_ returns every recorded call.
//
// The trailing underscore is `varnamelen`'s doing: `calls` is the field, and a
// method of the same name is the idiomatic Go alternative to a second name the test
// has to remember.
func (p *recordingPages) calls_() []searchCall {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]searchCall(nil), p.calls...)
}

// last returns the single call this fake received, or fails the test.
//
// A route that queried twice would break the one-call properties below — the
// revalidation test in particular — so the count is asserted rather than assumed.
func (p *recordingPages) last(t *testing.T) searchCall {
	t.Helper()

	calls := p.calls_()
	if len(calls) != 1 {
		t.Fatalf("the store was queried %d times, want exactly 1: %+v", len(calls), calls)
	}

	return calls[0]
}

// campaignStore answers the queries the access gate reads and refuses the three it
// does not need.
//
// Hand-written rather than pointed at a database so that a change to the schema
// cannot alter what the access matrix resolves to — which is half of what the
// visibility assertions below are about.
type campaignStore struct {
	campaign domain.Campaign
	members  map[int64]domain.Role
}

// CampaignBySlug returns the harness's campaign, or `store.ErrNotFound`.
func (s campaignStore) CampaignBySlug(_ context.Context, slug string) (domain.Campaign, error) {
	if slug != s.campaign.Slug {
		return domain.Campaign{}, store.ErrNotFound
	}

	return s.campaign, nil
}

// Membership returns the caller's role in the harness's campaign, or not found.
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

// CreateCampaign refuses: registration is not what this package exercises.
func (s campaignStore) CreateCampaign(
	_ context.Context,
	_ domain.Campaign,
) (domain.Campaign, error) {
	return domain.Campaign{}, errors.New("campaignStore: CreateCampaign is not used")
}

// CreateMembership refuses: the gate never writes.
func (s campaignStore) CreateMembership(
	_ context.Context,
	_ domain.Membership,
) (domain.Membership, error) {
	return domain.Membership{}, errors.New("campaignStore: CreateMembership is not used")
}

// DeleteCampaign refuses: nothing here deletes a campaign.
func (s campaignStore) DeleteCampaign(_ context.Context, _ int64) error {
	return errors.New("campaignStore: DeleteCampaign is not used")
}

// harness is one assembled search route over one campaign.
type harness struct {
	t *testing.T
	// pages is the recording fake. Always constructed, so a test that wants to
	// assert what the route *asked* has something to read, whether or not `index`
	// is still pointing at it.
	pages *recordingPages
	// index is what the route queries. `pages` by default; `real` replaces it with a
	// database, which is what the visibility assertions need.
	index search.Pages
	// backing is what the access gate reads. The in-memory `campaignStore` by
	// default; `real` replaces it with the same database, so the tier a test gets is
	// the tier `domain.ResolveAccess` derives from real membership rows.
	backing campaigns.Store
	// visibility is the campaign's, and a field rather than a constant because the
	// visibility assertions need a *public* campaign (so an anonymous reader reaches
	// the route at all and the test measures the route rather than the gate) while
	// the 404-oracle assertions need a private one.
	visibility domain.Visibility
	// logger receives this route's lines when set. Nil is allowed, and the route
	// discards its lines rather than failing, so a test that does not care about
	// logging constructs no logger.
	logger *slog.Logger
}

// indexedBy points the route at a fake the test already holds, so the assertions
// that read `pages.calls_()` and the route agree on one instance.
func (h *harness) indexedBy(pages *recordingPages) *harness {
	h.pages = pages
	h.index = pages

	return h
}

// real points both the route and the access gate at a database.
//
// The visibility assertions cannot be made against the fake: `recordingPages` has no
// tier, so it cannot express "this reader may not read this page", and a fake that
// answered differently per caller would be re-implementing the join the store
// already owns. `*store.Store` satisfies both `search.Pages` and `campaigns.Store`,
// so one handle is the index *and* the gate's source of truth.
func (h *harness) real(db *store.Store) *harness {
	h.index = db
	h.backing = db

	return h
}

// defaultBacking is the in-memory gate's source, built fresh per chain.
func (h *harness) defaultBacking() campaigns.Store {
	return campaignStore{
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
}

// newHarness builds a route over a public campaign with no rows.
//
// Public by default, for the reason `wiki`'s harness is: a gate that answers 404
// for everything is indistinguishable from a route that is working, and a suite
// whose failures are invisible is worse than one that is slow.
func newHarness(t *testing.T) *harness {
	t.Helper()

	pages := &recordingPages{}

	return &harness{
		t:          t,
		pages:      pages,
		index:      pages,
		visibility: domain.VisibilityPublic,
	}
}

// withRows sets the rows the fake will answer with.
func (h *harness) withRows(hits ...domain.SearchHit) *harness {
	h.pages.hits = hits

	return h
}

// failing makes the store answer err, so the route's own failure path is reached
// rather than the store's.
func (h *harness) failing(err error) *harness {
	h.pages.err = err

	return h
}

// logging installs a capture as the route's logger, so a test can assert what the
// route wrote. A field rather than a constructor parameter because most tests do not
// care and a `slog.Handler` parameter on every harness call would be noise.
func (h *harness) logging(handler slog.Handler) *harness {
	h.logger = slog.New(handler)

	return h
}

// handler builds the route under test. Called by `get`, so a test that changed a
// dependency between two requests gets both.
func (h *harness) handler() *search.Handler {
	return &search.Handler{
		Pages:       h.index,
		Instance:    components.InstanceView{Name: "Greyhaven", Version: "0.3.0"},
		SignOutHref: "/logout",
		Logger:      h.logger,
	}
}

// serve builds the whole chain, in the order `router.go` assembles it.
//
// The outer pattern is `/c/{slug}/` and not `/c/`, and that is load-bearing: a
// `net/http` path value is set by the mux whose *pattern* matched, so a pattern of
// `/c/` names no wildcard, `campaigns.Resolve` sees an empty slug, and
// `RequireRead` answers 404 for every request under it.
//
// Resolve outside RequireRead outside the campaign mux, because the guard beneath
// Resolve reads the tier Resolve decided on.
func (h *harness) serve() http.Handler {
	campaignMux := http.NewServeMux()
	search.Mount(campaignMux, h.handler())

	backing := h.backing
	if backing == nil {
		backing = h.defaultBacking()
	}

	outer := http.NewServeMux()
	outer.Handle("/c/{slug}/",
		campaigns.Resolve(backing)(
			middleware.Chain(campaignMux, campaigns.RequireRead),
		),
	)

	return middleware.RequestID(outer)
}

// serveUngated builds the same chain with the access gates removed.
//
// The one mount this package must be able to build and the whole reason
// `searchScope` exists: a route reached without `campaigns.Resolve` has no campaign
// and no tier on its context, and `store.PageSearch.CampaignID` reads 0 as "every
// campaign the requestor may read".
func (h *harness) serveUngated() http.Handler {
	campaignMux := http.NewServeMux()
	search.Mount(campaignMux, h.handler())

	outer := http.NewServeMux()
	outer.Handle("/c/{slug}/", campaignMux)

	return middleware.RequestID(outer)
}

// get issues one request as requestor and returns the recorded response.
func (h *harness) get(target string, requestor domain.Requestor) *httptest.ResponseRecorder {
	h.t.Helper()

	return h.request(h.serve(), target, requestor, nil)
}

// getWith issues one request carrying header, against an explicitly chosen chain.
func (h *harness) getWith(
	target string,
	requestor domain.Requestor,
	header http.Header,
) *httptest.ResponseRecorder {
	h.t.Helper()

	return h.request(h.serve(), target, requestor, header)
}

// ungated issues one request against the chain with the gates removed.
func (h *harness) ungated(target string, requestor domain.Requestor) *httptest.ResponseRecorder {
	h.t.Helper()

	return h.request(h.serveUngated(), target, requestor, nil)
}

// request issues one request against a freshly built chain.
//
// A fresh chain per request rather than one shared handler, because a shared one
// would hold the *first* handler's dependencies and a test that changed the rows
// between two requests would silently be testing the first.
func (h *harness) request(
	handler http.Handler,
	target string,
	requestor domain.Requestor,
	header http.Header,
) *httptest.ResponseRecorder {
	h.t.Helper()

	req := httptest.NewRequestWithContext(h.t.Context(), http.MethodGet, target, http.NoBody)
	req = req.WithContext(identity.WithRequestor(req.Context(), requestor))

	maps.Copy(req.Header, header)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	return recorder
}

// hit is one indexed row, built the way the indexer builds one: a campaign-relative
// path *with* the extension, a title from front matter, and a snippet that is
// FTS5's plain-text excerpt of `body_plain`.
func hit(id int64, pagePath, title, snippet string) domain.SearchHit {
	// The `Page` fields are written as promoted names rather than as a nested
	// `Page: domain.Page{…}`, because `SearchHit` embeds `Page` and a composite
	// literal spells an embedded struct's own fields directly. The lint that
	// enforces that shape, which is also why `content/index.go` writes its
	// `PageText` the same way.
	return domain.SearchHit{
		ID:           id,
		CampaignID:   testCampID,
		Path:         pagePath,
		Kind:         domain.KindProse,
		Title:        title,
		CampaignSlug: testSlug,
		Snippet:      snippet,
	}
}

// --- The DOM ------------------------------------------------------------------

// doc is one parsed document.
type doc struct {
	// where names it in a failure message.
	where string
	// status is the response status, carried because a DOM cannot hold it and
	// several assertions below are about a status.
	status int
	// headers are the response headers, for the `ETag` and `Vary` assertions. A DOM
	// cannot hold a header either.
	headers http.Header
	// root is the parsed document.
	root *html.Node
}

// document parses a recorded response, or fails the test.
//
// Parsed and never string-matched, for the reason `internal/httpapi`'s audit gives:
// a substring assertion over markup finds nothing in a document whose word sits in a
// comment, an `aria-label` or an attribute name, which is how a gate comes to pass
// while the thing it watches is in the page. Every §7.5 and §10.2 rule below is a
// question about a node, and some of them — "exactly one `<h1>`", "no live region
// anywhere" — have no expression over a string at all.
func document(t *testing.T, where string, recorder *httptest.ResponseRecorder) doc {
	t.Helper()

	body := recorder.Body.String()
	if !strings.Contains(strings.ToLower(body), "<html") {
		t.Fatalf("%s: the response is not an HTML document (status %d): %s",
			where, recorder.Code, firstBytes(body))
	}

	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("%s: parse rendered document: %v", where, err)
	}

	return doc{
		where:   where,
		status:  recorder.Code,
		headers: recorder.Header(),
		root:    root,
	}
}

// elements calls visit for every element, in document order.
//
// Document order rather than depth-first by branch: §7.2's skip-link rule and
// "the first focusable elements in the document" are both statements about the order
// a reader meets things.
func (d doc) elements(visit func(*html.Node)) {
	var walk func(node *html.Node)

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

// find returns every element with the given tag name, in document order.
func (d doc) find(tag string) []*html.Node {
	var found []*html.Node

	d.elements(func(node *html.Node) {
		if node.Data == tag {
			found = append(found, node)
		}
	})

	return found
}

// testID returns the element carrying hook, or nil.
//
// Exactly one element or nothing: a hook that appears twice selects nothing
// (§10.2), and a helper that returned the first of several would hide that.
func (d doc) testID(t *testing.T, hook string) *html.Node {
	t.Helper()

	var found []*html.Node

	d.elements(func(node *html.Node) {
		if attribute(node, "data-testid") == hook {
			found = append(found, node)
		}
	})

	switch len(found) {
	case 1:
		return found[0]
	case 0:
		t.Fatalf("%s: no element carries data-testid=%q; the document's hooks are %s",
			d.where, hook, strings.Join(d.testIDs(), ", "))

		return nil
	default:
		t.Fatalf("%s: %d elements carry data-testid=%q; a hook that appears twice "+
			"selects nothing (UI §10.2)", d.where, len(found), hook)

		return nil
	}
}

// testIDs returns every hook in the document, sorted, for a failure message.
func (d doc) testIDs() []string {
	var found []string

	d.elements(func(node *html.Node) {
		if id := attribute(node, "data-testid"); id != "" {
			found = append(found, id)
		}
	})

	sortStrings(found)

	return found
}

// hasTestID reports whether exactly one element carries hook.
func (d doc) hasTestID(hook string) bool {
	return len(d.testIDNodes(hook)) == 1
}

// testIDNodes returns every element carrying hook.
func (d doc) testIDNodes(hook string) []*html.Node {
	var found []*html.Node

	d.elements(func(node *html.Node) {
		if attribute(node, "data-testid") == hook {
			found = append(found, node)
		}
	})

	return found
}

// textOf returns an element's descendant text, collapsed to single spaces.
func textOf(node *html.Node) string {
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

	return strings.Join(strings.Fields(out.String()), " ")
}

// attribute returns an attribute's value, or the empty string.
//
// `Get` and a nil check rather than `Attribute` and a nil check, for the reason
// `internal/httpapi`'s helper gives: an *absent* attribute is not an empty one, and
// counting the empty string is how an audit reports every element without the
// attribute as a duplicate of every other.
func attribute(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}

	return ""
}

// hasAttribute reports whether the attribute is present, whatever its value.
func hasAttribute(node *html.Node, name string) bool {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return true
		}
	}

	return false
}

// hasClassToken reports whether an element carries a class, as a whole token.
//
// Token rather than substring, because `class="targetish"` is not a target and
// `class="not-target"` carrying `.target` is how a class check comes to pass on a
// document that fails §7.3.
func hasClassToken(node *html.Node, name string) bool {
	for token := range strings.FieldsSeq(attribute(node, "class")) {
		if token == name {
			return true
		}
	}

	return false
}

// firstBytes is the head of a body, for a failure message about an unparseable one.
func firstBytes(body string) string {
	const limit = 200

	if len(body) <= limit {
		return body
	}

	return body[:limit] + "…"
}

// excerpt is the first bytes of a value, for a failure message about one that is too
// long to quote.
//
// The ellipsis is what makes the message readable: a test that fails on a 10kB echo
// and then prints 10kB has replaced a finding with a wall.
func excerpt(value string) string {
	const limit = 80

	if len(value) <= limit {
		return value
	}

	return value[:limit] + "…"
}

// sortStrings sorts in place. A tiny wrapper so the helpers above stay free of the
// sort import's noise, and so every caller in this package sorts identically.
func sortStrings(values []string) {
	sort.Strings(values)
}

// logCapture is a `slog.Handler` that keeps the records in memory.
//
// The point of it is that a *log line* is part of this route's surface — AGENTS.md's
// rule about carrying caller input into a log line is a rule about bytes somebody
// can read in an aggregator — so asserting it needs a handler rather than a
// convention.
type logCapture struct {
	mu      sync.Mutex
	records []string
}

// Enabled accepts every level, so nothing is dropped before the test looks.
func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

// Handle formats one record as `event: key=value…` and keeps it.
func (c *logCapture) Handle(_ context.Context, record slog.Record) error {
	var out strings.Builder

	out.WriteString(record.Message)

	record.Attrs(func(attr slog.Attr) bool {
		fmt.Fprintf(&out, " %s=%s", attr.Key, attr.Value.String())

		return true
	})

	c.mu.Lock()
	defer c.mu.Unlock()

	c.records = append(c.records, out.String())

	return nil
}

// WithAttrs returns the handler unchanged: this capture keeps no attributes.
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }

// WithGroup returns the handler unchanged: this capture keeps no groups.
func (c *logCapture) WithGroup(string) slog.Handler { return c }

// all returns every captured line.
func (c *logCapture) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.records...)
}
