// The page index, in its two forms.
//
// A campaign's pages exist on disk and nowhere else — S-3.1 and ADR 0006 make the
// filesystem the source of truth and this file a *derivation* of it. Both halves
// below derive, and they differ only in when they run:
//
//   - `BuildPageIndex` walks a content root and answers one question — which
//     pages does this campaign contain — for link resolution. It is what the
//     composition root calls on a render cache miss, and it stays because link
//     resolution needs a *complete, current* answer at the moment of the render
//     and a maintained table that is one dropped event behind is not one.
//   - `Indexer` keeps the `pages` table in step with the tree, which is what
//     makes search (S-8.2) and the content hash the wiki route caches on (S-5.2)
//     answerable at all. It is fed settled `Change`s from the watcher (S-4.1) and
//     by two convergence operations that do not care whether the events arrived.
//
// # Convergence, not bookkeeping
//
// The second half is the interesting one, because a directory watcher is a lossy
// narrator. Events are dropped (an exhausted watch limit, S-4.5), coalesced, and
// reordered; a sync client moves a directory as one event and deletes a hundred
// files as a hundred more. Any index maintained purely by applying events is
// correct only for the events it received, which makes "the index matches the
// tree" a property of the filesystem's timing rather than of this code.
//
// So the invariant is stated against the tree rather than against the stream: a
// page is indexed if and only if a readable `.md` file says so, and
// `ReindexCampaign` — walk, index, prune — reaches that state from any starting
// point, including none. `PrunePages` is the half that makes it a claim: without
// it, a dropped delete event leaves a row, and a row is a search result that
// 404s.
//
// # `body_plain` is derived here, from the source
//
// The column is what a search snippet is built from, so it is a security surface
// rather than a convenience: S-5.11 and migration 0007 both promise that
// `[!secret]` callout content is excluded from it **in every reveal state**. The
// derivation is therefore from the file's own text, never from rendered HTML,
// and the argument for that is in `plainBody` below. ADR 0031 records the
// decision.

package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
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

// pageIndexMaxBytes caps a file the index will read.
//
// The index reads each page whole, and a vault is untrusted input (S-3.1, S-4.7).
// The cap is the same order as `MaxDocumentBytes` and exists so a 4 GB file named
// `.md` cannot be opened by an index build. It is *not* the document cap: a page
// larger than this is still readable through the route, which enforces its own
// limit on the read path. Such a page is skipped and reported rather than indexed
// with a body that stops halfway.
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
			// The root arrives as ".", and "." matches the leading-dot rule
			// below — so it is named out loud here. Skipping the root would skip
			// the whole tree, and the walk would return an empty index for a
			// campaign whose pages are all present, which is the exact failure the
			// walk exists to prevent.
			if rel != rootDirName && skipDirectory(entry.Name()) {
				return fs.SkipDir
			}

			return nil
		}

		if indexablePath(rel) {
			found = append(found, rel)
		}

		return nil
	}

	if err := root.Walk(walk); err != nil {
		return nil, fmt.Errorf("walk content root for %s: %w", root.Slug(), err)
	}

	return found, nil
}

// skipDirectory reports whether a directory is never a page's parent.
//
// The rule is the leading dot, and the reason it replaces an explicit list is
// that the list was always an enumeration of the same thing: `.git`,
// `.obsidian`, `.trash` and `.stfolder` are four spellings of "an editor, a sync
// client or this process writes its own bookkeeping here, and Obsidian hides it
// from the vault's own file listing". `node_modules` is the one that is not a dot,
// and it is the one that would otherwise pull a dependency tree into a wiki
// index.
func skipDirectory(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}

	return name == "node_modules"
}

// indexablePath reports whether a root-relative path is a page the index keeps.
//
// One predicate for both the walk and the incremental path, and that is the whole
// point of it: an indexer that applied an upsert for a file the walk had skipped
// would leave a row for a page the next rescan prunes, and the visible symptom is
// a page that appears in search results on a filesystem event and vanishes on the
// next tick. The two agreeing is what makes convergence mean anything.
//
// Dot-prefixed *components* are excluded, not just dot-prefixed directories: the
// atomic write stages through `.semiplane-…` (root.go), and Obsidian treats every
// dot-prefixed name as hidden. The cost is that a page under a dot-directory
// renders at its URL but is not indexed or searchable — which is the behaviour
// Obsidian itself gives such a file, and the alternative (indexing them) would
// put this process's temporary files into the search index.
func indexablePath(rel string) bool {
	if !strings.HasSuffix(rel, markdownExtension) {
		return false
	}

	// Every component, not just the last, and through skipDirectory rather than
	// the dot rule alone — so `node_modules/a-page.md` is refused here exactly as
	// the walk refuses it. Two predicates would be two policies.
	for segment := range strings.SplitSeq(rel, "/") {
		if skipDirectory(segment) {
			return false
		}
	}

	return true
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
	raw, info, err := readSource(root, rel)
	if err != nil {
		return domain.Page{}, err
	}

	doc := Parse(raw, nil)

	// The kind is resolved with no registry, so it is always prose. That is
	// correct for this index: the registry belongs to the renderer, and the index
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

// readSource reads one page's bytes through the confined root, and its stat.
//
// `Root.At` is the confinement boundary and it is the only way in: a path handed
// to this file as a string is never opened with `os`. The cap is checked against
// the stat so an oversized file is never read rather than read and then measured,
// and the returned stat is the one the walk's caller uses for a modification time.
func readSource(root *Root, rel string) ([]byte, fs.FileInfo, error) {
	target, err := root.At(rel)
	if err != nil {
		return nil, nil, err
	}

	info, err := target.Stat()
	if err != nil {
		return nil, nil, err
	}

	if info.Size() > pageIndexMaxBytes {
		return nil, nil, fmt.Errorf(
			"page %s is %d bytes, over the index cap",
			rel,
			info.Size(),
		)
	}

	raw, err := target.ReadFile()
	if err != nil {
		return nil, nil, err
	}

	return raw, info, nil
}

// PageStore is the persistence the Indexer needs.
//
// An interface declared by the consumer rather than a dependency on `store`,
// because the content package is the filesystem and the store is the database:
// importing one into the other would make the read path untestable without a
// connection, and would put the index's writer inside the package that owns
// nothing but path confinement and parsing. `*store.Store` satisfies this
// structurally, so the composition root hands it over and no adapter exists.
type PageStore interface {
	// UpsertPage writes one row and brings the FTS index in step with it.
	UpsertPage(ctx context.Context, indexed domain.PageText) (domain.Page, error)

	// DeletePage removes one row and its FTS entries, and reports ErrNotFound
	// when there was no row.
	DeletePage(ctx context.Context, campaignID int64, path string) error

	// RenamePage re-keys one row, keeping its identity and its FTS entries.
	RenamePage(ctx context.Context, campaignID int64, oldPath, newPath string) error

	// RenamePagesUnder moves every row in one directory subtree and returns how
	// many moved.
	RenamePagesUnder(ctx context.Context, campaignID int64, oldDir, newDir string) (int, error)

	// PagesForCampaign lists a campaign's rows as stored, which is how a
	// reindex learns the hashes it already holds.
	PagesForCampaign(ctx context.Context, campaignID int64) ([]domain.Page, error)

	// PrunePages deletes the rows whose path its predicate refuses.
	PrunePages(ctx context.Context, campaignID int64, keep func(path string) bool) (int, error)

	// RebuildPagesFTS rebuilds the search index in full (S-11.2).
	RebuildPagesFTS(ctx context.Context) error
}

// ErrNoContentRoot is what an Indexer reports when a campaign's root is not open.
//
// A separate value from the store's own errors because the condition is not a
// persistence failure and must not be mistaken for one: it is S-4.5's degraded
// campaign, which the server still starts for, and it resolves when the root is
// retained rather than by retrying the write.
var ErrNoContentRoot = errors.New("content: campaign has no content root")

// Indexer keeps the `pages` table in step with the content roots it is given.
//
// A value the composition root constructs and passes down, for the reason
// `content.Registry` exists: the set of campaigns with a root is a fact about
// this process and belongs in one place that is not a package global. There is no
// `init()` here and no registration by import.
//
// Safe for concurrent use. Every field is written once during construction, and
// `os.Root` — which every read goes through — is safe for concurrent use itself.
type Indexer struct {
	roots *Registry
	pages PageStore
	kinds domain.PageKindRegistry
}

// NewIndexer returns an Indexer that reads through roots and writes through
// pages.
//
// kinds may be nil, in which case every page is prose, which is what
// `content.Parse` does with no registry (S-3.3). It is a parameter rather than a
// hard-coded empty registry so that the composition root can hand over the plugin
// registry when phase 8 builds one and the index honours `kind: token` without a
// second edit.
func NewIndexer(roots *Registry, pages PageStore, kinds domain.PageKindRegistry) *Indexer {
	return &Indexer{roots: roots, pages: pages, kinds: kinds}
}

// HandleChange applies one settled change, and reports failures rather than
// returning them.
//
// A `ChangeSink`, so it is what the watcher's goroutine hands changes to without
// knowing anything else about the index. It logs and returns because a sink has
// nowhere to return to: a watcher calling a change handler must not stop watching
// because one page could not be indexed, and the failure that does matter is the
// one the caller *cannot* see — a dropped event — which is what the periodic
// rescan is for (S-4.5).
//
// A failure here is a page that is not indexed, not a page that is wrong: nothing
// this does writes a row from bytes it did not read whole. That is what makes
// logging-and-continuing safe rather than merely convenient.
func (ix *Indexer) HandleChange(ctx context.Context, change Change) {
	if err := ix.ApplyChange(ctx, change); err != nil {
		slog.ErrorContext(ctx, "index.change_failed",
			slog.String("event", "index.change_failed"),
			slog.String("campaign", change.Slug),
			slog.Int64("campaign_id", change.CampaignID),
			slog.String("op", change.Op.String()),
			slog.String("path", change.Path),
			slog.String("error", err.Error()),
		)
	}
}

// ApplyChange is HandleChange with a return value, so a caller that is not a
// goroutine — the rescan, a test — can see what happened.
//
// The three operations are dispatched here and nowhere else, so `Change`'s
// vocabulary has exactly one interpreter.
//
// # The fallback on rename
//
// `RenamePage` reports failure for two sequences that are entirely legal and
// would otherwise leave a row behind: a rename the watcher saw as a remove
// followed by a rename, where the source row is already gone, and a rename onto a
// path that already has a row, where the destination wins because the filesystem
// has already decided which file is at that path. Rather than tell those apart —
// which would mean importing the store's sentinels into this package, and which
// `PruneMissingPages` would clean up anyway — the indexer drops the source and
// indexes the destination. That is the same answer for both, and it is the answer
// that converges.
func (ix *Indexer) ApplyChange(ctx context.Context, change Change) error {
	switch change.Op {
	case OpUpsert:
		_, err := ix.indexPath(ctx, change.Slug, change.CampaignID, change.Path, "")

		return err

	case OpRemove:
		// A file the index never had, or a row whose deletion failed for a real
		// reason, both land here. The prune decides which; the log says which
		// happened.
		if err := ix.pages.DeletePage(ctx, change.CampaignID, change.Path); err != nil {
			slog.WarnContext(ctx, "index.remove_failed",
				slog.String("event", "index.remove_failed"),
				slog.String("campaign", change.Slug),
				slog.Int64("campaign_id", change.CampaignID),
				slog.String("path", change.Path),
				slog.String("error", err.Error()),
			)

			return fmt.Errorf("remove %s from the index: %w", change.Path, err)
		}

		return nil

	case OpRename:
		return ix.rename(ctx, change)

	default:
		return fmt.Errorf("content: cannot apply a change with operation %d", int(change.Op))
	}
}

// IndexReport counts what one convergence pass did.
//
// A value rather than a log line because the numbers are how a caller decides
// whether to try again: a pass that indexed nothing and pruned nothing has
// nothing left to do, and a pass that skipped files has a vault to look at.
type IndexReport struct {
	// Found is how many `.md` files the walk saw.
	Found int
	// Written is how many rows were written.
	Written int
	// Unchanged is how many files hashed to what the row already held, and were
	// therefore not written at all.
	Unchanged int
	// Skipped is how many files the walk found and the index could not read.
	Skipped int
	// Pruned is how many rows were deleted for want of a file.
	Pruned int
}

// ReindexCampaign brings one campaign's rows in line with its content root, and
// is the boot path, the rescan fallback (S-4.5) and the recovery path in one.
//
// Walk, index what the walk found, prune what the walk did not. The order is
// chosen so that the intermediate state is the safe one: the walk collects before
// anything is written, so a failure half-way leaves a campaign that is missing
// pages rather than one that has pages for files which are not there.
//
// A file the index cannot read is skipped and reported, never fatal, for the
// reason BuildPageIndex gives — and a page whose front matter did not interpret
// is *indexed* rather than skipped, because S-3.3 says such a page renders as
// prose and refusing to index it would break every link to a page that works.
func (ix *Indexer) ReindexCampaign(
	ctx context.Context,
	slug string,
	campaignID int64,
) (IndexReport, error) {
	root, err := ix.root(slug)
	if err != nil {
		return IndexReport{}, err
	}

	paths, err := walkMarkdown(root)
	if err != nil {
		return IndexReport{}, err
	}

	var report IndexReport

	report.Found = len(paths)

	// The stored hashes, so a rescan of an unchanged vault costs one query per
	// page that changed rather than one write transaction per page. A periodic
	// full rescan is a rescan because watch limits were exhausted (S-4.5), and a
	// fallback that writes the whole vault every tick is a fallback nobody leaves
	// running.
	known, err := ix.pages.PagesForCampaign(ctx, campaignID)
	if err != nil {
		return IndexReport{}, fmt.Errorf("read the indexed pages of %s: %w", slug, err)
	}

	hashes := make(map[string]string, len(known))
	for idx := range known {
		hashes[known[idx].Path] = known[idx].ContentHash
	}

	live := make(map[string]struct{}, len(paths))

	for _, rel := range paths {
		live[rel] = struct{}{}

		written, indexErr := ix.indexPath(ctx, slug, campaignID, rel, hashes[rel])
		if indexErr != nil {
			report.Skipped++

			ix.reportSkipped(ctx, slug, rel, indexErr)

			continue
		}

		if written {
			report.Written++
		} else {
			report.Unchanged++
		}
	}

	pruned, err := ix.pages.PrunePages(ctx, campaignID, func(rel string) bool {
		_, kept := live[rel]

		return kept
	})
	if err != nil {
		return report, fmt.Errorf("prune the index of %s: %w", slug, err)
	}

	report.Pruned = pruned

	return report, nil
}

// PruneMissingPages deletes the rows of one campaign whose file is not there, and
// returns how many it deleted.
//
// The half of convergence that has no event to hang off. A watch limit exhausted
// at startup, a directory moved in by a sync client that emitted one event for
// the directory, a file deleted while the process was not running: each of those
// leaves a row, and a row is a search hit for a page that 404s. It walks to find
// what is there and deletes the difference.
//
// Separate from `ReindexCampaign` because it is the cheaper of the two and the
// one a caller wants when it knows the rows are stale but the bodies are not: it
// reads every file's path and none of its bytes.
func (ix *Indexer) PruneMissingPages(
	ctx context.Context,
	slug string,
	campaignID int64,
) (int, error) {
	root, err := ix.root(slug)
	if err != nil {
		return 0, err
	}

	paths, err := walkMarkdown(root)
	if err != nil {
		return 0, err
	}

	live := make(map[string]struct{}, len(paths))
	for _, rel := range paths {
		live[rel] = struct{}{}
	}

	pruned, err := ix.pages.PrunePages(ctx, campaignID, func(rel string) bool {
		_, kept := live[rel]

		return kept
	})
	if err != nil {
		return 0, fmt.Errorf("prune the index of %s: %w", slug, err)
	}

	return pruned, nil
}

// rename applies an OpRename, deciding by shape whether it moved a page or a
// directory.
//
// The convention — a rename whose paths do not both name `.md` files moved a
// directory — is here rather than in the watcher because it is a statement about
// what `pages` rows can be, which is this package's business: `pages_campaign_path_key`
// holds page paths, so a subtree rename has a whole-tree operation behind it and a
// per-page one does not. A watcher that reported a folder move as a per-file
// sequence would still converge, through the upsert path and the next prune; this
// is the cheaper route to the same place.
func (ix *Indexer) rename(ctx context.Context, change Change) error {
	if !indexablePath(change.OldPath) || !indexablePath(change.Path) {
		moved, err := ix.pages.RenamePagesUnder(
			ctx,
			change.CampaignID,
			change.OldPath,
			change.Path,
		)
		if err != nil {
			return fmt.Errorf("move the index entries under %s: %w", change.OldPath, err)
		}

		slog.InfoContext(ctx, "index.renamed_directory",
			slog.String("event", "index.renamed_directory"),
			slog.String("campaign", change.Slug),
			slog.Int64("campaign_id", change.CampaignID),
			slog.String("from", change.OldPath),
			slog.String("to", change.Path),
			slog.Int("pages", moved),
		)

		return nil
	}

	if err := ix.pages.RenamePage(
		ctx,
		change.CampaignID,
		change.OldPath,
		change.Path,
	); err == nil {
		return nil
	} else {
		slog.WarnContext(ctx, "index.rename_rekey_failed",
			slog.String("event", "index.rename_rekey_failed"),
			slog.String("campaign", change.Slug),
			slog.Int64("campaign_id", change.CampaignID),
			slog.String("from", change.OldPath),
			slog.String("to", change.Path),
			slog.String("error", err.Error()),
		)
	}

	// The destination is indexed and the source dropped, in that order, so a
	// failure part-way leaves a page indexed twice rather than not at all — and
	// the prune removes the loser on the next pass.
	if _, err := ix.indexPath(ctx, change.Slug, change.CampaignID, change.Path, ""); err != nil {
		return err
	}

	if err := ix.pages.DeletePage(ctx, change.CampaignID, change.OldPath); err != nil {
		slog.WarnContext(ctx, "index.rename_source_left",
			slog.String("event", "index.rename_source_left"),
			slog.String("campaign", change.Slug),
			slog.Int64("campaign_id", change.CampaignID),
			slog.String("path", change.OldPath),
			slog.String("error", err.Error()),
		)
	}

	return nil
}

// indexPath reads one page and writes its row, reporting whether it wrote one.
//
// knownHash is the `content_hash` the row already holds for this path, or "" when
// the row is new and the caller has said nothing. A page whose bytes hash to that
// is not written at all: an upsert of an unchanged page would move `updated_at`
// for no reason, and `updated_at` is what every "recently changed" listing reads.
//
// Returns (false, nil) without reading anything for a path that is not a page —
// an asset, a dot-file, a directory — because the watcher reports those changes
// and the correct answer for them is silence.
func (ix *Indexer) indexPath(
	ctx context.Context,
	slug string,
	campaignID int64,
	rel, knownHash string,
) (bool, error) {
	if !indexablePath(rel) {
		return false, nil
	}

	root, err := ix.root(slug)
	if err != nil {
		return false, err
	}

	raw, _, err := readSource(root, rel)
	if err != nil {
		return false, fmt.Errorf("read %s for indexing: %w", rel, err)
	}

	// The hash and the size are computed from the *same* bytes, which is the
	// property that makes a row internally consistent: a writer that hashed one
	// read and stat'ed another would record a `content_hash` for a file and a
	// `byte_size` for a different one, and during a burst of writes — a page being
	// saved while the watcher indexes it — the pair would disagree for as long as
	// the write lasted. S-4.3 settles *when* to read; this is what makes the
	// reading that follows worth keeping.
	digest := contentHash(raw)

	if knownHash == digest {
		return false, nil
	}

	doc := Parse(raw, ix.kinds)

	if doc.FrontMatter.Err != nil {
		// Indexed anyway, and reported, because S-3.3 makes a block that did not
		// interpret inert rather than fatal: the page renders as prose and the
		// route will say so. Skipping it here would break every link to a page
		// that works, which is the exact failure a malformed block must not cause.
		slog.WarnContext(ctx, "index.page_degraded",
			slog.String("event", "index.page_degraded"),
			slog.String("campaign", slug),
			slog.Int64("campaign_id", campaignID),
			slog.String("path", rel),
			slog.String("error", doc.FrontMatter.Err.Error()),
		)
	}

	_, err = ix.pages.UpsertPage(ctx, domain.PageText{
		CampaignID:  campaignID,
		Path:        rel,
		Kind:        doc.FrontMatter.Kind,
		Title:       doc.FrontMatter.Title,
		ContentHash: digest,
		ByteSize:    int64(len(raw)),
		BodyPlain:   plainBody(doc),
	})
	if err != nil {
		return false, fmt.Errorf("index %s: %w", rel, err)
	}

	return true, nil
}

// reportSkipped says that a file the walk found is not in the index.
//
// Warn and not error, because one unreadable page in a vault of five hundred is
// an operator's problem and not a degraded service — unlike `watch.add_failed`,
// which is S-12.2's "never below error" because a watcher that has stopped
// watching is silently losing every change after it.
func (ix *Indexer) reportSkipped(ctx context.Context, slug, rel string, err error) {
	slog.WarnContext(ctx, "index.page_skipped",
		slog.String("event", "index.page_skipped"),
		slog.String("campaign", slug),
		slog.String("path", rel),
		slog.String("error", err.Error()),
	)
}

// root resolves a slug to its confined content root.
func (ix *Indexer) root(slug string) (*Root, error) {
	root, err := ix.roots.Get(slug)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoContentRoot, slug)
	}

	return root, nil
}

// contentHash is the digest `pages.content_hash` holds.
//
// Hex, because `ETag` quotes it and an ETag cannot carry arbitrary bytes
// (S-5.3). Computed over the file's whole byte sequence — front matter included —
// because the render cache is keyed on the file and not on the prose.
func contentHash(raw []byte) string {
	sum := sha256.Sum256(raw)

	return hex.EncodeToString(sum[:])
}

// The markdown `body_plain` has to flatten, in the order they are applied.
//
// One pattern each, applied in sequence, rather than one parser: the goal is
// words for a search index and not a rendering, and a shared grammar would make
// this file a second renderer that has to be kept in step with the first without
// any of its guarantees. The order is the nesting order — a reference inside a
// link label is replaced before the label's own link is, so the label is left
// holding words rather than brackets.
//
// The exceptions, and why each is here:
//
//   - The *content* of a reference is kept, because `[[Svartalfheim]]` is how the
//     corpus names a place and a wiki that cannot be searched by it is a wiki
//     nobody uses. An alias wins over the target, as it does when rendered.
//   - The *text* of an HTML tag is not, and its markup is not either: goldmark
//     never interprets raw HTML (ADR 0028) and the sanitiser strips what it does
//     not allow, so an inline tag is punctuation between two words.
//   - A footnote marker and a task box become nothing, which is what they are.
var (
	// `![[embed]]` and `[[wikilink]]`, with the embed's leading `!` optional and
	// the alias optional after it. One pattern for both because they are the same
	// syntax with a role, and handling them separately is how one of them ends up
	// with a subtly different grammar.
	referencePattern = regexp.MustCompile(`!?\[\[([^[\]|]+)(?:\|([^[\]]*))?\]\]`)

	// `![alt](src)` before `[text](href)`, because the second would otherwise
	// match an image from its second character and leave a `!` behind.
	imagePattern = regexp.MustCompile(`!\[([^]]*)\]\([^)]*\)`)

	linkPattern = regexp.MustCompile(`\[([^]]*)\]\([^)]*\)`)

	// A footnote, in both spellings: the reference `[^note]` and the definition
	// `[^note]: …`. Removed whole, because the alternative leaves the label behind
	// as a searchable token — `[^1]` becoming `1` means every page with a numbered
	// footnote matches a search for "1".
	footnotePattern = regexp.MustCompile(`\[\^[^\]]*\]:?`)

	// A task box, `- [x]` or `- [ ]`. Removed whole for the same reason: stripped
	// of its brackets it becomes the single letter "x", and a search for "x" then
	// matches every checklist in the vault.
	taskBoxPattern = regexp.MustCompile(`\[[ xX]\]`)

	// An inline code span, keeping its content. A statblock or a dice expression
	// is prose to the person who wrote it and the words in it are what they will
	// search for.
	inlineCodePattern = regexp.MustCompile("`+([^`]+)`+")

	// An HTML tag, open or close. The character class stops at the first `<` or
	// `>` so that `a < b > c` loses no more than a tag-shaped run of it.
	htmlTagPattern = regexp.MustCompile(`</?[a-zA-Z][^<>]*>`)

	// An entity, which the renderer would have decoded. `&nbsp;` becoming a word
	// separator is the right answer and `&amp;` becoming "&" is the wrong one,
	// because the second is not a word anybody searches for.
	entityPattern = regexp.MustCompile(
		`&(?:[a-zA-Z][a-zA-Z0-9]{1,30}|#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6});`,
	)

	// The block markers at the start of a line: quote marks, a bullet, an
	// ordered marker, a run of heading hashes. The ordered marker is bounded to
	// two digits and required to be followed by a space, because an unbounded `\d+`
	// would strip the year off a sentence that begins "2024. The …".
	lineMarkerPattern = regexp.MustCompile(
		`^[ \t]*(?:>[ \t]*)*(?:(?:[-*+]|\d{1,2}[.)])[ \t]+)?(?:#{1,6}[ \t]*)?`,
	)

	// An Obsidian callout marker, `[!note]` or `[!warning]-`. Stripped here rather
	// than in the `[!secret]` test because the two are different problems: this one
	// is markup a reader never sees, and the other is a security boundary. A
	// general pattern rather than a list of types, because the type name is the
	// author's to choose and semiplane has no registry of them — and because a
	// marker that survived would leave the word "secret" in the index of a page
	// whose callout content is not.
	calloutPattern = regexp.MustCompile(`\[!\s*[^\]]*\][-+]?`)

	// A table's delimiter row: pipes, dashes, colons and spaces and nothing else,
	// with at least one dash so that an empty row of pipes is not silently dropped.
	//
	// Matched as a whole line and dropped as a whole line, rather than by putting
	// `-` in the punctuation class below — because `-` is prose punctuation, and a
	// snippet is read by a person: `Svartalfheim-gate` rendered as
	// `Svartalfheim gate` is a small lie about what the file says, taken in order to
	// remove a row of hyphens that matches nothing anybody searches for.
	tableDelimiterPattern = regexp.MustCompile(`^[ \t|:-]*-+[ \t|:-]*$`)

	// The punctuation that is markup rather than language, once the constructs
	// that carry meaning are gone. Every alternative is a character markdown
	// introduces and a reader would not type; `-` and `+` are deliberately absent,
	// because they are punctuation a reader *would* type.
	markupPattern = regexp.MustCompile("[*_~`|>!#\\[\\]{}]")
)

// The exclusion states `plainBody` moves between, named rather than a bool
// because the two states are genuinely different: one ends at the end of a
// blockquote, the other at the end of the document.
const (
	// includeNone is indexing the line normally.
	includeNone = iota

	// excludeQuote is inside a callout that is a blockquote, and ends at the
	// first line that is not part of it.
	excludeQuote

	// excludeRest is inside a `[!secret]` marker that is not a callout, and ends
	// at the end of the document.
	excludeRest
)

// secretMarkerPattern matches the `[!secret]` marker in every form that can open
// one.
//
// Case-insensitively and with an optional space, because Obsidian accepts
// `[!Secret]` and `[! secret]` and a vault written by a person rather than by a
// tool contains both. The fold marker that follows — `-` folded, `+` open — is
// deliberately not part of the pattern and is not inspected anywhere: S-5.11
// excludes callout content in *every* reveal state, so a pattern that matched one
// of them would be a bug waiting for a reveal, and the reveal is the one moment a
// secret-aware mistake becomes a real disclosure.
var secretMarkerPattern = regexp.MustCompile(`(?i)\[!\s*secret\]`)

// plainBody derives `body_plain` from a page's source.
//
// Derived from the file's own text and **never** from rendered HTML, and the
// reason is the argument for this whole file's most security-relevant line:
//
//   - Rendered output is permission-variant by construction (ADR 0028). There is
//     exactly one deliberate exception, `[!secret]`, and a redaction rule that is
//     checked against rendered text is a rule checked against two different
//     documents depending on who asked — which is the shape of bug S-5.6 exists
//     to prevent.
//   - The renderer's buffers are the wrong place to read text out of at all
//     (ADR 0029): between the file and the HTML there are three of them, and the
//     non-GM pipeline's guarantee is about what is *downstream of the redactor*.
//     A value derived by rendering would be derived before it, on the GM's answer,
//     and stored.
//   - And the exclusion would be untestable in the way that matters. S-5.11 is a
//     promise about the column, and a test can assert the column. It cannot
//     assert the absence of a leak from a pipeline it does not run.
//
// Only the body is indexed. The front matter is stripped, because a front-matter
// value can be a path (S-3.5) and the index does not need the host's directory
// layout to be searchable; and because what a front-matter field is worth to a
// search is not knowable from here — a plugin's fields arrive in phase 8.
//
// The exclusion rule is deliberately blunt, and every branch of it fails toward
// hiding:
//
//   - A `[!secret]` marker inside a blockquote excludes the rest of that
//     blockquote, which is the callout.
//   - A `[!secret]` marker anywhere else excludes everything below it. There is
//     no marker that says where a secret stops, and a `[!secret]` in a code fence
//     or in ordinary prose is an author's example of the syntax or a mistake —
//     neither of which is worth guessing at. The cost is that a page documenting
//     the callout syntax loses the text after it from the search index, which is
//     the cost of not leaking.
//   - The marker test runs on **every** line, before and independently of any
//     fence tracking. A fence that is opened and never closed cannot cause a
//     `[!secret]` to be indexed, because fence state never suppresses the
//     exclusion — which is the property a fence-tracking implementation gets
//     wrong, and the reason the two concerns are not one loop.
//
// An empty result is a legitimate outcome: a page whose whole body is one secret
// callout indexes to no body text. Its title still reaches the index, so the page
// is still findable by name — which is the point of S-5.11's cost, that secrets
// are not full-text searchable. A secret-aware index is an additive later change.
func plainBody(doc Document) string {
	var kept []string

	// The fence, so that a line inside a code block keeps the characters a reader
	// would search for — `fn main()`, a statblock header — instead of being
	// stripped of punctuation that is not markup where it sits. It affects
	// flattening only, never the exclusion; see the doc comment.
	fenced := false

	state := includeNone

	for line := range strings.SplitSeq(doc.Body, "\n") {
		trimmed := strings.TrimSpace(line)

		switch state {
		case excludeRest:
			continue
		case excludeQuote:
			if isQuoteLine(trimmed) {
				continue
			}

			state = includeNone
		case includeNone:
		}

		if secretMarkerPattern.MatchString(line) {
			if isQuoteLine(trimmed) {
				state = excludeQuote
			} else {
				state = excludeRest
			}

			continue
		}

		if marker, isFence := codeFence(trimmed); isFence {
			fenced = !fenced || marker != fenceMarker

			continue
		}

		if fenced {
			kept = append(kept, strings.TrimSpace(line))

			continue
		}

		if flat := flattenLine(line); flat != "" {
			kept = append(kept, flat)
		}
	}

	return strings.Join(kept, " ")
}

// fenceMarker is the fence spelling a code block opens and closes with.
//
// Three characters rather than the run's full length, so that a document which
// opens ``` and closes ```` is treated as closed. The imprecision is in the
// direction of treating a line as prose, which can only mean a marker that should
// have been excluded is excluded *earlier* — see plainBody's third argument.
const fenceMarker = "```"

// codeFence reports whether a trimmed line opens or closes a code block, and
// with which marker.
func codeFence(trimmed string) (string, bool) {
	if !strings.HasPrefix(trimmed, fenceMarker) && !strings.HasPrefix(trimmed, "~~~") {
		return "", false
	}

	return trimmed[:len(fenceMarker)], true
}

// isQuoteLine reports whether a trimmed line is part of a blockquote.
//
// `>` with nothing required after it, because `>` alone is an empty quote line
// and a callout is allowed to contain one — which is how a two-paragraph secret
// is written.
func isQuoteLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, ">")
}

// flattenLine reduces one line of markdown to the words in it.
//
// Every substitution replaces with a space rather than nothing, so that joining
// two constructs does not fuse the words on either side of them into a third.
func flattenLine(line string) string {
	if tableDelimiterPattern.MatchString(line) {
		return ""
	}

	text := calloutPattern.ReplaceAllString(line, " ")
	text = lineMarkerPattern.ReplaceAllString(text, " ")
	text = htmlTagPattern.ReplaceAllString(text, " ")
	text = referencePattern.ReplaceAllStringFunc(text, referenceWords)
	text = imagePattern.ReplaceAllString(text, " $1 ")
	text = linkPattern.ReplaceAllString(text, " $1 ")
	text = footnotePattern.ReplaceAllString(text, " ")
	text = taskBoxPattern.ReplaceAllString(text, " ")
	text = inlineCodePattern.ReplaceAllString(text, " $1 ")
	text = entityPattern.ReplaceAllString(text, " ")
	text = markupPattern.ReplaceAllString(text, " ")

	return strings.Join(strings.Fields(text), " ")
}

// referenceWords is what a `[[…]]` reference contributes.
//
// The alias when there is one, and the target's own name otherwise: `[[Svartalfheim]]`
// is how the corpus writes it, and `[[lore/Deep Page]]` is how the file is named,
// so the name is what a reader would type. The `.md` is dropped because it is
// not part of the name and FTS would index it as the token `md`.
func referenceWords(match string) string {
	// Whole match, target, alias. The pattern has exactly two capture groups, so
	// a shorter slice means the pattern changed and this helper did not, and the
	// words are lost for one revision rather than the index panicking on a
	// campaign's whole tree.
	const (
		targetGroup = 1
		aliasGroup  = 2
		groupCount  = 3
	)

	groups := referencePattern.FindStringSubmatch(match)
	if len(groups) < groupCount {
		return " "
	}

	if alias := groups[aliasGroup]; alias != "" {
		return " " + alias + " "
	}

	return " " + strings.TrimSuffix(path.Base(groups[targetGroup]), markdownExtension) + " "
}
