# `tools/json2fm-5e` — 5e.tools JSON → `semiplane:` frontmatter

Structurally extracts gameplay data from the 5e.tools-typed source JSON into a
machine-readable `semiplane:` frontmatter namespace on the matching compendium
Markdown notes. The namespace is read-side input for the SQLite facet layer
(`internal/index`, `?facet=semiplane.<kind>`) and, downstream, the character-
creator SPA (tech-stack §3.4) — notes remain the user-facing surface, the
typed JSON the automation input.

**Dev tool, out of every ship path:** `make build`/`make cross` and CI compile
only `./cmd/semiplane`; nothing here is embedded, released, or vendored. It
consumes only stdlib + `gopkg.in/yaml.v3` (no `internal/*` imports) and reads
and writes only `fixtures/` — the license-unresolved 5e.tools corpus, which is
untracked and never committed. Every extracted record must carry `srd52: true`
(the redistribution flag); the gate is what makes default use safe.

*Status: maturing experiment, not an engine dependency. The namespace contract
below is the thing worth reviewing, not the tool's internals.*

## Write contract (invariants)

These hold for every kind the tool processes:

- **Marker-scoped merge.** Only the top-level `semiplane:` key inside the
  note's *leading frontmatter* is replaced — prose, Obsidian tags,
  `cssclasses`, and every other frontmatter key survive byte-for-byte.
- **Leading-frontmatter placement.** The namespace lives in the lead
  frontmatter doc, not the body — `internal/index` reads leading frontmatter
  only, so body-appended namespaces are invisible to the facet layer.
- **Idempotent.** Deterministic, always-sorted output; a second run reports
  `up-to-date` everywhere and writes nothing. This is the regression matrix:
  every kind is tested byte-identical on the second pass.
- **Validate before write.** The merged frontmatter is re-parsed with yaml.v3;
  an invalid result aborts rather than corrupting the note.
- **`schemaVersion` pinned.** Sits directly under `semiplane:` (tech-stack
  §3.8's ontology-pinning precedent). Consumers tell schema generations apart
  without diffing every field; a note lacking the marker predates versioning.
  Bump it — plus this doc's samples — whenever a field changes shape.
- **Structural extraction only.** Nothing is parsed out of prose (`entries`
  text never reaches the namespace). Unstructured facts stay *absent*, never
  guessed: an unmodeled prerequisite block is skipped whole, a `damage` field
  the record doesn't carry emits no key. Unknown vocabulary fails loudly or
  stays out — the same discipline as rules-ontology.
- **Provenance, never cross-edition inference.** Facts are emitted only from
  the record being extracted. The 2024 rules dropped the multiclass ability
  gate, so XPHB notes carry no `multiclass` block even though the PHB-era
  records in the same files carry `{int: 13}`.

## Usage

One flag per **kind**, value `Name@Source`; several kinds may combine in one
invocation. Notes resolve via the shared convention `<name>-<source>.md`
(`soldier-xphb.md`) under the matching output dir; a missing note is a
warn+skip (data-only run), matching missing subclass notes.

| Flag | Default | Meaning |
|---|---|---|
| `-class` | `Barbarian@XPHB` | class (and its subclasses) — *non-empty by default*; set `-class ""` to skip when running other kinds |
| `-background` / `-race` / `-feat` / `-spell` / `-optfeature` | `""` | one record each, `Name@Source` |
| `-spell-all` | off | bulk mode: every record in `spells/spells-xphb.json`, one JSON+join parse, count summary |
| `-srd` | `true` | keep only `srd52`-flagged records (license gate; `-srd=false` lifts) |
| `-dry-run` | off | report what would change, write nothing |
| `-print` | off | print the generated YAML namespace to stdout |
| `-data` / `-out` / `-bg-out` / `-race-out` / `-feat-out` / `-spell-out` / `-optfeature-out` | `fixtures/…` | source JSON root and per-kind compendium note dirs (also where sibling note links resolve) |

```
go run ./tools/json2fm-5e -class Wizard@XPHB          # class + subclasses
go run ./tools/json2fm-5e -class "" -feat "Boon of Fate@XPHB"
go run ./tools/json2fm-5e -class "" -spell-all        # bulk: 339 gated spells
```

Corpus full-regen (gate default-on; loops live in git history until the
extractor ships, the counts here are the current census):

| Kind | Notes | Source |
|---|---|---|
| classes + subclasses | 24 | `class/class-*.json` (12 XPHB classes) |
| backgrounds | 4 | `backgrounds.json` (of 157; only 4 pass the gate) |
| species | 9 | `races.json` (of 157) |
| feats | 17 | `feats.json` (of 265) |
| spells | 339 | `spells/spells-xphb.json` (391 records; 52 refuse) |
| optional features | 29 | `optionalfeatures.json` (of 213) |

## Schema (schemaVersion: 1, dnd5e-scoped)

Every namespace sits under `semiplane:` with `schemaVersion` and
`generatedFrom` (the source JSON path; `authored/...` marks hand-maintained
notes — see §Charcreation). The extractor cannot touch notes it does not
target, e.g. `classes.md` index notes stay untouched.

### `semiplane.class` (see `fixtures/compendium/classes/barbarian-xphb.md`)

```yaml
semiplane:
  schemaVersion: 1
  generatedFrom: class/class-barbarian.json
  class:
    name: Barbarian
    source: XPHB
    page: 50
    primaryAbility: [str]
    saves: [str, con]
    spellcastingAbility: int   # casters only (wizard int, cleric/druid wis,
                               # bard/paladin/sorcerer/warlock cha); non-caster
                               # notes omit the key entirely
    hp:
      hitDie: 12
      firstLevel: "12 + @con"  # dice-grammar strings; @con = CON modifier —
                               # evaluable via dice.EvalWithVars
      perLevel:
        roll: "1d12 + @con"
        rollMin: 1             # floor for a bad die, level-ups never lose HP
        flat: "7 + @con"       # take-the-average alternative = hd/2+1
    proficiencies:             # armor/weapons lists + skills {count, from}
    multiclassProficiencies:   # what you gain when multiclassing in/out
    multiclass:                # ability gate on multiclassing IN (PHB-era
      requirements: {int: 13}  # records); 2024 dropped it, so gated XPHB notes
                               # carry no block — never cross-edition inferred
    startingEquipment:         # quantities separate from names; {gp} options
      - id: A                  # (shared parser with backgrounds)
        items:
          - {item: greataxe}
          - {item: handaxe, qty: 4}
      - id: B
        items: [{gp: 15}]
    subclassTitle: Barbarian Subclass
    subclasses:                # gated pick list; note link when it exists
    features:                  # ordered {name, slug, level}; slug = stable id;
      - name: Ability Score Improvement   # plus per-entry discriminators:
        slug: ability-score-improvement     #   op: value       = introduces it
        level: 8                            #   op: extends     = additive on
        op: extends                         #     refSlug's first occurrence
        refSlug: ability-score-improvement  #   choices payloads (below)
    featProgression:           # named feat grants: {level, name, category[EB]}
    progression:               # per level: pb, feature slugs, and every
      - level: 1               # class-table column inlined under its
        pb: 2                  # label-slug: rages / rage-damage /
        features: [rage]       # weapon-mastery; casters additionally carry
        rages: 2               # cantrips, prepared-spells, and 1st…9th slots
        rage-damage: 2
```

**Recurrence (`op`/`refSlug`)** — a bare repeated slug is ambiguous (new?
replacement? upgrade?). One discriminator covers the whole corpus: first
occurrence carries `op: value`, every later one `op: extends` +
`refSlug` naming that first entry; single-occurrence entries carry neither.
Level ordering recovers the chain, so no `repeat`/`revalue` ops exist.

**Choices (`features[].choices`)** — where the source carries typed
`{type: options, count, entries}` blocks, the payload reuses the `ChoiceMeta
{count, from}` shape (`count` = picks, `from` = option names resolved
structurally from refs or inline option names). Verified carriers: Cleric
`Divine Order`, Druid `Primal Order`/`Elemental Fury`, Fighter `Maneuver
Options` (3), Sorcerer `Metamagic Options` (2), Warlock invocation lists.
Absent where the JSON is prose — Barbarian `Brutal Strike` options are typed
`entries` sub-blocks *with no pick count*, Primal Knowledge/Epic Boon are
prose sentences, so those carry no `choices` and stay prose.

### `semiplane.subclass`

`{name, shortName, source, page, class{name, source, note}, features[{name,
slug, level}]}` — resolved through `refSubclassFeature` indirection (the
subclass's level-3 banner record links its real features; Berserker → Frenzy@3
…) with `_copy` stub records skipped.

### `semiplane.background`

```yaml
  background:
    name: Soldier
    source: XPHB
    page: 185
    ability:                  # source's typed blocks verbatim, never
      - choose:               # reinterpreted — 2024 backgrounds are the
          weighted:           # "+2/+1 pair" and the "three +1s" alternative
            from: [str, dex, con]
            weights: [2, 1]
    skills: [{athletics: true}]   # source vocabulary verbatim (pickers keep
    tools: [{anyGamingSet: 1}]    # their tokens; unmodeled prose stays out)
    originFeats:              # `feats` refs, linked like subclasses;
      - name: Savage Attacker # refs with pick segments keep the variant
        source: XPHB          # (`variant: cleric` on Magic Initiate)
        note: ./savage-attacker-xphb.md
    startingEquipment:        # identical {item, qty}/{gp} form to classes
```

### `semiplane.species`

Deliberately the thinnest: `{name, source, page, size([M]), speed(int feet),
languages}` — languages only where the source carries them. Traits,
darkvision, resistances stay prose (no NLP). 2024 species carry no ability
scores; the namespace mirrors that absence.

### `semiplane.feat`

`{name, source, page, category(EB/O/G/FS…), prerequisites[]}`.
Prerequisites reduce **one typed block per candidate, skipped whole** if any
key is unmodeled — modeled terms: `level`, `ability` floors, `feature`,
`spellcasting: true`. A feat whose blocks are prose (`otherSummary`) ships no
`prerequisites` at all; partial requirements can never mean something they
don't. Ability increases / skill grants stay out (v0 = identity + category +
prerequisites).

### `semiplane.spell`

```yaml
  spell:
    name: Fireball
    source: XPHB
    page: 274
    level: 3
    school: V                 # source one-letter code (A/C/D/E/I/N/T/V)
    time:  [{number: 1, unit: action}]       # + condition/note verbs when typed
    range: {type: point, distance: {type: feet, amount: 150}}
    duration: [{type: instant}]              # timed/permanent shapes typed;
    components: {v: true, s: true,           # concentration only when true
                 m: {text: …, cost: …}}      # m null → key absent (V, S)
    savingThrow: [dexterity]
    classes:                  # from the gendata join — never note prose;
      - {name: Sorcerer, source: XPHB}     # bool + {definedInSources} both pass
    subclasses:
      - {name: College of Lore, shortName: Lore, source: XPHB,
         class: {name: Bard, source: XPHB},
         note: ../classes/bard-xphb-college-of-lore-xphb.md}
```

- The **class-list join is mandatory**: spell records carry no classes key;
  `fixtures/data/generated/gendata-spell-source-lookup.json` provides it,
  keyed lowercase-by-source-name (ahead of the `srd52`-rename resolution
  below).
- **`srd52` is not always boolean** (census: 322 bool / 17 strings / 52 null
  in the XPHB corpus): a string means "published under a rename" and the note
  publishes the renamed name while the join key stays the source name.
- Note-name **apostrophe fallback**: the compendium strips apostrophes
  (`hunters-mark-xphb.md`), so the shared convention is tried first, then the
  stripped form — the fallback path.
- **`damage` is modeled but deliberately absent everywhere here** (no XPHB
  spell record carries the key; dice live in prose + `scalingLevelDice`
  tables). Other source files may exercise it — modeled, never parsed from
  prose.

### `semiplane.optionalFeature`

`{name, source, page, featureType[EI/MM/…], consumes{name, amount?},
prerequisites[]}`. Prerequisite terms modeled from the corpus: `level` (+ the
granting class ref; the gate that names the class), `optionalfeature` (refs
resolved to display names), `pact` tokens; string-list spell refs carried,
typed filter objects skipped whole (same boundary as feats).

### `semiplane.charcreation` (authored, not extracted)

The one **hand-maintained** namespace: attribute-method rules cannot be
extracted (tech-stack §3.4 — point-buy rules come from Markdown; §10 — system
packs at `rules/systems/<id>/`). Live note:
`testdata/vaults/facets/rules/systems/dnd5e-2024/charcreation.md` with
`methods[]` (point-buy budget 27 / costs 8=0…15=9; standard array
15/14/13/12/10/8) and `increases` (background +2/+1). Drift is guarded by
invariants in `internal/index/charcreation_test.go` (canonical cost table;
the standard array spends exactly the budget) — regeneration by a tool is not
possible, so tests are the regression net.

## Limitations & deliberate gaps

- **Prose-only mechanics stay prose.** Rage damage on crits, Brutal Strike
  options, Primal Knowledge, Epic Boon picks: effects belong to the
  rules-engine hook layer (rules-ontology), not frontmatter. Frontmatter
  carries creator-facing facts only (picks, resources, progression).
- **Die-text table cells emit nothing.** `{type: dice}` cells (Bardic Die,
  Sneak Attack) have no scalar meaning until the dice-grammar layer consumes
  them — no resource key rather than guessed prose.
- **Equipment prose picks** (`equipmentType` "any martial weapon") emit no
  item in any kind; `goldAlternative` (bundle B as raw gold) is unextracted.
- **Spell damage** modeled, absent from this corpus (see above);
  `damageInflict`/`conditionInflict`/`spellAttack`/areaTags not carried —
  Obsidian tags already ride the notes.
- **Inventory is bound to the notes:** `-spell-all` walks `spells-xphb.json`
  by definition; other sources work via single flags, but the SRD-5.2 set is
  the only compendium spell inventory the notes exist for. Bulk reruns are
  the idempotency check of record.
- **Choices payload boundary is `class.features[]`**; subclass features carry
  `op` but not `choices` yet; the subclass *pick* lives in the class-level
  list by design.
- **No optional-feature bulk mode** (29 notes, no pressure); widening later
  is loop-shaped, not schema-shaped.
- **Known shape class, unhandled:** compound `primaryAbility` ("str or dex"),
  `additionalSpells`, species composite speeds — warned, never guessed.

## Verification

```
go test ./tools/json2fm-5e
```

Covers per kind: shape extraction, prose exclusion, merge idempotency
(byte-identical second pass), ref resolution & note links, srd gate +
`-srd=false` refusal paths, cell coercion, spell-progression groups,
choices/op/recurrence discriminators, prerequisite whole-block skipping, join
sorting/rename resolution, and the schemaVersion stamp. Corpus-level
regen/spot-verify commands and current counts live with the kind they verify;
the writes themselves land only in `fixtures/` (untracked), never `testdata/` —
curated, tracked examples belong in `testdata/vaults/` (facet + charcreation
tests read those).

## Consumers

- `internal/index` — `note_facets` derived table + `FacetPaths`, rebuilt by
  `semiplane reindex`; `internal/api/notes.go` answers
  `?facet=semiplane.<kind>` on the notes list (contract-checked against
  `openapi.yaml`).
- The future creator SPA reads `meta.semiplane` via the note API.

Related: tech-stack §3.2 (dice grammar + `@var`s), §3.4 (creator wizard),
§5 (frontmatter namespace contract), §5/§10 (SRD gating + vault layout),
rules-ontology (the no-prose discipline this tool inherits).
