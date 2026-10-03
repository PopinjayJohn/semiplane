// Package map_test holds the gates for the map client's shipped JavaScript.
//
// There is no Node in this repository's toolchain — deliberately, per the note in
// `Makefile`'s `a11y` target: CI must not come to depend on a browser toolchain.
// So a JavaScript file cannot be tested by running it, and the alternative —
// re-implementing it in Go and asserting the two agree — is the failure mode this
// repository keeps meeting: a test that passes because it is reading something
// other than the thing that ships. The two implementations drift, and the copy
// that drifts is never the one anybody looks at.
//
// So the arithmetic in `camera.js` is **evaluated from its own source text** by the
// small expression evaluator in this file. There is one implementation, it is the
// shipped bytes, and `make check` runs it without a browser.
//
// # Why `camera.js` is written in the restricted grammar this file accepts
//
// Every function in `camera.js` is `export function name(params) { return expr; }`
// where `expr` is built from numbers, records of numbers, `+ - * / < <= > >=`,
// `&& ||`, unary `-`, member access on records, calls to functions the module
// declares, and the four `Math` functions. There is no statement other than `return`,
// so there is no place for control flow to hide.
//
// The important property is the failure direction. A construct this evaluator does
// not understand is a **parse error**, so an edit that adds a conditional breaks
// the build loudly instead of silently turning the gate blind. That is the
// difference between a restricted grammar and a best-effort one, and
// `TestTheArithmeticGateRejectsWhatItCannotCheck` holds it by feeding the evaluator
// a conditional, a loop, a string literal, `Math.min` and an assignment.
//
// # What this file cannot do
//
// It reads text. The audits in `map_source_test.go` are therefore audits over
// *source*, not over a running browser: they can prove that `scene.js` asks
// `camera.js` for a 3px outline and that no colour literal exists, and they cannot
// prove what a WebGL context did with it. UI §3.7's §10.3–§10.7 sweeps stay
// agent-assisted for that reason, and every finding from them becomes a committed
// test here rather than a comment.
package map_test

import (
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The camera module, relative to this test's directory.
//
// Relative rather than resolved from the module root because a test that reads a
// fixed number of `../` has to be counted by a human, and this one is: `map` →
// `js` → `static` → `web` → `internal` → the repository root is five.
const cameraSource = "camera.js"

// arithEPS is the tolerance for every equality in this file.
//
// Absolute rather than relative because the quantities are viewport units and map
// units — the same order as the inputs — and a relative epsilon on a zero is
// undefined. 1e-9 is six orders of magnitude below a pixel at any viewport this
// product renders, and four orders above the double-precision noise of a chain of
// a dozen operations on values around 10^4.
const arithEPS = 1e-9

// --- The evaluator -------------------------------------------------------------

type arithKind int

const (
	arithNumber arithKind = iota
	arithIdent
	arithPunct
	arithKeyword
	arithString
)

func (k arithKind) String() string {
	switch k {
	case arithNumber:
		return "number"
	case arithIdent:
		return "name"
	case arithPunct:
		return "punctuation"
	case arithKeyword:
		return "keyword"
	case arithString:
		return "string"
	}

	return "token"
}

type arithToken struct {
	kind arithKind
	text string
	line int
}

// arithKeywords are lexed as keywords rather than names so that the parser can say
// "`if` is not something this gate evaluates" instead of "unknown name if", which
// is the difference between a diagnostic a reader can act on and one they have to
// decode.
var arithKeywords = map[string]bool{
	"function": true, "return": true, "export": true,
	"var": true, "const": true, "let": true,
	"if": true, "else": true, "for": true, "while": true, "do": true, "switch": true,
	"new": true, "this": true, "class": true, "try": true, "throw": true, "delete": true,
}

var arithPunctuation = "+-*/(){},:.;=<>!%&|?~^[]"

// arithTwoCharOperators are lexed as one token.
//
// Only the two boolean conjunctions are in the grammar. A `==` or an `=>` reaching
// the parser is reported *by the parser*, with the expression it appeared in, which
// is a better diagnostic than "expected ) and found =".
var arithTwoCharOperators = []string{
	"&&", "||", "<=", ">=", "==", "!=", "=>", "+=", "-=", "*=", "/=",
}

// arithComparisonOperators are the four ordering comparisons.
//
// `==` and `!=` are deliberately absent: the camera has no use for them, and a
// grammar that admits them would be able to compare two records by identity, which
// is a question nothing here asks an answer to.
var arithComparisonOperators = []string{"<=", ">=", "<", ">"}

// lexArith tokenises the restricted grammar, skipping comments.
//
// Comments are skipped rather than lexed so that a colour named in prose cannot be
// mistaken for a colour in an expression — the same reason the source audits read
// this token stream instead of the raw bytes.
func lexArith(src string) ([]arithToken, error) {
	tokens := make([]arithToken, 0, 128)
	line := 1

	for index := 0; index < len(src); {
		char := src[index]

		switch {
		case char == '\n':
			line++
			index++

		case char == ' ' || char == '\t' || char == '\r':
			index++

		case strings.HasPrefix(src[index:], "//"):
			end := strings.IndexByte(src[index:], '\n')
			if end < 0 {
				index = len(src)

				continue
			}

			index += end

		case strings.HasPrefix(src[index:], "/*"):
			end := strings.Index(src[index+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated block comment", line)
			}

			line += strings.Count(src[index:index+2+end+2], "\n")
			index += 2 + end + 2

		case isIdentStart(char):
			end := index
			for end < len(src) && isIdentPart(src[end]) {
				end++
			}

			text := src[index:end]
			kind := arithIdent
			if arithKeywords[text] {
				kind = arithKeyword
			}

			tokens = append(tokens, arithToken{kind: kind, text: text, line: line})
			index = end

		case char >= '0' && char <= '9':
			end := index
			for end < len(src) && (src[end] >= '0' && src[end] <= '9' || src[end] == '.') {
				end++
			}

			text := src[index:end]
			if _, err := strconv.ParseFloat(text, 64); err != nil {
				return nil, fmt.Errorf(
					"line %d: %q is not a number this gate can evaluate", line, text)
			}

			tokens = append(tokens, arithToken{kind: arithNumber, text: text, line: line})
			index = end

		case char == '\'' || char == '"':
			end := strings.IndexByte(src[index+1:], char)
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated string", line)
			}

			tokens = append(tokens, arithToken{
				kind: arithString,
				text: src[index : index+2+end],
				line: line,
			})
			index += 2 + end

		case hasTwoCharOperator(src[index:]):
			tokens = append(tokens, arithToken{
				kind: arithPunct,
				text: src[index : index+2],
				line: line,
			})
			index += 2

		case strings.IndexByte(arithPunctuation, char) >= 0:
			tokens = append(tokens, arithToken{kind: arithPunct, text: string(char), line: line})
			index++

		default:
			return nil, fmt.Errorf(
				"line %d: %q is not in the arithmetic grammar", line, string(char))
		}
	}

	return append(tokens, arithToken{kind: arithPunct, text: "", line: line}), nil
}

func isIdentStart(char byte) bool {
	return char == '_' || char == '$' ||
		(char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

func isIdentPart(char byte) bool {
	return isIdentStart(char) || (char >= '0' && char <= '9')
}

func hasTwoCharOperator(rest string) bool {
	if len(rest) < 2 {
		return false
	}

	return slices.Contains(arithTwoCharOperators, rest[:2])
}

// errNumberWanted is the one error this file raises about a *shape* rather than a
// value: a record where the grammar wants a number, or a number where it wants a
// record.
var errNumberWanted = errors.New("used a record where a number was required")

// arithValue is a number or a record of them.
type arithValue struct {
	number   float64
	fields   map[string]arithValue
	isRecord bool
}

func arithNum(value float64) arithValue {
	return arithValue{number: value}
}

func arithRec(fields map[string]float64) arithValue {
	record := make(map[string]arithValue, len(fields))

	for name, value := range fields {
		record[name] = arithNum(value)
	}

	return arithValue{fields: record, isRecord: true}
}

func (v arithValue) field(name string) (float64, error) {
	if !v.isRecord {
		return 0, fmt.Errorf("cannot read %q from a number", name)
	}

	member, found := v.fields[name]
	if !found {
		return 0, fmt.Errorf("no field %q on this record", name)
	}

	return member.number, nil
}

func (v arithValue) num() (float64, error) {
	if v.isRecord {
		return 0, errNumberWanted
	}

	return v.number, nil
}

// arithNode is one node of a parsed expression.
type arithNode interface {
	// line is the source line, so an error names where the arithmetic came from.
	line() int
	eval(program arithProgram, scope map[string]arithValue) (arithValue, error)
}

type arithNumNode struct {
	value float64
	at    int
}

func (n arithNumNode) line() int { return n.at }

func (n arithNumNode) eval(arithProgram, map[string]arithValue) (arithValue, error) {
	return arithNum(n.value), nil
}

type arithVarNode struct {
	name string
	at   int
}

func (n arithVarNode) line() int { return n.at }

func (n arithVarNode) eval(_ arithProgram, scope map[string]arithValue) (arithValue, error) {
	value, found := scope[n.name]
	if !found {
		return arithValue{}, fmt.Errorf("line %d: unknown name %q", n.at, n.name)
	}

	return value, nil
}

type arithMemberNode struct {
	recv  arithNode
	field string
	at    int
}

func (n arithMemberNode) line() int { return n.at }

func (n arithMemberNode) eval(
	program arithProgram,
	scope map[string]arithValue,
) (arithValue, error) {
	recv, err := n.recv.eval(program, scope)
	if err != nil {
		return arithValue{}, err
	}

	number, err := recv.field(n.field)
	if err != nil {
		return arithValue{}, fmt.Errorf("line %d: %w", n.at, err)
	}

	return arithNum(number), nil
}

type arithCallNode struct {
	name string
	args []arithNode
	at   int
}

func (n arithCallNode) line() int { return n.at }

func (n arithCallNode) eval(program arithProgram, scope map[string]arithValue) (arithValue, error) {
	args := make([]arithValue, 0, len(n.args))

	for _, arg := range n.args {
		value, err := arg.eval(program, scope)
		if err != nil {
			return arithValue{}, err
		}

		args = append(args, value)
	}

	value, err := program.call(n.name, args)
	if err != nil {
		return arithValue{}, fmt.Errorf("line %d: %w", n.at, err)
	}

	return value, nil
}

type arithBinaryNode struct {
	op          string
	left, right arithNode
	at          int
}

func (n arithBinaryNode) line() int { return n.at }

// eval evaluates both sides of a boolean operator rather than short-circuiting.
//
// Sound here precisely because neither side can have an effect: the grammar has no
// assignment, no call to anything but pure arithmetic, and no way to reach a
// global. A short circuit would be a branch, and "no branches" is the property
// `camera.js`'s comment claims.
func (n arithBinaryNode) eval(
	program arithProgram,
	scope map[string]arithValue,
) (arithValue, error) {
	left, err := n.left.eval(program, scope)
	if err != nil {
		return arithValue{}, err
	}

	right, err := n.right.eval(program, scope)
	if err != nil {
		return arithValue{}, err
	}

	switch n.op {
	case "+":
		return arithNum(left.number + right.number), nil

	case "-":
		return arithNum(left.number - right.number), nil

	case "*":
		return arithNum(left.number * right.number), nil

	case "/":
		return arithNum(left.number / right.number), nil

	case "&&":
		return arithNum(boolNumber(left.number != 0 && right.number != 0)), nil

	case "||":
		return arithNum(boolNumber(left.number != 0 || right.number != 0)), nil

	case "<":
		return arithNum(boolNumber(left.number < right.number)), nil

	case "<=":
		return arithNum(boolNumber(left.number <= right.number)), nil

	case ">":
		return arithNum(boolNumber(left.number > right.number)), nil

	case ">=":
		return arithNum(boolNumber(left.number >= right.number)), nil
	}

	return arithValue{}, fmt.Errorf(
		"line %d: %q is not an operator this gate evaluates",
		n.at,
		n.op,
	)
}

func boolNumber(value bool) float64 {
	if value {
		return 1
	}

	return 0
}

type arithNegateNode struct {
	operand arithNode
	at      int
}

func (n arithNegateNode) line() int { return n.at }

func (n arithNegateNode) eval(
	program arithProgram,
	scope map[string]arithValue,
) (arithValue, error) {
	value, err := n.operand.eval(program, scope)
	if err != nil {
		return arithValue{}, err
	}

	return arithNum(-value.number), nil
}

type arithRecordNode struct {
	fieldNames  []string
	fieldValues []arithNode
	at          int
}

func (n arithRecordNode) line() int { return n.at }

func (n arithRecordNode) eval(
	program arithProgram,
	scope map[string]arithValue,
) (arithValue, error) {
	record := make(map[string]arithValue, len(n.fieldNames))

	for index, name := range n.fieldNames {
		value, err := n.fieldValues[index].eval(program, scope)
		if err != nil {
			return arithValue{}, err
		}

		record[name] = value
	}

	return arithValue{fields: record, isRecord: true}, nil
}

// arithFunc is one `export function name(params) { return expr; }`.
type arithFunc struct {
	name   string
	params []string
	body   arithNode
}

// arithProgram is a parsed camera module.
type arithProgram struct {
	funcs map[string]arithFunc
	order []string
}

// params returns a declared function's parameter names, in order.
//
// Exported through the program rather than through a field so that a test can make
// a claim about a *signature* — `TestTheCameraCannotBeRefitFromAViewport` asserts
// that `resizeCamera` cannot see map bounds — which is a claim no amount of
// evaluating the body can make.
func (p arithProgram) params(name string) ([]string, error) {
	function, found := p.funcs[name]
	if !found {
		return nil, fmt.Errorf("no function %q in the module", name)
	}

	return function.params, nil
}

// callable reports whether a name is something a body is allowed to mention: one of
// this module's own functions, or one of the four closed built-ins.
func (p arithProgram) callable(name string) bool {
	if _, found := p.funcs[name]; found {
		return true
	}

	if !strings.HasPrefix(name, mathNamespace) {
		return false
	}

	_, found := builtinArity[strings.TrimPrefix(name, mathNamespace)]

	return found
}

// names returns the declared functions in source order.
func (p arithProgram) names() []string {
	return append([]string(nil), p.order...)
}

// freeNames returns every name a function's body reads, excluding its parameters.
//
// The exclusion is the point: what is left is a name the body would have to find in
// the module scope. `parseArith` requires each of them to be bound, which is what
// lets `TestTheCameraCannotBeRefitFromAViewport` claim that `resizeCamera`'s body can
// see only its own parameter — a claim no amount of evaluating the function could
// establish, because an evaluator only sees the paths a test happens to take.
func (p arithProgram) freeNames(name string) ([]string, error) {
	function, found := p.funcs[name]
	if !found {
		return nil, fmt.Errorf("no function %q in the module", name)
	}

	bound := make(map[string]bool, len(function.params))
	for _, param := range function.params {
		bound[param] = true
	}

	seen := map[string]bool{}
	free := []string{}

	// Both a bare name and a call's callee count: `min(a, 1)` reads `min` just as
	// surely as `min` on its own does, and a check that only looked at variables
	// would have accepted the one that actually shipped broken.
	report := func(name string) {
		if bound[name] || seen[name] {
			return
		}

		seen[name] = true
		free = append(free, name)
	}

	walk(function.body, func(found arithNode) {
		switch typed := found.(type) {
		case arithVarNode:
			report(typed.name)

		case arithCallNode:
			report(typed.name)
		}
	})

	slices.Sort(free)

	return free, nil
}

// records returns every record literal in the module, in source order.
func (p arithProgram) records() []arithRecordNode {
	found := []arithRecordNode{}

	for _, name := range p.order {
		walk(p.funcs[name].body, func(node arithNode) {
			if record, isRecord := node.(arithRecordNode); isRecord {
				found = append(found, record)
			}
		})
	}

	return found
}

// walk visits every node of an expression tree.
func walk(node arithNode, visit func(arithNode)) {
	visit(node)

	switch typed := node.(type) {
	case arithNumNode:
	case arithVarNode:

	case arithMemberNode:
		walk(typed.recv, visit)

	case arithCallNode:
		for _, arg := range typed.args {
			walk(arg, visit)
		}

	case arithBinaryNode:
		walk(typed.left, visit)
		walk(typed.right, visit)

	case arithNegateNode:
		walk(typed.operand, visit)

	case arithRecordNode:
		for _, value := range typed.fieldValues {
			walk(value, visit)
		}
	}
}

// call evaluates one declared function or built-in.
func (p arithProgram) call(name string, args []arithValue) (arithValue, error) {
	if value, ok, err := callBuiltin(name, args); ok {
		return value, err
	}

	function, found := p.funcs[name]
	if !found {
		return arithValue{}, fmt.Errorf("call to %q, which this module does not declare", name)
	}

	if len(args) != len(function.params) {
		return arithValue{}, fmt.Errorf(
			"%s takes %d arguments and was given %d", name, len(function.params), len(args))
	}

	scope := make(map[string]arithValue, len(function.params))
	for index, param := range function.params {
		scope[param] = args[index]
	}

	value, err := function.body.eval(p, scope)
	if err != nil {
		return arithValue{}, err
	}

	// A non-finite result is a division by zero or a `sqrt` of a negative. Letting
	// one through would make an assertion like `got == want` compare NaN != NaN and
	// fail for a reason no test message could explain, and an `Inf` would sail
	// through every bound check in this file.
	if err := finite(value, name); err != nil {
		return arithValue{}, err
	}

	return value, nil
}

// finite requires every number in a result to be finite.
func finite(value arithValue, where string) error {
	if !value.isRecord {
		if math.IsNaN(value.number) || math.IsInf(value.number, 0) {
			return fmt.Errorf("%s produced %v", where, value.number)
		}

		return nil
	}

	for name, member := range value.fields {
		if err := finite(member, where+"."+name); err != nil {
			return err
		}
	}

	return nil
}

// mathNamespace is the one global a camera expression may reach into, and
// `builtinNames` is the closed set it may reach for inside it.
//
// **JavaScript's own spelling, deliberately.** An earlier version of the evaluator
// bound bare `min`, `max`, `abs` and `sqrt`, and `camera.js` used those names — so
// the suite evaluated a module that no browser could load. A gate that supplies a name
// its host language does not have is not a gate on the shipped bytes. With the
// vocabulary narrowed to `Math.*`, a bare `min(…)` in the module is an unbound name
// and `TestNoCameraFunctionReadsAnythingItWasNotGiven` says so, which is the same
// message a browser would give.
const mathNamespace = "Math."

// builtinArity is how many arguments each `Math` function takes.
var builtinArity = map[string]int{"min": 2, "max": 2, "abs": 1, "sqrt": 1}

// callBuiltin evaluates the closed set of `Math` functions. The third result reports
// whether the name was a built-in at all, so an unknown call is a different error
// from a wrong-arity call.
func callBuiltin(name string, args []arithValue) (arithValue, bool, error) {
	function, found := strings.CutPrefix(name, mathNamespace)
	if !found {
		return arithValue{}, false, nil
	}

	arity, isBuiltin := builtinArity[function]
	if !isBuiltin {
		return arithValue{}, false, nil
	}

	if len(args) != arity {
		return arithValue{}, true, fmt.Errorf(
			"%s takes %d arguments and was given %d", name, arity, len(args))
	}

	left, err := args[0].num()
	if err != nil {
		return arithValue{}, true, err
	}

	if function == "abs" {
		return arithNum(math.Abs(left)), true, nil
	}

	if function == "sqrt" {
		return arithNum(math.Sqrt(left)), true, nil
	}

	right, err := args[1].num()
	if err != nil {
		return arithValue{}, true, err
	}

	if function == "min" {
		return arithNum(math.Min(left, right)), true, nil
	}

	return arithNum(math.Max(left, right)), true, nil
}

// arithParser is a recursive-descent parser over the token stream.
type arithParser struct {
	tokens []arithToken
	at     int
}

func parseArith(src string) (arithProgram, error) {
	tokens, err := lexArith(src)
	if err != nil {
		return arithProgram{}, err
	}

	parser := &arithParser{tokens: tokens}
	program := arithProgram{funcs: map[string]arithFunc{}}

	for !parser.done() {
		function, err := parser.function()
		if err != nil {
			return arithProgram{}, err
		}

		if _, seen := program.funcs[function.name]; seen {
			return arithProgram{}, fmt.Errorf("%s is declared twice", function.name)
		}

		program.funcs[function.name] = function
		program.order = append(program.order, function.name)
	}

	if len(program.funcs) == 0 {
		return arithProgram{}, errors.New("the module declares no functions")
	}

	// Boundness, after the whole program is known so a forward reference is legal and
	// a typo is not.
	//
	// **This check is here rather than in a test because of what it caught.** An
	// earlier evaluator bound a bare `min`, `max`, `abs` and `sqrt`, and `camera.js`
	// used those names: every test in the suite evaluated it, `make check` was green,
	// and a browser threw `ReferenceError` on the first frame. A name that neither
	// JavaScript nor this evaluator binds is a module that cannot load, so it is a
	// **parse** failure here — the loudest place this file has.
	if err := program.checkBound(); err != nil {
		return arithProgram{}, err
	}

	return program, nil
}

// checkBound reports the first name a function reads that nothing in the module binds.
func (p arithProgram) checkBound() error {
	for _, name := range p.order {
		free, err := p.freeNames(name)
		if err != nil {
			return err
		}

		for _, found := range free {
			if !p.callable(found) {
				return fmt.Errorf(
					"%s reads %q, which is neither a parameter, a function this module "+
						"declares, nor one of the four Math functions. Nothing binds that name "+
						"here and nothing binds it in a browser either, so the module would "+
						"throw on its first call",
					name, found)
			}
		}
	}

	return nil
}

func (p *arithParser) done() bool {
	return p.peek().text == ""
}

func (p *arithParser) peek() arithToken {
	return p.tokens[p.at]
}

func (p *arithParser) take() arithToken {
	token := p.tokens[p.at]
	p.at++

	return token
}

func (p *arithParser) expect(text string) error {
	token := p.take()

	if token.text != text {
		return fmt.Errorf("line %d: expected %q and found %q", token.line, text, token.text)
	}

	return nil
}

// function parses `[export] function name(params) { return expr; }`.
func (p *arithParser) function() (arithFunc, error) {
	if p.peek().text == "export" {
		p.take()
	}

	token := p.peek()

	switch {
	case token.kind == arithKeyword && token.text == "function":
	case token.kind == arithKeyword:
		return arithFunc{}, fmt.Errorf(
			"line %d: this gate does not evaluate %q; camera.js may only declare functions",
			token.line, token.text)

	default:
		return arithFunc{}, fmt.Errorf(
			"line %d: expected `export function` and found %q", token.line, token.text)
	}

	p.take()

	name := p.take()

	if name.kind != arithIdent {
		return arithFunc{}, fmt.Errorf("line %d: %q is not a function name", name.line, name.text)
	}

	function := arithFunc{name: name.text}

	if err := p.expect("("); err != nil {
		return arithFunc{}, err
	}

	for p.peek().text != ")" {
		param := p.take()

		if param.kind != arithIdent {
			return arithFunc{}, fmt.Errorf(
				"line %d: %q is not a parameter name",
				param.line,
				param.text,
			)
		}

		if p.peek().text == "=" {
			return arithFunc{}, fmt.Errorf(
				"line %d: this gate does not evaluate a default parameter", p.peek().line)
		}

		function.params = append(function.params, param.text)

		if p.peek().text == "," {
			p.take()

			continue
		}

		break
	}

	if err := p.expect(")"); err != nil {
		return arithFunc{}, err
	}

	if err := p.expect("{"); err != nil {
		return arithFunc{}, err
	}

	if p.peek().kind == arithKeyword && p.peek().text != "return" {
		return arithFunc{}, fmt.Errorf(
			"line %d: this gate does not evaluate %q; a body is a single return",
			p.peek().line, p.peek().text)
	}

	if err := p.expect("return"); err != nil {
		return arithFunc{}, err
	}

	body, err := p.expression()
	if err != nil {
		return arithFunc{}, err
	}

	function.body = body

	if err := p.expect(";"); err != nil {
		return arithFunc{}, err
	}

	if err := p.expect("}"); err != nil {
		return arithFunc{}, err
	}

	return function, nil
}

// expression parses a whole expression.
//
// The six levels are JavaScript's, so a comparison binds tighter than a conjunction
// and looser than an addition. The grammar therefore needs no knowledge of
// conditionals to get `a + b <= c && d` right, which is the point of the exercise.
func (p *arithParser) expression() (arithNode, error) {
	return p.conjunction()
}

// conjunction parses `&&` and `||`, left-associative.
func (p *arithParser) conjunction() (arithNode, error) {
	left, err := p.comparison()
	if err != nil {
		return nil, err
	}

	for p.peek().kind == arithPunct && (p.peek().text == "&&" || p.peek().text == "||") {
		op := p.take()
		right, err := p.comparison()
		if err != nil {
			return nil, err
		}

		left = arithBinaryNode{op: op.text, left: left, right: right, at: op.line}
	}

	return left, nil
}

// comparison parses one ordering comparison. Non-associative, as in JavaScript, so
// `a < b < c` is a parse-and-then-a-number rather than a chained comparison.
func (p *arithParser) comparison() (arithNode, error) {
	left, err := p.additive()
	if err != nil {
		return nil, err
	}

	if p.peek().kind != arithPunct || !slices.Contains(arithComparisonOperators, p.peek().text) {
		return left, nil
	}

	op := p.take()
	right, err := p.additive()
	if err != nil {
		return nil, err
	}

	return arithBinaryNode{op: op.text, left: left, right: right, at: op.line}, nil
}

// additive parses `+` and `-`, left-associative.
func (p *arithParser) additive() (arithNode, error) {
	left, err := p.multiplicative()
	if err != nil {
		return nil, err
	}

	for p.peek().kind == arithPunct && (p.peek().text == "+" || p.peek().text == "-") {
		op := p.take()
		right, err := p.multiplicative()
		if err != nil {
			return nil, err
		}

		left = arithBinaryNode{op: op.text, left: left, right: right, at: op.line}
	}

	return left, nil
}

func (p *arithParser) multiplicative() (arithNode, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}

	for p.peek().kind == arithPunct && (p.peek().text == "*" || p.peek().text == "/") {
		op := p.take()
		right, err := p.unary()
		if err != nil {
			return nil, err
		}

		left = arithBinaryNode{op: op.text, left: left, right: right, at: op.line}
	}

	return left, nil
}

func (p *arithParser) unary() (arithNode, error) {
	if p.peek().kind == arithPunct && p.peek().text == "-" {
		op := p.take()
		operand, err := p.unary()
		if err != nil {
			return nil, err
		}

		return arithNegateNode{operand: operand, at: op.line}, nil
	}

	return p.atom()
}

func (p *arithParser) atom() (arithNode, error) {
	token := p.peek()

	switch {
	case token.kind == arithNumber:
		p.take()
		value, err := strconv.ParseFloat(token.text, 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", token.line, err)
		}

		return arithNumNode{value: value, at: token.line}, nil

	case token.kind == arithString:
		return nil, fmt.Errorf(
			"line %d: this gate does not evaluate string literals", token.line)

	case token.kind == arithKeyword:
		return nil, fmt.Errorf(
			"line %d: this gate does not evaluate %q", token.line, token.text)

	case token.kind == arithPunct && token.text == "(":
		p.take()
		inner, err := p.expression()
		if err != nil {
			return nil, err
		}

		return inner, p.expect(")")

	case token.kind == arithPunct && token.text == "{":
		return p.record()

	case token.kind == arithIdent:
		return p.identifier()
	}

	return nil, fmt.Errorf("line %d: %q cannot start an expression", token.line, token.text)
}

func (p *arithParser) identifier() (arithNode, error) {
	name := p.take()

	if p.peek().kind == arithPunct && p.peek().text == "(" {
		args, err := p.arguments()
		if err != nil {
			return nil, err
		}

		return arithCallNode{name: name.text, args: args, at: name.line}, nil
	}

	node := arithNode(arithVarNode{name: name.text, at: name.line})

	for p.peek().kind == arithPunct && p.peek().text == "." {
		p.take()
		field := p.take()

		if field.kind != arithIdent {
			return nil, fmt.Errorf("line %d: %q is not a field name", field.line, field.text)
		}

		node = arithMemberNode{recv: node, field: field.text, at: field.line}
	}

	if p.peek().kind == arithPunct && p.peek().text == "(" {
		if builtin, ok := mathCallName(node); ok {
			args, err := p.arguments()
			if err != nil {
				return nil, err
			}

			return arithCallNode{name: builtin, args: args, at: name.line}, nil
		}

		return nil, fmt.Errorf(
			"line %d: this gate evaluates calls to functions camera.js declares and to %s, "+
				"not to a global namespace", name.line, mathNamespace+"min")
	}

	return node, nil
}

// mathCallName reports whether an expression is `Math.<builtin>` and returns the name
// the evaluator dispatches on.
//
// The receiver has to be the bare name `Math`: `camera.js` cannot alias it, and an
// expression that reaches `Math` through a record field is not something the gate
// knows how to read.
func mathCallName(node arithNode) (string, bool) {
	member, isMember := node.(arithMemberNode)
	if !isMember {
		return "", false
	}

	receiver, isName := member.recv.(arithVarNode)
	if !isName || receiver.name != strings.TrimSuffix(mathNamespace, ".") {
		return "", false
	}

	if _, found := builtinArity[member.field]; !found {
		return "", false
	}

	return mathNamespace + member.field, true
}

// arguments parses a parenthesised, comma-separated expression list.
//
// The opening parenthesis is consumed here rather than by each caller, because there
// are two callers — a plain call and a `Math.*` call, which is reached through the
// member-access path — and leaving it to them is how the `(` of `Math.min(a, b)` ends
// up parsed as a parenthesised expression that stops at the comma.
func (p *arithParser) arguments() ([]arithNode, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}

	args := []arithNode{}

	for p.peek().text != ")" {
		arg, err := p.expression()
		if err != nil {
			return nil, err
		}

		args = append(args, arg)

		if p.peek().text != "," {
			break
		}

		p.take()
	}

	return args, p.expect(")")
}

func (p *arithParser) record() (arithNode, error) {
	open := p.take()
	node := arithRecordNode{at: open.line}

	for p.peek().text != "}" {
		name := p.take()

		if name.kind != arithIdent {
			return nil, fmt.Errorf("line %d: %q is not a field name", name.line, name.text)
		}

		if err := p.expect(":"); err != nil {
			return nil, err
		}

		value, err := p.expression()
		if err != nil {
			return nil, err
		}

		node.fieldNames = append(node.fieldNames, name.text)
		node.fieldValues = append(node.fieldValues, value)

		if p.peek().text == "," {
			p.take()

			continue
		}

		break
	}

	return node, p.expect("}")
}

// --- Loading the module --------------------------------------------------------

// cameraProgram parses `camera.js` once per test binary.
//
// `sync.OnceValue` rather than a package-level variable so the read happens inside a
// test — a package-level read of a source file makes `go vet` and the test binary's
// start-up order someone's problem, and this repository has been bitten by package
// level state before.
func cameraProgram(t *testing.T) arithProgram {
	t.Helper()

	source, err := os.ReadFile(cameraSource)
	if err != nil {
		t.Fatalf("read %s: %v", cameraSource, err)
	}

	program, err := parseArith(string(source))
	if err != nil {
		t.Fatalf(
			"%s is not in the arithmetic grammar the gate evaluates: %v\n%s",
			cameraSource,
			err,
			grammarHelp(),
		)
	}

	return program
}

// grammarHelp is the diagnostic a person gets when they have edited `camera.js`
// into something the gate cannot read. It is an error message rather than a
// comment in the source because the source is what they will be looking at.
func grammarHelp() string {
	return "a camera.js body is `export function name(a, b) { return <expr>; }`\n" +
		"where <expr> is numbers, records, member access, calls to functions this\n" +
		"module declares, `+ - * / < <= > >= && ||`, unary `-`, and the four Math\n" +
		"functions min, max, abs and sqrt — spelled with the Math., because a bare\n" +
		"name is a name no browser binds.\n" +
		"Any statement other than `return`, and any construct above, is a build\n" +
		"failure on purpose: the gate would otherwise be reading less than it does\n" +
		"today. See camera_arith_test.go and TestTheArithmeticGateRejectsWhatItCannotCheck."
}

// parseArithSource parses an inline fixture. Used by the meta-test, which needs to
// feed the evaluator constructs the shipped module never contains.
// --- Calling the module --------------------------------------------------------

// evalNum calls a camera function and requires a number back.
func evalNum(t *testing.T, program arithProgram, name string, args ...any) float64 {
	t.Helper()

	value, err := program.call(name, evalArgs(t, args...))
	if err != nil {
		t.Fatalf("%s(%s): %v", name, describe(args), err)
	}

	number, err := value.num()
	if err != nil {
		t.Fatalf("%s returned a record where a number was required", name)
	}

	return number
}

// evalRec calls a camera function and requires a record back.
func evalRec(t *testing.T, program arithProgram, name string, args ...any) arithValue {
	t.Helper()

	value, err := program.call(name, evalArgs(t, args...))
	if err != nil {
		t.Fatalf("%s(%s): %v", name, describe(args), err)
	}

	if !value.isRecord {
		t.Fatalf("%s returned a number where a record was required", name)
	}

	return value
}

// evalArgs converts Go arguments to evaluator values. A `map[string]float64`
// becomes a record; a float64 becomes a number; anything else is a test bug and
// panics rather than being coerced.
func evalArgs(t *testing.T, args ...any) []arithValue {
	t.Helper()

	out := make([]arithValue, 0, len(args))

	for _, arg := range args {
		switch typed := arg.(type) {
		case float64:
			out = append(out, arithNum(typed))

		case int:
			out = append(out, arithNum(float64(typed)))

		case map[string]float64:
			out = append(out, arithRec(typed))

		case arithValue:
			out = append(out, typed)

		default:
			t.Fatalf("cannot pass a %T as a camera argument", arg)
		}
	}

	return out
}

func describe(args []any) string {
	parts := make([]string, 0, len(args))

	for _, arg := range args {
		switch typed := arg.(type) {
		case float64:
			parts = append(parts, strconv.FormatFloat(typed, 'g', -1, 64))

		case int:
			parts = append(parts, strconv.Itoa(typed))

		default:
			parts = append(parts, "{…}")
		}
	}

	return strings.Join(parts, ", ")
}

// --- The grids -----------------------------------------------------------------

// Viewports, in the units the camera works in. Deliberately the shapes phase 9
// actually renders: two phones, one rotated phone, a tablet at both of its
// orientations, three laptop widths and a television. A resize test that only ever
// compares two similar sizes proves nothing, and a portrait-to-landscape crossing
// is the case the requirement names.
var (
	viewportWidths  = []float64{320, 580, 768, 1024, 1280, 1536, 2200, 2560}
	viewportHeights = []float64{320, 580, 768, 900, 1024, 1440, 1600, 2560}
)

// Map sizes, in map units. A square, a wide one, a tall one, and a small one whose
// fit scale is above `nearestWorldPerViewport` so the zoom bound is reachable.
var mapSizes = [][2]float64{{4096, 3072}, {2048, 2048}, {1200, 2400}, {256, 256}, {64, 64}}

// Map sizes for the resize sweep, which is the one that dominates the suite's run
// time.
//
// **Two, chosen for the two orderings of the zoom bounds rather than for coverage of
// shapes.** A 4096-unit map in a 320-unit viewport fits at 12.8 map units per
// viewport unit — past 1:1, so the fit is the zoomed-*out* end; a 64-unit map in the
// same viewport fits at 0.0625, so the fit is the magnified end and 1:1 is inside it.
// Between them they exercise both of `clampCamera`'s orders, and the resize property
// itself is a function of the camera rather than of the map, so a wider sweep here
// would cost seconds and prove nothing the other tests do not.
var resizeMaps = [][2]float64{{4096, 3072}, {64, 64}}

// cameraAt fits a camera for the given map and viewport.
func cameraAt(
	t *testing.T,
	program arithProgram,
	size [2]float64,
	width, height float64,
) arithValue {
	t.Helper()

	return evalRec(t, program, "fitCamera",
		map[string]float64{"width": size[0], "height": size[1]},
		map[string]float64{"width": width, "height": height})
}

func closeEnough(got, want float64) bool {
	return math.Abs(got-want) <= arithEPS
}

// minMax returns the two bounds in ascending order.
func minMax(left, right float64) (float64, float64) {
	return math.Min(left, right), math.Max(left, right)
}
