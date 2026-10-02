package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/campaignroots"
	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/events"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/httpapi/wiki"
	"github.com/semiplane/semiplane/internal/observability"
	"github.com/semiplane/semiplane/internal/store"
)

// The slugs these fixtures register. Constants rather than literals because a slug
// appears in a URL, a directory and an assertion, and three copies of one string is
// two copies waiting to disagree with the third.
const (
	// fixtureSlug is the campaign with a readable vault. Public, so an anonymous
	// request reaches the wiki route and these tests measure the pipeline rather
	// than the access gate.
	fixtureSlug = "greyhaven"

	// unmountedSlug is the campaign whose vault is deleted after registration: the
	// shape a campaign whose disk is not mounted has at startup.
	unmountedSlug = "blackgate"
)

// closeTimeout bounds the shutdown assertion.
//
// Thirty seconds rather than one, because the failure it guards against is a
// goroutine parked on a channel nobody closes, which never returns at all — so the
// only question a shorter bound adds is how many CI machines it fails on. It is
// several orders of magnitude above what closing two goroutines costs.
const closeTimeout = 30 * time.Second

// instance is one wired server over a real store and real vaults: the store, the
// counter registry, the account, and the campaigns and their content. Everything
// that touches a filesystem or a database is the real thing, because every claim
// these tests make is about the order real subsystems were constructed in.
type instance struct {
	t          *testing.T
	store      *store.Store
	registry   *observability.Registry
	base       string
	owner      domain.User
	campaign   domain.Campaign
	registered []domain.Campaign

	// lister is the page listing the router was built with, kept so a test asserts
	// against the seam the product was handed rather than against a value it
	// constructed itself. A test that built its own `pageLister` would pass with a
	// store-backed one installed while the product ran on a different one.
	lister pageLister

	// plane is the realtime plane the router was built with, held for the same
	// reason as `lister`: a test asserting that a refusal opened no campaign state
	// asks the registry the product was handed, and a test that built its own
	// registry would be asserting about a different process.
	plane *realtimePlane
}

// newInstance opens a store, creates one account, and registers one campaign with
// it as the GM.
//
// A real account rather than a fabricated membership row, because `Register` seeds
// the GM membership itself and a fixture that inserted the row directly would be
// asserting against a wiring the product does not have.
func newInstance(t *testing.T) *instance {
	t.Helper()

	base := t.TempDir()
	database := filepath.Join(base, "semiplane.db")

	db, err := store.Open(t.Context(), "file:"+database)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// Registered first, so it runs last: the store has to outlive the pipeline,
	// whose goroutines write to it.
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close store: %v", closeErr)
		}
	})

	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	owner, err := db.CreateUser(t.Context(), domain.User{
		Username:     "ada",
		PasswordHash: hash,
		IsAdmin:      true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	inst := &instance{
		t:        t,
		store:    db,
		registry: observability.NewRegistry(),
		base:     base,
		owner:    owner,
	}

	inst.campaign = inst.registerCampaign(fixtureSlug)

	return inst
}

// registerCampaign registers one campaign through the registrar the CLI and the
// HTTP route both use, which creates its content root at mode 0700 and writes the
// absolute `content_root` column the watcher routes events by.
func (i *instance) registerCampaign(slug string) domain.Campaign {
	i.t.Helper()

	registrar := campaigns.NewRegistrar(i.store, filepath.Join(i.base, "vaults"), nil)

	campaign, err := registrar.Register(i.t.Context(), campaigns.RegisterRequest{
		Slug:           slug,
		Name:           slug,
		SystemID:       "5e-2024",
		RulesetVersion: "v1",
		Visibility:     domain.VisibilityPublic,
		OwnerID:        i.owner.ID,
	})
	if err != nil {
		i.t.Fatalf("register campaign %s: %v", slug, err)
	}

	i.registered = append(i.registered, campaign)

	return campaign
}

// writePage writes one page into a campaign's vault.
//
// Directly, and not through `content.Target.WriteFile`: this fixture is creating
// the world the product reads rather than exercising the write path, and the atomic
// write stages a temporary file and renames it — a shape the watcher has to survive
// and which these tests are not about.
func (i *instance) writePage(campaign domain.Campaign, rel, body string) {
	i.t.Helper()

	target := filepath.Join(campaign.ContentRoot, filepath.FromSlash(rel))

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		i.t.Fatalf("create the directory of %s: %v", rel, err)
	}

	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		i.t.Fatalf("write %s: %v", rel, err)
	}
}

// assemble opens the content roots and builds the pipeline over them, registering
// the teardown in the order `runServer` uses: stop the pipeline, then close the
// roots. The store is closed by the fixture's own cleanup, registered earlier, so it
// runs last — and the order is the point, because an `os.Root` closed while the
// settle filter still stats through it is a failure this test file would have
// manufactured.
func (i *instance) assemble(
	ctx context.Context,
	registered []domain.Campaign,
) (*contentPipeline, *content.Registry) {
	i.t.Helper()

	roots, _, err := campaignroots.Open(ctx, i.store, discardLogger())
	if err != nil {
		i.t.Fatalf("open campaign content roots: %v", err)
	}

	pipeline, err := newContentPipeline(
		ctx,
		roots,
		registered,
		i.store,
		pageKinds{},
		newContentSignals(i.registry, discardLogger()),
		// No event hub. The pipeline indexes either way, and a test asserting the
		// index converges must not also depend on a browser being attached.
		nil,
	)
	if err != nil {
		i.t.Fatalf("wire the content pipeline: %v", err)
	}

	i.t.Cleanup(func() {
		if err := pipeline.Close(); err != nil {
			i.t.Errorf("close content pipeline: %v", err)
		}

		if err := roots.Close(); err != nil {
			i.t.Errorf("close campaign content roots: %v", err)
		}
	})

	return pipeline, roots
}

// serve assembles the pipeline, runs the startup index, and builds the router over
// both — the whole of `runServer` from the content roots onwards.
//
// One helper rather than four, because the claim under test is about the *order* of
// those calls. A test that assembled them itself would be asserting that its own
// ordering works.
func (i *instance) serve(registered []domain.Campaign) http.Handler {
	i.t.Helper()

	ctx, cancel := context.WithCancel(i.t.Context())
	i.t.Cleanup(cancel)

	pipeline, roots := i.assemble(ctx, registered)

	// Before the router, and that is the whole claim: the handler these tests
	// serve was built over a table this call filled.
	pipeline.buildIndex(ctx, discardLogger())

	i.lister = pageLister{db: i.store}

	// The instance view the product builds, so every handler here is assembled from
	// the same parts `runServer` uses. A zero value would make these fixtures assert
	// about handlers no deployment serves — and would report every campaign healthy,
	// which is the bug this argument exists to stop coming back.
	instance := instanceView(testConfig(), nil)

	// One map, two routes. Hoisted rather than built twice so this helper cannot
	// drift from the composition root on the question that matters: the wiki route
	// and the editor must hold the same renderers, and a helper that built a second
	// map would be a place where that quietly stopped being true.
	renderers := i.renderers(roots, registered)

	// The hub, closed with the test. An unclosed hub holds no goroutine and no
	// resource — it is a mutex and a map — so this is hygiene rather than
	// correctness, and it is here because a fixture that constructs one and forgets
	// is how the next assertion about stream teardown gets written against a hub
	// another test already closed.
	hub := events.NewHub()
	i.t.Cleanup(func() {
		if err := hub.Close(); err != nil {
			i.t.Errorf("close the event hub: %v", err)
		}
	})

	// The realtime plane, through the product's own constructor over the fixture's
	// real store. `instance.serve` is the composition root's second half, and a
	// router that mounted `/play` over a hub this file built itself would assert
	// the route's table rather than the product's.
	//
	// `assemble` ran before this, so the pipeline's cleanup is already registered
	// and therefore runs *after* this one — the same order `runServer` produces for
	// the same two resources, and the order that matters: the plane stops, then the
	// pipeline, then the roots, then the store. A flush issued after the pipeline
	// had stopped writing is a flush contending with a writer that is going away.
	i.plane = newRealtimePlane(ctx, i.store, i.registry, discardLogger())
	i.t.Cleanup(func() {
		closeRealtimePlane(i.t.Context(), i.plane, discardLogger(), closeTimeout)
	})

	// The boot pass, over the fixture's campaigns. Its only effect on these tests
	// is a log line into a discarded logger, and running it is the point: a wiring
	// test that skipped the step the composition root runs would leave the step
	// unexercised everywhere.
	resumeCampaignStates(ctx, i.plane, registered, discardLogger())

	return httpapi.NewRouter(
		discardLogger(),
		testConfig(),
		i.registry,
		nil,
		i.store,
		newWikiRoute(
			roots,
			renderers,
			pageKinds{},
			i.lister,
			instance,
			discardLogger(),
		),
		newAssetRoute(roots, instance, discardLogger()),
		newSearchRoute(i.store, instance, discardLogger()),
		newEditRoute(
			roots,
			i.store,
			editorRenderers(renderers),
			pageKinds{},
			instance,
			discardLogger(),
		),
		newEventRoute(hub, discardLogger()),
		newPlayRoute(i.plane.hub, discardLogger()),
	)
}

// renderers builds one renderer per campaign whose content root is open, exactly
// as `runServer` does.
//
// A campaign without a root gets no renderer, which is why the healthy campaign's
// pages keep working beside a degraded one: the degraded campaign never reaches the
// renderer lookup at all.
func (i *instance) renderers(
	roots *content.Registry,
	registered []domain.Campaign,
) wiki.CampaignRenderers {
	renderers := make(wiki.CampaignRenderers, len(registered))

	for idx := range registered {
		if _, err := roots.Get(registered[idx].Slug); err != nil {
			continue
		}

		renderers[registered[idx].Slug] = content.NewRenderer(registered[idx].Slug, pageKinds{})
	}

	return renderers
}

// TestTheIndexIsBuiltBeforeTheRouterServes is the load-bearing test of this
// wiring: the `pages` table is populated before the first request, and a request
// therefore resolves a bare `[[Goblin]]` to a real page.
//
// It fails in the two ways that matter. Against an empty table — a `buildIndex` that
// was never called — the resolver has no name to answer and every reference in the
// product renders `data-broken="true"`. And against a `pageLister` that walks the
// content root instead, the row assertions fail, because a walk reads the files and
// leaves no row behind: nothing that reads a directory computes a `content_hash`.
//
// The row assertions come first on purpose. They are about the table, and they are
// the half of this test that cannot be satisfied by a filesystem walk — which is
// what makes the response assertion below the *consequence* of the boot step rather
// than a second, weaker restatement of it.
func TestTheIndexIsBuiltBeforeTheRouterServes(t *testing.T) {
	// Not parallel, and the reason is ADR 0004 rather than caution: `store.Open`
	// claims the process's one store slot and refuses a second handle, because two
	// handles would mean two writer queues. A parallel test would therefore fail on
	// whichever fixture lost the race — a failure about test scheduling wearing the
	// costume of a failure about the wiring.

	inst := newInstance(t)
	inst.writePage(inst.campaign, "index.md",
		"---\ntitle: The Gate\n---\n\nThe gate is watched by a [[Goblin]].\n")
	inst.writePage(inst.campaign, "Goblin.md",
		"---\ntitle: Goblin\n---\n\nIt watches the gate, and it is owed a week.\n")

	handler := inst.serve(inst.registered)

	pages, err := inst.lister.PagesForCampaign(t.Context(), inst.campaign.ID)
	if err != nil {
		t.Fatalf("PagesForCampaign: %v", err)
	}

	goblin, found := indexedPage(pages, "Goblin.md")
	if !found {
		t.Fatalf(
			"indexed pages = %v, want one at Goblin.md; the startup index did not run",
			pagePaths(pages),
		)
	}

	if goblin.ContentHash == "" {
		t.Error("Goblin.md's row carries no content_hash, so it did not come from the indexer")
	}

	if goblin.Title != "Goblin" {
		t.Errorf("Goblin.md title = %q, want %q", goblin.Title, "Goblin")
	}

	// The bare name, which is the spelling S-5.5 resolves by base name within the
	// campaign. Asserted on the same listing the route reads, so a resolution that
	// worked only for a relative path would not pass.
	resolved, found := content.NewPageIndex(pages).Lookup("Goblin")
	if !found || resolved != "Goblin" {
		t.Errorf("Lookup(Goblin) = %q, %v; want %q, true", resolved, found, "Goblin")
	}

	// And the product's own answer, through the router, to the same reference.
	recorder := serveWiki(t, handler, inst.campaign.Slug, "index")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /c/%s/wiki/index = %d, want 200; body:\n%s",
			inst.campaign.Slug, recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	if strings.Contains(body, `data-broken="true"`) {
		t.Errorf(
			"the served page marks [[Goblin]] broken, so a request observed a table the startup index had not filled:\n%s",
			body,
		)
	}

	if !strings.Contains(body, `href="/c/`+fixtureSlug+`/wiki/Goblin"`) {
		t.Errorf("the served page does not link to Goblin:\n%s", body)
	}
}

// TestAMissingContentRootDegradesItsCampaignAndNotTheInstance: S-4.5 says a
// campaign whose vault is not there is degraded and the server still starts, and
// ADR 0024 says its absence must not read as "this campaign does not exist".
//
// The status is the assertion that matters. A 404 would be the gate doing its job
// for a campaign that is not there — and a reader, and an attacker, could not tell
// it from a campaign that has been deleted. A 500 carrying the request id says
// *this instance could not load it*, which is the truth and correlates with the log
// line that says why.
func TestAMissingContentRootDegradesItsCampaignAndNotTheInstance(t *testing.T) {
	// Not parallel, and the reason is ADR 0004 rather than caution: `store.Open`
	// claims the process's one store slot and refuses a second handle, because two
	// handles would mean two writer queues. A parallel test would therefore fail on
	// whichever fixture lost the race — a failure about test scheduling wearing the
	// costume of a failure about the wiring.

	inst := newInstance(t)
	inst.writePage(inst.campaign, "index.md",
		"---\ntitle: The Gate\n---\n\nThe gate is watched by a [[Goblin]].\n")
	inst.writePage(inst.campaign, "Goblin.md",
		"---\ntitle: Goblin\n---\n\nIt watches the gate.\n")

	// A second campaign, registered and then emptied: a campaign whose vault lives
	// on a disk that is not mounted.
	unmounted := inst.registerCampaign(unmountedSlug)
	if err := os.RemoveAll(unmounted.ContentRoot); err != nil {
		t.Fatalf("remove the vault of %s: %v", unmounted.Slug, err)
	}

	// The wiring must succeed. If this call failed, the process would refuse to
	// start over one disk that is not mounted, which is the failure S-4.5 exists to
	// prevent.
	handler := inst.serve(inst.registered)

	// The campaign that can be read still serves, links and all: a degraded
	// instance is one where something is wrong, not one where nothing works.
	healthy := serveWiki(t, handler, inst.campaign.Slug, "index")
	if healthy.Code != http.StatusOK {
		t.Fatalf("GET /c/%s/wiki/index = %d, want 200 beside a degraded campaign; body:\n%s",
			inst.campaign.Slug, healthy.Code, healthy.Body.String())
	}

	if strings.Contains(healthy.Body.String(), `data-broken="true"`) {
		t.Errorf("the healthy campaign's page has a broken reference:\n%s", healthy.Body.String())
	}

	// And the degraded one answers a load error naming the request id.
	const probe = "degraded-probe"

	request := get(t, "/c/"+unmountedSlug+"/wiki/index")
	request.Header.Set(middleware.RequestIDHeader, probe)

	failure := httptest.NewRecorder()
	handler.ServeHTTP(failure, request)

	if failure.Code != http.StatusInternalServerError {
		t.Errorf(
			"GET /c/%s/wiki/index = %d, want 500; a 404 would read as \"this campaign does not exist\" (ADR 0024)",
			unmountedSlug,
			failure.Code,
		)
	}

	// The id carries the middleware's `fwd-` prefix rather than the raw header
	// value, because a caller-supplied id is marked rather than adopted. Asserting
	// the prefixed form also asserts that marking: an id echoed verbatim would be a
	// reader's own string sitting in this instance's log uncorrelated with ours.
	if !strings.Contains(failure.Body.String(), "fwd-"+probe) {
		t.Errorf("the load error does not carry the request id:\n%s", failure.Body.String())
	}
}

// TestShutdownDoesNotHangWithAWatcherAndSettleFilterLive: both components own a
// goroutine this process started, and `Close` is the only thing that says when they
// stop.
//
// The settle filter is armed with a pending change first, so the shutdown is asked
// to stop a scheduler with work outstanding rather than an idle one — which is the
// case that would hang if `Close` signalled without waiting.
func TestShutdownDoesNotHangWithAWatcherAndSettleFilterLive(t *testing.T) {
	// Not parallel, and the reason is ADR 0004 rather than caution: `store.Open`
	// claims the process's one store slot and refuses a second handle, because two
	// handles would mean two writer queues. A parallel test would therefore fail on
	// whichever fixture lost the race — a failure about test scheduling wearing the
	// costume of a failure about the wiring.

	inst := newInstance(t)
	inst.writePage(inst.campaign, "index.md", "---\ntitle: The Gate\n---\n\nWatched.\n")

	// A context this test owns and does not cancel, so what is measured is `Close`
	// and not the framework tidying up afterwards.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	pipeline, _ := inst.assemble(ctx, inst.registered)

	// Armed the way a filesystem event arms it, through the same call the watcher's
	// sink makes. `Touch` takes no context, deliberately: it does no I/O.
	pipeline.debouncer.Touch(inst.campaign.ID, inst.campaign.Slug, "index.md")

	closed := make(chan error, 1)

	go func() {
		closed <- pipeline.Close()
	}()

	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(closeTimeout):
		t.Fatalf(
			"Close did not return within %s with a watcher and a settle filter live",
			closeTimeout,
		)
	}

	// Idempotent, and asserted because both this test's cleanup and a signal
	// handler reach it: a second call that panicked on the way out of a shutdown
	// would be a process dying with a stack trace on SIGTERM.
	if err := pipeline.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestReadyzRendersThePipelineCountersAsZeros: S-12.1's promise is that an
// unwired signal reads as a visible zero rather than an absent key, and the only
// way to keep that promise is for something to look for the key.
//
// Every counter this wiring registers, at zero, on a process where nothing has gone
// wrong — the state an operator reads first, and the state in which a missing key is
// indistinguishable from a subsystem that was never built.
func TestReadyzRendersThePipelineCountersAsZeros(t *testing.T) {
	// Not parallel, and the reason is ADR 0004 rather than caution: `store.Open`
	// claims the process's one store slot and refuses a second handle, because two
	// handles would mean two writer queues. A parallel test would therefore fail on
	// whichever fixture lost the race — a failure about test scheduling wearing the
	// costume of a failure about the wiring.

	registry := observability.NewRegistry()

	// Constructed exactly as `runServer` constructs them, over this process's one
	// registry, with no campaign registered and nothing failed.
	newContentSignals(registry, discardLogger())

	// No account routes and none of the six campaign-scoped handlers. The
	// independently-runnable invariant from architecture §15 is that the server
	// starts and answers `/healthz` from phase 1 onward, and that has to hold for a
	// process with no content, no store and no account surface at all — so the
	// router is built with every one of them nil rather than with a fixture.
	handler := httpapi.NewRouter(
		discardLogger(), testConfig(), registry, nil, nil, nil, nil, nil, nil, nil, nil,
	)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, get(t, "/readyz"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /readyz = %d, want 200", recorder.Code)
	}

	var body struct {
		Counters map[string]observability.CounterValue `json:"counters"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /readyz: %v; body:\n%s", err, recorder.Body.String())
	}

	wanted := slices.Concat(pipelineCounterNames, []string{renderErrorCounter})

	for _, name := range wanted {
		value, present := body.Counters[name]
		if !present {
			t.Errorf(
				"/readyz has no %q; an unwired signal must be a visible zero, never an absent key (S-12.1)",
				name,
			)

			continue
		}

		if value.Total != 0 || value.Current != 0 {
			t.Errorf("%s = %+v, want zero: nothing has failed yet", name, value)
		}
	}
}

// indexedPage is the stored row for one root-relative path.
func indexedPage(pages []domain.Page, path string) (domain.Page, bool) {
	for i := range pages {
		if pages[i].Path == path {
			return pages[i], true
		}
	}

	return domain.Page{}, false
}

// pagePaths is every indexed path, for a failure message that has to name them.
func pagePaths(pages []domain.Page) []string {
	paths := make([]string, 0, len(pages))

	for i := range pages {
		paths = append(paths, pages[i].Path)
	}

	return paths
}

// serveWiki asks the router for one page and returns what it answered.
func serveWiki(
	t *testing.T,
	handler http.Handler,
	slug, page string,
) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, get(t, "/c/"+slug+"/wiki/"+page))

	return recorder
}

// get builds one GET request on the test's context.
//
// `http.NoBody` rather than nil, and `NewRequestWithContext` rather than
// `NewRequest`: a handler that reaches for the request's context — the wiki route
// does, for the campaign access and the request id — must see this test's context
// and not a fresh one the request invented.
func get(t *testing.T, target string) *http.Request {
	t.Helper()

	return httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
}

// testConfig is a configuration with only what the middleware chain reads: the
// handler budget, and no trusted proxies. Every other field is a zero no layer in
// `httpapi.NewRouter` looks at, and stating the whole struct would be a copy of
// `config.Load`'s defaults that drifts the moment those defaults do.
func testConfig() config.Config {
	// Positive, and not zero: `middleware.Timeout` builds a `context.WithTimeout`,
	// and a zero budget expires every request before the handler runs — which would
	// make these tests measure the timeout rather than the pipeline.
	return config.Config{HandlerTimeout: 5 * time.Second}
}

// discardLogger is a logger that throws its lines away.
//
// A test that asserts on a log line is asserting on a formatting decision, and the
// lines these components write are asserted on in the packages that own them. What
// the tests here need is for the logger to be *present*: `observability.NewWatch`
// and `NewIndex` both tolerate nil, and a nil logger would let a wiring bug hide
// behind that tolerance.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
