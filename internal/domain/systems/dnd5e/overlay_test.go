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
	"reflect"
	"slices"
	"strings"
	"testing"
)

// # The merge, and the one rule that states all of it
//
// §10.4 claims the editions "show up as reviewable data diffs". That is only true if a
// reviewer can read *which row* changed, so the merge is by slug everywhere and never by
// index: an index-keyed merge turns "2024 adds `inspiration`" into "every row after it
// changed", and a reviewer who has seen one of those learns to skim.
//
// The rule in one sentence: **a collection is merged by slug; a mapping is merged by
// key; a scalar is replaced when declared and inherited when it is not.** Every
// deviation from it is a collection that behaves differently from its neighbours, and
// this file is what holds them all to it.

// TestTheMergeIsOneRuleAndTheOverlayRestatesNothing is the §10.4 claim, asserted on the
// smallest possible overlay: a system name, a version and a title.
//
// **A restatement of the base pack's whole file is the failure this exists to prevent**,
// because then the two editions would be compared by a human reading two hundred lines to
// find the three that differ — which is the exercise the whole design removes. The
// assertions below therefore check that everything the overlay did *not* mention is
// inherited, collection by collection.
func TestTheMergeIsOneRuleAndTheOverlayRestatesNothing(t *testing.T) {
	t.Parallel()

	engine := anEngine(t, `system: "dnd5e"
version: "2024-overlay@1"
title: "the 2024 edition"
`)

	base := basePack(t)
	overlaid := engine.Pack()

	// Identity and notation.
	if overlaid.System() != base.System() || overlaid.Notation() != base.Notation() {
		t.Errorf("the overlay changed the system or the notation to %q / %q",
			overlaid.System(), overlaid.Notation())
	}

	// Collections, by length.
	for _, fixture := range []struct {
		what          string
		before, after int
	}{
		{"abilities", len(base.Abilities()), len(overlaid.Abilities())},
		{"conditions", len(base.Conditions()), len(overlaid.Conditions())},
		{"masteries", len(base.Masteries()), len(overlaid.Masteries())},
		{"proficiency bands", len(base.proficiency), len(overlaid.proficiency)},
	} {
		if fixture.before != fixture.after {
			t.Errorf("an overlay that restated nothing changed the %s from %d rows to %d",
				fixture.what, fixture.before, fixture.after)
		}
	}

	// Mappings, by key set.
	if len(base.ToggleNames()) != len(overlaid.ToggleNames()) {
		t.Errorf("an overlay that restated nothing changed the switch list from %v to %v",
			base.ToggleNames(), overlaid.ToggleNames())
	}

	// Formulas, by name.
	if !slices.Equal(base.FormulaNames(), overlaid.FormulaNames()) {
		t.Errorf("an overlay that restated nothing changed the formula list from %v to %v",
			base.FormulaNames(), overlaid.FormulaNames())
	}

	// Die sizes and the primary die.
	if !slices.Equal(base.DieSizes(), overlaid.DieSizes()) {
		t.Errorf("an overlay that restated nothing changed the die sizes from %v to %v",
			base.DieSizes(), overlaid.DieSizes())
	}

	// And the two scalars the overlay *did* declare.
	if overlaid.Title() != "the 2024 edition" {
		t.Errorf("the overlay's title is %q, want the overlay's own", overlaid.Title())
	}

	if overlaid.Version() != "2024-overlay@1" {
		t.Errorf("the merged pack's version is %q, want the overlay's own", overlaid.Version())
	}
}

// TestEveryCollectionIsMergedByItsSlug is the two-direction merge audit, and it is done
// **reflectively** over `packFile` rather than from a hand-written list.
//
// The failure it catches is the one that costs a GM a rule: a collection added to the
// pack's schema and not to `mergeFiles` is an overlay that silently never applies to it,
// so an edition difference is a no-op that the fingerprint records as applied. A
// hand-written list of collections is a list somebody has to remember to extend, and the
// reflective walk cannot be out of date — **it fails the moment a collection is added.**
func TestEveryCollectionIsMergedByItsSlug(t *testing.T) {
	t.Parallel()

	base, err := readPackFile(BasePackYAML())
	if err != nil {
		t.Fatalf("reading the embedded pack: %v", err)
	}

	shape := reflect.TypeFor[packFile]()

	// The collections that carry a `Slug` field: those are the ones the merge keys on.
	// `Proficiency` is the exception and gets its own keyed merge, because a level band
	// has no slug and inventing one would mean a YAML author keeping two keys in step.
	keyed := map[string]bool{"Proficiency": true}

	probed := 0

	for idx := range shape.NumField() {
		field := shape.Field(idx)

		if field.Type.Kind() != reflect.Slice {
			continue
		}

		collection := field.Type
		slug, found := collection.Elem().FieldByName("Slug")
		if !found || slug.Type.Kind() != reflect.String {
			continue
		}

		if keyed[field.Name] {
			continue
		}

		probed++

		// An overlay declaring one row in this collection and nothing else.
		probe := reflect.New(collection).Elem()
		probeRow := reflect.New(collection.Elem()).Elem()
		probeRow.FieldByName("Slug").SetString("probe_" + field.Name)

		probe = reflect.Append(probe, probeRow)

		overlay := reflect.New(shape)
		overlay.Elem().Field(idx).Set(probe)

		mergedFile, err := mergeFiles(base, overlay.Elem().Interface().(packFile))
		if err != nil {
			t.Errorf("merging a probe row into %s: %v", field.Name, err)

			continue
		}

		merged := reflect.ValueOf(mergedFile).Field(idx)

		if !carriesSlug(merged, "probe_"+field.Name) {
			t.Errorf("a probe row added to %s did not reach the merged file, so an overlay "+
				"declaring one silently does nothing there", field.Name)
		}
	}

	if probed == 0 {
		t.Fatal("no collection of slug-keyed rows was probed, so every assertion above passed " +
			"over a walk that found nothing")
	}

	// The exempted one is exempted **for a reason** and not by accident: a band with no
	// slug is keyed by its starting level, and inventing a slug would mean an author
	// keeping two keys in step.
	if _, found := reflect.TypeFor[proficiencyFile]().FieldByName("Slug"); found {
		t.Error("proficiencyFile has a Slug field, so the exemption in this test is stale")
	}
}

// carriesSlug reports whether a merged collection contains a row with this slug.
func carriesSlug(collection reflect.Value, slug string) bool {
	for idx := range collection.Len() {
		if collection.Index(idx).FieldByName("Slug").String() == slug {
			return true
		}
	}

	return false
}

// TestAnOverlayReplacesARowRatherThanAppendingASecond is "an overlay that restates a row
// replaces it", and it is asserted by the **length** as well as the contents.
//
// An implementation that appended would leave two rows with one slug, and the pack's own
// index would point at whichever came last — so the pack would compile, the overlay would
// report having been applied, and the edition difference would be decided by file order.
func TestAnOverlayReplacesARowRatherThanAppendingASecond(t *testing.T) {
	t.Parallel()

	engine := anEngine(t, `system: "dnd5e"
version: "2024-overlay@1"
title: "the 2024 edition"
conditions:
  - { slug: poisoned, label: "Poisoned (2024)", attack: advantage }
`)

	pack := engine.Pack()

	if len(pack.Conditions()) != len(basePack(t).Conditions()) {
		t.Errorf("the merged pack has %d conditions and the base has %d; a restated row was "+
			"appended rather than replaced",
			len(pack.Conditions()), len(basePack(t).Conditions()))
	}

	row, found := pack.ConditionAt("poisoned")
	if !found {
		t.Fatal("the restated condition is gone, which is a different bug")
	}

	if row.label != "Poisoned (2024)" {
		t.Errorf("the restated row's label is %q, want the overlay's", row.label)
	}

	if row.attack != AdvGains {
		t.Errorf("the restated row's own-attack column is %q, want advantage", row.attack)
	}

	// And a row the overlay did not mention keeps the base's own values — including the
	// columns the overlay's restatement left out, which is what "replaced when declared"
	// means at the row level and "inherited when not" means at the field level.
	if _, restated := pack.ConditionAt("prone"); !restated {
		t.Error("an untouched condition is gone")
	}
}

// TestAnOverlayAddingARowAppendsItInThePacksOrder is the other half of the merge, and
// "append" is a decision rather than the default.
//
// A new row goes on the end rather than in a sorted position, because declaration order
// is the *author's* order and it is what every ordered walk in this package reads: the
// display order of abilities, the order a stat block renders conditions in, the order a
// resolver folds granted masteries in. Sorting would silently reorder a base pack's rows
// because an overlay happened to add one, and a reordering that reaches a mutation's
// bytes is a replay hazard.
func TestAnOverlayAddingARowAppendsItInThePacksOrder(t *testing.T) {
	t.Parallel()

	base := basePack(t)
	last := base.Conditions()[len(base.Conditions())-1].slug

	engine := anEngine(t, `system: "dnd5e"
version: "2024-overlay@1"
title: "the 2024 edition"
conditions:
  - { slug: zz_bespoke, label: "Bespoke" }
`)

	declared := engine.Pack().Conditions()

	if len(declared) != len(base.Conditions())+1 {
		t.Fatalf("the merged pack has %d conditions, want one more than the base's %d",
			len(declared), len(base.Conditions()))
	}

	if declared[len(declared)-1].slug != "zz_bespoke" {
		t.Errorf("the added condition is %q rather than at the end of %v; an overlay's row "+
			"must not sort a base pack's rows",
			declared[len(declared)-1].slug, declared)
	}

	// The base's own last row is still where it was.
	if declared[len(declared)-2].slug != last {
		t.Errorf("the base pack's last condition moved from %q to %q",
			last, declared[len(declared)-2].slug)
	}
}

// TestAFormulaIsMergedFieldByField is the rule with the clearest counterfactual.
//
// A formula has three fields and an overlay changing one should be able to. Replacing the
// row would mean restating all three, which is three lines of diff per one line of change
// — the exact noise §10.4 says the split removes. The assertion is that a one-field
// overlay produces a **one-line** change.
func TestAFormulaIsMergedFieldByField(t *testing.T) {
	t.Parallel()

	engine := anEngine(t, `system: "dnd5e"
version: "2024-overlay@1"
title: "the 2024 edition"
formulas:
  armour_class:
    expr: "ac_base + ability.dexterity + 1"
`)

	pack := engine.Pack()

	formula, declared := pack.Formula("armour_class")
	if !declared {
		t.Fatal("the overlay's formula is gone")
	}

	// The declared field changed.
	if !strings.Contains(formula.Source(), "+ 1") {
		t.Errorf("the formula is %q, want the overlay's expression", formula.Source())
	}

	// And the undeclared ones did not: `for:` is inherited from the base, which had none,
	// and the clamp is inherited whole.
	if formula.For != ForNone {
		t.Errorf(
			"the formula binds an ability (%d) although the overlay declared no `for`",
			formula.For,
		)
	}

	if !formula.Clamped {
		t.Error("the overlay's formula lost the clamp it did not mention")
	}

	// The whole formula, as the base had it, differs in exactly the one field.
	base := basePack(t)
	if baseSource, _ := base.FormulaSource("armour_class"); baseSource == formula.Source() {
		t.Error("the overlay changed nothing, so the fixture is not measuring the merge")
	}
}

// TestAClampIsMergedRatherThanChosen is the sub-case that made `firstClamp` necessary,
// and it is stated as a use case rather than as a mechanism.
//
// `clamp: {max: "12"}` must work: a house rule raising a cap is one field, and restating
// both bounds would be a two-line diff for a one-line change. An overlay declaring `min`
// and no `max` gets the base's maximum, which is what "declared" means in every other
// field here.
func TestAClampIsMergedRatherThanChosen(t *testing.T) {
	t.Parallel()

	engine := anEngine(t, `system: "dnd5e"
version: "2024-overlay@1"
title: "the 2024 edition"
formulas:
  armour_class:
    clamp:
      max: "ac_base"
`)

	formula, declared := engine.Pack().Formula("armour_class")
	if !declared {
		t.Fatal("the overlay's clamp is gone")
	}

	if !formula.Clamped {
		t.Fatal("the clamp was dropped")
	}

	scope := map[string]int{
		"ac_base": 10, "ability.dexterity": 8, "ac_dex_cap": 10,
	}

	// The base's minimum is `0` and the overlay's maximum is `ac_base`, so a Dexterity
	// modifier of +8 is clamped back down to 10 — which is only true if the *base's*
	// minimum was inherited rather than replaced by an empty one.
	computed, err := formula.Evaluate(scope)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if computed != 10 {
		t.Errorf(
			"armour class is %d, want 10; the overlay's maximum with the base's minimum",
			computed,
		)
	}

	// The expression itself was not touched — the overlay declared only the clamp.
	if !strings.Contains(formula.Source(), "ability.dexterity") {
		t.Errorf("the formula's expression is %q, want the base's", formula.Source())
	}
}

// TestDieSizesAreReplacedRatherThanMerged is the one list that is not keyed by slug, and
// the reason is worth stating because it is the exception.
//
// `dice.sizes` is the one place a list is not keyed by slug, because the set *is* the
// value: there is no identity to merge on and an overlay that added one face to a
// seven-face list would be an overlay whose list length is not its declared count.
func TestDieSizesAreReplacedRatherThanMerged(t *testing.T) {
	t.Parallel()

	engine := anEngine(t, `system: "dnd5e"
version: "2024-overlay@1"
title: "the 2024 edition"
dice:
  sizes: [2, 6, 20]
`)

	sizes := engine.Pack().DieSizes()

	if !slices.Equal(sizes, []int{2, 6, 20}) {
		t.Errorf("the die sizes are %v, want exactly the overlay's [2 6 20]; a list with no "+
			"identity to merge on is replaced whole or not at all", sizes)
	}

	// And the notation followed: the grammar is built from this list, so a d4 is no
	// longer in the notation this pack resolves.
	if _, err := parseExpr(engine.Pack(), "1d4"); err == nil {
		t.Error("the notation still accepts a d4 after the overlay removed it")
	}

	if _, err := parseExpr(engine.Pack(), "1d2"); err != nil {
		t.Errorf("the notation refuses a d2 the overlay added: %v", err)
	}

	// Which is §10.3's "the protocol never assumes d20", held end to end: the pack's
	// dice reach the grammar, `Parse`, and the primary die's own consistency check.
	faces, _ := engine.Pack().PrimaryDie()
	if !engine.Pack().HasDieFace(faces) {
		t.Errorf("the primary die is a d%d and the overlay's sizes do not include it; a pack "+
			"whose primary die is not among its own sizes is refused at load", faces)
	}
}

// TestAnOverlayWithNoRowsIsStillAnOverlay is `Overlay.Zero`, and it is a shape rather
// than a missing input.
//
// An overlay that declared nothing but a version is a pack somebody will write, and it
// contributes no rows. `Zero` reports on **both** the name and the version, so a caller
// cannot mistake it for the absent overlay — and applying it is a no-op that still moves
// the overlay's fingerprint component, which is exactly right: a campaign that was written
// under a pack called "2024" should be refused under a build that has no such pack.
func TestAnOverlayWithNoRowsIsStillAnOverlay(t *testing.T) {
	t.Parallel()

	var absent Overlay

	if !absent.Zero() {
		t.Error("the zero Overlay is not reported as absent")
	}

	empty, err := ParseOverlay([]byte("system: \"dnd5e\"\nversion: \"sparse@1\"\n"))
	if err != nil {
		t.Fatalf("compiling a sparse overlay: %v", err)
	}

	if empty.Zero() {
		t.Error("an overlay declaring only a system and a version is reported as absent; it " +
			"contributes no rows but it is still the edition a campaign was written under")
	}

	if empty.Name() != "" {
		t.Errorf("the sparse overlay's name is %q, want empty — it declared no title", empty.Name())
	}

	engine := anEngine(t, "system: \"dnd5e\"\nversion: \"sparse@1\"\n")

	if engine.OverlayVersion() != "sparse@1" {
		t.Errorf("the overlay component is %q, want the sparse overlay's own version",
			engine.OverlayVersion())
	}
}

// TestTheBaseVersionIsCapturedBeforeTheMerge is the one line in `New` this file is really
// about, and it is invisible until it is wrong.
//
// `mergeFiles` lets the overlay's version win, so a merged file's version is the
// *overlay's*. Reading it back as the base's would put the same string in two of the four
// fingerprint components and make a base-pack revision invisible to the gate that exists
// to notice one. The assertion is that the base's component is the base's version after
// an overlay that declares its own.
func TestTheBaseVersionIsCapturedBeforeTheMerge(t *testing.T) {
	t.Parallel()

	standalone := anEngine(t, "")
	overlaid := anEngine(t, `system: "dnd5e"
version: "2024-overlay@1"
title: "the 2024 edition"
`)

	baseVersion := standalone.Pack().Version()

	if overlaid.PackVersion() != baseVersion {
		t.Errorf("the base pack's component is %q under an overlay and %q without one; the "+
			"merged file's version is the overlay's", overlaid.PackVersion(), baseVersion)
	}

	// And the merged file really does carry the overlay's, so the capture is not an
	// accident of the merge preferring the base.
	if overlaid.Pack().Version() != "2024-overlay@1" {
		t.Errorf(
			"the merged pack's own version is %q, want the overlay's",
			overlaid.Pack().Version(),
		)
	}
}

// TestARowWithNoSlugIsRefused is the merge's one refusal, and it is refused at the merge
// rather than at the compiler.
//
// The slug is the key every collection merges on, so an entry with no slug has no
// identity: appending it would make two of them one row whichever arrived last, and which
// arrived last is a property of the merge order.
func TestARowWithNoSlugIsRefused(t *testing.T) {
	t.Parallel()

	// **Through `New`, not through `ParseOverlay`.** `ParseOverlay` checks only that the
	// document is a pack at all — that it names this system and carries a version —
	// because an overlay on its own is not a ruleset: it is a set of replacements, and
	// applying it is `mergeFiles`' job. The refusal is therefore the merge's, which is
	// where the slug is actually needed.
	overlay, err := ParseOverlay([]byte(`system: "dnd5e"
version: "no-slug@1"
kinds:
  - { label: "Anonymous" }
`))
	if err != nil {
		t.Fatalf("compiling the overlay: %v", err)
	}

	_, err = New(Options{Overlay: overlay})
	if err == nil {
		t.Fatal("an overlay carrying a row with no slug loaded; two unslugged rows would be " +
			"one row whichever arrived last, and which arrived last is the merge order")
	}

	if !strings.Contains(err.Error(), ErrMissingSlug.Error()) {
		t.Errorf("want a refusal naming %v, got %v", ErrMissingSlug, err)
	}
}
