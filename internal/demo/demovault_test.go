package demo_test

// The demo vault's completeness gate: `make demo-check`.
//
// # What this is
//
// Six rules over a tree of campaigns, all of them **derived rather than enumerated**,
// and the derivation is the whole design:
//
//	reachability      every page reachable from its campaign's `kind: index`
//	kind coverage     from `plugin.Registry.Kinds()`, over every shipped edition
//	extension coverage from `ext.Builtins()`, observed as the *pipeline* reports it
//	secret states     from the two `content.SecretState` values
//	broken links      the budget is `demo.broken`'s length, not a constant
//	asset coverage    every asset a page references, resolved through the `Root`
//
// A gate that listed page names would be a checklist describing pages that no
// longer exist, and the failure mode is silent: deleting a page from a list and
// deleting a page from the vault look identical. So nothing here names a page, a kind
// or an extension. Every requirement is asked of the build or of the vault, and
// `TestDemoTheGateDerivesItsRequirementsRatherThanEnumeratingThem` reads this
// package's own AST to hold that.
//
// # Why it lives in test files
//
// The gate is a `go test` run through the Makefile, and its implementation is test
// code because `internal/demo`'s non-test files belong to another work item. The
// `_test` suffix on the package keeps the two apart entirely: nothing here can
// collide with a symbol the seed and the manifest reader declare, and nothing here
// depends on a manifest schema that has not landed.
//
// # Why the budget is declared and not computed
//
// One requirement cannot be derived, and pretending otherwise is worse than saying so.
// A reference that resolves to nothing is a **defect** until somebody says otherwise,
// because that is what it is in every real vault — and the demo needs at least one,
// because a vault that demonstrates only working links demonstrates nothing about
// what a broken one looks like. So the vault declares its own deliberate breakage, in
// the page that teaches it:
//
//	---
//	demo:
//	  broken:
//	    - The Sentinel
//	    - lost-realm/The Drowned Road
//	    - ../../../../etc/passwd
//	---
//
// The *count* is derived — it is the length of those lists — so there is no constant
// anywhere, and a vault demonstrating one broken link and a vault demonstrating six
// both pass. What the declaration buys is the two directions the gate asserts:
//
//   - an unresolved reference with no list entry is a finding (an accidental breakage
//     the author did not mean), and
//   - a list entry that now resolves is a finding (the demonstration has been
//     repaired by accident, or the page was renamed).
//
// Either one alone would be satisfiable by a constant. Together they cannot: a gate
// that hardcoded `3` fails the moment the vault demonstrates a different number, and
// `TestDemoTheBudgetIsDerivedAndNotAConstant` proves it fails for **0** and for **5**.
//
// The declaration cannot rot silently either way, which is the property a hand-written
// checklist lacks. It lists *targets that do not resolve*, never pages that must exist
// — so a renamed page breaks the wikilink (a finding) rather than quietly satisfying a
// stale entry.
//
// # Front matter, and what it costs
//
// `demo:` is an unknown key, and an unknown key is a non-event (S-3.3): a build
// without this gate ignores it entirely, and no template renders it. It is *not* the
// manifest, which is where per-campaign declared intent belongs (V2), and it is the
// one thing here the integrator should move when that schema lands — which is why the
// whole declaration is behind a single function, `declaredBrokenLinks`.
//
// # Secrets, and why a finding never prints one
//
// A finding may quote a reference's target, and a reference inside a `[!secret]` body
// is a reference. So a page's callouts contribute line ranges, and a finding whose
// line falls inside one prints the rule, the page and the line and nothing else.
// `TestDemoNoFindingCarriesSecretText` feeds the auditor a vault with a broken link
// inside a callout and requires the target to appear in no finding — because a gate
// that prints the thing it is checking has leaked it into CI output, which is a log
// aggregator like any other (AGENTS.md, S-12.3).

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e/overlays"
	"github.com/semiplane/semiplane/internal/plugin"
)

// The nine rules' identifiers, as stable strings.
//
// Named constants rather than bare literals at the finding sites, because a finding is
// what a test asserts on, and a test asserting on a misspelled literal fails for the
// wrong reason. The kebab-case spelling is the one that reaches CI output, so it is
// part of the contract rather than a presentation choice.
const (
	// demoRuleVault fires when the vault root does not exist, holds no campaign, or
	// holds a campaign whose content root will not open. "There is nothing to check"
	// and "everything checked out" must not print the same word, and this is the
	// rule that tells them apart.
	demoRuleVault = "vault-holds-at-least-one-campaign"

	// demoRuleReadable fires for a page that could not be read, whose front matter
	// did not interpret, or that the renderer refused. A page whose front matter did
	// not interpret renders as prose with no warning, so it would silently stop
	// demonstrating the kind it declares.
	demoRuleReadable = "every-page-is-readable"

	// demoRuleIndex fires when a campaign has no page of kind `index`, or more than
	// one. Zero leaves the reachability walk with no root; two leave it with an
	// arbitrary choice.
	demoRuleIndex = "every-campaign-has-exactly-one-index-page"

	// demoRuleReachable fires for a page no chain of wikilinks from the campaign's
	// index reaches.
	demoRuleReachable = "every-page-is-reachable-from-its-index"

	// demoRuleKind fires for a registered page kind no page demonstrates.
	demoRuleKind = "every-registered-kind-is-demonstrated"

	// demoRuleExtension fires for an installed render extension no page exercises.
	demoRuleExtension = "every-installed-render-extension-is-exercised"

	// demoRuleSecret fires for a `[!secret]` state no page carries.
	demoRuleSecret = "every-secret-state-is-demonstrated"

	// demoRuleBudget fires when the set of unresolved references is not exactly the
	// set the vault declared, in either direction.
	demoRuleBudget = "unresolved-links-are-exactly-the-declared-ones"

	// demoRuleAsset fires for an asset a page references that is not in the vault.
	demoRuleAsset = "every-referenced-asset-exists"
)

// demoFinding is one thing the vault got wrong.
//
// Carries the campaign, the page and a line rather than the page's text, because a
// finding is printed into CI output and CI output is a log aggregator (S-12.3). The
// `page` field is a path an author recognises; the `detail` field is a kind, a target
// the resolver saw, or a sentence about a declaration — and the one thing it may never
// hold is bytes from inside a `[!secret]` callout.
type demoFinding struct {
	rule     string
	campaign string
	page     string
	line     int
	detail   string
}

// String renders the finding for CI output: one line, rule first, so a log full of
// them sorts by cause.
func (f demoFinding) String() string {
	where := f.campaign

	if f.page != "" {
		where += "/" + f.page
	}

	if f.line > 0 {
		where += ":" + strconv.Itoa(f.line)
	}

	return fmt.Sprintf("%s: %s: %s", f.rule, where, f.detail)
}

// demoSecret is one `[!secret]` callout, reduced to what a finding may say about it.
//
// **State and line, and never the body.** `content.Secret` carries the author's text
// in a `Body` field and this type deliberately does not keep it: the auditor holds
// every page of the vault in memory at once, and a struct that retained the bodies
// would be a vault's worth of secrets in a value a `%v` or a `t.Log` could print.
type demoSecret struct {
	state content.SecretState
	first int
	last  int
}

// demoPage is one indexed page and the facts the gate needs about it.
type demoPage struct {
	// rel is the page's root-relative path with its `.md`, which is the same string
	// `domain.Page.Path` carries and the one `content.NewOrigin` wants.
	rel string

	// kind is the kind to *honour*, taken from what the renderer resolved rather than
	// from the front matter — so a page whose declared kind is unknown is recorded as
	// the prose it renders as, on the page and in this audit alike.
	kind domain.PageKind

	// declared is what the author wrote, kept so a finding can say "declared `tokn`"
	// rather than only "did not demonstrate `token`".
	declared string

	title string

	// refs are the references **as the render pipeline reported them**, which is what
	// makes extension coverage an observation rather than a guess: a page whose
	// `{{dice:…}}` the pipeline did not turn into a reference has not used it,
	// whatever its bytes look like.
	refs []content.Reference

	// doc is the page as the pipeline's own parser split it, kept whole so the budget
	// rule reads a declaration out of the front matter without a second YAML parse —
	// which would be a second answer to "what did this page declare".
	doc content.Document

	secrets []demoSecret
}

// demoCampaign is one campaign directory under the vault root.
type demoCampaign struct {
	slug string
	dir  string

	root *content.Root

	// kinds is the build's kind set, held per campaign because every page of a
	// campaign resolves against one value and `domain.ResolvePageKind` and
	// `content.NewRenderer` must be handed the same one.
	kinds demoKinds

	pages []demoPage

	// indexPath is the page of kind `index`, and empty when the campaign has none or
	// more than one. The reachability walk is refused in both cases rather than
	// picking one.
	indexPath string

	indexes int
}

// demoVault is the whole tree, loaded.
type demoVault struct {
	campaigns []demoCampaign
}

// demoKinds is the set of page kinds the gate demands, as the content pipeline's own
// interface.
//
// Derived from `plugin.Registry.Kinds()` over **every edition this build ships**,
// which is the one derivation that needs no copy of the composition root's
// `defaultEdition` — and `cmd/server` is `package main`, so no copy of it is even
// possible. A union is the conservative direction: a kind either edition registers is
// a kind the demo must show, so renaming `defaultEdition` cannot change what the gate
// demands, and `TestDemoTheKindsTheGateDemandsAreTheKindsEveryShippedEditionRegisters`
// holds that claim rather than assuming it.
type demoKinds struct {
	known map[string]struct{}
	order []string
}

// HasPageKind implements domain.PageKindRegistry.
//
// Exact match and no trimming, because `domain.ResolvePageKind` is exact and does no
// case folding: a second normaliser here would let the gate honour a kind the pipeline
// degrades to prose, and "the gate saw it, the page did not render it" is the one
// disagreement this whole audit exists to prevent.
func (kinds demoKinds) HasPageKind(name string) bool {
	_, known := kinds.known[name]

	return known
}

// with records one kind and returns the set, keeping first-seen order so the demands a
// finding lists are in registry order rather than map order.
//
// **A function rather than a pointer-receiver method**, because the set is built by
// folding it over a list and a mutating method would mean half the type taking a pointer
// and half a value — which `recvcheck` refuses, and rightly: a value receiver on a
// struct holding a map and a slice is the shape that makes an update invisible to the
// caller.
func (kinds demoKinds) with(name string) demoKinds {
	if _, seen := kinds.known[name]; seen {
		return kinds
	}

	kinds.known[name] = struct{}{}
	kinds.order = append(kinds.order, name)

	return kinds
}

// demoKindsForBuild returns the kinds the gate demands.
func demoKindsForBuild(t *testing.T) demoKinds {
	t.Helper()

	ids := overlays.IDs()
	if len(ids) == 0 {
		// A build with no edition registers no kinds beyond semiplane's five, and
		// the coverage rule would then demand almost nothing — so this is the state
		// where the rule is worth least and the gate must say so rather than quietly
		// demand less.
		t.Fatal("demo: this build ships no edition of any system, so the kind " +
			"coverage rule would demand nothing and pass on an empty vault")
	}

	kinds := demoKinds{known: make(map[string]struct{})}

	for _, id := range ids {
		edition, err := overlays.ByID(id)
		if err != nil {
			t.Fatalf("demo: read edition %s: %v", id, err)
		}

		system, err := edition.System()
		if err != nil {
			t.Fatalf("demo: build edition %s: %v", id, err)
		}

		// Registered exactly the way `cmd/server/systems.go` registers it, because
		// `Kinds()` is the *registry's* answer rather than the pack's and the two are
		// the same set only while the registration is the same.
		registry := plugin.New()
		if err = registry.Register(plugin.Entry{
			System: system,
			Codec:  plugin.PlacementCodec{},
		}); err != nil {
			t.Fatalf("demo: register edition %s: %v", id, err)
		}

		for _, kind := range registry.Kinds() {
			kinds = kinds.with(kind.String())
		}
	}

	return kinds
}

// auditDemoVault runs every rule over root and returns what it found.
//
// `t` is for failures that are the *test's* fault — a tree the test built, a system
// that will not compile. Everything the vault itself can be wrong about comes back as
// a finding, including the case that matters most: **a root that does not exist, or
// holds no campaign, is a finding rather than a pass.** A gate that has nothing to look
// at must say so in the same way it says everything else, because "the gate is green"
// has to mean "the gate looked".
func auditDemoVault(t *testing.T, root string) []demoFinding {
	t.Helper()

	kinds := demoKindsForBuild(t)
	loaded, findings := loadDemoVault(t, root, kinds)

	return auditLoadedVault(t, loaded, kinds, findings)
}

// auditLoadedVault runs the rules over a loaded vault.
//
// Split from `auditDemoVault` so a test can load once and ask questions of the same
// tree — which is how a mutation is run without a second copy of a rule's own logic
// being able to disagree with the first.
func auditLoadedVault(
	t *testing.T,
	loaded *demoVault,
	kinds demoKinds,
	findings []demoFinding,
) []demoFinding {
	t.Helper()

	findings = slices.Clone(findings)

	if len(loaded.campaigns) == 0 {
		// Every rule below that is *about a page* is vacuous here — reachability has
		// no page to fail for, the budget has nothing to be off by — and a gate that
		// returned "no findings" at all would be the silent pass this whole package
		// exists to prevent. So the vault-wide rules still run: kind coverage,
		// extension coverage and the secret states are exactly the claims that fail
		// hardest on an empty vault, and reporting them is what makes a reader of CI
		// output understand that the gate looked and found nothing to look at.
		findings = append(findings, demoFinding{
			rule:   demoRuleVault,
			detail: "the vault root holds no campaign, so nothing was checked",
		})

		findings = append(findings, demoKindFindings(loaded, kinds)...)
		findings = append(findings, demoExtensionFindings(loaded)...)
		findings = append(findings, demoSecretFindings(loaded)...)

		return sortDemoFindings(findings)
	}

	// Every root is closed after the last rule, because a rule may need another
	// campaign's root — a cross-campaign asset reference — and closing them as each
	// campaign finished would close one under a rule still reading it.
	t.Cleanup(func() {
		for at := range loaded.campaigns {
			if loaded.campaigns[at].root != nil {
				_ = loaded.campaigns[at].root.Close()
			}
		}
	})

	// Resolved once, after every campaign has loaded, and from the demo reader's
	// visible set. The order is forced: resolving campaign-by-campaign would make a
	// cross-campaign reference's answer depend on which campaign was visited first.
	visible := demoVisibleCampaigns(loaded)
	resolution := demoResolveVault(loaded, visible)

	for at := range loaded.campaigns {
		campaign := &loaded.campaigns[at]

		findings = append(findings, auditDemoIndex(campaign)...)
		findings = append(findings, auditDemoReachability(campaign, resolution[at])...)
		findings = append(findings, demoAssetFindings(loaded, at, resolution[at])...)
	}

	findings = append(findings, demoKindFindings(loaded, kinds)...)
	findings = append(findings, demoExtensionFindings(loaded)...)
	findings = append(findings, demoSecretFindings(loaded)...)
	findings = append(findings, demoBudgetFindings(loaded, resolution)...)

	return sortDemoFindings(findings)
}

// loadDemoVault opens every campaign directory under root and reads every page.
//
// Two passes, and the order is forced: link resolution needs every campaign's page
// list before it can answer a cross-campaign reference, so the paths are collected
// first and the bytes are read second.
func loadDemoVault(
	t *testing.T,
	root string,
	kinds demoKinds,
) (*demoVault, []demoFinding) {
	t.Helper()

	var findings []demoFinding

	slugs, err := demoCampaignSlugs(root)
	if err != nil {
		return &demoVault{}, []demoFinding{{
			rule:   demoRuleVault,
			detail: "the vault root could not be read: " + err.Error(),
		}}
	}

	loaded := &demoVault{}

	for _, slug := range slugs {
		campaign, campaignFindings := loadDemoCampaign(t, root, slug, kinds)
		findings = append(findings, campaignFindings...)

		if campaign != nil {
			loaded.campaigns = append(loaded.campaigns, *campaign)
		}
	}

	return loaded, findings
}

// demoCampaignSlugs returns every campaign directory name under root, sorted.
//
// A directory rather than a file, because a campaign is a content root and the seed
// opens one `os.Root` per campaign. A regular file at the vault root is
// `demo.manifest.yml` and belongs to another work item, so it is skipped rather than
// read — and skipped by *shape*, which is why the rule is "a directory holding at
// least one page" rather than a list of three names. Three names would be a checklist
// describing campaigns that no longer exist.
func demoCampaignSlugs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}

	var slugs []string

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pages, err := demoPagePaths(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s/%s: %w", root, entry.Name(), err)
		}

		if len(pages) == 0 {
			continue
		}

		slugs = append(slugs, entry.Name())
	}

	slices.Sort(slugs)

	return slugs, nil
}

// demoPagePaths returns every page path under dir, root-relative to dir.
//
// **This duplicates `content`'s indexability rule, and the duplication is deliberate
// and held two ways.** The gate has to find the pages the index would find before it
// can ask anything about them, and the predicates (`skipDirectory`, `indexablePath`)
// are unexported because the index is their only caller. So the rule is restated here —
// a `.md` file, and no component that is a leading dot or `node_modules` — and the
// restatement is held by negative cases rather than assumed:
// `internal/demo/testdata/vault/greyhaven/.obsidian/workspace.md` is a page under a
// dot-directory that the committed fixture contains and the gate must not count, and
// `TestDemoTheAuditSkipsThePagesContentDoesNotIndex` builds a `node_modules` tree in a
// temporary directory and requires the same.
//
// A divergence would not be quiet in one direction. An extra page is a page the
// reachability walk must reach, so the gate goes red; a *missing* page is a page the
// gate does not check — which is why the fixture carries a negative case rather than
// only positive ones.
func demoPagePaths(dir string) ([]string, error) {
	var found []string

	// `filepath.WalkDir` hands the callback paths **joined onto the root it was
	// given**, not root-relative ones — which was a live bug here, found by the
	// fixture rather than by reasoning: every page path came out as
	// `testdata/vault/greyhaven/index.md` and no page was readable. `filepath.Rel`
	// at the top rather than at each callback, and the failure it would have caused
	// is a vault of unreadable pages rather than a subtle wrong answer.
	err := filepath.WalkDir(dir, func(abs string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, relErr := filepath.Rel(dir, abs)
		if relErr != nil {
			return fmt.Errorf("relativise %s: %w", abs, relErr)
		}

		name := entry.Name()

		if entry.IsDir() {
			// `.` is the root itself and matches the leading-dot rule, so it is named
			// out loud here: skipping it would skip the tree.
			if rel != "." && demoSkipDirectory(name) {
				return filepath.SkipDir
			}

			return nil
		}

		if demoIndexablePath(rel) {
			found = append(found, filepath.ToSlash(rel))
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}

	slices.Sort(found)

	return found, nil
}

// demoSkipDirectory reports whether a directory is never a page's parent.
//
// The leading dot rather than a list, because `content`'s own comment says the list
// was always an enumeration of the same thing, and a second copy of a list is a second
// thing to keep in step. `node_modules` is the one component that is not a dot.
func demoSkipDirectory(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}

	return name == "node_modules"
}

// demoIndexablePath reports whether a root-relative path is a page.
//
// Every component rather than the last, and through `demoSkipDirectory` rather than
// the dot rule alone — the same coupling `content.indexablePath` documents, because two
// predicates would be two policies.
func demoIndexablePath(rel string) bool {
	if !strings.HasSuffix(rel, ".md") {
		return false
	}

	for segment := range strings.SplitSeq(rel, "/") {
		if demoSkipDirectory(segment) {
			return false
		}
	}

	return true
}
