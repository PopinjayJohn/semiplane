package demo_test

// The mutations: one per rule, each of which must turn the gate red.
//
// # The shape of a mutation test here
//
// Copy the committed fixture, break **exactly one** thing, and require the named rule
// to fire. The copying matters: a mutation applied to `testdata/vault` itself would
// make the fixture's own test order-dependent, and a mutation applied to a tree a test
// builds inline would be a tree whose every property the test author chose.
//
// # Why the empty vault is the important one
//
// The demo vault is two waves away, so the state this package could most easily ship
// is a gate that has nothing to fail on. `TestDemoTheAuditFailsOnAnEmptyVault` and
// `TestDemoTheShippedVaultIsAuditedWhenItExists` are that state handled deliberately:
// one proves the auditor goes red on nothing, and the other proves the *absence* of the
// real vault is exercised as a red path rather than skipped.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/content/ext"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
)

// demoBrokenLinkPage is the fixture page that teaches broken links, named rather than
// derived — it is the *subject under test* rather than a requirement, and every
// reference to it below is `demoBrokenLinkPage` so a rename is one edit.
const demoBrokenLinkPage = "greyhaven/notes/broken-links.md"

// demoBackToIndex is the trailing sentence of that page, which exists so a mutation can
// append to it without disturbing the three deliberate references above.
const demoBackToIndex = "Back to [[index]]."

// TestDemoAnUndeclaredBrokenLinkTurnsTheBudgetRed is the mutation for the budget's
// first direction.
//
// One extra failing reference with no `demo.broken` entry is exactly what an
// accidental breakage looks like, and it must be red: a budget rule that accepted an
// undeclared failure would be satisfied by any vault at all.
func TestDemoAnUndeclaredBrokenLinkTurnsTheBudgetRed(t *testing.T) {
	t.Parallel()

	vault := demoCopyFixture(t)
	demoReplaceOnce(t, vault, demoBrokenLinkPage, demoBackToIndex,
		"Also [[Nowhere At All]].\n\n"+demoBackToIndex)

	findings := demoAudit(t, vault)

	demoRequireRule(t, findings, demoRuleBudget)
	demoRequireFindingOn(t, findings, demoRuleBudget, "Nowhere At All")
}

// TestDemoARepairedDeclarationTurnsTheBudgetRed is the mutation for the budget's second
// direction.
//
// A declaration that no longer matches anything is a demonstration somebody has
// repaired by accident — the page was written, or renamed — and it must be red: a
// budget that only counted failures would be counting a link that works.
func TestDemoARepairedDeclarationTurnsTheBudgetRed(t *testing.T) {
	t.Parallel()

	vault := demoCopyFixture(t)
	demoReplaceOnce(t, vault, demoBrokenLinkPage, "    - The Sentinel\n", "")

	findings := demoAudit(t, vault)

	demoRequireRule(t, findings, demoRuleBudget)
	demoRequireFindingOn(t, findings, demoRuleBudget, "The Sentinel")
}

// TestDemoTheBudgetIsDerivedAndNotAConstant runs the same gate over three vaults whose
// deliberate breakages number **0**, **3** and **5**, and requires all three to pass.
//
// **This is the mutation for "make the budget a hardcoded constant", and it is not a
// change of number.** A gate that hardcoded the fixture's own count of three would pass
// only the middle row, so this test fails for a hardcoded gate whatever the constant
// is — and it also fails a gate that demanded a *minimum*, because the zero row
// demonstrates nothing broken at all. The count comes from
// `len(demoDeclaredBrokenLinkCount)`, which the test reads out of the front matter, so
// the middle row's number is not itself hardcoded here either.
func TestDemoTheBudgetIsDerivedAndNotAConstant(t *testing.T) {
	t.Parallel()

	declared := demoDeclaredBrokenLinkCount(t, demoFixtureRoot)
	if declared == 0 {
		t.Fatal("the fixture declares no deliberate breakage, so there is no budget to " +
			"derive and this test would prove nothing")
	}

	t.Run("the fixture's own count", func(t *testing.T) {
		t.Parallel()

		demoRequireClean(t, demoAudit(t, demoCopyFixture(t)))
	})

	t.Run("zero deliberate breakages", func(t *testing.T) {
		t.Parallel()

		vault := demoCopyFixture(t)

		// Every declared reference removed **and** every declaration removed, so the
		// two sides of the budget still agree — at zero. Removing only the references
		// would leave declarations matching nothing, which is the other mutation.
		//
		// Brackets off rather than a path operation on the whole construct: the
		// replacement has to leave a page that still reads.
		for _, target := range []string{
			"[[The Sentinel]]",
			"[[/lost-realm/The Drowned Road]]",
			"[[../../../../etc/passwd]]",
		} {
			plain := strings.TrimSuffix(strings.TrimPrefix(target, "[["), "]]")
			demoReplaceEvery(t, vault, demoBrokenLinkPage, target, plain)
		}

		demoReplaceOnce(t, vault, demoBrokenLinkPage,
			"demo:\n  broken:\n    - The Sentinel\n"+
				"    - /lost-realm/The Drowned Road\n    - ../../../../etc/passwd\n", "")

		if got := demoDeclaredBrokenLinkCount(t, vault); got != 0 {
			t.Fatalf("the mutated page still declares %d deliberate breakages, want 0", got)
		}

		demoRequireClean(t, demoAudit(t, vault))
	})

	t.Run("two more than the fixture declares", func(t *testing.T) {
		t.Parallel()

		vault := demoCopyFixture(t)
		before := demoDeclaredBrokenLinkCount(t, vault)

		demoReplaceOnce(t, vault, demoBrokenLinkPage, demoBackToIndex,
			"[[Absent One]] and [[/absent-two/Absent Two]].\n\n"+demoBackToIndex)

		demoReplaceOnce(t, vault, demoBrokenLinkPage, "    - The Sentinel\n",
			"    - The Sentinel\n    - Absent One\n    - /absent-two/Absent Two\n")

		if got := demoDeclaredBrokenLinkCount(t, vault); got != before+2 {
			t.Fatalf("the fixture now declares %d deliberate breakages, want %d", got, before+2)
		}

		demoRequireClean(t, demoAudit(t, vault))
	})
}

// demoDeclaredBrokenLinkCount reads the broken-links page's declared breakage through
// the gate's own reader.
//
// **Through `declaredBrokenLinks` and not a second YAML parse**, so a test asserting on
// the declaration is asserting on the same reading the rule makes. A second parser here
// would be a second answer to "what does this page declare", which is the one thing the
// budget's declaration must not have.
//
// It takes no path: the page that carries the declaration is `demoBrokenLinkPage` in
// every caller, and a parameter that is always the same value is a parameter that reads
// like an option and is not one.
func demoDeclaredBrokenLinkCount(t *testing.T, vault string) int {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(vault, demoBrokenLinkPage))
	if err != nil {
		t.Fatalf("read %s: %v", demoBrokenLinkPage, err)
	}

	doc := content.Parse(raw, demoKindsForBuild(t))

	return len(declaredBrokenLinks(doc))
}

// TestDemoRemovingAKindDemonstrationTurnsTheGateRed is the mutation for kind coverage,
// and it removes a kind **derived from the registry** rather than named.
//
// The page and the kind are both found by walking the loaded vault for the first page
// whose declared kind the registry knows, so this test does not contain the name of a
// kind: renaming a kind in the data pack moves the mutation with it, and a gate that
// had hardcoded the list would go red on the *requirement* side too — which is the
// behaviour D9 asks for and which this test cannot distinguish from a bug. What it
// pins is the other direction: a kind that stops being demonstrated must be reported.
func TestDemoRemovingAKindDemonstrationTurnsTheGateRed(t *testing.T) {
	t.Parallel()

	kinds := demoKindsForBuild(t)

	vault := demoCopyFixture(t)
	rel, kind := demoFindPageByKind(t, vault, func(declared string) bool {
		_, known := kinds.known[declared]

		return known
	})

	// To prose, which is the degradation `domain.ResolvePageKind` performs for an
	// unregistered kind and is therefore the one value guaranteed not to be demanded.
	demoReplaceOnce(t, vault, rel, "kind: "+kind, "kind: "+string(domain.KindProse))

	findings := demoAudit(t, vault)

	demoRequireRule(t, findings, demoRuleKind)
	demoRequireFindingOn(t, findings, demoRuleKind, kind)
}

// TestDemoTheRegistryTurnsTheGateRedWhenAKindJoins is the same rule from the *other*
// side, and it is the one D9 names.
//
// A kind added to the plugin's data pack must turn the gate red until the vault
// demonstrates it, with no edit to the gate. The mutation cannot add a kind to a
// compiled-in pack, so it adds one to the requirement set the rule reads — which is the
// same object the registry produces, and the test says so.
func TestDemoTheRegistryTurnsTheGateRedWhenAKindJoins(t *testing.T) {
	t.Parallel()

	loaded, _ := demoLoaded(t, demoCopyFixture(t))

	kinds := demoKindsForBuild(t)

	added := "a-kind-this-build-does-not-know"

	grown := demoKinds{known: make(map[string]struct{}, len(kinds.known)+1)}

	for _, kind := range kinds.order {
		grown = grown.with(kind)
	}

	grown = grown.with(added)

	findings := demoKindFindings(loaded, grown)
	if len(findings) != 1 {
		t.Fatalf("adding a kind produced %d findings, want exactly 1:\n%s",
			len(findings), demoReport(findings))
	}

	if !strings.Contains(findings[0].detail, added) {
		t.Fatalf("the finding does not name the kind it is about: %s", findings[0])
	}
}

// TestDemoRemovingAnExtensionTurnsTheGateRed is the mutation for extension coverage.
//
// The extension is derived from `ext.Builtins()` and the mutation rewrites its
// **keyword**, which is a field of the `ext.Definition` rather than a fact this test
// holds: replacing `{{statblock:` with an unrecognised directive leaves a page that
// renders — `ext`'s own rule is that an unknown `{{…}}` is text — and the extension is
// genuinely no longer exercised anywhere in the vault.
//
// A `{{name:…}}` extension is the only shape this mutation can address, which is
// deliberate: `[[` and `![[` have no keyword, and turning one into the other changes
// what the page *means* rather than making it inert.
func TestDemoRemovingAnExtensionTurnsTheGateRed(t *testing.T) {
	t.Parallel()

	target := demoDirectiveExtension()
	keyword := "{{" + target.Name + ":"

	vault := demoCopyFixture(t)

	replaced := 0

	for _, rel := range demoFixtureMarkdown(t) {
		body := demoRead(t, vault, rel)
		if !strings.Contains(body, keyword) {
			continue
		}

		replaced += strings.Count(body, keyword)

		demoWrite(t, vault, rel,
			strings.ReplaceAll(body, keyword, "{{a-directive-nobody-installs:"))
	}

	if replaced == 0 {
		t.Fatalf("the fixture exercises no %q extension, so the mutation cannot apply",
			target.Kind)
	}

	findings := demoAudit(t, vault)

	demoRequireRule(t, findings, demoRuleExtension)
	demoRequireFindingOn(t, findings, demoRuleExtension, target.Kind.String())
}

// demoDirectiveExtension returns the last `{{name:…}}` definition the pipeline installs.
//
// **Ordered, not chosen by name.** `ext.Builtins()` is a fixed-order list and the last
// directive-bearing entry is `dice` in this build; if a future extension is appended it
// moves here, and if the mutation stops applying the test says the fixture does not
// exercise the newest extension rather than silently passing.
func demoDirectiveExtension() ext.Definition {
	builtins := ext.Builtins()

	for _, definition := range slices.Backward(builtins) {
		if definition.Name != "" {
			return definition
		}
	}

	panic("no directive-bearing extension is installed, so there is no `{{name:…}}` " +
		"coverage to mutate")
}

// demoFixtureMarkdown returns every markdown path in the committed fixture, sorted.
func demoFixtureMarkdown(t *testing.T) []string {
	t.Helper()

	var found []string

	err := filepath.WalkDir(demoFixtureRoot, func(abs string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() || !strings.HasSuffix(abs, ".md") {
			return nil
		}

		rel, relErr := filepath.Rel(demoFixtureRoot, abs)
		if relErr != nil {
			return fmt.Errorf("relativise %s: %w", abs, relErr)
		}

		found = append(found, filepath.ToSlash(rel))

		return nil
	})
	if err != nil {
		t.Fatalf("walk the fixture: %v", err)
	}

	slices.Sort(found)

	return found
}

// TestDemoRemovingTheRevealedSecretTurnsTheGateRed is the mutation for the secret-state
// rule, and it is the exact one the work item names.
//
// The state byte comes from `content.SecretRevealed.String()`, so the mutation is "the
// vault stopped demonstrating a revealed secret" without this file naming the marker. A
// vault showing only collapsed secrets demonstrates a page with nothing in it.
func TestDemoRemovingTheRevealedSecretTurnsTheGateRed(t *testing.T) {
	t.Parallel()

	vault := demoCopyFixture(t)

	rel := demoFindPageInSecretState(t, vault, content.SecretRevealed)
	marker := "[!" + demoSecretCalloutName() + "]" + content.SecretRevealed.String()

	demoReplaceOnce(t, vault, rel, marker,
		"[!"+demoSecretCalloutName()+"]"+content.SecretCollapsed.String())

	findings := demoAudit(t, vault)

	demoRequireRule(t, findings, demoRuleSecret)
	demoRequireFindingOn(t, findings, demoRuleSecret, content.SecretRevealed.String())
}

// TestDemoRemovingTheCollapsedSecretTurnsTheGateRed is the other half, because "both
// states" is a claim about a pair and one direction of it proves half the pair.
func TestDemoRemovingTheCollapsedSecretTurnsTheGateRed(t *testing.T) {
	t.Parallel()

	vault := demoCopyFixture(t)

	rel := demoFindPageInSecretState(t, vault, content.SecretCollapsed)
	marker := "[!" + demoSecretCalloutName() + "]" + content.SecretCollapsed.String()

	demoReplaceOnce(t, vault, rel, marker,
		"[!"+demoSecretCalloutName()+"]"+content.SecretRevealed.String())

	findings := demoAudit(t, vault)

	demoRequireRule(t, findings, demoRuleSecret)
	demoRequireFindingOn(t, findings, demoRuleSecret, content.SecretCollapsed.String())
}

// demoFindPageInSecretState returns a page path carrying a callout in this state.
//
// **The only page in the fixture carrying one, and found through `content.ScanSecrets`**
// rather than by grepping for the marker — because a `-` inside a code fence is not a
// collapsed secret, and a test that grepped would pick it and then mutate a page that
// still demonstrates the state.
func demoFindPageInSecretState(
	t *testing.T,
	vault string,
	state content.SecretState,
) string {
	t.Helper()

	for _, rel := range demoFixtureMarkdown(t) {
		raw, err := os.ReadFile(filepath.Join(vault, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}

		for _, secret := range content.ScanSecrets(content.Parse(raw, nil).Body) {
			if secret.State == state {
				return rel
			}
		}
	}

	t.Fatalf("no page in the fixture carries a callout in state %q", state)

	return ""
}

// TestDemoAnUnreachablePageTurnsTheGateRed is the mutation for reachability.
//
// The page is chosen from the loaded graph as one the index is the **only** way to
// reach, and only that link is removed. A page other pages also link to would stay
// reachable and the test would be green for nothing — which is the failure a mutation
// test exists to catch, and one this file's `demoFindSoleInboundPage` documents.
func TestDemoAnUnreachablePageTurnsTheGateRed(t *testing.T) {
	t.Parallel()

	vault := demoCopyFixture(t)
	slug, rel := demoFindSoleInboundPage(t, vault)

	demoUnlinkFromIndex(t, vault, slug, rel)

	findings := demoAudit(t, vault)

	demoRequireRule(t, findings, demoRuleReachable)
	demoRequireFindingOn(t, findings, demoRuleReachable, rel)
}

// TestDemoAMissingAssetTurnsTheGateRed is the mutation for asset coverage.
//
// The asset is found by resolving the vault and asking the resolver which references
// are assets — so the deleted file is one a page genuinely references, rather than a
// file that happened to be in the tree.
func TestDemoAMissingAssetTurnsTheGateRed(t *testing.T) {
	t.Parallel()

	vault := demoCopyFixture(t)
	slug, rel := demoFirstReferencedAsset(t, vault)

	if err := os.Remove(filepath.Join(vault, demoVaultPath(slug, rel))); err != nil {
		t.Fatalf("remove %s/%s: %v", slug, rel, err)
	}

	findings := demoAudit(t, vault)

	demoRequireRule(t, findings, demoRuleAsset)
	demoRequireFindingOn(t, findings, demoRuleAsset, rel)
}

// demoFirstReferencedAsset returns the root-relative path of an asset a page in the
// first campaign references.
//
// Resolved through the product, and the path comes out of the **address** the resolver
// built rather than the author's target, so the test deletes the file the route would
// 404 on and not a differently-spelled one.
func demoFirstReferencedAsset(t *testing.T, vault string) (slug, rel string) {
	t.Helper()

	loaded, resolution := demoLoaded(t, vault)

	for campaignAt := range loaded.campaigns {
		for pageAt := range loaded.campaigns[campaignAt].pages {
			for oneAt := range resolution[campaignAt][pageAt] {
				one := &resolution[campaignAt][pageAt][oneAt]

				if one.link.Kind != content.LinkToAsset {
					continue
				}

				slug, section, rel, ok := demoParseHref(one.link.Href)
				if !ok || section != "assets" || slug != loaded.campaigns[campaignAt].slug {
					continue
				}

				return loaded.campaigns[campaignAt].slug, rel
			}
		}
	}

	t.Fatal("no page in the fixture references an asset in its own campaign")

	return "", ""
}

// TestDemoAnAssetInACampaignTheVaultDoesNotHoldTurnsTheGateRed covers the
// cross-campaign branch of the asset rule.
//
// **Separate from the budget on purpose.** An asset reference that 404s is a defect
// whoever wrote it, and the plan's deliberate breakage is unresolved *links*; putting
// assets in the budget would make a missing map indistinguishable from the three links
// the showcase teaches. The campaign slug here is invented — it is the shape of the
// failure, not a fact about the product.
func TestDemoAnAssetInACampaignTheVaultDoesNotHoldTurnsTheGateRed(t *testing.T) {
	t.Parallel()

	vault := demoCopyFixture(t)
	_, asset := demoFirstReferencedAsset(t, vault)

	// A **wikilink**, not an embed: ADR 0017 refuses an embed across a campaign
	// boundary, so an `![[…]]` here is refused with an empty address and never
	// reaches the asset rule at all. It would land in the budget instead, which is
	// correct and is the other mutation's subject.
	demoReplaceOnce(t, vault, demoBrokenLinkPage, demoBackToIndex,
		"[[/no-such-campaign/"+asset+"]]\n\n"+demoBackToIndex)

	findings := demoAudit(t, vault)

	demoRequireRule(t, findings, demoRuleAsset)
	demoRequireFindingOn(t, findings, demoRuleAsset, "no-such-campaign")
}

// TestDemoACampaignWithNoIndexPageTurnsTheGateRed is the mutation for the index rule.
//
// Removing the `kind:` line takes away the walk's root, so the reachability rule has
// nowhere to walk from and stays silent — which is correct rather than a hole, and this
// test's comment is where that is recorded. An earlier version of it asserted both
// findings on the reasoning that two was more informative; the fixture disagreed,
// because reporting every page of a campaign with no front page as *unreachable* is one
// cause reported sixteen times, and the index finding already says what to do.
func TestDemoACampaignWithNoIndexPageTurnsTheGateRed(t *testing.T) {
	t.Parallel()

	vault := demoCopyFixture(t)

	demoReplaceOnce(t, vault, demoIndexPageOf(t, vault, "greyhaven"),
		"kind: "+rules.KindIndex.String()+"\n", "")

	findings := demoAudit(t, vault)

	demoRequireRule(t, findings, demoRuleIndex)
	demoRequireNoRule(t, findings, demoRuleReachable)
}

// TestDemoACampaignWithTwoIndexPagesTurnsTheGateRed is the other half of the index rule.
//
// A second index page leaves the walk with an arbitrary choice of front page, and the
// second page is itself unreachable from the first — which is why this mutation is
// expected to produce two findings rather than one.
func TestDemoACampaignWithTwoIndexPagesTurnsTheGateRed(t *testing.T) {
	t.Parallel()

	vault := demoCopyFixture(t)

	demoWrite(t, vault, "greyhaven/second-index.md", strings.Join([]string{
		"---",
		"title: Second",
		"kind: " + rules.KindIndex.String(),
		"---",
		"",
		"# Second",
		"",
		"A second front page, which is one too many.",
		"",
	}, "\n"))

	demoRequireRule(t, demoAudit(t, vault), demoRuleIndex)

	// **Reachability stays silent here, and that is correct rather than a hole.**
	// With two index pages the walk has no root to walk from, so reporting every page
	// of the campaign would be one cause reported sixteen times. The index finding
	// names both paths, so the author is told exactly which two pages to choose
	// between.
	demoRequireNoRule(t, demoAudit(t, vault), demoRuleReachable)
}

// TestDemoNoFindingCarriesSecretText is the leak guard, and it is the reason a finding
// may not quote a reference that lives inside a callout.
//
// The mutation puts a **failing reference inside the collapsed callout's body**, which
// is a reference the pipeline reports and the budget must therefore complain about — so
// the finding is real and unavoidable. What must not happen is the target appearing in
// it, because a gate that prints the thing it is checking has put a secret into CI
// output, and CI output is a log aggregator like any other (S-12.3).
func TestDemoNoFindingCarriesSecretText(t *testing.T) {
	t.Parallel()

	const (
		// The body text the mutation puts inside the callout, and the phrase that must
		// appear in no finding.
		secretPhrase = "The vault's second entrance is behind the mill"
		// The reference the mutation puts in the same body line, and the string that
		// must appear in no finding either.
		leakedTarget = "Nothing At All"
	)

	vault := demoCopyFixture(t)

	demoReplaceOnce(t, vault, "greyhaven/secrets.md",
		secretPhrase, secretPhrase+" and [["+leakedTarget+"]]")

	findings := demoAudit(t, vault)

	// The finding must exist, or the test would be satisfied by the mutation having
	// produced nothing at all. What it must **not** say is the target: the assertion
	// looks for the word `callout`, which is what the redacted detail says instead,
	// and that is the shape of the guarantee — the finding is actionable about *where*
	// to look and silent about *what* is there.
	demoRequireRule(t, findings, demoRuleBudget)
	demoRequireFindingOn(t, findings, demoRuleBudget, "callout")

	for _, finding := range findings {
		rendered := finding.String()

		for _, forbidden := range []string{leakedTarget, secretPhrase} {
			if strings.Contains(rendered, forbidden) {
				t.Fatalf(
					"a finding printed text from inside a [!"+demoSecretCalloutName()+
						"] callout (%q appears in %q), which is the disclosure S-12.3 "+
						"forbids",
					forbidden, rendered,
				)
			}
		}
	}
}

// TestDemoTheAuditSkipsThePagesContentDoesNotIndex holds the duplicated indexability
// rule from both sides.
//
// The fixture already carries a page under a dot-directory — committed, so the committed
// tree proves it — and this adds the `node_modules` half, which **cannot** be committed
// because `.gitignore` excludes that name at any depth. Between them, the restatement
// of `content`'s predicates in this package is held by a negative case on both halves
// rather than by a comment claiming it matches.
func TestDemoTheAuditSkipsThePagesContentDoesNotIndex(t *testing.T) {
	t.Parallel()

	vault := demoCopyFixture(t)

	demoWrite(t, vault, "greyhaven/node_modules/pkg/readme.md", strings.Join([]string{
		"---",
		"title: A dependency's readme",
		"kind: " + rules.KindIndex.String(),
		"---",
		"",
		"# A dependency's readme",
		"",
		"This page is inside a `node_modules` directory, which the wiki index refuses and " +
			"so the demo gate must refuse. If the gate counted it, this would be a second " +
			"index page and an unreachable one, and the audit would report both.",
		"",
	}, "\n"))

	demoRequireClean(t, demoAudit(t, vault))
}

// TestDemoTheAuditIsDeterministic runs the same mutated vault twice and requires the
// findings to come out in the same order.
//
// The requirement is about ordering rather than about content, and it is a requirement
// because a gate whose output reorders between runs makes a reviewer read one failure
// as two — and because a finding list built from map iteration is the shape a
// determinism failure takes here.
func TestDemoTheAuditIsDeterministic(t *testing.T) {
	t.Parallel()

	build := func() string {
		vault := demoCopyFixture(t)
		demoReplaceOnce(t, vault, demoBrokenLinkPage, demoBackToIndex,
			"[[Absent One]] and [[/absent-two/Absent Two]].\n\n"+demoBackToIndex)

		return vault
	}

	first := demoReport(demoAudit(t, build()))
	second := demoReport(demoAudit(t, build()))

	if first != second {
		t.Fatalf("two audits of the same tree disagree:\nfirst:\n%s\nsecond:\n%s",
			first, second)
	}

	if !strings.Contains(first, demoRuleBudget) {
		t.Fatalf("the mutated vault reported no budget finding, so the determinism "+
			"check compared two empty lists:\n%s", first)
	}
}

// TestDemoTheAuditFailsOnAnEmptyVault is the crux, and it is the state this work item is
// most able to ship.
//
// The demo vault does not exist yet. A gate run over nothing would print nothing, exit
// zero and read exactly like a green gate over a verified vault — which is the failure
// this whole package exists to prevent. So an empty directory, and a directory that
// does not exist, must both be **red**.
//
// It also pins **which** rules fire, because the honest answer is not "all of them":
// reachability, the index rule, the budget and asset coverage are vacuous when there
// are no pages, and a test that demanded findings from them would be demanding
// nonsense. What must be impossible is the reverse — a rule being *absent from the
// expectation* because it can no longer fail.
func TestDemoTheAuditFailsOnAnEmptyVault(t *testing.T) {
	t.Parallel()

	empty := t.TempDir()

	findings := demoAudit(t, empty)
	if len(findings) == 0 {
		t.Fatal("the audit reported nothing for an empty directory, so a gate pointed at " +
			"an empty vault would pass — which is worse than no gate at all")
	}

	demoRequireRule(t, findings, demoRuleVault)
	demoRequireRule(t, findings, demoRuleKind)
	demoRequireRule(t, findings, demoRuleExtension)
	demoRequireRule(t, findings, demoRuleSecret)

	// The vacuous rules are named rather than merely absent, so the boundary is a
	// claim. A rule appearing here that cannot fire on an empty tree is a bug in this
	// expectation; a rule that *should* fire and does not is the bug this test exists
	// to catch.
	demoRequireNoRule(t, findings, demoRuleReachable)
	demoRequireNoRule(t, findings, demoRuleIndex)
	demoRequireNoRule(t, findings, demoRuleBudget)
	demoRequireNoRule(t, findings, demoRuleAsset)
	demoRequireNoRule(t, findings, demoRuleReadable)
}

// TestDemoTheAuditFailsOnAVaultRootThatDoesNotExist covers the other half of "nothing to
// check": the path is not there at all.
//
// **A missing directory is not a missing vault**, and the difference is worth a test
// because a shell target that reports "no campaigns found" for a path its own
// variable names has silently accepted a build without a vault — which is the state
// this work item lands in.
func TestDemoTheAuditFailsOnAVaultRootThatDoesNotExist(t *testing.T) {
	t.Parallel()

	findings := demoAudit(t, filepath.Join(t.TempDir(), "there-is-no-vault-here"))

	demoRequireRule(t, findings, demoRuleVault)
}

// TestDemoTheShippedVaultIsAuditedWhenItExists is the rule that turns this from a
// fixture gate into the real one the moment the vault lands.
//
// **Its absence is a red path and not a skip**, which is the whole design of this test.
// While `demo-vault/` does not exist it requires two things: that auditing the absent
// path is red (proved above, and re-proved here so the two live together), and that the
// fixture is what the gate is currently pointed at — so a reader who sees
// `make demo-check` green knows exactly what was checked and knows it was not the
// shipped vault.
//
// The moment `demo-vault/` exists, the same test requires it to be clean **and** to hold
// at least one campaign. A directory that exists and is empty is red rather than a
// pass, which is the shape of "the vault was committed with nothing in it".
func TestDemoTheShippedVaultIsAuditedWhenItExists(t *testing.T) {
	t.Parallel()

	vault := demoVaultUnderTest()

	if _, err := os.Stat(vault); err != nil {
		t.Logf(
			"%s does not exist yet — phase 11 wave B. The shipped vault is NOT verified "+
				"by this run; the gate is being proved against %s, and the absent path is "+
				"required to be red below.",
			vault, demoFixtureRoot,
		)

		demoRequireRule(t, demoAudit(t, vault), demoRuleVault)
		demoRequireClean(t, demoAudit(t, demoFixtureRoot))

		return
	}

	demoRequireClean(t, demoAudit(t, vault))
}

// TestDemoTheShippedVaultIsAuditedWhenItExists is the rule that turns this from a fixture
// gate into the real one the moment the vault lands.
//
// **Its absence is a red path and not a skip**, which is the whole design of this test.
// While the vault does not exist it requires two things: that auditing the absent path is
// red (proved above, and re-proved here so the two live together), and that the fixture is
// what the gate is currently pointed at — so a reader who sees `make demo-check` green
// knows exactly what was checked, and knows it was not the shipped vault.
//
// The moment the vault exists, the same test requires it to be clean, which a
// present-but-empty directory cannot be: `demo-vault/` with nothing in it audits red on
// `demoRuleVault` and on kind, extension and secret coverage.
func TestDemoAVaultThatExistsAndIsEmptyIsNotAPass(t *testing.T) {
	t.Parallel()

	empty := filepath.Join(t.TempDir(), "demo-vault")
	if err := os.MkdirAll(empty, 0o750); err != nil {
		t.Fatalf("create the empty vault: %v", err)
	}

	findings := demoAudit(t, empty)

	demoRequireRule(t, findings, demoRuleVault)
	demoRequireRule(t, findings, demoRuleKind)
}

// demoRequireFindingOn requires a finding of this rule whose text mentions this string.
//
// **Mentions, rather than equals**, because a finding is a sentence written for a vault
// author and requiring the whole sentence would couple every test to the wording. What
// is required is that the specific value the mutation introduced appears in it — which
// is what makes the finding *actionable*, and a rule that fired without naming its
// subject would be a rule nobody could act on.
func demoRequireFindingOn(t *testing.T, findings []demoFinding, rule, mentions string) {
	t.Helper()

	for _, finding := range findings {
		if finding.rule == rule && strings.Contains(finding.String(), mentions) {
			return
		}
	}

	t.Fatalf("no %q finding mentions %q. Findings:\n%s", rule, mentions, demoReport(findings))
}
