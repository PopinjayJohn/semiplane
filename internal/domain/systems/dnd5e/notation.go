package dnd5e

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

// # The notation, and why it is a parser here and data to a client
//
// §10.3's sentence is that a grammar exists so "the client can preview and validate
// an expression *before* sending it", and that "**the protocol never assumes d20**".
// Both halves are met by the same decision: **the shapes are the pack's, and the
// engine reads them.** `dice.sizes` is what `Grammar`'s RE2 patterns are built from
// and what `Parse` refuses anything outside, so a d20 system is this pack's
// configuration and not this engine's assumption. Change `dice.sizes` and the grammar
// a client validates against changes with it — which is what
// `TestTheGrammarIsBuiltFromThePacksDice` holds.
//
// The grammar is still only a *convenience*, and `Parse` is the authority, which is
// `rules.Term`'s own wording. Two tests hold the two together and they are
// deliberately **not symmetric**, because the divergence has a direction that hurts
// more than its opposite:
//
//   - `TestEveryExpressionTheGrammarAcceptsParses` — patterns ⊆ `Parse`. Strict and
//     exhaustive over generated strings, and it is the direction that matters: a
//     pattern that accepts what the server refuses is a client that told a player their
//     roll was fine and then refused it.
//   - `TestTheGrammarDivergesFromParseOnlyInTheFourNamedWays` — the other direction,
//     enumerated rather than asserted as a general claim. Four forms `Parse` accepts and
//     no pattern covers, listed one by one with the reason each exists, so that
//     widening the language fails the list instead of quietly changing what a browser
//     will let a player type.
//
// # The expression
//
// A sum of dice terms and flat modifiers: `2d6+3`, `1d20-1`, `4d6`, `1d20+2`.
// That is the whole language, and it is enough for this engine's slice: an attack
// roll is a die plus a bonus, and a damage expression is dice plus a bonus.
//
// **No parentheses, no division, no advantage in the text.** Each of those is a thing
// this engine deliberately gets from elsewhere — advantage from the condition table
// (`creature.go`'s `Advantage`), a clamp from the pack's formula — and a notation
// that could express a clamp would give a client two ways to say the same thing with
// no rule about which wins.

// Expr is one parsed roll expression.
//
// **A struct, not a map and not a string.** `rules.Expr.Node` is `any` for the reason
// §10.3 rules out a shared tree, and the proof that `any` earns its keep is a system
// putting its *own* type in it: semiplane routes the value back to whoever parsed it
// (`Expr.Owner`) and reads nothing from it, and a `string` would have been a value two
// systems could both appear to understand.
type Expr struct {
	// dice are the dice this expression rolls, in written order.
	dice []diceTerm

	// flat are the modifiers, in written order.
	flat []flatTerm
}

// diceTerm is one `NdM` group.
type diceTerm struct {
	// Count is how many dice, at least one. `0d6` is refused: it sums to nothing, and
	// a damage expression of `0d6` is a mistake rather than an intention.
	Count int

	// Faces is the die's face count, and **is one the pack declared** — see the type
	// comment. A `d7` is refused because the pack has no d7, which is what makes the
	// notation data.
	Faces int
}

// flatTerm is one `+N` or `-N`.
type flatTerm struct {
	// Negative is the sign, so the term's value is `-Value` when it is set.
	Negative bool

	// Value is the magnitude, at least one for the same reason `Count` is.
	Value int
}

// Value returns the term's signed value.
func (t flatTerm) signed() int {
	if t.Negative {
		return -t.Value
	}

	return t.Value
}

// Roll evaluates the expression against a seeded source, and returns the total, the
// per-die and per-modifier breakdown, and the dice that were kept.
//
// **`source` is always `call.Rand(label)` and never a package-level function.** S-10.4
// forbids reaching for ambient randomness directly; P1d's lint rule is what refuses
// the call, and this function's signature is what makes the sanctioned call the
// easy one. A `Roll()` method with no argument would have to reach for something, and
// the linter would be the only thing standing between a caller and the wrong answer.
//
// **The dice are returned rather than derived by the caller**, and the reason is the
// 2024 critical rule: "any damage die shows *its own* maximum" needs each die's face
// count beside its face, which a flat `[]int` of faces could not express without the
// caller guessing a maximum. One caller keeps them (`resolveDamage`, which hands them
// to the critical hook) and one ignores them (`resolveRoll`, whose record carries
// faces but not face counts); returning them once is cheaper than two roll paths.
//
// `mode` applies to a **single-die group only**, the same limitation `oneDie`
// documents: 5e's advantage is a rule about one d20, and applying it to `2d6` would
// have to mean "roll 4d6 and keep the best 2", which is a different question. The
// discarded die is discarded entirely — including for the critical check — which is
// why the kept die is the only one reported.
func (e Expr) Roll(source *rand.Rand, mode Advantage) (total int, parts []int, dice []rolledDie) {
	for _, group := range e.dice {
		// Advantage on a single die is `oneDie`'s answer and not this file's, so that
		// there is **one** implementation of "roll twice, keep one" in the package: a
		// resolver and a notation parser agreeing about what advantage means is a
		// coincidence two tests would both have to know about.
		if group.Count == 1 && mode != AdvNone {
			kept := oneDie(1, group.Faces, mode, source)

			parts = append(parts, kept)
			dice = append(dice, rolledDie{Face: kept, Faces: group.Faces})

			total += kept

			continue
		}

		for range group.Count {
			// `IntN(faces) + 1` rather than `IntN(faces+1)`: the latter would accept a
			// zero, and a d20 showing 0 is not a thing a roll can produce. The bias of
			// `IntN` is not discussed here — `math/rand/v2`'s `Uint64n` is unbiased over
			// the full range and `IntN` is documented as unbiased for this purpose, which
			// is the guarantee the audit trail rests on and not something to re-derive.
			face := 1 + source.IntN(group.Faces)

			parts = append(parts, face)
			dice = append(dice, rolledDie{Face: face, Faces: group.Faces})

			total += face
		}
	}

	for _, term := range e.flat {
		value := term.signed()

		parts = append(parts, value)

		total += value
	}

	return total, parts, dice
}

// Digest returns a short stable digest of the expression **as re-rendered**, not of
// the text the client sent.
//
// **The re-rendered form, and that is the whole point.** `Parse` normalises
// whitespace and nothing else, so `2d6 + 3` and `2d6+3` are one expression spelled
// two ways; hashing the source would give them two labels, two streams, and two
// irreproducible rolls from one campaign decision. A digest of `String()` makes the
// label a function of *what was rolled*, which is what the label is for.
func (e Expr) Digest() string { return digestOf(e.String()) }

// Natural returns the dice alone, ignoring every modifier.
//
// **Present only when the expression rolled a die**, because `0` is a legitimate
// natural — a die that showed nothing is impossible, but an expression with no dice
// has no natural at all, and recording `0` for it would put a figure in a roll log
// that means "the roll was a miss" rather than "there was no roll". The optional
// pointer in `rollRecord` is how the two are told apart.
func (e Expr) Natural(source *rand.Rand) (int, bool) {
	if len(e.dice) == 0 {
		return 0, false
	}

	total := 0

	for _, group := range e.dice {
		for range group.Count {
			total += 1 + source.IntN(group.Faces)
		}
	}

	return total, true
}

// DiceCount returns how many dice the expression rolls.
//
// Read by `roll`'s resolver to decide how many dice to take from the source under one
// label, and by `TestTheDiceCountMatchesWhatItRolls` to hold the two together — a
// count that disagreed with the roll would mean the label a draw is filed under is
// wrong, and a mislabelled draw is a draw a replay cannot reproduce.
func (e Expr) DiceCount() int {
	total := 0
	for _, group := range e.dice {
		total += group.Count
	}

	return total
}

// HasDice reports whether the expression rolls any dice at all.
//
// An expression of pure modifiers is legal — a resistance or a flat bonus — and it is
// the reason `Natural` returns a `bool`.
func (e Expr) HasDice() bool { return len(e.dice) > 0 }

// String renders the expression in the notation it was parsed from.
//
// **Re-rendered from the parsed terms rather than the source text.** They agree,
// because `Parse` normalises only whitespace — and re-rendering is what makes the
// roll log show the *resolved* expression, so a log that reads `2d6 + 3` in the table
// and `2d6+3` in the payload is one a reader has to reconcile by eye. The source is
// still on `Expr.Source`, because §16.3's audit wants what the player typed.
func (e Expr) String() string {
	var out strings.Builder

	for idx, group := range e.dice {
		if idx > 0 {
			out.WriteByte('+')
		}

		out.WriteString(strconv.Itoa(group.Count))
		out.WriteByte('d')
		out.WriteString(strconv.Itoa(group.Faces))
	}

	for _, term := range e.flat {
		out.WriteString("+")

		if term.Negative {
			out.WriteByte('-')
		}

		out.WriteString(strconv.Itoa(term.Value))
	}

	return out.String()
}

// parseExpr reads one expression in this system's notation, refusing anything else.
//
// **Refused rather than repaired**, for the reason `rules.NewIntent` refuses rather
// than repairs: an expression a client sent is attacker-influenceable, and a parser
// that guesses what a player meant produces a result nobody can audit against
// `Expr.Source` — which is the whole of §16.3's roll log.
//
// The die faces are checked against the pack rather than a constant, which is the
// mechanism that makes this a d20 system by configuration. See the type comment.
func parseExpr(pack *Pack, text string) (Expr, error) {
	// Whitespace is normalised rather than rejected, and this is the one thing this
	// parser is lenient about: it is stripped from every side of every boundary before
	// anything else runs. A player typing `2d6 + 3` on a phone keyboard is not making
	// a mistake, and a notation that refused it would teach players the notation is
	// hostile rather than teach them to type it without spaces.
	normalised := strings.Map(func(char rune) rune {
		if char == ' ' || char == '\t' {
			return -1
		}

		return char
	}, text)

	if normalised == "" {
		return Expr{}, fmt.Errorf("%w: it is empty", ErrRoll)
	}

	var (
		parsed Expr
		offset int
	)

	for {
		// A sign, but only where one is legal: at the start, or after another term.
		sign := 1

		switch {
		case offset == len(normalised):
			return parsed, nil
		case normalised[offset] == '+':
			offset++
		case normalised[offset] == '-':
			sign = -1

			offset++
		}

		if offset == len(normalised) {
			return Expr{}, fmt.Errorf("%w: %q ends with a sign", ErrRoll, text)
		}

		// A die group.
		if digits, faces, isDice := cutDice(normalised[offset:]); isDice {
			offset += len(digits) + 1 + len(faces)

			if !pack.HasDieFace(toInt(faces)) {
				return Expr{}, fmt.Errorf(
					"%w: %q rolls a d%s, and this pack has no such die; it has %v",
					ErrRoll, text, faces, pack.DieSizes(),
				)
			}

			count := toInt(digits)
			if count < 1 {
				return Expr{}, fmt.Errorf("%w: %q rolls no dice", ErrRoll, text)
			}

			parsed.dice = append(parsed.dice, diceTerm{Count: count, Faces: toInt(faces)})

			continue
		}

		// A flat modifier, which must run to the end: `2d6+3x` is not a modifier of
		// three followed by something.
		literal := readDigits(normalised[offset:])
		if literal == "" {
			return Expr{}, fmt.Errorf(
				"%w: %q has %q where a die group or a number belongs",
				ErrRoll,
				text,
				normalised[offset:],
			)
		}

		offset += len(literal)

		parsed.flat = append(parsed.flat, flatTerm{Negative: sign < 0, Value: toInt(literal)})
	}
}

// cutDice reads `NdM` from the front of text, and reports whether it did.
//
// **A hand-rolled split rather than a regexp**, and the reason is that a regexp would
// have to be built from the pack's die sizes to be *correct*, and the two would then
// be two answers to "which dice exist". One scan against `pack.HasDieFace` cannot
// disagree with the pack, which is the property `rules.Term` asks for and cannot
// check.
func cutDice(text string) (count, faces string, ok bool) {
	marker := strings.IndexByte(text, 'd')
	if marker < 1 {
		return "", "", false
	}

	count = text[:marker]
	if !allDigits(count) {
		return "", "", false
	}

	faces = readDigits(text[marker+1:])
	if faces == "" {
		return "", "", false
	}

	return count, faces, true
}

// readDigits returns the leading run of decimal digits, or empty.
func readDigits(text string) string {
	end := 0

	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}

	return text[:end]
}

// allDigits reports whether text is one or more decimal digits.
//
// **One or more, never zero-length**: `allDigits("")` must be false, and a
// `len > 0 &&` check is the only way to say it in one line that reads correctly.
func allDigits(text string) bool {
	if text == "" {
		return false
	}

	for idx := range len(text) {
		if text[idx] < '0' || text[idx] > '9' {
			return false
		}
	}

	return true
}

// toInt reads digits as an integer, and **0 for anything that is not digits**.
//
// Deliberately total and deliberately lossy. Every call site has already checked the
// digits with `allDigits` or `cutDice`, and the only way `toInt` could be asked to
// convert a non-number is if one of those checks were removed — in which case a zero
// produces a wrong roll rather than a panic, and the tests for those checks are the
// ones that would be failing at the same moment.
//
// The alternative is `strconv.Atoi` at four call sites with four error branches, for
// a conversion that cannot fail on checked input, and a function whose error branch is
// unreachable is a function whose error message nobody has ever read.
func toInt(digits string) int {
	total := 0

	for idx := range len(digits) {
		total = total*10 + int(digits[idx]-'0')
	}

	return total
}

// Grammar returns this system's notation, as data a client can validate against.
//
// **The patterns are built from the pack**, and that is what makes this a data
// declaration rather than a hardcoded d20 parser's. See the package comment and
// `TestTheGrammarIsBuiltFromThePacksDice`.
//
// The terms are three shapes in the order a client should offer them: a bare die
// group, a die group with a modifier, and a bare modifier. Order is the system's
// choice and `rules.Grammar.Terms` says so — a client presents them in the order the
// system thinks about them rather than in an order derived from a map.
func Grammar(pack *Pack) rules.Grammar {
	sizes := dieSizePattern(pack.sizes)

	// `1-9` rather than `0-9` for the count: `0d6` is refused by `parseExpr`, so a
	// pattern accepting it would be a client that validated an expression the server
	// then refused — and §10.3's sentence about the pattern being a convenience the
	// server can disagree with is a convenience a player should not have to discover.
	//
	// The magnitudes run to **any** number of digits rather than one, and the modifier
	// accepts `+0`. Both widen the pattern towards the language `Parse` accepts rather
	// than away from it, and the reason is which direction a divergence hurts: a
	// pattern that accepts what the server refuses is a client that validated an
	// expression and then got an error, while a pattern that refuses what the server
	// accepts is a client that blocks a roll the rules allow. The first is the
	// confusing one, so the pattern is the wider of the two wherever widening costs
	// nothing — and what is left over is listed, item by item, in
	// `TestTheGrammarDivergesFromParseOnlyInTheFourNamedWays` rather than left to this
	// paragraph.
	count := `[1-9][0-9]*`

	return rules.Grammar{
		Notation: pack.notation,
		Summary:  "dice and modifiers, added or subtracted: 2d6+3, 1d20-1",
		Example:  GrammarExample(pack),
		Terms: []rules.Term{
			{
				Name:    "die",
				Summary: "a bare group of dice, as NdM",
				Pattern: "^" + count + "d" + sizes + "$",
			},
			{
				Name:    "die-with-modifier",
				Summary: "dice and a signed modifier, as NdM+N or NdM-N",
				Pattern: "^" + count + "d" + sizes + `[+-][0-9]+$`,
			},
			{
				Name:    "modifier",
				Summary: "a bare signed modifier, as +N or -N",
				Pattern: `^[+-][0-9]+$`,
			},
		},
	}
}

// GrammarExample returns an expression built from the pack's own die sizes.
//
// **Composed, not written out**, and the reason is the same one the patterns are: a
// hardcoded example is one `parseExpr` could refuse after a pack changed, and a
// grammar whose example does not parse is a broken tooltip — which nothing in `rules`
// can check, because `rules` does not call into the system. That is why
// `TestTheExampleInTheGrammarParses` exists rather than a comment.
func GrammarExample(pack *Pack) string {
	faces, _ := pack.PrimaryDie()

	if !pack.HasDieFace(faces) {
		// A pack whose primary die is not among its own sizes is refused at load
		// (`compileDiceFaces`), so this cannot be reached — and the fallback is here
		// rather than skipped because a helper whose failure path returns an empty
		// string would put an empty tooltip in a build somebody had to work to make.
		return "1d" + strconv.Itoa(faces)
	}

	return "1d" + strconv.Itoa(faces) + "+" + strconv.Itoa(pack.constantOrZero("prof_bonus_base"))
}
