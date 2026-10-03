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
// **A selector boundary, not a bare substring.** `strings.Contains(css, ".notice")`
// answers `true` for a stylesheet carrying only `.notice--warning`, which is exactly the
// state this assertion exists to catch: the widget's own `.notice` was never emitted, a
// modifier was, and a substring check reports the page styled. Measured, not
// hypothesised — the minified stylesheet carries three `.notice--*` rules and no
// `.notice`, so removing the bare rule left `hasRule("notice")` answering `true`.
//
// The boundary is "the next character is not one a class name can continue with", which
// is the same set `escapeSelector` handles on the other side: identifier characters and
// the escaped punctuation. A selector end, a combinator, a pseudo-class or a brace all
// satisfy it, and a further `-`, `_`, `:` or escape does not.
//
// **Both forms, and both are needed.** The unescaped match is tried first because the
// overwhelming majority of class names need no escaping; the escaped match is the
// fallback for the characters Tailwind's own utilities contain. A class that needed
// escaping and was looked for only unescaped would report missing against a correct
// build, and a test that fails on a correct build is a test that gets commented out.
func hasRule(css, class string) bool {
	if selectorMatches(css, class) {
		return true
	}

	escaped := escapeSelector(class)

	return escaped != class && selectorMatches(css, escaped)
}

// selectorMatches reports whether the built stylesheet contains `.` followed by the
// given selector text and a character that cannot continue a class name.
func selectorMatches(css, selector string) bool {
	// Each pass skips a match that turned out to be the *prefix* of a longer class name,
	// resuming after the matched text rather than at the dot — so `.button--primary`
	// cannot make the loop revisit itself and a pathological stylesheet cannot spin.
	for offset := 0; offset < len(css); {
		at := strings.Index(css[offset:], "."+selector)
		if at < 0 {
			return false
		}

		end := offset + at + 1 + len(selector)
		if !continuesSelector(css[end:]) {
			return true
		}

		offset = end
	}

	return false
}

// continuesSelector reports whether a selector continues past where a class name ends.
//
// **The characters that can continue one**, and nothing else. Alphanumerics and `_` are
// ordinary identifier characters, `-` continues a name and starts a modifier, and a
// backslash continues into an escape — the variant separator, so a variant-prefixed
// class is only fully matched once its whole escaped text has been consumed.
//
// **An unescaped colon is a boundary, not a continuation**, and that is the subtle one.
// A variant's colon is *escaped* in a selector (`hover\:bg-accent-600`) while a
// pseudo-class's is bare (`:hover`), so a bare colon can never be inside a class name and
// can always be read as the start of a pseudo-class. Getting this backwards reports
// `.card-meta:hover{}` as no rule for `card-meta` — a class this assertion would then say
// is missing from a build that styles it.
func continuesSelector(rest string) bool {
	if rest == "" {
		// A truncated file is not a build this assertion can reason about, and calling
		// it a boundary would report a class present on the strength of a missing
		// closing brace.
		return true
	}

	switch next := rest[0]; {
	case next >= 'a' && next <= 'z', next >= 'A' && next <= 'Z',
		next >= '0' && next <= '9', next == '_', next == '-', next == '\\':
		return true
	default:
		return false
	}
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
// **`*.go` and `*.templ`, test files among them.** The plugins' markup is Go string
// constants and their tests carry templates too, and a glob that missed the test
// files would be a narrower claim than the one the `@source` line makes — Tailwind
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
// # Two readers, and the reason the first one is a parser
//
// Go sources are read through `go/parser` and the class attributes are then taken from
// the **string literals**, so a `class="…"` written in a comment is not a class. This
// is not a refinement — it is the difference between the walk working and the walk
// lying. `linkpreview`'s own test file contains `class="untargeted"` inside a comment
// explaining that a rename would be caught, and a walk that read comments reported
// `untargeted` as a class the stylesheet was missing. The failure is loud and it is
// *wrong*, and a gate that cries wolf about a comment is a gate whose next real failure
// is ignored. AGENTS.md states the same rule for the accessibility audits — parse the
// DOM, never substring-match the markup — and a Go comment is the same hazard.
//
// `.templ` files are read as raw text, and none exist under the plugin root today. If
// one lands, its classes are picked up by the same regexp as the Go literals, and a
// class named in a `{{/* comment */}}` would be reported — the honest failure for a
// format whose comments this package does not parse.
//
// # The sentinel is read through the same parser, and that is what makes it a parser
//
// The declaration is a constant declaration, so reading it as Go means a renamed
// identifier or a value built from another constant produces "no sentinel is declared"
// — the honest answer — rather than a regex that matched the wrong string and certified
// coverage nothing depends on.
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

		// The Go parser path, with its own sentinel read. A `.templ` file takes the
		// raw-text path and can hold no sentinel.
		if strings.HasSuffix(path, ".go") {
			literals, declared, found, parseErr := goSourceLiterals(path, source)
			if parseErr != nil {
				return nil, "", parseErr
			}

			for _, literal := range literals {
				classes.add(classesIn(literal)...)
			}

			if !found {
				continue
			}

			if sentinel != "" && declared != sentinel {
				return nil, "", errSentinelDisagrees(path, declared, sentinel)
			}

			sentinel = declared

			continue
		}

		classes.add(classesIn(source)...)
	}

	return classes, sentinel, nil
}

// goSourceLiterals returns a Go file's string literals and its scan sentinel, if it
// declares one.
//
// **Parsed rather than read as text**, for the reason `pluginClasses` gives, and with
// `parser.SkipObjectResolution` because this is a lexical question and resolving every
// declaration to check one constant name is work the walk does not need.
//
// A file that does not parse is an error rather than a skip: a plugin source this
// package cannot read is a file whose contents the assertion knows nothing about, and
// silence there is the vacuous pass this whole arrangement exists to prevent.
func goSourceLiterals(
	path, source string,
) (literals []string, sentinel string, found bool, err error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, "", false, fmt.Errorf("parse the plugin source %s: %w", path, err)
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.BasicLit:
			if value.Kind != token.STRING {
				return true
			}

			unquoted, unquotedErr := strconv.Unquote(value.Value)
			if unquotedErr != nil {
				// A string this package cannot unquote is not a class list and not
				// markup; skipping it is the only answer that does not invent one.
				return true
			}

			literals = append(literals, unquoted)

			return true
		case *ast.GenDecl:
			declared, isSentinel := sentinelIn(value)
			if isSentinel {
				sentinel, found = declared, true
			}

			return true
		default:
			return true
		}
	})

	return literals, sentinel, found, nil
}

// sentinelIn reports a declaration's value when it is the scan sentinel.
//
// **The value must be a plain string literal**, and anything else is "not the
// sentinel". A sentinel computed at runtime would be a declaration whose value this
// assertion cannot check, and treating it as absent is the safe direction: the
// coverage test then fails saying no sentinel exists rather than passing on a class
// nobody can name.
func sentinelIn(decl *ast.GenDecl) (string, bool) {
	if decl.Tok != token.CONST {
		return "", false
	}

	for _, spec := range decl.Specs {
		valueSpec, isValue := spec.(*ast.ValueSpec)
		if !isValue || len(valueSpec.Names) != 1 || len(valueSpec.Values) != 1 {
			continue
		}

		if valueSpec.Names[0].String() != "ScanSentinelClass" {
			continue
		}

		literal, isLiteral := valueSpec.Values[0].(*ast.BasicLit)
		if !isLiteral || literal.Kind != token.STRING {
			return "", false
		}

		unquoted, err := strconv.Unquote(literal.Value)
		if err != nil {
			return "", false
		}

		return unquoted, true
	}

	return "", false
}

// classesIn returns the class names one `class="…"` attribute value declares.
//
// # Templ actions are dropped, and the drop is the whole reason this is not a `Fields`
//
// A plugin template writes `class="{{.TargetClass}}"` — the class is supplied by the
// component's view model, so the literal in the source is an **expression**, not a
// class name. Reporting it as one would fail the coverage walk against markup that is
// correct, and `hasRule` would have to be taught to recognise every templ form to
// tolerate it. So an attribute value is split on whitespace and any field containing
// `{{` is dropped.
//
// **The substituted class is not thereby unchecked**, and this is worth being explicit
// about because it looks like a hole: what `{{.TargetClass}}` expands to is
// `ui.TargetClass`, a constant in `internal/web/components/ui`, whose spelling is
// asserted there and whose `.target` rule is asserted from the **built** stylesheet by
// §10.6's target-size gate. The coverage walk's subject is the classes a plugin's own
// source spells out; the interpolated ones belong to the package that declares them.
func classesIn(text string) []string {
	var names []string

	for _, match := range classAttribute.FindAllStringSubmatch(text, -1) {
		for field := range strings.FieldsSeq(match[1]) {
			if strings.Contains(field, "{{") {
				continue
			}

			names = append(names, field)
		}
	}

	return names
}

// errSentinelDisagrees is the failure for two files declaring different sentinels.
func errSentinelDisagrees(path, declared, first string) error {
	return &sentinelDisagreementError{path: path, declared: declared, first: first}
}

// sentinelDisagreementError is the typed form of that failure, so the message says
// what to do rather than what happened.
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

// TestASelectorPrefixIsNotASelector is the check on `hasRule` that the minified build
// forced, and it is the one a reader should read before believing the coverage walk.
//
// **The measured defect.** `strings.Contains(css, ".notice")` answers `true` for a
// stylesheet carrying only `.notice--warning`, `.notice--error` and nothing else — and
// the built stylesheet carries exactly three `.notice--*` rules. So a bare substring
// check reported the roller's own `.notice` present on the strength of a modifier it
// never uses, and the walk passed with the bare rule deleted from the build. That is a
// silent pass of the kind this file exists to prevent, found by deleting the rule and
// requiring the assertion to notice.
//
// Three near-misses, because each is a character that could plausibly have been
// forgotten: a hyphen (the modifier), an escaped colon (a variant), and a second
// hyphen past a matcher that only checked the first.
func TestASelectorPrefixIsNotASelector(t *testing.T) {
	t.Parallel()

	const stylesheet = ".notice--warning{}" +
		".hover\\:bg-accent-600:hover{}" +
		".button--primary{}" +
		".card-meta{}" +
		".notice{}"

	for _, testCase := range []struct {
		class string
		want  bool
	}{
		{class: "notice", want: true},
		{class: "notice--warning", want: true},
		{class: "notice--error", want: false},
		{class: "button", want: false},
		{class: "button--primary", want: true},
		{class: "card", want: false},
		{class: "card-meta", want: true},
		{class: "hover:bg-accent-600", want: true},
		{class: "hover:bg-red-500", want: false},
		// The bare word inside a longer identifier, which is the case a matcher that
		// checked only the character immediately after would also get right — and the
		// one a matcher that checked *anywhere later* would get wrong.
		{class: "notice--warning-extra", want: false},
	} {
		t.Run(testCase.class, func(t *testing.T) {
			t.Parallel()

			if got := hasRule(stylesheet, testCase.class); got != testCase.want {
				t.Errorf("hasRule = %v for %q, want %v; a class name that is only a "+
					"prefix of a longer one is not a rule for it, and a matcher that "+
					"says otherwise certifies a page as styled when it is not",
					got, testCase.class, testCase.want)
			}
		})
	}
}
