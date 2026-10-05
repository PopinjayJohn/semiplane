// P12 H6's corpus: the (state, intent, seed) triples the determinism property is
// asserted over, and the systems it is asserted against.
//
// **A separate file from the claim it serves**, because the claim is one function and
// the corpus is the thing that makes it a property. Keeping them apart is what lets a
// reader answer "how many shapes, and which?" without reading the property, and it is
// what lets the corpus grow for a claim nobody has noticed yet.
//
// # What a corpus is for
//
// A resolver that ranges over a map disagrees with itself **only when there is
// something to disagree about**. A tabletop with one object and a resolution with one
// mutation has one answer; a tabletop with three objects carrying three conditions
// between them has as many orderings as the map has keys. Every knob below is
// therefore a knob that *widens a shape*, and the axes are:
//
//   - the **operation**, because each one reaches a different part of the resolver:
//     `attack` draws two dice and writes two creatures, `apply_condition` prunes and
//     writes one, `apply_status` is the GM's adjudication and writes one,
//     `heal` clamps, `roll` draws once.
//   - the **width** of the tabletop, because a map walk over one key has one order.
//   - the **conditions in force**, because `creature.pruneConditions` rebuilds a list
//     out of the pack's rows and the rebuilt list is what a mutation's bytes carry.
//   - the **seed**, because the drawn dice are the other half of the payload.
//
// # How it is generated
//
// `rand.New(rand.NewPCG(...))` with fixed seeds — never the package-level source, and
// never the wall clock. That is not only S-10.4's rule (which governs rule code, not
// tests): a corpus that differed between runs would make a failure unreproducible,
// which is the same defect the property under test is about. Fixed PCG seeds make the
// corpus itself a function of this source file, so a failure names the same scenario
// on a colleague's machine as on this one.
//
// **`forbidigo` covers this directory including its tests**, which is why the
// construction is spelled out rather than reached for by habit: `rand.New` and
// `rand.NewPCG` are the two names `.golangci.yml` deliberately leaves off the
// `rand` alternation, because `rules.Context.Rand` is built from them and a
// determinism rule that fires on the one correct implementation of randomness is a
// rule the next contributor switches off.

package rules_test

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e/overlays"
	"github.com/semiplane/semiplane/internal/domain/systems/notfive"
)

// scenario is one (state, intent, seed) triple, plus the identity the resolution is
// performed as.
//
// **The campaign and actor are fields rather than constants** because they are part of
// what a resolver is handed and a corpus that varies them costs nothing: a resolver
// that reached for `call.Actor` in its answer would be reproducible and wrong, and
// varying the actor is the only way to see that. `rules_test.go`'s
// `TestIdentityIsCarriedButDoesNotChangeTheResultAndTheSeedDoes` holds the claim for
// one stub; this varies it across every shipped system.
type scenario struct {
	// name is what a failure reports, and it is the *shape* rather than an index.
	name string

	// objects is the tabletop, rebuilt into a fresh `State` before every run.
	objects []rules.Object

	// intent is the operation to resolve.
	intent rules.Intent

	// seed is the entropy the resolution draws from.
	seed rules.Seed

	// campaign and actor are the identity fields, varied across the corpus.
	campaign int64
	actor    int64

	// role is `gm` or `player`, varied so a resolver's role branch is exercised.
	role domain.Role
}

// fleet is one system and the scenarios it is certified against.
//
// A system rather than a package: the contract is `rules.System`, and §14's row is
// about a system. `notfive` is in the fleet for the reason it exists at all — it shares
// nothing with 5e, so a determinism failure that only 5e could produce would show up as
// a gap in the corpus rather than as a passing test.
type fleet struct {
	// name is the system id plus anything that distinguishes two fleets of one system
	// (the 2024 overlay is a different ruleset from the base pack alone).
	name string

	// system is the system under certification.
	system rules.System

	// scenarios is the corpus.
	scenarios []scenario

	// vocab builds game objects in this system's own encoding, which is what the view
	// tests need and what a corpus cannot supply: a corpus varies content, a view test
	// needs to *choose* how much of it there is.
	vocab vocabulary

	// gmOnly are the operations §7.2 reserves to the GM, which `conformance.New`
	// requires and which only the system knows.
	gmOnly []rules.Op
}

// vocabulary builds game objects in one system's own encoding, at three levels of
// content.
//
// **Three levels and not one, because "empty" is a claim about content rather than
// about a state being nil.** §14 asks every declared view to render for "an empty, a
// typical and a maximal `Derive` output", and the three words describe what the
// *system* found: a token with no conditions and no attacks, a token with the content
// a table normally has, and a token carrying everything the card can show. A view
// tested against a nil state and a view tested against a token with nothing on it are
// different tests, and the second is the one a GM meets on an empty campaign.
//
// It exists as an interface rather than as three functions per system because the view
// table walks *systems*, not fixtures: a test that asked each system for its three
// shapes by name would be a list somebody has to edit when a fourth system lands.
type vocabulary interface {
	// minimal returns one game object of this system's kind carrying as little as its
	// own encoding permits — and **still naming itself**, because a card with no name
	// is not an empty card, it is a card about nothing.
	minimal(t *testing.T) rules.Object

	// typical returns `count` objects with the content a table normally has.
	typical(t *testing.T, count int) []rules.Object

	// maximal returns `count` objects carrying everything the card can show: every
	// condition column a card has a line for, every attack, a stored roll.
	maximal(t *testing.T, count int) []rules.Object
}

// shippedFleets is every `rules.System` this build registers.
//
// **Derived from the same constructors the composition root uses** — `overlays.ByID`
// for the editions and `notfive.New` for the loom — rather than from a hand-written
// list of names. The composition root's `cmd/server/systems.go` is the one place that
// decides what this binary resolves under, and a fleet list that had to be edited
// alongside it is a second list: the shape `AGENTS.md` calls out for `cmd/server/systems.go`
// and `kindRegistry`.
//
// The order is the registration order: base pack alone first (the standalone shape
// §10.4 makes first-class), then each edition, then the system that shares nothing
// with 5e.
func shippedFleets(t *testing.T) []fleet {
	t.Helper()

	standalone, err := dnd5e.New(dnd5e.Options{})
	if err != nil {
		t.Fatalf("building 5e over its base pack: %v", err)
	}

	editions := overlays.IDs()

	fleets := make([]fleet, 0, len(editions)+2)
	fleets = append(fleets, fleet{
		name:      string(standalone.ID()) + " base pack",
		system:    standalone,
		scenarios: fiveeCorpus(t, standalone),
		vocab:     fiveeVocab{slugs: declaredConditionSlugs(t, standalone)},
		gmOnly:    dnd5e.GMOnlyOps(),
	})

	for _, id := range editions {
		edition, err := overlays.ByID(id)
		if err != nil {
			t.Fatalf("loading the %s edition: %v", id, err)
		}

		engine, err := edition.System()
		if err != nil {
			t.Fatalf("building 5e for the %s edition: %v", id, err)
		}

		fleets = append(fleets, fleet{
			name:      string(engine.ID()) + " " + string(id),
			system:    engine,
			scenarios: fiveeCorpus(t, engine),
			vocab:     fiveeVocab{slugs: declaredConditionSlugs(t, engine)},
			gmOnly:    dnd5e.GMOnlyOps(),
		})
	}

	loom := notfive.New()

	fleets = append(fleets, fleet{
		name:      string(loom.ID()),
		system:    loom,
		scenarios: loomCorpus(t),
		vocab:     loomVocab{},
		gmOnly:    notfive.LoomGMOnlyOps(),
	})

	return fleets
}

// # The vocabularies

// fiveeVocab builds 5e tokens at three levels of content.
//
// **A value rather than a pointer, carrying the pack's condition slugs**, because the
// maximal shape is only maximal if the conditions it lists are ones the pack declares —
// a card showing `poisoned` on a pack that removed it is a card about a condition that
// does not exist, and the resolver would have dropped it from the creature on the way
// in.
type fiveeVocab struct {
	// slugs are the condition slugs this build's pack declares.
	slugs []string
}

var _ vocabulary = fiveeVocab{}

// minimal is a creature with a name, a level and nothing else: no abilities, no hit
// points, no attacks, no conditions, no roll.
//
// **The name and the level are the whole point of the shape.** A card whose `name` is
// empty renders as a heading with nothing in it, and the view test's claim is that an
// empty output still renders — so the fixture has to be *empty of content* rather than
// empty of identity. `notfive`'s `Derive` comment says the same thing about a spool
// with no thread: "a spool with no thread and no dye is a spool, not a missing answer".
//
// **The level is there because of a finding, and it is worth stating rather than
// quietly working around.** The pack's proficiency table "covers levels 1-4, 5-8, … and
// not 0", so a token whose body carries no level cannot produce a stat block at all —
// while `readCreature` accepts such a body and `rules.NewState` accepts it as an
// object. `TestTheStatBlockForATokenWhoseBodyCarriesNoLevelIsARefusal` pins that
// behaviour and says plainly that this file does not endorse it; until somebody
// decides, the "empty" shape here is a *level-1* creature with nothing else on it, so
// that §14's row is asked of shapes a real tabletop can hold.
func (fiveeVocab) minimal(t *testing.T) rules.Object {
	t.Helper()

	return rules.Object{
		ID:   rules.ObjectID("bare_0"),
		Kind: rules.KindToken,
		Data: []byte(`{"name":"Nothing at all","level":1}`),
	}
}

// typical is a creature with the six abilities, hit points, an attack and no
// conditions.
//
// **Level from one, and that is not decoration.** The pack's proficiency table has no
// row for level 0, so a "typical" creature at level 0 cannot produce a stat block and
// the view tests would report a fixture bug as a §14 failure.
func (v fiveeVocab) typical(t *testing.T, count int) []rules.Object {
	t.Helper()

	objects := make([]rules.Object, 0, count)

	for idx := range count {
		objects = append(objects, rules.Object{
			ID:   rules.ObjectID(fmt.Sprintf("typ_%d", idx)),
			Kind: rules.KindToken,
			Data: []byte(v.body(fmt.Sprintf("Ordinary %d", idx), idx+1, false)),
		})
	}

	return objects
}

// maximal is a creature carrying conditions, two attacks, a stored roll and full
// scores — every section of the card `deriveStatBlock` can fill.
//
// **The stored roll is the half worth including**, because it is the one field the card
// renders conditionally (`json:"last_roll,omitempty"`). A view that only ever sees
// creatures nobody has rolled never exercises the branch that shows a roll, which is
// the shape that breaks first when a field becomes a pointer.
func (v fiveeVocab) maximal(t *testing.T, count int) []rules.Object {
	t.Helper()

	objects := make([]rules.Object, 0, count)

	for idx := range count {
		objects = append(objects, rules.Object{
			ID:   rules.ObjectID(fmt.Sprintf("max_%d", idx)),
			Kind: rules.KindToken,
			Data: []byte(v.body(fmt.Sprintf("Everything %d", idx), idx+6, true)),
		})
	}

	return objects
}

// body renders one creature body. `rich` is the flag that decides conditions, the
// second attack and the stored roll.
func (v fiveeVocab) body(name string, level int, rich bool) string {
	conditions := `""`
	secondAttack := ""
	roll := ""

	if rich {
		held := []string{}
		for idx, slug := range v.slugs {
			// Every third slug, so the maximal card carries several and not all
			// fifteen: a card listing every condition in the pack is the *reference*
			// view's job, and a maximal shape that is maximal for the wrong reason tests
			// the wrong thing.
			if idx%3 == 0 && len(held) < 4 {
				held = append(held, strconv.Quote(slug))
			}
		}

		conditions = strings.Join(held, ", ")

		secondAttack = `,
    {
      "name": "shieldbash",
      "ability": "dexterity",
      "damage": "1d6+2",
      "mastery": ["finesse"]
    }`

		roll = `,
  "last_roll": {
    "expr": "1d20+4",
    "label": "Perception",
    "total": 17,
    "natural": 13,
    "parts": [13, 4],
    "target": 15,
    "met": true
  }`
	}

	return fmt.Sprintf(`{
  "name": %q,
  "level": %d,
  "ability": {
    "strength": 18, "dexterity": 14, "constitution": 16,
    "intelligence": 10, "wisdom": 12, "charisma": 8
  },
  "hp": 22, "max_hp": 31, "ac": 0, "ac_bonus": 2, "speed": 30,
  "conditions": [%s],
  "attacks": [
    {
      "name": "greataxe",
      "ability": "strength",
      "damage": "1d12+4",
      "attack_bonus": 3,
      "mastery": ["great_weapon", "two_handed"]
    }%s
  ],
  "masteries": ["finesse", "great_weapon", "two_handed"]%s
}`, name, level, conditions, secondAttack, roll)
}

// loomVocab builds spools at three levels of content.
type loomVocab struct{}

var _ vocabulary = loomVocab{}

// minimal is a spool with no thread and no dye, which `notfive.Derive` calls out
// explicitly as "a legitimate row".
func (loomVocab) minimal(t *testing.T) rules.Object {
	t.Helper()

	return rules.Object{
		ID:   rules.ObjectID("bare_0"),
		Kind: notfive.KindSpool,
		Data: []byte(`{"length":0,"colour":0}`),
	}
}

func (loomVocab) typical(t *testing.T, count int) []rules.Object {
	t.Helper()

	return loomSpools(t, count, 12, 6)
}

func (loomVocab) maximal(t *testing.T, count int) []rules.Object {
	t.Helper()

	// A wider table than 5e's maximal, because a list view's maximal case is the one
	// that decides whether `Limit` is honoured at all.
	return loomSpools(t, count*2, 240, 6)
}

func loomSpools(t *testing.T, count, length, colours int) []rules.Object {
	t.Helper()

	objects := make([]rules.Object, 0, count)

	for idx := range count {
		objects = append(objects, rules.Object{
			ID:   rules.ObjectID(fmt.Sprintf("spool_%d", idx)),
			Kind: notfive.KindSpool,
			Data: []byte(fmt.Sprintf(`{"length":%d,"colour":%d}`,
				length+idx, idx%colours)),
		})
	}

	return objects
}

// fiveeCorpus is the 5e corpus: six operations × several tabletop widths × several
// condition sets × several seeds.
//
// **Generated rather than enumerated, and the seed stream is the reason.** A
// hand-written list of twenty scenarios is twenty shapes somebody chose; a generated
// one is twenty shapes from a space with far more in it, and the space is small enough
// that a failure is still reproducible because the stream is fixed. The two axes that
// matter most — the number of conditions in force and the tabletop's width — are the
// two that decide whether a map walk has anything to reorder, so both are varied
// deliberately rather than incidentally.
func fiveeCorpus(t *testing.T, system rules.System) []scenario {
	t.Helper()

	source := rand.New(rand.NewPCG(0x5EED, 0xC0FFEE))

	// The conditions this build's pack declares, probed rather than assumed. The
	// corpus needs at least two: one condition in force leaves `pruneConditions` with
	// nothing to reorder, and an ordered rebuild with nothing to reorder is the code a
	// map-iteration defect would break.
	conditions := declaredConditionSlugs(t, system)
	if len(conditions) < 2 {
		t.Fatalf("the pack declares %d conditions, so no scenario can put two in force "+
			"and a map walk has nothing to reorder", len(conditions))
	}

	operations := []rules.Op{
		dnd5e.OpAttack,
		dnd5e.OpApplyCondition,
		dnd5e.OpClearCondition,
		dnd5e.OpApplyStatus,
		dnd5e.OpHeal,
		dnd5e.OpRoll,
	}

	scenarios := make([]scenario, 0, len(operations)*corpusPerOperation)

	for _, operation := range operations {
		for nth := range corpusPerOperation {
			// **Width from 1 to 5.** One object is the shape where an order-dependent
			// resolver is indistinguishable from a deterministic one, and five is where a
			// map walk has five orderings to choose from.
			width := nth%fiveMaxObjects + 1

			// One to three conditions. Zero is excluded on purpose: `pruneConditions`
			// returns nil for an empty list, and a resolution with no conditions in force
			// never reaches the ordered rebuild.
			held := 1 + source.IntN(3)

			picked := make([]string, 0, held)
			for range held {
				picked = append(picked, conditions[source.IntN(len(conditions))])
			}

			objects := fiveeTable(t, width, picked, source)

			// The GM for every scenario except the ones that exist to check a player's
			// refusals, which are named rather than generated — see `playerFleet` below.
			call := scenario{
				objects:  objects,
				seed:     drawnSeed(source),
				campaign: 1 + int64(source.IntN(8)),
				actor:    1 + int64(source.IntN(16)),
				role:     domain.RoleGM,
			}

			call.intent = fiveeIntent(t, operation, objects, conditions, source)
			call.name = fmt.Sprintf("%s with %d objects and %d conditions",
				operation, width, held)

			scenarios = append(scenarios, call)
		}
	}

	return scenarios
}

// corpusPerOperation is how many shapes each operation is certified over.
//
// **Three**, and the arithmetic is what makes the corpus a quantifier rather than a
// sample: three shapes per operation over six operations is eighteen, above
// `minimumCorpus`, and each operation's three shapes differ in width and conditions —
// the two axes that decide whether an order-dependent resolver has anything to
// reorder. Two per operation would be twelve, which is the floor, and the floor is
// where a corpus stops varying an axis twice.
const corpusPerOperation = 3

// fiveMaxObjects is the widest tabletop the 5e corpus builds.
//
// Five, not more: the corpus is resolved a hundred times per scenario, so the width is
// a cost as well as a shape, and five is past the point where a map walk over
// single-digit keys has more than one ordering.
const fiveMaxObjects = 5

// candidateConditions is every condition slug the shipped 5e packs have declared,
// used as a *probe* rather than as a list.
//
// **Probed, not trusted, and the reason is that a corpus has to be built from what the
// pack says.** A slug this build's pack does not declare is dropped, because a scenario
// carrying one resolves to `ErrNoSuchCondition` — and a corpus of refusals passes a
// determinism test for the wrong reason, which is the failure mode this whole file is
// shaped to avoid. A pack revision that removed `poisoned` therefore leaves the corpus
// exercising one condition fewer rather than twenty scenarios that quietly stopped
// resolving.
//
// The alternative — reading the slugs out of the pack — is not available from outside
// `dnd5e`: `Pack.Conditions` returns `[]conditionRow` and `conditionRow`'s fields are
// unexported, which is deliberate (`pack.go` is explicit that a client reads a kind and
// not a condition row's innards). So the pack is asked, through its exported lookup,
// which of these slugs it knows.
var candidateConditions = []string{
	"blinded",
	"charmed",
	"deafened",
	"exhausted",
	"frightened",
	"grappled",
	"incapacitated",
	"invisible",
	//nolint:misspell // The pack's own slug, spelled as 5e spells it: `paralyzed` is a
	// wire-level identifier in `data/base.yaml`, and rewriting it here would make the
	// corpus probe a condition no pack declares — a corpus of refusals.
	"paralyzed",
	"petrified",
	"poisoned",
	"prone",
	"restrained",
	"stunned",
	"unconscious",
}

// declaredConditionSlugs returns the condition slugs this build's pack actually
// declares.
//
// **The probe's order rather than the pack's**, and that is a real difference worth
// naming rather than papering over. `creature.pruneConditions` rebuilds a creature's
// list in the *pack's* declaration order, so the order the corpus hands them in is
// deliberately **not** that order: a slug list already in pack order would make
// `pruneConditions` a no-op and the ordered rebuild — the code a map-iteration defect
// would break — would never run. The alphabetical probe gives it work to do.
func declaredConditionSlugs(t *testing.T, system rules.System) []string {
	t.Helper()

	packs, ok := system.(interface{ Pack() *dnd5e.Pack })
	if !ok {
		t.Fatalf("system %q does not expose its pack, so the corpus cannot be built from "+
			"what the pack declares", system.ID())
	}

	pack := packs.Pack()
	if pack == nil {
		t.Fatalf("system %q answered for its pack with nil", system.ID())
	}

	slugs := make([]string, 0, len(candidateConditions))

	for _, candidate := range candidateConditions {
		// The row's type is inferred and discarded: `conditionRow` is unexported and its
		// fields are too, which is exactly why this is a membership probe and not a read.
		if _, known := pack.ConditionAt(candidate); known {
			slugs = append(slugs, candidate)
		}
	}

	if len(slugs) < 2 {
		t.Fatalf("the pack answers for %d of the %d condition slugs this file knows, so no "+
			"scenario can put two in force and a map walk has nothing to reorder",
			len(slugs), len(candidateConditions))
	}

	return slugs
}

// fiveObjectPrefix is the object-id prefix the 5e corpus uses.
//
// **Distinct per scenario would be nicer and is not done on purpose.** The ids repeat
// across scenarios so a failure reads "run 47 of `attack` with 3 objects" rather than
// carrying a second identifier; and reusing them is safe because each scenario builds
// its own `State` from its own objects, so a resolver carrying state between
// resolutions would be caught within the first scenario rather than across two.
const fiveObjectPrefix = "tok"

// fiveeTable builds a tabletop of tokens, each carrying a creature body.
//
// **Creature bodies are generated, and the attack list is the same for every token**,
// because an attack names one of the attacker's declared attacks by name and a token
// with no attack would make every `attack` scenario a refusal — which is how a corpus
// silently stops exercising the resolution path it exists to exercise. The
// `resolving == 0` guard in the claim's own test is what catches that, and it is in the
// claim rather than here because a corpus cannot report on itself.
//
// Every token is placed for `actor: 0`, and **every scenario resolves as the GM**: a
// player acting for a token that belongs to nobody is refused by `actingCreature`,
// correctly and for a reason that has nothing to do with determinism. The corpus does
// vary the actor across scenarios, so a resolver reaching for `call.Actor` in its
// answer would be visible — it just does not use that to make a scenario
// unresolvable.
func fiveeTable(t *testing.T, width int, conditions []string, source *rand.Rand) []rules.Object {
	t.Helper()

	objects := make([]rules.Object, 0, width)

	for idx := range width {
		body := fmt.Sprintf(`{
  "name": "Token %d",
  "actor": 0,
  "level": %d,
  "ability": {
    "strength": %d, "dexterity": %d, "constitution": %d,
    "intelligence": %d, "wisdom": %d, "charisma": %d
  },
  "hp": %d, "max_hp": %d, "ac": 0, "ac_bonus": %d, "speed": %d,
  "conditions": [%s],
  "attacks": [
    {
      "name": "greataxe",
      "ability": "strength",
      "damage": "1d12+4",
      "attack_bonus": 0,
      "mastery": ["great_weapon", "two_handed"]
    }
  ],
  "masteries": ["finesse", "great_weapon", "two_handed"]
}`,
			idx,
			1+source.IntN(12),
			6+source.IntN(18), 6+source.IntN(18), 6+source.IntN(18),
			6+source.IntN(18), 6+source.IntN(18), 6+source.IntN(18),
			1+source.IntN(30), 31+source.IntN(30), source.IntN(3), 20+source.IntN(20),
			quotedConditions(conditions),
		)

		objects = append(objects, rules.Object{
			ID:   rules.ObjectID(fiveObjectPrefix + strconv.Itoa(idx)),
			Kind: rules.KindToken,
			Data: []byte(body),
		})
	}

	return objects
}

// quotedConditions renders a condition list as JSON, de-duplicated and in the order
// given.
//
// **De-duplicated because `apply_condition` refuses nothing but `slices.Contains`
// guards the append**, so a list with a repeat is a different shape rather than the
// same one twice — and the point of the corpus is shapes, not repeats.
func quotedConditions(conditions []string) string {
	quoted := make([]string, 0, len(conditions))

	seen := make(map[string]struct{}, len(conditions))

	for _, condition := range conditions {
		if _, already := seen[condition]; already {
			continue
		}

		seen[condition] = struct{}{}

		quoted = append(quoted, strconv.Quote(condition))
	}

	return strings.Join(quoted, ", ")
}

// fiveeIntent builds the intent for one operation over a tabletop.
//
// **Every arm names a real target from the tabletop it is given**, rather than a
// hardcoded id: a corpus whose `attack` scenario names a defender that is not there is
// a corpus of refusals, and a refusal corpus passes a determinism test for the wrong
// reason. `TestTheCorpusActuallyResolvesSomething` is the guard that catches it, and
// it exists because this function is where that mistake would be made.
func fiveeIntent(
	t *testing.T,
	operation rules.Op,
	objects []rules.Object,
	conditions []string,
	source *rand.Rand,
) rules.Intent {
	t.Helper()

	target := objects[source.IntN(len(objects))].ID

	var args []byte

	switch operation {
	case dnd5e.OpAttack:
		defender := objects[source.IntN(len(objects))].ID
		args = []byte(`{"attack":"greataxe","defender":"` + defender.String() + `"}`)
	case dnd5e.OpApplyCondition, dnd5e.OpClearCondition:
		args = []byte(`{"condition":"` + conditions[source.IntN(len(conditions))] + `"}`)
	case dnd5e.OpApplyStatus:
		args = []byte(`{"hp":` + strconv.Itoa(1+source.IntN(30)) +
			`,"condition":"` + conditions[source.IntN(len(conditions))] + `"}`)
	case dnd5e.OpHeal:
		args = []byte(`{"amount":` + strconv.Itoa(source.IntN(20)) + `}`)
	case dnd5e.OpRoll:
		args = []byte(`{"expr":"1d20+` + strconv.Itoa(source.IntN(8)) + `","dc":15}`)
	case dnd5e.OpRemoveToken:
		args = nil
	default:
		t.Fatalf("the corpus has no intent for %q", operation)
	}

	intent, err := rules.NewIntent(operation, target, args)
	if err != nil {
		t.Fatalf("building a %q intent for the corpus: %v", operation, err)
	}

	return intent
}

// loomCorpus is the counterexample's corpus.
//
// **The same shape as the 5e one, built the other way round**, and that is the point:
// notfive shares nothing with 5e, so a corpus written for it catches the class of
// failure that only a system with a different vocabulary could produce. The two axes
// are the tabletop's width and the seed, because the loom's draw is `Op` plus target
// (`call.Rand(intent.Op.String() + "/" + intent.Target.String())`) and there is no
// other input to vary.
func loomCorpus(t *testing.T) []scenario {
	t.Helper()

	source := rand.New(rand.NewPCG(0x100D, 0x5EED))

	operations := []rules.Op{
		notfive.OpSpin,
		notfive.OpWeave,
		notfive.OpDye,
		notfive.OpUnwind,
		notfive.OpRecut,
	}

	scenarios := make([]scenario, 0, len(operations)*corpusPerOperation)

	for _, operation := range operations {
		for nth := range corpusPerOperation {
			width := nth%fiveMaxObjects + 1
			objects := loomTable(t, width, source)

			intent, err := rules.NewIntent(
				operation, objects[source.IntN(len(objects))].ID, nil,
			)
			if err != nil {
				t.Fatalf("building a %q intent for the loom corpus: %v", operation, err)
			}

			scenarios = append(scenarios, scenario{
				name:     fmt.Sprintf("%s over %d spools", operation, width),
				objects:  objects,
				intent:   intent,
				seed:     drawnSeed(source),
				campaign: 1 + int64(source.IntN(8)),
				actor:    1 + int64(source.IntN(16)),
				role:     domain.RoleGM,
			})
		}
	}

	return scenarios
}

// loomTable builds a tabletop of spools.
//
// **Lengths and colours spread across the ranges the system uses**, including zero:
// the loom clamps a negative length to zero and `colourName` indexes a six-entry
// palette, so a corpus of non-zero middles would never reach the clamp.
func loomTable(t *testing.T, width int, source *rand.Rand) []rules.Object {
	t.Helper()

	objects := make([]rules.Object, 0, width)

	for idx := range width {
		body := `{"length":` + strconv.Itoa(source.IntN(20)) +
			`,"colour":` + strconv.Itoa(source.IntN(6)) + `}`

		objects = append(objects, rules.Object{
			ID:   rules.ObjectID("spool_" + strconv.Itoa(idx)),
			Kind: notfive.KindSpool,
			Data: []byte(body),
		})
	}

	return objects
}

// drawnSeed fills a seed from the corpus's own source.
//
// **All `SeedLen` bytes, and not just the first three.** The seed is 32 bytes because
// `Context.Rand` hashes it whole; a corpus that set three and left twenty-nine at zero
// would still produce varying draws, and the twenty-nine zero bytes would be a
// systematic sameness across every scenario in the corpus — which is the sort of thing
// that makes a passing test mean less than it appears to.
func drawnSeed(source *rand.Rand) rules.Seed {
	var seed rules.Seed

	for idx := range seed {
		seed[idx] = byte(source.UintN(256))
	}

	return seed
}
