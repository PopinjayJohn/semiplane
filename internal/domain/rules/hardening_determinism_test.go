// P12 H6, the first of three claims: **the same (state, intent, seed) produces the
// same mutations, one hundred times out of a hundred.**
//
// S-14.6 is one sentence in the spec and two sentences in `rules.go`:
//
//	"identical (state, intent, seed) yields identical mutations"
//
// This file is the property rather than the example, and the difference is the whole
// of it. Three things already existed before it and each of them is weaker:
//
//   - `rules_test.go`'s `TestIdenticalStateIntentAndSeedYieldIdenticalMutationsAcrossOneHundredRuns`
//     resolves **one** scenario one hundred times, against a stub the test itself owns.
//     A stub cannot reach for `time.Now` by accident, because it has no `time` import;
//     it proves the *type* is deterministic and says nothing about the systems that
//     ship.
//   - `conformance`'s `AuditDeterminism` certifies **one** scenario per system — the
//     one the system author declared — which is the right shape for a published suite
//     and a weak shape for a property.
//   - `determinism.Audit` reads the *source*, so it refuses `range` over a map by
//     parsing. That is the strongest of the three and it is a lint rule: it catches the
//     construct, not the consequence. A resolver that gets its order from something
//     that is not a map — a `sync.Map`, a `map` reached through an interface whose
//     dynamic type the type checker cannot see, a `Plugin.Open` returning rows in
//     whatever order the driver gave them — passes it and is exactly the failure §14
//     asks about.
//
// So this file closes the gap from the other side: **every system this build ships,
// a corpus of scenarios per system, and every scenario resolved one hundred times and
// compared byte for byte.** The corpus is the quantifier. A resolver whose order comes
// from anywhere ambient disagrees with itself within a hundred tries on a tabletop
// with three conditions on it, and agrees with itself on a tabletop with none of them
// every time.
//
// # Why a hundred, when one comparison would do
//
// Go randomises map iteration, so a resolver that orders its answer by a map walk
// produces one of *k* orderings per run for a tabletop of *k* objects. Two runs agree
// with probability 1/*k*; a hundred runs see every ordering with probability
// 1 − *k*^(−99). `TestTheDetectorDetectsADivergentResolver` below does not assert that
// — it cannot, from outside — it **runs a resolver built to diverge and requires the
// comparison to notice**, which is the direction an audit nobody can fail fails in.
//
// # What is compared, and what is never printed
//
// A digest over each mutation's target, op, removal flag and payload bytes, plus the
// refusal text when a scenario is refused. The digest rather than a rendering because a
// payload is where a resolved roll lives and S-12.3 forbids one reaching a log; the
// digest is what lets a failure say "run 47 of 100 differs" without saying what was
// rolled. `TestTheDetectorComparesPayloadsRatherThanOpNames` is what holds the half of
// that a reader would otherwise have to take on trust: a resolver whose *dice* move
// every run while its op names do not is the failure this file exists for, and it is
// invisible to any comparison that looks at `Mutation.String`.
//
// # No store, no clock, no ambient source
//
// Everything here is a pure function of its inputs, which is what lets the corpus be
// generated rather than written out: the generator is `rand.New(rand.NewPCG(...))`,
// the one construction `.golangci.yml` deliberately leaves off the `forbidigo` list
// because `rules.Context.Rand` is built from it. A package-level `rand.IntN` in this
// file would be the very thing this repository forbids in rule code, and the linter
// would be right to refuse it.

package rules_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
)

// determinismRuns is S-14.6's hundred, and the argument for it is in the file header.
//
// **A named constant rather than a literal in two places**, because the two places are
// a detector and a canary: if the canary were allowed a different number than the
// detector, a reader would have to work out which of them the claim is about.
const determinismRuns = 100

// # The property

// TestEveryShippedSystemIsAPureFunctionOfStateIntentAndSeed is the claim.
//
// A table over every `rules.System` this build registers, a corpus of scenarios per
// system, and one hundred resolutions of each. Three failure modes it is built to
// catch, each of which a single-scenario test cannot:
//
//   - **Map iteration in the resolution.** A creature carrying two conditions is
//     written back in the pack's declaration order by `creature.pruneConditions`; a
//     resolver that rebuilt that list from a map would emit the same two slugs in a
//     different order on most runs, and the payload bytes are the difference.
//   - **An ambient source reached for by name.** A resolver calling `time.Now` or the
//     package-level `math/rand` compiles, works, and produces a different answer every
//     run. `determinism.Audit` refuses the call by parsing the source; this refuses the
//     *consequence*, for the paths the parser cannot see.
//   - **A draw whose label is not a function of the resolution.** `Context.Rand` is
//     pure in (seed, label), so two draws agree exactly when their labels agree. A
//     resolver labelling by a counter, by a wall clock, or by how many times it has
//     been called produces a different stream every run — reproducible on no machine,
//     including the one that produced it.
//
// **The corpus is generated rather than written out, and that is a decision with a
// cost worth naming.** A hand-written corpus is a list of shapes somebody thought of;
// a generated one covers the shapes nobody thought of, and it cannot be read at a
// glance. What makes it readable anyway is that the generator's knobs are three lines
// of state-building and every scenario is named after the shape it is — so a failure
// says "attack with three conditions on the defender" rather than "scenario 17".
func TestEveryShippedSystemIsAPureFunctionOfStateIntentAndSeed(t *testing.T) {
	t.Parallel()

	for _, fleet := range shippedFleets(t) {
		t.Run(fleet.name, func(t *testing.T) {
			t.Parallel()

			if len(fleet.scenarios) < minimumCorpus {
				t.Fatalf("the corpus holds %d scenarios, so a resolver that is deterministic "+
					"only on the shapes somebody thought of would pass: raise the corpus or "+
					"say why %d is enough", len(fleet.scenarios), minimumCorpus)
			}

			resolving, refusing := 0, 0

			// Per operation rather than per fleet, and that is the whole of the
			// anti-vacuity guard. "At least one scenario resolved something" is satisfied
			// by a corpus of six attacks and eighteen refusals, and the eighteen refusals
			// agreeing with each other is worth nothing: a refusal is reproducible whether
			// or not the resolver is deterministic, which is `conformance`'s determinism
			// audit's second tooth and the reason it exists at all. **An operation whose
			// every scenario is refused has not been shown to be deterministic; it has
			// been shown to be unreachable.**
			byOperation := make(map[rules.Op]int)

			for _, one := range fleet.scenarios {
				outcome := resolveHundredTimes(t, fleet.system, one)

				switch {
				case outcome.divergedAt >= 2:
					t.Fatalf(
						"%s: run %d of %d resolved differently from run 0 with the same state, "+
							"intent and seed; resolution is a function of those three and of "+
							"nothing else, so something outside them reached the answer\n"+
							" the difference: %s\n run 0 gave %s\n run %d gave %s",
						one.name, outcome.divergedAt, determinismRuns,
						outcome.where, outcome.first, outcome.divergedAt, outcome.divergent,
					)
				case outcome.mutated:
					resolving++
					byOperation[one.intent.Op]++
				default:
					refusing++
				}
			}

			for _, operation := range operationsIn(fleet.scenarios) {
				if byOperation[operation] == 0 {
					t.Errorf(
						"every one of this fleet's %q scenarios was refused (%d of them); the "+
							"hundred runs agreed about the refusal, which a deterministic and a "+
							"nondeterministic resolver would both do, so this operation is "+
							"unreachable rather than certified",
						operation, countFor(fleet.scenarios, operation),
					)
				}
			}

			// The second half of the guard: a fleet where *nothing* resolves has not been
			// tested at all, and saying so in one line is clearer than six of the above.
			if resolving == 0 {
				t.Fatalf("all %d scenarios were refused, and %d refusals agreeing with each "+
					"other is not determinism: this corpus certified nothing",
					resolving+refusing, refusing)
			}
		})
	}
}

// operationsIn returns every operation the corpus resolves, once each, in a fixed
// order.
//
// **Sorted and de-duplicated**, because the error above is printed per operation and a
// list in map order would print a different set of operations on every run — which is
// the reason `conformance.Audits` is an array and `realtime.componentOrder` is fixed.
//
// Indexed rather than ranged by value: a `scenario` carries a tabletop, so ranging one
// copies every object in it, and this walks the corpus to print an error about it.
func operationsIn(scenarios []scenario) []rules.Op {
	seen := make(map[rules.Op]struct{}, len(scenarios))

	unique := make([]rules.Op, 0, len(scenarios))

	for idx := range scenarios {
		operation := scenarios[idx].intent.Op

		if _, already := seen[operation]; already {
			continue
		}

		seen[operation] = struct{}{}

		unique = append(unique, operation)
	}

	slices.Sort(unique)

	return unique
}

// countFor reports how many scenarios resolve one operation, for the error message.
func countFor(scenarios []scenario, operation rules.Op) int {
	total := 0

	for idx := range scenarios {
		if scenarios[idx].intent.Op == operation {
			total++
		}
	}

	return total
}

// minimumCorpus is the floor below which "a corpus" stops being a quantifier.
//
// Twelve, and the arithmetic is the reason: the corpus varies one axis per scenario —
// the operation, the tabletop's width, the conditions in force, the seed — over six
// operations and two systems, so a corpus smaller than this is one where a single axis
// is never varied twice. A property test whose corpus cannot vary an axis is an
// example with extra steps, and the number is here so that shortening the corpus is an
// edit somebody makes on purpose.
const minimumCorpus = 12

// outcome is what one scenario's hundred resolutions agreed about.
//
// **Four fields rather than a pair of booleans**, because the failure a reader needs is
// the *run* that differed and the two shapes that differed, and a bool pair reports
// "they differed" and leaves the reproduction to whoever has to do it.
type outcome struct {
	// mutated is whether every run resolved to at least one mutation.
	mutated bool

	// divergedAt is the run that first disagreed with run 0, or zero.
	//
	// **One-based, and the first run cannot be it** — run 0 is the reference and there
	// is nothing to compare it against. `2` is therefore the earliest possible value
	// and a detector reporting `1` would be comparing a run with itself.
	divergedAt int

	// first and divergent are the two outcomes, rendered as op sequences.
	first     string
	divergent string

	// distinctAnswers is how many different answers the hundred runs gave between
	// them, and it is the field that says whether the hundred were needed.
	//
	// **A detector reports one difference; this reports how many there were**, and the
	// distinction is the argument for S-14.6's number. A resolver with six orderings
	// that is compared twice is compared once; compared a hundred times, every ordering
	// is seen. A value of 1 means the hundred runs proved nothing that two would not.
	distinctAnswers int

	// where says which mutation of the diverging run differs, without quoting either
	// run's payload.
	//
	// **This is the field that makes the failure actionable**, and it exists because of
	// what the first mutation caught: with a map-ordered `pruneConditions`, two runs
	// produced mutation lists whose op names and payload *lengths* were identical and
	// whose payload *bytes* were not. A failure message that printed only the rendering
	// would have shown the reader the same line twice.
	where string
}

// resolveHundredTimes resolves one scenario `determinismRuns` times and reports what
// the answers had in common.
//
// **A fresh `State` per run**, built from the scenario's objects rather than handed
// over once. A shared snapshot would let a resolver that scribbled on the copy it was
// given hide behind the previous run's — and `rules.State`'s accessors return copies,
// so the scribbling is invisible to the caller and would make this test certify a
// resolver that mutates what it was handed. `rules_test.go` states the same reason for
// its own loop.
func resolveHundredTimes(
	t *testing.T,
	system rules.System,
	resolution scenario,
) outcome {
	t.Helper()

	var result outcome

	reference := ""

	distinct := make(map[string]struct{}, determinismRuns)

	// Kept so `where` can localise a divergence without printing a payload.
	referenceMutations := []rules.Mutation(nil)

	for run := range determinismRuns {
		state, err := rules.NewState(1, resolution.objects)
		if err != nil {
			t.Fatalf("%s: run %d: building the tabletop: %v", resolution.name, run, err)
		}

		call, err := rules.NewContext(
			resolution.campaign, resolution.actor, resolution.role, resolution.seed,
		)
		if err != nil {
			t.Fatalf("%s: run %d: building the rule context: %v", resolution.name, run, err)
		}

		mutations, applyErr := system.Apply(t.Context(), call, state, resolution.intent)

		var got string

		switch {
		case applyErr != nil:
			// A refusal is part of the outcome, and it has to be compared like a
			// resolution: a resolver that alternates between refusing and resolving
			// depending on something ambient is exactly the failure this file is for, and
			// a comparison that looked only at successful runs would miss it entirely.
			got = "refused: " + applyErr.Error()
		default:
			result.mutated = true
			got = mutationDigest(mutations)
		}

		distinct[got] = struct{}{}

		if run == 0 {
			reference = got
			referenceMutations = mutations
			result.first = describe(mutations, applyErr)

			continue
		}

		if got == reference {
			continue
		}

		if result.divergedAt == 0 {
			result.divergedAt = run + 1
			result.divergent = describe(mutations, applyErr)
			result.where = localise(referenceMutations, mutations, applyErr)
		}
	}

	result.distinctAnswers = len(distinct)

	return result
}

// localise says which mutation of a diverging run differs, and in which field.
//
// **Over the fields and the payload's length, never its bytes**, which is the whole
// discipline S-12.3 asks for and the reason this function returns a sentence rather
// than the payload. It has three answers because three things can differ:
//
//   - the two lists are different lengths, which is the crudest divergence and the one
//     a resolver that sometimes emits an extra mutation produces;
//   - a mutation's address, op or removal flag differs;
//   - every field matches and the payload's *bytes* differ — which is what a resolver
//     whose dice moved produces, and the case a rendering cannot show because
//     `Mutation.String` prints no payload and the lengths agree.
func localise(first, second []rules.Mutation, refusal error) string {
	if refusal != nil {
		return "one run refused and the other did not"
	}

	if len(first) != len(second) {
		return fmt.Sprintf("run 0 resolved to %d mutations and this one to %d",
			len(first), len(second))
	}

	for idx := range first {
		switch {
		case first[idx].Target != second[idx].Target:
			return fmt.Sprintf("mutation %d of %d addresses %q where run 0 addressed %q",
				idx, len(first), second[idx].Target, first[idx].Target)
		case first[idx].Op != second[idx].Op:
			return fmt.Sprintf("mutation %d of %d is %q where run 0 said %q",
				idx, len(first), second[idx].Op, first[idx].Op)
		case first[idx].Remove != second[idx].Remove:
			return fmt.Sprintf("mutation %d of %d is %s where run 0 said %s",
				idx, len(first), removal(second[idx]), removal(first[idx]))
		case len(first[idx].Args) != len(second[idx].Args):
			return fmt.Sprintf("mutation %d of %d carries %d payload bytes where run 0 "+
				"carried %d", idx, len(first), len(second[idx].Args), len(first[idx].Args))
		}
	}

	return fmt.Sprintf(
		"all %d mutations address the same objects with the same ops and the same payload "+
			"lengths, so the payload bytes themselves differ — which is the failure a "+
			"resolver's drawn numbers produce", len(first))
}

func removal(mutation rules.Mutation) string {
	if mutation.Remove {
		return "a removal"
	}

	return "a change"
}

// mutationDigest is the comparison: one hash over every mutation's address, its op,
// its removal flag and its payload bytes, in order.
//
// **In order, and that is the load-bearing word.** Two runs that produce the same
// mutations in a different order have produced different answers — the hub applies them
// in the order returned, so a client's state depends on the order — and a comparison
// that sorted first would call them equal. Sorting is exactly what a resolver must do
// to be deterministic, so the comparison must not do it for it.
//
// The digest is over the payload and not over `Mutation.String`, which prints the op
// and the target and stops: a resolver whose dice moved every run would pass a
// comparison that only looked at the names. `TestTheDetectorComparesPayloadsRatherThanOpNames`
// is the test that holds it.
func mutationDigest(mutations []rules.Mutation) string {
	digest := sha256.New()

	for _, mutation := range mutations {
		// Length-prefixed, so no two mutations can be reordered into the same bytes by
		// a coincidence of field values: `attack p1` + `remove p2` and `attack p1p2` +
		// `remove` must not hash alike.
		for _, part := range [][]byte{
			[]byte(mutation.Target),
			[]byte(mutation.Op),
			[]byte(strconv.FormatBool(mutation.Remove)),
			[]byte(strconv.Itoa(len(mutation.Args))),
			mutation.Args,
		} {
			digest.Write(part)
			digest.Write([]byte{0})
		}
	}

	return hex.EncodeToString(digest.Sum(nil))
}

// describe renders one resolution for a failure message: the op sequence, and nothing
// from any payload.
//
// **No payload bytes, deliberately.** S-12.3 forbids a dice result reaching anything
// that could record it, and a test's `t.Fatalf` is the easiest place in this
// repository to break that rule by accident — `rules_test.go` prints `%+v` of a
// mutation list in one place, and the argument there (a test is not a log) is
// defensible but is not a rule worth leaning on twice. A digest and a length are
// enough to localise the divergence.
func describe(mutations []rules.Mutation, refusal error) string {
	if refusal != nil {
		return "refused: " + refusal.Error()
	}

	parts := make([]string, 0, len(mutations))

	for _, mutation := range mutations {
		parts = append(parts, mutation.String()+" args="+strconv.Itoa(len(mutation.Args))+"b")
	}

	return "[" + strings.Join(parts, ", ") + "]"
}

// # The detector, and the canary that proves it can fail

// TestTheDetectorDetectsADivergentResolver is the negative control, and it is the test
// that makes every other assertion in this file mean something.
//
// `orderByMapResolver` resolves one intent into one mutation per object, **in map
// iteration order**, which is the exact construct `determinism.ForbiddenRules` refuses
// under `range/map` and the exact failure S-10.4 names: "a resolution that ranges over
// a map resolves the same intent differently on a second run". It is built here rather
// than in the dnd5e engine because a resolver that is broken on purpose has to be
// broken *permanently* — a mutation in the product is a thing this file cannot carry,
// and a gate wired to nothing is the failure this repository has paid for three times.
//
// **Three assertions, and each is a different claim:**
//
//  1. The detector reports a divergence, and reports it at run 2 or later — a detector
//     that fired at run 1 would be comparing a run with itself.
//  2. Fewer than a hundred runs would not have been enough *in general*, and the honest
//     way to say that is what is asserted here: **the hundred runs saw more than one
//     distinct answer.** A corpus compared twice would have compared one ordering of
//     several, which is the whole argument for S-14.6's number.
//  3. Every run still produced mutations, so the divergence is about their order and
//     not about a run that resolved nothing.
func TestTheDetectorDetectsADivergentResolver(t *testing.T) {
	t.Parallel()

	resolver := orderByMapResolver{width: 6}
	tabletop := resolver.tabletop()

	result := resolveHundredTimes(t, resolver, tabletop)

	if !result.mutated {
		t.Fatal("the divergent resolver resolved nothing, so it cannot show the detector " +
			"catching anything")
	}

	if result.divergedAt == 0 {
		t.Fatalf("a resolver ordering its mutations by map iteration agreed with itself "+
			"across %d runs; either Go stopped randomising iteration or the comparison "+
			"cannot see a difference, and both mean this file certifies nothing",
			determinismRuns)
	}

	if result.divergedAt < 2 {
		t.Fatalf("the detector reported the divergence at run %d, which is the reference "+
			"run itself: it is comparing a run with itself", result.divergedAt)
	}

	if distinct := result.distinctAnswers; distinct < 2 {
		t.Errorf("the hundred runs produced %d distinct answers, so nothing about this "+
			"resolver varies and there was nothing to detect", distinct)
	}
}

// TestTheDetectorComparesPayloadsRatherThanOpNames is the second half of the same
// control, and it is a *different* failure: a resolver whose op names are constant and
// whose dice are not.
//
// This is the failure `rules.Mutation.String` cannot see, and it is worth being precise
// about why it is the interesting one. A resolver that reached for `time.Now` shows up
// in the digest, yes — but the *invisible* version is a resolver that draws from a
// source the seed does not derive and happens to draw the same number twice, or one
// whose payload embeds a timestamp the op name does not mention. Comparing op names
// alone would certify both.
func TestTheDetectorComparesPayloadsRatherThanOpNames(t *testing.T) {
	t.Parallel()

	first := []rules.Mutation{statedMutation(t, "p1", "set_hit_points", `{"hp":10}`)}
	second := []rules.Mutation{statedMutation(t, "p1", "set_hit_points", `{"hp":11}`)}

	// The premise, asserted rather than assumed: the two render identically. Without
	// this line the assertion below would be satisfied by a comparison that never
	// looked at payloads in the first place.
	if first[0].String() != second[0].String() {
		t.Fatalf("two mutations differing only in their payload rendered as %q and %q, so "+
			"the fixture cannot show what the digest sees that the rendering does not",
			first[0], second[0])
	}

	if mutationDigest(first) == mutationDigest(second) {
		t.Error("two mutations differing only in their payload have the same digest, so a " +
			"resolver whose dice moved every run would pass this file")
	}

	// And the order is load-bearing: the same two mutations the other way round are a
	// different answer, because the hub applies them in the order returned and a client
	// reconciling them applies them in the order it was sent them.
	//
	// **Two distinct mutations rather than the payload pair above**, because comparing a
	// one-element list against a two-element one would differ for the length alone and
	// would pass whatever the digest does with order.
	alpha := statedMutation(t, "p1", "set_hit_points", `{"hp":10}`)
	beta := statedMutation(t, "p2", "apply_condition", `{"condition":"prone"}`)

	thisWay := mutationDigest([]rules.Mutation{alpha, beta})
	thatWay := mutationDigest([]rules.Mutation{beta, alpha})

	if thisWay == thatWay {
		t.Error("two mutations in opposite orders have the same digest, so a resolver whose " +
			"mutations came back in map order would pass this file")
	}
}

func statedMutation(
	t *testing.T,
	target rules.ObjectID,
	operation rules.Op,
	args string,
) rules.Mutation {
	t.Helper()

	mutation, err := rules.NewMutation(target, operation, []byte(args))
	if err != nil {
		t.Fatalf("building a fixture mutation: %v", err)
	}

	return mutation
}

// orderByMapResolver is a `rules.System` that is deterministic in every way a
// well-behaved one is, and orders its answer by a map walk.
//
// **Built to be caught.** It resolves against the state it is handed, draws nothing,
// reads no clock and imports no forbidden package — the only thing wrong with it is
// `for name := range byName`, and that one line is the whole of S-10.4's `range/map`
// rule as a runtime failure. Its value here is that it is a resolver which *compiles*,
// passes `rules.Validate`, and produces byte-identical answers on any single run it is
// compared on.
//
// The remaining methods are the minimum a `rules.System` needs, and they are dull on
// purpose: a canary that had interesting behaviour would make a failure ambiguous
// between "the detector works" and "the canary did something else".
type orderByMapResolver struct {
	// width is how many game objects one resolution answers for.
	width int
}

var _ rules.System = orderByMapResolver{}

func (orderByMapResolver) ID() rules.ID { return "divergent" }

func (orderByMapResolver) Title() string { return "A resolver built to diverge" }

func (orderByMapResolver) RulesetVersion() string { return "divergent@1" }

func (orderByMapResolver) Grammar() rules.Grammar {
	return rules.Grammar{
		Notation: "none",
		Summary:  "no notation; this system has no rolls to describe",
		Example:  "none",
		Terms:    []rules.Term{{Name: "none", Summary: "nothing", Pattern: `^none$`}},
	}
}

func (orderByMapResolver) Parse(string) (rules.Expr, error) {
	return rules.Expr{}, fmt.Errorf("%w: this system has no notation", rules.ErrInvalidOp)
}

func (orderByMapResolver) ContentKinds() []rules.Kind { return nil }

func (orderByMapResolver) Views() []rules.View {
	return []rules.View{{
		Name:     "say",
		Title:    "What it said",
		Renderer: rules.RendererGeneric,
		Shape:    rules.ShapeList,
	}}
}

func (orderByMapResolver) Derive(rules.State, rules.Query) (rules.Payload, error) {
	return rules.Payload{}, fmt.Errorf(
		"%w: this system is certified for resolution, not for rendering", rules.ErrInvalidView,
	)
}

// Apply is the defect, on its own, in as few lines as the defect can be written in.
func (r orderByMapResolver) Apply(
	_ context.Context,
	call rules.Context,
	state rules.State,
	in rules.Intent,
) ([]rules.Mutation, error) {
	if call.Role != domain.RoleGM {
		return nil, fmt.Errorf("%w: a player's to make", rules.ErrInvalidRole)
	}

	byName := make(map[string]string, state.Len())

	for _, object := range state.Objects() {
		byName[object.ID.String()] = object.Kind.String()
	}

	var mutations []rules.Mutation

	// **This range.** Everything else in this type is careful.
	for name, kind := range byName {
		mutation, err := rules.NewMutation(rules.ObjectID(name), in.Op, []byte(kind))
		if err != nil {
			return nil, fmt.Errorf("%w: stating what %q said: %w", rules.ErrInvalidOp, name, err)
		}

		mutations = append(mutations, mutation)
	}

	return mutations, nil
}

// tabletop is the shape one call of the canary is certified against.
func (r orderByMapResolver) tabletop() scenario {
	objects := make([]rules.Object, 0, r.width)

	for idx := range r.width {
		objects = append(objects, rules.Object{
			ID:   rules.ObjectID("p" + strconv.Itoa(idx)),
			Kind: rules.Kind("kind_" + strconv.Itoa(idx)),
			Data: []byte(`{"n":` + strconv.Itoa(idx) + `}`),
		})
	}

	intent, err := rules.NewIntent("say", "p0", nil)
	if err != nil {
		panic("the canary's own fixture is not a valid intent: " + err.Error())
	}

	return scenario{
		name:     "the canary's tabletop",
		objects:  objects,
		intent:   intent,
		seed:     rules.Seed{1, 2, 3},
		campaign: 1,
		actor:    1,
		role:     domain.RoleGM,
	}
}
