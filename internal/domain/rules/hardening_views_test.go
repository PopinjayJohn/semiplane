// P12 H6, the third claim: **every declared view renders for an empty, a typical and
// a maximal `Derive` output, and an unknown field is ignored rather than fatal.**
//
// §14's table says it in one cell:
//
//	| View rendering | Every declared view renders for an empty, a typical, and a maximal
//	| `Derive` output; unknown fields are ignored, not fatal |
//
// Two halves, and they fail differently.
//
// # "Renders for an empty, a typical and a maximal output"
//
// A view that handles only the typical case is a view that breaks on the first empty
// state, and **an empty state is not an exotic one** — it is what a campaign looks
// like before its first token is placed, what a list view looks like on a table with
// nothing in the condition, and what every view looks like after a GM removes the last
// thing it showed. So all three shapes are asserted for **every view of every system
// this build ships**, and the fleet is derived from the same constructors the
// composition root uses (`cmd/server/systems.go` is the one place that decides what
// this binary resolves under, and a view table that had to be edited beside it is a
// second list).
//
// **The shapes are about content, not about a nil state**, which is the reading §14's
// own words support and the one a fixture can express: `minimal` is a token with a
// name and nothing else, `typical` is a token with scores, hit points and an attack,
// `maximal` is one with conditions, two attacks and a stored roll. A fourth shape is
// added — **a state with nothing on it at all** — because "empty" and "nothing" are
// different questions and a view that answers the first need not answer the second.
// `notfive.Derive`'s comment is the sentence this follows: "a spool with no thread and
// no dye is a spool, not a missing answer".
//
// **How a view is addressed is probed rather than declared.** `rules.View` says whether
// a renderer is a plugin's or one of the built-in four, and which *shape*; it does not
// say whether the view is about one game object or about the whole table, because
// nothing in the contract could know that — it is the system's own declaration.
// Guessing wrong would turn a real answer into a reported failure, so the probe asks
// the view and then **requires the same form to answer for every shape**, which is
// itself a §14 claim: a view that renders when the query names an object and refuses
// when it does not is a view with two behaviours and one name.
//
// # "Unknown fields are ignored, not fatal"
//
// This is a **forward-compatibility** claim and it is the reason §14 words it as an
// obligation rather than as a nicety: a client or a renderer built against an older
// payload must still read a payload that has gained fields, or adding a field to a
// system breaks every older thing that renders it.
//
// What is reachable from `internal/domain` is the *payload's wire form*, and that is
// where the claim lives. The UI tier does not decode a payload — `Reader.View` hands a
// `rules.Payload` to a templ component that takes the system's own Go type out of it,
// which is type-safe rather than a traversal (§10.6.1) — so the renderable form of a
// payload is the JSON a renderer or a client sees, and that is what is asserted:
//
//   - the payload **marshals**, and to an object or an array, never to `null`. A `null`
//     is what a nil slice marshals to, and a renderer reading it in JavaScript gets a
//     length error rather than an empty list.
//   - the marshalled form **decodes into a struct that declares no fields at all**
//     without error. That is the literal statement of the claim: a renderer that knows
//     none of these fields is not made fatal by them.
//   - **every key is lower_snake_case**, because that is the wire the UI tier's
//     components and any client read, and a payload whose tag drifted to `hitPoints`
//     renders as an empty field on every card.
//   - and the **contrast**, which is the half that makes the rule legible: the same
//     unknown field inside an operation's `args` is **refused**. `engine.go` says why
//     ("an op's arguments are a request, where an unknown field means the client and
//     the server disagree about what was asked for") and `realtime`'s codec refuses it
//     too. Strict in, tolerant out; a payload that refused an unknown field would make
//     adding one to a system a breaking change for every renderer at once.
//
// **What this file cannot reach, and says so:** the renderer-side half — a built-in
// renderer or a plugin component actually walking an empty, a typical and a maximal
// payload and producing a document — lives in `internal/web/plugins` and
// `internal/httpapi/plugins`, which this work item does not own and `internal/domain`
// cannot import without a cycle. `rules.Payload`'s doc comment names the same split
// from the other side: "unknown fields are ignored, not fatal is the requirement that
// keeps that true … a property of the renderers rather than of this accessor". What is
// asserted here is the half that *is* the system's: the payload a renderer is handed
// is a form it can read, and reading more or fewer fields than it knows is not fatal.

package rules_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
)

// addressing is how a view is asked: about one game object, or about the table.
//
// **Probed, because the contract cannot declare it.** `rules.View` names a renderer and
// a shape; nothing in it says whether `Query.Object` is consulted, because that is the
// system's own declaration (`dnd5e`'s `deriveStatBlock` refuses an empty object while
// its `deriveConditionReference` deliberately ignores one). A test that assumed either
// would report a real answer as a failure, which is the worse of the two mistakes.
type addressing int

const (
	// aboutNothing is a campaign-wide query: `Query.Object` empty.
	aboutNothing addressing = iota

	// aboutOneObject is a query naming one game object.
	aboutOneObject
)

func (a addressing) String() string {
	if a == aboutOneObject {
		return "naming one game object"
	}

	return "naming none"
}

// viewShape is one of the shapes §14 names, plus the fourth.
//
// **A shape carries the tabletop, not the payload**: the payload is what the view
// produces from it, and a fixture that handed over a payload would be asserting that a
// view renders something the system never said.
type viewShape struct {
	// name is what a failure reports.
	name string

	// objects is the tabletop for this shape.
	objects []rules.Object
}

// theShapes is the four shapes every declared view is asked about.
//
// **Built per system from its `vocabulary`**, because the words mean the same thing in
// every system and the bytes do not: 5e's "nothing at all" is a creature with no
// abilities and the loom's is a spool of length zero.
func theShapes(t *testing.T, one vocabulary) []viewShape {
	t.Helper()

	return []viewShape{
		{
			name:    "empty: one object with nothing on it",
			objects: []rules.Object{one.minimal(t)},
		},
		{
			name:    "typical: four objects with ordinary content",
			objects: one.typical(t, 4),
		},
		{
			name:    "maximal: eight objects carrying everything a card can show",
			objects: one.maximal(t, 8),
		},
		{
			name:    "nothing at all on the table",
			objects: nil,
		},
	}
}

// TestEveryDeclaredViewRendersForAnEmptyATypicalAndAMaximalOutput is §14's row, over
// every view every shipped system declares.
//
// **Table-driven over fleets, views and shapes, and every cell asserts four things** —
// because "renders" is four claims and any one of them can hold alone:
//
//  1. **`Derive` answers.** No error. A view that refuses the empty case is the failure
//     this whole file is about, and it is the one a passing "typical" test hides.
//  2. **The payload is well formed**: `Valid()`, and `View()` is the name the query
//     asked for. `Payload.View` is what picks the renderer, so a payload answering a
//     different view is a card rendered with the wrong renderer and nothing saying so.
//  3. **The value is a form a renderer can read**: it marshals, and to an object or an
//     array rather than to `null`.
//  4. **The identity survives emptiness.** A payload about one game object carries that
//     object's id and the list views carry a list — so "empty" means "nothing to show"
//     and never "nothing at all", which is the difference between an empty card and a
//     blank document.
//
// The **fourth shape is where the fourth claim lives**: a state with nothing on it. A
// per-object view must refuse it — there is no creature to describe — and a table-wide
// view must answer with an empty collection. What it must **not** do is panic, and what
// it must not do is answer a per-object query with a card about no object, which is the
// failure `deriveStatBlock`'s own comment refuses by name.
func TestEveryDeclaredViewRendersForAnEmptyATypicalAndAMaximalOutput(t *testing.T) {
	t.Parallel()

	for _, one := range shippedFleets(t) {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()

			views := one.system.Views()
			if len(views) == 0 {
				t.Fatalf("the system declares no view, so there is nothing for §14's row " +
					"to be about")
			}

			shapes := theShapes(t, one.vocab)

			for _, declared := range views {
				if err := declared.Check(); err != nil {
					t.Fatalf("the system declares a view it would refuse to register: %v", err)
				}
			}

			for _, declared := range views {
				about := probeAddressing(t, one.system, declared, shapes[2])

				t.Run(declared.Name, func(t *testing.T) {
					t.Parallel()

					for _, shape := range shapes {
						t.Run(shape.name, func(t *testing.T) {
							t.Parallel()

							assertTheAnswer(t, declared, shape, about,
								derive(t, one, declared, shape, about, 0))
						})
					}
				})
			}
		})
	}
}

// TestAListViewHonoursItsLimit is the "maximal" half of §14's row, and it is separate
// because it is the only one of the four claims that needs a *second* query.
//
// `rules.Query` says `Limit` caps "how many entries a list-shaped payload should carry"
// and that zero means the system's own default, and `rules.Query`'s comment gives the
// reason: "§14's 'renders for an empty, a typical and a maximal `Derive` output' needs
// a maximal that is still a number". A view that ignores `Limit` has no maximal at all
// — it has whatever the table happened to hold — so **a list view asked for one entry
// must answer with one**.
//
// **Every value that is not a slice is skipped rather than asserted about**, and that is
// not a way of dodging the check: a stat block is one object and a `Limit` on it means
// nothing, so requiring a bound there would be requiring a feature. The classification
// is by the payload's own reflection kind rather than by the view's declared `Shape`,
// because the shape is a *renderer's* name and this is a question about the value.
func TestAListViewHonoursItsLimit(t *testing.T) {
	t.Parallel()

	for _, one := range shippedFleets(t) {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()

			shapes := theShapes(t, one.vocab)

			for _, declared := range one.system.Views() {
				about := probeAddressing(t, one.system, declared, shapes[2])

				t.Run(declared.Name, func(t *testing.T) {
					t.Parallel()

					payload := deriveOrExplain(t, one, declared, shapes[2], about)

					if !isCollection(payload.Value()) {
						return
					}

					for _, limit := range []int{1, 2, 3} {
						capped := deriveOrExplain(t, one, declared, shapes[2], about, limit)

						if got := collectionLength(t, capped); got > limit {
							t.Errorf(
								"a maximal table asked for at most %d entries produced %d; "+
									"`Limit` is the only thing that makes a maximal output a "+
									"number rather than whatever the table happened to hold",
								limit, got,
							)
						}
					}
				})
			}
		})
	}
}

// TestADerivedPayloadIsAFormARendererCanReadAndAnUnknownFieldIsNotFatal is §14's
// second clause, asserted over every view and every shape.
//
// Four assertions, each of which fails on a different mistake:
//
//   - **It marshals.** A payload carrying a type `encoding/json` cannot encode is a
//     payload no renderer can be handed, and the failure would surface in the UI tier
//     as a 500 rather than as a plugin bug.
//   - **It decodes into a struct declaring no fields.** The literal statement of "unknown
//     fields are ignored, not fatal": a renderer that knows *none* of these fields is
//     not made fatal by all of them.
//   - **Its keys are lower_snake_case**, because the UI tier's components and any
//     client read the wire, and a tag that drifted to `hitPoints` renders as an empty
//     field on every card with nothing saying why.
//   - **An unknown field in a payload survives a round trip through a narrower
//     renderer.** Encoded, then decoded into a `map[string]any`, and the keys are still
//     there — which is what "a payload may gain a field" looks like from the outside.
//
// **And the contrast, in `TestAnUnknownFieldInAnOperationIsRefusedRatherThanIgnored`.**
func TestADerivedPayloadIsAFormARendererCanReadAndAnUnknownFieldIsNotFatal(t *testing.T) {
	t.Parallel()

	for _, one := range shippedFleets(t) {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()

			shapes := theShapes(t, one.vocab)

			for _, declared := range one.system.Views() {
				about := probeAddressing(t, one.system, declared, shapes[2])

				t.Run(declared.Name, func(t *testing.T) {
					t.Parallel()

					for _, shape := range shapes {
						got := derive(t, one, declared, shape, about, 0)

						if !got.answered() {
							// A refusal carries no payload, and a payload is what this
							// test is about. The refusal itself is the previous test's
							// business, and `assertTheAnswer` is what holds it — so the one
							// legitimate refusal is skipped here and only here. **The
							// condition is the addressing as well as the empty table**: a
							// table-wide view refusing an empty table has no payload to
							// look at either, and skipping it would be skipping the §14
							// failure.
							if len(shape.objects) == 0 && about == aboutOneObject {
								continue
							}

							t.Fatalf("%s: %s: Derive: %v", declared.Name, shape.name, got.refusal)
						}

						encoded, err := json.Marshal(got.payload.Value())
						if err != nil {
							t.Fatalf("%s: %s: the payload does not marshal, so no renderer "+
								"can be handed it: %v", declared.Name, shape.name, err)
						}

						assertDecodesIntoARendererThatKnowsNothing(
							t, encoded, declared.Name, shape.name,
						)
						assertKeysAreWireShaped(t, encoded, declared.Name, shape.name)
					}
				})
			}
		})
	}
}

// TestAnUnknownFieldInAnOperationIsRefusedRatherThanIgnored is the other half of the
// asymmetry, and it is here because §14's clause only means something next to it.
//
// **Strict in, tolerant out.** An operation's `args` are a request: an unknown field is
// a client and a server disagreeing about what was asked for, and silently discarding
// it is how a client spends an afternoon on a typo the server never mentioned. So 5e
// reads its arguments through `decodeArgs`, which refuses an unknown field, and
// `realtime`'s codec refuses one at the frame level for the same reason.
//
// The test feeds 5e a well-formed `attack` with one extra field and requires a
// refusal. **It is a table over every operation rather than one case**, because the
// refusal could be per-op — a pack whose `apply_status` decoder forgot the strictness
// would answer while its `attack` decoder refused, and both would be "the args are
// read strictly" to anybody who tested one.
func TestAnUnknownFieldInAnOperationIsRefusedRatherThanIgnored(t *testing.T) {
	t.Parallel()

	one := fiveeFleet(t)

	// One extra field, named so that a client would plausibly have sent it: a field the
	// server does not know is exactly what an out-of-date client looks like, and this
	// file's claim is that the *payload* side tolerates that while the *request* side
	// does not.
	const extra = `,"client_note":"an old client"`

	for _, testCase := range []struct {
		name string
		op   rules.Op
		args string
	}{
		{
			name: "roll",
			op:   dnd5e.OpRoll,
			args: `{"expr":"1d20+3","dc":15` + extra + `}`,
		},
		{
			name: "attack",
			op:   dnd5e.OpAttack,
			args: `{"attack":"greataxe","defender":"typ_1"` + extra + `}`,
		},
		{
			name: "heal",
			op:   dnd5e.OpHeal,
			args: `{"amount":4` + extra + `}`,
		},
		{
			name: "apply_condition",
			op:   dnd5e.OpApplyCondition,
			args: `{"condition":"prone"` + extra + `}`,
		},
		{
			name: "apply_status",
			op:   dnd5e.OpApplyStatus,
			args: `{"hp":12` + extra + `}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			intent, err := rules.NewIntent(testCase.op, "typ_0", []byte(testCase.args))
			if err != nil {
				t.Fatalf("building the intent: %v", err)
			}

			state := stateOf(t, viewShape{objects: one.vocab.typical(t, 2)})

			mutations, err := one.system.Apply(
				t.Context(), gmCall(t), state, intent,
			)

			if err == nil {
				t.Fatalf(
					"%q accepted an argument it does not know and answered with %d "+
						"mutation(s); a request carrying an unknown field is a client and a "+
						"server disagreeing about what was asked for, and discarding it is "+
						"how a client spends an afternoon on a typo the server never mentioned",
					testCase.op, len(mutations),
				)
			}

			if len(mutations) != 0 {
				t.Errorf("a refused %q returned %d mutation(s)", testCase.op, len(mutations))
			}
		})
	}
}

// TestEveryViewIsAlsoAPureFunctionOfTheStateItIsGiven is the determinism claim applied
// to the render side, and it is in this file rather than the first because a `Derive`
// output is a *rendering input* and §14's determinism row is about resolutions.
//
// **A hundred runs, byte for byte, per view and per shape.** The reason it is worth a
// hundred rather than a comparison is the same as the resolution side, and the mutation
// it exists to catch is real: `deriveConditionIndex` builds its counts in a
// `map[string]int` and walks the *pack's* condition list to order them, precisely
// because "a tag cloud whose order is a map's iteration order differs between runs".
// Replace that walk with one over the counts map and every single-run check still
// passes.
//
// The comparison is over the marshalled bytes, so it holds the whole answer rather than
// the lengths of its parts — and the failure message prints **which view, which shape
// and which run**, and nothing from the payload.
func TestEveryViewIsAlsoAPureFunctionOfTheStateItIsGiven(t *testing.T) {
	t.Parallel()

	for _, one := range shippedFleets(t) {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()

			shapes := theShapes(t, one.vocab)

			for _, declared := range one.system.Views() {
				about := probeAddressing(t, one.system, declared, shapes[2])

				t.Run(declared.Name, func(t *testing.T) {
					t.Parallel()

					// Only the shapes that answer. A refusal is a stable answer too, so
					// comparing it costs nothing — but there is nothing to encode, and a
					// digest of the refusal's text would be testing `errors.Is` all over
					// again.
					for _, shape := range shapes {
						answer, ok := deriveBytes(t, one, declared, shape, about)
						if !ok {
							continue
						}

						for run := 1; run < determinismRuns; run++ {
							again, answered := deriveBytes(t, one, declared, shape, about)
							if !answered {
								t.Fatalf("%s: the shape answered on run 0 and was refused on "+
									"run %d", shape.name, run)
							}

							if !bytes.Equal(answer, again) {
								t.Fatalf(
									"%s: run %d of %d derived a different payload from run 0 with "+
										"the same state and query; a payload is a function of the "+
										"state and the query and of nothing else, so a rendered "+
										"card can differ between two readers of the same table",
									shape.name, run, determinismRuns,
								)
							}
						}
					}
				})
			}
		})
	}
}

// TestEveryShippedSystemDeclaresAtLeastOneViewAndDeclaresNoViewTwice is the shape the
// §14 table assumes, asserted so that "every declared view" cannot quietly become "no
// view at all".
//
// **A view that handles only the typical case is a view that breaks on the first empty
// state — and a system that declares no view has nothing to break.** A table-driven test
// over a system's own declarations is satisfied by an empty declaration for the same
// reason `conformance`'s determinism audit refuses a scenario that changes nothing: a
// gate over no fixtures finds no failures and certifies nothing.
//
// **The duplicate check is the other direction, and it is not tidiness.** `Payload.View`
// is what picks the renderer, so a system declaring `character-sheet` twice has two
// answers and one renderer: the second declaration is either unreachable or it is the
// one that answers, and neither is a thing a plugin author can see. `rules.Validate`
// does not refuse it — nothing in the contract could, since the name is the system's own
// to choose — so nothing else would notice.
func TestEveryShippedSystemDeclaresAtLeastOneViewAndDeclaresNoViewTwice(t *testing.T) {
	t.Parallel()

	for _, one := range shippedFleets(t) {
		views := one.system.Views()

		if len(views) == 0 {
			t.Errorf("%s declares no view, so §14's row is about nothing and every test "+
				"above walked an empty table", one.name)

			continue
		}

		seen := make(map[string]struct{}, len(views))

		for _, declared := range views {
			if _, twice := seen[declared.Name]; twice {
				t.Errorf("%s declares the view %q twice, and `Payload.View` is what picks "+
					"the renderer, so the two answers are one renderer with two meanings",
					one.name, declared.Name)
			}

			seen[declared.Name] = struct{}{}
		}

		// And the count, because a table of one is a system that has not built anything
		// yet and a table of three is a system that has — the assertion that catches a
		// fleet assembled from a list somebody edited by hand.
		if len(views) == 1 {
			t.Logf("%s declares exactly one view (%q); §14's row is asked of it and the "+
				"row is thinner for it, which is worth knowing rather than assuming",
				one.name, views[0].Name)
		}
	}
}

// TestTheStatBlockForATokenWhoseBodyCarriesNoLevelIsARefusal pins a **finding**, and
// the name says plainly that pinning it is not endorsing it.
//
// # The finding
//
// §14 requires every declared view to render for an **empty** `Derive` output, and the
// most natural reading of "empty" for a stat block is a token whose body carries
// nothing. `readCreature` accepts such a body — it decodes it without complaint and
// fills the ability map — and `rules.NewState` accepts it as a game object, so **such a
// token is a state this build can hold**. Its stat block is then refused:
//
//	dnd5e: the proficiency table covers levels 1-4, 5-8, 9-12, 13-16, 17-20 and not 0
//
// So for a per-object view the empty case is unreachable, and §14's row cannot be asked
// of `stat-block` for the shape it was written about. The refusal is the right *kind* —
// it names the input it choked on rather than rendering zeroes — and it is 5e's own
// vocabulary rather than a bug in the resolver. The question is whether a token with no
// level is a state a campaign may hold, and this work item cannot answer it: the answer
// belongs to whoever owns `readCreature` and `deriveStatBlock`.
//
// **What this test does with the finding**: pins it, so that a change in either
// direction is noticed. A build that starts rendering a card for a level-0 token and a
// build that stops accepting one are both *decisions*, and a test that watched for only
// one of them would let the other happen silently. It is a canary in the sense
// `TestTheDetectorDetectsADivergentResolver` is one: it fails when the behaviour moves,
// and it says in its own failure which way it moved.
//
// **Why `internal/domain` cannot fix it here**: the fix is either in `dnd5e`'s resolver
// (a decision about what a level-0 token means) or in `rules.NewState` (refusing a game
// object whose body a system cannot read, which `rules` cannot do because it does not
// know the body). Neither is a hook this work item may add, so the finding is reported
// rather than patched — and the corpus's "empty" shape is a level-1 creature with nothing
// else on it, so that §14's row is asked of shapes a real tabletop can hold.
func TestTheStatBlockForATokenWhoseBodyCarriesNoLevelIsARefusal(t *testing.T) {
	t.Parallel()

	one := fiveeFleet(t)

	// The body `readCreature` accepts and `NewState` carries: no level, no scores, no
	// attacks, no conditions. Not `{}` — a *named* token with nothing on it, so the
	// refusal cannot be mistaken for "there is no token here".
	body := []byte(`{"name":"Nobody rolled this in"}`)

	state, err := rules.NewState(1, []rules.Object{{
		ID:   "unlevelled_0",
		Kind: rules.KindToken,
		Data: body,
	}})
	if err != nil {
		t.Fatalf("a game object carrying %q is not a state this build can hold: %v", body, err)
	}

	payload, err := one.system.Derive(
		state, rules.Query{View: dnd5e.ViewStatBlock, Object: "unlevelled_0"},
	)
	if err == nil {
		encoded, marshalErr := json.Marshal(payload.Value())

		t.Fatalf(
			"a token whose body carries no level now renders a stat block (%s); that is "+
				"either the decision somebody made about what a level-0 token means — in "+
				"which case this test's comment is the wrong place for the rule — or a card "+
				"rendering zeroes for a creature at no level. marshal error: %v",
			encoded, marshalErr,
		)
	}

	// The refusal must name the input it choked on. A refusal saying "invalid" has moved
	// the diagnosis onto whoever reads the log, and the finding would then be "a stat
	// block is unavailable and nothing says why", which is worse.
	if !strings.Contains(err.Error(), "proficiency") && !strings.Contains(err.Error(), "level") {
		t.Errorf("the refusal %q does not say what it choked on; the finding is about a "+
			"specific input, and a refusal that does not name it is a different one", err)
	}
}

// # The helpers, and what each one refuses

// fiveeFleet returns the 5e fleet the request-side test resolves against.
//
// **The base pack alone**, because the strictness under test is `decodeArgs` — a
// function every edition shares — and the base pack is the smallest fixture that
// exercises it.
func fiveeFleet(t *testing.T) fleet {
	t.Helper()

	for _, one := range shippedFleets(t) {
		if strings.Contains(one.name, "base pack") {
			return one
		}
	}

	t.Fatal("no 5e fleet over its base pack, so the request-side test has nothing to ask")

	return fleet{}
}

// gmCall is a rule context for the GM, for the shapes that resolve rather than derive.
func gmCall(t *testing.T) rules.Context {
	t.Helper()

	call, err := rules.NewContext(1, 1, gmRole(), rules.Seed{1, 2, 3})
	if err != nil {
		t.Fatalf("building a rule context: %v", err)
	}

	return call
}

// stateOf builds a snapshot from one shape's tabletop.
func stateOf(t *testing.T, shape viewShape) rules.State {
	t.Helper()

	state, err := rules.NewState(1, shape.objects)
	if err != nil {
		t.Fatalf("%s: building the tabletop: %v", shape.name, err)
	}

	return state
}

// queryFor builds the query for one view and shape.
//
// **`Limit` is the last argument rather than a field of the shape**, because a shape is
// about content and a limit is about what the caller asked to see; the two vary
// independently and the table walks both.
func queryFor(declared rules.View, shape viewShape, about addressing, limit int) rules.Query {
	query := rules.Query{View: declared.Name, Limit: limit}

	if about == aboutOneObject && len(shape.objects) > 0 {
		query.Object = shape.objects[0].ID
	}

	return query
}

// probeAddressing works out whether a view consults `Query.Object`, by asking it twice
// over the same table and comparing the two answers.
//
// **Behaviourally, and the first version of this got it wrong in a way the assertions
// downstream felt.** It classified a view by "does it answer when the query names an
// object?", and `deriveConditionReference` answers that way *by design* — its own
// comment says the query's `Object` is "ignored rather than refused" because a client
// asking for a condition reference with a token in hand is asking the right question
// with a field filled in. So three of 5e's five views were classified as per-object,
// and the identity assertion then demanded that a pack-level reference list mention
// `typ_0` — a requirement about the wrong system entirely.
//
// **The question is therefore whether the answer *changes* with the object named**,
// which is the actual difference between the two kinds of view and is settled by the
// system rather than by a reader's guess. A view that tolerates a named object and
// answers the same either way is a table-wide view, and this is why: `condition-index`
// counts what the whole table carries and `condition-reference` lists what the *pack*
// declares, so neither has an object to name.
//
// **Two objects, and they must differ**: over a table whose objects are identical, "the
// answer changed" would be false for a per-object view too. `typical` builds objects at
// different levels and the card shows the level, so the two answers differ for a view
// that consults the field.
//
// **A view that answers under neither form is a §14 failure and stops the run here**,
// because every question this file would then ask of it would fail for the same
// uninteresting reason.
//
// **The callers pass the *maximal* shape, and that is a correction this file's own
// mutation made.** The probe first used the typical shape, whose tokens carry no
// conditions — so a mutation making the tag cloud refuse an empty result made the probe
// report "this view answers neither way" and the failure named the *probe* rather than
// the empty case. A probe must run against a shape where the view answers, or it is
// measuring emptiness and calling it addressing.
func probeAddressing(
	t *testing.T,
	system rules.System,
	declared rules.View,
	typical viewShape,
) addressing {
	t.Helper()

	if len(typical.objects) < 2 {
		t.Fatalf("the probe needs two objects on the table to tell the two kinds of view "+
			"apart, and the shape it was given carries %d", len(typical.objects))
	}

	state := stateOf(t, typical)

	first := viewShape{name: "typical, first object", objects: typical.objects[:1]}
	second := viewShape{name: "typical, second object", objects: typical.objects[1:2]}

	firstAnswer, firstErr := system.Derive(state, queryFor(declared, first, aboutOneObject, 0))
	if firstErr != nil {
		// It does not answer when a game object is named. It had better answer when
		// none is, or §14's row cannot be asked of it at all.
		_, noneErr := system.Derive(state, queryFor(declared, typical, aboutNothing, 0))
		if noneErr != nil {
			t.Fatalf(
				"the view %q answered neither naming one game object (%v) nor naming none "+
					"(%v) over a table of %d objects, so §14's row cannot be asked of it",
				declared.Name, firstErr, noneErr, len(typical.objects),
			)
		}

		return aboutNothing
	}

	secondAnswer, secondErr := system.Derive(state, queryFor(declared, second, aboutOneObject, 0))
	if secondErr != nil {
		t.Fatalf("the view %q answered about %q and then refused the same question about "+
			"%q; a view that consults the object named cannot answer about one and refuse "+
			"about the next", declared.Name, first.objects[0].ID, second.objects[0].ID)
	}

	firstBytes, err := json.Marshal(firstAnswer.Value())
	if err != nil {
		t.Fatalf("the view %q does not marshal its answer: %v", declared.Name, err)
	}

	secondBytes, err := json.Marshal(secondAnswer.Value())
	if err != nil {
		t.Fatalf("the view %q does not marshal its answer: %v", declared.Name, err)
	}

	if bytes.Equal(firstBytes, secondBytes) {
		// It tolerates a named object and answers the same either way: a table-wide
		// view, and the one the identity assertion must leave alone.
		return aboutNothing
	}

	return aboutOneObject
}

// answer is one query's outcome, and both halves of it are load-bearing.
//
// **A struct rather than a payload or an error**, because the interesting cell of the
// table is the one where *refusing is the correct answer* — a view about one game
// object asked over a table with nothing on it — and a `(rules.Payload, error)` pair
// invites collapsing that cell into a skip. Keeping the refusal as a value is what lets
// the assertions say what a refusal must look like rather than merely tolerate one.
type answer struct {
	// payload is what `Derive` answered, when it answered.
	payload rules.Payload

	// refusal is what it refused with, when it refused.
	refusal error
}

// answered reports whether the view answered.
func (a answer) answered() bool { return a.refusal == nil }

// derive asks one view one question and returns the answer or the refusal.
//
// **No `t.Fatalf` in here.** A refusal is a legitimate outcome for one cell of the
// table and a §14 failure for every other, and only the caller knows which cell it is
// asking about.
func derive(
	t *testing.T,
	one fleet,
	declared rules.View,
	shape viewShape,
	about addressing,
	limit int,
) answer {
	t.Helper()

	payload, err := one.system.Derive(stateOf(t, shape), queryFor(declared, shape, about, limit))
	if err != nil {
		return answer{refusal: err}
	}

	return answer{payload: payload}
}

// assertTheAnswer is §14's row over one cell, and it is where the four claims live.
//
// **The refusal branch is not a skip.** For a view about one game object asked over a
// table with nothing on it there is no object to answer about, and the correct answer
// is a refusal — but it must be the *contract's* view refusal (`rules.ErrInvalidView`),
// because a system that invented its own sentinel here would leave the composition
// root's adapter unable to route it, which is §10.7's one reason a refusal carries no
// wire word of its own. A view that answered that cell with a payload instead would be
// rendering a card about nothing, which is the failure `View.Check`'s comment calls "a
// blank document that says nothing went wrong".
func assertTheAnswer(
	t *testing.T,
	declared rules.View,
	shape viewShape,
	about addressing,
	got answer,
) {
	t.Helper()

	switch {
	case got.answered():
		assertPayloadIsWellFormed(t, got.payload, declared.Name)
		assertRenderable(t, got.payload, declared.Name, shape.name)
		assertTheObjectIsNamed(t, got.payload, shape, declared.Name, about)
	case len(shape.objects) == 0 && about == aboutOneObject:
		// An empty table, where a view about **one game object** has nothing to answer
		// about. Two things are required and neither is "the contract's sentinel":
		//
		//   - **no payload.** A card about no object is the forbidden outcome, and it is
		//     the one this cell exists to catch: `deriveConditionIndex`'s comment calls
		//     the alternative "a card rendered from nothing … a blank document that says
		//     nothing went wrong".
		//   - **a refusal that says something.** Which *sentinel* it is belongs to the
		//     system — 5e refuses `rules.ErrInvalidView` ("the query named none") and the
		//     loom refuses its own `ErrNoSuchSpool` ("no spool by that name is on the
		//     table"), and both are correct: §10.7 puts a system's refusals in its own
		//     vocabulary so the composition root's adapter can route them. A first
		//     version of this assertion required `rules.ErrInvalidView` and reported the
		//     loom as broken for using its own error type.
		//
		// **The `about == aboutOneObject` half of this condition was added by a
		// mutation**, and it is the sharper of the two errors this file could have made.
		// Written as `len(shape.objects) == 0` alone, the branch said *any* view may
		// refuse over an empty table — and a list-shaped view then refused an empty
		// table with "there is nothing to list" and the whole file stayed green. That is
		// precisely the failure §14's row is about ("a view that handles only the
		// typical case is a view that breaks on the first empty state"), and it was
		// written into the assertion that was supposed to catch it. A table-wide view
		// over an empty table has an answer: an empty collection.
		if got.payload.Valid() {
			t.Errorf("%s: %s: the view refused over an empty table and still handed back "+
				"a payload naming %q", declared.Name, shape.name, got.payload.View())
		}

		if strings.TrimSpace(got.refusal.Error()) == "" {
			t.Errorf("%s: %s: the view refused with an empty message, which has moved the "+
				"diagnosis onto the reader", declared.Name, shape.name)
		}
	default:
		t.Fatalf("%s: %s: Derive refused a shape §14 requires the view to render: %v",
			declared.Name, shape.name, got.refusal)
	}
}

// deriveOrExplain answers one query, failing the test for any refusal that is not the
// one legitimate cell.
func deriveOrExplain(
	t *testing.T,
	one fleet,
	declared rules.View,
	shape viewShape,
	about addressing,
	limit ...int,
) rules.Payload {
	t.Helper()

	bound := 0
	if len(limit) > 0 {
		bound = limit[0]
	}

	got := derive(t, one, declared, shape, about, bound)
	assertTheAnswer(t, declared, shape, about, got)

	return got.payload
}

// deriveBytes answers one query and marshals it, reporting whether it answered.
func deriveBytes(
	t *testing.T,
	one fleet,
	declared rules.View,
	shape viewShape,
	about addressing,
) ([]byte, bool) {
	t.Helper()

	payload, err := one.system.Derive(stateOf(t, shape), queryFor(declared, shape, about, 0))
	if err != nil {
		return nil, false
	}

	encoded, err := json.Marshal(payload.Value())
	if err != nil {
		t.Fatalf("%s: %s: the payload does not marshal: %v", declared.Name, shape.name, err)
	}

	return encoded, true
}

// assertPayloadIsWellFormed is claim 2: the payload names the view the query asked for
// and is a payload at all.
func assertPayloadIsWellFormed(t *testing.T, payload rules.Payload, view string) {
	t.Helper()

	if payload.View() == "" {
		return
	}

	if !payload.Valid() {
		t.Errorf("the payload is not one a renderer could be routed with: %+v", payload)
	}

	if payload.View() != view {
		t.Errorf("the payload answers %q and the query asked for %q; `Payload.View` is what "+
			"picks the renderer, so this is a card rendered by the wrong one", payload.View(), view)
	}
}

// assertRenderable is claim 3: the value marshals, and to something a renderer can
// walk.
//
// **Never `null`.** A nil slice marshals to `null`, and `len(null)` in the client is an
// error rather than an empty list — which is why every list-shaped `Derive` in this
// repository builds its slice with `make(…, 0, …)` rather than declaring a nil one. The
// assertion is on the bytes rather than on the Go value so it holds for a payload whose
// type this package cannot name: `statBlock` and `conditionSummary` are unexported.
func assertRenderable(t *testing.T, payload rules.Payload, view, shape string) {
	t.Helper()

	if payload.View() == "" {
		// The legitimate refusal, already logged by `deriveOrExplain`.
		return
	}

	encoded, err := json.Marshal(payload.Value())
	if err != nil {
		t.Fatalf("%s: %s: the payload does not marshal: %v", view, shape, err)
	}

	trimmed := bytes.TrimSpace(encoded)

	if bytes.Equal(trimmed, []byte("null")) {
		t.Errorf("%s: %s: the payload marshals to `null`, which is what a nil value "+
			"marshals to; a renderer reading that gets a length error rather than an "+
			"empty list", view, shape)
	}

	if !bytes.HasPrefix(trimmed, []byte("{")) && !bytes.HasPrefix(trimmed, []byte("[")) {
		t.Errorf("%s: %s: the payload marshals to %s, which is neither an object nor an "+
			"array; a renderer has nothing to walk", view, shape, trimmed)
	}
}

// assertTheObjectIsNamed is claim 4: emptiness does not mean namelessness.
//
// **Only for a per-object view over a tabletop with an object in it.** A card whose
// identity field is empty renders as a heading with nothing in it, and that is the
// failure "empty output" invites: a system that omits the fields it has nothing for and
// forgets the one that identifies what the card is about.
func assertTheObjectIsNamed(
	t *testing.T,
	payload rules.Payload,
	shape viewShape,
	view string,
	about addressing,
) {
	t.Helper()

	// **A table-wide view is exempt**, and the reason is that it is answering a
	// different question: `condition-reference` lists what the *pack* declares and
	// `condition-index` counts what the *table* carries, so neither has an object to
	// name and requiring one of them to name `max_0` would be requiring a feature
	// rather than holding a claim. The addressing is probed rather than declared (see
	// `probeAddressing`), so this is the probe's answer being used.
	if about != aboutOneObject || payload.View() == "" || len(shape.objects) == 0 {
		return
	}

	encoded, err := json.Marshal(payload.Value())
	if err != nil {
		t.Fatalf("%s: %s: the payload does not marshal: %v", view, shape.name, err)
	}

	if !bytes.Contains(encoded, []byte(strconv.Quote(shape.objects[0].ID.String()))) {
		t.Errorf(
			"%s: %s: the payload does not mention the game object it is about (%q), so an "+
				"empty output renders as a card about nothing rather than a card with "+
				"nothing on it",
			view, shape.name, shape.objects[0].ID,
		)
	}
}

// assertDecodesIntoARendererThatKnowsNothing is §14's "ignored, not fatal", stated the
// way a renderer would state it.
//
// **Decoded per element for an array**, which is the only shape of the claim that needs
// saying: `json.Unmarshal` into a bare `struct{}` fails on an array for a reason that
// has nothing to do with unknown fields (an array is not an object), and a first
// version of this assertion reported that as a §14 failure. A list renderer is a
// renderer that knows none of its rows' fields, so the rows are what get decoded.
func assertDecodesIntoARendererThatKnowsNothing(t *testing.T, encoded []byte, view, shape string) {
	t.Helper()

	trimmed := bytes.TrimSpace(encoded)

	switch {
	case bytes.HasPrefix(trimmed, []byte("[")):
		var rows []struct{}

		if err := json.Unmarshal(trimmed, &rows); err != nil {
			t.Errorf("%s: %s: the payload does not decode into a renderer that declares no "+
				"fields on its rows (%v), so adding a field to it would be a breaking "+
				"change for every renderer at once", view, shape, err)
		}
	case bytes.HasPrefix(trimmed, []byte("{")):
		if err := json.Unmarshal(trimmed, &struct{}{}); err != nil {
			t.Errorf("%s: %s: the payload does not decode into a renderer that declares no "+
				"fields of its own (%v), so adding a field to it would be a breaking "+
				"change for every renderer at once", view, shape, err)
		}

		// And into a map, which is the shape a generic renderer walks: the keys have to
		// be there, not merely tolerated.
		var asMap map[string]any

		if err := json.Unmarshal(trimmed, &asMap); err != nil {
			t.Errorf("%s: %s: the payload does not decode into a record: %v", view, shape, err)
		} else if len(asMap) == 0 {
			t.Errorf("%s: %s: the payload decodes to an object with no fields at all, "+
				"which is the blank document `View.Check`'s comment refuses to let a "+
				"renderer produce", view, shape)
		}
	default:
		t.Errorf("%s: %s: the payload is neither an object nor an array, so there is no "+
			"form for a renderer to read", view, shape)
	}
}

// assertKeysAreWireShaped is the half of the forward-compatibility claim that is about
// spelling rather than about tolerance.
//
// **Lower_snake_case, and every key of every object.** The payloads' JSON tags are the
// wire with the UI tier — `dnd5e`'s `view.go` says "renaming one is a breaking change
// there" — and a renderer reading `hitPoints` where it expects `hit_points` shows an
// empty field with nothing saying why. The check is over the decoded keys rather than
// over the marshalled bytes so it reaches the fields of nested records too, which is
// where a tag actually drifts: the outer struct is written once and the inner ones are
// written per record.
func assertKeysAreWireShaped(t *testing.T, encoded []byte, view, shape string) {
	t.Helper()

	trimmed := bytes.TrimSpace(encoded)

	switch {
	case bytes.HasPrefix(trimmed, []byte("[")):
		var rows []map[string]any

		if err := json.Unmarshal(trimmed, &rows); err != nil {
			t.Errorf("%s: %s: the payload is an array that does not decode into records: %v",
				view, shape, err)

			return
		}

		for idx, row := range rows {
			assertKeysSnake(t, keysOf(row), fmt.Sprintf("%s: %s: row %d", view, shape, idx))
		}
	case bytes.HasPrefix(trimmed, []byte("{")):
		var record map[string]any

		if err := json.Unmarshal(trimmed, &record); err != nil {
			t.Errorf("%s: %s: the payload is an object that does not decode into a record: %v",
				view, shape, err)

			return
		}

		assertKeysSnake(t, keysOf(record), view+": "+shape)

		walkNested(t, record, view+": "+shape)
	default:
		t.Errorf("%s: %s: the payload is neither an object nor an array, so there are no "+
			"keys to read", view, shape)
	}
}

// assertKeysSnake is the predicate, over one record's keys.
func assertKeysSnake(t *testing.T, keys []string, where string) {
	t.Helper()

	for _, key := range keys {
		if !wireShaped.MatchString(key) {
			t.Errorf("%s: the field %q is not lower_snake_case; the payloads' JSON tags are "+
				"the wire with the UI tier, and a tag that drifted renders as an empty "+
				"field on every card", where, key)
		}
	}
}

// walkNested checks the keys of every object one level down, which is where a nested
// record's own tags live.
func walkNested(t *testing.T, record map[string]any, where string) {
	t.Helper()

	for _, key := range keysOf(record) {
		nested, isObject := record[key].(map[string]any)
		if !isObject {
			continue
		}

		assertKeysSnake(t, keysOf(nested), where+": "+key)
	}
}

// wireShaped is what a payload's field names look like.
//
// **Compiled once and used everywhere**, and `[a-z0-9_]` rather than a stricter pattern
// so that the assertion is about the *case and the separators* and not about a naming
// style somebody tightens later: a payload's tag is a wire name, and the two facts that
// break a renderer are a field arriving under a name it does not expect and a field
// arriving under a name it cannot spell.
var wireShaped = regexp.MustCompile(`^[a-z0-9_]+$`)

func keysOf(record map[string]any) []string {
	return slices.Sorted(maps.Keys(record))
}

// isCollection reports whether a payload's value is a slice or an array, which is what
// makes `Query.Limit` meaningful for it.
func isCollection(value any) bool {
	if value == nil {
		return false
	}

	return reflect.ValueOf(value).Kind() == reflect.Slice
}

// collectionLength reports how many entries a collection payload carries.
func collectionLength(t *testing.T, payload rules.Payload) int {
	t.Helper()

	encoded, err := json.Marshal(payload.Value())
	if err != nil {
		t.Fatalf("the payload does not marshal: %v", err)
	}

	if bytes.HasPrefix(bytes.TrimSpace(encoded), []byte("[")) {
		var rows []json.RawMessage

		if err := json.Unmarshal(encoded, &rows); err != nil {
			t.Fatalf("the payload is an array that does not decode: %v", err)
		}

		return len(rows)
	}

	return 1
}

// gmRole is the GM, named once because two helpers need it and `domain.Role` is a type
// this file would otherwise import for one constant.
func gmRole() domain.Role { return domain.RoleGM }
