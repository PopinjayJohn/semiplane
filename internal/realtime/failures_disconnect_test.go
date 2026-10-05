package realtime_test

// Failure row §13 "Mass WS disconnect": presence cleanup happens, and a
// reconnect with `since` gets deltas rather than a full snapshot.
//
// The second half is covered elsewhere and is recorded here rather than
// re-asserted: `protocol_test.go`'s
// `TestASatisfiableSinceIsAnsweredWithADeltaAndAnUnsatisfiableOneWithASnapshot`
// and `TestResyncNeverAnswersAPartialDelta` pin the `Resync` decision, and
// `state_test.go`'s `TestAResumeAtTheCurrentRevisionReportsNothingChanged` pins
// the "nothing happened, send no snapshot" answer. Re-asserting either here
// would be a second answer to the same question.
//
// What nothing else pins is the first half at hub scale: many peers dropping at
// once — some of them behind, so their slots are full — must converge to an
// empty hub, must not trip the stale gauge, and must leave the hub usable. The
// gauge half matters because `ws.closed` and `ws.stale` are different signals in
// §13.2: an orderly leave that counted as stale would report a leak every time
// a table stood up from a finished game.
//
// Presence needs one sentence because the row names it: the hub keeps no
// presence roster. `protocol.go` makes presence ephemeral by construction — a
// cursor and a focus with no `seq` and nothing persisted — so "presence
// cleanup" is exactly peer removal. There is no second table to converge.

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/realtime"
)

// TestAMassDisconnectCleansUpEveryPeerAndLeavesTheStaleGaugeAlone is the
// orderly half of §13's mass-disconnect row.
//
// Sixteen peers across two campaigns, every one of them behind — two publishes
// per campaign with nobody reading, so the second finds every slot full — then
// all sixteen leave concurrently. The assertions are the cleanup, in the order
// an operator would check them: nobody is counted, no room outlives its last
// peer, no peer is left open, the stale gauge did not move, and the hub still
// works afterwards.
func TestAMassDisconnectCleansUpEveryPeerAndLeavesTheStaleGaugeAlone(t *testing.T) {
	hub, _ := newTestHub(t, nil)

	const perCampaign = 8

	peers := make([]*realtime.Peer, 0, 2*perCampaign)
	for range perCampaign {
		peers = append(peers, join(t, hub, 1), join(t, hub, secondCampaignID))
	}

	// Every peer behind: the first publish fills every slot, the second finds
	// them full. A mass disconnect of peers that had all drained would prove
	// nothing about the slot a disconnect is supposed to release.
	for _, campaign := range []int64{1, secondCampaignID} {
		for tick := range 2 {
			frame := clockDelta(epochClock.Add(time.Duration(tick) * time.Second))
			if err := hub.Publish(campaign, frame); err != nil {
				t.Fatalf("Publish(%d) error = %v, want nil", campaign, err)
			}
		}
	}

	// All at once. Sequential leaves are `TestJoinAndLeaveAreIdempotent`; the
	// failure this reaches is sixteen detaches racing on one lock, which is the
	// shape a real mass disconnect has.
	var done sync.WaitGroup

	for _, peer := range peers {
		done.Go(peer.Leave)
	}

	done.Wait()

	if got := hub.Stats().Peers; got != 0 {
		t.Errorf("Stats().Peers = %d after every peer left, want 0", got)
	}

	if got := hub.Stats().Campaigns; got != 0 {
		t.Errorf("Stats().Campaigns = %d after every peer left, want 0; an empty room "+
			"is a campaign the hub will never answer again", got)
	}

	// An orderly leave is not a stale peer. The sweep retires peers that stopped
	// reading; these peers were behind and then left cleanly, and counting them
	// as staled would make every finished game look like a leak in §13.2's
	// `ws.stale` gauge.
	if got := hub.Stats().Staled; got != 0 {
		t.Errorf("Stats().Staled = %d after an orderly mass leave, want 0; `ws.closed` "+
			"and `ws.stale` are different signals", got)
	}

	for index, peer := range peers {
		if !peer.Stats().Closed {
			t.Errorf("peer %d survived the mass disconnect", index)
		}

		// Drained first: the publishes above left a frame in every slot, and a
		// read before the drain returns that frame with `open` still true —
		// which would assert on the buffer rather than on the close.
		drain(t, peer.Broadcasts())

		if _, open := <-peer.Broadcasts(); open {
			t.Errorf("peer %d's broadcast slot is still open after it left", index)
		}
	}

	// And the hub still works: a room that was emptied is gone, not broken. A
	// reconnect joins cleanly and hears the next publish, and a publish to the
	// campaign nobody rejoined is a no-op rather than a send into a dead room.
	rejoined := join(t, hub, 1)

	published := clockDelta(epochClock)

	if err := hub.Publish(1, published); err != nil {
		t.Fatalf("Publish() after the mass disconnect error = %v, want nil", err)
	}

	if err := hub.Publish(secondCampaignID, published); err != nil {
		t.Fatalf("Publish() to the campaign nobody rejoined error = %v, want nil", err)
	}

	want, err := realtime.Encode(published)
	if err != nil {
		t.Fatalf("Encode() error = %v, want nil", err)
	}

	if got := receive(t, rejoined.Broadcasts()); !bytes.Equal(got, want) {
		t.Errorf("the rejoined peer holds %s, want %s", got, want)
	}

	if got := hub.Stats().Peers; got != 1 {
		t.Errorf("Stats().Peers = %d after one reconnect, want 1", got)
	}
}
