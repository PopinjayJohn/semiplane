package tokens_test

// Delivery and safety properties of the three modules.
//
// # The four claims, and why each is a test and not a review note
//
//  1. **The served bytes are the checked-in bytes.** `go:embed` and a file on
//     disk are two artefacts; the page serves one of them and a person edits the
//     other. The same arrangement as `internal/web/static/js/head.js` and
//     `shell.ResolverSource`, where `TestTheCheckedInScriptIsTheServedScript`
//     holds the pair together.
//
//  2. **Every module is written, and in an order that works.** The order is a
//     dependency — `tokens.js` calls `spFocusStep`, which `step.js` defines — so a
//     new file dropped into this directory and not added to the order is a file
//     the page never writes. `Names()` walks the embedded set and compares it
//     against `Order()`, which is the only assertion that can notice.
//
//  3. **No module can end its own script element.** The bodies are inlined
//     unescaped, because `&lt;` is not a JavaScript operator. A `</script` or a
//     `<!--` in a module would end the element and turn the rest of the script
//     into markup on every play page.
//
//  4. **No module writes markup, and no selector is built from a value.** The
//     placements on a table are campaign content and the remembered tab is
//     whatever is in `localStorage`; neither may reach `innerHTML`, and neither
//     may be concatenated into a selector. Both are refusals, and a refusal nobody
//     checks is not one.

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/web/static/js/tokens"
)

// TestTheServedScriptsAreTheCheckedInScripts is claim 1.
func TestTheServedScriptsAreTheCheckedInScripts(t *testing.T) {
	t.Parallel()

	for _, name := range tokens.Names() {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}

		served := tokens.Source(name)

		if served != string(raw) {
			t.Errorf("%s: the embedded copy is %d bytes and the file on disk is %d.\n"+
				"The file is what a person edits and the embedded bytes are what the "+
				"browser runs; they must be the same bytes, and the way to fix a "+
				"mismatch is to rebuild, not to edit one of them by hand",
				name, len(served), len(raw))
		}
	}
}

// TestEveryModuleIsWrittenAndTheOrderIsADependableOne is claim 2.
func TestEveryModuleIsWrittenAndTheOrderIsADependableOne(t *testing.T) {
	t.Parallel()

	written := tokens.Order()
	if len(written) == 0 {
		t.Fatal("the page writes no modules; the token list's keyboard model and " +
			"D16's remembered tab are both absent")
	}

	// **Compared as sets, not as sequences.** `Order` is a sequence because the
	// order is a dependency; `Names` is sorted because a directory listing has no
	// order. Comparing the sequence to the listing would fail on the dependency and
	// pass on a missing file, which is the wrong way round — so the sets are
	// compared here and the two ordering constraints are asserted separately
	// below.
	if !equalSets(written, tokens.Names()) {
		t.Errorf("the page writes %v and this directory holds %v. A module that is "+
			"not in the order is a file the page never sends, and a module that is "+
			"in the order and not on disk is a request for a 404 — one of the two "+
			"is the bug and neither is visible without this comparison",
			written, tokens.Names())
	}

	// The order is a dependency, so it is asserted rather than trusted: the kernel
	// defines the one name the list module calls, and a page that wrote them the
	// other way round would throw `ReferenceError` on the first keypress.
	//
	// **Strictly before, not merely present.** A first version asserted only that
	// `StepFile` appears in the order and that the caller does not precede it —
	// which an order putting them both *last* also satisfies, so a list module
	// written after the kernel it calls passed. Mutation-checked, and the strict
	// clause is what caught it.
	at := slices.Index(written, tokens.StepFile)
	caller := slices.Index(written, tokens.TokensFile)

	switch {
	case at < 0 || caller < 0:
		t.Errorf("the page never writes %s or %s, and %s calls spFocusStep",
			tokens.StepFile, tokens.TokensFile, tokens.TokensFile)
	case at > caller:
		t.Errorf("the page writes %s after %s, and %s calls spFocusStep. Two classic "+
			"scripts in the wrong order throw ReferenceError on the first keypress, "+
			"which is the least visible failure this page has",
			tokens.TokensFile, tokens.StepFile, tokens.TokensFile)
	}
}

// equalSets compares two slices as sets, which is what a directory listing and a
// write order can honestly be compared as.
func equalSets(got, want []string) bool {
	sorted := slices.Clone(got)
	slices.Sort(sorted)

	other := slices.Clone(want)
	slices.Sort(other)

	return slices.Equal(sorted, other)
}

// TestEveryModuleCanEndItsOwnScriptElement is claim 3, from the file's side.
//
// The same three sequences `play.checkInert` refuses on the way out. Asserted from
// both ends on purpose: the writer's check is a guard that could be dropped, and
// this is the assertion that says what the guard is guarding.
func TestNoModuleCanEndItsOwnScriptElement(t *testing.T) {
	t.Parallel()

	for _, name := range tokens.Names() {
		lowered := strings.ToLower(tokens.Source(name))

		for _, violation := range []string{"</script", "<!--", "-->"} {
			if strings.Contains(lowered, violation) {
				t.Errorf("%s contains %q, which ends a script element. The body is "+
					"written unescaped — escaping a script turns `&lt;` into "+
					"something that is not an operator — so the rest of the module "+
					"would be parsed as markup on every play page",
					name, violation)
			}
		}
	}
}

// markupSinks are the APIs that turn a string into markup.
var markupSinks = []string{
	"innerHTML",
	"outerHTML",
	"insertAdjacentHTML",
	"document.write",
	"createContextualFragment",
	"new Function",
}

// selectorCalls are the APIs that take a selector.
var selectorCalls = []string{"querySelector(", "querySelectorAll(", "closest("}

// TestNoModuleWritesMarkupOrBuildsASelectorFromAValue is claim 4.
func TestNoModuleWritesMarkupOrBuildsASelectorFromAValue(t *testing.T) {
	t.Parallel()

	for _, name := range tokens.Names() {
		source := strippedSource(tokens.Source(name))

		for _, sink := range markupSinks {
			if strings.Contains(source, sink) {
				t.Errorf("%s calls %s. A placement name is campaign content and a "+
					"remembered tab is whatever is in localStorage; neither may reach "+
					"a markup sink", name, sink)
			}
		}

		for _, call := range selectorCalls {
			for _, argument := range selectorArguments(source, call) {
				if !isPlainLiteral(argument) {
					t.Errorf("%s calls %s with %q. A selector assembled from a value "+
						"is how campaign content becomes a query, and the modules find "+
						"their elements first and compare afterwards",
						name, call, argument)
				}
			}
		}
	}
}

// selectorArguments returns the argument of every call of one selector API.
func selectorArguments(source, call string) []string {
	var found []string

	for index := 0; ; {
		at := strings.Index(source[index:], call)
		if at < 0 {
			return found
		}

		rest := source[index+at+len(call):]

		// The first `)` closes the call: no module here nests a call inside a
		// selector, and an argument with one would be a selector worth arguing
		// about rather than a literal.
		argument, after, closed := strings.Cut(rest, ")")
		if !closed {
			found = append(found, rest)

			return found
		}

		found = append(found, argument)
		index += at + len(call) + len(after)
	}
}

// isPlainLiteral reports whether text is one string literal with nothing
// concatenated into it.
func isPlainLiteral(text string) bool {
	trimmed := strings.TrimSpace(text)

	if len(trimmed) < 2 {
		return false
	}

	quote := trimmed[0]
	if quote != '"' && quote != '\'' {
		return false
	}

	return trimmed[len(trimmed)-1] == quote && !strings.Contains(trimmed, "+")
}

// TestEveryLocalStorageAccessIsInsideATry is D16's silent fallback, asserted.
//
// Storage throws in a private window, when a site is out of quota, and in a
// browser configured to block it. **Every one of those is an ordinary state**, so
// an access that is not guarded is a play page that throws on load for a reader
// who did nothing unusual — and the throw happens inside the module that reads the
// remembered tab, which is exactly where the fallback is supposed to be.
//
// Scoped to `railtabs.js`, which is the only module that stores anything: the
// token list keeps no state between visits.
func TestEveryLocalStorageAccessIsInsideATry(t *testing.T) {
	t.Parallel()

	source := strippedSource(tokens.Source(tokens.RailTabsFile))

	accesses := 0

	for index := 0; ; {
		at := strings.Index(source[index:], "localStorage")
		if at < 0 {
			break
		}

		accesses++
		at += index

		before := source[:at]

		guardAt := strings.LastIndex(before, "try {")
		escapeAt := strings.LastIndex(before, "} catch")

		if guardAt < escapeAt {
			t.Errorf("%s accesses localStorage at byte %d with no enclosing `try`. "+
				"Storage throws in a private window and on a quota, a reader reaches "+
				"both by doing nothing unusual, and D16 says the fallback is silent",
				tokens.RailTabsFile, at)
		}

		index = at + len("localStorage")
	}

	if accesses == 0 {
		t.Errorf("%s never touches localStorage, so D16's per-campaign memory is "+
			"not implemented and the tier default is re-derived on every visit",
			tokens.RailTabsFile)
	}
}
