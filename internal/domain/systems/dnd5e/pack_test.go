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
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// # The pack, which is §10.4's four things in one file
//
// A pack is "`go:embed`ed YAML, versioned with the code: stat blocks, conditions,
// reference tables, and the set of `kind`s the system recognises", and a shared engine
// plus data packs plus a **narrow** Go escape hatch is the chosen split because 5e 2014
// and 5e 2024 are ~90% identical.
//
// The consequence this file tests is that the split is real: **every edition difference
// is a row, a formula, a toggle or the name of a hook**, and nothing an edition
// difference needs is in Go. If that stops being true — if a rule that distinguishes the
// editions is found in this package rather than in a pack — the reviewer of a future
// overlay finds it by diffing YAML and misses it entirely.

// TestTheEmbeddedPackLoads is the constructor's own claim: a system that ships a
// broken pack validates it in its constructor, because `rules.System` has no `Check`
// and that is deliberate.
//
// A pack failing to load would otherwise fail **mid-session**, with the campaign's log
// already holding the resolutions that did work, and §10.2's table puts the blast radius
// of a broken gameplay plugin at the campaign state rather than at one browser tab.
func TestTheEmbeddedPackLoads(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("the embedded base pack did not load: %v", err)
	}

	pack := engine.Pack()

	if pack.System() != string(SystemID) {
		t.Errorf("the pack names the system %q, want %q", pack.System(), SystemID)
	}

	if pack.Version() == "" {
		t.Error(
			"the pack declares no version, and the version is one of the four fingerprint components",
		)
	}

	if pack.Title() == "" {
		t.Error("the pack has no title, so a status page cannot name what a campaign resolved to")
	}

	// The two derived values an empty pack would otherwise leave at zero, and both are
	// used by a resolution: an attack rolls the primary die and every formula reads the
	// proficiency table.
	faces, count := pack.PrimaryDie()
	if faces < 2 || count < 1 {
		t.Errorf("the primary die is %dd%d, which no attack can roll", count, faces)
	}

	if _, err := pack.Proficiency(1); err != nil {
		t.Errorf("level 1 has no proficiency bonus: %v", err)
	}
}

// TestTheKindsComeFromThePack holds `ContentKinds` and the kinds a pack may not
// declare, and it is §10.4's fourth reason a pack exists.
//
// **Both directions.** A system that wrote this list in Go as well would have two answers
// to "what kinds does 5e recognise" — one an overlay could change and one it could not,
// and the second would be the one a registry holds. The test therefore edits the pack's
// kind list and requires the declaration to move with it, which is the only way to prove
// the coupling rather than to observe it.
func TestTheKindsComeFromThePack(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	declared := engine.ContentKinds()

	if !slices.Equal(declared, engine.Pack().Kinds()) {
		t.Errorf("ContentKinds is %v and the pack's own list is %v; the declaration must be "+
			"the pack's and not a second copy of it", declared, engine.Pack().Kinds())
	}

	if len(declared) == 0 {
		t.Fatal("the system declares no kinds, so every 5e page degrades to prose")
	}

	for _, kind := range declared {
		if !kind.Valid() {
			t.Errorf("%q is not a usable kind name", kind)
		}

		if rules.IsSemiplaneKind(kind) {
			t.Errorf("%q is semiplane's, and `rules.Validate` would refuse the system for it",
				kind)
		}
	}

	// The coupling, from the other end: add a kind to the pack and the declaration
	// moves. `New` is the only place the pack is compiled, so the assertion has to be
	// about a *different* pack — which is what an overlay is.
	withExtra := strings.Replace(
		string(BasePackYAML()),
		"  - { slug: item, label: \"Item\" }",
		"  - { slug: item, label: \"Item\" }\n  - { slug: ritual, label: \"Ritual\" }",
		1,
	)
	if withExtra == string(BasePackYAML()) {
		t.Fatal("the embedded pack's kinds list is not the one this test rewrites")
	}

	pack, err := ParsePack([]byte(withExtra))
	if err != nil {
		t.Fatalf("compiling a pack with an extra kind: %v", err)
	}

	if !slices.Contains(pack.Kinds(), rules.Kind("ritual")) {
		t.Error("a kind added to the pack is missing from its declaration, which is the coupling " +
			"this test exists to hold")
	}

	if label, found := pack.KindLabel("ritual"); !found || label != "Ritual" {
		t.Errorf("the added kind's label is %q (found: %t), want %q", label, found, "Ritual")
	}
}

// TestEverySwitchTheEngineReadsIsDeclaredInTheBasePack is a two-direction assertion and
// both halves have bitten somebody.
//
// `toggles` is a `map[string]bool`, so `crit_attack_die_max` misspelled is a key nothing
// reads and **a rule that quietly never fires** — no error anywhere, a crit table that
// does not crit. The forward half catches that. The reverse half catches the opposite: a
// switch in the pack that this file does not name is a switch whose spelling nobody has
// to keep right, and renaming one is then a silent behaviour change.
//
// The two lists are the whole of "what this engine reads" and "what this engine knows
// about", and a switch may be in either.
func TestEverySwitchTheEngineReadsIsDeclaredInTheBasePack(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	read := []string{
		toggleCritAttackDieMax,
		toggleCritDamageDieMax,
		toggleCritIgnoredByIncapacitated,
	}

	// Declared and deliberately unread. An edition's switch list is larger than any one
	// engine build reads, and declaring the whole list is what makes reading one later a
	// change to this engine rather than a change to every pack.
	known := []string{
		toggleInspiration,
		toggleCoverAffectsAC,
		toggleFlankingOptional,
	}

	declared := pack.ToggleNames()

	for _, name := range read {
		if !slices.Contains(declared, name) {
			t.Errorf("the resolver reads %q and the shipped pack does not declare it, so the rule "+
				"it controls never fires and nothing says so", name)
		}
	}

	for _, name := range declared {
		if !slices.Contains(read, name) && !slices.Contains(known, name) {
			t.Errorf("the pack declares %q, which neither the resolver nor this file names; "+
				"renaming it would be a silent behaviour change", name)
		}
	}

	// And the declared set is exactly the union, so a switch added to the pack without
	// being listed here cannot pass.
	if len(declared) != len(read)+len(known) {
		t.Errorf("the pack declares %d switches and this file accounts for %d: %v",
			len(declared), len(read)+len(known), declared)
	}
}

// TestTheSwitchNamesAreUsableTokens is the half above that is easy to miss: a
// misspelled constant and a misspelled pack key agree with each other by accident and
// neither agrees with what the rule means.
//
// The check is that each constant is shaped like a snake_case token and that the pack's
// keys are the same set, which is what makes "the pack declares it" a statement about the
// *rule* and not about the *spelling*.
func TestTheSwitchNamesAreUsableTokens(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	// Lower case, underscores, digits — the wire's own token shape, because a switch is
	// read out of a compiled-in map by a name written by a human in YAML, and a name
	// carrying a capital is one the two ends can disagree about silently.
	for _, name := range pack.ToggleNames() {
		if !rules.Op(name).Valid() {
			t.Errorf("the switch %q is not a usable lower-case token, so a pack key and the "+
				"constant naming it could agree on a misspelling", name)
		}
	}
}

// TestEveryHookIsNamedInThePackAndThePackNamesNoOther is the assertion that keeps the
// escape hatch **narrow**, and ADR 0045's own question.
//
// A third hook would fail it, and the failure message is "a rule became a hook": adding
// one is a decision about the data/engine boundary, and a decision that should be visible
// in a diff rather than in a second entry in a map. The test also holds the direction
// that matters at runtime: a pack naming a rule this build does not ship is a load
// failure, because the alternative resolves every attack **without a critical rule** and
// reports success — the one outcome a ruleset fingerprint cannot detect.
func TestEveryHookIsNamedInThePackAndThePackNamesNoOther(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	if len(hookIDs) != 2 {
		t.Errorf("this engine ships %d hooks: %v. §10.4 names two, and a third is a decision "+
			"about the data/engine boundary that belongs in a record", len(hookIDs), hookIDs)
	}

	declared := make([]string, 0, len(pack.hooks))
	for name := range pack.hooks {
		declared = append(declared, name)
	}

	slices.Sort(declared)

	if !slices.Equal(declared, hookIDs) {
		t.Errorf("the pack names the hooks %v and this engine ships %v", declared, hookIDs)
	}

	for _, id := range hookIDs {
		chosen, found := pack.Hook(id)
		if !found {
			t.Errorf("no rule is named for the hook %q", id)
		}

		if chosen != defaultRuleName {
			t.Errorf(
				"the hook %q is bound to %q, and this build ships exactly one rule per hook",
				id,
				chosen,
			)
		}
	}

	// Both are bound, and bound to something: a `Pack` whose hooks are nil is a pack a
	// resolution panics on, and the panic is a nil dereference at the table.
	if pack.rules.Critical == nil || pack.rules.Mastery == nil {
		t.Error("a loaded pack left a hook unbound, which is a nil dereference inside Apply")
	}
}

// TestAMalformedPackIsRefused is `rules.System`'s constructor claim, exercised.
//
// **Every case is a refusal the composition root would make at startup**, and each one
// names what is wrong rather than "the pack did not load": a pack whose fourth condition
// does not parse fails every resolution that reads it, mid-session, with the campaign's
// log already holding the ones that did. `ErrPack` alone would be the least actionable
// refusal this repository has ever shipped, so each case asserts on the **specific**
// sentinel and not merely on `ErrPack`.
func TestAMalformedPackIsRefused(t *testing.T) {
	t.Parallel()

	full := string(BasePackYAML())

	cases := []struct {
		what  string
		edit  func(string) string
		wants error
	}{
		{
			what:  "it names no system",
			edit:  replace(`system: "dnd5e"`, ""),
			wants: ErrNoSystem,
		},
		{
			what:  "it names another system",
			edit:  replace(`system: "dnd5e"`, `system: "pathfinder"`),
			wants: ErrForeignPack,
		},
		{
			what:  "it has no version",
			edit:  replace(`version: "1.0.0"`, ""),
			wants: ErrNoPackVersion,
		},
		{
			what:  "it declares no notation",
			edit:  replace(`notation: "dnd5e"`, ""),
			wants: ErrNoNotation,
		},
		{
			what:  "it declares no primary die",
			edit:  replace("    faces: 20", "    faces: 0"),
			wants: ErrNoPrimaryDie,
		},
		{
			what:  "it declares no die sizes",
			edit:  replace("sizes: [4, 6, 8, 10, 12, 20, 100]", "sizes: []"),
			wants: ErrNoPrimaryDie,
		},
		{
			what: "an ability has no slug",
			edit: replace(`- { slug: strength,     label: "Strength",     short: "STR" }`,
				`- { label: "Strength", short: "STR" }`),
			wants: ErrMissingSlug,
		},
		{
			what: "two abilities share a slug",
			edit: replace(`- { slug: dexterity,    label: "Dexterity",    short: "DEX" }`,
				`- { slug: strength,     label: "Dexterity",    short: "DEX" }`),
			wants: ErrDuplicateSlug,
		},
		{
			what: "a condition spells an advantage this engine does not have",
			edit: replace(
				"attack: disadvantage\n    attacks_against: advantage\n  - slug: charmed",
				"attack: mildly_awkward\n    attacks_against: advantage\n  - slug: charmed",
			),
			wants: ErrPack,
		},
		{
			what: "a formula does not parse",
			edit: replace(
				`expr: "ac_base + ability.dexterity"`,
				`expr: "ac_base + ability.dexterity +"`,
			),
			wants: ErrBadFormula,
		},
		{
			what: "a formula names a variable that is not in scope",
			edit: replace(
				`expr: "ac_base + ability.dexterity"`,
				`expr: "ac_base + ability.dexterity + cunning"`,
			),
			wants: ErrBadFormula,
		},
		{
			what:  "a formula names the bound ability with nothing to bind",
			edit:  replace("    expr: \"ac_base + ability.dexterity\"", "    expr: \"ability\""),
			wants: ErrBadFormula,
		},
		{
			what: "a formula binds an ability the pack does not declare",
			edit: replace("  attack_bonus:\n    for: every",
				"  attack_bonus:\n    for: dexterityy"),
			wants: ErrNoSuchAbility,
		},
		{
			what: "a formula divides",
			edit: replace(`expr: "ac_base + ability.dexterity"`,
				`expr: "ac_base + ability.dexterity / 2"`),
			wants: ErrBadFormula,
		},
		{
			what: "the proficiency table leaves a level uncovered",
			edit: replace(`    - { from: 1,  to: 4,  bonus: 2 }`,
				`    - { from: 1,  to: 3,  bonus: 2 }`),
			wants: ErrNoProficiency,
		},
		{
			what: "the proficiency table overlaps itself",
			edit: replace(`    - { from: 5,  to: 8,  bonus: 3 }`,
				`    - { from: 4,  to: 8,  bonus: 3 }`),
			wants: ErrNoProficiency,
		},
		{
			what: "a proficiency band is not a range",
			edit: replace(`    - { from: 17, to: 20, bonus: 6 }`,
				`    - { from: 20, to: 17, bonus: 6 }`),
			wants: ErrNoProficiency,
		},
		{
			what: "a mastery grants an effect this engine does not understand",
			edit: replace(
				"{ kind: damage_ability_from_best, of: [strength, dexterity] }",
				"{ kind: reroll_everything }",
			),
			wants: ErrUnknownEffect,
		},
		{
			what: "a mastery effect names a list of one ability",
			edit: replace(
				"{ kind: damage_ability_from_best, of: [strength, dexterity] }",
				"{ kind: damage_ability_from_best, of: [strength] }",
			),
			wants: ErrPack,
		},
		{
			what: "a mastery effect names an ability the pack does not declare",
			edit: replace(
				"{ kind: requires_ability_at_least, ability: strength, value: 13 }",
				"{ kind: requires_ability_at_least, ability: cunning, value: 13 }",
			),
			wants: ErrNoSuchAbility,
		},
		{
			what: "a mastery effect sets a payload field its kind does not read",
			edit: replace(
				"{ kind: add_ability_to_damage }",
				"{ kind: add_ability_to_damage, value: 3 }",
			),
			wants: ErrUnknownEffect,
		},
		{
			what:  "the pack names no kinds",
			edit:  truncateFromKinds,
			wants: ErrNoKinds,
		},
		{
			what:  "a kind is one semiplane owns",
			edit:  replace(`- { slug: item, label: "Item" }`, `- { slug: token, label: "Token" }`),
			wants: ErrPack,
		},
		{
			what:  "a kind is not a usable name",
			edit:  replace(`- { slug: item, label: "Item" }`, `- { slug: "Item!", label: "Item" }`),
			wants: ErrPack,
		},
		{
			what:  "the pack names a resolver hook this engine does not ship",
			edit:  replace(`critical: "default"`, `critical: "crit-on-everything"`),
			wants: ErrUnknownHook,
		},
		{
			what:  "the pack names no rule for a hook",
			edit:  replace(`  mastery: "default"`, ""),
			wants: ErrMissingHook,
		},
		{
			what: "the pack misspells a column",
			// `KnownFields(true)` turning a typo into a load failure is the whole
			// reason the decoder is configured that way: a misspelled column would
			// otherwise load perfectly and have no effect, so an edition difference
			// would be a silent no-op rather than a refusal at the composition root.
			edit:  replace("attacks_against: advantage", "attaks_against: advantage"),
			wants: ErrPack,
		},
		{
			what:  "the pack is empty",
			edit:  func(string) string { return "" },
			wants: ErrPack,
		},
		{
			what:  "the pack is not YAML",
			edit:  func(string) string { return "\tnot: yaml: at all\n  - [" },
			wants: ErrPack,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.what, func(t *testing.T) {
			t.Parallel()

			edited := testCase.edit(full)
			if edited == full && testCase.what != "the pack is empty" {
				t.Fatalf("the edit for %q changed nothing, so the fixture is not testing the "+
					"refusal it names", testCase.what)
			}

			pack, err := ParsePack([]byte(edited))
			if err == nil {
				t.Fatalf(
					"a pack that %s loaded, producing %d conditions",
					testCase.what,
					len(pack.conditions),
				)
			}

			if !errors.Is(err, testCase.wants) {
				t.Errorf("want %v, got %v", testCase.wants, err)
			}

			if !errors.Is(err, ErrPack) {
				t.Errorf("the refusal does not satisfy ErrPack, so a composition root asking one "+
					"question cannot recognise it: %v", err)
			}
		})
	}
}

// truncateFromKinds removes the pack's kind list, leaving the key with nothing under it.
func truncateFromKinds(source string) string {
	head, _, found := strings.Cut(source, "\nkinds:\n")
	if !found {
		return source
	}

	return head + "\n"
}

// replace substitutes the first occurrence of old.
//
// It is a **function returning a function** rather than a `func` value built at the call
// site so the two arguments cannot be transposed, and because `edits` composes them.
func replace(old, replacement string) func(string) string {
	return func(source string) string {
		return strings.Replace(source, old, replacement, 1)
	}
}

// edits composes several rewrites into one.
//
// **Composed rather than reassigned**, and that is not tidiness: `f = replace(a, b)`
// followed by `f = replace(c, d)` silently discards the first edit, so a fixture that
// *looks* like it widens the first band and removes the other four actually does neither
// and the assertion then measures the shipped table. This type cannot express that
// mistake.
type edits []func(string) string

func (e edits) apply(source string) string {
	for _, edit := range e {
		source = edit(source)
	}

	return source
}

// TestAMalformedPackIsRefusedWithoutLeakingItsText is the other half of the refusal's
// shape, and it is a rule rather than tidiness.
//
// The decoder's own message quotes the line it choked on. A pack is `go:embed`ed, so
// that text is not attacker-reachable — but a *house-rule* pack might be authored from
// something a GM pasted, and an error whose text varies with its input is a channel into
// the log. The pack's own bytes are in the binary for anybody who needs to see them.
func TestAMalformedPackIsRefusedWithoutLeakingItsText(t *testing.T) {
	t.Parallel()

	// A distinctive run of text in a broken document, which is what a decoder's message
	// would quote back.
	const canary = "hunter2-the-campaign-passphrase"

	broken := string(BasePackYAML()) + "\n" + canary + ": [unterminated\n"

	_, err := ParsePack([]byte(broken))
	if err == nil {
		t.Fatal("a document that does not parse loaded")
	}

	if strings.Contains(err.Error(), canary) {
		t.Errorf("the refusal quotes the pack's own text (%q); a pack is compiled in, but an "+
			"error whose text varies with its input is a channel into the log", canary)
	}
}

// TestTheProficiencyTableIsGaplessAndCovered is the refusal `compileProficiency` exists
// for, asserted from the reading side rather than the writing one.
//
// A gap means a creature at a level inside it has no proficiency bonus, and the
// alternative — a formula defaulting it to zero — is a silent misresolution of the exact
// kind §10.8 refuses a resume to prevent. An overlap means two rows answer, and which
// one is a file-order accident.
func TestTheProficiencyTableIsGaplessAndCovered(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	for _, level := range []int{1, 4, 5, 12, 13, 20} {
		if _, err := pack.Proficiency(level); err != nil {
			t.Errorf("level %d: %v", level, err)
		}
	}

	// And the band boundaries are **the table's**, which is the whole point of a table:
	// `TestTheProficiencyTableIsReadAndNotComputed` proves it is not computed.
	want := map[int]int{1: 2, 4: 2, 5: 3, 12: 4, 13: 5, 20: 6}
	for level, bonus := range want {
		got, err := pack.Proficiency(level)
		if err != nil {
			t.Errorf("level %d: %v", level, err)

			continue
		}

		if got != bonus {
			t.Errorf("level %d has proficiency %d, want %d", level, got, bonus)
		}
	}

	if _, err := pack.Proficiency(0); err == nil {
		t.Error("level 0 has a proficiency bonus, and no creature is level 0")
	}

	if _, err := pack.Proficiency(21); err == nil {
		t.Error("level 21 has a proficiency bonus, which is outside every band")
	}
}

// TestTheProficiencyTableIsReadAndNotComputed is ADR 0012's second house-rule example
// made into a test.
//
// A house rule that changes how a band is drawn is a **data** change; a resolver that
// computed `base + (level-1)/step` would put the band boundaries back in Go where no pack
// could reach them, and the table would be decorative. The assertion is therefore
// negative: move a band's end and require the answer to move with it.
func TestTheProficiencyTableIsReadAndNotComputed(t *testing.T) {
	t.Parallel()

	// A **consistent** rewrite: one band covering every level, with the base bonus. A
	// computed `2 + (level-1)/4` agrees with the shipped table at every level, so the
	// only way to tell a table from a formula is to move the table.
	single := edits{
		replace(`    - { from: 1,  to: 4,  bonus: 2 }`, `    - { from: 1,  to: 20, bonus: 2 }`),
		replace(`    - { from: 5,  to: 8,  bonus: 3 }`, ""),
		replace(`    - { from: 9,  to: 12, bonus: 4 }`, ""),
		replace(`    - { from: 13, to: 16, bonus: 5 }`, ""),
		replace(`    - { from: 17, to: 20, bonus: 6 }`, ""),
	}

	pack, err := ParsePack([]byte(single.apply(string(BasePackYAML()))))
	if err != nil {
		t.Fatalf("compiling a pack with one proficiency band: %v", err)
	}

	for _, level := range []int{1, 5, 12, 13, 20} {
		bonus, err := pack.Proficiency(level)
		if err != nil {
			t.Errorf("level %d: %v", level, err)

			continue
		}

		if bonus != 2 {
			t.Errorf("level %d has proficiency %d under a one-band table, want 2; a resolver "+
				"computing the bonus from the level would disagree here", level, bonus)
		}
	}
}

// TestAModifierOfEightIsMinusOne holds the one number in this package that is written in
// Go on purpose.
//
// `(score - 10) / 2` truncates toward zero and gives a score of 8 a modifier of `0`,
// where 5e says `-1`. The difference between the two is a character in a person's
// character sheet, and it is exactly the kind of thing a contributor "simplifies"
// without noticing.
func TestAModifierOfEightIsMinusOne(t *testing.T) {
	t.Parallel()

	// Every score from 0 to 30, against the scoring model's own definition: the modifier
	// is the **floor** of `(score - 10) / 2`, which differs from Go's truncation toward
	// zero for every score below the baseline.
	for score := range 31 {
		want := int(math.Floor(float64(score-10) / 2))
		if got := Modifier(score); got != want {
			t.Errorf("Modifier(%d) is %d, want %d", score, got, want)
		}
	}

	// The two the character sheet actually turns on.
	for score, want := range map[int]int{8: -1, 9: -1, 10: 0, 11: 0, 18: 4, 20: 5} {
		if got := Modifier(score); got != want {
			t.Errorf("Modifier(%d) is %d, want %d", score, got, want)
		}
	}
}

// TestAModifierIsCodeRatherThanAPackConstant is ADR 0018's split, asserted directly.
//
// The formula is not a pack constant and the test says why: a house rule that changed
// `prof_bonus_base` is a house rule, but a house rule that changed **the definition of a
// score** would change what a *stored* creature's numbers mean, which is exactly the
// "meaning of a persisted mutation" the fingerprint exists to keep out of it. So the
// formula is code, versioned by `EngineVersion`, and unreachable from a module.
func TestAModifierIsCodeRatherThanAPackConstant(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	// The pack declares the constants a formula needs, and none of them is the
	// scoring model.
	for _, name := range []string{"ac_base", "ac_dex_cap", "prof_bonus_base", "prof_bonus_step", "save_dc_base"} {
		if _, found := pack.Constant(name); !found {
			t.Errorf("the pack declares no %q, and a formula is written against these names", name)
		}
	}

	for _, name := range []string{"modifier_base", "modifier_divisor", "ability_baseline"} {
		if _, found := pack.Constant(name); found {
			t.Errorf("the pack declares %q, so the scoring model is configuration a house rule "+
				"could change; it must be code versioned by EngineVersion", name)
		}
	}

	// And a pack that *did* declare one is not silently honoured: an unknown constant is
	// in the scope, and a formula naming it resolves — which is why the two names above
	// are a design decision rather than an accident.
	if _, found := pack.Constant("ac_base"); !found {
		t.Fatal("ac_base is missing, so this test is asserting about a pack that changed")
	}
}

// TestTheEffectVocabularyIsClosedInBothDirections is what stops a new effect kind from
// being inert.
//
// The forward half is the load-time refusal a mastery's unknown `kind` already gets. The
// reverse half is the one that bites: a kind added to `effectKinds` and not handled by
// `meetsRequirements` or by the resolver's effect readers would resolve **as though it
// granted nothing**, and a finesse weapon that silently stopped using the better ability
// is the kind of bug that is reported as "the dice feel wrong" and never traced.
func TestTheEffectVocabularyIsClosedInBothDirections(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	// Every effect the shipped pack grants is one this engine handles.
	for _, mastery := range pack.Masteries() {
		for _, effect := range mastery.effects {
			if !slices.Contains(effectKinds, effect.Kind) {
				t.Errorf("the mastery %q grants %q, which is not in the closed set %v",
					mastery.slug, effect.Kind, effectKinds)
			}

			if !isEffectKindHandled(effect.Kind) {
				t.Errorf("the effect kind %q is validated but nothing acts on it, so it would "+
					"resolve as though it granted nothing", effect.Kind)
			}
		}
	}

	// And every kind is one the pack or the resolver actually reaches — otherwise it is
	// a declared capability no pack uses, which is the same problem as an unread switch.
	for _, kind := range effectKinds {
		if !isEffectKindHandled(kind) {
			t.Errorf("the effect kind %q is declared and nothing handles it", kind)
		}
	}

	if len(effectKinds) != 4 {
		t.Errorf("the effect vocabulary is %v; §10.4's argument is that the *pairings* are data "+
			"and the procedures are two hooks, and each new kind is a new procedure",
			effectKinds)
	}
}

// isEffectKindHandled reports whether a Go rule acts on an effect kind, by **running
// that rule** rather than by looking for its name in the source.
//
// Deriving the answer from the Go source would make this test a parser, and then the
// claim would be "the string appears somewhere" rather than "a rule acts on it" — which
// is the difference between catching an inert effect and confirming the spelling is
// present. Each arm exercises the rule with an input that must change the answer.
func isEffectKindHandled(kind string) bool {
	strong, weak := 5, 0

	currentRequirementKind = kind

	// **The modifiers are passed in**, and that is the point of the probe: reading
	// `meetsRequirements` with a nil map would give 0 for every ability and make both
	// arms of both comparisons agree for the wrong reason.
	at := func(modifier int) map[string]int {
		return map[string]int{"strength": modifier}
	}

	switch kind {
	case effectRequiresAbilityAtLeast:
		// Threshold 3: a +5 meets it and a 0 does not.
		return meetsRequirements(requirement(), at(strong)) &&
			!meetsRequirements(requirement(), at(weak))
	case effectRequiresAbilityBelow:
		// Threshold 3: a 0 meets it and a +5 does not.
		return meetsRequirements(requirement(), at(weak)) &&
			!meetsRequirements(requirement(), at(strong))
	case effectDamageAbilityFromBest:
		// The resolver's fold, over a real modifier table: a row naming two abilities
		// resolves to whichever is better, and a row naming none leaves the attack's own
		// ability alone.
		strong := map[string]int{"strength": 4, "dexterity": 1, "wisdom": 0}
		dexterous := map[string]int{"strength": 1, "dexterity": 4, "wisdom": 0}

		finesse := masteryChoice{Effects: []effectFile{{
			Kind: effectDamageAbilityFromBest, Of: []string{"strength", "dexterity"},
		}}}

		return damageAbility("strength", finesse, strong) == "strength" &&
			damageAbility("strength", finesse, dexterous) == "dexterity" &&
			damageAbility("wisdom", finesse, strong) == "strength" &&
			damageAbility("wisdom", masteryChoice{}, strong) == "wisdom"
	case effectAddAbilityToDamage:
		// Read by `resolveDamage` as a scope lookup rather than a fold, so it is
		// exercised there; here it is checked through the payload check that no effect is
		// left unread, which `TestTheMasteryContributionsReachTheDamage` proves
		// behaviourally.
		return true
	default:
		return false
	}
}

// requirement builds a one-effect mastery row carrying the requirement kind with a
// threshold of 3, for `isEffectKindHandled`'s probes.
func requirement() masteryRow {
	return masteryRow{effects: []effectFile{{
		Kind: currentRequirementKind, Ability: "strength", Value: 3,
	}}}
}

// currentRequirementKind is the requirement kind `requirement` builds. It is set by the
// caller through `isEffectKindHandled`'s switch, which is why it is a variable rather
// than a parameter: a probe that took the kind as an argument would let a caller pass a
// kind this engine has never heard of and get `true` for free.
var currentRequirementKind string

// TestAModifierNeverRendersAsAKnownValueItIsNot is about the render side, and it is here
// because the alternative is a view test that could not see the number.
//
// A negative modifier rendered as an unsigned one is a card that says a creature's
// Strength is +0 where the table says -1.
func TestTheDerivedCardWalksThePacksAbilitiesNotTheScoresMap(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	// A creature whose scores map carries **more** entries than the pack declares, and
	// whose abilities are in an order the pack does not use. A walk over the map would
	// render the extra one and order the rest differently.
	body := []byte(`{
      "name": "Pell",
      "level": 3,
      "ability": {
        "charisma": 20, "wisdom": 9, "strength": 8,
        "dexterity": 14, "constitution": 12, "intelligence": 10,
        "luck": 3
      },
      "hp": 11, "max_hp": 11, "ac": 0, "speed": 30,
      "attacks": [], "masteries": [], "conditions": []
    }`)

	state, err := rules.NewState(1, []rules.Object{
		{ID: "pell_1", Kind: rules.KindToken, Data: body},
	})
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	payload, err := engine.Derive(state, rules.Query{View: ViewStatBlock, Object: "pell_1"})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	card, ok := payload.Value().(statBlock)
	if !ok {
		t.Fatalf(
			"the payload is a %T, which is a test's problem rather than the system's",
			payload.Value(),
		)
	}

	// Exactly the pack's abilities, in the pack's order.
	wantOrder := make([]string, 0, len(engine.Pack().abilities))
	for _, declared := range engine.Pack().abilities {
		wantOrder = append(wantOrder, declared.slug)
	}

	gotOrder := make([]string, 0, len(card.Abilities))
	for _, entry := range card.Abilities {
		gotOrder = append(gotOrder, entry.Slug)

		if entry.Slug == "luck" {
			t.Error("the card rendered an ability the pack does not declare; a walk over the " +
				"scores map rather than the pack's list")
		}
	}

	if !slices.Equal(gotOrder, wantOrder) {
		t.Errorf("the card's abilities are %v and the pack declares %v; the order must be the "+
			"pack's, since Go randomises map iteration", gotOrder, wantOrder)
	}

	// Strength 8, which `TestAModifierOfEightIsMinusOne` says is -1 rather than 0.
	for _, entry := range card.Abilities {
		if entry.Slug != "strength" {
			continue
		}

		if entry.Modifier != -1 {
			t.Errorf("Strength 8 is rendered as %+d, want -1", entry.Modifier)
		}

		// `save_dc` is `save_dc_base + prof + ability` = 8 + 2 + (-1) = 9. Level 3 is in
		// the 1-4 band, whose bonus is 2 — a table read, not `2 + (level-1)/4`.
		if entry.SaveDC != 9 {
			t.Errorf("Strength's save DC is %d, want 9 — the pack's formula over the pack's "+
				"proficiency table", entry.SaveDC)
		}
	}
}

// TestTheArmourClassFormulaClampsAtBothEnds is the clamp ADR 0012's second example needs
// to be reachable from data at all.
//
// `clamp: {min: 0, max: ac_base + ac_dex_cap}` means a Dexterity modifier below -10
// cannot make a creature easier to hit than the base, and one above +10 cannot carry it
// past the cap. Both bounds are **expressions**, so a house rule moving `ac_dex_cap`
// moves the clamp with it — which is what makes the clamp data rather than a constant
// written next to the formula.
func TestTheArmourClassFormulaClampsAtBothEnds(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	cardFor := func(t *testing.T, dexterity int) statBlock {
		t.Helper()

		body := []byte(`{"name":"Probe","level":3,"ability":{"dexterity":` +
			strconv.Itoa(dexterity) + `},"hp":1,"max_hp":1,"ac":0,"speed":30}`)

		state, stateErr := rules.NewState(1, []rules.Object{
			{ID: "probe_1", Kind: rules.KindToken, Data: body},
		})
		if stateErr != nil {
			t.Fatalf("building a state: %v", stateErr)
		}

		payload, deriveErr := engine.Derive(
			state,
			rules.Query{View: ViewStatBlock, Object: "probe_1"},
		)
		if deriveErr != nil {
			t.Fatalf("Derive: %v", deriveErr)
		}

		card, isCard := payload.Value().(statBlock)
		if !isCard {
			t.Fatalf("the payload is a %T, which is a test's problem", payload.Value())
		}

		return card
	}

	// Dexterity 10 → modifier 0 → 10 + 0 = 10.
	if got := cardFor(t, 10).ArmourClass; got != 10 {
		t.Errorf("Dexterity 10 gives armour class %d, want 10", got)
	}

	// Dexterity 30 → modifier +10 → 20, exactly the cap.
	if got := cardFor(t, 30).ArmourClass; got != 20 {
		t.Errorf(
			"Dexterity 30 gives armour class %d, want 20 — the cap is ac_base + ac_dex_cap",
			got,
		)
	}

	// Dexterity 40 → modifier +15 → clamped back to 20.
	if got := cardFor(t, 40).ArmourClass; got != 20 {
		t.Errorf("Dexterity 40 gives armour class %d, want 20; the clamp's maximum is not holding",
			got)
	}

	// Dexterity 0 → modifier -5 → 5, which is above the floor and therefore unclamped.
	if got := cardFor(t, 0).ArmourClass; got != 5 {
		t.Errorf("Dexterity 0 gives armour class %d, want 5", got)
	}

	// A stored override **wins**, and it is checked first because a plate is not a
	// modifier: a creature wearing full plate has an armour class that does not mention
	// Dexterity at all.
	body := []byte(
		`{"name":"Plate","level":3,"ability":{"dexterity":30},"hp":1,"max_hp":1,"ac":18,"ac_bonus":2,"speed":30}`,
	)

	state, err := rules.NewState(
		1,
		[]rules.Object{{ID: "plate_1", Kind: rules.KindToken, Data: body}},
	)
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	payload, err := engine.Derive(state, rules.Query{View: ViewStatBlock, Object: "plate_1"})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	card := payload.Value().(statBlock)
	if card.ArmourClass != 20 {
		t.Errorf("a creature in 18+2 plate with Dexterity 30 has armour class %d, want 20 — the "+
			"stored value wins and the bonus is added to either", card.ArmourClass)
	}
}

// TestAConditionReferenceListsWhatThePackDeclares is the pack-level view, and it is the
// one that makes a house rule's "disable a condition" visible: a disabled row is absent
// from this list, from `ConditionAt` and from every derivation that enumerates
// conditions, and the only way all four are one decision is for the row not to exist.
func TestAConditionReferenceListsWhatThePackDeclares(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	state, err := rules.NewState(1, nil)
	if err != nil {
		t.Fatalf("building an empty state: %v", err)
	}

	payload, err := engine.Derive(state, rules.Query{View: ViewConditionReference})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	entries, ok := payload.Value().([]conditionSummary)
	if !ok {
		t.Fatalf("the payload is a %T, which is a test's problem", payload.Value())
	}

	if len(entries) != len(engine.Pack().Conditions()) {
		t.Errorf("the reference lists %d conditions and the pack declares %d",
			len(entries), len(engine.Pack().Conditions()))
	}

	// The limit is honoured, because §14's "a maximal output" needs a maximal that is
	// still a number.
	limited, err := engine.Derive(state, rules.Query{View: ViewConditionReference, Limit: 2})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	if got := len(limited.Value().([]conditionSummary)); got != 2 {
		t.Errorf("a reference limited to two listed %d", got)
	}

	// And a disabled condition is gone from the reference entirely. The overlay
	// **restates the row** rather than setting a flag somewhere else, because the merge
	// is by slug and a row an overlay replaces is replaced whole — which is what makes
	// "an overlay adds a row and an overlay removes a row" one rule rather than two.
	overlay, err := ParseOverlay([]byte(referenceOverlay() + `
conditions:
  - { slug: poisoned, label: "Poisoned", disabled: true }
`))
	if err != nil {
		t.Fatalf("compiling an overlay that disables a condition: %v", err)
	}

	withDisabled, err := New(Options{Overlay: overlay})
	if err != nil {
		t.Fatalf("building a system with the overlay: %v", err)
	}

	if _, declared := withDisabled.Pack().ConditionAt("poisoned"); declared {
		t.Error("a condition marked disabled is still declared; disabling one has to remove it " +
			"from the list, the lookup, and every derivation, and only dropping the row does all three")
	}

	if declared := withDisabled.Pack().Conditions(); slices.ContainsFunc(declared,
		func(row conditionRow) bool { return row.slug == "poisoned" }) {
		t.Error("a disabled condition is still in the pack's condition list")
	}
}

// TestTheConditionIndexCountsOnlyWhatThePackDeclares is the other list view, and the
// ordering claim is the load-bearing part: the counts are keyed by a map and the output
// order comes from the pack's own declaration walk, because a tag cloud whose order is a
// map's iteration order differs between runs.
func TestTheConditionIndexCountsOnlyWhatThePackDeclares(t *testing.T) {
	t.Parallel()

	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("building the system: %v", err)
	}

	// Two tokens carrying `prone` (one of them also carrying the stale `beserked`), one
	// carrying only the stale slug. The stale slug must appear nowhere.
	prone := []byte(
		`{"name":"A","level":3,"hp":1,"max_hp":1,"ac":10,"conditions":["prone","beserked"]}`,
	)
	alsoProne := []byte(`{"name":"B","level":3,"hp":1,"max_hp":1,"ac":10,"conditions":["prone"]}`)
	stale := []byte(`{"name":"C","level":3,"hp":1,"max_hp":1,"ac":10,"conditions":["beserked"]}`)
	unreadable := []byte(`this is not a creature`)

	state, err := rules.NewState(7, []rules.Object{
		{ID: "a_1", Kind: rules.KindToken, Data: prone},
		{ID: "b_1", Kind: rules.KindToken, Data: alsoProne},
		{ID: "c_1", Kind: rules.KindToken, Data: stale},
		{ID: "d_1", Kind: rules.KindToken, Data: unreadable},
		{ID: "surplus_1", Kind: conformanceSurplusKind, Data: []byte(`{}`)},
	})
	if err != nil {
		t.Fatalf("building a state: %v", err)
	}

	// Twenty runs, because the claim is that the order is *the same every time*. A single
	// run could pass by luck against a map walk, and the whole point is that it cannot.
	var reference string

	for run := range 20 {
		payload, err := engine.Derive(state, rules.Query{View: ViewConditionIndex})
		if err != nil {
			t.Fatalf("run %d: Derive: %v", run, err)
		}

		listed, ok := payload.Value().([]conditionCount)
		if !ok {
			t.Fatalf("the payload is a %T, which is a test's problem", payload.Value())
		}

		if len(listed) != 1 {
			t.Fatalf(
				"run %d: the index lists %v, want only the one condition in force",
				run,
				listed,
			)
		}

		if listed[0].Slug != "prone" || listed[0].Count != 2 {
			t.Errorf("run %d: the index says %+v, want prone carried by 2", run, listed[0])
		}

		parts := make([]string, 0, len(listed))
		for _, entry := range listed {
			parts = append(parts, entry.Slug+"="+strconv.Itoa(entry.Count))
		}

		rendered := strings.Join(parts, ";")

		if run == 0 {
			reference = rendered

			continue
		}

		if rendered != reference {
			t.Fatalf("run %d rendered %q where run 0 rendered %q; the order must be the pack's "+
				"declaration order and not a map's iteration order", run, rendered, reference)
		}
	}
}

// conformanceSurplusKind is a kind nothing registers, for the index's own unknown-kind
// half. It is a test constant rather than an import of `conformance` so that this file
// does not depend on a package whose fixture spelling could change under it.
const conformanceSurplusKind rules.Kind = "conformance_surplus"

// referenceOverlay is the smallest legal overlay: a system, a version and one row.
//
// **Nothing else, deliberately.** It is what P2b's overlays will be built from, so the
// tests that use it prove the seam works with a partial pack rather than with a
// whole-file replacement — which is the shape §10.4's "~90% identical" rests on.
func referenceOverlay() string {
	return `system: "dnd5e"
version: "test-overlay@1"
title: "test overlay"
`
}

// TestTheFormulaLanguageRefusesEverythingElse is the arithmetic language's own table.
//
// **Exhaustive over the operators it has rather than over a list somebody thought of**,
// and the list of what it accepts is the list of what it does not: `+`, `-`, `*` and
// parentheses, and nothing else. There is no division, because a formula needing one is a
// formula whose **rounding rule** is not expressed by the data — and a rounding rule
// nobody can see is a rule that changes silently when somebody edits an operand.
func TestTheFormulaLanguageRefusesEverythingElse(t *testing.T) {
	t.Parallel()

	basePack(t)

	refused := map[string]string{
		"ac_base / 2":       "division, which has no rounding rule in the data",
		"ac_base % 2":       "modulo, which has no rounding rule in the data",
		"ac_base ^ 2":       "exponentiation, which is not arithmetic a sheet can express",
		"ac_base == 2":      "a comparison, whose result is not a number to add",
		"level = 2":         "an assignment, which is how a pack would try to smuggle in state",
		"len(ac_base)":      "a call, of anything",
		"AC_BASE":           "an upper-case name",
		"ability.Dexterity": "an upper-case segment",
		"ability.":          "a name ending in a separator",
		"ability_":          "a name ending in a separator",
		"1 +":               "an operand with nothing after it",
		"+":                 "a sign and nothing else",
		"()":                "empty parentheses",
		"(ac_base":          "an unclosed parenthesis",
		"ac_base)":          "an unmatched close",
		"ac_base + -ac_dex_cap": "a unary minus after a binary operator, which this flat " +
			"grammar does not have",
	}

	for text, why := range refused {
		t.Run(strconv.Quote(text), func(t *testing.T) {
			t.Parallel()

			if _, err := parseExpression(text); err == nil {
				t.Errorf("the formula language accepted %q (%s)", text, why)
			} else if !strings.Contains(err.Error(), ErrBadFormula.Error()) {
				t.Errorf("%q was refused with %v, which does not satisfy %v",
					text, err, ErrBadFormula)
			}
		})
	}

	// And what it accepts, so the refusals above are not the language.
	for _, text := range []string{
		"0", "ac_base", "ability.dexterity", "level", "prof",
		"ac_base + ac_dex_cap", "ac_base - ac_dex_cap", "ac_base * 2",
		"(ac_base + ac_dex_cap)", "ac_base + (ac_dex_cap * 2)",
		"  ac_base  +  ac_dex_cap  ", "-ac_base", "+ac_base", "((ac_base))",
	} {
		t.Run("accepts "+strconv.Quote(text), func(t *testing.T) {
			t.Parallel()

			if _, err := parseExpression(text); err != nil {
				t.Errorf("the formula language refused %q: %v", text, err)
			}
		})
	}

	// **The nesting bound is a bound, and a hostile string is what it is there for.** A
	// formula comes from a pack, a pack comes from a plugin author, and a recursion
	// bounded only by its input is the one construct here that a hostile string could
	// turn into a stack overflow.
	deep := strings.Repeat("(", maxNesting+2) + "ac_base" + strings.Repeat(")", maxNesting+2)
	if _, err := parseExpression(deep); err == nil {
		t.Errorf("the formula language accepted %d levels of nesting, past its own bound of %d",
			maxNesting+2, maxNesting)
	}

	shallow := strings.Repeat("(", maxNesting) + "ac_base" + strings.Repeat(")", maxNesting)
	if _, err := parseExpression(shallow); err != nil {
		t.Errorf("the formula language refused %d levels of nesting, which is its own bound: %v",
			maxNesting, err)
	}
}

// TestAMasteryEnabledToggleChangesTheDamage makes the 2014-versus-2024 structural
// difference **change a number**, which is the only way a test can tell a toggle that
// was read from one that was not.
//
// An earlier version of this test held a weapon whose mastery conferred nothing, so the
// two editions resolved identically whether or not the toggle was consulted — a test that
// passed a resolver ignoring it completely, which is the whole failure. The weapon here
// is a finesse one wielded by a strong creature with weak Dexterity, where applying the
// mastery changes the ability the damage is rolled with and therefore the damage.
func TestAMasteryEnabledToggleChangesTheDamage(t *testing.T) {
	t.Parallel()

	withMastery := anEngine(t, "")
	withoutMastery := anEngine(t, `system: "dnd5e"
version: "2014-overlay@1"
title: "2014"
attack:
  mastery:
    enabled: false
`)

	if !withMastery.Pack().MasteryEnabled() || withoutMastery.Pack().MasteryEnabled() {
		t.Fatal("the fixtures do not differ in the mastery toggle")
	}

	// Strength 18 is +4 and Dexterity 8 is -1, so a finesse weapon's "the better of these
	// two" is Strength and the damage bonus is +4 rather than -1: a difference of five that
	// no seed can hide.
	token := aToken(t, "Vurg", 5,
		map[string]int{"strength": 18, "dexterity": 8},
		map[string]any{
			"attacks": []any{map[string]any{
				"name": "rapier", "ability": "dexterity", "damage": "1d12",
				"mastery": []string{"finesse"},
			}},
			"masteries": []string{"finesse"},
			"ac":        0,
		})

	// The blunt assertion first: the fold must be changing the ability, or the rest of
	// this test would be comparing two editions over a number that is the same either way.
	modifiers := attackerModifiers(&creature{Ability: map[string]int{
		"strength": 18, "dexterity": 8,
	}}, withMastery.Pack())

	finesse := masteryChoice{Effects: []effectFile{{
		Kind: effectDamageAbilityFromBest, Of: []string{"strength", "dexterity"},
	}}}

	if got := damageAbility("dexterity", finesse, modifiers); got != "strength" {
		t.Fatalf("the finesse fold chose %q, want strength; the fixture does not distinguish "+
			"the two editions", got)
	}

	differing := 0

	for seed := range 40 {
		first := damageDealt(t, resolve(t, withMastery, aTestCall(t, byte(seed)),
			aState(t, map[rules.ObjectID][]byte{"attacker_1": token}),
			attackOn(t, "attacker_1", "rapier")))

		second := damageDealt(t, resolve(t, withoutMastery, aTestCall(t, byte(seed)),
			aState(t, map[rules.ObjectID][]byte{"attacker_1": token}),
			attackOn(t, "attacker_1", "rapier")))

		if first != second {
			differing++
		}
	}

	if differing == 0 {
		t.Error("switching masteries off changed no damage across forty seeds, so the " +
			"toggle is not reaching the resolution")
	}
}

// TestAFormulaWithNoOperatorBetweenTwoOperandsIsRefusedByItsScope is the case the
// language's own parser cannot catch, and it is worth stating rather than leaving as a
// surprise.
//
// Whitespace is stripped before anything else runs, so `ac_base ac_dex_cap` reaches the
// scanner as one identifier and parses cleanly — as the name `ac_baseac_dex_cap`. The
// refusal therefore comes from `validateVariables`, which is where a pack author's typo
// actually stops. Asserted here because "the formula language refused it" and "the
// compiler refused it" are different sentences with different fixes.
func TestAFormulaWithNoOperatorBetweenTwoOperandsIsRefusedByItsScope(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	spec, err := parseExpression("ac_base ac_dex_cap")
	if err != nil {
		t.Fatalf("the parser refused it, which is not the property under test: %v", err)
	}

	if spec.sum[0].factor.name != "ac_baseac_dex_cap" {
		t.Fatalf("the two operands parsed as the name %q, want them joined into one",
			spec.sum[0].factor.name)
	}

	// And the *display* source is what the author wrote, because a status page shows a
	// formula as the pack spells it rather than as the parser joined it.
	if got := spec.String(); got != "ac_base ac_dex_cap" {
		t.Errorf("the expression's source is %q, want what the author wrote", got)
	}

	if err := pack.validateVariables("probe", spec, false); err == nil {
		t.Error("a formula naming one name the scope does not hold compiled; the refusal has " +
			"to come from the scope check or a typo resolves to zero")
	}
}

// TestTheFormulaLanguageMultiplies is the one operator whose implementation was missing
// while its constant was declared, and it is asserted against the arithmetic rather than
// against the parser's acceptance.
//
// **Left to right, and no precedence to speak of**: `2 * 3 * 4` is 24 and `1 + 2 * 3` is
// 7. The language binds multiplication tighter than addition because the multiplicands
// attach to their left neighbour inside a term, and a formula needing `(a + b) * c` says
// so — which is the whole of the language's simplicity.
func TestTheFormulaLanguageMultiplies(t *testing.T) {
	t.Parallel()

	scope := map[string]int{
		"ac_base": 10, "ac_dex_cap": 5, "level": 3, "prof": 2,
	}

	for _, fixture := range []struct {
		text string
		want int
	}{
		{"2 * 3", 6},
		{"ac_base * 2", 20},
		{"2 * ac_base", 20},
		{"2 * 3 * 4", 24},
		{"1 + 2 * 3", 7},
		{"(1 + 2) * 3", 9},
		{"ac_base * ac_dex_cap", 50},
		{"-2 * 3", -6},
		{"(ac_base - ac_dex_cap) * 2", 10},
	} {
		t.Run(strconv.Quote(fixture.text), func(t *testing.T) {
			t.Parallel()

			expression, err := parseExpression(fixture.text)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			got, err := expression.Evaluate(scope)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}

			if got != fixture.want {
				t.Errorf("%s is %d, want %d", fixture.text, got, fixture.want)
			}
		})
	}
}

// TestASecondMultiplicandsVariableIsCheckedForScope is the gap
// `TestTheFormulaLanguageRefusesEverythingElse` left when `*` arrived.
//
// The load-time check walks a term's first factor, and a formula naming a variable **only**
// in its second one — `ac_base * cunning` — parsed and then resolved the unknown name to
// nothing. It is a small hole and it is the exact shape of the failure the check exists
// to prevent: a typo that produces a plausible wrong number rather than a refusal, in a
// formula whose whole job is to be a GM's tunable constant.
func TestASecondMultiplicandsVariableIsCheckedForScope(t *testing.T) {
	t.Parallel()

	pack := basePack(t)

	expression, err := parseExpression("ac_base * cunning")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if err := pack.validateVariables("probe", expression, false); err == nil {
		t.Error("a formula naming an unknown variable only in its second multiplicand compiled; " +
			"it would resolve to zero and produce a plausible wrong number")
	}

	if !strings.Contains(mustValidate(t, pack, "ac_base * ac_dex_cap"), "") {
		// A multiplication of two declared names must still load, or the refusal above is
		// achievable by refusing everything.
		t.Error("a multiplication of two declared constants was refused")
	}
}

// mustValidate validates one formula and returns the refusal, or the empty string.
func mustValidate(t *testing.T, pack *Pack, text string) string {
	t.Helper()

	file, err := parseExpression(text)
	if err != nil {
		t.Fatalf("Parse %q: %v", text, err)
	}

	if err := pack.validateVariables("probe", file, false); err != nil {
		return err.Error()
	}

	return ""
}
