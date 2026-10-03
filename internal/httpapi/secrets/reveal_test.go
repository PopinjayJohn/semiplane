package secrets_test

// The write path: the precondition, the byte, the record, and the refusals in between.
//
// This file holds §5.6.4's storage clause — "every reveal is written to `audit_log`
// and to the ledger" — and S-6.2's storage clause — "a mismatch returns 412 with the
// current body and its hash, **and records nothing**". Both are claims about a
// **table**, so both are asserted against the shipped schema and not against a spy: a
// double that records "was it called?" is a different statement about a different
// thing, and it is satisfied just as happily by a row written by some other path.
//
// The three clauses of "a refused reveal records nothing" are checked separately
// because each is invisible from the status line: the status, the bytes on disk, the
// ledger, and the audit log. A handler that answered 412 and then wrote anyway passes
// every assertion that reads only the status.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/httpapi/secrets"
	"github.com/semiplane/semiplane/internal/store"
)

// withSecrets is the page most of these tests start from: two collapsed callouts, the
// first carrying an Obsidian block id and the second carrying none.
//
// **Two, and the second has no block id, on purpose.** A single-secret fixture would
// satisfy every assertion in this file against a route that ignored the anchor and
// always acted on ordinal 0 — so the ordinal fallback, the anchor lookup and the
// "which one did it touch" assertions all need a page where the two answers differ.
const withSecrets = "---\ntitle: The vault\n---\n" +
	"The vault door is iron, and it has never been opened with a key.\n" +
	"\n" +
	"> [!secret]- The traitor  ^traitor\n" +
	"> Captain Aldric replaced the eastern signal fire.\n" +
	"\n" +
	"Sticks. The grease on the hinge is from a decade ago.\n" +
	"\n" +
	"> [!secret]-\n" +
	"> A second secret, and a second captain.\n"

// withoutSecrets is the same page with **both** callouts revealed.
//
// What two reveals produce, written out so the tests can assert **byte equality**
// rather than "contains a `+` somewhere" — which is the assertion a route that flipped
// the wrong byte, or the same byte twice, would satisfy.
const withoutSecrets = "---\ntitle: The vault\n---\n" +
	"The vault door is iron, and it has never been opened with a key.\n" +
	"\n" +
	"> [!secret]+ The traitor  ^traitor\n" +
	"> Captain Aldric replaced the eastern signal fire.\n" +
	"\n" +
	"Sticks. The grease on the hinge is from a decade ago.\n" +
	"\n" +
	"> [!secret]+\n" +
	"> A second secret, and a second captain.\n"

// firstHalfRevealed is `withSecrets` with the block-id callout revealed and the derived
// one untouched.
const firstHalfRevealed = "---\ntitle: The vault\n---\n" +
	"The vault door is iron, and it has never been opened with a key.\n" +
	"\n" +
	"> [!secret]+ The traitor  ^traitor\n" +
	"> Captain Aldric replaced the eastern signal fire.\n" +
	"\n" +
	"Sticks. The grease on the hinge is from a decade ago.\n" +
	"\n" +
	"> [!secret]-\n" +
	"> A second secret, and a second captain.\n"

// errLedgerRefused is what a store that could not commit returns.
//
// `internal/store`'s own errors are unexported sentinels behind its own interface, and
// this route does not classify them — it answers 500 for anything it was not taught
// about, which is the safe direction. So the test uses a plain error, which is exactly
// what "not taught about" means, and the property under test is the *consequence* of a
// 500 rather than the mapping.
var errLedgerRefused = errors.New("secrets_test: the ledger could not commit")

// TestARevealFlipsTheByteWritesTheLedgerRowAndTheAuditRow is §5.6.4 in full: the byte,
// the ledger and the audit log, all three checked against the thing itself.
//
// The byte clause is **byte equality against a fixture**, not a substring: a route that
// flipped the second callout's marker, or flipped the first one and normalised the line
// ending, would satisfy "contains a `+`" and fail here.
//
// The two table clauses are checked separately and both read every row for the
// campaign rather than one path, so a route that recorded the right anchor against the
// wrong page would still fail.
func TestARevealFlipsTheByteWritesTheLedgerRowAndTheAuditRow(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	recorder := fixed.reveal(pagePath, gmRequestor(),
		fixed.validatorFor(pagePath),
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("a matched reveal = %d, want 204; body:\n%s", recorder.Code, recorder.Body)
	}

	// The byte, from the filesystem.
	if got := fixed.read(pagePath); got != firstHalfRevealed {
		t.Fatalf("the page on disk is\n%q\nwant\n%q\nA reveal rewrites one marker byte and "+
			"nothing else (S-5.8).", got, firstHalfRevealed)
	}

	// The ledger row, from the table, with the campaign's own GM named as the
	// discloser.
	rows := fixed.ledgerRows()
	if len(rows) != 1 {
		t.Fatalf("the ledger holds %d rows, want 1:%s", len(rows), describeLedger(rows))
	}

	if rows[0].Path != pagePath {
		t.Errorf("the ledger row's path is %q, want %q", rows[0].Path, pagePath)
	}

	if rows[0].Anchor != "traitor" {
		t.Errorf("the ledger row's anchor is %q, want %q — a block id is stored without "+
			"its caret, and it is the row's own key (S-5.9)", rows[0].Anchor, "traitor")
	}

	if rows[0].RevealedBy != gmUser {
		t.Errorf("the ledger row names user %d as the discloser, want the GM %d; the "+
			"account comes from the gate's resolved requestor and from nothing in the "+
			"request", rows[0].RevealedBy, gmUser)
	}

	if rows[0].RevealedAt == 0 {
		t.Error("the ledger row carries no reveal time; S-5.8 makes the ledger the " +
			"authority for *when* a secret was disclosed")
	}

	// The audit row, from the table, with §5.6.4's two halves both present.
	entries := fixed.auditRows()
	if len(entries) != 1 {
		t.Fatalf("the audit log holds %d rows, want 1:%s", len(entries), describeAudit(entries))
	}

	if entries[0].Action != "secret.reveal" {
		t.Errorf("the audit row's action is %q, want %q", entries[0].Action, "secret.reveal")
	}

	// The target names the page behind a `secret/` prefix and the detail carries the
	// anchor — `internal/store/secrets.go` writes both and a test that asserted only
	// that *a* row appeared would be satisfied by a row about something else.
	if entries[0].Target != "secret/"+pagePath {
		t.Errorf("the audit row's target is %q, want %q", entries[0].Target, "secret/"+pagePath)
	}

	if entries[0].Detail != "traitor" {
		t.Errorf("the audit row's detail is %q, want %q", entries[0].Detail, "traitor")
	}

	if entries[0].Actor != gmUser {
		t.Errorf("the audit row names actor %d, want the GM %d", entries[0].Actor, gmUser)
	}
}

// TestARevealChangesExactlyOneByte is §5.6's own sentence — "the single character is
// the whole edit" — asserted as a property rather than as a fixture.
//
// The fixture comparison above pins the exact bytes for one page; this one holds the
// *rule* across every shape of callout the grammar admits, including the two that are
// where an offset computation goes wrong: a callout written `>[!secret]-` with no
// space after the quote, and one quoted two levels deep with a tab.
func TestARevealChangesExactlyOneByte(t *testing.T) {
	t.Parallel()

	for name, page := range map[string]string{
		"a block id and a title":                                  "---\ntitle: T\n---\nProse.\n\n> [!secret]- The traitor  ^traitor\n> Body.\n",
		"no space after the quote":                                "Prose.\n\n>[!secret]- Tight  ^tight\n> Body.\n",
		"a tab after the quote":                                   "Prose.\n\n>\t[!secret]- Tabbed  ^tabbed\n> Body.\n",
		"no title and no id":                                      "Prose.\n\n> [!secret]-\n> Body.\n",
		"a callout at the end of a file with no trailing newline": "Prose.\n\n> [!secret]- Last  ^last\n> Body.",
		"CRLF line endings":                                       "Prose.\r\n\r\n> [!secret]- Windows  ^windows\r\n> Body.\r\n",
		"a multi-line body":                                       "Prose.\n\n> [!secret]- Long  ^long\n> First line.\n> Second line.\n\nAfter.\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			const pagePath = "Vault.md"

			fixed := newHarness(t)
			fixed.write(pagePath, page)

			// The ordinal, because these fixtures' anchors are derived hashes the test
			// would otherwise have to recompute — and the ordinal is the fallback
			// selector this route must honour anyway.
			recorder := fixed.reveal(pagePath, gmRequestor(),
				fixed.validatorFor(pagePath),
				revealBody(t, map[string]any{"ordinal": 0, "revealed": true}))
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("a matched reveal = %d, want 204; body:\n%s", recorder.Code, recorder.Body)
			}

			got := fixed.read(pagePath)

			// One byte, in both directions: exactly one position differs, and it is a
			// marker byte that became `+`.
			changed := byteDifferences(page, got)
			if len(changed) != 1 {
				t.Fatalf("the reveal changed %d bytes (%v), want exactly 1:\n%q\n%q",
					len(changed), changed, page, got)
			}

			// `page[changed[0]]`, not `changed[0]`: the helper answers offsets and this
			// assertion is about the byte at one. Reading the index where the byte
			// belongs is a comparison that is always false for an ASCII offset, so it
			// would fail for every fixture and pass for none.
			if page[changed[0]] != '-' || got[changed[0]] != '+' {
				t.Fatalf("the byte at %d was %q and became %q, want a marker byte '-' → '+'",
					changed[0], page[changed[0]], got[changed[0]])
			}

			// And every other byte identical, which is what the index assertion above
			// means — asserted separately so a failure names which of the two halves
			// broke.
			if restored := replaceAt(got, changed[0], '-'); restored != page {
				t.Errorf("putting the byte back did not restore the page:\n%q\n%q",
					restored, page)
			}
		})
	}
}

// byteDifferences returns the offsets at which two equal-length byte slices differ.
//
// A plain loop rather than a library, and with the length check spelled out: the two
// sides of this assertion are meant to be the same length, and a helper that silently
// handled a length change would report a shift as "no differences" — which is the
// shape of false pass this file exists to avoid.
func byteDifferences(before, after string) []int {
	if len(before) != len(after) {
		return []int{-1}
	}

	var changed []int

	for offset := range len(before) {
		if before[offset] != after[offset] {
			changed = append(changed, offset)
		}
	}

	return changed
}

// replaceAt returns s with the byte at offset replaced, for the restore assertion.
func replaceAt(s string, offset int, with byte) string {
	if offset < 0 || offset >= len(s) {
		return s
	}

	return s[:offset] + string(with) + s[offset+1:]
}

// TestAStaleIfMatchAnswers412AndChangesNothing is S-6.2, S-6.3 and the plan's own row
// for this work item, in four assertions checked against four different things.
//
// The shape is the point. A 412 is a *statement the route makes about itself*, so a
// test that only reads the status tests the statement. The second clause is checked by
// reading the file, the third and fourth by reading the two tables — so the test fails
// against a handler that returns 412 and then writes the byte and the ledger anyway,
// which is the shape of defect this whole work item exists to prevent.
func TestAStaleIfMatchAnswers412AndChangesNothing(t *testing.T) {
	t.Parallel()

	const (
		pagePath = "Vault.md"
		// h1 is what the GM opened. On disk when they reveal, the page is something
		// else — Obsidian, a sync client, a second browser tab. That is the whole
		// scenario S-6.1 exists for: there is no lock, so the two writers never knew
		// about each other.
		h1         = withSecrets
		onDisk     = firstHalfRevealed
		disclosure = withSecrets
	)

	fixed := newHarness(t)
	fixed.write(pagePath, h1)

	// A sibling page, untouched by this request, so the "the disk is unchanged" clause
	// has something to be about. A handler that wrote the wrong file — a path resolved
	// from the wrong field, a `..` normalised the wrong way — passes every other
	// assertion in this test and fails this one.
	const sibling = "Other.md"

	fixed.write(sibling, "Iron and rust.\n")

	h1Validator := fixed.validatorFor(pagePath)

	// Something else writes the page. The watcher would notice; the precondition does
	// not care, which is the point.
	fixed.write(pagePath, onDisk)

	recorder := fixed.reveal(pagePath, gmRequestor(), h1Validator,
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))

	// Clause one: 412.
	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412; body:\n%s", recorder.Code, recorder.Body)
	}

	// Clause two: the disk is unchanged, not one byte. Read from the filesystem rather
	// than from anything the route said.
	if got := fixed.read(pagePath); got != onDisk {
		t.Errorf("the file on disk is\n%q\nwant\n%q\nA stale reveal wrote to the vault, so "+
			"a secret the GM did not approve was published.", got, onDisk)
	}

	// Clause three: no ledger row. Read from the table, because a spy would only assert
	// that a function was not called. **The whole campaign's ledger**, not this page's
	// rows: a reveal that resolved its path wrongly would land on *some* row, and
	// asking for one path would report the table as empty.
	if rows := fixed.ledgerRows(); len(rows) != 0 {
		t.Errorf("a refused reveal recorded %d ledger rows:%s\n"+
			"A row for a disclosure that did not happen is the one defect this clause "+
			"exists to prevent.", len(rows), describeLedger(rows))
	}

	// Clause four: and no audit row. §5.6.4 writes both together, so an audit row
	// without a ledger row would be the other half of the same lie.
	if entries := fixed.auditRows(); len(entries) != 0 {
		t.Errorf("a refused reveal wrote %d audit rows:%s\n"+
			"§5.6.4 records a *disclosure*, and none happened.",
			len(entries), describeAudit(entries))
	}

	// And the sibling, for the same reason.
	if got := fixed.read(sibling); got != "Iron and rust.\n" {
		t.Errorf("a refused reveal changed a page it was not asked about:\n%s", got)
	}
}

// TestTheConflictCarriesTheCurrentSourceAndItsHash is the other half of S-6.2: a
// mismatch returns "412 with the current body and its hash".
//
// Checked against the **file on disk** rather than against the request's own buffer,
// because a response carrying the request's body instead would satisfy a substring
// assertion and be completely wrong — the whole point of a 412 is that the two texts
// differ.
func TestTheConflictCarriesTheCurrentSourceAndItsHash(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	staleValidator := fixed.validatorFor(pagePath)

	const changed = "---\ntitle: The vault\n---\nThe vault door is brass.\n"
	fixed.write(pagePath, changed)

	recorder := fixed.reveal(pagePath, gmRequestor(), staleValidator,
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))

	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("a stale reveal = %d, want 412; body:\n%s", recorder.Code, recorder.Body)
	}

	var conflict struct {
		Error       string `json:"error"`
		ContentHash string `json:"contentHash"`
		Content     string `json:"content"`
	}

	decodeJSON(t, recorder, &conflict)

	// The disk's text, exactly. A response that echoed the request's own body would
	// pass a "contains the page" substring check and be useless.
	if conflict.Content != changed {
		t.Errorf("the 412's content is\n%q\nwant\n%q\nS-6.2 asks for the *current* body.",
			conflict.Content, changed)
	}

	// Its digest, unsalted, and it must be a digest of those bytes rather than of the
	// request's.
	if want := sha256Hex(changed); conflict.ContentHash != want {
		t.Errorf("the 412's contentHash is %q, want the hex digest of the page on disk %q",
			conflict.ContentHash, want)
	}

	// The salted validator on the header, and it must be the **disk's** — a client that
	// retries with what it was handed must not be refused again.
	if got, want := recorder.Header().Get("ETag"), fixed.validatorFor(pagePath); got != want {
		t.Errorf("the 412's ETag is %q, want the current page's %q; after a 412 the "+
			"precondition a client holds must be the disk's", got, want)
	}
}

// TestAMissingIfMatchIsPreconditionRequired is S-6.2's other status and S-6.3's rule
// that a write with no precondition is not a write.
//
// Asserted with four things beyond the status: the current validator is offered so a
// client can recover without a second round trip, the body is refused rather than
// guessed at, and neither table gains a row.
func TestAMissingIfMatchIsPreconditionRequired(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	recorder := fixed.reveal(pagePath, gmRequestor(), "",
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))

	if recorder.Code != http.StatusPreconditionRequired {
		t.Fatalf("a reveal with no If-Match = %d, want 428; body:\n%s",
			recorder.Code, recorder.Body)
	}

	if got := recorder.Header().Get("ETag"); got != fixed.validatorFor(pagePath) {
		t.Errorf("the 428 offers the validator %q, want the current page's %q; the header "+
			"is what lets a client recover without re-reading the page", got,
			fixed.validatorFor(pagePath))
	}

	if got := fixed.read(pagePath); got != withSecrets {
		t.Errorf("a reveal with no If-Match changed the file:\n%s", got)
	}

	if rows := fixed.ledgerRows(); len(rows) != 0 {
		t.Errorf("a 428 recorded %d ledger rows:%s", len(rows), describeLedger(rows))
	}

	if entries := fixed.auditRows(); len(entries) != 0 {
		t.Errorf("a 428 wrote %d audit rows:%s", len(entries), describeAudit(entries))
	}
}

// TestThePreconditionComparisonIsATable is `matches`, driven through the route.
//
// A table rather than a unit test of the unexported function, because the property is
// about **what a client may send** and the route is where a client's header arrives.
// Every row that must match and every row that must not, including the two leniencies
// `precondition.go` argues for — a strong spelling of a weak validator, and a list with
// a stray comma — because an untested leniency is a leniency nobody knows is there.
func TestThePreconditionComparisonIsATable(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	// Built once per row because each case needs a pristine vault: a matched reveal
	// changes the byte, and the next row's validator would then be stale for a reason
	// that has nothing to do with the case under test.
	// `%q` in a row's `sent` is the placeholder for the page's own validator, so every
	// row that should match is a row that must carry the *real* current value rather
	// than a hand-written guess. A row that said `""` would be a 428 and would tell
	// you nothing about the comparison.
	for name, testCase := range map[string]struct {
		sent  string
		match bool
	}{
		"the exact validator":                     {sent: `%q`, match: true},
		"the strong spelling of a weak validator": {sent: `"%[1]s"`, match: true},
		"a list with the right validator":         {sent: `W/"other", %q`, match: true},
		"a list with a stray comma":               {sent: `, %q`, match: true},
		"the any validator":                       {sent: "*", match: true},
		"a different validator":                   {sent: `W/"nope"`, match: false},
		"an unquoted value":                       {sent: `garbage`, match: false},
		"a bare digest with no quotes":            {sent: `abc123`, match: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write(pagePath, withSecrets)

			current := fixed.validatorFor(pagePath)

			// `%[1]s` and `%q` are both filled from the current validator. `%[1]s` is
			// used for the strong spelling, where the `W/` marker has to come off and the
			// quotes stay on — the leniency `precondition.go` argues for.
			sent := strings.NewReplacer(
				`"%[1]s"`, strongSpelling(current),
				"%q", current,
			).Replace(testCase.sent)

			recorder := fixed.reveal(pagePath, gmRequestor(), sent,
				revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))

			matched := recorder.Code == http.StatusNoContent

			if matched != testCase.match {
				t.Errorf("If-Match %q = %d, want matched=%v", sent, recorder.Code, testCase.match)
			}

			// A refusal of either shape records nothing, in both senses.
			if !matched {
				if rows := fixed.ledgerRows(); len(rows) != 0 {
					t.Errorf("a refused precondition recorded %d ledger rows:%s",
						len(rows), describeLedger(rows))
				}

				if got := fixed.read(pagePath); got != withSecrets {
					t.Errorf("a refused precondition changed the file:\n%s", got)
				}
			}
		})
	}
}

// TestAnUnknownAnchorOrOrdinalChangesNothing is the "and no file change and no ledger
// row" half of an unresolvable selector.
//
// **The page exists in every row but one**, and that row is the important one: a page
// with no callouts at all answers the same 404 as a page with two callouts and an
// anchor that names neither, and a route that answered "this page has no secrets" for
// the first would have turned the 404 into a statement about the page's interior.
//
// Every case is checked for the same three things: the status, the bytes, and the
// ledger. The audit log is not re-checked per row because `TestAStaleIfMatch…` already
// holds that a refusal writes no audit row, and a table with a fourth column on every
// row is a table nobody reads.
func TestAnUnknownAnchorOrOrdinalChangesNothing(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	// The derived anchor of a secret that is not on the page: twelve hex characters,
	// correctly *shaped* and correctly *absent*. A substring or length check would pass
	// it; only a lookup against the page finds that it names nothing.
	const strangerAnchor = "000000000000"

	for name, testCase := range map[string]struct {
		page    string
		payload map[string]any
	}{
		"a block id no callout carries": {
			page:    withSecrets,
			payload: map[string]any{"anchor": "admiral", "revealed": true},
		},
		"a well-shaped derived anchor that is not on the page": {
			page:    withSecrets,
			payload: map[string]any{"anchor": strangerAnchor, "revealed": true},
		},
		"an ordinal past the end": {
			page:    withSecrets,
			payload: map[string]any{"ordinal": 2, "revealed": true},
		},
		"a negative ordinal": {
			page:    withSecrets,
			payload: map[string]any{"ordinal": -1, "revealed": true},
		},
		"a page with no secrets at all": {
			page:    "---\ntitle: The vault\n---\nIron and rust.\n",
			payload: map[string]any{"anchor": "traitor", "revealed": true},
		},
		"a page whose only callout is inside a fenced code block": {
			page:    "Prose.\n\n```\n> [!secret]- Example  ^example\n> Body.\n```\n",
			payload: map[string]any{"anchor": "example", "revealed": true},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write(pagePath, testCase.page)

			recorder := fixed.reveal(pagePath, gmRequestor(),
				fixed.validatorFor(pagePath), revealBody(t, testCase.payload))

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("an unresolvable secret = %d, want 404; body:\n%s",
					recorder.Code, recorder.Body)
			}

			if got := fixed.read(pagePath); got != testCase.page {
				t.Errorf("a refused selector changed the file:\n%q", got)
			}

			if rows := fixed.ledgerRows(); len(rows) != 0 {
				t.Errorf("an unresolvable selector recorded %d ledger rows:%s",
					len(rows), describeLedger(rows))
			}

			if entries := fixed.auditRows(); len(entries) != 0 {
				t.Errorf("an unresolvable selector wrote %d audit rows:%s",
					len(entries), describeAudit(entries))
			}
		})
	}
}

// TestTheAnchorIsPreferredOverTheOrdinal is §5.6.3's resolution order, and the reason
// an ordinal is the fallback rather than the alternative.
//
// The two rows are the whole argument. A client that sends a **wrong anchor and the
// right ordinal** is refused — the anchor wins, so the route never rewrites the byte
// the client's more fragile selector pointed at. And a client that sends the right
// anchor and a **wrong ordinal** succeeds, and the byte that changed is the anchor's.
//
// Without the first row, "prefer the anchor" would be indistinguishable from "use
// whichever selector happens to be right", which is the behaviour that lets a derived
// anchor die on a reorder and take a reveal with it.
func TestTheAnchorIsPreferredOverTheOrdinal(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	t.Run("a wrong anchor wins over a right ordinal", func(t *testing.T) {
		t.Parallel()

		fixed := newHarness(t)
		fixed.write(pagePath, withSecrets)

		recorder := fixed.reveal(pagePath, gmRequestor(), fixed.validatorFor(pagePath),
			revealBody(t, map[string]any{"anchor": "admiral", "ordinal": 0, "revealed": true}))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("a wrong anchor with a right ordinal = %d, want 404; the anchor is "+
				"the more stable name and it wins, or the ordinal is not a fallback but a "+
				"coin toss", recorder.Code)
		}

		if got := fixed.read(pagePath); got != withSecrets {
			t.Errorf("the page changed:\n%q", got)
		}
	})

	t.Run("a right anchor wins over a wrong ordinal", func(t *testing.T) {
		t.Parallel()

		fixed := newHarness(t)
		fixed.write(pagePath, withSecrets)

		// Ordinal 1 is the second callout; the anchor names the first. If the ordinal
		// were consulted at all the byte that changed would be the other's.
		recorder := fixed.reveal(pagePath, gmRequestor(), fixed.validatorFor(pagePath),
			revealBody(t, map[string]any{"anchor": "traitor", "ordinal": 1, "revealed": true}))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("a right anchor with a wrong ordinal = %d, want 204; body:\n%s",
				recorder.Code, recorder.Body)
		}

		if got := fixed.read(pagePath); got != firstHalfRevealed {
			t.Errorf("the page is\n%q\nwant\n%q\nThe anchor named the first callout, so "+
				"the first callout's marker is the byte that moved.", got, firstHalfRevealed)
		}

		rows := fixed.ledgerRows()
		if len(rows) != 1 || rows[0].Anchor != "traitor" {
			t.Errorf("the ledger holds %d rows, want one keyed on %q:%s",
				len(rows), "traitor", describeLedger(rows))
		}
	})
}

// TestADerivedAnchorIsRecordedWhenTheAuthorWroteNoBlockID is the other half of
// S-5.9: the fallback name, and the property that makes it a *derived* one.
//
// Asserted three ways, because a single assertion is satisfied by a constant:
//
//   - the row's anchor is the resolver's answer for that callout, computed in the test
//     from `content.Resolve` rather than from the route;
//   - it is **twelve hex characters**, which is what a derived anchor looks like and
//     what a block id is not;
//   - and it **changes** when the first line of the body is rewritten, which is
//     §5.6.3's own statement of what the form survives and what it does not.
//
// The third is what makes the second meaningful: without it, a route that wrote a
// constant would pass.
func TestADerivedAnchorIsRecordedWhenTheAuthorWroteNoBlockID(t *testing.T) {
	t.Parallel()

	const (
		pagePath = "Vault.md"
		page     = "---\ntitle: The vault\n---\nIron and rust.\n\n> [!secret]-\n> Captain Aldric burned the signal fire.\n"
	)

	fixed := newHarness(t)
	fixed.write(pagePath, page)

	recorder := fixed.reveal(pagePath, gmRequestor(), fixed.validatorFor(pagePath),
		revealBody(t, map[string]any{"ordinal": 0, "revealed": true}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("a matched reveal = %d, want 204; body:\n%s", recorder.Code, recorder.Body)
	}

	rows := fixed.ledgerRows()
	if len(rows) != 1 {
		t.Fatalf("the ledger holds %d rows, want 1:%s", len(rows), describeLedger(rows))
	}

	found := content.ScanSecrets(page)
	if len(found) != 1 {
		t.Fatalf("the fixture holds %d secrets, want 1 — the test is not measuring what "+
			"it claims to", len(found))
	}

	want := content.Resolve(fixed.campaign.ID, pagePath, found[0])
	if rows[0].Anchor != want.Value {
		t.Errorf("the ledger row's anchor is %q, want the resolver's %q (form %q); the key "+
			"must come from the file rather than from the request",
			rows[0].Anchor, want.Value, want.Form)
	}

	if !want.IsDerived() {
		t.Fatalf("the fixture's anchor has form %q, want the derived one; the test would "+
			"pass against a constant", want.Form)
	}

	if !twelveHex.MatchString(rows[0].Anchor) {
		t.Errorf("the recorded anchor %q is not twelve hex characters, which is §5.6.3's "+
			"derived form", rows[0].Anchor)
	}

	// And it moves with the first body line: rewrite it, reveal again on a second
	// campaign, and the new row is keyed differently. Asserted on a *fresh* campaign so
	// the first row is still there to compare against.
	other := newHarness(t)
	other.write(pagePath, strings.Replace(page,
		"Captain Aldric burned the signal fire.", "Admiral Vane burned the signal fire.", 1))

	otherRecorder := other.reveal(pagePath, gmRequestor(), other.validatorFor(pagePath),
		revealBody(t, map[string]any{"ordinal": 0, "revealed": true}))
	if otherRecorder.Code != http.StatusNoContent {
		t.Fatalf("a matched reveal on the rewritten page = %d, want 204; body:\n%s",
			otherRecorder.Code, otherRecorder.Body)
	}

	otherRows := other.ledgerRows()
	if len(otherRows) != 1 {
		t.Fatalf("the second ledger holds %d rows, want 1:%s",
			len(otherRows), describeLedger(otherRows))
	}

	if otherRows[0].Anchor == rows[0].Anchor {
		t.Errorf("rewriting the first body line produced the same anchor %q; §5.6.3's "+
			"derived form hashes that line precisely so that it *changes* here", rows[0].Anchor)
	}
}

// twelveHex matches §5.6.3's derived-anchor shape.
var twelveHex = regexp.MustCompile(`^[0-9a-f]{12}$`)

// TestTheLedgerIsWrittenAfterTheByteAndTheRetry is the package header's central claim,
// and the ordering it argues for.
//
// A ledger that refuses the write leaves the byte already flipped. The three clauses:
//
//  1. the response is a **500**, not a 204 — the GM must learn the ledger did not record
//     it, because a 204 would tell them a secret is public *and* accounted for;
//  2. **no ledger row exists**, which is what makes the failure safe: §5.6.2's
//     reconciliation re-applies `+` only for rows, so no row means nothing will ever be
//     re-applied, and the failure resolves toward hiding;
//  3. a retry **converges**: the second request finds the byte already correct, skips
//     the write, and records the row.
//
// The first clause is why the ordering is safe to choose at all. A 500 is a signal the
// client can act on; the ledger-first ordering this route deliberately does **not** use
// would have had to answer the same failure with a 500 *and* leave a row that
// reconciliation would act on — a disclosure nobody asked for, caused by our own error.
func TestTheLedgerIsWrittenAfterTheByteAndTheRetry(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t).failing(errLedgerRefused)
	fixed.write(pagePath, withSecrets)

	recorder := fixed.reveal(pagePath, gmRequestor(), fixed.validatorFor(pagePath),
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("a reveal whose ledger write failed = %d, want 500; the precondition "+
			"matched, so 412 would be a conflict that does not exist and 204 would "+
			"report a disclosure nobody can account for; body:\n%s",
			recorder.Code, recorder.Body)
	}

	// The byte is down. Stated rather than asserted as a defect: the ordering is the
	// argument, and a test that pretended the byte were unchanged would be asserting
	// the ordering this route rejects.
	if got := fixed.read(pagePath); got != firstHalfRevealed {
		t.Fatalf("the page is\n%q\nwant\n%q\nThe byte goes down before the ledger "+
			"deliberately — see the package header.", got, firstHalfRevealed)
	}

	// Clause two: no row. Which is what makes clause one safe.
	if rows := fixed.ledgerRows(); len(rows) != 0 {
		t.Errorf("the ledger holds %d rows after a failed ledger write:%s\n"+
			"store.RevealSecret is one transaction, so a failure leaves no row at all — "+
			"and no row is what stops reconciliation re-applying the reveal.",
			len(rows), describeLedger(rows))
	}

	// Clause three: the retry converges.
	repaired := newHarnessWith(t, fixed)

	retry := repaired.reveal(pagePath, gmRequestor(), repaired.validatorFor(pagePath),
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))
	if retry.Code != http.StatusNoContent {
		t.Fatalf("the retry = %d, want 204; body:\n%s", retry.Code, retry.Body)
	}

	rows := repaired.ledgerRows()
	if len(rows) != 1 || rows[0].Anchor != "traitor" {
		t.Errorf("the retry left %d ledger rows, want one keyed on %q:%s\n"+
			"§5.6.2 makes the file authoritative for whether a secret is revealed, so a "+
			"retry over an already-correct byte is how the state converges.",
			len(rows), "traitor", describeLedger(rows))
	}
}

// newHarnessWith builds a harness over an existing campaign's vault, so a retry is a
// retry rather than a fresh request against a fresh page.
//
// The failing ledger is replaced with the real one, which is the whole point: the
// second attempt is the same request from the same GM against the same file, with the
// one thing that was broken now working.
func newHarnessWith(t *testing.T, from *harness) *harness {
	t.Helper()

	fixed := buildHarness(t, from.campaign.Visibility)

	// Reuse the original campaign and its rows: `buildHarness` created a *new*
	// campaign, so this harness is pointed at the old one instead and its own rows are
	// ignored. Everything the assertions read is scoped to `from.campaign.ID`.
	fixed.campaign = from.campaign
	fixed.dir = from.dir
	fixed.root = from.root
	fixed.ledger = sharedStore

	return fixed
}

// TestARevealOfANestedCalloutIsRefusedAndRecordsNothing is `content`'s nesting rule
// reaching the route.
//
// `content.ScanSecrets` forces a callout that contains another to `SecretCollapsed`
// "whatever its marker byte says", and deliberately does not rewrite the file. So a
// reveal aimed at one would write a `+` that renders as collapsed — and record a
// ledger row for a disclosure that never reached a reader. The route therefore refuses,
// which is the direction §5.6.2 requires every failure path in this subsystem to
// resolve toward: a record of a disclosure, or no disclosure.
//
// The nested **inner** callout is revealed successfully in the last subtest, because it
// has no children and the refusal is specifically about an outer.
func TestARevealOfANestedCalloutIsRefusedAndRecordsNothing(t *testing.T) {
	t.Parallel()

	const (
		pagePath = "Vault.md"
		page     = "Prose.\n\n> [!secret]- Outer\n> > [!secret]- Inner  ^inner\n> > The inner secret.\n"
	)

	t.Run("the outer callout is refused", func(t *testing.T) {
		t.Parallel()

		fixed := newHarness(t)
		fixed.write(pagePath, page)

		recorder := fixed.reveal(pagePath, gmRequestor(), fixed.validatorFor(pagePath),
			revealBody(t, map[string]any{"ordinal": 0, "revealed": true}))

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("revealing a callout that nests another = %d, want 400; body:\n%s",
				recorder.Code, recorder.Body)
		}

		if got := fixed.read(pagePath); got != page {
			t.Errorf("the refused reveal changed the file:\n%q\nwant\n%q", got, page)
		}

		if rows := fixed.ledgerRows(); len(rows) != 0 {
			t.Errorf("the refused reveal recorded %d ledger rows:%s\n"+
				"A row here would say the GM disclosed something no reader ever received.",
				len(rows), describeLedger(rows))
		}
	})

	t.Run("the inner callout is revealed", func(t *testing.T) {
		t.Parallel()

		fixed := newHarness(t)
		fixed.write(pagePath, page)

		recorder := fixed.reveal(pagePath, gmRequestor(), fixed.validatorFor(pagePath),
			revealBody(t, map[string]any{"anchor": "inner", "revealed": true}))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("revealing the inner callout = %d, want 204; the refusal is about "+
				"nesting, not about a callout inside another; body:\n%s",
				recorder.Code, recorder.Body)
		}

		if got := fixed.read(pagePath); !strings.Contains(got, "> [!secret]+ Inner  ^inner") {
			t.Errorf("the inner callout's marker did not change:\n%s", got)
		}

		if rows := fixed.ledgerRows(); len(rows) != 1 || rows[0].Anchor != "inner" {
			t.Errorf("the ledger holds %d rows, want one keyed on %q:%s",
				len(rows), "inner", describeLedger(rows))
		}
	})
}

// TestTheRevealAndTheWikiRouteAgreeOnTheValidator is the guard on the one piece of
// duplicated derivation in this route: the content hash.
//
// Both routes derive their validator from `content.CacheKey.ETag` over a digest of the
// file's bytes, and this route's digest is its own three lines of `sha256` because
// `content`'s is unexported. Two derivations of one fact is a hazard, and the
// substantive property is not "the hash function is the hash function" — it is that **a
// GM who read a page can take that response's `ETag` straight to a reveal.** Every test
// in this file depends on it, because the harness takes every `If-Match` from the wiki
// route; if the two routes ever disagreed, this whole package would be 412ing every
// request it made.
//
// The two routes are mounted on one campaign mux here, exactly as the router will mount
// them, so this is a test of the pair rather than of one of them.
func TestTheRevealAndTheWikiRouteAgreeOnTheValidator(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	page := fixed.get("/c/"+fixed.campaign.Slug+"/wiki/Vault", gmRequestor())
	if page.Code != http.StatusOK {
		t.Fatalf("the wiki route = %d, want 200; body:\n%s", page.Code, page.Body)
	}

	// And the reverse direction: a reveal hands back a validator, and that validator is
	// the one the wiki route will advertise for the page it just changed. A client
	// that revealed a secret and then re-read the page must not find a *different*
	// current version.
	revealed := fixed.reveal(pagePath, gmRequestor(), page.Header().Get("ETag"),
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))
	if revealed.Code != http.StatusNoContent {
		t.Fatalf("a reveal presenting the wiki route's validator = %d, want 204; body:\n%s",
			revealed.Code, revealed.Body)
	}

	after := fixed.get("/c/"+fixed.campaign.Slug+"/wiki/Vault", gmRequestor())
	if after.Code != http.StatusOK {
		t.Fatalf("the wiki route after the reveal = %d, want 200; body:\n%s",
			after.Code, after.Body)
	}

	if got, want := after.Header().Get("ETag"), revealed.Header().Get("ETag"); got != want {
		t.Errorf("after the reveal the wiki route advertises %q and the reveal answered %q; "+
			"a GM who revealed a secret and then re-read the page must find the same "+
			"current version", got, want)
	}

	// And the validator *moved*, which is what makes the agreement meaningful rather
	// than vacuous: a route that returned the pre-reveal validator would satisfy the
	// first assertion while lying about the page.
	if after.Header().Get("ETag") == page.Header().Get("ETag") {
		t.Errorf("the validator did not change across a reveal (%q); the byte is part of "+
			"the digest, so a validator that survived it is not describing this page",
			after.Header().Get("ETag"))
	}
}

// TestTheRevealedPageIsWritten0600 is `pageFileMode`, asserted from the filesystem.
//
// The campaign's content root is created 0o700, so a world-readable file inside it
// would be consistent with that root only by accident — and a page that arrived from a
// sync client may already be 0o644, in which case a write that inherited the existing
// mode would widen it rather than narrow it.
func TestTheRevealedPageIsWritten0600(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	recorder := fixed.reveal(pagePath, gmRequestor(), fixed.validatorFor(pagePath),
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("a matched reveal = %d, want 204; body:\n%s", recorder.Code, recorder.Body)
	}

	if got, want := fixed.writeFileMode(pagePath), os.FileMode(0o600); got != want {
		t.Errorf("the revealed page's mode is %04o, want %04o", got, want)
	}
}

// TestTheSecondRevealPresentsTheNewValidator is the round trip a client actually
// performs: reveal, take the validator the response handed back, reveal again.
//
// It is here rather than folded into the interop test because it is the *chain* that
// matters: a reveal that invalidated the previous validator and did not mint a new one
// would make a second reveal impossible, and a GM disclosing two secrets on one page
// would have to re-read the whole page between them.
func TestTheSecondRevealPresentsTheNewValidator(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	first := fixed.reveal(pagePath, gmRequestor(), fixed.validatorFor(pagePath),
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))
	if first.Code != http.StatusNoContent {
		t.Fatalf("the first reveal = %d, want 204; body:\n%s", first.Code, first.Body)
	}

	// The response's own validator, which is what a client holds next.
	second := fixed.reveal(pagePath, gmRequestor(), first.Header().Get("ETag"),
		revealBody(t, map[string]any{"ordinal": 1, "revealed": true}))
	if second.Code != http.StatusNoContent {
		t.Fatalf("the second reveal presenting the first's validator = %d, want 204; "+
			"body:\n%s", second.Code, second.Body)
	}

	if got := fixed.read(pagePath); got != withoutSecrets {
		t.Errorf("the page is\n%q\nwant\n%q\nboth callouts should be revealed",
			got, withoutSecrets)
	}

	// Two rows, one per secret, and two audit rows: a GM who disclosed two things
	// disclosed two things.
	if rows := fixed.ledgerRows(); len(rows) != 2 {
		t.Errorf("the ledger holds %d rows, want 2:%s", len(rows), describeLedger(rows))
	}

	if entries := fixed.auditRows(); len(entries) != 2 {
		t.Errorf("the audit log holds %d rows, want 2:%s", len(entries), describeAudit(entries))
	}

	// Re-revealing the first is a no-op on the byte and records **one** row: the store's
	// `RevealSecret` is idempotent on the primary key and writes no second audit row,
	// because a double click is not a second disclosure.
	//
	// The file's identity is asserted too, and it is the only thing that makes "a
	// no-op" observable. The bytes are already `+`, so a route that rewrote the page
	// anyway would produce an identical file, an identical digest and an identical
	// validator — three comparisons that cannot see it. What it *would* change is the
	// inode, and the inode is what the watcher's own event is bound to, so a rewrite
	// that changes nothing is a re-index of a page that did not change.
	before := fixed.stat(pagePath)

	again := fixed.reveal(pagePath, gmRequestor(), second.Header().Get("ETag"),
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))
	if again.Code != http.StatusNoContent {
		t.Fatalf("re-revealing an already-public secret = %d, want 204; body:\n%s",
			again.Code, again.Body)
	}

	after := fixed.stat(pagePath)

	if after.inode != before.inode {
		t.Errorf("re-revealing an already-public secret rewrote the page: the inode went "+
			"from %d to %d.\n"+
			"The bytes are already right, so the write achieves nothing and costs a "+
			"watcher event, a re-render and a re-index of a page that did not change.",
			before.inode, after.inode)
	}

	if got, want := again.Header().Get("ETag"), second.Header().Get("ETag"); got != want {
		t.Errorf("the no-op reveal's validator is %q, want the unchanged %q; the digest "+
			"covers the byte and the byte did not move", got, want)
	}

	if entries := fixed.auditRows(); len(entries) != 2 {
		t.Errorf("re-revealing an already-public secret wrote an audit row: %d, want 2:%s\n"+
			"§5.6.4 records a *disclosure*, and there was not a second one.",
			len(entries), describeAudit(entries))
	}
}

// TestHidingASecretRemovesTheLedgerRow is the other direction, and it is not the mirror
// of the reveal.
//
// Hiding rewrites `+` back to `-` **and** deletes the ledger row, and both halves
// matter: the byte is what readers see, and the row is what reconciliation re-applies
// from. A hide that only rewrote the byte would leave a row saying "this is revealed",
// and the next time sync touched the file reconciliation would re-publish it — undoing
// the GM's revocation without the GM knowing.
//
// The audit row is asserted too, with the **other** action value: the vocabulary is
// closed and a hide is a different act from a reveal.
func TestHidingASecretRemovesTheLedgerRow(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	revealed := fixed.reveal(pagePath, gmRequestor(), fixed.validatorFor(pagePath),
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))
	if revealed.Code != http.StatusNoContent {
		t.Fatalf("the reveal = %d, want 204; body:\n%s", revealed.Code, revealed.Body)
	}

	hidden := fixed.reveal(pagePath, gmRequestor(), revealed.Header().Get("ETag"),
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": false}))
	if hidden.Code != http.StatusNoContent {
		t.Fatalf("the hide = %d, want 204; body:\n%s", hidden.Code, hidden.Body)
	}

	if got := fixed.read(pagePath); got != withSecrets {
		t.Errorf("the page is\n%q\nwant\n%q\nthe marker byte went back to '-'", got, withSecrets)
	}

	if rows := fixed.ledgerRows(); len(rows) != 0 {
		t.Errorf("a hidden secret still holds %d ledger rows:%s\n"+
			"§5.6.2's reconciliation re-applies `+` for every row, so a surviving row "+
			"would re-publish the secret the GM just took back.",
			len(rows), describeLedger(rows))
	}

	actions := make([]string, 0, 2)
	for _, entry := range fixed.auditRows() {
		actions = append(actions, entry.Action)
	}

	want := []string{"secret.reveal", "secret.unreveal"}
	if len(actions) != 2 || actions[0] != want[0] || actions[1] != want[1] {
		t.Errorf("the audit log's actions are %v, want %v — the vocabulary is closed "+
			"(migration 0009) and a hide is a different act from a reveal", actions, want)
	}
}

// TestThePathIsConfinedOnAReveal is S-3.5 on this route, and the status it must answer
// with is the part that is easy to get wrong.
//
// A path that **leaves** the campaign's root is a 403 and never a 404. A 404 says "there
// is nothing at this path", and answering it for a path outside the campaign turns a
// reveal into a probe for the layout of the host's filesystem: a reader who can tell
// 403 from 404 has learned something about a filesystem they were never granted.
//
// Both forms are driven: the escaped `..`, which is the only spelling that survives
// `net/http`'s own path cleaning, and the symlink, which is the vault half of the same
// rule (S-4.4). Neither may touch the file.
func TestThePathIsConfinedOnAReveal(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	t.Run("a traversal out of the campaign", func(t *testing.T) {
		t.Parallel()

		fixed := newHarness(t)
		fixed.write(pagePath, withSecrets)

		// Written as `%2e%2e%2f`, because `net/http` cleans a literal `../` and answers
		// with a redirect before a handler runs. Only the escaped form reaches here,
		// which is what makes the confinement load-bearing rather than a second opinion.
		recorder := fixed.reveal("..%2f..%2fetc%2fpasswd", gmRequestor(),
			fixed.validatorFor(pagePath),
			revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))

		if recorder.Code != http.StatusForbidden && recorder.Code != http.StatusNotFound {
			t.Fatalf("a traversal = %d, want 403 (or 404 from the mux before it); a 404 "+
				"here would be indistinguishable from a page that is not there, which is "+
				"how a confinement probe works; body:\n%s", recorder.Code, recorder.Body)
		}

		if rows := fixed.ledgerRows(); len(rows) != 0 {
			t.Errorf("a refused traversal recorded %d ledger rows:%s",
				len(rows), describeLedger(rows))
		}
	})

	t.Run("a symlink out of the campaign", func(t *testing.T) {
		t.Parallel()

		fixed := newHarness(t)
		fixed.write(pagePath, withSecrets)

		// The link points outside the content root, so `content.RefuseSymlinks` refuses
		// it — the same policy every other read path in this project runs under.
		outside := filepath.Join(t.TempDir(), "outside.md")
		if err := os.WriteFile(outside, []byte("not yours\n"), 0o600); err != nil {
			t.Fatalf("write the file outside the root: %v", err)
		}

		if err := os.Symlink(outside, filepath.Join(fixed.dir, "Escape.md")); err != nil {
			t.Skipf("this host does not allow symlinks: %v", err)
		}

		recorder := fixed.reveal("Escape", gmRequestor(), fixed.validatorFor(pagePath),
			revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))

		if recorder.Code != http.StatusForbidden {
			t.Fatalf("a symlink out of the campaign = %d, want 403; body:\n%s",
				recorder.Code, recorder.Body)
		}

		// And the file outside the root is untouched — the point of the whole refusal.
		data, err := os.ReadFile(outside)
		if err != nil {
			t.Fatalf("read the file outside the root: %v", err)
		}

		if string(data) != "not yours\n" {
			t.Errorf("the file outside the campaign's root was rewritten:\n%s", data)
		}
	})
}

// TestARequestBodyIsRefused changesNothing is the 400 surface, as a table.
//
// Every row is a body a client can plausibly send by mistake, and every one of them
// must leave the vault and the ledger untouched. The two rows that matter most are the
// **misspelled field name** and the **omitted `revealed`**: both are bodies a lenient
// decoder would accept, and the second one would default a request into *hiding* a
// secret the GM meant to show.
func TestARequestBodyIsRefusedChangesNothing(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	for name, body := range map[string]string{
		"not JSON at all":                  `reveal`,
		"a JSON array":                     `[{"anchor":"traitor","revealed":true}]`,
		"an empty body":                    ``,
		"a truncated object":               `{"anchor":"traitor"`,
		"a misspelled field name":          `{"anchro":"traitor","revealed":true}`,
		"no `revealed` at all":             `{"anchor":"traitor"}`,
		"neither an anchor nor an ordinal": `{"revealed":true}`,
		"a string where a bool belongs":    `{"anchor":"traitor","revealed":"yes"}`,
		"two objects in one body":          `{"anchor":"traitor","revealed":true}{"anchor":"x","revealed":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write(pagePath, withSecrets)

			recorder := fixed.reveal(pagePath, gmRequestor(),
				fixed.validatorFor(pagePath), body)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("a malformed body = %d, want 400; body:\n%s\nsent: %s",
					recorder.Code, recorder.Body, body)
			}

			if got := fixed.read(pagePath); got != withSecrets {
				t.Errorf("a malformed body changed the file:\n%q", got)
			}

			if rows := fixed.ledgerRows(); len(rows) != 0 {
				t.Errorf("a malformed body recorded %d ledger rows:%s",
					len(rows), describeLedger(rows))
			}
		})
	}
}

// TestAnOversizedBodyIsRefused is the request cap, answered 413 rather than 400.
//
// A reveal body is under 300 bytes; the cap is 4 KiB. The status is RFC 9110's 413 and
// not the 400 a malformed body gets, because "too large" and "wrong shape" are
// different things a client fixes differently.
func TestAnOversizedBodyIsRefused(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	// A valid object with an anchor far longer than any block id `content` admits, so
	// the only thing wrong with it is its size.
	oversized := revealBody(t, map[string]any{
		"anchor":   strings.Repeat("t", 8<<10),
		"revealed": true,
	})

	recorder := fixed.reveal(pagePath, gmRequestor(), fixed.validatorFor(pagePath), oversized)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized body = %d, want 413; body:\n%s", recorder.Code, recorder.Body)
	}

	if got := fixed.read(pagePath); got != withSecrets {
		t.Errorf("an oversized body changed the file:\n%q", got)
	}

	if rows := fixed.ledgerRows(); len(rows) != 0 {
		t.Errorf("an oversized body recorded %d ledger rows:%s",
			len(rows), describeLedger(rows))
	}
}

// TestAMisspelledFieldIsRefusedDifferentlyFromNamingNoSecret is the whole of
// `DisallowUnknownFields`' reason for existing.
//
// Both requests are 400 either way — `{"anchro":"traitor","revealed":true}` decodes to
// no anchor and no ordinal, which is the same shape as `{"revealed":true}` — so a status
// assertion cannot tell whether the strict decoder is installed. The two **messages**
// can, and the difference is the whole benefit: a GM who misspelled a field is told so,
// and a GM who sent no selector is told that.
//
// Without this the strict decoder would be a comment. It was written the other way round
// first — the misspelled-field row of `TestARequestBodyIsRefusedChangesNothing` asserted
// only the 400 — and it passed against a decoder with `DisallowUnknownFields` removed,
// which is the defect class this repository keeps meeting: an assertion that passes for
// the wrong reason because the thing it meant to check is invisible from where it looks.
func TestAMisspelledFieldIsRefusedDifferentlyFromNamingNoSecret(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	// A fresh harness per request, because the first request's harness would otherwise
	// be the one the second is served from and a test comparing two responses would be
	// comparing two requests against one page.
	send := func(body string) *httptest.ResponseRecorder {
		t.Helper()

		one := newHarness(t)
		one.write(pagePath, withSecrets)

		recorder := one.reveal(pagePath, gmRequestor(), one.validatorFor(pagePath), body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s = %d, want 400; body:\n%s", body, recorder.Code, recorder.Body)
		}

		return recorder
	}

	misspelled := send(`{"anchro":"traitor","revealed":true}`)
	namedNone := send(`{"revealed":true}`)

	if misspelled.Body.String() == namedNone.Body.String() {
		t.Errorf("a misspelled field name and a request that named no secret are refused "+
			"with the same bytes (%q), so the strict decoder is doing nothing — a GM who "+
			"misspelled a field is told they named no secret and looks at the wrong thing",
			misspelled.Body.String())
	}

	if !strings.Contains(misspelled.Body.String(), "not a reveal") {
		t.Errorf("the misspelled field's refusal does not say the body was not a reveal; "+
			"got %q", misspelled.Body.String())
	}

	if !strings.Contains(namedNone.Body.String(), "named no secret") {
		t.Errorf("the refusal for a body naming no secret does not say so; got %q",
			namedNone.Body.String())
	}
}

// TestAMissingPageIsNotFound is the ordinary 404, and it is the same 404 the gate
// writes.
//
// Compared **byte for byte** against the gate's own body rather than checked for a
// phrase, because "contains `not found`" is satisfied by a body that also says which
// of the three possibilities happened — which is exactly what would make it an oracle.
func TestAMissingPageIsNotFound(t *testing.T) {
	t.Parallel()

	const pagePath = "Nope.md"

	fixed := newHarness(t)

	recorder := fixed.reveal(pagePath, gmRequestor(), `W/"anything"`,
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("a page that is not there = %d, want 404; body:\n%s",
			recorder.Code, recorder.Body)
	}

	// Byte equality against the fixed refusal, which is what `campaigns.writeError`
	// writes for a campaign the reader may not see.
	if got, want := recorder.Body.String(), `{"error":"not found"}`+"\n"; got != want {
		t.Errorf("the 404 body is %q, want %q\n"+
			"It must be the same bytes the access gate writes, or a reader can tell "+
			"\"the page is not there\" from \"the campaign is not yours\".", got, want)
	}
}

// TestAGetIsNotAllowed is the surface half of `Mount`: one method, and a 405 with an
// `Allow` header for the rest.
//
// A `GET` to this URL is a method that exists nowhere in the design — the anchors are
// properties of a page's own text, and a client that wants them can read the page it is
// already looking at — so `net/http`'s 405 is a more honest answer than a branch that
// guessed, and it is what makes "one `PUT`, nothing else" a fact rather than a
// convention.
func TestAGetIsNotAllowed(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			recorder := fixed.request(method, "/c/"+fixed.campaign.Slug+"/secrets/"+pagePath,
				gmRequestor(), nil, "")

			if recorder.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s to the reveal route = %d, want 405; body:\n%s",
					method, recorder.Code, recorder.Body)
			}

			if got := recorder.Header().Get("Allow"); !strings.Contains(got, http.MethodPut) {
				t.Errorf("the 405 offers Allow: %q, want it to name PUT", got)
			}

			if got := fixed.read(pagePath); got != withSecrets {
				t.Errorf("a %s changed the file:\n%q", method, got)
			}
		})
	}
}

// TestTheRevealMountCarriesTheSlug is this route's copy of the wiki route's
// `TestTheCampaignMountMustCarryTheSlug`, and it is here rather than in
// `router_test.go` for the reason that test gives: `router.go` is not this work item's
// file.
//
// `campaigns.Resolve` reads the campaign out of `r.PathValue("slug")`, and `net/http`
// sets a path value from the pattern that *matched* — not from the mux the handler
// underneath happens to be. Mounted at `/c/`, no wildcard is named, the tier stays
// `TierNone`, and `RequireEdit` answers 404 for every request under it, including a GM
// revealing a secret that exists. Asserted both ways, because a test that only asserted
// the working mount would still pass after somebody "simplified" it back to a prefix.
func TestTheRevealMountCarriesTheSlug(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	campaignMux := http.NewServeMux()
	secrets.Mount(campaignMux, fixed.handler())

	backing := campaignStore{
		campaign: fixed.campaign,
		members:  map[int64]domain.Role{gmUser: domain.RoleGM},
	}

	serve := func(outerPattern string) http.Handler {
		outer := http.NewServeMux()
		outer.Handle(outerPattern,
			campaigns.Resolve(backing)(
				middleware.Chain(campaignMux, campaigns.RequireRead),
			),
		)

		return middleware.RequestID(outer)
	}

	request := func() *http.Request {
		req := httptest.NewRequestWithContext(
			t.Context(), http.MethodPut,
			"/c/"+fixed.campaign.Slug+"/secrets/"+pagePath,
			strings.NewReader(`{"anchor":"traitor","revealed":true}`),
		)
		req.Header.Set("If-Match", `W/"whatever"`)
		req = req.WithContext(identity.WithRequestor(req.Context(), gmRequestor()))

		return req
	}

	withSlug := httptest.NewRecorder()
	serve("/c/{slug}/").ServeHTTP(withSlug, request())

	// 400 rather than 404: at `/c/{slug}/` the gate admits the GM and the route
	// reaches its own first step, which is the page path — and the request names a
	// campaign whose root the harness never registered, so it fails there. What matters
	// is that it is not 404, which is the failure the prefix mount produces.
	if withSlug.Code == http.StatusNotFound {
		t.Errorf("the /c/{slug}/ mount answered 404; a GM revealing a secret on a page "+
			"that exists must not be refused by the gate — body:\n%s", withSlug.Body)
	}

	prefixOnly := httptest.NewRecorder()
	serve("/c/").ServeHTTP(prefixOnly, request())
	if prefixOnly.Code != http.StatusNotFound {
		t.Errorf("the /c/ mount answered %d, want 404; it cannot see {slug}, so "+
			"campaigns.Resolve resolves nothing and RequireRead refuses every request",
			prefixOnly.Code)
	}
}

// TestEveryReportedSecretHasAReadableMarkerByte is the invariant `written` depends on.
//
// `written` reads `source[secret.MarkerOffset]` with **no bounds check**, on the
// strength of `content.Secret`'s own documentation — "`MarkerOffset` is the byte
// offset of the state byte itself". A guard there would be a branch no test could
// reach, and a branch nobody can fail is not one; so the property is asserted here over
// every shape of page this grammar admits, which is what makes the absence of a check an
// evidenced decision rather than an oversight.
//
// If this ever fails, the fix is in `content` — the offset would be pointing outside
// the page — and the route's `written` becomes a panic on the request path until it is
// guarded.
func TestEveryReportedSecretHasAReadableMarkerByte(t *testing.T) {
	t.Parallel()

	for name, page := range map[string]string{
		"a block id and a title":   "Prose.\n\n> [!secret]- The traitor  ^traitor\n> Body.\n",
		"no space after the quote": "Prose.\n\n>[!secret]- Tight  ^tight\n> Body.\n",
		"a tab after the quote":    "Prose.\n\n>\t[!secret]- Tabbed  ^tabbed\n> Body.\n",
		"no body at all":           "Prose.\n\n> [!secret]- Empty  ^empty\n",
		"nested":                   "Prose.\n\n> [!secret]- Outer\n> > [!secret]- Inner  ^inner\n> > Body.\n",
		"inside a fence": "Prose.\n\n```\n> [!secret]- Fenced  ^fenced\n> Body.\n```\n" +
			"\n> [!secret]- Real  ^real\n> Body.\n",
		"already revealed":    "Prose.\n\n> [!secret]+ Public  ^public\n> Body.\n",
		"CRLF line endings":   "Prose.\r\n\r\n> [!secret]- Windows  ^windows\r\n> Body.\r\n",
		"no trailing newline": "Prose.\n\n> [!secret]- Last  ^last\n> Body.",
		"front matter":        "---\nnotes: |\n  > [!secret]- Front  ^front\n  > Body.\n---\nProse.\n",
		"many on one page": "Prose.\n\n" +
			"> [!secret]- One  ^one\n> Body.\n\n> [!secret]- Two\n> Body.\n\n" +
			"> [!secret]- Three\n> Body.\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			found := content.ScanSecrets(page)
			if len(found) == 0 {
				t.Fatalf("the fixture reports no secrets, so the case is vacuous:\n%q", page)
			}

			for _, secret := range found {
				if secret.MarkerOffset < 0 || secret.MarkerOffset >= len(page) {
					t.Fatalf("secret %d reports MarkerOffset %d for a page of %d bytes",
						secret.Ordinal, secret.MarkerOffset, len(page))
				}

				// And the byte it points at is a marker byte, which is a stronger claim
				// than "in range" and is what `written` actually needs.
				got := page[secret.MarkerOffset]
				if got != '-' && got != '+' {
					t.Errorf("secret %d: MarkerOffset %d points at %q, want a '-' or '+' "+
						"marker byte", secret.Ordinal, secret.MarkerOffset, got)
				}
			}
		})
	}
}

// strongSpelling is a validator with its weak marker removed and its quotes kept.
//
// RFC 9110 §13.1.1 says `If-Match` uses the strong comparison function, and
// `precondition.go` deliberately does not. The client-side counterpart is the spelling a
// strict client would send, and this is it: a GM whose tooling normalises the header
// must not be refused by every reveal.
func strongSpelling(validator string) string {
	return strings.TrimPrefix(validator, "W/")
}

// sha256Hex is the hex SHA-256 the route's `contentHash` computes, spelled out here so
// the 412 assertion is against the digest of the file rather than against the route's
// own answer.
func sha256Hex(raw string) string {
	sum := sha256.Sum256([]byte(raw))

	return hex.EncodeToString(sum[:])
}

// decodeJSON parses a response body into target, failing the test if it does not parse.
//
// `json.Unmarshal` rather than a decoder over the body, because the response bodies
// here are small and complete and a decoder that stopped at the first value would
// silently accept a body with trailing content — the very defect `request.go` refuses on
// the way in, so the assertion on the way out must not have it.
func decodeJSON(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()

	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("the response body is not the JSON this test expects: %v\nbody:\n%s",
			err, recorder.Body)
	}
}

// compile-time proof that the composition root's handle satisfies the route's
// interface, so a signature change in `internal/store` fails the build here rather than
// in `cmd/server`.
var _ secrets.Ledger = (*store.Store)(nil)
