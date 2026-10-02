package rules_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

func TestNewMutationRefusesWhatTheHubCouldNotApply(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		target  rules.ObjectID
		op      rules.Op
		args    []byte
		wantErr error
	}{
		{name: "no target", op: "roll", wantErr: rules.ErrInvalidObject},
		{
			name:    "a target carrying a control character",
			target:  "p1\x00",
			op:      "roll",
			wantErr: rules.ErrInvalidObject,
		},
		{name: "no op", target: "p1", wantErr: rules.ErrInvalidOp},
		{name: "an op that is not a token", target: "p1", op: "Roll", wantErr: rules.ErrInvalidOp},
		{
			name:    "a payload over the bound",
			target:  "p1",
			op:      "roll",
			args:    make([]byte, rules.MaxOpArgsLen+1),
			wantErr: rules.ErrOpArgsTooLarge,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			mutation, err := rules.NewMutation(testCase.target, testCase.op, testCase.args)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("want %v, got %v", testCase.wantErr, err)
			}

			if mutation.Valid() {
				t.Error("a refused mutation reports itself valid")
			}
		})
	}
}

// TestAPayloadAtTheBoundIsAccepted, so the bound is a bound and not a smaller number
// hidden behind an off-by-one: an 8 KiB stat block has to fit.
func TestAPayloadAtTheBoundIsAccepted(t *testing.T) {
	t.Parallel()

	mutation, err := rules.NewMutation("p1", "roll", make([]byte, rules.MaxOpArgsLen))
	if err != nil {
		t.Fatalf("a payload of exactly the bound was refused: %v", err)
	}

	if !mutation.Valid() {
		t.Error("a payload of exactly the bound reports itself invalid")
	}

	if len(mutation.Args) != rules.MaxOpArgsLen {
		t.Errorf("the payload is %d bytes, want %d", len(mutation.Args), rules.MaxOpArgsLen)
	}
}

func TestNewMutationCopiesItsPayload(t *testing.T) {
	t.Parallel()

	args := []byte(`{"result":11}`)

	mutation, err := rules.NewMutation("p1", "roll", args)
	if err != nil {
		t.Fatalf("NewMutation: %v", err)
	}

	args[0] = 'X'

	if string(mutation.Args) != `{"result":11}` {
		t.Fatalf("the caller's edit reached the mutation: %q", mutation.Args)
	}
}

// TestTheZeroMutationIsInvalid, because a mutation with no address is a broadcast
// nobody can reconcile and a version nobody could stamp.
func TestTheZeroMutationIsInvalid(t *testing.T) {
	t.Parallel()

	var mutation rules.Mutation
	if mutation.Valid() {
		t.Error("the zero mutation reports itself valid")
	}

	if (rules.Mutation{Target: "p1"}).Valid() {
		t.Error("a mutation with no op reports itself valid")
	}

	if (rules.Mutation{Op: "roll"}).Valid() {
		t.Error("a mutation with no target reports itself valid")
	}

	// A removal is a normal mutation with a flag, so it validates the same way.
	removal := rules.Mutation{Target: "p1", Op: "remove_token", Remove: true}
	if !removal.Valid() {
		t.Error("a removal reports itself invalid")
	}
}

// TestNeitherAnIntentNorAMutationEverPrintsItsPayload is S-12.3 as a unit test: the
// `String` a log line would call carries the op and the address and nothing else,
// because the payload is where a resolved roll would leak from.
func TestNeitherAnIntentNorAMutationEverPrintsItsPayload(t *testing.T) {
	t.Parallel()

	secretish := []byte(`{"result":20,"note":"the passphrase is hunter2"}`)

	intent, err := rules.NewIntent("roll", "p1", secretish)
	if err != nil {
		t.Fatalf("NewIntent: %v", err)
	}

	mutation, err := rules.NewMutation("p1", "roll", secretish)
	if err != nil {
		t.Fatalf("NewMutation: %v", err)
	}

	printed := intent.String() + " " + mutation.String()

	for _, forbidden := range []string{"20", "hunter2", "passphrase", "{"} {
		if strings.Contains(printed, forbidden) {
			t.Errorf("%q reached a String method: %s", forbidden, printed)
		}
	}

	if printed != "roll p1 roll p1" {
		t.Errorf("printed %q", printed)
	}

	// And the removal case, where the word is the only difference a reader needs.
	mutation.Remove = true

	if mutation.String() != "remove roll p1" {
		t.Errorf("a removal printed %q", mutation.String())
	}
}
