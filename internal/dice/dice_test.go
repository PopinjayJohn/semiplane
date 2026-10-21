package dice

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestEvalTable(t *testing.T) {
	cases := []struct {
		name          string
		formula       string
		queue         []int
		wantFaces     []int
		wantKept      []int
		wantDiscarded []int
		wantTotal     int
		wantSuccesses int
	}{
		{
			name: "advantage style keep high plus modifier", formula: "2d20kh1+5", queue: []int{17, 4},
			wantFaces: []int{17, 4}, wantKept: []int{17}, wantDiscarded: []int{4},
			wantTotal: 22, wantSuccesses: 1,
		},
		{
			name: "straight roll with modifier", formula: "1d12+4", queue: []int{7},
			wantFaces: []int{7}, wantKept: []int{7}, wantDiscarded: []int{},
			wantTotal: 11, wantSuccesses: 1,
		},
		{
			name: "keep three of four", formula: "4d6kh3", queue: []int{6, 1, 5, 2},
			wantFaces: []int{6, 1, 5, 2}, wantKept: []int{6, 5, 2}, wantDiscarded: []int{1},
			wantTotal: 13, wantSuccesses: 3,
		},
		{
			name: "keep lowest", formula: "2d6kl1", queue: []int{5, 2},
			wantFaces: []int{5, 2}, wantKept: []int{2}, wantDiscarded: []int{5},
			wantTotal: 2, wantSuccesses: 1,
		},
		{
			name: "bare d20", formula: "d20", queue: []int{13},
			wantFaces: []int{13}, wantKept: []int{13}, wantDiscarded: []int{},
			wantTotal: 13, wantSuccesses: 1,
		},
		{
			name: "parens and multiply", formula: "(2d6+3)*2", queue: []int{4, 1},
			wantFaces: []int{4, 1}, wantKept: []int{4, 1}, wantDiscarded: []int{},
			wantTotal: 16, wantSuccesses: 2,
		},
		{
			name: "precedence mul before add", formula: "2+3*4",
			wantFaces: []int{}, wantKept: []int{}, wantDiscarded: []int{},
			wantTotal: 14, wantSuccesses: 0,
		},
		{
			name: "integer division truncates", formula: "7/2",
			wantFaces: []int{}, wantKept: []int{}, wantDiscarded: []int{},
			wantTotal: 3, wantSuccesses: 0,
		},
		{
			name: "division with dice", formula: "8/4+1d4", queue: []int{3},
			wantFaces: []int{3}, wantKept: []int{3}, wantDiscarded: []int{},
			wantTotal: 5, wantSuccesses: 1,
		},
		{
			name: "whitespace tolerated", formula: " 2d6 + 3 ", queue: []int{4, 2},
			wantFaces: []int{4, 2}, wantKept: []int{4, 2}, wantDiscarded: []int{},
			wantTotal: 9, wantSuccesses: 2,
		},
		{
			name: "unary minus", formula: "-5+8",
			wantFaces: []int{}, wantKept: []int{}, wantDiscarded: []int{},
			wantTotal: 3, wantSuccesses: 0,
		},
		{
			name: "subtract dice result", formula: "2d6-1", queue: []int{3, 3},
			wantFaces: []int{3, 3}, wantKept: []int{3, 3}, wantDiscarded: []int{},
			wantTotal: 5, wantSuccesses: 2,
		},
		{
			name: "uppercase keywords", formula: "2D6KH1", queue: []int{5, 1},
			wantFaces: []int{5, 1}, wantKept: []int{5}, wantDiscarded: []int{1},
			wantTotal: 5, wantSuccesses: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ast, err := Parse(tc.formula)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.formula, err)
			}
			got, err := ast.Eval(NewQueueRoller(tc.queue...))
			if err != nil {
				t.Fatalf("Eval(%q) error: %v", tc.formula, err)
			}
			if !reflect.DeepEqual(got.Faces, tc.wantFaces) {
				t.Errorf("Faces = %v, want %v", got.Faces, tc.wantFaces)
			}
			if !reflect.DeepEqual(got.Kept, tc.wantKept) {
				t.Errorf("Kept = %v, want %v", got.Kept, tc.wantKept)
			}
			if !reflect.DeepEqual(got.Discarded, tc.wantDiscarded) {
				t.Errorf("Discarded = %v, want %v", got.Discarded, tc.wantDiscarded)
			}
			if got.Total != tc.wantTotal {
				t.Errorf("Total = %d, want %d", got.Total, tc.wantTotal)
			}
			if got.Successes != tc.wantSuccesses {
				t.Errorf("Successes = %d, want %d", got.Successes, tc.wantSuccesses)
			}
			if got.Successes != len(got.Kept) {
				t.Errorf("Successes = %d, want len(Kept) = %d", got.Successes, len(got.Kept))
			}
			if len(got.Breakdown) == 0 {
				t.Errorf("Breakdown is empty")
			}
		})
	}
}

func TestBreakdownLines(t *testing.T) {
	ast, err := Parse("2d20kh1+5")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	got, err := ast.Eval(NewQueueRoller(17, 4))
	if err != nil {
		t.Fatalf("Eval error: %v", err)
	}
	if len(got.Breakdown) != 2 {
		t.Fatalf("Breakdown has %d lines %q, want 2 (one per term)", len(got.Breakdown), got.Breakdown)
	}
	if !strings.Contains(got.Breakdown[0], "2d20kh1") {
		t.Errorf("first line %q does not name the dice term", got.Breakdown[0])
	}
}

func TestParseCapRejections(t *testing.T) {
	long65 := strings.Repeat("1+", 32) + "1" // 65 chars
	if len(long65) != 65 {
		t.Fatalf("test setup: want 65 chars, got %d", len(long65))
	}
	cases := []struct {
		name    string
		formula string
		wantCap string
	}{
		{"input too long", long65, "MaxInputLen"},
		{"count over max", "101d6", "MaxCount"},
		{"count zero", "0d6", "MaxCount"},
		{"sides over max", "1d1001", "MaxSides"},
		{"sides zero", "1d0", "MaxSides"},
		{"paren depth five", "(((((1)))))", "MaxParenDepth"},
		{"keep exceeds count", "1d6kh2", "MaxKeep"},
		{"keep zero", "2d6kh0", "MaxKeep"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.formula)
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want cap error naming %s", tc.formula, tc.wantCap)
			}
			if !strings.Contains(err.Error(), tc.wantCap) {
				t.Errorf("Parse(%q) error %q does not name %s", tc.formula, err, tc.wantCap)
			}
		})
	}
}

func TestParseBoundariesAccepted(t *testing.T) {
	exact64 := strings.Repeat("1+", 31) + "11" // 64 chars: 31 ones plus eleven
	if len(exact64) != MaxInputLen {
		t.Fatalf("test setup: want %d chars, got %d", MaxInputLen, len(exact64))
	}
	ast, err := Parse(exact64)
	if err != nil {
		t.Fatalf("Parse(64-char) error: %v", err)
	}
	got, err := ast.Eval(NewQueueRoller())
	if err != nil {
		t.Fatalf("Eval error: %v", err)
	}
	if got.Total != 42 {
		t.Errorf("Total = %d, want 42", got.Total)
	}

	for _, f := range []string{"((((1))))", "100d6", "1d1000", "2d6kh2"} {
		ast, err := Parse(f)
		if err != nil {
			t.Errorf("Parse(%q) error: %v, want accepted boundary", f, err)
			continue
		}
		if _, err := ast.Eval(NewQueueRoller(1)); err != nil {
			t.Errorf("Eval(%q) error: %v", f, err)
		}
	}
}

func TestParseSyntaxErrors(t *testing.T) {
	for _, f := range []string{"", "   ", "2d6+", "2d6 3", "2d6kh", "()", "(1", "2d6k3", "2dd6", "abc", "2d6+-"} {
		if _, err := Parse(f); err == nil {
			t.Errorf("Parse(%q) succeeded, want syntax error", f)
		}
	}
}

func TestEvalErrors(t *testing.T) {
	ast, err := Parse("1/0")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if _, err := ast.Eval(NewQueueRoller()); err == nil {
		t.Errorf("Eval(1/0) succeeded, want division-by-zero error")
	} else if !strings.Contains(err.Error(), "zero") {
		t.Errorf("Eval(1/0) error %q does not mention zero", err)
	}

	ast2, err := Parse("d20")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if _, err := ast2.Eval(nil); err == nil {
		t.Errorf("Eval(nil roller) succeeded, want error")
	}
	if _, err := (*AST)(nil).Eval(NewQueueRoller(1)); err == nil {
		t.Errorf("nil AST Eval succeeded, want error")
	}
}

func TestSeededDeterminism(t *testing.T) {
	seed := SeedFromEnv(12345)
	ast, err := Parse("4d6+2d8+3")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	a, err := ast.Eval(NewSeededRoller(seed))
	if err != nil {
		t.Fatalf("Eval error: %v", err)
	}
	b, err := ast.Eval(NewSeededRoller(seed))
	if err != nil {
		t.Fatalf("Eval error: %v", err)
	}
	if !reflect.DeepEqual(a.Faces, b.Faces) || a.Total != b.Total {
		t.Errorf("same seed diverged: %+v vs %+v", a, b)
	}
	if len(a.Faces) != 6 {
		t.Errorf("Faces has %d entries, want 6", len(a.Faces))
	}
}

func TestQueueExhaustionDeterminism(t *testing.T) {
	ast, err := Parse("3d6")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	a, err := ast.Eval(NewQueueRoller(3))
	if err != nil {
		t.Fatalf("Eval error: %v", err)
	}
	b, err := ast.Eval(NewQueueRoller(3))
	if err != nil {
		t.Fatalf("Eval error: %v", err)
	}
	if !reflect.DeepEqual(a.Faces, b.Faces) {
		t.Errorf("exhausted queues diverged: %v vs %v", a.Faces, b.Faces)
	}
	if len(a.Faces) != 3 || a.Faces[0] != 3 {
		t.Errorf("Faces = %v, want [3 x y]", a.Faces)
	}
}

func TestSeedFromEnv(t *testing.T) {
	t.Setenv("SEMIPLANE_TEST_SEED", "77")
	if got := SeedFromEnv(1); got != 77 {
		t.Errorf("SeedFromEnv = %d, want 77", got)
	}
	t.Setenv("SEMIPLANE_TEST_SEED", "not-a-number")
	if got := SeedFromEnv(9); got != 9 {
		t.Errorf("SeedFromEnv(invalid) = %d, want default 9", got)
	}
	if err := os.Unsetenv("SEMIPLANE_TEST_SEED"); err != nil {
		t.Fatalf("Unsetenv: %v", err)
	}
	if got := SeedFromEnv(9); got != 9 {
		t.Errorf("SeedFromEnv(unset) = %d, want default 9", got)
	}
}

func TestEvaluatorRoll(t *testing.T) {
	var ev Evaluator
	a, err := ev.Roll(context.Background(), "2d20kh1+5", 7)
	if err != nil {
		t.Fatalf("Roll error: %v", err)
	}
	b, err := ev.Roll(context.Background(), "2d20kh1+5", 7)
	if err != nil {
		t.Fatalf("Roll error: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("same seed diverged: %+v vs %+v", a, b)
	}
	if a.Total < 6 || a.Total > 25 {
		t.Errorf("Total = %d, want in [6,25]", a.Total)
	}
	if _, err := ev.Roll(context.Background(), "101d6", 1); err == nil {
		t.Errorf("Roll(101d6) succeeded, want cap error")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ev.Roll(ctx, "d20", 1); err == nil {
		t.Errorf("Roll(cancelled ctx) succeeded, want context error")
	}
}

func TestVariableSubstitution(t *testing.T) {
	cases := []struct {
		name          string
		formula       string
		vars          map[string]int
		queue         []int
		wantFaces     []int
		wantKept      []int
		wantDiscarded []int
		wantTotal     int
		wantSuccesses int
		wantErr       bool
	}{
		{
			name:      "simple variable",
			formula:   "1d12 + @con",
			vars:      map[string]int{"con": 3},
			queue:     []int{7},
			wantFaces: []int{7}, wantKept: []int{7}, wantDiscarded: []int{},
			wantTotal: 10, wantSuccesses: 1, wantErr: false,
		},
		{
			name:      "variable in keep expression",
			formula:   "2d20kh1 + @mod",
			vars:      map[string]int{"mod": 5},
			queue:     []int{17, 4},
			wantFaces: []int{17, 4}, wantKept: []int{17}, wantDiscarded: []int{4},
			wantTotal: 22, wantSuccesses: 1, wantErr: false,
		},
		{
			name:      "negative variable",
			formula:   "1d20 - @penalty",
			vars:      map[string]int{"penalty": 2},
			queue:     []int{15},
			wantFaces: []int{15}, wantKept: []int{15}, wantDiscarded: []int{},
			wantTotal: 13, wantSuccesses: 1, wantErr: false,
		},
		{
			name:      "multiple variables",
			formula:   "@a + @b + @c",
			vars:      map[string]int{"a": 1, "b": 2, "c": 3},
			queue:     []int{},
			wantFaces: []int{}, wantKept: []int{}, wantDiscarded: []int{},
			wantTotal: 6, wantSuccesses: 0, wantErr: false,
		},
		{
			name:      "variable with multiplication",
			formula:   "2 * @mult",
			vars:      map[string]int{"mult": 4},
			queue:     []int{},
			wantFaces: []int{}, wantKept: []int{}, wantDiscarded: []int{},
			wantTotal: 8, wantSuccesses: 0, wantErr: false,
		},
		{
			name:      "variable in parentheses",
			formula:   "(@base + 2) * 3",
			vars:      map[string]int{"base": 4},
			queue:     []int{},
			wantFaces: []int{}, wantKept: []int{}, wantDiscarded: []int{},
			wantTotal: 18, wantSuccesses: 0, wantErr: false,
		},
		{
			name:    "unknown variable fails",
			formula: "1d20 + @unknown",
			vars:    map[string]int{"known": 5},
			queue:   []int{10},
			wantErr: true,
		},
		{
			name:    "nil vars map fails on variable",
			formula: "1d20 + @con",
			vars:    nil,
			queue:   []int{10},
			wantErr: true,
		},
		{
			name:    "empty vars map fails on variable",
			formula: "1d20 + @con",
			vars:    map[string]int{},
			queue:   []int{10},
			wantErr: true,
		},
		{
			name:      "variable name with underscore",
			formula:   "1d6 + @my_var",
			vars:      map[string]int{"my_var": 2},
			queue:     []int{3},
			wantFaces: []int{3}, wantKept: []int{3}, wantDiscarded: []int{},
			wantTotal: 5, wantSuccesses: 1, wantErr: false,
		},
		{
			name:      "variable name with numbers",
			formula:   "1d6 + @con12",
			vars:      map[string]int{"con12": 3},
			queue:     []int{4},
			wantFaces: []int{4}, wantKept: []int{4}, wantDiscarded: []int{},
			wantTotal: 7, wantSuccesses: 1, wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ast, err := Parse(tc.formula)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.formula, err)
			}
			got, err := ast.EvalWithVars(NewQueueRoller(tc.queue...), tc.vars)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("EvalWithVars(%q, %v) succeeded, want error", tc.formula, tc.vars)
				}
				if !strings.Contains(err.Error(), "unknown variable") {
					t.Errorf("EvalWithVars(%q) error %q does not mention unknown variable", tc.formula, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("EvalWithVars(%q, %v) error: %v", tc.formula, tc.vars, err)
			}
			if !reflect.DeepEqual(got.Faces, tc.wantFaces) {
				t.Errorf("Faces = %v, want %v", got.Faces, tc.wantFaces)
			}
			if !reflect.DeepEqual(got.Kept, tc.wantKept) {
				t.Errorf("Kept = %v, want %v", got.Kept, tc.wantKept)
			}
			if !reflect.DeepEqual(got.Discarded, tc.wantDiscarded) {
				t.Errorf("Discarded = %v, want %v", got.Discarded, tc.wantDiscarded)
			}
			if got.Total != tc.wantTotal {
				t.Errorf("Total = %d, want %d", got.Total, tc.wantTotal)
			}
			if got.Successes != tc.wantSuccesses {
				t.Errorf("Successes = %d, want %d", got.Successes, tc.wantSuccesses)
			}
			if got.Successes != len(got.Kept) {
				t.Errorf("Successes = %d, want len(Kept) = %d", got.Successes, len(got.Kept))
			}
			if len(got.Breakdown) == 0 && (len(tc.wantFaces) > 0 || tc.wantTotal != 0) {
				t.Errorf("Breakdown is empty")
			}
		})
	}
}

func TestEvaluatorRollWithVars(t *testing.T) {
	var ev Evaluator
	a, err := ev.RollWithVars(context.Background(), "1d12 + @con", 7, map[string]int{"con": 3})
	if err != nil {
		t.Fatalf("RollWithVars error: %v", err)
	}
	b, err := ev.RollWithVars(context.Background(), "1d12 + @con", 7, map[string]int{"con": 3})
	if err != nil {
		t.Fatalf("RollWithVars error: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("same seed diverged: %+v vs %+v", a, b)
	}
	if a.Total < 4 || a.Total > 15 {
		t.Errorf("Total = %d, want in [4,15]", a.Total)
	}

	// Unknown variable should fail
	if _, err := ev.RollWithVars(context.Background(), "1d20 + @unk", 1, map[string]int{}); err == nil {
		t.Errorf("RollWithVars(unknown var) succeeded, want error")
	} else if !strings.Contains(err.Error(), "unknown variable") {
		t.Errorf("RollWithVars error %q does not mention unknown variable", err)
	}

	// Cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ev.RollWithVars(ctx, "d20", 1, nil); err == nil {
		t.Errorf("RollWithVars(cancelled ctx) succeeded, want context error")
	}
}

func TestVariableFuzz(t *testing.T) {
	// Fuzz test for variable parsing
	testCases := []string{
		"@con",
		"@CON",
		"@Con",
		"@con12",
		"@my_var",
		"@_private",
		"1d20+@mod",
		"2d20kh1+@mod",
		"@a+@b",
		"(@x)*2",
		"-@penalty",
	}
	for _, tc := range testCases {
		ast, err := Parse(tc)
		if err != nil {
			t.Errorf("Parse(%q) error: %v", tc, err)
			continue
		}
		// Should evaluate without error when vars provided
		_, err = ast.EvalWithVars(NewQueueRoller(1, 2, 3, 4, 5), map[string]int{"con": 1, "mod": 2, "a": 3, "b": 4, "x": 5, "penalty": 1, "CON": 1, "Con": 1, "con12": 1, "my_var": 1, "_private": 1})
		if err != nil {
			t.Errorf("EvalWithVars(%q) error: %v", tc, err)
		}
	}
}
