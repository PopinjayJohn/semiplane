# Rules Ontology (canonical — referenced by tech-stack.md §3.8)

Status: accepted 2026-10-16 (graduated from spike; v0-first-draft layering fixed).
Closed vocabularies, semver'd as core `ontology v0` + named system packs.

Reading guide: **Part I is the whole contract every system shares.**
Part II is *one* pack profile (`dnd5e-2024`) — normative for D&D content,
irrelevant otherwise. Part III is how to author another pack. If a sentence
names a die size, an attribute, or a rest, it lives in Part II or III, never I.

---

# Part I — Core (system-neutral; lives in the binary)

## 0. Non-negotiables (testable invariants, no game rules)

1. **Structural triggers only.** A hook fires iff its `on` event kind matches
   a canonical event and its `when` expression evaluates true over typed
   context. No prose, regex, or NLP at roll time. All prose→structure work
   happens at ingest (extraction + human review + type-check + seeded
   simulation).
2. **Refusal is pack-relative.** Unknown event kind, state path, `do` verb,
   slot, tier, condition key, attribute, or source at authoring time is a hard
   error — judged against the vault's **active system pack**, so the same
   hook can be valid in one system and refused in another. Never guessed at
   roll time.
3. **Damage contributions carry their own slot.** Every `add_damage` op
   declares `slot: dice | flat`, and the breakdown always renders dice and
   flats as separate lines. What a critical hit *does* to each slot is a
   **pack rule** (dnd5e: dice roll twice, flats never double) — the core only
   guarantees the slot is declared, ordered by pack precedence, and auditable.
4. **Pure reducers.** `hook(state, event) → (state, effects)`. Seeded RNG
   threaded through, no wall-clock, no I/O. Same seed + same state = same trace.
5. **Dice specs are event-level, never hook modifiers.** Anything that changes
   how many dice are rolled or kept (advantage, disadvantage, bonus dice,
   keep-highest) is computed by the engine from event fields + pack rules.
   Hooks add contributions; they never touch the dice.

## 1. Event envelope and kind set

Every event, every system:

```yaml
id: evt_01J...        # ulid, assigned by engine
kind: attack          # closed core enum below
round: 3
turn_actor: throg
caused_by: evt_...    # optional causal parent
payload: {...}        # kind-specific struct (typed, §3)
```

| # | kind | emitted when | core payload shape |
|---|------|--------------|--------------------|
| 1 | `attack` | an attack roll is made | `attacker, target, used_ability, source, adv, dis, kept, discarded?, total, hit, crit` |
| 2 | `damage` | damage rolled/applied (follows a hit `attack`, or standalone) | `source, attacker?, target, base_dice, contributions[], total` |
| 3 | `check` | trait/skill check | `actor, attribute, specialty?, difficulty?, adv, dis, total, success?` |
| 4 | `save` | resist/defense roll | `actor, attribute, difficulty?, adv, dis, total, success?` |
| 5 | `cast` | spell cast (committed, before anything it causes) | `caster, spell, cost?, targets[]` |
| 6 | `condition_apply` | named effect starts | `target, condition, until?, source_hook` |
| 7 | `condition_remove` | effect ends early / expires / is consumed | `target, condition, reason` |
| 8 | `resource_spend` | pool debit (ammo, charges, stress, slots) | `actor, resource, amount, before, after` |
| 9 | `rest_short` | short recovery | `actor, resources_regained[]` |
| 10 | `rest_long` | full recovery | `actor, resources_regained[]` |
| 11 | `turn_start` / `turn_end` | turn boundaries, paired by `round/turn` | `actor, round` |
| 12 | `action_spend` | action-economy debit | `actor, cost, remaining` |
| 13 | `move` | requested movement | `actor, from, to, distance, provokes?` |
| 14 | `line_of_sight` | visibility query (pure; no mutation) | `viewer, target, blocked?` |
| 15 | `adjacency` | closeness query (pure) | `a, b, within, adjacent?` |
| 16 | `area` | template query (pure) | `origin, shape, size, caught[]` |

Notes:

- Packs use a **subset**: a system with no rests never emits `rest_*`; one
  with no positions skips 13–16. Unused kinds cost nothing.
- Field *names* are core; field *value domains* marked † are pack-defined:
  `used_ability` values, `source` values, `condition` keys, `cost`/`resource`
  names, distance units, difficulty scales.
- `attack` and `damage` are separate events with a causal link
  (`damage.caused_by = attack.id`). Whether crit exists, what determines it,
  and what it does to dice are all pack rules; the core only propagates the
  `crit` flag.
- VTT-era queries (13–16) return facts; side-effects happen only via explicit
  `condition_apply` / `action_spend` / `damage` follow-ups.
- `cast` never rolls dice; the save/attack it forces is a child event.

## 2. State minimum

```yaml
actors:
  throg:
    id: throg
    name: Throg
    traits: {...}          # † pack-shaped (scores, dots, approaches…)
    pools: {...}           # † named resources (hp, stress, slots…)
    conditions:            # † pack-closed keys
      - {key: <pack-key>, until: <expiry>, source: <hook-id>}
    position: {...}        # optional; absent = theatre-of-mind
turn: {round: 3, active_actor: throg}
```

Conventions:

- Hooks read any path but write only via `do` verbs (§4). Unknown paths fail
  validation against core schema + pack trait/pool names.
- `conditions[].key` is closed **per pack** (homebrew adds keys via pack
  minor bump, never free text at roll time).
- `until` is structured (`turn_start:<actor>:<round>`, `manual`, plus
  † pack-named expiries such as rests). Never "a while".
- Positions are unit-agnostic; † packs fix the unit (feet, meters, zones).
  Queries 13–16 are pure functions of positions + optional map data passed
  as query input, never stored lore.

## 3. Hook record format

One hook = one YAML mapping in a `rules/` Markdown code fence
(` ```semiplane-hook `) or a `semiplane:` frontmatter list entry. Same record.

```yaml
id: <namespace>:<slug>     # unique
source: <provenance>       # tier:id:feature, e.g. class:barbarian:rage
on: [damage]               # 1+ core kinds (§1), structural match
when: <expr over ctx>      # typed payload + state; schema from core+pack
do:
  - add_damage: {slot: flat, tier: <pack-tier>, label: "...", amount: 2}
once_per_turn: false       # per-(hook, turn) latch when true
priority: 100              # lower runs first; ties broken by id
```

`do` verb set (core). Verbs marked † only fire meaningfully where the pack
defines the concept; validation rejects them otherwise:

| verb | args | effect |
|------|------|--------|
| `add_damage` | `{slot: dice\|flat, tier†, label, dice?: {n,size}, amount?: int}` | appends a contribution, ordered by pack precedence at render |
| `apply_condition` | `{condition†, until}` | appends to `target.conditions`, emits `condition_apply` |
| `spend_resource` | `{resource†, amount}` | debits pool, emits `resource_spend`; fails closed if insufficient |
| `expand_crit_range` † | `{to: int}` | attack-only; widens the pack crit threshold |
| `grant_advantage` / `grant_disadvantage` † | `{to, scope}` | **rejected unless the pack defines event-level adv/dis** — in v0 packs this is dnd-shaped; listed so authors get a helpful error, not a silent no-op |
| `redirect_damage` | `{resistance\|vulnerability\|immunity}` | damage-only type pipeline (pack defines the types) |

Validation (authoring time, all hard errors): `on` ⊆ core kinds; `when`
parses against the core+pack `ctx` schema; `add_damage` requires
`slot` + pack-known `tier`; `apply_condition` requires a pack-known key;
`priority` int default 100 (orders *execution*; tiers order the *display*);
`when` side-effect free (no RNG/clock — expr sandbox).

## 4. Dice, breakdown, versioning (core pipeline)

- The engine rolls a pack-shaped spec per event (count/size/keep) from the
  seeded RNG, then runs hook reducers, then renders. Hooks never roll.
- `POST /api/v1/dice/roll` returns the trace; `breakdown[]` (kept/discarded
  faces, per-tier dice/flat lines) ships iff `?breakdown=1` or the GM toggle
  is on — the auditable surface for "what doubled and what didn't".
- Seeded RNG: engine takes an `int64` seed (explicit face queue in tests).
  Oplog records every face so `replay(seed, log) == trace`.
- Versioning is two-level: `ontology: vN` (core) + `system: <pack-id>` (pack)
  in every hook file's frontmatter. Core removals = core major; pack key
  removals = pack major; anything added = minor of its level. The validator
  pins both and refuses newer files with a clear error.

---

# Part II — Pack profile: `dnd5e-2024` (normative for D&D content)

Everything in the v0 first draft that named a rule lives here, intact.

- **Attributes:** `str|dex|con|int|wis|cha`, scores raw, modifier
  `(score-10)//2`. `used_ability` in payloads takes these values.
- **Sources:** `weapon_melee|weapon_ranged|unarmed|spell|class_feature|other`.
- **Advantage (event-level):** `adv XOR dis → 2d20kh1 / 2d20kl1`, else `1d20`.
  Cancel, never stack, never from hooks.
- **Crit:** `{enabled, die: 20, threshold: 20}`; only `dice`-slot
  contributions roll twice (`2n`), flats never double. `expand_crit_range`
  widens the threshold (e.g. Champion 19–20).
- **Damage tiers:** `inherent → ability → item → class → feat → misc`.
- **Conditions (closed):** `rage, reckless, dodge, grappled, incapacitated,
  sapped, slow_10, …`. `sapped` = disadvantage on the victim's next attack
  (engine ORs it into the dice spec, consumed hit-or-miss); `slow_10` =
  speed −10, non-stacking. Contrast deliberate: Sap≠Slow.
- **Economy:** action / bonus action / reaction / feet; short + long rests;
  Vancian `slot_level` on `cast`.
- **State shape:** `traits: {str…}`, `pools: {hp, rages, spell_slots_1…}`,
  `ac`, `proficiency_bonus`, `speed_ft`/`base_speed_ft`, positions in feet —
  e.g. the level-5 barbarian: STR 18 (+4), PB +3, +2 greataxe 1d12 slashing,
  Rage +2 (class tier, gated `used_ability == 'str'` and
  `source in ['weapon_melee','unarmed']`), Reckless = advantage source.

Worked hooks (normative for the spike traces in `spike/engine/*_test.go`):

```yaml
# Rage damage: class tier, flat, STR-gated, melee/unarmed-gated
id: barbarian:rage-damage
source: class:barbarian:rage
on: [damage]
when: ctx.used_ability == 'str' and ctx.source in ['weapon_melee','unarmed']
do: [{add_damage: {slot: flat, tier: class, label: "Rage +2", amount: 2}}]
priority: 30
```

```yaml
# Sap: applies `sapped`; the engine enforces the disadvantage at dice-spec time
id: mastery:sap
source: item:mace:sap
on: [damage]
when: ctx.hit == true
do: [{apply_condition: {condition: sapped, until: "turn_start:<attacker>:next"}}]
priority: 200
```

```yaml
# Homebrew feat (scenario 2): feat tier, flat, melee-gated, never doubled
id: homebrew:mighty-blow
source: feat:mighty-blow
on: [damage]
when: ctx.source == 'weapon_melee'
do: [{add_damage: {slot: flat, tier: feat, label: "Mighty Blow +2", amount: 2}}]
priority: 50
```

---

# Part III — Authoring a pack (sketch: `bitd`)

A pack is vault data (`rules/systems/<id>/`), not code. Checklist, with the
Blades-in-the-Dark sketch the spike proves (`spike/testdata/packs/bitd.json`,
`TestBladesResistanceRoll`, `TestRefusalIsPackRelative`):

1. **Attributes:** `insight|prowess|resolve` (action dots are counts, not scores).
2. **Sources:** `action|resistance|fortune|downtime|other`.
3. **Crit:** disabled — no threshold, no doubling branch.
4. **Conditions:** `lesser_harm|moderate_harm|severe_harm|trauma`. No
   disadv-key (`disadv_key: ""`) — `sapped` is refused here, correctly.
5. **Tiers:** `inherent|ability|item|playbook|misc`.
6. **Bands:** resistance `full(6)|partial(4–5)|none(1–3)` over rating-d6
   keep-highest — resolved by the same engine, zero D&D concepts.

```yaml
# Same record shape as Part I §3; every value domain resolves against bitd.
id: whisper:tempest
source: playbook:whisper
on: [check]
when: ctx.used_ability == 'prowess' and ctx.source == 'action'
do: [{add_damage: {slot: flat, tier: playbook, label: "Tempest", amount: 1}}]
```

Validator behavior (pack-relative, tested): `str` passes under `dnd5e-2024`
and fails under `bitd`; `prowess` the reverse; `sapped` fails under `bitd`.

**Deliberately v1, not here:** per-pack action-economy shapes (3-action,
clocks, zones-as-units), the symbolic-dice `Face` type (spike-report §7),
and the pack-as-Markdown loader (the spike loads equivalent JSON to stay
stdlib-only). The seam for all three is the pack schema — the engine needs
no new branches, only new pack fields.
