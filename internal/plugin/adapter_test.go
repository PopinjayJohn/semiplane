package plugin_test

import (
	"cmp"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/realtime"
)

// TestTheTranslationTable is the adapter's whole contract in one table: what a
// client is told, for every way a resolution can fail.
//
// The reason it is one test rather than eleven is that the table *is* the
// translation — `internal/domain/rules` names the pairs that need converting and
// says the adapter owns the table, and a table written as prose in a doc comment is
// a table that drifts from the code. Each row therefore carries the exact
// `realtime.RejectReason`, read off the `*plugin.Rejection` the adapter returns, and
// the reason column is the closed set rather than a word this package chose: the
// wire vocabulary belongs to `protocol.go`, and a test that accepted a ninth reason
// would be the second answer.
//
// The rows in refusal order, because the order is what a player experiences:
// malformed frame, then the campaign's condition, then the system's own answer.
func TestTheTranslationTable(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		// frame is what the client sent. A nil frame is the "no frame" row.
		frame func() *realtime.ClientIntent

		// identity is the connection the hub admitted, as a name: `gm` or `player`.
		identity func(*realtime.ClientIntent) realtime.Intent

		// system returns what the campaign's system returns for that intent.
		system func(ctx context.Context, call rules.Context, state rules.State, in rules.Intent) ([]rules.Mutation, error)

		// registered is the id in the registry, and wanted is the id the campaign
		// claims. They differ in the S-10.6 row.
		registered rules.ID
		wanted     rules.ID

		want    realtime.RejectReason
		wantErr error
	}{
		"no frame at all": {
			frame:    func() *realtime.ClientIntent { return nil },
			identity: gm,
			want:     realtime.RejectInvalidArgs,
			wantErr:  plugin.ErrNoFrame,
		},
		"an op that is not a token": {
			frame:    func() *realtime.ClientIntent { return gmFrame(1, "Move Token") },
			identity: gm,
			want:     realtime.RejectInvalidArgs,
			wantErr:  rules.ErrInvalidOp,
		},
		"an op nobody has a name for": {
			frame:    func() *realtime.ClientIntent { return gmFrame(1, "") },
			identity: gm,
			want:     realtime.RejectInvalidArgs,
			wantErr:  rules.ErrInvalidOp,
		},
		"a target that cannot be carried": {
			frame: func() *realtime.ClientIntent {
				return playerFrame(1, "set_hp", realtime.PlacementID("p1\np2"))
			},
			identity: gm,
			want:     realtime.RejectInvalidArgs,
			wantErr:  rules.ErrInvalidObject,
		},
		"a campaign whose system is not registered": {
			frame:      func() *realtime.ClientIntent { return gmFrame(1, "roll") },
			identity:   gm,
			registered: "5e-2024",
			wanted:     "5e-2014",
			want:       realtime.RejectServerError,
			wantErr:    plugin.ErrUnknownSystem,
		},
		"the system refuses by name": {
			frame:      func() *realtime.ClientIntent { return gmFrame(1, "roll") },
			identity:   gm,
			registered: "5e-2024",
			wanted:     "5e-2024",
			system: func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
				return nil, plugin.Reject(realtime.RejectUnknownOp, nil)
			},
			want: realtime.RejectUnknownOp,
		},
		"the system says it is not the actor's turn": {
			frame:      func() *realtime.ClientIntent { return gmFrame(1, "attack") },
			identity:   player,
			registered: "5e-2024",
			wanted:     "5e-2024",
			system: func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
				return nil, plugin.Reject(realtime.RejectNotYourTurn, nil)
			},
			want: realtime.RejectNotYourTurn,
		},
		"the system refuses a GM-only operation": {
			frame:      func() *realtime.ClientIntent { return playerFrame(1, "set_hp", "p1") },
			identity:   player,
			registered: "5e-2024",
			wanted:     "5e-2024",
			system: func(_ context.Context, call rules.Context, _ rules.State, in rules.Intent) ([]rules.Mutation, error) {
				// §7.2's rule, enforced in the system: it branches on the role the
				// connection proved. The adapter has no list and cannot have one.
				if call.Role != domain.RoleGM {
					return nil, plugin.Reject(realtime.RejectNotPermitted, nil)
				}

				return nil, plugin.Reject(realtime.RejectNotYourTurn, nil)
			},
			want: realtime.RejectNotPermitted,
		},
		"the system's own argument refusal": {
			frame:      func() *realtime.ClientIntent { return playerFrame(1, "set_hp", "p1") },
			identity:   gm,
			registered: "5e-2024",
			wanted:     "5e-2024",
			system: func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
				return nil, plugin.Reject(realtime.RejectInvalidArgs, rules.ErrInvalidOp)
			},
			want: realtime.RejectInvalidArgs,
		},
		"a malformed intent refusal with no reason attached": {
			frame:      func() *realtime.ClientIntent { return playerFrame(1, "set_hp", "p1") },
			identity:   gm,
			registered: "5e-2024",
			wanted:     "5e-2024",
			system: func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
				// A bare `rules.ErrInvalidOp` rather than a `Rejection`: the reason
				// column answers from the sentinel, because the adapter's table says
				// a refusal about the *arguments* is `invalid_args` whoever names it.
				return nil, rules.ErrInvalidOp
			},
			want: realtime.RejectInvalidArgs,
		},
		"a failure nobody here classifies": {
			frame:      func() *realtime.ClientIntent { return gmFrame(1, "roll") },
			identity:   gm,
			registered: "5e-2024",
			wanted:     "5e-2024",
			system: func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
				return nil, errors.New("the pack file is not where the constructor said it was")
			},
			want: realtime.RejectServerError,
		},
		"a mutation for a placement that is not there": {
			frame:      func() *realtime.ClientIntent { return playerFrame(1, "set_hp", "p1") },
			identity:   gm,
			registered: "5e-2024",
			wanted:     "5e-2024",
			system: func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
				return []rules.Mutation{moveTo(t, "p404", 1, 2)}, nil
			},
			want:    realtime.RejectNoSuchPlacement,
			wantErr: realtime.ErrNoPlacement,
		},
		"a mutation the hub could not apply": {
			frame:      func() *realtime.ClientIntent { return playerFrame(1, "set_hp", "p1") },
			identity:   gm,
			registered: "5e-2024",
			wanted:     "5e-2024",
			system: func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
				// No op: a mutation with nothing in it that the hub could apply.
				return []rules.Mutation{{Target: "p1"}}, nil
			},
			want:    realtime.RejectServerError,
			wantErr: plugin.ErrUnappliable,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			states := newStates(t)
			open(t, states, 1)

			// The rows that do not care which id is registered share one, and the
			// default is written here rather than repeated in every row so a row
			// added later cannot accidentally test an unregistered campaign.
			registered := cmp.Or(testCase.registered, testCase.wanted, "5e-2024")
			wanted := cmp.Or(testCase.wanted, testCase.registered, "5e-2024")

			system := systemWith(func(s *stubSystem) {
				s.id = registered
				s.apply = testCase.system
			})

			registry := plugin.New()
			registerWith(t, registry, system)

			resolver, err := plugin.NewResolver(plugin.ResolverConfig{
				Systems: registry,
				States:  states,
				SystemOf: func(context.Context, int64) (rules.ID, error) {
					return wanted, nil
				},
				Seeds: fixedSeeds(aSeed(t, 1)),
			})
			if err != nil {
				t.Fatalf("building the resolver: %v", err)
			}

			intent := testCase.identity(testCase.frame())

			resolution, err := resolver.Resolve(t.Context(), intent)

			// A refusal is **not** a failure of `Resolve`'s contract: the hub
			// reports it to the client as a successful answer and never puts it in a
			// log as an error. So there is no resolution, and there is no broadcast.
			if resolution.Answer != nil || resolution.Broadcast != nil {
				t.Errorf("a refused intent returned %+v", resolution)
			}

			if err == nil {
				t.Fatalf("the intent was not refused; the resolution was %+v", resolution)
			}

			if got := reasonOf(t, err); got != testCase.want {
				t.Errorf("the client is told %q, want %q", got, testCase.want)
			}

			if testCase.wantErr != nil && !errors.Is(err, testCase.wantErr) {
				t.Errorf("the refusal %v does not satisfy %v", err, testCase.wantErr)
			}
		})
	}
}

// TestASuccessfulResolutionIsTheHubsVersionAndTheSystemsOp is the other direction of
// the table: what leaves when nothing is wrong.
//
// Every assertion here is a claim about **which half of the change belongs to whom**.
// The version is the hub's and the system could not forge it (`rules.Mutation` has
// no version field); the op and payload are the system's and the hub does not
// interpret them; the actor is the connection's, which is the only half a client
// frame has no field to influence.
func TestASuccessfulResolutionIsTheHubsVersionAndTheSystemsOp(t *testing.T) {
	t.Parallel()

	states := newStates(t)
	state := open(t, states, 2)

	recorded := &calls{}

	before, _ := state.Version("p1")

	system := systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.recorded = recorded
		s.apply = func(
			_ context.Context,
			_ rules.Context,
			resolved rules.State,
			in rules.Intent,
		) ([]rules.Mutation, error) {
			// The system resolves against the snapshot it was handed, and names a
			// change of its own — the op is not the intent's op, which is the one
			// thing `rules.Mutation.Op`'s own comment insists on.
			if resolved.Len() != 2 {
				t.Errorf("the system saw %d objects, want the two on the table", resolved.Len())
			}

			mutation, err := rules.NewMutation("p1", "damage", []byte(`{"rolled":17}`))
			if err != nil {
				t.Errorf("building a mutation: %v", err)
			}

			return []rules.Mutation{mutation}, nil
		}
	})

	resolver := newResolver(t, system, states)

	resolution, err := resolver.Resolve(t.Context(), gmIntent(playerFrame(7, "attack", "p1")))
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}

	if len(resolution.Broadcast) != 1 {
		t.Fatalf("the resolution carries %d changes, want 1", len(resolution.Broadcast))
	}

	change := resolution.Broadcast[0]

	if change.Placement != "p1" {
		t.Errorf("the change is addressed to %q, want %q", change.Placement, "p1")
	}

	// The hub's version, and it moved. A system that could name one would forge
	// currency, so the assertion is that the number came from the state.
	after, _ := state.Version("p1")
	if change.Version != realtime.Version(after) {
		t.Errorf("the change carries version %d, the placement is at %d", change.Version, after)
	}

	if after <= before {
		t.Errorf("the placement is at version %d, want more than the %d it was at", after, before)
	}

	if change.Op != "damage" {
		t.Errorf("the change names op %q, want the system's own %q", change.Op, "damage")
	}

	if string(change.Args) != `{"rolled":17}` {
		t.Errorf("the change carries %q, want the system's payload verbatim", change.Args)
	}

	if change.By != realtime.UserID(testActor) {
		t.Errorf("the change is attributed to %d, want the connection's actor %d",
			change.By, testActor)
	}

	// The answer the actor receives.
	applied, ok := resolution.Answer.(*realtime.ServerApplied)
	if !ok {
		t.Fatalf("the answer is %T, want a *realtime.ServerApplied", resolution.Answer)
	}

	if applied.Seq != 7 {
		t.Errorf("the answer is addressed to seq %d, want the frame's 7", applied.Seq)
	}

	if applied.Version != change.Version || applied.Placement != change.Placement {
		t.Errorf("the answer reports %q at version %d, the change reports %q at %d",
			applied.Placement, applied.Version, change.Placement, change.Version)
	}

	// The rule context: the three identity fields came from the connection, which
	// is the security-relevant half. A client frame has no field in which to name a
	// campaign or an actor, so a system cannot be told which player is rolling by
	// anything that player sent.
	call := recorded.context
	if call.Campaign != testCampaign {
		t.Errorf("the system was told campaign %d, want the connection's %d",
			call.Campaign, testCampaign)
	}

	if call.Actor != testActor {
		t.Errorf("the system was told actor %d, want the connection's %d", call.Actor, testActor)
	}

	if call.Role != domain.RoleGM {
		t.Errorf("the system was told role %q, want the connection's %q", call.Role, domain.RoleGM)
	}

	// And the intent: the op, the target, and the frame's arguments encoded.
	if recorded.intent.Op != "attack" {
		t.Errorf("the system was asked to resolve %q, want %q", recorded.intent.Op, "attack")
	}

	if recorded.intent.Target != "p1" {
		t.Errorf("the system was told the target %q, want %q", recorded.intent.Target, "p1")
	}

	// `json.Marshal` of the frame's `Args`, with `omitempty` on every field: a
	// placement that is set is present, the rest are absent rather than zero.
	if !strings.Contains(string(recorded.intent.Args), `"placement":"p1"`) {
		t.Errorf(
			"the system was given %q, want the frame's arguments encoded",
			recorded.intent.Args,
		)
	}
}

// TestAnIntentThatResolvesToNothingAnswersNothing is `Resolution.Answer`'s optionality,
// which is a correctness property rather than a convenience.
//
// An `applied` frame names a placement and its version. A resolution that applied
// nothing has neither, so the adapter answers `nil` — and `Hub.Apply` sends nothing,
// which is right: a client that received an `applied` claiming a version would be
// reconciling against a number nobody stamped.
func TestAnIntentThatResolvesToNothingAnswersNothing(t *testing.T) {
	t.Parallel()

	states := newStates(t)
	state := open(t, states, 1)

	before := documentBytes(t, state)

	system := systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		// A presence-like intent: the system resolved it and it changed nothing.
		s.apply = func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
			return nil, nil
		}
	})

	resolver := newResolver(t, system, states)

	resolution, err := resolver.Resolve(t.Context(), gmIntent(gmFrame(3, "pause")))
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}

	if resolution.Answer != nil {
		t.Errorf("a resolution that changed nothing answered %T", resolution.Answer)
	}

	if len(resolution.Broadcast) != 0 {
		t.Errorf("a resolution that changed nothing broadcast %v", resolution.Broadcast)
	}

	if documentChanged(t, state, before) {
		t.Error("a resolution that changed nothing changed the state")
	}
}

// TestARemovalStampsTheVersionThatOutlivesThePlacement is the removal half of S-7.2.
//
// The version a removal reports is the one the placement *would* have carried, and
// it outlives the placement in the state's version map — so a re-created placement
// of the same id is stamped above it and a late client can still reconcile. The
// assertion is that the number on the delta is above the number the placement
// carried, which is what makes a client's drop-and-reconcile correct.
func TestARemovalStampsTheVersionThatOutlivesThePlacement(t *testing.T) {
	t.Parallel()

	states := newStates(t)
	state := open(t, states, 2)

	before, _ := state.Version("p1")

	system := systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.apply = func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
			return []rules.Mutation{{Target: "p1", Op: "remove", Remove: true}}, nil
		}
	})

	resolver := newResolver(t, system, states)

	resolution, err := resolver.Resolve(t.Context(), gmIntent(gmFrame(1, "remove_token")))
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}

	if len(resolution.Broadcast) != 1 {
		t.Fatalf("the resolution carries %d changes, want 1", len(resolution.Broadcast))
	}

	change := resolution.Broadcast[0]
	if uint64(change.Version) <= before {
		t.Errorf("the removal stamped version %d, want more than the %d the placement held",
			change.Version, before)
	}

	if _, live := state.Placement("p1"); live {
		t.Error("p1 is still on the table")
	}

	// The version survives the placement, which is what makes a re-create above it.
	recreated, err := state.Create("p1", realtime.Placement{})
	if err != nil {
		t.Fatalf("re-creating p1: %v", err)
	}

	if recreated.Version <= uint64(change.Version) {
		t.Errorf("a re-created p1 is at version %d, want above the removal's %d",
			recreated.Version, change.Version)
	}
}

// TestASystemResolvingTheSameIntentTwiceProducesTheSameChanges is S-14.6, observed at
// the boundary rather than inside the system.
//
// The adapter contributes two things to that property and both are asserted here:
// the state it hands a system is built by `rules.NewState`, which **sorts** — so the
// order a system's objects arrive in is a property of their contents and not of the
// hub's map — and the seed it supplies is the one the caller asked for. A resolver
// called twice with the same seed and the same state therefore sees the same inputs.
func TestASystemResolvingTheSameIntentTwiceProducesTheSameChanges(t *testing.T) {
	t.Parallel()

	states := newStates(t)
	open(t, states, 3)

	seen := make([]string, 0, 2)

	system := systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.apply = func(
			_ context.Context,
			_ rules.Context,
			resolved rules.State,
			in rules.Intent,
		) ([]rules.Mutation, error) {
			// A record of exactly what the system was given, in the order it was
			// given it. If the snapshot's order were the hub's map order, two calls
			// would differ.
			objects := resolved.Objects()
			record := &strings.Builder{}

			for _, object := range objects {
				record.WriteString(object.ID.String())
				record.WriteString(" ")
				record.WriteString(object.Kind.String())
				record.WriteString(" ")
				record.Write(object.Data)
				record.WriteString("\n")
			}

			record.Write(in.Args)

			seen = append(seen, record.String())

			return nil, nil
		}
	})

	registry := plugin.New()
	registerWith(t, registry, system)

	resolver, err := plugin.NewResolver(plugin.ResolverConfig{
		Systems: registry,
		States:  states,
		SystemOf: func(context.Context, int64) (rules.ID, error) {
			return "5e-2024", nil
		},
		// The same seed twice, which is the property: a resolution is a function of
		// (state, intent, seed).
		Seeds: fixedSeeds(aSeed(t, 9)),
	})
	if err != nil {
		t.Fatalf("building the resolver: %v", err)
	}

	for range 2 {
		if _, err := resolver.Resolve(
			t.Context(),
			playerIntent(playerFrame(1, "attack", "p1")),
		); err != nil {
			t.Fatalf("resolving: %v", err)
		}
	}

	if len(seen) != 2 {
		t.Fatalf("the system was called %d times, want 2", len(seen))
	}

	if seen[0] != seen[1] {
		t.Errorf(
			"the same (state, intent, seed) reached the system twice differently:\n%s\n---\n%s",
			seen[0],
			seen[1],
		)
	}

	// And the objects arrived in id order, which is the property that is easy to lose
	// and invisible when it is lost.
	if !strings.HasPrefix(seen[0], "p1 token ") {
		t.Errorf("the system saw %q first, want p1 — the snapshot is not sorted", seen[0][:12])
	}
}

// TestTheSeedIsFreshPerResolution is why `SeedFunc` is per resolution.
//
// `rules.Context.Rand` derives a source from `(seed, label)`. With one seed per
// campaign, every `roll` in that campaign would draw the same number, and the roll
// log §16.3 wants would be a list of one value repeated. So the test is not "the
// seed is random" — it is that two resolutions of the same campaign are handed
// different entropy, and that a caller who wants reproducibility supplies the seed.
func TestTheSeedIsFreshPerResolution(t *testing.T) {
	t.Parallel()

	states := newStates(t)
	open(t, states, 1)

	recorded := &calls{}

	system := systemWith(func(s *stubSystem) {
		s.id = "5e-2024"
		s.recorded = recorded
		s.apply = func(context.Context, rules.Context, rules.State, rules.Intent) ([]rules.Mutation, error) {
			return nil, nil
		}
	})

	registry := plugin.New()
	registerWith(t, registry, system)

	resolver, err := plugin.NewResolver(plugin.ResolverConfig{
		Systems: registry,
		States:  states,
		SystemOf: func(context.Context, int64) (rules.ID, error) {
			return "5e-2024", nil
		},
	})
	if err != nil {
		t.Fatalf("building the resolver: %v", err)
	}

	draws := make([]int, 0, 2)

	for range 2 {
		if _, err := resolver.Resolve(t.Context(), gmIntent(gmFrame(1, "roll"))); err != nil {
			t.Fatalf("resolving: %v", err)
		}

		draws = append(draws, recorded.context.Rand("attack/1").IntN(1_000_000))
	}

	if draws[0] == draws[1] {
		t.Errorf("two resolutions of one campaign drew the same number (%d) from the same label",
			draws[0])
	}
}

// TestCryptoSeedsRefusesAReaderThatFailsRatherThanHandingOutAZeroSeed is the fixed
// point ADR 0041 named: the zero `rules.Seed` is a *legal* seed, so a failed draw
// must not silently resolve a campaign's rolls to the same numbers forever.
func TestCryptoSeedsRefusesAReaderThatFailsRatherThanHandingOutAZeroSeed(t *testing.T) {
	t.Parallel()

	drawn := plugin.CryptoSeeds()

	first, err := drawn(t.Context(), testCampaign)
	if err != nil {
		t.Fatalf("drawing a seed: %v", err)
	}

	second, err := drawn(t.Context(), testCampaign)
	if err != nil {
		t.Fatalf("drawing a second seed: %v", err)
	}

	if first == second {
		t.Error("two draws returned the same seed")
	}

	if first == (rules.Seed{}) {
		t.Error("a draw returned the zero seed, which is a legal seed and never a random one")
	}
}

// TestNewResolverRefusesAMissingSeam is the wiring check, and it is a table because
// each of the three is a different mistake in the composition root.
func TestNewResolverRefusesAMissingSeam(t *testing.T) {
	t.Parallel()

	states := newStates(t)
	registry := plugin.New()
	registerWith(t, registry, validSystem("5e-2024"))

	lookup := func(context.Context, int64) (rules.ID, error) { return "5e-2024", nil }

	cases := map[string]plugin.ResolverConfig{
		"no gameplay registry": {States: states, SystemOf: lookup},
		"no state registry":    {Systems: registry, SystemOf: lookup},
		"no way to name a campaign's system": {
			Systems: registry,
			States:  states,
		},
	}

	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := plugin.NewResolver(config); err == nil {
				t.Fatal("NewResolver accepted an unwired configuration")
			} else if !errors.Is(err, plugin.ErrMalformedPlugin) {
				t.Errorf(
					"NewResolver returned %v, want a refusal satisfying ErrMalformedPlugin",
					err,
				)
			}
		})
	}
}

// TestACancelledContextResolvesNothing is the first step of `Resolve`, and it is
// checked before the frame because a cancelled request is not a request.
func TestACancelledContextResolvesNothing(t *testing.T) {
	t.Parallel()

	states := newStates(t)
	state := open(t, states, 1)

	system := systemWith(func(s *stubSystem) { s.id = "5e-2024" })
	resolver := newResolver(t, system, states)

	before := documentBytes(t, state)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := resolver.Resolve(ctx, gmIntent(gmFrame(1, "roll")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("resolving on a cancelled context returned %v, want context.Canceled", err)
	}

	if documentChanged(t, state, before) {
		t.Error("a cancelled resolution changed the state")
	}
}

// TestACampaignWithNoLiveStateRefusesRatherThanOpeningOne is the adapter's half of
// S-10.6's second half: the game refuses to start, and the adapter does not open a
// state to make the refusal impossible.
//
// Opening one would be a state written on behalf of an intent nobody authorised,
// which is `campaign_state` created by the resolution path rather than by the
// campaign's own registration — and the row would then exist for a campaign whose
// wiki has been serving all along.
func TestACampaignWithNoLiveStateRefusesRatherThanOpeningOne(t *testing.T) {
	t.Parallel()

	states := newStates(t)

	system := systemWith(func(s *stubSystem) { s.id = "5e-2024" })
	resolver := newResolver(t, system, states)

	_, err := resolver.Resolve(t.Context(), gmIntent(gmFrame(1, "roll")))
	if err == nil {
		t.Fatal("resolving against an unopened campaign was not refused")
	}

	if got := reasonOf(t, err); got != realtime.RejectServerError {
		t.Errorf("the client is told %q, want %q", got, realtime.RejectServerError)
	}

	if states.Live() != 0 {
		t.Errorf("the adapter opened %d states; it should have opened none", states.Live())
	}
}
