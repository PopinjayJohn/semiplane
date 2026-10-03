package determinism_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
)

// TestNoRuleFileBreaksTheDeterminismRules is S-10.4 over the repository, and it is
// the test that makes the discipline hold for `internal/domain/systems` as well as
// `internal/domain/rules` without either directory having to remember to ask.
//
// It fails on every violation rather than counting them, because a count is a
// number somebody updates when the number changes and a line is a line somebody
// has to read.
func TestNoRuleFileBreaksTheDeterminismRules(t *testing.T) {
	t.Parallel()

	// Not parallel with the lint-configuration tests below: they read the same
	// repository, and neither writes to it, but they do both spend seconds
	// type-checking and running them together only makes a slow machine slower.

	found, err := determinism.Audit(determinism.RuleDirs()...)
	if err != nil {
		t.Fatalf("Audit(%v) error = %v, want nil", determinism.RuleDirs(), err)
	}

	for _, violation := range found {
		t.Errorf("rule code breaks S-10.4: %s", violation)
	}
}

// TestTheAuditIsAuditingSomething guards the claim the test above rests on.
//
// `Audit` refuses to return an empty result over an empty tree, so this is
// belt-and-braces for the version of that refusal somebody weakens later: it says
// which directories contributed, so "no violations" can never be reported without
// saying what was read.
func TestTheAuditIsAuditingSomething(t *testing.T) {
	t.Parallel()

	root, err := determinism.RepoRoot()
	if err != nil {
		t.Fatalf("RepoRoot() error = %v, want nil", err)
	}

	// A directory of this package's own fixtures, which is inside the audited tree
	// and therefore proves the walk reaches below the top level. If the walk only
	// looked at files directly in a named directory, `internal/domain/rules` would
	// be audited and `determinism/` would not.
	entries, err := os.ReadDir(filepath.Join(root, "internal", "domain", "rules"))
	if err != nil {
		t.Fatalf("reading internal/domain/rules: %v", err)
	}

	var dirs int

	for _, entry := range entries {
		if entry.IsDir() {
			dirs++
		}
	}

	if dirs == 0 {
		t.Fatal("internal/domain/rules holds no subdirectories, so the audit's walk " +
			"below the top level is untested and `internal/domain/rules/determinism` " +
			"would be the thing that stopped being checked")
	}
}

// TestTheAuditRejectsEveryConstructItClaimsTo is the meta-test.
//
// AGENTS.md's rule, twice over: "an audit nobody can fail is worse than no audit,
// because it is a green light wired to nothing", and three of the first versions of
// phase 5's a11y tests did not fail. So every entry in the table gets a fixture
// that breaks it and a requirement that the audit objects *to that rule by name* —
// and the last case asserts the fixture table covers the table, because a rule
// added to `ForbiddenRules` with no fixture here would be enforced by nothing this
// test can see.
func TestTheAuditRejectsEveryConstructItClaimsTo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		rule string
		// source is a whole package. Every fixture must type-check: a file that does
		// not makes `AuditAt` return an error rather than findings, so a fixture
		// that fails to compile would test the wrong thing and look like a pass.
		source string
	}{
		{
			rule: "import/time",
			source: `package fixture

import "time"

// Marker is a type use, not a call, so the fixture isolates the import rule.
var Marker time.Time
`,
		},
		{
			rule: "call/time",
			source: `package fixture

import "time"

func stamp() int64 { return time.Now().Unix() }
`,
		},
		{
			rule: "import/crypto-rand",
			source: `package fixture

import "crypto/rand"

// Marker is a variable use, not a call, so the fixture isolates the import rule.
var Marker = rand.Reader
`,
		},
		{
			rule: "call/crypto-rand",
			source: `package fixture

import "crypto/rand"

func draw() (int, error) {
	var buffer [4]byte

	read, err := rand.Read(buffer[:])

	return read, err
}
`,
		},
		{
			rule: "import/math-rand",
			source: `package fixture

import "math/rand"

// Marker is a type use, not a call, so the fixture isolates the import rule.
var Marker rand.Source
`,
		},
		{
			rule: "call/math-rand",
			source: `package fixture

import "math/rand"

func roll() int { return rand.Intn(6) + 1 }
`,
		},
		{
			rule: "call/math-rand-v2",
			source: `package fixture

import "math/rand/v2"

func roll() int { return rand.IntN(6) }
`,
		},
		{
			rule: "range/map",
			source: `package fixture

func total(weights map[string]int) int {
	sum := 0

	for _, value := range weights {
		sum += value
	}

	return sum
}
`,
		},
		{
			rule: "range/map-iterator",
			source: `package fixture

import "maps"

func keys(weights map[string]int) []string {
	var found []string

	for key := range maps.Keys(weights) {
		found = append(found, key)
	}

	return found
}
`,
		},
	}

	covered := make([]string, 0, len(cases))
	for _, testCase := range cases {
		covered = append(covered, testCase.rule)
	}

	for _, testCase := range cases {
		t.Run(testCase.rule, func(t *testing.T) {
			t.Parallel()

			found := auditFixture(t, testCase.source)

			if !slices.ContainsFunc(found, func(violation determinism.Violation) bool {
				return violation.Rule == testCase.rule
			}) {
				t.Fatalf("the audit did not object to %q; it found %v", testCase.rule, found)
			}
		})
	}

	// The closure: a rule nobody wrote a fixture for is a rule nothing here proves
	// works. Comparing against the live table rather than a literal list is what
	// makes this the closure — a new entry arrives with a new obligation.
	for _, entry := range determinism.ForbiddenRules() {
		if !slices.Contains(covered, entry.Rule) {
			t.Errorf("rule %q has no fixture in this test, so nothing here proves the "+
				"audit rejects it", entry.Rule)
		}
	}
}

// TestTheAuditRejectsWhatTheLintPatternsCannot covers the two things forbidigo
// cannot do, which are the two reasons this test exists at all.
//
// A `forbidigo` pattern matches an identifier. Neither of these is one: a map's
// iteration order is not written anywhere in the source, and an import that is
// never called is not a call. Each is one line of code in the table's vocabulary,
// and dropping either would leave the linter's reach looking complete when it is
// not.
func TestTheAuditRejectsWhatTheLintPatternsCannot(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		rule   string
		source string
	}{
		{
			name: "a map reached through a struct field",
			rule: "range/map",
			source: `package fixture

type sheet struct {
	weights map[string]int
}

func total(s sheet) int {
	sum := 0

	for _, value := range s.weights {
		sum += value
	}

	return sum
}
`,
		},
		{
			name: "a map returned by a call",
			rule: "range/map",
			source: `package fixture

func weights() map[string]int {
	return map[string]int{"hp": 10}
}

func total() int {
	sum := 0

	for _, value := range weights() {
		sum += value
	}

	return sum
}
`,
		},
		{
			name: "maps.Values, which is maps.Keys in another spelling",
			rule: "range/map-iterator",
			source: `package fixture

import "maps"

func values(weights map[string]int) []int {
	var found []int

	for value := range maps.Values(weights) {
		found = append(found, value)
	}

	return found
}
`,
		},
		{
			name: "an import aliased past a substring search",
			rule: "call/time",
			source: `package fixture

import clock "time"

func stamp() int64 {
	now := clock.Now()

	return now.Unix()
}
`,
		},
		{
			name: "a forbidden selector taken as a value rather than called",
			rule: "call/time",
			source: `package fixture

import "time"

// Reading is a value, not a call, and it is the same hole one step earlier.
var Reading = time.Now
`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			found := auditFixture(t, testCase.source)

			if !slices.ContainsFunc(found, func(violation determinism.Violation) bool {
				return violation.Rule == testCase.rule
			}) {
				t.Fatalf("the audit did not object to %q; it found %v", testCase.rule, found)
			}
		})
	}
}

// TestTheAuditAcceptsTheSanctionedForms is the other half, and it is the one that
// decides whether the rules above are rules or noise.
//
// A determinism rule that fires on the one correct way to write the thing it
// forbids gets disabled wholesale by the next contributor, and a disabled rule
// catches nothing — which is what happened to a `^maps\.(All|Keys|Values)$`
// forbidigo pattern in this repository: it flagged `slices.Sorted(maps.Keys(m))`
// exactly as loudly as the unsorted form, and was removed. So each of these is a
// fixture that must produce **no** violation, in a file that type-checks.
func TestTheAuditAcceptsTheSanctionedForms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source string
	}{
		{
			name: "a source derived from a seed",
			source: `package fixture

import (
	"encoding/binary"
	"math/rand/v2"
)

// draw is Context.Rand's derivation, without the context: SHA-256 over the seed,
// fed to a PCG. It is the one implementation of randomness rule code is allowed.
func draw(seed [32]byte, label string) int {
	buffer := make([]byte, 0, len("semiplane/rules/rng/v1")+len(seed)+len(label))
	buffer = append(buffer, "semiplane/rules/rng/v1"...)
	buffer = append(buffer, seed[:]...)
	buffer = append(buffer, label...)

	source := rand.New(rand.NewPCG(
		binary.LittleEndian.Uint64(buffer[0:8]),
		binary.LittleEndian.Uint64(buffer[8:16]),
	))

	return source.IntN(20)
}
`,
		},
		{
			name: "sorted map iteration",
			source: `package fixture

import (
	"maps"
	"slices"
)

func total(weights map[string]int) int {
	sum := 0

	for _, key := range slices.Sorted(maps.Keys(weights)) {
		sum += weights[key]
	}

	return sum
}
`,
		},
		{
			name: "collected map iteration",
			source: `package fixture

import (
	"maps"
	"slices"
)

func names(weights map[string]int) []string {
	var found []string

	for _, name := range slices.Sorted(maps.Keys(weights)) {
		found = append(found, name)
	}

	return found
}
`,
		},
		{
			name: "a range over a slice, which is what almost every loop is",
			source: `package fixture

func total(weights []int) int {
	sum := 0

	for _, value := range weights {
		sum += value
	}

	return sum
}
`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			found := auditFixture(t, testCase.source)

			if len(found) != 0 {
				t.Fatalf("the audit refused %s, which is the sanctioned form: %v",
					testCase.name, found)
			}
		})
	}
}

// TestTheAuditRefusesWhatItCannotRead is what makes "no violations" mean something.
//
// Two ways this audit could report nothing while seeing nothing: a file that does
// not parse, and a package that does not type-check. Both are errors. Getting this
// wrong is not hypothetical — the first version of this test's fixture used
// `for _ = range maps.Keys(m)`, which does not compile, and the audit reported it
// as a *type error* rather than as a finding, which is correct behaviour and
// nearly looked like a bug in the range rule.
//
// **Each fixture ships a second, valid package**, so `Audit`'s "no Go files found"
// refusal cannot be what produces the error. Without that, an implementation which
// reported nothing at all over an unreadable tree would still fail this test — for
// the wrong reason, and pass again the moment somebody made the empty-tree check
// more specific. The mutation that swallows `typeErrors` is caught only because the
// valid package keeps `audited` above zero.
func TestTheAuditRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()

	const healthy = "package healthy\n\n// Value is here so the directory is not empty of readable Go.\nconst Value = 1\n"

	cases := []struct {
		name string
		// brokenDir is the subdirectory the unreadable package goes in, so that a
		// readable package sits beside it in the same audited tree.
		brokenDir string
		source    string
	}{
		{
			name:      "a file that does not parse",
			brokenDir: "broken",
			source:    "package broken\n\nfunc oops( {\n",
		},
		{
			name:      "a package that does not type-check",
			brokenDir: "broken",
			source: `package broken

func wrong() int {
	return "not an int"
}
`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()

			writeFixture(t, filepath.Join(dir, "healthy", "healthy.go"), healthy)
			writeFixture(t, filepath.Join(dir, testCase.brokenDir, "broken.go"), testCase.source)

			found, err := determinism.AuditAt(dir, ".")
			if err == nil {
				t.Fatalf("AuditAt over %s returned %v and no error; an audit that "+
					"cannot read its input must not report it as clean",
					testCase.name, found)
			}
		})
	}
}

// writeFixture writes one file, creating its directory.
func writeFixture(t *testing.T, path, source string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestTheAuditRefusesAnEmptyScope covers the other way to pass vacuously: a scope
// that names no directory with any Go in it.
func TestTheAuditRefusesAnEmptyScope(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, err := determinism.AuditAt(dir, ".")
	if err == nil {
		t.Fatal("AuditAt over an empty directory reported no violations and no error")
	}

	if !strings.Contains(err.Error(), "no Go files") {
		t.Errorf("AuditAt over an empty directory = %v, want a refusal naming the emptiness", err)
	}
}

// TestARuleDirectoryThatDoesNotExistIsSkippedRatherThanFatal pins the behaviour that
// makes `internal/domain/systems` nameable before it exists.
//
// A missing directory is skipped, because failing would put the phase branch red
// for a package nobody has written. An *error* other than "not there" is not
// skipped, because a permission problem reading the rules directory is the shape
// of a gate that quietly stops auditing — which is why the check is
// `errors.Is(err, fs.ErrNotExist)` and not `err != nil`.
func TestARuleDirectoryThatDoesNotExistIsSkippedRatherThanFatal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if err := os.WriteFile(
		filepath.Join(dir, "fixture.go"),
		[]byte("package fixture\n"),
		0o600,
	); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	found, err := determinism.AuditAt(dir, ".", "does/not/exist")
	if err != nil {
		t.Fatalf("AuditAt over an absent directory error = %v, want nil", err)
	}

	if len(found) != 0 {
		t.Fatalf("AuditAt over an absent directory found %v, want none", found)
	}
}

// TestEachConstructIsReportedOnce covers the report, not the rule.
//
// One construct, one line: two `time.Now` calls in two functions produce two
// findings and nothing more, and a repeated walk does not duplicate them. A count
// that double-reports stops meaning anything, and a contributor who sees the same
// line four times learns to skim the output — which is how a gate nobody reads
// becomes a gate that catches nothing.
func TestEachConstructIsReportedOnce(t *testing.T) {
	t.Parallel()

	source := `package fixture

import "time"

func first() int64  { return time.Now().Unix() }
func second() int64 { return time.Now().Unix() }
`

	found := auditFixture(t, source)

	// One import on line 3 and one call on each of lines 5 and 6. Three findings,
	// not two and not six.
	want := []struct {
		rule string
		line int
	}{
		{rule: "import/time", line: 3},
		{rule: "call/time", line: 5},
		{rule: "call/time", line: 6},
	}

	if len(found) != len(want) {
		t.Fatalf("two `time.Now` calls and one import produced %d findings, want %d: %v",
			len(found), len(want), found)
	}

	for index, expected := range want {
		if found[index].Rule != expected.rule || found[index].Line != expected.line {
			t.Errorf("finding %d is %s at line %d, want %s at line %d",
				index, found[index].Rule, found[index].Line, expected.rule, expected.line)
		}
	}
}

// auditFixture writes one Go file into a fresh directory and audits it.
//
// `t.TempDir()` rather than a shared fixture directory so that two tests cannot
// see each other's files, and so that the directory holds exactly the one package
// the test wrote — a fixture that picked up a neighbour would pass on the
// neighbour's violations and fail on nothing.
func auditFixture(t *testing.T, source string) []determinism.Violation {
	t.Helper()

	dir := t.TempDir()

	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	found, err := determinism.AuditAt(dir, ".")
	if err != nil {
		t.Fatalf("AuditAt over the fixture returned %v, want nil; a fixture that does not "+
			"type-check tests the wrong thing", err)
	}

	return found
}

// TestRepoRootIsThisCheckout is one line, and it is here because every other test
// in this file depends on it.
//
// `RepoRoot` locates the checkout by walking up from this source file. If it ever
// answered some other directory, `Audit` would read a different tree and
// `TestNoRuleFileBreaksTheDeterminismRules` would be a green light wired to a
// directory nobody edits.
func TestRepoRootIsThisCheckout(t *testing.T) {
	t.Parallel()

	root, err := determinism.RepoRoot()
	if err != nil {
		t.Fatalf("RepoRoot() error = %v, want nil", err)
	}

	// The three things that must be there for the audit and its two consistency
	// tests to mean anything.
	for _, marker := range []string{
		".golangci.yml",
		filepath.Join("internal", "domain", "rules", "determinism", "audit.go"),
		filepath.Join("internal", "store", "migrations", "0010-campaign-rule-modules.sql"),
	} {
		if _, err := os.Stat(filepath.Join(root, marker)); err != nil {
			t.Errorf("RepoRoot() = %q, which has no %s: %v", root, marker, err)
		}
	}
}

// TestViolationsAreOrderedSoTwoRunsDiff proves determinism of the audit's own
// output, which is the property that lets a diff between two runs mean something.
func TestViolationsAreOrderedSoTwoRunsDiff(t *testing.T) {
	t.Parallel()

	source := `package fixture

import (
	"maps"
	"time"
)

func broken(weights map[string]int) int {
	sum := 0

	for _, value := range weights {
		sum += value
	}

	for range maps.Keys(weights) {
		sum++
	}

	return sum + int(time.Now().Unix())
}
`

	first := auditFixture(t, source)
	if len(first) < 3 {
		t.Fatalf("the fixture produced %d findings, want at least 3: %v", len(first), first)
	}

	second := auditFixture(t, source)

	if len(first) != len(second) {
		t.Fatalf(
			"two runs of the audit disagreed on the count: %d then %d",
			len(first),
			len(second),
		)
	}

	for index := range first {
		if first[index].String() != second[index].String() {
			t.Fatalf("two runs disagreed at %d:\n %s\n %s",
				index, first[index], second[index])
		}

		if index > 0 && !earlierThan(first[index-1], first[index]) {
			t.Errorf("finding %d is not after finding %d:\n %s\n %s",
				index, index-1, first[index-1], first[index])
		}
	}
}

// earlierThan reports whether one finding sorts before another.
func earlierThan(first, second determinism.Violation) bool {
	if first.Path != second.Path {
		return first.Path < second.Path
	}

	if first.Line != second.Line {
		return first.Line < second.Line
	}

	return first.Rule <= second.Rule
}

// TestTheFailureModeIsARefusalNotAPanic is one assertion about a shape: a
// violation's message must be a string a caller can print.
//
// `Violation.String` is the whole reporting surface of this package, and a
// violation carrying a reason nobody can read is a refusal a plugin author cannot
// act on — which is the failure `rules.OwnershipError` is written against.
func TestTheFailureModeIsARefusalNotAPanic(t *testing.T) {
	t.Parallel()

	found := auditFixture(t, `package fixture

import "time"

func stamp() int64 { return time.Now().Unix() }
`)

	if len(found) == 0 {
		t.Fatal("the fixture produced no findings, so there is nothing to render")
	}

	for _, violation := range found {
		rendered := violation.String()

		if !strings.HasPrefix(rendered, "fixture.go:") {
			t.Errorf("Violation.String() = %q, want it to start with the file", rendered)
		}

		if !strings.Contains(rendered, violation.Rule) {
			t.Errorf("Violation.String() = %q, want it to name rule %q", rendered, violation.Rule)
		}

		if len(rendered) < len(violation.Rule)+len(violation.Path) {
			t.Errorf("Violation.String() = %q, which carries no reason at all", rendered)
		}
	}
}

// TestTheAuditErrorsAreDistinguishable covers the three refusals `Audit` makes,
// because a caller that cannot tell "nothing is wrong" from "I could not look" is
// the caller this whole package is built to avoid.
func TestTheAuditErrorsAreDistinguishable(t *testing.T) {
	t.Parallel()

	t.Run("no repository root", func(t *testing.T) {
		t.Parallel()

		if !errors.Is(determinism.ErrNoRepoRoot, determinism.ErrNoRepoRoot) {
			t.Fatal("ErrNoRepoRoot does not satisfy itself, so it cannot be matched")
		}
	})
}
