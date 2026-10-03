package play_test

// D16's rule, the document's independence from anything a device says, and the
// client scripts the page carries.
//
// # What is actually testable about "the document never varies by device"
//
// A server cannot see a device — §3.7 resolves the tier in the browser, from the
// `?ui=` parameter, the `sp_ui` cookie, the user-agent hint and the viewport — so
// there is no input for a route to vary by, and an assertion of the form "the
// document is the same for a TV" would be asserting the absence of an input.
//
// Three claims replace it, and each is a property of the bytes:
//
//  1. **The document carries the whole rule.** `data-tab-defaults` holds every tier
//     the resolver can write, each attached to the tab that is default there. A
//     document with all seven answers has not chosen one, so it cannot have varied
//     by device — and a server that *did* resolve the rule would carry one key
//     where this carries seven, which is what the test looks for.
//  2. **The view model has no field that could carry a device or a preference.**
//     Checked by walking the struct with reflection, so a field added later fails.
//  3. **The rendering is a pure function of the view model.** Two independently
//     built equal view models render identical bytes.
//
// And the rule itself is tested where it lives: `Tiers` partition exactly across
// the tabs, in Go, with no browser involved.
//
// # What the client is *not* allowed to know
//
// The client names no tier. If it did, a tier added to §3.7's resolver and not to
// the client would produce a wrong answer in the browser rather than a missing key
// in the document — and a missing key is the failure the Go test above catches.
// `TestTheClientNamesNoTierSoTheDocumentOwnsTheRule` is that assertion, and it is
// the reason the rule is data rather than a table in a script.

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/web/components/play"
	"github.com/semiplane/semiplane/internal/web/static/js/tokens"
)

// ruleAttribute is the wrapper attribute holding the tier rule.
const ruleAttribute = "data-tab-defaults"

// TestTheTierRulePartitionsEveryTierTheResolverCanWrite is D16 as a table test.
//
// Every tier is in exactly one tab's list. Three states are failures and only
// three:
//
//   - a tier in no list — the document would not know what to select, and the
//     reader gets the server's choice with no error;
//   - a tier in two lists — the client resolves the first in the document's order,
//     so the answer depends on a map's key order rather than on the rule;
//   - a tier that is not a tier — a list entry no resolver can write is dead text
//     that a reader of the record would count as coverage.
func TestTheTierRulePartitionsEveryTierTheResolverCanWrite(t *testing.T) {
	t.Parallel()

	rule := play.DefaultTabDefaults()

	claimed := map[string][]string{}

	for tab, tiers := range rule {
		if len(tiers) == 0 {
			t.Errorf("the rule gives the %q tab no tier at all; a tab nothing is "+
				"ever default for is a tab a reader can only reach by arrowing to "+
				"it", tab)
		}

		for _, tier := range tiers {
			if !slices.Contains(play.Tiers, tier) {
				t.Errorf("the rule names %q as a tier, and it is not one of %v. §3.7's "+
					"resolver cannot write a value the client would look up, so the "+
					"entry is dead text", tier, play.Tiers)
			}

			claimed[tier] = append(claimed[tier], tab)
		}
	}

	for _, tier := range play.Tiers {
		switch owners := claimed[tier]; len(owners) {
		case 0:
			t.Errorf("no tab is the default at %q. D16's rule covers every tier "+
				"§3.7 can write, and a tier with no default is a reader whose "+
				"first visit lands on whatever the server picked", tier)
		case 1:
		default:
			t.Errorf("%q is the default for %v; a tier claimed twice resolves to "+
				"whichever key the JSON object happens to list first, which is a "+
				"property of encoding/json's map handling rather than of D16",
				tier, owners)
		}
	}
}

// TestTheRuleFollowsTheRecord is D16's two halves, stated literally.
//
// `Tokens` on TV and on the phone tiers, `Initiative` on the laptop tiers, and
// **nothing else**. The tiers are named here as well as in the rule so a change to
// either side fails; the alternative is a test that asserts the rule equals itself.
func TestTheRuleFollowsTheRecord(t *testing.T) {
	t.Parallel()

	rule := play.DefaultTabDefaults()

	want := map[string][]string{
		play.TokensTabID:     {"compact", "compact-short", "tv", "tv-wide"},
		play.InitiativeTabID: {"large", "medium", "wide"},
	}

	for tab, tiers := range want {
		got := slices.Clone(rule[tab])
		slices.Sort(got)

		if !slices.Equal(got, tiers) {
			t.Errorf("the rule makes %q the default at %v, want %v. §7.6 says the "+
				"token list is the primary way to change what the map shows on TV "+
				"(dragging is unavailable) and §4.9 says the map is a readout on a "+
				"phone, so those five tiers default to Tokens and the laptop "+
				"tiers default to Initiative", tab, got, tiers)
		}
	}
}

// TestTheRuleIsCarriedWholeAndNoTierIsResolved is claim 1, read out of the served
// document rather than out of the Go value.
//
// The unmarshal is the whole point: it proves the JSON survives templ's attribute
// escaping, so the client's `JSON.parse` gets the same table the Go test above
// checked. An attribute holding `&#34;` that a browser decoded to nothing would
// leave the client with no rule and no error.
func TestTheRuleIsCarriedWholeAndNoTierIsResolved(t *testing.T) {
	t.Parallel()

	parsed := populated(t)

	rail := elementsWith(t, parsed, play.RailChromeHook)
	if len(rail) != 1 {
		t.Fatalf("the document has %d [%s] elements, want 1",
			len(rail), play.RailChromeHook)
	}

	raw, present := attribute(rail[0], ruleAttribute)
	if !present {
		t.Fatalf("the rail carries no %s; D16's rule is server-authored data and "+
			"this is where it rides", ruleAttribute)
	}

	var served map[string][]string

	if err := json.Unmarshal([]byte(raw), &served); err != nil {
		t.Fatalf("the rail's %s is not the JSON the client parses: %q: %v\n"+
			"an unreadable rule leaves the client with no default and no error",
			ruleAttribute, raw, err)
	}

	if !reflect.DeepEqual(served, map[string][]string(play.DefaultTabDefaults())) {
		t.Errorf("the document carries the rule %v, want %v", served, play.DefaultTabDefaults())
	}

	// And the shape that makes the document device-independent: seven keys' worth
	// of tiers, one answer per tier, **none of them chosen**.
	seen := 0

	for _, tiers := range served {
		seen += len(tiers)
	}

	if seen != len(play.Tiers) {
		t.Errorf("the document's rule covers %d tiers, want %d. A document that "+
			"carried fewer has resolved the rule server-side, which is the "+
			"varying-by-device document §13.5 rules out", seen, len(play.Tiers))
	}

	// The document names no tier as *this document's* tier. `data-ui` is the
	// resolver's attribute and this page writes it from JavaScript, so a value
	// here would mean the server had answered the question the browser is meant
	// to answer.
	if value, present := attribute(rail[0], "data-ui"); present {
		t.Errorf("the rail carries data-ui=%q. §3.7 resolves the tier in the "+
			"browser; a server-rendered one is a document that varies by device",
			value)
	}
}

// TestTheDocumentIsByteIdenticalAcrossRenders is claim 3.
//
// Two independently constructed view models, rendered twice each. The comparison
// is over the **bytes**, not over a parsed tree, because the claim is about the
// document — a rendering that is stable as a tree and unstable as bytes (a map
// iteration deciding attribute order, say) is exactly the drift that a `Vary`-free
// response can still hide until a proxy disagrees with itself.
func TestTheDocumentIsByteIdenticalAcrossRenders(t *testing.T) {
	t.Parallel()

	first := renderBytes(t, document(populatedList()))
	second := renderBytes(t, document(populatedList()))

	if first != second {
		t.Errorf("two renderings of the same view model differ: %d and %d bytes. "+
			"§13.5's byte-identity is over the response, so a rendering that is "+
			"stable as a tree and not as bytes is not stable",
			len(first), len(second))
	}
}

// deviceWords are the words a field would use if it carried something the server
// could see about the reader's device or preferences.
var deviceWords = []string{
	"tier", "formfactor", "viewport", "width", "height",
	"preference", "prefs", "remembered", "remember",
	"cookie", "mode", "device", "screen",
}

// TestTheViewModelHasNoFieldThatCouldVaryTheDocument is claim 2.
//
// Reflection rather than a comment, because a comment asserting a struct's shape
// stops being true the moment somebody adds a field and nobody re-reads the
// comment. **Both view models are walked**, and so is the placement: a `Layer` is
// a fact about a placement and a `Tier` would be a fact about a reader, and the
// two are not distinguishable by name alone.
func TestTheViewModelHasNoFieldThatCouldVaryTheDocument(t *testing.T) {
	t.Parallel()

	for _, model := range []any{
		play.RailView{}, play.TokenListView{}, play.PlacementView{}, play.Tab{},
	} {
		value := reflect.TypeOf(model)

		for field := range value.Fields() {
			name := strings.ToLower(field.Name)

			for _, word := range deviceWords {
				if strings.Contains(name, word) {
					t.Errorf("%s has a field %q. A field the server can fill with "+
						"something about the reader's device or preferences is a "+
						"field a route can use to vary the document, and §13.5 and "+
						"ADR 0035 rule that out — the tier is resolved client-side "+
						"by design",
						value.Name(), field.Name)
				}
			}
		}
	}
}

// TestTheRailSelectsTokensWhateverTheCallerAsks is the server half of D16.
//
// The selection is a fixed value and not a field, because a server cannot see a
// device. The test proves it by putting a *different* tab first and last in the
// view: whichever order the route fills, the rendered document selects Tokens.
//
// And when the token panel is not in the rail at all — a campaign whose gameplay
// system has no tokens, or a route that has not mounted it — the widget falls back
// to its own first tab rather than claiming a selection that does not exist. That
// case is asserted too, because it is the one where "Tokens, always" would be a
// lie.
func TestTheRailSelectsTokensWhateverTheCallerAsks(t *testing.T) {
	t.Parallel()

	t.Run("tokens is selected whatever the order", func(t *testing.T) {
		t.Parallel()

		view := railView()
		view.Tabs = []play.Tab{
			{ID: play.InitiativeTabID, Label: "Initiative", Content: otherPanel("Turn order.")},
			{ID: "dice", Label: "Dice", Content: otherPanel("1d20.")},
			play.TokensTab(play.TokenList(populatedList())),
		}

		assertExactlyOneSelectedTab(t, render(t, play.Rail(view)), play.TokensTabID)
	})

	t.Run("the fallback is the first tab, not tokens", func(t *testing.T) {
		t.Parallel()

		view := railView()
		view.Tabs = []play.Tab{
			{ID: play.InitiativeTabID, Label: "Initiative", Content: otherPanel("Turn order.")},
		}

		assertExactlyOneSelectedTab(t, render(t, play.Rail(view)), play.InitiativeTabID)
	})
}

// assertExactlyOneSelectedTab requires exactly one tab to carry
// `aria-selected="true"`, and that it is the named one.
func assertExactlyOneSelectedTab(t *testing.T, parsed *html.Node, want string) {
	t.Helper()

	tabs := elementsWith(t, parsed, "data-tab")

	if len(tabs) == 0 {
		t.Fatal("the rail renders no tab")
	}

	selected := elementsWithValue(t, parsed, "aria-selected", "true")

	if len(selected) != 1 {
		t.Errorf("the rail has %d tabs with aria-selected=\"true\", want exactly 1: "+
			"a tablist with two selected tabs has two answers to what the reader is "+
			"looking at, and one with none shows labels and no content",
			len(selected))
	}

	for _, tab := range selected {
		if got := attributeOr(tab, "data-tab"); got != want {
			t.Errorf("the selected tab is %q, want %q", got, want)
		}
	}

	// Roving tabindex: §10.2's audit forbids a positive value, and the tabs
	// pattern's defining property is that the tablist is one tab stop.
	for _, tab := range tabs {
		value, present := attribute(tab, "tabindex")
		if !present {
			t.Errorf("the tab %q carries no tabindex; the tabs pattern puts 0 on "+
				"the selected tab and -1 on the rest, so the widget is one tab stop",
				attributeOr(tab, "data-tab"))

			continue
		}

		if value != "0" && value != "-1" {
			t.Errorf("the tab %q carries tabindex=%q; §7.4 permits -1 and 0",
				attributeOr(tab, "data-tab"), value)
		}
	}
}

// TestTheClientNamesNoTierSoTheDocumentOwnsTheRule is the claim above, asserted.
//
// A tier name in the client would mean the client has a table of its own, and the
// document's table would be decoration. The failure mode it prevents is specific:
// §3.7 gains a tier, the client's table does not, and the browser silently applies
// the wrong default with nothing to fail.
func TestTheClientNamesNoTierSoTheDocumentOwnsTheRule(t *testing.T) {
	t.Parallel()

	for _, name := range tokens.Names() {
		source := tokens.Source(name)

		for _, tier := range play.Tiers {
			if strings.Contains(source, `"`+tier+`"`) || strings.Contains(source, "'"+tier+"'") {
				t.Errorf("%s names the tier %q as a string. D16's rule is data on "+
					"the document (`%s`), and the client reads it from there: a tier "+
					"the client knows about is a tier that can be known by one half "+
					"of the product and not the other",
					name, tier, ruleAttribute)
			}
		}
	}
}

// TestThePageCarriesItsClientScriptsInOrder is the delivery claim.
//
// One `<script>` element per module, in the order `tokens.Order` gives, because the
// order is a dependency: `tokens.js` calls `spFocusStep`, which `step.js` defines,
// and two classic scripts written the other way round throw `ReferenceError` on the
// first keypress.
func TestThePageCarriesItsClientScriptsInOrder(t *testing.T) {
	t.Parallel()

	rendered := renderBytes(t, play.ClientScripts())

	parsed := render(t, play.ClientScripts())

	if got := len(scriptsOf(t, parsed)); got != len(tokens.Order()) {
		t.Errorf("ClientScripts() renders %d script element(s) for %d modules; one "+
			"per module is the whole arrangement, and a module rendered as two or "+
			"not at all is either half a script or a reference to something that "+
			"was never sent", got, len(tokens.Order()))
	}

	for _, name := range tokens.Order() {
		if !strings.Contains(rendered, tokens.Source(name)) {
			t.Errorf("ClientScripts() omits %s", name)
		}
	}

	// The order, by offset rather than by a comparison of two lists.
	for index := 1; index < len(tokens.Order()); index++ {
		earlier := tokens.Source(tokens.Order()[index-1])
		later := tokens.Source(tokens.Order()[index])

		if strings.Index(rendered, earlier) > strings.Index(rendered, later) {
			t.Errorf("%s is written after %s, and %s calls into the scope %s defines",
				tokens.Order()[index], tokens.Order()[index-1],
				tokens.Order()[index], tokens.Order()[index-1])
		}
	}

	// And the one ordering that is a dependency rather than a preference.
	if strings.Index(rendered, tokens.Source(tokens.StepFile)) >=
		strings.Index(rendered, tokens.Source(tokens.TokensFile)) {
		t.Errorf("%s is written after %s, which calls spFocusStep; two classic "+
			"scripts in the wrong order throw ReferenceError on the first keypress",
			tokens.StepFile, tokens.TokensFile)
	}
}

// TestAnInlineScriptCannotEndItsOwnElement is the guard the writer relies on.
func TestAnInlineScriptCannotEndItsOwnElement(t *testing.T) {
	t.Parallel()

	for _, name := range tokens.Names() {
		source := tokens.Source(name)
		lowered := strings.ToLower(source)

		for _, violation := range []string{"</script", "<!--", "-->"} {
			if strings.Contains(lowered, violation) {
				t.Errorf("%s contains %q, which ends a script element. The body is "+
					"written unescaped, so the rest of it would be parsed as markup",
					name, violation)
			}
		}
	}
}

// renderBytes renders a component to its bytes.
func renderBytes(t *testing.T, component templ.Component) string {
	t.Helper()

	var out strings.Builder

	if err := component.Render(context.Background(), &out); err != nil {
		t.Fatalf("render: %v", err)
	}

	return out.String()
}

// scriptsOf returns every `<script>` element in a document.
func scriptsOf(t *testing.T, parsed *html.Node) []*html.Node {
	t.Helper()

	var found []*html.Node

	walkAll(parsed, func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "script" {
			found = append(found, node)
		}
	})

	return found
}
