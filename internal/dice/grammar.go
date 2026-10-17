package dice

import (
	"fmt"
	"strings"

	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"
)

// Formula safety caps. Every rejection names the violated cap.
const (
	// MaxInputLen bounds the raw formula in bytes (formulas are ASCII).
	// Longer input is rejected before parsing.
	MaxInputLen = 64
	// MaxCount bounds the dice count of a single term ([count]d sides).
	MaxCount = 100
	// MaxSides bounds the die size of a single term (d sides).
	MaxSides = 1000
	// MaxParenDepth bounds parenthesis nesting, checked on the AST
	// after parsing.
	MaxParenDepth = 4
)

// diceLexer tokenizes formulas. An explicit token set (rather than the
// default text/scanner lexer) is required so that "d20" lexes as Dice+Int
// instead of a single identifier; whitespace is elided.
var diceLexer = lexer.MustSimple([]lexer.SimpleRule{
	{Name: "Whitespace", Pattern: `\s+`},
	{Name: "Int", Pattern: `\d+`},
	{Name: "Keep", Pattern: `[kK][hHlL]`},
	{Name: "Dice", Pattern: `[dD]`},
	{Name: "Op", Pattern: `[-+*/()]`},
})

var formulaParser = participle.MustBuild[formula](
	participle.Lexer(diceLexer),
	participle.Elide("Whitespace"),
	participle.UseLookahead(4),
)

// Precedence is hand-layered (participle has no precedence option):
// sum -> product ((+|-) product)*, product -> unary ((*|/) unary)*,
// unary -> -? atom, atom -> dice | int | '(' sum ')'.
type formula struct {
	Expr *sumGram `parser:"@@"`
}

type sumGram struct {
	First *prodGram `parser:"@@"`
	Rest  []*sumOp  `parser:"@@*"`
}

type sumOp struct {
	Op   string    `parser:"@('+' | '-')"`
	Next *prodGram `parser:"@@"`
}

type prodGram struct {
	First *unaryGram `parser:"@@"`
	Rest  []*prodOp  `parser:"@@*"`
}

type prodOp struct {
	Op   string     `parser:"@('*' | '/')"`
	Next *unaryGram `parser:"@@"`
}

type unaryGram struct {
	Neg  bool      `parser:"@'-'?"`
	Atom *atomGram `parser:"@@"`
}

type atomGram struct {
	Dice *diceGram `parser:"  @@"`
	Num  *int      `parser:"| @Int"`
	Sub  *sumGram  `parser:"| '(' @@ ')'"`
}

type diceGram struct {
	Count *int      `parser:"(@Int)?"`
	D     string    `parser:"@('d' | 'D')"`
	Sides int       `parser:"@Int"`
	Keep  *keepGram `parser:"(@@)?"`
}

type keepGram struct {
	Op string `parser:"@Keep"`
	N  int    `parser:"@Int"`
}

// parseFormula parses input and converts it to an evaluable node,
// enforcing every cap. depth tracks parenthesis nesting.
func parseFormula(input string) (node, error) {
	if len(input) > MaxInputLen {
		return nil, fmt.Errorf("dice: input length %d exceeds MaxInputLen=%d", len(input), MaxInputLen)
	}
	if strings.TrimSpace(input) == "" {
		return nil, fmt.Errorf("dice: empty formula")
	}
	f, err := formulaParser.ParseString("formula", input)
	if err != nil {
		return nil, fmt.Errorf("dice: parse %q: %w", input, err)
	}
	return buildSum(f.Expr, 0)
}

func buildSum(s *sumGram, depth int) (node, error) {
	first, err := buildProd(s.First, depth)
	if err != nil {
		return nil, err
	}
	out := &addExpr{first: first}
	for _, r := range s.Rest {
		next, err := buildProd(r.Next, depth)
		if err != nil {
			return nil, err
		}
		op := addOp(r.Op[0])
		if op != '+' && op != '-' {
			return nil, fmt.Errorf("dice: invalid additive operator %q", r.Op)
		}
		out.rest = append(out.rest, addOpNode{op: op, next: next})
	}
	return out, nil
}

func buildProd(p *prodGram, depth int) (node, error) {
	first, err := buildUnary(p.First, depth)
	if err != nil {
		return nil, err
	}
	out := &mulExpr{first: first}
	for _, r := range p.Rest {
		next, err := buildUnary(r.Next, depth)
		if err != nil {
			return nil, err
		}
		op := mulOp(r.Op[0])
		if op != '*' && op != '/' {
			return nil, fmt.Errorf("dice: invalid multiplicative operator %q", r.Op)
		}
		out.rest = append(out.rest, mulOpNode{op: op, next: next})
	}
	return out, nil
}

func buildUnary(u *unaryGram, depth int) (node, error) {
	atom, err := buildAtom(u.Atom, depth)
	if err != nil {
		return nil, err
	}
	if u.Neg {
		return &negExpr{inner: atom}, nil
	}
	return atom, nil
}

func buildAtom(a *atomGram, depth int) (node, error) {
	switch {
	case a.Dice != nil:
		return buildDice(a.Dice)
	case a.Num != nil:
		return &intLit{value: *a.Num}, nil
	case a.Sub != nil:
		if depth+1 > MaxParenDepth {
			return nil, fmt.Errorf("dice: paren depth %d exceeds MaxParenDepth=%d", depth+1, MaxParenDepth)
		}
		inner, err := buildSum(a.Sub, depth+1)
		if err != nil {
			return nil, err
		}
		return &parenExpr{inner: inner}, nil
	default:
		return nil, fmt.Errorf("dice: empty atom")
	}
}

func buildDice(d *diceGram) (node, error) {
	count := 1
	if d.Count != nil {
		count = *d.Count
	}
	if count < 1 || count > MaxCount {
		return nil, fmt.Errorf("dice: count %d out of range 1..MaxCount=%d", count, MaxCount)
	}
	if d.Sides < 1 || d.Sides > MaxSides {
		return nil, fmt.Errorf("dice: sides %d out of range 1..MaxSides=%d", d.Sides, MaxSides)
	}
	out := &diceExpr{count: count, sides: d.Sides}
	src := fmt.Sprintf("%dd%d", count, d.Sides)
	if d.Keep != nil {
		keep := d.Keep.N
		if keep < 1 || keep > count {
			return nil, fmt.Errorf("dice: keep %d out of range 1..count (MaxKeep<=count, count=%d)", keep, count)
		}
		switch strings.ToLower(d.Keep.Op) {
		case "kh":
			out.keepHigh = true
		case "kl":
			out.keepHigh = false
		default:
			return nil, fmt.Errorf("dice: invalid keep operator %q, want kh|kl", d.Keep.Op)
		}
		out.hasKeep = true
		out.keep = keep
		src += strings.ToLower(d.Keep.Op) + fmt.Sprintf("%d", keep)
	}
	out.src = src
	return out, nil
}
