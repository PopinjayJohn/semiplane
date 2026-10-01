// Package content reads and writes a campaign's pages, with every path
// confined to that campaign's own `os.Root`.
//
// The confinement is why this package exists, and why it is a package rather
// than a helper somewhere in the request path. S-3.5 makes every path in front
// matter attacker-reachable — Obsidian Sync is untrusted input — and a content
// root is a directory on a host that may also hold other campaigns, other
// accounts' files, and things semiplane has never heard of. A read path that can
// be talked into leaving the root is a read path into somebody else's campaign,
// and it fails as a *wrong page* rather than as an error, which is why it is
// worth a module of its own.
//
// Four properties follow, and they shape every line below:
//
//   - `os.Root` is the authority, not an approximation of it. Nothing here
//     cleans a path and compares prefixes. A path is handed to the root and the
//     answer is interpreted, because a second implementation of the same rule
//     is a second answer and it is the one that will be wrong.
//   - Nothing here hands out an absolute path. Every path a caller can see is
//     root-relative, so a path that escapes into a log line, a cache key or a
//     link href is confined by construction, and a bug in the caller cannot
//     widen it.
//   - The three ways a path can fail to be readable are three different
//     answers, and one of them must never be confused with another. A refusal
//     is not a 404, because a 404 is an answer about what exists.
//   - The registry is a value the composition root constructs and passes down.
//     Not a package global, and not something an `init()` fills in: a global is
//     a second source of truth about which campaigns have a root, it is shared
//     by every test in the process, and there is no ordering story that makes
//     it right. AGENTS.md is explicit that registration happens in the
//     composition root, and a map that fills up as a side effect of importing a
//     package is registration that happened somewhere nobody can see.
package content

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
)

// The answers a caller can act on. They are distinct types of fact, and the
// reason they are distinct is that they are distinct types of response.
//
// Conflating any two of them is a security bug rather than a simplification,
// and the one that matters most is ErrOutsideRoot against ErrNotExist: a 404
// is a statement about existence, so a 404 in reply to a path that leaves the
// root tells an attacker which paths exist outside it, and turns a page read
// into a probe for the layout of the host's filesystem.
//
// The messages carry no path, and that is a deliberate constraint rather than
// an omission. The caller always has the offending value — it came from the
// request or from a page — so it can log it, and a refusal whose text does not
// vary with the attacker's input cannot become a channel. ErrNotExist is the
// exception and it names its path, because a missing path inside a campaign the
// caller has already been authorised for is what a broken-link report is built
// from.
var (
	// ErrNotExist names a path that is inside the root and is not there. The
	// only one of these that says anything about existence, and a 404.
	ErrNotExist = errors.New("content: no such path in the content root")

	// ErrOutsideRoot names a path that leaves the root, or a symlink on the
	// path that does. A refusal: never a 404, and never a 404-shaped body.
	ErrOutsideRoot = errors.New("content: path leaves the content root")

	// ErrInvalidRef names a reference that never named a location relative to
	// the root: empty, absolute, or carrying a NUL. A 400, because the input
	// is a bug in the caller rather than an attempt.
	ErrInvalidRef = errors.New("content: malformed path")

	// ErrSymlink names a symlink the policy refuses. S-4.4's rejection, and
	// distinguishable from ErrOutsideRoot because the remedy is different: a
	// vault with a link in it is a configuration to look at, not an attack.
	ErrSymlink = errors.New("content: symlink in the content root")

	// ErrNotDir names a path asked for as a directory that is not one.
	ErrNotDir = errors.New("content: not a directory")

	// ErrNoRoot names a campaign with no retained content root. S-4.5 marks
	// such a campaign degraded; until then this is the caller's answer.
	ErrNoRoot = errors.New("content: campaign has no content root")

	// ErrDuplicateRoot names a second adoption of a slug that already has a
	// root. The alternative is replacing it, which loses a handle nobody
	// closed and quietly swaps the boundary a campaign's pages are confined by.
	ErrDuplicateRoot = errors.New("content: campaign already has a content root")
)

const (
	// tempPrefix opens the name the atomic write stages through, so a directory
	// listing that ignores dot-files never offers it and the indexer can skip
	// it without knowing what a temp file is.
	tempPrefix = ".semiplane-"

	// tempSuffix ends it. A `.tmp` name is not a `.md` name, so nothing that
	// walks a content root for pages picks it up by accident.
	tempSuffix = ".tmp"

	// rootDirName is how the content root names itself, to fs.ReadDir and to a
	// walk. The one place a path of "." is legitimate: a listing, and a tree
	// scan. At refuses it, so no Target and therefore no page cache key can
	// ever carry it.
	rootDirName = "."
)

// SymlinkPolicy states what a Root does with a symlink inside a content tree.
//
// `os.Root` already refuses a symlink that leaves the root and refuses an
// absolute one, so the difference between these two values is narrower than it
// looks: AllowSymlinks permits a link that stays inside the root, and
// RefuseSymlinks refuses that as well. S-4.4 says the second, and it is the
// zero value, so a Root built without a stated policy is the safe one.
type SymlinkPolicy int

const (
	// RefuseSymlinks refuses every symlink on a path.
	//
	// A content tree is a sync target, so a symlink in one arrived from
	// somewhere and the somewhere is not trusted. Refusing it is a decision not
	// to follow input, and it is also the only policy under which a link
	// cannot re-point a page at other content in the same root — a link is a
	// second name for a file, and a second name is a page identity nobody
	// indexed.
	RefuseSymlinks SymlinkPolicy = iota

	// AllowSymlinks follows a symlink that stays inside the root.
	//
	// For a campaign whose content root deliberately holds links: a shared
	// prologue directory, a symlinked handouts folder an operator made on
	// purpose. It buys nothing for a synced vault, and it is emphatically not
	// a way to reach outside — `os.Root` refuses that whatever this says.
	AllowSymlinks
)

// Dir is the directory a relative reference is interpreted against: the
// directory of the page the reference appears in.
//
// A named type rather than a string because the most likely mistake is passing
// the referring *page* where its *directory* belongs, and a string cannot catch
// that. The zero value is the root of the content tree, so the common case
// needs no construction and RootDir exists to say so out loud.
type Dir struct {
	rel string
}

// RootDir is the Dir of the content root itself: a reference resolved against
// it is relative to the top of the campaign.
func RootDir() Dir {
	return Dir{}
}

// DirOf returns the Dir a page's own relative references resolve against.
//
// The referring page's directory, which is what a relative link in markdown
// means and what makes `[[../sibling]]` in `notes/deep/page.md` a page in
// `notes/` rather than a page in the campaign root. A page path that does not
// stay inside the root is refused, and a caller should treat that as a broken
// page rather than as a reference it may follow.
func DirOf(page string) (Dir, error) {
	cleaned, err := cleanRef(page)
	if err != nil {
		return Dir{}, err
	}

	return Dir{rel: path.Dir(cleaned)}, nil
}

// String returns the directory as a root-relative slash path, with the root
// itself rendered as ".".
func (d Dir) String() string {
	if d.rel == "" {
		return rootDirName
	}

	return d.rel
}

// Root is one campaign's confined content tree.
//
// It owns an `*os.Root` and the slug the root belongs to. Safe for concurrent
// use: `os.Root` is, and every field is written once during construction. Every
// operation is a method rather than a free function taking a path, because a
// free function is a path a caller can build however it likes and this package
// cannot see.
type Root struct {
	slug   string
	root   *os.Root
	policy SymlinkPolicy
	fsys   fs.FS
}

// NewRoot opens dir as a campaign's confined content tree.
//
// dir is the absolute path the campaign's `content_root` column holds, created
// by the registrar at registration. It is opened here and nowhere else, so the
// process has exactly one place where a content root becomes a handle and a
// path from a request never becomes one.
//
// The policy is a parameter rather than a constant because AllowSymlinks is a
// legitimate answer for a hand-built tree, and a constant would make that
// campaign unservable instead of configured differently.
func NewRoot(slug, dir string, policy SymlinkPolicy) (*Root, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open content root for %s: %w", slug, err)
	}

	return &Root{
		slug:   slug,
		root:   root,
		policy: policy,
		fsys:   root.FS(),
	}, nil
}

// Slug returns the campaign this root belongs to. For logs; it is not a
// capability and it is not an authorisation input.
func (r *Root) Slug() string {
	return r.slug
}

// Close releases the root's handle.
//
// The registry calls this, and the composition root calls the registry. A
// caller holding a *Root does not: the handle is shared by every request for
// the campaign, so closing it from a handler would break every other handler
// in the process for as long as the server runs.
func (r *Root) Close() error {
	if err := r.root.Close(); err != nil {
		return fmt.Errorf("close content root for %s: %w", r.slug, err)
	}

	return nil
}

// At confines a root-relative path and returns a handle to it.
//
// The path need not exist. A save addresses a file that is about to be
// created, and refusing it here would mean the code that reads and the code
// that writes resolve paths through two different functions — which is how one
// of them ends up not resolving them at all. Everything `os.Root` refuses is
// still refused, at the operation, with the right answer.
//
// This is the entry point for a path that arrived as a path: the page path in a
// URL, a path from the database. It is not for a reference inside a document,
// which Resolve interprets.
func (r *Root) At(rel string) (*Target, error) {
	cleaned, err := cleanRef(rel)
	if err != nil {
		return nil, err
	}

	if err := r.checkSymlink(cleaned); err != nil {
		return nil, err
	}

	return &Target{root: r, rel: cleaned}, nil
}

// Resolve interprets ref relative to base and returns a handle to what it
// names, or one of the three answers below.
//
// ref is the path portion of a reference, already stripped of an Obsidian
// anchor or alias by whoever parsed it. This function is about confinement and
// the wikilink grammar is a separate decision with a separate owner; a caller
// that has an Obsidian link beginning with a slash means the top of the vault
// and must say so with RootDir rather than passing the slash through, because
// a leading slash here is a malformed reference and not a filesystem root.
//
// The three answers are deliberately not interchangeable:
//
//   - ErrNotExist: the path is inside the root and is not there. A 404.
//   - ErrOutsideRoot: the path leaves the root, or a symlink on it does. A
//     refusal, answered as a rejection and never as a 404. A 404 says "there is
//     nothing at this path", and an attacker who can tell that from "there is
//     nothing at this path *and* it is outside" has learned something about a
//     filesystem they were never granted.
//   - ErrInvalidRef: the reference never named a location. A 400.
//
// None of them carries the offending path. The caller has it — it came from the
// request or from a page — and an error whose text is a function of an
// attacker's input is a channel, however briefly it is open.
func (r *Root) Resolve(ref string, base Dir) (*Target, error) {
	if err := validRef(ref); err != nil {
		return nil, err
	}

	// Joined before At rather than inside it, so the base is applied to a
	// reference that has already been checked for absoluteness. path.Join
	// discards everything before an absolute component, and a check after the
	// join would therefore never see one.
	target, err := r.At(path.Join(base.rel, ref))
	if err != nil {
		return nil, err
	}

	// Existence is asked about only after confinement is settled. The reverse
	// order would let the shape of a path change whether a filesystem is
	// consulted at all, which is the difference between a 404 and a probe.
	if _, err := target.Stat(); err != nil {
		return nil, err
	}

	return target, nil
}

// List returns a directory's entries, with no "." and no ".." in them.
//
// rel may be the root itself, spelled "." or "", because listing a campaign's
// top level is the first thing an index does and requiring a caller to invent a
// name for it would be a rule with no purpose. ErrSymlink if any entry is a
// symlink the policy refuses, for the same reason Glob refuses one: a listing
// that quietly omits the link is indistinguishable from a directory that does
// not contain one, and the index built from it is wrong in a way nobody notices
// until a page is missing.
func (r *Root) List(rel string) ([]fs.DirEntry, error) {
	// The root is named by "." here, and only here. At refuses "." so that no
	// Target can carry it, which keeps a page's cache key from ever being ".";
	// a listing is the one operation for which the root is a legitimate
	// argument, and fs.ReadDir needs a name for it either way.
	dir := rootDirName
	if rel != "" && rel != rootDirName {
		target, err := r.At(rel)
		if err != nil {
			return nil, err
		}

		info, err := target.Stat()
		if err != nil {
			return nil, err
		}

		if !info.IsDir() {
			return nil, fmt.Errorf("%w: %s", ErrNotDir, target.rel)
		}

		dir = target.rel
	}

	entries, err := fs.ReadDir(r.fsys, dir)
	if err != nil {
		return nil, classify(dir, err)
	}

	for _, entry := range entries {
		if err := r.checkSymlink(path.Join(dir, entry.Name())); err != nil {
			return nil, err
		}
	}

	return entries, nil
}

// Glob returns the root-relative paths in the tree matching pattern, sorted.
//
// pattern is a path.Match pattern and not a recursive one, so "**" is two
// stars rather than "any depth"; use Walk for the tree. The pattern is confined
// before it is used, because fs.Glob answers a pattern that escapes with an
// empty list rather than an error, and a caller who asked for everything
// outside the root would be told the root is empty — a lie about the
// campaign's own content, and the kind an indexer cannot detect.
//
// ErrSymlink if a match is a symlink the policy refuses, rather than a short
// list. See List for why a quietly short set is worse than an error.
func (r *Root) Glob(pattern string) ([]string, error) {
	if _, err := cleanRef(pattern); err != nil {
		return nil, err
	}

	matches, err := fs.Glob(r.fsys, pattern)
	if err != nil {
		return nil, fmt.Errorf("glob: %w", err)
	}

	for _, match := range matches {
		if err := r.checkSymlink(match); err != nil {
			return nil, err
		}
	}

	return matches, nil
}

// Walk calls walk for every path in the tree, root-relative and slash
// separated, in lexical order. The root itself arrives as ".".
//
// A symlink the policy refuses is passed to walk as (path, entry, ErrSymlink)
// and the walk continues unless walk says otherwise, because a tree scan has to
// reach every page and a vault containing one link is a vault whose other pages
// still need indexing. Every other operation in this file treats a refused
// symlink as an error, because those address a specific set and a set that is
// quietly short is indistinguishable from one that is complete.
//
// The check is on the entry the walk already produced rather than an Lstat of
// the whole path, which is both free and sufficient: fs.WalkDir does not
// descend into a symlink, so every symlink in the tree arrives as a leaf entry
// whose Type is ModeSymlink.
//
// An error from walk is wrapped with the campaign's slug, so errors.Is still
// reaches the caller's own sentinel. fs.WalkDir turns SkipDir and SkipAll into
// a nil result before returning it, so neither of those comes back wrapped and
// a caller comparing with == is not affected.
func (r *Root) Walk(walk fs.WalkDirFunc) error {
	err := fs.WalkDir(r.fsys, rootDirName, func(rel string, entry fs.DirEntry, err error) error {
		if err != nil {
			return walk(rel, entry, err)
		}

		if entry.Type()&fs.ModeSymlink != 0 && r.policy == RefuseSymlinks {
			return walk(rel, entry, ErrSymlink)
		}

		return walk(rel, entry, nil)
	})
	if err != nil {
		return fmt.Errorf("walk content root for %s: %w", r.slug, err)
	}

	return nil
}

// checkSymlink reports whether any component of rel is a symlink the policy
// refuses.
//
// os.Root is the confinement and this is a policy layered on top of it: the
// root refuses a link that leaves, and this refuses a link that stays. The
// order is the only order there is — every operation on a Target has already
// been through At, so the check cannot be skipped by choosing a different entry
// point, and a Target is built once, so it cannot be turned off after the fact.
func (r *Root) checkSymlink(rel string) error {
	if r.policy == AllowSymlinks {
		return nil
	}

	if slices.ContainsFunc(prefixes(rel), r.linkAt) {
		return ErrSymlink
	}

	return nil
}

// linkAt reports whether prefix is itself a symlink.
//
// Every error is answered "no", and that is the point: this is a policy check
// and not a confinement check. A prefix that does not exist has nothing below it
// that can be a symlink, and the operation that follows says so with better
// precision than "no such path" for something three components long. A prefix
// `os.Root` refuses is already refused, and reporting it as a policy decision
// here would file an escape attempt as a configuration choice.
func (r *Root) linkAt(prefix string) bool {
	info, err := r.root.Lstat(prefix)

	return err == nil && info.Mode()&fs.ModeSymlink != 0
}

// Target is a path proven addressable inside one campaign's content root, and
// the only way this package hands out a path.
//
// Returned by At and Resolve rather than as a string, because a string is
// something a caller can log, cache, concatenate, and then open again with
// something that is not this package. A Target can only be used for what its
// root permits, so a Target that leaks into a log line leaks a page name and
// not the host's directory layout.
type Target struct {
	root *Root
	rel  string
}

// Path returns the target as a root-relative slash path, which is the form the
// cache key (S-5.2), the render pipeline and the watcher all need.
//
// Never an absolute path. An absolute one would carry the campaign's directory
// layout on the host into every cache key, log line and href, and it would be a
// path a caller could hand straight to os.Open — which is the one thing this
// package exists to prevent, published as a convenience.
func (t *Target) Path() string {
	return t.rel
}

// String returns the root-relative path, so that a Target formatted with %v
// prints its page path and not a pointer and a struct dump.
func (t *Target) String() string {
	return t.rel
}

// Dir returns the directory a reference inside this target's own page resolves
// against.
func (t *Target) Dir() (Dir, error) {
	return DirOf(t.rel)
}

// ReadFile returns the target's bytes, or one of the classified answers.
func (t *Target) ReadFile() ([]byte, error) {
	data, err := t.root.root.ReadFile(t.rel)
	if err == nil {
		return data, nil
	}

	// A directory is its own answer, and it is checked for on the failure path
	// only. `os.Root` reports it as EISDIR on Unix and as a permission error on
	// Windows, so there is no portable error to match — and matching the error
	// would make the answer depend on the platform the operator deployed on.
	// The stat costs nothing on the success path, which is the path a page read
	// takes every time.
	if info, statErr := t.root.root.Stat(t.rel); statErr == nil && info.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrNotDir, t.rel)
	}

	return nil, classify(t.rel, err)
}

// Open returns the target open for reading, positioned at the start.
//
// The handle a ranged asset read needs (P6): http.ServeContent wants an
// *os.File and a size, and handing it a path it can open itself would be a path
// that skipped the confinement.
func (t *Target) Open() (*os.File, error) {
	file, err := t.root.root.Open(t.rel)
	if err != nil {
		return nil, classify(t.rel, err)
	}

	return file, nil
}

// Stat returns the target's file information, following a symlink the policy
// allows.
func (t *Target) Stat() (fs.FileInfo, error) {
	info, err := t.root.root.Stat(t.rel)
	if err != nil {
		return nil, classify(t.rel, err)
	}

	return info, nil
}

// WriteFile replaces the target's bytes atomically: a temp file in the same
// directory, written, fsynced, renamed over the target, then the directory
// fsynced so the rename itself survives a crash. S-6.4.
//
// The temp file is in the same directory because a rename is only atomic within
// a filesystem, and a temp file in the system temp directory is a temp file on
// a different one. That version publishes a truncated page, which is what a
// half-finished save looks like from inside the wiki.
//
// The rename replaces whatever is at the target path rather than following it,
// so a symlink at that path is replaced by the new content instead of being
// written through. That is `os.Root`'s behaviour and not something chosen here,
// and it is the safe direction: a save cannot be redirected outside the root by
// planting a link at the destination.
//
// ctx exists for the log line if the temp file cannot be removed. A caller that
// has cancelled its request still has to learn that its save did not happen,
// which is the opposite of what dropping the context would do.
//
// An error returned after the rename has already happened means *durability
// unknown*, not *not written*. A caller that appends a page_revisions row
// (S-6.4) has to treat those differently, and the difference is why the
// directory fsync reports its failure instead of swallowing it: a swallowed
// fsync error is a save that claims to have worked and did not.
func (t *Target) WriteFile(ctx context.Context, data []byte, perm fs.FileMode) error {
	temp := t.tempName()

	// Removed on every path that does not reach the rename. A temp file left
	// behind is a second copy of a page in the tree, and the indexer will
	// index it as one.
	renamed := false

	defer func() {
		if renamed {
			return
		}

		if err := t.root.root.Remove(temp); err != nil {
			slog.WarnContext(ctx, "content.temp_left",
				slog.String("campaign", t.root.slug),
				slog.String("path", t.rel),
				slog.String("error", err.Error()),
			)
		}
	}()

	if err := t.stage(temp, data, perm); err != nil {
		return err
	}

	if err := t.root.root.Rename(temp, t.rel); err != nil {
		return classify(t.rel, err)
	}

	renamed = true

	return t.syncDir(path.Dir(t.rel))
}

// stage writes data to the temp file of an atomic write and closes it.
//
// Separate from WriteFile so the close can be deferred: the file has to be
// closed on the error paths too, and a close in a branch and a close in a defer
// is two places for the next edit to forget one of.
func (t *Target) stage(temp string, data []byte, perm fs.FileMode) error {
	// O_EXCL, so a name that already exists is a leftover from a process that
	// died mid-write and this write reports it rather than truncating a file it
	// did not create. The 128-bit name makes that a 1-in-2^128 event, and it is
	// still handled rather than assumed away: classify would read a collision
	// as a refusal, and "your page is outside the content root" is a lie an
	// operator would spend an afternoon on.
	file, err := t.root.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("staged file for %s already exists: %w", t.rel, err)
		}

		return classify(t.rel, err)
	}

	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			slog.Warn("content.temp_close_failed",
				slog.String("campaign", t.root.slug),
				slog.String("path", t.rel),
				slog.String("error", closeErr.Error()),
			)
		}
	}()

	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write staged file for %s: %w", t.rel, err)
	}

	if err := file.Sync(); err != nil {
		return fmt.Errorf("fsync staged file for %s: %w", t.rel, err)
	}

	return nil
}

// syncDir fsyncs a directory so a rename into it survives a crash.
//
// An *os.File on the directory, which on Linux fsyncs its entries. This is the
// step that makes the rename durable: without it a crash can lose the rename
// while keeping the old contents, and the page quietly reverts to the revision
// the GM just replaced.
//
// The only deliberately POSIX-shaped thing in this file, and a content root is
// created 0o700 on a disk, so the assumption is already made. A platform that
// cannot fsync a directory reports that rather than pretending otherwise.
func (t *Target) syncDir(dir string) error {
	handle, err := t.root.root.Open(dir)
	if err != nil {
		return classify(dir, err)
	}

	syncErr := handle.Sync()

	// Closed on both paths, and a close error is reported only when the fsync
	// itself succeeded. Failing to close a read-only directory handle says
	// nothing about whether the rename is durable, and reporting it anyway
	// would turn a harmless condition into a failed save.
	if closeErr := handle.Close(); closeErr != nil && syncErr == nil {
		return fmt.Errorf("close %s: %w", dir, closeErr)
	}

	if syncErr != nil {
		return fmt.Errorf("fsync %s: %w", dir, syncErr)
	}

	return nil
}

// tempName is the sibling path an atomic write stages through.
//
// A page name with a dot-prefixed, `.tmp`-suffixed, 128-bit random component in
// the middle, so two writers cannot collide and a directory listing that
// ignores dot-files never offers it.
//
// crypto/rand is a deliberate departure from AGENTS.md's "no direct
// crypto/rand", and the rule it breaks is the one for rule code, where a value
// that varies between runs makes a rule untestable. A temp file name is not a
// rule output — it is never compared, hashed, cached or rendered — and the
// alternatives make the name derivable, which is a name another process on the
// same content root can guess.
func (t *Target) tempName() string {
	return path.Join(
		path.Dir(t.rel),
		tempPrefix+path.Base(t.rel)+"."+rand.Text()+tempSuffix,
	)
}

// Registry maps a campaign slug to its confined content root.
//
// A value the composition root constructs and passes down, which is the reason
// it is a type at all: a package-level map would be a second source of truth
// about which campaigns have a root, shared by every test in the process, with
// no ordering story that makes it right. AGENTS.md puts registration in the
// composition root, and a registry is registration.
//
// A sync.RWMutex over a typed map rather than a sync.Map, for two reasons. The
// write side is once per campaign at registration and the read side is once per
// request, which is the read-mostly shape sync.Map exists for; and sync.Map
// stores any, so every read is a type assertion that can fail at a request
// instead of at a construction. The lock costs one atomic increment per request,
// which is not a cost worth optimising away.
type Registry struct {
	mu     sync.RWMutex
	policy SymlinkPolicy
	roots  map[string]*Root
}

// NewRegistry returns a Registry that opens content roots with the given
// symlink policy.
//
// The policy belongs to the registry rather than to each Root because it is a
// stance the process takes and not a property of one campaign. S-4.4 rejects
// symlinks everywhere, and a per-campaign setting would be a flag a caller can
// forget to set, which is a campaign silently served under the other policy.
func NewRegistry(policy SymlinkPolicy) *Registry {
	return &Registry{policy: policy, roots: make(map[string]*Root)}
}

// Adopt takes ownership of an open os.Root, keyed by slug.
//
// The signature the campaigns.Registrar retain callback takes, so
// `campaigns.NewRegistrar(store, base, registry.Adopt)` needs no closure: the
// registrar opens the root, proves the campaign is confinable, and hands the
// handle over; from here the registry closes it and nothing else does.
//
// A slug that already has a root is ErrDuplicateRoot, and the adopted root is
// *not* closed. The registrar's contract is that it closes a handle it failed
// to hand over, so closing it here as well would either double-close it or
// leave the caller unable to tell which of the two happened.
func (reg *Registry) Adopt(slug string, root *os.Root) error {
	return reg.retain(slug, &Root{
		slug:   slug,
		root:   root,
		policy: reg.policy,
		fsys:   root.FS(),
	})
}

// Open opens dir as slug's content root and retains it.
//
// The startup path: a campaign row exists from a previous run, its root was
// never adopted in this process, and a request arrives holding the slug. dir
// comes from the campaign's `content_root` column, written at registration,
// which is the only place a campaign's directory is ever read from.
func (reg *Registry) Open(slug, dir string) (*Root, error) {
	// Opened before the lock is taken. os.OpenRoot is a syscall, and holding
	// the registry's write lock across one would serialise every request for
	// every campaign in the process behind a filesystem call.
	root, err := NewRoot(slug, dir, reg.policy)
	if err != nil {
		return nil, err
	}

	if err := reg.retain(slug, root); err != nil {
		if closeErr := root.Close(); closeErr != nil {
			slog.Warn("content.root_close_failed",
				slog.String("slug", slug),
				slog.String("error", closeErr.Error()),
			)
		}

		return nil, err
	}

	return root, nil
}

// Get returns the retained root for slug, or ErrNoRoot.
//
// ErrNoRoot and not a new root, because a campaign whose content root is
// missing is one an operator has to hear about (S-4.5) rather than one a
// request quietly creates: opening a root on a path the process invented is a
// directory with no campaign behind it, and deciding that a directory should
// exist is not this function's to make.
//
// It closes nothing, and the caller must not close what it gets. The Root is
// shared by every request for the campaign.
func (reg *Registry) Get(slug string) (*Root, error) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	root, ok := reg.roots[slug]
	if !ok {
		return nil, ErrNoRoot
	}

	return root, nil
}

// Slugs returns the retained slugs, sorted.
//
// Sorted because this is the list the watcher builds its prefix routing table
// from and the list the startup scan walks, and a map's iteration order differs
// between runs. Two runs of the same server over the same data should produce
// the same log.
func (reg *Registry) Slugs() []string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	return slices.Sorted(maps.Keys(reg.roots))
}

// Close releases every retained root, emptying the registry.
//
// Called once at shutdown by the composition root, and it is the counterpart of
// the retention: one handle per campaign held for the life of the process is
// small, and held by a process that leaks them is a descriptor limit reached
// on a server that never restarts.
//
// Every root is attempted even when one fails, because stopping at the first
// failure leaves the rest open for no benefit at all.
func (reg *Registry) Close() error {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	slugs := slices.Sorted(maps.Keys(reg.roots))
	errs := make([]error, 0, len(slugs))

	for _, slug := range slugs {
		if err := reg.roots[slug].Close(); err != nil {
			errs = append(errs, err)
		}

		delete(reg.roots, slug)
	}

	return errors.Join(errs...)
}

// retain inserts root under slug, refusing a slug that already has one.
func (reg *Registry) retain(slug string, root *Root) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	if _, exists := reg.roots[slug]; exists {
		return ErrDuplicateRoot
	}

	reg.roots[slug] = root

	return nil
}

// validRef reports whether ref is a reference this package can interpret.
//
// Checked on the reference rather than on the joined path, because path.Join
// discards everything before an absolute component: path.Join("sub",
// "/etc/passwd") is "sub/etc/passwd", so a check after the join would never see
// an absolute reference at all — and a page called etc/passwd under a
// subdirectory is a page an attacker gets to create, not one they get to read.
func validRef(ref string) error {
	switch {
	case ref == "":
		// An empty field in front matter names nothing, and treating it as
		// "this page" would make a missing value resolve to whatever the reader
		// is already looking at — a bug that renders as content.
		return ErrInvalidRef
	case strings.HasPrefix(ref, "/"):
		// Obsidian's root-relative link syntax begins with a slash and means
		// the top of the vault, not the top of the filesystem. Rewriting it is
		// the caller's job, because only the caller knows it is looking at an
		// Obsidian link; here a leading slash can only be a mistake.
		return ErrInvalidRef
	case strings.ContainsRune(ref, 0):
		// os.Root rejects it too, but the error it produces is about a path
		// escaping, and this one is about a reference that never was one.
		return ErrInvalidRef
	default:
		return nil
	}
}

// cleanRef normalises a root-relative path and refuses the shapes that name
// nothing inside the root.
//
// Pure and total, and tested over the combinations that cannot occur in a
// request as well as the ones that can. A lexical normaliser with an untested
// corner is where a bypass goes to live, and this is the corner one would live
// in.
//
// The escape check here is a fast path and not the confinement: `os.Root` still
// refuses every path this accepts, and refuses the ones this misses. What the
// check buys is that an obviously escaping path is refused *before* it becomes a
// cache key, a log line, or a handle this package handed to somebody.
//
// An absolute or NUL-bearing path is ErrInvalidRef rather than
// ErrOutsideRoot. It does not name a location relative to the root at all, and
// a caller answering 400 for a malformed reference and a rejection for an
// escape needs to tell those apart: one is a bug in the caller, the other is
// somebody trying something.
func cleanRef(rel string) (string, error) {
	if err := validRef(rel); err != nil {
		return "", err
	}

	cleaned := path.Clean(rel)
	if cleaned == rootDirName {
		// The content root itself, reached by ".", "./", or "a/..". A page is
		// not a directory, and a Target whose Path is "." is a cache key that
		// collides with every other such key.
		return "", ErrInvalidRef
	}

	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrOutsideRoot
	}

	return cleaned, nil
}

// prefixes returns rel and each of its ancestor directories, shallowest first.
//
// Shallowest first so a refusal names the outermost link, because a link's own
// target decides whether the rest of the path is addressable, which makes it
// the one worth reporting.
func prefixes(rel string) []string {
	parts := strings.Split(rel, "/")
	out := make([]string, 0, len(parts))

	for idx := range len(parts) {
		out = append(out, strings.Join(parts[:idx+1], "/"))
	}

	return out
}

// classify turns an error from an `os.Root` operation into one of this
// package's answers.
//
// The default is ErrOutsideRoot, which is the safe direction: every error
// `os.Root` produces that is not "not there" is a refusal, and a refusal
// answered as a 404 would be a 404 confirming a path exists somewhere. That
// includes a permission error, which is the case a reader is most likely to
// meet — a file in a synced vault whose mode excludes the process — and which
// is reported as a refusal precisely because a 404 is an answer about
// existence, and "this exists and you may not read it" is not a thing the S-8
// matrix says to anybody.
//
// Only ErrNotExist is wrapped with the path, and only because a missing path
// inside a campaign the caller was already authorised for is what a
// broken-link report is built from. The refusals carry no path, so their text
// is a function of the policy rather than of the attacker's input.
func classify(rel string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotExist, rel)
	}

	return ErrOutsideRoot
}
