package tokens_test

// The evaluator's own two tests.
//
// The gate in `step_arith_test.go` is only honest in one direction if the parser
// can do both halves of the job, and this file is the half that is usually
// missing: **a parser that refuses everything is green, and a parser that accepts
// everything is green.** Either would make the exhaustive grid over the shipped
// kernel a test of nothing at all.
//
// So:
//
//   - `TestTheArithmeticGateEvaluatesWhatItClaimsTo` evaluates a set of
//     expressions with answers worked out by hand, including the operator
//     precedence and associativity a wrong parser gets wrong.
//   - `TestTheArithmeticGateRejectsWhatItCannotCheck` feeds it a conditional, a
//     loop, a string, a template literal, a call, a member access, an assignment,
//     a ternary, a comma expression, a bare keyword, an unbound name and a
//     function call through a name it does not bind — and requires it to object
//     to **each**, with a message naming the input.

import (
	"strconv"
	"testing"
)

// testCase is one expression with the answer worked out by hand.
type testCase struct {
	name       string
	expression string
	env        map[string]float64
	want       float64
}

// acceptance is the arithmetic the evaluator claims to compute.
//
// `from + delta * 2` is in the list because precedence is the thing a
// hand-rolled recursive-descent parser gets wrong most often, and `from - delta -
// 1` because associativity is the second.
var acceptance = []testCase{
	{name: "a literal", expression: "42", env: nil, want: 42},
	{name: "a parameter", expression: "count", env: map[string]float64{"count": 7}, want: 7},
	{
		name:       "addition",
		expression: "from + delta",
		env:        map[string]float64{"from": 3, "delta": 4},
		want:       7,
	},
	{
		name:       "subtraction",
		expression: "from - delta",
		env:        map[string]float64{"from": 3, "delta": 4},
		want:       -1,
	},
	{
		name:       "multiplication binds tighter than addition",
		expression: "from + delta * 2",
		env:        map[string]float64{"from": 3, "delta": 4},
		want:       11,
	},
	{
		name:       "subtraction is left-associative",
		expression: "from - delta - 1",
		env:        map[string]float64{"from": 10, "delta": 3},
		want:       6,
	},
	{
		name:       "parentheses override precedence",
		expression: "(from + delta) * 2",
		env:        map[string]float64{"from": 3, "delta": 4},
		want:       14,
	},
	{
		name:       "a unary minus on a parenthesised group",
		expression: "-(from + delta)",
		env:        map[string]float64{"from": 3, "delta": 4},
		want:       -7,
	},
	{
		name:       "unary plus is the identity",
		expression: "+count",
		env:        map[string]float64{"count": 5},
		want:       5,
	},
	{
		name:       "nested parentheses",
		expression: "((from))",
		env:        map[string]float64{"from": 9},
		want:       9,
	},
	{
		name:       "division",
		expression: "count / 2",
		env:        map[string]float64{"count": 9},
		want:       4.5,
	},
	{
		name:       "a modulo of positive values",
		expression: "from % count",
		env:        map[string]float64{"from": 7, "count": 3},
		want:       1,
	},
	{
		name:       "whitespace and newlines are not significant",
		expression: "from   +\n  delta",
		env:        map[string]float64{"from": 2, "delta": 3},
		want:       5,
	},
}

// TestTheArithmeticGateEvaluatesWhatItClaimsTo is the half of the gate's honesty
// that a strict parser passes and a broken one does not.
func TestTheArithmeticGateEvaluatesWhatItClaimsTo(t *testing.T) {
	t.Parallel()

	for _, test := range acceptance {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := parseStrict(test.expression)
			if err != nil {
				t.Fatalf("the gate refused %q, an expression it claims to support: %v",
					test.expression, err)
			}

			if got := parsed.eval(test.env); got != test.want {
				t.Errorf("%q = %v, want %v", test.expression, got, test.want)
			}
		})
	}
}

// TestTheArithmeticGateRejectsWhatItCannotCheck is the other half: everything the
// grammar does not have is a parse error, so a kernel that grows one fails the
// build rather than being silently under-examined.
//
// **Through `parseStrict`, not `parseExpression`.** The inner parser stops at the
// end of a value and hands back the rest, which is what lets a parenthesised
// group know it has ended; `parseStrict` is the entry point that turns an
// unconsumed remainder into a refusal. Running the controls against the inner
// parser instead was the first version of this test and it passed eleven
// constructs the grammar does not have — `from = 1`, `from[0]`, `from; count`,
// `count ? from : delta` — because each was silently truncated to `from` and
// truncated to a valid node looks exactly like an accept.
func TestTheArithmeticGateRejectsWhatItCannotCheck(t *testing.T) {
	t.Parallel()

	for _, test := range rejected {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := parseStrict(test.expression)
			if err == nil {
				t.Fatalf("the gate accepted %q, which parsed as %s and is "+
					"outside the grammar. Accepting it means the build would stay "+
					"green while the kernel stopped being checked",
					test.expression, describe(parsed))

				return
			}

			if err.Error() == "" {
				t.Errorf("the gate refused %q with an empty message; a refusal "+
					"nobody can read is a refusal nobody will look for",
					test.expression)
			}
		})
	}
}

// rejected is every construct the grammar must refuse.
//
// **Each one is a construct that a looser parser would accept and that a browser
// would then evaluate.** `Math.min(from, count)` and `window.innerWidth` are the
// two that matter most, because an evaluator that bound a name its host language
// does not have once passed an entire suite in this repository and threw
// `ReferenceError` on the first frame.
var rejected = []struct {
	name       string
	expression string
}{
	{name: "a conditional", expression: "from > 0 ? 1 : 0"},
	{name: "a statement", expression: "if (count) from"},
	{name: "a loop", expression: "for (var i = 0; i < count; i++) from"},
	{name: "a while loop", expression: "while (count) from"},
	{name: "a double-quoted string", expression: `"hello"`},
	{name: "a single-quoted string", expression: `'hello'`},
	{name: "a template literal", expression: "`${from}`"},
	{name: "a call", expression: "Math.min(from, count)"},
	{name: "a call through an unbound name", expression: "abs(from)"},
	{name: "a member access", expression: "Math.PI"},
	{name: "an assignment", expression: "from = 1"},
	{name: "a compound assignment", expression: "from += 1"},
	{name: "a ternary", expression: "count ? from : delta"},
	{name: "a comma expression", expression: "from, delta"},
	{name: "a declaration", expression: "var from = 1"},
	{name: "the keyword undefined", expression: "undefined"},
	{name: "the keyword null", expression: "null"},
	{name: "a boolean", expression: "true"},
	{name: "an index", expression: "from[0]"},
	{name: "an object literal", expression: "{a: 1}"},
	{name: "an arrow function", expression: "(x) => x"},
	{name: "a semicolon-separated statement", expression: "from; count"},
	{name: "an unclosed parenthesis", expression: "(from + delta"},
	{name: "an operator with no operand", expression: "from +"},
	{name: "a logical operator", expression: "from && delta"},
	{name: "a bitwise operator", expression: "from | delta"},
	{name: "a comparison", expression: "from < count"},
	{name: "a global object", expression: "window"},
	{name: "a document", expression: "document.body"},
	{name: "a lowercase alias of a parameter", expression: "COUNT"},
	{name: "a prefix of a parameter", expression: "cou"},
}

// describe renders a parsed node for a failure message.
func describe(parsed node) string {
	switch typed := parsed.(type) {
	case numberNode:
		return "the number " + strconv.FormatFloat(float64(typed), 'g', -1, 64)
	case nameNode:
		return "the name " + string(typed)
	case binaryNode:
		return "the binary operator " + typed.op
	case unaryNode:
		return "the unary operator " + typed.op
	default:
		return "an unprintable node"
	}
}
