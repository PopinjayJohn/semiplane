package plugins_test

// The harness: one assembled plugin route over one campaign, plus the DOM helpers the
// assertions in this package share.
//
// # Why the chain is assembled here
//
// `internal/httpapi/router.go` is **not this work item's to edit**, and its
// `mountCampaignRoutes` does not list this route until the integrator adds it — so the
// chain is built here, in the same order and with the same mount `router.go` assembles,
// and out loud. That is the point: a change on either side shows up as a failing test
// rather than as a route that quietly lost its gate.
//
// The hub is **not the real one either**, and the reason is worth more than a database
// would be. What has to be proved here is not that `plugin.Resolver` resolves a roll —
// phase 8's own tests hold that, and re-proving it through this route would test the
// adapter — but that **this route's dispatch carries the identity the gate resolved** and
// renders the `Resolution` the hub returned. So the fake is a `realtime.Resolver` that
// records the `realtime.Intent` it was called with and answers from a fixture, and every
// assertion below reads what *this route passed into it*.
//
// # The store is a fixture, for the reason `search`'s harness gives
//
// The access matrix is `domain.ResolveAccess`'s and it is already asserted by its own
// tests. What has to be proved here is that the mount put the gate in front of the route,
// and a hand-written `campaignStore` cannot be changed by a schema migration.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/httpapi/plugins"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
	webplugins "github.com/semiplane/semiplane/internal/web/plugins"
	"github.com/semiplane/semiplane/internal/web/plugins/dice"
	"github.com/semiplane/semiplane/internal/web/plugins/linkpreview"
)

// The campaign these tests reach, and the identifiers its rows carry.
const (
	testSlug   = "greyhaven"
	testCampID = int64(1)
	gmUser     = int64(10)
	playerUser = int64(11)
)

// gmRequestor is the campaign's GM.
func gmRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: gmUser, Username: "mira"}
}

// playerRequestor is a member with the player role.
func playerRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: playerUser, Username: "tobin"}
}

// anonymousRequestor is a signed-out reader of a public campaign: TierReadOnly, which
// `RequirePlay` refuses — the case that proves the roller's gate is the play gate and not
// the read gate.
func anonymousRequestor() domain.Requestor {
	return domain.Requestor{}
}

// recordedIntent is what the fake resolver was handed.
//
// **Every field is recorded, including the frame**, because the two halves of this route's
// argument are separate claims and a test that recorded only one could not tell them
// apart: `Campaign`, `Actor` and `Role` prove the identity is server-derived, and
// `Frame` proves the plugin's send went through the wire's codec rather than around it.
type recordedIntent struct {
	// intent is the whole `realtime.Intent`, verbatim.
	intent realtime.Intent

	// decoded is `intent.Frame` re-decoded from its own marshalled bytes, which is what a
	// test reads to check the *wire* shape rather than the Go struct.
	decoded []byte
}

// recordingResolver is a `realtime.Resolver` that remembers what it was asked and answers
// from a fixture.
//
// Safe for concurrent use because the hub may resolve two intents for one campaign at
// once (S-7.2's per-placement versioning is what makes that possible), and a fake without
// a mutex would be a data race the race detector finds on a test that happens to run two
// subtests in parallel.
type recordingResolver struct {
	mu sync.Mutex

	// intents is every intent this resolver was asked about, in order.
	intents []recordedIntent

	// answer is the `ServerApplied` the next resolution returns. Nil means "no changes",
	// which is a legitimate answer for a resolution that changed nothing.
	answer *realtime.ServerApplied

	// changes is what the next resolution broadcasts.
	changes []realtime.Change

	// refuse, when set, is returned instead of a resolution.
	refuse error
}

// Resolve records the intent and answers from the fixture.
func (r *recordingResolver) Resolve(
	_ context.Context,
	intent realtime.Intent,
) (realtime.Resolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Re-marshalled so a test can assert on the *bytes* the codec would have seen. The
	// codec cannot fail here — every field is a string or an integer — and a test reading
	// `json.Marshal` output is reading exactly what `ReadFrame` would have handed `Decode`.
	encoded, err := realtime.Encode(&realtime.ServerApplied{
		Type:      realtime.TypeApplied,
		Seq:       intent.Frame.Seq,
		Placement: intent.Frame.Args.Placement,
		Op:        intent.Frame.Op,
	})
	if err != nil {
		// Unreachable, and a failure here would be a bug in this file's fixture rather than
		// in the route. Recorded as the marshal error so a test reports something.
		encoded = []byte(err.Error())
	}

	r.intents = append(r.intents, recordedIntent{intent: intent, decoded: encoded})

	if r.refuse != nil {
		return realtime.Resolution{}, r.refuse
	}

	return realtime.Resolution{Answer: r.answer, Broadcast: r.changes}, nil
}

// recorded returns every intent this resolver was asked about.
func (r *recordingResolver) recorded() []recordedIntent {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]recordedIntent(nil), r.intents...)
}

// last returns the single intent the resolver was asked about, and fails otherwise.
//
// The count is asserted rather than assumed: the one-call properties below are meaningless
// if the route dispatched twice, and a fake that returned "the last one" would hide that.
func (r *recordingResolver) last(t *testing.T) recordedIntent {
	t.Helper()

	intents := r.recorded()
	if len(intents) != 1 {
		t.Fatalf("the resolver was called %d times, want exactly 1; the route dispatched: %+v",
			len(intents), intents)
	}

	return intents[0]
}

// campaignStore answers the queries the access gate reads and refuses the three it does
// not need.
//
// Hand-written so a schema change cannot alter what the access matrix resolves to — which
// is half of what the gate assertions below are about.
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

// harness is one assembled plugin route over one campaign.
type harness struct {
	t *testing.T

	// resolver is the recording fake the hub dispatches through.
	resolver *recordingResolver

	// ui is the UI registry the route mounts and renders from. Built over a gameplay
	// registry holding the real 5e engine, because §10.6's dice roller is meaningless
	// against a system that resolves nothing and the declaration-time op check would then
	// refuse the plugin — which is correct behaviour and would make the whole route
	// untestable.
	ui *webplugins.Registry

	// preview is the unfurl helper. `nil` by default so the route's own wiring fault is
	// reachable, and set by the tests that need a fetch.
	preview *linkpreview.Unfurler

	// hub is the broker. Nil by default, which is the "no hub wired" case.
	hub *realtime.Hub

	// states is the hub's state registry, closed by `close`.
	states *realtime.Registry

	// systems reports the campaign's gameplay system, and is nil by default so
	// "a build that wired the UI tier without a gameplay registry" is the default state
	// rather than an extra builder call. `withSystems` installs the real engine.
	systems plugins.Systems

	// role is the membership's role for `playerUser`, and a field so a test can put a role on
	// the row that this build has no name for.
	//
	// **`domain.RolePlayer` by default** — a field rather than a builder parameter because only
	// one test needs the other value, and a parameter on `newHarness` would be noise for every
	// other test. An unrecognised role resolves to `TierNone` in `domain.ResolveAccess`, which is
	// the fail-closed direction: the reader is locked out and the bad value stays visible in the
	// database rather than being guessed at.
	role domain.Role

	// visibility is the campaign's, and a field because the gate assertions need both a
	// public campaign (so an anonymous reader reaches the route at all and the test
	// measures the route rather than the gate) and a private one (so the 404-oracle
	// assertions have something to hide).
	visibility domain.Visibility

	// logger receives this route's lines when set.
	logger *slog.Logger
}

// newHarness builds a route over a **public** campaign.
//
// Public by default for the reason `search`'s and `wiki`'s harnesses are: a gate that
// answers 404 for everything is indistinguishable from a route that is working, and a suite
// whose failures are invisible is worse than one that is slow.
func newHarness(t *testing.T) *harness {
	t.Helper()

	return &harness{
		t:          t,
		resolver:   &recordingResolver{},
		visibility: domain.VisibilityPublic,
	}
}

// systems builds a gameplay registry holding the real 5e engine.
//
// **The real engine and not a fixture**, because `webplugins.Registry.checkEmits` refuses a
// UI plugin declaring an op no registered system resolves — so a fixture system's `Resolves`
// returning `false` would have the registration refused, which is correct and would make
// every route test fail at construction for a reason that is not about the route.
func systems(t *testing.T) *plugin.Registry {
	t.Helper()

	engine, err := dnd5e.New(dnd5e.Options{})
	if err != nil {
		t.Fatalf("building the 5e engine: %v", err)
	}

	registry := plugin.New()

	// `plugin.PlacementCodec`, and the reason is `dnd5e`'s own package comment: the engine
	// does not implement `plugin.Codec` because `internal/domain/systems` imports nothing
	// from `internal/plugin`. Its placement *is* a placement, so the projection with no
	// opinion in it is the correct one.
	if err := registry.Register(
		plugin.Entry{System: engine, Codec: plugin.PlacementCodec{}},
	); err != nil {
		t.Fatalf("registering the 5e engine: %v", err)
	}

	return registry
}

// UI builds a UI registry with both reference plugins registered, and reports whether the
// registration succeeded.
//
// **`linkpreview` is registered too**, and that is the point of the whole harness: both
// §10.6 plugins in one registry, over one gameplay registry, with the dice roller's
// `Emits` checked against the real engine's `Resolves`. A build where the roller
// registered and the preview did not is a build where one half of the tier is exercised
// and the other is not, which is what the §10.6 gate is about.
func (h *harness) withPlugins() *harness {
	h.t.Helper()

	if h.ui != nil {
		return h
	}

	h.ui = webplugins.New(systems(h.t))

	// Indexed rather than ranged: a `Declaration` is 128 bytes, and copying one per
	// iteration is what `rangeValCopy` is about.
	declarations := []webplugins.Declaration{dice.Declaration(), linkpreview.Declaration()}

	for index := range declarations {
		if err := h.ui.Register(declarations[index]); err != nil {
			h.t.Fatalf("registering %q: %v", declarations[index].Name, err)
		}
	}

	return h
}

// withHub builds a hub over a state registry with no writer and points the route at it.
//
// **A registry with no database, and that is sound rather than a shortcut.** `Dispatch`
// resolves through `h.resolve` and announces through `Publish`, and *neither* touches the
// state registry: opening a campaign's state is `Join`'s job, and a UI plugin's dispatch is
// explicitly the case that has no `Peer` (ADR 0042's `Actor` doc says so). So the registry
// is required by `HubConfig` and unused by this path — and a test that opened a database
// here would be testing `realtime.Registry`, which has its own tests.
//
// The hub is **not** closed by `t.Cleanup`, and the reason is that `Hub.Close` flushes the
// state registry, which with no writer would report a failure a test would then have to
// explain. The one goroutine the hub starts is its sweeper, which exits when
// `t.Context()` is cancelled — and that is why the context is `t.Context()` and not a
// `context.Background()`.
func (h *harness) withHub() *harness {
	h.t.Helper()

	h.states = realtime.NewRegistry(h.t.Context(), realtime.Config{})
	h.hub = realtime.NewHub(h.t.Context(), realtime.HubConfig{
		States:  h.states,
		Resolve: h.resolver,
	})

	return h
}

// withPreview installs an unfurl helper over the caller's client.
//
// `NewOver` and not `New`, because a test needs an `httptest.Server` and that is on
// `127.0.0.1` — an address `New` correctly refuses. The cost is that the **address policy**
// is off, so this is the seam for a test about the route's wiring and its degradation; a
// test about the refusals themselves uses `withGuardedPreview`.
func (h *harness) withPreview(client *http.Client) *harness {
	h.preview = linkpreview.NewOver(client)

	return h
}

// withGuardedPreview installs the unfurler **production wires**, with the dial hook installed.
//
// For the one claim that must hold with no seam in the way: a URL naming this host must be
// refused before the request leaves the process. A test using `withPreview` cannot make that
// observation, because `NewOver` disables the address policy by design — the fetch it
// performs is the seam's whole price.
func (h *harness) withGuardedPreview() *harness {
	h.preview = linkpreview.New()

	return h
}

// `theNextSeq` is the sequence every fixture answers under. It is a **constant and not a
// parameter**, and the reason is that a frame's `seq` is what pairs a resolution with an
// intent: a fixture that could answer under any sequence would be a fixture that proves the
// route echoes whatever sequence it was handed, which is the opposite of the claim.
const theNextSeq realtime.ClientSeq = 7

// applied makes the next resolution answer with an `applied` frame under `theNextSeq`.
//
// **The version is the point.** It is set to a value no client could have guessed and no
// re-roll could reproduce, because the test that reads it is asserting that what the page
// shows is *the server's stamp* rather than a number derived from the expression.
func (h *harness) applied(version realtime.Version) *harness {
	h.resolver.answer = &realtime.ServerApplied{
		Type:      realtime.TypeApplied,
		Seq:       theNextSeq,
		Version:   version,
		Placement: "p1",
		Op:        realtime.Op(dice.OpRoll),
		By:        realtime.UserID(gmUser),
	}

	return h
}

// refusing makes the next resolution be refused for reason, as the production adapter
// refuses.
//
// **`plugin.Reject`, and that is the shape a real build produces.** ADR 0042's
// `plugin.Resolver` reports a refusal as a `*RejectionError` carrying a reason and no
// frame, because a UI plugin's dispatch has no client frame behind it. Using the real
// constructor rather than a hand-rolled error is what makes this fixture reach the same
// branch a production refusal reaches — and if `ReadRefusal` read only the frame shape,
// this test would fail, which is the point.
func (h *harness) refusing(reason realtime.RejectReason) *harness {
	h.resolver.refuse = plugin.Reject(reason, errors.New("plugins_test: the resolver refused"))

	return h
}

// refusingWith makes the resolution fail with an error that is **not** a refusal.
//
// The case `RefusalOutcome` must decline to read: a wiring fault is not a refusal, and a
// page that rendered "not your turn" for a resolver that crashed would be a lie about the
// campaign's rules.
func (h *harness) refusingWith(err error) *harness {
	h.resolver.refuse = err

	return h
}

// handler builds the route under test.
//
// **`Systems` answers the harness's campaign with the real 5e engine**, and that is the
// point of §10.4's "the table is data": a fixture spelling `1d20` would make
// "the widget renders the system's notation" pass whether or not the wiring put the
// system's grammar on the page. The negative case — a harness with no `Systems` at all — is
// `withNoSystem`, and the two between them are what makes the notation's absence a state
// rather than an untested corner.
func (h *harness) handler() *plugins.Handler {
	return &plugins.Handler{
		UI:      h.ui,
		Hub:     h.hub,
		Systems: h.systems,
		Preview: h.preview,
		Logger:  h.logger,
	}
}

// withNoSystem installs no `Systems` at all, which is a build that wired the UI tier without
// a gameplay registry — S-10.6's first row reached from the UI side.
//
// The engine is built anyway so a failure in `dnd5e.New` is still reported; it is simply not
// handed to the route.
func (h *harness) withNoSystem() *harness {
	h.systems = nil

	return h
}

// withBrokenSystem installs a `Systems` that always fails, which is the case `grammar`
// treats as a fault that is *not* this page's: the widget is still usable without a
// notation beside the expression.
func (h *harness) withBrokenSystem() *harness {
	h.systems = func(context.Context, int64) (rules.System, error) {
		return nil, errors.New("plugins_test: the system id resolves to nothing")
	}

	return h
}

// withSystems installs the real engine for every campaign.
func (h *harness) withSystems() *harness {
	engine, err := dnd5e.New(dnd5e.Options{})
	if err != nil {
		h.t.Fatalf("building the 5e engine: %v", err)
	}

	h.systems = func(context.Context, int64) (rules.System, error) { return engine, nil }

	return h
}

// serve builds the whole chain, in the order `router.go` assembles it.
//
// The outer pattern is `/c/{slug}/` and **not** `/c/`, and that is load-bearing: a
// `net/http` path value is set by the mux whose *pattern* matched, so a pattern of `/c/`
// names no wildcard, `campaigns.Resolve` sees an empty slug, and `RequireRead` answers 404
// for every request under it.
//
// Resolve outside the campaign mux outside `RequireRead`, because the guard beneath Resolve
// reads the tier Resolve decided on. **`RequireRead` is layered under whatever the route
// mounted** — `router.go`'s `mountCampaignRoutes` puts the campaign mux under
// `RequireRead`, and the route's own `RequirePlay` sits inside it. Both are here so the
// tests exercise the real arrangement rather than a convenient one.
func (h *harness) serve() http.Handler {
	campaignMux := http.NewServeMux()
	plugins.Mount(campaignMux, h.handler())

	outer := http.NewServeMux()
	outer.Handle("/c/{slug}/",
		campaigns.Resolve(h.backing())(
			middleware.Chain(campaignMux, campaigns.RequireRead),
		),
	)

	return middleware.RequestID(outer)
}

// backing is the gate's source of truth.
func (h *harness) backing() campaigns.Store {
	role := h.role
	if role == "" {
		role = domain.RolePlayer
	}

	return campaignStore{
		campaign: domain.Campaign{
			ID:         testCampID,
			Slug:       testSlug,
			Name:       "Greyhaven",
			Visibility: h.visibility,
		},
		members: map[int64]domain.Role{
			gmUser:     domain.RoleGM,
			playerUser: role,
		},
	}
}

// serveUngated builds the same chain with the access gates removed.
//
// The one mount this package must be able to build: a route reached without
// `campaigns.Resolve` has no campaign and no tier on its context, so `AccessFrom` reports
// `TierNone` — which is what makes "a route mounted without its gate refuses rather than
// dispatching" a testable claim rather than a comment.
func (h *harness) serveUngated() http.Handler {
	campaignMux := http.NewServeMux()
	plugins.Mount(campaignMux, h.handler())

	outer := http.NewServeMux()
	outer.Handle("/c/{slug}/", campaignMux)

	return middleware.RequestID(outer)
}

// get issues one GET as requestor.
func (h *harness) get(target string, requestor domain.Requestor) *httptest.ResponseRecorder {
	h.t.Helper()

	return h.request(http.MethodGet, target, requestor, nil)
}

// post issues one POST carrying form, as requestor.
func (h *harness) post(
	target string,
	form map[string]string,
	requestor domain.Requestor,
) *httptest.ResponseRecorder {
	h.t.Helper()

	encoded := strings.NewReader(urlValues(form))

	return h.requestAgainst(h.serve(), http.MethodPost, target, requestor,
		http.Header{"Content-Type": []string{"application/x-www-form-urlencoded"}},
		encoded)
}

// ungated issues one GET against the chain with the gates removed.
func (h *harness) ungated(target string) *httptest.ResponseRecorder {
	h.t.Helper()

	return h.requestAgainst(h.serveUngated(), http.MethodGet, target, gmRequestor(), nil, nil)
}

// request issues one request against a freshly built chain.
//
// A fresh chain per request rather than one shared handler, because a shared one would hold
// the *first* handler's dependencies and a test that changed the resolver's fixture between
// two requests would silently be testing the first.
func (h *harness) request(
	method, target string,
	requestor domain.Requestor,
	header http.Header,
) *httptest.ResponseRecorder {
	h.t.Helper()

	return h.requestAgainst(h.serve(), method, target, requestor, header, nil)
}

// requestAgainst issues one request against a chosen chain, with an optional body.
func (h *harness) requestAgainst(
	handler http.Handler,
	method, target string,
	requestor domain.Requestor,
	header http.Header,
	body *strings.Reader,
) *httptest.ResponseRecorder {
	h.t.Helper()

	reader := io.Reader(http.NoBody)
	if body != nil {
		reader = body
	}

	req := httptest.NewRequestWithContext(h.t.Context(), method, target, reader)
	req = req.WithContext(identity.WithRequestor(req.Context(), requestor))

	maps.Copy(req.Header, header)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	return recorder
}

// urlValues encodes a form the way a browser would.
//
// **`url.Values.Encode`, and the ordering note is not an accident**: it sorts by key, so a
// test that reads the recorded body sees a deterministic string rather than a map iteration
// order.
func urlValues(form map[string]string) string {
	values := make(url.Values, len(form))
	for name, value := range form {
		values.Set(name, value)
	}

	return values.Encode()
}

// --- The DOM helpers the audits in this package share -------------------------
//
// `golang.org/x/net/html` rather than string matching, for the reason AGENTS.md states as
// an invariant: substring matching is how "no world" passes while the word sits in an HTML
// comment, an `aria-label` or a `data-` attribute. Every §10.2 rule below runs against a
// parsed tree.

// retiredWords is UI §1.2's closed vocabulary. Neither entity exists, a leftover is a bug,
// and a grep for them is a test — which is this.
//
// Case-insensitive and a substring, because the failure being guarded against is a noun
// phrase inside a sentence ("your session has expired", "the world map"), and neither is
// spelled with a capital letter in isolation.
var retiredWords = []string{"world", "session"}

// renderedDocument is one audited response.
type renderedDocument struct {
	// where names it in a failure message.
	where string

	// status is the response status, carried because it is how a reader knows which state
	// they are looking at and because an audit that only ever sees a 200 is an audit of the
	// easy path.
	status int

	// body is the response bytes.
	body []byte
}

// auditFailer is the subset of `*testing.T` the rules use.
//
// An interface rather than `*testing.T` because the rules have to be *tested* — a rule that
// cannot be shown to reject a violation it claims to catch is a gate wired to nothing — and
// a test of a `*testing.T`-typed function is a test that fails the suite. `silentFailer`
// below satisfies this and records instead of reporting.
//
// Deliberately without `Fatalf`: the parser's own constructor aborts on a response that is
// not HTML, and that is correct behaviour for it to have.
type auditFailer interface {
	Helper()
	Errorf(format string, args ...any)
}

// docAudit is one parsed document and where it came from.
type docAudit struct {
	t      auditFailer
	where  string
	status int
	root   *html.Node
}

// parseDocument parses an audited response, aborting when it is not HTML.
func parseDocument(t *testing.T, doc renderedDocument) *docAudit {
	t.Helper()

	if !strings.Contains(strings.ToLower(string(doc.body)), "<html") {
		t.Fatalf("%s: the response is not an HTML document (status %d); §10.2's rules "+
			"are about a document and there is nothing to audit. Begins: %q",
			doc.where, doc.status, truncateBody(string(doc.body)))
	}

	root, err := html.Parse(strings.NewReader(string(doc.body)))
	if err != nil {
		t.Fatalf("%s: parse rendered document: %v", doc.where, err)
	}

	return &docAudit{t: t, where: doc.where, status: doc.status, root: root}
}

// parseFixture parses a minimal document written to trip one rule.
func parseFixture(t *testing.T, body string) *html.Node {
	t.Helper()

	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	return root
}

// elements visits every element in the document, in document order.
func (a *docAudit) elements(visit func(*html.Node)) {
	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			visit(node)
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(a.root)
}

// focusStops returns every element a keyboard can reach, in document order.
//
// The same seven the wiki route's audit walks, and §10.6's list: `a[href]`, `button`,
// `input`, `select`, `textarea`, `summary` and anything with a `tabindex`.
func (a *docAudit) focusStops() []*html.Node {
	stops := make([]*html.Node, 0)

	a.elements(func(node *html.Node) {
		if isFocusStop(node) {
			stops = append(stops, node)
		}
	})

	return stops
}

// isFocusStop reports whether an element is one a keyboard can reach.
//
// **`<input type="hidden">` is excluded, and that is the reason this is not a one-line tag
// check.** A hidden field is not a focus stop, and an audit that counted it would demand a
// `.target` class on an element no keyboard can reach — which is both a false finding and a
// rule a future form would have to work around. The roller's `seq` field is exactly such a
// field, so the exclusion is reachable rather than theoretical.
func isFocusStop(node *html.Node) bool {
	switch node.Data {
	case "a":
		return hasAttribute(node, "href")
	case "button", "select", "textarea", "summary":
		return true
	case "input":
		return !strings.EqualFold(attr(node, "type"), "hidden")
	default:
		return hasAttribute(node, "tabindex")
	}
}

// attr returns an element's attribute value, or the empty string.
func attr(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}

	return ""
}

// hasAttribute reports whether an element carries an attribute at all.
//
// **Presence, not a non-empty value** — and that distinction is load-bearing twice over
// here: a `tabindex=""` is present and unusable, and a `data-testid=""` is a hook that
// selects nothing. `assertEveryTestIDIsPresent` says so in its own words; this is where
// it is implemented.
func hasAttribute(node *html.Node, name string) bool {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return true
		}
	}

	return false
}

// classTokens splits an element's class attribute.
func classTokens(node *html.Node) []string {
	return strings.Fields(attr(node, "class"))
}

// hasClass reports whether an element carries a class token.
func hasClass(node *html.Node, token string) bool {
	return slices.Contains(classTokens(node), token)
}

// anyTag is the tag name `findAll` accepts to mean "every element".
//
// A constant rather than a magic string at each call site, because `findAll(root, "*")`
// reading as "the tag whose name is an asterisk" is exactly the ambiguity a constant
// removes.
const anyTag = "*"

// findAll returns every element with the given tag name, in document order.
//
// `anyTag` matches every element, and it is needed because `byID` has to search the whole
// document for an id and the alternative — a second walker — would be two walkers to keep
// in step.
func findAll(node *html.Node, tag string) []*html.Node {
	found := make([]*html.Node, 0)

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if current.Type == html.ElementNode && (tag == anyTag || current.Data == tag) {
			found = append(found, current)
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return found
}

// byID returns the element with this id, or nil.
func byID(root *html.Node, id string) *html.Node {
	for _, node := range findAll(root, anyTag) {
		if attr(node, "id") == id {
			return node
		}
	}

	return nil
}

// nodePath names an element's position for a failure message.
func nodePath(node *html.Node) string {
	if node == nil {
		return "(nil)"
	}

	var parts []string

	for current := node; current != nil && current.Type == html.ElementNode; current = current.Parent {
		if current.Data == "html" {
			break
		}

		parts = append([]string{describe(current)}, parts...)
	}

	if len(parts) == 0 {
		return node.Type.String()
	}

	return strings.Join(parts, " > ")
}

// describe names one element with its identifying attributes.
//
// A `strings.Builder` rather than `+=`: the concatenation is in a loop, and `perfsprint`
// is right that this is the shape where it shows.
func describe(node *html.Node) string {
	var described strings.Builder

	described.WriteString("<" + node.Data)

	for _, name := range []string{"id", "href", "for", "data-testid", "role", "aria-label"} {
		if value := attr(node, name); value != "" {
			described.WriteString(" " + name + "=" + strconv.Quote(value))
		}
	}

	described.WriteString(">")

	return described.String()
}

// roleOf returns an element's landmark role, explicit or implicit.
//
// The implicit mapping is small and is the one §10.2 needs: `main`, `nav`, `header` as a
// direct child of `body`, `footer` as a direct child of `body`, and `aside`. A `header`
// inside an article is the article's header, not the banner, and `header.templ`'s own
// comment about `role="banner"` on each landmark is why this file reads the explicit role
// first.
func roleOf(node *html.Node) string {
	if role := attr(node, "role"); role != "" {
		return role
	}

	switch node.Data {
	case "main":
		return "main"
	case "nav":
		return "navigation"
	case "aside":
		return "complementary"
	case "header":
		return "banner"
	case "footer":
		return "contentinfo"
	default:
		return ""
	}
}

// textOf returns an element's rendered text, comments excluded.
func textOf(node *html.Node) string {
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

	return strings.Join(strings.Fields(text.String()), " ")
}

// landmark is one region of a document.
type landmark struct {
	// role is its landmark role.
	role string

	// name is its accessible name, empty when it has none.
	name string

	// namedBy is how it was named, for a failure message.
	namedBy string

	// node is the element.
	node *html.Node
}

// landmarks returns every landmark in the document, in document order.
func (a *docAudit) landmarks() []landmark {
	found := make([]landmark, 0)

	a.elements(func(node *html.Node) {
		role := roleOf(node)
		if role == "" {
			return
		}

		found = append(found, landmark{
			role:    role,
			name:    a.accessibleName(node),
			namedBy: namingMechanism(node),
			node:    node,
		})
	})

	return found
}

// namingMechanism reports how an element is named, for a failure message.
func namingMechanism(node *html.Node) string {
	if attr(node, "aria-label") != "" {
		return "its aria-label"
	}

	if attr(node, "aria-labelledby") != "" {
		return "aria-labelledby"
	}

	if attr(node, "title") != "" {
		return "its title"
	}

	return "nothing"
}

// accessibleName returns an element's accessible name, or the empty string.
//
// **Three mechanisms and no more**: `aria-labelledby`, `aria-label`, `title`. Anything
// richer is a name the reader would get from a browser's own implementation and a DOM
// cannot check, so a rule built on it would be a rule this project cannot hold itself to.
func (a *docAudit) accessibleName(node *html.Node) string {
	if labelled := attr(node, "aria-labelledby"); labelled != "" {
		parts := make([]string, 0)

		for id := range strings.FieldsSeq(labelled) {
			if target := byID(a.root, id); target != nil {
				parts = append(parts, textOf(target))
			}
		}

		return strings.TrimSpace(strings.Join(parts, " "))
	}

	if label := attr(node, "aria-label"); label != "" {
		return strings.TrimSpace(label)
	}

	return strings.TrimSpace(attr(node, "title"))
}

// skipLink is one of a document's skip links.
type skipLink struct {
	// node is the anchor.
	node *html.Node

	// href is its `href`, verbatim.
	href string

	// text is its link text.
	text string
}

// skipLinks returns the document's skip links, in document order.
//
// Keyed on the class `ui.SkipLinkClass` names rather than on `href` shape, because the
// stylesheet's off-screen rule targets that class and a link without it is not a skip link
// however it is written.
func (a *docAudit) skipLinks() []skipLink {
	links := make([]skipLink, 0)

	for _, node := range findAll(a.root, "a") {
		if !hasClass(node, "skip-link") {
			continue
		}

		links = append(links, skipLink{
			node: node,
			href: attr(node, "href"),
			text: textOf(node),
		})
	}

	return links
}

// containsFocusStop reports whether an element has a focusable descendant.
func containsFocusStop(node *html.Node) bool {
	found := false

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if found {
			return
		}

		if current != node && current.Type == html.ElementNode && isFocusStop(current) {
			found = true

			return
		}

		for child := current.FirstChild; child != nil && !found; child = child.NextSibling {
			walk(child)
		}
	}

	for child := node.FirstChild; child != nil && !found; child = child.NextSibling {
		walk(child)
	}

	return found
}

// hasAncestor reports whether an element has an ancestor with this tag name.
func hasAncestor(node *html.Node, tag string) bool {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && ancestor.Data == tag {
			return true
		}
	}

	return false
}

// truncateBody shortens a body for a failure message.
func truncateBody(body string) string {
	const limit = 200

	if len(body) <= limit {
		return body
	}

	return body[:limit] + "…"
}

// rulesGrammar is the campaign system's notation, as the widget renders it.
//
// **The real 5e grammar and not a literal**, because the point of the fixture is that the
// notation on the page came from a gameplay module: a fixture spelling `1d20` here would
// make "the plugin renders the system's example" pass whether or not the wiring did it.
func rulesGrammar() rules.Grammar {
	engine, err := dnd5e.New(dnd5e.Options{})
	if err != nil {
		panic("plugins_test: the 5e engine does not build: " + err.Error())
	}

	return engine.Grammar()
}
