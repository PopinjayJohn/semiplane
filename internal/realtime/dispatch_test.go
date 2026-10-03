package realtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/realtime"
)

// recordingResolver answers every intent with a fixed set of changes and a fixed
// error, and remembers what identity it was called with.
//
// The identity recording is the point. A dispatch seam that resolved correctly but
// resolved *as the wrong actor* would still broadcast a plausible-looking delta, and
// every assertion about "the server decides who this is" would pass, because a
// resolver here would not care. Recording it makes a substituted campaign or role
// visible rather than silent.
type recordingResolver struct {
	answer  realtime.ServerFrame
	changes []realtime.Change
	err     error

	calls []realtime.Intent
}

func (r *recordingResolver) Resolve(
	_ context.Context,
	intent realtime.Intent,
) (realtime.Resolution, error) {
	r.calls = append(r.calls, intent)

	if r.err != nil {
		return realtime.Resolution{}, r.err
	}

	return realtime.Resolution{Answer: r.answer, Broadcast: r.changes}, nil
}

// lastCall returns the identity the resolver was last called with, failing if it was
// never called.
func (r *recordingResolver) lastCall(t *testing.T) realtime.Intent {
	t.Helper()

	if len(r.calls) == 0 {
		t.Fatal("the resolver was never called")
	}

	return r.calls[len(r.calls)-1]
}

// oneChange is the smallest change set that is observably a broadcast.
func oneChange() []realtime.Change {
	return []realtime.Change{{
		Placement: "p1",
		Version:   7,
		Op:        "move_token",
	}}
}

// moveToken is the frame most of these tests dispatch. `placement` lives inside
// `args`, not beside `op`: the codec rejects a field it does not know, so a frame
// written the way the doc comment at `ClientIntent` abbreviates it would be refused
// at `Decode` and the test would be asserting the codec rather than the seam.
func moveToken(t *testing.T, seq realtime.ClientSeq) *realtime.ClientIntent {
	t.Helper()

	frame := fmt.Sprintf(
		`{"t":"intent","seq":%d,"op":"move_token","args":{"placement":"p1"}}`, seq,
	)

	return decodeIntent(t, frame)
}

// TestADispatchNeedsNoConnection is the capability the seam exists for.
//
// It is the DoD claim "a UI plugin attempting to mutate state cannot, and no write
// path is reachable" made reachable in the first place: a UI plugin dispatches
// intents, so whether a dispatch can happen without a socket is not academic. Before
// `Dispatch` existed the only answer was no, and the plugin tier shipped with an
// `Emitter` interface and no way to implement it.
func TestADispatchNeedsNoConnection(t *testing.T) {
	resolve := &recordingResolver{changes: oneChange()}
	hub, _ := newTestHub(t, resolve)

	// A peer that will observe the broadcast without having sent anything. If the
	// dispatch only reached its own caller, this is what would go unfed.
	watcher := join(t, hub, 1)

	got, err := hub.Dispatch(t.Context(), gmActor(t, 1), moveToken(t, 1))
	if err != nil {
		t.Fatalf("Dispatch() error = %v, want nil", err)
	}

	if len(got.Broadcast) != 1 {
		t.Fatalf("Broadcast has %d changes, want 1", len(got.Broadcast))
	}

	// The watcher, who sent nothing, is told. A dispatch that resolved and returned
	// without publishing passes every assertion above. Compared as bytes against
	// `Encode`'s own output, which is the idiom `hub_test.go`'s fan-out tests use:
	// decoding into a local struct would be a second struct over a frame, and a
	// second struct is a second answer to "what is a delta".
	want, err := realtime.Encode(&realtime.ServerDelta{
		Type:    realtime.TypeDelta,
		Changes: oneChange(),
	})
	if err != nil {
		t.Fatalf("Encode() error = %v, want nil", err)
	}

	if got := receive(t, watcher.Broadcasts()); !bytes.Equal(got, want) {
		t.Errorf("the watcher received %s, want %s", got, want)
	}

	// The identity came from the Actor, not from the frame. `ClientIntent` has no
	// campaign field and one must never be added: a frame that named its own
	// campaign would be a frame that chose which table it was playing on.
	call := resolve.lastCall(t)

	if call.Campaign != 1 || call.Actor != 7 || call.Role != domain.RoleGM {
		t.Errorf(
			"resolved as (campaign=%d actor=%d role=%q), want (1, 7, %q)",
			call.Campaign, call.Actor, call.Role, domain.RoleGM,
		)
	}
}

// TestADispatchCarriesTheServerDerivedIdentityUnchanged is the property the whole type
// rests on, asserted per field rather than as a whole.
//
// The failure it catches is a future refactor that rebuilds the `realtime.Intent` from
// the frame, drops the role, or defaults the campaign. Each of those compiles, and
// each looks like an optimisation.
func TestADispatchCarriesTheServerDerivedIdentityUnchanged(t *testing.T) {
	resolve := &recordingResolver{}
	hub, _ := newTestHub(t, resolve)

	// Campaign 2 is not campaign 1, and `insertSecondCampaign` exists so a test can
	// tell the two apart. A dispatch that resolved against "the" campaign would be
	// otherwise indistinguishable.
	actor, err := realtime.NewActor(secondCampaignID, 42, domain.RolePlayer)
	if err != nil {
		t.Fatalf("NewActor() error = %v, want nil", err)
	}

	if _, err := hub.Dispatch(t.Context(), actor, moveToken(t, 1)); err != nil {
		t.Fatalf("Dispatch() error = %v, want nil", err)
	}

	call := resolve.lastCall(t)

	switch {
	case call.Campaign != secondCampaignID:
		t.Errorf("Campaign = %d, want %d", call.Campaign, secondCampaignID)
	case call.Actor != 42:
		t.Errorf("Actor = %d, want 42", call.Actor)
	case call.Role != domain.RolePlayer:
		t.Errorf("Role = %q, want %q", call.Role, domain.RolePlayer)
	}
}

// TestARefusalPublishesNothingAndCarriesItsReason is the half of the contract a
// refusal gets wrong most easily.
//
// A refusal that broadcast would tell the table something changed when nothing did,
// and the client that believed it would hold a version for a placement that never
// moved, which is the exact divergence `state.go`'s per-placement version exists to
// prevent. So the assertion is on the *absence* of a frame, which is the harder one to
// write and the one worth writing.
func TestARefusalPublishesNothingAndCarriesItsReason(t *testing.T) {
	resolve := &recordingResolver{
		err: &realtime.RejectionError{
			Reason: realtime.RejectNotPermitted,
			Err:    errors.New("a player may not do that"),
		},
	}

	hub, _ := newTestHub(t, resolve)
	watcher := join(t, hub, 1)

	got, err := hub.Dispatch(t.Context(), playerActor(t, 1), moveToken(t, 1))
	if err == nil {
		t.Fatal("Dispatch() error = nil, want the refusal")
	}

	// The reason travels as a value a UI plugin can render rather than as a frame
	// nobody would read, which is why `Dispatch` returns an error instead of a
	// `ServerRejected`. Only a peer has a `seq` to reject.
	if reason := reasonOf(err); reason != realtime.RejectNotPermitted {
		t.Errorf("reason = %q, want %q", reason, realtime.RejectNotPermitted)
	}

	if len(got.Broadcast) != 0 {
		t.Errorf("a refusal returned %d changes, want none", len(got.Broadcast))
	}

	// Nothing was published. `expectNothing` bounds the window explicitly rather than
	// relying on the test finishing before a broadcast would have arrived, which is
	// the assertion that silently stops testing anything the moment fan-out slows.
	expectNothing(t, watcher.Broadcasts())
}

// TestDispatchRefusesAnActorItCannotResolveAgainst is the zero-value guard.
//
// An `Actor` with no campaign is what a handler that forgot to read the campaign out
// of the URL path produces. Without this the dispatch would resolve against campaign
// zero and the resolver would be the only thing between that and whatever state
// campaign zero happens to have.
func TestDispatchRefusesAnActorItCannotResolveAgainst(t *testing.T) {
	// The zero value is the **only** unusable actor reachable from outside this
	// package, and that is a direct consequence of the fields being unexported: a
	// caller cannot spell `{campaign: -1}`, so the one bad value it can hold is the
	// one it gets by accident. `Dispatch` checks it, and this is the test for that
	// check — a dispatch that trusted it would resolve against campaign zero.
	t.Run("zero", func(t *testing.T) {
		resolve := &recordingResolver{changes: oneChange()}
		hub, _ := newTestHub(t, resolve)

		if _, err := hub.Dispatch(t.Context(), realtime.Actor{}, moveToken(t, 1)); err == nil {
			t.Fatal("Dispatch() error = nil, want a refusal")
		}

		// The refusal happened *before* the resolver. A dispatch that resolved first
		// and validated the campaign afterwards would still have called the system,
		// which is the thing this guard is actually for.
		if len(resolve.calls) != 0 {
			t.Errorf("the resolver was called %d times, want 0", len(resolve.calls))
		}
	})

	// And the constructor refuses the value the unexported fields already prevent,
	// which is the other half: a caller cannot be *told* an actor was usable when it
	// was not.
	for name, campaign := range map[string]int64{"zero": 0, "negative": -1} {
		t.Run("NewActor/"+name, func(t *testing.T) {
			if _, err := realtime.NewActor(campaign, 7, domain.RoleGM); err == nil {
				t.Errorf("NewActor(%d) error = nil, want a refusal", campaign)
			}
		})
	}
}

// TestApplyStillAnswersItsPeer is the regression guard on the refactor.
//
// `Apply` is now a thin wrapper, and the wrapper is what a WebSocket route calls for
// every intent a table sends. The two things it must keep doing are answering the
// peer's `seq` and, through `Dispatch`, publishing. A suite that only covered
// `Dispatch` would pass with both of those deleted.
func TestApplyStillAnswersItsPeer(t *testing.T) {
	intent := moveToken(t, 11)

	resolve := &recordingResolver{
		// The resolver answers the `seq` it was handed, which is the whole of what a
		// `ServerApplied` is: the pair. A resolver that answered no seq would compile
		// and would leave the peer waiting, which is why this constructs the answer
		// from `intent` rather than hard-coding `11` twice.
		answer: &realtime.ServerApplied{
			Type:      realtime.TypeApplied,
			Seq:       intent.Seq,
			Version:   7,
			Placement: "p1",
			Op:        "move_token",
			By:        viewerGM.ID,
		},
	}

	hub, _ := newTestHub(t, resolve)
	peer := join(t, hub, 1)

	if err := hub.Apply(t.Context(), peer, intent); err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}

	// Unmarshalled into a two-field struct rather than decoded into a
	// `ServerDelta`-shaped one, because the assertion is about which frame and which
	// seq, and a local struct declaring only what it looks at cannot quietly stop
	// matching when a field is added.
	var answered struct {
		Type realtime.Type      `json:"t"`
		Seq  realtime.ClientSeq `json:"seq"`
	}

	payload := receive(t, peer.Answers())

	if err := json.Unmarshal(payload, &answered); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v, want nil", payload, err)
	}

	if answered.Type != realtime.TypeApplied {
		t.Errorf("answered with %q, want %q", answered.Type, realtime.TypeApplied)
	}

	if answered.Seq != intent.Seq {
		t.Errorf("answered seq %d, want the intent's own %d", answered.Seq, intent.Seq)
	}

	if got := resolve.lastCall(t).Actor; got != viewerGM.ID {
		t.Errorf("resolved as actor %d, want the peer's %d", got, viewerGM.ID)
	}
}

// TestApplyRejectsItsPeerStill is here because the refactor moved that check.
//
// The nil-peer guard stays in `Apply` rather than relying on `Dispatch`'s campaign
// check: a nil peer has no campaign, so `Dispatch` would refuse it too, but for a
// reason that reads as "bad actor" rather than "no peer" and `Apply` would go on to
// dereference the nil peer on the rejection path.
func TestApplyRejectsItsPeerStill(t *testing.T) {
	resolve := &recordingResolver{}
	hub, _ := newTestHub(t, resolve)

	if err := hub.Apply(t.Context(), nil, moveToken(t, 1)); err == nil {
		t.Error("Apply(nil peer) error = nil, want a refusal")
	}

	if len(resolve.calls) != 0 {
		t.Errorf("the resolver was called %d times, want 0", len(resolve.calls))
	}
}

// TestAnActorCannotBeDecodedFromBytes is the forgery guard, and it is structural
// rather than procedural.
//
// `Actor`'s whole security argument is "every field is server-derived", and the
// obvious way to break that is a handler decoding a request body straight into one.
// "We do not do that" is a habit, and habits are what a future PR breaks.
//
// So the type refuses the decode instead of relying on the habit — and the assertion
// is on the *outcome*, not on the shape, because the shape alone was not enough. This
// test was written first against a type with unexported fields and no
// `UnmarshalJSON`, and it **failed**: `encoding/json` does not reject a struct it
// cannot populate. It returns no error and changes nothing, so the decode "succeeded",
// the actor came out zero, and the only thing standing between that and a dispatch as
// the zero actor was one layer away in `Dispatch` with a message about a campaign. A
// silent failure wearing a loud-looking one. Hence `UnmarshalJSON` on the type, and
// hence this test asserting the error rather than the absence of fields.
func TestAnActorCannotBeDecodedFromBytes(t *testing.T) {
	const body = `{"campaign":9,"user":7,"role":"gm"}`

	t.Run("Unmarshal", func(t *testing.T) {
		actor := gmActor(t, 1)

		if err := json.Unmarshal([]byte(body), &actor); err == nil {
			t.Error("json.Unmarshal into a realtime.Actor succeeded, want an error")
		}

		// And the value is untouched. A decoder that errored *and* partially populated
		// would leave an actor with a campaign and a stranger's user on it, which is
		// the forgery one layer removed from a dispatch.
		if got := actor; got != gmActor(t, 1) {
			t.Errorf("the actor changed to %+v despite the refusal", got)
		}
	})

	t.Run("Decoder", func(t *testing.T) {
		// The streaming path, because `json.Decoder.Decode` finds the same method but
		// a test that only covers `Unmarshal` would not notice if a future refactor
		// reached for a decoder instead.
		actor := gmActor(t, 1)

		if err := json.NewDecoder(strings.NewReader(body)).Decode(&actor); err == nil {
			t.Error("json.Decoder.Decode into a realtime.Actor succeeded, want an error")
		}

		if got := actor; got != gmActor(t, 1) {
			t.Errorf("the actor changed to %+v despite the refusal", got)
		}
	})

	t.Run("zero value", func(t *testing.T) {
		// The zero value is the one actor reachable without `NewActor`, so it gets
		// the same answer: a decode into it must not be the thing that makes it
		// usable.
		var actor realtime.Actor

		if err := json.Unmarshal([]byte(body), &actor); err == nil {
			t.Error("json.Unmarshal into a zero realtime.Actor succeeded, want an error")
		}
	})
}

// TestAnActorViewerIsThePeersOwnIdentity is the one-line consistency check between the
// two ways of being someone.
//
// `Actor` and `Viewer` carry the same two fields and `Peer.who` is a `Viewer`, so the
// conversion has to be the identity one. If it ever became anything else, a
// dispatch's snapshot addressing and a peer's would disagree about who the recipient
// is.
func TestAnActorViewerIsThePeersOwnIdentity(t *testing.T) {
	actor := gmActor(t, secondCampaignID)

	want := realtime.Viewer{ID: 7, Role: domain.RoleGM}
	if got := actor.Viewer(); got != want {
		t.Errorf("Viewer() = %+v, want %+v", got, want)
	}

	// `Campaign` is deliberately absent from `Viewer`: a snapshot's `you` addresses the
	// recipient, and which campaign is being watched is the frame's business, not the
	// addressee's. A `Viewer` carrying one would be a second place a campaign could be
	// named.
	if got := actor.Viewer(); got.ID != actor.User() || got.Role != actor.Role() {
		t.Errorf("Viewer() = %+v, want the actor's own id and role", got)
	}
}

// reasonOf pulls the wire reason out of an error, so the assertion above reads in
// terms of what a client would be told rather than in terms of the type.
func reasonOf(err error) realtime.RejectReason {
	// `AsType` rather than `errors.As` with a declared variable, which is this
	// package's own idiom in `hub.go` — and which matters here for a second reason:
	// `Dispatch` wraps the resolver's refusal, so the reason is only reachable by
	// unwrapping, and both spellings unwrap identically.
	if rejected, ok := errors.AsType[*realtime.RejectionError](err); ok {
		return rejected.Reason
	}

	return ""
}

// gmActor is `realtime.NewActor` for a GM, failing the test if the constructor refuses —
// which would mean the fixture is wrong rather than the code.
func gmActor(t *testing.T, campaign int64) realtime.Actor {
	t.Helper()

	actor, err := realtime.NewActor(campaign, 7, domain.RoleGM)
	if err != nil {
		t.Fatalf("NewActor() error = %v, want nil", err)
	}

	return actor
}

// playerActor is `gmActor` for a player, and exists so a test that is about the *role* does
// not have to restate the id and accidentally vary one of the three.
func playerActor(t *testing.T, campaign int64) realtime.Actor {
	t.Helper()

	actor, err := realtime.NewActor(campaign, 7, domain.RolePlayer)
	if err != nil {
		t.Fatalf("NewActor() error = %v, want nil", err)
	}

	return actor
}
