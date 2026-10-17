// Package pluginapi is the PUBLIC, semver'd plugin surface for Semiplane.
//
// Stability promise: anything exported here is covered by the plugin API
// compatibility matrix (tech-stack §4, §9). Internal refactors must never
// break these signatures; breaking changes require an ADR and a major bump.
//
// Boundary rule (docs/repo-layout.md): this package imports NOTHING from
// internal/. Implementations live in internal/* and satisfy these interfaces;
// plugin authors depend only on this package. Implementations are registered
// via blank imports plus an init() registry, optionally behind build tags
// (e.g. -tags plugin_dnd5e).
package pluginapi

import "context"

// APIVersion is the plugin API version plugins negotiate against.
// Core removals bump the major; additions bump the minor.
const APIVersion = "v0"

// Common sentinel errors. Implementations MUST return these (wrapped with
// %w is fine) so handlers and plugins share one error-handling path.
var (
	ErrNotFound     = errString("pluginapi: not found")
	ErrConflict     = errString("pluginapi: conflict")
	ErrUnauthorized = errString("pluginapi: unauthorized")
	ErrForbidden    = errString("pluginapi: forbidden")
)

type errString string

func (e errString) Error() string { return string(e) }

// Note is the unit of vault content: a Markdown document addressable by its
// vault-relative path (e.g. "campaign/session-1.md").
type Note struct {
	// Path is vault-relative, slash-separated, always ending in .md.
	Path string
	// Content is the raw Markdown file body, frontmatter included.
	Content string
	// Hash is the hex content hash at read time. Put compares it against the
	// on-disk hash for external-edit conflict detection (tech-stack §5):
	// empty Hash means "no precondition", non-empty mismatch yields ErrConflict.
	Hash string
}

// NoteRepository is the vault boundary (implemented by internal/vault).
// Implementations MUST write atomically (temp → fsync → rename) and MUST
// reject paths escaping the vault root.
type NoteRepository interface {
	// Get reads a note. Returns ErrNotFound for unknown paths.
	Get(ctx context.Context, path string) (Note, error)
	// Put creates or replaces a note. note.Hash is the read-time precondition
	// ("" disables the check). Returns ErrConflict on concurrent external edit.
	Put(ctx context.Context, note Note) (Note, error)
	// Delete removes a note. Returns ErrNotFound for unknown paths.
	Delete(ctx context.Context, path string) error
	// List returns note paths under prefix ("" lists all), in lexical order.
	List(ctx context.Context, prefix string) ([]string, error)
}

// SearchHit is one ranked full-text result.
type SearchHit struct {
	Path    string
	Snippet string
	Score   float64
}

// SearchIndex is the derived, rebuildable full-text index over the vault
// (implemented by internal/index over SQLite FTS5). Losing the index loses
// nothing: reindex rebuilds it from Markdown.
type SearchIndex interface {
	// Search runs a BM25-ranked query, returning at most limit hits.
	Search(ctx context.Context, query string, limit int) ([]SearchHit, error)
}

// Identity is the authenticated principal making a call.
type Identity struct {
	// ID is the stable user identifier (e.g. "player1").
	ID string
	// Role is "gm" or "player".
	Role string
}

// AuthProvider verifies credentials and tokens (implemented by internal/auth).
type AuthProvider interface {
	// Verify resolves an opaque session/API token to its identity.
	// Unknown or revoked tokens yield ErrUnauthorized.
	Verify(ctx context.Context, token string) (Identity, error)
}

// DiceResult is the outcome of evaluating one dice formula.
type DiceResult struct {
	// Total is the final numeric outcome after all modifiers.
	Total int
	// Faces records every die face rolled, in roll order (seeded replay).
	Faces []int
}

// DiceEvaluator resolves dice formulas server-authoritatively
// (implemented by internal/dice; the same engine later serves /ws/vtt).
type DiceEvaluator interface {
	// Roll evaluates formula (e.g. "2d20kh1+5") with the given seed.
	// Deterministic: same formula + same seed = same result.
	Roll(ctx context.Context, formula string, seed int64) (DiceResult, error)
}

// Capability is a named optional surface a host or plugin may offer.
// Unknown capabilities are rejected with a helpful error, never silently
// ignored.
type Capability string

// Capabilities is the negotiated set shared by host and plugin.
type Capabilities map[Capability]struct{}

// Has reports whether c was negotiated.
func (c Capabilities) Has(cap Capability) bool {
	_, ok := c[cap]
	return ok
}

// Negotiate returns the intersection of offered and wanted, preserving the
// order of wanted. Either side may offer capabilities the other side does
// not know; those are dropped (documented, not a bug).
func Negotiate(offered, wanted []Capability) Capabilities {
	seen := make(map[Capability]struct{}, len(offered))
	for _, c := range offered {
		seen[c] = struct{}{}
	}
	out := make(Capabilities, len(wanted))
	for _, c := range wanted {
		if _, ok := seen[c]; ok {
			out[c] = struct{}{}
		}
	}
	return out
}

// Slot is a named UI mount point a runtime plugin may declare.
// Plugins declare slots, never positions (tech-stack §4): the vocabulary is
// closed and versioned here so two plugins cannot claim overlapping layout.
// New slots arrive as minor version additions.
type Slot string

// Closed slot vocabulary. A plugin bound to a mode-hidden slot silently
// does not render (documented, not a bug).
const (
	SlotToolbar     Slot = "toolbar"
	SlotLeftSidebar Slot = "left-sidebar"
	SlotRightPanel  Slot = "right-panel"
	SlotPageFooter  Slot = "page-footer"
	SlotDiceTray    Slot = "dice-tray"
)

// KnownSlots is the closed slot vocabulary in canonical order.
var KnownSlots = []Slot{
	SlotToolbar,
	SlotLeftSidebar,
	SlotRightPanel,
	SlotPageFooter,
	SlotDiceTray,
}

// ValidSlot reports whether s is in the closed vocabulary.
func ValidSlot(s Slot) bool {
	for _, known := range KnownSlots {
		if s == known {
			return true
		}
	}
	return false
}
