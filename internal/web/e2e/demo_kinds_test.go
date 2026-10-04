package e2e_test

// Claim one: every page kind this build registers has a page in the demo vault,
// and that page is served.
//
// # The claim, and why it is derived rather than listed
//
// §10.7 makes `kind` registry-backed, so "which kinds exist" is a property of the
// running process and not a fact any list can hold. `make demo-check` already
// derives it — `internal/demo/demovault_test.go`'s `demoKindsForBuild` reads
// `plugin.Registry.Kinds()` over every edition the build ships — and this file
// makes the **same** derivation for the same reason, with one addition the demo
// gate cannot make.
//
// The addition is a cross-check against the running server. `registerPlugins`
// writes `kinds` into its `plugins registered` line, and that number is the
// product's own answer from §10.5's chain. So:
//
//   - the expectation is derived here, from `overlays.IDs()` and a registry built
//     exactly as `cmd/server/systems.go` builds it; and
//   - the product's answer is read off the boot log of the process under test.
//
// Two derivations that have to agree, one of them the product's. A kind added to a
// pack without a page added to the vault turns this red **and** `make demo-check`
// red, which is the property the task asks for: the same gap, noticed twice, from
// two directions.
//
// **A union over every shipped edition, not this build's one.** `internal/demo`
// states the reason and it is the right one: renaming `defaultEdition` must not
// change what the suite demands, and the server registers exactly one edition
// because both are `dnd5e` and a duplicate id is refused. Asserting the union
// equals the single edition's count is then itself a claim — it is the same one
// `TestDemoTheKindsTheGateDemandsAreTheKindsEveryShippedEditionRegisters` makes,
// made from the other direction.
//
// # What is and is not observable in a served document
//
// **The kind never reaches the response.** `content.Rendered.Kind` is computed and
// then dropped on the floor by `wiki.Handler`: the centre slot is
// `templ.HTMLUnsafe` of the sanitised body and nothing else, and the navigation's
// `chrome.PageNode.Kind` — documented as "resolved by the caller", rendered by
// `nav.templ` as a `nav-kind` badge, and given a rule in `shell.css` — is never
// set by `wiki.wikiTree`, whose intermediate `treeNode` has no field for it. So
// there is no byte in any served document that says "this page is a `spell`".
//
// That is reported, not worked around, and the suite does not pretend to have seen
// a kind where the product does not emit one. What **is** checked:
//
//   - the pipeline resolved the declared kind against the registry, through the
//     product's own `content.Parse` and `domain.ResolvePageKind`, and did not
//     degrade it to prose (S-3.3, S-14.7); and
//   - the page the server sends for that path is that page — its own `<h1>`, its own
//     first heading, and a row in the navigation the server itself rendered.
//
// Together those are "the demo demonstrates this kind and the reader can open it".
// "The kind is visible in the rendered markup" is **not checked here**, because
// nothing renders it. Where it *is* checked: `cmd/server/systems_test.go`'s
// `TestAPageWhoseKindNoPluginRegistersDegradesToProseWithItsContentIntact` holds
// both directions of the registry-backed degradation, and `internal/content`'s own
// tests hold the resolution.
//
// # A demo that demonstrates nothing, stated
//
// The suite below is 13 assertions over 13 kinds because this build registers 13.
// **It is not 13 by construction**, and that is the point: the count is read, the
// per-kind list comes from the registry, and a fourteenth kind arrives as a
// fourteenth assertion or as a failure.

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e/overlays"
	"github.com/semiplane/semiplane/internal/plugin"
)

// demoKinds is the page-kind set a renderer is asked about, and the order the
// registry reported.
//
// Ordered because a failure message listing missing kinds is read by a human, and
// a list that reorders between runs makes one defect look like several.
type demoKinds struct {
	known map[string]bool
	order []string
}

// HasPageKind implements `domain.PageKindRegistry`.
//
// **Exact match and no trimming or case folding**, because `domain.ResolvePageKind`
// is exact and does the same: a second normaliser here would let the suite honour a
// kind the pipeline degrades to prose, and "the suite saw it, the page did not
// render it" is the one disagreement this file exists to prevent.
func (kinds demoKinds) HasPageKind(name string) bool {
	return kinds.known[name]
}

// with records one kind, keeping first-seen order.
func (kinds demoKinds) with(name string) demoKinds {
	if kinds.known[name] {
		return kinds
	}

	kinds.known[name] = true
	kinds.order = append(kinds.order, name)

	return kinds
}

// demoKindsForBuild returns the page kinds this build registers.
//
// **Derived from `overlays.IDs()`, over every edition the build ships**, and
// registered into a `plugin.Registry` exactly as `cmd/server/systems.go` does —
// because `Kinds()` is the *registry's* answer and the pack's own list is a
// different one that happens to agree. `internal/demo/demovault_test.go` derives
// this identically and says why the derivation has to be repeated rather than
// shared: the original is unexported test code in another package, and the two
// copies exist to notice if either drifts from §10.5's chain.
//
// **An empty edition list is a `Fatal`, not an empty answer.** A build that ships
// no edition registers only semiplane's own five kinds, and a suite that demanded
// almost nothing would pass on a vault that demonstrated almost nothing — which is
// the silent pass `internal/demo` writes at length about.
func demoKindsForBuild(t *testing.T) demoKinds {
	t.Helper()

	ids := overlays.IDs()
	if len(ids) == 0 {
		t.Fatal("this build ships no edition of any system, so the kind claim would " +
			"demand nothing and pass on a vault that demonstrates nothing")
	}

	kinds := demoKinds{known: map[string]bool{}}

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
			kinds = kinds.with(kind.String())
		}
	}

	return kinds
}

// demoPage is one page of the copied artefact, as the product's own parser read
// it.
type demoPage struct {
	// campaign is the slug whose vault holds it.
	campaign string

	// rel is the campaign-relative path, extension included, as the index stores it.
	rel string

	// declared is the front matter's `kind:` verbatim, or empty.
	declared string

	// honoured is what `domain.ResolvePageKind` made of it. Equal to `declared` for
	// every page the artefact is supposed to demonstrate, and `prose` for one whose
	// kind this build does not register.
	honoured domain.PageKind

	// heading is the page's first `##` heading's text, or empty.
	heading string
}

// name is what the interface calls the page: its base name, without the extension.
//
// **`pageName`'s own rule**, from `wiki/handler.go`: not the front matter's
// `title:`, which is a second name for one page and would make the `<h1>`
// assertion answer a different question.
func (p demoPage) name() string {
	return strings.TrimSuffix(path.Base(p.rel), ".md")
}

// url is the address this page has inside its campaign.
func (p demoPage) url() string {
	return "/c/" + p.campaign + "/wiki/" + strings.TrimSuffix(p.rel, ".md")
}

// walkDemoVault reads every Markdown page of a vault copy through the product's
// own parser, in path order.
//
// **Path order and campaign order both sorted**, because `internal/demo`'s loader
// is sorted and a suite that picked "the first page of this kind" off an unsorted
// walk would make its choice depend on the filesystem.
func walkDemoVault(t *testing.T, vault demoVaultDir, kinds demoKinds) []demoPage {
	t.Helper()

	var pages []demoPage

	entries, err := readDirSorted(vault.root)
	if err != nil {
		t.Fatalf("read the vault copy at %s: %v", vault.root, err)
	}

	for _, campaign := range entries {
		if !campaign.IsDir() {
			continue
		}

		pages = append(pages, walkDemoCampaign(t, vault, campaign.Name(), kinds)...)
	}

	slices.SortStableFunc(pages, func(first, second demoPage) int {
		if byCampaign := strings.Compare(first.campaign, second.campaign); byCampaign != 0 {
			return byCampaign
		}

		return strings.Compare(first.rel, second.rel)
	})

	return pages
}

// walkDemoCampaign reads one campaign's pages.
func walkDemoCampaign(
	t *testing.T,
	vault demoVaultDir,
	campaign string,
	kinds demoKinds,
) []demoPage {
	t.Helper()

	campaignRoot := filepath.Join(vault.root, campaign)

	var relatives []string

	err := filepath.WalkDir(campaignRoot, func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			return nil
		}

		relatives = append(relatives, strings.TrimPrefix(
			strings.TrimPrefix(current, campaignRoot), string(filepath.Separator),
		))

		return nil
	})
	if err != nil {
		t.Fatalf("walk %s in the vault copy: %v", campaign, err)
	}

	slices.Sort(relatives)

	pages := make([]demoPage, 0, len(relatives))

	for _, rel := range relatives {
		raw, readErr := os.ReadFile(filepath.Join(campaignRoot, rel))
		if readErr != nil {
			t.Fatalf("read %s/%s from the vault copy: %v", campaign, rel, readErr)
		}

		// **The product's own parser**, so the declared kind and the honoured kind
		// are the two the pipeline computed rather than two this file re-derived.
		document := content.Parse(raw, kinds)

		pages = append(pages, demoPage{
			campaign: campaign,
			rel:      filepath.ToSlash(rel),
			declared: document.FrontMatter.DeclaredKind,
			honoured: document.FrontMatter.Kind,
			heading:  firstHeading(string(raw)),
		})
	}

	return pages
}

// firstHeading is a page's first `##` heading, or empty.
//
// Chosen over "the first line after the front matter" because a heading is the one
// piece of every demo page that is (a) rendered as its own element, (b) not the
// `<h1>`, whose text is the page's *name* rather than anything the author wrote as
// a heading, and (c) not transformed by the renderer, so the needle survives into
// the response byte for byte.
//
// Every page of every registered kind in the artefact opens with one, which is
// checked rather than assumed: a page with no `##` heading fails the assertion that
// its heading is served, and the failure says so.
func firstHeading(source string) string {
	inFrontMatter := strings.HasPrefix(source, "---\n")

	for line := range strings.SplitSeq(source, "\n") {
		trimmed := strings.TrimRight(line, "\r")

		if inFrontMatter {
			if strings.TrimSpace(trimmed) == "---" {
				inFrontMatter = false
			}

			continue
		}

		if rest, isHeading := strings.CutPrefix(trimmed, "## "); isHeading {
			return strings.TrimSpace(rest)
		}
	}

	return ""
}

// TestTheKindSetTheBuildRegistersIsTheKindSetTheSuiteDemands is the two-derivations
// check, and it is what makes the rest of this file's list trustworthy.
//
// One number from the product's own boot line and one derived here, required to be
// equal. Either alone would be satisfiable by a constant:
//
//   - a hardcoded `13` would pass while §10.5's chain registered a fourteenth kind
//     and `demo-check` went red with no other signal; and
//   - a derivation that read the *server's* number would be circular, and would
//     pass with a registry that registered nothing at all.
func TestTheKindSetTheBuildRegistersIsTheKindSetTheSuiteDemands(t *testing.T) {
	boot := sharedDemo(t)

	kinds := demoKindsForBuild(t)

	registered := boot.logCarrying(t, "plugins registered")
	if len(registered) != 1 {
		t.Fatalf("the server logged %d `plugins registered` lines, want exactly 1. "+
			"That line is §10.5's chain reporting what this binary resolved under, "+
			"and there is one registration per process — several would mean something "+
			"registered twice.\n%s", len(registered), boot.rawLog())
	}

	if got := registered[0].Kinds; got != len(kinds.order) {
		t.Errorf("this build registers %d page kinds and the suite derived %d %v. "+
			"Either §10.5's chain and `overlays.IDs()` disagree — which is the claim "+
			"`internal/demo` makes and this file re-checks from the other direction — "+
			"or this suite's derivation has drifted from the registration",
			got, len(kinds.order), kinds.order)
	}
}

// TestEveryRegisteredKindHasAPageThatTheServerServes is claim one.
//
// **Four assertions per kind, and each answers a different question:**
//
//  1. the set of kinds the artefact *demonstrates* equals the set this build
//     registers — in both directions, so a vault that grew a page of a kind this
//     build cannot render is as red as one missing a kind it can;
//  2. the page's declared kind survived resolution and did not degrade to prose
//     (S-3.3, S-14.7) — the assertion that would fail if the registry a renderer is
//     handed stopped knowing the kind;
//  3. the served document is that page: its `<h1>` is the page's own name, its body
//     carries the page's own first heading as a heading, and the campaign navigation
//     the server rendered has a row for its address; and
//  4. `ETag` differs per campaign, which is the cheapest proof that the page came
//     from that campaign's root rather than from a cache or a fixture.
func TestEveryRegisteredKindHasAPageThatTheServerServes(t *testing.T) {
	boot := sharedDemo(t)

	kinds := demoKindsForBuild(t)
	pages := walkDemoVault(t, boot.vault, kinds)

	demonstrated := map[string]bool{}

	for _, page := range pages {
		if page.declared == "" {
			continue
		}

		demonstrated[page.declared] = true
	}

	registered := map[string]bool{}
	for _, name := range kinds.order {
		registered[name] = true
	}

	if missing := demoSubtracted(registered, demonstrated); len(missing) > 0 {
		t.Errorf("this build registers %d page kinds and the demo vault demonstrates "+
			"%d of them. No page declares %v, so the demo shows nothing of them and "+
			"`make demo-check` is reporting the same gap from the other direction. "+
			"A kind with no page is a kind a first-time operator never meets",
			len(kinds.order), len(demonstrated), missing)
	}

	if extra := demoSubtracted(demonstrated, registered); len(extra) > 0 {
		t.Errorf("the demo vault declares %v, which this build does not register, so "+
			"those pages render as prose rather than as the game objects their authors "+
			"wrote them to be (S-3.3). Every kind in the vault must be one this "+
			"build's registry knows", extra)
	}

	for _, name := range kinds.order {
		t.Run(name, func(t *testing.T) {
			page, found := firstPageOfKind(pages, name)
			if !found {
				// Already reported above, with the whole list. Skipping here would be
				// the silent pass this suite is written against, so it fails: a
				// subtest that reports nothing for a kind nothing demonstrates is a
				// green tick over a missing page.
				t.Fatalf("no page in the vault declares kind %q", name)
			}

			if page.honoured != domain.PageKind(name) {
				t.Errorf("%s declares kind %q and the pipeline honoured %q. S-3.3 "+
					"degrades an unregistered kind to prose, so this page is prose in "+
					"this build — which means the registry a renderer is handed does "+
					"not know a kind this suite's derivation says it does",
					page.url(), name, page.honoured)
			}

			document := boot.requireDocument(t, boot.gm, boot.base+page.url())

			heading := demoRequireTestID(t, document, "page-title")
			if got := demoFold(strings.TrimSpace(demoText(heading))); got != page.name() {
				t.Errorf("the served document's heading reads %q, want %q. §7.2 puts "+
					"the page's own name there and `wiki.pageName` refuses the front "+
					"matter's `title:` — a wiki that had two names for one page would "+
					"have a link label and a heading that disagree",
					got, page.name())
			}

			body := demoRequireTestID(t, document, "page-body")

			if page.heading == "" {
				t.Fatalf("%s has no `##` heading, so this suite has no needle for "+
					"its body. Every page of every registered kind in the artefact "+
					"opens with one; a page that does not is a page whose body this "+
					"suit cannot check", page.rel)
			}

			served := demoHeadings(body)

			if !slices.Contains(served, demoFold(page.heading)) {
				t.Errorf("the served body carries no %q heading. It has %v. So the "+
					"response is not this page's body, or the author wrote the heading "+
					"in a form the renderer did not carry",
					page.heading, served)
			}

			assertNavigationOffers(t, document, page)
		})
	}
}

// firstPageOfKind is the first page, in walk order, declaring this kind.
//
// Deterministic because `walkDemoVault` sorts, and the choice is stated rather than
// left to whichever page the filesystem offered first — which would make a failure
// message depend on the machine.
func firstPageOfKind(pages []demoPage, kind string) (demoPage, bool) {
	for _, page := range pages {
		if page.declared == kind {
			return page, true
		}
	}

	return demoPage{}, false
}

// firstKindPageOfCampaign is the first page of one campaign that declares a kind.
//
// For the fixtures that need *a* page of a registered kind rather than a named
// one, and which must be in a named campaign so the request is made by a reader
// entitled to it.
func firstKindPageOfCampaign(pages []demoPage, campaign string) (demoPage, bool) {
	for _, page := range pages {
		if page.campaign == campaign && page.declared != "" {
			return page, true
		}
	}

	return demoPage{}, false
}

// assertNavigationOffers requires the campaign navigation the server rendered to
// have a row for this page's own address.
//
// **`data-page` and not the row's text.** The row's label is the page's name and
// two pages in a vault can share one; `data-page` is the address, which is the fact
// under test. And the navigation is built by `wiki.wikiTree` from the same listing
// the route used to resolve this page's references, so a row here is evidence that
// the startup index ran — a missing row is what a request against an unindexed
// campaign looks like, with the links still rendering.
func assertNavigationOffers(t *testing.T, document demoDocument, page demoPage) {
	t.Helper()

	address := page.url()

	for _, node := range demoElementsWithAttr(document.root, "data-page", address) {
		if node.Data == "li" {
			return
		}
	}

	t.Errorf("the served document's navigation has no row for %s. The tree is built "+
		"from the `pages` table the startup index fills, so a missing row is what a "+
		"request against an unindexed campaign looks like — except that the page "+
		"itself rendered, which is the shape where only the reference resolution and "+
		"the navigation disagree", address)
}

// demoHeadings is a subtree's `<h2>` texts, in document order.
//
// Folded, because a heading is prose and goldmark's typographer rewrites prose —
// `demoFold` says which characters and why.
func demoHeadings(node *html.Node) []string {
	var found []string

	eachElement(node, func(current *html.Node) {
		if current.Data == "h2" {
			found = append(found, demoFold(strings.TrimSpace(demoText(current))))
		}
	})

	return found
}

// TestTheKindBadgeIsNotRenderedBecauseNoCallerProducesIt is a **report**, written as
// a test so it cannot rot.
//
// UI §4.11.3 asks the navigation for each page's kind as a badge. `nav.templ`
// renders one (`<span class="nav-kind">`), `shell.css` styles it, and
// `chrome.PageNode.Kind` is documented as "resolved by the caller". **No caller
// resolves it**: `wiki.wikiTree` builds a `treeNode` with `title` and `href` and no
// kind at all, and `chromeNodes` copies those two fields across. `domain.Page` — the
// listing the tree is built from — carries `Kind`, read by `store`, and nothing reads
// it.
//
// The assertion below is therefore that **no served page carries the badge**, which
// is the opposite of what §4.11.3 asks for. It is here for two reasons:
//
//   - it is a fact about the shipped product that a reader of this suite should not
//     have to take on trust, and a test is where this repository puts facts; and
//   - the day somebody wires it, this test goes red and says what changed, which is
//     the direction a fact should fail in.
//
// **It is not a defect this work item can fix.** `wiki/nav.go` and
// `components/chrome` are not its paths. The fix is two fields and one assignment,
// and the report names them.
func TestTheKindBadgeIsNotRenderedBecauseNoCallerProducesIt(t *testing.T) {
	boot := sharedDemo(t)

	kinds := demoKindsForBuild(t)
	pages := walkDemoVault(t, boot.vault, kinds)

	page, found := firstKindPageOfCampaign(pages, showcaseSlug)
	if !found {
		t.Fatal("the showcase campaign declares no page kind at all, so the absence " +
			"of a badge would prove nothing about chrome.PageNode.Kind")
	}

	document := boot.requireDocument(t, boot.gm, boot.base+page.url())

	badges := demoElementsWithAttr(document.root, "class", "nav-kind")
	if len(badges) > 0 {
		t.Fatalf("the navigation now renders %d `nav-kind` badges. UI §4.11.3 asks "+
			"for one, `nav.templ` renders one and `shell.css` styles one, so this is "+
			"the expected state — which means somebody wired `chrome.PageNode.Kind` "+
			"up since this test was written. Update it: the badge's text is the "+
			"registry's label and the assertion should now be that the label is the "+
			"page's kind rather than that the badge is absent",
			len(badges))
	}

	// The negative half, and the reason the test is not a constant: the page *is* of
	// a registered kind, so "no badge" cannot be explained by the page being prose.
	if page.declared == "" {
		t.Fatalf("%s declares no kind, so the absence of a badge proves nothing "+
			"about `chrome.PageNode.Kind`", page.url())
	}
}
