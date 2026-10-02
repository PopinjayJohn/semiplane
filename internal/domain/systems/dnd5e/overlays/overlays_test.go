// This package's tests, all of them `overlays_test`, because every claim here is about what
// the two editions are **as a reviewer sees them** — two files, and the difference between
// them — and every one of those claims is answerable from outside.
//
// Nothing here reads an unexported field of `dnd5e`, and that is not an accident of
// authorship: it is the boundary §10.4 draws, and the strongest form of "the editions differ
// only where they differ" is a test that *cannot* reach past the engine's public surface.
// Where a compiled row's fields are unexported (`dnd5e`'s `conditionRow`), the assertion is
// made against the YAML the overlay shipped and against the payload the product actually
// renders — a stronger pair than a reflection walk would be.
package overlays_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/conformance"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e/overlays"
)

// # The edition differences, named once
//
// §10.4 names three and `dnd5e`'s `hooks.go` names a fourth. Every difference test here is
// written against one list, and `TestTheOnlyDifferencesBetweenTheTwoOverlayFilesAreTheEditionRules`
// computes the answer rather than trusting the list — so a difference nobody thought of
// shows up as a difference this file does not account for.
//
// All four are **data**, and "data" is the claim:
//
//  1. `crit_damage_die_max` — 2014 crits on a natural 20 from the attack die and on nothing
//     else; 2024 also crits when a damage die shows its maximum.
//  2. `crit_ignored_by_incapacitated` plus `critical_exempt` on three condition rows — 2024
//     exempts attacks against an incapable, paralysed or unconscious creature.
//  3. `attack.mastery.enabled` — 2014 has no weapon mastery properties at all.
//  4. The `ancestry` kind's label — "Race" in 2014, "Species" in 2024.

// editionDifferences is the list above, as the `path=value` comparisons a test makes.
//
// **Paths and not accessors**, because the comparison that matters is between two *files*
// and a file has no accessors. One path per difference, plus the two paths that are
// identity rather than rule (`title` and `version`) — which is what a campaign's
// fingerprint needs in order to gate a resume on this file rather than on the base pack.
var editionDifferences = []string{
	// Rules.
	"attack.mastery.enabled",
	"kinds[0].label",

	// Identity: which file this is. Sorted with the rules, because the list is compared
	// against a computed, sorted answer and a hand-ordered list would be a second thing
	// to keep in step.
	"title",
	"toggles.crit_damage_die_max",
	"toggles.crit_ignored_by_incapacitated",
	"version",
}

// editionAgreements are the paths both files declare with the *same* value, and the list is
// closed: a path added to both files and not here fails the difference test, because the
// computed "agreeing" set then carries an entry this table does not.
//
// **It is a list and not a sentence.** "The two editions agree about the hooks" is prose;
// these five lines are a thing a reviewer can check, and the point of the test is that the
// *whole* of the agreement is here rather than wherever the last contributor put it.
var editionAgreements = []string{
	"hooks.critical",
	"hooks.mastery",
	"kinds[0].slug",
	"system",
	"toggles.crit_attack_die_max",
}

// exemptedConditions are the three conditions 2024 exempts from the critical rule.
//
// **All three, and the engine's own prose names two.** `dnd5e`'s `hooks.go` and
// `packfile.go` both describe the exemption as "paralysed or unconscious", while 2024's
// rule covers an *incapacitated* target as well — which is what the switch is called:
// `crit_ignored_by_incapacitated`. The column is the pack's to fill, so the pack fills it
// with the rule and the engine's comment is narrower than the rule it describes. Reported to
// the integrator rather than worked around.
//
//nolint:misspell // `paralyzed` is `dnd5e`'s own slug for the condition, spelled as the base
//nolint:misspell // pack spells it and as 5e's own rules text spells it. "Correcting" it here would
//nolint:misspell // make this list name something no pack declares.
var exemptedConditions = []string{"incapacitated", "paralyzed", "unconscious"}

// # Both editions, built

// built is one edition and the system it resolves to.
type built struct {
	id      overlays.EditionID
	edition overlays.Edition
	engine  *dnd5e.Engine
}

// pack returns the compiled pack an edition resolved to.
func (b *built) pack(t *testing.T) *dnd5e.Pack {
	t.Helper()

	if b.engine == nil {
		t.Fatalf("the %s edition has no engine", b.id)
	}

	return b.engine.Pack()
}

// everyEdition builds every edition this package ships, in declaration order.
//
// **A slice, and no map keyed by edition**, because the order a test reports its subtests
// in is a property of this repository's reports, and `overlays.IDs()` declares that order
// once. A map would need a walk to iterate, and S-10.4's audit is about the construct even
// where the outcome happens to be harmless.
func everyEdition(t *testing.T) []*built {
	t.Helper()

	editions := make([]*built, 0, len(overlays.IDs()))

	for _, id := range overlays.IDs() {
		edition, err := overlays.ByID(id)
		if err != nil {
			t.Fatalf("%s: parsing the overlay: %v", id, err)
		}

		engine, err := edition.System()
		if err != nil {
			t.Fatalf("%s: building the system: %v", id, err)
		}

		editions = append(editions, &built{id: id, edition: edition, engine: engine})
	}

	return editions
}

// editionNamed builds one edition, and fails if it is not there.
//
// **A scan rather than a lookup by index**, so that a third edition added to
// `overlays.IDs()` makes every test that names an edition keep working rather than silently
// indexing a slice that grew. The failure is loud for the case that matters — a fixture
// named by a test and absent from the product — because that is the `A11Y_ROUTE_PKGS`
// argument applied to a fixture.
func editionNamed(t *testing.T, id overlays.EditionID) *built {
	t.Helper()

	for _, candidate := range everyEdition(t) {
		if candidate.id == id {
			return candidate
		}
	}

	t.Fatalf("no edition %q is shipped, so this test cannot reach the fixture it names", id)

	return nil
}

// basePack compiles the base pack on its own, which is what both editions are compared
// against for everything they did not change.
func basePack(t *testing.T) *dnd5e.Pack {
	t.Helper()

	pack, err := dnd5e.ParsePack(dnd5e.BasePackYAML())
	if err != nil {
		t.Fatalf("compiling the base pack: %v", err)
	}

	return pack
}

// baseSystem is the base pack with no overlay, which §10.4 makes a first-class system.
func baseSystem(t *testing.T) *dnd5e.Engine {
	t.Helper()

	engine, err := dnd5e.New(dnd5e.Options{})
	if err != nil {
		t.Fatalf("building the base system: %v", err)
	}

	return engine
}

// TestBothEditionsAreRegistrableSystems is the floor, and it is deliberately boring: each
// edition produces a `rules.System` the registrar would accept.
//
// **A smoke test, and it is here because the failure it catches is the worst one.** An
// overlay that does not compile is not "an edition with a mistake in it" — it is a
// deployment that cannot start, and every other test here would report a confusing
// failure from a fixture instead. `rules.Validate` is the registrar's own check rather than
// a set of assertions of my own, so this test cannot pass against a system `internal/plugin`
// would have refused.
func TestBothEditionsAreRegistrableSystems(t *testing.T) {
	t.Parallel()

	for _, fixture := range everyEdition(t) {
		t.Run(string(fixture.id), func(t *testing.T) {
			t.Parallel()

			if err := rules.Validate(fixture.engine); err != nil {
				t.Fatalf("the %s edition is not a registrable system: %v", fixture.id, err)
			}
		})
	}
}

// TestEachEditionIsTheOverlayItSaysItIs is the "did the file actually apply" check, and it
// is separate from the smoke test because the failure it catches is *silent*.
//
// `Overlay.apply` is unexported and its one caller is `dnd5e.New`, so the only way an
// overlay could fail to apply is a merge that dropped it — and a merge that dropped it would
// produce a system that loads, resolves, and records the overlay's version in the
// fingerprint while answering the base pack's rules. §10.8's gate cannot see that. So each
// engine's own merged pack is asked what it thinks it is.
func TestEachEditionIsTheOverlayItSaysItIs(t *testing.T) {
	t.Parallel()

	base := baseSystem(t)

	for _, fixture := range everyEdition(t) {
		t.Run(string(fixture.id), func(t *testing.T) {
			t.Parallel()

			if got := fixture.engine.OverlayVersion(); got != fixture.edition.Version() {
				t.Errorf("the %s edition's overlay component is %q and its own version is "+
					"%q; the overlay did not apply to the pack this system resolves under",
					fixture.id, got, fixture.edition.Version())
			}

			if got := fixture.engine.Pack().Title(); got != fixture.edition.Name() {
				t.Errorf("the merged pack's title is %q and the overlay's is %q; the title is "+
					"how a status page names what a campaign resolved to", got, fixture.edition.Name())
			}

			// And the two are not the base pack wearing a version. `MasteryEnabled` is
			// one edition's difference and the ancestry label is the other's, so a system
			// that had silently fallen back to the base would differ from it here.
			if fixture.id == overlays.Dnd5e2014 {
				if fixture.engine.Pack().MasteryEnabled() {
					t.Error("the 2014 edition resolves weapon masteries, which 2014 does not " +
						"have; the overlay did not apply")
				}

				if got := labelOf(t, fixture.pack(t), "ancestry"); got != "Race" {
					t.Errorf("the 2014 edition calls an ancestry %q, want %q", got, "Race")
				}
			}

			if fixture.id == overlays.Dnd5e2024 && !fixture.engine.Pack().MasteryEnabled() {
				t.Error("the 2024 edition does not resolve weapon masteries, and 2024 has " +
					"them; the overlay did not apply")
			}

			// Every edition still inherits the base pack's own version, which is the
			// component a base-pack revision gates on.
			if got, want := fixture.engine.PackVersion(), base.PackVersion(); got != want {
				t.Errorf("the %s edition reports the base pack's component as %q and the base "+
					"pack is %q; a base-pack revision would gate nothing", fixture.id, got, want)
			}
		})
	}
}

// # The two files, and what a reviewer can see in them

// document decodes one overlay file into a generic tree.
//
// **A `map[string]any` and not `dnd5e`'s own file shape**, for two reasons that point the
// same way: this test is about what the file *declares*, so it must not read it through a
// decoder that accepts a misspelled key or fills in a default; and it must see the
// `conditions` block in its *file* form, which the compiled pack deliberately does not expose.
func document(t *testing.T, id overlays.EditionID) map[string]any {
	t.Helper()

	source, err := overlays.Source(id)
	if err != nil {
		t.Fatalf("reading the %s edition's source: %v", id, err)
	}

	return decodeDocument(t, source)
}

// baseDocument is the base pack decoded the same way, so the two are comparable.
func baseDocument(t *testing.T) map[string]any {
	t.Helper()

	return decodeDocument(t, dnd5e.BasePackYAML())
}

func decodeDocument(t *testing.T, source []byte) map[string]any {
	t.Helper()

	var decoded map[string]any

	if err := yaml.Unmarshal(source, &decoded); err != nil {
		t.Fatalf("a pack does not decode: %v", err)
	}

	if len(decoded) == 0 {
		t.Fatal("a pack decoded to nothing, so every assertion about it would be vacuous")
	}

	return decoded
}

// flattened is a document as a map from leaf path to the text a diff would show.
//
// **Built during the walk rather than resolved afterwards**, because a path like
// `kinds[0].label` is not a key of the document — `kinds` is — and a resolver that split the
// path on dots and looked it up would read nothing and report an empty document. Sorted
// iteration and a `slices.Sorted(maps.Keys(...))` over it are the sanctioned spelling.
func flattened(t *testing.T, doc map[string]any) map[string]string {
	t.Helper()

	leaves := make(map[string]string)

	var walk func(prefix string, node any)

	walk = func(prefix string, node any) {
		switch value := node.(type) {
		case map[string]any:
			for _, key := range sortedKeys(value) {
				walk(prefix+"."+key, value[key])
			}
		case []any:
			for index, entry := range value {
				walk(prefix+"["+strconv.Itoa(index)+"]", entry)
			}
		default:
			// `fmt.Sprint` rather than a switch over Go types: the value came out of
			// YAML and the three types it produces are `bool`, `int` and `string`, all of
			// which render themselves as the file spells them. A helper with a `switch`
			// would be a second answer to "how is a YAML scalar spelled", and one of the
			// three would be wrong.
			leaves[prefix] = fmt.Sprint(value)
		}
	}

	for _, key := range sortedKeys(doc) {
		walk(key, doc[key])
	}

	return leaves
}

// sortedKeys returns a mapping's keys in sorted order.
func sortedKeys(node map[string]any) []string {
	return slices.Sorted(maps.Keys(node))
}

// difference is the comparison of two flattened documents.
type difference struct {
	// differing are the paths both declare with different values.
	differing []string

	// agreeing are the paths both declare with the same value.
	agreeing []string

	// onlyLeft and onlyRight are the paths exactly one of them declares.
	onlyLeft  []string
	onlyRight []string
}

// compareDocuments is the whole of §10.4's claim, as a value a test can assert on.
//
// **Both directions, and the "only in one" halves are what make it a claim about the files
// rather than about the differences.** A test comparing only the values would pass against
// an overlay that restated the entire base pack and happened to differ in four places —
// which is the failure the second requirement names, and which a reviewer reading two
// hundred lines would not notice.
func compareDocuments(left, right map[string]string) difference {
	var out difference

	for _, path := range slices.Sorted(maps.Keys(left)) {
		other, both := right[path]
		if !both {
			out.onlyLeft = append(out.onlyLeft, path)

			continue
		}

		if left[path] == other {
			out.agreeing = append(out.agreeing, path)

			continue
		}

		out.differing = append(out.differing, path)
	}

	for _, path := range slices.Sorted(maps.Keys(right)) {
		if _, both := left[path]; !both {
			out.onlyRight = append(out.onlyRight, path)
		}
	}

	return out
}

// TestTheOnlyDifferencesBetweenTheTwoOverlayFilesAreTheEditionRules is §10.4's sentence as
// an assertion: *"The differences show up as reviewable data diffs."*
//
// **The computed answer, not the list.** This test flattens both files and asks what is
// left over — what differs, what agrees, what exactly one of them declares — and compares
// all four sets to the lists at the top of this file. A test that only asserted the four
// named differences would pass against an overlay that *also* restated the proficiency table
// with the same values; this one cannot, because the agreement set is closed.
//
// The direction that matters most is `onlyRight`: 2024 restates three condition rows because
// `dnd5e`'s merge replaces a row whole rather than a row's fields. That cost is asserted
// here — precisely those rows and nothing else — so it is a decision a reviewer sees rather
// than a surprise they meet while reading.
func TestTheOnlyDifferencesBetweenTheTwoOverlayFilesAreTheEditionRules(t *testing.T) {
	t.Parallel()

	fourteen := flattened(t, document(t, overlays.Dnd5e2014))
	twentyFour := flattened(t, document(t, overlays.Dnd5e2024))

	got := compareDocuments(fourteen, twentyFour)

	if !slices.Equal(got.differing, editionDifferences) {
		t.Errorf(
			"the two editions differ at %v; this file accounts for %v. Every edition "+
				"difference has to be named here, because this list is what a reviewer reads",
			got.differing, editionDifferences,
		)
	}

	if !slices.Equal(got.agreeing, editionAgreements) {
		t.Errorf(
			"the two editions agree at %v; this file accounts for %v. A path both editions "+
				"declare is a path neither has an opinion about, and an unlisted one is an "+
				"opinion nobody reviewed",
			got.agreeing, editionAgreements,
		)
	}

	if len(got.onlyLeft) != 0 {
		t.Errorf("the 2014 overlay declares %v and 2024 does not; an edition difference is "+
			"never the absence of something the other edition has", got.onlyLeft)
	}

	// The 2024-only paths, asserted as a collection plus a slug set rather than as
	// nineteen literals: the rows themselves are held against the base pack's rows by
	// `TestTheTwentyTwentyfourConditionRowsAreTheBaseRowsPlusTheExemption`, and repeating
	// their text here would make a typo fix a two-test edit for no gain.
	for _, path := range got.onlyRight {
		if !strings.HasPrefix(path, "conditions[") {
			t.Errorf("the 2024 overlay declares %q and the 2014 overlay does not; the only "+
				"declared-and-not collection is the critical-hit exemption, and a new one is "+
				"a fifth edition difference that has to be named", path)
		}
	}

	restated := rowsBySlug(t, document(t, overlays.Dnd5e2024), "conditions")
	gotSlugs := slices.Sorted(maps.Keys(restated))

	if !slices.Equal(gotSlugs, exemptedConditions) {
		t.Errorf("the 2024 overlay restates the conditions %v; the ones it exempts from the "+
			"critical rule are %v. Both lists are sorted and closed, so a fourth restated "+
			"condition fails here rather than shipping",
			gotSlugs, exemptedConditions)
	}
}

// # A row-level difference costs a row, and here is the bill

// TestTheTwentyTwentyfourConditionRowsAreTheBaseRowsPlusTheExemption is the guard on the one
// place where this work item copied text out of the base pack.
//
// `dnd5e`'s merge replaces a **row** when an overlay declares one; it does not merge a
// row's fields. So setting `critical_exempt` on a row the 2024 overlay inherited costs the
// whole row, and these three blocks are that cost paid in full. It is a second copy of
// regenerable text — the failure ADR 0027 names for the page index ("a second copy of a
// regenerable block is a second answer"), arriving through YAML instead of SQLite. The
// alternative, leaving 2024's exemption unimplemented, was rejected outright: an overlay that
// silently does not apply is the one failure a ruleset fingerprint cannot detect, because
// the overlay loaded, its version was recorded, and the campaign resolved under 2014's rule
// while claiming 2024's.
//
// So the copy is made **loud instead of silent**: every field of every restated row is
// required to equal the base pack's own, except a named exception per row. A typo fixed in
// `dnd5e/data/base.yaml` therefore fails *this* test instead of forking the 2024 edition's
// copy of that condition forever.
//
// **Three directions, and each can fail.** A row missing a field the base declares, a row
// carrying a field the base does not, and a row whose exempt flag is wrong are three
// different mistakes with three different fixes, and one "the rows differ" assertion would
// name one of them in a message that sends a reader to the wrong place.
func TestTheTwentyTwentyfourConditionRowsAreTheBaseRowsPlusTheExemption(t *testing.T) {
	t.Parallel()

	// The one summary that moves, and why: the base pack's `unconscious` row says hits
	// against it "are critical", which is the rule `crit_ignored_by_incapacitated` turns
	// off. A pack that exempted the rule while displaying an explanation of the opposite
	// would contradict itself where a player reads it.
	allowed := map[string][]string{
		"incapacitated": {"critical_exempt"},

		//nolint:misspell // the base pack's own slug; see `exemptedConditions`.
		"paralyzed": {"critical_exempt"},

		"unconscious": {"critical_exempt", "summary"},
	}

	if len(allowed) != len(exemptedConditions) {
		t.Fatalf("the exemption table covers %d of the %d conditions this file names; a "+
			"condition with no entry would be compared against the base pack as though "+
			"nothing about it changed",
			len(allowed), len(exemptedConditions))
	}

	base := rowsBySlug(t, baseDocument(t), "conditions")
	overlaid := rowsBySlug(t, document(t, overlays.Dnd5e2024), "conditions")

	if len(overlaid) != len(exemptedConditions) {
		t.Errorf("the 2024 overlay restates %d conditions (%v) and the exemption covers %d "+
			"(%v); the restated set is the exemption's set, so a reader sees what 2024 changed "+
			"by reading what it restated",
			len(overlaid), slices.Sorted(maps.Keys(overlaid)),
			len(exemptedConditions), exemptedConditions)
	}

	for _, slug := range exemptedConditions {
		restated, present := overlaid[slug]
		if !present {
			t.Errorf("the 2024 overlay does not restate %q, so the pack does not exempt it "+
				"and the toggle has nothing to switch on", slug)

			continue
		}

		original, declared := base[slug]
		if !declared {
			t.Errorf("the base pack declares no %q, so an overlay restating it is a row this "+
				"engine has no other answer for", slug)

			continue
		}

		permitted := allowed[slug]

		for _, key := range sortedKeys(restated) {
			if key == "slug" {
				continue
			}

			value := fmt.Sprint(restated[key])
			inherited, wasDeclared := renderIfPresent(original, key)

			switch {
			case !slices.Contains(permitted, key):
				if !wasDeclared {
					t.Errorf("%s declares %q, which the base pack does not; a restated row may "+
						"add %v and nothing else", slug, key, permitted)

					continue
				}

				if value != inherited {
					t.Errorf("%s's %q is %q in the 2024 overlay and %q in the base pack; this "+
						"row is a copy of the base's with only %v changed, so a copy that has "+
						"drifted is a second answer to what the condition is",
						slug, key, value, inherited, permitted)
				}
			case key == "critical_exempt" && value != "true":
				t.Errorf("%s's %q is %q in the 2024 overlay; a row restated to exempt itself "+
					"and does not is a row that silently changed something else",
					slug, key, value)
			case key == "summary" && !rewritesFinalClause(inherited, value):
				t.Errorf("%s's summary is %q where the base pack says %q; the only summary "+
					"sentence 2024 rewrites is the last clause, because that clause is the "+
					"rule the exemption turns off. A summary that differs anywhere else is a "+
					"copy that has drifted, and a summary identical to the base's states the "+
					"rule this edition denies",
					slug, value, inherited)
			}
		}

		for _, key := range sortedKeys(original) {
			if key == "slug" || slices.Contains(permitted, key) {
				continue
			}

			if _, restatedIt := restated[key]; !restatedIt {
				t.Errorf("%s does not restate %q, which the base pack declares; `dnd5e`'s "+
					"merge replaces a row whole, so a missing field is a field this edition "+
					"silently dropped", slug, key)
			}
		}

		// And the permitted keys must be **present**, which is the other half of "may
		// differ": a row that restates a condition and omits the field the whole restatement
		// exists for is a row that silently dropped the edition rule. Without this arm a
		// mutation that deletes `critical_exempt` from one row passes — the loop above only
		// ever looks at fields the row *has*.
		for _, key := range permitted {
			if _, restatedIt := restated[key]; !restatedIt {
				t.Errorf("%s does not restate %q, which is the field the restatement exists "+
					"for; a missing field is a field this edition silently dropped, because "+
					"`dnd5e`'s merge replaces a row whole", slug, key)
			}
		}
	}
}

// clauseJoin is the conjunction `unconscious`'s summary joins its clauses with, and the
// reason `rewritesFinalClause` can say *which* sentence 2024 rewrote without repeating it.
const clauseJoin = ", and "

// rewritesFinalClause reports whether `rewritten` is `original` with only its last clause
// changed.
//
// **A clause comparison and not a string equality**, and the difference is the whole
// function. "May differ" would have let two mistakes through that this package shipped once:
// a summary that merely happened to be different from the base's (a typo in a clause the
// edition has no opinion about is still a second answer to what the condition is), and a
// summary reverted to the base's wording (which states the rule this edition denies while
// the switch above denies it). So the shared clauses must be byte-identical, the clause
// count must match, and the final clause must actually differ.
//
// The count check is what catches a summary that was *rewritten* rather than amended: adding
// a clause changes what the sentence asserts, and this edition's difference is one sentence
// inside an existing summary rather than a new sentence.
func rewritesFinalClause(original, rewritten string) bool {
	before := strings.Split(original, clauseJoin)
	after := strings.Split(rewritten, clauseJoin)

	if len(before) != len(after) || len(before) < 2 {
		return false
	}

	for index := range before[:len(before)-1] {
		if before[index] != after[index] {
			return false
		}
	}

	return before[len(before)-1] != after[len(after)-1]
}

// renderIfPresent renders a row's field, reporting whether the row declares it.
func renderIfPresent(row map[string]any, key string) (string, bool) {
	value, present := row[key]
	if !present {
		return "", false
	}

	return fmt.Sprint(value), true
}

// rowsBySlug returns the rows of one collection in a document, keyed by `slug`.
//
// **A refusal on a duplicate rather than a silent overwrite**: `dnd5e` refuses a pack with
// two rows sharing a slug, and a fixture that quietly kept one of them would be asserting
// about a row neither file declared.
func rowsBySlug(t *testing.T, doc map[string]any, collection string) map[string]map[string]any {
	t.Helper()

	node, present := doc[collection]
	if !present {
		t.Fatalf("a document declares no %q", collection)
	}

	rows, isSequence := node.([]any)
	if !isSequence {
		t.Fatalf("%q is not a list of rows", collection)
	}

	bySlug := make(map[string]map[string]any, len(rows))

	for index, entry := range rows {
		row, isMapping := entry.(map[string]any)
		if !isMapping {
			t.Fatalf("%s[%d] is not a row", collection, index)
		}

		slug := fmt.Sprint(row["slug"])
		if slug == "" {
			t.Fatalf("%s[%d] has no slug", collection, index)
		}

		if _, repeated := bySlug[slug]; repeated {
			t.Fatalf("%s is declared twice", slug)
		}

		bySlug[slug] = row
	}

	return bySlug
}

// TestAnOverlayRestatesNothingThatTheBasePackAlreadySays is the *sibling* requirement, and
// it is why the previous test's shape is not enough.
//
// A pair of identical overlays would pass the difference test only if the four differences
// were deleted, so that test cannot be the whole guard. What it cannot see is an overlay that
// **restates the base pack wholesale** — every row, every formula, every band — and would
// then differ in four places while being a second base pack, which defeats the reviewable
// diff by making the reviewer read two hundred lines to find four.
//
// So this test asserts each overlay's *shape*: which top-level blocks it declares, and how
// many rows each of its collections holds. An overlay that grew a `formulas:` or
// `reference:` block fails here, and the message names what it restated.
func TestAnOverlayRestatesNothingThatTheBasePackAlreadySays(t *testing.T) {
	t.Parallel()

	// The blocks each overlay may declare, and `conditions` is 2024's alone — the
	// exemption's cost. Everything absent is inherited, which is the whole point.
	blocks := map[overlays.EditionID][]string{
		overlays.Dnd5e2014: {"attack", "hooks", "kinds", "system", "title", "toggles", "version"},
		overlays.Dnd5e2024: {
			"attack", "conditions", "hooks", "kinds", "system", "title", "toggles", "version",
		},
	}

	for _, fixture := range everyEdition(t) {
		t.Run(string(fixture.id), func(t *testing.T) {
			t.Parallel()

			doc := document(t, fixture.id)
			want := blocks[fixture.id]

			if len(want) == 0 {
				t.Fatalf("this file accounts for no blocks of the %s edition, so it cannot "+
					"reach the fixture it names", fixture.id)
			}

			if got := sortedKeys(doc); !slices.Equal(got, want) {
				t.Errorf("the %s overlay declares %v; an overlay is a *patch*, so the blocks "+
					"it declares are %v and everything else is inherited. A restated block "+
					"is a second copy of the base pack's answers",
					fixture.id, got, want)
			}

			// The row counts, which is where a wholesale restatement shows up first.
			for _, collection := range []string{"kinds"} {
				rows, present := collectRows(t, doc, collection)
				if !present {
					t.Errorf("the %s overlay declares no %q rows", fixture.id, collection)

					continue
				}

				if len(rows) != 1 {
					t.Errorf("the %s overlay restates %d %s rows and this file accounts for "+
						"one; the base pack declares them all, and an overlay that repeats "+
						"them is a second base pack",
						fixture.id, len(rows), collection)
				}
			}
		})
	}
}

// collectRows returns one collection's rows, or reports that the document has none.
func collectRows(t *testing.T, doc map[string]any, collection string) ([]any, bool) {
	t.Helper()

	node, present := doc[collection]
	if !present {
		return nil, false
	}

	rows, isSequence := node.([]any)
	if !isSequence {
		t.Fatalf("%q is not a list of rows", collection)
	}

	return rows, true
}

// # Where they must agree

// TestTheEditionsAreIdenticalWhereTheyShould is the compiled-pack half of "patch, not
// redefine", and it is the direction a difference test cannot see: two packs can differ in
// four places and still be different *packs*.
//
// Everything §10.4 says the two editions share, read through the engine's exported
// accessors, so a change to either file that moved a die face or a formula fails here. Each
// comparison is its own assertion rather than a table of `any`, because a table over
// heterogeneous slice types needs a boxing helper and the failure message would say the same
// thing five times.
func TestTheEditionsAreIdenticalWhereTheyShould(t *testing.T) {
	t.Parallel()

	base := basePack(t)
	fourteen := editionNamed(t, overlays.Dnd5e2014).pack(t)
	twentyFour := editionNamed(t, overlays.Dnd5e2024).pack(t)

	if fourteen.System() != twentyFour.System() || fourteen.Notation() != twentyFour.Notation() {
		t.Errorf("the editions name the system %q/%q and the notation %q/%q; one system id "+
			"for both is ADR 0045's decision and a notation difference would be a fork",
			fourteen.System(), twentyFour.System(), fourteen.Notation(), twentyFour.Notation())
	}

	if !slices.Equal(fourteen.DieSizes(), twentyFour.DieSizes()) {
		t.Errorf("the editions admit dice %v and %v; the notation is shared mechanics",
			fourteen.DieSizes(), twentyFour.DieSizes())
	}

	if !slices.Equal(fourteen.ToggleNames(), twentyFour.ToggleNames()) {
		t.Errorf("the editions declare switches %v and %v; §10.4 says an edition's switch "+
			"list is larger than any one build reads, so the two carry the same list",
			fourteen.ToggleNames(), twentyFour.ToggleNames())
	}

	if !slices.Equal(fourteen.FormulaNames(), twentyFour.FormulaNames()) {
		t.Errorf("the editions declare formulas %v and %v; a formula is a rule and one of "+
			"them is shared", fourteen.FormulaNames(), twentyFour.FormulaNames())
	}

	if fourteen.DamageCap() != twentyFour.DamageCap() {
		t.Errorf("the editions cap an attack's damage at %d and %d, and neither edition "+
			"caps it", fourteen.DamageCap(), twentyFour.DamageCap())
	}

	if !slices.Equal(fourteen.Kinds(), twentyFour.Kinds()) {
		t.Errorf("the editions declare the kinds %v and %v; an edition changes a kind's label "+
			"and not which kinds there are", fourteen.Kinds(), twentyFour.Kinds())
	}

	// The collections by length, because a row's fields are unexported and a length is the
	// strongest thing an external test can say about "the same rows".
	for _, fixture := range []struct {
		what            string
		base, four, two int
	}{
		{"abilities", len(base.Abilities()), len(fourteen.Abilities()), len(twentyFour.Abilities())},
		{
			"conditions",
			len(base.Conditions()),
			len(fourteen.Conditions()),
			len(twentyFour.Conditions()),
		},
		{"masteries", len(base.Masteries()), len(fourteen.Masteries()), len(twentyFour.Masteries())},
	} {
		if fixture.four != fixture.base || fixture.two != fixture.base {
			t.Errorf("the editions have %d and %d %s where the base pack has %d; an edition "+
				"adds or removes rows only where it says so",
				fixture.four, fixture.two, fixture.what, fixture.base)
		}
	}

	// Both hooks, named in both editions and bound to the same rule. A change here is a
	// change to the *procedure* both editions share, and it would be a third Go rule
	// wearing a data diff's clothes — which is the one thing ADR 0045's
	// `TestEveryHookIsNamedInThePackAndThePackNamesNoOther` is asking about.
	for _, hook := range []string{"critical", "mastery"} {
		left, leftNamed := fourteen.Hook(hook)
		right, rightNamed := twentyFour.Hook(hook)

		if !leftNamed || !rightNamed {
			t.Errorf(
				"the %q hook is named by only one edition (%t, %t)",
				hook,
				leftNamed,
				rightNamed,
			)

			continue
		}

		if left != right {
			t.Errorf("the %q hook is bound to %q in 2014 and %q in 2024; both editions share "+
				"the same procedure, so a difference here is a fork", hook, left, right)
		}
	}
}

// TestTheEditionsAgreeOnEveryConditionButOne is the condition-reference view, field for
// field, and it is where the *product* is asked rather than the pack.
//
// `dnd5e`'s `conditionRow` has no exported fields, so this is the strongest external
// statement available about fifteen rows of conditions: the payload the condition-reference
// page actually renders. It also catches what a length comparison cannot — an overlay that
// restated a row and dropped its summary would keep the count and lose the meaning.
//
// **Exactly one entry may differ**, and *which* one is asserted rather than counted: a count
// would pass for a difference in the wrong condition.
func TestTheEditionsAgreeOnEveryConditionButOne(t *testing.T) {
	t.Parallel()

	baseEntries := conditionReference(t, baseSystem(t))
	fourteen := conditionReference(t, editionNamed(t, overlays.Dnd5e2014).engine)
	twentyFour := conditionReference(t, editionNamed(t, overlays.Dnd5e2024).engine)

	if len(baseEntries) != len(fourteen) || len(baseEntries) != len(twentyFour) {
		t.Fatalf("the condition reference has %d entries under the base pack, %d under 2014 "+
			"and %d under 2024; an edition changes what a condition is, not which conditions "+
			"there are", len(baseEntries), len(fourteen), len(twentyFour))
	}

	// The 2014 edition is the base pack verbatim — it inherits every row — so it is
	// compared entry for entry with no tolerance at all.
	for index, entry := range fourteen {
		if entry != baseEntries[index] {
			t.Errorf("the 2014 edition's condition reference differs from the base pack's at "+
				"entry %d:\n base: %s\n 2014: %s", index, baseEntries[index], entry)
		}
	}

	differing := make([]string, 0, 2)

	for index, entry := range twentyFour {
		if entry == fourteen[index] {
			continue
		}

		differing = append(differing, entry)

		if !strings.Contains(entry, "unconscious") {
			t.Errorf("the 2024 edition's condition reference differs from the 2014 edition's "+
				"at an entry that is not `unconscious`: %s", entry)
		}
	}

	if len(differing) != 1 {
		t.Errorf("the 2024 edition's condition reference differs from the 2014 edition's in "+
			"%d entries (%v); one is deliberate — the `unconscious` summary, which says a hit "+
			"against it is critical in 2014 and does not in 2024 — and a second is an edition "+
			"difference nobody decided on", len(differing), differing)
	}
}

// conditionReference renders the `condition-reference` view: every condition the pack
// declares, with what being in it does.
//
// **JSON rather than a type assertion**, and the reason is the boundary: `dnd5e`'s
// `conditionSummary` is unexported and a payload is deliberately opaque to semiplane.
// Marshalling the value is the only way an outside test can read it, and it is the same bytes
// a renderer would draw — so this asserts about the page rather than about the pack.
func conditionReference(t *testing.T, engine *dnd5e.Engine) []string {
	t.Helper()

	payload, err := engine.Derive(rules.State{}, rules.Query{View: dnd5e.ViewConditionReference})
	if err != nil {
		t.Fatalf("asking for the condition reference: %v", err)
	}

	if payload.View() != dnd5e.ViewConditionReference {
		t.Fatalf("the payload answers %q, want %q", payload.View(), dnd5e.ViewConditionReference)
	}

	encoded, err := json.Marshal(payload.Value())
	if err != nil {
		t.Fatalf("encoding the condition reference: %v", err)
	}

	var entries []map[string]any

	if err := json.Unmarshal(encoded, &entries); err != nil {
		t.Fatalf("decoding the condition reference: %v", err)
	}

	rendered := make([]string, 0, len(entries))

	for _, entry := range entries {
		fields := make([]string, 0, len(entry))

		for _, key := range sortedKeys(entry) {
			fields = append(fields, key+"="+fmt.Sprint(entry[key]))
		}

		rendered = append(rendered, strings.Join(fields, " "))
	}

	return rendered
}

// # Where they must differ

// TestTheEditionsDifferWhereTheyMust is the compiled-pack counterpart of the diff test, and
// it is here because a **declared** difference and an **effective** difference are not the
// same thing.
//
// The failure it catches is the one `dnd5e` is built to prevent and the one no amount of
// reading YAML would reveal: a switch the engine does not read, or reads under a name that
// does not match. `toggles` is a `map[string]bool`, so `crit_damage_die_mx` is a key nothing
// reads and a rule that quietly never fires — the pack loads, the fingerprint records the
// overlay's version, and the campaign resolves under 2014's critical rule while claiming
// 2024's.
//
// So this reads the *compiled* pack, which is the only place a switch's spelling is settled.
func TestTheEditionsDifferWhereTheyMust(t *testing.T) {
	t.Parallel()

	fourteen := editionNamed(t, overlays.Dnd5e2014).pack(t)
	twentyFour := editionNamed(t, overlays.Dnd5e2024).pack(t)

	// Difference 4, and the whole of "race naming".
	for _, fixture := range []struct {
		id   overlays.EditionID
		pack *dnd5e.Pack
		want string
	}{
		{overlays.Dnd5e2014, fourteen, "Race"},
		{overlays.Dnd5e2024, twentyFour, "Species"},
	} {
		if got := labelOf(t, fixture.pack, "ancestry"); got != fixture.want {
			t.Errorf(
				"the %s edition calls an `ancestry` %q, want %q",
				fixture.id,
				got,
				fixture.want,
			)
		}
	}

	// Difference 3: no weapon mastery properties at all.
	if fourteen.MasteryEnabled() {
		t.Error("the 2014 edition resolves weapon masteries, and 2014 has none")
	}

	if !twentyFour.MasteryEnabled() {
		t.Error("the 2024 edition does not resolve weapon masteries, and 2024 has them")
	}

	// Differences 1 and 2, plus the switch both editions share so that a change to *it*
	// fails here rather than passing as "no difference".
	for _, fixture := range []struct {
		name             string
		wantOld, wantNew bool
	}{
		{"crit_attack_die_max", true, true},
		{"crit_damage_die_max", false, true},
		{"crit_ignored_by_incapacitated", false, true},
	} {
		if got := fourteen.Toggle(fixture.name); got != fixture.wantOld {
			t.Errorf("the 2014 edition reads %q as %v, want %v",
				fixture.name, got, fixture.wantOld)
		}

		if got := twentyFour.Toggle(fixture.name); got != fixture.wantNew {
			t.Errorf("the 2024 edition reads %q as %v, want %v",
				fixture.name, got, fixture.wantNew)
		}
	}

	// The two must not collide on identity either: one system, two packs, and the
	// fingerprint has to be able to tell them apart.
	if fourteen.Version() == twentyFour.Version() {
		t.Errorf("both editions report the pack version %q; §10.8 gates a resume on it, and a "+
			"campaign that changed edition would resume silently", fourteen.Version())
	}
}

// labelOf returns a declared kind's display name.
func labelOf(t *testing.T, pack *dnd5e.Pack, kind rules.Kind) string {
	t.Helper()

	label, found := pack.KindLabel(kind)
	if !found {
		t.Fatalf("the pack declares no kind %q", kind)
	}

	return label
}

// # The behavioural fixtures

// The ids the fixtures' two tokens are placed under.
const (
	attackerID rules.ObjectID = "vurg_1"
	defenderID rules.ObjectID = "kobold_1"
)

// The scenario's actor, which is the attacker token's owner — see
// `suiteConfig`.
const scenarioActor = 7

// twenty is the primary die's face count, which three subtests compare against.
const twenty = 20

// defenderHP is the defender's hit points: far above any damage the fixture rolls, so
// nothing clamps. **A clamp would make two different rules produce the same hit points**, and
// the comparison would prove nothing.
const defenderHP = 500

// The greataxe's damage die, as the face count the critical-multiplier assertion compares
// against.
const greataxeFaces = 12

// outcome is one resolved attack, read back off the mutations it produced.
type outcome struct {
	// Met reports that the attack landed, from the roll record rather than from the
	// mutation count: a miss is a complete resolution producing one mutation, and a test
	// inferring "hit" from "two mutations" would be reading a packaging detail.
	Met bool

	// Natural is the attack die's face.
	Natural int

	// Critical is the roll record's flag. Under 2024 and with an attack die that did not
	// show its maximum, this flag **is** "a damage die showed its maximum", because that
	// is the only other thing the critical rule consults — which is why the subtest that
	// needs that die searches for this flag rather than reading the damage dice.
	//
	// **Not read from `rollRecord.Parts`,** which `resolveAttack` leaves nil: the parts
	// breakdown is filled in by `roll` and not by `attack`, so a test reaching for it
	// would find an absent field and conclude a die never showed its maximum. The
	// damage comparison below is the substantive check, and it needs no dice list.
	Critical bool

	// Damage is the hit points the defender lost.
	Damage int
}

// rollRecord is the part of a creature's encoding these tests read.
//
// **A local struct over `json.Unmarshal` into `map[string]any`**, because the names are the
// point: a test reaching into `map[string]any` with the string `"critical"` would be
// asserting on a *spelling*, and this repository's whole argument about encodings is that a
// field a test cannot name is a field nothing checks.
//
// `Parts` and `Advantage` are declared because the encoding has them and a reader comparing
// this struct with `dnd5e`'s would want to see they were considered. **No assertion reads
// them**: `resolveRoll` fills in the breakdown and `resolveAttack` does not, so a field that
// is present in the encoding and empty for the resolution under test is precisely the shape
// of an assumption that would fail quietly.
type rollRecord struct {
	Expr      string `json:"expr"`
	Total     int    `json:"total"`
	Natural   *int   `json:"natural"`
	Parts     []int  `json:"parts"`
	Advantage int    `json:"advantage"`
	Target    int    `json:"target"`
	Met       bool   `json:"met"`
	Critical  bool   `json:"critical"`
}

// creatureBody is the part of a creature's encoding these tests read.
//
//nolint:tagliatelle // `dnd5e`'s creature encoding is snake_case on the wire, and these tags
//nolint:tagliatelle // name the fields of *that* encoding rather than re-spelling them for this
//nolint:tagliatelle // repository's taste.
type creatureBody struct {
	Name     string      `json:"name"`
	HitPoint int         `json:"hp"`
	LastRoll *rollRecord `json:"last_roll"`
}

// greataxeBody is the scenario's attacker: Strength 18, Dexterity 12, no conditions, and a
// greataxe whose damage is a bare `1d12`.
//
// **The bare `1d12` is deliberate.** A critical multiplies the *dice* and not the modifiers,
// so a modifierless expression makes the difference between the editions exactly one die —
// and the assertion can then be "2024 deals exactly twelve more", which no switch that was
// merely set could produce.
//
// `attack_bonus: 12` is a large flat bonus so that a landing attack is common and the seed
// search below is short. It is the attacker's own declared number, not a pack one.
const greataxeBody = `{
  "name": "Vurg",
  "actor": 7,
  "level": 5,
  "ability": {
    "strength": 18, "dexterity": 12, "constitution": 16,
    "intelligence": 8, "wisdom": 10, "charisma": 6
  },
  "hp": 44, "max_hp": 44, "ac": 0, "ac_bonus": 0, "speed": 30,
  "conditions": [],
  "attacks": [
    {
      "name": "greataxe",
      "ability": "strength",
      "damage": "1d12",
      "attack_bonus": 12,
      "mastery": ["great_weapon"]
    }
  ],
  "masteries": ["great_weapon"]
}`

// shortswordBody is the same creature holding a shortsword whose damage is `1d6` and whose
// only granted mastery is `finesse`.
//
// **A second weapon on a *separate* token, not a second attack on the first.** Finesse's
// effect chooses the ability the damage is rolled *with*, so a fixture with both weapons on
// one token could not tell which weapon's rule applied — and the assertion is about which
// ability three points of modifier came from.
const shortswordBody = `{
  "name": "Vurg",
  "actor": 7,
  "level": 5,
  "ability": {
    "strength": 18, "dexterity": 12, "constitution": 16,
    "intelligence": 8, "wisdom": 10, "charisma": 6
  },
  "hp": 44, "max_hp": 44, "ac": 0, "ac_bonus": 0, "speed": 30,
  "conditions": [],
  "attacks": [
    {
      "name": "shortsword",
      "ability": "dexterity",
      "damage": "1d6",
      "attack_bonus": 12,
      "mastery": ["finesse"]
    }
  ],
  "masteries": ["finesse"]
}`

// defenderBody builds a defender, optionally carrying one condition.
//
// **`incapacitated` rather than `unconscious` for the exemption subtest**, and the reason is
// worth recording because it is the fixture's whole job: `unconscious` grants *advantage*
// against attacks made at it, so its attack die is drawn twice and the probability of a
// natural 20 drops from one in twenty to one in four hundred — a seed search four hundred
// times wider for no gain. `incapacitated` is also the condition 2024's rule names and
// `dnd5e`'s prose does not, so it is the one at risk of being left out.
func defenderBody(conditions ...string) string {
	held := "[]"
	if len(conditions) > 0 {
		held = `["` + strings.Join(conditions, `","`) + `"]`
	}

	return fmt.Sprintf(`{
  "name": "Kobold",
  "actor": 0,
  "level": 3,
  "ability": {
    "strength": 7, "dexterity": 15, "constitution": 9,
    "intelligence": 8, "wisdom": 7, "charisma": 8
  },
  "hp": %d, "max_hp": %d, "ac": 0, "ac_bonus": 0, "speed": 30,
  "conditions": %s,
  "attacks": [],
  "masteries": []
}`, defenderHP, defenderHP, held)
}

// findSeed finds a seed whose resolution matches want, and fails if none does.
//
// **A search rather than a hardcoded byte**, for the reason `dnd5e`'s own fixture gives: a
// hardcoded seed is a golden value that changes the moment a pack's numbers change, and a
// test that fails because a proficiency bonus moved is a test about the pack wearing a
// resolver's clothes.
//
// **Deterministic by construction**: the loop is over integers and the generator is
// `rules.Context.Rand`, whose derivation is a pure function of the seed bytes and a label
// (ADR 0044's table). Nothing here reads a clock, so the seed found today is the seed found
// in a race-enabled CI run. Two hundred clears the rarest fixture — a specific die showing
// its maximum, which is a twelfth — by a wide margin.
func findSeed(t *testing.T, what string, resolve func(byte) outcome, want func(outcome) bool) byte {
	t.Helper()

	for seed := byte(1); seed < 201; seed++ {
		if want(resolve(seed)) {
			return seed
		}
	}

	t.Fatalf("no seed in 1..200 produced %s; the fixture cannot reach the resolution this "+
		"test asserts on, so every assertion below would be vacuous", what)

	return 0
}

// resolve resolves one attack and reads both mutations back.
//
// **One mutation on a miss and two on a hit**, so both are accepted and the outcome is taken
// from the roll record. The damage is the fixture's hit points minus the defender's *after*
// the hit, because the mutation carries the defender once the damage is applied.
func resolve(
	t *testing.T,
	engine *dnd5e.Engine,
	seed byte,
	attacker string,
	weapon string,
	held ...string,
) outcome {
	t.Helper()

	state, err := rules.NewState(9, []rules.Object{
		{ID: attackerID, Kind: rules.KindToken, Data: []byte(attacker)},
		{ID: defenderID, Kind: rules.KindToken, Data: []byte(defenderBody(held...))},
	})
	if err != nil {
		t.Fatalf("building the fixture's table: %v", err)
	}

	call, err := rules.NewContext(42, scenarioActor, domain.RoleGM, rules.Seed{seed, seed, seed})
	if err != nil {
		t.Fatalf("building a rule context: %v", err)
	}

	args := []byte(`{"attack":"` + weapon + `","defender":"` + defenderID.String() + `"}`)

	intent, err := rules.NewIntent(dnd5e.OpAttack, attackerID, args)
	if err != nil {
		t.Fatalf("building the intent: %v", err)
	}

	mutations, err := engine.Apply(t.Context(), call, state, intent)
	if err != nil {
		t.Fatalf("seed %d: Apply: %v", seed, err)
	}

	if len(mutations) == 0 {
		t.Fatalf("seed %d: an attack resolved to nothing, so there is no roll record to read",
			seed)
	}

	var rolled creatureBody

	if err := json.Unmarshal(mutations[0].Args, &rolled); err != nil {
		t.Fatalf("seed %d: decoding the attacker's mutation: %v", seed, err)
	}

	if rolled.LastRoll == nil {
		t.Fatalf("seed %d: the attacker's mutation carries no roll record", seed)
	}

	record := rolled.LastRoll

	got := outcome{Met: record.Met, Critical: record.Critical}

	if record.Natural != nil {
		got.Natural = *record.Natural
	}

	if len(mutations) > 1 {
		var hurt creatureBody

		if err := json.Unmarshal(mutations[1].Args, &hurt); err != nil {
			t.Fatalf("seed %d: decoding the defender's mutation: %v", seed, err)
		}

		got.Damage = defenderHP - hurt.HitPoint
	}

	return got
}

// greataxe resolves the greataxe fixture's attack, optionally against a defender carrying
// one condition.
func greataxe(
	t *testing.T,
	engine *dnd5e.Engine,
	seed byte,
	held ...string,
) outcome {
	t.Helper()

	return resolve(t, engine, seed, greataxeBody, "greataxe", held...)
}

// shortsword resolves the shortsword fixture's attack, which is a different weapon on a
// different token.
func shortsword(t *testing.T, engine *dnd5e.Engine, seed byte) outcome {
	t.Helper()

	return resolve(t, engine, seed, shortswordBody, "shortsword")
}

// TestTheEditionsResolveDifferentlyUnderOneSeed is the strongest form of the "they differ"
// requirement: the differences are not merely **declared**, they are **effective**, and this
// is the only test here that observes a resolution.
//
// Three claims, one per behavioural difference, each with its own seed search:
//
//  1. A damage die at its maximum is a critical hit in 2024 and an ordinary hit in 2014.
//  2. A finesse weapon uses the better of two abilities in 2024 and the weapon's own in
//     2014 — the same die and a different modifier, so the difference is a **number** rather
//     than a flag.
//  3. A natural 20 against an incapable creature is a critical hit in 2014 and is not in
//     2024.
//
// **One seed per claim, found by searching, and the same seed drives both editions.** The
// draw labels are built from the operation, the target, the weapon and the damage
// expression — never from the pack — so a seed produces the same dice under both editions
// and the *only* thing that can differ is the policy. That is what makes this a test of the
// overlays rather than of the dice: if a label ever read the pack, the two editions would
// draw differently and this test would fail on its fixtures rather than on the rules. It
// also matches §16.3's roll log, where a stored mutation has to replay — which is
// unbuildable if the dice depend on which edition is installed.
func TestTheEditionsResolveDifferentlyUnderOneSeed(t *testing.T) {
	t.Parallel()

	old := editionNamed(t, overlays.Dnd5e2014).engine
	newer := editionNamed(t, overlays.Dnd5e2024).engine

	t.Run("a damage die at its maximum crits in 2024 only", func(t *testing.T) {
		t.Parallel()

		// The predicate is "2024 crits and the attack die did not", which under 2024 is
		// exactly "a damage die showed its maximum" — the critical rule consults nothing
		// else. So the seed is found from the flag rather than from a dice list the
		// attack's roll record does not carry, and the damage comparison below is what
		// proves the die was a d12 at its maximum.
		seed := findSeed(
			t,
			"a landing greataxe attack that crits under 2024 with an attack die that did "+
				"not show its maximum",
			func(candidate byte) outcome { return greataxe(t, newer, candidate) },
			func(got outcome) bool {
				return got.Met && got.Natural != twenty && got.Critical
			},
		)

		before := greataxe(t, old, seed)
		after := greataxe(t, newer, seed)

		if before.Critical {
			t.Errorf("seed %d crits under 2014, where a damage die at its maximum is an "+
				"ordinary hit", seed)
		}

		if !after.Critical {
			t.Errorf("seed %d does not crit under 2024, where a damage die at its maximum is "+
				"a critical hit", seed)
		}

		// The **dice** are multiplied and the modifiers are not, so the difference is
		// exactly the value of the die that showed its maximum — and that value is
		// asserted to be the whole of a d12. This is what makes the claim about the rule
		// rather than about a flag having been set: a `true` in the YAML cannot produce
		// a damage number twelve larger.
		if want := before.Damage + greataxeFaces; after.Damage != want {
			t.Errorf("seed %d deals %d under 2014 and %d under 2024; a critical doubles the "+
				"damage dice and nothing else, so 2024's is exactly %d more",
				seed, before.Damage, after.Damage, greataxeFaces)
		}
	})

	t.Run("a finesse weapon uses the better ability in 2024 only", func(t *testing.T) {
		t.Parallel()

		seed := findSeed(
			t,
			"a landing shortsword attack",
			func(candidate byte) outcome { return shortsword(t, newer, candidate) },
			func(got outcome) bool { return got.Met },
		)

		before := shortsword(t, old, seed)
		after := shortsword(t, newer, seed)

		// The fixture's attacker has Strength 18 (+4) and Dexterity 12 (+1), so the
		// whole difference is three points of modifier on the same die. Asserted as a
		// number, because a number cannot be produced by a switch that was merely set.
		const modifierGap = 3

		if after.Damage-before.Damage != modifierGap {
			t.Errorf("seed %d deals %d under 2014 and %d under 2024; the fixture's better "+
				"ability is worth +%d and 2024's finesse mastery is what applies it",
				seed, before.Damage, after.Damage, modifierGap)
		}
	})

	t.Run("a natural 20 against an incapable creature crits in 2014 only", func(t *testing.T) {
		t.Parallel()

		seed := findSeed(
			t,
			"a natural 20 against an incapacitated defender",
			func(candidate byte) outcome { return greataxe(t, newer, candidate, "incapacitated") },
			func(got outcome) bool { return got.Natural == twenty && got.Met },
		)

		before := greataxe(t, old, seed, "incapacitated")
		after := greataxe(t, newer, seed, "incapacitated")

		if !before.Critical {
			t.Errorf("seed %d does not crit under 2014, where nothing exempts a helpless "+
				"target from the critical rule", seed)
		}

		if after.Critical {
			t.Errorf("seed %d crits under 2024, where an attack against an incapacitated "+
				"creature does not automatically count as a critical hit", seed)
		}
	})
}

// # The ruleset fingerprint

// The conformance seams.
//
// `conformance.Config` needs five facts this package does not own: a system, a scenario, the
// GM-only operations, an error-to-wire-word map and a ruleset decision. The first three come
// from `dnd5e` and the scenario below; the last two are **this deployment's**, which is the
// entire reason they are seams. The encoding mirrors `internal/realtime`'s — `sp1:` and four
// keyed components in order — because a fixture the product's own parser would reject would
// make the version audit a test of this file's string handling rather than of the editions'
// semantics.

// The encoding's format marker and its component keys, in the persisted order.
const fingerprintPrefix = "sp1:"

// componentKeys is `realtime`'s list.
var componentKeys = []string{"system", "ruleset", "base", "overlay"}

// The two refusals, kept apart: a column this build cannot parse is a migration problem and
// a genuine mismatch is a GM's decision, and one sentinel for both would tell a GM to discard
// a game over a string.
var (
	errDrift      = errors.New("realtime: persisted state was written under a different ruleset")
	errUnreadable = errors.New("realtime: persisted ruleset_version is not comparable")
)

// deployment is the ruleset seam.
type deployment struct{}

// Fingerprint is what a campaign opened now would be written under.
//
// **`house` is read for nothing**, and that is ADR 0018 as an assertion rather than as a
// comment: the suite toggles a house rule and requires the two fingerprints to be
// byte-identical, so a future edit that started reading `house` fails a test rather than
// stranding a GM's campaign.
//
// The two pack components come from the engine's optional second interface, with an **empty
// fallback rather than a refusal**: `realtime.checkComponent` exempts exactly the overlay
// component, so a system that volunteers no pack versions still contributes a fingerprint a
// deployment can compare.
func (deployment) Fingerprint(system rules.System, _ []conformance.HouseRule) string {
	values := []string{system.ID().String(), system.RulesetVersion(), "", ""}

	if versioned, ok := system.(interface {
		Versions() dnd5e.Versions
	}); ok {
		versions := versioned.Versions()
		values[2] = versions.BasePack
		values[3] = versions.OverlayPack
	}

	parts := make([]string, 0, len(componentKeys))
	for index, key := range componentKeys {
		parts = append(parts, key+"="+values[index])
	}

	return fingerprintPrefix + strings.Join(parts, ";")
}

// Check answers whether a campaign written under persisted may resume.
func (d deployment) Check(
	system rules.System,
	house []conformance.HouseRule,
	persisted string,
) conformance.Verdict {
	expected := d.Fingerprint(system, house)
	verdict := conformance.Verdict{Persisted: persisted, Expected: expected}

	if persisted == expected {
		return verdict
	}

	if !parsable(persisted) {
		verdict.Err = fmt.Errorf(
			"%w: %q is not a fingerprint this build wrote; it was written by some other build",
			errUnreadable, persisted,
		)

		return verdict
	}

	verdict.Err = fmt.Errorf(
		"%w: the state was written under %q and this build resolves %q",
		errDrift, persisted, expected,
	)

	return verdict
}

// parsable reports whether a value decodes into this encoding's four keyed components, in
// this encoding's order.
//
// **Key order is checked and not merely the key count**, for the reason `realtime`'s own
// parser checks it: the two lists are positionally paired and a row in any other order is a
// row this build did not write.
func parsable(encoded string) bool {
	body, found := strings.CutPrefix(encoded, fingerprintPrefix)
	if !found {
		return false
	}

	components := strings.Split(body, ";")
	if len(components) != len(componentKeys) {
		return false
	}

	for index, component := range components {
		key, _, split := strings.Cut(component, "=")
		if !split || key != componentKeys[index] {
			return false
		}
	}

	return true
}

var _ conformance.Resume = deployment{}

// classify is the composition root's error-to-wire-word map: a switch on `dnd5e`'s own
// sentinels and nothing else.
//
// **A `switch` and no string matching**, because the composition root is the only place
// holding both a `rules.System` and a `realtime.RejectReason`, and letting a system's own
// sentence reach a browser is how whatever a plugin choked on ends up in a page. The default
// is `server_error`, which is §10.7's "the one reason that says nothing about what failed".
func classify(refusal error) string {
	switch {
	case errors.Is(refusal, dnd5e.ErrGMOnly), errors.Is(refusal, dnd5e.ErrNotYourCreature):
		return conformance.ReasonNotPermitted
	case errors.Is(refusal, dnd5e.ErrUnknownOp):
		return conformance.ReasonUnknownOp
	case errors.Is(refusal, dnd5e.ErrNoSuchCreature),
		errors.Is(refusal, dnd5e.ErrMalformedCreature):
		return conformance.ReasonNoSuchPlacement
	case errors.Is(refusal, dnd5e.ErrBadArguments),
		errors.Is(refusal, dnd5e.ErrRoll),
		errors.Is(refusal, dnd5e.ErrNoSuchAttack),
		errors.Is(refusal, dnd5e.ErrNoSuchCondition),
		errors.Is(refusal, dnd5e.ErrNoSuchAbility),
		errors.Is(refusal, dnd5e.ErrNoSuchMastery):
		return conformance.ReasonInvalidArgs
	default:
		return conformance.ReasonServerError
	}
}

// suiteConfig builds a complete `conformance.Config` for one edition.
//
// **One attack against one defender**, because the determinism audit needs a resolution that
// *changes something*: an intent that resolves to nothing is reproducible whether or not the
// resolver is deterministic, and `conformance.New` refuses to certify one.
//
// The attacker's token is **placed for the scenario's actor**, which is what makes the
// scenario playable as a player: the role audit resolves this very scenario as a player and
// requires it not to be refused, and a token belonging to somebody else would be refused for
// ownership — correctly, and for a reason that has nothing to do with the audit.
func suiteConfig(t *testing.T, fixture *built) conformance.Config {
	t.Helper()

	args := []byte(`{"attack":"greataxe","defender":"` + defenderID.String() + `"}`)

	intent, err := rules.NewIntent(dnd5e.OpAttack, attackerID, args)
	if err != nil {
		t.Fatalf("building the scenario's intent: %v", err)
	}

	call, err := rules.NewContext(42, scenarioActor, domain.RoleGM, rules.Seed{3, 3, 3})
	if err != nil {
		t.Fatalf("building the scenario's rule context: %v", err)
	}

	return conformance.Config{
		System: fixture.engine,
		Scenario: conformance.Scenario{
			Objects: []rules.Object{
				{ID: attackerID, Kind: rules.KindToken, Data: []byte(greataxeBody)},
				{ID: defenderID, Kind: rules.KindToken, Data: []byte(defenderBody())},
			},
			Intent: intent,
			Call:   call,
		},
		GMOnly:   dnd5e.GMOnlyOps(),
		Classify: classify,
		Resume:   deployment{},
	}
}

// TestBothEditionsPassEveryConformanceAudit is phase 8's own requirement, and it is one call
// per edition rather than one call for both — because a conformance failure under one edition
// and not the other is the exact shape of a data difference the engine cannot honour.
//
// **All five audits through `conformance.Check`**, the entry point `conformance.New`'s
// refusals cannot be skipped from: there is no way to run a suite without passing through
// `New`, so a suite missing a seam cannot be run at all rather than run and found nothing.
func TestBothEditionsPassEveryConformanceAudit(t *testing.T) {
	t.Parallel()

	for _, fixture := range everyEdition(t) {
		t.Run(string(fixture.id), func(t *testing.T) {
			t.Parallel()

			if err := conformance.Check(t.Context(), suiteConfig(t, fixture)); err != nil {
				t.Fatalf("the %s edition does not conform: %v", fixture.id, err)
			}
		})
	}
}

// TestTheSuiteStillHasTheFiveAuditsItDocuments guards the guard.
//
// `conformance.Audits()` has exactly five entries, and a sixth would mean the contract this
// package is certified against is not the one its documentation claims. One assertion
// because it is one fact, and it belongs next to the certification rather than in the
// suite's own package.
func TestTheSuiteStillHasTheFiveAuditsItDocuments(t *testing.T) {
	t.Parallel()

	if got := len(conformance.Audits()); got != 5 {
		t.Fatalf("the suite runs %d audits and §14 names five", got)
	}
}

// TestTheFingerprintCarriesTheEditionAndNotAHouseRule is the fingerprint requirement, and it
// is the test ADR 0018 would want written if nobody had written it for an edition.
//
// Five claims, each of which can fail:
//
//  1. **The ruleset component is the engine's constant**, under both editions and under the
//     base pack. An edition changes which pack applies and not what the resolver means, so
//     folding an edition's identity in here would strand a campaign twice for one change.
//  2. **The overlay's version is a component.** Each edition fingerprints with its own
//     version, and the three systems fingerprint differently, so a campaign that changed
//     edition is refused.
//  3. **The base pack's component is the base pack's**, under both editions. `dnd5e` captures
//     that before the merge precisely so an overlay's version does not land in two components
//     at once, and a base-pack revision would otherwise gate nothing.
//  4. **A house rule moves nothing** — the same module, enabled and disabled, byte-identical
//     fingerprints, and the campaign still resumes. This is ADR 0018's exclusion as bytes
//     rather than as a comment, and the fault it prevents — refusing to resume every campaign
//     whose GM enabled a rule at the table — reads as a *feature*, because the message would
//     be perfectly reasonable on its face.
//  5. **A campaign that changed edition is refused, distinguishably.** One sentinel for drift
//     and for an unreadable column would tell a GM to discard a game over a string.
//
// **Claim 4 is the one an overlay could plausibly break.** A fingerprint that hashed an
// edition's *contents* — of the merged pack rather than of its declared version — would still
// satisfy claims 1 to 3 and would strand a campaign for a house rule applied on top of it.
func TestTheFingerprintCarriesTheEditionAndNotAHouseRule(t *testing.T) {
	t.Parallel()

	base := &built{id: "base", engine: baseSystem(t)}
	fourteen := editionNamed(t, overlays.Dnd5e2014)
	twentyFour := editionNamed(t, overlays.Dnd5e2024)
	all := []*built{fourteen, twentyFour, base}

	// One seam value, so the five claims below are all the same deployment's answers rather
	// than five fresh ones that agree by construction.
	probe := deployment{}

	// Claim 1.
	for _, fixture := range all {
		if got := fixture.engine.RulesetVersion(); got != dnd5e.EngineVersion {
			t.Errorf("the %s system's ruleset component is %q, want the engine constant %q; "+
				"an edition changes which pack applies and not what the resolver means",
				fixture.id, got, dnd5e.EngineVersion)
		}
	}

	// Claims 2 and 3.
	seen := make(map[string]bool, len(all))

	for _, fixture := range all {
		fingerprint := probe.Fingerprint(fixture.engine, nil)

		if seen[fingerprint] {
			t.Errorf("the %s system fingerprints as %q, which another system already "+
				"fingerprints as; §10.8 gates a resume on this string", fixture.id, fingerprint)
		}

		seen[fingerprint] = true

		if fixture.edition.Version() == "" {
			continue
		}

		if !strings.Contains(fingerprint, fixture.edition.Version()) {
			t.Errorf("the %s edition fingerprints as %q and does not carry its own version "+
				"%q; a pack revision that gates nothing gates nothing",
				fixture.id, fingerprint, fixture.edition.Version())
		}
	}

	shared := base.engine.PackVersion()

	for _, fixture := range []*built{fourteen, twentyFour} {
		versions := fixture.engine.Versions()

		if versions.BasePack != shared {
			t.Errorf("the %s edition reports the base pack's component as %q and the base "+
				"pack is %q; `dnd5e` captures that before the merge, and an overlay's version "+
				"must never land in two components at once",
				fixture.id, versions.BasePack, shared)
		}

		if versions.OverlayPack == shared {
			t.Errorf("the %s edition's overlay component holds the base pack's version",
				fixture.id)
		}

		if versions.OverlayPack != fixture.edition.Version() {
			t.Errorf("the %s edition reports the overlay component as %q and its own version "+
				"is %q", fixture.id, versions.OverlayPack, fixture.edition.Version())
		}
	}

	// Claim 4.
	off := []conformance.HouseRule{{ModuleID: "overlay-probe", Position: 0, Enabled: false}}
	on := []conformance.HouseRule{{ModuleID: "overlay-probe", Position: 0, Enabled: true}}

	for _, fixture := range all {
		mine := probe.Fingerprint(fixture.engine, off)
		moved := probe.Fingerprint(fixture.engine, on)

		if moved != mine {
			t.Errorf("the %s system fingerprints as %q with a house rule off and %q with it "+
				"on; ADR 0018 requires the fingerprint to name resolution semantics and not "+
				"house-rule configuration, or a GM enabling a rule is refused on their own "+
				"campaign", fixture.id, mine, moved)
		}

		if verdict := probe.Check(fixture.engine, on, mine); verdict.Err != nil {
			t.Errorf("the %s system refused to resume a campaign this deployment wrote "+
				"itself, once a house rule was enabled: %v", fixture.id, verdict.Err)
		}
	}

	// Claim 5: each edition refuses the other's campaigns.
	for _, pair := range []struct {
		written, resumed *built
	}{
		{written: fourteen, resumed: twentyFour},
		{written: twentyFour, resumed: fourteen},
	} {
		verdict := probe.Check(
			pair.resumed.engine, off, probe.Fingerprint(pair.written.engine, off),
		)

		if verdict.Err == nil {
			t.Errorf("a campaign written under %s was permitted to resume under %s",
				pair.written.id, pair.resumed.id)
		}

		if !errors.Is(verdict.Err, errDrift) {
			t.Errorf("refusing %s for %s gave %v, want %v; an unreadable column and a genuine "+
				"mismatch are different failures with different fixes",
				pair.written.id, pair.resumed.id, verdict.Err, errDrift)
		}
	}

	unreadable := probe.Check(fourteen.engine, off, "not a fingerprint this build wrote")
	if !errors.Is(unreadable.Err, errUnreadable) {
		t.Errorf("an unreadable ruleset_version gave %v, want %v", unreadable.Err, errUnreadable)
	}
}

// # The two refusals

// TestAnUnknownEditionIsRefused holds the first of this package's two refusals, and it is a
// separate test because the two have **different fixes**: a typo in a configuration value and
// a `var` nobody filled in are not one sentence to a reader.
func TestAnUnknownEditionIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := overlays.ByID("2025"); !errors.Is(err, overlays.ErrUnknownEdition) {
		t.Errorf("asking for the 2025 edition: want %v, got %v", overlays.ErrUnknownEdition, err)
	}

	if _, err := overlays.Source("2025"); !errors.Is(err, overlays.ErrUnknownEdition) {
		t.Errorf("reading the 2025 edition's source: want %v, got %v",
			overlays.ErrUnknownEdition, err)
	}

	// The empty id is refused rather than treated as the base pack.
	if _, err := overlays.ByID(""); !errors.Is(err, overlays.ErrUnknownEdition) {
		t.Errorf("asking for the empty edition: want %v, got %v", overlays.ErrUnknownEdition, err)
	}
}

// TestTheZeroEditionRefusesRatherThanResolvingUnderTheBasePack is the refusal that prevents
// the quietest wrong answer in this package.
//
// `dnd5e.Overlay`'s `Zero` predicate is deliberate and correct: an empty overlay *is* the
// base pack alone, and §10.4 makes that a first-class system. But a zero `overlays.Edition` is
// not a request for the base pack — it is a value nobody filled in, and building the base pack
// from it would produce a campaign whose fingerprint has an empty `overlay` component and **no
// way to tell that is what happened**. A GM who asked for 2024 and got 2014's rules would
// have nothing to read.
func TestTheZeroEditionRefusesRatherThanResolvingUnderTheBasePack(t *testing.T) {
	t.Parallel()

	var absent overlays.Edition

	if absent.ID() != "" || absent.Name() != "" || absent.Version() != "" {
		t.Errorf("the zero Edition reports %q, %q and %q; it names nothing",
			absent.ID(), absent.Name(), absent.Version())
	}

	if _, err := absent.System(); !errors.Is(err, overlays.ErrNoEdition) {
		t.Fatalf("building the zero edition: want %v, got %v", overlays.ErrNoEdition, err)
	}
}

// # What this package is

// TestThisPackageImportsNothingButTheEngine is the layering rule, read from this package's own
// source rather than asserted in a paragraph.
//
// **`go/ast` and not a grep**, for the reason `dnd5e`'s own import audit gives: a grep is a
// pattern somebody has to remember to extend, and a package that reached for
// `internal/realtime` would not create a cycle here — it would *compile*, and the cost would
// be an edition pack with a handle to the hub. Nothing in the type system stops the second.
//
// `internal/domain` and `internal/domain/rules` are permitted because `dnd5e` is the only
// thing this package has anything to say *to*: it is data for the 5e engine and its contract,
// and a third-party dependency would be a second answer to a question the engine answers.
func TestThisPackageImportsNothingButTheEngine(t *testing.T) {
	t.Parallel()

	permitted := []string{
		"github.com/semiplane/semiplane/internal/domain",
		"github.com/semiplane/semiplane/internal/domain/rules",
		"github.com/semiplane/semiplane/internal/domain/systems/dnd5e",
	}

	files := ruleFiles(t)

	if len(files) == 0 {
		t.Fatal("this package has no rule files, so the audit below would be reading nothing")
	}

	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}

		for _, imported := range parsed.Imports {
			path := strings.Trim(imported.Path.Value, "\"`")

			if !strings.HasPrefix(path, "github.com/semiplane/") {
				continue
			}

			if slices.Contains(permitted, path) {
				continue
			}

			t.Errorf("%s imports %q; this package is data for the 5e engine and reaches for "+
				"nothing else of semiplane's, because the dependency runs inward", file, path)
		}
	}
}

// TestThisPackageCarriesNoInit is S-10.1, and this package has no state for one to hide.
//
// Parsed rather than grepped, for the same reason `dnd5e`'s own test is: `func init()` and a
// bare `init` reference parse the same way to `go/ast` and none of them are grep-visible with
// a pattern somebody remembered.
func TestThisPackageCarriesNoInit(t *testing.T) {
	t.Parallel()

	for _, file := range ruleFiles(t) {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}

		ast.Inspect(parsed, func(node ast.Node) bool {
			declaration, isFunction := node.(*ast.FuncDecl)
			if isFunction && declaration.Recv == nil && declaration.Name.Name == "init" {
				t.Errorf("%s declares an init(); which editions a deployment ships is a choice "+
					"made at the composition root, in the place every other choice is made", file)
			}

			return true
		})
	}
}

// TestTheEmbeddedFileNamesAndItsDeclaredVersionAgree is the half of identity a caller cannot
// see.
//
// The file is called `dnd5e-2024.yaml` and its `version:` says `dnd5e-2024-overlay@1`. Two
// names for one thing, and a file called 2024 that fingerprints as the 2014 overlay is a
// campaign that resumed under the wrong rules and reported nothing — the failure this
// package's whole existence is about. The version is read from `Edition.Version()`, which is
// what the fingerprint carries, rather than by parsing the YAML a second time.
//
// The second half is that `Source` hands out a **copy**: a caller that appended to
// `go:embed`'s slice would corrupt every edition not yet parsed, and a rules pack corrupted
// in memory is a campaign whose fingerprint says one thing and whose resolver does another.
func TestTheEmbeddedFileNamesAndItsDeclaredVersionAgree(t *testing.T) {
	t.Parallel()

	versions := make(map[string]bool, len(overlays.IDs()))

	for _, id := range overlays.IDs() {
		edition, err := overlays.ByID(id)
		if err != nil {
			t.Fatalf("%s: parsing the overlay: %v", id, err)
		}

		version := edition.Version()
		if version == "" {
			t.Fatalf("the %s edition declares no version, so a campaign's resume cannot be "+
				"compared", id)
		}

		for _, fragment := range []string{string(id), "@"} {
			if !strings.Contains(version, fragment) {
				t.Errorf("the %s edition's version is %q and carries no %q; a GM who changed "+
					"nothing and a GM who changed a row both wrote %q, and only one of them "+
					"should be able to resume", id, version, fragment, string(id))
			}
		}

		if versions[version] {
			t.Errorf("the %s edition's version is %q, which another edition also reports; "+
				"§10.8 cannot tell them apart", id, version)
		}

		versions[version] = true

		source, err := overlays.Source(id)
		if err != nil {
			t.Fatalf("reading the %s edition's source: %v", id, err)
		}

		if len(source) == 0 {
			t.Fatalf("the %s edition's source is empty, so there is no file to have been parsed",
				id)
		}

		// A byte the file cannot start with, so writing it is a **detectable** change. The
		// first byte of either overlay is `#`, and writing `#` would leave the value
		// identical and the assertion vacuous.
		const replacement = 'Z'

		source[0] = replacement

		again, err := overlays.Source(id)
		if err != nil {
			t.Fatalf("reading the %s edition's source again: %v", id, err)
		}

		if again[0] == replacement {
			t.Errorf("writing to the value Source returned for %s changed it", id)
		}
	}
}

// ruleFiles returns this package's own rule files — the same definition `determinism.Audit`
// uses, so the two audits see the same files, and the paths reported are ones a reader can
// open.
func ruleFiles(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("listing this package's files: %v", err)
	}

	files := make([]string, 0, len(entries))

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		files = append(files, name)
	}

	slices.Sort(files)

	return files
}
