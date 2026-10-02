package dnd5e

import (
	"fmt"
	"maps"
	"slices"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// compile turns a merged file into a `Pack`: defaults resolved, formulas parsed,
// references checked, and every refusal that a resolution would otherwise discover
// at the table discovered here instead.
//
// # Why the validation is this thorough
//
// `rules.System` has no `Check() error` — ADR 0041 records the reasoning, that a
// tenth method makes every future method a breaking change — so a plugin whose packs
// are broken must find out in its own constructor. And §10.2's table says the
// blast radius of a broken gameplay plugin is the **campaign state**, not one
// browser tab: a pack whose fourth condition does not parse fails every resolution
// that reads it, mid-session, with the campaign's log already holding the ones that
// did. A constructor that refuses is a failure at the composition root, where the
// operator is watching the log.
//
// So every one of these refusals names the row it is about. `ErrPack` alone would be
// a pack that does not load and nothing more, which is the least actionable refusal
// this repository has ever shipped.
// later overlay work item needs: a pack that resolved a formula's provenance would
// take them here. Removing it now and re-adding it later is the change this note
// exists to prevent.
//
// The `versions` are not read yet and the parameter is the seam a later overlay
// work item needs: a pack that resolved a formula's provenance would take
// them here. `unparam` is right that it is unused today and left for that
// reason.
func compile(file packFile, versions Versions) (*Pack, error) { //nolint:unparam // see above.
	if file.System == "" {
		return nil, ErrNoSystem
	}

	if file.System != string(SystemID) {
		return nil, fmt.Errorf("%w: it names %q, and this engine implements %q",
			ErrForeignPack, file.System, SystemID)
	}

	if file.Version == "" {
		return nil, ErrNoPackVersion
	}

	if file.Notation == "" {
		return nil, ErrNoNotation
	}

	// The primary die is the one field with no default in either direction: `faces`
	// decides what an attack rolls and `count` how many, so a pack that omitted either
	// has an attack roll this engine would have to invent. Checked here rather than in
	// `compileDiceFaces` because `dice.sizes` is separately checked below and the two
	// failures want different messages.
	if file.Dice.Primary.Faces == nil || *file.Dice.Primary.Faces < 2 {
		return nil, fmt.Errorf(
			"%w: it declares %v, and an attack roll needs a die of at least two faces",
			ErrNoPrimaryDie, file.Dice.Primary.Faces,
		)
	}

	if len(file.Dice.Sizes) == 0 {
		return nil, fmt.Errorf(
			"%w: it declares no die sizes, so the notation has no die it could roll",
			ErrNoPrimaryDie,
		)
	}

	pack := &Pack{
		system:         file.System,
		version:        file.Version,
		notation:       file.Notation,
		title:          file.Title,
		sizes:          slices.Clone(file.Dice.Sizes),
		primaryFaces:   *file.Dice.Primary.Faces,
		primaryCount:   intOr(file.Dice.Primary.Count, 1),
		toggles:        maps.Clone(file.Toggles),
		hooks:          maps.Clone(file.Hooks),
		abilityAt:      make(map[string]int, len(file.Abilities)),
		conditionAt:    make(map[string]int, len(file.Conditions)),
		masteryAt:      make(map[string]int, len(file.Masteries)),
		dcAt:           make(map[string]int, len(file.Reference.DCs)),
		constants:      maps.Clone(file.Reference.Constants),
		masteryEnabled: boolOr(file.Attack.Mastery.Enabled, true),
		damageCap:      intOr(file.Attack.DamageCap, 0),
	}

	if err := pack.compileAbilities(file.Abilities); err != nil {
		return nil, err
	}

	if err := pack.compileConditions(file.Conditions); err != nil {
		return nil, err
	}

	if err := pack.compileMasteries(file.Masteries); err != nil {
		return nil, err
	}

	if err := pack.compileProficiency(file.Reference.Proficiency); err != nil {
		return nil, err
	}

	if err := pack.compileDCs(file.Reference.DCs); err != nil {
		return nil, err
	}

	if err := pack.compileFormulas(file.Formulas); err != nil {
		return nil, err
	}

	if err := pack.compileKinds(file.Kinds); err != nil {
		return nil, err
	}

	if err := pack.compileHooks(); err != nil {
		return nil, err
	}

	return pack, nil
}

// compileAbilities resolves the ability list and its index.
//
// The **index is a map** because a lookup is a lookup, and the ordered walk every
// derivation does is over the slice — which is the half that could not have been a
// map. A creature's *scores* are keyed by slug and never walked in that form; see
// `creature.go`, which is where S-14.6's map rule would have landed first.
//
// **The only refusals are a missing slug and a repeated one**, and that is worth
// stating because "is this a real ability" is the question two *other* validators ask
// (`validateEffect`, which checks a mastery effect's payload, and the formulas, which
// check every variable they name). An ability row cannot be asked that question about
// itself — it is the definition — so a check here would be the row refusing to be
// itself, and the only row that could pass it would be the first one.
func (p *Pack) compileAbilities(files []abilityFile) error {
	if len(files) == 0 {
		return fmt.Errorf(
			"%w: it declares no abilities, so no formula can bind one",
			ErrNoSuchAbility,
		)
	}

	p.abilities = make([]abilityRow, 0, len(files))

	for idx, file := range files {
		if file.Slug == "" {
			return fmt.Errorf("%w: abilities[%d]", ErrMissingSlug, idx)
		}

		if _, repeated := p.abilityAt[file.Slug]; repeated {
			return fmt.Errorf("%w: abilities %q is declared twice", ErrDuplicateSlug, file.Slug)
		}

		p.abilityAt[file.Slug] = len(p.abilities)
		p.abilities = append(p.abilities, abilityRow{
			slug:  file.Slug,
			label: nonEmpty(file.Label, file.Slug),
			short: nonEmpty(file.Short, file.Slug),
		})
	}

	return nil
}

// declaresAbility reports whether a named ability is in the pack, read from the
// index.
//
// A helper because "is this a real ability" is asked by three validators and a
// slice scan in each of them would be three chances to disagree about it.
func (p *Pack) declaresAbility(slug string) bool {
	_, found := p.abilityAt[slug]

	return found
}

// compileConditions resolves the condition list, its index, and **drops the
// disabled rows**.
//
// Dropping rather than keeping and filtering is the load-bearing half of ADR 0012's
// third house-rule example: a disabled condition must be absent from `Conditions`,
// absent from `ConditionAt`, absent from a view that lists them, and refused by name,
// and the only way all four of those are one decision rather than four is for the
// row to not exist.
func (p *Pack) compileConditions(files []conditionFile) error {
	for idx, file := range files {
		if file.Slug == "" {
			return fmt.Errorf("%w: conditions[%d]", ErrMissingSlug, idx)
		}

		if _, repeated := p.conditionAt[file.Slug]; repeated {
			return fmt.Errorf("%w: conditions %q is declared twice", ErrDuplicateSlug, file.Slug)
		}

		if file.Disabled {
			continue
		}

		// Both parse failures carry `ErrPack` themselves rather than being wrapped here,
		// so that "is this a pack this engine can load" is one question at every call
		// site. `parseAdvantage` is also used by nothing else, so the wrapping is on the
		// sentinel rather than at the caller.
		attack, err := parseAdvantage(defaulted(file.Attack, string(AdvNone)))
		if err != nil {
			return fmt.Errorf("dnd5e: condition %q: %w", file.Slug, err)
		}

		against, err := parseAdvantage(defaulted(file.AttacksAgainst, string(AdvNone)))
		if err != nil {
			return fmt.Errorf("dnd5e: condition %q: %w", file.Slug, err)
		}

		p.conditionAt[file.Slug] = len(p.conditions)
		p.conditions = append(p.conditions, conditionRow{
			slug:            file.Slug,
			label:           nonEmpty(file.Label, file.Slug),
			summary:         file.Summary,
			attack:          attack,
			attacksAgainst:  against,
			speedMultiplier: intOr(file.SpeedMultiplier, 1),
			criticalExempt:  file.CriticalExempt,
		})
	}

	return nil
}

// The effect vocabulary. A closed set, declared once, and the reason an unknown
// `kind` is a load failure rather than a warning: a mastery granting an effect this
// engine does not understand would resolve **as though it granted nothing**, and a
// finesse weapon that silently stopped using the better ability is the kind of bug
// that is reported as "the dice feel wrong" and never traced.
//
// **Named constants rather than four string literals**, and the reason is that each of
// these names is spelled in five places in this package: the load-time validator, the
// requirement check in `hooks.go`, the ability fold in `resolve.go`, the two switch arms
// that read an effect's payload, and the tests that assert the vocabulary is closed.
// Five literals is five chances to disagree about what a finesse weapon does, and the
// disagreement would be silent — the failure is a rule that quietly does nothing.
//
// Read at load rather than ranged over at resolution: a map walk here would be
// S-10.4's forbidden construct, and sorting first makes the order a property of the
// declaration.
const (
	// effectAddAbilityToDamage adds the damage ability's modifier to a hit's damage,
	// which is what a two-handed weapon's mastery grants.
	//
	// **Applied to the damage ability the hook chose**, not to the attack's own: a
	// finesse two-handed weapon rolls damage with the better of two abilities and the
	// mastery applies to *that*, and reading the attack's ability here would add the
	// worse one.
	effectAddAbilityToDamage = "add_ability_to_damage"

	// effectDamageAbilityFromBest rolls the damage with the better of the abilities the
	// effect names. The *pairing* is data — the row says which two — and the comparison
	// is the hook's, because "the better of these" is a fold no row can answer alone.
	effectDamageAbilityFromBest = "damage_ability_from_best"

	// effectRequiresAbilityAtLeast and effectRequiresAbilityBelow are requirements
	// rather than effects: they do not confer anything, they gate whether the mastery
	// applies at all.
	effectRequiresAbilityAtLeast = "requires_ability_at_least"
	effectRequiresAbilityBelow   = "requires_ability_below"
)

// effectKinds is every effect kind this engine ships, in declaration order.
//
// `TestTheEffectVocabularyIsClosedInBothDirections` requires the set to be exactly
// these four and requires every one of them to be read by a Go rule, so adding a fifth
// is a change to this package *and* a decision about the data/engine boundary — which
// is ADR 0045's question and belongs in a diff rather than in a map entry.
var effectKinds = []string{
	effectAddAbilityToDamage,
	effectDamageAbilityFromBest,
	effectRequiresAbilityAtLeast,
	effectRequiresAbilityBelow,
}

// compileMasteries resolves the mastery list, its index, and every effect's kind.
//
// The four kinds are the whole vocabulary this engine ships, and each maps to one
// Go rule in `resolve.go`. A fifth would be a fifth rule and, by ADR 0045's own
// argument, a question about whether the pairing or the procedure is what changed.
func (p *Pack) compileMasteries(files []masteryFile) error {
	for idx, file := range files {
		if file.Slug == "" {
			return fmt.Errorf("%w: masteries[%d]", ErrMissingSlug, idx)
		}

		if _, repeated := p.masteryAt[file.Slug]; repeated {
			return fmt.Errorf("%w: masteries %q is declared twice", ErrDuplicateSlug, file.Slug)
		}

		row := masteryRow{
			slug:    file.Slug,
			label:   nonEmpty(file.Label, file.Slug),
			summary: file.Summary,
		}

		for _, effect := range file.Effects {
			if !slices.Contains(effectKinds, effect.Kind) {
				return fmt.Errorf("%w: mastery %q grants %q, which is one of %v",
					ErrUnknownEffect, file.Slug, effect.Kind, effectKinds)
			}

			if err := p.validateEffect(file.Slug, effect); err != nil {
				return err
			}

			row.effects = append(row.effects, effect)
		}

		p.masteryAt[file.Slug] = len(p.masteries)
		p.masteries = append(p.masteries, row)
	}

	return nil
}

// validateEffect checks that an effect's payload fields are the ones its kind uses.
//
// **Refused rather than ignored**, and the reason is the same one `KnownFields(true)`
// is: an effect written `{kind: requires_ability_at_least, ability: dexterity}`
// (omitting `value`) is a rule that compares an ability against zero, which resolves
// and is wrong. Refusing is what makes "the pack is either right or does not load"
// true rather than "the pack is right unless you misspelled something".
func (p *Pack) validateEffect(mastery string, effect effectFile) error {
	switch effect.Kind {
	case effectAddAbilityToDamage:
		if err := noPayload(effectAddAbilityToDamage, effect); err != nil {
			return fmt.Errorf("dnd5e: mastery %q: %w", mastery, err)
		}
	case effectDamageAbilityFromBest:
		if len(effect.Of) < 2 {
			// Under `ErrUnknownEffect` and not bare, and the reason is the composition
			// root: every refusal in this file is supposed to satisfy `ErrPack` so a
			// registrar asks one question. A refusal that does not is a refusal the caller
			// has to recognise by reading the message.
			return fmt.Errorf(
				"%w: mastery %q: %s names %v, and a comparison needs two abilities to compare",
				ErrUnknownEffect, mastery, effectDamageAbilityFromBest, effect.Of,
			)
		}

		for _, slug := range effect.Of {
			if !p.declaresAbility(slug) {
				return fmt.Errorf("%w: mastery %q names %q", ErrNoSuchAbility, mastery, slug)
			}
		}
	case effectRequiresAbilityAtLeast, effectRequiresAbilityBelow:
		if effect.Ability == "" {
			return fmt.Errorf(
				"%w: mastery %q: %q names no ability", ErrUnknownEffect, mastery, effect.Kind,
			)
		}

		if !p.declaresAbility(effect.Ability) {
			return fmt.Errorf("%w: mastery %q names %q", ErrNoSuchAbility, mastery, effect.Ability)
		}
	}

	return nil
}

// noPayload refuses an effect that sets a field its kind does not read.
func noPayload(kind string, effect effectFile) error {
	switch {
	case len(effect.Of) > 0:
		return fmt.Errorf("%w: %q reads no list and one was given", ErrUnknownEffect, kind)
	case effect.Ability != "":
		return fmt.Errorf("%w: %q names no ability and one was given", ErrUnknownEffect, kind)
	case effect.Value != 0:
		return fmt.Errorf("%w: %q reads no value and one was given", ErrUnknownEffect, kind)
	default:
		return nil
	}
}

// compileProficiency resolves the level-to-bonus table and requires it to be
// **gapless and ordered**.
//
// Both halves are refusals rather than behaviour. A gap means a creature at a level
// in it has no proficiency bonus, and the alternative — a formula defaulting it to
// zero — is a silent misresolution of the exact kind §10.8 refuses a resume to
// prevent. An overlap means two rows answer, and which one is a file-order accident.
func (p *Pack) compileProficiency(files []proficiencyFile) error {
	if len(files) == 0 {
		return fmt.Errorf("%w: it declares no bands", ErrNoProficiency)
	}

	p.proficiency = make([]proficiencyRow, 0, len(files))

	for idx, file := range files {
		if file.From <= 0 || file.To < file.From {
			return fmt.Errorf(
				"%w: band %d-%d is not a range of levels",
				ErrNoProficiency,
				file.From,
				file.To,
			)
		}

		if idx > 0 && file.From != p.proficiency[idx-1].to+1 {
			return fmt.Errorf(
				"%w: band %d-%d does not follow %d-%d, so a level is covered twice or not at all",
				ErrNoProficiency, file.From, file.To,
				p.proficiency[idx-1].from, p.proficiency[idx-1].to,
			)
		}

		p.proficiency = append(p.proficiency, proficiencyRow{
			from:  file.From,
			to:    file.To,
			bonus: file.Bonus,
		})
	}

	return nil
}

// compileDCs resolves the named difficulty classes.
func (p *Pack) compileDCs(files []dcFile) error {
	p.dcs = make([]dcRow, 0, len(files))

	for idx, file := range files {
		if file.Slug == "" {
			return fmt.Errorf("%w: reference.dcs[%d]", ErrMissingSlug, idx)
		}

		if _, repeated := p.dcAt[file.Slug]; repeated {
			return fmt.Errorf("%w: reference.dcs %q is declared twice", ErrDuplicateSlug, file.Slug)
		}

		p.dcAt[file.Slug] = len(p.dcs)
		p.dcs = append(
			p.dcs,
			dcRow{slug: file.Slug, label: nonEmpty(file.Label, file.Slug), dc: file.DC},
		)
	}

	return nil
}

// compileFormulas parses every formula and reports the first one that does not
// parse, naming it.
//
// **At load, not at resolution.** A malformed formula is a broken pack, and a
// resolution that discovered it would be discovering it with a campaign on the
// other end — while `compile` refusing means the composition root does, at startup,
// where the operator is reading the log for something else.
//
// The map is walked in **sorted key order** so that the refusal names the same
// formula on every machine: a formula whose name is "first" would otherwise depend
// on Go's map iteration, and a startup failure that names a different row each run
// is a startup failure nobody can act on.
func (p *Pack) compileFormulas(files map[string]formulaFile) error {
	p.formulas = make(map[string]compiledFormula, len(files))
	p.formulaFor = make([]string, 0, len(files))

	// `slices.Sorted(maps.Keys(…))` rather than a range and a sort afterwards: the sort
	// makes the *result* deterministic, which is why this package shipped with it, but
	// `determinism`'s rule is about the construct — a range over a map — because a range is
	// one edit away from reaching a value and the sort is the thing somebody removes
	// first when a startup refusal names the wrong formula.
	for _, name := range slices.Sorted(maps.Keys(files)) {
		compiled, err := p.compileFormula(name, files[name])
		if err != nil {
			return err
		}

		p.formulas[name] = compiled
		p.formulaFor = append(p.formulaFor, name)
	}

	return nil
}

// FormulaNames is the list of formulas a resolution may read, in sorted order.
//
// Sorted because it is derived from a map and a listing that differed between runs
// would be a resolution that could. See `compileFormulas`.
func (p *Pack) FormulaNames() []string { return slices.Clone(p.formulaFor) }

// Formula returns a compiled formula by name.
func (p *Pack) Formula(name string) (compiledFormula, bool) {
	compiled, found := p.formulas[name]

	return compiled, found
}

// compileKinds resolves the kinds this system recognises, which is §10.4's fourth
// reason a pack exists and the single source of `ContentKinds`.
//
// Refuses an empty list even though `rules.Validate` does not: a system declaring no
// kinds passes the contract and defeats the reason the pack carries them, and the
// symptom — every 5e page degrading to prose — is one nobody traces back here.
func (p *Pack) compileKinds(files []kindFile) error {
	if len(files) == 0 {
		return ErrNoKinds
	}

	p.kinds = make([]rules.Kind, 0, len(files))
	p.kindLabels = make(map[rules.Kind]string, len(files))

	for idx, file := range files {
		kind := rules.Kind(file.Slug)

		if file.Slug == "" {
			return fmt.Errorf("%w: kinds[%d]", ErrMissingSlug, idx)
		}

		if !kind.Valid() {
			return fmt.Errorf("%w: kinds %q is not a usable kind name", ErrPack, file.Slug)
		}

		if rules.IsSemiplaneKind(kind) {
			// `rules.Validate` refuses this too, at registration. Here it is a load
			// failure naming the *pack*, which is the more useful half: a pack listing
			// `token` is a pack author who thinks `token` is theirs, and the
			// composition root is where that gets said.
			return fmt.Errorf(
				"%w: kinds %q, which semiplane owns in every build",
				ErrPack,
				file.Slug,
			)
		}

		if _, repeated := p.kindLabels[kind]; repeated {
			return fmt.Errorf("%w: kinds %q is declared twice", ErrDuplicateSlug, file.Slug)
		}

		p.kinds = append(p.kinds, kind)
		p.kindLabels[kind] = nonEmpty(file.Label, file.Slug)
	}

	return nil
}

// nonEmpty returns value, or fallback when value is empty.
//
// A pack row's `label` is display copy for a status page and a view, and a blank one
// is a blank one; the slug is always there and always readable, so it is the
// fallback rather than an error. Refusing an unlabelled row would make `label`
// mandatory for a reason no reader has.
func nonEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

// defaulted returns value, or fallback when value is empty.
func defaulted(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

// intOr returns the pointed-to value, or fallback when the pointer is nil.
func intOr(value *int, fallback int) int {
	if value == nil {
		return fallback
	}

	return *value
}

// boolOr returns the pointed-to value, or fallback when the pointer is nil.
func boolOr(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}

	return *value
}

// slicesClone is `slices.Clone`, named for the one-line rule that a package holding
// a `[]T` and handing it out must clone it.
//
// `rules.SemiplaneKinds`'s comment gives the reasoning and this package follows it
// exactly: an exported slice handed to a caller that appended to it is package state
// changing under a registry.
func slicesClone[T any](values []T) []T {
	if values == nil {
		return nil
	}

	return append([]T(nil), values...)
}
