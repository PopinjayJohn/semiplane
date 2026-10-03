package dnd5e

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// # What a game object is
//
// A creature is semiplane's `rules.Object` whose `Kind` is **`rules.KindToken`** —
// semiplane's, in every build, forever. §10.2.1's row says `token` and `scene` are
// "present on every map in every system, parameterised by the system, not defined by
// it", and that is load-bearing: it is what lets the PixiJS map layer render a
// table without knowing any rules, and so what makes this engine optional.
//
// Everything 5e-specific is in `Data`, and only this package reads it. That boundary
// is `rules.Object.Data`'s own, and the consequence is worth stating: **this package
// declares no game-object kind of its own.** Its `ContentKinds` are the pack's
// *rules-content* kinds — `creature`, `spell`, `class`, `feat` — which are page kinds
// a campaign's wiki uses, and a token on the table is a token of everybody's.
//
// # Why the encoding is JSON rather than the pack's YAML
//
// The pack is *configuration* and this is *runtime state*: a creature's hit points
// change twenty times a session and a pack revision must not be able to reshape a
// creature that already exists. Different lifetimes, different tools — and
// `encoding/json` is the standard library's while the pack's YAML is because a human
// edits it and a machine does not.
//
// `placementCodec` in `internal/plugin` is the same encoding and the same reasoning,
// and it is deliberately *this* package's rather than the shared one: §10.8's codec
// row says only the code that wrote an encoding can read it back, and a creature's
// action economy, its attacks and its condition set are 5e's facts.

// creature is one game object's runtime state, in this system's own encoding.
//
// spreadsheet, and a payload spelled `maxHp` would be the only place in the product
// where the same fact had two spellings.
//
// **Unexported, and reachable only through `readCreature` and `encodeCreature`.** A
// caller that built one itself could hand `Apply` a state the hub never held, and the
// mutation it produced would describe a creature that does not exist.
//
//nolint:tagliatelle // the field names are 5e's, not JavaScript's: `max_hp` and `ac_bonus` appear on a character sheet, in a campaign's own notes and in a GM's
type creature struct {
	// Name is the display name. Never redacted and never a secret: it is a token's
	// name, and `rules.Mutation.String` never reaches into a payload anyway.
	Name string `json:"name"`

	// Actor is the `domain.User.ID` this token is placed for, and **zero means the
	// GM's or nobody's**.
	//
	// It is what §7.2's second half enforces in rules: a player may act for their own
	// creature and not for another's. Zero is a legal value rather than "unset" because
	// a GM-placed token has no player behind it and saying so with a sentinel would be
	// a value a client could not read back.
	//
	// **It comes from the token's own data and never from the frame**, which is the
	// security-relevant property: `realtime.Intent` carries the actor because the access
	// gate admitted the connection, and this field is compared *against* that actor. A
	// client cannot name a token it does not own into this field — the token's body is
	// the hub's, written by a previous resolution.
	Actor int64 `json:"actor,omitempty"`

	// Level is the creature's level, and reads proficiency from the pack's table.
	Level int `json:"level"`

	// Ability holds the six scores by slug, and **is never walked**.
	//
	// That is the load-bearing part. Every ordered walk in this package goes over the
	// **pack's** ability list, which is a slice in declaration order, and looks a
	// score up in this map by slug. A walk over `Ability` directly would be S-10.4's
	// forbidden construct arriving in the most obvious way — a creature's six scores
	// is the smallest possible map and therefore the one a contributor reaches for
	// first. `TestTheDerivedCardWalksThePacksAbilitiesNotTheScoresMap` is what holds
	// it.
	Ability map[string]int `json:"ability"`

	// HitPoints and MaxHitPoints are the creature's resource.
	HitPoints    int `json:"hp"`
	MaxHitPoints int `json:"max_hp"`

	// ArmourClass overrides the computed value when non-zero, which is what a plate
	// and a shield do. Zero means "ask the pack".
	ArmourClass int `json:"ac"`

	// ArmourBonus is added to the computed value, for a shield and a magic item.
	ArmourBonus int `json:"ac_bonus"`

	// Speed is the creature's speed in feet, before conditions.
	Speed int `json:"speed"`

	// Conditions are slugs from the pack, and a slug the pack does not declare is
	// **dropped on write rather than refused**.
	//
	// S-3.3's inertness in the direction that matters: a campaign whose pack removed a
	// condition keeps creatures carrying its slug, and refusing to *read* one would make
	// every resolution against that creature fail — which is §10.8's forbidden failure,
	// a plugin removal breaking resolutions that have nothing to do with it. Dropping
	// on write is the same "inert, not fatal" decision as an unknown kind, applied to
	// a slug rather than to a kind.
	Conditions []string `json:"conditions"`

	// Attacks are the creature's attacks, in declaration order.
	Attacks []attackDef `json:"attacks"`

	// Masteries are the slugs the creature holds proficiency in.
	Masteries []string `json:"masteries"`

	// LastRoll is the most recent roll this creature resolved, and the reason `roll`
	// and `attack` produce a mutation at all.
	//
	// **On the creature rather than in a second object**, and the reason is a shape
	// rule rather than a modelling preference: `rules.NewMutation` requires a target,
	// and a roll names no object of its own, so a campaign-wide roll log would need a
	// placement to hang on and semiplane has no such thing. Every client at the table
	// already holds this creature's token, so the answer rides along on it.
	//
	// A pointer, and `nil` is the meaningful value: a token nobody has rolled has no
	// record, and a zero-valued struct would render as "a roll of 0" rather than as
	// nothing.
	LastRoll *rollRecord `json:"last_roll,omitempty"`
}

// says and what the arguments of `attack` say.
//
// says and what the arguments of `attack` say.
//
// says and what the arguments of `attack` say.
//
// says and what the arguments of `attack` say.
//
// says and what the arguments of `attack` say.
//
// says and what the arguments of `attack` say.
//
// says and what the arguments of `attack` say.
//
// attackDef is one attack a creature has.
//
// **`Damage` is a notation expression, not a number.** `2d6+3` is what a weapon does
// and it is evaluated per resolution against that resolution's scope — a magic weapon
// whose `+3` came from a pack constant changes with the pack, and a number stored on
// the creature would not.
//
//nolint:tagliatelle // `attack_bonus` is what a weapon's line on a character sheet
//nolint:tagliatelle // `attack_bonus` is what a weapon's line on a character sheet
//nolint:tagliatelle // `attack_bonus` is what a weapon's line on a character sheet
//nolint:tagliatelle // `attack_bonus` is what a weapon's line on a character sheet
//nolint:tagliatelle // `attack_bonus` is what a weapon's line on a character sheet
//nolint:tagliatelle // `attack_bonus` is what a weapon's line on a character sheet
//nolint:tagliatelle // `attack_bonus` is what a weapon's line on a character sheet
type attackDef struct {
	Name string `json:"name"`

	// Ability is the ability the attack is made and, unless a mastery says otherwise,
	// rolled for damage with.
	Ability string `json:"ability"`

	// Damage is the notation expression this attack deals, e.g. `1d12+3`.
	Damage string `json:"damage"`

	// AttackBonus is a flat bonus added to the attack roll after the pack's
	// `attack_bonus` formula and before the armour-class comparison.
	//
	// **Separate from the formula, and the reason is what a formula cannot hold.**
	// `attack_bonus` is `prof + ability` for every creature of the pack; a magic
	// weapon's `+3` belongs to *this* attack of *this* creature, so it is state rather
	// than configuration. A house rule that changes the shape of the bonus changes the
	// formula and moves `EngineVersion`; a GM who hands a player a `+1` sword changes
	// one token and moves nothing at all — which is exactly the split ADR 0018 draws
	// between the meaning of a rule and the state a campaign is in.
	AttackBonus int `json:"attack_bonus,omitempty"`

	// Mastery are the slugs this weapon grants, **best first**.
	//
	// The order is the weapon's, and the mastery hook folds over it in this order
	// rather than ranking the list itself — see `defaultMastery.Mastery`, which is the
	// whole reason mastery is a hook.
	Mastery []string `json:"mastery,omitempty"`
}

// attackByName returns the attack with this name, and whether the creature has it.
//
// A linear walk over the declaration order rather than a map, and the reason is
// `rules.NewState`'s: the order is the creature's own, a table holds tens of attacks
// at most, and a map would be a second source of truth for an order the author wrote.
func (c *creature) attackByName(name string) (attackDef, bool) {
	for _, attack := range c.Attacks {
		if attack.Name == name {
			return attack, true
		}
	}

	return attackDef{}, false
}

// hasCondition reports whether the creature is in this condition.
//
// Read against the **pack's** condition list rather than against `Conditions` as a
// membership test, because a slug the pack does not declare is not a condition and
// `Conditions` may still carry it. See the field comment.
func (c *creature) hasCondition(pack *Pack, slug string) bool {
	if _, declared := pack.ConditionAt(slug); !declared {
		return false
	}

	return slices.Contains(c.Conditions, slug)
}

// pruneConditions returns the creature's conditions with the ones this pack does not
// declare removed, **in the pack's declaration order**.
//
// Sorted by the pack rather than left in the order they were added, and the reason is
// the same as everywhere else in this project: a slice's order reaching a mutation's
// bytes is a replay hazard. Two creatures carrying `prone` and `blinded` must produce
// the same bytes whichever acquired which first.
func (c *creature) pruneConditions(pack *Pack) {
	kept := make([]string, 0, len(c.Conditions))

	for _, declared := range pack.Conditions() {
		if slices.Contains(c.Conditions, declared.slug) {
			kept = append(kept, declared.slug)
		}
	}

	if len(kept) == 0 {
		kept = nil
	}

	c.Conditions = kept
}

// Modifier returns the ability modifier for a score.
//
// The formula is 5e's and it is **here rather than in the pack**, and that is worth a
// record-level explanation because almost everything else numeric here is data.
//
// A modifier is not a tunable: it is the *definition* of what a score means, and
// ADR 0018 makes the fingerprint name "resolution semantics". If the formula were a
// pack constant, a house rule could change it — and a house rule that changed it
// would change what a **stored** creature's scores mean, which is exactly the
// "meaning of a persisted mutation" that ADR 0018 exists to keep out of the
// fingerprint. So it is code, it is versioned by `EngineVersion`, and it is not
// reachable from a module. The *constants* a formula needs (`ac_base`, `ac_dex_cap`,
// `prof_bonus_base`) are all data; the shape of the scoring model is not.
func Modifier(score int) int {
	// Not `(score - 10) / 2`, which truncates toward zero and gives a score of 9 a
	// modifier of `0` when 5e says `-1`. The formula is written out because the
	// difference between these two is a character in a person's character sheet, and
	// `TestAModifierOfEightIsMinusOne` is what holds it.
	//
	// **`+ 1` before the division, and that is the whole of the rounding rule.** Go's
	// integer division truncates toward zero, so `(-2)/2` is `-1` and `(-1)/2` is `0`;
	// the scoring model needs the *floor* of the negative half, which is what rounding a
	// score below the baseline away from zero does. Writing `-((10 - score - 1) / 2)`
	// instead — the shape this function had before the test below existed — is the same
	// mistake one operator to the left: it gives 8 a modifier of `0`, and it gives 9 one
	// too, so **every** score below the baseline is off by one rather than only the odd
	// ones. A truncation toward zero is wrong for every negative score; that is why the
	// correction is a whole formula and not a special case.
	const baseline = 10

	if score < baseline {
		return -((baseline - score + 1) / 2)
	}

	return (score - baseline) / 2
}

// readCreature decodes one game object as a creature.
//
// Refused for an object whose `Data` does not decode, and **not** for an object of
// another kind. The distinction is `rules.KindToken`: it is semiplane's, so every
// object on a 5e campaign's table is a token whether or not this system put it there,
// and refusing a token's absence of content would make every resolution against a
// page-derived token fail. §10.8's forbidden failure again, in the place a
// contributor reaches for first.
func readCreature(object rules.Object) (creature, error) {
	var decoded creature

	if err := json.Unmarshal(object.Data, &decoded); err != nil {
		// The decoder's text quotes the bytes it choked on. Those bytes are this
		// system's own encoding of a token, which is not vault content — a token's
		// `Data` is runtime state, and §10.8's own row says a bad pack is the failure
		// mode, not a bad vault — but the *shape* of the refusal is what matters and it
		// names the object, which is the half an operator needs. The error is wrapped
		// rather than dropped because unlike a YAML parse this one is not
		// attacker-reachable: a token's body is written by this package's own codec.
		return creature{}, fmt.Errorf("%w: %q: %w", ErrMalformedCreature, object.ID, err)
	}

	if decoded.Ability == nil {
		decoded.Ability = map[string]int{}
	}

	return decoded, nil
}

// encodeCreature renders a creature's body as the opaque payload a mutation carries.
//
// One function rather than an inline `json.Marshal` at each of the seven call sites,
// so that the encoding a resolver writes and the encoding `readCreature` reads are
// the same pair by construction rather than by two lines happening to agree.
func encodeCreature(state *creature) []byte {
	// A struct of integers and strings cannot fail to marshal. The fallback is a body
	// both sides can read — `{}` decodes to zeroes rather than failing — and a
	// resolver that returned no payload instead would return a mutation that says
	// nothing happened, which is a *different* statement and a wrong one.
	encoded, err := json.Marshal(state)
	if err != nil {
		return []byte(`{}`)
	}

	return encoded
}

// rollRecord is the last roll this creature resolved, and the reason `roll` produces
// a mutation at all.
//
// §10.7's worked consequence for the graphical dice roller: "on send, emits
// `{"op":"roll"}` and renders the server's result". The result has to reach every
// client at the table, and `rules.Mutation.Args` is the field the answer travels in —
// so the roll is recorded **on the rolling creature**, where every client already has
// a placement to attach it to.
//
// It is the creature's state and not a separate object because `rules.NewMutation`
// requires a target and a roll names no object of its own: a campaign-wide roll log
// would need a placement to hang on, and semiplane has no such thing.
type rollRecord struct {
	// Expr is the source as written, kept because `rules.Expr` says the same thing:
	// an expression nobody can re-read cannot be audited (§16.3's roll log).
	Expr string `json:"expr"`

	// Label is what the player called the roll.
	Label string `json:"label,omitempty"`

	// Total is the resolved figure, modifiers included.
	Total int `json:"total"`

	// Natural is the dice alone, and is present only when the expression rolled a die
	// rather than summing constants.
	Natural *int `json:"natural,omitempty"`

	// Parts is the breakdown, one entry per die and one per modifier, in the order the
	// expression wrote them.
	Parts []int `json:"parts"`

	// Advantage is what the conditions in force added: `-1` none, `0` advantage,
	// `1` disadvantage. **An int rather than the `Advantage` string**, because this is
	// the one place the value is persisted on a creature and a string would let a pack
	// author write `advantage` into a creature's history by changing a row.
	Advantage int `json:"advantage,omitempty"`

	// Target and Met carry the DC the roll was measured against.
	Target int  `json:"target,omitempty"`
	Met    bool `json:"met,omitempty"`

	// Critical reports that an attack's hit was a critical hit, and is absent on a
	// roll that was not an attack — `roll` never crits, because the critical rule is
	// about an attack die and a damage die and a plain expression has neither of the
	// first.
	//
	// The flag is what a client scales a damage number by, and it is recorded rather
	// than re-derived: whether the hit critted depended on the *defender's* state at
	// resolution time, and a client re-deriving it from its own copy of the roll would
	// be re-deriving a question about a snapshot it may no longer have.
	Critical bool `json:"critical,omitempty"`
}

// advantageScore renders an `Advantage` as the small integer a `rollRecord` holds.
//
// **Kept in step with `Cancel`** by construction: `TestAdvantageScoresAndCancelAgree`
// runs every combination of two advantages through both and requires the same answer,
// because the pair is exactly the kind of duplicate answer this repository keeps
// finding.
func advantageScore(value Advantage) int {
	switch value {
	case AdvGains:
		return 0
	case AdvLoses:
		return 1
	case AdvNone:
		return -1
	default:
		return -1
	}
}
