package tokens_test

// The key model, read out of `tokens.js` and evaluated against the shipped
// arithmetic.
//
// # What is being claimed
//
// UI §7.6 requires `ArrowUp`/`ArrowDown` to move focus between placements, and
// UI §7.5's arrow row adds `Home` and `End` as the composite-widget pair. Those
// four keys are the whole keyboard model, and **they are declared as data** rather
// than as a chain of comparisons in a handler:
//
//     spTokenKeys   — keys that move relatively, by a wrapped step
//     spTokenEdges  — keys that jump to an absolute row, where a negative index
//                     means "count + index"
//
// Because they are data, they exist exactly once in the file, and this file can
// read them. It then evaluates every one of them, over every row count and every
// starting row, using `step.js`'s own arithmetic — so the claim "ArrowUp moves
// focus to the previous row and wraps" is checked against the bytes a browser
// will run, not against a description of them.
//
// # Why not just read the key names out with a regexp
//
// Because a regexp finds `ArrowUp` and is silent about what it does with it. The
// failure this catches is `ArrowUp: 1`, which moves the wrong way, passes every
// substring assertion in the repository, and is invisible until somebody uses the
// arrow keys on the first token of a table. The direction is asserted as a value
// (`ArrowUp` must be negative) *and* as a behaviour over the grid.
//
// # The one thing this file cannot reach
//
// The wiring — the branch that sends a relative key to `spFocusStep` and an edge
// key to `count + edge`. That is DOM work in a file this gate does not
// evaluate, so it is asserted structurally and narrowly at the bottom: both
// expressions must appear, verbatim, in the function that resolves a key. Both
// were checked by mutation; see the report.

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/web/static/js/tokens"
)

// tokensSource reads the token list's module from disk.
func tokensSource(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(tokens.TokensFile)
	if err != nil {
		t.Fatalf("read %s: %v", tokens.TokensFile, err)
	}

	return string(raw)
}

// the two tables' names, as the module declares them.
const (
	keysTable  = "spTokenKeys"
	edgesTable = "spTokenEdges"
)

// TestTheArrowKeysBindExactlyTheFourTheRecordNames is the vocabulary claim.
//
// **Exact, not a superset.** A key added to either table gets a behaviour nobody
// asked for, and the most likely accident is a letter key — `PageUp`, `Space` —
// which would then stop doing what the browser does with it.
func TestTheArrowKeysBindExactlyTheFourTheRecordNames(t *testing.T) {
	t.Parallel()

	source := tokensSource(t)

	relative := mustTable(t, source, keysTable)
	edges := mustTable(t, source, edgesTable)

	if got, want := sortedKeys(relative), []string{"ArrowDown", "ArrowUp"}; !equal(got, want) {
		t.Errorf("%s binds %v, want exactly %v; a key bound here gets a behaviour "+
			"the browser's own handling of that key no longer applies to",
			keysTable, got, want)
	}

	if got, want := sortedKeys(edges), []string{"End", "Home"}; !equal(got, want) {
		t.Errorf("%s binds %v, want exactly %v", edgesTable, got, want)
	}
}

// TestUpMovesTowardsTheStartAndDownTowardsTheEnd is the direction claim, held
// twice over.
//
// Once as values, because a step of `+1` on `ArrowUp` is the specific bug, and
// once as behaviour over the grid, because a value can be right while the rule it
// is fed to is applied from the wrong end.
func TestUpMovesTowardsTheStartAndDownTowardsTheEnd(t *testing.T) {
	t.Parallel()

	source := tokensSource(t)
	relative := mustTable(t, source, keysTable)
	edges := mustTable(t, source, edgesTable)

	if relative["ArrowUp"] != -1 {
		t.Errorf("%s binds ArrowUp to %v, want -1: a step of +1 moves focus "+
			"towards the end of the list, which is the direction ArrowDown has",
			keysTable, relative["ArrowUp"])
	}

	if relative["ArrowDown"] != 1 {
		t.Errorf("%s binds ArrowDown to %v, want 1", keysTable, relative["ArrowDown"])
	}

	if edges["Home"] != 0 {
		t.Errorf("%s binds Home to %v, want 0: zero is the first row, and an "+
			"edge of zero is the one edge that needs no arithmetic",
			edgesTable, edges["Home"])
	}

	if edges["End"] != -1 {
		t.Errorf("%s binds End to %v, want -1: a negative edge means `count + "+
			"edge`, so -1 is the last row and the wrap rule stays the only "+
			"arithmetic in the module", edgesTable, edges["End"])
	}
}

// TestEveryBoundKeyReachesTheRowItShould is the behavioural claim, for all four
// keys, over every list length and every starting row.
func TestEveryBoundKeyReachesTheRowItShould(t *testing.T) {
	t.Parallel()

	rule := mustParseKernel(t)
	source := tokensSource(t)

	relative := mustTable(t, source, keysTable)
	edges := mustTable(t, source, edgesTable)

	for count := 1; count <= 12; count++ {
		for from := 0; from < count; from++ {
			for key, step := range relative {
				got := rule.eval(map[string]float64{
					"from": float64(from), "delta": step, "count": float64(count),
				})
				want := positiveMod(float64(from)+step, float64(count))

				if got != want {
					t.Errorf("%s from row %d of %d landed on %v, want %v: the key "+
						"that moves one row %s must wrap within the list",
						key, from, count, got, want,
						map[bool]string{true: "down", false: "up"}[step > 0])
				}
			}

			for key, edge := range edges {
				// The module's own edge arithmetic, `edge < 0 ? count + edge :
				// edge`, written out here because this is the specification and
				// the module is the thing being checked.
				want := edge
				if edge < 0 {
					want = float64(count) + edge
				}

				if want < 0 || want >= float64(count) {
					t.Errorf("%s at %d rows resolves to %v, which is not a row; a "+
						"focus call with that index moves focus nowhere",
						key, count, want)
				}
			}
		}
	}
}

// TestTheKeyModelIsResolvedThroughTheTestedArithmetic is the structural claim
// about the wiring, and it is narrow on purpose.
//
// Two expressions, both required verbatim in the module:
//
//   - `spFocusStep(from, move, count)` — a relative key goes through the kernel.
//     If a future edit calls something else, or omits `count`, the kernel is no
//     longer the rule and the grid above stops describing the product.
//   - `count + edge` — the edge arithmetic. Removing it (to write `count - 1`
//     inline, say) means the two tables no longer describe the behaviour.
//
// These are source-shape assertions and they are labelled as such. They were each
// checked by mutation: dropping either expression fails this test.
func TestTheKeyModelIsResolvedThroughTheTestedArithmetic(t *testing.T) {
	t.Parallel()

	source := strippedSource(tokensSource(t))

	for _, required := range []string{
		"spFocusStep(from, move, count)",
		"count + edge",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("%s does not contain %q. The key tables are read and "+
				"evaluated by this gate, so the wiring that turns a table entry "+
				"into a row index has to be the one this gate evaluated — otherwise "+
				"the tables are data about nothing", tokens.TokensFile, required)
		}
	}
}

// --- Reading the tables out of the file -------------------------------------------

// mustTable parses one declared table or fails the test.
func mustTable(t *testing.T, source, name string) map[string]float64 {
	t.Helper()

	table, err := parseTable(source, name)
	if err != nil {
		t.Fatalf("%s declares no readable %s: %v\nthis is the whole key model, "+
			"and a gate that cannot read it is a gate that checks nothing",
			tokens.TokensFile, name, err)
	}

	return table
}

// parseTable reads `var <name> = { Key: <number>, … }` out of a module.
//
// Only numbers, only flat, and only one declaration: everything else is an error,
// for the same reason the expression grammar is closed. A table whose values were
// expressions over the DOM would be logic this gate cannot evaluate, and a gate
// that silently accepted one would be evaluating less than it claims.
func parseTable(source, name string) (map[string]float64, error) {
	declaration := "var " + name + " = {"

	_, after, ok := strings.Cut(source, declaration)
	if !ok {
		return nil, fmt.Errorf("no %q", declaration)
	}

	body := after

	if strings.Count(body, "}") == 0 {
		return nil, fmt.Errorf("%s is not closed", declaration)
	}

	end := strings.IndexByte(body, '}')

	table := map[string]float64{}

	for entry := range strings.SplitSeq(body[:end], ",") {
		key, valueText, found := strings.Cut(entry, ":")
		if !found {
			return nil, fmt.Errorf("entry %q has no `:`", strings.TrimSpace(entry))
		}

		key = strings.TrimSpace(key)
		if key == "" || !isBareName(key) {
			return nil, fmt.Errorf("entry %q has no plain name", entry)
		}

		value, err := parseStrict(strings.TrimSpace(valueText))
		if err != nil {
			return nil, fmt.Errorf("entry %q: %w", entry, err)
		}

		// Evaluated rather than pattern-matched, because `-1` parses as a sign
		// over a literal rather than as a literal, and a table value the gate
		// cannot compute is a value it cannot check.
		number := value.eval(nil)
		if math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) {
			return nil, fmt.Errorf("entry %q has the value %v, which is not a whole "+
				"number; this gate evaluates constants, and anything else is logic "+
				"it cannot check", entry, number)
		}

		table[key] = number
	}

	if len(table) == 0 {
		return nil, fmt.Errorf("%s is empty", declaration)
	}

	return table, nil
}

// isBareName reports whether text is a plain JavaScript identifier.
func isBareName(text string) bool {
	for index := 0; index < len(text); index++ {
		if index == 0 && !isNameByte(text[index]) {
			return false
		}

		if !isNameByte(text[index]) {
			return false
		}
	}

	return text != ""
}

// sortedKeys returns a table's keys in order, for a comparison that does not
// depend on Go's map iteration order.
func sortedKeys(table map[string]float64) []string {
	keys := make([]string, 0, len(table))

	for key := range table {
		keys = append(keys, key)
	}

	sortStrings(keys)

	return keys
}

// sortStrings is `sort.Strings`, named locally so the reader of `sortedKeys` sees
// why the comparison is ordered.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// equal compares two ordered string slices.
func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}

	return true
}

// strippedSource removes comments and string literals, so a required expression
// cannot be satisfied by a sentence describing it.
//
// Without this, `spFocusStep(from, move, count)` in the module's own header
// comment would satisfy `TestTheKeyModelIsResolvedThroughTheTestedArithmetic` on
// a module that no longer calls the kernel at all — which is precisely the
// "an audit that passes while reading prose" failure this repository keeps
// meeting.
func strippedSource(source string) string {
	var out strings.Builder

	for index := 0; index < len(source); index++ {
		switch {
		case source[index] == '/' && strings.HasPrefix(source[index:], "//"):
			for index < len(source) && source[index] != '\n' {
				index++
			}

			out.WriteByte('\n')

		case source[index] == '/' && strings.HasPrefix(source[index:], "/*"):
			index++

			for index < len(source) && !strings.HasPrefix(source[index:], "*/") {
				index++
			}

			index++

		case source[index] == '"' || source[index] == '\'':
			quote := source[index]
			index++

			for index < len(source) && source[index] != quote {
				index++
			}

			out.WriteString(`""`)

		default:
			out.WriteByte(source[index])
		}
	}

	return out.String()
}
