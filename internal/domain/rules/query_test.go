package rules_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

func TestAQueryNamesAViewAndForbidsANegativeLimit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		query rules.Query
		want  bool
	}{
		{name: "a view with no object", query: rules.Query{View: "character-sheet"}, want: true},
		{
			name:  "a view with an object",
			query: rules.Query{View: "spell-list", Object: "p1"},
			want:  true,
		},
		{
			name:  "a view with a limit",
			query: rules.Query{View: "spell-list", Limit: 20},
			want:  true,
		},
		{name: "no view", query: rules.Query{Object: "p1"}, want: false},
		{
			name:  "an object carrying a control character",
			query: rules.Query{View: "v", Object: "p1\n"},
			want:  false,
		},
		{name: "a negative limit", query: rules.Query{View: "v", Limit: -1}, want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.query.Check(); got != testCase.want {
				t.Fatalf("Check() = %t, want %t", got, testCase.want)
			}
		})
	}
}

// TestAPayloadCarriesTheViewItAnswers is the whole reason `Payload` is not an `any`:
// the renderer is chosen by the view, and a value that did not say which one would
// leave the choice to whichever lookup ran second.
func TestAPayloadCarriesTheViewItAnswers(t *testing.T) {
	t.Parallel()

	rows := []struct{ name, target string }{
		{name: "Grog", target: "12"},
		{name: "Grin", target: "4"},
	}

	payload, err := rules.NewPayload("attack-table", rows)
	if err != nil {
		t.Fatalf("NewPayload: %v", err)
	}

	if payload.View() != "attack-table" {
		t.Errorf("View() = %q", payload.View())
	}

	if !payload.Valid() {
		t.Error("a payload with a view reports itself invalid")
	}

	// The value is the system's own, passed through untouched.
	if !reflect.DeepEqual(payload.Value(), any(rows)) {
		t.Errorf("Value() = %v, want the value handed in", payload.Value())
	}
}

func TestAPayloadNeedsAViewAndTheZeroOneHasNone(t *testing.T) {
	t.Parallel()

	var payload rules.Payload

	if payload.Valid() {
		t.Error("the zero payload reports itself valid")
	}

	if payload.View() != "" {
		t.Errorf("the zero payload names view %q", payload.View())
	}

	if payload.Value() != nil {
		t.Errorf("the zero payload carries %v", payload.Value())
	}

	if _, err := rules.NewPayload("", nil); !errors.Is(err, rules.ErrInvalidView) {
		t.Fatalf("an unnamed payload was accepted: %v", err)
	}
}

// TestAPayloadWithNothingInItIsStillAPayload is §14's "renders for an empty `Derive`
// output" in one assertion: an empty result is a value to render, not an absence, and
// a renderer handed one produces an empty document rather than a blank page it cannot
// explain.
func TestAPayloadWithNothingInItIsStillAPayload(t *testing.T) {
	t.Parallel()

	empty, err := rules.NewPayload("spell-list", nil)
	if err != nil {
		t.Fatalf("NewPayload: %v", err)
	}

	if !empty.Valid() {
		t.Error("an empty payload reports itself invalid")
	}

	if empty.Value() != nil {
		t.Errorf("Value() = %v, want nil", empty.Value())
	}

	list, err := rules.NewPayload("spell-list", []string{})
	if err != nil {
		t.Fatalf("NewPayload: %v", err)
	}

	if !list.Valid() {
		t.Error("an empty list payload reports itself invalid")
	}
}

// TestDeriveAnswersWithAPayloadNamingTheViewAskedFor, which is the contract the
// renderer depends on: the stub is a stand-in for a system, and the assertion is that
// `Derive`'s answer is usable without the caller knowing anything else about it.
func TestDeriveAnswersWithAPayloadNamingTheViewAskedFor(t *testing.T) {
	t.Parallel()

	system := wellFormed()

	for _, view := range system.Views() {
		payload, err := system.Derive(aTabletop(t), rules.Query{View: view.Name, Object: "p1"})
		if err != nil {
			t.Fatalf("Derive(%q): %v", view.Name, err)
		}

		if !payload.Valid() {
			t.Fatalf("Derive(%q) answered with an invalid payload", view.Name)
		}

		if payload.View() != view.Name {
			t.Errorf("Derive(%q) answered for view %q", view.Name, payload.View())
		}
	}
}
