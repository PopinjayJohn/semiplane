package plugin_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/realtime"
)

// This file is the fixtures every other test in the package shares: a
// configurable gameplay system, a real `realtime.Registry` over a writer that
// writes nothing, and the two helpers that put a placement on a table.
//
// The state registry is **real** rather than a fake, and that is the decision
// worth stating. S-10.2's promise is about `campaign_state`, and a test that
// asserted it against a stub would be asserting it about the stub. Everything the
// containment tests claim is measured by encoding the hub's own document before
// and after a panicking system.

// the campaign every fixture plays on.
const (
	testCampaign int64 = 42
	testActor    int64 = 7
)

// newStates returns a live state registry whose writes go nowhere.
//
// The writer succeeds and calls nothing, and the reader reports no row: this is a
// registry that holds real state in memory and persists none of it, which is what
// a test of the in-memory half wants. It is not a mock of `CampaignState` — every
// lock, version and stamp in these tests is the hub's own code.
func newStates(t *testing.T) *realtime.Registry {
	t.Helper()

	states := realtime.NewRegistry(t.Context(), realtime.Config{
		Write: func(context.Context, func(context.Context, *sql.Tx) error) error {
			return nil
		},
		Read: func(context.Context, int64) (realtime.Persisted, error) {
			return realtime.Persisted{}, realtime.ErrNoState
		},
		// An hour of debounce, so no test ever waits on a flush and no scheduler
		// writes behind a test's back.
		Cadence: realtime.Cadence{Debounce: time.Hour},
	})

	t.Cleanup(func() {
		// `WithoutCancel`: `Close` is the flush, and `t.Context()` is done by the
		// time cleanup runs, so handing it the test's own context is the crash case
		// `CampaignState.Close`'s doc warns about.
		if err := states.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("closing the state registry: %v", err)
		}
	})

	return states
}

// open returns the live state for one campaign, with `count` placements on it.
func open(t *testing.T, states *realtime.Registry, count int) *realtime.CampaignState {
	t.Helper()

	state, err := states.Open(t.Context(), testCampaign)
	if err != nil {
		t.Fatalf("opening campaign %d: %v", testCampaign, err)
	}

	for _, id := range placements(count) {
		if _, err := state.Create(
			id,
			realtime.Placement{X: 10, Y: 20, HP: 10, MaxHP: 10},
		); err != nil {
			t.Fatalf("placing %q: %v", id, err)
		}
	}

	return state
}

// placements returns count placement ids, in the order `Create` is called.
func placements(count int) []realtime.PlacementID {
	ids := make([]realtime.PlacementID, 0, count)
	for index := range count {
		ids = append(ids, placementID(index))
	}

	return ids
}

// placementID is the nth fixture placement.
func placementID(index int) realtime.PlacementID {
	return realtime.PlacementID("p" + string(rune('1'+index)))
}

// documentBytes encodes a campaign's state, for the byte-identical comparisons.
func documentBytes(t *testing.T, state *realtime.CampaignState) []byte {
	t.Helper()

	encoded, err := realtime.EncodeDocument(state.Snapshot())
	if err != nil {
		t.Fatalf("encoding the state: %v", err)
	}

	return encoded
}

// documentChanged reports whether a campaign's state no longer encodes to before.
//
// The comparison is `bytes.Equal` rather than two string conversions, which is what
// the linter asks for and which is also honest: these are documents, and a
// one-character difference in one is the entire question.
func documentChanged(t *testing.T, state *realtime.CampaignState, before []byte) bool {
	t.Helper()

	return !bytes.Equal(documentBytes(t, state), before)
}

// reasonOf reads the wire reason off a resolver's error.
//
// The adapter's error carries two things — the reason, which goes to the client,
// and the detail, which goes to the log — and a test that asserted only
// `errors.Is(err, ErrSomething)` would be asserting the half nobody receives.
func reasonOf(t *testing.T, err error) realtime.RejectReason {
	t.Helper()

	rejection, ok := errors.AsType[*plugin.RejectionError](err)
	if !ok {
		t.Fatalf("the refusal carries no reason: %v", err)
	}

	return rejection.Reason
}

// calls records what a system was handed, so a test can assert on the rule context
// rather than on the fact that `Apply` ran.
type calls struct {
	context rules.Context
	intent  rules.Intent
	state   rules.State
	count   int
}

// stubSystem is a gameplay system with every method a test can steer.
//
// The zero value is **not** valid — `rules.Validate` refuses a system with no id —
// so `validSystem` is the starting point and each test departs from it by naming
// the one field it is about.
type stubSystem struct {
	id      rules.ID
	title   string
	version string
	kinds   []rules.Kind
	views   []rules.View
	grammar rules.Grammar

	// apply is the resolution. Nil means "no mutations, no error", which is a legal
	// answer and the one most tests do not care about.
	apply func(ctx context.Context, call rules.Context, state rules.State, in rules.Intent) ([]rules.Mutation, error)

	// recorded is where the last call is written, so a test can read the context.
	recorded *calls
}

// validSystem is a system `rules.Validate` accepts, so a test about the registry
// does not accidentally fail on the fixture.
func validSystem(id rules.ID) *stubSystem {
	return &stubSystem{
		id:      id,
		title:   "Fixture system",
		version: "fixture-1",
		kinds:   []rules.Kind{"spell", "class"},
		grammar: rules.Grammar{
			Notation: "d20",
			Terms:    []rules.Term{{Name: "roll", Pattern: `^[0-9]+d[0-9]+$`}},
		},
	}
}

// ID implements rules.System.
func (s *stubSystem) ID() rules.ID { return s.id }

// Title implements rules.System.
func (s *stubSystem) Title() string { return s.title }

// RulesetVersion implements rules.System.
func (s *stubSystem) RulesetVersion() string { return s.version }

// Grammar implements rules.System.
func (s *stubSystem) Grammar() rules.Grammar { return s.grammar }

// Parse implements rules.System. The fixtures never parse: `Parse` is the authority
// `Apply` calls on the system's behalf in real code, and a fixture that resolved
// arguments would be testing the fixture.
func (s *stubSystem) Parse(string) (rules.Expr, error) {
	return rules.NewExpr(s.id, s.grammar.Notation, "1d20", nil), nil
}

// Apply implements rules.System, and records what it was handed.
func (s *stubSystem) Apply(
	ctx context.Context,
	call rules.Context,
	state rules.State,
	in rules.Intent,
) ([]rules.Mutation, error) {
	if s.recorded != nil {
		s.recorded.context = call
		s.recorded.intent = in
		s.recorded.state = state
		s.recorded.count++
	}

	if s.apply == nil {
		return nil, nil
	}

	return s.apply(ctx, call, state, in)
}

// Derive implements rules.System.
func (s *stubSystem) Derive(rules.State, rules.Query) (rules.Payload, error) {
	payload, err := rules.NewPayload("sheet", struct{}{})
	if err != nil {
		return rules.Payload{}, fmt.Errorf("the fixture could not build a payload: %w", err)
	}

	return payload, nil
}

// Views implements rules.System.
func (s *stubSystem) Views() []rules.View { return s.views }

// ContentKinds implements rules.System.
func (s *stubSystem) ContentKinds() []rules.Kind { return s.kinds }

// opSystem is a stub that also answers `plugin.Operations`, which is the optional
// interface S-10.3's "may a UI plugin emit an op some system resolves" is asked of.
//
// A separate type rather than a field, because Go decides interface satisfaction
// by the method set: a `stubSystem` with an `ops` field would still not implement
// `Resolves`, and the two cases must be distinguishable — the second is the safe
// direction and `TestResolvesTrustsTheSystemThatDoesNotAnswer` is about it.
type opSystem struct {
	*stubSystem
	ops []rules.Op
}

// Resolves implements plugin.Operations.
func (s *opSystem) Resolves(op rules.Op) bool {
	return slices.Contains(s.ops, op)
}

// registerWith adds a system and the shared codec, and fails the test if the
// registry refuses it.
func registerWith(t *testing.T, registry *plugin.Registry, system rules.System) {
	t.Helper()

	if err := registry.Register(
		plugin.Entry{System: system, Codec: plugin.PlacementCodec{}},
	); err != nil {
		t.Fatalf("registering %q: %v", system.ID(), err)
	}
}

// newResolver builds the hub's resolver over one system and one open campaign.
func newResolver(t *testing.T, system rules.System, states *realtime.Registry) *plugin.Resolver {
	t.Helper()

	registry := plugin.New()
	registerWith(t, registry, system)

	resolver, err := plugin.NewResolver(plugin.ResolverConfig{
		Systems: registry,
		States:  states,
		SystemOf: func(context.Context, int64) (rules.ID, error) {
			return system.ID(), nil
		},
	})
	if err != nil {
		t.Fatalf("building the resolver: %v", err)
	}

	return resolver
}

// fixedSeeds returns a `SeedFunc` handing out the same seed every time, so a test
// about determinism is not a test about `crypto/rand`.
func fixedSeeds(seed rules.Seed) plugin.SeedFunc {
	return func(context.Context, int64) (rules.Seed, error) { return seed, nil }
}

// aSeed returns a seed built from one byte, which is legal and distinct per value.
func aSeed(t *testing.T, value byte) rules.Seed {
	t.Helper()

	entropy := make([]byte, rules.SeedLen)
	entropy[0] = value

	seed, err := rules.NewSeed(entropy)
	if err != nil {
		t.Fatalf("building a seed: %v", err)
	}

	return seed
}

// gmFrame is a client intent frame, which is all a resolver is handed besides the
// three identity fields.
func gmFrame(seq realtime.ClientSeq, op realtime.Op) *realtime.ClientIntent {
	return &realtime.ClientIntent{
		Type: realtime.TypeIntent,
		Seq:  seq,
		Op:   op,
	}
}

// playerFrame is a client intent frame naming a placement, with no args.
func playerFrame(
	seq realtime.ClientSeq,
	op realtime.Op,
	target realtime.PlacementID,
) *realtime.ClientIntent {
	frame := gmFrame(seq, op)
	frame.Args = realtime.IntentArgs{Placement: target}

	return frame
}

// gm and player name the two identities the hub takes from a connection, for a
// table whose rows are about who is asking.
func gm(frame *realtime.ClientIntent) realtime.Intent     { return gmIntent(frame) }
func player(frame *realtime.ClientIntent) realtime.Intent { return playerIntent(frame) }

// gmIntent wraps a frame in the identity the hub took from the connection.
func gmIntent(frame *realtime.ClientIntent) realtime.Intent {
	return realtime.Intent{
		Campaign: testCampaign,
		Actor:    realtime.UserID(testActor),
		Role:     domain.RoleGM,
		Frame:    frame,
	}
}

// playerIntent is `gmIntent` with the lesser role, for the authorisation rows.
func playerIntent(frame *realtime.ClientIntent) realtime.Intent {
	intent := gmIntent(frame)
	intent.Role = domain.RolePlayer

	return intent
}

// moveTo builds a mutation carrying a placement's new position, encoded the way
// `PlacementCodec` decodes it.
func moveTo(t *testing.T, target rules.ObjectID, x, y int) rules.Mutation {
	t.Helper()

	return payload(t, target, realtime.Placement{X: x, Y: y, HP: 10, MaxHP: 10})
}

// payload builds a mutation whose args are one placement, JSON-encoded.
func payload(t *testing.T, target rules.ObjectID, placement realtime.Placement) rules.Mutation {
	t.Helper()

	encoded, err := encodePlacement(placement)
	if err != nil {
		t.Fatalf("encoding a payload: %v", err)
	}

	return rules.Mutation{Target: target, Op: "set_hp", Args: encoded}
}

// encodePlacement is `PlacementCodec`'s own encoding, written out here rather than
// reached for, so a test that builds a payload is building what the codec decodes
// rather than whatever the codec happens to do.
func encodePlacement(placement realtime.Placement) ([]byte, error) {
	return json.Marshal(placement)
}
