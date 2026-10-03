package conformance

import (
	"context"
	"errors"
	"fmt"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// explodingPanic is the value the controlled probe panics with.
//
// A string and not an error, deliberately: a plugin's most common panic value is a
// string, so the boundary is tested with the shape it will actually meet.
const explodingPanic = "conformance: a resolver that gives up half way through"

// exploding is the controlled probe the containment audit induces a panic with.
//
// **It is here rather than in a test file, and the reason is the argument in the
// package comment.** The only system that will panic on request is one the suite
// broke itself, and a boundary whose behaviour has only ever been exercised by
// well-behaved systems has never been tested: `recover` would be a line nothing ever
// reached, and deleting it would change no test's outcome. An audit called "panic
// containment" that never induces a panic is a green light wired to nothing, and this
// repository has paid for three of those.
//
// `Apply` is declared on the struct, so it shadows the embedded interface's method
// at depth 0 and every other method is promoted from the system under certification.
// That is the most honest form the fixture can take: a plugin shipping a data pack
// it did not validate is exactly this, with everything else correct.
//
// The audit polices its own fixture. If this ever stopped panicking — a refactor
// that moved `Apply` behind another embedded field, say — the containment audit
// would find "no error" where it required a contained panic and the whole suite
// would fail. A fixture that cannot silently stop working is the difference between
// a test and a decoration.
type exploding struct {
	rules.System
}

// Apply panics, after doing what a resolver does before it gives up: reading the
// state it was handed and drawing from its seed.
//
// The order is the point. A boundary that recovers before the resolver has touched
// anything proves less than one that recovers after, and the realistic failure — a
// data pack whose fourth entry does not parse — happens deep inside resolution,
// with the state read and a draw already taken.
func (exploding) Apply(
	_ context.Context,
	call rules.Context,
	state rules.State,
	intent rules.Intent,
) ([]rules.Mutation, error) {
	_, _ = state.Lookup(intent.Target)
	_ = call.Rand("conformance/exploding")

	panic(explodingPanic)
}

// explode returns the system under certification with `Apply` replaced by the
// controlled probe.
func (s *Suite) explode() rules.System { return exploding{System: s.cfg.System} }

// Containment is S-10.2 and §10.8's panic row as audits: a resolution cannot change
// the state it was handed, and a resolution that reported failure returned nothing.
//
// The property S-10.2 states is that **a panic inside `Apply` leaves
// `campaign_state` byte-identical**, and the reason it holds is structural rather
// than promised: `Apply` returns its changes instead of making them, the state
// arrives by value, and `rules.State` keeps its fields unexported so there is
// nothing to write to. What is left to audit is the half that is a claim, and there
// are two halves:
//
//  1. **Nothing reached the state.** The whole snapshot is compared after every
//     resolution this suite ran — the one that panicked, the one that was refused,
//     and the author's own — and a difference is reported with both renderings,
//     because "the content changed" is not something an author can act on. It will
//     not fail today, and that is the point: it is the assertion that `State`'s copy
//     discipline is a property rather than a comment, and this repository's
//     mutation-verification habit exists to keep assertions of that kind honest.
//  2. **A failed resolution returned no mutations.** `rules.go` states this as a rule
//     on `Apply` and cannot enforce it. A resolver that has resolved half an operation
//     by the time it fails — which is exactly when a bad data pack fails, after the
//     first three mutations are in the slice — would otherwise hand the hub a partial
//     list, and the hub would have to choose between applying a truncated resolution
//     and discarding mutations the campaign's log may already claim happened. `Contain`
//     enforces it; this asserts it, so a resolver that finds its own way around the
//     boundary is caught.
//
// And the boundary itself, on every run: a controlled resolver that panics after
// reading the state and drawing from the seed must come back as a `*PanicError`, with
// no mutations and with the snapshot untouched. A boundary that lost its `recover` —
// the likeliest way for that line to be deleted — then takes the process down inside
// a GM's test rather than inside a GM's campaign, which is the whole reason this
// suite exists.
func (s *Suite) Containment(ctx context.Context) []Finding {
	var found []Finding

	// Before anything is resolved, so the comparison covers every resolution below
	// rather than only the last one.
	state, err := s.state()
	if err != nil {
		return []Finding{{Audit: AuditContainment, Summary: err.Error()}}
	}

	before := Fingerprint(state)

	probeState, err := s.state()
	if err != nil {
		return append(found, Finding{Audit: AuditContainment, Summary: err.Error()})
	}

	probeBefore := Fingerprint(probeState)

	mutations, err := Contain(s.explode()).Resolve(
		ctx, s.cfg.Scenario.Call, probeState, s.cfg.Scenario.Intent,
	)

	switch {
	case err == nil:
		found = append(found, Finding{
			Audit: AuditContainment,
			Summary: "resolving through the boundary produced no error, so either the panic did not " +
				"happen or it did not cross the boundary; §10.8 requires it recovered, returned an " +
				"error and mutated nothing",
		})
	case !errors.Is(err, ErrContained):
		found = append(found, Finding{
			Audit: AuditContainment,
			Summary: fmt.Sprintf(
				"the contained panic came back as %v, which is not a %v; the composition root's adapter "+
					"recognises a contained panic by that sentinel, and a boundary that returns anything "+
					"else cannot be told apart from the system's own failure",
				err,
				ErrContained,
			),
		})
	case len(mutations) > 0:
		found = append(found, Finding{
			Audit: AuditContainment,
			Summary: fmt.Sprintf(
				"a resolver that panicked still returned %s; a resolution that failed changes nothing",
				render(mutations),
			),
		})
	}

	if after := Fingerprint(probeState); after != probeBefore {
		found = append(found, Finding{
			Audit: AuditContainment,
			Summary: fmt.Sprintf(
				"the tabletop a panicking resolver was handed is not the tabletop it was given:\n"+
					"before:\n%s\nafter:\n%s",
				probeBefore, after,
			),
		})
	}

	// The author's own resolver, called **directly rather than through `Contain`**,
	// which is the one thing this audit does that looks wrong. `Contain` discards a
	// failed resolution's mutations, so asking it would audit the boundary rather than
	// the system — and the system is the half that can be broken. The boundary's own
	// half of the same rule is held by
	// `TestTheBoundaryDiscardsTheMutationsOfAFailedResolution` in this package's tests.
	//
	// Both snapshots are the same value read twice, so this `before` and the one above
	// are comparable, and the check is here rather than only in `UnknownKind` because a
	// system may well write to what it was handed in exactly the resolutions that fail.
	resolved, resolveErr := s.cfg.System.Apply(
		ctx, s.cfg.Scenario.Call, state, s.cfg.Scenario.Intent,
	)

	if resolveErr != nil && len(resolved) > 0 {
		found = append(found, Finding{
			Audit: AuditContainment,
			Summary: fmt.Sprintf(
				"resolving %q reported %v and still returned %s; a resolution that reported failure must "+
					"have changed nothing, or the hub has to choose between applying half an answer and "+
					"discarding mutations its log already records",
				s.cfg.Scenario.Intent,
				resolveErr,
				render(resolved),
			),
		})
	}

	if after := Fingerprint(state); after != before {
		found = append(found, Finding{
			Audit: AuditContainment,
			Summary: fmt.Sprintf(
				"resolving %q changed the tabletop it was handed:\nbefore:\n%s\nafter:\n%s",
				s.cfg.Scenario.Intent, before, after,
			),
		})
	}

	return found
}
