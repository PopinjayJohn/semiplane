package tokens_test

// The arithmetic gate: `step.js` is evaluated by Go, from the shipped bytes.
//
// # Why this file exists at all
//
// Phase 9's DoD says "the token list is operable by keyboard alone", and UI §7.6
// says `ArrowUp`/`ArrowDown` move focus between placements. There is no Node in
// this repository's toolchain — deliberately, per the note on `make a11y` — so a
// JavaScript test is not available, and a Go test asserting that the source
// *mentions* `ArrowDown` is not a test of anything.
//
// So the rule is arithmetic, and the arithmetic is a restricted grammar that Go
// can evaluate. `step.js` is one `return` of one expression over three numbers;
// this file parses it and computes what a browser would compute, over **every**
// combination of `from`, `delta` and `count`. No transcription, no second
// implementation of the rule to drift from it, and no browser.
//
// # "Unsupported" means red
//
// The restriction is the load-bearing part, and it works in one direction only.
// The grammar has: numbers, `+ - * / %`, parentheses, unary signs, and the three
// parameter names. **Everything else is a parse error**, so if a future edit adds
// a conditional, a loop, a string, a member access, an assignment or a call, the
// build goes red rather than the gate quietly evaluating less of the file.
//
// Two tests make that claim honest rather than decorative:
//
//   - `TestTheArithmeticGateRejectsWhatItCannotCheck` feeds the evaluator a
//     conditional, a loop, a string, a call, a member access, an assignment, a
//     comma expression and an unbound name, and requires it to object to each.
//   - `TestTheArithmeticGateEvaluatesWhatItClaimsTo` evaluates a small set of
//     expressions with known answers, so an evaluator that rejected everything —
//     or accepted everything and computed nonsense — fails here.
//
// There is also **no evaluator-invented vocabulary**. `step.js` can only mention
// `from`, `delta` and `count`, and any other identifier is an error. That is a
// deliberate difference from the map camera's gate, which binds `Math.min` and
// friends: an evaluator that supplies a name its host language does not have is a
// gate on a document rather than on the shipped bytes, and a bare `min(…)` there
// once passed the whole suite and threw `ReferenceError` in a browser.

import (
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/semiplane/semiplane/internal/web/static/js/tokens"
)

// stepSource reads the shipped kernel from disk.
//
// **From disk, not from the embedded copy.** `tokens.Source` would make this a
// comparison of a thing with itself; the file on disk is the artefact a person
// edits, and the embedded bytes are what the browser runs.
// `TestTheServedScriptsAreTheCheckedInScripts` holds the two together, which is
// what makes reading the file here a test of the shipped bytes.
func stepSource(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(tokens.StepFile)
	if err != nil {
		t.Fatalf("read %s: %v\nthis file is the arithmetic kernel and is not "+
			"optional; without it the arrow keys are untested", tokens.StepFile, err)
	}

	return string(raw)
}

// stepFile is the kernel's name, from the package that embeds it.
const stepFile = tokens.StepFile

// parameters are the only names the kernel's expression may mention.
var parameters = []string{"from", "delta", "count"}

// TestTheFocusRuleWrapsOverEveryInput is the behaviour claim: the next row is
// `from` moved by `delta`, wrapped into `0..count-1`.
//
// Exhaustive rather than sampled, because the grid is small and the interesting
// cases are the edges: `from + delta` **negative** (ArrowUp from the first row),
// `from + delta` exactly one past the end (ArrowDown from the last), a delta
// larger than the list (a jump key), and `count == 1` (a one-token table, where
// every arrow must land on the only row).
func TestTheFocusRuleWrapsOverEveryInput(t *testing.T) {
	t.Parallel()

	rule := mustParseKernel(t)

	// Deltas from -12 to 12 rather than only -1 and +1: the rule is "move by
	// delta", and a delta outside the list's length is the case a modulo has to
	// get right rather than being handled specially.
	const maxDelta = 12

	for count := 1; count <= 12; count++ {
		for from := 0; from < count; from++ {
			for delta := -maxDelta; delta <= maxDelta; delta++ {
				got := rule.eval(map[string]float64{
					"from":  float64(from),
					"delta": float64(delta),
					"count": float64(count),
				})

				want := positiveMod(float64(from+delta), float64(count))

				if got != want {
					t.Fatalf("spFocusStep(from=%d, delta=%d, count=%d) = %v, want %v\n"+
						"the rule is a wrapped move, and every row of this grid is a "+
						"keystroke a reader can make", from, delta, count, got, want)
				}
			}
		}
	}
}

// TestTheFocusRuleNeverLandsOutsideTheList is the property a reader would notice
// before the exact index: focus is always on one of the rows.
//
// Separate from the exhaustive grid because it is the one that can be stated as an
// invariant rather than as an equality, and because a rule that is merely
// *wrong* (off by one) can satisfy "in range" for most inputs and fail for one.
func TestTheFocusRuleNeverLandsOutsideTheList(t *testing.T) {
	t.Parallel()

	rule := mustParseKernel(t)

	for count := 1; count <= 12; count++ {
		for from := 0; from < count; from++ {
			for _, delta := range []float64{-1, 1, -2, 3, -7, 11} {
				got := rule.eval(map[string]float64{
					"from":  float64(from),
					"delta": float64(delta),
					"count": float64(count),
				})

				if got < 0 || got >= float64(count) || got != math.Trunc(got) {
					t.Fatalf("spFocusStep(from=%d, delta=%v, count=%d) = %v, which is "+
						"not a row of a list of %d; a focus call with that index moves "+
						"focus nowhere", from, delta, count, got, count)
				}
			}
		}
	}
}

// TestTheKernelDeclaresTheThreeParametersItIsCalledWith holds the signature.
//
// The expression evaluator binds three names, and this asserts the *file* binds
// the same three in the same order. A fourth parameter would be `undefined` in
// the expression and `NaN` in a browser — the evaluator would never see it,
// because it evaluates the expression rather than the signature.
func TestTheKernelDeclaresTheThreeParametersItIsCalledWith(t *testing.T) {
	t.Parallel()

	source := stepSource(t)

	const open = "function spFocusStep("

	_, after, ok := strings.Cut(source, open)
	if !ok {
		t.Fatalf("%s declares no %s; the arithmetic gate finds its expression "+
			"through this signature, and a kernel that does not have one is not "+
			"being evaluated", stepFile, open)
	}

	rest := after

	declared, _, closed := strings.Cut(rest, ")")
	if !closed {
		t.Fatalf("%s has an unterminated parameter list after %s", stepFile, open)
	}

	names := strings.Split(declared, ",")

	// The count first: the comparison below indexes by position, and a kernel
	// declaring two parameters would otherwise panic rather than fail.
	if len(names) != len(parameters) {
		t.Fatalf("%s declares %d parameters, want %d: %q. The evaluator binds "+
			"%v, so a fourth parameter would be `undefined` in the expression and "+
			"a missing one would make it `NaN` in a browser — and neither is "+
			"visible to an evaluator that reads only the expression",
			stepFile, len(names), len(parameters), declared, parameters)
	}

	for index, name := range parameters {
		if strings.TrimSpace(names[index]) != name {
			t.Errorf("%s parameter %d is %q, want %q; the evaluator binds %v and "+
				"a parameter it does not bind evaluates to nothing", stepFile,
				index, strings.TrimSpace(names[index]), name, parameters)
		}
	}
}

// TestTheKernelHasExactlyOneReturn is what makes "the expression" unambiguous.
//
// The evaluator reads the first `return` it finds and stops at the next `;`. A
// second `return` — a guard clause, an early exit — would be invisible to it, so
// the count is asserted rather than the first-one-wins behaviour relied on.
func TestTheKernelHasExactlyOneReturn(t *testing.T) {
	t.Parallel()

	count := countReturns(stepSource(t))

	if count != 1 {
		t.Errorf("%s has %d `return` statements, want exactly 1; the arithmetic "+
			"gate evaluates the one expression it can find and would silently "+
			"ignore a second", stepFile, count)
	}
}

// countReturns counts `return` used as a statement, skipping comments and
// strings.
//
// Comments matter more than they look: this kernel's header is a comment that
// discusses `return` in prose, so a plain `strings.Count` finds six and a parser
// that trusted it would refuse the file.
func countReturns(source string) int {
	count := 0

	for index := 0; index+len("return") <= len(source); index++ {
		if source[index:index+len("return")] != "return" {
			continue
		}

		if index > 0 && isNameByte(source[index-1]) {
			continue
		}

		if index+len("return") < len(source) && isNameByte(source[index+len("return")]) {
			continue
		}

		if !isStatementStart(source, index) {
			continue
		}

		count++
	}

	return count
}

// isStatementStart reports whether offset begins a `return` that is code rather
// than prose — not inside a `//` comment, a `/* */` comment or a string.
func isStatementStart(source string, offset int) bool {
	line := source[:offset]

	// A `//` comment on this line swallows everything after it.
	if _, after, found := strings.CutLast(line, "\n"); found {
		if strings.Contains(after, "//") {
			return false
		}
	} else if strings.Contains(line, "//") {
		return false
	}

	// A block comment anywhere before it on the same line, or an unclosed one
	// carried down from earlier lines. Counting both is enough for a kernel whose
	// comments are line comments and a `/* … */` header.
	depth := strings.Count(source[:offset], "/*") - strings.Count(source[:offset], "*/")

	return depth <= 0
}

// --- The evaluator ----------------------------------------------------------------

// parseStrict parses one expression and refuses anything left over.
//
// **The entry point the refusal contract belongs to.** `parseExpression` stops at
// the end of a value and hands back the remainder, which is how a parenthesised
// group learns it has ended — and which means `parseExpression("from = 1")`
// returns the name `from` and no error at all. A caller that forgot to look at the
// remainder would be evaluating the first word of every line of JavaScript it was
// given, and would report success. `parseKernel` checks the remainder, and this
// is that check as a function the control tests can call directly.
func parseStrict(input string) (node, error) {
	parsed, remainder, err := parseExpression(input)
	if err != nil {
		return nil, err
	}

	if trimmed := strings.TrimSpace(remainder); trimmed != "" {
		return nil, fmt.Errorf("%q is outside the grammar; %q follows an expression "+
			"the evaluator has already finished", trimmed, input)
	}

	return parsed, nil
}

// kernel is a parsed `spFocusStep`: a parameter list and an expression.
type kernel struct {
	params []string
	expr   node
}

// eval computes the expression in the given environment.
func (k kernel) eval(env map[string]float64) float64 {
	return k.expr.eval(env)
}

// mustParseKernel parses the shipped kernel or fails the test.
func mustParseKernel(t *testing.T) kernel {
	t.Helper()

	parsed, err := parseKernel(stepSource(t))
	if err != nil {
		t.Fatalf("%s is outside the grammar this gate can evaluate: %v\n"+
			"that is a red build on purpose — either narrow the kernel back to one "+
			"expression over its three parameters, or widen the evaluator and hold "+
			"it to the same two tests", stepFile, err)
	}

	return parsed
}

// parseKernel reads the single `return` expression out of the kernel.
func parseKernel(source string) (kernel, error) {
	at := indexReturn(source)
	if at < 0 {
		return kernel{}, errors.New("no `return` statement")
	}

	rest := source[at+len("return"):]

	expr, _, terminated := strings.Cut(rest, ";")
	if !terminated {
		return kernel{}, errors.New("the `return` statement is not terminated by `;`")
	}

	if countReturns(source) != 1 {
		return kernel{}, errors.New("more than one `return` statement in the kernel")
	}

	parsed, remainder, err := parseExpression(strings.TrimSpace(expr))
	if err != nil {
		return kernel{}, err
	}

	if remainder != "" {
		return kernel{}, fmt.Errorf("trailing input after the expression: %q", remainder)
	}

	return kernel{params: parameters, expr: parsed}, nil
}

// indexReturn finds the offset of the kernel's `return` keyword, or -1.
//
// A keyword rather than a substring match: `returnValue` is not a `return`, and a
// parser that cannot tell them apart is a parser reading prose.
func indexReturn(source string) int {
	for index := 0; index+len("return") <= len(source); index++ {
		if source[index:index+len("return")] != "return" {
			continue
		}

		if index > 0 && isNameByte(source[index-1]) {
			continue
		}

		if index+len("return") < len(source) && isNameByte(source[index+len("return")]) {
			continue
		}

		if !isStatementStart(source, index) {
			continue
		}

		return index
	}

	return -1
}

// node is one parsed expression.
type node interface {
	eval(env map[string]float64) float64
}

// numberNode is a literal.
type numberNode float64

func (n numberNode) eval(map[string]float64) float64 { return float64(n) }

// nameNode is a parameter, and only ever one of the three.
type nameNode string

func (n nameNode) eval(env map[string]float64) float64 {
	value, ok := env[string(n)]
	if !ok {
		// Unreachable: the parser refuses a name outside `parameters`. Returning
		// NaN rather than zero keeps a future edit from producing a plausible
		// wrong answer.
		return math.NaN()
	}

	return value
}

// binaryNode is an arithmetic operation.
type binaryNode struct {
	op          string
	left, right node
}

func (b binaryNode) eval(env map[string]float64) float64 {
	left := b.left.eval(env)
	right := b.right.eval(env)

	switch b.op {
	case "+":
		return left + right
	case "-":
		return left - right
	case "*":
		return left * right
	case "/":
		return left / right
	case "%":
		return math.Mod(left, right)
	default:
		// Unreachable: the parser emits only the five operators above.
		return math.NaN()
	}
}

// unaryNode is a sign.
type unaryNode struct {
	op   string
	what node
}

func (u unaryNode) eval(env map[string]float64) float64 {
	value := u.what.eval(env)
	if u.op == "-" {
		return -value
	}

	return value
}

// parseExpression parses a full expression and returns the unconsumed remainder.
//
// The remainder is returned rather than required to be empty at every level, which
// is what lets the top level distinguish `a + b` (consumed) from `a; b` (a `;` the
// grammar does not have, so `b` is left over and the caller complains).
func parseExpression(input string) (node, string, error) {
	left, rest, err := parseTerm(input)
	if err != nil {
		return nil, "", err
	}

	for {
		rest = skipSpace(rest)

		if rest == "" || rest[0] == ')' || !strings.ContainsRune("+-", rune(rest[0])) {
			return left, rest, nil
		}

		right, remaining, err := parseTerm(rest[1:])
		if err != nil {
			return nil, "", err
		}

		left = binaryNode{op: string(rest[0]), left: left, right: right}
		rest = remaining
	}
}

// parseTerm parses the multiplicative level.
//
// **The operator is the first character after whitespace, not the first
// occurrence anywhere in the remainder.** Searching with `IndexAny` was the first
// version and it was wrong in a way the fixture would not have caught: on
// `from + delta) % count` it found the `%` eleven characters in, skipped over the
// `+`, and built `from % count` — a different expression that still parses and
// still evaluates. A gate reading a different expression than the file holds is
// the failure this whole file exists to prevent, so the operator position is
// decided by the next character and nothing else.
func parseTerm(input string) (node, string, error) {
	left, rest, err := parseUnary(input)
	if err != nil {
		return nil, "", err
	}

	for {
		rest = skipSpace(rest)

		if rest == "" || rest[0] == ')' || !strings.ContainsRune("*/%", rune(rest[0])) {
			return left, rest, nil
		}

		right, remaining, err := parseUnary(rest[1:])
		if err != nil {
			return nil, "", err
		}

		left = binaryNode{op: string(rest[0]), left: left, right: right}
		rest = remaining
	}
}

// parseUnary parses any number of leading signs and one primary.
func parseUnary(input string) (node, string, error) {
	rest := skipSpace(input)

	sign := "+"

	for rest != "" && (rest[0] == '+' || rest[0] == '-') {
		sign = string(rest[0])
		rest = skipSpace(rest[1:])
	}

	what, remaining, err := parsePrimary(rest)
	if err != nil {
		return nil, "", err
	}

	if sign == "-" {
		return unaryNode{op: "-", what: what}, remaining, nil
	}

	return what, remaining, nil
}

// parsePrimary parses a number, a parameter name, or a parenthesised expression.
func parsePrimary(input string) (node, string, error) {
	rest := skipSpace(input)

	if rest == "" {
		return nil, "", errors.New("the expression ends where a value was expected")
	}

	switch {
	case rest[0] == '(':
		inner, remaining, err := parseExpression(rest[1:])
		if err != nil {
			return nil, "", err
		}

		closed := skipSpace(remaining)
		if closed == "" || closed[0] != ')' {
			return nil, "", errors.New("a `(` is not closed")
		}

		return inner, closed[1:], nil

	case unicode.IsDigit(rune(rest[0])):
		digits := 0

		for digits < len(rest) && unicode.IsDigit(rune(rest[digits])) {
			digits++
		}

		value, err := strconv.ParseFloat(rest[:digits], 64)
		if err != nil {
			return nil, "", fmt.Errorf("unparseable number %q", rest[:digits])
		}

		return numberNode(value), rest[digits:], nil

	case isNameByte(rest[0]):
		end := 0

		for end < len(rest) && isNameByte(rest[end]) {
			end++
		}

		name := rest[:end]

		if !slices.Contains(parameters, name) {
			// The refusal that matters: an evaluator that bound arbitrary names
			// would accept `Math.min`, `window` and `location` and evaluate a
			// document rather than the file. See the file header.
			return nil, "", fmt.Errorf("%q is not one of the parameters %v", name, parameters)
		}

		return nameNode(name), rest[end:], nil

	default:
		return nil, "", fmt.Errorf(
			"%q is outside the grammar; this evaluator reads numbers, %v, parentheses, "+
				"unary signs and + - * / %% — nothing else", string(rest[0]), parameters)
	}
}

// isNameByte reports whether a byte can be part of a JavaScript identifier.
func isNameByte(char byte) bool {
	return char == '_' || char == '$' ||
		(char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9')
}

// skipSpace drops leading whitespace, including newlines.
func skipSpace(input string) string {
	return strings.TrimLeft(input, " \t\r\n")
}

// positiveMod is Go's `%` for a possibly negative dividend, returned as a
// non-negative residue.
//
// This is the reference the shipped expression is compared against, and it is
// five lines rather than a second parser: the claim under test is that the file's
// bytes compute this, and the definition here is the specification.
func positiveMod(dividend, divisor float64) float64 {
	if divisor == 0 {
		return math.NaN()
	}

	return math.Mod(math.Mod(dividend, divisor)+divisor, divisor)
}
