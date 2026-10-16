// Package events holds the closed core event/state ontology from
// docs/rules-ontology.md Part I. It is the VTT protocol seed and the base
// every other stream builds on: rules reducers, SSE broadcasts, and the
// oplog all speak these types.
//
// Layering (ontology reading guide): Part I is system-neutral and lives in
// this binary. Anything naming a die size, attribute, rest, condition key,
// or tier order lives in vault system packs (rules/systems/<id>/), never
// here. Field VALUE domains marked pack-defined (†) are strings validated
// against the active pack at authoring time, never at roll time.
//
// This package has ZERO dependencies (stdlib only, no imports at all) and
// is read-only after Phase 0 merges: extend via system packs, not edits.
package events

// OntologyVersion is the core ontology version pinned by every hook file's
// frontmatter (ontology: vN). Core removals bump the major; additions bump
// the minor. The validator refuses files pinned to a newer core.
const OntologyVersion = "v0"

// Kind is the closed core event enum (ontology §1, rows 1–16; row 11 holds
// the turn_start/turn_end pair, so 16 rows = 17 kinds). Unknown kinds are a
// hard authoring-time error, judged against the active system pack.
type Kind string

const (
	KindAttack          Kind = "attack"
	KindDamage          Kind = "damage"
	KindCheck           Kind = "check"
	KindSave            Kind = "save"
	KindCast            Kind = "cast"
	KindConditionApply  Kind = "condition_apply"
	KindConditionRemove Kind = "condition_remove"
	KindResourceSpend   Kind = "resource_spend"
	KindRestShort       Kind = "rest_short"
	KindRestLong        Kind = "rest_long"
	KindTurnStart       Kind = "turn_start"
	KindTurnEnd         Kind = "turn_end"
	KindActionSpend     Kind = "action_spend"
	KindMove            Kind = "move"
	KindLineOfSight     Kind = "line_of_sight"
	KindAdjacency       Kind = "adjacency"
	KindArea            Kind = "area"
)

// KnownKinds is the closed kind set in §1 row order.
var KnownKinds = []Kind{
	KindAttack,
	KindDamage,
	KindCheck,
	KindSave,
	KindCast,
	KindConditionApply,
	KindConditionRemove,
	KindResourceSpend,
	KindRestShort,
	KindRestLong,
	KindTurnStart,
	KindTurnEnd,
	KindActionSpend,
	KindMove,
	KindLineOfSight,
	KindAdjacency,
	KindArea,
}

// ValidKind reports whether k is in the closed core enum.
func ValidKind(k Kind) bool {
	for _, known := range KnownKinds {
		if k == known {
			return true
		}
	}
	return false
}

// Event is the envelope every event, every system shares (ontology §1).
// ID is a ULID assigned by the engine; CausedBy links damage to its parent
// attack (damage.caused_by = attack.id) or a forced save/attack to its
// parent cast. Payload is exactly one of the kind-specific structs below.
type Event struct {
	ID        string
	Kind      Kind
	Round     int
	TurnActor string
	CausedBy  string
	Payload   any
}

// AttackPayload (§1 row 1). Whether crit exists and what determines hit is a
// pack rule; the core only propagates the flags. UsedAttribute and Source
// value domains (†) are pack-defined.
type AttackPayload struct {
	Attacker      string
	Target        string
	UsedAttribute string // † pack-defined attribute key
	Source        string // † pack-defined source key
	Adv           bool
	Dis           bool
	Kept          []int
	Discarded     []int
	Total         int
	Hit           bool
	Crit          bool
}

// Slot is the damage-contribution slot (ontology §0.3). Every add_damage op
// declares one; the breakdown always renders dice and flats as separate
// lines. What crit DOES to each slot is a pack rule (dnd5e: dice roll twice,
// flats never double) — the core only guarantees the slot is declared.
type Slot string

const (
	SlotDice Slot = "dice"
	SlotFlat Slot = "flat"
)

// DamageContribution is one tiered damage line in a damage event.
type DamageContribution struct {
	Slot     Slot
	Tier     string // † pack-defined tier key, ordered by pack precedence
	Label    string
	DiceN    int // set when Slot == SlotDice
	DiceSize int // pack-defined die size; 0 when Slot == SlotFlat
	Amount   int // set when Slot == SlotFlat
}

// DamagePayload (§1 row 2): damage rolled/applied, following a hit attack or
// standalone. Attacker is empty for standalone damage.
type DamagePayload struct {
	Source        string // † pack-defined source key
	Attacker      string
	Target        string
	BaseDice      string
	Contributions []DamageContribution
	Total         int
}

// CheckPayload (§1 row 3): trait/skill check.
type CheckPayload struct {
	Actor      string
	Attribute  string // † pack-defined attribute key
	Specialty  string
	Difficulty string // † pack-defined difficulty scale
	Adv        bool
	Dis        bool
	Total      int
	Success    *bool
}

// SavePayload (§1 row 4): resist/defense roll.
type SavePayload struct {
	Actor      string
	Attribute  string // † pack-defined attribute key
	Difficulty string // † pack-defined difficulty scale
	Adv        bool
	Dis        bool
	Total      int
	Success    *bool
}

// CastPayload (§1 row 5): spell cast, committed before anything it causes.
// A cast never rolls dice; the save/attack it forces is a child event.
type CastPayload struct {
	Caster  string
	Spell   string
	Cost    string
	Targets []string
}

// ConditionApplyPayload (§1 row 6): a named effect starts.
type ConditionApplyPayload struct {
	Target     string
	Condition  string // † closed per pack
	Until      string // structured expiry (UntilManual, UntilTurnStart…), never prose
	SourceHook string
}

// Expiry markers for ConditionApplyPayload.Until. Pack-named expiries
// (e.g. rests) are plain strings in the same field; "a while" is never valid.
const (
	// UntilManual expires only via an explicit condition_remove event.
	UntilManual = "manual"
)

// ConditionRemovePayload (§1 row 7): an effect ends early, expires, or is
// consumed.
type ConditionRemovePayload struct {
	Target    string
	Condition string // † closed per pack
	Reason    string
}

// ResourceSpendPayload (§1 row 8): pool debit (ammo, charges, stress, slots).
type ResourceSpendPayload struct {
	Actor    string
	Resource string // † pack-defined resource name
	Amount   int
	Before   int
	After    int
}

// RestPayload (§1 rows 9–10): short or full recovery.
type RestPayload struct {
	Actor             string
	ResourcesRegained []string
}

// TurnPayload (§1 row 11): turn boundaries, paired by round/turn actor.
type TurnPayload struct {
	Actor string
	Round int
}

// ActionSpendPayload (§1 row 12): action-economy debit.
type ActionSpendPayload struct {
	Actor     string
	Cost      string // † pack-defined cost name
	Remaining string
}

// MovePayload (§1 row 13): requested movement. Positions are unit-agnostic;
// packs fix the unit (feet, meters, zones).
type MovePayload struct {
	Actor    string
	From     string
	To       string
	Distance float64
	Provokes *bool
}

// LineOfSightPayload (§1 row 14): visibility query. Pure: no mutation;
// side-effects happen only via explicit follow-up events.
type LineOfSightPayload struct {
	Viewer  string
	Target  string
	Blocked *bool
}

// AdjacencyPayload (§1 row 15): closeness query. Pure.
type AdjacencyPayload struct {
	A        string
	B        string
	Within   float64
	Adjacent *bool
}

// AreaPayload (§1 row 16): template query. Pure.
type AreaPayload struct {
	Origin string
	Shape  string
	Size   float64
	Caught []string
}

// Condition is one entry of ActorState.Conditions. Key is closed per pack
// (homebrew adds keys via pack minor bump, never free text at roll time).
// Until is structured (UntilManual, "turn_start:<actor>:<round>", or a
// pack-named expiry). Source is the hook id that applied it.
type Condition struct {
	Key    string
	Until  string
	Source string
}

// ActorState is the per-actor slice of the state minimum (ontology §2).
// Traits and Pools are pack-shaped (†) and therefore maps here; Positions
// are unit-agnostic. A nil Position means theatre-of-mind.
type ActorState struct {
	ID         string
	Name       string
	Traits     map[string]any
	Pools      map[string]int
	Conditions []Condition
	Position   map[string]any
}

// TurnState is the turn slice of the state minimum (ontology §2).
type TurnState struct {
	Round       int
	ActiveActor string
}

// State is the state minimum every hook reads (ontology §2). Hooks read any
// path but write only via do verbs; unknown paths fail validation against
// the core schema plus pack trait/pool names.
type State struct {
	Actors map[string]ActorState
	Turn   TurnState
}

// KeepSpec is an event-level dice-keep spec: roll Count dice, keep Keep of
// them, highest-first when KeepHigh. The DIE SIZE is pack data
// (e.g. dnd5e attack_die), never core — the core only decides count/keep so
// Part I names no die sizes, per the ontology reading guide.
type KeepSpec struct {
	Count    int
	Keep     int
	KeepHigh bool
}

// AdvDice computes the event-level dice spec from advantage/disadvantage
// flags (ontology Part II, dnd5e-2024: adv XOR dis → 2-keep-highest /
// 2-keep-lowest, else straight). Advantage and disadvantage CANCEL to a
// straight roll; they never stack. Dice-affecting state is computed by the
// engine from event fields plus pack rules — hooks never touch the dice
// (ontology §0.5).
func AdvDice(adv, dis bool) KeepSpec {
	switch {
	case adv && !dis:
		return KeepSpec{Count: 2, Keep: 1, KeepHigh: true}
	case dis && !adv:
		return KeepSpec{Count: 2, Keep: 1, KeepHigh: false}
	default:
		return KeepSpec{Count: 1, Keep: 1, KeepHigh: true}
	}
}
