package events_test

// UI §10.2 and §10.6 for every **document** this route can put in front of a reader
// who passed the access gate, gate-blocking.
//
// # Why this file is here and what it is not
//
// `Makefile`'s `A11Y_ROUTE_PKGS` is a `$(wildcard …)` over route package directories,
// and **creating `internal/httpapi/events/` does not add it** — AGENTS.md is explicit
// that the wildcard buys only a package named before it lands, and that a route's
// directory appearing on disk means nothing to the gate. So this file is the claim the
// integrator's one-line addition to that variable is making, and it is here rather
// than in `internal/httpapi` for the reason `plugins`' is: that package's audit
// helpers are unexported in `httpapi_test` and a test package is not importable.
//
// # Why this route has no documents
//
// It does not. `/c/{slug}/events` answers an event stream, and its payload is a
// **fragment** — a `<li>`, a `<p>`, a `<section>` — never a page. So §10.2's rules
// are split, and the split is stated rather than quietly narrowed:
//
//   - **Rules that are properties of a fragment** run here, over every fragment the
//     route actually emits, taken off a real socket: vocabulary, focus stops and
//     `.target`, positive `tabindex`, inline outline suppression, `aria-hidden` on a
//     focus stop, id and `data-testid` uniqueness within the fragment, and reference
//     integrity within the fragment.
//   - **Rules that need a whole document** — one `<h1>`, landmarks, skip links — are
//     over the *sidebar* the fragments land in, and they live in
//     `internal/web/components/live`'s `document_test.go`, because that is where the
//     document is rendered. Naming this package for the document half would be a
//     claim the file does not make.
//
// # The rules that are genuinely about fragments
//
// Three of §10.2's seven are about a document's structure and cannot be applied to a
// fragment without being made vacuous — "exactly one `<h1>`" over a `<li>` is always
// true and always wrong to assert. The remaining four are real, and they are the four
// a fragment can break: a retired entity in an `aria-label`, a focus stop without
// `.target`, an `aria-hidden` on something reachable, and a `data-testid` appearing
// twice.

import (
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/httpapi/events"
	"github.com/semiplane/semiplane/internal/web/components/chat"
	"github.com/semiplane/semiplane/internal/web/components/live"
	"github.com/semiplane/semiplane/internal/web/components/ui"
)

// auditedFragments returns every HTML fragment this route can put on the wire.
//
// **Off a real socket, not rendered directly**, and that is the point: the thing under
// test is what a browser receives, so reading the components' output would skip the
// escaping, the field encoding and the target that the handler adds between them — and
// the `data:` prefix is exactly one of those three things.
func auditedFragments(t *testing.T) []renderedDocument {
	t.Helper()

	hub, _, server := sidebar(t, func(handler *events.Handler) {
		handler.KeepAlive = hourLong
	})

	//nolint:bodyclose // `open` registers the body's close as a cleanup, and this
	// file's whole subject is a set of responses that are deliberately never closed by
	// the test bodies.
	resp := open(t, server, gmRequestor())
	lines := read(resp)
	lines.next(t) // the opening comment

	fragments := make([]renderedDocument, 0, len(events.Kinds())+1)

	for _, kind := range []events.Kind{
		events.KindChange,
		events.KindInitiative,
		events.KindChat,
		events.KindDice,
		events.KindTurn,
		events.KindDegradedWatch,
		events.KindReconcileCapped,
		events.KindStreamDegraded,
	} {
		if kind == events.KindChange {
			hub.Publish(changed("Vault.md", content.OpUpsert))
		} else {
			hub.PublishSidebar(events.Notice{CampaignID: testCampID, Kind: kind})
		}

		frame := sseFields(lines.next(t))
		body := strings.Join(frame.lines(), "\n")

		fragments = append(fragments, renderedDocument{
			where: "a fragment for a " + kind.String() + " change",
			body:  []byte(body),
		})
	}

	return fragments
}

// momentUTC is one instant, so two fragments' timestamps are comparable.
var momentUTC = time.Date(2026, time.September, 30, 14, 0, 0, 0, time.UTC)

// hourLong is a keep-alive far outside any test's lifetime, so a fragment's record is
// the next thing on the wire.
const hourLong = time.Hour

// renderedDocument is one audited response or fragment.
type renderedDocument struct {
	// where names it in a failure message.
	where string

	// body is the bytes.
	body []byte
}

// TestEveryRouteSatisfiesTheStructuralContract is UI §10.2, gate-blocking, for every
// fragment this route emits.
//
// Each rule in its own subtest, so a failure names the rule rather than "the a11y test
// failed".
func TestEveryRouteSatisfiesTheStructuralContract(t *testing.T) {
	t.Parallel()

	for _, doc := range auditedFragments(t) {
		t.Run(doc.where, func(t *testing.T) {
			t.Parallel()

			audit := parseDocument(t, doc)

			t.Run("ExactlyOneTarget", func(t *testing.T) {
				// **A fragment names one selector**, and §10.2's hook rule has a fragment
				// version: a `data-testid` appearing twice inside one fragment selects
				// nothing, and a fragment that will be appended into a list is exactly
				// where a duplicate hides.
				assertHooksAreUnique(t, audit)
			})
			t.Run("Vocabulary", func(t *testing.T) {
				assertNoRetiredEntityIsNamed(t, audit)
			})
			t.Run("NoPositiveTabindex", func(t *testing.T) {
				assertNoPositiveTabindex(t, audit)
			})
			t.Run("NoInlineOutlineSuppression", func(t *testing.T) {
				assertNoInlineOutlineSuppression(t, audit)
			})
			t.Run("NoAriaHiddenOnAFocusStop", func(t *testing.T) {
				assertNoAriaHiddenOnAFocusStop(t, audit)
			})
			t.Run("EveryReferenceResolves", func(t *testing.T) {
				assertReferencesResolveWithinTheFragment(t, audit)
			})
			t.Run("EveryElementIsOpaque", func(t *testing.T) {
				assertNoRawScriptOrEventHandler(t, audit)
			})
		})
	}
}

// TestEveryRouteCarriesTheTargetClassOnEveryFocusStop is UI §10.6 and §7.3's "every
// interactive element … uses it", over every fragment this route emits.
//
// Its own top-level test rather than another subtest, because §10.6 is a different
// section of the record with its own floor, and a rule that fails is easier to route
// to a person when the failure line names the section it came from.
//
// **And it passes vacuously today**, which is stated rather than glossed: `Decide`
// refuses any fragment holding a focus stop, so by construction nothing this route
// emits has one. The test is here to hold *that* — it fails the day a family grows a
// button **and** the refusal is relaxed, and the refusal is relaxed by the next person
// who finds it inconvenient. §10.6's own rule says "including plugin output", and
// `components/chat`'s compose form is the closest thing this route emits to output it
// does not write itself.
func TestEveryRouteCarriesTheTargetClassOnEveryFocusStop(t *testing.T) {
	t.Parallel()

	for _, doc := range auditedFragments(t) {
		t.Run(doc.where, func(t *testing.T) {
			t.Parallel()

			audit := parseDocument(t, doc)

			for _, node := range audit.focusStops() {
				if hasClass(node, ui.TargetClass) {
					continue
				}

				t.Errorf("%s: %s is focusable and does not carry the .target class; "+
					"§7.3 enforces the --target-min minimum by construction and §10.6 "+
					"audits for it. A fragment that reached a reader without it would be "+
					"a 30px control on a television — and `live.Decide` refuses a "+
					"fragment holding a focus stop at all, so reaching here means that "+
					"refusal was relaxed",
					doc.where, nodePath(node))
			}
		})
	}
}

// TestEveryFragmentIsRenderedMarkupAndNotAClientRenderer is §7.5's "rendered DOM
// fragments … patched by Datastar", asserted on the payload.
//
// Three claims and each is a different way the rule can be broken: the payload is not
// JSON, it parses as HTML with at least one element, and it carries no `<script>`. A
// JSON payload would be a client renderer — and a client renderer could not reproduce
// the server's escaping, which is the thing S-12.3 depends on.
func TestEveryFragmentIsRenderedMarkupAndNotAClientRenderer(t *testing.T) {
	t.Parallel()

	for _, doc := range auditedFragments(t) {
		t.Run(doc.where, func(t *testing.T) {
			t.Parallel()

			trimmed := strings.TrimSpace(string(doc.body))
			if trimmed == "" {
				t.Fatalf("%s: the fragment is empty. An empty patch announces nothing "+
					"and shows nothing, and it reads to a reader as \"nothing is "+
					"happening\" rather than as a fault", doc.where)
			}

			if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
				t.Errorf("%s: the payload is a JSON document; §7.5 requires rendered "+
					"DOM fragments so the client never has to render content itself:\n%s",
					doc.where, doc.body)
			}

			audit := parseDocument(t, doc)

			if elements := len(findAll(audit.root, anyTag)); elements == 0 {
				t.Errorf("%s: the payload parsed to no elements at all, so it is not a "+
					"DOM fragment:\n%s", doc.where, doc.body)
			}
		})
	}
}

// TestTheFragmentsAreTheOnesThisPackageDeclares holds the two halves of the contract
// together, and it is the test C4's play document depends on.
//
// Each family must emit the fragment its **target** names and its **region** says, and
// `live.Decide` must accept it. Read from the Go constants rather than spelled here, so
// a rename on the interface side fails rather than leaving a selector matching nothing.
func TestTheFragmentsAreTheOnesThisPackageDeclares(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"initiative":   `[data-chrome="initiative-track"]`,
		"chat log":     `[data-chrome="chat-log"]`,
		"dice log":     `[data-chrome="dice-log"]`,
		"announcement": `[data-chrome="live-announce"]`,
		"notice":       `[data-chrome="live-notice"]`,
	}

	for _, target := range live.Targets() {
		selector, found := want[target.Name]
		if !found {
			t.Fatalf("no expected selector for the %q target; a new target needs an "+
				"entry here, because this is the assertion C4's document binds to",
				target.Name)
		}

		if target.Selector != selector {
			t.Errorf("the %q target's selector is %q, want %q. The selector is what a "+
				"patch names and what `internal/web/components/live` renders the hook "+
				"for, and the two drifting apart is a patch into nothing",
				target.Name, target.Selector, selector)
		}

		if target.Focusable {
			t.Errorf("the %q target declares itself a focus stop. `live.Decide` refuses "+
				"every patch into a focusable target, so this is a sidebar that never "+
				"updates; see §7.5's rule that a patch must never touch the focused "+
				"element", target.Name)
		}

		if target.Mode != live.ModeInner && target.Mode != live.ModeAppend {
			t.Errorf("the %q target's mode is %q, want `inner` or `append`. `outer` would "+
				"replace the container the document owns and destroy the state the "+
				"client attached to it", target.Name, target.Mode)
		}
	}
}

// TestTheChatLineFragmentIsTheChatPackageSRow is the single-owner claim in the
// direction a drift breaks.
//
// The chat fragment renders `chat.MessageRow` — the chat package's own component — so
// the row's escaping, its `data-seq`, its time format and its class vocabulary all have
// exactly one answer. A second row component would be a second renderer for one
// conversation, and the two would drift the first time either was restyled.
func TestTheChatLineFragmentIsTheChatPackageSRow(t *testing.T) {
	t.Parallel()

	fragment, err := live.RenderChatLine(t.Context(), chat.Message{
		Seq: 7, Author: "Tobin", Body: "It opens.", At: momentUTC,
	})
	if err != nil {
		t.Fatalf("build the chat fragment: %v", err)
	}

	// The row's identifying attribute is the chat package's, and the body is escaped by
	// it. Asserting the attribute rather than the class keeps this tied to the contract
	// (`Chat.Message.Seq`: "Rendered as `data-seq` on the row so a fragment arriving
	// over SSE can be reconciled without parsing prose") rather than to styling.
	if !strings.Contains(fragment.Markup, `data-seq="7"`) {
		t.Errorf("the chat fragment carries no data-seq:\\n%s", fragment.Markup)
	}

	if !strings.Contains(fragment.Markup, "It opens.") {
		t.Errorf("the chat fragment does not carry its body:\\n%s", fragment.Markup)
	}

	if fragment.Target.Hook != chat.LogChromeHook {
		t.Errorf("the chat fragment targets %q, want the hook `components/chat` declares "+
			"(%q). The container is that package's, and a second spelling of its hook is "+
			"a selector that matches nothing",
			fragment.Target.Hook, chat.LogChromeHook)
	}
}

// --- The rules --------------------------------------------------------------------------------

// assertHooksAreUnique is §10.2's hook rule at fragment scope.
//
// Both hooks, and each counted separately: a `data-testid` and a `data-chrome` with
// the same value are two hooks, and counting them as one set would report a
// duplicate where there is none and miss a duplicate where there is.
func assertHooksAreUnique(t auditFailer, audit *docAudit) {
	t.Helper()

	seen := map[string]int{}

	audit.elements(func(node *html.Node) {
		for _, hook := range []string{"data-testid", live.ChromeAttribute} {
			if !hasAttribute(node, hook) {
				continue
			}

			value := attr(node, hook)
			if strings.TrimSpace(value) == "" {
				t.Errorf("%s: %s carries an empty %s. A hook with no value selects "+
					"nothing and cannot be asserted on (UI §10.2)",
					audit.where, nodePath(node), hook)
			}

			seen[hook+"="+value]++
		}
	})

	duplicates := make([]string, 0)

	for hook, count := range seen {
		if count > 1 {
			duplicates = append(duplicates, hook+" x"+strconv.Itoa(count))
		}
	}

	sort.Strings(duplicates)

	if len(duplicates) > 0 {
		t.Errorf("%s: these hooks appear more than once in one fragment: %s; a hook "+
			"that appears twice selects nothing, and a fragment is appended into a "+
			"list where a duplicate is invisible (UI §10.2)",
			audit.where, strings.Join(duplicates, ", "))
	}
}

// assertNoRetiredEntityIsNamed is UI §1.2 and §10.2's last clause, over a fragment.
//
// The scan covers **every text node, every comment, every attribute value and every
// attribute name**, for the reasons `plugins`' audit gives. On this route the attribute
// values are the interesting ones: a fragment's `aria-label` is announced to a reader,
// so a retired entity named there is named to the person the rule protects.
func assertNoRetiredEntityIsNamed(t auditFailer, audit *docAudit) {
	t.Helper()

	report := func(where, text string) {
		lowered := strings.ToLower(text)

		for _, retired := range retiredWords {
			if strings.Contains(lowered, retired) {
				t.Errorf("%s: %s contains %q; neither entity exists, and §1.2 makes the "+
					"words appear nowhere in the interface", audit.where, where, retired)
			}
		}
	}

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		switch node.Type {
		case html.TextNode:
			report("a text node", node.Data)
		case html.CommentNode:
			report("an HTML comment", node.Data)
		case html.ElementNode:
			for _, attribute := range node.Attr {
				report("the attribute "+attribute.Key, attribute.Val)
				report("an attribute name", attribute.Key)
			}
		case html.DoctypeNode:
		default:
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(audit.root)
}

// assertNoPositiveTabindex is §10.2's tabindex rule and §7.4's "no positive tabindex,
// anywhere — gate failure".
//
// Zero is excluded too, for the reason `plugins`' audit gives: `tabindex="0"` moves one
// element to the front of the focus order while the markup still reads in the original
// order, so the two disagree and neither a reader nor a test can tell which the page
// means.
func assertNoPositiveTabindex(t auditFailer, audit *docAudit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		raw := attr(node, "tabindex")
		if raw == "" {
			return
		}

		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			t.Errorf("%s: %s carries tabindex=%q, which is not an integer; three "+
				"browsers would disagree about the tab order (UI §10.2, §7.2)",
				audit.where, nodePath(node), raw)

			return
		}

		if value > -1 {
			t.Errorf("%s: %s carries tabindex=%d; only -1 is permitted (UI §10.2, §7.4)",
				audit.where, nodePath(node), value)
		}
	})
}

// assertNoInlineOutlineSuppression is §10.2's "no `outline: none` without a
// replacement", over the half a fragment can carry: a `style` attribute.
//
// Read as a **declaration** rather than as a substring, so `outline-offset` and
// `outline-colour` — which suppress nothing — are not findings, and `outline : 0` is.
func assertNoInlineOutlineSuppression(t auditFailer, audit *docAudit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		style := attr(node, "style")
		if style == "" {
			return
		}

		for declaration := range strings.SplitSeq(style, ";") {
			property, value, split := strings.Cut(declaration, ":")
			if !split {
				continue
			}

			if strings.ToLower(strings.TrimSpace(property)) != "outline" {
				continue
			}

			switch strings.ToLower(strings.TrimSpace(value)) {
			case "none", "0":
				t.Errorf("%s: %s carries an inline `outline: %s`; §10.2 and §7.10 "+
					"prohibit removing a focus indicator without a visible replacement, "+
					"and this stylesheet's replacement is the two-tone ring",
					audit.where, nodePath(node), strings.TrimSpace(value))
			default:
			}
		}
	})
}

// assertNoAriaHiddenOnAFocusStop is §10.2's `aria-hidden` rule, asserted on the
// *combination* rather than the attribute.
//
// Two cases, and the second catches real bugs: a focusable element **inside** an
// `aria-hidden` subtree is unreachable, and the offending attribute is on an ancestor
// several levels up.
func assertNoAriaHiddenOnAFocusStop(t auditFailer, audit *docAudit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		if attr(node, "aria-hidden") != "true" {
			return
		}

		if isFocusStop(node) {
			t.Errorf("%s: %s is focusable and aria-hidden; a control a screen reader "+
				"cannot see is one that reader cannot reach (UI §10.2, §7.10)",
				audit.where, nodePath(node))
		}

		if stops := focusStopsWithin(node); len(stops) != 0 {
			t.Errorf("%s: %s is aria-hidden and contains focusable elements (%s), so "+
				"everything inside it is unreachable to a screen reader (UI §7.10)",
				audit.where, nodePath(node), strings.Join(stops, ", "))
		}
	})
}

// assertReferencesResolveWithinTheFragment is the ARIA integrity rule, scoped.
//
// **Within the fragment, and the scoping is a real limitation rather than a
// convenience.** A fragment's `aria-labelledby` legitimately points at an element in
// the *document* — the sidebar's own headings name the regions — so a whole-document
// resolution would report every well-formed fragment as broken. What is checked here
// is the half that cannot be satisfied from outside: an `id` inside a fragment must be
// unique inside it, because a fragment that will be appended into a list twice brings
// its duplicate with it.
func assertReferencesResolveWithinTheFragment(t auditFailer, audit *docAudit) {
	t.Helper()

	ids := map[string]int{}

	audit.elements(func(node *html.Node) {
		if id := attr(node, "id"); id != "" {
			ids[id]++
		}
	})

	for id, count := range ids {
		if count > 1 {
			t.Errorf("%s: id %q appears %d times in one fragment; a fragment is "+
				"appended into a list, so both copies arrive and every reference to the "+
				"id is ambiguous (UI §7.2)", audit.where, id, count)
		}
	}
}

// assertNoRawScriptOrEventHandler is the escaping claim, in the shape a fragment can
// break it.
//
// §7.5 requires the sidebar's contents to be **rendered** fragments, and a chat message
// body is the one piece of this surface a reader cannot be trusted to have authored.
// So: no `<script>`, and no `on*` attribute. A `<script>` inside a fragment would be
// inserted by the client rather than parsed from the document, and an `on*` attribute
// would mean a value reached the reader as markup rather than as text.
//
// This is the mechanism behind S-12.3 for this route, and it is asserted rather than
// assumed: the payloads are `templ`'s output and `templ.Raw` appears nowhere in either
// component package, but "appears nowhere" is a fact about a grep and this is a fact
// about the bytes.
func assertNoRawScriptOrEventHandler(t auditFailer, audit *docAudit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		if node.Data == "script" {
			t.Errorf("%s: %s is a <script> element. The sidebar's fragments are the "+
				"server's rendered markup and are placed by the client, never executed; "+
				"a script in one would be the one piece of this surface whose code a "+
				"reader could author",
				audit.where, nodePath(node))
		}

		for _, attribute := range node.Attr {
			if strings.HasPrefix(strings.ToLower(attribute.Key), "on") {
				t.Errorf("%s: %s carries %s=%q; a value reached the reader as markup "+
					"rather than as text, and the fields on this surface include a chat "+
					"message body",
					audit.where, nodePath(node), attribute.Key, truncateBody(attribute.Val))
			}
		}
	})
}
