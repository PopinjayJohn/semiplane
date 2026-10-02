package plugin_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealtimeDoesNotImportThisPackage is the claim `hub.go`'s `Resolver` doc comment
// makes — "declared here rather than imported from a systems package so that
// `internal/realtime` does not depend on `internal/plugin`" — read off the source.
//
// **A comment is not a dependency rule.** The interface was declared in `realtime`
// for this reason, and the reason is only still true while nothing imports this
// package from there; a later phase adding one import would turn the seam into a
// cycle the compiler refuses, which is the failure this test converts into a message
// that says which package broke it and why it matters.
//
// Parsed imports rather than a substring search, for the reason
// `rules`' own audit gives: an import can be aliased, grouped and commented in ways a
// search has to guess about, and `plugin` appears in a comment right here.
func TestRealtimeDoesNotImportThisPackage(t *testing.T) {
	t.Parallel()

	const forbidden = "github.com/semiplane/semiplane/internal/plugin"

	found := false

	for _, imports := range parsedPackage(t, "../realtime") {
		for _, imported := range imports {
			if imported == forbidden {
				found = true
			}
		}
	}

	if found {
		t.Errorf("internal/realtime imports %q; the dependency runs the other way, "+
			"and the Resolver interface is declared in realtime precisely so that it does",
			forbidden)
	}
}

// TestThisPackageImportsRealtime is the other half of the same rule, and it is a test
// rather than a comment because the direction is the package's reason to exist: the
// adapter implements `realtime.Resolver` and reads `realtime.CampaignState`, which is
// only possible while `internal/realtime` imports nothing from here.
func TestThisPackageImportsRealtime(t *testing.T) {
	t.Parallel()

	const wanted = "github.com/semiplane/semiplane/internal/realtime"

	found := false

	for _, imports := range parsedPackage(t, ".") {
		for _, imported := range imports {
			if imported == wanted {
				found = true
			}
		}
	}

	if !found {
		t.Errorf("nothing imports %q any more: the adapter is the whole of this "+
			"package's reason to exist, and it is what implements realtime.Resolver", wanted)
	}
}

// TestNoFileInThisPackageHasAnInit is S-10.1 and ADR 0011 as an executable rule.
//
// `init()` in a package that registers plugins is the thing both documents refuse: it
// hides the order packs compose in, and it registers into every test binary that
// imports the package, so two tests cannot disagree about what is registered. The
// refusal is easy to write down and easy to lose in a refactor, so it is parsed for.
//
// `var` initialisers are **not** searched, and the distinction is the point: a
// package-level value is fine as long as it is not a registration, and this test
// cannot tell the two apart — a registration in a package-level map would pass here.
// What it catches is the ordering hazard, which is the one `init()` creates.
func TestNoFileInThisPackageHasAnInit(t *testing.T) {
	t.Parallel()

	for _, name := range goFiles(t, ".") {
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		for _, declaration := range parsed.Decls {
			function, isFunction := declaration.(*ast.FuncDecl)

			// A method, not a package initialiser: `init` is a function and never a
			// method, so the receiver check is what stops `func (x T) init()` from
			// being reported as one.
			if !isFunction || function.Recv != nil || function.Name.Name != "init" {
				continue
			}

			t.Errorf("%s declares init(): plugins are registered in the composition "+
				"root, where the order is readable and the state is not global "+
				"(S-10.1, ADR 0011)", name)
		}
	}
}

// parsedPackage returns each non-test Go file's import paths in a directory.
func parsedPackage(t *testing.T, dir string) [][]string {
	t.Helper()

	packages := make([][]string, 0, len(goFiles(t, dir)))

	for _, name := range goFiles(t, dir) {
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		paths := make([]string, 0, len(parsed.Imports))

		for _, imported := range parsed.Imports {
			paths = append(paths, strings.Trim(imported.Path.Value, `"`))
		}

		packages = append(packages, paths)
	}

	return packages
}

// goFiles returns the non-test Go files in a directory, as paths the parser can read
// from the test's working directory — which is the package's own directory, so
// `"."` is this package and `"../realtime"` is its neighbour.
func goFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		names = append(names, filepath.Join(dir, name))
	}

	if len(names) == 0 {
		t.Fatalf("no Go files in %s; the audit would pass by reading nothing", dir)
	}

	return names
}
