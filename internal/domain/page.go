package domain

import "time"

// Page is one indexed markdown file: a row of `pages`.
//
// It is a *row of the index*, not the file. S-3.1 makes the filesystem the
// source of truth and ADR 0006 says what that means for this type: nothing here
// is authoritative, everything here is rebuildable, and a disagreement between a
// Page and the file it describes is resolved in favour of the file. What the
// index buys is the two things a wiki cannot do without — a search over bodies
// it has already read, and a content hash to key a cache on (S-5.2).
type Page struct {
	ID int64
	// CampaignID is the tenancy key. Every row is inside exactly one campaign,
	// which is what makes S-8.2 expressible as a join rather than as a filter a
	// caller has to remember.
	CampaignID int64
	// Path is root-relative to the campaign's content root, slash-separated, with
	// no leading `./` and no `..`. It is the same string the wiki URL is built
	// from, which is why it is stored relative and not absolute: an absolute path
	// is the one value that cannot be moved between two deployments of the same
	// vault, and it would leak the host's directory layout into every index.
	//
	// Not `filepath` output. A path that has been through filepath.Clean is a
	// path nobody validated, and S-3.5 makes every one of these attacker-reachable
	// through a front-matter reference; the value stored here has been resolved
	// inside the campaign's `os.Root` by the time it arrives.
	Path string
	// Kind is the *honoured* kind: KindProse when the page declared no kind, and
	// equally when it declared one this build does not know. The declaration
	// itself is not kept here — it lives in the file, which is the source of
	// truth — so the value is what to render as, never what the author wrote.
	Kind PageKind
	// Title is the page's display name, empty when the front matter did not give
	// one. Deliberately allowed to be empty rather than defaulted to a derived
	// name: the derivation belongs to whoever renders the page and would
	// otherwise be frozen here on first index and go stale on rename.
	Title string
	// ContentHash is the digest of the file's bytes, and it is what the render
	// cache is keyed on (S-5.2). Validity is by this and never by mtime, because
	// sync paths coarsen mtime and 1-second granularity lets two distinct writes
	// collide.
	ContentHash string
	// ByteSize is the length of the file's bytes on disk. The watcher's
	// size-stable confirmation compares it across two `stat` samples (S-4.3), and
	// it is the size the document cap is measured against (S-4.7) — so it is the
	// one place a file's bulk is recorded rather than re-derived by a `stat` that
	// a rename may already have invalidated.
	ByteSize  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PageText pairs a page with the plain text derived from it for the index.
//
// A separate type, and not a field on Page, because `body_plain` is by far the
// largest column in the table and the campaign listing must not drag it: a
// thousand-page vault would carry every body through a request that wants paths.
// Splitting the write shape from the read shape makes that impossible to get
// wrong — there is no list query that can accidentally select the text, and no
// text that can accidentally be omitted from an upsert.
type PageText struct {
	Page
	// BodyPlain is the searchable text, and it is the *redacted* text: it
	// excludes `[!secret]` callout content in **every** reveal state (S-5.11).
	// That is what stops a search snippet carrying secret text to a player
	// (ADR 0007), and it is why there is exactly one column for it rather than a
	// plain and an unredacted variant: a second column is a second thing to
	// remember to exclude, and the state it would record — a revealed secret —
	// is already recorded in the file and in the ledger.
	BodyPlain string
}

// FrontMatter is one page's YAML front matter: the block between the `---`
// delimiters, interpreted.
//
// It is a domain type because it is a statement about a page, and because the
// consumers that need it — the renderer, the link resolver, the indexer — must be
// able to name it without depending on the parser that produced it.
//
// Fields carries every key the block declared, including the ones semiplane does
// not interpret. Obsidian and its plugins write keys that have nothing to do with
// a tabletop (S-3.3: an unknown key is tolerated, never a failure), and keeping
// them means a future semiplane promotes one to a typed field by reading it here
// rather than by inventing a second parser that agrees about the ones it knows.
type FrontMatter struct {
	// Kind is the kind to honour, resolved against the registry. KindProse unless
	// DeclaredKind names something registered.
	Kind PageKind
	// DeclaredKind is the `kind` value verbatim, and empty when the block did not
	// declare one.
	//
	// Kept alongside Kind because the pair is what makes the degradation
	// *reportable* rather than merely silent: Kind says what to render and
	// DeclaredKind says what the author wrote, so a GM can be told "kind `tokn`
	// is not registered; rendered as prose" instead of being handed a page that
	// quietly lost its token. It is also what survives a plugin being removed —
	// Kind degrades, this does not.
	DeclaredKind string
	// Title is the `title` value, and empty when absent or when it was not a
	// string. A `title` that is a list or a number is treated as absent rather
	// than coerced: coercion invents a display name the author did not write, and
	// the page's real name is recoverable from its path.
	Title string
	// Fields is every declared key, interpreted ones included, as the YAML parser
	// returned them: `map[string]any`, `[]any`, `string`, `bool`, `int`, `float64`
	// or `nil`. Read-only by convention, and the values are untrusted input —
	// every path among them is resolved inside the campaign's `os.Root` before it
	// is used (S-3.5), and no template ever renders one without escaping.
	//
	// Nil when the page has no front matter, and non-nil but possibly empty when
	// it has an empty block.
	Fields map[string]any
	// Err reports that a front-matter block was present and could not be
	// interpreted: not YAML, not a mapping, or refused by the parser's own
	// expansion limits (S-4.7). Every field above is then the zero value, because
	// a partly-interpreted block is the one outcome that must not reach a
	// renderer.
	//
	// It is a field rather than a return value so that "malformed front matter is
	// inert" (S-3.3) is structural: the parser hands back a complete Document
	// whatever happened, and a caller that forgets to look logs nothing and breaks
	// nothing. A caller that does look gets errors.Is(err,
	// content.ErrMalformedFrontMatter) or content.ErrDocumentTooLarge, and
	// nothing more — see the parser for why the underlying message is not carried.
	Err error
}

// SearchHit is one result of a search: a page, plus what makes it a result.
//
// It embeds Page so that a hit names its path, title and kind without a second
// lookup, and adds only the two things a result list actually needs.
type SearchHit struct {
	Page
	// CampaignSlug is here because a search is not necessarily scoped to one
	// campaign (ADR 0007 requires every FTS query to join `campaigns`, which only
	// buys anything if the result can cross campaigns), and a cross-campaign
	// result is unusable without the campaign it came from.
	CampaignSlug string
	// Snippet is FTS5's excerpt of `body_plain`, and therefore the redacted text
	// (S-5.11). Plain text with no markup: the excerpt markers are empty, so a
	// snippet can be escaped once by a template and there is nothing here that
	// would tempt a caller into `html.WithUnsafe`.
	Snippet string
}
