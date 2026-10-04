package demo_test

// The gate's integrity: the claims this package makes about *itself*.
//
// Each of these exists because the corresponding mistake is one this repository has
// made before. AGENTS.md records the pattern twice — a package listed in a gate that
// holds no test the pattern matches, and a sheet whose test ran under `make check` but
// not under the target whose whole job was the claims about it — and the half neither
// guard can see is a test that exists and is not named. So:
//
//   - the Makefile's pattern is checked against this package's own AST, so a new gate
//     test that nobody added to the pattern fails the build rather than running only
//     under `make check`;
//   - the requirement lists are checked against the registry, the extension table and
//     the scanner, so a hardcoded list fails rather than drifts;
//   - the shell target is checked to exist and to name this package, because a target
//     that quietly stopped running the gate is the same silent pass wearing a
//     different hat.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/content/ext"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e/overlays"
	"github.com/semiplane/semiplane/internal/plugin"
)

// demoMakefile is the repository Makefile, which owns the gate's pattern and its
// target. Relative to this package, like every other path a test here takes.
const demoMakefile = "../../Makefile"

// demoShellTarget is the script the Makefile's `demo-check` recipe runs.
const demoShellTarget = "../../scripts/check-demo.sh"

// demoGatePrefix is the naming convention every gate test follows.
//
// A prefix rather than a hand-maintained list, because the list is the thing this
// repository has had to remember to update and has forgotten; a prefix means a new
// gate test cannot be written without being covered by the check below, because the
// check looks for the prefix rather than for a list of names.
const demoGatePrefix = "TestDemo"

// demoParseMakefile returns the value of a `NAME := …` assignment.
func demoParseMakefile(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile(demoMakefile)
	if err != nil {
		t.Fatalf("read the Makefile: %v", err)
	}

	for line := range strings.SplitSeq(string(raw), "\n") {
		rest, found := strings.CutPrefix(strings.TrimSpace(line), name+" :=")
		if !found {
			continue
		}

		return strings.TrimSpace(rest)
	}

	t.Fatalf("the Makefile defines no `%s :=` assignment", name)

	return ""
}

// demoPackageTests returns every top-level test function this package declares, by
// name, sorted.
//
// **Through `go/parser` and not `go test -list`.** `go test -list` compiles the package
// and runs its `TestMain`, and this check has to hold for a package that does not
// compile — a compile failure is exactly when a reader most needs the Makefile told to
// them — and a raw read would also pick up the words `TestDemo` out of comments, which
// is how a gate test that exists only in prose gets counted.
func demoPackageTests(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}

	fset := token.NewFileSet()

	var names []string

	for _, entry := range entries {
		name := entry.Name()

		if entry.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}

			names = append(names, fn.Name.Name)
		}
	}

	slices.Sort(names)

	return names
}

// TestDemoEveryGateTestIsSelectedByTheMakefileGate closes the half of the guard that
// counts a package's *contribution* but cannot tell whether the contribution is the test
// the pattern meant.
//
// AGENTS.md records the failure three times: the wiki route held 23 tests and matched
// none, the assets route held 49, and three work items independently shipped
// `TestTheSheetIsInsideTheBuild` while `A11Y_TESTS` matched none of them — so every
// built-artefact assertion ran under `make check` and not under `make a11y`, the target
// whose entire job was those claims. The shell guard cannot see it either; this test
// can, because it reads the package's AST and requires every gate test to be **selected
// by the pattern**.
//
// **Matched the way `go test -run` matches**, as an unanchored regexp over a whole test
// name — which is what lets `DEMO_TESTS` stay a readable list of distinctive substrings
// rather than twenty-eight full names. The first version of this check required the full
// name as a substring of the pattern, and it rejected every one of them: the pattern is
// `RemovingAKind|UndeclaredBrokenLink|…`, and `TestDemoRemovingAKind…` is not a substring
// of that. A check that only its own author can satisfy is a check that gets deleted, so
// the model follows the tool. `go test -run` splits its pattern on `/` to address
// subtests; no pattern element here contains one and no gate test is a subtest, so that
// half does not apply.
func TestDemoEveryGateTestIsSelectedByTheMakefileGate(t *testing.T) {
	t.Parallel()

	pattern := demoParseMakefile(t, "DEMO_TESTS")
	if pattern == "" {
		t.Fatal("DEMO_TESTS is empty, so `make demo-check` would run `go test -run ''` " +
			"and select nothing while exiting 0")
	}

	selector, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("DEMO_TESTS is not a valid -run pattern, so `go test` would refuse it "+
			"rather than select nothing: %v", err)
	}

	var unselected []string

	found := 0

	for _, name := range demoPackageTests(t) {
		if !strings.HasPrefix(name, demoGatePrefix) {
			continue
		}

		found++

		if selector.MatchString(name) {
			continue
		}

		unselected = append(unselected, name)
	}

	if found == 0 {
		t.Fatal("this package declares no gate test, so the pattern names nothing and " +
			"`make demo-check` would pass while checking nothing")
	}

	if len(unselected) > 0 {
		t.Fatalf(
			"these gate tests are not selected by DEMO_TESTS, so `make demo-check` "+
				"would not run them: %s\n\nA gate test that exists and is not in the "+
				"pattern is the half of the guard the guard cannot see; add a "+
				"distinctive alternative to DEMO_TESTS in the Makefile.",
			strings.Join(unselected, ", "),
		)
	}
}

// TestDemoTheGateTargetRunsThisPackagesTests holds that `make demo-check` is wired to
// this package at all.
//
// A target that exists and runs nothing is a green gate with no rules in it, and the
// Makefile is the only place that can say so. The recipe is checked rather than the
// target's mere existence because a target whose recipe was truncated by an edit keeps
// its name.
func TestDemoTheGateTargetRunsThisPackagesTests(t *testing.T) {
	t.Parallel()

	if _, err := os.Stat(demoShellTarget); err != nil {
		t.Fatalf("the gate script is missing (%s): %v", demoShellTarget, err)
	}

	raw, err := os.ReadFile(demoMakefile)
	if err != nil {
		t.Fatalf("read the Makefile: %v", err)
	}

	makefile := string(raw)

	if !strings.Contains(makefile, "check-demo.sh") {
		t.Fatalf("the Makefile never mentions %s, so `make demo-check` runs something "+
			"else — or nothing", demoShellTarget)
	}

	if !strings.Contains(makefile, "$(DEMO_PKG)") &&
		!strings.Contains(makefile, "./internal/demo") {
		t.Fatal("the Makefile's demo-check does not name internal/demo, so the gate's " +
			"own package is not what it runs")
	}

	// The fixture must be reachable from the repository root, because the script runs
	// with the Makefile's working directory rather than the test's.
	if _, err := os.Stat(
		filepath.Join("..", "..", "internal", "demo", demoFixtureRoot),
	); err != nil {
		t.Fatalf("the committed fixture is not where the tests expect it: %v", err)
	}

	// The override the script documents has to be the override the tests honour, or a
	// reviewer who points the gate at a vault under construction is told the gate
	// looked at something it never opened. It did not, once: the test hardcoded
	// `../../demo-vault` and a broken vault passed.
	if override := os.Getenv(demoVaultDirEnv); override != "" &&
		demoVaultUnderTest() == demoShippedVault {
		t.Fatalf(
			"%s=%q was set but the shipped-vault test would still audit %s, so the "+
				"override changes the script's banner and nothing else",
			demoVaultDirEnv, override, demoShippedVault,
		)
	}
}

// TestDemoTheKindsTheGateDemandsAreTheKindsEveryShippedEditionRegisters holds the claim
// the gate's kind rule rests on.
//
// The requirement list is a union over every edition the build ships, because
// `cmd/server/systems.go` is `package main` and its `defaultEdition` cannot be imported
// — a union is the only derivation available from outside it. A union is only the right
// answer while **every** edition registers the same set, which is a property of the data
// packs rather than of this package. If an overlay ever adds a kind to one edition and
// not the other, the gate would demand a demonstration that a build of the other edition
// could not render — so this test fails then, by name.
func TestDemoTheKindsTheGateDemandsAreTheKindsEveryShippedEditionRegisters(t *testing.T) {
	t.Parallel()

	ids := overlays.IDs()
	if len(ids) == 0 {
		t.Fatal("this build ships no edition, so there is nothing to hold a union to")
	}

	demanded := make(map[string]struct{})
	for _, kind := range demoKindsForBuild(t).order {
		demanded[kind] = struct{}{}
	}

	for _, id := range ids {
		edition, err := overlays.ByID(id)
		if err != nil {
			t.Fatalf("read edition %s: %v", id, err)
		}

		system, err := edition.System()
		if err != nil {
			t.Fatalf("build edition %s: %v", id, err)
		}

		registry := plugin.New()
		if err = registry.Register(plugin.Entry{
			System: system,
			Codec:  plugin.PlacementCodec{},
		}); err != nil {
			t.Fatalf("register edition %s: %v", id, err)
		}

		for _, kind := range registry.Kinds() {
			if _, demanded := demanded[kind.String()]; demanded {
				continue
			}

			t.Fatalf(
				"edition %s registers kind %q and the gate does not demand it, so the "+
					"union the gate takes over editions is no longer the set one edition "+
					"registers. Either make the union the per-edition set, or say here "+
					"that a build of %s cannot render the kind the demo would have to show",
				id, kind, id,
			)
		}
	}
}

// TestDemoEverySecretStateTheScannerCanReportIsDemstrated holds that "both states" is
// the whole set of states rather than a pair this package picked.
//
// The check is on the **marker byte**: a third byte produces no callout at all, and a
// marker with no byte produces no callout either. So the two `content.SecretState` values
// are exhaustive over what `content.ScanSecrets` can report, and requiring one of each is
// requiring all of them. If the grammar ever grows a third state this test fails — which
// is the moment a new state needs demonstrating and this package would otherwise be
// quietly satisfied by two.
func TestDemoEverySecretStateTheScannerCanReportIsDemstrated(t *testing.T) {
	t.Parallel()

	states := demoSecretStates()
	if len(states) != 2 {
		t.Fatalf("this gate requires %d secret states, want 2: %v", len(states), states)
	}

	// A byte that is neither `-` nor `+`, and a marker with no byte at all.
	for _, header := range []string{
		"> [!" + demoSecretCalloutName() + "]~\n> a body\n",
		"> [!" + demoSecretCalloutName() + "]\n> a body\n",
		"> [!" + demoSecretCalloutName() + "] \n> a body\n",
	} {
		if found := content.ScanSecrets(header); len(found) != 0 {
			t.Fatalf("the scanner reports %d callout(s) for %q, so the grammar has a "+
				"state beyond the %d this gate demands: %v",
				len(found), header, len(states), found)
		}
	}

	// And the two it does demand are reachable, or the rule would be demanding
	// nothing for the same reason.
	for _, state := range states {
		source := "> [!" + demoSecretCalloutName() + "]" + state.String() + " title\n> a body\n"

		found := content.ScanSecrets(source)
		if len(found) != 1 || found[0].State != state {
			t.Fatalf("the scanner does not report a %q callout for %q: %v",
				state, source, found)
		}
	}
}

// TestDemoTheRedactionMarkerIsTheProductsOwn holds the one place this package
// duplicates the product's vocabulary.
//
// `content`'s callout prefix is unexported, so `demoSecretCalloutName` spells it here —
// and a spelling that drifted would produce a redaction sentence naming a marker the
// product does not use, which is a small lie in the one string a finding prints when it
// is refusing to print anything else. The check writes a callout with **this** name and
// requires the scanner to find it.
func TestDemoTheRedactionMarkerIsTheProductsOwn(t *testing.T) {
	t.Parallel()

	name := demoSecretCalloutName()

	source := "> [!" + name + "]- the only one\n> a body\n"

	found := content.ScanSecrets(source)
	if len(found) != 1 {
		t.Fatalf(
			"the scanner reports %d callouts for %q, so the marker this package spells "+
				"and the marker the product reads are different strings, and the "+
				"redaction sentence would name a marker that does not exist",
			len(found), source,
		)
	}

	if found[0].State != content.SecretCollapsed {
		t.Fatalf("the scanner read %q as %q, want %q", source, found[0].State,
			content.SecretCollapsed)
	}
}

// TestDemoTheBudgetIsResolvedForTheDemoReader holds which viewer's resolution the budget
// is about, because it is an argument rather than something derived.
//
// `Records` takes the visible set as an input on purpose — S-5.5 says never probe a
// campaign the viewer cannot see, and a type cannot enforce that — so "resolved" has a
// different answer per viewer and the gate has to pick. It picks the GM's, because the
// demo reader signs in as the demo GM and the broken-link report they would see is the
// GM's.
//
// The assertion is that the fixture's cross-campaign link **resolves**, which is the
// observable consequence: audited as an anonymous reader it would be unresolved, and a
// vault with such a link would need it declared broken — which would be a demo teaching
// the wrong lesson about a working feature.
func TestDemoTheBudgetIsResolvedForTheDemoReader(t *testing.T) {
	t.Parallel()

	loaded, resolution := demoLoaded(t, demoFixtureRoot)

	if len(loaded.campaigns) < 2 {
		t.Fatalf("the fixture holds %d campaign(s); the demo reader's visible set needs "+
			"at least two for a cross-campaign reference to mean anything",
			len(loaded.campaigns))
	}

	visible := demoVisibleCampaigns(loaded)
	if len(visible) != len(loaded.campaigns) {
		t.Fatalf("the visible set holds %d campaigns and the vault holds %d, so a "+
			"campaign is invisible to the demo reader and its links would audit as broken",
			len(visible), len(loaded.campaigns))
	}

	found := false

	for campaignAt := range loaded.campaigns {
		campaign := &loaded.campaigns[campaignAt]

		for pageAt := range loaded.campaigns[campaignAt].pages {
			for _, one := range resolution[campaignAt][pageAt] {
				if one.record.TargetCampaign == "" || one.record.TargetCampaign == campaign.slug {
					continue
				}

				if !one.record.Resolved {
					t.Fatalf(
						"a cross-campaign reference did not resolve for the demo reader "+
							"(%s -> %s in %s); audited as an anonymous reader it would "+
							"have to be declared deliberate breakage, which would make the "+
							"demo teach that a working feature is broken",
						loaded.campaigns[campaignAt].pages[pageAt].rel,
						one.record.TargetPath, one.record.TargetCampaign,
					)
				}

				found = true
			}
		}
	}

	if !found {
		t.Fatal("the fixture resolves no cross-campaign reference at all, so this test " +
			"cannot tell the demo reader's view from an anonymous one")
	}
}

// TestDemoTheGateDerivesItsRequirementsRatherThanEnumeratingThem is the anti-checklist
// test, and it reads this package's own AST to do it.
//
// A gate that listed kind names, extension names or page paths would pass today and drift
// tomorrow: a page deleted from the vault and a page deleted from the list are the same
// event, so nothing reports it. The requirement that D9 states — the list comes from the
// registry, so **adding** a kind turns the demo red — is only meaningful if the list is
// not also written down somewhere, and this is the check that it is not.
//
// **What it can and cannot see.** It sees every string literal in the package, so
// `[]string{"spell", "class"}` and `"spell, class"` are both caught. It cannot see a kind
// named inside a sentence — “ "no page demonstrates `spell`" “ — and does not claim
// to. That limit is stated rather than left for the next reader to discover.
func TestDemoTheGateDerivesItsRequirementsRatherThanEnumeratingThem(t *testing.T) {
	t.Parallel()

	kinds := demoKindsForBuild(t)

	forbidden := make(map[string]string, len(kinds.order))

	for _, kind := range kinds.order {
		forbidden[kind] = "a page kind"
	}

	for _, definition := range ext.Builtins() {
		forbidden[definition.Kind.String()] = "a render extension"
	}

	fset := token.NewFileSet()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()

		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		// Every literal the package writes down, whether in the gate or in its tests:
		// a hardcoded list in a test is the same checklist one layer out.
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}

			value, unquoteErr := strconv.Unquote(literal.Value)
			if unquoteErr != nil {
				return true
			}

			for _, field := range strings.FieldsFunc(value, func(r rune) bool {
				return r == ',' || r == '\n'
			}) {
				trimmed := strings.Trim(strings.TrimSpace(field), "`'\"")

				what, listed := forbidden[trimmed]
				if !listed {
					continue
				}

				t.Errorf(
					"%s spells out %s %q in a string literal, so this gate carries a "+
						"list as well as deriving one. A list of %ss is a checklist that "+
						"drifts the day a page is renamed, and D9's claim — that adding a "+
						"kind turns the demo red — only holds while the registry is the "+
						"only source of it",
					name, what, trimmed, what,
				)
			}

			return true
		})
	}
}

// TestDemoTheFixtureCarriesBothSecretStates is the positive half of the secret rule: the
// mutations prove the rule goes red, and this proves the fixture is not red because it
// demonstrates nothing at all.
//
// Without it, a gate whose secret rule were accidentally inverted — requiring that *no*
// secret be present, say — would pass on a fixture that had none. The pair of tests is
// the whole claim.
func TestDemoTheFixtureCarriesBothSecretStates(t *testing.T) {
	t.Parallel()

	loaded, _ := demoLoaded(t, demoFixtureRoot)

	shown := make(map[content.SecretState]struct{})

	for at := range loaded.campaigns {
		for _, page := range loaded.campaigns[at].pages {
			for _, secret := range page.secrets {
				shown[secret.state] = struct{}{}
			}
		}
	}

	for _, state := range demoSecretStates() {
		if _, present := shown[state]; present {
			continue
		}

		t.Fatalf("the fixture carries no %q callout, so the secret-state rule is being "+
			"satisfied by a fixture that demonstrates nothing", state)
	}
}
