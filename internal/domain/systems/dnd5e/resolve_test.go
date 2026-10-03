package dnd5e //nolint:testpackage // see the note below.
// This file asserts on what the package holds internally, and the internal form is
// the assertion: a parsed expression's dice and modifiers, a pack's compiled rows, the
// effect vocabulary's closed set, the merge by slug. Every one of those is
// unexported on purpose — a client validates against `Grammar` and `Parse`, never
// against `Expr`'s fields — so moving this file out of the package would mean
// exporting internals for a test's benefit, which is the opposite of what the
// boundary is for. The externally reachable behaviour is certified separately in
// `dnd5e_test.go`, through the `rules.System` interface.
import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
)

// # What these tests are for
//
// `conformance` proves the engine obeys the contract and `notation_test.go` proves it
// parses what it should. These prove it **resolves the rules it claims to** — and the
// claim is §10.4's split, so the tests are shaped around where each rule's data comes
// from: a rule that a pack author could reasonably expect an overlay to change is
// tested *through an overlay*, so that a later work item adding one finds the seam works
// rather than finding it does nothing.
//
// Every assertion here is an **identity over a draw the test controls**, not a
// distribution. A test that said "damage is usually more on a critical" would pass half
// the time and be worse than none, so where a roll's value matters the test finds a seed
// that produces the case and then computes the expected number from the pack.

// engineWithOverlay builds a system over the base pack with one overlay applied.
//
// **Through `New` and not by merging by hand**, because `Overlay.apply` is unexported
// deliberately: merging is this package's job, and a test that reached around it would be
// testing a path no caller has. It also means every test here exercises the real seam a
// future overlay work item will use.
func engineWithOverlay(t *testing.T, yaml string) *Engine {
	t.Helper()

	overlay, err := ParseOverlay([]byte(yaml))
	if err != nil {
		t.Fatalf("compiling the overlay: %v", err)
	}

	engine, err := New(Options{Overlay: overlay})
	if err != nil {
		t.Fatalf("building the system with the overlay: %v", err)
	}

	return engine
}

// engineWithDie builds a system whose notation admits a d2, for the tests that need an
// exhaustive claim about a die's maximum.
func engineWithDie(t *testing.T) *Engine {
	t.Helper()

	return engineWithOverlay(t, `system: "dnd5e"
version: "test-dice@1"
title: "test dice"
dice:
  sizes: [2, 4, 6, 8, 10, 12, 20, 100]
`)
}

// aToken builds a token body for a resolution, from the parts the assertions care about.
//
// **Assembled rather than pasted**, because every literal JSON body in a table of cases
// is a body somebody has to keep in step with the encoding, and a change to a field name
// would leave twenty tests failing with twenty unrelated messages.
func aToken(
	t *testing.T,
	name string,
	level int,
	abilities map[string]int,
	body map[string]any,
) []byte {
	t.Helper()

	if abilities == nil {
		abilities = map[string]int{}
	}

	full := map[string]any{
		"name": name, "level": level, "ability": abilities,
		"hp": 30, "max_hp": 30, "ac": 12, "speed": 30,
		"attacks": []any{}, "masteries": []string{}, "conditions": []string{},
	}

	maps.Copy(full, body)

	encoded, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("encoding the token %q: %v", name, err)
	}

	return encoded
}

// resolve applies one intent and returns the mutations, failing the test on a refusal.
func resolve(
	t *testing.T,
	engine *Engine,
	call rules.Context,
	state rules.State,
	intent rules.Intent,
) []rules.Mutation {
	t.Helper()

	mutations, err := engine.Apply(t.Context(), call, state, intent)
	if err != nil {
		t.Fatalf("Apply %s: %v", intent.Op, err)
	}

	return mutations
}

// aTestToken is the standard attacker: level 5, Strength 18, a greataxe granting
// `great_weapon` and `two_handed`, and the masteries to hold both.
func aTestToken(t *testing.T) []byte {
	t.Helper()

	return aToken(t, "Vurg", 5,
		map[string]int{"strength": 18, "dexterity": 12, "constitution": 16},
		map[string]any{
			"attacks": []any{map[string]any{
				"name": "greataxe", "ability": "strength", "damage": "1d12+4",
				"mastery": []string{"great_weapon", "two_handed"},
			}},
			"masteries": []string{"great_weapon", "two_handed", "finesse"},
			"ac":        0,
		})
}

// aTestCall is a GM's context with a stated seed.
func aTestCall(t *testing.T, seed ...byte) rules.Context {
	t.Helper()

	first := byte(3)
	if len(seed) > 0 {
		first = seed[0]
	}

	call, err := rules.NewContext(42, 7, domain.RoleGM, rules.Seed{first, first, first})
	if err != nil {
		t.Fatalf("building a rule context: %v", err)
	}

	return call
}

// aState builds a state from a map of token bodies, **plus a defender** so that an
// attack has something to aim at.
func aState(t *testing.T, tokens map[rules.ObjectID][]byte) rules.State {
	t.Helper()

	objects := make([]rules.Object, 0, len(tokens)+1)

	for id, body := range tokens {
		objects = append(objects, rules.Object{ID: id, Kind: rules.KindToken, Data: body})
	}

	if _, given := tokens["defender_1"]; !given {
		// **Armour class 1**, so every attack lands. These tests are about what a
		// resolution does on a *hit*, and an armour class of 12 would make a fifth of
		// every loop a miss — which reads as a flaky fixture and is really a question
		// about the attack roll that `TestAnAttackLandsWhenTheRollBeatsArmourClass`
		// answers once, deliberately.
		objects = append(objects, rules.Object{
			ID:   "defender_1",
			Kind: rules.KindToken,
			Data: aToken(t, "Target", 3, map[string]int{"dexterity": 14},
				map[string]any{"ac": 1, "hp": 100, "max_hp": 100}),
		})
	}

	state, err := rules.NewState(1, objects)
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	return state
}

// attackOn builds an `attack` intent against `defender_1`.
func attackOn(t *testing.T, attacker rules.ObjectID, attack string) rules.Intent {
	t.Helper()

	intent, err := rules.NewIntent(OpAttack, attacker,
		[]byte(`{"attack":"`+attack+`","defender":"defender_1"}`))
	if err != nil {
		t.Fatalf("building the attack intent: %v", err)
	}

	return intent
}

// TestTwoDrawsInOneResolutionAreUnrelated is the labelled-draw design's load-bearing
// claim, stated in the form the numbers can actually decide.
//
// `Context.Rand` is pure in (seed, label), so **a label that is a constant rather than a
// function of what the draw is makes two different questions share one stream.** The
// consequence is not visible inside a single resolution — the second draw simply
// continues the first one's sequence and both numbers stay plausible — but it is visible
// *across* two: two attacks by the same creature under one seed would answer
// identically, which is a table where every swing of the same weapon rolls the same
// number.
//
// So the assertion is over **two weapons, one seed, four hundred repetitions**, and the
// threshold is arithmetic rather than taste: two *independent* d20 streams agree about
// one seed in twenty, so four hundred seeds agree about twenty times with a standard
// deviation of four — **fifty is more than seven standard deviations out**, and a
// constant label agrees four hundred times. The version of this test that compared the
// two draws *inside* one resolution was tried first and does not work — a d2 damage die
// makes agreement impossible by construction and so cannot distinguish a shared stream
// from a distinct one, which is the whole reason the claim is made across resolutions
// instead.
func TestTwoDrawsInOneResolutionAreUnrelated(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	// One creature, two weapons, the same defender and the same seed throughout: the only
	// thing that varies is which declared attack the intent names.
	twoWeapons := aToken(t, "Vurg", 5,
		map[string]int{"strength": 18, "dexterity": 12},
		map[string]any{
			"attacks": []any{
				map[string]any{
					"name": "greataxe", "ability": "strength", "damage": "1d12+4",
				},
				map[string]any{
					"name": "spear", "ability": "strength", "damage": "1d12+4",
				},
			},
			"ac": 0,
		})

	// A defender that cannot be hurt to death and cannot be missed, so every seed
	// resolves a hit and every seed's attack die is recorded.
	defender := aToken(t, "Wall", 1, map[string]int{"dexterity": 10},
		map[string]any{"ac": 1, "hp": 1000, "max_hp": 1000})

	agreed := 0

	for seed := range 400 {
		state := aState(t, map[rules.ObjectID][]byte{
			"attacker_1": twoWeapons, "defender_1": defender,
		})

		first := firstRoll(t, resolve(t, engine, aTestCall(t, byte(seed)), state,
			attackOn(t, "attacker_1", "greataxe")))
		second := firstRoll(t, resolve(t, engine, aTestCall(t, byte(seed)), state,
			attackOn(t, "attacker_1", "spear")))

		if first == second {
			agreed++
		}
	}

	if agreed > 50 {
		t.Errorf("%d of 400 seeds rolled the same attack die for two different weapons; a "+
			"draw's label must be a function of what the draw is, and two answers sharing "+
			"one stream is the failure that looks like coincidence", agreed)
	}
}

// TestTheCriticalRuleIsReadFromThePacksTogglesAndNotFromGo is §10.4's split, tested
// through the mechanism it claims: **one rule, two editions, differing by a boolean**.
//
// The 2014 rule crits on a natural 20 from the attack die; 2024 also crits when a
// damage die shows its own maximum. Two packs, the *same draws*, and the opposite
// answers — which is only possible if the policy is in the pack and the procedure is
// shared. A second Go rule would pass a test that varied the seed instead, and would
// fail this one.
//
// The seeds are **searched rather than hardcoded**: a d2 damage die reaches its maximum
// half the time and the attack die reaches 20 one time in twenty, so a fixed seed would
// be a golden value that changes whenever the pack's numbers do.
func TestTheCriticalRuleIsReadFromThePacksTogglesAndNotFromGo(t *testing.T) {
	t.Parallel()

	// A d2 damage die so "shows its maximum" is common, and `hit_damage: "1d2"` so
	// nothing else varies.
	token := aToken(t, "Vurg", 5,
		map[string]int{"strength": 18, "dexterity": 12},
		map[string]any{
			"attacks": []any{map[string]any{
				"name": "spear", "ability": "strength", "damage": "1d2",
			}},
			"ac": 0,
		})

	state := aState(t, map[rules.ObjectID][]byte{"attacker_1": token})

	// 2024: both toggles on.
	edition2024 := engineWithOverlay(t, `system: "dnd5e"
version: "test-2024@1"
title: "2024"
dice:
  sizes: [2, 4, 6, 8, 10, 12, 20, 100]
toggles:
  crit_attack_die_max: true
  crit_damage_die_max: true
`)

	// 2014: the damage half off. Nothing else differs.
	edition2014 := engineWithOverlay(t, `system: "dnd5e"
version: "test-2014@1"
title: "2014"
dice:
  sizes: [2, 4, 6, 8, 10, 12, 20, 100]
toggles:
  crit_attack_die_max: true
  crit_damage_die_max: false
`)

	// A third edition that turns **both** rules off, which is the direction the attack-die
	// toggle moves. Without it a resolver that ignored `crit_attack_die_max` entirely
	// would pass the 2014-versus-2024 comparison, because both of those packs set it.
	noCrits := engineWithOverlay(t, `system: "dnd5e"
version: "test-nocrits@1"
title: "no crits"
dice:
  sizes: [2, 4, 6, 8, 10, 12, 20, 100]
toggles:
  crit_attack_die_max: false
  crit_damage_die_max: false
`)

	if !edition2024.Pack().Toggle(toggleCritDamageDieMax) {
		t.Fatal("the 2024 overlay did not switch the damage-die rule on; the fixture is wrong")
	}

	if edition2014.Pack().Toggle(toggleCritDamageDieMax) {
		t.Fatal("the 2014 overlay left the damage-die rule on; the fixture is not a 2014 pack")
	}

	// Find a seed where the attack die is **not** a natural 20 and the damage die shows
	// its maximum. Under 2024 that is a critical; under 2014 it is not.
	compared, differing := 0, 0

	for seed := byte(1); seed < 60; seed++ {
		call := aTestCall(t, seed)

		modern := resolve(t, edition2024, call, state, attackOn(t, "attacker_1", "spear"))
		legacy := resolve(t, edition2014, call, state, attackOn(t, "attacker_1", "spear"))

		if len(modern) < 1 || len(legacy) < 1 {
			t.Fatalf("seed %d: an attack resolved to %d and %d mutations",
				seed, len(modern), len(legacy))
		}

		compared++

		modernCritical := isCritical(t, modern[0])
		legacyCritical := isCritical(t, legacy[0])
		muteless := resolve(t, noCrits, call, state, attackOn(t, "attacker_1", "spear"))

		// A pack with both rules off must never crit, whatever the dice did — including
		// on a natural 20, which is 5e's automatic critical and is exactly the claim
		// `crit_attack_die_max: false` is a pack making.
		if len(muteless) == 2 && isCritical(t, muteless[0]) {
			t.Errorf("seed %d: a pack with both critical rules off critted anyway", seed)
		}

		if modernCritical == legacyCritical {
			continue
		}

		differing++

		if !modernCritical {
			t.Errorf("seed %d: the 2024 pack did not crit on a damage die at its maximum", seed)
		}

		if legacyCritical {
			t.Errorf("seed %d: the 2014 pack crited on a damage die at its maximum, which is "+
				"the half 2014 does not have", seed)
		}
	}

	if differing == 0 {
		t.Errorf("no seed in 1..59 made the two editions disagree, across %d paired resolutions; "+
			"a damage die at its maximum has to be findable", compared)
	}
}

// TestDamageDiceAreMultipliedAndModifiersAreNot is 5e's rule and the bug this project's
// comments keep naming: doubling a +4 because the attack critted is a defect a table
// would notice immediately.
//
// **The expected figure is computed from the pack rather than observed.** A d2 damage die
// and a `+3` in the expression make the arithmetic checkable: on a critical, a die showing
// `f` deals `2f` plus the modifier **once**, so the two candidates differ by 3 for every
// seed and no seed makes them agree.
func TestDamageDiceAreMultipliedAndModifiersAreNot(t *testing.T) {
	t.Parallel()

	engine := engineWithDie(t)

	token := aToken(t, "Vurg", 5,
		map[string]int{"strength": 18, "dexterity": 12},
		map[string]any{
			"attacks": []any{map[string]any{
				"name": "spear", "ability": "strength", "damage": "1d2+3",
			}},
			"ac": 0,
		})

	// A defender with room to take it, because a clamped-at-zero creature would hide the
	// very figure under test.
	defender := aState(t, map[rules.ObjectID][]byte{"attacker_1": token})

	critics := 0

	for seed := byte(1); seed < 40; seed++ {
		mutations := resolve(
			t,
			engine,
			aTestCall(t, seed),
			defender,
			attackOn(t, "attacker_1", "spear"),
		)

		hit, ok := mutationFor(t, mutations, "defender_1")
		if !ok {
			continue // a miss
		}

		if !isCritical(t, mutations[0]) {
			continue
		}

		critics++

		struck := bodyOf(t, hit.Args)
		damage := 100 - struck.HitPoints

		// `damage_bonus` is `prof + ability` = 3 + 4 = 7, and the expression's own `+3`
		// is a modifier rather than a die. So a critical showing `f` deals `2f + 3 + 7`.
		face := damage - 3 - 7
		if face < 2 || face%2 != 0 {
			t.Fatalf("seed %d: a critical dealt %d damage, which is 2f+3+7 for no face f; "+
				"the modifiers are being multiplied too", seed, damage)
		}
	}

	if critics == 0 {
		t.Error("no seed in 1..39 produced a critical, so the damage-multiplication rule was " +
			"never exercised")
	}
}

// TestAFinesseMasteryChoosesTheBetterAbilityNotTheLaterOne is the bug the fold was
// written against: choosing by name rather than by modifier.
//
// **Both directions, because either one alone is satisfiable by a constant.** A resolver
// that always answered `strength` passes "a strong character's finesse weapon uses
// Strength"; a resolver that always answered `dexterity` passes the other. Asserting both
// is what makes the fold a fold.
func TestAFinesseMasteryChoosesTheBetterAbilityNotTheLaterOne(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	if _, declared := pack.MasteryAt("finesse"); !declared {
		t.Fatal("the shipped pack declares no finesse mastery, so this test is measuring nothing")
	}

	strong := attackerModifiers(&creature{Ability: map[string]int{
		"strength": 18, "dexterity": 10,
	}}, pack)

	dexterous := attackerModifiers(&creature{Ability: map[string]int{
		"strength": 10, "dexterity": 18,
	}}, pack)

	finesse := masteryChoice{Effects: []effectFile{{
		Kind: effectDamageAbilityFromBest, Of: []string{"strength", "dexterity"},
	}}}

	// Strength 18 is +4 and Dexterity 10 is 0.
	if got := damageAbility("strength", finesse, strong); got != "strength" {
		t.Errorf(
			"a character with Strength 18 rolls a finesse weapon's damage with %q, want strength",
			got,
		)
	}

	if got := damageAbility("strength", finesse, dexterous); got != "dexterity" {
		t.Errorf(
			"a character with Dexterity 18 rolls a finesse weapon's damage with %q, want dexterity; "+
				"the later name in the row's list is not the better ability",
			got,
		)
	}

	// A tie keeps the attack's own ability, which is what a weapons table says.
	even := attackerModifiers(&creature{Ability: map[string]int{
		"strength": 14, "dexterity": 14,
	}}, pack)

	if got := damageAbility("dexterity", finesse, even); got != "dexterity" {
		t.Errorf("with equal modifiers the fold chose %q, want the attack's own ability", got)
	}

	// And a row with no such effect leaves the attack's ability alone.
	if got := damageAbility("strength", masteryChoice{}, strong); got != "strength" {
		t.Errorf("a mastery granting no ability effect changed the damage ability to %q", got)
	}
}

// TestAMasteryRequirementIsCheckedAgainstTheChosenMasteryNotTheGrantedList is the
// subtlety `masteryChoice`'s own comment names.
//
// A greatsword grants `great_weapon`, `heavy` and `two_handed`. An attacker holding only
// `great_weapon` must **not** get the heavy weapon's effects, and a resolver that checked
// the granted list would say they did. The test builds the exact case: an attacker who
// cannot meet a requirement on a mastery they hold anyway.
func TestAMasteryRequirementIsCheckedAgainstTheChosenMasteryNotTheGrantedList(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	heavy, declared := pack.MasteryAt("heavy")
	if !declared {
		t.Fatal("the shipped pack declares no heavy mastery")
	}

	// **Scores, not modifiers**: the row says "at least 13", which is a number on a
	// character sheet. A Strength 18's *modifier* is +4, and comparing that against 13
	// would refuse a heavy weapon to every creature in the game.
	weak := attackerScores(&creature{Ability: map[string]int{"strength": 8}}, pack)
	mighty := attackerScores(&creature{Ability: map[string]int{"strength": 18}}, pack)

	if meetsRequirements(heavy, weak) {
		t.Error("a Strength 8 creature meets the heavy mastery, whose requirement is 13")
	}

	if !meetsRequirements(heavy, mighty) {
		t.Error("a Strength 18 creature does not meet the heavy mastery, whose requirement is 13")
	}

	// The fold picks the **first granted** mastery the attacker holds, and checks the
	// requirement of *that* one.
	choice, err := defaultMastery{}.Mastery(masteryInput{
		pack:    pack,
		granted: []string{"great_weapon", "heavy"},
		held:    []string{"great_weapon"},
		scores:  weak,
	})
	if err != nil {
		t.Fatalf("Mastery: %v", err)
	}

	if choice.Slug != "great_weapon" {
		t.Errorf("the fold chose %q, want great_weapon", choice.Slug)
	}

	// An attacker holding **only** `heavy` and unable to meet it falls back — and the
	// fallback is **not an error**, because the attack still happens and refusing would
	// be a rules bug presenting as a permission problem.
	fellBack, err := defaultMastery{}.Mastery(masteryInput{
		pack:    pack,
		granted: []string{"heavy"},
		held:    []string{"heavy"},
		scores:  weak,
	})
	if err != nil {
		t.Fatalf("Mastery: %v", err)
	}

	if !fellBack.FellBack || len(fellBack.Effects) != 0 {
		t.Errorf("an unqualified mastery conferred %v (fell back: %t); a held but unmet "+
			"mastery must confer nothing", fellBack.Effects, fellBack.FellBack)
	}
}

// TestAWeaponGrantingAMasteryThePackDoesNotDeclareIsRefused is the asymmetry the code
// comments keep defending: a mastery cannot be validated at load, because which
// masteries a weapon offers lives on the creature's runtime state, which no load check
// sees.
//
// Skipping the unknown mastery would resolve the attack with no mastery and **report
// success** — the same failure `ErrUnknownHook` refuses at load, arriving by the one door
// load validation cannot close.
func TestAWeaponGrantingAMasteryThePackDoesNotDeclareIsRefused(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	// A weapon granting a mastery **this pack does not declare**, listed first so the
	// fold reaches it before any row it does know.
	//
	// An overlay cannot produce this: the merge is by slug and appends, so a row the
	// base declared stays. The realistic door is a character authored against a pack
	// that declared a mastery a later pack dropped — which is precisely why the refusal
	// is at resolution and not at load, because which masteries a weapon offers lives on
	// a creature's runtime state that no load check sees.
	token := aToken(t, "Vurg", 5,
		map[string]int{"strength": 18},
		map[string]any{
			"attacks": []any{map[string]any{
				"name": "greataxe", "ability": "strength", "damage": "1d12+4",
				"mastery": []string{"three_handed", "great_weapon"},
			}},
			"masteries": []string{"three_handed", "great_weapon"},
			"ac":        0,
		})

	state := aState(t, map[rules.ObjectID][]byte{"attacker_1": token})

	_, err = engine.Apply(
		t.Context(), aTestCall(t), state, attackOn(t, "attacker_1", "greataxe"),
	)
	if !errors.Is(err, ErrNoSuchMastery) {
		t.Fatalf("want %v, got %v", ErrNoSuchMastery, err)
	}
}

// TestAdvantageAndDisadvantageCancelAcrossBothParticipants is §7.2's "advantage and
// disadvantage are not the same thing", resolved.
//
// The fold is over **both** creatures' condition rows, and cancelling is 5e's rule rather
// than a column: a creature that is invisible *and* prone has an advantage and a
// disadvantage on the same roll, and the answer is neither. The assertion is an identity
// rather than a distribution: an attacker carrying a cancelling pair must draw exactly what
// an unconditioned attacker draws under the same seed, because the mode — and therefore
// the draw's label — is `none` for both.
func TestAdvantageAndDisadvantageCancelAcrossBothParticipants(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	// `invisible` grants advantage on its own attacks; `poisoned` costs it. The pair
	// cancels.
	cancelling := aToken(t, "Vurg", 5,
		map[string]int{"strength": 18, "dexterity": 12},
		map[string]any{
			"conditions": []string{"invisible", "poisoned"},
			"attacks": []any{map[string]any{
				"name": "spear", "ability": "strength", "damage": "1d12+4",
			}},
			"ac": 0,
		})

	plain := aToken(t, "Vurg", 5,
		map[string]int{"strength": 18, "dexterity": 12},
		map[string]any{
			"attacks": []any{map[string]any{
				"name": "spear", "ability": "strength", "damage": "1d12+4",
			}},
			"ac": 0,
		})

	// And the defender's own rows read the other column: a `prone` defender is attacked
	// with advantage, which cancels an attacker's disadvantage.
	state := aState(t, map[rules.ObjectID][]byte{"attacker_1": cancelling, "defender_1": plain})

	// A second attacker, carrying only `poisoned`, so the two must **not** agree.
	disadvantaged := aToken(t, "Vurg", 5,
		map[string]int{"strength": 18, "dexterity": 12},
		map[string]any{
			"conditions": []string{"poisoned"},
			"attacks": []any{map[string]any{
				"name": "spear", "ability": "strength", "damage": "1d12+4",
			}},
			"ac": 0,
		})

	mixed := aState(t, map[rules.ObjectID][]byte{"attacker_1": disadvantaged, "defender_1": plain})

	diverged := false

	for seed := byte(1); seed < 20; seed++ {
		call := aTestCall(t, seed)

		cancelledRoll := firstRoll(
			t,
			resolve(t, engine, call, state, attackOn(t, "attacker_1", "spear")),
		)
		plainRoll := firstRoll(t, resolve(t, engine, call, aState(t, map[rules.ObjectID][]byte{
			"attacker_1": plain, "defender_1": plain,
		}), attackOn(t, "attacker_1", "spear")))
		disadvantagedRoll := firstRoll(
			t,
			resolve(t, engine, call, mixed, attackOn(t, "attacker_1", "spear")),
		)

		if cancelledRoll != plainRoll {
			t.Fatalf("seed %d: an attacker carrying advantage and disadvantage read %d where an "+
				"unconditioned attacker read %d; the two did not cancel", seed, cancelledRoll, plainRoll)
		}

		if disadvantagedRoll == plainRoll {
			diverged = false

			continue
		}

		diverged = true
	}

	if !diverged {
		t.Error("nineteen seeds gave a disadvantaged attacker the same roll as an unconditioned " +
			"one, so the condition rows are not reaching the draw's label")
	}
}

// TestTheExemptionComesFromTheConditionRowNotFromASlug is the column a house rule
// declares; the linter's dictionary is for prose, not for a pack's vocabulary.
// An engine that checked for one spelled condition would have to be forked for an
// overlay that wanted a different exemption; an engine that consulted no column would
// silently crit a helpless creature under a pack that had asked it not to. Both
// failures are invisible —
// helpless creature under a pack that had asked it not to. Both failures are invisible —
// the numbers are plausible — which is why the assertion is a pair of packs whose
// identical draws produce opposite crits.
//
//nolint:misspell // `paralyzed` is the pack's own spelling and the slug it
func TestTheExemptionComesFromTheConditionRowNotFromASlug(t *testing.T) {
	t.Parallel()

	// Both packs declare the exemption **toggle** on and the damage-die rule on. The
	// only difference between them is one column on one condition row, so every
	// difference in their answers is attributable to the column.
	const head = `system: "dnd5e"
title: "exemption"
dice:
  sizes: [2, 4, 6, 8, 10, 12, 20, 100]
toggles:
  crit_damage_die_max: true
  crit_ignored_by_incapacitated: true
`

	exempting := engineWithOverlay(t, head+`version: "test-exempt@1"
conditions:
  - { slug: unconscious, label: "Unconscious", critical_exempt: true }
`)

	notExempting := engineWithOverlay(t, head+`version: "test-not-exempt@1"
conditions:
  - { slug: unconscious, label: "Unconscious" }
`)

	// A fourth pack, and it is the one that catches a resolver that reads the row's
	// column but **not the toggle**: the row still exempts and the switch is off, so the
	// exemption must not apply. Without it, a critical rule that honoured `column ||
	// !column` regardless of the switch would pass the comparison above — every pair in
	// that comparison has the toggle *on*.
	toggleOff := engineWithOverlay(t, `system: "dnd5e"
version: "test-toggle-off@1"
title: "toggle off"
dice:
  sizes: [2, 4, 6, 8, 10, 12, 20, 100]
toggles:
  crit_damage_die_max: true
  crit_ignored_by_incapacitated: false
conditions:
  - { slug: unconscious, label: "Unconscious", critical_exempt: true }
`)

	if toggleOff.Pack().Toggle(toggleCritIgnoredByIncapacitated) {
		t.Fatal("the fourth fixture left the exemption switch on; it is measuring nothing")
	}

	if !exempting.Pack().Toggle(toggleCritIgnoredByIncapacitated) {
		t.Fatal("the fixture did not switch the exemption on")
	}

	row, declared := exempting.Pack().ConditionAt("unconscious")
	if !declared || !row.criticalExempt {
		t.Fatal("the fixture's condition row does not carry the column")
	}

	plain, declared := notExempting.Pack().ConditionAt("unconscious")
	if !declared {
		t.Fatal("the second fixture dropped the row rather than its column, so the packs differ " +
			"in more than the thing under test")
	}

	if plain.criticalExempt {
		t.Fatal("the second fixture's condition row carries the column too")
	}

	// A spear whose damage die is a d2, so it reaches its maximum about half the time and
	// a natural 20 on the attack die happens about once in twenty.
	attacker := aToken(t, "Vurg", 5,
		map[string]int{"strength": 18, "dexterity": 12},
		map[string]any{
			"attacks": []any{map[string]any{
				"name": "spear", "ability": "strength", "damage": "1d2",
			}},
			"ac": 0,
		})

	unconscious := aToken(t, "Down", 3, map[string]int{"dexterity": 10},
		map[string]any{"conditions": []string{"unconscious"}, "ac": 1, "hp": 40, "max_hp": 40})

	state := aState(t, map[rules.ObjectID][]byte{
		"attacker_1": attacker, "defender_1": unconscious,
	})

	exempted, critted := 0, 0

	for seed := byte(1); seed < 60; seed++ {
		call := aTestCall(t, seed)

		againstExempt := resolve(t, exempting, call, state, attackOn(t, "attacker_1", "spear"))
		againstPlain := resolve(t, notExempting, call, state, attackOn(t, "attacker_1", "spear"))
		switchOff := resolve(t, toggleOff, call, state, attackOn(t, "attacker_1", "spear"))

		if len(againstExempt) == 2 && isCritical(t, againstExempt[0]) {
			exempted++
		}

		if len(againstPlain) == 2 && isCritical(t, againstPlain[0]) {
			critted++
		}

		// The switch is what the row's column is gated by, so the pack with the switch off
		// must answer exactly as the pack with no column does — on every seed, not on
		// average.
		if len(switchOff) == 2 && isCritical(t, switchOff[0]) != isCritical(t, againstPlain[0]) {
			t.Errorf("seed %d: a pack whose exemption switch is off critted %t where a pack "+
				"with no exemption column critted %t",
				seed, isCritical(t, switchOff[0]), isCritical(t, againstPlain[0]))
		}
	}

	if critted == 0 {
		t.Error("no seed produced a critical against a defender whose row carries no exemption, " +
			"so the comparison is measuring nothing")
	}

	// The exemption is checked **first** and can only ever answer false, so a pack whose
	// row says so cannot crit anything against that defender — not on a 20, not on a
	// maximum damage die.
	if exempted != 0 {
		t.Errorf("the exempting pack critted %d times against a defender its own rows exempt; "+
			"the toggle says those attacks cannot crit", exempted)
	}

	// And the converse: a pack that does **not** exempt must crit exactly when the other
	// does not, seed for seed. Without this the assertion above would also pass for a
	// critical rule that never fired.
	if exempted+critted >= 60 {
		t.Error("the two packs disagreed on every seed, so the column is not the only thing " +
			"that reaches the critical rule")
	}
}

// TestADamageCapIsDataAndIsAppliedBeforeMultipliers is a house rule's "cap the big hits"
// expressed as one field.
//
// The cap is checked against the **post-bonus total**, and the order matters: a cap
// applied before the critical multiplier would let a critical through the cap, which is
// the opposite of what a GM capping a table's big hits is asking for.
func TestADamageCapIsDataAndIsAppliedBeforeMultipliers(t *testing.T) {
	t.Parallel()

	engine := engineWithDie(t)
	capped := engineWithOverlay(t, `system: "dnd5e"
version: "test-cap@1"
title: "capped"
dice:
  sizes: [2, 4, 6, 8, 10, 12, 20, 100]
attack:
  damage_cap: 8
`)

	token := aToken(t, "Vurg", 5,
		map[string]int{"strength": 18, "dexterity": 12},
		map[string]any{
			"attacks": []any{map[string]any{
				"name": "spear", "ability": "strength", "damage": "1d2+6",
			}},
			"ac": 0,
		})

	for _, fixture := range []struct {
		what   string
		system *Engine
	}{
		{"uncapped", engine},
		{"capped", capped},
	} {
		defender := aState(t, map[rules.ObjectID][]byte{"attacker_1": token})

		for seed := byte(1); seed < 30; seed++ {
			mutations := resolve(t, fixture.system, aTestCall(t, seed), defender,
				attackOn(t, "attacker_1", "spear"))

			hit, ok := mutationFor(t, mutations, "defender_1")
			if !ok {
				continue
			}

			damage := 100 - bodyOf(t, hit.Args).HitPoints
			limit := 0

			if fixture.system.Pack().DamageCap() > 0 {
				limit = fixture.system.Pack().DamageCap()
			}

			if limit > 0 && damage > limit {
				t.Errorf("%s: seed %d dealt %d damage, over the pack's cap of %d",
					fixture.what, seed, damage, limit)
			}
		}
	}

	if capped.Pack().DamageCap() != 8 {
		t.Fatalf("the overlay's damage cap is %d, want 8; the fixture is not testing the cap",
			capped.Pack().DamageCap())
	}
}

// TestTheMasteryContributionsReachTheDamage is `add_ability_to_damage` proved live.
//
// The effect was validated at load and read by nothing for a while, and the failure that
// produces is the quietest kind: a weapon whose mastery does nothing, resolving
// successfully and reporting a plausible number. The assertion is that an attacker holding
// `two_handed` deals **more** than one who does not, from the same draw.
func TestTheMasteryContributionsReachTheDamage(t *testing.T) {
	t.Parallel()

	engine := engineWithDie(t)

	build := func(t *testing.T, mastery []string) []byte {
		t.Helper()

		return aToken(t, "Vurg", 5,
			map[string]int{"strength": 18, "dexterity": 12},
			map[string]any{
				"attacks": []any{map[string]any{
					"name": "quarterstaff", "ability": "strength", "damage": "1d2",
					"mastery": mastery,
				}},
				"masteries": mastery,
				"ac":        0,
			})
	}

	twoHanded := build(t, []string{"two_handed"})
	bare := build(t, nil)

	// Strength 18 is a modifier of +4, and the effect adds it a second time.
	const abilityBonus = 4

	matched := 0

	for seed := byte(1); seed < 30; seed++ {
		withMastery := damageDealt(t, resolve(t, engine, aTestCall(t, seed),
			aState(t, map[rules.ObjectID][]byte{"attacker_1": twoHanded}),
			attackOn(t, "attacker_1", "quarterstaff")))

		other := aState(t, map[rules.ObjectID][]byte{"attacker_1": bare})

		without := damageDealt(t, resolve(t, engine, aTestCall(t, seed), other,
			attackOn(t, "attacker_1", "quarterstaff")))

		switch {
		case withMastery == without+abilityBonus:
			matched++
		case withMastery != without:
			t.Errorf("seed %d: two_handed dealt %d where an unarmed-in-that-respect attack "+
				"dealt %d; the difference is %d, not %d",
				seed, withMastery, without, withMastery-without, abilityBonus)
		}
	}

	if matched == 0 {
		t.Error("no seed showed the two_handed mastery adding its ability modifier, so " +
			"add_ability_to_damage is validated and read by nothing")
	}
}

// TestHealIsClampedAtTheMaximumAndAtZero is `resolveHeal`'s two clamps, and each has a
// different failure.
//
// A heal past the maximum would let a GM's arithmetic turn a campaign into infinite hit
// points; a negative amount would be a heal that damages. Both directions are asserted,
// because a clamp applied to only one of them is a clamp that passes half this test.
func TestHealIsClampedAtTheMaximumAndAtZero(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	body := aToken(t, "Vurg", 5, map[string]int{"strength": 18},
		map[string]any{"hp": 10, "max_hp": 20})

	for _, fixture := range []struct {
		amount int
		want   int
		why    string
	}{
		{amount: 5, want: 15, why: "a heal inside the range"},
		{amount: 100, want: 20, why: "a heal past the maximum"},
		{amount: -100, want: 0, why: "a negative amount, which must not heal"},
		{amount: 0, want: 10, why: "zero is a legal amount and changes nothing"},
	} {
		t.Run(fixture.why, func(t *testing.T) {
			t.Parallel()

			intent, err := rules.NewIntent(OpHeal, "orc_1",
				[]byte(`{"amount":`+strconv.Itoa(fixture.amount)+`}`))
			if err != nil {
				t.Fatalf("building the intent: %v", err)
			}

			state, err := rules.NewState(1, []rules.Object{
				{ID: "orc_1", Kind: rules.KindToken, Data: body},
			})
			if err != nil {
				t.Fatalf("building a state: %v", err)
			}

			mutations := resolve(t, engine, aTestCall(t), state, intent)

			healed := bodyOf(t, mutations[0].Args)
			if healed.HitPoints != fixture.want {
				t.Errorf("a heal of %d left %d hit points, want %d",
					fixture.amount, healed.HitPoints, fixture.want)
			}
		})
	}
}

// TestApplyStatusResolvesTheMaximumBeforeTheHitPoints is the one order-dependence in the
// GM's adjudication, and both halves of it are asserted.
//
// A GM who raises the maximum and sets hit points below the old one gets the hit points
// they set; a GM who lowers the maximum below the current hit points gets the hit points
// clamped. **Clamped, not refused**: a token showing 40 of 12 is a state no client can
// render.
func TestApplyStatusResolvesTheMaximumBeforeTheHitPoints(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	body := aToken(t, "Vurg", 5, map[string]int{"strength": 18},
		map[string]any{"hp": 44, "max_hp": 49})

	for _, fixture := range []struct {
		args string
		hp   int
		max  int
		why  string
	}{
		{args: `{"hp":20}`, hp: 20, max: 49, why: "setting hit points alone"},
		{args: `{"max_hp":30}`, hp: 30, max: 30, why: "lowering the maximum clamps the hit points"},
		{args: `{"max_hp":60,"hp":10}`, hp: 10, max: 60, why: "raising the maximum keeps the hit points set"},
		{args: `{"hp":-5}`, hp: 0, max: 49, why: "a negative hit-point figure clamps at zero"},
		{args: `{"speed":-3}`, hp: 44, max: 49, why: "a negative speed clamps at zero"},
	} {
		t.Run(fixture.why, func(t *testing.T) {
			t.Parallel()

			intent, err := rules.NewIntent(OpApplyStatus, "orc_1", []byte(fixture.args))
			if err != nil {
				t.Fatalf("building the intent: %v", err)
			}

			state, err := rules.NewState(1, []rules.Object{
				{ID: "orc_1", Kind: rules.KindToken, Data: body},
			})
			if err != nil {
				t.Fatalf("building a state: %v", err)
			}

			adjusted := bodyOf(t, resolve(t, engine, aTestCall(t), state, intent)[0].Args)

			if adjusted.HitPoints != fixture.hp || adjusted.MaxHitPoints != fixture.max {
				t.Errorf("%s left %d of %d, want %d of %d",
					fixture.why, adjusted.HitPoints, adjusted.MaxHitPoints, fixture.hp, fixture.max)
			}
		})
	}
}

// TestConditionRowsArePrunedIntoThePacksOrder is the write path's half of S-3.3's
// inertness, and it is asserted through the mutation's bytes rather than through a
// decoded value.
//
// **The order claim is the load-bearing part.** A slice's order reaching a mutation's
// bytes is a replay hazard: two creatures carrying `prone` and `blinded` must produce the
// same bytes whichever acquired which first, and one that did not would make a replay
// depend on the order a GM happened to click.
func TestConditionRowsArePrunedIntoThePacksOrder(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	// Applying two conditions in two orders, one resolution at a time, with each
	// resolution reading the previous mutation's own bytes. That is the real sequence:
	// the hub applies a mutation and the next intent reads the document it wrote.
	carriedIn := func(conditions ...string) []byte {
		t.Helper()

		return aToken(t, "Vurg", 5, nil, map[string]any{"conditions": conditions})
	}

	applyConditions := func(t *testing.T, slugs ...string) []string {
		t.Helper()

		carried := carriedIn()

		for _, slug := range slugs {
			intent, buildErr := rules.NewIntent(OpApplyCondition, "orc_1",
				[]byte(`{"condition":"`+slug+`"}`))
			if buildErr != nil {
				t.Fatalf("building the intent: %v", buildErr)
			}

			state, err := rules.NewState(1, []rules.Object{
				{ID: "orc_1", Kind: rules.KindToken, Data: carried},
			})
			if err != nil {
				t.Fatalf("building a state: %v", err)
			}

			carried = resolve(t, engine, aTestCall(t), state, intent)[0].Args
		}

		return bodyOf(t, carried).Conditions
	}

	proneThenBlinded := applyConditions(t, "prone", "blinded")
	blindedThenProne := applyConditions(t, "blinded", "prone")

	if !slices.Equal(proneThenBlinded, blindedThenProne) {
		t.Errorf("the same two conditions applied in two orders gave %v and %v",
			proneThenBlinded, blindedThenProne)
	}

	// And it is the **pack's** order, not the order they were applied in: `blinded` is
	// declared before `prone` in `data/base.yaml`.
	if !slices.Equal(proneThenBlinded, []string{"blinded", "prone"}) {
		t.Errorf("the conditions are %v, want the pack's declaration order [blinded prone]",
			proneThenBlinded)
	}

	// Clearing one leaves the other, and the mutation's op is the intent's own rather
	// than a second record of the same fact.
	clearing, clearErr := rules.NewIntent(
		OpClearCondition, "orc_1", []byte(`{"condition":"blinded"}`),
	)
	if clearErr != nil {
		t.Fatalf("building the intent: %v", clearErr)
	}

	state, stateErr := rules.NewState(1, []rules.Object{
		{ID: "orc_1", Kind: rules.KindToken, Data: carriedIn("blinded", "prone")},
	})
	if stateErr != nil {
		t.Fatalf("building a state: %v", stateErr)
	}

	mutations := resolve(t, engine, aTestCall(t), state, clearing)

	if mutations[0].Op != OpClearCondition {
		t.Errorf("the mutation is %s, want clear_condition", mutations[0])
	}

	if held := bodyOf(t, mutations[0].Args).Conditions; !slices.Equal(held, []string{"prone"}) {
		t.Errorf("clearing blinded left %v, want [prone]", held)
	}
}

// TestAnAttackNamingAnAttackTheCreatureDoesNotHaveIsRefused is the refusal that must
// name the attack rather than resolving to nothing.
//
// `rules.System.Apply` says returning no mutations with no error is legitimate, so a
// resolver that ignored an unknown attack name would report success for an intent it did
// not carry out.
func TestAnAttackNamingAnAttackTheCreatureDoesNotHaveIsRefused(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	state := aState(t, map[rules.ObjectID][]byte{"attacker_1": aTestToken(t)})

	for _, name := range []string{"longbow", "", "Greataxe"} {
		t.Run(strconv.Quote(name), func(t *testing.T) {
			t.Parallel()

			intent, err := rules.NewIntent(OpAttack, "attacker_1",
				[]byte(`{"attack":"`+name+`","defender":"defender_1"}`))
			if err != nil {
				t.Fatalf("building the intent: %v", err)
			}

			mutations, err := engine.Apply(t.Context(), aTestCall(t), state, intent)
			if !errors.Is(err, ErrNoSuchAttack) {
				t.Fatalf("want %v, got %v", ErrNoSuchAttack, err)
			}

			if len(mutations) > 0 {
				t.Errorf("a refused attack returned %d mutation(s)", len(mutations))
			}
		})
	}
}

// TestAnUnknownOperationIsRefusedAsAWiringFault is the last of `Apply`'s six rules: the
// op is checked before the state, so a system asked for something it does not resolve
// reports the disagreement rather than something about the tabletop.
func TestAnUnknownOperationIsRefusedAsAWiringFault(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	for _, op := range []rules.Op{"teleport", "cast_spell", "move_token", "set_hp"} {
		t.Run(op.String(), func(t *testing.T) {
			t.Parallel()

			intent, err := rules.NewIntent(op, "attacker_1", []byte(`{}`))
			if err != nil {
				t.Fatalf("building the intent: %v", err)
			}

			// **Against an empty state**, so a refusal about the tabletop could not be
			// mistaken for the refusal being tested.
			empty, err := rules.NewState(0, nil)
			if err != nil {
				t.Fatalf("building a state: %v", err)
			}

			if _, err := engine.Apply(
				t.Context(),
				aTestCall(t),
				empty,
				intent,
			); !errors.Is(
				err,
				ErrUnknownOp,
			) {
				t.Errorf("want %v, got %v", ErrUnknownOp, err)
			}
		})
	}

	// And every op it *does* resolve is one it declares.
	for _, op := range []rules.Op{
		OpRoll, OpAttack, OpHeal, OpApplyCondition, OpClearCondition, OpApplyStatus, OpRemoveToken,
	} {
		if !knownOp(op) {
			t.Errorf("Apply has a case for %q and `Resolves` says it does not", op)
		}

		if !engine.Resolves(op) {
			t.Errorf("%q resolves but `Resolves` does not say so", op)
		}
	}

	if engine.Resolves("cast_spell") {
		t.Error("`Resolves` claims to resolve an operation this system does not")
	}
}

// TestEveryOpTheEngineResolvesIsAValidWireToken is the shape half of §10.2: a system may
// define its vocabulary, and what is checked is that the wire could carry it.
//
// A mutation op that the codec would refuse is a fact no client could send, and the
// failure would appear as a protocol error at the frame rather than as a mistake in this
// package.
func TestEveryOpTheEngineResolvesIsAValidWireToken(t *testing.T) {
	t.Parallel()

	ops := []rules.Op{
		OpRoll, OpAttack, OpHeal, OpApplyCondition, OpClearCondition, OpApplyStatus,
		OpRemoveToken, OpSetHitPoints,
	}

	for _, op := range ops {
		if !op.Valid() {
			t.Errorf("%q is not a usable op token, so no client could ever send it", op)
		}

		if !GMOnly(op) == (op == OpApplyStatus || op == OpRemoveToken) {
			t.Errorf("%q and GMOnly disagree about who may make it", op)
		}
	}
}

// # Reading a resolution's outcome
//
// The mutation's payload is a creature's body, which is this package's own encoding, and
// a test asserting on a roll's *outcome* rather than on a mutation's shape has to decode
// it. `readCreature` is that decoder, and using it here rather than a hand-written
// unmarshal is what keeps the assertion honest if the encoding's field names change.

// isCritical reports whether an attack's record says it critted.
func isCritical(t *testing.T, mutation rules.Mutation) bool {
	t.Helper()

	attacker := bodyOf(t, mutation.Args)

	if attacker.LastRoll == nil {
		t.Fatalf("the attack's mutation carries no roll record, so nothing said what happened")
	}

	return attacker.LastRoll.Critical
}

// firstRoll returns the natural of an attack's roll record, which is the draw itself
// rather than the total — the assertion that advantage and disadvantage cancel is about
// the die that was kept.
func firstRoll(t *testing.T, mutations []rules.Mutation) int {
	t.Helper()

	if len(mutations) == 0 {
		t.Fatal("an attack resolved to nothing")
	}

	attacker := bodyOf(t, mutations[0].Args)

	if attacker.LastRoll == nil || attacker.LastRoll.Natural == nil {
		t.Fatal("the attack's mutation carries no natural, so there is nothing to compare")
	}

	return *attacker.LastRoll.Natural
}

// mutationFor returns the mutation addressing one object, and whether there was one.
//
// assertion sees which placement it was about — which is the same reason the conformance
// suite's own fixture names its two objects.
//
//nolint:unparam // the target is named rather than assumed so that a reader of a failing
func mutationFor(
	t *testing.T,
	mutations []rules.Mutation,
	target rules.ObjectID,
) (rules.Mutation, bool) {
	t.Helper()

	for _, mutation := range mutations {
		if mutation.Target == target {
			return mutation, true
		}
	}

	return rules.Mutation{}, false
}

// bodyOf decodes a mutation's payload as a creature.
//
// **A named wrapper over `readCreature`** rather than a hand-written unmarshal, because
// the assertion is about the outcome of a resolution and the encoding is this package's:
// reading the same bytes the resolver wrote is what keeps a change to a field name from
// turning twenty assertions into twenty tests that pass against an empty struct.
func bodyOf(t *testing.T, body []byte) creature {
	t.Helper()

	decoded, err := readCreature(rules.Object{ID: "body", Kind: rules.KindToken, Data: body})
	if err != nil {
		t.Fatalf("the payload is not a readable creature: %v", err)
	}

	return decoded
}

// damageDealt returns how much hit points the defender lost, from the mutations.
func damageDealt(t *testing.T, mutations []rules.Mutation) int {
	t.Helper()

	hit, ok := mutationFor(t, mutations, "defender_1")
	if !ok {
		t.Fatal("the attack did not reach the defender, so there is no damage to compare")
	}

	return 100 - bodyOf(t, hit.Args).HitPoints
}

// TestAnAttackLandsWhenTheRollBeatsArmourClass is the one question about the attack roll,
// asked once and deliberately.
//
// Almost every other test in this file uses a defender whose armour class is 1 so that every
// attack lands, and a fixture that cannot fail is a fixture that has stopped testing. So the
// comparison itself is asserted here, in both directions, with the natural recovered from the
// roll record — which is the same figure a player sees, so the assertion is about the number
// on the token rather than about an internal.
func TestAnAttackLandsWhenTheRollBeatsArmourClass(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	// Armour class 14: `ac_base` 10 plus a Dexterity 14 modifier of +2.
	defender := aToken(t, "Wall", 3, map[string]int{"dexterity": 14},
		map[string]any{"ac": 14, "hp": 1000, "max_hp": 1000})

	// Level 5, Strength 18: `prof` 3 and a Strength modifier of +4, so the roll total is the
	// attack die plus seven.
	attacker := aToken(t, "Vurg", 5,
		map[string]int{"strength": 18, "dexterity": 12},
		map[string]any{
			"attacks": []any{map[string]any{
				"name": "spear", "ability": "strength", "damage": "1d12+4",
			}},
			"ac": 0,
		})

	landed, missed := 0, 0

	for seed := range 60 {
		state := aState(t, map[rules.ObjectID][]byte{
			"attacker_1": attacker, "defender_1": defender,
		})

		mutations := resolve(t, engine, aTestCall(t, byte(seed)), state,
			attackOn(t, "attacker_1", "spear"))

		record := bodyOf(t, mutations[0].Args).LastRoll
		if record == nil || record.Natural == nil {
			t.Fatalf("seed %d: the attack recorded no natural", seed)
		}

		_, hit := mutationFor(t, mutations, "defender_1")

		switch {
		case *record.Natural+7 >= 14:
			if !hit {
				t.Errorf("seed %d: the attack die showed %d and the total was %d against armour "+
					"class 14, and it still missed", seed, *record.Natural, *record.Natural+7)
			}

			landed++
		default:
			if hit {
				t.Errorf("seed %d: the attack die showed %d and the total was %d against armour "+
					"class 14, and it landed anyway", seed, *record.Natural, *record.Natural+7)
			}

			missed++
		}
	}

	if landed == 0 || missed == 0 {
		t.Errorf("%d landed and %d missed across sixty seeds; both branches must be exercised, "+
			"and a fixture that only ever saw one of them would certify half a rule",
			landed, missed)
	}
}

// TestANaturalTwentyHitsUnderBothEditionsAndTheExemptionDoesNotMakeItMiss is the bug a
// player finds in one round, written down before it was fixed.
//
// `defaultCritical` short-circuits on the defender's exemption, and `resolveAttack` used to
// read that rule's answer as **the hit**. So under a 2024 pack — the toggle on, and the
// `unconscious` row carrying `critical_exempt` — a natural 20 against an unconscious
// defender **missed**: an armour class of 30, a total of 27, `met: false`, no mutation
// addressing the defender at all. The identical roll auto-hit under 2014 and against any
// unexempted target, so the defect was invisible to every fixture that did not combine a
// maximum die, an unreachable armour class and a 2024 condition row.
//
// The rule that was consulted is not wrong. A natural 20 is an automatic critical hit in
// 5e, and what 2024 changes is that it does **not** automatically *count as* a critical
// hit against an incapacitated, paralysed or unconscious target — a statement about the
// damage dice, not about whether the sword lands.
//
// # Both editions, and both halves of each
//
// **The table, because either row alone is satisfiable by a constant.** A resolver that
// always let a 20 through would pass 2024 and is exactly the bug; a resolver that never
// let one through would pass a 2014-only test and break the table. So each row asserts the
// hit **and** the critical, which are the two questions the fix separated, and the two
// editions are required to disagree about the second while agreeing about the first.
//
// The seeds are searched rather than hardcoded, for the reason
// `TestTheCriticalRuleIsReadFromThePacksTogglesAndNotFromGo` searches: a d20 reaches its
// maximum one time in twenty, and a golden seed is a value that changes whenever the
// pack's numbers do. **A search that found no 20 fails**, because a test that silently
// never reached the case is the shape of failure this file is written against.
func TestANaturalTwentyHitsUnderBothEditionsAndTheExemptionDoesNotMakeItMiss(t *testing.T) {
	t.Parallel()

	// Both editions through this package's own overlay seam. The editions themselves ship
	// as embedded files in `dnd5e/overlays`, and a test in *this* package cannot read them:
	// that package imports this one, so importing it back is an import cycle. The two
	// overlays below therefore restate the two rows this test is about — the toggle and the
	// `unconscious` row's `critical_exempt` — and nothing else, which is the same shape
	// every other edition test in this file uses.
	edition2024 := engineWithOverlay(t, `system: "dnd5e"
version: "test-2024-exemption@1"
title: "2024"
toggles:
  crit_attack_die_max: true
  crit_damage_die_max: true
  crit_ignored_by_incapacitated: true
conditions:
  - slug: unconscious
    label: "Unconscious"
    summary: "Not awake; cannot act."
    attack: advantage
    critical_exempt: true
`)

	edition2014 := engineWithOverlay(t, `system: "dnd5e"
version: "test-2014-exemption@1"
title: "2014"
toggles:
  crit_attack_die_max: true
  crit_damage_die_max: false
  crit_ignored_by_incapacitated: false
`)

	// Level 5, Strength 18: `prof` 3 plus a Strength modifier of +4, so a natural 20 is a
	// total of 27.
	attacker := aToken(t, "Vurg", 5,
		map[string]int{"strength": 18, "dexterity": 12},
		map[string]any{
			"attacks": []any{map[string]any{
				"name": "spear", "ability": "strength", "damage": "1d12+4",
			}},
			"ac": 0,
		})

	// Armour class 30 and a natural 20's total of 27: **no attack die this system can roll
	// beats this on the total**, so every hit in this test is an automatic one and the
	// comparison against armour class is inert by construction. Room to take the damage, and
	// the condition the exemption is read from.
	defender := aToken(t, "Wall", 3, map[string]int{"dexterity": 14},
		map[string]any{
			"ac": 30, "hp": 1000, "max_hp": 1000,
			"conditions": []string{"unconscious"},
		})

	if !edition2024.Pack().Toggle(toggleCritIgnoredByIncapacitated) {
		t.Fatal("the 2024 fixture did not switch the exemption on; the fixture is wrong")
	}

	if edition2014.Pack().Toggle(toggleCritIgnoredByIncapacitated) {
		t.Fatal("the 2014 fixture left the exemption on; the fixture is not a 2014 pack")
	}

	for _, testCase := range []struct {
		name         string
		engine       *Engine
		wantCritical bool
	}{
		// 2024 denies the *critical*, and says nothing about the hit.
		{name: "2024", engine: edition2024, wantCritical: false},
		// 2014 crits on the 20, and the hit was never in question.
		{name: "2014", engine: edition2014, wantCritical: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			naturalTwenties := 0

			for seed := range 200 {
				state := aState(t, map[rules.ObjectID][]byte{
					"attacker_1": attacker, "defender_1": defender,
				})

				mutations := resolve(t, testCase.engine, aTestCall(t, byte(seed)), state,
					attackOn(t, "attacker_1", "spear"))

				record := bodyOf(t, mutations[0].Args).LastRoll
				if record == nil || record.Natural == nil {
					t.Fatalf("seed %d: the attack recorded no natural", seed)
				}

				if *record.Natural != 20 {
					continue
				}

				naturalTwenties++

				if !record.Met {
					t.Errorf("seed %d: a natural 20 against an unconscious defender with an "+
						"unreachable armour class did not hit (total %d, target %d); a "+
						"maximum attack die lands whatever the critical rule said",
						seed, record.Total, record.Target)
				}

				hit, reached := mutationFor(t, mutations, "defender_1")
				if !reached {
					t.Errorf("seed %d: a natural 20 reached nobody; the defender took no damage, "+
						"so the auto-hit did not resolve", seed)

					continue
				}

				if bodyOf(t, hit.Args).HitPoints >= 1000 {
					t.Errorf("seed %d: the attack hit and dealt nothing", seed)
				}

				if got := record.Critical; got != testCase.wantCritical {
					t.Errorf("seed %d: a natural 20 against an unconscious defender critted=%v, "+
						"want %v", seed, got, testCase.wantCritical)
				}
			}

			if naturalTwenties == 0 {
				t.Fatalf("no seed in 0..199 rolled a natural 20, so this test never reached the " +
					"case it exists for; a search that finds nothing is a test that proves nothing")
			}
		})
	}
}
