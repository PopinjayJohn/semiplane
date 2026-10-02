package dnd5e

import (
	"fmt"
	"slices"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// # The views, and what a payload is allowed to be
//
// §10.6.1 says the built-in renderers serve "a stat block, key/value, list and tag
// cloud", and that a plugin needing none of them pays nothing. This package needs all
// three of the ones it uses and **ships no templ component and no JavaScript**, which
// is the whole of "a simple plugin ships data and zero UI code" — and the reason a
// system sharing nothing with 5e needs no client work at all.
//
// The payloads are **unexported structs**, not `map[string]any`. A payload's shape is
// the system's own (`rules.Payload` says so with a character-sheet argument), and a
// struct is what makes a renderer type-safe rather than a traversal through `any`. The
// JSON tags are what a built-in renderer marshals them through, so they are part of
// this package's wire with semiplane's UI tier and renaming one is a breaking change
// there.
//
// **No resolved roll is ever in a payload except a creature's own `LastRoll`.** That is
// not a leak — `LastRoll` is state the hub already holds on the token and hands to
// every client — but it is worth naming, because the temptation to add a "last roll of
// the table" view is exactly the one S-12.3 forbids: a campaign-wide roll log is a
// place where a party's dice results would be readable by a reader who should not have
// them, and `rules.Mutation.String` withholds payloads for the same reason.

// views is what `Views` declares, and the three names are the ones `Derive` switches
// on.
//
// **A `switch` on the name rather than a lookup table**, for the reason
// `knownOp` is a switch: the vocabulary is this package's declaration, and a `switch`
// makes adding a fifth view a compile-visible edit rather than an entry somebody
// appends to a map beside a loop. The failure it prevents is a view a renderer can
// name and `Derive` cannot answer, which renders as a blank page saying nothing went
// wrong.
var views = []rules.View{
	{
		Name:     ViewStatBlock,
		Title:    "Stat block",
		Renderer: rules.RendererGeneric,
		Shape:    rules.ShapeStatBlock,
	},
	{
		Name:     ViewConditionReference,
		Title:    "Condition reference",
		Renderer: rules.RendererGeneric,
		Shape:    rules.ShapeList,
	},
	{
		Name:     ViewConditionIndex,
		Title:    "Conditions in force",
		Renderer: rules.RendererGeneric,
		Shape:    rules.ShapeTagCloud,
	},
}

// Derive answers one render query with opaque data.
//
// **Three rules, each a way this could have been wrong:**
//
//  1. **A view this system does not declare is refused**, not answered with an empty
//     payload. `Payload.View` is what picks the renderer, so answering an unknown name
//     produces a card rendered from nothing: a blank document that says nothing went
//     wrong, which is the failure mode `rules.View.Check` refuses an invalid declaration
//     over and is the same failure one layer up.
//  2. **Only `token` objects are read**, and only the ones the query named. That is the
//     render-side half of S-14.7: an unknown kind is inert, so a tabletop carrying a
//     placement nothing registers still renders every card on it.
//  3. **A row this system cannot read is skipped, never fatal** — for the list-shaped
//     views. A stale token whose `Data` is not this system's encoding is not a reason to
//     refuse to describe the other nineteen, and refusing here would put one corrupt row
//     in the way of reading a whole table. The *named* view is different: a stat block
//     about an object that cannot be read is a refusal, because there is nothing
//     legitimate to render in its place.
func (e *Engine) Derive(state rules.State, query rules.Query) (rules.Payload, error) {
	if e == nil || e.pack == nil {
		return rules.Payload{}, ErrNoEngine
	}

	if !query.Check() {
		return rules.Payload{}, fmt.Errorf(
			"%w: the query names view %q at object %q",
			rules.ErrInvalidView, query.View, query.Object,
		)
	}

	switch query.View {
	case ViewStatBlock:
		return e.deriveStatBlock(state, query)
	case ViewConditionReference:
		return deriveConditionReference(e.pack, query.Limit)
	case ViewConditionIndex:
		return e.deriveConditionIndex(state, query.Limit)
	default:
		return rules.Payload{}, fmt.Errorf(
			"%w: %q is not a view this system declares", rules.ErrInvalidView, query.View,
		)
	}
}

// statBlock is what `stat-block` answers with: one creature's derived card.
//
// spelling: a renderer that showed `hitPoints` beside a character sheet's
// "Hit Points" would be asking a GM to translate between two spellings of one
// number.
//
// A struct and not a map, for the reason the file comment gives. Every field is either
// read straight off the creature or computed from a **pack formula**, so there is no
// figure on this card that a house rule cannot move and no figure that only exists
// because Go wrote it down.
//
//nolint:tagliatelle // the field names are the pack's and the sheet's, in the same
type statBlock struct {
	// Object is the game object this card is about, so a client reconciling several
	// cards does not have to carry the mapping itself.
	Object string `json:"object"`

	// Name is the token's display name.
	Name string `json:"name"`

	// Level and Proficiency come from the creature and the pack's table respectively.
	Level       int `json:"level"`
	Proficiency int `json:"proficiency"`

	// ArmourClass is the creature's effective value: a stored override (a plate) or the
	// pack's `armour_class` formula, plus the shield bonus.
	ArmourClass int `json:"armour_class"`

	// HitPoints and MaxHitPoints are the creature's own, verbatim.
	HitPoints    int `json:"hit_points"`
	MaxHitPoints int `json:"max_hit_points"`

	// Speed is the stored speed, and EffectiveSpeed is that speed with the pack's
	// `speed_multiplier` applied to every condition in force.
	//
	// **The only reader of `speed_multiplier` in this slice, and it is a display
	// figure rather than a rule** — nothing in this engine resolves movement, so no
	// resolution reads it and no house rule may change what a movement resolution
	// would do. It is here because a card that shows a grappled creature walking at 30
	// feet is a card a GM cannot use, and because a pack column that no code reads is a
	// column whose spelling nobody has to keep right.
	Speed          int `json:"speed"`
	EffectiveSpeed int `json:"effective_speed"`

	// Abilities are the creature's scores **in the pack's declaration order**, never in
	// the order of the scores map. See `creature.Ability`.
	Abilities []abilityScore `json:"abilities"`

	// Attacks are the creature's declared attacks, in the creature's own order.
	Attacks []attackSummary `json:"attacks"`

	// Conditions are the conditions in force, in the pack's declaration order.
	Conditions []conditionSummary `json:"conditions"`

	// LastRoll is the creature's most recent resolved roll, absent when it has none.
	//
	// **State, not a re-derivation.** Whether the roll critted depended on the
	// defender's state at resolution time, so a client re-deriving it would be
	// re-deriving a question about a snapshot it may no longer hold.
	LastRoll *rollRecord `json:"last_roll,omitempty"`
}

// heading.
//
// heading.
//
// heading.
//
// heading.
//
// heading.
//
// abilityScore is one ability on a card: what the creature has, what it is worth,
// and the difficulty class its saving throw imposes.
//
//nolint:tagliatelle // `save_dc` is the pack's formula name and a stat block's
//nolint:tagliatelle // `save_dc` is the pack's formula name and a stat block's
//nolint:tagliatelle // `save_dc` is the pack's formula name and a stat block's
//nolint:tagliatelle // `save_dc` is the pack's formula name and a stat block's
//nolint:tagliatelle // `save_dc` is the pack's formula name and a stat block's
type abilityScore struct {
	Slug     string `json:"slug"`
	Label    string `json:"label"`
	Short    string `json:"short"`
	Score    int    `json:"score"`
	Modifier int    `json:"modifier"`

	// SaveDC is the pack's `save_dc` formula evaluated for this ability, which is what
	// the *opposing* creature must beat on this saving throw.
	//
	// **The save's difficulty and not the save's bonus**, because the pack declares one
	// formula and not the other. A pack wanting a save-bonus column declares
	// `save_bonus` beside it and this card would carry the field — which is the point:
	// the card reports what the pack can compute, and complains about nothing else.
	SaveDC int `json:"save_dc"`
}

// attackSummary is one declared attack on a card.
//
// `Damage` is the notation expression, **not a rolled number**: a card that showed a
// damage figure would be a card showing a die roll nobody asked for, and S-12.3's rule
// about dice results reaching somewhere they would be recorded is the same rule that
// keeps `LastRoll` on the creature rather than in a derived view.
//
//nolint:tagliatelle // `attack_bonus` is the sheet's spelling, not this one's.
//nolint:tagliatelle // `attack_bonus` is the sheet's spelling, not this one's.
//nolint:tagliatelle // `attack_bonus` is the sheet's spelling, not this one's.
//nolint:tagliatelle // `attack_bonus` is the sheet's spelling, not this one's.
//nolint:tagliatelle // `attack_bonus` is the sheet's spelling, not this one's.
type attackSummary struct {
	Name        string   `json:"name"`
	Ability     string   `json:"ability"`
	Damage      string   `json:"damage"`
	AttackBonus int      `json:"attack_bonus,omitempty"`
	Mastery     []string `json:"mastery,omitempty"`
}

// conditionSummary is one condition as a card or a reference lists it.
type conditionSummary struct {
	Slug    string `json:"slug"`
	Label   string `json:"label"`
	Summary string `json:"summary,omitempty"`
}

// conditionCount is one line of `condition-index`: how many tokens carry a condition.
type conditionCount struct {
	Slug  string `json:"slug"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// deriveStatBlock answers `stat-block` for the one object the query named.
func (e *Engine) deriveStatBlock(state rules.State, query rules.Query) (rules.Payload, error) {
	if query.Object == "" {
		// Refused rather than answered with a campaign-wide card. `rules.Query.Check`
		// allows an empty object because a campaign-wide query is a legitimate question
		// in general — and for *this* view it names nothing, so the only thing to answer
		// would be a card about no creature.
		return rules.Payload{}, fmt.Errorf(
			"%w: %q is about one creature and the query named none",
			rules.ErrInvalidView, ViewStatBlock,
		)
	}

	object, found := state.Lookup(query.Object)
	if !found {
		return rules.Payload{}, fmt.Errorf("%w: %q", ErrNoSuchCreature, query.Object)
	}

	if object.Kind != rules.KindToken {
		// Refused by **kind** rather than by failing to decode, because a non-token
		// placement is not a broken creature: it is something else on the table, and the
		// card would render zeroes from an empty body and look like a creature at 0 of 0.
		return rules.Payload{}, fmt.Errorf(
			"%w: %q is a %q and this card is about a token",
			ErrNoSuchCreature, object.ID, object.Kind,
		)
	}

	subject, err := readCreature(object)
	if err != nil {
		return rules.Payload{}, err
	}

	scope, err := e.scope(&subject)
	if err != nil {
		return rules.Payload{}, err
	}

	e.dcScope(scope)

	armourClass, err := e.armourClass(&subject, scope)
	if err != nil {
		return rules.Payload{}, err
	}

	card, err := e.buildStatBlock(object.ID, &subject, scope, armourClass)
	if err != nil {
		return rules.Payload{}, err
	}

	payload, err := rules.NewPayload(ViewStatBlock, card)
	if err != nil {
		return rules.Payload{}, fmt.Errorf("dnd5e: answering %q: %w", ViewStatBlock, err)
	}

	return payload, nil
}

// buildStatBlock fills in the parts of a card that need the pack's formulas.
//
// **Every ordered walk here is over the pack's slices**, and never over a creature's
// six scores, which is the load-bearing half of `creature.Ability`'s comment. The
// abilities map is read by slug and nothing else.
//
// The scope is a parameter rather than something built here, so that this function is
// the only place a card is assembled and `deriveStatBlock` is the only place a scope is
// built — a second `scope()` call would be a second answer to "what is in scope", and
// the one that forgot the difficulty classes would produce a card missing a save.
func (e *Engine) buildStatBlock(
	id rules.ObjectID,
	subject *creature,
	scope map[string]int,
	armourClass int,
) (statBlock, error) {
	card := statBlock{
		Object:       id.String(),
		Name:         subject.Name,
		Level:        subject.Level,
		Proficiency:  scope[scopeProf],
		ArmourClass:  armourClass,
		HitPoints:    subject.HitPoints,
		MaxHitPoints: subject.MaxHitPoints,
		Speed:        subject.Speed,
		Abilities:    make([]abilityScore, 0, len(e.pack.abilities)),
		Attacks:      make([]attackSummary, 0, len(subject.Attacks)),
	}

	// Conditions first, because `EffectiveSpeed` reads them.
	card.Conditions = e.conditionsInForce(subject)

	card.EffectiveSpeed = effectiveSpeed(e.pack, subject.Speed, card.Conditions)

	for _, declared := range e.pack.abilities {
		// The formula is evaluated **before** the score is written, so a card never
		// carries a save DC the pack could not compute — the failure is a refusal from
		// `evaluateFor` naming the formula rather than a card with a zero in it.
		difficulty, err := e.evaluateFor("save_dc", declared.slug, scope)
		if err != nil {
			return statBlock{}, err
		}

		card.Abilities = append(card.Abilities, abilityScore{
			Slug:     declared.slug,
			Label:    declared.label,
			Short:    declared.short,
			Score:    subject.Ability[declared.slug],
			Modifier: Modifier(subject.Ability[declared.slug]),
			SaveDC:   difficulty,
		})
	}

	for _, attack := range subject.Attacks {
		card.Attacks = append(card.Attacks, attackSummary{
			Name:        attack.Name,
			Ability:     attack.Ability,
			Damage:      attack.Damage,
			AttackBonus: attack.AttackBonus,
			Mastery:     slices.Clone(attack.Mastery),
		})
	}

	card.LastRoll = subject.LastRoll

	return card, nil
}

// conditionsInForce lists the creature's conditions in the **pack's declaration order**.
//
// Pruned through `pruneConditions` on a copy rather than filtered here, so that the
// card's order and the order a mutation's bytes carry are one answer: two creatures
// carrying `prone` and `blinded` produce the same list whichever acquired which first.
func (e *Engine) conditionsInForce(subject *creature) []conditionSummary {
	pruned := *subject
	pruned.pruneConditions(e.pack)

	listed := make([]conditionSummary, 0, len(pruned.Conditions))

	for _, slug := range pruned.Conditions {
		row, declared := e.pack.ConditionAt(slug)
		if !declared {
			// Unreachable: `pruneConditions` kept only declared slugs. Present because a
			// card with a blank line where a condition should be is a card a GM reads
			// and believes.
			continue
		}

		listed = append(
			listed,
			conditionSummary{Slug: row.slug, Label: row.label, Summary: row.summary},
		)
	}

	return listed
}

// effectiveSpeed applies the pack's `speed_multiplier` for every condition in force.
//
// **A product, and clamped at zero** — clamped because a condition that sets speed to
// zero must not be undone by another that halves it, and clamped rather than left
// negative because a card showing a creature moving at negative feet is not a card a
// reader can act on. The product rather than a minimum because the multipliers compose:
// a grappled (zero) creature that is also exhausted stays at zero whichever order they
// are applied in, which is what makes the answer independent of the walk.
func effectiveSpeed(pack *Pack, speed int, conditions []conditionSummary) int {
	total := speed

	for _, held := range conditions {
		row, declared := pack.ConditionAt(held.Slug)
		if !declared {
			continue
		}

		total *= row.speedMultiplier
	}

	return max(total, 0)
}

// deriveConditionReference answers `condition-reference`: every condition this pack
// declares, with what being in it does.
//
// **Pack-level and object-free**, which is why the query's `Object` is ignored rather
// than refused: this view is about the *pack*, not about a token, and a client asking
// for a condition reference with a token in hand is asking the right question with a
// field filled in. `Limit` is honoured because §14's "a maximal output" needs a
// maximal that is still a number, and a pack with two hundred conditions rendering all
// of them is not a page.
func deriveConditionReference(pack *Pack, limit int) (rules.Payload, error) {
	entries := make([]conditionSummary, 0, len(pack.conditions))

	for _, row := range pack.conditions {
		if limit > 0 && len(entries) == limit {
			break
		}

		entries = append(
			entries,
			conditionSummary{Slug: row.slug, Label: row.label, Summary: row.summary},
		)
	}

	payload, err := rules.NewPayload(ViewConditionReference, entries)
	if err != nil {
		return rules.Payload{}, fmt.Errorf(
			"dnd5e: answering %q: %w", ViewConditionReference, err,
		)
	}

	return payload, nil
}

// deriveConditionIndex answers `condition-index`: the conditions in force on the table,
// with how many tokens carry each.
//
// **A fixed order — the pack's declaration order — and the counts keyed by that walk
// rather than by a map**, because the tag cloud's order is what a reader scans and a
// count map's iteration order is not a property of anything. A slug the pack no longer
// declares is skipped rather than reported: a pack revision that removed a condition
// leaves tokens carrying its slug, and a tag cloud that listed `poisoned` for a pack
// with no `poisoned` row would be answering a question nobody asked.
//
// **An unreadable token is skipped.** Same rule as `readCreature`'s neighbours: one
// stale row does not put a whole table out of reach.
func (e *Engine) deriveConditionIndex(state rules.State, limit int) (rules.Payload, error) {
	counts := make(map[string]int, len(e.pack.conditions))

	for _, object := range state.OfKind(rules.KindToken) {
		subject, err := readCreature(object)
		if err != nil {
			continue
		}

		for _, slug := range subject.Conditions {
			if _, declared := e.pack.ConditionAt(slug); declared {
				counts[slug]++
			}
		}
	}

	listed := make([]conditionCount, 0, len(e.pack.conditions))

	for _, row := range e.pack.conditions {
		carried := counts[row.slug]
		if carried == 0 {
			// A condition nobody is in is omitted rather than shown at zero: a tag cloud
			// with twenty tags on it is a legend, and a legend of a pack's full condition
			// list is the reference view's job, not this one's.
			continue
		}

		if limit > 0 && len(listed) == limit {
			break
		}

		listed = append(listed, conditionCount{Slug: row.slug, Label: row.label, Count: carried})
	}

	payload, err := rules.NewPayload(ViewConditionIndex, listed)
	if err != nil {
		return rules.Payload{}, fmt.Errorf("dnd5e: answering %q: %w", ViewConditionIndex, err)
	}

	return payload, nil
}
