package conformance_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/conformance"
)

// stub is a gameplay system whose every answer is a field, so a test can break one
// thing at a time and attribute a finding to it.
//
// **Its vocabulary is 5e's on purpose.** The suite must work on the system everybody
// will actually write, and the counterexample to "it only works for 5e" is
// `internal/domain/systems/notfive`, certified in its own package. If this stub were
// neutral, a suite that had accidentally grown 5e assumptions would still pass.
//
// It draws from `call.Rand` and reads from `state`, because an audit run against a
// resolver that returns a constant is an audit about nothing: reproducibility over an
// answer that cannot vary is unfalsifiable, which is the same reason
// `AuditDeterminism` requires the scenario to produce a mutation.
type stub struct {
	id      rules.ID
	title   string
	ruleset string

	grammar rules.Grammar
	kinds   []rules.Kind
	views   []rules.View

	// shared is a random source carried on the system rather than on the resolution —
	// the shape §10.3's seeded `Context` exists to make unrepresentable, reproduced
	// deliberately so that the determinism audit is known to catch something.
	shared *rand.Rand

	// fatal makes `Apply` refuse a tabletop carrying a kind it does not declare,
	// which is §10.8's "fatal" reading of a stale kind.
	fatal bool

	// touchSurplus makes `Apply` address the unknown-kind object as well as the
	// intended one, which is "not inert".
	touchSurplus bool

	// ignoreRole makes `Apply` skip the GM-only check, which is "no role at all".
	ignoreRole bool

	// denyGM makes `Apply` refuse the GM-only operation to the GM as well, which is a
	// system nobody at the table can use.
	denyGM bool

	// refuseEverything makes `Apply` refuse every operation as the GM-only refusal,
	// which enforces no role at all because it refuses play.
	refuseEverything bool

	// failWithMutation makes `Apply` return a mutation *and* an error, which
	// `rules.go` forbids and the containment audit asserts.
	failWithMutation bool

	// agreeFor makes `Apply` return the same answer for its first `agreeFor` calls and
	// a different one after, which is the shape of the failure the *number* S-14.6 names
	// exists to catch: a resolver that is reproducible for two calls and wrong for every
	// call after. A fixture that diverged on the second call would be caught by an audit
	// comparing two runs, which is why the number would then be untested.
	agreeFor int

	// agreed is the answer the agreeing calls returned, kept so the divergence is real
	// rather than a second draw from the same label.
	agreed []rules.Mutation

	// calls counts every `Apply`, which is what `agreeFor` counts against.
	calls int
}

// wellFormed is the system every table starts from and then breaks in one place.
//
// A function rather than a package-level value: a shared mutable stub is how one
// test's kind list becomes another test's failure.
func wellFormed() *stub {
	return &stub{
		id:      "5e-2024",
		title:   "D&D 5e (2024)",
		ruleset: "5e-2024@1",
		grammar: rules.Grammar{
			Notation: "d20",
			Summary:  "NdM+K",
			Example:  "2d20+3",
			Terms: []rules.Term{{
				Name:    "roll",
				Summary: "a number of twenty-sided dice and a flat bonus",
				Pattern: `^(\d*)d(\d+)([+-]\d+)?$`,
			}},
		},
		kinds: []rules.Kind{"spell", "class", "creature"},
		views: []rules.View{{
			Name:     "character-sheet",
			Title:    "Character sheet",
			Renderer: rules.RendererGeneric,
			Shape:    rules.ShapeStatBlock,
		}},
	}
}

var _ rules.System = (*stub)(nil)

// The refusals, one per failure this stub can produce, each named so the adapter
// fixture below can map it and the suite can ask which one a finding is about.
var (
	errStubGMOnly     = errors.New("stub: that operation is the GM's")
	errStubNoSuch     = errors.New("stub: no placement by that name")
	errStubUnknownOp  = errors.New("stub: this system does not resolve that op")
	errStubBadPayload = errors.New("stub: the pack entry did not parse")
)

func (s *stub) ID() rules.ID { return s.id }

func (s *stub) Title() string { return s.title }

func (s *stub) RulesetVersion() string { return s.ruleset }

func (s *stub) Grammar() rules.Grammar { return s.grammar }

func (s *stub) Parse(expr string) (rules.Expr, error) {
	if !strings.Contains(expr, "d") {
		return rules.Expr{}, fmt.Errorf("stub: %q is not a dice expression", expr)
	}

	return rules.NewExpr(s.id, s.grammar.Notation, expr, map[string]string{"source": expr}), nil
}

func (s *stub) Apply(
	_ context.Context,
	call rules.Context,
	state rules.State,
	in rules.Intent,
) ([]rules.Mutation, error) {
	switch in.Op {
	case "roll", "move_token", "set_hp", "pause":
	default:
		return nil, fmt.Errorf("%w: %q", errStubUnknownOp, in.Op)
	}

	if s.refuseEverything {
		return nil, fmt.Errorf("%w: %q", errStubGMOnly, in.Op)
	}

	if s.gmOnly(in.Op) {
		if s.denyGM || (call.Role != domain.RoleGM && !s.ignoreRole) {
			return nil, fmt.Errorf("%w: %q", errStubGMOnly, in.Op)
		}
	}

	if s.fatal && len(state.OfKind(conformance.SurplusKind)) > 0 {
		// The violation: an intent unrelated to the stale kind fails because of it.
		return nil, fmt.Errorf("stub: cannot resolve with %q on the table", conformance.SurplusKind)
	}

	object, found := state.Lookup(in.Target)
	if !found {
		return nil, fmt.Errorf("%w: %q", errStubNoSuch, in.Target)
	}

	var total int

	if s.shared != nil {
		total = s.shared.IntN(20) + 1
	} else {
		dice := call.Rand(in.Op.String() + "/" + in.Target.String())
		total = 1 + dice.IntN(20) + len(object.Data)
	}

	payload := []byte(strconv.Itoa(total))

	mutation, err := rules.NewMutation(in.Target, in.Op, payload)
	if err != nil {
		return nil, fmt.Errorf("stub: building a mutation: %w", err)
	}

	changes := []rules.Mutation{mutation}

	if s.agreeFor > 0 {
		s.calls++

		if s.calls <= s.agreeFor {
			if s.agreed == nil {
				s.agreed = changes
			}

			return s.agreed, nil
		}

		// Past the agreeing window the answer has to *differ*, and it cannot differ by
		// drawing again: the stub's draw is a pure function of (seed, label), so a second
		// draw from the same label returns the same number — which is the whole of
		// S-14.6's property working. What diverges here is a call counter, which is
		// exactly the ambient state the rule forbids and the audit has to notice.
		diverged, buildErr := rules.NewMutation(
			in.Target, in.Op, []byte(strconv.Itoa(total+s.calls)),
		)
		if buildErr != nil {
			return nil, fmt.Errorf("stub: building the diverging mutation: %w", buildErr)
		}

		return []rules.Mutation{diverged}, nil
	}

	if s.touchSurplus {
		for _, surplus := range state.OfKind(conformance.SurplusKind) {
			spotted, buildErr := rules.NewMutation(surplus.ID, "mark", payload)
			if buildErr != nil {
				return nil, fmt.Errorf("stub: addressing the surplus object: %w", buildErr)
			}

			changes = append(changes, spotted)
		}
	}

	if s.failWithMutation {
		// The violation: a refused resolution that still says what changed.
		return changes, fmt.Errorf("%w: giving up half way through", errStubBadPayload)
	}

	return changes, nil
}

func (s *stub) Derive(state rules.State, query rules.Query) (rules.Payload, error) {
	payload, err := rules.NewPayload(query.View, map[string]any{
		"objects": state.Len(),
		"target":  query.Object.String(),
	})
	if err != nil {
		return rules.Payload{}, fmt.Errorf("stub: deriving %q: %w", query.View, err)
	}

	return payload, nil
}

func (s *stub) Views() []rules.View { return s.views }

func (s *stub) ContentKinds() []rules.Kind { return s.kinds }

// gmOnly reports whether op is one this system reserves to the GM.
//
// The same set `Config.GMOnly` declares, hard-typed rather than derived from the
// config, because a stub that read its own role policy from the thing testing it could
// not be wrong about it.
func (s *stub) gmOnly(op rules.Op) bool {
	return op == "set_hp" || op == "pause"
}

// classify is the composition root's error-to-wire-word map, which is the fourth seam
// and the one that keeps a refusal from becoming its own sentence.
//
// Declared here rather than inside the audit because it is the adapter's job: the
// composition root is the only place holding both a `rules.System` and a
// `realtime.RejectReason`.
func classify(refusal error) string {
	switch {
	case errors.Is(refusal, errStubGMOnly):
		return conformance.ReasonNotPermitted
	case errors.Is(refusal, errStubNoSuch):
		return conformance.ReasonNoSuchPlacement
	case errors.Is(refusal, errStubUnknownOp):
		return conformance.ReasonUnknownOp
	default:
		return conformance.ReasonServerError
	}
}

// config is a complete suite configuration over a system, and the starting point for
// every table below.
func config(system rules.System) conformance.Config {
	return conformance.Config{
		System: system,
		Scenario: conformance.Scenario{
			Objects: []rules.Object{
				{ID: "p1", Kind: rules.KindToken, Data: []byte(`{"hp":10}`)},
				{ID: "s1", Kind: rules.KindScene, Data: []byte(`{"fog":"dark"}`)},
			},
			// A struct literal rather than `rules.NewIntent`: the three arguments are
			// constants, and `conformance.New` checks the result with `Intent.Valid()`
			// — which is the check that matters, and is the reason the audit refuses a
			// scenario the wire would never have produced.
			Intent: rules.Intent{Op: "roll", Target: "p1", Args: []byte(`{"expr":"2d20+3"}`)},
			Call: rules.Context{
				Campaign: 42,
				Actor:    7,
				Role:     domain.RoleGM,
				Seed:     rules.Seed{1, 2, 3},
			},
		},
		GMOnly:   []rules.Op{"set_hp", "pause"},
		Classify: classify,
		Resume:   deployment{},
	}
}

// mustSuite configures a suite or fails the test that asked for one.
func mustSuite(t *testing.T, cfg conformance.Config) *conformance.Suite {
	t.Helper()

	suite, err := conformance.New(cfg)
	if err != nil {
		t.Fatalf("the suite refused a complete configuration: %v", err)
	}

	return suite
}

// scribbler is a resolver that writes to every object it reads and then answers
// normally.
//
// **The fixture the containment audit's first tooth exists for**, and the reason it
// lives in the test package rather than in a violations row: with `rules.State` handing
// out copies, a scribbling resolver's writes are invisible, so an audit cannot *fail*
// because of it — there is nothing to fail. Which means a violations row for it would be
// a row whose fixture could never trip, and this repository's rule about unreachable
// fixtures applies to a fixture just as much as to an audit.
//
// So the claim is asserted directly instead:
// `TestAScribblingResolverChangesNothingItWasHanded` builds the same state, resolves with
// this, and compares the fingerprint. It passes today because `rules.State`'s accessors
// clone, and it is verified by mutation: breaking `cloneObject` turns it red, which is
// the only way an assertion about a property of another package can be shown to have
// teeth. `rules.State` is not this work item's to edit, so the mutation is applied,
// run and reverted.
type scribbler struct {
	rules.System
}

func (scribbler) Apply(
	_ context.Context,
	call rules.Context,
	state rules.State,
	in rules.Intent,
) ([]rules.Mutation, error) {
	for _, object := range state.Objects() {
		for idx := range object.Data {
			object.Data[idx] = 'X'
		}
	}

	mutation, err := rules.NewMutation(in.Target, in.Op, []byte("1"))
	if err != nil {
		return nil, fmt.Errorf("scribbler: building a mutation: %w", err)
	}

	return []rules.Mutation{mutation}, nil
}
