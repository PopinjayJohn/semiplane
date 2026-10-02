package rules_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

func TestNewIntentRefusesWhatTheHubCouldNotHaveProduced(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		op      rules.Op
		target  rules.ObjectID
		args    []byte
		wantErr error
	}{
		{name: "an op that is not a token", op: "Move Token", wantErr: rules.ErrInvalidOp},
		{name: "an empty op", op: "", wantErr: rules.ErrInvalidOp},
		{
			name:    "an op over the bound",
			op:      rules.Op(strings.Repeat("a", 65)),
			wantErr: rules.ErrInvalidOp,
		},
		{
			name:    "a target carrying a control character",
			op:      "roll",
			target:  "p1\n",
			wantErr: rules.ErrInvalidObject,
		},
		{
			name:    "arguments over the bound",
			op:      "roll",
			args:    make([]byte, rules.MaxOpArgsLen+1),
			wantErr: rules.ErrOpArgsTooLarge,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			intent, err := rules.NewIntent(testCase.op, testCase.target, testCase.args)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("want %v, got %v", testCase.wantErr, err)
			}

			if intent.Valid() {
				t.Error("a refused intent reports itself valid")
			}
		})
	}
}

// TestNewIntentCopiesTheBytesItWasGiven, because the bytes are a frame's and the
// frame's buffer is the transport's: a resolver that scribbled on them would corrupt
// a buffer the codec still holds.
func TestNewIntentCopiesTheBytesItWasGiven(t *testing.T) {
	t.Parallel()

	args := []byte(`{"expr":"2d20+3"}`)

	intent, err := rules.NewIntent("roll", "p1", args)
	if err != nil {
		t.Fatalf("NewIntent: %v", err)
	}

	args[0] = 'X'

	if string(intent.Args) != `{"expr":"2d20+3"}` {
		t.Fatalf("the caller's edit reached the intent: %q", intent.Args)
	}
}

// TestTheZeroIntentIsInvalid: a caller that forgot to build one must be told, because
// an empty op handed to a resolver resolves nothing and says nothing about why.
func TestTheZeroIntentIsInvalid(t *testing.T) {
	t.Parallel()

	var intent rules.Intent
	if intent.Valid() {
		t.Error("the zero intent reports itself valid")
	}

	// A hand-built value with only an op is a legitimate intent: the fields are
	// exported so an adapter can build one, and `Valid` is the check that it built it
	// correctly. `pause` has no arguments and no target.
	bare := rules.Intent{Op: "pause"}
	if !bare.Valid() {
		t.Error("an intent with no target and no arguments is not valid")
	}

	// The same value with an unshapeable op is not.
	if (rules.Intent{Op: "Pause"}).Valid() {
		t.Error("an intent with an uppercase op reports itself valid")
	}

	// Nor with a payload over the bound, which a caller may have assembled by hand.
	if (rules.Intent{Op: "roll", Args: make([]byte, rules.MaxOpArgsLen+1)}).Valid() {
		t.Error("an intent over the argument bound reports itself valid")
	}
}

// TestAnIntentWithNoTargetNamesOnlyItsOp, because `Target` is the one address the
// hub honours and a log line that omitted it would be hiding the difference between a
// campaign-wide intent and one aimed at a token.
func TestAnIntentWithNoTargetNamesOnlyItsOp(t *testing.T) {
	t.Parallel()

	intent, err := rules.NewIntent("pause", "", nil)
	if err != nil {
		t.Fatalf("NewIntent: %v", err)
	}

	if intent.String() != "pause" {
		t.Errorf("String() = %q, want the op alone", intent)
	}
}

// TestOpAcceptsExactlyTheWireTokenCharset, because the charset is copied from the
// codec rather than chosen: an op this package accepts and the wire refuses is an op
// no client could ever send.
func TestOpAcceptsExactlyTheWireTokenCharset(t *testing.T) {
	t.Parallel()

	cases := []struct {
		op   rules.Op
		want bool
	}{
		{op: "move_token", want: true},
		{op: "set_hp", want: true},
		{op: "roll2", want: true},
		{op: "", want: false},
		{op: "Move_Token", want: false},
		{op: "move-token", want: false},
		{op: "move token", want: false},
		{op: rules.Op(strings.Repeat("a", 65)), want: false},
	}

	for _, testCase := range cases {
		if got := testCase.op.Valid(); got != testCase.want {
			t.Errorf("Op(%q).Valid() = %t, want %t", testCase.op, got, testCase.want)
		}
	}

	if rules.Op("roll").String() != "roll" {
		t.Error("String did not return the stored text")
	}
}
