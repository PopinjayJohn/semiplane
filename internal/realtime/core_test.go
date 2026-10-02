package realtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/realtime"
)

// coreFor is the inert system phase 7 runs against, built the way the composition
// root will build it.
func coreFor() realtime.Core {
	return realtime.Core{SystemID: "core", RulesetVersion: "sp1:system=core"}
}

// intentFor builds an intent the way `Hub.Apply` does: the campaign, actor and
// role are bound by the **server**, and only the frame comes from the client.
func intentFor(role domain.Role, seq uint64, op string) realtime.Intent {
	return realtime.Intent{
		Campaign: 1,
		Actor:    7,
		Role:     role,
		Frame: &realtime.ClientIntent{
			Type: realtime.TypeIntent,
			Seq:  realtime.ClientSeq(seq),
			Op:   realtime.Op(op),
		},
	}
}

// TestTheInertSystemRefusesEveryOperation is the whole contract, stated once.
//
// The system is inert because phase 8 is not written yet, and the value of it is
// that a real client can be driven through the protocol against it. A client that
// sends an operation must be **refused by name** — not ignored, not dropped, and
// not answered with silence, because a socket that accepts an intent and never
// answers it is a client stuck on a spinner with no way to learn why.
func TestTheInertSystemRefusesEveryOperation(t *testing.T) {
	t.Parallel()

	for _, op := range []string{"move_token", "roll", "pause", "set_hp", "advance_turn"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()

			_, err := coreFor().Resolve(t.Context(), intentFor(domain.RoleGM, 5, op))
			if err == nil {
				t.Fatalf("Resolve(%s) = nil error; the inert system resolves nothing, "+
					"so every operation is refused rather than silently dropped", op)
			}

			resolution := realtime.ResolutionFor(err)

			// The answer is the whole point: it carries the actor's `seq` back, so
			// the client can pair it with the intent it sent.
			rejected, ok := resolution.Answer.(*realtime.ServerRejected)
			if !ok {
				t.Fatalf("Resolve(%s) answer is %T, want *ServerRejected", op, resolution.Answer)
			}

			if rejected.Seq != 5 {
				t.Errorf("the refusal answers seq %d, want 5; a client that cannot "+
					"pair the answer with its intent cannot leave its optimistic state",
					rejected.Seq)
			}
		})
	}
}

// TestARefusalBroadcastsNothing is the security half of a refusal.
//
// A rejection that broadcast would tell the **whole campaign** that this client
// attempted something, and the reason travels on the wire — so every refused
// intent becomes a channel for telling a table what another member is trying. A
// refusal is between the actor and the server.
func TestARefusalBroadcastsNothing(t *testing.T) {
	t.Parallel()

	_, err := coreFor().Resolve(t.Context(), intentFor(domain.RoleGM, 1, "move_token"))

	resolution := realtime.ResolutionFor(err)
	if len(resolution.Broadcast) != 0 {
		t.Errorf("a refused intent broadcast %d change(s); a refusal is between the "+
			"actor and the server, and broadcasting it tells the campaign what "+
			"another member is attempting", len(resolution.Broadcast))
	}
}

// TestARejectedIntentStillCarriesAReason is what makes the refusal usable.
//
// `protocol.go` refuses to encode a reason outside its closed set, so a resolver
// that answered "no" would be rejected by its own codec — the refusal would be
// undeliverable rather than merely unhelpful.
func TestARejectedIntentStillCarriesAReason(t *testing.T) {
	t.Parallel()

	_, err := coreFor().Resolve(t.Context(), intentFor(domain.RoleGM, 1, "move_token"))

	rejected, ok := realtime.ResolutionFor(err).Answer.(*realtime.ServerRejected)
	if !ok {
		t.Fatal("the refusal carried no ServerRejected frame")
	}

	if rejected.Reason == "" {
		t.Error("the refusal carries no reason; `protocol.Encode` refuses a reason " +
			"outside the closed set, so an empty one would be undeliverable")
	}

	if rejected.Reason != realtime.RejectUnknownOp {
		t.Errorf("refusal reason = %q, want %q; this system resolves no operations, "+
			"so 'unknown' is true of every one of them", rejected.Reason,
			realtime.RejectUnknownOp)
	}
}

// TestAGMOnlyShapeIsRefusedToAPlayerBeforeTheUnknownOpRefusal is about *which*
// word comes back.
//
// The two checks are in a deliberate order, and the order is the test: a player
// attempting a GM-only operation is told `not_permitted` rather than
// `unknown_op`, because the first answers "is this allowed" about *them* and the
// second answers "is anything allowed" about the server. An observer could
// otherwise tell a player from a GM by which word came back, which is a role
// oracle on a wire anyone on the campaign can see.
func TestAGMOnlyShapeIsRefusedToAPlayerBeforeTheUnknownOpRefusal(t *testing.T) {
	t.Parallel()

	_, asPlayer := coreFor().Resolve(t.Context(), intentFor(domain.RolePlayer, 1, "pause"))
	_, asGM := coreFor().Resolve(t.Context(), intentFor(domain.RoleGM, 1, "pause"))

	player, okPlayer := realtime.ResolutionFor(asPlayer).Answer.(*realtime.ServerRejected)
	gm, okGM := realtime.ResolutionFor(asGM).Answer.(*realtime.ServerRejected)

	if !okPlayer || !okGM {
		t.Fatal("one of the two refusals carried no frame")
	}

	if player.Reason != realtime.RejectNotPermitted {
		t.Errorf("a player attempting `pause` is refused %q, want %q; the role check "+
			"runs first so the word answers about the actor rather than about the server",
			player.Reason, realtime.RejectNotPermitted)
	}

	if gm.Reason == player.Reason {
		t.Errorf("a GM and a player attempting the same operation both receive %q; the "+
			"wire would then carry a role oracle anyone on the campaign could read",
			gm.Reason)
	}
}

// TestAPlayerMayStillAttemptANonGMShape covers the other direction: the role
// check must not refuse everything, or `not_permitted` becomes the answer for every
// intent from a player and the rule says nothing.
func TestAPlayerMayStillAttemptANonGMShape(t *testing.T) {
	t.Parallel()

	_, err := coreFor().Resolve(t.Context(), intentFor(domain.RolePlayer, 1, "move_token"))

	rejected, ok := realtime.ResolutionFor(err).Answer.(*realtime.ServerRejected)
	if !ok {
		t.Fatal("the refusal carried no frame")
	}

	if rejected.Reason == realtime.RejectNotPermitted {
		t.Error("a player attempting `move_token` is told `not_permitted`; nothing " +
			"makes that operation GM-only, and a rule that refuses everything " +
			"distinguishes nothing")
	}
}

// TestTheOpIsShapeCheckedAndNeverMembershipChecked is the claim phase 8 depends
// on.
//
// The operation vocabulary belongs to the gameplay system. A list here would be a
// second source of truth that phase 8 contradicts, and a client would be refused
// as *unknown* for an operation phase 8 supports — a 5e client would be told its
// `roll` does not exist by a system that was only ever meant to be replaced.
//
// What can be checked without the vocabulary is the token's **shape**: non-empty,
// bounded, lowercase. That is what stops a megabyte of junk reaching the log and
// the error path.
func TestTheOpIsShapeCheckedAndNeverMembershipChecked(t *testing.T) {
	t.Parallel()

	core := coreFor()

	// Shape: refused, and refused as `invalid_args` rather than as unknown.
	for _, op := range []string{"", "Move_Token", "move token", "move-token", strings.Repeat("x", 65)} {
		_, err := core.Resolve(t.Context(), intentFor(domain.RoleGM, 1, op))

		rejected, ok := realtime.ResolutionFor(err).Answer.(*realtime.ServerRejected)
		if !ok {
			t.Fatalf("op %q: the refusal carried no frame", op)
		}

		if rejected.Reason != realtime.RejectInvalidArgs {
			t.Errorf("op %q is refused %q, want %q; a malformed token is a bad "+
				"argument, and calling it unknown would blame the vocabulary",
				op, rejected.Reason, realtime.RejectInvalidArgs)
		}
	}

	// Membership: an operation this system has never heard of is **not** refused as
	// malformed. It reaches the unknown-op refusal, which is the only honest word for
	// a system that resolves nothing.
	for _, op := range []string{"cast_fireball", "xyzzy", "q"} {
		_, err := core.Resolve(t.Context(), intentFor(domain.RoleGM, 1, op))

		rejected, ok := realtime.ResolutionFor(err).Answer.(*realtime.ServerRejected)
		if !ok {
			t.Fatalf("op %q: the refusal carried no frame", op)
		}

		if rejected.Reason == realtime.RejectInvalidArgs {
			t.Errorf("op %q is refused as a malformed token, so this system is "+
				"membership-checking an operation vocabulary; phase 8's would be "+
				"contradicted by a list here", op)
		}
	}
}

// TestANilIntentIsRefusedRatherThanPanicking: a resolver is called from a socket
// read loop, and a panic there takes the connection's goroutine with it.
func TestANilIntentIsRefusedRatherThanPanicking(t *testing.T) {
	t.Parallel()

	_, err := coreFor().Resolve(t.Context(), realtime.Intent{Campaign: 1, Actor: 7, Role: domain.RoleGM})
	if err == nil {
		t.Fatal("Resolve with no frame = nil error; want a refusal")
	}

	// And no frame is answered: a nil frame has no `seq` to echo, and answering
	// with an invented one would pair the refusal with an intent the client never
	// sent.
	if resolution := realtime.ResolutionFor(err); resolution.Answer != nil {
		t.Errorf("a refusal with no intent answered %v; there is no seq to answer",
			resolution.Answer)
	}
}

// TestTheRefusalErrorCarriesNoClientContent: the reason is on the wire and is one
// of eight fixed words, while the error text is for a log line. They are separate
// fields for exactly that reason — `AGENTS.md` records the invariant that no event
// carries secret content, and a resolver that poured a callout body into a
// rejection would put it in every browser on the campaign.
func TestTheRefusalErrorCarriesNoClientContent(t *testing.T) {
	t.Parallel()

	const secret = "the passphrase is hunter2"

	_, err := coreFor().Resolve(t.Context(), intentFor(domain.RoleGM, 1, secret))

	if err == nil {
		t.Fatal("want a refusal")
	}

	rejected, ok := realtime.ResolutionFor(err).Answer.(*realtime.ServerRejected)
	if !ok {
		t.Fatal("the refusal carried no frame")
	}

	if strings.Contains(string(rejected.Reason), secret) {
		t.Errorf("the wire reason carries the client's own text %q; a reason is one "+
			"of eight fixed words, and anything else here reaches every browser on "+
			"the campaign", secret)
	}
}

// TestResolutionForIgnoresAnUnrelatedError: a caller that passes some other error
// must not get an empty frame it could send.
func TestResolutionForIgnoresAnUnrelatedError(t *testing.T) {
	t.Parallel()

	if got := realtime.ResolutionFor(errors.New("something else")); got.Answer != nil {
		t.Errorf("ResolutionFor(unrelated) = %v, want the zero Resolution; an "+
			"unrelated error must not become a frame a caller sends to a client",
			got.Answer)
	}
}

// TestACancelledContextIsNotARefusal: `Resolve` takes a context and must honour
// it. A refusal and a cancellation are different facts, and answering a client
// whose connection is gone is work for nobody.
func TestACancelledContextIsNotARefusal(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := coreFor().Resolve(ctx, intentFor(domain.RoleGM, 1, "move_token"))

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Resolve on a cancelled context = %v, want context.Canceled", err)
	}

	if resolution := realtime.ResolutionFor(err); resolution.Answer != nil {
		t.Error("a cancelled context produced a frame; cancellation is not a refusal")
	}
}
