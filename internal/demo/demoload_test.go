package demo_test

// The loader: how a tree of directories becomes the facts the six rules read.
//
// See `democheck_test.go` for what the gate is and why every requirement in it is
// derived rather than enumerated, and `demorules_test.go` for the rules.
//
// Everything in this file is a pure function of the tree except the two
// constructors, and both of those take their configuration from the product rather
// than from this file: the `content.Root` is the confinement boundary, and the
// `content.Renderer` is the pipeline. There is no constant in this package that
// names a page, a kind, an extension or a count.

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
)

// loadDemoCampaign opens one campaign and reads every page in it.
//
// Returns a nil campaign when the content root could not be opened, with the
// finding that says so — a campaign the gate cannot open is a campaign it has not
// checked, and the honest report of that is a finding rather than a skip.
func loadDemoCampaign(
	t *testing.T,
	vaultRoot string,
	slug string,
	kinds demoKinds,
) (*demoCampaign, []demoFinding) {
	t.Helper()

	dir := filepath.Join(vaultRoot, slug)

	root, err := content.NewRoot(slug, dir, content.RefuseSymlinks)
	if err != nil {
		return nil, []demoFinding{{
			rule:     demoRuleVault,
			campaign: slug,
			detail: "the campaign's content root could not be opened: " +
				err.Error(),
		}}
	}

	paths, err := demoPagePaths(dir)
	if err != nil {
		_ = root.Close()

		return nil, []demoFinding{{
			rule:     demoRuleReadable,
			campaign: slug,
			detail:   err.Error(),
		}}
	}

	campaign := &demoCampaign{slug: slug, dir: dir, root: root, kinds: kinds}
	renderer := content.NewRenderer(slug, kinds)

	var findings []demoFinding

	// In path order, because `demoPagePaths` sorts and the order a page's
	// references are resolved in must not depend on the filesystem.
	for _, rel := range paths {
		page, pageFindings := loadDemoPage(t, campaign, renderer, rel)

		findings = append(findings, pageFindings...)

		campaign.pages = append(campaign.pages, page)
		campaign.noteIndex(page)
	}

	return campaign, findings
}

// noteIndex records the campaign's `kind: index` page, counting rather than keeping
// one, so "two index pages" is distinguishable from "one".
//
// `rules.KindIndex` is the domain's own constant for the kind, so this is the
// product's vocabulary rather than a list this file invented; and the page is the
// one whose **honoured** kind is `index`, so a page declaring `kind: index` in a
// build where the kind is not registered is prose and is not the index.
func (c *demoCampaign) noteIndex(page demoPage) {
	if page.kind != domain.PageKind(rules.KindIndex) {
		return
	}

	c.indexes++

	if c.indexes == 1 {
		c.indexPath = page.rel
	}
}

// loadDemoPage reads one page through the content root and records what the pipeline
// saw.
//
// Every read goes through `content.Root`, so the bytes are the ones the product would
// serve rather than the ones a plain `os.ReadFile` happened to see — a demo gate
// reading bytes by a different path than the wiki route is a gate that can be green
// over a page the wiki will not serve.
func loadDemoPage(
	t *testing.T,
	campaign *demoCampaign,
	renderer *content.Renderer,
	rel string,
) (demoPage, []demoFinding) {
	t.Helper()

	page := demoPage{rel: rel}

	raw, err := demoReadFile(campaign.root, rel)
	if err != nil {
		return page, []demoFinding{{
			rule:     demoRuleReadable,
			campaign: campaign.slug,
			page:     rel,
			detail:   "the page could not be read: " + err.Error(),
		}}
	}

	if len(raw) > content.MaxDocumentBytes {
		return page, []demoFinding{{
			rule:     demoRuleReadable,
			campaign: campaign.slug,
			page:     rel,
			detail: fmt.Sprintf(
				"the page is %d bytes and the product refuses anything over %d",
				len(raw), content.MaxDocumentBytes,
			),
		}}
	}

	// The **same** registry handed to the parser, to `ResolvePageKind` and to the
	// renderer. Three callers and one value, because a page's declared kind, the
	// kind the audit honours and the kind the pipeline renders are one question
	// asked three times, and two registries would be three answers.
	doc := content.Parse(raw, campaign.kinds)
	page.doc = doc
	page.title = doc.FrontMatter.Title
	page.declared = doc.FrontMatter.DeclaredKind
	page.kind = domain.ResolvePageKind(doc.FrontMatter.DeclaredKind, campaign.kinds)
	page.secrets = demoSecrets(doc.Body)

	var findings []demoFinding

	if doc.FrontMatter.Err != nil {
		findings = append(findings, demoFinding{
			rule:     demoRuleReadable,
			campaign: campaign.slug,
			page:     rel,
			detail: "the front matter did not interpret, so the page renders as " +
				"prose: " + doc.FrontMatter.Err.Error(),
		})
	}

	// The **whole** pipeline, not a second parse of the page's bytes: the references
	// a page makes are what the renderer's collector observed, and a hand-rolled scan
	// for `[[` would disagree with it on every construct the pipeline declines.
	rendered, err := renderer.Render(doc)
	if err != nil {
		return page, append(findings, demoFinding{
			rule:     demoRuleReadable,
			campaign: campaign.slug,
			page:     rel,
			detail:   "the page did not render: " + err.Error(),
		})
	}

	page.refs = rendered.References

	// The kind the **pipeline** honoured rather than the one the front matter
	// declared, so a page whose kind is unknown is recorded as the prose it renders
	// as here too. A gate that counted declarations would count a `tokn` as a
	// demonstrated `token`.
	if rendered.Kind != "" {
		page.kind = rendered.Kind
	}

	return page, findings
}

// demoReadFile reads one root-relative path through the confinement boundary.
func demoReadFile(root *content.Root, rel string) ([]byte, error) {
	target, err := root.At(rel)
	if err != nil {
		return nil, fmt.Errorf("open %s in the content root: %w", rel, err)
	}

	raw, err := target.ReadFile()
	if err != nil {
		return nil, fmt.Errorf("read %s from the content root: %w", rel, err)
	}

	return raw, nil
}

// demoSecrets reduces a page body to the states and line ranges a finding may use.
//
// The byte offsets the scanner reports are into `body`, so the lines are counted here
// rather than taken: `content`'s own `lineOf` is unexported, and the only thing
// needed is whether a *reference's* line falls inside a callout — a question that
// answers itself correctly from a newline count.
func demoSecrets(body string) []demoSecret {
	scanned := content.ScanSecrets(body)

	found := make([]demoSecret, 0, len(scanned))

	for _, secret := range scanned {
		found = append(found, demoSecret{
			state: secret.State,
			first: demoLineOf(body, secret.HeaderStart),
			last:  demoLineOf(body, secret.BodyEnd),
		})
	}

	return found
}

// demoLineOf returns the 1-based line a byte offset falls on.
func demoLineOf(body string, offset int) int {
	if offset <= 0 {
		return 1
	}

	if offset > len(body) {
		offset = len(body)
	}

	return strings.Count(body[:offset], "\n") + 1
}

// holdsSecret reports whether a line falls inside one of a page's callouts.
//
// Inclusive of the header line, so a finding about a reference written on a callout's
// own header line is suppressed as well as one about a reference in its body. A
// callout's title is author text too.
func (p demoPage) holdsSecret(line int) bool {
	for _, secret := range p.secrets {
		if line >= secret.first && line <= secret.last {
			return true
		}
	}

	return false
}

// sortDemoFindings puts findings in a stable order.
//
// **Not optional.** Findings are accumulated by several rules, and a gate whose
// output reorders between runs makes a reviewer read one failure as two.
func sortDemoFindings(findings []demoFinding) []demoFinding {
	slices.SortFunc(findings, func(a, b demoFinding) int {
		switch {
		case a.rule != b.rule:
			return strings.Compare(a.rule, b.rule)
		case a.campaign != b.campaign:
			return strings.Compare(a.campaign, b.campaign)
		case a.page != b.page:
			return strings.Compare(a.page, b.page)
		case a.line != b.line:
			return a.line - b.line
		default:
			return strings.Compare(a.detail, b.detail)
		}
	})

	return findings
}
