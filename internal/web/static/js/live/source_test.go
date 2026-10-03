package live_test

// The live chrome's client source audits.
//
// # Why audits over source at all
//
// `gap.js` is *evaluated* by `gap_arith_test.go`, and nothing else in this tree can
// be evaluated without a browser, which this repository does not have in CI by
// design. So the remaining claims are made against the **shipped bytes** — not a
// copy, not a summary, the files a browser would fetch.
//
// Two properties make these more than greps.
//
//  1. **Comments are blanked first, preserving offsets.** A banned word or a banned
//     constructor inside a comment explaining why it is banned is prose, and an audit
//     reading raw text reports it as a violation — a loud and *wrong* failure, which
//     is worse than none, because the next real one gets ignored.
//  2. **Every audit is paired with a mutation.**
//     `TestEveryLiveSourceAuditRejectsItsViolation` applies each violation to a copy
//     of the source in memory and requires the audit to object. An audit nobody can
//     fail is not an audit, and three of phase 5's first gate tests did not fail.
//
// # What these audits cannot do
//
// They read text. They can prove that `chrome.js` constructs exactly one WebSocket
// and never touches `innerHTML`; they cannot prove what a socket did when it opened.
// UI §10.3–§10.7 remain agent-assisted for that reason, and every finding from them
// becomes a committed test here rather than a comment.

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	// The components package is imported under a second name because this test is
	// about the two trees agreeing, and a file that could reach either one as `live`
	// could not say which it meant. `chrome` is the interface side: it declares the
	// hooks this source reads.
	chrome "github.com/semiplane/semiplane/internal/web/components/live"
	"github.com/semiplane/semiplane/internal/web/static/js/live"
)

// liveSources are the client files, relative to this test's directory.
var liveSources = []string{live.GapFile, live.ChromeFile}

// readSource reads one file with its comments blanked.
func readSource(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}

	return blankComments(string(raw))
}

// blankComments replaces every comment with spaces, keeping offsets so a diagnostic
// can still name a line.
//
// A block comment ends at the first `*/` and a line comment at the first newline.
// Neither nests in JavaScript, and neither appears inside a string literal in this
// tree, which `TestTheBlankCommentsDoNotMisreadAString` checks rather than assumes.
func blankComments(source string) string {
	out := []byte(source)
	quote := byte(0)

	for index := 0; index < len(source); index++ {
		char := source[index]

		if quote != 0 {
			switch char {
			case '\\':
				index++
			case quote:
				quote = 0
			}

			continue
		}

		switch {
		case char == '\'' || char == '"' || char == '`':
			quote = char

		case char == '/' && index+1 < len(source) && source[index+1] == '/':
			for ; index < len(source) && source[index] != '\n'; index++ {
				out[index] = ' '
			}

		case char == '/' && index+1 < len(source) && source[index+1] == '*':
			for end := index + 2; end+1 < len(source); end++ {
				if source[end] == '*' && source[end+1] == '/' {
					for blank := index; blank <= end+1; blank++ {
						if out[blank] != '\n' {
							out[blank] = ' '
						}
					}

					index = end + 1

					break
				}
			}
		}
	}

	return string(out)
}

// TestTheBlankCommentsDoNotMisreadAString is the assumption `blankComments` rests
// on, asserted rather than trusted.
//
// A `//` inside a string is *not* a comment, and a blanker that believed otherwise
// would swallow the rest of the file — which reads as a source file that suddenly
// fails five audits for no reason a reader could find.
func TestTheBlankCommentsDoNotMisreadAString(t *testing.T) {
	t.Parallel()

	const tricky = `const url = "http://example.invalid/a//b";
const marker = 'still here';
/* a block comment */
const after = 'also here';`

	blanked := blankComments(tricky)

	for _, wanted := range []string{
		`const url = "http://example.invalid/a//b";`,
		`const marker = 'still here';`, `const after = 'also here';`,
	} {
		if !strings.Contains(blanked, wanted) {
			t.Errorf("blanking the comments removed %q. A `//` inside a string is part "+
				"of the string, and a blanker that cannot tell the difference silently "+
				"deletes code", wanted)
		}
	}

	if strings.Contains(blanked, "a block comment") {
		t.Error("a block comment survived blanking")
	}
}

// --- The served modules are the checked-in modules --------------------------------

// TestTheServedModulesAreTheCheckedInModules holds the embed and the file together.
//
// **From disk, not from the embedded copy**, so the comparison is of two artefacts
// rather than a tautology: `live.Source` would be compared with itself.
func TestTheServedModulesAreTheCheckedInModules(t *testing.T) {
	t.Parallel()

	for _, name := range live.Order() {
		// **The raw bytes, not the comment-blanked ones.** This test compares the file
		// to the embed; the blanking exists so the audits read code rather than prose,
		// and applying it here would compare a transformed source with an untransformed
		// copy and fail forever.
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}

		onDisk := string(raw)
		served := live.Source(name)

		if onDisk != served {
			t.Errorf("%s on disk and the copy embedded in the binary differ.\n"+
				"A person edits the file; the browser runs the embedded bytes, so a "+
				"file that was not re-generated is a behaviour change that never shipped "+
				"and no other test can see", name)
		}
	}
}

// TestEveryModuleInTheDirectoryIsInTheOrdering is the drift guard.
//
// A file dropped into this directory and not added to `Order` is a module no page
// loads, which is a silently absent behaviour — and the audit that would catch it is
// the one below, reading the directory rather than the list.
func TestEveryModuleInTheDirectoryIsInTheOrdering(t *testing.T) {
	t.Parallel()

	declared := live.Order()
	present := live.Names()

	for _, name := range present {
		if !slices.Contains(declared, name) {
			t.Errorf("%s is embedded and in the directory but not in the load order. A "+
				"module nobody loads is a behaviour that never arrives, and nothing else "+
				"in this package can notice", name)
		}
	}

	for _, name := range declared {
		if !slices.Contains(present, name) {
			t.Errorf("%s is in the load order but not in the directory, so it cannot be "+
				"embedded and `live.Source` would panic", name)
		}
	}
}

// --- The audits ---------------------------------------------------------------------

// socketConstructors matches the ways a file opens a socket, and the count is the
// whole claim: exactly one on the play page.
//
// `new WebSocket` rather than `WebSocket`, so a *reference* to the constructor — in a
// comment, in a string, in a `typeof` guard — is not counted as an opening. The
// comment here says "no `EventSource`" and that must not be counted either.
var socketConstructors = regexp.MustCompile(`new\s+WebSocket\s*\(`)

// secondTransports are the ways a file could open a second connection to this origin.
//
// Architecture §7's ceiling is about six concurrent connections per origin, and
// every one of these holds one **permanently**. A live region that reconnected by
// polling would spend four of them and leave nothing for the document.
var secondTransports = []string{
	"EventSource",
	"XMLHttpRequest",
	"navigator.sendBeacon",
	"import(",
	"new Worker",
	"new SharedWorker",
	"new EventSource",
}

// markupSinks are the ways a file could turn a string into DOM.
//
// §7.5 says the sidebar's contents are *rendered DOM fragments* — markup this process
// produced — so a client that assembled any of its own would be a second renderer,
// and the one nobody reviews. Every one of these is a sink for exactly that.
var markupSinks = []string{
	"innerHTML",
	"outerHTML",
	"insertAdjacentHTML",
	"document.write",
	"eval(",
	"new Function",
}

// timers is the client-authoring escape hatch that matters here: `eval` and
// `Function` build code from a string, and §7.10's list is a list of *banned
// mechanisms* rather than only of banned outcomes.

// TestTheClientOpensExactlyOneWebSocketAndNoSecondTransport is §7.5's second task,
// over the shipped bytes.
//
// **One, not at least one, and not zero.** "At least one" would be satisfied by a
// module that opens four, which is the failure the task exists to prevent: four
// connections per reader against plan §7's ceiling of about six, with the
// WebSocket route, the wiki, and the assets that make the page render all
// competing for what is left.
func TestTheClientOpensExactlyOneWebSocketAndNoSecondTransport(t *testing.T) {
	t.Parallel()

	source := readSource(t, live.ChromeFile)

	if got := len(socketConstructors.FindAllString(source, -1)); got != 1 {
		t.Errorf("%s constructs a WebSocket %d time(s), want exactly 1.\n"+
			"§7.5's task is \"the play page opens exactly one WS and one SSE\", and "+
			"every SSE stream holds a connection permanently against plan §7's ceiling "+
			"of about six. Zero means the tabletop is never live; two means the sidebar "+
			"and the canvas disagree, because only one of them is being read",
			live.ChromeFile, got)
	}

	auditSecondTransport(t, source)
}

// TestTheClientAuthorsNoMarkup is §7.5's "rendered DOM fragments … patched by
// Datastar", from the client side.
//
// The server-side half is that every fragment this route emits is a templ component's
// output, escaped by templ. This is the other half: the client never assembles any,
// so there is no second renderer to disagree with the server about what a chat line
// or a roll is called.
func TestTheClientAuthorsNoMarkup(t *testing.T) {
	t.Parallel()

	// §7.5 requires the sidebar's contents to be server-rendered fragments the client
	// only places, and a string-to-DOM sink is the one mechanism by which a client
	// could render its own — which is a second renderer for content this product
	// deliberately does not reproduce in a browser.
	auditMarkupSinks(t, readSource(t, live.ChromeFile))
}

// bannedVocabulary is UI §1.2's closed list, and the same two words every route's
// audit carries.
//
// Case-insensitive and a substring, because the failure being guarded against is a
// noun phrase inside a sentence — "your session has expired", "the world map" — and
// neither is spelled with a capital letter in isolation.
var bannedVocabulary = []string{"world", "session"}

// TestTheClientNamesNoRetiredEntity is the vocabulary rule over a *source file*.
//
// The per-route audits cover rendered markup, which is where the rule bites hardest,
// but a string literal in the client is rendered into a live region by whoever writes
// the sentence — and this module authors no sentences at all, which is exactly why a
// banned word here would be a sign that it had started.
func TestTheClientNamesNoRetiredEntity(t *testing.T) {
	t.Parallel()

	// Through `auditVocabulary`, so the assertion the product makes and the one the
	// mutation control breaks are the *same function*. A control that tested a
	// paraphrase would be a control of the paraphrase.
	for _, name := range liveSources {
		auditVocabulary(t, readSource(t, name))
	}
}

// TestTheClientNamesTheDeclaredHooks holds the hook vocabulary.
//
// Every `data-chrome` value and every `data-testid` this module reads must be one
// `internal/web/components/live` declares, and **spelled identically**. A hook
// spelled two ways is a hook that matches nothing, and the symptom is a connection
// that silently never opens — a page that looks fine.
func TestTheClientNamesTheDeclaredHooks(t *testing.T) {
	t.Parallel()

	source := readSource(t, live.ChromeFile)

	auditHooks(t, source)

	// And the reverse direction: every hook this module reads must be one the live
	// package declares, read from the **Go constants** rather than spelled here. That
	// is the half a "does it contain X" check cannot do, and it is the half that
	// catches a hook renamed on the interface side — which would otherwise leave the
	// client selecting on a string nothing renders, and the connection silently never
	// opening.
	//
	// The two connection-state hooks are `data-testid`s rather than `data-chrome`
	// ones, because they are paragraphs *inside* the connection container and §10.2's
	// hook rule is what makes a testid a single owner: a `data-chrome` on both would
	// make the container's own hook appear twice.
	for _, declared := range []string{
		chrome.WebSocketHook,
		chrome.SocketConnectedTestID,
		chrome.SocketLostTestID,
	} {
		if !strings.Contains(source, `"`+declared+`"`) {
			t.Errorf("%s does not read the hook %q that `internal/web/components/live` "+
				"declares. A rename on the interface side leaves the client selecting on "+
				"a string nothing renders, and the symptom is a connection that silently "+
				"never opens",
				live.ChromeFile, declared)
		}
	}
}

// --- The negative controls ------------------------------------------------------------

// auditFailer is the subset of `*testing.T` the audits use.
//
// An interface rather than `*testing.T` because the audits have to be *tested* — a
// rule that cannot be shown to reject a violation it claims to catch is a gate wired
// to nothing — and a test of a `*testing.T`-typed function is a test that fails the
// suite. `silentFailer` below satisfies this and records instead of reporting.
type auditFailer interface {
	Helper()
	Errorf(format string, args ...any)
}

// silentFailer is a `*testing.T` whose `Errorf` is intercepted.
type silentFailer struct {
	*testing.T

	failures int
}

// Errorf records the failure and carries on, so one source produces a count rather
// than stopping at the first violation.
func (f *silentFailer) Errorf(format string, args ...any) {
	f.failures++
	f.Logf(format, args...)
}

// Helper satisfies the audits' `t.Helper()` calls without recording anything.
func (f *silentFailer) Helper() {}

// liveSourceAudits is the list the mutation control walks.
//
// Each row is a rule, the line that violates it, and the fragment of a message only
// that rule can produce — so "did it report" is not the check, and a rule firing for
// an unrelated reason fails the row.
var liveSourceAudits = []struct {
	// name identifies the audit in a failure message.
	name string

	// audit is the function under test.
	audit func(auditFailer, string)

	// mutation is a line appended to the source to break it.
	mutation string

	// wantOne is a fragment only this audit's messages can contain.
	wantOne string
}{
	{
		name:     "the socket count",
		audit:    auditSocketCount,
		mutation: "const accidental = new WebSocket(url);\n",
		wantOne:  "constructs a WebSocket",
	},
	{
		name:     "the second transport",
		audit:    auditSecondTransport,
		mutation: "const accidental = new EventSource(url);\n",
		wantOne:  "second connection",
	},
	{
		name:     "the markup sinks",
		audit:    auditMarkupSinks,
		mutation: "region.innerHTML = frame;\n",
		wantOne:  "string-to-DOM sink",
	},
	{
		name:     "the retired vocabulary",
		audit:    auditVocabulary,
		mutation: "const where = 'the world of this campaign';\n",
		wantOne:  "outside a comment",
	},
	{
		// A *typo* rather than an addition, and that is the interesting mutation: it
		// is the one a rename produces, and it removes a string rather than adding
		// one — so an audit that only ever detected additions would miss it.
		name:     "the declared hooks",
		audit:    auditHooks,
		mutation: "",
		wantOne:  "does not name",
	},
}

// TestEveryLiveSourceAuditRejectsTheViolationItClaimsTo is the negative control.
//
// Three claims, and the first is the one that is easy to leave out: **the audit must
// be silent on the unmutated source.** An audit that fires on the file it was written
// for is broken, and the mutation half alone would not notice.
func TestEveryLiveSourceAuditRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	source := readSource(t, live.ChromeFile)

	for _, subject := range liveSourceAudits {
		t.Run(subject.name, func(t *testing.T) {
			t.Parallel()

			quiet := &silentFailer{T: t}
			subject.audit(quiet, source)

			if quiet.failures != 0 {
				t.Errorf("the audit reported %d finding(s) about the real source, which "+
					"it is written for. An audit that fires where it has nothing to say "+
					"is as useless as one that never fires", quiet.failures)
			}

			// The mutation is appended for every rule except the hooks one, which is
			// broken by a *rename* — so the control removes the hook rather than adding
			// anything, which is what a rename actually does to a document.
			mutated := appendLine(source, subject.mutation)
			if subject.mutation == "" {
				mutated = strings.ReplaceAll(
					source, `"live-socket-lost"`, `"live-socket-gone"`,
				)
			}

			loud := &silentFailer{T: t}
			subject.audit(loud, mutated)

			if loud.failures == 0 {
				t.Errorf("the audit reported nothing for a source that violates it. An " +
					"audit that reports nothing is the same failure as no audit, with a " +
					"green light on top")
			}
		})
	}
}

// appendLine adds a line to a source.
func appendLine(source, line string) string {
	if !strings.HasSuffix(source, "\n") {
		return source + "\n" + line
	}

	return source + line
}

// --- The audit bodies, so the control can call them ------------------------------------

// auditSocketCount is the WebSocket-count rule, as a function.
//
// A function rather than inline assertions because the same code serves the product
// assertion and the mutation control, which is the only way the control tests *the
// audit* rather than a paraphrase of it.
func auditSocketCount(t auditFailer, source string) {
	t.Helper()

	if got := len(socketConstructors.FindAllString(source, -1)); got != 1 {
		t.Errorf("the source constructs a WebSocket %d time(s), want exactly 1; §7.5's "+
			"task is that the play page opens exactly one", got)
	}
}

// auditSecondTransport is the no-second-transport rule.
func auditSecondTransport(t auditFailer, source string) {
	t.Helper()

	for _, transport := range secondTransports {
		if strings.Contains(source, transport) {
			t.Errorf("the source mentions %q, which would be a second connection to "+
				"this origin and a permanent one", transport)
		}
	}
}

// auditMarkupSinks is the no-client-rendered-markup rule.
func auditMarkupSinks(t auditFailer, source string) {
	t.Helper()

	for _, sink := range markupSinks {
		if strings.Contains(source, sink) {
			t.Errorf("the source mentions %q, which is a string-to-DOM sink", sink)
		}
	}
}

// auditVocabulary is the retired-entity rule.
func auditVocabulary(t auditFailer, source string) {
	t.Helper()

	lowered := strings.ToLower(source)

	for _, banned := range bannedVocabulary {
		if strings.Contains(lowered, banned) {
			t.Errorf("the source names %q outside a comment; neither entity exists",
				banned)
		}
	}
}

// auditHooks is the hook-vocabulary rule.
func auditHooks(t auditFailer, source string) {
	t.Helper()

	for _, wanted := range []string{
		`"live-ws"`,
		`"live-socket-connected"`,
		`"live-socket-lost"`,
	} {
		if !strings.Contains(source, wanted) {
			t.Errorf("the source does not name %s; the hook is declared by "+
				"`internal/web/components/live` and a spelling that differs matches nothing",
				wanted)
		}
	}
}
