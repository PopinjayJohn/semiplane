package rules

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/PopinjayJohn/semiplane/internal/dice"
	"github.com/PopinjayJohn/semiplane/internal/events"
)

// ---------- Seeded RNG ----------

// Roller is the seeded RNG surface shared with internal/dice (repo-layout:
// rules imports dice + events, never the reverse). Tests use NewQueueRoller
// with exact faces; anything past the queue falls back to a deterministic
// stream — never wall-clock, never the shared math/rand source. Explicitly
// seeded rollers honor SEMIPLANE_TEST_SEED via dice.SeedFromEnv.
type Roller = dice.Roller

// NewQueueRoller returns a Roller that yields faces in order, then falls
// back to the deterministic stream. Exact semantics live in internal/dice.
func NewQueueRoller(faces ...int) *Roller {
	return dice.NewQueueRoller(faces...)
}

// ---------- Shared helpers ----------

// orderedHooks returns hooks sorted by priority (lower first, id tiebreak).
func orderedHooks(hooks []Hook) []Hook {
	out := append([]Hook(nil), hooks...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority == out[j].Priority {
			return out[i].ID < out[j].ID
		}
		return out[i].Priority < out[j].Priority
	})
	return out
}

// latchKey keys a once_per_turn latch on (hook, turn actor, round).
func latchKey(hookID, turnActor string, round int) string {
	return hookID + "\x00" + turnActor + "\x00" + strconv.Itoa(round)
}

func turnOf(st *events.State) (actor string, round int) {
	if st == nil {
		return "", 0
	}
	return st.Turn.ActiveActor, st.Turn.Round
}

func getActor(st *events.State, id string) (events.ActorState, error) {
	if st == nil {
		return events.ActorState{}, fmt.Errorf("unknown actor %q (nil state)", id)
	}
	a, ok := st.Actors[id]
	if !ok {
		return events.ActorState{}, fmt.Errorf("unknown actor %q", id)
	}
	return a, nil
}

func putActor(st *events.State, a events.ActorState) {
	if st.Actors == nil {
		st.Actors = map[string]events.ActorState{}
	}
	st.Actors[a.ID] = a
}

// debitPool subtracts amount from the actor's pool, failing closed (error,
// no write) on shortfall. pools.temp_hp is RESERVED for the v1 damage order
// (concentration -> temp HP -> death saves -> exhaustion); temp HP itself
// is not implemented, so spending it fails validation today.
func debitPool(st *events.State, actorID, resource string, amount int) (before, after int, err error) {
	if amount < 1 {
		return 0, 0, fmt.Errorf("spend_resource: amount must be >= 1, got %d", amount)
	}
	a, err := getActor(st, actorID)
	if err != nil {
		return 0, 0, err
	}
	before = a.Pools[resource]
	if amount > before {
		return before, before, fmt.Errorf("spend_resource: %s has %d %s, needs %d (fails closed)", actorID, before, resource, amount)
	}
	if a.Pools == nil {
		a.Pools = map[string]int{}
	}
	a.Pools[resource] = before - amount
	putActor(st, a)
	return before, before - amount, nil
}

// applyConditionOp appends the op's condition to the target actor and
// returns the condition_apply effect line.
func applyConditionOp(st *events.State, targetID, hookID string, op *ApplyConditionOp) (string, error) {
	a, err := getActor(st, targetID)
	if err != nil {
		return "", err
	}
	src := op.Source
	if src == "" {
		src = hookID
	}
	a.Conditions = append(a.Conditions, events.Condition{
		Key: op.Condition, Until: op.Until, Source: src,
	})
	putActor(st, a)
	return fmt.Sprintf("condition_apply: %s gains %s until %s (source %s)", targetID, op.Condition, op.Until, src), nil
}

// ---------- Damage resolution ----------

// DamageCtx is the hook-visible damage context; Hit/Crit/DamageType gate
// `when` expressions over Ctx.
type DamageCtx struct {
	Source      string
	UsedAbility string
	Hit         bool
	Crit        bool
	DamageType  string
}

// DamageMod is a request-level damage modifier (e.g. from traits or the
// encounter): resistance halves (floor), vulnerability doubles, immunity
// zeroes, after the tier sum. DamageType empty matches any type.
type DamageMod struct {
	Kind       string // resistance | vulnerability | immunity
	DamageType string
}

// DamageRequest is a standalone damage roll. ResolveAttack builds one from
// the attack; callers may also roll damage directly.
type DamageRequest struct {
	AttackerID   string
	TargetID     string // receives apply_condition ops; "" with such ops is an error
	Source       string
	UsedAbility  string
	Hit          bool
	Crit         bool
	DamageType   string
	BaseLabel    string
	BaseN        int
	BaseSize     int
	AbilityFlat  int
	AbilityLabel string
	ItemFlat     int
	ItemLabel    string
	Mods         []DamageMod
}

// Contribution is one tiered damage line. Slot decides crit behavior:
// dice roll twice on crit, flats never double.
type Contribution struct {
	Tier    string
	Slot    events.Slot
	Label   string
	Rolls   []int // dice only
	Amount  int   // flat only
	Doubled bool  // dice rolled 2n on crit
}

// Value sums the contribution.
func (c Contribution) Value() int {
	if c.Slot == events.SlotDice {
		s := 0
		for _, r := range c.Rolls {
			s += r
		}
		return s
	}
	return c.Amount
}

// DamageResult is the resolved damage: precedence-ordered contributions,
// the post-redirect total, per-contribution breakdown lines (GM-toggle
// auditable), and effect lines (resource_spend, condition_apply).
type DamageResult struct {
	Contributions []Contribution
	Total         int
	Breakdown     []string
	Effects       []string
}

// ResolveDamage runs base dice plus firing damage-phase hooks over req.
// Pure except for consuming the roller and writing hook effects (pool
// debits, conditions) into st. Returns an error, failing closed, on
// unvalidated hooks, short pools, or missing actors.
func ResolveDamage(st *events.State, roller *Roller, pack *Pack, req DamageRequest, hooks []Hook, fired map[string]bool) (*DamageResult, error) {
	if pack == nil {
		return nil, fmt.Errorf("resolve damage: nil pack")
	}
	if roller == nil {
		return nil, fmt.Errorf("resolve damage: nil roller")
	}
	if len(pack.Precedence) == 0 {
		return nil, fmt.Errorf("resolve damage: pack %s has no precedence", pack.ID)
	}
	if req.BaseN > 0 && req.BaseSize < 1 {
		return nil, fmt.Errorf("resolve damage: base dice size must be >= 1, got %d", req.BaseSize)
	}
	if fired == nil {
		fired = map[string]bool{}
	}
	turnActor, round := turnOf(st)

	n := req.BaseN
	doubled := false
	if req.Crit {
		n = req.BaseN * 2 // only dice multiply
		doubled = true
	}
	rolls := make([]int, 0, n)
	for i := 0; i < n; i++ {
		rolls = append(rolls, roller.Roll(req.BaseSize))
	}
	// Base tier is the pack's first precedence tier. Ability/item flats use
	// the canonical tier names (tech-stack §3.8 canonical precedence:
	// inherent -> ability -> item -> class -> feat -> misc).
	contribs := []Contribution{
		{Tier: pack.Precedence[0], Slot: events.SlotDice, Label: req.BaseLabel, Rolls: rolls, Doubled: doubled},
	}
	if req.AbilityFlat != 0 {
		label := req.AbilityLabel
		if label == "" {
			label = req.UsedAbility + " ability"
		}
		contribs = append(contribs, Contribution{Tier: "ability", Slot: events.SlotFlat, Label: label, Amount: req.AbilityFlat})
	}
	if req.ItemFlat != 0 {
		label := req.ItemLabel
		if label == "" {
			label = "item bonus"
		}
		contribs = append(contribs, Contribution{Tier: "item", Slot: events.SlotFlat, Label: label, Amount: req.ItemFlat})
	}

	dctx := DamageCtx{Source: req.Source, UsedAbility: req.UsedAbility, Hit: req.Hit, Crit: req.Crit, DamageType: req.DamageType}
	var redirects []string
	var effects []string

	for _, h := range orderedHooks(hooks) {
		if !h.firesOn("damage") {
			continue
		}
		prog, err := CompileHook(h.When)
		if err != nil {
			return nil, fmt.Errorf("hook %s: %w", h.ID, err)
		}
		fire, err := evalWhen(prog, Ctx{
			Source: dctx.Source, UsedAbility: dctx.UsedAbility,
			Hit: dctx.Hit, Crit: dctx.Crit, DamageType: dctx.DamageType,
			Attacker: req.AttackerID, Target: req.TargetID,
		})
		if err != nil {
			return nil, fmt.Errorf("hook %s: %w", h.ID, err)
		}
		if !fire {
			continue
		}
		if h.OncePerTurn {
			key := latchKey(h.ID, turnActor, round)
			if fired[key] {
				continue
			}
			fired[key] = true
		}
		for _, op := range h.Do {
			switch {
			case op.AddDamage != nil:
				a := op.AddDamage
				if a.Slot == string(events.SlotDice) {
					if a.DiceN < 1 || a.DiceSize < 1 {
						return nil, fmt.Errorf("hook %s: add_damage dice needs n>=1 and size>=1, got %d/%d (unvalidated hook?)", h.ID, a.DiceN, a.DiceSize)
					}
					nn := a.DiceN
					db := false
					if req.Crit {
						nn *= 2
						db = true
					}
					rr := make([]int, 0, nn)
					for i := 0; i < nn; i++ {
						rr = append(rr, roller.Roll(a.DiceSize))
					}
					contribs = append(contribs, Contribution{Tier: a.Tier, Slot: events.SlotDice, Label: a.Label, Rolls: rr, Doubled: db})
				} else {
					contribs = append(contribs, Contribution{Tier: a.Tier, Slot: events.SlotFlat, Label: a.Label, Amount: a.Amount})
				}
			case op.RedirectDamage != nil:
				redirects = append(redirects, op.RedirectDamage.Kind)
			case op.SpendResource != nil:
				before, after, err := debitPool(st, req.AttackerID, op.SpendResource.Resource, op.SpendResource.Amount)
				if err != nil {
					return nil, fmt.Errorf("hook %s: %w", h.ID, err)
				}
				effects = append(effects, fmt.Sprintf("resource_spend: %s %s -%d (%d→%d)",
					req.AttackerID, op.SpendResource.Resource, op.SpendResource.Amount, before, after))
			case op.ApplyCondition != nil:
				if req.TargetID == "" {
					return nil, fmt.Errorf("hook %s: apply_condition needs a damage target", h.ID)
				}
				line, err := applyConditionOp(st, req.TargetID, h.ID, op.ApplyCondition)
				if err != nil {
					return nil, fmt.Errorf("hook %s: %w", h.ID, err)
				}
				effects = append(effects, line)
			case op.ExpandCrit != nil:
				// Attack-phase only; ignored here (validation forces On∋attack,
				// so such a hook already contributed pre-roll).
			default:
				return nil, fmt.Errorf("hook %s: empty do op (unvalidated hook?)", h.ID)
			}
		}
	}
	for _, m := range req.Mods {
		if m.DamageType != "" && m.DamageType != req.DamageType {
			continue
		}
		redirects = append(redirects, m.Kind)
	}

	// Precedence order for the breakdown (stable), from pack data.
	sort.SliceStable(contribs, func(i, j int) bool {
		return pack.rankOf(contribs[i].Tier) < pack.rankOf(contribs[j].Tier)
	})

	total := 0
	var bd []string
	for _, c := range contribs {
		total += c.Value()
		if c.Slot == events.SlotDice {
			extra := ""
			if c.Doubled {
				extra = " (crit ×2 dice)"
			}
			bd = append(bd, fmt.Sprintf("%s [%s/%s dice]: %v%s = %d", c.Label, c.Tier, c.Slot, c.Rolls, extra, c.Value()))
		} else {
			extra := ""
			if req.Crit {
				extra = " (not doubled on crit)"
			}
			bd = append(bd, fmt.Sprintf("%s [%s/%s flat]: %+d%s", c.Label, c.Tier, c.Slot, c.Amount, extra))
		}
	}

	total, redirLines, err := applyRedirects(total, req.DamageType, redirects)
	if err != nil {
		return nil, err
	}
	bd = append(bd, redirLines...)

	return &DamageResult{Contributions: contribs, Total: total, Breakdown: bd, Effects: effects}, nil
}

// applyRedirects folds redirect kinds over the post-tier-sum total:
// immunity zeroes; otherwise resistance halves (floor) then vulnerability
// doubles. Same-kind redirects never stack.
func applyRedirects(total int, dtype string, kinds []string) (int, []string, error) {
	var imm, res, vul bool
	for _, k := range kinds {
		switch k {
		case "immunity":
			imm = true
		case "resistance":
			res = true
		case "vulnerability":
			vul = true
		default:
			return 0, nil, fmt.Errorf("unknown redirect kind %q (want resistance|vulnerability|immunity)", k)
		}
	}
	var lines []string
	if imm {
		lines = append(lines, fmt.Sprintf("immunity (%s): %d → 0", dtype, total))
		return 0, lines, nil
	}
	if res {
		halved := total / 2
		lines = append(lines, fmt.Sprintf("resistance (%s): %d → %d", dtype, total, halved))
		total = halved
	}
	if vul {
		lines = append(lines, fmt.Sprintf("vulnerability (%s): %d → %d", dtype, total, total*2))
		total *= 2
	}
	return total, lines, nil
}

// ---------- Attack resolution ----------

// OnHitCondition is a generic on-hit effect: on a hit the engine appends
// the condition to the target and emits a condition_apply effect line. It
// replaces system-named request fields: the pack decides what the key
// means (via DisadvKey etc.), the request just carries data.
type OnHitCondition struct {
	Condition string
	Until     string
	Source    string
}

// AttackRequest is one attack roll. Adv/Dis are the event-level dice spec;
// OnHit carries generic hit effects; labels default when empty.
type AttackRequest struct {
	AttackerID   string
	TargetID     string
	UsedAbility  string
	Source       string
	WeaponDiceN  int
	WeaponDice   int
	DamageType   string
	AbilityMod   int
	ProfBonus    int
	MagicBonus   int
	TargetAC     int
	Adv          bool
	Dis          bool
	OnHit        []OnHitCondition
	WeaponLabel  string
	AbilityLabel string
	ItemLabel    string
}

// AttackResult is the resolved attack: kept/discarded faces, total vs AC,
// hit/crit flags, damage on a hit, effect lines, and the audit trace.
type AttackResult struct {
	Kept      int
	Discarded *int
	Total     int
	Hit       bool
	Crit      bool
	Damage    *DamageResult
	Effects   []string
	Trace     []string
}

// ResolveAttack resolves one attack: pack-shaped dice spec, pack crit rule
// (widened by firing attack-phase expand_crit_range hooks), damage on hit,
// generic OnHit conditions. The pack disadv-key condition on the attacker
// is engine-level dice spec: ORed into dis and consumed hit-or-miss.
func ResolveAttack(st *events.State, roller *Roller, pack *Pack, req AttackRequest, hooks []Hook, fired map[string]bool) (*AttackResult, error) {
	if pack == nil {
		return nil, fmt.Errorf("resolve attack: nil pack")
	}
	if roller == nil {
		return nil, fmt.Errorf("resolve attack: nil roller")
	}
	if pack.Attack.Die < 2 {
		return nil, fmt.Errorf("resolve attack: pack %s defines no attack die", pack.ID)
	}
	if _, err := getActor(st, req.AttackerID); err != nil {
		return nil, err
	}
	if _, err := getActor(st, req.TargetID); err != nil {
		return nil, err
	}
	if fired == nil {
		fired = map[string]bool{}
	}
	turnActor, round := turnOf(st)

	actx := Ctx{
		Source: req.Source, UsedAbility: req.UsedAbility,
		Hit: false, Crit: false, DamageType: req.DamageType,
		TargetAC: req.TargetAC, KeptD20: 0,
		Attacker: req.AttackerID, Target: req.TargetID,
	}

	// Attack phase: expand_crit_range widens the threshold; spend and
	// conditions resolve before the roll. add_damage/redirect_damage are
	// damage-phase verbs and ignored here.
	threshold := pack.Crit.Threshold
	var effects []string
	for _, h := range orderedHooks(hooks) {
		if !h.firesOn("attack") {
			continue
		}
		prog, err := CompileHook(h.When)
		if err != nil {
			return nil, fmt.Errorf("hook %s: %w", h.ID, err)
		}
		fire, err := evalWhen(prog, actx)
		if err != nil {
			return nil, fmt.Errorf("hook %s: %w", h.ID, err)
		}
		if !fire {
			continue
		}
		if h.OncePerTurn {
			key := latchKey(h.ID, turnActor, round)
			if fired[key] {
				continue
			}
			fired[key] = true
		}
		for _, op := range h.Do {
			switch {
			case op.ExpandCrit != nil:
				if op.ExpandCrit.To < threshold {
					threshold = op.ExpandCrit.To
				}
			case op.SpendResource != nil:
				before, after, err := debitPool(st, req.AttackerID, op.SpendResource.Resource, op.SpendResource.Amount)
				if err != nil {
					return nil, fmt.Errorf("hook %s: %w", h.ID, err)
				}
				effects = append(effects, fmt.Sprintf("resource_spend: %s %s -%d (%d→%d)",
					req.AttackerID, op.SpendResource.Resource, op.SpendResource.Amount, before, after))
			case op.ApplyCondition != nil:
				line, err := applyConditionOp(st, req.TargetID, h.ID, op.ApplyCondition)
				if err != nil {
					return nil, fmt.Errorf("hook %s: %w", h.ID, err)
				}
				effects = append(effects, line)
			case op.AddDamage != nil, op.RedirectDamage != nil:
				// Damage-phase verbs; ignored pre-roll (see doc.go).
			default:
				return nil, fmt.Errorf("hook %s: empty do op (unvalidated hook?)", h.ID)
			}
		}
	}

	var trace []string
	trace = append(trace, effects...)

	// Pack-defined "disadvantage on next attack" condition. Event-level
	// dice spec, never a hook: ORed into dis, consumed hit-or-miss.
	att, _ := getActor(st, req.AttackerID)
	sapIdx := -1
	if pack.DisadvKey != "" {
		for i, c := range att.Conditions {
			if c.Key == pack.DisadvKey {
				sapIdx = i
				break
			}
		}
	}
	effDis := req.Dis || sapIdx >= 0
	if sapIdx >= 0 {
		att.Conditions = append(att.Conditions[:sapIdx], att.Conditions[sapIdx+1:]...)
		putActor(st, att)
		trace = append(trace, fmt.Sprintf("condition_remove: %s consumes %s (disadvantage applies to this attack)", req.AttackerID, pack.DisadvKey))
	}

	// Event-level advantage: cancels, never stacks.
	die := pack.Attack.Die
	count, keep := 1, 1
	high := true
	if req.Adv != effDis { // XOR
		count, keep = pack.Attack.AdvCount, pack.Attack.AdvKeep
		high = req.Adv && !effDis
	}
	faces := make([]int, 0, count)
	for i := 0; i < count; i++ {
		faces = append(faces, roller.Roll(die))
	}
	kept := faces[0]
	var discarded *int
	spec := fmt.Sprintf("%dd%d", count, die)
	if count > 1 {
		ordered := append([]int(nil), faces...)
		hl := "l"
		if high {
			sort.Sort(sort.Reverse(sort.IntSlice(ordered)))
			hl = "h"
		} else {
			sort.Ints(ordered)
		}
		spec = fmt.Sprintf("%dd%dk%s%d", count, die, hl, keep)
		if keep > len(ordered) {
			keep = len(ordered)
		}
		kept = 0
		for _, f := range ordered[:keep] {
			kept += f
		}
		if len(ordered) > keep {
			d := ordered[keep]
			discarded = &d
		}
	}
	total := kept + req.AbilityMod + req.ProfBonus + req.MagicBonus
	// Crit is a pack rule over the natural kept face, possibly widened by
	// attack-phase hooks above; packs without crit never crit.
	crit := pack.Crit.Enabled && kept >= threshold
	autoMiss := kept == pack.Attack.AutoMissOn
	hit := crit || (!autoMiss && total >= req.TargetAC)

	trace = append(trace, fmt.Sprintf("attack [%s]: d%d(%s) kept=%d discarded=%v +mods(%d+%d+%d) = %d vs AC %d → hit=%v crit=%v",
		req.AttackerID, die, spec, kept, discardedOrNil(discarded), req.AbilityMod, req.ProfBonus, req.MagicBonus, total, req.TargetAC, hit, crit))
	var dmg *DamageResult
	if hit {
		weaponLabel := req.WeaponLabel
		if weaponLabel == "" {
			weaponLabel = fmt.Sprintf("%dd%d %s", req.WeaponDiceN, req.WeaponDice, req.DamageType)
		}
		dmgReq := DamageRequest{
			AttackerID: req.AttackerID, TargetID: req.TargetID,
			Source: req.Source, UsedAbility: req.UsedAbility,
			Hit: true, Crit: crit, DamageType: req.DamageType,
			BaseLabel: weaponLabel, BaseN: req.WeaponDiceN, BaseSize: req.WeaponDice,
			AbilityFlat: req.AbilityMod, AbilityLabel: req.AbilityLabel,
			ItemFlat: req.MagicBonus, ItemLabel: req.ItemLabel,
		}
		var err error
		dmg, err = ResolveDamage(st, roller, pack, dmgReq, hooks, fired)
		if err != nil {
			return nil, err
		}
		trace = append(trace, dmg.Breakdown...)
		trace = append(trace, fmt.Sprintf("damage total = %d %s", dmg.Total, req.DamageType))
		effects = append(effects, dmg.Effects...)
		for _, oh := range req.OnHit {
			tgt, err := getActor(st, req.TargetID)
			if err != nil {
				return nil, err
			}
			tgt.Conditions = append(tgt.Conditions, events.Condition{
				Key: oh.Condition, Until: oh.Until, Source: oh.Source,
			})
			putActor(st, tgt)
			line := fmt.Sprintf("condition_apply: %s gains %s until %s (source %s)",
				req.TargetID, oh.Condition, oh.Until, oh.Source)
			effects = append(effects, line)
			trace = append(trace, line)
		}
	}
	return &AttackResult{Kept: kept, Discarded: discarded, Total: total, Hit: hit, Crit: crit, Damage: dmg, Effects: effects, Trace: trace}, nil
}

// ---------- Alien event: resistance-style roll ----------

// ResistanceRequest is a rating-dice keep-highest query (pack die, pack
// bands). Pure query: clocks/stress are not v0 state, so the outcome is
// reported, not applied.
type ResistanceRequest struct {
	ActorID     string
	Attribute   string // pack attribute
	Rating      int    // number of dice rolled
	Consequence string // prose, never parsed
}

// ResistanceResult carries the faces, the kept highest, and the pack band.
type ResistanceResult struct {
	Faces   []int
	Highest int
	Band    string
	Trace   []string
}

// ResolveResistance rolls rating pack-dice, keeps highest, bands it via
// pack data. Unknown attributes and packs without a resistance die are
// refused (reported, not guessed). st is reserved for v1 clocks.
func ResolveResistance(st *events.State, roller *Roller, pack *Pack, req ResistanceRequest) *ResistanceResult {
	_ = st
	if !pack.attrSet()[req.Attribute] {
		return &ResistanceResult{Trace: []string{
			fmt.Sprintf("resistance REFUSED: unknown attribute %q for pack %s", req.Attribute, pack.ID)}}
	}
	if pack.ResistanceDie < 2 {
		return &ResistanceResult{Trace: []string{
			fmt.Sprintf("resistance REFUSED: pack %s defines no resistance die", pack.ID)}}
	}
	faces := make([]int, 0, req.Rating)
	highest := 0
	for i := 0; i < req.Rating; i++ {
		f := roller.Roll(pack.ResistanceDie)
		faces = append(faces, f)
		if f > highest {
			highest = f
		}
	}
	band := pack.ResistanceBand(highest)
	return &ResistanceResult{
		Faces: faces, Highest: highest, Band: band,
		Trace: []string{fmt.Sprintf("resistance [%s/%s]: %dd%d %v keep-highest=%d → %s (vs %q)",
			req.ActorID, req.Attribute, req.Rating, pack.ResistanceDie, faces, highest, band, req.Consequence)},
	}
}

func discardedOrNil(d *int) any {
	if d == nil {
		return nil
	}
	return *d
}
