package rules_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

func TestAGrammarNeedsANameAndATerm(t *testing.T) {
	t.Parallel()

	full := rules.Grammar{
		Notation: "2d6-pool",
		Summary:  "a pool of six-sided dice counted against a target",
		Example:  "5d6",
		Terms: []rules.Term{{
			Name:    "pool",
			Summary: "a number of six-sided dice",
			Pattern: `^\d*d6$`,
		}},
	}

	if !full.Valid() {
		t.Fatal("a complete grammar reports itself invalid")
	}

	cases := []struct {
		name    string
		grammar rules.Grammar
		wantErr error
	}{
		{
			name:    "no notation",
			grammar: rules.Grammar{Terms: full.Terms},
			wantErr: rules.ErrNotAGrammar,
		},
		{name: "no terms", grammar: rules.Grammar{Notation: "d20"}, wantErr: rules.ErrNotAGrammar},
		{name: "nothing at all", grammar: rules.Grammar{}, wantErr: rules.ErrNotAGrammar},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			system := wellFormed()
			system.grammar = testCase.grammar

			if err := rules.Validate(system); !errors.Is(err, testCase.wantErr) {
				t.Fatalf("want %v, got %v", testCase.wantErr, err)
			}
		})
	}

	// A pattern that compiles is required, and it is required *before* registration
	// rather than at first use: a client validating a roll against a pattern that does
	// not compile would be told its expression was fine.
	var zero rules.Grammar
	if zero.Valid() {
		t.Error("the zero grammar reports itself valid")
	}
}

// TestOneNotationIsNotAnotherSystemsGrammar is the reason `Grammar` is data. The stub
// declares a d20 grammar and a system that shares nothing with it declares something
// else, and both are accepted — because the alternative is a parser here, and a parser
// here is a parser that only one system can use.
func TestOneNotationIsNotAnotherSystemsGrammar(t *testing.T) {
	t.Parallel()

	ours := wellFormed()

	theirs := &stub{
		id:      "2d6-pool",
		title:   "Something entirely different",
		ruleset: "pool@1",
		grammar: rules.Grammar{
			Notation: "2d6-pool",
			Example:  "5d6",
			Terms:    []rules.Term{{Name: "pool", Pattern: `^\d*d6$`}},
		},
		kinds: []rules.Kind{"move", "doctrine"},
		views: []rules.View{{
			Name:     "move-card",
			Title:    "Move",
			Renderer: rules.RendererPlugin,
		}},
	}

	for _, system := range []*stub{ours, theirs} {
		if err := rules.Validate(system); err != nil {
			t.Fatalf("system %q was refused: %v", system.ID(), err)
		}
	}
}

func TestAParsedExpressionNamesTheSystemThatParsedIt(t *testing.T) {
	t.Parallel()

	system := wellFormed()

	expr, err := system.Parse("2d20+3")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if expr.Owner() != system.ID() {
		t.Errorf("Owner() = %q, want %q", expr.Owner(), system.ID())
	}

	if expr.Notation != "d20" {
		t.Errorf("Notation = %q, want the grammar's own name", expr.Notation)
	}

	if expr.Source != "2d20+3" {
		t.Errorf("Source = %q, want the text verbatim", expr.Source)
	}

	if expr.Node == nil {
		t.Error("the stub's node is missing, so the payload is not what it would be in anger")
	}

	if expr.String() != expr.Source {
		t.Errorf("String() = %q, want the source", expr)
	}

	if !expr.Valid() {
		t.Error("a parsed expression reports itself invalid")
	}

	// The same text parsed twice is the same value, which is what makes an audit
	// trail of rolls possible at all (§16.3).
	again, err := system.Parse("2d20+3")
	if err != nil {
		t.Fatalf("Parse again: %v", err)
	}

	if again.Source != expr.Source || again.Owner() != expr.Owner() ||
		again.Notation != expr.Notation {
		t.Errorf("two parses of one text disagree:\n %+v\n %+v", expr, again)
	}
}

// TestTheZeroExpressionIsInvalid is the forger's case: a value that claims to be a
// parse with no owner, no notation and no text is not a parse, and a hub that routed
// on it would be routing on nothing.
func TestTheZeroExpressionIsInvalid(t *testing.T) {
	t.Parallel()

	var expr rules.Expr

	if expr.Valid() {
		t.Error("the zero expression reports itself valid")
	}

	if expr.Owner() != "" {
		t.Errorf("the zero expression names owner %q", expr.Owner())
	}

	// Every field is load-bearing: each one alone is enough to make it unusable.
	for _, built := range []rules.Expr{
		rules.NewExpr("", "d20", "2d20+3", nil),
		rules.NewExpr("5e-2024", "", "2d20+3", nil),
		rules.NewExpr("5e-2024", "d20", "", nil),
	} {
		if built.Valid() {
			t.Errorf("%+v reports itself valid", built)
		}
	}

	// A node is not required. An expression whose parse is "no dice" is still an
	// expression, and a system that returns one with no node must not be told it
	// built nothing.
	bare := rules.NewExpr("5e-2024", "d20", "0", nil)
	if !bare.Valid() {
		t.Error("an expression with no node reports itself invalid")
	}
}

// TestAnExpressionIsRoutedToItsOwnerAndNotToWhicheverSystemAnswers: the refusal the
// `Owner` field exists to make expressible, asserted as the field's value rather than
// as a hub's behaviour, because the hub is P4's adapter and not this package's.
func TestAnExpressionIsRoutedToItsOwnerAndNotToWhicheverSystemAnswers(t *testing.T) {
	t.Parallel()

	mine := wellFormed()
	theirs := &stub{id: "2d6-pool", title: "Other", ruleset: "pool@1"}

	expr, err := theirs.Parse("5d6")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if expr.Owner() == mine.ID() {
		t.Fatal("the expression claims the wrong owner, so routing by it would be routing by luck")
	}

	if expr.Owner() != theirs.ID() {
		t.Fatalf("Owner() = %q, want %q", expr.Owner(), theirs.ID())
	}

	// A refusal is what a hub must produce, and the comparison is the whole contract:
	// `Owner() != the system about to be called`.
	if expr.Owner() == mine.ID() {
		t.Error("a hub routing this expression to the stub would not have noticed")
	}
}

// TestAnExpressionNeverPrintsItsNode, because the node is the system's parse of text
// a client sent and a resolved roll would leak out of it into a log.
func TestAnExpressionNeverPrintsItsNode(t *testing.T) {
	t.Parallel()

	node := map[string]string{"result": "11", "note": "the passphrase is hunter2"}

	expr := rules.NewExpr("5e-2024", "d20", "4d6+2", node)

	for _, forbidden := range []string{"11", "hunter2", "passphrase"} {
		if strings.Contains(expr.String(), forbidden) {
			t.Errorf("%q reached the expression's String: %s", forbidden, expr)
		}
	}

	if expr.String() != "4d6+2" {
		t.Errorf("String() = %q, want the source alone", expr)
	}
}
