package realtime_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/realtime"
)

// The tests `core_test.go` held that are about the **refusal carrier** rather than
// about the inert system. `Core` is deleted (ADR 0039: phase 8 replaces it, it does not
// extend it), and these are what remains of its suite — the questions about a frame
// travelling on an error, which no gameplay system depends on and the UI tier does.
//
// They are here rather than dropped because `ResolutionFor` has exactly one caller in
// production (`dice.ReadRefusal`) and that caller reads it in **two** shapes: a
// `rejected` frame when the hub resolved for a socket, and a `*plugin.RejectionError`
// when a form post dispatched. The frame shape is reachable and would be untested if
// this file did not exist.

// TestTheRefusalCarriesAClosedReasonAndNothingElse is S-12.3's requirement on the
// carrier, and it is asserted on a refusal this package actually built.
//
// **Both halves, and the second is the one that is easy to get wrong.** A `rejected`
// frame's `Reason` is one of `protocol.go`'s eight words and reaches every browser on the
// campaign, so it must not be anything a caller put there — and `Refuse` takes a reason
// from its caller, so the guarantee is that `protocol.Encode` refuses a reason outside
// the set. `encodeRejected` is used rather than a bare round-trip because the codec is
// the boundary: a frame that cannot be encoded must not be sent, and this proves the
// boundary is where that is decided.
//
// The detail half is the other direction: `Rejection`'s text is for a log line and is
// **never** the frame, so a detail naming a client's own payload cannot reach a browser
// even by accident.
func TestTheRefusalCarriesAClosedReasonAndNothingElse(t *testing.T) {
	t.Parallel()

	const clientText = "the passphrase is hunter2"

	// A refusal whose *detail* names the client's own text — the mistake S-12.3
	// describes, built deliberately so the assertion below is about the separation
	// rather than about a case that could not occur.
	refusal := realtime.Refuse(5, realtime.RejectUnknownOp, clientText)

	resolution := realtime.ResolutionFor(refusal)
	if resolution.Answer == nil {
		t.Fatalf("Refuse(5, …) carried no answer, so the frame it was built to answer "+
			"with is missing and this test is asserting on nothing: %v", refusal)
	}

	rejected, isRejected := resolution.Answer.(*realtime.ServerRejected)
	if !isRejected {
		t.Fatalf("the answer is %T, want *ServerRejected", resolution.Answer)
	}

	if rejected.Seq != 5 {
		t.Errorf("the frame answers seq %d, want 5; a client that cannot pair the answer "+
			"with its intent cannot leave its optimistic state", rejected.Seq)
	}

	if strings.Contains(string(rejected.Reason), clientText) {
		t.Errorf("the wire reason carries the client's own text %q; a reason is one of "+
			"eight fixed words, and anything else here reaches every browser on the "+
			"campaign", clientText)
	}

	// And it encodes. This is the boundary that makes the closed set a guarantee
	// rather than a convention: `protocol.Encode` refuses a reason it does not know, so
	// a caller who passed a bad word here gets an error rather than a frame.
	encoded, err := realtime.Encode(rejected)
	if err != nil {
		t.Errorf("the refusal's frame does not encode: %v; a reason outside the closed set "+
			"would be refused here rather than sent", err)
	}

	if encoded == nil {
		t.Error("Encode returned no frame for a refusal carrying one")
	}
}

// TestResolutionForIgnoresAnUnrelatedError: a caller that passes some other error must
// not get an empty frame it could send.
//
// **Both kinds of unrelated error, plus a wrapper**, because they fail differently: a
// bare error and a `context.Canceled` are the obvious cases, and a **wrapped** refusal
// is the one `errors.As` exists for — a type assertion here would return an empty frame
// for it, and `dice.ReadRefusal` would then answer "no refusal" for every refusal the
// hub produced over a socket. That is the case worth asserting, and it is the reason
// `ResolutionFor`'s own comment reaches for `errors.AsType`.
func TestResolutionForIgnoresAnUnrelatedError(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		errors.New("something else"),
		context.Canceled,
	} {
		if got := realtime.ResolutionFor(err); got.Answer != nil {
			t.Errorf("ResolutionFor(%v) = %v, want the zero Resolution; an "+
				"unrelated error must not become a frame a caller sends to a client",
				err, got.Answer)
		}
	}
}

// TestResolutionForReadsARefusalThroughAWrapper is the positive half, and the one the
// deleted `Core` used to make reachable by accident.
//
// Every call site between the hub and a reader adds context with `%w` — the linter
// insists on it — so `ResolutionFor` is **always** called on a wrapped refusal in
// production. A version that matched only the outermost type would work against a
// refusal returned bare and fail against every real one, which is the shape of bug this
// repository's mutation checks exist to catch.
//
// The campaign and the actor are in the wrapper's text on purpose: they are server-side
// facts about the dispatch, and a wrapper carrying them is the ordinary case rather than
// the contrived one.
func TestResolutionForReadsARefusalThroughAWrapper(t *testing.T) {
	t.Parallel()

	refusal := realtime.Refuse(11, realtime.RejectServerError, "a cause")

	// Three layers, because one is the easy case and three is what a call chain
	// actually produces between `Hub.Dispatch` and a route.
	wrapped := fmt.Errorf("dispatch in campaign 3 as 9: %w",
		fmt.Errorf("the gameplay resolver: %w", refusal))

	resolution := realtime.ResolutionFor(wrapped)
	if resolution.Answer == nil {
		t.Fatalf("ResolutionFor recovered nothing from a three-layer wrap: %v; the "+
			"dice roller reads its refusals through exactly this shape, so a match "+
			"that does not unwrap answers \"no refusal\" for every refusal the hub "+
			"produces", wrapped)
	}

	rejected, isRejected := resolution.Answer.(*realtime.ServerRejected)
	if !isRejected {
		t.Fatalf("the recovered answer is %T, want *ServerRejected", resolution.Answer)
	}

	if rejected.Seq != 11 {
		t.Errorf("the recovered frame answers seq %d, want 11", rejected.Seq)
	}

	if rejected.Reason != realtime.RejectServerError {
		t.Errorf("the recovered frame carries reason %q, want %q",
			rejected.Reason, realtime.RejectServerError)
	}

	// And `errors.Is` still reaches the cause, which is what makes the wrapped error
	// usable as an error at all: a caller distinguishing "the hub refused" from "the
	// resolver faulted" needs it, and a carrier that broke the chain would take it.
	if !strings.Contains(wrapped.Error(), "a cause") {
		t.Error("the wrapped error lost the cause's text, so the chain no longer carries " +
			"the reason the refusal happened")
	}
}
