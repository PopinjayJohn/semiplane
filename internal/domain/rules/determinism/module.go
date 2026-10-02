package determinism

import (
	"encoding/json"
	"sort"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// Module is one house-rule module a campaign has enabled, in the order it applies.
//
// **The persisted record, not the module itself.** A module's *definition* — what
// it changes and what it may change, which is the `Scope` above — is compiled in
// and registered at the composition root, for the reason `campaigns.system_id` is
// a string and not a foreign key: the set of available modules is a property of
// the running build and not of the database. A row naming a module this build does
// not have is a refusal at campaign load (S-10.6's shape), not a database error.
//
// So this type carries only what a row can hold: which module, whether it is on,
// what data it was given, and where it sits in the application order. It is here
// rather than in `internal/store` for the reason `domain.Campaign` and
// `domain.Membership` are in `internal/domain` — `store` owns persistence and
// `domain` owns what is persisted, and a query that returned a type of its own
// would make every caller import `store` to name what it read.
type Module struct {
	// CampaignID is `domain.Campaign.ID`, and an INTEGER: `campaigns.id` is
	// `INTEGER PRIMARY KEY AUTOINCREMENT` (migration 0005), which is a departure
	// from architecture §10.7's `TEXT` and is recorded there.
	CampaignID int64

	// ModuleID names the compiled-in module. A `rules.ID` and not a plain string
	// so that the *type* answers "is this a usable identifier": the value comes
	// from a column, and `rules.ParseID` is the one rule about what may be in it.
	ModuleID rules.ID

	// Enabled is off without being deleted, so that re-enabling one is a
	// single-column write and so that "this campaign tried this and turned it off"
	// is still a fact the table can answer.
	Enabled bool

	// Config is the module's configuration as a JSON **object**, unvalidated beyond
	// that shape.
	//
	// Opaque on purpose. The keys a module understands belong to the module, and
	// this package deliberately does not define them: a config vocabulary is
	// `houserules`' to write, and a second implementation of it here would be two
	// answers to "what may this module be configured with". What is checked is the
	// shape the column's `DEFAULT '{}'` already asserts — a row whose config is
	// the empty string or a bare number is a row no application can read.
	//
	// `json.RawMessage` rather than `map[string]any` so that the type says
	// "validated JSON, decode it yourself" instead of implying this package parsed
	// it.
	Config json.RawMessage

	// Position is the declaration order, and the whole of first-match-wins: §10.5
	// resolves a conflict in favour of the module that comes first, so this is the
	// conflict policy and not a cosmetic sort key.
	Position int
}

// Order returns modules in the one total order S-10.5 first-match-wins needs:
// ascending position, then module id.
//
// **The tie-break is the point.** Two modules sharing a `position` is not a
// mistake the schema prevents — nothing declares positions unique — and
// "first-match-wins" over a set whose order is not total has no meaning at all: a
// conflict between two modules at the same position would resolve differently
// depending on which one the storage engine happened to return first, which is the
// last-write-wins behaviour §10.5 names and forbids. `module_id` is unique within
// a campaign by the primary key, so it can never tie and the order is total.
//
// A copy rather than an in-place sort: the caller holds a slice it may still want,
// and a function that reorders its argument is a function whose callers have to
// know that.
func Order(modules []Module) []Module {
	ordered := make([]Module, len(modules))
	copy(ordered, modules)

	sort.SliceStable(ordered, func(first, second int) bool {
		if ordered[first].Position != ordered[second].Position {
			return ordered[first].Position < ordered[second].Position
		}

		return ordered[first].ModuleID < ordered[second].ModuleID
	})

	return ordered
}
