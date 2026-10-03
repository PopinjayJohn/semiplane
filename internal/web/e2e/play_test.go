package e2e_test

// The play page, served, with nothing stubbed that the product does not stub.
//
// # What this file is for, and what it is not
//
// Phase 9's other tests each see one layer: the route audits its document, the
// component tests render their own markup, the client modules audit their own
// bytes. **None of them sees whether the page the server actually serves loads the
// modules those layers wrote.** That gap is not hypothetical — it is how this file
// came to exist, and what it found on its first run:
//
//   - `components/play/client.go` wrote the head resolver and the three `tokens`
//     modules, and **nothing from `static/js/live` or `static/js/map`**. So the
//     document mounted all five `data-chrome` patch targets, opened no WebSocket,
//     opened no event stream, and drew no map — and every gate in the repository
//     was green, because each layer's own test passes on its own bytes.
//
// That is the failure this file exists for, and it is the same shape as the
// missing-`@import` failure `AGENTS.md` documents: correct markup, absent
// behaviour, nothing red.
//
// # How it serves the page without a browser
//
// There is **no Node and no browser in `make check`** — deliberately, per the
// toolchain note — so this is not a Playwright script. It stands up the real
// router over the real handler, the real hub and the real 5e engine, fetches the
// document over HTTP, and asserts on the **parsed DOM**.
//
// Parsed, not substring-matched. §7.10's "no `role="application"` anywhere" and
// §3.7's "one blocking script, before the stylesheet" are both claims about
// structure, and this phase has now produced four checks that read raw text and
// reported a violation where there was none. A `<script` inside a comment is
// prose; `golang.org/x/net/html` does not believe it.
//
// Where a claim genuinely cannot be checked without executing JavaScript, it is
// stated here as **not checked here** and asserted against the module's own bytes
// in `static/js/*/source_test.go` instead — which is the arrangement the toolchain
// note describes, not a compromise made here.

import (
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/httpapi/shell"
	weblive "github.com/semiplane/semiplane/internal/web/components/live"
	"github.com/semiplane/semiplane/internal/web/static/js/live"
	"github.com/semiplane/semiplane/internal/web/static/js/tokens"
)

// TestEveryModuleTreeThePageDependsOnIsServed is the test that found the gap.
//
// # The claim
//
// **Every module tree this repository ships under `static/js/` is either served to
// the play page or is deliberately not.** There is no third state, and the third
// state is the one that shipped: a directory of real, tested, audited JavaScript
// that no document references, which is indistinguishable from a feature that does
// not work once the features stop being new.
//
// # Why a directory listing rather than a list
//
// A list would go stale the same way the gap did. `Names()` exists on both module
// packages precisely for a drift test — `tokens`'s own comment says "a directory
// listing rather than `order`, and the distinction is the point: a new `.js` file
// dropped into this directory has to appear in *someone's* audit" — so this reads
// the directories and requires each file to be served.
//
// The map tree is walked by reading the directory rather than by naming its four
// files, because it has no `Order()`/`Names()` accessor and adding one is a change
// to a package this work item does not own. It is named in `moduleTreesRequired`
// with the reason.
//
// # Mutation
//
// Removing one `<script src>` from the document's tail takes exactly one file from
// served to absent and this goes red naming the file and the directory it came
// from. That was measured, not assumed — see the commit message.
func TestEveryModuleTreeThePageDependsOnIsServed(t *testing.T) {
	t.Parallel()

	document := parsePlayDocument(t)

	// Every embedded module, by its own package's accessor, so the list cannot
	// drift from what is embedded.
	for _, tree := range []struct {
		directory string
		order     func() []string
		source    func(string) string
	}{
		{"tokens", tokens.Order, tokens.Source},
		{"live", live.Order, live.Source},
	} {
		for _, name := range tree.order() {
			module := tree.source(name)

			if !strings.Contains(document.body, module) &&
				!servedByHref(document, tree.directory, name) {
				t.Errorf("the play page serves no %s/%s. It is embedded, it is "+
					"audited by %s's own tests, and nothing on the page loads it, "+
					"so the behaviour it implements is absent rather than degraded",
					tree.directory, name, tree.directory)
			}
		}
	}
}

// moduleTreesRequired names the directories under `static/js/` that must appear on
// the play page, and why.
//
// **A list of directories, not of files**, so a new module in a listed tree is
// covered automatically and a new *tree* is a decision somebody makes here.
var moduleTreesRequired = []struct {
	directory string
	why       string
}{
	{
		"tokens",
		"UI §7.6: the token list is the accessibility source of truth for the table",
	},
	{
		"live",
		"UI §7.5: the sidebar's five regions and the two connections",
	},
	{
		"map",
		"UI §7.6: the canvas is the visual mirror of the token list",
	},
}

// TestTheTabletopOpensExactlyTwoConnectionsAndNoOthers is §7's ceiling and §7.5's
// second Task, counted over the served document.
//
// Two and not four: the WebSocket carries structured state to the canvas, and the
// event stream carries rendered fragments to the sidebar. Architecture §7 puts the
// ceiling at about six, and this asserts the page uses two — which is a *budget*,
// and a budget nobody measures is a budget nobody holds.
//
// Counted over the parsed DOM and matched on the attribute that **acts**: a
// `data-init` that opens a stream, and an element carrying the socket's URL. An
// element that merely mentions `/events` in prose is not a connection, and §10.2's
// vocabulary audit exists because prose is not markup.
func TestTheTabletopOpensExactlyTwoConnectionsAndNoOthers(t *testing.T) {
	t.Parallel()

	document := parsePlayDocument(t)

	var (
		streamOpeners []string
		socketHooks   []string
		otherOpeners  []string
	)

	eachElement(document.root, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		if init := attrOf(node, "data-init"); init != "" {
			switch {
			case strings.Contains(init, "/events"):
				streamOpeners = append(streamOpeners, init)
			default:
				otherOpeners = append(otherOpeners, init)
			}
		}

		if attrOf(node, weblive.ChromeAttribute) == weblive.WebSocketHook {
			socketHooks = append(socketHooks, attrOf(node, "data-socket"))
		}
	})

	if len(streamOpeners) != 1 {
		t.Errorf("the play page carries %d elements whose data-init opens the event "+
			"stream, want exactly 1 (UI §7.5: one WS and one SSE)", len(streamOpeners))
	}

	if len(socketHooks) != 1 {
		t.Errorf("the play page carries %d elements with %s=%q, want exactly 1: a "+
			"socket is a standing capability and a second one is a second writer",
			len(socketHooks), weblive.ChromeAttribute, weblive.WebSocketHook)
	}

	if len(otherOpeners) != 0 {
		t.Errorf("the play page carries %d further data-init openers (%v). Each one "+
			"is a connection this page did not budget for, and §7's ceiling is the "+
			"reason there are two rather than four",
			len(otherOpeners), otherOpeners)
	}
}

// TestThePlayPageSatisfiesTheShellContract delegates to the shell's own auditor
// rather than re-deriving it, and the delegation is the interesting part.
//
// # Why this is not a second implementation
//
// The first version of this file counted blocking scripts and located the
// resolver by searching its text for `data-ui` or `data-theme`. It failed on a
// correct document, and `internal/httpapi/shell/audit.go` says why in its own
// comment: *"the resolver's own source contains the text `data-ui` and
// `data-theme`, so a substring test over the response either fails on a correct
// document or has to carve out the script — and a carve-out is exactly where a
// `data-ui` hidden inside an HTML comment goes unnoticed."*
//
// It also walked the tree where that one walks in document order, and it guessed
// where the existing auditor **knows**. So this file's version was a second answer
// to a question the repository already answers, and a worse one — which is the
// shape of defect this phase has produced four times over, in tests rather than
// in features. Deleted, and the existing auditor called instead.
//
// # Why the play page needs it at all
//
// **The play page writes its own `<head>`.** `shell/head_test.go` audits the
// shell's documents; a route that renders `<html>` itself can drift from §3.7 —
// drop the resolver, move it after the stylesheet, resolve the tier server-side —
// and nothing in that file sees it. This is the only place the shell contract is
// checked on a document the shell did not write.
func TestThePlayPageSatisfiesTheShellContract(t *testing.T) {
	t.Parallel()

	document := parsePlayDocument(t)
	audit := shell.AuditDocument(document.body)

	for _, fault := range audit.Faults {
		t.Errorf("the play page violates the shell contract: %s. The play page "+
			"renders its own <head>, so `shell/head_test.go` does not see it and "+
			"this is the only place the contract is checked here", fault.What)
	}

	// Stated rather than inferred, because every one of these is a boolean the
	// auditor computed and a future edit could quietly stop computing.
	for name, holds := range map[string]bool{
		"HasResolver":          audit.HasResolver,
		"ResolverInHead":       audit.ResolverInHead,
		"ResolverBeforeStyles": audit.ResolverBeforeStyles,
		"HasSheetScript":       audit.HasSheetScript,
		"HasColorSchemeMeta":   audit.HasColorSchemeMeta,
	} {
		if !holds {
			t.Errorf("the play page does not satisfy %s. §3.7 resolves data-theme "+
				"and data-ui from one blocking script before the stylesheet link, "+
				"and ADR 0035 is the record of why there is one script and not two",
				name)
		}
	}
}

// TestThePlayPageAddsNoProhibitedConstruct is §7.10's list, over the parsed DOM,
// and it is here because a *client* can add things a server audit cannot see.
//
// The whole of §7.10 that a document can carry:
//
//   - no `role="application"`;
//   - no positive `tabindex`; and
//   - no `aria-hidden` on a focusable element.
//
// The client half — that the shipped JavaScript does not inject an element
// carrying `role="application"`, nor call `setAttribute("tabindex", "1")` — cannot
// be checked without executing JavaScript, and is asserted against the module's
// bytes in `static/js/live/source_test.go`. **Saying which half lives where is the
// point**: a reader auditing this file should not assume the client's half is
// covered because this file exists.
func TestThePlayPageAddsNoProhibitedConstruct(t *testing.T) {
	t.Parallel()

	document := parsePlayDocument(t)

	focusable := map[string]bool{
		"a": true, "button": true, "input": true, "select": true,
		"textarea": true, "summary": true,
	}

	eachElement(document.root, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		if role := attrOf(node, "role"); role == "application" {
			t.Errorf("the play page carries role=\"application\" on a <%s>. §7.10 "+
				"prohibits it outright: it makes a screen reader hand the keyboard "+
				"to the page and stop announcing, which is the opposite of the "+
				"contract §7.6 builds the token list to satisfy", node.Data)
		}

		if value := attrOf(node, "tabindex"); value != "" {
			if number, err := strconv.Atoi(value); err == nil && number > 0 {
				t.Errorf("the play page carries tabindex=%q on a <%s>. §7.10 "+
					"prohibits a positive tabindex: it makes the tab order differ "+
					"from the DOM order with nothing on screen to explain it",
					value, node.Data)
			}
		}

		if hidden := attrOf(node, "aria-hidden"); hidden == "true" &&
			(focusable[node.Data] || hasAttr(node, "tabindex")) {
			t.Errorf("the play page carries aria-hidden=\"true\" on a focusable "+
				"<%s>. §7.10 prohibits it, and the reason is mechanical: a reader "+
				"can focus an element it is not told about, and the two facts "+
				"together are a control that exists and does not", node.Data)
		}
	})
}

// TestEveryModuleTreeIsAccountedFor is the drift guard, and it is the reason the
// test above can be trusted to stay true.
//
// **It fails when a directory appears under `static/js/` that no test claims.**
// Without it, adding `static/js/inventory/` tomorrow produces a fourth tree that
// nothing serves and nothing notices — the same gap, one release later, with the
// test that would have caught it sitting here having quietly stopped covering the
// world.
func TestEveryModuleTreeIsAccountedFor(t *testing.T) {
	t.Parallel()

	present := moduleDirectories(t)

	claimed := map[string]bool{}

	for _, tree := range moduleTreesRequired {
		claimed[tree.directory] = true

		if !present[tree.directory] {
			t.Errorf("%s/%s is required by this test but the directory does not "+
				"exist. A required tree that has been renamed or removed is a claim "+
				"about the world that stopped being true, and leaving it here would "+
				"be a test asserting the absence of something it never checks",
				live.AssetPrefix, tree.directory)
		}
	}

	for directory := range present {
		if !claimed[directory] {
			t.Errorf("static/js/%s exists and no test claims it. Either the play "+
				"page should serve it, in which case add it to "+
				"moduleTreesRequired, or it should not exist, in which case "+
				"deleting it is better than shipping an unserved module",
				directory)
		}
	}
}

// moduleDirectories is every directory under `static/js/`, read from disk.
//
// `os.ReadDir` rather than an embedded listing, because the claim is about the
// working tree: a module that exists on disk and is not embedded is a different
// failure from one that is embedded and not served, and conflating them would
// make this report the wrong one.
func moduleDirectories(t *testing.T) map[string]bool {
	t.Helper()

	entries, err := readDirSorted(staticJSRoot)
	if err != nil {
		t.Fatalf("read %s: %v", staticJSRoot, err)
	}

	found := map[string]bool{}

	for _, entry := range entries {
		if entry.IsDir() {
			found[entry.Name()] = true
		}
	}

	return found
}

// servedByHref reports whether the document carries a `<script src>` or `<link href>`
// naming the module at its own asset URL.
//
// The alternative to inlining, and one this file needs because **two of the three
// trees are ES modules**: `import` and `export` are illegal in a classic inline
// script, so `live/` and `map/` cannot be written the way `tokens/` is and must be
// referenced by URL. Both arrangements are correct; what is not correct is a tree
// that is neither.
func servedByHref(document servedDocument, directory, name string) bool {
	wanted := "/assets/js/" + directory + "/" + name

	for _, reference := range document.references {
		if strings.Contains(reference, wanted) {
			return true
		}
	}

	return false
}
