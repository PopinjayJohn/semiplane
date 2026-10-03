package accounts_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/accounts"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

// fakeStore is an in-memory stand-in for the five queries the two routes use.
type fakeStore struct {
	users      map[string]domain.User
	campaigns  map[int64]domain.Campaign
	members    map[int64]domain.Role
	sessions   map[string]store.AuthSession
	campaignNo error
	memberErr  error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:     map[string]domain.User{},
		campaigns: map[int64]domain.Campaign{},
		members:   map[int64]domain.Role{},
		sessions:  map[string]store.AuthSession{},
	}
}

// withUser adds an account with a known password.
//
// The username is a parameter because the enumeration test needs to *miss* one —
// it asks for a name no account holds. unparam sees the passing calls only and
// not the string literal inside the case table, so the suppression sits here.
//
//nolint:unparam // See above: the unknown-username case passes a name no account has.
func (f *fakeStore) withUser(
	t *testing.T,
	username, password string,
) *fakeStore {
	t.Helper()

	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	user := domain.User{ID: 1, Username: username, PasswordHash: hash}
	f.users[username] = user

	return f
}

func (f *fakeStore) SessionByTokenHash(
	_ context.Context,
	tokenHash string,
) (store.AuthSession, error) {
	session, ok := f.sessions[tokenHash]
	if !ok {
		return store.AuthSession{}, store.ErrNotFound
	}

	return session, nil
}

func (f *fakeStore) UserByID(_ context.Context, id int64) (domain.User, error) {
	for _, user := range f.users {
		if user.ID == id {
			return user, nil
		}
	}

	return domain.User{}, store.ErrNotFound
}

func (f *fakeStore) UserByUsername(_ context.Context, username string) (domain.User, error) {
	user, ok := f.users[username]
	if !ok {
		return domain.User{}, store.ErrNotFound
	}

	return user, nil
}

func (f *fakeStore) CreateSession(
	_ context.Context,
	session store.AuthSession,
) (store.AuthSession, error) {
	f.sessions[session.TokenHash] = session

	return session, nil
}

func (f *fakeStore) DeleteSession(_ context.Context, tokenHash string) error {
	delete(f.sessions, tokenHash)

	return nil
}

func (f *fakeStore) CampaignsForUser(_ context.Context, userID int64) ([]domain.Campaign, error) {
	if f.campaignNo != nil {
		return nil, f.campaignNo
	}

	// Membership-scoped, which is what the real query does: a public campaign
	// the reader is not a member of is reachable by its slug and does not belong
	// in their list. Keyed the same way as `members`, so a test that registers a
	// campaign without a membership gets an empty list — the first-run state.
	campaigns := make([]domain.Campaign, 0, len(f.members))

	for campaignID := range f.members {
		campaign, ok := f.campaigns[campaignID]
		if !ok {
			continue
		}

		campaigns = append(campaigns, campaign)
	}

	// The user id is unused by the fake's membership keying, which is deliberate:
	// a second account arrives with the second-user test. Asserted so the
	// parameter cannot drift into looking load-bearing.
	if userID <= 0 {
		return nil, nil
	}

	return campaigns, nil
}

func (f *fakeStore) MembershipsForUser(
	_ context.Context,
	userID int64,
) ([]domain.Membership, error) {
	if f.memberErr != nil {
		return nil, f.memberErr
	}

	memberships := make([]domain.Membership, 0, len(f.members))

	for campaignID, role := range f.members {
		memberships = append(memberships, domain.Membership{
			CampaignID: campaignID,
			UserID:     userID,
			Role:       role,
		})
	}

	return memberships, nil
}

// signedInCookie returns a session cookie for a user, having written the
// session through the store so the cookie resolves to a real row.
//
// A second account arrives with the second-user test, and widening the
// signature then is cheaper than a suppression nobody removes.
func signedInCookie(t *testing.T, st *fakeStore, userID int64) *http.Cookie {
	t.Helper()

	token, hash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	if _, err := st.CreateSession(t.Context(), store.AuthSession{
		TokenHash: hash, UserID: userID, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	return &http.Cookie{Name: auth.SessionCookieName, Value: token}
}

// newRouter builds a router over a fake store, with the identity middleware in
// front of it the way the composition root wires it.
func newRouter(st *fakeStore) http.Handler {
	router := &accounts.Router{
		Store: st,
		Now:   func() time.Time { return time.Unix(1_700_000_000, 0) },
	}

	mux := http.NewServeMux()
	router.Mount(mux)

	return identity.Authenticate(st)(mux)
}

// request issues a request through the full chain.
func request(
	t *testing.T,
	st *fakeStore,
	method, target string,
	form url.Values,
	cookies ...*http.Cookie,
) *httptest.ResponseRecorder {
	t.Helper()

	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}

	var req *http.Request

	if method == http.MethodPost {
		req = httptest.NewRequestWithContext(t.Context(), method, target, body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody)
	}

	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	recorder := httptest.NewRecorder()
	newRouter(st).ServeHTTP(recorder, req)

	return recorder
}

// TestLoginSucceedsAndSetsACookie is the happy path: a correct password yields a
// session row, a cookie, and a redirect to the campaign list.
func TestLoginSucceedsAndSetsACookie(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "correct horse")

	recorder := request(t, st, http.MethodPost, "/login", url.Values{
		"username": {"ada"}, "password": {"correct horse"},
	})

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", recorder.Code)
	}

	if got := recorder.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want %q", got, "/")
	}

	cookies := recorder.Result().Cookies()

	var session *http.Cookie

	for _, cookie := range cookies {
		if cookie.Name == auth.SessionCookieName {
			session = cookie
		}
	}

	if session == nil {
		t.Fatal("no session cookie was set")
	}

	// The row is written before the cookie, so a cookie that exists must resolve
	// to a session. Asserted against the store rather than trusted.
	if _, ok := st.sessions[auth.HashSessionToken(session.Value)]; !ok {
		t.Error(
			"the cookie's hash is not in auth_sessions: the row was not written before the cookie",
		)
	}

	if session.HttpOnly != true {
		t.Error("the session cookie is not HttpOnly")
	}
}

// TestLoginFailureIsIndistinguishable is the enumeration defence at the route
// level: a wrong password, an unknown username, and an empty submission produce
// the same status and the same body.
func TestLoginFailureIsIndistinguishable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		form url.Values
	}{
		{"wrong password", url.Values{"username": {"ada"}, "password": {"wrong"}}},
		{"unknown user", url.Values{"username": {"nobody"}, "password": {"correct horse"}}},
		{"empty submission", url.Values{}},
		{"no password field", url.Values{"username": {"ada"}}},
	}

	// One body per case, written by index. Each subtest owns its own slot, and
	// an `append` to a shared slice from parallel subtests is a data race — which
	// is what the first version of this test did, and why it failed under
	// `-count=2` and passed under `-count=1`.
	bodies := make([]string, len(cases))

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A store per subtest for the same reason: a successful sign-in
			// writes the sessions map, and four of these run concurrently.
			st := newFakeStore().withUser(t, "ada", "correct horse")

			recorder := request(t, st, http.MethodPost, "/login", tc.form)

			if recorder.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", recorder.Code)
			}

			if len(recorder.Result().Cookies()) != 0 {
				t.Error("a failed sign-in set a cookie")
			}

			bodies[i] = recorder.Body.String()
		})
	}

	// The bodies must agree, with the username being the one field allowed to
	// differ — so compare after blanking the echoed username out.
	for i := 1; i < len(bodies); i++ {
		if redact(bodies[i]) != redact(bodies[0]) {
			t.Errorf("failure %d differs from failure 0:\n got:  %q\n want: %q",
				i, redact(bodies[i]), redact(bodies[0]))
		}
	}
}

// redact blanks a username out of a rendered form so two failure bodies can be
// compared. The username is the one field a failed sign-in echoes back.
func redact(body string) string {
	for _, username := range []string{"ada", "nobody"} {
		body = strings.ReplaceAll(body, `value="`+username+`"`, `value="X"`)
	}

	return body
}

// TestLoginEchoesTheUsernameAndNeverThePassword: a reader should not retype
// their name, and a password in a response is a password in the browser cache
// and in every proxy that saw it.
func TestLoginEchoesTheUsernameAndNeverThePassword(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "correct horse")

	recorder := request(t, st, http.MethodPost, "/login", url.Values{
		"username": {"ada"}, "password": {"hunter2"},
	})

	body := recorder.Body.String()

	if !strings.Contains(body, `value="ada"`) {
		t.Error("the username was not echoed back into the form")
	}

	if strings.Contains(body, "hunter2") {
		t.Error("the submitted password appears in the response body")
	}
}

// TestLoginRefusesAnOversizedBody: the limit applies while reading, and a body
// over it is a failed credential rather than an unbounded read.
func TestLoginRefusesAnOversizedBody(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "correct horse")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/login",
		strings.NewReader(strings.Repeat("x", 32<<10)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	recorder := httptest.NewRecorder()
	newRouter(st).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for an oversized body", recorder.Code)
	}
}

// TestLoginRedirectsToTheCampaignList: the destination is validated.
func TestLoginRedirectsToTheCampaignList(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		next string
		want string
	}{
		{"no next", "", "/"},
		{"site-relative path", "/c/greyhaven/wiki/Page", "/c/greyhaven/wiki/Page"},
		{"absolute url", "https://evil.example/steal", "/"},
		{"protocol-relative", "//evil.example/steal", "/"},
		{"backslash host", `/\evil.example/steal`, "/"},
		{"javascript scheme", "javascript:alert(1)", "/"},
		{"path with a newline", "/c/greyhaven\n/../admin", "/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Per subtest: every case here signs in successfully, so each one
			// writes the sessions map, and they run concurrently.
			st := newFakeStore().withUser(t, "ada", "hunter2")

			recorder := request(t, st, http.MethodPost, "/login", url.Values{
				"username": {"ada"}, "password": {"hunter2"}, "next": {tc.next},
			})

			if got := recorder.Header().Get("Location"); got != tc.want {
				t.Errorf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLoginFormRedirectsASignedInReader: a reader who is already signed in is
// sent to the list, not shown a form to sign in twice.
func TestLoginFormRedirectsASignedInReader(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "hunter2")
	cookie := signedInCookie(t, st, 1)

	recorder := request(t, st, http.MethodGet, "/login", nil, cookie)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", recorder.Code)
	}

	if got := recorder.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want %q", got, "/")
	}
}

// TestLoginFormHonoursNextForASignedInReader: a deep link from inside a campaign
// is not thrown away by the sign-in form.
func TestLoginFormHonoursNextForASignedInReader(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "hunter2")
	cookie := signedInCookie(t, st, 1)

	recorder := request(t, st, http.MethodGet, "/login?next=/c/greyhaven/wiki/Page", nil, cookie)

	if recorder.Code != http.StatusOK {
		t.Fatalf(
			"status = %d, want 200: a signed-in reader with a next is shown the form",
			recorder.Code,
		)
	}

	if !strings.Contains(recorder.Body.String(), "/c/greyhaven/wiki/Page") {
		t.Error("the next destination was not carried into the form")
	}
}

// TestCampaignListShowsTheFormToAnAnonymousReader: `/` is reachable without a
// session, and shows the way in rather than redirecting. An operator following a
// link to the instance should see what it is before being asked to authenticate.
func TestCampaignListShowsTheFormToAnAnonymousReader(t *testing.T) {
	t.Parallel()

	recorder := request(t, newFakeStore(), http.MethodGet, "/", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	if !strings.Contains(recorder.Body.String(), `data-testid="login-form"`) {
		t.Error("an anonymous reader was not shown the sign-in form")
	}
}

// TestCampaignListRendersMemberships is the signed-in case: the reader's
// campaigns, each labelled with their role and visibility.
func TestCampaignListRendersMemberships(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "hunter2")
	st.campaigns[1] = domain.Campaign{
		ID: 1, Slug: "greyhaven", Name: "Greyhaven", Visibility: domain.VisibilityPrivate,
	}
	st.campaigns[2] = domain.Campaign{
		ID: 2, Slug: "public-post", Name: "Public Post", Visibility: domain.VisibilityPublic,
	}
	st.members[1] = domain.RoleGM
	st.members[2] = domain.RolePlayer

	cookie := signedInCookie(t, st, 1)
	recorder := request(t, st, http.MethodGet, "/", nil, cookie)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	body := recorder.Body.String()

	for _, want := range []string{`data-slug="greyhaven"`, `data-slug="public-post"`, "Game Master", "Player", "Public", "Private", "ada"} {
		if !strings.Contains(body, want) {
			t.Errorf("the campaign list does not contain %q", want)
		}
	}
}

// TestCampaignListShowsFirstRunWhenThereAreNoCampaigns: an empty list is a
// designed surface (UI §4.7), not an error, and it says so.
func TestCampaignListShowsFirstRunWhenThereAreNoCampaigns(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "hunter2")
	cookie := signedInCookie(t, st, 1)

	recorder := request(t, st, http.MethodGet, "/", nil, cookie)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	if !strings.Contains(recorder.Body.String(), `data-testid="first-run"`) {
		t.Error("an empty campaign list did not render the first-run state")
	}
}

// TestCampaignListReportsALoadFailureSeparately: a list that could not be
// fetched is not the same as an empty one, and the first-run state would send a
// reader looking for a create button that cannot fix it.
func TestCampaignListReportsALoadFailureSeparately(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "hunter2")
	st.campaignNo = errors.New("disk I/O error")
	cookie := signedInCookie(t, st, 1)

	recorder := request(t, st, http.MethodGet, "/", nil, cookie)

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", recorder.Code)
	}

	body := recorder.Body.String()

	if !strings.Contains(body, `data-testid="error-state"`) {
		t.Error("a failed load did not render the error state")
	}

	if strings.Contains(body, `data-testid="first-run"`) {
		t.Error(
			"a failed load rendered the first-run state: a reader would look for a create button",
		)
	}
}

// TestCampaignListShowsARequestReferenceOnFailure: the reference is what ties a
// reader's screenshot to a line in the access log.
func TestCampaignListShowsARequestReferenceOnFailure(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "hunter2")
	st.campaignNo = errors.New("disk I/O error")
	cookie := signedInCookie(t, st, 1)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.AddCookie(cookie)
	req.Header.Set("X-Request-Id", "gen-abc123")

	recorder := httptest.NewRecorder()
	newRouter(st).ServeHTTP(recorder, req)

	if !strings.Contains(recorder.Body.String(), "gen-abc123") {
		t.Error("the request id was not rendered into the error state")
	}
}

// TestLogoutRevokesTheSessionAndClearsTheCookie, and asserts the order: the row
// is gone, not merely the cookie. A browser that ignored the clear would then be
// offering a token that authenticates nobody, which fails closed.
func TestLogoutRevokesTheSessionAndClearsTheCookie(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "hunter2")
	cookie := signedInCookie(t, st, 1)

	recorder := request(t, st, http.MethodPost, "/logout", url.Values{}, cookie)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", recorder.Code)
	}

	if len(st.sessions) != 0 {
		t.Errorf("%d sessions remain: the row must be deleted, not only the cookie cleared",
			len(st.sessions))
	}

	var cleared *http.Cookie

	for _, set := range recorder.Result().Cookies() {
		if set.Name == auth.SessionCookieName {
			cleared = set
		}
	}

	if cleared == nil {
		t.Fatal("no session cookie was cleared")
	}

	// Max-Age -1 emits `Max-Age=0`, which browsers treat as "delete now".
	// Zero is a valid age and would re-save the cookie on some clients.
	if cleared.MaxAge >= 0 {
		t.Errorf("cleared cookie MaxAge = %d, want a negative value", cleared.MaxAge)
	}
}

// TestLogoutWithoutACookieStillSucceeds: a double-submitted logout, or a POST
// with no cookie, is the same success. A reader who asked to sign out must end up
// signed out regardless of what they sent.
func TestLogoutWithoutACookieStillSucceeds(t *testing.T) {
	t.Parallel()

	recorder := request(t, newFakeStore(), http.MethodPost, "/logout", url.Values{})

	if recorder.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", recorder.Code)
	}
}

// TestRouterWithoutAStoreRefusesTheSignInRoute: a read-only instance is a real
// deployment shape, and reaching a sign-in route on one is a clear refusal
// rather than a nil dereference.
func TestRouterWithoutAStoreRefusesTheSignInRoute(t *testing.T) {
	t.Parallel()

	router := &accounts.Router{}

	mux := http.NewServeMux()
	router.Mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(),
		http.MethodPost, "/login", strings.NewReader("")))

	if recorder.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 for a router with no store", recorder.Code)
	}
}

// TestSecureAttributeFollowsConfiguration: a hardcoded Secure logs every
// self-hoster on plain HTTP out permanently, and a browser silently refusing the
// cookie is not diagnosable from the server's log.
func TestSecureAttributeFollowsConfiguration(t *testing.T) {
	t.Parallel()

	for _, secure := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "development", true: "production"}[secure],
			func(t *testing.T) {
				t.Parallel()

				st := newFakeStore().withUser(t, "ada", "hunter2")
				router := &accounts.Router{Store: st, Secure: secure}

				mux := http.NewServeMux()
				router.Mount(mux)
				handler := identity.Authenticate(st)(mux)

				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/login",
					strings.NewReader(url.Values{
						"username": {"ada"}, "password": {"hunter2"},
					}.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, req)

				for _, cookie := range recorder.Result().Cookies() {
					if cookie.Name == auth.SessionCookieName && cookie.Secure != secure {
						t.Errorf("cookie Secure = %t, want %t", cookie.Secure, secure)
					}
				}
			},
		)
	}
}

// TestSessionExpiryComesFromTheCookieConstant: the cookie's Max-Age and the
// row's expiry are the same number, so a browser and the database cannot
// disagree about how long a session lasts.
func TestSessionExpiryComesFromTheCookieConstant(t *testing.T) {
	t.Parallel()

	const want = auth.SessionCookieMaxAge

	st := newFakeStore().withUser(t, "ada", "hunter2")
	now := time.Unix(1_700_000_000, 0)
	router := &accounts.Router{Store: st, Now: func() time.Time { return now }}

	mux := http.NewServeMux()
	router.Mount(mux)
	handler := identity.Authenticate(st)(mux)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/login",
		strings.NewReader(url.Values{"username": {"ada"}, "password": {"hunter2"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if len(st.sessions) != 1 {
		t.Fatalf("%d sessions stored, want 1", len(st.sessions))
	}

	for _, session := range st.sessions {
		if got := session.ExpiresAt.Sub(now); got != want {
			t.Errorf("session expiry = %s, want %s", got, want)
		}
	}

	var cookie *http.Cookie

	for _, set := range recorder.Result().Cookies() {
		if set.Name == auth.SessionCookieName {
			cookie = set
		}
	}

	if cookie == nil {
		t.Fatal("no session cookie was set")
	}

	if got := time.Duration(cookie.MaxAge) * time.Second; got != want {
		t.Errorf("cookie MaxAge = %s, want %s: the browser and the database must agree",
			got, want)
	}
}

// TestSessionCookieCarriesNoUsernameOrRole: the cookie's value is the raw token
// and nothing else.
//
// Asserted by decoding the token, not by searching the string for a username. A
// base64url token is 43 characters of [A-Za-z0-9_-], so it contains "gm" or "ada"
// by chance roughly one run in twenty — the first version of this test searched
// the raw value and failed intermittently for that reason, which is a test
// asserting on randomness rather than on the code.
//
// What is actually being checked is that the value carries no *structure*: it
// decodes to exactly the 32 bytes of crypto/rand, with no prefix, no JSON, no
// `user=1` segment. A cookie holding identity would be a value the client could
// read and edit, and `internal/httpapi/auth` documents the token as unguessable
// precisely so nothing in it is meaningful to anybody but the server.
func TestSessionCookieCarriesNoUsernameOrRole(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withUser(t, "ada", "hunter2")

	recorder := request(t, st, http.MethodPost, "/login", url.Values{
		"username": {"ada"}, "password": {"hunter2"},
	})

	var value string

	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			value = cookie.Value
		}
	}

	if value == "" {
		t.Fatal("no session cookie was set")
	}

	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("the cookie value is not a raw-URL base64 token: %v", err)
	}

	// Exactly the minted entropy, and no longer. A value carrying a username or a
	// role would be longer, or would decode to something whose length is not the
	// token's.
	const tokenBytes = 32
	if len(raw) != tokenBytes {
		t.Errorf("the cookie value decodes to %d bytes, want %d: it carries something "+
			"beyond the token", len(raw), tokenBytes)
	}

	// And the token is the only thing that authenticates: the store keyed on its
	// hash, so nothing in the row is derived from a username.
	if _, ok := st.sessions[auth.HashSessionToken(value)]; !ok {
		t.Error("the cookie's hash is not in auth_sessions")
	}
}

// TestComponentContractIsHonoured: the routes render the components P2/I5 built
// against fixtures, which is the integration surface that work item documented.
// This asserts the wiring rather than the markup — the markup is asserted in the
// components package.
func TestComponentContractIsHonoured(t *testing.T) {
	t.Parallel()

	// Both routes are the ones the login and campaign-list components post to
	// and link from, so a rename would silently break the form.
	var login strings.Builder
	if err := components.LoginPage(components.LoginView{}).Render(t.Context(), &login); err != nil {
		t.Fatalf("render login: %v", err)
	}

	if !strings.Contains(login.String(), `action="/login"`) {
		t.Error("the login form does not post to /login")
	}

	cards := components.CampaignListView{
		Campaigns: []components.CampaignCard{{Name: "Greyhaven", Slug: "greyhaven"}},
	}

	var list strings.Builder
	if err := components.CampaignListPage(cards).Render(t.Context(), &list); err != nil {
		t.Fatalf("render campaign list: %v", err)
	}

	if !strings.Contains(list.String(), `href="/c/greyhaven`) {
		t.Error("a campaign card does not link under /c/{slug}")
	}
}
