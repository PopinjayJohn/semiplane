// Package determinism enforces S-10.4 on rule code: no wall clock, no ambient
// randomness and no map iteration, because a resolution has to be a pure
// function of (state, intent, seed).
//
// # Why the rule exists
//
// Architecture §10.3 gives the reason in one sentence — `campaign_state`
// supports optimistic client apply, resume-by-version and replay, "all of which
// break if resolution is not a pure function of (state, intent, seed)" — and then
// says the enforcement is "a lint rule plus tests". This package is the tests
// half and the table both halves read.
// [0041]({{ "decisions/0041-the-rules-contract-returns-mutations-and-carries-its-own-rng/" | relURL }})
// settled the type-level half: `rules.Context` has no clock field and no handle,
// so nothing ambient is *reachable*; what is left is a plugin reaching for an
// ambient source by name, and only a name can be noticed.
//
// # Two layers, one table
//
// There is no sandbox — these are compiled-in packages running in-process with
// the server's authority — so the rule is a discipline, and a discipline needs two
// teeth. "A gate that can be skipped by not running a command is a gate nobody
// runs", and this is the sentence `AGENTS.md` uses for an accessibility gate that
// read a built artefact:
//
//   - **`forbidigo`, configured in `.golangci.yml`.** It sees selectors, it runs
//     for every contributor whether or not they read this file, and it points at
//     the exact expression.
//   - **`Audit`, in this package.** It sees imports, references that are never
//     called, and every `range` — and it runs under `go test`.
//
// They are kept from becoming two answers to one question **by construction, not
// by discipline**: `LintPatterns` derives the linter's configuration from
// `ForbiddenRules`, and a test refuses a `.golangci.yml` whose `forbidigo` list is
// not exactly what `LintPatterns` returns. Edit the table and the two layers
// cannot drift apart, because the second is computed from the first.
//
// The two are not the same strength, and claiming they are would be the kind of
// assertion this repository keeps getting wrong. The audit is the superset; the
// rules no selector pattern can express are listed by `LintBlindSpots` and asserted
// by a test, so the gap is written down rather than discovered.
//
// # Scope
//
// `RuleDirs` names the directories that hold rule code — `internal/domain/rules`
// and `internal/domain/systems`. The second does not exist yet; naming it costs
// nothing and makes the gate pick a new package up the moment it lands, with no
// edit here and no window in which rule code is unaudited. `ScopePattern` is the
// expression `.golangci.yml` narrows `forbidigo` to, derived from `RuleDirs` for
// the same reason: an audit scoped to one set of directories and a linter scoped
// to another is two answers.
//
// # The other half
//
// `Scope` is this discipline at the granularity a house-rule module is declared
// at. S-10.5 refuses a module that reorders resolution or introduces
// nondeterminism, and `Scope.Admissible` is what turns that from a sentence in a
// review comment into a check a registry runs.

package determinism

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Kind is which shape of construct a Forbidden entry is about.
type Kind string

const (
	// KindImport is an import path rule code may not import at all. The stronger
	// of the two call-shaped rules, because a package that is imported and never
	// called is still a dependency the next contributor reaches for.
	KindImport Kind = "import"

	// KindCall is a selector rule code may not reference — called, or merely
	// assigned, because `x := time.Now` is the same hole one step before it is
	// used.
	KindCall Kind = "call"

	// KindRange is a range clause rule code may not write. Nothing in this project
	// forbids a `for` statement; the object being ranged is the rule.
	KindRange Kind = "range"
)

// Forbidden is one thing rule code may not do, and why.
//
// One entry per (kind, package) rather than one per selector, so that a package
// with nine forbidden clock functions carries one reason rather than nine copies
// of it — and so that `LintPatterns` can turn the `Calls` of every entry into the
// linter's configuration without a second table to keep in step.
type Forbidden struct {
	// Rule is the stable short name a report prints and a test matches on:
	// `import/time`, `call/time`, `range/map`. Not derived from the fields,
	// because a derived name changes when the package is renamed and a report an
	// operator greps for should not.
	Rule string

	// Kind is which shape of construct this entry is about.
	Kind Kind

	// Import is the package, as the import path rule code writes. Empty for a rule
	// with no package behind it — `range/map` has none.
	Import string

	// Calls are the selectors within that package. Empty for `KindImport`, which
	// is about the package rather than anything in it.
	Calls []string

	// Reason is what a refusal reports, written for whoever wrote the offending
	// line: the useful half of the message is what to reach for instead.
	Reason string
}

// forbiddenRules is the whole of S-10.4 as this project enforces it.
//
// A package-level variable rather than a function result because it is a table
// and reads as one; it is exported through `ForbiddenRules`, and only `LintPatterns`
// and `Audit` read it here.
var forbiddenRules = []Forbidden{
	{
		Rule:   "import/time",
		Kind:   KindImport,
		Import: "time",
		Reason: "a resolution must not be able to read a wall clock, and importing `time` " +
			"is how it gets one",
	},
	{
		Rule:   "call/time",
		Kind:   KindCall,
		Import: "time",
		Calls: []string{
			"After", "AfterFunc", "NewTimer", "NewTicker", "Now", "Since", "Sleep", "Tick", "Until",
		},
		Reason: "each of these puts the wall clock, or the passage of it, where a result " +
			"can depend on it",
	},
	{
		Rule:   "import/crypto-rand",
		Kind:   KindImport,
		Import: "crypto/rand",
		Reason: "ambient randomness is not reproducible, so a roll drawn from it cannot be " +
			"audited and a replay does not replay it",
	},
	{
		Rule:   "call/crypto-rand",
		Kind:   KindCall,
		Import: "crypto/rand",
		Calls:  []string{"Int", "Prime", "Read", "Text"},
		Reason: "ambient randomness is not reproducible; rules.Context.Rand derives every " +
			"number a resolution draws",
	},
	{
		Rule:   "import/math-rand",
		Kind:   KindImport,
		Import: "math/rand",
		Reason: "the package-level source is seeded from the OS at startup, so two processes " +
			"disagree and neither can be reproduced from a campaign's seed",
	},
	{
		Rule:   "call/math-rand",
		Kind:   KindCall,
		Import: "math/rand",
		Calls: []string{
			"ExpFloat64", "Float32", "Float64", "Int", "Int31", "Int31n", "Int63", "Int63n",
			"Intn", "NormFloat64", "Perm", "Read", "Seed", "Shuffle", "Uint32", "Uint64",
		},
		Reason: "these draw from the process-global source rather than from the resolution's " +
			"seed; rand.New and rand.NewPCG construct a seeded source and are not listed",
	},
	{
		Rule:   "call/math-rand-v2",
		Kind:   KindCall,
		Import: "math/rand/v2",
		Calls: []string{
			"ExpFloat64", "Float32", "Float64", "Int32N", "Int64N", "IntN", "N", "NormFloat64",
			"Perm", "Seed", "Shuffle", "Uint32", "Uint32N", "Uint64", "Uint64N",
		},
		Reason: "math/rand/v2's package-level functions read the runtime's global source, which " +
			"is seeded per process; rules.Context.Rand returns a rand.New(rand.NewPCG(...)) " +
			"derived from the campaign's recorded seed, and none of those constructors is listed",
	},
	// `math/rand/v2` is absent from the import rules above on purpose, and its
	// absence is a decision rather than an oversight: it is the one `rand` package
	// rule code may import, because `rules.Context.Rand` has to construct a seeded
	// source from somewhere and S-10.4 forbids reaching `crypto/rand` *directly*
	// rather than forbidding randomness. What is forbidden is this entry's list —
	// the package-level functions — and the constructors are not on it.
	// `TestTheSeededDerivationIsNotFlagged` is what holds that boundary, because a
	// determinism rule that fires on the one correct implementation of randomness is
	// a rule the next contributor disables wholesale.
	{
		Rule: "range/map",
		Kind: KindRange,
		Reason: "Go randomises map iteration, so a resolution that ranges over a map resolves " +
			"the same intent differently on a second run, which is what S-14.6 asserts it " +
			"cannot do. Sort first: slices.Sorted(maps.Keys(m)) is the sanctioned form and is " +
			"not flagged, because it is a range over a slice",
	},
	{
		Rule:   "range/map-iterator",
		Kind:   KindRange,
		Import: "maps",
		Calls:  []string{"All", "Keys", "Values"},
		Reason: "these return an iterator over a map, so ranging one is ranging a map in three " +
			"more spellings; slices.Sorted or slices.Collect over them is the sanctioned form",
	},
}

// ForbiddenRules returns S-10.4 as this project enforces it.
//
// A function returning the table, so the exported surface is documented in one
// place and a caller cannot re-order what three other pieces of this package read.
func ForbiddenRules() []Forbidden {
	return forbiddenRules
}

// LintBlindSpots are the rules no selector pattern can express.
//
// Exported, and asserted by `TestTheLintConfigurationIsTheTableTheAuditWalks`,
// because "the two layers refuse the same constructs" is only true if the places
// they differ are written down. There are two, and they are the two `range` rules:
// ranging over a map is not an identifier, and a pattern that forbade
// `maps.Keys` fires on `slices.Sorted(maps.Keys(m))` as well — the *sanctioned*
// spelling, which is the failure mode this project already paid for once with
// `Context.Rand`. Measured, not assumed: that pattern was written, watched fire
// on the correct implementation, and removed. See the comment in
// `.golangci.yml`.
//
// What the linter can reach that the audit cannot: nothing.
func LintBlindSpots() []string {
	return []string{"range/map", "range/map-iterator"}
}

// LintPatterns returns the regular expressions `.golangci.yml` configures
// `forbidigo` with.
//
// **Derived from `forbiddenRules` and never written out.** Every `KindCall` entry
// contributes its selectors to the pattern for that package's *name*, because a
// name is what a selector expression spells — `crypto/rand`, `math/rand` and
// `math/rand/v2` are three import paths and one package name, `rand`. Two of them
// are the same package as far as the linter is concerned, and pretending otherwise
// would be three patterns with three chances to disagree.
//
// `KindRange` entries are skipped, and that is a measurement rather than a
// limitation: a `range` has no selector, and the one range rule that does name a
// package (`maps.Keys`) cannot be expressed as a call pattern without also
// forbidding the sorted form. `LintBlindSpots` is what it is called instead.
//
// Deterministic by construction: entries are visited in order, groups keep the
// order their package was first listed in, and the selectors inside a group are
// sorted and de-duplicated — so the same table always yields the same patterns, in
// the same order, and an operator can read a pattern in `.golangci.yml` and find
// the table entry it came from without reading the derivation.
func LintPatterns() []string {
	var (
		names     []string
		collected [][]string
	)

	for _, entry := range forbiddenRules {
		if entry.Kind != KindCall || len(entry.Calls) == 0 {
			continue
		}

		name := packageName(entry.Import)

		group := indexOf(names, name)
		if group < 0 {
			group = len(names)
			names = append(names, name)
			collected = append(collected, nil)
		}

		collected[group] = append(collected[group], entry.Calls...)
	}

	patterns := make([]string, 0, len(names))

	for group, name := range names {
		patterns = append(
			patterns,
			"^"+regexp.QuoteMeta(
				name,
			)+"\\.("+strings.Join(
				selectorAlternation(collected[group]),
				"|",
			)+")$",
		)
	}

	return patterns
}

// selectorAlternation returns the sorted, de-duplicated, escaped selectors of one
// group as the pieces of a regex alternation.
//
// Sorted so the pattern is readable and so that two runs cannot differ; sorted
// before de-duplicated, so the de-duplication is adjacency and the result is
// sorted, which is one pass rather than a set.
func selectorAlternation(selectors []string) []string {
	if len(selectors) == 0 {
		return nil
	}

	sorted := slices.Sorted(slices.Values(selectors))

	unique := make([]string, 0, len(sorted))
	for _, selector := range sorted {
		if len(unique) > 0 && unique[len(unique)-1] == selector {
			continue
		}

		unique = append(unique, regexp.QuoteMeta(selector))
	}

	return unique
}

// packageName returns the name a selector expression spells for an import path:
// the last element, without a major-version suffix.
//
// `math/rand/v2` names a package called `rand`, and `crypto/rand` and `math/rand`
// name the same one.
func packageName(importPath string) string {
	name := path.Base(importPath)

	if len(name) > 1 && name[0] == 'v' && isDigits(name[1:]) {
		name = path.Base(path.Dir(importPath))
	}

	return name
}

// isDigits reports whether text is one or more ASCII digits.
func isDigits(text string) bool {
	for _, char := range text {
		if char < '0' || char > '9' {
			return false
		}
	}

	return text != ""
}

// indexOf returns the position of name in names, or -1 when it is absent.
func indexOf(names []string, name string) int {
	for position, listed := range names {
		if listed == name {
			return position
		}
	}

	return -1
}

// RuleDirs returns the directories that hold rule code, relative to the
// repository root, in the order the audit walks them.
//
// **`internal/domain/systems` does not exist yet.** It is named anyway, for the
// reason `A11Y_ROUTE_PKGS` is a `$(wildcard …)`: a scope that has to be edited to
// cover a new package is a scope that will not be edited, and the window between a
// package landing and somebody remembering it is a window in which rule code is
// unaudited. `Audit` reports a directory that is absent rather than refusing to
// run, and fails if *no* directory is there — so a rename cannot leave the audit
// passing over nothing.
func RuleDirs() []string {
	return []string{"internal/domain/rules", "internal/domain/systems"}
}

// ScopePattern returns the regular expression `.golangci.yml` narrows `forbidigo`
// to.
//
// Derived from `RuleDirs`, for the reason `LintPatterns` is derived from the table:
// two hand-written lists of the same thing is one more thing to forget, and the
// failure is silent in the worst direction — an audit that watches directories the
// linter does not.
func ScopePattern() string {
	alternatives := make([]string, 0, len(RuleDirs()))
	for _, dir := range RuleDirs() {
		alternatives = append(alternatives, regexp.QuoteMeta(dir)+"/")
	}

	return "^(" + strings.Join(alternatives, "|") + ")"
}

// Violation is one determinism rule one file breaks.
//
// Fields rather than a formatted string because a caller may want to group by
// rule, and an audit that can only report prose cannot.
type Violation struct {
	// Path is the file relative to the repository root, so it is the path an
	// operator can open.
	Path string

	// Line is the 1-based line the construct is on.
	Line int

	// Rule is the `Forbidden.Rule` that was broken, so a caller names the rule
	// rather than matching this package's prose.
	Rule string

	// Detail is the construct: the selector as written, the import path, or the
	// type that was ranged over.
	Detail string
}

// String renders one violation as one line: path, line, rule, construct.
func (v Violation) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", v.Path, v.Line, v.Rule, v.Detail)
}
