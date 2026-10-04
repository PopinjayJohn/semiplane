package secrets_test

// The release gate: S-14.1, as the delivery plan assigns it to this path.
//
// `internal/content/redact_test.go` holds S-5.6 at the level of the redactor, and
// `internal/httpapi/wiki/route_a11y_test.go` holds it at the level of a rendered page.
// **This file holds it at the level of the endpoint that makes secrets public**, which is
// a third and separate level, and the reason it is separate is that this route is the only
// surface in the product where a `[!secret]` can *become* public.
//
// # Why the table is {reader} × {status} and not {reader} × {representation}
//
// The plan writes H1 as "a table over {GM, player, anonymous} × {HTML body, headers,
// comments, JSON payload}". **This route serves no HTML at all** — it is a JSON API with
// no `GET` — so two of those four representations do not exist here, and asserting them
// would be asserting the absence of an HTML document rather than the absence of secret
// text. What replaces them is the axis that matters more for this route: the *status*. A
// 204, a 412, a 428, a 400, a 404 and a 500 are six different bodies written by six
// different branches, and the 412's is the only one that legitimately contains the page.
//
// So the table here is {reader} × {status} over every status this route can answer, and
// each cell is checked at **three levels**: the raw bytes, every header value, and every
// log line the route wrote. The third is the one no other package's version of this gate
// covers, and it is here because a disclosure to a log aggregator is the same disclosure
// with a longer lifetime.
//
// # The one exception, and it is a real one
//
// A **GM's** 412 carries the page's source, because S-6.2 requires "412 with the current
// body and its hash" and the current body of a `[!secret]` page is the secret. So the GM
// column is not "no secret text anywhere": it is "no secret text anywhere except the one
// response S-6.2 names", and that response is asserted **positively** rather than excused
// here. A gate with a silent exception is a gate whose exception nobody re-checks when the
// surface changes.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
)

// logCapture is a `slog.Handler` that keeps every line it is given, so the assertions can
// read them.
//
// **Attributes included, and that is the whole point.** The route's lines carry the path
// and the anchor as attributes; a capture that kept only `record.Message` would see
// "secrets.revealed" and nothing else, and a route that had put a secret in an attribute
// would pass every log assertion in this package.
//
// **A mutex, not a bare slice.** The route logs from the request goroutine and
// `t.Parallel()` runs the cells concurrently, so an unsynchronised append here is a data
// race that `-race` would report as a flaky failure in a file it has nothing to do with.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

// Enabled reports that the handler wants every level, so a test can capture a debug line.
//
// Every level, including debug: `secrets.resolved` is a debug line and it is one of the
// two places a route could most plausibly put the anchor's context, so a capture that
// filtered at Warn would miss it.
func (*logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }

// Handle records one rendered log line, message and attributes alike.
func (c *logCapture) Handle(_ context.Context, record slog.Record) error {
	var out strings.Builder

	out.WriteString(record.Message)
	out.WriteString(" ")

	record.Attrs(func(attr slog.Attr) bool {
		out.WriteString(attr.Key)
		out.WriteString("=")
		out.WriteString(attr.Value.String())
		out.WriteString(" ")

		return true
	})

	c.mu.Lock()
	defer c.mu.Unlock()

	c.lines = append(c.lines, out.String())

	return nil
}

// WithAttrs implements `slog.Handler` and returns the receiver: the capture keeps whole
// lines, so there is nothing to add to a later one.
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }

// WithGroup implements `slog.Handler` and returns the receiver, for the same reason.
func (c *logCapture) WithGroup(string) slog.Handler { return c }

// captured returns every line recorded so far, one per line.
func (c *logCapture) captured() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return strings.Join(c.lines, "\n")
}

// TestTheSecretTextNeverReachesAReaderWhoMayNotSeeIt is S-14.1 and the release gate, as a
// table over readers and statuses.
//
// **The positive control comes first, and it is a control rather than a formality.** The
// fixture's GM is shown a 412 that legitimately carries the page, and the test proves the
// detector finds the secret in such a response *before* using that detector to assert its
// absence everywhere else. Without it the whole table would be satisfied by a detector
// that cannot see anything — the exact failure `internal/content/redact_test.go` records
// having shipped once (`TestTheAbsenceChecksAreNotVacuous`).
//
// Three levels per cell, and the third is the one this package adds:
//
//   - **the raw body bytes**, because a secret escaped, split across a JSON string or
//     hidden in an attribute is still in the bytes, and `strings.Contains` over the whole
//     value cannot be fooled by structure;
//   - **every header value**, because a secret in a header is a secret in a log
//     aggregator and in every echo a proxy makes;
//   - **every log line**, because this route writes its own and the temptation is
//     specific: "which callout did that" is a question whose answer is the callout's own
//     text, and the project's rule is that no event and no error may carry page content
//     (S-12.3).
func TestTheSecretTextNeverReachesAReaderWhoMayNotSeeIt(t *testing.T) {
	t.Parallel()

	// --- The positive control. ---------------------------------------------------
	control := newHarness(t)
	control.write("Vault.md", withSecrets)

	stale := control.validatorFor("Vault.md")
	control.write("Vault.md", firstHalfRevealed)

	controlCapture := &logCapture{}
	control.logger = slog.New(controlCapture)

	controlResponse := control.reveal("Vault.md", gmRequestor(), stale,
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))

	if controlResponse.Code != http.StatusPreconditionFailed {
		t.Fatalf("the control answered %d, want 412; body:\n%s",
			controlResponse.Code, controlResponse.Body)
	}

	if !carriesAnyFragment(controlResponse.Body.String()) {
		t.Fatalf("the GM's 412 does not carry the page at all, so every absence "+
			"assertion below is being made with a detector that cannot see a secret:\n%s",
			controlResponse.Body)
	}

	// And the detector is not simply matching the request back: the request named the
	// anchor `traitor`, which is a block id and **not** one of the fragments, and the
	// fragments are body text and a title.
	if carriesAnyFragment(controlCapture.captured()) {
		t.Errorf("the route's log lines carry a secret fragment:\n%s", controlCapture.captured())
	}

	// --- The table. ---------------------------------------------------------------
	readers := map[string]domain.Requestor{
		"the GM":                 gmRequestor(),
		"a player":               playerRequestor(),
		"an anonymous reader":    anonymousRequestor(),
		"a signed-in non-member": strangerRequestor(),
	}

	for readerName, reader := range readers {
		for _, request := range revealRequests(t) {
			t.Run(readerName+" / "+request.name, func(t *testing.T) {
				t.Parallel()

				fixed, header := request.build(t)
				capture := &logCapture{}
				fixed.logger = slog.New(capture)

				recorder := fixed.request(http.MethodPut,
					"/c/"+fixed.campaign.Slug+"/secrets/"+request.target,
					reader, header, request.body)

				// The status first, so a cell that never reached the branch it names
				// fails loudly instead of passing its absence assertions vacuously. **The
				// gate's own statuses are accepted for the non-GM columns**: a player is
				// refused 403 and a stranger 404 before the route runs, and a gate
				// response is still a response that must carry no secret text.
				if readerName == "the GM" && recorder.Code != request.want {
					t.Fatalf("the GM's %s = %d, want %d; body:\n%s",
						request.name, recorder.Code, request.want, recorder.Body)
				}

				if readerName != "the GM" && recorder.Code < 400 {
					t.Fatalf("%s's %s = %d, which is not a refusal at all",
						readerName, request.name, recorder.Code)
				}

				if request.mayCarryPage && readerName == "the GM" {
					// The one cell allowed to carry the page, asserted **positively**, so
					// the exception is a fact this suite holds rather than a hole in a
					// rule. A future change that stopped sending the source fails here
					// rather than passing silently.
					if !carriesAnyFragment(recorder.Body.String()) {
						t.Errorf("the GM's 412 no longer carries the page's source, so "+
							"S-6.2's \"412 with the current body\" is not being answered:\n%s",
							recorder.Body)
					}

					return
				}

				where := readerName + " / " + request.name
				assertNoSecretText(t, where+" (the body)", recorder.Body.String())
				assertNoSecretText(t, where+" (the headers)", headersOf(recorder.Header()))
				assertNoSecretText(t, where+" (the log)", capture.captured())
			})
		}
	}
}

// revealRequest is one request that produces one of this route's statuses.
type revealRequest struct {
	// name identifies the row in the table and in a failure message.
	name string
	// target is the page path after `/secrets/`.
	target string
	// body is the request body.
	body string
	// want is the status the GM must receive.
	want int
	// build assembles a fresh harness and the header this request needs. A function
	// rather than a string because three of the six rows need the page's *own* validator
	// — and a row that sends a stale one is answered 412 instead of reaching the branch
	// it names, which is how two rows of this table were wrong before they were fixed.
	build func(t *testing.T) (*harness, http.Header)
	// mayCarryPage is true for exactly one row, and the reason is S-6.2.
	mayCarryPage bool
}

// revealRequests is the status axis of the gate table.
//
// **Every status this route can answer**, and the list is read off `write.go`'s five
// writers rather than off the handler's branches, so a new writer is a missing row rather
// than an uncovered one.
//
// The two rows that are *not* statuses the route raises on its own — the 403 for a path
// that leaves the root and the 500 for a ledger that refuses — are here because they are
// the two that reach a writer a careless reader would not think of.
func revealRequests(t *testing.T) []revealRequest {
	t.Helper()

	const pagePath = "Vault.md"

	// The nested-callout page, whose refusal is a 400 rather than a 404.
	const nestedPath = "Nested.md"

	nestedPage := "Prose.\n\n> [!secret]- Outer\n> > [!secret]- Inner  ^inner\n> > Body.\n"

	// `current` is the header for a row that must get past the precondition, and it
	// takes the **target's** path rather than a closed-over one. Two rows below aim at a
	// page other than `Vault.md`, and sending them `Vault.md`'s validator earns them a 412
	// instead of the status they name — which is what happened, and why the parameter is
	// here.
	current := func(fixed *harness, target string) http.Header {
		return http.Header{
			"If-Match": {fixed.validatorFor(strings.TrimSuffix(target, ".md"))},
		}
	}

	return []revealRequest{
		{
			name:   "204 a matched reveal",
			target: pagePath,
			body:   revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			want:   http.StatusNoContent,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, current(fixed, pagePath)
			},
		},
		{
			name:         "412 a stale precondition",
			target:       pagePath,
			body:         revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			want:         http.StatusPreconditionFailed,
			mayCarryPage: true,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, http.Header{"If-Match": {`W/"nope"`}}
			},
		},
		{
			name:   "428 no precondition at all",
			target: pagePath,
			body:   revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			want:   http.StatusPreconditionRequired,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, http.Header{}
			},
		},
		{
			name:   "400 a body that named no state",
			target: pagePath,
			body:   `{"anchor":"traitor"}`,
			want:   http.StatusBadRequest,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, current(fixed, pagePath)
			},
		},
		{
			name:   "400 a reveal of a nested callout",
			target: nestedPath,
			body:   revealBody(t, map[string]any{"ordinal": 0, "revealed": true}),
			want:   http.StatusBadRequest,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)
				fixed.write(nestedPath, nestedPage)

				return fixed, current(fixed, nestedPath)
			},
		},
		{
			name:   "404 a secret that is not there",
			target: pagePath,
			body:   revealBody(t, map[string]any{"anchor": "admiral", "revealed": true}),
			want:   http.StatusNotFound,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, current(fixed, pagePath)
			},
		},
		{
			name:   "404 a page that is not there",
			target: "Nowhere.md",
			body:   revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			want:   http.StatusNotFound,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, current(fixed, pagePath)
			},
		},
		{
			name:   "403 a path that leaves the campaign",
			target: "..%2f..%2fetc%2fpasswd",
			body:   revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			want:   http.StatusForbidden,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, current(fixed, pagePath)
			},
		},
		{
			name:   "500 a ledger that refuses",
			target: pagePath,
			body:   revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			want:   http.StatusInternalServerError,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t).failing(errLedgerRefused)
				fixed.write(pagePath, withSecrets)

				return fixed, current(fixed, pagePath)
			},
		},
	}
}

// TestEveryResponseIsPrivateAndUnstorable is the `Cache-Control` half of S-5.4 and of
// ADR 0016, over **every** status this route can answer.
//
// A property of the route rather than of a branch, so it is asserted over the whole status
// set in one place instead of being repeated in each test that happens to look at a
// response. The mutation it exists for is a branch that forgets `writePrivateHeaders` —
// and that mutation would leave every other test in this package green, because a missing
// header is invisible to a status assertion.
//
// Three responses carry material that must never be stored by a shared cache: the 204's
// and the 428's GM-salted validators (a stored GM-salted validator answers "what is this
// page's current version" for a page that may hold secret text), and the 412's page
// source. All of them are responses a proxy would keep.
func TestEveryResponseIsPrivateAndUnstorable(t *testing.T) {
	t.Parallel()

	for _, request := range revealRequests(t) {
		t.Run(request.name, func(t *testing.T) {
			t.Parallel()

			fixed, header := request.build(t)

			recorder := fixed.request(http.MethodPut,
				"/c/"+fixed.campaign.Slug+"/secrets/"+request.target,
				gmRequestor(), header, request.body)

			if recorder.Code != request.want {
				t.Fatalf("%s = %d, want %d; body:\n%s",
					request.name, recorder.Code, request.want, recorder.Body)
			}

			if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Errorf("%s carries Cache-Control: %q, want %q.\n"+
					"Every response on this route is reader-dependent or carries a "+
					"GM-salted validator, and a reverse proxy in front of a self-hosted "+
					"instance is the ordinary deployment — one that stored any of these "+
					"would serve it to a reader who may not have it.",
					request.name, got, "private, no-store")
			}
		})
	}
}

// TestNoRouteLineCarriesASecretBodyOrTitle is the log half of S-12.3, after a
// **successful** reveal.
//
// The table above checks the log for every refusal; this one checks it after the request
// whose log line has the most tempting fields to fill in — the anchor's form, the ordinal,
// the new state. A route that logged the resolved callout's `Title` or `Body` on success
// would satisfy every other assertion in this package and put a secret in a log
// aggregator, which is the one destination S-12.3 exists to keep text out of.
//
// Asserted as an **absence over a positive frame**: the line is captured, and it must both
// carry the facts it is supposed to carry (the campaign, the path, the anchor, the new
// state) and carry none of the page's text. A route that logged nothing would satisfy the
// absence half alone.
func TestNoRouteLineCarriesASecretBodyOrTitle(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	capture := &logCapture{}
	fixed.logger = slog.New(capture)

	recorder := fixed.reveal(pagePath, gmRequestor(), fixed.validatorFor(pagePath),
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("a matched reveal = %d, want 204; body:\n%s", recorder.Code, recorder.Body)
	}

	lines := capture.captured()

	assertNoSecretText(t, "the log after a successful reveal", lines)

	// The frame: the line names what happened, so the absence above is an absence of
	// secret text rather than an absence of a line.
	for _, wanted := range []string{"secrets.revealed", fixed.campaign.Slug, pagePath} {
		if !strings.Contains(lines, wanted) {
			t.Errorf("the success line does not mention %q, so the absence assertion above "+
				"would be satisfied by a route that logged nothing:\n%s", wanted, lines)
		}
	}
}

// TestNoRefusalNamesThePageOrTheSecret is the error half of S-12.3, read from the
// responses rather than from the sentinels.
//
// Every refusal this route raises is a **constant**, and that is not tidiness. An error
// whose text is a function of the caller's input is a channel, and on an endpoint about
// secrets the caller's input is a page. `errUnknownSecret`'s alternative spelling —
// `content.UnknownSecretOrdinalError`, which carries the ordinal and the count — is a
// perfectly reasonable sentence and a leak, because "the page has 3 secrets" is a fact
// about callouts a reader may never have been shown. The content layer's own errors carry
// no path for the same reason, and a route that echoed one would undo that.
//
// **Read from the response** rather than from the package's internals because the response
// is what a reader and a log aggregator actually see: a second implementation of the
// refusal inside a test would be a second thing to keep in step with the first.
func TestNoRefusalNamesThePageOrTheSecret(t *testing.T) {
	t.Parallel()

	for _, request := range revealRequests(t) {
		t.Run(request.name, func(t *testing.T) {
			t.Parallel()

			// The GM's 412 is the one response that carries the page on purpose (S-6.2),
			// and `TestTheConflictBodyCarriesThePageOnlyToTheGm` asserts that positively
			// **and** asserts the absence for every reader the gate refuses. Asserting it
			// here as well would only restate it.
			if request.mayCarryPage {
				t.Skip("the GM's 412 carries the page by requirement; the exception is " +
					"asserted in TestTheConflictBodyCarriesThePageOnlyToTheGm")
			}

			fixed, header := request.build(t)

			recorder := fixed.request(http.MethodPut,
				"/c/"+fixed.campaign.Slug+"/secrets/"+request.target,
				gmRequestor(), header, request.body)

			body := recorder.Body.String()

			// The page's path, which the content layer keeps out of its errors because
			// "a 403 that echoed the attempted path would undo `content.Root`'s
			// confinement in one string". `Vault.md` is the one path a refusal may never
			// name; the traversal row's own path is the caller's input rather than a page
			// in the vault, so it is not asserted here.
			if strings.Contains(body, "Vault.md") {
				t.Errorf("%s names the page in its refusal body: %q.\n"+
					"The path is in the access log, which the middleware wrote before this "+
					"handler ran; a body that repeats it hands the vault's layout to "+
					"whoever asked.", request.name, body)
			}

			// And the callout's own text, which `assertNoSecretText` covers — repeated
			// here in one line so this test reads as the pair it is: a refusal names
			// neither the page nor the callout.
			assertNoSecretText(t, request.name+" (the refusal body)", body)
		})
	}
}
