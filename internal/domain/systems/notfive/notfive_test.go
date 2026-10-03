package notfive_test

import (
	"bytes"
	"errors"
	"strconv"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/conformance"
	"github.com/semiplane/semiplane/internal/domain/systems/notfive"
)

// # The seams
//
// These two functions are the whole of what this package has to hand the conformance
// suite beyond the `rules.System` it exports, and they are the shape an author outside
// this repository copies. Neither reaches into anything semiplane owns except the
// contract and the role vocabulary.

// classify is the composition root's error-to-wire-word map.
//
// It is a **switch on this system's own sentinels** and nothing else, which is what
// makes a wire word possible at all: the composition root is the only place holding
// both a `rules.System` and a `realtime.RejectReason`, and the alternative — string
// matching, or letting the system's own sentence through — is how whatever a plugin
// choked on reaches a browser.
//
// `ErrContained` maps to `server_error`, and that is not an accident of placement: it is
// the word §10.7 means, "the one reason that says nothing about what failed", and a
// contained panic is exactly that.
func classify(refusal error) string {
	switch {
	case errors.Is(refusal, notfive.ErrGMOnly):
		return conformance.ReasonNotPermitted
	case errors.Is(refusal, notfive.ErrUnknownOp):
		return conformance.ReasonUnknownOp
	case errors.Is(refusal, notfive.ErrNoSuchSpool):
		return conformance.ReasonNoSuchPlacement
	case errors.Is(refusal, notfive.ErrNotation):
		return conformance.ReasonInvalidArgs
	case errors.Is(refusal, conformance.ErrContained):
		return conformance.ReasonServerError
	default:
		// The safe default, and the reason the switch is exhaustive over *this package's*
		// sentinels rather than over an enumeration: a refusal nobody recognises is
		// `server_error` rather than a word chosen for it.
		return conformance.ReasonServerError
	}
}

// ruleset is the deployment's ruleset decision for the loom.
//
// **Deliberately tiny**, because the seam is a *shape* and this file's job is to show
// the shape. A real deployment's answer is `realtime.FingerprintOf` over a
// `realtime.Descriptor` plus `realtime.Gate.Check`, and it is named
// `Fixture`-prefixed here so nothing mistakes this for the product's ruleset logic.
type ruleset struct{}

var _ conformance.Resume = ruleset{}

// Fingerprint is what a campaign opened now would be written under.
//
// The loom's own version and nothing else, and `house` is read for nothing — which is
// ADR 0018 as an assertion rather than as a comment. The suite toggles a house rule
// and requires the two fingerprints to be byte-identical, so a future edit that starts
// reading `house` fails a test rather than stranding a GM's campaign.
func (ruleset) Fingerprint(system rules.System, _ []conformance.HouseRule) string {
	return "fixture:system=" + system.ID().String() + ";ruleset=" + system.RulesetVersion()
}

// Check answers whether a campaign written under persisted may resume.
//
// Two refusals, kept distinguishable for the reason the suite's version audit insists
// on: a column this build cannot parse is a migration problem, and a genuine mismatch is
// a GM's decision, and one sentinel for both would send the GM to discard a game over a
// string.
func (ruleset) Check(
	system rules.System,
	house []conformance.HouseRule,
	persisted string,
) conformance.Verdict {
	expected := ruleset{}.Fingerprint(system, house)

	verdict := conformance.Verdict{Persisted: persisted, Expected: expected}

	if persisted == expected {
		return verdict
	}

	if persisted == "" {
		verdict.Err = errors.New("fixture: the persisted ruleset_version names no ruleset")

		return verdict
	}

	if persisted[:min(len(persisted), len("fixture:"))] != "fixture:" {
		verdict.Err = errors.New("fixture: that ruleset_version is not comparable")

		return verdict
	}

	verdict.Err = errors.New("fixture: the state was written under " + strconv.Quote(persisted) +
		" and this build resolves " + strconv.Quote(expected))

	return verdict
}

// suiteFor builds a complete configuration for the loom: one scenario, the two
// GM-only operations, this package's refusals, and the ruleset seam above.
//
// The scenario is a `spin` on a spool with thread already on it, because the
// determinism audit needs a resolution that **changes something** — an intent that
// resolves to nothing is reproducible whether or not the resolver is deterministic, and
// the suite refuses to certify one.
func suiteFor(system rules.System) conformance.Config {
	return conformance.Config{
		System: system,
		Scenario: conformance.Scenario{
			Objects: []rules.Object{
				{ID: "spool_1", Kind: notfive.KindSpool, Data: []byte(`{"length":12,"colour":2}`)},
				{ID: "spool_2", Kind: notfive.KindSpool, Data: []byte(`{"length":3,"colour":0}`)},
			},
			Intent: rules.Intent{Op: notfive.OpSpin, Target: "spool_1", Args: nil},
			Call: rules.Context{
				Campaign: 42,
				Actor:    7,
				Role:     domain.RoleGM,
				Seed:     rules.Seed{4, 5, 6},
			},
		},
		GMOnly:   notfive.LoomGMOnlyOps(),
		Classify: classify,
		Resume:   ruleset{},
	}
}

// # The system's own behaviour
//
// The conformance suite proves the loom obeys the contract. These prove it is a *loom*,
// which is what makes the counterexample worth anything: a system that passed the
// suite by refusing everything would satisfy every audit and demonstrate nothing.

func TestTheLoomIsAWellFormedExpressionGrammar(t *testing.T) {
	t.Parallel()

	grammar := notfive.LoomGrammar()

	for _, term := range grammar.Terms {
		if term.Pattern == "" {
			t.Errorf(
				"term %q has no pattern, so a client has nothing to validate against",
				term.Name,
			)
		}
	}

	// The example has to parse. `rules.Grammar.Example` is documentation, and a system
	// shipping an example its own `Parse` refuses has shipped a broken tooltip — which
	// nothing in `rules` can check, because `rules` does not call into the system.
	if _, err := notfive.New().Parse(grammar.Example); err != nil {
		t.Errorf("the grammar's own example %q does not parse: %v", grammar.Example, err)
	}
}

func TestTheLoomRefusesNotationItDoesNotAccept(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"", "0x12", "12x", "x12", "1x1x1", "007", "-3", "12 x 12x", "1x12x"} {
		t.Run(strconv.Quote(text), func(t *testing.T) {
			t.Parallel()

			if _, err := notfive.New().Parse(text); !errors.Is(err, notfive.ErrNotation) {
				t.Errorf("want %v, got %v", notfive.ErrNotation, err)
			}
		})
	}
}

func TestTheLoomDrawsItsNumbersFromTheSeedAndNotFromAnythingElse(t *testing.T) {
	t.Parallel()

	system := notfive.New()
	state := aTable(t)

	first, err := system.Apply(
		t.Context(),
		aCall(t, domain.RolePlayer),
		state,
		anIntent(t, notfive.OpSpin),
	)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	second, err := system.Apply(
		t.Context(),
		aCall(t, domain.RolePlayer),
		state,
		anIntent(t, notfive.OpSpin),
	)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !bytes.Equal(first[0].Args, second[0].Args) {
		t.Errorf("the same seed gave %s and then %s", first[0].Args, second[0].Args)
	}

	// A different seed has to give a different answer eventually, or the draw is not
	// reaching the result and the determinism audit above it is certifying an answer that
	// cannot vary.
	//
	// **Several seeds rather than one.** `spin` adds between one and eight picks, so any
	// two seeds agree about one time in nine, and a test asserting on a single pair would
	// fail on about one run in nine — which is worse than no test, because it fails in the
	// direction that reads as a real regression. Nine seeds make a coincidence (9^-8)
	// something a reader does not have to reason about.
	diverged := false

	for seed := byte(2); seed < 11; seed++ {
		elsewhere, err := system.Apply(
			t.Context(), aCallWith(t, domain.RolePlayer, seed), state, anIntent(t, notfive.OpSpin),
		)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}

		if !bytes.Equal(elsewhere[0].Args, first[0].Args) {
			diverged = true

			break
		}
	}

	if !diverged {
		t.Error("nine different seeds produced one answer, so the draw is not reaching the result")
	}
}

func TestTheLoomRefusesItsGmOnlyOperationsToAPlayerBeforeItReadsTheTable(t *testing.T) {
	t.Parallel()

	system := notfive.New()

	// An empty state: the refusal has to come from the role, not from the absence of the
	// spool, or it would be reporting `no_such_placement` and the wire would tell the
	// campaign's table something about its own state.
	empty, err := rules.NewState(0, nil)
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	for _, op := range notfive.LoomGMOnlyOps() {
		t.Run(op.String(), func(t *testing.T) {
			t.Parallel()

			mutations, err := system.Apply(
				t.Context(), aCall(t, domain.RolePlayer), empty, anIntent(t, op),
			)
			if !errors.Is(err, notfive.ErrGMOnly) {
				t.Fatalf("want %v, got %v", notfive.ErrGMOnly, err)
			}

			if len(mutations) > 0 {
				t.Errorf("a refused operation returned %d mutation(s)", len(mutations))
			}
		})
	}
}

func TestTheLoomLetsTheGmAdjudicate(t *testing.T) {
	t.Parallel()

	system := notfive.New()

	for _, op := range notfive.LoomGMOnlyOps() {
		t.Run(op.String(), func(t *testing.T) {
			t.Parallel()

			mutations, err := system.Apply(
				t.Context(), aCall(t, domain.RoleGM), aTable(t), anIntent(t, op),
			)
			if err != nil {
				t.Fatalf("the GM was refused %q: %v", op, err)
			}

			if len(mutations) != 1 {
				t.Errorf("%q resolved to %d mutations, want 1", op, len(mutations))
			}
		})
	}
}

// TestTheLoomIgnoresAPlacementWhoseKindItDoesNotDeclare is S-14.7's half a system can
// get wrong, asserted on the system rather than through the audit.
//
// An **empty** state and a resolution that succeeds is the strongest form: a resolver
// that walked its state to check for anything would find nothing here either, so the
// fixture would pass for a resolver that validates kinds rather than for one that
// ignores them. What the audit adds is the *other* direction — a tabletop that does
// carry a foreign object — and this test is what says the loom is inert by construction
// rather than by luck of the fixture.
func TestTheLoomIgnoresAPlacementWhoseKindItDoesNotDeclare(t *testing.T) {
	t.Parallel()

	state, err := rules.NewState(3, []rules.Object{
		{ID: "spool_1", Kind: notfive.KindSpool, Data: []byte(`{"length":5,"colour":1}`)},
		{ID: "stranger", Kind: "conformance_surplus", Data: []byte(`{"length":99,"colour":5}`)},
	})
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	mutations, err := notfive.New().Apply(
		t.Context(), aCall(t, domain.RolePlayer), state, anIntent(t, notfive.OpSpin),
	)
	if err != nil {
		t.Fatalf("a tabletop carrying a foreign kind was refused: %v", err)
	}

	for _, mutation := range mutations {
		if mutation.Target == "stranger" {
			t.Errorf("the resolution addressed the foreign object: %s", mutation)
		}
	}
}

func TestTheLoomAnswersItsDeclaredViewsAndRefusesTheRest(t *testing.T) {
	t.Parallel()

	system := notfive.New()
	state := aTable(t)

	for _, view := range notfive.LoomViews() {
		t.Run(view.Name, func(t *testing.T) {
			t.Parallel()

			query := rules.Query{View: view.Name, Object: "spool_1"}

			payload, err := system.Derive(state, query)
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}

			if payload.View() != view.Name {
				t.Errorf("the payload answers %q, not %q; the renderer is chosen by the view",
					payload.View(), view.Name)
			}
		})
	}

	if _, err := system.Derive(state, rules.Query{View: "character-sheet"}); err == nil {
		t.Error(
			"a view this system does not declare was answered rather than refused; a card rendered " +
				"from nothing is a blank document that says nothing went wrong",
		)
	}
}

func TestTheLoomSkipsAnUnreadableRowRatherThanRefusingTheWholeList(t *testing.T) {
	t.Parallel()

	// A spool whose bytes are not this system's encoding, alongside one that is: the list
	// view must still describe the readable one, because a stale row is not a reason to
	// refuse to read a whole table.
	state, err := rules.NewState(3, []rules.Object{
		{ID: "spool_1", Kind: notfive.KindSpool, Data: []byte(`{"length":5,"colour":1}`)},
		{ID: "spool_9", Kind: notfive.KindSpool, Data: []byte(`not json`)},
	})
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	payload, err := notfive.New().Derive(state, rules.Query{View: notfive.ViewStockList})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	rows, ok := payload.Value().([]struct {
		Spool  string `json:"spool"`
		Length int    `json:"length"`
		Colour string `json:"colour"`
	})
	if ok {
		t.Fatalf(
			"the list came back as %T, which is a test's problem rather than the system's",
			rows,
		)
	}
}

// # Fixtures

func aTable(t *testing.T) rules.State {
	t.Helper()

	state, err := rules.NewState(9, []rules.Object{
		{ID: "spool_1", Kind: notfive.KindSpool, Data: []byte(`{"length":12,"colour":2}`)},
		{ID: "spool_2", Kind: notfive.KindSpool, Data: []byte(`{"length":3,"colour":0}`)},
	})
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	return state
}

func aCall(t *testing.T, role domain.Role) rules.Context {
	t.Helper()

	return aCallWith(t, role, 3)
}

func aCallWith(t *testing.T, role domain.Role, seedByte byte) rules.Context {
	t.Helper()

	return rules.Context{
		Campaign: 42,
		Actor:    7,
		Role:     role,
		Seed:     rules.Seed{seedByte, seedByte, seedByte},
	}
}

func anIntent(t *testing.T, op rules.Op) rules.Intent {
	t.Helper()

	intent, err := rules.NewIntent(op, "spool_1", nil)
	if err != nil {
		t.Fatalf("building the intent %q: %v", op, err)
	}

	return intent
}
