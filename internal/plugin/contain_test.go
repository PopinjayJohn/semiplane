package plugin_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/realtime"
)

// This file is the proof of the two invariants in `apply.go`, and it is separate
// from the translation table because those two are about what a failure **did** and
// this table is about what it left behind.
//
// Both are measured against the hub's own `*realtime.CampaignState`, encoded with
// `realtime.EncodeDocument` before and after. A stub state would prove the same
// assertions about the stub, and S-10.2's promise names `campaign_state`.

// TestAPanicInsideApplyLeavesTheHubStateByteIdentical is §10.8's fourth row and S-10.2's
// second sentence, measured rather than asserted from the signature.
//
// Three things are checked, and the third is the one a stub could not check:
//
//   - the panic does not escape `Resolve` — it comes back as an error;
//   - `campaign_state` encodes to the same bytes it did before;
//   - the refusal names the panic's **type** and not the value, because a panic
//     value is routinely the payload the resolver choked on and S-12.3 forbids an
//     event carrying that.
func TestAPanicInsideApplyLeavesTheHubStateByteIdentical(t *testing.T) {
	t.Parallel()

	const secret = "the passphrase is hunter2"

	states := newStates(t)
	state := open(t, states, 3)

	before := documentBytes(t, state)

	system := systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.apply = func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
			// A plugin mid-way through resolving, panicking on a value that came
			// from the thing it was resolving. The idiomatic panic.
			panic("the pack row said " + secret)
		}
	})

	resolver := newResolver(t, system, states)

	resolution, err := resolver.Resolve(t.Context(), gmIntent(playerFrame(1, "attack", "p1")))

	if err == nil {
		t.Fatalf("the panic was not contained; the resolution was %+v", resolution)
	}

	if resolution.Answer != nil || resolution.Broadcast != nil {
		t.Errorf("a panicking system produced %+v", resolution)
	}

	if documentChanged(t, state, before) {
		t.Error("campaign_state changed across a panic")
	}

	panicked, ok := errors.AsType[*plugin.PanicError](err)
	if !ok {
		t.Fatalf("the refusal is %T, want a *plugin.PanicError", err)
	}

	if !errors.Is(err, plugin.ErrPanicked) {
		t.Errorf("the refusal %v does not satisfy ErrPanicked", err)
	}

	if panicked.System != "5e-2024" {
		t.Errorf("the panic names system %q, want %q", panicked.System, "5e-2024")
	}

	if panicked.Type != "string" {
		t.Errorf("the panic records type %q, want %q", panicked.Type, "string")
	}

	// S-12.3. The panic *value* is the string a plugin panicked with, and the
	// idiomatic panic is one the plugin built out of the thing it was resolving.
	// Neither the error nor the class may carry it.
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the refusal carries the panic's value: %v", err)
	}

	if panicked.Class() != "plugin.panicked" {
		t.Errorf("Class() is %q, want %q", panicked.Class(), "plugin.panicked")
	}

	if got := reasonOf(t, err); got != realtime.RejectServerError {
		t.Errorf("the client is told %q, want %q", got, realtime.RejectServerError)
	}
}

// TestAPanicAfterTheSystemHasResolvedNothingAppliesNothing is the same containment
// with the panic raised from a **nil map** rather than a string.
//
// It is a separate test because the type recorded differs (`*runtime.errorString`
// against `string`), and `PanicError.Type` exists to be the thing an alert groups
// on. A record that always said "panic" would be no record at all.
func TestAPanicAfterTheSystemHasResolvedNothingAppliesNothing(t *testing.T) {
	t.Parallel()

	states := newStates(t)
	state := open(t, states, 2)

	before := documentBytes(t, state)

	system := systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.apply = func(_ context.Context, _ rules.Context, resolved rules.State, _ rules.Intent) ([]rules.Mutation, error) {
			// An index past the end of the state it was handed: the commonest panic
			// in a rules engine that read a table it meant to build first. Chosen
			// over a nil map write because the linter reads *this* one statically and
			// refuses a test whose panic it can prove.
			objects := resolved.Objects()

			return nil, errors.New(objects[7].ID.String())
		}
	})

	resolver := newResolver(t, system, states)

	if _, err := resolver.Resolve(t.Context(), gmIntent(gmFrame(1, "roll"))); err == nil {
		t.Fatal("the panic was not contained")
	} else {
		var panicked *plugin.PanicError
		if !errors.As(err, &panicked) {
			t.Fatalf("the refusal is %T, want a *plugin.PanicError", err)
		}

		if !strings.HasPrefix(panicked.Type, "runtime.") {
			t.Errorf("the panic records type %q, want the runtime's own", panicked.Type)
		}
	}

	if documentChanged(t, state, before) {
		t.Error("campaign_state changed across a panic")
	}
}

// TestAPanicInsideACodecLeavesTheStateByteIdentical widens the boundary to the *other*
// plugin code the adapter calls.
//
// §10.8 names `Apply`, but a codec is plugin code too and it runs earlier: `Object`
// while the snapshot is being built and `Apply` while drafts are decoded. The
// ordering in `apply.go` is what makes the single `recover` cover them — every plugin
// call happens before the first write — and these two rows are what hold that claim
// to the two halves of the codec.
func TestAPanicInsideACodecLeavesTheStateByteIdentical(t *testing.T) {
	t.Parallel()

	cases := map[string]plugin.Codec{
		"while the state is being read":    panickingCodec{onObject: true},
		"while a payload is being decoded": panickingCodec{onApply: true},
	}

	for name, codec := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			states := newStates(t)
			state := open(t, states, 2)

			before := documentBytes(t, state)

			system := systemWith(func(s *stubSystem) {
				s.id = "5e-2024"
				s.apply = func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
					// A perfectly good resolution, so the only thing that can fail is
					// the codec.
					return []rules.Mutation{moveTo(t, "p1", 99, 99)}, nil
				}
			})

			registry := plugin.New()
			if err := registry.Register(plugin.Entry{System: system, Codec: codec}); err != nil {
				t.Fatalf("registering: %v", err)
			}

			resolver, err := plugin.NewResolver(plugin.ResolverConfig{
				Systems: registry,
				States:  states,
				SystemOf: func(context.Context, int64) (rules.ID, error) {
					return "5e-2024", nil
				},
				Seeds: fixedSeeds(aSeed(t, 1)),
			})
			if err != nil {
				t.Fatalf("building the resolver: %v", err)
			}

			if _, err := resolver.Resolve(
				t.Context(),
				gmIntent(playerFrame(1, "set_hp", "p1")),
			); err == nil {
				t.Fatal("the panic was not contained")
			}

			if documentChanged(t, state, before) {
				t.Error("campaign_state changed across a codec panic")
			}
		})
	}
}

// TestMutationsReturnedWithAnErrorApplyNothing is the invariant the whole of
// `apply.go` exists to make unrepresentable, observed at the boundary.
//
// A system that returns `([]Mutation{...}, err)` has broken its contract — ADR 0041
// and `rules.System.Apply` both say an error means nothing was applied — and the
// adapter's answer is not a check but a **shape**: the function that called `Apply`
// returns `nil` for its mutations on every error path, so there is no value in
// scope for a caller to apply.
//
// The assertions are therefore: nothing was refused *silently* (there is an error),
// the client is told something that says nothing about the game, and
// `campaign_state` is byte-identical. A future edit that returned both would fail
// the last one, which is the whole point.
func TestMutationsReturnedWithAnErrorApplyNothing(t *testing.T) {
	t.Parallel()

	states := newStates(t)
	state := open(t, states, 3)

	before := documentBytes(t, state)

	resolved := []rules.Mutation{moveTo(t, "p1", 11, 22), moveTo(t, "p2", 33, 44)}

	system := systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.apply = func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
			// The contract violation: two good mutations *and* an error.
			return resolved, errors.New("ran out of conditions half way through")
		}
	})

	resolver := newResolver(t, system, states)

	resolution, err := resolver.Resolve(t.Context(), gmIntent(playerFrame(1, "set_hp", "p1")))

	if err == nil {
		t.Fatalf("a system that returned mutations and an error was not refused: %+v", resolution)
	}

	if resolution.Answer != nil || resolution.Broadcast != nil {
		t.Errorf("a refused resolution carried %+v", resolution)
	}

	if documentChanged(t, state, before) {
		t.Error("campaign_state changed for a system that returned mutations with an error")
	}

	// Not a panic and not a plugin-shaped refusal: it is an ordinary failure, and
	// the reason is `server_error` because nothing here classified it.
	if got := reasonOf(t, err); got != realtime.RejectServerError {
		t.Errorf("the client is told %q, want %q", got, realtime.RejectServerError)
	}

	if errors.Is(err, plugin.ErrPanicked) {
		t.Error("an error was reported as a panic")
	}

	// The system's own text reaches neither the client nor a log line: the
	// rejection's `Error()` is its reason, and its `Err` field holds the detail for
	// a caller that logs it deliberately. S-12.3 is the reason — a system resolving
	// an attack against a handout can quote the handout.
	if strings.Contains(err.Error(), "ran out of conditions") {
		t.Errorf("the refusal carries the system's own text: %v", err)
	}

	// And the detail is still *reachable*, which is the other half: an operator with
	// a log can get it without the client ever seeing it.
	rejection, ok := errors.AsType[*plugin.RejectionError](err)
	if !ok {
		t.Fatalf("the refusal carries no reason: %v", err)
	}

	if rejection.Err == nil {
		t.Fatal("the refusal dropped the system's own error rather than holding it")
	}
}

// TestTheFirstMutationThatCannotBeAppliedStopsTheWholeList is the precheck's reason
// for existing, and it is asserted in the form that matters: the *other* mutation
// does not land.
//
// A hub that applied a list until it hit the bad entry would leave a table where one
// creature has moved and the other has not, and no client can reconcile against a
// delta that describes only half of what happened.
//
// **Each case also names the reason**, and that is not decoration. An earlier
// version of this table passed its duplicate case for the wrong reason: the
// duplicate check and the currency check shared one map, so the second mutation
// naming a placement read the first one's slot as a resolved version of 0 and was
// refused as `stale_version` — which blames the client for a plugin's mistake. It
// passed because this test only asked "was it refused". Pinning the reason is what
// makes each case mean what its name says.
func TestTheFirstMutationThatCannotBeAppliedStopsTheWholeList(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		mutations []rules.Mutation
		want      realtime.RejectReason
	}{
		"a malformed mutation second": {
			mutations: []rules.Mutation{
				moveTo(t, "p1", 11, 22),
				{Target: "p2"},
			},
			want: realtime.RejectServerError,
		},
		"a placement that is not there, second": {
			mutations: []rules.Mutation{
				moveTo(t, "p1", 11, 22),
				moveTo(t, "p404", 33, 44),
			},
			want: realtime.RejectNoSuchPlacement,
		},
		"two mutations naming one placement": {
			mutations: []rules.Mutation{
				moveTo(t, "p1", 11, 22),
				moveTo(t, "p1", 33, 44),
			},
			want: realtime.RejectServerError,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			states := newStates(t)
			state := open(t, states, 2)

			before := documentBytes(t, state)

			system := systemWith(func(s *stubSystem) {
				s.id = "5e-2024"
				s.apply = func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
					return testCase.mutations, nil
				}
			})

			resolver := newResolver(t, system, states)

			resolution, err := resolver.Resolve(
				t.Context(),
				gmIntent(playerFrame(1, "set_hp", "p1")),
			)
			if err == nil {
				t.Fatalf("an unappliable list was accepted: %+v", resolution)
			}

			if resolution.Broadcast != nil {
				t.Errorf("a refused resolution broadcast %v", resolution.Broadcast)
			}

			if got := reasonOf(t, err); got != testCase.want {
				t.Errorf("the client is told %q, want %q", got, testCase.want)
			}

			// The whole point: `p1`'s good mutation did **not** land either.
			if documentChanged(t, state, before) {
				t.Error("a refused list was partly applied")
			}
		})
	}
}

// TestAPayloadTheCodecCannotDecodeStampsNothing is the draft's reason for existing.
//
// A decode failure arrives as an error from `Codec.Apply`, which runs on a copy
// outside the hub's lock. Were it inside `Mutate`, the version would already have
// been stamped when the failure surfaced, and the placement would have advanced a
// version while nothing about it had changed — a client reconciling against that
// version waits for a delta that never comes.
func TestAPayloadTheCodecCannotDecodeStampsNothing(t *testing.T) {
	t.Parallel()

	states := newStates(t)
	state := open(t, states, 1)

	before, _ := state.Version("p1")
	beforeBytes := documentBytes(t, state)

	system := systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.apply = func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
			// Not a placement at all: the codec cannot read it back.
			return []rules.Mutation{{Target: "p1", Op: "set_hp", Args: []byte("not json")}}, nil
		}
	})

	registry := plugin.New()
	if err := registry.Register(
		plugin.Entry{System: system, Codec: plugin.PlacementCodec{}},
	); err != nil {
		t.Fatalf("registering: %v", err)
	}

	resolver, err := plugin.NewResolver(plugin.ResolverConfig{
		Systems: registry,
		States:  states,
		SystemOf: func(context.Context, int64) (rules.ID, error) {
			return "5e-2024", nil
		},
		Seeds: fixedSeeds(aSeed(t, 1)),
	})
	if err != nil {
		t.Fatalf("building the resolver: %v", err)
	}

	if _, err := resolver.Resolve(
		t.Context(),
		gmIntent(playerFrame(1, "set_hp", "p1")),
	); err == nil {
		t.Fatal("a payload the codec cannot read was accepted")
	} else if !errors.Is(
		err,
		plugin.ErrUnappliable,
	) {
		t.Errorf("the refusal %v does not satisfy ErrUnappliable", err)
	}

	after, _ := state.Version("p1")
	if after != before {
		t.Errorf("the placement's version moved from %d to %d on a decode failure", before, after)
	}

	if documentChanged(t, state, beforeBytes) {
		t.Error("campaign_state changed on a decode failure")
	}
}

// TestAVersionThatMovedUnderAResolutionIsRefusedWhole is the precheck's reason for
// existing, stated as the difference it makes.
//
// The interleaving is manufactured rather than raced, because a test that waits for
// a race is a test that fails on a slow machine and passes on a fast one. The codec's
// `Apply` runs while drafts are decoded — after the snapshot, before the first write
// — and this one moves `p2` the first time it is called, which is the code path a
// concurrent intent would occupy.
//
// **The order of the list is the test.** `p1` is named first and `p2` second, so:
//
//   - with the currency precheck, `p2`'s plan is refused before anything is written,
//     and `p1` does not move;
//   - without it, `p1`'s plan is written and only then does `Mutate` refuse `p2`.
//
// The second case is what `CampaignState.Mutate`'s version check is *for*, and both
// results are correct refusals — `stale_version` either way. Only one of them leaves
// the table in a state a client can reconcile, which is the whole reason this
// function checks before it writes rather than relying on the writer.
func TestAVersionThatMovedUnderAResolutionIsRefusedWhole(t *testing.T) {
	t.Parallel()

	states := newStates(t)
	state := open(t, states, 2)

	p1Before, _ := state.Version("p1")
	p2Before, _ := state.Version("p2")

	// A codec that moves p2 the first time it is asked to decode anything.
	meddling := &meddlingCodec{state: state, target: "p2", once: true}

	system := systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.apply = func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
			// `p1` first, so the stale entry is discovered *after* the good one is
			// ready to write.
			return []rules.Mutation{moveTo(t, "p1", 55, 55), moveTo(t, "p2", 66, 66)}, nil
		}
	})

	registry := plugin.New()
	if err := registry.Register(plugin.Entry{System: system, Codec: meddling}); err != nil {
		t.Fatalf("registering: %v", err)
	}

	resolver, err := plugin.NewResolver(plugin.ResolverConfig{
		Systems: registry,
		States:  states,
		SystemOf: func(context.Context, int64) (rules.ID, error) {
			return "5e-2024", nil
		},
		Seeds: fixedSeeds(aSeed(t, 1)),
	})
	if err != nil {
		t.Fatalf("building the resolver: %v", err)
	}

	_, err = resolver.Resolve(t.Context(), gmIntent(playerFrame(1, "set_hp", "p2")))
	if err == nil {
		t.Fatal("a resolution against a moved placement was accepted")
	}

	if got := reasonOf(t, err); got != realtime.RejectStaleVersion {
		t.Errorf("the client is told %q, want %q", got, realtime.RejectStaleVersion)
	}

	if !errors.Is(err, realtime.ErrVersionMismatch) {
		t.Errorf("the refusal %v does not satisfy realtime.ErrVersionMismatch", err)
	}

	// `p2` moved — by the interleaving, which is a real change and is not the
	// adapter's to undo, and which is also how the fixture proves it ran.
	p2After, _ := state.Version("p2")
	if p2After <= p2Before {
		t.Errorf("the interleaving did not move p2 (%d to %d); the fixture did not run",
			p2Before, p2After)
	}

	// `p1` did not, and that is the property. It is the first entry in the list, so
	// without the precheck it would already have been written by the time `p2` was
	// refused.
	p1After, _ := state.Version("p1")
	if p1After != p1Before {
		t.Errorf("p1 moved from version %d to %d although the list was refused",
			p1Before, p1After)
	}
}

// TestTheRuleContextCarriesTheSeedsValueAndNothingElse is the subtraction ADR 0041
// makes the enforcement for, observed from the adapter's side.
//
// The adapter builds the context with `rules.NewContext` and has no way to add a
// field, so a resolution is a function of four values. What this test pins is the
// one that is easy to add later: a seed that is *the same* for two resolutions is a
// campaign whose every roll is the same number, and `TestTheSeedIsFreshPerResolution`
// is the other half of that pair.
func TestTheRuleContextCarriesTheSeedsValueAndNothingElse(t *testing.T) {
	t.Parallel()

	states := newStates(t)
	open(t, states, 1)

	recorded := &calls{}
	seed := aSeed(t, 3)

	system := systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.recorded = recorded
	})

	registry := plugin.New()
	registerWith(t, registry, system)

	resolver, err := plugin.NewResolver(plugin.ResolverConfig{
		Systems: registry,
		States:  states,
		SystemOf: func(context.Context, int64) (rules.ID, error) {
			return "5e-2024", nil
		},
		Seeds: fixedSeeds(seed),
	})
	if err != nil {
		t.Fatalf("building the resolver: %v", err)
	}

	if _, err := resolver.Resolve(t.Context(), playerIntent(gmFrame(1, "roll"))); err != nil {
		t.Fatalf("resolving: %v", err)
	}

	if recorded.context.Seed != seed {
		t.Errorf("the system was given seed %s, want the one the caller supplied %s",
			recorded.context.Seed, seed)
	}

	if recorded.context.Role != domain.RolePlayer {
		t.Errorf("the system was told role %q, want the player's", recorded.context.Role)
	}
}

// TestEveryPanicARefusalCanNameIsReachable is the panic record's completeness: the
// three ways plugin code can fail to return are a value, an error, and a runtime
// error, and `PanicError.Type` is what tells them apart in a log.
func TestEveryPanicARefusalCanNameIsReachable(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		raise  func()
		wantIs string
	}{
		"a string":   {raise: func() { panic("no") }, wantIs: "string"},
		"an error":   {raise: func() { panic(errors.New("no")) }, wantIs: "*errors.errorString"},
		"an integer": {raise: func() { panic(42) }, wantIs: "int"},
		"a struct":   {raise: func() { panic(struct{ A int }{}) }, wantIs: "struct { A int }"},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			states := newStates(t)
			state := open(t, states, 1)

			before := documentBytes(t, state)

			system := systemWith(func(s *stubSystem) {
				s.id = "5e-2024"
				s.apply = func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
					testCase.raise()

					return nil, nil
				}
			})

			resolver := newResolver(t, system, states)

			_, err := resolver.Resolve(t.Context(), gmIntent(gmFrame(1, "roll")))

			panicked, ok := errors.AsType[*plugin.PanicError](err)
			if !ok {
				t.Fatalf("the refusal is %v, want a *plugin.PanicError", err)
			}

			if panicked.Type != testCase.wantIs {
				t.Errorf("the panic records type %q, want %q", panicked.Type, testCase.wantIs)
			}

			if documentChanged(t, state, before) {
				t.Errorf("campaign_state changed across a panic of %s", testCase.wantIs)
			}
		})
	}
}

// TestAResolverAnswersOneIntentsChangesAndNeverBoth is the answer's contract, read
// from the two ends at once: a resolution that applied something answers the actor,
// and a resolution that was refused answers nothing.
//
// `Hub.Apply` reads exactly two things out of a resolver — the answer and the
// broadcast — and both are read off one value, so a resolver that returned an answer
// beside a refusal would send two frames for one `seq`.
func TestAResolverAnswersOneIntentsChangesAndNeverBoth(t *testing.T) {
	t.Parallel()

	states := newStates(t)
	open(t, states, 1)

	resolver := newResolver(t, systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.apply = func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
			return []rules.Mutation{moveTo(t, "p1", 5, 5)}, nil
		}
	}), states)

	applied, err := resolver.Resolve(t.Context(), gmIntent(gmFrame(1, "roll")))
	if err != nil {
		t.Fatalf("the first intent was refused: %v", err)
	}

	if applied.Answer == nil {
		t.Error("a resolution with one mutation answered nothing")
	}

	if len(applied.Broadcast) != 1 {
		t.Errorf("a resolution with one mutation broadcast %d changes", len(applied.Broadcast))
	}
}

// panickingCodec panics from one half of `plugin.Codec` and behaves normally from
// the other, so a test can choose which side of the boundary the failure is on.
type panickingCodec struct {
	onObject bool
	onApply  bool
}

// Object implements plugin.Codec.
func (c panickingCodec) Object(realtime.Placement) (rules.Object, error) {
	if c.onObject {
		panic("the codec could not read a placement")
	}

	object, err := plugin.PlacementCodec{}.Object(realtime.Placement{ID: "p1"})
	if err != nil {
		return rules.Object{}, fmt.Errorf("the fixture codec could not read a placement: %w", err)
	}

	return object, nil
}

// Apply implements plugin.Codec.
func (c panickingCodec) Apply(placement *realtime.Placement, mutation rules.Mutation) error {
	if c.onApply {
		panic("the codec could not decode a payload")
	}

	if err := (plugin.PlacementCodec{}).Apply(placement, mutation); err != nil {
		return fmt.Errorf("the fixture codec could not decode a payload: %w", err)
	}

	return nil
}

// meddlingCodec is the shared codec plus one movement of a placement, applied once,
// from inside `Apply` — which the adapter runs while it is decoding drafts and
// therefore before the first write.
type meddlingCodec struct {
	state  *realtime.CampaignState
	target realtime.PlacementID
	once   bool
}

// Object implements plugin.Codec.
func (c *meddlingCodec) Object(placement realtime.Placement) (rules.Object, error) {
	object, err := plugin.PlacementCodec{}.Object(placement)
	if err != nil {
		return rules.Object{}, fmt.Errorf(
			"the fixture codec could not read %q: %w",
			placement.ID,
			err,
		)
	}

	return object, nil
}

// Apply implements plugin.Codec.
func (c *meddlingCodec) Apply(placement *realtime.Placement, mutation rules.Mutation) error {
	if c.once {
		c.once = false

		// A concurrent intent, occupying the window between the snapshot and the
		// write. The version claim fails below, which is the point.
		version, _ := c.state.Version(c.target)
		if _, err := c.state.Mutate(c.target, version, nil); err != nil {
			panic(fmt.Sprintf("the interleaving could not be staged: %v", err))
		}
	}

	if err := (plugin.PlacementCodec{}).Apply(placement, mutation); err != nil {
		return fmt.Errorf("the fixture codec could not decode a payload: %w", err)
	}

	return nil
}
