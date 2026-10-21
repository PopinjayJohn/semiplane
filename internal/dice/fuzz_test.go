package dice

import (
	"strings"
	"testing"
)

// FuzzParse feeds arbitrary strings through Parse and, when parsing
// succeeds, through Eval. Parsing and evaluation must never panic;
// evaluation errors (division by zero, overflow) are acceptable outcomes.
func FuzzParse(f *testing.F) {
	seeds := []string{
		"d20",
		"2d20kh1+5",
		"4d6kh3",
		"2d6kl1",
		"1d12+4",
		"(2d6+3)*2",
		"2+3*4",
		"7/2",
		"-d20",
		"",
		"(",
		")",
		"()",
		"ddd",
		"101d6",
		"1d1001",
		"(((((1)))))",
		"2d6kh",
		"2d6+",
		"0d0",
		"1/0",
		strings.Repeat("1+", 40),
		strings.Repeat("(", 10),
		strings.Repeat("d", 20),
		// Variable expressions
		"@con",
		"1d20+@mod",
		"2d20kh1+@mod",
		"@a+@b",
		"(@x)*2",
		"-@penalty",
		"1d12+@con",
		"@my_var",
		"@con12",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ast, err := Parse(s)
		if err != nil {
			return
		}
		if ast == nil {
			t.Fatal("Parse returned nil AST with nil error")
		}
		// Use a vars map that covers common test variable names
		vars := map[string]int{"con": 1, "mod": 2, "a": 3, "b": 4, "x": 5, "penalty": 1, "my_var": 1, "con12": 1}
		_, _ = ast.EvalWithVars(NewQueueRoller(1, 2, 3, 4, 5, 6), vars)
		_, _ = ast.EvalWithVars(NewSeededRoller(1), vars)
	})
}
