package demo_test

// The six rules. Each one asks the build or the vault a question; none of them
// names a page, a kind, an extension or a count.
//
// Every rule returns findings rather than failing, and the caller prints them. That
// is the difference between a gate that says what is wrong and one that says
// "something is wrong", and a demo gate's whole job is to be the former: the vault
// is authored *against* this gate, so every finding is an instruction to a writer.

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/content/ext"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
)

// demoIsUnresolvedPageLink reports whether a reference is an unresolved **page**
// link, which is the whole of what the budget counts.
//
// Refused and unresolved are both included, and they are different faults: a
// refusal is a malformed reference and an unresolved one names a page that is not
// there. `content.Record.Refusal` separates them in the finding.
//
// The two `{{…}}` extensions are excluded because `Records` refuses them as
// unsupported — they name a game object and an expression, not a path — and
// counting them would put every statblock and every dice expression in the budget.
func demoIsUnresolvedPageLink(one demoResolved) bool {
	if one.ref.Extension != ext.KindWikilink && one.ref.Extension != ext.KindEmbed {
		return false
	}

	if one.link.Kind != content.LinkToPage {
		return false
	}

	if one.record.Resolved {
		return false
	}

	// An anchor-only reference names this page and no target at all; there is
	// nothing for it to fail to resolve.
	return one.record.Reference != ""
}

// demoUnresolvedDetail says what went wrong with one undeclared reference, without
// printing a target that came from inside a secret.
//
// **Resolution is the product's, twice, and never a scan.** `content.Resolver.Link`
// answers what a reference *means* — whether it names a page or an asset, whether
// it was refused, and why — and `Records` answers whether the reader may reach it,
// which is a different question with a different answer per viewer. Using only one
// of them is the mistake `links.go`'s own header names: the markup cannot encode
// viewer-dependent resolution (ADR 0017), so the record is the only place the
// answer to "is this link broken" can live.
type demoResolved struct {
	ref    content.Reference
	link   content.Link
	record content.Outbound
}

// demoResolution is one page's references, in document order.
type demoResolution []demoResolved

// demoVaultResolution is a campaign's pages' resolutions, parallel to
// `demoCampaign.pages`.
type demoVaultResolution []demoResolution

// demoVisibleCampaigns is the demo reader's visible set: every campaign in the
// vault.
//
// **The GM's view, and it is a claim.** The demo's reader signs in as the demo GM
// and the broken-link report they would see is the GM's, so the gate resolves for
// the GM. It is an argument rather than something derived, because `Records` takes
// the visible set as an input on purpose (S-5.5: never probe a campaign the viewer
// cannot see, and the type cannot enforce that), and
// `TestDemoTheBudgetIsResolvedForTheDemoReader` holds which set is passed.
func demoVisibleCampaigns(loaded *demoVault) []content.VisibleCampaign {
	visible := make([]content.VisibleCampaign, 0, len(loaded.campaigns))

	for at := range loaded.campaigns {
		visible = append(visible, content.VisibleCampaign{
			Slug:  loaded.campaigns[at].slug,
			Index: content.NewPageIndex(demoDomainPages(&loaded.campaigns[at])),
		})
	}

	return visible
}

// demoResolveVault resolves every reference in the vault, once.
//
// After every campaign has loaded, and never before: a cross-campaign reference
// resolves against the visible set, so resolving campaign-by-campaign would make
// the answer depend on which campaign happened to be visited first.
func demoResolveVault(
	loaded *demoVault,
	visible []content.VisibleCampaign,
) []demoVaultResolution {
	out := make([]demoVaultResolution, 0, len(loaded.campaigns))

	for at := range loaded.campaigns {
		campaign := &loaded.campaigns[at]

		perPage := make(demoVaultResolution, len(campaign.pages))

		resolver := content.NewResolver(campaign.root, demoDomainPages(campaign))

		for index := range campaign.pages {
			page := &campaign.pages[index]

			pageOrigin, err := content.NewOrigin(page.rel)
			if err != nil {
				perPage[index] = demoResolution{}

				continue
			}

			links := resolver.Links(pageOrigin, page.refs)
			records := resolver.Records(pageOrigin, page.refs, visible)

			perPage[index] = make(demoResolution, 0, len(page.refs))

			for at := range page.refs {
				perPage[index] = append(perPage[index], demoResolved{
					ref:    page.refs[at],
					link:   links[at],
					record: records[at],
				})
			}
		}

		out = append(out, perPage)
	}

	return out
}

// demoDomainPages builds the rows `content.PageIndex` is built from.
//
// `domain.Page` is what `store.PagesForCampaign` returns, and one row per page is
// the documented way to construct an index without a database handle. The rows
// carry the **honoured** kind and the declared title, which is what the index
// stores (ADR 0027 keeps no front-matter copy), so a link resolving to a page
// resolves to the page the wiki route would serve.
func demoDomainPages(campaign *demoCampaign) []domain.Page {
	pages := make([]domain.Page, 0, len(campaign.pages))

	for index := range campaign.pages {
		page := &campaign.pages[index]

		pages = append(pages, domain.Page{
			Path:  page.rel,
			Kind:  page.kind,
			Title: page.title,
		})
	}

	return pages
}

// auditDemoIndex reports a campaign with no index page, or with more than one.
//
// **Zero is the finding that matters**, because it is the one that takes the
// reachability rule down with it: with no index there is no front page, the walk
// has no root, and every page of the campaign would otherwise be unreachable — one
// cause reported nine times.
func auditDemoIndex(campaign *demoCampaign) []demoFinding {
	switch campaign.indexes {
	case 1:
		return nil
	case 0:
		return []demoFinding{{
			rule:     demoRuleIndex,
			campaign: campaign.slug,
			detail: "no page declares kind `" + rules.KindIndex.String() +
				"`, so the campaign has no front page and nothing is reachable",
		}}
	default:
		paths := make([]string, 0, campaign.indexes)

		for index := range campaign.pages {
			if campaign.pages[index].kind == domain.PageKind(rules.KindIndex) {
				paths = append(paths, campaign.pages[index].rel)
			}
		}

		return []demoFinding{{
			rule:     demoRuleIndex,
			campaign: campaign.slug,
			detail: fmt.Sprintf(
				"%d pages declare kind `%s` (%s), so the campaign has no single "+
					"front page for the tutorial to start from",
				campaign.indexes, rules.KindIndex, strings.Join(paths, ", "),
			),
		}}
	}
}

// auditDemoReachability reports every page of a campaign that no chain of
// wikilinks from that campaign's index reaches.
//
// The walk follows **resolved, same-campaign page references only**, and the three
// exclusions are each a claim:
//
//   - A cross-campaign edge cannot make a page reachable *from this* campaign's
//     index. The vault is three campaigns and each index is the front page of its
//     own, so a page only linked from another campaign is a genuinely unreachable
//     page in this one.
//   - A refused reference has no address, so following it reaches nothing.
//   - An unresolved reference reaches nothing, and **that is the demonstration**:
//     `notes/broken-links.md` is reachable, and the three references inside it that
//     go nowhere are not edges. A walk that counted an unresolved reference as an
//     edge would report a page reachable that no reader can get to.
//
// Breadth-first over a sorted frontier, so the order findings come out in does not
// depend on which page was visited first.
func auditDemoReachability(
	campaign *demoCampaign,
	resolution demoVaultResolution,
) []demoFinding {
	if campaign.indexes != 1 {
		// Reported by `auditDemoIndex`, and walking from nothing would turn one
		// cause into one finding per page.
		return nil
	}

	edges := make(map[string][]string, len(campaign.pages))

	for at := range campaign.pages {
		page := &campaign.pages[at]

		for oneAt := range resolution[at] {
			target, ok := demoSameCampaignTarget(campaign, resolution[at][oneAt])
			if !ok {
				continue
			}

			edges[page.rel] = append(edges[page.rel], target)
		}
	}

	reachable := map[string]struct{}{campaign.indexPath: {}}

	frontier := []string{campaign.indexPath}

	for len(frontier) > 0 {
		var next []string

		for _, from := range frontier {
			for _, to := range edges[from] {
				if _, seen := reachable[to]; seen {
					continue
				}

				reachable[to] = struct{}{}
				next = append(next, to)
			}
		}

		slices.Sort(next)
		frontier = next
	}

	var findings []demoFinding

	for index := range campaign.pages {
		page := &campaign.pages[index]

		if _, found := reachable[page.rel]; found {
			continue
		}

		detail := "no chain of resolved wikilinks from the campaign index reaches it"

		if len(edges[campaign.indexPath]) == 0 {
			detail = "the campaign index reaches nothing at all"
		}

		findings = append(findings, demoFinding{
			rule:     demoRuleReachable,
			campaign: campaign.slug,
			page:     page.rel,
			detail:   detail,
		})
	}

	return findings
}

// demoSameCampaignTarget returns the root-relative path a reference reaches inside
// its own campaign, and whether it reaches one.
//
// Three conditions, and each is a reading of `content`: the reference must have
// resolved (`record.Resolved`), it must have resolved into the home campaign
// (`record.TargetCampaign == campaign.slug`), and it must have resolved to a page
// rather than an asset (`link.Kind == content.LinkToPage`). The third is what keeps
// an embed of a map from counting as a page edge — a map is an asset and there is
// no page to reach.
func demoSameCampaignTarget(campaign *demoCampaign, one demoResolved) (string, bool) {
	if !one.record.Resolved || one.record.TargetCampaign != campaign.slug {
		return "", false
	}

	if one.link.Kind != content.LinkToPage {
		return "", false
	}

	return one.record.TargetPath + ".md", true
}

// demoAssetFindings reports every asset a page references that is not in the vault.
//
// An asset's existence is **not** a link-resolution question, and `content`
// documents why: `PageIndex` holds pages, because `store.PagesForCampaign` returns
// pages, so an asset reference comes back `Broken` whether the file is there or not
// — and a budget that counted assets would be counting every embed in the vault. So
// the answer comes from the file system, through the same `content.Root` that backs
// `/c/{slug}/assets/…`.
//
// The address the resolver built is parsed rather than the author's target
// re-normalised, which means the check also holds the address: a reference whose
// href does not name an asset segment is not an asset reference, and is left to the
// budget.
func demoAssetFindings(
	loaded *demoVault,
	campaignAt int,
	resolution demoVaultResolution,
) []demoFinding {
	campaign := &loaded.campaigns[campaignAt]

	var findings []demoFinding

	for at := range campaign.pages {
		page := &campaign.pages[at]

		for oneAt := range resolution[at] {
			one := resolution[at][oneAt]

			if one.link.Kind != content.LinkToAsset || one.link.Href == "" {
				continue
			}

			findings = append(findings,
				demoAssetFinding(loaded, campaign, *page, one)...)
		}
	}

	return findings
}

// demoAssetFinding is one asset reference's existence check, as zero or more
// findings.
//
// Cross-campaign assets are the reason this returns a slice: a reference into
// another campaign is answered from **that** campaign's root, and if the vault does
// not hold that campaign there is no root to ask. The plan's deliberate breakage is
// unresolved *links*, and an unresolved asset is never one of them — an asset
// reference that 404s is a defect, and putting it in the budget would make it
// indistinguishable from the three links the showcase teaches.
func demoAssetFinding(
	loaded *demoVault,
	campaign *demoCampaign,
	page demoPage,
	one demoResolved,
) []demoFinding {
	slug, section, rel, ok := demoParseHref(one.link.Href)
	if !ok || section != "assets" {
		return nil
	}

	detail := func(reason string) demoFinding {
		if page.holdsSecret(one.ref.Line) {
			reason = demoRedactTarget()
		}

		return demoFinding{
			rule:     demoRuleAsset,
			campaign: campaign.slug,
			page:     page.rel,
			line:     one.ref.Line,
			detail:   reason,
		}
	}

	owner := loaded.campaignBySlug(slug)
	if owner == nil {
		return []demoFinding{detail(
			"the page references an asset in campaign `" + slug +
				"`, which is not one of this vault's campaigns, so it resolves to nothing",
		)}
	}

	if _, err := demoStatAsset(owner, rel); err != nil {
		return []demoFinding{detail(
			"the page references `" + rel + "` in campaign `" + slug +
				"` and it is not there: " + err.Error(),
		)}
	}

	return nil
}

// campaignBySlug returns the campaign with this slug, or nil.
//
// Linear over a handful of campaigns in slug order, rather than an index: the vault
// is three directories and this is asked once per asset reference, so the index
// would be a second structure to keep in step for a lookup that never misses a
// cache.
func (loaded *demoVault) campaignBySlug(slug string) *demoCampaign {
	for at := range loaded.campaigns {
		if loaded.campaigns[at].slug == slug {
			return &loaded.campaigns[at]
		}
	}

	return nil
}

// demoStatAsset stats a root-relative asset path through a campaign's content root.
func demoStatAsset(campaign *demoCampaign, rel string) (any, error) {
	target, err := campaign.root.At(rel)
	if err != nil {
		return nil, fmt.Errorf("open the asset path %s: %w", rel, err)
	}

	info, err := target.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat the asset path %s: %w", rel, err)
	}

	return info, nil
}

// demoParseHref splits `/c/{slug}/{section}/{escaped path}` into its three parts.
//
// The **address** rather than the author's target, for the reason `demoAssetFindings`
// gives. `url.PathUnescape` because `AssetHref` escapes every path segment, and an
// asset whose name holds a space or a `%` is one whose escaped and unescaped
// spellings differ — comparing the unescaped one against a root-relative path is
// what makes `maps/grey haven.png` findable at all.
func demoParseHref(href string) (slug, section, rel string, ok bool) {
	trimmed := strings.TrimPrefix(href, "/c/")

	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}

	unescaped, err := url.PathUnescape(parts[2])
	if err != nil {
		return "", "", "", false
	}

	return parts[0], parts[1], unescaped, true
}

// demoRedactTarget is what a finding says when the reference it is about sits inside a
// `[!secret]` callout.
//
// **It takes no argument, and that is the point.** The detail it replaces carried the
// reference's target, and the target is author text inside a secret — so a replacement
// built from what it replaces would be re-deriving the thing being withheld. A function
// that ignored its parameter would be a linter finding and a design smell for the same
// reason: the parameter is an invitation to use it.
func demoRedactTarget() string {
	return "the reference is inside a [!" + demoSecretCalloutName() + "] callout, " +
		"so nothing about it is printed"
}

// demoSecretCalloutName is the callout keyword the scanner looks for.
//
// Spelled here rather than read from `content`, whose `calloutPrefix` is
// unexported, and it is the one string in this file that duplicates the product's
// vocabulary. It is in a *redaction* path — the sentence that replaces a target
// when the target is secret — so it cannot affect any answer, only what a finding
// says when it has already refused to say anything else.
// `TestDemoTheRedactionMarkerIsTheProductsOwn` holds the spelling against the scanner
// by requiring a callout written with this marker to be found.
func demoSecretCalloutName() string { return "secret" }

// demoKindFindings reports every registered kind no page demonstrates.
//
// **The requirement list is the registry's**, walked through `plugin.Registry.Kinds()`
// for every edition the build ships — see `demoKindsForBuild`. A kind added to a
// plugin's data pack therefore turns the gate red until the vault demonstrates it,
// which is the whole of D9's "adding a kind to a plugin turns the demo red", and it
// happens with no edit to this file.
//
// What is compared is the **honoured** kind, which `loadDemoPage` takes from what the
// renderer resolved rather than from the front matter, so a page declaring a kind this
// build does not know does not count: it renders as prose, and a gate counting the
// declaration would report a demonstration the reader cannot see.
func demoKindFindings(loaded *demoVault, kinds demoKinds) []demoFinding {
	shown := make(map[string]struct{})

	for at := range loaded.campaigns {
		for index := range loaded.campaigns[at].pages {
			shown[string(loaded.campaigns[at].pages[index].kind)] = struct{}{}
		}
	}

	var findings []demoFinding

	for _, kind := range kinds.order {
		if _, present := shown[kind]; present {
			continue
		}

		findings = append(findings, demoFinding{
			rule: demoRuleKind,
			detail: "no page in the vault declares kind `" + kind + "`, and this " +
				"build registers it, so the demo does not show it",
		})
	}

	return findings
}

// demoExtensionFindings reports every installed render extension no page exercises.
//
// The requirement list is `ext.Builtins()` — **what the pipeline installs**, not what
// this file remembers — and the evidence is what the pipeline *reported*: a page
// counts as having used `{{dice:…}}` because the render collector handed back a
// reference whose `Extension` is `ext.KindDice`, not because its bytes contain the
// characters. That is the difference between a gate that survives an extension being
// renamed and one that silently stops covering it, and it is why this rule needs no
// knowledge of any extension's grammar.
func demoExtensionFindings(loaded *demoVault) []demoFinding {
	used := make(map[ext.Kind]struct{})

	for at := range loaded.campaigns {
		for index := range loaded.campaigns[at].pages {
			page := &loaded.campaigns[at].pages[index]

			for _, ref := range page.refs {
				used[ref.Extension] = struct{}{}
			}
		}
	}

	var findings []demoFinding

	for _, definition := range ext.Builtins() {
		if _, present := used[definition.Kind]; present {
			continue
		}

		findings = append(findings, demoFinding{
			rule: demoRuleExtension,
			detail: "no page exercises the `" + definition.Kind.String() +
				"` extension, which the render pipeline installs",
		})
	}

	return findings
}

// demoSecretStates is every state a `[!secret]` callout can be in.
//
// Two values, and they are the product's own constants rather than a pair this file
// picked — but the pair being *complete* is a claim, not a fact about reading the
// constants, and `TestEverySecretStateTheScannerCanReportIsDemstrated` holds it by
// requiring a third marker byte to be no callout at all.
func demoSecretStates() []content.SecretState {
	return []content.SecretState{content.SecretCollapsed, content.SecretRevealed}
}

// demoSecretFindings reports every secret state no page demonstrates.
//
// **Both directions, and that is the whole rule.** A vault showing only collapsed
// secrets demonstrates a page with nothing in it; a vault showing only revealed
// ones demonstrates a page with nothing to hide. Either is a demo of the mistake
// rather than of the feature, and the state is read from the file by
// `content.ScanSecrets` rather than by counting `-` and `+` characters — a `+` in a
// code fence is not a reveal.
func demoSecretFindings(loaded *demoVault) []demoFinding {
	shown := make(map[content.SecretState]struct{})

	for at := range loaded.campaigns {
		for index := range loaded.campaigns[at].pages {
			for _, secret := range loaded.campaigns[at].pages[index].secrets {
				shown[secret.state] = struct{}{}
			}
		}
	}

	var findings []demoFinding

	for _, state := range demoSecretStates() {
		if _, present := shown[state]; present {
			continue
		}

		findings = append(findings, demoFinding{
			rule: demoRuleSecret,
			detail: fmt.Sprintf(
				"no page carries a `[!%s]%s` callout, so the vault never shows a secret in that state",
				demoSecretCalloutName(),
				state,
			),
		})
	}

	return findings
}

// demoBudgetFindings reports every disagreement between the unresolved references
// and the ones the vault declared.
//
// **Two directions, and either alone would be satisfiable by a constant.** An
// unresolved reference with no declaration is an accidental breakage; a declaration
// that no longer matches anything is a demonstration somebody has repaired by
// accident. Both are findings, and the comparison is a multiset over
// `(page, target-as-written)` so two identical broken targets on one page count
// twice and a declaration listing one of them twice is also a mismatch.
//
// Assets are excluded, and the reason is `PageIndex`'s: it holds pages, so every
// asset reference is `Broken` whether or not the file exists, and counting them
// would make the budget a count of embeds. `demoAssetFindings` answers for assets
// against the file system instead.
func demoBudgetFindings(loaded *demoVault, resolution []demoVaultResolution) []demoFinding {
	declared := make(map[demoReference]int)

	for at := range loaded.campaigns {
		campaign := &loaded.campaigns[at]

		for index := range campaign.pages {
			page := &campaign.pages[index]

			for _, target := range declaredBrokenLinks(page.doc) {
				declared[demoReference{page: page.rel, target: target}]++
			}
		}
	}

	var findings []demoFinding

	actual := make(map[demoReference]int)

	for campaignAt := range loaded.campaigns {
		campaign := &loaded.campaigns[campaignAt]

		for at := range campaign.pages {
			page := &campaign.pages[at]

			for oneAt := range resolution[campaignAt][at] {
				one := resolution[campaignAt][at][oneAt]

				if !demoIsUnresolvedPageLink(one) {
					continue
				}

				key := demoReference{page: page.rel, target: one.record.Reference}

				actual[key]++

				if declared[key] > 0 {
					continue
				}

				findings = append(findings, demoFinding{
					rule:     demoRuleBudget,
					campaign: campaign.slug,
					page:     page.rel,
					line:     one.ref.Line,
					detail:   demoUnresolvedDetail(one, key.target, *page),
				})
			}
		}
	}

	for key, count := range declared {
		if actual[key] >= count {
			continue
		}

		findings = append(findings, demoFinding{
			rule:     demoRuleBudget,
			campaign: loaded.campaignOfPage(key.page),
			page:     key.page,
			detail: fmt.Sprintf(
				"the vault declares %s deliberate breakage that resolves, so the "+
					"demonstration has been repaired by accident and the budget is "+
					"counting a link that works", demoQuote(key.target),
			),
		})
	}

	return findings
}

// demoReference identifies one reference by the page that wrote it and the target
// as the author wrote it.
//
// **The author's bytes and not the resolved path**, because `content.Record.Reference`
// is documented as verbatim-and-un-normalised for exactly this reason: a report tells
// an author what they typed. Matching a declaration against a normalised path would
// mean the declaration had to spell it the normalised way, which is the one spelling
// the vault author would get wrong.
type demoReference struct {
	page   string
	target string
}

// demoUnresolvedDetail says what went wrong with one undeclared reference, without
// printing a target that came from inside a secret.
//
// The redaction test is against **the referring page**, and that is a correction rather
// than a preference: an earlier version asked whether *any* page of the campaign had a
// callout covering that line number, on the reasoning that suppressing is the safe
// direction. It is not — line numbers are per page, so a campaign whose `secrets.md` has
// a callout at line 12 redacted every finding anywhere in the campaign that happened to
// land on line 12. The fixture caught it: a repaired-declaration finding on
// `notes/broken-links.md:12` came back saying "inside a secret callout" about a link
// that is not inside one, and the finding had stopped being actionable.
func demoUnresolvedDetail(
	one demoResolved,
	target string,
	page demoPage,
) string {
	if page.holdsSecret(one.ref.Line) {
		return demoRedactTarget()
	}

	if one.record.Refusal != content.Allowed {
		return fmt.Sprintf(
			"the reference %s was refused (%s) and the vault does not declare it "+
				"deliberate", demoQuote(target), one.record.Refusal,
		)
	}

	return fmt.Sprintf(
		"the reference %s resolves to no page and the vault does not declare it "+
			"deliberate; add it to the page's front matter under demo.broken if it is",
		demoQuote(target),
	)
}

// declaredBrokenLinks returns the deliberate breakage a page declares.
//
// **The one declaration the gate reads, and the one place the manifest should
// eventually be consulted instead** (V2 puts per-campaign declared intent in
// `demo.manifest.yml`, which belongs to another work item). It is behind a function
// so that wiring it is this body's replacement rather than a search: a second
// reader of the same front matter would be a second answer to "what does this page
// say is broken".
//
// Reads `demo.broken` and nothing else under `demo:`. A value of the wrong shape is
// ignored rather than reported, for the reason the pipeline ignores an unknown
// front-matter key (S-3.3): this is author input, the vault is authored against
// this gate, and a mistyped declaration that made the gate red for an unparseable
// reason would be worse than one the budget rule then reports as a mismatch.
func declaredBrokenLinks(doc content.Document) []string {
	demo, ok := doc.FrontMatter.Fields["demo"].(map[string]any)
	if !ok {
		return nil
	}

	values, ok := demo["broken"].([]any)
	if !ok {
		return nil
	}

	targets := make([]string, 0, len(values))

	for _, value := range values {
		target, isString := value.(string)
		if !isString {
			continue
		}

		targets = append(targets, target)
	}

	return targets
}

// campaignOfPage returns the campaign slug holding a page, for a finding built from
// a declaration rather than from a walk.
func (loaded *demoVault) campaignOfPage(page string) string {
	for at := range loaded.campaigns {
		for index := range loaded.campaigns[at].pages {
			if loaded.campaigns[at].pages[index].rel == page {
				return loaded.campaigns[at].slug
			}
		}
	}

	return ""
}

// demoQuote renders a target for a finding, in backticks.
func demoQuote(target string) string { return "`" + target + "`" }
