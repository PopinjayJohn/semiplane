package dnd5e

import (
	"fmt"
	"maps"
	"slices"
)

// # Merge: the one rule, stated once
//
// **A collection is merged by slug; a mapping is merged by key; a scalar is replaced
// when it is declared and inherited when it is not.** One rule, and every part of it
// follows from §10.4's claim that the differences between editions "show up as
// reviewable data diffs".
//
// It follows from it because a diff a reviewer can read is a diff that *points at
// something*. An index-keyed merge turns "2024 adds `inspiration`" into "every row
// after it changed", and a reviewer who has seen one of those learns to skim. A
// slug-keyed merge turns it into one added row and nothing else, and the reviewer
// reads it in the time it takes to look.
//
// # Why an overlay is a *partial* pack
//
// Because 2014 and 2024 are ~90% identical, which is the fact §10.4's split rests
// on. An overlay that had to restate everything would be a second base pack, and the
// two would be compared by a human reading two hundred lines to find the three that
// differ — which is the exercise this whole design removes.
//
// # What an overlay may *not* do
//
// It may not change the notation's die faces without restating `dice.sizes`, and it
// may not introduce a hook this engine does not ship. Both are refused at load, and
// the reason is the same one `ErrUnknownHook` gives: a pack that named a rule this
// build does not have would resolve **without** it and report success, which is the
// one outcome a ruleset gate cannot detect.

// Name returns the overlay's display name.
func (o Overlay) Name() string { return o.name }

// Version returns the overlay's version, which is the third component of
// `realtime.Fingerprint` — empty for no overlay, which `realtime` explicitly allows
// because §10.4 makes a standalone pack a first-class shape.
func (o Overlay) Version() string { return o.file.Version }

// apply merges this overlay's declarations over a base file and returns the file the
// compiler reads.
//
// **Unexported, and `New` is the only caller**, and that is deliberate: merging is not
// an operation a caller may perform, because the answer must be compiled once and then
// held. A pack that could be re-merged between two resolutions of one campaign would
// make resolution a function of more than `(state, intent, seed)`, which is the thing
// S-10.4 exists to prevent. There is no method on `Engine` that merges anything.
//
// Merging the **files** rather than the compiled packs is what makes an overlay apply
// to exactly what the base declared. The two are identical for the shipped pack and
// would diverge the first time `compile` resolved a default the overlay had to
// override — and an overlay that silently did not apply to that default would be
// invisible: the pack would load, the fingerprint would record the overlay's version,
// and the campaign would resolve under the base's rules while claiming the overlay's.
func (o Overlay) apply(base packFile) (packFile, error) {
	return mergeFiles(base, o.file)
}

// Zero reports whether the overlay is absent.
//
// A predicate and not `Overlay == Overlay{}`, because a comparison would also answer
// `true` for an overlay that declared nothing but a version — which is a pack
// somebody will write and which contributes no rows.
func (o Overlay) Zero() bool { return o.file.Version == "" && o.name == "" }

// mergeFiles applies an overlay's declarations over a base's, and returns the file
// the compiler reads.
//
// **Every collection goes through `mergeBySlug`, including the proficiency table.**
// The proficiency bands carry no slug because a level range is its key and inventing a
// slug for them would mean a YAML author has to keep two keys in step — so the table
// gets its own keyed merge and `proficiencyFileFrom` derives a stable key from the
// band's start.
func mergeFiles(base, overlay packFile) (packFile, error) {
	merged := packFile{
		// Scalars: replace when the overlay declares one.
		System:   firstNonEmpty(overlay.System, base.System),
		Version:  firstNonEmpty(overlay.Version, base.Version),
		Notation: firstNonEmpty(overlay.Notation, base.Notation),
		Title:    firstNonEmpty(overlay.Title, base.Title),
	}

	// Mappings: merged per key, because a mapping's key *is* its identity and a
	// whole-list replacement would be a way to remove a switch by accident.
	merged.Toggles = mergeMapping(base.Toggles, overlay.Toggles)
	merged.Hooks = mergeStrings(base.Hooks, overlay.Hooks)
	merged.Formulas = mergeFormulas(base.Formulas, overlay.Formulas)

	merged.Dice = diceSpec{
		Sizes: overrideSlice(base.Dice.Sizes, overlay.Dice.Sizes),
		Primary: dieSpec{
			Faces: firstInt(base.Dice.Primary.Faces, overlay.Dice.Primary.Faces),
			Count: firstInt(base.Dice.Primary.Count, overlay.Dice.Primary.Count),
		},
	}

	// `err` is declared once and reused, because six merges can each fail and six
	// `if err :=` blocks would each need the same two lines. The order is **collection
	// order in the file**, not alphabetical and not by size, so that a pack with three
	// problems reports the one a reader would meet first.
	var err error

	merged.Reference = referenceFile{
		Constants:   mergeMapping(base.Reference.Constants, overlay.Reference.Constants),
		Proficiency: mergeProficiency(base.Reference.Proficiency, overlay.Reference.Proficiency),
	}

	if merged.Reference.DCs, err = mergeBySlug(
		base.Reference.DCs, overlay.Reference.DCs, dcSlug,
	); err != nil {
		return packFile{}, err
	}

	merged.Attack = attackFile{
		Mastery: masteryToggle{
			Enabled: firstBool(base.Attack.Mastery.Enabled, overlay.Attack.Mastery.Enabled),
		},
		DamageCap: firstInt(base.Attack.DamageCap, overlay.Attack.DamageCap),
	}

	if merged.Abilities, err = mergeBySlug(
		base.Abilities,
		overlay.Abilities,
		abilitySlug,
	); err != nil {
		return packFile{}, err
	}

	if merged.Conditions, err = mergeBySlug(
		base.Conditions,
		overlay.Conditions,
		conditionSlug,
	); err != nil {
		return packFile{}, err
	}

	if merged.Masteries, err = mergeBySlug(
		base.Masteries,
		overlay.Masteries,
		masterySlug,
	); err != nil {
		return packFile{}, err
	}

	if merged.Kinds, err = mergeBySlug(base.Kinds, overlay.Kinds, kindSlug); err != nil {
		return packFile{}, err
	}

	return merged, nil
}

// The keys every collection merges by, named so the six calls in `mergeFiles` read
// as one table rather than six closures.
//
// **One function per collection, all returning `Slug`**, and the reason is
// exhaustiveness rather than brevity: with the closure inline, adding a collection
// means writing a `func(row T) string { return row.Slug }` and a reader cannot see
// that every key is the slug without reading six of them. Here the key is stated once
// per collection, and `TestEveryCollectionIsMergedByItsSlug` is what holds that they
// all still are — a collection added to `packFile` and not to `mergeFiles` is the
// failure, and it would otherwise be an overlay that silently never applied to it.
func abilitySlug(row abilityFile) string     { return row.Slug }
func conditionSlug(row conditionFile) string { return row.Slug }
func masterySlug(row masteryFile) string     { return row.Slug }
func kindSlug(row kindFile) string           { return row.Slug }
func dcSlug(row dcFile) string               { return row.Slug }

// mergeBySlug merges one collection by its slug, replacing a row an overlay restates
// and appending a row it adds.
//
// **Append, never insert.** A new row goes on the end rather than in a sorted
// position, and the reason is that declaration order is the *author's* order and is
// what every ordered walk in this package reads: the display order of abilities, the
// order a stat block renders conditions in, the order a resolver folds granted
// masteries in. Sorting would silently reorder a base pack's rows because an overlay
// happened to add one, and a reordering that reaches a mutation's bytes is a replay
// hazard — which is why `creature.pruneConditions` sorts *by the pack* rather than
// leaving the order conditions were acquired in.
//
// The empty-slug refusal is here rather than at each of the six call sites because the
// slug is the key this function merges on: an entry with no slug has no identity, and
// appending it would make two of them one row whichever arrived last.
func mergeBySlug[T any](base, overlay []T, key func(T) string) ([]T, error) {
	merged := slicesClone(base)
	position := make(map[string]int, len(base))

	for idx := range merged {
		position[key(merged[idx])] = idx
	}

	for _, entry := range overlay {
		name := key(entry)
		if name == "" {
			return nil, fmt.Errorf("%w: a row has no slug to be merged by", ErrMissingSlug)
		}

		where, replaced := position[name]
		if !replaced {
			position[name] = len(merged)
			merged = append(merged, entry)

			continue
		}

		merged[where] = entry
	}

	return merged, nil
}

// mergeProficiency merges the level bands, keyed by their starting level.
//
// **A band whose `from` matches replaces it whole**, so an overlay may correct a
// band's bonus without restating its range, and may restate a range without touching
// a bonus. Both are single-field edits to one row, which is what makes an edition
// difference reviewable.
func mergeProficiency(base, overlay []proficiencyFile) []proficiencyFile {
	merged := slicesClone(base)
	position := make(map[int]int, len(base))

	for idx := range merged {
		position[merged[idx].From] = idx
	}

	for _, band := range overlay {
		where, replaced := position[band.From]

		if !replaced {
			position[band.From] = len(merged)
			merged = append(merged, band)

			continue
		}

		// A band's range is corrected by restating it, and a restated range whose `to`
		// is absent would be a band of zero length. The compiler refuses that, which is
		// the right place: the refusal names the band and the pack, and an overlay
		// author gets told rather than shipped a gap.
		merged[where] = band
	}

	// Sorted by `from`, because the compiler's gap check and `Proficiency`'s lookup
	// both walk in order, and an overlay that appended a band out of order would
	// otherwise fail to compile for a reason about ordering rather than about the
	// band.
	slices.SortStableFunc(merged, func(a, b proficiencyFile) int {
		return a.From - b.From
	})

	return merged
}

// mergeFormulas merges the formula table per key.
//
// **A formula's fields are replaced individually rather than the row**, because a
// formula has three of them (`expr`, `for`, `clamp`) and an overlay changing one
// should be able to. Replacing the row would mean restating all three, which is three
// lines of diff per one line of change — the exact noise §10.4 says the split removes.
func mergeFormulas(base, overlay map[string]formulaFile) map[string]formulaFile {
	merged := make(map[string]formulaFile, len(base)+len(overlay))

	// **Both walks are over sorted key lists**, not over the maps: `mergeFormulas`
	// returns a map and its caller `compile` sorts before it compiles anything, so the
	// result was already deterministic — but `determinism`'s rule is about the construct
	// rather than the outcome, and a range is one edit away from a value reaching a
	// result. `maps.Copy` would be wrong here: a formula is merged field by field, so the
	// overlay's row has to be combined with the base's rather than replace it.
	for _, name := range slices.Sorted(maps.Keys(base)) {
		merged[name] = base[name]
	}

	for _, name := range slices.Sorted(maps.Keys(overlay)) {
		row := overlay[name]
		existing, present := merged[name]

		if !present {
			merged[name] = row

			continue
		}

		merged[name] = formulaFile{
			Expr:  firstNonEmpty(row.Expr, existing.Expr),
			For:   firstNonEmpty(row.For, existing.For),
			Clamp: firstClamp(existing.Clamp, row.Clamp),
		}
	}

	return merged
}

// firstClamp returns the overlay's clamp if it declared one, and otherwise the base's
// **merged with the overlay's fields**.
//
// The merge rather than a straight choice is what makes `clamp: {max: "12"}` work:
// an overlay raising a cap is one field, and restating both bounds would be a
// two-line diff for a one-line change. An overlay declaring `min` and no `max` gets
// the base's maximum, which is what "declared" means in every other field here.
func firstClamp(base, overlay *clampFile) *clampFile {
	switch {
	case base == nil && overlay == nil:
		return nil
	case overlay == nil:
		return &clampFile{Min: base.Min, Max: base.Max}
	case base == nil:
		return &clampFile{Min: firstNonEmpty(overlay.Min, "0"), Max: overlay.Max}
	default:
		return &clampFile{
			Min: firstNonEmpty(overlay.Min, base.Min),
			Max: firstNonEmpty(overlay.Max, base.Max),
		}
	}
}

// mergeMapping merges a map of values per key, per overlay entry.
func mergeMapping[V any](base, overlay map[string]V) map[string]V {
	merged := make(map[string]V, len(base)+len(overlay))

	maps.Copy(merged, base)
	maps.Copy(merged, overlay)

	return merged
}

// mergeStrings merges a map of strings per key, per overlay entry.
//
// A named function rather than a generic, and the reason is worth recording: a
// generic would be one fewer name and the two call sites would both read
// `mergeMapping`, so a reader looking for what happens to a hook name would have to
// check which of two identical signatures is in scope. One line of duplication buys
// a name that says what it holds.
func mergeStrings(base, overlay map[string]string) map[string]string {
	return mergeMapping(base, overlay)
}

// overrideSlice returns the overlay's slice when it declared one, and the base's
// otherwise.
//
// **A slice is replaced, not merged.** `dice.sizes` is the one place a list is not
// keyed by slug, because the set *is* the value: there is no identity to merge on and
// an overlay that added one face to a seven-face list would be an overlay whose list
// length is not its declared count.
func overrideSlice[T any](base, overlay []T) []T {
	if len(overlay) > 0 {
		return slices.Clone(overlay)
	}

	return slices.Clone(base)
}

// firstNonEmpty returns the overlay's value when it declared one.
func firstNonEmpty(overlay, base string) string {
	if overlay != "" {
		return overlay
	}

	return base
}

// firstInt returns the overlay's value when it declared one.
//
// **Pointers, not zero values**, and that is the whole of ADR 0012's first two
// house-rule examples in one line: `crit_multiplier: 0` and `damage_cap: 0` are both
// meaningful declarations, and a `firstNonZeroInt` helper would make them
// unexpressible — a house rule that removed a cap by setting it to zero would be a
// house rule that could not be written.
func firstInt(base, overlay *int) *int {
	if overlay != nil {
		return overlay
	}

	return base
}

// firstBool returns the overlay's value when it declared one, and the reason
// `mastery.enabled` is a pointer rather than a bool is the same one `firstInt` gives:
// `enabled: false` is 2014's entire structural difference from 2024.
func firstBool(base, overlay *bool) *bool {
	if overlay != nil {
		return overlay
	}

	return base
}
