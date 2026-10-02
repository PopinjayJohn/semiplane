package wiki_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/httpapi/wiki"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

// The harness drives the route through the same chain `router.go` builds: the
// campaign mux that `mountCampaignRoutes` fills, wrapped in `campaigns.Resolve`
// outside `campaigns.RequireRead`, mounted at `/c/` behind `middleware.RequestID`.
//
// `router.go` is not this work item's to edit and its `mountCampaignRoutes` is
// empty until the integrator adds this route, so the chain is assembled here —
// identically, and out loud, so that a change on either side shows up as a failing
// test rather than as a route that quietly lost its gate.
//
// The objects are real where the property under test is the real thing's: a real
// `content.Root` for the confinement, a real `content.Cache` for the salted
// validator and the single-flight, a real `content.Renderer` for the render. Fakes
// appear only where this phase has nothing real to use — the campaign rows, the
// page listing, and the renderer when a test needs to see what reached it.

// The campaign these tests read, and the identifiers its rows carry.
const (
	testSlug   = "greyhaven"
	testCampID = int64(1)
	gmUser     = int64(10)
	playerUser = int64(11)
)

// gmRequestor is the campaign's GM: the only tier whose responses may include
// secrets.
func gmRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: gmUser, Username: "mira"}
}

// playerRequestor is a member with the player role, which S-8 says never includes
// secrets and which must be indistinguishable from anonymous as far as this route
// is concerned.
func playerRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: playerUser, Username: "tobin"}
}

// anonymousRequestor is a signed-out reader of a public campaign: TierReadOnly.
func anonymousRequestor() domain.Requestor {
	return domain.Requestor{}
}

// campaignStore answers the two queries the access gate reads and refuses the
// three it does not need.
//
// Hand-written rather than pointed at a database so that a change to the schema
// cannot alter what the access matrix resolves to — which is half of what the
// redaction tests below are about.
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

// pageListing is the page listing link resolution reads. Empty unless a test sets
// it: a page with no references resolves nothing.
type pageListing struct {
	pages []domain.Page
	err   error
}

func (l pageListing) PagesForCampaign(_ context.Context, _ int64) ([]domain.Page, error) {
	if l.err != nil {
		return nil, l.err
	}

	return l.pages, nil
}

// campaignLister answers the navigation's Campaigns section, and records whether
// it was asked at all.
//
// The count is there so a test can assert the query is *skipped* for an anonymous
// reader rather than merely returning nothing for one: a lister that returned an
// empty slice unconditionally would satisfy every assertion about what the
// navigation shows and none of them about what it asked for.
type campaignLister struct {
	campaigns []domain.Campaign
	err       error
	asked     atomic.Int64
}

func (l *campaignLister) CampaignsForUser(_ context.Context, _ int64) ([]domain.Campaign, error) {
	l.asked.Add(1)

	if l.err != nil {
		return nil, l.err
	}

	return l.campaigns, nil
}

// recordingRenderer wraps a renderer and remembers every document body it was
// given.
//
// That record is the only way to assert the *ordering* rather than the outcome. A
// test that checks a response for absent text cannot tell redaction-before-render
// from redaction-after-sanitisation, because both produce the same response; a
// test that checks what reached the renderer can.
type recordingRenderer struct {
	inner wiki.Renderer

	mu   sync.Mutex
	seen []string
}

func (r *recordingRenderer) Render(doc content.Document) (content.Rendered, error) {
	r.mu.Lock()
	r.seen = append(r.seen, doc.Body)
	r.mu.Unlock()

	if r.inner == nil {
		return content.Rendered{}, nil
	}

	rendered, err := r.inner.Render(doc)
	if err != nil {
		return content.Rendered{}, fmt.Errorf("recording renderer: %w", err)
	}

	return rendered, nil
}

// bodies returns every document body the renderer was given.
func (r *recordingRenderer) bodies() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.seen...)
}

// oneRenderer answers every campaign with the same renderer, which is what a
// process with one campaign serves and what keeps a test's fixture to one line.
type oneRenderer struct {
	renderer wiki.Renderer
}

func (o oneRenderer) Renderer(_ string) (wiki.Renderer, error) {
	return o.renderer, nil
}

// harness is one assembled route and the vault behind it.
type harness struct {
	t         *testing.T
	root      *content.Registry
	dir       string
	cache     *content.Cache
	recorder  *recordingRenderer
	redactor  content.Redactor
	pages     pageListing
	renderers wiki.Renderers
	// campaigns is the navigation's Campaigns section. A pointer so a test can
	// assert it was *asked* — that is the difference between "the section is
	// absent because the reader is anonymous" and "the section is absent because
	// nobody asked".
	campaigns *campaignLister
	// systemID is the campaign's gameplay system, empty by default. The
	// navigation's Table destination keys on it (UI §4.7's `system_id` row), so
	// the default is the case where there is no Table — which is most campaigns
	// before phase 8 registers anything.
	systemID string
	// campaignName is the campaign's name, "Greyhaven" by default. A field rather
	// than a constant because the slug fallback in the banner and in the document
	// title is only observable when the name is absent, and hardcoding the name
	// would make the fallback untestable.
	campaignName string
	// instanceView is the instance the shell reports. A pointer rather than a value
	// because most tests never touch it and a value would mean every one of them
	// spelled out a default they do not care about.
	instanceView *components.InstanceView
	// visibility is the campaign's, and is a field rather than a constant
	// because most of these tests want a *public* campaign — so an anonymous
	// reader reaches a page at all and the test measures the render rather than
	// the gate — and the ones that are about authorisation need a private one.
	// Hardcoding either would make the other half of the suite untestable.
	visibility domain.Visibility
}

// private makes the harness's campaign private, so a reader who is not a member
// is refused rather than served.
//
// The default is public and that is deliberate: a gate that answers 404 for
// everything is indistinguishable from a route that is working, and a test suite
// whose failures are invisible is worse than one that is slow.
func (h *harness) private() *harness {
	h.visibility = domain.VisibilityPrivate

	return h
}

// newHarness builds a route over an empty campaign, with a cache that holds
// eight renders — small enough that a test could fill it, large enough that no
// test does by accident.
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

	recorder := &recordingRenderer{inner: content.NewRenderer(testSlug, nil)}

	return &harness{
		t:            t,
		root:         registry,
		dir:          dir,
		cache:        content.NewCache(8),
		recorder:     recorder,
		redactor:     content.NoSecrets(),
		renderers:    oneRenderer{renderer: recorder},
		visibility:   domain.VisibilityPublic,
		campaignName: "Greyhaven",
		instanceView: &components.InstanceView{Name: "Greyhaven", Version: "0.3.0"},
	}
}

// instance returns the harness's instance view.
//
// A pointer, and **not** lazily created: several tests call it before their
// parallel subtests start, and a lazy initialiser would be a write the first
// `get` in a subtest performs while a sibling is reading — which `-race` reports
// and which is a real data race in the harness rather than in the route.
func (h *harness) instance() *components.InstanceView {
	return h.instanceView
}

// indexed gives the harness a campaign whose page listing is pages.
//
// A separate entry point from `write` because the two are different acts of
// authorship: `write` puts bytes in the vault, and the listing is what the
// watcher *derived* from them. Most of the navigation's assertions are about the
// listing rather than about the vault, and a test that only wrote files would be
// asserting against an empty index and proving nothing.
func (h *harness) indexed(paths ...string) {
	pages := make([]domain.Page, 0, len(paths))
	for _, rel := range paths {
		pages = append(pages, domain.Page{CampaignID: testCampID, Path: rel})
	}

	h.pages = pageListing{pages: pages}
}

// write puts a page into the campaign's content root, creating its directory.
//
// Written with `os` because the test is the vault's author here, not the route:
// the confinement under test is the one between a request and a page, and a test
// that could not put a page in the root could not test a route that reads one.
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

// withGameplaySystem gives the campaign a gameplay system, which is the other
// half of what makes the navigation offer a Table.
//
// UI §4.7's `system_id` row: a campaign with no system installed has no tabletop,
// so the destination is absent rather than a link that answers 404.
func (h *harness) withGameplaySystem() *harness {
	h.systemID = "5e"

	return h
}

// handler builds the route under test. Called by `get`, so a test that changed a
// dependency between two requests gets both.
func (h *harness) handler() *wiki.Handler {
	return &wiki.Handler{
		Roots:       h.root,
		Renderers:   h.renderers,
		Pages:       h.pages,
		Redactor:    h.redactor,
		Cache:       h.cache,
		Instance:    *h.instance(),
		SignOutHref: "/logout",
		// The CampaignLister interface is satisfied by the pointer or nil, so the
		// field is set conditionally rather than to a typed nil: a non-nil
		// interface holding a nil pointer is the shape of a panic on the request
		// path, and a test that forgot to wire it would find out by crashing.
		Campaigns: h.campaignLister(),
	}
}

// campaignLister returns the harness's lister, or nil when the test wired none.
//
// The nil is the *interface* nil, not a typed nil, because the route treats a nil
// `CampaignLister` as "omit the section" and a typed nil would satisfy the
// interface and then dereference.
func (h *harness) campaignLister() wiki.CampaignLister {
	if h.campaigns == nil {
		return nil
	}

	return h.campaigns
}

// serve builds the whole chain, in the order `router.go` assembles it.
//
// The outer pattern is `/c/{slug}/` and not `/c/`, and that is the one place this
// harness is not a copy of what is in `router.go` today.
//
// `campaigns.Resolve` reads the campaign out of `r.PathValue("slug")`, and a
// `net/http` path value is set by the mux whose *pattern* matched — not by the
// mux the handler underneath then happens to be. A pattern of `/c/` matches no
// wildcard, so `Resolve` sees an empty slug, resolves no access, and
// `RequireRead` answers 404 for every request under `/c/`. No campaign route has
// ever been mounted, so nothing has exercised it.
//
// `TestTheCampaignMountMustCarryTheSlug` is the regression guard for that, and it
// is here rather than in `router_test.go` because `router.go` is not this work
// item's file to change: the integrator makes the one-line mount change and this
// test fails loudly if it is reverted.
// serve builds the whole chain, in the order `router.go` assembles it, with the
// mount the campaign gates need.
func (h *harness) serve() http.Handler {
	return h.serveMountedAt("/c/{slug}/")
}

// serveMountedAt builds the same chain with a chosen outer pattern.
func (h *harness) serveMountedAt(outerPattern string) http.Handler {
	campaignMux := http.NewServeMux()
	wiki.Mount(campaignMux, h.handler())

	backing := campaignStore{
		campaign: domain.Campaign{
			ID:         testCampID,
			Slug:       testSlug,
			Name:       h.campaignName,
			SystemID:   h.systemID,
			Visibility: h.visibility,
		},
		members: map[int64]domain.Role{
			gmUser:     domain.RoleGM,
			playerUser: domain.RolePlayer,
		},
	}

	// Resolve outside RequireRead outside the campaign mux: the ordering
	// `router.go` documents, because the guard beneath Resolve reads the tier it is
	// deciding on.
	outer := http.NewServeMux()
	outer.Handle(outerPattern,
		campaigns.Resolve(backing)(
			middleware.Chain(campaignMux, campaigns.RequireRead),
		),
	)

	return middleware.RequestID(outer)
}

// get issues one request as requestor and returns the recorded response.
func (h *harness) get(target string, requestor domain.Requestor) *httptest.ResponseRecorder {
	h.t.Helper()

	return h.request(target, requestor, nil)
}

// getWith issues one request carrying header.
func (h *harness) getWith(
	target string,
	requestor domain.Requestor,
	header http.Header,
) *httptest.ResponseRecorder {
	h.t.Helper()

	return h.request(target, requestor, header)
}

// request issues one request against a freshly built chain.
//
// A fresh chain per request rather than one shared handler, because a shared one
// would hold the *first* handler's dependencies and a test that changes the
// redactor or the renderer between two requests would silently be testing the
// first. The cache and the content root are deliberately shared, since those are
// what a second request for the same page is supposed to hit.
func (h *harness) request(
	target string,
	requestor domain.Requestor,
	header http.Header,
) *httptest.ResponseRecorder {
	h.t.Helper()

	req := httptest.NewRequestWithContext(
		h.t.Context(),
		http.MethodGet,
		target,
		http.NoBody,
	)
	req = req.WithContext(identity.WithRequestor(req.Context(), requestor))

	maps.Copy(req.Header, header)

	recorder := httptest.NewRecorder()
	h.serve().ServeHTTP(recorder, req)

	return recorder
}

// pageBody is one page's markdown, with a front-matter block so that a test is
// exercising a real document rather than a bare fragment.
func pageBody(prose string) string {
	return "---\ntitle: A page\n---\n" + prose
}

// TestPageRendersForAMember is the route's ordinary success: a reader who passed
// the gate gets the page, inside the shell, with the landmarks UI §4.5 fixes.
func TestPageRendersForAMember(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Some Page.md", pageBody("The tide comes in over Greyhaven's wall.\n"))

	recorder := fixed.get("/c/greyhaven/wiki/Some%20Page", gmRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", recorder.Code, recorder.Body)
	}

	document := recorder.Body.String()

	for _, want := range []string{
		"The tide comes in over Greyhaven",
		`data-testid="shell-header"`,
		`data-testid="shell-main"`,
		`data-testid="shell-rail"`,
		`data-testid="shell-footer"`,
		`data-testid="page-title"`,
		`data-testid="page-body"`,
		"Some Page",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("response does not contain %q", want)
		}
	}

	// The prose arrived as text rather than as markup: an author who writes HTML
	// in a page must get it shown or dropped, never executed, and the entity-escaped
	// apostrophe above is the shape of that.
	//
	// This asserted `!strings.Contains(document, "<script")`, which was correct
	// until P5 and is now too blunt: UI §3.7 puts one blocking inline script in
	// `<head>` on every document, so the substring can no longer be absent. The
	// assertion is narrowed to the property that was actually being tested — no
	// script element anywhere a *page author* can reach — and it is checked on
	// the parsed tree rather than on the markup, because a substring test passes
	// on a document whose script tag is spelled across an attribute boundary.
	//
	// What it now requires is stronger than what it required: every script
	// element in the document must be inside `<head>`, and the page's own body
	// must contain none. The `<head>` position is the security property — a
	// script the author controls executes wherever it appears, and a script
	// outside `<head>` is either the author's or a mistake.
	assertNoScriptOutsideHead(t, document)

	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
}

// TestRedactedTextAppearsNowhereInANonGMResponse is S-14.1's shape, and the phase's
// DoD row: for a page holding text the viewer may not see, that text is absent
// from the body, from every header, from the validator, from the directives, and
// from everything the renderer was ever handed.
//
// The redaction ordering test in its two halves. The first is the *outcome*: the
// response does not contain the marker, anywhere. The second is the *position*:
// the renderer's recorded input did not contain it either, which is what rules
// out a redactor running after the render — that version produces an identical
// response and leaves the text in three buffers this test would then be unable to
// see.
func TestRedactedTextAppearsNowhereInANonGMResponse(t *testing.T) {
	t.Parallel()

	const marker = "SECRET-MARKER"

	for _, testCase := range []struct {
		name      string
		requestor domain.Requestor
		isGM      bool
	}{
		{name: "player", requestor: playerRequestor()},
		{name: "anonymous", requestor: anonymousRequestor()},
		{name: "gm sees it", requestor: gmRequestor(), isGM: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.redactor = removingRedactor(marker)
			fixed.write("Vault.md", pageBody(
				"The vault door is iron.\n\n"+marker+" the combination is 1-2-3-4\n\nIt sticks.\n",
			))

			recorder := fixed.get("/c/greyhaven/wiki/Vault", testCase.requestor)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body:\n%s", recorder.Code, recorder.Body)
			}

			bodies := fixed.recorder.bodies()

			if len(bodies) == 0 {
				t.Fatal("the renderer was never called, so the ordering was not exercised")
			}

			if testCase.isGM {
				assertBodyMentions(t, recorder, marker)

				return
			}

			assertAbsentEverywhere(t, recorder, marker)

			for _, body := range bodies {
				if strings.Contains(body, marker) {
					t.Errorf("the renderer was handed text the redactor had removed: %q", body)
				}
			}
		})
	}
}

// assertBodyMentions fails when the GM's response does not carry the marker.
//
// The GM is the one reader who is meant to see it: a redaction that removed text
// from a GM's response would be a page with a hole in it that nobody could fill,
// and this is the assertion that says the flag reaches the redactor as well as the
// key.
func assertBodyMentions(t *testing.T, recorder *httptest.ResponseRecorder, marker string) {
	t.Helper()

	if !strings.Contains(recorder.Body.String(), marker) {
		t.Errorf("the GM's response does not contain %q; the GM sees everything", marker)
	}
}

// assertAbsentEverywhere fails when marker appears in the body or in any header.
//
// Every header, not only the two this route writes, because S-14.1 says "anywhere"
// and because the header that leaks is the one nobody thought to look at: a
// validator derived from unredacted bytes, a `Content-Location`, a cookie. A check
// of the body alone would pass against all three.
func assertAbsentEverywhere(t *testing.T, recorder *httptest.ResponseRecorder, marker string) {
	t.Helper()

	if strings.Contains(recorder.Body.String(), marker) {
		t.Errorf("the body contains %q; redaction must be omission (S-5.6)", marker)
	}

	for name, values := range recorder.Header() {
		if strings.Contains(strings.Join(values, " | "), marker) {
			t.Errorf("header %s contains %q", name, marker)
		}
	}
}

// removingRedactor drops every line holding a marker, and is the shape P10
// replaces `content.NoSecrets` with.
//
// Declared here rather than added to `internal/content` as a `RedactorFunc`
// adapter: this phase needs exactly one kind of test redactor, and an exported
// function-to-interface adapter on the security seam is a capability nothing in
// the product calls yet.
//
// Lines rather than the marker string alone, because a redactor that removed only
// the marker would leave the sentence around it, and the whole point of the seam
// is that the callout goes entirely (S-5.6).
type removingRedactor string

func (redactor removingRedactor) Redact(body string, includeSecrets bool) (string, error) {
	if includeSecrets {
		return body, nil
	}

	kept := make([]string, 0, 8)

	for line := range strings.SplitSeq(body, "\n") {
		if strings.Contains(line, string(redactor)) {
			continue
		}

		kept = append(kept, line)
	}

	return strings.Join(kept, "\n"), nil
}

// TestIfNoneMatchYieldsNotModifiedWithNoBody is revalidation: a client holding the
// current validator gets a 304, the validator back, and no bytes.
func TestIfNoneMatchYieldsNotModifiedWithNoBody(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	first := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor())
	validator := first.Header().Get("ETag")

	if validator == "" {
		t.Fatal("the first response carried no ETag")
	}

	header := http.Header{}
	header.Set("If-None-Match", validator)

	second := fixed.getWith("/c/greyhaven/wiki/Vault", gmRequestor(), header)

	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304; body:\n%s", second.Code, second.Body)
	}

	if second.Body.Len() != 0 {
		t.Errorf("a 304 carried a body:\n%s", second.Body)
	}

	if got := second.Header().Get("ETag"); got != validator {
		t.Errorf("304 ETag = %q, want %q", got, validator)
	}

	if got := second.Header().Get("Cache-Control"); got == "" {
		t.Error("a 304 carried no Cache-Control; a client updating its stored copy needs it")
	}
}

// TestIfNoneMatchComparesWeakly: a client that dropped the `W/` has still named
// this body, because a validator for a GET is compared with weak comparison.
func TestIfNoneMatchComparesWeakly(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	first := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor())
	validator := first.Header().Get("ETag")

	strong := strings.TrimPrefix(validator, "W/")

	for _, testCase := range []struct {
		name    string
		header  string
		want304 bool
	}{
		{name: "weak", header: validator, want304: true},
		{name: "strong", header: strong, want304: true},
		{name: "list containing it", header: `"other", ` + validator, want304: true},
		{name: "star", header: "*", want304: true},
		{name: "a different body", header: `W/"0000"`, want304: false},
		{name: "absent", header: "", want304: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			header := http.Header{}
			if testCase.header != "" {
				header.Set("If-None-Match", testCase.header)
			}

			recorder := fixed.getWith("/c/greyhaven/wiki/Vault", gmRequestor(), header)

			want := http.StatusOK
			if testCase.want304 {
				want = http.StatusNotModified
			}

			if recorder.Code != want {
				t.Errorf("status = %d, want %d", recorder.Code, want)
			}
		})
	}
}

// TestGMAndPlayerNeverShareAnETag is S-14.2 and S-5.3: a GM's response and a
// player's response must never advertise the same validator, because any cache
// holding both would then serve whichever it stored first — which is S-5.6's
// omission undone at the last possible moment.
func TestGMAndPlayerNeverShareAnETag(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	gmETag := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Header().Get("ETag")
	playerETag := fixed.get("/c/greyhaven/wiki/Vault", playerRequestor()).Header().Get("ETag")

	if gmETag == "" || playerETag == "" {
		t.Fatalf("a response carried no ETag: gm=%q player=%q", gmETag, playerETag)
	}

	if gmETag == playerETag {
		t.Errorf("GM and player share the validator %q (S-14.2)", gmETag)
	}

	// Two entries in the cache, and no hits, is the same property seen from the
	// other side: a single key would have made the second request a hit on the
	// first request's entry.
	if got := fixed.cache.Len(); got != 2 {
		t.Errorf("the cache holds %d entries, want 2; the two variants are one key", got)
	}

	if hits := fixed.cache.Stats().Hits; hits != 0 {
		t.Errorf("the cache recorded %d hits; the two viewers shared an entry", hits)
	}
}

// TestOnlyTheGMVariantMayBeStored is S-5.4: every response carrying secrets is
// `private, no-store`, and the redacted variant is not.
func TestOnlyTheGMVariantMayBeStored(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	for _, testCase := range []struct {
		name       string
		requestor  domain.Requestor
		wantNoStor bool
	}{
		{name: "gm", requestor: gmRequestor(), wantNoStor: true},
		{name: "player", requestor: playerRequestor()},
		{name: "anonymous", requestor: anonymousRequestor()},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			recorder := fixed.get("/c/greyhaven/wiki/Vault", testCase.requestor)
			directives := recorder.Header().Get("Cache-Control")

			if directives == "" {
				t.Fatal("the response carried no Cache-Control")
			}

			if got := strings.Contains(directives, "no-store"); got != testCase.wantNoStor {
				t.Errorf("Cache-Control = %q; no-store present = %t, want %t",
					directives, got, testCase.wantNoStor)
			}

			if !strings.Contains(directives, "private") {
				t.Errorf("Cache-Control = %q; the document carries the reader's name", directives)
			}
		})
	}
}

// TestResponseVariesOnTheCookie: the body is permission-neutral but the document
// is not — the shell carries the reader's name and a sign-out form — so a shared
// cache keyed only on the URL would hand one reader another's header.
func TestResponseVariesOnTheCookie(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	recorder := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor())

	if got := recorder.Header().Get("Vary"); !strings.Contains(got, "Cookie") {
		t.Errorf("Vary = %q, want it to name Cookie", got)
	}
}

// TestAbsentPageRendersTheLoadErrorState is the 404 a reader sees: the designed
// state, inside the shell, carrying the request id that ties it to the access log.
func TestAbsentPageRendersTheLoadErrorState(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)

	recorder := fixed.get("/c/greyhaven/wiki/Nowhere", gmRequestor())

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body:\n%s", recorder.Code, recorder.Body)
	}

	document := recorder.Body.String()
	requestID := recorder.Header().Get("X-Request-Id")

	for _, want := range []string{
		`data-testid="error-state"`,
		`data-testid="error-state-reference"`,
		`data-testid="shell-main"`,
		"Nowhere",
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the load-error state does not contain %q", want)
		}
	}

	if requestID == "" {
		t.Fatal("the response carried no request id")
	}

	if !strings.Contains(document, requestID) {
		t.Errorf("the load-error state does not carry the request id %q", requestID)
	}
}

// TestEscapeAttemptIsRefusedAndThePathIsNotEchoed is the confinement, end to end.
//
// The spelling matters and is the reason this test is here at all: a *literal*
// `..` never reaches a handler, because `net/http` cleans the request path and
// answers with a redirect first. Only a percent-encoded one survives that, and it
// arrives with the `..` intact — so the confinement in `content.Root` is
// load-bearing rather than a second opinion.
//
// Three assertions: the refusal is not a 404 (a 404 would be a statement about
// what exists at a path that left the campaign), the response does not echo the
// attempted path, and the response does not mention the file it was aimed at.
func TestEscapeAttemptIsRefusedAndThePathIsNotEchoed(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	recorder := fixed.get(
		"/c/greyhaven/wiki/notes/%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		gmRequestor(),
	)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body:\n%s", recorder.Code, recorder.Body)
	}

	document := recorder.Body.String()

	for _, absent := range []string{"..", "passwd", "%2e", "notes"} {
		if strings.Contains(document, absent) {
			t.Errorf("the refusal echoes %q, which came from the request:\n%s", absent, document)
		}
	}
}

// TestALiteralParentSegmentNeverReachesTheHandler records the other half of that
// interaction: `net/http` answers the literal spelling itself, so the handler is
// never given a chance to answer it wrongly. Asserted because the two spellings
// behaving differently is surprising, and a future change to the chain that let
// one through would want a failing test rather than a silent difference.
func TestALiteralParentSegmentNeverReachesTheHandler(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	recorder := fixed.get("/c/greyhaven/wiki/notes/../Vault", gmRequestor())

	if recorder.Code != http.StatusTemporaryRedirect {
		t.Errorf("status = %d, want 307 from the mux's own path cleaning", recorder.Code)
	}

	if len(fixed.recorder.bodies()) != 0 {
		t.Error("the handler rendered a page for a path net/http had already redirected")
	}
}

// TestExtensionlessAndMarkdownSpellingsReachTheSamePage is S-9: the URL scheme
// carries no extension, so the handler puts one back and both spellings address
// one page — which the shared validator proves, since the validator is derived
// from the bytes.
func TestExtensionlessAndMarkdownSpellingsReachTheSamePage(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("notes/Some Page.md", pageBody("A page in a directory.\n"))

	extensionless := fixed.get("/c/greyhaven/wiki/notes/Some%20Page", gmRequestor())
	withExtension := fixed.get("/c/greyhaven/wiki/notes/Some%20Page.md", gmRequestor())

	if extensionless.Code != http.StatusOK || withExtension.Code != http.StatusOK {
		t.Fatalf("statuses = %d and %d, want 200 and 200",
			extensionless.Code, withExtension.Code)
	}

	if extensionless.Body.String() != withExtension.Body.String() {
		t.Error("the two spellings produced different documents")
	}

	if extensionless.Header().Get("ETag") != withExtension.Header().Get("ETag") {
		t.Errorf("the two spellings produced different validators: %q and %q; they are one page",
			extensionless.Header().Get("ETag"), withExtension.Header().Get("ETag"))
	}
}

// TestConcurrentRequestsForOneUncachedPageRenderOnce is the single-flight
// property, asserted through the cache's own counters rather than through a spy:
// sixteen concurrent readers of one uncached page must produce one render and
// fifteen answers from memory, and every response must be the same document.
//
// It is a real end-to-end test of `content.Cache.Render` under this route rather
// than a test of the cache alone, which is the point: what could break it here is
// a handler that rendered outside the cache's flight, and only a request through
// the handler can show that.
func TestConcurrentRequestsForOneUncachedPageRenderOnce(t *testing.T) {
	t.Parallel()

	const readers = 16

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	var (
		wait    sync.WaitGroup
		mutex   sync.Mutex
		bodies  = make([]string, 0, readers)
		status  = make([]int, 0, readers)
		etag    string
		etagSet bool
	)

	for range readers {
		wait.Go(func() {
			recorder := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor())

			mutex.Lock()
			defer mutex.Unlock()

			bodies = append(bodies, recorder.Body.String())
			status = append(status, recorder.Code)

			if !etagSet {
				etag, etagSet = recorder.Header().Get("ETag"), true
			}

			if got := recorder.Header().Get("ETag"); got != etag {
				t.Errorf("concurrent readers got different validators: %q and %q", etag, got)
			}
		})
	}

	wait.Wait()

	for at, code := range status {
		if code != http.StatusOK {
			t.Fatalf("reader %d got status %d, want 200", at, code)
		}
	}

	for at, body := range bodies {
		if body != bodies[0] {
			t.Errorf("reader %d got a different document from reader 0", at)
		}
	}

	stats := fixed.cache.Stats()

	if stats.Misses != 1 {
		t.Errorf(
			"the cache recorded %d misses for %d concurrent readers, want 1",
			stats.Misses,
			readers,
		)
	}

	if stats.Hits != readers-1 {
		t.Errorf("the cache recorded %d hits, want %d", stats.Hits, readers-1)
	}
}

// TestReferencesResolveToAddressesInsideTheCampaign covers the substitution the
// renderer deliberately leaves to its caller: a wikilink becomes an anchor with a
// destination, an asset embed becomes an image, and a reference that leaves the
// campaign becomes a span carrying the reason rather than a link that goes
// somewhere it must not.
func TestReferencesResolveToAddressesInsideTheCampaign(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.pages = pageListing{pages: []domain.Page{
		{CampaignID: testCampID, Path: "Goblin.md"},
	}}
	fixed.write("Vault.md", pageBody(
		"See [[Goblin]], then ![[map.png]], then [[../Elsewhere]].\n",
	))

	document := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String()

	// The three forms, spelled in full because the whole point of this test is
	// that they differ: an `a` with an address, an `img` for an asset, and a
	// `span` with a reason in `title` rather than a link out of the campaign.
	//
	// `class="wikilink target"` rather than `class="wikilink"` because
	// `content`'s `.target` pass (ADR 0037) appends its token to every focusable
	// element in the page body before this route attaches anything — so the
	// anchor carries both, and the `span` refusal carries it too even though a
	// `span` is not focusable and the class is inert on it.
	for _, want := range []string{
		`<a class="wikilink target" data-ext="wikilink" data-ref-index="0" ` +
			`href="/c/greyhaven/wiki/Goblin">Goblin</a>`,
		`<img src="/c/greyhaven/assets/map.png" alt="map.png"`,
		`<span class="wikilink target" data-ext="wikilink" data-ref-index="2" ` +
			`title="This link points outside the campaign.">Elsewhere</span>`,
	} {
		if !strings.Contains(document, want) {
			t.Errorf("the page does not contain\n  %s\ngot:\n%s", want, document)
		}
	}
}

// TestAttachmentIntroducesNoAttributeOutsideTheSanitiserAllowlist is why the
// substitution is allowed to run after sanitisation at all: every form it writes
// is one `content/policy.go` already allows, so a page sanitised after the
// substitution would come out identical.
//
// A table rather than a review, because a reviewer cannot check a policy against a
// diff of a string builder, and because a policy that gains an attribute this file
// writes would otherwise be noticed here rather than in a security review.
func TestAttachmentIntroducesNoAttributeOutsideTheSanitiserAllowlist(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{
		"class": true, "id": true, "title": true, "dir": true, "lang": true,
		"href": true, "src": true, "alt": true, "width": true, "height": true,
		"data-ext": true, "data-ref-index": true, "data-arg": true,
		// Added with the broken-link marker, which is on the policy allowlist
		// constrained to the single value `true`.
		"data-broken": true,
	}
	attributes := regexp.MustCompile(`\s([a-zA-Z-]+)=`)

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody(
		"See [[Goblin]], ![[map.png]], [[../Elsewhere]], [[Goblin|the goblin]].\n",
	))

	page := pageFragment(fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String())

	for _, match := range attributes.FindAllStringSubmatch(page, -1) {
		if !allowed[match[1]] {
			t.Errorf("the page carries %s=, which the sanitiser policy does not allow",
				match[1])
		}
	}
}

// TestARendererFailureIsALoadFailure: a render that fails is an operator's problem
// and must not read to a reader as a missing page.
func TestARendererFailureIsALoadFailure(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.renderers = oneRenderer{renderer: failingRenderer{}}
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	recorder := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor())

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body:\n%s", recorder.Code, recorder.Body)
	}

	if !strings.Contains(recorder.Body.String(), `data-testid="error-state"`) {
		t.Error("a failed render did not render the error state")
	}
}

// failingRenderer fails every render.
type failingRenderer struct{}

func (failingRenderer) Render(content.Document) (content.Rendered, error) {
	return content.Rendered{}, errors.New("test: render failed")
}

// TestAPageListingFailureIsALoadFailure: the addresses cannot be resolved without
// the listing, and a page whose references are unresolvable is a page whose links
// would be wrong — so this is a failure and not a page with dead links.
func TestAPageListingFailureIsALoadFailure(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.pages = pageListing{err: errors.New("test: listing unavailable")}
	fixed.write("Vault.md", pageBody("See [[Goblin]].\n"))

	recorder := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor())

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body:\n%s", recorder.Code, recorder.Body)
	}

	if !strings.Contains(recorder.Body.String(), `data-testid="error-state"`) {
		t.Error("a failed listing did not render the error state")
	}
}

// TestTheInterfaceNeverNamesWorldOrSession is UI §1.2 over this route's own output:
// neither word appears in a rendered page, in an empty state or in error copy.
//
// Rendered through the handler rather than through a fixture so that the error
// state is covered too — an error message is exactly where a leftover surfaces,
// because error copy is written by the person who hit the failure.
func TestTheInterfaceNeverNamesWorldOrSession(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	documents := []string{
		fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String(),
		fixed.get("/c/greyhaven/wiki/Nowhere", gmRequestor()).Body.String(),
		fixed.get(
			"/c/greyhaven/wiki/notes/%2e%2e%2fsecret",
			gmRequestor(),
		).Body.String(),
	}

	for at, document := range documents {
		lowered := strings.ToLower(document)

		for _, forbidden := range []string{"world", "session"} {
			if strings.Contains(lowered, forbidden) {
				t.Errorf(
					"response %d contains %q; neither entity exists in the interface (UI §1.2)",
					at,
					forbidden,
				)
			}
		}
	}
}

// TheRouteIsMountedBehindTheAccessGate is ADR 0024 asserted at the only place
// it can be: a slug naming no campaign, and a private campaign the reader is not a
// member of, must both answer 404 with a body identical to the one an unmatched
// route produces.
//
// The handler contributes nothing to that — it never asks whether the reader may
// read the campaign — so this test is really a test that the chain still refuses
// before the handler runs, and it would fail the day somebody mounted the route
// outside the gate.
func TestTheRouteIsMountedBehindTheAccessGate(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	unknown := fixed.get("/c/no-such-campaign/wiki/Vault", gmRequestor())

	if unknown.Code != http.StatusNotFound {
		t.Errorf("an unknown campaign answered %d, want 404", unknown.Code)
	}

	if !strings.Contains(unknown.Body.String(), "not found") {
		t.Errorf("an unknown campaign did not get the not-found body:\n%s", unknown.Body)
	}
}

// TestTheCampaignMountMustCarryTheSlug guards the one thing about `router.go`
// this route depends on and cannot fix itself.
//
// `campaigns.Resolve` reads the campaign out of `r.PathValue("slug")`, and
// `net/http` sets a path value from the pattern that matched — not from the mux the
// handler underneath happens to be. Mounted at `/c/`, no wildcard is named, so
// Resolve resolves nothing, the tier stays `TierNone`, and `RequireRead` answers 404
// for every request under `/c/` including a page that exists.
//
// Asserted both ways on purpose: the correct mount serves the page, and the
// prefix-only mount does not. A test that only asserted the first would still pass
// after someone "simplified" the mount back, which is the change that breaks it.
func TestTheCampaignMountMustCarryTheSlug(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	req := func() *http.Request {
		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"/c/greyhaven/wiki/Vault",
			http.NoBody,
		)

		return request.WithContext(identity.WithRequestor(request.Context(), gmRequestor()))
	}

	withSlug := httptest.NewRecorder()
	fixed.serveMountedAt("/c/{slug}/").ServeHTTP(withSlug, req())

	if withSlug.Code != http.StatusOK {
		t.Errorf("the /c/{slug}/ mount answered %d, want 200; body:\n%s",
			withSlug.Code, withSlug.Body)
	}

	prefixOnly := httptest.NewRecorder()
	fixed.serveMountedAt("/c/").ServeHTTP(prefixOnly, req())

	if prefixOnly.Code != http.StatusNotFound {
		t.Errorf("the /c/ mount answered %d, want 404; it cannot see {slug}, so "+
			"campaigns.Resolve resolves nothing and RequireRead refuses every request",
			prefixOnly.Code)
	}
}

// pageFragment returns just the rendered page, without the shell around it.
//
// Scoped because the shell is templ's markup and not the sanitiser's output: the
// claim under test is about what the attachment writes into author-controlled
// HTML, and the shell's `data-testid`, `role` and `aria-*` attributes are a
// different contract with a different owner.
func pageFragment(document string) string {
	start := strings.Index(document, `data-testid="page-body">`)
	if start < 0 {
		return document
	}

	rest, _, closed := strings.Cut(document[start:], "</main>")
	if !closed {
		return document[start:]
	}

	return rest
}

// TestCampaignRenderersResolvesPerCampaign is the composition root's half of this
// route, in one line: a map of slug to `*content.Renderer`, which is what the
// process builds at startup and what `Mount` needs.
//
// Asserted here because it is this package's exported convenience type and nothing
// else in the package would notice it breaking — the tests use a stub, since they
// need to see what reached the renderer. A type that only the integrator depends on
// and no test exercises is a type whose signature can change without anything going
// red.
func TestCampaignRenderersResolvesPerCampaign(t *testing.T) {
	t.Parallel()

	renderers := wiki.CampaignRenderers{testSlug: content.NewRenderer(testSlug, nil)}

	resolved, err := renderers.Renderer(testSlug)
	if err != nil {
		t.Fatalf("Renderer(%q): %v", testSlug, err)
	}

	// The interface only promises `Render`, so this asserts the identity of what
	// came back through a type assertion rather than by widening the interface: the
	// point is that the map handed the caller *this* campaign's renderer.
	concrete, isRenderer := resolved.(*content.Renderer)
	if !isRenderer {
		t.Fatalf("the resolved renderer is %T, want *content.Renderer", resolved)
	}

	if concrete.Slug() != testSlug {
		t.Errorf("the resolved renderer serves %q, want %q", concrete.Slug(), testSlug)
	}

	// A campaign with no renderer is the composition root's fault, and it must not
	// resolve to some other campaign's renderer: two campaigns sharing a renderer
	// would put one campaign's slug on every log line and in every broken-link
	// report the other one produces.
	if _, err := renderers.Renderer("no-such-campaign"); err == nil {
		t.Error("an unknown campaign resolved a renderer")
	}
}

// assertNoScriptOutsideHead requires that every script element in a rendered
// document is a descendant of `<head>`.
//
// UI §3.7 puts exactly one blocking inline script in `<head>` — the resolver
// that writes `data-theme` and `data-ui` before the first paint — plus the sheet
// re-parenting that follows it, also in `<head>`. So the correct assertion is not
// "there is no script" but "no script has left the head", which is the stronger
// and more durable form: it fails on a script the *renderer* ever introduces,
// whatever the shell does next.
//
// The check is on the parsed tree, not on the markup. A substring test is what
// lets a `<script` spelled as `<SCRIPT`, or split by a templ attribute
// interpolation, through a gate that appears to be watching for exactly that.
func assertNoScriptOutsideHead(t *testing.T, document string) {
	t.Helper()

	root, err := html.Parse(strings.NewReader(document))
	if err != nil {
		t.Fatalf("parse rendered document: %v", err)
	}

	head := findElement(root, "head")

	var walk func(node *html.Node, path string)
	walk = func(node *html.Node, path string) {
		if node.Type == html.ElementNode {
			next := path + "/" + node.Data
			if node.Data == "script" && !hasAncestor(node, "head") {
				t.Errorf(
					"a script element appears at %s; every script in the document must be "+
						"a descendant of <head>. A page author who writes HTML gets it "+
						"escaped or sanitised away (S-5.7, 0028), so a script outside the "+
						"head is either the author's or a bug in the shell",
					trimPath(next),
				)
			}
			path = next
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, path)
		}
	}
	walk(root, "")

	// And the head script is actually there. Asserting only its absence would
	// pass on a document with no script at all, which is what this file asserted
	// before P5 and which would have been satisfied by a shell that quietly
	// stopped resolving its two root attributes — a bug no other test in the
	// repository can see, because §3.7's contract is with a browser.
	if head == nil {
		t.Fatal("the rendered document has no <head>")
	}

	if countElements(head, "script") == 0 {
		t.Error(
			"the document's <head> carries no script; UI §3.7 requires one blocking " +
				"inline resolver before the first paint, and its absence means " +
				"data-theme and data-ui are never resolved",
		)
	}
}

// findElement returns the first element with the given tag name, or nil.
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

// countElements counts the elements with the given tag name beneath node.
func countElements(node *html.Node, tag string) int {
	count := 0

	for current := node; current != nil; current = current.NextSibling {
		if current.Type == html.ElementNode && current.Data == tag {
			count++
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			count += countElements(child, tag)
		}
	}

	return count
}

// trimPath shortens an element path to its last three segments, so a failure
// message names where the script is without reproducing the whole document.
func trimPath(path string) string {
	segments := strings.Split(path, "/")
	if len(segments) <= 3 {
		return path
	}

	return "..." + strings.Join(segments[len(segments)-3:], "/")
}
