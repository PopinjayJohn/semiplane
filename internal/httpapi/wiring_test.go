package httpapi_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi"
	"github.com/semiplane/semiplane/internal/httpapi/accounts"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/observability"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

// wiringStore is a fake satisfying httpapi.Store — the union the router takes.
//
// It exists because of a bug this file is here to prevent: the router mounted the
// account routes and the campaign gates but never put `identity.Authenticate` in
// the chain, so every request resolved as anonymous. Each package's own tests
// passed, because each mounted the middleware itself and therefore never noticed
// that the composition root had not. A wiring test is the only kind that sees it,
// and it exists here rather than as an assertion on the router's internals
// because the failure was observable only through a real request.
type wiringStore struct {
	users     map[string]domain.User
	sessions  map[string]store.AuthSession
	campaigns map[string]domain.Campaign
	members   map[int64]domain.Role

	// hits are the rows `SearchPages` answers with, keyed by the query term that
	// finds them. There, rather than one fixed result, because the audit needs
	// *three* search documents from one fixture — a result list, the idle state and
	// a campaign the reader cannot see — and a store that answered the same list to
	// every query could not produce two of them.
	//
	// The real `*store.Store` satisfies `search.Pages` too; that is asserted in
	// `cmd/server` rather than here, because this fake exists so the audit can run
	// without claiming the process's single-instance slot.
	hits map[string][]domain.SearchHit

	// searched records every `PageSearch` the route asked for, so a test can assert
	// the campaign scope rather than only the rows. `search.Pages.CampaignID` of 0
	// means *every campaign the requestor may read*, and a route that passed it
	// would leak private page titles rather than fail, so the field is the one worth
	// pinning.
	searched []store.PageSearch
}

func newWiringStore() *wiringStore {
	return &wiringStore{
		users:     map[string]domain.User{},
		sessions:  map[string]store.AuthSession{},
		campaigns: map[string]domain.Campaign{},
		members:   map[int64]domain.Role{},
		hits:      map[string][]domain.SearchHit{},
	}
}

// SearchPages answers with the rows registered for the query's only term.
//
// A term rather than a whole-query match, because the audit's queries are one word
// each and a matcher that understood FTS5's syntax would be a second implementation
// of `store.buildMatchQuery` in a test file. The scope is recorded and the campaign
// filter is **not** re-applied here: `store.SearchPages` joins campaign visibility
// itself, and a fake that filtered again would make the audit blind to the route
// passing a scope of 0 — the one mistake that turns a result list into a leak.
func (f *wiringStore) SearchPages(
	_ context.Context,
	search store.PageSearch,
	_ domain.Requestor,
) ([]domain.SearchHit, error) {
	f.searched = append(f.searched, search)

	for term, rows := range f.hits {
		if strings.Contains(search.Query, term) {
			return rows, nil
		}
	}

	return nil, nil
}

func (f *wiringStore) UserByUsername(_ context.Context, username string) (domain.User, error) {
	user, ok := f.users[username]
	if !ok {
		return domain.User{}, store.ErrNotFound
	}

	return user, nil
}

func (f *wiringStore) CreateUser(ctx context.Context, user domain.User) (domain.User, error) {
	f.users[user.Username] = user

	return user, nil
}

func (f *wiringStore) CreateSession(
	_ context.Context,
	session store.AuthSession,
) (store.AuthSession, error) {
	f.sessions[session.TokenHash] = session

	return session, nil
}

func (f *wiringStore) DeleteSession(_ context.Context, tokenHash string) error {
	delete(f.sessions, tokenHash)

	return nil
}

func (f *wiringStore) SessionByTokenHash(
	_ context.Context,
	tokenHash string,
) (store.AuthSession, error) {
	session, ok := f.sessions[tokenHash]
	if !ok {
		return store.AuthSession{}, store.ErrNotFound
	}

	return session, nil
}

func (f *wiringStore) UserByID(_ context.Context, id int64) (domain.User, error) {
	for _, user := range f.users {
		if user.ID == id {
			return user, nil
		}
	}

	return domain.User{}, store.ErrNotFound
}

func (f *wiringStore) CampaignsForUser(_ context.Context, _ int64) ([]domain.Campaign, error) {
	campaigns := make([]domain.Campaign, 0, len(f.members))

	for campaignID := range f.members {
		// Indexed rather than ranged: domain.Campaign is 128 bytes and only the
		// ID is read while matching.
		for i := range f.campaigns {
			if f.campaigns[i].ID == campaignID {
				campaigns = append(campaigns, f.campaigns[i])

				break
			}
		}
	}

	return campaigns, nil
}

func (f *wiringStore) MembershipsForUser(
	_ context.Context,
	userID int64,
) ([]domain.Membership, error) {
	memberships := make([]domain.Membership, 0, len(f.members))

	for campaignID, role := range f.members {
		memberships = append(memberships, domain.Membership{
			CampaignID: campaignID, UserID: userID, Role: role,
		})
	}

	return memberships, nil
}

func (f *wiringStore) CampaignBySlug(_ context.Context, slug string) (domain.Campaign, error) {
	campaign, ok := f.campaigns[slug]
	if !ok {
		return domain.Campaign{}, store.ErrNotFound
	}

	return campaign, nil
}

func (f *wiringStore) Membership(
	_ context.Context,
	campaignID, userID int64,
) (domain.Membership, error) {
	role, ok := f.members[campaignID]
	if !ok {
		return domain.Membership{}, store.ErrNotFound
	}

	return domain.Membership{CampaignID: campaignID, UserID: userID, Role: role}, nil
}

func (f *wiringStore) CreateCampaign(
	_ context.Context,
	campaign domain.Campaign,
) (domain.Campaign, error) {
	if _, exists := f.campaigns[campaign.Slug]; exists {
		return domain.Campaign{}, store.ErrConflict
	}

	campaign.ID = int64(len(f.campaigns) + 1)
	f.campaigns[campaign.Slug] = campaign

	return campaign, nil
}

func (f *wiringStore) CreateMembership(
	_ context.Context,
	membership domain.Membership,
) (domain.Membership, error) {
	f.members[membership.CampaignID] = membership.Role

	return membership, nil
}

func (f *wiringStore) DeleteCampaign(context.Context, int64) error { return nil }

// wiringHandlerTimeout is the budget the wiring tests give the handler.
//
// Generous on purpose, and the value is chosen rather than copied. Signing in
// runs PBKDF2-HMAC-SHA256 at 600,000 iterations (ADR 0021), which is roughly half
// a second of CPU by design and several seconds under `-race`. A one-second
// budget — the value the middleware tests use, and the right one for them because
// none of them hash anything — answers 504 before the credential check finishes,
// so the wiring tests got a timeout rather than the behaviour they were asserting.
//
// The real default is 25s (config.HandlerTimeout), which is ample for one
// password verification. Asserted rather than assumed: a test that passes with an
// artificially short budget is not testing the chain a deployment runs.
const wiringHandlerTimeout = 20 * time.Second

// fullRouter builds the router exactly as the composition root does: an account
// Router and one store, with no middleware added by the test.
//
// **No campaign routes.** That is deliberate and it is the reason this helper and
// `campaignRouter` are two functions rather than one with a flag. The tests that
// use this one are about the account surface and about the *gates* —
// `TestUnmatchedCampaignPathIsNotFoundNotAuthorised` asserts that
// `/c/greyhaven/edit/Page` is a 404, which is only the right assertion while no
// editor is mounted, and a helper that quietly grew one would turn that test into
// a test of a different route while still reading as the same test.
//
// The §10.2 audit in `shell_render_test.go` needs the opposite, and gets it from
// `campaignRouter`.
//
// `instance` is passed rather than defaulted to a zero value because a zero
// `components.InstanceView` reports the instance **healthy** — an empty
// `Degraded` slice means healthy by construction. Every audit that runs over the
// routes this builds would therefore skip the degraded notice entirely, which is
// how a vocabulary bug in that notice's copy can sit in the tree while the gate
// reporting zero findings is green.
//
// The default is a *degraded* instance, so a fixture that does not think about it
// is auditing the surface where a mistake is most likely.
func fullRouter(backing httpapi.Store) http.Handler {
	return fullRouterWithInstance(backing, degradedInstanceView())
}

// degradedInstanceView is an instance with one campaign that cannot be read.
//
// Matches the shape `cmd/server.instanceView` produces for a campaign whose
// content root vanished (S-4.5), so the audit sees the same markup a real
// degraded instance renders.
func degradedInstanceView() components.InstanceView {
	return components.InstanceView{
		Degraded: []components.DegradedView{{
			Name:   "Campaign greyhaven",
			Detail: "its content root is not readable, so its pages cannot load",
		}},
	}
}

// fullRouterWithInstance builds the router with an explicit instance view.
//
// Every campaign-scoped handler is nil, and **the plugin route is one of them on
// purpose**: it renders a widget over a live hub, which this fixture has none of, and a
// 503 is the honest answer rather than a page to assert on. Its own audits and its mount
// are asserted where a hub exists — `internal/httpapi/plugins` for the audits, and
// `cmd/server`'s wiring tests for the mount.
func fullRouterWithInstance(backing httpapi.Store, instance components.InstanceView) http.Handler {
	return httpapi.NewRouter(
		slog.New(slog.DiscardHandler),
		config.Config{HandlerTimeout: wiringHandlerTimeout},
		observability.NewRegistry(),
		&accounts.Router{Store: backing, Instance: instance},
		backing,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
}

// TestSignInThroughTheRouterProducesAWorkingSession is the end-to-end wiring
// assertion: post credentials, follow the redirect with the cookie the router
// set, and land on the campaign list showing the account's campaigns.
//
// Every step goes through the router the binary uses. A test that built its own
// middleware chain would pass with `Authenticate` missing from the composition
// root, which is exactly the failure this test was written for.
func TestSignInThroughTheRouterProducesAWorkingSession(t *testing.T) {
	t.Parallel()

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

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signIn(t, "ada", "correct horse"))

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("sign-in status = %d, want 303", recorder.Code)
	}

	session := sessionCookieOf(t, recorder)

	// The campaign list, carrying the cookie the router itself set.
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, getRequest(t, "/", session))

	if list.Code != http.StatusOK {
		t.Fatalf("campaign list status = %d, want 200", list.Code)
	}

	body := list.Body.String()

	// The identity reached the handler. Before the fix, the router resolved every
	// request as anonymous and rendered the sign-in form here instead.
	if !strings.Contains(body, `data-testid="campaign-list"`) {
		t.Errorf("the campaign list did not render; body began %q", firstBytes(body))
	}

	for _, want := range []string{`data-slug="greyhaven"`, "ada", "Game Master"} {
		if !strings.Contains(body, want) {
			t.Errorf("the campaign list does not contain %q; body began %q", want, firstBytes(body))
		}
	}
}

// TestSignOutThroughTheRouterRevokesTheSession: the same wiring assertion for
// the other direction, and it fails if the cookie is cleared without the row.
func TestSignOutThroughTheRouterRevokesTheSession(t *testing.T) {
	t.Parallel()

	st := newWiringStore()

	hash, err := auth.HashPassword("pw")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	st.users["ada"] = domain.User{ID: 1, Username: "ada", PasswordHash: hash}

	handler := fullRouter(st)

	signInRecorder := httptest.NewRecorder()
	handler.ServeHTTP(signInRecorder, signIn(t, "ada", "pw"))

	session := sessionCookieOf(t, signInRecorder)

	if len(st.sessions) != 1 {
		t.Fatalf("%d sessions stored, want 1", len(st.sessions))
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, formPost(t, "/logout", url.Values{}, session))

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("sign-out status = %d, want 303", recorder.Code)
	}

	if len(st.sessions) != 0 {
		t.Errorf("%d sessions remain after sign-out, want 0", len(st.sessions))
	}
}

// TestCampaignGatesSeeTheIdentityThroughTheRouter is the other half of the
// missing-middleware bug: Resolve reads the requestor to load a membership, so
// without Authenticate in the chain every campaign route answered as an
// anonymous visitor regardless of the cookie.
//
// There is no campaign route mounted in this phase, so the assertion is made
// through the identity the gates consume: a request carrying a valid session
// must not be anonymous. Without the cookie, it must be.
func TestCampaignGatesSeeTheIdentityThroughTheRouter(t *testing.T) {
	t.Parallel()

	st := newWiringStore()

	hash, err := auth.HashPassword("pw")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	st.users["ada"] = domain.User{ID: 1, Username: "ada", PasswordHash: hash}

	handler := fullRouter(st)

	// A request with no cookie reaches the campaign-list route as anonymous,
	// which is the observable form of the identity.
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, getRequest(t, "/", nil))

	if strings.Contains(anonymous.Body.String(), `data-testid="header-account"`) {
		t.Error("a request with no cookie rendered an account zone: identity is not being resolved")
	}

	// With a valid session, the same route must render the identity. This is the
	// assertion that fails when Authenticate is missing from the chain.
	signInRecorder := httptest.NewRecorder()
	handler.ServeHTTP(signInRecorder, signIn(t, "ada", "pw"))

	session := sessionCookieOf(t, signInRecorder)

	signedIn := httptest.NewRecorder()
	handler.ServeHTTP(signedIn, getRequest(t, "/", session))

	if !strings.Contains(signedIn.Body.String(), `data-testid="header-account"`) {
		t.Errorf("a request with a valid session did not render an account zone; "+
			"body began %q. identity.Authenticate is not in the chain", firstBytes(signedIn.Body.String()))
	}
}

// TestRouterWithoutAStoreStillServesLiveness is the independently-runnable
// invariant from architecture §15, and it holds for a router with no database at
// all — which is what makes the wiring above a wiring choice rather than a
// dependency.
func TestRouterWithoutAStoreStillServesLiveness(t *testing.T) {
	t.Parallel()

	handler := httpapi.NewRouter(
		slog.New(slog.DiscardHandler),
		config.Config{HandlerTimeout: time.Second},
		observability.NewRegistry(),
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	for path, want := range map[string]int{
		"/healthz": http.StatusOK,
		"/readyz":  http.StatusOK,
		"/nope":    http.StatusNotFound,
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, getRequest(t, path, nil))

		if recorder.Code != want {
			t.Errorf("GET %s = %d, want %d", path, recorder.Code, want)
		}
	}
}

// TestUnmatchedCampaignPathIsNotFoundNotAuthorised: the campaign subtree is
// mounted behind the access gates, and a path inside it that matches no route
// answers 404 with the not-found body — the same answer for a slug nobody
// registered.
func TestUnmatchedCampaignPathIsNotFoundNotAuthorised(t *testing.T) {
	t.Parallel()

	st := newWiringStore()
	handler := fullRouter(st)

	paths := []string{
		"/c/greyhaven/wiki/Page",
		"/c/greyhaven/edit/Page",
		"/c/greyhaven/secrets/Page",
		"/c/no-such-campaign/wiki/Page",
		"/c/greyhaven/",
	}

	for _, path := range paths {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, getRequest(t, path, nil))

		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, recorder.Code)
		}
	}
}

// TestCampaignSubtreeRedirectIsIdenticalForEverySlug: `/c/{slug}` canonicalises
// to `/c/{slug}/`, and `net/http` adds that redirect for any pattern ending in
// a slash.
//
// The redirect happens in the mux, *before* the access gate runs, and its
// Location is derived from the request path alone. That is the property worth
// pinning: an existing campaign and a campaign nobody registered must produce
// the same redirect, or the redirect becomes an existence oracle for every
// private campaign on the instance — the one leak a 404-shaped answer was
// specifically arranged to prevent (ADR 0024).
func TestCampaignSubtreeRedirectIsIdenticalForEverySlug(t *testing.T) {
	t.Parallel()

	handler := fullRouter(newWiringStore())

	seen := map[string]int{}

	for _, slug := range []string{"greyhaven", "no-such-campaign", "anything-at-all"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, getRequest(t, "/c/"+slug, nil))

		if recorder.Code != http.StatusTemporaryRedirect {
			t.Errorf("GET /c/%s = %d, want 307", slug, recorder.Code)
		}

		seen[slug] = recorder.Code

		// The Location is the canonical form of the path that was asked for, and
		// nothing else. If it ever varied with whether the campaign exists, this
		// is where it would show.
		if want := "/c/" + slug + "/"; recorder.Header().Get("Location") != want {
			t.Errorf("GET /c/%s redirected to %q, want %q",
				slug, recorder.Header().Get("Location"), want)
		}
	}

	if seen["greyhaven"] != seen["no-such-campaign"] {
		t.Error("an existing campaign and an absent one answer the redirect differently")
	}
}

// signIn builds a credential POST.
func signIn(t *testing.T, username, password string) *http.Request {
	t.Helper()

	form := url.Values{"username": {username}, "password": {password}}

	return formPost(t, "/login", form, nil)
}

// formPost builds a form-encoded POST, optionally carrying a cookie.
func formPost(t *testing.T, path string, form url.Values, cookie *http.Cookie) *http.Request {
	t.Helper()

	r := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, path, strings.NewReader(form.Encode()),
	)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if cookie != nil {
		r.AddCookie(cookie)
	}

	return r
}

// getRequest builds a GET, optionally carrying a cookie.
func getRequest(t *testing.T, path string, cookie *http.Cookie) *http.Request {
	t.Helper()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
	if cookie != nil {
		r.AddCookie(cookie)
	}

	return r
}

// sessionCookieOf returns the session cookie a response set.
func sessionCookieOf(t *testing.T, recorder *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()

	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			return cookie
		}
	}

	t.Fatal("no session cookie was set")

	return nil
}

// firstBytes truncates a body for a failure message, because a whole rendered
// page in a test failure is unreadable.
func firstBytes(s string) string {
	const limit = 200
	if len(s) > limit {
		return s[:limit] + "..."
	}

	return s
}
