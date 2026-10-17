package rules

import (
	"strings"
	"testing"

	"github.com/PopinjayJohn/semiplane/internal/dice"
	"github.com/PopinjayJohn/semiplane/internal/events"
)

func testState() *events.State {
	return &events.State{
		Actors: map[string]events.ActorState{
			"throg": {
				ID:   "throg",
				Name: "Throg",
				Traits: map[string]any{
					"str": 18, "level": 5,
					"proficiency_bonus": 3, "ac": 15,
				},
				Pools: map[string]int{"hp": 44, "rages": 3},
			},
			"goblin": {
				ID:   "goblin",
				Name: "Goblin",
				Traits: map[string]any{
					"str": 8, "level": 1, "ac": 15,
				},
				Pools: map[string]int{"hp": 7},
			},
		},
		Turn: events.TurnState{Round: 3, ActiveActor: "throg"},
	}
}

func baseReq() AttackRequest {
	return AttackRequest{
		AttackerID: "throg", TargetID: "goblin",
		UsedAbility: "str", Source: "weapon_melee",
		WeaponDiceN: 1, WeaponDice: 12, DamageType: "slashing",
		AbilityMod: 4, ProfBonus: 3, MagicBonus: 2,
		TargetAC: 15, Adv: true, Dis: false,
		OnHit: []OnHitCondition{{
			Condition: "sapped", Until: "turn_start:throg:next",
			Source: "item:greataxe:sap",
		}},
		WeaponLabel:  "greataxe 1d12 slashing",
		AbilityLabel: "STR ability",
		ItemLabel:    "weapon +2",
	}
}

func mustPack(t *testing.T, path string) *Pack {
	t.Helper()
	p, err := LoadPack(path)
	if err != nil {
		t.Fatalf("load pack %s: %v", path, err)
	}
	return p
}

func dnd(t *testing.T) *Pack  { return mustPack(t, "../../testdata/packs/dnd5e-2024.json") }
func bitd(t *testing.T) *Pack { return mustPack(t, "../../testdata/packs/bitd.json") }

func traceHas(trace []string, want string) bool {
	for _, line := range trace {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// RageDamageHook: +2 flat (class tier), gated on used_ability + source.
// Pack-specific test data, NOT engine code: the engine never names rules.
func RageDamageHook() Hook {
	return Hook{
		ID:       "barbarian:rage-damage",
		Source:   "class:barbarian:rage",
		On:       []string{"damage"},
		When:     "ctx.used_ability == 'str' and ctx.source in ['weapon_melee','unarmed']",
		Do:       []DoOp{{AddDamage: &AddDamageOp{Slot: "flat", Tier: "class", Label: "Rage +2", Amount: 2}}},
		Priority: 30,
	}
}

// MightyBlowFeat: homebrew scenario 2, feat tier flat +2 on melee weapons.
func MightyBlowFeat() Hook {
	return Hook{
		ID:       "homebrew:mighty-blow",
		Source:   "feat:mighty-blow",
		On:       []string{"damage"},
		When:     "ctx.source == 'weapon_melee'",
		Do:       []DoOp{{AddDamage: &AddDamageOp{Slot: "flat", Tier: "feat", Label: "Mighty Blow +2", Amount: 2}}},
		Priority: 50,
	}
}

// Scenario 1: seeded d20s 17,4 -> keep 17, total 17+4+3+2=26 hit.
// Damage d12 -> 7; flats 4+2+2 -> total 15.
func TestScenario1_BarbarianRageRecklessSap(t *testing.T) {
	st := testState()
	r := NewQueueRoller(17, 4, 7) // d20, d20, d12
	got, err := ResolveAttack(st, r, dnd(t), baseReq(), []Hook{RageDamageHook()}, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if got.Kept != 17 {
		t.Fatalf("kept d20 = %d, want 17", got.Kept)
	}
	if got.Discarded == nil || *got.Discarded != 4 {
		t.Fatalf("discarded d20 = %v, want 4", got.Discarded)
	}
	if got.Total != 26 {
		t.Fatalf("attack total = %d, want 26 (17+4+3+2)", got.Total)
	}
	if !got.Hit || got.Crit {
		t.Fatalf("hit=%v crit=%v, want hit=true crit=false", got.Hit, got.Crit)
	}
	if got.Damage.Total != 15 {
		t.Fatalf("damage total = %d, want 15 (7+4+2+2)", got.Damage.Total)
	}
	wantLines := []string{
		"greataxe 1d12 slashing [inherent/dice dice]: [7] = 7",
		"STR ability [ability/flat flat]: +4",
		"weapon +2 [item/flat flat]: +2",
		"Rage +2 [class/flat flat]: +2",
		"damage total = 15 slashing",
	}
	for _, w := range wantLines {
		if !traceHas(got.Trace, w) {
			t.Errorf("trace missing %q\nfull trace:\n%s", w, strings.Join(got.Trace, "\n"))
		}
	}
	// Generic OnHit: target gains the key; the pack (not the request) says
	// what the key means. No speed/score touched.
	gob := st.Actors["goblin"]
	if len(gob.Conditions) != 1 || gob.Conditions[0].Key != "sapped" {
		t.Fatalf("conditions = %+v, want one sapped", gob.Conditions)
	}
	if c := gob.Conditions[0]; c.Until != "turn_start:throg:next" || c.Source != "item:greataxe:sap" {
		t.Fatalf("condition = %+v, want until/source carried from OnHit", c)
	}
	if !traceHas(got.Trace, "condition_apply: goblin gains sapped until turn_start:throg:next (source item:greataxe:sap)") {
		t.Fatalf("trace missing generic condition_apply line:\n%s", strings.Join(got.Trace, "\n"))
	}
	if gob.Pools["hp"] != 7 {
		t.Fatalf("goblin hp = %d, want 7 (OnHit applies conditions, never damage)", gob.Pools["hp"])
	}
}

// Disadv-key holder rolls 2d20kl1 on its next attack and consumes it.
func TestScenario1_SapConsumedOnNextAttack(t *testing.T) {
	st := testState()
	r := NewQueueRoller(17, 4, 7)
	if _, err := ResolveAttack(st, r, dnd(t), baseReq(), []Hook{RageDamageHook()}, map[string]bool{}); err != nil {
		t.Fatalf("setup resolve: %v", err)
	}

	goblinReq := AttackRequest{
		AttackerID: "goblin", TargetID: "throg",
		UsedAbility: "str", Source: "weapon_melee",
		WeaponDiceN: 1, WeaponDice: 6, DamageType: "piercing",
		AbilityMod: 1, ProfBonus: 2, MagicBonus: 0,
		TargetAC: 15,
	}
	r2 := NewQueueRoller(12, 9, 4)
	got, err := ResolveAttack(st, r2, dnd(t), goblinReq, nil, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Kept != 9 {
		t.Fatalf("sapped kept = %d, want 9 (2d20kl1 of 12,9)", got.Kept)
	}
	if got.Discarded == nil || *got.Discarded != 12 {
		t.Fatalf("sapped discarded = %v, want 12", got.Discarded)
	}
	if !traceHas(got.Trace, "condition_remove: goblin consumes sapped (disadvantage applies to this attack)") {
		t.Fatalf("trace missing consume line:\n%s", strings.Join(got.Trace, "\n"))
	}
	if len(st.Actors["goblin"].Conditions) != 0 {
		t.Fatalf("sapped not consumed: %+v", st.Actors["goblin"].Conditions)
	}
	// Next attack is straight again: single d20.
	r3 := NewQueueRoller(14, 3)
	got2, err := ResolveAttack(st, r3, dnd(t), goblinReq, nil, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got2.Kept != 14 || got2.Discarded != nil {
		t.Fatalf("post-sap attack should be straight 1d20, got kept=%d discarded=%v", got2.Kept, got2.Discarded)
	}
}

// Scenario 1b: natural 20 -> only dice double (2d12), flats unchanged.
func TestScenario1_CritOnlyDiceDouble(t *testing.T) {
	st := testState()
	r := NewQueueRoller(20, 4, 7, 5) // nat20 kept, discarded 4, then 2d12 faces 7,5
	got, err := ResolveAttack(st, r, dnd(t), baseReq(), []Hook{RageDamageHook()}, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !got.Crit || !got.Hit {
		t.Fatalf("hit=%v crit=%v, want both true", got.Hit, got.Crit)
	}
	// 2d12 = 7+5=12, flats 4+2+2=8 -> 20.
	if got.Damage.Total != 20 {
		t.Fatalf("crit damage = %d, want 20 (12 dice + 8 flats)", got.Damage.Total)
	}
	for _, c := range got.Damage.Contributions {
		if c.Slot == events.SlotFlat && c.Doubled {
			t.Errorf("flat contribution %q marked doubled — flats never double", c.Label)
		}
		if c.Slot == events.SlotDice && !c.Doubled {
			t.Errorf("dice contribution %q not marked doubled on crit", c.Label)
		}
	}
}

// Scenario 1c: Rage requires used_ability gated AND weapon/unarmed —
// a DEX ranged weapon must NOT get rage damage. The gate is expr over
// ctx now, not a hardwired predicate.
func TestScenario1_RageDeniedDexThrown(t *testing.T) {
	st := testState()
	req := baseReq()
	req.UsedAbility = "dex"
	req.Source = "weapon_ranged"
	req.Adv = false // straight roll for clarity
	req.OnHit = nil
	r := NewQueueRoller(17, 7)
	got, err := ResolveAttack(st, r, dnd(t), req, []Hook{RageDamageHook()}, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Total != 17+4+3+2 {
		t.Fatalf("attack total = %d, want 26", got.Total)
	}
	// No rage: 7 + 4 + 2 = 13.
	if got.Damage.Total != 13 {
		t.Fatalf("dex-thrown damage = %d, want 13 (rage refused)", got.Damage.Total)
	}
	for _, c := range got.Damage.Contributions {
		if c.Label == "Rage +2" {
			t.Fatalf("rage applied to DEX ranged weapon — must NOT happen")
		}
	}
}

// Advantage cancels: adv+dis -> straight roll, single d20 consumed.
func TestAdvantageCancelsNeverStacks(t *testing.T) {
	st := testState()
	req := baseReq()
	req.Adv, req.Dis = true, true
	r := NewQueueRoller(17, 7)
	got, err := ResolveAttack(st, r, dnd(t), req, []Hook{RageDamageHook()}, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Kept != 17 || got.Discarded != nil {
		t.Fatalf("cancelled roll should keep single d20 17, got kept=%d discarded=%v", got.Kept, got.Discarded)
	}
	if got.Damage.Total != 13+2 { // 7+4+2+2 = 15
		t.Fatalf("damage = %d, want 15", got.Damage.Total)
	}
}

// Scenario 2: homebrew feat on damage, source-gated, flat +2.
// Same trace 15 -> 17; not doubled on crit; visible in breakdown.
func TestScenario2_HomebrewFeatFlat(t *testing.T) {
	st := testState()
	r := NewQueueRoller(17, 4, 7)
	got, err := ResolveAttack(st, r, dnd(t), baseReq(), []Hook{RageDamageHook(), MightyBlowFeat()}, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Damage.Total != 17 {
		t.Fatalf("damage with feat = %d, want 17 (15+2)", got.Damage.Total)
	}
	found := false
	for _, line := range got.Damage.Breakdown {
		if strings.Contains(line, "Mighty Blow +2") && strings.Contains(line, "[feat/flat flat]") {
			found = true
		}
	}
	if !found {
		t.Fatalf("feat contribution missing from GM breakdown:\n%s", strings.Join(got.Damage.Breakdown, "\n"))
	}
	// Crit: feat flat still +2 (12 dice + 8 + 2 = 22).
	st2 := testState()
	r2 := NewQueueRoller(20, 4, 7, 5)
	got2, err := ResolveAttack(st2, r2, dnd(t), baseReq(), []Hook{RageDamageHook(), MightyBlowFeat()}, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got2.Damage.Total != 22 {
		t.Fatalf("crit damage with feat = %d, want 22 (dice 12 + flats 8 + feat 2)", got2.Damage.Total)
	}
}

// Unknown vocabulary refused at authoring time, never guessed at roll time.
func TestUnknownVocabularyRefused(t *testing.T) {
	bad := Hook{
		ID: "homebrew:bogus", Source: "feat:bogus", On: []string{"damage"},
		When: "ctx.vibes == 'strong'",
		Do:   []DoOp{{AddDamage: &AddDamageOp{Slot: "flat", Tier: "feat", Label: "x", Amount: 1}}},
	}
	if err := ValidateHook(bad, dnd(t)); err == nil {
		t.Fatal("expected authoring-time refusal of ctx.vibes, got nil")
	}
	badEv := Hook{
		ID: "homebrew:bogus2", Source: "feat:bogus", On: []string{"mind_meld"},
		When: "ctx.hit == true",
		Do:   []DoOp{{AddDamage: &AddDamageOp{Slot: "flat", Tier: "feat", Label: "x", Amount: 1}}},
	}
	if err := ValidateHook(badEv, dnd(t)); err == nil {
		t.Fatal("expected authoring-time refusal of event mind_meld, got nil")
	}
	good := RageDamageHook()
	if err := ValidateHook(good, dnd(t)); err != nil {
		t.Fatalf("valid rage hook refused: %v", err)
	}
}

// Refusal is pack-relative: same strings, opposite verdicts.
func TestRefusalIsPackRelative(t *testing.T) {
	dndHook := Hook{
		ID: "barbarian:rage-damage", Source: "class:barbarian:rage", On: []string{"damage"},
		When: "ctx.used_ability == 'str' and ctx.source in ['weapon_melee','unarmed']",
		Do:   []DoOp{{AddDamage: &AddDamageOp{Slot: "flat", Tier: "class", Label: "Rage +2", Amount: 2}}},
	}
	if err := ValidateHook(dndHook, dnd(t)); err != nil {
		t.Fatalf("dnd hook refused under dnd pack: %v", err)
	}
	if err := ValidateHook(dndHook, bitd(t)); err == nil {
		t.Fatal("dnd hook ACCEPTED under bitd pack — refusal must be pack-relative")
	}
	bitdHook := Hook{
		ID: "whisper:tempest", Source: "playbook:whisper", On: []string{"check"},
		When: "ctx.used_ability == 'prowess' and ctx.source == 'action'",
		Do:   []DoOp{{AddDamage: &AddDamageOp{Slot: "flat", Tier: "playbook", Label: "tempest", Amount: 1}}},
	}
	if err := ValidateHook(bitdHook, bitd(t)); err != nil {
		t.Fatalf("bitd hook refused under bitd pack: %v", err)
	}
	if err := ValidateHook(bitdHook, dnd(t)); err == nil {
		t.Fatal("bitd hook ACCEPTED under dnd pack — refusal must be pack-relative")
	}
	// Condition keys are pack-checked too.
	condHook := Hook{
		ID: "x", Source: "y", On: []string{"damage"},
		When: "ctx.hit == true",
		Do:   []DoOp{{ApplyCondition: &ApplyConditionOp{Condition: "sapped", Until: "manual"}}},
	}
	if err := ValidateHook(condHook, bitd(t)); err == nil {
		t.Fatal("sapped ACCEPTED under bitd pack — condition keys must be pack-checked")
	}
}

// Alien event through the same engine: rating 2, faces 5,2 -> keep 5 ->
// band "partial". No crit rule, no modifiers — dice spec and bands come
// from pack data. Pure query: reported, not applied.
func TestBladesResistanceRoll(t *testing.T) {
	st := testState()
	r := NewQueueRoller(5, 2)
	got := ResolveResistance(st, r, bitd(t), ResistanceRequest{
		ActorID: "whisper", Attribute: "prowess", Rating: 2,
		Consequence: "possessed by the spirit",
	})
	if got.Highest != 5 || got.Band != "partial" {
		t.Fatalf("highest=%d band=%q, want 5 partial", got.Highest, got.Band)
	}
	want := "resistance [whisper/prowess]: 2d6 [5 2] keep-highest=5 → partial (vs \"possessed by the spirit\")"
	if len(got.Trace) != 1 || got.Trace[0] != want {
		t.Fatalf("trace = %q, want %q", got.Trace, want)
	}
	// Full success: single 6 with rating 1.
	r2 := NewQueueRoller(6)
	got2 := ResolveResistance(st, r2, bitd(t), ResistanceRequest{
		ActorID: "whisper", Attribute: "insight", Rating: 1, Consequence: "shot",
	})
	if got2.Band != "full" {
		t.Fatalf("band = %q, want full", got2.Band)
	}
	// Unknown attribute for the pack is refused, not guessed.
	got3 := ResolveResistance(st, NewQueueRoller(6), bitd(t), ResistanceRequest{
		ActorID: "whisper", Attribute: "str", Rating: 1, Consequence: "shot",
	})
	if got3.Highest != 0 {
		t.Fatalf("str resistance under bitd should refuse, got %+v", got3)
	}
	// Packs without a resistance die refuse too (dnd5e has no such roll).
	got4 := ResolveResistance(st, NewQueueRoller(6), dnd(t), ResistanceRequest{
		ActorID: "throg", Attribute: "str", Rating: 1, Consequence: "shot",
	})
	if got4.Highest != 0 {
		t.Fatalf("resistance under dnd5e should refuse (no resistance die), got %+v", got4)
	}
}

// ---------- Graduation fixes ----------

func TestCompileHookRejectsClock(t *testing.T) {
	for _, when := range []string{
		"now() > now()",
		"ctx.hit == true and now() != now()",
		`date("2026-01-01") == date("2026-01-01")`,
		`duration("1h") == duration("1h")`,
	} {
		if _, err := CompileHook(when); err == nil {
			t.Errorf("CompileHook(%q) accepted clock builtin, want refusal", when)
		}
	}
}

func TestCompileHookCachesPrograms(t *testing.T) {
	a, err := CompileHook("ctx.hit == true")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	b, err := CompileHook("ctx.hit == true")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if a != b {
		t.Fatal("same when-string compiled twice — cache must return one program")
	}
	if _, err := CompileHook(""); err == nil {
		t.Fatal("empty when compiled, want error")
	}
}

func TestExpandCritRange(t *testing.T) {
	champion := Hook{
		ID: "fighter:improved-critical", Source: "class:fighter:champion",
		On: []string{"attack"}, When: "ctx.attacker == 'throg'",
		Do:       []DoOp{{ExpandCrit: &ExpandCritOp{To: 19}}},
		Priority: 10,
	}
	if err := ValidateHook(champion, dnd(t)); err != nil {
		t.Fatalf("valid expand hook refused: %v", err)
	}
	// expand_crit_range without On∋attack is refused at validation.
	bad := Hook{
		ID: "x", Source: "y", On: []string{"damage"}, When: "ctx.hit == true",
		Do: []DoOp{{ExpandCrit: &ExpandCritOp{To: 19}}},
	}
	if err := ValidateHook(bad, dnd(t)); err == nil {
		t.Fatal("expand_crit_range on damage-only hook accepted, want refusal")
	} else if !strings.Contains(err.Error(), "on∋attack") {
		t.Fatalf("refusal should name the attack-phase rule, got: %v", err)
	}

	// Kept 19, straight roll: crits with the feat (threshold 19), hits
	// without it. Queue faces identical in both runs.
	for _, tc := range []struct {
		name  string
		hooks []Hook
		crit  bool
		total int
	}{
		{"widened", []Hook{champion, RageDamageHook()}, true, 20},
		{"pack", []Hook{RageDamageHook()}, false, 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testState()
			req := baseReq()
			req.Adv = false
			req.OnHit = nil
			got, err := ResolveAttack(st, NewQueueRoller(19, 7, 5), dnd(t), req, tc.hooks, map[string]bool{})
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.Crit != tc.crit {
				t.Fatalf("crit=%v, want %v (kept 19)", got.Crit, tc.crit)
			}
			if got.Damage.Total != tc.total {
				t.Fatalf("damage=%d, want %d", got.Damage.Total, tc.total)
			}
		})
	}
}

func TestRedirectDamage(t *testing.T) {
	fireward := Hook{
		ID: "spell:fire-ward", Source: "spell:ward",
		On: []string{"damage"}, When: "ctx.damage_type == 'fire'",
		Do:       []DoOp{{RedirectDamage: &RedirectOp{Kind: "resistance"}}},
		Priority: 90,
	}
	if err := ValidateHook(fireward, dnd(t)); err != nil {
		t.Fatalf("valid redirect hook refused: %v", err)
	}
	base := DamageRequest{
		AttackerID: "throg", TargetID: "goblin",
		Source: "spell", UsedAbility: "int", Hit: true,
		DamageType: "fire", BaseLabel: "flame", BaseN: 1, BaseSize: 12,
		AbilityFlat: 4, AbilityLabel: "INT ability",
	}
	// 7 + 4 = 11 -> resistance -> 5 (floor).
	got, err := ResolveDamage(testState(), NewQueueRoller(7), dnd(t), base, []Hook{fireward}, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Total != 5 {
		t.Fatalf("resisted total = %d, want 5 (11 halved, floor)", got.Total)
	}
	if !traceHas(got.Breakdown, "resistance (fire): 11 → 5") {
		t.Fatalf("breakdown missing redirect line:\n%s", strings.Join(got.Breakdown, "\n"))
	}
	// Gated on damage type: cold damage passes through untouched.
	cold := base
	cold.DamageType = "cold"
	gotCold, err := ResolveDamage(testState(), NewQueueRoller(7), dnd(t), cold, []Hook{fireward}, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if gotCold.Total != 11 {
		t.Fatalf("cold total = %d, want 11 (ward is fire-gated)", gotCold.Total)
	}
	// Immunity zeroes; vulnerability doubles. Request-level mods work too.
	for _, tc := range []struct {
		name  string
		hooks []Hook
		mods  []DamageMod
		want  int
	}{
		{"immunity-hook", []Hook{{
			ID: "x", Source: "y", On: []string{"damage"}, When: "ctx.hit == true",
			Do: []DoOp{{RedirectDamage: &RedirectOp{Kind: "immunity"}}},
		}}, nil, 0},
		{"vulnerability-hook", []Hook{{
			ID: "x", Source: "y", On: []string{"damage"}, When: "ctx.hit == true",
			Do: []DoOp{{RedirectDamage: &RedirectOp{Kind: "vulnerability"}}},
		}}, nil, 22},
		{"vulnerability-request", nil, []DamageMod{{Kind: "vulnerability"}}, 22},
		{"mismatched-request-type", nil, []DamageMod{{Kind: "vulnerability", DamageType: "cold"}}, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			req.Mods = tc.mods
			got, err := ResolveDamage(testState(), NewQueueRoller(7), dnd(t), req, tc.hooks, map[string]bool{})
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.Total != tc.want {
				t.Fatalf("total = %d, want %d", got.Total, tc.want)
			}
		})
	}
	// Unknown redirect kinds and bad tiers/slots are authoring errors.
	for _, op := range []DoOp{
		{RedirectDamage: &RedirectOp{Kind: "healing"}},
		{AddDamage: &AddDamageOp{Slot: "splash", Tier: "feat", Label: "x", Amount: 1}},
		{AddDamage: &AddDamageOp{Slot: "flat", Tier: "epic", Label: "x", Amount: 1}},
	} {
		h := Hook{ID: "x", Source: "y", On: []string{"damage"}, When: "ctx.hit == true", Do: []DoOp{op}}
		if err := ValidateHook(h, dnd(t)); err == nil {
			t.Errorf("op %+v accepted, want refusal", op)
		}
	}
}

func TestSpendResource(t *testing.T) {
	spend := Hook{
		ID: "feat:costly", Source: "feat:costly",
		On: []string{"damage"}, When: "true",
		Do:       []DoOp{{SpendResource: &SpendOp{Resource: "rages", Amount: 1}}},
		Priority: 5,
	}
	if err := ValidateHook(spend, dnd(t)); err != nil {
		t.Fatalf("valid spend hook refused: %v", err)
	}
	st := testState()
	got, err := ResolveDamage(st, NewQueueRoller(7), dnd(t), DamageRequest{
		AttackerID: "throg", TargetID: "goblin",
		Source: "weapon_melee", UsedAbility: "str", Hit: true,
		DamageType: "slashing", BaseLabel: "axe", BaseN: 1, BaseSize: 12,
	}, []Hook{spend}, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if st.Actors["throg"].Pools["rages"] != 2 {
		t.Fatalf("rages = %d, want 2 (debited)", st.Actors["throg"].Pools["rages"])
	}
	if !traceHas(got.Effects, "resource_spend: throg rages -1 (3→2)") {
		t.Fatalf("effects missing spend line: %v", got.Effects)
	}
	// Insufficient pool fails closed: error, no partial write.
	broke := Hook{
		ID: "feat:broke", Source: "feat:broke",
		On: []string{"damage"}, When: "true",
		Do: []DoOp{{SpendResource: &SpendOp{Resource: "rages", Amount: 9}}},
	}
	if _, err := ResolveDamage(testState(), NewQueueRoller(7), dnd(t), DamageRequest{
		AttackerID: "throg", TargetID: "goblin",
		Source: "weapon_melee", UsedAbility: "str", Hit: true,
		DamageType: "slashing", BaseLabel: "axe", BaseN: 1, BaseSize: 12,
	}, []Hook{broke}, map[string]bool{}); err == nil {
		t.Fatal("overspend accepted, want fail-closed error")
	} else if !strings.Contains(err.Error(), "insufficient") && !strings.Contains(err.Error(), "Fails closed") && !strings.Contains(err.Error(), "fails closed") {
		t.Fatalf("error should say fails closed, got: %v", err)
	}
	// Unknown resources are refused at authoring time (never guessed).
	unknown := Hook{
		ID: "x", Source: "y", On: []string{"damage"}, When: "true",
		Do: []DoOp{{SpendResource: &SpendOp{Resource: "mana", Amount: 1}}},
	}
	if err := ValidateHook(unknown, dnd(t)); err == nil {
		t.Fatal("unknown resource accepted, want refusal")
	}
}

func TestAttackPhaseSpendAndCondition(t *testing.T) {
	st := testState()
	hook := Hook{
		ID: "maneuver:goad", Source: "feat:goad",
		On: []string{"attack"}, When: "ctx.attacker == 'throg'",
		Do: []DoOp{
			{SpendResource: &SpendOp{Resource: "rages", Amount: 1}},
			{ApplyCondition: &ApplyConditionOp{Condition: "sapped", Until: "manual", Source: "feat:goad"}},
		},
	}
	if err := ValidateHook(hook, dnd(t)); err != nil {
		t.Fatalf("valid attack-phase hook refused: %v", err)
	}
	req := baseReq()
	req.Adv = false
	req.OnHit = nil
	got, err := ResolveAttack(st, NewQueueRoller(17, 7), dnd(t), req, []Hook{hook}, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !got.Hit {
		t.Fatalf("want hit (17+9 vs 15), got %+v", got)
	}
	if st.Actors["throg"].Pools["rages"] != 2 {
		t.Fatalf("rages = %d, want 2", st.Actors["throg"].Pools["rages"])
	}
	if !traceHas(got.Effects, "condition_apply: goblin gains sapped until manual (source feat:goad)") {
		t.Fatalf("effects missing attack-phase condition: %v", got.Effects)
	}
}

func TestGrantAdvantageAndRerollRejected(t *testing.T) {
	for _, verb := range []string{"grant_advantage", "grant_disadvantage"} {
		h := Hook{
			ID: "x", Source: "y", On: []string{"attack"}, When: "ctx.hit == false",
			Do: []DoOp{{RawVerb: verb}},
		}
		err := ValidateHook(h, dnd(t))
		if err == nil {
			t.Fatalf("%s accepted, want rejection", verb)
		}
		if !strings.Contains(err.Error(), "event-level dice-spec") {
			t.Fatalf("%s rejection should point at the dice-spec field, got: %v", verb, err)
		}
	}
	reroll := Hook{
		ID: "x", Source: "y", On: []string{"attack"}, When: "ctx.hit == false",
		Do: []DoOp{{RawVerb: "reroll"}},
	}
	if err := ValidateHook(reroll, dnd(t)); err == nil {
		t.Fatal("reroll accepted, want unknown-verb refusal (v1 candidate, not v0)")
	} else if !strings.Contains(err.Error(), "unknown verb") {
		t.Fatalf("reroll refusal should say unknown verb, got: %v", err)
	}
	empty := Hook{
		ID: "x", Source: "y", On: []string{"damage"}, When: "ctx.hit == true",
		Do: []DoOp{{}},
	}
	if err := ValidateHook(empty, dnd(t)); err == nil {
		t.Fatal("empty do op accepted, want refusal")
	}
}

func TestOncePerTurnLatch(t *testing.T) {
	sneak := Hook{
		ID: "rogue:sneak", Source: "class:rogue",
		On: []string{"damage"}, When: "ctx.hit == true",
		Do:       []DoOp{{AddDamage: &AddDamageOp{Slot: "flat", Tier: "class", Label: "Sneak", Amount: 2}}},
		Priority: 40, OncePerTurn: true,
	}
	req := DamageRequest{
		AttackerID: "throg", TargetID: "goblin",
		Source: "weapon_melee", UsedAbility: "dex", Hit: true,
		DamageType: "piercing", BaseLabel: "dagger", BaseN: 1, BaseSize: 4,
	}
	fired := map[string]bool{}
	first, err := ResolveDamage(testState(), NewQueueRoller(3), dnd(t), req, []Hook{sneak}, fired)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	second, err := ResolveDamage(testState(), NewQueueRoller(3), dnd(t), req, []Hook{sneak}, fired)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if first.Total != 5 || second.Total != 3 {
		t.Fatalf("totals = %d/%d, want 5/3 (latch fires once per turn actor+round)", first.Total, second.Total)
	}
	// Fresh map (or a new round) fires again.
	third, err := ResolveDamage(testState(), NewQueueRoller(3), dnd(t), req, []Hook{sneak}, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if third.Total != 5 {
		t.Fatalf("third total = %d, want 5", third.Total)
	}
	stNext := testState()
	stNext.Turn.Round = 4
	fourth, err := ResolveDamage(stNext, NewQueueRoller(3), dnd(t), req, []Hook{sneak}, fired)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if fourth.Total != 5 {
		t.Fatalf("next-round total = %d, want 5 (latch keys on round)", fourth.Total)
	}
}

func TestDamagePhaseConditionFromHook(t *testing.T) {
	mark := Hook{
		ID: "x", Source: "test:hook", On: []string{"damage"}, When: "ctx.hit == true",
		Do: []DoOp{{ApplyCondition: &ApplyConditionOp{Condition: "sapped", Until: "manual", Source: "test:hook"}}},
	}
	if err := ValidateHook(mark, dnd(t)); err != nil {
		t.Fatalf("valid condition hook refused: %v", err)
	}
	st := testState()
	got, err := ResolveDamage(st, NewQueueRoller(7), dnd(t), DamageRequest{
		AttackerID: "throg", TargetID: "goblin",
		Source: "weapon_melee", UsedAbility: "str", Hit: true,
		DamageType: "slashing", BaseLabel: "axe", BaseN: 1, BaseSize: 12,
	}, []Hook{mark}, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conds := st.Actors["goblin"].Conditions
	if len(conds) != 1 || conds[0].Key != "sapped" {
		t.Fatalf("conditions = %+v, want one sapped", conds)
	}
	if !traceHas(got.Effects, "condition_apply: goblin gains sapped until manual (source test:hook)") {
		t.Fatalf("effects missing line: %v", got.Effects)
	}
}

func TestUnknownActorFailsClosed(t *testing.T) {
	req := baseReq()
	req.AttackerID = "nobody"
	if _, err := ResolveAttack(testState(), NewQueueRoller(17, 4, 7), dnd(t), req, nil, map[string]bool{}); err == nil {
		t.Fatal("unknown attacker accepted, want fail-closed error")
	}
	req = baseReq()
	req.TargetID = "nobody"
	if _, err := ResolveAttack(testState(), NewQueueRoller(17, 4, 7), dnd(t), req, nil, map[string]bool{}); err == nil {
		t.Fatal("unknown target accepted, want fail-closed error")
	}
}

func TestRollerDeterministicFallback(t *testing.T) {
	a, b := NewQueueRoller(), NewQueueRoller()
	for i := 0; i < 8; i++ {
		fa, fb := a.Roll(20), b.Roll(20)
		if fa != fb {
			t.Fatalf("same construction diverged at roll %d: %d vs %d", i, fa, fb)
		}
		if fa < 1 || fa > 20 {
			t.Fatalf("face %d out of range", fa)
		}
	}
	// Queue faces still win over the stream.
	q := NewQueueRoller(17, 4)
	if q.Roll(20) != 17 || q.Roll(20) != 4 {
		t.Fatal("queue faces must be consumed first")
	}
	// Explicitly seeded rollers honor SEMIPLANE_TEST_SEED via dice.SeedFromEnv.
	t.Setenv("SEMIPLANE_TEST_SEED", "987654321")
	sa, sb := dice.NewSeededRoller(dice.SeedFromEnv(0)), dice.NewSeededRoller(987654321)
	for i := 0; i < 8; i++ {
		if fa, fb := sa.Roll(20), sb.Roll(20); fa != fb {
			t.Fatalf("same seed diverged at roll %d: %d vs %d", i, fa, fb)
		}
	}
}

func TestLoadPack(t *testing.T) {
	d := dnd(t)
	if d.Attack.Die == 0 || d.Attack.AutoMissOn == 0 || d.Attack.AdvCount == 0 || d.Attack.AdvKeep == 0 {
		t.Fatalf("dnd attack spec incomplete: %+v", d.Attack)
	}
	if len(d.DamageTypes) == 0 || len(d.Resources) == 0 {
		t.Fatalf("dnd pack needs damage_types and resources: %+v", d)
	}
	if !d.Crit.Enabled {
		t.Fatal("dnd crit must be enabled")
	}
	// bitd omits the attack section: absent-OK, resistance path unaffected.
	b := bitd(t)
	if b.Attack.Die != 0 {
		t.Fatalf("bitd must omit attack (absent-OK), got %+v", b.Attack)
	}
	if b.DisadvKey != "" {
		t.Fatalf("bitd must have no disadv key, got %q", b.DisadvKey)
	}
	if _, err := LoadPack("../../testdata/packs/nope.json"); err == nil {
		t.Fatal("missing pack path accepted, want error")
	}
}
