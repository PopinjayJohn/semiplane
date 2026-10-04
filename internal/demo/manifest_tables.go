package demo

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
	"github.com/semiplane/semiplane/internal/realtime"
)

// Row converts one manifest module into the row the store persists.
//
// `Enabled` and `Config` are normalised here rather than at parse because both have a
// default the *store* also applies and the two must not differ. **A nil `map[string]any`
// marshals to `null`, not to `{}`**, so a manifest that declared no `config:` would
// otherwise produce a row the store refuses — which reads as the schema rejecting a module
// rather than as two answers for "no configuration" disagreeing.
//
// The `rules.ParseID` refusal is the store's rule called rather than restated, and it is the
// same reason `internal/store`'s `validatedRuleModules` calls it: one answer to "may this
// identifier be stored", in one place.
func (m *Module) Row(campaignID int64) (determinism.Module, error) {
	id, err := rules.ParseID(m.ID)
	if err != nil {
		return determinism.Module{}, fmt.Errorf("%w: rule module: %w", ErrIncompleteManifest, err)
	}

	config := m.Config
	if config == nil {
		config = map[string]any{}
	}

	encoded, err := json.Marshal(config)
	if err != nil {
		// Unreachable for `map[string]any` decoded from YAML unless a value is a channel or
		// a function, which a YAML decoder cannot produce. Handled rather than dropped
		// because a module that could not be encoded would otherwise be stored as a config
		// nothing can read.
		return determinism.Module{}, fmt.Errorf(
			"%w: rule module %q: %w",
			ErrIncompleteManifest,
			m.ID,
			err,
		)
	}

	return determinism.Module{
		CampaignID: campaignID,
		ModuleID:   id,
		Enabled:    *m.Enabled,
		Config:     encoded,
		Position:   m.Position,
	}, nil
}

// Rows converts a campaign's declared modules into the rows the store persists, with
// `campaignID` filled in.
//
// In manifest order and **not re-sorted here**: `determinism.Order` is the authority on
// application order and `store.ReplaceRuleModules` calls it, so sorting in this package too
// would be a third order for the same question. The manifest's order is the author's
// declaration and the store's is the total one; the two agree because `position` is written
// from the same declaration.
func (c *Campaign) Rows(campaignID int64) ([]determinism.Module, error) {
	if len(c.RuleModules) == 0 {
		return nil, nil
	}

	rows := make([]determinism.Module, 0, len(c.RuleModules))

	for index := range c.RuleModules {
		row, err := c.RuleModules[index].Row(campaignID)
		if err != nil {
			return nil, err
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// Document builds the bytes for this campaign's `campaign_state` row, and refuses to build
// any the state column could not hold.
//
// # The revision and the versions are computed, not declared
//
// `Document.check` refuses a placement at version 0 and one whose version exceeds the
// document's revision, because every placement this project creates is stamped at creation
// and each version bump is also a revision bump. A manifest that could state either would be
// a second place that invariant is decided. So each placement is stamped in **sorted id
// order** with its 1-based position and the revision is the number of placements: every
// version is then at most the revision, none is zero, and the document's bytes are a function
// of the manifest alone — which is what lets the release artefact be reproducible and lets a
// test compare the seeded row against a second seed of the same manifest.
//
// Sorting by id first is not decoration. `EncodeDocument` requires a sorted document and
// does not sort one, because a persisted blob whose order depends on who built it is a blob a
// test cannot compare; the placements therefore arrive sorted at the encoder whatever order
// the manifest wrote them in.
//
// # The round trip is the check
//
// The document is encoded and then **decoded again**, and a decode failure is a refusal.
// `realtime.PlacementID`'s validity rules (non-empty, at most 128 bytes, no control
// characters) are unexported and are enforced by `DecodeDocument` on the way back in, so
// decoding is the only way this package can ask "would the bytes I am about to write load"
// without restating three rules. A manifest naming a placement id with a newline in it is
// refused here rather than written, and it would otherwise be refused by the first GM who
// tried to open the table.
func (t Table) Document() ([]byte, error) {
	placements := make([]realtime.Placement, 0, len(t.Placements))

	for index := range t.Placements {
		declared := &t.Placements[index]

		placement := realtime.Placement{
			ID:      realtime.PlacementID(declared.ID),
			X:       declared.X,
			Y:       declared.Y,
			HP:      declared.HP,
			MaxHP:   declared.MaxHP,
			Visible: true,
		}

		if declared.Visible != nil {
			placement.Visible = *declared.Visible
		}

		// `AddCondition` rather than assigning `Conditions`: the document promises its
		// condition set is held sorted and free of duplicates, and that method is the one
		// implementation of that promise. Two spellings of it in the same repository is a
		// way for one of them to stop sorting.
		for _, condition := range declared.Conditions {
			placement.AddCondition(condition)
		}

		placements = append(placements, placement)
	}

	slices.SortFunc(placements, func(first, second realtime.Placement) int {
		return strings.Compare(string(first.ID), string(second.ID))
	})

	for index := range placements {
		// 1-based, so no placement is ever at version 0.
		placements[index].Version = uint64(index + 1)
	}

	document := realtime.Document{
		Revision:   uint64(len(placements)),
		Paused:     t.Paused,
		Placements: placements,
	}

	encoded, err := realtime.EncodeDocument(document)
	if err != nil {
		return nil, fmt.Errorf("%w: encode the seeded tabletop: %w", errDemo, err)
	}

	// The read-back, for the reason this doc comment gives.
	if _, err := realtime.DecodeDocument(encoded); err != nil {
		return nil, fmt.Errorf(
			"%w: the seeded tabletop is not a document this build could load: %w",
			ErrIncompleteManifest, err,
		)
	}

	return encoded, nil
}

// The two statements that touch `campaign_state`.
//
// Named rather than written at the call sites for the reason `internal/realtime`'s
// `upsertState` is: a column list and the arguments filling it have to agree, and one name is
// what makes a mismatch impossible to introduce rather than unlikely.
//
// They live here rather than in `internal/store` because the seed is the only writer of a
// campaign's state that is not the realtime registry, and a statement duplicated into the
// store would be a second answer to "who writes `campaign_state`". The shape of the upsert is
// `realtime`'s, deliberately: the same `ON CONFLICT DO UPDATE`, for the same reason — one
// statement, so the row is never absent once it is present.
//
// `deleteState` exists at all because `campaign_state` has **no foreign key** to `campaigns`:
// migration 0005 records that the constraint needs a table rebuild its author called out as
// belonging to the work item that owns that table, and `store.DeleteCampaign` documents its
// deletion as knowingly incomplete for exactly this reason. A reset that dropped the campaigns
// and left the rows would hand the next campaign registered that id's state — which
// `AUTOINCREMENT` prevents — while still leaving three rows no campaign can ever read.
const (
	upsertState = `INSERT INTO campaign_state (campaign_id, state, version, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(campaign_id) DO UPDATE SET
			state = excluded.state,
			version = excluded.version,
			updated_at = excluded.updated_at`

	deleteState = "DELETE FROM campaign_state WHERE campaign_id = ?"
)
