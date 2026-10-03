// Package dnd5e is shared 5e mechanics: the engine that resolves an intent, and
// the base data pack it resolves against.
//
// # What this package is, in §10.4's own words
//
// *"A pack is `go:embed`ed YAML, versioned with the code: stat blocks, conditions,
// reference tables, and the set of `kind`s the system recognises."* All four of
// those are `data/base.yaml`, and the pack is embedded with the code so that a
// deployed binary cannot be missing the rules it claims to run.
//
// *"A shared engine plus data packs plus a narrow Go escape hatch is the chosen
// split, because 5e 2014 and 5e 2024 are ~90% identical."* That is the whole
// design, and it is why this package has two halves with a hard boundary between
// them:
//
//   - **Data** is everything a row, a formula or a toggle can say. The differences
//     between editions live here, so P2b's overlays are reviewable YAML diffs
//     rather than Go forks.
//   - **Hooks** are the two rules data genuinely cannot express, named in the
//     pack's `hooks:` block and implemented in Go below. There are two of them and
//     ADR 0045 is the record for why, and for what would have made a third.
//
// *"The engine is therefore **shared 5e mechanics**, not 'the rules engine'. Systems
// with nothing in common do not use it."* Nothing in this package is required by
// any other system, `notfive` shares nothing with it, and the import audit in
// `codec_test.go` holds that.
//
// # What the engine resolves
//
// Seven operations, and they are the whole vocabulary:
//
//   - `roll` — resolve an expression in this system's notation against a DC, and
//     record the resolved roll on the rolling creature.
//   - `attack` — a d20 attack against a named defender, with conditions from the
//     pack supplying advantage, a critical hook, a mastery hook, and damage
//     applied to the defender.
//   - `heal` — restore hit points, clamped at the maximum.
//   - `apply_condition` and `clear_condition` — put a condition from the pack on a
//     creature, or take it off.
//   - `apply_status` — the GM's adjudication: set hit points, hit-point maximum or
//     speed outright. **GM-only.**
//   - `remove_token` — the GM takes a creature off the table. **GM-only**, and the
//     only op whose resolution sets `Mutation.Remove`.
//
// The two GM-only operations are §7.2's reserved set, and they are the first thing
// `Apply` checks: a player's attempt is refused before any state is read, so a
// refusal cannot leak whether the creature exists.
//
// # What is a Go hook and what is data, and why that line is where it is
//
// A rule became a hook when the thing it computes is a **choice among pack rows
// that data alone cannot rank**, and stayed data when the rows already rank
// themselves. Concretely:
//
//   - **Critical** (`hookCritical`) is a hook because it compares a roll's actual
//     faces against the policy the pack declares — and the policy, not the
//     procedure, is what the editions differ on. 2014 crits on a natural 20 from
//     the attack die; 2024 also crits when a damage die shows its maximum. That is
//     one boolean in `toggles` and **not** two Go rules. The procedure is shared;
//     only the policy moved, so only the policy is data.
//   - **Mastery** (`hookMastery`) is a hook because it ranks two ordered lists
//     against each other — the masteries a weapon grants, and the masteries a
//     creature holds — and picks a winner. A table cannot express "the highest of
//     these that the attacker also has", because that is a fold over a partial
//     order rather than a lookup. The *pairings* stay data: each mastery grants a
//     list of named effects, and the effect vocabulary is closed and validated at
//     load.
//
// Everything else stayed data, and the things that *would* have been hooks are
// named here because they are the ones a later contributor will be tempted to
// move:
//
//   - **Advantage and disadvantage** are a reading of two condition rows and one
//     cancellation rule, so they are a resolver and two columns.
//   - **Armour class, attack bonus, damage bonus, save DC** are formula strings
//     over a documented variable scope, because ADR 0012's second house-rule
//     example is "change a DC formula constant" and a constant that is a constant
//     in Go is a house rule that needs a Go fork.
//   - **Proficiency by level** is a table, not `2 + (level-1)/4`.
//   - **The die notation** is data twice over: `dice.sizes` is what
//     `Grammar`'s RE2 patterns are built from and what `Parse` refuses anything
//     outside, so this engine is a d20 system by configuration rather than by
//     assumption. §10.3's "the protocol never assumes d20" is satisfied by the
//     pattern being generated.
//
// # The ruleset fingerprint, and what it excludes
//
// `RulesetVersion` is this engine's half of `realtime.Fingerprint` and is the
// **engine's** semantics version — a constant, and a pack revision is *not* part
// of it. The other three components come from `Versions`, because
// `realtime.Descriptor` is the composition root's struct and this package cannot
// import it. The distinction is load-bearing (ADR 0018): enabling a house rule
// moves no component, and a pack revision moves exactly one.
//
// # Where the state lives
//
// A game object is semiplane's `rules.Object`, and `Data` is this system's own
// encoding of a creature's runtime facts — `creature.go`. Semiplane's `token` kind
// is what a creature is *placed as*: §10.2.1's rule is that `token` is
// semiplane's in every build so the client renders a table without knowing any
// rules, and a 5e creature is a token whose `Data` this package reads.
//
// # What this package does not do
//
// It does not implement `plugin.Codec` — `internal/domain/systems` imports nothing
// from `internal/plugin`, `internal/realtime`, `internal/store` or `internal/httpapi`,
// and `codec_test.go` audits that. It does not carry house-rule modules (P2d) or the
// 2014 and 2024 overlays (P2b); it carries the *mechanism* both need, and
// `overlay.go` is the seam. It ships no `init()`, no templ component and no
// JavaScript, which is §10.6.1's "a simple plugin ships data and zero UI code".
package dnd5e

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

//go:embed data/base.yaml
var basePackYAML []byte

// The system's identity, and the shape of its ruleset fingerprint.
const (
	// SystemID is permanent. Stored in `campaigns.system_id`, and a rename is a
	// migration for every campaign that named it rather than a rename.
	SystemID rules.ID = "dnd5e"

	// SystemTitle is the name shown where a campaign's system is named to a person.
	SystemTitle = "D&D 5e"

	// EngineVersion is this engine's half of the ruleset fingerprint: the version of
	// its resolution **semantics**.
	//
	// A constant, and not a hash of the packs, and that is the whole of ADR 0018's
	// split as this package sees it. `realtime.Fingerprint` has four components and
	// this is one of them; the base pack's version and the overlay's version are the
	// other two, reachable through `Versions`, and a house rule is none of them.
	// Folding the packs in here would mean a campaign could be stranded twice over
	// — once by the pack revision, which *should* refuse it, and once by a
	// fingerprint component that moved for the same reason and named nothing about
	// what moved.
	//
	// It moves when a **rule** changes meaning: a formula's grammar gains an
	// operator, the effect vocabulary gains a kind, the advantage cancellation rule
	// changes. It does not move when a row changes, because a row is a pack
	// revision and the pack versions already say so.
	EngineVersion = "dnd5e-engine@1"

	// Notation is the name of this system's expression notation. A label and never a
	// selector: nothing in `rules` switches on it, because a name that selects
	// behaviour is a name the contract would have to know.
	Notation = "dnd5e"
)

// The view names, which are this system's to choose and which the queries and
// payloads have to agree on.
//
// Constants rather than repeated literals because they appear in three places each —
// the declaration, the `Derive` switch and the payload — and `Payload.View` is what
// picks the renderer, so a name spelled two ways is a card rendered with the wrong
// renderer and nothing saying so.
const (
	// ViewStatBlock is one creature's derived card: armour class, hit points,
	// speeds, saves and its attacks.
	ViewStatBlock = "stat-block"

	// ViewConditionReference is every condition the pack declares, with what it does.
	ViewConditionReference = "condition-reference"

	// ViewConditionIndex is the conditions in force on the table.
	ViewConditionIndex = "condition-index"
)

// The operation vocabulary, which §10.2 says a gameplay plugin defines.
//
// A string and not an enum, for the reason `rules.Op` is one: membership is the
// system's answer, and a list in the contract would be a list no system could join.
// What is checked is shape, by `rules.Op.Valid`, which mirrors the codec's.
const (
	// OpRoll resolves an expression against a DC and records the result.
	OpRoll rules.Op = "roll"

	// OpAttack resolves a d20 attack against a named defender.
	OpAttack rules.Op = "attack"

	// OpHeal restores hit points on the target.
	OpHeal rules.Op = "heal"

	// OpApplyCondition puts a condition from the pack on the target.
	OpApplyCondition rules.Op = "apply_condition"

	// OpClearCondition takes a named condition off the target.
	OpClearCondition rules.Op = "clear_condition"

	// OpApplyStatus is the GM's adjudication: hit points, maximum and speed set
	// outright rather than rolled. **GM-only**.
	OpApplyStatus rules.Op = "apply_status"

	// OpRemoveToken takes the target off the table. **GM-only**.
	OpRemoveToken rules.Op = "remove_token"
)

// OpSetHitPoints is the mutation op for a resolved change to a creature's hit
// points, and it is deliberately **not** any of the intents above.
//
// `rules.Mutation` says one intent may resolve to several mutations and reusing
// the intent's name for each would put a second record of the same fact on the
// wire. An `attack` that lands resolves to `attack` on the attacker and
// `set_hit_points` on the defender, and a client reconciling the two reads two
// different facts rather than one fact recorded twice.
const OpSetHitPoints rules.Op = "set_hit_points"

// gmOnly is the GM-only half of the vocabulary, in declaration order.
//
// A package-level slice because `rules.SemiplaneKinds` is one and the reasoning is
// the same: it is written once from constants and nothing mutates it. What this
// package does **not** do is hand the same slice out twice, which is why
// `GMOnlyOps` clones it.
var gmOnly = []rules.Op{OpApplyStatus, OpRemoveToken}

// GMOnlyOps returns the operations §7.2 reserves to the GM.
//
// **A fresh slice per call**, for the reason `rules.SemiplaneKinds` gives: a shared
// slice handed to a caller that appended to it is package state changing under a
// registry, and a caller that sorted it in place reorders the vocabulary the
// conformance suite's findings are written in.
func GMOnlyOps() []rules.Op { return slices.Clone(gmOnly) }

// The pack's switches, as constants.
//
// **Named, because a switch read by name at three call sites and written by name in
// three packs is six spellings of one fact**, and a typo in a pack is invisible —
// `toggles` is a `map[string]bool`, so `crit_attack_die_max` misspelled is a key
// nothing reads and a rule that quietly never fires. That is why the resolver reads
// these and a *test* reads them: `TestEverySwitchTheEngineReadsIsDeclaredInTheBasePack`
// requires each of these to be a key in the shipped pack, and it is checked in both
// directions so a toggle in the pack that nothing reads is also reported.
const (
	// toggleCritAttackDieMax makes an attack die showing its maximum a critical hit.
	// **This is 2014's whole rule** and 2024's first half, which is why the editions
	// differ by a boolean rather than by a hook.
	toggleCritAttackDieMax = "crit_attack_die_max"

	// toggleCritDamageDieMax makes any damage die showing **its own** maximum a
	// critical hit. False in 2014, true in 2024.
	toggleCritDamageDieMax = "crit_damage_die_max"

	// toggleCritIgnoredByIncapacitated exempts attacks against a paralysed or
	// unconscious creature from the critical rule. False in 2014, true in 2024.
	toggleCritIgnoredByIncapacitated = "crit_ignored_by_incapacitated"

	// toggleInspiration is declared and **read by nothing in this slice**.
	//
	// It is in the pack and not in this package deliberately, and that is worth a
	// sentence: an edition's switch list is larger than any one engine build reads, and
	// a pack carrying a switch its engine ignores is harmless while a pack missing a
	// switch its engine needs is a rule that never fires. Declaring the whole list is
	// what makes a *later* work item's ability to read it a change to this package
	// rather than a change to every pack. `TestEverySwitchTheEngineReadsIsDeclaredInTheBasePack`
	// holds the half that matters — the direction where a missing switch is a bug.
	toggleInspiration = "inspiration"

	// toggleCoverAffectsAC makes cover contribute to the attacker's own armour class.
	// **Read by nothing in this slice** either: there is no cover on a placement this
	// engine places. It is declared for the same reason `toggleInspiration` is, and it
	// is the clearest statement of this package's scope: **what is in the pack is not
	// the same list as what this engine resolves.**.
	toggleCoverAffectsAC = "cover_affects_ac"

	// toggleFlankingOptional is ADR 0012's first house-rule example, a flag a module may
	// toggle. **Read by nothing in this slice**, for the same reason.
	toggleFlankingOptional = "flanking_optional"
)

// GMOnly reports whether this system resolves op as the GM's to resolve.
//
// A predicate and not a `slices.Contains` at each call site, because
// `plugin.Registry.Resolves` asks it for every op a UI tier wants to emit and the
// answer is the same set twice.
func GMOnly(op rules.Op) bool { return slices.Contains(gmOnly, op) }

// The refusals. Each carries an identifier and nothing from a vault, which is what
// makes them safe in a log line, and each is `errors.Is`-testable by the
// composition root's adapter, so the mapping onto a wire word is a line somebody
// wrote and reviewed rather than a string somebody matched.
var (
	// ErrGMOnly is an operation §7.2 reserves to the GM, attempted by a player.
	ErrGMOnly = errors.New("dnd5e: that operation is the GM's to make")

	// ErrNotYourCreature is an operation a player aimed at a creature that belongs to
	// somebody else.
	//
	// Distinct from `ErrGMOnly` because it is a different rule: this one is about
	// *whose* table an object is on, and it holds for operations the GM may also
	// perform. The adapter maps both to `not_permitted`, which is the same word for
	// the same wire, and keeping them apart is what lets the message say which.
	ErrNotYourCreature = errors.New("dnd5e: that creature is not yours to act for")

	// ErrUnknownOp is an op this system does not resolve.
	//
	// It exists although the hub never routes one — `realtime.Core.check` refuses an
	// unknown op before a resolver is called — because a resolver asked for one means
	// the registry and the system disagree, which is a wiring fault. Refusing it here
	// rather than resolving nothing keeps that fault loud.
	ErrUnknownOp = errors.New("dnd5e: this system does not resolve that operation")

	// ErrNoSuchCreature is an operation naming a game object that is not a creature of
	// this system's, or naming none at all.
	ErrNoSuchCreature = errors.New("dnd5e: no creature by that name is on the table")

	// ErrMalformedCreature is a game object whose `Data` this system could not read as
	// one of its own.
	//
	// **The one place this system is fatal about a thing it does not understand**, and
	// the reason it is defensible: the bytes are inside a placement whose kind *is*
	// semiplane's `token`, so a token that does not decode is a corrupted state
	// rather than a foreign kind. An unknown kind is inert — see `Apply`, which reads
	// only the object the intent addressed.
	ErrMalformedCreature = errors.New("dnd5e: that game object is not a readable creature")

	// ErrBadArguments is an operation whose arguments this system could not read.
	//
	// One sentinel for the whole argument vocabulary, because the wire has one word
	// for it (`invalid_args`) and a caller does not care which of the eight fields
	// was the wrong shape.
	ErrBadArguments = errors.New("dnd5e: those arguments are not ones this operation takes")

	// ErrNoSuchAttack is an attack naming an attack the attacker does not have.
	ErrNoSuchAttack = errors.New("dnd5e: that creature has no such attack")

	// ErrNoSuchCondition is a named condition this pack does not declare, or declares
	// with `disabled`.
	ErrNoSuchCondition = errors.New("dnd5e: this pack declares no such condition")

	// ErrNoSuchMastery is a weapon granting a mastery this pack does not declare.
	//
	// **A resolution-time refusal, not a load-time one**, and the reason is a real
	// asymmetry: which masteries a weapon offers lives on the *creature's* runtime
	// state, which is state a hub loads at startup and which this package never
	// validates as a whole. A creature could be authored against a pack that declared
	// a mastery, the pack changed, and the grant now names nothing. Skipping the
	// unknown mastery would resolve the attack with no mastery and report success —
	// the same failure `ErrUnknownHook` refuses at load, arriving by the one door load
	// validation cannot close.
	ErrNoSuchMastery = errors.New("dnd5e: this pack declares no such weapon mastery")

	// ErrPack is a data pack that would not load: malformed YAML, a missing
	// reference, a formula that does not parse, or a hook this engine does not ship.
	//
	// A single umbrella because every instance has the same owner and the same fix —
	// the pack — and because a composition root has one question to ask about a
	// registration: did the packs load?
	ErrPack = errors.New("dnd5e: this data pack cannot be loaded")

	// ErrRoll is an expression this system's notation does not accept.
	//
	// The counterpart to `ErrBadArguments` and deliberately separate from it: an
	// expression also arrives through `Parse`, which a client calls before it sends
	// anything, and a client that is told `invalid_args` about its own notation and
	// `invalid_args` about an argument it got wrong cannot tell which it fixed.
	ErrRoll = errors.New("dnd5e: that is not an expression in this system's notation")
)

// Versions are the components this system contributes to `realtime.Fingerprint`.
//
// **A struct rather than three getters**, for the reason `realtime.Descriptor` is
// one: these are one decision — *what did this campaign resolve to* — and a caller
// handed three separate calls could take two from one campaign and one from another.
// The composition root builds a `realtime.Descriptor` out of one of these.
//
// `Ruleset` is `EngineVersion` and nothing else. It is **not** a hash of the packs,
// and `TestThePackVersionIsNotPartOfTheRulesetVersion` is what holds that:
// `realtime` has four components and a value that moved under two of them would name
// neither.
type Versions struct {
	// System is the system id.
	System string

	// Ruleset is the engine's resolution-semantics version.
	Ruleset string

	// BasePack is the base data pack's version.
	BasePack string

	// OverlayPack is the overlay's version, or empty for a standalone pack.
	//
	// Empty is a legal value and §10.4 makes it a first-class shape rather than a
	// missing input: `realtime.checkComponent` exempts exactly this component.
	OverlayPack string
}

// String renders the versions for a refusal or a status page.
//
// **Never a resolved value**, for the reason `rules.Mutation.String` withholds a
// payload: every part of this is a compiled-in identifier or a version string, which
// is what makes it safe in a log line.
func (v Versions) String() string {
	return "system=" + v.System + ";ruleset=" + v.Ruleset +
		";base=" + v.BasePack + ";overlay=" + v.OverlayPack
}

// The pack-level refusals, each naming what broke it and carrying only a slug, an
// identifier or a formula — a pack's own text, which is compiled in and is never
// vault content.
var (
	// ErrNoSystem is a pack that names no system, which would make a fingerprint's
	// first component empty and therefore uncomparable.
	ErrNoSystem = fmt.Errorf("%w: it names no system", ErrPack)

	// ErrNoPackVersion is a pack with no version. It is one of the four fingerprint
	// components and its absence would make a pack revision invisible to the gate
	// that exists to notice one.
	ErrNoPackVersion = fmt.Errorf("%w: it has no version", ErrPack)

	// ErrForeignPack is a pack naming a system other than `SystemID`.
	ErrForeignPack = fmt.Errorf("%w: it names a system this engine does not implement", ErrPack)

	// ErrDuplicateSlug is two entries in one collection sharing a slug. Kept rather
	// than deduplicated, because there is no defensible winner and a merge that
	// silently keeps one is a merge whose result depends on file order.
	ErrDuplicateSlug = fmt.Errorf("%w: two entries share a slug", ErrPack)

	// ErrMissingSlug is a collection entry with no slug, which is the key every merge
	// is by and so the one field with no default.
	ErrMissingSlug = fmt.Errorf("%w: an entry has no slug to be keyed by", ErrPack)

	// ErrNoSuchAbility is a formula, an effect or an argument naming an ability the
	// pack does not declare.
	ErrNoSuchAbility = fmt.Errorf("%w: it names an ability this pack does not declare", ErrPack)

	// ErrBadFormula is a formula that does not parse, or that names a variable this
	// engine does not put in scope.
	ErrBadFormula = fmt.Errorf(
		"%w: a formula is not an expression this engine can evaluate",
		ErrPack,
	)

	// ErrUnknownConstant is a formula naming a reference constant the pack does not
	// declare.
	ErrUnknownConstant = fmt.Errorf(
		"%w: a formula names a constant this pack does not declare",
		ErrPack,
	)

	// ErrUnknownEffect is a mastery effect whose `kind` is not in the closed set.
	ErrUnknownEffect = fmt.Errorf(
		"%w: a mastery effect is not one this engine understands",
		ErrPack,
	)

	// ErrUnknownHook is a `hooks:` block naming a rule this engine does not ship.
	//
	// Refused rather than skipped, and the direction is the point: a pack naming a
	// rule this build does not have would otherwise resolve attacks **without a
	// critical rule** and report every crit as a plain hit, which is a game that
	// quietly does not work. A build that cannot satisfy a pack must say so.
	ErrUnknownHook = fmt.Errorf(
		"%w: the pack names a resolver hook this engine does not ship",
		ErrPack,
	)

	// ErrMissingHook is a pack that names no rule for one of the two hooks every pack
	// must name, which is the same failure in the other direction.
	ErrMissingHook = fmt.Errorf(
		"%w: the pack names no rule for a resolver hook every pack needs",
		ErrPack,
	)

	// ErrNoPrimaryDie is a pack with no primary die, which is the die every attack
	// rolls and so the one field with no default.
	ErrNoPrimaryDie = fmt.Errorf("%w: it declares no primary die", ErrPack)

	// ErrNoProficiency is a pack whose proficiency table does not cover a level.
	ErrNoProficiency = fmt.Errorf("%w: its proficiency table leaves a level uncovered", ErrPack)

	// ErrNoKinds is a pack that declares no kinds, which satisfies `rules.Validate`
	// and defeats §10.4's fourth reason a pack exists.
	ErrNoKinds = fmt.Errorf("%w: it declares no kinds, so the system recognises none", ErrPack)

	// ErrNoNotation is a pack with no notation name, which is what `Grammar` reports
	// and what `Expr` records.
	ErrNoNotation = fmt.Errorf("%w: it declares no notation", ErrPack)
)

// Pack is one loaded, merged and validated data pack: the rows an overlay may have
// replaced, resolved into the values the engine reads.
//
// **Read-only and safe for concurrent use.** Every lookup is a map read and every
// ordered walk is over a slice built in the pack's declaration order, so a
// resolution over the same pack is the same resolution — which is S-14.6's property
// arriving a package earlier than anyone expected.
//
// The zero `Pack` is not usable. `ParsePack` and `ParseOverlay` are the only ways to
// build one.
type Pack struct {
	system   string
	version  string
	notation string
	title    string

	sizes          []int
	primaryFaces   int
	primaryCount   int
	toggles        map[string]bool
	abilities      []abilityRow
	abilityAt      map[string]int
	conditions     []conditionRow
	conditionAt    map[string]int
	masteries      []masteryRow
	masteryAt      map[string]int
	constants      map[string]int
	proficiency    []proficiencyRow
	dcs            []dcRow
	dcAt           map[string]int
	formulas       map[string]compiledFormula
	formulaFor     []string
	masteryEnabled bool
	damageCap      int
	hooks          map[string]string
	rules          ruleSet
	kinds          []rules.Kind
	kindLabels     map[rules.Kind]string
}

// ruleSet is the procedural half of a pack: the two rules its `hooks:` block chose,
// resolved once at load.
//
// **A field on `Pack` rather than something `Pack` looks up per resolution**, and the
// reason is that a resolution must not depend on anything but the pack: a name lookup
// that could fail inside `Apply` is a name lookup whose failure is a panic at the
// table. `compileHooks` refuses anything unresolvable, so by the time a `Pack` exists
// both fields are non-nil — and `TestEveryHookIsNamedInThePackAndThePackNamesNoOther` is what holds
// that rather than a comment.
type ruleSet struct {
	// Critical decides whether a resolved attack roll is a critical hit. Never nil on
	// a loaded pack.
	Critical CriticalRule

	// Mastery decides which weapon mastery an attack uses. Never nil on a loaded pack.
	Mastery MasteryRule
}

// abilityRow is one ability, and the scores a creature holds for it are keyed by
// this slug.
type abilityRow struct {
	slug  string
	label string
	short string
}

// conditionRow is one condition and what being in it does.
type conditionRow struct {
	slug            string
	label           string
	summary         string
	attack          Advantage
	attacksAgainst  Advantage
	speedMultiplier int

	// criticalExempt exempts attacks against a creature in this condition from the
	// critical rule. Read by the resolver and answered by the hook, so the *policy*
	// stays in `toggles` and only the *naming* of the affected condition is here —
	// which is the same split ADR 0045 draws for the critical rule itself.
	criticalExempt bool
}

// masteryRow is one weapon mastery and the effects it grants.
//
// **`effects` are `effectFile`s, not a compiled type**, and the reason is the split
// ADR 0045 exists to state: the *vocabulary* of effects is validated at load and is
// closed, but what each effect does is decided by a Go rule in `resolve.go` at
// resolution time. A second compiled type would imply the effects were resolved when
// they are not — they are read, applied, and applied differently depending on the
// creature and the attack in front of them, which is what makes the mastery a *hook*
// and not a column.
//
// A 2014 pack has an empty list here rather than a pack that does not load. §10.4
// names "no mastery properties" as one of the three overlay differences, and the
// honest encoding of that is a pack whose `masteries:` block is absent or whose rows
// grant nothing — the 2014 overlay clears `attack.mastery.enabled` **and** may leave
// this list alone, because `MasteryEnabled` false means the list is never walked. An
// overlay that did want to strip the rows entirely sets each row's `disabled`, which
// this package does not support for masteries: a mastery with no row and a disabled
// mastery are the same absence, and one spelling of it is enough.
type masteryRow struct {
	slug    string
	label   string
	summary string
	effects []effectFile
}

// proficiencyRow is one band of the level-to-bonus table.
type proficiencyRow struct {
	from  int
	to    int
	bonus int
}

// dcRow is one named difficulty class.
type dcRow struct {
	slug  string
	label string
	dc    int
}

// Advantage is what a condition does to a roll, and the whole vocabulary of §7.2's
// "advantage and disadvantage are not the same thing".
//
// A closed string set rather than an enum because the pack spells these words and
// the pack is data — and `exhaustive` cannot help a value read out of YAML. `none`,
// `advantage` and `disadvantage` are the three legal spellings and
// `parseAdvantage` refuses anything else at load, so a resolution never branches on
// a value nobody validated.
type Advantage string

const (
	// AdvNone leaves a roll alone.
	AdvNone Advantage = "none"

	// AdvGains is advantage.
	AdvGains Advantage = "advantage"

	// AdvLoses is disadvantage.
	AdvLoses Advantage = "disadvantage"
)

// Cancel resolves what a set of advantages amounts to.
//
// **An advantage and a disadvantage cancel**, and that is a rule rather than a
// column: it is the one place two rows of the pack meet and the outcome is not
// either of them. Everything else is a fold over the same three values, so it is a
// function and not a data field — a data field would make "both" a fourth value
// every condition row would have to be able to say, and a condition never says
// "both".
func Cancel(values ...Advantage) Advantage {
	gains, loses := 0, 0

	for _, value := range values {
		switch value {
		case AdvGains:
			gains++
		case AdvLoses:
			loses++
		case AdvNone:
		}
	}

	switch {
	case gains > loses:
		return AdvGains
	case loses > gains:
		return AdvLoses
	default:
		return AdvNone
	}
}

// parseAdvantage reads one of the three spellings, refusing the rest at load.
// **Under `ErrPack`, not bare.** Every refusal in this package answers the composition
// root's one question — "did the packs load?" — with `errors.Is(err, ErrPack)`, and a
// refusal that does not is a refusal the caller has to recognise by reading its message.
// The three legal spellings are quoted because the condition row's column name is not
// in the message and the wrong word in it is invisible otherwise.
func parseAdvantage(text string) (Advantage, error) {
	switch Advantage(text) {
	case AdvNone:
		return AdvNone, nil
	case AdvGains:
		return AdvGains, nil
	case AdvLoses:
		return AdvLoses, nil
	default:
		return "", fmt.Errorf(
			"%w: %q is not one of %q, %q or %q",
			ErrPack, text, AdvNone, AdvGains, AdvLoses,
		)
	}
}

// System returns the system id the pack names, which is the first component of the
// fingerprint.
func (p *Pack) System() string { return p.system }

// Version returns the pack's version, which is one of the fingerprint's four
// components and the reason a pack revision gates a resume.
func (p *Pack) Version() string { return p.version }

// Notation returns the notation name the pack declares, which is what `Grammar`
// reports and what `Expr` records.
//
// Read from the pack rather than from the `Notation` constant so that an overlay
// which named a different notation would be *visible*: `TestTheKindsComeFromThePack`
// is what holds that, and a client validating against one notation while the server
// parsed another is the accident `Expr.Owner` exists to prevent.
func (p *Pack) Notation() string { return p.notation }

// Title returns the pack's display name, for a status page listing what a campaign
// resolved to.
func (p *Pack) Title() string { return p.title }

// Toggle reports a declared switch.
//
// A lookup and not a field, because the set is open: a pack declares whatever
// switches its edition has, and `Apply` asks for the ones the resolver happens to
// read. A pack that declares one this engine does not read is harmless, and a
// resolver that read a switch no pack declared would be reading its own default —
// which is why the resolver's own defaults are spelled out beside each call.
func (p *Pack) Toggle(name string) bool { return p.toggles[name] }

// ToggleNames returns every declared switch in the pack's own sorted order.
//
// **Sorted, and not declaration order**, because YAML mappings are unordered and Go
// map iteration is randomised: a listing built from a map walk would differ between
// runs, which is the one thing S-14.6 is about. `slices.Sorted(maps.Keys(…))` is the
// sanctioned form and is a range over a *slice*.
func (p *Pack) ToggleNames() []string {
	return slices.Sorted(maps.Keys(p.toggles))
}

// Abilities returns the abilities in declaration order, which is the display order
// and the order every derivation walks.
func (p *Pack) Abilities() []abilityRow { return slices.Clone(p.abilities) }

// AbilityAt returns the ability with this slug.
func (p *Pack) AbilityAt(slug string) (abilityRow, bool) {
	at, found := p.abilityAt[slug]
	if !found {
		return abilityRow{}, false
	}

	return p.abilities[at], true
}

// Conditions returns the enabled conditions in declaration order.
//
// **Enabled only.** A condition a pack marks `disabled` is not a condition of that
// pack, and a house rule's third example is "disable a condition" — so disabling one
// removes it from this list, from `ConditionAt`, and from every derivation that
// enumerates conditions, which is the whole of what disabling has to mean.
func (p *Pack) Conditions() []conditionRow { return slices.Clone(p.conditions) }

// ConditionAt returns the enabled condition with this slug.
func (p *Pack) ConditionAt(slug string) (conditionRow, bool) {
	at, found := p.conditionAt[slug]
	if !found {
		return conditionRow{}, false
	}

	return p.conditions[at], true
}

// Masteries returns the masteries in declaration order.
func (p *Pack) Masteries() []masteryRow { return slices.Clone(p.masteries) }

// MasteryAt returns the mastery with this slug.
func (p *Pack) MasteryAt(slug string) (masteryRow, bool) {
	at, found := p.masteryAt[slug]
	if !found {
		return masteryRow{}, false
	}

	return p.masteries[at], true
}

// MasteryEnabled reports whether an attack resolves its weapon's masteries at all.
//
// False is 2014, which has no weapon masteries, and the whole of that edition
// difference is this one boolean — see ADR 0045.
func (p *Pack) MasteryEnabled() bool { return p.masteryEnabled }

// DamageCap is the most damage one attack may do before multipliers, or zero for
// uncapped.
func (p *Pack) DamageCap() int { return p.damageCap }

// Constant returns a declared reference constant.
func (p *Pack) Constant(name string) (int, bool) {
	value, found := p.constants[name]

	return value, found
}

// DC returns a named difficulty class.
func (p *Pack) DC(slug string) (dcRow, bool) {
	at, found := p.dcAt[slug]
	if !found {
		return dcRow{}, false
	}

	return p.dcs[at], true
}

// Proficiency returns the proficiency bonus for a level.
//
// Refused rather than computed, because the table is where ADR 0012's second
// house-rule example lives: a house rule that changes how a band is drawn is a data
// change, and a resolver that computed `base + (level-1)/step` would put the band
// boundaries back in Go where no pack could reach them.
func (p *Pack) Proficiency(level int) (int, error) {
	for _, row := range p.proficiency {
		if level >= row.from && level <= row.to {
			return row.bonus, nil
		}
	}

	return 0, fmt.Errorf("dnd5e: the proficiency table covers levels %s and not %d",
		p.levelsCovered(), level)
}

// HasDieFace reports whether the notation admits a die with this many faces.
//
// The pack's `dice.sizes` is what makes the notation data: `Grammar` builds its
// patterns from it and `Parse` refuses anything outside it, so a d20 system is this
// pack's configuration rather than this engine's assumption. §10.3's "the protocol
// never assumes d20" is satisfied by the assumption not being in the code.
func (p *Pack) HasDieFace(faces int) bool { return slices.Contains(p.sizes, faces) }

// DieSizes returns the notation's die faces, sorted ascending.
//
// **Sorted**, for the reason `ToggleNames` gives: YAML sequences keep their order but
// a listing that sorted in place would mutate the pack's own slice.
func (p *Pack) DieSizes() []int {
	sorted := slices.Clone(p.sizes)
	slices.Sort(sorted)

	return sorted
}

// PrimaryDie returns the die an attack rolls: faces and count.
func (p *Pack) PrimaryDie() (faces, count int) { return p.primaryFaces, p.primaryCount }

// Hook returns the name of the rule this pack chose for one hook.
func (p *Pack) Hook(name string) (string, bool) {
	chosen, found := p.hooks[name]

	return chosen, found
}

// Kinds returns the kinds this pack declares, in declaration order.
//
// **The single source of `ContentKinds`.** §10.4 names "the set of `kind`s the
// system recognises" as one of the four things a pack carries, and a system that
// wrote the list in Go as well would have two answers to "what kinds does this
// system know" — one of which an overlay could change and the other of which it
// could not.
func (p *Pack) Kinds() []rules.Kind { return slices.Clone(p.kinds) }

// KindLabel returns a declared kind's display name.
func (p *Pack) KindLabel(kind rules.Kind) (string, bool) {
	label, found := p.kindLabels[kind]

	return label, found
}

// constantOrZero returns a declared reference constant, or zero when it is not
// declared.
//
// **Only for display copy.** Every place a *resolution* reads a constant goes through
// a formula, which `validateVariables` has already refused to compile if it named an
// undeclared one — so a formula can never reach this. This exists for a status page
// and a grammar example, where a missing constant should render as nothing rather than
// refuse to render, and where a resolution is not involved.
func (p *Pack) constantOrZero(name string) int {
	value, _ := p.Constant(name)

	return value
}

// levelsCovered renders the proficiency table's bands for a refusal message.
func (p *Pack) levelsCovered() string {
	bands := make([]string, 0, len(p.proficiency))
	for _, row := range p.proficiency {
		bands = append(bands, strconv.Itoa(row.from)+"-"+strconv.Itoa(row.to))
	}

	return strings.Join(bands, ", ")
}

// isDieSizePattern is the pre-image of the notation's die term, built from a pack's
// `dice.sizes`.
//
// Built rather than written out because a written-out pattern is a hardcoded d20
// parser in disguise: `Grammar` would then be showing a client one vocabulary while
// `Parse` accepted another, and §10.3 says the pattern is a convenience and `Parse`
// is the authority — which is only a true statement while they agree.
func dieSizePattern(sizes []int) string {
	sorted := slices.Clone(sizes)
	slices.Sort(sorted)

	parts := make([]string, 0, len(sorted))

	for _, size := range sorted {
		parts = append(parts, strconv.Itoa(size))
	}

	return "(" + strings.Join(parts, "|") + ")"
}

// BasePackYAML returns the embedded base pack's bytes.
//
// Exported so a test can prove what `New` loaded rather than what it claims to have
// loaded, and so an operator can read the rules a deployed binary is running without
// a source checkout: `go tool nm` would find the variable, and this makes it
// printable.
func BasePackYAML() []byte { return slices.Clone(basePackYAML) }

// ParsePack reads a complete, standalone data pack.
//
// This is how P2b's overlay package hands this engine a pack if it ever ships a
// standalone one, and it is the constructor the base pack itself goes through. It
// **validates**, because `rules.System` has no `Check` and says so: "a system that
// ships a broken data pack validates the pack in its own constructor".
func ParsePack(data []byte) (*Pack, error) {
	file, err := readPackFile(data)
	if err != nil {
		return nil, err
	}

	return compile(file, Versions{BasePack: file.Version, OverlayPack: ""})
}

// ParseOverlay reads a partial pack meant to be applied over a base.
//
// The result is a `packFile` rather than a `Pack`, because an overlay on its own is
// not a ruleset: it is a set of replacements, and applying it is `Merge`'s job. What
// this **does** check is that it is a pack at all — that it names this system and
// carries a version — because an overlay that named another system would otherwise
// be applied to this one and the fingerprint would record a version nobody can
// resolve.
func ParseOverlay(data []byte) (Overlay, error) {
	file, err := readPackFile(data)
	if err != nil {
		return Overlay{}, err
	}

	if file.System != string(SystemID) {
		return Overlay{}, fmt.Errorf("%w: it names %q", ErrForeignPack, file.System)
	}

	if file.Version == "" {
		return Overlay{}, fmt.Errorf(
			"%w: an overlay has no version to fingerprint",
			ErrNoPackVersion,
		)
	}

	return Overlay{name: file.Title, file: file}, nil
}

// readPackFile decodes YAML into the file shape, refusing a field this engine does
// not know.
//
// `KnownFields(true)` and not `yaml.Unmarshal`, and that is a real refusal rather than
// a tidiness: an overlay that misspelled `attaks_against` would otherwise load
// perfectly and have no effect, so an edition difference would be a silent no-op
// rather than a load failure at the composition root. §12's "untrusted disk content"
// does not apply here — the pack is compiled in — but the *typo* it would leave
// behind is the same failure it exists to prevent, one layer down.
//
// The decoder's own message is **dropped**, for the reason
// `content.interpretFrontMatter` gives: it quotes the line it choked on, and a pack
// is not attacker-reachable but a *house-rule* pack might be authored from something
// a GM pasted, and an error whose text varies with its input is a channel into the
// log. The file's own bytes are in the binary for anyone who needs to see them.
func readPackFile(data []byte) (packFile, error) {
	var file packFile

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	// Two failures, one refusal, and the distinction is worth the branch: a document
	// with nothing in it decodes with no error at all, and a pack of zeroes is a pack
	// whose every table is empty — which would otherwise reach a resolver as a
	// creature with no abilities and a proficiency bonus of nothing. The decoder's own
	// text is dropped for the reason the paragraph above gives.
	if err := decoder.Decode(&file); err != nil {
		if errors.Is(err, io.EOF) {
			return packFile{}, fmt.Errorf(
				"%w: it holds nothing at all, not even a system name",
				ErrPack,
			)
		}

		return packFile{}, fmt.Errorf("%w: it does not parse as a pack", ErrPack)
	}

	return file, nil
}
