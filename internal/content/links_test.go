package content_test

import (
	"errors"
	"flag"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/content/ext"
	"github.com/semiplane/semiplane/internal/domain"
)

// The fixtures are real directories under t.TempDir() and real content roots, and
// real symlinks — never mocks. What is under test is a claim about what a
// reference is allowed to become, and that claim is about `os.Root`, a `..`
// segment, a percent sign and a browser's idea of a URL. A mock filesystem would
// agree with whatever this file believes, which is the one thing these tests exist
// to check.
//
// The goldens are the markup C3's renderer will emit once the addresses from
// Links are attached, rendered by the `renderLinks` helper below rather than by
// ext's writers — because ext deliberately writes an `<a>` with no href and a
// `<span>` with no contents, and the substitution is C4's. Reading the goldens
// against a renderer that had to be taught the rules would test the teaching;
// these are the rules.

var updateLinkGoldens = flag.Bool(
	"update-links",
	false,
	"rewrite the link goldens under testdata/links",
)

const (
	linkGoldenDir = "testdata/links"

	// homeSlug and the other slugs are the campaigns the fixtures build. They
	// appear in every expected address, so they are named once.
	homeSlug   = "greyhaven"
	publicSlug = "public-post"
	hiddenSlug = "forgotten-realm"
)

// campaign is one campaign's content tree on disk, with its index built from the
// same `[]domain.Page` a `store.PagesForCampaign` call would return.
type campaign struct {
	slug  string
	dir   string
	root  *content.Root
	index content.PageIndex
	pages []domain.Page
}

// newFixture writes files as slug-relative paths under a fresh content root and
// opens it. The files' contents do not matter — link resolution never reads a
// page, and that is the property the index is there to make cheap — so they are
// one line of prose each.
func newCampaign(t *testing.T, slug string, files ...string) campaign {
	t.Helper()

	dir := t.TempDir()

	pages := make([]domain.Page, 0, len(files))

	for _, rel := range files {
		linkWrite(t, filepath.Join(dir, filepath.FromSlash(rel)), "# "+rel+"\n")

		// Only markdown files are pages. An asset on disk is not a row in `pages`
		// and `store.PagesForCampaign` never returns one, so indexing it here would
		// make this fixture disagree with the store and the asset assertions would
		// prove nothing.
		if strings.HasSuffix(rel, ".md") {
			pages = append(pages, domain.Page{CampaignID: 1, Path: rel, Kind: domain.KindProse})
		}
	}

	root, err := content.NewRoot(slug, dir, content.RefuseSymlinks)
	if err != nil {
		t.Fatalf("open content root for %s: %v", slug, err)
	}

	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close content root for %s: %v", slug, err)
		}
	})

	return campaign{
		slug:  slug,
		dir:   dir,
		root:  root,
		index: content.NewPageIndex(pages),
		pages: pages,
	}
}

// resolver returns a Resolver over the campaign, which is the whole of what a
// caller supplies: the root, and the pages.
func (f campaign) resolver(t *testing.T) *content.Resolver {
	t.Helper()

	return content.NewResolver(f.root, f.pages)
}

// visible returns the campaign as a VisibleCampaign, which is the whole of what a
// caller supplies for a campaign the viewer may see: a slug and an index. Note
// what it takes: no root, so nothing downstream can read the campaign.
func (f campaign) visible() content.VisibleCampaign {
	return content.VisibleCampaign{Slug: f.slug, Index: f.index}
}

// write creates one file, creating its parent directories.
func linkWrite(t *testing.T, path, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// origin returns the Origin for a page path, failing the test if it cannot.
func linkOrigin(t *testing.T, page string) content.Origin {
	t.Helper()

	value, err := content.NewOrigin(page)
	if err != nil {
		t.Fatalf("origin for %s: %v", page, err)
	}

	return value
}

// linkRefExt builds one outbound reference the way render.go reports it: the target
// verbatim as the author wrote it, and the extension that produced it.
func linkRefExt(extension ext.Kind, target string) content.Reference {
	return content.Reference{Extension: extension, Target: target}
}

// linkRef is `[[target]]`.
func linkRef(target string) content.Reference {
	return linkRefExt(ext.KindWikilink, target)
}

// linkEmbed is `![[target]]`.
func linkEmbed(target string) content.Reference {
	return linkRefExt(ext.KindEmbed, target)
}

// rootRef is a campaign-qualified reference: `[[/slug/target]]`, Obsidian's
// vault-absolute spelling. The leading slash is what makes it a reference into
// *another* campaign rather than a path relative to the page it was written on, so
// every cross-campaign case in this file spells it that way.
func rootRef(slug, target string) string {
	return "/" + slug + "/" + target
}

// linkFor is the one-link form of Links, for the table tests.
func linkOne(t *testing.T, res *content.Resolver, page string, one content.Reference) content.Link {
	t.Helper()

	links := res.Links(linkOrigin(t, page), []content.Reference{one})
	if len(links) != 1 {
		t.Fatalf("Links returned %d links, want 1", len(links))
	}

	return links[0]
}

// TestRelativePathResolution covers step 1 of S-5.5: a reference that looks like a
// path is a path, and it is interpreted against the referring page's directory.
func TestRelativePathResolution(t *testing.T) {
	t.Parallel()

	camp := newCampaign(t, homeSlug,
		"index.md",
		"notes/Goblin.md",
		"notes/deep/page.md",
		"notes/deep/sibling.md",
		"notes/maps/Drowned Lighthouse.png",
	)
	res := camp.resolver(t)

	for _, tc := range []struct {
		name   string
		page   string
		target string
		want   string
		broken bool
	}{
		{
			name:   "a sibling of the referring page",
			page:   "notes/deep/page.md",
			target: "sibling",
			want:   "/c/" + homeSlug + "/wiki/notes/deep/sibling",
		},
		{
			name:   "a path that walks up and back down again",
			page:   "notes/deep/page.md",
			target: "../deep/sibling",
			want:   "/c/" + homeSlug + "/wiki/notes/deep/sibling",
		},
		{
			name:   "a path into a directory that names no page is broken, not refused",
			page:   "notes/deep/page.md",
			target: "./Goblin",
			want:   "/c/" + homeSlug + "/wiki/notes/deep/Goblin",
			broken: true,
		},
		{
			name:   "a parent reference that stays inside the root",
			page:   "notes/deep/page.md",
			target: "../Goblin",
			want:   "/c/" + homeSlug + "/wiki/notes/Goblin",
		},
		{
			// Relative means relative. `deep/sibling` in a page under `notes/deep` is
			// `notes/deep/deep/sibling`, and reading it as a campaign called `deep`
			// would make a link's meaning depend on which directories exist.
			name:   "a relative path is joined onto the page's directory, never onto the root",
			page:   "notes/deep/page.md",
			target: "deep/sibling",
			want:   "/c/" + homeSlug + "/wiki/notes/deep/deep/sibling",
			broken: true,
		},
		{
			name:   "the same page, reached from the top of the campaign",
			page:   "index.md",
			target: "notes/deep/sibling",
			want:   "/c/" + homeSlug + "/wiki/notes/deep/sibling",
		},
		{
			name:   "a vault-root reference means this campaign's root, not the host's",
			page:   "notes/deep/page.md",
			target: "/notes/Goblin",
			want:   "/c/" + homeSlug + "/wiki/notes/Goblin",
		},
		{
			name:   "an explicit extension is the same page as the bare spelling",
			page:   "notes/deep/page.md",
			target: "../Goblin.md",
			want:   "/c/" + homeSlug + "/wiki/notes/Goblin",
		},
		{
			// An asset is not a page, so `store.PagesForCampaign` never returned it
			// and the index cannot answer for it. It still gets a correct address on
			// the asset route, and it is marked broken — which is honest, because this
			// layer really does not know whether the file is there.
			name:   "an asset is addressed on the asset route",
			page:   "notes/deep/page.md",
			target: "../maps/Drowned Lighthouse.png",
			want:   "/c/" + homeSlug + "/assets/notes/maps/Drowned%20Lighthouse.png",
			broken: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := linkOne(t, res, tc.page, linkRef(tc.target))
			if got.Href != tc.want {
				t.Errorf("href = %q, want %q", got.Href, tc.want)
			}

			if got.Refusal != content.Allowed {
				t.Errorf("refusal = %v, want allowed", got.Refusal)
			}

			if got.Broken != tc.broken {
				t.Errorf("broken = %v, want %v", got.Broken, tc.broken)
			}
		})
	}
}

// TestBareNameResolvesByBasenameIndex covers step 2, and the branch that makes it
// different from step 1: a bare name is a *name*, so it is not joined against the
// referring page's directory.
func TestBareNameResolvesByBasenameIndex(t *testing.T) {
	t.Parallel()

	camp := newCampaign(t, homeSlug, "index.md", "notes/Goblin.md", "notes/deep/page.md")
	res := camp.resolver(t)

	// Two directories deep, and still the one page whose base name matches: this is
	// the assertion that a name is not a relative path.
	got := linkOne(t, res, "notes/deep/page.md", linkRef("Goblin"))
	if want := "/c/" + homeSlug + "/wiki/notes/Goblin"; got.Href != want {
		t.Errorf("href = %q, want %q", got.Href, want)
	}

	if got.Broken {
		t.Error("broken = true, want false")
	}

	records := res.Records(
		linkOrigin(t, "notes/deep/page.md"),
		[]content.Reference{linkRef("Goblin")},
		nil,
	)
	if len(records) != 1 || !records[0].Resolved {
		t.Fatalf("record = %+v, want resolved", records)
	}

	if records[0].TargetPath != "notes/Goblin" {
		t.Errorf("target path = %q, want %q", records[0].TargetPath, "notes/Goblin")
	}

	if records[0].TargetCampaign != homeSlug {
		t.Errorf("target campaign = %q, want %q", records[0].TargetCampaign, homeSlug)
	}
}

// TestBareNameResolvesAcrossVisibleCampaigns covers step 3 of S-5.5: a name the
// home campaign does not hold is looked for in the campaigns the viewer may see.
func TestBareNameResolvesAcrossVisibleCampaigns(t *testing.T) {
	t.Parallel()

	home := newCampaign(t, homeSlug, "index.md")
	public := newCampaign(t, publicSlug, "Some Page.md", "notes/Goblin.md")
	res := home.resolver(t)

	refs := []content.Reference{linkRef("Some Page")}
	visible := []content.VisibleCampaign{public.visible()}

	records := res.Records(linkOrigin(t, "index.md"), refs, visible)
	if len(records) != 1 {
		t.Fatalf("Records returned %d records, want 1", len(records))
	}

	got := records[0]
	if !got.Resolved {
		t.Fatalf("record = %+v, want resolved for a viewer who can see %s", got, publicSlug)
	}

	if got.TargetCampaign != publicSlug {
		t.Errorf("target campaign = %q, want %q", got.TargetCampaign, publicSlug)
	}

	if got.TargetPath != "Some Page" {
		t.Errorf("target path = %q, want %q", got.TargetPath, "Some Page")
	}

	// And the campaign-qualified spelling of the same reference agrees, which is
	// what makes the two forms one reference rather than two dialects.
	qualified := res.Records(linkOrigin(t, "index.md"),
		[]content.Reference{linkRef(rootRef(publicSlug, "Some Page"))}, visible)

	if !qualified[0].Resolved || qualified[0].TargetPath != "Some Page" {
		t.Errorf("qualified record = %+v, want resolved to %q", qualified[0], "Some Page")
	}
}

// TestInvisibleCampaignIsIndistinguishableFromNothing is the ADR 0017 requirement
// stated as an equality: a reference naming a campaign the viewer cannot see must
// produce *exactly* the record a reference naming nothing produces. Anything less —
// a refusal, a hint, an empty slug — turns the broken-link report into an
// existence oracle.
func TestInvisibleCampaignIsIndistinguishableFromNothing(t *testing.T) {
	t.Parallel()

	home := newCampaign(t, homeSlug, "index.md")
	hidden := newCampaign(t, hiddenSlug, "Hidden Page.md")
	res := home.resolver(t)

	// The viewer can see nothing but their own campaign.
	refs := []content.Reference{
		linkRef(rootRef(hiddenSlug, "Hidden Page")),
		linkRef("Nowhere At All"),
	}

	records := res.Records(linkOrigin(t, "index.md"), refs, nil)
	if len(records) != 2 {
		t.Fatalf("Records returned %d records, want 2", len(records))
	}

	invisible, absent := records[0], records[1]

	if invisible.Resolved != absent.Resolved ||
		invisible.TargetCampaign != absent.TargetCampaign ||
		invisible.TargetPath != absent.TargetPath ||
		invisible.Refusal != absent.Refusal {
		t.Errorf(
			"a reference into an invisible campaign produced %+v and one naming nothing produced %+v;\n"+
				"the two must be indistinguishable or the report is an existence oracle",
			invisible,
			absent,
		)
	}

	if invisible.Resolved {
		t.Error("the reference into the invisible campaign resolved")
	}

	// The same reference, for a viewer who *can* see that campaign, resolves. So the
	// difference is the viewer's and not the reference's — which is the whole reason
	// the markup may not encode it.
	seen := res.Records(
		linkOrigin(t, "index.md"),
		refs[:1],
		[]content.VisibleCampaign{hidden.visible()},
	)
	if !seen[0].Resolved || seen[0].TargetCampaign != hiddenSlug {
		t.Errorf("record for a visible campaign = %+v, want resolved to %s", seen[0], hiddenSlug)
	}
}

// TestRenderPathCannotSeeAnotherCampaign is the structural half of the rule above:
// the render path is handed the visible set and ignores it, because it has nowhere
// to put it.
func TestRenderPathCannotSeeAnotherCampaign(t *testing.T) {
	t.Parallel()

	home := newCampaign(t, homeSlug, "index.md")
	public := newCampaign(t, publicSlug, "Some Page.md")
	res := home.resolver(t)

	// The bare name resolves across campaigns for the record…
	withVisible := res.Records(linkOrigin(t, "index.md"),
		[]content.Reference{linkRef("Some Page")},
		[]content.VisibleCampaign{public.visible()})

	if !withVisible[0].Resolved {
		t.Fatal("the record for a visible campaign did not resolve")
	}

	// …and the markup is the same either way, because Links takes no visible set.
	links := res.Links(linkOrigin(t, "index.md"), []content.Reference{linkRef("Some Page")})
	want := "/c/" + homeSlug + "/wiki/Some%20Page"

	if links[0].Href != want {
		t.Errorf("href = %q, want %q", links[0].Href, want)
	}

	// It *is* marked broken, and identically so for every viewer: the home
	// campaign's index does not hold the name. What the render path cannot know is
	// whether another campaign does — and that is exactly what must not reach the
	// markup, so there is no cross-campaign marker and no "exists elsewhere".
	if !links[0].Broken {
		t.Error("broken = false, want true: the home campaign does not hold this name")
	}

	if links[0].CrossCampaign {
		t.Error("cross-campaign = true for a reference that named no campaign")
	}
}

// TestUnresolvedLinkDoesNotFailThePageAndIsRecorded: one broken reference costs the
// page nothing, and is still reported.
func TestUnresolvedLinkDoesNotFailThePageAndIsRecorded(t *testing.T) {
	t.Parallel()

	camp := newCampaign(t, homeSlug, "index.md", "notes/Goblin.md")
	res := camp.resolver(t)

	refs := []content.Reference{
		linkRef("Goblin"),
		linkRef("Nobody"),
		linkRef("notes/Nothing"),
	}
	page := linkOrigin(t, "index.md")

	links := res.Links(page, refs)
	if len(links) != 3 {
		t.Fatalf("Links returned %d links, want 3", len(links))
	}

	if links[1].Refusal != content.Allowed {
		t.Errorf(
			"an unresolved reference was refused (%v); it should be broken, not refused",
			links[1].Refusal,
		)
	}

	if !links[1].Broken {
		t.Error("broken = false for a name the index does not hold")
	}

	// It still has an address, so following it is a 404 for a reader who is already
	// authorised for the campaign rather than a link that goes nowhere.
	if want := "/c/" + homeSlug + "/wiki/Nobody"; links[1].Href != want {
		t.Errorf("href = %q, want %q", links[1].Href, want)
	}

	if links[0].Broken {
		t.Error("a resolved reference was marked broken")
	}

	if !links[2].Broken {
		t.Error("broken = false for a path that names no page")
	}

	records := res.Records(page, refs, nil)
	if len(records) != 3 {
		t.Fatalf("Records returned %d records, want 3", len(records))
	}

	for _, idx := range []int{1, 2} {
		if records[idx].Resolved {
			t.Errorf("record %d resolved a reference that names nothing: %+v", idx, records[idx])
		}

		if records[idx].Refusal != content.Allowed {
			t.Errorf(
				"record %d was refused (%v), want merely unresolved",
				idx,
				records[idx].Refusal,
			)
		}
	}

	if !records[0].Resolved {
		t.Errorf("record 0 did not resolve: %+v", records[0])
	}
}

// TestCrossCampaignMarkupIsByteIdenticalForEveryViewer is the assertion the phased
// delivery plan names for D12: render one page carrying a cross-campaign link as a
// GM, as a player and as an anonymous reader, and diff the three bodies.
//
// The three viewer states differ where they are *supposed* to differ — in the
// records — and the test asserts that difference first, so the equality below is
// not vacuous: it is an equality between three genuinely different inputs.
func TestCrossCampaignMarkupIsByteIdenticalForEveryViewer(t *testing.T) {
	t.Parallel()

	home := newCampaign(t, homeSlug, "index.md")
	public := newCampaign(t, publicSlug, "Some Page.md")
	res := home.resolver(t)

	refs := []content.Reference{
		linkRef(rootRef(publicSlug, "Some Page")),
		linkEmbed(rootRef(publicSlug, "Some Page")),
		linkRef("Some Page"),
	}
	page := linkOrigin(t, "index.md")

	// The GM of greyhaven also plays in public-post. The player and the anonymous
	// reader do not, and public-post being private means neither of them can see
	// it by tier either.
	asGM := res.Records(page, refs, []content.VisibleCampaign{public.visible()})
	asPlayer := res.Records(page, refs, nil)
	asAnon := res.Records(page, refs, nil)

	if !asGM[0].Resolved || asGM[0].TargetCampaign != publicSlug {
		t.Fatalf(
			"the GM's record for the qualified reference = %+v, want resolved to %s",
			asGM[0],
			publicSlug,
		)
	}

	if asPlayer[0].Resolved || asAnon[0].Resolved {
		t.Fatalf("a player resolved a reference into a campaign they cannot see: %+v", asPlayer[0])
	}

	if asGM[2].Resolved == asPlayer[2].Resolved {
		t.Fatal("the bare-name record did not differ between the GM and the player, " +
			"so this test is comparing three identical inputs")
	}

	// The whole point: the markup did not move.
	gmBody := renderLinkMarkup(res.Links(page, refs))
	playerBody := renderLinkMarkup(res.Links(page, refs))
	anonBody := renderLinkMarkup(res.Links(page, refs))

	if gmBody != playerBody {
		t.Errorf(
			"the GM's body and the player's body differ:\ngm:\n%s\nplayer:\n%s",
			gmBody,
			playerBody,
		)
	}

	if gmBody != anonBody {
		t.Errorf(
			"the GM's body and the anonymous body differ:\ngm:\n%s\nanonymous:\n%s",
			gmBody,
			anonBody,
		)
	}

	assertLinkGolden(t, "cross-campaign.html", gmBody)

	// And ADR 0017's own example: a plain anchor, the author's text as its label,
	// the other campaign's slug in the address, and no `403`-shaped body anywhere.
	if !strings.Contains(gmBody, `<a href="/c/`+publicSlug+`/wiki/Some%20Page"`) {
		t.Errorf(
			"the cross-campaign link is not the plain hyperlink ADR 0017 requires:\n%s",
			gmBody,
		)
	}

	if strings.Contains(gmBody, "inlined") || strings.Contains(gmBody, "<blockquote") {
		t.Errorf("something was inlined:\n%s", gmBody)
	}
}

// TestCrossCampaignEmbedIsRefused: embedding is inlining, so it is refused — as a
// refusal, not a 404, and not a silent strip.
func TestCrossCampaignEmbedIsRefused(t *testing.T) {
	t.Parallel()

	home := newCampaign(t, homeSlug, "index.md", "notes/Goblin.md")
	public := newCampaign(t, publicSlug, "Some Page.md")
	res := home.resolver(t)

	page := linkOrigin(t, "index.md")

	links := res.Links(page, []content.Reference{
		linkEmbed(rootRef(publicSlug, "Some Page")),
		linkEmbed(rootRef(publicSlug, "map.png")),
		linkEmbed("Goblin"),
	})

	for _, idx := range []int{0, 1} {
		got := links[idx]

		if got.Refusal != content.RefuseCrossCampaignEmbed {
			t.Errorf("embed %d refusal = %v, want cross-campaign-embed", idx, got.Refusal)
		}

		if got.Href != "" {
			t.Errorf("embed %d has href %q; a refused embed has no address at all", idx, got.Href)
		}

		if !errors.Is(got.Refusal.Err(), content.ErrCrossCampaignEmbed) {
			t.Errorf("embed %d sentinel = %v, want ErrCrossCampaignEmbed", idx, got.Refusal.Err())
		}
	}

	// An embed inside the campaign is not refused: it is a slot the caller fills
	// after rendering the target itself.
	if links[2].Refusal != content.Allowed {
		t.Errorf("an embed within the campaign was refused: %v", links[2].Refusal)
	}

	if want := "/c/" + homeSlug + "/wiki/notes/Goblin"; links[2].Href != want {
		t.Errorf("href = %q, want %q", links[2].Href, want)
	}

	body := renderLinkMarkup(links)
	assertLinkGolden(t, "cross-campaign-embed.html", body)

	if !strings.Contains(body, content.RefuseCrossCampaignEmbed.Reason()) {
		t.Errorf("the refusal is not visible to the reader:\n%s", body)
	}

	records := res.Records(page, []content.Reference{linkEmbed(rootRef(publicSlug, "Some Page"))},
		[]content.VisibleCampaign{public.visible()})

	if records[0].Refusal != content.RefuseCrossCampaignEmbed {
		t.Errorf("record refusal = %v, want cross-campaign-embed", records[0].Refusal)
	}

	if records[0].Resolved {
		t.Error("a refused embed was recorded as resolved")
	}
}

// TestHostileReferenceCannotBecomeAnHref: nothing an author writes reaches the
// address field as a URL of its own, and nothing climbs out of the campaign prefix.
func TestHostileReferenceCannotBecomeAnHref(t *testing.T) {
	t.Parallel()

	camp := newCampaign(t, homeSlug, "index.md", "notes/Goblin.md")
	res := camp.resolver(t)

	for _, tc := range []struct {
		name    string
		target  string
		want    string
		refusal content.Refusal
	}{
		{
			name:   "a javascript scheme is a path segment, never a scheme",
			target: "javascript:alert(1)",
			want:   "/c/" + homeSlug + "/wiki/javascript:alert%281%29",
		},
		{
			name:   "a vbscript scheme likewise",
			target: "vbscript:msgbox(1)",
			want:   "/c/" + homeSlug + "/wiki/vbscript:msgbox%281%29",
		},
		{
			name:   "a quote cannot terminate an attribute",
			target: `Goblin" onmouseover="alert(1)`,
			want:   `/c/` + homeSlug + `/wiki/Goblin%22%20onmouseover=%22alert%281%29`,
		},
		{
			name:   "angle brackets are escaped rather than markup",
			target: "<img src=x onerror=alert(1)>",
			want:   "/c/" + homeSlug + "/wiki/%3Cimg%20src=x%20onerror=alert%281%29%3E",
		},
		{
			name:    "a parent reference that leaves the root is refused",
			target:  "../../etc/passwd",
			refusal: content.RefuseOutsideRoot,
		},
		{
			name:    "a parent reference hidden inside a path is refused",
			target:  "notes/../../../etc/passwd",
			refusal: content.RefuseOutsideRoot,
		},
		{
			name:    "a parent reference into another campaign is refused too",
			target:  rootRef(publicSlug, "../escape"),
			refusal: content.RefuseOutsideRoot,
		},
		{
			name:    "a NUL is malformed, not an escape",
			target:  "notes/Go\x00blin",
			refusal: content.RefuseMalformed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := linkOne(t, res, "index.md", linkRef(tc.target))

			if got.Refusal != tc.refusal {
				t.Fatalf("refusal = %v, want %v", got.Refusal, tc.refusal)
			}

			if tc.refusal != content.Allowed {
				if got.Href != "" {
					t.Errorf("a refused reference produced href %q", got.Href)
				}

				return
			}

			if got.Href != tc.want {
				t.Errorf("href = %q, want %q", got.Href, tc.want)
			}

			// Two invariants, both structural rather than per-input: the address is
			// under this campaign's prefix, and it carries no `..` segment for a
			// browser to normalise away.
			if !strings.HasPrefix(got.Href, "/c/"+homeSlug+"/") {
				t.Errorf("href %q is not under this campaign's prefix", got.Href)
			}

			for segment := range strings.SplitSeq(got.Href, "/") {
				if segment == ".." {
					t.Errorf("href %q carries a `..` segment", got.Href)
				}
			}
		})
	}

	assertLinkGolden(
		t,
		"hostile.html",
		renderLinkMarkup(res.Links(linkOrigin(t, "index.md"), []content.Reference{
			linkRef("javascript:alert(1)"),
			linkRef(`Goblin" onmouseover="alert(1)`),
			linkRef("<img src=x onerror=alert(1)>"),
			linkRef("../../etc/passwd"),
		})),
	)
}

// TestHrefEscapesEachSegmentSeparately is the test that says why per-segment
// escaping and not escaping the path in one call.
func TestHrefEscapesEachSegmentSeparately(t *testing.T) {
	t.Parallel()

	camp := newCampaign(t, homeSlug,
		"index.md",
		"lore/Coast Road.md",
		"lore/Café 100% & Salt.md",
		"lore/Grüße (2026).md",
	)
	res := camp.resolver(t)

	for _, tc := range []struct {
		name   string
		target string
		want   string
	}{
		{
			name:   "a space",
			target: "lore/Coast Road",
			want:   "/c/" + homeSlug + "/wiki/lore/Coast%20Road",
		},
		{
			name:   "unicode, a percent sign and an ampersand",
			target: "lore/Café 100% & Salt",
			want:   "/c/" + homeSlug + "/wiki/lore/Caf%C3%A9%20100%25%20&%20Salt",
		},
		{
			name:   "parentheses and non-ASCII punctuation",
			target: "lore/Grüße (2026)",
			want:   "/c/" + homeSlug + "/wiki/lore/Gr%C3%BC%C3%9Fe%20%282026%29",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := linkOne(t, res, "index.md", linkRef(tc.target))
			if got.Href != tc.want {
				t.Errorf("href = %q, want %q", got.Href, tc.want)
			}

			// The separator has to survive: escaping `lore/Coast Road` in one call
			// produces `lore%2FCoast%20Road`, which is one segment where the URL
			// scheme expects two and which therefore matches no route at all. It
			// reads like it worked, because the result is still a well-formed URL.
			if !strings.Contains(got.Href, "/wiki/lore/") {
				t.Errorf("href %q lost its separator to the escaper", got.Href)
			}
		})
	}

	assertLinkGolden(
		t,
		"escapes.html",
		renderLinkMarkup(res.Links(linkOrigin(t, "index.md"), []content.Reference{
			linkRef("lore/Coast Road"),
			linkRef("lore/Café 100% & Salt"),
			linkRef("lore/Grüße (2026)"),
		})),
	)

	// The counter-example, asserted rather than asserted-about.
	if whole := linkEscapeWhole("lore/Coast Road"); whole == "/c/"+homeSlug+"/wiki/"+whole {
		t.Error("the single-call escape is indistinguishable from the per-segment one, " +
			"which would mean this test proves nothing")
	}
}

// TestAnchorsAndAliases covers the rest of the `[[…]]` grammar: a heading anchor, a
// block anchor, an alias, and the anchor-only reference that points inside the
// current page.
func TestAnchorsAndAliases(t *testing.T) {
	t.Parallel()

	camp := newCampaign(t, homeSlug, "index.md", "notes/Goblin.md")
	res := camp.resolver(t)

	page := linkOrigin(t, "notes/Goblin.md")

	for _, tc := range []struct {
		name   string
		ref    content.Reference
		want   string
		label  string
		broken bool
	}{
		{
			name: "a heading anchor is a fragment on the page's own address",
			ref: content.Reference{
				Extension: ext.KindWikilink,
				Target:    "Goblin",
				Anchor:    "#Chief",
			},
			want:  "/c/" + homeSlug + "/wiki/notes/Goblin#Chief",
			label: "Goblin",
		},
		{
			name: "a block anchor keeps its sigil",
			ref: content.Reference{
				Extension: ext.KindWikilink,
				Target:    "Goblin",
				Anchor:    "#^goblin-1",
			},
			want:  "/c/" + homeSlug + "/wiki/notes/Goblin#%5Egoblin-1",
			label: "Goblin",
		},
		{
			name:   "an alias replaces the label",
			ref:    content.Reference{Extension: ext.KindWikilink, Target: "Goblin", Alias: "the goblin"},
			want:   "/c/" + homeSlug + "/wiki/notes/Goblin",
			label:  "the goblin",
			broken: false,
		},
		{
			name: "an alias and an anchor together",
			ref: content.Reference{
				Extension: ext.KindWikilink,
				Target:    "Goblin",
				Anchor:    "#Chief",
				Alias:     "its chief",
			},
			want:  "/c/" + homeSlug + "/wiki/notes/Goblin#Chief",
			label: "its chief",
		},
		{
			name: "an anchor-only reference points inside the page it is written on",
			ref: content.Reference{
				Extension: ext.KindWikilink,
				Anchor:    "#Chief",
			},
			want:  "/c/" + homeSlug + "/wiki/notes/Goblin#Chief",
			label: "",
		},
		{
			name:   "an anchor on a name that does not resolve is still an address",
			ref:    content.Reference{Extension: ext.KindWikilink, Target: "Nobody", Anchor: "#Chief"},
			want:   "/c/" + homeSlug + "/wiki/Nobody#Chief",
			label:  "Nobody",
			broken: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := linkOne(t, res, "notes/Goblin.md", tc.ref)

			if got.Href != tc.want {
				t.Errorf("href = %q, want %q", got.Href, tc.want)
			}

			if got.Refusal != content.Allowed {
				t.Errorf("refusal = %v, want allowed", got.Refusal)
			}

			if got.Broken != tc.broken {
				t.Errorf("broken = %v, want %v", got.Broken, tc.broken)
			}

			// The label is ext.Reference.Label's answer, not a title read from the
			// target. Asserted here because it is the field ADR 0017 is about.
			if want := tc.ref.Label(); got.Label != want {
				t.Errorf("label = %q, want %q", got.Label, want)
			}
		})
	}

	// An anchor-only reference is not a broken link: it names this page.
	records := res.Records(
		page,
		[]content.Reference{{Extension: ext.KindWikilink, Anchor: "#Chief"}},
		nil,
	)
	if !records[0].Resolved {
		t.Errorf(
			"an anchor-only reference was not recorded as resolving to its own page: %+v",
			records[0],
		)
	}

	if records[0].TargetPath != "notes/Goblin" {
		t.Errorf("target path = %q, want %q", records[0].TargetPath, "notes/Goblin")
	}
}

// TestSymlinkAndEscapeAreRefusedViaRootSentinels: the confinement answers come from
// `os.Root` through root.go's own sentinels, not from a second implementation of
// the same rule here.
func TestSymlinkAndEscapeAreRefusedViaRootSentinels(t *testing.T) {
	t.Parallel()

	camp := newCampaign(t, homeSlug, "index.md", "notes/Goblin.md")
	res := camp.resolver(t)

	// A symlink out of the root, and one that stays inside. Both are planted in the
	// real tree, because the claim under test is what the kernel does with `..` and
	// with a link — not what a mock believes.
	outside := filepath.Join(t.TempDir(), "target.md")
	linkWrite(t, outside, "# outside\n")

	camp.symlink(t, "notes/leaked", outside)
	camp.symlink(t, "notes/stays", "notes/Goblin.md")

	links := res.Links(linkOrigin(t, "index.md"), []content.Reference{
		linkRef("notes/leaked"),
		linkRef("notes/stays"),
	})

	got := links[0]
	if got.Refusal != content.RefuseSymlink {
		t.Errorf("a symlink out of the root produced refusal %v, want symlink", got.Refusal)
	}

	if !errors.Is(got.Refusal.Err(), content.ErrSymlink) {
		t.Errorf("sentinel = %v, want ErrSymlink", got.Refusal.Err())
	}

	if got.Href != "" {
		t.Errorf("a refused symlink produced href %q", got.Href)
	}

	// The link that stays inside is refused too, because the policy is
	// RefuseSymlinks: a second name for a file is a page identity nobody indexed.
	if links[1].Refusal != content.RefuseSymlink {
		t.Errorf("a symlink inside the root produced refusal %v, want symlink", links[1].Refusal)
	}

	assertLinkGolden(t, "refusals.html", renderLinkMarkup(links))
}

// TestRefusalsMapToRootSentinels: every refusal this layer reports is
// distinguishable through `errors.Is` on root.go's vocabulary, so a caller writes
// its assertions against the boundary it already knows.
func TestRefusalsMapToRootSentinels(t *testing.T) {
	t.Parallel()

	camp := newCampaign(t, homeSlug, "index.md", "notes/Goblin.md")

	for _, tc := range []struct {
		refusal content.Refusal
		want    error
	}{
		{content.Allowed, nil},
		{content.RefuseOutsideRoot, content.ErrOutsideRoot},
		{content.RefuseMalformed, content.ErrInvalidRef},
		{content.RefuseSymlink, content.ErrSymlink},
		{content.RefuseCrossCampaignEmbed, content.ErrCrossCampaignEmbed},
		{content.RefuseUnavailable, content.ErrNoRoot},
	} {
		got := tc.refusal.Err()

		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%v.Err() = %v, want nil", tc.refusal, got)
		case tc.want != nil && !errors.Is(got, tc.want):
			t.Errorf("%v.Err() = %v, want it to match %v", tc.refusal, got, tc.want)
		}
	}

	// A Resolver with no root answers every path-shaped reference with "unavailable"
	// rather than dereferencing nil.
	bare := content.NewResolver(nil, camp.pages)

	link := linkOne(t, bare, "notes/Goblin", linkRef("../Goblin"))
	if link.Refusal != content.RefuseUnavailable {
		t.Errorf("a nil root produced refusal %v, want unavailable", link.Refusal)
	}

	if link.Href != "" {
		t.Errorf("a nil root produced href %q", link.Href)
	}

	// A name still resolves through the index — the index is not the root — but it
	// has no address, because an address needs a campaign slug and the slug comes
	// from the root that is not there.
	got := linkOne(t, bare, "notes/Goblin", linkRef("Goblin"))

	if got.Href != "" {
		t.Errorf("a nil root produced address %q; there is no slug to put in one", got.Href)
	}

	if got.Refusal != content.Allowed {
		t.Errorf("a bare name was refused with a nil root: %v", got.Refusal)
	}

	if got.Broken {
		t.Error("broken = true for a name the index holds, even with no root")
	}
}

// TestUnsupportedExtensionsKeepTheirOrdinal is a positional-seam test: Links and
// Records return one entry per reference, always, because render.go counted a
// `{{statblock}}` operand as a reference and put it in the document order too.
func TestUnsupportedExtensionsKeepTheirOrdinal(t *testing.T) {
	t.Parallel()

	camp := newCampaign(t, homeSlug, "index.md", "notes/Goblin.md")
	res := camp.resolver(t)

	refs := []content.Reference{
		{Index: 0, Extension: ext.KindWikilink, Target: "Goblin"},
		{Index: 1, Extension: ext.KindStatblock, Target: "Goblin", Arg: "Goblin"},
		{Index: 2, Extension: ext.KindDice, Arg: "1d20+5"},
		{Index: 3, Extension: ext.KindEmbed, Target: "notes/Goblin"},
	}

	links := res.Links(linkOrigin(t, "index.md"), refs)
	if len(links) != len(refs) {
		t.Fatalf("Links returned %d links for %d references", len(links), len(refs))
	}

	for idx, one := range links {
		if one.Index != idx {
			t.Errorf(
				"link %d carries index %d; the ordinal is what addresses it in the HTML",
				idx,
				one.Index,
			)
		}
	}

	for _, idx := range []int{1, 2} {
		if links[idx].Refusal != content.RefuseUnsupported {
			t.Errorf(
				"%s reference produced refusal %v, want unsupported",
				links[idx].Extension,
				links[idx].Refusal,
			)
		}

		if links[idx].Href != "" {
			t.Errorf("%s reference produced href %q", links[idx].Extension, links[idx].Href)
		}
	}

	if links[0].Href == "" || links[3].Href == "" {
		t.Error("a wikilink or an embed produced no address")
	}

	records := res.Records(linkOrigin(t, "index.md"), refs, nil)
	if len(records) != len(refs) {
		t.Fatalf("Records returned %d records for %d references", len(records), len(refs))
	}

	if records[1].Refusal != content.RefuseUnsupported ||
		records[2].Refusal != content.RefuseUnsupported {
		t.Error("a `{{…}}` reference was recorded as a broken link rather than unsupported")
	}

	assertLinkGolden(t, "records.txt", renderLinkRecords(records))
}

// TestBareNameIsNotAPath is the branch that makes step 2 different from step 1,
// asserted as a failure of the naive reading: joining a bare name against the
// referring page's directory finds nothing.
func TestBareNameIsNotAPath(t *testing.T) {
	t.Parallel()

	camp := newCampaign(t, homeSlug, "index.md", "notes/Goblin.md", "notes/deep/page.md")
	res := camp.resolver(t)

	// The naive join would look for `notes/deep/Goblin`, which does not exist.
	if camp.index.HasPath("notes/deep/Goblin") {
		t.Fatal("the campaign is wrong: `notes/deep/Goblin` exists, so this test proves nothing")
	}

	got := linkOne(t, res, "notes/deep/page.md", linkRef("Goblin"))
	if want := "/c/" + homeSlug + "/wiki/notes/Goblin"; got.Href != want {
		t.Errorf("href = %q, want %q", got.Href, want)
	}
}

// TestDuplicateBaseNamesResolveDeterministically: two pages can carry one name, and
// the answer must not depend on the order the store happened to return rows in.
func TestDuplicateBaseNamesResolveDeterministically(t *testing.T) {
	t.Parallel()

	forward := content.NewPageIndex([]domain.Page{
		{CampaignID: 1, Path: "zeta/Index.md"},
		{CampaignID: 1, Path: "alpha/Index.md"},
	})
	reverse := content.NewPageIndex([]domain.Page{
		{CampaignID: 1, Path: "alpha/Index.md"},
		{CampaignID: 1, Path: "zeta/Index.md"},
	})

	first, ok := forward.Lookup("Index")
	if !ok {
		t.Fatal("Lookup did not find a page called Index")
	}

	if first != "alpha/Index" {
		t.Errorf("Lookup = %q, want %q: the lexicographically first wins", first, "alpha/Index")
	}

	second, _ := reverse.Lookup("Index")
	if second != first {
		t.Errorf(
			"Lookup returned %q then %q for the same index in a different order",
			first,
			second,
		)
	}

	// With the same name in one campaign and one in a visible other, the first in
	// the caller's order wins — the caller's decision, because only the caller knows
	// which campaign the reader meant.
	home := newCampaign(t, homeSlug, "index.md")
	res := home.resolver(t)

	records := res.Records(linkOrigin(t, "index.md"), []content.Reference{linkRef("Index")},
		[]content.VisibleCampaign{
			{Slug: "aaa", Index: forward},
			{Slug: "zzz", Index: reverse},
		})

	if records[0].TargetCampaign != "aaa" {
		t.Errorf(
			"target campaign = %q, want the first listed: %q",
			records[0].TargetCampaign,
			"aaa",
		)
	}
}

// TestCampaignHrefEscapesAParentSegment: the exported constructors are the boundary
// a caller can reach without going through a reference, so they hold the line too.
func TestCampaignHrefEscapesAParentSegment(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{
			name: "a parent segment is escaped rather than emitted",
			got:  content.WikiHref(homeSlug, "notes/../../etc/passwd"),
			want: "/c/" + homeSlug + "/wiki/notes/%2E%2E/%2E%2E/etc/passwd",
		},
		{
			name: "the asset route is escaped the same way",
			got:  content.AssetHref(homeSlug, "images/../secret.png"),
			want: "/c/" + homeSlug + "/assets/images/%2E%2E/secret.png",
		},
		{
			name: "an empty path has no address",
			got:  content.WikiHref(homeSlug, ""),
			want: "",
		},
		{
			name: "the content root is not a page",
			got:  content.WikiHref(homeSlug, "."),
			want: "",
		},
		{
			name: "an empty slug has no address either",
			got:  content.WikiHref("", "notes/Goblin"),
			want: "",
		},
		{
			name: "the slug is escaped as a segment too",
			got:  content.WikiHref("we ird/slug", "notes/Goblin"),
			want: "/c/we%20ird%2Fslug/wiki/notes/Goblin",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.got != tc.want {
				t.Errorf("href = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// TestPageIndexKeys covers the three questions the index answers, because all three
// are load-bearing and one of them — HasUnder — decides whether a qualified
// reference crosses a campaign boundary at all.
func TestPageIndexKeys(t *testing.T) {
	t.Parallel()

	index := content.NewPageIndex([]domain.Page{
		{CampaignID: 1, Path: "index.md"},
		{CampaignID: 1, Path: "notes/Goblin.md"},
		{CampaignID: 1, Path: "notes/deep/Nested.md"},
		{CampaignID: 1, Path: ""},
	})

	if got := index.Len(); got != 3 {
		t.Errorf("Len = %d, want 3: the empty path is not a page", got)
	}

	for _, rel := range []string{"index", "index.md", "notes/Goblin", "notes/deep/Nested"} {
		if !index.HasPath(rel) {
			t.Errorf("HasPath(%q) = false, want true", rel)
		}
	}

	for _, rel := range []string{"notes/Goblin.md.md", "goblin", "notes/deep"} {
		if index.HasPath(rel) {
			t.Errorf("HasPath(%q) = true, want false", rel)
		}
	}

	for _, dir := range []string{"notes", "notes/deep"} {
		if !index.HasUnder(dir) {
			t.Errorf("HasUnder(%q) = false, want true: there is a page in it", dir)
		}
	}

	for _, dir := range []string{"notes/deeper", "lore", "index"} {
		if index.HasUnder(dir) {
			t.Errorf("HasUnder(%q) = true, want false", dir)
		}
	}
}

// TestADirectoryNamedLikeACampaignIsThisCampaign: when the home campaign happens to
// have a directory with the other campaign's name, the reference is read as a
// broken reference into *this* campaign — and it is read that way for every viewer,
// which is what keeps the markup permission-neutral.
func TestADirectoryNamedLikeACampaignIsThisCampaign(t *testing.T) {
	t.Parallel()

	camp := newCampaign(t, homeSlug, "index.md", publicSlug+"/Coast Road.md")
	res := camp.resolver(t)

	got := linkOne(t, res, "index.md", linkRef(rootRef(publicSlug, "Ghost")))

	if got.CrossCampaign {
		t.Error("cross-campaign = true, but this campaign has a directory of that name")
	}

	if !got.Broken {
		t.Error("broken = false, want true: the page is not there either way")
	}

	if want := "/c/" + homeSlug + "/wiki/" + publicSlug + "/Ghost"; got.Href != want {
		t.Errorf("href = %q, want %q", got.Href, want)
	}
}

// symlink plants a symlink inside the campaign's content root. `target` is either
// absolute (a path outside the root) or relative to the root.
func (f campaign) symlink(t *testing.T, rel, target string) {
	t.Helper()

	if !filepath.IsAbs(target) {
		target = filepath.Join(f.dir, filepath.FromSlash(target))
	}

	if err := os.MkdirAll(
		filepath.Dir(filepath.Join(f.dir, filepath.FromSlash(rel))),
		0o750,
	); err != nil {
		t.Fatalf("create parent of %s: %v", rel, err)
	}

	if err := os.Symlink(target, filepath.Join(f.dir, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("symlink %s: %v", rel, err)
	}
}

// assertLinkGolden compares got with the contents of testdata/links/name, or rewrites
// it under -update-links.
func assertLinkGolden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join(linkGoldenDir, name)

	if *updateLinkGoldens {
		if err := os.MkdirAll(linkGoldenDir, 0o750); err != nil {
			t.Fatalf("create %s: %v", linkGoldenDir, err)
		}

		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run: go test ./internal/content -update-links)", name, err)
	}

	if string(want) != got {
		t.Errorf(
			"%s does not match:\ngot:\n%s\nwant:\n%s\n(run: go test ./internal/content -update-links)",
			name,
			got,
			want,
		)
	}
}

// renderLinks is what a renderer does with the addresses: fill the element render.go
// left empty.
//
// It lives in the test because the element is ext's and the substitution is the
// caller's — C6's route, or C3's renderer once it takes an address table. What is
// under test is that the *addresses* are identical for every viewer, so the
// renderer has to be the simplest thing that consumes them: any variation here would
// be a variation in this file, not in the code being tested.
func renderLinkMarkup(links []content.Link) string {
	var body strings.Builder

	for _, link := range links {
		label := linkEscape(link.Label)

		switch {
		case link.Refusal != content.Allowed:
			body.WriteString(`<span class="link-refused" title="` +
				linkEscape(link.Refusal.Reason()) + `">` + label + "</span>\n")
		case link.Kind == content.LinkToAsset && link.Extension == ext.KindEmbed:
			body.WriteString(`<img src="` + link.Href + `" alt="` + label + `">` + "\n")
		default:
			broken := ""
			if link.Broken {
				broken = ` data-broken="true"`
			}

			body.WriteString(`<a href="` + link.Href + `"` + broken +
				` data-ref-index="` + linkIndex(link.Index) + `">` + label + "</a>\n")
		}
	}

	return body.String()
}

// renderRecords is the broken-link report's view of one page, as the tab-separated
// text a report shows a GM.
func renderLinkRecords(records []content.Outbound) string {
	var out strings.Builder

	for _, rec := range records {
		campaign, target, written := rec.TargetCampaign, rec.TargetPath, rec.Reference
		if rec.Refusal != content.Allowed {
			campaign, target = "-", "-"
		}

		// A `{{…}}` reference has no target at all, which is not the same as a
		// target that happened to be empty: a reader sent looking for a blank line in
		// their own file is looking for the wrong thing.
		if written == "" {
			written = "(no target)"
		}

		out.WriteString(linkIndex(rec.Index) + "\t" + written + "\t" +
			rec.Refusal.String() + "\t" + linkYesNo(rec.Resolved) + "\t" +
			campaign + "\t" + target + "\n")
	}

	return out.String()
}

// escapeWhole is `url.PathEscape` over a whole path, which is the thing per-segment
// escaping exists to avoid. Asserted against so the reason is a test and not a
// claim.
func linkEscapeWhole(path string) string {
	return url.PathEscape(path)
}

// itoa is strconv.Itoa, for the two places a number reaches a golden as text.
func linkIndex(value int) string {
	return strconv.Itoa(value)
}

// escapeHTML is the escaping a renderer owes author text. `html.EscapeString` and
// not a home-grown version, so the goldens are not asserting this file's idea of
// what an attribute terminator is.
func linkEscape(value string) string {
	return html.EscapeString(value)
}

// boolText is "yes"/"no" for the report golden, so the golden is readable and a
// boolean column cannot be mistaken for a campaign slug.
func linkYesNo(value bool) string {
	if value {
		return "yes"
	}

	return "no"
}
