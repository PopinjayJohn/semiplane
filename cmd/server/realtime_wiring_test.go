// The realtime plane's wiring tests: that `/play` is mounted on the router the
// binary serves, that the S-8 matrix holds *through* that router, that a refusal
// carries no campaign state, that the shutdown is clean with a live socket, and
// that the ordering hazard in `realtime.go` cannot be reintroduced.
//
// `play`'s own tests already assert the S-8 matrix over real sockets, and this
// file does not repeat that work. What it adds is the one claim no package's test
// can make: that the router the **composition root builds** reaches the route at
// all. `play_test.go` mounts `play.Mount` on a mux of its own, so it would pass
// with the route absent from `httpapi.NewRouter` entirely — which is exactly the
// state this work item found the tree in.

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/store"
)

// The wait bounds for this file. Both are bounds on a *failure*, never costs on a
// success: the waits below poll for an event that has either happened or has not,
// and a test that slept for a fixed duration would be a test whose failure is
// indistinguishable from a slow machine.
const (
	// playSettle is how long any single wait here may take before it is declared a
	// failure rather than a wait.
	playSettle = 5 * time.Second

	// playPoll is how often those waits re-check. Short enough that the budget is
	// spent on the thing being waited for, long enough not to be a spin loop.
	playPoll = 2 * time.Millisecond
)

// TestThePlayRouteIsMountedOnTheRouterTheProductServes is the wiring assertion this
// work item exists to make, and the smallest form of it.
//
// One request, one status. `GET /c/{slug}/play` from an anonymous reader of a
// public campaign is a **401**, and the number is the whole claim:
//
//   - **404** would mean the route is not on the router — which is the bug. The
//     campaign subtree is mounted and gated, and no handler inside it matched, so
//     the subtree's not-found handler answered.
//   - **101** would mean the route answered without a gate, which is worse: a
//     tabletop anybody who can reach the port can sit at.
//   - **403** would mean the outer `RequireRead` resolved and the inner
//     `RequirePlay` did not run.
//
// So one status distinguishes all three failure shapes and only one of them is
// success. A test that asserted the route *exists* by looking for a pattern would
// assert nothing; a test that asserted it by opening a socket would be the same
// test as the matrix below, and would pass with the gate applied by hand somewhere
// other than the mount.
func TestThePlayRouteIsMountedOnTheRouterTheProductServes(t *testing.T) {
	inst := newInstance(t)
	handler := inst.serve(inst.registered)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, inst.get("/c/"+fixtureSlug+"/play", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("GET /c/%s/play as an anonymous reader = %d, want 401. "+
			"404 means /play is not mounted on the router the binary serves; "+
			"101 means it is mounted without RequirePlay; 403 means the inner gate "+
			"did not run under the outer one", fixtureSlug, recorder.Code)
	}
}

// TestThePlayMatrixThroughTheRouter answers S-8 for the router the product serves.
//
// Five rows and two columns, and the second column is what makes this a security
// test rather than a status-code test:
//
//   - **101 for a GM and for a player.** Same socket, same tier: a route that
//     gated on the *role* rather than the *tier* would refuse the player and pass
//     every other row.
//   - **401 anonymous** on a public campaign. S-8.1: no amount of public
//     visibility makes a tabletop public.
//   - **403 authenticated non-member** of a public campaign. The row that
//     distinguishes `TierReadOnly` from `TierNone` — and the one a route missing
//     its inner gate would answer 101 to.
//   - **404 private campaign seen from outside**, and **404 for a slug that
//     resolves to nothing**. S-8: no access is 404, never 403, and the two 404s
//     must be indistinguishable.
//
// Every refusal additionally asserts **no campaign state**: zero peers in the hub
// and zero live states in the registry, and no `campaign_state` row on disk. "No
// access is 404" and "no access is an open socket that goes quiet" are both "no
// access" from the requestor's side, and only the first is the requirement — so
// the refusal rows assert the state rather than only the status.
//
// The registry is the product's own, reached through `inst.plane`, because a test
// that built its own would be asserting about a different process.
func TestThePlayMatrixThroughTheRouter(t *testing.T) {
	inst := newInstance(t)

	// A player, and a second account who belongs to nothing. Both real rows: the
	// membership is what `RequirePlay` reads, and a fabricated requestor would skip
	// the lookup that decides the tier — which is the entire claim of the 403 and
	// 404 rows.
	player := inst.createUser("player", "pw")
	outsider := inst.createUser("outsider", "pw")

	public := inst.campaign
	private := inst.registerPrivateCampaign("blackgate-private")

	inst.addMember(public.ID, player.ID, domain.RolePlayer)
	// **No** membership for the private campaign's GM here: the registrar seeded
	// one, and seeding it a second time is a uniqueness violation. The fixture that
	// needed a hand-added GM row would add it where the registrar had not.

	handler := inst.serve(inst.registered)

	cases := []struct {
		name   string
		slug   string
		user   *http.Cookie
		status int
	}{
		{
			name: "a GM reaches the table", slug: public.Slug,
			user: inst.session(), status: http.StatusSwitchingProtocols,
		},
		{
			name: "a player reaches the table", slug: public.Slug,
			user: inst.sessionFor(player.ID), status: http.StatusSwitchingProtocols,
		},
		{
			name: "an anonymous reader of a public campaign is challenged",
			slug: public.Slug, user: nil, status: http.StatusUnauthorized,
		},
		{
			name: "an authenticated non-member of a public campaign is refused",
			slug: public.Slug, user: inst.sessionFor(outsider.ID),
			status: http.StatusForbidden,
		},
		{
			name: "an anonymous reader of a private campaign is a 404",
			slug: private.Slug, user: nil, status: http.StatusNotFound,
		},
		{
			name: "an authenticated non-member of a private campaign is a 404",
			slug: private.Slug, user: inst.sessionFor(outsider.ID),
			status: http.StatusNotFound,
		},
		{
			name: "a slug that resolves to nothing is the same 404",
			slug: "no-such-campaign", user: inst.sessionFor(outsider.ID),
			status: http.StatusNotFound,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Sequential, and for two reasons rather than one. `store.Open` claims a
			// process-wide single-instance slot (ADR 0004), so a parallel fixture here
			// could not open a second store at all — which is why every test in this
			// package that builds an instance is sequential. And every row asserts on
			// the *shared* plane's peer and live-state counts, so two simultaneous
			// tables would make both numbers a race even if a second store were
			// possible. Stated here rather than left as an unexplained absence of
			// `t.Parallel`.
			endpoint := inst.listen(t, handler)

			if testCase.status != http.StatusSwitchingProtocols {
				// Before, not after: this plane has already admitted a GM and a
				// player in the rows above, so "a refusal carries no state" is not
				// observable as a count of zero here. It is observable as a count
				// that did not move. `TestARefusedPlayCarriesNoCampaignState` is
				// the test that asks the stronger question against a plane that has
				// never admitted anybody.
				before := inst.campaignState()

				status := inst.dialRefused(t, endpoint, testCase.slug, testCase.user)
				if status != testCase.status {
					t.Errorf("GET /c/%s/play = %d, want %d", testCase.slug, status, testCase.status)
				}

				inst.assertCampaignStateUnchanged(t, testCase.slug, before)

				return
			}

			//nolint:bodyclose // On a **successful** handshake the response body *is*
			// the socket: the library has taken the connection over and owns it from
			// here, and `CloseNow` below is what releases it. There is no HTTP body to
			// close and closing one would close the table.
			// `/ws` and not `/play`: architecture S-9 puts the tabletop's document
			// at `/c/{slug}/play` and the upgrade at `/c/{slug}/ws`, and phase 7
			// mounted the socket at `/play` only because there was no document to
			// serve there yet. Dialling `/play` now answers 200 with HTML, which is
			// exactly what the browser got before phase 9 fixed it -- so this dial
			// is the assertion that the split is real, and it fails loudly rather
			// than passing against a page.
			conn, _, err := websocket.Dial(t.Context(),
				"ws"+strings.TrimPrefix(endpoint, "http")+"/c/"+testCase.slug+"/ws",
				&websocket.DialOptions{HTTPHeader: handshakeHeader(testCase.user)},
			)
			if err != nil {
				t.Fatalf("dial the table as a member: %v", err)
			}

			t.Cleanup(func() { _ = conn.CloseNow() })

			// The response body on a **successful** handshake is the WebSocket
			// itself, already taken over by the library; `websocket.Dial` owns it and
			// `CloseNow` above is what releases it. There is nothing to close here,
			// and the `bodyclose` findings on the refusal paths are real: those
			// responses are ordinary HTTP with a body a caller must read, which
			// `dialRefused` does.

			// An open socket is only half the affirmative answer; the other half is
			// that it is a **live** socket, which a `101` followed by silence is not.
			// Waiting for the peer count rather than for a frame is what makes this
			// the wiring claim: the peer exists in the product's own hub.
			inst.waitFor(t, "the peer to join", func() bool {
				return inst.plane.hub.Stats().Peers == 1
			})

			if live := inst.plane.registry.Live(); live != 1 {
				t.Errorf("the registry holds %d live states after one join, want 1: "+
					"a table with no state behind it is a socket that goes quiet", live)
			}

			// And the campaign's state reached the table on disk, which is the half of
			// the wiring no status code can show: the `Writer` closure ran, through
			// `store.Store.Write`, on the product's writer queue.
			inst.assertStateRowExists(t, testCase.slug)
		})
	}
}

// TestARefusedPlayCarriesNoCampaignState is the negative half of the matrix, on its
// own, for one reason the table above cannot give it: **the assertion is about
// what is absent, so it has to run against a plane that has never had a
// successful join.** In the matrix the GM rows run first and leave a live state and
// a `campaign_state` row behind, which means "the refusal changed nothing" is not
// observable there — only "the refusal did not add anything", which a route that
// joined and then closed would also satisfy.
//
// So this asks the stronger question against a clean plane: after a refusal, is
// there a peer, is there a live state, and is there a row? All three are no, and
// all three would be yes-or-worse for a route that joined before it checked.
func TestARefusedPlayCarriesNoCampaignState(t *testing.T) {
	inst := newInstance(t)

	outsider := inst.createUser("outsider", "pw")

	handler := inst.serve(inst.registered)
	endpoint := inst.listen(t, handler)

	// Every row of the matrix that is a refusal, against one plane that has never
	// admitted anybody.
	refusals := []struct {
		slug string
		user *http.Cookie
		want int
	}{
		{slug: inst.campaign.Slug, user: nil, want: http.StatusUnauthorized},
		{slug: inst.campaign.Slug, user: inst.sessionFor(outsider.ID), want: http.StatusForbidden},
		{slug: "no-such-campaign", user: inst.sessionFor(outsider.ID), want: http.StatusNotFound},
	}

	for _, refusal := range refusals {
		if got := inst.dialRefused(t, endpoint, refusal.slug, refusal.user); got != refusal.want {
			t.Errorf("GET /c/%s/play = %d, want %d", refusal.slug, got, refusal.want)
		}

		inst.assertNoCampaignState(t, refusal.slug)
	}
}

// TestThePlaySocketOutlivesAGracefulShutdownWithTheStoreStillOpen is the shutdown
// claim for the plane, and it is the test that would have caught the ordering this
// work item had to get right.
//
// The claim has two halves and neither is observable alone:
//
//  1. **The drain finishes inside its budget with a socket open.** `play`'s read
//     loop does not return on the request context's cancellation — the route's own
//     header says so — and `http.Server.Shutdown` waits for every in-flight
//     request. So a `/play` socket open at shutdown is a drain that burns the whole
//     budget and then fails with `context deadline exceeded`, on **every**
//     shutdown, for a reason that reads as a server fault. `Hub.Close` ending each
//     peer is what releases it.
//
//  2. **The final state reached the store.** `Registry.Close` is the flush, and a
//     flush with `context.WithoutCancel` is the difference between a table that
//     survives a restart and a game that was lost on a clean stop. The row is
//     counted on the `*store.Store` handle the process would then close.
//
// The socket is real and the drain is a real `http.Server.Shutdown`, because
// "Shutdown waits for idle connections" is the property and a recorder cannot
// produce it. The server is given **no** write timeout, which makes it the worst
// case the drain has to survive: a timeout would end the loop on its own and the
// ordering would stop mattering.
//
// The budget is a second, separate one from the product's: the point is that the
// drain completes *promptly*, and a budget long enough to be generous about a
// slow machine would also be long enough to hide a drain that only finished
// because the budget ran out. So it is short, and the failure message says which
// half of the claim was lost.
func TestThePlaySocketOutlivesAGracefulShutdownWithTheStoreStillOpen(t *testing.T) {
	inst := newInstance(t)

	handler := inst.serve(inst.registered)

	// A real listener and a real `http.Server`, for the reason the event-hub
	// shutdown test states: the operation under test *is* `Shutdown`, and its
	// wait-for-idle semantics are the whole claim.
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: time.Second,
		// No `WriteTimeout` and no `IdleTimeout`, deliberately: the route clears the
		// write deadline the handler budget leaves behind (play's header says so), and
		// a table is a response with no end. A server configured with either would
		// end the loop for us and make this test pass with the ordering wrong.
	}

	serving := make(chan struct{})

	go func() {
		defer close(serving)

		if serveErr := server.Serve(listener); serveErr != nil &&
			!errors.Is(serveErr, http.ErrServerClosed) {
			t.Errorf("serve: %v", serveErr)
		}
	}()

	t.Cleanup(func() {
		_ = server.Close()
		<-serving
	})

	endpoint := "http://" + listener.Addr().String()

	// A GM sits at the table and says nothing, which is a game between turns and
	// exactly the connection a shutdown has to end rather than wait on.
	//
	// `//nolint:bodyclose` because on a **successful** handshake the response body
	// *is* the socket: the library took the connection over and owns it, and
	// `CloseNow` below is what releases it. There is no HTTP body to close, and
	// closing one would close the table.
	//
	//nolint:bodyclose // The body is the socket; see above.
	conn, _, err := websocket.Dial(t.Context(),
		"ws"+strings.TrimPrefix(endpoint, "http")+"/c/"+inst.campaign.Slug+"/ws",
		&websocket.DialOptions{HTTPHeader: handshakeHeader(inst.session())},
	)
	if err != nil {
		t.Fatalf("dial the table: %v", err)
	}

	t.Cleanup(func() { _ = conn.CloseNow() })

	inst.waitFor(t, "the peer to join", func() bool {
		return inst.plane.hub.Stats().Peers == 1
	})

	// The signal context, cancelled exactly as `signal.NotifyContext` cancels one,
	// so the shutdown below runs against a context that is *already* done — which is
	// what makes `context.WithoutCancel` load-bearing rather than stylistic.
	signalCtx, cancelSignal := context.WithCancel(t.Context())
	cancelSignal()

	drained := make(chan error, 1)

	go func() {
		// The composition root's own two steps, in its own order, and then the drain.
		// `serve` is called rather than `http.Server.Shutdown` directly so the test
		// exercises the function the binary uses, including its `beforeDrain` step —
		// which is where the hub close was placed.
		drained <- serve(signalCtx, server, discardLogger(), 5*time.Second, func() {
			closeRealtimePlane(signalCtx, inst.plane, discardLogger(), closeTimeout)
		})
	}()

	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("the server did not shut down cleanly with a table open: %v. "+
				"A `/play` socket does not return on the request context, so the hub "+
				"has to be closed BEFORE Shutdown or the drain waits for the whole "+
				"budget", err)
		}
	case <-time.After(playSettle):
		t.Fatalf("the drain did not complete within %s while a table was open. "+
			"The realtime hub must be closed in the beforeDrain step, before "+
			"http.Server.Shutdown, because the route's read loop ends on the hub's "+
			"close and on nothing else", playSettle)
	}

	// Half two: the state was flushed, and it went through the store's writer queue
	// as the `Writer` closure promises. `Hub.Close` closes the registry itself, and
	// the explicit second close in `closeRealtimePlane` is what makes the order
	// checkable rather than dependent on another package's internals — so either
	// flush reaching the row is a pass, and a row that never appeared is a failure
	// of the whole chain from the closure to the commit.
	inst.waitFor(t, "the campaign_state row to survive the shutdown", func() bool {
		rows, err := inst.countStateRows(inst.campaign.ID)
		return err == nil && rows == 1
	})

	// And the peers are gone, which is the property that makes the drain's
	// completion mean something: a hub that closed its registry and left its peers
	// connected would flush state nothing could write to any more.
	if peers := inst.plane.hub.Stats().Peers; peers != 0 {
		t.Errorf("the hub holds %d peers after closing, want 0", peers)
	}

	if live := inst.plane.registry.Live(); live != 0 {
		t.Errorf("the registry holds %d live states after closing, want 0", live)
	}
}

// TestStatesAreOnlyOpenedThroughTheGate is the ordering hazard, held.
//
// `ruleset.go` states it in one sentence: `Registry.Open` cannot read `campaigns`,
// it writes a fresh `campaign_state` row under the *new* fingerprint, and a
// composition root that opened first and checked afterwards has by then destroyed
// the evidence that there had ever been a difference to report. So the composition
// root must open a campaign's state through `Gate.Resume` and through nothing else.
//
// This asserts it on the **source**, not on behaviour, and deliberately: the
// behaviour — "a drifted campaign refuses to resume" — is `ruleset.go`'s own test
// and passes whether or not the composition root calls the gate at all, because
// `resumeCampaignStates` opens nothing. A behavioural test could not see a
// `registry.Open` added beside the gate. A source assertion can.
//
// # The receiver is the hazard, not the method name
//
// This is a scan of eleven `.Open(` calls in this package, of which three are
// state opens and the rest are `store.Open`, `campaignroots.Open` and
// `contentRoots.Get`. Matching the method name would need a suppression comment
// for each of the eight, and a suppression comment per false positive is a list
// that grows every time somebody calls `Open` on something new.
//
// `Registry` is instead the **only** type in the product that holds a
// `realtime.Writer`, and the only one whose `Open` calls `state.persist` on the
// initial load — which is the code that writes the fresh `campaign_state` row
// under the new fingerprint. So `registry.Open` *is* the hazard by identity rather
// than by coincidence, and the eight other `Open` calls are not false positives at
// all: they are simply not the same call.
//
// # Both directions, because a prohibition is not a gate
//
// Nothing opening directly is necessary and not sufficient. A composition root
// that opened nothing would pass that check and be completely broken — the boot
// pass would report drift and no campaign could ever be played. So the count of
// permitted opens is asserted non-zero, because "a scan that matches nothing" and
// "a package that does nothing" are the same observation.
func TestStatesAreOnlyOpenedThroughTheGate(t *testing.T) {
	t.Parallel()

	const packageDir = "."

	entries, err := os.ReadDir(packageDir)
	if err != nil {
		t.Fatalf("read the composition root: %v", err)
	}

	// The gate must be **consulted**, by one of the two methods `ruleset.go` offers,
	// and the count is over the gate specifically rather than over every state
	// operation this package performs.
	//
	// That specificity is not fussiness. The first version of this check counted
	// *permitted* calls, and the flush (`registry.Close`) was on the permitted list —
	// so deleting `NewGate` outright left a non-zero count and this test reported
	// green over a plane that could never have refused a drifted campaign. A check
	// that survives the removal of the thing it checks is not a check, and this is
	// the second time in this repository that a gate passed because it was counting
	// the wrong thing (`AGENTS.md`'s `A11Y_ROUTE_PKGS` story, at package scale).
	consults := []string{
		"gate.Inspect(",       // the read-only half: reports drift, opens nothing
		"gate.Resume(",        // the ordered entry point: checks, then opens
		"plane.gate.Inspect(", // reached through the plane rather than a local
	}

	// `registry.Open(` is the hazard, named as the **forbidden** call so a reader
	// sees the rule stated positively rather than inferred from what is missing.
	const forbidden = "registry.Open("

	var consulted int

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		source, err := os.ReadFile(filepath.Join(packageDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}

		for number, line := range strings.Split(string(source), "\n") {
			code := line
			if comment := strings.Index(code, "//"); comment >= 0 {
				// Comments are excluded because this file's own header **names** the
				// hazard it is guarding, and a scan that counted its own prose would
				// fail every time the documentation was written well.
				code = code[:comment]
			}

			if strings.Contains(code, forbidden) {
				t.Errorf("%s:%d calls %s directly: %s\n"+
					"  Registry.Open writes a fresh campaign_state row under the new "+
					"fingerprint, destroying the evidence the gate reports drift from. "+
					"Call gate.Resume(ctx, plane.registry, id) instead",
					name, number+1, forbidden, strings.TrimSpace(code))
			}

			for _, call := range consults {
				if strings.Contains(code, call) {
					consulted++
				}
			}
		}
	}

	if consulted == 0 {
		t.Error("the ruleset gate is never consulted anywhere in the composition " +
			"root, so no campaign's fingerprint is ever compared against the " +
			"registered ruleset. Either the gate is not wired, or this scan has " +
			"stopped recognising the calls it looks for — and both are failures it " +
			"exists to notice, because a scan matching nothing and a package " +
			"consulting nothing are the same observation")
	}
}

// The fixture helpers. Each is a method on `instance` rather than a free function
// because they all need the fixture's store, and a free function would take it as
// its first argument on every call.

// createUser adds an account with a known password.
//
// Through `store.CreateUser` rather than by writing the row: `identity.Authenticate`
// resolves a session cookie to a `users` row and then to the password hash, and a
// fixture that inserted the row directly would be asserting about a path the
// product does not take. The hash is not exercised here — the session is minted
// directly, as `instance.session` documents — but the row must be a real one.
func (i *instance) createUser(username, password string) domain.User {
	i.t.Helper()

	hash, err := auth.HashPassword(password)
	if err != nil {
		i.t.Fatalf("hash a password for %s: %v", username, err)
	}

	user, err := i.store.CreateUser(i.t.Context(), domain.User{
		Username:     username,
		PasswordHash: hash,
	})
	if err != nil {
		i.t.Fatalf("create the user %s: %v", username, err)
	}

	return user
}

// registerPrivateCampaign registers a campaign nobody but its GM can see.
//
// Through the registrar, like every other campaign in this fixture, so its content
// root exists and its `ruleset_version` column is written the way the product
// writes it. The visibility is the only difference, and it is the difference the
// 404 rows turn on: a private campaign seen from outside has to be
// indistinguishable from one that does not exist.
func (i *instance) registerPrivateCampaign(slug string) domain.Campaign {
	i.t.Helper()

	registrar := campaigns.NewRegistrar(i.store, filepath.Join(i.base, "vaults"), nil)

	campaign, err := registrar.Register(i.t.Context(), campaigns.RegisterRequest{
		Slug:           slug,
		Name:           slug,
		SystemID:       "5e-2024",
		RulesetVersion: "v1",
		Visibility:     domain.VisibilityPrivate,
		OwnerID:        i.owner.ID,
	})
	if err != nil {
		i.t.Fatalf("register the private campaign %s: %v", slug, err)
	}

	i.registered = append(i.registered, campaign)

	return campaign
}

// addMember seeds one membership row.
//
// Directly, because there is no HTTP surface that creates a membership and adding
// one would be another work item's route. The row is the thing `RequirePlay`
// reads, and it is a real row in the real table.
func (i *instance) addMember(campaignID, userID int64, role domain.Role) {
	i.t.Helper()

	if _, err := i.store.CreateMembership(i.t.Context(), domain.Membership{
		CampaignID: campaignID,
		UserID:     userID,
		Role:       role,
	}); err != nil {
		i.t.Fatalf("add %d to campaign %d as %s: %v", userID, campaignID, role, err)
	}
}

// sessionFor mints a session cookie for one account.
//
// `instance.session` mints one for the owner, and this is the same thing for an
// arbitrary id. A session row written directly rather than by posting to `/login`,
// for the reason `instance.session` gives: the fixture builds no account routes, so
// a sign-in round trip would be a dependency on a subsystem these tests are not
// about. What matters is that the session is **real** — `identity.Authenticate`
// resolves the cookie to a `users` row, `campaigns.Resolve` reads that identity to
// load a membership, and `RequirePlay` reads the role.
func (i *instance) sessionFor(userID int64) *http.Cookie {
	i.t.Helper()

	token, hash, err := auth.NewSessionToken()
	if err != nil {
		i.t.Fatalf("mint a session token: %v", err)
	}

	if _, err := i.store.CreateSession(i.t.Context(), store.AuthSession{
		TokenHash: hash,
		UserID:    userID,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		i.t.Fatalf("create the session for user %d: %v", userID, err)
	}

	return &http.Cookie{Name: auth.SessionCookieName, Value: token, Path: "/"}
}

// get builds a GET carrying an optional cookie.
func (i *instance) get(path string, cookie *http.Cookie) *http.Request {
	i.t.Helper()

	request := httptest.NewRequestWithContext(i.t.Context(), http.MethodGet, path, http.NoBody)
	if cookie != nil {
		request.AddCookie(cookie)
	}

	return request
}

// listen serves handler on a real listener and returns its base URL.
//
// A listener rather than `httptest.NewServer`, for the reason the shutdown test
// states: a WebSocket upgrade needs a real `net.Listener` behind a real
// `http.Server`, and `httptest.NewServer` would have that — but `httptest` also
// installs its own `Config` fields, and a test asserting the composition root's
// own drain needs to own the server. The one thing `httptest.NewServer` buys that
// this does not is automatic cleanup, which `i.t.Cleanup` provides.
func (i *instance) listen(t *testing.T, handler http.Handler) string {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}

	serving := make(chan struct{})

	go func() {
		defer close(serving)

		if serveErr := server.Serve(listener); serveErr != nil &&
			!errors.Is(serveErr, http.ErrServerClosed) {
			t.Errorf("serve: %v", serveErr)
		}
	}()

	t.Cleanup(func() {
		_ = server.Close()
		<-serving
	})

	return "http://" + listener.Addr().String()
}

// handshakeHeader builds the headers for a dial, carrying the session cookie.
//
// No `Origin`, deliberately: that is the branch `play.originAllowed` documents for
// non-browser clients, and setting one here would be asserting the same-origin rule
// as a side effect of a test about the access gate. `play`'s own tests cover both
// `Origin` branches.
func handshakeHeader(cookie *http.Cookie) http.Header {
	header := http.Header{}
	if cookie != nil {
		header.Add("Cookie", cookie.Name+"="+cookie.Value)
	}

	return header
}

// dialRefused opens a socket expecting a refusal and returns the status code.
//
// It fails the test if the handshake **succeeds**, because a test that asked for a
// refusal and got a socket would go on to assert things about a connection that
// should not exist — which is the shape of a test that passes without the boundary
// it claims to be checking. This is `play`'s own harness rule, restated because the
// consequence here is worse: the assertions that follow are about campaign state.
//
// The response body is drained and closed, so a refused handshake leaves no
// connection held out of the server's pool.
func (i *instance) dialRefused(
	t *testing.T,
	endpoint, slug string,
	cookie *http.Cookie,
) int {
	t.Helper()

	conn, resp, err := websocket.Dial(t.Context(),
		"ws"+strings.TrimPrefix(endpoint, "http")+"/c/"+slug+"/play",
		&websocket.DialOptions{HTTPHeader: handshakeHeader(cookie)},
	)
	if err == nil {
		_ = conn.CloseNow()

		t.Fatalf("the handshake to /c/%s/play succeeded, but a refusal was required",
			slug)
	}

	if resp == nil {
		t.Fatalf("the handshake to /c/%s/play failed with %v and carried no HTTP "+
			"status, so there is no refusal to assert on", slug, err)
	}

	// Drained and closed, and the drain is not ceremony: an unread body holds the
	// connection out of the server's pool for as long as the client lives, which for
	// a fixture that opens several refusals in a row is a test that stalls for a
	// reason unrelated to what it is checking.
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	return resp.StatusCode
}

// stateCounts is what "how much campaign state does this process hold" looks like at
// one instant.
//
// A value rather than three return values, because the three are read together and
// a test that read them at three different instants could be handed a peer count
// from before a join and a row count from after one — which is a fixture asserting
// on a state that never existed. That is the same reasoning `realtime.Stats` gives
// for being a struct.
type stateCounts struct {
	peers int
	live  int
	rows  int
}

// campaignState reads all three counts.
func (i *instance) campaignState() stateCounts {
	i.t.Helper()

	rows, err := i.countStateRows(i.campaign.ID)
	if err != nil {
		i.t.Fatalf("count the campaign_state rows: %v", err)
	}

	return stateCounts{
		peers: i.plane.hub.Stats().Peers,
		live:  i.plane.registry.Live(),
		rows:  rows,
	}
}

// assertNoCampaignState asserts that a refusal left the process holding nothing: no
// peer, no live state, and no row on disk.
//
// The absolute form, and it is only meaningful against a plane that has **never**
// admitted anybody — see `assertCampaignStateUnchanged` for the delta form, which is
// what the matrix uses.
//
// Three observations rather than one, and each catches something the others cannot:
//
//   - **`hub.Stats().Peers`** is the peer count, and it is the first place a route
//     that checked the gate *after* the upgrade would show up.
//   - **`registry.Live()`** is the number of campaigns with an open authority, and a
//     `Registry.Open` on a refused request is a state held for a campaign whose
//     peer is not there.
//   - **The row count on the store** is the durable half. `Registry.Open` writes its
//     initial row *immediately*, so a refusal that opened a state and then closed
//     the connection would still leave a `campaign_state` row behind — and a row
//     that says a game has been played is a claim nobody's `Live()` can refute.
func (i *instance) assertNoCampaignState(t *testing.T, slug string) {
	t.Helper()

	if state := i.campaignState(); state != (stateCounts{}) {
		t.Errorf("a refused /c/%s/play left %+v behind, want nothing: a refused "+
			"request must join nothing, open no state and write no row", slug, state)
	}
}

// assertCampaignStateUnchanged asserts that a refusal moved no count.
//
// The delta form, and the reason it is needed is that a refusal cannot *reduce*
// state — it can only add — so on a plane that already holds a table the assertion
// "nothing is there" is not available and "nothing was added" is the strongest
// claim the observation supports. That is a weaker claim than the absolute form and
// it is stated as weaker rather than dressed up as the same one: the row count
// cannot distinguish "a refusal wrote a row" from "a row was already there", which
// is precisely why `TestARefusedPlayCarriesNoCampaignState` exists and asks the
// question against a clean plane.
func (i *instance) assertCampaignStateUnchanged(t *testing.T, slug string, before stateCounts) {
	t.Helper()

	after := i.campaignState()

	// **Only the row count is compared here, and that is the whole point of this
	// helper.**
	//
	// `peers` and `live` are **global** to the hub and the registry, and the rows
	// above this one have already admitted a GM and a player on the same plane.
	// Closing those sockets makes the hub detach their peers, and the detach is
	// asynchronous — the read loop notices the closed connection, then the peer
	// leaves. On a fast machine that completes before the next row samples; on a
	// loaded runner it lands *inside* this row, and the count moves for reasons that
	// have nothing to do with the request under test.
	//
	// It failed on CI exactly that way: 18.25s for the test, and the refusal row at
	// 0.01s reporting a move it did not cause.
	//
	// The row is the campaign-scoped fact, and it is the one that matters: `Registry.Open`
	// writes its row immediately, so a refusal that opened a state leaves a row
	// saying the game was played, and no in-process count can refute that. The
	// **absolute** claim — a refusal leaves zero peers, zero live states, zero rows —
	// is `TestARefusedPlayCarriesNoCampaignState`, which runs against a plane that
	// has never admitted anybody and so is immune to this entirely.
	if after.rows != before.rows {
		t.Errorf("a refused /c/%s/play wrote a campaign_state row: %d before, %d after. "+
			"A refusal must open no state, and `Registry.Open` writes its row "+
			"immediately, so this is the trace of one that did", slug, before.rows,
			after.rows)
	}
}

// assertStateRowExists asserts that a campaign's state reached `campaign_state`.
//
// The half of the wiring no status code can show. `Registry.Open` writes the row
// through `realtime.Writer`, which is the closure over `store.Store.Write`, so a row
// appearing here is evidence that the whole chain works: the route joined, the hub
// found an authority, the registry's load found no row, and its first persist
// reached the store's **writer queue** rather than the raw handle.
func (i *instance) assertStateRowExists(t *testing.T, slug string) {
	t.Helper()

	i.waitFor(t, "the campaign_state row for "+slug, func() bool {
		rows, err := i.countStateRows(i.campaign.ID)
		return err == nil && rows == 1
	})
}

// countStateRows counts one campaign's `campaign_state` rows.
//
// A raw query through `DB()`, which is what that handle is documented for, and it
// is also what `realtimeReader` uses — so a count taken here and a read taken there
// are two reads of the same connection and cannot disagree about what is stored.
func (i *instance) countStateRows(campaignID int64) (int, error) {
	var count int

	row := i.store.DB().QueryRowContext(i.t.Context(),
		`SELECT COUNT(*) FROM campaign_state WHERE campaign_id = ?`, campaignID,
	)

	// Wrapped rather than returned bare: a bare `*sql.Row.Scan` error says "scan
	// failed" and nothing about which campaign's rows were being counted.
	if err := row.Scan(&count); err != nil {
		return 0, errors.New("scan the campaign_state row count: " + err.Error())
	}

	return count, nil
}

// waitFor polls until condition holds, and fails with what it was waiting for.
//
// The same rule `play`'s harness states, for the same reason: "the write
// arrived" is a claim about an event, and asserting it by sleeping a fixed
// duration makes a failure indistinguishable from a slow machine.
func (i *instance) waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(playSettle)

	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", playSettle, what)
		}

		time.Sleep(playPoll)
	}
}
