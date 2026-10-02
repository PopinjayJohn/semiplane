// Package notfive is a gameplay system with nothing in common with 5e, and it exists
// to be the counterexample S-14.9 asks for.
//
// # What this package is for
//
// S-14.9 is one sentence and it is the requirement that decides whether phase 8
// generalised or merely grew: *"a system sharing nothing with 5e passes the
// conformance suite **without touching `internal/domain/rules`.** This is the test
// that proves the plugin system actually generalises."* A suite that had only ever
// run against a 5e engine would prove nothing about that, because a 5e engine is the
// one system for which a suite can have been written around shared helpers by
// accident.
//
// So this package is a complete gameplay plugin with no d20, no ability scores, no
// conditions, no data pack and no shared engine: a loom. Its objects are spools of
// thread, its operations are `spin`, `weave`, `dye` and two GM adjudications, its
// notation is `2x12`, and its content kinds are `spool`, `bolt` and `dye_lot` — none
// of which is semiplane's and none of which is 5e's. The only thing it shares with
// anything is `rules.System` and the value types its nine methods return.
//
// # What is deliberately absent, and why each absence is the point
//
//   - **No shared engine.** §10.4 says a plugin sharing nothing with 5e "ships one
//     complete standalone pack and its own resolver hooks, and implements `System`
//     without touching `internal/domain/rules`, the engine, or semiplane itself."
//     "The engine" is phase 8's 5e mechanics package; this one does not import it, and
//     the import audit in `notfive_test.go` is closed — the **first** import of a 5e
//     package from here fails the build's test run rather than compiling.
//   - **No d20 anywhere, not even as a number.** §10.3's reason for `Grammar` is that
//     "the protocol never assumes d20", and the only way to demonstrate that is a
//     system for which d20 would be meaningless. Every draw here is an index into a
//     declared palette, which is not a die.
//   - **No shared data.** A pack is `go:embed`ed YAML and `go:embed` needs
//     `_ "embed"`. That import is absent, so a reader does not have to take this
//     paragraph's word for it.
//   - **No 5e vocabulary in identifiers or literals.** `notfive_test.go` audits it by
//     parsing this package's own AST, and the audit covers *identifiers and string
//     literals only* — a comment may say "not a d20 system", because a comment is
//     documentation and vocabulary-in-use is what a shared assumption is made of.
//
// # Why the determinism audit is not vacuous here
//
// A system with no randomness would pass S-14.6 trivially, so this one draws: every
// operation takes its number from `call.Rand`, labelled by operation and target, which
// is exactly the shape a reproducibility bug hides in. The labels are derived from the
// resolution's own inputs and never from a counter, a map walk or a clock — the
// property the audit observes, and observing it here means observing it on a system
// that had to work at it rather than on one that could not have done otherwise.
//
// # The refusal the adapter has to translate
//
// `ErrGMOnly` exists so the suite's `Classify` seam has something real to map: §7.2's
// rule is not "refuse a player", it is "refuse a player in a way the client can be
// told", and the wire's word for that is `not_permitted`. A system whose only refusal
// is its own sentence is a system whose refusals reach a browser verbatim.
package notfive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
)

// The system's identity, and the shape of its ruleset fingerprint.
//
// `SystemVersion` is `notfive@1` and nothing more: a loom's resolution semantics change
// when a dye lot is added, not when the palette is reordered. This package has no house
// rules at all, which is one fewer way to be wrong about ADR 0018's exclusion, and
// `notfive_test.go` asserts the exclusion through the suite rather than trusting this
// paragraph.
const (
	// SystemID is permanent. Stored in `campaigns.system_id`, and a rename is a
	// migration for every campaign that named it rather than a rename.
	SystemID rules.ID = "notfive"

	// SystemTitle is the name shown where a campaign's system is named to a person.
	SystemTitle = "Not Five"

	// SystemVersion is this system's half of the ruleset fingerprint.
	SystemVersion = "notfive@1"

	// Notation is the name of this system's expression notation. A label and never a
	// selector: nothing in `rules` switches on it, because a name that selects
	// behaviour is a name the contract would have to know.
	Notation = "loom"
)

// The view names, which are this system's to choose and which the suite's queries and
// payloads have to agree on.
//
// Constants rather than repeated literals because they appear in three places each —
// the declaration, the `Derive` switch and the payload — and `Payload.View` is what
// picks the renderer, so a name spelled two ways is a card rendered with the wrong
// renderer and nothing saying so.
const (
	// ViewBoltCard is one finished length, as a stat block.
	ViewBoltCard = "bolt-card"

	// ViewStockList is everything on the table, as a list.
	ViewStockList = "stock-list"
)

// The operation vocabulary, which is this system's to define: §10.2's "a gameplay
// plugin defines the operation vocabulary".
//
// Two are GM-only, and that split is the reason `ErrGMOnly` exists at all. §7.2
// reserves adjudication to the GM — a GM may restrengthen a thread or recut a player's
// work, and a player may not, because in this system those two operations decide
// whether a player's work counts at all.
const (
	// OpSpin draws thread onto a spool.
	OpSpin rules.Op = "spin"

	// OpWeave turns a spool into finished cloth.
	OpWeave rules.Op = "weave"

	// OpDye sets the colour of a finished length.
	OpDye rules.Op = "dye"

	// OpUnwind removes thread from a spool. GM-only.
	OpUnwind rules.Op = "unwind"

	// OpRecut returns a finished length to raw thread. GM-only.
	OpRecut rules.Op = "recut"
)

// The refusals. Each carries an identifier and nothing from a vault, which is what
// makes them safe in a log line, and each is `errors.Is`-testable by the composition
// root's adapter, so the mapping onto a wire word is a line somebody wrote and reviewed
// rather than a string somebody matched.
var (
	// ErrGMOnly is an operation §7.2 reserves to the GM, attempted by a player.
	//
	// A sentinel rather than only a message, because the wire has to be told
	// `not_permitted`, and the only honest way for it to know which refusal this is
	// for the system to name it. See the package comment.
	ErrGMOnly = errors.New("notfive: that operation is the GM's to make")

	// ErrUnknownOp is an op this system does not resolve.
	//
	// It exists although the hub never routes one — `realtime.Core.check` refuses an
	// unknown op before a resolver is called — because a resolver asked for one means
	// the registry and the system disagree, which is a wiring fault. Refusing it here
	// rather than resolving nothing keeps that fault loud instead of turning it into
	// an intent that quietly changed nothing.
	ErrUnknownOp = errors.New("notfive: this system does not resolve that operation")

	// ErrNoSuchSpool is an op naming an object that is not a spool of this system's.
	ErrNoSuchSpool = errors.New("notfive: no spool by that name is on the table")

	// ErrMalformedSpool is a game object whose `Data` this system could not read as
	// one of its own.
	//
	// **The one place this system is fatal about a thing it does not understand**, and
	// the reason it is defensible: the bytes are inside a placement whose kind *is*
	// this system's, so a spool that does not decode is a corrupted state rather than
	// a foreign kind. An unknown kind is inert — see `Apply`, which reads only the
	// object the intent addressed — and this refusal is reachable only for that one
	// object.
	ErrMalformedSpool = errors.New("notfive: that game object is not a readable spool")

	// ErrNotation is text this system's grammar does not accept.
	ErrNotation = errors.New("notfive: that is not a loom expression")
)

// gmOnly is the GM-only half of the vocabulary, in declaration order.
//
// A package-level slice because `rules.SemiplaneKinds` is one and the reasoning is the
// same: it is written once from constants and nothing mutates it. What this package
// does **not** do is hand the same slice out twice, which is why `LoomGMOnlyOps`
// clones it.
var gmOnly = []rules.Op{OpUnwind, OpRecut}

// LoomGMOnlyOps returns the operations §7.2 reserves to the GM.
//
// **A fresh slice per call**, for the reason `rules.SemiplaneKinds` gives: a shared
// slice handed to a caller that appended to it is package state changing under a
// registry, and a caller that sorted it in place reorders the vocabulary the findings
// are written in. One element copy per call is not a cost worth optimising into a
// global-mutation bug.
func LoomGMOnlyOps() []rules.Op { return slices.Clone(gmOnly) }

// The content kinds this system defines, all of its own and none of semiplane's.
//
// `KindSpool` is a **game object** kind — it is what a placement on the table is — and
// `KindBolt` and `KindDyeLot` are rules-content kinds, the shape §10.2.1's table gives
// `spell` and `creature`. `token` and `scene` are absent and must stay absent: they are
// semiplane's, and that is exactly what lets the client render a table without knowing
// this system exists.
const (
	// KindSpool is a game object on the table: one spool of thread.
	KindSpool rules.Kind = "spool"

	// KindBolt is a finished length of cloth, as campaign content.
	KindBolt rules.Kind = "bolt"

	// KindDyeLot is a batch of dye, as campaign content.
	KindDyeLot rules.Kind = "dye_lot"
)

// contentKinds is what `ContentKinds` declares, in a fixed order.
var contentKinds = []rules.Kind{KindSpool, KindBolt, KindDyeLot}

// LoomKinds returns a copy of this system's declared content kinds.
func LoomKinds() []rules.Kind { return slices.Clone(contentKinds) }

// palette is the dye lots a campaign has declared, and the whole of the randomness
// this system draws on.
//
// **An index, not a die.** `Context.Rand` is §10.3's deliberate substrate: "a d20
// system wants `1 + IntN(20)`; a 2d6-pool system wants six `IntN(6)` results counted
// against a target." This system wants neither. It wants an entry from a list its
// author wrote down, so it asks for a number and uses it as an index — the clearest
// demonstration available that the protocol carries no dice vocabulary.
var palette = []string{"undyed", "madder", "woad", "weld", "kermes", "lichen"}

// views is what `Views` declares.
//
// Both are served by semiplane's built-in renderers: **this package ships no templ
// component and no JavaScript**, which is §10.6.1's point that a simple plugin ships
// data and zero UI code, and also why a system nobody has heard of can be added
// without any client work at all.
var views = []rules.View{
	{
		Name:     ViewBoltCard,
		Title:    "Bolt card",
		Renderer: rules.RendererGeneric,
		Shape:    rules.ShapeStatBlock,
	},
	{
		Name:     ViewStockList,
		Title:    "Stock",
		Renderer: rules.RendererGeneric,
		Shape:    rules.ShapeList,
	},
}

// LoomViews returns a copy of this system's declared views.
func LoomViews() []rules.View { return slices.Clone(views) }

// LoomGrammar returns this system's notation, as data a client can validate against.
//
// The patterns are RE2 and in the portable subset `rules.Term` asks for — anchors,
// character classes and repetition, nothing a browser's engine would read differently.
// **The pattern is a convenience and `Parse` is the authority**, and this system keeps
// the two agreeing by hand because the suite cannot check it: `smallNumber` reads
// exactly what `^[1-9][0-9]?$` accepts, and `notfive_test.go` asserts `Example` parses
// so the tooltip is not a broken tooltip.
func LoomGrammar() rules.Grammar {
	return rules.Grammar{
		Notation: Notation,
		Summary:  "picks by width, or a bare number of picks",
		Example:  "2x12",
		Terms: []rules.Term{
			{
				Name:    "spin",
				Summary: "a bare number of picks to draw",
				Pattern: `^[1-9][0-9]?$`,
			},
			{
				Name:    "weave",
				Summary: "picks by width, as warp by weft",
				Pattern: `^([1-9][0-9]?)x([1-9][0-9]?)$`,
			},
		},
	}
}

// loom is the system.
//
// **A zero-size value.** Everything it needs is package state written once from
// constants, so there is nothing to configure and no constructor that can fail — which
// is the shape `rules.Validate`'s doc comment assumes: "a system that ships a broken
// data pack validates the pack in its own constructor", and this one ships no pack, so
// there is nothing for a pack to be wrong about.
type loom struct{}

// New returns the loom system.
//
// A constructor rather than an exported `var` because S-10.1 forbids `init()` and
// registration happens in the composition root: a plugin that hands out a package-level
// value shares it with every caller, and a value with a constructor is one a future pack
// can return from an erroring constructor without an API change.
func New() rules.System { return loom{} }

// The interface is the whole of what this package needs from the contract, and this
// assertion is the cheapest possible statement of that.
var _ rules.System = loom{}

// ID returns the permanent system identifier.
func (loom) ID() rules.ID { return SystemID }

// Title returns the name shown to a person.
func (loom) Title() string { return SystemTitle }

// RulesetVersion returns this system's half of the ruleset fingerprint.
func (loom) RulesetVersion() string { return SystemVersion }

// Grammar returns the notation.
//
// A method that builds rather than returns the package-level value, so a caller that
// edited the `Terms` slice it was handed edited a slice nobody else holds — the same
// reasoning `rules.SemiplaneKinds` gives, and the reason that function clones.
func (loom) Grammar() rules.Grammar { return LoomGrammar() }

// ContentKinds returns a copy of the declared content kinds.
func (loom) ContentKinds() []rules.Kind { return LoomKinds() }

// Views returns a copy of the declared views.
func (loom) Views() []rules.View { return LoomViews() }

// Parse turns notation text into this system's own expression tree.
//
// **A struct, not a map and not a string.** `rules.Expr.Node` is `any` for the reason
// §10.3 rules out a shared tree, and the proof that `any` earns its keep is a system
// putting its *own* type in it: semiplane routes the value back to whoever parsed it
// (`Expr.Owner`) and reads nothing from it, and a `string` would have been a value two
// systems could both appear to understand.
func (loom) Parse(text string) (rules.Expr, error) {
	spec, err := parseLoom(text)
	if err != nil {
		return rules.Expr{}, err
	}

	return rules.NewExpr(SystemID, Notation, text, spec), nil
}

// weaveSpec is this system's parse tree: how many picks, across how many threads.
//
// Unexported and unreachable from anywhere but `Parse`. Semiplane routes the `Expr`
// back to its owner and nothing else may read the node, which is why this can be a
// system type at all.
type weaveSpec struct {
	picks   int
	threads int
}

// parseLoom reads one expression.
//
// **Refused rather than repaired**, for the reason `rules.NewIntent` refuses rather
// than repairs: an expression a client sent is attacker-influenceable, and a parser
// that guesses what a player meant produces a result nobody can audit against
// `Expr.Source` — which is the whole of §16.3's roll log.
func parseLoom(text string) (weaveSpec, error) {
	trimmed := strings.TrimSpace(text)

	if spins, err := smallNumber(trimmed); err == nil {
		return weaveSpec{picks: spins}, nil
	}

	picks, threads, cut := strings.Cut(trimmed, "x")
	if !cut {
		return weaveSpec{}, fmt.Errorf("%w: %q", ErrNotation, text)
	}

	across, err := smallNumber(picks)
	if err != nil {
		return weaveSpec{}, fmt.Errorf("%w: %q has no pick count", ErrNotation, text)
	}

	along, err := smallNumber(threads)
	if err != nil {
		return weaveSpec{}, fmt.Errorf("%w: %q has no thread count", ErrNotation, text)
	}

	return weaveSpec{picks: across, threads: along}, nil
}

// smallNumber reads a one-or-two digit positive number, and nothing else.
//
// A hand-rolled bound rather than `strconv.Atoi` followed by a range check, because the
// grammar's pattern is `^[1-9][0-9]?$` and a parser that accepts what the pattern would
// accept — and no more — is the only way "the pattern is a convenience and `Parse` is
// the authority" stays true. `Atoi("007")` would succeed; this refuses it, because the
// client was told it could not be sent.
func smallNumber(text string) (int, error) {
	if text == "" || len(text) > 2 {
		return 0, fmt.Errorf("%w: %q is not a number of picks", ErrNotation, text)
	}

	total := 0

	for idx := range len(text) {
		char := text[idx]

		if char < '0' || char > '9' {
			return 0, fmt.Errorf("%w: %q is not a number of picks", ErrNotation, text)
		}

		if idx == 0 && char == '0' {
			return 0, fmt.Errorf("%w: %q is not a positive number of picks", ErrNotation, text)
		}

		total = total*10 + int(char-'0')
	}

	return total, nil
}

// knownOp reports whether this system resolves op.
//
// A `switch` and not a package-level set, for the reason `rules.IsSemiplaneKind` is
// one: the vocabulary is this package's declaration, written out where adding a sixth
// operation is a compile-visible edit rather than an entry somebody appends to a slice
// beside a loop.
func knownOp(op rules.Op) bool {
	switch op {
	case OpSpin, OpWeave, OpDye, OpUnwind, OpRecut:
		return true
	default:
		return false
	}
}

// Apply resolves one validated intent against one read-only snapshot.
//
// Five rules, and each is a way this system could have been wrong:
//
//  1. **The role decides, first.** §7.2's GM-only operations are refused before any
//     state is read, so a player's attempt reveals nothing about the tabletop — not
//     even whether the spool exists.
//  2. **Only the addressed object is read.** `state.Lookup(in.Target)` and nothing
//     else. That is what makes an unknown kind **inert rather than fatal** (S-14.7,
//     §10.8): a tabletop carrying a placement whose kind nothing registers resolves
//     exactly as it would not have been, because this system never walks its state.
//     The suite plants such an object and requires the resolution to succeed anyway.
//  3. **Every draw is named.** `call.Rand` is asked for a source under a label derived
//     from the operation and the target, never from a counter or a map key's order.
//     Two draws in one resolution would need two labels; this system has one per
//     resolution and names it for what the resolution is.
//  4. **Mutations are returned, never applied.** `rules.NewMutation` hands back a
//     value, the state arrived by value and its fields are unexported, and there is no
//     path from here to the hub's document. S-10.2's sentence about a panic leaving
//     `campaign_state` byte-identical is a consequence of that rather than a promise
//     this function makes.
//  5. **A refusal returns nothing.** `Contain` enforces it at the boundary; this
//     returns early rather than building a slice it is about to discard.
func (loom) Apply(
	_ context.Context,
	call rules.Context,
	state rules.State,
	intent rules.Intent,
) ([]rules.Mutation, error) {
	if slices.Contains(gmOnly, intent.Op) && call.Role != domain.RoleGM {
		return nil, fmt.Errorf("%w: %q is not a player's to make", ErrGMOnly, intent.Op)
	}

	if !knownOp(intent.Op) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownOp, intent.Op)
	}

	object, found := state.Lookup(intent.Target)
	if !found {
		return nil, fmt.Errorf("%w: %q", ErrNoSuchSpool, intent.Target)
	}

	before, err := readSpool(object)
	if err != nil {
		return nil, err
	}

	// The one draw, labelled by what the resolution is. Not by how many draws came
	// before: a counter would make the third resolution of a campaign depend on the two
	// before it, and S-14.6 is about a resolution being a function of its inputs.
	draw := call.Rand(intent.Op.String() + "/" + intent.Target.String())

	after := before

	switch intent.Op {
	case OpSpin:
		after.Length += 1 + int(draw.UintN(8))
	case OpWeave, OpDye:
		after.Colour = int(draw.UintN(uint(len(palette))))
	case OpUnwind:
		after.Length = before.Length - 1 - int(draw.UintN(4))
	case OpRecut:
		after.Length = 1
		after.Colour = 0
	}

	if after.Length < 0 {
		after.Length = 0
	}

	// The mutation's `Op` is this operation's own name, for the reason
	// `rules.Mutation` gives: one intent may resolve to several mutations, and reusing
	// the intent's name for each would put a second record of the same fact on the
	// wire. There is one mutation here and the same name, and that is the record rather
	// than a duplicate of it.
	mutation, err := rules.NewMutation(intent.Target, intent.Op, encode(after))
	if err != nil {
		return nil, fmt.Errorf("notfive: stating the result of %q: %w", intent.Op, err)
	}

	return []rules.Mutation{mutation}, nil
}

// spoolState is a game object's body, in this system's own encoding.
//
// **Opaque to semiplane**, which is the boundary `rules.Object.Data` draws: the id, the
// kind and the ordering are semiplane's, and everything else belongs to the system. A
// d20 creature's action economy and a spool's length are not the same shape, and a union
// here would be a type every new system has to edit.
type spoolState struct {
	Length int `json:"length"`
	Colour int `json:"colour"`
}

// readSpool decodes one game object as a spool.
//
// Refused for an object whose kind is not this system's as well as for one whose bytes
// do not decode, and the distinction is deliberately **not** drawn: a placement of
// another kind addressed by name is this system's business only to the extent of saying
// "that is not mine", and the unknown-kind rule is about an object the resolution never
// looked at (rule 2 of `Apply`).
func readSpool(object rules.Object) (spoolState, error) {
	if object.Kind != KindSpool {
		return spoolState{}, fmt.Errorf("%w: %q is a %q, not a %q",
			ErrNoSuchSpool, object.ID, object.Kind, KindSpool)
	}

	var decoded spoolState

	if err := json.Unmarshal(object.Data, &decoded); err != nil {
		return spoolState{}, fmt.Errorf("%w: %q: %w", ErrMalformedSpool, object.ID, err)
	}

	return decoded, nil
}

// encode renders a spool's new body as the opaque payload a mutation carries.
//
// One function rather than an inline `json.Marshal` at each call site, so the encoding
// the resolver writes and the encoding `readSpool` reads are the same pair by
// construction rather than by two lines happening to agree.
func encode(state spoolState) []byte {
	// A struct of two integers cannot fail to marshal, and the fallback is a body both
	// sides can read: an empty object decodes to zeroes rather than failing.
	encoded, err := json.Marshal(state)
	if err != nil {
		return []byte(`{}`)
	}

	return encoded
}

// boltCard is what `bolt-card` answers with: a named block of fields, which is what
// `rules.ShapeStatBlock`'s renderer expects.
//
// A struct and not a `map[string]any`, and the reason is §10.6.1's: a payload's shape is
// the system's own. Had this package shipped a templ component, `Payload.Value` would
// return this and the component would take a `boltCard` — type-safe instead of a map
// traversal through `any`.
type boltCard struct {
	Spool  string `json:"spool"`
	Length int    `json:"length"`
	Colour string `json:"colour"`
}

// stockRow is one line of `stock-list`.
type stockRow struct {
	Spool  string `json:"spool"`
	Length int    `json:"length"`
	Colour string `json:"colour"`
}

// Derive answers one render query with opaque data.
//
// **It reads the state the way `Apply` does** — one object, by name — so a query about
// an object that is not there is a refusal with `ErrNoSuchSpool` rather than an empty
// card. §14's "every declared view renders for an empty, a typical and a maximal
// `Derive` output" is about what a *renderer* does with a payload's shape, and this
// payload's empty case is a legitimate row: a spool with no thread and no dye is a
// spool, not a missing answer.
func (loom) Derive(state rules.State, query rules.Query) (rules.Payload, error) {
	if !query.Check() {
		return rules.Payload{}, fmt.Errorf(
			"%w: the query names view %q at object %q",
			rules.ErrInvalidView,
			query.View,
			query.Object,
		)
	}

	switch query.View {
	case ViewBoltCard:
		return deriveBoltCard(state, query.Object)
	case ViewStockList:
		return deriveStock(state, query.Limit)
	default:
		// Refused rather than answered with an empty payload: the view name is what
		// picks the renderer (`Payload.View`), and a card rendered from nothing is a
		// blank document that says nothing went wrong.
		return rules.Payload{}, fmt.Errorf("%w: %q is not a view this system declares",
			rules.ErrInvalidView, query.View)
	}
}

// deriveBoltCard answers the `bolt-card` view for one spool.
func deriveBoltCard(state rules.State, object rules.ObjectID) (rules.Payload, error) {
	found, exists := state.Lookup(object)
	if !exists {
		return rules.Payload{}, fmt.Errorf("%w: %q", ErrNoSuchSpool, object)
	}

	spool, err := readSpool(found)
	if err != nil {
		return rules.Payload{}, err
	}

	payload, err := rules.NewPayload(ViewBoltCard, boltCard{
		Spool:  found.ID.String(),
		Length: spool.Length,
		Colour: colourName(spool.Colour),
	})
	if err != nil {
		return rules.Payload{}, fmt.Errorf("notfive: answering %q: %w", ViewBoltCard, err)
	}

	return payload, nil
}

// deriveStock answers the `stock-list` view for the whole tabletop.
//
// `Limit` is honoured because `rules.Query` says zero means the system's own default
// and a negative is refused, and §14's "a maximal output" needs a maximal that is still
// a number. **An object of a kind this system does not declare is skipped rather than
// refused** — that is the render-side half of S-14.7's inertness, and the half a
// list-shaped view gets for free: a table with a placement nothing understands still
// lists.
func deriveStock(state rules.State, limit int) (rules.Payload, error) {
	rows := make([]stockRow, 0, state.Len())

	for _, object := range state.Objects() {
		if object.Kind != KindSpool || limit > 0 && len(rows) == limit {
			continue
		}

		spool, err := readSpool(object)
		if err != nil {
			// Skipped, not fatal. A row this system cannot describe is not a reason to
			// refuse to describe the others, and refusing here would put a stale kind
			// in the way of reading a whole table.
			continue
		}

		rows = append(rows, stockRow{
			Spool:  object.ID.String(),
			Length: spool.Length,
			Colour: colourName(spool.Colour),
		})
	}

	payload, err := rules.NewPayload(ViewStockList, rows)
	if err != nil {
		return rules.Payload{}, fmt.Errorf("notfive: answering %q: %w", ViewStockList, err)
	}

	return payload, nil
}

// colourName renders a colour index.
//
// **Out of range renders as undyed rather than as a number or a panic**, because the
// index came out of a game object's `Data` and S-3.3's "untrusted disk content" applies
// to a state document as much as to a page. A renderer showing undyed for a spool whose
// colour index is nonsense is showing a table; one panicking is showing a crash.
func colourName(index int) string {
	if index < 0 || index >= len(palette) {
		return palette[0]
	}

	return palette[index]
}
