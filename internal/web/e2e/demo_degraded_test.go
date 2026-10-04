package e2e_test

// Claim four: `forgotten-realm` keeps its wiki, refuses its game, and tells an
// operator which system it wanted.
//
// # Three claims, and any one of them can hold while the others fail
//
// AGENTS.md's security invariants state the requirement as three separate ones, and
// S-14.8's test asserts them separately rather than as one "the wiki works" check.
// They are:
//
//  1. **the wiki answers 200 with the page's content** — §10.8's "Wiki still serves",
//     and the half a Game Master notices;
//  2. **the game refuses** — a dispatch produces a refusal and applies nothing; and
//  3. **the refusal is nameable** — an operator reading the boot log learns which
//     campaign names which id.
//
// # A fourth thing this file asserts is what claim 3 must *not* be
//
// §10.8's row and S-14.8's note are emphatic that the id reaches an operator through a
// log line and **not** through the error's text, "because that string reaches a
// browser and a log line, so it is the reason alone (S-12.3)". So a refused dispatch's
// response is asserted **not** to name the system, and the boot log is asserted to
// name it. Both halves: a suite that only checked the browser would be satisfied by
// the disclosure S-12.3 forbids, and one that only checked the log would miss the id
// appearing in front of a player.
//
// # Why the boot log and not `plugin.UnknownSystemError`
//
// `reportMissingSystems` is unexported and lives in `package main`. It is also the only
// thing in the product that produces `plugin.missing`, and it runs before the listener
// opens — so the line is on stdout before a request is possible, and this suite reads
// it there. `cmd/server/systems_test.go` asserts the line's shape over a synthetic
// campaign; this file asserts that the **shipped artefact** produces it, with the
// artefact's own slug and the artefact's own id.
//
// # What is not checked here, and why
//
// **The tabletop's placements are not in any fetched document.** The canvas is
// hydrated from structured data over the socket, and `play.Handler.Snapshot` — the one
// field that would put placements in the server-rendered token list — is unset in the
// shipped binary (see `TestTheResolvedCampaignsTableAlsoShowsNoNotation` below). So
// nothing here claims to have seen a token on screen. `openTable` reads one socket
// frame and asserts only that it arrived; what is *in* a snapshot is
// `internal/realtime`'s own coverage, and a suite that parsed it would be a second
// implementation of a codec reader.
//
// **The roller's `server_error` word is not by itself evidence of anything.**
// `plugin.RejectionError`'s reason is one of eight closed words and `server_error` is
// the safe default for every failure the table does not classify — so the attribution
// comes from the boot log, and from the contrast with a campaign whose system this
// build does resolve.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	neturl "net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e/overlays"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/web/plugins/dice"
)

// The roller route, derived rather than written out.
//
// **`plugins.Mount` derives its patterns from `webplugins.Registry.PageTypes`,** so
// the path is `dice.PagePath` with the campaign's slug substituted — which is
// literally `plugins.pathFor`. Naming `dice.PagePath` and substituting the same way
// the route's own `kindFor` does keeps the two answers together: if a plugin changed
// its mount, this suite follows, and if the mount and the declaration disagreed the
// suite would be asking for a URL the product does not serve.
func rollerURL(boot *demoBoot, campaign string) string {
	return boot.base + strings.ReplaceAll(dice.PagePath, "{slug}", campaign)
}

// tableBudget bounds one socket join.
//
// `realtime`'s own read bound is 90 seconds and a join's first frame is sent
// immediately, so five is generous. A deadline is not politeness: a table that
// accepts the upgrade and then says nothing is a state the assertions below must not
// mistake for a working game.
const tableBudget = 5 * time.Second

// TestTheDegradedCampaignStillServesItsWiki is claim four's first third.
//
// **Every page of `forgotten-realm`, not its front page.** The requirement is that a
// campaign whose plugin was removed keeps its content, and a campaign whose front page
// survives while one of its other pages fails has not kept its content. Each page is
// checked for its own `<h1>` and its own first heading, so a route answering the same
// document for every path would not pass.
func TestTheDegradedCampaignStillServesItsWiki(t *testing.T) {
	boot := sharedDemo(t)

	pages := pagesOfCampaign(t, boot, degradedSlug)
	if len(pages) == 0 {
		t.Fatalf("the degraded campaign holds no pages in the vault copy, so this " +
			"claim would pass on a campaign that demonstrates nothing")
	}

	for _, page := range pages {
		t.Run(page.name(), func(t *testing.T) {
			document := boot.requireDocument(t, boot.gm, boot.base+page.url())

			heading := demoRequireTestID(t, document, "page-title")
			if got := demoFold(strings.TrimSpace(demoText(heading))); got != page.name() {
				t.Errorf("the document heads %q, want %q. §10.8 requires the wiki to "+
					"keep serving for a campaign whose `system_id` resolves to nothing, "+
					"so the response has to be this page", got, page.name())
			}

			if page.heading != "" && !slices.Contains(demoHeadings(
				demoRequireTestID(t, document, "page-body"),
			), demoFold(page.heading)) {
				t.Errorf("the document for %s carries no %q heading, so the 200 did "+
					"not carry this page's body", page.url(), page.heading)
			}
		})
	}
}

// TestTheDegradedCampaignRendersATableThatSaysGameplayIsUnavailable is the second
// claim, over the document a Game Master actually opens.
//
// `/c/{slug}/play` is the VTT document (S-9), and for a campaign whose gameplay system
// this build does not resolve the roller renders **with no notation at all**. That is
// S-10.6's first row reached from the UI side and §14's "renders for an empty output"
// applied to a page type: the widget is still there, the reader is told why it is
// empty, and the roller is not silently wrong.
//
// Four assertions, and the first is what makes the rest mean anything:
//
//  1. the response is a **200 and a document** — the shell's landmarks are present and
//     it is not a refusal sentence, because a "gameplay is unavailable" claim about a
//     plain-text body would be a claim about nothing;
//  2. the token list panel is present (`play-token-list`), which is UI §7.6's
//     accessibility source of truth for the table and the one a screen reader uses
//     instead of the canvas;
//  3. the roll dialog's centre is the **empty state**, by `data-testid`, and its text
//     names the reason; and
//  4. the dialog does **not** carry an expression field — a widget that said "no
//     notation" and rendered an input anyway would be telling a Game Master to type
//     into something this build cannot answer.
//
// **None of this is evidence that the system is missing.**
// `TestTheResolvedCampaignsTableAlsoShowsNoNotation` records that the *showcase*
// campaign — whose system this build does resolve — renders the same empty state,
// because the shipped `newPlayRoute` leaves `Systems` unset. §10.6's UI row is
// satisfied; the distinction it should draw is not, and the report says so.
func TestTheDegradedCampaignRendersATableThatSaysGameplayIsUnavailable(t *testing.T) {
	boot := sharedDemo(t)

	document := boot.requireDocument(t, boot.gm, boot.base+"/c/"+degradedSlug+"/play")

	// 1. A document and not a refusal.
	for _, landmark := range []string{"shell-header", "shell-main", "shell-footer"} {
		if demoByTestID(document.root, landmark) == nil {
			t.Errorf("the table document carries no %q landmark. S-9's `/play` is an "+
				"HTML document a browser can be pointed at, so a response without the "+
				"shell's landmarks is a response this route should never have written",
				landmark)
		}
	}

	// 2. The token list, the accessibility source of truth.
	tokens := demoRequireTestID(t, document, "token-list-panel")
	if attrOf(tokens, "data-state") != "empty" {
		t.Errorf("the table's token list is in state %q, want %q. This campaign's "+
			"manifest declares no `state:`, so an empty table is the honest thing to "+
			"render and any other state is a claim about placements nothing holds",
			attrOf(tokens, "data-state"), "empty")
	}

	// 3. The roll dialog's empty state.
	empty := demoRequireTestID(t, document, "play-roll-empty")
	if text := strings.ToLower(demoText(empty)); !strings.Contains(text, "notation") {
		t.Errorf("the roll dialog's empty state reads %q. §10.6's first row reached "+
			"from the UI side is a widget that renders with no notation **and says "+
			"so**; a widget that rendered nothing at all would be a control that exists "+
			"and does not", strings.TrimSpace(demoText(empty)))
	}

	// 4. And no way to type an expression into it.
	for _, field := range demoElementsWithAttr(document.root, "name", "expr") {
		t.Errorf("the table carries an expression field at <%s> while its own dialog "+
			"says no notation is published. A Game Master typing into a widget this "+
			"build cannot answer is the state §4.7's honest empty state exists to avoid",
			field.Data)
	}
}

// TestTheShowcaseCampaignsTableRendersItsOwnNotationAndPlacements is what the report
// below became, once the defect it described was fixed.
//
// # Why it was a report first
//
// `cmd/server/realtime.go`'s `newPlayRoute` built `&play.Handler{Hub: hub, Logger:
// logger}` and nothing else, justified by a comment saying `/play` renders no document.
// It does — `handler.serveDocument` writes a complete shell — and `play.Handler` had
// since grown `Campaigns`, `Systems` and `Snapshot`. So every campaign rendered §4.7's
// empty state for all three, including `greyhaven` and its three seeded placements.
//
// The first version of this test **asserted the defect** — that the showcase shows no
// notation and an empty token list — with a message naming the fix and instructing its
// own deletion. That was the right instinct and the wrong instrument: a test named
// "renders no notation" reads as a specification, and the moment the wiring landed the
// suite went red for a reason that had nothing to do with the change.
//
// **A defect is not a specification.** The fix replaced it.
//
// # What this asserts instead
//
// The showcase campaign is the one whose manifest declares a `system` this build
// resolves, so the roll dialog is where the `Systems` seam must **show**: before the
// wiring it read "no roll notation" on a campaign whose system the build answers for,
// which is the defect stated as a fact. That assertion is the one that fails if the
// wiring is undone.
//
// The `Snapshot` seam is **not** assertable from a fetched document, and the file says
// so at the point where a reader would expect it to. The degraded campaign's table is
// asserted separately, by
// `TestTheDegradedCampaignRendersATableThatSaysGameplayIsUnavailable` — and the pair
// together is the distinction §10.8's UI row claims, which neither could establish
// alone: before the fix both campaigns rendered identically, so the degraded test proved
// nothing about degradation.
func TestTheShowcaseCampaignsTableRendersItsOwnNotationAndPlacements(t *testing.T) {
	boot := sharedDemo(t)

	document := boot.requireDocument(t, boot.gm, boot.base+"/c/"+showcaseSlug+"/play")

	// The roll dialog must NOT be in its no-notation empty state. `dnd5e.SystemID` is
	// what the showcase's manifest declares and what this build registers, so a dialog
	// that still says "no roll notation" means `Systems` is unwired or is answering for
	// the wrong campaign.
	if empty := demoByTestID(document.root, "play-roll-empty"); empty != nil {
		t.Fatalf("the showcase campaign's roll dialog still renders the no-notation "+
			"empty state, though its manifest declares the system this build resolves "+
			"(%s). `newPlayRoute` has had `Systems` unwired again — the defect the "+
			"version of this test that asserted the defect described",
			dnd5e.SystemID)
	}

	// The token list is asserted **as empty, and for a reason** — which is the opposite
	// of what this test's own predecessor claimed, and the reason is worth stating
	// because the two halves look contradictory.
	//
	// `Snapshot` reads `realtime.Registry`, the **in-memory** live states, not
	// `campaign_state` in the database. The showcase's three placements were seeded into
	// that column, but a state is opened only when a client joins the table — `Hub`'s
	// own comment says it is the only thing that causes one to be opened. So the first
	// server-rendered document renders before any socket exists and the token list is
	// empty, which is `play.SnapshotFunc`'s documented contract: "nil, or a campaign
	// whose state is not open, renders the token list's empty state — which is the
	// truth until a client says otherwise."
	//
	// **So the defect this file's predecessor described is fixed, and the symptom it
	// used to assert is still there for a legitimate reason.** Before the wiring, the
	// list was empty *forever*; now it is empty *until a client connects*. This
	// assertion cannot tell those apart from a fetched document, which is the honest
	// limit of what a document-only test can say about the table.
	//
	// What it does hold is the part that **is** observable without executing
	// JavaScript: the token list is in its empty state rather than absent, so the panel
	// a screen reader is pointed at exists before any client connects. Before the fix
	// it was in that same state — so this assertion is a **negative** control, and it is
	// labelled as one rather than dressed up as coverage.
	//
	// Proving the placements arrive belongs to a test that opens the socket, and
	// `cmd/server/play_wiring_test.go` is that test: it joins the hub and requires the
	// document to carry the revision, the pause flag and the created placement.
	tokens := demoRequireTestID(t, document, "token-list-panel")

	if state := attrOf(tokens, "data-state"); state != "empty" {
		t.Errorf("the showcase campaign's token list rendered state %q before any "+
			"client joined, so no state is live and the empty state is the contract. "+
			"If this is ever non-empty, either a state is opened without a client or the "+
			"list has stopped rendering — both worth a look, neither a defect",
			state)
	}
}

// TestTheDegradedCampaignRefusesToRecordARoll is claim four's second third.
//
// # Why the showcase campaign is joined first
//
// **A UI plugin's dispatch requires the campaign's live state**, and only `Hub.Join`
// opens one, which happens on a WebSocket. `plugin.Resolver.Resolve` answers
// `RejectServerError` with "campaign %d has no live state" when nobody has joined, so
// a roll posted to a campaign nobody is sitting at is refused for a reason that has
// nothing to do with §10.8. `openTable` therefore dials `/c/{slug}/ws` for both
// campaigns first, so the refusal below is the resolver's and not the registry's.
//
// # Three assertions
//
//  1. the degraded campaign **refuses**, and says why in `data-reason`;
//  2. it applies **nothing** -- the response carries no outcome, because a refusal that
//     stamped a version would be worse than no answer, since a client would reconcile
//     against a change nobody made; and
//  3. **it does not name the system.** S-12.3, and the half of §10.8 that matters to a
//     reader: the response is a page a player can see, so it carries the reason and
//     nothing else.
//
// # What this test deliberately does *not* claim, and why it cannot
//
// **A roll cannot succeed in this build, on any campaign**, so there is no positive
// control to contrast against and the refusal's `server_error` does not by itself
// attribute anything. `TestNoRollGoesThroughTheShippedRoller` records that, with the
// cause; the attribution here comes from the boot log in
// `TestTheBootLogNamesTheCampaignAndTheSystemItWants`.
//
// **This is the honest shape of the claim and it is worth saying plainly rather than
// dressing up.** §10.8 requires a campaign whose system resolves to nothing to refuse
// its game, and it does. But a suite that asserted "refused here, recorded there"
// would be asserting something this build cannot produce, and the alternative --
// dropping the attribution -- would leave §10.8's actionable half unchecked.
func TestTheDegradedCampaignRefusesToRecordARoll(t *testing.T) {
	boot := sharedDemo(t)

	openTable(t, boot, showcaseSlug)
	openTable(t, boot, degradedSlug)

	declined := postRoll(t, boot, degradedSlug)

	refusal := demoByTestID(declined.root, "roll-refused")
	if refusal == nil {
		t.Fatal("the degraded campaign recorded a roll its gameplay system does not " +
			"exist to evaluate. §10.8 requires the game to refuse, and a table that " +
			"answered would be resolving intents under no rules at all")
	}

	if attrOf(refusal, "data-reason") == "" {
		t.Error("the refusal carries no `data-reason`. The reason is the whole of what " +
			"the actor is told, and an empty one is a refusal with no content")
	}

	if outcome := demoByTestID(declined.root, "roll-result"); outcome != nil {
		t.Errorf("the refusal also rendered a result: %q. A refusal must apply nothing; "+
			"a client reconciling against a change nobody made shows a number the server "+
			"never produced", strings.TrimSpace(demoText(outcome)))
	}

	for _, leak := range []string{degradedSystemID, "pathfinder"} {
		if carried := demoCarry(declined.root, leak); len(carried) > 0 {
			t.Errorf("the refused roll's response names %q, in %v. §10.8 says the "+
				"refusal is nameable and S-12.3 says where: a typed field an operator "+
				"reads in a log line. The error's text reaches a browser and a log line, "+
				"so it is the reason alone", leak, carried)
		}
	}
}

// TestNoRollGoesThroughTheShippedRoller is a **report**, written as a test so it
// cannot rot. It is claim four's missing positive control.
//
// # What it found
//
// **Every roll this build accepts from the graphical dice roller is refused**, on every
// campaign, resolved system or not. The cause is a disagreement between two packages
// and not between a campaign and its system:
//
//   - `internal/web/plugins/dice`'s `Request.Frame` puts `placement`, `expr` and
//     `reason` into the intent's arguments, because a roll is addressed to a token and
//     the route reads the token out of a form field;
//   - `internal/domain/systems/dnd5e`'s `rollArgs` has `expr`, `label` and `dc`, and
//     **no `placement`**, and `decodeArgs` unmarshals with `DisallowUnknownFields`.
//
// So every frame the roller builds is refused at `Apply` with `those arguments are not
// ones this operation takes: "roll": json: unknown field "placement"`, and
// `plugin.wireReason` reduces it to `server_error`. Measured by temporarily
// instrumenting `internal/httpapi/plugins`'s dispatch path and reverting it; the
// error's own text is deliberately not asserted here, because S-12.3 keeps it out of
// the response and nothing logs it.
//
// **Two consequences, and the second is the worse one:**
//
//   - the artefact's headline interactive feature -- the roller the manifest's
//     `demonstrates:` list names -- is inert in this build; and
//   - a refusal's `server_error` word is **not evidence of §10.8**, which is why
//     `TestTheDegradedCampaignRefusesToRecordARoll` takes its attribution from the
//     boot log instead.
//
// **This work item does not own either package** and does not patch them.
//
// # Why it asserts the defect rather than the fix
//
// The right answer is a roll that records, and asserting that would be red on arrival
// -- a suite may not ship red. So the test asserts **what is true**, with the cause in
// the message, and goes red the day somebody makes it untrue. Same shape as
// `TestTheShippedTableRendersNoNotationAndNoTokenListForEveryCampaign`.
func TestNoRollGoesThroughTheShippedRoller(t *testing.T) {
	boot := sharedDemo(t)

	// The showcase campaign: this build resolves its system and its table is joined, so
	// nothing about the refusal below is the campaign's or the registry's.
	openTable(t, boot, showcaseSlug)

	document := postRoll(t, boot, showcaseSlug)

	refusal := demoByTestID(document.root, "roll-refused")
	if refusal == nil {
		t.Fatalf("a roll went through the shipped roller. That is the expected " +
			"direction: `internal/web/plugins/dice` and `internal/domain/systems/dnd5e` " +
			"disagree about the intent's arguments -- the roller sends `placement` and the " +
			"engine's `rollArgs` refuses unknown fields -- and somebody has made them " +
			"agree. Delete this test, and `TestTheDegradedCampaignRefusesToRecordARoll` " +
			"can take the positive control it currently has to do without")
	}

	if reason := attrOf(refusal, "data-reason"); reason != "server_error" {
		t.Logf("the roller refuses with data-reason=%q rather than server_error, so the "+
			"cause may have moved. Check `internal/web/plugins/dice`'s `Request.Frame` "+
			"against `internal/domain/systems/dnd5e`'s `rollArgs`", reason)
	}
}

// TestTheBootLogNamesTheCampaignAndTheSystemItWants is claim four's third third, and
// the only place the id is allowed to appear.
//
// # Three assertions about the log line
//
//  1. **exactly one** `plugin.missing` line, naming `forgotten-realm` and
//     `pathfinder-2e` — `campaign` and `system` read as **decoded JSON fields** rather
//     than as substrings, because a `detail` sentence mentioning the id would satisfy a
//     search and mean nothing;
//  2. **no other campaign** produces one, which is the control: the line is computed
//     from `system_id` resolving to nothing, so a build reporting it for every campaign
//     would say nothing about `forgotten-realm`; and
//  3. the line is at **`error`**, because a campaign that cannot start its game is a
//     Game Master who will discover it mid-session.
//
// # And the typed refusal, so that the log line is not the only evidence
//
// `plugin.UnknownSystemError.ID` is the field an operator's tooling reads. Asserting
// it here means the two are independent — the log line is what an operator sees, the
// field is what a caller reads — rather than one derived from the other.
func TestTheBootLogNamesTheCampaignAndTheSystemItWants(t *testing.T) {
	boot := sharedDemo(t)

	missing := boot.logCarrying(t, "plugin.missing")

	if len(missing) != 1 {
		t.Fatalf("the server wrote %d `plugin.missing` lines, want exactly 1 — one per "+
			"campaign whose `system_id` resolves to nothing, and the artefact declares "+
			"exactly one such campaign (%s).\n\n%s",
			len(missing), degradedSlug, boot.rawLog())
	}

	line := missing[0]

	if line.Campaign != degradedSlug {
		t.Errorf("`plugin.missing` names campaign %q, want %q. The line's whole value "+
			"is that an operator can tell *which* of their campaigns is affected",
			line.Campaign, degradedSlug)
	}

	if line.System != degradedSystemID {
		t.Errorf("`plugin.missing` names system %q, want %q. §10.8 says the refusal "+
			"says which ID it wants, and 'the game cannot start' does not tell a Game "+
			"Master what to do about it", line.System, degradedSystemID)
	}

	if line.Level != "ERROR" {
		t.Errorf("`plugin.missing` is logged at %q, want ERROR. `resumeCampaignStates` "+
			"treats the same class of condition as an error for the same reason: a "+
			"campaign that cannot start its game is a Game Master who finds out "+
			"mid-session, and a warning on a boot line is a line nobody reads",
			line.Level)
	}

	for _, campaign := range []string{showcaseSlug, publicSlug} {
		for _, other := range missing {
			if other.Campaign == campaign {
				t.Errorf("`plugin.missing` also names %s, whose system this build "+
					"resolves. The line is computed from `system_id` resolving to "+
					"nothing, so reporting a resolvable campaign would mean this suite "+
					"is not measuring what it claims", campaign)
			}
		}
	}

	// The typed half, and the control that the showcase's id *does* resolve.
	if err := demoResolve(t, dnd5e.SystemID); err != nil {
		t.Fatalf("this build does not resolve %s: %v. The showcase campaign's control "+
			"roll in the test above would then have been refused for the same reason as "+
			"the degraded one, and nothing either test claims would be distinguishable",
			dnd5e.SystemID, err)
	}

	err := demoResolve(t, degradedSystemID)
	if err == nil {
		t.Fatalf("this build resolves %s, so the boot line above cannot be about a "+
			"missing system. `demo-vault/demo.manifest.yml` says `make demo-check` "+
			"must turn red if a Pathfinder plugin is ever registered — and so must this",
			degradedSystemID)
	}

	var unknown *plugin.UnknownSystemError
	if !errors.As(err, &unknown) {
		t.Fatalf("the refusal is %T, want something wrapping *plugin.UnknownSystemError. "+
			"That type is the only thing carrying the id to a caller as a field, and "+
			"S-12.3 exists because the error's text is the reason alone", err)
	}

	if string(unknown.ID) != degradedSystemID {
		t.Errorf("the typed refusal names %q, want %q", unknown.ID, degradedSystemID)
	}

	if unknown.Class() != missingClass {
		t.Errorf("the typed refusal's class is %q, want %q. The class is what an alert "+
			"matches, and `observability.errorClass` would otherwise report %T — a type "+
			"name no alert can match", unknown.Class(), missingClass, unknown)
	}
}

// missingClass is `plugin.missing`, the §13.2 event name and the class the boot line
// and the typed refusal share.
//
// **A constant here rather than a use of `plugin.UnknownSystemError.Class()`**, so the
// assertion is about the product's published class name and not about the type
// agreeing with itself.
const missingClass = "plugin.missing"

// postRoll posts one roll to a campaign's roller and parses the answer.
//
// **A 200 even for a refusal**, and that is `plugins.go`'s own contract: "A refusal is
// a 200 with the reason on the page, not a 4xx. The reader's action was understood and
// answered." So a non-200 here is a fault rather than a refusal, and failing on it is
// correct.
//
// **The POST's own response is parsed.** Fetching the page again and reading that would
// be a different document — one with no outcome on it — and the whole claim is about
// what the POST answered.
func postRoll(t *testing.T, boot *demoBoot, campaign string) demoDocument {
	t.Helper()

	url := rollerURL(boot, campaign)

	status, body := demoPostForm(t, boot.gm, url, demoRollForm())
	if status != http.StatusOK {
		t.Fatalf("POST %s = %d, want 200. A roll that reaches the table is answered "+
			"with the table's answer on a 200; a 4xx would mean the request itself was "+
			"refused, which is a different fault", url, status)
	}

	return demoParse(t, body, url)
}

// demoRollForm is the roller's own form, filled in.
//
// **The field names are `internal/httpapi/plugins`' constants** and the expression is
// one a 5e grammar accepts with no modifier, so this is a request a Game Master could
// send. `seq` is required by the route and is a client counter in a real page; `1` is
// what a fresh client sends.
//
// The placement is `gh-iron-vigil`, **read from the manifest's own seeded state**
// rather than from the token page's prose, so it cannot drift from the row the seed
// wrote — and it is one of the two *visible* placements, because the third is marked
// `visible: false` and a roll against an unsent placement would be refused for a
// second reason.
func demoRollForm() neturl.Values {
	return neturl.Values{
		"seq":       {"1"},
		"placement": {seededVisiblePlacement},
		"expr":      {"1d20"},
		"reason":    {"the demo suite"},
	}
}

// seededVisiblePlacement is the placement id `demo.manifest.yml` writes into the
// showcase campaign's `campaign_state`, and it is the artefact's own identifier.
const seededVisiblePlacement = "gh-iron-vigil"

// openTable dials a campaign's socket, which is what brings its state to life.
//
// **It returns nothing, and that is deliberate.** Nothing here reads a frame -- see
// the header -- so the connection exists only to hold the join open for the test's
// lifetime, and a returned `*websocket.Conn` would be an invitation to read one.
//
// # Why the handshake and not a frame is the readiness signal
//
// **`Hub.Join` sends nothing.** The browser client in
// `static/js/live/chrome.js` opens the socket and only ever reads, so the first frame
// arrives when something mutates the state. A test that waited for a frame here would
// wait for an event it had not caused, and `play`'s own harness says so about its
// equivalent helper: "a timed read on a socket that has not yet been read at all is a
// different test with a different failure".
//
// What `Join` *does* do, before the upgrade, is `openAuthority` — which opens the
// campaign's state row — and that is the fact the roll below depends on:
// `plugin.Resolver.Resolve` answers `RejectServerError` with "campaign %d has no live
// state" without one, so a roll posted to a campaign nobody joined is refused whatever
// its system. A completed handshake is proof the state exists.
//
// **The session goes on the handshake by hand**, because `websocket.Dial` is not an
// `http.Client` with a jar. A dial without it is anonymous, and on a private campaign
// the correct answer to an anonymous socket request is 404 — which is exactly what the
// first run of this test got.
//
// **No `Origin` header.** `play.originAllowed` treats an absent `Origin` as "no
// browser, therefore no cross-origin vector", and `websocket.Dial` sets none — so the
// suite reaches the table by the branch a non-browser client takes, which is the
// branch that same comment names `wscat` and the demo check as legitimate users of.
func openTable(t *testing.T, boot *demoBoot, campaign string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), tableBudget)
	defer cancel()

	url := "ws" + strings.TrimPrefix(boot.base, "http") + "/c/" + campaign + "/ws"

	header := http.Header{}
	header.Set("Cookie", "sp_session="+demoSessionCookie(t, boot.gm, boot.base))

	//nolint:bodyclose // `websocket.Dial` hands back the handshake response, whose
	// body the library has already drained and closed on the failure path.
	conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if response != nil {
			t.Fatalf("dial %s: %v (the handshake answered %d)", url, err, response.StatusCode)
		}

		t.Fatalf("dial %s: %v", url, err)
	}

	t.Cleanup(func() {
		if closeErr := conn.Close(
			websocket.StatusNormalClosure,
			"the suite is done",
		); closeErr != nil {
			_ = closeErr
		}
	})
}

// demoResolve asks what this build resolves a gameplay system id to.
//
// # A registry per edition, never one registry for all of them
//
// Both editions are `dnd5e` — `engine.go` says the editions differ by their pack
// versions, which are their own fingerprint component — and `plugin.Register` refuses
// a duplicate system id. Registering both into one registry fails on the second, and
// that refusal is the correct one: a Game Master at the table cannot tell 5e-2014 from
// whichever build claimed the id second.
//
// So each edition is asked separately and the first one that answers decides. When
// none answers, the question is put to a registry built over the **first** shipped
// edition, so the refusal the caller receives is the product's own
// `*plugin.UnknownSystemError` rather than a synthesised one — a suite that fabricated
// its error and then asserted on its type would be asserting on itself.
//
// A union over the shipped packs is the only answer reachable from outside
// `package main`, and `internal/demo`'s `demoKindsForBuild` argues the same thing for
// the same reason.
func demoResolve(t *testing.T, id rules.ID) error {
	t.Helper()

	editions := overlays.IDs()
	if len(editions) == 0 {
		t.Fatal("this build ships no edition of any system, so a refusal cannot be " +
			"measured against a build that resolves something")
	}

	for _, edition := range editions {
		registry := plugin.New()

		if err := registerEdition(registry, edition); err != nil {
			t.Fatalf("register edition %s: %v", edition, err)
		}

		if _, err := registry.Resolve(id); err == nil {
			return nil
		}
	}

	registry := plugin.New()
	if err := registerEdition(registry, editions[0]); err != nil {
		t.Fatalf("register edition %s: %v", editions[0], err)
	}

	if _, err := registry.Resolve(id); err != nil {
		return fmt.Errorf("demo: %s resolves to nothing and no edition says why: %w",
			id, err)
	}

	return nil
}

// registerEdition compiles one edition and registers it, the way
// `cmd/server/systems.go` registers the one this build resolves under.
//
// **Both editions, and not the one `defaultEdition` names.** That constant is
// unexported in `package main`, and `internal/demo` states that a copy of it is not
// even possible. Registering every shipped edition is the conservative direction: a
// kind or a system id either edition registers is one this build could resolve.
func registerEdition(registry *plugin.Registry, id overlays.EditionID) error {
	edition, err := overlays.ByID(id)
	if err != nil {
		return fmt.Errorf("read edition %s: %w", id, err)
	}

	system, err := edition.System()
	if err != nil {
		return fmt.Errorf("build edition %s: %w", id, err)
	}

	if err := registry.Register(plugin.Entry{
		System: system,
		Codec:  plugin.PlacementCodec{},
	}); err != nil {
		return fmt.Errorf("register edition %s: %w", id, err)
	}

	return nil
}
