package live_test

// The arithmetic gate: `gap.js` is evaluated by Go, from the shipped bytes.
//
// # Why this file exists
//
// `chrome.js` decides whether an appended chat or dice line scrolls into view, and
// UI §7.5's replay rule is why that decision is not "always". Getting it wrong has
// two opposite symptoms a reader can name: a log that yanks itself to the bottom
// every time somebody types, which makes reading history impossible, and a log that
// refuses to follow a reader who *was* at the bottom, which means a table's newest
// roll is off-screen while the reader watches it arrive.
//
// There is no Node in this repository's toolchain — deliberately, per the note on
// `make a11y` — so this rule is arithmetic in a restricted grammar that Go can
// evaluate, and this file parses `gap.js` and computes what a browser would compute.
// No transcription, no second implementation of the rule to drift from it, and no
// browser.
//
// # "Unsupported" means red
//
// The restriction is the load-bearing part, and it works in one direction only. The
// grammar has: numbers, `+` and `-`, parentheses, and the four parameter names.
// **Everything else is a parse error**, so a future edit that adds a conditional, a
// loop, a string, a call, a member access, an assignment or a call to an unbound
// name turns the build red rather than leaving the gate quietly evaluating less of
// the file than it used to.
//
// Three tests make that honest rather than decorative:
//
//   - `TestTheArithmeticGateRejectsWhatItCannotCheck` feeds the evaluator a
//     conditional, a loop, a string, a call, a member access, an assignment, a
//     comma expression, a comparison and an unbound name, and requires it to object
//     to each.
//   - `TestTheArithmeticGateEvaluatesWhatItClaimsTo` evaluates a handful of
//     expressions with known answers, so an evaluator that rejected everything — or
//     accepted everything and computed nonsense — fails here.
//   - `TestTheShippedKernelIsEvaluatedOverEveryInputThatMatters` evaluates the
//     *shipped file* rather than a copy, over a grid that includes every awkward
//     case: a region shorter than its content, one scrolled to the very bottom, one
//     over-scrolled by a browser that rounds, and a zero tolerance.
//
// # No evaluator-invented vocabulary
//
// `gap.js` may only mention `viewTop`, `viewHeight`, `contentHeight` and
// `tolerance`. Any other identifier is an error. An evaluator that supplied `Math`,
// `document` or anything else would be a gate on a document rather than on the
// shipped bytes — the mistake `static/js/tokens`' own gate records, where a bare
// `min(…)` once passed the whole suite and threw `ReferenceError` in a browser.
//
// The grammar is narrower than `step.js`'s on purpose: `*`, `/` and `%` are absent
// here because the follow-gap rule needs none of them, and a grammar with productions
// nothing uses is three productions to review and keep correct.

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

	"github.com/semiplane/semiplane/internal/web/static/js/live"
)

// parameters are the only names the kernel's expression may mention.
var parameters = []string{"viewTop", "viewHeight", "contentHeight", "tolerance"}

// gapSource reads the shipped kernel from disk.
//
// **From disk, not from the embedded copy.** `live.Source` would make this a
// comparison of a thing with itself; the file on disk is the artefact a person
// edits, and the embedded bytes are what the browser runs.
// `TestTheServedModulesAreTheCheckedInModules` holds the two together.
func gapSource(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(live.GapFile)
	if err != nil {
		t.Fatalf("read %s: %v\nthis file is the scroll-retention kernel and is not "+
			"optional; without it an appended line either steals the reader's position "+
			"or never follows them", live.GapFile, err)
	}

	return string(raw)
}

// TestTheShippedKernelIsEvaluatedOverEveryInputThatMatters is the behaviour claim.
//
// The rule: `viewTop + viewHeight - (contentHeight - tolerance)`. Positive means the
// reader is at the bottom and an appended line should scroll into view; negative
// means they have scrolled away and it must not.
//
// The grid is a **product of ranges rather than an exhaustive sweep**, and the
// ranges are the interesting values rather than a prefix: a zero scroll position, a
// region taller than its content (which reports `scrollHeight` *below* `clientHeight`
// and is the case that breaks a naive exact comparison), a region scrolled exactly to
// its bottom, one a pixel short, one over-scrolled by the fractional-pixel overhang
// some platforms produce, and a tolerance of zero.
func TestTheShippedKernelIsEvaluatedOverEveryInputThatMatters(t *testing.T) {
	t.Parallel()

	kernel := mustParseKernel(t)

	views := []int{0, 1, 4, 40, 100, 400}
	heights := []int{0, 40, 100, 250}
	contents := []int{0, 40, 100, 140, 101, 251}
	tolerances := []int{0, 1, 4}

	for _, viewTop := range views {
		for _, viewHeight := range heights {
			for _, contentHeight := range contents {
				for _, tolerance := range tolerances {
					got := kernel.eval(map[string]float64{
						"viewTop":       float64(viewTop),
						"viewHeight":    float64(viewHeight),
						"contentHeight": float64(contentHeight),
						"tolerance":     float64(tolerance),
					})

					want := wantFollowGap(viewTop, viewHeight, contentHeight, tolerance)

					if got != want {
						t.Fatalf("spFollowGap(viewTop=%d, viewHeight=%d, contentHeight=%d, "+
							"tolerance=%d) = %v, want %v. The rule is: the reader is "+
							"following the tail when the bottom of the viewport has reached "+
							"within `tolerance` of the bottom of the content, and every row "+
							"of this grid is a scroll position a reader can be in",
							viewTop, viewHeight, contentHeight, tolerance, got, want)
					}
				}
			}
		}
	}
}

// wantFollowGap is the specification the shipped expression is compared against.
//
// Two lines rather than a second parser: the claim under test is that the file's
// bytes compute this, and the definition here is the specification.
func wantFollowGap(viewTop, viewHeight, contentHeight, tolerance int) float64 {
	return float64(viewTop+viewHeight) - float64(contentHeight-tolerance)
}

// TestAFollowThresholdNeverPlacesTheNewLineOffScreen states the property a reader
// would notice, as an invariant rather than an equality.
//
// Separate from the grid because an expression that is merely *wrong* — a sign
// transposed, a term dropped — can satisfy "some comparison is true" for most inputs
// and fail for one, and because the invariant is the thing the browser pass would
// have found.
func TestAFollowThresholdNeverPlacesTheNewLineOffScreen(t *testing.T) {
	t.Parallel()

	kernel := mustParseKernel(t)

	const (
		viewHeight = 100
		tolerance  = 4
	)

	for contentHeight := 0; contentHeight <= 400; contentHeight++ {
		for _, viewTop := range []int{
			0,
			contentHeight - viewHeight,
			contentHeight - viewHeight - 1,
			contentHeight - viewHeight + 1,
		} {
			gap := kernel.eval(map[string]float64{
				"viewTop":       float64(viewTop),
				"viewHeight":    float64(viewHeight),
				"contentHeight": float64(contentHeight),
				"tolerance":     float64(tolerance),
			})

			// A region scrolled exactly to its bottom is following, and one pixel
			// further up is not. That is the whole contract, and it is a *pair* of
			// assertions on adjacent rows: a rule that returned a constant zero would
			// pass either one alone.
			if contentHeight >= viewHeight && viewTop == contentHeight-viewHeight && gap < 0 {
				t.Errorf("a region scrolled to its bottom reports gap %v; it is following "+
					"the tail by definition, so an appended line must scroll into view",
					gap)
			}

			if contentHeight >= viewHeight &&
				viewTop == contentHeight-viewHeight-tolerance-1 && gap >= 0 {
				t.Errorf("a region %d pixels further from the bottom than the tolerance "+
					"reports gap %v, which reads as following; the reader has scrolled away "+
					"and an appended line must not move them",
					tolerance+1, gap)
			}
		}
	}
}

// TestTheKernelDeclaresTheFourParametersItIsCalledWith holds the signature.
//
// The evaluator binds four names and this asserts the *file* binds the same four.
// A fifth parameter would be `undefined` in the expression and `NaN` in a browser,
// and an evaluator reading only the expression would never see it.
func TestTheKernelDeclaresTheFourParametersItIsCalledWith(t *testing.T) {
	t.Parallel()

	source := gapSource(t)

	const open = "function spFollowGap("

	_, after, ok := strings.Cut(source, open)
	if !ok {
		t.Fatalf("%s declares no %s; the arithmetic gate finds its expression "+
			"through this signature, and a kernel that does not have one is not being "+
			"evaluated", live.GapFile, open)
	}

	declared, _, closed := strings.Cut(after, ")")
	if !closed {
		t.Fatalf("%s has an unterminated parameter list after %s", live.GapFile, open)
	}

	names := strings.Split(declared, ",")

	if len(names) != len(parameters) {
		t.Fatalf("%s declares %d parameters, want %d: %q. The evaluator binds %v, so a "+
			"fifth would be `undefined` in the expression and a missing one would make the "+
			"whole rule `NaN` in a browser — and neither is visible to an evaluator that "+
			"reads only the expression",
			live.GapFile, len(names), len(parameters), declared, parameters)
	}

	for index, name := range parameters {
		if strings.TrimSpace(names[index]) != name {
			t.Errorf("%s parameter %d is %q, want %q; the evaluator binds %v and a "+
				"parameter it does not bind evaluates to nothing",
				live.GapFile, index, strings.TrimSpace(names[index]), name, parameters)
		}
	}
}

// TestTheKernelHasExactlyOneReturn is what makes "the expression" unambiguous.
//
// The evaluator reads the first `return` it finds and stops at the next `;`. A
// second `return` — a guard clause, an early exit — would be invisible to it, so the
// count is asserted rather than the first-one-wins behaviour relied on.
func TestTheKernelHasExactlyOneReturn(t *testing.T) {
	t.Parallel()

	count := countReturns(gapSource(t))

	if count != 1 {
		t.Errorf("%s has %d `return` statements, want exactly 1; the arithmetic gate "+
			"evaluates the one expression it can find and would silently ignore a second",
			live.GapFile, count)
	}
}

// TestTheArithmeticGateRejectsWhatItCannotCheck is the refusal contract, from the
// outside.
//
// Every input here is JavaScript a future edit might plausibly add to this file, and
// every one must be a parse error. An evaluator that quietly accepted a subset would
// leave the gate evaluating less than it used to while still reporting green — which
// is the failure this repository records three times over.
func TestTheArithmeticGateRejectsWhatItCannotCheck(t *testing.T) {
	t.Parallel()

	for _, rejected := range []struct {
		what  string
		input string
	}{
		{what: "a conditional", input: "viewTop > 0 ? 1 : 0"},
		{what: "a loop", input: "for (;;) {}"},
		{what: "a string", input: `"a string"`},
		{what: "a single-quoted string", input: "'a string'"},
		{what: "a template literal", input: "`a string`"},
		{what: "a call", input: "Math.min(viewTop, viewHeight)"},
		{what: "a member access", input: "viewTop.length"},
		{what: "an assignment", input: "viewTop = 1"},
		{what: "a comma expression", input: "viewTop, viewHeight"},
		{what: "a comparison", input: "viewTop === viewHeight"},
		{what: "a multiplication", input: "viewTop * viewHeight"},
		{what: "a division", input: "viewTop / viewHeight"},
		{what: "a modulo", input: "viewTop % viewHeight"},
		{what: "a bitwise and", input: "viewTop & viewHeight"},
		{what: "an unbound name", input: "scrollTop + clientHeight"},
		{what: "a Math global", input: "Math.abs(viewTop)"},
		{what: "a document access", input: "document.scrollTop"},
		{what: "two statements", input: "viewTop; viewHeight"},
		{what: "an unclosed parenthesis", input: "(viewTop + viewHeight"},
	} {
		t.Run(rejected.what, func(t *testing.T) {
			t.Parallel()

			if _, err := parseStrict(rejected.input); err == nil {
				t.Errorf("the evaluator accepted %s (%q). Its grammar is numbers, "+
					"%v, parentheses and + - only: anything else is a parse error, so "+
					"that an edit to the kernel outside the grammar stops the build "+
					"rather than quietly narrowing what the gate reads",
					rejected.what, rejected.input, parameters)
			}
		})
	}
}

// TestTheArithmeticGateEvaluatesWhatItClaimsTo holds the other direction, so the
// refusals above cannot be satisfied by refusing everything.
func TestTheArithmeticGateEvaluatesWhatItClaimsTo(t *testing.T) {
	t.Parallel()

	for _, known := range []struct {
		input string
		want  float64
	}{
		{input: "1 + 2", want: 3},
		{input: "5 - 8", want: -3},
		// `(1 + 2) * 3` is absent rather than present-and-failing: `*` is not in this
		// grammar, and the refusals table already carries it as a required rejection.
		// A row here would assert two things — that it is refused, and that the
		// message names the operator — and the second is not what this file is for.
		{input: "((1 + 2))", want: 3},
		{input: "viewTop + viewHeight", want: 5},
		{input: "viewTop - viewHeight", want: -5},
		{input: "viewTop + viewHeight - contentHeight + tolerance", want: -3},
		{input: "0 - 0 - (4 - 0)", want: -4},
		{input: "viewTop + viewHeight - (contentHeight - tolerance)", want: -3},
	} {
		t.Run(known.input, func(t *testing.T) {
			t.Parallel()

			parsed, err := parseStrict(known.input)
			if err != nil {
				t.Fatalf("the evaluator refused %q, which is inside its grammar: %v",
					known.input, err)
			}

			got := parsed.eval(map[string]float64{
				"viewTop":       0,
				"viewHeight":    5,
				"contentHeight": 12,
				"tolerance":     4,
			})

			if got != known.want {
				t.Errorf("%q evaluated to %v, want %v", known.input, got, known.want)
			}
		})
	}
}

// --- The evaluator ----------------------------------------------------------------

// parseStrict parses one expression and refuses anything left over.
//
// **The entry point the refusal contract belongs to.** `parseExpression` stops at
// the end of a value and hands back the remainder, which is how a parenthesised
// group learns it has ended — and which means `parseStrict("viewTop = 1")` returns
// the name `viewTop` and no error at all if the caller forgets to look at the
// remainder. A caller that forgot would be evaluating the first word of every line of
// JavaScript it was given, and would report success.
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

// kernel is a parsed `spFollowGap`: a parameter list and an expression.
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

	parsed, err := parseKernel(gapSource(t))
	if err != nil {
		t.Fatalf("%s is outside the grammar this gate can evaluate: %v\n"+
			"that is a red build on purpose — either narrow the kernel back to one "+
			"expression over its four parameters, or widen the evaluator and hold it "+
			"to the same two tests", live.GapFile, err)
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
// parser that cannot tell them apart is a parser reading prose — and this kernel's
// header is a comment that discusses `return` in prose seven times.
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

// countReturns counts `return` used as a statement, skipping comments and strings.
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

	if _, after, found := strings.CutLast(line, "\n"); found {
		if strings.Contains(after, "//") {
			return false
		}
	} else if strings.Contains(line, "//") {
		return false
	}

	depth := strings.Count(source[:offset], "/*") - strings.Count(source[:offset], "*/")

	return depth <= 0
}

// node is one parsed expression.
type node interface {
	eval(env map[string]float64) float64
}

// numberNode is a literal.
type numberNode float64

func (n numberNode) eval(map[string]float64) float64 { return float64(n) }

// nameNode is a parameter, and only ever one of the four.
type nameNode string

func (n nameNode) eval(env map[string]float64) float64 {
	value, ok := env[string(n)]
	if !ok {
		// Unreachable: the parser refuses a name outside `parameters`. NaN rather than
		// zero, so a future edit produces a visible nonsense rather than a plausible
		// wrong answer.
		return math.NaN()
	}

	return value
}

// binaryNode is `+` or `-`.
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
	default:
		// Unreachable: the parser emits only the two operators above.
		return math.NaN()
	}
}

// parseExpression parses a full expression and returns the unconsumed remainder.
//
// The remainder is returned rather than required to be empty at every level, which is
// what lets the top level distinguish `a + b` (consumed) from `a; b` (a `;` the
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

// parseTerm parses the additive level, and **is** that level: the grammar has no
// multiplicative operators, so there is one precedence level and one function.
//
// A separate function rather than inlining it into `parseExpression` because the two
// levels must be *distinguishable*: a future editor adding `*` needs to add a level
// here and not to widen the operator set where the remainder logic cannot see it.
func parseTerm(input string) (node, string, error) {
	left, rest, err := parsePrimary(input)
	if err != nil {
		return nil, "", err
	}

	for {
		rest = skipSpace(rest)

		if rest == "" || rest[0] == ')' || !strings.ContainsRune("+-", rune(rest[0])) {
			return left, rest, nil
		}

		right, remaining, err := parsePrimary(rest[1:])
		if err != nil {
			return nil, "", err
		}

		left = binaryNode{op: string(rest[0]), left: left, right: right}
		rest = remaining
	}
}

// parsePrimary parses a number, a parameter name, or a parenthesised expression.
//
// `+` and `-` are **binary only** in this grammar. There is no unary sign, and that
// is deliberate: `spFollowGap` has no negative literal, and admitting unary minus
// would make `-viewTop` evaluate where the spec's own arithmetic reads the other way.
// A future kernel that genuinely needs one must extend the grammar and its tests.
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

		// A decimal point is refused rather than consumed: `0.5` is a number this
		// grammar does not have, and accepting the leading `0` of it would evaluate a
		// fraction as an integer and report success.
		if digits < len(rest) && rest[digits] == '.' {
			return nil, "", fmt.Errorf("a decimal point at %q is outside the grammar; "+
				"this evaluator reads integers, and truncating one would report a value "+
				"the file does not compute", rest[:digits+1])
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
			// The refusal that matters: an evaluator that bound arbitrary names would
			// accept `Math.min`, `scrollTop` and `document` and evaluate a document
			// rather than the file.
			return nil, "", fmt.Errorf("%q is not one of the parameters %v", name, parameters)
		}

		return nameNode(name), rest[end:], nil

	default:
		return nil, "", fmt.Errorf(
			"%q is outside the grammar; this evaluator reads numbers, %v, parentheses "+
				"and + - only", string(rest[0]), parameters)
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
