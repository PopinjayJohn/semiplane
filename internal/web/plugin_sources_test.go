package web_test

// The stylesheet's **scan coverage**, which is a different claim from the one
// `a11y_test.go` makes.
//
// # Why this file exists, and what it is not
//
// `TestTheBuiltStylesheetCarriesTheTokensAndTheGrid` holds the `@import`s: that
// `tokens.css`, `shell.css` and `tv.css` reached the built file. This holds the
// `@source` globs: that Tailwind **scanned** the trees where class names live.
//
// The two fail in the same way and neither is caught by anything else. A missing
// `@import` emits no token layer, no grid and no `.target`, and every §10.2 and
// §10.6 audit still passes because they read the markup, which is correct. A missing
// `@source` emits no Tailwind utility, and again every audit passes — the markup
// carries the class and the browser finds no rule for it. The product renders unstyled
// and the whole gate reports green. That is the same failure twice, so it is held
// twice, and the second is the one phase 8 introduced by adding a directory of markup
// (`internal/web/plugins`) the stylesheet did not name.
//
// # What is asserted, and why the sources rather than a hardcoded list
//
// Every class the plugin sources declare must have a rule in the **built** file, and a
// class nothing outside those sources declares must be among them.
//
// The first half is a walk rather than a list because a list is a second answer to
// "what classes do the plugins use", maintained by hand and wrong the first time a
// plugin adds one — which is precisely the defect this is here to catch. The second
// half is the scan sentinel declared in `plugins/scan_sentinel_test.go`, read out of
// there by name, because **without it the walk is vacuous**: every class the shipped
// plugins use today is a component class from `shell.css`, so all of them are in the
// built file whether or not this directory is scanned, and the walk would pass with the
// glob deleted. The sentinel is a Tailwind utility, which `shell.css` cannot supply,
// and `plugins.TestTheScanSentinelIsNamedInNoOtherSource` holds it unique to these
// sources — so its presence in the built file can only have come from the scan.
//
// # The negative control
//
// `TestTheScanCoverageIsAnAssertionAndNotAVacuousPass` is the other half: it removes the
// sentinel's rule from a **copy** of the built stylesheet and requires the class to be
// reported missing, and it requires `app.css` to name a plugin scan root at all. A test
// that cannot fail is a green light wired to nothing, so the mutation is applied to the
// bytes the assertion reads rather than left as an argument in a comment.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// pluginSourceRoot is the directory holding §10.6's reference plugins, relative to
// this package's own.
//
// A constant rather than a walk upwards, because the assertion is about *this*
// directory and a walk would silently widen it to `internal/web` as a whole — which is
// where the other `@source` glob points, and widening it would make this test
// indistinguishable from "some glob covered it".
const pluginSourceRoot = "plugins"

// classAttribute matches a `class="…"` attribute and captures its value.
//
// **Attributes only, and not every quoted string in the tree.** The plugins' markup
// is Go string constants full of prose, gofmt-quoted error messages and templ actions;
// treating every string as a class list would produce hundreds of nonsense "classes"
// and a test that fails for reasons no author could act on. What a class actually is,
// in every file under this tree, is a `class` attribute.
var classAttribute = regexp.MustCompile(`\bclass="([^"]*)"`)

// TestTheBuiltStylesheetScansThePluginSources holds the `@source` glob that covers
// `internal/web/plugins`.
//
// The walk and the sentinel, and the reasoning for both is in this file's header. The
// failure message names the fix rather than the symptom, because the symptom — a
// plugin that renders unstyled — looks like a CSS bug in a file nobody suspects.
func TestTheBuiltStylesheetScansThePluginSources(t *testing.T) {
	t.Parallel()

	css := built(t)
	sources, err := pluginSources(t)
	if err != nil {
		t.Fatalf("read the plugin sources: %v", err)
	}

	if len(sources) == 0 {
		t.Fatalf("no plugin source under %s carries a class. Either the directory moved or "+
			"the walk is broken, and in both cases this test proves nothing: the empty walk "+
			"would pass with the @source glob deleted", pluginSourceRoot)
	}

	declared, sentinel, err := pluginClasses(sources)
	if err != nil {
		t.Fatalf("read the plugin classes: %v", err)
	}

	if sentinel == "" {
		t.Fatalf("no scan sentinel is declared under %s. Without a class nothing outside these "+
			"sources names, every assertion below is satisfied by %s's own component classes "+
			"and passes with the @source glob deleted — the silent pass this test exists to "+
			"prevent", pluginSourceRoot, filepath.Base(pluginSourceRoot))
	}

	classes := append(declared.values(), sentinel)

	for _, class := range classes {
		if hasRule(css, class) {
			continue
		}

		t.Errorf("the plugin sources declare %q and the built stylesheet has no rule for it. "+
			"A class a plugin renders with is a class Tailwind has to have scanned, and "+
			"`static/css/app.css` is the only file that names the roots: check that it still "+
			"has an @source glob covering internal/web/plugins. This is not a build failure "+
			"and no accessibility audit can see it — it is a page that renders unstyled "+
			"while every gate in this repository reports green", class)
	}
}

// TestTheScanCoverageIsAnAssertionAndNotAVacuousPass is the negative control, and it
// is mechanical rather than by inspection.
//
// The claim under test is that `TestTheBuiltStylesheetScansThePluginSources` would
// **fail** if `app.css` stopped scanning this directory. That claim is checkable
// without a rebuild, because it only depends on which rules the current built file
// carries:
//
//   - with the glob, the sentinel's rule is in the built file and the walk passes;
//   - remove the `@source` lines from `app.css`, and the built file — which was built
//     from a version of `app.css` that had them — still carries it, so the *mutation*
//     under test has to be applied to the built file as well.
//
// So this asserts the shape of the guard directly: the sentinel's rule must be in the
// built file **because of a `@source` glob naming this directory**, and a stylesheet
// whose only copy of the sentinel class arrived by another route is not evidence of
// anything. The check is that the built file's copy of the sentinel and the source
// file's declaration of it are both present, and that removing either one breaks the
// walk — which is what the two sub-cases below do on copies, in memory.
//
// A negative control that re-implements the positive test is worth nothing, so this
// does not: it removes the rule from a copy of the CSS and requires the class to be
// reported missing, and it removes the `@source` lines from a copy of `app.css` and
// requires that copy to name no plugin root.
func TestTheScanCoverageIsAnAssertionAndNotAVacuousPass(t *testing.T) {
	t.Parallel()

	css := built(t)
	sources, err := pluginSources(t)
	if err != nil {
		t.Fatalf("read the plugin sources: %v", err)
	}

	declared, sentinel, err := pluginClasses(sources)
	if err != nil {
		t.Fatalf("read the plugin classes: %v", err)
	}

	if sentinel == "" {
		t.Fatal("no scan sentinel is declared under the plugin sources, so there is nothing " +
			"for this control to remove")
	}

	t.Run("RemovingTheSentinelRuleMakesTheWalkFail", func(t *testing.T) {
		t.Parallel()

		// The same walk, against a stylesheet the sentinel's rule has been cut out of.
		// This is the mutation `make css` without the glob produces, and it is done on a
		// copy so the test is repeatable and order-independent.
		without := strings.Replace(css, "."+sentinel+"{", ".removed-by-this-test{", 1)

		if without == css {
			t.Fatalf("the built stylesheet has no %q rule to remove, so this control would "+
				"pass without proving anything", sentinel)
		}

		if hasRule(without, sentinel) {
			t.Errorf("a stylesheet with %s's rule cut out still reports the class present, so "+
				"the positive assertion cannot fail on it", sentinel)
		}

		// And every other class still resolves, which is the point of the sentinel: it
		// is the only class whose absence distinguishes "scanned" from "not scanned".
		for _, class := range declared.values() {
			if !hasRule(without, class) {
				t.Errorf("cutting out the sentinel rule also removed %q; the control is not "+
					"isolating what it claims to isolate", class)
			}
		}
	})

	t.Run("DroppingTheSourceGlobRemovesTheScanRoot", func(t *testing.T) {
		t.Parallel()

		entry, err := os.ReadFile(filepath.Join("static", "css", "app.css"))
		if err != nil {
			t.Fatalf("read static/css/app.css: %v", err)
		}

		lines := strings.Split(string(entry), "\n")
		kept := make([]string, 0, len(lines))
		dropped := 0

		for _, line := range lines {
			if strings.Contains(line, "@source") && strings.Contains(line, pluginSourceRoot) {
				dropped++

				continue
			}

			kept = append(kept, line)
		}

		if dropped == 0 {
			t.Fatalf("static/css/app.css has no @source glob covering %s. If that is "+
				"deliberate — the plugins were removed, say — then the sentinel and this "+
				"file go with it, and leaving them asserts coverage that no longer exists",
				pluginSourceRoot)
		}

		for _, line := range kept {
			if strings.Contains(line, "@source") && strings.Contains(line, pluginSourceRoot) {
				t.Errorf("a copy of app.css with its plugin @source glob removed still names "+
					"the plugin root: %q", line)
			}
		}
	})
}

// hasRule reports whether the built stylesheet carries a rule for a class.
//
// **`.` then the name, with no trailing character required**, because the minifier
// writes `.a,.b{…}` and a check that demanded a `{` immediately after the class would
// read zero against a correct build for every class that is ever grouped with
// another. The same argument `TestTheBuiltStylesheetCarriesTheTokensAndTheGrid` makes
// about attribute-value quotes.
//
// A class whose name needs escaping in a selector is checked in both forms, since
// `w-1/2` and `hover:bg-red-500` are written escaped by the minifier and unescaped by
// whoever wrote them. Getting that wrong produces a failing test for a correct build,
// which is how a check like this comes to be ignored.
func hasRule(css, class string) bool {
	if strings.Contains(css, "."+class) {
		return true
	}

	escaped := escapeSelector(class)

	return escaped != class && strings.Contains(css, "."+escaped)
}

// escapeSelector renders a class name the way a minifier writes it in a selector.
//
// **Every character Tailwind's own class names need escaped**, and nothing else.
//
// The list has two halves and both are needed:
//
//   - the separators and numerals — the fraction slash in `w-1/2`, the variant colon
//     in `hover:bg-accent-600`, the decimal point in `p-1.5`;
//   - the arbitrary-value punctuation in `w-[calc(100%-1rem)]`, where the brackets,
//     the parentheses and the percent sign are all escaped by the minifier.
//
// The backslash leads because a name already carrying one is escaped twice.
//
// A wrong escape is a false negative, and a false negative on a check like this is the
// failure mode it has: a test that cannot pass gets deleted, and the coverage it was
// holding goes with it. So the list is closed, each entry has an example in
// `TestTheSelectorMatchingHandlesTheClassesTailwindActuallyEscapes`, and a character
// missing from it produces a failing sub-case rather than a silently unmatched class.
func escapeSelector(class string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		"/", `\/`,
		":", `\:`,
		".", `\.`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"%", `\%`,
		"#", `\#`,
		"&", `\&`,
		"!", `\!`,
	).Replace(class)
}

// pluginSources returns the plugin source files, as name → contents.
//
// **`*.go` and `*.templ`, and test files among them.** The plugins' markup is Go
// string constants and their tests carry templates too, and a glob that missed the
// test files would be a narrower claim than the one the `@source` line makes — Tailwind
// scans every file it is pointed at, whatever its name.
func pluginSources(t *testing.T) (map[string]string, error) {
	t.Helper()

	sources := make(map[string]string)

	err := filepath.WalkDir(
		pluginSourceRoot,
		func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return fmt.Errorf("walk the plugin sources: %w", err)
			}

			if entry.IsDir() || (!strings.HasSuffix(path, ".go") &&
				!strings.HasSuffix(path, ".templ")) {
				return nil
			}

			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return fmt.Errorf("read a plugin source: %w", readErr)
			}

			sources[path] = string(raw)

			return nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("walk the plugin sources: %w", err)
	}

	return sources, nil
}

// pluginClasses returns every class the plugin sources declare, and the scan sentinel.
//
// The sentinel is read through the **Go parser** rather than a regular expression,
// which is the reason it is worth a parser: the declaration is a constant
// declaration, so reading it as Go means a renamed identifier or a value built from
// another constant produces "no sentinel is declared" — the honest answer — rather
// than a regex that matched the wrong string and certified coverage nothing depends
// on.
//
// The classes themselves come from `class="…"` attributes, for the reason this file's
// header gives: prose in a Go constant is not a class, and a walk that treated it as
// one would be a walk whose every result is noise.
func pluginClasses(sources map[string]string) (classes classSet, sentinel string, err error) {
	classes = newClassSet()

	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}

	// Sorted so the error a run produces is the same one every time, which is the
	// same reason `plugin.Registry.Systems` is ordered rather than ranged.
	slices.Sort(paths)

	for _, path := range paths {
		source := sources[path]

		for _, match := range classAttribute.FindAllStringSubmatch(source, -1) {
			classes.add(strings.Fields(match[1])...)
		}

		found, declared, parseErr := scanSentinel(path, source)
		if parseErr != nil {
			return nil, "", parseErr
		}

		if found && sentinel == "" {
			sentinel = declared
		}

		if found && declared != sentinel {
			return nil, "", errSentinelDisagrees(path, declared, sentinel)
		}
	}

	return classes, sentinel, nil
}

// scanSentinel reads one file's scan-sentinel declaration, if it has one.
//
// Parsed, and only for the string-valued constant declarations: a file that does not
// parse is reported rather than skipped, because a plugin source the parser cannot
// read is a file whose contents this assertion knows nothing about, and silence there
// would be the vacuous pass again.
func scanSentinel(path, source string) (found bool, declared string, err error) {
	if !strings.Contains(source, "ScanSentinelClass") {
		return false, "", nil
	}

	file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.SkipObjectResolution)
	if err != nil {
		return false, "", fmt.Errorf("parse the plugin source %s: %w", path, err)
	}

	for _, decl := range file.Decls {
		genDecl, isGen := decl.(*ast.GenDecl)
		if !isGen {
			continue
		}

		for _, spec := range genDecl.Specs {
			valueSpec, isValue := spec.(*ast.ValueSpec)
			if !isValue || len(valueSpec.Names) != 1 || len(valueSpec.Values) != 1 {
				continue
			}

			if valueSpec.Names[0].String() != "ScanSentinelClass" {
				continue
			}

			literal, isLiteral := valueSpec.Values[0].(*ast.BasicLit)
			if !isLiteral || literal.Kind != token.STRING {
				continue
			}

			quoted, unquotedErr := strconv.Unquote(literal.Value)
			if unquotedErr != nil {
				return false, "", fmt.Errorf("unquote the scan sentinel in %s: %w",
					path, unquotedErr)
			}

			return true, quoted, nil
		}
	}

	return false, "", nil
}

// errSentinelDisagrees is the failure for two files declaring different sentinels.
func errSentinelDisagrees(path, declared, first string) error {
	return &sentinelDisagreementError{path: path, declared: declared, first: first}
}

// sentinelDisagreementError is the typed form of that failure, so the message says what to
// do rather than what happened.
type sentinelDisagreementError struct {
	path     string
	declared string
	first    string
}

func (e *sentinelDisagreementError) Error() string {
	return "the scan sentinel is declared twice with different values: " +
		e.declared + " in " + e.path + " and " + e.first +
		" elsewhere; one class, one declaration, or the stylesheet's coverage assertion " +
		"depends on which of two a reader happened to find"
}

// classSet is the set of class names the plugin sources declare.
//
// A set with a `values` accessor rather than a bare `map[string]struct{}` because the
// caller needs the names in a stable order and a map's iteration order is a property of
// the process — the same fixed-point argument ADR 0037 makes and `plugin.Registry.Systems`
// answers with `slices.Clone`.
type classSet map[string]struct{}

func newClassSet() classSet { return make(classSet) }

// add records each of the given names.
//
// **The template expression `{{.TargetClass}}` is kept, not filtered.** A class name
// this walk cannot resolve is a class whose presence in the built file is a question
// rather than a fact, and dropping it would be to make the walk's output tidier at the
// cost of making it wrong: the class it names is the plugin's `.target` contract and
// §10.6's target-size gate reads it from the built stylesheet by that very name.
//
// So it is recorded, and `hasRule` then answers the question honestly — `{{.TargetClass}}`
// has no rule, and a plugin interpolating an unknown class into `class="…"` is a
// defect this walk reports rather than hides. The alternative, filtering anything that
// is not `[-a-zA-Z0-9_/]`, would silence that and silence a typo at the same time.
func (c classSet) add(names ...string) {
	for _, name := range names {
		if name == "" {
			continue
		}

		c[name] = struct{}{}
	}
}

// values returns every recorded name, in sorted order.
func (c classSet) values() []string {
	names := make([]string, 0, len(c))
	for name := range c {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

// TestTheSelectorMatchingHandlesTheClassesTailwindActuallyEscapes is the check on the
// check: `hasRule` matches a class name against the built stylesheet, and the built
// stylesheet is minified, so a name needing an escape must be found in escaped form.
//
// **Both forms, and both directions.** The unescaped match is tried first because the
// overwhelming majority of class names need no escaping and must be found by a plain
// substring; the escaped match is the fallback for the three characters Tailwind's own
// utilities contain. A class that needed escaping and was looked for only in unescaped
// form would report missing against a correct build, and a test that fails on a correct
// build is a test that gets commented out.
func TestTheSelectorMatchingHandlesTheClassesTailwindActuallyEscapes(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		class   string
		renders string
	}{
		{name: "Plain", class: "notice", renders: ".notice,.card-meta{"},
		{name: "Fraction", class: "w-1/2", renders: `.w-1\/2{`},
		{name: "Decimal", class: "p-1.5", renders: `.p-1\.5{`},
		{name: "Variant", class: "hover:bg-accent-600", renders: `.hover\:bg-accent-600{`},
		{
			name:    "TargetStylePunctuation",
			class:   `w-[calc(100%-1rem)]`,
			renders: `.w-\[calc\(100\%-1rem\)\]{`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if !hasRule(testCase.renders, testCase.class) {
				t.Errorf("hasRule(%q, %q) = false on the minified selector it renders as; a "+
					"class needing an escape must be found in escaped form, or the assertion "+
					"fails on a correct build and gets deleted with it",
					testCase.class, testCase.renders)
			}
		})
	}

	// And the negative: a class nothing renders is reported missing. A `hasRule` that
	// answered true for everything would satisfy every case above.
	if hasRule(".notice{", "card-meta") {
		t.Error("hasRule found a class the stylesheet does not carry; it must answer false " +
			"for a class with no rule, or the coverage walk passes unconditionally")
	}
}
