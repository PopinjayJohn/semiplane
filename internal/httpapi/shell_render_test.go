package httpapi_test

// UI §10.2 and §10.6, gate-blocking, against **every route**.
//
// The stylesheet half is `internal/web/a11y_test.go`; this is the document half.
// The two are separate because they make different claims — §10.2's
// `outline: none` rule is about a file the DOM does not contain, and §7.3's
// target *minimums* are about a class the DOM does carry but the stylesheet
// decides — and because neither can see the other's subject.
//
// # Why the routes and not the templates
//
// §10.2 says "for every route, including every registered plugin page type". A
// template rendered by hand can differ from a route's document by exactly the
// things this gate exists to catch: a middleware that adds a landmark, a handler
// that fills a slot the template left empty, a header that sets a `Vary` the
// template knows nothing about. Every fixture below therefore goes through
// `httpapi.NewRouter` — the same composition root the binary uses — and is
// asserted on the bytes that come back.
//
// # Why the DOM is parsed and never string-matched
//
// A substring assertion over markup is how a gate comes to pass while the thing
// it watches sits in a comment, in an attribute nothing renders, or spelled across
// an attribute boundary by a templ interpolation. §10.2's vocabulary rule is the
// clearest case: "no rendered string contains world or session" is trivially
// satisfied while `<!-- the world of this account -->` sits in the document — and
// a reader never sees a comment, which is precisely why it must be excluded
// rather than scanned.
//
// The parser also gets what a string match structurally cannot: which element an
// `id` resolves to, whether a landmark is nested inside another, and the order of
// the document's focusable elements. §10.2's skip-link rule is an *ordering* rule
// and there is no way to state it over a string.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi"
	"github.com/semiplane/semiplane/internal/httpapi/accounts"
	"github.com/semiplane/semiplane/internal/httpapi/assets"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/httpapi/edit"
	"github.com/semiplane/semiplane/internal/httpapi/events"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/httpapi/search"
	"github.com/semiplane/semiplane/internal/httpapi/wiki"
	"github.com/semiplane/semiplane/internal/observability"
)

// headerSearchLabel is the accessible name §7.2 gives the **header's** search
// form, and the one name in the shell's `search` role that is reserved.
//
// The other `search` landmark is the search route's own form, which names itself
// ("Search this campaign"). The reservation matters because two landmarks of the
// same role with the same name make landmark navigation useless — and the header's
// is the one pinned, so that a rename of the route's form is a collision the audit
// sees rather than a reader discovering.
const headerSearchLabel = "Search pages"

// headerSearchHook is the `data-testid` the header component puts on the form that
// owns `headerSearchLabel`.
//
// How the audit tells the header's search form from any other one, which matters
// only because the reserved name could in principle be claimed twice. It is read
// from a `data-testid` rather than matched on position or on an ancestor, because
// AGENTS.md's rule is to assert on test hooks and never on DOM shape: a hook is
// something the component promises, and a position is something a reordering
// silently takes away.
const headerSearchHook = "header-search"

// forbiddenVocabulary is UI §1.2's closed vocabulary. Neither entity exists, a
// leftover is a bug, and a grep for them is a test — which is this.
//
// Matched case-insensitively and as a substring, because the failure this guards
// against is a noun phrase inside a sentence ("your session has expired", "the
// world map"), and neither is spelled with a capital S in isolation. Both name an
// entity the product does not have.
var forbiddenVocabulary = []string{"world", "session"}

// renderedRoute is one audited document.
type renderedRoute struct {
	// where names the route in a failure message.
	where string
	// status is the response status. A route that answered an error is still
	// audited: §10.2 says every route, and the error documents are built by a
	// different path than the success ones, which is exactly where an audit that
	// only checks `200` would find nothing.
	status int
	// body is the response bytes.
	body []byte
	// vary is the `Vary` response header, carried because a DOM cannot hold it.
	vary string
	// themeVariants is the *same* document re-rendered with each of
	// `themeCookieValues`, in that order.
	//
	// Present because the substantive form of S-13.5 cannot be stated over one
	// response. "The document does not vary by the theme cookie" is a claim about
	// five requests, and a single response can only ever be one of them — so
	// asserting it from one is asserting that one particular value produced the
	// bytes, which is a much weaker claim and is satisfied by a route that reads
	// the cookie and ignores it *for that value*.
	//
	// The empty string is first and is not an empty cookie: it is the *absent*
	// cookie, which is what a reader who has expressed no preference has, and it
	// is the one value a reader-independent document is most likely to be wrong
	// about, because a fallback path is a second answer to "what theme".
	themeVariants [][]byte
}

// auditedRoutes drives every route this phase registers and returns its document.
//
// The list is the route table, and `TestEveryRegisteredRouteIsCoveredHere` reads
// the router's own registrations and fails when one is missing from it — so this
// file cannot quietly stop covering a route. The failure is "a route was added
// and not audited" rather than nothing at all, which is the only way a coverage
// gate keeps its value as the router grows.
//
// Ten documents, over two routers.
//
// The first four are the pre-campaign surfaces, covering the states the shell
// distinguishes before a campaign exists:
//
//   - `/` for a signed-in member of one campaign — the list, with cards.
//   - `/` for a signed-in member of none — §4.7's first-run state, which is a
//     designed surface and not an error, and which has its own copy.
//   - `/login` for an anonymous reader — the state most of a new installation is
//     actually in, and the only route with no account zone.
//   - `/login` with a failed credential — §4.7's `403` row over the form, so the
//     error copy is audited rather than assumed.
//
// The other six are the campaign-scoped routes, over a second router that mounts
// all five of them: the wiki page, an asset failure, a search result list, the
// search idle state, the editor, and the editor's 412.
//
// The pre-campaign/campaign split matters because §4.6 is a *structural*
// difference: the navigation landmark and its skip link exist on one and not on the
// other. A shell that rendered both, or neither, would pass a single-state audit.
//
// Two shapes per route are audited on purpose, and they are not redundant. The
// asset's **failure** document and the editor's **412** are built by a different
// path than their success documents — a different writer, a different view model,
// and for the 412 a completely different body — so auditing only the 200 audits
// one of two implementations of the same route. §10.2 says *every route*, and a
// failure state is the one a reader meets when something is wrong, which is
// exactly when its copy needs auditing most.
func auditedRoutes(t *testing.T) []renderedRoute {
	t.Helper()

	st := newWiringStore()

	hash, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	st.users["ada"] = domain.User{ID: 1, Username: "ada", PasswordHash: hash}
	st.campaigns["greyhaven"] = domain.Campaign{
		ID: 1, Slug: "greyhaven", Name: "Greyhaven", Visibility: domain.VisibilityPrivate,
	}
	st.members[1] = domain.RoleGM

	handler := fullRouter(st)

	// Signed in, so the audit sees a document with an account zone — the header's
	// sign-out form is one of the shell's focus stops and §10.6 walks it.
	signedIn := httptest.NewRecorder()
	handler.ServeHTTP(signedIn, signIn(t, "ada", "correct horse"))
	if signedIn.Code != http.StatusFound && signedIn.Code != http.StatusSeeOther {
		t.Fatalf("sign in = %d, want a redirect", signedIn.Code)
	}

	cookie := sessionCookieOf(t, signedIn)

	// The first-run document needs an account with no campaigns, so a second
	// store. Signing out of one and into the other would be a longer route to the
	// same two documents.
	empty := newWiringStore()
	emptyHash, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	empty.users["grace"] = domain.User{ID: 7, Username: "grace", PasswordHash: emptyHash}

	emptyHandler := fullRouter(empty)

	emptySignedIn := httptest.NewRecorder()
	emptyHandler.ServeHTTP(emptySignedIn, signIn(t, "grace", "correct horse"))
	if emptySignedIn.Code != http.StatusFound && emptySignedIn.Code != http.StatusSeeOther {
		t.Fatalf("sign in (first run) = %d, want a redirect", emptySignedIn.Code)
	}

	emptyCookie := sessionCookieOf(t, emptySignedIn)

	// The failed credential. §4.7's `403` row is what this answers, and the copy
	// a reader sees on a wrong password is interface text, so it is in scope for
	// the vocabulary rule.
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, formPost(t, "/login", signInForm("ada", "wrong"), nil))

	// The anonymous sign-in form. `handlers` rather than two anonymousGet calls,
	// because one call per fixture keeps the response and the status together;
	// two calls meant two requests to read one document.
	anonymous := anonymousGet(t, handler, "/login")

	// The anonymous form and the rejected credential are the two documents that
	// come from a recorder rather than from `renderRoute`, so their theme variants
	// are re-rendered here — with the same request shape each time, which is the
	// whole reason `themeVariants` takes a closure instead of a path.
	anonymousVariants := themeVariants(t, func(theme *http.Cookie) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, pinned(withCookies(getRequest(t, "/login", nil), nil, theme)))

		return out
	})

	rejectedVariants := themeVariants(t, func(theme *http.Cookie) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		handler.ServeHTTP(
			out,
			pinned(withCookies(
				formPost(t, "/login", signInForm("ada", "wrong"), nil), nil, theme,
			)),
		)

		return out
	})

	campaign := newCampaignFixture(t)

	return []renderedRoute{
		renderRoute(t, handler, "/ (a member of one campaign)", cookie, "/"),
		renderRoute(t, emptyHandler, "/ (first run: no campaigns)", emptyCookie, "/"),
		routeFromRecorder("/login (anonymous)", anonymous, anonymousVariants),
		routeFromRecorder("/login (a rejected credential)", rejected, rejectedVariants),

		// --- The campaign-scoped routes --------------------------------------
		//
		// A page that exists, rendered by a real renderer out of a real confined
		// root over a real maintained index. The `[[wikilink]]` on it resolves,
		// which is what makes this document exercise the link markup rather than a
		// paragraph of prose.
		campaign.get("/c/greyhaven/wiki/Goblin (a page)"),
		// The asset *failure* state, not the bytes. A found asset is a PNG, and a
		// DOM audit over a PNG is the audit of nothing; the failure state is the
		// one document this route composes.
		campaign.get("/c/greyhaven/assets/no-such-map.png (a missing asset)"),
		campaign.getVariants("/c/greyhaven/search?q=goblin (results)"),
		campaign.getVariants("/c/greyhaven/search (the idle state)"),
		campaign.get("/c/greyhaven/edit/Goblin (the editor)"),

		// The 412, reached by the only route to it: a `PUT` carrying a validator
		// that is not the page's. It is issued here rather than pasted in as a
		// fixture document, so the audit cannot drift from the route's real
		// precondition handling — a hand-written 412 body would be a document
		// the product never serves.
		campaign.staleSave(),

		// --- A fixture, because no served route carries the pair --------------
		//
		// Two `search` landmarks in one document, so the rule the landmark case
		// states — that the header's form and the search route's own form are
		// named differently — is actually exercised. With the campaign routes
		// wired, the search route renders its own form; the header's is still
		// suppressed in production, so the pair no served document carries is
		// hand-written here on purpose.
		//
		// Without it the `case "search"` branch is unreachable: no audited route
		// renders a second search landmark, so an assertion in it can be wrong in
		// either direction and nothing notices. That is not hypothetical: the
		// first version of this fix asserted one literal name instead of a set,
		// which would have failed the search route's own landmark the moment that
		// route landed, and which no mutation could catch because the branch never
		// ran. A rule with no reachable instance is not a rule.
		{
			where:         "a document carrying both search landmarks",
			body:          []byte(searchLandmarkFixture),
			themeVariants: themeVariantsOf(searchLandmarkFixture),
		},
	}
}

// campaignFixture is the router the §10.2 audit drives the campaign-scoped routes
// through, over a **real** confined content root and a **real** renderer.
//
// A separate fixture from `fullRouter` rather than a flag on it, because the two
// need genuinely different worlds. `fullRouter`'s store is a map of structs and
// has no filesystem behind it, which is exactly right for the account surface and
// exactly wrong for a page route: a wiki document rendered from a fixture string
// is a document no reader will ever be shown, and the markup a template produces
// from a hard-coded body is not the markup it produces from a `[[wikilink]]` that
// resolved. §10.2 audits the route, and the route reads the vault.
//
// The five handlers are built here over one set of dependencies, in the same
// arrangement `cmd/server` uses: one registry, one renderer map, one instance
// view, one store. A fixture that built the renderers twice would be auditing a
// composition the product does not run.
type campaignFixture struct {
	t       *testing.T
	handler http.Handler
	cookie  *http.Cookie
	store   *wiringStore
}

// The fixture's campaign and its two pages.
//
// The two pages exist so the wiki document carries a **resolved** `[[wikilink]]`
// and not a broken one. Both states are link markup and the audit walks every
// anchor, so a fixture that only produced broken links would audit the broken
// branch twice and the resolved branch never.
const (
	fixtureCampaign = "greyhaven"
	fixtureIndex    = "Index"
	fixtureGoblin   = "Goblin"
)

// newCampaignFixture builds the router the campaign routes are audited through.
func newCampaignFixture(t *testing.T) *campaignFixture {
	t.Helper()

	store := newWiringStore()

	hash, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	store.users["ada"] = domain.User{ID: 1, Username: "ada", PasswordHash: hash}
	store.campaigns[fixtureCampaign] = domain.Campaign{
		ID: 1, Slug: fixtureCampaign, Name: "Greyhaven", Visibility: domain.VisibilityPrivate,
	}
	store.members[1] = domain.RoleGM

	// One row per page, answered by the `pages` lister below. The campaign id is
	// the store's, so a route that scoped its index query to a campaign that is
	// not this one would resolve no reference and the link would render broken —
	// which the audit cannot distinguish from correct markup, and which is why the
	// lister filters rather than returning everything.
	rows := []domain.Page{
		{ID: 1, CampaignID: 1, Path: fixtureIndex + ".md", Title: "Index"},
		{ID: 2, CampaignID: 1, Path: fixtureGoblin + ".md", Title: "Goblin"},
	}

	store.hits["goblin"] = []domain.SearchHit{{
		ID: 2, CampaignID: 1, Path: fixtureGoblin + ".md", Title: "Goblin",
		CampaignSlug: fixtureCampaign,
		// FTS5's excerpt of `body_plain`, which excludes `[!secret]` content in
		// every reveal state (S-5.11). A result list is therefore safe to cache
		// even for a GM, and this is the row that says so.
		Snippet: "A goblin watches the wyvern sea.",
	}}

	// A real directory with real files, opened through the real registry. `Open`
	// is the test-facing constructor: it takes the directory, proves it is
	// confinable, and the registry owns the handle from there — so the routes
	// under audit are reading through `os.Root` and not through a `map[string]string`.
	registry := content.NewRegistry(content.RefuseSymlinks)
	if _, err := registry.Open(fixtureCampaign, writeFixtureVault(t)); err != nil {
		t.Fatalf("open the fixture content root: %v", err)
	}

	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close the fixture content roots: %v", err)
		}
	})

	// `nil` kinds, which the wiki route and the editor both document as "every page
	// is prose". The plugin registry is phase 8, and a fixture that hand-wrote a
	// kind table would be auditing a registry the product does not have.
	renderers := wiki.CampaignRenderers{
		fixtureCampaign: content.NewRenderer(fixtureCampaign, nil),
	}

	instance := degradedInstanceView()
	logger := slog.New(slog.DiscardHandler)

	// The hub exists so the event route is mounted, which is what puts
	// `GET /c/{slug}/events` on the router and therefore in `auditedRoutes`'s
	// remit. The audit does not open a stream: an event stream is a response with
	// no end, and §10.2's rules are all about a document a reader can finish.
	// Mounting it is the claim being made — that the route exists and is behind
	// the gate — and the assertions on it live in `events`'s own tests.
	hub := events.NewHub()
	t.Cleanup(func() {
		if err := hub.Close(); err != nil {
			t.Errorf("close the fixture event hub: %v", err)
		}
	})

	handler := httpapi.NewRouter(
		logger,
		config.Config{HandlerTimeout: wiringHandlerTimeout},
		observability.NewRegistry(),
		&accounts.Router{Store: store, Instance: instance},
		store,
		&wiki.Handler{
			Roots:       registry,
			Renderers:   renderers,
			Pages:       auditPages{rows: rows},
			Redactor:    content.NoSecrets(),
			Cache:       content.NewCache(renderCacheEntriesForAudit),
			Logger:      logger,
			Instance:    instance,
			SignOutHref: signOutHrefForAudit,
		},
		&assets.Handler{
			Roots:       registry,
			Logger:      logger,
			Instance:    instance,
			SignOutHref: signOutHrefForAudit,
		},
		&search.Handler{
			Pages:       store,
			Instance:    instance,
			SignOutHref: signOutHrefForAudit,
			Logger:      logger,
		},
		&edit.Handler{
			Roots: registry,
			// A log that refuses every append. Nothing here saves successfully, and
			// the one branch that would append is the 204 — which this fixture never
			// asks for, because §10.2 audits documents and a 204 has none. A real
			// store handle here would claim the process's single-instance slot for
			// the privilege of writing a row nothing reads.
			Revisions: edit.NewRevisionLog(nil),
			Renderers: edit.CampaignRenderers(renderers),
			Redactor:  content.NoSecrets(),
			Logger:    logger,
			Instance:  instance,
			// The sign-out href and the instance view again: the editor composes
			// the same shell as the other four, and a route that rendered a
			// different chrome would be audited as if it were the same document.
			SignOutHref: signOutHrefForAudit,
		},
		&events.Handler{Hub: hub, Logger: logger},
		// The tabletop socket is mounted as a `nil` handler on purpose, and the
		// reason is the one `make a11y`'s package list states: `/play` renders no
		// document. Every answer it gives is either a `101` with a socket on it or
		// a sentence of plain text, and neither has landmarks, headings, a title
		// or a vocabulary to audit — so a §10.2 audit over this route would be an
		// audit of nothing, and naming this package in `A11Y_ROUTE_PKGS` would be a
		// claim about audits that do not exist.
		//
		// `play.Handler` is still a real value in the product: `playRoute` is built
		// by the composition root and threaded through `NewRouter` exactly like
		// the other five. It is nil *here* because this fixture audits documents,
		// and a socket is not one. The mount itself is asserted where a socket can
		// actually be opened — `play.TestThePlayRouteAnswersTheS8Matrix` opens
		// one, and `cmd/server`'s wiring test asks the router it builds for a 101.
		//
		// `plugins.Handler` is nil for the same reason and one more: the two
		// reference plugins render a widget and a link preview, neither of which
		// is a shell document this file audits, and a plugin route with a hub over
		// no state would answer 503 rather than render anything to audit. Its own
		// audits live in `internal/httpapi/plugins`, which `A11Y_ROUTE_PKGS` names.
		nil,
		nil,
		// `theme.Handler` is nil for the same reason: the generated sheet is a
		// stylesheet, not a shell document, and its audits live in
		// `internal/httpapi/theme`. What this file audits is the `<link>` the
		// shell emits, which is a shell concern and is asserted here.
		nil,
		// `secrets.Handler` is nil for the same reason once more: it answers JSON
		// and mutates a file, and a document audit has nothing to say about either.
		// Its own tests mount it on a mux of their own.
		nil,
	)

	// Signed in as the campaign's GM. The editor and the stream are GM-only, so an
	// audit run as a `player` would be auditing a 403 rather than a document — and
	// §10.2's rules are about documents.
	signedIn := httptest.NewRecorder()
	handler.ServeHTTP(signedIn, signIn(t, "ada", "correct horse"))
	if signedIn.Code != http.StatusFound && signedIn.Code != http.StatusSeeOther {
		t.Fatalf("sign in for the campaign fixture = %d, want a redirect", signedIn.Code)
	}

	return &campaignFixture{
		t:       t,
		handler: handler,
		cookie:  sessionCookieOf(t, signedIn),
		store:   store,
	}
}

// The two values the fixture's handlers share with the composition root, restated
// rather than imported: `cmd/server` is package `main` and cannot be imported, and
// a value that has to be copied is a value that can disagree.
//
// `renderCacheEntriesForAudit` is a *small* bound rather than the product's 512,
// because the cache is irrelevant to §10.2 and a small one makes the fixture's
// memory use obvious. The one property the audit does depend on is that the wiki
// handler holds a **real** cache, so a page is rendered once and then served from
// it — a handler with `NewCache(0)` renders every time, and a template that
// misbehaves only on a second render would be invisible here.
const (
	renderCacheEntriesForAudit = 8
	signOutHrefForAudit        = "/logout"
)

// auditPages is the maintained `pages` table as the wiki route sees it.
//
// A slice rather than a real `*store.Store`, for the same reason the rest of this
// fixture is a fake store: `store.Open` claims the process's single-instance slot,
// and the audit runs in the same process as every other test in this package. The
// *query* is the part that matters and it is real — a campaign filter, so a route
// that asked for a campaign nobody registered would resolve no reference and the
// audit would see a broken link and read it as correct markup.
type auditPages struct{ rows []domain.Page }

// PagesForCampaign returns the rows belonging to one campaign.
func (p auditPages) PagesForCampaign(
	_ context.Context,
	campaignID int64,
) ([]domain.Page, error) {
	mine := make([]domain.Page, 0, len(p.rows))

	// Indexed rather than ranged: `domain.Page` carries a `time.Time` twice and is
	// well over a word, so ranging copies one per iteration to read two integers.
	for index := range p.rows {
		if p.rows[index].CampaignID == campaignID {
			mine = append(mine, p.rows[index])
		}
	}

	return mine, nil
}

// writeFixtureVault creates the fixture's content root and returns its path.
//
// Real files on a real filesystem, because the routes under audit go through
// `os.Root` and the whole of S-3.5 is a claim about what that refuses. A fixture
// backed by a map would audit the templates and skip the boundary.
//
// Two pages and one asset. The asset is never fetched — the audited document is
// the *failure* state, since a PNG is not a document §10.2 can read — but it has
// to exist, because a 404 and a 403 are different documents and a fixture that
// omitted the file would audit the wrong one.
func writeFixtureVault(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	files := map[string]string{
		fixtureIndex + ".md": "---\ntitle: Index\n---\n\nA link to [[" +
			fixtureGoblin + "]].\n",
		fixtureGoblin + ".md": "---\ntitle: Goblin\n---\n\nA goblin watches the wyvern sea.\n",
	}

	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write the fixture page %s: %v", name, err)
		}
	}

	// A one-pixel PNG, written as bytes rather than decoded, so the fixture does
	// not need an image library to exist. `assets`' own tests are where the media
	// table is read; this only has to be a file the table recognises.
	const onePixelPNG = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" +
		"\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00" +
		"\x1f\x15\xc4\x89\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05" +
		"\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82"

	if err := os.WriteFile(
		filepath.Join(dir, "greyhaven-map.png"),
		[]byte(onePixelPNG),
		0o600,
	); err != nil {
		t.Fatalf("write the fixture asset: %v", err)
	}

	return dir
}

// get drives one GET through the fixture's router and returns the document.
func (f *campaignFixture) get(where string) renderedRoute {
	f.t.Helper()

	return renderRoute(f.t, f.handler, where, f.cookie, pathOf(where))
}

// getVariants is `get` for a document whose request is not a plain session GET.
//
// The search routes are the case: `?q=` is part of the request the theme cookie
// must not be allowed to change, so the variants have to be re-fetched with the
// query intact. Building them from `pathOf(where)` rather than passing a second
// path keeps the label and the URL the same string, which is what stops the two
// drifting apart.
func (f *campaignFixture) getVariants(where string) renderedRoute {
	f.t.Helper()

	path := pathOf(where)

	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(
		recorder,
		pinned(withCookies(getRequest(f.t, path, nil), f.cookie, nil)),
	)

	return routeFromRecorder(where, recorder, themeVariants(
		f.t,
		func(theme *http.Cookie) *httptest.ResponseRecorder {
			out := httptest.NewRecorder()
			f.handler.ServeHTTP(
				out,
				pinned(withCookies(getRequest(f.t, path, nil), f.cookie, theme)),
			)

			return out
		},
	))
}

// staleSave issues the `PUT` that answers 412, and returns the document it carries.
//
// A validator that is not the page's, rather than a real one fetched and then
// invalidated, because the second is two requests and a race against the file's
// mtime: an `ETag` derived from a content hash does not change, so the pair would
// be stable — but it would also be two ways to be wrong, and this one cannot be.
//
// The body is the GM's unsaved text, which is the whole point of the conflict
// view: the response must render the *request's* buffer, not the disk's. A fixture
// that sent the disk's own text would audit a 412 whose most important assertion
// cannot fail.
func (f *campaignFixture) staleSave() renderedRoute {
	f.t.Helper()

	const buffer = "---\ntitle: Goblin\n---\n\nA goblin watches the wyvern sea, and " +
		"the wyvern watches back.\n"

	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, staleSaveRequest(f.t, f.cookie, nil, buffer))

	if recorder.Code != http.StatusPreconditionFailed {
		f.t.Fatalf("the stale save = %d, want 412; a fixture that cannot reach the "+
			"conflict view is auditing a document the product never serves: %s",
			recorder.Code, firstBytes(recorder.Body.String()))
	}

	const where = "/c/greyhaven/edit/" + fixtureGoblin + " (a stale save: 412)"

	// Re-issued per theme value rather than the first response being reused, so
	// the conflict view is audited the way a reader meets it: five times, with the
	// only difference being a cookie that must make no difference. Reusing the
	// bytes would have asserted nothing at all, because the assertion is a
	// comparison.
	variants := themeVariants(f.t, func(theme *http.Cookie) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		f.handler.ServeHTTP(out, staleSaveRequest(f.t, f.cookie, theme, buffer))

		return out
	})

	return routeFromRecorder(where, recorder, variants)
}

// staleSaveRequest builds the `PUT` a conflict view is rendered from.
//
// A function because the conflict has to be produced five times — once for the
// document under audit and once per theme cookie — and a hand-inlined second copy
// is how the two start disagreeing about which validator is stale.
func staleSaveRequest(
	t *testing.T,
	session, theme *http.Cookie,
	body string,
) *http.Request {
	t.Helper()

	request := httptest.NewRequestWithContext(
		t.Context(), http.MethodPut, "/c/greyhaven/edit/"+fixtureGoblin,
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "text/markdown; charset=utf-8")
	request.Header.Set("If-Match", `W/"0000000000000000000000000000000000000000"`)
	return pinned(withCookies(request, session, theme))
}

// pathOf is the request path inside an audit label.
//
// The label is a sentence a failure message can print — "`/c/greyhaven/wiki/Goblin
// (a page)`" — and the request needs only the path. Splitting here rather than
// passing two strings keeps the label and the URL from drifting apart, which is
// the failure a coverage list is most vulnerable to: a route renamed, the label
// still naming the old one, and the gate reporting it covered.
func pathOf(where string) string {
	path, _, _ := strings.Cut(where, " (")
	return path
}

// renderRoute GETs a path through a handler with a cookie, and re-renders it once
// per theme-cookie value.

// searchLandmarkFixture is a minimal document carrying the header's search form
// and a search route's own, each named as the design record fixes them.
//
// Hand-written rather than rendered from a route on purpose: it is the *pair* the
// rule is about, and no single route renders both — with the campaign routes
// wired, `/c/{slug}/search` now renders a second search landmark, but the header
// form is still suppressed there (`chrome.SearchForm.Action` is empty in
// production), so no single served document carries both. Keeping the fixture here
// means the landmark audit has an instance of the case regardless of which routes
// are wired.
//
// It carries a skip link and `.target` on every focus stop, because the fixture
// is audited by **every** §10.2 rule and not only the landmark one: a fixture that
// violates three unrelated rules fails the audit three times and buries the
// assertion it was written for. That is what happened the first time.
const searchLandmarkFixture = `<!doctype html>
<html lang="en"><head><title>Fixture</title></head><body>
<a class="skip-link target" href="#main">Skip to content</a>
<header role="banner"><form role="search" aria-label="Search pages" data-testid="header-search"><input class="target" type="search" name="q"/></form></header>
<main id="main" class="target" tabindex="-1"><h1>Fixture</h1>
<form role="search" aria-label="Search this campaign"><input class="target" type="search" name="q"/></form>
</main></body></html>`

// themeVariantsOf is the same document repeated once per entry in
// `themeCookieValues`, which is what a served route produces when it does not vary
// by the cookie.
//
// Five identical copies rather than one, and not for the sake of the count: the
// audit refuses to score a document that was rendered fewer times than the list
// names, because a fixture standing in for the byte-identity claim has to be
// *checked against* that claim or it is not standing in for it. The bytes here are
// the same for every cookie because the fixture is hand-written and reads no
// cookie at all — which is the property being asserted, arrived at honestly.
func themeVariantsOf(document string) [][]byte {
	variants := make([][]byte, 0, len(themeCookieValues))
	for range themeCookieValues {
		variants = append(variants, []byte(document))
	}

	return variants
}

// renderRoute GETs a path through a handler with a cookie.
func renderRoute(
	t *testing.T,
	handler http.Handler,
	where string,
	cookie *http.Cookie,
	path string,
) renderedRoute {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, pinned(withCookies(getRequest(t, path, nil), cookie, nil)))

	return routeFromRecorder(
		where,
		recorder,
		themeVariants(t, func(theme *http.Cookie) *httptest.ResponseRecorder {
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, pinned(withCookies(getRequest(t, path, nil), cookie, theme)))

			return out
		}),
	)
}

// auditRequestID is the correlation id every audited request carries.
//
// Present because a failure state's document **names its request id** — that is
// the whole content of the state, and it is how a reader's screenshot and the log
// line become the same request. Which means two requests for the same failing
// document produce two different documents for a reason that has nothing to do
// with S-13.5, and a byte-identity assertion that did not control for it would
// fail on every failure state and pass for the wrong reason on every other.
//
// `middleware.RequestID` honours a short printable-ASCII inbound id precisely so
// that a request is traceable across a reverse proxy, so pinning it here is the
// supported path rather than a trick. With it pinned, the **only** difference
// between the six requests behind one audited document is the theme cookie — which
// is what turns the comparison into a statement about S-13.5.
const auditRequestID = "audit-fixture-request"

// pinned stamps the fixed correlation id onto a request.
func pinned(request *http.Request) *http.Request {
	request.Header.Set(middleware.RequestIDHeader, auditRequestID)

	return request
}

// routeFromRecorder captures a response as an audited document, along with the same
// document rendered under each theme-cookie value.
//
// The variants are a parameter rather than something derived from the recorder,
// because a recorder holds one response and a theme cookie is a property of a
// *request*. The callers that already have the recorder therefore also have to
// supply the re-render, which is the point: a fixture cannot accidentally audit
// S-13.5 against a single value and call it a day.
func routeFromRecorder(
	where string,
	recorder *httptest.ResponseRecorder,
	themeVariants [][]byte,
) renderedRoute {
	return renderedRoute{
		where:  where,
		status: recorder.Code,
		body:   recorder.Body.Bytes(),
		// `Header.Values` rather than `Get`, because `Get` cannot tell "unset"
		// from "set to the empty string", and "no `Vary` header names the theme
		// cookie" is the requirement — an empty `Vary` is still a header, and a
		// cache reads it.
		vary:          strings.Join(recorder.Header().Values("Vary"), ", "),
		themeVariants: themeVariants,
	}
}

// themeCookieValues are the `sp_ui` values every audited document is re-rendered
// with.
//
// Five, and each one is a value the client can actually produce rather than a
// string invented to make the loop look thorough:
//
//   - the **absent** cookie, which is a reader who has expressed no preference and
//     is therefore the most common request this server will ever see;
//   - the default theme, named explicitly — a reader whose stored preference
//     happens to equal the default still sends the cookie, so "absent" and
//     "default" are two different requests that must produce the same bytes;
//   - a non-default theme, which is the one that would move `data-theme` if
//     anything read it;
//   - both fields at once, because the cookie carries two and a document that
//     reads one and not the other is a document that varies for half its readers;
//   - the television layout, because `[data-ui="tv"]` is the tier that changes the
//     grid most and is the one a server-side branch would most plausibly reach for.
var themeCookieValues = []string{
	"",
	"theme=system",
	"theme=dark",
	"theme=dark&ui=compact",
	"theme=light&ui=tv",
}

// themeVariants re-renders one document once per theme-cookie value.
//
// `send` is handed each theme cookie and returns the response it produced, so the
// caller keeps its own request shape — a `GET` with a session, an anonymous `GET`,
// a `POST` with a rejected credential — and this function stays a loop. Sharing
// the loop matters: the five values have to be the same five for every route, or
// "the document does not vary by the theme cookie" is five different claims
// depending on which document is being asked about.
func themeVariants(
	t *testing.T,
	send func(theme *http.Cookie) *httptest.ResponseRecorder,
) [][]byte {
	t.Helper()

	variants := make([][]byte, 0, len(themeCookieValues))

	for _, value := range themeCookieValues {
		recorder := send(themeCookie(value))
		variants = append(variants, recorder.Body.Bytes())
	}

	return variants
}

// themeCookie is the `sp_ui` cookie carrying a value, or no cookie at all for the
// empty value.
//
// A `*http.Cookie` with an empty `Value` is **not** the same thing, and the
// difference is the point of having the empty string in the list: `AddCookie` with
// an empty value writes `sp_ui=`, which is a present cookie with nothing in it, and
// a route that branches on the cookie's *presence* would take a different path from
// one that received no cookie at all. The absent case has to be genuinely absent.
func themeCookie(value string) *http.Cookie {
	if value == "" {
		return nil
	}

	return &http.Cookie{Name: auth.UICookieName, Value: value}
}

// withCookies attaches a session cookie and a theme cookie to a request, in that
// order, and **keeps both**.
//
// Written as a function over the request rather than as a "combine these two
// cookies into one" helper because that is where this went wrong first. A request
// carries any number of cookies, so there is nothing to combine — and the earlier
// version returned a single cookie built from the session's name and value, which
// silently dropped the theme cookie on every signed-in request. The audit then
// compared five copies of the *same* request, found them all equal, and reported
// that no document varies by the theme cookie. It was green because it was not
// looking.
//
// That is the whole reason a gate is mutation-checked: a rule that cannot fire is
// indistinguishable from a rule that holds, and the only way to tell them apart is
// to make the route read the cookie and require the gate to notice.
//
// A `nil` session with a non-nil theme is a real reader and is the more
// interesting case of the two — somebody who set a theme before signing in — so it
// is representable rather than papered over.
func withCookies(request *http.Request, session, theme *http.Cookie) *http.Request {
	for _, cookie := range []*http.Cookie{session, theme} {
		if cookie != nil {
			request.AddCookie(cookie)
		}
	}

	return request
}

// anonymousGet GETs a path through a handler with no credential.
func anonymousGet(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, getRequest(t, path, nil))

	return recorder
}

// signInForm builds a credential pair.
func signInForm(username, password string) map[string][]string {
	return map[string][]string{"username": {username}, "password": {password}}
}

// failer is the subset of *testing.T the audit actually uses.
//
// An interface rather than `*testing.T` because the audit's own tests have to
// *test* it, and testing a `*testing.T`-typed function means a real failure ends
// the test. `countingT` below satisfies this and records instead of reporting —
// which is the only way to assert that a rule fires without asserting it fires on
// the fixture you happened to write.
//
// Deliberately not including `Fatalf`: the audit's own constructor aborts on an
// unparseable document, and that behaviour should stay unreachable from a
// sub-audit rather than be faked.
type failer interface {
	Helper()
	Errorf(format string, args ...any)
}

// audit is one parsed document, plus the two response properties a DOM cannot
// carry.
type audit struct {
	t      failer
	where  string
	status int
	// varyHeader is the response's `Vary`, captured as a string because the
	// *field names* in it are the finding and a DOM cannot hold a header. A
	// `Vary: Cookie` here is correct — the shell carries the reader's name and
	// a sign-out form — and a `Vary` naming the theme cookie is not. See
	// `assertNoVaryOnTheThemeCookie`.
	varyHeader string
	// themeVariants is the same document rendered once per `sp_ui` value, and
	// is what makes S-13.5 checkable: the claim is about five requests, so a
	// single response cannot establish it.
	themeVariants [][]byte
	// document is the bytes the variants are compared against. Separate from
	// `body`, which is the *parsed* `<body>` element: a re-parse is a
	// normalisation, and this comparison is about bytes on the wire, so
	// normalising first would hide exactly the difference it is looking for.
	document []byte
	root     *html.Node
	body     *html.Node
	head     *html.Node
}

// newAudit parses a rendered document.
func newAudit(t *testing.T, route renderedRoute) *audit {
	t.Helper()

	if !strings.Contains(strings.ToLower(string(route.body)), "<html") {
		t.Fatalf("%s: the response is not an HTML document (status %d): %s",
			route.where, route.status, firstBytes(string(route.body)))
	}

	root, err := html.Parse(strings.NewReader(string(route.body)))
	if err != nil {
		t.Fatalf("%s: parse rendered document: %v", route.where, err)
	}

	return &audit{
		t:             t,
		where:         route.where,
		status:        route.status,
		varyHeader:    route.vary,
		themeVariants: route.themeVariants,
		document:      route.body,
		root:          root,
		head:          findElement(root, "head"),
		body:          findElement(root, "body"),
	}
}

// --- The walk ---------------------------------------------------------------

// elements calls visit for every element, in document order.
//
// Document order rather than depth-first by branch: §10.2's skip-link rule and
// §7.2's "the first focusable elements in the document" are both statements about
// the order a reader meets things, and a walk that finished the first element's
// whole subtree before visiting the second would report a different order than a
// keyboard gets.
func (a *audit) elements(visit func(*html.Node)) {
	a.t.Helper()

	var walk func(node *html.Node)
	walk = func(node *html.Node) {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)

			if child.Type == html.ElementNode {
				visit(child)
			}
		}
	}
	walk(a.root)
}

// focusable returns every element a keyboard can reach, in document order.
func (a *audit) focusable() []*html.Node {
	a.t.Helper()

	var found []*html.Node

	a.elements(func(node *html.Node) {
		if isFocusable(node) {
			found = append(found, node)
		}
	})

	return found
}

// attribute returns an attribute's value, or "" when absent.
func attribute(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}

	return ""
}

// hasAttribute reports whether an attribute is present, whatever its value.
//
// Separate from `attribute != ""` because `aria-expanded=""` and
// `aria-expanded="false"` are different states, and an empty attribute value is
// almost never what the author meant.
func hasAttribute(node *html.Node, name string) bool {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return true
		}
	}

	return false
}

// hasClassToken reports whether an element's class list contains a token.
//
// Split on whitespace and compared exactly, never `strings.Contains`: `target` is
// a substring of `data-target` and of `target-large`, and §7.3's contract is about
// the *class*.
func hasClassToken(node *html.Node, token string) bool {
	for field := range strings.FieldsSeq(attribute(node, "class")) {
		if field == token {
			return true
		}
	}

	return false
}

// isFocusable reports whether a keyboard can reach an element.
func isFocusable(node *html.Node) bool {
	if node.Type != html.ElementNode {
		return false
	}

	switch node.DataAtom {
	case atom.A:
		return hasAttribute(node, "href")
	case atom.Button, atom.Input, atom.Select, atom.Textarea, atom.Summary:
		return true
	default:
		return hasAttribute(node, "tabindex")
	}
}

// findElement returns the first element with a tag name, or nil.
func findElement(node *html.Node, tag string) *html.Node {
	if node.Type == html.ElementNode && node.Data == tag {
		return node
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findElement(child, tag); found != nil {
			return found
		}
	}

	return nil
}

// findElements returns every element with a tag name, in document order.
func findElements(node *html.Node, tag string) []*html.Node {
	var found []*html.Node

	var walk func(*html.Node)
	walk = func(current *html.Node) {
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && child.Data == tag {
				found = append(found, child)
			}

			walk(child)
		}
	}
	walk(node)

	return found
}

// hasAncestor reports whether any ancestor has the given tag name.
func hasAncestor(node *html.Node, tag string) bool {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && ancestor.Data == tag {
			return true
		}
	}

	return false
}

// elementByID returns the element carrying an id, or nil.
func elementByID(root *html.Node, id string) *html.Node {
	var found *html.Node

	var walk func(*html.Node)
	walk = func(node *html.Node) {
		for child := node.FirstChild; child != nil && found == nil; child = child.NextSibling {
			if child.Type == html.ElementNode && attribute(child, "id") == id {
				found = child

				return
			}

			walk(child)
		}
	}
	walk(root)

	return found
}

// elementPath is a readable location for a failure message: the nearest four
// ancestors, innermost first, each labelled by id, test hook or first class.
func elementPath(node *html.Node) string {
	parts := []string{}

	for current := node; current != nil && current.Type == html.ElementNode; current = current.Parent {
		label := current.Data

		switch {
		case attribute(current, "id") != "":
			label += "#" + attribute(current, "id")
		case attribute(current, "data-testid") != "":
			label += "[" + attribute(current, "data-testid") + "]"
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

// roleOf returns an element's explicit role, or the implicit role its tag carries.
//
// The implicit half is not a convenience: §7.2's contract is written as
// `header[banner]` and `footer[contentinfo]`, and a document relying on the
// implicit role is correct. The audit has to agree with the specification, not
// with the subset somebody wrote an attribute for.
func roleOf(node *html.Node) string {
	if explicit := attribute(node, "role"); explicit != "" {
		return explicit
	}

	switch node.DataAtom {
	case atom.Header:
		return "banner"
	case atom.Footer:
		return "contentinfo"
	case atom.Nav:
		return "navigation"
	case atom.Main:
		return "main"
	case atom.Aside:
		return "complementary"
	case atom.Form:
		// A form is a landmark only when it carries an explicit role. An
		// unlabelled form is not one, and treating it as `form` here would invent
		// a landmark the specification does not have.
		return ""
	default:
		return ""
	}
}

// textContent returns an element's descendant text.
//
// Text nodes only: an `alt` or a `title` is text a reader hears and is not in the
// text content, and §10.2's vocabulary rule is about *rendered strings*. The
// attribute scan below covers the rest of the document.
func textContent(node *html.Node) string {
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

// --- §10.2: the seven rules, one subtest each ------------------------------

// TestEveryRouteSatisfiesTheStructuralContract is UI §10.2, gate-blocking.
//
// Each rule in its own subtest, so a failure names the rule rather than "the a11y
// test failed". The rules are §10.2's own list, in its order, plus the two it
// implies and does not spell out — heading levels and reference integrity —
// because a landmark can be present and correctly labelled while the document's
// outline and its ARIA wiring are both broken, and neither shows up in a
// "landmarks are fine" result.
func TestEveryRouteSatisfiesTheStructuralContract(t *testing.T) {
	t.Parallel()

	for _, route := range auditedRoutes(t) {
		t.Run(route.where, func(t *testing.T) {
			t.Parallel()

			audit := newAudit(t, route)

			t.Run("ExactlyOneH1", func(t *testing.T) {
				assertExactlyOneH1(t, audit)
			})
			t.Run("HeadingLevelsNeverSkip", func(t *testing.T) {
				assertHeadingLevelsNeverSkip(t, audit)
			})
			t.Run("NoHeadingIsRepeated", func(t *testing.T) {
				assertNoHeadingIsRepeated(t, audit)
			})
			t.Run("LandmarksArePresentAndDistinct", func(t *testing.T) {
				assertLandmarks(t, audit)
			})
			t.Run("NoPositiveTabindex", func(t *testing.T) {
				assertNoPositiveTabindex(t, audit)
			})
			t.Run("NoAriaHiddenOnAFocusableElement", func(t *testing.T) {
				assertNoAriaHiddenOnFocusable(t, audit)
			})
			t.Run("SkipLinksComeFirstAndResolve", func(t *testing.T) {
				assertSkipLinks(t, audit)
			})
			t.Run("EveryTestIDResolves", func(t *testing.T) {
				assertTestIDsResolve(t, audit)
			})
			t.Run("NoRetiredEntityIsNamed", func(t *testing.T) {
				assertVocabulary(t, audit)
			})
			t.Run("EveryFocusableElementCarriesTarget", func(t *testing.T) {
				assertEveryFocusableCarriesTarget(t, audit)
			})
			t.Run("EveryReferenceResolves", func(t *testing.T) {
				assertEveryReferenceResolves(t, audit)
			})
			t.Run("TheDocumentDoesNotVaryByTheThemeCookie", func(t *testing.T) {
				assertNoVaryOnTheThemeCookie(t, audit)
			})
		})
	}
}

// assertExactlyOneH1 is §7.2's first structural rule.
//
// Exactly one, not at least one. Two `<h1>`s tell a screen reader the page has two
// titles; none tell it the page has none while looking complete to a sighted
// reader.
func assertExactlyOneH1(t failer, audit *audit) {
	t.Helper()

	headings := findElements(audit.root, "h1")
	if len(headings) == 1 {
		return
	}

	names := make([]string, 0, len(headings))
	for _, heading := range headings {
		names = append(names, elementPath(heading))
	}

	t.Errorf("%s: the document has %d <h1> elements, want exactly 1 (UI §7.2); found: %s",
		audit.where, len(headings), strings.Join(names, ", "))
}

// assertHeadingLevelsNeverSkip is §7.2's second structural rule.
//
// Separate from the `<h1>` count because it can fail where the count passes: a
// document whose headings start at `<h3>` has exactly one `<h1>`-shaped hole and
// no valid outline at all.
func assertHeadingLevelsNeverSkip(t failer, audit *audit) {
	t.Helper()

	previous := 0

	audit.elements(func(node *html.Node) {
		if len(node.Data) != 2 || node.Data[0] != 'h' {
			return
		}

		level, err := strconv.Atoi(node.Data[1:])
		if err != nil || level < 1 || level > 6 {
			return
		}

		if previous != 0 && level > previous+1 {
			t.Errorf("%s: a heading jumps from h%d to h%d at %s; a skipped level "+
				"is announced as a missing section (UI §7.2)",
				audit.where, previous, level, elementPath(node))
		}

		previous = level
	})
}

// landmark is one landmark region and how it is named.
type landmark struct {
	role string
	// label is the accessible name, following `aria-labelledby` when that is
	// what it uses.
	label string
	// namedBy records the mechanism, so a failure can say "labelled by nothing"
	// rather than "labelled by the empty string".
	namedBy string
	node    *html.Node
}

// landmarks returns the document's landmark regions in document order.
//
// The set is §7.2's own diagram: banner, navigation, main, complementary,
// contentinfo, and an explicit `search` role. Anything else is not a landmark.
func landmarks(audit *audit) []landmark {
	var found []landmark

	audit.elements(func(node *html.Node) {
		role := roleOf(node)
		if role == "" {
			return
		}

		switch role {
		case "banner", "navigation", "main", "complementary", "contentinfo", "search":
			found = append(found, landmark{
				role:    role,
				label:   accessibleName(audit, node),
				namedBy: namingMechanism(node),
				node:    node,
			})
		default:
		}
	})

	return found
}

// namingMechanism records which attribute named a landmark.
func namingMechanism(node *html.Node) string {
	if attribute(node, "aria-label") != "" {
		return "aria-label"
	}

	if hasAttribute(node, "aria-labelledby") {
		return "aria-labelledby"
	}

	return "nothing"
}

// accessibleName returns a landmark's name, following `aria-labelledby` when used.
func accessibleName(audit *audit, node *html.Node) string {
	if label := attribute(node, "aria-label"); label != "" {
		return label
	}

	var parts []string

	for id := range strings.FieldsSeq(attribute(node, "aria-labelledby")) {
		if target := elementByID(audit.root, id); target != nil {
			parts = append(parts, strings.TrimSpace(textContent(target)))
		}
	}

	return strings.Join(parts, " ")
}

// assertLandmarks is §7.2's second rule, in four parts.
//
// Presence, because a missing landmark is a region a landmark-navigation reader
// cannot jump to. Distinguishing labels, because two landmarks of the same role
// with the same name make landmark navigation useless — §7.2 calls that a gate
// failure, not a style nit, and the compact bottom bar is the case it exists for.
// Exactness, for the three the record writes out. And nesting, because a
// `contentinfo` inside a `main` is not the document's contentinfo and a screen
// reader's landmark list will say otherwise.
func assertLandmarks(t failer, audit *audit) {
	t.Helper()

	found := landmarks(audit)

	present := map[string]int{}
	for _, region := range found {
		present[region.role]++
	}

	// headerSearch counts the `search` landmarks carrying the header's reserved
	// name, and exists so that "more than one" is a finding. §7.2 permits one.
	// Zero is **not** a finding today, and `headerSearchLabel`'s comment says why.
	headerSearch := 0

	// `main` is the only landmark required unconditionally. §4.6 removes the
	// navigation before a campaign exists, and §3.1's tiers move the rail out of
	// the flow rather than out of the document — the rail stays in the
	// accessibility tree at every tier so a script-less reader does not lose it,
	// which shell.css states at the rule that moves it off-screen.
	if present["main"] == 0 {
		t.Errorf("%s: the document has no main landmark (UI §7.2)", audit.where)
	}

	if present["main"] > 1 {
		t.Errorf("%s: the document has %d main landmarks, want 1 (UI §7.2)",
			audit.where, present["main"])
	}

	for _, region := range found {
		switch region.role {
		case "navigation", "complementary", "search":
			if region.label == "" {
				t.Errorf(
					"%s: the %s landmark at %s is labelled by %s; §7.2 requires a "+
						"distinguishing label, and an unnamed landmark cannot be told "+
						"from another of the same role",
					audit.where, region.role, elementPath(region.node), region.namedBy,
				)
			}
		case "banner", "contentinfo":
		default:
		}
	}

	// The two the record writes out, exactly. Both navigation names are checked
	// as a set rather than positionally, because which of them comes first is a
	// tier decision the stylesheet makes and the markup does not.
	allowedNavigation := map[string]bool{"Campaign": true, "Primary": true}

	for _, region := range found {
		switch region.role {
		case "navigation":
			if !allowedNavigation[region.label] {
				t.Errorf("%s: a navigation landmark is labelled %q; UI §7.2 names "+
					"them \"Campaign\" (the campaign nav) and \"Primary\" (the compact bar)",
					audit.where, region.label)
			}
		case "complementary":
			if region.label != "Utilities" {
				t.Errorf("%s: the complementary landmark is labelled %q, want %q (UI §7.2)",
					audit.where, region.label, "Utilities")
			}
		case "search":
			// §7.2 reserves "Search pages" for the **header's** search form, and
			// the reservation is what stops the search route's own form colliding
			// with it. So a `search` landmark carrying that name must *be* the
			// header's: `data-testid="header-search"` is the hook the header
			// component puts on it, and there is exactly one header per document.
			//
			// **The converse is not asserted, and cannot be.** The header's search
			// zone renders only when `chrome.SearchForm.Action` is non-empty
			// (`header.templ`), and nothing in production populates it — the search
			// box in the banner is not in any document this server serves today, and
			// only the chrome's own tests set the field. So a rule demanding that
			// name be present would fail on every route for a reason that has
			// nothing to do with what §10.2 is checking, and would be switched off
			// rather than fixed. Asserting the name is reserved is the half that
			// holds today and that keeps holding once the zone is wired; the
			// presence of the zone belongs to whichever work item populates
			// `ShellView.Search`, and this file will start asserting it then.
			if region.label != headerSearchLabel {
				continue
			}

			headerSearch++

			if testID := attribute(region.node, "data-testid"); testID != headerSearchHook {
				t.Errorf("%s: a search landmark is labelled %q but is %s; §7.2 reserves "+
					"that name for the header's own search form, and a route's form "+
					"wearing it collides with the banner's in landmark navigation",
					audit.where, region.label, elementPath(region.node))
			}
		case "banner", "contentinfo", "main":
		default:
		}
	}

	if headerSearch > 1 {
		t.Errorf("%s: %d search landmarks are labelled %q; §7.2 gives that name to the "+
			"header's search form, and a document has one header",
			audit.where, headerSearch, headerSearchLabel)
	}

	// Distinctness across the whole document.
	seen := map[string]string{}

	for _, region := range found {
		if region.label == "" {
			continue
		}

		key := region.role + "=" + region.label
		if first, duplicate := seen[key]; duplicate {
			t.Errorf("%s: two %s landmarks are both labelled %q (%s and %s); "+
				"§7.2 requires distinguishing labels or landmark navigation is useless",
				audit.where, region.role, region.label, first, elementPath(region.node))
		}

		seen[key] = elementPath(region.node)
	}

	for _, region := range found {
		if region.role == "contentinfo" && hasAncestor(region.node, "main") {
			t.Errorf("%s: a contentinfo landmark is inside <main> at %s; it is the "+
				"page's footer, not the article's (UI §7.2)",
				audit.where, elementPath(region.node))
		}
	}
}

// assertNoHeadingIsRepeated is the rule §7.2 does not state and §4.6's
// composition makes necessary.
//
// The shell has one centre slot and one rail. §4.2 puts a persistent "Not working"
// warning in the footer, and the pre-campaign rail carries its own notice under the
// same heading — so a composition that passes the subsystems to *both* renders
// `<h2>Not working</h2>` twice in one document.
//
// §7.2 requires exactly one `<h1>` and that heading levels never skip, and a
// repeated heading satisfies both. It is still wrong: a screen reader's heading
// list is the reader's table of contents, and two identical entries in it point at
// two different regions, so a reader navigating by heading arrives at one and has
// no way to know the other exists. Nothing else in the phase can see it — the
// count is right, the levels are right, and both copies render correctly.
//
// The rule is general rather than named after one string: two headings with the
// same text in one document of this shell is always a defect, and a rule about one
// string would be re-armed by a rename.
func assertNoHeadingIsRepeated(t *testing.T, audit *audit) {
	t.Helper()

	seen := map[string]*html.Node{}

	audit.elements(func(node *html.Node) {
		if len(node.Data) != 2 || node.Data[0] != 'h' {
			return
		}

		text := strings.TrimSpace(textContent(node))
		if text == "" {
			return
		}

		if first, repeated := seen[text]; repeated {
			t.Errorf("%s: the heading %q appears twice, at %s and %s. A heading "+
				"list is a reader's table of contents, and two identical entries "+
				"point at two different regions — §4.2 puts the degraded warning in "+
				"the footer while the rail carries its own notice, and a composition "+
				"that renders both meets the reader with the same heading twice",
				audit.where, text, elementPath(first), elementPath(node))

			return
		}

		seen[text] = node
	})
}

// assertNoPositiveTabindex is §7.4's "no positive tabindex, anywhere — gate
// failure".
//
// Zero is excluded too. §7.4 says Tab follows natural document order, and a
// `tabindex="0"` moves one element to the front of the focus order while the
// markup still reads in the original order — so the two disagree and neither a
// reader nor a test can tell which one the page means. Only `-1` is usable: it
// makes an element programmatically focusable for a skip link without putting it
// in the tab sequence.
func assertNoPositiveTabindex(t failer, audit *audit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		raw := attribute(node, "tabindex")
		if raw == "" {
			return
		}

		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			t.Errorf("%s: %s carries tabindex=%q, which is not an integer",
				audit.where, elementPath(node), raw)

			return
		}

		if value > -1 {
			t.Errorf("%s: %s carries tabindex=%d; only -1 is permitted. 0 moves the "+
				"element to the front of the tab order while the markup still reads "+
				"in document order (UI §7.4, §10.2)",
				audit.where, elementPath(node), value)
		}
	})
}

// assertNoAriaHiddenOnFocusable is §7.10's rule, asserted on the *combination*.
//
// Not on the attribute: §7.10 prohibits `aria-hidden` on an element that can
// receive focus, and the legitimate case in this shell is a navigation's icons —
// an `<svg aria-hidden="true">` inside a focusable link. The attribute alone is
// therefore not the finding; the attribute on something a keyboard can reach is.
//
// Two cases, and the second is the one that catches real bugs: a focusable
// element *inside* an `aria-hidden` subtree is just as unreachable, and the
// offending attribute is on an ancestor several levels up.
func assertNoAriaHiddenOnFocusable(t failer, audit *audit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		if attribute(node, "aria-hidden") != "true" {
			return
		}

		if isFocusable(node) {
			t.Errorf("%s: %s is focusable and aria-hidden; a control a screen reader "+
				"cannot see is one that reader cannot reach (UI §7.10)",
				audit.where, elementPath(node))
		}

		if containsFocusable(node) {
			t.Errorf("%s: %s is aria-hidden and contains focusable elements, so "+
				"everything inside it is unreachable to a screen reader (UI §7.10)",
				audit.where, elementPath(node))
		}

		for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
			if ancestor.Type == html.ElementNode &&
				attribute(ancestor, "aria-hidden") == "true" && containsFocusable(ancestor) {
				t.Errorf("%s: %s is inside the aria-hidden subtree at %s, which "+
					"contains focusable elements; everything focusable inside it is "+
					"unreachable to a screen reader (UI §7.10)",
					audit.where, elementPath(node), elementPath(ancestor))

				return
			}
		}
	})
}

// containsFocusable reports whether a subtree holds anything a keyboard can reach.
func containsFocusable(node *html.Node) bool {
	found := false

	var walk func(*html.Node)
	walk = func(current *html.Node) {
		for child := current.FirstChild; child != nil && !found; child = child.NextSibling {
			if isFocusable(child) {
				found = true

				return
			}

			walk(child)
		}
	}
	walk(node)

	return found
}

// skipLink is one skip link and what it points at.
type skipLink struct {
	text string
	href string
	node *html.Node
}

// skipLinks returns the document's skip links, in document order.
//
// Identified by class rather than by href. §7.2 fixes the targets, and the class
// is the hook the stylesheet uses to move the link off-screen until it is
// focused — so a "skip" link without the class is not off-screen and is therefore
// in the wrong place, and reading it from the class is the same claim made from
// the other end.
func skipLinks(audit *audit) []skipLink {
	var found []skipLink

	audit.elements(func(node *html.Node) {
		if node.DataAtom != atom.A || !hasClassToken(node, "skip-link") {
			return
		}

		found = append(found, skipLink{
			text: strings.TrimSpace(textContent(node)),
			href: attribute(node, "href"),
			node: node,
		})
	})

	return found
}

// assertSkipLinks is §7.2's ordering rule and §10.2's "skip links first in tab
// order", in three parts.
//
// Position — the links must be the first focusable elements in the document. A
// skip link after the banner is not a skip link, it is a link named "skip".
//
// Resolution — each `href="#id"` must resolve, and to a focusable landmark. A
// skip link to a landmark that is not in the document moves focus nowhere, which
// costs a reader a keypress and teaches them the skip links are unreliable.
//
// Order — §7.2's own sequence, and the campaign-navigation link only where the
// navigation exists. §4.6 removes the nav before a campaign does, and §7.2 says
// the link appears "only where the nav exists", so the link and the landmark are
// one condition expressed in two places. That is the exact thing a test is for.
func assertSkipLinks(t failer, audit *audit) {
	t.Helper()

	links := skipLinks(audit)
	if len(links) == 0 {
		t.Errorf("%s: the document has no skip link; §7.2 makes them the first "+
			"focusable elements and this shell always has at least one", audit.where)

		return
	}

	focusable := audit.focusable()

	for index, link := range links {
		if index >= len(focusable) || focusable[index] != link.node {
			t.Errorf("%s: skip link %d (%q) is not focusable element %d; §7.2 puts "+
				"the skip links first in tab order, before the banner",
				audit.where, index+1, link.text, index+1)

			continue
		}

		fragment, isFragment := strings.CutPrefix(link.href, "#")
		if !isFragment || fragment == "" {
			t.Errorf("%s: skip link %q has href %q; §7.2's links are fragments",
				audit.where, link.text, link.href)

			continue
		}

		target := elementByID(audit.root, fragment)
		if target == nil {
			t.Errorf("%s: skip link %q points at #%s, which is not in the document; "+
				"§4.6 removes some landmarks per route, so the link has to go with them",
				audit.where, link.text, fragment)

			continue
		}

		if !hasAttribute(target, "tabindex") {
			t.Errorf("%s: skip link %q lands on %s, which has no tabindex; without "+
				"one the target is not focusable and focus lands at the top of a "+
				"scroll container instead of on an announced element (UI §7.2)",
				audit.where, link.text, elementPath(target))
		}

		if roleOf(target) == "" {
			t.Errorf("%s: skip link %q lands on %s, which is not a landmark; a skip "+
				"link's target is the region it names (UI §7.2)",
				audit.where, link.text, elementPath(target))
		}
	}

	// §7.2's order, over the links that are present.
	contentAt, navigationAt, utilitiesAt := -1, -1, -1

	for index, link := range links {
		switch {
		case strings.Contains(link.text, "content"):
			contentAt = index
		case strings.Contains(link.text, "navigation"):
			navigationAt = index
		case strings.Contains(link.text, "utilities"):
			utilitiesAt = index
		}
	}

	if contentAt != 0 {
		t.Errorf("%s: the first skip link is %q; §7.2's order is content, campaign "+
			"navigation (where the nav exists), utilities",
			audit.where, skipLinkText(links, 0))
	}

	if navigationAt > 0 && navigationAt < contentAt {
		t.Errorf("%s: the campaign-navigation skip link precedes the content link; "+
			"§7.2's order is content first", audit.where)
	}

	if utilitiesAt >= 0 && utilitiesAt < contentAt {
		t.Errorf("%s: the utilities skip link precedes the content link; §7.2's "+
			"order is content first", audit.where)
	}

	// §4.6's half: the navigation skip link and the navigation landmark are one
	// condition in two places, and this is what catches them disagreeing. Both
	// directions, because "a link with no landmark" and "a landmark with no link"
	// are both failures and only one of them is visible in a rendering.
	hasNavLandmark := elementByID(audit.root, "nav") != nil
	hasNavLink := navigationAt >= 0

	if hasNavLandmark && !hasNavLink {
		t.Errorf("%s: the document has a navigation landmark but no skip link to "+
			"it; §7.2 lists \"Skip to campaign navigation\" -> #nav as the third "+
			"skip link wherever the nav exists", audit.where)
	}

	if hasNavLink && !hasNavLandmark {
		t.Errorf("%s: the document has a skip link to #nav but no navigation "+
			"landmark; §4.6 removes the nav before a campaign exists and a skip "+
			"link to an absent landmark moves focus nowhere", audit.where)
	}
}

// skipLinkText names the nth skip link for a failure message.
func skipLinkText(links []skipLink, index int) string {
	if index >= len(links) {
		return "(none)"
	}

	return links[index].text
}

// assertTestIDsResolve is §10.2's "every data-testid present", read as
// well-formedness.
//
// The two properties a hook must have to be usable at all: a non-empty value, and
// appearing once in the document. The per-route *enumeration* — which hooks this
// phase promises — lives with each route's own test, where a reader can see the
// list; what is asserted here is the property that makes an enumeration
// meaningful, and the reason is in the failure message: a hook that appears twice
// selects nothing.
func assertTestIDsResolve(t failer, audit *audit) {
	t.Helper()

	counts := map[string]int{}

	audit.elements(func(node *html.Node) {
		// An *absent* hook is not an empty one. Counting the empty string would
		// report every element without a hook as a duplicate of every other, and
		// the count would be the element count of the document — which is how this
		// first reported fifteen duplicates on a correct shell.
		if id := attribute(node, "data-testid"); id != "" {
			counts[id]++
		}
	})

	duplicates := make([]string, 0)

	for id, count := range counts {
		if count > 1 {
			duplicates = append(duplicates, id+" x"+strconv.Itoa(count))
		}
	}

	sort.Strings(duplicates)

	if len(duplicates) > 0 {
		t.Errorf("%s: these data-testid hooks appear more than once: %s; a hook that "+
			"appears twice selects nothing (§10.2)",
			audit.where, strings.Join(duplicates, ", "))
	}
}

// assertVocabulary is UI §1.2 and §10.2's last clause: no rendered string
// contains "world" or "session".
//
// The scan covers **every text node, every attribute value, every attribute name
// and every HTML comment**, and each inclusion has a reason.
//
// Comments: a word in a comment is invisible to a reader, which is exactly why a
// gate reading only text nodes would pass on a document whose markup still names a
// retired entity. `TestTheVocabularyAuditFindsTheWordWhereverItIs` feeds this
// audit six ways of smuggling one in and requires it to object to each.
//
// Attribute values: an `aria-label` or a `title` is announced to a reader, so a
// retired entity named there is named to the person the rule protects.
//
// Attribute names: a `data-world` is invisible to a reader and obvious to a
// developer grepping for the feature it implies, which makes it exactly as retired
// as a label.
func assertVocabulary(t failer, audit *audit) {
	t.Helper()

	report := func(where, text string) {
		lowered := strings.ToLower(text)

		for _, forbidden := range forbiddenVocabulary {
			if strings.Contains(lowered, forbidden) {
				t.Errorf("%s: %s contains %q; neither entity exists, and §1.2 makes "+
					"the words appear nowhere in the interface",
					audit.where, where, forbidden)
			}
		}
	}

	var walk func(node *html.Node)
	walk = func(node *html.Node) {
		switch node.Type {
		case html.TextNode:
			report("a text node", node.Data)
		case html.CommentNode:
			report("an HTML comment", node.Data)
		case html.ElementNode:
			for _, attr := range node.Attr {
				report("the attribute "+attr.Key, attr.Val)
				report("an attribute name", attr.Key)
			}
		case html.DoctypeNode:
		default:
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(audit.root)
}

// assertEveryFocusableCarriesTarget is §7.3's "enforced by construction" made
// checkable, and §10.6's "grep rendered markup for interactive elements missing
// the class".
//
// §10.6 says *including plugin output*. A plugin component's markup is in the
// same tree as everything else's, so a plugin rendering a button without `.target`
// fails this test exactly as a shell component would. The residual is stated
// rather than hidden: a plugin that injects markup by a mechanism bypassing the
// document tree is not visible to a Go test over parsed HTML, and §4.11.1 makes
// the client's involvement in that a contract rather than an implementation.
func assertEveryFocusableCarriesTarget(t failer, audit *audit) {
	t.Helper()

	for _, node := range audit.focusable() {
		if hasClassToken(node, "target") {
			continue
		}

		t.Errorf("%s: %s is focusable and does not carry the .target class; §7.3 "+
			"enforces the --target-min minimum by construction and §10.6 audits for it",
			audit.where, elementPath(node))
	}
}

// assertEveryReferenceResolves is the ARIA integrity rule §10.2's list implies
// and does not spell out: a document whose `aria-controls` points at nothing has a
// control claiming to control something invisible.
//
// All five reference attributes, plus `<label for>` and `href="#…"`. The id
// uniqueness half is not incidental: two elements sharing an id make every
// reference to it ambiguous, and a screen reader resolves the ambiguity by picking
// the first — so a control can end up controlling the wrong element with nothing
// in the markup to say so.
func assertEveryReferenceResolves(t failer, audit *audit) {
	t.Helper()

	ids := map[string]int{}

	audit.elements(func(node *html.Node) {
		if id := attribute(node, "id"); id != "" {
			ids[id]++
		}
	})

	for id, count := range ids {
		if count > 1 {
			t.Errorf("%s: id %q appears %d times; every reference to it is ambiguous "+
				"and a screen reader resolves the ambiguity by picking the first (UI §7.2)",
				audit.where, id, count)
		}
	}

	references := []string{
		"aria-controls",
		"aria-labelledby",
		"aria-describedby",
		"aria-owns",
		"aria-activedescendant",
	}

	audit.elements(func(node *html.Node) {
		for _, name := range references {
			value := attribute(node, name)
			if value == "" {
				continue
			}

			for id := range strings.FieldsSeq(value) {
				if elementByID(audit.root, id) == nil {
					t.Errorf("%s: %s carries %s=%q, which resolves to no element; a "+
						"control that claims to control something invisible is a "+
						"control that lies (UI §7.2)",
						audit.where, elementPath(node), name, id)
				}
			}
		}

		// §7.7: a real `<label for>`, never a placeholder. A `for` pointing at
		// nothing is a field with no accessible name.
		if node.DataAtom == atom.Label {
			if target := attribute(node, "for"); target != "" &&
				elementByID(audit.root, target) == nil {
				t.Errorf("%s: %s has for=%q, which resolves to no element; the field "+
					"has no accessible name (UI §7.7)",
					audit.where, elementPath(node), target)
			}
		}

		if node.DataAtom == atom.A {
			if fragment, isFragment := strings.CutPrefix(
				attribute(node, "href"),
				"#",
			); isFragment &&
				fragment != "" {
				if elementByID(audit.root, fragment) == nil {
					t.Errorf("%s: %s has href=\"#%s\", which resolves to no element",
						audit.where, elementPath(node), fragment)
				}
			}
		}
	})
}

// assertNoVaryOnTheThemeCookie is S-13.5 and UI §6.6, in the two halves the
// property actually has.
//
// # What it used to assert, and why that was wrong
//
// This rule used to read: *no `Vary` header may be emitted at all*. That was a
// claim ADR 0035 recorded and a later correction to that record withdrew — the
// wiki route, the search route, the editor and the asset route all carry
// `Vary: Cookie`, **deliberately**, because the shell around their content carries
// the reader's name and a sign-out form, so two 200 responses to one URL really do
// differ. Measured: 4460 bytes for a GM, 4227 for an anonymous reader. ADR 0035's
// "no `Vary` anywhere" was false and the record now says so.
//
// So the absence was the wrong rule twice over. It forbade a header that is
// required, and — this is the part that matters — **a test asserting a header's
// absence cannot see the variation that header was protecting.** It would have
// passed on a document that emitted `Vary: Cookie` and then branched on the theme
// cookie, because a branch that comes with a `Vary` still has a `Vary`.
//
// # What it asserts now
//
// Two things, and the second is the substantive one:
//
//  1. **No `Vary` names the theme cookie.** Not "no `Vary`" — "no `Vary: sp_ui`".
//     A `Vary` on `Cookie` is correct here and is what every campaign route emits;
//     a `Vary` naming `sp_ui` would be a promise the document does not keep,
//     because §3.7's resolver writes `data-theme` and `data-ui` client-side before
//     the first paint and the server never sees the preference.
//
//  2. **The bytes are identical across five `sp_ui` values.** This is the check
//     that can fail, and it is the one that catches a route reading the cookie.
//     `themeCookieValues` names the values and `renderedRoute.themeVariants`
//     carries the re-renders: the absent cookie, the default theme named
//     explicitly, a non-default theme, both fields at once, and the television
//     layout. A document that varies for one of those is a document whose theme is
//     decided twice — once client-side before the first paint and once server-side
//     after it — and the reader sees the second one win.
func assertNoVaryOnTheThemeCookie(t failer, audit *audit) {
	t.Helper()

	// Half one: the header, on the recorder, because a DOM cannot hold it.
	//
	// Matched against the cookie's *name* rather than against the word "Cookie", so
	// a `Vary: sp_ui` is caught even though it is not what anybody would write. A
	// `Vary` is a comma-separated list of field names and this is the one field
	// name in it that must never appear.
	if field := varyFieldNaming(audit.varyHeader, auth.UICookieName); field != "" {
		t.Errorf("%s: the response carries Vary naming %q (%s); the document does not "+
			"vary by that cookie — §3.7 resolves both root attributes client-side "+
			"before the first paint, so promising a variation here is a promise the "+
			"response cannot keep (UI §6.6, S-13.5)",
			audit.where, field, audit.varyHeader)
	}

	// Half two: the bytes, which is where a reader would actually see it.
	if len(audit.themeVariants) != len(themeCookieValues) {
		t.Errorf("%s: %d theme variants were rendered, want %d; a document audited "+
			"against fewer cookie values than the list names is not audited against "+
			"the claim it is standing in for",
			audit.where, len(audit.themeVariants), len(themeCookieValues))

		return
	}

	for index, variant := range audit.themeVariants {
		if bytes.Equal(variant, audit.document) {
			continue
		}

		t.Errorf("%s: the document rendered with sp_ui=%q differs from the one "+
			"rendered with sp_ui=%q (%d bytes against %d); the theme cookie must not "+
			"reach the server's answer, because §3.7 has already decided the theme "+
			"client-side and a server that decides it again shows the reader one "+
			"theme before the first paint and another after (UI §6.6, S-13.5)",
			audit.where,
			describeThemeValue(themeCookieValues[index]),
			describeThemeValue(themeCookieValues[0]),
			len(variant), len(audit.document))
	}
}

// varyFieldNaming returns the field name in a `Vary` value that matches want, or
// the empty string when there is none.
//
// A `Vary` is a comma-separated list of field names, and `net/http` lets one
// response carry several `Vary` headers — which is why `renderedRoute.vary` joins
// `Header.Values` rather than reading one. Matching is case-insensitive because
// field names are, and because `sp_ui` and `SP_UI` are the same field to a cache:
// a bug in either case is the same bug.
func varyFieldNaming(vary, want string) string {
	for field := range strings.SplitSeq(vary, ",") {
		if strings.EqualFold(strings.TrimSpace(field), want) {
			return field
		}
	}

	return ""
}

// describeThemeValue names a cookie value for a failure message.
//
// The empty value is the **absent** cookie and is called that, because
// "rendered with sp_ui=\"\"" reads as a bug in the message when it is the
// fixture's first case.
func describeThemeValue(value string) string {
	if value == "" {
		return "(absent)"
	}

	return value
}

// --- The audit's own tests -------------------------------------------------

// TestTheVocabularyAuditFindsTheWordWhereverItIs is the audit's own test.
//
// §10.2's vocabulary rule is the easiest one in the list to pass vacuously: a
// substring assertion over a rendered document finds nothing in a correct shell
// and also finds nothing in a shell with `<!-- the world of this account -->` in
// it, because a comment is not a text node and the word is in a comment. The same
// is true of an `aria-label`, a `data-` attribute and an attribute *name*.
//
// So the audit is fed six ways of smuggling a retired entity in, and each has to
// be objected to. This is the same discipline `internal/httpapi/shell`'s
// `TestTheAuditFindsAResolvedAttributeWhereverItIs` applies to the resolver
// audit, and for the same reason: an audit that cannot fail is worse than no
// audit, because it is a green light wired to nothing.
//
// Every case is a *minimal* document — one element and the word — so a failure
// names the smuggling method rather than a shell that happens to be large.
func TestTheVocabularyAuditFindsTheWordWhereverItIs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"a text node", `<html><body><p>the World of this account</p></body></html>`},
		{"an aria-label", `<html><body><nav aria-label="game sessions"></nav></body></html>`},
		{"a title attribute", `<html><body><span title="Your session"></span></body></html>`},
		{"a data attribute value", `<html><body><div data-note="world map"></div></body></html>`},
		{
			"an HTML comment",
			`<html><body><!-- the world of this account --><p>Fine.</p></body></html>`,
		},
		{
			// The nastiest one, because it is the one a substring assertion over
			// attributes would find and a rendered-document reader would not: the
			// word is in the *name* of an attribute nothing renders.
			"an attribute name",
			`<html><body><div data-world="true"></div></body></html>`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// A silent audit: it must report at least one violation, and it must
			// be a *t.Errorf* call rather than a return value, because that is how
			// the real audit is written and a test of a different shape would not
			// be testing the thing.
			counter := &countingT{T: t}

			assertVocabulary(counter, &audit{
				t:     counter,
				where: "fixture",
				root:  mustParse(t, testCase.body),
				body:  findElement(mustParse(t, testCase.body), "body"),
			})

			if counter.failures == 0 {
				t.Errorf("the vocabulary audit reported nothing for %s; a substring "+
					"assertion over a document finds no word in an HTML comment, an "+
					"attribute name or an aria-label, which is how this gate comes to "+
					"pass while the word sits in the markup (§1.2, §10.2)", testCase.name)
			}
		})
	}
}

// TestTheVocabularyAuditPassesOnTheRealShell is the other half: the audit must
// also be capable of reporting nothing.
//
// Six positive controls and one negative, because a gate that always fails is
// just as useless as one that never does — and an audit tuned until it fires on
// everything gets switched off within a phase.
func TestTheVocabularyAuditPassesOnTheRealShell(t *testing.T) {
	t.Parallel()

	for _, route := range auditedRoutes(t) {
		t.Run(route.where, func(t *testing.T) {
			t.Parallel()

			counter := &countingT{T: t}

			parsed := newAudit(t, route)
			parsed.t = counter

			assertVocabulary(counter, parsed)

			if counter.failures != 0 {
				t.Errorf("the vocabulary audit reported %d failures on the real "+
					"shell, which renders nothing forbidden; the audit is too eager "+
					"and will be switched off rather than fixed",
					counter.failures)
			}
		})
	}
}

// mustParse parses a fixture document, aborting on failure.
//
// Separate from `newAudit` because these fixtures are *not* responses: they are
// minimal documents written to trip one rule, and they carry no route, no status
// and no headers.
func mustParse(t *testing.T, body string) *html.Node {
	t.Helper()

	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	return root
}

// countingT records Errorf calls instead of reporting them.
//
// A `testing.T` whose `Errorf` is intercepted, so a sub-audit can be *tested*
// rather than merely run. `FailNow` is deliberately not part of it: `Fatalf`
// aborting the test is correct behaviour for the audit's own constructor and is
// left alone.
//
// The *messages* are kept as well as the count, because a rule that fires for the
// wrong reason is still a rule that appears to work: a `<h1>` audit that also
// reported duplicate hooks would pass a fixture built to trip the heading count,
// and the gate would look covered while the thing it names was not being checked.
type countingT struct {
	*testing.T

	failures int
	messages []string
}

// Errorf records the failure and carries on, so one document produces a count and
// a list rather than stopping at the first violation.
func (c *countingT) Errorf(format string, args ...any) {
	c.failures++
	c.messages = append(c.messages, fmt.Sprintf(format, args...))

	c.Logf(format, args...)
}

// mentions reports whether any recorded message contains a fragment.
func (c *countingT) mentions(fragment string) bool {
	for _, message := range c.messages {
		if strings.Contains(message, fragment) {
			return true
		}
	}

	return false
}

// Helper satisfies the audit's `t.Helper()` calls without recording anything.
func (c *countingT) Helper() {}

// TestTheStructuralAuditFindsEachViolationItClaimsTo is the same discipline over
// the rules that are not the vocabulary rule.
//
// Four violations, one per rule, each injected into an otherwise-valid shell
// document. A rule that cannot fail is a rule that has stopped being a gate, and
// these four are the ones with a real temptation to stop: the `<h1>` count (a
// second heading added by a route is easy), the tabindex (a plugin's first
// control), the skip-link resolution (a landmark removed per §4.6 without its
// link) and the `.target` class (§10.6's grep, which is the one most likely to be
// satisfied by a shell that happens to be tidy today).
func TestTheStructuralAuditFindsEachViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	// A minimal document with exactly one h1 and a resolvable skip link, so each
	// case perturbs one thing and the others stay valid.
	const base = `<html><head><title>t</title></head><body>` +
		`<a class="skip-link target" href="#main">Skip to content</a>` +
		`<main id="main" class="shell-main target" tabindex="-1"><h1>Page</h1>` +
		`<a class="target" href="/x">Link</a></main></body></html>`

	cases := []struct {
		name    string
		body    string
		check   func(failer, *audit)
		wantOne string
	}{
		{
			name:  "a second h1",
			body:  strings.Replace(base, "<h1>Page</h1>", "<h1>Page</h1><h1>Also page</h1>", 1),
			check: assertExactlyOneH1, wantOne: "<h1>",
		},
		{
			name: "a positive tabindex",
			body: strings.Replace(
				base,
				`<a class="target" href="/x">`,
				`<a tabindex="3" class="target" href="/x">`,
				1,
			),
			check:   assertNoPositiveTabindex,
			wantOne: "tabindex",
		},
		{
			name: "a focusable element with no target class",
			body: strings.Replace(
				base,
				`<a class="target" href="/x">`,
				`<a class="link" href="/x">`,
				1,
			),
			check:   assertEveryFocusableCarriesTarget,
			wantOne: ".target",
		},
		{
			name:  "a skip link to an absent landmark",
			body:  strings.Replace(base, `href="#main"`, `href="#nowhere"`, 1),
			check: assertSkipLinks, wantOne: "not in the document",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			counter := &countingT{T: t}
			root := mustParse(t, testCase.body)

			testCase.check(counter, &audit{
				t:      counter,
				where:  "fixture",
				status: 200,
				root:   root,
				body:   findElement(root, "body"),
				head:   findElement(root, "head"),
			})

			if counter.failures == 0 {
				t.Errorf("the audit reported nothing for %s; the rule is not "+
					"holding anything (§10.2)", testCase.name)
			}

			// And it fired for the *right* reason. A count alone cannot tell which
			// rule spoke, and an audit that fires for an unrelated reason looks
			// exactly like one that works.
			if !counter.mentions(testCase.wantOne) {
				t.Errorf("the audit reported %d failures for %s but none of them "+
					"mentions %q, so the rule that fired was not the rule under "+
					"test; messages:\n  %s",
					counter.failures, testCase.name, testCase.wantOne,
					strings.Join(counter.messages, "\n  "))
			}
		})
	}
}
