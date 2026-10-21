# Experiment: gameplay data from 5e.tools JSON → `semiplane:` frontmatter

*Status: experiment, not a decision · 2026-10-20*

## Goal

The character-creator wizard (tech-stack §3.4: "SPA wizard driven by `rules/`
metadata") needs classes and subclasses as data, not prose. This experiment
extracts the gameplay data from the typed source JSON in `fixtures/data` into a
machine-readable `semiplane:` frontmatter namespace on the matching compendium
Markdown, using **Barbarian (XPHB)** and its subclass **Path of the Berserker
(XPHB)** as the first targets.

## What was built

`tools/json2fm-5e` (out-of-tree helper per repo-layout; consumes
only stdlib + `gopkg.in/yaml.v3`, never `internal/*`):

```
go run ./tools/json2fm-5e -class Barbarian@XPHB   # merge + write
go run ./tools/json2fm-5e -class Barbarian@XPHB -dry-run
go run ./tools/json2fm-5e -class Barbarian@XPHB -print
```

Constraints it enforces (tech-stack §5):

- Machine data lives **only** under one top-level `semiplane:` key in the
  frontmatter namespace; the tool replaces that key and nothing else.
- Regeneration is **idempotent** (second run: `up-to-date`); prose, tags, and
  every other frontmatter key are carried through byte-for-byte.
- The merged frontmatter is re-parsed with yaml.v3 before writing; an invalid
  or namespace-less result aborts instead of corrupting the note.

## Schema (v1, dnd5e-scoped)

Class note (`fixtures/compendium/classes/barbarian-xphb.md`):

```yaml
semiplane:
  schemaVersion: 1                # namespace-shape generation; bump on schema change
  generatedFrom: class/class-barbarian.json
  class:
    name: Barbarian
    source: XPHB
    page: 50
    primaryAbility: [str]
    saves: [str, con]
    spellcastingAbility: int      # casters only (wizard: int, cleric: wis…);
                                  # non-caster notes omit the key entirely
    hp:
      hitDie: 12
      firstLevel: "12 + @con"        # dice-grammar strings, @con = CON modifier
      perLevel:
        roll: "1d12 + @con"
        rollMin: 1                   # floor for a bad die, so level-ups never lose HP
        flat: "7 + @con"             # "take the average" alternative = hd/2 + 1
    proficiencies:            # armor/weapons lists + skills choose{n, from}
    multiclassProficiencies: # what you gain when multiclassing in/out
    multiclass:               # ability-score gate on multiclassing IN, e.g.
      requirements: {int: 13} # PHB-era records. The 2024 rules dropped the
                              # gate, so gated (XPHB) notes carry no block —
                              # never inferred from another edition's record.
    startingEquipment:       # structured, quantities separate:
                             #   - {item: greataxe}
                             #   - {item: handaxe, qty: 4}
                             #   - {gp: 15}
    subclassTitle: Barbarian Subclass
    subclasses:              # every edition's pick list; note: on the note if it exists
    features:                # ordered (name, slug, level); slug = stable id.
                             # Optional per-field enrichments, both corpus-derived:
                             #   op/refSlug — recurrence (see below), e.g.
                             #     - {name: Ability Score Improvement,
                             #        slug: ability-score-improvement, level: 8,
                             #        op: extends, refSlug: ability-score-improvement}
                             #   choices — structured option payloads (see below), e.g.
                             #     - {name: Divine Order, slug: divine-order, level: 1,
                             #        choices: [{count: 1, from: [Protector, Thaumaturge]}]}
    featProgression:         # where named feat grants land by level
                             #   - {level: 19, name: Epic Boon, category: [EB]}
    progression:             # per level: pb, feature slugs, and every class-table
                             # column inlined under its label-slug: rages,
                             # rage-damage, weapon-mastery (Barbarian); casters
                             # additionally carry cantrips, prepared-spells and
                             # the spell-slot columns (1st … nth)
```

Subclass note (`.../barbarian-xphb-path-of-the-berserker-xphb.md`):
`semiplane.subclass.{name, shortName, source, page, class{name,source,note},
features[{name,slug,level}]}`, stamped with the same `schemaVersion` under
`semiplane:` — Berserker resolves to Frenzy 3,
Mindless Rage 6, Retaliation 10, Intimidating Presence 14.

### Schema version marker (`schemaVersion`)

`schemaVersion` sits directly under `semiplane:` (mirroring tech-stack §3.8's
ontology-pinning precedent) and pins the generation of the namespace shape:
**1** adds `spellcastingAbility`, the `multiclass` requirements gate, and this
marker itself. Every extraction rerun stamps it, so consumers can tell schema
generations apart without diffing every field; a note lacking the marker
predates versioning. Bump it — plus this doc's sample — whenever a field
changes shape.

### Feature recurrence (`op` / `refSlug`)

The same feature slug legitimately appears at several levels, and a bare
`{slug, level}` list is ambiguous for the wizard SPA: is level 13's
`improved-brutal-strike` a new feature, a replacement, or an upgrade? The
schema answers with one per-entry discriminator:

- **`op: value`** — this entry introduces the feature (fully specified there).
- **`op: extends`` + `refSlug: <slug>`** — this entry is *additive* onto the
  earlier entry of the same slug: new option entries, another independent
  pick, or a raised value. `refSlug` names the slot's first occurrence (the
  entry carrying `op: value`); the SPA reads that entry's level from the same
  list, so no extra field is needed to walk the chain.

Entries that appear exactly once carry neither field. The corpus stays in one
shape everywhere — XPHB's "Ability Score Improvement" (4/8/12/16, second
independent pick each time), "Subclass Feature" (6/10/14, a new addition to
the subclass pick), "Improved Brutal Strike" (13/17, extra options then a
raised damage die) all map to `value` at the first level and `extends` on
every later one — so separate `repeat`/`revalue` ops were not needed; level
ordering within a slot is recoverable from the entries themselves.

### Per-feature choices (`features[].choices`)

Option choices are carried structurally in the source JSON as
`{type: options, count: n, entries: [...]}` blocks inside a feature record's
entry tree. Where such a block exists, `features[].choices` carries a payload
reusing the established `ChoiceMeta {count, from}` shape (same shape as
`proficiencies.skills`): `count` names how many options the player picks, and
`from` resolves each option's name structurally — the first segment of a
`refClassFeature`/`refSubclassFeature`/`refOptionalfeature` ref (e.g.
"Careful Spell|XPHB" → "Careful Spell") or the `name` of an inline
`type: entries` option, with per-option `name` overrides winning. A block
without an explicit `count` means 1.

Real XPHB carriers (verified on the corpus): Cleric `Divine Order`@1
(count 1, Protector/Thaumaturge), Druid `Primal Order`@1 and
`Elemental Fury`@7, Fighter `Maneuver Options`@3 (count 3, from
`refOptionalfeature` refs), Sorcerer `Metamagic Options`@2 (count 2),
Warlock `Eldritch Invocation Options`@1.

Where structured data is absent, the field stays **absent** — no prose
parsing, per the tool's core rule. The deliberate XPHB Barbarian/Wizard gaps:
Brutal Strike's strike options ("Forceful Blow", "Staggering Blow" …) are
named `type: entries` sub-blocks, not `options` blocks (there is no typed
pick-count to copy), and Primal Knowledge / Epic Boon choices are prose
sentences only.

## SRD gate (`srd52`)

The tool is SRD-gated by default (`-srd=false` lifts it): the class record, each
subclass record, and every classFeature/subclassFeature record resolved from
refs must carry `srd52: true`, the corpus's redistribution flag (tech-stack §10,
"mixed SRD/non-SRD provenance per source tag gates what may be redistributed").
Effects for the test corpus:

- Barbarian@XPHB keeps only **Path of the Berserker** in its subclass pick list
  (the PHB/TCE/XGE entries are excluded).
- Wizard@XPHB keeps only **Evoker** among its XPHB subclasses (Abjurer,
  Diviner, and Illusionist are not flagged `srd52`); all wizard class features
  pass the gate, so `features`/`progression` stay complete.
- `-class Wizard@PHB` refuses with a pointer to `-srd=false` instead of
  silently emitting non-SRD data.

## Findings worth keeping

1. **`refSubclassFeature` indirection is the structural seam.** A subclass's
   level-N *banner* record ("Path of the Berserker" at 3) links its real
   features (`Frenzy`) via a `refSubclassFeature` entry inside its `entries`.
   Following that ref extracted the features without any prose parsing — this
   is the same "structural matches, never prose" stance as rules-ontology.
2. **Where the wizard's numbers live:** per-level scalars come from
   `classTableGroups` — *all* groups, not just the first. Every group's row
   aligns 1:1 to one class level; spell-slot tables present the same row shape
   under `rowsSpellProgression` (with a `title` like "Spell Slots per Spell
   Level"). Resource keys come from reading `colLabels` structurally: labels
   wrapped in a `{@filter Name|…}` tag unwrap to the readable name before the
   first `|`, so the wizard's table yields `cantrips`, `prepared-spells` and
   slot columns `1st`…`9th` with no hardcoded column list. Cell coercion
   covers the shapes the corpus exercises: bare JSON numbers (which decode as
   float64 — that was silently dropping the wizard's cantrips table),
   numeric strings, booleans, and `{type: bonus, value}` maps. Feature-
   comes-at-level comes from the ordered `classFeatures` refs, picks
   (skills/equipment/subclass choices) from `startingProficiencies`.
    `startingEquipment.entries` prose is deliberately *not* copied — the
   structured `defaultData` is the automation input.
3. **`featProgression` is a compact side-table**, not a table group:
   `[{name: "Epic Boon", category: [EB], progression: {19: 1}}]` maps to
   `semiplane.class.featProgression: [{level: 19, name: Epic Boon,
   category: [EB]}]`, so the wizard never re-derives where named feat grants
   land.
4. `_copy` stub records (e.g. reprints keeping page numbers) must be
   skipped — they duplicate a subclass with no gameplay data.
5. PB is not in the JSON; it is derived (`2 + (level-1)/4`) and emitted per
   level so the wizard never re-derives it.
6. **Recurrence is one homogeneous pattern.** Across all 12 class files,
   repeated feature slugs (Ability Score Improvement, Subclass Feature,
   Improved Brutal Strike, Action Surge 2/17, Mystic Arcanum 11–17…) are all
   additive on the earlier entry, so a single `value`/`extends` op — derived
   structurally from ref order, no per-class classification table — covers
   the whole corpus.
7. **Choices are typed, but thinly.** `options` blocks carry exactly a count
   and reference list; `preserve` flags on warlock options (e.g.
   `prerequisite` gating) are typed too — deliberately not carried in v0,
   since `{count, from}` is the only choice shape the namespace commits to.
8. **v1 additions ride typed keys only.** `spellcastingAbility` is a
   top-level typed key on caster records (wizard `int`, cleric/druid `wis`,
   bard/paladin/sorcerer/warlock `cha`) — non-caster records carry no key, so
   the YAML key stays absent. `multiclassing.requirements` lands as
   `multiclass.requirements` only when the record itself carries it: the 2024
   rules dropped the ability gate, so all twelve XPHB notes stay gate-less
   rather than borrowing PHB-era tables ({int: 13} on Wizard@PHB,
   {dex: 13, wis: 13} on Monk@PHB).
9. **Decoding druid surfaced an armor-list shape.** PHB-era
   `startingProficiencies` lists mix bare strings with
   `{proficiency: "shield", full: "…prose caveat…"}` objects; the typed decode
   owns it now via `profList()` (keeps strings verbatim, lifts the typed
   `proficiency` name, drops nothing typed rather than crash on the PHB
   record that shares the file with the XPHB record being extracted).
10. **The whole corpus is stamped, not just the first targets.** The initial
   run produced only 4 notes (Barbarian/Wizard, class + subclass); a full
   regen pass re-runs the tool per XPHB class so all 12 class notes and 12
   existing subclass notes carry `generatedFrom` + `schemaVersion: 1` and
   progression numbers (spell slots on casters, resources on non-casters).
   `classes.md` and any note the tool does not target stay untouched.

## Known gaps / next steps (if this graduates)

- Only shapes that Barbarian/XPHB exercise used to be handled; spell-progression
  table groups are inlined now. Still untouched: `equipmentType`
  ranges (PHB "any martial weapon"), `goldAlternative`, compound
  `primaryAbility` ("str or dex"), spellcasting classes' `additionalSpells`,
  and `{type: dice}` table cells (Bardic Die, Sneak Attack, martial-arts dice)
  — those have no scalar meaning until the dice-grammar layer consumes them,
  so they emit no resource key rather than guessed prose.
- Gameplay *effects* (Rage damage on crits, Brutal Strike options) stay as
  prose here on purpose — those belong in the rules-engine hook layer
  (rules-ontology) once extraction there is designed; frontmatter only carries
  creator-facing facts (picks, resources, progression).
- Subclass notes carry `op` discriminators tomorrow but no `choices` yet
  (neither Berserker nor Evoker has an `options` block; the boundary of
  v0 is `semiplane.class.features[].choices`). If a subclass later needs
  choices, `resolveSubclassFeatureNames` records are already at hand.
- The subclass *pick* itself (which of `subclasses[]` you take) is structured
  at class level but is not a per-feature `choices` payload — that seam lives
  in the pick list on purpose.
- License posture unchanged: everything lives in untracked `fixtures/`;
  a graduation PR would move the schema + tool to `tools/` proper and sample
  data to `testdata/`.

## Verification

```
go test ./tools/json2fm-5e   # merge idempotency, prose
                                            # preservation, ref resolution, srd gate,
                                            # cell coercion, spell-progression groups,
                                            # featProgression side-table,
                                            # recurrence ops, choices extraction,
                                            # spellcastingAbility, multiclass
                                            # requirements gate, schemaVersion stamping

# full regen, SRD gate default-on (all twelve XPHB records pass it):
for cl in Barbarian Bard Cleric Druid Fighter Monk Paladin Ranger \
          Rogue Sorcerer Warlock Wizard; do
  go run ./tools/json2fm-5e -class ${cl}@XPHB
done
# first pass: 24 notes updated (each class note + its existing subclass note)
# second pass: up-to-date ×24 — idempotent everywhere
```

The regen pass brings the ten pre-marker notes (monk, bard, cleric, …) onto
the current shape: they now carry progression numbers — spell-slot columns
(`1st` … `9th`) and `cantrips`/`prepared-spells` on casters, `rages`,
`focus-points`, `unarmored-movement` etc. on non-casters — with dice-text
cells (`1d6` martial arts, sneak attack) still intentionally absent. Older
frontmatter (`obsidianUIMode`, `cssclasses`, `tags`) and all prose survive
byte-for-byte.

## The flat-file namespaces: background, species, feat

*Added 2026-10-20, same tool, same merge/SRD/validation machinery.*

Classes live one file per class in `fixtures/data/class/`; backgrounds (157),
races (157) and feats (265) are flat single files (`backgrounds.json`,
`races.json`, `feats.json`). The tool now carries a kind per flag —
`-background Name@Source`, `-race Name@Source`, `-feat Name@Source` (one
target each, several kinds in one invocation; `-class ""` skips the class
kind) — plus per-kind note dirs `-bg-out` / `-race-out` / `-feat-out`
(defaults `fixtures/compendium/{backgrounds,races,feats}`). The target→note
mapping is the shared convention `<name>-<source>.md`
(`soldier-xphb.md`, `dwarf-xphb.md`, `boon-of-fate-xphb.md`), so no
note-directory inference is needed. An absent note is a data-only warn+skip,
matching missing subclass notes.

### Schema additions

Background note (`fixtures/compendium/backgrounds/soldier-xphb.md`):

```yaml
semiplane:
  background:
    name: Soldier
    source: XPHB
    page: 185
    ability:                 # carried as the source's typed blocks — never
      - choose:              # reinterpreted (every 2024 background in the
          weighted:          # corpus is exactly this two-block pair:
            from: [str, dex, con]   # the "+2/+1 pair" and the
            weights: [2, 1]         # "three +1s" alternative)
      - choose:
          weighted:
            from: [str, dex, con]
            weights: [1, 1, 1]
    skills:                  # source vocabulary verbatim; fixed grants are
      - athletics: true      # maps, so pickers keep their tokens instead of
    tools:                   # an invented name list ("anyGamingSet": 1 =
      - anyGamingSet: 1      # any one gaming set)
    originFeats:             # from the `feats` refs, linked like subclasses
      - name: Savage Attacker
        source: XPHB
        note: ./savage-attacker-xphb.md
    startingEquipment:       # identical {item, qty}/{gp} form to the class
      - id: A                # namespace (one shared parser now feeds both);
        items:               # background bundles are the AB letter options,
          - item: spear      # e.g. 14 GP = value 1400 → {gp: 14}, 50 GP → B
          - gp: 14
      - id: B
        items:
          - {gp: 50}
```

Species note (`fixtures/compendium/races/dwarf-xphb.md`) — deliberately the
thinnest namespace: `name/source/page`, `size` (source size codes,
`[M]`, `[S, M]`), `speed` (plain walk speed in feet), and
`languages` (same verbatim-token rule as background proficiencies, e.g.
`{auran: true}`). Traits stay prose — nothing is extracted from `entries`
(no NLP, per rules-ontology discipline). Darkvision/resistances and the like
are not carried.

Feat note (`fixtures/compendium/feats/boon-of-fate-xphb.md`):

```yaml
semiplane:
  feat:
    name: Boon of Fate
    source: XPHB
    page: 210
    category: EB             # the mechanic bucket straight from the source:
                             # EB epic boons, O origin, G general,
                             # FS/FS:P/FS:R fighting styles
    prerequisites:           # only where the typed JSON is machine-shaped;
      - level: 19            # benefits/effects stay prose
```

`prerequisites` maps one block to one record with exactly the terms the
corpus exercises (`FeatRequirement`): `level` (a required level),
`ability` (score floors, `{str: 13}`), `feature` (named grants it rides on,
e.g. Fighting Style feats), and `spellcasting: true` (the source's
`spellcasting2020` flag — the requirement is carried, not the era marker).
Several blocks are the source's way of listing alternatives
(Grappler → Str 13 *or* Dex 13, both with level 4). A block carrying any
unmodeled key (`campaign`, `feat`, `background`, `race`, `otherSummary`,
… — 15+ shapes exist across the 265 feats) is skipped **whole**, so a
partially-emitted requirement can never mean something it doesn't; a feat
whose only blocks are prose (`otherSummary`) ships no `prerequisites` at all.

### Origin-feat resolution

Background `feats` refs (`{"savage attacker|xphb": true}`) are lower-cased;
display names come from a deterministic title-case (`"savage attacker" →
"Savage Attacker"`), and the linked note remains the display authority.
Refs with a pick segment — `"magic initiate; cleric|xphb"` — keep the
variant as its own token (`variant: cleric`) rather than folding it into
the name. The note link (`note: ./savage-attacker-xphb.md`) is emitted only
when the file exists in `-feat-out`, like `SubclassRef.Note`.

### SRD gate vocabulary

All three flat files carry the same provenance flags as the class file, and
the tool gates on **`srd52`** for each kind, default-on, identical refusal
(`background Farmer@XPHB is not flagged srd52; rerun with -srd=false to
include it`), lifted collectively by `-srd=false`. The flag census matters
here: the sets that pass the gate are exactly the XPHB/SRD-5.2 notes that
already exist in the compendium — **4** backgrounds (Acolyte, Criminal,
Sage, Soldier), **9** species (Dragonborn, Dwarf, Elf, Gnome, Goliath,
Halfling, Human, Orc, Tiefling — the 2024 species minus Aasimar, which
carries no `srd52`), and **17** feats (the two Ability Score Improvements'
level-4 set, the six SRD boons, the four fighting styles, and the four
origin feats). 2014-era flags (`srd`, `basicRules`) do not pass the gate,
same as they don't for classes.

### Deliberate v0 gaps (flat kinds)

- `equipmentType` prose picks ("any gaming set") in background bundles emit
  no item — the prose bullet on the note remains the authority (same hole as
  the class namespace).
- Composite speeds (`{walk: 20, fly: 50}`, `{climb: true}`) and size
  descriptions are skipped, not parsed; the gated corpus is all plain `int`
  speeds, and the warn surfaces any future shape.
- Species without `languageProficiencies` in the JSON (all nine gated ones —
  2024 languages live in the character-creation rules, not per species) ship
  no `languages` field; it appears only where the source actually carries
  it (2014 species, e.g. `{auran: true}`).
- Feat prerequisite terms beyond the four modeled ones stay out entirely;
  extending `prerequisiteDigest` is a widening, not a rewrite.
- Feat `ability` score increases (`skillProficiencies`, `expertise` grants)
  and `additionalSpells` are untouched — v0 is identity + category +
  prerequisites for feats, per the same "creator-facing facts" boundary.

## Verification (flat kinds)

```
go test ./tools/json2fm-5e   # + background/species/feat shape
                                            # extraction, origin-variant
                                            # resolution, prerequisite digest
                                            # (+ unmodeled-block skipping),
                                            # per-kind srd gate, flat-namespace
                                            # merge idempotency & replacement

# full flat regen, gate default-on (class kind skipped):
for bg in acolyte criminal sage soldier; do
  go run ./tools/json2fm-5e -class "" -background ${bg^}@XPHB
done
for rc in dragonborn dwarf elf gnome goliath halfling human orc tiefling; do
  go run ./tools/json2fm-5e -class "" -race ${rc^}@XPHB
done
# feats (17 gated record names, all with existing notes)
# first pass: 30 notes updated; second pass: up-to-date ×30

# spot-verify (the task's three targets):
go run ./tools/json2fm-5e -class "" -background Soldier@XPHB
#   → ability blocks + skills/tools tokens + originFeats Savage Attacker +
#     equipment {gp: 14}/{gp: 50}, up-to-date ×2
go run ./tools/json2fm-5e -class "" -race Dwarf@XPHB
#   → size [M], speed 30, no languages (absent in JSON), up-to-date ×2
go run ./tools/json2fm-5e -class "" -feat "Boon of Fate@XPHB"
#   → category EB, prerequisite level 19, up-to-date ×2

# gate refusals (parity with -class Wizard@PHB):
go run ./tools/json2fm-5e -class "" -race Aasimar@XPHB        # refuses
go run ./tools/json2fm-5e -class "" -feat "Boon of Blazing Dawn@ABH"  # refuses
```

## The typed spell / optional-feature namespaces

*Added 2026-10-21 (second batch of the same workday), same tool, same
merge/SRD-gate/validation machinery.*

The two largest remaining kinds: **spells** (391 XPHB records,
`fixtures/data/spells/spells-xphb.json`, notes under
`fixtures/compendium/spells/`) and **optional features** (213 records,
`fixtures/data/optionalfeatures.json`, notes under
`fixtures/compendium/optional-features/`). New modes:

```
go run ./tools/json2fm-5e -class "" -spell "Fireball@XPHB"  # smoke target
go run ./tools/json2fm-5e -class "" -spell-all             # bulk (count summary)
go run ./tools/json2fm-5e -class "" -optfeature "Subtle Spell@XPHB"
```

One-spell-per-flag does not scale, so `-spell-all` walks the whole
`spells/spells-xphb.json` corpus: the spell JSON and the class-list join are
parsed **once**, each note is read once per pass, and the run ends with
`bulk spells: N updated, M up-to-date, K not flagged srd52, W without a note`.
Optional features stay one-flag-per-note (29 gated notes, no bulk circuit
needed — the flag set that exists is exactly what's gated).

### Schema additions

Spell note (`fixtures/compendium/spells/fireball-xphb.md`):

```yaml
semiplane:
  spell:
    name: Fireball
    source: XPHB
    page: 274
    level: 3
    school: V            # the source's one-letter code (A/C/D/E/I/N/T/V), verbatim
    time:
      - {number: 1, unit: action}
    range:
      type: point
      distance: {type: feet, amount: 150}
    duration:
      - {type: instant}
    components:
      v: true
      s: true
      m:
        text: a ball of bat guano and sulfur
    savingThrow:
      - dexterity
    classes:             # from the gendata join, never from note prose
      - {name: Sorcerer, source: XPHB}
      - {name: Wizard, source: XPHB}
    subclasses:
      - name: College of Lore
        shortName: Lore
        source: XPHB
        class: {name: Bard, source: XPHB}
        note: ../classes/bard-xphb-college-of-lore-xphb.md   # when the note exists
```

`entries` and `entriesHigherLevel` prose stays out of the namespace entirely,
per the tool's core rule.

Optional-feature note
(`fixtures/compendium/optional-features/subtle-spell-xphb.md`):

```yaml
semiplane:
  optionalFeature:
    name: Subtle Spell
    source: XPHB
    page: 142
    featureType: [MM]            # Metamagic / Eldritch Invocation / Pact Boon…
    consumes: {name: Sorcery Point}
```

### The spell shapes, as verified on the corpus

- **Casting time** blocks are `{number, unit}`; a handful carry a `condition`
  (every smite spell's trigger) or a `note` (Plant Growth's
  Overgrowth/Enrichment modes). The condition/note is typed source text, not
  prose-parsed — it is carried verbatim **with** the block, because dropping
  it would half-read the casting rule ("1 bonus action" without the
  when-only-after-riding-a-hit trigger).
- **Range** is `{type, distance{type, amount}}`. Amount is present only for
  measurable types (feet ×251, miles ×6); touch/self/unlimited/sight carry
  the type alone. `type` also covers the AoE spell shapes
  (emanation/cone/sphere/cube/line beyond plain `point`).
- **Duration** is `{type: instant|timed|permanent|special,
  duration{type: amount, unit}, concentration, ends}` — only `timed` blocks
  carry the span, `concentration: true` rides 160 records (never false in
  the corpus, so the key is present only when true, like every other flag),
  and `ends: [dispel]`/`[dispel, trigger]` survives on the permanent ones.
- **Components** are `{v, s, m}`. `m` comes in three shapes: a plain string
  (the majority — carried verbatim as `text`), the typed
  `{text, cost, consume}` (Arcane Lock's gold dust — `cost` rides the
  source's copper units; gp converts like every other equipment value in the
  namespace, so 2500 cp → `gp: 25`), and `null`. Null means the note says
  "V, S" with no M, so the `m:` key is absent, not `m: null`.
- **Saving throw**: the ability list, where the record carries one (154 of
  391: wisdom/dexterity/constitution… lowercase keys verbatim).
- **Damage deliberately absent everywhere in this corpus.** The task brief
  expected `damage` objects on some records with Fireball's being `null`;
  what the file actually shows is the stricter version — **no spell record
  in spells-xphb.json carries a top-level `damage` key at all** (the ~18
  spells that carry dice do it through `entries` prose + a
  `scalingLevelDice` table; Fireball's `{@damage 8d6}` lives in prose only).
  So the extractor models the shape (`{dice, damageType, bonus}`, never
  parsed out of prose, unmodeled keys skip the block whole) and the
  namespace key is consciously absent on all 339 gated notes; other source
  files (PHB/TCE/…) may exercise it, `damageInflict`-free v0 stays that way.

### The class-list join (`gendata-spell-source-lookup.json`)

The spell records carry **no classes key**; the join is mandatory.
`fixtures/data/generated/gendata-spell-source-lookup.json` is keyed by
lowercase spell name inside a lowercase source section, with two wrinkle
shapes: class entries arrive as `true` or as
`{definedInSources: ["SatO"]}` objects (SatO-era spells) — both name the
class, so both include — and subclass entries are four-level
`classSource → class → subSource → shortName` nests whose leaf carries the
`{name}`. Subclass note links resolve into the classes dir when the note
exists (`note: ../classes/…`, relative to the spell note); the whole join is
a *name-level* pass-through, so the srd gate applies to the spell record
itself, not to class/subclass *names* the note's own tags already carry.
Deterministic sorted output (by source, then name) is what makes a rerun a
no-op.

### `srd52` on spells is not a boolean

Census across the 391 records: **322 bool true, 17 strings, 52 nulls**. A
string (e.g. `srd52: "Arcane Hand"` on Bigby's Hand) means "published in SRD
5.2 under a rename", and the compendium note carries that name (aliases
included). The extractor gates on all three shapes (null = not flagged) and
publishes the record under the renamed entry's name; the join key stays the
source name (`"bigby's hand"`), which is why the join lookup is keyed
lowercase-by-source-name, separate from the note's published name.

### Note-name spelling drift (apostrophes)

The compendium's spell and optional-feature notes were published with
apostrophes *stripped*, not slug-mangled: `dragons-breath-xphb.md`,
`hunters-mark-xphb.md`, `devils-sight-xphb.md` (not the tool's
`dragon-s-breath` convention). The note resolver now tries the shared
`<slug>-<src>.md` convention first and falls back to the
apostrophe-stripped form — the primary convention wins whenever its note
exists, so nothing else in the tool (feature slugs, class subdir note
names) changes shape. Without the fallback, Dragon's Breath / Hunter's Mark
/ Devil's Sight would silently stay unstamped.

### Optional-feature prerequisite shapes (all verified in-file)

`prerequisite` blocks reduce the way the feat namespace does —
**unmodeled key ⇒ whole block skipped**, never partially read. The terms
modeled from the corpus:

- `level` — always a dict `{level: N, class{name, source, visibleStats?},
  subclass?}` — emitted as `level` + the granting class ref (the UI hint
  `visibleStats` stays out); the gated records exercise Warlock@XPHB at
  levels 2/5/9/15.
- `optionalfeature` — lowercase refs (`"pact of the blade|xphb"`) resolved to
  their display names from this file's own records (title-case fallback,
  same as background origin feats). Lifedrinker/Thirsting Blade → Pact of
  the Blade; Eldritch Smite → pact of the blade, etc.
- `pact` — the bare token ("Tome", "Chain", "Blade") from PHB/TCE/XGE-era
  records, verbatim.
- `spell` — only the PHB-era *string-list* form
  (`["eldritch blast#c"]`) is carried. The XPHB-era form pairs a real level
  block with a typed cantrip-filter *object*
  (`{choose: "level=0|class=Warlock", entry: "…a Warlock Cantrip That Deals
  Damage", entrySummary}`) — since an alternative-pick filter with prose
  entries is not a machine fact the namespace can commit to, those blocks
  are **skipped whole**: Agonizing Blast, Eldritch Spear and Repelling Blast
  ship no `prerequisites` even though each carries a level-2 Warlock block.
  This is the same boundary as the feats' `otherSummary` case (recorded
  there), extended to `item` (attunement prose) which is also skipped.

`consumes` reduces to `{name, amount?, amountMin?, amountMax?}` (Extended/
Quickened/Heightened Spell's `amount: 1/2`); `featureType` tokens
(EI ×82, MM ×20, ED, AS, AI, PB, MV:B, RN, RP, FS:*) pass through verbatim —
the class association rides the prerequisite level blocks, which is exactly
why Subtle Spell (metamagic has no level gate) carries no class ref and no
prerequisite key at all.

## Verification (spell / optional-feature)

```
go test ./tools/json2fm-5e   # + spell shape extraction, prose
                                            # exclusion, m:null omission, join
                                            # reduction (bool × definedInSources),
                                            # subclass sort & note links, damage
                                            # only-when-carried (and null /
                                            # unmodeled skip), srd52 rename +
                                            # note-path fallback, min corpus-id
                                            # bulk (5 updated → 5 up-to-date,
                                            # byte-identical second pass),
                                            # optional-feature prerequisite
                                            # digests (incl. whole-block skips),
                                            # PHB-era terms (gate lifted);

# corpus runs, SRD gate default-on:
go run ./tools/json2fm-5e -class "" -spell-all
# first pass: 339 updated, 52 not flagged srd52, 0 without a note
# second pass: 0 updated, 339 up-to-date — idempotent everywhere, zero warns

# the 29 gated optional features, run by name (same while-read loop as the
# flat kinds' regen above; names are the census list below the srd gate):
while IFS= read -r t; do
  go run ./tools/json2fm-5e -class "" -optfeature "$t"
done < <(python3 -c 'import json
d=json.load(open("fixtures/data/optionalfeatures.json"))
print("\n".join("%s@%s" % (s["name"], s["source"]) for s in d["optionalfeature"] if s.get("srd52")))')
# first pass: 28 updated + 1 via the apostrophe fallback (Devil's Sight)
# second pass: up-to-date ×29

# spot-verify (the task's three targets):
go run … -spell "Fireball@XPHB"      # join → Sorcerer+Wizard, subclasses with
                                     # ../classes note links, damage absent
go run … -spell "Acid Splash@XPHB"   # cantrip (level 0), m: null → key absent
go run … -optfeature "Subtle Spell@XPHB"  # MM token, Sorcery Point, no prereq
```

### Deliberate v0 gaps (spell / optional-feature)

- No `damageInflict`/`conditionInflict`/`spellAttack`/`abilityCheck`/
  `meta`/`miscTags`/`areaTags` — not in the scope's machine set, and tags
  already ride the note's Obsidian tags.
- Spell notes under other sources (PHB/TCE/XGE…) are supported by the
  single-spell mode (`-spell Name@Source` + the join's per-source section)
  but the SRD-5.2 set is the only compendium inventory in v0;
  `-spell-all` walks `spells-xphb.json` by definition.
- Join `subSubclasses` (Circle of the Land → Arid/Tropical Land) collapse to
  the parent subclass entry — the note inventory is parent-level.
- The optional-feature bulk mode was deliberately not added: 29 notes, no
  scale pressure; widening later is a loop-shaped change, not a schema one.

## 5. Authored system notes (not extractor output)

The attribute-methods data the creator needs cannot be extracted — it must be
authored (tech-stack §3.4: point-buy rules come from Markdown; §10: system
packs live at `rules/systems/<id>/`). First of these:
`testdata/vaults/facets/rules/systems/dnd5e-2024/charcreation.md` — the tracked
curated-vault location mirrors a real deployment's vault path exactly, so the
facet layer sees it as a real system note would be seen.

- `semiplane.charcreation.methods[]`: `point-buy` (budget 27, score range
  8–15, canonical SRD 5.2 costs 8=0…15=9) and `standard-array`
  (15/14/13/12/10/8).
- `semiplane.charcreation.increases`: background grants +2/+1 (`weights:
  [2,1]`) — species grant nothing under the 2024 rules, mirroring the
  extractor's empty `ability` on XPHB species notes.
- `generatedFrom: authored/...` distinguishes hand-maintained namespaces from
  extractor output (which always points at a source JSON path).

Invariants live in `internal/index/charcreation_test.go`: canonical cost
table, score range, array shape, and the cross-method property that the
standard array spends exactly the 27-point budget (9+7+5+4+2+0). The note is
hand-written, so drift in it can only be caught by tests — the same
vocabulary-refusal philosophy the extractor applies to unknown records.
