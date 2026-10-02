package edit_test

// The write path: the precondition, the write, the record, and the 412 in between.
//
// This file holds the phase's §14 row — "a stale `If-Match` gives **412**, **the
// disk is unchanged**, and **no revision row is written**" — and that row is the one
// assertion in the phase that must not be approximated. A test that checks only the
// 412 passes against code that also wrote the file and recorded a revision, because
// both of those are invisible from the status line. So `TestAStaleIfMatchAnswers412
// AndChangesNothing` asserts all three clauses, against the file on disk and against
// the table, and the two evidence halves are separate helpers (`read` and
// `revisionsFor`) precisely so that a handler which reported success without doing
// anything would still fail.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/edit"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/httpapi/wiki"
	"github.com/semiplane/semiplane/internal/web/components"
)

// original is the page most of these tests start from, and the string they assert
// the file still holds afterwards.
const original = "---\ntitle: A page\n---\nIron and rust.\n"

// replacement is what a matched save writes.
const replacement = "---\ntitle: A page\n---\nIron, rust and gold.\n"

// TestAStaleIfMatchAnswers412AndChangesNothing is S-6.2, S-6.3 and the phase's §14
// row, in three assertions that are checked against three different things.
//
// The shape of the test is the point. A 412 is a *statement the route makes about
// itself*, so a test that only reads the status is testing the statement. The
// second clause is checked by reading the file the vault actually holds, and the
// third by reading the table the revision log actually wrote to — so the test fails
// against a handler that returns 412 and then writes anyway, which is the shape of
// defect this whole phase exists to prevent.
func TestAStaleIfMatchAnswers412AndChangesNothing(t *testing.T) {
	t.Parallel()

	const (
		pagePath = "Vault.md"
		// H1 is what the GM opened. H2 is what is on disk when they save, because
		// something else wrote it in between — Obsidian, a sync client, a second
		// browser tab. That is the whole scenario S-6.1 exists for: there is no
		// lock, so the two writers never knew about each other.
		firstWrite  = "---\ntitle: A page\n---\nIron and rust.\n"
		diskContent = "---\ntitle: A page\n---\nIron, rust and gold.\n"
		gmBuffer    = "---\ntitle: A page\n---\nIron, rust and sea-green.\n"
	)

	fixed := newHarness(t)
	fixed.write(pagePath, firstWrite)

	// A sibling page, untouched by this request, so the third clause has something to
	// be about. A handler that wrote the wrong file — a path resolved from the wrong
	// field, a `..` normalised the wrong way — passes every other assertion in this
	// test and fails this one.
	sibling := "Other.md"
	fixed.write(sibling, "---\ntitle: Another page\n---\nUnrelated.\n")

	// The GM opens the editor. H1 is the validator they now hold.
	h1 := fixed.validatorFor(pagePath)

	// Something else writes the page. The watcher would notice; the editor's
	// precondition does not care, which is the point.
	fixed.write(pagePath, diskContent)

	recorder := fixed.put("/c/greyhaven/edit/"+pagePath, gmRequestor(), h1, gmBuffer)

	// Clause one: 412.
	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412; body:\n%s", recorder.Code, recorder.Body)
	}

	// Clause two: the disk is unchanged, not one byte. Read from the filesystem
	// rather than from anything the route said.
	if got := fixed.read(pagePath); got != diskContent {
		t.Errorf("the file on disk is\n%q\nwant\n%q\nA stale save wrote to the vault.",
			got, diskContent)
	}

	// Clause three: no revision row. Read from the table, because a spy would only
	// assert that a function was not called.
	if rows := fixed.revisionsFor(pagePath); len(rows) != 0 {
		t.Errorf("a refused save recorded %d revision rows (%v); S-6.2 says it "+
			"records nothing", len(rows), rows)
	}

	// And nothing next door. The whole table, not just this page's rows: a save that
	// resolved its path wrongly would land on *some* row, and asking for one path
	// would report the table as empty.
	if rows := fixed.allRevisions(); len(rows) != 0 {
		t.Errorf("a refused save left %d revision rows in the table (%v); it records "+
			"nothing at all", len(rows), rows)
	}

	// The sibling, for the same reason.
	if got := fixed.read(sibling); got != "---\ntitle: Another page\n---\nUnrelated.\n" {
		t.Errorf("a refused save changed a page it was not asked about:\n%s", got)
	}
}

// TestTheConflictCarriesTheCurrentContentAndItsHash is the other half of S-6.2: a
// mismatch returns "412 with the current body and its hash".
//
// Both halves are checked in both directions, because a response that carried the
// *buffer* in the diff instead of the disk would satisfy "carries the current
// content" as a substring test and be completely wrong. The right-hand column is the
// disk's text; the field is the GM's.
func TestTheConflictCarriesTheCurrentContentAndItsHash(t *testing.T) {
	t.Parallel()

	const (
		pagePath     = "Vault.md"
		diskContent  = "---\ntitle: A page\n---\nIron, rust and gold.\n"
		gmBuffer     = "---\ntitle: A page\n---\nIron, rust and sea-green.\n"
		diskOnlyLine = "the tide goes out"
		bufferOnly   = "Iron, rust and sea-green."
	)

	fixed := newHarness(t)
	fixed.write(pagePath, original)
	fixed.write(pagePath, diskContent+diskOnlyLine+"\n")

	recorder := fixed.put("/c/greyhaven/edit/"+pagePath, gmRequestor(), "W/\"stale\"", gmBuffer)

	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412; body:\n%s", recorder.Code, recorder.Body)
	}

	body := recorder.Body.String()

	// The current content, in the diff. Searched in the parsed tree's text so that
	// an entity-escaped rendering of the line still counts — the text *is* there,
	// however the markup spells it.
	rendered := parse(t, "a 412", body)
	all := renderedText(rendered.root)

	if !strings.Contains(all, diskOnlyLine) {
		t.Errorf("the 412 does not carry the disk's current content; it must, so that "+
			"the GM can see what they are reconciling against:\n%s", body)
	}

	// The hash, twice and in two forms. The `ETag` header is the salted validator a
	// follow-up save must present, and the hex digest in the body is the same fact
	// unsalted — S-5.3 salts the validator so a GM's and a player's are never the
	// same string, which means the validator alone answers no question about
	// content.
	validator := recorder.Header().Get("ETag")
	if validator == "" {
		t.Fatal("the 412 carries no ETag; §6.2's follow-up save needs the new hash " +
			"and the header is where a client reads it")
	}

	if got := fixed.validatorFor(pagePath); got != validator {
		t.Errorf("the 412's ETag is %q but a fresh GET reports %q; the conflict must "+
			"hand back the validator the editor would have got", validator, got)
	}

	// The digest, in the two places that carry it, each asserted in *its own*
	// element. Asserting it against the whole document would hold for a response that
	// carried it in only one of them, and a follow-up save needs the copy beside the
	// validator it goes with while a support report needs the copy a reader can see.
	digest := digestOf(t, diskContent+diskOnlyLine+"\n")

	if got := textWithin(t, rendered, "conflict-hash"); !strings.Contains(got, digest) {
		t.Errorf("the conflict's own hash line reads %q and does not carry the digest "+
			"%s; §6.2 says the 412 returns the current body *and its hash*", got, digest)
	}

	if got := textWithin(t, rendered, "editor-content-hash"); !strings.Contains(got, digest) {
		t.Errorf("the rail's hash reads %q and does not carry the digest %s; the "+
			"unsalted digest is what a reader can put into a diff tool, because S-5.3 "+
			"salt means the validator answers no question about content", got, digest)
	}

	// And the validator, in the same place, so "save with this" is one string the
	// reader can see rather than a value they have to infer.
	if got := textWithin(t, rendered, "conflict-validator"); !strings.Contains(got, validator) {
		t.Errorf("the conflict's validator line reads %q, want the validator the "+
			"follow-up save must present (%q)", got, validator)
	}

	// And the buffer is where the GM left it, not where the disk is.
	if !strings.Contains(all, bufferOnly) {
		t.Errorf("the 412 does not carry the GM's buffer; the buffer is the reason " +
			"the response is a document at all (§4.8, §11)")
	}
}

// TestTheBufferSurvivesTheConflict is §10.8's fifth automated check, and the one the
// risk table in §11 calls out by name: "the editor buffer is the user's unsaved
// work; a bug that clears it loses data".
//
// Asserted on the *parsed* textarea's value, which is stronger than asserting that a
// textarea exists: it is the only assertion here that fails if a component fills the
// field from disk instead of from the request, and that component would render
// perfectly — a textarea, with the page's text in it, and a diff beside it that no
// longer matches what the reader is looking at.
func TestTheBufferSurvivesTheConflict(t *testing.T) {
	t.Parallel()

	const (
		pagePath    = "Vault.md"
		diskContent = "---\ntitle: A page\n---\nIron, rust and gold.\n"
		// A buffer that starts with a newline, because that is the case HTML
		// itself eats: a `<textarea>` drops a newline immediately after its opening
		// tag, so a component that interpolated the buffer naively would show a
		// field one line short of the GM's — and the diff, built from the real
		// buffer, would then disagree with the field beside it by exactly one line.
		gmBuffer = "\n---\ntitle: A page\n---\nIron, rust and sea-green.\n"
	)

	fixed := newHarness(t)
	fixed.write(pagePath, diskContent)

	recorder := fixed.put("/c/greyhaven/edit/"+pagePath, gmRequestor(), "W/\"stale\"", gmBuffer)

	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412; body:\n%s", recorder.Code, recorder.Body)
	}

	rendered := parse(t, "a 412", recorder.Body.String())
	field := rendered.requireTestID("editor-textarea")

	if got := textareaValue(field); got != gmBuffer {
		t.Errorf("the textarea holds\n%q\nwant\n%q\nThe buffer did not survive the "+
			"412, and the buffer is the GM's unsaved work (§4.8, §11).", got, gmBuffer)
	}
}

// textareaValue is a `<textarea>`'s content as a browser would read it.
//
// `text` over the parsed tree, which is the whole point: `golang.org/x/net/html`
// implements the HTML rule that a newline immediately after a `<textarea>`'s opening
// tag is discarded, so the parser has *already* eaten the newline the component adds
// to preserve the GM's. Reading the raw bytes would show the workaround and reading
// the value shows what a GM sees — and this test asserts the value, because the
// buffer is the GM's unsaved work and a test that read the workaround would pass
// against a component that had lost a line.
func textareaValue(field *html.Node) string {
	return text(field)
}

// TestAMatchedSaveWritesThePageAndRecordsARevision is the success path, and it
// asserts four things S-6.4 and architecture §6.1 require: 204, the bytes on disk,
// the `page_revisions` row with `source='web'` and the GM as its author, and **a
// different `ETag`**.
//
// "Different" rather than "present" is the assertion that matters. A route that
// echoed the validator it was given would satisfy every other clause and leave a GM
// whose second save is refused against their own first save — a bug whose only
// symptom is a conflict diff against text the GM wrote themselves.
func TestAMatchedSaveWritesThePageAndRecordsARevision(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, original)

	before := fixed.validatorFor(pagePath)

	recorder := fixed.put("/c/greyhaven/edit/"+pagePath, gmRequestor(), before, replacement)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body:\n%s", recorder.Code, recorder.Body)
	}

	if recorder.Body.Len() != 0 {
		t.Errorf("a 204 carried a body:\n%s", recorder.Body)
	}

	if got := fixed.read(pagePath); got != replacement {
		t.Errorf("the file on disk is\n%q\nwant\n%q", got, replacement)
	}

	after := recorder.Header().Get("ETag")
	if after == "" {
		t.Fatal("the 204 carried no ETag; §6.4's last step is to return the new one")
	}

	if after == before {
		t.Errorf("the 204's ETag is the one that was sent (%q); it must be the "+
			"validator of the bytes just written, or the GM's next save is refused "+
			"against their own", after)
	}

	if got := fixed.validatorFor(pagePath); got != after {
		t.Errorf("a fresh GET reports %q but the save reported %q; the validator a "+
			"client is handed must be the one its own bytes produce", after, got)
	}

	rows := fixed.revisionsFor(pagePath)
	if len(rows) != 1 {
		t.Fatalf("a successful save recorded %d revision rows, want 1 (S-6.4)", len(rows))
	}

	if rows[0].Content != replacement {
		t.Errorf("the revision holds\n%q\nwant\n%q\nS-6.4 appends what was published.",
			rows[0].Content, replacement)
	}

	if rows[0].Source != "web" {
		t.Errorf("the revision's source = %q, want %q; S-6.4 fixes it", rows[0].Source, "web")
	}

	if rows[0].AuthorID != gmUser {
		t.Errorf("the revision's author = %d, want %d; the writer is the signed-in GM",
			rows[0].AuthorID, gmUser)
	}
}

// TestASecondSavePresentsTheNewValidator: the chained case, and the one that proves
// the first save's validator is not merely different but *usable*.
//
// Two saves in a row with the validator from the previous response between them. A
// route that derived its new validator from anything other than the bytes it wrote —
// from a timestamp, from a counter, from a `stat` — would pass
// `TestAMatchedSaveWritesThePageAndRecordsARevision` and fail here.
func TestASecondSavePresentsTheNewValidator(t *testing.T) {
	t.Parallel()

	const (
		pagePath = "Vault.md"
		other    = "Other.md"
	)

	fixed := newHarness(t)
	fixed.write(pagePath, original)
	fixed.write(other, "---\ntitle: Another page\n---\nUnrelated.\n")

	// A second page's validator, taken before the first save. It must be unaffected
	// by it: a validator derived from anything but the bytes of *its own* page would
	// make saving one page invalidate another, and the GM would get a 412 against text
	// they did not touch.
	otherValidator := fixed.validatorFor(other)

	first := fixed.put("/c/greyhaven/edit/"+pagePath, gmRequestor(),
		fixed.validatorFor(pagePath), replacement)
	if first.Code != http.StatusNoContent {
		t.Fatalf("the first save = %d, want 204; body:\n%s", first.Code, first.Body)
	}

	second := fixed.put("/c/greyhaven/edit/"+pagePath, gmRequestor(),
		first.Header().Get("ETag"), original+"And a new line.\n")
	if second.Code != http.StatusNoContent {
		t.Fatalf("the second save = %d, want 204; the validator from the first "+
			"response must be accepted; body:\n%s", second.Code, second.Body)
	}

	if rows := fixed.revisionsFor(pagePath); len(rows) != 2 {
		t.Errorf("two saves recorded %d revision rows, want 2", len(rows))
	}

	if got := fixed.validatorFor(other); got != otherValidator {
		t.Errorf("saving %s changed %s's validator (%q became %q); a validator must "+
			"be a function of its own page's bytes",
			pagePath, other, otherValidator, got)
	}
}

// TestAMissingIfMatchIsPreconditionRequired is RFC 6585's 428, and the reason it is
// not a 200 is the comment on `writePreconditionRequired`: a request with no
// precondition is an unconditional overwrite wearing a save's clothes.
//
// Three assertions, and the last two are the ones that make the first meaningful: a
// 428 that wrote the file would be a 428 in name only, and S-6.3's "no silent
// overwrite" is about the *effect*, not the status.
func TestAMissingIfMatchIsPreconditionRequired(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, original)

	recorder := fixed.put("/c/greyhaven/edit/"+pagePath, gmRequestor(), "", replacement)

	if recorder.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428; body:\n%s", recorder.Code, recorder.Body)
	}

	if got := fixed.read(pagePath); got != original {
		t.Errorf("a save with no precondition wrote the file:\n%s", got)
	}

	if rows := fixed.revisionsFor(pagePath); len(rows) != 0 {
		t.Errorf("a save with no precondition recorded %d revision rows", len(rows))
	}

	// And it renders no editor: a 428 is reached before the body is read, so there
	// is no buffer to echo, and an editor with an empty field would be a response
	// that invites a client to replace the GM's unsaved work in exchange for a
	// status code that already said nothing was written.
	rendered := parse(t, "a 428", recorder.Body.String())
	rendered.requireTestID("precondition-required")

	for _, node := range []*html.Node{rendered.byTestID("editor-textarea")} {
		if node != nil {
			t.Error("the 428 rendered an editor; it must not, because it has no " +
				"buffer to preserve and an empty field is a data-loss hazard")
		}
	}
}

// TestThePreconditionComparisonIsATable: what an `If-Match` may say, and what each
// spelling means.
//
// The interesting rows are the two the RFC and this project disagree about. RFC 9110
// §13.1.1 specifies the *strong* comparison function for `If-Match`, and a strict
// implementation would find this server's own weak validator never matching — so
// every save would be a 412 and the route would be a working precondition that
// refuses every request. The weak comparison is the deliberate answer, and the
// `strong spelling` row is what pins it: a client that dropped the `W/` and sent
// `"abc"` must be told the same thing.
func TestThePreconditionComparisonIsATable(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, original)

	current := fixed.validatorFor(pagePath)
	stale := `W/"0000"`

	for _, testCase := range []struct {
		name    string
		ifMatch string
		want    int
	}{
		{name: "exact", ifMatch: current, want: http.StatusNoContent},
		{name: "strong spelling", ifMatch: strings.TrimPrefix(current, "W/"), want: http.StatusNoContent},
		{name: "in a list", ifMatch: `"other", ` + current, want: http.StatusNoContent},
		{name: "star", ifMatch: "*", want: http.StatusNoContent},
		{name: "a stale digest", ifMatch: stale, want: http.StatusPreconditionFailed},
		{name: "unquoted", ifMatch: "garbage", want: http.StatusPreconditionFailed},
		// A stray comma is a list with a valid element in it, and §13.1.1's
		// condition is "any of them match". Refusing the whole header would turn a
		// typo into a 412 for a client whose validator was right.
		{name: "a stray comma in a list", ifMatch: `,` + current, want: http.StatusNoContent},
		{name: "an empty header is a 428, not here", ifMatch: "", want: http.StatusPreconditionRequired},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// A fresh harness per case, so a 204 in one row cannot leave a changed
			// file for the next row to be stale against.
			fixed := newHarness(t)
			fixed.write(pagePath, original)

			recorder := fixed.put("/c/greyhaven/edit/"+pagePath, gmRequestor(),
				testCase.ifMatch, replacement)

			if recorder.Code != testCase.want {
				t.Errorf("If-Match %q gave %d, want %d; body:\n%s",
					testCase.ifMatch, recorder.Code, testCase.want, recorder.Body)
			}

			if testCase.want != http.StatusNoContent {
				return
			}

			if got := fixed.read(pagePath); got != replacement {
				t.Errorf("a matched save did not write the file:\n%s", got)
			}
		})
	}
}

// TestThePathIsConfinedOnASave is S-3.5 on the write path, which is where it matters
// most: a read that escapes the root is a wrong page, and a *write* that escapes it
// is a file created outside the campaign.
//
// A percent-encoded `..`, because a literal one never reaches a handler — `net/http`
// cleans the request path and redirects first. So the confinement in `content.Root` is
// load-bearing rather than a second opinion, and this is the test that says so on the
// write side.
func TestThePathIsConfinedOnASave(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", original)
	// A second page in a subdirectory, so the escape attempt has a real page above
	// it and the refusal is about the *path* rather than about a missing file.
	fixed.write("notes/Safe.md", pageBody("Nothing to see.\n"))

	recorder := fixed.put(
		"/c/greyhaven/edit/notes/%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		gmRequestor(), `W/"anything"`, "written outside the campaign\n",
	)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body:\n%s", recorder.Code, recorder.Body)
	}

	// Nothing was created outside the root. The harness's directory is the campaign's
	// content root, so its *parent* is where an escape would land, and this asserts
	// the file is not there.
	escape := fixed.dir + "/../escaped.md"
	if !absentFile(escape) {
		t.Errorf("a refused write created %s", escape)
	}

	if rows := fixed.revisionsFor("../etc/passwd.md"); len(rows) != 0 {
		t.Errorf("a refused write recorded %d revision rows", len(rows))
	}

	// And the refusal does not echo the attempted path. `content.Root` refuses to put
	// it in its own error for this reason; a body that echoed it would undo that one
	// string later, and the 403 and the 412 are the two answers most easily confused
	// in a log.
	body := recorder.Body.String()

	for _, absent := range []string{"passwd", "%2e", fixed.dir} {
		if strings.Contains(body, absent) {
			t.Errorf("the refusal echoes %q, which came from the request", absent)
		}
	}
}

// TestAMissingPageIsNotFound: the editor has no create path, and that is a decision
// rather than an omission.
//
// Creating a page needs a *create-only* precondition — `If-None-Match: *` — which
// S-6.2 does not authorise, and a write to a path with no file under a validator for
// a file that does not exist would be the unconditional overwrite S-6.3 forbids. So a
// `PUT` to a path with no page is a 404, and a page is created by putting a file in
// the vault (Obsidian, or the operator's editor) — which the watcher then indexes.
func TestAMissingPageIsNotFound(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)

	get := fixed.get("/c/greyhaven/edit/Nowhere", gmRequestor())
	if get.Code != http.StatusNotFound {
		t.Errorf("GET a missing page = %d, want 404; body:\n%s", get.Code, get.Body)
	}

	put := fixed.put("/c/greyhaven/edit/Nowhere", gmRequestor(), `W/"anything"`, "new page\n")
	if put.Code != http.StatusNotFound {
		t.Errorf("PUT a missing page = %d, want 404; this route does not create "+
			"pages, because a create needs a precondition S-6.2 does not define; "+
			"body:\n%s", put.Code, put.Body)
	}

	// And no file appeared.
	if !absentFile(fixed.dir + "/Nowhere.md") {
		t.Error("a refused create wrote the file")
	}
}

// TestAnOversizedBodyIsRefused is the size cap on the write path (S-4.7).
//
// 413 rather than 500, and the difference is the direction of the blame: a file on
// disk being too large is an operator's problem, and a *request body* being too large
// is RFC 9110's answer and a fact about the request the GM can act on.
func TestAnOversizedBodyIsRefused(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, original)

	oversized := strings.Repeat("a", content.MaxDocumentBytes+1)

	recorder := fixed.put("/c/greyhaven/edit/"+pagePath, gmRequestor(),
		fixed.validatorFor(pagePath), oversized)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body:\n%s", recorder.Code, recorder.Body)
	}

	if got := fixed.read(pagePath); got != original {
		t.Errorf("an oversized save changed the file:\n%s", got)
	}

	rendered := parse(t, "a 413", recorder.Body.String())
	rendered.requireTestID("save-too-large")
}

// TestAPostToAnEditorURLIsNotAllowed: `net/http`'s own 405, asserted because the
// route registers two methods and a reader of the source should know that the third
// is refused by the mux rather than by a branch that guesses.
func TestAPostToAnEditorURLIsNotAllowed(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", original)

	recorder := fixed.request(http.MethodPost, "/c/greyhaven/edit/Vault",
		gmRequestor(), nil, "x")

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("a POST to an editor URL = %d, want 405", recorder.Code)
	}
}

// TestTheEditorAndTheWikiRouteAgreeOnTheValidator is the guard on the one piece of
// duplicated derivation in this route: the content hash.
//
// Both routes derive their validator from `content.CacheKey.ETag` over a digest of
// the file's bytes, and `edit`'s digest is its own three lines of `sha256` because
// `content`'s is unexported. Two derivations of one fact is a hazard, and the
// substantive property is not "the hash function is the hash function" — it is that
// **a GM who opened a page in the wiki can take that response's `ETag` straight to
// the editor and save with it.** If the two routes ever disagree, a GM's first save
// after reading a page is a 412 against text they did not change.
//
// The two routes are mounted on one campaign mux here, exactly as the router will
// mount them, so this is a test of the pair rather than of one of them.
func TestTheEditorAndTheWikiRouteAgreeOnTheValidator(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, original)

	campaignMux := http.NewServeMux()
	wiki.Mount(campaignMux, &wiki.Handler{
		Roots:       fixed.root,
		Renderers:   wiki.CampaignRenderers{testSlug: content.NewRenderer(testSlug, nil)},
		Pages:       noPages{},
		Redactor:    content.NoSecrets(),
		Cache:       content.NewCache(8),
		Instance:    components.InstanceView{Name: "Greyhaven", Version: "0.6.0"},
		SignOutHref: "/logout",
	})
	edit.Mount(campaignMux, fixed.handler())

	backing := campaignStore{
		campaign: domain.Campaign{
			ID: testCampID, Slug: testSlug, Name: "Greyhaven",
			Visibility: domain.VisibilityPublic,
		},
		members: map[int64]domain.Role{gmUser: domain.RoleGM, playerUser: domain.RolePlayer},
	}

	outer := http.NewServeMux()
	outer.Handle("/c/{slug}/", campaigns.Resolve(backing)(
		middleware.Chain(campaignMux, campaigns.RequireRead),
	))

	serve := middleware.RequestID(outer)

	get := func(target string, requestor domain.Requestor) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
		req = req.WithContext(identity.WithRequestor(req.Context(), requestor))

		recorder := httptest.NewRecorder()
		serve.ServeHTTP(recorder, req)

		return recorder
	}

	page := get("/c/greyhaven/wiki/Vault", gmRequestor())
	if page.Code != http.StatusOK {
		t.Fatalf("the wiki route = %d, want 200; body:\n%s", page.Code, page.Body)
	}

	editor := get("/c/greyhaven/edit/Vault", gmRequestor())
	if editor.Code != http.StatusOK {
		t.Fatalf("the editor = %d, want 200; body:\n%s", editor.Code, editor.Body)
	}

	if got, want := editor.Header().Get("ETag"), page.Header().Get("ETag"); got != want {
		t.Fatalf("the editor's validator is %q and the wiki route's is %q; the two "+
			"routes must agree for the same bytes, or a GM who read a page cannot "+
			"save it", got, want)
	}

	// And the agreement is not vacuous: the wiki route's validator for a *player*
	// must differ from the GM's (S-14.2), or "they agree" would be trivially true
	// of any constant.
	player := get("/c/greyhaven/wiki/Vault", playerRequestor())
	if player.Header().Get("ETag") == page.Header().Get("ETag") {
		t.Error("the GM and the player share a validator (S-14.2), so this agreement " +
			"test would pass against a constant")
	}
}

// TestAConflictIsReportedThroughObservability is S-12.3's enforcement for this route.
//
// The line is parsed rather than substring-matched, and two things are checked: the
// event name is `conflict.412` — the one spelling §13.2 fixes and this project
// already declares — and the line carries **no page content**. The second is the
// point of emitting through `observability.Event` rather than through a raw
// `slog.Any`: an event that carried the buffer would put a GM's draft into a log
// aggregator, which is the one thing S-12.3 forbids.
func TestAConflictIsReportedThroughObservability(t *testing.T) {
	t.Parallel()

	const (
		pagePath  = "Vault.md"
		secretish = "THE-DRAFT-THE-GM-NEVER-SAVED"
	)

	fixed := newHarness(t)
	fixed.write(pagePath, original)

	lines := &bytes.Buffer{}
	fixed.logTo(slog.NewJSONHandler(lines, nil))

	recorder := fixed.put("/c/greyhaven/edit/"+pagePath, gmRequestor(),
		"W/\"stale\"", secretish+"\n")
	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412; body:\n%s", recorder.Code, recorder.Body)
	}

	if lines.Len() == 0 {
		t.Fatal("a 412 wrote no log line; `conflict.412` is the signal an operator " +
			"alerts on, and an elevated rate means Obsidian and the web editor are fighting")
	}

	sawConflict := false

	for line := range strings.SplitSeq(strings.TrimSpace(lines.String()), "\n") {
		var record map[string]any

		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("the log line is not JSON: %q", line)
		}

		if record["event"] == "conflict.412" {
			sawConflict = true
		}

		for key, value := range record {
			if text, isText := value.(string); isText && strings.Contains(text, secretish) {
				t.Errorf("the %s attribute carries the GM's unsaved buffer; S-12.3 "+
					"forbids an event carrying file contents", key)
			}
		}
	}

	if !sawConflict {
		t.Errorf("no line carried event=conflict.412; the lines were:\n%s", lines)
	}
}

// textWithin is the text of the element carrying a test hook, failing if the hook is
// not in the document exactly once.
func textWithin(t *testing.T, rendered *document, testID string) string {
	t.Helper()

	return text(rendered.requireTestID(testID))
}

// renderedText is a node's descendant text, for assertions that care about the words
// rather than the markup.
func renderedText(node *html.Node) string {
	var out strings.Builder

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			out.WriteString(current.Data)
			out.WriteByte('\n')
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return out.String()
}

// digestOf is the hex SHA-256 of a page's bytes, computed here so the test does not
// assert on the route's own function.
//
// Deliberately spelled out rather than shared with the route: the test's value must
// come from somewhere the route did not write, or a change to the route's derivation
// would change both sides of the assertion at once and the test would still pass.
func digestOf(t *testing.T, body string) string {
	t.Helper()

	sum := sha256.Sum256([]byte(body))

	return hex.EncodeToString(sum[:])
}

// absentFile reports whether a path is not there, named so the confinement test
// reads as a question about a path rather than as a syscall.
//
// A missing file is the *expected* answer on every call, so the error is not
// inspected: the assertion is about the file's absence, and a permission error would
// satisfy it wrongly. Hence the explicit `os.Stat` and the `os.IsNotExist` check
// below rather than `os.ReadFile`.
func absentFile(name string) bool {
	_, err := os.Stat(name)

	return errors.Is(err, os.ErrNotExist)
}
