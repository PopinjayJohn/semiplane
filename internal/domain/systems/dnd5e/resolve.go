package dnd5e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// The argument shapes, one per operation.
//
// and deliberately the same words the pack and the character sheet use: they are
// and a second spelling would be a second answer to "what does this op take".
// **A typed struct per op, decoded strictly**, for the reason `decodeArgs` gives: an
// resolution would report a number that means nothing.
// Every field is documented because these are the wire's vocabulary for this system —
// what a UI plugin's dice roller, its attack widget and its token list each send. The
// JSON names are part of the protocol and renaming one is a breaking change for every
// client, which is the same standing `realtime.Op` has.

// rollArgs are `roll`'s arguments.
type rollArgs struct {
	// Expr is the notation to resolve, and is the only required field.
	Expr string `json:"expr"`

	// Label is what the player called the roll. **Never a rule input** — it is
	// recorded on the creature so a client can show "Perception" next to the number,
	// and it is absent-by-default rather than empty-by-default so a roll log does not
	// read " for 14".
	Label string `json:"label,omitempty"`

	// DC is the difficulty class the roll was measured against, zero for none. Zero is
	// the right default because **a DC of zero is not a thing** — the lowest DC a pack
	// declares is a positive number — so absence and "no DC" are the same value and
	// there is no need for a pointer to tell them apart.
	DC int `json:"dc,omitempty"`
}

// attackArgs are `attack`'s arguments.
//
// **`Defender` names a second game object, which is not a second claim about the
// target.** `rules.Intent` says `Target` is "the only address the hub honours" and
// that a system whose arguments repeat a placement id "is describing a second address"
// — and here that second address is a *different* object, not a contradicting one. The
// rule the field comment names is about two claims on the *same* object; an attack has
// two participants and the hub applies the two mutations to the two targets, which is
// exactly what `rules.Mutation.Target` is for.
type attackArgs struct {
	// Attack is the name of one of the attacker's declared attacks.
	Attack string `json:"attack"`

	// Defender is the game object being attacked.
	Defender rules.ObjectID `json:"defender"`
}

// healArgs are `heal`'s arguments.
type healArgs struct {
	// Amount is how many hit points to restore. **Zero is legal** — a GM healing
	// nothing is a legal act and refusing it would be a rule about nothing.
	Amount int `json:"amount"`
}

// conditionArgs are `apply_condition`'s and `clear_condition`'s arguments.
type conditionArgs struct {
	// Condition is a slug the pack declares.
	Condition string `json:"condition"`
}

// sends and the words a character sheet uses.
//
// sends and the words a character sheet uses.
//
// sends and the words a character sheet uses.
//
// sends and the words a character sheet uses.
//
// sends and the words a character sheet uses.
//
// sends and the words a character sheet uses.
//
// sends and the words a character sheet uses.
//
// statusArgs are `apply_status`'s arguments, and **every field is optional**.
//
// One operation rather than four because §7.2's adjudication is one act: a GM who
// brings a creature from zero to full and marks it exhausted is doing one thing, and a
// client that had to decide which of four ops to send would have to ask the server which
// fields it was going to honour. Every field is a pointer so that "not stated" and
// "stated as zero" are different — a GM setting hit points *to* zero and a GM saying
// nothing about hit points are different acts, and a plain `int` cannot tell them.
//
//nolint:tagliatelle // `max_hp` and `clear_condition` are the words a GM's client
//nolint:tagliatelle // `max_hp` and `clear_condition` are the words a GM's client
//nolint:tagliatelle // `max_hp` and `clear_condition` are the words a GM's client
//nolint:tagliatelle // `max_hp` and `clear_condition` are the words a GM's client
//nolint:tagliatelle // `max_hp` and `clear_condition` are the words a GM's client
//nolint:tagliatelle // `max_hp` and `clear_condition` are the words a GM's client
//nolint:tagliatelle // `max_hp` and `clear_condition` are the words a GM's client
type statusArgs struct {
	HitPoints      *int   `json:"hp,omitempty"`
	MaxHitPoints   *int   `json:"max_hp,omitempty"`
	Speed          *int   `json:"speed,omitempty"`
	Condition      string `json:"condition,omitempty"`
	ClearCondition string `json:"clear_condition,omitempty"`
}

// stated reports whether the GM stated anything at all.
//
// **A refusal rather than a silent no-op**, and that is the difference this method
// exists for: an `apply_status` that changed nothing would return no mutations, and
// `rules.System.Apply` says that is "a legitimate answer" for an intent that resolved
// to nothing changed — so a client whose arguments were all mistyped would see a
// successful resolution that did nothing. Refusing names the problem.
func (s statusArgs) stated() bool {
	return s.HitPoints != nil || s.MaxHitPoints != nil || s.Speed != nil ||
		s.Condition != "" || s.ClearCondition != ""
}

// # How a draw is labelled
//
// `Context.Rand(label)` is pure in (seed, label), so two resolutions of the same
// intent under the same seed draw the same numbers **exactly when their labels are
// equal**. The label is therefore a function of what the draw *is* and of nothing else:
//
//   - Never a counter. `attack/2` would make the third attack of a campaign depend on
//     the two before it, and S-14.6 is about a resolution being a function of
//     (state, intent, seed) and not of anything that came earlier in the session.
//   - Including the **creature ids** and, for an attack, the attack's name — so two
//     attacks on the same creature from the same intent text are separate draws.
//   - Including a **digest of the expression** rather than the expression, because a
//     pack revision may re-spell an expression without changing what it means. Two
//     numerically identical damage expressions then draw the same numbers, which is
//     what §16.3's roll log wants; see `Expression.Digest`.
//
// The consequence worth stating: **two draws in one resolution get two labels.** An
// attack draws its attack die and, on a hit, its damage dice, and those are two labels
// because they are two unrelated streams. `TestTwoDrawsInOneResolutionAreUnrelated`
// is what holds it.

// oneDie rolls `count` dice of `faces` and returns the kept face, applying the mode.
//
// **Advantage rolls twice and keeps one, and the kept die is the only one reported.**
// That detail is a rule, not an implementation: 5e crits on a natural 20 from the
// *attack roll*, and under disadvantage an attack roll of 1 after a 20 is not a 20. So
// the discarded die is discarded entirely — including for the critical check — which is
// why this returns an `int` and not a `[]int`.
//
// **Advantage applies to a single-die group only**, and the limitation is stated rather
// than approximated: 5e's advantage is a rule about one d20, and applying it to `2d6`
// would have to mean "roll 4d6 and keep the best 2", which is a *different* question
// with a different answer under a different set of rules. So a multi-die group rolls as
// written and the mode is ignored for it — and a test asserts that, because an
// advantage that silently applied where it should not is worse than one that never did.
func oneDie(count, faces int, mode Advantage, source drawSource) int {
	if count == 1 && mode != AdvNone {
		first := 1 + source.IntN(faces)
		second := 1 + source.IntN(faces)

		if mode == AdvGains {
			return max(first, second)
		}

		return min(first, second)
	}

	kept := 0

	for range count {
		kept += 1 + source.IntN(faces)
	}

	return kept
}

// drawSource is the deterministic source one labelled draw is taken from.
//
// **A type alias rather than `*rand.Rand` throughout**, so that the label a draw is
// filed under and the source it came from cannot be confused: every constructor takes
// the label, and there is no way to reach a source without stating it. That is the
// mechanism S-10.4's answer to ambient randomness relies on — the sanctioned call is
// the only call — and P1d's lint rule is what refuses the other one.
//
// It is `math/rand/v2`'s `*rand.Rand` and not a narrow `Roll` interface, for the
// reason `rules.Context.Rand` gives: an interface with `Roll` and `Sum` would be a
// dice vocabulary in this package, and §10.3's requirement is that **the protocol
// never assumes d20**. Importing the package is not what S-10.4 forbids — it is the
// one `rand` package rule code may import, because the seeded source has to be
// constructed from somewhere and `rules.Context.Rand` is what constructs it — and
// what is forbidden is this file calling a **package-level** function of it, which
// `determinism` reads out of the type information rather than out of the spelling.
type drawSource = *rand.Rand

// newDraw returns a source for one labelled draw.
//
// **The label is an argument, not an afterthought**, and that is the whole design:
// there is no `newDraw()` with no argument, so there is no call site that could take a
// source without naming what it drew. `call.Rand` is the only randomness a resolution
// has, and this is the one place it is reached.
func newDraw(call rules.Context, label string) drawSource {
	return call.Rand(compileID(label))
}

// resolveRoll resolves `roll`: an expression against a DC, recorded on the creature.
//
// **One mutation, on the rolling creature.** §10.7's worked consequence for the
// graphical dice roller is "on send, emits `{"op":"roll"}` and renders the server's
// result", and the result has to reach every client at the table. `rules.NewMutation`
// requires a target, and a roll names no object of its own — so the answer travels on
// the creature whose token every client already holds.
//
// The roll record is a `LastRoll` field on the creature rather than a second mutation,
// because a mutation's payload is opaque and uninterpreted by semiplane: a *second*
// object to hang it on would need a placement, and semiplane has no such thing.
func (e *Engine) resolveRoll(
	_ context.Context,
	call rules.Context,
	state rules.State,
	intent rules.Intent,
) ([]rules.Mutation, error) {
	var args rollArgs
	if err := decodeArgs(intent, &args); err != nil {
		return nil, err
	}

	subject, err := e.actingCreature(call, state, intent)
	if err != nil {
		return nil, err
	}

	expression, err := parseExpr(e.pack, args.Expr)
	if err != nil {
		return nil, err
	}

	// Advantage comes from the **pack's condition rows**, not from the arguments: a
	// blinded creature rolls with disadvantage because its token says `blinded` and the
	// pack says what `blinded` does to an attack. That is §10.4's whole claim in one
	// line — the table is data and the resolver reads it — and it is why no part of this
	// function knows what any particular condition *is*.
	mode := Cancel(attackModes(e.pack, &subject)...)

	// One draw under one label, and the mode is an **argument to the roll** rather
	// than a second draw: `Expr.Roll` applies it to a single-die group and ignores it
	// for a multi-die group, exactly as `oneDie` documents. Rolling the attack die
	// once to learn `natural` and then rolling the whole expression again — the shape
	// this function was cut off with — would spend two streams on one question and
	// throw the first away, and the surviving `natural` would be recomputed from the
	// parts anyway.
	total, parts, _ := expression.Roll(
		newDraw(call, rollLabel(intent, expression, mode)), mode,
	)

	// `Natural` is a pointer for the reason `Expr.Natural` gives, and it is derived
	// from the **parts** rather than rolled again: the dice came first in `parts` and
	// the modifiers after them, so the dice are exactly the leading run, and
	// `countModifiers` is how many of them the expression wrote.
	record := rollRecord{
		Expr:      expression.String(),
		Label:     args.Label,
		Total:     total,
		Parts:     parts,
		Advantage: advantageScore(mode),
		Target:    args.DC,
		Met:       args.DC > 0 && total >= args.DC,
	}

	if expression.HasDice() {
		natural := 0

		for _, part := range parts[:len(parts)-countModifiers(expression)] {
			natural += part
		}

		record.Natural = &natural
	}

	subject.LastRoll = &record

	mutation, err := rules.NewMutation(intent.Target, OpRoll, encodeCreature(&subject))
	if err != nil {
		return nil, fmt.Errorf("dnd5e: stating %q on %q: %w", OpRoll, intent.Target, err)
	}

	return []rules.Mutation{mutation}, nil
}

// countModifiers returns how many trailing parts of a breakdown are modifiers rather
// than dice.
//
// **Derived from the parsed expression and not from the parts**, because the parts are
// a flat `[]int` with no tags and inferring which entries are dice from their magnitude
// would be a guess — `1d6` showing 6 and a `+6` modifier are the same number.
func countModifiers(expression Expr) int { return len(expression.flat) }

// rollLabel names a `roll`'s draw.
//
// **The advantage is part of the label**, and that is a rule rather than an
// implementation detail: rolling with advantage draws *two* dice and rolling normally
// draws one, so the same expression under the two modes is two different draws and must
// be two different streams. Leaving it out would make a player's second roll — after a
// condition cleared — draw the first one's numbers.
func rollLabel(intent rules.Intent, expression Expr, mode Advantage) string {
	return compileID(
		string(OpRoll),
		intent.Target.String(),
		expression.Digest(),
		string(mode),
	)
}

// attackRollLabel names the attack die's draw.
//
// **Two draws in one resolution get two labels**, and this is the other half of the
// pair with `damageLabel`: an attack that lands draws its attack die and its damage
// dice, and they are unrelated streams because they answer different questions.
// `TestTwoDrawsInOneResolutionAreUnrelated` is what holds it — two attacks that differ
// only in which dice they rolled must not share a stream, or a replay would draw the
// attack die from the damage die's numbers.
//
// The defender is in the label because an attack against a different creature is a
// different attack, and the mode because advantage draws two dice where one is drawn
// otherwise. Neither is a counter: both are what the draw *is*.
func attackRollLabel(intent rules.Intent, args attackArgs, mode Advantage) string {
	return compileID(
		string(OpAttack),
		intent.Target.String(),
		args.Defender.String(),
		args.Attack,
		string(mode),
	)
}

// damageLabel names the damage dice's draw.
//
// **The trailing `damage` is what separates it from `attackRollLabel`**, and the
// expression's digest is what makes a pack revision harmless: two numerically
// identical damage expressions draw the same numbers however they are spelled, which
// is what §16.3's roll log wants and what `rollLabel` also relies on.
func damageLabel(intent rules.Intent, args attackArgs, expression Expr) string {
	return compileID(
		string(OpAttack),
		intent.Target.String(),
		args.Defender.String(),
		args.Attack,
		"damage",
		expression.Digest(),
	)
}

// attackModes returns what this creature's conditions do to its own rolls.
//
// **One row per condition, in the pack's declaration order**, and the order matters
// only in that it is *fixed*: `Cancel` is a count, so any order gives the same answer,
// and the pack's order is used so the walk is deterministic regardless. A creature
// carrying two disadvantages has disadvantage, not "disadvantage twice".
func attackModes(pack *Pack, subject *creature) []Advantage {
	var modes []Advantage

	for _, condition := range pack.Conditions() {
		if subject.hasCondition(pack, condition.slug) {
			modes = append(modes, condition.attack)
		}
	}

	return modes
}

// resolveAttack resolves `attack`: the flagship rule, and the one §10.4's split is
// about.
//
// **Two mutations, on two objects, under two op names** — `attack` on the attacker
// carrying the roll, and `set_hit_points` on the defender carrying the damage. That is
// `rules.Mutation`'s rule made concrete: one intent may resolve to several mutations
// and reusing the intent's name for each would put a second record of the same fact on
// the wire. A client reconciling the two reads *two different facts*.
//
// The steps, and which of them is data and which is a hook:
//
//  1. Read the attacker's declared attack — **the creature's data**.
//  2. Draw the attack die, applying advantage from the pack's condition rows on **both**
//     participants — **data**, because every condition row says what it does.
//  3. Decide the hit: a maximum attack die hits whatever else is true, and otherwise
//     the total is compared against the defender's armour class, from the pack's
//     formula — **data**.
//  4. Resolve the weapon's mastery — **a hook**, because it is a fold over two ordered
//     lists.
//  5. Roll the damage dice and ask the critical rule whether the attack was a critical
//     hit — **a hook**, and both editions' policy in one place.
//  6. Apply the damage, clamped at zero hit points.
//
// # The hit and the critical are two questions, and this file used to ask them as one
//
// **Asking the critical rule whether the attack landed was wrong, and the shape of the
// bug is worth recording because it is quiet.** `defaultCritical` short-circuits on the
// defender's exemption — 2024's `crit_ignored_by_incapacitated` answers `false` before
// it consults the attack die at all — so a resolver that took the rule's answer as "it
// hit" made a **natural 20 miss** against an unconscious, paralysed or incapacitated
// defender under 2024, while the identical roll auto-hit under 2014 and against any
// unexempted target. Measured, before this was fixed: `ac: 30`, `unconscious`, a natural
// 20, a total of 21, `met: false` and no hit.
//
// The rule itself was never wrong. A natural 20 is an automatic critical hit in 5e, and
// 2024's change is that it does **not** automatically count as a *critical hit* against an
// incapacitated target — which is a statement about the damage dice, not about whether
// the sword lands. So the exemption governs what a 20 does to the dice (below, in
// `resolveDamage`) and **nothing else**.
//
// `TestANaturalTwentyHitsUnderBothEditionsAndTheExemptionDoesNotMakeItMiss` is what
// holds it, and it is a table over both editions because either one alone is satisfiable
// by a constant.
func (e *Engine) resolveAttack(
	_ context.Context,
	call rules.Context,
	state rules.State,
	intent rules.Intent,
) ([]rules.Mutation, error) {
	var args attackArgs
	if err := decodeArgs(intent, &args); err != nil {
		return nil, err
	}

	attacker, err := e.actingCreature(call, state, intent)
	if err != nil {
		return nil, err
	}

	defender, err := e.defendingCreature(state, args.Defender)
	if err != nil {
		return nil, err
	}

	attack, found := attacker.attackByName(args.Attack)
	if !found {
		return nil, fmt.Errorf(
			"%w: %q has none called %q",
			ErrNoSuchAttack,
			intent.Target,
			args.Attack,
		)
	}

	if _, declared := e.pack.AbilityAt(attack.Ability); !declared {
		return nil, fmt.Errorf(
			"%w: attack %q names %q",
			ErrNoSuchAbility,
			attack.Name,
			attack.Ability,
		)
	}

	attackerScope, err := e.scope(&attacker)
	if err != nil {
		return nil, err
	}

	e.dcScope(attackerScope)

	defenderScope, err := e.scope(&defender)
	if err != nil {
		return nil, err
	}

	e.dcScope(defenderScope)

	defence, err := e.armourClass(&defender, defenderScope)
	if err != nil {
		return nil, err
	}

	bonus, err := e.evaluateFor("attack_bonus", attack.Ability, attackerScope)
	if err != nil {
		return nil, err
	}

	// The mode is a fold over **both** participants' condition rows, and cancelling is
	// 5e's rule rather than a column: a creature that is invisible *and* prone has an
	// advantage and a disadvantage on the same roll, and the answer is neither.
	mode := Cancel(
		append(attackModes(e.pack, &attacker), againstModes(e.pack, &defender)...)...,
	)

	natural := oneDie(
		e.pack.primaryCount,
		e.pack.primaryFaces,
		mode,
		newDraw(call, attackRollLabel(intent, args, mode)),
	)

	total := natural + bonus + attack.AttackBonus
	exempt := exemptFromCritical(e.pack, &defender)

	record := rollRecord{
		Expr:      attack.Name,
		Label:     "attack",
		Total:     total,
		Natural:   &natural,
		Advantage: advantageScore(mode),
		Target:    defence,
		Met:       e.hitsOnFace(natural) || total >= defence,
	}

	// A miss is a complete resolution and produces one mutation. The alternative —
	// returning nothing — would be indistinguishable from an intent that changed
	// nothing, and a player watching their attack fizzle needs the roll recorded.
	if !record.Met {
		attacker.LastRoll = &record

		return e.singleMutation(intent.Target, OpAttack, &attacker)
	}

	damage, critical, err := e.resolveDamage(
		call,
		intent,
		args,
		&attacker,
		attackerScope,
		natural,
		exempt,
	)
	if err != nil {
		return nil, err
	}

	record.Critical = critical

	attacker.LastRoll = &record

	// Damage is applied and clamped at zero, because a creature reduced below zero is
	// not a state a token can hold: a client rendering "hit points: -4" is showing a
	// number no rule in this system produced.
	damage = min(damage, defender.HitPoints)

	defender.HitPoints -= damage
	defender.pruneConditions(e.pack)

	mutations, err := e.singleMutation(intent.Target, OpAttack, &attacker)
	if err != nil {
		return nil, err
	}

	hurt, err := rules.NewMutation(args.Defender, OpSetHitPoints, encodeCreature(&defender))
	if err != nil {
		return nil, fmt.Errorf("dnd5e: stating %q on %q: %w", OpSetHitPoints, args.Defender, err)
	}

	return append(mutations, hurt), nil
}

// againstModes returns what this creature's conditions do to rolls made *against* it.
//
// The mirror of `attackModes`, and a separate function rather than a flag on that one
// because a condition row has **two** columns and both are read: a prone creature
// attacks with disadvantage *and* is attacked with advantage, and one of those is not
// the negation of the other.
func againstModes(pack *Pack, subject *creature) []Advantage {
	var modes []Advantage

	for _, condition := range pack.Conditions() {
		if subject.hasCondition(pack, condition.slug) {
			modes = append(modes, condition.attacksAgainst)
		}
	}

	return modes
}

// exemptFromCritical reports whether the pack exempts attacks against this creature
// from the critical rule.
//
// **Read from the pack's own toggles through the hook**, and named for what it is: the
// condition that makes a creature exempt is a *condition row* that names it, so this is
// a lookup over `condition.critical_exempt` rather than a hardcoded list of "paralysed"
// and "unconscious". A 2014 overlay that leaves the field unset gets no exemption from
// anywhere, and a 2024 one that sets it gets it without this package naming a single
// condition.
func exemptFromCritical(pack *Pack, subject *creature) bool {
	for _, condition := range pack.Conditions() {
		if condition.criticalExempt && subject.hasCondition(pack, condition.slug) {
			return true
		}
	}

	return false
}

// hitsOnFace reports whether a maximum attack die lands whatever the armour class.
//
// **A resolver rule, and deliberately not a hook and not the critical rule.** Three
// reasons, and the first is the bug this function exists to fix:
//
//   - **The critical rule's answer is not the hit's answer.** `defaultCritical`
//     short-circuits on 2024's `crit_ignored_by_incapacitated`, so asking it "did it
//     hit?" made a natural 20 *miss* against an unconscious defender. The exemption is
//     about whether a 20 counts as a critical hit — what it does to the damage dice —
//     and says nothing about whether the attack lands.
//   - **No toggle governs it.** `crit_attack_die_max` switches the *critical* off, and
//     `TestTheCriticalRuleIsReadFromThePacksTogglesAndNotFromGo` asserts a pack with it
//     off never crits — including on a 20. Reading the toggle here would make that
//     "never crits" into "never hits", which is a different claim and a wrong one.
//   - **It is 5e's automatic critical hit**, which is a statement about the attack roll
//     and not about the dice that follow, so it is shared by both editions and is
//     therefore not what separates them.
//
// **One die only**, and the count is read for the reason `oneDie` gives: `natural` is a
// **sum** when the pack's primary die draws more than one, and a sum of two d20s is not a
// natural 20. A pack that rolls `2d10` as its attack die has no face value at all, so
// there is nothing for this to answer and the comparison is against the armour class
// alone.
//
// `natural > 0` is the "no attack die was rolled" guard, for the reason
// `defaultCritical` keeps: a resolution that reached here without rolling must not
// acquire an automatic hit from a zero.
func (e *Engine) hitsOnFace(natural int) bool {
	faces, count := e.pack.PrimaryDie()

	return count == 1 && natural > 0 && natural == faces
}

// resolveDamage rolls a hit's damage and reports whether it was a critical hit.
//
// **The damage draw happens only on a hit**, and that is what makes the draw count a
// function of the state rather than of the intent: a missed attack draws no damage
// dice, so the attack die is the only stream it touched, and a campaign replaying it
// draws the same nothing.
func (e *Engine) resolveDamage(
	call rules.Context,
	intent rules.Intent,
	args attackArgs,
	attacker *creature,
	scope map[string]int,
	natural int,
	exempt bool,
) (damage int, critical bool, err error) {
	attack, found := attacker.attackByName(args.Attack)
	if !found {
		// Unreachable: the caller already found it, and `rules.Object.Data` cannot have
		// changed between the two reads because nothing wrote to it. Present because a
		// silent zero here would be a damage of zero on an attack that hit.
		return 0, false, fmt.Errorf(
			"%w: %q has none called %q",
			ErrNoSuchAttack,
			intent.Target,
			args.Attack,
		)
	}

	choice := masteryChoice{FellBack: true}

	if e.pack.MasteryEnabled() {
		choice, err = e.rules.Mastery.Mastery(masteryInput{
			pack:    e.pack,
			granted: attack.Mastery,
			held:    attacker.Masteries,
			scores:  attackerScores(attacker, e.pack),
		})
		if err != nil {
			return 0, false, fmt.Errorf("dnd5e: the mastery rule on %q: %w", args.Attack, err)
		}
	}

	// The mastery decides *which* ability the damage uses, and that is the effect a
	// finesse weapon grants. It is a hook's answer because the choice is a comparison
	// over two pack rows; the *pairing* — that finesse means "the better of these two" —
	// is a row's payload.
	ability := damageAbility(attack.Ability, choice, attackerModifiers(attacker, e.pack))

	expression, err := parseExpr(e.pack, attack.Damage)
	if err != nil {
		return 0, false, fmt.Errorf(
			"%w: attack %q has damage %q: %w",
			ErrRoll,
			attack.Name,
			attack.Damage,
			err,
		)
	}

	// The damage draw takes **no advantage**, and that is 5e's rule rather than an
	// omission: advantage is a property of the *attack roll*, and applying it to a
	// greatsword's two d6 would mean rolling four and keeping the best two, which is a
	// different question under a different set of rules. `Expr.Roll`'s own comment says
	// the same thing about a multi-die group, and this is the case where it holds for
	// the group's benefit rather than as a limitation.
	rolled, parts, dice := expression.Roll(
		newDraw(call, damageLabel(intent, args, expression)), AdvNone,
	)

	// The critical rule, consulted **again** now that the damage dice exist. 2014's
	// toggle is false for the damage half, so this call returns what the first one did;
	// 2024's is true, so a damage die at its own maximum is a critical.
	critical, err = e.rules.Critical.Critical(criticalInput{
		pack: e.pack, natural: natural, damageDice: dice, defenderExempt: exempt,
	})
	if err != nil {
		return 0, false, fmt.Errorf("dnd5e: the critical rule on %q's damage: %w", args.Attack, err)
	}

	rolledTotal := rolled

	if critical {
		// **The dice are multiplied and the modifiers are not.** That is 5e's rule and
		// it is why the multiplier is applied to `parts` up to the first modifier rather
		// than to the total: doubling a +3 ability bonus because the attack crits is a
		// defect a table would notice immediately, and one this slice must not have.
		rolledTotal = 0

		for idx, part := range parts {
			if idx < expression.DiceCount() {
				rolledTotal += part * multiplier(e.pack)

				continue
			}

			rolledTotal += part
		}
	}

	bonus, err := e.evaluateFor("damage_bonus", ability, scope)
	if err != nil {
		return 0, false, err
	}

	total := rolledTotal + bonus

	// The mastery's own contributions, **after** the pack's `damage_bonus` formula and
	// before the cap.
	//
	// `add_ability_to_damage` reads `scope[scopePrefixAbility+ability]` — the ability the
	// damage was **rolled with**, which is the mastery hook's choice and not the
	// attack's. That distinction is the whole effect: a finesse two-handed weapon rolls
	// damage with the better of two abilities, and a resolver reading the attack's own
	// ability here would add the worse one. It is a Go rule and not a column because it
	// is a fold over what the hook already decided — see ADR 0045.
	for _, effect := range choice.Effects {
		if effect.Kind == effectAddAbilityToDamage {
			total += scope[scopePrefixAbility+ability]
		}
	}

	if ceiling := e.pack.DamageCap(); ceiling > 0 {
		total = min(total, ceiling)
	}

	return max(total, 0), critical, nil
}

// multiplier returns the pack's critical multiplier, or 2 when it declares none.
//
// **A default, and a pack without one is not refused.** `crit_multiplier` is the one
// constant a pack may omit without breaking: a critical hit has to do *something*, and
// 2 is what every edition of this game uses. A pack that wants a different number
// declares it; a pack that wants to refuse to have critical hits at all sets the
// toggles false, which is the honest way to say it.
func multiplier(pack *Pack) int {
	if declared, found := pack.Constant("crit_multiplier"); found && declared > 0 {
		return declared
	}

	return 2
}

// attackerModifiers returns a creature's ability modifiers, by slug.
//
// **Built by walking the pack and looking the score up**, for the reason
// `creature.Ability` gives: the result is a map because a mastery effect names an
// ability by slug, and it is built from the pack's ordered list so the *construction*
// is deterministic even though the result is keyed.
func attackerModifiers(subject *creature, pack *Pack) map[string]int {
	modifiers := make(map[string]int, len(pack.abilities))

	for _, ability := range pack.abilities {
		modifiers[ability.slug] = Modifier(subject.Ability[ability.slug])
	}

	return modifiers
}

// attackerScores returns a creature's ability scores, by slug.
//
// **Two maps rather than one parameterised by "which kind of number"**, and the reason is
// that the two consumers cannot be given the same thing and still be right: the ability
// fold compares *modifiers* ("the better of these two") and a mastery requirement
// compares *scores* ("at least 13"). One map holding one or the other would make one of
// them wrong, and both were wrong at different times in this package's history.
func attackerScores(subject *creature, pack *Pack) map[string]int {
	scores := make(map[string]int, len(pack.abilities))

	for _, ability := range pack.abilities {
		scores[ability.slug] = subject.Ability[ability.slug]
	}

	return scores
}

// damageAbility returns the ability a hit's damage is rolled with.
//
// **The mastery hook's answer applied as a rule**, and there are exactly two things it
// can say: the attack's own ability, or the better of a pair a `damage_ability_from_best`
// effect names. Choosing between two is a comparison over two modifiers, which is why
// the *pair* is data (`of: [strength, dexterity]` is the statement) and this comparison
// is not — a table cannot say "the better of these two" without saying which two.
//
// **"Better" means the larger modifier, and it takes the modifiers as an argument rather
// than reading them.** Taking the alphabetically-latest slug instead would answer
// `strength` for `of: [strength, dexterity]` whatever the creature's scores were, so a
// finesse weapon in a Dexterious character's hands would add a Strength bonus — a bug
// that reads as "the dice feel wrong", is never traced, and is worse the *stronger* the
// character gets. `TestAFinesseMasteryChoosesTheBetterAbilityNotTheLaterOne` is what
// holds it.
//
// **A tie keeps the attack's own ability**, which is the reading a weapons table gives:
// "use Strength or Dexterity" leaves the choice with the wielder when neither is better,
// and the weapon they drew already said which. Any other tie-break would be a function
// of declaration order, which is a property of a YAML file's line numbers.
func damageAbility(attackAbility string, choice masteryChoice, modifiers map[string]int) string {
	for _, effect := range choice.Effects {
		if effect.Kind != effectDamageAbilityFromBest || len(effect.Of) == 0 {
			continue
		}

		best := effect.Of[0]

		for _, slug := range effect.Of[1:] {
			if modifiers[slug] > modifiers[best] {
				best = slug
			}
		}

		if modifiers[best] == modifiers[attackAbility] {
			return attackAbility
		}

		return best
	}

	return attackAbility
}

// defendingCreature reads the creature an attack is aimed at.
//
// **No ownership check, deliberately**, and the reason is that a defender is chosen by
// someone else: the attacker's player may attack *anybody*, and refusing an attack
// because the target is another player's would make the game unplayable. `Actor` is
// compared only where a creature acts *for* itself — `actingCreature` — which is the
// direction where the check has meaning.
func (e *Engine) defendingCreature(state rules.State, id rules.ObjectID) (creature, error) {
	object, found := state.Lookup(id)
	if !found {
		return creature{}, fmt.Errorf("%w: %q is not on the table", ErrNoSuchCreature, id)
	}

	return readCreature(object)
}

// resolveHeal resolves `heal`: restore hit points, clamped at the maximum.
//
// **One mutation, on the healing creature, under `set_hit_points`** and not under
// `heal`. `rules.Mutation`'s rule allows reusing the intent's op "when a mutation is
// exactly the operation asked for", and a hit-point change is not that: the intent asked
// to heal and the mutation says hit points are now this. Two names for one fact would be
// a second record of it on the wire, free to drift from what the system did.
func (e *Engine) resolveHeal(
	_ context.Context,
	call rules.Context,
	state rules.State,
	intent rules.Intent,
) ([]rules.Mutation, error) {
	var args healArgs
	if err := decodeArgs(intent, &args); err != nil {
		return nil, err
	}

	subject, err := e.actingCreature(call, state, intent)
	if err != nil {
		return nil, err
	}

	// **Clamped at the maximum, and at zero below it.** Both directions matter: a heal
	// past the maximum would let a GM's arithmetic turn a campaign into infinite hit
	// points, and a negative amount would be a heal that damages.
	subject.HitPoints = min(max(subject.HitPoints+args.Amount, 0), max(subject.MaxHitPoints, 0))

	return e.singleMutation(intent.Target, OpSetHitPoints, &subject)
}

// resolveCondition resolves `apply_condition` and `clear_condition`.
//
// **One function for both**, and the shared part is most of the work: the arguments
// are the same shape, the lookup in the pack is the same lookup, the prune is the same
// prune, and the only difference is which end of the set the slug goes on. Splitting
// them would duplicate the refusal that matters — a slug the pack does not declare —
// into two places that could disagree.
func (e *Engine) resolveCondition(
	_ context.Context,
	call rules.Context,
	state rules.State,
	intent rules.Intent,
) ([]rules.Mutation, error) {
	var args conditionArgs
	if err := decodeArgs(intent, &args); err != nil {
		return nil, err
	}

	subject, err := e.actingCreature(call, state, intent)
	if err != nil {
		return nil, err
	}

	// The pack is consulted before the creature is modified, and a slug it does not
	// declare is refused — **including one it declares with `disabled`**, which is
	// `ErrNoSuchCondition` and is the whole of ADR 0012's third house-rule example: a
	// GM who turns `poisoned` off has made it not a condition, and applying it would be
	// the rule silently not applying.
	if _, declared := e.pack.ConditionAt(args.Condition); !declared {
		return nil, fmt.Errorf("%w: %q", ErrNoSuchCondition, args.Condition)
	}

	switch intent.Op {
	case OpApplyCondition:
		if !slices.Contains(subject.Conditions, args.Condition) {
			subject.Conditions = append(subject.Conditions, args.Condition)
		}
	case OpClearCondition:
		subject.Conditions = slices.DeleteFunc(subject.Conditions, func(held string) bool {
			return held == args.Condition
		})
	}

	// Pruned into the pack's declaration order, which is what makes the mutation's bytes
	// a function of *which* conditions are present rather than of the order they were
	// acquired in. See `creature.pruneConditions`.
	subject.pruneConditions(e.pack)

	return e.singleMutation(intent.Target, intent.Op, &subject)
}

// resolveStatus resolves `apply_status`, the GM's adjudication.
//
// **The one operation that writes a creature's hit points outright**, and that is
// exactly why it is GM-only: it is the operation that decides a creature's fate without
// rolling, so §7.2 reserves it. A player who could send it could set their own hit
// points, which is not a balance question but a hole.
//
// Every field is optional and the resolution names **one** mutation under the intent's
// own op, because a GM bringing a creature from zero to full and marking it exhausted is
// performing one act. Four mutations would make a client apply them in sequence against
// a document that is only written once.
func (e *Engine) resolveStatus(
	_ context.Context,
	call rules.Context,
	state rules.State,
	intent rules.Intent,
) ([]rules.Mutation, error) {
	var args statusArgs
	if err := decodeArgs(intent, &args); err != nil {
		return nil, err
	}

	// The GM is the only actor who reaches here — `Apply` refused a player before any
	// state was read — so `actingCreature`'s ownership branch is inert for this op. It
	// is called anyway rather than through a second read, so that "the creature does not
	// exist" is one refusal with one message across all seven operations.
	subject, err := e.actingCreature(call, state, intent)
	if err != nil {
		return nil, err
	}

	if !args.stated() {
		return nil, fmt.Errorf("%w: %q stated nothing to apply", ErrBadArguments, intent.Op)
	}

	if args.HitPoints != nil {
		subject.HitPoints = max(*args.HitPoints, 0)
	}

	// The maximum first, then the hit points clamped to it. **That order**, and it is a
	// rule: a GM who raises the maximum and sets hit points below the old one gets the
	// hit points they set, and a GM who lowers the maximum below the current hit points
	// gets the hit points clamped — a creature cannot carry more health than it can have,
	// and a token showing 40 of 12 is a state no client can render.
	if args.MaxHitPoints != nil {
		subject.MaxHitPoints = max(*args.MaxHitPoints, 0)
		subject.HitPoints = min(subject.HitPoints, subject.MaxHitPoints)
	}

	if args.Speed != nil {
		subject.Speed = max(*args.Speed, 0)
	}

	if args.Condition != "" {
		if _, declared := e.pack.ConditionAt(args.Condition); !declared {
			return nil, fmt.Errorf("%w: %q", ErrNoSuchCondition, args.Condition)
		}

		if !slices.Contains(subject.Conditions, args.Condition) {
			subject.Conditions = append(subject.Conditions, args.Condition)
		}
	}

	if args.ClearCondition != "" {
		subject.Conditions = slices.DeleteFunc(subject.Conditions, func(held string) bool {
			return held == args.ClearCondition
		})
	}

	subject.pruneConditions(e.pack)

	return e.singleMutation(intent.Target, OpApplyStatus, &subject)
}

// singleMutation states one creature's new body as one mutation.
//
// **One helper for all seven operations**, because the encoding and the refusal are the
// same in every one and a second copy of `rules.NewMutation`'s error handling would be a
// second place for a mutation a hub could not apply to be produced from.
func (e *Engine) singleMutation(
	target rules.ObjectID,
	operation rules.Op,
	subject *creature,
) ([]rules.Mutation, error) {
	mutation, err := rules.NewMutation(target, operation, encodeCreature(subject))
	if err != nil {
		return nil, fmt.Errorf("dnd5e: stating %q on %q: %w", operation, target, err)
	}

	return []rules.Mutation{mutation}, nil
}
