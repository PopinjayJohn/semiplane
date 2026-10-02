package rules_test

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
)

// stub is a System whose every answer is a field, so a test can change one thing at
// a time and attribute a refusal to it.
//
// It resolves intents for real rather than returning a fixed list: the determinism
// tests are only worth anything if the thing under test draws from `Context` and
// reads from `State`, because a stub that returned a constant would be reproducible
// whether or not `Context` were seeded at all. The draw is two d20s plus an object
// lookup, which is enough to be sensitive to the seed, to the state, and to the
// order the two were combined in.
type stub struct {
	id      rules.ID
	title   string
	ruleset string

	grammar rules.Grammar
	kinds   []rules.Kind
	views   []rules.View

	// draws records every label Apply asked `Context.Rand` for, in order, so a test
	// can assert that the *labels* are what made a run reproducible rather than
	// asserting only that two runs matched.
	draws []string

	// shared is a source carried across calls when non-nil. It is the failure this
	// package's `Context` exists to make unrepresentable — a `*rand.Rand` on the
	// system rather than on the resolution — reproduced deliberately by a stub so
	// that the test which catches it is known to catch *something*.
	shared *rand.Rand

	// scribble makes Apply write to the state it was handed, the second thing a
	// resolver cannot be allowed to do.
	scribble bool

	// panics makes Apply give up the way a bad data pack would.
	panics bool
}

// The stub is the whole of a system's obligations, so the compiler is the cheapest
// check there is that this package's interface is implementable by something with no
// dependency on semiplane beyond it.
var _ rules.System = (*stub)(nil)

// A well-formed system, which every table below starts from and then breaks in one
// place. Built by a function rather than a package-level value because a shared
// mutable stub is how one test's kind list becomes another test's failure.
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
		kinds: []rules.Kind{"spell", "class", "feat", "creature", "ancestry"},
		views: []rules.View{{
			Name:     "character-sheet",
			Title:    "Character sheet",
			Renderer: rules.RendererGeneric,
			Shape:    rules.ShapeStatBlock,
		}},
	}
}

func (s *stub) ID() rules.ID { return s.id }

func (s *stub) Title() string { return s.title }

func (s *stub) RulesetVersion() string { return s.ruleset }

func (s *stub) Grammar() rules.Grammar { return s.grammar }

func (s *stub) Parse(expr string) (rules.Expr, error) {
	if !strings.Contains(expr, "d") {
		return rules.Expr{}, fmt.Errorf("stub: %q is not a dice expression", expr)
	}

	// A node that is not a string, deliberately: the point of `Node` being `any` is
	// that a system may hand over a tree, so the stub hands over a struct.
	return rules.NewExpr(s.id, s.grammar.Notation, expr, map[string]string{"source": expr}), nil
}

func (s *stub) Apply(
	_ context.Context,
	call rules.Context,
	state rules.State,
	in rules.Intent,
) ([]rules.Mutation, error) {
	if s.panics {
		panic("stub: a resolver that gives up half way through")
	}

	if s.scribble {
		// The one thing a resolver cannot be allowed to do, attempted through the
		// only accessor there is.
		for _, object := range state.Objects() {
			for idx := range object.Data {
				object.Data[idx] = 'X'
			}
		}
	}

	if s.shared != nil {
		return []rules.Mutation{{
			Target: in.Target,
			Op:     in.Op,
			Args:   []byte(strconv.Itoa(s.shared.IntN(1 << 20))),
		}}, nil
	}

	object, found := state.Lookup(in.Target)
	if !found {
		return nil, nil
	}

	label := in.Op.String() + "/" + in.Target.String()
	s.draws = append(s.draws, label)

	dice := call.Rand(label)
	total := 2*(dice.IntN(20)+1) + len(object.Data)

	mutation, err := rules.NewMutation(in.Target, in.Op, []byte(strconv.Itoa(total)))
	if err != nil {
		return nil, fmt.Errorf("stub: building a mutation: %w", err)
	}

	return []rules.Mutation{mutation}, nil
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

// aTabletop is a state with three objects of two kinds, declared out of order on
// purpose so a test can tell a sort from a coincidence.
func aTabletop(t *testing.T) rules.State {
	t.Helper()

	state, err := rules.NewState(7, []rules.Object{
		{ID: "p3", Kind: rules.KindToken, Data: []byte(`{"hp":3}`)},
		{ID: "p1", Kind: rules.KindToken, Data: []byte(`{"hp":10}`)},
		{ID: "s1", Kind: rules.KindScene, Data: []byte(`{}`)},
	})
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	return state
}

func anIntent(t *testing.T) rules.Intent {
	t.Helper()

	intent, err := rules.NewIntent("roll", "p1", []byte(`{"expr":"2d20+3"}`))
	if err != nil {
		t.Fatalf("NewIntent: %v", err)
	}

	return intent
}

func aContext(t *testing.T) rules.Context {
	t.Helper()

	return rules.Context{
		Campaign: 42,
		Actor:    7,
		Role:     domain.RoleGM,
		Seed:     rules.Seed{1, 2, 3},
	}
}

func TestValidateAcceptsASystemThatIsWellFormed(t *testing.T) {
	t.Parallel()

	if err := rules.Validate(wellFormed()); err != nil {
		t.Fatalf("a well-formed system was refused: %v", err)
	}
}

// TestASystemClaimingAKindSemiplaneOwnsIsRefused walks the ownership table of
// §10.2.1 and S-3.4 one kind at a time, and names each in the failure.
//
// Five cases rather than one, because the table is the argument and a loop over a
// slice would report "a system claimed a kind semiplane owns" for a regression that
// only added back `token`. The refusal is also required to be *specific*: which kind
// and which claimant, because "kind owned" is not something a plugin author can act
// on.
func TestASystemClaimingAKindSemiplaneOwnsIsRefused(t *testing.T) {
	t.Parallel()

	owned := []rules.Kind{
		rules.KindJournal,
		rules.KindHandout,
		rules.KindIndex,
		rules.KindToken,
		rules.KindScene,
	}

	if len(owned) != len(rules.SemiplaneKinds()) {
		t.Fatalf(
			"this test covers %d kinds, SemiplaneKinds reports %d",
			len(owned),
			len(rules.SemiplaneKinds()),
		)
	}

	for _, kind := range owned {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()

			system := wellFormed()
			system.kinds = append(system.kinds, kind)

			err := rules.Validate(system)
			if !errors.Is(err, rules.ErrKindOwned) {
				t.Fatalf("a system declaring %q was not refused for ownership: %v", kind, err)
			}

			var ownership *rules.OwnershipError
			if !errors.As(err, &ownership) {
				t.Fatalf("the refusal is not an *OwnershipError: %v", err)
			}

			if ownership.Kind != kind {
				t.Errorf("the refusal names kind %q, want %q", ownership.Kind, kind)
			}

			if ownership.System != system.id {
				t.Errorf("the refusal names system %q, want %q", ownership.System, system.id)
			}

			if ownership.Class() == "" {
				t.Error("the refusal has no class, so a log line carrying it cannot be matched")
			}

			if !strings.Contains(err.Error(), kind.String()) {
				t.Errorf("the refusal text does not name the kind: %s", err)
			}

			if !errors.Is(err, rules.ErrMalformedSystem) {
				t.Error("the refusal does not satisfy the umbrella a registry asks with errors.Is")
			}
		})
	}
}

// TestASystemClaimingNoKindsAtAllIsFine, because the ownership rule is about
// refusing a claim and not about requiring one: a system whose data is all prose is
// a system this contract has no reason to refuse.
func TestASystemClaimingNoKindsAtAllIsFine(t *testing.T) {
	t.Parallel()

	system := wellFormed()
	system.kinds = nil

	if err := rules.Validate(system); err != nil {
		t.Fatalf("a system declaring no kinds was refused: %v", err)
	}
}

func TestValidateRefusesEachContractFault(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		break_  func(system *stub)
		wantErr error
	}{
		{
			name:    "no id",
			break_:  func(system *stub) { system.id = "" },
			wantErr: rules.ErrInvalidID,
		},
		{
			name:    "an id with an uppercase character",
			break_:  func(system *stub) { system.id = "5e-2024X" },
			wantErr: rules.ErrInvalidID,
		},
		{
			name:    "an id with a trailing hyphen",
			break_:  func(system *stub) { system.id = "5e-" },
			wantErr: rules.ErrInvalidID,
		},
		{
			name:    "no title",
			break_:  func(system *stub) { system.title = "" },
			wantErr: rules.ErrNoTitle,
		},
		{
			name:    "no ruleset version",
			break_:  func(system *stub) { system.ruleset = "" },
			wantErr: rules.ErrNoRulesetVersion,
		},
		{
			name:    "a kind that is not a usable kind name",
			break_:  func(system *stub) { system.kinds = []rules.Kind{"Spell List"} },
			wantErr: rules.ErrInvalidKind,
		},
		{
			name:    "the same kind twice",
			break_:  func(system *stub) { system.kinds = []rules.Kind{"spell", "spell"} },
			wantErr: rules.ErrDuplicateKind,
		},
		{
			name:    "a view with no name",
			break_:  func(system *stub) { system.views[0].Name = "" },
			wantErr: rules.ErrInvalidView,
		},
		{
			name:    "a view with no title",
			break_:  func(system *stub) { system.views[0].Title = "" },
			wantErr: rules.ErrInvalidView,
		},
		{
			name:    "a view whose name is not a usable token",
			break_:  func(system *stub) { system.views[0].Name = "Character Sheet" },
			wantErr: rules.ErrInvalidView,
		},
		{
			name:    "a view with no renderer",
			break_:  func(system *stub) { system.views[0].Renderer = "" },
			wantErr: rules.ErrInvalidView,
		},
		{
			name: "a plugin view that also names a built-in shape",
			break_: func(system *stub) {
				system.views[0].Renderer = rules.RendererPlugin
			},
			wantErr: rules.ErrInvalidView,
		},
		{
			name: "a generic view with no shape",
			break_: func(system *stub) {
				system.views[0].Shape = ""
			},
			wantErr: rules.ErrInvalidView,
		},
		{
			name: "a grammar with no terms",
			break_: func(system *stub) {
				system.grammar.Terms = nil
			},
			wantErr: rules.ErrNotAGrammar,
		},
		{
			name: "a grammar term whose pattern does not compile",
			break_: func(system *stub) {
				system.grammar.Terms[0].Pattern = "(2d20"
			},
			wantErr: rules.ErrInvalidPattern,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			system := wellFormed()
			testCase.break_(system)

			err := rules.Validate(system)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("want %v, got %v", testCase.wantErr, err)
			}

			if !errors.Is(err, rules.ErrMalformedSystem) {
				t.Error("the refusal does not satisfy the umbrella a registry asks with errors.Is")
			}
		})
	}
}

func TestValidateRefusesNoSystemAtAll(t *testing.T) {
	t.Parallel()

	err := rules.Validate(nil)
	if !errors.Is(err, rules.ErrNoSystem) {
		t.Fatalf("a nil system was not refused: %v", err)
	}

	if !errors.Is(err, rules.ErrMalformedSystem) {
		t.Error("the refusal does not satisfy the umbrella a registry asks with errors.Is")
	}
}

// TestIdenticalStateIntentAndSeedYieldIdenticalMutationsAcrossOneHundredRuns is
// S-14.6, as a property rather than a single comparison.
//
// The number is not the point — any two runs would agree by luck, which is why it is
// one hundred — and the property is the point: the mutations are a function of
// (state, intent, seed) and of nothing else. Nothing in this test's environment
// varies between runs, so what it rules out is a resolver that reaches for something
// ambient, because an ambient source is what makes two runs of the *same* test
// disagree.
func TestIdenticalStateIntentAndSeedYieldIdenticalMutationsAcrossOneHundredRuns(t *testing.T) {
	t.Parallel()

	const runs = 100

	system := wellFormed()

	var first []rules.Mutation

	for run := range runs {
		// Built inside the loop on purpose: a shared `State` or `Intent` would let a
		// resolver that scribbled on either hide behind the previous run's copy, and
		// the scribbling has its own test below.
		mutations, err := system.Apply(t.Context(), aContext(t), aTabletop(t), anIntent(t))
		if err != nil {
			t.Fatalf("run %d: Apply: %v", run, err)
		}

		if len(mutations) != 1 {
			t.Fatalf("run %d: got %d mutations, want 1", run, len(mutations))
		}

		if run == 0 {
			first = mutations

			continue
		}

		if !reflect.DeepEqual(mutations, first) {
			t.Fatalf("run %d produced different mutations from run 0:\n run 0: %+v\n run %d: %+v",
				run, first, run, mutations)
		}
	}

	if len(system.draws) != runs {
		t.Fatalf("the resolver drew %d times across %d runs", len(system.draws), runs)
	}

	for _, draw := range system.draws {
		if draw != system.draws[0] {
			t.Fatalf("the resolver asked for a different draw label: %q then %q",
				system.draws[0], draw)
		}
	}
}

// TestTheSameInputsGiveTheSameMutationsAcrossDifferentActorsAndRoles pins the
// other half of the property: identity is carried, but it does not leak into the
// result unless a system uses it.
//
// A resolver that drew from `Campaign` or `Actor` — from the row it happened to be
// holding rather than from its own rules — would be reproducible and still wrong:
// two players at the same table would resolve the same intent differently. So the
// test varies the actor and the role and requires the mutations to be unchanged,
// while a *seed* change is required to change them.
func TestIdentityIsCarriedButDoesNotChangeTheResultAndTheSeedDoes(t *testing.T) {
	t.Parallel()

	system := wellFormed()
	state := aTabletop(t)
	intent := anIntent(t)

	asGM := rules.Context{Campaign: 42, Actor: 7, Role: domain.RoleGM, Seed: rules.Seed{1, 2, 3}}
	asPlayer := rules.Context{
		Campaign: 42,
		Actor:    99,
		Role:     domain.RolePlayer,
		Seed:     rules.Seed{1, 2, 3},
	}
	otherSeed := rules.Context{
		Campaign: 42,
		Actor:    7,
		Role:     domain.RoleGM,
		Seed:     rules.Seed{9, 9, 9},
	}

	same, err := system.Apply(t.Context(), asPlayer, state, intent)
	if err != nil {
		t.Fatalf("Apply as a player: %v", err)
	}

	reference, err := system.Apply(t.Context(), asGM, state, intent)
	if err != nil {
		t.Fatalf("Apply as the GM: %v", err)
	}

	if !reflect.DeepEqual(same, reference) {
		t.Errorf(
			"the actor and role changed the result:\n as gm: %+v\n player: %+v",
			reference,
			same,
		)
	}

	different, err := system.Apply(t.Context(), otherSeed, state, intent)
	if err != nil {
		t.Fatalf("Apply with another seed: %v", err)
	}

	if reflect.DeepEqual(different, reference) {
		t.Error(
			"a different seed produced identical mutations, so the seed is not reaching the draw",
		)
	}
}

// TestTheSharedRandomSourceFailureIsCaughtHere is the negative control for the test
// above, and it is why that test is worth running.
//
// A resolver that keeps its own `*rand.Rand` — the shape §10.3's "seeded RNG" is
// usually written to *avoid* — is perfectly reproducible for its first call and
// wrong for every call after it. Asserting that it diverges is therefore an
// assertion that can always fail: the stub draws from a source the first call
// advances, so the second call cannot produce the same numbers.
func TestTheSharedRandomSourceFailureIsCaughtHere(t *testing.T) {
	t.Parallel()

	system := wellFormed()
	system.shared = rand.New(rand.NewPCG(1, 2))

	first, err := system.Apply(t.Context(), aContext(t), aTabletop(t), anIntent(t))
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}

	second, err := system.Apply(t.Context(), aContext(t), aTabletop(t), anIntent(t))
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	if reflect.DeepEqual(first, second) {
		t.Fatal("a resolver carrying its own random source produced identical mutations " +
			"twice, so this test cannot tell the two designs apart")
	}
}

// TestASystemThatScribblesOnTheStateItWasHandedCannotBeSeen is S-10.2's "state is
// not mutated", as far as a type can enforce it.
//
// Two independent teeth, and both are asserted: the resolver's own view of the state
// is a copy, so writing to it changes nothing; and the *caller's* objects are a copy
// too, so writing to what the resolver was handed does not reach the hub's
// collection. Either clone removed and this fails.
func TestASystemThatScribblesOnTheStateItWasHandedCannotBeSeen(t *testing.T) {
	t.Parallel()

	objects := []rules.Object{{ID: "p1", Kind: rules.KindToken, Data: []byte(`{"hp":10}`)}}

	state, err := rules.NewState(7, objects)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	system := wellFormed()
	system.scribble = true

	if _, err := system.Apply(t.Context(), aContext(t), state, anIntent(t)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if string(objects[0].Data) != `{"hp":10}` {
		t.Errorf("the caller's object was changed by a resolver: %q", objects[0].Data)
	}

	object, found := state.Lookup("p1")
	if !found {
		t.Fatal("the state lost its object")
	}

	if string(object.Data) != `{"hp":10}` {
		t.Errorf("the state itself was changed by a resolver: %q", object.Data)
	}
}

// TestAPanicInsideApplyLeavesTheStateByteIdentical is S-10.2's sentence as a test:
// the state is not mutated because mutations are returned, never applied in place.
//
// The recovery is the hub's job and is P4's, so this test does the recovery itself
// and then asks the only question that matters: is the state the resolver was handed
// the same bytes it was before. It is, because the resolver was handed copies — which
// is the point of `Apply` taking a value and `State` keeping its fields unexported.
func TestAPanicInsideApplyLeavesTheStateByteIdentical(t *testing.T) {
	t.Parallel()

	objects := []rules.Object{
		{ID: "p1", Kind: rules.KindToken, Data: []byte(`{"hp":10}`)},
		{ID: "s1", Kind: rules.KindScene, Data: []byte(`{"fog":"dark"}`)},
	}

	state, err := rules.NewState(7, objects)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	before := fingerprint(state)

	system := wellFormed()
	system.panics = true
	system.scribble = true

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("the stub did not panic, so this test is not testing containment")
			}
		}()

		_, _ = system.Apply(t.Context(), aContext(t), state, anIntent(t))
	}()

	after := fingerprint(state)

	if before != after {
		t.Fatalf("a resolver that panicked changed the state:\nbefore %s\nafter  %s", before, after)
	}

	if string(objects[0].Data) != `{"hp":10}` || string(objects[1].Data) != `{"fog":"dark"}` {
		t.Fatalf("the caller's own objects changed: %q %q", objects[0].Data, objects[1].Data)
	}
}

// fingerprint renders everything a snapshot exposes, through its exported accessors
// and nothing else.
//
// Deliberately **not** an encoder this package owns. §14 asks whether a panic inside
// `Apply` leaves `campaign_state` byte-identical, and the honest reading is that the
// bytes are the *hub's* document — `realtime.Document`, which owns persistence — so a
// second encoding here would be a second answer to "what is a campaign's state" and
// the two would drift the first time a field was added. A test-local rendering keeps
// the comparison inside the contract and reads the state the only way a resolver
// can: through copies.
func fingerprint(state rules.State) string {
	var out strings.Builder

	fmt.Fprintf(&out, "revision %d, %d objects\n", state.Revision(), state.Len())

	for _, object := range state.Objects() {
		fmt.Fprintf(&out, "%s %s %q\n", object.ID, object.Kind, object.Data)
	}

	return out.String()
}

// TestAFreshContextCarriesIdentityAndASeedAndNothingElse is the "by construction"
// half of the determinism rule, as an assertion about the type.
//
// S-10.4 is enforced by a lint rule and by the conformance suite, and both of those
// work by noticing a call. This works before either can: a `Context` with a clock
// field would be a type through which a wall clock arrives, and no audit of call
// sites catches that — the clock would not be called, it would merely be there. So
// the field set is pinned, name by name and type by type, and the method set with
// it: a `Context` offering a second way to obtain randomness is the same hole one
// level down.
func TestAFreshContextCarriesIdentityAndASeedAndNothingElse(t *testing.T) {
	t.Parallel()

	wantFields := map[string]string{
		"Campaign": "int64",
		"Actor":    "int64",
		"Role":     "domain.Role",
		"Seed":     "rules.Seed",
	}

	contextType := reflect.TypeFor[rules.Context]()

	gotFields := make(map[string]string, 4)
	for field := range contextType.Fields() {
		gotFields[field.Name] = field.Type.String()
	}

	if !reflect.DeepEqual(wantFields, gotFields) {
		t.Fatalf("the fields a resolution is handed are\n %v\nwant\n %v",
			gotFields, wantFields)
	}

	// Exported methods only: an unexported one is invisible to reflection by type and
	// to a plugin as well.
	var gotMethods []string

	for method := range contextType.Methods() {
		gotMethods = append(gotMethods, method.Name)
	}

	wantMethods := []string{"Rand"}

	if !reflect.DeepEqual(gotMethods, wantMethods) {
		t.Fatalf("the methods a Context offers are %v, want %v", gotMethods, wantMethods)
	}
}

// TestNoRuleFileImportsAClockOrAnAmbientSourceOfRandomness is the same rule read off
// the source instead of the type.
//
// A test on the type cannot see a `time.Now` a resolver calls with its own hands, and
// a test on a resolver cannot run before the resolver exists. Reading the package's
// own files is the one check that works today and keeps working, and it is why this
// is an audit over parsed imports rather than a `strings.Contains` over the text:
// `math/rand/v2` is required and `math/rand` is not, and a substring search cannot
// tell those apart.
func TestNoRuleFileImportsAClockOrAnAmbientSourceOfRandomness(t *testing.T) {
	t.Parallel()

	forbidden := map[string]string{
		"time":        "a resolution must not be able to read a wall clock",
		"crypto/rand": "ambient randomness is not reproducible, so a roll is not auditable",
		"math/rand":   "the package-level source is seeded from the OS at startup",
	}

	// The one permitted import, and the reason it is permitted rather than merely
	// absent: a deterministic source derived from a recorded seed.
	required := map[string]string{
		"math/rand/v2": "Context.Rand returns a source derived from the seed, not the global one",
	}

	found := make(map[string][]string, len(required))

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		imports := parsedImports(t, name)

		for _, imported := range imports {
			if reason, banned := forbidden[imported]; banned {
				t.Errorf("%s imports %q: %s", name, imported, reason)
			}

			if _, wanted := required[imported]; wanted {
				found[imported] = append(found[imported], name)
			}
		}
	}

	for imported, reason := range required {
		if len(found[imported]) == 0 {
			t.Errorf("nothing imports %q any more: %s", imported, reason)
		}
	}
}

// parsedImports returns the import paths of one file in this directory, parsed
// rather than matched: an import can be aliased, grouped and commented in ways that a
// substring search has to guess about.
func parsedImports(t *testing.T, name string) []string {
	t.Helper()

	parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}

	paths := make([]string, 0, len(parsed.Imports))

	for _, imported := range parsed.Imports {
		paths = append(paths, strings.Trim(imported.Path.Value, `"`))
	}

	return paths
}

// TestEveryRefusalIsReachableThroughTheUmbrella is a meta-check on the sentinel set.
//
// A refusal that does not satisfy `ErrMalformedSystem` is a refusal a registry
// cannot act on, and the way one appears is a new `errors.New` beside a type rather
// than a `fmt.Errorf` in the block. This asserts the shape of the ones the table
// above reaches, so the shape is a property and not a convention.
func TestEveryRefusalIsReachableThroughTheUmbrella(t *testing.T) {
	t.Parallel()

	refusals := []error{
		rules.ErrNoSystem,
		rules.ErrInvalidID,
		rules.ErrNoTitle,
		rules.ErrNoRulesetVersion,
		rules.ErrInvalidKind,
		rules.ErrDuplicateKind,
		rules.ErrInvalidView,
		rules.ErrNotAGrammar,
		rules.ErrInvalidPattern,
		rules.ErrInvalidOp,
		rules.ErrInvalidObject,
		rules.ErrOpArgsTooLarge,
		rules.ErrDuplicateObject,
		rules.ErrKindOwned,
		rules.ErrInvalidSeed,
		rules.ErrInvalidRole,
	}

	for _, refusal := range refusals {
		if !errors.Is(refusal, rules.ErrMalformedSystem) {
			t.Errorf("%v does not satisfy ErrMalformedSystem", refusal)
		}
	}
}
