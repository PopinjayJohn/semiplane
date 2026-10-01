package domain_test

import (
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
)

// fakeKinds is a PageKindRegistry over a fixed set, standing in for the plugin
// registry phase 8 builds.
//
// A fake rather than the real registry because the property under test is the
// answer, not the lookup: the whole claim of S-3.3 is that the set of known kinds
// arrives from outside this repository, and a test that needed the real registry
// would be asserting phase 8's code as much as this phase's.
type fakeKinds map[string]bool

func (k fakeKinds) HasPageKind(name string) bool {
	return k[name]
}

// TestResolvePageKindIsRegistryBacked is S-3.3's core and S-3.4's reason.
//
// One registry, two declarations, and the two answers differ only because the
// registry said so. A hardcoded list would pass a test written against the kinds
// semiplane happens to own and would degrade every plugin's kind to prose, which
// is the failure this shape exists to prevent.
func TestResolvePageKindIsRegistryBacked(t *testing.T) {
	t.Parallel()

	registry := fakeKinds{
		"token":           true,
		"scene":           true,
		"shadow-hexblade": true,
		"innate-ward":     true,
	}

	cases := []struct {
		name     string
		declared string
		registry domain.PageKindRegistry
		want     domain.PageKind
	}{
		{
			name:     "a kind the registry knows",
			declared: "token",
			registry: registry,
			want:     domain.PageKind("token"),
		},
		{
			// A gameplay plugin's rules-content kind. There is no way for this
			// repository to know the name, which is the point: the registry is the
			// only thing that has to be updated when a plugin adds one.
			name:     "a plugin kind the registry knows",
			declared: "shadow-hexblade",
			registry: registry,
			want:     domain.PageKind("shadow-hexblade"),
		},
		{
			name:     "a kind the registry does not know",
			declared: "tokne",
			registry: registry,
			want:     domain.KindProse,
		},
		{
			name:     "an absent kind",
			declared: "",
			registry: registry,
			want:     domain.KindProse,
		},
		{
			// The registry is the only source. A kind it once knew and no longer
			// does degrades, which is what makes removing a plugin safe (ADR 0007
			// and the architecture record both say a removal must not destroy
			// content).
			name:     "a kind the registry has forgotten",
			declared: "shadow-hexblade",
			registry: fakeKinds{},
			want:     domain.KindProse,
		},
		{
			// No registry at all: phase 8 has not landed, and semiplane serves a
			// wiki in the meantime (S-14.8). Degrading is the answer; panicking
			// would take the content read path down with it.
			name:     "no registry",
			declared: "token",
			registry: nil,
			want:     domain.KindProse,
		},
		{
			// No folding and no trimming, for the reason ParseRole gives: a kind
			// reaches a URL segment, a token-list filter and a template class name,
			// and a padded value would have to be escaped in each of them.
			name:     "a padded kind",
			declared: " token ",
			registry: registry,
			want:     domain.KindProse,
		},
		{
			name:     "a differently cased kind",
			declared: "Token",
			registry: registry,
			want:     domain.KindProse,
		},
		{
			// An empty name is not `prose`, and a registry that happened to answer
			// true for it would hand back a kind whose String is the empty string --
			// the confusion "prose is a first-class value" exists to prevent.
			name:     "a whitespace-only kind",
			declared: "   ",
			registry: registry,
			want:     domain.KindProse,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := domain.ResolvePageKind(testCase.declared, testCase.registry)

			if got != testCase.want {
				t.Errorf("ResolvePageKind(%q) = %q, want %q", testCase.declared, got, testCase.want)
			}
		})
	}
}

// TestKindProseIsNotTheEmptyString is the property the constant exists for: prose
// is a value with a name, so a switch over kinds cannot mistake it for a kind that
// nobody set, and a stored `prose` is never ambiguous with a blank column.
func TestKindProseIsNotTheEmptyString(t *testing.T) {
	t.Parallel()

	if domain.KindProse.String() == "" {
		t.Error("KindProse.String() is empty; prose must be a named value")
	}

	if domain.PageKind("").String() != "" {
		t.Error("the zero PageKind is not the empty string; a kind nobody set must not " +
			"render as a name")
	}

	if domain.KindProse == domain.PageKind("") {
		t.Error("KindProse equals the zero PageKind")
	}
}

// TestPageKindStringIsIdentity asserts that String does not validate.
//
// The convention Role.String and Visibility.String set: an unrecognised value
// prints as the value that was read, which is what a log line needs, and a caller
// that needs to *know* the kind is understood asks the registry rather than
// believing a string.
func TestPageKindStringIsIdentity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		kind domain.PageKind
		want string
	}{
		{kind: domain.KindProse, want: "prose"},
		{kind: domain.PageKind("token"), want: "token"},
		{
			kind: domain.PageKind("something-this-build-has-never-heard-of"),
			want: "something-this-build-has-never-heard-of",
		},
		{kind: domain.PageKind(""), want: ""},
	}

	for _, testCase := range cases {
		if got := testCase.kind.String(); got != testCase.want {
			t.Errorf("PageKind(%q).String() = %q, want %q", testCase.want, got, testCase.want)
		}
	}
}
