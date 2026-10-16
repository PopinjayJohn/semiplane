package rules

import (
	"encoding/json"
	"fmt"
	"os"
)

// CritRule is the pack's critical-hit rule. Whether crits exist, on what
// die, and from what natural roll up are all pack data; the engine only
// propagates the flag and doubles dice-slot contributions.
type CritRule struct {
	Enabled   bool `json:"enabled"`
	Die       int  `json:"die"`
	Threshold int  `json:"threshold"`
}

// AttackSpec is the pack's attack dice shape. The engine rolls AdvCount and
// keeps AdvKeep (highest on advantage, lowest on disadvantage); a straight
// roll is a single die. AutoMissOn is the natural face that always misses.
type AttackSpec struct {
	Die        int `json:"die"`
	AutoMissOn int `json:"auto_miss_on"`
	AdvCount   int `json:"adv_count"`
	AdvKeep    int `json:"adv_keep"`
}

// Band maps a kept-die minimum to an outcome label for resistance-style
// rolls. Bands are ordered high-min first in pack data.
type Band struct {
	Min   int    `json:"min"`
	Label string `json:"label"`
}

// Pack is one system pack: every value judgment the core refuses to make.
// Core (this binary): event envelope, hook record shape, reducer purity,
// seeded RNG. Pack (vault data): attribute sets, source taxonomy, damage
// types, resource pools, attack dice shape, crit rule, closed condition
// keys, damage-tier precedence, the disadv-key, outcome bands.
//
// The attack section may be absent (zero value) in packs with no attack
// roll; the resistance die may be absent in packs with no resistance roll.
// Absent sections are never touched by the paths that do not need them.
type Pack struct {
	ID              string     `json:"id"`
	Attributes      []string   `json:"attributes"`
	Sources         []string   `json:"sources"`
	DamageTypes     []string   `json:"damage_types"`
	Resources       []string   `json:"resources"`
	Attack          AttackSpec `json:"attack"`
	Crit            CritRule   `json:"crit"`
	Conditions      []string   `json:"conditions"`
	Precedence      []string   `json:"precedence"`
	DisadvKey       string     `json:"disadv_key"`
	ResistanceBands []Band     `json:"resistance_bands"`
	ResistanceDie   int        `json:"resistance_die"`
}

// LoadPack reads a system pack from JSON. Only id/attributes/precedence are
// required; every other section is optional per-system data.
func LoadPack(path string) (*Pack, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Pack
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.ID == "" || len(p.Attributes) == 0 || len(p.Precedence) == 0 {
		return nil, fmt.Errorf("pack %s: id/attributes/precedence required", path)
	}
	if p.Attack.Die != 0 {
		if p.Attack.Die < 2 {
			return nil, fmt.Errorf("pack %s: attack.die must be >= 2, got %d", path, p.Attack.Die)
		}
		if p.Attack.AdvCount < 2 {
			return nil, fmt.Errorf("pack %s: attack.adv_count must be >= 2, got %d", path, p.Attack.AdvCount)
		}
		if p.Attack.AdvKeep < 1 || p.Attack.AdvKeep > p.Attack.AdvCount {
			return nil, fmt.Errorf("pack %s: attack.adv_keep must be within 1..adv_count, got %d", path, p.Attack.AdvKeep)
		}
	}
	return &p, nil
}

func (p *Pack) attrSet() map[string]bool {
	m := map[string]bool{}
	for _, a := range p.Attributes {
		m[a] = true
	}
	return m
}

func (p *Pack) sourceSet() map[string]bool {
	m := map[string]bool{}
	for _, s := range p.Sources {
		m[s] = true
	}
	return m
}

func (p *Pack) damageTypeSet() map[string]bool {
	m := map[string]bool{}
	for _, d := range p.DamageTypes {
		m[d] = true
	}
	return m
}

func (p *Pack) condSet() map[string]bool {
	m := map[string]bool{}
	for _, c := range p.Conditions {
		m[c] = true
	}
	return m
}

func (p *Pack) resourceSet() map[string]bool {
	m := map[string]bool{}
	for _, r := range p.Resources {
		m[r] = true
	}
	return m
}

// vocabPool is the closed per-pack literal pool for `when` string literals:
// attributes, sources, damage types, and condition keys. Judged per active
// pack, so the same hook can be valid in one system and refused in another.
func (p *Pack) vocabPool() map[string]bool {
	m := map[string]bool{}
	for k := range p.attrSet() {
		m[k] = true
	}
	for k := range p.sourceSet() {
		m[k] = true
	}
	for k := range p.damageTypeSet() {
		m[k] = true
	}
	for k := range p.condSet() {
		m[k] = true
	}
	return m
}

// tierRank derives display/execution order from pack data, never globals.
func (p *Pack) tierRank() map[string]int {
	m := map[string]int{}
	for i, t := range p.Precedence {
		m[t] = i
	}
	return m
}

// rankOf returns the precedence rank of a tier; tiers outside the pack
// (e.g. caller-supplied base tiers) render after all pack tiers, stably.
func (p *Pack) rankOf(tier string) int {
	if r, ok := p.tierRank()[tier]; ok {
		return r
	}
	return len(p.Precedence)
}

// ResistanceBand returns the first band whose min the kept die meets.
func (p *Pack) ResistanceBand(kept int) string {
	for _, b := range p.ResistanceBands {
		if kept >= b.Min {
			return b.Label
		}
	}
	return "none"
}
