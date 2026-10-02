package plugin

import (
	"encoding/json"
	"fmt"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/realtime"
)

// PlacementCodec is the codec for a system whose game objects are semiplane's own
// placements — which today is every system, because `realtime.Placement` is the
// only per-object state `campaign_state` holds.
//
// **The projection with no opinion in it**: an object is its placement, encoded as
// that placement's own JSON, and a mutation's payload is written back onto a draft
// of the placement it came from. A system with per-object state of its own — a 5e
// creature's action economy, a Starfinder's cargo — implements `Codec` instead and
// keeps `PlacementCodec` for what it does share.
//
// The kind it names is `rules.KindToken`, and that is §10.2.1's whole argument
// rather than a default: `token` is semiplane's in every build precisely so the
// client can render a placement without knowing any rules. A system that places
// something a client must not treat as a token supplies its own codec, and the
// cost of doing that is a client that knows about one more kind — which is the
// deal §10.2.1 says a system pays only when it genuinely needs to.
//
// ## The two authority fields are never written
//
// `Apply` copies `X`, `Y`, `HP`, `MaxHP`, `Conditions` and `Visible` and **never**
// `ID` or `Version`. Those two are stamped by `CampaignState.Mutate` after the
// codec's function returns, which is S-7.2's rule ("the version is the only
// ordering authority") expressed as a property rather than a review comment: a
// payload claiming `version: 9999` decodes into a draft that is handed to `Mutate`,
// which overwrites the field. The draft itself is discarded on the next apply,
// so nothing survives to be applied.
//
// `Conditions` is re-sorted through `AddCondition`, because the ordering is the
// invariant `realtime.Placement`'s own comment holds a document's determinism to,
// and a payload that arrived with them shuffled would break it.
type PlacementCodec struct{}

// Codec is the whole of the placement codec's contract, stated in the type rather
// than only in the two method comments.
var _ Codec = PlacementCodec{}

// Object renders a placement as the game object a system sees.
func (PlacementCodec) Object(placement realtime.Placement) (rules.Object, error) {
	// The placement is encoded whole, including `ID` and `Version`, and the two
	// are *not* stripped from `Data`. A system that reads them is reading a copy
	// of semiplane's envelope inside its own opaque payload, and the authoritative
	// pair is always the hub's — `rules.State` does not even carry a version. The
	// reason they are here rather than removed is that stripping them would make
	// the encoding of one object differ from the persisted document's encoding of
	// the same object, and a system that round-tripped a payload through a state
	// snapshot and back would produce a document that is byte-different from the
	// one it read.
	encoded, err := json.Marshal(placement)
	if err != nil {
		return rules.Object{}, fmt.Errorf("plugin: encode placement %q: %w", placement.ID, err)
	}

	return rules.Object{
		ID:   rules.ObjectID(placement.ID),
		Kind: rules.KindToken,
		Data: encoded,
	}, nil
}

// Apply writes what a resolved mutation says onto a placement.
func (PlacementCodec) Apply(placement *realtime.Placement, mutation rules.Mutation) error {
	// An empty payload is a mutation that changes nothing, which is legal: the
	// example is an op whose whole statement is "this token acted", where the
	// action is the record and the placement does not move. Refusing it would mean
	// a system had to invent a payload to say nothing happened.
	if len(mutation.Args) == 0 {
		return nil
	}

	var applied realtime.Placement
	if err := json.Unmarshal(mutation.Args, &applied); err != nil {
		// The decoder's text quotes the payload it choked on, which is the
		// system's own bytes rather than anything from a vault — a mutation's
		// payload is produced by the plugin, not by a client. It is still not
		// wrapped into the *wire* answer: this error reaches a log line through
		// `Rejection.Err` and never `Rejection.Reason`, which is why the reason
		// and the error are two fields rather than one string.
		return fmt.Errorf("plugin: decode payload for %q: %w", mutation.Target, err)
	}

	placement.X = applied.X
	placement.Y = applied.Y
	placement.HP = applied.HP
	placement.MaxHP = applied.MaxHP
	placement.Visible = applied.Visible

	// Rebuilt rather than assigned: `AddCondition` keeps the set sorted and free of
	// duplicates, and assigning the decoded slice would take whatever order the
	// payload arrived in straight into the persisted document.
	placement.Conditions = nil

	for _, condition := range applied.Conditions {
		placement.AddCondition(condition)
	}

	return nil
}

// snapshot renders a campaign's live document as the read-only state a system
// resolves against.
//
// It is `Entry`-level rather than free-standing because the codec is what
// translates, and a snapshot built with anything but this entry's codec would be
// the second answer to "what does this system's world look like" that `Codec` is
// written to prevent.
//
// **The whole document is built, placements and all**, and the error is refused
// rather than skipped: a state where one placement failed to encode and the rest
// did not is a state the system would resolve against with a hole in it, and a
// roll against a missing opponent is a roll nobody can reproduce.
func snapshot(entry Entry, document realtime.Document) (rules.State, error) {
	objects := make([]rules.Object, 0, len(document.Placements))

	for _, placement := range document.Placements {
		object, err := entry.Codec.Object(placement)
		if err != nil {
			return rules.State{}, fmt.Errorf(
				"plugin: read the state for system %q: %w", entry.System.ID(), err,
			)
		}

		objects = append(objects, object)
	}

	// `NewState` sorts and copies, so the order the document arrived in is not
	// this system's problem — see its own comment, which is S-14.6's rule about
	// map iteration arriving one package earlier than expected.
	state, err := rules.NewState(document.Revision, objects)
	if err != nil {
		return rules.State{}, fmt.Errorf(
			"plugin: read the state for system %q: %w", entry.System.ID(), err,
		)
	}

	return state, nil
}
