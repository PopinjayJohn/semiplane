package rules_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

func TestNewStateSortsObjectsWhateverOrderTheyArriveIn(t *testing.T) {
	t.Parallel()

	// Three declarations, deliberately out of order: a test that passes because the
	// input happened to be sorted is a test that proves nothing.
	input := []rules.Object{
		{ID: "p3", Kind: rules.KindToken, Data: []byte("c")},
		{ID: "p1", Kind: rules.KindToken, Data: []byte("a")},
		{ID: "s1", Kind: rules.KindScene, Data: []byte("s")},
	}

	state, err := rules.NewState(7, input)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	want := []rules.ObjectID{"p1", "p3", "s1"}

	got := make([]rules.ObjectID, 0, state.Len())
	for _, object := range state.Objects() {
		got = append(got, object.ID)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Objects() = %v, want %v", got, want)
	}

	if state.Revision() != 7 {
		t.Errorf("Revision() = %d, want 7", state.Revision())
	}

	// The caller's slice is not reordered either, which is the same property from the
	// other side: a hub building a snapshot must not find its own collection
	// shuffled by doing it.
	if input[0].ID != "p3" {
		t.Errorf("NewState reordered the caller's slice: %v", input)
	}
}

func TestNewStateRefusesTwoObjectsSharingOneID(t *testing.T) {
	t.Parallel()

	_, err := rules.NewState(1, []rules.Object{
		{ID: "p1", Kind: rules.KindToken, Data: []byte("a")},
		{ID: "p1", Kind: rules.KindToken, Data: []byte("b")},
	})
	if !errors.Is(err, rules.ErrDuplicateObject) {
		t.Fatalf("two objects with one id were accepted: %v", err)
	}
}

func TestTheZeroStateIsAnEmptySnapshot(t *testing.T) {
	t.Parallel()

	var state rules.State

	if state.Len() != 0 {
		t.Errorf("Len() = %d, want 0", state.Len())
	}

	if state.Revision() != 0 {
		t.Errorf("Revision() = %d, want 0", state.Revision())
	}

	if objects := state.Objects(); len(objects) != 0 {
		t.Errorf("Objects() = %v, want none", objects)
	}

	if objects := state.OfKind(rules.KindToken); len(objects) != 0 {
		t.Errorf("OfKind(token) = %v, want none", objects)
	}

	if _, found := state.Lookup("p1"); found {
		t.Error("the empty state found an object")
	}

	if _, err := rules.NewState(0, nil); err != nil {
		t.Errorf("NewState with no objects: %v", err)
	}
}

func TestStateAccessorsAnswerForEachObjectAndKind(t *testing.T) {
	t.Parallel()

	state := aTabletop(t)

	tokens := state.OfKind(rules.KindToken)
	if len(tokens) != 2 {
		t.Fatalf("OfKind(token) returned %d objects, want 2", len(tokens))
	}

	if tokens[0].ID != "p1" || tokens[1].ID != "p3" {
		t.Errorf("OfKind did not keep id order: %v", tokens)
	}

	// An unknown kind is inert rather than an error, which is §10.8's last row and
	// S-3.3's degradation applied to the other direction: a system asking for a kind
	// this campaign has none of is the same question asked backwards.
	if missing := state.OfKind("spell"); len(missing) != 0 {
		t.Errorf("OfKind(spell) = %v, want none", missing)
	}

	object, found := state.Lookup("p3")
	if !found {
		t.Fatal("Lookup(p3) found nothing")
	}

	if string(object.Data) != `{"hp":3}` {
		t.Errorf("Lookup(p3) returned %q", object.Data)
	}

	if _, found := state.Lookup("p9"); found {
		t.Error("Lookup found an object that is not there")
	}

	if _, found := state.Lookup(""); found {
		t.Error("Lookup accepted an empty id")
	}
}

// TestEveryObjectHandedOutIsACopy is the property that makes the unexported fields
// worth their cost, asserted twice over because there are two clones and either one
// removed is enough to break it: one in `NewState`, one in the accessors.
func TestEveryObjectHandedOutIsACopy(t *testing.T) {
	t.Parallel()

	objects := []rules.Object{{ID: "p1", Kind: rules.KindToken, Data: []byte("abc")}}

	state, err := rules.NewState(1, objects)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}

	// The caller's own bytes, which the snapshot must not be sharing.
	objects[0].Data[0] = 'z'

	first, found := state.Lookup("p1")
	if !found {
		t.Fatal("Lookup(p1) found nothing")
	}

	if string(first.Data) != "abc" {
		t.Fatalf("the caller's edit reached the snapshot: %q", first.Data)
	}

	// The accessor's copy, twice over: two reads must not alias each other.
	first.Data[0] = 'y'

	second, found := state.Lookup("p1")
	if !found {
		t.Fatal("Lookup(p1) found nothing the second time")
	}

	if string(second.Data) != "abc" {
		t.Fatalf("a write to one handed-out object reached the next: %q", second.Data)
	}

	for _, object := range state.Objects() {
		object.Data[0] = 'x'
	}

	if again, _ := state.Lookup("p1"); string(again.Data) != "abc" {
		t.Fatalf("a write to an Objects() element reached the state: %q", again.Data)
	}
}

func TestAnObjectIDIsRefusedWhenItWouldBreakAProtocolLine(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   string
		want bool
	}{
		{name: "a simple id", id: "p1", want: true},
		{name: "a hyphenated id", id: "grog-the-wise", want: true},
		{name: "a space", id: "grog the wise", want: true},
		{name: "empty", id: "", want: false},
		{name: "a newline", id: "p1\np2", want: false},
		{name: "a tab", id: "p1\t", want: false},
		{name: "a control character", id: "p1\x00", want: false},
		{name: "over the bound", id: strings.Repeat("a", 129), want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := rules.ObjectID(testCase.id).Valid(); got != testCase.want {
				t.Fatalf("ObjectID(%q).Valid() = %t, want %t", testCase.id, got, testCase.want)
			}
		})
	}

	if rules.ObjectID("p1").String() != "p1" {
		t.Error("String did not return the stored text")
	}
}
