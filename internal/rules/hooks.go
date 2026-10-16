package rules

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/PopinjayJohn/semiplane/internal/events"
	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// maxWhenNodes caps `when` AST size so a hostile hook file cannot turn
// authoring-time validation into a compile-time blowup.
const maxWhenNodes = 500

// Ctx is the typed evaluation context for `when` expressions. Field names
// are the closed ctx schema (expr tags); unknown paths fail compilation,
// and string-literal values are additionally checked against the active
// pack in ValidateHook, so refusal stays pack-relative.
type Ctx struct {
	Source          string `expr:"source"`
	UsedAbility     string `expr:"used_ability"`
	Hit             bool   `expr:"hit"`
	Crit            bool   `expr:"crit"`
	DamageType      string `expr:"damage_type"`
	TargetAC        int    `expr:"target_ac"`
	KeptD20         int    `expr:"kept_d20"`
	Attacker        string `expr:"attacker"`
	Target          string `expr:"target"`
	AttackerVisible bool   `expr:"attacker_visible"`
	Provoked        bool   `expr:"provoked"`
	Skill           string `expr:"skill"`
	Ability         string `expr:"ability"`
	DC              int    `expr:"dc"`
	Mode            string `expr:"mode"`
}

// Env is the whole expr environment: struct only, never a map, so the
// compiler allowlists every reachable name and nothing else (no RNG, no
// clock, no builtins beyond the pure predicate core).
type Env struct {
	Ctx Ctx `expr:"ctx"`
}

// progCache compiles each distinct `when` once and runs it many times,
// keyed by source string. Programs are pure over Env, so sharing across
// packs is sound; pack-relative literal refusal lives in ValidateHook,
// not in the compiled program.
var progCache sync.Map // map[string]*vm.Program

// CompileHook compiles a `when` source string to a bool program. Unknown
// names, non-bool output, clock/RNG builtins, and oversized ASTs are all
// compile-time errors.
func CompileHook(when string) (*vm.Program, error) {
	if v, ok := progCache.Load(when); ok {
		return v.(*vm.Program), nil
	}
	prog, err := expr.Compile(when,
		expr.Env(Env{}),
		expr.AsBool(),
		expr.DisableBuiltin("now"),
		expr.DisableBuiltin("date"),
		expr.DisableBuiltin("duration"),
		expr.MaxNodes(maxWhenNodes),
	)
	if err != nil {
		return nil, fmt.Errorf("compile when %q: %w", when, err)
	}
	progCache.Store(when, prog)
	return prog, nil
}

// evalWhen runs a compiled `when` over ctx. Output is bool by construction
// (expr.AsBool); anything else is an engine bug, reported as an error.
func evalWhen(prog *vm.Program, ctx Ctx) (bool, error) {
	out, err := expr.Run(prog, Env{Ctx: ctx})
	if err != nil {
		return false, err
	}
	b, ok := out.(bool)
	if !ok {
		return false, fmt.Errorf("when returned %T, want bool", out)
	}
	return b, nil
}

// ---------- Hook records ----------

// AddDamageOp appends one damage contribution, ordered by pack precedence
// at render. Dice ops roll DiceN (doubled on crit); flat ops add Amount
// (never doubled on crit).
type AddDamageOp struct {
	Slot     string // dice | flat
	Tier     string // pack-known tier key
	Label    string
	DiceN    int
	DiceSize int
	Amount   int // flat only
}

// ApplyConditionOp appends a pack-closed condition key to the target.
type ApplyConditionOp struct {
	Condition string
	Until     string
	Source    string // defaults to the firing hook id when empty
}

// ExpandCritOp widens the pack crit threshold to To (effective threshold
// is the minimum over all firing ops). Attack-phase only.
type ExpandCritOp struct {
	To int
}

// RedirectOp applies resistance, vulnerability, or immunity to the damage
// total after the tier sum. Damage-phase only; the `when` ctx carries the
// damage type for gating.
type RedirectOp struct {
	Kind string // resistance | vulnerability | immunity
}

// SpendOp debits a pack-known resource pool. Fails closed on shortfall.
type SpendOp struct {
	Resource string
	Amount   int
}

// DoOp is one effect operation. RawVerb carries verbs the Go schema does
// not model (e.g. parsed from hook frontmatter) so validation can reject
// them with a helpful error instead of silently dropping them.
type DoOp struct {
	AddDamage      *AddDamageOp
	ApplyCondition *ApplyConditionOp
	ExpandCrit     *ExpandCritOp
	RedirectDamage *RedirectOp
	SpendResource  *SpendOp
	RawVerb        string
}

// Hook is one automation record: structural trigger (On + When) plus
// effects (Do), ordered by Priority (lower first, id tiebreak) with an
// optional once-per-turn latch.
type Hook struct {
	ID          string
	Source      string
	On          []string
	When        string
	Do          []DoOp
	Priority    int
	OncePerTurn bool
}

func (h Hook) firesOn(kind string) bool {
	for _, k := range h.On {
		if k == kind {
			return true
		}
	}
	return false
}

// ctxPaths is the closed ctx schema; anything else is refused before
// compilation for a better authoring-time error.
var ctxPaths = map[string]bool{
	"source": true, "used_ability": true, "hit": true, "crit": true,
	"damage_type": true, "target_ac": true, "kept_d20": true,
	"attacker": true, "target": true, "attacker_visible": true,
	"provoked": true, "skill": true, "ability": true, "dc": true,
	"mode": true,
}

// ValidateHook refuses unknown vocabulary at authoring time, relative to
// the active pack: event kinds against the core enum, ctx paths against
// the closed schema, literal values / tiers / conditions / resources
// against the pack. Never guessed at roll time.
func ValidateHook(h Hook, pack *Pack) error {
	if h.ID == "" {
		return fmt.Errorf("hook id required")
	}
	if len(h.On) == 0 {
		return fmt.Errorf("hook %s: on[] required", h.ID)
	}
	for _, k := range h.On {
		if !events.ValidKind(events.Kind(k)) {
			return fmt.Errorf("hook %s: unknown event kind %q (refused at authoring time)", h.ID, k)
		}
	}
	if strings.TrimSpace(h.When) == "" {
		return fmt.Errorf("hook %s: when required (empty conditions are a separate explicit `true`)", h.ID)
	}
	if err := validateWhenVocab(h.When, pack); err != nil {
		return fmt.Errorf("hook %s: %w", h.ID, err)
	}
	if _, err := CompileHook(h.When); err != nil {
		return fmt.Errorf("hook %s: %w", h.ID, err)
	}
	rank := pack.tierRank()
	conds := pack.condSet()
	res := pack.resourceSet()
	for _, op := range h.Do {
		set := 0
		if op.AddDamage != nil {
			set++
		}
		if op.ApplyCondition != nil {
			set++
		}
		if op.ExpandCrit != nil {
			set++
		}
		if op.RedirectDamage != nil {
			set++
		}
		if op.SpendResource != nil {
			set++
		}
		if op.RawVerb != "" {
			set++
		}
		if set == 0 {
			return fmt.Errorf("hook %s: empty do op", h.ID)
		}
		if set > 1 {
			return fmt.Errorf("hook %s: do op sets %d verbs (one per op)", h.ID, set)
		}
		switch {
		case op.RawVerb != "":
			if op.RawVerb == "grant_advantage" || op.RawVerb == "grant_disadvantage" {
				return fmt.Errorf("hook %s: %s is rejected in v0: advantage is an event-level dice-spec field (adv/dis on the roll request), never a hook verb", h.ID, op.RawVerb)
			}
			return fmt.Errorf("hook %s: unknown verb %q (refused at authoring time)", h.ID, op.RawVerb)
		case op.AddDamage != nil:
			a := op.AddDamage
			if a.Slot != "dice" && a.Slot != "flat" {
				return fmt.Errorf("hook %s: add_damage slot must be dice|flat, got %q", h.ID, a.Slot)
			}
			if _, ok := rank[a.Tier]; !ok {
				return fmt.Errorf("hook %s: unknown tier %q for pack %s", h.ID, a.Tier, pack.ID)
			}
			if a.Slot == "dice" && (a.DiceN < 1 || a.DiceSize < 1) {
				return fmt.Errorf("hook %s: add_damage dice needs n>=1 and size>=1, got %d/%d", h.ID, a.DiceN, a.DiceSize)
			}
		case op.ApplyCondition != nil:
			if !conds[op.ApplyCondition.Condition] {
				return fmt.Errorf("hook %s: unknown condition %q for pack %s", h.ID, op.ApplyCondition.Condition, pack.ID)
			}
		case op.ExpandCrit != nil:
			if !h.firesOn("attack") {
				return fmt.Errorf("hook %s: expand_crit_range only valid on hooks with on∋attack", h.ID)
			}
			if op.ExpandCrit.To < 2 {
				return fmt.Errorf("hook %s: expand_crit_range to must be >= 2, got %d", h.ID, op.ExpandCrit.To)
			}
		case op.RedirectDamage != nil:
			k := op.RedirectDamage.Kind
			if k != "resistance" && k != "vulnerability" && k != "immunity" {
				return fmt.Errorf("hook %s: redirect_damage kind must be resistance|vulnerability|immunity, got %q", h.ID, k)
			}
		case op.SpendResource != nil:
			if !res[op.SpendResource.Resource] {
				return fmt.Errorf("hook %s: unknown resource %q for pack %s", h.ID, op.SpendResource.Resource, pack.ID)
			}
			if op.SpendResource.Amount < 1 {
				return fmt.Errorf("hook %s: spend_resource amount must be >= 1, got %d", h.ID, op.SpendResource.Amount)
			}
		}
	}
	return nil
}

// Only literals compared against (or listed for) pack-closed ctx string
// fields († in the ontology: source keys, attribute keys, damage types —
// the field alternations in the cmp/in-list regexes below) are checked
// against the pack pool. Actor references (attacker/target) and open text
// (skill/mode/DC prose) are deliberately NOT checked: only those fields'
// literals are pack vocabulary.

var (
	ctxPathRe  = regexp.MustCompile(`ctx\.([A-Za-z_][A-Za-z0-9_]*)`)
	cmpRightRe = regexp.MustCompile(`ctx\.(source|used_ability|ability|damage_type)\s*(==|!=)\s*['"]([^'"]*)['"]`)
	cmpLeftRe  = regexp.MustCompile(`['"]([^'"]*)['"]\s*(==|!=)\s*ctx\.(source|used_ability|ability|damage_type)`)
	inListRe   = regexp.MustCompile(`ctx\.(source|used_ability|ability|damage_type)\s+(not\s+)?in\s+\[([^\]]*)\]`)
)

// validateWhenVocab judges `when` against the closed schema plus the active
// pack pool. ctx paths must all be known; string literals compared against
// (or listed for) pack-closed fields must be pack vocabulary. Actor ids and
// open text are never pack vocabulary and are left to the compiler.
func validateWhenVocab(when string, pack *Pack) error {
	for _, m := range ctxPathRe.FindAllStringSubmatch(when, -1) {
		if !ctxPaths[m[1]] {
			return fmt.Errorf("unknown ctx path %q (refused at authoring time)", "ctx."+m[1])
		}
	}
	pool := pack.vocabPool()
	check := func(lit string) error {
		if !pool[lit] {
			return fmt.Errorf("unknown vocabulary %q in when %q for pack %s (refused at authoring time)", lit, when, pack.ID)
		}
		return nil
	}
	for _, m := range cmpRightRe.FindAllStringSubmatch(when, -1) {
		if err := check(m[3]); err != nil {
			return err
		}
	}
	for _, m := range cmpLeftRe.FindAllStringSubmatch(when, -1) {
		if err := check(m[1]); err != nil {
			return err
		}
	}
	for _, m := range inListRe.FindAllStringSubmatch(when, -1) {
		for _, item := range strings.Split(m[3], ",") {
			item = strings.TrimSpace(item)
			item = strings.Trim(item, "'\"")
			if item == "" {
				continue
			}
			if err := check(item); err != nil {
				return err
			}
		}
	}
	return nil
}
