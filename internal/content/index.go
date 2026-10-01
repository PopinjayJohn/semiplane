// The page index, built by walking a campaign's content root.
//
// This is the `internal/content/index.go` the phase plan reserves, and the
// question it answers is "which pages does this campaign contain" — the input
// `links.go` needs to resolve a bare `[[Goblin]]` by name.
//
// # Why the filesystem, and not the `pages` table
//
// ADR 0006 makes the filesystem the source of truth and the database a
// *rebuildable index*. Building the index by walking is therefore the definition
// of correct, not a fallback: a page exists if and only if its file does.
//
// It is also, in this phase, the only thing that works. The `pages` table exists
// — migration 0007 created it, with the FTS5 shadow and the external-content
// index — but nothing writes it until the watcher (P4) indexes the tree, and a
// request-time index that consults an empty table resolves *nothing*. The
// observable result of doing it the other way round is a wiki where every
// wikilink is marked `data-broken="true"` because nothing has been indexed yet,
// which is a broken product rather than an unimplemented optimisation.
//
// So: walk now, and let P4 replace this with the maintained index. The seam is
// one interface in the route, and the swap is a change to the composition root.
//
// # What a walk costs
//
// One `ReadDir` per directory per cache miss. For the vault sizes this project
// targets — a campaign is tens or hundreds of pages, and a request that misses
// the render cache is a page that changed — that is not a cost worth optimising
// before the thing that makes it irrelevant exists. When the watcher maintains
// the table, this whole file becomes a test fixture rather than a code path.

package content

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/semiplane/semiplane/internal/domain"
)

// markdownExtension is the only file extension a page can have.
//
// A `.md` and nothing else, and that is what makes a directory listing a page
// listing. The alternative — indexing every file and letting the route filter —
// puts a filename convention in the wrong layer and makes the index larger than
// the thing it indexes.
const markdownExtension = ".md"

// pageIndexMaxBytes caps a file the walk will read front matter from.
//
// The walk reads each page's head to learn its title and kind, and a vault is
// untrusted input (S-3.1, S-4.7). The cap is the same order as
// `MaxDocumentBytes` and exists so a 4 GB file named `.md` cannot be opened by an
// index build. It is *not* the document cap: a page larger than this is still
// readable through the route, which enforces its own limit on the read path.
const pageIndexMaxBytes = 1 << 20

// BuildPageIndex lists a campaign's pages by walking its content root.
//
// Returns the index, and an error only for a failure that is *not* per-file. A
// single unreadable file is skipped: a vault with one bad file should index the
// rest, and a build that fails wholesale because of one file would make the wiki
// unavailable for a reason the operator cannot see from a 500.
//
// The error that *is* returned is a failure to walk the root at all — it is gone,
// or it is not readable — which is the degraded case `campaignroots` reports at
// startup.
func BuildPageIndex(root *Root, campaignID int64) (PageIndex, error) {
	paths, err := walkMarkdown(root)
	if err != nil {
		return PageIndex{}, err
	}

	pages := make([]domain.Page, 0, len(paths))

	for _, rel := range paths {
		page, err := indexPage(root, rel, campaignID)
		if err != nil {
			// Skipped rather than fatal, and named here so the reason is the one
			// a reader of this function would expect: a file the index cannot
			// read is a file whose name still resolves, just not one whose title
			// is known. Its links are what break, not the page.
			continue
		}

		pages = append(pages, page)
	}

	return NewPageIndex(pages), nil
}

// walkMarkdown returns every `.md` path under the root, root-relative.
func walkMarkdown(root *Root) ([]string, error) {
	var found []string

	walk := func(rel string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A directory we cannot read is skipped, not fatal. `Walk`'s own
			// contract is that returning nil continues the walk, and a campaign
			// with one unreadable subdirectory is still mostly readable.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}

			return nil
		}

		if entry.IsDir() {
			// `.git`, `.obsidian` and friends. Obsidian writes both into a
			// vault, and indexing its own state files as pages would put entries
			// in the resolver that no reader can ever reach.
			if skipDirectory(entry.Name()) {
				return fs.SkipDir
			}

			return nil
		}

		if strings.HasSuffix(entry.Name(), markdownExtension) {
			found = append(found, rel)
		}

		return nil
	}

	if err := root.Walk(walk); err != nil {
		return nil, fmt.Errorf("walk content root for %s: %w", root.Slug(), err)
	}

	return found, nil
}

// skipDirectory reports whether a directory name is never a page's parent.
func skipDirectory(name string) bool {
	switch name {
	case ".git", ".obsidian", ".trash", ".stfolder", "node_modules":
		return true
	default:
		return false
	}
}

// indexPage reads one page's head to learn its title and kind.
//
// Deliberately does not hash the body. The `content_hash` in the route's cache key
// is computed from the bytes the route *read*, so a second read here would be a
// second read of every page on every cache miss, and a hash computed from a
// partial read would not be the hash the route uses — which would make the two
// disagree and the cache never hit.
//
// The file is read through the confined root, capped, and parsed with the same
// `Parse` the route uses, so an index entry and a rendered page cannot disagree
// about a page's title.
func indexPage(root *Root, rel string, campaignID int64) (domain.Page, error) {
	target, err := root.At(rel)
	if err != nil {
		return domain.Page{}, err
	}

	info, err := target.Stat()
	if err != nil {
		return domain.Page{}, err
	}

	if info.Size() > pageIndexMaxBytes {
		return domain.Page{}, fmt.Errorf(
			"page %s is %d bytes, over the index cap",
			rel,
			info.Size(),
		)
	}

	raw, err := target.ReadFile()
	if err != nil {
		return domain.Page{}, err
	}

	doc := Parse(raw, nil)

	// The kind is resolved with no registry, so it is always prose. That is
	// correct for an index: the registry belongs to the renderer, and the index
	// exists to answer "what is this page called", not "what renders it". A page
	// whose kind matters to a reader is a page the route resolved already.
	return domain.Page{
		CampaignID: campaignID,
		Path:       rel,
		Kind:       domain.KindProse,
		Title:      doc.FrontMatter.Title,
		// The modified time is the only time this function can know, and it is
		// not used for cache validity (S-5.2) — it is carried so a caller that
		// wants to show it does not have to stat again.
		CreatedAt: info.ModTime().UTC(),
		UpdatedAt: info.ModTime().UTC(),
	}, nil
}
