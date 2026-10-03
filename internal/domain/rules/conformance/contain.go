package conformance

import (
	"context"
	"errors"
	"fmt"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// ErrContained is what `Contain` returns in place of a panic that escaped a
// resolution.
//
// A sentinel and not a formatted message, because the composition root's adapter
// has to be able to *recognise* it: §10.8's row is "recover() at the `Apply`
// boundary; return an error", and the error that goes on the wire for a resolver
// that gave up is `server_error` — "the one reason that says nothing about what
// failed". An adapter that string-matched this would break on the first rewrite,
// and an adapter that could not recognise it would have nowhere to put the panic's
// own text, which is the correct place for it to go and the wrong place for
// anything else.
//
// A `plugin.PanicError` wrapping it carries the recovered value for the log line,
// and satisfies `errors.Is`.
var ErrContained = errors.New("conformance: a resolution panicked and was contained")

// PanicError is a panic that was caught at the `Apply` boundary, with the value it
// carried.
//
// A concrete error rather than the sentinel alone, for the reason
// `realtime.DriftError` is one: an operator reading a log line needs the panic's
// value, and a handler answering a request needs to know not to. The value
// reaches this struct and nowhere else — `Error` prints it, which is a log line
// and a test failure, and **not** an observability event, because
// `observability.EventAttributes` has no field one could be passed through.
//
// `Class` names it for `observability.Classed`, so the log line and any startup
// message say the same word rather than one of them saying `*fmt.wrapError`.
type PanicError struct {
	// Value is what the panic carried, exactly as `recover` returned it.
	Value any
}

// Error renders the containment and the value.
//
// The value's own text goes in, and the reason that is safe while a payload's is
// not is the difference between the two: a panic value is a string or an error a
// plugin wrote about its own code path, and a payload is an answer to a roll a
// player asked for. S-12.3 is about the second.
func (e *PanicError) Error() string {
	return fmt.Sprintf("%v: the resolver panicked: %v", ErrContained, e.Value)
}

// Unwrap returns `ErrContained`, so a caller asks one question.
func (e *PanicError) Unwrap() error { return ErrContained }

// Class names this failure for a log line's `detail`.
func (e *PanicError) Class() string { return "resolver_panic" }

// Boundary is a `rules.System` behind the `recover()` §10.8 requires.
//
// **This is production code, not test scaffolding**, and it lives in this package
// for the reason the package comment gives: the composition root's adapter should
// call `Contain` rather than write a sixth copy of a five-line function, and a
// copy is a copy that will eventually be the one without the `recover()` in it.
type Boundary struct {
	system rules.System
}

// Contain returns a system behind the panic boundary.
//
// A nil system is tolerated here and **refused at use**, failing closed: the
// alternative is treating an absent resolver as one that resolved nothing, which
// is a gate wired to nothing reporting success.
func Contain(system rules.System) Boundary { return Boundary{system: system} }

// Resolve resolves one intent, containing a panic.
//
// The contract in the body is the contract of the whole method, and it is three
// rules rather than one:
//
//  1. A panic becomes an error. The recovered value is kept, in a `*PanicError`, so
//     the log line says what happened without a plugin's panic text ever becoming a
//     refusal a client is shown.
//  2. A resolution that failed returns **no mutations**. `rules.go` states this as a
//     rule on `Apply` and cannot enforce it; this is where it is enforced, and it
//     is the half of S-10.2 that is not free. A resolver that has half-resolved by
//     the time it gives up — which is exactly when a bad data pack panics, after
//     the first three mutations are in the slice — would otherwise hand the hub a
//     partial list, and the hub's choice would be between applying a truncated
//     resolution and discarding mutations the campaign's log already claims
//     happened.
//  3. The caller applies nothing either way. There is no write path from here to
//     the state; `Apply` takes it by value and `rules.State` keeps its fields
//     unexported, so this is a consequence of the signature rather than of
//     anything in this function.
//
// The first mutation is discarded rather than the rest returned, because a partial
// list is not a prefix of an answer — it is an answer to a different question.
func (b Boundary) Resolve(
	ctx context.Context,
	call rules.Context,
	state rules.State,
	intent rules.Intent,
) (mutations []rules.Mutation, err error) {
	if b.system == nil {
		return nil, fmt.Errorf("conformance: %w: no resolver to contain", ErrContained)
	}

	// Named results because `recover` only works in a deferred function of the frame that
	// deferred it, so the assignment has to land on this frame's results.
	//
	// **Two explicit paths and no third.** A panic is caught here and both results are
	// replaced; an error is returned below with a nil slice. A deferred "if err != nil,
	// discard the mutations" would have been the third, and it is deliberately absent:
	// `mutations` is only ever assigned by the one statement at the bottom of this
	// function, so a panic cannot leave a partial list in it, and an error is answered
	// with `nil` explicitly. A branch that cannot fire is a rule nobody can break and
	// nobody can point at, which is the opposite of what this file is for — the two
	// paths that *can* be broken each have a test, and mutating either one turns the
	// gate red.
	defer func() {
		if recovered := recover(); recovered != nil {
			mutations, err = nil, &PanicError{Value: recovered}
		}
	}()

	mutations, err = b.system.Apply(ctx, call, state, intent)
	if err != nil {
		// Wrapped, and the wrapper is load-bearing in both directions: the adapter needs
		// `errors.Is` to reach the system's own sentinel so it can pick a wire word, and an
		// operator reading the log needs to know which resolver gave up before the system's
		// own text says so. `wrapcheck` is right that an error crossing a package boundary
		// unannotated is a log line that reads as ours.
		return nil, fmt.Errorf("conformance: resolving %s: %w", intent.Op, err)
	}

	return mutations, nil
}
