// Package accounts serves the two surfaces that exist before a campaign does:
// signing in, and the list of campaigns an account is a member of.
//
// Both are thin. The credential work is in `internal/httpapi/auth`, the access
// matrix is in `internal/domain`, and the markup is in
// `internal/web/components`. What is left here is genuinely this package's: what
// a failed sign-in says, where a successful one goes, what an anonymous reader
// is shown instead of a redirect loop, and validating the `next` a caller
// supplied.
package accounts

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

// maxFormBytes bounds a sign-in submission.
//
// 8 KiB is generous for a username and a password and small enough that the
// server never buffers a body of consequence. http.MaxBytesReader rather than
// reading to a limit and erroring afterwards, because the limit has to apply
// while reading.
const maxFormBytes = 8 << 10

// Store is every query and write the two routes need.
//
// One interface rather than three, because a caller that can sign in can also
// list campaigns, and splitting it would let a test pass with a store that can do
// one and not the other. `store.Store` satisfies it structurally.
type Store interface {
	// Accounts.
	UserByUsername(ctx context.Context, username string) (domain.User, error)
	CreateSession(ctx context.Context, session store.AuthSession) (store.AuthSession, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	// Campaign list. MembershipsForUser supplies the reader's role per
	// campaign, which a campaign row cannot carry — the role is the reader's
	// relationship to the tenant, not a property of the tenant.
	CampaignsForUser(ctx context.Context, userID int64) ([]domain.Campaign, error)
	MembershipsForUser(ctx context.Context, userID int64) ([]domain.Membership, error)
}

// Reader is the read half, so a Router can be built with a store that cannot
// write and the read-only routes still work. The composition root passes the one
// store twice; a test that only serves the list passes a narrower fake.
type Reader interface {
	CampaignsForUser(ctx context.Context, userID int64) ([]domain.Campaign, error)
	MembershipsForUser(ctx context.Context, userID int64) ([]domain.Membership, error)
}

// Router holds what the two routes need.
//
// A struct rather than package functions so the store is a field the composition
// root passes once and a test supplies without a global.
type Router struct {
	// Now is the clock, as an exported field so a test can state a session's
	// expiry rather than assert against time.Now. Nil means time.Now.
	//
	// Exported for the same reason httptest's clock is: a package's only
	// legitimate reason to name an unexported field from a test is a test in
	// that package, and these are black-box tests on purpose. Nothing in
	// production sets it, and a caller that does is overriding the clock rather
	// than configuring the server.
	Now func() time.Time

	// store and the fields below are the configuration; see each for its
	// reasoning.
	// Store is the persistence. Required for the sign-in routes; the campaign
	// list uses Reader, and a Router built with only a Reader serves that route
	// and refuses the rest — which is how a read-only test instance is wired
	// without a second constructor.
	Store Store
	// Logger receives the sign-in lines. Nil is allowed and discards them; a
	// test should not have to construct a logger to serve a request.
	Logger *slog.Logger
	// Secure is cfg.IsProduction() and decides the session cookie's Secure
	// attribute. Passed in rather than read from config so this package has no
	// config dependency and a test can exercise both values.
	Secure bool
	// DocsHref is where the first run sends a new operator. Empty omits the
	// link, which is why it is a field: an instance with no configured
	// documentation must not render a link somewhere that answers 404.
	DocsHref string
	// Instance is the identity the header and the rail render. A zero value
	// renders an empty version and — more importantly — reports the instance
	// healthy, because an empty `Degraded` slice means healthy by construction.
	// The wiki route carries the same view, and the two must not disagree: a
	// campaign with an unreadable content root (S-4.5) is degraded on every page
	// or on none.
	Instance components.InstanceView
}

// Mount registers the routes on mux.
//
// `GET /login` and `POST /login` share a pattern, and that is deliberate: a
// failed sign-in re-renders the form with the reason in the body rather than
// redirecting to it, which would put the reason in a query string and in the
// browser history.
func (r *Router) Mount(mux *http.ServeMux) {
	mux.Handle("GET /{$}", http.HandlerFunc(r.campaignList))
	mux.Handle("GET /login", http.HandlerFunc(r.loginForm))
	mux.Handle("POST /login", http.HandlerFunc(r.loginSubmit))
	mux.Handle("POST /logout", http.HandlerFunc(r.logout))
}

// campaignList renders the campaigns the reader is a member of.
//
// An anonymous reader is shown the sign-in form rather than a redirect to it.
// This is the root URL, and an operator following a link to a running instance
// should see what it is — the product name, the way in — before being asked to
// authenticate. Redirecting `/` to `/login` would make every shared link to the
// instance a login prompt, and would make this page unreachable to anyone not
// already signed in.
func (r *Router) campaignList(w http.ResponseWriter, req *http.Request) {
	requestor := identity.Requestor(req.Context())
	shell := r.shell(requestor)

	if !requestor.Authenticated {
		r.render(req.Context(), w, http.StatusOK, components.LoginPage(components.LoginView{
			Shell:    shell,
			ReturnTo: req.URL.Path,
		}))

		return
	}

	view := components.CampaignListView{Shell: shell, DocsHref: r.DocsHref}

	campaigns, err := r.Store.CampaignsForUser(req.Context(), requestor.UserID)
	if err != nil {
		// A list that could not be fetched is not the same as an empty list.
		// Telling a reader they have no campaigns when the database was
		// unreachable sends them looking for a create button that cannot fix
		// it, and the reference ties their screenshot to an access-log line.
		r.warn(req.Context(), "campaign.list_failed", requestor, slog.String("error", err.Error()))

		r.renderLoadFailure(w, req, view)

		return
	}

	memberships, err := r.Store.MembershipsForUser(req.Context(), requestor.UserID)
	if err != nil {
		r.warn(
			req.Context(),
			"membership.list_failed",
			requestor,
			slog.String("error", err.Error()),
		)

		r.renderLoadFailure(w, req, view)

		return
	}

	// Keyed by campaign id, so labelling a card is a map read rather than a scan
	// per campaign. A campaign whose membership is absent from this read renders
	// without a role line rather than with a wrong one.
	roles := make(map[int64]domain.Role, len(memberships))
	for _, membership := range memberships {
		roles[membership.CampaignID] = membership.Role
	}

	// Indexed rather than ranged: domain.Campaign is 128 bytes, and ranging by
	// value copies it once per campaign to read two fields from it.
	for i := range campaigns {
		view.Campaigns = append(
			view.Campaigns,
			components.NewCampaignCard(campaigns[i], roles[campaigns[i].ID]),
		)
	}

	r.render(req.Context(), w, http.StatusOK, components.CampaignListPage(view))
}

// renderLoadFailure answers the campaign list with its error state.
func (r *Router) renderLoadFailure(
	w http.ResponseWriter,
	req *http.Request,
	view components.CampaignListView,
) {
	view.Campaigns = nil
	view.Failure = &components.LoadFailure{Reference: req.Header.Get("X-Request-Id")}

	r.render(req.Context(), w, http.StatusInternalServerError, components.CampaignListPage(view))
}

// loginForm renders an empty sign-in form.
//
// A signed-in reader is redirected to the campaign list rather than shown a form
// to sign in twice — unless the request names a `next`, so a reader who followed
// a link from inside a campaign reaches what they wanted instead of the list.
func (r *Router) loginForm(w http.ResponseWriter, req *http.Request) {
	requestor := identity.Requestor(req.Context())

	if requestor.Authenticated && req.URL.Query().Get("next") == "" {
		http.Redirect(w, req, "/", http.StatusSeeOther)

		return
	}

	r.render(req.Context(), w, http.StatusOK, components.LoginPage(components.LoginView{
		Shell:    r.shell(requestor),
		ReturnTo: safeNext(req.URL.Query().Get("next")),
	}))
}

// loginSubmit authenticates a sign-in attempt.
//
// Every failure produces the same message through the same enum case, and that
// is the whole design: an unknown username and a wrong password must be
// indistinguishable, or the form becomes a probe for which usernames exist. The
// store already returns one sentinel for both (store.ErrNotFound) and this
// handler adds no distinction on top of it.
//
// The password is verified and discarded. It is never echoed, never logged, and
// never carried into the re-rendered view — the login component has no field to
// put it in, which is the right place for that guarantee to live.
func (r *Router) loginSubmit(w http.ResponseWriter, req *http.Request) {
	if r.Store == nil {
		r.render(
			req.Context(),
			w,
			http.StatusNotImplemented,
			components.LoginPage(components.LoginView{
				Shell: r.shell(identity.Anonymous()),
			}),
		)

		return
	}

	req.Body = http.MaxBytesReader(w, req.Body, maxFormBytes)

	if err := req.ParseForm(); err != nil {
		// A body over the limit, or a form that would not parse. Treated as a
		// failed credential rather than a 400: the message is identical either
		// way, so a malformed body learns nothing about the server, and there is
		// nothing in "try again" a reader could act on that "bad request" would
		// have told them.
		r.renderFailure(w, req, "", components.LoginFailureCredentials)

		return
	}

	username := req.PostFormValue("username")
	password := req.PostFormValue("password")

	user, err := r.Store.UserByUsername(req.Context(), username)
	// One branch for an unknown username and a wrong password, and one message
	// for both. A log line saying "no such user" would be the same oracle one
	// level down, in the file an operator reads while investigating a login
	// problem.
	if err != nil || !credentialsMatch(user.PasswordHash, password) {
		r.renderFailure(w, req, username, components.LoginFailureCredentials)

		return
	}

	if err := r.startSession(w, req, user); err != nil {
		r.warn(
			req.Context(),
			"session.create_failed",
			identity.Anonymous(),
			slog.String("error", err.Error()),
		)
		r.renderFailure(w, req, username, components.LoginFailureCredentials)

		return
	}

	// 303 rather than 302: the browser must follow with GET. A 302 after a POST
	// is followed as a GET by current clients and re-submits the POST on others,
	// which would sign somebody in twice.
	//
	//nolint:gosec // G710 is a taint rule and cannot see that postLoginRedirect
	// returns only safeNext's output, which rejects a scheme, a host, a `//`
	// prefix and a backslash. The value's provenance is exactly what G710 is
	// worried about, so the justification has to be here: the sanitiser is
	// safeNext, TestLoginRedirectsToTheCampaignList asserts all four rejections,
	// and the alternative — removing the rule — would not make the code safer.
	http.Redirect(w, req, postLoginRedirect(req), http.StatusSeeOther)
}

// logout revokes the session and clears the cookie.
//
// Revoke first, clear second, and the order is the point. A browser that ignored
// the clear would then be offering a token no longer in `auth_sessions`, which
// fails closed. Clearing first and failing to revoke would leave a live token
// that nothing in the browser holds but that is still in a database backup, and
// still valid.
func (r *Router) logout(w http.ResponseWriter, req *http.Request) {
	if token, ok := auth.SessionToken(req); ok && r.Store != nil {
		if err := r.Store.DeleteSession(req.Context(), auth.HashSessionToken(token)); err != nil {
			// Logged, not fatal. The cookie is cleared either way, and a reader
			// who asked to sign out must end up signed out. A failed DELETE
			// leaves a row that has already expired, or that a later sign-in
			// supersedes, and refusing to clear the cookie would be worse than
			// the residue.
			r.warn(req.Context(), "session.delete_failed", identity.Requestor(req.Context()),
				slog.String("error", err.Error()))
		}
	}

	auth.ClearSessionCookie(w, r.Secure)

	// Always a redirect, never a bare success: a form posted here came from a
	// page, and answering it without a redirect leaves the reader where they
	// signed out, still looking at an identity the cookie no longer carries.
	http.Redirect(w, req, "/", http.StatusSeeOther)
}

// startSession mints a token, stores its hash, and sets the cookie.
//
// The order is the substance: the row is written before the cookie is set, so a
// request arriving with the cookie always finds a session. Setting the cookie
// first leaves a window where the browser holds a token that authenticates
// nobody, and the reader's next request is an unexplained anonymous one.
func (r *Router) startSession(w http.ResponseWriter, req *http.Request, user domain.User) error {
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return fmt.Errorf("mint session token: %w", err)
	}

	if _, err := r.Store.CreateSession(req.Context(), store.AuthSession{
		TokenHash: hash,
		UserID:    user.ID,
		// The cookie's Max-Age and this expiry come from one constant, so the
		// browser and the database cannot disagree about how long a session
		// lasts.
		ExpiresAt: r.clock().Add(auth.SessionCookieMaxAge),
	}); err != nil {
		return fmt.Errorf("store session: %w", err)
	}

	auth.SetSessionCookie(w, token, r.Secure)

	return nil
}

// renderFailure re-renders the sign-in form with a reason.
//
// 401 rather than 200: the response is an authentication failure, and a client
// that is not a browser needs to tell it from a successful sign-in without
// parsing the body. A browser is unaffected — the form renders either way.
func (r *Router) renderFailure(
	w http.ResponseWriter,
	req *http.Request,
	username string,
	failure components.LoginFailure,
) {
	r.render(req.Context(), w, http.StatusUnauthorized, components.LoginPage(components.LoginView{
		Shell:    r.shell(identity.Anonymous()),
		Username: username,
		Failure:  failure,
		ReturnTo: safeNext(req.PostFormValue("next")),
	}))
}

// shell builds the chrome both pages share.
//
// The Account field comes from the resolved identity, which is what makes the
// header show a name and a sign-out form on a signed-in reader's campaign list
// and neither on /login. The sign-out href is empty on /login so the button
// cannot appear on a page whose reader may not be signed in: a control that posts
// somewhere unusable is a focus stop that does nothing.
func (r *Router) shell(requestor domain.Requestor) components.ShellView {
	shell := components.ShellView{Instance: r.Instance}

	if requestor.Authenticated {
		shell.Account = components.AccountView{Username: requestor.Username}
		shell.SignOutHref = "/logout"
	}

	return shell
}

// safeNext validates a post-sign-in destination.
//
// A `next` is caller-supplied, and returning it unvalidated is an open redirect:
// a link shaped /login?next=https://elsewhere.example sends a reader to a
// convincing copy of this product immediately after they typed their password
// into this one. That is credential phishing exactly, and it is why this function
// exists rather than a caller passing the value through.
//
// Only a site-absolute path is accepted. Rejected: anything with a scheme or a
// host; a `//` prefix, which is protocol-relative and resolves to another origin;
// a backslash, which every current browser normalises to a slash, making
// `/\evil.example` a host to the client; and a value the URL parser rejects.
// The empty result means "go to the campaign list", which is the default anyway.
func safeNext(next string) string {
	if next == "" {
		return ""
	}

	if strings.Contains(next, `\`) {
		return ""
	}

	parsed, err := url.Parse(next)
	if err != nil {
		return ""
	}

	// IsAbs catches `https://host`; the Host check is the belt to its braces,
	// because a relative reference can still carry an authority.
	if parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(next, "/") {
		return ""
	}

	return next
}

// postLoginRedirect returns where a successful sign-in goes: the validated
// `next`, or the campaign list.
func postLoginRedirect(req *http.Request) string {
	if next := safeNext(req.PostFormValue("next")); next != "" {
		return next
	}

	return "/"
}

// credentialsMatch reports whether password satisfies the stored hash.
//
// A thin wrapper so the call site reads as a question about credentials rather
// than as a comparison against an error. auth.VerifyPassword returns an error
// for both a wrong password and a stored hash this build cannot parse, and both
// are the same answer here: this attempt did not authenticate. Inverting it
// would put a `!= nil` in the middle of a two-condition `if`, which is where the
// subtle bug lives.
func credentialsMatch(encodedHash, password string) bool {
	return auth.VerifyPassword(encodedHash, password) == nil
}

// clock returns the Router's time source, defaulting to time.Now.
func (r *Router) clock() time.Time {
	if r.Now != nil {
		return r.Now()
	}

	return time.Now()
}

// render writes a templ component as a complete HTML document.
//
// templ.Component, named in the signature rather than as an inline interface, so
// a component that stops satisfying it is a compile error at the call site. The
// render error is logged and nothing else: the status line is already committed,
// so there is no second answer available, and a page that failed halfway has
// already told the reader more than an error page would.
func (r *Router) render(
	ctx context.Context,
	w http.ResponseWriter,
	status int,
	page templ.Component,
) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)

	if err := page.Render(ctx, w); err != nil {
		r.warn(ctx, "page.render_failed", identity.Anonymous(), slog.String("error", err.Error()))
	}
}

// warn logs a line, discarding it when no logger is configured.
//
// The context is threaded rather than replaced with context.Background: the
// request's values are what tie this line to the access-log entry for the same
// request, and a log that cannot be correlated with the request it describes is
// the failure slog exists to prevent.
func (r *Router) warn(
	ctx context.Context,
	event string,
	requestor domain.Requestor,
	attrs ...slog.Attr,
) {
	if r.Logger == nil {
		return
	}

	r.Logger.LogAttrs(ctx, slog.LevelWarn, "accounts",
		append([]slog.Attr{
			slog.String("event", event),
			slog.String("username", requestor.Username),
		}, attrs...)...)
}
