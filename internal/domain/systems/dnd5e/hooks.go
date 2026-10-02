package dnd5e

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// # The escape hatch, and the test for whether a rule belongs in it
//
// §10.4 says the split is "a shared engine plus data packs plus **a narrow** Go escape
// hatch", and that the hooks are the rules "data genuinely cannot express" — naming
// two of them, *weapon mastery resolution* and *2024 crit*. This file is those two,
// and **it is two, not five**, and the reason there are only two is a rule with a
// test:
//
//   - **A rule became a hook because the thing it computes is a choice among pack
//     rows that the rows cannot rank between themselves.** Mastery is that: "the
//     highest of these the attacker also holds" is a fold over a partial order, and no
//     table row can answer it — every row is locally correct and none of them is the
//     answer. The *pairings* stayed data (`masteryRow.effects`), so adding a mastery
//     is a YAML edit.
//   - **A rule stayed data because the rows already ranked themselves.** That is the
//     critical rule's whole 2014-versus-2024 difference: 2014 crits on a natural 20
//     from the attack die, 2024 also crits when a damage die shows its maximum. One
//     boolean in `toggles` and **not** a second Go rule. The procedure is shared; only
//     the policy moved, so only the policy is data.
//
// The test that keeps the line honest is `TestEveryHookIsNamedInThePackAndThePackNamesNoOther`,
// which asserts the hook table has exactly the two entries `hookIDs` names and that
// every shipped pack names exactly those. A third hook would fail it, and the failure
// message is the question ADR 0045 asks. That is deliberate: adding a hook is a
// decision about the data/engine boundary, and a decision that should be visible in a
// diff rather than in a second entry in a map.
//
// # The pack chooses by name, and an unknown name is a load failure
//
// A pack's `hooks:` block names the rule for each hook. The name is resolved **once,
// at load**, and a name this build does not ship is refused rather than skipped.
//
// Skipping is the failure this exists to prevent. A pack naming `critical: crit-on-any-d20`
// against a build that has only `default` would otherwise resolve every attack **with
// no critical rule at all** and report success — a game where a natural 20 is an
// ordinary hit, discovered by a player, months later, as "crits feel broken". A gate
// that cannot detect this is the one outcome a ruleset fingerprint cannot cover: the
// pack loaded, so the fingerprint matched, so the resume was permitted.

// hookIDs is every hook this engine ships, and the pack must name a rule for each.
//
// **The complete list, asserted by a test.** `hookIDs` is the whole of "a narrow Go
// escape hatch", and a hook table with an entry nothing in `hookIDs` names — or an
// id with no entry — is a mismatch between the two answers that would otherwise be
// found by an attack that resolved without its rule.
var hookIDs = []string{
	// critical decides whether a resolved attack roll is a critical hit.
	critical,

	// mastery decides which weapon mastery an attack uses.
	mastery,
}

// The hook names, as the pack's `hooks:` block spells them.
//
// Constants rather than literals scattered through the loader, for the reason
// `rules.IsSemiplaneKind` is a switch: a hook's name appears in the loader, in the
// table, and in every pack, and three spellings of one name is three chances to
// disagree about which rule a pack asked for.
const (
	critical = "critical"
	mastery  = "mastery"
)

// defaultRuleName is the one rule every shipped pack names.
//
// A name and not "the rule", because a pack that names nothing is a pack this build
// cannot satisfy and saying so at load is the point. `defaultRule` appears in the
// base pack and in both overlays, and its presence in all three is what
// `TestEveryHookIsNamedInThePackAndThePackNamesNoOther` checks.
const defaultRuleName = "default"

// CriticalRule decides whether a resolved attack roll is a critical hit.
//
// One method, and the parameter is a **struct rather than four arguments** because
// the arguments are all "what the roll was", and a signature that grows a fifth is a
// breaking change for every rule written against it — ADR 0041's argument about
// `rules.System` applied to this interface.
type CriticalRule interface {
	// Critical reports whether this roll is a critical hit.
	Critical(in criticalInput) (bool, error)
}

// MasteryRule decides which weapon mastery an attack uses, and therefore which
// effects it carries.
type MasteryRule interface {
	// Mastery reports which of the granted masteries the attacker holds, and the
	// effects that mastery confers.
	Mastery(in masteryInput) (masteryChoice, error)
}

// criticalInput is what the critical rule reads.
//
// **The pack's toggles are reached through it rather than held by the rule**, and
// that is what keeps the 2014/2024 difference out of Go: the rule reads three
// booleans the pack sets, so a build with two policies has one rule.
type criticalInput struct {
	// pack is the effective pack, for its toggles.
	pack *Pack

	// natural is the attack die's face, from 1 to the primary die's faces.
	natural int

	// damageDice are the damage dice that were rolled, each carrying **its own face
	// count**, and is empty for an attack that rolled no damage dice.
	//
	// The face count is per-die rather than a single number because the 2024 critical
	// rule is "any damage die shows *its* maximum", and a greatsword's d6 showing 6 is
	// a critical while a d12 showing 6 is not. A single `[]int` of faces could not
	// express that at all — every entry would have to be compared against a maximum
	// the caller guessed — so the struct is what makes the rule correct rather than
	// approximately right.
	damageDice []rolledDie

	// defenderExempt reports whether the defender is in a condition the pack exempts
	// from the critical rule. 2024 adds that exemption for attacks against a
	// paralysed or unconscious creature; 2014 has none, so its overlay leaves the
	// toggle false.
	defenderExempt bool
}

// rolledDie is one die that was rolled: the face it showed, and how many faces it
// has.
type rolledDie struct {
	// Face is what it showed, from 1 to Faces.
	Face int

	// Faces is how many faces the die has, which is what "shows its maximum" is
	// compared against.
	Faces int
}

// masteryInput is what the mastery rule reads.
type masteryInput struct {
	// pack is the effective pack, for the mastery rows.
	pack *Pack

	// granted is what the weapon offers, best first, and is the weapon's own list.
	granted []string

	// held is what the attacker holds proficiency in.
	held []string

	// scores is the attacker's ability **scores** by slug, which is what a mastery
	// requirement is written against: "wieldable when the wielder's Strength is at
	// least 13" is a statement about the character sheet, not about a derived number.
	//
	// **Scores rather than modifiers**, and the difference is the whole reason this map
	// is not `masteryInput`'s old name. Comparing a *modifier* against a pack's stated
	// threshold is a unit error that resolves: a Strength 18 has a modifier of +4, which
	// is below 13, so a strong creature would be refused a heavy weapon and the game
	// would be quietly unplayable in exactly the case the rule is about. It is the kind
	// of bug no test written against the fixture would catch while the fixture's
	// creature was weak.
	scores map[string]int
}

// masteryChoice is what the mastery rule decided.
//
// **A struct rather than a bare bool**, because "which mastery" and "does the attacker
// meet its requirement" are two questions and the resolver asks both. Collapsing them
// would leave the second one asked against the *granted* mastery rather than the
// *chosen* one, which is a subtly wrong answer: a greatsword grants `great_weapon`,
// `heavy` and `two_handed`, an attacker holding only `great_weapon` does not get the
// heavy weapon, and a resolver that checked `heavy` against the granted list would say
// they did.
type masteryChoice struct {
	// Slug is the mastery chosen, empty when none applied.
	Slug string

	// Effects are that mastery's effects, in declaration order.
	Effects []effectFile

	// FellBack reports that the attacker held none of what the weapon granted, and the
	// attack resolved without a mastery. 2014's answer is this always, which is why
	// `MasteryEnabled` is checked before the rule is called rather than inside it.
	FellBack bool
}

// criticalRules is the set of critical rules this engine ships, by name.
//
// A map and not a slice, because a pack chooses **by name** and a name that resolves
// to nothing is a broken pack rather than a missing method. Read with `maps.Keys`
// and sorted rather than ranged over: see `slices.Sorted(maps.Keys(…))` in
// determinism.go's own table, which is the sanctioned spelling and is a range over a
// *slice*.
var criticalRules = map[string]CriticalRule{
	defaultRuleName: defaultCritical{},
}

// masteryRules is the set of mastery rules this engine ships, by name.
var masteryRules = map[string]MasteryRule{
	defaultRuleName: defaultMastery{},
}

// compileHooks resolves the pack's `hooks:` block into the rules a resolution calls.
//
// **Every hook must be named**, and a missing one is refused rather than defaulted.
// `ErrMissingHook` and `ErrUnknownHook` are two refusals rather than one because they
// are two different bugs: one is a pack that predates this hook existing, the other is
// a pack written against a build this is not. A pack author needs to know which.
func (p *Pack) compileHooks() error {
	p.rules.Critical = nil
	p.rules.Mastery = nil

	for _, id := range hookIDs {
		chosen, declared := p.hooks[id]
		if !declared {
			return fmt.Errorf(
				"%w: no rule is named for %q, and this engine ships exactly one per hook",
				ErrMissingHook,
				id,
			)
		}

		if err := p.bindHook(id, chosen); err != nil {
			return err
		}
	}

	return nil
}

// bindHook resolves one hook name.
//
// Two tables and a `switch` rather than one table of a union, and the reason is that
// the two rule interfaces share no method: a single table would have to hold
// `any`, and `any` here is exactly what ADR 0041 argues against for `Expr.Node` —
// a value two things could both appear to understand.
func (p *Pack) bindHook(id, chosen string) error {
	switch id {
	case critical:
		rule, shipped := criticalRules[chosen]
		if !shipped {
			return fmt.Errorf(
				"%w: %q is not one of %v",
				ErrUnknownHook,
				chosen,
				sortedNames(criticalRules),
			)
		}

		p.rules.Critical = rule

		return nil
	case mastery:
		rule, shipped := masteryRules[chosen]
		if !shipped {
			return fmt.Errorf(
				"%w: %q is not one of %v",
				ErrUnknownHook,
				chosen,
				sortedNames(masteryRules),
			)
		}

		p.rules.Mastery = rule

		return nil
	default:
		// Unreachable through `hookIDs`, which `compileHooks` walks exhaustively and
		// which a test pins. Present because a function with an implicit zero return
		// is a function whose failure is invisible.
		return fmt.Errorf("%w: %q is not a hook this engine knows", ErrUnknownHook, id)
	}
}

// sortedNames returns a table's keys in sorted order, for a refusal message.
//
// **Sorted**, because the message goes to an operator reading a log and a list whose
// order differs between runs is a list they cannot act on — which is the same reason
// `realtime.componentOrder` is fixed and `audit_version.go` fixes its own.
func sortedNames[V any](table map[string]V) []string {
	return slices.Sorted(maps.Keys(table))
}

// defaultCritical is the one critical rule this engine ships.
//
// **The whole of the 2014-versus-2024 difference is in what it reads**, and this is
// the sentence ADR 0045 exists to make: there is one rule, and the editions differ by
// two of its four inputs. That is what makes the overlay a reviewable data diff rather
// than a Go fork, and it is why this type has no edition in its name.
//
// A second rule would only be right if the *procedure* differed — if 2014 needed to
// ask a question 2024 does not. It does not: both ask "did the attack die show its
// maximum, and did a damage die show its maximum", and both consult the defender's
// exemption.
type defaultCritical struct{}

// Critical reports whether the roll is a critical hit.
//
// Three independent questions, answered in a fixed order, and **the order is the
// rule's readability rather than its result**: the exemption first because it is a
// single boolean and it can end the question, then the attack die, then the damage
// dice. Short-circuiting in a different order would give the same answer; writing it
// longest-first would make a reader wonder why the exemption is checked against
// damage dice.
func (defaultCritical) Critical(given criticalInput) (bool, error) {
	if given.pack == nil {
		return false, errors.New("dnd5e: the critical rule was asked with no pack")
	}

	// The exemption, and it is checked **first** because it is the only one that can
	// make the answer `false` no matter what the dice did. A 2024 pack sets the toggle
	// and exempts attacks against a paralysed or unconscious creature entirely; a 2014
	// pack leaves it false and no attack is exempt.
	if given.defenderExempt && given.pack.Toggle(toggleCritIgnoredByIncapacitated) {
		return false, nil
	}

	faces, _ := given.pack.PrimaryDie()

	// The attack die at its maximum. `given.natural <= 0` is the "no attack die was rolled"
	// case, which a resolution that called the rule without rolling should not be able
	// to make a critical by accident — and the refusal is `false` rather than an error
	// because the caller has a roll it believes in and a rule that disagrees about its
	// shape is not a pack's problem.
	if given.natural > 0 && given.natural == faces && given.pack.Toggle(toggleCritAttackDieMax) {
		return true, nil
	}

	// A damage die at its **own** maximum, which is the 2024 half of the difference.
	//
	// The walk is over a slice and not a map, and it is the only loop in this rule: the
	// damage dice were appended in the order the damage expression wrote them, so two
	// runs of the same attack under the same seed see them in the same order. A
	// `break` on the first critical die rather than a counter, because the answer is a
	// `bool` and the first is as true as the second.
	//
	// `given.pack.Toggle` rather than a field, so that this line and the two above it read
	// as three policy questions with one procedure behind them — which is the whole
	// claim this file makes about where the line sits.
	if !given.pack.Toggle(toggleCritDamageDieMax) {
		return false, nil
	}

	for _, die := range given.damageDice {
		if die.Faces > 1 && die.Face == die.Faces {
			return true, nil
		}
	}

	return false, nil
}

// defaultMastery is the one mastery rule this engine ships.
//
// **A fold over a partial order**, which is the entire reason this is a hook. "The
// highest of the masteries a weapon grants that the attacker holds" compares two
// ordered lists, and no row of either can answer it.
type defaultMastery struct{}

// Mastery reports the best granted mastery the attacker holds.
//
// Five steps in a fixed order, and each is a question the *pack* cannot answer:
// which masteries the weapon granted (the weapon's list), which of those the attacker
// holds (a membership test over two lists), which is highest (the fold), whether the
// attacker's **scores** meet that mastery’s requirement (an effect's payload against
// the attacker's scores), and what it therefore confers.
//
// **The requirement check is against the *chosen* mastery and not the granted list**,
// and that is the subtlety the `masteryChoice` comment names: a greatsword grants
// `great_weapon`, `heavy` and `two_handed`, so an attacker holding only `great_weapon`
// must not get the heavy weapon's effects. `TestAMasteryRequirementIsCheckedAgainstTheChosenMasteryNotTheGrantedList`
// is what holds it.
func (defaultMastery) Mastery(given masteryInput) (masteryChoice, error) {
	if given.pack == nil {
		return masteryChoice{}, errors.New("dnd5e: the mastery rule was asked with no pack")
	}

	// The fold. `given.granted` is the weapon's own order, best first, so the first
	// granted mastery the attacker holds *is* the highest one they hold — which is why
	// this is a `break` and not a maximum: the order is the weapon's, and a rule that
	// searched for the "best" would need to know what best means for every pair, which
	// is a rule the pack would then have to declare.
	for _, slug := range given.granted {
		if !slices.Contains(given.held, slug) {
			continue
		}

		row, declared := given.pack.MasteryAt(slug)
		if !declared {
			// A weapon granting a mastery the pack does not declare. Refused rather than
			// skipped, and for the reason `ErrUnknownHook` gives: skipping would resolve
			// the attack with no mastery at all and report success.
			return masteryChoice{}, fmt.Errorf("%w: the weapon grants %q", ErrNoSuchMastery, slug)
		}

		if !meetsRequirements(row, given.scores) {
			// Held but not qualified — a light weapon in a strong creature's hands, or a
			// heavy one in a weak one's. **Not an error**: the attack still happens, and
			// an attack with no applicable mastery is an ordinary attack. The alternative
			// — refusing — would mean a GM holding a `light` mastery on a creature who
			// outgrew it cannot attack at all, which is a rules bug presenting as a
			// permission problem and would be reported as the latter.
			return masteryChoice{FellBack: true}, nil
		}

		return masteryChoice{Slug: row.slug, Effects: slices.Clone(row.effects)}, nil
	}

	return masteryChoice{FellBack: true}, nil
}

// meetsRequirements reports whether an attacker's ability **scores** satisfy a
// mastery's requirements.
//
// **Scores, not modifiers**, and the unit is the whole of it: a pack says "wieldable when
// the wielder's Strength is at least 13", and 13 is a number on a character sheet. A
// modifier for that Strength is 4, which is below 13, so a comparator over modifiers
// refuses the heavy weapon to *every* creature below Strength 22 and the rule is
// silently inverted for the strong ones too. `TestAMasteryRequirementIsCheckedAgainstTheChosenMasteryNotTheGrantedList`
// holds the half that a weak-creature fixture cannot: the strong case.
//
// **Only the `requires_ability_*` effects are requirements**, and the effect kinds are
// a closed validated set — so this switch has no default arm that could hide a new
// kind, and `TestAModifierIsCodeRatherThanAPackConstant`'s sibling
// `TestTheEffectVocabularyIsClosedInBothDirections` is what holds `effectKinds` and this
// function in step. A kind added to `effectKinds` without being classified here would be
// silently inert, and that is the shape of failure this file's whole argument is about:
// a rule that quietly does nothing.
func meetsRequirements(row masteryRow, scores map[string]int) bool {
	for _, effect := range row.effects {
		switch effect.Kind {
		case effectRequiresAbilityAtLeast:
			if scores[effect.Ability] < effect.Value {
				return false
			}
		case effectRequiresAbilityBelow:
			if scores[effect.Ability] >= effect.Value {
				return false
			}
		case effectAddAbilityToDamage, effectDamageAbilityFromBest:
			// Not requirements. Listed so the switch is **exhaustive** over `effectKinds`
			// rather than silently ignoring a kind it does not recognise: a new effect
			// kind added to the vocabulary without being classified here would be treated
			// as a requirement-less mastery and would apply unconditionally, which is the
			// opposite failure from the one `TestTheEffectVocabularyIsClosedInBothDirections`
			// guards and is just as quiet.
		}
	}

	return true
}
