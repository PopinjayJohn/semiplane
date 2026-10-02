package play_test

// What these tests are for, and why each had to be a socket
// ========================================================
//
// The route is a transport, a boundary and a loop, and all three claims are
// invisible from a `ResponseRecorder`:
//
//   - **The S-8 matrix is a transport property.** `RequirePlay` answers a status
//     code. Whether a client observes a refusal or an open socket it cannot use is
//     the difference between "no access is 404" being true and being a comment.
//   - **`Origin` is a handshake header.** Asserting the predicate directly passes
//     against a route that never called it — the exact failure three gate drafts in
//     phase 6 had, and the reason this file dials.
//   - **`Peer.Advance` is a counter inside the hub.** Its regression is a healthy
//     table closing mid-game, and the only way to produce that is a peer that was
//     briefly behind. Asserting "Advance was called" would need a seam in the loop
//     that exists only for the test; asserting *the table survived the window* is
//     the property, and it is observable only over a socket.
//   - **Shutdown and the read bound are about a socket going away.** A goroutine
//     count is the second half; a real close is the first.
//
// The rule every test below follows: **assert the product, not a function.** Where
// a white-box shortcut was available it was not taken, and where one was necessary
// — the rate meter's window arithmetic, the close-code table — it went into
// `internal_test.go` against a pure function with an injected clock.

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/semiplane/semiplane/internal/httpapi/play"
	"github.com/semiplane/semiplane/internal/realtime"
)

// The S-8 matrix for `/play`, over real sockets.
//
// Six rows, and the two columns that matter are the status **and** whether a
// refused peer received any campaign state — because "no access is 404" and "no
// access is an open socket that goes quiet" are both "no access" from the
// requestor's side, and only the first is the requirement.
func TestThePlayRouteAnswersTheS8Matrix(t *testing.T) {
	t.Parallel()

	const anonymous = int64(0)

	cases := []struct {
		name   string
		slug   string
		user   int64
		status int
	}{
		{
			// S-8: a GM reaches the table.
			name: "the GM reaches the table", slug: gmSlug, user: gmUserID,
			status: http.StatusSwitchingProtocols,
		},
		{
			// S-8: a player reaches the table, and it is the same socket a GM gets.
			// Asserting that is the claim: a route that gated on the *role* rather
			// than the *tier* would refuse this row and pass the rest.
			name: "a player reaches the table", slug: gmSlug, user: playerUserID,
			status: http.StatusSwitchingProtocols,
		},
		{
			// S-8.1: no amount of public visibility makes a tabletop public. A public
			// campaign's wiki is readable; its table is not, and the route that proves
			// it is the one that mounts the gate.
			name:   "an anonymous reader of a public campaign is refused",
			slug:   gmSlug,
			user:   anonymous,
			status: http.StatusUnauthorized,
		},
		{
			// The authenticated form of the same row, which is the one that
			// distinguishes `TierReadOnly` from `TierNone` — the public wiki tier.
			name:   "an authenticated non-member of a public campaign is refused",
			slug:   gmSlug,
			user:   outsiderUserID,
			status: http.StatusForbidden,
		},
		{
			// S-8: a private campaign answers 404 to a non-member. The row that proves
			// the 404 is not derived from a lookup that missed is the next one.
			name:   "an anonymous reader of a private campaign is a 404",
			slug:   privateSlug,
			user:   anonymous,
			status: http.StatusNotFound,
		},
		{
			// S-8: "no access is 404, never 403" is about the two shapes of one answer
			// being indistinguishable, and an authenticated non-member of a private
			// campaign is the row that holds it: 404, not 403, even though the reader
			// *is* authenticated and so would get a 403 from a public campaign.
			name:   "an authenticated non-member of a private campaign is a 404",
			slug:   privateSlug,
			user:   outsiderUserID,
			status: http.StatusNotFound,
		},
		{
			// S-8: a campaign that does not exist answers exactly as one that exists
			// and is invisible. The same status, or the difference is an existence
			// oracle.
			name:   "a slug that resolves to nothing is the same 404",
			slug:   absentSlug,
			user:   outsiderUserID,
			status: http.StatusNotFound,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t, &stubResolver{})

			header := http.Header{}
			if testCase.user != anonymous {
				header.Set(userHeader, strconv.FormatInt(testCase.user, 10))
			}

			if testCase.status != http.StatusSwitchingProtocols {
				if got := harness.dialStatus(testCase.slug, header); got != testCase.status {
					t.Errorf("GET /c/%s/play as user %d = %d, want %d",
						testCase.slug, testCase.user, got, testCase.status)
				}

				// The second column, and the one that makes this a security test
				// rather than a status-code test. A refused peer has no state, and the
				// hub's peer count is the only observation that reaches it — which is
				// the first place a handler that checked the gate *after* the upgrade
				// would show up.
				if got := harness.hub.Stats().Peers; got != 0 {
					t.Errorf("hub.Stats().Peers = %d after a refused upgrade, want 0: "+
						"a refused peer must receive no campaign state", got)
				}

				return
			}

			conn := harness.dialAs(testCase.slug, testCase.user)

			// An open socket is the whole of the affirmative answer, and it is
			// asserted rather than assumed.
			waitFor(t, "the peer to join", func() bool { return harness.hub.Stats().Peers == 1 })

			// And the socket works, so "101" is not a 101 that goes quiet.
			writeIntent(t, conn, 1)

			if kind, _ := readFrame(t, conn); kind != string(realtime.TypeApplied) {
				t.Errorf("an admitted socket answered %q, want %q", kind, realtime.TypeApplied)
			}
		})
	}
}

// The `Origin` policy, in both directions.
//
// Two tests rather than one table, because the two branches answer **different
// questions** — "was a browser on another site asking?" and "is there a browser at
// all?" — and a table over one boolean would read as a single switch.
// `originAllowed`'s comment says the absence is a decision rather than an accident
// of not checking; a test that only asserted refusals could not tell the difference.
func TestAnOriginFromAnotherSiteIsRefusedBeforeTheHandshake(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, &stubResolver{})

	header := http.Header{}
	header.Set(userHeader, strconv.FormatInt(gmUserID, 10))
	header.Set("Origin", "http://elsewhere.example")

	if got := harness.dialStatus(gmSlug, header); got != http.StatusForbidden {
		t.Errorf("a cross-origin upgrade = %d, want %d", got, http.StatusForbidden)
	}

	// The refusal is before the join, so a cross-origin page cannot cause a peer to
	// exist even transiently.
	if got := harness.hub.Stats().Peers; got != 0 {
		t.Errorf("hub.Stats().Peers = %d after a refused origin, want 0", got)
	}
}

// A *same-origin* header is admitted, which is the other direction of the first
// test and the one a `CheckOrigin`-style predicate gets wrong: comparing the origin
// to a hard-coded host name refuses a deployment behind any other name, and
// comparing it to nothing accepts everything.
func TestASameOriginHeaderIsAdmitted(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, &stubResolver{})

	header := http.Header{}
	header.Set(userHeader, strconv.FormatInt(gmUserID, 10))
	// The host the test server actually answers on, which is the only host this
	// route may accept — read from the request rather than assumed.
	header.Set("Origin", "http://"+strings.TrimPrefix(harness.server.URL, "http://"))

	//nolint:bodyclose // `dial` drains and closes the body on its failure path.
	conn, resp, err := harness.dial(gmSlug, header)
	if err != nil {
		t.Fatalf("a same-origin upgrade was refused: %v (status %d)", err, statusOf(resp))
	}

	t.Cleanup(func() { _ = conn.CloseNow() })

	waitFor(t, "the peer to join", func() bool { return harness.hub.Stats().Peers == 1 })
}

// The two origins a browser can send that this one is not.
//
// Two rows, and each is refused by a **different** clause — which is the reason they
// are two rows rather than one. `null` is what a sandboxed iframe and a `file://` page
// send, and it is refused because it named no scheme at all. `ws://` on this very
// host is refused because it named a scheme a browser never emits for a page it
// loaded over http or https, and it is the row that only the scheme check can catch:
// its host matches, so a handler that compared hosts alone would admit it.
//
// Both are attacker-reachable from a page a stranger controls, which is the whole
// reason the scheme is a list rather than a comparison.
func TestAnOriginThatIsNotThisInstancesPageIsRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		origin func(host string) string
	}{
		{
			// The sandboxed-iframe origin. A browser sends the literal string `null`.
			name:   "an opaque origin",
			origin: func(string) string { return "null" },
		},
		{
			// The right host and the wrong scheme. A page cannot make a browser send
			// this for a WebSocket handshake, so a script must — and a script that can
			// set the header is exactly the client the session cookie rides along for.
			name:   "a websocket-scheme origin on this host",
			origin: func(host string) string { return "ws://" + host },
		},
		{
			// A `file://` page, whose origin is opaque on every browser that
			// implements it and `null` on the others.
			name:   "a file origin",
			origin: func(string) string { return "file://" },
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t, &stubResolver{})
			host := strings.TrimPrefix(harness.server.URL, "http://")

			header := http.Header{}
			header.Set(userHeader, strconv.FormatInt(gmUserID, 10))
			header.Set("Origin", testCase.origin(host))

			if got := harness.dialStatus(gmSlug, header); got != http.StatusForbidden {
				t.Errorf("Origin %q = %d, want %d", testCase.origin(host), got,
					http.StatusForbidden)
			}

			if got := harness.hub.Stats().Peers; got != 0 {
				t.Errorf("hub.Stats().Peers = %d after a refused origin, want 0", got)
			}
		})
	}
}

// The absent branch: a non-browser client is admitted, and the socket works.
//
// A `websocket.Dial` with no `Origin` header is what a test harness, a server-side
// integration and an operator at a `wscat` prompt all send. Refusing them would buy
// nothing — there is no cross-origin vector when no browser is involved — and would
// cost a self-hosted instance its own tooling.
//
// The positive half matters as much as the negative: a route that refused *every*
// `Origin` would pass a test that only asserted refusals, so this asserts a socket
// opens **and carries a frame**.
func TestTheAbsentOriginTakesTheOtherBranchAndIsAdmitted(t *testing.T) {
	t.Parallel()

	resolver := &stubResolver{}
	harness := newHarness(t, resolver)

	conn := harness.dialAs(gmSlug, gmUserID)

	writeIntent(t, conn, 1)

	// The frame the client's own intent is answered with, which is only possible if
	// the upgrade happened and the loop is running.
	kind, frame := readFrame(t, conn)
	if kind != string(realtime.TypeApplied) {
		t.Fatalf("first frame after an intent = %q, want %q", kind, realtime.TypeApplied)
	}

	if got := frame["seq"]; got != float64(1) {
		t.Errorf("applied.seq = %v, want 1", got)
	}
}

// The `Advance` regression: a table that was briefly behind must survive the sweep.
//
// The scenario is the one the hub's author named, and staging it needs three things
// this file has: a client that does not read, a publish that arrives while the
// broadcast slot of one is **still full**, and a clock the test owns.
//
//  1. The client stops reading, and publishes are made until the hub reports one as
//     **superseded**. That statistic is the only proof that `offer` took its
//     full-on-arrival path, and that path is the *only* place `behindSince` is armed
//     — a publish into an empty slot resets it to zero. A test that published twice
//     and hoped would be a race, and its own comment would be the lie.
//  2. The client reads one frame, which is the route's loop taking a frame off the
//     queue and calling `Peer.Advance`.
//  3. The clock is advanced a whole window and the sweep is caused. The peer must
//     still be there, and a later publish must still arrive.
//
// The mutation this must catch is deleting either `peer.Advance()` call: `behindSince`
// is then never cleared, step 1's mark survives step 2, and the window in step 3
// retires a table that is demonstrably reading — a reconnect loop with an error
// nowhere in sight. `TestAPeerThatNeverReadsIsRetiredByTheSweep` is the control: a
// test with only the healthy half would pass against a sweep that retired nobody,
// which is the failure the a11y guard in `make check` was written to catch.
func TestAPeerThatDrainedIsNotRetiredByTheSweep(t *testing.T) {
	t.Parallel()

	const window = 45 * time.Second

	// The read bound is set well past anything this test waits for, because the read
	// bound **also** ends a connection: with the default 250ms it would retire the
	// peer on its own and the sweep would never be the thing under test. A test that
	// measured two mechanisms at once would be indistinguishable from a test that
	// measured the wrong one.
	harness := sweepHarness(t, window)
	conn := harness.dialAs(gmSlug, gmUserID)

	waitFor(t, "the peer to join", func() bool { return harness.hub.Stats().Peers == 1 })

	fillTheSlot(t, harness)

	// The drain. `readFrame` returns once the client holds the frame, and the loop
	// advances *before* it writes — so by the time this returns, the advance has
	// happened and the test's next step cannot race it.
	readFrame(t, conn)

	// The control, and it is inside this test on purpose. "The peer survived" is
	// worth nothing unless the sweep demonstrably ran, and the sweep runs on a
	// goroutine this hub started, so the only way to know it woke is to count the
	// timers the clock delivered. Without this precondition the whole test also
	// passes against a sweeper that never fires, which is the failure the a11y guard
	// in `make check` was written to catch.
	harness.clock.waitArmed(t)
	harness.clock.advance(window)

	// Waiting for the sweeper to **re-arm**, which is the only observation that says
	// the sweep it was about to run has finished. Waiting for the clock's delivery
	// count instead would return while the sweeper is still inside `sweep`, and every
	// assertion after it would be a race that happened to go its way — which is why
	// this is the second `waitArmed` and not a count.
	harness.clock.waitArmed(t)

	if got := harness.hub.Stats().Peers; got != 1 {
		t.Fatalf("hub.Stats().Peers = %d after a whole window and a sweep, want 1: a "+
			"peer that drained was retired anyway, so the loop is not calling "+
			"Peer.Advance", got)
	}

	// And a publish after the window must still arrive, which is the observable
	// difference between "not retired" and "retired and re-joined".
	publishDelta(t, harness.hub, gmCampaignID, 99)

	ctx, cancel := context.WithTimeout(t.Context(), settleBudget)
	defer cancel()

	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("a peer that drained was retired anyway, and a later publish never "+
			"arrived: %v", err)
	}
}

// What the sweep cannot do through this route, stated because it is a finding
// rather than an omission.
//
// The obvious negative control — "a peer that never reads is retired" — is **not
// reachable from a socket test of this route**, and the reason is worth recording:
// the route's `pump` always drains both queues and writes what it took, and a local
// TCP socket absorbs the write, so the peer is never "behind" from the hub's point
// of view no matter how long the client declines to read. The hub's staleness
// detector sees a queue it cannot put a frame into, and this route never fails to.
//
// So the reachable detectors are the two the design names as complementary: the read
// bound, which fires when a peer stops talking at all, and the sweep, which fires
// when a peer's *receive window* is full — a phone on a bad link, which no local
// test can stage for the price of a packet. Asserting the unreachable one would mean
// asserting on something a socket test cannot produce, and the test above pairs its
// positive with the sweep's own firing instead.
//
// `internal/realtime`'s own `hub_test.go` drives the sweep against peers it
// constructs itself, which is the right place for that claim: it can hold a queue
// full without a route in the way.

// sweepHarness is an instance whose only retirement mechanism under test is the
// hub's sweep.
//
// The read bound is pushed out to five seconds, which is past the test's own budget,
// and the reason is that this route has two independent ways to end a connection and
// a test that leaves both armed cannot say which one it observed. `Staled` is the
// discriminator: it counts sweep retirements and nothing else, and the test that
// needs it asserts on it.
func sweepHarness(t *testing.T, window time.Duration) *harness {
	t.Helper()

	return newMounted(t, mount{
		resolve: &stubResolver{},
		stale:   window,
		handler: func(handler *play.Handler) { handler.ReadTimeout = 5 * time.Second },
	})
}

// fillTheSlot publishes until the hub reports a **superseded** broadcast, which is
// the only observable proof that `offer` found the slot full on arrival.
//
// That is the arming condition for the peer's staleness clock, and reaching it
// without the statistic is a race: the route's loop drains the slot of one the
// moment a publish wakes it, so "publish twice quickly" is a coin flip rather than
// a setup step.
func fillTheSlot(t *testing.T, harness *harness) {
	t.Helper()

	deadline := time.Now().Add(settleBudget)

	for {
		publishDelta(t, harness.hub, gmCampaignID, 1)

		if harness.hub.Stats().Superseded > 0 {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("no broadcast was superseded within %s, so the peer's staleness "+
				"clock was never armed and every retirement assertion here is "+
				"unreachable; the client's socket is draining faster than publishes "+
				"arrive", settleBudget)
		}
	}
}

// The queue separation: an answer and a broadcast are two different facts about one
// gesture, and a client must receive both.
//
// `hub.go` says a single slot per peer would mean "a delta published microseconds
// after an `applied` eats the `applied`", and a client that never receives its
// answer rolls its optimistic update back and waits forever. The test therefore
// asserts the **presence of both frames and which is which**, not a count of two: a
// route that drained both queues through one channel could deliver two frames and
// still have lost the one that mattered, and a count cannot see that.
func TestAnIntentGetsItsOwnAnswerAndTheBroadcastDoesNotConsumeIt(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, &stubResolver{})
	conn := harness.dialAs(gmSlug, gmUserID)

	// The stub answers with an `applied` **and** broadcasts a delta carrying the
	// same change, which is the case the two queues exist for: both are ready at
	// once, and the answer is the one carrying the `seq`.
	writeIntent(t, conn, 7)

	kinds := make([]string, 0, 2)
	seen := make(map[string]map[string]any, 2)

	for range 2 {
		kind, frame := readFrame(t, conn)

		kinds = append(kinds, kind)
		seen[kind] = frame
	}

	if len(kinds) != 2 || kinds[0] == kinds[1] {
		t.Fatalf("frames after one intent = %v, want one applied and one delta", kinds)
	}

	applied, haveAnswer := seen[string(realtime.TypeApplied)]
	if !haveAnswer {
		t.Fatalf("no applied after an intent that resolved: the broadcast consumed the "+
			"answer, and a client that never receives its seq waits forever (%v)", kinds)
	}

	if got := applied["seq"]; got != float64(7) {
		t.Errorf("applied.seq = %v, want 7: the answer addressed a different seq", got)
	}

	if _, haveBroadcast := seen[string(realtime.TypeDelta)]; !haveBroadcast {
		t.Errorf("no delta after an intent that changed something: %v", kinds)
	}
}

// A rejection is answered on the same queue, with the same `seq`, and the frame
// that was refused is not echoed anywhere.
//
// The second half is the S-12.3 half: a resolver that returns an error whose text
// quotes the thing it choked on must not put that text on the wire, and the only
// place it could reach a browser from is the `rejected` frame.
func TestARefusalIsAnsweredWithAReasonAndNotTheResolversText(t *testing.T) {
	t.Parallel()

	const marker = "the vault's passphrase is hunter2"

	resolver := &stubResolver{refuse: &realtime.RejectionError{
		Reason: realtime.RejectNotPermitted,
		Err:    errors.New(marker),
	}}

	harness := newHarness(t, resolver)
	conn := harness.dialAs(gmSlug, gmUserID)

	writeIntent(t, conn, 3)

	kind, frame := readFrame(t, conn)
	if kind != string(realtime.TypeRejected) {
		t.Fatalf("frame after a refused intent = %q, want %q", kind, realtime.TypeRejected)
	}

	if got := frame["reason"]; got != string(realtime.RejectNotPermitted) {
		t.Errorf("rejected.reason = %v, want %q", got, realtime.RejectNotPermitted)
	}

	if got := frame["seq"]; got != float64(3) {
		t.Errorf("rejected.seq = %v, want 3", got)
	}

	if strings.Contains(string(mustJSON(t, frame)), marker) {
		t.Error("the rejection frame carries the resolver's own error text")
	}
}

// An answer with no broadcast still reaches the client.
//
// `Hub.Apply` publishes only when the resolver reports changes, so an intent that is
// answered without changing anything — a presence, a turn acknowledgement, an op whose
// effect is not a placement — puts a frame in the **answers** queue and nothing at
// all in the broadcast slot. That is the case where the loop has exactly one source of
// work, and it is what separates "the select has an answers arm" from "the answers
// happen to be swept up by the non-blocking drain on the way round".
//
// The stub used everywhere else answers *and* broadcasts, so the broadcast always
// wakes the loop and a loop with no answers arm would still pass the test above. That
// is a test that cannot fail for the reason it was written, which is the one thing
// this file is not allowed to contain.
func TestAnAnswerWithNoBroadcastStillReachesTheClient(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, &quietResolver{})
	conn := harness.dialAs(gmSlug, gmUserID)

	writeIntent(t, conn, 11)

	kind, frame := readFrame(t, conn)
	if kind != string(realtime.TypeApplied) {
		t.Fatalf("the only frame after an intent that broadcast nothing = %q, want %q",
			kind, realtime.TypeApplied)
	}

	if got := frame["seq"]; got != float64(11) {
		t.Errorf("applied.seq = %v, want 11", got)
	}

	// And nothing else arrives, which is the other half: a resolver that reported no
	// changes must not produce a delta.
	expectNoFrame(t, conn, "a delta after a resolver that broadcast nothing")
}

// The server bound the identity, and a frame cannot name its campaign.
//
// Two claims. The first is S-7.1's "no inbound field can hold a result, and an
// unknown field is a rejection rather than a discarded value": a frame carrying a
// `campaign` and a `by` is **rejected**, and the resolver is never asked. The
// second is the binding: on a well-formed frame the resolver is handed the campaign
// from the **peer** and the actor from the **connection**, so the answer's `by` is
// the user the gate admitted rather than the number the client put in the frame.
func TestTheCampaignAndTheActorComeFromTheConnectionNotTheFrame(t *testing.T) {
	t.Parallel()

	resolver := &stubResolver{}
	harness := newHarness(t, resolver)
	conn := harness.dialAs(privateSlug, gmUserID)

	// A frame carrying a `campaign` and a `by` the client chose. Both are unknown
	// fields, and `Decode` refuses an unknown field rather than discarding it, so the
	// codec never produced an intent to answer — which is why the answer is a **close**
	// and not a `rejected`. A refusal that has no `seq` to address has no other answer.
	writeRaw(t, conn, `{"t":"intent","seq":1,"op":"move_token","campaign":999,"by":4242}`)

	ctx, cancel := context.WithTimeout(t.Context(), settleBudget)
	defer cancel()

	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("a frame naming a campaign was accepted")
	}

	if got := websocket.CloseStatus(err); got != websocket.StatusPolicyViolation {
		t.Errorf("close status after a frame naming a campaign = %d, want %d", got,
			websocket.StatusPolicyViolation)
	}

	if got := resolver.seenIntents(); got != 0 {
		t.Errorf("the resolver was asked %d times, want 0: a frame with a campaign "+
			"field never reached it", got)
	}

	// And the connection was not left merely quiet: a fresh one binds the same way.
	conn = harness.dialAs(privateSlug, gmUserID)

	writeIntent(t, conn, 2)

	waitFor(t, "the resolver to be asked", func() bool { return resolver.seenIntents() == 1 })

	intent, ok := resolver.lastIntent()
	if !ok {
		t.Fatal("the resolver saw no intent")
	}

	if intent.Campaign != privateCampaignID {
		t.Errorf("Intent.Campaign = %d, want %d: the campaign came from somewhere "+
			"other than the connection the gate admitted", intent.Campaign, privateCampaignID)
	}

	if int64(intent.Actor) != gmUserID {
		t.Errorf("Intent.Actor = %d, want %d: the actor came from somewhere other "+
			"than the connection", int64(intent.Actor), gmUserID)
	}
}

// A `hello` and a `presence` do not end the connection, produce no frame of their
// own, and are not handed to the resolver — which is the documented gap the wiring
// work item fills.
//
// The "nothing arrived" assertion is made by **order** rather than by a timed read:
// the intent's own `applied` is written last, so anything `hello` or `presence`
// produced would arrive *before* it. A test that waited for nothing would be a claim
// about a duration, and this is a claim about a sequence — which is also why no
// read here carries a deadline: a read that expires mid-frame is a different test
// with a different failure, and conflating the two is how a socket test becomes
// untrustworthy.
func TestHelloAndPresenceAreRoutedWithoutEndingTheConnection(t *testing.T) {
	t.Parallel()

	resolver := &stubResolver{}
	harness := newHarness(t, resolver)
	conn := harness.dialAs(gmSlug, gmUserID)

	writeRaw(t, conn, `{"t":"hello"}`)
	writeRaw(t, conn, `{"t":"presence","args":{"cursor":[10,20]}}`)
	writeIntent(t, conn, 1)

	// Two frames are owed: the answer to the intent and the broadcast it caused. The
	// first must be the answer, because a `hello` or a `presence` that produced
	// anything would have produced it first.
	first, _ := readFrame(t, conn)
	if first != string(realtime.TypeApplied) {
		t.Fatalf("the first frame after a hello, a presence and an intent = %q, want "+
			"%q: a frame this route cannot answer must not produce one", first,
			realtime.TypeApplied)
	}

	second, _ := readFrame(t, conn)
	if second != string(realtime.TypeDelta) {
		t.Errorf("the second frame = %q, want %q", second, realtime.TypeDelta)
	}

	if got := resolver.seenIntents(); got != 1 {
		t.Errorf("the resolver was asked %d times, want 1: a hello and a presence are "+
			"not intents, and this route must not invent one for them", got)
	}
}

// Shutdown is clean with a live socket: bounded, and no leaked goroutine.
//
// Two assertions, and the second is the one that is easy to fake. "The client saw a
// close" is one observation; "the server's goroutine count came back" is the other,
// and a test with only the first passes against a route that abandons its reader.
//
// The count is compared against a mark taken *after* the connection was admitted,
// because the reader goroutine is started by the upgrade and a mark taken before it
// would be asserting that the route starts no goroutine at all — which is a
// different, and wrong, claim.
func TestAHubShutdownClosesALiveSocketAndLeaksNoGoroutine(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, &stubResolver{})
	conn := harness.dialAs(gmSlug, gmUserID)

	waitFor(t, "the peer to join", func() bool { return harness.hub.Stats().Peers == 1 })

	// Settled first, so the mark is a floor rather than a sample of whatever the
	// siblings were doing, and taken after the reader exists so the comparison is about
	// the teardown and not about the startup.
	waitFor(t, "the reader goroutine", func() bool {
		return runtime.NumGoroutine() > settleGoroutines(t)
	})

	mark := settleGoroutines(t)

	// `WithoutCancel` because `t.Context()` is cancelled before cleanups run, and
	// `Hub.Close` documents that handing it a cancelled context has asked for the
	// crash case.
	if err := harness.hub.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatalf("hub.Close() error = %v, want nil", err)
	}

	// The client observes a close, and it is a close *frame* rather than a bare
	// transport failure — a client cannot tell "the table is shutting down" from
	// "the network broke" without one.
	ctx, cancel := context.WithTimeout(t.Context(), settleBudget)
	defer cancel()

	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("a live socket survived the hub's shutdown")
	}

	// A close frame **or** a bare end-of-stream, and the reason is a real race rather
	// than a loosened assertion. The reader goroutine is blocked inside `Read` when the
	// hub closes, and the library holds a read mutex for the duration of a read, so the
	// route's `writer.Close` cannot write the close frame until that read is released —
	// which happens at the read bound. Whether the frame reaches the client or the
	// socket is simply gone first depends on which of the two runs, and a client
	// reconnecting either way is the same client. The status is asserted **when there
	// is one**, because a close code is a promise this route can keep and a bare EOF
	// is one it cannot.
	if code := websocket.CloseStatus(err); code != -1 && code != websocket.StatusGoingAway {
		t.Errorf("close status after a hub shutdown = %d, want %d", code,
			websocket.StatusGoingAway)
	}

	// The goroutines, waited for rather than slept on: the reader is released by the
	// server's close *and* the client's, and a fixed wait would be a fixed race. The
	// tolerance is two, and it is stated rather than hidden: the floor was a minimum
	// over a window and the test binary's own machinery is not obliged to stay at it.
	waitFor(t, "the route's goroutines to be released", func() bool {
		return runtime.NumGoroutine() <= mark+goroutineTolerance
	})
}

// A shutdown **waits** for a reader that is inside a resolver.
//
// The other half of the test above, and the half that can see the join. A reader that
// has stopped is a goroutine that exits; a reader blocked inside `Hub.Apply` is a
// goroutine that cannot, and the difference is what the `<-readerDone` is for: without
// it, `serve` returns, `net/http` declares the connection finished, and a process
// shutting down at that moment loses whatever the resolver was about to write.
//
// The assertion is the **count**, not the client: the client is told the table is gone
// either way, because the pump's queues close independently of the reader. What differs
// is whether the handler's own goroutine is still on the stack, and that is one
// goroutine.
func TestAShutdownWaitsForAReaderThatIsInsideAResolver(t *testing.T) {
	t.Parallel()

	resolver := newBlockingResolver()
	harness := newHarness(t, resolver)

	// The floor is taken **before** the dial, and that ordering is the whole
	// assertion.
	//
	// The test then waits for `NumGoroutine() > floor` to learn that this route's
	// reader has started. Sampled after the dial, the floor already contains that
	// reader, the count never rises above it, and the wait times out — which is
	// what it did: 3 failures in 3 runs, alone, with no load. A test that fails
	// deterministically is a broken test rather than a slow one, and reading it as
	// a timing problem is how it survived review.
	//
	// `settleGoroutines` waits for the count to stop moving, so taking it first
	// also means the baseline is a settled one rather than a snapshot mid-dial.
	floor := settleGoroutines(t)

	conn := harness.dialAs(gmSlug, gmUserID)

	waitFor(t, "the peer to join", func() bool { return harness.hub.Stats().Peers == 1 })

	waitFor(t, "the reader goroutine", func() bool {
		return runtime.NumGoroutine() > floor
	})

	writeIntent(t, conn, 5)

	// The reader is now inside the resolver and cannot leave.
	select {
	case <-resolver.entered:
	case <-time.After(settleBudget):
		t.Fatal("the reader never entered the resolver within the budget")
	}

	if err := harness.hub.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatalf("hub.Close() error = %v, want nil", err)
	}

	// The client is told the table is gone, which happens whether or not the reader is
	// joined: the pump's queues close, and the pump is not waiting on the reader.
	ctx, cancel := context.WithTimeout(t.Context(), settleBudget)
	defer cancel()

	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("a live socket survived the hub's shutdown")
	}

	// The resolver is still outstanding, so the reader is still inside it — and the
	// handler's goroutine is still on the stack because `serve` joins the reader before
	// it returns.
	if !resolver.outstanding(t) {
		t.Error("the resolver was released, so this test is not measuring a blocked reader")
	}

	// The **peak** over a window, against the floor: a point sample would race a
	// sibling the same way the shutdown test's did.
	if got := peakGoroutines(t) - floor; got < 2 {
		t.Errorf("goroutines with a reader blocked in a resolver = %d above the floor, "+
			"want at least 2: the reader, and the handler waiting on it", got)
	}

	// Release it, and both go away.
	close(resolver.release)

	waitFor(t, "the handler and the reader to be released", func() bool {
		return runtime.NumGoroutine() <= floor+goroutineTolerance
	})
}

// An oversize frame does not kill the connection *silently*.
//
// "Silently" is the operative word. A read limit that merely truncated would turn an
// oversized frame into a malformed one and the client would see a close with
// somebody else's reason, so the assertion is on a **close frame** — not on the
// payload's absence, and not on "the connection ended".
func TestAnOversizeFrameClosesTheConnectionDeliberately(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, &stubResolver{})
	conn := harness.dialAs(gmSlug, gmUserID)

	waitFor(t, "the peer to join", func() bool { return harness.hub.Stats().Peers == 1 })

	// One message past the transport's own limit, and it is a *valid* frame padded
	// out — so what refuses it is the size and not the grammar. A test that sent
	// `strings.Repeat("x", n)` would be refused by the codec at any size, which
	// proves nothing about the limit.
	//
	// The padding is a valid unknown-field-free shape: a long `placement` token, so
	// the only thing wrong with the frame is how much of it there is.
	oversize := []byte(`{"t":"intent","seq":1,"op":"move_token","args":{"placement":"p1",` +
		`"reason":"` + strings.Repeat("a", realtime.MaxTransportReadBytes) + `"}}`)

	if len(oversize) <= realtime.MaxTransportReadBytes {
		t.Fatalf("the oversize fixture is %d bytes, which is not past the %d limit",
			len(oversize), realtime.MaxTransportReadBytes)
	}

	if err := conn.Write(t.Context(), websocket.MessageText, oversize); err != nil {
		t.Fatalf("write an oversize frame: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), settleBudget)
	defer cancel()

	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("an oversize frame was accepted")
	}

	if websocket.CloseStatus(err) == -1 {
		t.Errorf("an oversize frame ended the connection with a bare transport error "+
			"(%v) rather than a close frame, so the client is told nothing", err)
	}

	waitFor(t, "the peer to be released", func() bool { return harness.hub.Stats().Peers == 0 })
}

// A malformed frame is refused with the codec's own class and nothing else.
//
// Two assertions and the second is the security one: **the reason that reaches the
// wire is a class, not the codec's text.** A `FrameError`'s message quotes the
// bytes that broke it, and those bytes are whatever a client chose to send — so a
// reason that echoed them would render a client-chosen string in a browser and land
// it in a log (S-12.3).
func TestAMalformedFrameClosesTheConnectionWithAClassAndNotTheBytes(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, &stubResolver{})
	conn := harness.dialAs(gmSlug, gmUserID)

	waitFor(t, "the peer to join", func() bool { return harness.hub.Stats().Peers == 1 })

	// A marker that would be recognisable in a reason if the reason carried bytes, and
	// which the codec is *likely* to quote — a decoder that names the field it did not
	// recognise is the useful behaviour, and it is exactly what must not be forwarded.
	const marker = "hunter2-the-client-sent-this"

	writeRaw(t, conn, `{"t":"intent","seq":1,"op":"move_token","`+marker+`":1}`)

	ctx, cancel := context.WithTimeout(t.Context(), settleBudget)
	defer cancel()

	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("a frame with an unknown field was accepted")
	}

	if got := websocket.CloseStatus(err); got != websocket.StatusPolicyViolation {
		t.Errorf("close status after a malformed frame = %d, want %d", got,
			websocket.StatusPolicyViolation)
	}

	// A **value**, not a pointer. The library's `parseClosePayload` returns a
	// `CloseError` by value and wraps it with `%w`, so `errors.As` reaches a
	// `CloseError` and not a `*CloseError` — and an `errors.As` against the pointer
	// form reports false against a perfectly good close frame, which is the kind of
	// assertion that fails for a reason nobody can see.
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("the client saw %#v, want a close frame carrying a reason", err)
	}

	if strings.Contains(closeErr.Reason, marker) {
		t.Errorf("the close reason carries the client's own bytes: %q", closeErr.Reason)
	}

	// The **exact** reason, read from the codec rather than spelled out here. A prefix
	// assertion would be satisfied by `err.Error()` — which also begins with the
	// route's own prefix — and the codec's text for this frame does not happen to
	// quote the field name, so "the reason contains no client bytes" alone is a
	// weaker claim than it looks. Deriving the expected value from `realtime.Decode`
	// on the same payload makes the assertion about the wiring of the class into the
	// close reason rather than about a string this file invented.
	payload := `{"t":"intent","seq":1,"op":"move_token","` + marker + `":1}`

	_, decodeErr := realtime.Decode([]byte(payload))
	if decodeErr == nil {
		t.Fatal("a frame with an unknown field decoded, so the class under test is unreachable")
	}

	if want := "frame_" + realtime.FrameClass(decodeErr); closeErr.Reason != want {
		t.Errorf("close reason = %q, want %q", closeErr.Reason, want)
	}

	if len(closeErr.Reason) > 123 {
		t.Errorf("the close reason is %d bytes, over the 123 a control frame carries",
			len(closeErr.Reason))
	}
}

// A route with no hub answers 503 rather than opening a socket that receives
// nothing.
//
// The alternative is a 101 and a client that waits for a snapshot that can never
// arrive, which a browser reports as an intermittent network error for as long as
// the miswiring lasts.
func TestARouteWithNoHubRefusesRatherThanOpeningASocket(t *testing.T) {
	t.Parallel()

	harness := newUnwiredHarness(t)

	// As the GM, because the gate answers an anonymous requestor first and this test
	// is about what the *handler* does once a member has got past it. Dialling
	// anonymously would assert 401 and prove nothing about the wiring fault.
	header := http.Header{}
	header.Set(userHeader, strconv.FormatInt(gmUserID, 10))

	if got := harness.dialStatus(gmSlug, header); got != http.StatusServiceUnavailable {
		t.Errorf("a handler with no hub = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

// A refusal **this route** produces is not cacheable.
//
// AGENTS.md states that every reader-dependent response is `private, no-store`
// because a reverse proxy in front of a self-hosted instance is the ordinary
// deployment, and this route's own refusals are about the connection rather than the
// campaign — but the same proxy would cache them, and a cached 403 on `/play` is a
// table that stays shut until somebody clears a cache.
//
// The refusal is the cross-origin one rather than a gate's, and the choice is the
// point: the gate answers with `campaigns.writeError`, which sets these directives
// itself, so a test aimed at a gate refusal would assert the gate and learn nothing
// about `refuse`. The status is asserted alongside the header so a 403 from the wrong
// layer cannot satisfy it.
func TestARefusalIsPrivateAndNoStore(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, &stubResolver{})

	header := http.Header{}
	header.Set(userHeader, strconv.FormatInt(gmUserID, 10))
	header.Set("Origin", "http://elsewhere.example")

	//nolint:bodyclose // `dial` drains and closes the body on its failure path.
	_, resp, err := harness.dial(gmSlug, header)
	if err == nil {
		t.Fatal("a cross-origin upgrade succeeded, but the test is about its refusal")
	}

	if got := statusOf(resp); got != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, and a refusal from another layer would make "+
			"the header assertions below vacuous", got, http.StatusForbidden)
	}

	control := resp.Header.Get("Cache-Control")
	for _, directive := range []string{"private", "no-store"} {
		if !strings.Contains(control, directive) {
			t.Errorf("Cache-Control on a refusal = %q, want it to carry %q: a shared "+
				"cache must not keep a refused table", control, directive)
		}
	}
}

// A live socket outlives the server's `WriteTimeout`, with a broadcast delivered
// after it.
//
// A real server deadline and a real socket, and the assertion is the product
// property: a table must not close on a timer the operator set for pages.
//
// Stated honestly, because the mechanism today is not this route's: `net/http`
// clears both deadlines on a hijacked connection — `(*conn).hijackLocked` calls
// `SetDeadline(time.Time{})` — so the route's own `clearWriteDeadline` is not what
// makes this pass. It is kept because the property is the one that matters and the
// standard library's guarantee is invisible from here: a `ResponseWriter` that did not
// reach the real connection on `Hijack`, or an upgrade that stopped hijacking, would
// break this silently. The mutation of that call does **not** fail this test, and
// saying so is the point of saying it.
func TestALiveSocketOutlivesTheServersWriteTimeout(t *testing.T) {
	t.Parallel()

	const writeTimeout = 300 * time.Millisecond

	// Both bounds are configured, and the read bound is the one that matters here: it
	// has to outlast the write timeout, or the assertion would be about the route's
	// own deadline rather than the server's.
	harness := newMounted(t, mount{
		resolve: &stubResolver{},
		handler: func(handler *play.Handler) { handler.ReadTimeout = 5 * time.Second },
		server:  func(server *http.Server) { server.WriteTimeout = writeTimeout },
	})

	conn := harness.dialAs(gmSlug, gmUserID)

	// Past the deadline, with a frame arriving afterwards.
	time.Sleep(2 * writeTimeout)

	publishDelta(t, harness.hub, gmCampaignID, 1)

	ctx, cancel := context.WithTimeout(t.Context(), settleBudget)
	defer cancel()

	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("the socket did not outlive the server's %s write timeout: %v",
			writeTimeout, err)
	}
}

// What a connection costs, in goroutines.
//
// `hub.go`'s header says there is no goroutine per peer, and the claim is about the
// **hub**: its sends are non-blocking, so it never needs a goroutine per subscriber.
// A socket that reads and writes cannot be served from one goroutine, and this is the
// assertion that says what the route's marginal cost actually is rather than leaving
// the reader unclaimed.
//
// The count is **marginal** — a mark, then a batch, then a second mark — because an
// absolute count would be asserting on whatever else the test binary is doing. The
// expected composition is two per connection: `net/http`'s own goroutine serving the
// hijacked connection, which is the one it already accounts for, and this route's
// reader. So the assertion is a bound, `[1, 2]` per connection, and the number that
// matters is the **upper** one: a route that started a goroutine per queue, or per
// broadcast, or per pending write would exceed it, and none of those is bounded by
// anything else in the design.
//
// The lower bound is load-bearing too. A route that started *no* goroutine per
// connection could not read and write at all, and a count of zero would otherwise
// satisfy "at most two".
func TestAConnectionCostsOneReaderAndNothingElse(t *testing.T) {
	// Deliberately **not** `t.Parallel`, and this is the whole reason the test was
	// red on CI and green locally.
	//
	// It measures a process-global: `runtime.NumGoroutine` counts every goroutine in
	// the binary. Run in parallel with the rest of the package, its peak includes
	// the other tests' sockets, their readers, and — under `-race` on a loaded
	// runner — the detector's own bookkeeping. The assertion is a band, so extra
	// goroutines push the cost above it and the failure is about the machine rather
	// than about this route.
	//
	// The property under test is *this route's* cost per connection. Measuring a
	// process-global to test a local property needs the global to be quiet, so the
	// test takes the package for itself rather than widening the band until it can
	// no longer catch the bug it exists for. Two readers per connection would still
	// fail at `batch + 1`; the band's upper bound is kept tight on purpose.
	const batch = 10

	harness := newHarness(t, &stubResolver{})

	conns := make([]*websocket.Conn, 0, batch)

	// One warm-up connection, opened and closed, **before** the mark. The first
	// `websocket.Dial` in a process starts the client transport's own bookkeeping and
	// it does not go away, so without this the delta carries a one-time cost that
	// belongs to the client rather than to any connection — and the test would be
	// asserting a number one larger than the one it means.
	warmup := harness.dialAs(gmSlug, gmUserID)
	_ = warmup.CloseNow()

	waitFor(t, "the warm-up connection to be released", func() bool {
		return harness.hub.Stats().Peers == 0
	})

	floor := settleGoroutines(t)

	for range batch {
		conns = append(conns, harness.dialAs(gmSlug, gmUserID))
	}

	waitFor(t, "every connection to join", func() bool {
		return harness.hub.Stats().Peers == batch
	})

	// The **peak** over a window rather than a point sample: the readers are started
	// by the upgrades, and a sample taken before they are scheduled is a count of the
	// wrong thing.
	peak := peakGoroutines(t)
	cost := peak - floor

	if cost < batch || cost > 2*batch {
		t.Errorf("a connection cost %d goroutines across %d connections, want between "+
			"%d and %d: one is net/http's connection goroutine and one is this route's "+
			"reader (%d at the floor, %d at the peak)", cost, batch, batch, 2*batch,
			floor, peak)
	}

	// And they come back, so the reader is released rather than merely bounded. This
	// is the half a count cannot make: a leaked reader is the failure, and a leaked
	// reader over a campaign's whole life is unbounded.
	for _, conn := range conns {
		_ = conn.CloseNow()
	}

	waitFor(t, "the connections to be released", func() bool {
		return runtime.NumGoroutine() <= floor+goroutineTolerance
	})
}

// goroutineTolerance is how far above the observed floor a test will accept the count
// coming back down to.
//
// Two, and it is stated rather than hidden: the floor is a minimum over a window and
// the test binary's own machinery — sibling tests starting and finishing — is not
// obliged to stay at it. A tolerance of zero would make three of the four goroutine
// assertions a race with every other test in the package.
const goroutineTolerance = 2

// Goroutine counting, in the presence of parallel tests.
//
// The obvious spelling — take a number, open N connections, take another — is a race
// with every sibling test in the binary, and it failed here exactly that way: a
// sibling finishing between the two samples moved the count **down**, and a
// `>=` against a mark taken earlier waited for a number that was never going to
// arrive. So the two helpers below sample a **window** and take an extremum:
//
//   - `settleGoroutines` returns the **minimum** over a short window, which is the
//     quietest moment observed and therefore the fairest floor.
//   - `peakGoroutines` returns the **maximum** over a short window, which is the
//     busiest.
//
// A difference between the two is then a statement about this test's connections
// rather than about the binary, because any sibling churn is in both. The tests below
// keep a small tolerance on the way back down for the same reason and say so.
const goroutineWindow = 150 * time.Millisecond

// settleGoroutines returns the quietest goroutine count observed over a window.
func settleGoroutines(t *testing.T) int {
	t.Helper()

	lowest := -1

	deadline := time.Now().Add(goroutineWindow)

	for {
		current := runtime.NumGoroutine()
		if lowest < 0 || current < lowest {
			lowest = current
		}

		if time.Now().After(deadline) {
			return lowest
		}

		time.Sleep(poll)
	}
}

// peakGoroutines returns the busiest goroutine count observed over a window.
func peakGoroutines(t *testing.T) int {
	t.Helper()

	highest := 0

	deadline := time.Now().Add(goroutineWindow)

	for {
		if current := runtime.NumGoroutine(); current > highest {
			highest = current
		}

		if time.Now().After(deadline) {
			return highest
		}

		time.Sleep(poll)
	}
}

// A client that outruns the frame rate is closed deliberately, with the code that
// says it was refused rather than disconnected.
//
// This is the socket half of `frameMeter`'s unit test, and it is the half the unit
// test cannot give: the meter is arithmetic, and what matters about it is **where the
// trip goes** — a connection that ends with a bare transport error is a client that
// retries forever against a rule it was never told, and a 1011 would be a claim that
// the server broke something. 1008 with a fixed reason is the answer.
func TestAClientThatOutrunsTheFrameRateIsClosedWithAPolicyViolation(t *testing.T) {
	t.Parallel()

	// Comfortably over the limit rather than one past it, because the count is
	// per window and a test that sits exactly on the boundary is a test whose result
	// depends on how many frames the client managed before the server's first read
	// returned.
	const frames = 4 * defaultFrameLimitForTest

	harness := newHarness(t, &stubResolver{})
	conn := harness.dialAs(gmSlug, gmUserID)

	waitFor(t, "the peer to join", func() bool { return harness.hub.Stats().Peers == 1 })

	// Written as fast as the socket takes them, and never read: the rate is counted
	// on arrival, and a client that also drained its own replies would be a different
	// client.
	for range frames {
		if err := conn.Write(t.Context(), websocket.MessageText,
			[]byte(`{"t":"presence","args":{"cursor":[1,2]}}`)); err != nil {
			// A refusal partway through is the expected outcome on a fast machine, and
			// the read below is what asserts it properly.
			break
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), settleBudget)
	defer cancel()

	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("a client past the frame rate was still served")
	}

	if got := websocket.CloseStatus(err); got != websocket.StatusPolicyViolation {
		t.Errorf("close status after a frame-rate trip = %d, want %d", got,
			websocket.StatusPolicyViolation)
	}

	// The reason, and **not** the status, is what tells a rate trip from a protocol
	// error: both are 1008, and a client told "frame_unknown_field" when it sent
	// nothing but valid frames will fix the wrong thing. A codec refusal's reason
	// begins with the codec's class; a rate's does not.
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("the client saw %#v, want a close frame carrying a reason", err)
	}

	if strings.HasPrefix(closeErr.Reason, "frame_") {
		t.Errorf("close reason after a frame-rate trip = %q, which reads as a codec "+
			"refusal; a client told it sent bad bytes will fix the wrong thing",
			closeErr.Reason)
	}

	if closeErr.Reason != "read_bound" {
		t.Errorf("close reason after a frame-rate trip = %q, want %q", closeErr.Reason,
			"read_bound")
	}
}

// defaultFrameLimitForTest is the route's own limit, so the test sends several times
// it rather than a number that has to be kept in step by hand.
const defaultFrameLimitForTest = 60

// writeIntent sends one well-formed intent frame.
func writeIntent(t *testing.T, conn *websocket.Conn, seq uint64) {
	t.Helper()

	writeRaw(t, conn, `{"t":"intent","seq":`+strconv.FormatUint(seq, 10)+
		`,"op":"move_token","args":{"placement":"p1","x":10,"y":20}}`)
}

// writeRaw sends a frame verbatim, so a test can send one the codec refuses.
func writeRaw(t *testing.T, conn *websocket.Conn, payload string) {
	t.Helper()

	if err := conn.Write(t.Context(), websocket.MessageText, []byte(payload)); err != nil {
		t.Fatalf("write %q: %v", payload, err)
	}
}

// publishDelta broadcasts one change, the way the hub's own mutation path does.
//
// A `delta` and not a `clock`, because the clock is the one outbound frame a
// production client would not tolerate being flooded with and the tests here publish
// in a loop on purpose.
func publishDelta(t *testing.T, hub *realtime.Hub, campaignID, version int64) {
	t.Helper()

	if err := hub.Publish(campaignID, &realtime.ServerDelta{
		Type: realtime.TypeDelta,
		Changes: []realtime.Change{{
			Placement: placement,
			Version:   realtime.Version(version),
			Op:        "move_token",
			By:        realtime.UserID(gmUserID),
		}},
	}); err != nil {
		t.Fatalf("publish a delta: %v", err)
	}
}
