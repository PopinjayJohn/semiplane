// The vocabulary the content pipeline's event-driven half speaks.
//
// Two components produce and consume settled changes — the watcher that turns
// filesystem events into changes, and the settle filter that decides a change is
// finished happening — and they are written separately. This file is the
// contract between them, so that neither has to know the other's shape and a
// change means exactly one thing in the package.
//
// # Why a change type at all
//
// S-4.1 makes the pipeline event-driven: the web editor and Obsidian converge on
// one path because both of them move a file, and the file is the only thing
// either of them writes. That leaves one component watching and another
// applying, and the words they exchange have to exist somewhere. Putting them
// here rather than in either component means the set of things that can happen to
// a page is stated once, in one file, and a third consumer — the reindex on a
// rescan, the change notification SSE surface in phase 6 — reads it rather than
// inventing a parallel one.
//
// # What "settled" means
//
// A `Change` is not an event: it is the conclusion that a path has stopped
// moving. A file being written arrives as many filesystem events over some
// window of time, and indexing the third one of them reads a truncated file —
// which is a page in the search index whose `content_hash` disagrees with the
// bytes on disk, and therefore a render cache entry that never invalidates.
// `debounce.go` decides when a change is settled; by the time one reaches a sink,
// that question is answered.

package content

import (
	"context"
	"strconv"
)

// Op is what happened to a path.
//
// Three operations and not a bitmask, because every consumer switches on the
// operation and a set of operations has no exhaustive switch. The three are the
// three things a filesystem does to a file that matter here; anything finer is a
// distinction the index does not care about, and an operation the index ignores
// is an operation that has to be argued for every time one is added.
type Op uint8

const (
	// OpUpsert is a page that was created or written. The index reads the file
	// and makes the row agree with it, so "created" and "written" are one
	// operation: the index cannot tell them apart either, because after the
	// event the file is simply there with content in it.
	OpUpsert Op = iota + 1

	// OpRemove is a page that is gone. The row and its FTS entries go with it.
	//
	// Not "may be gone": a page that vanished and came back — an Obsidian trash
	// restore, a sync client re-creating a file it briefly lost — produces a
	// remove followed by an upsert, and the index converges through both. A
	// "gone" that is later contradicted is a tombstone the upsert clears.
	OpRemove

	// OpRename is a page that moved. `OldPath` names where it was.
	//
	// A distinct operation rather than a remove plus an upsert because the two
	// are not equivalent in the index: a remove-then-upsert loses the row's
	// identity and its `created_at`, so a page that was renamed in Obsidian would
	// appear to the index as a brand-new page. Carrying `OldPath` lets the row
	// move, which is also the cheap answer — the content is unchanged, so there
	// is nothing to re-read.
	//
	// Renames are also the one change a directory watch must survive, which is
	// why S-4.2 forbids watching files: an atomic save is a rename, and a
	// watcher bound to the old inode never sees the new file at all.
	OpRename
)

// String renders the operation for a log line and for a `Change`'s own rendering.
//
// Never a numeric code. A change's `String` ends up in a log line and in a test
// failure message, and `1` in either place tells a reader nothing that `upsert`
// does not tell them better.
func (o Op) String() string {
	switch o {
	case OpUpsert:
		return "upsert"
	case OpRemove:
		return "remove"
	case OpRename:
		return "rename"
	default:
		// An operation this build does not know is a *build* that does not know,
		// and saying so is more useful than panicking in a watcher goroutine or
		// writing a number into an operator's log with nothing to interpret it.
		return "unknown(" + strconv.Itoa(int(o)) + ")"
	}
}

// Change is one settled change to one path in one campaign.
//
// `CampaignID` rather than only a slug because the index is keyed by id
// (`pages.campaign_id` is the leading column of the identity index) and a
// change that had to resolve a slug to reach it would be a change that can fail
// for a reason unrelated to the file. `Slug` is carried alongside for the same
// reason the rest of the package carries it: for logs, never for authorisation
// and never as a capability.
type Change struct {
	// CampaignID is the campaign whose root the path is relative to.
	CampaignID int64

	// Slug is that campaign's slug. Present for logging; see the type comment.
	Slug string

	// Op is what happened.
	Op Op

	// Path is the page's path after the change: slash-separated and relative to
	// the campaign's content root, which is the spelling `pages.path` holds and
	// the spelling a URL carries.
	Path string

	// OldPath is where the page was, and is set only for `OpRename`. Empty is
	// never a path: a page at the vault root still has a name, and a rename
	// whose source cannot be named is not a rename this type can express.
	OldPath string
}

// String renders a change for a log line.
//
// Carries the campaign, the operation and the paths — which are all operator
// input, all bounded, and none of them content. It deliberately does not render
// anything derived from the file's bytes: S-12.3 forbids an event carrying file
// contents, and a String method reachable from a log line is exactly where that
// would slip in.
func (c Change) String() string {
	if c.Op == OpRename {
		return c.Slug + " rename " + c.OldPath + " -> " + c.Path
	}

	return c.Slug + " " + c.Op.String() + " " + c.Path
}

// ChangeSink receives settled changes.
//
// A function type rather than an interface because there is one implementation
// in this phase — the indexer — and an interface here would be a second thing to
// keep in step with the first. It becomes an interface the moment there is a
// second consumer that is not a function.
//
// A sink may be called from the watcher's goroutine and must therefore not block
// indefinitely and must not assume anything about ordering: two changes to the
// same path may arrive concurrently if two writers race, and the indexer's
// answer to that is to converge, not to serialise. `ctx` is cancelled when the
// watcher shuts down, and a sink that ignores it holds shutdown open.
type ChangeSink func(ctx context.Context, change Change)
