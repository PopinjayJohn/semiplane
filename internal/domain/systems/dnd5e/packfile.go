package dnd5e

// # The pack's **file** shape: what YAML decodes into, before anything is resolved,
// defaulted or validated.
//
// # Why there are two shapes rather than one
//
// A pack is read from two directions. `ParsePack` reads a *standalone* pack — every
// collection present, every scalar stated — and `ParseOverlay` reads a *partial* one,
// which is what §10.4's 2014 and 2024 diffs are. Those need different types,
// because the difference between "this pack declares it is zero" and "this pack does
// not declare it" is invisible in a plain `int` and is exactly what a merge turns
// on. A 2014 overlay saying `toggles: {crit_damage_die_max: false}` against a base
// that said `true` must *replace*, and an overlay that omits the key must *inherit*.
//
// So the file shape uses pointers where absence is a value a merge can see, and the
// compiled `Pack` uses plain fields with defaults resolved once at load. That is the
// cost, and it buys a merge rule that can be stated in one sentence: **a collection
// is merged by slug; a scalar is replaced when declared; nothing declared is
// inherited.** Without the two shapes, that rule would need a `*int` for every
// integer in the schema, and the compiled pack — which every resolution reads —
// would carry thirty pointers it dereferences on every hit.
//
// # The merge is by slug, everywhere
//
// Not by index, and not by "replace the whole list". By slug, for one reason: a
// reviewable data diff is only reviewable if a reviewer can read *which row* changed.
// An index-keyed merge turns "2024 adds `inspiration`" into "every row after it
// changed", and a reviewer learns to skim it.
//
// # The pack is compiled input, and it is treated as such
//
// §12's "untrusted disk content" does not reach here: this file is `go:embed`ed and
// an overlay is compiled in too. What *does* reach here is a plugin author, and the
// failure mode worth engineering against is a mistyped key — which
// `decoder.KnownFields(true)` turns from a silent no-op into a load failure.

// packFile is the YAML shape of a pack or an overlay.
//
// Every field is optional, because an overlay declares only what it changes. The
// pointers are the shape of that statement; see the type comment.
//
// into a pack; a pack is a file a human edits.
type packFile struct {
	// System is the system this pack is for. Required in both directions.
	System string `yaml:"system"`

	// Version is the pack's revision, and one of the four fingerprint components.
	// Required in both directions.
	Version string `yaml:"version"`

	// Notation is the expression notation's name.
	Notation string `yaml:"notation"`

	// Title is the pack's display name, for a status page listing what a campaign
	// resolved to. An overlay's title names the overlay.
	Title string `yaml:"title"`

	Dice       diceSpec               `yaml:"dice"`
	Toggles    map[string]bool        `yaml:"toggles"`
	Abilities  []abilityFile          `yaml:"abilities"`
	Conditions []conditionFile        `yaml:"conditions"`
	Masteries  []masteryFile          `yaml:"masteries"`
	Reference  referenceFile          `yaml:"reference"`
	Formulas   map[string]formulaFile `yaml:"formulas"`
	Attack     attackFile             `yaml:"attack"`
	Hooks      map[string]string      `yaml:"hooks"`
	Kinds      []kindFile             `yaml:"kinds"`
}

// diceSpec is the notation's dice.
type diceSpec struct {
	// Sizes are the faces the notation admits. This is what makes the notation
	// **data**: `Grammar` builds its patterns from this list and `Parse` refuses
	// anything outside it, so a system whose primary die were not twenty changes this
	// list and two numbers rather than the parser. §10.3's "the protocol never
	// assumes d20" holds because the assumption is not in the code.
	Sizes []int `yaml:"sizes"`

	// Primary is the die an attack rolls.
	Primary dieSpec `yaml:"primary"`
}

// dieSpec is one die's shape. Pointer fields because a zero is meaningful for both.
type dieSpec struct {
	Faces *int `yaml:"faces"`
	Count *int `yaml:"count"`
}

// abilityFile is one ability, keyed by slug.
type abilityFile struct {
	Slug  string `yaml:"slug"`
	Label string `yaml:"label"`
	Short string `yaml:"short"`
}

// what the 5e text calls it.
//
// what the 5e text calls it.
//
// what the 5e text calls it.
//
// what the 5e text calls it.
//
// what the 5e text calls it.
//
// what the 5e text calls it.
//
// what the 5e text calls it.
//
// conditionFile is one condition, keyed by slug.
//
// `SpeedMultiplier` is a pointer and the reason is 5e's own data: "no entry" means
// the condition does not touch speed, and `0` means it sets speed to zero, and a
// plain `int` cannot say both. `Disabled` is **not** a pointer — absent and `false`
// mean the same thing, so a pointer would be noise.
//
//nolint:tagliatelle // a pack is a file a human edits, and `attacks_against` is
//nolint:tagliatelle // a pack is a file a human edits, and `attacks_against` is
//nolint:tagliatelle // a pack is a file a human edits, and `attacks_against` is
//nolint:tagliatelle // a pack is a file a human edits, and `attacks_against` is
//nolint:tagliatelle // a pack is a file a human edits, and `attacks_against` is
//nolint:tagliatelle // a pack is a file a human edits, and `attacks_against` is
//nolint:tagliatelle // a pack is a file a human edits, and `attacks_against` is
type conditionFile struct {
	Slug            string `yaml:"slug"`
	Label           string `yaml:"label"`
	Summary         string `yaml:"summary"`
	Attack          string `yaml:"attack"`
	AttacksAgainst  string `yaml:"attacks_against"`
	SpeedMultiplier *int   `yaml:"speed_multiplier"`
	Disabled        bool   `yaml:"disabled"`

	// CriticalExempt exempts attacks made against a creature in this condition from
	// the critical rule, which is 2024's rule for a paralysed or unconscious
	// creature.
	//
	// **A column and not a hardcoded list of condition slugs**, and the two
	// directions matter. An engine that checked for one spelled condition would have to
	// be forked for an overlay that wanted a different exemption, and an engine that
	// consulted no column would silently crit a helpless creature under a pack that had
	// asked it not to. `TestTheExemptionComesFromTheConditionRowNotFromASlug` holds it.
	CriticalExempt bool `yaml:"critical_exempt"`
}

// masteryFile is one weapon mastery, keyed by slug.
type masteryFile struct {
	Slug    string       `yaml:"slug"`
	Label   string       `yaml:"label"`
	Summary string       `yaml:"summary"`
	Effects []effectFile `yaml:"effects"`
}

// effectFile is one thing a mastery grants.
//
// The closed set of `Kind` values is the effect vocabulary, and the reason each
// effect is **data** while the rule that applies it is a **hook** is ADR 0045's
// subject: "add the ability to damage" is a pairing a table says, and deciding that
// a finesse mastery means the damage uses the better of two abilities is a fold over
// two pack rows that no row can answer alone.
//
// `Of` and `On` are per-kind payload, and a payload field an effect does not use is
// a validation failure rather than a silently-ignored value — see `validateEffect`.
type effectFile struct {
	Kind    string   `yaml:"kind"`
	Of      []string `yaml:"of"`
	Ability string   `yaml:"ability"`
	Value   int      `yaml:"value"`
}

// referenceFile is the pack's reference tables.
type referenceFile struct {
	// Constants are the names every formula is written against. A map rather than a
	// list so that a house rule can add one ("change a DC formula constant") without
	// renaming a row.
	Constants map[string]int `yaml:"constants"`

	// Proficiency is the level-to-bonus table, and a **table** rather than
	// `2 + (level-1)/4` on purpose: the band boundaries are the part a house rule
	// changes, and a computed bonus puts them back in Go where no pack can reach them.
	Proficiency []proficiencyFile `yaml:"proficiency"`

	// DCs are named difficulty classes other rules reference by slug.
	DCs []dcFile `yaml:"dcs"`
}

// proficiencyFile is one band of the proficiency table.
type proficiencyFile struct {
	From  int `yaml:"from"`
	To    int `yaml:"to"`
	Bonus int `yaml:"bonus"`
}

// dcFile is one named difficulty class.
type dcFile struct {
	Slug  string `yaml:"slug"`
	Label string `yaml:"label"`
	DC    int    `yaml:"dc"`
}

// formulaFile is one formula, and `For` is what binds the bare `ability` variable.
//
// Absent means evaluated once. `every` means once per declared ability, and a slug
// means once for that one. A formula naming `ability` with nothing here is refused
// at load, because a formula that reads an unbound variable has no answer and a
// resolver that returned one would be guessing.
type formulaFile struct {
	Expr  string     `yaml:"expr"`
	For   string     `yaml:"for"`
	Clamp *clampFile `yaml:"clamp"`
}

// clampFile bounds a formula's result. Both bounds are expressions over the same
// scope, so a cap written as `ac_base + ac_dex_cap` moves when a constant moves.
type clampFile struct {
	Min string `yaml:"min"`
	Max string `yaml:"max"`
}

// attackFile is how an attack resolves.
//
//nolint:tagliatelle // `damage_cap` is a house rule's name for the field.
//nolint:tagliatelle // `damage_cap` is a house rule's name for the field.
//nolint:tagliatelle // `damage_cap` is a house rule's name for the field.
//nolint:tagliatelle // `damage_cap` is a house rule's name for the field.
//nolint:tagliatelle // `damage_cap` is a house rule's name for the field.
//nolint:tagliatelle // `damage_cap` is a house rule's name for the field.
//nolint:tagliatelle // `damage_cap` is a house rule's name for the field.
type attackFile struct {
	Mastery masteryToggle `yaml:"mastery"`

	// DamageCap is the most damage one attack may do before multipliers, or zero for
	// uncapped. A house rule that caps a table's big hits is this field.
	DamageCap *int `yaml:"damage_cap"`
}

// masteryToggle is 2014's one structural difference, and it is a boolean.
type masteryToggle struct {
	Enabled *bool `yaml:"enabled"`
}

// kindFile is one kind this system recognises, keyed by slug. §10.4's fourth thing a
// pack carries, and the single source of `ContentKinds`.
type kindFile struct {
	Slug  string `yaml:"slug"`
	Label string `yaml:"label"`
}

// Overlay is one edition's replacements over a base pack.
//
// A named value rather than a bare `*packFile` for two reasons: an overlay in a
// refusal message should be named by its edition rather than by nothing, and the
// composition root hands one to `New` and would otherwise be able to hand a nil.
type Overlay struct {
	// name is the overlay's display name, for a status page and for ADR 0045's "the
	// differences show up as reviewable data diffs".
	//
	// **Unexported**, and the accessor below is why: an exported field and a `Name`
	// method is a collision the compiler refuses, and the field is only ever written
	// by `ParseOverlay`. What an overlay's caller needs is the two operations — *what
	// is it called* and *what does it produce* — and `Base` is the one that matters.
	name string

	// file is the replacements. Unexported because merging is this package's job: an
	// overlay's whole contract is "apply me", and exporting its fields would be a way
	// to apply it by hand with a different merge.
	file packFile
}
