// Command json2fm-5e (formerly extract-class-frontmatter) is an experiment in pulling gameplay data
// from the 5e.tools-typed source JSON (fixtures/data/class/*.json) into the
// `semiplane:` frontmatter namespace of the compendium Markdown, so the
// character-creator wizard can read classes/subclasses as data.
//
// Rules it intentionally honors (tech-stack §5):
//   - machine data lives only inside the top-level `semiplane:` frontmatter key;
//   - regeneration is marker-scoped and idempotent (only that key is rewritten;
//     every other frontmatter key and all prose are untouched);
//   - extraction is structural (typed JSON), never parsed prose.
//
// Usage:
//
//	go run ./tools/json2fm-5e -class Barbarian@XPHB
//	go run ./tools/json2fm-5e -class Barbarian@XPHB -dry-run
//	go run ./tools/json2fm-5e -class Barbarian@XPHB -print
//	go run ./tools/json2fm-5e -background Soldier@XPHB
//	go run ./tools/json2fm-5e -race Dwarf@XPHB
//	go run ./tools/json2fm-5e -feat "Boon of Fate@XPHB"
//	go run ./tools/json2fm-5e -spell "Fireball@XPHB"
//	go run ./tools/json2fm-5e -class "" -spell-all       # bulk
//	go run ./tools/json2fm-5e -optfeature "Subtle Spell@XPHB"
//
// Every kind is an independent target (several kinds may be processed in one
// invocation); an empty kind flag means "skip that kind". All kinds write the
// same marker-scoped `semiplane:` namespace: class/subclass into the classes
// dir, background into the backgrounds dir, species into the races dir, feat
// into the feats dir, spell into the spells dir, optional feature into the
// optional-features dir (see the per-kind -out flags). -spell-all walks a
// whole spell source file in one pass (the source JSON and the class-list
// join are parsed once) and prints a count summary.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// ── 5e.tools source JSON shapes (only the fields this tool reads) ──

type Choice struct {
	Count int      `json:"count"`
	From  []string `json:"from"`
}

type SkillProficiency struct {
	Choose *Choice `json:"choose"`
}

type Class struct {
	Name                string           `json:"name"`
	Source              string           `json:"source"`
	Page                int              `json:"page"`
	Srd52               bool             `json:"srd52"`
	PrimaryAbility      []map[string]any `json:"primaryAbility"`
	Proficiency         []string         `json:"proficiency"`         // saving throws
	SpellcastingAbility string           `json:"spellcastingAbility"` // casters only, e.g. "int"
	HD                  json.RawMessage  `json:"hd"`
	FeatProgression     []struct {
		Name        string         `json:"name"`
		Category    []string       `json:"category"` // feat buckets: e.g. EB, FS
		Progression map[string]int `json:"progression"`
	} `json:"featProgression"`
	StartingProficiencies struct {
		Armor   []any              `json:"armor"` // string names or {proficiency: …} objects
		Weapons []any              `json:"weapons"`
		Skills  []SkillProficiency `json:"skills"`
	} `json:"startingProficiencies"`
	StartingEquipment struct {
		DefaultData []map[string]json.RawMessage `json:"defaultData"`
		Default     []string                     `json:"default"` // prose (PHB-style); kept out of the namespace
		Entries     []string                     `json:"entries"` // prose; kept out of the namespace
	} `json:"startingEquipment"`
	Multiclassing struct {
		Requirements        map[string]any `json:"requirements"`
		ProficienciesGained map[string]any `json:"proficienciesGained"`
	} `json:"multiclassing"`
	ClassTableGroups []TableGroup      `json:"classTableGroups"`
	SubclassTitle    string            `json:"subclassTitle"`
	ClassFeatures    []json.RawMessage `json:"classFeatures"` // string refs or gainSubclassFeature objects
}

type TableGroup struct {
	ColLabels []string   `json:"colLabels"`
	Rows      []TableRow `json:"rows"`
	// Spell-slot spacer tables present the same row shape under this key
	// instead of rows (every XPHB caster's second table group).
	RowsSpellProgression []TableRow `json:"rowsSpellProgression"`
}

type TableRow []any

type Subclass struct {
	Name             string   `json:"name"`
	ShortName        string   `json:"shortName"`
	Source           string   `json:"source"`
	Srd52            bool     `json:"srd52"`
	ClassName        string   `json:"className"`
	ClassSource      string   `json:"classSource"`
	Page             int      `json:"page"`
	SubclassFeatures []string `json:"subclassFeatures"`
}

type ClassFeature struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	ClassName   string `json:"className"`
	ClassSource string `json:"classSource"`
	Level       int    `json:"level"`
	Srd52       bool   `json:"srd52"`
	Entries     []any  `json:"entries"`
}

type SubclassFeature struct {
	Name              string `json:"name"`
	Source            string `json:"source"`
	ClassName         string `json:"className"`
	ClassSource       string `json:"classSource"`
	SubclassShortName string `json:"subclassShortName"`
	SubclassSource    string `json:"subclassSource"`
	Level             int    `json:"level"`
	Srd52             bool   `json:"srd52"`
	Entries           []any  `json:"entries"`
}

type SourceFile struct {
	Class           []Class           `json:"class"`
	Subclass        []Subclass        `json:"subclass"`
	ClassFeature    []ClassFeature    `json:"classFeature"`
	SubclassFeature []SubclassFeature `json:"subclassFeature"`
}

// fixtures/data/backgrounds.json — classes use file-per-class; these three
// kinds live in one flat file each, keyed by a top-level list.
type Background struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Page   int    `json:"page"`
	Srd52  bool   `json:"srd52"`
	// Ability score increases. The 2024 corpus carries these exclusively as
	// {choose: {weighted: {from: [...], weights: [...]}}} blocks; they are
	// carried verbatim, never interpreted into prose.
	Ability []map[string]any `json:"ability"`
	// Granted origin feats: [{"savage attacker|xphb": true}] — one map per
	// grant, ref keys in the shared 5e.tools "name|source" convention.
	Feats              []map[string]any             `json:"feats"`
	SkillProficiencies []map[string]any             `json:"skillProficiencies"`
	ToolProficiencies  []map[string]any             `json:"toolProficiencies"`
	StartingEquipment  []map[string]json.RawMessage `json:"startingEquipment"` // bundles, as class defaultData
}

type BackgroundFile struct {
	Background []Background `json:"background"`
}

type Race struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Page   int    `json:"page"`
	Srd52  bool   `json:"srd52"`
	// Creature size codes (S/M series). Everything else the note shows as
	// prose (traits, darkvision, resistances) is deliberately not extracted.
	Size []string `json:"size"`
	// Plain numbers in the whole gated corpus; composite shapes ({walk, fly})
	// are skipped rather than guessed (see extractSpecies).
	Speed json.RawMessage `json:"speed"`

	LanguageProficiencies []map[string]any `json:"languageProficiencies"`
}

type RaceFile struct {
	Race []Race `json:"race"`
}

type Feat struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Page   int    `json:"page"`
	Srd52  bool   `json:"srd52"`
	// Mechanic category bucket (EB = Epic Boon, O = Origin, G = General,
	// FS = Fighting Style …); absent for most 2014-era feats.
	Category     string                       `json:"category"`
	Prerequisite []map[string]json.RawMessage `json:"prerequisite"` // ordered blocks
}

type FeatFile struct {
	Feat []Feat `json:"feat"`
}

// fixtures/data/spells/spells-<source>.json — one flat file per spell source,
// like the class files. The records carry the structural casting data (time,
// range, duration, components) but NO classes key: the class/subclass lists
// come from fixtures/data/generated/gendata-spell-source-lookup.json, which is
// keyed by lowercase spell name per source (see loadSpellJoins). `entries` and
// `entriesHigherLevel` prose stays out of the namespace entirely.
//
// The structural sub-shapes below are decoded block-by-block through digest
// helpers, mirroring the feat prerequisite discipline: a block carrying an
// unmodeled key is skipped whole (with a warn), never half-read.
type Spell struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Page   int    `json:"page"`
	// Srd52 is the redistribution flag; on spell records it arrives in three
	// shapes — bool, null (not flagged), and string (the spell is published
	// in SRD 5.2 under a RENAME, e.g. "Nystul's Magic Aura" → "Arcanist's
	// Magic Aura"). Either way it gates; the string is also the compendium
	// note's identity name (see spellDisplayName).
	Srd52 json.RawMessage `json:"srd52"`
	Level int             `json:"level"`
	// School is the source's one-letter class code (A abjuration, C conjuration,
	// D divination, E enchantment, I illusion, N necromancy, T transmutation,
	// V evocation). Carried verbatim — the mapping is schema, not corpus data.
	School      string                       `json:"school"`
	Time        []map[string]json.RawMessage `json:"time"`
	Range       map[string]json.RawMessage   `json:"range"`
	Duration    []map[string]json.RawMessage `json:"duration"`
	Components  map[string]json.RawMessage   `json:"components"`
	SavingThrow []string                     `json:"savingThrow"`
	// Damage is deliberately optional: the SRD 5.2 corpus carries none (some
	// sources carry a `damage` object, and Fireball's is null there too — the
	// dice are only uniform once a source structures them). Emitted only where
	// the record itself carries it; never inferred from prose.
	Damage map[string]json.RawMessage `json:"damage"`
}

type SpellFile struct {
	Spell []Spell `json:"spell"`
}

// fixtures/data/optionalfeatures.json — the last flat file. Notes exist under
// fixtures/compendium/optional-features/ for the SRD 5.2 set.
type OptionalFeature struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Page   int    `json:"page"`
	Srd52  bool   `json:"srd52"`
	// Feature-area tokens in the source vocabulary (MM Metamagic, EI Eldritch
	// Invocation, PB Pact Boon, …), carried verbatim — the loose class tie.
	FeatureType  []string                     `json:"featureType"`
	Consumes     map[string]json.RawMessage   `json:"consumes"`
	Prerequisite []map[string]json.RawMessage `json:"prerequisite"`
}

type OptionalFeatureFile struct {
	OptionalFeature []OptionalFeature `json:"optionalfeature"`
}

// ── the spell class-list join ──

// The gendata lookup is keyed by lowercase spell name inside a lowercase
// source section: {"xphb": {"fireball": {"class": {"XPHB": {"Sorcerer": true}},
// "subclass": {"XPHB": {"Bard": {"XPHB": {"Lore": {"name": "College of
// Lore"}}}}}}}}. The spell records themselves carry no classes key.
type spellJoinSub struct {
	Name string `json:"name"`
}

type spellJoinSpell struct {
	// Class tables are {classSource: {className: true-or-object}} — most
	// entries are `true`, some carry {definedInSources: [...]} naming the
	// section the class learns it from (SatO-era entries). Both shapes carry
	// the same name-level fact, so both include; false/null stays out.
	Class    map[string]map[string]any                                `json:"class"`
	Subclass map[string]map[string]map[string]map[string]spellJoinSub `json:"subclass"`
}

// ── `semiplane:` namespace output model ──

type FeatureRef struct {
	Name  string `yaml:"name"`
	Slug  string `yaml:"slug"`
	Level int    `yaml:"level"`
	// Recurrence discriminator: repeated slugs are one feature *slot* with
	// several entries. The first entry of a group carrys op: value; every
	// later one carries op: extends plus refSlug pointing at that first entry
	// (later entries are additive — new options, another pick, a raised
	// value). Features that appear once carry no discriminator.
	Op      string `yaml:"op,omitempty"`      // value | extends
	RefSlug string `yaml:"refSlug,omitempty"` // extends → the group's first occurrence
	// Structured option choices carried in the typed JSON ({type: options}
	// blocks); absent when the choice only exists as prose. Never parsed prose.
	Choices []ChoiceMeta `yaml:"choices,omitempty"`
}

type SubclassRef struct {
	Name      string   `yaml:"name"`
	ShortName string   `yaml:"shortName"`
	Source    string   `yaml:"source"`
	Page      int      `yaml:"page,omitempty"`
	Features  []string `yaml:"features"`
	Note      string   `yaml:"note,omitempty"` // compendium Markdown, if it exists
}

type Progression struct {
	Level     int            `yaml:"level"`
	PB        int            `yaml:"pb"`
	Features  []string       `yaml:"features,omitempty"`
	Resources map[string]any `yaml:",inline"` // class-table columns, e.g. rages
}

type ClassMeta struct {
	Name                string            `yaml:"name"`
	Source              string            `yaml:"source"`
	Page                int               `yaml:"page,omitempty"`
	PrimaryAbility      []string          `yaml:"primaryAbility"`
	Saves               []string          `yaml:"saves"`
	SpellcastingAbility string            `yaml:"spellcastingAbility,omitempty"`
	HP                  HP                `yaml:"hp"`
	Proficiencies       Proficiencies     `yaml:"proficiencies"`
	Multiclass          map[string]any    `yaml:"multiclassProficiencies,omitempty"`
	MulticlassGate      *MulticlassGate   `yaml:"multiclass,omitempty"`
	StartingEquipment   []EquipmentOption `yaml:"startingEquipment"`
	SubclassTitle       string            `yaml:"subclassTitle"`
	Subclasses          []SubclassRef     `yaml:"subclasses"`
	Features            []FeatureRef      `yaml:"features"`
	FeatProgression     []FeatTier        `yaml:"featProgression,omitempty"`
	Progression         []Progression     `yaml:"progression"`
}

// MulticlassGate is the ability-score gate on multiclassing INTO the class
// (e.g. PHB Wizard's {"int": 13}), sitting next to multiclassProficiencies so
// both halves of the multiclassing rule read together. The 2024 rules dropped
// it, so gated records carry none — the block stays absent rather than being
// inferred from another edition's record.
type MulticlassGate struct {
	Requirements map[string]any `yaml:"requirements,omitempty"`
}

// FeatTier is the compact side-table for named feat grants: which level, which
// feat name, and its mechanic category (EB = Epic Boon, FS = Fighting Style…).
type FeatTier struct {
	Level    int      `yaml:"level"`
	Name     string   `yaml:"name"`
	Category []string `yaml:"category,omitempty"`
}

type HP struct {
	HitDie     int     `yaml:"hitDie"`
	FirstLevel string  `yaml:"firstLevel"`
	PerLevel   HPLevel `yaml:"perLevel"`
}

// HPLevel expresses the choose-roll-or-fake-average rule programmatically:
// both variants are dice-grammar strings (`@con` = Constitution modifier),
// rollMin bounds a bad dN so a level-up can never lose HP.
type HPLevel struct {
	Roll    string `yaml:"roll"`    // "1d12 + @con"
	RollMin int    `yaml:"rollMin"` // die result minimum (1)
	Flat    string `yaml:"flat"`    // "7 + @con" — ceiling of hitDie/2 + 1
}

type Proficiencies struct {
	Armor   []string    `yaml:"armor,omitempty"`
	Weapons []string    `yaml:"weapons,omitempty"`
	Skills  *ChoiceMeta `yaml:"skills,omitempty"`
}

type ChoiceMeta struct {
	Count int      `yaml:"count"`
	From  []string `yaml:"from"`
}

type EquipmentOption struct {
	ID    string      `yaml:"id"`
	Items []ItemEntry `yaml:"items"`
}

// ItemEntry keeps quantity separate from the name; gold grants use gp.
type ItemEntry struct {
	Item string `yaml:"item,omitempty"`
	Qty  int    `yaml:"qty,omitempty"` // 1 → omitted
	GP   int    `yaml:"gp,omitempty"`
}

type SubclassMeta struct {
	Name      string       `yaml:"name"`
	ShortName string       `yaml:"shortName"`
	Source    string       `yaml:"source"`
	Page      int          `yaml:"page,omitempty"`
	Class     ClassPoint   `yaml:"class"`
	Features  []FeatureRef `yaml:"features"`
}

type ClassPoint struct {
	Name   string `yaml:"name"`
	Source string `yaml:"source"`
	Note   string `yaml:"note,omitempty"`
}

type Namespace struct {
	// SchemaVersion marks the generation of the namespace shape (mirrors
	// tech-stack §3.8's ontology pinning): consumers can tell schema
	// generations apart without diffing every field. Bump on shape changes.
	SchemaVersion   int                  `yaml:"schemaVersion,omitempty"`
	GeneratedFrom   string               `yaml:"generatedFrom"`
	Class           *ClassMeta           `yaml:"class,omitempty"`
	Subclass        *SubclassMeta        `yaml:"subclass,omitempty"`
	Background      *BackgroundMeta      `yaml:"background,omitempty"`
	Species         *SpeciesMeta         `yaml:"species,omitempty"`
	Feat            *FeatMeta            `yaml:"feat,omitempty"`
	Spell           *SpellMeta           `yaml:"spell,omitempty"`
	OptionalFeature *OptionalFeatureMeta `yaml:"optionalFeature,omitempty"`
}

// ── background / species / feat namespaces (v0, dnd5e-scoped) ──

// BackgroundMeta carries the character-creator-facing facts of a background:
// its ability-score pick form, proficiencies, granted origin feats, and the
// structured starting-equipment bundles. Everything else (feature prose,
// flavor) stays prose on the note.
type BackgroundMeta struct {
	Name   string `yaml:"name"`
	Source string `yaml:"source"`
	Page   int    `yaml:"page,omitempty"`
	// Ability score increases, carried as the source's typed blocks (see
	// Background.Ability): every 2024 background is the two-block
	// choose/weighted pair. Fixed-value blocks would carry straight maps.
	Ability []map[string]any `yaml:"ability,omitempty"`
	// Proficiencies stay in the source's typed vocabulary: fixed grants are
	// {"athletics": true} maps; pickers keep their tokens ({"anyGamingSet": 1}
	// = any one gaming set). No name lists are invented.
	Skills []map[string]any `yaml:"skills,omitempty"`
	Tools  []map[string]any `yaml:"tools,omitempty"`
	// Origin feats granted by this background, linked to their compendium
	// notes (same convention as SubclassRef.Note). From the `feats` refs.
	OriginFeats []OriginFeat `yaml:"originFeats,omitempty"`
	// Same structured {item, qty} / {gp} form as the class namespace.
	StartingEquipment []EquipmentOption `yaml:"startingEquipment,omitempty"`
}

// OriginFeat is a background's granted origin feat, resolved from its ref.
// Some refs carry a pick variant before the source ("magic initiate; cleric")
// — kept as a separate token, never folded into the name.
type OriginFeat struct {
	Name    string `yaml:"name"`
	Variant string `yaml:"variant,omitempty"`
	Source  string `yaml:"source"`
	Note    string `yaml:"note,omitempty"` // compendium Markdown, if it exists
}

// SpeciesMeta: identity + the few typed pickups on the race record. Traits
// stay prose (no NLP, per the tool's core rule).
type SpeciesMeta struct {
	Name   string `yaml:"name"`
	Source string `yaml:"source"`
	Page   int    `yaml:"page,omitempty"`
	// Size codes as the source stores them (e.g. [M], [S, M]).
	Size []string `yaml:"size,omitempty"`
	// Walk speed in feet; composite speed shapes are skipped, not parsed.
	Speed int `yaml:"speed,omitempty"`
	// Language proficiencies in the source's typed vocabulary (like
	// background skills: {"auran": true}, {"anyStandard": 2} …), verbatim.
	Languages []map[string]any `yaml:"languages,omitempty"`
}

// FeatMeta: identity plus its mechanic bucket and machine-shaped
// prerequisites; benefits/effects stay prose.
type FeatMeta struct {
	Name   string `yaml:"name"`
	Source string `yaml:"source"`
	Page   int    `yaml:"page,omitempty"`
	// Category bucket straight from the source: EB, FS, FS:P, FS:R, O, G.
	Category string `yaml:"category,omitempty"`
	// Prerequisites where the typed JSON is machine-shaped (see
	// prerequisiteDigest); blocks with unmodeled terms are skipped whole.
	Prerequisites []FeatRequirement `yaml:"prerequisites,omitempty"`
}

// FeatRequirement is one prerequisite block, reduced to the typed terms the
// corpus exercises: a required level, ability-score minimums, named feature
// grants it rides on, and having the Spellcasting feature (the source's
// spellcasting2020 flag). Multiple blocks are the source's way of listing
// alternative prerequisites (Grappler: Str or Dex 13).
type FeatRequirement struct {
	Level        int            `yaml:"level,omitempty"`
	Ability      map[string]int `yaml:"ability,omitempty"`
	Feature      []string       `yaml:"feature,omitempty"`
	Spellcasting bool           `yaml:"spellcasting,omitempty"`
}

// ── spell namespace ──

// NameSourceRef is a bare typed reference pair (class, subclass, or a
// prerequisite's optional feature). Source is omitted when the source says so
// (some prerequisite class refs carry no source segment).
type NameSourceRef struct {
	Name   string `yaml:"name"`
	Source string `yaml:"source,omitempty"`
}

// SpellSubclassRef is one join-resolved subclass a spell belongs to. The note
// link resolves into the classes dir when that note exists (relative to the
// spell note, so ../classes/…).
type SpellSubclassRef struct {
	Name      string        `yaml:"name"`
	ShortName string        `yaml:"shortName"`
	Source    string        `yaml:"source"`
	Class     NameSourceRef `yaml:"class"`
	Note      string        `yaml:"note,omitempty"`
}

// SpellMeta carries the structural casting data of a spell, the machine-half
// of the note already shows as prose. Prose (`entries`, `entriesHigherLevel`)
// stays out; damage is carried only where the record structurally carries it.
type SpellMeta struct {
	Name   string `yaml:"name"`
	Source string `yaml:"source"`
	Page   int    `yaml:"page,omitempty"`
	Level  int    `yaml:"level"`
	// School is the source's one-letter code, verbatim (V = evocation…).
	School      string           `yaml:"school"`
	Time        []SpellTime      `yaml:"time,omitempty"`
	Range       *SpellRange      `yaml:"range,omitempty"`
	Duration    []SpellDuration  `yaml:"duration,omitempty"`
	Components  *SpellComponents `yaml:"components,omitempty"`
	SavingThrow []string         `yaml:"savingThrow,omitempty"`
	Damage      *SpellDamage     `yaml:"damage,omitempty"`
	// Class/subclass lists come from the gendata join, keyed to the note's
	// spell — never from note prose.
	Classes    []NameSourceRef    `yaml:"classes,omitempty"`
	Subclasses []SpellSubclassRef `yaml:"subclasses,omitempty"`
}

// SpellTime is one casting-time block. `condition`/`note` are short typed
// tokens in the source (the smite triggers, Plant Growth's modes) — carried
// verbatim with the block rather than half-dropping the rule.
type SpellTime struct {
	Number    int    `yaml:"number"`
	Unit      string `yaml:"unit"`
	Condition string `yaml:"condition,omitempty"`
	Note      string `yaml:"note,omitempty"`
}

type SpellRange struct {
	Type     string              `yaml:"type"`
	Distance *SpellRangeDistance `yaml:"distance,omitempty"`
}

// SpellRangeDistance: amount is present only for measurable distance types
// (feet/miles); touch/self/unlimited/sight carry type alone.
type SpellRangeDistance struct {
	Type   string `yaml:"type"`
	Amount int    `yaml:"amount,omitempty"`
}

type SpellDurationSpan struct {
	Type   string `yaml:"type"`
	Amount int    `yaml:"amount"`
}

type SpellDuration struct {
	Type string `yaml:"type"`
	// Duration is the timed span ({minute, 10}); instant/special/permanent
	// blocks carry none.
	Duration      *SpellDurationSpan `yaml:"duration,omitempty"`
	Concentration bool               `yaml:"concentration,omitempty"`
	// End conditions from the source vocabulary (dispel, trigger).
	Ends []string `yaml:"ends,omitempty"`
}

type SpellComponents struct {
	V bool `yaml:"v,omitempty"`
	S bool `yaml:"s,omitempty"`
	// M is the material component: either the plain verbatim name or, where
	// the source types it, {text, gp, consume} (gp converted from the source's
	// copper-unit cost, same convention as equipment values).
	M *SpellMaterial `yaml:"m,omitempty"`
}

type SpellMaterial struct {
	Text    string `yaml:"text"`
	GP      int    `yaml:"gp,omitempty"`
	Consume bool   `yaml:"consume,omitempty"`
}

// SpellDamage is emitted only where the record structurally carries it
// ({dice, damageType, bonus}); a block with any unmodeled key is skipped whole
// with a warn — dice are never parsed out of prose. The SRD 5.2 corpus
// exercises none, so the key is absent on every gated note.
type SpellDamage struct {
	Dice       string `yaml:"dice,omitempty"`
	Bonus      int    `yaml:"bonus,omitempty"`
	DamageType string `yaml:"damageType,omitempty"`
}

// ── optional-feature namespace ──

// OptionalFeatureMeta: identity + the feature-area tokens + machine-shaped
// prerequisites. `entries` prose stays out; the class refs ride the
// prerequisite level blocks, the only structural carrier.
type OptionalFeatureMeta struct {
	Name        string   `yaml:"name"`
	Source      string   `yaml:"source"`
	Page        int      `yaml:"page,omitempty"`
	FeatureType []string `yaml:"featureType,omitempty"`
	// Consumes is the resource spent (e.g. Sorcery Point) with its typed
	// amount, in the source vocabulary.
	Consumes      map[string]any               `yaml:"consumes,omitempty"`
	Prerequisites []OptionalFeatureRequirement `yaml:"prerequisites,omitempty"`
}

// OptionalFeatureRequirement is one prerequisite block, reduced to the typed
// terms the corpus exercises: the level (+ granting class/subclass ref), the
// pact boon token, spell refs (PHB-era string refs) and prerequisite
// optional-feature refs. Blocks with unmodeled keys — prose items, the XPHB
// cantrip-filter objects, otherSummary — are skipped whole.
type OptionalFeatureRequirement struct {
	Level           int             `yaml:"level,omitempty"`
	Class           *NameSourceRef  `yaml:"class,omitempty"`
	Subclass        *NameSourceRef  `yaml:"subclass,omitempty"`
	Pact            string          `yaml:"pact,omitempty"`
	Spell           []string        `yaml:"spell,omitempty"`
	OptionalFeature []NameSourceRef `yaml:"optionalFeature,omitempty"`
}

// schemaVersion is the current namespace-shape generation. v1 adds the
// spellcastingAbility field, the multiclass requirements gate, and this marker.
const schemaVersion = 1

// ── helpers ──

func slug(s string) string {
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func str(v any) string { s, _ := v.(string); return s }

// labelKey turns one column label into a resource key. Labels wrapped in a
// 5e.tools tag ("{@filter Cantrips|spells|level=0|class=wizard}") carry the
// readable name before the first "|" — unwrap to that so the key matches how
// the table reads, never the tag plumbing or a hardcoded column list.
func labelKey(label string) string {
	if strings.HasPrefix(label, "{@") {
		name := strings.SplitN(label, " ", 2)
		if len(name) == 2 {
			label = name[1]
		}
		if i := strings.Index(label, "|"); i >= 0 {
			label = label[:i]
		}
	}
	return slug(label)
}

// cellValue coerces one table cell into the scalar resource value it carries:
//   - bare numbers (JSON ints decode as float64) and numeric strings → int;
//   - booleans stay booleans;
//   - {type: bonus, value: n} maps unwrap to their value;
//   - strings stay strings.
//
// Other shapes ({type: dice} spell-dice cells) have no scalar meaning yet and
// produce no resource key rather than prose guessing.
func cellValue(cell any) (any, bool) {
	switch c := cell.(type) {
	case string:
		if n, err := strconv.Atoi(c); err == nil {
			return n, true
		}
		return c, true
	case int:
		return c, true
	case float64:
		return int(c), true
	case bool:
		return c, true
	case map[string]any:
		if v, ok := c["value"]; ok {
			if f, ok := v.(float64); ok {
				return int(f), true
			}
			return v, true
		}
		return nil, false
	default:
		return nil, false
	}
}

// tableRows returns the level-aligned rows of a table group: spell-slot
// progression groups store them under rowsSpellProgression, everything else
// under rows — the row shape is identical (one row per class level).
func tableRows(g TableGroup) []TableRow {
	if len(g.Rows) > 0 {
		return g.Rows
	}
	return g.RowsSpellProgression
}

// ref "name|class|src|level" → (name, level)
func parseFeatureRef(ref string) (string, int) {
	parts := strings.Split(ref, "|")
	lvl, _ := strconv.Atoi(parts[len(parts)-1])
	return parts[0], lvl
}

// classFeatureRefs flattens the mixed classFeatures list (string refs +
// {"classFeature": ref, "gainSubclassFeature": true} objects) into plain refs.
func featureRefs(cls *Class) []string {
	var out []string
	for _, raw := range cls.ClassFeatures {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			out = append(out, s)
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err == nil {
			if s := str(obj["classFeature"]); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// ── extraction ──

// annotateRecurrence groups repeated slugs into feature slots, structurally:
// the first entry in a group gets op: value, later ones op: extends +
// refSlug anchoring back to it. The corpus uses one shape everywhere (XPHB
// "Ability Score Improvement" 4/8/12/16, "Subclass Feature" 6/10/14,
// "Improved Brutal Strike" 13/17 …), and every later entry is additive —
// another pick, new options, or a raised value — so a single extends op is
// enough to disambiguate; the SPA can still order within the slot by level.
func annotateRecurrence(features []FeatureRef) {
	repeats := map[string]int{}
	for _, f := range features {
		repeats[f.Slug]++
	}
	first := map[string]bool{}
	for i := range features {
		f := &features[i]
		if repeats[f.Slug] < 2 {
			continue // appears once — no discriminator, field stays absent
		}
		if !first[f.Slug] {
			f.Op = "value" // introduce the slot; later entries extend it
			first[f.Slug] = true
			continue
		}
		f.Op = "extends"
		f.RefSlug = f.Slug
	}
}

// findClassFeature resolves the class feature record behind a
// "Name|Class|Src|Level" ref, mirroring the ref segment order.
func findClassFeature(sf *SourceFile, ref string, cls *Class) *ClassFeature {
	name, level := parseFeatureRef(ref)
	for i := range sf.ClassFeature {
		f := &sf.ClassFeature[i]
		if f.Name == name && f.ClassName == cls.Name && f.ClassSource == cls.Source && f.Level == level {
			return f
		}
	}
	return nil
}

// collectOptionBlocks gathers every {type: options} block in a feature's
// entry tree, in document order.
func collectOptionBlocks(v any, out *[]map[string]any) {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			collectOptionBlocks(e, out)
		}
	case map[string]any:
		if t["type"] == "options" {
			*out = append(*out, t)
		}
		for _, e := range t {
			collectOptionBlocks(e, out)
		}
	}
}

// optionName resolves one option's display name structurally: a per-option
// name override, otherwise the first segment of its ref — the convention for
// refClassFeature/refSubclassFeature/refOptionalfeature (reprint sources ride
// in later segments and are dropped here, e.g. "Careful Spell|XPHB").
func optionName(opt map[string]any) string {
	if n := str(opt["name"]); n != "" {
		return n
	}
	for _, key := range []string{"subclassFeature", "classFeature", "optionalfeature"} {
		if ref := str(opt[key]); ref != "" {
			return strings.SplitN(ref, "|", 2)[0]
		}
	}
	return ""
}

// featureChoices turns a feature record's {type: options} blocks into
// ChoiceMeta payloads. Features whose choices exist only as prose yield
// nothing at all — their entries are never parsed.
func featureChoices(rec *ClassFeature) []ChoiceMeta {
	var blocks []map[string]any
	collectOptionBlocks(rec.Entries, &blocks)
	var out []ChoiceMeta
	for _, b := range blocks {
		entries, _ := b["entries"].([]any)
		var from []string
		for _, e := range entries {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if n := optionName(m); n != "" {
				from = append(from, n)
			}
		}
		if len(from) == 0 {
			continue // an empty/garbled options block carries no choice
		}
		count := 1
		if f, ok := b["count"].(float64); ok {
			count = int(f)
		}
		out = append(out, ChoiceMeta{Count: count, From: from})
	}
	return out
}

// cfg carries extraction knobs shared across the call tree.
type cfg struct {
	srOnly   bool // gate on the srd52 provenance field (default true)
	outDir   string
	featDir  string // feats dir — where background origin-feat note links resolve
	dataDir  string // root of the typed source JSON
	spellDir string // spells dir — where spell notes live
	optDir   string // optional-features dir — where optional-feature notes live
}

func classFeatureRecordIndex(sf *SourceFile, cls *Class) map[string]bool {
	// Feature refs don't carry provenance; resolved records do. Key in the
	// ref segment order: name|class|classSrc|level.
	srd := map[string]bool{}
	for i := range sf.ClassFeature {
		f := &sf.ClassFeature[i]
		if f.ClassName != cls.Name || f.ClassSource != cls.Source {
			continue
		}
		srd[fmt.Sprintf("%s|%d", f.Name, f.Level)] = f.Srd52
	}
	return srd
}

func extractClass(sf *SourceFile, cls *Class, c *cfg) *Namespace {
	// Class feature refs are already ordered; dedupe through "Name@Level"
	// and drop records without srd52 when gated.
	srdIdx := classFeatureRecordIndex(sf, cls)
	var features []FeatureRef
	seen := map[string]bool{}
	for _, ref := range featureRefs(cls) {
		name, lvl := parseFeatureRef(ref)
		if c.srOnly && !srdIdx[fmt.Sprintf("%s|%d", name, lvl)] {
			continue
		}
		key := fmt.Sprintf("%s@%d", name, lvl)
		if seen[key] {
			continue
		}
		seen[key] = true
		f := FeatureRef{Name: name, Slug: slug(name), Level: lvl}
		if rec := findClassFeature(sf, ref, cls); rec != nil {
			f.Choices = featureChoices(rec)
		}
		features = append(features, f)
	}
	annotateRecurrence(features)

	// progression: every table group — spell slots included — aligns one row
	// to one class level, and all groups share one per-level inlined resource
	// map (the row shape of rowsSpellProgression is identical to rows).
	levelCount := 0
	for _, g := range cls.ClassTableGroups {
		if n := len(tableRows(g)); n > levelCount {
			levelCount = n
		}
	}
	var prog []Progression
	for lvl := 0; lvl < levelCount; lvl++ {
		p := Progression{Level: lvl + 1, PB: 2 + (lvl)/4}
		p.Features = featuresAt(features, p.Level)
		res := map[string]any{}
		for _, g := range cls.ClassTableGroups {
			rows := tableRows(g)
			if lvl >= len(rows) {
				continue
			}
			for i, cell := range rows[lvl] {
				if i >= len(g.ColLabels) {
					continue
				}
				key := labelKey(g.ColLabels[i])
				if v, ok := cellValue(cell); ok {
					res[key] = v
				}
			}
		}
		p.Resources = res
		prog = append(prog, p)
	}

	// featProgression is a compact side-table of where named feat grants
	// ("Epic Boon", "Fighting Style") land by level.
	var featTiers []FeatTier
	for _, fp := range cls.FeatProgression {
		levels := make([]int, 0, len(fp.Progression))
		for k := range fp.Progression {
			lvl, err := strconv.Atoi(k)
			if err != nil {
				continue // non-numeric keys are not levels
			}
			levels = append(levels, lvl)
		}
		sort.Ints(levels)
		for _, lvl := range levels {
			featTiers = append(featTiers, FeatTier{Level: lvl, Name: fp.Name, Category: fp.Category})
		}
	}

	subs := collectSubclasses(sf, cls, c)

	// Resolve the net ability keys from the source shape: [{"str": true}].
	primary := []string{}
	for _, pa := range cls.PrimaryAbility {
		for k, v := range pa {
			if b, ok := v.(bool); !ok || b {
				primary = append(primary, k)
			}
		}
	}
	sort.Strings(primary)

	skillChoice := (*ChoiceMeta)(nil)
	if len(cls.StartingProficiencies.Skills) > 0 && cls.StartingProficiencies.Skills[0].Choose != nil {
		c := cls.StartingProficiencies.Skills[0].Choose
		skillChoice = &ChoiceMeta{Count: c.Count, From: c.From}
	}

	hd := 0
	if faces, _, err := hitDice(cls.HD); err == nil {
		hd = faces
	}

	mc := map[string]any{}
	for k, v := range cls.Multiclassing.ProficienciesGained {
		mc[k] = v
	}
	var mcHeader map[string]any
	if len(mc) > 0 {
		mcHeader = mc
	}
	// The requirements gate lives right next to the proficienciesGained copy
	// so every multiclass field is threaded in one place. Numeric floors
	// coerce to int; a record without a gate yields nil — the block stays
	// absent, never inferred from another edition's record.
	var mcGate *MulticlassGate
	if reqs := requirementsGate(cls.Multiclassing.Requirements); reqs != nil {
		mcGate = &MulticlassGate{Requirements: reqs}
	}

	return &Namespace{
		SchemaVersion: schemaVersion,
		GeneratedFrom: "class/class-" + strings.ToLower(cls.Name) + ".json",
		Class: &ClassMeta{
			Name:                cls.Name,
			Source:              cls.Source,
			Page:                cls.Page,
			PrimaryAbility:      primary,
			Saves:               cls.Proficiency,
			SpellcastingAbility: cls.SpellcastingAbility,
			HP:                  hpFormula(hd),
			Proficiencies: Proficiencies{
				Armor:   profList(cls.StartingProficiencies.Armor),
				Weapons: profList(cls.StartingProficiencies.Weapons),
				Skills:  skillChoice,
			},
			Multiclass:        mcHeader,
			MulticlassGate:    mcGate,
			StartingEquipment: equipmentOptions(cls),
			SubclassTitle:     cls.SubclassTitle,
			Subclasses:        subs,
			Features:          features,
			FeatProgression:   featTiers,
			Progression:       prog,
		},
	}
}

func featuresAt(features []FeatureRef, lvl int) []string {
	out := []string{}
	for _, f := range features {
		if f.Level == lvl {
			out = append(out, f.Slug)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// hpFormula derives the level-1 / per-level HP expressions from the hit-die.
// @con is the dice-grammar variable for the Constitution modifier;
// flat is the "take the average instead of rolling" alternative.
func hpFormula(hd int) HP {
	return HP{
		HitDie:     hd,
		FirstLevel: fmt.Sprintf("%d + @con", hd),
		PerLevel: HPLevel{
			Roll:    fmt.Sprintf("1d%d + @con", hd),
			RollMin: 1,
			Flat:    fmt.Sprintf("%d + @con", hd/2+1),
		},
	}
}

// profList reads one startingProficiencies list: bare strings are kept
// verbatim ({@item …} tags ride through for the value layer to unwrap), and
// the {proficiency: …} object shape some records use (with a "full" prose
// caveat alongside — druids and metal armor) contributes its typed
// `proficiency` name. Anything else carries no typed proficiency name and is
// skipped, never parsed from prose.
func profList(raw []any) []string {
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		switch t := e.(type) {
		case string:
			out = append(out, t)
		case map[string]any:
			if p := str(t["proficiency"]); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// requirementsGate coerces the multiclassing requirement table into int
// floors ({int: 13}; JSON numbers decode as float64). Values that carry no
// numeric floor are dropped rather than guessed at; an empty or missing
// table yields nil so the multiclass block stays absent.
func requirementsGate(raw map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range raw {
		if f, ok := v.(float64); ok {
			out[k] = int(f)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// equipmentBundles parses the equipment-bundle defaults shared by classes
// (startingEquipment.defaultData) and backgrounds (startingEquipment):
// a list of bundles, each an option-id → entries map (pick one option per
// bundle). Option values come in two shapes: {"item": "..."} dicts with
// optional quantity/value, or bare "item|source" strings. The PHB-style
// equipmentType dicts are excluded for now (see notes in the write-up).
func equipmentBundles(defs []map[string]json.RawMessage) []EquipmentOption {
	var opts []EquipmentOption
	for _, def := range defs {
		ids := make([]string, 0, len(def))
		for id := range def {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			var entries []any
			if err := json.Unmarshal(def[id], &entries); err != nil {
				continue
			}
			items := []ItemEntry{}
			for _, e := range entries {
				switch v := e.(type) {
				case string:
					items = append(items, itemEntry(v, 1))
				case map[string]any:
					q := 1
					if f, ok := v["quantity"].(float64); ok {
						q = int(f)
					}
					if item := str(v["item"]); item != "" {
						items = append(items, itemEntry(item, q))
						continue
					}
					if cp, ok := v["value"].(float64); ok {
						items = append(items, ItemEntry{GP: int(cp / 100)})
					}
				}
			}
			opts = append(opts, EquipmentOption{ID: id, Items: items})
		}
	}
	return opts
}

func equipmentOptions(cls *Class) []EquipmentOption {
	return equipmentBundles(cls.StartingEquipment.DefaultData)
}

// itemEntry converts a source ref ("handaxe|xphb", optional quantity) into a
// structured {item, qty} entry; qty 1 stays implicit (omitted in YAML).
func itemEntry(ref string, qty int) ItemEntry {
	name := strings.SplitN(ref, "|", 2)[0]
	it := ItemEntry{Item: name}
	if qty > 1 {
		it.Qty = qty
	}
	return it
}

func collectSubclasses(sf *SourceFile, cls *Class, c *cfg) []SubclassRef {
	var refs []SubclassRef
	for i := range sf.Subclass {
		sc := &sf.Subclass[i]
		if sc.ClassName != cls.Name || sc.ClassSource != cls.Source {
			continue
		}
		if c.srOnly && !sc.Srd52 {
			continue // not SRD 5.2 — outside this corpus's redistribution gate
		}
		if len(sc.SubclassFeatures) == 0 {
			// `_copy` lookup stubs (e.g. PHB→XPHB reprints) carry no gameplay
			// data themselves; the record they copy is listed separately.
			continue
		}
		feats := resolveSubclassFeatureNames(sf, sc, c)
		r := SubclassRef{
			Name:      sc.Name,
			ShortName: sc.ShortName,
			Source:    sc.Source,
			Page:      sc.Page,
		}
		for _, f := range feats {
			r.Features = append(r.Features, f.Slug)
		}
		note := filepath.Join(c.outDir, subdirName(cls.Name, cls.Source, sc.Name, sc.Source))
		if fi, err := os.Stat(note); err == nil && !fi.IsDir() {
			r.Note = "./" + filepath.Base(note)
		}
		refs = append(refs, r)
	}
	return refs
}

// subdirName is the compendium note naming convention for subclasses:
// <class>-<classSrc>-<full subclass name slug>-<subSrc>.md.
func subdirName(class, classSrc, sub, subSrc string) string {
	return fmt.Sprintf("%s-%s-%s-%s.md", slug(class), strings.ToLower(classSrc), slug(sub), strings.ToLower(subSrc))
}

// featureKey mirrors the ref segment order: name|class|classSrc|short|shortSrc|level.
func featureKey(f *SubclassFeature) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%d",
		f.Name, f.ClassName, f.ClassSource, f.SubclassShortName, f.SubclassSource, f.Level)
}

// indexSubclassFeatures keys the resolved-level subclass feature records.
func indexSubclassFeatures(sf *SourceFile) map[string]*SubclassFeature {
	idx := map[string]*SubclassFeature{}
	for i := range sf.SubclassFeature {
		f := &sf.SubclassFeature[i]
		idx[featureKey(f)] = f
	}
	return idx
}

// resolveSubclassFeatureNames turns a subclass's SubclassFeatures refs into
// user-facing features. A header banner record (e.g. "Path of the Berserker"
// at the subclass-pick level) links its real features through a
// refSubclassFeature entry inside its entries — follow that ref so the feature
// falls out structurally (Frenzy), never by parsing prose.
func resolveSubclassFeatureNames(sf *SourceFile, sc *Subclass, c *cfg) []FeatureRef {
	idx := indexSubclassFeatures(sf)
	var out []FeatureRef
	seen := map[string]bool{}
	push := func(name string, lvl int) {
		key := fmt.Sprintf("%s@%d", name, lvl)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, FeatureRef{Name: name, Slug: slug(name), Level: lvl})
	}
	for _, ref := range sc.SubclassFeatures {
		rec, ok := idx[ref]
		if !ok {
			fmt.Fprintf(os.Stderr, "warn: unresolved subclass feature ref %s\n", ref)
			continue
		}
		if c.srOnly && !rec.Srd52 {
			continue
		}
		followed := false
		for _, e := range rec.Entries {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if inner := str(m["subclassFeature"]); inner != "" && m["type"] == "refSubclassFeature" {
				if target, ok := idx[inner]; ok {
					if c.srOnly && !target.Srd52 {
						continue
					}
					push(target.Name, target.Level)
					followed = true
				}
			}
		}
		if !followed {
			push(rec.Name, rec.Level)
		}
	}
	annotateRecurrence(out)
	return out
}

// hitDice accepts both shapes the sources use: a single object
// {"faces":12} or an array of such objects; returns the first die.
func hitDice(raw json.RawMessage) (int, int, error) {
	var one struct {
		Faces  int `json:"faces"`
		Number int `json:"number"`
	}
	if err := json.Unmarshal(raw, &one); err == nil && one.Faces > 0 {
		return one.Faces, one.Number, nil
	}
	var many []struct {
		Faces  int `json:"faces"`
		Number int `json:"number"`
	}
	if err := json.Unmarshal(raw, &many); err == nil && len(many) > 0 {
		return many[0].Faces, many[0].Number, nil
	}
	return 0, 0, fmt.Errorf("unrecognized hd shape")
}

func extractSubclass(sf *SourceFile, sc *Subclass, c *cfg) *Namespace {
	feats := resolveSubclassFeatureNames(sf, sc, c)
	classNote := "./" + slug(sc.ClassName) + "-" + strings.ToLower(sc.ClassSource) + ".md"
	if fi, err := os.Stat(filepath.Join(c.outDir, filepath.Base(classNote))); err != nil || fi.IsDir() {
		classNote = ""
	}
	return &Namespace{
		SchemaVersion: schemaVersion,
		GeneratedFrom: "class/class-" + strings.ToLower(sc.ClassName) + ".json",
		Subclass: &SubclassMeta{
			Name:      sc.Name,
			ShortName: sc.ShortName,
			Source:    sc.Source,
			Page:      sc.Page,
			Class:     ClassPoint{Name: sc.ClassName, Source: sc.ClassSource, Note: classNote},
			Features:  feats,
		},
	}
}

// ── background / species / feat extraction ──

// gateRecord enforces the srd52 provenance gate for the flat-file kinds,
// with the same default and the same escape hatch (-srd=false) as the class
// namespace. Called by each extract* so the refusal is unit-testable.
func gateRecord(kind, name, source string, flagged bool, c *cfg) error {
	if !c.srOnly || flagged {
		return nil
	}
	return fmt.Errorf("%s %s@%s is not flagged srd52; rerun with -srd=false to include it", kind, name, source)
}

// coerceNumbers rewrites every bare JSON number in a verbatim payload to a Go
// int when integral (JSON decode yields float64; the namespace keeps typed
// scalars so the SPA never distinguishes 2 from 2.0 in weights or counts).
func coerceNumbers(v any) any {
	switch t := v.(type) {
	case float64:
		if t == float64(int(t)) {
			return int(t)
		}
		return t
	case map[string]any:
		for k, e := range t {
			t[k] = coerceNumbers(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = coerceNumbers(e)
		}
		return t
	default:
		return v
	}
}

func coerceBlockList(blocks []map[string]any) []map[string]any {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		if len(b) == 0 {
			continue
		}
		out = append(out, coerceNumbers(b).(map[string]any))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func extractBackground(b *Background, c *cfg) (*Namespace, error) {
	if err := gateRecord("background", b.Name, b.Source, b.Srd52, c); err != nil {
		return nil, err
	}
	bm := &BackgroundMeta{
		Name:              b.Name,
		Source:            b.Source,
		Page:              b.Page,
		Ability:           coerceBlockList(b.Ability),
		Skills:            coerceBlockList(b.SkillProficiencies),
		Tools:             coerceBlockList(b.ToolProficiencies),
		OriginFeats:       resolveOriginFeats(b.Feats, c),
		StartingEquipment: equipmentBundles(b.StartingEquipment),
	}
	return &Namespace{
		SchemaVersion: schemaVersion,
		GeneratedFrom: "backgrounds.json",
		Background:    bm,
	}, nil
}

// parseOriginFeatRef splits an origin-feat ref into (base name, variant,
// source). Refs are lower-case keys of the form
// "savage attacker|xphb" or "magic initiate; cleric|xphb" — the "; pick"
// segment names the feat's class pick (the base feat record stays one).
func parseOriginFeatRef(ref string) (name, variant, source string, ok bool) {
	key, src, found := strings.Cut(ref, "|")
	if !found || key == "" {
		return "", "", "", false
	}
	base, variant, hasVariant := strings.Cut(key, ";")
	if !hasVariant {
		return base, "", strings.ToUpper(src), true
	}
	return strings.TrimSpace(base), strings.TrimSpace(variant), strings.ToUpper(src), true
}

// titleCase is the deterministic display fallback for feat refs stored
// lower-case by the source ("savage attacker" → "Savage Attacker"). The
// linked note remains the display authority; this never reads prose.
func titleCase(s string) string {
	words := strings.Split(s, " ")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// resolveOriginFeats turns a background's `feats` grants into linked refs.
// Notes are looked up only by filename convention in the feats dir —
// unlinked grants carry name/source without a note.
func resolveOriginFeats(grants []map[string]any, c *cfg) []OriginFeat {
	var out []OriginFeat
	for _, m := range grants {
		refs := make([]string, 0, len(m))
		for ref := range m {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		for _, ref := range refs {
			if granted, ok := m[ref].(bool); !ok || !granted {
				continue
			}
			name, variant, source, ok := parseOriginFeatRef(ref)
			if !ok {
				continue
			}
			of := OriginFeat{Name: titleCase(name), Variant: variant, Source: source}
			note := filepath.Join(c.featDir, noteFileName(of.Name, source))
			if fi, err := os.Stat(note); err == nil && !fi.IsDir() {
				of.Note = "./" + filepath.Base(note)
			}
			out = append(out, of)
		}
	}
	return out
}

func extractSpecies(rc *Race, c *cfg) (*Namespace, error) {
	if err := gateRecord("species", rc.Name, rc.Source, rc.Srd52, c); err != nil {
		return nil, err
	}
	sm := &SpeciesMeta{
		Name:      rc.Name,
		Source:    rc.Source,
		Page:      rc.Page,
		Size:      rc.Size,
		Languages: coerceBlockList(rc.LanguageProficiencies),
	}
	var speed int
	if err := json.Unmarshal(rc.Speed, &speed); err == nil && speed > 0 {
		sm.Speed = speed
	} else if len(rc.Speed) > 0 && sm.Speed == 0 {
		// Composite speed shapes ({walk: 20, fly: 50}) and zero speeds have
		// no scalar meaning in v0 — skipped, never guessed.
		fmt.Fprintf(os.Stderr, "warn: species %s: unmodeled speed shape %s; omitted\n", rc.Name, string(rc.Speed))
	}
	return &Namespace{
		SchemaVersion: schemaVersion,
		GeneratedFrom: "races.json",
		Species:       sm,
	}, nil
}

// prerequisiteDigest reduces one typed prerequisite block. Only the terms the
// corpus exercises are modeled (level, ability floors, feature names, the
// spellcasting flag); a block carrying any unmodeled key is skipped whole, so
// a partially-emitted requirement can never mean something it doesn't.
func prerequisiteDigest(block map[string]json.RawMessage) (FeatRequirement, bool) {
	var raw map[string]any
	if err := json.Unmarshal(collectRaw(block), &raw); err != nil {
		return FeatRequirement{}, false
	}
	var req FeatRequirement
	for k, v := range raw {
		switch k {
		case "level":
			f, ok := v.(float64)
			if !ok {
				return FeatRequirement{}, false
			}
			req.Level = int(f)
		case "ability":
			list, ok := v.([]any)
			if !ok {
				return FeatRequirement{}, false
			}
			req.Ability = map[string]int{}
			for _, e := range list {
				m, ok := e.(map[string]any)
				if !ok {
					return FeatRequirement{}, false
				}
				for score, min := range m {
					n, ok := min.(float64)
					if !ok {
						return FeatRequirement{}, false
					}
					req.Ability[score] = int(n)
				}
			}
			if len(req.Ability) == 0 {
				return FeatRequirement{}, false
			}
		case "feature":
			refs, ok := coerceStrs(v)
			if !ok {
				return FeatRequirement{}, false
			}
			req.Feature = refs
		case "spellcasting2020":
			flag, ok := v.(bool)
			if !ok || !flag {
				return FeatRequirement{}, false
			}
			req.Spellcasting = true
		default:
			return FeatRequirement{}, false
		}
	}
	if req.Level == 0 && req.Ability == nil && req.Feature == nil && !req.Spellcasting {
		return FeatRequirement{}, false
	}
	return req, true
}

// spellcasting2020 → spellcasting: the source's era marker for "you have the
// Spellcasting feature"; the namespace carries the requirement, not the era.

// collectRaw reassembles one prerequisite block's JSON for wholesale decoding.
func collectRaw(block map[string]json.RawMessage) []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for k, v := range block {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// coerceStrs coerces a JSON list of strings; anything else fails the digest.
func coerceStrs(v any) ([]string, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func extractFeat(f *Feat, c *cfg) (*Namespace, error) {
	if err := gateRecord("feat", f.Name, f.Source, f.Srd52, c); err != nil {
		return nil, err
	}
	fm := &FeatMeta{
		Name:     f.Name,
		Source:   f.Source,
		Page:     f.Page,
		Category: f.Category,
	}
	for _, block := range f.Prerequisite {
		if req, ok := prerequisiteDigest(block); ok {
			fm.Prerequisites = append(fm.Prerequisites, req)
		}
	}
	return &Namespace{
		SchemaVersion: schemaVersion,
		GeneratedFrom: "feats.json",
		Feat:          fm,
	}, nil
}

// ── spell / optional-feature extraction ──

// unmodeledKeys reports which of a block's keys no digest models — the
// whole-block-skip trigger for the same reason as feat prerequisites: a
// partially emitted block can never mean something it doesn't.
func unmodeledKeys(block map[string]json.RawMessage, known map[string]bool) []string {
	var out []string
	keys := make([]string, 0, len(block))
	for k := range block {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !known[k] {
			out = append(out, k)
		}
	}
	return out
}

// The known-key sets for the spell sub-blocks. Any other key skips the block
// whole (with a warn), materialized per digest below.
var (
	spellTimeKeys       = set("number", "unit", "condition", "note")
	spellRangeKeys      = set("type", "distance")
	spellRangeDistKeys  = set("type", "amount")
	spellDurationKeys   = set("type", "duration", "concentration", "ends")
	spellDurationSpanKs = set("type", "amount", "upTo")
	spellComponentKeys  = set("v", "s", "m")
	spellDamageKeys     = set("dice", "bonus", "damageType")
)

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func spellTimeDigests(blocks []map[string]json.RawMessage, name string) []SpellTime {
	var out []SpellTime
	for _, b := range blocks {
		if keys := unmodeledKeys(b, spellTimeKeys); len(keys) > 0 {
			fmt.Fprintf(os.Stderr, "warn: spell %s: unmodeled time key %s; block omitted\n", name, keys[0])
			continue
		}
		var t SpellTime
		if err := json.Unmarshal(collectRaw(b), &t); err != nil {
			fmt.Fprintf(os.Stderr, "warn: spell %s: unreadable time block (%v); block omitted\n", name, err)
			continue
		}
		out = append(out, t)
	}
	return out
}

func spellRangeDigest(block map[string]json.RawMessage, name string) *SpellRange {
	if keys := unmodeledKeys(block, spellRangeKeys); len(keys) > 0 {
		fmt.Fprintf(os.Stderr, "warn: spell %s: unmodeled range key %s; block omitted\n", name, keys[0])
		return nil
	}
	r := &SpellRange{}
	if err := json.Unmarshal(collectRaw(block), r); err != nil {
		fmt.Fprintf(os.Stderr, "warn: spell %s: unreadable range block (%v); block omitted\n", name, err)
		return nil
	}
	if r.Type == "" {
		return nil
	}
	if raw := block["distance"]; raw != nil {
		var dist map[string]any
		if err := json.Unmarshal(raw, &dist); err != nil {
			fmt.Fprintf(os.Stderr, "warn: spell %s: unreadable range-distance block (%v); distance omitted\n", name, err)
			r.Distance = nil
		} else if ok, d := spellDistanceDigest(dist, name); ok {
			r.Distance = d
		} else {
			r.Distance = nil
		}
	}
	return r
}

func spellDistanceDigest(raw map[string]any, name string) (bool, *SpellRangeDistance) {
	if raw == nil {
		return true, nil // no distance in the block (e.g. a self range)
	}
	for k := range raw {
		if !spellRangeDistKeys[k] {
			fmt.Fprintf(os.Stderr, "warn: spell %s: unmodeled range-distance key %s; distance omitted\n", name, k)
			return false, nil
		}
	}
	d := &SpellRangeDistance{}
	if s, ok := raw["type"].(string); ok {
		d.Type = s
	}
	if f, ok := raw["amount"].(float64); ok {
		d.Amount = int(f)
	}
	return true, d
}

func spellDurationDigests(blocks []map[string]json.RawMessage, name string) []SpellDuration {
	var out []SpellDuration
	for _, b := range blocks {
		if keys := unmodeledKeys(b, spellDurationKeys); len(keys) > 0 {
			fmt.Fprintf(os.Stderr, "warn: spell %s: unmodeled duration key %s; block omitted\n", name, keys[0])
			continue
		}
		var d SpellDuration
		if err := json.Unmarshal(collectRaw(b), &d); err != nil {
			fmt.Fprintf(os.Stderr, "warn: spell %s: unreadable duration block (%v); block omitted\n", name, err)
			continue
		}
		// The timed span rides as `duration`; unmodeled keys inside it skip
		// only the span, never the whole block.
		if b["duration"] != nil {
			var span map[string]any
			if err := json.Unmarshal(b["duration"], &span); err == nil {
				ok, s := spellDurationSpanDigest(span, name)
				if ok {
					d.Duration = s
				}
			}
		}
		out = append(out, d)
	}
	return out
}

func spellDurationSpanDigest(raw map[string]any, name string) (bool, *SpellDurationSpan) {
	for k := range raw {
		if !spellDurationSpanKs[k] {
			fmt.Fprintf(os.Stderr, "warn: spell %s: unmodeled duration-span key %s; span omitted\n", name, k)
			return false, nil
		}
	}
	s := &SpellDurationSpan{}
	if t, ok := raw["type"].(string); ok {
		s.Type = t
	}
	if f, ok := raw["amount"].(float64); ok {
		s.Amount = int(f)
	}
	return true, s
}

func spellComponentsDigest(block map[string]json.RawMessage, name string) *SpellComponents {
	if keys := unmodeledKeys(block, spellComponentKeys); len(keys) > 0 {
		fmt.Fprintf(os.Stderr, "warn: spell %s: unmodeled components key %s; block omitted\n", name, keys[0])
		return nil
	}
	c := &SpellComponents{}
	if raw := block["v"]; raw != nil {
		var v bool
		if err := json.Unmarshal(raw, &v); err == nil {
			c.V = v
		}
	}
	if raw := block["s"]; raw != nil {
		var s bool
		if err := json.Unmarshal(raw, &s); err == nil {
			c.S = s
		}
	}
	if raw := block["m"]; raw != nil {
		switch string(raw) {
		case "null":
			// m: null = no material component in the note; the key stays out.
		default:
			if mt, err := jsonUnmarshalString(raw); err == nil {
				// The plain form carries the material name verbatim.
				c.M = &SpellMaterial{Text: mt}
				break
			}
			var mat struct {
				Text    string `json:"text"`
				Cost    int    `json:"cost"`
				Consume bool   `json:"consume"`
			}
			if err := json.Unmarshal(raw, &mat); err != nil {
				fmt.Fprintf(os.Stderr, "warn: spell %s: unmodeled material shape; material omitted\n", name)
				break
			}
			m := &SpellMaterial{Text: mat.Text, Consume: mat.Consume}
			// The source keeps the cost in copper units (like every equipment
			// value elsewhere in the corpus); gp converts the same way.
			if mat.Cost > 0 {
				m.GP = mat.Cost / 100
			}
			c.M = m
		}
	}
	if !c.V && !c.S && c.M == nil {
		return nil
	}
	return c
}

// jsonUnmarshalString decodes a quoted JSON string; anything else fails.
func jsonUnmarshalString(raw json.RawMessage) (string, error) {
	var s string
	err := json.Unmarshal(raw, &s)
	return s, err
}

func spellDamageDigest(block map[string]json.RawMessage, name string) *SpellDamage {
	if block == nil {
		return nil
	}
	if keys := unmodeledKeys(block, spellDamageKeys); len(keys) > 0 {
		fmt.Fprintf(os.Stderr, "warn: spell %s: unmodeled damage key %s; damage omitted\n", name, keys[0])
		return nil
	}
	d := &SpellDamage{}
	if err := json.Unmarshal(collectRaw(block), d); err != nil {
		fmt.Fprintf(os.Stderr, "warn: spell %s: unreadable damage block (%v); damage omitted\n", name, err)
		return nil
	}
	if d.Dice == "" && d.DamageType == "" && d.Bonus == 0 {
		return nil // a null damage field carries nothing structural
	}
	return d
}

// material cost rides the source's typed {cost} in copper units; gp converts
// like the equipment values do elsewhere in the namespace (2500 cp → gp 25).

// spellJoins holds the whole gendata lookup, sectioned by lowercase source
// and keyed by lowercase spell name, parsed once per run.
type spellJoins map[string]map[string]*spellJoinSpell

// loadSpellJoins reads fixtures/data/generated/gendata-spell-source-lookup.json.
// A missing file leaves an empty index (extraction warns per spell whose
// class list then stays absent rather than guessing).
func loadSpellJoins(c *cfg) spellJoins {
	out := spellJoins{}
	raw, err := os.ReadFile(filepath.Join(c.dataDir, "generated", "gendata-spell-source-lookup.json"))
	if err != nil {
		return out
	}
	var sections map[string]map[string]*spellJoinSpell
	if err := json.Unmarshal(raw, &sections); err != nil {
		fmt.Fprintf(os.Stderr, "warn: unparseable spell source lookup (%v); class lists will stay absent\n", err)
		return out
	}
	return sections
}

// spellClassRefs reduces the join's class tables to sorted NameSourceRef list.
func spellClassRefs(join *spellJoinSpell) []NameSourceRef {
	var out []NameSourceRef
	for src, classes := range join.Class {
		for cl, v := range classes {
			// bool true and {definedInSources…} objects both name the class;
			// false/null stays out.
			switch t := v.(type) {
			case bool:
				if !t {
					continue
				}
			case map[string]any:
			default:
				continue
			}
			out = append(out, NameSourceRef{Name: cl, Source: src})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// spellSubclassRefs reduces the join's four-level nesting
// (classSource → class → subclassSource → shortName) to sorted refs; the
// note link resolves into the classes dir when that note exists.
func spellSubclassRefs(join *spellJoinSpell, c *cfg) []SpellSubclassRef {
	var out []SpellSubclassRef
	for classSrc, classes := range join.Subclass {
		for className, subs := range classes {
			for subSrc, shorts := range subs {
				for short, sub := range shorts {
					ref := SpellSubclassRef{
						Name:      sub.Name,
						ShortName: short,
						Source:    subSrc,
						Class:     NameSourceRef{Name: className, Source: classSrc},
					}
					note := filepath.Join(c.outDir, subdirName(className, classSrc, sub.Name, subSrc))
					if fi, err := os.Stat(note); err == nil && !fi.IsDir() {
						ref.Note = "../classes/" + filepath.Base(note)
					}
					out = append(out, ref)
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := &out[i], &out[j]
		for _, cmp := range [][2]string{
			{a.Class.Source, b.Class.Source}, {a.Class.Name, b.Class.Name},
			{a.Source, b.Source}, {a.ShortName, b.ShortName},
		} {
			if cmp[0] != cmp[1] {
				return cmp[0] < cmp[1]
			}
		}
		return false
	})
	return out
}

// spellHasSrd52 reads the flag through its three shapes (bool, null, and the
// renamed-entry string); a null (or missing) value is not flagged.
func spellHasSrd52(sp *Spell) bool {
	var b bool
	if err := json.Unmarshal(sp.Srd52, &b); err == nil {
		return b
	}
	var s string
	if err := json.Unmarshal(sp.Srd52, &s); err == nil {
		return s != ""
	}
	return false
}

// spellDisplayName resolves the record's published name: a string srd52 value
// is the renamed SRD 5.2 entry, and the note the data merges into carries that
// name (its aliases list it too). Plain records publish under Name.
func spellDisplayName(sp *Spell) string {
	var s string
	if err := json.Unmarshal(sp.Srd52, &s); err == nil && s != "" {
		return s
	}
	return sp.Name
}

// notePath resolves a compendium note for a record's published name: the
// shared <slug>-<src>.md convention first, then the apostrophe-stripped
// variant some notes were published under ("Dragon's Breath" →
// dragons-breath-xphb.md, "Devil's Sight" → devils-sight-xphb.md). The second
// lookup exists only for that spelling drift; the primary convention wins
// whenever its note exists, so existing kinds and all other slug() uses
// (feature slugs, subdir note names) stay byte-identical.
func notePath(dir, name, source string) (string, bool) {
	p := filepath.Join(dir, noteFileName(name, source))
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		return p, true
	}
	p = filepath.Join(dir, noteFileName(strings.ReplaceAll(name, "'", ""), source))
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		return p, true
	}
	return p, false
}

func extractSpell(sp *Spell, join *spellJoinSpell, c *cfg) (*Namespace, error) {
	if err := gateRecord("spell", sp.Name, sp.Source, spellHasSrd52(sp), c); err != nil {
		return nil, err
	}
	sm := &SpellMeta{
		Name:        spellDisplayName(sp),
		Source:      sp.Source,
		Page:        sp.Page,
		Level:       sp.Level,
		School:      sp.School,
		Time:        spellTimeDigests(sp.Time, sp.Name),
		Range:       spellRangeDigest(sp.Range, sp.Name),
		Duration:    spellDurationDigests(sp.Duration, sp.Name),
		Components:  spellComponentsDigest(sp.Components, sp.Name),
		SavingThrow: sp.SavingThrow,
		Damage:      spellDamageDigest(sp.Damage, sp.Name),
	}
	if join == nil {
		fmt.Fprintf(os.Stderr, "warn: spell %s: no class-list join entry; classes/subclasses stay absent\n", sp.Name)
	} else {
		sm.Classes = spellClassRefs(join)
		sm.Subclasses = spellSubclassRefs(join, c)
	}
	return &Namespace{
		SchemaVersion: schemaVersion,
		GeneratedFrom: "spells/spells-" + strings.ToLower(sp.Source) + ".json",
		Spell:         sm,
	}, nil
}

// OptionalFeature shape digests — same whole-block discipline, with the
// prereq keys the corpus exercises. `item` and `otherSummary` are prose;
// XPHB-era `spell` blocks carry a typed cantrip-filter object (not a string
// list), so those blocks are wholly skipped too — the feat namespace's
// boundary applied to prerequisites.
var (
	optFeatureReqKeys      = set("level", "pact", "spell", "optionalfeature")
	optFeatureLevelKeys    = set("level", "class", "subclass")
	optFeatureClassKeys    = set("name", "source", "visibleStats")
	optFeatureConsumesKeys = set("name", "amount", "amountMin", "amountMax")
)

// optionalFeatureConsumesDigest keeps the resource name and its typed amounts,
// coerced to ints; an unmodeled key drops the whole block with a warn.
func optionalFeatureConsumesDigest(block map[string]json.RawMessage, name string) map[string]any {
	if block == nil {
		return nil
	}
	if keys := unmodeledKeys(block, optFeatureConsumesKeys); len(keys) > 0 {
		fmt.Fprintf(os.Stderr, "warn: optional feature %s: unmodeled consumes key %s; block omitted\n", name, keys[0])
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(collectRaw(block), &raw); err != nil {
		fmt.Fprintf(os.Stderr, "warn: optional feature %s: unreadable consumes block (%v); block omitted\n", name, err)
		return nil
	}
	return coerceNumbers(raw).(map[string]any)
}

// optionalFeatureRequirementDigest reduces one prerequisite block. It returns
// false when the block is unmodeled (WHOLE block skipped, no partial meaning).
// The level block unwraps to the level integer plus the granting class
// ref (`visibleStats` is a UI hint and stays out) and optional subclass ref.
// optionalFeature refs resolve their display names from this file's records
// (falling back to the deterministic title-case), like origin feats.
func optionalFeatureRequirementDigest(block map[string]json.RawMessage, file *OptionalFeatureFile) (OptionalFeatureRequirement, bool) {
	if keys := unmodeledKeys(block, optFeatureReqKeys); len(keys) > 0 {
		return OptionalFeatureRequirement{}, false
	}
	var req OptionalFeatureRequirement
	if raw := block["level"]; raw != nil {
		var lv struct {
			Level    int            `json:"level"`
			Class    map[string]any `json:"class"`
			Subclass map[string]any `json:"subclass"`
		}
		if err := json.Unmarshal(raw, &lv); err != nil {
			return OptionalFeatureRequirement{}, false
		}
		var lvKeys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &lvKeys); err != nil {
			return OptionalFeatureRequirement{}, false
		}
		if unmodeled := unmodeledKeys(lvKeys, optFeatureLevelKeys); len(unmodeled) > 0 {
			return OptionalFeatureRequirement{}, false
		}
		req.Level = lv.Level
		if lv.Class != nil {
			if unmodeled := unmodeledKeys(mapKeys(lv.Class), optFeatureClassKeys); len(unmodeled) > 0 {
				return OptionalFeatureRequirement{}, false
			}
			req.Class = &NameSourceRef{Name: str(lv.Class["name"]), Source: str(lv.Class["source"])}
		}
		if lv.Subclass != nil {
			req.Subclass = &NameSourceRef{Name: str(lv.Subclass["name"]), Source: str(lv.Subclass["source"])}
		}
	}
	if raw := block["pact"]; raw != nil {
		if err := json.Unmarshal(raw, &req.Pact); err != nil {
			return OptionalFeatureRequirement{}, false
		}
	}
	if raw := block["spell"]; raw != nil {
		var spells []string
		if err := json.Unmarshal(raw, &spells); err != nil {
			// XPHB-era spell refs are {choose, entry, entrySummary} prose
			// filter objects, not string lists — unmodeled shape.
			return OptionalFeatureRequirement{}, false
		}
		req.Spell = spells
	}
	if raw := block["optionalfeature"]; raw != nil {
		var refs []string
		if err := json.Unmarshal(raw, &refs); err != nil {
			return OptionalFeatureRequirement{}, false
		}
		for _, ref := range refs {
			key, src, found := strings.Cut(ref, "|")
			if !found || key == "" {
				return OptionalFeatureRequirement{}, false
			}
			ofr := NameSourceRef{Name: titleCase(strings.TrimSpace(key)), Source: strings.ToUpper(src)}
			for i := range file.OptionalFeature {
				rec := &file.OptionalFeature[i]
				if strings.EqualFold(rec.Name, key) && strings.EqualFold(rec.Source, src) {
					ofr.Name = rec.Name
					break
				}
			}
			req.OptionalFeature = append(req.OptionalFeature, ofr)
		}
	}
	if req.Level == 0 && req.Class == nil && req.Subclass == nil && req.Pact == "" &&
		len(req.Spell) == 0 && len(req.OptionalFeature) == 0 {
		return OptionalFeatureRequirement{}, false
	}
	return req, true
}

func mapKeys(m map[string]any) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m))
	for k := range m {
		out[k] = nil
	}
	return out
}

func extractOptionalFeature(ofr *OptionalFeature, file *OptionalFeatureFile, c *cfg) (*Namespace, error) {
	if err := gateRecord("optional feature", ofr.Name, ofr.Source, ofr.Srd52, c); err != nil {
		return nil, err
	}
	fm := &OptionalFeatureMeta{
		Name:        ofr.Name,
		Source:      ofr.Source,
		Page:        ofr.Page,
		FeatureType: ofr.FeatureType,
		Consumes:    optionalFeatureConsumesDigest(ofr.Consumes, ofr.Name),
	}
	for _, block := range ofr.Prerequisite {
		if req, ok := optionalFeatureRequirementDigest(block, file); ok {
			fm.Prerequisites = append(fm.Prerequisites, req)
		}
	}
	return &Namespace{
		SchemaVersion:   schemaVersion,
		GeneratedFrom:   "optionalfeatures.json",
		OptionalFeature: fm,
	}, nil
}

// flatRecord is what the flat-file kinds share for record lookup.
type flatRecord interface {
	name() string
	source() string
}

// cutTarget splits the "Name@Source" target syntax shared by all kinds.
func cutTarget(kind, target string) (string, string, error) {
	name, source, ok := strings.Cut(target, "@")
	if !ok {
		return "", "", fmt.Errorf("-%s must be Name@Source, e.g. Soldier@XPHB", kind)
	}
	return name, source, nil
}

func (b Background) name() string   { return b.Name }
func (b Background) source() string { return b.Source }
func (r Race) name() string         { return r.Name }
func (r Race) source() string       { return r.Source }
func (f Feat) name() string         { return f.Name }
func (f Feat) source() string       { return f.Source }

// noteFileName is the compendium note convention for these flat-file kinds
// (also the class note): <slug>-<source>.md.
func noteFileName(name, source string) string {
	return slug(name) + "-" + strings.ToLower(source) + ".md"
}

// processFlatKind runs one flat-file target end to end: load the source file,
// find the record by name/source, extract (the srd52 gate lives inside
// extract*), and merge into the matching compendium note. An absent note is a
// data-only warn+skip, the same stance as missing subclass notes.
func processFlatKind[T flatRecord, F any](kind, target, dataFile, outDir string, records func(*F) []T, extract func(*T, *cfg) (*Namespace, error), c *cfg, dryRun, prt bool) error {
	name, source, err := cutTarget(kind, target)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(c.dataDir, dataFile))
	if err != nil {
		return fmt.Errorf("read %s: %w", dataFile, err)
	}
	var file F
	if err := json.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("parse %s: %w", dataFile, err)
	}
	recs := records(&file)
	var rec *T
	for i := range recs {
		if strings.EqualFold(recs[i].name(), name) && strings.EqualFold(recs[i].source(), source) {
			rec = &recs[i]
			break
		}
	}
	if rec == nil {
		return fmt.Errorf("%s %s@%s not found in %s", kind, name, source, dataFile)
	}
	ns, err := extract(rec, c)
	if err != nil {
		return err
	}
	notePath, ok := notePath(outDir, name, source)
	if !ok {
		fmt.Fprintf(os.Stderr, "no compendium note for %s@%s: %s (nothing to merge)\n", name, source, notePath)
		return nil
	}
	_, err = mergeFile(notePath, ns, dryRun, prt)
	return err
}

// bulkSpellSource is the spell source file the -spell-all bulk mode walks:
// the whole SRD 5.2 (srd52-gated) spell corpus, all of whose notes exist.
const bulkSpellSource = "XPHB"

// bulkCounts is the count summary the bulk mode prints at the end.
type bulkCounts struct {
	updated  int
	upToDate int
	gated    int
	noNote   int
}

// loadSpellSource reads one spell source flat file and returns its records
// sorted by name (deterministic output order, joins resolved per record).
func loadSpellSource(c *cfg, source string) (SpellFile, error) {
	dataFile := "spells/spells-" + strings.ToLower(source) + ".json"
	raw, err := os.ReadFile(filepath.Join(c.dataDir, dataFile))
	if err != nil {
		return SpellFile{}, fmt.Errorf("read %s: %w", dataFile, err)
	}
	var file SpellFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return SpellFile{}, fmt.Errorf("parse %s: %w", dataFile, err)
	}
	sort.SliceStable(file.Spell, func(i, j int) bool {
		return file.Spell[i].Name < file.Spell[j].Name
	})
	return file, nil
}

// processSpell is the one-spell-per-flag mode: `-spell "Fireball@XPHB"`.
func processSpell(c *cfg, target string, dryRun, prt bool) error {
	name, source, err := cutTarget("spell", target)
	if err != nil {
		return err
	}
	file, err := loadSpellSource(c, source)
	if err != nil {
		return err
	}
	var rec *Spell
	for i := range file.Spell {
		if strings.EqualFold(file.Spell[i].Name, name) && strings.EqualFold(file.Spell[i].Source, source) {
			rec = &file.Spell[i]
			break
		}
	}
	if rec == nil {
		return fmt.Errorf("spell %s@%s not found in spells/spells-%s.json", name, source, strings.ToLower(source))
	}
	ns, err := extractSpell(rec, loadSpellJoins(c)[strings.ToLower(source)][strings.ToLower(name)], c)
	if err != nil {
		return err
	}
	publishName := spellDisplayName(rec)
	notePath, ok := notePath(c.spellDir, publishName, source)
	if !ok {
		fmt.Fprintf(os.Stderr, "no compendium note for spell %s@%s: %s (nothing to merge)\n", name, source, notePath)
		return nil
	}
	_, err = mergeFile(notePath, ns, dryRun, prt)
	return err
}

// processSpellsBulk is the `-spell-all` mode: walk the whole gated corpus in
// one pass — the spell JSON and the class-list join are parsed once, each note
// is re-read once per pass — and print a count summary. A second identical
// pass must report all up-to-date (idempotence is the shared merge's job, not
// the bulk loop's).
func processSpellsBulk(c *cfg, dryRun, prt bool) (bulkCounts, error) {
	counts := bulkCounts{}
	file, err := loadSpellSource(c, bulkSpellSource)
	if err != nil {
		return counts, err
	}
	joins := loadSpellJoins(c)
	section := joins[strings.ToLower(bulkSpellSource)]
	for i := range file.Spell {
		sp := &file.Spell[i]
		if c.srOnly && !spellHasSrd52(sp) {
			counts.gated++
			continue
		}
		ns, err := extractSpell(sp, section[strings.ToLower(sp.Name)], c)
		if err != nil {
			return counts, err
		}
		notePath, ok := notePath(c.spellDir, spellDisplayName(sp), sp.Source)
		if !ok {
			counts.noNote++
			fmt.Fprintf(os.Stderr, "no compendium note for spell %s@%s (skipped)\n", sp.Name, sp.Source)
			continue
		}
		changed, err := mergeFile(notePath, ns, dryRun, prt)
		if err != nil {
			return counts, err
		}
		if changed {
			counts.updated++
		} else {
			counts.upToDate++
		}
	}
	return counts, nil
}

// processOptionalFeature is the one-target mode: `-optfeature "Subtle
// Spell@XPHB"`. Same flat-file shape as background/race/feat, but the
// extraction also needs the whole file (prerequisite optional-feature refs
// resolve their display names from the records).
func processOptionalFeature(c *cfg, target string, dryRun, prt bool) error {
	name, source, err := cutTarget("optfeature", target)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(c.dataDir, "optionalfeatures.json"))
	if err != nil {
		return fmt.Errorf("read optionalfeatures.json: %w", err)
	}
	var file OptionalFeatureFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("parse optionalfeatures.json: %w", err)
	}
	var rec *OptionalFeature
	for i := range file.OptionalFeature {
		if strings.EqualFold(file.OptionalFeature[i].Name, name) && strings.EqualFold(file.OptionalFeature[i].Source, source) {
			rec = &file.OptionalFeature[i]
			break
		}
	}
	if rec == nil {
		return fmt.Errorf("optional feature %s@%s not found in optionalfeatures.json", name, source)
	}
	ns, err := extractOptionalFeature(rec, &file, c)
	if err != nil {
		return err
	}
	notePath, ok := notePath(c.optDir, name, source)
	if !ok {
		fmt.Fprintf(os.Stderr, "no compendium note for optional feature %s@%s: %s (nothing to merge)\n", name, source, notePath)
		return nil
	}
	_, err = mergeFile(notePath, ns, dryRun, prt)
	return err
}

// root makes the encoder emit the whole namespace indented under one
// `semiplane:` key, keeping field order deterministic.
type root struct {
	Semiplane *Namespace `yaml:"semiplane"`
}

// merge regenerates only the `semiplane:` namespace inside the frontmatter.
// Everything else — including all other frontmatter keys and the body — is
// carried through byte-for-byte. Returns the merged document.
func merge(md []byte, ns *Namespace) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root{Semiplane: ns}); err != nil {
		return nil, fmt.Errorf("encode semiplane namespace: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	block := strings.TrimSuffix(buf.String(), "\n")

	lines := strings.Split(string(md), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return nil, fmt.Errorf("no frontmatter fence at start of file")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return nil, fmt.Errorf("unterminated frontmatter")
	}

	// Drop any previous semiplane: block. The regenerated block is always
	// appended at the end of the frontmatter, so everything from its marker
	// (or any stray legacy `semiplane:`/machine keys) to the close fence goes.
	kept := []string{lines[0]}
	for i := 1; i < end; i++ {
		if strings.TrimSpace(lines[i]) == "semiplane:" {
			break
		}
		kept = append(kept, lines[i])
	}

	fm := append(kept, strings.Split(strings.TrimRight(block, "\n"), "\n")...)
	out := append(fm, lines[end:]...)
	joined := strings.Join(out, "\n")

	// Guard the invariant: the merged frontmatter must still be valid YAML and
	// must contain the regenerated namespace. Nothing is written otherwise.
	var fmWithFence []string
	fmWithFence = append(fmWithFence, fm...)
	fmWithFence = append(fmWithFence, lines[end])
	if err := validateFrontmatter(fmWithFence); err != nil {
		return nil, err
	}
	return []byte(joined), nil
}

// validateFrontmatter parses the given frontmatter block (open fence + keys +
// close fence) as YAML and checks that the semiplane namespace survived.
func validateFrontmatter(fmLines []string) error {
	var meta map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(fmLines, "\n")), &meta); err != nil {
		return fmt.Errorf("merged frontmatter is not valid YAML: %w", err)
	}
	if _, ok := meta["semiplane"].(map[string]any); !ok {
		return fmt.Errorf("merged frontmatter lost the semiplane: mapping")
	}
	return nil
}

func main() {
	class := flag.String("class", "Barbarian@XPHB", "class to process, as \"Name@Source\"")
	dataDir := flag.String("data", "fixtures/data", "root of the typed source JSON")
	outDir := flag.String("out", "fixtures/compendium/classes", "compendium classes dir; also where note links resolve")
	bgOut := flag.String("bg-out", "fixtures/compendium/backgrounds", "compendium backgrounds dir")
	raceOut := flag.String("race-out", "fixtures/compendium/races", "compendium races dir")
	featOut := flag.String("feat-out", "fixtures/compendium/feats", "compendium feats dir")
	spellOut := flag.String("spell-out", "fixtures/compendium/spells", "compendium spells dir")
	optOut := flag.String("optfeature-out", "fixtures/compendium/optional-features", "compendium optional-features dir")
	background := flag.String("background", "", `background to process, as "Name@Source"; empty to skip`)
	race := flag.String("race", "", `species note to process, as "Name@Source"; empty to skip`)
	feat := flag.String("feat", "", `feat to process, as "Name@Source"; empty to skip`)
	spell := flag.String("spell", "", `spell to process, as "Name@Source"; empty to skip`)
	spellAll := flag.Bool("spell-all", false, `bulk: process every record in `+"`spells/spells-xphb.json`"+` (count summary at the end)`)
	optfeature := flag.String("optfeature", "", `optional feature to process, as "Name@Source"; empty to skip`)
	dryRun := flag.Bool("dry-run", false, "do not write; just report what would change")
	print := flag.Bool("print", false, "print the generated YAML namespace to stdout")
	srd := flag.Bool("srd", true, "only keep content flagged srd52 (license gate)")
	flag.Parse()

	c := &cfg{srOnly: *srd, outDir: *outDir, featDir: *featOut, dataDir: *dataDir,
		spellDir: *spellOut, optDir: *optOut}

	// The class kind keeps its historical default (Bare runs regenerate the
	// Barbarian corpus); an explicit -class "" skips it so a run can target
	// only the flat-file kinds below.
	if *class != "" {
		name, source, ok := strings.Cut(*class, "@")
		if !ok {
			fmt.Fprintf(os.Stderr, "-class must be Name@Source, e.g. Barbarian@XPHB\n")
			os.Exit(2)
		}

		raw, err := os.ReadFile(filepath.Join(*dataDir, "class", fmt.Sprintf("class-%s.json", strings.ToLower(name))))
		if err != nil {
			fmt.Fprintf(os.Stderr, "read class JSON: %v\n", err)
			os.Exit(1)
		}
		var sf SourceFile
		if err := json.Unmarshal(raw, &sf); err != nil {
			fmt.Fprintf(os.Stderr, "parse class JSON: %v\n", err)
			os.Exit(1)
		}

		fi := -1
		for i := range sf.Class {
			if strings.EqualFold(sf.Class[i].Name, name) && strings.EqualFold(sf.Class[i].Source, source) {
				fi = i
				break
			}
		}
		if fi == -1 {
			fmt.Fprintf(os.Stderr, "class %s@%s not found in %s\n", name, source, *dataDir)
			os.Exit(1)
		}
		cls := &sf.Class[fi]
		if c.srOnly && !cls.Srd52 {
			fmt.Fprintf(os.Stderr, "class %s@%s is not flagged srd52; rerun with -srd=false to include it\n", name, source)
			os.Exit(1)
		}
		classMD := filepath.Join(*outDir, fmt.Sprintf("%s-%s.md", slug(cls.Name), strings.ToLower(cls.Source)))

		// Subclass notes sit next to the class note as
		// <class>-<classSrc>-<subclass slug>-<subSrc>.md; merge every one found.
		for i := range sf.Subclass {
			sc := &sf.Subclass[i]
			if sc.ClassName != cls.Name || sc.ClassSource != cls.Source {
				continue
			}
			notePath := filepath.Join(*outDir, subdirName(sc.ClassName, sc.ClassSource, sc.Name, sc.Source))
			if _, err := os.Stat(notePath); err != nil {
				// No compendium note for this subclass — data-only, nothing to merge.
				continue
			}
			if _, err := mergeFile(notePath, extractSubclass(&sf, sc, c), *dryRun, *print); err != nil {
				fmt.Fprintf(os.Stderr, "subclass %s: %v\n", sc.Name, err)
				os.Exit(1)
			}
		}

		if _, err := os.Stat(classMD); err != nil {
			fmt.Fprintf(os.Stderr, "compendium class note missing: %s\n", classMD)
			os.Exit(1)
		}
		if _, err := mergeFile(classMD, extractClass(&sf, cls, c), *dryRun, *print); err != nil {
			fmt.Fprintf(os.Stderr, "merge class: %v\n", err)
			os.Exit(1)
		}
	}

	// The flat-file kinds run independently of the class kind; each flag is
	// one Name@Source target, several kinds may be processed in one run, and
	// the srd52 gate is enforced inside their extract* (same default, same
	// -srd=false escape hatch).
	if *background != "" {
		if err := processFlatKind[Background, BackgroundFile]("background", *background, "backgrounds.json", *bgOut,
			func(f *BackgroundFile) []Background { return f.Background }, extractBackground, c, *dryRun, *print); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}
	if *race != "" {
		if err := processFlatKind[Race, RaceFile]("race", *race, "races.json", *raceOut,
			func(f *RaceFile) []Race { return f.Race }, extractSpecies, c, *dryRun, *print); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}
	if *feat != "" {
		if err := processFlatKind[Feat, FeatFile]("feat", *feat, "feats.json", *featOut,
			func(f *FeatFile) []Feat { return f.Feat }, extractFeat, c, *dryRun, *print); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}
	if *spell != "" {
		if err := processSpell(c, *spell, *dryRun, *print); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}
	if *spellAll {
		// Bulk runs set the SRD gate only on records (class lists are name
		// pass-through); a second identical pass must report all up-to-date.
		counts, err := processSpellsBulk(c, *dryRun, *print)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		fmt.Printf("bulk spells (%s@%s gate=%v): %d updated, %d up-to-date, %d not flagged srd52, %d without a note\n",
			"all", "spells/spells-"+strings.ToLower(bulkSpellSource)+".json", c.srOnly,
			counts.updated, counts.upToDate, counts.gated, counts.noNote)
	}
	if *optfeature != "" {
		if err := processOptionalFeature(c, *optfeature, *dryRun, *print); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}
}

// mergeFile applies the namespace to one compendium note and rewrites it, or
// only reports when dryRun/print are set.
func mergeFile(path string, ns *Namespace, dryRun, print bool) (bool, error) {
	md, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	out, err := merge(md, ns)
	if err != nil {
		return false, err
	}
	if print || dryRun {
		fmt.Printf("--- regenerated namespace for %s ---\n%s\n", path, sectionYAML(out))
	}
	if bytes.Equal(md, out) {
		fmt.Printf("up-to-date: %s\n", path)
		return false, nil
	}
	if dryRun {
		fmt.Printf("would update: %s\n", path)
		return true, nil
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return false, err
	}
	fmt.Printf("updated: %s\n", path)
	return true, nil
}

// sectionYAML extracts the semiplane block from a merged doc for printing.
func sectionYAML(md []byte) string {
	lines := strings.Split(string(md), "\n")
	var out []string
	in := false
	for _, l := range lines {
		if l == "semiplane:" {
			in = true
		}
		if in {
			if l == "---" {
				break
			}
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
