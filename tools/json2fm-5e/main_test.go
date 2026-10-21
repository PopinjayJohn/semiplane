package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleMD = `---
obsidianUIMode: preview
cssclasses:
- json5e-class
tags:
- class/barbarian
aliases:
- "Barbarian"
---
# Barbarian

Some prose with special characters and — em dashes.

## Hit Points

- **Hit Dice**: 1d12 per Barbarian level
`

func mustBuild(t *testing.T, ns *Namespace) string {
	t.Helper()
	out, err := merge([]byte(sampleMD), ns)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	return string(out)
}

func TestMergeAddsNamespaceAndKeepsProse(t *testing.T) {
	out := mustBuild(t, &Namespace{GeneratedFrom: "x", Class: &ClassMeta{Name: "Barbarian"}})

	if !strings.Contains(out, "# Barbarian\n") || !strings.Contains(out, "em dashes.") {
		t.Fatal("prose was not preserved:\n" + out)
	}
	if !strings.Contains(out, "obsidianUIMode: preview") {
		t.Fatal("original frontmatter was not preserved")
	}
	if !strings.Contains(out, "semiplane:") {
		t.Fatal("semiplane namespace missing")
	}
}

func TestMergeIsIdempotent(t *testing.T) {
	ns := &Namespace{GeneratedFrom: "x", Class: &ClassMeta{Name: "Barbarian", Source: "XPHB"}}
	once := mustBuild(t, ns)
	twice, err := merge([]byte(once), ns)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if string(twice) != once {
		t.Fatalf("merge is not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestMergeReplacesPreviousNamespace(t *testing.T) {
	v1 := mustBuild(t, &Namespace{GeneratedFrom: "old", Class: &ClassMeta{Name: "Barbarian"}})
	out, err := merge([]byte(v1), &Namespace{GeneratedFrom: "new", Subclass: &SubclassMeta{Name: "Berserker"}})
	if err != nil {
		t.Fatalf("merge over previous namespace: %v", err)
	}
	if strings.Contains(string(out), "old") {
		t.Fatal("previous namespace leaked through:\n" + string(out))
	}
	if !strings.Contains(string(out), "generatedFrom: new") {
		t.Fatal("new namespace missing:\n" + string(out))
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Path of the Berserker":     "path-of-the-berserker",
		"Ability Score Improvement": "ability-score-improvement",
		"Explorer's Pack":           "explorer-s-pack",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// Table cells arrive in several scalar shapes: numeric columns can be bare
// JSON numbers (.decode to float64), numeric strings, or booleans. Every one
// of them must become a resource key under its (slugified, tag-unwrapped)
// column label — the wizard's cantrips/prepared-spells table is bare ints.
func TestTableCellsCoerceToScalars(t *testing.T) {
	g := TableGroup{
		ColLabels: []string{"Cantrips", "Rage Damage", "Mystic Focus", "Level Cap"},
		Rows: []TableRow{
			{3, map[string]any{"type": "bonus", "value": float64(2)}, true, "4"},
			{4, map[string]any{"type": "bonus", "value": float64(0)}, false, "5"},
		},
	}
	cls := &Class{Name: "Wizard", Source: "XPHB", ClassTableGroups: []TableGroup{g}}
	ns := extractClass(&SourceFile{}, cls, &cfg{})
	p := ns.Class.Progression
	if len(p) != 2 {
		t.Fatalf("want 2 progression rows, got %d", len(p))
	}
	l1 := p[0].Resources
	if l1["cantrips"] != 3 {
		t.Errorf("bare int cell = %v (%T), want int 3", l1["cantrips"], l1["cantrips"])
	}
	if l1["rage-damage"] != 2 {
		t.Errorf("bonus cell = %v (%T), want 2", l1["rage-damage"], l1["rage-damage"])
	}
	if l1["mystic-focus"] != true || p[1].Resources["mystic-focus"] != false {
		t.Errorf("bool cells = %v / %v, want true / false", l1["mystic-focus"], p[1].Resources["mystic-focus"])
	}
	if l1["level-cap"] != 4 {
		t.Errorf("numeric string cell = %v (%T), want int 4", l1["level-cap"], l1["level-cap"])
	}
}

// The spell-slot progression table lives in a second group using the
// rowsSpellProgression key; its columns must reach the same inlined per-level
// resources, keyed by the readable name inside "{@filter …}" tags.
func TestSpellProgressionTableGroupInlines(t *testing.T) {
	mk := func(level string) string {
		return "{@filter " + level + "|spells|level=1|class=Wizard}"
	}
	cls := &Class{
		Name: "Wizard", Source: "XPHB",
		ClassTableGroups: []TableGroup{
			{
				ColLabels: []string{"{@filter Cantrips|spells|level=0|class=Wizard}"},
				Rows:      []TableRow{{3}, {3}},
			},
			{
				ColLabels: []string{mk("1st"), mk("2nd")},
				RowsSpellProgression: []TableRow{
					{float64(2), float64(0)},
					{float64(3), float64(2)},
				},
			},
		},
	}
	ns := extractClass(&SourceFile{}, cls, &cfg{})
	l1 := ns.Class.Progression[0].Resources
	if l1["cantrips"] != 3 {
		t.Errorf("group-0 cantrips missing/lost type: %v", l1["cantrips"])
	}
	if l1["1st"] != 2 || l1["2nd"] != 0 {
		t.Errorf("rowsSpellProgression cells missing: %v", l1)
	}
	if l2 := ns.Class.Progression[1].Resources; l2["1st"] != 3 || l2["2nd"] != 2 {
		t.Errorf("level-2 spell slots missing: %v", l2)
	}
}

// featProgression ([{name, category, progression}] keyed by string levels)
// becomes a compact side-table of level → name/category.
func TestFeatProgressionSideTable(t *testing.T) {
	cls := &Class{Name: "Fighter", Source: "XPHB"}
	cls.FeatProgression = append(cls.FeatProgression, struct {
		Name        string         `json:"name"`
		Category    []string       `json:"category"`
		Progression map[string]int `json:"progression"`
	}{
		Name: "Epic Boon", Category: []string{"EB"},
		Progression: map[string]int{"19": 1},
	})
	ns := extractClass(&SourceFile{}, cls, &cfg{})
	feats := ns.Class.FeatProgression
	if len(feats) != 1 || feats[0].Level != 19 || feats[0].Name != "Epic Boon" ||
		len(feats[0].Category) != 1 || feats[0].Category[0] != "EB" {
		t.Fatalf("featProgression side-table wrong: %+v", feats)
	}
}

// The level-N banner of a subclass (e.g. "Path of the Berserker" at level 3)
// carries its real features through refSubclassFeature entries; resolution
// must follow those refs structurally instead of exposing the banner name.
func TestResolveFollowsRefSubclassFeature(t *testing.T) {
	features := []SubclassFeature{
		{
			Name: "Path of the Berserker", Source: "XPHB",
			ClassName: "Barbarian", ClassSource: "XPHB",
			SubclassShortName: "Berserker", SubclassSource: "XPHB", Level: 3,
			Srd52: true,
			Entries: []any{
				"banner prose",
				map[string]any{"type": "refSubclassFeature", "subclassFeature": "Frenzy|Barbarian|XPHB|Berserker|XPHB|3"},
			},
		},
		{
			Name: "Frenzy", Source: "XPHB",
			ClassName: "Barbarian", ClassSource: "XPHB",
			SubclassShortName: "Berserker", SubclassSource: "XPHB", Level: 3,
			Srd52: true,
		},
	}
	sf := &SourceFile{
		Subclass: []Subclass{{
			Name: "Path of the Berserker", ShortName: "Berserker", Source: "XPHB",
			ClassName: "Barbarian", ClassSource: "XPHB",
			SubclassFeatures: []string{"Path of the Berserker|Barbarian|XPHB|Berserker|XPHB|3"},
		}},
		SubclassFeature: features,
	}
	got := resolveSubclassFeatureNames(sf, &sf.Subclass[0], &cfg{})
	if len(got) != 1 {
		t.Fatalf("want 1 resolved feature, got %d: %+v", len(got), got)
	}
	if got[0].Name != "Frenzy" || got[0].Level != 3 || got[0].Slug != "frenzy" {
		t.Fatalf("resolution did not follow refSubclassFeature: %+v", got[0])
	}
}

// Repeated slugs are one feature slot with several entries: the first
// occurrence is the value, later ones extend it and anchor back to it.
// Features that appear once carry no discriminator at all.
func TestAnnotateRecurrence(t *testing.T) {
	mk := func(name string, level int) FeatureRef {
		return FeatureRef{Name: name, Slug: slug(name), Level: level}
	}
	features := []FeatureRef{
		mk("Rage", 1),
		mk("Ability Score Improvement", 4),
		mk("Ability Score Improvement", 8),
		mk("Improved Brutal Strike", 13),
		mk("Ability Score Improvement", 12),
		mk("Improved Brutal Strike", 17),
	}
	annotateRecurrence(features)

	once := features[0]
	if once.Op != "" || once.RefSlug != "" {
		t.Errorf("unique feature %s got discriminator %q/%q", once.Name, once.Op, once.RefSlug)
	}
	first := features[1] // Ability Score Improvement @4
	if first.Op != "value" || first.RefSlug != "" {
		t.Errorf("slot introduction wrong: %+v", first)
	}
	for _, later := range []FeatureRef{features[2], features[4]} { // @8, @12
		if later.Op != "extends" || later.RefSlug != "ability-score-improvement" {
			t.Errorf("repeat entry wrong: %+v", later)
		}
	}
	ibs13 := features[3]
	if ibs13.Op != "value" {
		t.Errorf("second slot's first entry (Improved Brutal Strike @13) wrong: %+v", ibs13)
	}
	ibs17 := features[5]
	if ibs17.Op != "extends" || ibs17.RefSlug != "improved-brutal-strike" {
		t.Errorf("extends entry wrong: %+v", ibs17)
	}
}

// features[].choices come from {type: options} blocks in the typed JSON —
// never from prose. Ref-style options resolve by their first ref segment
// (with per-option name overrides winning), inline named entries by name,
// and an absent count means 1.
func TestFeatureChoicesFromOptionsBlocks(t *testing.T) {
	rec := &ClassFeature{
		Name: "Divine Order", Source: "XPHB",
		ClassName: "Cleric", ClassSource: "XPHB", Level: 1, Srd52: true,
		Entries: []any{
			"You gain one of the following features of your choice.",
			map[string]any{
				"type":  "options",
				"count": float64(1),
				"entries": []any{
					map[string]any{"type": "refClassFeature", "classFeature": "Protector|Cleric|XPHB|1|XPHB"},
					map[string]any{"type": "refClassFeature", "classFeature": "Thaumaturge|Cleric|XPHB|1|XPHB"},
				},
			},
			map[string]any{ // nested inside an entries block, with an override name
				"type": "entries",
				"entries": []any{
					map[string]any{
						"type": "options",
						"entries": []any{
							map[string]any{"type": "refOptionalfeature", "optionalfeature": "Careful Spell|XPHB", "name": "Careful Spell (unmarked)"},
							map[string]any{"type": "entries", "name": "Ethereal Sight", "entries": []any{"prose"}},
						},
					},
				},
			},
		},
	}
	got := featureChoices(rec)
	if len(got) != 2 {
		t.Fatalf("want 2 choices, got %d: %+v", len(got), got)
	}
	if got[0].Count != 1 || len(got[0].From) != 2 || got[0].From[0] != "Protector" || got[0].From[1] != "Thaumaturge" {
		t.Errorf("ref-style options wrong: %+v", got[0])
	}
	if got[1].Count != 1 { // no explicit count → 1
		t.Errorf("default count wrong: %+v", got[1])
	}
	if got[1].From[0] != "Careful Spell (unmarked)" || got[1].From[1] != "Ethereal Sight" {
		t.Errorf("name override / inline entry wrong: %+v", got[1])
	}
}

// A feature whose choice exists only as prose must ship no choices payload.
func TestFeatureChoicesAbsentWithoutOptionsBlock(t *testing.T) {
	rec := &ClassFeature{
		Name: "Primal Knowledge", Source: "XPHB",
		ClassName: "Barbarian", ClassSource: "XPHB", Level: 3, Srd52: true,
		Entries: []any{
			"You gain proficiency in another skill of your choice from the skill list available to Barbarians.",
			map[string]any{"type": "entries", "entries": []any{"prose"}},
		},
	}
	if got := featureChoices(rec); len(got) != 0 {
		t.Fatalf("prose must not become a choices payload: %+v", got)
	}
}

// extractClass must thread both new fields through the feature list: choices
// from the resolved record, recurrence across the ordered refs.
func TestExtractClassFeaturesCarryChoicesAndOps(t *testing.T) {
	sf := &SourceFile{
		ClassFeature: []ClassFeature{
			{Name: "Divine Order", Source: "XPHB", ClassName: "Cleric", ClassSource: "XPHB", Level: 1, Srd52: true,
				Entries: []any{
					map[string]any{"type": "options", "count": float64(1), "entries": []any{
						map[string]any{"type": "refClassFeature", "classFeature": "Protector|Cleric|XPHB|1"},
					}},
				}},
			{Name: "Ability Score Improvement", Source: "XPHB", ClassName: "Cleric", ClassSource: "XPHB", Level: 4, Srd52: true},
			{Name: "Ability Score Improvement", Source: "XPHB", ClassName: "Cleric", ClassSource: "XPHB", Level: 8, Srd52: true},
		},
	}
	cls := &Class{Name: "Cleric", Source: "XPHB",
		ClassFeatures: []json.RawMessage{
			json.RawMessage(`"Divine Order|Cleric|XPHB|1"`),
			json.RawMessage(`"Ability Score Improvement|Cleric|XPHB|4"`),
			json.RawMessage(`"Ability Score Improvement|Cleric|XPHB|8"`),
		},
	}
	ns := extractClass(sf, cls, &cfg{srOnly: true})
	feats := ns.Class.Features
	if len(feats) != 3 {
		t.Fatalf("want 3 features, got %d: %+v", len(feats), feats)
	}
	if len(feats[0].Choices) != 1 || feats[0].Choices[0].From[0] != "Protector" {
		t.Errorf("choices not threaded through extractClass: %+v", feats[0])
	}
	if feats[1].Op != "value" || feats[2].Op != "extends" || feats[2].RefSlug != "ability-score-improvement" {
		t.Errorf("ops not threaded through extractClass: %+v %+v", feats[1], feats[2])
	}
}

// spellcastingAbility rides from the source's top-level key to the class
// level; non-caster records omit it entirely rather than emitting an empty key.
func TestSpellcastingAbilityExtractedAndOmitted(t *testing.T) {
	cls := &Class{Name: "Wizard", Source: "XPHB", SpellcastingAbility: "int"}
	ns := extractClass(&SourceFile{}, cls, &cfg{})
	if ns.Class.SpellcastingAbility != "int" {
		t.Fatalf("spellcastingAbility not extracted: %+v", ns.Class)
	}
	out := mustBuild(t, ns)
	if !strings.Contains(out, "spellcastingAbility: int") {
		t.Fatal("spellcastingAbility missing from merged namespace:\n" + out)
	}

	barbarian := &Class{Name: "Barbarian", Source: "XPHB"}
	bns := extractClass(&SourceFile{}, barbarian, &cfg{})
	if bns.Class.SpellcastingAbility != "" {
		t.Fatalf("Barbarian must carry no spellcastingAbility: %+v", bns.Class)
	}
	bout := mustBuild(t, bns)
	if strings.Contains(bout, "spellcastingAbility") {
		t.Fatal("absent spellcastingAbility must not be emitted:\n" + bout)
	}
}

// The multiclass data sits together: multiclassProficiencies keeps its key
// and shape, and the ability-score gate is a sibling multiclass block with
// int-coerced floors. A record without a gate emits no multiclass block.
func TestMulticlassRequirementsGate(t *testing.T) {
	cls := &Class{Name: "Monk", Source: "PHB"}
	cls.Multiclassing.Requirements = map[string]any{
		"dex": float64(13),
		"wis": float64(13),
		// A non-numeric row carries no floor; it is dropped, not guessed.
		"feature-chores": "prose",
	}
	ns := extractClass(&SourceFile{}, cls, &cfg{})
	out := mustBuild(t, ns)
	if !strings.Contains(out, "multiclass:\n") || !strings.Contains(out, "dex: 13") || !strings.Contains(out, "wis: 13") {
		t.Fatal("multiclass requirements gate missing from merged namespace:\n" + out)
	}
	if strings.Contains(out, "feature-chores") {
		t.Fatal("non-numeric requirement row leaked through:\n" + out)
	}

	barbarian := &Class{Name: "Barbarian", Source: "XPHB"}
	barbarian.Multiclassing.ProficienciesGained = map[string]any{"armor": []any{"shield"}}
	bns := extractClass(&SourceFile{}, barbarian, &cfg{})
	if bns.Class.MulticlassGate != nil {
		t.Fatalf("gate must stay nil without requirements: %+v", bns.Class.MulticlassGate)
	}
	bout := mustBuild(t, bns)
	if !strings.Contains(bout, "multiclassProficiencies:") {
		t.Fatal("multiclassProficiencies copy was dropped:\n" + bout)
	}
	if strings.Contains(bout, "requirements:") {
		t.Fatal("absent requirements must not be emitted:\n" + bout)
	}
}

// schemaVersion under semiplane: pins the namespace shape so consumers can
// tell schema generations apart; both extractors stamp the current one.
func TestSchemaVersionStamped(t *testing.T) {
	cns := extractClass(&SourceFile{}, &Class{Name: "Wizard", Source: "XPHB"}, &cfg{})
	if cns.SchemaVersion != schemaVersion || schemaVersion < 1 {
		t.Fatalf("extractClass did not stamp schema version %d: got %d", schemaVersion, cns.SchemaVersion)
	}
	sns := extractSubclass(&SourceFile{}, &Subclass{
		Name: "Evoker", Source: "XPHB", ClassName: "Wizard", ClassSource: "XPHB",
	}, &cfg{})
	if sns.SchemaVersion != schemaVersion {
		t.Fatalf("extractSubclass did not stamp schema version %d: got %d", schemaVersion, sns.SchemaVersion)
	}
	out := mustBuild(t, cns)
	if !strings.Contains(out, "semiplane:\n  schemaVersion: 1") {
		t.Fatal("schemaVersion is not the first key directly under semiplane:\n" + strings.Join(strings.Split(out, "\n")[:6], "\n"))
	}
}

// srd52 provenance still gates everything: a record without it must be
// excluded when srOnly is on, and included when the gate is lifted.
func TestSrdGateFiltersRecords(t *testing.T) {
	mk := func(srd bool) *SourceFile {
		return &SourceFile{
			SubclassFeature: []SubclassFeature{
				{
					Name: "Homebrew Sniffle", Source: "HB",
					ClassName: "Barbarian", ClassSource: "XPHB",
					SubclassShortName: "Berserker", SubclassSource: "XPHB", Level: 6,
					Srd52: srd,
				},
			},
			Subclass: []Subclass{{
				Name: "Path of the Berserker", ShortName: "Berserker", Source: "XPHB",
				ClassName: "Barbarian", ClassSource: "XPHB", Srd52: true,
				SubclassFeatures: []string{"Homebrew Sniffle|Barbarian|XPHB|Berserker|XPHB|6"},
			}},
		}
	}
	gated := resolveSubclassFeatureNames(mk(false), &mk(false).Subclass[0], &cfg{srOnly: true})
	if len(gated) != 0 {
		t.Fatalf("srd52 gate did not filter non-SRD record: %+v", gated)
	}
	opened := resolveSubclassFeatureNames(mk(false), &mk(false).Subclass[0], &cfg{srOnly: false})
	if len(opened) != 1 || opened[0].Name != "Homebrew Sniffle" {
		t.Fatalf("gate-off run should keep the record: %+v", opened)
	}
}

// ── background / species / feat namespaces ──

// soldier-shaped background record: the schema carries the source's typed
// ability blocks verbatim (choose/weighted), proficiency tokens verbatim
// (including the anyGamingSet picker), and equipment bundles in the same
// {item, qty}/{gp} form as the class namespace; equipmentType prose-picks are
// excluded and origin feats resolve to name/source (+note when present).
func TestExtractBackgroundCarriesRecordShape(t *testing.T) {
	bg := &Background{
		Name: "Soldier", Source: "XPHB", Page: 185, Srd52: true,
		Ability: []map[string]any{
			{"choose": map[string]any{"weighted": map[string]any{
				"from":    []any{"str", "dex", "con"},
				"weights": []any{float64(2), float64(1)},
			}}},
			{"choose": map[string]any{"weighted": map[string]any{
				"from":    []any{"str", "dex", "con"},
				"weights": []any{float64(1), float64(1), float64(1)},
			}}},
		},
		Feats: []map[string]any{{"savage attacker|xphb": true}},
		SkillProficiencies: []map[string]any{
			{"athletics": true, "intimidation": true},
		},
		ToolProficiencies: []map[string]any{{"anyGamingSet": 1}},
		StartingEquipment: []map[string]json.RawMessage{{
			"A": json.RawMessage(`[{"item": "spear|xphb"}, {"item": "parchment|xphb", "quantity": 10}, {"value": 1400}, {"equipmentType": "setGaming"}]`),
			"B": json.RawMessage(`[{"value": 5000}]`),
		}},
	}
	ns, err := extractBackground(bg, &cfg{})
	if err != nil {
		t.Fatalf("extractBackground: %v", err)
	}
	bm := ns.Background
	if bm == nil || bm.Name != "Soldier" || bm.Source != "XPHB" || bm.Page != 185 {
		t.Fatalf("identity fields wrong: %+v", bm)
	}
	// Ability blocks carried verbatim; weights arrive as bare JSON numbers and
	// must be integral, typed scalars, not float64.
	if len(bm.Ability) != 2 {
		t.Fatalf("want both ability blocks, got %d", len(bm.Ability))
	}
	weighted := bm.Ability[0]["choose"].(map[string]any)["weighted"].(map[string]any)
	weights := weighted["weights"].([]any)
	if len(weights) != 2 || weights[0] != 2 || weights[1] != 1 {
		t.Fatalf("weights wrong: %v (%T)", weights, weights)
	}
	if froms := weighted["from"].([]any); len(froms) != 3 || froms[0] != "str" {
		t.Fatalf("from order/pick list wrong: %v", froms)
	}
	// Proficiencies keep the source vocabulary: fixed grants are maps, the
	// picker token is carried as-is (no invented name lists).
	if got := bm.Skills[0]; len(got) != 2 || got["athletics"] != true || got["intimidation"] != true {
		t.Fatalf("skills wrong: %v", got)
	}
	if got := bm.Tools[0]; got["anyGamingSet"] != 1 {
		t.Fatalf("tool picker token wrong: %v", got)
	}
	// Equipment: spear + 10 parchment + 14 gp in bundle A; equipmentType prose
	// picks get no item entry; bundle B is the plain 50 gp gold grant.
	a := bm.StartingEquipment[0]
	if a.ID != "A" || len(a.Items) != 3 {
		t.Fatalf("bundle A wrong: %+v", a.Items)
	}
	if a.Items[0].Item != "spear" || a.Items[1].Item != "parchment" || a.Items[1].Qty != 10 || a.Items[2].GP != 14 {
		t.Fatalf("bundle A items wrong: %+v", a.Items)
	}
	if b := bm.StartingEquipment[1]; b.ID != "B" || len(b.Items) != 1 || b.Items[0].GP != 50 {
		t.Fatalf("bundle B wrong: %+v", b.Items)
	}
	// Origin feat resolves the lower-cased ref to a display name.
	if len(bm.OriginFeats) != 1 || bm.OriginFeats[0].Name != "Savage Attacker" || bm.OriginFeats[0].Source != "XPHB" {
		t.Fatalf("origin feat wrong: %+v", bm.OriginFeats)
	}
}

// Origin-feat refs can carry a pick variant ("magic initiate; cleric|xphb");
// the variant stays a separate token and links resolve relative to the feats
// dir only when the note actually exists.
func TestExtractBackgroundOriginFeatVariantAndNote(t *testing.T) {
	withFeatDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(withFeatDir, "magic-initiate-xphb.md"), []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bg := func() *Background {
		return &Background{Name: "Acolyte", Source: "XPHB", Srd52: true, Feats: []map[string]any{{"magic initiate; cleric|xphb": true}}}
	}
	ns, err := extractBackground(bg(), &cfg{featDir: withFeatDir})
	if err != nil {
		t.Fatalf("extractBackground: %v", err)
	}
	of := ns.Background.OriginFeats[0]
	if of.Name != "Magic Initiate" || of.Variant != "cleric" || of.Source != "XPHB" {
		t.Fatalf("variant resolution wrong: %+v", of)
	}
	if of.Note != "./magic-initiate-xphb.md" {
		t.Fatalf("existing note not linked: %+v", of)
	}
	// No feats dir → still a data-only ref, no invented link.
	if ns2, _ := extractBackground(bg(), &cfg{featDir: t.TempDir()}); ns2.Background.OriginFeats[0].Note != "" {
		t.Fatalf("note link invented without a note: %+v", ns2.Background.OriginFeats[0])
	}
}

// Species extraction carries the few typed pickups and nothing from entries
// prose; languages follow the same verbatim token rule as proficiencies and a
// non-scalar speed shape is omitted, never guessed.
func TestExtractSpeciesCarriesTypedPickups(t *testing.T) {
	rc := func() *Race {
		return &Race{
			Name: "Dwarf", Source: "XPHB", Page: 188, Srd52: true,
			Size:                  []string{"M"},
			Speed:                 json.RawMessage(`30`),
			LanguageProficiencies: []map[string]any{{"auran": true}},
		}
	}
	ns, err := extractSpecies(rc(), &cfg{})
	if err != nil {
		t.Fatalf("extractSpecies: %v", err)
	}
	sm := ns.Species
	if sm == nil || sm.Name != "Dwarf" || sm.Source != "XPHB" || sm.Page != 188 {
		t.Fatalf("identity fields wrong: %+v", sm)
	}
	if len(sm.Size) != 1 || sm.Size[0] != "M" || sm.Speed != 30 {
		t.Fatalf("size/speed wrong: %+v", sm)
	}
	if langs := sm.Languages; len(langs) != 1 || langs[0]["auran"] != true {
		t.Fatalf("languages wrong: %+v", langs)
	}
	// Composite speeds are skipped (with a warn), not parsed into guesses.
	composite := func() *Race {
		r := rc()
		r.Speed = json.RawMessage(`{"walk": 20, "fly": 50}`)
		return r
	}
	if ns2, _ := extractSpecies(composite(), &cfg{}); ns2.Species.Speed != 0 {
		t.Fatalf("composite speed must be omitted, got %d", ns2.Species.Speed)
	}
}

// Human-like species carry a size range; both range members must survive.
func TestExtractSpeciesSizeRange(t *testing.T) {
	ns, err := extractSpecies(&Race{Name: "Human", Source: "XPHB", Srd52: true, Size: []string{"S", "M"}, Speed: json.RawMessage(`30`)}, &cfg{})
	if err != nil {
		t.Fatalf("extractSpecies: %v", err)
	}
	if sizes := ns.Species.Size; len(sizes) != 2 || sizes[0] != "S" || sizes[1] != "M" {
		t.Fatalf("size range wrong: %+v", sizes)
	}
}

// boon-of-fate-shaped feat: category bucket straight from the source and
// machine-shaped prerequisites (level only); the benefits stay prose.
func TestExtractFeatPrerequisites(t *testing.T) {
	ns, err := extractFeat(&Feat{
		Name: "Boon of Fate", Source: "XPHB", Page: 210, Srd52: true, Category: "EB",
		Prerequisite: []map[string]json.RawMessage{{"level": json.RawMessage(`19`)}},
	}, &cfg{})
	if err != nil {
		t.Fatalf("extractFeat: %v", err)
	}
	fm := ns.Feat
	if fm == nil || fm.Name != "Boon of Fate" || fm.Category != "EB" || fm.Page != 210 {
		t.Fatalf("identity fields wrong: %+v", fm)
	}
	if len(fm.Prerequisites) != 1 {
		t.Fatalf("want one prerequisite, got %+v", fm.Prerequisites)
	}
	pr := fm.Prerequisites[0]
	if pr.Level != 19 || pr.Ability != nil || pr.Feature != nil || pr.Spellcasting {
		t.Fatalf("prerequisite wrong: %+v", pr)
	}
}

// Fighting-style feats ride a named feature grant; Boon of Spell Recall adds
// the spellcasting requirement (the source's spellcasting2020 flag); Grappler
// lists alternative ability floors as two blocks.
func TestExtractFeatPrerequisiteVariants(t *testing.T) {
	archery, err := extractFeat(&Feat{
		Name: "Archery", Source: "XPHB", Srd52: true, Category: "FS",
		Prerequisite: []map[string]json.RawMessage{{"feature": json.RawMessage(`["Fighting Style"]`)}},
	}, &cfg{})
	if err != nil {
		t.Fatalf("extractFeat: %v", err)
	}
	if pr := archery.Feat.Prerequisites[0]; len(pr.Feature) != 1 || pr.Feature[0] != "Fighting Style" {
		t.Fatalf("feature prerequisite wrong: %+v", pr)
	}

	recall, err := extractFeat(&Feat{
		Name: "Boon of Spell Recall", Source: "XPHB", Srd52: true, Category: "EB",
		Prerequisite: []map[string]json.RawMessage{{"level": json.RawMessage(`19`), "spellcasting2020": json.RawMessage(`true`)}},
	}, &cfg{})
	if err != nil {
		t.Fatalf("extractFeat: %v", err)
	}
	if pr := recall.Feat.Prerequisites[0]; pr.Level != 19 || !pr.Spellcasting {
		t.Fatalf("spellcasting prerequisite wrong: %+v", pr)
	}

	grappler, err := extractFeat(&Feat{
		Name: "Grappler", Source: "XPHB", Srd52: true, Category: "G",
		Prerequisite: []map[string]json.RawMessage{
			{"level": json.RawMessage(`4`), "ability": json.RawMessage(`[{"str": 13}]`)},
			{"level": json.RawMessage(`4`), "ability": json.RawMessage(`[{"dex": 13}]`)},
		},
	}, &cfg{})
	if err != nil {
		t.Fatalf("extractFeat: %v", err)
	}
	if prs := grappler.Feat.Prerequisites; len(prs) != 2 || prs[0].Ability["str"] != 13 || prs[1].Ability["dex"] != 13 {
		t.Fatalf("ability prerequisites wrong: %+v", prs)
	}
}

// A prerequisite block with unmodeled keys is skipped whole (no partial
// requirements); a feat whose only blocks are unmodeled ships none at all.
func TestExtractFeatSkipsUnmodeledPrerequisiteBlocks(t *testing.T) {
	ns, _ := extractFeat(&Feat{
		Name: "Exotic", Source: "SRC", Srd52: true,
		Prerequisite: []map[string]json.RawMessage{
			{"level": json.RawMessage(`4`), "campaign": json.RawMessage(`{"fr:cobalt": true}`)},
			{"level": json.RawMessage(`19`)},
		},
	}, &cfg{})
	if prs := ns.Feat.Prerequisites; len(prs) != 1 || prs[0].Level != 19 {
		t.Fatalf("unmodeled block must be dropped whole: %+v", prs)
	}
	proseOnly, _ := extractFeat(&Feat{
		Name: "Special Only", Source: "SRC", Srd52: true,
		Prerequisite: []map[string]json.RawMessage{{"otherSummary": json.RawMessage(`{"entry": "Special", "entrySummary": "Special"}`)}},
	}, &cfg{})
	if len(proseOnly.Feat.Prerequisites) != 0 {
		t.Fatalf("prose-only prerequisites must stay out: %+v", proseOnly.Feat.Prerequisites)
	}
}

// The srd52 gate applies to every flat kind with the same default and the
// same -srd=false escape hatch as the class namespace.
func TestSrdGatesFlatKinds(t *testing.T) {
	checks := map[string]func(*cfg) error{
		"background": func(c *cfg) error {
			_, err := extractBackground(&Background{Name: "Variant Spy", Source: "PHB"}, c)
			return err
		},
		"species": func(c *cfg) error {
			_, err := extractSpecies(&Race{Name: "Aasimar", Source: "XPHB"}, c)
			return err
		},
		"feat": func(c *cfg) error {
			_, err := extractFeat(&Feat{Name: "Homebrew Boon", Source: "HB"}, c)
			return err
		},
	}
	for kind, run := range checks {
		if err := run(&cfg{srOnly: true}); err == nil || !strings.Contains(err.Error(), "not flagged srd52") {
			t.Errorf("%s: gate did not refuse: %v", kind, err)
		} else if !strings.Contains(err.Error(), "-srd=false") {
			t.Errorf("%s: refusal missing the escape hatch pointer: %v", kind, err)
		}
		if err := run(&cfg{srOnly: false}); err != nil {
			t.Errorf("%s: gate-off run must pass: %v", kind, err)
		}
	}
}

// Each flat namespace merges idempotently and replaces a previous
// cross-kind namespace (only one kind survives under semiplane:).
func TestFlatNamespacesMergeIdempotentAndReplacing(t *testing.T) {
	bg := &Namespace{GeneratedFrom: "backgrounds.json", Background: &BackgroundMeta{
		Name: "Soldier", Source: "XPHB", Page: 185,
		Ability: []map[string]any{{"choose": map[string]any{"weighted": map[string]any{"from": []any{"str"}}}}},
	}}
	once := mustBuild(t, bg)
	twice, err := merge([]byte(once), bg)
	if err != nil {
		t.Fatalf("second background merge: %v", err)
	}
	if string(twice) != once {
		t.Fatalf("background merge not idempotent:\n%s\n---\n%s", once, twice)
	}

	sp := &Namespace{Species: &SpeciesMeta{Name: "Dwarf", Source: "XPHB", Speed: 30}}
	onceS := mustBuild(t, sp)
	twiceS, err := merge([]byte(onceS), sp)
	if err != nil || string(twiceS) != onceS {
		t.Fatalf("species merge not idempotent: %v\n%s", err, twiceS)
	}

	ft := &Namespace{Feat: &FeatMeta{Name: "Boon of Fate", Source: "XPHB", Category: "EB"}}
	onceF := mustBuild(t, ft)
	twiceF, err := merge([]byte(onceF), ft)
	if err != nil || string(twiceF) != onceF {
		t.Fatalf("feat merge not idempotent: %v\n%s", err, twiceF)
	}

	// Cross-kind replacement: the feat run over a background note must drop
	// the background block entirely.
	replaced, err := merge([]byte(once), ft)
	if err != nil {
		t.Fatalf("replace merge: %v", err)
	}
	if strings.Contains(string(replaced), "background") {
		t.Fatalf("old background namespace leaked through:\n%s", string(replaced))
	}
	if !strings.Contains(string(replaced), "feat:") {
		t.Fatal("new feat namespace missing:\n" + string(replaced))
	}
}

// The flat kinds run to a merge through the shared plumbing: the marker-scoped
// merge applies to their notes exactly like it does to class notes.
func TestCutTargetAndNoteFileName(t *testing.T) {
	if _, _, err := cutTarget("background", "Soldier"); err == nil {
		t.Fatal("cutTarget must reject malformed targets")
	}
	if name, source, err := cutTarget("feat", "Boon of Fate@XPHB"); err != nil || name != "Boon of Fate" || source != "XPHB" {
		t.Fatalf("cutTarget wrong: %q %q %v", name, source, err)
	}
	if got := noteFileName("Boon of Fate", "XPHB"); got != "boon-of-fate-xphb.md" {
		t.Fatalf("noteFileName wrong: %q", got)
	}
}

// ── spell namespace ──

// extractSpellTestData loads one record (by name) from the tool's testdata
// spell corpus through the same loader the CLI runs use.
func extractSpellTestData(t *testing.T, name string, c *cfg) (*Namespace, *Spell) {
	t.Helper()
	file, err := loadSpellSource(c, "XPHB")
	if err != nil {
		t.Fatalf("loadSpellSource: %v", err)
	}
	joins := loadSpellJoins(c)
	var rec *Spell
	for i := range file.Spell {
		if strings.EqualFold(file.Spell[i].Name, name) {
			rec = &file.Spell[i]
			break
		}
	}
	if rec == nil {
		t.Fatalf("spell %q not in testdata", name)
	}
	ns, err := extractSpell(rec, joins["xphb"][strings.ToLower(name)], c)
	if err != nil {
		t.Fatalf("extractSpell %s: %v", name, err)
	}
	return ns, rec
}

// The structural casting data carries through verbatim; the entries/
// entriesHigherLevel prose never reaches the namespace; class/subclass lists
// come from the gendata join (the record itself carries no classes key).
func TestExtractSpellCarriesStructuralShape(t *testing.T) {
	ns, _ := extractSpellTestData(t, "Fireball", &cfg{dataDir: "testdata"})
	sp := ns.Spell
	if sp.Name != "Fireball" || sp.Source != "XPHB" || sp.Page != 274 {
		t.Fatalf("identity fields wrong: %+v", sp)
	}
	if sp.Level != 3 || sp.School != "V" {
		t.Fatalf("level/school wrong: %d / %q", sp.Level, sp.School)
	}
	if len(sp.Time) != 1 || sp.Time[0].Number != 1 || sp.Time[0].Unit != "action" || sp.Time[0].Condition != "" {
		t.Fatalf("time wrong: %+v", sp.Time)
	}
	if sp.Range.Type != "point" || sp.Range.Distance == nil ||
		sp.Range.Distance.Type != "feet" || sp.Range.Distance.Amount != 150 {
		t.Fatalf("range wrong: %+v", sp.Range)
	}
	if len(sp.Duration) != 1 || sp.Duration[0].Type != "instant" ||
		sp.Duration[0].Concentration || sp.Duration[0].Ends != nil {
		t.Fatalf("duration wrong: %+v", sp.Duration)
	}
	if sp.Components == nil || !sp.Components.V || !sp.Components.S ||
		sp.Components.M == nil || sp.Components.M.Text != "a ball of bat guano and sulfur" {
		t.Fatalf("components wrong: %+v", sp.Components)
	}
	if got := sp.SavingThrow; len(got) != 1 || got[0] != "dexterity" {
		t.Fatalf("savingThrow wrong: %v", got)
	}
	// Fireball carries no structural damage in the corpus: the field stays
	// absent (never parsed out of the entries prose), per the tool's rule.
	if sp.Damage != nil {
		t.Fatalf("prose-only damage must stay absent: %+v", sp.Damage)
	}
	// The class join: Sorcerer + Wizard, sorted, source-tagged.
	if len(sp.Classes) != 2 || sp.Classes[0].Name != "Sorcerer" || sp.Classes[0].Source != "XPHB" ||
		sp.Classes[1].Name != "Wizard" || sp.Classes[1].Source != "XPHB" {
		t.Fatalf("class join wrong: %+v", sp.Classes)
	}
	// Subclass refs resolve with the class point on them and ordered
	// deterministically across sources.
	if len(sp.Subclasses) != 4 {
		t.Fatalf("subclass join wrong: %+v", sp.Subclasses)
	}
	bard := sp.Subclasses[2] // sorted: PHB Cleric, TCE Artificer, XPHB Bard, XPHB Wizard
	if bard.Name != "College of Lore" || bard.ShortName != "Lore" || bard.Class.Name != "Bard" {
		t.Fatalf("subclass ref wrong: %+v", bard)
	}
}

// Prose never leaks and absent structural keys are absent in the merged YAML.
func TestSpellNamespaceOmitsProseAndAbsentKeys(t *testing.T) {
	ns, _ := extractSpellTestData(t, "Fireball", &cfg{dataDir: "testdata"})
	out := mustBuild(t, ns)
	if strings.Contains(out, "bright streak") || strings.Contains(out, "entered prose") {
		t.Fatal("entries prose leaked into the namespace:\n" + out)
	}
	if strings.Contains(out, "damageInflict") || strings.Contains(out, "scalingLevelDice") {
		t.Fatal("non-modeled keys leaked:\n" + out)
	}
	if strings.Contains(out, "damage:") {
		t.Fatal("fireball carries no structural damage; the damage key must be absent:\n" + out)
	}
	// Acid Splash: cantrip, m: null → components carry v/s only, no m key.
	ns0, _ := extractSpellTestData(t, "Acid Splash", &cfg{dataDir: "testdata"})
	sp0 := ns0.Spell
	if sp0.Level != 0 {
		t.Fatalf("cantrip level wrong: %d", sp0.Level)
	}
	if sp0.Components.M != nil {
		t.Fatalf("m: null must keep the m key absent: %+v", sp0.Components)
	}
	out0 := mustBuild(t, ns0)
	if strings.Contains(out0, "\nm:\n") {
		t.Fatal("m key must not be emitted for a null material:\n" + out0)
	}
	// A self range carries its type with no distance block.
	ns1, _ := extractSpellTestData(t, "Banishing Smite", &cfg{dataDir: "testdata"})
	if ns1.Spell.Range.Type != "self" || ns1.Spell.Range.Distance != nil {
		t.Fatalf("self range wrong: %+v", ns1.Spell.Range)
	}
	// …and the typed casting-time condition survives with its block.
	if ns1.Spell.Time[0].Condition == "" {
		t.Fatalf("typed condition dropped: %+v", ns1.Spell.Time)
	}
}

// Damage rides in only where the record structurally carries it; an
// unmodeled damage key skips the block whole (with a warn).
func TestSpellDamageOnlyWhenCarried(t *testing.T) {
	ns, _ := extractSpellTestData(t, "Imploding Smoke", &cfg{dataDir: "testdata"})
	d := ns.Spell.Damage
	if d == nil || d.Dice != "8d6" || d.DamageType != "necrotic" {
		t.Fatalf("structural damage wrong: %+v", d)
	}
	out := mustBuild(t, ns)
	if !strings.Contains(out, "dice: 8d6") || !strings.Contains(out, "damageType: necrotic") {
		t.Fatal("structural damage missing from merged namespace:\n" + out)
	}

	// A damage object carrying an unmodeled key (e.g. higherTypical in other
	// sources' files) is never half-read.
	rec := &Spell{
		Name: "Shaped Blast", Source: "XPHB", Level: 1, School: "E",
		Time:   []map[string]json.RawMessage{{"number": json.RawMessage(`1`), "unit": json.RawMessage(`"action"`)}},
		Range:  map[string]json.RawMessage{"type": json.RawMessage(`"self"`)},
		Damage: map[string]json.RawMessage{"dice": json.RawMessage(`"1d6"`), "higherTypical": json.RawMessage(`"2d6"`)},
	}
	ns2, _ := extractSpell(rec, &spellJoinSpell{}, &cfg{})
	if ns2.Spell.Damage != nil {
		t.Fatalf("unmodeled damage keys must skip the block whole: %+v", ns2.Spell.Damage)
	}
	// A null damage field carries nothing structural either.
	rec.Damage = map[string]json.RawMessage{"damage": nil}
	ns3, _ := extractSpell(rec, &spellJoinSpell{}, &cfg{})
	if ns3.Spell.Damage != nil {
		t.Fatalf("null damage must stay absent: %+v", ns3.Spell.Damage)
	}
}

// A string srd52 value is a renamed SRD 5.2 entry: the published name names
// the record (and the note it merges into), while the flag still passes the
// gate. The spell notes also exist under an apostrophe-stripped spelling; the
// note resolver must fall back exactly there and nowhere else.
func TestSpellRenameAndNotePathFallback(t *testing.T) {
	ns, rec := extractSpellTestData(t, "Bigby's Hand", &cfg{dataDir: "testdata"})
	if ns.Spell.Name != "Arcane Hand" {
		t.Fatalf("renamed entry must publish under its SRD name: %q", ns.Spell.Name)
	}
	if spellDisplayName(rec) != "Arcane Hand" || !spellHasSrd52(rec) {
		t.Fatalf("rename helpers wrong: %q/%v", spellDisplayName(rec), spellHasSrd52(rec))
	}

	dir := t.TempDir()
	if p, ok := notePath(dir, "Dragon's Breath", "XPHB"); ok {
		t.Fatalf("no note must be reported as such: %s", p)
	}
	fallback := filepath.Join(dir, "dragons-breath-xphb.md")
	if err := os.WriteFile(fallback, []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, ok := notePath(dir, "Dragon's Breath", "XPHB")
	if !ok || p != fallback {
		t.Fatalf("apostrophe-stripped fallback broken: ok=%v p=%s", ok, p)
	}
	// The primary convention wins when both spellings exist.
	primary := filepath.Join(dir, "dragon-s-breath-xphb.md")
	if err := os.WriteFile(primary, []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, _ := notePath(dir, "Dragon's Breath", "XPHB"); p != primary {
		t.Fatalf("primary convention must win: %s", p)
	}
}

// The bulk mode walks the whole corpus in one pass and prints a count
// summary; a second identical pass is all up-to-date and byte-for-byte equal.
func TestSpellBulkIdempotent(t *testing.T) {
	c := &cfg{
		dataDir: "testdata", srOnly: true,
		outDir: t.TempDir(), // classes dir for subclass-note links (none exist)
	}
	c.spellDir = t.TempDir()
	// Notes exist for every gated record (including the renamed Arcane Hand);
	// Friends is not flagged srd52 and is counted as gated whether or not the
	// note exists.
	for _, note := range []string{
		"fireball-xphb.md", "acid-splash-xphb.md", "arcane-hand-xphb.md",
		"imploding-smoke-xphb.md", "banishing-smite-xphb.md", "friends-xphb.md",
	} {
		if err := os.WriteFile(filepath.Join(c.spellDir, note), []byte(sampleMD), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	first, err := processSpellsBulk(c, false, false)
	if err != nil {
		t.Fatalf("bulk pass 1: %v", err)
	}
	if first.updated != 5 || first.upToDate != 0 || first.gated != 1 || first.noNote != 0 {
		t.Fatalf("pass-1 summary wrong: %+v", first)
	}
	// Snapshot one run's output; the second pass must leave every file alone.
	fireball, err := os.ReadFile(filepath.Join(c.spellDir, "fireball-xphb.md"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := processSpellsBulk(c, false, false)
	if err != nil {
		t.Fatalf("bulk pass 2: %v", err)
	}
	if second.updated != 0 || second.upToDate != 5 || second.gated != 1 {
		t.Fatalf("pass-2 summary wrong: %+v", second)
	}
	after, err := os.ReadFile(filepath.Join(c.spellDir, "fireball-xphb.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(fireball) {
		t.Fatalf("second bulk pass rewrote the note:\n%s\n---\n%s", fireball, after)
	}
	// The merged namespace carries the rename through the bulk path too.
	renamed, err := os.ReadFile(filepath.Join(c.spellDir, "arcane-hand-xphb.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(renamed), "name: Arcane Hand") {
		t.Fatalf("renamed spell not published by its SRD name:\n%s", renamed)
	}
}

// A null srd52 (or missing field) is not flagged: same refusal, same
// -srd=false escape hatch as the other kinds, for both new namespaces.
func TestSrdGatesSpellAndOptFeature(t *testing.T) {
	checks := map[string]func(*cfg) error{
		"spell": func(c *cfg) error {
			_, err := extractSpell(&Spell{Name: "Friends", Source: "XPHB"}, &spellJoinSpell{}, c)
			return err
		},
		"optional feature": func(c *cfg) error {
			_, err := extractOptionalFeature(&OptionalFeature{Name: "Homebrew Boon", Source: "HB"}, &OptionalFeatureFile{}, c)
			return err
		},
	}
	for kind, run := range checks {
		if err := run(&cfg{srOnly: true}); err == nil || !strings.Contains(err.Error(), "not flagged srd52") {
			t.Errorf("%s: gate did not refuse: %v", kind, err)
		} else if !strings.Contains(err.Error(), "-srd=false") {
			t.Errorf("%s: refusal missing the escape hatch pointer: %v", kind, err)
		}
		if err := run(&cfg{srOnly: false}); err != nil {
			t.Errorf("%s: gate-off run must pass: %v", kind, err)
		}
	}
}

// ── optional-feature namespace ──

// extractOptFeatureTestData loads one record from the tool's testdata
// optionalfeatures file.
func extractOptFeatureTestData(t *testing.T, name string, srOnly bool) (*Namespace, *OptionalFeatureFile) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "optionalfeatures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file OptionalFeatureFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	var rec *OptionalFeature
	for i := range file.OptionalFeature {
		if strings.EqualFold(file.OptionalFeature[i].Name, name) {
			rec = &file.OptionalFeature[i]
			break
		}
	}
	if rec == nil {
		t.Fatalf("optional feature %q not in testdata", name)
	}
	ns, err := extractOptionalFeature(rec, &file, &cfg{srOnly: srOnly})
	if err != nil {
		t.Fatalf("extractOptionalFeature %s: %v", name, err)
	}
	return ns, &file
}

// Subtle Spell: the metamagic token, the sorcery-point cost, and — having no
// machine-shaped prerequisites — no prerequisites key at all.
func TestExtractOptFeatureSubtleSpell(t *testing.T) {
	ns, _ := extractOptFeatureTestData(t, "Subtle Spell", true)
	fm := ns.OptionalFeature
	if fm.Name != "Subtle Spell" || fm.Source != "XPHB" || fm.Page != 142 {
		t.Fatalf("identity fields wrong: %+v", fm)
	}
	if len(fm.FeatureType) != 1 || fm.FeatureType[0] != "MM" {
		t.Fatalf("featureType wrong: %v", fm.FeatureType)
	}
	if len(fm.Consumes) != 1 || fm.Consumes["name"] != "Sorcery Point" {
		t.Fatalf("consumes wrong: %v", fm.Consumes)
	}
	if len(fm.Prerequisites) != 0 {
		t.Fatalf("no machine-shaped prerequisites must ship none: %+v", fm.Prerequisites)
	}
	out := mustBuild(t, ns)
	if !strings.Contains(out, "featureType:\n      - MM") {
		t.Fatal("featureType missing from merged namespace:\n" + out)
	}
	if strings.Contains(out, "prerequisites") {
		t.Fatal("absent prerequisites must not be emitted:\n" + out)
	}
	if strings.Contains(out, "Spend 1 Sorcery Point") {
		t.Fatal("entries prose leaked into the namespace:\n" + out)
	}
}

// Lifedrinker: the level block unwraps to level + granting class ref (the
// visibleStats UI hint stays out), and the prerequisite optional-feature ref
// resolves its display name from the file's own records.
func TestExtractOptFeaturePrerequisites(t *testing.T) {
	ns, _ := extractOptFeatureTestData(t, "Lifedrinker", true)
	if prs := ns.OptionalFeature.Prerequisites; len(prs) != 1 {
		t.Fatalf("want one prerequisite, got %+v", prs)
	}
	pr := ns.OptionalFeature.Prerequisites[0]
	if pr.Level != 9 || pr.Class == nil || pr.Class.Name != "Warlock" || pr.Class.Source != "XPHB" {
		t.Fatalf("level prerequisite wrong: %+v", pr)
	}
	if len(pr.OptionalFeature) != 1 || pr.OptionalFeature[0].Name != "Pact of the Blade" || pr.OptionalFeature[0].Source != "XPHB" {
		t.Fatalf("optional-feature ref wrong: %+v", pr.OptionalFeature)
	}
	out := mustBuild(t, ns)
	if !strings.Contains(out, "level: 9") ||
		!strings.Contains(out, "name: Warlock\n          source: XPHB") ||
		!strings.Contains(out, "name: Pact of the Blade") {
		t.Fatal("prerequisite shape wrong in merged namespace:\n" + out)
	}
	if strings.Contains(out, "visibleStats") {
		t.Fatal("visibleStats is a UI hint and must stay out:\n" + out)
	}
}

// A prerequisite block with any unmodeled key is skipped whole — the prose
// item, and the XPHB cantrip-filter object (which pairs a real level block
// with prose filter status; half-reading it would mean the wrong level).
func TestExtractOptFeatureSkipsUnmodeledPrerequisiteBlocks(t *testing.T) {
	ns, _ := extractOptFeatureTestData(t, "Agonizing Blast", true)
	if prs := ns.OptionalFeature.Prerequisites; len(prs) != 0 {
		t.Fatalf("cantrip-filter block must be skipped whole: %+v", prs)
	}
	ns2, _ := extractOptFeatureTestData(t, "Item Locked", true)
	if prs := ns2.OptionalFeature.Prerequisites; len(prs) != 0 {
		t.Fatalf("item block must be skipped whole: %+v", prs)
	}
}

// PHB-era records (gate lifted) carry the typed string spell refs and the
// pact token verbatim; the class ref may lack a source segment.
func TestExtractOptFeaturePHBEraPrerequisite(t *testing.T) {
	ns, _ := extractOptFeatureTestData(t, "Old Blast", false)
	fm := ns.OptionalFeature
	if len(fm.Prerequisites) != 1 {
		t.Fatalf("want one prerequisite, got %+v", fm.Prerequisites)
	}
	pr := fm.Prerequisites[0]
	if pr.Level != 2 || pr.Pact != "Tome" || len(pr.Spell) != 1 || pr.Spell[0] != "eldritch blast#c" {
		t.Fatalf("PHB-era terms wrong: %+v", pr)
	}
	if pr.Class == nil || pr.Class.Name != "Warlock" || pr.Class.Source != "" {
		t.Fatalf("class ref (no source segment) wrong: %+v", pr.Class)
	}
}

// Both new namespaces merge idempotently like the flat ones.
func TestSpellAndOptFeatureMergeIdempotent(t *testing.T) {
	sp := &Namespace{GeneratedFrom: "spells/spells-xphb.json", Spell: &SpellMeta{
		Name: "Fireball", Source: "XPHB", Level: 3, School: "V",
		Time:  []SpellTime{{Number: 1, Unit: "action"}},
		Range: &SpellRange{Type: "point", Distance: &SpellRangeDistance{Type: "feet", Amount: 150}},
	}}
	once := mustBuild(t, sp)
	twice, err := merge([]byte(once), sp)
	if err != nil || string(twice) != once {
		t.Fatalf("spell merge not idempotent: %v\n%s", err, twice)
	}

	of := &Namespace{OptionalFeature: &OptionalFeatureMeta{
		Name: "Subtle Spell", Source: "XPHB", FeatureType: []string{"MM"},
		Consumes: map[string]any{"name": "Sorcery Point"},
	}}
	onceOF := mustBuild(t, of)
	twiceOF, err := merge([]byte(onceOF), of)
	if err != nil || string(twiceOF) != onceOF {
		t.Fatalf("optional-feature merge not idempotent: %v\n%s", err, twiceOF)
	}
}
