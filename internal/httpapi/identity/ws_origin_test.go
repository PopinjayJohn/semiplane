package identity_test

// The WebSocket's `Origin`, and the authorisation the socket shares with the
// document it upgrades.
//
// The claim has two halves, and the second is the one a socket is uniquely able to
// lose:
//
//   1. **`Origin` is enforced before the handshake**, and enforced as *this
//     instance's* page rather than as "not obviously cross-origin". `Origin` is a
//     browser header and the session cookie rides along automatically, so a page on
//     another site that opens a socket is not a stranger asking — it is a stranger
//     asking **as the reader**.
//   2. **A socket enforces the same authorisation as the document.** `/c/{slug}/ws`
//     and `/c/{slug}/play` are one capability with two transports, and a socket more
//     permissive than its page is the whole vulnerability: a table's live state is
//     readable, writable and observable by anyone who can reach the port, and the
//     document next to it is not.
//
// `internal/httpapi/play`'s own suite holds both claims over real sockets — the S-8
// matrix, the four `Origin` branches, the absence branch — and it does it with the
// identity injected from a **header**, because the routes read the identity from the
// context and the header is the least surprising way for a fixture to put it there.
// So the gap this file fills is the credential path itself: a reader identified by a
// **session cookie**, resolved by `identity.Authenticate`, through the same
// `campaigns.Resolve` and the same `play.Mount` the composition root uses.
//
// Which is also why this file is in the identity package. It is the only place the
// two halves meet end to end: a cookie becomes a `domain.Requestor`, the requestor
// becomes a tier, the tier becomes a status code on both transports, and the status
// code must be the same one on both. The role rows and the 404 rows are here for the
// same reason — the gate is what turns an identity into a decision, and this is the
// only place both ends of that are in one test.
//
// # No store, no process-wide slot
//
// The campaign and membership lookups are a scripted `campaigns.Store` and the
// session lookup is a scripted `identity.Reader`, both of which those packages define
// as narrow interfaces for exactly this. The only real database is the
// `realtime.Registry`'s own `campaign_state` table, opened over a bare `sql.DB` — the
// same shape `play`'s harness uses, and deliberately **not** `store.Open`, so
// nothing here competes for ADR 0004's process-wide single-instance slot and every
// test here can be parallel.

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	_ "modernc.org/sqlite"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/play"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

// The three campaigns, the three users, and the reader who is a member of nothing.
//
// Fixed rather than generated so a failure message names a number a reader can look
// up in the fixture below.
const (
	publicCampaign  int64 = 1
	privateCampaign int64 = 2
	// foreignCampaign is private and has a GM who is not `gmUser`, so it is a
	// campaign the fixture's members can be shown reaching for.
	foreignCampaign int64 = 3

	gmUser     int64 = 11
	playerUser int64 = 12
	// outsiderUser is authenticated and in **no** campaign. That is the row the
	// 404-not-403 claim cannot be expressed without: a reader who is a member of
	// nothing resolves to `TierReadOnly` on a public campaign and `TierNone` on a
	// private one, so the two refusals are distinguishable in the matrix and the
	// claim is that the *answers* are not.
	outsiderUser int64 = 13
	// adminUser is an instance administrator and a member of nothing. S-2.7: that
	// raises nothing on a campaign, and the row is here because it is the one an
	// authorisation bug hides behind.
	adminUser int64 = 14
	// editorUser holds a membership whose role this build has no name for. There is
	// no `editor` role (S-6.5: content editing is GM-only), so a row carrying one is
	// a real state after a downgrade removed a role — and it must be no access
	// rather than a guess.
	editorUser int64 = 15
)

const (
	publicSlug  = "gilded-cage"
	privateSlug = "hollow-choir"
	foreignSlug = "drowned-lighthouse"
	absentSlug  = "no-such-campaign"
)

// reader is one identified account.
//
// **No token field**, and that is load-bearing rather than an omission. The session
// tokens are minted by the store each harness builds, so a table built before any
// harness existed holds no token — and a reader whose token was still empty would
// arrive anonymous, which is the shape of a fixture that passes for the wrong reason.
// Asking the store for the token at dial time removes the ordering hazard entirely.
type reader struct {
	name string
	id   int64
	// anonymous rather than `id == 0`, because the distinction the gate makes is
	// `Authenticated` and not "has an id": a requestor the middleware failed to
	// authenticate still carries one, and treating the two the same is the mistake
	// `domain.Requestor`'s own comment is about.
	anonymous bool
	admin     bool
}

// harness is one mounted instance: a scripted store, a scripted session reader, a
// real hub and a real server.
type harness struct {
	t      *testing.T
	server *httptest.Server
	hub    *realtime.Hub
	store  *scriptedStore
}

// newHarness mounts the table's two routes behind the real chain.
//
// The chain is the one `router.go` composes, in the same order: authenticate, then
// resolve the campaign, then the route's own `RequirePlay`. `Resolve` reads
// `r.PathValue("slug")`, so it has to be registered on a **pattern** rather than
// wrapped around the whole server — a fixture that got that wrong would resolve every
// campaign as `TierNone` and see a uniform 404, which is exactly the shape a passing
// matrix can have for the wrong reason.
func newHarness(t *testing.T) *harness {
	t.Helper()

	backing := newScriptedStore()

	registry := newStateRegistry(t)

	hub := realtime.NewHub(context.WithoutCancel(t.Context()), realtime.HubConfig{
		States: registry,
		Clock:  realtime.HubClock{Stale: time.Hour},
	})

	handler := &play.Handler{
		Hub:         hub,
		ReadTimeout: 250 * time.Millisecond,
		Instance:    components.InstanceView{Name: "Greyhaven", Version: "test"},
		SignOutHref: "/logout",
		StatusHref:  "/status",
		Campaigns:   backing,
	}

	route := http.NewServeMux()
	play.Mount(route, handler)

	mux := http.NewServeMux()
	mux.Handle("/c/{slug}/", campaigns.Resolve(backing)(route))

	server := httptest.NewServer(identity.Authenticate(backing)(mux))
	t.Cleanup(server.Close)

	t.Cleanup(func() {
		if err := hub.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("hub.Close() error = %v, want nil", err)
		}
	})

	return &harness{t: t, server: server, hub: hub, store: backing}
}

// cookie returns the header carrying this reader's session cookie, and nothing for
// the anonymous one.
func (h *harness) cookie(who reader) http.Header {
	header := http.Header{}

	if who.anonymous {
		return header
	}

	header.Set("Cookie", auth.SessionCookieName+"="+h.store.tokenFor(h.t, who.id))

	return header
}

// dial opens a socket to slug as one reader, with the given extra headers.
//
// A real `websocket.Dial` rather than an `http.Client` doing the handshake by hand,
// because this route's refusals are status codes and a client that mishandles a 101
// cannot observe a refusal at all. The response is returned rather than only the
// error for the same reason: the status code is on the handshake response.
func (h *harness) dial(
	slug string,
	who reader,
	header http.Header,
) (*websocket.Conn, *http.Response, error) {
	h.t.Helper()

	if header == nil {
		header = http.Header{}
	}

	endpoint := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/c/" + slug + "/ws"

	// The cookie rides on the handshake header rather than in a jar, so the reader is
	// whoever the test says they are and nothing else.
	for _, values := range h.cookie(who) {
		header[http.CanonicalHeaderKey("Cookie")] = values
	}

	conn, resp, err := websocket.Dial(
		context.Background(),
		endpoint,
		&websocket.DialOptions{HTTPHeader: header},
	)

	// Drained and closed on the failure path: a refused handshake is a 4xx with a
	// body, and a body left unread holds the connection out of the client's pool.
	if err != nil && resp != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	// Unwrapped deliberately and unlike almost everything else in this repository:
	// the status code the test reads lives on `resp`, and wrapping the error would
	// be wrapping a value whose only job is to be handed back whole. `wrapcheck`
	// objects because the rule is right nearly everywhere else.
	//nolint:wrapcheck // see above: `resp` carries the status this test exists to read
	return conn, resp, err
}

// dialStatus opens a socket expecting a refusal and returns the status.
//
// It fails the test if the handshake **succeeds**, because a test that asked for a
// refusal and got a socket would go on to assert things about a connection that
// should not exist.
func (h *harness) dialStatus(slug string, who reader, header http.Header) int {
	h.t.Helper()

	//nolint:bodyclose // `dial` drains and closes the body on its failure path.
	conn, resp, err := h.dial(slug, who, header)
	if err == nil {
		_ = conn.CloseNow()
		h.t.Fatalf("the handshake to /c/%s/ws succeeded as %s, but a refusal was required",
			slug, who.name)
	}

	status := 0
	if resp != nil {
		status = resp.StatusCode
	}

	if status == 0 {
		h.t.Fatalf("the handshake to /c/%s/ws failed with %v and carried no HTTP status",
			slug, err)
	}

	return status
}

// document fetches the table's document as one reader and reads it whole.
//
// The headers come with the body because two of this route's claims are about
// headers — the cache policy and the content type — and a helper returning only the
// body would make each of them a second request.
func (h *harness) document(slug string, who reader) (int, http.Header, string) {
	h.t.Helper()

	request, err := http.NewRequestWithContext(
		h.t.Context(),
		http.MethodGet,
		h.server.URL+"/c/"+slug+"/play",
		http.NoBody,
	)
	if err != nil {
		h.t.Fatalf("build the request for %s: %v", slug, err)
	}

	maps.Copy(request.Header, h.cookie(who))

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		h.t.Fatalf("GET /c/%s/play: %v", slug, err)
	}

	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatalf("read the body of %s: %v", slug, err)
	}

	return response.StatusCode, response.Header, string(body)
}

// statusOf reads a refused handshake's status, or zero when there was no response.
func statusOf(resp *http.Response) int {
	if resp == nil {
		return 0
	}

	return resp.StatusCode
}

// TestTheSocketAndItsDocumentAnswerTheSameWay is the parity claim, and it is
// asserted as a **pair** rather than as two matrices.
//
// Two transports, one capability. For every identity and every campaign, the two
// routes must either both admit or both refuse — and when they refuse, with the same
// status. A socket that admitted what its document refused would be a table reachable
// without the gate the document is behind, and the symptom is not a wrong status: it
// is an open socket carrying the campaign's live state.
//
// The admitted rows are asserted too, because a pair of routes that both refused
// everything would pass a parity test, and the affirmative is where a socket differs
// legitimately: `/ws` answers 101 and `/play` answers 200.
func TestTheSocketAndItsDocumentAnswerTheSameWay(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		slug string
		who  reader
		// wantRefusal is 0 for the rows both routes admit.
		wantRefusal int
	}{
		{
			name: "a GM reaches both", slug: publicSlug, who: readers["gm"],
		},
		{
			name: "a player reaches both", slug: publicSlug, who: readers["player"],
		},
		{
			// S-8.1: no amount of public visibility makes a tabletop public.
			name: "an anonymous reader of a public campaign", slug: publicSlug,
			who: readers["anonymous"], wantRefusal: http.StatusUnauthorized,
		},
		{
			name: "an authenticated non-member of a public campaign", slug: publicSlug,
			who: readers["outsider"], wantRefusal: http.StatusForbidden,
		},
		{
			name: "an anonymous reader of a private campaign", slug: privateSlug,
			who: readers["anonymous"], wantRefusal: http.StatusNotFound,
		},
		{
			// The row that holds "no access is 404, never 403": the reader is
			// authenticated and so would get a 403 from a public campaign.
			name: "an authenticated non-member of a private campaign", slug: privateSlug,
			who: readers["outsider"], wantRefusal: http.StatusNotFound,
		},
		{
			// A membership naming a role this build has no name for is no access, and
			// no access is a 404.
			//
			// **The foreign campaign**, which is the one the editor actually holds a
			// membership in. Pointing this row at a campaign they are not a member of
			// would produce the same 404 for a different reason — a non-member of a
			// private campaign — and the row would then be testing the previous case a
			// second time while appearing to cover this one.
			name: "a member whose role this build has no name for", slug: foreignSlug,
			who: readers["editor"], wantRefusal: http.StatusNotFound,
		},
		{
			name: "an instance administrator", slug: publicSlug,
			who: readers["admin"], wantRefusal: http.StatusForbidden,
		},
		{
			name: "a slug that resolves to nothing", slug: absentSlug,
			who: readers["outsider"], wantRefusal: http.StatusNotFound,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			status, header, _ := h.document(testCase.slug, testCase.who)

			if testCase.wantRefusal == 0 {
				if status != http.StatusOK {
					t.Fatalf("GET /c/%s/play as %s = %d, want 200",
						testCase.slug, testCase.who.name, status)
				}

				// Admitted on both transports: the socket is the row a parity test
				// cannot reach with a status code alone, because a refusal and a
				// refusal is a pass and so is an admission and an admission.
				//nolint:bodyclose // `dial` drains and closes the body on failure.
				conn, resp, err := h.dial(testCase.slug, testCase.who, nil)
				if err != nil {
					t.Fatalf("dial /c/%s/ws as %s: %v (status %d)",
						testCase.slug, testCase.who.name, err, statusOf(resp))
				}

				_ = conn.CloseNow()

				// The document is not a table, and the difference is the point: a 200
				// here is not the capability the socket is.
				if got := header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
					t.Errorf("Content-Type = %q, want text/html", got)
				}

				return
			}

			if status != testCase.wantRefusal {
				t.Errorf("GET /c/%s/play as %s = %d, want %d",
					testCase.slug, testCase.who.name, status, testCase.wantRefusal)
			}

			// The socket, and the comparison the test exists for.
			if got := h.dialStatus(testCase.slug, testCase.who, nil); got != status {
				t.Errorf("/c/%s/ws answered %d where /c/%s/play answered %d as %s: a "+
					"socket more permissive than its document is the whole vulnerability",
					testCase.slug, got, testCase.slug, status, testCase.who.name)
			}

			// A refused peer holds no state, and the peer count is the only observation
			// that reaches it — which is where a handler that checked the gate *after*
			// the upgrade would show up.
			if got := h.hub.Stats().Peers; got != 0 {
				t.Errorf("hub.Stats().Peers = %d after a refused upgrade as %s, want 0",
					got, testCase.who.name)
			}
		})
	}
}

// TestNoAccessIs404AndTheBodyDoesNotSayWhy is the existence-oracle claim, asserted
// on the bytes rather than on the status.
//
// A 403 says "this exists and you may not". A 404 says "there is nothing here" — and
// a private campaign has to answer the *second* one, identically, to anything a
// stranger asks. The status alone is not the claim: **the body must be
// byte-identical too**, because a body that named the campaign, or a header that
// differed, would restore the oracle through a channel a status-code test never
// looks at.
//
// Both transports, and the gate's own `Cache-Control`, because the ordinary
// deployment for a self-hosted instance has a reverse proxy in front of it and a
// cached 404 is a 404 served to somebody who was entitled.
func TestNoAccessIs404AndTheBodyDoesNotSayWhy(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, _, privateBody := h.document(privateSlug, readers["outsider"])
	_, privateHeader, _ := h.document(privateSlug, readers["outsider"])
	_, _, absentBody := h.document(absentSlug, readers["outsider"])
	_, absentHeader, _ := h.document(absentSlug, readers["outsider"])

	if privateBody != absentBody {
		t.Errorf("the body for an invisible campaign differs from the body for a "+
			"campaign that does not exist:\ninvisible: %q\nabsent:    %q",
			privateBody, absentBody)
	}

	for _, header := range []struct {
		name string
		got  http.Header
	}{
		{name: "invisible campaign", got: privateHeader},
		{name: "absent campaign", got: absentHeader},
	} {
		if got := header.got.Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("Cache-Control for a %s is %q, want %q: a shared cache in front of "+
				"this instance would serve a stored 404 to the GM who is entitled to it",
				header.name, got, "private, no-store")
		}
	}

	// And the socket's refusal is the same shape, so a cross-origin page probing for
	// campaigns gets one answer from both transports.
	socketAbsent := h.dialStatus(absentSlug, readers["outsider"], nil)
	socketPrivate := h.dialStatus(privateSlug, readers["outsider"], nil)

	if socketAbsent != socketPrivate {
		t.Errorf("/c/%s/ws = %d where /c/%s/ws = %d as a non-member: the socket "+
			"distinguishes a campaign it may not see from one that is not there",
			absentSlug, socketAbsent, privateSlug, socketPrivate)
	}

	if socketPrivate != http.StatusNotFound {
		t.Errorf("/c/%s/ws as a non-member = %d, want 404", privateSlug, socketPrivate)
	}
}

// TestContentEditingIsGMOnly is the role matrix, and it is here rather than in the
// wiki package because the *same* gate answers both transports and one mount list is
// what keeps them in step.
//
// S-6.5: content editing is GM-only and **there is no `editor` role**. The rows are
// the three answers the guard can give, and each one is a different failure mode:
//
//   - The player gets a **403**. They are entitled to *something* here — the campaign
//     is not a secret from them — and the answer is that this particular action needs
//     a different role.
//   - The anonymous reader gets a **401 with a challenge**, because reaching a
//     capability gate at all means the campaign is not itself a secret, and 401 is
//     what tells a reader that signing in will help.
//   - The `editor` gets a **404**. Their membership names a role this build has no
//     name for, `ResolveAccess` treats an unrecognised role as *no access* rather
//     than guessing at it, and no access is a 404. A build that grew an `editor` role
//     later without an ADR would turn this row from 404 into a grant, which is why it
//     is asserted rather than assumed.
func TestContentEditingIsGMOnly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		slug string
		who  reader
		// wantStatus is the status the guard answers with.
		wantStatus int
		// wantChallenge is whether a `WWW-Authenticate` header must be present, which
		// is the difference between a 401 and a 403 wearing its status.
		wantChallenge bool
	}{
		{
			name: "the GM may edit", slug: publicSlug, who: readers["gm"],
			wantStatus: http.StatusNoContent,
		},
		{
			// The row that matters: a player is a member and still may not write
			// content. S-14.4 permits 403 and nothing else here.
			name: "a player may not edit", slug: publicSlug, who: readers["player"],
			wantStatus: http.StatusForbidden,
		},
		{
			name: "an anonymous reader is challenged", slug: publicSlug,
			who: readers["anonymous"], wantStatus: http.StatusUnauthorized,
			wantChallenge: true,
		},
		{
			name: "an authenticated non-member may not edit", slug: publicSlug,
			who: readers["outsider"], wantStatus: http.StatusForbidden,
		},
		{
			// S-2.7: instance administration is not campaign write.
			name: "an instance administrator may not edit a campaign", slug: publicSlug,
			who: readers["admin"], wantStatus: http.StatusForbidden,
		},
		{
			// The unknown-role row, against the campaign whose membership names it, so
			// the 404 is this row's reason and not the previous one.
			name: "a member whose role this build has no name for", slug: foreignSlug,
			who: readers["editor"], wantStatus: http.StatusNotFound,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			// A real `RequireEdit` mounted behind the real `Resolve`, and a handler
			// that answers 204 — so the assertions are about the gate and not about
			// whatever a wiki edit route does with a body it never received.
			route := http.NewServeMux()
			route.Handle("PUT /c/{slug}/wiki/Page",
				campaigns.RequireEdit(http.HandlerFunc(
					func(w http.ResponseWriter, _ *http.Request) {
						w.WriteHeader(http.StatusNoContent)
					},
				)),
			)

			mux := http.NewServeMux()
			mux.Handle("/c/{slug}/", campaigns.Resolve(h.store)(route))

			server := httptest.NewServer(identity.Authenticate(h.store)(mux))
			t.Cleanup(server.Close)

			request, err := http.NewRequestWithContext(
				t.Context(),
				http.MethodPut,
				server.URL+"/c/"+testCase.slug+"/wiki/Page",
				http.NoBody,
			)
			if err != nil {
				t.Fatalf("build the request: %v", err)
			}

			maps.Copy(request.Header, h.cookie(testCase.who))

			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("PUT /c/%s/wiki/Page as %s: %v",
					testCase.slug, testCase.who.name, err)
			}

			defer response.Body.Close()

			if response.StatusCode != testCase.wantStatus {
				t.Errorf("PUT /c/%s/wiki/Page as %s = %d, want %d",
					testCase.slug, testCase.who.name, response.StatusCode, testCase.wantStatus)
			}

			challenge := response.Header.Get("WWW-Authenticate")

			if testCase.wantChallenge && !strings.HasPrefix(challenge, "Cookie ") {
				t.Errorf("WWW-Authenticate = %q, want a Cookie challenge: %s is told to "+
					"sign in and a bare 401 tells them nothing about what would help",
					challenge, testCase.who.name)
			}

			if !testCase.wantChallenge && challenge != "" {
				t.Errorf("WWW-Authenticate = %q on a %d, want none: a challenge on a 403 "+
					"tells an authenticated reader that signing in would help, and it would not",
					challenge, testCase.wantStatus)
			}
		})
	}
}

// TestACrossOriginSocketIsRefusedForEveryIdentity is the `Origin` claim, and the
// reason it is a table over identities rather than a table over origins.
//
// A cross-origin refusal that only covers the GM proves nothing about anybody else,
// and the reader most likely to be a campaign's GM is the one whose cookie is worth
// stealing. So every identity this fixture can produce is dialled cross-origin, and
// the row that decides the case is the **instance administrator**: S-2.7 keeps
// administration out of the S-8 matrix, and a socket that honoured it for the
// transport would be handing the whole instance to a page on another site.
//
// And the refusal is asserted to have happened *before the join*, because the join is
// the only thing in the handler that touches a campaign: a cross-origin page must not
// be able to cause a peer to exist even transiently.
func TestACrossOriginSocketIsRefusedForEveryIdentity(t *testing.T) {
	t.Parallel()

	// Every identity, including anonymous and including the ones that are refused by
	// the gate anyway. The gate is checked first, so for those the 403 is never
	// reached — and that is worth asserting rather than leaving to the ordering, so
	// the identity that *is* entitled carries the weight.
	for _, who := range orderedReaders() {
		t.Run(who.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			header := http.Header{}
			header.Set("Origin", "https://elsewhere.example")

			got := h.dialStatus(publicSlug, who, header)

			want := http.StatusForbidden
			if !isEntitledToTheTable(who) {
				// Refused earlier, by the gate, with the gate's answer. Recording it
				// rather than skipping it is what keeps the table honest: a row that
				// said "403" for everything would pass a test that only checked the
				// status.
				want = gateRefusal(who)
			}

			if got != want {
				t.Errorf("a cross-origin upgrade as %s = %d, want %d", who.name, got, want)
			}

			if got := h.hub.Stats().Peers; got != 0 {
				t.Errorf("hub.Stats().Peers = %d after a refused origin as %s, want 0: "+
					"the join happens after the Origin check and must not happen at all",
					got, who.name)
			}
		})
	}
}

// orderedReaders is `readers` as a slice, in a fixed order.
//
// A map would make the subtest order random, and a table whose rows run in a
// different order on every machine is a table whose failure is hard to read. Sorting
// by name also means the fixture and the test cannot disagree about which identities
// exist: a row added to `readers` appears here without a second edit.
func orderedReaders() []reader {
	ordered := make([]reader, 0, len(readers))
	for _, who := range readers {
		ordered = append(ordered, who)
	}

	slices.SortFunc(ordered, func(a, b reader) int {
		return strings.Compare(a.name, b.name)
	})

	return ordered
}

// isEntitledToTheTable reports whether this reader reaches `/ws` at all.
func isEntitledToTheTable(who reader) bool {
	return who.id == gmUser || who.id == playerUser
}

// gateRefusal is the status `RequirePlay` answers this reader with, independently of
// `Origin`.
//
// Spelled out rather than shared with the production code so the table above asserts
// the S-8 matrix rather than agreeing with whatever the gate happens to do.
func gateRefusal(who reader) int {
	if !who.anonymous {
		// Authenticated: a non-member of a public campaign is `TierReadOnly`, which
		// reaches a capability gate and is refused with 403 rather than 404.
		return http.StatusForbidden
	}

	// Anonymous: 401 with a challenge, because reaching the gate at all means the
	// campaign is not a secret and signing in would help.
	return http.StatusUnauthorized
}

// TestTheOriginBranchesAreBothAnswers is the policy itself, over both directions.
//
// `Origin` is a **browser** header, so the two cases answer different questions: is
// there a browser on another site asking, and is there a browser at all. A table over
// one boolean would read as a single switch, which is the mistake ADR 0010's argument
// warns about. So:
//
//   - **Present.** It must be an `http` or `https` origin on this host. `null` — what
//     a sandboxed iframe and a `file://` page send — is refused for naming no host at
//     all. `ws://` on this very host is the row only a scheme check can catch: its
//     host matches, so a handler comparing hosts alone admits it. And `https` on this
//     host must be **admitted**, because a deployment terminating TLS in front of this
//     process sends a browser an `https` origin its own certificate implies, and
//     comparing the scheme to `http` would break it.
//   - **Absent.** Admitted, and deliberately: the clients that legitimately send no
//     `Origin` are non-browser ones — a test harness, a server-side integration, an
//     operator at a `wscat` prompt, the demo check — and the absence is a decision
//     rather than an accident of not checking. A test that only asserted refusals
//     could not tell the difference, so the affirmative is asserted with a socket
//     that opens.
func TestTheOriginBranchesAreBothAnswers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		origin func(host string) string
		want   int
	}{
		{
			name:   "another site",
			origin: func(string) string { return "http://elsewhere.example" },
			want:   http.StatusForbidden,
		},
		{
			name:   "this host on another port",
			origin: func(host string) string { return "http://" + hostname(host) + ":9999" },
			want:   http.StatusForbidden,
		},
		{
			name:   "an opaque origin",
			origin: func(string) string { return "null" },
			want:   http.StatusForbidden,
		},
		{
			name:   "a websocket-scheme origin on this host",
			origin: func(host string) string { return "ws://" + host },
			want:   http.StatusForbidden,
		},
		{
			name:   "a file origin",
			origin: func(string) string { return "file://" },
			want:   http.StatusForbidden,
		},
		{
			// The deployment behind a TLS-terminating proxy. A browser on that
			// deployment sends `https://thishost`, and a comparison against `http`
			// would refuse every socket in it.
			name:   "this host over https",
			origin: func(host string) string { return "https://" + host },
			want:   http.StatusSwitchingProtocols,
		},
		{
			name:   "this host over http",
			origin: func(host string) string { return "http://" + host },
			want:   http.StatusSwitchingProtocols,
		},
		{
			// Case-insensitivity, which is a row of its own because a comparison with
			// `==` on a host an operator typed in mixed case refuses a socket in their
			// own instance.
			name:   "this host in mixed case",
			origin: func(host string) string { return "http://" + strings.ToUpper(host) },
			want:   http.StatusSwitchingProtocols,
		},
		{
			name:   "absent, the non-browser branch",
			origin: func(string) string { return "" },
			want:   http.StatusSwitchingProtocols,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			host := strings.TrimPrefix(h.server.URL, "http://")

			header := http.Header{}
			if origin := testCase.origin(host); origin != "" {
				header.Set("Origin", origin)
			}

			if testCase.want == http.StatusSwitchingProtocols {
				//nolint:bodyclose // `dial` drains and closes the body on failure.
				conn, resp, err := h.dial(publicSlug, readers["gm"], header)
				if err != nil {
					t.Fatalf("a %s was refused with %d; want it admitted",
						testCase.name, statusOf(resp))
				}

				t.Cleanup(func() { _ = conn.CloseNow() })

				return
			}

			if got := h.dialStatus(publicSlug, readers["gm"], header); got != testCase.want {
				t.Errorf("%s = %d, want %d", testCase.name, got, testCase.want)
			}
		})
	}
}

// hostname drops the port from a host:port pair.
func hostname(hostport string) string {
	before, _, found := strings.Cut(hostport, ":")
	if !found {
		return hostport
	}

	return before
}

// scriptedStore is an in-memory `campaigns.Store` **and** an `identity.Reader`.
//
// One type implementing both because the fixture is one instance of the product —
// campaigns with memberships and sessions in one store — and two types would be two
// fixtures that could disagree about which users exist.
type scriptedStore struct {
	mu          sync.Mutex
	campaigns   map[string]domain.Campaign
	memberships map[int64]map[int64]domain.Membership
	sessions    map[string]store.AuthSession
	users       map[int64]domain.User
	// tokens is the raw token per user id, so a request can be sent as that user
	// without the test carrying the value around.
	tokens map[int64]string
}

// readers is the fixture's identities, keyed by a short label.
//
// `anonymous` is a real entry rather than a zero value because the table over
// identities has to include it, and "no cookie" is the shape it takes on the wire.
var readers = map[string]reader{
	"anonymous": {name: "an anonymous reader", anonymous: true},
	"gm":        {name: "the GM", id: gmUser},
	"player":    {name: "a player", id: playerUser},
	"outsider":  {name: "an authenticated non-member", id: outsiderUser},
	"admin":     {name: "an instance administrator", id: adminUser, admin: true},
	"editor":    {name: "a member with an unknown role", id: editorUser},
}

// newScriptedStore builds the campaigns, the memberships, the users and one live
// session per identified reader.
//
// A public campaign with a GM and a player; a private campaign with a GM; and a
// private campaign whose only member is the `editor`. `reader` mints a real session
// token per user so the chain under test is the credential path and not a
// context injection.
func newScriptedStore() *scriptedStore {
	backing := &scriptedStore{
		campaigns:   make(map[string]domain.Campaign),
		memberships: make(map[int64]map[int64]domain.Membership),
		sessions:    make(map[string]store.AuthSession),
		users:       make(map[int64]domain.User),
		tokens:      make(map[int64]string),
	}

	backing.campaigns[publicSlug] = domain.Campaign{
		ID: publicCampaign, Slug: publicSlug, Name: "The Gilded Cage",
		Visibility: domain.VisibilityPublic,
		SystemID:   "5e-2024",
	}
	backing.campaigns[privateSlug] = domain.Campaign{
		ID: privateCampaign, Slug: privateSlug, Name: "The Hollow Choir",
		Visibility: domain.VisibilityPrivate,
		SystemID:   "5e-2024",
	}
	backing.campaigns[foreignSlug] = domain.Campaign{
		ID: foreignCampaign, Slug: foreignSlug, Name: "The Drowned Lighthouse",
		Visibility: domain.VisibilityPrivate,
		SystemID:   "5e-2024",
	}

	backing.memberships[publicCampaign] = map[int64]domain.Membership{
		gmUser:     {CampaignID: publicCampaign, UserID: gmUser, Role: domain.RoleGM},
		playerUser: {CampaignID: publicCampaign, UserID: playerUser, Role: domain.RolePlayer},
	}
	backing.memberships[privateCampaign] = map[int64]domain.Membership{
		gmUser: {CampaignID: privateCampaign, UserID: gmUser, Role: domain.RoleGM},
	}
	// The role this build has no name for. A real state after a downgrade that
	// removed a role, and the row S-6.5's "there is no editor role" is about.
	backing.memberships[foreignCampaign] = map[int64]domain.Membership{
		editorUser: {
			CampaignID: foreignCampaign,
			UserID:     editorUser,
			Role:       domain.Role("editor"),
		},
	}

	// One user and one live session per identified reader. The token is minted here
	// and kept against the user id, so a test asks the store for it rather than
	// carrying one around.
	for name, who := range readers {
		if who.anonymous {
			continue
		}

		backing.users[who.id] = domain.User{
			ID: who.id, Username: name, IsAdmin: who.admin,
		}

		token, _, err := auth.NewSessionToken()
		if err != nil {
			panic("mint a session token for " + name + ": " + err.Error())
		}

		backing.sessions[auth.HashSessionToken(token)] = store.AuthSession{
			TokenHash: auth.HashSessionToken(token),
			UserID:    who.id,
			ExpiresAt: time.Now().Add(time.Hour),
		}

		backing.tokens[who.id] = token
	}

	return backing
}

// tokenFor returns the session token this store minted for a user.
//
// `t.Fatalf` rather than an error return: a missing token is a fixture that has
// drifted from the reader table, and there is nothing a caller could do about it.
func (s *scriptedStore) tokenFor(t *testing.T, userID int64) string {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	token, found := s.tokens[userID]
	if !found {
		t.Fatalf("the fixture holds no session for user %d; the reader table and the "+
			"session table have drifted apart", userID)
	}

	return token
}

func (s *scriptedStore) CampaignBySlug(
	_ context.Context,
	slug string,
) (domain.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	campaign, found := s.campaigns[slug]
	if !found {
		return domain.Campaign{}, store.ErrNotFound
	}

	return campaign, nil
}

func (s *scriptedStore) Membership(
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

// CampaignsForUser is the document's navigation, so it lists what the fixture holds.
func (s *scriptedStore) CampaignsForUser(
	_ context.Context,
	userID int64,
) ([]domain.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	listed := make([]domain.Campaign, 0, len(s.campaigns))

	// `rangeValCopy`: `s.campaigns` is a `map[string]domain.Campaign`, so a range
	// value cannot be addressed, and `listed` holds whole rows because the caller
	// sorts and renders them. Copying 128 bytes per campaign is the price of that
	// contract and there is no indexing form available to avoid it.
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

func (s *scriptedStore) CreateCampaign(
	context.Context,
	domain.Campaign,
) (domain.Campaign, error) {
	return domain.Campaign{}, store.ErrConflict
}

func (s *scriptedStore) CreateMembership(
	context.Context,
	domain.Membership,
) (domain.Membership, error) {
	return domain.Membership{}, store.ErrConflict
}

func (s *scriptedStore) DeleteCampaign(context.Context, int64) error {
	return store.ErrNotFound
}

func (s *scriptedStore) SessionByTokenHash(
	_ context.Context,
	tokenHash string,
) (store.AuthSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, found := s.sessions[tokenHash]
	if !found {
		return store.AuthSession{}, store.ErrNotFound
	}

	return session, nil
}

func (s *scriptedStore) UserByID(_ context.Context, id int64) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, found := s.users[id]
	if !found {
		return domain.User{}, store.ErrNotFound
	}

	return user, nil
}

// newStateRegistry returns a `realtime.Registry` over a real SQLite transaction.
//
// `Registry.Open` writes through a `*sql.Tx`, so a fake writer passing `nil` would be
// asserting against a shape the product never runs — and the DDL is the one
// `realtime`'s own statement names. A bare `sql.Open` also sidesteps `store.Open`'s
// process-wide claim (ADR 0004), which is what lets every test in this file be
// parallel.
func newStateRegistry(t *testing.T) *realtime.Registry {
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
		Read: func(_ context.Context, _ int64) (realtime.Persisted, error) {
			return realtime.Persisted{}, realtime.ErrNoState
		},
	})
}

// The one assertion about the cookie path itself, kept next to the routes because it
// is what makes the rest of the file about a *credential* rather than about a header.
//
// `identity.Authenticate` never fails a request: a missing, malformed, unknown,
// expired or revoked cookie are five facts that all mean "this request is
// anonymous", and a middleware that could tell them apart is the account-enumeration
// oracle `store.ErrNotFound` is written to refuse. So a reader presenting a garbage
// cookie is anonymous, not a 401 and not an error — and a socket opened with one is
// refused by the gate rather than by the credential.
func TestAGarbageCookieIsAnonymousRatherThanAnError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	header := http.Header{}
	header.Set("Cookie", auth.SessionCookieName+"=not-a-real-token")

	got := h.dialStatus(publicSlug, readers["anonymous"], header)
	if got != http.StatusUnauthorized {
		t.Errorf("a socket opened with a garbage cookie = %d, want 401: the credential "+
			"resolved to anonymous and the gate is what answered", got)
	}

	// And with no cookie at all the answer is the same number, which is the property:
	// the two are indistinguishable to anyone not already holding a session.
	if anonymous := h.dialStatus(publicSlug, readers["anonymous"], nil); anonymous != got {
		t.Errorf("a garbage cookie answered %d where no cookie answered %d; a reader "+
			"can tell an unknown session from an absent one", got, anonymous)
	}

	if peers := h.hub.Stats().Peers; peers != 0 {
		t.Errorf("hub.Stats().Peers = %d after two refused upgrades, want 0", peers)
	}
}
