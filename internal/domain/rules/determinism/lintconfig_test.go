package determinism_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain/rules/determinism"
)

// TestTheLintConfigurationIsTheTableTheAuditWalks is the consistency the task
// turns on: **the two enforcement layers must not become two answers to the same
// question.**
//
// The obvious way to get there is to write the `forbidigo` patterns in
// `.golangci.yml` and the forbidden selectors in Go, and then have a third place —
// this test — try to keep them in step by listing them. A list of three copies is a
// list with two chances to drift. So this repository does not write the patterns
// at all: `LintPatterns()` derives them from `ForbiddenRules()`, the same table
// `Audit` walks, and this test asserts the file is *exactly* what that function
// returns.
//
// Three assertions, each catching a different failure:
//
//   - set equality: the file is the derived list, so it is not a second answer.
//   - equal length as well as equal membership: a duplicated pattern would satisfy
//     set equality while hiding a missing one.
//   - every `KindCall` selector is *matched* by a configured pattern, compiled and
//     run. Set equality against a derivation could be satisfied by a derivation
//     that is wrong in both places at once; this one is not.
func TestTheLintConfigurationIsTheTableTheAuditWalks(t *testing.T) {
	t.Parallel()

	configured := lintConfig(t)

	want := determinism.LintPatterns()

	if len(configured) != len(want) {
		t.Fatalf(".golangci.yml configures %d forbidigo patterns, the table derives %d:\n"+
			" configured %v\n derived    %v",
			len(configured), len(want), configured, want)
	}

	for index, pattern := range want {
		if !slices.Contains(configured, pattern) {
			t.Errorf("no configured pattern is %q, which the table derives as entry %d of %v",
				pattern, index, want)
		}
	}

	// Coverage, run rather than assumed. `selectorFor` below is a second
	// implementation of the import-path-to-package-name rule on purpose: if it were
	// the package's own function then this assertion would be checking the
	// derivation against itself and could not fail for the reason that matters.
	compiled := make([]*regexp.Regexp, 0, len(configured))

	for _, pattern := range configured {
		expression, err := regexp.Compile(pattern)
		if err != nil {
			t.Fatalf("configured pattern %q does not compile: %v", pattern, err)
		}

		compiled = append(compiled, expression)
	}

	for _, entry := range determinism.ForbiddenRules() {
		if entry.Kind != determinism.KindCall {
			continue
		}

		for _, call := range entry.Calls {
			selector := selectorFor(entry.Import) + "." + call

			if !slices.ContainsFunc(compiled, func(expression *regexp.Regexp) bool {
				return expression.MatchString(selector)
			}) {
				t.Errorf("no configured forbidigo pattern matches %q, so a call to it in "+
					"rule code would be reported by the audit and not by the linter", selector)
			}
		}
	}
}

// TestTheLintBlindSpotsAreExactlyTheRangeRules closes the other direction.
//
// `LintBlindSpots` is how this package says "the linter does not cover these". A
// list that names less than the truth is a false claim, and one that names more is
// a linter rule that was quietly demoted. So it is asserted against the live
// table: exactly the `KindRange` entries, and nothing else.
func TestTheLintBlindSpotsAreExactlyTheRangeRules(t *testing.T) {
	t.Parallel()

	var wanted []string

	for _, entry := range determinism.ForbiddenRules() {
		if entry.Kind == determinism.KindRange {
			wanted = append(wanted, entry.Rule)
		}
	}

	if len(wanted) == 0 {
		t.Fatal("the table holds no `range` rule, so this assertion is vacuous: either the " +
			"map-iteration rules were removed or the Kind constant was renamed past it")
	}

	blind := determinism.LintBlindSpots()

	if !slices.Equal(blind, wanted) {
		t.Fatalf("LintBlindSpots() = %v, want %v -- exactly the rules no selector pattern "+
			"can express, so that the gap between the two layers is written down rather "+
			"than assumed", blind, wanted)
	}

	// And the claim itself, once: no configured pattern mentions a map at all, which
	// is the configuration half of "the linter cannot see this".
	for _, pattern := range lintConfigurationOf(t).patterns {
		if strings.Contains(pattern, "maps") {
			t.Errorf("pattern %q mentions `maps`, so the linter does reach the "+
				"`range/map-iterator` rule it is documented not to reach", pattern)
		}
	}
}

// TestTheLintScopeIsTheAuditScope is the other half of "one question, two layers",
// and the direction of the failure is the expensive one.
//
// `RuleDirs` is what `Audit` reads and `ScopePattern` is what `.golangci.yml`
// narrows `forbidigo` to. If the file named fewer directories than the audit walks,
// the build would still be green for a violation in the directory only the audit
// watches — no, worse: a violation the *linter* misses in a directory the *audit*
// covers is a green build with a real nondeterminism in it. So equality is required,
// not containment.
func TestTheLintScopeIsTheAuditScope(t *testing.T) {
	t.Parallel()

	config := lintConfigurationOf(t)

	got := config.pathExcept

	if len(got) != 1 {
		t.Fatalf(".golangci.yml has %d `path-except` rules for forbidigo, want 1: %v",
			len(got), got)
	}

	if got[0] != determinism.ScopePattern() {
		t.Errorf(".golangci.yml narrows forbidigo to %q, and the audit walks %v, which "+
			"derives %q", got[0], determinism.RuleDirs(), determinism.ScopePattern())
	}

	if !config.pathExceptCovers("forbidigo") {
		t.Error("the `path-except` rule does not name forbidigo among its linters, so it " +
			"narrows nothing and every forbidigo finding in the repository is excluded")
	}

	// The pattern is only worth anything if it actually matches the directories the
	// audit walks. Compiled and run, because a regex that matches nothing and a
	// regex that matches everything look identical in a string comparison.
	expression, err := regexp.Compile(determinism.ScopePattern())
	if err != nil {
		t.Fatalf("ScopePattern() does not compile: %v", err)
	}

	for _, dir := range determinism.RuleDirs() {
		candidate := dir + "/some_file.go"

		if !expression.MatchString(candidate) {
			t.Errorf("ScopePattern() does not match %q, so the linter would skip rule code "+
				"in a directory the audit reads", candidate)
		}

		if expression.MatchString("internal/store/store.go") {
			t.Error("ScopePattern() matches internal/store, so a clock call in the store " +
				"would be reported by a rule that is about rule code")
		}
	}
}

// TestRuleDirsNameTheDirectoriesThatHoldRuleCode pins the claim the scope rests on.
//
// Two directories, both named, and the second one does not exist yet. The claim is
// worth asserting rather than commenting because it is the reason a new rule
// package is covered without an edit: a scope that had to be amended to include a
// directory is a scope that would be amended after the violation, not before it.
func TestRuleDirsNameTheDirectoriesThatHoldRuleCode(t *testing.T) {
	t.Parallel()

	root, err := determinism.RepoRoot()
	if err != nil {
		t.Fatalf("RepoRoot() error = %v, want nil", err)
	}

	dirs := determinism.RuleDirs()

	if len(dirs) != 2 {
		t.Fatalf("RuleDirs() = %v, want exactly two directories", dirs)
	}

	// The one that exists today must exist, or the audit is watching a directory
	// with no rule code in it while `internal/domain/systems` gets the attention.
	first := filepath.Join(root, dirs[0], "rules.go")
	if _, err := os.Stat(first); err != nil {
		t.Errorf("RuleDirs()[0] = %q, which holds no rules.go: %v", dirs[0], err)
	}

	// The one that does not exist yet is a claim, and this asserts the claim is
	// *forward*-facing rather than a typo: the directory's absence is expected and
	// its name is what will be scanned when it lands.
	if dirs[1] != "internal/domain/systems" {
		t.Errorf("RuleDirs()[1] = %q, want internal/domain/systems", dirs[1])
	}
}

// lintConfiguration is the part of `.golangci.yml` this package's consistency
// depends on: the `forbidigo` patterns, and the `path-except` rules with the
// linters each of them names.
type lintConfiguration struct {
	patterns      []string
	pathExcept    []string
	pathExceptFor [][]string
}

// lintConfig returns the configured `forbidigo` patterns.
func lintConfig(t *testing.T) []string {
	t.Helper()

	return lintConfigurationOf(t).patterns
}

// lintConfiguration reads `.golangci.yml`.
//
// **Scans lines rather than parsing YAML**, and the reason is `go.mod`: the yaml
// library this repository already depends on is `gopkg.in/yaml.v3`, but
// `go.mod` is the phase integrator's file, and a work item that cannot reach it
// does not add to it for this. What is read is deliberately the smallest shape
// that can be read reliably — lines beginning `- pattern:`, lines beginning
// `- path-except:`, and the list items indented under each — so the scan has three
// cases rather than a grammar, and a future edit that changes the *style* of the
// file fails this test loudly rather than being silently misread.
func lintConfigurationOf(t *testing.T) lintConfiguration {
	t.Helper()

	root, err := determinism.RepoRoot()
	if err != nil {
		t.Fatalf("RepoRoot() error = %v, want nil", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, ".golangci.yml"))
	if err != nil {
		t.Fatalf("reading .golangci.yml: %v", err)
	}

	var (
		config     lintConfiguration
		patternAt  = regexp.MustCompile(`^\s*-\s*pattern:\s*'(.*)'\s*$`)
		pathExcept = regexp.MustCompile(`^(\s*)-\s*path-except:\s*'(.*)'\s*$`)
		listItem   = regexp.MustCompile(`^(\s*)-\s+(\S.*)$`)
	)

	lines := strings.Split(string(raw), "\n")

	for index, line := range lines {
		if match := patternAt.FindStringSubmatch(line); match != nil {
			config.patterns = append(config.patterns, match[1])

			continue
		}

		match := pathExcept.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		config.pathExcept = append(config.pathExcept, match[2])
		config.pathExceptFor = append(config.pathExceptFor, nil)

		// The linters a `path-except` rule names are the list items indented under
		// it, up to the next line at the rule's own indentation or shallower —
		// which is what makes the next rule's `- path:` line the boundary.
		for _, following := range lines[index+1:] {
			trimmed := strings.TrimLeft(following, " ")
			if trimmed == "" {
				continue
			}

			if len(following)-len(trimmed) <= len(match[1]) {
				break
			}

			item := listItem.FindStringSubmatch(following)
			if item == nil {
				continue
			}

			entry := len(config.pathExceptFor) - 1
			config.pathExceptFor[entry] = append(config.pathExceptFor[entry], item[2])
		}
	}

	return config
}

// pathExceptCovers reports whether a `path-except` rule names the given linter.
func (c lintConfiguration) pathExceptCovers(linter string) bool {
	for _, linters := range c.pathExceptFor {
		if slices.Contains(linters, linter) {
			return true
		}
	}

	return false
}

// selectorFor returns the name a selector expression spells for an import path.
//
// Written out here rather than imported, deliberately: this is the second
// implementation that makes the coverage assertion meaningful. If it were the
// package's own function, a derivation that got the package name wrong would be
// checked against itself and pass.
//
// The rule is short enough to state: the last path element, minus a `/vN` suffix,
// which is a module path element and never part of the package's name.
func selectorFor(importPath string) string {
	elements := strings.Split(importPath, "/")
	name := elements[len(elements)-1]

	if len(name) > 1 && name[0] == 'v' && strings.Trim(name[1:], "0123456789") == "" {
		return elements[len(elements)-2]
	}

	return name
}
