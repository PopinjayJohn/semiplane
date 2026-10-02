package dnd5e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// # The scope a formula is evaluated over
//
// A formula is an arithmetic expression over **named integers**, and the names are
// the whole of the vocabulary. That is deliberate and it is what keeps house rules
// data-level: ADR 0012's second example is "change a DC formula constant", and a
// constant the pack cannot see is a constant no module can change without a fork.
//
//   - Every entry of the pack's `reference.constants`, by name.
//   - `level` — the creature's level.
//   - `prof` — its proficiency bonus, read from the table.
//   - `ability` — **only** when the formula declares `for:`, which is what binds it.
//   - `ability.<slug>` — every declared ability's modifier, always.
//   - `dc.<slug>` — every declared named difficulty class.
//
// Reading a name that is not in scope is a load failure, not a zero. A formula whose
// typo silently evaluated a missing variable to zero would resolve every DC that used
// it to a plausible wrong number, which is precisely the "silently misresolve" that
// §10.8 refuses a resume to prevent — arrived at by a different road.
//
// # Why there is no division
//
// `+`, `-`, `*` and parentheses, and nothing else. No division, because a formula
// needing one is a formula whose **rounding rule** is not expressed by the data, and
// a rounding rule nobody can see is a rule that changes silently when someone edits
// an operand. `TestTheFormulaLanguageRefusesEverythingElse` feeds it each operator it
// does not have and requires a refusal rather than an answer.
//
// # Determinism
//
// `Expression.Evaluate` reads a `map[string]int` and never ranges over one: a
// missing name is a refusal, not an iteration. The terms of a sum are a slice built
// at parse time in source order, so the addition is the author's order and never a
// map's.

const (
	// scopeLevel and scopeProf are the two engine-computed names every pack formula
	// may use.
	scopeLevel = "level"
	scopeProf  = "prof"

	// scopeAbility is the bound ability, available only to a formula that declared
	// what to bind it to.
	scopeAbility = "ability"

	// scopePrefixAbility and scopePrefixDC are namespaces rather than names, so a
	// formula naming `ability` and `ability.dexterity` reads as the two things it is.
	scopePrefixAbility = "ability."
	scopePrefixDC      = "dc."
)

// EveryFor is the `for:` value that means "once per declared ability".
const EveryFor = "every"

// FormulaForEvery is what a compiled formula declares about `for`, and the two
// answers plus "neither".
type FormulaForEvery int

const (
	// ForNone is a formula evaluated once, with no bound ability.
	ForNone FormulaForEvery = iota

	// ForAbility is a formula evaluated once per declared ability, and one per
	// declared ability it named.
	ForAbility
)

// compiledFormula is a formula's parsed expression, its clamp, and what it binds.
type compiledFormula struct {
	// Name is the key it was declared under, so a refusal names the row.
	Name string

	// Expr is the parsed body.
	Expr Expression

	// Clamp bounds the result, and a zero `Expression` means no bound. Two zero
	// values rather than a pointer, because `clamp: {min: "0"}` is a legitimate
	// declaration with no maximum and the difference between them is a cap that
	// never fires.
	Min Expression
	Max Expression

	// Clamped reports whether either bound was declared, which `Evaluate` needs and a
	// zero `Expression` cannot say.
	Clamped bool

	// For is whether and how the bare `ability` name is bound.
	For FormulaForEvery

	// Abilities is the named abilities when `For` is `ForAbility` and the declaration
	// was not `every`; nil means every declared ability, in declaration order.
	Abilities []string
}

// Expression is a parsed arithmetic expression: a sum of signed terms.
//
// **A tree, and not a string.** S-10.4 makes determinism a discipline, and an
// expression held as text is re-parsed on every resolution — which is both slower
// and one more place for a half-implemented parser to live. Parsed once at load, the
// expression is four node kinds and an evaluation with no control flow at all.
type Expression struct {
	source string
	sum    []term
}

// term is one signed addend of a sum, and an addend may be a **product**: `2 * ac_dex_cap`
// is one term, not two.
//
// The multiplicands hang off the first factor rather than in a tree of their own, and the
// reason is that the grammar is deliberately flat: `sum` of `term` of `factor`, with
// `factor` handling the parenthesised case by recursion. A product inside a `term` keeps
// `Expression`'s shape to three node kinds — which is what makes it a language small
// enough to read in one screen and to test exhaustively, and a language with a real
// expression tree is a language somebody will eventually reach `x :=` out of.
type term struct {
	sign   int
	factor factor
}

// factor is one multiplicand, and the `times` list are the ones after it.
type factor struct {
	literal  int
	name     string
	parent   *Expression
	isParent bool

	// times are the factors this one is multiplied by, in written order. A literal, a
	// name or a parenthesised expression may follow a `*`, and the empty list is the
	// common case.
	times []factor
}

// constantNames returns every name a scope may hold, for `validateVariables`.
//
// **Derived from the pack rather than listed**, which is the only way the check can
// be exhaustive: a name this function does not know about is a name the pack
// introduced, and the check is what says whether a formula may use it.
func (p *Pack) constantNames() []string {
	names := make([]string, 0, len(p.constants)+len(p.abilities)+len(p.dcs)+3)

	names = append(names, slices.Sorted(maps.Keys(p.constants))...)

	for _, ability := range p.abilities {
		names = append(names, scopePrefixAbility+ability.slug)
	}

	for _, named := range p.dcs {
		names = append(names, scopePrefixDC+named.slug)
	}

	// The bound `ability` is appended rather than sorted in, because whether it is in
	// scope is a property of the *formula* and not of the pack — see `variableInScope`.
	return append(names, scopeLevel, scopeProf)
}

// variableInScope reports whether name may appear in a formula binding `bound`.
//
// Three answers and not two, and the third is the one this function exists for:
// `ability` is in scope **only** for a formula that declared what to bind it to, and
// is out of scope for every other formula in the same pack. A check that only knew
// "in" and "out" would have to either allow it everywhere — so `attack_bonus` could
// be evaluated with an unbound `ability` and read a zero — or refuse it everywhere,
// which is the same failure with a different message.
func (p *Pack) variableInScope(name string, bound bool) bool {
	if name == scopeAbility {
		return bound
	}

	if slices.Contains(p.constantNames(), name) {
		return true
	}

	return false
}

// compileFormula parses one formula row and checks every name it names.
//
// The `for` handling is where the two forms meet: `every` binds every declared
// ability, a slug binds exactly that one, and **an unrecognised `for` is a load
// failure** rather than a formula evaluated once. `for: dexterityy` would otherwise
// become a formula that evaluates once with no bound ability, and the *value* it
// produced would be plausible.
func (p *Pack) compileFormula(name string, file formulaFile) (compiledFormula, error) {
	compiled := compiledFormula{Name: name}

	bound := false

	switch file.For {
	case "":
	case EveryFor:
		compiled.For = ForAbility

		bound = true
	default:
		row, declared := p.AbilityAt(file.For)
		if !declared {
			return compiledFormula{}, fmt.Errorf(
				"%w: formula %q binds %q",
				ErrNoSuchAbility,
				name,
				file.For,
			)
		}

		compiled.For = ForAbility
		compiled.Abilities = []string{row.slug}

		bound = true
	}

	expr, err := parseExpression(file.Expr)
	if err != nil {
		return compiledFormula{}, fmt.Errorf("%w: formula %q: %w", ErrBadFormula, name, err)
	}

	if invalid := p.validateVariables(name, expr, bound); invalid != nil {
		return compiledFormula{}, invalid
	}

	compiled.Expr = expr

	if file.Clamp == nil {
		return compiled, nil
	}

	low, err := parseExpression(defaulted(file.Clamp.Min, "0"))
	if err != nil {
		return compiledFormula{}, fmt.Errorf(
			"%w: formula %q: clamp min: %w",
			ErrBadFormula,
			name,
			err,
		)
	}

	high, err := parseExpression(defaulted(file.Clamp.Max, "0"))
	if err != nil {
		return compiledFormula{}, fmt.Errorf(
			"%w: formula %q: clamp max: %w",
			ErrBadFormula,
			name,
			err,
		)
	}

	if err := p.validateVariables(name+" clamp min", low, bound); err != nil {
		return compiledFormula{}, err
	}

	if err := p.validateVariables(name+" clamp max", high, bound); err != nil {
		return compiledFormula{}, err
	}

	compiled.Min, compiled.Max, compiled.Clamped = low, high, true

	return compiled, nil
}

// validateVariables refuses a name no scope holds.
//
// Walked in **source order** so the refusal names the first offending variable as
// written rather than the first one a traversal happened to reach, which for a
// factored expression is not the same variable.
func (p *Pack) validateVariables(where string, expr Expression, bound bool) error {
	for _, each := range expr.sum {
		// **Every multiplicand, not only the first**, and the reason is that a formula
		// naming a variable only in its second factor is the same mistake: a typo that
		// resolved to zero would produce a plausible wrong number rather than a refusal,
		// which is the failure §10.8's ruleset row exists to prevent arriving by a
		// different road.
		//
		// Walked in **written order**, so the refusal names the first offending name as
		// the author spelled it.
		for _, multiplicand := range append([]factor{each.factor}, each.factor.times...) {
			if multiplicand.name == "" {
				continue
			}

			if !p.variableInScope(multiplicand.name, bound) {
				return fmt.Errorf(
					"%w: formula %q names %q, which is not in scope",
					ErrBadFormula,
					where,
					multiplicand.name,
				)
			}
		}
	}

	return nil
}

// Evaluate returns the formula's value over scope, with its clamp applied.
//
// **Integer arithmetic throughout**, and the reason is a rounding rule rather than
// pedantry: a half-point of armour class and a threshold on a d20 are both integers,
// and a formula that could produce a fraction would have to say what it does with
// one.
func (c compiledFormula) Evaluate(scope map[string]int) (int, error) {
	total, err := c.Expr.Evaluate(scope)
	if err != nil {
		return 0, err
	}

	if !c.Clamped {
		return total, nil
	}

	low, err := c.Min.Evaluate(scope)
	if err != nil {
		return 0, fmt.Errorf("dnd5e: formula %q: clamp min: %w", c.Name, err)
	}

	high, err := c.Max.Evaluate(scope)
	if err != nil {
		return 0, fmt.Errorf("dnd5e: formula %q: clamp max: %w", c.Name, err)
	}

	// `min` and `max` rather than a comparison chain, so that a formula declaring a
	// cap below its floor resolves to the floor — a pack's mistake that resolves to
	// something a GM can see rather than to something no rule can explain.
	return min(max(total, low), high), nil
}

// Source returns the expression as it was written, for a status page naming what a
// campaign's armour class is computed from.
func (c compiledFormula) Source() string { return c.Expr.source }

// String returns the formula's name and its source, which is what an operator
// reading a status page wants and never a resolved value.
func (c compiledFormula) String() string { return c.Name + " = " + c.Expr.source }

// FormulaSource returns the parsed expression as it was written.
//
// Exported through `Formula` rather than by exporting `Expression`'s field, because
// `compiledFormula` is an unexported type an unexported method of `Pack` returns and
// an exported method returning an unexported type is exactly the shape
// `revive`'s `unexported-return` rule forbids.
func (p *Pack) FormulaSource(name string) (string, bool) {
	formula, found := p.formulas[name]
	if !found {
		return "", false
	}

	return formula.Source(), true
}

// The operator characters, as a set rather than a `switch`, because the parser and
// the refuser have to agree on the list and two lists would be two answers.
const (
	opPlus  = '+'
	opMinus = '-'
	opStar  = '*'
	opOpen  = '('
	opClose = ')'
)

// parseExpression reads one arithmetic expression and nothing else.
//
// A hand-written scanner rather than `go/parser` and rather than `text/template`, and
// both exclusions have reasons. `go/parser` would put a whole language into a pack's
// grammar vocabulary and give a pack author `x := make(chan int)` to try; a template
// is not an arithmetic language at all and would be an `eval` with a resolver's
// authority behind it. **Four operators and a name** is a language small enough to
// read in one screen and to test exhaustively, which is what
// `TestTheFormulaLanguageRefusesEverythingElse` does.
func parseExpression(text string) (Expression, error) {
	source := strings.TrimSpace(text)

	if source == "" {
		return Expression{}, fmt.Errorf("%w: it is empty", ErrBadFormula)
	}

	scan := &scanner{text: withoutSpaces(source)}

	expr, err := scan.sum(0)
	if err != nil {
		return Expression{}, err
	}

	if scan.rest() != "" {
		return Expression{}, fmt.Errorf("%w: %q has %q after a complete expression",
			ErrBadFormula, source, scan.rest())
	}

	// The **display** source, restored after the scan, and it is a one-line restoration
	// for a real reason: `expr.expr: "ac_base + ability.dexterity"` is what a status page
	// shows a GM and what a pack author reads their own file against, and a card
	// displaying `ac_base+ability.dexterity` — the scanner's own spelling — is a card
	// that does not match the file. The *parsed* form has no spaces because the language
	// has no spaces; the text a human wrote does, and that is a display concern.
	expr.source = source

	return expr, nil
}

// withoutSpaces removes every ASCII space and tab from text.
//
// **Stripped rather than skipped at the scanner's boundaries**, and the reason is that
// a space is not part of this language: `ac_base + ability.dexterity` and
// `ac_base+ability.dexterity` are one formula, and a scanner that treated the space as
// "a token begins here" would have had to decide at every boundary whether a space was
// legal there. Stripping it once makes every boundary a boundary between two things
// rather than between two things and a separator.
//
// Tabs and spaces only, and not `unicode.IsSpace`: a formula comes from a pack, and a
// non-breaking space is a typo a pack author would want told about rather than
// silently swallowed.
func withoutSpaces(text string) string {
	return strings.Map(func(char rune) rune {
		if char == ' ' || char == '\t' {
			return -1
		}

		return char
	}, text)
}

// maxNesting bounds a formula's parenthesis depth.
//
// Bounded rather than unbounded because a formula comes from a pack and a pack
// comes from a plugin author, and a recursion bounded only by the input is the one
// construct in this package that a hostile string could turn into a stack overflow —
// which is §10.8's "recover at the boundary" covering a plugin's own bug rather than
// an input. Ten is four orders of magnitude above any formula that reads like one.
const maxNesting = 10

// scanner walks one expression, holding the position so that every refusal can name
// what is left.
type scanner struct {
	text string
	at   int
}

// sum reads a sequence of signed terms.
//
// `depth` is the parenthesis nesting, checked against `maxNesting` at the point the
// parenthesis is consumed, so a runaway input is refused before the recursion has
// gone deep enough to matter rather than after.
func (s *scanner) sum(depth int) (Expression, error) {
	expr := Expression{source: s.text}

	sign := 1

	switch s.peek() {
	case opMinus:
		s.at++

		sign = -1
	case opPlus:
		s.at++
	}

	first, err := s.factor(depth)
	if err != nil {
		return Expression{}, err
	}

	expr.sum = append(expr.sum, term{sign: sign, factor: first})

	for {
		switch s.peek() {
		case opPlus:
			s.at++

			sign = 1
		case opMinus:
			s.at++

			sign = -1
		default:
			return expr, nil
		}

		next, err := s.factor(depth)
		if err != nil {
			return Expression{}, err
		}

		expr.sum = append(expr.sum, term{sign: sign, factor: next})
	}
}

// factor reads one multiplicand and then every further one joined to it by `*`.
//
// **The `*` fold lives here rather than in `sum`**, and the reason is where the language
// binds: `1 + 2 * 3` is seven, and the only way to say that with a flat grammar is to let
// the multiplicands attach to their left neighbour while `sum` walks addends. The
// alternative — a precedence-climbing parser — is more general, and it is exactly what
// §12's argument about untrusted input is against: a grammar somebody can extend is a
// grammar with room in it for something nobody reviewed.
func (s *scanner) factor(depth int) (factor, error) {
	first, err := s.primary(depth)
	if err != nil {
		return factor{}, err
	}

	for s.peek() == opStar {
		s.at++

		next, err := s.primary(depth)
		if err != nil {
			return factor{}, err
		}

		first.times = append(first.times, next)
	}

	return first, nil
}

// primary reads one literal, name or parenthesised expression, with no operator.
//
// **Split out of `factor`**, and the reason is that the two have different shapes of
// failure: a `primary` refuses what it cannot read, and a `factor` refuses a `*` with
// nothing after it. In one function the second refusal would have to be a second return
// path from the middle of a `switch`, which is how an operator list and a refusal list
// become two answers to one question.
func (s *scanner) primary(depth int) (factor, error) {
	switch char := s.peek(); {
	case char == opOpen:
		if depth+1 > maxNesting {
			return factor{}, fmt.Errorf(
				"%w: %q nests more than %d deep",
				ErrBadFormula,
				s.text,
				maxNesting,
			)
		}

		s.at++

		inner, err := s.sum(depth + 1)
		if err != nil {
			return factor{}, err
		}

		if s.peek() != opClose {
			return factor{}, fmt.Errorf("%w: %q has an unclosed parenthesis", ErrBadFormula, s.text)
		}

		s.at++

		return factor{parent: &inner, isParent: true}, nil
	case char >= '0' && char <= '9':
		return s.literal()
	default:
		return s.name()
	}
}

// literal reads a run of decimal digits.
//
// A **signed integer literal**, and refusing `+`/`-` inside a literal is the whole of
// why the scanner's sign handling is outside `factor`: `-3` is a unary minus on `3`
// and `3-3` is a difference, and a parser that let a literal carry its own sign would
// need to decide which before it had seen what followed.
func (s *scanner) literal() (factor, error) {
	start := s.at

	for s.at < len(s.text) && s.text[s.at] >= '0' && s.text[s.at] <= '9' {
		s.at++
	}

	if s.at == start {
		return factor{}, fmt.Errorf(
			"%w: %q has a number with no digits in it",
			ErrBadFormula,
			s.text,
		)
	}

	value, err := strconv.Atoi(s.text[start:s.at])
	if err != nil {
		// Overflow, and the only reason `Atoi` can fail here. `s.text[start:s.at]` is a
		// pack's own literal, so quoting it is the half an operator needs.
		return factor{}, fmt.Errorf("%w: %q: %w", ErrBadFormula, s.text[start:s.at], err)
	}

	return factor{literal: value}, nil
}

// name reads a dotted lower-case identifier, or refuses what it is given.
func (s *scanner) name() (factor, error) {
	start := s.at

	for s.at < len(s.text) && nameByte(s.text[s.at], s.at, start) {
		s.at++
	}

	read := s.text[start:s.at]

	if read == "" {
		return factor{}, fmt.Errorf(
			"%w: %q has %q where a number, a name or an operator belongs",
			ErrBadFormula, s.text, string(s.peek()),
		)
	}

	// A name may not *end* in a separator. `ability.` would otherwise scan as a
	// trailing dot after a valid name, and then fail lookup with a message about
	// scope — which sends a pack author looking at the pack's scope when the mistake
	// is a stray character in the formula they wrote.
	if last := read[len(read)-1]; last == '.' || last == '_' {
		return factor{}, fmt.Errorf("%w: %q ends in %q, which no name may",
			ErrBadFormula, s.text, string(last))
	}

	return factor{name: read}, nil
}

// nameByte reports whether char may appear in a name at position `at` of `text`,
// where `from` is where the name began.
//
// **Lower case, ASCII, and `_` never leading.** A name naming a variable that is not
// in scope is already a load failure; a name in the wrong case is that same failure
// with a message saying nothing about what is wrong, and a pack author who wrote
// `Ability.Dexterity` would be told "not in scope" rather than "names are lower
// case". One rule, one message.
//
// A `.` is a separator rather than a name byte and is handled by `name`, because
// `ability.` must be a *syntax* refusal ("a name may not end in a dot") and not a
// *scope* refusal ("`ability.` is not in scope"), which are different bugs with
// different fixes.
func nameByte(char byte, position, from int) bool {
	switch {
	case char >= 'a' && char <= 'z':
		return true
	case char >= '0' && char <= '9':
		return true
	case char == '_':
		return position > from
	case char == '.':
		return position > from
	}

	return false
}

// peek returns the byte at the scanner's position, or zero at the end.
//
// Zero rather than a byte constant, and the loops above all test against explicit
// ranges so zero terminates every one of them.
func (s *scanner) peek() byte {
	if s.at >= len(s.text) {
		return 0
	}

	return s.text[s.at]
}

// rest returns what the scanner has not read.
func (s *scanner) rest() string { return s.text[s.at:] }

// Evaluate returns the expression's value over scope.
//
// **No map iteration and no clock**, and the two are the same property from opposite
// ends: every value is read by name and every term is visited in source order, so
// the result is a function of the scope and of the parsed expression alone. That is
// S-10.4's requirement arriving as a shape rather than as a discipline.
func (e Expression) Evaluate(scope map[string]int) (int, error) {
	total := 0

	for _, each := range e.sum {
		value, err := each.factor.evaluate(scope)
		if err != nil {
			return 0, err
		}

		total += each.sign * value
	}

	return total, nil
}

// evaluate returns one factor's value.
func (f factor) evaluate(scope map[string]int) (int, error) {
	// Left to right, and the order is a property of multiplication's commutativity rather
	// than of anything a caller can observe — the magnitudes a pack may state cannot
	// overflow, and the literal parser refuses a literal that would.
	product, err := f.leading(scope)
	if err != nil {
		return 0, err
	}

	for _, next := range f.times {
		value, err := next.leading(scope)
		if err != nil {
			return 0, err
		}

		product *= value
	}

	return product, nil
}

// leading returns the value of the factor before its `times` list.
func (f factor) leading(scope map[string]int) (int, error) {
	switch {
	case f.isParent:
		return f.parent.Evaluate(scope)
	case f.name == "":
		return f.literal, nil
	default:
		value, found := scope[f.name]
		if !found {
			// A refusal naming the variable, and **not** a zero. A formula naming
			// something out of scope that resolved to zero would produce a plausible
			// number, which is the worst of both: the resolution succeeds and the
			// campaign's rules are quietly wrong.
			return 0, fmt.Errorf("dnd5e: %q is not in scope", f.name)
		}

		return value, nil
	}
}

// String returns the expression as it was written, which is the form a status page
// and a refusal can both show.
//
// **Never a resolved value.** A formula is what a campaign's armour class is computed
// *from*; the computed figure is a placement's business and reaches the state
// document, not a log line.
func (e Expression) String() string { return e.source }

// Digest returns a short stable digest of the expression's source.
//
// Used by the identifier a draw is labelled with, so that two formulas producing the
// same number are the *same* draw for replay purposes: a campaign that resolves an
// attack, upgrades the pack to a pack whose attack formula is textually different but
// numerically identical, and re-resolves would find the same numbers — which is what
// §16.3's roll log wants, and is achieved by hashing the source rather than by
// assuming the numbers.
//
// The `rules.FormatID` the identifier is built with is a concern of the
// composition root's convention, not of this package; the digest is just 16 hex
// characters, which is inside every id charset here.
func (e Expression) Digest() string { return digestOf(e.source) }

// compileID builds the stable identifier this package labels every draw with.
//
// **Fixed shape, and it is `rules`'s to keep.** ADR 0049 (`rules.ID`) is the
// contract that every id in this project is lower case with `_`/`-` separators, so the
// parts are joined with `_`, the label is the ASCII-lower-cased form, and the whole
// is bounded by the id length `rules` enforces. An identifier that did not satisfy
// `rules.ID.Valid` would still *work* — nothing rejects it — and the first symptom
// would be a label rendered from a raw error string somewhere downstream, which is
// the exact failure ADR 0049 exists to prevent.
// compileID joins parts into one lowercase identifier, bounded.
//
// **Every draw this package makes is labelled by one of these**, and the labelling
// is the whole of how determinism is achieved without a counter: `Context.Rand` is
// pure in (seed, label), so two resolutions of the same intent under the same seed
// draw the same numbers as long as their labels are equal — and two *different*
// draws are unrelated as long as their labels are not.
//
// So the label must be a function of what the draw *is*, and never of how many draws
// came before. `roll/2d6/0` is a counter and would make the third attack of a
// campaign depend on the two before it; `attack/c1/greataxe/roll` is what the draw
// is and depends on nothing else.
func compileID(parts ...string) string {
	joined := make([]string, 0, len(parts))

	for _, part := range parts {
		cleaned := strings.Map(func(char rune) rune {
			switch {
			case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '_':
				return char
			case char >= 'A' && char <= 'Z':
				return char - 'A' + 'a'
			default:
				return -1
			}
		}, part)

		if cleaned != "" {
			joined = append(joined, cleaned)
		}
	}

	identifier := strings.Join(joined, "_")

	// Bounded by `rules.ID.Valid`'s own limit, and the limit is not exported so it is
	// stated here with the reason it is the same number: a system id and a label both
	// appear side by side on a status page, and a reader should not learn two bounds.
	//
	// **Truncated from the end and given a digest of the whole.** Keeping the front
	// keeps the label legible — which is what an operator reads — and the digest keeps
	// two long labels that agree on their first `maxLabelLen` bytes from drawing the
	// same number, which would be two creatures whose attacks share a stream and a
	// replay that cannot separate them.
	const maxLabelLen = 64

	if len(identifier) <= maxLabelLen {
		return identifier
	}

	const digestLen = 17

	return identifier[:maxLabelLen-digestLen] + "_" + digestOf(identifier)
}

// digestOf returns 16 hex characters of SHA-256 over text.
//
// A helper rather than a method on `Expression`, because two callers want it and
// neither has an expression.
func digestOf(text string) string {
	digest := sha256.Sum256([]byte(text))

	return hex.EncodeToString(digest[:8])
}
