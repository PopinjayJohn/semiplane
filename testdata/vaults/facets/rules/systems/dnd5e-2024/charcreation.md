---
tags:
- rules/system
semiplane:
  schemaVersion: 1
  generatedFrom: authored/rules/systems/dnd5e-2024
  charcreation:
    methods:
    - id: point-buy
      budget: 27
      scoreMin: 8
      scoreMax: 15
      costs:
        "8": 0
        "9": 1
        "10": 2
        "11": 3
        "12": 4
        "13": 5
        "14": 7
        "15": 9
    - id: standard-array
      values: [15, 14, 13, 12, 10, 8]
    increases:
      from: background
      count: 2
      weights: [2, 1]
---
# Character Creation (D&D 5e 2024)

*Source: SRD 5.2 and the Free Rules (2024). Authored namespace — not extractor
output; the `generatedFrom` marks it as hand-maintained.*

## Ability Scores

Six scores: Strength, Dexterity, Constitution, Intelligence, Wisdom, Charisma.
Generate them with point buy or the standard array — rolling is a table-variant
the GM may allow, not the baseline.

## Point Buy

Spend 27 points across the six scores. Every score starts at 8; no score may
exceed 15 via point buy (increases from later apply on top of the cap).

| Score | Cost |
|---|---|
| 8 | 0 |
| 9 | 1 |
| 10 | 2 |
| 11 | 3 |
| 12 | 4 |
| 13 | 5 |
| 14 | 7 |
| 15 | 9 |

Canonical checks: **15, 15, 15, 8, 8, 8** (9 + 9 + 9 = 27) and the standard
array **15, 14, 13, 12, 10, 8** (9 + 7 + 5 + 4 + 2 + 0 = 27) each buy exactly
the full budget.

## Standard Array

Assign **15, 14, 13, 12, 10, 8** to the six scores in any order.

## Ability Score Increases

The chosen background grants two increases — one +2 and one +1 — in any
combination across the six scores (see `semiplane.background` notes: the
`ability` field carries this as a weighted choose). Species grant no ability
scores under the 2024 rules.
