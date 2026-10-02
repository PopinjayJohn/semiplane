package rules

import (
	"fmt"
	"slices"
)

// MaxOpArgsLen bounds an intent's arguments and a mutation's payload at 8 KiB.
//
// A second bound beside the codec's, and both are enforced — the argument here is
// `protocol.go`'s, which draws the same line between the limits that are properties
// of the bytes and the limits that belong to the transport: a limit enforced in one
// of two places is a limit one caller routes around.
//
// So this one is the *system's* limit and the codec's `maxArgsLen` is the *frame's*,
// and a payload within this bound is not thereby within the frame's. The adapter
// checks both; the smaller wins. 8 KiB is a stat block or a resolved roll, not a
// document — and the reason the bound exists at all is that an unbounded opaque
// payload is unbounded by definition, since this package does not interpret it and
// therefore cannot know what is in it.
const MaxOpArgsLen = 8 << 10

// ErrOpArgsTooLarge is an intent's arguments or a mutation's payload over
// `MaxOpArgsLen`.
//
// One sentinel for both directions, because both are the same bound on the same
// opaque payload and a caller asking "was it too big?" does not care which side of
// the resolution it was on.
var ErrOpArgsTooLarge = fmt.Errorf(
	"%w: the operation payload is over the bound",
	ErrMalformedSystem,
)

// Mutation is one change a system wants made to the tabletop.
//
// **It is the system's statement, not the hub's receipt.** The hub's own
// `realtime.Mutation` carries a placement, a version and a revision and nothing
// else, because the caller that requested the change already knows what it asked
// for; this one carries what changed, in the system's own words, and deliberately
// carries **no version and no revision**. S-7.2 makes the version the only ordering
// authority and `state.go` stamps it after the caller's function returns, over
// whatever the caller's function left — so a system that could name a version could
// forge currency, and a mutation carrying one would be a second answer to "how
// current is this placement".
//
// The translation is the adapter's, and it is one line in each direction:
//
//	rules.Mutation   →  realtime.Change{Placement, Op, Args, Version, By}
//	  .Target          →    .Placement          (the adapter's conversion)
//	  .Op              →    .Op                 (the system's vocabulary)
//	  .Args            →    .Args               (opaque, copied)
//	  (nothing)        →    .Version            (stamped by CampaignState.Mutate)
//	  (nothing)        →    .By                 (the actor, which the Context carries)
//
// The two omissions are the safety. Everything the hub owns — the version, the
// revision, the actor — arrives from the hub, and everything the system owns — the
// op, the payload — arrives from the system, and neither can forge the other's half.
type Mutation struct {
	// Target is the game object this changes, and it is meaningful for a removal
	// too: the id is what a client needs in order to drop its copy, and it is no
	// longer in the state.
	Target ObjectID

	// Op names the change in the system's own vocabulary, which is also the op the
	// broadcast carries.
	//
	// It is **not** the intent's op by default. One intent can resolve to several
	// mutations, and an attack that applies damage and then removes a marker names
	// two things that happened. Reusing the intent's name for both would put a
	// second record of the same fact on the wire, free to drift from what the
	// system actually did.
	//
	// The exception is a mutation that is exactly the operation asked for — a token
	// moved, hit points set — which is most of them, and there the same name is not
	// a second record but the record.
	Op Op

	// Args is the resolver-shaped payload, opaque to semiplane.
	//
	// This is what a client is told happened, and it is where a roll's result
	// belongs: S-7.3 makes the server authoritative for a roll precisely because
	// the answer travels here. That is also why it must never reach a log: S-12.3
	// forbids an event carrying dice results, and an opaque payload is the field
	// such a value would leak through. `Mutation.String` therefore prints the op and
	// the target and stops.
	Args []byte

	// Remove reports that the target leaves the tabletop, rather than being changed.
	//
	// A flag and not an op, because removal is not a normal mutation: the hub's
	// removal path stamps a version that outlives the placement so a late client can
	// still reconcile against it. Deciding that from an op's name would put that
	// distinction in a string, and a system that spelled it `delete_token` would get
	// a removal's version semantics on a placement that still exists.
	Remove bool
}

// NewMutation returns a mutation, refusing one the hub could not apply.
//
// The same refusals as `NewIntent`, for the same reasons, plus the target: a
// mutation with no target names nothing, and the hub's mutation path is per
// placement — there is no "campaign-level mutation" in this type, because a
// campaign-level change (a pause, an initiative reset) is expressed as an intent
// whose resolution is *no mutations* plus whatever the system can express about
// objects.
func NewMutation(target ObjectID, operation Op, args []byte) (Mutation, error) {
	if !target.Valid() {
		return Mutation{}, fmt.Errorf("%w: %q", ErrInvalidObject, target)
	}

	if !operation.Valid() {
		return Mutation{}, fmt.Errorf("%w: %q", ErrInvalidOp, operation)
	}

	if len(args) > MaxOpArgsLen {
		return Mutation{}, fmt.Errorf("%w: %d bytes, maximum is %d",
			ErrOpArgsTooLarge, len(args), MaxOpArgsLen)
	}

	return Mutation{Target: target, Op: operation, Args: slices.Clone(args)}, nil
}

// Valid reports whether the mutation is shaped like one the hub could apply.
//
// The zero `Mutation` is **invalid** — no target, no op — for the reason `Intent`'s
// zero value is: a caller that forgot to build one should be told rather than
// handing the hub a change with no address, which is a broadcast nobody can
// reconcile.
func (m Mutation) Valid() bool {
	return m.Target.Valid() && m.Op.Valid() && len(m.Args) <= MaxOpArgsLen
}

// String renders the mutation for a log line.
//
// **Never the payload.** See the `Args` comment: it is opaque to semiplane and
// S-12.3 forbids an event carrying dice results, so the worst thing this can print
// is an op name, an object id and the word `removed` — all of them plugin-authored
// identifiers, all bounded, none of them from a vault.
func (m Mutation) String() string {
	if m.Remove {
		return "remove " + m.Op.String() + " " + m.Target.String()
	}

	return m.Op.String() + " " + m.Target.String()
}
