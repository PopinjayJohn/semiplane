package rules_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules"
)

func TestKindAcceptsOnlyLowercaseTokenLikeNames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		kind string
		want bool
	}{
		{name: "a rules-content kind", kind: "spell", want: true},
		{name: "a hyphenated kind", kind: "spell-list", want: true},
		{name: "an underscored kind", kind: "spell_list", want: true},
		{name: "digits", kind: "sr5", want: true},
		{name: "empty", kind: "", want: false},
		{name: "uppercase", kind: "Spell", want: false},
		{name: "a leading hyphen", kind: "-spell", want: false},
		{name: "a trailing underscore", kind: "spell_", want: false},
		{name: "consecutive hyphens", kind: "spell--list", want: false},
		{name: "a mixed doubled separator", kind: "spell-_list", want: false},
		{name: "a space", kind: "spell list", want: false},
		{name: "a quote", kind: `spell"`, want: false},
		{name: "over the bound", kind: strings.Repeat("a", 65), want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := rules.Kind(testCase.kind).Valid(); got != testCase.want {
				t.Fatalf("Kind(%q).Valid() = %t, want %t", testCase.kind, got, testCase.want)
			}
		})
	}
}

// TestSemiplaneKindsAreExactlyTheFiveTheOwnershipTableNames pins the set.
//
// §10.2.1's table is three wiki kinds and two VTT kinds, and `Validate` refuses a
// system for claiming one of them. Adding a sixth to `IsSemiplaneKind` would refuse
// a sixth kind without anybody deciding to, so the set is asserted here rather than
// left to the table: the next edit to that function has to come with a test change,
// and the test change is where the decision becomes visible.
func TestSemiplaneKindsAreExactlyTheFiveTheOwnershipTableNames(t *testing.T) {
	t.Parallel()

	owned := rules.SemiplaneKinds()

	if len(owned) != 5 {
		t.Fatalf("SemiplaneKinds reports %d kinds: %v", len(owned), owned)
	}

	want := []rules.Kind{
		rules.KindJournal,
		rules.KindHandout,
		rules.KindIndex,
		rules.KindToken,
		rules.KindScene,
	}

	if !slices.Equal(owned, want) {
		t.Fatalf("SemiplaneKinds = %v, want %v", owned, want)
	}

	for _, kind := range want {
		if !rules.IsSemiplaneKind(kind) {
			t.Errorf("IsSemiplaneKind(%q) = false", kind)
		}
	}

	// The other direction: a rules-content kind is not semiplane's, or every system
	// would be refused for declaring one.
	for _, kind := range []rules.Kind{"spell", "class", "feat", "creature", "ancestry", "prose"} {
		if rules.IsSemiplaneKind(kind) {
			t.Errorf("IsSemiplaneKind(%q) = true", kind)
		}
	}
}

// TestSemiplaneKindsHandsOutNothingACallerCanCorrupt is the half of "no package-level
// mutable state" that is easy to get wrong, asserted by **comparison rather than by
// corruption**: writing to a returned slice to see whether the write bites would work,
// and it would also poison every other test in the binary that reads the same array —
// including a parallel one, which under `-race` would be a report about this test
// rather than about the code. Two addresses answer the same question without a write.
//
// It is a real assertion rather than a formality because the two obvious
// implementations fail differently: returning a package-level slice shares every
// element, and returning a capacity-clamped view of a package-level array stops an
// append from reaching past the slice while leaving the elements themselves shared.
func TestSemiplaneKindsHandsOutNothingACallerCanCorrupt(t *testing.T) {
	t.Parallel()

	first := rules.SemiplaneKinds()
	second := rules.SemiplaneKinds()

	if len(first) == 0 || len(second) == 0 {
		t.Fatal("SemiplaneKinds returned nothing")
	}

	if &first[0] == &second[0] {
		t.Error("two calls returned views of one array, so a caller can reach the package's state")
	}
}

func TestAKindIsPrintedAsItWasDeclared(t *testing.T) {
	t.Parallel()

	declared := rules.Kind("Spell List")

	if declared.String() != "Spell List" {
		t.Fatalf("String() = %q, want the declaration verbatim", declared)
	}

	if declared.Valid() {
		t.Error("a kind with a space reports itself valid")
	}
}
