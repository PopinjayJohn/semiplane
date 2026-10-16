package dice

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Result is the outcome of evaluating one formula.
//
// Faces holds every face rolled, in roll order, across all dice terms.
// Kept holds the faces that count toward the total, in roll order;
// Discarded holds faces dropped by kh/kl. Straight rolls (no keep) keep
// everything and discard nothing. Successes is len(Kept): the numeric-only
// symbolic-dice seam (symbols are unmodelled; this int is the placeholder).
// Breakdown holds one line per evaluated leaf — each dice roll, each integer
// literal — in evaluation order, plus a "(subexpr) = n" summary line per
// parenthesized group. Operators fold silently into Total.
type Result struct {
	Faces     []int
	Total     int
	Successes int
	Kept      []int
	Discarded []int
	Breakdown []string
}

// AST is a parsed, cap-checked formula ready for evaluation.
type AST struct {
	root node
}

// Parse parses formula and enforces every cap (see grammar.go). A nil *AST
// is never returned with a nil error.
func Parse(formula string) (*AST, error) {
	root, err := parseFormula(formula)
	if err != nil {
		return nil, err
	}
	return &AST{root: root}, nil
}

// Eval evaluates the AST, drawing faces from roller in left-to-right order.
// A nil roller is an error, never a fallback to a global RNG. On error the
// returned Result is zero.
func (a *AST) Eval(roller *Roller) (Result, error) {
	if a == nil || a.root == nil {
		return Result{}, errors.New("dice: nil AST")
	}
	if roller == nil {
		return Result{}, errors.New("dice: nil roller")
	}
	acc := &Result{
		Faces:     []int{},
		Kept:      []int{},
		Discarded: []int{},
		Breakdown: []string{},
	}
	total, err := a.root.eval(roller, acc)
	if err != nil {
		return Result{}, err
	}
	acc.Total = total
	acc.Successes = len(acc.Kept)
	return *acc, nil
}

// Evaluator evaluates formulas with a seed-derived deterministic roller.
//
// The method set is intentionally structurally compatible with the plugin
// dice interface — Roll(context.Context, formula string, seed int64)
// (Result, error) — without importing pkg/pluginapi (see doc.go).
type Evaluator struct{}

// Roll parses formula and evaluates it with NewSeededRoller(seed). Equal
// seeds yield equal results. A cancelled context fails before parsing.
func (Evaluator) Roll(ctx context.Context, formula string, seed int64) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("dice: %w", err)
	}
	ast, err := Parse(formula)
	if err != nil {
		return Result{}, err
	}
	return ast.Eval(NewSeededRoller(seed))
}

// node is one evaluable AST node.
type node interface {
	eval(r *Roller, acc *Result) (int, error)
}

type addOp byte

type addExpr struct {
	first node
	rest  []addOpNode
}

type addOpNode struct {
	op   addOp
	next node
}

type mulOp byte

type mulExpr struct {
	first node
	rest  []mulOpNode
}

type mulOpNode struct {
	op   mulOp
	next node
}

type negExpr struct {
	inner node
}

type intLit struct {
	value int
}

type parenExpr struct {
	inner node
}

type diceExpr struct {
	count    int
	sides    int
	keep     int
	keepHigh bool
	hasKeep  bool
	src      string
}

func (n *addExpr) eval(r *Roller, acc *Result) (int, error) {
	total, err := n.first.eval(r, acc)
	if err != nil {
		return 0, err
	}
	for _, o := range n.rest {
		v, err := o.next.eval(r, acc)
		if err != nil {
			return 0, err
		}
		switch o.op {
		case '+':
			total, err = addOvf(total, v)
		case '-':
			total, err = subOvf(total, v)
		default:
			return 0, fmt.Errorf("dice: invalid additive operator %q", byte(o.op))
		}
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func (n *mulExpr) eval(r *Roller, acc *Result) (int, error) {
	total, err := n.first.eval(r, acc)
	if err != nil {
		return 0, err
	}
	for _, o := range n.rest {
		v, err := o.next.eval(r, acc)
		if err != nil {
			return 0, err
		}
		switch o.op {
		case '*':
			total, err = mulOvf(total, v)
		case '/':
			if v == 0 {
				return 0, errors.New("dice: division by zero")
			}
			total = total / v // integer division truncates toward zero
		default:
			return 0, fmt.Errorf("dice: invalid multiplicative operator %q", byte(o.op))
		}
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func (n *negExpr) eval(r *Roller, acc *Result) (int, error) {
	v, err := n.inner.eval(r, acc)
	if err != nil {
		return 0, err
	}
	return subOvf(0, v)
}

func (n *intLit) eval(_ *Roller, acc *Result) (int, error) {
	acc.Breakdown = append(acc.Breakdown, fmt.Sprintf("%d", n.value))
	return n.value, nil
}

func (n *parenExpr) eval(r *Roller, acc *Result) (int, error) {
	v, err := n.inner.eval(r, acc)
	if err != nil {
		return 0, err
	}
	acc.Breakdown = append(acc.Breakdown, fmt.Sprintf("(subexpr) = %d", v))
	return v, nil
}

func (n *diceExpr) eval(r *Roller, acc *Result) (int, error) {
	faces := make([]int, 0, n.count)
	for i := 0; i < n.count; i++ {
		faces = append(faces, r.Roll(n.sides))
	}
	acc.Faces = append(acc.Faces, faces...)
	kept := []int{}
	discarded := []int{}
	sub := 0
	if !n.hasKeep {
		kept = append(kept, faces...)
		for _, f := range faces {
			sub += f
		}
		acc.Kept = append(acc.Kept, kept...)
		acc.Breakdown = append(acc.Breakdown, fmt.Sprintf("%s -> faces %v = %d", n.src, faces, sub))
		return sub, nil
	}
	inKept := make([]bool, len(faces))
	for _, j := range keptIndices(faces, n.keep, n.keepHigh) {
		inKept[j] = true
	}
	for j, f := range faces {
		if inKept[j] {
			kept = append(kept, f)
			sub += f
		} else {
			discarded = append(discarded, f)
		}
	}
	acc.Kept = append(acc.Kept, kept...)
	acc.Discarded = append(acc.Discarded, discarded...)
	acc.Breakdown = append(acc.Breakdown, fmt.Sprintf("%s -> faces %v kept %v discarded %v = %d", n.src, faces, kept, discarded, sub))
	return sub, nil
}

// keptIndices returns the indices of the keep highest (or lowest) faces.
// Ties break toward the earlier roll; the result is sorted ascending so
// Kept preserves roll order.
func keptIndices(faces []int, keep int, high bool) []int {
	order := make([]int, len(faces))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		fa, fb := faces[a], faces[b]
		if fa != fb {
			if high {
				return fb - fa
			}
			return fa - fb
		}
		return a - b
	})
	chosen := append([]int(nil), order[:keep]...)
	slices.Sort(chosen)
	return chosen
}

var errOverflow = errors.New("dice: integer overflow")

func addOvf(a, b int) (int, error) {
	c := a + b
	if (b > 0 && c < a) || (b < 0 && c > a) {
		return 0, errOverflow
	}
	return c, nil
}

func subOvf(a, b int) (int, error) {
	c := a - b
	if (b > 0 && c > a) || (b < 0 && c < a) {
		return 0, errOverflow
	}
	return c, nil
}

func mulOvf(a, b int) (int, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	c := a * b
	if c/b != a {
		return 0, errOverflow
	}
	return c, nil
}
