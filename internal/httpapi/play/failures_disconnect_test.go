package play_test

// Failure row §13 "Mass WS disconnect", socket side: presence cleanup happens
// over real connections.
//
// `internal/realtime`'s mass-disconnect test pins the hub's half — concurrent
// `Leave` converges, the stale gauge stays put, the hub stays usable. What the
// hub cannot observe is whether the route actually calls `Leave` when sockets
// go away: `hub.go`'s own header says the socket-side half of a peer's
// liveness is visible only through a socket. A route whose read loop forgot its
// deferred leave would pass every hub test and leak a peer per disconnect,
// which is the `ws.closed` gauge flatlining while the table is gone.
//
// So this test dials like a table — eight sockets, every one carrying a
// presence frame first, because presence is the ephemeral state the row names —
// drops them all at once, and requires the hub to converge to zero peers and
// zero campaigns. A reconnect afterwards must work, which is what proves the
// rooms were removed rather than wedged.

import (
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/semiplane/semiplane/internal/httpapi/play"
	"github.com/semiplane/semiplane/internal/realtime"
)

func TestAMassClientDisconnectReleasesEveryPeer(t *testing.T) {
	t.Parallel()

	// A read bound nothing here can reach. Every other socket test runs under
	// 250ms, and a connection that reaches it is closed by the route — which
	// would retire peers mid-measurement for a reason that has nothing to do
	// with the disconnect under test. The closes below are the clients', so the
	// bound is not load-bearing here; see
	// `TestAConnectionCostsOneReaderAndNothingElse` for the full argument.
	harness := newMounted(t, mount{
		resolve: &stubResolver{},
		handler: func(handler *play.Handler) { handler.ReadTimeout = time.Minute },
	})

	const batch = 8

	sockets := make([]*websocket.Conn, 0, batch)

	for range batch {
		conn := harness.dialAs(gmSlug, gmUserID)
		sockets = append(sockets, conn)

		// Presence first: the row names presence cleanup, and a peer that never
		// sent anything but the handshake is a weaker fixture for it. The
		// route answers presence with nothing — `TestHelloAndPresenceAreRouted`
		// pins that — so nothing here needs reading.
		writeRaw(t, conn, `{"t":"presence","args":{"cursor":[10,20]}}`)
	}

	waitFor(t, "every connection to join", func() bool {
		return harness.hub.Stats().Peers == batch
	})

	// All at once, from the clients' side: the shape a dropped network has.
	for _, conn := range sockets {
		_ = conn.CloseNow()
	}

	waitFor(t, "the hub to release every disconnected peer", func() bool {
		stats := harness.hub.Stats()

		return stats.Peers == 0 && stats.Campaigns == 0
	})

	// And a table that reconnects afterwards gets a working game, not a wedged
	// room: the join succeeds and an intent is answered.
	reconnected := harness.dialAs(gmSlug, gmUserID)

	writeIntent(t, reconnected, 1)

	first, _ := readFrame(t, reconnected)
	if first != string(realtime.TypeApplied) {
		t.Fatalf("the first frame after a reconnect = %q, want %q; the room the "+
			"mass disconnect emptied must accept a new game", first, realtime.TypeApplied)
	}

	if got := harness.hub.Stats().Peers; got != 1 {
		t.Errorf("Stats().Peers = %d after one reconnect, want 1", got)
	}
}
