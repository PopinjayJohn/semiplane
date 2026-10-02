package plugin

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/realtime"
)

// This file holds the one part of the adapter that writes: taking what a system
// returned and putting it into `campaign_state`. It is separated from `adapter.go`
// because the two have opposite failure stories — `adapter.go` translates and can
// refuse, this file commits — and because the invariant below is worth stating
// once, in the file whose whole purpose is to maintain it.
//
// # The invariant: an error means nothing was applied
//
// S-10.2 says `Apply` returns `[]Mutation` and never applies in place, so "a plugin
// cannot write `campaign_state`" is a property of the type rather than a review
// promise. This file is where that property becomes enforceable, and the shape of
// the answer is: **there is no value in this package that holds mutations and an
// error at once.** `settle` is the only function that calls `System.Apply`, it holds
// the result in a local, and every one of its error returns is `return nil, err`.
// So a system that returns `([]Mutation{...}, err)` — a contract violation ADR 0041
// and `rules.System.Apply` both forbid — has its mutations dropped by the shape of
// the code rather than by a check.
//
// That is the whole enforcement. It is *not* a test of the form "if err != nil &&
// len(mutations) > 0", because a check is a thing a later edit can drop and this is
// a thing a later edit cannot: writing `return mutations, err` requires a named
// result that no function here has.
//
// # The invariant: a panic in plugin code leaves the state byte-identical
//
// §10.8's fourth row says a panic inside `Apply` is recovered, reported as an error,
// and mutates nothing. It is met here by **ordering**, not by a lock:
//
//	 1. every plugin call happens before the first write — `snapshot` calls
//	     `Codec.Object`, `settle` calls `System.Apply`, and `prepare` calls
//	     `Codec.Apply` on drafts;
//	 2. only then does `commit` write, through `CampaignState.Mutate` and
//	     `CampaignState.Remove`.
//
// So the single `recover` in `settle` is not a partial containment: if it fires, no
// write has happened. A codec that panics reading the third placement leaves the
// first two exactly as they were, and a system that panics halfway through resolving
// applies nothing — which is the blast radius §10.2's table promises for a gameplay
// plugin, stated as a consequence of the order rather than as a review item.
//
// The residual is stated rather than hidden: a panic inside `CampaignState.Mutate`
// itself would also be recovered here, after `commit` had begun writing. That is
// semiplane's own code and `state.go`'s contract says a state does not panic; the
// recovery is still worth having, because it keeps a hub's socket loop alive.

// settle resolves one intent against one campaign's state and returns the changes
// to broadcast.
//
// **The whole of a resolution, and the only function that calls `System.Apply`.**
// Three steps in a fixed order — snapshot, resolve, apply — and the defer is the
// recovery boundary §10.8 means: it is at the `Apply` boundary because `Apply` is
// where plugin code starts and ends for an intent, and it is here rather than around
// one call so that a codec panicking in step one is contained by the same boundary
// as a system panicking in step two.
//
// The snapshot and the mutations come from **one** document. Reading the state twice
// would hand a system one revision and apply against another, and the currency check
// in `prepare` would be comparing a document nobody resolved against.
//
// Every error return is `return nil, err`, with the mutations discarded — see the
// file's first invariant. A cancelled context arrives from `Apply` as an ordinary
// error and is refused as `server_error`, which is `protocol.go`'s default for
// anything unrecognised and the right word for a resolution that says nothing about
// the game.
func settle(
	ctx context.Context,
	entry Entry,
	state *realtime.CampaignState,
	call rules.Context,
	requested rules.Intent,
	actor realtime.UserID,
) (changes []realtime.Change, err error) {
	// Named results because `recover` writes to them, and `changes` is cleared in
	// the recover branch rather than merely left nil: a resolution that has already
	// built a broadcast and then panicked must not send half of it, and the cost of
	// being certain about that is one assignment.
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}

		changes = nil
		err = &PanicError{
			System: entry.System.ID(),
			Type:   fmt.Sprintf("%T", recovered),
		}
	}()

	document := state.Snapshot()

	resolved, err := snapshot(entry, document)
	if err != nil {
		return nil, err
	}

	// Wrapped for the same reason `settle`'s doc gives: the returned error is the
	// plugin's, it is held in `Rejection.Err` and printed by nothing, and a log line
	// that says which system refused is worth the prefix.
	mutations, err := entry.System.Apply(ctx, call, resolved, requested)
	if err != nil {
		return nil, fmt.Errorf(
			"plugin: system %q resolved %q: %w", entry.System.ID(), requested, err,
		)
	}

	return apply(entry, state, document, mutations, actor)
}

// PanicError is a gameplay system — or its codec — that panicked while resolving an
// intent.
//
// Its own type because the class is worth matching on: a plugin panicking on one
// table is a bug report, and every other refusal in the adapter is a client or an
// operator's condition. It satisfies no `ErrMalformed*` sentinel deliberately — a
// panicking system is not a malformed one, and lumping them together would make a
// boot that counted malformed registrations report a plugin's runtime crash as a
// wiring fault.
//
// ## The value is dropped, and that is the interesting part of the type
//
// `recovered` is recorded as its **type**, never as itself. A panic value is whatever
// the plugin panicked with, and the idiomatic panic is `panic(err)` or
// `panic(fmt.Sprintf(...))` — so the value is routinely the payload the resolver
// choked on, which on this project is a dice result or a `[!secret]` callout body.
// S-12.3 forbids an event carrying either, and a log line is an event. The type is
// enough to act on: `runtime.plainError` is a bounds or type error, a nil map write
// is a missing initialisation, and the seed plus the intent in the audit trail
// reproduces the rest.
type PanicError struct {
	// System is the id of the system whose code panicked.
	System rules.ID

	// Type is `%T` of the recovered value.
	Type string
}

// Error returns the system and the panic's type, and nothing that came from a
// resolution.
func (e *PanicError) Error() string {
	return "plugin: system " + string(e.System) + " panicked resolving an intent: " + e.Type
}

// Unwrap returns `ErrPanicked`, so one question — "did a plugin crash?" — is
// answerable with `errors.Is` without the panic's value being part of the chain.
//
// Returning the sentinel rather than nil, and rather than the dropped value: the
// value is deliberately not retained (see the type comment), so there is nothing
// else to return, and a type whose `Unwrap` returns nil cannot participate in an
// `errors.Is` chain at all — which would make the one question a caller most wants
// to ask the one it could not ask.
func (e *PanicError) Unwrap() error { return ErrPanicked }

// Class returns the content-free class a log line should carry, so an alert can
// match "a gameplay plugin panicked" without matching anything a plugin wrote.
func (e *PanicError) Class() string { return "plugin.panicked" }

// ErrPanicked is plugin code that panicked inside a resolution.
//
// It is the only refusal in this package that does not wrap `ErrMalformedPlugin`:
// it is not a mistake at registration, it is a crash at runtime, and a caller
// counting startup refusals must not count it.
var ErrPanicked = errors.New("plugin: a gameplay system panicked while resolving an intent")

// plan is one mutation, decoded and ready to write.
//
// It holds a **draft** — a copy of the placement — rather than the live one,
// because decoding is plugin code and plugin code must run before the first write.
// See `commit`.
type plan struct {
	// id is the placement this plan writes to.
	id realtime.PlacementID

	// seen is the version the plan is applied at, and the version the system
	// resolved against.
	seen uint64

	// draft is the placement as the codec left it. Ignored for a removal.
	draft realtime.Placement

	// mutation is the system's own statement, carried through to the change.
	mutation rules.Mutation
}

// apply turns one system's mutations into changes, writing nothing until every one
// of them is known to be writable.
//
// Three steps — shape, currency, decode — and then `commit`. The separation is the
// point, and it is what makes §10.8's blast radius honest: by the time the first
// byte of `campaign_state` moves, every plugin call has already returned.
func apply(
	entry Entry,
	state *realtime.CampaignState,
	document realtime.Document,
	mutations []rules.Mutation,
	actor realtime.UserID,
) ([]realtime.Change, error) {
	plans, err := prepare(entry, state, document, mutations)
	if err != nil {
		return nil, err
	}

	return commit(plans, state, actor)
}

// prepare refuses the whole list or returns one plan per mutation.
//
// Refusing **whole** is the property worth having, and it is what distinguishes a
// precheck from `Mutate`'s own check: `CampaignState.Mutate` verifies the version
// under its lock and is the guarantee, but it refuses one mutation at a time, so a
// list of five with one stale entry would land four changes and then stop. A table
// where four things moved and the fifth did not is a state no client can reconcile
// against a delta, because the delta names the placement that did not move and
// says nothing about the ones that did.
//
// The window between this function and `commit` is real and is documented on
// `apply`: two intents for one campaign can interleave there, and the loser's
// `Mutate` refuses it under the lock. What cannot happen is a *silent* partial
// application of a list this function accepted.
func prepare(
	entry Entry,
	state *realtime.CampaignState,
	document realtime.Document,
	mutations []rules.Mutation,
) ([]plan, error) {
	if err := checkShapes(entry, mutations); err != nil {
		return nil, err
	}

	// Two maps, and the second one is not optional. `claimed` holds the versions
	// the system resolved against and `accounted` holds the placements this
	// resolution has already written a plan for; a single map cannot do both jobs,
	// because "seen, and its version is 0" and "seen" are the same value. The bug
	// that costs was real: a second mutation naming one placement read the first
	// one's *slot* as its resolved version, compared 0 against the live version,
	// and was refused as `stale_version` — the right outcome for the wrong reason,
	// which is a refusal a plugin author cannot act on.
	claimed := documentVersions(document)
	accounted := make(map[realtime.PlacementID]struct{}, len(mutations))
	plans := make([]plan, 0, len(mutations))

	for _, mutation := range mutations {
		id := realtime.PlacementID(mutation.Target)

		if _, twice := accounted[id]; twice {
			return nil, fmt.Errorf(
				"%w: two mutations name %q in one resolution", ErrUnappliable, id,
			)
		}

		accounted[id] = struct{}{}

		seen, err := currency(state, claimed, id)
		if err != nil {
			return nil, err
		}

		next := plan{id: id, seen: seen, mutation: mutation}
		if !mutation.Remove {
			if next.draft, err = draft(entry, state, id, mutation); err != nil {
				return nil, err
			}
		}

		plans = append(plans, next)
	}

	return plans, nil
}

// draft copies a placement and writes one resolved mutation onto the copy.
//
// Outside the hub's lock, deliberately: a codec that cannot decode returns an error
// here, with nothing stamped. Were it called inside `Mutate`'s function, a decode
// failure would arrive *after* the version had been stamped, and the placement
// would have advanced a version while nothing about it had changed — a client
// reconciling against it waits forever for a delta that never describes the
// version it was given.
func draft(
	entry Entry,
	state *realtime.CampaignState,
	id realtime.PlacementID,
	mutation rules.Mutation,
) (realtime.Placement, error) {
	placement, live := state.Placement(id)
	if !live {
		return realtime.Placement{}, fmt.Errorf(
			"%w: %q", realtime.ErrNoPlacement, id,
		)
	}

	// The conditions array is cloned because a struct copy shares a slice's backing
	// array: a codec appending to `Conditions` without going through `AddCondition`
	// would be writing into the live placement's array from outside the lock, and
	// the race detector would not see it because nothing else touches that array at
	// the time. `realtime.Placement.clone` is unexported for the same reason it is
	// unexported there.
	placement.Conditions = slices.Clone(placement.Conditions)

	if err := entry.Codec.Apply(&placement, mutation); err != nil {
		return realtime.Placement{}, fmt.Errorf(
			"%w: system %q resolved %q: %w", ErrUnappliable, entry.System.ID(), mutation, err,
		)
	}

	return placement, nil
}

// currency answers the version one mutation must be applied at, and refuses when the
// placement has moved since the system resolved against it.
//
// **The version comes from the document, not from the state.** Reading the current
// version and applying to it would apply a decision made against version 3 to
// version 4: the roll was computed from state the caller never saw, which is the
// optimistic-apply bug S-7.2's per-placement versioning exists to make impossible.
//
// A placement the snapshot did not carry is `no_such_placement` — the system
// resolved against a table that did not have it, which is either a plugin that kept
// a stale object id or a client acting on a placement that is gone.
func currency(
	state *realtime.CampaignState,
	claimed map[realtime.PlacementID]uint64,
	id realtime.PlacementID,
) (uint64, error) {
	resolved, was := claimed[id]
	if !was {
		return 0, fmt.Errorf(
			"%w: %q was not in the state the system resolved",
			realtime.ErrNoPlacement, id,
		)
	}

	current, live := state.Version(id)
	if !live {
		return 0, fmt.Errorf("%w: %q is not placed", realtime.ErrNoPlacement, id)
	}

	if resolved != current {
		return 0, fmt.Errorf(
			"%w: %q is at version %d, the system resolved at %d",
			realtime.ErrVersionMismatch, id, current, resolved,
		)
	}

	return current, nil
}

// documentVersions returns the version every placement in the snapshot carried.
func documentVersions(document realtime.Document) map[realtime.PlacementID]uint64 {
	versions := make(map[realtime.PlacementID]uint64, len(document.Placements))

	for _, placement := range document.Placements {
		versions[placement.ID] = placement.Version
	}

	return versions
}

// checkShapes refuses the first mutation the hub could not apply.
//
// `rules.Mutation.Valid` is asked and not `NewMutation`, because a system builds
// its mutations by struct literal — the fields are exported, and `NewMutation`
// exists for callers that want the copy — so the predicate is the only thing that
// sees every one of them.
//
// `ErrUnappliable` and not a wire reason, because this is the system's bug rather
// than the client's: the adapter maps it to `server_error`, which says nothing
// about what failed, and the message names the system and the mutation.
func checkShapes(entry Entry, mutations []rules.Mutation) error {
	for _, mutation := range mutations {
		if mutation.Valid() {
			continue
		}

		return fmt.Errorf("%w: system %q resolved %q", ErrUnappliable, entry.System.ID(), mutation)
	}

	return nil
}

// commit writes prepared plans, in the order the system listed them.
//
// **No plugin code runs here**, which is the invariant this file is built around.
// Every function this calls belongs to `internal/realtime`, and the closure handed
// to `Mutate` is a struct assignment.
func commit(
	plans []plan,
	state *realtime.CampaignState,
	actor realtime.UserID,
) ([]realtime.Change, error) {
	changes := make([]realtime.Change, 0, len(plans))

	// Indexed rather than ranged: `plan` is 176 bytes of placement, and a loop that
	// copied one per iteration would copy the tabletop's worth of it per mutation.
	for index := range plans {
		receipt, err := write(plans[index], state)
		if err != nil {
			return nil, err
		}

		changes = append(changes, change(receipt, plans[index].mutation, actor))
	}

	return changes, nil
}

// write commits one plan and returns the hub's receipt for it.
func write(next plan, state *realtime.CampaignState) (realtime.Mutation, error) {
	if next.mutation.Remove {
		// A removal stamps the version the placement *would* have carried, and the
		// version map outlives the placement so a re-created placement of the same
		// id is stamped above it. The delta is therefore the only place that number
		// exists afterwards, which is why the change is assembled from this receipt
		// rather than reconstructed by a caller.
		receipt, err := state.Remove(next.id, next.seen)
		if err != nil {
			return realtime.Mutation{}, fmt.Errorf(
				"plugin: remove %q: %w", next.id, err,
			)
		}

		return receipt, nil
	}

	draft := next.draft

	// `Mutate` stamps `ID` and `Version` back over whatever this leaves, so the
	// draft's copies of them are inert — S-7.2's authority, enforced by the hub
	// rather than by this function trusting a payload.
	receipt, err := state.Mutate(next.id, next.seen, func(live *realtime.Placement) {
		*live = draft
	})
	if err != nil {
		return realtime.Mutation{}, fmt.Errorf("plugin: write %q: %w", next.id, err)
	}

	return receipt, nil
}

// change assembles one broadcast entry from the hub's receipt and the system's
// statement.
//
// **The version is the hub's and the op is the system's**, and neither side can
// forge the other's half: `rules.Mutation` has no version field to fill in and
// `realtime.Mutation` has no op or args to fill in. ADR 0041's translation table
// in one line of code.
func change(
	receipt realtime.Mutation,
	mutation rules.Mutation,
	actor realtime.UserID,
) realtime.Change {
	return realtime.Change{
		Placement: receipt.Placement,
		Version:   realtime.Version(receipt.Version),
		Op:        realtime.Op(mutation.Op),
		Args:      mutation.Args,
		By:        actor,
	}
}
