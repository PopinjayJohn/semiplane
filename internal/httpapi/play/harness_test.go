package play_test

// The harness: a mounted instance, a real hub, and a real socket.
//
// # Why every test here goes through a socket
//
// `hub.go`'s own header says the socket-side half of a peer's liveness cannot be
// observed without writing to a socket, and every property this route owns is on
// that side of the line:
//
//   - The S-8 matrix is a *transport* property. `RequirePlay` answers a status
//     code, and a status code is only real if a client that is refused an upgrade
//     observes the refusal rather than an open socket it cannot use.
//   - `Origin` is a header on the handshake. Asserting `originAllowed` directly
//     would pass against a route that never called it, which is the assertion
//     three gate drafts in phase 6 could not make.
//   - `Peer.Advance` is a counter inside the hub, observable only through whether
//     a sweep retires a peer. Its regression is a healthy table closing mid-game,
//     and nothing short of a socket can produce that.
//   - "Shutdown is clean" is a claim about goroutines and about a socket the
//     server actually closed.
//
// So: no `httptest.NewRecorder`, no fake `Conn`, no `httptest.NewServer` without
// a listener. A `httptest.NewServer` is a real `net.Listener` behind a real
// `http.Server`, which is what makes `http.Hijacker` — and therefore the upgrade —
// work at all.
//
// # The middleware chain is the one `router.go` builds, minus the timed layers
//
// `campaigns.Resolve` and the requestor injection are the two that shape what
// this route sees, and both are here. The timed layers are absent for a reason
// that is not convenience: `middleware.Timeout`'s writer follows `Unwrap` to the
// real writer, so a hijack *would* still work through it, and the route clears the
// write deadline it leaves behind. Including the layer would test `middleware`,
// not `play`.
//
// # The registry is over a real `*sql.Tx`, and not over `store.Open`
//
// `Registry.Open` writes through a `*sql.Tx` — `realtime.Writer` is a function
// that hands one to a callback — so a fake writer that passed `nil` would be
// asserting against a shape the product never runs. A bare `sql.Open("sqlite", …)`
// with migration 0002's own DDL is the smallest thing that produces a real
// transaction, and it sidesteps `store.Open`'s process-wide single-instance claim
// (ADR 0004) so these tests are not serialised against it.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	_ "modernc.org/sqlite"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/play"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

// The test constants. Every one of them is a bound rather than a sleep, and the
// distinction is the file's argument: a test that waits longer than it needs to is
// a test whose failure is indistinguishable from a slow machine, and a test that
// waits less than a bound needs is a test that passes on a fast one and fails on a
// loaded CI runner.
const (
	// settleBudget is how long any single wait in this file may take before it is
	// declared a failure rather than a wait. A ceiling on a failure, not a cost on
	// a success.
	settleBudget = 5 * time.Second

	// poll is how often a wait re-checks. Short enough that the budget is spent on
	// the thing being waited for, long enough that a hundred connections' worth of
	// tests is not a spin loop.
	poll = 2 * time.Millisecond

	// campaignID and the other ids are fixed rather than generated so a failure
	// message names a number a reader can look up in the fixture.
	// gmCampaignID is a public campaign with a GM and a player.
	gmCampaignID = int64(1)
	// privateCampaignID is a private campaign with one GM.
	privateCampaignID = int64(2)

	// gmUserID is the GM of both campaigns, playerUserID a member of the public one
	// and outsiderUserID an authenticated non-member of both.
	gmUserID       = int64(11)
	playerUserID   = int64(12)
	outsiderUserID = int64(13)

	// absence is how long `expectNoFrame` waits before concluding nothing arrived.
	//
	// Stated rather than hidden: "no frame reached this peer within 200ms" is a weaker
	// claim than "no frame reached this peer", and it is the strongest one available
	// without a protocol-level acknowledgement — which this route deliberately does not
	// have, because a `clock` a client must answer to prove liveness is a rule the client
	// can fail to honour and the server cannot enforce.
	absence = 200 * time.Millisecond

	// testReadTimeout is the read bound every socket test runs under.
	//
	// Short, and short on purpose: it is the one duration a test must be able to
	// make fire, and the only way it can be exercised is by a test that does not
	// wait a minute and a half. It is also what makes the shutdown assertions
	// bounded — a loop blocked in `Read` learns of a hub close at its next read
	// deadline, so this number is the shutdown bound as well as the liveness one.
	testReadTimeout = 250 * time.Millisecond

	// placement is the placement id every test's changes name. A valid token,
	// because `Encode` refuses a delta whose placement is not one — a test that
	// built an invalid frame would be refused by the codec and would look like a
	// route failure.
	placement = realtime.PlacementID("p1")
)

// The three slugs, and the two that do not resolve.
const (
	gmSlug      = "gilded-cage"
	privateSlug = "hollow-choir"
	absentSlug  = "no-such-campaign"
)

// The instance's chrome, as `cmd/server` supplies it to every route.
const (
	instanceName = "Greyhaven"
	testVersion  = "test"
	signOutPath  = "/logout"
	statusPath   = "/status"
)

// The test identity header. A header rather than a cookie because the route reads
// the identity from the context and never from the request, so the harness has to
// put it there — and a header is the least surprising way to say "this connection
// is this reader" in a fixture that is not testing authentication.
const userHeader = "X-Test-User"

// harness is one mounted instance: a store, a hub, a registry and a server.
type harness struct {
	t      *testing.T
	server *httptest.Server
	hub    *realtime.Hub
	store  *fixtureStore
	clock  *fakeClock
}

// mount is how a test asks for a differently-shaped instance.
//
// A struct rather than a fifth positional argument, because two of these fields are
// optional and four positional arguments of which two are nil is a call site that has
// to be read rather than scanned. Every field names one thing a test might need to
// change, and none of them can change the *gate*: the access resolution is applied by
// `newMounted` and a test cannot reach past it, because a test that mounted its own
// route would stop testing the one the product mounts.
type mount struct {
	// handler configures the route's bounds. Nil leaves the defaults.
	handler func(*play.Handler)
	// server configures the `http.Server` **before** it starts.
	//
	// Before it starts, and that is the whole reason this is a hook rather than a
	// field on `harness`: `http.Server.WriteTimeout` is read by the accept loop on
	// every connection, so writing it after `httptest.NewServer` has begun serving is
	// a data race on a live server — which the race detector reports as one, and
	// which is a race in the test rather than in the product.
	server func(*http.Server)
	// resolve is the hub's `Resolver`. Nil is a hub that cannot answer an intent,
	// which is a working read-only hub and the shape a composition root has before the
	// gameplay system is registered.
	resolve realtime.Resolver
	// stale is the hub's sweep window. Zero is `DefaultStaleAfter`, long enough that
	// no test can observe a retirement by accident.
	stale time.Duration
}

// newHarness mounts the route with the given resolver and the default sweep window.
//
// No sweep-window parameter: the only tests that move the window use `newMounted`, and
// a parameter every caller passes the same value for is a parameter nobody reads.
func newHarness(t *testing.T, resolve realtime.Resolver) *harness {
	t.Helper()

	return newMounted(t, mount{resolve: resolve})
}

// newUnwiredHarness mounts the route with **no** hub, which is a composition root
// that has not wired one yet.
//
// A separate constructor rather than a flag, because the state under test is
// "nothing is connected", and a flag would let a test that meant a live hub reach
// the same code by forgetting to set it.
func newUnwiredHarness(t *testing.T) *harness {
	t.Helper()

	backing := newFixtureStore()

	route := http.NewServeMux()
	play.Mount(route, &play.Handler{})

	mux := http.NewServeMux()
	mux.Handle("/c/{slug}/", resolveCampaign(backing, route))

	server := httptest.NewServer(withRequestor(mux))
	t.Cleanup(server.Close)

	return &harness{t: t, server: server, store: backing}
}

// newMounted is the constructor the rest delegate to.
func newMounted(t *testing.T, with mount) *harness {
	t.Helper()

	backing := newFixtureStore()
	registry := newTestRegistry(t)
	clock := newFakeClock()

	hub := realtime.NewHub(t.Context(), realtime.HubConfig{
		States:  registry,
		Resolve: with.resolve,
		Clock:   realtime.HubClock{Stale: with.stale, Now: clock.Now, After: clock.After},
	})

	handler := &play.Handler{
		Hub:         hub,
		ReadTimeout: testReadTimeout,
		// The document's chrome, wired the way `cmd/server` wires the wiki route's:
		// an instance name, a sign-out target and a status link. Every one of them
		// is optional on the handler, so a test that wants the fallbacks sets them
		// back to zero through `mount.handler` rather than mounting a second route.
		Instance:    components.InstanceView{Name: instanceName, Version: testVersion},
		SignOutHref: signOutPath,
		StatusHref:  statusPath,
		// The die sheet's notation and the token list's rows are the two document
		// fields with a source, and both are wired here so that the default document
		// is a *populated* one — a fixture where every section is empty can only
		// assert that emptiness renders, and the interesting assertions are about
		// what the sections do with content.
		Systems:   fixtureSystems(t),
		Snapshot:  fixtureSnapshot,
		Campaigns: backing,
	}
	if with.handler != nil {
		with.handler(handler)
	}

	// `Mount` registers the route on a mux of its own, exactly as the composition
	// root will, and the resolved campaign access is wrapped in front of that mux
	// rather than inside the handler — the ADR 0024 placement the route's own
	// `Mount` documents. A test that mounted the handler directly, with the gate
	// applied by hand inside `ServeHTTP`, would pass against a route whose `Mount`
	// had lost its gate, and the gate is the whole of this route's S-8 behaviour.
	route := http.NewServeMux()
	play.Mount(route, handler)

	mux := http.NewServeMux()
	// The requestor first, then the campaign resolution, then the route: the same
	// order `router.go` composes them in, because `Resolve` reads the requestor to
	// decide the tier and a reader installed after it would resolve every campaign
	// as TierNone.
	//
	// The **subtree** pattern rather than the two routes individually: S-9 splits
	// this package across `/play` and `/ws`, and a fixture that named only one of
	// them would serve the other from an unmatched path — which `net/http` answers
	// with a 404 that reads exactly like a gate refusing, and this file's whole
	// matrix is about telling those two apart.
	mux.Handle("/c/{slug}/", resolveCampaign(backing, route))

	server := httptest.NewUnstartedServer(withRequestor(mux))
	if with.server != nil {
		with.server(server.Config)
	}

	server.Start()

	// Hub before the store, and LIFO is the only order that works: the hub's `Close`
	// is the flush. The context is `WithoutCancel` because `t.Context()` is cancelled
	// *before* cleanups run, which is the requirement `Hub.Close` documents and
	// writing it here makes it a checked claim rather than a habit.
	t.Cleanup(func() {
		server.Close()

		if err := hub.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("hub.Close() error = %v, want nil", err)
		}
	})

	return &harness{t: t, server: server, hub: hub, store: backing, clock: clock}
}

// resolveCampaign puts the access resolution in front of a mounted route, on a mux
// of its own.
//
// The mux is load-bearing and this fixture learned that the hard way. `Resolve` reads
// `r.PathValue("slug")`, and a path value exists only **after** a `ServeMux` has
// matched the request — so a `Resolve` wrapped around the whole server rather than
// registered on a pattern reads an empty slug, treats the request as having no
// campaign, and every campaign answers 404. `router.go` registers it on a pattern and
// this fixture does the same, and the assertion in `TestThePlayRouteAnswersTheS8Matrix`
// that a *public* campaign answers 401 to an anonymous reader is what would have
// caught a composition that got it wrong: an empty slug answers 404, and the two are
// the difference between "this reader may read the wiki" and "this campaign exists".
func resolveCampaign(backing campaigns.Store, route http.Handler) http.Handler {
	return campaigns.Resolve(backing)(route)
}

// withRequestor installs the identity the `X-Test-User` header names.
//
// `identity.Authenticate` is not used because it is a session-cookie lookup and
// this file is not testing sessions — the route reads the identity from the
// context either way, and the *only* thing a session would add here is a store
// and a failure mode that has nothing to do with a WebSocket.
func withRequestor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get(userHeader)
		if raw == "" || raw == "0" {
			anonymous := identity.WithRequestor(r.Context(), identity.Anonymous())
			next.ServeHTTP(w, r.WithContext(anonymous))

			return
		}

		userID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writePlain(w, http.StatusBadRequest, "bad test user")

			return
		}

		next.ServeHTTP(w, r.WithContext(identity.WithRequestor(r.Context(), domain.Requestor{
			UserID:        userID,
			Username:      "tester",
			Authenticated: true,
		})))
	})
}

// dial opens a socket to slug with the given extra handshake headers.
//
// A real `websocket.Dial` and not an `http.Client` doing the handshake by hand: the
// route's refusals are status codes, and a client that mishandles a 101 cannot
// observe a refusal at all. `websocket.Dial` hands back the handshake response on
// failure, which is where the status code is — that is why this returns the
// response rather than only an error.
//
// The response body is drained and closed on the failure path, so a refused
// handshake leaves no connection in the test server's pool, which is what makes
// the "no leaked goroutine" assertion about the server as well as the route.
func (h *harness) dial(slug string, header http.Header) (*websocket.Conn, *http.Response, error) {
	h.t.Helper()

	endpoint := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/c/" + slug + "/ws"
	if header == nil {
		header = http.Header{}
	}

	// The identity travels as a header, so it has to survive into the handshake.
	if _, present := header[userHeader]; !present {
		header.Set(userHeader, "0")
	}

	conn, resp, err := websocket.Dial(
		context.Background(),
		endpoint,
		&websocket.DialOptions{HTTPHeader: header},
	)

	// Drained and closed on the failure path. A refused handshake is a 4xx with a body,
	// and a body left unread holds the connection out of the server's pool for as long
	// as the client lives — which, for a test that opens ten of them, is a test that
	// times out for a reason that has nothing to do with what it is testing.
	if err != nil && resp != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	if err != nil {
		return conn, resp, fmt.Errorf("dial %s: %w", slug, err)
	}

	return conn, resp, nil
}

// dialAs opens a socket as one user, with no `Origin` — the branch a non-browser
// client takes.
func (h *harness) dialAs(slug string, userID int64) *websocket.Conn {
	h.t.Helper()

	header := http.Header{}
	header.Set(userHeader, strconv.FormatInt(userID, 10))

	//nolint:bodyclose // `dial` drains and closes the body on its failure path.
	conn, resp, err := h.dial(slug, header)
	if err != nil {
		h.t.Fatalf("dial %s as user %d: %v (status %d)", slug, userID, err, statusOf(resp))
	}

	h.t.Cleanup(func() { _ = conn.CloseNow() })

	return conn
}

// dialStatus opens a socket expecting a refusal and returns the status code.
//
// It fails the test if the handshake **succeeds**, because a test that asked for a
// refusal and got a socket would otherwise go on to assert things about a
// connection that should not exist — which is the shape of a test that passes
// without the boundary it claims to be checking.
func (h *harness) dialStatus(slug string, header http.Header) int {
	h.t.Helper()

	//nolint:bodyclose // `dial` drains and closes the body on its failure path.
	conn, resp, err := h.dial(slug, header)
	if err == nil {
		_ = conn.CloseNow()
		h.t.Fatalf("the handshake to %s succeeded, but a refusal was required", slug)
	}

	status := statusOf(resp)
	if status == 0 {
		h.t.Fatalf("the handshake to %s failed with %v and carried no HTTP status", slug, err)
	}

	return status
}

// statusOf reads a refused handshake's status, or zero when there was no response.
func statusOf(resp *http.Response) int {
	if resp == nil {
		return 0
	}

	return resp.StatusCode
}

// readFrame reads one frame and decodes enough of it to name its type.
func readFrame(t *testing.T, conn *websocket.Conn) (string, map[string]any) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), settleBudget)
	defer cancel()

	_, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read a frame: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode %q: %v", payload, err)
	}

	kind, _ := decoded["t"].(string)

	return kind, decoded
}

// expectNoFrame concludes that nothing arrived within `absence`.
//
// Stated as a wait rather than a non-blocking read, because "no frame arrived" is a
// claim about the **absence** of an event and the only way to observe one is to wait
// past the point where it would have arrived. The window is named in the failure
// message so a reader does not take it for "the peer is silent forever".
//
// Used only where a timed read is safe — a client that has already received a frame and
// is waiting for a *second* one it should never get. A timed read on a socket that has
// not yet been read at all is a different test with a different failure, and the two
// are kept apart deliberately.
func expectNoFrame(t *testing.T, conn *websocket.Conn, what string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), absence)
	defer cancel()

	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatalf("a frame arrived, but %s required none", what)
	}
}

// waitFor polls until condition holds, and fails with what it was waiting for.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(settleBudget)

	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", settleBudget, what)
		}

		time.Sleep(poll)
	}
}

// fixtureStore is an in-memory `campaigns.Store`.
//
// Three campaigns and three memberships, which is the smallest set that can answer
// every row of the S-8 table this route is behind: a GM, a player, an
// authenticated non-member of a public campaign, an anonymous reader of the same
// one, an anonymous reader of a private one, and a slug that resolves to nothing.
//
// The two write methods are refused rather than implemented. A test that created a
// campaign would be testing phase 2, and a stub that quietly succeeded would let
// such a test pass for the wrong reason.
type fixtureStore struct {
	mu          sync.Mutex
	campaigns   map[string]domain.Campaign
	memberships map[int64]map[int64]domain.Membership
}

func newFixtureStore() *fixtureStore {
	backing := &fixtureStore{
		campaigns:   make(map[string]domain.Campaign),
		memberships: make(map[int64]map[int64]domain.Membership),
	}

	backing.campaigns[gmSlug] = domain.Campaign{
		ID: gmCampaignID, Slug: gmSlug, Name: "The Gilded Cage",
		Visibility: domain.VisibilityPublic,
		// A gameplay system, because a campaign without one has no table to
		// offer: `tableHref` is membership **and** `SystemID`, so a fixture with
		// no system would render a document whose navigation silently omits the
		// Table destination and every assertion about it would be vacuous.
		SystemID: string(dnd5e.SystemID),
	}
	backing.campaigns[privateSlug] = domain.Campaign{
		ID: privateCampaignID, Slug: privateSlug, Name: "The Hollow Choir",
		Visibility: domain.VisibilityPrivate,
		SystemID:   string(dnd5e.SystemID),
	}

	// `outsiderUserID` is in **neither** campaign. That is the row the S-8 matrix
	// cannot express without: an authenticated reader who is a member of nothing, so
	// the tier they resolve to is `TierReadOnly` on a public campaign and `TierNone`
	// on a private one. A fixture that made them a member of the public campaign
	// would make the 403 row unreachable and the 404 row accidental.
	backing.memberships[gmCampaignID] = map[int64]domain.Membership{
		gmUserID:     {CampaignID: gmCampaignID, UserID: gmUserID, Role: domain.RoleGM},
		playerUserID: {CampaignID: gmCampaignID, UserID: playerUserID, Role: domain.RolePlayer},
	}
	backing.memberships[privateCampaignID] = map[int64]domain.Membership{
		gmUserID: {CampaignID: privateCampaignID, UserID: gmUserID, Role: domain.RoleGM},
	}

	return backing
}

func (s *fixtureStore) CampaignBySlug(_ context.Context, slug string) (domain.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	campaign, found := s.campaigns[slug]
	if !found {
		return domain.Campaign{}, store.ErrNotFound
	}

	return campaign, nil
}

func (s *fixtureStore) Membership(
	_ context.Context,
	campaignID, userID int64,
) (domain.Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	membership, found := s.memberships[campaignID][userID]
	if !found {
		return domain.Membership{}, store.ErrNotFound
	}

	return membership, nil
}

func (s *fixtureStore) CreateCampaign(context.Context, domain.Campaign) (domain.Campaign, error) {
	return domain.Campaign{}, errFixtureUnimplemented
}

// CampaignsForUser is the navigation's Campaigns section: the reader's own
// campaigns, sorted by slug.
//
// Sorted because map iteration is not an order, and the section is rendered into
// a document a test compares. The same invariant rule code has, applied to a
// fixture because a fixture that answered in random order would make a
// byte-comparison flaky rather than wrong.
func (s *fixtureStore) CampaignsForUser(
	_ context.Context,
	userID int64,
) ([]domain.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	listed := make([]domain.Campaign, 0, len(s.campaigns))
	// rangeValCopy: `s.campaigns` is a `map[int64]domain.Campaign`, so a range
	// value cannot be addressed, and `listed` holds whole rows because the
	// caller sorts and renders them. Copying 128 bytes per campaign is the price
	// of that contract and there is no indexing form available to avoid it.
	//nolint:gocritic // see above: a map range cannot take an address
	for _, campaign := range s.campaigns {
		if _, member := s.memberships[campaign.ID][userID]; member {
			listed = append(listed, campaign)
		}
	}

	slices.SortFunc(listed, func(a, b domain.Campaign) int {
		return strings.Compare(a.Slug, b.Slug)
	})

	return listed, nil
}

func (s *fixtureStore) CreateMembership(
	context.Context,
	domain.Membership,
) (domain.Membership, error) {
	return domain.Membership{}, errFixtureUnimplemented
}

func (s *fixtureStore) DeleteCampaign(context.Context, int64) error {
	return errFixtureUnimplemented
}

// errFixtureUnimplemented is what the fixture's write methods return.
var errFixtureUnimplemented = store.ErrNotFound

// stubResolver answers every intent, and records what it was asked.
//
// Recording is the point of the stub rather than a convenience: S-7.3 says a roll
// is evaluated server-side and the client never supplies a result, and the only
// observable proof of that is a resolver that was handed the campaign and the
// actor from the **peer** and not from the frame.
type stubResolver struct {
	mu   sync.Mutex
	seen []realtime.Intent
	// refuse, when set, is returned instead of an answer — the §7.2 GM-only path.
	refuse error
}

func (r *stubResolver) Resolve(
	_ context.Context,
	intent realtime.Intent,
) (realtime.Resolution, error) {
	r.mu.Lock()
	r.seen = append(r.seen, intent)
	refusal := r.refuse
	r.mu.Unlock()

	if refusal != nil {
		return realtime.Resolution{}, refusal
	}

	return realtime.Resolution{
		Answer: &realtime.ServerApplied{
			Type:      realtime.TypeApplied,
			Seq:       intent.Frame.Seq,
			Version:   1,
			Placement: placement,
			Op:        intent.Frame.Op,
			By:        intent.Actor,
		},
		Broadcast: []realtime.Change{{
			Placement: placement,
			Version:   1,
			Op:        intent.Frame.Op,
			By:        intent.Actor,
		}},
	}, nil
}

// seenIntents returns how many intents the resolver was asked about.
func (r *stubResolver) seenIntents() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.seen)
}

// lastIntent returns the most recent intent, or false.
func (r *stubResolver) lastIntent() (realtime.Intent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.seen) == 0 {
		return realtime.Intent{}, false
	}

	return r.seen[len(r.seen)-1], true
}

// quietResolver answers an intent and broadcasts nothing.
//
// The counterpart to `stubResolver`, and the reason it is a separate type rather than
// a flag: `Resolution.Broadcast` being empty is a whole shape of the resolver contract
// ("an intent applied to nothing broadcasts nothing"), and a flag on the shared stub
// would make the interesting case the one a reader had to notice was set.
type quietResolver struct{}

func (r *quietResolver) Resolve(
	_ context.Context,
	intent realtime.Intent,
) (realtime.Resolution, error) {
	return realtime.Resolution{
		Answer: &realtime.ServerApplied{
			Type:      realtime.TypeApplied,
			Seq:       intent.Frame.Seq,
			Version:   1,
			Placement: placement,
			Op:        intent.Frame.Op,
			By:        intent.Actor,
		},
	}, nil
}

// blockingResolver enters `Resolve` and stays there until it is released.
//
// The shape a real resolver has when a system blocks — a dice engine waiting on
// something, a plugin in a syscall — and the only way a route's shutdown can be
// observed to *wait* for its reader rather than abandon it. A resolver that returns
// immediately leaves nothing to wait for.
type blockingResolver struct {
	// entered is closed the first time `Resolve` is called, so a test can be sure the
	// reader is inside the resolver rather than merely about to be.
	entered chan struct{}
	// release unblocks it.
	release chan struct{}
	// calls counts the entries, under `mu`.
	mu    sync.Mutex
	calls int
}

func newBlockingResolver() *blockingResolver {
	return &blockingResolver{entered: make(chan struct{}), release: make(chan struct{})}
}

func (r *blockingResolver) Resolve(
	ctx context.Context,
	intent realtime.Intent,
) (realtime.Resolution, error) {
	r.mu.Lock()
	r.calls++
	first := r.calls == 1
	r.mu.Unlock()

	if first {
		close(r.entered)
	}

	select {
	case <-r.release:
	case <-ctx.Done():
		return realtime.Resolution{}, fmt.Errorf("the resolver was cancelled: %w", ctx.Err())
	}

	return realtime.Resolution{
		Answer: &realtime.ServerApplied{
			Type:      realtime.TypeApplied,
			Seq:       intent.Frame.Seq,
			Version:   1,
			Placement: placement,
			Op:        intent.Frame.Op,
			By:        intent.Actor,
		},
	}, nil
}

// outstanding reports how many calls are in flight.
func (r *blockingResolver) outstanding(t *testing.T) bool {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.calls > 0
}

// newTestRegistry returns a registry over a real SQLite transaction.
//
// The DDL is migration 0002's own, because the statement that writes is
// `state.go`'s and it names these four columns and this primary key. A fixture
// table that differed would fail in a way that looks like a persistence bug.
func newTestRegistry(t *testing.T) *realtime.Registry {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/state.db")
	if err != nil {
		t.Fatalf("open the state database: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close the state database: %v", err)
		}
	})

	const ddl = `CREATE TABLE campaign_state (
		campaign_id INTEGER PRIMARY KEY,
		state       BLOB    NOT NULL,
		version     INTEGER NOT NULL DEFAULT 0,
		updated_at  INTEGER NOT NULL
	)`

	if _, err := db.ExecContext(t.Context(), ddl); err != nil {
		t.Fatalf("create campaign_state: %v", err)
	}

	return realtime.NewRegistry(t.Context(), realtime.Config{
		Write: func(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("begin the state transaction: %w", err)
			}

			if err := fn(ctx, tx); err != nil {
				_ = tx.Rollback()

				return err
			}

			return tx.Commit()
		},
		Read: func(ctx context.Context, campaignID int64) (realtime.Persisted, error) {
			row := db.QueryRowContext(ctx,
				`SELECT state, version, updated_at FROM campaign_state WHERE campaign_id = ?`,
				campaignID,
			)

			var (
				blob      []byte
				version   int64
				updatedAt int64
			)

			if err := row.Scan(&blob, &version, &updatedAt); err != nil {
				return realtime.Persisted{}, realtime.ErrNoState
			}

			return realtime.Persisted{
				Blob:      blob,
				Version:   version,
				UpdatedAt: time.Unix(updatedAt, 0).UTC(),
			}, nil
		},
	})
}

// fixtureSystems answers a campaign's gameplay system with the real 5e engine.
//
// The real engine rather than a stub `rules.System`: that interface has nine
// methods, and a stub returning a hand-written grammar would assert only that
// this route copies four fields across — which it does either way. The join is
// what could be wrong (asking about the reader instead of the campaign, or
// returning the answer for the wrong id), and only a system that is actually
// constructed answers that.
func fixtureSystems(t *testing.T) play.Systems {
	t.Helper()

	engine, err := dnd5e.New(dnd5e.Options{})
	if err != nil {
		t.Fatalf("build the 5e engine: %v", err)
	}

	return func(context.Context, int64) (rules.System, error) {
		return engine, nil
	}
}

// The two placements every document fixture carries, and the difference between
// them is the whole of the visibility rule: one a player may see and one they
// may not.
const (
	shownPlacementID  = realtime.PlacementID("p-shown")
	hiddenPlacementID = realtime.PlacementID("p-hidden")
)

// fixtureSnapshot is the live state the first server-rendered document reads.
//
// It is a function rather than a value because the handler's seam takes the
// campaign id, and a fixture that ignored the id would pass against a route that
// asked about the *reader* — the same join `fixtureSystems` exists to check.
func fixtureSnapshot(_ context.Context, campaignID int64) (realtime.Document, bool) {
	if campaignID <= 0 {
		return realtime.Document{}, false
	}

	return realtime.Document{
		Revision: 7,
		Placements: []realtime.Placement{
			{
				ID: shownPlacementID, X: 1, Y: 2,
				HP: 7, MaxHP: 7, Visible: true, Version: 7,
			},
			{
				ID: hiddenPlacementID, X: 3, Y: 4,
				HP: 1, MaxHP: 4, Visible: false, Version: 7,
			},
		},
	}, true
}

// documentResponse is one served document, read whole.
//
// The headers come with the body because three of this route's claims are about
// headers — the content type, the cache policy and `Vary` — and a helper that
// returned only the body would make each of them a second request.
type documentResponse struct {
	status int
	header http.Header
	body   string
}

// fetch performs a plain `GET` against an absolute path on the test server, as
// one user — 0 meaning anonymous — and reads status, headers and body whole.
//
// The general form of `get`, because S-9's split makes two paths claim two
// different answers (`/play` is a document, `/ws` is an upgrade) and a reader
// written twice would be a second answer to "what did the server send".
func (h *harness) fetch(path string, user int64) documentResponse {
	h.t.Helper()

	request, err := http.NewRequestWithContext(
		h.t.Context(),
		http.MethodGet,
		h.server.URL+path,
		http.NoBody,
	)
	if err != nil {
		h.t.Fatalf("build the request for %s: %v", path, err)
	}

	if user != 0 {
		request.Header.Set(userHeader, strconv.FormatInt(user, 10))
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatalf("read the body of %s: %v", path, err)
	}

	return documentResponse{
		status: response.StatusCode,
		header: response.Header,
		body:   string(body),
	}
}

// get fetches `/c/{slug}/play` as one user, 0 meaning anonymous.
func (h *harness) get(slug string, user int64) documentResponse {
	h.t.Helper()

	return h.fetch("/c/"+slug+"/play", user)
}

// writePlain answers a request with a short text body, for the harness's own
// refusals. It is not the route's `refuse` — that is unexported and untested here,
// because a fixture that called the code under test to set up a case for the code
// under test is a test that asserts nothing.
func writePlain(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message))
}

// fakeClock is the hub's injected sweep clock, driven by hand.
//
// The reason it exists is `HubClock`'s own: "this peer has been behind for longer
// than the window" is a claim about the **absence** of an event, and asserting
// absence in real time means sleeping past the window — which makes a test slow
// and its assertion weaker at the same time. With a clock the window is a number
// the test chooses and a retirement is an event it can cause.
//
// It supplies `Now` and `After` and nothing else. The sweeper's loop is the code
// under test and reimplementing it here would make the test agree with whatever
// defect the sweeper was written from.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*fakeWaiter
	// fired counts every timer this clock has delivered on, so a test can tell "the
	// sweeper woke and swept" from "the clock moved and nothing was due". Without it
	// an assertion that a peer *survived* a sweep is worth nothing: a sweep that
	// never ran also leaves the peer alive.
	fired int
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, time.September, 30, 14, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

// After returns a channel `advance` delivers on once the clock has moved d.
//
// A deadline already in the past delivers immediately, exactly as `time.After`
// does for a non-positive duration. That is what makes the clock safe against a
// sweeper that arms late: the deadline has passed, the timer is due, and the sweep
// runs rather than waiting for a test that has already moved on. The delivery is
// buffered with one slot and the waiter is not queued, so a second `advance` can
// never deliver it twice and hang on a channel nobody reads.
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	waiter := &fakeWaiter{at: c.now.Add(d), ch: make(chan time.Time, 1)}

	if !waiter.at.After(c.now) {
		waiter.ch <- c.now

		return waiter.ch
	}

	c.waiters = append(c.waiters, waiter)

	return waiter.ch
}

// advance moves the clock forward and delivers on every timer that came due.
//
// The move and the delivery happen in one critical section so a sweeper that
// reads `Now` when the timer fires sees the time the timer was armed for. Doing
// them in two steps would let the sweeper observe an earlier `now` than the one
// that fired it, and the "did the window move" assertion would be testing the
// test.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)

	var due []*fakeWaiter

	remaining := c.waiters[:0]

	for _, waiter := range c.waiters {
		if waiter.at.After(c.now) {
			remaining = append(remaining, waiter)

			continue
		}

		due = append(due, waiter)
	}

	for index := len(remaining); index < len(c.waiters); index++ {
		c.waiters[index] = nil
	}

	c.waiters = remaining
	c.fired += len(due)

	for _, waiter := range due {
		waiter.ch <- c.now
	}
}

// Fired returns how many timers have been delivered.
//
// The count and not "did my waiter go off", because a waiter that fired immediately
// because its deadline had already passed is counted too and would otherwise make
// the number depend on how late the sweeper armed.
func (c *fakeClock) Fired() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.fired
}

// waitArmed waits until the sweeper has asked for a timer, which is what makes
// "publish, then advance" safe rather than a race: without it, a test that
// advances the clock before the sweeper has reached its `select` delivers nothing
// and concludes the wrong thing. A gate that cannot fire is worse than no gate.
func (c *fakeClock) waitArmed(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(settleBudget)

	for {
		c.mu.Lock()
		armed := len(c.waiters) > 0
		c.mu.Unlock()

		if armed {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("the hub's sweeper armed no timer within %s, so no sweep can be "+
				"caused and every retirement assertion below is unreachable", settleBudget)
		}

		time.Sleep(poll)
	}
}

// mustJSON re-encodes a decoded frame so a test can assert on its bytes as a whole
// rather than field by field.
//
// The use is S-12.3: "the frame does not contain this string" is the claim, and a
// field-by-field walk would pass against a frame that echoed the value inside a
// field nobody thought to check.
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("re-encode a decoded frame: %v", err)
	}

	return encoded
}
